// Package wstheme serves the per-workspace saved UI theme presets (Django:
// apps/api/plane/app/views/workspace/base.py -> WorkspaceThemeViewSet;
// urls/workspace.py: workspace-themes):
//
//	POST/GET         /workspaces/{slug}/workspace-themes/
//	GET/PATCH/DELETE /workspaces/{slug}/workspace-themes/{pk}/
//
// Quirks pinned by probing the Python reference directly (not obvious from
// reading the view/serializer alone) -- see tests/test_workspace_themes.py
// in the contract suite for the full rationale on each:
//
//   - `permission_classes = [WorkSpaceAdminPermission]` despite the name
//     allows role in {ADMIN=20, MEMBER=15}, not just admins (same quirk as
//     internal/analyticview). A bad/missing workspace slug, or a caller with
//     no sufficient membership there, collapses to DRF's generic
//     `{"detail": "You do not have permission to perform this action."}`
//     403 -- never a 404, and checked before any object lookup.
//   - WorkspaceThemeSerializer is `fields = "__all__"` with
//     `read_only_fields = ["workspace", "actor"]`. The model's
//     `unique_together = ["workspace", "name", "deleted_at"]` makes DRF's
//     UniqueTogetherValidator require every field in that tuple from the
//     client unless it's read_only -- and `deleted_at` is NOT read_only. So
//     POST always 400s with `{"deleted_at": ["This field is required."]}`
//     unless the caller explicitly sends `"deleted_at": null` (or a real
//     timestamp, which creates an already-soft-deleted row). This is
//     replicated verbatim as the create contract, not "fixed".
//   - Because `deleted_at` is writable, PATCH can set it too, soft-deleting
//     the row on the spot (it then 404s on every subsequent GET, since every
//     query here filters `deleted_at is null`).
//   - `colors` is `JSONField(default=dict)`: any JSON value works (object,
//     array, string, ...), just not JSON null (model has no `null=True`, so
//     an explicit `"colors": null` 400s -- not modeled here since it's
//     outside the frozen contract test).
//   - `name` is required, non-blank after trimming, max_length=300.
//   - A duplicate (workspace, name) among non-deleted rows raises
//     IntegrityError, caught by BaseViewSet.handle_exception and rendered as
//     `{"error": "The payload is not valid"}` (400), not a per-field error.
//   - PATCH always touches the row (bumps updated_at, sets updated_by to the
//     caller) even with an empty JSON body, since ModelViewSet.
//     partial_update always calls serializer.save().
//   - list() has no per-actor scoping: every non-deleted theme in the
//     workspace is returned regardless of who created it.
//   - An invalid (non-UUID) `pk` path segment never reaches the view --
//     Django's URL routing 404s first, rendered as the app-wide
//     `{"error": "Page not found."}`, distinct from DRF's
//     `{"detail": "No WorkspaceTheme matches the given query."}` for a
//     well-formed but nonexistent id.
package wstheme

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/workspace-themes/"
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{pk}/", h.retrieve)
	r.Patch(base+"{pk}/", h.update)
	r.Delete(base+"{pk}/", h.destroy)
}

// --- row / response shaping -------------------------------------------------

type row struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	ActorID     uuid.UUID
	Colors      []byte
	CreatedBy   pgtype.UUID
	UpdatedBy   pgtype.UUID
	DeletedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const rowCols = `id, workspace_id, name, actor_id, colors,
	created_by, updated_by, deleted_at, created_at, updated_at`

func scanRow(rw pgx.Row) (row, error) {
	var v row
	err := rw.Scan(&v.ID, &v.WorkspaceID, &v.Name, &v.ActorID, &v.Colors,
		&v.CreatedBy, &v.UpdatedBy, &v.DeletedAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func rawColors(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// resp mirrors WorkspaceThemeSerializer (fields = "__all__").
func resp(v row) map[string]any {
	return map[string]any{
		"id":         v.ID.String(),
		"created_at": v.CreatedAt,
		"updated_at": v.UpdatedAt,
		"deleted_at": v.DeletedAt,
		"name":       v.Name,
		"colors":     rawColors(v.Colors),
		"created_by": dbx.StrPtr(v.CreatedBy),
		"updated_by": dbx.StrPtr(v.UpdatedBy),
		"workspace":  v.WorkspaceID.String(),
		"actor":      v.ActorID.String(),
	}
}

// --- permission / lookup helpers --------------------------------------------

// resolveWorkspace mirrors WorkspaceThemeViewSet's DRF
// `permission_classes = [WorkSpaceAdminPermission]`: role must be ADMIN(20)
// or MEMBER(15). A missing workspace or missing/insufficient membership both
// collapse to DRF's generic permission-denied envelope, never a 404, and are
// checked before any object lookup.
func (h *Handler) resolveWorkspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return uuid.UUID{}, false
	}
	slug := chi.URLParam(r, "slug")
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx, `
		select ws.id
		from workspaces ws
		join workspace_members wm on wm.workspace_id = ws.id and wm.member_id = $2
		where ws.slug = $1 and ws.deleted_at is null and wm.role >= 15
	`, slug, u.ID).Scan(&wsID)
	if err != nil {
		httpx.Detail(w, http.StatusForbidden, "You do not have permission to perform this action.")
		return uuid.UUID{}, false
	}
	return wsID, true
}

func (h *Handler) getRow(ctx context.Context, wsID, id uuid.UUID) (row, error) {
	q := `select ` + rowCols + ` from workspace_themes where id = $1 and workspace_id = $2 and deleted_at is null`
	return scanRow(h.pool.QueryRow(ctx, q, id, wsID))
}

// --- field validation (mirrors DRF CharField / DateTimeField defaults) -----

// validateName mirrors CharField(max_length=300)'s default validation:
// trim_whitespace=True runs first, and the *trimmed* value is what's stored.
// Returns (trimmed, fieldErrors); the caller distinguishes "key absent" from
// "key present but invalid" itself.
func validateName(v string) (string, []string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return v, []string{"This field may not be blank."}
	}
	if len(v) > 300 {
		return v, []string{"Ensure this field has no more than 300 characters."}
	}
	return v, nil
}

const dateFormatErr = "Datetime has wrong format. Use one of these formats instead: YYYY-MM-DDThh:mm[:ss[.uuuuuu]][+HH:MM|-HH:MM|Z]."

// parseDeletedAt mirrors DRF DateTimeField's ISO-8601 parsing. `null` is
// valid (clears the value); an empty string is not (DRF only treats it as
// null when allow_null + the field is also allow_blank, which DateTimeField
// isn't).
func parseDeletedAt(raw json.RawMessage) (*time.Time, bool) {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	if s == nil {
		return nil, true
	}
	if *s == "" {
		return nil, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if ts, err := time.Parse(layout, *s); err == nil {
			return &ts, true
		}
	}
	return nil, false
}

// --- handlers ----------------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	errs := map[string][]string{}

	// name: required.
	name := ""
	if v, present := raw["name"]; !present {
		errs["name"] = []string{"This field is required."}
	} else {
		var s *string
		if err := json.Unmarshal(v, &s); err != nil || s == nil {
			errs["name"] = []string{"This field may not be null."}
		} else {
			trimmed, verrs := validateName(*s)
			if len(verrs) > 0 {
				errs["name"] = verrs
			}
			name = trimmed
		}
	}

	// deleted_at: required (see package doc -- the UniqueTogetherValidator
	// quirk), even though it's not conceptually a "create" input.
	var deletedAt *time.Time
	if v, present := raw["deleted_at"]; !present {
		errs["deleted_at"] = []string{"This field is required."}
	} else {
		ts, valid := parseDeletedAt(v)
		if !valid {
			errs["deleted_at"] = []string{dateFormatErr}
		}
		deletedAt = ts
	}

	// colors: optional, defaults to {}; any JSON value except null.
	colors := []byte("{}")
	if v, present := raw["colors"]; present {
		if string(v) == "null" {
			errs["colors"] = []string{"This field may not be null."}
		} else {
			colors = v
		}
	}

	if len(errs) > 0 {
		httpx.JSON(w, http.StatusBadRequest, errs)
		return
	}

	rw := h.pool.QueryRow(ctx, `
		insert into workspace_themes (workspace_id, name, actor_id, colors, deleted_at, created_by)
		values ($1, $2, $3, $4, $5, $6)
		returning `+rowCols,
		wsID, name, u.ID, colors, deletedAt, u.ID,
	)
	v, err := scanRow(rw)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, resp(v))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspace(ctx, w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(ctx, `
		select `+rowCols+`
		from workspace_themes
		where workspace_id = $1 and deleted_at is null
		order by created_at desc`, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		v, err := scanRow(rows)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
			return
		}
		out = append(out, resp(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspace(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	v, err := h.getRow(ctx, wsID, pk)
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No WorkspaceTheme matches the given query.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(v))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	cur, err := h.getRow(ctx, wsID, pk)
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No WorkspaceTheme matches the given query.")
		return
	}

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	errs := map[string][]string{}

	name := cur.Name
	if v, present := raw["name"]; present {
		var s *string
		if err := json.Unmarshal(v, &s); err != nil || s == nil {
			errs["name"] = []string{"This field may not be null."}
		} else {
			trimmed, verrs := validateName(*s)
			if len(verrs) > 0 {
				errs["name"] = verrs
			}
			name = trimmed
		}
	}

	colors := cur.Colors
	if v, present := raw["colors"]; present {
		if string(v) == "null" {
			errs["colors"] = []string{"This field may not be null."}
		} else {
			colors = v
		}
	}

	// deleted_at is writable (not a read_only_field): PATCH can soft-delete
	// the row directly by sending a real timestamp, same as create.
	deletedAt := cur.DeletedAt
	if v, present := raw["deleted_at"]; present {
		ts, valid := parseDeletedAt(v)
		if !valid {
			errs["deleted_at"] = []string{dateFormatErr}
		} else {
			deletedAt = ts
		}
	}

	// "workspace" and "actor" are read_only_fields: any values sent for them
	// are silently ignored (never read here).

	if len(errs) > 0 {
		httpx.JSON(w, http.StatusBadRequest, errs)
		return
	}

	rw := h.pool.QueryRow(ctx, `
		update workspace_themes
		set name = $1, colors = $2, deleted_at = $3, updated_by = $4, updated_at = now()
		where id = $5 and workspace_id = $6
		returning `+rowCols,
		name, colors, deletedAt, u.ID, pk, wsID,
	)
	v, err := scanRow(rw)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(v))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspace(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	tag, err := h.pool.Exec(ctx, `
		update workspace_themes set deleted_at = now()
		where id = $1 and workspace_id = $2 and deleted_at is null`, pk, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Detail(w, http.StatusNotFound, "No WorkspaceTheme matches the given query.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

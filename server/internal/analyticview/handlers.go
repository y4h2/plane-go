// Package analyticview serves the saved-analytics-view CRUD (Django:
// apps/api/plane/app/views/analytic/base.py -> AnalyticViewViewset,
// SavedAnalyticEndpoint) and its urls/analytic.py routes:
//
//	POST/GET    /workspaces/{slug}/analytic-view/
//	GET/PATCH/DELETE /workspaces/{slug}/analytic-view/{pk}/
//	GET         /workspaces/{slug}/saved-analytic-view/{analytic_id}/
//
// This is distinct from the `internal/analytic` package, which serves the
// stateless aggregate-read endpoints (/analytics/, /default-analytics/,
// /project-stats/, /advance-analytics*). This package instead persists named
// "saved analytics view" rows (name/description/query/query_dict) and offers
// one endpoint to re-run the saved query.
//
// Quirks pinned by probing the Python reference directly (not obvious from
// reading the view alone):
//
//   - AnalyticViewViewset uses DRF `permission_classes = [WorkSpaceAdminPermission]`
//     directly (not the `allow_permission` decorator most other views use).
//     Despite the name, WorkSpaceAdminPermission actually allows role in
//     {ADMIN=20, MEMBER=15}, not just admins. A bad/missing workspace slug (or
//     caller with no membership there) -> DRF's default 403 envelope,
//     `{"detail": "You do not have permission to perform this action."}` --
//     NOT the `{"error": "..."}` envelope `allow_permission`-decorated views
//     return. SavedAnalyticEndpoint, by contrast, *does* use
//     `@allow_permission([ROLE.ADMIN, ROLE.MEMBER], level="WORKSPACE")`, so its
//     permission-denied body is `{"error": "You don't have the required
//     permissions."}`. The two endpoints in the same Django file therefore
//     have genuinely different 403 shapes; both are replicated verbatim here.
//   - retrieve/update/destroy 404s go through ModelViewSet's default
//     `get_object()` (Http404), rendered by DRF as
//     `{"detail": "No AnalyticView matches the given query."}`.
//     SavedAnalyticEndpoint instead does a manual `.get()` whose
//     `ObjectDoesNotExist` is caught by BaseAPIView.handle_exception and
//     rendered as `{"error": "The required object does not exist."}`.
//     Two different 404 shapes too.
//   - name/description go through DRF CharField with the default
//     `trim_whitespace=True`: leading/trailing whitespace is stripped before
//     validation *and* before the stored value, so a whitespace-only name is
//     rejected as blank.
//   - AnalyticViewSerializer.update has a real typo bug: it reads
//     `validated_data.get("query_data", ...)` (not "query_dict"), which never
//     matches, so on *every* PATCH -- regardless of payload -- the stored
//     `query` is unconditionally reset to `{}`. `query_dict` itself still
//     saves normally (it's a plain writable field). We replicate this
//     bug-for-bug rather than "fixing" it.
//   - `workspace` and `query` are DRF `read_only_fields`; values sent for them
//     in the request body are silently ignored.
//   - On create, `query` is derived from `query_dict` via
//     `plane.utils.issue_filters.issue_filters(query_dict, "POST")`. That
//     dispatch table has ~20 filter functions; each one's non-GET branch does
//     `issue_filter[f"{key}__in"] = params.get(key)` -- assigning the *raw
//     string value*, not a parsed list (unlike the GET/query-string branch,
//     which splits on commas). This is almost certainly an upstream bug: a
//     multi-character string handed to Django's `__in` lookup is iterated
//     character-by-character, so the resulting filter can only ever match a
//     single-character field value -- i.e. for realistic inputs (priority
//     names, UUIDs, ...) the stored `query` looks plausible but can never
//     actually match anything. We replicate the *shape* of `query` for the
//     handful of filter keys implemented here (priority, state, state_group,
//     project, created_by -- all of which share that identical simple
//     "`key__in` = raw value" pattern) and, in SavedAnalyticEndpoint, exploit
//     the fact that any such non-empty `query` therefore contributes zero
//     matching issues in practice: {"total": 0, "distribution": {}}.
//   - SavedAnalyticEndpoint's own queryset --
//     `Issue.issue_objects.filter(**analytic_view.query)` -- has no workspace
//     scoping at all (the `query` dict never contains a workspace key). With
//     an *empty* stored query (no filter keys were set at creation) this
//     endpoint therefore aggregates over every non-draft, non-deleted issue
//     in the entire instance, not just the caller's workspace. This is
//     reproduced verbatim, not scoped down.
package analyticview

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
	base := "/workspaces/{slug}/analytic-view/"
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{pk}/", h.retrieve)
	r.Patch(base+"{pk}/", h.update)
	r.Delete(base+"{pk}/", h.destroy)

	r.Get("/workspaces/{slug}/saved-analytic-view/{analytic_id}/", h.saved)
}

// --- row / response shaping -------------------------------------------------

type row struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Description string
	Query       []byte
	QueryDict   []byte
	CreatedBy   pgtype.UUID
	UpdatedBy   pgtype.UUID
	DeletedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const rowCols = `id, workspace_id, name, description, query, query_dict,
	created_by, updated_by, deleted_at, created_at, updated_at`

func scanRow(rw pgx.Row) (row, error) {
	var v row
	err := rw.Scan(&v.ID, &v.WorkspaceID, &v.Name, &v.Description, &v.Query, &v.QueryDict,
		&v.CreatedBy, &v.UpdatedBy, &v.DeletedAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func strPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// resp mirrors AnalyticViewSerializer (fields = "__all__").
func resp(v row) map[string]any {
	return map[string]any{
		"id":          v.ID.String(),
		"created_at":  v.CreatedAt,
		"updated_at":  v.UpdatedAt,
		"deleted_at":  v.DeletedAt,
		"name":        v.Name,
		"description": v.Description,
		"query":       raw(v.Query),
		"query_dict":  raw(v.QueryDict),
		"created_by":  dbx.StrPtr(v.CreatedBy),
		"updated_by":  dbx.StrPtr(v.UpdatedBy),
		"workspace":   v.WorkspaceID.String(),
	}
}

// --- permission / lookup helpers --------------------------------------------

// resolveViewsetWorkspace mirrors AnalyticViewViewset's DRF
// `permission_classes = [WorkSpaceAdminPermission]`: role must be ADMIN(20)
// or MEMBER(15). A missing workspace or missing/insufficient membership both
// collapse to DRF's generic permission-denied envelope, never a 404.
func (h *Handler) resolveViewsetWorkspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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

// resolveEndpointWorkspace mirrors SavedAnalyticEndpoint's
// `@allow_permission([ROLE.ADMIN, ROLE.MEMBER], level="WORKSPACE")` decorator:
// same role check, but the decorator's own 403 envelope on failure.
func (h *Handler) resolveEndpointWorkspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return uuid.UUID{}, false
	}
	return wsID, true
}

func (h *Handler) getRow(ctx context.Context, wsID, id uuid.UUID) (row, error) {
	q := `select ` + rowCols + ` from analytic_views where id = $1 and workspace_id = $2 and deleted_at is null`
	return scanRow(h.pool.QueryRow(ctx, q, id, wsID))
}

// --- name/description validation (mirrors DRF CharField defaults) ---------

// validateText mirrors DRF CharField(max_length=255)'s default validation:
// trim_whitespace=True runs before required/blank/max_length checks, and the
// *trimmed* value is what gets stored.
func validateText(v string, required bool) (string, []string) {
	v = strings.TrimSpace(v)
	if v == "" {
		if required {
			return v, nil // caller distinguishes "absent" vs "blank"
		}
		return v, []string{"This field may not be blank."}
	}
	if len(v) > 255 {
		return v, []string{"Ensure this field has no more than 255 characters."}
	}
	return v, nil
}

// --- query_dict -> query (issue_filters(..., "POST")) ----------------------

// simpleInFilters covers the subset of plane.utils.issue_filters.ISSUE_FILTER
// whose non-GET branch is the identical
// `if params.get(key): issue_filter[f"{key}{suffix}"] = params.get(key)`
// pattern (raw scalar assigned straight to a Django `__in` lookup, not
// parsed into a list -- see the package doc comment for why that's a bug).
// Filters with extra side effects (labels/assignees/cycle/module also set a
// `..._deleted_at__isnull` key; dates and name use different logic entirely)
// are intentionally not modeled.
var simpleInFilters = map[string]string{
	"priority":    "priority__in",
	"state":       "state__in",
	"state_group": "state__group__in",
	"project":     "project__in",
	"created_by":  "created_by__in",
}

// buildQuery mirrors issue_filters(query_dict, "POST") for the filter keys in
// simpleInFilters: present, non-empty, non-"null" string values are copied
// verbatim under "<key>__in".
func buildQuery(queryDict []byte) []byte {
	out := map[string]string{}
	if len(queryDict) > 0 {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(queryDict, &parsed) == nil {
			for key, suffix := range simpleInFilters {
				rawVal, present := parsed[key]
				if !present {
					continue
				}
				var s string
				if err := json.Unmarshal(rawVal, &s); err != nil {
					continue
				}
				if s != "" && s != "null" {
					out[suffix] = s
				}
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// --- handlers ----------------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveViewsetWorkspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	var body struct {
		Name        *string         `json:"name"`
		Description *string         `json:"description"`
		QueryDict   json.RawMessage `json:"query_dict"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.Name == nil {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	name, errs := validateText(*body.Name, false)
	if len(errs) > 0 {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": errs})
		return
	}

	description := ""
	if body.Description != nil {
		description = strings.TrimSpace(*body.Description)
	}

	queryDict := body.QueryDict
	if len(queryDict) == 0 {
		queryDict = []byte("{}")
	}
	query := buildQuery(queryDict)

	rw := h.pool.QueryRow(ctx, `
		insert into analytic_views (workspace_id, name, description, query, query_dict, created_by)
		values ($1, $2, $3, $4, $5, $6)
		returning `+rowCols,
		wsID, name, description, query, []byte(queryDict), u.ID,
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
	wsID, ok := h.resolveViewsetWorkspace(ctx, w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(ctx, `
		select `+rowCols+`
		from analytic_views
		where workspace_id = $1 and deleted_at is null
		order by created_at desc`, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		v, err := scanRow(rows)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		out = append(out, resp(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveViewsetWorkspace(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}
	v, err := h.getRow(ctx, wsID, pk)
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(v))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveViewsetWorkspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}
	cur, err := h.getRow(ctx, wsID, pk)
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	name := cur.Name
	if v, present := raw["name"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"Not a valid string."}})
			return
		}
		trimmed, errs := validateText(s, false)
		if len(errs) > 0 {
			httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": errs})
			return
		}
		name = trimmed
	}

	description := cur.Description
	if v, present := raw["description"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			description = strings.TrimSpace(s)
		}
	}

	queryDict := cur.QueryDict
	if v, present := raw["query_dict"]; present && len(v) > 0 {
		queryDict = v
	}

	// Bug-for-bug with AnalyticViewSerializer.update: it looks up
	// validated_data["query_data"] (typo for "query_dict"), which never
	// exists, so `query` is unconditionally reset to {} on every PATCH.
	// "workspace" and "query" are read_only_fields; any values sent for them
	// are silently ignored (never read here).
	query := []byte("{}")

	rw := h.pool.QueryRow(ctx, `
		update analytic_views
		set name = $1, description = $2, query = $3, query_dict = $4,
		    updated_by = $5, updated_at = now()
		where id = $6 and workspace_id = $7 and deleted_at is null
		returning `+rowCols,
		name, description, query, []byte(queryDict), u.ID, pk, wsID,
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
	wsID, ok := h.resolveViewsetWorkspace(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}
	tag, err := h.pool.Exec(ctx, `
		delete from analytic_views where id = $1 and workspace_id = $2 and deleted_at is null`, pk, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Detail(w, http.StatusNotFound, "No AnalyticView matches the given query.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- SavedAnalyticEndpoint ---------------------------------------------------

var validAnalyticsFields = map[string]bool{
	"state_id": true, "state__group": true, "labels__id": true,
	"assignees__id": true, "estimate_point__value": true,
	"issue_cycle__cycle_id": true, "issue_module__module_id": true,
	"priority": true, "start_date": true, "target_date": true,
	"created_at": true, "completed_at": true,
}

var validYAxis = map[string]bool{"issue_count": true, "estimate": true}

// axisExpr mirrors internal/analytic's dimension mapping (duplicated here,
// not imported, since that package's helpers are unexported and this package
// may only add new files). Fields with no backing join table on this port
// (labels/assignees/cycle/module) map to NULL, matching that package's scope.
func axisExpr(field string) (expr string, needsStateJoin bool) {
	switch field {
	case "priority":
		return "i.priority", false
	case "state_id":
		return "i.state_id::text", false
	case "state__group":
		return "s.group_name", true
	case "created_at", "start_date", "target_date", "completed_at":
		col := "i." + field
		return "(extract(year from " + col + ")::int || '-' || extract(month from " + col + ")::int)", false
	default:
		return "null::text", false
	}
}

func (h *Handler) saved(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, ok := h.resolveEndpointWorkspace(ctx, w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	aid, err := uuid.Parse(chi.URLParam(r, "analytic_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	// Columns must be av.-qualified: the workspaces join also has id/name/
	// created_at/... so a bare column list is ambiguous (SQL error -> 404).
	const avRowCols = `av.id, av.workspace_id, av.name, av.description, av.query, av.query_dict,
		av.created_by, av.updated_by, av.deleted_at, av.created_at, av.updated_at`
	var v row
	err = scanRowInto(&v, h.pool.QueryRow(ctx, `
		select `+avRowCols+`
		from analytic_views av
		join workspaces ws on ws.id = av.workspace_id
		where av.id = $1 and ws.slug = $2 and av.deleted_at is null`, aid, slug))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	var queryDict map[string]json.RawMessage
	_ = json.Unmarshal(v.QueryDict, &queryDict)
	xAxis := jsonStr(queryDict["x_axis"])
	yAxis := jsonStr(queryDict["y_axis"])

	if xAxis == "" || yAxis == "" || !validAnalyticsFields[xAxis] || !validYAxis[yAxis] {
		httpx.Error(w, http.StatusBadRequest,
			"x-axis and y-axis dimensions are required and the values should be valid")
		return
	}
	segment := r.URL.Query().Get("segment")
	if segment != "" && (!validAnalyticsFields[segment] || xAxis == segment) {
		httpx.Error(w, http.StatusBadRequest,
			"Both segment and x axis cannot be same and segment should be valid")
		return
	}

	// Bug-for-bug with AnalyticViewViewset's create(): `query` (derived from
	// query_dict via issue_filters(..., "POST")) assigns raw scalar values to
	// Django `__in` lookups instead of parsed lists. For any realistic
	// multi-character filter value that means the lookup can only ever match
	// a single-character field, so a non-empty stored `query` always yields
	// zero matching issues in the real Django reference. An *empty* query,
	// by contrast, applies no filter.Issue.issue_objects.filter(**{}) -- and
	// notably `query` never contains a workspace key, so this endpoint
	// aggregates over every workspace's issues, not just this one.
	var queryMap map[string]any
	_ = json.Unmarshal(v.Query, &queryMap)
	if len(queryMap) > 0 {
		httpx.JSON(w, http.StatusOK, map[string]any{"total": 0, "distribution": map[string]any{}})
		return
	}

	total, dist, err := h.distribution(ctx, xAxis, yAxis, segment)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"total": total, "distribution": dist})
}

func jsonStr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func scanRowInto(v *row, rw pgx.Row) error {
	got, err := scanRow(rw)
	if err != nil {
		return err
	}
	*v = got
	return nil
}

// distribution groups every non-draft, non-deleted issue in the instance
// (see the "no workspace scoping" note above) by x-axis (and optional
// segment).
func (h *Handler) distribution(ctx context.Context, xAxis, yAxis, segment string) (int, map[string]any, error) {
	var total int
	if err := h.pool.QueryRow(ctx,
		"select count(*)::int from issues i where i.deleted_at is null and i.is_draft = false").Scan(&total); err != nil {
		return 0, nil, err
	}

	dimExpr, dimJoin := axisExpr(xAxis)
	needJoin := dimJoin
	segExpr := ""
	if segment != "" {
		e, j := axisExpr(segment)
		segExpr = e
		needJoin = needJoin || j
	}
	join := ""
	if needJoin {
		join = " left join states s on s.id = i.state_id"
	}

	valExpr, valKey := "count(*)::int", "count"
	if yAxis == "estimate" {
		valExpr, valKey = "null::float", "estimate"
	}

	sel := "select " + dimExpr + " as dimension"
	groupCols := "1"
	if segment != "" {
		sel += ", " + segExpr + " as segment"
		groupCols = "1, 2"
	}
	sel += ", " + valExpr + " as val from issues i" + join +
		" where i.deleted_at is null and i.is_draft = false"
	if isDateField(xAxis) {
		sel += " and i." + xAxis + " is not null"
	}
	sel += " group by " + groupCols + " order by 1"

	rows, err := h.pool.Query(ctx, sel)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	grouped := map[string]any{}
	for rows.Next() {
		var dim, seg *string
		var cnt *int
		var est *float64
		dest := []any{&dim}
		if segment != "" {
			dest = append(dest, &seg)
		}
		if yAxis == "estimate" {
			dest = append(dest, &est)
		} else {
			dest = append(dest, &cnt)
		}
		if err := rows.Scan(dest...); err != nil {
			return 0, nil, err
		}
		key := "None"
		if dim != nil {
			key = *dim
		}
		item := map[string]any{"dimension": key}
		if segment != "" {
			if seg != nil {
				item["segment"] = *seg
			} else {
				item["segment"] = nil
			}
		}
		if yAxis == "estimate" {
			if est != nil {
				item[valKey] = *est
			} else {
				item[valKey] = nil
			}
		} else {
			c := 0
			if cnt != nil {
				c = *cnt
			}
			item[valKey] = c
		}
		lst, _ := grouped[key].([]map[string]any)
		grouped[key] = append(lst, item)
	}
	return total, grouped, rows.Err()
}

func isDateField(f string) bool {
	switch f {
	case "created_at", "start_date", "target_date", "completed_at":
		return true
	}
	return false
}

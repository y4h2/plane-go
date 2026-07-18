// Package apitoken implements the user-scoped API token endpoints
// (Django: apps/api/plane/app/urls/api.py -> ApiTokenEndpoint).
//
// Note: despite living under a "workspace-scoped" umbrella of modules, the
// Django reference mounts this endpoint at plain "/api/users/api-tokens/"
// (and "/api/users/api-tokens/<uuid:pk>/") with no workspace in the path.
// The underlying APIToken model has a nullable workspace FK, but this view
// never sets it on create, so it is always null in responses here. This
// package therefore does not resolve or require a workspace at all.
package apitoken

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	base := "/users/api-tokens/"
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{pk}/", h.retrieve)
	r.Patch(base+"{pk}/", h.update)
	r.Delete(base+"{pk}/", h.destroy)
}

type apiToken struct {
	ID               uuid.UUID
	Label            string
	Description      string
	IsActive         bool
	LastUsed         *time.Time
	Token            string
	UserID           uuid.UUID
	UserType         int16
	WorkspaceID      *uuid.UUID
	ExpiredAt        *time.Time
	IsService        bool
	AllowedRateLimit string
	CreatedBy        *uuid.UUID
	UpdatedBy        *uuid.UUID
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const tokenCols = `id, label, description, is_active, last_used, token, user_id, user_type,
	workspace_id, expired_at, is_service, allowed_rate_limit, created_by, updated_by,
	deleted_at, created_at, updated_at`

func scanToken(row pgx.Row) (apiToken, error) {
	var t apiToken
	err := row.Scan(&t.ID, &t.Label, &t.Description, &t.IsActive, &t.LastUsed, &t.Token,
		&t.UserID, &t.UserType, &t.WorkspaceID, &t.ExpiredAt, &t.IsService, &t.AllowedRateLimit,
		&t.CreatedBy, &t.UpdatedBy, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// writeResp mirrors APITokenSerializer (fields = "__all__"): includes the raw
// token value and the raw is_active column. Used for create/update responses,
// where the token is visible "only while creating" per the Django comment
// (though PATCH's response shape matches too, since it reuses the same
// serializer).
func writeResp(t apiToken) map[string]any {
	return map[string]any{
		"id":                 t.ID.String(),
		"created_at":         t.CreatedAt,
		"updated_at":         t.UpdatedAt,
		"deleted_at":         t.DeletedAt,
		"label":              t.Label,
		"description":        t.Description,
		"is_active":          t.IsActive,
		"last_used":          t.LastUsed,
		"token":              t.Token,
		"user_type":          int(t.UserType),
		"expired_at":         t.ExpiredAt,
		"is_service":         t.IsService,
		"allowed_rate_limit": t.AllowedRateLimit,
		"created_by":         strPtr(t.CreatedBy),
		"updated_by":         strPtr(t.UpdatedBy),
		"user":               t.UserID.String(),
		"workspace":          strPtr(t.WorkspaceID),
	}
}

// readResp mirrors APITokenReadSerializer (exclude "token", is_active
// computed from expired_at rather than the stored column).
func readResp(t apiToken) map[string]any {
	isActive := true
	if t.ExpiredAt != nil {
		isActive = time.Now().UTC().Before(*t.ExpiredAt)
	}
	return map[string]any{
		"id":                 t.ID.String(),
		"is_active":          isActive,
		"created_at":         t.CreatedAt,
		"updated_at":         t.UpdatedAt,
		"deleted_at":         t.DeletedAt,
		"label":              t.Label,
		"description":        t.Description,
		"last_used":          t.LastUsed,
		"user_type":          int(t.UserType),
		"expired_at":         t.ExpiredAt,
		"is_service":         t.IsService,
		"allowed_rate_limit": t.AllowedRateLimit,
		"created_by":         strPtr(t.CreatedBy),
		"updated_by":         strPtr(t.UpdatedBy),
		"user":               t.UserID.String(),
		"workspace":          strPtr(t.WorkspaceID),
	}
}

func strPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func generateToken() string { return "plane_api_" + randHex(16) }
func generateLabel() string { return randHex(16) }

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	label := generateLabel()
	if v, present := raw["label"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			label = s
		}
	}
	description := ""
	if v, present := raw["description"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			description = s
		}
	}
	var expiredAt *time.Time
	if v, present := raw["expired_at"]; present {
		var s *string
		if err := json.Unmarshal(v, &s); err == nil && s != nil {
			if ts, err := time.Parse(time.RFC3339, *s); err == nil {
				expiredAt = &ts
			}
		}
	}

	userType := int16(0)
	if u.IsBot {
		userType = 1
	}

	row := h.pool.QueryRow(ctx, `
		insert into api_tokens (label, description, token, user_id, user_type, expired_at, created_by)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning `+tokenCols,
		label, description, generateToken(), u.ID, userType, expiredAt, u.ID,
	)
	t, err := scanToken(row)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	httpx.JSON(w, http.StatusCreated, writeResp(t))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	rows, err := h.pool.Query(ctx, `
		select `+tokenCols+`
		from api_tokens
		where user_id = $1 and is_service = false and deleted_at is null
		order by created_at desc`, u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
			return
		}
		out = append(out, readResp(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getOwned(ctx context.Context, userID, pk uuid.UUID) (apiToken, error) {
	row := h.pool.QueryRow(ctx, `
		select `+tokenCols+`
		from api_tokens
		where id = $1 and user_id = $2 and is_service = false and deleted_at is null`, pk, userID)
	return scanToken(row)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	t, err := h.getOwned(ctx, u.ID, pk)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, readResp(t))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	cur, err := h.getOwned(ctx, u.ID, pk)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	label := cur.Label
	if v, present := raw["label"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			label = s
		}
	}
	description := cur.Description
	if v, present := raw["description"]; present {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			description = s
		}
	}
	// All other fields (token, expired_at, workspace, user, is_active,
	// last_used, user_type, allowed_rate_limit) are read-only on
	// APITokenSerializer, matching Django: any values sent for them are
	// silently ignored.

	row := h.pool.QueryRow(ctx, `
		update api_tokens
		set label = $1, description = $2, updated_by = $3, updated_at = now()
		where id = $4 and user_id = $5 and is_service = false and deleted_at is null
		returning `+tokenCols,
		label, description, u.ID, pk, u.ID,
	)
	t, err := scanToken(row)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	httpx.JSON(w, http.StatusOK, writeResp(t))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found.")
		return
	}
	tag, err := h.pool.Exec(ctx, `
		delete from api_tokens
		where id = $1 and user_id = $2 and is_service = false and deleted_at is null`, pk, u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

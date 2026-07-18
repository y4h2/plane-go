// Package userprops serves the per-user per-project properties endpoint
// (issue-view layout/filters). GET is get-or-create; PATCH updates.
package userprops

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/projects/{project_id}/user-properties/"
	r.Get(base, h.get)
	r.Patch(base, h.update)
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func resp(p gen.ProjectUserProperty) map[string]any {
	return map[string]any{
		"id":                 p.ID.String(),
		"project":            p.ProjectID.String(),
		"workspace":          p.WorkspaceID.String(),
		"user":               p.UserID.String(),
		"filters":            raw(p.Filters),
		"display_filters":    raw(p.DisplayFilters),
		"display_properties": raw(p.DisplayProperties),
		"created_at":         p.CreatedAt,
		"updated_at":         p.UpdatedAt,
	}
}

// getOrCreate returns the row for (project, user), creating it on first access.
func (h *Handler) getOrCreate(ctx context.Context, wsID, pid, uid uuid.UUID) (gen.ProjectUserProperty, error) {
	p, err := h.q.GetUserProps(ctx, gen.GetUserPropsParams{ProjectID: pid, UserID: uid})
	if err == nil {
		return p, nil
	}
	return h.q.CreateUserProps(ctx, gen.CreateUserPropsParams{WorkspaceID: wsID, ProjectID: pid, UserID: uid})
}

func (h *Handler) scope(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, false
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.UUID{}, uuid.UUID{}, uuid.UUID{}, false
	}
	u, _ := auth.UserFrom(ctx)
	return ws.ID, pid, u.ID, true
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, uid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	p, err := h.getOrCreate(ctx, wsID, pid, uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(p))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, uid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	current, err := h.getOrCreate(ctx, wsID, pid, uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var body struct {
		Filters           json.RawMessage `json:"filters"`
		DisplayFilters    json.RawMessage `json:"display_filters"`
		DisplayProperties json.RawMessage `json:"display_properties"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	filters, df, dp := current.Filters, current.DisplayFilters, current.DisplayProperties
	if len(body.Filters) > 0 {
		filters = body.Filters
	}
	if len(body.DisplayFilters) > 0 {
		df = body.DisplayFilters
	}
	if len(body.DisplayProperties) > 0 {
		dp = body.DisplayProperties
	}
	updated, err := h.q.UpdateUserProps(ctx, gen.UpdateUserPropsParams{
		ProjectID: pid, UserID: uid, Filters: filters, DisplayFilters: df, DisplayProperties: dp,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(updated))
}

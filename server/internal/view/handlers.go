// Package view serves saved views at both workspace and project scope. The
// filter/query JSON blobs are persisted (the frontend saves filters into views).
package view

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/views/", h.listWorkspace)
	r.Post("/workspaces/{slug}/views/", h.createWorkspace)
	r.Get("/workspaces/{slug}/views/{view_id}/", h.get)
	r.Patch("/workspaces/{slug}/views/{view_id}/", h.update)
	r.Delete("/workspaces/{slug}/views/{view_id}/", h.destroy)
	r.Get("/workspaces/{slug}/projects/{project_id}/views/", h.listProject)
	r.Post("/workspaces/{slug}/projects/{project_id}/views/", h.createProject)
	r.Get("/workspaces/{slug}/projects/{project_id}/views/{view_id}/", h.get)
	r.Patch("/workspaces/{slug}/projects/{project_id}/views/{view_id}/", h.update)
	r.Delete("/workspaces/{slug}/projects/{project_id}/views/{view_id}/", h.destroy)
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func orDefault(b json.RawMessage) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

func viewResp(v gen.View) map[string]any {
	return map[string]any{
		"id":                 v.ID.String(),
		"name":               v.Name,
		"description":        v.Description,
		"access":             int(v.Access),
		"query":              raw(v.Query),
		"filters":            raw(v.Filters),
		"display_filters":    raw(v.DisplayFilters),
		"display_properties": raw(v.DisplayProperties),
		"logo_props":         raw(v.LogoProps),
		"rich_filters":       json.RawMessage("{}"),
		"sort_order":         httpx.Float(v.SortOrder),
		"is_locked":          v.IsLocked,
		"workspace":          v.WorkspaceID.String(),
		"project":            dbx.StrPtr(v.ProjectID),
		"owned_by":           dbx.StrPtr(v.OwnedBy),
		"created_by":         dbx.StrPtr(v.CreatedBy),
		"updated_by":         dbx.StrPtr(v.UpdatedBy),
		"created_at":         v.CreatedAt,
		"updated_at":         v.UpdatedAt,
		"archived_at":        nil,
		"deleted_at":         v.DeletedAt,
	}
}

type viewBody struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Access            *int            `json:"access"`
	Query             json.RawMessage `json:"query"`
	Filters           json.RawMessage `json:"filters"`
	DisplayFilters    json.RawMessage `json:"display_filters"`
	DisplayProperties json.RawMessage `json:"display_properties"`
}

func (h *Handler) createWorkspace(w http.ResponseWriter, r *http.Request) {
	h.doCreate(w, r, pgtype.UUID{})
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	h.doCreate(w, r, dbx.PgUUID(pid))
}

func (h *Handler) doCreate(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var b viewBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	access := int16(1)
	if b.Access != nil {
		access = int16(*b.Access)
	}
	v, err := h.q.CreateView(ctx, gen.CreateViewParams{
		WorkspaceID: ws.ID, ProjectID: projectID, Name: strings.TrimSpace(b.Name),
		Description: b.Description, Access: access,
		Query: orDefault(b.Query), Filters: orDefault(b.Filters),
		DisplayFilters: orDefault(b.DisplayFilters), DisplayProperties: orDefault(b.DisplayProperties),
		OwnedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, viewResp(v))
}

func (h *Handler) listWorkspace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, r)
	if !ok {
		return
	}
	rows, err := h.q.ListWorkspaceViews(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	h.writeList(w, rows)
}

func (h *Handler) listProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.workspace(ctx, w, r); !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	rows, err := h.q.ListProjectViews(ctx, dbx.PgUUID(pid))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	h.writeList(w, rows)
}

func (h *Handler) writeList(w http.ResponseWriter, rows []gen.View) {
	out := make([]map[string]any, 0, len(rows))
	for _, v := range rows {
		out = append(out, viewResp(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, ok := h.view(ctx, w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, viewResp(v))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, ok := h.view(ctx, w, r)
	if !ok {
		return
	}
	var b viewBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	name, desc := v.Name, v.Description
	if strings.TrimSpace(b.Name) != "" {
		name = b.Name
	}
	if b.Description != "" {
		desc = b.Description
	}
	access := v.Access
	if b.Access != nil {
		access = int16(*b.Access)
	}
	filters, query := v.Filters, v.Query
	if len(b.Filters) > 0 {
		filters = b.Filters
	}
	if len(b.Query) > 0 {
		query = b.Query
	}
	df, dp := v.DisplayFilters, v.DisplayProperties
	if len(b.DisplayFilters) > 0 {
		df = b.DisplayFilters
	}
	if len(b.DisplayProperties) > 0 {
		dp = b.DisplayProperties
	}
	u, _ := auth.UserFrom(ctx)
	updated, err := h.q.UpdateView(ctx, gen.UpdateViewParams{
		ID: v.ID, WorkspaceID: v.WorkspaceID, Name: name, Description: desc, Access: access,
		Filters: filters, Query: query, DisplayFilters: df, DisplayProperties: dp, UpdatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, viewResp(updated))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, ok := h.view(ctx, w, r)
	if !ok {
		return
	}
	if err := h.q.SoftDeleteView(ctx, gen.SoftDeleteViewParams{ID: v.ID, WorkspaceID: v.WorkspaceID}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ---------------------------------------------------------------

func (h *Handler) workspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.Workspace, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, false
	}
	return ws, true
}

func (h *Handler) view(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.View, bool) {
	ws, ok := h.workspace(ctx, w, r)
	if !ok {
		return gen.View{}, false
	}
	vid, err := uuid.Parse(chi.URLParam(r, "view_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.View{}, false
	}
	v, err := h.q.GetView(ctx, gen.GetViewParams{ID: vid, WorkspaceID: ws.ID})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.View{}, false
	}
	return v, true
}

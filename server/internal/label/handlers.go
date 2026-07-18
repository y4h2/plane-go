// Package label serves the issue-label endpoints (project-scoped CRUD plus a
// workspace-wide list). Response is the exact 7-key LabelSerializer shape.
package label

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/issue-labels/", h.create)
	r.Get("/workspaces/{slug}/projects/{project_id}/issue-labels/", h.list)
	r.Get("/workspaces/{slug}/projects/{project_id}/issue-labels/{label_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/issue-labels/{label_id}/", h.update)
	r.Put("/workspaces/{slug}/projects/{project_id}/issue-labels/{label_id}/", h.update)
	r.Delete("/workspaces/{slug}/projects/{project_id}/issue-labels/{label_id}/", h.destroy)
	r.Get("/workspaces/{slug}/labels/", h.workspaceLabels)
}

func labelResp(l gen.Label) map[string]any {
	return map[string]any{
		"id":           l.ID.String(),
		"name":         l.Name,
		"color":        l.Color,
		"parent":       dbx.StrPtr(l.ParentID),
		"project_id":   l.ProjectID.String(),
		"workspace_id": l.WorkspaceID.String(),
		"sort_order":   httpx.Float(l.SortOrder),
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	if exists, _ := h.q.LabelNameExists(ctx, gen.LabelNameExistsParams{ProjectID: pid, Lower: name}); exists {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"LABEL_NAME_ALREADY_EXISTS"}})
		return
	}
	sortOrder, err := h.q.NextLabelSortOrder(ctx, pid)
	if err != nil {
		sortOrder = 65535
	}
	l, err := h.q.CreateLabel(ctx, gen.CreateLabelParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: name, Color: body.Color, SortOrder: sortOrder,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, labelResp(l))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	labels, err := h.q.ListLabels(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(labels))
	for _, l := range labels {
		out = append(out, labelResp(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	l, found := h.label(ctx, w, pid, chi.URLParam(r, "label_id"))
	if !found {
		return
	}
	httpx.JSON(w, http.StatusOK, labelResp(l))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	l, found := h.label(ctx, w, pid, chi.URLParam(r, "label_id"))
	if !found {
		return
	}
	var body struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, color := l.Name, l.Color
	if body.Name != nil {
		name = *body.Name
	}
	if body.Color != nil {
		color = *body.Color
	}
	updated, err := h.q.UpdateLabel(ctx, gen.UpdateLabelParams{ID: l.ID, ProjectID: pid, Name: name, Color: color})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, labelResp(updated))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	l, found := h.label(ctx, w, pid, chi.URLParam(r, "label_id"))
	if !found {
		return
	}
	if err := h.q.DeleteLabel(ctx, gen.DeleteLabelParams{ID: l.ID, ProjectID: pid}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) workspaceLabels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	labels, err := h.q.ListWorkspaceLabels(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(labels))
	for _, l := range labels {
		out = append(out, labelResp(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- helpers ---------------------------------------------------------------

func (h *Handler) resolve(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.Workspace, uuid.UUID, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, uuid.UUID{}, false
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, uuid.UUID{}, false
	}
	return ws, pid, true
}

func (h *Handler) label(ctx context.Context, w http.ResponseWriter, pid uuid.UUID, idStr string) (gen.Label, bool) {
	lid, err := uuid.Parse(idStr)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Label{}, false
	}
	l, err := h.q.GetLabel(ctx, gen.GetLabelParams{ID: lid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Label{}, false
	}
	return l, true
}

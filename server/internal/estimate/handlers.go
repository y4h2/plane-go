// Package estimate serves project estimates (with nested estimate points).
// Create returns 200 (a deliberate quirk shared with state).
package estimate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/projects/{project_id}/estimates/"
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{estimate_id}/", h.retrieve)
	r.Patch(base+"{estimate_id}/", h.update)
	r.Delete(base+"{estimate_id}/", h.destroy)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	eid, err := uuid.Parse(chi.URLParam(r, "estimate_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	cur, err := h.q.GetEstimate(ctx, gen.GetEstimateParams{ID: eid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Estimate struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"estimate"`
		EstimatePoints []struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		} `json:"estimate_points"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// Python guards: PATCH with no estimate_points is a 400.
	if len(body.EstimatePoints) == 0 {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "Estimate points are required"})
		return
	}
	name, typ := cur.Name, cur.Type
	if body.Estimate.Name != "" {
		name = body.Estimate.Name
	}
	if body.Estimate.Type != "" {
		typ = body.Estimate.Type
	}
	e, err := h.q.UpdateEstimate(ctx, gen.UpdateEstimateParams{ID: eid, Name: name, Type: typ, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	for _, p := range body.EstimatePoints {
		if pointID, err := uuid.Parse(p.ID); err == nil {
			_ = h.q.UpdateEstimatePointValue(ctx, gen.UpdateEstimatePointValueParams{ID: pointID, Value: p.Value})
		}
	}
	pts, _ := h.q.ListEstimatePoints(ctx, e.ID)
	httpx.JSON(w, http.StatusOK, estimateResp(e, pts))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	eid, err := uuid.Parse(chi.URLParam(r, "estimate_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteEstimate(ctx, gen.DeleteEstimateParams{ID: eid, ProjectID: pid})
	w.WriteHeader(http.StatusNoContent)
}

func pointResp(p gen.EstimatePoint) map[string]any {
	return map[string]any{
		"id":         p.ID.String(),
		"key":        int(p.Key),
		"value":      p.Value,
		"description": p.Description,
		"estimate":   p.EstimateID.String(),
		"project":    p.ProjectID.String(),
		"workspace":  p.WorkspaceID.String(),
		"created_at": p.CreatedAt,
		"updated_at": p.UpdatedAt,
	}
}

func estimateResp(e gen.Estimate, points []gen.EstimatePoint) map[string]any {
	pts := make([]map[string]any, 0, len(points))
	for _, p := range points {
		pts = append(pts, pointResp(p))
	}
	return map[string]any{
		"id":          e.ID.String(),
		"name":        e.Name,
		"type":        e.Type,
		"description": e.Description,
		"last_used":   e.LastUsed,
		"points":      pts,
		"project":     e.ProjectID.String(),
		"workspace":   e.WorkspaceID.String(),
		"created_at":  e.CreatedAt,
		"updated_at":  e.UpdatedAt,
		"created_by":  dbx.StrPtr(e.CreatedBy),
		"updated_by":  dbx.StrPtr(e.UpdatedBy),
		"deleted_at":  e.DeletedAt,
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Estimate struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"estimate"`
		EstimatePoints []struct {
			Key   int    `json:"key"`
			Value string `json:"value"`
		} `json:"estimate_points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if strings.TrimSpace(body.Estimate.Name) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	typ := body.Estimate.Type
	if typ == "" {
		typ = "points"
	}
	e, err := h.q.CreateEstimate(ctx, gen.CreateEstimateParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: strings.TrimSpace(body.Estimate.Name),
		Type: typ, Description: body.Estimate.Description, CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	points := make([]gen.EstimatePoint, 0, len(body.EstimatePoints))
	for _, ep := range body.EstimatePoints {
		p, err := h.q.CreateEstimatePoint(ctx, gen.CreateEstimatePointParams{
			WorkspaceID: ws.ID, ProjectID: pid, EstimateID: e.ID,
			Key: int32(ep.Key), Value: ep.Value, CreatedBy: dbx.PgUUID(u.ID),
		})
		if err == nil {
			points = append(points, p)
		}
	}
	httpx.JSON(w, http.StatusOK, estimateResp(e, points)) // quirk: 200, not 201
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	estimates, err := h.q.ListEstimates(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(estimates))
	for _, e := range estimates {
		pts, _ := h.q.ListEstimatePoints(ctx, e.ID)
		out = append(out, estimateResp(e, pts))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	eid, err := uuid.Parse(chi.URLParam(r, "estimate_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	e, err := h.q.GetEstimate(ctx, gen.GetEstimateParams{ID: eid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	pts, _ := h.q.ListEstimatePoints(ctx, e.ID)
	httpx.JSON(w, http.StatusOK, estimateResp(e, pts))
}

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

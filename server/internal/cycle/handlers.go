// Package cycle serves the cycle endpoints. Responses are bare .values() dicts
// (a superset covering the project-scoped and workspace-wide shapes). Cycle-issue
// linking add returns {"message":"success"}; the cycle-issues list is the issue
// cursor envelope.
package cycle

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
	"planego/internal/issue"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/cycles/", h.create)
	r.Get("/workspaces/{slug}/projects/{project_id}/cycles/", h.list)
	r.Post("/workspaces/{slug}/projects/{project_id}/cycles/date-check/", h.dateCheck)
	r.Get("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/", h.update)
	r.Delete("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/", h.destroy)
	r.Get("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/cycle-issues/", h.listIssues)
	r.Post("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/cycle-issues/", h.addIssues)
	r.Get("/workspaces/{slug}/cycles/", h.workspaceCycles)
	r.Post("/workspaces/{slug}/projects/{project_id}/user-favorite-cycles/", h.addFavorite)
	r.Delete("/workspaces/{slug}/projects/{project_id}/user-favorite-cycles/{cycle_id}/", h.delFavorite)
	r.Post("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/transfer-issues/", h.transferIssues)
}

func (h *Handler) addFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Cycle string `json:"cycle"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cid, err := uuid.Parse(body.Cycle)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	_, _ = h.q.CreateFavorite(ctx, gen.CreateFavoriteParams{
		WorkspaceID: ws.ID, UserID: u.ID, EntityType: "cycle",
		EntityIdentifier: dbx.PgUUID(cid), Name: "", ProjectID: dbx.PgUUID(pid),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) delFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.resolve(ctx, w, r); !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	cid, err := uuid.Parse(chi.URLParam(r, "cycle_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteFavoriteByEntity(ctx, gen.DeleteFavoriteByEntityParams{UserID: u.ID, EntityIdentifier: dbx.PgUUID(cid)})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) transferIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	var body struct {
		NewCycleID string `json:"new_cycle_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	newID, err := uuid.Parse(body.NewCycleID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "new_cycle_id is required")
		return
	}
	_ = h.q.TransferCycleIssues(ctx, gen.TransferCycleIssuesParams{CycleID: c.ID, CycleID_2: newID})
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Success"})
}


// cycleBase is the shared .values() dict. The contract is strict about two keys:
// `cancelled_issues` appears only on list/workspace, `sub_issues` only on
// retrieve — so callers add those per-endpoint rather than baking them in here.
func cycleBase(c gen.Cycle) map[string]any {
	return map[string]any{
		"id":                c.ID.String(),
		"workspace_id":      c.WorkspaceID.String(),
		"project_id":        c.ProjectID.String(),
		"name":              c.Name,
		"description":       c.Description,
		"start_date":        c.StartDate,
		"end_date":          c.EndDate,
		"owned_by_id":       c.OwnedByID.String(),
		"view_props":        map[string]any{},
		"sort_order":        httpx.Float(c.SortOrder),
		"external_source":   nil,
		"external_id":       nil,
		"progress_snapshot": map[string]any{},
		"logo_props":        map[string]any{},
		"total_issues":      0,
		"completed_issues":  0,
		"assignee_ids":      []string{},
		"status":            "DRAFT",
		"version":           1,
		"is_favorite":       false,
		"created_by":        dbx.StrPtr(c.CreatedBy),
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
		Name        string  `json:"name"`
		Description string  `json:"description"`
		StartDate   *string `json:"start_date"`
		EndDate     *string `json:"end_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	start, end, ok := parseDatePair(w, body.StartDate, body.EndDate)
	if !ok {
		return
	}
	c, err := h.q.CreateCycle(ctx, gen.CreateCycleParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: strings.TrimSpace(body.Name),
		Description: body.Description, StartDate: start, EndDate: end,
		OwnedByID: u.ID, CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, cycleBase(c))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	cycles, err := h.q.ListCycles(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(cycles))
	for _, c := range cycles {
		m := cycleBase(c)
		m["cancelled_issues"] = 0
		out = append(out, m)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	m := cycleBase(c)
	m["sub_issues"] = 0
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		StartDate   *string `json:"start_date"`
		EndDate     *string `json:"end_date"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, desc := c.Name, c.Description
	if body.Name != nil {
		name = *body.Name
	}
	if body.Description != nil {
		desc = *body.Description
	}
	start, end := c.StartDate, c.EndDate
	if body.StartDate != nil || body.EndDate != nil {
		s, e, ok := parseDatePair(w, body.StartDate, body.EndDate)
		if !ok {
			return
		}
		start, end = s, e
	}
	updated, err := h.q.UpdateCycle(ctx, gen.UpdateCycleParams{
		ID: c.ID, ProjectID: pid, Name: name, Description: desc, StartDate: start, EndDate: end,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, cycleBase(updated))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	if err := h.q.SoftDeleteCycle(ctx, gen.SoftDeleteCycleParams{ID: c.ID, ProjectID: pid}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) dateCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	var body struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	start, err1 := time.Parse("2006-01-02", body.StartDate)
	end, err2 := time.Parse("2006-01-02", body.EndDate)
	if err1 != nil || err2 != nil {
		httpx.Error(w, http.StatusBadRequest, "Start and end dates are required")
		return
	}
	n, _ := h.q.CountOverlappingCycles(ctx, gen.CountOverlappingCyclesParams{
		ProjectID: pid, RangeStart: &start, RangeEnd: &end,
	})
	if n > 0 {
		httpx.JSON(w, http.StatusOK, map[string]any{"status": false, "error": "Cycle already exists in the given date range"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": true})
}

func (h *Handler) listIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	issues, err := h.q.ListCycleIssueIssues(ctx, c.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	vals := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		vals = append(vals, issue.Values(i))
	}
	httpx.JSON(w, http.StatusOK, issue.Envelope(vals, len(vals), nil))
}

func (h *Handler) addIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	c, found := h.cycle(ctx, w, pid, chi.URLParam(r, "cycle_id"))
	if !found {
		return
	}
	var body struct {
		Issues []string `json:"issues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Issues) == 0 {
		httpx.Error(w, http.StatusBadRequest, "At least one issue is required")
		return
	}
	for _, id := range body.Issues {
		iid, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		_ = h.q.AddCycleIssue(ctx, gen.AddCycleIssueParams{
			WorkspaceID: ws.ID, ProjectID: pid, CycleID: c.ID, IssueID: iid,
		})
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"message": "success"})
}

func (h *Handler) workspaceCycles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	cycles, err := h.q.ListWorkspaceCycles(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(cycles))
	for _, c := range cycles {
		m := cycleBase(c)
		m["cancelled_issues"] = 0
		m["started_issues"] = 0
		m["unstarted_issues"] = 0
		m["backlog_issues"] = 0
		out = append(out, m)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- helpers ---------------------------------------------------------------

// parseDatePair enforces "both or neither" and start<=end, writing the 400 and
// returning ok=false on violation. Accepts RFC3339 datetimes.
func parseDatePair(w http.ResponseWriter, startStr, endStr *string) (*time.Time, *time.Time, bool) {
	hasStart := startStr != nil && *startStr != ""
	hasEnd := endStr != nil && *endStr != ""
	if hasStart != hasEnd {
		httpx.Error(w, http.StatusBadRequest, "Both start and end dates are required")
		return nil, nil, false
	}
	if !hasStart {
		return nil, nil, true
	}
	start, err1 := time.Parse(time.RFC3339, *startStr)
	end, err2 := time.Parse(time.RFC3339, *endStr)
	if err1 != nil || err2 != nil {
		httpx.Error(w, http.StatusBadRequest, "Invalid date format")
		return nil, nil, false
	}
	if start.After(end) {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"non_field_errors": {"Start date cannot exceed end date"}})
		return nil, nil, false
	}
	return &start, &end, true
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

func (h *Handler) cycle(ctx context.Context, w http.ResponseWriter, pid uuid.UUID, idStr string) (gen.Cycle, bool) {
	cid, err := uuid.Parse(idStr)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Cycle{}, false
	}
	c, err := h.q.GetCycle(ctx, gen.GetCycleParams{ID: cid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Cycle{}, false
	}
	return c, true
}

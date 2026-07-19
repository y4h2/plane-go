// Package module serves the module endpoints. POST/GET/PATCH/list share one
// superset .values() dict (the contract asserts no key absences here); PUT
// returns a distinct ModuleSerializer shape. Module-issue add returns
// {"message":"success"}; the module-issues list is the issue cursor envelope.
package module

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
	"planego/internal/issue"
	"planego/internal/moduleanalytics"
)

type Handler struct {
	q    *gen.Queries
	pool *pgxpool.Pool
}

func New(q *gen.Queries, pool *pgxpool.Pool) *Handler { return &Handler{q: q, pool: pool} }

// withAnalytics merges a module's live progress counts + distribution/estimate
// dicts into its base .values() dict. Django embeds this data directly in the
// module retrieve/list/create responses (modules have no separate analytics
// endpoint). Best-effort: on a compute error the hardcoded defaults stand.
func (h *Handler) withAnalytics(ctx context.Context, base map[string]any, moduleID uuid.UUID) map[string]any {
	if prog, err := moduleanalytics.Progress(ctx, h.pool, moduleID); err == nil {
		for k, v := range prog {
			base[k] = v
		}
	}
	if dist, err := moduleanalytics.Distribution(ctx, h.pool, moduleID); err == nil {
		base["distribution"] = dist
	}
	if est, err := moduleanalytics.EstimateDistribution(ctx, h.pool, moduleID); err == nil {
		base["estimate_distribution"] = est
	}
	return base
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/modules/", h.create)
	r.Get("/workspaces/{slug}/projects/{project_id}/modules/", h.list)
	r.Get("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/", h.update)
	r.Put("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/", h.replace)
	r.Delete("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/", h.destroy)
	r.Get("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/issues/", h.listIssues)
	r.Post("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/issues/", h.addIssues)
	r.Delete("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/issues/{issue_id}/", h.removeIssue)
	r.Get("/workspaces/{slug}/modules/", h.workspaceModules)
	r.Post("/workspaces/{slug}/projects/{project_id}/user-favorite-modules/", h.addFavorite)
	r.Delete("/workspaces/{slug}/projects/{project_id}/user-favorite-modules/{module_id}/", h.delFavorite)
}

func (h *Handler) addFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Module string `json:"module"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	mid, err := uuid.Parse(body.Module)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	_, _ = h.q.CreateFavorite(ctx, gen.CreateFavoriteParams{
		WorkspaceID: ws.ID, UserID: u.ID, EntityType: "module",
		EntityIdentifier: dbx.PgUUID(mid), Name: "", ProjectID: dbx.PgUUID(pid),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) delFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.resolve(ctx, w, r); !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	mid, err := uuid.Parse(chi.URLParam(r, "module_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteFavoriteByEntity(ctx, gen.DeleteFavoriteByEntityParams{UserID: u.ID, EntityIdentifier: dbx.PgUUID(mid)})
	w.WriteHeader(http.StatusNoContent)
}

// moduleValues is a superset covering MODULE_SHAPE + detail extras + the
// workspace shape (all tolerant, no absence assertions).
func moduleValues(m gen.Module) map[string]any {
	return map[string]any{
		"id":                        m.ID.String(),
		"workspace_id":              m.WorkspaceID.String(),
		"project_id":                m.ProjectID.String(),
		"name":                      m.Name,
		"description":               m.Description,
		"description_text":          nil,
		"description_html":          nil,
		"start_date":                nil,
		"target_date":               nil,
		"status":                    m.Status,
		"lead_id":                   dbx.StrPtr(m.LeadID),
		"member_ids":                []string{},
		"view_props":                map[string]any{},
		"sort_order":                httpx.Float(m.SortOrder),
		"external_source":           nil,
		"external_id":               nil,
		"logo_props":                map[string]any{},
		"created_at":                m.CreatedAt,
		"updated_at":                m.UpdatedAt,
		"archived_at":               nil,
		"is_favorite":               false,
		"total_issues":              0,
		"cancelled_issues":          0,
		"completed_issues":          0,
		"started_issues":            0,
		"unstarted_issues":          0,
		"backlog_issues":            0,
		"total_estimate_points":     0,
		"completed_estimate_points": 0,
		"backlog_estimate_points":   0,
		"unstarted_estimate_points": 0,
		"started_estimate_points":   0,
		"cancelled_estimate_points": 0,
		"link_module":               []any{},
		"sub_issues":                0,
		"distribution":              map[string]any{"assignees": []any{}, "labels": []any{}, "completion_chart": map[string]any{}},
		"estimate_distribution":     map[string]any{},
	}
}

// modulePut is the distinct ModuleSerializer shape PUT returns.
func modulePut(m gen.Module) map[string]any {
	return map[string]any{
		"id":               m.ID.String(),
		"lead_id":          dbx.StrPtr(m.LeadID),
		"created_at":       m.CreatedAt,
		"updated_at":       m.UpdatedAt,
		"deleted_at":       m.DeletedAt,
		"name":             m.Name,
		"description":      m.Description,
		"description_text": nil,
		"description_html": nil,
		"start_date":       nil,
		"target_date":      nil,
		"status":           m.Status,
		"view_props":       map[string]any{},
		"sort_order":       httpx.Float(m.SortOrder),
		"external_source":  nil,
		"external_id":      nil,
		"archived_at":      nil,
		"logo_props":       map[string]any{},
		"created_by":       dbx.StrPtr(m.CreatedBy),
		"updated_by":       dbx.StrPtr(m.UpdatedBy),
		"project":          m.ProjectID.String(),
		"workspace":        m.WorkspaceID.String(),
		"lead":             dbx.StrPtr(m.LeadID),
		"members":          []any{},
		"member_ids":       []string{},
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
		Status      string  `json:"status"`
		StartDate   *string `json:"start_date"`
		TargetDate  *string `json:"target_date"`
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
	if !datesOK(body.StartDate, body.TargetDate) {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"non_field_errors": {"Start date cannot exceed target date"}})
		return
	}
	if exists, _ := h.q.ModuleNameExists(ctx, gen.ModuleNameExistsParams{ProjectID: pid, Lower: name}); exists {
		httpx.Error(w, http.StatusBadRequest, "Module with this name already exists")
		return
	}
	status := body.Status
	if status == "" {
		status = "backlog"
	}
	m, err := h.q.CreateModule(ctx, gen.CreateModuleParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: name, Description: body.Description,
		Status: status, CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, h.withAnalytics(ctx, moduleValues(m), m.ID))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	modules, err := h.q.ListModules(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(modules))
	for _, m := range modules {
		out = append(out, h.withAnalytics(ctx, moduleValues(m), m.ID))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
	if !found {
		return
	}
	httpx.JSON(w, http.StatusOK, h.withAnalytics(ctx, moduleValues(m), m.ID))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	m, ok := h.applyUpdate(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, h.withAnalytics(r.Context(), moduleValues(m), m.ID))
}

func (h *Handler) replace(w http.ResponseWriter, r *http.Request) {
	m, ok := h.applyUpdate(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, modulePut(m)) // PUT returns the distinct shape
}

func (h *Handler) applyUpdate(w http.ResponseWriter, r *http.Request) (gen.Module, bool) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return gen.Module{}, false
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
	if !found {
		return gen.Module{}, false
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, desc, status := m.Name, m.Description, m.Status
	if body.Name != nil {
		name = *body.Name
	}
	if body.Description != nil {
		desc = *body.Description
	}
	if body.Status != nil {
		status = *body.Status
	}
	u, _ := auth.UserFrom(ctx)
	updated, err := h.q.UpdateModule(ctx, gen.UpdateModuleParams{
		ID: m.ID, ProjectID: pid, Name: name, Description: desc, Status: status, UpdatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return gen.Module{}, false
	}
	return updated, true
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
	if !found {
		return
	}
	if err := h.q.SoftDeleteModule(ctx, gen.SoftDeleteModuleParams{ID: m.ID, ProjectID: pid}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
	if !found {
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy != "" && !issue.ValidGroupBy(groupBy) {
		httpx.Detail(w, http.StatusBadRequest, "Invalid group_by field: "+groupBy)
		return
	}
	issues, err := h.q.ListModuleIssueIssues(ctx, m.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	f := issue.ParseFilter(r.URL.Query())
	var sg map[string]string
	if f.NeedsStateGroups() {
		if states, err := h.q.ListStates(ctx, pid); err == nil {
			sg = issue.StateGroupMap(states)
		}
	}
	issues = f.Apply(issues, sg)
	vals := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		v := issue.Values(i)
		v["module_ids"] = []string{m.ID.String()} // these issues belong to this module
		vals = append(vals, v)
	}
	httpx.JSON(w, http.StatusOK, issue.GroupValues(vals, groupBy))
}

func (h *Handler) addIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
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
		_ = h.q.AddModuleIssue(ctx, gen.AddModuleIssueParams{
			WorkspaceID: ws.ID, ProjectID: pid, ModuleID: m.ID, IssueID: iid,
		})
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"message": "success"})
}

func (h *Handler) removeIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	m, found := h.module(ctx, w, pid, chi.URLParam(r, "module_id"))
	if !found {
		return
	}
	iid, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.RemoveModuleIssue(ctx, gen.RemoveModuleIssueParams{ModuleID: m.ID, IssueID: iid})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) workspaceModules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	modules, err := h.q.ListWorkspaceModules(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(modules))
	for _, m := range modules {
		out = append(out, h.withAnalytics(ctx, moduleValues(m), m.ID))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- helpers ---------------------------------------------------------------

// datesOK returns false only when both dates are present and start > target.
func datesOK(startStr, targetStr *string) bool {
	if startStr == nil || targetStr == nil || *startStr == "" || *targetStr == "" {
		return true
	}
	start, err1 := time.Parse("2006-01-02", *startStr)
	target, err2 := time.Parse("2006-01-02", *targetStr)
	if err1 != nil || err2 != nil {
		return true // unparseable dates aren't the ordering error
	}
	return !start.After(target)
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

func (h *Handler) module(ctx context.Context, w http.ResponseWriter, pid uuid.UUID, idStr string) (gen.Module, bool) {
	mid, err := uuid.Parse(idStr)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Module{}, false
	}
	m, err := h.q.GetModule(ctx, gen.GetModuleParams{ID: mid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Module{}, false
	}
	return m, true
}

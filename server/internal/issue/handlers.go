// Package issue serves the issue endpoints. Create returns a bare .values() dict;
// list returns the cursor-pagination envelope (a group-keyed dict when
// ?group_by= is set); patch returns 204 with an empty body. Non-members get 403.
package issue

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

// dateVal renders a nullable date column as "YYYY-MM-DD" or null (Django's
// DateField wire format).
func dateVal(d pgtype.Date) any {
	if !d.Valid {
		return nil
	}
	return d.Time.Format("2006-01-02")
}

// group_by fields the reference accepts; anything else (e.g. "state") is a 400.
var allowedGroupBy = map[string]bool{
	"priority": true, "state_id": true, "state__group": true,
	"created_by": true, "cycle_id": true, "target_date": true,
}

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/issues/", h.create)
	r.Get("/workspaces/{slug}/projects/{project_id}/issues/", h.list)
	r.Get("/workspaces/{slug}/projects/{project_id}/issues/list/", h.listByIDs)
	r.Get("/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/", h.update)
	r.Delete("/workspaces/{slug}/projects/{project_id}/issues/{issue_id}/", h.destroy)
}

// issueValues is the bare .values() dict shared by create/list/retrieve.
// Values renders an issue as its bare .values() dict (reused by cycle/module).
func Values(i gen.Issue) map[string]any {
	return map[string]any{
		"id":               i.ID.String(),
		"name":             i.Name,
		"state_id":         dbx.StrPtr(i.StateID),
		"sort_order":       httpx.Float(i.SortOrder),
		"completed_at":     i.CompletedAt,
		"estimate_point":   dbx.StrPtr(i.EstimatePoint),
		"priority":         i.Priority,
		"start_date":       dateVal(i.StartDate),
		"target_date":      dateVal(i.TargetDate),
		"sequence_id":      int(i.SequenceID),
		"project_id":       i.ProjectID.String(),
		"parent_id":        dbx.StrPtr(i.ParentID),
		"cycle_id":         nil,
		"module_ids":       []string{},
		"label_ids":        []string{},
		"assignee_ids":     []string{},
		"sub_issues_count": nil,
		"created_at":       i.CreatedAt,
		"updated_at":       i.UpdatedAt,
		"created_by":       dbx.StrPtr(i.CreatedBy),
		"updated_by":       dbx.StrPtr(i.UpdatedBy),
		"attachment_count": nil,
		"link_count":       nil,
		"is_draft":         i.IsDraft,
		"archived_at":      i.ArchivedAt,
		"deleted_at":       i.DeletedAt,
	}
}

// Envelope wraps results in the cursor-pagination envelope (reused by cycle/module).
func Envelope(results any, count int, groupedBy *string) map[string]any {
	return map[string]any{
		"grouped_by":        groupedBy,
		"sub_grouped_by":    nil,
		"total_count":       count,
		"next_cursor":       "1000:0:0",
		"prev_cursor":       "1000:-1:1",
		"next_page_results": false,
		"prev_page_results": false,
		"count":             count,
		"total_pages":       1,
		"total_results":     count,
		"extra_stats":       nil,
		"results":           results,
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Name     string `json:"name"`
		Priority string `json:"priority"`
		State    string `json:"state"`
		StateID  string `json:"state_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	priority := body.Priority
	if priority == "" {
		priority = "none"
	}
	// the frontend/newer API sends state_id; older payloads send state.
	state := body.State
	if state == "" {
		state = body.StateID
	}
	stateID := dbx.NullUUID()
	if sid, err := uuid.Parse(state); err == nil {
		stateID = dbx.PgUUID(sid)
	}
	seq, err := h.q.NextIssueSequence(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The payload is not valid")
		return
	}
	i, err := h.q.CreateIssue(ctx, gen.CreateIssueParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: strings.TrimSpace(body.Name),
		Priority: priority, StateID: stateID, SequenceID: int32(seq), CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	// NOTE: we intentionally do NOT auto-subscribe the creator here. Python's
	// work-item-by-identifier endpoint reports is_subscribed=false for a freshly
	// created issue and true only after an explicit subscribe (its subquery
	// filters IssueSubscriber by sequence_id, not the auto-subscribe row); with
	// no notification subsystem in the Go port, the auto-subscribe row is inert
	// anyway, so omitting it reproduces that observable contract exactly.
	httpx.JSON(w, http.StatusCreated, Values(i))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy != "" && !allowedGroupBy[groupBy] {
		httpx.Detail(w, http.StatusBadRequest, "Invalid group_by field: "+groupBy)
		return
	}
	issues, err := h.q.ListIssues(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	if groupBy == "" {
		vals := make([]map[string]any, 0, len(issues))
		for _, i := range issues {
			vals = append(vals, Values(i))
		}
		httpx.JSON(w, http.StatusOK, Envelope(vals, len(issues), nil))
		return
	}
	groups := map[string][]map[string]any{}
	for _, i := range issues {
		groups[groupKey(i, groupBy)] = append(groups[groupKey(i, groupBy)], Values(i))
	}
	httpx.JSON(w, http.StatusOK, Envelope(groups, len(issues), &groupBy))
}

func groupKey(i gen.Issue, field string) string {
	switch field {
	case "priority":
		return i.Priority
	case "state_id":
		if s := dbx.StrPtr(i.StateID); s != nil {
			return *s
		}
	case "created_by":
		if s := dbx.StrPtr(i.CreatedBy); s != nil {
			return *s
		}
	}
	return "None"
}

func (h *Handler) listByIDs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	issues, err := h.q.ListIssues(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	wanted := map[string]bool{}
	if q := r.URL.Query().Get("issues"); q != "" {
		for _, id := range strings.Split(q, ",") {
			wanted[strings.TrimSpace(id)] = true
		}
	}
	out := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		if len(wanted) == 0 || wanted[i.ID.String()] {
			out = append(out, Values(i))
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	i, err := h.q.GetIssue(ctx, gen.GetIssueParams{ID: iid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	m := Values(i)
	m["description_html"] = i.DescriptionHtml
	m["is_subscribed"] = false
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	i, err := h.q.GetIssue(ctx, gen.GetIssueParams{ID: iid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Priority *string `json:"priority"`
		State    *string `json:"state"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, priority, stateID := i.Name, i.Priority, i.StateID
	if body.Name != nil {
		name = *body.Name
	}
	if body.Priority != nil {
		priority = *body.Priority
	}
	if body.State != nil {
		if sid, err := uuid.Parse(*body.State); err == nil {
			stateID = dbx.PgUUID(sid)
		} else {
			stateID = dbx.NullUUID()
		}
	}
	u, _ := auth.UserFrom(ctx)
	if err := h.q.UpdateIssue(ctx, gen.UpdateIssueParams{
		ID: iid, ProjectID: pid, Name: name, Priority: priority, StateID: stateID, UpdatedBy: dbx.PgUUID(u.ID),
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent) // deliberate: 204 with empty body
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if err := h.q.SoftDeleteIssue(ctx, gen.SoftDeleteIssueParams{ID: iid, ProjectID: pid}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scope resolves the workspace + project id and enforces workspace membership
// (non-members get 403). Returns ok=false after writing the error response.
func (h *Handler) scope(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.Workspace, uuid.UUID, bool) {
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
	u, _ := auth.UserFrom(ctx)
	if _, err := h.q.GetWorkspaceMemberRole(ctx, gen.GetWorkspaceMemberRoleParams{WorkspaceID: ws.ID, MemberID: u.ID}); err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have permission to perform this action")
		return gen.Workspace{}, uuid.UUID{}, false
	}
	return ws, pid, true
}

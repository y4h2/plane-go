// Package issue serves the issue endpoints. Create returns a bare .values() dict;
// list returns the cursor-pagination envelope (a group-keyed dict when
// ?group_by= is set); patch returns 204 with an empty body. Non-members get 403.
package issue

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/activity"
	"planego/internal/auth"
	"planego/internal/bg"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
	"planego/internal/webhookdelivery"
)

// Filter captures the issue-list filter query params Go can evaluate against its
// schema: priority, state (state_id), state_group, created_by, parent,
// estimate_point — each a comma-separated set (matching Django's *__in filters).
// Label/assignee/mention/date filters are NOT applied — the Go schema has no
// such associations/date-range parsing — so they impose no constraint.
type Filter struct {
	priority   map[string]bool
	state      map[string]bool
	stateGroup map[string]bool
	createdBy  map[string]bool
	parent     map[string]bool
	estimate   map[string]bool
	active     bool
}

func csvSet(v string) map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" && s != "null" {
			m[s] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// mergeFilterVal appends a JSON filter value (a comma-string or a []string) to
// any flat query value for the same dimension.
func mergeFilterVal(flat string, v any) string {
	var add string
	switch t := v.(type) {
	case string:
		add = t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		add = strings.Join(parts, ",")
	}
	switch {
	case add == "":
		return flat
	case flat == "":
		return add
	default:
		return flat + "," + add
	}
}

// ParseFilter reads the supported filter dimensions from a list request. The
// frontend sends them two ways depending on the surface: flat query params
// (priority=urgent,high — cycle/module boards) and a JSON `filters` blob with
// Django-style keys (filters={"priority__in":"urgent,high"} — the project
// board). Both are honored and merged.
func ParseFilter(q url.Values) Filter {
	pr, st, sg := q.Get("priority"), q.Get("state"), q.Get("state_group")
	cb, pa, es := q.Get("created_by"), q.Get("parent"), q.Get("estimate_point")
	if raw := q.Get("filters"); raw != "" {
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			pr = mergeFilterVal(pr, m["priority__in"])
			st = mergeFilterVal(st, m["state__in"])
			sg = mergeFilterVal(sg, m["state__group__in"])
			cb = mergeFilterVal(cb, m["created_by__in"])
			pa = mergeFilterVal(pa, m["parent__in"])
			es = mergeFilterVal(es, m["estimate_point__in"])
		}
	}
	f := Filter{
		priority:   csvSet(pr),
		state:      csvSet(st),
		stateGroup: csvSet(sg),
		createdBy:  csvSet(cb),
		parent:     csvSet(pa),
		estimate:   csvSet(es),
	}
	f.active = f.priority != nil || f.state != nil || f.stateGroup != nil ||
		f.createdBy != nil || f.parent != nil || f.estimate != nil
	return f
}

// NeedsStateGroups reports whether Apply requires a state_id->group map.
func (f Filter) NeedsStateGroups() bool { return f.stateGroup != nil }

func setKeys(m map[string]bool) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Dimension accessors for SQL callers (e.g. the workspace global list, which
// filters in the query rather than in Go).
func (f Filter) Priority() []string   { return setKeys(f.priority) }
func (f Filter) State() []string      { return setKeys(f.state) }
func (f Filter) StateGroup() []string { return setKeys(f.stateGroup) }
func (f Filter) CreatedBy() []string  { return setKeys(f.createdBy) }
func (f Filter) Parent() []string     { return setKeys(f.parent) }

// Apply returns the issues that pass every active filter. stateGroup maps a
// state_id to its group_name (only consulted when a state_group filter is set;
// may be nil otherwise).
func (f Filter) Apply(issues []gen.Issue, stateGroup map[string]string) []gen.Issue {
	if !f.active {
		return issues
	}
	out := make([]gen.Issue, 0, len(issues))
	for _, i := range issues {
		if f.priority != nil && !f.priority[i.Priority] {
			continue
		}
		sid := dbx.StrOrEmpty(i.StateID)
		if f.state != nil && !f.state[sid] {
			continue
		}
		if f.stateGroup != nil && !f.stateGroup[stateGroup[sid]] {
			continue
		}
		if f.createdBy != nil && !f.createdBy[dbx.StrOrEmpty(i.CreatedBy)] {
			continue
		}
		if f.parent != nil && !f.parent[dbx.StrOrEmpty(i.ParentID)] {
			continue
		}
		if f.estimate != nil && !f.estimate[dbx.StrOrEmpty(i.EstimatePoint)] {
			continue
		}
		out = append(out, i)
	}
	return out
}

// StateGroupMap builds a state_id -> group_name lookup from a project's states.
func StateGroupMap(states []gen.State) map[string]string {
	m := make(map[string]string, len(states))
	for _, s := range states {
		m[s.ID.String()] = s.GroupName
	}
	return m
}

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

type Handler struct {
	q    *gen.Queries
	pool *pgxpool.Pool
	bg   *bg.Dispatcher
}

func New(q *gen.Queries, pool *pgxpool.Pool, dispatcher *bg.Dispatcher) *Handler {
	return &Handler{q: q, pool: pool, bg: dispatcher}
}

// recordActivity persists one issue-activity entry off the request path — the
// goroutine analog of Plane's issue_activity Celery task.
func (h *Handler) recordActivity(e activity.Entry) {
	if h.bg == nil || h.pool == nil {
		return
	}
	h.bg.Submit(func(ctx context.Context) {
		if err := activity.Record(ctx, h.pool, e); err != nil {
			log.Printf("issue activity: %v", err)
		}
	})
}

// fireWebhook delivers an issue event to subscribed webhooks off the request
// path — the goroutine analog of Plane's webhook_task Celery worker.
func (h *Handler) fireWebhook(wsID uuid.UUID, action string, data map[string]any) {
	if h.bg == nil || h.pool == nil {
		return
	}
	h.bg.Submit(func(ctx context.Context) {
		webhookdelivery.Fire(ctx, h.pool, wsID, "issue", action, data)
	})
}

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
	// Record the "created" activity + deliver webhooks in the background.
	h.recordActivity(activity.Entry{
		WorkspaceID: ws.ID, ProjectID: pid, IssueID: i.ID, ActorID: u.ID,
		Verb: "created", Comment: "created the work item",
	})
	h.fireWebhook(ws.ID, "created", Values(i))
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
	if groupBy != "" && !ValidGroupBy(groupBy) {
		httpx.Detail(w, http.StatusBadRequest, "Invalid group_by field: "+groupBy)
		return
	}
	// The module/cycle boards hit this project-issues endpoint with ?module= or
	// ?cycle= rather than the nested list, so scope the base set accordingly.
	var issues []gen.Issue
	var err error
	if mid, e := uuid.Parse(r.URL.Query().Get("module")); e == nil {
		issues, err = h.q.ListModuleIssueIssues(ctx, mid)
	} else if cid, e := uuid.Parse(r.URL.Query().Get("cycle")); e == nil {
		issues, err = h.q.ListCycleIssueIssues(ctx, cid)
	} else {
		issues, err = h.q.ListIssues(ctx, pid)
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	issues = h.filter(ctx, pid, r, issues)
	httpx.JSON(w, http.StatusOK, ListEnvelope(issues, groupBy))
}

// filter narrows an issue slice by the request's list-filter query params,
// fetching the project's states only when a state_group filter needs them.
func (h *Handler) filter(ctx context.Context, pid uuid.UUID, r *http.Request, issues []gen.Issue) []gen.Issue {
	f := ParseFilter(r.URL.Query())
	var sg map[string]string
	if f.NeedsStateGroups() {
		if states, err := h.q.ListStates(ctx, pid); err == nil {
			sg = StateGroupMap(states)
		}
	}
	return f.Apply(issues, sg)
}

// ValidGroupBy reports whether field is an accepted group_by (else the caller
// should 400). Shared by the cycle/module issue-list endpoints.
func ValidGroupBy(field string) bool { return allowedGroupBy[field] }

// ListEnvelope builds an issue-list response: a flat cursor envelope when
// groupBy is empty, or a grouped envelope keyed by the group field where each
// group is a {results, total_results} sub-envelope (the shape the frontend's
// grouped/kanban renderer expects — a bare list renders empty). Reused by the
// cycle-issues and module-issues lists so their boards group correctly too.
func ListEnvelope(issues []gen.Issue, groupBy string) map[string]any {
	if groupBy == "" {
		vals := make([]map[string]any, 0, len(issues))
		for _, i := range issues {
			vals = append(vals, Values(i))
		}
		return Envelope(vals, len(issues), nil)
	}
	lists := map[string][]map[string]any{}
	for _, i := range issues {
		k := groupKey(i, groupBy)
		lists[k] = append(lists[k], Values(i))
	}
	groups := make(map[string]any, len(lists))
	for k, v := range lists {
		groups[k] = map[string]any{"results": v, "total_results": len(v)}
	}
	return Envelope(groups, len(issues), &groupBy)
}

// GroupValues is like ListEnvelope but operates on already-built .values() dicts
// (for callers that post-process each value, e.g. module-issues injecting
// module_ids). Groups on the value map's own group_by key.
func GroupValues(vals []map[string]any, groupBy string) map[string]any {
	if groupBy == "" {
		return Envelope(vals, len(vals), nil)
	}
	lists := map[string][]map[string]any{}
	for _, v := range vals {
		k := "None"
		if raw, ok := v[groupBy]; ok && raw != nil {
			if s, ok := raw.(string); ok && s != "" {
				k = s
			}
		}
		lists[k] = append(lists[k], v)
	}
	groups := make(map[string]any, len(lists))
	for k, v := range lists {
		groups[k] = map[string]any{"results": v, "total_results": len(v)}
	}
	return Envelope(groups, len(vals), &groupBy)
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
	ws, pid, ok := h.scope(ctx, w, r)
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
		StateID  *string `json:"state_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// the frontend/newer API sends state_id; older payloads send state.
	if body.State == nil {
		body.State = body.StateID
	}
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
	// Record one "updated" activity per changed field, in the background.
	if body.Name != nil && name != i.Name {
		h.recordActivity(activity.Entry{WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, ActorID: u.ID,
			Verb: "updated", Field: "name", OldValue: i.Name, NewValue: name})
	}
	if body.Priority != nil && priority != i.Priority {
		h.recordActivity(activity.Entry{WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, ActorID: u.ID,
			Verb: "updated", Field: "priority", OldValue: i.Priority, NewValue: priority})
	}
	if body.State != nil && dbx.StrOrEmpty(stateID) != dbx.StrOrEmpty(i.StateID) {
		h.recordActivity(activity.Entry{WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, ActorID: u.ID,
			Verb: "updated", Field: "state", OldValue: dbx.StrOrEmpty(i.StateID), NewValue: dbx.StrOrEmpty(stateID)})
	}
	h.fireWebhook(ws.ID, "updated", map[string]any{"id": iid.String(), "name": name, "priority": priority})
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

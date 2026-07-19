// Package analytic serves the workspace analytics endpoints. These are computed
// aggregate reads over the existing issues/states/cycles/modules/project_members
// tables -- no tables of their own. Responses mirror Django's bare dicts.
//
// Covered:
//
//	GET /workspaces/{slug}/analytics/         (x_axis/y_axis distribution + extras)
//	GET /workspaces/{slug}/default-analytics/ (summary rollups)
//	GET /workspaces/{slug}/project-stats/     (per-project counts)
//
// The port has no assignee/label/cycle-issue/module-issue join tables, so the
// per-dimension "extras" for those axes are always empty (matching Django's
// default `{}`), and assignee-based rollups collapse to a single null row.
package analytic

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/httpx"
)

// ph renders a positional placeholder like "$3".
func ph(n int) string { return "$" + strconv.Itoa(n) }

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/analytics/", h.analytics)
	r.Get("/workspaces/{slug}/default-analytics/", h.defaultAnalytics)
	// NOTE: /project-stats/ is registered by the search package (identical
	// path + shape); registering it here too would panic chi on a dup route.
	// Advanced analytics (the workspace Analytics page). The frontend calls
	// these without a trailing slash; register both forms.
	for _, s := range []string{"", "/"} {
		r.Get("/workspaces/{slug}/advance-analytics"+s, h.advanceOverview)
		r.Get("/workspaces/{slug}/advance-analytics-stats"+s, h.advanceStats)
		r.Get("/workspaces/{slug}/advance-analytics-charts"+s, h.advanceCharts)
	}
}

// wsID resolves the workspace uuid for the {slug}. Returns false (and writes a
// 404) when the slug is unknown.
func (h *Handler) wsID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	var id uuid.UUID
	err := h.pool.QueryRow(r.Context(),
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return id, false
	}
	return id, true
}

func (h *Handler) count(ctx context.Context, sql string, args ...any) int {
	var n int
	_ = h.pool.QueryRow(ctx, sql, args...).Scan(&n)
	return n
}

// advanceOverview backs the Analytics > Overview stat tiles.
func (h *Handler) advanceOverview(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.wsID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c := func(sql string, args ...any) map[string]any { return map[string]any{"count": h.count(ctx, sql, args...)} }
	httpx.JSON(w, http.StatusOK, map[string]any{
		"total_users":      c(`select count(*) from workspace_members where workspace_id=$1`, ws),
		"total_admins":     c(`select count(*) from workspace_members where workspace_id=$1 and role=20`, ws),
		"total_members":    c(`select count(*) from workspace_members where workspace_id=$1 and role=15`, ws),
		"total_guests":     c(`select count(*) from workspace_members where workspace_id=$1 and role=5`, ws),
		"total_projects":   c(`select count(*) from projects where workspace_id=$1 and deleted_at is null`, ws),
		"total_work_items": c(`select count(*) from issues i join projects p on p.id=i.project_id where p.workspace_id=$1 and i.deleted_at is null`, ws),
		"total_cycles":     c(`select count(*) from cycles c join projects p on p.id=c.project_id where p.workspace_id=$1 and c.deleted_at is null`, ws),
		"total_intake":     c(`select count(*) from intakes ik join projects p on p.id=ik.project_id where p.workspace_id=$1`, ws),
	})
}

// advanceStats backs Analytics > Work items (per-project state-group counts).
func (h *Handler) advanceStats(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.wsID(w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		select p.id, p.name,
		  count(*) filter (where s.group_name='cancelled')  as cancelled,
		  count(*) filter (where s.group_name='completed')  as completed,
		  count(*) filter (where s.group_name='backlog')    as backlog,
		  count(*) filter (where s.group_name='unstarted')  as unstarted,
		  count(*) filter (where s.group_name='started')    as started
		from projects p
		left join issues i on i.project_id=p.id and i.deleted_at is null
		left join states s on s.id=i.state_id
		where p.workspace_id=$1 and p.deleted_at is null
		group by p.id, p.name`, ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		var canc, comp, back, unst, star int
		if rows.Scan(&id, &name, &canc, &comp, &back, &unst, &star) == nil {
			out = append(out, map[string]any{
				"project_id": id.String(), "project__name": name,
				"cancelled_work_items": canc, "completed_work_items": comp,
				"backlog_work_items": back, "un_started_work_items": unst, "started_work_items": star,
			})
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// advanceCharts backs the Analytics charts. type=projects returns per-entity
// workspace counts; any other type returns a (possibly single-bucket) time
// series over issue creation dates.
func (h *Handler) advanceCharts(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.wsID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if r.URL.Query().Get("type") == "projects" {
		httpx.JSON(w, http.StatusOK, []map[string]any{
			{"key": "work_items", "name": "Work Items", "count": h.count(ctx, `select count(*) from issues i join projects p on p.id=i.project_id where p.workspace_id=$1 and i.deleted_at is null`, ws)},
			{"key": "cycles", "name": "Cycles", "count": h.count(ctx, `select count(*) from cycles c join projects p on p.id=c.project_id where p.workspace_id=$1 and c.deleted_at is null`, ws)},
			{"key": "modules", "name": "Modules", "count": h.count(ctx, `select count(*) from modules m join projects p on p.id=m.project_id where p.workspace_id=$1 and m.deleted_at is null`, ws)},
			{"key": "intake", "name": "Intake", "count": h.count(ctx, `select count(*) from intakes ik join projects p on p.id=ik.project_id where p.workspace_id=$1`, ws)},
			{"key": "members", "name": "Members", "count": h.count(ctx, `select count(*) from workspace_members where workspace_id=$1`, ws)},
			{"key": "pages", "name": "Pages", "count": h.count(ctx, `select count(*) from pages where workspace_id=$1 and deleted_at is null`, ws)},
		})
		return
	}
	rows, err := h.pool.Query(ctx, `
		select to_char(i.created_at::date,'YYYY-MM-DD') as d,
		       count(*) as created,
		       count(*) filter (where s.group_name='completed') as completed
		from issues i
		join projects p on p.id=i.project_id
		left join states s on s.id=i.state_id
		where p.workspace_id=$1 and i.deleted_at is null
		group by d order by d`, ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	data := []map[string]any{}
	for rows.Next() {
		var d string
		var created, completed int
		if rows.Scan(&d, &created, &completed) == nil {
			data = append(data, map[string]any{"key": d, "name": d, "count": created, "completed_issues": completed, "created_issues": created})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"data":   data,
		"schema": map[string]any{"completed_issues": "completed_issues", "created_issues": "created_issues"},
	})
}

// ---- shared ----------------------------------------------------------------

// workspaceID resolves the slug to a workspace id (as text), or writes a 404 and
// returns false. IDs are carried as strings throughout so pgx never has to encode
// google/uuid values (columns are compared via ::text).
func (h *Handler) workspaceID(ctx context.Context, w http.ResponseWriter, slug string) (string, bool) {
	var id string
	err := h.pool.QueryRow(ctx,
		`select id::text from workspaces where slug=$1 and deleted_at is null`, slug).Scan(&id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return "", false
	}
	return id, true
}

// parseIDList reads a comma-separated query param into a slice of valid uuid
// strings (invalid entries are dropped, matching Django's filter_valid_uuids).
func parseIDList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if _, err := uuid.Parse(part); err == nil {
			out = append(out, part)
		}
	}
	return out
}

// issueScope builds the shared WHERE fragment + args for a workspace's non-draft,
// non-deleted issues, optionally scoped to a set of projects. Args start at $1.
func issueScope(wsID string, projects []string) (string, []any) {
	where := "i.workspace_id::text = $1 and i.deleted_at is null and i.is_draft = false"
	args := []any{wsID}
	if len(projects) > 0 {
		where += " and i.project_id::text = any($2::text[])"
		args = append(args, projects)
	}
	return where, args
}

// ---- /analytics/ -----------------------------------------------------------

var validAnalyticsFields = map[string]bool{
	"state_id": true, "state__group": true, "labels__id": true,
	"assignees__id": true, "estimate_point__value": true,
	"issue_cycle__cycle_id": true, "issue_module__module_id": true,
	"priority": true, "start_date": true, "target_date": true,
	"created_at": true, "completed_at": true,
}

var validYAxis = map[string]bool{"issue_count": true, "estimate": true}

func isDateField(f string) bool {
	switch f {
	case "created_at", "start_date", "target_date", "completed_at":
		return true
	}
	return false
}

// axisExpr returns the SQL expression for a dimension/segment field and whether a
// states join is required. Fields with no backing data on this port map to NULL.
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
		// labels__id, assignees__id, issue_cycle__cycle_id, issue_module__module_id,
		// estimate_point__value -- no backing join data on this port.
		return "null::text", false
	}
}

func (h *Handler) analytics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	xAxis := q.Get("x_axis")
	yAxis := q.Get("y_axis")
	segment := q.Get("segment")

	if xAxis == "" || yAxis == "" || !validAnalyticsFields[xAxis] || !validYAxis[yAxis] {
		httpx.Error(w, http.StatusBadRequest,
			"x-axis and y-axis dimensions are required and the values should be valid")
		return
	}
	if segment != "" && (!validAnalyticsFields[segment] || xAxis == segment) {
		httpx.Error(w, http.StatusBadRequest,
			"Both segment and x axis cannot be same and segment should be valid")
		return
	}

	wsID, ok := h.workspaceID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	projects := parseIDList(r.URL.Query().Get("project"))
	where, args := issueScope(wsID, projects)

	// total issue count for the filtered set
	var total int
	if err := h.pool.QueryRow(ctx,
		"select count(*)::int from issues i where "+where, args...).Scan(&total); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	dist, err := h.distribution(ctx, xAxis, yAxis, segment, where, args)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	// extras: only state_details is computable on this port; the rest have no
	// backing join tables and stay empty (Django's default is `{}`).
	extras := map[string]any{
		"state_details":    map[string]any{},
		"assignee_details": map[string]any{},
		"label_details":    map[string]any{},
		"cycle_details":    map[string]any{},
		"module_details":   map[string]any{},
	}
	if xAxis == "state_id" || segment == "state_id" {
		details, derr := h.stateDetails(ctx, where, args)
		if derr == nil {
			extras["state_details"] = details
		}
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"total":        total,
		"distribution": dist,
		"extras":       extras,
	})
}

// distribution groups the filtered issues by the x-axis (and optional segment),
// returning Django's shape: {dimension: [{dimension, [segment], count/estimate}]}.
func (h *Handler) distribution(ctx context.Context, xAxis, yAxis, segment, where string, args []any) (map[string]any, error) {
	dimExpr, dimJoin := axisExpr(xAxis)
	needStateJoin := dimJoin
	segExpr := ""
	if segment != "" {
		e, j := axisExpr(segment)
		segExpr = e
		needStateJoin = needStateJoin || j
	}

	join := ""
	if needStateJoin {
		join = " left join states s on s.id = i.state_id"
	}

	// value aggregate: issue_count -> count(*); estimate -> null on this port
	// (no estimate point values wired), preserving the key shape.
	valExpr := "count(*)::int"
	valKey := "count"
	if yAxis == "estimate" {
		valExpr = "null::float"
		valKey = "estimate"
	}

	// Build SELECT. For date x-axis, exclude rows whose date is null.
	sel := "select " + dimExpr + " as dimension"
	groupCols := "1"
	if segment != "" {
		sel += ", " + segExpr + " as segment"
		groupCols = "1, 2"
	}
	sel += ", " + valExpr + " as val from issues i" + join + " where " + where
	if isDateField(xAxis) {
		sel += " and i." + xAxis + " is not null"
	}
	sel += " group by " + groupCols + " order by 1"

	rows, err := h.pool.Query(ctx, sel, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	grouped := map[string]any{}
	for rows.Next() {
		var dim *string
		var seg *string
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
			return nil, err
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
	return grouped, rows.Err()
}

// stateDetails returns the distinct states of the filtered issues, matching
// Django's {state_id, state__name, state__color} rows.
func (h *Handler) stateDetails(ctx context.Context, where string, args []any) ([]map[string]any, error) {
	sql := "select distinct s.id::text, s.name, s.color from issues i " +
		"join states s on s.id = i.state_id where " + where +
		" and i.state_id is not null order by s.id::text"
	rows, err := h.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, color string
		if err := rows.Scan(&id, &name, &color); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"state_id":     id,
			"state__name":  name,
			"state__color": color,
		})
	}
	return out, rows.Err()
}

// ---- /default-analytics/ ---------------------------------------------------

var openGroups = []string{"backlog", "unstarted", "started"}

func (h *Handler) defaultAnalytics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.workspaceID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	projects := parseIDList(r.URL.Query().Get("project"))
	where, args := issueScope(wsID, projects)

	fail := func(err error) bool {
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return true
		}
		return false
	}

	var totalIssues int
	if fail(h.pool.QueryRow(ctx, "select count(*)::int from issues i where "+where, args...).Scan(&totalIssues)) {
		return
	}

	totalClassified, err := h.groupCounts(ctx, where, args, "")
	if fail(err) {
		return
	}

	openWhere := where + " and s.group_name = any(" + ph(len(args)+1) + "::text[])"
	openArgs := append(append([]any{}, args...), openGroups)
	var openIssues int
	if fail(h.pool.QueryRow(ctx,
		"select count(*)::int from issues i join states s on s.id = i.state_id where "+openWhere,
		openArgs...).Scan(&openIssues)) {
		return
	}
	openClassified, err := h.groupCounts(ctx, where, args, "started,unstarted,backlog")
	if fail(err) {
		return
	}

	completedMonthWise, err := h.completedMonthWise(ctx, where, args)
	if fail(err) {
		return
	}

	createdUsers, err := h.mostCreatedUsers(ctx, where, args)
	if fail(err) {
		return
	}

	pending, err := h.pendingUser(ctx, where, args)
	if fail(err) {
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"total_issues":               totalIssues,
		"total_issues_classified":    totalClassified,
		"open_issues":                openIssues,
		"open_issues_classified":     openClassified,
		"issue_completed_month_wise": completedMonthWise,
		"most_issue_created_user":    createdUsers,
		"most_issue_closed_user":     []any{}, // no assignee join table on this port
		"pending_issue_user":         pending,
		"open_estimate_sum":          nil,
		"total_estimate_sum":         nil,
	})
}

// groupCounts returns [{state_group, state_count}] ordered by group name. If
// `restrict` is a comma list of groups, only those are included.
func (h *Handler) groupCounts(ctx context.Context, where string, args []any, restrict string) ([]map[string]any, error) {
	sql := "select s.group_name, count(*)::int from issues i join states s on s.id = i.state_id where " + where
	callArgs := args
	if restrict != "" {
		groups := strings.Split(restrict, ",")
		sql += " and s.group_name = any(" + ph(len(args)+1) + "::text[])"
		callArgs = append(append([]any{}, args...), groups)
	}
	sql += " group by s.group_name order by s.group_name"
	rows, err := h.pool.Query(ctx, sql, callArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var g string
		var c int
		if err := rows.Scan(&g, &c); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"state_group": g, "state_count": c})
	}
	return out, rows.Err()
}

func (h *Handler) completedMonthWise(ctx context.Context, where string, args []any) ([]map[string]any, error) {
	year := time.Now().Year()
	sql := "select extract(month from i.completed_at)::int as month, count(*)::int from issues i where " + where +
		" and i.completed_at is not null and extract(year from i.completed_at)::int = " + ph(len(args)+1) +
		" group by 1 order by 1"
	callArgs := append(append([]any{}, args...), year)
	rows, err := h.pool.Query(ctx, sql, callArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var month, count int
		if err := rows.Scan(&month, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"month": month, "count": count})
	}
	return out, rows.Err()
}

func (h *Handler) mostCreatedUsers(ctx context.Context, where string, args []any) ([]map[string]any, error) {
	sql := "select u.first_name, u.last_name, u.display_name, u.id::text, count(i.id)::int, u.avatar " +
		"from issues i join users u on u.id = i.created_by where " + where +
		" and i.created_by is not null " +
		"group by u.id, u.first_name, u.last_name, u.display_name, u.avatar " +
		"order by count(i.id) desc limit 5"
	rows, err := h.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var first, last, display, id, avatar string
		var count int
		if err := rows.Scan(&first, &last, &display, &id, &count, &avatar); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"created_by__first_name":   first,
			"created_by__last_name":    last,
			"created_by__display_name": display,
			"created_by__id":           id,
			"count":                    count,
			"created_by__avatar_url":   avatar,
		})
	}
	return out, rows.Err()
}

// pendingUser mirrors Django's assignee rollup of not-completed issues. With no
// assignee join table, all pending issues collapse into one null-assignee row.
func (h *Handler) pendingUser(ctx context.Context, where string, args []any) ([]map[string]any, error) {
	var pending int
	if err := h.pool.QueryRow(ctx,
		"select count(*)::int from issues i where "+where+" and i.completed_at is null", args...).Scan(&pending); err != nil {
		return nil, err
	}
	if pending == 0 {
		return []map[string]any{}, nil
	}
	return []map[string]any{{
		"assignees__first_name":   nil,
		"assignees__last_name":    nil,
		"assignees__display_name": nil,
		"assignees__id":           nil,
		"count":                   pending,
		"assignees__avatar_url":   nil,
	}}, nil
}

// ---- /project-stats/ -------------------------------------------------------

var validStatFields = []string{
	"total_issues", "completed_issues", "total_members", "total_cycles", "total_modules",
}

func (h *Handler) projectStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.workspaceID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}

	// requested fields (default = all)
	req := map[string]bool{}
	for _, f := range strings.Split(r.URL.Query().Get("fields"), ",") {
		f = strings.TrimSpace(f)
		for _, v := range validStatFields {
			if f == v {
				req[f] = true
			}
		}
	}
	if len(req) == 0 {
		for _, v := range validStatFields {
			req[v] = true
		}
	}

	// project id filter
	projIDs := parseIDList(r.URL.Query().Get("project_ids"))

	sql := "select id::text from projects where workspace_id::text = $1 and deleted_at is null"
	pargs := []any{wsID}
	if len(projIDs) > 0 {
		sql += " and id::text = any($2::text[])"
		pargs = append(pargs, projIDs)
	}
	rows, err := h.pool.Query(ctx, sql, pargs...)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	out := make([]map[string]any, 0, len(ids))
	for _, pid := range ids {
		row := map[string]any{"id": pid}
		if req["total_issues"] {
			row["total_issues"] = h.countScalar(ctx,
				"select count(*)::int from issues where project_id::text=$1 and deleted_at is null and is_draft=false", pid)
		}
		if req["completed_issues"] {
			row["completed_issues"] = h.countScalar(ctx,
				"select count(*)::int from issues i join states s on s.id=i.state_id "+
					"where i.project_id::text=$1 and i.deleted_at is null and i.is_draft=false "+
					"and s.group_name in ('completed','cancelled')", pid)
		}
		if req["total_cycles"] {
			row["total_cycles"] = h.countScalar(ctx,
				"select count(*)::int from cycles where project_id::text=$1 and deleted_at is null", pid)
		}
		if req["total_modules"] {
			row["total_modules"] = h.countScalar(ctx,
				"select count(*)::int from modules where project_id::text=$1 and deleted_at is null", pid)
		}
		if req["total_members"] {
			row["total_members"] = h.countScalar(ctx,
				"select count(*)::int from project_members pm join users u on u.id=pm.member_id "+
					"where pm.project_id::text=$1 and u.is_bot=false", pid)
		}
		out = append(out, row)
	}

	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) countScalar(ctx context.Context, sql string, args ...any) int {
	var n int
	_ = h.pool.QueryRow(ctx, sql, args...).Scan(&n)
	return n
}

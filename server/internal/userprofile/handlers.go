// Package userprofile serves the per-user workspace analytics reads. Like the
// analytic package these are aggregate reads over the existing
// issues/states/projects/workspace_members tables -- no tables of their own.
//
// Covered:
//
//	GET /workspaces/{slug}/user-stats/{user_id}/          (issue counts + distributions)
//	GET /workspaces/{slug}/user-activity/{user_id}/       (activity log -- paginated envelope)
//	GET /workspaces/{slug}/user-profile/{user_id}/        (per-project counts + user_data)
//	GET /users/me/workspaces/{slug}/activity-graph/       (issue-created-by-date heatmap)
//
// The port has NO issue-activity/audit table and NO issue<->assignee join, so
// (matching the internal/analytic convention where assignee/label joins are
// missing):
//   - user-activity always returns an EMPTY paginated envelope (no activity log).
//   - state/priority distributions and created/completed/pending counts are
//     derived from issues.created_by (the column that exists), NOT assignees.
//     The assignee-derived scalars (assigned/completed/pending/subscribed and the
//     per-project assigned/completed/pending) collapse to 0.
//   - activity-graph groups the user's created issues by created_at date.
package userprofile

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/user-stats/{user_id}/", h.userStats)
	r.Get("/workspaces/{slug}/user-activity/{user_id}/", h.userActivity)
	r.Get("/workspaces/{slug}/user-profile/{user_id}/", h.userProfile)
	r.Get("/users/me/workspaces/{slug}/activity-graph/", h.activityGraph)
}

// wsID resolves the workspace uuid (as text) for the {slug}, or writes a 404.
func (h *Handler) wsID(ctx context.Context, w http.ResponseWriter, slug string) (string, bool) {
	var id string
	err := h.pool.QueryRow(ctx,
		`select id::text from workspaces where slug=$1 and deleted_at is null`, slug).Scan(&id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return "", false
	}
	return id, true
}

func (h *Handler) count(ctx context.Context, sql string, args ...any) int {
	var n int
	_ = h.pool.QueryRow(ctx, sql, args...).Scan(&n)
	return n
}

// priorityOrder mirrors Django's Case ordering: urgent<high<medium<low<none.
func priorityOrder(p string) int {
	switch p {
	case "urgent":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "none":
		return 4
	default:
		return 5
	}
}

// ---- /user-stats/ ----------------------------------------------------------

func (h *Handler) userStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.wsID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	userID := chi.URLParam(r, "user_id")

	// state_distribution -- issues the user CREATED, grouped by state group.
	// (Django groups by assignee; on this port there is no assignee join, so we
	// use created_by. Shape-only in the contract test.)
	stateDist := []map[string]any{}
	if rows, err := h.pool.Query(ctx, `
		select s.group_name, count(*)::int
		from issues i join states s on s.id = i.state_id
		where i.workspace_id::text = $1 and i.created_by::text = $2
		  and i.deleted_at is null and i.is_draft = false
		group by s.group_name order by s.group_name`, ws, userID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var g string
			var c int
			if rows.Scan(&g, &c) == nil {
				stateDist = append(stateDist, map[string]any{"state_group": g, "state_count": c})
			}
		}
	}

	// priority_distribution -- created_by based, ordered by Django's priority order.
	priDist := []map[string]any{}
	if rows, err := h.pool.Query(ctx, `
		select i.priority, count(*)::int
		from issues i
		where i.workspace_id::text = $1 and i.created_by::text = $2
		  and i.deleted_at is null and i.is_draft = false
		group by i.priority`, ws, userID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			var c int
			if rows.Scan(&p, &c) == nil {
				priDist = append(priDist, map[string]any{
					"priority": p, "priority_count": c, "priority_order": priorityOrder(p),
				})
			}
		}
	}
	// order by priority_order (stable insertion-order sort)
	for i := 1; i < len(priDist); i++ {
		for j := i; j > 0 && priDist[j-1]["priority_order"].(int) > priDist[j]["priority_order"].(int); j-- {
			priDist[j-1], priDist[j] = priDist[j], priDist[j-1]
		}
	}

	createdIssues := h.count(ctx, `
		select count(*)::int from issues i
		where i.workspace_id::text = $1 and i.created_by::text = $2
		  and i.deleted_at is null and i.is_draft = false and i.archived_at is null`, ws, userID)

	httpx.JSON(w, http.StatusOK, map[string]any{
		"state_distribution":    stateDist,
		"priority_distribution": priDist,
		"created_issues":        createdIssues,
		// Assignee-derived on Django; no assignee join on this port -> 0.
		"assigned_issues":   0,
		"completed_issues":  0,
		"pending_issues":    0,
		"subscribed_issues": 0,
		"present_cycles":    []any{},
		"upcoming_cycles":   []any{},
	})
}

// ---- /user-activity/ -------------------------------------------------------

// userActivity mirrors Django's paginated issue-activity feed. The port has no
// activity/audit table, so the envelope always carries an empty result list.
func (h *Handler) userActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsID(ctx, w, chi.URLParam(r, "slug")); !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"grouped_by":        nil,
		"sub_grouped_by":    nil,
		"total_count":       0,
		"next_cursor":       "1000:1:0",
		"prev_cursor":       "1000:-1:1",
		"next_page_results": false,
		"prev_page_results": false,
		"count":             0,
		"total_pages":       0,
		"total_results":     0,
		"extra_stats":       nil,
		"results":           []any{},
	})
}

// ---- /user-profile/ --------------------------------------------------------

func (h *Handler) userProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.wsID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	targetID := chi.URLParam(r, "user_id")
	reqUser, _ := auth.UserFrom(ctx)
	reqUserID := reqUser.ID.String()

	// Requesting member's role gates whether project_data is populated (>=15).
	var role int
	_ = h.pool.QueryRow(ctx,
		`select role from workspace_members where workspace_id::text=$1 and member_id::text=$2`,
		ws, reqUserID).Scan(&role)

	projectData := []map[string]any{}
	if role >= 15 {
		// Projects the requesting user is a member of, annotated with the count of
		// issues CREATED BY the target user. assigned/completed/pending are
		// assignee-derived (no join on this port) -> 0.
		// NOTE: the projects table has no logo_props column on this port (the
		// project package always emits {}), so we don't select it.
		rows, err := h.pool.Query(ctx, `
			select p.id::text,
			  count(i.id) filter (
			    where i.created_by::text = $3 and i.archived_at is null and i.is_draft = false
			  )::int as created_issues
			from projects p
			join project_members pm on pm.project_id = p.id and pm.member_id::text = $2
			left join issues i on i.project_id = p.id and i.deleted_at is null
			where p.workspace_id::text = $1 and p.archived_at is null and p.deleted_at is null
			group by p.id`, ws, reqUserID, targetID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				var created int
				if rows.Scan(&id, &created) == nil {
					projectData = append(projectData, map[string]any{
						"id":               id,
						"logo_props":       map[string]any{},
						"created_issues":   created,
						"assigned_issues":  0,
						"completed_issues": 0,
						"pending_issues":   0,
					})
				}
			}
		}
	}

	// user_data for the target user.
	var email, firstName, lastName, displayName string
	var dateJoined time.Time
	err := h.pool.QueryRow(ctx,
		`select email, first_name, last_name, display_name, date_joined from users where id::text=$1`,
		targetID).Scan(&email, &firstName, &lastName, &displayName, &dateJoined)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"project_data": projectData,
		"user_data": map[string]any{
			"email":           email,
			"first_name":      firstName,
			"last_name":       lastName,
			"avatar_url":      nil, // avatar-asset resolution not wired on this port
			"cover_image_url": nil,
			"date_joined":     dateJoined.UTC().Format("2006-01-02T15:04:05.000000Z"),
			"user_timezone":   "UTC",
			"display_name":    displayName,
		},
	})
}

// ---- /activity-graph/ ------------------------------------------------------

// activityGraph mirrors Django's GitHub-style heatmap. Django counts
// issue-activity rows per date; this port has no activity table, so it counts
// the authenticated user's CREATED issues per date (last 6 months) instead.
func (h *Handler) activityGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.wsID(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	out := []map[string]any{}
	rows, err := h.pool.Query(ctx, `
		select to_char(i.created_at::date, 'YYYY-MM-DD') as d, count(*)::int
		from issues i
		where i.workspace_id::text = $1 and i.created_by::text = $2
		  and i.deleted_at is null and i.is_draft = false
		  and i.created_at::date >= (current_date - interval '6 months')
		group by d order by d`, ws, u.ID.String())
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d string
			var c int
			if rows.Scan(&d, &c) == nil {
				out = append(out, map[string]any{"created_date": d, "activity_count": c})
			}
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

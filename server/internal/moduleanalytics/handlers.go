// Package moduleanalytics computes the module "progress" (issue counts by
// state group) and "analytics"/distribution (assignee/label rollups +
// completion-chart burndown) data.
//
// Unlike cycles, Django exposes NO standalone `.../modules/{id}/progress/`
// or `.../modules/{id}/analytics/` route for modules — probed live against
// the Python reference, both 404 (see the module_analytics contract test).
// Instead this data is embedded directly in the existing module
// retrieve/list/create response: the flat `total_issues`/`backlog_issues`/…
// fields are the progress analog, and the `distribution` /
// `estimate_distribution` nested dicts are the analytics/burndown analog.
//
// This package therefore exposes pure computation functions rather than
// HTTP handlers — internal/module owns the `/modules/...` routes and wires
// these into its `moduleValues()` response builder. No routes are
// registered here.
//
// Two gaps versus Python are permanent given the existing Go schema (no
// migration is added by this package):
//   - modules has no start_date/target_date columns, so completion_chart is
//     always {} (Python's burndown_plot requires both dates to be set on
//     the module before it computes anything).
//   - there is no issue<->assignee or issue<->label join table anywhere in
//     this port (see internal/analytic's identical note on workspace
//     analytics), so Distribution's assignees/labels rows collapse to at
//     most one row per group — mirroring Python's own "issue has no
//     assignee -> one row with all-null grouping fields" LEFT JOIN
//     artifact — rather than a real per-assignee/per-label breakdown.
package moduleanalytics

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Progress returns the module's issue counts broken down by state group:
// total_issues, backlog_issues, unstarted_issues, started_issues,
// completed_issues, cancelled_issues (all int). Mirrors the flat fields
// Django's ModuleViewSet.get_queryset() annotates onto every module row.
// Issues with no state (state_id null) count toward total_issues only.
func Progress(ctx context.Context, pool *pgxpool.Pool, moduleID uuid.UUID) (map[string]any, error) {
	var backlog, unstarted, started, completed, cancelled, total int
	err := pool.QueryRow(ctx, `
		select
			coalesce(sum(case when s.group_name = 'backlog' then 1 else 0 end), 0)::int,
			coalesce(sum(case when s.group_name = 'unstarted' then 1 else 0 end), 0)::int,
			coalesce(sum(case when s.group_name = 'started' then 1 else 0 end), 0)::int,
			coalesce(sum(case when s.group_name = 'completed' then 1 else 0 end), 0)::int,
			coalesce(sum(case when s.group_name = 'cancelled' then 1 else 0 end), 0)::int,
			count(*)::int
		from module_issues mi
		join issues i on i.id = mi.issue_id and i.deleted_at is null
		left join states s on s.id = i.state_id
		where mi.module_id = $1`, moduleID,
	).Scan(&backlog, &unstarted, &started, &completed, &cancelled, &total)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total_issues":     total,
		"backlog_issues":   backlog,
		"unstarted_issues": unstarted,
		"started_issues":   started,
		"completed_issues": completed,
		"cancelled_issues": cancelled,
	}, nil
}

// Distribution returns the module's analytics/burndown payload:
// {assignees, labels, completion_chart}. completion_chart is always {} (see
// package doc). assignees/labels are each empty when the module has no
// linked issues, else a single collapsed row keyed on the module's
// total/completed issue counts (see package doc on the missing
// assignee/label join tables); "completed" here means state group
// "completed", matching what Python's own completed_at-driven filter
// resolves to for a freshly-transitioned issue.
func Distribution(ctx context.Context, pool *pgxpool.Pool, moduleID uuid.UUID) (map[string]any, error) {
	progress, err := Progress(ctx, pool, moduleID)
	if err != nil {
		return nil, err
	}
	total := progress["total_issues"].(int)
	completed := progress["completed_issues"].(int)

	assignees := []any{}
	labels := []any{}
	if total > 0 {
		pending := total - completed
		assignees = []any{map[string]any{
			"first_name": nil, "last_name": nil, "assignee_id": nil,
			"display_name": nil, "avatar_url": nil,
			"total_issues": total, "completed_issues": completed, "pending_issues": pending,
		}}
		labels = []any{map[string]any{
			"label_name": nil, "color": nil, "label_id": nil,
			"total_issues": total, "completed_issues": completed, "pending_issues": pending,
		}}
	}
	return map[string]any{
		"assignees":        assignees,
		"labels":           labels,
		"completion_chart": map[string]any{},
	}, nil
}

// EstimateDistribution returns the module's estimate_distribution payload:
// {} unless the module's project has a point-type estimate configured
// (matching Python's `estimate_type` gate), in which case it mirrors
// Distribution's shape but keyed on estimate-point totals instead of issue
// counts — still collapsed to zero-or-one row per group for the same
// missing-join-table reason (see package doc). completion_chart is always
// {} here too (it depends on the same absent module start_date/target_date
// columns as Distribution's).
func EstimateDistribution(ctx context.Context, pool *pgxpool.Pool, moduleID uuid.UUID) (map[string]any, error) {
	var hasPointEstimates bool
	err := pool.QueryRow(ctx, `
		select exists(
			select 1 from estimates e
			join modules m on m.project_id = e.project_id
			where m.id = $1 and e.type = 'points' and e.deleted_at is null
		)`, moduleID,
	).Scan(&hasPointEstimates)
	if err != nil {
		return nil, err
	}
	if !hasPointEstimates {
		return map[string]any{}, nil
	}

	var totalPts, completedPts float64
	err = pool.QueryRow(ctx, `
		select
			coalesce(sum(ep.value::float8), 0),
			coalesce(sum(case when s.group_name = 'completed' then ep.value::float8 else 0 end), 0)
		from module_issues mi
		join issues i on i.id = mi.issue_id and i.deleted_at is null and i.estimate_point is not null
		left join states s on s.id = i.state_id
		join estimate_points ep on ep.id = i.estimate_point
		where mi.module_id = $1`, moduleID,
	).Scan(&totalPts, &completedPts)
	if err != nil {
		return nil, err
	}

	assignees := []any{}
	labels := []any{}
	if totalPts > 0 {
		pending := totalPts - completedPts
		assignees = []any{map[string]any{
			"assignee_id": nil, "total_estimates": totalPts,
			"completed_estimates": completedPts, "pending_estimates": pending,
		}}
		labels = []any{map[string]any{
			"label_id": nil, "total_estimates": totalPts,
			"completed_estimates": completedPts, "pending_estimates": pending,
		}}
	}
	return map[string]any{
		"assignees":        assignees,
		"labels":           labels,
		"completion_chart": map[string]any{},
	}, nil
}

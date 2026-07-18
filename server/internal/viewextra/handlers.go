// Package viewextra serves two workspace/view-adjacent endpoints that don't
// belong in internal/view or internal/issue:
//
//   - user-favorite-views: project-scoped favorite/unfavorite of a saved
//     view (backed by the shared user_favorites table, entity_type="view").
//   - global-view-issues: GET /workspaces/{slug}/issues/, the workspace-level
//     (cross-project) issue list used by global/saved views. Reuses the
//     issue-family cursor-pagination envelope (see internal/issue.Envelope).
//
// Raw pgx (pgxpool) -- this package does not use sqlc.
//
// Contract quirks pinned by tests/test_view_extras.py against the Python
// reference (apps/api/plane/app/views/view/base.py):
//
//   - GET .../user-favorite-views/ is ALWAYS a 500
//     {"error": "Something went wrong please try again later"}, for any
//     authenticated caller regardless of membership. The Django viewset
//     wires this route to `list` but defines neither a `list()` override nor
//     a `serializer_class`; DRF's default list() blows up with an
//     AssertionError that the base view's generic handler downgrades to a
//     bare 500. We replicate the bug verbatim rather than "fixing" it into a
//     real list.
//
//   - POST create does NOT validate that `view` refers to a real IssueView
//     row (entity_identifier is a bare UUID column, not an FK) -- favoriting
//     a made-up view id still 204s. A missing `view` key is also accepted
//     (204, null entity_identifier). A non-UUID `view` value is a 400
//     {"error": "Please provide valid detail"}.
//
//   - Duplicate (view, user) favorite -> 400 {"error": "The payload is not
//     valid"} (unique constraint violation, see migration 0025).
//
//   - DELETE on a favorite that doesn't exist (never created, or already
//     deleted) -> 404 {"error": "The required object does not exist."}.
//
//   - Both endpoints require an ADMIN/MEMBER *project* member; a non-member
//     (or a bad workspace/project) gets 403 {"error": "You don't have the
//     required permissions."} -- the permission check runs before any
//     lookup, so "doesn't exist" and "not a member" look identical.
//
//   - global-view-issues aggregates issues across every project in the
//     workspace the caller is an active project member of (any role).
//
//   - `?priority=` and `?project=` are comma-separated legacy filters (mirrors
//     plane.utils.issue_filters.issue_filters); the filterset's `project_id=`
//     param is a confirmed no-op on the reference and is intentionally NOT
//     implemented here.
//
//   - `?group_by=` is a confirmed no-op: the reference's
//     WorkspaceViewIssuesViewSet.list() never passes group_by_field_name into
//     paginate(), so results always stay a flat list with grouped_by: null --
//     even for values (e.g. "state") that the project-scoped issue list
//     rejects with 400. We match by ignoring the parameter entirely.
//
//   - Requires WORKSPACE-level membership (any of ADMIN/MEMBER/GUEST); a
//     non-member (or bad workspace slug) gets 403.
//
//   - Row shape mirrors ViewIssueListSerializer: the same key set as the
//     project-scoped issue list's .values() dict (see internal/issue.Values)
//     plus an extra "state__group" field. cycle_id/module_ids/label_ids/
//     assignee_ids/sub_issues_count/attachment_count/link_count/start_date/
//     target_date are stubbed the same way internal/issue.Values already
//     stubs them (that package never joins cycle/module/label/assignee data
//     either) -- not a simplification introduced by this package.
package viewextra

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/dbx"
	"planego/internal/httpx"
	"planego/internal/issue"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	favBase := "/workspaces/{slug}/projects/{project_id}/user-favorite-views/"
	r.Get(favBase, h.listFavoriteViews)
	r.Post(favBase, h.createFavoriteView)
	r.Delete(favBase+"{view_id}/", h.deleteFavoriteView)

	r.Get("/workspaces/{slug}/issues/", h.globalViewIssues)
}

// ---- permission / scope helpers ---------------------------------------------

// resolveProjectMember enforces the PROJECT-level ADMIN/MEMBER check
// (role >= 15) that the reference's `allow_permission([ROLE.ADMIN,
// ROLE.MEMBER])` decorator (default level="PROJECT") applies to both
// user-favorite-views actions. A nonexistent workspace/project and "not an
// allowed-role member there" all collapse to the same 403, matching the
// decorator running before any object lookup.
func (h *Handler) resolveProjectMember(ctx context.Context, w http.ResponseWriter, r *http.Request) (wsID, projectID uuid.UUID, ok bool) {
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.Nil, uuid.Nil, false
	}
	u, _ := auth.UserFrom(ctx)
	err = h.pool.QueryRow(ctx, `
		select p.workspace_id
		from projects p
		join workspaces w on w.id = p.workspace_id and w.deleted_at is null
		join project_members pm on pm.project_id = p.id and pm.member_id = $3 and pm.role >= 15
		where w.slug = $1 and p.id = $2 and p.deleted_at is null
	`, chi.URLParam(r, "slug"), pid, u.ID).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return uuid.Nil, uuid.Nil, false
	}
	return wsID, pid, true
}

// resolveWorkspaceMember enforces the WORKSPACE-level check (any active
// member: ADMIN/MEMBER/GUEST) that global-view-issues requires.
func (h *Handler) resolveWorkspaceMember(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	u, _ := auth.UserFrom(ctx)
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx, `
		select w.id from workspaces w
		join workspace_members wm on wm.workspace_id = w.id and wm.member_id = $2
		where w.slug = $1 and w.deleted_at is null
	`, chi.URLParam(r, "slug"), u.ID).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return uuid.Nil, false
	}
	return wsID, true
}

// ---- user-favorite-views -----------------------------------------------------

// listFavoriteViews always 500s -- see package doc.
func (h *Handler) listFavoriteViews(w http.ResponseWriter, _ *http.Request) {
	httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
}

func (h *Handler) createFavoriteView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolveProjectMember(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	var body struct {
		View *string `json:"view"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}

	ident := dbx.NullUUID()
	if body.View != nil {
		vid, err := uuid.Parse(*body.View)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "Please provide valid detail")
			return
		}
		ident = dbx.PgUUID(vid)
	}

	_, err := h.pool.Exec(ctx,
		`insert into user_favorites (workspace_id, user_id, entity_type, entity_identifier, project_id)
		 values ($1, $2, 'view', $3, $4)`,
		wsID, u.ID, ident, pid)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteFavoriteView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolveProjectMember(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	vid, err := uuid.Parse(chi.URLParam(r, "view_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	tag, err := h.pool.Exec(ctx,
		`delete from user_favorites
		 where entity_type = 'view' and entity_identifier = $1
		   and user_id = $2 and workspace_id = $3 and project_id = $4`,
		vid, u.ID, wsID, pid)
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- global-view-issues -------------------------------------------------------

type globalIssueRow struct {
	ID             uuid.UUID
	Name           string
	StateID        pgtype.UUID
	SortOrder      float64
	CompletedAt    *time.Time
	EstimatePoint  pgtype.UUID
	Priority       string
	SequenceID     int32
	ProjectID      uuid.UUID
	ParentID       pgtype.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      pgtype.UUID
	UpdatedBy      pgtype.UUID
	IsDraft        bool
	ArchivedAt     *time.Time
	StateGroupName *string
}

const globalIssueCols = `i.id, i.name, i.state_id, i.sort_order, i.completed_at, i.estimate_point,
	i.priority, i.sequence_id, i.project_id, i.parent_id, i.created_at, i.updated_at,
	i.created_by, i.updated_by, i.is_draft, i.archived_at, s.group_name`

func scanGlobalIssue(row pgx.Row) (globalIssueRow, error) {
	var g globalIssueRow
	err := row.Scan(&g.ID, &g.Name, &g.StateID, &g.SortOrder, &g.CompletedAt, &g.EstimatePoint,
		&g.Priority, &g.SequenceID, &g.ProjectID, &g.ParentID, &g.CreatedAt, &g.UpdatedAt,
		&g.CreatedBy, &g.UpdatedBy, &g.IsDraft, &g.ArchivedAt, &g.StateGroupName)
	return g, err
}

// values mirrors ViewIssueListSerializer.to_representation: the same field
// set as internal/issue.Values plus "state__group".
func (g globalIssueRow) values() map[string]any {
	return map[string]any{
		"id":               g.ID.String(),
		"name":             g.Name,
		"state_id":         dbx.StrPtr(g.StateID),
		"sort_order":       httpx.Float(g.SortOrder),
		"completed_at":     g.CompletedAt,
		"estimate_point":   dbx.StrPtr(g.EstimatePoint),
		"priority":         g.Priority,
		"start_date":       nil,
		"target_date":      nil,
		"sequence_id":      int(g.SequenceID),
		"project_id":       g.ProjectID.String(),
		"parent_id":        dbx.StrPtr(g.ParentID),
		"cycle_id":         nil,
		"module_ids":       []string{},
		"label_ids":        []string{},
		"assignee_ids":     []string{},
		"sub_issues_count": nil,
		"created_at":       g.CreatedAt,
		"updated_at":       g.UpdatedAt,
		"created_by":       dbx.StrPtr(g.CreatedBy),
		"updated_by":       dbx.StrPtr(g.UpdatedBy),
		"attachment_count": nil,
		"link_count":       nil,
		"is_draft":         g.IsDraft,
		"archived_at":      g.ArchivedAt,
		"state__group":     g.StateGroupName,
	}
}

// splitCSV mirrors the shape of the reference's legacy comma-separated
// filters closely enough for contract purposes: split, trim, drop blanks.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && p != "null" {
			out = append(out, p)
		}
	}
	return out
}

func parseUUIDs(vals []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(vals))
	for _, v := range vals {
		if id, err := uuid.Parse(v); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func (h *Handler) globalViewIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveWorkspaceMember(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	q := `select ` + globalIssueCols + `
		from issues i
		join projects p on p.id = i.project_id and p.deleted_at is null
		join project_members pm on pm.project_id = p.id and pm.member_id = $2
		left join states s on s.id = i.state_id
		where p.workspace_id = $1 and i.deleted_at is null`
	args := []any{wsID, u.ID}

	if priorities := splitCSV(r.URL.Query().Get("priority")); len(priorities) > 0 {
		args = append(args, priorities)
		q += " and i.priority = any($" + strconv.Itoa(len(args)) + ")"
	}
	if projectIDs := parseUUIDs(splitCSV(r.URL.Query().Get("project"))); len(projectIDs) > 0 {
		args = append(args, projectIDs)
		q += " and i.project_id = any($" + strconv.Itoa(len(args)) + ")"
	}
	// NOTE: `?group_by=` is deliberately never read here -- see package doc.
	q += " order by i.created_at desc"

	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()

	vals := []map[string]any{}
	for rows.Next() {
		g, err := scanGlobalIssue(rows)
		if err != nil {
			continue
		}
		vals = append(vals, g.values())
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	httpx.JSON(w, http.StatusOK, issue.Envelope(vals, len(vals), nil))
}

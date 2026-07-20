// Package wslist fills workspace-level "list" gaps left uncovered by
// internal/workspace: the bare `GET /workspaces/` collection endpoint and
// `GET /workspaces/{slug}/project-members/`.
//
// Django reference: apps/api/plane/app/urls/workspace.py + apps/api/plane/app/views/workspace/{base,member}.py.
//
// GET /workspaces/ is a deliberate bug-for-bug port, not a real "list my
// workspaces" implementation: WorkSpaceViewSet.list is decorated with
// @allow_permission([ROLE.ADMIN, ROLE.MEMBER, ROLE.GUEST], level="WORKSPACE"),
// whose WORKSPACE-level branch unconditionally does kwargs["slug"] to look up
// the caller's role. The `list` route (`/workspaces/`, no {slug} segment) never
// populates that kwarg, so every authenticated call raises a bare KeyError,
// which BaseViewSet.handle_exception converts into a fixed
// {"error": "The required key does not exist."} / 400 response. This happens
// for every authenticated caller regardless of workspace membership -- verified
// live against the Python reference (frozen in
// api-go-contract/tests/test_workspace_list.py). The actual "list my
// workspaces" data endpoint the frontend uses is GET /users/me/workspaces/,
// already implemented in internal/workspace.
//
// GET /workspaces/{slug}/project-members/ mirrors WorkspaceProjectMemberEndpoint:
// for every project in this workspace that the caller is a member of, list all
// members of that project, keyed by project id.
package wslist

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes registers this package's endpoints. Must be mounted inside the
// authenticated group (behind auth.Require) alongside internal/workspace's
// Routes -- chi permits GET and POST on the same literal path ("/workspaces/")
// to be registered from different packages/handlers, since chi dispatches by
// method, not by registrant.
func (h *Handler) Routes(r chi.Router) {
	// Both slash variants: the frontend/clients hit /workspaces and /workspaces/
	// and Django answers both with the same crash-derived 400.
	r.Get("/workspaces", h.list)
	r.Get("/workspaces/", h.list)
	r.Get("/workspaces/{slug}/project-members/", h.projectMembers)
}

// list reproduces the Django reference's crash-turned-error response for
// GET /workspaces/ -- see package doc. No DB access needed: the body is
// identical for every authenticated caller.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	httpx.Error(w, http.StatusBadRequest, "The required key does not exist.")
}

type projectMemberEntry struct {
	ID           uuid.UUID `json:"id"`
	Role         int       `json:"role"`
	Member       uuid.UUID `json:"member"`
	OriginalRole int       `json:"original_role"`
	CreatedAt    time.Time `json:"created_at"`
}

func (h *Handler) projectMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	// Mirrors WorkspaceProjectMemberEndpoint.get: restrict to projects in this
	// workspace where the caller is themself a project member, then list every
	// member of those projects.
	rows, err := h.pool.Query(ctx, `
		select pm.id, pm.project_id, pm.role, pm.member_id, pm.created_at
		from project_members pm
		where pm.workspace_id = $1
		  and pm.project_id in (select project_id from project_members where member_id = $2)
		order by pm.created_at`,
		wsID, u.ID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	defer rows.Close()

	out := map[string][]projectMemberEntry{}
	for rows.Next() {
		var (
			id, projectID, memberID uuid.UUID
			role                    int16
			createdAt               time.Time
		)
		if err := rows.Scan(&id, &projectID, &role, &memberID, &createdAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
			return
		}
		out[projectID.String()] = append(out[projectID.String()], projectMemberEntry{
			ID:           id,
			Role:         int(role),
			Member:       memberID,
			OriginalRole: int(role),
			CreatedAt:    createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}

	httpx.JSON(w, http.StatusOK, out)
}

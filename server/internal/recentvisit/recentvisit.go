// Package recentvisit records and lists a user's recently-visited entities.
// In Plane's Django backend the write is the recent_visited_task Celery task
// (fired from the project/page/module/cycle/view retrieve views); here Record
// runs in a bg.Dispatcher goroutine off the request path, and List backs the
// GET /workspaces/{slug}/recent-visits/ endpoint.
package recentvisit

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

// Handler serves the recent-visits read endpoint.
type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/recent-visits/", h.list)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	var wsID uuid.UUID
	if err := h.pool.QueryRow(ctx,
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&wsID); err != nil {
		httpx.JSON(w, http.StatusOK, []any{})
		return
	}
	httpx.JSON(w, http.StatusOK, List(ctx, h.pool, wsID, u.ID))
}

// keepN caps the stored history per (user, workspace), matching Django's trim.
const keepN = 20

// Record upserts a visit (bumping visited_at when the entity was seen before),
// then trims the user's history to the most recent keepN entries. Safe to run
// from a background goroutine.
func Record(ctx context.Context, pool *pgxpool.Pool, wsID, userID, projectID, entityID uuid.UUID, entityName string) {
	var proj any
	if projectID != uuid.Nil {
		proj = projectID
	}
	_, err := pool.Exec(ctx, `
		insert into user_recent_visits (workspace_id, user_id, project_id, entity_name, entity_identifier, visited_at)
		values ($1,$2,$3,$4,$5, now())
		on conflict (user_id, workspace_id, entity_name, entity_identifier)
		do update set visited_at = now(), updated_at = now()`,
		wsID, userID, proj, entityName, entityID)
	if err != nil {
		return
	}
	// trim to the most recent keepN
	_, _ = pool.Exec(ctx, `
		delete from user_recent_visits
		where user_id=$1 and workspace_id=$2 and id not in (
			select id from user_recent_visits
			where user_id=$1 and workspace_id=$2
			order by visited_at desc limit $3
		)`, userID, wsID, keepN)
}

// List returns the user's recent visits (newest first) with each entity's data
// embedded, matching the recent-visits serializer.
func List(ctx context.Context, pool *pgxpool.Pool, wsID, userID uuid.UUID) []map[string]any {
	rows, err := pool.Query(ctx, `
		select v.id, v.entity_name, v.entity_identifier, v.visited_at,
		       p.id, p.name, p.identifier
		from user_recent_visits v
		left join projects p on p.id = v.entity_identifier and v.entity_name = 'project'
		where v.user_id = $1 and v.workspace_id = $2
		order by v.visited_at desc`, userID, wsID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, entityID          uuid.UUID
			entityName            string
			visitedAt             any
			pID                   *uuid.UUID
			pName, pIdent         *string
		)
		if rows.Scan(&id, &entityName, &entityID, &visitedAt, &pID, &pName, &pIdent) != nil {
			continue
		}
		var entityData any
		if pID != nil {
			entityData = map[string]any{
				"id":              pID.String(),
				"name":            deref(pName),
				"identifier":      deref(pIdent),
				"logo_props":      map[string]any{},
				"project_members": []any{},
			}
		}
		out = append(out, map[string]any{
			"id":                id.String(),
			"entity_name":       entityName,
			"entity_identifier": entityID.String(),
			"entity_data":       entityData,
			"visited_at":        visitedAt,
		})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

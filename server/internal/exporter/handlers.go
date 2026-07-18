// Package exporter implements the workspace issue-export endpoints
// (Django: apps/api/plane/app/urls/exporter.py -> ExportIssuesEndpoint).
//
// POST enqueues an export job (Django hands it to a celery task that
// uploads a zip to S3/MinIO; this port has no background-worker
// equivalent, so the job is recorded as "queued" and left there — the
// contract only pins the create/list shapes, not that the job ever
// actually completes). GET lists the workspace's export jobs through the
// same cursor-pagination envelope the issue family uses.
package exporter

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
	"planego/internal/issue"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/export-issues/"
	r.Post(base, h.create)
	r.Get(base, h.list)
}

var validProviders = map[string]bool{"csv": true, "xlsx": true, "json": true}

func (h *Handler) resolveWorkspace(r *http.Request) (uuid.UUID, error) {
	var wsID uuid.UUID
	err := h.pool.QueryRow(r.Context(),
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&wsID)
	return wsID, err
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, err := h.resolveWorkspace(r)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	var body struct {
		Provider *string  `json:"provider"`
		Multiple bool     `json:"multiple"`
		Project  []string `json:"project"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Mirrors Django's `request.data.get("provider", False)`: an absent key
	// renders as the literal string "False" in the error message below.
	providerDisplay := "False"
	provider := ""
	if body.Provider != nil {
		providerDisplay = *body.Provider
		provider = *body.Provider
	}

	if !validProviders[provider] {
		httpx.Error(w, http.StatusBadRequest, "Provider '"+providerDisplay+"' not found.")
		return
	}

	projectIDs := make([]uuid.UUID, 0, len(body.Project))
	for _, p := range body.Project {
		if id, err := uuid.Parse(p); err == nil {
			projectIDs = append(projectIDs, id)
		}
	}
	if len(projectIDs) == 0 {
		// Default: every active project in the workspace the caller is a
		// member of (mirrors the Django view's fallback query).
		rows, err := h.pool.Query(ctx, `
			select p.id
			from projects p
			join project_members pm on pm.project_id = p.id
			where p.workspace_id = $1 and pm.member_id = $2
				and p.archived_at is null and p.deleted_at is null`,
			wsID, u.ID)
		if err == nil {
			for rows.Next() {
				var pid uuid.UUID
				if err := rows.Scan(&pid); err == nil {
					projectIDs = append(projectIDs, pid)
				}
			}
			rows.Close()
		}
	}

	token := randToken()
	_, err = h.pool.Exec(ctx, `
		insert into exporters (workspace_id, project, initiated_by_id, provider, type, token, created_by)
		values ($1, $2, $3, $4, 'issue_exports', $5, $3)`,
		wsID, projectIDs, u.ID, provider, token,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]string{
		"message": "Once the export is ready you will be able to download it",
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, err := h.resolveWorkspace(r)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	if r.URL.Query().Get("per_page") == "" || r.URL.Query().Get("cursor") == "" {
		httpx.Error(w, http.StatusBadRequest, "per_page and cursor are required")
		return
	}

	rows, err := h.pool.Query(ctx, `
		select e.id, e.created_at, e.updated_at, e.project, e.provider, e.status, e.url,
			e.initiated_by_id, e.token, e.created_by, e.updated_by,
			u.first_name, u.last_name, u.avatar, u.is_bot, u.display_name
		from exporters e
		join users u on u.id = e.initiated_by_id
		where e.workspace_id = $1 and e.type = 'issue_exports' and e.deleted_at is null
		order by e.created_at desc`, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()

	results := []map[string]any{}
	for rows.Next() {
		m, err := scanRow(rows)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		results = append(results, m)
	}

	httpx.JSON(w, http.StatusOK, issue.Envelope(results, len(results), nil))
}

func scanRow(row pgx.Row) (map[string]any, error) {
	var (
		id                          uuid.UUID
		createdAt, updatedAt        time.Time
		project                     []uuid.UUID
		provider, status            string
		url                         *string
		initiatedBy                 uuid.UUID
		token                       string
		createdBy, updatedBy        *uuid.UUID
		firstName, lastName, avatar string
		isBot                       bool
		displayName                 string
	)
	if err := row.Scan(&id, &createdAt, &updatedAt, &project, &provider, &status, &url,
		&initiatedBy, &token, &createdBy, &updatedBy,
		&firstName, &lastName, &avatar, &isBot, &displayName); err != nil {
		return nil, err
	}

	projectStrs := make([]string, len(project))
	for i, p := range project {
		projectStrs[i] = p.String()
	}

	var avatarURL any
	if avatar != "" {
		avatarURL = avatar
	}

	return map[string]any{
		"id":           id.String(),
		"created_at":   createdAt,
		"updated_at":   updatedAt,
		"project":      projectStrs,
		"provider":     provider,
		"status":       status,
		"url":          url,
		"initiated_by": initiatedBy.String(),
		"initiated_by_detail": map[string]any{
			"id":           initiatedBy.String(),
			"first_name":   firstName,
			"last_name":    lastName,
			"avatar":       avatar,
			"avatar_url":   avatarURL,
			"is_bot":       isBot,
			"display_name": displayName,
		},
		"token":      token,
		"created_by": strPtr(createdBy),
		"updated_by": strPtr(updatedBy),
	}, nil
}

func strPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func randToken() string {
	id := uuid.New()
	b := id[:]
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

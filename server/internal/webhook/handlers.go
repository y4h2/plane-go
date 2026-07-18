// Package webhook serves workspace-scoped webhook CRUD, secret regeneration,
// and the (always-empty, since nothing writes to it) webhook-logs listing.
//
// Contract quirks pinned by tests/test_webhooks.py against the Python
// reference (apps/api/plane/app/views/webhook/base.py):
//
//   - The Django view passes a `fields=(...)` restriction into
//     WebhookSerializer intending to hide secret_key from list/retrieve/patch
//     responses, but DynamicBaseSerializer.__init__ unconditionally
//     overwrites that kwarg with an unrelated `expand` kwarg (defaulting to
//     `[]`), making the restriction dead code. Every response — create,
//     list, retrieve, patch, regenerate — therefore returns the full field
//     set, including secret_key, workspace, created_by, updated_by,
//     deleted_at, is_internal, and version. We replicate that (accidental)
//     behavior verbatim rather than "fixing" it.
//   - Create returns 201.
//   - Duplicate (workspace, url) -> 409 {"error": "URL already exists for
//     the workspace"} (a partial-unique-index violation mapped explicitly,
//     not a generic 400).
//   - A missing/foreign workspace slug -> 403 {"error": "You don't have the
//     required permissions."}, not 404: the admin-role permission check
//     resolves workspace + membership together, so "no such workspace" and
//     "not an admin there" look identical.
//   - url goes through basic shape/scheme/localhost checks (all reported as
//     a list under the "url" key, mirroring DRF field-validator errors) plus
//     a best-effort SSRF check (DNS resolution + private/loopback block,
//     reported as a bare string under "url", mirroring the manual
//     serializer-level check) — see validateWebhookURL.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/webhooks/"
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{webhook_id}/", h.retrieve)
	r.Patch(base+"{webhook_id}/", h.update)
	r.Delete(base+"{webhook_id}/", h.destroy)
	r.Post(base+"{webhook_id}/regenerate/", h.regenerate)

	r.Get("/workspaces/{slug}/webhook-logs/{webhook_id}/", h.logs)
}

// --- row / response shaping -------------------------------------------------

type row struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	URL          string
	IsActive     bool
	SecretKey    string
	Project      bool
	Issue        bool
	Module       bool
	Cycle        bool
	IssueComment bool
	IsInternal   bool
	Version      string
	CreatedBy    pgtype.UUID
	UpdatedBy    pgtype.UUID
	DeletedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const rowCols = `id, workspace_id, url, is_active, secret_key, project, issue, module, cycle,
	issue_comment, is_internal, version, created_by, updated_by, deleted_at, created_at, updated_at`

func scanRow(rw pgx.Row) (row, error) {
	var wh row
	err := rw.Scan(
		&wh.ID, &wh.WorkspaceID, &wh.URL, &wh.IsActive, &wh.SecretKey, &wh.Project, &wh.Issue,
		&wh.Module, &wh.Cycle, &wh.IssueComment, &wh.IsInternal, &wh.Version,
		&wh.CreatedBy, &wh.UpdatedBy, &wh.DeletedAt, &wh.CreatedAt, &wh.UpdatedAt,
	)
	return wh, err
}

func resp(wh row) map[string]any {
	return map[string]any{
		"id":            wh.ID.String(),
		"url":           wh.URL,
		"created_at":    wh.CreatedAt,
		"updated_at":    wh.UpdatedAt,
		"deleted_at":    wh.DeletedAt,
		"is_active":     wh.IsActive,
		"secret_key":    wh.SecretKey,
		"project":       wh.Project,
		"issue":         wh.Issue,
		"module":        wh.Module,
		"cycle":         wh.Cycle,
		"issue_comment": wh.IssueComment,
		"is_internal":   wh.IsInternal,
		"version":       wh.Version,
		"created_by":    dbx.StrPtr(wh.CreatedBy),
		"updated_by":    dbx.StrPtr(wh.UpdatedBy),
		"workspace":     wh.WorkspaceID.String(),
	}
}

// --- permission / lookup helpers --------------------------------------------

// resolveAdminWorkspace mirrors Django's WORKSPACE-level ROLE.ADMIN permission
// check: it resolves the workspace and the caller's membership role in a
// single query, so a nonexistent workspace and "not an admin there" both
// surface as the same generic 403 (never a 404).
func (h *Handler) resolveAdminWorkspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return uuid.UUID{}, false
	}
	slug := chi.URLParam(r, "slug")
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx, `
		select ws.id
		from workspaces ws
		join workspace_members wm on wm.workspace_id = ws.id and wm.member_id = $2
		where ws.slug = $1 and ws.deleted_at is null and wm.role >= 20
	`, slug, u.ID).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return uuid.UUID{}, false
	}
	return wsID, true
}

func (h *Handler) getWebhook(ctx context.Context, wsID, wid uuid.UUID) (row, error) {
	q := `select ` + rowCols + ` from webhooks where id = $1 and workspace_id = $2 and deleted_at is null`
	return scanRow(h.pool.QueryRow(ctx, q, wid, wsID))
}

// --- handlers ----------------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)

	var body struct {
		URL          *string `json:"url"`
		IsActive     *bool   `json:"is_active"`
		Project      *bool   `json:"project"`
		Issue        *bool   `json:"issue"`
		Module       *bool   `json:"module"`
		Cycle        *bool   `json:"cycle"`
		IssueComment *bool   `json:"issue_comment"`
		IsInternal   *bool   `json:"is_internal"`
		Version      *string `json:"version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.URL == nil || strings.TrimSpace(*body.URL) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"url": []string{"This field is required."}})
		return
	}
	if verrs, plain := validateWebhookURL(*body.URL); len(verrs) > 0 || plain != "" {
		writeURLError(w, verrs, plain)
		return
	}

	isActive := true
	if body.IsActive != nil {
		isActive = *body.IsActive
	}
	version := "v1"
	if body.Version != nil && *body.Version != "" {
		version = *body.Version
	}
	secret := generateSecret()

	q := `
		insert into webhooks (workspace_id, url, is_active, secret_key, project, issue, module,
			cycle, issue_comment, is_internal, version, created_by, updated_by)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		returning ` + rowCols
	wh, err := scanRow(h.pool.QueryRow(ctx, q,
		wsID, *body.URL, isActive, secret,
		boolOr(body.Project), boolOr(body.Issue), boolOr(body.Module), boolOr(body.Cycle), boolOr(body.IssueComment),
		boolOr(body.IsInternal), version, dbx.PgUUID(u.ID),
	))
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Error(w, http.StatusConflict, "URL already exists for the workspace")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, resp(wh))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	q := `select ` + rowCols + ` from webhooks where workspace_id = $1 and deleted_at is null order by created_at desc`
	rows, err := h.pool.Query(ctx, q, wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		wh, err := scanRow(rows)
		if err != nil {
			continue
		}
		out = append(out, resp(wh))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	wid, err := uuid.Parse(chi.URLParam(r, "webhook_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	wh, err := h.getWebhook(ctx, wsID, wid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(wh))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	wid, err := uuid.Parse(chi.URLParam(r, "webhook_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	cur, err := h.getWebhook(ctx, wsID, wid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	var body struct {
		URL          *string `json:"url"`
		IsActive     *bool   `json:"is_active"`
		Project      *bool   `json:"project"`
		Issue        *bool   `json:"issue"`
		Module       *bool   `json:"module"`
		Cycle        *bool   `json:"cycle"`
		IssueComment *bool   `json:"issue_comment"`
		IsInternal   *bool   `json:"is_internal"`
		Version      *string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}

	newURL := cur.URL
	if body.URL != nil && *body.URL != "" {
		if verrs, plain := validateWebhookURL(*body.URL); len(verrs) > 0 || plain != "" {
			writeURLError(w, verrs, plain)
			return
		}
		newURL = *body.URL
	}
	u, _ := auth.UserFrom(ctx)

	q := `
		update webhooks set
			url = $1, is_active = $2, project = $3, issue = $4, module = $5, cycle = $6,
			issue_comment = $7, is_internal = $8, version = $9, updated_by = $10, updated_at = now()
		where id = $11 and workspace_id = $12
		returning ` + rowCols
	wh, err := scanRow(h.pool.QueryRow(ctx, q,
		newURL,
		boolOrDefault(body.IsActive, cur.IsActive),
		boolOrDefault(body.Project, cur.Project),
		boolOrDefault(body.Issue, cur.Issue),
		boolOrDefault(body.Module, cur.Module),
		boolOrDefault(body.Cycle, cur.Cycle),
		boolOrDefault(body.IssueComment, cur.IssueComment),
		boolOrDefault(body.IsInternal, cur.IsInternal),
		strOrDefault(body.Version, cur.Version),
		dbx.PgUUID(u.ID),
		wid, wsID,
	))
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Error(w, http.StatusConflict, "URL already exists for the workspace")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(wh))
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	wid, err := uuid.Parse(chi.URLParam(r, "webhook_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	tag, err := h.pool.Exec(ctx, `delete from webhooks where id = $1 and workspace_id = $2`, wid, wsID)
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) regenerate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	wid, err := uuid.Parse(chi.URLParam(r, "webhook_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	q := `
		update webhooks set secret_key = $1, updated_by = $2, updated_at = now()
		where id = $3 and workspace_id = $4 and deleted_at is null
		returning ` + rowCols
	wh, err := scanRow(h.pool.QueryRow(ctx, q, generateSecret(), dbx.PgUUID(u.ID), wid, wsID))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, resp(wh))
}

// logs lists webhook delivery logs. Nothing in this port writes to
// webhook_logs (delivery is an async background worker in the Django
// reference), so this always returns an empty list — which matches the
// reference in any environment where no deliveries have actually fired.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.resolveAdminWorkspace(ctx, w, r)
	if !ok {
		return
	}
	wid, err := uuid.Parse(chi.URLParam(r, "webhook_id"))
	if err != nil {
		httpx.JSON(w, http.StatusOK, []any{})
		return
	}
	rows, err := h.pool.Query(ctx, `
		select id, event_type, request_method, response_status, retry_count, created_at
		from webhook_logs where workspace_id = $1 and webhook = $2 and deleted_at is null
		order by created_at desc
	`, wsID, wid)
	if err != nil {
		httpx.JSON(w, http.StatusOK, []any{})
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id                                       uuid.UUID
			eventType, requestMethod, responseStatus *string
			retryCount                               int16
			createdAt                                time.Time
		)
		if err := rows.Scan(&id, &eventType, &requestMethod, &responseStatus, &retryCount, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":              id.String(),
			"event_type":      eventType,
			"request_method":  requestMethod,
			"response_status": responseStatus,
			"retry_count":     int(retryCount),
			"created_at":      createdAt,
			"webhook":         wid.String(),
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// --- small helpers -------------------------------------------------------

func boolOr(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func boolOrDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func strOrDefault(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func generateSecret() string {
	return "plane_wh_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// writeURLError renders the "url" validation failure. Django reports two
// distinct shapes depending on which layer rejected the URL: field-level
// validators (bad scheme, malformed URL, localhost) produce a list of
// messages; the manual SSRF check produces a bare string.
func writeURLError(w http.ResponseWriter, listErrs []string, plain string) {
	if len(listErrs) > 0 {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"url": listErrs})
		return
	}
	httpx.JSON(w, http.StatusBadRequest, map[string]any{"url": plain})
}

// validateWebhookURL replicates the reference's URL validation:
//  1. field-level checks (parseable absolute http/https URL, not localhost)
//     -> returned as listErrs (DRF-style field error list)
//  2. a best-effort SSRF check (DNS resolution + private/loopback/link-local
//     block) -> returned as plain (a bare string, matching the manual
//     serializer-level ValidationError in the reference)
//
// Exactly one of the two returns is non-empty when validation fails; both are
// empty when the URL is acceptable.
func validateWebhookURL(raw string) (listErrs []string, plain string) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return []string{"Enter a valid URL."}, ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return []string{"Invalid schema. Only HTTP and HTTPS are allowed."}, ""
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return []string{"Enter a valid URL."}, ""
	}
	if hostname == "localhost" || hostname == "127.0.0.1" {
		return []string{"Local URLs are not allowed."}, ""
	}

	ips, err := net.LookupIP(hostname)
	if err != nil || len(ips) == 0 {
		return nil, "Invalid or disallowed webhook URL."
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return nil, "Invalid or disallowed webhook URL."
		}
	}
	return nil, ""
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

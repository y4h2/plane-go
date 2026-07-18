// Package issuetail serves two read-only issue endpoints that sit outside the
// core issue CRUD surface:
//
//   - GET /workspaces/{slug}/projects/{project_id}/deleted-issues/ -- lists
//     issue ids that are soft-deleted OR archived, as a **bare list of id
//     strings** (not the cursor envelope, not `.values()` dicts).
//   - GET /workspaces/{slug}/work-items/{project_identifier}-{issue_identifier}/
//     -- looks up a single issue by its human identifier (e.g. "ALPHA-1") and
//     returns the full IssueDetailSerializer shape.
//
// Wire-contract quirks mirrored from Plane's Django reference:
//   - deleted-issues is gated by DRF's `allow_permission([ADMIN, MEMBER,
//     GUEST], level="PROJECT")`, which runs BEFORE any object lookup: an
//     unknown workspace slug, an unknown/foreign project id, AND a caller who
//     isn't a project member all collapse to the SAME 403
//     {"error":"You don't have the required permissions."} -- never 404.
//     Results are ordered newest-created first (`-created_at`).
//   - The identifier route has no such decorator; it looks the project up
//     directly (case-insensitive on `identifier`) and 404s
//     {"error":"The required object does not exist."} on an unknown
//     workspace/project/sequence or a soft-deleted issue. A non-numeric
//     sequence half is a 400 {"error":"Invalid issue identifier"}. Only
//     *after* the issue is found does it check project membership, yielding
//     403 {"error":"You are not allowed to view this issue"} (no trailing
//     period -- unlike every other error string in this codebase) for a
//     non-member. Workspace membership is never checked.
//   - The identifier response reuses the issue `.values()` field set
//     (internal/issue.Values) plus description_html/is_subscribed/is_intake,
//     minus deleted_at (not emitted here). Unlike the bare `.values()` dict,
//     sub_issues_count/attachment_count/link_count are real ints (0 default),
//     not null -- Django annotates them with Count(...) here.
package issuetail

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/httpx"
	"planego/internal/issue"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/projects/{project_id}/deleted-issues/", h.deletedIssues)
	r.Get("/workspaces/{slug}/work-items/{identifier}/", h.byIdentifier)
}

// ---- GET .../deleted-issues/ ------------------------------------------------

func (h *Handler) deletedIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)

	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	// The reference's permission decorator runs before any object lookup: an
	// unknown workspace slug, an unknown/foreign project id, and a
	// non-member all fail this single membership check and yield the same
	// 403 (never 404).
	var isMember bool
	err = h.pool.QueryRow(ctx, `
		select true
		from project_members pm
		join workspaces w on w.id = pm.workspace_id and w.deleted_at is null
		where w.slug = $1 and pm.project_id = $2 and pm.member_id = $3
	`, chi.URLParam(r, "slug"), pid, u.ID).Scan(&isMember)
	if err != nil || !isMember {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return
	}

	q := `select id from issues where project_id = $1 and (archived_at is not null or deleted_at is not null)`
	args := []any{pid}
	if v := r.URL.Query().Get("updated_at__gt"); v != "" {
		if ts, perr := parseTimestamp(v); perr == nil {
			args = append(args, ts)
			q += " and updated_at > $" + strconv.Itoa(len(args))
		}
	}
	q += " order by created_at desc"

	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id.String())
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, ids)
}

// parseTimestamp accepts RFC3339 (with or without fractional seconds) --
// close enough to Django's flexible ISO-8601 parsing for contract purposes.
func parseTimestamp(v string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return ts, nil
	}
	return time.Parse(time.RFC3339, v)
}

// ---- GET .../work-items/{project_identifier}-{issue_identifier}/ ----------

const issueCols = `id, workspace_id, project_id, name, description_html, priority, state_id, parent_id,
	estimate_point, sequence_id, sort_order, start_date, target_date, completed_at, is_draft,
	archived_at, created_by, updated_by, deleted_at, created_at, updated_at`

func scanIssue(row pgx.Row) (gen.Issue, error) {
	var i gen.Issue
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.ProjectID, &i.Name, &i.DescriptionHtml, &i.Priority,
		&i.StateID, &i.ParentID, &i.EstimatePoint, &i.SequenceID, &i.SortOrder, &i.StartDate,
		&i.TargetDate, &i.CompletedAt, &i.IsDraft, &i.ArchivedAt, &i.CreatedBy, &i.UpdatedBy,
		&i.DeletedAt, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

// strictParseInt mirrors the reference's strict_str_to_int: digits only, or a
// '-' prefix followed by digits. strconv.Atoi alone is too permissive (it
// accepts a leading "+", which the reference rejects).
func strictParseInt(s string) (int, bool) {
	digits := s
	if strings.HasPrefix(s, "-") {
		digits = s[1:]
	}
	if digits == "" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (h *Handler) byIdentifier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)

	raw := chi.URLParam(r, "identifier")
	idx := strings.LastIndex(raw, "-")
	if idx <= 0 || idx == len(raw)-1 {
		httpx.Error(w, http.StatusBadRequest, "Invalid issue identifier")
		return
	}
	projectIdentifier, seqStr := raw[:idx], raw[idx+1:]
	seq, ok := strictParseInt(seqStr)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "Invalid issue identifier")
		return
	}

	var pid uuid.UUID
	err := h.pool.QueryRow(ctx, `
		select p.id
		from projects p
		join workspaces w on w.id = p.workspace_id and w.deleted_at is null
		where w.slug = $1 and p.deleted_at is null and lower(p.identifier) = lower($2)
	`, chi.URLParam(r, "slug"), projectIdentifier).Scan(&pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	i, err := scanIssue(h.pool.QueryRow(ctx, `select `+issueCols+`
		from issues where project_id = $1 and sequence_id = $2 and deleted_at is null`, pid, seq))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}

	var isMember bool
	_ = h.pool.QueryRow(ctx, `select true from project_members where project_id = $1 and member_id = $2`,
		pid, u.ID).Scan(&isMember)
	if !isMember {
		httpx.Error(w, http.StatusForbidden, "You are not allowed to view this issue")
		return
	}

	m := issue.Values(i)
	delete(m, "deleted_at")
	m["description_html"] = i.DescriptionHtml
	m["is_subscribed"] = h.isSubscribed(ctx, i.ID, u.ID)
	m["is_intake"] = h.isIntake(ctx, i.ID)
	m["sub_issues_count"] = h.subIssuesCount(ctx, i.ID)
	m["attachment_count"] = h.attachmentCount(ctx, i.ID)
	m["link_count"] = h.linkCount(ctx, i.ID)
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) isSubscribed(ctx context.Context, issueID, memberID uuid.UUID) bool {
	var v bool
	_ = h.pool.QueryRow(ctx,
		`select true from issue_subscribers where issue_id = $1 and subscriber_id = $2`,
		issueID, memberID).Scan(&v)
	return v
}

func (h *Handler) isIntake(ctx context.Context, issueID uuid.UUID) bool {
	var v bool
	_ = h.pool.QueryRow(ctx,
		`select true from intake_issues where issue_id = $1 and status in (-2, 0)`,
		issueID).Scan(&v)
	return v
}

func (h *Handler) subIssuesCount(ctx context.Context, issueID uuid.UUID) int {
	var n int
	_ = h.pool.QueryRow(ctx,
		`select count(*) from issues where parent_id = $1 and deleted_at is null`,
		issueID).Scan(&n)
	return n
}

func (h *Handler) attachmentCount(ctx context.Context, issueID uuid.UUID) int {
	var n int
	_ = h.pool.QueryRow(ctx,
		`select count(*) from assets where entity_type = 'ISSUE_ATTACHMENT' and entity_identifier = $1 and is_uploaded = true`,
		issueID.String()).Scan(&n)
	return n
}

func (h *Handler) linkCount(ctx context.Context, issueID uuid.UUID) int {
	var n int
	_ = h.pool.QueryRow(ctx, `select count(*) from issue_links where issue_id = $1`, issueID).Scan(&n)
	return n
}

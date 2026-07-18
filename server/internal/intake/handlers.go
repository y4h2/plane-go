// Package intake serves the intake / triage-inbox endpoints. Intake is the inbox
// where incoming work items land for triage.
//
// Wire-contract quirks mirrored from Plane's Django reference:
//   - GET /intakes/ returns a SINGLE object (not a list) with a project_detail
//     annotation and a pending_issue_count (# of status=-2 rows).
//   - The default intake is auto-created lazily on first access (the reference
//     creates it when the project is PATCHed with inbox_view=true; we don't own
//     the project package, so every intake handler ensures it exists).
//   - POST /intake-issues/ takes a nested {"issue": {...}} body, creates the
//     underlying issue in the project's TRIAGE state AND the intake_issue row,
//     and returns 200 (not 201).
//   - status is an int enum: -2 pending, -1 rejected, 0 snoozed, 1 accepted,
//     2 duplicate. GET /intake-issues/ defaults to status=-2 (pending) unless a
//     ?status= filter overrides it.
//   - PATCH status=1 (accept) moves the issue out of TRIAGE into the project's
//     default state.
//   - retrieve/patch/delete key on the ISSUE id (not the intake_issue id).
package intake

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
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	// intake-* and inbox-* are aliases in the reference; register both.
	for _, seg := range []string{"intakes", "inboxes"} {
		base := "/workspaces/{slug}/projects/{project_id}/" + seg + "/"
		r.Get(base, h.getIntake)
		r.Post(base, h.getIntake) // create is effectively a no-op read here
		r.Get(base+"{pk}/", h.getIntake)
	}
	for _, seg := range []string{"intake-issues", "inbox-issues"} {
		base := "/workspaces/{slug}/projects/{project_id}/" + seg + "/"
		r.Get(base, h.listIssues)
		r.Post(base, h.createIssue)
		r.Get(base+"{issue_id}/", h.retrieveIssue)
		r.Patch(base+"{issue_id}/", h.updateIssue)
		r.Delete(base+"{issue_id}/", h.destroyIssue)
	}
}

// triageStateID mirrors internal/state.intakeState: a stable synthetic id per
// project (we don't persist a triage state row, and issues.state_id has no FK).
func triageStateID(pid uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("triage:"+pid.String()))
}

// ---- scope helpers ---------------------------------------------------------

func (h *Handler) scope(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.Nil, uuid.Nil, false
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.Nil, uuid.Nil, false
	}
	return wsID, pid, true
}

// ensureIntake returns the project's default intake id, creating it if missing.
func (h *Handler) ensureIntake(ctx context.Context, wsID, pid uuid.UUID, actor pgtype.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select id from intakes where project_id=$1 and deleted_at is null order by created_at limit 1`,
		pid).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return uuid.Nil, err
	}
	var projName string
	_ = h.pool.QueryRow(ctx, `select name from projects where id=$1`, pid).Scan(&projName)
	err = h.pool.QueryRow(ctx,
		`insert into intakes (workspace_id, project_id, name, is_default, created_by)
		 values ($1,$2,$3,true,$4) returning id`,
		wsID, pid, strings.TrimSpace(projName+" Intake"), actor).Scan(&id)
	return id, err
}

func (h *Handler) defaultStateID(ctx context.Context, pid uuid.UUID) (uuid.UUID, bool) {
	var id uuid.UUID
	if err := h.pool.QueryRow(ctx,
		`select id from states where project_id=$1 and is_default=true limit 1`, pid).Scan(&id); err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func actorID(ctx context.Context) pgtype.UUID {
	if u, ok := auth.UserFrom(ctx); ok {
		return dbx.PgUUID(u.ID)
	}
	return dbx.NullUUID()
}

// ---- intake object ---------------------------------------------------------

func (h *Handler) getIntake(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actorID(ctx))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var (
		name, desc           string
		isDefault            bool
		createdAt, updatedAt time.Time
		deletedAt            *time.Time
		createdBy, updatedBy pgtype.UUID
		viewProps, logoProps []byte
	)
	err = h.pool.QueryRow(ctx,
		`select name, description, is_default, view_props, logo_props, created_by, updated_by, deleted_at, created_at, updated_at
		   from intakes where id=$1`, intakeID).
		Scan(&name, &desc, &isDefault, &viewProps, &logoProps, &createdBy, &updatedBy, &deletedAt, &createdAt, &updatedAt)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var pending int
	_ = h.pool.QueryRow(ctx,
		`select count(*) from intake_issues where intake_id=$1 and status=-2 and deleted_at is null`,
		intakeID).Scan(&pending)

	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":                  intakeID.String(),
		"project_detail":      h.projectDetail(ctx, pid),
		"pending_issue_count": pending,
		"created_at":          createdAt,
		"updated_at":          updatedAt,
		"deleted_at":          deletedAt,
		"name":                name,
		"description":         desc,
		"is_default":          isDefault,
		"view_props":          rawOrEmptyObj(viewProps),
		"logo_props":          rawOrEmptyObj(logoProps),
		"created_by":          dbx.StrPtr(createdBy),
		"updated_by":          dbx.StrPtr(updatedBy),
		"project":             pid.String(),
		"workspace":           wsID.String(),
	})
}

func (h *Handler) projectDetail(ctx context.Context, pid uuid.UUID) map[string]any {
	var name, identifier, desc string
	if err := h.pool.QueryRow(ctx,
		`select name, identifier, description from projects where id=$1`, pid).
		Scan(&name, &identifier, &desc); err != nil {
		return nil
	}
	return map[string]any{
		"id":              pid.String(),
		"identifier":      identifier,
		"name":            name,
		"cover_image":     nil,
		"cover_image_url": nil,
		"logo_props":      map[string]any{},
		"description":     desc,
	}
}

// ---- intake issues ---------------------------------------------------------

func (h *Handler) createIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	actor := actorID(ctx)
	var body struct {
		Issue struct {
			Name     string `json:"name"`
			Priority string `json:"priority"`
		} `json:"issue"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Issue.Name) == "" {
		httpx.Error(w, http.StatusBadRequest, "Name is required")
		return
	}
	priority := body.Issue.Priority
	if priority == "" {
		priority = "none"
	}
	if !validPriority(priority) {
		httpx.Error(w, http.StatusBadRequest, "Invalid priority")
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actor)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var seq int
	_ = h.pool.QueryRow(ctx, `select coalesce(max(sequence_id),0)+1 from issues where project_id=$1`, pid).Scan(&seq)

	triage := triageStateID(pid)
	var issueID uuid.UUID
	err = h.pool.QueryRow(ctx,
		`insert into issues (workspace_id, project_id, name, priority, state_id, sequence_id, created_by)
		 values ($1,$2,$3,$4,$5,$6,$7) returning id`,
		wsID, pid, strings.TrimSpace(body.Issue.Name), priority, triage, seq, actor).Scan(&issueID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if _, err := h.pool.Exec(ctx,
		`insert into intake_issues (workspace_id, project_id, intake_id, issue_id, status, source, created_by)
		 values ($1,$2,$3,$4,-2,'IN_APP',$5)`,
		wsID, pid, intakeID, issueID, actor); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The payload is not valid")
		return
	}
	h.writeDetail(ctx, w, pid, issueID)
}

func (h *Handler) listIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actorID(ctx))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	statuses := parseStatuses(r.URL.Query().Get("status"))

	q := `select ii.id, ii.status, ii.duplicate_to, ii.snoozed_till, ii.source, ii.created_by,
	             i.id, i.name, i.priority, i.sequence_id, i.project_id, i.created_at, i.created_by
	        from intake_issues ii join issues i on i.id = ii.issue_id
	       where ii.intake_id=$1 and ii.deleted_at is null`
	args := []any{intakeID}
	if len(statuses) > 0 {
		q += ` and ii.status = any($2)`
		args = append(args, statuses)
	}
	q += ` order by i.created_at desc`

	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	results := []map[string]any{}
	for rows.Next() {
		var (
			id         uuid.UUID
			status     int
			dupTo      pgtype.UUID
			snoozed    *time.Time
			source     *string
			createdBy  pgtype.UUID
			iID        uuid.UUID
			iName      string
			iPriority  string
			iSeq       int
			iProject   uuid.UUID
			iCreatedAt time.Time
			iCreatedBy pgtype.UUID
		)
		if err := rows.Scan(&id, &status, &dupTo, &snoozed, &source, &createdBy,
			&iID, &iName, &iPriority, &iSeq, &iProject, &iCreatedAt, &iCreatedBy); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id":           id.String(),
			"status":       status,
			"duplicate_to": dbx.StrPtr(dupTo),
			"snoozed_till": snoozed,
			"source":       source,
			"issue": map[string]any{
				"id":          iID.String(),
				"name":        iName,
				"priority":    iPriority,
				"sequence_id": iSeq,
				"project_id":  iProject.String(),
				"created_at":  iCreatedAt,
				"label_ids":   []string{},
				"created_by":  dbx.StrPtr(iCreatedBy),
			},
			"created_by": dbx.StrPtr(createdBy),
		})
	}
	httpx.JSON(w, http.StatusOK, envelope(results))
}

func (h *Handler) retrieveIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actorID(ctx))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	issueID, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if _, ok := h.findIntakeIssue(ctx, intakeID, issueID); !ok {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	h.writeDetail(ctx, w, pid, issueID)
}

func (h *Handler) updateIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actorID(ctx))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	issueID, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	iiID, found := h.findIntakeIssue(ctx, intakeID, issueID)
	if !found {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Status      *int    `json:"status"`
		SnoozedTill *string `json:"snoozed_till"`
		DuplicateTo *string `json:"duplicate_to"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	set := []string{"updated_at = now()"}
	args := []any{iiID}
	n := 2
	if body.Status != nil {
		set = append(set, "status = $"+strconv.Itoa(n))
		args = append(args, *body.Status)
		n++
	}
	if body.SnoozedTill != nil {
		if t, e := time.Parse(time.RFC3339, *body.SnoozedTill); e == nil {
			set = append(set, "snoozed_till = $"+strconv.Itoa(n))
			args = append(args, t)
			n++
		}
	}
	if body.DuplicateTo != nil {
		if d, e := uuid.Parse(*body.DuplicateTo); e == nil {
			set = append(set, "duplicate_to = $"+strconv.Itoa(n))
			args = append(args, d)
			n++
		}
	}
	if _, err := h.pool.Exec(ctx,
		`update intake_issues set `+strings.Join(set, ", ")+` where id=$1`, args...); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	// Accepting moves the issue out of TRIAGE into the project's default state.
	if body.Status != nil && *body.Status == 1 {
		if def, ok := h.defaultStateID(ctx, pid); ok {
			_, _ = h.pool.Exec(ctx,
				`update issues set state_id=$1, updated_by=$2, updated_at=now() where id=$3`,
				def, actorID(ctx), issueID)
		}
	}
	h.writeDetail(ctx, w, pid, issueID)
}

func (h *Handler) destroyIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	intakeID, err := h.ensureIntake(ctx, wsID, pid, actorID(ctx))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	issueID, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	row, found := h.findIntakeIssueStatus(ctx, intakeID, issueID)
	if !found {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	// non-accepted statuses (-2,-1,0,2) also delete the underlying issue.
	if row.status != 1 {
		_, _ = h.pool.Exec(ctx, `update issues set deleted_at=now() where id=$1`, issueID)
	}
	if _, err := h.pool.Exec(ctx, `delete from intake_issues where id=$1`, row.id); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- shared read helpers ---------------------------------------------------

func (h *Handler) findIntakeIssue(ctx context.Context, intakeID, issueID uuid.UUID) (uuid.UUID, bool) {
	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select id from intake_issues where intake_id=$1 and issue_id=$2 and deleted_at is null`,
		intakeID, issueID).Scan(&id)
	return id, err == nil
}

type intakeIssueRow struct {
	id     uuid.UUID
	status int
}

func (h *Handler) findIntakeIssueStatus(ctx context.Context, intakeID, issueID uuid.UUID) (intakeIssueRow, bool) {
	var row intakeIssueRow
	err := h.pool.QueryRow(ctx,
		`select id, status from intake_issues where intake_id=$1 and issue_id=$2 and deleted_at is null`,
		intakeID, issueID).Scan(&row.id, &row.status)
	return row, err == nil
}

// writeDetail emits the IntakeIssueDetailSerializer shape (create/retrieve/patch).
func (h *Handler) writeDetail(ctx context.Context, w http.ResponseWriter, pid, issueID uuid.UUID) {
	var (
		iiID    uuid.UUID
		status  int
		dupTo   pgtype.UUID
		snoozed *time.Time
		source  *string
	)
	err := h.pool.QueryRow(ctx,
		`select id, status, duplicate_to, snoozed_till, source
		   from intake_issues where issue_id=$1 and deleted_at is null limit 1`, issueID).
		Scan(&iiID, &status, &dupTo, &snoozed, &source)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	issue := h.issueDetail(ctx, pid, issueID)
	if issue == nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var dupDetail any
	if dupTo.Valid {
		dupDetail = h.issueCompact(ctx, uuid.UUID(dupTo.Bytes))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":                     iiID.String(),
		"status":                 status,
		"duplicate_to":           dbx.StrPtr(dupTo),
		"snoozed_till":           snoozed,
		"duplicate_issue_detail": dupDetail,
		"source":                 source,
		"issue":                  issue,
	})
}

// issueDetail renders the IssueDetailSerializer subset embedded in intake detail.
func (h *Handler) issueDetail(ctx context.Context, pid, issueID uuid.UUID) map[string]any {
	var (
		id, project                   uuid.UUID
		name, priority, descHTML      string
		stateID, parentID, estimatePt pgtype.UUID
		sortOrder                     float64
		seq                           int
		completedAt, archivedAt       *time.Time
		startDate, targetDate         pgtype.Date
		createdAt, updatedAt          time.Time
		createdBy, updatedBy          pgtype.UUID
		isDraft                       bool
	)
	err := h.pool.QueryRow(ctx,
		`select id, name, state_id, sort_order, completed_at, estimate_point, priority,
		        start_date, target_date, sequence_id, project_id, parent_id, created_at,
		        updated_at, created_by, updated_by, is_draft, archived_at, description_html
		   from issues where id=$1 and project_id=$2`, issueID, pid).
		Scan(&id, &name, &stateID, &sortOrder, &completedAt, &estimatePt, &priority,
			&startDate, &targetDate, &seq, &project, &parentID, &createdAt,
			&updatedAt, &createdBy, &updatedBy, &isDraft, &archivedAt, &descHTML)
	if err != nil {
		return nil
	}
	return map[string]any{
		"id":               id.String(),
		"name":             name,
		"state_id":         dbx.StrPtr(stateID),
		"sort_order":       httpx.Float(sortOrder),
		"completed_at":     completedAt,
		"estimate_point":   dbx.StrPtr(estimatePt),
		"priority":         priority,
		"start_date":       dateOut(startDate),
		"target_date":      dateOut(targetDate),
		"sequence_id":      seq,
		"project_id":       project.String(),
		"parent_id":        dbx.StrPtr(parentID),
		"label_ids":        []string{},
		"assignee_ids":     []string{},
		"created_at":       createdAt,
		"updated_at":       updatedAt,
		"created_by":       dbx.StrPtr(createdBy),
		"updated_by":       dbx.StrPtr(updatedBy),
		"is_draft":         isDraft,
		"archived_at":      archivedAt,
		"description_html": descHTML,
	}
}

// issueCompact renders the IssueIntakeSerializer shape (duplicate_issue_detail).
func (h *Handler) issueCompact(ctx context.Context, issueID uuid.UUID) map[string]any {
	var (
		id, project uuid.UUID
		name        string
		priority    string
		seq         int
		createdAt   time.Time
		createdBy   pgtype.UUID
	)
	if err := h.pool.QueryRow(ctx,
		`select id, name, priority, sequence_id, project_id, created_at, created_by
		   from issues where id=$1`, issueID).
		Scan(&id, &name, &priority, &seq, &project, &createdAt, &createdBy); err != nil {
		return nil
	}
	return map[string]any{
		"id":          id.String(),
		"name":        name,
		"priority":    priority,
		"sequence_id": seq,
		"project_id":  project.String(),
		"created_at":  createdAt,
		"label_ids":   []string{},
		"created_by":  dbx.StrPtr(createdBy),
	}
}

// ---- small helpers ---------------------------------------------------------

func validPriority(p string) bool {
	switch p {
	case "low", "medium", "high", "urgent", "none":
		return true
	}
	return false
}

// parseStatuses mirrors the reference: default "-2", drop "null", parse ints.
func parseStatuses(raw string) []int {
	if raw == "" {
		raw = "-2"
	}
	out := []int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "null" {
			continue
		}
		if v, err := strconv.Atoi(part); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// envelope is the cursor-pagination envelope the intake list shares with issues.
func envelope(results []map[string]any) map[string]any {
	count := len(results)
	pages := 0
	if count > 0 {
		pages = 1
	}
	return map[string]any{
		"grouped_by":        nil,
		"sub_grouped_by":    nil,
		"total_count":       count,
		"next_cursor":       "1000:1:0",
		"prev_cursor":       "1000:-1:1",
		"next_page_results": false,
		"prev_page_results": false,
		"count":             count,
		"total_pages":       pages,
		"total_results":     count,
		"extra_stats":       nil,
		"results":           results,
	}
}

func dateOut(d pgtype.Date) any {
	if !d.Valid {
		return nil
	}
	return d.Time.Format("2006-01-02")
}

func rawOrEmptyObj(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil || v == nil {
		return map[string]any{}
	}
	return v
}

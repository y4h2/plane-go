// Package issueextra serves the "issue extras" endpoints that live outside the
// core issue CRUD surface:
//
//   - Draft issues: workspace-scoped draft-issue CRUD plus a "promote draft to a
//     real issue" action. Drafts mirror many issue columns but project_id and
//     name are nullable. The .values() shape is the DraftIssueSerializer key set
//     (21 keys, NO sequence_id / is_draft / deleted_at). List uses the
//     issue-family cursor-pagination envelope; create returns a bare dict (201);
//     patch/delete return 204.
//   - Bulk-update-dates: POST /issue-dates/ takes {"updates":[{id,start_date,
//     target_date}]} and raw-UPDATEs each issue's dates, returning
//     {"message":"Issues updated successfully"} (200) or, if a resulting
//     start_date would exceed target_date, {"message":"Start date cannot exceed
//     target date"} (400).
//
// Wire-contract quirks mirrored from Plane's Django reference:
//   - Draft endpoints are WORKSPACE scoped and gated on workspace membership;
//     non-members (and unknown slugs) get 403 (permission runs before lookup).
//   - Draft create does NOT validate name (a nameless draft is a 201 with
//     name=null); it DOES 400 on start_date > target_date with
//     {"non_field_errors":["Start date cannot exceed target date"]}.
//   - retrieve/patch/delete key on the draft pk AND created_by=request.user
//     (a draft the caller didn't author reads as 404).
//   - draft-to-issue on a draft with no project_id is a 400.
package issueextra

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
	base := "/workspaces/{slug}/draft-issues/"
	r.Get(base, h.listDrafts)
	r.Post(base, h.createDraft)
	r.Get(base+"{pk}/", h.retrieveDraft)
	r.Patch(base+"{pk}/", h.updateDraft)
	r.Delete(base+"{pk}/", h.destroyDraft)
	r.Post("/workspaces/{slug}/draft-to-issue/{draft_id}/", h.draftToIssue)
	// bulk-update-dates operates on the existing issues table.
	r.Post("/workspaces/{slug}/projects/{project_id}/issue-dates/", h.bulkUpdateDates)
}

// ---- scope helpers ---------------------------------------------------------

// wsScope resolves the workspace by slug and enforces membership. A missing
// workspace OR a non-member caller both yield 403 (the reference's permission
// decorator runs before the object lookup). Returns ok=false after responding.
func (h *Handler) wsScope(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	u, _ := auth.UserFrom(ctx)
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select w.id from workspaces w
		   join workspace_members wm on wm.workspace_id = w.id
		  where w.slug = $1 and w.deleted_at is null and wm.member_id = $2`,
		chi.URLParam(r, "slug"), u.ID).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return uuid.Nil, false
	}
	return wsID, true
}

func actorID(ctx context.Context) pgtype.UUID {
	if u, ok := auth.UserFrom(ctx); ok {
		return dbx.PgUUID(u.ID)
	}
	return dbx.NullUUID()
}

// ---- draft row model + rendering ------------------------------------------

type draftRow struct {
	id                    uuid.UUID
	name                  *string
	stateID               pgtype.UUID
	sortOrder             float64
	completedAt           *time.Time
	estimatePoint         pgtype.UUID
	priority              string
	startDate, targetDate pgtype.Date
	projectID             pgtype.UUID
	parentID              pgtype.UUID
	createdAt, updatedAt  time.Time
	createdBy, updatedBy  pgtype.UUID
	typeID                pgtype.UUID
	descriptionHTML       string
}

const draftCols = `id, name, state_id, sort_order, completed_at, estimate_point, priority,
	start_date, target_date, project_id, parent_id, created_at, updated_at,
	created_by, updated_by, type_id, description_html`

func scanDraft(row pgx.Row) (draftRow, error) {
	var d draftRow
	err := row.Scan(&d.id, &d.name, &d.stateID, &d.sortOrder, &d.completedAt, &d.estimatePoint, &d.priority,
		&d.startDate, &d.targetDate, &d.projectID, &d.parentID, &d.createdAt, &d.updatedAt,
		&d.createdBy, &d.updatedBy, &d.typeID, &d.descriptionHTML)
	return d, err
}

// draftValues renders the DraftIssueSerializer / create .values() shape (21 keys).
// cycle_id/module_ids/label_ids/assignee_ids are annotations we always emit empty
// (no draft M2M tables in the port); NOTE the absence of sequence_id/is_draft.
func draftValues(d draftRow) map[string]any {
	return map[string]any{
		"id":               d.id.String(),
		"name":             d.name,
		"state_id":         dbx.StrPtr(d.stateID),
		"sort_order":       httpx.Float(d.sortOrder),
		"completed_at":     d.completedAt,
		"estimate_point":   dbx.StrPtr(d.estimatePoint),
		"priority":         d.priority,
		"start_date":       dateOut(d.startDate),
		"target_date":      dateOut(d.targetDate),
		"project_id":       dbx.StrPtr(d.projectID),
		"parent_id":        dbx.StrPtr(d.parentID),
		"cycle_id":         nil,
		"module_ids":       []string{},
		"label_ids":        []string{},
		"assignee_ids":     []string{},
		"created_at":       d.createdAt,
		"updated_at":       d.updatedAt,
		"created_by":       dbx.StrPtr(d.createdBy),
		"updated_by":       dbx.StrPtr(d.updatedBy),
		"type_id":          dbx.StrPtr(d.typeID),
		"description_html": d.descriptionHTML,
	}
}

// ---- draft handlers --------------------------------------------------------

func (h *Handler) listDrafts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsScope(ctx, w, r); !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	rows, err := h.pool.Query(ctx,
		`select `+draftCols+` from draft_issues
		  where workspace_id = (select id from workspaces where slug=$1 and deleted_at is null)
		    and created_by = $2 and deleted_at is null
		  order by created_at desc`,
		chi.URLParam(r, "slug"), u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	results := []map[string]any{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			continue
		}
		results = append(results, draftValues(d))
	}
	httpx.JSON(w, http.StatusOK, envelope(results))
}

func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.wsScope(ctx, w, r)
	if !ok {
		return
	}
	var body struct {
		Name       *string `json:"name"`
		Priority   string  `json:"priority"`
		ProjectID  *string `json:"project_id"`
		State      *string `json:"state_id"`
		StartDate  *string `json:"start_date"`
		TargetDate *string `json:"target_date"`
		DescHTML   *string `json:"description_html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	// start_date > target_date is the only field validation on create.
	if !datesOK(body.StartDate, body.TargetDate) {
		httpx.JSON(w, http.StatusBadRequest,
			map[string][]string{"non_field_errors": {"Start date cannot exceed target date"}})
		return
	}
	priority := body.Priority
	if priority == "" {
		priority = "none"
	}
	projectID := dbx.NullUUID()
	if body.ProjectID != nil {
		if pid, err := uuid.Parse(*body.ProjectID); err == nil {
			projectID = dbx.PgUUID(pid)
		}
	}
	stateID := dbx.NullUUID()
	if body.State != nil {
		if sid, err := uuid.Parse(*body.State); err == nil {
			stateID = dbx.PgUUID(sid)
		}
	} else if projectID.Valid {
		// mirror the model save(): a project-scoped draft with no state gets the
		// project's default state.
		if def, ok := h.defaultStateID(ctx, uuid.UUID(projectID.Bytes)); ok {
			stateID = dbx.PgUUID(def)
		}
	}
	descHTML := "<p></p>"
	if body.DescHTML != nil && *body.DescHTML != "" {
		descHTML = *body.DescHTML
	}
	sortOrder := h.nextSortOrder(ctx, projectID, stateID)

	var id uuid.UUID
	err := h.pool.QueryRow(ctx,
		`insert into draft_issues
		   (workspace_id, project_id, name, priority, state_id, sort_order,
		    start_date, target_date, description_html, created_by)
		 values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 returning id`,
		wsID, projectID, body.Name, priority, stateID, sortOrder,
		dateIn(body.StartDate), dateIn(body.TargetDate), descHTML, actorID(ctx)).Scan(&id)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	d, err := scanDraft(h.pool.QueryRow(ctx, `select `+draftCols+` from draft_issues where id=$1`, id))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusCreated, draftValues(d))
}

func (h *Handler) retrieveDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsScope(ctx, w, r); !ok {
		return
	}
	d, ok := h.findOwnDraft(ctx, w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, draftValues(d))
}

func (h *Handler) updateDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsScope(ctx, w, r); !ok {
		return
	}
	d, ok := h.findOwnDraft(ctx, w, r)
	if !ok {
		return
	}
	var body struct {
		Name       *string `json:"name"`
		Priority   *string `json:"priority"`
		State      *string `json:"state_id"`
		StartDate  *string `json:"start_date"`
		TargetDate *string `json:"target_date"`
		DescHTML   *string `json:"description_html"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// resulting start/target (new value falling back to the stored one) must not invert.
	newStart := dateStrPtr(d.startDate)
	if body.StartDate != nil {
		newStart = body.StartDate
	}
	newTarget := dateStrPtr(d.targetDate)
	if body.TargetDate != nil {
		newTarget = body.TargetDate
	}
	if !datesOK(newStart, newTarget) {
		httpx.JSON(w, http.StatusBadRequest,
			map[string][]string{"non_field_errors": {"Start date cannot exceed target date"}})
		return
	}

	set := []string{"updated_at = now()", "updated_by = $2"}
	args := []any{d.id, actorID(ctx)}
	add := func(col string, val any) {
		set = append(set, col+" = $"+strconv.Itoa(len(args)+1))
		args = append(args, val)
	}
	if body.Name != nil {
		add("name", body.Name)
	}
	if body.Priority != nil {
		add("priority", *body.Priority)
	}
	if body.State != nil {
		if sid, err := uuid.Parse(*body.State); err == nil {
			add("state_id", dbx.PgUUID(sid))
		} else {
			add("state_id", dbx.NullUUID())
		}
	}
	if body.StartDate != nil {
		add("start_date", dateIn(body.StartDate))
	}
	if body.TargetDate != nil {
		add("target_date", dateIn(body.TargetDate))
	}
	if body.DescHTML != nil {
		add("description_html", *body.DescHTML)
	}
	if _, err := h.pool.Exec(ctx,
		`update draft_issues set `+strings.Join(set, ", ")+` where id = $1`, args...); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent) // deliberate: 204 with empty body
}

func (h *Handler) destroyDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsScope(ctx, w, r); !ok {
		return
	}
	d, ok := h.findOwnDraft(ctx, w, r)
	if !ok {
		return
	}
	if _, err := h.pool.Exec(ctx, `delete from draft_issues where id=$1`, d.id); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findOwnDraft loads a draft by pk scoped to the caller (created_by). A draft the
// caller didn't author, or a bad pk, reads as 404 (matching the reference).
func (h *Handler) findOwnDraft(ctx context.Context, w http.ResponseWriter, r *http.Request) (draftRow, bool) {
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return draftRow{}, false
	}
	u, _ := auth.UserFrom(ctx)
	d, err := scanDraft(h.pool.QueryRow(ctx,
		`select `+draftCols+` from draft_issues
		  where id=$1 and created_by=$2 and deleted_at is null`, pk, u.ID))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return draftRow{}, false
	}
	return d, true
}

// ---- draft -> issue --------------------------------------------------------

func (h *Handler) draftToIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, ok := h.wsScope(ctx, w, r)
	if !ok {
		return
	}
	did, err := uuid.Parse(chi.URLParam(r, "draft_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	d, err := scanDraft(h.pool.QueryRow(ctx, `select `+draftCols+` from draft_issues where id=$1 and deleted_at is null`, did))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if !d.projectID.Valid {
		httpx.Error(w, http.StatusBadRequest, "Project is required to create an issue.")
		return
	}
	pid := uuid.UUID(d.projectID.Bytes)

	var body struct {
		Name     *string `json:"name"`
		Priority *string `json:"priority"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := ""
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	if name == "" && d.name != nil {
		name = *d.name
	}
	if name == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"name": {"This field is required."}})
		return
	}
	priority := d.priority
	if body.Priority != nil {
		priority = *body.Priority
	}

	var seq int
	_ = h.pool.QueryRow(ctx, `select coalesce(max(sequence_id),0)+1 from issues where project_id=$1`, pid).Scan(&seq)

	actor := actorID(ctx)
	var iss issueRow
	err = h.pool.QueryRow(ctx,
		`insert into issues
		   (workspace_id, project_id, name, priority, state_id, parent_id, estimate_point,
		    sequence_id, start_date, target_date, description_html, created_by)
		 values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 returning `+issueCols,
		wsID, pid, name, priority, d.stateID, d.parentID, d.estimatePoint,
		seq, d.startDate, d.targetDate, d.descriptionHTML, actor).Scan(iss.dest()...)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	// the draft is consumed once promoted.
	_, _ = h.pool.Exec(ctx, `delete from draft_issues where id=$1`, did)

	httpx.JSON(w, http.StatusCreated, iss.values(wsID))
}

// issueRow is the subset of issues columns echoed back by draft-to-issue.
type issueRow struct {
	id                    uuid.UUID
	name                  string
	stateID               pgtype.UUID
	parentID              pgtype.UUID
	estimatePoint         pgtype.UUID
	priority              string
	sortOrder             float64
	seq                   int
	projectID             uuid.UUID
	startDate, targetDate pgtype.Date
	completedAt           *time.Time
	archivedAt            *time.Time
	isDraft               bool
	descriptionHTML       string
	createdAt, updatedAt  time.Time
	createdBy, updatedBy  pgtype.UUID
	deletedAt             *time.Time
}

const issueCols = `id, name, state_id, parent_id, estimate_point, priority, sort_order,
	sequence_id, project_id, start_date, target_date, completed_at, archived_at,
	is_draft, description_html, created_at, updated_at, created_by, updated_by, deleted_at`

func (i *issueRow) dest() []any {
	return []any{&i.id, &i.name, &i.stateID, &i.parentID, &i.estimatePoint, &i.priority, &i.sortOrder,
		&i.seq, &i.projectID, &i.startDate, &i.targetDate, &i.completedAt, &i.archivedAt,
		&i.isDraft, &i.descriptionHTML, &i.createdAt, &i.updatedAt, &i.createdBy, &i.updatedBy, &i.deletedAt}
}

// values renders the IssueCreateSerializer shape (draft-to-issue response).
func (i *issueRow) values(wsID uuid.UUID) map[string]any {
	return map[string]any{
		"id":                   i.id.String(),
		"name":                 i.name,
		"state_id":             dbx.StrPtr(i.stateID),
		"parent_id":            dbx.StrPtr(i.parentID),
		"estimate_point":       dbx.StrPtr(i.estimatePoint),
		"priority":             i.priority,
		"sort_order":           httpx.Float(i.sortOrder),
		"sequence_id":          i.seq,
		"project_id":           i.projectID.String(),
		"start_date":           dateOut(i.startDate),
		"target_date":          dateOut(i.targetDate),
		"completed_at":         i.completedAt,
		"archived_at":          i.archivedAt,
		"is_draft":             i.isDraft,
		"description_html":     i.descriptionHTML,
		"description_json":     map[string]any{},
		"description_stripped": "",
		"point":                nil,
		"external_source":      nil,
		"external_id":          nil,
		"created_at":           i.createdAt,
		"updated_at":           i.updatedAt,
		"created_by":           dbx.StrPtr(i.createdBy),
		"updated_by":           dbx.StrPtr(i.updatedBy),
		"deleted_at":           i.deletedAt,
		"project":              i.projectID.String(),
		"workspace":            wsID.String(),
		"parent":               dbx.StrPtr(i.parentID),
		"state":                dbx.StrPtr(i.stateID),
		"type":                 nil,
		"assignees":            []string{},
		"labels":               []string{},
		"assignee_ids":         []string{},
		"label_ids":            []string{},
	}
}

// ---- bulk update dates -----------------------------------------------------

func (h *Handler) bulkUpdateDates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.wsScope(ctx, w, r); !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Updates []struct {
			ID         string  `json:"id"`
			StartDate  *string `json:"start_date"`
			TargetDate *string `json:"target_date"`
		} `json:"updates"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	for _, up := range body.Updates {
		iid, err := uuid.Parse(up.ID)
		if err != nil {
			continue
		}
		// fetch the issue's current dates (scoped to workspace slug + project).
		var curStart, curTarget pgtype.Date
		err = h.pool.QueryRow(ctx,
			`select i.start_date, i.target_date from issues i
			   join workspaces w on w.id = i.workspace_id
			  where i.id=$1 and i.project_id=$2 and w.slug=$3 and i.deleted_at is null`,
			iid, pid, chi.URLParam(r, "slug")).Scan(&curStart, &curTarget)
		if err != nil {
			continue // unknown id is silently skipped (matches the reference)
		}
		// resulting dates: new value if provided, else the stored one.
		start := dateStrPtr(curStart)
		if up.StartDate != nil {
			start = up.StartDate
		}
		target := dateStrPtr(curTarget)
		if up.TargetDate != nil {
			target = up.TargetDate
		}
		if !datesOK(start, target) {
			httpx.JSON(w, http.StatusBadRequest,
				map[string]string{"message": "Start date cannot exceed target date"})
			return
		}
		if up.StartDate != nil {
			_, _ = h.pool.Exec(ctx, `update issues set start_date=$1, updated_at=now() where id=$2`,
				dateIn(up.StartDate), iid)
		}
		if up.TargetDate != nil {
			_, _ = h.pool.Exec(ctx, `update issues set target_date=$1, updated_at=now() where id=$2`,
				dateIn(up.TargetDate), iid)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Issues updated successfully"})
}

// ---- small helpers ---------------------------------------------------------

func (h *Handler) defaultStateID(ctx context.Context, pid uuid.UUID) (uuid.UUID, bool) {
	var id uuid.UUID
	if err := h.pool.QueryRow(ctx,
		`select id from states where project_id=$1 and is_default=true limit 1`, pid).Scan(&id); err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// nextSortOrder mirrors the model save(): max(sort_order)+10000 among drafts with
// the same (nullable) project_id and state_id, defaulting to 65535.
func (h *Handler) nextSortOrder(ctx context.Context, projectID, stateID pgtype.UUID) float64 {
	var maxSort *float64
	_ = h.pool.QueryRow(ctx,
		`select max(sort_order) from draft_issues
		  where project_id is not distinct from $1 and state_id is not distinct from $2
		    and deleted_at is null`,
		projectID, stateID).Scan(&maxSort)
	if maxSort != nil {
		return *maxSort + 10000
	}
	return 65535
}

// envelope is the cursor-pagination envelope the draft list shares with issues.
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

func dateIn(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	if t, err := time.Parse("2006-01-02", *s); err == nil {
		return t
	}
	return nil
}

func dateStrPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// datesOK returns false only when both dates are present and start > target.
func datesOK(start, target *string) bool {
	if start == nil || target == nil || *start == "" || *target == "" {
		return true
	}
	s, err1 := time.Parse("2006-01-02", *start)
	t, err2 := time.Parse("2006-01-02", *target)
	if err1 != nil || err2 != nil {
		return true
	}
	return !s.After(t)
}

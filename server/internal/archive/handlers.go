// Package archive serves the cycle and module archive/unarchive endpoints
// (Django: plane/app/views/cycle/archive.py -> CycleArchiveUnarchiveEndpoint,
// plane/app/views/module/archive.py -> ModuleArchiveUnarchiveEndpoint).
//
// Wire-contract quirks pinned by probing the Python reference live:
//   - POST .../cycles/<cycle_id>/archive/ archives a cycle ONLY if its end_date
//     is in the past (`end_date >= now()` -> 400 "Only completed cycles can be
//     archived"). A cycle with no end_date crashes the reference (unhandled
//     TypeError comparing None >= datetime) -> 500
//     {"error":"Something went wrong please try again later"}; we mirror that.
//   - DELETE .../cycles/<cycle_id>/archive/ unarchives (204, idempotent even if
//     not currently archived). This is the ONLY working unarchive route: the
//     reference's `delete()` method requires a `cycle_id` kwarg, which the
//     archived-cycles/<pk>/ URL does NOT supply (only `pk`) -> that combination
//     500s in the reference. We mirror the 500 for parity rather than "fixing"
//     the route.
//   - GET .../archived-cycles/ lists archived cycles (bare list, excludes
//     non-archived); GET .../archived-cycles/<pk>/ is the rich detail shape.
//     A <pk> that exists but isn't archived (or doesn't exist at all) crashes
//     the reference (NoneType not subscriptable) -> 500; mirrored.
//   - POST .../modules/<module_id>/archive/ archives a module only if its
//     status is "completed" or "cancelled" (else 400). DELETE unarchives (204,
//     idempotent), same cycle_id/pk routing quirk as cycles -> unarchive via
//     archived-modules/<pk>/ 500s in the reference; mirrored.
//   - GET .../archived-modules/<pk>/ for a module that isn't archived (or
//     doesn't exist) does NOT 500 like cycles: ModuleDetailSerializer(None)
//     degrades to a fixed 200 body {"member_ids":[],"estimate_distribution":{},
//     "distribution":{"assignees":[],"labels":[],"completion_chart":{}}}.
//     Mirrored verbatim.
//   - Archived cycles/modules are excluded from the normal (non-archived)
//     cycles/modules list and 404 from the normal retrieve endpoint in the
//     reference. NOTE: internal/cycle and internal/module's list/retrieve
//     handlers in THIS Go port do not yet exclude archived rows (the
//     archived_at column is new, added by this package's migration) — see the
//     package doc / task follow-up note; not fixed here since this package may
//     only touch its own files.
package archive

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/archive/", h.archiveCycle)
	r.Delete("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/archive/", h.unarchiveCycle)
	r.Get("/workspaces/{slug}/projects/{project_id}/archived-cycles/", h.listArchivedCycles)
	r.Get("/workspaces/{slug}/projects/{project_id}/archived-cycles/{pk}/", h.retrieveArchivedCycle)
	r.Delete("/workspaces/{slug}/projects/{project_id}/archived-cycles/{pk}/", h.badUnarchiveCycle)

	r.Post("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/archive/", h.archiveModule)
	r.Delete("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/archive/", h.unarchiveModule)
	r.Get("/workspaces/{slug}/projects/{project_id}/archived-modules/", h.listArchivedModules)
	r.Get("/workspaces/{slug}/projects/{project_id}/archived-modules/{pk}/", h.retrieveArchivedModule)
	r.Delete("/workspaces/{slug}/projects/{project_id}/archived-modules/{pk}/", h.badUnarchiveModule)
}

const serverErrMsg = "Something went wrong please try again later"

// ---- scope ------------------------------------------------------------

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

// badUnarchiveCycle / badUnarchiveModule mirror the reference's routing quirk:
// DELETE on archived-cycles/<pk>/ (resp. archived-modules/<pk>/) hits a view
// method that requires a differently-named kwarg than the URL supplies, so the
// reference always 500s there regardless of the pk's validity.
func (h *Handler) badUnarchiveCycle(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
}

func (h *Handler) badUnarchiveModule(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
}

// ---- cycles -------------------------------------------------------------

type cycleRow struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	Description string
	StartDate   *time.Time
	EndDate     *time.Time
	OwnedByID   uuid.UUID
	SortOrder   float64
	ExternalSrc *string
	ExternalID  *string
	CreatedBy   *uuid.UUID
	ArchivedAt  *time.Time
}

// Go's cycles table has no external_source/external_id columns (leaner than
// Django's); those response fields stay nil and render as null.
const cycleCols = `id, workspace_id, project_id, name, description, start_date, end_date,
	owned_by_id, sort_order, created_by, archived_at`

func scanCycle(row pgx.Row) (cycleRow, error) {
	var c cycleRow
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.ProjectID, &c.Name, &c.Description, &c.StartDate, &c.EndDate,
		&c.OwnedByID, &c.SortOrder, &c.CreatedBy, &c.ArchivedAt)
	return c, err
}

func cycleListItem(c cycleRow) map[string]any {
	return map[string]any{
		"id":                c.ID.String(),
		"workspace_id":      c.WorkspaceID.String(),
		"project_id":        c.ProjectID.String(),
		"name":              c.Name,
		"description":       c.Description,
		"start_date":        c.StartDate,
		"end_date":          c.EndDate,
		"owned_by_id":       c.OwnedByID.String(),
		"view_props":        map[string]any{},
		"sort_order":        httpx.Float(c.SortOrder),
		"external_source":   c.ExternalSrc,
		"external_id":       c.ExternalID,
		"progress_snapshot": map[string]any{},
		"archived_at":       c.ArchivedAt,
		"is_favorite":       false,
		"total_issues":      0,
		"completed_issues":  0,
		"cancelled_issues":  0,
		"started_issues":    0,
		"unstarted_issues":  0,
		"backlog_issues":    0,
		// Only cycles whose end_date has already passed can ever reach the
		// archived table (enforced by archiveCycle), so COMPLETED is accurate
		// here, not just a placeholder.
		"status":       "COMPLETED",
		"assignee_ids": []string{},
	}
}

func cycleDetailItem(c cycleRow) map[string]any {
	m := cycleListItem(c)
	m["logo_props"] = map[string]any{}
	m["created_by"] = strPtr(c.CreatedBy)
	m["completed_estimate_points"] = httpx.Float(0)
	m["total_estimate_points"] = httpx.Float(0)
	m["sub_issues"] = 0
	m["estimate_distribution"] = map[string]any{}
	m["distribution"] = map[string]any{
		"assignees":        []any{},
		"labels":           []any{},
		"completion_chart": map[string]any{},
	}
	return m
}

func strPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func (h *Handler) archiveCycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(chi.URLParam(r, "cycle_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var endDate *time.Time
	err = h.pool.QueryRow(ctx,
		`select end_date from cycles where id=$1 and project_id=$2 and workspace_id=$3 and deleted_at is null`,
		cid, pid, wsID).Scan(&endDate)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if endDate == nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	if !endDate.Before(time.Now().UTC()) {
		httpx.Error(w, http.StatusBadRequest, "Only completed cycles can be archived")
		return
	}
	var archivedAt time.Time
	if err := h.pool.QueryRow(ctx,
		`update cycles set archived_at=now() where id=$1 returning archived_at`, cid,
	).Scan(&archivedAt); err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	_, _ = h.pool.Exec(ctx,
		`delete from user_favorites where entity_type='cycle' and entity_identifier=$1 and project_id=$2 and workspace_id=$3`,
		cid, pid, wsID)
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": archivedAt})
}

func (h *Handler) unarchiveCycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(chi.URLParam(r, "cycle_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	tag, err := h.pool.Exec(ctx,
		`update cycles set archived_at=null where id=$1 and project_id=$2 and workspace_id=$3 and deleted_at is null`,
		cid, pid, wsID)
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listArchivedCycles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(ctx,
		`select `+cycleCols+` from cycles where project_id=$1 and archived_at is not null and deleted_at is null order by name`,
		pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		c, err := scanCycle(rows)
		if err != nil {
			continue
		}
		out = append(out, cycleListItem(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieveArchivedCycle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	row := h.pool.QueryRow(ctx,
		`select `+cycleCols+` from cycles where id=$1 and project_id=$2 and archived_at is not null and deleted_at is null`,
		pk, pid)
	c, err := scanCycle(row)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	httpx.JSON(w, http.StatusOK, cycleDetailItem(c))
}

// ---- modules --------------------------------------------------------------

type moduleRow struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	Description string
	Status      string
	LeadID      *uuid.UUID
	SortOrder   float64
	ExternalSrc *string
	ExternalID  *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

// Go's modules table has no external_source/external_id columns; those
// response fields stay nil and render as null.
const moduleCols = `id, workspace_id, project_id, name, description, status, lead_id, sort_order,
	created_at, updated_at, archived_at`

func scanModule(row pgx.Row) (moduleRow, error) {
	var m moduleRow
	err := row.Scan(&m.ID, &m.WorkspaceID, &m.ProjectID, &m.Name, &m.Description, &m.Status, &m.LeadID,
		&m.SortOrder, &m.CreatedAt, &m.UpdatedAt, &m.ArchivedAt)
	return m, err
}

func moduleListItem(m moduleRow) map[string]any {
	return map[string]any{
		"id":               m.ID.String(),
		"workspace_id":     m.WorkspaceID.String(),
		"project_id":       m.ProjectID.String(),
		"name":             m.Name,
		"description":      m.Description,
		"description_text": nil,
		"description_html": nil,
		"start_date":       nil,
		"target_date":      nil,
		"status":           m.Status,
		"lead_id":          strPtr(m.LeadID),
		"view_props":       map[string]any{},
		"sort_order":       httpx.Float(m.SortOrder),
		"external_source":  m.ExternalSrc,
		"external_id":      m.ExternalID,
		"created_at":       m.CreatedAt,
		"updated_at":       m.UpdatedAt,
		"archived_at":      m.ArchivedAt,
		"is_favorite":      false,
		"completed_issues": 0,
		"cancelled_issues": 0,
		"started_issues":   0,
		"unstarted_issues": 0,
		"backlog_issues":   0,
		"total_issues":     0,
		"member_ids":       []string{},
	}
}

func moduleDetailItem(m moduleRow) map[string]any {
	item := moduleListItem(m)
	item["logo_props"] = map[string]any{}
	item["total_estimate_points"] = httpx.Float(0)
	item["completed_estimate_points"] = httpx.Float(0)
	item["backlog_estimate_points"] = httpx.Float(0)
	item["unstarted_estimate_points"] = httpx.Float(0)
	item["started_estimate_points"] = httpx.Float(0)
	item["cancelled_estimate_points"] = httpx.Float(0)
	item["link_module"] = []any{}
	item["sub_issues"] = 0
	item["estimate_distribution"] = map[string]any{}
	item["distribution"] = map[string]any{
		"assignees":        []any{},
		"labels":           []any{},
		"completion_chart": map[string]any{},
	}
	return item
}

// moduleDegenerateDetail mirrors ModuleDetailSerializer(None).data as observed
// live against the reference for a pk that isn't archived (or doesn't exist):
// the view still layers estimate_distribution/distribution onto whatever the
// serializer produced for a None instance, which resolves to just these keys.
var moduleDegenerateDetail = map[string]any{
	"member_ids":            []string{},
	"estimate_distribution": map[string]any{},
	"distribution": map[string]any{
		"assignees":        []any{},
		"labels":           []any{},
		"completion_chart": map[string]any{},
	},
}

func (h *Handler) archiveModule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	mid, err := uuid.Parse(chi.URLParam(r, "module_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var status string
	err = h.pool.QueryRow(ctx,
		`select status from modules where id=$1 and project_id=$2 and workspace_id=$3 and deleted_at is null`,
		mid, pid, wsID).Scan(&status)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if status != "completed" && status != "cancelled" {
		httpx.Error(w, http.StatusBadRequest, "Only completed or cancelled modules can be archived")
		return
	}
	var archivedAt time.Time
	if err := h.pool.QueryRow(ctx,
		`update modules set archived_at=now() where id=$1 returning archived_at`, mid,
	).Scan(&archivedAt); err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	_, _ = h.pool.Exec(ctx,
		`delete from user_favorites where entity_type='module' and entity_identifier=$1 and project_id=$2 and workspace_id=$3`,
		mid, pid, wsID)
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": archivedAt})
}

func (h *Handler) unarchiveModule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	mid, err := uuid.Parse(chi.URLParam(r, "module_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	tag, err := h.pool.Exec(ctx,
		`update modules set archived_at=null where id=$1 and project_id=$2 and workspace_id=$3 and deleted_at is null`,
		mid, pid, wsID)
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listArchivedModules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(ctx,
		`select `+moduleCols+` from modules where project_id=$1 and archived_at is not null and deleted_at is null order by name`,
		pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, serverErrMsg)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		m, err := scanModule(rows)
		if err != nil {
			continue
		}
		out = append(out, moduleListItem(m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieveArchivedModule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	pk, err := uuid.Parse(chi.URLParam(r, "pk"))
	if err != nil {
		httpx.JSON(w, http.StatusOK, moduleDegenerateDetail)
		return
	}
	row := h.pool.QueryRow(ctx,
		`select `+moduleCols+` from modules where id=$1 and project_id=$2 and archived_at is not null and deleted_at is null`,
		pk, pid)
	m, err := scanModule(row)
	if err != nil {
		httpx.JSON(w, http.StatusOK, moduleDegenerateDetail)
		return
	}
	httpx.JSON(w, http.StatusOK, moduleDetailItem(m))
}

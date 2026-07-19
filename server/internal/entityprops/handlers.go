// Package entityprops serves the per-user per-module and per-cycle
// user-properties endpoints (the view settings — layout/filters/display props —
// the frontend fetches before rendering a module's or cycle's issue board). GET
// is get-or-create; PATCH merges. Mirrors the project-level userprops package
// but scoped to a module/cycle. Raw pgx (no sqlc).
package entityprops

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// entity describes one scoped variant (module or cycle).
type entity struct {
	table    string // e.g. "module_user_properties"
	idCol    string // e.g. "module_id"
	urlParam string // e.g. "module_id"
	respKey  string // e.g. "module"
}

var moduleEntity = entity{"module_user_properties", "module_id", "module_id", "module"}
var cycleEntity = entity{"cycle_user_properties", "cycle_id", "cycle_id", "cycle"}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/user-properties/", h.handle(moduleEntity, false))
	r.Patch("/workspaces/{slug}/projects/{project_id}/modules/{module_id}/user-properties/", h.handle(moduleEntity, true))
	r.Get("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/user-properties/", h.handle(cycleEntity, false))
	r.Patch("/workspaces/{slug}/projects/{project_id}/cycles/{cycle_id}/user-properties/", h.handle(cycleEntity, true))
}

// Django-parity default view settings, seeded on first access.
const (
	defFilters  = `{"priority":null,"state":null,"state_group":null,"assignees":null,"created_by":null,"labels":null,"start_date":null,"target_date":null,"subscriber":null}`
	defDisplayF = `{"group_by":null,"order_by":"-created_at","type":null,"sub_issue":true,"show_empty_groups":true,"layout":"list","calendar_date_range":""}`
	defDisplayP = `{"assignee":true,"attachment_count":true,"created_on":true,"due_date":true,"estimate":true,"key":true,"labels":true,"link":true,"priority":true,"start_date":true,"state":true,"sub_issue_count":true,"updated_on":true}`
)

type row struct {
	id                                     uuid.UUID
	entityID, projectID, workspaceID, user uuid.UUID
	filters, displayFilters, displayProps  []byte
	createdAt, updatedAt                    any
}

func (h *Handler) handle(e entity, isPatch bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		u, _ := auth.UserFrom(ctx)
		var wsID, pid, eid uuid.UUID
		if err := h.pool.QueryRow(ctx,
			`select id from workspaces where slug=$1 and deleted_at is null`,
			chi.URLParam(r, "slug")).Scan(&wsID); err != nil {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
			return
		}
		var perr error
		if pid, perr = uuid.Parse(chi.URLParam(r, "project_id")); perr != nil {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
			return
		}
		if eid, perr = uuid.Parse(chi.URLParam(r, e.urlParam)); perr != nil {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
			return
		}
		cur, err := h.getOrCreate(ctx, e, wsID, pid, eid, u.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		if isPatch {
			var body struct {
				Filters           json.RawMessage `json:"filters"`
				DisplayFilters    json.RawMessage `json:"display_filters"`
				DisplayProperties json.RawMessage `json:"display_properties"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// jsonb-merge each provided block into the stored value.
			if err := h.pool.QueryRow(ctx,
				`update `+e.table+` set
				    filters            = filters            || coalesce($2::jsonb, '{}'::jsonb),
				    display_filters    = display_filters    || coalesce($3::jsonb, '{}'::jsonb),
				    display_properties = display_properties || coalesce($4::jsonb, '{}'::jsonb),
				    updated_at = now()
				 where id = $1
				 returning id, `+e.idCol+`, project_id, workspace_id, user_id, filters, display_filters, display_properties, created_at, updated_at`,
				cur.id, rawOrNil(body.Filters), rawOrNil(body.DisplayFilters), rawOrNil(body.DisplayProperties)).
				Scan(&cur.id, &cur.entityID, &cur.projectID, &cur.workspaceID, &cur.user,
					&cur.filters, &cur.displayFilters, &cur.displayProps, &cur.createdAt, &cur.updatedAt); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
				return
			}
		}
		httpx.JSON(w, http.StatusOK, resp(e, cur))
	}
}

func (h *Handler) getOrCreate(ctx context.Context, e entity, wsID, pid, eid, uid uuid.UUID) (row, error) {
	var rw row
	sel := `select id, ` + e.idCol + `, project_id, workspace_id, user_id, filters, display_filters, display_properties, created_at, updated_at
	         from ` + e.table + ` where ` + e.idCol + `=$1 and user_id=$2`
	err := h.pool.QueryRow(ctx, sel, eid, uid).Scan(&rw.id, &rw.entityID, &rw.projectID, &rw.workspaceID, &rw.user,
		&rw.filters, &rw.displayFilters, &rw.displayProps, &rw.createdAt, &rw.updatedAt)
	if err == nil {
		return rw, nil
	}
	if err != pgx.ErrNoRows {
		return rw, err
	}
	ins := `insert into ` + e.table + ` (workspace_id, project_id, ` + e.idCol + `, user_id, filters, display_filters, display_properties)
	         values ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb)
	         returning id, ` + e.idCol + `, project_id, workspace_id, user_id, filters, display_filters, display_properties, created_at, updated_at`
	err = h.pool.QueryRow(ctx, ins, wsID, pid, eid, uid, defFilters, defDisplayF, defDisplayP).
		Scan(&rw.id, &rw.entityID, &rw.projectID, &rw.workspaceID, &rw.user,
			&rw.filters, &rw.displayFilters, &rw.displayProps, &rw.createdAt, &rw.updatedAt)
	return rw, err
}

func rawOrNil(m json.RawMessage) any {
	if len(m) == 0 {
		return nil
	}
	return string(m)
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func resp(e entity, rw row) map[string]any {
	return map[string]any{
		"id":                 rw.id.String(),
		"created_at":         rw.createdAt,
		"updated_at":         rw.updatedAt,
		"deleted_at":         nil,
		"filters":            raw(rw.filters),
		"display_filters":    raw(rw.displayFilters),
		"display_properties": raw(rw.displayProps),
		"rich_filters":       json.RawMessage("{}"),
		"created_by":         nil,
		"updated_by":         nil,
		"project":            rw.projectID.String(),
		"workspace":          rw.workspaceID.String(),
		e.respKey:            rw.entityID.String(),
		"user":               rw.user.String(),
	}
}

// Package page serves the wiki/docs metadata CRUD surface (create, list,
// retrieve, update, archive/unarchive, lock/unlock, access, favorite,
// duplicate, summary). The live collaborative binary description sync (the
// `live` server, /description/ and /versions/ endpoints) is out of scope.
//
// Raw pgx (pgxpool) — this package does not use sqlc.
package page

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

func (h *Handler) Routes(r chi.Router) {
	base := "/workspaces/{slug}/projects/{project_id}/pages/"
	r.Get("/workspaces/{slug}/projects/{project_id}/pages-summary/", h.summary)
	r.Post(base, h.create)
	r.Get(base, h.list)
	r.Get(base+"{page_id}/", h.retrieve)
	r.Patch(base+"{page_id}/", h.update)
	r.Delete(base+"{page_id}/", h.destroy)
	r.Post(base+"{page_id}/archive/", h.archive)
	r.Delete(base+"{page_id}/archive/", h.unarchive)
	r.Post(base+"{page_id}/lock/", h.lock)
	r.Delete(base+"{page_id}/lock/", h.unlock)
	r.Post(base+"{page_id}/access/", h.access)
	r.Post(base+"{page_id}/duplicate/", h.duplicate)
	fav := "/workspaces/{slug}/projects/{project_id}/favorite-pages/{page_id}/"
	r.Post(fav, h.favorite)
	r.Delete(fav, h.unfavorite)
}

// ---- row + response --------------------------------------------------------

type pageRow struct {
	ID              uuid.UUID
	Name            string
	Access          int16
	Color           string
	DescriptionHTML string
	OwnedBy         uuid.UUID
	Parent          pgtype.UUID
	ArchivedAt      pgtype.Date
	IsLocked        bool
	ViewProps       []byte
	LogoProps       []byte
	WorkspaceID     uuid.UUID
	CreatedBy       pgtype.UUID
	UpdatedBy       pgtype.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const pageCols = `p.id, p.name, p.access, p.color, p.description_html, p.owned_by,
	p.parent_id, p.archived_at, p.is_locked, p.view_props, p.logo_props,
	p.workspace_id, p.created_by, p.updated_by, p.created_at, p.updated_at`

// same columns, no table alias — for INSERT/UPDATE ... RETURNING.
const pageColsBare = `id, name, access, color, description_html, owned_by,
	parent_id, archived_at, is_locked, view_props, logo_props,
	workspace_id, created_by, updated_by, created_at, updated_at`

func scanPage(row pgx.Row) (pageRow, error) {
	var p pageRow
	err := row.Scan(&p.ID, &p.Name, &p.Access, &p.Color, &p.DescriptionHTML, &p.OwnedBy,
		&p.Parent, &p.ArchivedAt, &p.IsLocked, &p.ViewProps, &p.LogoProps,
		&p.WorkspaceID, &p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuid.UUID(u.Bytes).String()
	return &s
}

func archivedStr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// base carries the fields common to every page representation.
func (p pageRow) base() map[string]any {
	return map[string]any{
		"id":          p.ID.String(),
		"name":        p.Name,
		"owned_by":    p.OwnedBy.String(),
		"access":      int(p.Access),
		"color":       p.Color,
		"parent":      uuidPtr(p.Parent),
		"is_locked":   p.IsLocked,
		"archived_at": archivedStr(p.ArchivedAt),
		"workspace":   p.WorkspaceID.String(),
		"created_at":  p.CreatedAt,
		"updated_at":  p.UpdatedAt,
		"created_by":  uuidPtr(p.CreatedBy),
		"updated_by":  uuidPtr(p.UpdatedBy),
		"view_props":  raw(p.ViewProps),
		"logo_props":  raw(p.LogoProps),
	}
}

// ---- resolve helpers -------------------------------------------------------

func (h *Handler) resolve(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	var wsID uuid.UUID
	err := h.pool.QueryRow(ctx,
		`select id from workspaces where slug=$1 and deleted_at is null`,
		chi.URLParam(r, "slug")).Scan(&wsID)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.UUID{}, uuid.UUID{}, false
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.UUID{}, uuid.UUID{}, false
	}
	return wsID, pid, true
}

// fetchInProject loads a page that is joined to the given project (mirrors
// Django's projects__id=project_id + project_pages__deleted_at__isnull filter).
func (h *Handler) fetchInProject(ctx context.Context, pageID, wsID, projectID uuid.UUID) (pageRow, error) {
	q := `select ` + pageCols + ` from pages p
		join project_pages pp on pp.page_id = p.id and pp.project_id=$3 and pp.deleted_at is null
		where p.id=$1 and p.workspace_id=$2 and p.deleted_at is null`
	return scanPage(h.pool.QueryRow(ctx, q, pageID, wsID, projectID))
}

func (h *Handler) projectIDs(ctx context.Context, pageID uuid.UUID) []string {
	out := []string{}
	rows, err := h.pool.Query(ctx,
		`select project_id from project_pages where page_id=$1 and deleted_at is null`, pageID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			out = append(out, id.String())
		}
	}
	return out
}

func (h *Handler) isFavorite(ctx context.Context, pageID, wsID, userID uuid.UUID) bool {
	var ok bool
	_ = h.pool.QueryRow(ctx,
		`select exists(select 1 from user_favorites
			where entity_type='page' and entity_identifier=$1 and user_id=$2 and workspace_id=$3)`,
		pageID, userID, wsID).Scan(&ok)
	return ok
}

// ---- handlers --------------------------------------------------------------

type pageBody struct {
	Name            *string         `json:"name"`
	Access          *int            `json:"access"`
	Color           *string         `json:"color"`
	DescriptionHTML *string         `json:"description_html"`
	Parent          *string         `json:"parent"`
	ViewProps       json.RawMessage `json:"view_props"`
	LogoProps       json.RawMessage `json:"logo_props"`
	IsLocked        *bool           `json:"is_locked"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var b pageBody
	_ = json.NewDecoder(r.Body).Decode(&b)

	name := ""
	if b.Name != nil {
		name = *b.Name
	}
	access := 0
	if b.Access != nil {
		access = *b.Access
	}
	color := ""
	if b.Color != nil {
		color = *b.Color
	}
	descHTML := "<p></p>"
	if b.DescriptionHTML != nil {
		descHTML = *b.DescriptionHTML
	}
	viewProps := []byte(`{"full_width": false}`)
	if len(b.ViewProps) > 0 {
		viewProps = b.ViewProps
	}
	logoProps := []byte(`{}`)
	if len(b.LogoProps) > 0 {
		logoProps = b.LogoProps
	}
	var parent pgtype.UUID
	if b.Parent != nil {
		if pu, err := uuid.Parse(*b.Parent); err == nil {
			parent = pgtype.UUID{Bytes: pu, Valid: true}
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer tx.Rollback(ctx)

	var pageID uuid.UUID
	err = tx.QueryRow(ctx,
		`insert into pages (workspace_id, name, access, color, description_html, owned_by,
			parent_id, view_props, logo_props, created_by)
		 values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$6) returning id`,
		wsID, name, access, color, descHTML, u.ID, parent, viewProps, logoProps).Scan(&pageID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	_, err = tx.Exec(ctx,
		`insert into project_pages (workspace_id, project_id, page_id, created_by, updated_by)
		 values ($1,$2,$3,$4,$4)`, wsID, pid, pageID, u.ID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	m := p.base()
	m["is_favorite"] = false
	m["label_ids"] = []string{}
	m["project_ids"] = h.projectIDs(ctx, pageID)
	m["description_html"] = p.DescriptionHTML
	httpx.JSON(w, http.StatusCreated, m)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	q := `select ` + pageCols + ` from pages p
		join project_pages pp on pp.page_id = p.id and pp.project_id=$2 and pp.deleted_at is null
		where p.workspace_id=$1 and p.deleted_at is null and p.parent_id is null
		  and (p.owned_by=$3 or p.access=0)
		order by p.created_at desc`
	rows, err := h.pool.Query(ctx, q, wsID, pid, u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	var pages []pageRow
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			continue
		}
		pages = append(pages, p)
	}
	rows.Close()
	for _, p := range pages {
		m := p.base()
		m["is_favorite"] = h.isFavorite(ctx, p.ID, wsID, u.ID)
		m["label_ids"] = []string{}
		m["project_ids"] = h.projectIDs(ctx, p.ID)
		out = append(out, m)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Page not found")
		return
	}
	m := p.base()
	m["is_favorite"] = h.isFavorite(ctx, p.ID, wsID, u.ID)
	m["label_ids"] = []string{}
	m["project_ids"] = h.projectIDs(ctx, p.ID)
	m["description_html"] = p.DescriptionHTML
	m["issue_ids"] = []string{}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
		return
	}
	if p.IsLocked {
		httpx.Error(w, http.StatusBadRequest, "Page is locked")
		return
	}
	var b pageBody
	_ = json.NewDecoder(r.Body).Decode(&b)

	// Only the owner may change access.
	if b.Access != nil && int16(*b.Access) != p.Access && p.OwnedBy != u.ID {
		httpx.Error(w, http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
		return
	}

	name := p.Name
	if b.Name != nil {
		name = *b.Name
	}
	access := p.Access
	if b.Access != nil {
		access = int16(*b.Access)
	}
	color := p.Color
	if b.Color != nil {
		color = *b.Color
	}
	descHTML := p.DescriptionHTML
	if b.DescriptionHTML != nil {
		descHTML = *b.DescriptionHTML
	}
	isLocked := p.IsLocked
	if b.IsLocked != nil {
		isLocked = *b.IsLocked
	}
	viewProps := p.ViewProps
	if len(b.ViewProps) > 0 {
		viewProps = b.ViewProps
	}
	logoProps := p.LogoProps
	if len(b.LogoProps) > 0 {
		logoProps = b.LogoProps
	}
	parent := p.Parent
	if b.Parent != nil {
		if pu, perr := uuid.Parse(*b.Parent); perr == nil {
			parent = pgtype.UUID{Bytes: pu, Valid: true}
		}
	}

	updated, err := scanPage(h.pool.QueryRow(ctx,
		`update pages set name=$2, access=$3, color=$4, description_html=$5, is_locked=$6,
			view_props=$7, logo_props=$8, parent_id=$9, updated_by=$10, updated_at=now()
		 where id=$1 returning `+pageColsBare,
		p.ID, name, access, color, descHTML, isLocked, viewProps, logoProps, parent, u.ID))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	// partial_update returns the plain PageDetailSerializer: description_html is
	// present, but the annotated is_favorite / label_ids / project_ids are NOT.
	m := updated.base()
	m["description_html"] = updated.DescriptionHTML
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) lock(w http.ResponseWriter, r *http.Request)   { h.setLocked(w, r, true) }
func (h *Handler) unlock(w http.ResponseWriter, r *http.Request) { h.setLocked(w, r, false) }

func (h *Handler) setLocked(w http.ResponseWriter, r *http.Request, locked bool) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if _, err := h.fetchInProject(ctx, pageID, wsID, pid); err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_, _ = h.pool.Exec(ctx, `update pages set is_locked=$2, updated_at=now() where id=$1`, pageID, locked)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) access(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var b struct {
		Access *int `json:"access"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	access := 0
	if b.Access != nil {
		access = *b.Access
	}
	if int16(access) != p.Access && p.OwnedBy != u.ID {
		httpx.Error(w, http.StatusBadRequest, "Access cannot be updated since this page is owned by someone else")
		return
	}
	_, _ = h.pool.Exec(ctx, `update pages set access=$2, updated_at=now() where id=$1`, pageID, access)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if _, err := h.fetchInProject(ctx, pageID, wsID, pid); err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	now := time.Now()
	// Remove favorites and archive the page + all descendants.
	_, _ = h.pool.Exec(ctx,
		`delete from user_favorites where entity_type='page' and entity_identifier=$1 and workspace_id=$2`,
		pageID, wsID)
	_, _ = h.pool.Exec(ctx, `
		with recursive descendants as (
			select id from pages where id=$1
			union all
			select p.id from pages p join descendants d on p.parent_id=d.id
		)
		update pages set archived_at=$2, updated_at=now() where id in (select id from descendants)`,
		pageID, now)
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": now.Format("2006-01-02 15:04:05.000000")})
}

func (h *Handler) unarchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	// If the parent is archived, detach so we don't break the hierarchy.
	if p.Parent.Valid {
		var parentArchived pgtype.Date
		_ = h.pool.QueryRow(ctx, `select archived_at from pages where id=$1`,
			uuid.UUID(p.Parent.Bytes)).Scan(&parentArchived)
		if parentArchived.Valid {
			_, _ = h.pool.Exec(ctx, `update pages set parent_id=null where id=$1`, pageID)
		}
	}
	_, _ = h.pool.Exec(ctx, `
		with recursive descendants as (
			select id from pages where id=$1
			union all
			select p.id from pages p join descendants d on p.parent_id=d.id
		)
		update pages set archived_at=null, updated_at=now() where id in (select id from descendants)`,
		pageID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if !p.ArchivedAt.Valid {
		httpx.Error(w, http.StatusBadRequest, "The page should be archived before deleting")
		return
	}
	// Detach children, soft-delete page + its project links, drop favorites.
	_, _ = h.pool.Exec(ctx, `update pages set parent_id=null where parent_id=$1`, pageID)
	_, _ = h.pool.Exec(ctx, `update pages set deleted_at=now() where id=$1`, pageID)
	_, _ = h.pool.Exec(ctx, `update project_pages set deleted_at=now() where page_id=$1`, pageID)
	_, _ = h.pool.Exec(ctx,
		`delete from user_favorites where entity_type='page' and entity_identifier=$1 and workspace_id=$2`,
		pageID, wsID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) duplicate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.fetchInProject(ctx, pageID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if p.Access == 1 && p.OwnedBy != u.ID {
		httpx.Error(w, http.StatusForbidden, "Permission denied")
		return
	}
	projectIDs := h.projectIDs(ctx, pageID)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	defer tx.Rollback(ctx)

	var newID uuid.UUID
	err = tx.QueryRow(ctx,
		`insert into pages (workspace_id, name, access, color, description_html, owned_by,
			parent_id, view_props, logo_props, created_by, updated_by)
		 values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$6,$6) returning id`,
		wsID, p.Name+" (Copy)", p.Access, p.Color, p.DescriptionHTML, u.ID, p.Parent, p.ViewProps, p.LogoProps).Scan(&newID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	for _, projStr := range projectIDs {
		projUUID, perr := uuid.Parse(projStr)
		if perr != nil {
			continue
		}
		_, err = tx.Exec(ctx,
			`insert into project_pages (workspace_id, project_id, page_id, created_by, updated_by)
			 values ($1,$2,$3,$4,$4)`, wsID, projUUID, newID, u.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}

	np, err := h.fetchInProject(ctx, newID, wsID, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	// Duplicate returns PageDetailSerializer annotated with project_ids only
	// (no is_favorite, no label_ids).
	m := np.base()
	m["project_ids"] = h.projectIDs(ctx, newID)
	m["description_html"] = np.DescriptionHTML
	httpx.JSON(w, http.StatusCreated, m)
}

func (h *Handler) favorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_, err = h.pool.Exec(ctx,
		`insert into user_favorites (workspace_id, user_id, entity_type, entity_identifier, project_id)
		 values ($1,$2,'page',$3,$4)`, wsID, u.ID, pageID, pid)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) unfavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, _, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	pageID, err := uuid.Parse(chi.URLParam(r, "page_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_, _ = h.pool.Exec(ctx,
		`delete from user_favorites where entity_type='page' and entity_identifier=$1 and user_id=$2 and workspace_id=$3`,
		pageID, u.ID, wsID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var pub, priv, arch int
	err := h.pool.QueryRow(ctx, `
		select
			count(*) filter (where p.access=0 and p.archived_at is null),
			count(*) filter (where p.access=1 and p.archived_at is null),
			count(*) filter (where p.archived_at is not null)
		from pages p
		join project_pages pp on pp.page_id=p.id and pp.project_id=$2 and pp.deleted_at is null
		where p.workspace_id=$1 and p.deleted_at is null and p.parent_id is null
		  and (p.owned_by=$3 or p.access=0)`,
		wsID, pid, u.ID).Scan(&pub, &priv, &arch)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"public_pages":   pub,
		"private_pages":  priv,
		"archived_pages": arch,
	})
}

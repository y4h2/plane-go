// Package workspace serves the workspace endpoints (create, list-mine, retrieve,
// update, slug-check). Response shape matches the frozen contract exactly.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/", h.create)
	r.Get("/workspaces/{slug}/", h.retrieve)
	r.Patch("/workspaces/{slug}/", h.update)
	r.Get("/users/me/workspaces/", h.listMine)
	r.Get("/workspace-slug-check/", h.slugCheck)
	// invitations
	r.Get("/workspaces/{slug}/invitations/", h.listWorkspaceInvites)
	r.Post("/workspaces/{slug}/invitations/", h.createInvites)
	r.Get("/users/me/workspaces/invitations/", h.listMyInvites)
	r.Post("/users/me/workspaces/invitations/", h.acceptInvites)
	// membership / roles
	r.Get("/workspaces/{slug}/workspace-members/me/", h.memberMe)
	r.Get("/workspaces/{slug}/members/", h.membersList)
	r.Get("/users/me/workspaces/{slug}/project-roles/", h.projectRoles)
}

func (h *Handler) memberMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	u, _ := auth.UserFrom(ctx)
	wm, err := h.q.GetWorkspaceMemberByUser(ctx, gen.GetWorkspaceMemberByUserParams{WorkspaceID: ws.ID, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":                wm.ID.String(),
		"role":              int(wm.Role),
		"draft_issue_count": 0,
		"view_props":        map[string]any{},
		"company_role":      "",
		"created_at":        wm.CreatedAt,
		"updated_at":        wm.UpdatedAt,
		"deleted_at":        nil,
	})
}

func (h *Handler) membersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	rows, err := h.q.ListWorkspaceMembersFull(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		out = append(out, map[string]any{
			"id": m.ID.String(),
			"member": map[string]any{
				"id": m.MemberID.String(), "display_name": m.DisplayName,
				"first_name": m.FirstName, "last_name": m.LastName,
				"avatar": m.Avatar, "avatar_url": nil, "is_bot": m.IsBot, "email": m.Email,
			},
			"role": int(m.Role), "company_role": "", "view_props": map[string]any{},
			"created_at": m.CreatedAt, "updated_at": m.UpdatedAt, "deleted_at": nil,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) projectRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	u, _ := auth.UserFrom(ctx)
	rows, err := h.q.ProjectRolesForUser(ctx, gen.ProjectRolesForUserParams{WorkspaceID: ws.ID, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := map[string]int{}
	for _, row := range rows {
		out[row.ProjectID.String()] = int(row.Role)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// wsResponse is the exact wire shape (17 keys) the contract pins.
type wsResponse struct {
	ID               string     `json:"id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	CreatedBy        *string    `json:"created_by"`
	UpdatedBy        *string    `json:"updated_by"`
	DeletedAt        *time.Time `json:"deleted_at"`
	Name             string     `json:"name"`
	Logo             *string    `json:"logo"`
	LogoAsset        *string    `json:"logo_asset"`
	LogoURL          *string    `json:"logo_url"`
	Owner            string     `json:"owner"`
	Slug             string     `json:"slug"`
	OrganizationSize *string    `json:"organization_size"`
	Timezone         string     `json:"timezone"`
	BackgroundColor  string     `json:"background_color"`
	TotalMembers     int        `json:"total_members"`
	Role             *int       `json:"role"`
}

func wsResp(w gen.Workspace, total int, role *int) wsResponse {
	return wsResponse{
		ID:               w.ID.String(),
		CreatedAt:        w.CreatedAt,
		UpdatedAt:        w.UpdatedAt,
		CreatedBy:        dbx.StrPtr(w.CreatedBy),
		UpdatedBy:        dbx.StrPtr(w.UpdatedBy),
		DeletedAt:        w.DeletedAt,
		Name:             w.Name,
		Logo:             w.Logo,
		LogoAsset:        dbx.StrPtr(w.LogoAsset),
		LogoURL:          w.Logo, // no asset pipeline yet; mirrors logo (nil when unset)
		Owner:            w.OwnerID.String(),
		Slug:             w.Slug,
		OrganizationSize: w.OrganizationSize,
		Timezone:         w.Timezone,
		BackgroundColor:  w.BackgroundColor,
		TotalMembers:     total,
		Role:             role,
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	var body struct {
		Name             string  `json:"name"`
		Slug             string  `json:"slug"`
		OrganizationSize *string `json:"organization_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	name := strings.TrimSpace(body.Name)
	slug := strings.TrimSpace(body.Slug)
	if name == "" || slug == "" || len(name) > 80 || len(slug) > 48 || !slugRe.MatchString(slug) {
		httpx.Error(w, http.StatusBadRequest, "Please provide a valid name and slug")
		return
	}
	ctx := r.Context()
	ws, err := h.q.CreateWorkspace(ctx, gen.CreateWorkspaceParams{
		Name:             name,
		Slug:             slug,
		OwnerID:          u.ID,
		OrganizationSize: body.OrganizationSize,
		CreatedBy:        dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if err := h.q.AddWorkspaceMember(ctx, gen.AddWorkspaceMemberParams{
		WorkspaceID: ws.ID, MemberID: u.ID, Role: 20,
	}); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	role := 20
	httpx.JSON(w, http.StatusCreated, wsResp(ws, 1, &role))
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, slug)
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, h.decorate(ctx, ws))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, slug)
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	var body struct {
		Name             *string `json:"name"`
		OrganizationSize *string `json:"organization_size"`
		Timezone         *string `json:"timezone"`
		BackgroundColor  *string `json:"background_color"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := ws.Name
	if body.Name != nil {
		name = *body.Name
	}
	orgSize := ws.OrganizationSize
	if body.OrganizationSize != nil {
		orgSize = body.OrganizationSize
	}
	tz := ws.Timezone
	if body.Timezone != nil {
		tz = *body.Timezone
	}
	bg := ws.BackgroundColor
	if body.BackgroundColor != nil {
		bg = *body.BackgroundColor
	}
	u, _ := auth.UserFrom(ctx)
	updated, err := h.q.UpdateWorkspace(ctx, gen.UpdateWorkspaceParams{
		Slug:             slug,
		Name:             name,
		OrganizationSize: orgSize,
		Timezone:         tz,
		BackgroundColor:  bg,
		UpdatedBy:        dbx.PgUUID(u.ID),
	})
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, h.decorate(ctx, updated))
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	rows, err := h.q.ListUserWorkspaces(r.Context(), u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]wsResponse, 0, len(rows))
	for _, row := range rows {
		role := int(row.MemberRole)
		out = append(out, wsResp(gen.Workspace{
			ID: row.ID, Name: row.Name, Slug: row.Slug, OwnerID: row.OwnerID,
			Logo: row.Logo, LogoAsset: row.LogoAsset, OrganizationSize: row.OrganizationSize,
			Timezone: row.Timezone, BackgroundColor: row.BackgroundColor,
			CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, DeletedAt: row.DeletedAt,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}, int(row.TotalMembers), &role))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) slugCheck(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	exists, err := h.q.WorkspaceSlugExists(r.Context(), slug)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"status": !exists})
}

// decorate builds the response for a single workspace, computing total_members
// and the requesting user's role.
func (h *Handler) decorate(ctx context.Context, ws gen.Workspace) wsResponse {
	count, _ := h.q.WorkspaceMemberCount(ctx, ws.ID)
	var role *int
	if u, ok := auth.UserFrom(ctx); ok {
		if rr, err := h.q.GetWorkspaceMemberRole(ctx, gen.GetWorkspaceMemberRoleParams{
			WorkspaceID: ws.ID, MemberID: u.ID,
		}); err == nil {
			ri := int(rr)
			role = &ri
		}
	}
	return wsResp(ws, int(count), role)
}

func (h *Handler) notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
}

// ---- invitations -----------------------------------------------------------

func (h *Handler) createInvites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	var body struct {
		Emails []struct {
			Email string `json:"email"`
			Role  int    `json:"role"`
		} `json:"emails"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Emails) == 0 {
		httpx.Error(w, http.StatusBadRequest, "At least one email is required")
		return
	}
	out := make([]map[string]any, 0, len(body.Emails))
	for _, e := range body.Emails {
		role := int16(e.Role)
		if role == 0 {
			role = 15
		}
		inv, err := h.q.CreateWorkspaceInvite(ctx, gen.CreateWorkspaceInviteParams{
			WorkspaceID: ws.ID, Email: strings.ToLower(strings.TrimSpace(e.Email)), Role: role,
		})
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
			return
		}
		out = append(out, map[string]any{
			"id": inv.ID.String(), "email": inv.Email, "role": int(inv.Role),
			"workspace": ws.ID.String(),
		})
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listWorkspaceInvites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	rows, err := h.q.ListWorkspaceInvites(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		out = append(out, map[string]any{
			"id": inv.ID.String(), "email": inv.Email, "role": int(inv.Role),
			"accepted": inv.Accepted, "workspace": ws.ID.String(), "created_at": inv.CreatedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listMyInvites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	rows, err := h.q.ListInvitesForEmail(ctx, u.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		out = append(out, map[string]any{
			"id":         inv.ID.String(),
			"role":       int(inv.Role),
			"accepted":   inv.Accepted,
			"created_at": inv.CreatedAt,
			"workspace": map[string]any{
				"id": inv.WorkspaceID.String(), "slug": inv.WorkspaceSlug, "name": inv.WorkspaceName,
			},
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) acceptInvites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Invitations []string `json:"invitations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	for _, id := range body.Invitations {
		iid, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		inv, err := h.q.GetInvite(ctx, iid)
		if err != nil {
			continue
		}
		_ = h.q.AddWorkspaceMember(ctx, gen.AddWorkspaceMemberParams{
			WorkspaceID: inv.WorkspaceID, MemberID: u.ID, Role: inv.Role,
		})
		_ = h.q.AcceptInvite(ctx, iid)
	}
	w.WriteHeader(http.StatusNoContent)
}

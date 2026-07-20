// Package project serves the project endpoints and the nested project-member
// endpoints. Response shapes match the frozen contract exactly (the 43-key
// ProjectListSerializer for create/details/retrieve/patch; a bare .values()
// subset for the list; a 6-key row for members).
package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
	"planego/internal/state"
)

const roleAdmin = 20

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/", h.create)
	r.Get("/workspaces/{slug}/projects/", h.list)
	r.Get("/workspaces/{slug}/projects/details/", h.details)
	r.Get("/workspaces/{slug}/project-identifiers/", h.identifiers)
	r.Post("/workspaces/{slug}/projects/{project_id}/archive/", h.archive)
	r.Delete("/workspaces/{slug}/projects/{project_id}/archive/", h.unarchive)
	r.Get("/workspaces/{slug}/projects/{project_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/", h.update)
	r.Delete("/workspaces/{slug}/projects/{project_id}/", h.destroy)
	// members
	r.Get("/workspaces/{slug}/projects/{project_id}/members/", h.listMembers)
	r.Post("/workspaces/{slug}/projects/{project_id}/members/", h.addMembers)
	r.Get("/workspaces/{slug}/projects/{project_id}/members/{member_id}/", h.retrieveMember)
	r.Patch("/workspaces/{slug}/projects/{project_id}/members/{member_id}/", h.updateMember)
	r.Get("/workspaces/{slug}/projects/{project_id}/project-members/me/", h.membersMe)
	// project-scoped issue search (IssueSearchEndpoint)
	r.Get("/workspaces/{slug}/projects/{project_id}/search-issues/", h.searchIssues)
}

func (h *Handler) updateMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	mrid, err := uuid.Parse(chi.URLParam(r, "member_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Role *int `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pm, err := h.q.GetProjectMember(ctx, gen.GetProjectMemberParams{ProjectID: pid, ID: mrid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	role := pm.Role
	if body.Role != nil {
		role = int16(*body.Role)
	}
	updated, err := h.q.UpdateProjectMemberRole(ctx, gen.UpdateProjectMemberRoleParams{ProjectID: pid, ID: mrid, Role: role})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, h.memberDetail(ctx, updated, ws))
}

// ---- response shapes -------------------------------------------------------

// projectResponse is the exact 43-key ProjectListSerializer wire shape. Fields
// we don't yet store are emitted with faithful defaults (null / false / {} / 0).
type projectResponse struct {
	ID                    string         `json:"id"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	CreatedBy             *string        `json:"created_by"`
	UpdatedBy             *string        `json:"updated_by"`
	DeletedAt             *time.Time     `json:"deleted_at"`
	Name                  string         `json:"name"`
	Description           string         `json:"description"`
	DescriptionText       *string        `json:"description_text"`
	DescriptionHTML       *string        `json:"description_html"`
	Network               int            `json:"network"`
	Workspace             string         `json:"workspace"`
	Identifier            string         `json:"identifier"`
	DefaultAssignee       *string        `json:"default_assignee"`
	ProjectLead           *string        `json:"project_lead"`
	Emoji                 *string        `json:"emoji"`
	IconProp              *string        `json:"icon_prop"`
	ModuleView            bool           `json:"module_view"`
	CycleView             bool           `json:"cycle_view"`
	IssueViewsView        bool           `json:"issue_views_view"`
	PageView              bool           `json:"page_view"`
	IntakeView            bool           `json:"intake_view"`
	IsTimeTrackingEnabled bool           `json:"is_time_tracking_enabled"`
	IsIssueTypeEnabled    bool           `json:"is_issue_type_enabled"`
	GuestViewAllFeatures  bool           `json:"guest_view_all_features"`
	CoverImage            *string        `json:"cover_image"`
	CoverImageAsset       *string        `json:"cover_image_asset"`
	Estimate              *string        `json:"estimate"`
	ArchiveIn             int            `json:"archive_in"`
	CloseIn               int            `json:"close_in"`
	LogoProps             map[string]any `json:"logo_props"`
	DefaultState          *string        `json:"default_state"`
	ArchivedAt            *time.Time     `json:"archived_at"`
	Timezone              string         `json:"timezone"`
	ExternalSource        *string        `json:"external_source"`
	ExternalID            *string        `json:"external_id"`
	IsFavorite            bool           `json:"is_favorite"`
	SortOrder             httpx.Float    `json:"sort_order"`
	MemberRole            *int           `json:"member_role"`
	Anchor                *string        `json:"anchor"`
	Members               []string       `json:"members"`
	CoverImageURL         *string        `json:"cover_image_url"`
	InboxView             bool           `json:"inbox_view"`
	NextWorkItemSequence  int            `json:"next_work_item_sequence"`
}

func projectResp(p gen.Project, memberRole *int, members []string) projectResponse {
	return projectResponse{
		ID:                    p.ID.String(),
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
		CreatedBy:             dbx.StrPtr(p.CreatedBy),
		UpdatedBy:             dbx.StrPtr(p.UpdatedBy),
		DeletedAt:             p.DeletedAt,
		Name:                  p.Name,
		Description:           p.Description,
		Network:               int(p.Network),
		Workspace:             p.WorkspaceID.String(),
		Identifier:            p.Identifier,
		ModuleView:            p.ModuleView,
		CycleView:             p.CycleView,
		IssueViewsView:        p.IssueViewsView,
		PageView:              p.PageView,
		IntakeView:            p.IntakeView,
		IsTimeTrackingEnabled: p.IsTimeTrackingEnabled,
		IsIssueTypeEnabled:    p.IsIssueTypeEnabled,
		GuestViewAllFeatures:  p.GuestViewAllFeatures,
		LogoProps:             map[string]any{},
		Timezone:              "UTC",
		SortOrder:             httpx.Float(p.SortOrder),
		MemberRole:            memberRole,
		Members:               members,
		NextWorkItemSequence:  1,
		CoverImageAsset:       dbx.StrPtr(p.CoverImageAsset),
		CoverImageURL:         coverURL(p.CoverImageAsset),
		// QUIRK (matches Django): `inbox_view` is a read-only alias mirroring
		// `intake_view` on the wire, but the PATCH handler only writes intake_view
		// when the caller sends `inbox_view` in the request body (see update()).
		InboxView: p.IntakeView,
	}
}

// coverURL renders a project's cover asset as a servable URL (nil when unset).
func coverURL(a pgtype.UUID) *string {
	s := dbx.StrPtr(a)
	if s == nil {
		return nil
	}
	url := "/api/assets/v2/static/" + *s + "/"
	return &url
}

// listItem is the bare .values() subset the plain list endpoint returns.
type listItem struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Identifier  string         `json:"identifier"`
	Workspace   string         `json:"workspace"`
	Network     int            `json:"network"`
	MemberRole  *int           `json:"member_role"`
	IntakeCount int            `json:"intake_count"`
	SortOrder   httpx.Float    `json:"sort_order"`
	LogoProps   map[string]any `json:"logo_props"`
	CreatedAt   time.Time      `json:"created_at"`
}

type memberRow struct {
	ID           string    `json:"id"`
	Role         int       `json:"role"`
	Member       string    `json:"member"`
	Project      string    `json:"project"`
	OriginalRole int       `json:"original_role"`
	CreatedAt    time.Time `json:"created_at"`
}

func memberRowOf(m gen.ProjectMember) memberRow {
	return memberRow{
		ID: m.ID.String(), Role: int(m.Role), Member: m.MemberID.String(),
		Project: m.ProjectID.String(), OriginalRole: int(m.Role), CreatedAt: m.CreatedAt,
	}
}

// ---- handlers --------------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Name            string `json:"name"`
		Identifier      string `json:"identifier"`
		Description     string `json:"description"`
		CoverImageAsset string `json:"cover_image_asset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	name := strings.TrimSpace(body.Name)
	ident := strings.TrimSpace(body.Identifier)
	fieldErrs := map[string][]string{}
	if name == "" {
		fieldErrs["name"] = []string{"This field is required."}
	}
	if ident == "" {
		fieldErrs["identifier"] = []string{"This field is required."}
	}
	if len(fieldErrs) > 0 {
		httpx.JSON(w, http.StatusBadRequest, fieldErrs)
		return
	}
	p, err := h.q.CreateProject(ctx, gen.CreateProjectParams{
		WorkspaceID: ws.ID, Name: name, Identifier: ident,
		Description: body.Description, CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if _, err := h.q.AddProjectMember(ctx, gen.AddProjectMemberParams{
		ProjectID: p.ID, WorkspaceID: ws.ID, MemberID: u.ID, Role: roleAdmin,
	}); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	// seed the five default workflow states (matches Plane's project bootstrap)
	_ = state.SeedDefaults(ctx, h.q, ws.ID, p.ID)
	// attach an uploaded cover image if one was provided
	if cid, err := uuid.Parse(body.CoverImageAsset); err == nil {
		_ = h.q.SetProjectCover(ctx, gen.SetProjectCoverParams{ID: p.ID, WorkspaceID: ws.ID, CoverImageAsset: dbx.PgUUID(cid)})
		p.CoverImageAsset = dbx.PgUUID(cid)
	}
	role := roleAdmin
	httpx.JSON(w, http.StatusCreated, projectResp(p, &role, h.memberIDs(ctx, p.ID)))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	rows, err := h.q.ListProjects(ctx, gen.ListProjectsParams{WorkspaceID: ws.ID, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]listItem, 0, len(rows))
	for _, row := range rows {
		role := int(row.MemberRole)
		out = append(out, listItem{
			ID: row.ID.String(), Name: row.Name, Identifier: row.Identifier,
			Workspace: row.WorkspaceID.String(), Network: int(row.Network),
			MemberRole: &role, IntakeCount: 0, SortOrder: httpx.Float(row.SortOrder),
			LogoProps: map[string]any{}, CreatedAt: row.CreatedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) details(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	rows, err := h.q.ListProjects(ctx, gen.ListProjectsParams{WorkspaceID: ws.ID, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]projectResponse, 0, len(rows))
	for _, row := range rows {
		role := int(row.MemberRole)
		out = append(out, projectResp(gen.Project{
			ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, Identifier: row.Identifier,
			Description: row.Description, Network: row.Network, SortOrder: row.SortOrder,
			CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, DeletedAt: row.DeletedAt,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}, &role, []string{}))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.q.GetProjectByID(ctx, gen.GetProjectByIDParams{ID: pid, WorkspaceID: ws.ID})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	// Python's retrieve() filters archived_at__isnull=True, so an archived
	// project 404s from the normal detail route (unarchive it to see it again).
	if p.ArchivedAt != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	// permission: not a workspace member -> 403; workspace member but not a
	// project member -> 409; project member -> 200.
	if _, err := h.q.GetWorkspaceMemberRole(ctx, gen.GetWorkspaceMemberRoleParams{WorkspaceID: ws.ID, MemberID: u.ID}); err != nil {
		httpx.Error(w, http.StatusForbidden, "You don't have permission to access this project")
		return
	}
	pm, err := h.q.GetProjectMemberByUser(ctx, gen.GetProjectMemberByUserParams{ProjectID: pid, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusConflict, "You are not a member of this project")
		return
	}
	role := int(pm.Role)
	httpx.JSON(w, http.StatusOK, projectResp(p, &role, h.memberIDs(ctx, pid)))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	p, err := h.q.GetProjectByID(ctx, gen.GetProjectByIDParams{ID: pid, WorkspaceID: ws.ID})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	// Matches Django: only a workspace admin OR a project admin (role 20) may
	// PATCH a project. Any other project member (or non-member) gets 403.
	if !h.isWorkspaceOrProjectAdmin(ctx, ws.ID, pid, u.ID) {
		httpx.Error(w, http.StatusForbidden, "You don't have the required permissions.")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		ModuleView  *bool   `json:"module_view"`
		CycleView   *bool   `json:"cycle_view"`
		IssueViews  *bool   `json:"issue_views_view"`
		PageView    *bool   `json:"page_view"`
		// QUIRK (matches Django): the intake toggle is only settable through the
		// `inbox_view` alias -- a bare `intake_view` key in the body is ignored,
		// mirroring plane/app/views/project/base.py's
		// `intake_view = request.data.get("inbox_view", project.intake_view)`.
		InboxView      *bool `json:"inbox_view"`
		TimeTracking   *bool `json:"is_time_tracking_enabled"`
		IssueType      *bool `json:"is_issue_type_enabled"`
		GuestAllViewFn *bool `json:"guest_view_all_features"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, desc := p.Name, p.Description
	if body.Name != nil {
		name = *body.Name
	}
	if body.Description != nil {
		desc = *body.Description
	}
	moduleView, cycleView, issueViewsView, pageView := p.ModuleView, p.CycleView, p.IssueViewsView, p.PageView
	intakeView := p.IntakeView
	timeTracking, issueType, guestViewAll := p.IsTimeTrackingEnabled, p.IsIssueTypeEnabled, p.GuestViewAllFeatures
	if body.ModuleView != nil {
		moduleView = *body.ModuleView
	}
	if body.CycleView != nil {
		cycleView = *body.CycleView
	}
	if body.IssueViews != nil {
		issueViewsView = *body.IssueViews
	}
	if body.PageView != nil {
		pageView = *body.PageView
	}
	if body.InboxView != nil {
		intakeView = *body.InboxView
	}
	if body.TimeTracking != nil {
		timeTracking = *body.TimeTracking
	}
	if body.IssueType != nil {
		issueType = *body.IssueType
	}
	if body.GuestAllViewFn != nil {
		guestViewAll = *body.GuestAllViewFn
	}
	updated, err := h.q.UpdateProjectFeatures(ctx, gen.UpdateProjectFeaturesParams{
		ID: pid, WorkspaceID: ws.ID, Name: name, Description: desc,
		ModuleView: moduleView, CycleView: cycleView, IssueViewsView: issueViewsView, PageView: pageView,
		IntakeView: intakeView, IsTimeTrackingEnabled: timeTracking, IsIssueTypeEnabled: issueType,
		GuestViewAllFeatures: guestViewAll, UpdatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	var role *int
	if pm, err := h.q.GetProjectMemberByUser(ctx, gen.GetProjectMemberByUserParams{ProjectID: pid, MemberID: u.ID}); err == nil {
		ri := int(pm.Role)
		role = &ri
	}
	httpx.JSON(w, http.StatusOK, projectResp(updated, role, h.memberIDs(ctx, pid)))
}

// isWorkspaceOrProjectAdmin matches Django's partial_update permission check:
// the caller must be either a workspace admin (role 20) or a project admin
// (role 20) of this specific project. Any other role -- or no relationship at
// all -- is denied.
func (h *Handler) isWorkspaceOrProjectAdmin(ctx context.Context, workspaceID, projectID, userID uuid.UUID) bool {
	if role, err := h.q.GetWorkspaceMemberRole(ctx, gen.GetWorkspaceMemberRoleParams{WorkspaceID: workspaceID, MemberID: userID}); err == nil && int(role) == roleAdmin {
		return true
	}
	if pm, err := h.q.GetProjectMemberByUser(ctx, gen.GetProjectMemberByUserParams{ProjectID: projectID, MemberID: userID}); err == nil && int(pm.Role) == roleAdmin {
		return true
	}
	return false
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if err := h.q.SoftDeleteProject(ctx, gen.SoftDeleteProjectParams{ID: pid, WorkspaceID: ws.ID}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.ArchiveProject(ctx, gen.ArchiveProjectParams{ID: pid, WorkspaceID: ws.ID})
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": time.Now().UTC()})
}

func (h *Handler) unarchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.UnarchiveProject(ctx, gen.UnarchiveProjectParams{ID: pid, WorkspaceID: ws.ID})
	w.WriteHeader(http.StatusNoContent)
}

// searchIssueRow is the exact 11-key bare `.values()` row IssueSearchEndpoint
// returns (dunder-joined related-field names preserved verbatim).
type searchIssueRow struct {
	Name              string `json:"name"`
	ID                string `json:"id"`
	StartDate         any    `json:"start_date"`
	SequenceID        int    `json:"sequence_id"`
	ProjectName       string `json:"project__name"`
	ProjectIdentifier string `json:"project__identifier"`
	ProjectID         string `json:"project_id"`
	WorkspaceSlug     string `json:"workspace__slug"`
	StateName         string `json:"state__name"`
	StateGroup        string `json:"state__group"`
	StateColor        string `json:"state__color"`
}

// seqDigitsRe extracts the same "\b\d+\b" substrings Django's search_issues()
// pulls out of the query text to OR-match against sequence_id.
var seqDigitsRe = regexp.MustCompile(`\b\d+\b`)

// searchIssues backs GET .../projects/{project_id}/search-issues/
// (IssueSearchEndpoint). Only the `search` and `workspace_search` query params
// are implemented -- the reference also accepts parent/issue_relation/cycle/
// module/sub_issue/target_date/issue_id narrowing filters, which are out of
// scope here (not exercised by the frontend's project-scoped picker this
// endpoint primarily serves).
func (h *Handler) searchIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	query := r.URL.Query().Get("search")
	workspaceSearch := r.URL.Query().Get("workspace_search") == "true"

	var seqIDs []int32
	if query != "" && len(query) <= 20 {
		for _, m := range seqDigitsRe.FindAllString(query, -1) {
			if n, err := strconv.ParseInt(m, 10, 32); err == nil {
				seqIDs = append(seqIDs, int32(n))
			}
		}
	}

	out := make([]searchIssueRow, 0)
	if workspaceSearch {
		rows, err := h.q.SearchWorkspaceIssues(ctx, gen.SearchWorkspaceIssuesParams{
			Slug: ws.Slug, MemberID: u.ID, Column3: query, Column4: seqIDs,
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		for _, row := range rows {
			out = append(out, searchIssueRow{
				Name:              row.Name,
				ID:                row.ID.String(),
				StartDate:         dateVal(row.StartDate),
				SequenceID:        int(row.SequenceID),
				ProjectName:       row.ProjectName,
				ProjectIdentifier: row.ProjectIdentifier,
				ProjectID:         row.ProjectID.String(),
				WorkspaceSlug:     row.WorkspaceSlug,
				StateName:         row.StateName,
				StateGroup:        row.StateGroup,
				StateColor:        row.StateColor,
			})
		}
	} else {
		rows, err := h.q.SearchProjectIssues(ctx, gen.SearchProjectIssuesParams{
			Slug: ws.Slug, MemberID: u.ID, ID: pid, Column4: query, Column5: seqIDs,
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
			return
		}
		for _, row := range rows {
			out = append(out, searchIssueRow{
				Name:              row.Name,
				ID:                row.ID.String(),
				StartDate:         dateVal(row.StartDate),
				SequenceID:        int(row.SequenceID),
				ProjectName:       row.ProjectName,
				ProjectIdentifier: row.ProjectIdentifier,
				ProjectID:         row.ProjectID.String(),
				WorkspaceSlug:     row.WorkspaceSlug,
				StateName:         row.StateName,
				StateGroup:        row.StateGroup,
				StateColor:        row.StateColor,
			})
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// dateVal renders a nullable date column as "YYYY-MM-DD" or null (Django's
// DateField wire format).
func dateVal(d pgtype.Date) any {
	if !d.Valid {
		return nil
	}
	return d.Time.Format("2006-01-02")
}

func (h *Handler) identifiers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if strings.TrimSpace(name) == "" {
		httpx.Error(w, http.StatusBadRequest, "Please provide a valid name")
		return
	}
	rows, err := h.q.ListProjectIdentifiers(ctx, gen.ListProjectIdentifiersParams{WorkspaceID: ws.ID, Upper: name})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	ids := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, map[string]string{"name": row.Name, "project": row.ID.String()})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"exists": len(ids), "identifiers": ids})
}

// ---- members ---------------------------------------------------------------

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	rows, err := h.q.ListProjectMembers(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]memberRow, 0, len(rows))
	for _, m := range rows {
		out = append(out, memberRowOf(m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) addMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	var body struct {
		Members []struct {
			MemberID string `json:"member_id"`
			Role     int    `json:"role"`
		} `json:"members"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	if len(body.Members) == 0 {
		httpx.Error(w, http.StatusBadRequest, "At least one member is required")
		return
	}
	out := make([]memberRow, 0, len(body.Members))
	for _, m := range body.Members {
		mid, err := uuid.Parse(m.MemberID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
			return
		}
		// only existing workspace members may be added
		if _, err := h.q.GetWorkspaceMemberRole(ctx, gen.GetWorkspaceMemberRoleParams{WorkspaceID: ws.ID, MemberID: mid}); err != nil {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
			return
		}
		role := int16(m.Role)
		if role == 0 {
			role = 15
		}
		pm, err := h.q.AddProjectMember(ctx, gen.AddProjectMemberParams{
			ProjectID: pid, WorkspaceID: ws.ID, MemberID: mid, Role: role,
		})
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
			return
		}
		out = append(out, memberRowOf(pm))
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) retrieveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	mrid, err := uuid.Parse(chi.URLParam(r, "member_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	pm, err := h.q.GetProjectMember(ctx, gen.GetProjectMemberParams{ProjectID: pid, ID: mrid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, h.memberDetail(ctx, pm, ws))
}

func (h *Handler) membersMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	pm, err := h.q.GetProjectMemberByUser(ctx, gen.GetProjectMemberByUserParams{ProjectID: pid, MemberID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, h.memberDetail(ctx, pm, ws))
}

// memberDetail builds the nested member/project/workspace response used by the
// single-member retrieve and project-members/me endpoints.
func (h *Handler) memberDetail(ctx context.Context, pm gen.ProjectMember, ws gen.Workspace) map[string]any {
	member := map[string]any{"id": pm.MemberID.String()}
	if u, err := h.q.GetUserByID(ctx, pm.MemberID); err == nil {
		member["id"] = u.ID.String()
		member["display_name"] = u.DisplayName
		member["email"] = u.Email
	}
	proj := map[string]any{"id": pm.ProjectID.String()}
	if p, err := h.q.GetProjectByID(ctx, gen.GetProjectByIDParams{ID: pm.ProjectID, WorkspaceID: ws.ID}); err == nil {
		proj["name"] = p.Name
		proj["identifier"] = p.Identifier
	}
	return map[string]any{
		"id":         pm.ID.String(),
		"role":       int(pm.Role),
		"member":     member,
		"project":    proj,
		"workspace":  map[string]any{"id": ws.ID.String(), "slug": ws.Slug, "name": ws.Name},
		"created_at": pm.CreatedAt,
	}
}

// ---- helpers ---------------------------------------------------------------

func (h *Handler) workspace(ctx context.Context, w http.ResponseWriter, slug string) (gen.Workspace, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		} else {
			httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		}
		return gen.Workspace{}, false
	}
	return ws, true
}

func (h *Handler) memberIDs(ctx context.Context, pid uuid.UUID) []string {
	ids, err := h.q.ProjectMemberUserIDs(ctx, pid)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

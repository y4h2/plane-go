// Package search serves workspace search, entity (@mention) search, and
// project-stats — the top command bar and related widgets.
package search

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"planego/internal/db/gen"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/search/", h.search)
	r.Get("/workspaces/{slug}/entity-search/", h.entitySearch)
	r.Get("/workspaces/{slug}/project-stats/", h.projectStats)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	// Python treats an empty `search` param as "no filter" (query or None), so
	// it returns everything visible. Our ILIKE '%'||term||'%' matches all rows
	// when term is empty, so we always run the queries — no short-circuit.
	term := strings.TrimSpace(r.URL.Query().Get("search"))
	issues := []map[string]any{}
	projects := []map[string]any{}
	cycles := []map[string]any{}
	modules := []map[string]any{}
	if rows, err := h.q.SearchIssues(ctx, gen.SearchIssuesParams{WorkspaceID: ws.ID, Column2: &term}); err == nil {
		for _, x := range rows {
			issues = append(issues, map[string]any{
				"name": x.Name, "id": x.ID.String(), "sequence_id": int(x.SequenceID),
				"project__identifier": x.ProjectIdentifier, "project_id": x.ProjectID.String(),
				"workspace__slug": x.WorkspaceSlug,
			})
		}
	}
	if rows, err := h.q.SearchProjects(ctx, gen.SearchProjectsParams{WorkspaceID: ws.ID, Column2: &term}); err == nil {
		for _, x := range rows {
			projects = append(projects, map[string]any{
				"name": x.Name, "id": x.ID.String(), "identifier": x.Identifier, "workspace__slug": x.WorkspaceSlug,
			})
		}
	}
	if rows, err := h.q.SearchCycles(ctx, gen.SearchCyclesParams{WorkspaceID: ws.ID, Column2: &term}); err == nil {
		for _, x := range rows {
			cycles = append(cycles, map[string]any{
				"name": x.Name, "id": x.ID.String(), "project_id": x.ProjectID.String(),
				"project__identifier": x.ProjectIdentifier, "workspace__slug": x.WorkspaceSlug,
			})
		}
	}
	if rows, err := h.q.SearchModules(ctx, gen.SearchModulesParams{WorkspaceID: ws.ID, Column2: &term}); err == nil {
		for _, x := range rows {
			modules = append(modules, map[string]any{
				"name": x.Name, "id": x.ID.String(), "project_id": x.ProjectID.String(),
				"project__identifier": x.ProjectIdentifier, "workspace__slug": x.WorkspaceSlug,
			})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": map[string]any{
		"workspace": []any{}, "project": projects, "issue": issues, "cycle": cycles,
		"module": modules, "issue_view": []any{}, "page": []any{}, "intake": []any{},
	}})
}

func (h *Handler) entitySearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	term := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	rows, _ := h.q.ListWorkspaceMembersFull(ctx, ws.ID)
	mentions := []map[string]any{}
	for _, m := range rows {
		if term != "" && !strings.Contains(strings.ToLower(m.DisplayName), term) {
			continue
		}
		mentions = append(mentions, map[string]any{
			"member__display_name": m.DisplayName, "member__id": m.MemberID.String(), "member__avatar_url": m.Avatar,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user_mention": mentions})
}

func (h *Handler) projectStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	rows, err := h.q.ProjectStats(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	// Optional ?project_ids=a,b filters the set; ?fields=x,y restricts the
	// returned columns to id + the requested ones (matching Django .values()).
	idFilter := map[string]bool{}
	if raw := strings.TrimSpace(r.URL.Query().Get("project_ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				idFilter[id] = true
			}
		}
	}
	fieldFilter := map[string]bool{}
	if raw := strings.TrimSpace(r.URL.Query().Get("fields")); raw != "" {
		for _, f := range strings.Split(raw, ",") {
			if f = strings.TrimSpace(f); f != "" {
				fieldFilter[f] = true
			}
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		id := s.ID.String()
		if len(idFilter) > 0 && !idFilter[id] {
			continue
		}
		full := map[string]any{
			"total_issues": int(s.TotalIssues), "completed_issues": int(s.CompletedIssues),
			"total_cycles": int(s.TotalCycles), "total_modules": int(s.TotalModules), "total_members": int(s.TotalMembers),
		}
		row := map[string]any{"id": id} // id is always present
		if len(fieldFilter) > 0 {
			for k, v := range full {
				if fieldFilter[k] {
					row[k] = v
				}
			}
		} else {
			for k, v := range full {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	httpx.JSON(w, http.StatusOK, out)
}

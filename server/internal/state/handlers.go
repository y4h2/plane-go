// Package state serves the workflow-state endpoints. Create returns 200 (a
// deliberate quirk, not 201). List/grouped responses add an `order` field that
// the single-object responses omit. New projects are seeded with five default
// states via SeedDefaults.
package state

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{slug}/projects/{project_id}/states/", h.create)
	r.Get("/workspaces/{slug}/projects/{project_id}/states/", h.list)
	r.Get("/workspaces/{slug}/projects/{project_id}/states/{state_id}/", h.retrieve)
	r.Patch("/workspaces/{slug}/projects/{project_id}/states/{state_id}/", h.update)
	r.Post("/workspaces/{slug}/projects/{project_id}/states/{state_id}/mark-default/", h.markDefault)
	r.Delete("/workspaces/{slug}/projects/{project_id}/states/{state_id}/", h.destroy)
	r.Get("/workspaces/{slug}/states/", h.workspaceStates)
	r.Get("/workspaces/{slug}/projects/{project_id}/intake-state/", h.intakeState)
}

// intakeState returns the project's triage/intake state. We don't model intake
// separately, so this synthesizes a stable triage state per project.
func (h *Handler) intakeState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("triage:"+pid.String()))
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":           id.String(),
		"project_id":   pid.String(),
		"workspace_id": ws.ID.String(),
		"name":         "Triage",
		"color":        "#4E5355",
		"group":        "triage",
		"default":      false,
		"description":  "",
		"sequence":     httpx.Float(65535),
	})
}

// stateResp is the 9-key single-object shape (no `order`).
func stateResp(s gen.State) map[string]any {
	return map[string]any{
		"id":           s.ID.String(),
		"project_id":   s.ProjectID.String(),
		"workspace_id": s.WorkspaceID.String(),
		"name":         s.Name,
		"color":        s.Color,
		"group":        s.GroupName,
		"default":      s.IsDefault,
		"description":  s.Description,
		"sequence":     httpx.Float(s.Sequence),
	}
}

// stateListItem adds `order` (list/grouped responses include it).
func stateListItem(s gen.State) map[string]any {
	m := stateResp(s)
	m["order"] = httpx.Float(s.Sequence)
	return m
}

var defaultStates = []struct {
	Name, Color, Group string
	Default            bool
	Seq                float64
}{
	{"Backlog", "#A3A3A3", "backlog", true, 15000},
	{"Todo", "#3F76FF", "unstarted", false, 25000},
	{"In Progress", "#F59E0B", "started", false, 35000},
	{"Done", "#16A34A", "completed", false, 45000},
	{"Cancelled", "#EF4444", "cancelled", false, 55000},
}

// SeedDefaults creates the five default states for a freshly-created project.
func SeedDefaults(ctx context.Context, q *gen.Queries, wsID, projID uuid.UUID) error {
	for _, d := range defaultStates {
		if _, err := q.CreateState(ctx, gen.CreateStateParams{
			WorkspaceID: wsID, ProjectID: projID, Name: d.Name, Color: d.Color,
			GroupName: d.Group, IsDefault: d.Default, Description: "", Sequence: d.Seq,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || strings.TrimSpace(body.Color) == "" {
		httpx.Error(w, http.StatusBadRequest, "Name and color are required")
		return
	}
	if body.Group == "triage" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"non_field_errors": {"Cannot create triage state"}})
		return
	}
	if exists, _ := h.q.StateNameExists(ctx, gen.StateNameExistsParams{ProjectID: pid, Lower: name}); exists {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"name": "The state name is already taken"})
		return
	}
	group := body.Group
	if group == "" {
		group = "backlog"
	}
	s, err := h.q.CreateState(ctx, gen.CreateStateParams{
		WorkspaceID: ws.ID, ProjectID: pid, Name: name, Color: body.Color,
		GroupName: group, IsDefault: false, Description: "", Sequence: 65535,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusOK, stateResp(s)) // quirk: 200, not 201
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	states, err := h.q.ListStates(ctx, pid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	if r.URL.Query().Get("grouped") == "true" {
		groups := map[string][]map[string]any{}
		for _, s := range states {
			groups[s.GroupName] = append(groups[s.GroupName], stateListItem(s))
		}
		httpx.JSON(w, http.StatusOK, groups)
		return
	}
	out := make([]map[string]any, 0, len(states))
	for _, s := range states {
		out = append(out, stateListItem(s))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	s, err := h.state(ctx, w, pid, chi.URLParam(r, "state_id"))
	if !err {
		return
	}
	httpx.JSON(w, http.StatusOK, stateResp(s))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	s, found := h.state(ctx, w, pid, chi.URLParam(r, "state_id"))
	if !found {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Color       *string `json:"color"`
		Description *string `json:"description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name, color, desc := s.Name, s.Color, s.Description
	if body.Name != nil {
		name = *body.Name
	}
	if body.Color != nil {
		color = *body.Color
	}
	if body.Description != nil {
		desc = *body.Description
	}
	updated, err := h.q.UpdateState(ctx, gen.UpdateStateParams{ID: s.ID, ProjectID: pid, Name: name, Color: color, Description: desc})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, stateResp(updated))
}

func (h *Handler) markDefault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	sid, err := uuid.Parse(chi.URLParam(r, "state_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.ClearDefaultStates(ctx, pid)
	_ = h.q.SetDefaultState(ctx, gen.SetDefaultStateParams{ID: sid, ProjectID: pid})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.resolve(ctx, w, r)
	if !ok {
		return
	}
	s, found := h.state(ctx, w, pid, chi.URLParam(r, "state_id"))
	if !found {
		return
	}
	if s.IsDefault {
		httpx.Error(w, http.StatusBadRequest, "Default state cannot be deleted")
		return
	}
	if n, _ := h.q.CountIssuesByState(ctx, dbx.PgUUID(s.ID)); n > 0 {
		httpx.Error(w, http.StatusBadRequest, "The state is not empty, only empty states can be deleted")
		return
	}
	if err := h.q.DeleteState(ctx, gen.DeleteStateParams{ID: s.ID, ProjectID: pid}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) workspaceStates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	states, err := h.q.ListWorkspaceStates(ctx, ws.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(states))
	for _, s := range states {
		out = append(out, stateListItem(s))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- helpers ---------------------------------------------------------------

func (h *Handler) resolve(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.Workspace, uuid.UUID, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, uuid.UUID{}, false
	}
	pid, err := uuid.Parse(chi.URLParam(r, "project_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, uuid.UUID{}, false
	}
	return ws, pid, true
}

func (h *Handler) state(ctx context.Context, w http.ResponseWriter, pid uuid.UUID, idStr string) (gen.State, bool) {
	sid, err := uuid.Parse(idStr)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.State{}, false
	}
	s, err := h.q.GetState(ctx, gen.GetStateParams{ID: sid, ProjectID: pid})
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.State{}, false
	}
	return s, true
}

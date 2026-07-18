// Package wsextra serves assorted workspace-scoped endpoints the frontend polls:
// notifications (list + unread count) and user favorites (CRUD).
package wsextra

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{slug}/users/notifications/", h.notifications)
	r.Get("/workspaces/{slug}/users/notifications/unread/", h.unread)
	r.Get("/workspaces/{slug}/user-favorites/", h.listFavorites)
	r.Post("/workspaces/{slug}/user-favorites/", h.createFavorite)
	r.Delete("/workspaces/{slug}/user-favorites/{favorite_id}/", h.deleteFavorite)
	// home-page reads
	r.Get("/workspaces/{slug}/recent-visits/", h.emptyList)
	r.Get("/workspaces/{slug}/quick-links/", h.emptyList)
	r.Get("/workspaces/{slug}/home-preferences/", h.homePreferences)
	r.Get("/workspaces/{slug}/stickies/", h.stickies)
	// misc boot reads
	r.Get("/workspaces/{slug}/sidebar-preferences/", h.sidebarPreferences)
	r.Get("/workspaces/{slug}/user-properties/", h.workspaceUserProps)
	r.Get("/workspaces/{slug}/estimates/", h.emptyList)
	r.Get("/workspaces/{slug}/users/notifications", h.notificationsPaginated)
	r.Get("/timezones/", h.timezones)
}

func (h *Handler) sidebarPreferences(w http.ResponseWriter, _ *http.Request) {
	pref := func(pinned bool, order float64) map[string]any {
		return map[string]any{"is_pinned": pinned, "sort_order": httpx.Float(order)}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"your_work":     pref(false, 55535),
		"views":         pref(false, 65535),
		"active_cycles": pref(false, 75535),
		"analytics":     pref(false, 85535),
		"drafts":        pref(true, 95535),
		"projects":      pref(true, 45535),
	})
}

func (h *Handler) workspaceUserProps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	u, _ := auth.UserFrom(ctx)
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("wsprops:"+ws.ID.String()+":"+u.ID.String()))
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":                 id.String(),
		"workspace":          ws.ID.String(),
		"user":               u.ID.String(),
		"deleted_at":         nil,
		"filters":            map[string]any{},
		"display_filters":    map[string]any{},
		"display_properties": map[string]any{},
	})
}

func (h *Handler) notificationsPaginated(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"grouped_by": nil, "sub_grouped_by": nil, "total_count": 0,
		"next_cursor": "300:1:0", "prev_cursor": "300:-1:1",
		"next_page_results": false, "prev_page_results": false,
		"count": 0, "total_pages": 0, "total_results": 0,
		"extra_stats": nil, "results": []any{},
	})
}

func (h *Handler) timezones(w http.ResponseWriter, _ *http.Request) {
	tz := func(utc, val, label string) map[string]any {
		return map[string]any{"utc_offset": "UTC" + utc, "gmt_offset": "GMT" + utc, "value": val, "label": label}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"timezones": []map[string]any{
		tz("-08:00", "America/Los_Angeles", "Pacific Time"),
		tz("-07:00", "America/Denver", "Mountain Time"),
		tz("-06:00", "America/Chicago", "Central Time"),
		tz("-05:00", "America/New_York", "Eastern Time"),
		tz("-05:00", "America/Toronto", "Toronto"),
		tz("+00:00", "Etc/UTC", "UTC"),
		tz("+00:00", "Europe/London", "London"),
		tz("+01:00", "Europe/Berlin", "Berlin"),
		tz("+05:30", "Asia/Kolkata", "India"),
		tz("+08:00", "Asia/Shanghai", "Shanghai"),
		tz("+08:00", "Asia/Singapore", "Singapore"),
		tz("+09:00", "Asia/Tokyo", "Tokyo"),
		tz("+10:00", "Australia/Sydney", "Sydney"),
	}})
}

func (h *Handler) emptyList(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, []any{})
}

func (h *Handler) homePreferences(w http.ResponseWriter, _ *http.Request) {
	pref := func(key string, order float64) map[string]any {
		return map[string]any{"key": key, "is_enabled": true, "config": map[string]any{}, "sort_order": httpx.Float(order)}
	}
	httpx.JSON(w, http.StatusOK, []map[string]any{
		pref("my_stickies", 997), pref("recents", 998), pref("quick_links", 999),
	})
}

func (h *Handler) stickies(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"grouped_by": nil, "sub_grouped_by": nil, "total_count": 0,
		"next_cursor": "20:1:0", "prev_cursor": "20:-1:1",
		"next_page_results": false, "prev_page_results": false,
		"count": 0, "total_pages": 0, "total_results": 0,
		"extra_stats": nil, "results": []any{},
	})
}

func (h *Handler) notifications(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, []any{})
}

func (h *Handler) unread(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"total_unread_notifications_count":   0,
		"mention_unread_notifications_count": 0,
	})
}

func favResp(f gen.UserFavorite) map[string]any {
	return map[string]any{
		"id":                f.ID.String(),
		"entity_type":       f.EntityType,
		"entity_identifier": dbx.StrOrEmpty(f.EntityIdentifier),
		"entity_data":       map[string]any{},
		"is_folder":         f.IsFolder,
		"name":              f.Name,
		"parent":            dbx.StrPtr(f.Parent),
		"project_id":        dbx.StrPtr(f.ProjectID),
		"sequence":          httpx.Float(f.Sequence),
		"workspace_id":      f.WorkspaceID.String(),
	}
}

func (h *Handler) listFavorites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	rows, err := h.q.ListFavorites(ctx, gen.ListFavoritesParams{WorkspaceID: ws.ID, UserID: u.ID})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, favResp(f))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) createFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, ok := h.workspace(ctx, w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		EntityType       string `json:"entity_type"`
		EntityIdentifier string `json:"entity_identifier"`
		Name             string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.EntityType == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"entity_type": {"This field is required."}})
		return
	}
	ident := dbx.NullUUID()
	projectID := dbx.NullUUID()
	if id, err := uuid.Parse(body.EntityIdentifier); err == nil {
		ident = dbx.PgUUID(id)
		if body.EntityType == "project" {
			projectID = ident
		}
	}
	f, err := h.q.CreateFavorite(ctx, gen.CreateFavoriteParams{
		WorkspaceID: ws.ID, UserID: u.ID, EntityType: body.EntityType,
		EntityIdentifier: ident, Name: body.Name, ProjectID: projectID,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusOK, favResp(f)) // quirk: 200, not 201
}

func (h *Handler) deleteFavorite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := h.workspace(ctx, w, r); !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	fid, err := uuid.Parse(chi.URLParam(r, "favorite_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteFavorite(ctx, gen.DeleteFavoriteParams{ID: fid, UserID: u.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) workspace(ctx context.Context, w http.ResponseWriter, r *http.Request) (gen.Workspace, bool) {
	ws, err := h.q.GetWorkspaceBySlug(ctx, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return gen.Workspace{}, false
	}
	return ws, true
}

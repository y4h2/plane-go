// Package user serves the current-user endpoints (/api/users/me/*): the user
// object, settings (default-workspace pointers), and profile (preferences).
// Names and profile fields are updatable (the onboarding flow writes them).
package user

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/httpx"
)

type Handler struct{ q *gen.Queries }

func New(q *gen.Queries) *Handler { return &Handler{q: q} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/users/me/", h.me)
	r.Patch("/users/me/", h.updateMe)
	r.Get("/users/me/settings/", h.settings)
	r.Get("/users/me/profile/", h.profile)
	r.Patch("/users/me/profile/", h.updateProfile)
	r.Patch("/users/me/onboard/", h.onboard)
	r.Patch("/users/me/tour-completed/", h.tourCompleted)
}

// tourCompleted persists the product-tour dismissal ("No thanks") so the welcome
// modal doesn't reappear.
func (h *Handler) tourCompleted(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	_, _ = h.q.MergeUserProfile(ctx, gen.MergeUserProfileParams{ID: u.ID, Column2: []byte(`{"is_tour_completed":true}`)})
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Updated successfully"})
}

// onboard marks onboarding progress by merging the payload (e.g.
// {"is_onboarded": true}) into the user's profile blob.
func (h *Handler) onboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	patch, _ := io.ReadAll(r.Body)
	if len(patch) == 0 || string(patch) == "null" {
		patch = []byte(`{"is_onboarded":true}`)
	}
	if _, err := h.q.MergeUserProfile(ctx, gen.MergeUserProfileParams{ID: u.ID, Column2: patch}); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Updated successfully"})
}

type meResponse struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	FirstName   string     `json:"first_name"`
	LastName    string     `json:"last_name"`
	Avatar      string     `json:"avatar"`
	IsActive    bool       `json:"is_active"`
	IsBot       bool       `json:"is_bot"`
	DateJoined  time.Time  `json:"date_joined"`
	LastLogin   *time.Time `json:"last_login"`
}

func meResp(u gen.User) meResponse {
	return meResponse{
		ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName,
		FirstName: u.FirstName, LastName: u.LastName, Avatar: u.Avatar,
		IsActive: u.IsActive, IsBot: u.IsBot, DateJoined: u.DateJoined, LastLogin: u.LastLogin,
	}
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	httpx.JSON(w, http.StatusOK, meResp(u))
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	var body struct {
		FirstName   *string `json:"first_name"`
		LastName    *string `json:"last_name"`
		DisplayName *string `json:"display_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fn, ln, dn := u.FirstName, u.LastName, u.DisplayName
	if body.FirstName != nil {
		fn = *body.FirstName
	}
	if body.LastName != nil {
		ln = *body.LastName
	}
	if body.DisplayName != nil {
		dn = *body.DisplayName
	}
	updated, err := h.q.UpdateUserNames(ctx, gen.UpdateUserNamesParams{ID: u.ID, FirstName: fn, LastName: ln, DisplayName: dn})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, meResp(updated))
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	var fallbackID, fallbackSlug *string
	if ws, err := h.q.UserFallbackWorkspace(ctx, u.ID); err == nil {
		id, slug := ws.ID.String(), ws.Slug
		fallbackID, fallbackSlug = &id, &slug
	}
	invites, _ := h.q.CountPendingInvites(ctx, u.Email)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":    u.ID.String(),
		"email": u.Email,
		"workspace": map[string]any{
			"last_workspace_id":       nil,
			"last_workspace_slug":     nil,
			"fallback_workspace_id":   fallbackID,
			"fallback_workspace_slug": fallbackSlug,
			"invites":                 int(invites),
		},
	})
}

// profileResponse builds the profile from sensible defaults overlaid with the
// user's stored profile blob (which the onboarding flow writes via PATCH).
func profileResponse(u gen.User) map[string]any {
	m := map[string]any{
		"id":                     u.ID.String(),
		"created_at":             u.CreatedAt,
		"updated_at":             u.UpdatedAt,
		"theme":                  map[string]any{},
		"is_app_rail_docked":     true,
		"is_tour_completed":      false,
		"onboarding_step":        map[string]any{"workspace_join": false, "profile_complete": false, "workspace_create": false, "workspace_invite": false},
		"use_case":               nil,
		"role":                   nil,
		"is_onboarded":           false,
		"last_workspace_id":      nil,
		"company_name":           "",
		"notification_view_mode": "full",
		"language":               "en",
		"start_of_the_week":      0,
		"goals":                  map[string]any{},
		"background_color":       "#6c5ce7",
	}
	if len(u.Profile) > 0 {
		var stored map[string]any
		if json.Unmarshal(u.Profile, &stored) == nil {
			for k, v := range stored {
				m[k] = v
			}
		}
	}
	return m
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	httpx.JSON(w, http.StatusOK, profileResponse(u))
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	patch, _ := io.ReadAll(r.Body)
	if len(patch) == 0 || string(patch) == "null" {
		patch = []byte("{}")
	}
	if _, err := h.q.MergeUserProfile(ctx, gen.MergeUserProfileParams{ID: u.ID, Column2: patch}); err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	fresh, err := h.q.GetUserByID(ctx, u.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	httpx.JSON(w, http.StatusOK, profileResponse(fresh))
}

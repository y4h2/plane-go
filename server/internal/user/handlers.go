// Package user serves the current-user endpoints (/api/users/me/*): the user
// object, settings (default-workspace pointers), and profile (preferences).
package user

import (
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
	r.Get("/users/me/settings/", h.settings)
	r.Get("/users/me/profile/", h.profile)
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

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	httpx.JSON(w, http.StatusOK, meResponse{
		ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName,
		FirstName: u.FirstName, LastName: u.LastName, Avatar: u.Avatar,
		IsActive: u.IsActive, IsBot: u.IsBot, DateJoined: u.DateJoined, LastLogin: u.LastLogin,
	})
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

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":                  u.ID.String(),
		"created_at":          u.CreatedAt,
		"updated_at":          u.UpdatedAt,
		"theme":               map[string]any{},
		"is_app_rail_docked":  true,
		"is_tour_completed":   false,
		"onboarding_step":     map[string]any{"workspace_join": false, "profile_complete": false, "workspace_create": false, "workspace_invite": false},
		"use_case":            nil,
		"role":                nil,
		"is_onboarded":        false,
		"last_workspace_id":   nil,
		"company_name":        "",
		"notification_view_mode": "full",
		"language":            "en",
		"start_of_the_week":   0,
		"goals":               map[string]any{},
		"background_color":    "#6c5ce7",
	})
}

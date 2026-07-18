// Package instance serves GET /api/instances/ — the instance config the frontend
// reads on boot. Values are static/self-managed defaults (no admin panel yet).
package instance

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"planego/internal/httpx"
)

type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/instances/", h.get)
}

func (h *Handler) get(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	httpx.JSON(w, http.StatusOK, map[string]any{
		"config": map[string]any{
			"enable_signup":                  true,
			"is_workspace_creation_disabled": false,
			"is_google_enabled":              false,
			"is_github_enabled":              false,
			"is_gitlab_enabled":              false,
			"is_gitea_enabled":               false,
			"is_magic_login_enabled":         false,
			"is_email_password_enabled":      true,
			"github_app_name":                "",
			"slack_client_id":                nil,
			"posthog_api_key":                nil,
			"posthog_host":                   nil,
			"has_unsplash_configured":        false,
			"has_llm_configured":             false,
			"file_size_limit":                httpx.Float(5242880),
			"is_smtp_configured":             false,
			"admin_base_url":                 "http://localhost:3001",
			"space_base_url":                 "http://localhost:3002",
			"app_base_url":                   "http://localhost:3000",
			"is_self_managed":                true,
		},
		"instance": map[string]any{
			"id":              "00000000-0000-0000-0000-000000000001",
			"created_at":      now,
			"updated_at":      now,
			"instance_name":   "Plane (Go)",
			"current_version": "1.0.0",
			"latest_version":  "1.0.0",
			"is_setup_done":   true,
			"is_signup_screen_visited": true,
			"is_telemetry_enabled":     false,
			"is_support_required":      false,
			"workspaces_exist":         true,
			"is_activated":             true,
		},
	})
}

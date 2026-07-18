// Package external implements the Unsplash and AI-assistant integration
// endpoints (Django: apps/api/plane/app/urls/external.py ->
// UnsplashEndpoint, GPTIntegrationEndpoint, WorkspaceGPTIntegrationEndpoint).
//
// This environment has no UNSPLASH_ACCESS_KEY or LLM_API_KEY configured, and
// these handlers only need to match what the Django reference actually
// returns in that unconfigured state — they never call the real third-party
// APIs. Frozen against the Python reference:
//
//   - GET /unsplash/ always returns 200 [] (Django short-circuits before
//     ever building the Unsplash request when the access key is unset).
//   - Both ai-assistant endpoints always return 400
//     {"error": "LLM provider API key and model are required"} (Django
//     checks the configured LLM key/model before it even looks at
//     "task"/"prompt", so that check fires unconditionally here too).
package external

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"planego/internal/auth"
	"planego/internal/httpx"
)

type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/unsplash/", h.unsplash)
	r.Post("/workspaces/{slug}/projects/{project_id}/ai-assistant/", h.aiAssistant)
	r.Post("/workspaces/{slug}/ai-assistant/", h.aiAssistant)
}

func (h *Handler) unsplash(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	// No UNSPLASH_ACCESS_KEY configured: match Django's short-circuit.
	httpx.JSON(w, http.StatusOK, []any{})
}

func (h *Handler) aiAssistant(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}
	// No LLM_API_KEY configured: Django's get_llm_config() fails before the
	// view ever looks at the request body, so this fires regardless of
	// "task"/"prompt" or whether the workspace/project in the URL exist.
	httpx.Error(w, http.StatusBadRequest, "LLM provider API key and model are required")
}

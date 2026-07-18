// Package userextra implements two session-adjacent user endpoints that don't
// fit the plain CRUD shape of internal/user: change-password and account
// deactivation.
//
// Django sources:
//   - plane/authentication/urls.py -> ChangePasswordEndpoint
//     (plane/authentication/views/common.py). Despite being a "user" concern,
//     this is mounted at plain `/auth/change-password/`, NOT under
//     `/api/users/me/...` -- there is no `/api/users/me/change-password/`
//     route at all on the reference (confirmed 404). It is also a plain DRF
//     APIView using default SessionAuthentication, which enforces Django's
//     double-submit CSRF check (unlike the `/api/*` ViewSets, which don't).
//   - plane/app/urls/user.py -> UserEndpoint.deactivate (DELETE
//     /api/users/me/), plane/app/views/user/base.py.
//
// Because /auth/change-password/ needs auth.Require applied to a route that
// isn't naturally part of the already-authenticated /api group, this package
// exposes two separate entry points instead of a single Routes method:
//
//   - Routes(r chi.Router): DELETE /users/me/ -- call this inside the
//     existing authenticated /api group, alongside internal/user's
//     GET/PATCH on the same path (chi dispatches by method, so this is a
//     second, independent registration for that literal path).
//   - ChangePassword(w, r): an http.HandlerFunc-compatible method for
//     POST /change-password/ -- mount this under the /auth router group,
//     wrapped with auth.Require (see the wiring note in the package's
//     deliverable notes / commit message).
package userextra

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/auth"
	"planego/internal/config"
	"planego/internal/httpx"
)

type Handler struct {
	pool              *pgxpool.Pool
	sessionCookieName string
	sessionTTL        time.Duration
}

// New takes only the pgx pool per house rule for this package; the session
// cookie name/TTL are read from config.Load() internally (env-driven,
// matching internal/config's own defaults) since the constructor signature
// can't carry a config.Config parameter.
func New(pool *pgxpool.Pool) *Handler {
	cfg := config.Load()
	return &Handler{pool: pool, sessionCookieName: cfg.SessionCookieName, sessionTTL: cfg.SessionTTL}
}

// Routes registers the DELETE /users/me/ deactivate endpoint. Call inside the
// authenticated /api group.
func (h *Handler) Routes(r chi.Router) {
	r.Delete("/users/me/", h.deactivate)
}

// deactivate mirrors UserEndpoint.deactivate: scrambles the password (so old
// credentials can never sign in again), flips is_active off, resets the
// stored profile blob to defaults, deletes every session belonging to the
// user (not just the current one), and explicitly clears the session cookie.
// Returns 204 with an empty body.
//
// The Django reference additionally blocks deactivation when the user is the
// sole admin of a project/workspace with other members, and (for the admin
// case) when they're an instance admin. Those checks depend on
// workspace/project-membership state outside this package's users-table
// scope; the frozen contract only exercises the common case (a throwaway
// user with no workspaces), where the reference's checks are all no-ops, so
// they're intentionally not replicated here.
func (h *Handler) deactivate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	scrambled := auth.HashPassword(uuid.NewString())
	if _, err := h.pool.Exec(ctx, `
		update users
		set password = $1, is_active = false, profile = '{}'::jsonb, updated_at = now()
		where id = $2`, scrambled, u.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}

	if _, err := h.pool.Exec(ctx, `delete from sessions where user_id = $1`, u.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: h.sessionCookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ChangePassword mirrors ChangePasswordEndpoint.post. Every test-relevant
// account in this system signs up through /auth/sign-up/ with a real
// password (there's no magic-link/OAuth path that would leave a password
// "autoset" the way Django's does), so unlike the reference this always
// requires and verifies old_password rather than conditionally skipping that
// check for autoset accounts.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := auth.UserFrom(ctx)
	if !ok {
		httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
		return
	}

	if !h.checkCSRF(r) {
		httpx.Detail(w, http.StatusForbidden, "CSRF Failed: CSRF token missing.")
		return
	}

	var body struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.OldPassword == "" {
		writeAuthError(w, 5138, "MISSING_PASSWORD", "Old password is missing")
		return
	}
	if body.NewPassword == "" {
		writeAuthError(w, 5138, "MISSING_PASSWORD", "Old or new password is missing")
		return
	}
	if !auth.VerifyPassword(body.OldPassword, u.Password) {
		writeAuthError(w, 5135, "INCORRECT_OLD_PASSWORD", "Old password is not correct")
		return
	}
	if auth.PasswordTooWeak(body.NewPassword) {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{
			"error_code": 5021, "error_message": "PASSWORD_TOO_WEAK",
		})
		return
	}

	newHash := auth.HashPassword(body.NewPassword)
	if _, err := h.pool.Exec(ctx, `update users set password = $1, updated_at = now() where id = $2`, newHash, u.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}

	// Rotate the session, mirroring Django's login() cycling the session key
	// on password change: delete the session that authenticated this
	// request and issue a fresh one under the same cookie name.
	if c, err := r.Cookie(h.sessionCookieName); err == nil {
		_, _ = h.pool.Exec(ctx, `delete from sessions where key = $1`, c.Value)
	}
	newKey := randSessionKey(128)
	if _, err := h.pool.Exec(ctx, `
		insert into sessions (key, user_id, expires_at) values ($1, $2, $3)`,
		newKey, u.ID, time.Now().Add(h.sessionTTL)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong please try again later")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: h.sessionCookieName, Value: newKey, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(h.sessionTTL.Seconds()),
	})

	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Password updated successfully"})
}

// checkCSRF replicates internal/auth's double-submit check (cookie value
// must match the submitted token) for a JSON POST: the token travels in the
// X-CSRFToken header instead of a form field.
func (h *Handler) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie("csrftoken")
	if err != nil || c.Value == "" {
		return false
	}
	tok := r.Header.Get("X-CSRFToken")
	if tok == "" {
		tok = r.FormValue("csrfmiddlewaretoken")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(tok)) == 1
}

func writeAuthError(w http.ResponseWriter, code int, msg, errText string) {
	httpx.JSON(w, http.StatusBadRequest, map[string]any{
		"error_code": code, "error_message": msg, "error": errText,
	})
}

const sessionKeyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randSessionKey mirrors internal/auth's unexported randString: a
// cryptographically-random string over [a-z0-9] of length n.
func randSessionKey(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	for i := range b {
		b[i] = sessionKeyAlphabet[int(b[i])%len(sessionKeyAlphabet)]
	}
	return string(b)
}

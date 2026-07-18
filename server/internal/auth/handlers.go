// Package auth implements the /auth/* session flow the Plane frontend speaks:
// form-urlencoded sign-up/in/out that 302-redirect and set a `session-id` cookie,
// plus a CSRF-token endpoint. It mirrors the reference's observable behavior, not
// its Django internals.
package auth

import (
	"crypto/subtle"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"

	"planego/internal/config"
	"planego/internal/db/gen"
	"planego/internal/httpx"
)

type Auth struct {
	q   *gen.Queries
	cfg config.Config
}

func New(q *gen.Queries, cfg config.Config) *Auth { return &Auth{q: q, cfg: cfg} }

func (a *Auth) Routes(r chi.Router) {
	r.Get("/get-csrf-token/", a.csrf)
	r.Post("/sign-up/", a.signUp)
	r.Post("/sign-in/", a.signIn)
	r.Post("/sign-out/", a.signOut)
}

func (a *Auth) csrf(w http.ResponseWriter, r *http.Request) {
	token := randString(32)
	http.SetCookie(w, &http.Cookie{
		Name: "csrftoken", Value: token, Path: "/",
		SameSite: http.SameSiteLaxMode, MaxAge: int(a.cfg.SessionTTL.Seconds()),
	})
	httpx.JSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}

// checkCSRF does Django-style double-submit: the form field (or X-CSRFTOKEN
// header) must match the csrftoken cookie.
func (a *Auth) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie("csrftoken")
	if err != nil {
		return false
	}
	tok := r.FormValue("csrfmiddlewaretoken")
	if tok == "" {
		tok = r.Header.Get("X-CSRFTOKEN")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(tok)) == 1
}

func (a *Auth) signUp(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if !a.checkCSRF(r) {
		a.redirectErr(w, r, "CSRF_FAILED")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	if email == "" || pw == "" {
		a.redirectErr(w, r, "REQUIRED_EMAIL_PASSWORD_SIGN_UP")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		a.redirectErr(w, r, "INVALID_EMAIL")
		return
	}
	if PasswordTooWeak(pw) {
		a.redirectErr(w, r, "PASSWORD_TOO_WEAK")
		return
	}
	ctx := r.Context()
	if _, err := a.q.GetUserByEmail(ctx, email); err == nil {
		a.redirectErr(w, r, "USER_ALREADY_EXIST")
		return
	}
	u, err := a.q.CreateUser(ctx, gen.CreateUserParams{
		Email:       email,
		Password:    HashPassword(pw),
		DisplayName: displayName(email),
	})
	if err != nil {
		a.redirectErr(w, r, "SIGNUP_FAILED")
		return
	}
	if err := a.startSession(ctx, w, u.ID); err != nil {
		a.redirectErr(w, r, "SIGNUP_FAILED")
		return
	}
	a.redirectOK(w, r)
}

func (a *Auth) signIn(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if !a.checkCSRF(r) {
		a.redirectErr(w, r, "CSRF_FAILED")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	if email == "" || pw == "" {
		a.redirectErr(w, r, "REQUIRED_EMAIL_PASSWORD_SIGN_IN")
		return
	}
	ctx := r.Context()
	u, err := a.q.GetUserByEmail(ctx, email)
	if err != nil {
		a.redirectErr(w, r, "USER_DOES_NOT_EXIST")
		return
	}
	if !VerifyPassword(pw, u.Password) {
		a.redirectErr(w, r, "AUTHENTICATION_FAILED")
		return
	}
	_ = a.q.TouchLastLogin(ctx, u.ID)
	if err := a.startSession(ctx, w, u.ID); err != nil {
		a.redirectErr(w, r, "SIGNIN_FAILED")
		return
	}
	a.redirectOK(w, r)
}

func (a *Auth) signOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.cfg.SessionCookieName); err == nil {
		_ = a.q.DeleteSession(r.Context(), c.Value)
		a.clearSessionCookie(w)
	}
	a.redirectOK(w, r)
}

// redirectOK / redirectErr mirror the reference: a 302 to the web app, with an
// error_code query param on failure (the frontend reads it to show a message).
func (a *Auth) redirectOK(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, a.cfg.WebURL+"/", http.StatusFound)
}

func (a *Auth) redirectErr(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, a.cfg.WebURL+"/?error_code="+code+"&error_message="+code, http.StatusFound)
}

func displayName(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}

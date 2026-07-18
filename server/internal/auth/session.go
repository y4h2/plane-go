package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"planego/internal/db/gen"
)

// startSession creates a DB-backed session row and sets the session-id cookie.
func (a *Auth) startSession(ctx context.Context, w http.ResponseWriter, uid uuid.UUID) error {
	key := randString(128)
	if _, err := a.q.CreateSession(ctx, gen.CreateSessionParams{
		Key:       key,
		UserID:    uid,
		ExpiresAt: time.Now().Add(a.cfg.SessionTTL),
	}); err != nil {
		return err
	}
	a.setSessionCookie(w, key)
	return nil
}

func (a *Auth) setSessionCookie(w http.ResponseWriter, key string) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cfg.SessionCookieName,
		Value:    key,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.cfg.SessionTTL.Seconds()),
		// Secure intentionally false: local dev serves over http, and the
		// frontend relies on the cookie being sent on same-origin http requests.
	})
}

func (a *Auth) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cfg.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

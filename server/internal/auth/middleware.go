package auth

import (
	"context"
	"net/http"

	"planego/internal/db/gen"
	"planego/internal/httpx"
)

type ctxKey int

const userCtxKey ctxKey = iota

// Require authenticates via the session-id cookie and injects the user into the
// request context. Unauthenticated requests get 401 (the frontend redirects to
// login on 401).
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(a.cfg.SessionCookieName)
		if err != nil {
			httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
			return
		}
		u, err := a.q.GetSessionUser(r.Context(), c.Value)
		if err != nil {
			httpx.Detail(w, http.StatusUnauthorized, "Authentication credentials were not provided.")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFrom returns the authenticated user placed in the context by Require.
func UserFrom(ctx context.Context) (gen.User, bool) {
	u, ok := ctx.Value(userCtxKey).(gen.User)
	return u, ok
}

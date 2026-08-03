// Package middleware holds the HTTP middleware the server wraps routes in.
package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

// Middleware is the shape every middleware in this package returns, so routes
// can be composed with chain().
type Middleware func(http.Handler) http.Handler

// RequireAdmin rejects requests without a valid admin session.
//
// A browser navigating to a protected page is redirected to the login form. An
// htmx request is not: swapping a login page into a fragment of the current
// page would produce a broken hybrid, so it gets a 401 and a header telling
// htmx to reload the page properly.
func RequireAdmin(sessions *auth.Sessions, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(auth.CookieName)
			if err == nil {
				if _, err := sessions.Verify(cookie.Value, time.Now()); err == nil {
					next.ServeHTTP(w, r)
					return
				} else if !errors.Is(err, auth.ErrExpired) {
					// Expiry is routine; anything else means the cookie was
					// forged, replayed from another deployment, or mangled.
					log.Warn("rejected admin session", "path", r.URL.Path, "error", err)
				}
			}

			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Refresh", "true")
				http.Error(w, "unauthorised", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		})
	}
}

// Chain wraps h in every middleware, applied in reverse so that call sites read
// outermost-first: Chain(h, a, b) serves a(b(h)).
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for _, m := range slices.Backward(mw) {
		h = m(h)
	}
	return h
}

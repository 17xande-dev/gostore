package middleware

import (
	"net/http"
	"slices"
	"strings"
)

// Policy is the parts of the Content-Security-Policy that depend on how this
// particular store is deployed. Everything else in the policy is fixed, because
// the templates earn it.
type Policy struct {
	// FrameAncestors are the origins allowed to frame the store — where
	// embedding is permitted to happen. Empty means 'none'.
	FrameAncestors []string

	// FormActions are the external origins a form may post to, beyond the store
	// itself. In practice this is the payment gateway: the checkout hands the
	// shopper to it with a real form submission, and form-action 'self' alone
	// makes the browser block that — silently enough to cost an afternoon.
	FormActions []string
}

// SecurityHeaders sets the response headers every page wants.
//
// The Content-Security-Policy is strict because the templates earn it: no inline
// script, no external origins, htmx served from the binary. Adopters who add a
// CDN font or an analytics tag will need to widen it, which is the correct
// direction of travel — start closed and open deliberately.
func SecurityHeaders(p Policy) Middleware {
	frame := "'none'"
	if len(p.FrameAncestors) > 0 {
		frame = strings.Join(p.FrameAncestors, " ")
	}
	formAction := "'self'"
	if len(p.FormActions) > 0 {
		formAction += " " + strings.Join(p.FormActions, " ")
	}

	csp := strings.Join([]string{
		"default-src 'self'",
		// Product images are pasted URLs until object storage lands, so https
		// and data: are allowed for images and nothing else.
		"img-src 'self' https: data:",
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self'",
		"form-action " + formAction,
		"base-uri 'none'",
		"object-src 'none'",
		"frame-ancestors " + frame,
	}, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			next.ServeHTTP(w, r)
		})
	}
}

// CORS allows the listed origins to fetch a handler cross-origin.
//
// It belongs only on the read-only, cookie-free catalog routes. Nothing it
// guards may depend on a cookie or change state: no credentials are allowed, so
// a permissive origin list here cannot become a way to act as somebody.
func CORS(allowedOrigins []string) Middleware {
	allowAll := slices.Contains(allowedOrigins, "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case len(allowedOrigins) == 0 || origin == "":
				// No embedding configured, or a same-origin request: send no
				// CORS headers at all rather than an empty allowance.
			case allowAll:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			case slices.Contains(allowedOrigins, origin):
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// The response varies by Origin, so a shared cache must not
				// serve one embedder's copy to another.
				w.Header().Add("Vary", "Origin")
			default:
				w.Header().Add("Vary", "Origin")
			}

			// Preflights: only the htmx request header needs allowing, and only
			// GET is ever offered.
			if r.Method == http.MethodOptions && origin != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "HX-Request, HX-Current-URL, HX-Target, HX-Trigger")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

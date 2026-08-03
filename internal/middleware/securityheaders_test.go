package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(nil)(http.HandlerFunc(ok))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/products", nil))

	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'", // htmx is served from the binary, so no CDN needs allowing
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP is missing %q: %s", want, csp)
		}
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options is not set")
	}
	if w.Header().Get("Referrer-Policy") == "" {
		t.Error("Referrer-Policy is not set")
	}
}

func TestSecurityHeaders_FrameAncestorsFollowEmbedOrigins(t *testing.T) {
	// Embedding the catalog in someone else's page is the point of the feature,
	// so the origins allowed to fetch it must also be allowed to frame it.
	h := SecurityHeaders([]string{"https://cms.example", "https://other.example"})(http.HandlerFunc(ok))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/products", nil))

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors https://cms.example https://other.example") {
		t.Errorf("frame-ancestors does not list the embed origins: %s", csp)
	}
}

func TestCORS(t *testing.T) {
	allowed := "https://cms.example"
	h := CORS([]string{allowed})(http.HandlerFunc(ok))

	cases := []struct {
		name, origin, wantAllow string
		wantVary                bool
	}{
		{name: "no origin at all", origin: "", wantAllow: ""},
		{name: "allowed origin", origin: allowed, wantAllow: allowed, wantVary: true},
		{name: "unlisted origin", origin: "https://evil.example", wantAllow: "", wantVary: true},
		// A prefix of an allowed origin is a different origin.
		{name: "lookalike origin", origin: "https://cms.example.evil.test", wantAllow: "", wantVary: true},
		{name: "scheme downgrade", origin: "http://cms.example", wantAllow: "", wantVary: true},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/products", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		h.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q", tc.name, got, tc.wantAllow)
		}
		if got := w.Header().Get("Vary") != ""; got != tc.wantVary {
			t.Errorf("%s: Vary present = %v, want %v", tc.name, got, tc.wantVary)
		}
		if w.Body.String() != "ok" {
			t.Errorf("%s: the handler did not run", tc.name)
		}
	}
}

func TestCORS_NeverAllowsCredentials(t *testing.T) {
	// The fragments are cookie-free by design. If this header ever appears, a
	// permissive origin list stops being safe.
	for _, origins := range [][]string{{"https://cms.example"}, {"*"}} {
		h := CORS(origins)(http.HandlerFunc(ok))

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/products", nil)
		req.Header.Set("Origin", "https://cms.example")
		h.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("origins %v: Access-Control-Allow-Credentials = %q, want it absent", origins, got)
		}
	}
}

func TestCORS_Wildcard(t *testing.T) {
	h := CORS([]string{"*"})(http.HandlerFunc(ok))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/products", nil)
	req.Header.Set("Origin", "https://anyone.example")
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestCORS_UnconfiguredSendsNothing(t *testing.T) {
	h := CORS(nil)(http.HandlerFunc(ok))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/products", nil)
	req.Header.Set("Origin", "https://cms.example")
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q with no origins configured", got)
	}
}

func TestCORS_Preflight(t *testing.T) {
	reached := false
	h := CORS([]string{"https://cms.example"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/products", nil)
	req.Header.Set("Origin", "https://cms.example")
	req.Header.Set("Access-Control-Request-Headers", "hx-request")
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	// htmx sends HX-Request, which makes the fetch non-simple; without this the
	// browser refuses before the request is ever made.
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "HX-Request") {
		t.Errorf("Access-Control-Allow-Headers = %q, want HX-Request", got)
	}
	if reached {
		t.Error("the preflight reached the wrapped handler")
	}
}

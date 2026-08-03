package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

var testSecret = bytes.Repeat([]byte("k"), auth.MinSecretLen)

func testSessions(t *testing.T, secret []byte) *auth.Sessions {
	t.Helper()
	s, err := auth.NewSessions(secret, nil, time.Hour)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	return s
}

// issue returns a session cookie value signed by secret, valid or already
// expired depending on ttl.
func issue(t *testing.T, secret []byte, ttl time.Duration) string {
	t.Helper()
	s, err := auth.NewSessions(secret, nil, ttl)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	value, err := s.Issue(time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return value
}

// protected returns a handler wrapped in RequireAdmin, plus a pointer to a flag
// that records whether the wrapped handler was ever reached.
func protected(t *testing.T) (http.Handler, *bool) {
	t.Helper()

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte("secret"))
	})
	return RequireAdmin(testSessions(t, testSecret), slog.New(slog.DiscardHandler))(next), &reached
}

func TestRequireAdmin_AllowsWithValidCookie(t *testing.T) {
	h, reached := protected(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/products", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: issue(t, testSecret, time.Hour)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !*reached {
		t.Error("the protected handler was not reached with a valid session")
	}
}

func TestRequireAdmin_RedirectsUnauthenticated(t *testing.T) {
	cases := map[string]*http.Cookie{
		"no cookie":        nil,
		"empty cookie":     {Name: auth.CookieName, Value: ""},
		"forged cookie":    {Name: auth.CookieName, Value: "bm93LmZha2U"},
		"other deployment": {Name: auth.CookieName, Value: issue(t, bytes.Repeat([]byte("j"), auth.MinSecretLen), time.Hour)},
		// Genuinely issued by this deployment, but its second has passed.
		"expired": {Name: auth.CookieName, Value: issue(t, testSecret, time.Nanosecond)},
	}

	for name, cookie := range cases {
		h, reached := protected(t)

		req := httptest.NewRequest(http.MethodGet, "/admin/products", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusSeeOther {
			t.Errorf("%s: status = %d, want 303", name, w.Code)
		}
		if got := w.Header().Get("Location"); got != "/admin/login" {
			t.Errorf("%s: Location = %q, want /admin/login", name, got)
		}
		if *reached {
			t.Errorf("%s: the protected handler ran anyway", name)
		}
	}
}

func TestRequireAdmin_HTMXGets401(t *testing.T) {
	h, reached := protected(t)

	// A fragment request must not be answered with a login page: htmx would
	// swap it into the middle of the current document.
	req := httptest.NewRequest(http.MethodPost, "/admin/products", nil)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("HX-Refresh"); got != "true" {
		t.Errorf("HX-Refresh = %q, want true", got)
	}
	if *reached {
		t.Error("the protected handler ran anyway")
	}
}

func TestChain_AppliesOutermostFirst(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	final := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") })

	Chain(final, mw("first"), mw("second")).ServeHTTP(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"first", "second", "handler"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

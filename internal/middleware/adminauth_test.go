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

// protected returns a handler wrapped in RequireAdmin, plus a pointer to a flag
// that records whether the wrapped handler was ever reached.
func protected(t *testing.T) (http.Handler, *bool) {
	t.Helper()

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte("secret"))
	})
	return RequireAdmin(testSecret, slog.New(slog.DiscardHandler))(next), &reached
}

func TestRequireAdmin_AllowsWithValidCookie(t *testing.T) {
	h, reached := protected(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/products", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.CookieName,
		Value: auth.IssueSession(testSecret, time.Hour, time.Now()),
	})
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
		"no cookie":     nil,
		"empty cookie":  {Name: auth.CookieName, Value: ""},
		"forged cookie": {Name: auth.CookieName, Value: "bm93" + ".ZmFrZQ"},
		"other deployment": {Name: auth.CookieName, Value: auth.IssueSession(
			bytes.Repeat([]byte("j"), auth.MinSecretLen), time.Hour, time.Now())},
		"expired": {Name: auth.CookieName, Value: auth.IssueSession(testSecret, -time.Minute, time.Now())},
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

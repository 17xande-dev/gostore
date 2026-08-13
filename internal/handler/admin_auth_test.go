package handler

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

func TestAdminAuth_RedirectsUnauthenticated(t *testing.T) {
	srv, _ := newServer(t)

	// Every protected route, not a sample of them: an unprotected route is the
	// kind of mistake that only shows up when someone finds it.
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/"},
		{http.MethodGet, "/admin/products"},
		{http.MethodGet, "/admin/products/new"},
		{http.MethodPost, "/admin/products"},
		{http.MethodGet, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/edit"},
		{http.MethodPost, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{http.MethodPost, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/delete"},
		{http.MethodPost, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants"},
		{http.MethodPost, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants/9f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{http.MethodPost, "/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants/9f2504e0-4f89-41d3-9a0c-0305e82c3301/delete"},
	}

	for _, tc := range paths {
		var res *http.Response
		if tc.method == http.MethodGet {
			res, _ = get(t, srv, tc.path)
		} else {
			res, _ = post(t, srv, tc.path, url.Values{"title": {"Sneaky"}})
		}
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("%s %s = %d, want 303", tc.method, tc.path, res.StatusCode)
			continue
		}
		if got := res.Header.Get("Location"); got != "/admin/login" {
			t.Errorf("%s %s redirected to %q, want /admin/login", tc.method, tc.path, got)
		}
	}
}

func TestAdminAuth_AllowsWithValidCookie(t *testing.T) {
	srv, _ := newServer(t)

	res, body := get(t, srv, "/admin/login")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/login = %d", res.StatusCode)
	}
	if !strings.Contains(body, `name="password"`) {
		t.Error("the login page has no password field")
	}

	res, body = post(t, srv, "/admin/login", url.Values{"password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != "/admin/products" {
		t.Errorf("Location = %q, want /admin/products", got)
	}

	cookie := sessionCookieFrom(t, res)
	if !cookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	// Scoped to /admin, so it is never sent with the cookie-free, embeddable
	// storefront fragments.
	if cookie.Path != "/admin" {
		t.Errorf("Path = %q, want /admin", cookie.Path)
	}
	if _, err := testSessions(t).Verify(cookie.Value, time.Now()); err != nil {
		t.Errorf("the issued cookie does not verify: %v", err)
	}

	// The jar now holds the session, so the protected pages open.
	if res, _ := get(t, srv, "/admin/products"); res.StatusCode != http.StatusOK {
		t.Errorf("GET /admin/products after signing in = %d, want 200", res.StatusCode)
	}
}

func TestAdminAuth_RejectsWrongPassword(t *testing.T) {
	srv, _ := newServer(t)

	for _, password := range []string{"", "wrong", testPassword + "x", strings.ToUpper(testPassword)} {
		res, body := post(t, srv, "/admin/login", url.Values{"password": {password}})
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("password %q = %d, want 401", password, res.StatusCode)
		}
		if !strings.Contains(body, "That password is not right.") {
			t.Errorf("password %q: no error message on the re-rendered form", password)
		}
		for _, c := range res.Cookies() {
			if c.Name == auth.CookieName && c.Value != "" {
				t.Errorf("password %q issued a session cookie", password)
			}
		}
	}

	// And a failed login leaves the admin closed.
	if res, _ := get(t, srv, "/admin/products"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /admin/products after failed logins = %d, want 303", res.StatusCode)
	}
}

func TestAdminAuth_LogoutClearsTheSession(t *testing.T) {
	srv, _ := setup(t)

	if res, _ := get(t, srv, "/admin/products"); res.StatusCode != http.StatusOK {
		t.Fatal("not signed in at the start of the test")
	}

	res, body := post(t, srv, "/admin/logout", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", got)
	}
	cookie := sessionCookieFrom(t, res)
	if cookie.Value != "" || cookie.MaxAge >= 0 {
		t.Errorf("logout cookie is %+v, want an empty value and a negative MaxAge", cookie)
	}

	if res, _ := get(t, srv, "/admin/products"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /admin/products after logout = %d, want 303", res.StatusCode)
	}
}

func TestAdminAuth_LoginPageSkippedWhenSignedIn(t *testing.T) {
	srv, _ := setup(t)

	res, _ := get(t, srv, "/admin/login")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /admin/login while signed in = %d, want 303", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/admin/products" {
		t.Errorf("Location = %q, want /admin/products", got)
	}
}

func TestAdminAuth_ExpiredSessionIsRejected(t *testing.T) {
	srv, _ := newServer(t)

	// A session that was genuinely issued, by this deployment's secret, but has
	// run out. The expiry is signed, so a client cannot extend it.
	cfg := testConfig()
	shortLived, err := auth.NewSessions(cfg.SessionSecret, nil, time.Nanosecond)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	value, err := shortLived.Issue(time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/admin/products", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: value})

	res, _ := do(t, srv, req)
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("expired session = %d, want 303", res.StatusCode)
	}
}

func sessionCookieFrom(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in the response", auth.CookieName)
	return nil
}

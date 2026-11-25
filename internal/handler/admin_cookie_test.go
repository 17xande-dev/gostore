package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/websec/webtest"
)

func TestAdminCookieAttributes(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%t", secure), func(t *testing.T) {
			const ttl = time.Hour
			origin, csrfName, sessionName := "http://example.test", "csrf_token", "admin_session"
			if secure {
				// The prefixes are literal on purpose: the browser enforces them, so
				// the test must not derive them from the code that picks them.
				origin, csrfName, sessionName = "https://example.test", "__Host-csrf_token", "__Secure-admin_session"
			}
			s := newStore(t, func(c *config.Config) {
				c.CookieSecure, c.SessionTTL, c.BaseURL = secure, ttl, origin
			})
			send := func(method, path string, form url.Values, cookies ...*http.Cookie) *webtest.Response {
				// Each request specifies its cookies, including the deliberate use
				// of the pre-login CSRF cookie and token on logout.
				c := webtest.New(t, s.srv.Config.Handler, origin, webtest.WithOrigin(origin))
				r := c.NewRequest(method, path, strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				for _, ck := range cookies {
					r.AddCookie(ck)
				}
				return c.Do(r)
			}
			cookie := func(r *webtest.Response, name, path string, session, cleared bool) *http.Cookie {
				t.Helper()
				return webtest.WantCookie(t, r, webtest.CookieWant{
					Name: name, Path: path, HttpOnly: true, SameSite: http.SameSiteLaxMode,
					Secure: secure, Session: session, Cleared: cleared,
				})
			}
			loginForm := send(http.MethodGet, "/admin/login", nil)
			if loginForm.Status != http.StatusOK {
				t.Fatalf("login form: status %d", loginForm.Status)
			}
			csrf := cookie(loginForm, csrfName, "/", true, false)
			if csrf.Value == "" {
				t.Error("CSRF cookie must carry a value and end with the browser session (no Max-Age, no Expires)")
			}
			before := time.Now()
			login := send(http.MethodPost, "/admin/login", url.Values{
				"email": {testEmail}, "password": {testPassword}, "csrf_token": {csrfFromResponse(t, loginForm)},
			}, csrf)
			if login.Status != http.StatusSeeOther {
				t.Fatalf("sign in: status %d", login.Status)
			}
			session := cookie(login, sessionName, "/admin", false, false)
			// Max-Age counts down from the stored expiry and is floored, so it is a
			// second or two under the TTL, never over it (the cookie must not outlive
			// the server's session).
			if session.Value == "" || session.MaxAge > int(ttl.Seconds()) || session.MaxAge < int(ttl.Seconds())-2 ||
				session.Expires.Before(before.Add(ttl).Truncate(time.Second)) || session.Expires.After(time.Now().Add(ttl)) {
				t.Error("session cookie must carry a value and expire at the configured TTL")
			}

			logout := send(http.MethodPost, "/admin/logout", url.Values{
				"csrf_token": {csrfFromResponse(t, loginForm)},
			}, csrf, session)
			if logout.Status != http.StatusSeeOther || logout.Location() != "/admin/login" {
				t.Fatalf("logout: status %d, Location %q", logout.Status, logout.Location())
			}
			cleared := cookie(logout, sessionName, "/admin", false, true)
			if cleared.Value != "" || !cleared.Expires.Before(time.Now()) {
				t.Error("logout must clear the cookie immediately with an empty value and negative MaxAge")
			}
		})
	}
}

func csrfFromResponse(t *testing.T, r *webtest.Response) string {
	t.Helper()
	return r.Page().Token()
}

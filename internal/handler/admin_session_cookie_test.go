package handler

import (
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/websec/webtest"
)

// secureShop is the production shape: https BASE_URL, so the prefixed cookie
// names a browser would enforce. Each in-process request carries the explicit
// cookies for that case so the jar cannot hide a stale or misnamed cookie.
type secureShop struct {
	*shop
	origin string
}

func newSecureShop(t *testing.T) secureShop {
	t.Helper()
	const origin = "https://store.example"
	s := newStore(t, func(c *config.Config) {
		c.CookieSecure, c.BaseURL, c.SessionTTL = true, origin, time.Hour
	})
	return secureShop{s, origin}
}

func (s secureShop) send(t *testing.T, method, path string, form url.Values, cookies ...*http.Cookie) *webtest.Response {
	t.Helper()
	c := webtest.New(t, s.srv.Config.Handler, s.origin, webtest.WithOrigin(s.origin))
	r := c.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, ck := range cookies {
		r.AddCookie(ck)
	}
	return c.Do(r)
}

func setCookie(t *testing.T, r *webtest.Response, name string) *http.Cookie {
	t.Helper()
	return r.SetCookie(name)
}

// The CSRF token is replaced at sign-in and at sign-out, and the one issued
// before is refused afterwards. A cookie planted before sign-in (cookie
// injection) would otherwise give its planter a valid token for the session.
func TestCSRFTokenIsRenewedAtSignInAndSignOut(t *testing.T) {
	s := newSecureShop(t)
	const csrfName, sessionName = "__Host-csrf_token", "__Secure-admin_session"

	loginForm := s.send(t, http.MethodGet, "/admin/login", nil)
	before := setCookie(t, loginForm, csrfName)
	beforeToken := csrfFromResponse(t, loginForm)

	login := s.send(t, http.MethodPost, "/admin/login", url.Values{
		"email": {testEmail}, "password": {testPassword}, "csrf_token": {beforeToken},
	}, before)
	if login.Status != http.StatusSeeOther {
		t.Fatalf("sign in: status %d", login.Status)
	}
	session := setCookie(t, login, sessionName)
	signedIn := setCookie(t, login, csrfName)
	if signedIn.Value == "" || signedIn.Value == before.Value {
		t.Fatal("sign-in did not replace the CSRF cookie")
	}

	// What the browser now holds: the new CSRF cookie and the session.
	forged := s.send(t, http.MethodPost, "/admin/products", url.Values{
		"csrf_token": {beforeToken}, "title": {"Forged"},
	}, signedIn, session)
	if forged.Status != http.StatusForbidden {
		t.Errorf("the pre-sign-in token after sign-in = %d, want 403", forged.Status)
	}
	page := s.send(t, http.MethodGet, accountPath, nil, signedIn, session)
	if page.Status != http.StatusOK {
		t.Fatalf("account page: status %d", page.Status)
	}
	signedInToken := csrfFromResponse(t, page)

	logout := s.send(t, http.MethodPost, "/admin/logout", url.Values{"csrf_token": {signedInToken}}, signedIn, session)
	if logout.Status != http.StatusSeeOther {
		t.Fatalf("sign out: status %d", logout.Status)
	}
	after := setCookie(t, logout, csrfName)
	if after.Value == "" || after.Value == signedIn.Value {
		t.Fatal("sign-out did not replace the CSRF cookie")
	}
	if cleared := setCookie(t, logout, sessionName); cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Error("sign-out did not clear the session cookie")
	}
	// The signed-in token, replayed into the signed-out browser, is refused.
	replay := s.send(t, http.MethodPost, "/admin/login", url.Values{
		"email": {testEmail}, "password": {testPassword}, "csrf_token": {signedInToken},
	}, after)
	if replay.Status != http.StatusForbidden {
		t.Errorf("the signed-in token after sign-out = %d, want 403", replay.Status)
	}
}

// A session cookie under its pre-migration bare name is not a session: a sibling
// subdomain can set a bare cookie, and the prefixed name is the one a browser
// refuses to let it overwrite.
func TestBareNamedSessionCookieIsIgnored(t *testing.T) {
	s := newSecureShop(t)
	token, _, err := s.users.IssueSession(t.Context(), s.owner.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	bare := &http.Cookie{Name: "admin_session", Value: token}
	prefixed := &http.Cookie{Name: "__Secure-admin_session", Value: token}

	if w := s.send(t, http.MethodGet, "/admin/products", nil, prefixed); w.Status != http.StatusOK {
		t.Fatalf("prefixed session cookie = %d, want 200 (the fixture is wrong)", w.Status)
	}
	w := s.send(t, http.MethodGet, "/admin/products", nil, bare)
	if w.Status != http.StatusSeeOther || !strings.HasPrefix(w.Location(), "/admin/login") {
		t.Errorf("bare-named session cookie = %d to %q, want a redirect to sign in", w.Status, w.Location())
	}
	// The login form's "already signed in" shortcut reads the cookie too.
	if w := s.send(t, http.MethodGet, "/admin/login", nil, bare); w.Status != http.StatusOK {
		t.Errorf("sign-in page with a bare-named cookie = %d, want 200 (not signed in)", w.Status)
	}
}

// Changing your own password also renews the CSRF token: the form that carried
// the old one, or a token an attacker planted earlier, is dead afterwards.
func TestCSRFTokenIsRenewedAtOwnPasswordChange(t *testing.T) {
	s := newSecureShop(t)
	const csrfName, sessionName = "__Host-csrf_token", "__Secure-admin_session"

	form := s.send(t, http.MethodGet, "/admin/login", nil)
	pre := setCookie(t, form, csrfName)
	login := s.send(t, http.MethodPost, "/admin/login", url.Values{
		"email": {testEmail}, "password": {testPassword}, "csrf_token": {csrfFromResponse(t, form)},
	}, pre)
	session, csrf := setCookie(t, login, sessionName), setCookie(t, login, csrfName)
	page := s.send(t, http.MethodGet, accountPath, nil, csrf, session)
	token := csrfFromResponse(t, page)

	const next = "a different long passphrase 42"
	change := s.send(t, http.MethodPost, accountPath, url.Values{
		"current_password": {testPassword}, "password": {next}, "password_confirm": {next}, "csrf_token": {token},
	}, csrf, session)
	if change.Status != http.StatusSeeOther {
		t.Fatalf("password change: status %d", change.Status)
	}
	renewed := setCookie(t, change, csrfName)
	if renewed.Value == "" || renewed.Value == csrf.Value {
		t.Fatal("own password change did not replace the CSRF cookie")
	}
	replay := s.send(t, http.MethodPost, "/admin/login", url.Values{
		"email": {testEmail}, "password": {next}, "csrf_token": {token},
	}, renewed)
	if replay.Status != http.StatusForbidden {
		t.Errorf("the pre-change token after a password change = %d, want 403", replay.Status)
	}
}

// An admin page refuses a session that has sat unused for longer than the idle
// limit, though its absolute expiry is hours away, and a request inside the limit
// keeps working. Through the real route, so the lookup the middleware runs is the
// one under test.
func TestIdleSessionIsRefusedAtTheAdminRoute(t *testing.T) {
	s := newSecureShop(t)
	token, _, err := s.users.IssueSession(t.Context(), s.owner.ID, 24*time.Hour)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	cookie := &http.Cookie{Name: "__Secure-admin_session", Value: token}
	// created_at is moved back as well: activity before creation counts as creation.
	setSeen := func(ago time.Duration) {
		t.Helper()
		sum := sha256.Sum256([]byte(token))
		tag, err := s.pool.Exec(t.Context(),
			`UPDATE admin_sessions SET last_seen = now() - make_interval(secs => $2),
			   created_at = now() - interval '4 hours', expires_at = now() + interval '20 hours'
			 WHERE token_hash = $1`,
			sum[:], ago.Seconds())
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("set last_seen: %v (rows %d)", err, tag.RowsAffected())
		}
	}
	setSeen(auth.SessionIdle - 5*time.Minute)
	if w := s.send(t, http.MethodGet, "/admin/products", nil, cookie); w.Status != http.StatusOK {
		t.Fatalf("session idle for just under the limit = %d, want 200", w.Status)
	}
	setSeen(auth.SessionIdle + 5*time.Minute)
	if w := s.send(t, http.MethodGet, "/admin/products", nil, cookie); w.Status != http.StatusSeeOther {
		t.Errorf("session idle past the limit = %d, want a redirect to sign in", w.Status)
	}
}

package handler

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

var shownToken = regexp.MustCompile(`<code>(gst_[A-Za-z0-9_-]+)</code>`)

func TestAdminTokens_CreateShowsTheTokenOnceAndItAuthenticates(t *testing.T) {
	s := setupShop(t)

	res, body := post(t, s.srv, "/admin/account/tokens", url.Values{"name": {"laptop"}, "days": {"90"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create = %d %s", res.StatusCode, body)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a page holding a token", res.Header.Get("Cache-Control"))
	}
	m := shownToken.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no token on the page:\n%s", body)
	}
	_, user, err := s.users.APITokenUser(t.Context(), m[1])
	if err != nil || user.ID != s.owner.ID {
		t.Fatalf("the shown token does not authenticate as the owner: %v %+v", err, user)
	}

	// Listed afterwards, but never shown again.
	res, body = get(t, s.srv, accountPath)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "laptop") {
		t.Fatalf("list = %d, missing the token's name", res.StatusCode)
	}
	if strings.Contains(body, m[1]) {
		t.Fatal("the token is shown again on the list page")
	}
}

func TestAdminTokens_RefusesABadNameAndAnUnofferedLifetime(t *testing.T) {
	s := setupShop(t)

	res, _ := post(t, s.srv, "/admin/account/tokens", url.Values{"name": {"  "}, "days": {"90"}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("blank name = %d, want 422", res.StatusCode)
	}
	res, _ = post(t, s.srv, "/admin/account/tokens", url.Values{"name": {"x"}, "days": {"10000"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unoffered lifetime = %d, want 400", res.StatusCode)
	}
}

func TestAdminTokens_RevokeOnlyYourOwn(t *testing.T) {
	s := setupShop(t)
	other := mustAccount(t, s, "other@example.com", "correct horse battery", auth.RoleAdmin)
	_, theirs, err := s.users.IssueAPIToken(t.Context(), other.ID, "theirs", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, mine, err := s.users.IssueAPIToken(t.Context(), s.owner.ID, "mine", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	res, _ := post(t, s.srv, "/admin/account/tokens/"+theirs.ID+"/revoke", url.Values{})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking another account's token = %d, want 404", res.StatusCode)
	}
	res, _ = post(t, s.srv, "/admin/account/tokens/"+mine.ID+"/revoke", url.Values{})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoking your own = %d, want 303", res.StatusCode)
	}
	left, err := s.users.APITokens(t.Context(), other.ID)
	if err != nil || len(left) != 1 {
		t.Fatalf("the other account's tokens = %v, %v", left, err)
	}
}

// Tokens are for administrators: a manager or a viewer is refused the routes and
// is not offered the section, while still reaching the rest of their profile.
func TestAdminTokens_OnlyAdministratorsMayHoldThem(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleManager, auth.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			s := newStore(t)
			email := string(role) + "@example.com"
			me := mustAccount(t, s, email, "correct horse battery", role)
			signInAs(t, s.srv, email, "correct horse battery")

			res, _ := post(t, s.srv, "/admin/account/tokens", url.Values{"name": {"read-only"}, "days": {"30"}})
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("create = %d, want 403", res.StatusCode)
			}
			left, err := s.users.APITokens(t.Context(), me.ID)
			if err != nil || len(left) != 0 {
				t.Errorf("tokens after a refused create = %v, %v", left, err)
			}

			res, body := get(t, s.srv, accountPath)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("profile settings = %d", res.StatusCode)
			}
			if strings.Contains(body, "/admin/account/tokens") {
				t.Error("the profile page offers API tokens to a role that cannot hold them")
			}
			if !strings.Contains(body, "current_password") {
				t.Error("the profile page lost the password form")
			}
		})
	}
}

func TestAdminTokens_ShownOnAnAdministratorsProfile(t *testing.T) {
	s := setupShop(t)
	res, body := get(t, s.srv, accountPath)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profile settings = %d", res.StatusCode)
	}
	for _, want := range []string{`action="/admin/account/tokens"`, `action="/admin/account"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the owner's profile page has no %s", want)
		}
	}
}

// The storefront's account menu: the admin links for a signed-in browser, and
// nothing at all — a 204 htmx leaves unswapped — for anybody else.
func TestAccountMenu(t *testing.T) {
	s := newStore(t)
	res, body := get(t, s.srv, "/admin/account/menu")
	if res.StatusCode != http.StatusNoContent || body != "" {
		t.Errorf("anonymous menu = %d %q, want an empty 204", res.StatusCode, body)
	}

	s = setupShop(t)
	res, body = get(t, s.srv, "/admin/account/menu")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signed-in menu = %d", res.StatusCode)
	}
	for _, want := range []string{`href="/admin/products"`, `href="/admin/account"`, `action="/admin/logout"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the menu has no %s", want)
		}
	}
	if got := res.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q; a menu naming who is signed in must not be cached", got)
	}
}

package handler

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

func TestCSRF_RejectsPostWithoutAToken(t *testing.T) {
	srv, store := setup(t)

	// Every state-changing admin route, because a form that forgot its hidden
	// field is exactly the kind of gap that goes unnoticed until it is abused.
	paths := []string{
		"/admin/login",
		"/admin/logout",
		"/admin/products",
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/delete",
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants",
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants/9f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/variants/9f2504e0-4f89-41d3-9a0c-0305e82c3301/delete",
	}
	for _, path := range paths {
		// An explicit empty token stops the helper from supplying a real one.
		res, _ := post(t, srv, path, url.Values{"csrf_token": {""}, "title": {"Forged"}, "kind": {"book"}})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s without a token = %d, want 403", path, res.StatusCode)
		}
	}

	if products, err := store.List(t.Context()); err != nil || len(products) != 0 {
		t.Errorf("List = %v, %v; a request without a CSRF token wrote something", products, err)
	}
}

func TestCSRF_RejectsATokenFromAnotherSession(t *testing.T) {
	victim, store := setup(t)
	attacker, _ := newServer(t)

	// nosurf ties the submitted token to the client's own cookie, so a token
	// minted for one visitor is useless to another.
	stolen := csrfToken(t, attacker)

	res, _ := post(t, victim, "/admin/products", url.Values{
		"csrf_token": {stolen},
		"title":      {"Forged"},
		"kind":       {"book"},
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("POST with another session's token = %d, want 403", res.StatusCode)
	}
	if products, err := store.List(t.Context()); err != nil || len(products) != 0 {
		t.Errorf("List = %v, %v; the forged request wrote something", products, err)
	}
}

// The origin check has three inputs and short-circuits on the first, so a test
// that always sends Sec-Fetch-Site never exercises the other two. nosurf assumes
// https unless configured otherwise, which made a plain-HTTP deployment reject
// every form while the tests passed — hence this.
func TestCSRF_AcceptsSameOriginPostByOriginOrRefererAlone(t *testing.T) {
	cases := map[string]func(r *http.Request, origin string){
		"Origin only":  func(r *http.Request, origin string) { r.Header.Set("Origin", origin) },
		"Referer only": func(r *http.Request, origin string) { r.Header.Set("Referer", origin+"/admin/products/new") },
	}

	for name, setHeaders := range cases {
		srv, store := setup(t)

		form := url.Values{"csrf_token": {csrfToken(t, srv)}, "title": {name}, "kind": {"book"}, "active": {"1"}}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/admin/products",
			strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		setHeaders(req, srv.URL)

		res, body := do(t, srv, req)
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("%s: POST = %d, want 303: %s", name, res.StatusCode, body)
			continue
		}
		if products, err := store.List(t.Context()); err != nil || len(products) != 1 {
			t.Errorf("%s: List = %v, %v; the accepted request did not write", name, products, err)
		}
	}
}

func TestCSRF_RejectsACrossOriginPost(t *testing.T) {
	srv, store := setup(t)

	// nosurf checks where the request came from as well as its token. A valid
	// token submitted from another origin — the shape of a real CSRF attempt if
	// a token ever leaked — must still be refused.
	form := url.Values{"csrf_token": {csrfToken(t, srv)}, "title": {"Forged"}, "kind": {"book"}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/admin/products",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://not-this-store.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	res, _ := do(t, srv, req)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST with a valid token = %d, want 403", res.StatusCode)
	}
	if products, err := store.List(t.Context()); err != nil || len(products) != 0 {
		t.Errorf("List = %v, %v; the cross-origin request wrote something", products, err)
	}
}

func TestCSRF_TokenIsInEveryAdminForm(t *testing.T) {
	srv, store := setup(t)

	p, err := store.Create(t.Context(), catalogProduct())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.CreateVariant(t.Context(), variantFor(p.ID)); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	// Count forms against tokens: the edit page carries several, and one form
	// missing its field would otherwise hide behind the others.
	for _, path := range []string{"/admin/products", "/admin/products/new", "/admin/products/" + p.ID + "/edit"} {
		_, body := get(t, srv, path)
		forms := strings.Count(body, "<form ")
		tokens := strings.Count(body, `name="csrf_token"`)
		if forms == 0 {
			t.Errorf("%s has no forms; the check is not testing anything", path)
		}
		if tokens != forms {
			t.Errorf("%s has %d forms but %d CSRF fields", path, forms, tokens)
		}
	}
}

func TestCSRF_DoesNotLeakItsCookieOutsideAdmin(t *testing.T) {
	// A fresh client, because nosurf only sets the cookie when the request
	// arrives without a valid one.
	srv, _ := newServer(t)

	// The storefront catalog fragments are meant to be embeddable cross-origin,
	// which means no cookies at all — so nosurf's cookie must stay scoped to
	// /admin. The fragments themselves land in the next phase; this pins the
	// scope before they do.
	res, _ := get(t, srv, "/admin/login")
	var found bool
	for _, c := range res.Cookies() {
		if c.Name == "csrf_token" {
			found = true
			if c.Path != "/admin" {
				t.Errorf("the CSRF cookie has Path %q, want /admin", c.Path)
			}
			if !c.HttpOnly {
				t.Error("the CSRF cookie is not HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("the CSRF cookie has SameSite %v, want Lax", c.SameSite)
			}
		}
	}
	if !found {
		t.Fatal("no csrf_token cookie was set on the login page")
	}
}

func catalogProduct() catalog.Product {
	return catalog.Product{Kind: "apparel", Slug: "tee", Title: "Tee", Active: true}
}

func variantFor(productID string) catalog.Variant {
	return catalog.Variant{ProductID: productID, SKU: "TEE-M", Size: "M", PriceCents: 29900, StockQty: 3, Active: true}
}

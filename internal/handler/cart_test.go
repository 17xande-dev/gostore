package handler

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// stockCart fills the catalog for the cart tests and returns the variants by
// size, since which variant is which matters to every assertion here.
func stockCart(t *testing.T, store *catalog.Store) map[string]catalog.Variant {
	t.Helper()
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "tee", Title: "Sample Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	variants := map[string]catalog.Variant{}
	for _, v := range []catalog.Variant{
		{ProductID: p.ID, SKU: "TEE-S", Size: "S", PriceCents: 29900, StockQty: 4, Active: true},
		{ProductID: p.ID, SKU: "TEE-M", Size: "M", PriceCents: 31900, StockQty: 1, Active: true},
		{ProductID: p.ID, SKU: "TEE-L", Size: "L", PriceCents: 99900, StockQty: 5, Active: false},
	} {
		out, err := store.CreateVariant(ctx, v)
		if err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
		variants[v.Size] = out
	}

	book, err := store.Create(ctx, catalog.Product{Slug: "a-book", Title: "A Book", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	out, err := store.CreateVariant(ctx, catalog.Variant{ProductID: book.ID, SKU: "BOOK-1", PriceCents: 24900, StockQty: 2, Active: true})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	variants["book"] = out
	return variants
}

// shopper is a client with a cookie jar and no admin session: the cart is
// anonymous, so nothing here signs in.
func shopper(t *testing.T) (*httptest.Server, *catalog.Store, map[string]catalog.Variant) {
	t.Helper()
	srv, store := newServer(t)
	return srv, store, stockCart(t, store)
}

func TestCart_EmptyByDefault(t *testing.T) {
	srv, _, _ := shopper(t)

	res, body := get(t, srv, "/cart")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart = %d", res.StatusCode)
	}
	if !strings.Contains(body, "Your cart is empty") {
		t.Error("an empty cart does not say so")
	}
	// Browsing must not leave a trail of empty cart rows, so no cart is created
	// and no cookie is set until something is added.
	for _, c := range res.Cookies() {
		if c.Name == CartCookieName {
			t.Error("visiting the cart page issued a cart cookie")
		}
	}
}

func TestCart_AddSetsCookieAndShowsTheItem(t *testing.T) {
	srv, _, variants := shopper(t)

	res, body := post(t, srv, "/cart/items", url.Values{
		"variant_id": {variants["S"].ID},
		"quantity":   {"2"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /cart/items = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != "/cart" {
		t.Errorf("Location = %q, want /cart", got)
	}

	cookie := cartCookieFrom(t, res)
	if !cookie.HttpOnly {
		t.Error("the cart cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	// Scoped to /cart, which is what keeps the catalog pages cookie-free and so
	// cacheable and embeddable.
	if cookie.Path != "/cart" {
		t.Errorf("Path = %q, want /cart", cookie.Path)
	}
	if cookie.MaxAge < 29*24*60*60 {
		t.Errorf("MaxAge = %d, want about 30 days", cookie.MaxAge)
	}
	// The token is the cart's only key, so it must not be guessable.
	if len(cookie.Value) < 32 {
		t.Errorf("cart token %q is too short to be unguessable", cookie.Value)
	}

	_, body = get(t, srv, "/cart")
	for _, want := range []string{"Sample Tee", "ZAR 299.00", "ZAR 598.00", "2 items"} {
		if !strings.Contains(body, want) {
			t.Errorf("the cart page is missing %q: %s", want, body)
		}
	}
}

func TestCart_AddDefaultsToOne(t *testing.T) {
	srv, _, variants := shopper(t)

	// A form without a quantity field means one of the thing.
	if res, body := post(t, srv, "/cart/items", url.Values{"variant_id": {variants["book"].ID}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST without quantity = %d %s", res.StatusCode, body)
	}
	if _, body := get(t, srv, "/cart"); !strings.Contains(body, "Total (1 item)") {
		t.Errorf("the cart does not hold exactly one item: %s", body)
	}
}

func TestCart_HTMXAddReturnsAStatusFragment(t *testing.T) {
	srv, _, variants := shopper(t)

	res, body := postHTMX(t, srv, "/cart/items", url.Values{
		"variant_id": {variants["S"].ID},
		"quantity":   {"1"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("htmx add = %d %s", res.StatusCode, body)
	}
	// The shopper keeps their place on the product page: a small status block
	// comes back rather than a redirect to the cart.
	if strings.Contains(body, "<html") {
		t.Error("the htmx add returned a whole document")
	}
	for _, want := range []string{"Added to your cart", "1 item", "/cart"} {
		if !strings.Contains(body, want) {
			t.Errorf("the status fragment is missing %q: %s", want, body)
		}
	}
}

func TestCart_StatusEndpointReflectsTheCart(t *testing.T) {
	srv, _, variants := shopper(t)

	// Product pages cannot read the cart themselves — the cookie is scoped to
	// /cart — so they ask for this fragment.
	_, body := get(t, srv, "/cart/status")
	if strings.Contains(body, "item") {
		t.Errorf("the status of an empty cart mentions items: %s", body)
	}

	if res, body := post(t, srv, "/cart/items", url.Values{"variant_id": {variants["S"].ID}, "quantity": {"3"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add = %d %s", res.StatusCode, body)
	}

	_, body = get(t, srv, "/cart/status")
	if !strings.Contains(body, "3 items") {
		t.Errorf("the status does not show 3 items: %s", body)
	}
	if !strings.Contains(body, "ZAR 897.00") {
		t.Errorf("the status does not show the total: %s", body)
	}
}

func TestCart_UpdateQuantity(t *testing.T) {
	srv, _, variants := shopper(t)
	addToCart(t, srv, variants["S"].ID, 1)

	res, body := post(t, srv, "/cart/items/"+variants["S"].ID, url.Values{"quantity": {"3"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	if _, body := get(t, srv, "/cart"); !strings.Contains(body, "3 items") {
		t.Errorf("the quantity did not change: %s", body)
	}

	// htmx gets the cart body back so the totals update in place.
	res, body = postHTMX(t, srv, "/cart/items/"+variants["S"].ID, url.Values{"quantity": {"1"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("htmx update = %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "<html") {
		t.Error("the htmx update returned a whole document")
	}
	if !strings.Contains(body, "Total (1 item)") || !strings.Contains(body, "ZAR 299.00") {
		t.Errorf("the swapped cart body is wrong: %s", body)
	}
	// The wrapper the fragment targets must not be inside the fragment, or each
	// swap would nest another copy of it.
	if strings.Contains(body, `id="cart"`) {
		t.Error("the cart fragment contains its own swap target")
	}
}

func TestCart_RemoveByQuantityZeroAndByDelete(t *testing.T) {
	srv, _, variants := shopper(t)

	// Without JavaScript, the remove button posts quantity 0 — same endpoint, no
	// extra route.
	addToCart(t, srv, variants["S"].ID, 2)
	if res, body := post(t, srv, "/cart/items/"+variants["S"].ID, url.Values{"quantity": {"0"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove via quantity 0 = %d %s", res.StatusCode, body)
	}
	if _, body := get(t, srv, "/cart"); !strings.Contains(body, "Your cart is empty") {
		t.Errorf("quantity 0 did not empty the cart: %s", body)
	}

	// With htmx, the same button sends a real DELETE.
	addToCart(t, srv, variants["S"].ID, 1)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete,
		srv.URL+"/cart/items/"+variants["S"].ID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", csrfToken(t, srv))
	req.Header.Set("Origin", srv.URL)
	res, body := do(t, srv, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE = %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "Your cart is empty") {
		t.Errorf("DELETE did not empty the cart: %s", body)
	}
}

func TestCart_RefusesMoreThanStock(t *testing.T) {
	srv, _, variants := shopper(t)

	// One M is in stock.
	res, body := post(t, srv, "/cart/items", url.Values{"variant_id": {variants["M"].ID}, "quantity": {"2"}})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("adding 2 of 1 = %d, want 409: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "Only 1 of that option left") {
		t.Errorf("the message does not say how many are left: %s", body)
	}
	if _, body := get(t, srv, "/cart"); !strings.Contains(body, "Your cart is empty") {
		t.Error("a refused add wrote to the cart anyway")
	}
}

func TestCart_RefusesUnavailableVariants(t *testing.T) {
	srv, _, variants := shopper(t)

	cases := map[string]string{
		"inactive variant": variants["L"].ID,
		"unknown variant":  "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"not a uuid":       "nonsense",
	}
	for name, id := range cases {
		res, body := post(t, srv, "/cart/items", url.Values{"variant_id": {id}, "quantity": {"1"}})
		if res.StatusCode != http.StatusConflict {
			t.Errorf("%s: %d, want 409: %s", name, res.StatusCode, body)
		}
		if !strings.Contains(body, "not for sale") {
			t.Errorf("%s: unexpected message: %s", name, body)
		}
	}
}

func TestCart_ShowsWithdrawnItemsAsUnavailable(t *testing.T) {
	srv, store, variants := shopper(t)
	addToCart(t, srv, variants["S"].ID, 2)

	// The shop withdraws it after it is in the cart.
	v := variants["S"]
	v.Active = false
	if _, err := store.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	_, body := get(t, srv, "/cart")
	if !strings.Contains(body, "Sample Tee") {
		t.Error("the withdrawn line disappeared from the cart")
	}
	if !strings.Contains(body, "no longer for sale") {
		t.Errorf("the cart does not explain the problem: %s", body)
	}
	if !strings.Contains(body, "Fix the problems above") {
		t.Error("the cart does not block checkout")
	}
}

func TestCart_StaleCookieStartsAFreshCart(t *testing.T) {
	srv, _, variants := shopper(t)
	addToCart(t, srv, variants["S"].ID, 1)

	// Simulate the cleanup job having removed the cart this cookie names: the
	// shopper should get a new cart rather than an error page.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/cart/items",
		strings.NewReader(url.Values{
			"variant_id": {variants["book"].ID},
			"quantity":   {"1"},
			"csrf_token": {csrfToken(t, srv)},
		}.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(&http.Cookie{Name: CartCookieName, Value: "a-token-that-names-no-cart"})

	res, body := do(t, srv, req)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add with a stale cookie = %d %s", res.StatusCode, body)
	}
	if cookie := cartCookieFrom(t, res); cookie.Value == "a-token-that-names-no-cart" {
		t.Error("the stale token was kept")
	}
}

// The product page's own form, submitted with its own token, exactly as a
// browser would. Everything else in this file posts to /cart/items with a token
// fetched from elsewhere, which is how an empty token on the product page went
// unnoticed until it was tried by hand: the catalog sits outside the CSRF group,
// so nosurf.Token was returning "" there and every add-to-cart was a 403.
func TestCart_AddFromTheProductPageWorksEndToEnd(t *testing.T) {
	srv, _, variants := shopper(t)

	_, page := get(t, srv, "/products/tee")
	if !strings.Contains(page, `action="/cart/items"`) {
		t.Fatalf("the product page has no add-to-cart form: %s", page)
	}

	m := csrfFieldRE.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the add-to-cart form has no CSRF field: %s", page)
	}
	token := html.UnescapeString(m[1])
	if token == "" {
		t.Fatal("the add-to-cart form's CSRF token is empty, so every add would be refused")
	}

	// Post the page's own form, with the browser's headers.
	form := url.Values{
		"csrf_token": {token},
		"variant_id": {variants["S"].ID},
		"quantity":   {"2"},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/cart/items",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Referer", srv.URL+"/products/tee")

	res, body := do(t, srv, req)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("submitting the product page's form = %d, want 303: %s", res.StatusCode, body)
	}
	if _, cart := get(t, srv, "/cart"); !strings.Contains(cart, "Total (2 items)") {
		t.Errorf("the item did not reach the cart: %s", cart)
	}
}

func TestCart_NeedsACSRFToken(t *testing.T) {
	srv, _, variants := shopper(t)

	// The cart changes state, so it sits inside the same CSRF group as the admin.
	res, _ := post(t, srv, "/cart/items", url.Values{
		"csrf_token": {""},
		"variant_id": {variants["S"].ID},
		"quantity":   {"1"},
	})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("POST /cart/items without a token = %d, want 403", res.StatusCode)
	}
	if _, body := get(t, srv, "/cart"); !strings.Contains(body, "Your cart is empty") {
		t.Error("a request without a CSRF token changed the cart")
	}
}

func TestCart_IsNotVisibleToAnotherShopper(t *testing.T) {
	srv, _, variants := shopper(t)
	addToCart(t, srv, variants["S"].ID, 1)

	// A second client with its own jar: carts are keyed by an unguessable token,
	// so one shopper's cart is not another's.
	other, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/cart", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := (&http.Client{}).Do(other) // no cookie jar
	if err != nil {
		t.Fatalf("GET /cart: %v", err)
	}
	defer res.Body.Close()

	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	if strings.Contains(string(buf[:n]), "Sample Tee") {
		t.Error("a shopper with no cookie saw somebody else's cart")
	}
}

func addToCart(t *testing.T, srv *httptest.Server, variantID string, quantity int) {
	t.Helper()
	res, body := post(t, srv, "/cart/items", url.Values{
		"variant_id": {variantID},
		"quantity":   {strconv.Itoa(quantity)},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add %s x%d = %d %s", variantID, quantity, res.StatusCode, body)
	}
}

func postHTMX(t *testing.T, srv *httptest.Server, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	if _, set := form["csrf_token"]; !set {
		form.Set("csrf_token", csrfToken(t, srv))
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Origin", srv.URL)
	return do(t, srv, req)
}

func cartCookieFrom(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == CartCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in the response", CartCookieName)
	return nil
}

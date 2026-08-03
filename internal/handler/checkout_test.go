package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/orders"
)

// newCheckoutShop is a shop with a stocked catalog and an anonymous shopper — the
// starting point for every test from "add to cart" onwards.
func newCheckoutShop(t *testing.T, edit ...func(*config.Config)) *shop {
	t.Helper()
	s := newStore(t, edit...)
	s.variants = stockCart(t, s.catalog)
	return s
}

// stockOf reads a variant's remaining stock straight from the catalog, which is
// what "the shop has three left" actually means.
func (s *shop) stockOf(t *testing.T, sku string) int {
	t.Helper()
	p, err := s.catalog.GetBySlug(t.Context(), "tee")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	for _, v := range p.Variants {
		if v.SKU == sku {
			return v.StockQty
		}
	}
	t.Fatalf("no variant %s", sku)
	return 0
}

func validCheckoutForm() url.Values {
	return url.Values{
		"name":    {"Jane Doe"},
		"email":   {"jane@example.com"},
		"phone":   {"+27 11 555 0100"},
		"address": {"1 Example Road\nExampletown"},
	}
}

func TestCheckout_EmptyCartGoesBackToTheCart(t *testing.T) {
	s := newCheckoutShop(t)

	// Nothing to buy: the cart page is where that is explained, so there is no
	// point rendering a form that cannot succeed.
	res, _ := get(t, s.srv, "/cart/checkout")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /cart/checkout with an empty cart = %d, want 303", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/cart" {
		t.Errorf("Location = %q, want /cart", got)
	}

	res, _ = post(t, s.srv, "/cart/checkout", validCheckoutForm())
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /cart/checkout with an empty cart = %d, want 303", res.StatusCode)
	}
}

func TestCheckout_ShowsTheFormAndWhatIsBeingBought(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 2)

	res, body := get(t, s.srv, "/cart/checkout")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart/checkout = %d", res.StatusCode)
	}
	for _, want := range []string{
		`action="/cart/checkout"`,
		`name="name"`, `name="email"`, `name="address"`,
		"Sample Tee",
		"ZAR 598.00", // the total, so nobody pays a figure they have not seen
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the checkout page is missing %q: %s", want, body)
		}
	}
	// It is inside the CSRF group, so the form has a real token. A page rendered
	// outside it would carry an empty one and every submission would be a 403.
	m := csrfFieldRE.FindStringSubmatch(body)
	if m == nil || m[1] == "" {
		t.Error("the checkout form has no CSRF token, so every submission would be refused")
	}
}

func TestCheckout_CreatesPendingOrderAndHandsOverToTheGateway(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 2)

	res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /cart/checkout = %d %s", res.StatusCode, body)
	}

	// One pending order, priced from the catalog.
	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	if order.Status != orders.StatusPending {
		t.Errorf("Status = %q, want pending — nothing has been paid yet", order.Status)
	}
	if order.TotalCents != 59800 {
		t.Errorf("TotalCents = %d, want 59800", order.TotalCents)
	}
	if order.Customer.Email != "jane@example.com" || order.Customer.Address == "" {
		t.Errorf("Customer = %+v", order.Customer)
	}
	if order.Gateway != "fake" {
		t.Errorf("Gateway = %q", order.Gateway)
	}

	// And the gateway was handed the order id and the order's own total — not a
	// figure from the submitted page.
	requests := s.gateway.Requests()
	if len(requests) != 1 {
		t.Fatalf("%d gateway requests, want 1", len(requests))
	}
	req := requests[0]
	if req.OrderID != order.ID || req.AmountCents != order.TotalCents {
		t.Errorf("gateway request = %+v, want order %s for %d", req, order.ID, order.TotalCents)
	}
	if req.Currency != "ZAR" || req.NameFirst != "Jane" || req.NameLast != "Doe" {
		t.Errorf("gateway request = %+v", req)
	}

	// The response is the auto-submitting hand-over form.
	for _, want := range []string{
		`id="gateway-redirect"`,
		`action="` + s.gateway.FormActionOrigin(),
		`name="order_id" value="` + order.ID + `"`,
		`name="signature"`,
		"/static/redirect.js", // the CSP forbids an inline script, so this is a file
		"Continue to fake",    // and without JavaScript the button is the mechanism
		order.Reference(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the redirect page is missing %q: %s", want, body)
		}
	}

	// Stock has not moved: it moves when the money arrives, not when a shopper
	// reaches a payment page.
	if got := s.stockOf(t, "TEE-S"); got != 4 {
		t.Errorf("stock = %d, want 4 — checkout must not reserve stock", got)
	}
	// And the cart is intact, for a shopper who backs out of paying.
	if _, cart := get(t, s.srv, "/cart"); !strings.Contains(cart, "Total (2 items)") {
		t.Errorf("the cart was emptied by reaching the payment page: %s", cart)
	}
}

func TestCheckout_RejectsAnIncompleteFormWithoutCreatingAnOrder(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	cases := map[string]func(url.Values){
		"no name":      func(f url.Values) { f.Set("name", "") },
		"no email":     func(f url.Values) { f.Set("email", "") },
		"no address":   func(f url.Values) { f.Set("address", "") },
		"bad email":    func(f url.Values) { f.Set("email", "jane at example.com") },
		"email typo":   func(f url.Values) { f.Set("email", "jane@example") },
		"absurd phone": func(f url.Values) { f.Set("phone", strings.Repeat("1", 200)) },
	}
	for name, edit := range cases {
		form := validCheckoutForm()
		edit(form)

		res, body := post(t, s.srv, "/cart/checkout", form)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422: %s", name, res.StatusCode, body)
			continue
		}
		// The form comes back with what was typed rather than emptied.
		if !strings.Contains(body, `action="/cart/checkout"`) {
			t.Errorf("%s: the form was not re-rendered", name)
		}
	}

	if _, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv)); err == nil {
		t.Error("a rejected checkout form created an order")
	}
	if len(s.gateway.Requests()) != 0 {
		t.Error("a rejected checkout form reached the gateway")
	}
}

func TestCheckout_RefusesACartThatCannotBeBought(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	// Withdrawn after the cart page was rendered, which is the case the checkout
	// has to re-check rather than trust the cart page for.
	v := s.variants["S"]
	v.Active = false
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	res, _ := post(t, s.srv, "/cart/checkout", validCheckoutForm())
	// A cart with an unbuyable line is not purchasable at all, so this lands back
	// on the cart page where the problem is spelled out.
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/cart" {
		t.Errorf("checkout with a withdrawn line = %d %q, want 303 /cart",
			res.StatusCode, res.Header.Get("Location"))
	}
	if _, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv)); err == nil {
		t.Error("an unbuyable cart produced an order")
	}
}

func TestCheckout_NeedsACSRFToken(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	form := validCheckoutForm()
	form.Set("csrf_token", "")
	if res, _ := post(t, s.srv, "/cart/checkout", form); res.StatusCode != http.StatusForbidden {
		t.Errorf("POST /cart/checkout without a token = %d, want 403", res.StatusCode)
	}
	if _, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv)); err == nil {
		t.Error("a request without a CSRF token created an order")
	}
}

func TestCheckout_SuccessPageGrantsNothing(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)
	if res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatalf("checkout = %d %s", res.StatusCode, body)
	}

	res, body := get(t, s.srv, "/cart/checkout/success")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart/checkout/success = %d", res.StatusCode)
	}

	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	// It names the order, because the cart cookie identifies it and a reference is
	// what a customer needs to quote.
	if !strings.Contains(body, order.Reference()) {
		t.Errorf("the success page does not name the order: %s", body)
	}
	// But it does not claim the payment succeeded: a shopper can reach this page by
	// typing the URL, and only the authenticated callback decides anything.
	if strings.Contains(body, "Payment confirmed") {
		t.Error("the return page claims payment succeeded for an unpaid order")
	}
	if !strings.Contains(body, "waiting for the payment to be confirmed") {
		t.Errorf("the return page does not say the payment is still unconfirmed: %s", body)
	}
	if order.Status != orders.StatusPending {
		t.Errorf("Status = %q; visiting the return page changed the order", order.Status)
	}
	if got := s.stockOf(t, "TEE-S"); got != 4 {
		t.Errorf("stock = %d; visiting the return page moved stock", got)
	}
}

func TestCheckout_SuccessPageWithoutACartCookieIsStillAPage(t *testing.T) {
	s := newCheckoutShop(t)

	// A shopper with no cookie — a different browser, a cleared jar — gets the
	// generic message rather than an error.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.srv.URL+"/cart/checkout/success", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := (&http.Client{}).Do(req) // no jar
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET /cart/checkout/success with no cookie = %d, want 200", res.StatusCode)
	}
}

func TestCheckout_CancelLeavesEverythingAlone(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 2)
	if res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatalf("checkout = %d %s", res.StatusCode, body)
	}

	res, body := get(t, s.srv, "/cart/checkout/cancel")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart/checkout/cancel = %d", res.StatusCode)
	}
	if !strings.Contains(body, "Nothing has been charged") {
		t.Errorf("the cancel page does not say so: %s", body)
	}

	// The cart is untouched, so trying again is one click.
	if _, cart := get(t, s.srv, "/cart"); !strings.Contains(cart, "Total (2 items)") {
		t.Errorf("cancelling emptied the cart: %s", cart)
	}
	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	if order.Status != orders.StatusPending {
		t.Errorf("Status = %q, want the order left pending", order.Status)
	}
}

func TestCheckout_CartPageOffersItOnlyWhenBuyable(t *testing.T) {
	s := newCheckoutShop(t)

	// An empty cart offers no checkout link.
	if _, body := get(t, s.srv, "/cart"); strings.Contains(body, "/cart/checkout") {
		t.Error("an empty cart offers a checkout link")
	}

	addToCart(t, s.srv, s.variants["S"].ID, 1)
	if _, body := get(t, s.srv, "/cart"); !strings.Contains(body, `href="/cart/checkout"`) {
		t.Errorf("a buyable cart does not offer a checkout link: %s", body)
	}

	// Withdraw it: the link goes, and the reason appears instead.
	v := s.variants["S"]
	v.Active = false
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}
	_, body := get(t, s.srv, "/cart")
	if strings.Contains(body, `href="/cart/checkout"`) {
		t.Error("an unbuyable cart still offers a checkout link")
	}
	if !strings.Contains(body, "Fix the problems above") {
		t.Errorf("the cart does not explain why checkout is unavailable: %s", body)
	}
}

// cartTokenOf reads the cart token out of the client's jar, which is how a test
// asks the store about the cart the requests have been building up.
func cartTokenOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()

	u, err := url.Parse(srv.URL + "/cart")
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	for _, c := range srv.Client().Jar.Cookies(u) {
		if c.Name == CartCookieName {
			return c.Value
		}
	}
	t.Fatal("no cart cookie in the jar")
	return ""
}

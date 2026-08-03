package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
)

// placeOrder runs a real checkout and returns the pending order it created, so the
// callback tests act on an order that came into existence the way orders actually
// do.
func placeOrder(t *testing.T, s *shop, sku string, quantity int) orders.Order {
	t.Helper()

	addToCart(t, s.srv, s.variants[sku].ID, quantity)
	if res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatalf("checkout = %d %s", res.StatusCode, body)
	}
	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	return order
}

// callback posts a notification the way a gateway does: no session, no CSRF token,
// no cookies at all.
func callback(t *testing.T, srv *httptest.Server, gateway string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+"/payments/"+gateway+"/callback", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Deliberately not srv.Client(): that has the jar, and a gateway has no
	// cookies. Using it would hide a dependency on one.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("POST callback: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func (s *shop) reload(t *testing.T, id string) orders.Order {
	t.Helper()
	o, err := s.orders.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get order: %v", err)
	}
	return o
}

func TestCallback_MarksPaidAndDecrementsStock(t *testing.T) {
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)
	before := s.stockOf(t, "TEE-S")

	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("callback = %d, want 200", res.StatusCode)
	}

	paid := s.reload(t, order.ID)
	if !paid.Paid() {
		t.Fatalf("Status = %q, want paid", paid.Status)
	}
	if paid.PaidAt.IsZero() {
		t.Error("paid_at was not set")
	}
	if paid.GatewayRef != "1089250" || paid.GatewayPayload == "" {
		t.Errorf("the gateway's own record was not kept: %+v", paid)
	}
	if got := s.stockOf(t, "TEE-S"); got != before-2 {
		t.Errorf("stock = %d, want %d", got, before-2)
	}

	// The basket has become an order, so it is emptied — and the shopper's cookie
	// keeps working.
	if _, cart := get(t, s.srv, "/cart"); !strings.Contains(cart, "Your cart is empty") {
		t.Errorf("the cart was not emptied by payment: %s", cart)
	}

	// And now the return page can say so.
	if _, body := get(t, s.srv, "/cart/checkout/success"); !strings.Contains(body, "Payment confirmed") {
		t.Errorf("the success page does not report the confirmed payment: %s", body)
	}
}

func TestCallback_IdempotentOnReplay(t *testing.T) {
	// Gateways retry, so a replay is routine traffic. It must not sell the same
	// stock twice.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)
	body := payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents)

	if res := callback(t, s.srv, "fake", body); res.StatusCode != http.StatusOK {
		t.Fatalf("first callback = %d", res.StatusCode)
	}
	after := s.stockOf(t, "TEE-S")

	for i := range 3 {
		if res := callback(t, s.srv, "fake", body); res.StatusCode != http.StatusOK {
			t.Fatalf("replay %d = %d, want 200 — a non-200 makes the gateway retry forever", i, res.StatusCode)
		}
	}
	if got := s.stockOf(t, "TEE-S"); got != after {
		t.Errorf("stock moved on a replay: %d, want %d", got, after)
	}
	if !s.reload(t, order.ID).Paid() {
		t.Error("a replay unpaid the order")
	}
}

func TestCallback_RejectsWhatTheGatewayCannotVouchFor(t *testing.T) {
	// The fake refuses to authenticate, standing in for a bad signature, a source
	// IP outside the allowlist, or PayFast declining to confirm the notification —
	// each of which the payfast package tests on its own.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)
	before := s.stockOf(t, "TEE-S")

	s.gateway.Reject = true
	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))

	// Still 200: an unauthenticated notification is not "try again later", it is
	// either forged or broken, and a gateway retrying it forever helps nobody.
	if res.StatusCode != http.StatusOK {
		t.Errorf("rejected callback = %d, want 200", res.StatusCode)
	}
	if got := s.reload(t, order.ID); got.Paid() {
		t.Error("an unauthenticated notification paid an order")
	}
	if got := s.stockOf(t, "TEE-S"); got != before {
		t.Errorf("an unauthenticated notification moved stock: %d, want %d", got, before)
	}
}

func TestCallback_RejectsAmountMismatch(t *testing.T) {
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)
	before := s.stockOf(t, "TEE-S")

	// Correctly authenticated, and for the wrong amount. The figure paid is not the
	// figure this store quoted, so it is not credited.
	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents-100))
	if res.StatusCode != http.StatusOK {
		t.Errorf("callback = %d, want 200", res.StatusCode)
	}

	got := s.reload(t, order.ID)
	if got.Paid() {
		t.Error("an order was paid for less than it cost")
	}
	// The status is left alone rather than called failed — the gateway said the
	// payment completed, so "failed" would misdescribe it — but the notification is
	// filed for whoever reconciles it.
	if got.Status != orders.StatusPending {
		t.Errorf("Status = %q, want it left at pending", got.Status)
	}
	if got.GatewayPayload == "" || got.GatewayAmount == "" {
		t.Errorf("the mismatched notification was not recorded: %+v", got)
	}
	if stock := s.stockOf(t, "TEE-S"); stock != before {
		t.Errorf("stock = %d, want %d", stock, before)
	}
}

func TestCallback_RecordsUnpaidOutcomes(t *testing.T) {
	cases := map[string]orders.Status{
		"CANCELLED": orders.StatusCancelled,
		"FAILED":    orders.StatusFailed,
		// A status this code does not recognise is not evidence that a payment will
		// not arrive, so the order stays pending.
		"PENDING":       orders.StatusPending,
		"SOMETHING_NEW": orders.StatusPending,
	}
	for gatewayStatus, want := range cases {
		t.Run(gatewayStatus, func(t *testing.T) {
			s := newCheckoutShop(t)
			order := placeOrder(t, s, "S", 1)
			before := s.stockOf(t, "TEE-S")

			res := callback(t, s.srv, "fake",
				payment.FakeCallbackBody(order.ID, "1089250", gatewayStatus, order.TotalCents))
			if res.StatusCode != http.StatusOK {
				t.Fatalf("callback = %d", res.StatusCode)
			}

			got := s.reload(t, order.ID)
			if got.Status != want {
				t.Errorf("Status = %q, want %q", got.Status, want)
			}
			if got.GatewayStatus != gatewayStatus {
				t.Errorf("gateway_status = %q, want the gateway's own %q", got.GatewayStatus, gatewayStatus)
			}
			if stock := s.stockOf(t, "TEE-S"); stock != before {
				t.Errorf("an unpaid outcome moved stock: %d, want %d", stock, before)
			}
			// The cart survives an unpaid outcome, so the shopper can try again.
			if _, cart := get(t, s.srv, "/cart"); strings.Contains(cart, "Your cart is empty") {
				t.Error("an unpaid outcome emptied the cart")
			}
		})
	}
}

func TestCallback_AlwaysAnswers200(t *testing.T) {
	// Every rejection path, because a non-200 makes a gateway retry something this
	// store has already decided to ignore.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 1)

	cases := map[string]struct {
		gateway string
		body    []byte
	}{
		"unknown gateway":  {"stripe", payment.FakeCallbackBody(order.ID, "1", "paid", order.TotalCents)},
		"unknown order":    {"fake", payment.FakeCallbackBody("3f2504e0-4f89-41d3-9a0c-0305e82c3301", "1", "paid", 100)},
		"malformed order":  {"fake", payment.FakeCallbackBody("not-a-uuid", "1", "paid", 100)},
		"empty body":       {"fake", nil},
		"nonsense body":    {"fake", []byte("%%%")},
		"no order id":      {"fake", []byte("status=paid&amount=299.00")},
		"absurd body size": {"fake", []byte("a=" + strings.Repeat("b", maxCallbackBytes+100))},
	}
	for name, tc := range cases {
		res := callback(t, s.srv, tc.gateway, tc.body)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", name, res.StatusCode)
		}
	}

	if got := s.reload(t, order.ID); got.Paid() {
		t.Error("one of the rejected notifications paid the order")
	}
}

func TestCallback_IsOutsideTheCSRFGroup(t *testing.T) {
	// A gateway cannot carry a CSRF token, so the callback has to work without one —
	// and it must not pick up a token cookie either, since it is not a browser and
	// nothing should be issued to it.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 1)

	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))
	if res.StatusCode == http.StatusForbidden {
		t.Fatal("the callback is inside the CSRF group, so no gateway could ever reach it")
	}
	if cookies := res.Cookies(); len(cookies) != 0 {
		t.Errorf("the callback response set cookies: %v", cookies)
	}
	if !s.reload(t, order.ID).Paid() {
		t.Error("a notification with no CSRF token was not applied")
	}
}

func TestCallback_OversellIsRecordedRatherThanRefused(t *testing.T) {
	// Stock ran out between checkout and payment. The money is taken, so the order
	// stands and a human reconciles the stock; refusing it would lose the sale and
	// still be oversold.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)

	v := s.variants["S"]
	v.StockQty = 1
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("callback = %d", res.StatusCode)
	}

	if !s.reload(t, order.ID).Paid() {
		t.Error("the order was not recorded paid, so the money is unaccounted for")
	}
	if got := s.stockOf(t, "TEE-S"); got != 1 {
		t.Errorf("stock = %d, want it left at 1 rather than driven negative", got)
	}
}

func TestCallback_OnlyAcceptsPOST(t *testing.T) {
	s := newCheckoutShop(t)

	// A GET is not a notification. It reaches no handler at all, which is the mux
	// doing its job.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.srv.URL+"/payments/fake/callback", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("GET /payments/fake/callback was served")
	}
}

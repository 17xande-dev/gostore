package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/payment"
)

// qrGateway is a second configured provider whose hand-over is a link and a QR
// code rather than a form post — SnapScan's shape, without SnapScan.
func qrGateway() *payment.Fake {
	g := payment.NewFakeNamed("qrfake", "Fake QR wallet")
	g.Kind = payment.HandoverLink
	return g
}

// One gateway is not a choice, and asking somebody to make it would be noise. The
// name still reaches the handler, as a hidden field, so there is one code path
// and not two.
func TestCheckout_WithOneGatewayAsksNothing(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	_, body := get(t, s.srv, "/cart/checkout")
	if strings.Contains(body, `type="radio"`) {
		t.Error("a store with one gateway rendered a payment-method chooser")
	}
	if !strings.Contains(body, `name="gateway" value="fake"`) {
		t.Errorf("the form does not carry the gateway name: %s", body)
	}
}

func TestCheckout_WithTwoGatewaysLetsTheShopperPick(t *testing.T) {
	second := qrGateway()
	s := newStoreWith(t, []payment.Gateway{second})
	s.variants = stockCart(t, s.catalog)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	_, body := get(t, s.srv, "/cart/checkout")
	for _, want := range []string{
		`name="gateway" value="fake"`,
		`name="gateway" value="qrfake"`,
		"Fake QR wallet", // the label, not the route name
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the chooser is missing %q: %s", want, body)
		}
	}
	// The first configured gateway is preselected, so a shopper who ignores the
	// question still has an answer.
	if !strings.Contains(body, `value="fake" checked`) {
		t.Errorf("no gateway is preselected: %s", body)
	}

	// Picking the second one is what the order records, and what the callback will
	// later have to agree with.
	form := validCheckoutForm()
	form.Set("gateway", "qrfake")
	res, body := post(t, s.srv, "/cart/checkout", form)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /cart/checkout = %d: %s", res.StatusCode, body)
	}

	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	if order.Gateway != "qrfake" {
		t.Errorf("order.Gateway = %q, want the gateway the shopper chose", order.Gateway)
	}
	if len(second.Requests()) != 1 || len(s.gateway.Requests()) != 0 {
		t.Errorf("the order went to the wrong gateway: %d to the chosen one, %d to the other",
			len(second.Requests()), len(s.gateway.Requests()))
	}
}

// A gateway this store has not configured must not be usable, whatever the form
// says. Otherwise a tampered field decides where a shopper is sent to pay.
func TestCheckout_RefusesAnUnconfiguredGateway(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	form := validCheckoutForm()
	form.Set("gateway", "somebody-elses-gateway")

	res, body := post(t, s.srv, "/cart/checkout", form)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /cart/checkout = %d, want 422: %s", res.StatusCode, body)
	}
	if _, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv)); err == nil {
		t.Error("an unconfigured gateway still created an order")
	}
	if len(s.gateway.Requests()) != 0 {
		t.Error("an unconfigured gateway still reached a real one")
	}
}

// With a choice on the page, not making it is not something to guess at: the two
// gateways are not interchangeable to the shopper.
func TestCheckout_WithTwoGatewaysRefusesAnEmptyChoice(t *testing.T) {
	s := newStoreWith(t, []payment.Gateway{qrGateway()})
	s.variants = stockCart(t, s.catalog)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm())
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /cart/checkout with no gateway = %d, want 422: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "choose how you would like to pay") {
		t.Errorf("the page does not say what is wrong: %s", body)
	}
}

// The QR hand-over is a different page from the form post, and the parts that
// matter are the link, the image, and the absence of an auto-submit.
func TestCheckout_QRHandoverShowsALinkAndACode(t *testing.T) {
	second := qrGateway()
	s := newStoreWith(t, []payment.Gateway{second})
	s.variants = stockCart(t, s.catalog)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	form := validCheckoutForm()
	form.Set("gateway", "qrfake")
	res, body := post(t, s.srv, "/cart/checkout", form)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /cart/checkout = %d: %s", res.StatusCode, body)
	}

	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}

	for _, want := range []string{
		"Open Fake QR wallet to pay",                    // the link a phone follows
		`https://gateway.example/qr/`,                   // and it goes to the gateway
		`<img src="https://gateway.example/qr/fake.svg`, // the code a desktop shows
		order.Reference(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the QR hand-over page is missing %q: %s", want, body)
		}
	}
	// A link must never be followed for the shopper: the page is the instructions.
	if strings.Contains(body, "redirect.js") {
		t.Error("the QR hand-over page loads the auto-submit script")
	}
	// The QR image must carry no width or height attributes — they distort the
	// code past scanning, and the failure is invisible outside a camera.
	img := body[strings.Index(body, "<img src=\"https://gateway.example/qr/"):]
	img = img[:strings.Index(img, ">")]
	if strings.Contains(img, "width=") || strings.Contains(img, "height=") {
		t.Errorf("the QR image is sized by attribute, which distorts it: %s", img)
	}
}

// The poll exists because a QR payment has no redirect back. It reports the
// order's status and grants nothing.
func TestCheckoutStatus_ReportsTheOrderAndRedirectsWhenPaid(t *testing.T) {
	second := qrGateway()
	s := newStoreWith(t, []payment.Gateway{second})
	s.variants = stockCart(t, s.catalog)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	// Nothing ordered yet: an answer, not an error.
	res, body := get(t, s.srv, "/cart/checkout/status")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart/checkout/status = %d", res.StatusCode)
	}

	form := validCheckoutForm()
	form.Set("gateway", "qrfake")
	if res, body := post(t, s.srv, "/cart/checkout", form); res.StatusCode != http.StatusOK {
		t.Fatalf("POST /cart/checkout = %d: %s", res.StatusCode, body)
	}

	// Pending: it keeps asking, which is what the polling attribute is.
	res, body = get(t, s.srv, "/cart/checkout/status")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /cart/checkout/status = %d", res.StatusCode)
	}
	if !strings.Contains(body, "hx-trigger=") {
		t.Errorf("a pending order's status does not keep polling: %s", body)
	}

	// Paid, through the gateway the order was placed on.
	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}
	callback(t, s.srv, "qrfake", payment.FakeCallbackBody(order.ID, "ref-1", "paid", order.TotalCents))

	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/cart/checkout/status", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	res, err = s.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /cart/checkout/status: %v", err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("HX-Redirect"); got != "/cart/checkout/success" {
		t.Errorf("HX-Redirect = %q, want the success page once the money is in", got)
	}
}

// A gateway only ever proves things about its own account. Without this check, a
// genuine notification from one provider could settle an order placed through
// another — which is a way to be paid nothing and ship anyway.
func TestCallback_RefusesAnOrderPlacedOnAnotherGateway(t *testing.T) {
	second := qrGateway()
	s := newStoreWith(t, []payment.Gateway{second})
	s.variants = stockCart(t, s.catalog)
	addToCart(t, s.srv, s.variants["S"].ID, 1)

	// Placed on "fake"…
	form := validCheckoutForm()
	form.Set("gateway", "fake")
	if res, body := post(t, s.srv, "/cart/checkout", form); res.StatusCode != http.StatusOK {
		t.Fatalf("POST /cart/checkout = %d: %s", res.StatusCode, body)
	}
	order, err := s.orders.LatestForCart(t.Context(), cartTokenOf(t, s.srv))
	if err != nil {
		t.Fatalf("LatestForCart: %v", err)
	}

	// …and confirmed by "qrfake", which is not this order's gateway.
	res := callback(t, s.srv, "qrfake", payment.FakeCallbackBody(order.ID, "ref-1", "paid", order.TotalCents))
	// Still 200: a gateway retries anything else, and this notification is not
	// going to become valid on the third attempt.
	if res.StatusCode != http.StatusOK {
		t.Errorf("callback = %d, want 200", res.StatusCode)
	}

	after, err := s.orders.Get(t.Context(), order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Paid() {
		t.Error("a notification from another gateway marked the order paid")
	}
	if after.GatewayRef != "" {
		t.Errorf("GatewayRef = %q; another gateway's reference was recorded", after.GatewayRef)
	}
}

// A callback for a gateway nobody configured is dropped, not resolved to the
// default.
func TestCallback_RefusesAnUnknownGateway(t *testing.T) {
	s := newCheckoutShop(t)

	res := callback(t, s.srv, "nosuchgateway",
		payment.FakeCallbackBody("does-not-matter", "ref", "paid", 100))
	if res.StatusCode != http.StatusOK {
		t.Errorf("callback = %d, want 200", res.StatusCode)
	}
}

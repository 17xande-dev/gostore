package handler

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/outbox"
	"github.com/17xande-dev/gostore/internal/payment"
)

func TestCheckout_ConcurrentSubmissionIsOneOrder(t *testing.T) {
	s := newCheckoutShop(t)
	addToCart(t, s.srv, s.variants["S"].ID, 1)
	token := cartTokenOf(t, s.srv)
	form := validCheckoutForm()
	key := form.Get("checkout_key")
	// Concurrent requests use the same form, just as a double click or retry does.
	form = withToken(t, s.srv, form)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			req := newRequest(t, s.srv, http.MethodPost, "/cart/checkout", form)
			res, body := do(t, s.srv, req)
			if res.StatusCode != http.StatusOK {
				t.Errorf("checkout: %d %s", res.StatusCode, body)
			}
		})
	}
	wg.Wait()
	order, err := s.orders.CheckoutByKey(t.Context(), token, key)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.orders.List(t.Context(), 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("orders = %d: %v", len(list), err)
	}
	for _, request := range s.gateway.Requests() {
		if request.OrderID != order.ID {
			t.Fatal("duplicate submission sent a different order")
		}
	}
	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "once", "paid", order.TotalCents))
	res, _ := post(t, s.srv, "/cart/checkout", form)
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), order.ID) {
		t.Fatal("replayed paid checkout should show its original receipt")
	}
}

func TestCheckout_LatePaymentPreservesEditedCartAndNamesItsOrder(t *testing.T) {
	s := newCheckoutShop(t)
	first := placeOrder(t, s, "S", 1)
	addToCart(t, s.srv, s.variants["book"].ID, 1)
	if res, body := post(t, s.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatal(body)
	}
	callback(t, s.srv, "fake", payment.FakeCallbackBody(first.ID, "late", "paid", first.TotalCents))
	basket, err := cart.NewStore(s.pool).Get(t.Context(), cartTokenOf(t, s.srv))
	if err != nil || len(basket.Items) != 2 {
		t.Fatalf("new basket lost: %+v %v", basket, err)
	}
	_, body := get(t, s.srv, "/cart/checkout/success?order="+first.ID)
	if !strings.Contains(body, "Payment confirmed") || !strings.Contains(body, first.Reference()) {
		t.Fatal("return page followed newer order")
	}
	newSession(t, s.srv)
	_, body = get(t, s.srv, "/cart/checkout/success?order="+first.ID)
	if strings.Contains(body, first.Reference()) {
		t.Fatal("order id bypassed cart ownership")
	}
}

func TestOrderMail_EncryptedQueueSurvivesFailureAndRestart(t *testing.T) {
	d := newDigitalShop(t)
	addToCart(t, d.srv, d.audio.ID, 1)
	if res, body := post(t, d.srv, "/cart/checkout", validCheckoutForm()); res.StatusCode != http.StatusOK {
		t.Fatal(body)
	}
	order, err := d.orders.LatestForCart(t.Context(), cartTokenOf(t, d.srv))
	if err != nil {
		t.Fatal(err)
	}
	callback(t, d.srv, "fake", payment.FakeCallbackBody(order.ID, "durable", "paid", order.TotalCents))
	if len(d.mail.Sent()) != 0 {
		t.Fatal("HTTP callback waited for email")
	}
	var encrypted []byte
	if err := d.pool.QueryRow(t.Context(), `SELECT payload FROM email_jobs WHERE order_id=$1`, order.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("/downloads/")) || bytes.Contains(encrypted, []byte("jane@example.com")) {
		t.Fatal("queue leaked plaintext")
	}

	d.mail.Err = errors.New("temporary SMTP failure")
	d.handler.ProcessMail(t.Context())
	jobs, err := d.handler.outbox.ForOrder(t.Context(), order.ID)
	if err != nil || len(jobs) != 1 || jobs[0].Attempts != 1 || jobs[0].SentAt != nil {
		t.Fatalf("failure not durable: %+v %v", jobs, err)
	}
	// A fresh queue instance has no memory of the callback's plaintext tokens.
	d.handler.outbox, err = outbox.New(d.pool, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	d.mail.Err = nil
	signIn(t, d.srv)
	res, body := post(t, d.srv, "/admin/orders/"+order.ID+"/email/retry", url.Values{})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry: %d %s", res.StatusCode, body)
	}
	d.handler.ProcessMail(t.Context())
	sent := d.mail.To("jane@example.com")
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	token := tokenFromEmail(t, sent[0].Text)
	if _, err := d.grants.Lookup(t.Context(), token); err != nil {
		t.Fatalf("recovered email contains invalid link: %v", err)
	}
	if err := d.pool.QueryRow(t.Context(), `SELECT payload FROM email_jobs WHERE order_id=$1`, order.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) != 0 || !d.reload(t, order.ID).Emailed {
		t.Fatal("sent payload was not erased and delivery recorded")
	}
}

func TestOrderMail_QueueFailureRollsBackPayment(t *testing.T) {
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 1)
	if _, err := s.pool.Exec(t.Context(), `ALTER TABLE email_jobs ADD CONSTRAINT test_no_jobs CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	res := callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "atomic", "paid", order.TotalCents))
	if res.StatusCode != http.StatusServiceUnavailable || s.reload(t, order.ID).Paid() || s.stockOf(t, "TEE-S") != 4 {
		t.Fatal("payment committed without its delivery job")
	}
}

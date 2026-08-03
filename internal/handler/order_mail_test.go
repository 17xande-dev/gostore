package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/payment"
)

// notifying returns a shop whose config sends the owner a copy, since that is off
// unless ORDER_NOTIFY_EMAIL is set.
const ownerAddress = "orders@example.com"

func newNotifyingShop(t *testing.T) *shop {
	t.Helper()
	return newCheckoutShop(t, func(c *config.Config) { c.OrderNotifyEmail = ownerAddress })
}

func TestOrderMail_ConfirmationOnPaidCallback(t *testing.T) {
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)

	// Nothing is sent for a pending order: only a paid one gets a receipt.
	if sent := s.mail.Sent(); len(sent) != 0 {
		t.Fatalf("%d emails sent before payment: %+v", len(sent), sent)
	}

	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))

	sent := s.mail.To("jane@example.com")
	if len(sent) != 1 {
		t.Fatalf("%d confirmations to the customer, want 1: %+v", len(sent), s.mail.Sent())
	}
	m := sent[0]

	if !strings.Contains(m.Subject, order.Reference()) {
		t.Errorf("the subject does not carry the reference: %q", m.Subject)
	}
	if !strings.Contains(m.Subject, "Test Store") {
		t.Errorf("the subject does not name the store: %q", m.Subject)
	}

	// Both parts, and the plain-text one is not optional: a receipt has to arrive
	// readable in a client that refuses HTML.
	if m.Text == "" {
		t.Fatal("the confirmation has no plain-text part")
	}
	if m.HTML == "" {
		t.Error("the confirmation has no HTML part")
	}

	for _, want := range []string{"Jane Doe", "Sample Tee", "598.00", order.Reference(), "1 Example Road"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("the plain-text confirmation is missing %q:\n%s", want, m.Text)
		}
		if !strings.Contains(m.HTML, want) {
			t.Errorf("the HTML confirmation is missing %q", want)
		}
	}
	// The plain-text part is text/template output, so nothing in it is
	// HTML-escaped — a receipt with &amp; in it is the bug this guards.
	if strings.Contains(m.Text, "&amp;") || strings.Contains(m.Text, "&#") {
		t.Errorf("the plain-text part went through the HTML escaper:\n%s", m.Text)
	}
	// And the HTML part renders the address's newlines rather than running the
	// lines together.
	if !strings.Contains(m.HTML, "1 Example Road<br>Exampletown") {
		t.Errorf("the HTML address lost its line breaks: %s", m.HTML)
	}

	// The order records that the receipt went out.
	if !s.reload(t, order.ID).Emailed {
		t.Error("orders.emailed was not set after the confirmation was sent")
	}
}

func TestOrderMail_NotResentOnReplay(t *testing.T) {
	// Gateways retry. A customer should get one receipt, not one per retry.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 1)
	body := payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents)

	for range 4 {
		callback(t, s.srv, "fake", body)
	}

	if sent := s.mail.To("jane@example.com"); len(sent) != 1 {
		t.Errorf("%d confirmations after four notifications, want 1", len(sent))
	}
}

func TestOrderMail_NothingSentForUnpaidOutcomes(t *testing.T) {
	for _, status := range []string{"CANCELLED", "FAILED", "PENDING"} {
		t.Run(status, func(t *testing.T) {
			s := newCheckoutShop(t)
			order := placeOrder(t, s, "S", 1)

			callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1", status, order.TotalCents))

			if sent := s.mail.Sent(); len(sent) != 0 {
				t.Errorf("%d emails sent for a %s payment: %+v", len(sent), status, sent)
			}
			if s.reload(t, order.ID).Emailed {
				t.Error("emailed was set for an unpaid order")
			}
		})
	}
}

func TestOrderMail_NothingSentForARejectedNotification(t *testing.T) {
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 1)

	s.gateway.Reject = true
	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1", "paid", order.TotalCents))

	if sent := s.mail.Sent(); len(sent) != 0 {
		t.Errorf("an unauthenticated notification produced email: %+v", sent)
	}
}

func TestOrderMail_DeliveryFailureDoesNotLoseTheSale(t *testing.T) {
	// The invariant the whole design turns on: the order is recorded paid *before*
	// mail is attempted, so a mail server having a bad afternoon cannot cost a sale.
	s := newCheckoutShop(t)
	order := placeOrder(t, s, "S", 2)
	s.mail.Err = errors.New("mail server is having a bad afternoon")

	res := callback(t, s.srv, "fake",
		payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))

	// Still 200 — the gateway must not retry because *email* failed. The payment
	// is recorded and retrying would not help.
	if res.StatusCode != http.StatusOK {
		t.Errorf("callback = %d after a mail failure, want 200", res.StatusCode)
	}

	paid := s.reload(t, order.ID)
	if !paid.Paid() {
		t.Error("a mail failure prevented the order being recorded paid")
	}
	if got := s.stockOf(t, "TEE-S"); got != 2 {
		t.Errorf("stock = %d, want 2 — a mail failure interfered with inventory", got)
	}
	// emailed stays false, which is honest: nothing was sent.
	if paid.Emailed {
		t.Error("emailed was set despite the send failing")
	}
}

func TestOrderMail_OwnerNotification(t *testing.T) {
	s := newNotifyingShop(t)
	order := placeOrder(t, s, "S", 2)

	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))

	// Two separate sends, not one message with two recipients: a receipt and a work
	// order say different things, and one failing must not suppress the other.
	owner := s.mail.To(ownerAddress)
	if len(owner) != 1 {
		t.Fatalf("%d notifications to the owner, want 1: %+v", len(owner), s.mail.Sent())
	}
	if len(s.mail.To("jane@example.com")) != 1 {
		t.Error("the customer's confirmation went missing when the owner copy was added")
	}

	m := owner[0]
	for _, want := range []string{
		"Sample Tee",       // what to pack
		"1 Example Road",   // where it goes
		"jane@example.com", // who to contact
		order.Reference(),  // and which order it is
		"/admin/orders/" + order.ID,
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("the owner notification is missing %q:\n%s", want, m.Text)
		}
	}
	if !strings.Contains(m.Subject, "598.00") {
		t.Errorf("the subject does not carry the total: %q", m.Subject)
	}
	if strings.Contains(m.Subject, "OVERSOLD") {
		t.Errorf("a normal order was flagged as oversold: %q", m.Subject)
	}
}

func TestOrderMail_OwnerNotificationCarriesTheOversellWarning(t *testing.T) {
	// The oversell only otherwise exists in the logs, and the person who has to
	// tell a customer their item is gone should not have to find it there.
	s := newNotifyingShop(t)
	order := placeOrder(t, s, "S", 2)

	v := s.variants["S"]
	v.StockQty = 1
	if _, err := s.catalog.UpdateVariant(t.Context(), v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1089250", "paid", order.TotalCents))

	owner := s.mail.To(ownerAddress)
	if len(owner) != 1 {
		t.Fatalf("%d notifications to the owner, want 1", len(owner))
	}
	if !strings.HasPrefix(owner[0].Subject, "OVERSOLD:") {
		t.Errorf("the subject does not lead with the oversell: %q", owner[0].Subject)
	}
	if !strings.Contains(owner[0].Text, "OVERSOLD") || !strings.Contains(owner[0].Text, "Sample Tee") {
		t.Errorf("the body does not name the oversold line:\n%s", owner[0].Text)
	}

	// The customer is deliberately not told, in the same breath as being thanked,
	// that their order may not be deliverable.
	customer := s.mail.To("jane@example.com")
	if len(customer) != 1 {
		t.Fatalf("%d confirmations to the customer, want 1", len(customer))
	}
	if strings.Contains(customer[0].Text, "OVERSOLD") {
		t.Error("the customer's receipt mentions the oversell")
	}
}

func TestOrderMail_NoOwnerNotificationWhenUnconfigured(t *testing.T) {
	s := newCheckoutShop(t) // ORDER_NOTIFY_EMAIL unset
	order := placeOrder(t, s, "S", 1)

	callback(t, s.srv, "fake", payment.FakeCallbackBody(order.ID, "1", "paid", order.TotalCents))

	if sent := s.mail.Sent(); len(sent) != 1 {
		t.Errorf("%d emails with no notify address configured, want just the customer's: %+v", len(sent), sent)
	}
}

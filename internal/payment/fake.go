package payment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
)

// ErrFakeRejected is what a Fake returns for a callback it was told to refuse,
// standing in for any of the reasons a real gateway rejects one.
var ErrFakeRejected = errors.New("payment: fake gateway rejected the callback")

// Fake is a Gateway for tests. It exists in this package, next to the interface,
// for the same reason email.Sender's fake will: the handler and store tests need
// to exercise the whole payment path — a pending order becoming paid, stock
// moving, a replay being ignored — without a network call to a payment provider
// or a set of credentials in CI.
//
// It is not wired into the server. Nothing in cmd or main constructs one, and
// there is no configuration value that selects it: a store that silently took no
// money would be worse than one that refuses to start.
type Fake struct {
	// Reject makes ParseCallback fail, standing in for a bad signature, a
	// source IP outside the allowlist, or a failed server-to-server check.
	Reject bool

	mu       sync.Mutex
	requests []Request
}

// NewFake returns a Fake gateway.
func NewFake() *Fake { return &Fake{} }

func (f *Fake) Name() string { return "fake" }

func (f *Fake) FormActionOrigin() string { return "https://gateway.example" }

// BuildRedirectForm records the request and returns a form that would post to
// nowhere. The recorded requests are what a test asserts the checkout handed
// over.
func (f *Fake) BuildRedirectForm(r Request) (string, []Field, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()

	return f.FormActionOrigin() + "/pay", []Field{
		{Name: "order_id", Value: r.OrderID},
		{Name: "amount", Value: strconv.FormatInt(r.AmountCents, 10)},
		{Name: "signature", Value: "fake-signature"},
	}, nil
}

// Requests returns every request handed to BuildRedirectForm, in order.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// ParseCallback reads a form-encoded body with the fields FakeCallbackBody
// writes. It authenticates nothing beyond honouring Reject — proving a callback
// genuine is the one thing a fake cannot stand in for, which is why the real
// implementation's validation has its own tests.
func (f *Fake) ParseCallback(_ context.Context, body []byte, _ string) (Callback, error) {
	if f.Reject {
		return Callback{}, ErrFakeRejected
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return Callback{}, fmt.Errorf("payment: fake: parse body: %w", err)
	}

	amount := values.Get("amount")
	cents, err := ParseAmount(amount)
	if err != nil {
		return Callback{}, fmt.Errorf("payment: fake: amount %q: %w", amount, err)
	}

	status := values.Get("status")
	return Callback{
		OrderID:     values.Get("order_id"),
		Ref:         values.Get("ref"),
		Status:      status,
		Paid:        status == "paid",
		Amount:      amount,
		AmountCents: cents,
		Raw:         body,
	}, nil
}

// FakeCallbackBody builds the body a Fake understands, so tests write a callback
// the same way in every one of them.
func FakeCallbackBody(orderID, ref, status string, amountCents int64) []byte {
	return []byte(url.Values{
		"order_id": {orderID},
		"ref":      {ref},
		"status":   {status},
		"amount":   {FormatAmount(amountCents)},
	}.Encode())
}

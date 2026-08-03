// Package payment is the gateway-agnostic half of taking money: the interface a
// gateway implements, the values it exchanges with the rest of the store, and a
// fake for tests.
//
// Only one real gateway ships, in internal/payment/payfast. The split exists so
// that adding a second is a documented extension point rather than a fork, and
// so the handler tests never talk to a payment provider — see fake.go and
// CONTRIBUTING.md.
package payment

import "context"

// Request is an order presented to a gateway for payment. Amounts are integer
// cents, as everywhere else in this project: a float total rounded differently
// from a gateway's amount string is a real and hard-to-find class of bug.
type Request struct {
	// OrderID is this store's own order id. A gateway echoes it back on the
	// callback, and it is how the callback finds the order again.
	OrderID     string
	AmountCents int64
	Currency    string
	// ItemName is a one-line description of the purchase, shown on the
	// gateway's payment page and on the customer's statement.
	ItemName string

	NameFirst, NameLast, Email string
}

// Field is one form field. Insertion order is preserved because PayFast's
// signature is computed over the fields in the order they were submitted — not
// alphabetically — so anywhere a signature is computed or emitted this must be a
// slice and never a map.
type Field struct{ Name, Value string }

// Callback is an authenticated asynchronous notification from a gateway,
// normalised into this store's vocabulary.
type Callback struct {
	OrderID string // the gateway's echo of our order id
	Ref     string // the gateway's own payment id
	Status  string // the gateway's own status vocabulary, recorded verbatim
	Paid    bool   // Status normalised: the money is actually taken
	// Amount is the amount as received, kept as a string for the audit trail.
	// AmountCents is the same figure parsed, for comparing against the order.
	Amount      string
	AmountCents int64
	Raw         []byte // the callback body exactly as received, for disputes
}

// Gateway is everything the store needs from a payment provider.
//
// Implementing one is a small job on purpose. The parts that are genuinely
// difficult — proving a callback is real, keeping the order and stock consistent
// — are either inside the implementation or in the store, not spread across the
// handler.
type Gateway interface {
	// Name is the gateway's identifier, used in the callback route
	// (/payments/{gateway}/callback) and stored on the order.
	Name() string

	// BuildRedirectForm returns a POST target and the ordered hidden fields for
	// an auto-submitting form that sends the shopper to the gateway.
	BuildRedirectForm(Request) (action string, fields []Field, err error)

	// FormActionOrigin is the origin the redirect form posts to, for the
	// Content-Security-Policy's form-action directive. A gateway that does not
	// report it correctly has its redirect blocked by the browser, which is why
	// this is part of the interface rather than a configuration value somebody
	// has to remember to set.
	FormActionOrigin() string

	// ParseCallback parses and fully authenticates an asynchronous
	// notification. It returns an error unless the callback is provably genuine;
	// a Callback that comes back without one has been proven to come from the
	// gateway, and nothing beyond that.
	ParseCallback(ctx context.Context, body []byte, sourceIP string) (Callback, error)
}

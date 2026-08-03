// Package email sends the store's transactional mail: an order confirmation to
// the customer, and a copy to whoever has to pack the parcel.
//
// The Sender interface is the point of the package. Handler tests capture mail
// through Fake rather than talking to a mail server, and a deployment with no SMTP
// configured gets Discard, so an unconfigured mail server can never be the reason
// an order fails to record — see the invariant in ordersOnPaid: the order is marked
// paid *before* any mail is attempted, so a mail server having a bad afternoon
// cannot lose a sale.
package email

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Message is one email. Text is required and HTML is optional: a receipt has to
// arrive readable even in a client that refuses HTML, and a plain-text part is
// also what keeps a transactional message out of spam folders.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a Message. Implementations must be safe for concurrent use:
// two payment notifications can arrive at once.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Discard is the Sender for a deployment with no SMTP configured. It logs what it
// would have sent, at warning level, and reports success.
//
// Reporting success is deliberate. The alternative — an error — would make every
// paid order log a delivery failure for a store that simply has not set SMTP up,
// burying the failures that matter. The loud warning at startup is where that
// problem is meant to be noticed.
type Discard struct{ Log *slog.Logger }

func (d Discard) Send(_ context.Context, m Message) error {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("email not configured; discarding message", "to", m.To, "subject", m.Subject)
	return nil
}

// Fake captures messages instead of sending them. It backs every handler test
// that involves mail, and lives here next to the interface for the same reason
// payment.Fake does.
type Fake struct {
	// Err, when set, is returned by every Send, standing in for a mail server
	// that is refusing or unreachable.
	Err error

	mu   sync.Mutex
	sent []Message
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Send(_ context.Context, m Message) error {
	if f.Err != nil {
		return f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

// Sent returns every message handed to Send, in order.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// To returns the messages addressed to one recipient, which is what a test
// usually wants to assert on.
func (f *Fake) To(address string) []Message {
	var out []Message
	for _, m := range f.Sent() {
		if m.To == address {
			out = append(out, m)
		}
	}
	return out
}

// Validate checks a Message is sendable, so a template that produced nothing is
// caught here rather than as a puzzling rejection from a mail server.
func (m Message) Validate() error {
	switch {
	case m.To == "":
		return fmt.Errorf("email: message has no recipient")
	case m.Subject == "":
		return fmt.Errorf("email: message to %s has no subject", m.To)
	case m.Text == "":
		return fmt.Errorf("email: message to %s has no plain-text body", m.To)
	}
	return nil
}

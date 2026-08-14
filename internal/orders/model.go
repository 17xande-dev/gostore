// Package orders holds a placed order: what was bought, by whom, for how much,
// and what the payment gateway has said about it.
//
// An order is a *snapshot*, and that is the whole point of it existing separately
// from the cart. A cart holds quantities and prices everything live; an order
// records the title, options and unit price as they were at the moment of
// purchase, so editing the catalog afterwards — a price rise, a renamed product,
// a withdrawn variant — cannot rewrite what somebody bought. The separation is
// the one idea worth borrowing from every mature store: a mutable checkout
// becomes an immutable order at payment.
package orders

import (
	"strings"
	"time"
)

// Status is an order's lifecycle, kept deliberately short. There is no
// `refunded`, no `shipped`, no partial anything: this schema models a single
// forward payment, and adding a state is the moment to re-read what the plan says
// about scope.
type Status string

const (
	// StatusPending is an order created at checkout, before the gateway has said
	// anything. Most abandoned checkouts stay here forever, which is correct.
	StatusPending Status = "pending"
	// StatusPaid is set only by an authenticated gateway notification that says
	// the money is taken. Nothing a shopper's browser does can produce it.
	StatusPaid      Status = "paid"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Customer is who to send the goods to. There is no customer account and no
// customer table: this is a small shop selling physical things, and a name and an
// address on the order is what fulfilling one actually needs.
type Customer struct {
	Name    string
	Email   string
	Phone   string
	Address string
}

// FirstName and LastName split a single name field for gateways that insist on
// two. Splitting on the last space is crude and occasionally wrong for names it
// has no business having an opinion about — which is exactly why the store asks
// for one name and only splits at the boundary where a gateway forces it.
func (c Customer) FirstName() string {
	if i := strings.LastIndex(strings.TrimSpace(c.Name), " "); i > 0 {
		return strings.TrimSpace(c.Name)[:i]
	}
	return strings.TrimSpace(c.Name)
}

// LastName is whatever follows the last space, and empty for a single-word name.
func (c Customer) LastName() string {
	name := strings.TrimSpace(c.Name)
	if i := strings.LastIndex(name, " "); i > 0 {
		return strings.TrimSpace(name[i+1:])
	}
	return ""
}

// Item is one line of an order: a quantity of a variant, with everything needed
// to describe it copied in. The variant is still referenced, so stock can be
// decremented and the admin can follow the link, but nothing on this row is read
// through that reference.
type Item struct {
	// ID is the order_items row, which entitlements reference. Zero on an item
	// that has not been written yet.
	ID        int64
	VariantID string

	// Kind is 'physical' or 'digital', snapshotted at purchase like everything
	// else here. It decides two things after the fact: whether stock is
	// decremented — a download cannot run out — and whether payment mints a
	// download entitlement. Reading it from the product instead would let a
	// product flipped afterwards change how a completed sale behaved.
	Kind string

	Title string
	// VariantLabel is the variant's options as they read at purchase — "L / Navy",
	// "Hardcover" — rendered once when the order was created rather than stored as
	// three values and joined at read time. A product that later renames its option
	// slots therefore cannot relabel a completed sale, which is the same reason
	// Title and UnitPriceCents are copies rather than lookups.
	VariantLabel   string
	UnitPriceCents int64
	Quantity       int
}

// Label describes the item's options for display — "L / Navy", or "" for a
// product with no options. It is the snapshot verbatim: unlike a cart line, this
// must never be recomputed from the catalog as it stands now.
func (i Item) Label() string { return i.VariantLabel }

// LineTotalCents is what this line cost, at the price it was bought at.
func (i Item) LineTotalCents() int64 { return i.UnitPriceCents * int64(i.Quantity) }

// Order is one purchase.
type Order struct {
	ID string
	// CartID is the cart this came from, or "" once that cart has been cleaned
	// up. It is a breadcrumb, not a dependency: everything needed is on the
	// order.
	CartID string

	Customer   Customer
	TotalCents int64
	Currency   string
	Status     Status

	// The gateway columns are deliberately gateway-neutral, so adding a second
	// gateway is code and no migration. GatewayPayload is the notification body
	// exactly as received: the thing to reach for when a customer and a gateway
	// disagree about what happened.
	Gateway        string
	GatewayRef     string
	GatewayStatus  string
	GatewayAmount  string
	GatewayPayload string

	// Emailed records that the confirmation went out. It is separate from Status
	// on purpose: the order is marked paid *before* the email is attempted, so a
	// mail server having a bad afternoon can never lose a sale.
	Emailed bool

	// Oversold means this order was paid but its stock could not be decremented —
	// somebody else bought the last one between checkout and payment. The order
	// stands, because the money was taken; this needs a human to fulfil late or
	// refund. Set in the same transaction that records the payment, so an order is
	// never paid-but-unflagged.
	Oversold bool

	CreatedAt time.Time
	// PaidAt is zero until an authenticated notification says otherwise.
	PaidAt time.Time

	Items []Item
}

// Paid reports whether the money is in.
func (o Order) Paid() bool { return o.Status == StatusPaid }

// Count is the number of individual things ordered.
func (o Order) Count() int {
	n := 0
	for _, i := range o.Items {
		n += i.Quantity
	}
	return n
}

// Reference is the short, quotable form of the order id — what a customer reads
// off a confirmation page and puts in an email. The full UUID is unreasonable to
// read aloud; the first block of it is unique enough for a shop this size, and the
// admin can still be searched by the whole thing.
func (o Order) Reference() string {
	if i := strings.IndexByte(o.ID, '-'); i > 0 {
		return strings.ToUpper(o.ID[:i])
	}
	return strings.ToUpper(o.ID)
}

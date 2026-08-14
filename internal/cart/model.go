// Package cart holds an anonymous shopper's basket.
//
// A cart is a row in the database keyed by an opaque random token that is also
// the cookie value — not a signed cart carried in the cookie itself. Prices and
// stock are live server-side truth that has to be re-read on every render
// anyway, so reading the cart from the database is not extra work, it is the
// same work. The token grants nothing beyond one anonymous cart, so it needs to
// be unguessable rather than signed.
package cart

import (
	"fmt"
	"time"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// Cart is one shopper's basket, with its items priced from the catalog as it
// stands right now.
type Cart struct {
	ID        string // the opaque token, also the cookie value
	Items     []Item
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Item is a quantity of one variant, carrying the catalog details needed to show
// it. Nothing here is stored on the cart row: the title, price and stock are
// read live, so a price change or a sell-out is visible the next time the cart
// is looked at rather than at checkout.
type Item struct {
	VariantID string
	Quantity  int

	ProductSlug  string
	ProductTitle string
	// Kind is the product's, read live like the price. It decides whether this
	// line needs a delivery address; the order snapshots its own copy at purchase.
	Kind    string
	SKU     string
	Option1 string
	Option2 string
	Option3 string

	UnitPriceCents int64
	StockQty       int
	// Purchasable is false once the product or the variant has been
	// deactivated. Such an item stays in the cart and is shown as unavailable
	// rather than vanishing, because a line disappearing between page loads
	// looks like a bug or, worse, like a silent price change.
	Purchasable bool
}

// Label describes the item's options for display — "L / Navy", or "" for a
// product with no options. Shared with the storefront's rendering rather than
// reimplemented, so a cart line and a product page cannot disagree.
func (i Item) Label() string {
	return catalog.OptionLabel(i.Option1, i.Option2, i.Option3)
}

// LineTotalCents is the cost of this line at the current price.
func (i Item) LineTotalCents() int64 { return i.UnitPriceCents * int64(i.Quantity) }

// InStock reports whether the requested quantity is actually available.
// A download cannot run out, so it is always in stock however many are asked
// for. Everything below that reports a shortage goes through this.
func (i Item) InStock() bool {
	return catalog.Kind(i.Kind).Digital() || i.StockQty >= i.Quantity
}

// Available reports whether this line could be bought as it stands.
func (i Item) Available() bool { return i.Purchasable && i.InStock() }

// TotalCents is the cart total. Unavailable lines are included: hiding them from
// the total would show a figure that does not match what is on the page.
func (c Cart) TotalCents() int64 {
	var total int64
	for _, i := range c.Items {
		total += i.LineTotalCents()
	}
	return total
}

// Count is the number of individual things in the cart, which is what a badge
// showing "3" means to a shopper.
func (c Cart) Count() int {
	n := 0
	for _, i := range c.Items {
		n += i.Quantity
	}
	return n
}

// NeedsShipping reports whether anything in the cart has to be posted, which is
// what decides whether checkout asks for an address.
//
// True for a mixed cart, and that is the only sensible answer: one parcel in a
// basket of downloads still has to go somewhere. An empty cart needs no address,
// but it also cannot be checked out, so that branch never matters.
func (c Cart) NeedsShipping() bool {
	for _, i := range c.Items {
		if !catalog.Kind(i.Kind).Digital() {
			return true
		}
	}
	return false
}

// Digital reports whether the cart is downloads only, which is what a page saying
// "nothing to post" checks.
func (c Cart) Digital() bool { return !c.Empty() && !c.NeedsShipping() }

// Empty reports whether there is nothing in the cart.
func (c Cart) Empty() bool { return len(c.Items) == 0 }

// Purchasable reports whether every line can be bought right now, which is what
// checkout will require.
func (c Cart) Purchasable() bool {
	if c.Empty() {
		return false
	}
	for _, i := range c.Items {
		if !i.Available() {
			return false
		}
	}
	return true
}

// Problems lists what stands between this cart and a checkout, in the shopper's
// terms rather than the code's.
func (c Cart) Problems() []string {
	var problems []string
	for _, i := range c.Items {
		switch {
		case !i.Purchasable:
			problems = append(problems, i.ProductTitle+" is no longer for sale.")
		case i.InStock():
			// Nothing to say. A download is always in stock, so this is also the
			// branch every digital line takes.
		case i.StockQty == 0:
			problems = append(problems, i.ProductTitle+" is sold out.")
		default:
			problems = append(problems, fmt.Sprintf("%s only has %d left.", i.ProductTitle, i.StockQty))
		}
	}
	return problems
}

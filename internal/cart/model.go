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
	SKU          string
	Option1      string
	Option2      string
	Option3      string

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
func (i Item) InStock() bool { return i.StockQty >= i.Quantity }

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
		case i.StockQty == 0:
			problems = append(problems, i.ProductTitle+" is sold out.")
		case !i.InStock():
			problems = append(problems, fmt.Sprintf("%s only has %d left.", i.ProductTitle, i.StockQty))
		}
	}
	return problems
}

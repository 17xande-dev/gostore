// Package catalog owns products and their variants: the priced, stocked things
// a customer can actually buy.
//
// A variant is the purchasable unit. A single-edition book still has exactly
// one variant (size and colour both empty), so cart, order and stock code never
// branches on "has options versus not".
package catalog

import (
	"strings"
	"time"
)

// Product is one catalog entry. Variants is populated by the store methods that
// say so; it is nil otherwise rather than empty, so "not loaded" and "no
// variants" stay distinguishable at the call site.
type Product struct {
	ID          string    `json:"id,omitempty"`
	Kind        string    `json:"kind"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ImageURL    string    `json:"image_url"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
	Variants    []Variant `json:"variants,omitempty"`
}

// Variant is a purchasable, priced, stocked row under a product.
type Variant struct {
	ID         string `json:"id,omitempty"`
	ProductID  string `json:"-"`
	SKU        string `json:"sku"`
	Size       string `json:"size"`
	Color      string `json:"color"`
	PriceCents int64  `json:"price_cents"`
	StockQty   int    `json:"stock_qty"`
	Active     bool   `json:"active"`
}

// Label describes the variant's options for display — "Large / Navy", "Large",
// or "" when the product has no options at all.
func (v Variant) Label() string {
	parts := make([]string, 0, 2)
	if v.Size != "" {
		parts = append(parts, v.Size)
	}
	if v.Color != "" {
		parts = append(parts, v.Color)
	}
	return strings.Join(parts, " / ")
}

// InStock reports whether this variant can be sold right now.
func (v Variant) InStock() bool { return v.StockQty > 0 }

// TotalStock sums stock across the product's loaded variants.
func (p Product) TotalStock() int {
	total := 0
	for _, v := range p.Variants {
		total += v.StockQty
	}
	return total
}

// Slugify derives a URL-safe slug from a title, so leaving the slug field blank
// in the admin or a seed file is a reasonable thing to do.
func Slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastHyphen := true // leading hyphens are suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

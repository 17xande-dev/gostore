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
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
	Variants    []Variant `json:"variants,omitempty"`

	// ImageKey names the object holding this product's picture — a bucket key, or a
	// path relative to IMAGE_DIR. Empty means no image.
	//
	// The key is the only thing stored. The URL it is served at depends on which
	// backend is configured and is computed when a page is rendered, by the `image`
	// template function: the same row therefore works on a development machine
	// serving from a directory and in production serving from a bucket, with no data
	// to migrate when that changes.
	//
	// An image is always bytes this store holds. There is no way to point a product
	// at a URL on somebody else's server, because those bytes can change or vanish
	// and a product page with a broken picture is worse than one with none. Not
	// settable from JSON, so a seed file cannot claim an image it never uploaded.
	ImageKey string `json:"-"`
}

// HasImage reports whether the product has a picture to show.
func (p Product) HasImage() bool { return p.ImageKey != "" }

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

// InStock reports whether any of the product's loaded variants can be sold.
func (p Product) InStock() bool {
	for _, v := range p.Variants {
		if v.InStock() {
			return true
		}
	}
	return false
}

// MinPriceCents and MaxPriceCents bracket the product's loaded variants, so a
// listing can show one price or a range without the template doing arithmetic.
// Both are 0 for a product with no variants loaded.
func (p Product) MinPriceCents() int64 {
	if len(p.Variants) == 0 {
		return 0
	}
	min := p.Variants[0].PriceCents
	for _, v := range p.Variants[1:] {
		if v.PriceCents < min {
			min = v.PriceCents
		}
	}
	return min
}

func (p Product) MaxPriceCents() int64 {
	var max int64
	for _, v := range p.Variants {
		if v.PriceCents > max {
			max = v.PriceCents
		}
	}
	return max
}

// OnePrice reports whether every variant costs the same, which is what decides
// between showing a price and showing a range.
func (p Product) OnePrice() bool { return p.MinPriceCents() == p.MaxPriceCents() }

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

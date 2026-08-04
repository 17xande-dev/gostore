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
	ID          string `json:"id,omitempty"`
	Kind        string `json:"kind"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// ImageURL is where the image is served from — a bucket URL, or a same-origin
	// /images/ path when storage is a local directory. It is never a URL somebody
	// typed: an image is always bytes this store holds, because a product page with
	// a broken picture is worse than one with none, and a stranger's server is not
	// somewhere to keep a shop's photographs.
	//
	// Not settable from JSON, so a seed file cannot point a product at bytes it
	// never uploaded.
	ImageURL  string    `json:"-"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
	Variants  []Variant `json:"variants,omitempty"`

	// ImageKey names the object that ImageURL serves — a bucket key, or a path
	// relative to IMAGE_DIR. It is what deletion needs, since a URL is not
	// something storage can be asked to remove.
	//
	// It is set whenever ImageURL is, for anything this store uploaded. A row with a
	// URL and no key is an image pasted in before that stopped being allowed; see
	// Product.HasForeignImage.
	ImageKey string `json:"-"`
}

// HasImage reports whether the product has a picture to show.
func (p Product) HasImage() bool { return p.ImageURL != "" }

// HasForeignImage reports a row left over from when the admin accepted a pasted
// URL: an image the store points at but does not hold, and cannot delete from
// storage because there is no object.
//
// It exists so the admin can say so and offer to clear it, rather than a migration
// silently blanking somebody's catalog. Nothing creates one of these any more.
func (p Product) HasForeignImage() bool { return p.ImageURL != "" && p.ImageKey == "" }

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

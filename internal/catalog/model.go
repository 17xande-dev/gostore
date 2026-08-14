// Package catalog owns products and their variants: the priced, stocked things
// a customer can actually buy.
//
// A variant is the purchasable unit. A single-edition book still has exactly
// one variant (size and colour both empty), so cart, order and stock code never
// branches on "has options versus not".
package catalog

import (
	"slices"
	"strings"
	"time"
)

// Category is one entry in the shop's taxonomy: a row rather than a string on
// the product, so a product can be in several and renaming one does not have to
// find every product that said it.
//
// Slug is the public URL parameter, and Position is the display order — sorting
// by Name would put "Apparel" ahead of "Books" for ever, and the order things are
// offered in is the shop owner's decision, not the alphabet's.
type Category struct {
	ID       string `json:"-"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// Product is one catalog entry. Variants is populated by the store methods that
// say so; it is nil otherwise rather than empty, so "not loaded" and "no
// variants" stay distinguishable at the call site. Categories works the same way,
// and is attached only where it is read: the admin needs it, storefront cards do
// not render it, so the hot path does not pay for a second query.
type Product struct {
	ID          string     `json:"id,omitempty"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"-"`
	UpdatedAt   time.Time  `json:"-"`
	Variants    []Variant  `json:"variants,omitempty"`
	Categories  []Category `json:"-"`

	// CategorySlugs is how a seed file names the categories a product belongs to,
	// because a fixture has no ids to refer to. It is input only: everything that
	// reads a product back reads Categories, which carries the rows as stored.
	CategorySlugs []string `json:"categories,omitempty"`

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

	// Option1Name..Option3Name name this product's variant options — "Size" and
	// "Colour" for a t-shirt, "Cover" for a book, "Format" for a recording. The
	// values live on the variants; these are only the headings.
	//
	// They are declared per product rather than per category on purpose. Categories
	// are many-to-many so that a book can also be a gift, so structure hung off them
	// would give that product two contradictory answers — and adding a "Sale"
	// category for a promotion would change what a variant is.
	//
	// Empty means the slot is unused, and slots fill in order.
	Option1Name string `json:"option1_name,omitempty"`
	Option2Name string `json:"option2_name,omitempty"`
	Option3Name string `json:"option3_name,omitempty"`
}

// HasImage reports whether the product has a picture to show.
func (p Product) HasImage() bool { return p.ImageKey != "" }

// OptionNames returns the declared names in slot order, including empties, so a
// caller can line them up against a variant's values by index.
func (p Product) OptionNames() [OptionSlots]string {
	return [OptionSlots]string{p.Option1Name, p.Option2Name, p.Option3Name}
}

// VariantOption pairs an option name with one variant's value for it. Slot is
// 1-based and is what a form field is named after, so a template never has to
// derive "option2" from a loop index.
type VariantOption struct {
	Slot  int
	Name  string
	Value string
}

// OptionsFor lines a variant's values up against this product's names, skipping
// slots the product does not use. It is what the admin form and the storefront
// selector iterate over, so neither has to know that three is the number.
func (p Product) OptionsFor(v Variant) []VariantOption {
	return p.optionsWith(v.Options())
}

// OptionsWith is OptionsFor against loose values rather than a saved variant, for
// re-rendering a form whose submission was rejected.
func (p Product) OptionsWith(values [OptionSlots]string) []VariantOption {
	return p.optionsWith(values)
}

func (p Product) optionsWith(values [OptionSlots]string) []VariantOption {
	out := make([]VariantOption, 0, OptionSlots)
	for i, name := range p.OptionNames() {
		if name == "" {
			continue
		}
		out = append(out, VariantOption{Slot: i + 1, Name: name, Value: values[i]})
	}
	return out
}

// HasOptions reports whether the product declares any variant options at all,
// which is what decides between a variant selector and a bare add-to-cart button.
func (p Product) HasOptions() bool { return p.Option1Name != "" }

// OptionHeading labels the variant selector in the shopper's terms — "Size /
// Colour", "Cover", "Format" — falling back to "Option" for a product whose
// variants differ without anybody having said how.
func (p Product) OptionHeading() string {
	names := p.OptionNames()
	if h := OptionLabel(names[:]...); h != "" {
		return h
	}
	return "Option"
}

// InCategory reports whether the product's loaded categories include one, which
// is what decides whether a checkbox on the admin form is ticked.
func (p Product) InCategory(id string) bool {
	return slices.ContainsFunc(p.Categories, func(c Category) bool { return c.ID == id })
}

// CategoryNames lists the product's categories for display, in the order they
// were loaded.
func (p Product) CategoryNames() []string {
	names := make([]string, 0, len(p.Categories))
	for _, c := range p.Categories {
		names = append(names, c.Name)
	}
	return names
}

// TitleFromSlug turns a slug into a presentable name — "gift-cards" into "Gift
// Cards". It exists for seeding, where a fixture names a category by slug alone
// and something has to go in the name column; a category the operator has since
// renamed keeps its name, because seeding never overwrites one.
func TitleFromSlug(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

// OptionSlots is how many option values a variant can carry, and therefore how
// many names a product can declare. Three is where Shopify landed after a decade
// of selling other people's inventory, and it comfortably covers the cases here:
// size and colour for apparel, one slot for a book's cover or a recording's
// format, and one spare.
const OptionSlots = 3

// Variant is a purchasable, priced, stocked row under a product.
//
// Option1..3 are the *values* for the option names declared on the product:
// "L"/"Navy" under Size/Colour, "Hardcover" under Cover, "Audio" under Format.
// A product with no options at all has one variant with all three empty, which
// is what keeps cart, order and stock code free of a "has options or not" branch.
type Variant struct {
	ID         string `json:"id,omitempty"`
	ProductID  string `json:"-"`
	SKU        string `json:"sku"`
	Option1    string `json:"option1"`
	Option2    string `json:"option2"`
	Option3    string `json:"option3"`
	PriceCents int64  `json:"price_cents"`
	StockQty   int    `json:"stock_qty"`
	Active     bool   `json:"active"`
}

// Options returns the variant's three values in slot order, including empties, so
// a caller that needs to line them up against the product's names can index them.
func (v Variant) Options() [OptionSlots]string {
	return [OptionSlots]string{v.Option1, v.Option2, v.Option3}
}

// Label describes the variant's options for display — "L / Navy", "Hardcover",
// or "" when the product has no options at all.
func (v Variant) Label() string {
	opts := v.Options()
	return OptionLabel(opts[:]...)
}

// OptionLabel joins the non-empty option values for display. It lives here, and
// is exported, because a variant's options are rendered in three places — the
// storefront, the cart, and the snapshot written into an order — and three
// independent implementations is how "L / Navy" becomes "L/Navy" on one page.
func OptionLabel(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			parts = append(parts, v)
		}
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

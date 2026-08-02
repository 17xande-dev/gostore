// Package validate holds form validation: a field-keyed error map plus one
// function per form, so handlers stay about HTTP and templates can render an
// error next to the input that caused it.
package validate

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// FormErrors maps a form field name to a single message. One message per field
// is deliberate: a form that lists three complaints about the same input is
// harder to act on than the first one.
type FormErrors map[string]string

// Add records a message unless the field already has one.
func (e FormErrors) Add(field, msg string) {
	if _, seen := e[field]; !seen {
		e[field] = msg
	}
}

// Any reports whether the form failed validation.
func (e FormErrors) Any() bool { return len(e) > 0 }

// String renders every message in field order, for logs and command-line tools
// that have no form to render them into.
func (e FormErrors) String() string {
	fields := make([]string, 0, len(e))
	for field := range e {
		fields = append(fields, field)
	}
	slices.Sort(fields)

	var b strings.Builder
	for i, field := range fields {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(field)
		b.WriteString(": ")
		b.WriteString(e[field])
	}
	return b.String()
}

// Product validates a product as submitted by the admin form. The slug is
// checked strictly because it is a public URL, and changing it later breaks
// links that already exist.
func Product(p catalog.Product) FormErrors {
	e := FormErrors{}

	required(e, "title", p.Title)
	maxLen(e, "title", p.Title, 200)
	required(e, "kind", p.Kind)
	maxLen(e, "kind", p.Kind, 50)
	maxLen(e, "description", p.Description, 10_000)
	maxLen(e, "image_url", p.ImageURL, 2_000)

	switch {
	case p.Slug == "":
		e.Add("slug", "Required.")
	case p.Slug != catalog.Slugify(p.Slug):
		e.Add("slug", "Use lowercase letters, numbers and hyphens only.")
	default:
		maxLen(e, "slug", p.Slug, 200)
	}

	if p.ImageURL != "" && !isHTTPURL(p.ImageURL) {
		e.Add("image_url", "Must start with http:// or https://.")
	}
	return e
}

// Variant validates a variant as submitted by the admin form. Price and stock
// arrive as text, so their parse failures are reported by the caller against
// the same field names used here.
func Variant(v catalog.Variant) FormErrors {
	e := FormErrors{}

	required(e, "sku", v.SKU)
	maxLen(e, "sku", v.SKU, 100)
	maxLen(e, "size", v.Size, 100)
	maxLen(e, "color", v.Color, 100)

	if v.PriceCents < 0 {
		e.Add("price", "Cannot be negative.")
	}
	if v.StockQty < 0 {
		e.Add("stock_qty", "Cannot be negative.")
	}
	return e
}

func required(e FormErrors, field, value string) {
	if strings.TrimSpace(value) == "" {
		e.Add(field, "Required.")
	}
}

func maxLen(e FormErrors, field, value string, max int) {
	if utf8.RuneCountInString(value) > max {
		e.Add(field, "Too long.")
	}
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

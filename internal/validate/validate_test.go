package validate

import (
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

func TestFormErrors(t *testing.T) {
	e := FormErrors{}
	if e.Any() {
		t.Error("an empty FormErrors reports errors")
	}

	e.Add("title", "Required.")
	e.Add("title", "Something else.")
	if got := e["title"]; got != "Required." {
		t.Errorf("title = %q, want the first message to win", got)
	}

	e.Add("slug", "Bad.")
	if got, want := e.String(), "slug: Bad.; title: Required."; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestProduct(t *testing.T) {
	valid := catalog.Product{
		Kind: "book", Slug: "a-book", Title: "A Book",
		Description: "Fine.", ImageURL: "https://example.com/a.png",
	}
	if errs := Product(valid); errs.Any() {
		t.Errorf("a valid product was rejected: %s", errs)
	}

	cases := []struct {
		name  string
		mut   func(*catalog.Product)
		field string
	}{
		{"no title", func(p *catalog.Product) { p.Title = "  " }, "title"},
		{"no kind", func(p *catalog.Product) { p.Kind = "" }, "kind"},
		{"no slug", func(p *catalog.Product) { p.Slug = "" }, "slug"},
		{"slug with spaces", func(p *catalog.Product) { p.Slug = "a book" }, "slug"},
		{"slug in capitals", func(p *catalog.Product) { p.Slug = "A-Book" }, "slug"},
		{"long title", func(p *catalog.Product) { p.Title = strings.Repeat("x", 201) }, "title"},
	}
	for _, tc := range cases {
		p := valid
		tc.mut(&p)
		errs := Product(p)
		if _, ok := errs[tc.field]; !ok {
			t.Errorf("%s: no error on %q, got %s", tc.name, tc.field, errs)
		}
	}

	// The image is deliberately not validated here any more. It is not form input:
	// an image arrives by upload and its URL is whatever storage says it is, so
	// there is nothing a shop operator could type wrongly. Anything already in
	// ImageURL passes, including a value no form could have produced.
	for _, url := range []string{"/images/products/a/b.jpg", "https://images.example/a.jpg", "nonsense"} {
		p := valid
		p.ImageURL = url
		if errs := Product(p); errs.Any() {
			t.Errorf("ImageURL %q produced form errors %s", url, errs)
		}
	}
}

func TestVariant(t *testing.T) {
	valid := catalog.Variant{SKU: "TEE-M", Size: "M", Color: "Black", PriceCents: 100, StockQty: 1}
	if errs := Variant(valid); errs.Any() {
		t.Errorf("a valid variant was rejected: %s", errs)
	}

	// A variant with no options at all is the normal case for a book, not an
	// error: exactly one such variant is what makes cart and stock code
	// branch-free.
	plain := catalog.Variant{SKU: "BOOK-1", PriceCents: 24900}
	if errs := Variant(plain); errs.Any() {
		t.Errorf("an optionless variant was rejected: %s", errs)
	}

	cases := []struct {
		name  string
		mut   func(*catalog.Variant)
		field string
	}{
		{"no sku", func(v *catalog.Variant) { v.SKU = "" }, "sku"},
		{"negative price", func(v *catalog.Variant) { v.PriceCents = -1 }, "price"},
		{"negative stock", func(v *catalog.Variant) { v.StockQty = -1 }, "stock_qty"},
	}
	for _, tc := range cases {
		v := valid
		tc.mut(&v)
		errs := Variant(v)
		if _, ok := errs[tc.field]; !ok {
			t.Errorf("%s: no error on %q, got %s", tc.name, tc.field, errs)
		}
	}
}

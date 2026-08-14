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
		Slug: "a-book", Title: "A Book", Description: "Fine.",
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
		{"category slug not a slug", func(p *catalog.Product) { p.CategorySlugs = []string{"Gift Cards"} }, "categories"},
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

	// The image is deliberately not validated here any more, and there is no longer
	// even a URL to validate: a product stores only the storage key of something it
	// uploaded, and the key is generated rather than typed.
	for _, key := range []string{"products/a/b.jpg", "", "anything at all"} {
		p := valid
		p.ImageKey = key
		if errs := Product(p); errs.Any() {
			t.Errorf("ImageKey %q produced form errors %s", key, errs)
		}
	}
}

func TestVariant(t *testing.T) {
	valid := catalog.Variant{SKU: "TEE-M", Option1: "M", Option2: "Black", PriceCents: 100, StockQty: 1}
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
		{"overlong option 1", func(v *catalog.Variant) { v.Option1 = strings.Repeat("x", 101) }, "option1"},
		{"overlong option 3", func(v *catalog.Variant) { v.Option3 = strings.Repeat("x", 101) }, "option3"},
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

func TestProductOptionNames(t *testing.T) {
	base := catalog.Product{Slug: "tee", Title: "Tee"}

	ok := []struct {
		name    string
		options [3]string
	}{
		{"none at all", [3]string{}},
		{"one", [3]string{"Format", "", ""}},
		{"two", [3]string{"Size", "Colour", ""}},
		{"all three", [3]string{"Size", "Colour", "Material"}},
	}
	for _, tc := range ok {
		p := base
		p.Option1Name, p.Option2Name, p.Option3Name = tc.options[0], tc.options[1], tc.options[2]
		if errs := Product(p); errs.Any() {
			t.Errorf("%s: valid option names were rejected: %s", tc.name, errs)
		}
	}

	bad := []struct {
		name    string
		options [3]string
		field   string
	}{
		// Values are matched to names by position, so a hole would leave every
		// variant's first value with no heading above it.
		{"gap in slot 1", [3]string{"", "Colour", ""}, "option2_name"},
		{"gap in slot 2", [3]string{"Size", "", "Material"}, "option3_name"},
		// Two headings reading the same makes the variants under them
		// indistinguishable, and the comparison is deliberately case-insensitive.
		{"duplicate", [3]string{"Size", "Size", ""}, "option2_name"},
		{"duplicate in another case", [3]string{"Size", "SIZE", ""}, "option2_name"},
		{"overlong", [3]string{strings.Repeat("x", 101), "", ""}, "option1_name"},
	}
	for _, tc := range bad {
		p := base
		p.Option1Name, p.Option2Name, p.Option3Name = tc.options[0], tc.options[1], tc.options[2]
		errs := Product(p)
		if _, ok := errs[tc.field]; !ok {
			t.Errorf("%s: no error on %q, got %s", tc.name, tc.field, errs)
		}
	}
}

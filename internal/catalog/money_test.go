package catalog

import (
	"slices"
	"testing"
)

func TestParsePrice(t *testing.T) {
	valid := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"149", 14900},
		{"149.9", 14990},
		{"149.99", 14999},
		{"1,149.99", 114999},
		{" 149.99 ", 14999},
		{"0.05", 5},
	}
	for _, tc := range valid {
		got, err := ParsePrice(tc.in)
		if err != nil {
			t.Errorf("ParsePrice(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePrice(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	// Three decimals is the interesting one: silently rounding a price the
	// operator typed is worse than refusing it.
	invalid := []string{"", "  ", "abc", "-5", "1.999", "1.", ".99", "R149.99", "1 2 . 3 4x"}
	for _, in := range invalid {
		if got, err := ParsePrice(in); err == nil {
			t.Errorf("ParsePrice(%q) = %d, want an error", in, got)
		}
	}
}

func TestFormatPrice(t *testing.T) {
	cases := map[int64]string{
		0:      "0.00",
		5:      "0.05",
		50:     "0.50",
		14900:  "149.00",
		114999: "1149.99",
	}
	for cents, want := range cases {
		if got := FormatPrice(cents); got != want {
			t.Errorf("FormatPrice(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestPriceRoundTrips(t *testing.T) {
	for _, cents := range []int64{0, 1, 99, 100, 14999, 999_999_99} {
		got, err := ParsePrice(FormatPrice(cents))
		if err != nil {
			t.Fatalf("ParsePrice(FormatPrice(%d)): %v", cents, err)
		}
		if got != cents {
			t.Errorf("round trip of %d gave %d", cents, got)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"The Quiet Machine":     "the-quiet-machine",
		"  Spaced  Out  ":       "spaced-out",
		"Ampersands & Symbols!": "ampersands-symbols",
		"Already-a-slug":        "already-a-slug",
		"Numbers 123":           "numbers-123",
		"!!!":                   "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProductPricingHelpers(t *testing.T) {
	// No variants loaded: the helpers must not panic or invent a price.
	var bare Product
	if bare.MinPriceCents() != 0 || bare.MaxPriceCents() != 0 || bare.InStock() || bare.TotalStock() != 0 {
		t.Error("a product with no variants does not read as empty")
	}
	if !bare.OnePrice() {
		t.Error("OnePrice() should be trivially true with nothing to compare")
	}

	one := Product{Variants: []Variant{{PriceCents: 24900, StockQty: 3}}}
	if one.MinPriceCents() != 24900 || one.MaxPriceCents() != 24900 || !one.OnePrice() {
		t.Errorf("single variant: %d–%d, OnePrice=%v", one.MinPriceCents(), one.MaxPriceCents(), one.OnePrice())
	}

	// The lowest price is not the first row, so a helper that returns
	// Variants[0] would pass a laxer test than this one.
	many := Product{Variants: []Variant{
		{PriceCents: 31900, StockQty: 0},
		{PriceCents: 19900, StockQty: 0},
		{PriceCents: 34900, StockQty: 2},
	}}
	if many.MinPriceCents() != 19900 {
		t.Errorf("MinPriceCents = %d, want 19900", many.MinPriceCents())
	}
	if many.MaxPriceCents() != 34900 {
		t.Errorf("MaxPriceCents = %d, want 34900", many.MaxPriceCents())
	}
	if many.OnePrice() {
		t.Error("OnePrice() is true across three different prices")
	}
	if !many.InStock() {
		t.Error("InStock() is false although the last variant has stock")
	}
	if many.TotalStock() != 2 {
		t.Errorf("TotalStock = %d, want 2", many.TotalStock())
	}

	soldOut := Product{Variants: []Variant{{PriceCents: 100}, {PriceCents: 100}}}
	if soldOut.InStock() {
		t.Error("InStock() is true with every variant at zero")
	}
	if !soldOut.OnePrice() {
		t.Error("OnePrice() is false for two identically priced variants")
	}
}

func TestVariantLabel(t *testing.T) {
	cases := []struct {
		v    Variant
		want string
	}{
		{Variant{}, ""},
		{Variant{Option1: "L"}, "L"},
		{Variant{Option2: "Navy"}, "Navy"},
		{Variant{Option1: "L", Option2: "Navy"}, "L / Navy"},
		{Variant{Option1: "L", Option3: "Cotton"}, "L / Cotton"},
		{Variant{Option1: "Audio"}, "Audio"},
	}
	for _, tc := range cases {
		if got := tc.v.Label(); got != tc.want {
			t.Errorf("Label(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestProductOptionsFor(t *testing.T) {
	// Values are matched to names by position, so the slot number a form field is
	// named after has to survive the skipping of unused slots. A product using
	// slots 1 and 2 must not renumber them 0 and 1.
	p := Product{Option1Name: "Size", Option2Name: "Colour"}
	v := Variant{Option1: "L", Option2: "Navy", Option3: "ignored"}

	got := p.OptionsFor(v)
	want := []VariantOption{
		{Slot: 1, Name: "Size", Value: "L"},
		{Slot: 2, Name: "Colour", Value: "Navy"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("OptionsFor() = %+v, want %+v", got, want)
	}
}

func TestProductOptionsFor_UnusedSlotsAreSkipped(t *testing.T) {
	// A variant carrying a value in a slot the product does not name is not an
	// error and is not rendered: the name is what makes a value mean anything, so
	// a leftover from an earlier configuration simply does not appear.
	p := Product{Option1Name: "Format"}
	got := p.OptionsFor(Variant{Option1: "Audio", Option2: "leftover"})
	if len(got) != 1 || got[0].Value != "Audio" {
		t.Errorf("OptionsFor() = %+v, want just the named slot", got)
	}
}

func TestProductOptionHeading(t *testing.T) {
	cases := []struct {
		p    Product
		want string
	}{
		{Product{}, "Option"},
		{Product{Option1Name: "Cover"}, "Cover"},
		{Product{Option1Name: "Size", Option2Name: "Colour"}, "Size / Colour"},
	}
	for _, tc := range cases {
		if got := tc.p.OptionHeading(); got != tc.want {
			t.Errorf("OptionHeading(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestProductHasOptions(t *testing.T) {
	if (Product{}).HasOptions() {
		t.Error("a product declaring no option names reports HasOptions()")
	}
	if !(Product{Option1Name: "Format"}).HasOptions() {
		t.Error("a product declaring an option name does not report HasOptions()")
	}
}

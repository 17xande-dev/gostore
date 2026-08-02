package main

import "testing"

// The shipped fixture is the first thing a new adopter runs, so a typo in it
// must fail here rather than at their first `make seed`.
func TestReadProducts_ShippedFixtureIsValid(t *testing.T) {
	products, err := readProducts("../../testdata/products.json")
	if err != nil {
		t.Fatalf("readProducts: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("the fixture contains no products")
	}

	slugs := map[string]bool{}
	skus := map[string]bool{}
	for _, p := range products {
		if slugs[p.Slug] {
			t.Errorf("duplicate slug %q", p.Slug)
		}
		slugs[p.Slug] = true

		// A product with no variants cannot be bought, which would make the
		// demo catalog look broken.
		if len(p.Variants) == 0 {
			t.Errorf("%s has no variants", p.Slug)
		}
		for _, v := range p.Variants {
			if skus[v.SKU] {
				t.Errorf("duplicate SKU %q", v.SKU)
			}
			skus[v.SKU] = true
			if v.PriceCents <= 0 {
				t.Errorf("%s: variant %s has price %d", p.Slug, v.SKU, v.PriceCents)
			}
		}
	}
}

func TestReadProducts_RejectsAMissingFile(t *testing.T) {
	if _, err := readProducts("testdata/does-not-exist.json"); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

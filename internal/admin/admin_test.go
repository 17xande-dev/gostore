package admin

import (
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

func TestPrepareProduct_DerivesSlugAndResolvesCategories(t *testing.T) {
	known := []catalog.Category{{ID: "c1", Slug: "mugs", Name: "Mugs"}}
	p, errs := PrepareProduct(catalog.Product{Title: "  Blue Mug ", Kind: " physical "}, []string{"c1"}, known)
	if errs.Any() {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if p.Title != "Blue Mug" || p.Slug != "blue-mug" || p.Kind != catalog.KindPhysical {
		t.Fatalf("not normalised: %+v", p)
	}
	if len(p.Categories) != 1 || p.Categories[0].ID != "c1" {
		t.Fatalf("categories = %+v", p.Categories)
	}
}

func TestPrepareProduct_UnknownCategoryIsAFieldError(t *testing.T) {
	_, errs := PrepareProduct(catalog.Product{Title: "Mug", Kind: catalog.KindPhysical}, []string{"nope"}, nil)
	if !errs.Any() {
		t.Fatal("an unknown category id was accepted")
	}
}

func TestPrepareVariant_ValidatesAfterTrimming(t *testing.T) {
	if _, errs := PrepareVariant(catalog.Variant{SKU: "   ", PriceCents: 100}); !errs.Any() {
		t.Fatal("a blank SKU was accepted")
	}
	v, errs := PrepareVariant(catalog.Variant{SKU: " MUG-1 ", PriceCents: 100})
	if errs.Any() || v.SKU != "MUG-1" {
		t.Fatalf("v = %+v, errs = %v", v, errs)
	}
}

func TestProductWriteErrors(t *testing.T) {
	if errs, ok := ProductWriteErrors(&catalog.ConflictError{Field: "slug"}); !ok || errs["slug"] == "" {
		t.Fatalf("conflict: %v %v", errs, ok)
	}
	if errs, ok := ProductWriteErrors(&catalog.KindLockedError{Ordered: true}); !ok || errs["kind"] == "" {
		t.Fatalf("kind lock: %v %v", errs, ok)
	}
	if _, ok := ProductWriteErrors(catalog.ErrNotFound); ok {
		t.Fatal("a genuine failure was treated as a field error")
	}
}

func TestCheckOrderSearch(t *testing.T) {
	for _, c := range []struct {
		search, filter string
		page           int
		ok             bool
	}{
		{"", "", 1, true},
		{"alice", "unfulfilled", 3, true},
		{"", "shipped", 1, false},
		{"", "", 0, false},
		{string(make([]byte, 201)), "", 1, false},
	} {
		if err := CheckOrderSearch(c.search, c.filter, c.page); (err == nil) != c.ok {
			t.Errorf("CheckOrderSearch(%q, %q, %d) = %v", c.search, c.filter, c.page, err)
		}
	}
}

func TestCategoryDeleted(t *testing.T) {
	for n, want := range map[int64]string{
		0: "Category deleted. It was not used by any product.",
		1: "Category deleted, and removed from 1 product. The product itself is untouched.",
		3: "Category deleted, and removed from 3 products. The products themselves are untouched.",
	} {
		if got := CategoryDeleted(n); got != want {
			t.Errorf("CategoryDeleted(%d) = %q", n, got)
		}
	}
}

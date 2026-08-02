package catalog_test

import (
	"errors"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
)

func TestStore_CreateGetAndUpdate(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	created, err := s.Create(ctx, catalog.Product{
		Kind: "book", Slug: "a-book", Title: "A Book", Description: "d", Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned an empty id")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create returned a zero created_at")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "A Book" || got.Slug != "a-book" || !got.Active {
		t.Errorf("Get returned %+v", got)
	}
	if len(got.Variants) != 0 {
		t.Errorf("new product has %d variants, want 0", len(got.Variants))
	}

	got.Title = "A Better Book"
	got.Active = false
	updated, err := s.Update(ctx, got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "A Better Book" || updated.Active {
		t.Errorf("Update returned %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Error("Update did not move updated_at forward")
	}

	bySlug, err := s.GetBySlug(ctx, "a-book")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.ID != created.ID {
		t.Errorf("GetBySlug returned %s, want %s", bySlug.ID, created.ID)
	}
}

func TestStore_NotFound(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	// A well-formed id that does not exist, and a path parameter that is not a
	// UUID at all, must both be 404s rather than driver errors.
	for _, id := range []string{"3f2504e0-4f89-41d3-9a0c-0305e82c3301", "not-a-uuid", ""} {
		if _, err := s.Get(ctx, id); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
		}
		if err := s.Delete(ctx, id); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("Delete(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if _, err := s.GetBySlug(ctx, "nope"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("GetBySlug = %v, want ErrNotFound", err)
	}
}

func TestStore_DuplicateSlugIsAConflict(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := catalog.Product{Kind: "book", Slug: "taken", Title: "First", Active: true}
	if _, err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p.Title = "Second"
	_, err := s.Create(ctx, p)
	var conflict *catalog.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Create with a duplicate slug = %v, want a *ConflictError", err)
	}
	if conflict.Field != "slug" {
		t.Errorf("conflict field = %q, want slug", conflict.Field)
	}
}

func TestStore_Variants(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Kind: "apparel", Slug: "tee", Title: "Tee", Active: true})

	v, err := s.CreateVariant(ctx, catalog.Variant{
		ProductID: p.ID, SKU: "TEE-M-BLK", Size: "M", Color: "Black", PriceCents: 29900, StockQty: 7, Active: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	if v.ID == "" || v.ProductID != p.ID {
		t.Fatalf("CreateVariant returned %+v", v)
	}

	// A duplicate SKU and a duplicate size/colour pair are different mistakes
	// and must be reported against different fields.
	_, err = s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "TEE-M-BLK", Size: "L", PriceCents: 1, Active: true})
	var conflict *catalog.ConflictError
	if !errors.As(err, &conflict) || conflict.Field != "sku" {
		t.Errorf("duplicate SKU = %v, want a sku conflict", err)
	}
	_, err = s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "OTHER", Size: "M", Color: "Black", PriceCents: 1, Active: true})
	if !errors.As(err, &conflict) || conflict.Field != "options" {
		t.Errorf("duplicate size/colour = %v, want an options conflict", err)
	}

	v.StockQty = 2
	v.PriceCents = 31900
	if _, err := s.UpdateVariant(ctx, v); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Variants) != 1 {
		t.Fatalf("product has %d variants, want 1", len(got.Variants))
	}
	if got.Variants[0].StockQty != 2 || got.Variants[0].PriceCents != 31900 {
		t.Errorf("variant is %+v after update", got.Variants[0])
	}
	if got.TotalStock() != 2 {
		t.Errorf("TotalStock = %d, want 2", got.TotalStock())
	}

	// A variant id belonging to another product must not be editable through
	// this product's URL.
	other := mustCreate(t, s, catalog.Product{Kind: "book", Slug: "other", Title: "Other", Active: true})
	stray := v
	stray.ProductID = other.ID
	if _, err := s.UpdateVariant(ctx, stray); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("UpdateVariant across products = %v, want ErrNotFound", err)
	}
	if err := s.DeleteVariant(ctx, other.ID, v.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("DeleteVariant across products = %v, want ErrNotFound", err)
	}

	if err := s.DeleteVariant(ctx, p.ID, v.ID); err != nil {
		t.Fatalf("DeleteVariant: %v", err)
	}
	if vs, err := s.Variants(ctx, p.ID); err != nil || len(vs) != 0 {
		t.Errorf("Variants after delete = %v, %v", vs, err)
	}
}

func TestStore_DeleteCascadesToVariants(t *testing.T) {
	pool := dbtest.Pool(t)
	s := catalog.NewStore(pool)
	ctx := t.Context()

	p := mustCreate(t, s, catalog.Product{Kind: "book", Slug: "doomed", Title: "Doomed", Active: true})
	if _, err := s.CreateVariant(ctx, catalog.Variant{ProductID: p.ID, SKU: "D-1", PriceCents: 100, Active: true}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	if err := s.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var variants int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM product_variants").Scan(&variants); err != nil {
		t.Fatalf("count variants: %v", err)
	}
	if variants != 0 {
		t.Errorf("%d variants survived the product, want 0", variants)
	}
}

func TestStore_List(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	if products, err := s.List(ctx); err != nil || len(products) != 0 {
		t.Fatalf("List on an empty catalog = %v, %v", products, err)
	}

	b := mustCreate(t, s, catalog.Product{Kind: "book", Slug: "b", Title: "Beta", Active: true})
	a := mustCreate(t, s, catalog.Product{Kind: "book", Slug: "a", Title: "Alpha", Active: true})
	if _, err := s.CreateVariant(ctx, catalog.Variant{ProductID: b.ID, SKU: "B-1", PriceCents: 100, StockQty: 3, Active: true}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	products, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("List returned %d products, want 2", len(products))
	}
	if products[0].ID != a.ID || products[1].ID != b.ID {
		t.Error("List is not ordered by title")
	}
	// Variants must be attached to the right product, not to whichever came
	// back first.
	if len(products[0].Variants) != 0 {
		t.Errorf("Alpha has %d variants, want 0", len(products[0].Variants))
	}
	if len(products[1].Variants) != 1 || products[1].Variants[0].SKU != "B-1" {
		t.Errorf("Beta has variants %+v, want just B-1", products[1].Variants)
	}
}

func TestStore_UpsertIsRerunnable(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	p := catalog.Product{
		Kind: "apparel", Slug: "tote", Title: "Tote", Active: true,
		Variants: []catalog.Variant{
			{SKU: "TOTE-STD", Color: "Natural", PriceCents: 14900, StockQty: 20, Active: true},
		},
	}

	first, err := s.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Someone sells eleven of them.
	sold := first.Variants[0]
	sold.StockQty = 9
	if _, err := s.UpdateVariant(ctx, sold); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}

	p.Title = "Canvas Tote"
	second, err := s.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("Upsert created a second product (%s then %s)", first.ID, second.ID)
	}
	if second.Title != "Canvas Tote" {
		t.Errorf("Upsert did not update the title, got %q", second.Title)
	}

	products, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 1 || len(products[0].Variants) != 1 {
		t.Fatalf("catalog is %+v, want one product with one variant", products)
	}
	// Reseeding must not reset stock to the fixture's number: the fixture is a
	// starting point, not a source of truth about inventory.
	if got := products[0].Variants[0].StockQty; got != 9 {
		t.Errorf("stock is %d after reseeding, want 9", got)
	}
}

func mustCreate(t *testing.T, s *catalog.Store, p catalog.Product) catalog.Product {
	t.Helper()
	out, err := s.Create(t.Context(), p)
	if err != nil {
		t.Fatalf("Create %q: %v", p.Slug, err)
	}
	return out
}

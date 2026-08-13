package catalog_test

import (
	"errors"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/dbtest"
)

func TestCategories_CreateUpdateAndOrder(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	// Deliberately created out of display order, and with names whose alphabetical
	// order is the opposite of the positions: this is the whole reason `position`
	// exists, so a test that seeded them in order would prove nothing.
	apparel, err := s.CreateCategory(ctx, catalog.Category{Slug: "apparel", Name: "Apparel", Position: 2})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	books, err := s.CreateCategory(ctx, catalog.Category{Slug: "books", Name: "Books", Position: 1})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	got, err := s.Categories(ctx)
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(got) != 2 || got[0].ID != books.ID || got[1].ID != apparel.ID {
		t.Errorf("categories came back in %v order, want Books then Apparel", names(got))
	}

	// A duplicate slug is a form error against the slug field, not a 500.
	_, err = s.CreateCategory(ctx, catalog.Category{Slug: "books", Name: "More Books"})
	var conflict *catalog.ConflictError
	if !errors.As(err, &conflict) || conflict.Field != "slug" {
		t.Errorf("duplicate slug = %v, want a slug conflict", err)
	}

	books.Name = "Reading"
	books.Position = 5
	if _, err := s.UpdateCategory(ctx, books); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}
	reread, err := s.Category(ctx, books.ID)
	if err != nil {
		t.Fatalf("Category: %v", err)
	}
	if reread.Name != "Reading" || reread.Position != 5 {
		t.Errorf("after update: %+v", reread)
	}

	if _, err := s.Category(ctx, "not-a-uuid"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("Category(malformed id) = %v, want ErrNotFound", err)
	}
}

func TestCategories_ProductLinksAreReplacedOnSave(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	books := mustCategory(t, s, catalog.Category{Slug: "books", Name: "Books", Position: 1})
	gifts := mustCategory(t, s, catalog.Category{Slug: "gifts", Name: "Gifts", Position: 2})

	// In two at once, because that is the case a column on products could not
	// express and is the reason the join table exists.
	p, err := s.Create(ctx, catalog.Product{
		Slug: "a-book", Title: "A Book", Active: true,
		Categories: []catalog.Category{books, gifts},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(p.Categories) != 2 {
		t.Fatalf("created product is in %v, want both", names(p.Categories))
	}

	// The form submits every ticked box, so a save with one of them missing is how
	// unticking is expressed. Nothing else says "remove this link".
	p.Categories = []catalog.Category{gifts}
	if _, err := s.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Categories) != 1 || got.Categories[0].ID != gifts.ID {
		t.Errorf("after unticking Books the product is in %v", names(got.Categories))
	}
}

func TestCategories_DeleteUnlinksAndNeverDeletesProducts(t *testing.T) {
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	books := mustCategory(t, s, catalog.Category{Slug: "books", Name: "Books"})
	p := mustCreate(t, s, catalog.Product{
		Slug: "a-book", Title: "A Book", Active: true,
		Categories: []catalog.Category{books},
	})

	unlinked, err := s.DeleteCategory(ctx, books.ID)
	if err != nil {
		t.Fatalf("DeleteCategory: %v", err)
	}
	if unlinked != 1 {
		t.Errorf("DeleteCategory unlinked %d products, want 1", unlinked)
	}

	// The point of the whole cascade decision: the product survives its category.
	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("the product went with its category: %v", err)
	}
	if len(got.Categories) != 0 {
		t.Errorf("the product is still in %v", names(got.Categories))
	}

	if _, err := s.DeleteCategory(ctx, books.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

func TestCategories_UpsertKeepsAnEditedNameAndReordering(t *testing.T) {
	// Seeding is rerunnable, and a fixture is not authoritative over a taxonomy
	// somebody has since renamed — the same stance the seed takes on stock counts.
	s := catalog.NewStore(dbtest.Pool(t))
	ctx := t.Context()

	if _, err := s.Upsert(ctx, catalog.Product{
		Slug: "a-book", Title: "A Book", Active: true, CategorySlugs: []string{"books"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	cats, err := s.Categories(ctx)
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(cats) != 1 || cats[0].Slug != "books" || cats[0].Name != "Books" {
		t.Fatalf("seeding produced %+v, want one category named Books", cats)
	}

	edited := cats[0]
	edited.Name = "Reading"
	edited.Position = 3
	if _, err := s.UpdateCategory(ctx, edited); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	p, err := s.Upsert(ctx, catalog.Product{
		Slug: "a-book", Title: "A Book", Active: true, CategorySlugs: []string{"books"},
	})
	if err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if len(p.Categories) != 1 || p.Categories[0].Name != "Reading" || p.Categories[0].Position != 3 {
		t.Errorf("re-seeding overwrote the operator's edit: %+v", p.Categories)
	}
}

func mustCategory(t *testing.T, s *catalog.Store, c catalog.Category) catalog.Category {
	t.Helper()
	out, err := s.CreateCategory(t.Context(), c)
	if err != nil {
		t.Fatalf("CreateCategory(%s): %v", c.Slug, err)
	}
	return out
}

func names(cats []catalog.Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.Name)
	}
	return out
}

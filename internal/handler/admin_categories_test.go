package handler

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

func TestAdminCategories_CreateDerivesSlugAndLists(t *testing.T) {
	srv, store := setup(t)

	_, body := get(t, srv, "/admin/categories")
	if !strings.Contains(body, "No categories yet") {
		t.Error("an empty taxonomy does not say so")
	}

	res, body := post(t, srv, "/admin/categories", url.Values{
		"name":     {"Gift Cards"},
		"position": {"2"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /admin/categories = %d %s", res.StatusCode, body)
	}

	cats, err := store.Categories(t.Context())
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(cats) != 1 {
		t.Fatalf("stored %d categories, want 1", len(cats))
	}
	// The slug is derived from the name, on the same grounds as a product's: it is
	// a detail of the URL rather than a decision worth making twice.
	if cats[0].Slug != "gift-cards" || cats[0].Position != 2 {
		t.Errorf("stored category is %+v", cats[0])
	}

	_, body = get(t, srv, "/admin/categories")
	if !strings.Contains(body, "Gift Cards") || !strings.Contains(body, "gift-cards") {
		t.Error("the list does not show the new category")
	}
}

func TestAdminCategories_RejectsBadInputWithoutWriting(t *testing.T) {
	srv, store := setup(t)

	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		{"no name", url.Values{"name": {"  "}}, "name"},
		{"slug not a slug", url.Values{"name": {"Books"}, "slug": {"Not A Slug"}}, "slug"},
		{"position not a number", url.Values{"name": {"Books"}, "position": {"first"}}, "position"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := post(t, srv, "/admin/categories", tc.form)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("POST = %d, want 422", res.StatusCode)
			}
		})
	}

	cats, err := store.Categories(t.Context())
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("a rejected form wrote %d categories", len(cats))
	}
}

func TestAdminCategories_DeleteUnlinksProductsAndSaysHowMany(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	books, err := store.CreateCategory(ctx, catalog.Category{Slug: "books", Name: "Books"})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	p, err := store.Create(ctx, catalog.Product{
		Slug: "a-book", Title: "A Book", Active: true,
		Categories: []catalog.Category{books},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, body := post(t, srv, "/admin/categories/"+books.ID+"/delete", url.Values{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d %s", res.StatusCode, body)
	}
	// The count is the point: unlinking twelve products and unlinking none leave
	// exactly the same page behind, and only one of those is what was expected.
	if !strings.Contains(body, "removed from 1 product") {
		t.Errorf("the page does not say what the delete did:\n%s", body)
	}

	// A taxonomy edit must never delete catalog entries.
	got, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("the product went with its category: %v", err)
	}
	if len(got.Categories) != 0 {
		t.Errorf("the product is still categorised: %+v", got.Categories)
	}
}

func TestAdminProducts_CategoriesAreTickedSavedAndUnticked(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	books, err := store.CreateCategory(ctx, catalog.Category{Slug: "books", Name: "Books", Position: 1})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	gifts, err := store.CreateCategory(ctx, catalog.Category{Slug: "gifts", Name: "Gifts", Position: 2})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	// A checkbox list submits the same name repeatedly, which is what makes
	// belonging to several categories work with no JavaScript at all.
	res, body := post(t, srv, "/admin/products", url.Values{
		"title":    {"A Book"},
		"active":   {"1"},
		"category": {books.ID, gifts.ID},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d %s", res.StatusCode, body)
	}
	p, err := store.GetBySlug(ctx, "a-book")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	stored, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Categories) != 2 {
		t.Fatalf("created product is in %d categories, want 2", len(stored.Categories))
	}

	_, body = get(t, srv, "/admin/products/"+p.ID+"/edit")
	if !strings.Contains(body, `value="`+books.ID+`"`) || !strings.Contains(body, "Gifts") {
		t.Error("the edit form does not offer the categories")
	}

	// Unticking is expressed by the box simply not being submitted, so a save with
	// one id is a removal of the other.
	res, body = post(t, srv, "/admin/products/"+p.ID, url.Values{
		"title":    {"A Book"},
		"slug":     {"a-book"},
		"active":   {"1"},
		"category": {gifts.ID},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	stored, err = store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Categories) != 1 || stored.Categories[0].ID != gifts.ID {
		t.Errorf("after unticking Books the product is in %+v", stored.Categories)
	}
}

func TestAdminProducts_RejectsAnUnknownCategoryWithoutWriting(t *testing.T) {
	// A category id that is not among the ones the form was rendered from is a
	// message on the form. Letting it reach the database would be a foreign key
	// violation with no field attached — a 500 where a 422 belongs.
	srv, store := setup(t)

	res, body := post(t, srv, "/admin/products", url.Values{
		"title":    {"A Book"},
		"active":   {"1"},
		"category": {"11111111-1111-1111-1111-111111111111"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create = %d, want 422:\n%s", res.StatusCode, body)
	}
	if !strings.Contains(body, "no longer exists") {
		t.Errorf("the form does not explain the problem:\n%s", body)
	}

	products, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 0 {
		t.Errorf("a rejected form wrote %d products", len(products))
	}
}

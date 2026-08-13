package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// stockCatalog fills the shop with enough products to page, in categories, with
// titles chosen so that a stemmed search and a misspelled one can be told apart.
func stockCatalog(t *testing.T, store *catalog.Store) {
	t.Helper()
	ctx := t.Context()

	books := mustCat(t, store, catalog.Category{Slug: "books", Name: "Books", Position: 1})
	apparel := mustCat(t, store, catalog.Category{Slug: "apparel", Name: "Apparel", Position: 2})

	add := func(slug, title, description string, cats ...catalog.Category) {
		t.Helper()
		p, err := store.Create(ctx, catalog.Product{
			Slug: slug, Title: title, Description: description, Active: true, Categories: cats,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", slug, err)
		}
		_, err = store.CreateVariant(ctx, catalog.Variant{
			ProductID: p.ID, SKU: strings.ToUpper(slug), PriceCents: 10000, StockQty: 3, Active: true,
		})
		if err != nil {
			t.Fatalf("CreateVariant %s: %v", slug, err)
		}
	}

	add("the-quiet-machine", "The Quiet Machine", "A book about silence.", books)
	add("canvas-tote", "Canvas Tote", "A sturdy bag.", apparel)
	// In both, which is the case a column on products could not express.
	add("gift-book-bundle", "Gift Book Bundle", "A book and a shirt.", books, apparel)

	// Filler, so there is a second page: 24 to a page, and 3 + 22 is 25.
	for i := range 22 {
		add(fmt.Sprintf("filler-%02d", i), fmt.Sprintf("Filler %02d", i), "Padding.")
	}
}

func mustCat(t *testing.T, store *catalog.Store, c catalog.Category) catalog.Category {
	t.Helper()
	out, err := store.CreateCategory(t.Context(), c)
	if err != nil {
		t.Fatalf("CreateCategory %s: %v", c.Slug, err)
	}
	return out
}

func TestSearch_FindsAStemmedWord(t *testing.T) {
	// The full-text half. "books" must reach a description containing "book",
	// which trigram cannot do — it has no idea the two words are related.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/products?q=books")
	if !strings.Contains(body, "The Quiet Machine") {
		t.Errorf("a stemmed search did not find the book:\n%s", excerpt(body))
	}
	if strings.Contains(body, "Canvas Tote") {
		t.Error("a search for books returned the tote")
	}
}

func TestSearch_SurvivesATypo(t *testing.T) {
	// The trigram half, and the only check that proves it is wired rather than
	// merely indexed: a correctly spelled query passes with full text alone and
	// tells you nothing.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/products?q=canvis+tote")
	if !strings.Contains(body, "Canvas Tote") {
		t.Errorf("a misspelled search found nothing:\n%s", excerpt(body))
	}
}

func TestSearch_ShortQueryIsNoQuery(t *testing.T) {
	// A trigram index cannot help below three characters and one letter matches
	// most of a catalog, so anything shorter is treated as no search at all.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/products?q=a")
	if !strings.Contains(body, "of 25") {
		t.Errorf("a one-character search narrowed the catalog:\n%s", excerpt(body))
	}
}

func TestSearch_CategoriesWidenRatherThanNarrow(t *testing.T) {
	// Getting OR and AND the wrong way round still returns results, so this needs
	// a case where the two answers differ: the intersection of books and apparel is
	// one product, the union is three.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/products?category=books&category=apparel")
	for _, want := range []string{"The Quiet Machine", "Canvas Tote", "Gift Book Bundle"} {
		if !strings.Contains(body, want) {
			t.Errorf("the union is missing %q", want)
		}
	}
	if strings.Contains(body, "Filler 00") {
		t.Error("filtering by category returned an uncategorised product")
	}

	// One category on its own still narrows.
	_, body = get(t, srv, "/products?category=apparel")
	if strings.Contains(body, "The Quiet Machine") {
		t.Error("filtering by apparel returned a book")
	}
}

func TestSearch_UnknownCategoryMatchesNothing(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	res, body := get(t, srv, "/products?category=does-not-exist")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200 — an empty result is not a missing page", res.StatusCode)
	}
	if !strings.Contains(body, "Nothing matched") {
		t.Errorf("an empty result does not say so:\n%s", excerpt(body))
	}
}

func TestPagination_SecondPageDiffersAndKeepsFilters(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, first := get(t, srv, "/products")
	if !strings.Contains(first, "Showing 1–24 of 25") {
		t.Errorf("the first page does not count itself:\n%s", excerpt(first))
	}

	_, second := get(t, srv, "/products?page=2")
	if !strings.Contains(second, "Showing 25–25 of 25") {
		t.Errorf("the second page is not the rest of the catalog:\n%s", excerpt(second))
	}

	// Every page link has to carry the search and the categories that produced the
	// page, or paging away from a search silently drops it.
	_, filtered := get(t, srv, "/products?q=filler")
	if strings.Contains(filtered, "page=2") && !strings.Contains(filtered, "q=filler&amp;page=2") {
		t.Errorf("a page link dropped the search:\n%s", excerpt(filtered))
	}
}

func TestPagination_OutOfRangePageIsNotFound(t *testing.T) {
	// A 404 rather than a clamp: ?page=900 succeeding silently would let a crawler
	// index one catalog under endless URLs.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	for _, path := range []string{"/products?page=900", "/products?page=0", "/products?page=-1", "/products?page=two"} {
		if res, _ := get(t, srv, path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}

	// Page 1 is exempt: a search that matched nothing is a page with something to
	// say, not a page that does not exist.
	if res, _ := get(t, srv, "/products?q=zzzznothing"); res.StatusCode != http.StatusOK {
		t.Errorf("an empty search = %d, want 200", res.StatusCode)
	}
}

func TestSearch_WorksWithoutJavaScript(t *testing.T) {
	// The filter is a plain GET form and the page links are plain hrefs, so the
	// whole feature works with scripting off. This asserts the markup that makes
	// that true, since htmx attributes alone would look identical in a screenshot.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/products")
	for _, want := range []string{
		`<form class="filters" method="get" action="/products"`,
		`name="q"`,
		`type="checkbox" name="category" value="books"`,
		`href="/products?page=2"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q, so it needs JavaScript:\n%s", want, excerpt(body))
		}
	}

	// And the htmx upgrade is present on the same elements.
	if !strings.Contains(body, `hx-target="#products"`) || !strings.Contains(body, "show:window:top") {
		t.Error("the htmx upgrade is missing")
	}
}

func TestSearch_EmbeddedFragmentHasNoControls(t *testing.T) {
	// Filter and page controls push the URL they navigate to, and inside somebody
	// else's page that would rewrite their address bar. An embedder gets the first
	// page and a link through to the real catalog instead.
	cfg := testConfig()
	cfg.EmbedOrigins = []string{"https://cms.example"}
	cfg.BaseURL = "https://store.example"
	srv, store := newStorefront(t, cfg, "")
	stockCatalog(t, store)

	body := getWith(t, srv, "/products?q=filler&page=2", http.Header{
		"Origin":     {"https://cms.example"},
		"HX-Request": {"true"},
	})

	for _, unwanted := range []string{"hx-push-url", `name="q"`, "Showing 1–24", "products_pager"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the embedded fragment carries %q", unwanted)
		}
	}
	// It says how many there are and where they live: 24 of 25 shown silently would
	// look like the whole shop.
	if !strings.Contains(body, "See all 25 products") {
		t.Errorf("the embedded fragment does not link out:\n%s", excerpt(body))
	}
	if !strings.Contains(body, `href="https://store.example/products"`) {
		t.Error("the link out is not absolute, so it points at the embedder")
	}
}

func TestImages_FirstRowIsEagerAndTheRestAreLazy(t *testing.T) {
	// Both halves matter: all-eager and all-lazy each look fine in a screenshot.
	// The first row decides how quickly the page looks finished, and lazy-loading an
	// image already on screen defers exactly that one.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	// Counted inside the grid only: the site header carries a logo, which is not a
	// product image and has its own rules.
	_, page := get(t, srv, "/products")
	body := between(t, page, `<ul class="products">`, "</ul>")
	imgs := strings.Count(body, "<img")
	lazy := strings.Count(body, `loading="lazy"`)
	if imgs != 24 {
		t.Fatalf("the grid has %d images, want a page of 24", imgs)
	}
	if lazy != imgs-4 {
		t.Errorf("%d of %d images are lazy, want all but the first four", lazy, imgs)
	}
	if strings.Count(body, `decoding="async"`) != imgs {
		t.Error("not every card image decodes off the main thread")
	}
}

func TestImages_DetailImageIsEagerAndPrioritised(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	product := stock(t, store)

	_, page := get(t, srv, "/products/"+product.Slug)
	body := between(t, page, `<article class="product-detail">`, "</span>")
	if strings.Contains(body, `loading="lazy"`) {
		t.Error("the product page's own image is lazy, and it is the one being waited for")
	}
	if !strings.Contains(body, `fetchpriority="high"`) {
		t.Error("the detail image does not ask for priority")
	}
	// aspect-ratio on .frame already reserves the box, so intrinsic dimensions
	// would be redundant — and wrong for object-fit: cover images whose real
	// dimensions the store never records.
	if strings.Contains(body, "width=") || strings.Contains(body, "height=") {
		t.Error("an image carries width/height, which .frame already handles")
	}
}

// between returns the part of a page between two markers, so an assertion about
// product images is not answered by the logo in the site header.
func between(t *testing.T, body, start, end string) string {
	t.Helper()
	_, rest, found := strings.Cut(body, start)
	if !found {
		t.Fatalf("the page has no %q:\n%s", start, excerpt(body))
	}
	inner, _, found := strings.Cut(rest, end)
	if !found {
		t.Fatalf("the page has no %q after %q", end, start)
	}
	return inner
}

// excerpt trims a page down to something readable in a failure message.
func excerpt(body string) string {
	if len(body) > 1500 {
		return body[:1500] + "\n…"
	}
	return body
}

package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndex_ShowsTheStoreAndTheWayToTheCatalog(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	res, body := get(t, srv, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	for _, want := range []string{"Test Store", "Sample Tee", `href="/products"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the index is missing %q", want)
		}
	}
	// The same visibility rules as the catalog: the front page must never show
	// something /products hides.
	if strings.Contains(body, "Unpublished Draft") {
		t.Error("the index shows an inactive product")
	}
}

func TestIndex_ShowsTheNewestFewOnly(t *testing.T) {
	// stockCatalog inserts 25 products in a known order, so both halves are
	// checkable: that the newest are there and that the oldest is not.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/")
	grid := between(t, body, `<ul class="products">`, "</ul>")

	if got := strings.Count(grid, "<li>"); got != indexProducts {
		t.Errorf("the index shows %d products, want %d", got, indexProducts)
	}
	// Last inserted, so newest.
	if !strings.Contains(grid, "Filler 21") {
		t.Errorf("the newest product is missing:\n%s", grid)
	}
	// First inserted, so oldest — and the proof that this is ordered rather than
	// just truncated at whatever the database felt like returning.
	if strings.Contains(grid, "The Quiet Machine") {
		t.Error("the index shows the oldest product")
	}
}

func TestIndex_ImagesAreAllEager(t *testing.T) {
	// Four cards are one row, all above the fold, so none of them should be
	// deferred — lazy-loading an image already on screen delays the thing the
	// visitor is waiting for. The catalog's first four are eager for the same
	// reason, which is why this needs no flag: the index only ever shows four.
	srv, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	_, body := get(t, srv, "/")
	grid := between(t, body, `<ul class="products">`, "</ul>")

	if strings.Contains(grid, `loading="lazy"`) {
		t.Error("an index image is lazy, although the whole page is one row")
	}
	if got := strings.Count(grid, `decoding="async"`); got != indexProducts {
		t.Errorf("%d of %d images decode off the main thread", got, indexProducts)
	}
}

func TestIndex_EmptyCatalogStillRenders(t *testing.T) {
	// A shop that has not added anything yet still has a front door, and it says
	// so rather than 500ing or showing an empty grid.
	srv, _ := newStorefront(t, testConfig(), "")

	res, body := get(t, srv, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / on an empty catalog = %d", res.StatusCode)
	}
	if !strings.Contains(body, "Nothing for sale yet") {
		t.Errorf("an empty catalog does not say so:\n%s", excerpt(body))
	}
	if strings.Contains(body, `<ul class="products">`) {
		t.Error("an empty catalog renders an empty grid")
	}
}

func TestIndex_RootPatternIsNotACatchAll(t *testing.T) {
	// The reason the route is "GET /{$}" and not "/": the bare pattern is a
	// subtree that matches every path nothing else claimed, which would turn every
	// honest 404 in the store into the home page. This is the test that catches the
	// worst mistake available here.
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	for _, path := range []string{"/nope", "/products/no-such-product", "/deep/nested/nonsense"} {
		res, body := get(t, srv, path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
		if strings.Contains(body, "Welcome to") {
			t.Errorf("GET %s served the index page", path)
		}
	}
}

func TestIndex_DoesNotAnswerAPost(t *testing.T) {
	// The index is registered as "GET /{$}", so a POST is not it — and it says so
	// with a 405 rather than pretending the front page does not exist.
	//
	// The "/" catch-all that installs the 404 page would otherwise swallow this,
	// because a pattern that matches beats one that would only have matched under
	// another method. The catch-all asks the mux which methods the path does have;
	// see notFoundFor.
	srv, _ := newStorefront(t, testConfig(), "")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", res.StatusCode)
	}
	if got := res.Header.Get("Allow"); !strings.Contains(got, "GET") {
		t.Errorf("Allow = %q, want it to name GET", got)
	}
}

func TestIndex_SetsNoCookies(t *testing.T) {
	// The index carries no form, so it is outside the CSRF layer and outside the
	// cart, which leaves it the one HTML page that sets nothing at all. Worth
	// pinning: adding a form here would silently start setting a token cookie and
	// then refuse the form anyway, since this route is not in the CSRF group.
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	res, _ := get(t, srv, "/")
	if cookies := res.Cookies(); len(cookies) != 0 {
		t.Errorf("the index set %d cookie(s): %v", len(cookies), cookies)
	}
}

func TestIndex_CardsComeFromTheSharedGrid(t *testing.T) {
	// The index and the catalog render cards through one template, so an adopter
	// who restyles a card gets it in both places. Overriding product_grid and
	// seeing both pages change is what proves that, and it is the property that
	// duplicating the markup would have quietly broken.
	dir := t.TempDir()
	writeOverride(t, dir, "products.gohtml", `{{define "product_grid"}}SHARED-GRID{{end}}`)

	srv, store := newStorefront(t, testConfig(), dir)
	stock(t, store)

	for _, path := range []string{"/", "/products"} {
		if _, body := get(t, srv, path); !strings.Contains(body, "SHARED-GRID") {
			t.Errorf("GET %s does not use the shared grid", path)
		}
	}
}

func TestIndex_IsOverridable(t *testing.T) {
	// The whole customisation story: one file in TEMPLATE_DIR replaces the page.
	dir := t.TempDir()
	writeOverride(t, dir, "index.gohtml", `{{define "index"}}MY OWN FRONT PAGE{{end}}`)

	srv, store := newStorefront(t, testConfig(), dir)
	stock(t, store)

	_, body := get(t, srv, "/")
	if got := strings.TrimSpace(body); got != "MY OWN FRONT PAGE" {
		t.Errorf("the override did not take: %q", excerpt(got))
	}
}

func TestIndex_HeaderPointsHomeAndShopPointsAtTheCatalog(t *testing.T) {
	// Two links that are now easy to conflate: the brand goes home, and the nav
	// item next to it goes to the catalog.
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	_, body := get(t, srv, "/products")
	if !strings.Contains(body, `<a class="brand" href="/"`) {
		t.Error("the brand does not link home")
	}
	if !strings.Contains(body, `<a href="/products">Shop</a>`) {
		t.Error("the Shop nav item no longer links to the catalog")
	}
}

// The store-level half of the ordering check, without a handler in the way.
func TestStoreNewestActive_OrdersAndLimits(t *testing.T) {
	_, store := newStorefront(t, testConfig(), "")
	stockCatalog(t, store)

	got, err := store.NewestActive(t.Context(), 3)
	if err != nil {
		t.Fatalf("NewestActive: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("NewestActive(3) returned %d products", len(got))
	}
	if got[0].Title != "Filler 21" {
		t.Errorf("the first product is %q, want the newest", got[0].Title)
	}
	// Variants come with them, or the cards cannot price anything.
	if len(got[0].Variants) == 0 {
		t.Error("NewestActive returned a product with no variants attached")
	}

	if _, err := store.NewestActive(t.Context(), 0); err == nil {
		t.Error("NewestActive(0) did not complain")
	}
}

// writeOverride drops a template override into a TEMPLATE_DIR for a test.
func writeOverride(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write override %s: %v", name, err)
	}
}

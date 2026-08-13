package handler

import (
	"bytes"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/dbtest"
	"github.com/17xande-dev/gostore/internal/email"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
)

// testPassword is the admin password every test in this package signs in with.
const testPassword = "correct horse battery staple"

// testHash is argon2id at its cheapest: these tests assert on authentication
// behaviour, not on how expensive the hash is, and DefaultParams would add 64 MiB
// and a tenth of a second to every sign-in here.
var testHash = sync.OnceValue(func() string {
	h, err := auth.HashPassword(testPassword,
		auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		panic(err)
	}
	return h
})

func testConfig() config.Config {
	return config.Config{
		StoreName:         "Test Store",
		Currency:          "ZAR",
		AdminPasswordHash: testHash(),
		SessionSecret:     bytes.Repeat([]byte("s"), auth.MinSecretLen),
		SessionTTL:        time.Hour,
	}
}

// shop is a running server and everything behind it a test might assert on: what
// reached the database, what the checkout handed the gateway, and what mail went
// out. The fakes are the point — a test can inspect both sides of every boundary
// without a payment provider or a mail server.
type shop struct {
	srv     *httptest.Server
	catalog *catalog.Store
	orders  *orders.Store
	gateway *payment.Fake
	mail    *email.Fake
	images  *blob.Fake

	// variants is the stocked catalog, by size, for the tests that put things in
	// a cart. Empty until stockCart has run.
	variants map[string]catalog.Variant
}

// newServer is the narrow view, for tests that only care about the catalog and the
// admin.
func newServer(t *testing.T) (*httptest.Server, *catalog.Store) {
	t.Helper()
	s := newStore(t)
	return s.srv, s.catalog
}

func testSessions(t *testing.T) *auth.Sessions {
	t.Helper()
	cfg := testConfig()
	s, err := auth.NewSessions(cfg.SessionSecret, nil, cfg.SessionTTL)
	if err != nil {
		t.Fatalf("NewSessions: %v", err)
	}
	return s
}

// newStore mounts everything exactly as main.go does — same subtrees, same CSRF
// wrapper, same middleware — with a cookie jar but no session yet. Tests that
// build their own routing would stop testing what actually runs.
// edit lets a test change the configuration before the handler reads it, which is
// the only chance it gets — the handler takes a copy at construction.
func newStore(t *testing.T, edit ...func(*config.Config)) *shop {
	t.Helper()

	cfg := testConfig()
	for _, e := range edit {
		e(&cfg)
	}

	pool := dbtest.Pool(t)
	store := catalog.NewStore(pool)
	orderStore := orders.NewStore(pool)

	// One storage fake, shared: the templates resolve a product's image key through
	// the same backend the upload handler wrote to, exactly as in production.
	images := blob.NewFake()
	tmpl, err := ParseTemplates("", images)
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	log := slog.New(slog.DiscardHandler)
	sessions := testSessions(t)
	gateway := payment.NewFake()
	mail := email.NewFake()
	h := New(cfg, log, tmpl, store, cart.NewStore(pool), orderStore, gateway, mail, images, sessions)

	mux := http.NewServeMux()
	// Everything main.go mounts, mounted the same way: the cart tests need the
	// product pages, because an add-to-cart starts on one, and the callback has to
	// be outside the CSRF group for the same reason it is in production.
	h.RegisterStorefront(mux)
	h.RegisterPayments(mux)
	firstParty := h.FirstPartyHandler(middleware.RequireAdmin(sessions, log))
	mux.Handle("/admin/", firstParty)
	mux.Handle("/cart", firstParty)
	mux.Handle("/cart/", firstParty)

	srv := httptest.NewServer(mux)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	srv.Client().Jar = jar
	// Redirects are the assertion in several tests, so they must not be
	// followed away.
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(srv.Close)
	return &shop{srv: srv, catalog: store, orders: orderStore, gateway: gateway, mail: mail, images: images}
}

// setup returns a signed-in server for the admin routes plus the catalog store
// behind it, so a test can assert on both the response and what actually landed
// in the database.
func setup(t *testing.T) (*httptest.Server, *catalog.Store) {
	t.Helper()

	srv, store := newServer(t)
	signIn(t, srv)
	return srv, store
}

func signIn(t *testing.T, srv *httptest.Server) {
	t.Helper()

	res, body := post(t, srv, "/admin/login", url.Values{"password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in = %d %s", res.StatusCode, body)
	}
	if len(res.Cookies()) == 0 {
		t.Fatal("sign in set no cookie")
	}
}

func TestAdmin_RedirectsToProducts(t *testing.T) {
	srv, _ := setup(t)

	res, body := get(t, srv, "/admin/")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /admin/ = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != "/admin/products" {
		t.Errorf("Location = %q", got)
	}
}

func TestAdminProducts_ListsProductsAndStock(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	res, body := get(t, srv, "/admin/products")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/products = %d", res.StatusCode)
	}
	if !strings.Contains(body, "No products yet") {
		t.Error("an empty catalog does not say so")
	}

	apparel, err := store.CreateCategory(ctx, catalog.Category{Slug: "apparel", Name: "Apparel"})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	p, err := store.Create(ctx, catalog.Product{
		Slug: "tee", Title: "Sample Tee", Active: true,
		Categories: []catalog.Category{apparel},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, v := range []catalog.Variant{
		{ProductID: p.ID, SKU: "TEE-S", Size: "S", PriceCents: 29900, StockQty: 4, Active: true},
		{ProductID: p.ID, SKU: "TEE-M", Size: "M", PriceCents: 29900, StockQty: 3, Active: true},
	} {
		if _, err := store.CreateVariant(ctx, v); err != nil {
			t.Fatalf("CreateVariant: %v", err)
		}
	}

	_, body = get(t, srv, "/admin/products")
	for _, want := range []string{"Sample Tee", "Apparel", "tee", ">7<", "/admin/products/" + p.ID + "/edit"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list is missing %q", want)
		}
	}
}

func TestAdminProducts_CreateDerivesSlugAndRedirectsToEdit(t *testing.T) {
	srv, store := setup(t)

	res, body := post(t, srv, "/admin/products", url.Values{
		"title":       {"The Quiet Machine"},
		"description": {"A demo book."},
		"active":      {"1"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /admin/products = %d %s", res.StatusCode, body)
	}

	p, err := store.GetBySlug(t.Context(), "the-quiet-machine")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if !p.Active {
		t.Errorf("stored product is %+v", p)
	}
	// Straight to the edit page, because a product with no variants cannot be
	// bought yet.
	if got, want := res.Header.Get("Location"), "/admin/products/"+p.ID+"/edit"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestAdminProducts_RejectsInvalidFormWithoutWriting(t *testing.T) {
	srv, store := setup(t)

	res, body := post(t, srv, "/admin/products", url.Values{
		"title": {""},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST with no title = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "Required.") {
		t.Error("the re-rendered form does not show the error")
	}

	products, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 0 {
		t.Errorf("%d products were written by a rejected form", len(products))
	}
}

func TestAdminProducts_DuplicateSlugIsAFieldError(t *testing.T) {
	srv, store := setup(t)

	form := url.Values{"title": {"A Book"}, "slug": {"a-book"}, "active": {"1"}}
	if res, body := post(t, srv, "/admin/products", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("first create = %d %s", res.StatusCode, body)
	}

	res, body := post(t, srv, "/admin/products", form)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second create = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "Already used by another product.") {
		t.Error("the duplicate slug is not reported on the form")
	}

	products, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 1 {
		t.Errorf("%d products exist, want 1", len(products))
	}
}

func TestAdminProducts_Update(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "a-book", Title: "A Book", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, body := post(t, srv, "/admin/products/"+p.ID, url.Values{
		"title": {"A Better Book"},
		"slug":  {"a-better-book"},
		// "active" omitted: an unchecked checkbox sends nothing, which must
		// deactivate the product rather than leaving it as it was.
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}

	got, err := store.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "A Better Book" || got.Slug != "a-better-book" || got.Active {
		t.Errorf("stored product is %+v", got)
	}
}

func TestAdminProducts_UnknownIDIs404(t *testing.T) {
	srv, _ := setup(t)

	for _, path := range []string{
		"/admin/products/3f2504e0-4f89-41d3-9a0c-0305e82c3301/edit",
		"/admin/products/not-a-uuid/edit",
	} {
		if res, _ := get(t, srv, path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestAdminVariants_AddParsesPriceAsCents(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, body := post(t, srv, "/admin/products/"+p.ID+"/variants", url.Values{
		"sku":       {"TEE-M-BLK"},
		"size":      {"M"},
		"color":     {"Black"},
		"price":     {"299.99"},
		"stock_qty": {"7"},
		"active":    {"1"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("add variant = %d %s", res.StatusCode, body)
	}

	variants, err := store.Variants(ctx, p.ID)
	if err != nil {
		t.Fatalf("Variants: %v", err)
	}
	if len(variants) != 1 {
		t.Fatalf("%d variants, want 1", len(variants))
	}
	v := variants[0]
	if v.PriceCents != 29999 {
		t.Errorf("price = %d cents, want 29999", v.PriceCents)
	}
	if v.StockQty != 7 || v.SKU != "TEE-M-BLK" || !v.Active {
		t.Errorf("stored variant is %+v", v)
	}

	// The edit page shows the price back as an amount, not as cents.
	_, body = get(t, srv, "/admin/products/"+p.ID+"/edit")
	if !strings.Contains(body, `value="299.99"`) {
		t.Error("the variant price is not rendered as a decimal amount")
	}
}

func TestAdminVariants_RejectsBadPriceAndKeepsInput(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, body := post(t, srv, "/admin/products/"+p.ID+"/variants", url.Values{
		"sku":       {"TEE-M-BLK"},
		"price":     {"R 299,999"},
		"stock_qty": {"seven"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad variant = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "Enter an amount like 149.99.") {
		t.Error("the price error is not shown")
	}
	if !strings.Contains(body, "Enter a whole number of items") {
		t.Error("the stock error is not shown")
	}
	// The form comes back with what was typed, so it can be corrected rather
	// than re-entered.
	if !strings.Contains(body, `value="R 299,999"`) {
		t.Error("the rejected price was not returned to the form")
	}

	if variants, err := store.Variants(ctx, p.ID); err != nil || len(variants) != 0 {
		t.Errorf("Variants = %v, %v; a rejected form wrote a variant", variants, err)
	}
}

func TestAdminVariants_UpdateAndDelete(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v, err := store.CreateVariant(ctx, catalog.Variant{
		ProductID: p.ID, SKU: "TEE-M", Size: "M", PriceCents: 29900, StockQty: 7, Active: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	base := "/admin/products/" + p.ID + "/variants/" + v.ID
	res, body := post(t, srv, base, url.Values{
		"sku":       {"TEE-M"},
		"size":      {"M"},
		"price":     {"319.00"},
		"stock_qty": {"2"},
		// "active" omitted, so the variant comes off sale.
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("update variant = %d %s", res.StatusCode, body)
	}

	variants, err := store.Variants(ctx, p.ID)
	if err != nil {
		t.Fatalf("Variants: %v", err)
	}
	if len(variants) != 1 {
		t.Fatalf("%d variants, want 1", len(variants))
	}
	if got := variants[0]; got.PriceCents != 31900 || got.StockQty != 2 || got.Active {
		t.Errorf("updated variant is %+v", got)
	}

	if res, body := post(t, srv, base+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete variant = %d %s", res.StatusCode, body)
	}
	if variants, err := store.Variants(ctx, p.ID); err != nil || len(variants) != 0 {
		t.Errorf("Variants after delete = %v, %v", variants, err)
	}
}

func TestAdminVariants_DuplicateSKUIsAFieldError(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "tee", Title: "Tee", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.CreateVariant(ctx, catalog.Variant{
		ProductID: p.ID, SKU: "TEE-M", Size: "M", PriceCents: 29900, StockQty: 1, Active: true,
	}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	res, body := post(t, srv, "/admin/products/"+p.ID+"/variants", url.Values{
		"sku":       {"TEE-M"},
		"size":      {"L"},
		"price":     {"299.00"},
		"stock_qty": {"1"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate SKU = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "Already used by another variant.") {
		t.Error("the duplicate SKU is not reported on the form")
	}

	// Same SKU rejected, and the same size/colour pair reported differently.
	res, body = post(t, srv, "/admin/products/"+p.ID+"/variants", url.Values{
		"sku":       {"TEE-M-2"},
		"size":      {"M"},
		"price":     {"299.00"},
		"stock_qty": {"1"},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate options = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "already has that size and colour") {
		t.Error("the duplicate size/colour pair is not reported on the form")
	}
}

func TestAdminProducts_Delete(t *testing.T) {
	srv, store := setup(t)
	ctx := t.Context()

	p, err := store.Create(ctx, catalog.Product{Slug: "doomed", Title: "Doomed", Active: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, body := post(t, srv, "/admin/products/"+p.ID+"/delete", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != "/admin/products" {
		t.Errorf("Location = %q", got)
	}
	if products, err := store.List(ctx); err != nil || len(products) != 0 {
		t.Errorf("List = %v, %v after delete", products, err)
	}
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return do(t, srv, req)
}

// post submits a form with a valid CSRF token, so ordinary tests exercise the
// same path a browser takes rather than being exempted from it. Pass a
// csrf_token explicitly — including an empty one — to control it, which is how
// the CSRF tests themselves get a rejection.
func post(t *testing.T, srv *httptest.Server, path string, form url.Values) (*http.Response, string) {
	t.Helper()

	if form == nil {
		form = url.Values{}
	}
	if _, set := form["csrf_token"]; !set {
		form.Set("csrf_token", csrfToken(t, srv))
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// nosurf checks the request's origin as well as its token, via
	// Sec-Fetch-Site, Origin or Referer — a browser sends all three on a
	// same-origin form post, so a test that sends none is not emulating one.
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Referer", srv.URL+path)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return do(t, srv, req)
}

// csrfToken reads a token out of a rendered form. nosurf validates the
// submitted token against the client's cookie, so any token issued to this jar
// works for any later request from it.
//
// Which page depends on whether the client is signed in: the login form
// redirects away once it is, and the new-product form is unreachable until it
// is. Between them one of the two always renders.
func csrfToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()

	res, body := get(t, srv, "/admin/login")
	if res.StatusCode != http.StatusOK {
		_, body = get(t, srv, "/admin/products/new")
	}

	m := csrfFieldRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf_token field in the page: %s", body)
	}
	// The token is base64, so html/template entity-escapes any "+" in it. A
	// browser decodes that before submitting; this has to do the same.
	return html.UnescapeString(m[1])
}

var csrfFieldRE = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func do(t *testing.T, srv *httptest.Server, req *http.Request) (*http.Response, string) {
	t.Helper()
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, string(body)
}

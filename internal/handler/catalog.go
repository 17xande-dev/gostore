package handler

import (
	"net/http"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/middleware"
)

// The storefront catalog is read-only and sets no cookies, which is what makes
// it embeddable in a page on another origin. Everything from "add to cart"
// onward is first-party on the store's own domain, so the cart cookie stays
// first-party and none of SameSite=None, third-party cookie blocking, or iframe
// checkout ever comes up.
//
// Each route answers twice over: a full page for a normal visit, and a bare
// fragment when htmx asks — same URL, same data, so an embedder needs no special
// endpoint and no API.

type productsPageData struct {
	page
	Products []catalog.Product
}

type productPageData struct {
	page
	Product catalog.Product
}

// RegisterStorefront wires the public catalog routes. They take no session and
// no CSRF token because they change nothing; CORS comes from config, so
// embedding is off until an adopter names the origins allowed to do it.
func (h *Handler) RegisterStorefront(mux *http.ServeMux) {
	cors := middleware.CORS(h.cfg.EmbedOrigins)

	mux.Handle("GET /products", cors(http.HandlerFunc(h.products)))
	mux.Handle("GET /products/{slug}", cors(http.HandlerFunc(h.product)))
	// Preflight, for a cross-origin htmx fetch that sets HX-Request.
	mux.Handle("OPTIONS /products", cors(http.HandlerFunc(notFound)))
	mux.Handle("OPTIONS /products/{slug}", cors(http.HandlerFunc(notFound)))

	// Vendored htmx, served from the binary so the storefront needs no CDN.
	mux.Handle("GET /static/", http.HandlerFunc(h.static))
}

func (h *Handler) products(w http.ResponseWriter, r *http.Request) {
	products, err := h.cat.ListActive(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	data := productsPageData{page: h.newPage(r, "Products"), Products: products}
	h.render(w, r, http.StatusOK, fragmentOr(r, "products_list", "products"), data)
}

func (h *Handler) product(w http.ResponseWriter, r *http.Request) {
	p, err := h.cat.GetActiveBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	data := productPageData{page: h.newPage(r, p.Title), Product: p}
	h.render(w, r, http.StatusOK, fragmentOr(r, "product_detail", "product"), data)
}

// fragmentOr picks the fragment template for an htmx request and the full page
// for anything else. One URL serving both is what lets the same catalog be
// browsed on the store and embedded elsewhere without a second implementation.
func fragmentOr(r *http.Request, fragment, full string) string {
	// HX-Boosted means htmx is navigating on behalf of a plain link, so the
	// browser expects a whole document.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		return fragment
	}
	return full
}

func notFound(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

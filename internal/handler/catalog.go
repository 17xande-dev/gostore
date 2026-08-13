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

	// Search is what was asked for, so the form comes back filled in.
	Search   string
	Facets   []facet
	Filtered bool
	Pager    pagination

	// Embedded says this response is going into a page on another origin, which is
	// what decides between a pager and a link out to the store. An embedder's
	// address bar is not this store's to rewrite, and hx-push-url inside their page
	// would do exactly that.
	Embedded bool
}

type productPageData struct {
	page
	Product catalog.Product
}

// RegisterStorefront wires the public catalog routes. They need no session and
// change nothing; CORS comes from config, so embedding is off until an adopter
// names the origins allowed to do it.
//
// Each route is served through one of two chains, chosen per request:
//
//   - **Embedded** (the request carries a foreign Origin): served bare, so no
//     cookie of any kind is set and the response stays cacheable and safe to drop
//     into another origin's page.
//   - **First-party** (an ordinary visit to the store): served through the CSRF
//     handler, because the product page carries an add-to-cart form and a form
//     needs a token. Nothing else about the response differs.
//
// The alternative — leaving the whole storefront outside CSRF — renders that form
// with an empty token, so every add-to-cart is refused with a 403. The
// alternative in the other direction, putting the storefront wholly inside CSRF,
// sets a cookie on the fragments that exist precisely to be cookie-free.
func (h *Handler) RegisterStorefront(mux *http.ServeMux) {
	cors := middleware.CORS(h.cfg.EmbedOrigins)

	catalogMux := http.NewServeMux()
	catalogMux.HandleFunc("GET /products", h.products)
	catalogMux.HandleFunc("GET /products/{slug}", h.product)
	// Preflight, for a cross-origin htmx fetch that sets HX-Request.
	catalogMux.HandleFunc("OPTIONS /products", plainNotFound)
	catalogMux.HandleFunc("OPTIONS /products/{slug}", plainNotFound)

	firstParty := h.withCSRF(catalogMux)
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.isEmbedded(r) {
			catalogMux.ServeHTTP(w, r)
			return
		}
		firstParty.ServeHTTP(w, r)
	})

	mux.Handle("GET /products", cors(dispatch))
	mux.Handle("GET /products/{slug}", cors(dispatch))
	mux.Handle("OPTIONS /products", cors(dispatch))
	mux.Handle("OPTIONS /products/{slug}", cors(dispatch))

	// The index, outside the two chains above and outside CORS. It carries no
	// form, so it needs no CSRF token and sets no cookie of any kind; and it is
	// this store's own front door rather than something to drop into another
	// origin's page, so it is not embeddable and wants no CORS header.
	//
	// "GET /{$}" and not "/": {$} matches the root and only the root, so the index
	// is the front page rather than the answer to every unclaimed path. Same
	// distinction as GET /admin/{$}.
	//
	// nosurf.Token returns "" off the CSRF path rather than panicking, which the
	// embedded catalog above already relies on, so newPage is safe here.
	mux.HandleFunc("GET /{$}", h.index)

	// And "/" — the bare subtree, which matches every path no other pattern
	// claimed — is how a custom 404 page is installed, since ServeMux has no
	// NotFoundHandler to set. The pair is deliberate: {$} is the front page, and
	// this is everything nobody claimed.
	//
	// One consequence worth knowing: a request to a *known* path under an
	// unregistered method used to get 405 from the mux, and now lands here as a
	// 404 instead, because a pattern that matches beats one that would only have
	// matched with a different method. POST / is the case that changed.
	mux.HandleFunc("/", h.notFound)

	// Vendored htmx, served from the binary so the storefront needs no CDN.
	mux.Handle("GET /static/", http.HandlerFunc(h.static))
}

// isEmbedded reports whether this request comes from a page on another origin,
// which is when the response must carry no cookies.
//
// A browser sends Origin on a cross-origin fetch and omits it on an ordinary
// same-origin navigation, so its presence and value is the signal. Sec-Fetch-Site
// is checked too, for browsers that send Origin on same-origin fetches.
func (h *Handler) isEmbedded(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "cross-site", "same-site":
		return true
	}
	origin := r.Header.Get("Origin")
	return origin != "" && origin != h.cfg.BaseURL
}

func (h *Handler) products(w http.ResponseWriter, r *http.Request) {
	params, ok := parseSearch(r)
	if !ok {
		h.notFound(w, r)
		return
	}

	// An embedded fragment gets the first page and nothing that navigates: the
	// filter form and the page links push a URL, and inside somebody else's page
	// that would rewrite their address bar. It gets a link to the store instead.
	embedded := h.isEmbedded(r)
	if embedded {
		params = searchParams{Page: 1}
	}

	results, err := h.cat.SearchActive(r.Context(), catalog.Search{
		Query:         params.Query,
		CategorySlugs: params.Categories,
		Page:          params.Page,
		PageSize:      pageSize,
	})
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	// A page past the end is a 404, on the same grounds as an inactive product.
	// Page 1 is exempt: an empty catalog, and a search that matched nothing, are
	// both pages with something to say rather than pages that do not exist.
	if params.Page > 1 && params.Page > results.Pages {
		h.notFound(w, r)
		return
	}

	// Every category, always, in its configured order — not just the ones the
	// current results hit. A filter list that reshapes itself as the shopper types
	// moves the option they were reaching for. The cost is that an empty category
	// is offered and returns nothing.
	var facets []facet
	if !embedded {
		cats, err := h.cat.Categories(r.Context())
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		selected := make(map[string]bool, len(params.Categories))
		for _, slug := range params.Categories {
			selected[slug] = true
		}
		facets = make([]facet, 0, len(cats))
		for _, c := range cats {
			facets = append(facets, facet{Slug: c.Slug, Name: c.Name, Selected: selected[c.Slug]})
		}
	}

	data := productsPageData{
		page:     h.newPage(r, "Products"),
		Products: results.Products,
		Search:   params.Query,
		Facets:   facets,
		Filtered: params.filtered(),
		Pager:    paginate(params, params.Page, results.Pages, results.Total),
		Embedded: embedded,
	}
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

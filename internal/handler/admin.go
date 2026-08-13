package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/email"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
	"github.com/17xande-dev/gostore/internal/validate"
	"github.com/justinas/nosurf"
)

// Handler holds everything the HTTP layer needs. It is created once at startup
// and is safe for concurrent use.
type Handler struct {
	cfg      config.Config
	log      *slog.Logger
	tmpl     *Templates
	cat      *catalog.Store
	cart     *cart.Store
	orders   *orders.Store
	gateway  payment.Gateway
	mail     email.Sender
	blob     blob.Storage
	sessions *auth.Sessions

	// limits are built here from cfg rather than passed in, so that a rate limit is
	// applied on the line that registers the route it protects — the same reasoning
	// as RequireAdmin. A limiter wrapped around a prefix by the caller is one
	// refactor away from silently not covering a new route.
	limits limiters
}

// limiters are the per-surface rate limits. A zero limit means the surface is
// unlimited, which is what the test configuration uses and what an operator gets
// by setting RATE_LIMIT_*_PER_MINUTE=0.
type limiters struct {
	login    middleware.Middleware
	checkout middleware.Middleware
	callback middleware.Middleware
}

// perMinute builds a limiter allowing n requests a minute, or a pass-through when
// n is zero. Burst is a third of the allowance, minimum two, so a shopper who
// double-clicks is never the one who trips it.
func perMinute(name string, n int, trustProxy bool, log *slog.Logger) middleware.Middleware {
	if n <= 0 {
		log.Warn("rate limiting is disabled for a surface that has one available", "limiter", name)
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.RateLimit(middleware.RateLimitConfig{
		Name:  name,
		Every: time.Minute / time.Duration(n),
		Burst: max(2, n/3),
	}, trustProxy, log)
}

func New(cfg config.Config, log *slog.Logger, tmpl *Templates, cat *catalog.Store, carts *cart.Store,
	ord *orders.Store, gateway payment.Gateway, mail email.Sender, images blob.Storage,
	sessions *auth.Sessions) *Handler {
	return &Handler{
		cfg: cfg, log: log, tmpl: tmpl, cat: cat, cart: carts,
		orders: ord, gateway: gateway, mail: mail, blob: images, sessions: sessions,
		limits: limiters{
			login:    perMinute("admin login", cfg.RateLimits.LoginPerMinute, cfg.TrustProxyIP, log),
			checkout: perMinute("checkout", cfg.RateLimits.CheckoutPerMinute, cfg.TrustProxyIP, log),
			callback: perMinute("payment callback", cfg.RateLimits.CallbackPerMinute, cfg.TrustProxyIP, log),
		},
	}
}

// FirstPartyHandler returns everything that changes state — the admin, the cart
// and the checkout — behind CSRF protection. The admin routes additionally require
// a session; the cart and checkout are anonymous.
//
// CSRF is scoped to these routes rather than wrapped around the server's whole
// mux, because nosurf sets a token cookie on every response it handles and the
// embeddable catalog reads must stay cookie-free to be droppable into another
// origin's page. Scoping by group is also what makes the payment callback
// CSRF-exempt: it is not in this group at all, rather than being excused by an
// exempt-path string that has to keep matching the route.
func (h *Handler) FirstPartyHandler(protect middleware.Middleware) http.Handler {
	mux := http.NewServeMux()
	h.RegisterAdmin(mux, protect)
	h.registerCart(mux)
	h.registerCheckout(mux)
	return h.withCSRF(mux)
}

// withCSRF wraps a handler in nosurf. It is one function so that every
// CSRF-protected group — the admin, the cart, and the first-party catalog pages
// that carry an add-to-cart form — shares a single configuration and a single
// token pool.
func (h *Handler) withCSRF(next http.Handler) http.Handler {
	csrf := nosurf.New(next)
	csrf.SetBaseCookie(http.Cookie{
		// Path "/" because the protected routes span /admin, /cart, /cart/checkout
		// and the first-party catalog pages, and a cookie has only one path.
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   nosurf.MaxAge,
	})
	csrf.SetFailureHandler(http.HandlerFunc(h.csrfFailed))

	// nosurf compares a request's Origin against one it builds from the Host
	// header, and assumes https unless told otherwise — so without this every
	// form on a plain-HTTP deployment is rejected as cross-origin. BaseURL is
	// the right signal rather than r.TLS: behind a TLS-terminating proxy the
	// connection is plain HTTP but the browser's origin is https.
	csrf.SetIsTLSFunc(func(*http.Request) bool { return h.cfg.CookieSecure })
	return csrf
}

// csrfFailed answers a request whose CSRF token was missing or wrong. It is a
// 403 and nothing else: the request was either forged or made with a stale form,
// and neither case should be retried silently.
func (h *Handler) csrfFailed(w http.ResponseWriter, r *http.Request) {
	h.log.Warn("rejected request with a bad CSRF token",
		"method", r.Method, "path", r.URL.Path, "reason", nosurf.Reason(r))
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	http.Error(w, "the form you submitted has expired; reload the page and try again", http.StatusForbidden)
}

// RegisterAdmin wires the admin routes: the login form and the sign-out
// endpoint reachable by anyone, everything else behind protect.
//
// Protection is applied here, route by route, rather than by the caller. A
// middleware wrapped around a prefix somewhere else is one refactor away from
// silently no longer covering a new route; a handler registered without protect
// in this list is visible on the line that registers it.
func (h *Handler) RegisterAdmin(mux *http.ServeMux, protect middleware.Middleware) {
	mux.HandleFunc("GET /admin/login", h.adminLoginForm)
	// Rate limited, and only the POST: the form itself is harmless, and limiting a
	// GET would lock an operator out of the page they need to read the message on.
	// argon2id's cost already makes each attempt expensive, but cost is not a limit.
	mux.Handle("POST /admin/login", h.limits.login(http.HandlerFunc(h.adminLogin)))
	mux.HandleFunc("POST /admin/logout", h.adminLogout)

	admin := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, protect(handler))
	}
	admin("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
	})
	admin("GET /admin/products", h.adminProductList)
	admin("GET /admin/products/new", h.adminProductNew)
	admin("POST /admin/products", h.adminProductCreate)
	admin("GET /admin/products/{id}/edit", h.adminProductEdit)
	admin("POST /admin/products/{id}", h.adminProductUpdate)
	admin("POST /admin/products/{id}/delete", h.adminProductDelete)
	admin("POST /admin/products/{id}/variants", h.adminVariantCreate)
	admin("POST /admin/products/{id}/variants/{variantID}", h.adminVariantUpdate)
	admin("POST /admin/products/{id}/variants/{variantID}/delete", h.adminVariantDelete)
	admin("POST /admin/products/{id}/image", h.adminProductImageUpload)
	admin("POST /admin/products/{id}/image/delete", h.adminProductImageDelete)
	// Categories. Deleting one unlinks it from its products and never deletes them
	// — see internal/handler/admin_categories.go.
	admin("GET /admin/categories", h.adminCategoryList)
	admin("GET /admin/categories/new", h.adminCategoryNew)
	admin("POST /admin/categories", h.adminCategoryCreate)
	admin("GET /admin/categories/{id}/edit", h.adminCategoryEdit)
	admin("POST /admin/categories/{id}", h.adminCategoryUpdate)
	admin("POST /admin/categories/{id}/delete", h.adminCategoryDelete)
	// Read-only on purpose: only an authenticated gateway notification may change
	// an order. See internal/handler/admin_orders.go.
	admin("GET /admin/orders", h.adminOrderList)
	admin("GET /admin/orders/{id}", h.adminOrderShow)
}

// page is what every rendered page needs regardless of what it shows. It is
// embedded rather than repeated so that adding something universal — the CSRF
// token was exactly this — is one change, not one per page.
type page struct {
	Title     string
	StoreName string
	Currency  string
	CSRFToken string
}

func (h *Handler) newPage(r *http.Request, title string) page {
	return page{
		Title:     title,
		StoreName: h.cfg.StoreName,
		Currency:  h.cfg.Currency,
		CSRFToken: nosurf.Token(r),
	}
}

type productsPage struct {
	page
	Products []catalog.Product
}

type productFormPage struct {
	page

	IsNew   bool
	Product catalog.Product
	Errors  validate.FormErrors

	// Categories is the whole taxonomy, so the form can offer every category as a
	// checkbox; Product.Categories decides which are ticked.
	Categories []catalog.Category

	// Variant form state. VariantErrorID names the existing variant whose edit
	// failed, so the message renders on that row; when it is empty the errors
	// belong to the add-a-variant form.
	VariantForm    variantForm
	VariantErrors  validate.FormErrors
	VariantErrorID string

	// Image upload state. UploadsEnabled is false when no object storage is
	// configured, in which case the page offers a pasted URL and says so rather
	// than showing a form that could only fail.
	UploadsEnabled bool
	AcceptTypes    string
	MaxUploadMB    int64
}

// variantForm is the add-variant form's raw input, kept as typed so a
// rejected submission comes back with what was actually entered rather than a
// reformatted guess at it.
type variantForm struct {
	SKU      string
	Size     string
	Color    string
	Price    string
	StockQty string
	Active   bool
}

func (h *Handler) adminProductList(w http.ResponseWriter, r *http.Request) {
	products, err := h.cat.List(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_products", productsPage{
		page:     h.newPage(r, "Products"),
		Products: products,
	})
}

func (h *Handler) adminProductNew(w http.ResponseWriter, r *http.Request) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	h.render(w, r, http.StatusOK, "admin_product_form", h.productForm(r, catalog.Product{Active: true}, cats, true, nil))
}

func (h *Handler) adminProductCreate(w http.ResponseWriter, r *http.Request) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	p, errs, ok := h.parseProduct(w, r, cats)
	if !ok {
		return
	}

	for field, msg := range validate.Product(p) {
		errs.Add(field, msg)
	}
	if errs.Any() {
		h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", h.productForm(r, p, cats, true, errs))
		return
	}

	created, err := h.cat.Create(r.Context(), p)
	if err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs := validate.FormErrors{}
			errs.Add(conflict.Field, "Already used by another product.")
			h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", h.productForm(r, p, cats, true, errs))
			return
		}
		h.serverError(w, r, err)
		return
	}

	// Straight to the edit page: a product without variants cannot be bought,
	// so the next step is always adding one.
	http.Redirect(w, r, "/admin/products/"+created.ID+"/edit", http.StatusSeeOther)
}

func (h *Handler) adminProductEdit(w http.ResponseWriter, r *http.Request) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	p, err := h.cat.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_product_form", h.productForm(r, p, cats, false, nil))
}

func (h *Handler) adminProductUpdate(w http.ResponseWriter, r *http.Request) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	p, errs, ok := h.parseProduct(w, r, cats)
	if !ok {
		return
	}
	p.ID = r.PathValue("id")

	// Nothing here has to defend the image any more: UpdateProduct does not write
	// either image column, so the form cannot touch the picture whatever it submits.
	// That replaced a read-then-preserve dance in this function.
	for field, msg := range validate.Product(p) {
		errs.Add(field, msg)
	}
	if errs.Any() {
		h.renderProductForm(w, r, http.StatusUnprocessableEntity, p, cats, errs)
		return
	}

	if _, err := h.cat.Update(r.Context(), p); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs := validate.FormErrors{}
			errs.Add(conflict.Field, "Already used by another product.")
			h.renderProductForm(w, r, http.StatusUnprocessableEntity, p, cats, errs)
			return
		}
		h.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

func (h *Handler) adminProductDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.cat.Delete(r.Context(), id)
	switch {
	case err == nil:
		http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
	case errors.Is(err, catalog.ErrInUse):
		// The product has been ordered. Deleting it would rewrite history, so
		// the form says so instead of failing opaquely.
		cats, ok := h.categories(w, r)
		if !ok {
			return
		}
		p, getErr := h.cat.Get(r.Context(), id)
		if getErr != nil {
			h.storeError(w, r, getErr)
			return
		}
		errs := validate.FormErrors{}
		errs.Add("delete", "This product has been ordered and cannot be deleted. Deactivate it instead.")
		h.render(w, r, http.StatusConflict, "admin_product_form", h.productForm(r, p, cats, false, errs))
	default:
		h.storeError(w, r, err)
	}
}

func (h *Handler) adminVariantCreate(w http.ResponseWriter, r *http.Request) {
	productID := r.PathValue("id")
	v, form, errs, ok := h.parseVariant(w, r)
	if !ok {
		return
	}
	v.ProductID = productID

	if errs.Any() {
		h.renderVariantErrors(w, r, http.StatusUnprocessableEntity, productID, form, "", errs)
		return
	}

	if _, err := h.cat.CreateVariant(r.Context(), v); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs.Add(conflict.Field, conflictMessage(conflict.Field))
			h.renderVariantErrors(w, r, http.StatusUnprocessableEntity, productID, form, "", errs)
			return
		}
		h.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+productID+"/edit", http.StatusSeeOther)
}

func (h *Handler) adminVariantUpdate(w http.ResponseWriter, r *http.Request) {
	productID, variantID := r.PathValue("id"), r.PathValue("variantID")
	v, form, errs, ok := h.parseVariant(w, r)
	if !ok {
		return
	}
	v.ID, v.ProductID = variantID, productID

	if errs.Any() {
		h.renderVariantErrors(w, r, http.StatusUnprocessableEntity, productID, form, variantID, errs)
		return
	}

	if _, err := h.cat.UpdateVariant(r.Context(), v); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs.Add(conflict.Field, conflictMessage(conflict.Field))
			h.renderVariantErrors(w, r, http.StatusUnprocessableEntity, productID, form, variantID, errs)
			return
		}
		h.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+productID+"/edit", http.StatusSeeOther)
}

func (h *Handler) adminVariantDelete(w http.ResponseWriter, r *http.Request) {
	productID, variantID := r.PathValue("id"), r.PathValue("variantID")
	err := h.cat.DeleteVariant(r.Context(), productID, variantID)
	switch {
	case err == nil:
		http.Redirect(w, r, "/admin/products/"+productID+"/edit", http.StatusSeeOther)
	case errors.Is(err, catalog.ErrInUse):
		errs := validate.FormErrors{}
		errs.Add("sku", "This variant has been ordered and cannot be deleted. Deactivate it instead.")
		h.renderVariantErrors(w, r, http.StatusConflict, productID, variantForm{Active: true}, variantID, errs)
	default:
		h.storeError(w, r, err)
	}
}

// parseProduct reads the product form. A blank slug is derived from the title,
// because a slug is a detail of the URL, not a decision the operator has to
// make on every product.
//
// known is the taxonomy the form was rendered from; the submitted category ids
// are resolved against it, so an id that names nothing is a message on the form
// rather than a foreign key violation with no field attached.
func (h *Handler) parseProduct(w http.ResponseWriter, r *http.Request, known []catalog.Category) (catalog.Product, validate.FormErrors, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return catalog.Product{}, nil, false
	}
	p := catalog.Product{
		Slug:        strings.TrimSpace(r.PostFormValue("slug")),
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		// No image_url: the form does not offer one, and reading it here would be a
		// way to set it by hand-crafting a request. Images arrive by upload only.
		Active: r.PostFormValue("active") != "",
	}
	if p.Slug == "" {
		p.Slug = catalog.Slugify(p.Title)
	}

	// A repeated field rather than one comma-separated value, because that is what
	// a checkbox list submits natively — no JavaScript, and no parsing of a format
	// somebody has to get right.
	chosen, errs := validate.ProductCategories(r.PostForm["category"], known)
	p.Categories = chosen
	return p, errs, true
}

// categories loads the taxonomy for a form that has to render it, answering the
// request itself if the read fails.
func (h *Handler) categories(w http.ResponseWriter, r *http.Request) ([]catalog.Category, bool) {
	cats, err := h.cat.Categories(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return nil, false
	}
	return cats, true
}

// parseVariant reads a variant form, returning both the parsed variant and the
// raw input, so a rejected form can be re-rendered exactly as it was typed.
func (h *Handler) parseVariant(w http.ResponseWriter, r *http.Request) (catalog.Variant, variantForm, validate.FormErrors, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return catalog.Variant{}, variantForm{}, nil, false
	}

	form := variantForm{
		SKU:      strings.TrimSpace(r.PostFormValue("sku")),
		Size:     strings.TrimSpace(r.PostFormValue("size")),
		Color:    strings.TrimSpace(r.PostFormValue("color")),
		Price:    strings.TrimSpace(r.PostFormValue("price")),
		StockQty: strings.TrimSpace(r.PostFormValue("stock_qty")),
		Active:   r.PostFormValue("active") != "",
	}
	v := catalog.Variant{
		SKU:    form.SKU,
		Size:   form.Size,
		Color:  form.Color,
		Active: form.Active,
	}

	errs := validate.FormErrors{}
	cents, err := catalog.ParsePrice(form.Price)
	if err != nil {
		errs.Add("price", "Enter an amount like 149.99.")
	}
	v.PriceCents = cents

	stock, err := strconv.Atoi(form.StockQty)
	if err != nil || stock < 0 {
		errs.Add("stock_qty", "Enter a whole number of items, 0 or more.")
	} else {
		v.StockQty = stock
	}

	for field, msg := range validate.Variant(v) {
		errs.Add(field, msg)
	}
	return v, form, errs, true
}

func (h *Handler) productForm(r *http.Request, p catalog.Product, cats []catalog.Category, isNew bool, errs validate.FormErrors) productFormPage {
	title := "Edit product"
	if isNew {
		title = "New product"
	}
	return productFormPage{
		page:           h.newPage(r, title),
		IsNew:          isNew,
		Product:        p,
		Categories:     cats,
		Errors:         errs,
		VariantForm:    variantForm{Active: true},
		UploadsEnabled: h.cfg.ImagesEnabled(),
		AcceptTypes:    strings.Join(blob.SupportedTypes(), ","),
		MaxUploadMB:    blob.MaxUploadBytes >> 20,
	}
}

// renderProductForm re-renders the edit page for a rejected product form,
// keeping the submitted values but showing the variants as stored.
func (h *Handler) renderProductForm(w http.ResponseWriter, r *http.Request, status int, p catalog.Product, cats []catalog.Category, errs validate.FormErrors) {
	variants, err := h.cat.Variants(r.Context(), p.ID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	p.Variants = variants
	h.render(w, r, status, "admin_product_form", h.productForm(r, p, cats, false, errs))
}

// renderVariantErrors re-renders the edit page after a variant form was
// rejected. The product is re-read, so everything except the offending form is
// shown as stored.
func (h *Handler) renderVariantErrors(w http.ResponseWriter, r *http.Request, status int, productID string, form variantForm, variantID string, errs validate.FormErrors) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	p, err := h.cat.Get(r.Context(), productID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	page := h.productForm(r, p, cats, false, nil)
	page.VariantForm = form
	page.VariantErrors = errs
	page.VariantErrorID = variantID
	h.render(w, r, status, "admin_product_form", page)
}

func conflictMessage(field string) string {
	if field == "options" {
		return "Another variant of this product already has that size and colour."
	}
	return "Already used by another variant."
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	if err := h.tmpl.Render(w, status, name, data); err != nil {
		// The status line may already be written, so this cannot become a 500;
		// log it loudly instead.
		h.log.Error("render failed", "template", name, "path", r.URL.Path, "error", err)
	}
}

// storeError maps a catalog error onto a response: missing rows are 404s, and
// anything else is a genuine server fault.
func (h *Handler) storeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, catalog.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.serverError(w, r, err)
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("server error", "method", r.Method, "path", r.URL.Path, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

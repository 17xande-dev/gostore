package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Handler holds everything the HTTP layer needs. It is created once at startup
// and is safe for concurrent use.
type Handler struct {
	cfg  config.Config
	log  *slog.Logger
	tmpl *Templates
	cat  *catalog.Store
}

func New(cfg config.Config, log *slog.Logger, tmpl *Templates, cat *catalog.Store) *Handler {
	return &Handler{cfg: cfg, log: log, tmpl: tmpl, cat: cat}
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
	mux.HandleFunc("POST /admin/login", h.adminLogin)
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
}

type productsPage struct {
	Title     string
	StoreName string
	Currency  string
	Products  []catalog.Product
}

type productFormPage struct {
	Title     string
	StoreName string
	Currency  string

	IsNew   bool
	Product catalog.Product
	Errors  validate.FormErrors

	// Variant form state. VariantErrorID names the existing variant whose edit
	// failed, so the message renders on that row; when it is empty the errors
	// belong to the add-a-variant form.
	VariantForm    variantForm
	VariantErrors  validate.FormErrors
	VariantErrorID string
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
		Title:     "Products",
		StoreName: h.cfg.StoreName,
		Currency:  h.cfg.Currency,
		Products:  products,
	})
}

func (h *Handler) adminProductNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "admin_product_form", h.productForm(catalog.Product{Active: true}, true, nil))
}

func (h *Handler) adminProductCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.parseProduct(w, r)
	if !ok {
		return
	}

	if errs := validate.Product(p); errs.Any() {
		h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", h.productForm(p, true, errs))
		return
	}

	created, err := h.cat.Create(r.Context(), p)
	if err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs := validate.FormErrors{}
			errs.Add(conflict.Field, "Already used by another product.")
			h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", h.productForm(p, true, errs))
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
	p, err := h.cat.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_product_form", h.productForm(p, false, nil))
}

func (h *Handler) adminProductUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.parseProduct(w, r)
	if !ok {
		return
	}
	p.ID = r.PathValue("id")

	if errs := validate.Product(p); errs.Any() {
		h.renderProductForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}

	if _, err := h.cat.Update(r.Context(), p); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs := validate.FormErrors{}
			errs.Add(conflict.Field, "Already used by another product.")
			h.renderProductForm(w, r, http.StatusUnprocessableEntity, p, errs)
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
		p, getErr := h.cat.Get(r.Context(), id)
		if getErr != nil {
			h.storeError(w, r, getErr)
			return
		}
		errs := validate.FormErrors{}
		errs.Add("delete", "This product has been ordered and cannot be deleted. Deactivate it instead.")
		h.render(w, r, http.StatusConflict, "admin_product_form", h.productForm(p, false, errs))
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
func (h *Handler) parseProduct(w http.ResponseWriter, r *http.Request) (catalog.Product, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return catalog.Product{}, false
	}
	p := catalog.Product{
		Kind:        strings.TrimSpace(r.PostFormValue("kind")),
		Slug:        strings.TrimSpace(r.PostFormValue("slug")),
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		ImageURL:    strings.TrimSpace(r.PostFormValue("image_url")),
		Active:      r.PostFormValue("active") != "",
	}
	if p.Slug == "" {
		p.Slug = catalog.Slugify(p.Title)
	}
	return p, true
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

func (h *Handler) productForm(p catalog.Product, isNew bool, errs validate.FormErrors) productFormPage {
	title := "Edit product"
	if isNew {
		title = "New product"
	}
	return productFormPage{
		Title:       title,
		StoreName:   h.cfg.StoreName,
		Currency:    h.cfg.Currency,
		IsNew:       isNew,
		Product:     p,
		Errors:      errs,
		VariantForm: variantForm{Active: true},
	}
}

// renderProductForm re-renders the edit page for a rejected product form,
// keeping the submitted values but showing the variants as stored.
func (h *Handler) renderProductForm(w http.ResponseWriter, r *http.Request, status int, p catalog.Product, errs validate.FormErrors) {
	variants, err := h.cat.Variants(r.Context(), p.ID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	p.Variants = variants
	h.render(w, r, status, "admin_product_form", h.productForm(p, false, errs))
}

// renderVariantErrors re-renders the edit page after a variant form was
// rejected. The product is re-read, so everything except the offending form is
// shown as stored.
func (h *Handler) renderVariantErrors(w http.ResponseWriter, r *http.Request, status int, productID string, form variantForm, variantID string, errs validate.FormErrors) {
	p, err := h.cat.Get(r.Context(), productID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	page := h.productForm(p, false, nil)
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

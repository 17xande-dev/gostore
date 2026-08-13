package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/validate"
)

// The category admin. It is deliberately plainer than the product admin: a
// category is four columns and has no variants, no images and no history to
// protect, so there is nothing here that a list, a form and a delete do not cover.

type categoriesPage struct {
	page
	Categories []catalog.Category

	// Notice carries the outcome of a delete, which is the one operation here whose
	// effect is invisible on the page that follows it: unlinking twelve products
	// and unlinking none both leave a list with one fewer row.
	Notice string
	Errors validate.FormErrors
}

type categoryFormPage struct {
	page
	IsNew    bool
	Category catalog.Category

	// Position is kept as submitted rather than as parsed, so a rejected form comes
	// back showing what was typed instead of the zero an unparseable value became.
	Position string
	Errors   validate.FormErrors
}

func (h *Handler) adminCategoryList(w http.ResponseWriter, r *http.Request) {
	h.renderCategoryList(w, r, http.StatusOK, "", nil)
}

func (h *Handler) renderCategoryList(w http.ResponseWriter, r *http.Request, status int, notice string, errs validate.FormErrors) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	h.render(w, r, status, "admin_categories", categoriesPage{
		page:       h.newPage(r, "Categories"),
		Categories: cats,
		Notice:     notice,
		Errors:     errs,
	})
}

func (h *Handler) adminCategoryNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "admin_category_form", h.categoryForm(r, catalog.Category{}, "0", true, nil))
}

func (h *Handler) adminCategoryCreate(w http.ResponseWriter, r *http.Request) {
	c, position, errs, ok := h.parseCategory(w, r)
	if !ok {
		return
	}
	if errs.Any() {
		h.render(w, r, http.StatusUnprocessableEntity, "admin_category_form", h.categoryForm(r, c, position, true, errs))
		return
	}

	if _, err := h.cat.CreateCategory(r.Context(), c); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs.Add(conflict.Field, "Already used by another category.")
			h.render(w, r, http.StatusUnprocessableEntity, "admin_category_form", h.categoryForm(r, c, position, true, errs))
			return
		}
		h.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

func (h *Handler) adminCategoryEdit(w http.ResponseWriter, r *http.Request) {
	c, err := h.cat.Category(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_category_form",
		h.categoryForm(r, c, strconv.Itoa(c.Position), false, nil))
}

func (h *Handler) adminCategoryUpdate(w http.ResponseWriter, r *http.Request) {
	c, position, errs, ok := h.parseCategory(w, r)
	if !ok {
		return
	}
	c.ID = r.PathValue("id")

	if errs.Any() {
		h.render(w, r, http.StatusUnprocessableEntity, "admin_category_form", h.categoryForm(r, c, position, false, errs))
		return
	}

	if _, err := h.cat.UpdateCategory(r.Context(), c); err != nil {
		if conflict, ok := errors.AsType[*catalog.ConflictError](err); ok {
			errs.Add(conflict.Field, "Already used by another category.")
			h.render(w, r, http.StatusUnprocessableEntity, "admin_category_form", h.categoryForm(r, c, position, false, errs))
			return
		}
		h.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// adminCategoryDelete removes a category. Unlike deleting a product this is never
// refused: the join rows go with it and the products themselves are untouched,
// because a taxonomy edit must not delete catalog entries.
//
// It reports how many products were unlinked, because otherwise the operation is
// indistinguishable from one that did nothing.
func (h *Handler) adminCategoryDelete(w http.ResponseWriter, r *http.Request) {
	unlinked, err := h.cat.DeleteCategory(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	notice := "Category deleted. It was not used by any product."
	if unlinked == 1 {
		notice = "Category deleted, and removed from 1 product. The product itself is untouched."
	} else if unlinked > 1 {
		notice = "Category deleted, and removed from " + strconv.FormatInt(unlinked, 10) +
			" products. The products themselves are untouched."
	}
	h.renderCategoryList(w, r, http.StatusOK, notice, nil)
}

// parseCategory reads the category form. A blank slug is derived from the name,
// on the same grounds as a product's: the slug is a detail of the URL rather than
// a decision worth making twice.
func (h *Handler) parseCategory(w http.ResponseWriter, r *http.Request) (catalog.Category, string, validate.FormErrors, bool) {
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return catalog.Category{}, "", nil, false
	}

	c := catalog.Category{
		Slug: strings.TrimSpace(r.PostFormValue("slug")),
		Name: strings.TrimSpace(r.PostFormValue("name")),
	}
	if c.Slug == "" {
		c.Slug = catalog.Slugify(c.Name)
	}

	position := strings.TrimSpace(r.PostFormValue("position"))
	errs := validate.FormErrors{}
	switch n, err := strconv.Atoi(position); {
	case position == "":
		c.Position = 0
	case err != nil:
		errs.Add("position", "Enter a whole number. Lower numbers are shown first.")
	default:
		c.Position = n
	}

	for field, msg := range validate.Category(c) {
		errs.Add(field, msg)
	}
	return c, position, errs, true
}

func (h *Handler) categoryForm(r *http.Request, c catalog.Category, position string, isNew bool, errs validate.FormErrors) categoryFormPage {
	title := "Edit category"
	if isNew {
		title = "New category"
	}
	return categoryFormPage{
		page:     h.newPage(r, title),
		IsNew:    isNew,
		Category: c,
		Position: position,
		Errors:   errs,
	}
}

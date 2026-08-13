package handler

import "net/http"

// The not-found page. Every HTML surface in the store ends up here — an unknown
// URL, a withdrawn product, a page number past the end of the catalog, an order id
// that is not one — so a visitor who mistypes something gets a way onward rather
// than Go's plain `404 page not found`.
//
// Byte endpoints deliberately do not use it. A missing image or asset answers with
// the plain text, because nothing is going to read an HTML page in an <img> tag and
// sending one would only make the failure larger.

type notFoundPageData struct {
	page
}

// notFound renders the 404 page, falling back to the plain one if the template
// cannot be rendered.
//
// The fallback matters more here than anywhere else: this is the response an
// adopter's broken theme override is most likely to break, and a 404 that fails to
// render must still be a 404. h.render would log and leave the status at 200,
// which is the one outcome a crawler must never see.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	data := notFoundPageData{page: h.newPage(r, "Page not found")}
	if err := h.tmpl.Render(w, http.StatusNotFound, "not_found", data); err != nil {
		h.log.Error("render failed", "template", "not_found", "path", r.URL.Path, "error", err)
		http.NotFound(w, r)
	}
}

// plainNotFound is Go's own 404, for the places where HTML would be the wrong
// answer: a CORS preflight, and anything serving bytes rather than a page.
func plainNotFound(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

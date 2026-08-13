package handler

import (
	"net/http"

	"github.com/17xande-dev/gostore/internal/catalog"
)

// The index page: the store's front door, and the page most likely to be replaced
// wholesale by whoever runs the shop.
//
// It has two jobs that pull against each other — work out of the box, and be
// obviously somebody else's to change — so it carries the least that is still a
// page: the store name, a line to replace, a few products, and the way to the
// catalog. Everything on it is already overridable by name from TEMPLATE_DIR, so
// customising it needs no fork and no rebuild.
//
// It is deliberately not a fragment. The catalog answers twice over because it is
// embeddable; the index is this store's own address and there is nothing to embed.

// indexProducts is how many products the front page shows. Not configurable, on
// the same grounds as pageSize: a shop that wants a different number is
// overriding the template anyway, and another environment variable to document
// buys nothing. Four fills one row of the auto-fill grid on a wide screen and two
// on a phone.
const indexProducts = 4

type indexPageData struct {
	page
	Products []catalog.Product
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	products, err := h.cat.NewestActive(r.Context(), indexProducts)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "index", indexPageData{
		page:     h.newPage(r, "Home"),
		Products: products,
	})
}

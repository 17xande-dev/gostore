package handler

import (
	"errors"
	"net/http"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/orders"
)

// The admin's view of orders is read-only, and stays that way for a reason: an
// order is a record of something that happened, and the only thing allowed to
// change one is an authenticated gateway notification. A button here that marked an
// order paid would be a way to record money that never arrived.
//
// What the operator needs is what these two pages show: what to pack, where to
// send it, and — on the detail page — exactly what the gateway said, for the day a
// customer and a bank disagree.

type ordersPage struct {
	page
	Orders []orders.Order
	Limit  int
}

type orderPage struct {
	page
	Order orders.Order
}

func (h *Handler) adminOrderList(w http.ResponseWriter, r *http.Request) {
	list, err := h.orders.List(r.Context(), orders.DefaultListLimit)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_orders", ordersPage{
		page:   h.newPage(r, "Orders"),
		Orders: list,
		Limit:  orders.DefaultListLimit,
	})
}

func (h *Handler) adminOrderShow(w http.ResponseWriter, r *http.Request) {
	order, err := h.orders.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.orderError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_order", orderPage{
		page:  h.newPage(r, "Order "+order.Reference()),
		Order: order,
	})
}

// orderError maps an orders error onto a response. It exists separately from
// storeError because the two packages have their own ErrNotFound, and one
// package's sentinel does not match the other's.
func (h *Handler) orderError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, orders.ErrNotFound) {
		h.notFound(w, r)
		return
	}
	h.serverError(w, r, err)
}

// formatCents is catalog.FormatPrice under a name that says what it takes, for the
// email subject lines that cannot go through a template function.
func formatCents(cents int64) string { return catalog.FormatPrice(cents) }

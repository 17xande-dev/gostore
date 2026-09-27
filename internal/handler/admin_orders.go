package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/downloads"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/outbox"
)

// Payment facts stay read-only. Fulfillment, download access and email delivery
// are separate operational state the administrator can manage.

type ordersPage struct {
	page
	Orders   []orders.Order
	Search   string
	Filter   string
	Number   int
	Previous string
	Next     string
}

type orderPage struct {
	page
	Order orders.Order
	// Entitlements are the download grants this order created, with a revoke
	// button each. Empty for an order of physical goods only.
	Entitlements  []downloads.OrderEntitlement
	Emails        []outbox.Status
	PendingEmails bool
}

func (h *Handler) adminOrderList(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := r.URL.Query().Get("filter")
	number := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		number, err = strconv.Atoi(raw)
		if err != nil || number < 1 || number > 1000000 {
			h.badForm(w, r)
			return
		}
	}
	if len(search) > 200 {
		h.badForm(w, r)
		return
	}
	switch filter {
	case "", "oversold", "email", "unfulfilled":
	default:
		h.badForm(w, r)
		return
	}
	list, next, err := h.orders.Search(r.Context(), search, filter, number)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	data := ordersPage{
		page:   h.newPage(r, "Orders"),
		Orders: list,
		Search: search, Filter: filter, Number: number,
	}
	link := func(page int) string {
		return "/admin/orders?" + url.Values{"q": {search}, "filter": {filter}, "page": {strconv.Itoa(page)}}.Encode()
	}
	if number > 1 {
		data.Previous = link(number - 1)
	}
	if next {
		data.Next = link(number + 1)
	}
	h.render(w, r, http.StatusOK, "admin_orders", data)
}

func (h *Handler) adminOrderFulfillment(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return
	}
	state := r.PostFormValue("fulfilled")
	tracking, note := strings.TrimSpace(r.PostFormValue("tracking")), strings.TrimSpace(r.PostFormValue("note"))
	if (state != "0" && state != "1") || len(tracking) > 200 || len(note) > 4000 {
		h.badForm(w, r)
		return
	}
	actor, ok := middleware.AdminUser(r)
	if !ok {
		h.clientError(w, r, http.StatusForbidden, "Sign in required", "Sign in before updating an order.")
		return
	}
	err := h.orders.SetFulfillment(r.Context(), r.PathValue("id"), actor.ID, state == "1", tracking, note)
	if errors.Is(err, orders.ErrNotFulfillable) {
		h.clientError(w, r, http.StatusConflict, "Cannot fulfill this order", "Only paid orders containing physical goods can be fulfilled.")
		return
	}
	if err != nil {
		h.orderError(w, r, err)
		return
	}
	h.logger(r).Info("updated order fulfillment", "order", r.PathValue("id"), "actor", actor.ID, "fulfilled", state == "1")
	http.Redirect(w, r, "/admin/orders/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) adminOrderShow(w http.ResponseWriter, r *http.Request) {
	order, err := h.orders.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.orderError(w, r, err)
		return
	}
	// Read unconditionally rather than only for an order with digital lines: the
	// kind lives on order_items, so deciding here would mean scanning them for the
	// same answer this query already gives, and the query is a no-op for the
	// common case.
	grants, err := h.grants.ForOrder(r.Context(), order.ID)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	var emails []outbox.Status
	var pending bool
	if h.outbox != nil {
		emails, err = h.outbox.ForOrder(r.Context(), order.ID)
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		for _, email := range emails {
			if email.SentAt == nil {
				pending = true
			}
		}
	}
	h.render(w, r, http.StatusOK, "admin_order", orderPage{
		page:          h.newPage(r, "Order "+order.Reference()),
		Order:         order,
		Entitlements:  grants,
		Emails:        emails,
		PendingEmails: pending,
	})
}

func (h *Handler) adminOrderEmailRetry(w http.ResponseWriter, r *http.Request) {
	order, err := h.orders.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.orderError(w, r, err)
		return
	}
	if err := h.outbox.Retry(r.Context(), order.ID); err != nil {
		h.serverError(w, r, err)
		return
	}
	h.logger(r).Info("queued order emails for retry", "order", order.ID)
	http.Redirect(w, r, "/admin/orders/"+order.ID, http.StatusSeeOther)
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

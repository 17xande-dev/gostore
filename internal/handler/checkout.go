package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/17xande-dev/gostore/internal/cart"
	"github.com/17xande-dev/gostore/internal/orders"
	"github.com/17xande-dev/gostore/internal/payment"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Checkout lives under /cart, not at /checkout as the plan's route list had it.
// The reason is the cart cookie: it is scoped to /cart so that the catalog pages
// stay genuinely cookie-free and embeddable, and a page at /checkout would
// therefore not be sent the token that identifies the basket it is meant to be
// checking out. The alternatives were widening the cookie to / — which gives the
// catalog a cookie back, to fix a problem in a different place — or a second
// cookie, which is worse. Nesting the checkout inside the cart's path costs a URL
// segment, needs no new routing, and puts it inside the CSRF group already.
//
// The flow is deliberately three steps with a hard boundary in the middle:
//
//	GET  /cart/checkout          the form, alongside what is being bought
//	POST /cart/checkout          creates a *pending* order and hands over to the gateway
//	     ...the shopper pays on the gateway's own site...
//	POST /payments/{gw}/callback the only thing that can mark an order paid
//	GET  /cart/checkout/success  informational, and nothing more than that
//
// /cart/checkout/success is where the gateway sends the shopper's browser back
// to. It grants nothing: a shopper can navigate there directly without paying, so
// it says the payment is being confirmed rather than that it succeeded. Anything
// that depends on money having moved hangs off the callback.

type checkoutPageData struct {
	page
	Cart cart.Cart
	Form checkoutForm
	// Errors are per-field messages for the form; Error is one message about the
	// checkout as a whole, such as the cart having become unbuyable.
	Errors validate.FormErrors
	Error  string
}

// redirectPageData is the hand-over to the gateway: a form of hidden fields that
// submits itself.
type redirectPageData struct {
	page
	Order   orders.Order
	Action  string
	Fields  []payment.Field
	Gateway string
}

type successPageData struct {
	page
	// Order is the order just placed, when it can be identified from the cart
	// cookie, and zero otherwise — a shopper who arrives here with no cookie gets
	// the generic message rather than an error.
	Order      orders.Order
	HaveOrder  bool
	Cancelled  bool
	SupportRef string
}

// checkoutForm is the shipping form's raw input, kept as typed so a rejected
// submission comes back with what was actually entered.
type checkoutForm struct {
	Name    string
	Email   string
	Phone   string
	Address string
}

func (h *Handler) registerCheckout(mux *http.ServeMux) {
	mux.HandleFunc("GET /cart/checkout", h.checkoutShow)
	// Rate limited, because this is the route that writes order rows. Loose enough
	// that a shopper who double-clicks the pay button never meets it.
	mux.Handle("POST /cart/checkout", h.limits.checkout(http.HandlerFunc(h.checkoutSubmit)))
	mux.HandleFunc("GET /cart/checkout/success", h.checkoutSuccess)
	mux.HandleFunc("GET /cart/checkout/cancel", h.checkoutCancel)
}

func (h *Handler) checkoutShow(w http.ResponseWriter, r *http.Request) {
	c := h.currentCart(r)
	if !c.Purchasable() {
		// Nothing to check out, or something in the basket cannot be bought. Either
		// way the cart page is where the problem is explained, so send them there
		// rather than showing a form that cannot succeed.
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}
	h.renderCheckout(w, r, http.StatusOK, c, checkoutForm{}, nil, "")
}

func (h *Handler) checkoutSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return
	}

	token := h.tokenFromCookie(r)
	c := h.cartFor(r, token)
	if !c.Purchasable() {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}

	form := checkoutForm{
		Name:    strings.TrimSpace(r.PostFormValue("name")),
		Email:   strings.TrimSpace(r.PostFormValue("email")),
		Phone:   strings.TrimSpace(r.PostFormValue("phone")),
		Address: strings.TrimSpace(r.PostFormValue("address")),
	}
	customer := orders.Customer{Name: form.Name, Email: form.Email, Phone: form.Phone, Address: form.Address}

	if errs := validate.Customer(customer); errs.Any() {
		h.renderCheckout(w, r, http.StatusUnprocessableEntity, c, form, errs, "")
		return
	}

	// The order's total is computed inside this call, from prices read in the same
	// transaction — never from the figure the submitted page was showing, because
	// that is the number that will be checked against what the gateway says was
	// paid.
	order, err := h.orders.CreateFromCart(r.Context(), token, customer, h.cfg.Currency, h.gateway.Name())
	if err != nil {
		var unavailable *orders.UnavailableError
		switch {
		case errors.Is(err, orders.ErrEmptyCart):
			http.Redirect(w, r, "/cart", http.StatusSeeOther)
		case errors.As(err, &unavailable):
			// The catalog changed between rendering the cart and submitting it.
			h.renderCheckout(w, r, http.StatusConflict, h.cartFor(r, token), form, nil,
				strings.Join(unavailable.Problems, " "))
		default:
			h.serverError(w, r, err)
		}
		return
	}

	action, fields, err := h.gateway.BuildRedirectForm(payment.Request{
		OrderID:     order.ID,
		AmountCents: order.TotalCents,
		Currency:    order.Currency,
		ItemName:    h.cfg.StoreName + " order " + order.Reference(),
		NameFirst:   customer.FirstName(),
		NameLast:    customer.LastName(),
		Email:       customer.Email,
	})
	if err != nil {
		// The order exists and is pending, which is correct: it records that a
		// checkout was attempted, and a pending order nobody paid for is harmless.
		// What is not harmless is a shopper seeing a blank page, so this reports the
		// gateway's own complaint — a below-minimum total, most likely.
		h.log.Error("build gateway redirect", "order", order.ID, "gateway", h.gateway.Name(), "error", err)
		h.renderCheckout(w, r, http.StatusUnprocessableEntity, h.cartFor(r, token), form, nil,
			"This order cannot be sent for payment: "+err.Error())
		return
	}

	h.log.Info("checkout created a pending order",
		"order", order.ID, "total_cents", order.TotalCents, "gateway", h.gateway.Name())

	h.render(w, r, http.StatusOK, "checkout_redirect", redirectPageData{
		page:    h.newPage(r, "Redirecting to payment"),
		Order:   order,
		Action:  action,
		Fields:  fields,
		Gateway: h.gateway.Name(),
	})
}

// checkoutSuccess is the gateway's return_url. It is informational only — see the
// comment at the top of this file — and says so.
func (h *Handler) checkoutSuccess(w http.ResponseWriter, r *http.Request) {
	h.renderOutcome(w, r, "checkout_success", false)
}

// checkoutCancel is the gateway's cancel_url: the shopper backed out. The order
// stays pending, and the cart is untouched, so they can try again.
func (h *Handler) checkoutCancel(w http.ResponseWriter, r *http.Request) {
	h.renderOutcome(w, r, "checkout_cancel", true)
}

func (h *Handler) renderOutcome(w http.ResponseWriter, r *http.Request, name string, cancelled bool) {
	data := successPageData{page: h.newPage(r, "Thank you"), Cancelled: cancelled}
	if cancelled {
		data.page = h.newPage(r, "Payment cancelled")
	}

	// The cart cookie is the only thing identifying the shopper here, and it is
	// enough: it names their own basket and the orders placed from it, and nothing
	// else. A missing or stale cookie is not an error, just a generic page.
	if token := h.tokenFromCookie(r); token != "" {
		order, err := h.orders.LatestForCart(r.Context(), token)
		switch {
		case err == nil:
			data.Order = order
			data.HaveOrder = true
			data.SupportRef = order.Reference()
		case errors.Is(err, orders.ErrNotFound):
		default:
			h.log.Error("read order for outcome page", "error", err)
		}
	}
	h.render(w, r, http.StatusOK, name, data)
}

func (h *Handler) renderCheckout(w http.ResponseWriter, r *http.Request, status int, c cart.Cart, form checkoutForm, errs validate.FormErrors, problem string) {
	h.render(w, r, status, fragmentOr(r, "checkout_form", "checkout"), checkoutPageData{
		page:   h.newPage(r, "Checkout"),
		Cart:   c,
		Form:   form,
		Errors: errs,
		Error:  problem,
	})
}

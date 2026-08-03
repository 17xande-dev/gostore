package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/17xande-dev/gostore/internal/cart"
)

// CartCookieName is the cookie holding the cart token. It is scoped to /cart, so
// it is never sent with the storefront reads that are meant to be cacheable and
// embeddable.
const CartCookieName = "cart_token"

// cartCookieTTL matches the plan's 30 days: long enough that a shopper can come
// back tomorrow, short enough that the cleanup job has something to do.
const cartCookieTTL = 30 * 24 * time.Hour

type cartPageData struct {
	page
	Cart cart.Cart
	// Notice is a message about what just happened — added, removed, or refused.
	Notice string
	Error  string
}

func (h *Handler) registerCart(mux *http.ServeMux) {
	mux.HandleFunc("GET /cart", h.cartShow)
	// A product page cannot read the cart itself — the cookie is scoped to /cart
	// so that the catalog stays cookie-free — so it asks for this fragment
	// instead.
	mux.HandleFunc("GET /cart/status", h.cartStatus)
	mux.HandleFunc("POST /cart/items", h.cartAdd)
	mux.HandleFunc("POST /cart/items/{variantID}", h.cartUpdate)
	mux.HandleFunc("DELETE /cart/items/{variantID}", h.cartDelete)
}

func (h *Handler) cartShow(w http.ResponseWriter, r *http.Request) {
	c := h.currentCart(r)
	h.renderCart(w, r, http.StatusOK, c, "", "")
}

// cartStatus renders the small "N items in your cart" block. It is always a
// fragment: nothing navigates here.
func (h *Handler) cartStatus(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "cart_status", cartPageData{
		page: h.newPage(r, "Cart"), Cart: h.currentCart(r),
	})
}

func (h *Handler) cartAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}

	variantID := r.PostFormValue("variant_id")
	quantity, err := strconv.Atoi(orDefault(r.PostFormValue("quantity"), "1"))
	if err != nil {
		h.cartProblem(w, r, "Enter a whole number of items.")
		return
	}

	// The cart row is created on the first add, not on every visit: browsing the
	// store must not leave a trail of empty carts in the table.
	token, err := h.cartToken(w, r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	switch err := h.cart.Add(r.Context(), token, variantID, quantity); {
	case err == nil:
	case errors.Is(err, cart.ErrNotFound):
		// The cookie named a cart that no longer exists. Start a fresh one and
		// retry once, so a shopper returning after a cleanup is not stuck.
		token, err = h.newCart(w, r)
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		if err := h.cart.Add(r.Context(), token, variantID, quantity); err != nil {
			h.cartAddFailed(w, r, err)
			return
		}
	default:
		h.cartAddFailed(w, r, err)
		return
	}

	c := h.cartFor(r, token)
	if isHTMX(r) {
		// The product page swaps a small status block, so adding to the cart does
		// not throw away the page the shopper is reading.
		h.render(w, r, http.StatusOK, "cart_status", cartPageData{
			page: h.newPage(r, "Cart"), Cart: c, Notice: "Added to your cart.",
		})
		return
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

func (h *Handler) cartUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}

	// Quantity zero removes the line, which is how the remove button works
	// without JavaScript.
	quantity, err := strconv.Atoi(r.PostFormValue("quantity"))
	if err != nil {
		h.cartProblem(w, r, "Enter a whole number of items.")
		return
	}

	token := h.tokenFromCookie(r)
	if token == "" {
		h.cartProblem(w, r, "Your cart has expired.")
		return
	}

	if err := h.cart.SetQuantity(r.Context(), token, r.PathValue("variantID"), quantity); err != nil {
		h.cartUpdateFailed(w, r, token, err)
		return
	}
	h.afterCartChange(w, r, token, "Cart updated.")
}

func (h *Handler) cartDelete(w http.ResponseWriter, r *http.Request) {
	token := h.tokenFromCookie(r)
	if token == "" {
		h.cartProblem(w, r, "Your cart has expired.")
		return
	}
	if err := h.cart.Remove(r.Context(), token, r.PathValue("variantID")); err != nil {
		h.serverError(w, r, err)
		return
	}
	h.afterCartChange(w, r, token, "Removed from your cart.")
}

// afterCartChange answers a mutation: the updated cart body for htmx, a redirect
// for a plain form post so a refresh does not resubmit it.
func (h *Handler) afterCartChange(w http.ResponseWriter, r *http.Request, token, notice string) {
	c := h.cartFor(r, token)
	if isHTMX(r) {
		h.render(w, r, http.StatusOK, "cart_items", cartPageData{
			page: h.newPage(r, "Your cart"), Cart: c, Notice: notice,
		})
		return
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

func (h *Handler) cartAddFailed(w http.ResponseWriter, r *http.Request, err error) {
	var out *cart.OutOfStockError
	switch {
	case errors.As(err, &out):
		if out.Available == 0 {
			h.cartProblem(w, r, "That option is sold out.")
			return
		}
		h.cartProblem(w, r, "Only "+strconv.Itoa(out.Available)+" of that option left.")
	case errors.Is(err, cart.ErrUnavailable):
		h.cartProblem(w, r, "That option is not for sale.")
	case errors.Is(err, cart.ErrQuantity):
		h.cartProblem(w, r, "Choose between 1 and "+strconv.Itoa(cart.MaxQuantity)+" items.")
	default:
		h.serverError(w, r, err)
	}
}

func (h *Handler) cartUpdateFailed(w http.ResponseWriter, r *http.Request, token string, err error) {
	var out *cart.OutOfStockError
	switch {
	case errors.As(err, &out):
		h.renderCart(w, r, http.StatusConflict, h.cartFor(r, token), "",
			"Only "+strconv.Itoa(out.Available)+" of that option left.")
	case errors.Is(err, cart.ErrUnavailable):
		h.renderCart(w, r, http.StatusConflict, h.cartFor(r, token), "", "That option is not for sale.")
	case errors.Is(err, cart.ErrQuantity):
		h.renderCart(w, r, http.StatusUnprocessableEntity, h.cartFor(r, token), "",
			"Choose between 0 and "+strconv.Itoa(cart.MaxQuantity)+" items.")
	default:
		h.serverError(w, r, err)
	}
}

// cartProblem reports a refused change without pretending it succeeded: 409, and
// the same status block or page the shopper was looking at.
func (h *Handler) cartProblem(w http.ResponseWriter, r *http.Request, message string) {
	c := h.currentCart(r)
	if isHTMX(r) {
		h.render(w, r, http.StatusConflict, "cart_status", cartPageData{
			page: h.newPage(r, "Cart"), Cart: c, Error: message,
		})
		return
	}
	h.renderCart(w, r, http.StatusConflict, c, "", message)
}

func (h *Handler) renderCart(w http.ResponseWriter, r *http.Request, status int, c cart.Cart, notice, problem string) {
	data := cartPageData{page: h.newPage(r, "Your cart"), Cart: c, Notice: notice, Error: problem}
	h.render(w, r, status, fragmentOr(r, "cart_items", "cart"), data)
}

// currentCart reads the cart named by the cookie, treating a missing or stale
// token as an empty cart rather than an error.
func (h *Handler) currentCart(r *http.Request) cart.Cart {
	return h.cartFor(r, h.tokenFromCookie(r))
}

func (h *Handler) cartFor(r *http.Request, token string) cart.Cart {
	if token == "" {
		return cart.Cart{}
	}
	c, err := h.cart.Get(r.Context(), token)
	if err != nil {
		if !errors.Is(err, cart.ErrNotFound) {
			h.log.Error("read cart", "error", err)
		}
		return cart.Cart{}
	}
	return c
}

func (h *Handler) tokenFromCookie(r *http.Request) string {
	c, err := r.Cookie(CartCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// cartToken returns the cookie's token, creating a cart if there is none.
func (h *Handler) cartToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if token := h.tokenFromCookie(r); token != "" {
		return token, nil
	}
	return h.newCart(w, r)
}

func (h *Handler) newCart(w http.ResponseWriter, r *http.Request) (string, error) {
	token, err := h.cart.Create(r.Context())
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:  CartCookieName,
		Value: token,
		// Scoped to /cart: the catalog pages stay cookie-free, which is what
		// keeps them cacheable and embeddable.
		Path:     "/cart",
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(cartCookieTTL),
		MaxAge:   int(cartCookieTTL.Seconds()),
	})
	return token, nil
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func orDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

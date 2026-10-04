package handler

import (
	"net/http"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Your profile settings: one page for everything about your own account. Every
// role has a password to change here; the API tokens section is there only for
// the roles holding auth.PermAPITokens, and its routes refuse everybody else
// whatever the page renders.

// accountNotices are the notices the account page shows, by code. See
// userNotices for why a notice is looked up rather than echoed.
var accountNotices = map[string]string{
	"token_revoked": "Token revoked. Anything still using it is refused from its next request.",
}

type accountPage struct {
	page
	Notice string

	// Forced is set when this page was reached by being bounced to it, so it can
	// say why rather than looking like a page the browser wandered onto.
	Forced         bool
	PasswordErrors validate.FormErrors

	// The API tokens section. ShowTokens is false for a role that cannot hold
	// one, and while a password change is forced — the token routes bounce such
	// an account here anyway, so offering the form would be a loop.
	ShowTokens  bool
	Tokens      []auth.APIToken
	Lifetimes   []int
	TokenForm   tokenForm
	TokenErrors validate.FormErrors
	// Created is the token just made, shown this once. Empty on every other
	// render — the store has only its hash.
	Created     string
	CreatedName string
	// Endpoint is the MCP URL, for the connection instructions beside the token.
	Endpoint string
}

// adminAccount is your profile settings page, and the one admin page every role
// can reach — including an account that has been bounced here and can reach
// nothing else.
func (h *Handler) adminAccount(w http.ResponseWriter, r *http.Request) {
	h.renderAccount(w, r, http.StatusOK, accountPage{Notice: noticeFor(r, accountNotices)})
}

// renderAccount fills in what every render of the account page needs around the
// parts the caller set: the page, the forced flag, and the token list.
func (h *Handler) renderAccount(w http.ResponseWriter, r *http.Request, status int, v accountPage) {
	user, ok := middleware.AdminUser(r)
	if !ok {
		h.serverError(w, r, errNoAdminUser)
		return
	}
	v.page = h.newPage(r, "Profile settings")
	v.Forced = user.MustChangePassword
	v.ShowTokens = user.Can(auth.PermAPITokens) && !user.MustChangePassword
	if v.ShowTokens {
		tokens, err := h.users.APITokens(r.Context(), user.ID)
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		v.Tokens = tokens
		v.Lifetimes = tokenLifetimes
		v.Endpoint = h.cfg.BaseURL + "/mcp"
		if v.TokenForm.Days == 0 {
			v.TokenForm.Days = defaultTokenDays
		}
	}
	if v.Created != "" {
		// A page holding a live credential must not be kept by the browser's cache
		// or anything between it and here.
		w.Header().Set("Cache-Control", "no-store")
	}
	h.render(w, r, status, "admin_account", v)
}

// accountMenu is the storefront header's account menu: a fragment the public
// layout loads with htmx, so the storefront's own pages stay the same for
// everybody — cacheable, and never carrying the admin session, whose cookie is
// scoped to /admin and so reaches only this request.
//
// Nobody signed in is a 204, which htmx is configured not to swap: the
// placeholder stays empty, and a shopper sees nothing.
func (h *Handler) accountMenu(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok := middleware.AdminUser(r); !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.render(w, r, http.StatusOK, "account_menu", h.newPage(r, ""))
}

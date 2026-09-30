package handler

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Your own API tokens — the bearer credentials an MCP client, or any program,
// uses to act as you. Every role may make them, because a token can do exactly
// what its account's role can and nothing more; a viewer's token is a way to let
// an assistant read the store, which is worth having on its own.
//
// Only ever your own. Another administrator's tokens are not listed or revocable
// from here: disabling that account, changing its role or resetting its password
// revokes all of them at once, and that is the tool for the case where somebody
// else's access has to stop.

// tokenLifetimes are the expiries on offer, in days. A short fixed list rather
// than a free field: nobody needs 47 days, and a token that never expires is not
// offered at all.
var tokenLifetimes = []int{30, 90, 365}

const defaultTokenDays = 90

type tokensPage struct {
	page
	Tokens    []auth.APIToken
	Lifetimes []int
	Form      tokenForm
	Errors    validate.FormErrors
	// Created is the token just made, shown this once. Empty on every other
	// render — the store has only its hash.
	Created     string
	CreatedName string
	Notice      string
	// Endpoint is the MCP URL, for the connection instructions beside the token.
	Endpoint string
}

type tokenForm struct {
	Name string
	Days int
}

func (h *Handler) adminTokenList(w http.ResponseWriter, r *http.Request) {
	notice := ""
	if r.URL.Query().Get("revoked") == "1" {
		notice = "Token revoked. Anything still using it is refused from its next request."
	}
	h.renderTokens(w, r, http.StatusOK, tokenForm{Days: defaultTokenDays}, nil, "", "", notice)
}

func (h *Handler) adminTokenCreate(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.AdminUser(r)
	if !ok {
		h.serverError(w, r, errNoAdminUser)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return
	}
	form := tokenForm{Name: strings.TrimSpace(r.PostFormValue("name"))}
	days, err := strconv.Atoi(r.PostFormValue("days"))
	if err != nil || !slices.Contains(tokenLifetimes, days) {
		h.badForm(w, r)
		return
	}
	form.Days = days

	token, rec, err := h.users.IssueAPIToken(r.Context(), user.ID, form.Name, time.Duration(days)*24*time.Hour)
	if errors.Is(err, auth.ErrInvalidTokenName) {
		errs := validate.FormErrors{}
		errs.Add("name", "Give the token a name of up to 100 characters — say where it will be used.")
		h.renderTokens(w, r, http.StatusUnprocessableEntity, form, errs, "", "", "")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.logger(r).Info("issued an api token", "user", user.ID, "token", rec.ID, "expires", rec.ExpiresAt)
	// Rendered rather than redirected: the token exists only in this response,
	// and a redirect would have to carry it in a URL to show it at all.
	h.renderTokens(w, r, http.StatusOK, tokenForm{Days: defaultTokenDays}, nil, token, rec.Name, "")
}

func (h *Handler) adminTokenRevoke(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.AdminUser(r)
	if !ok {
		h.serverError(w, r, errNoAdminUser)
		return
	}
	err := h.users.RevokeAPIToken(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, auth.ErrNotFound) {
		h.notFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.logger(r).Info("revoked an api token", "user", user.ID, "token", r.PathValue("id"))
	http.Redirect(w, r, "/admin/account/tokens?revoked=1", http.StatusSeeOther)
}

func (h *Handler) renderTokens(w http.ResponseWriter, r *http.Request, status int, form tokenForm, errs validate.FormErrors, created, createdName, notice string) {
	user, ok := middleware.AdminUser(r)
	if !ok {
		h.serverError(w, r, errNoAdminUser)
		return
	}
	tokens, err := h.users.APITokens(r.Context(), user.ID)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if created != "" {
		// A page holding a live credential must not be kept by the browser's cache
		// or anything between it and here.
		w.Header().Set("Cache-Control", "no-store")
	}
	h.render(w, r, status, "admin_tokens", tokensPage{
		page:        h.newPage(r, "API tokens"),
		Tokens:      tokens,
		Lifetimes:   tokenLifetimes,
		Form:        form,
		Errors:      errs,
		Created:     created,
		CreatedName: createdName,
		Notice:      notice,
		Endpoint:    h.cfg.BaseURL + "/mcp",
	})
}

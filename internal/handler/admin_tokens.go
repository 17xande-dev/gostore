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
// uses to act as you. They live on your profile settings page, and only for the
// roles holding auth.PermAPITokens: owners and admins. A token is a credential
// that outlives the browser, kept in some program's config, and the accounts
// that run the store are the ones worth that exposure.
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

type tokenForm struct {
	Name string
	Days int
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
		h.renderAccount(w, r, http.StatusUnprocessableEntity, accountPage{TokenForm: form, TokenErrors: errs})
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.logger(r).Info("issued an api token", "user", user.ID, "token", rec.ID, "expires", rec.ExpiresAt)
	// Rendered rather than redirected: the token exists only in this response,
	// and a redirect would have to carry it in a URL to show it at all.
	h.renderAccount(w, r, http.StatusOK, accountPage{Created: token, CreatedName: rec.Name})
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
	http.Redirect(w, r, accountPath+"?notice=token_revoked#api-tokens", http.StatusSeeOther)
}

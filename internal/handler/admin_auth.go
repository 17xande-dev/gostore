package handler

import (
	"net/http"
	"time"

	"github.com/17xande-dev/gostore/internal/auth"
)

type loginPage struct {
	page
	Error string
}

func (h *Handler) adminLoginForm(w http.ResponseWriter, r *http.Request) {
	// Already signed in? Skip the form rather than inviting a second login.
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		if _, err := h.sessions.Verify(cookie.Value, time.Now()); err == nil {
			http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
			return
		}
	}
	h.render(w, r, http.StatusOK, "admin_login", loginPage{page: h.newPage(r, "Sign in")})
}

func (h *Handler) adminLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return
	}

	if !auth.CheckPassword(h.cfg.AdminPasswordHash, r.PostFormValue("password")) {
		// One message for every failure, and no detail about which part was
		// wrong. The bcrypt comparison above also makes each attempt cost real
		// time; a per-IP rate limit lands in the hardening phase.
		h.log.Warn("failed admin login", "remote", r.RemoteAddr)
		h.render(w, r, http.StatusUnauthorized, "admin_login", loginPage{
			page:  h.newPage(r, "Sign in"),
			Error: "That password is not right.",
		})
		return
	}

	value, err := h.sessions.Issue(time.Now())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	http.SetCookie(w, h.sessionCookie(value, h.sessions.TTL()))
	h.log.Info("admin signed in", "remote", r.RemoteAddr)
	http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
}

func (h *Handler) adminLogout(w http.ResponseWriter, r *http.Request) {
	// Nothing server-side to delete: the cookie *is* the session, so clearing
	// it is the whole operation. That is also the trade — a session cannot be
	// revoked from elsewhere before it expires.
	http.SetCookie(w, h.sessionCookie("", -time.Hour))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// sessionCookie builds the admin cookie, or its removal when value is empty.
// Path is /admin, so the cookie is never sent with a storefront request and
// cannot leak into the embeddable, deliberately cookie-free catalog fragments.
func (h *Handler) sessionCookie(value string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     auth.CookieName,
		Value:    value,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
	if ttl > 0 {
		c.Expires = time.Now().Add(ttl)
		c.MaxAge = int(ttl.Seconds())
	} else {
		c.MaxAge = -1
	}
	return c
}

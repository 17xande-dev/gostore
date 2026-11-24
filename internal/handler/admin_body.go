package handler

import (
	"math"
	"mime"
	"net/http"
	"strings"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/websec/csrf"
)

func closeUnreadBody(w http.ResponseWriter, r *http.Request) {
	// Do not drain the rejected body to reuse an HTTP/1 connection.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
}

// uploadTooLarge reports that the kit discarded an over-cap upload from a
// permitted account, so the route renders its own "too large" page.
func uploadTooLarge(r *http.Request) bool { return csrf.Oversized(r) }

const (
	productImageUpload = "POST /admin/products/{id}/image"
	productFileUpload  = "POST /admin/products/{id}/files"

	// Large files may spool to disk, but only after the account is checked.
	downloadMultipartMemory = 1 << 20
	defaultFormLimit        = 256 << 10
)

// downloadBodyLimit is the largest accepted product-file upload: the configured
// file cap plus an envelope allowance, which must not wrap an unusually large
// configured cap.
func (h *Handler) downloadBodyLimit() int64 {
	return min(h.downloadMaxBytes(), math.MaxInt64-4096) + 4096
}

// bodyLimit is csrf.Config.Limit. The first-party mux's actual match selects the
// exception; a nearby path gets no larger cap. The kit calls it before routing
// and for every unsafe request, including ones that never reach this mux.
// MultipartMaxBytes applies to multipart bodies only, so the large allowance
// reserved for streaming files to disk never admits a big urlencoded, JSON or
// text body: those keep the default cap.
func (h *Handler) bodyLimit(r *http.Request) csrf.BodyLimit {
	routes := h.firstParty.Load()
	if routes == nil {
		return csrf.BodyLimit{}
	}
	_, pattern := routes.Handler(r)
	switch pattern {
	case productFileUpload:
		return csrf.BodyLimit{
			MultipartMaxBytes: h.downloadBodyLimit(),
			MaxMemory:         downloadMultipartMemory,
			// gateUploads has already authenticated and authorised this
			// request, before anything was read.
			PassOversized: func(*http.Request) bool { return true },
		}
	case productImageUpload:
		return csrf.BodyLimit{
			MultipartMaxBytes: maxImageBytes + 1024,
			MaxMemory:         maxImageBytes,
			PassOversized: func(r *http.Request) bool {
				user, ok := h.currentSession(r)
				return ok && user.Can(auth.PermCatalogWrite) && !user.MustChangePassword
			},
		}
	}
	return csrf.BodyLimit{}
}

// bodyRefused is csrf.Config.OnBodyError: the app's own wording for the kit's
// body refusals, which it has always answered in plain text.
func (h *Handler) bodyRefused(w http.ResponseWriter, r *http.Request, status int) {
	switch status {
	case http.StatusRequestEntityTooLarge:
		// The kit has already set Connection: close.
		http.Error(w, "Request body too large.", status)
	case http.StatusUnsupportedMediaType:
		http.Error(w, "Multipart forms are not accepted on this route.", status)
	default:
		http.Error(w, "That form could not be read.", status)
	}
}

// gateUploads runs the checks that must happen before the kit reads any of an
// upload: nosurf (inside the kit) parses a form looking for its token, and an
// anonymous or unauthorised caller must not get a large body read and spooled.
// The mux's actual match selects the route; a nearby path gets no gate.
func (h *Handler) gateUploads(kit http.Handler, routes *http.ServeMux) http.Handler {
	largeUpload := h.RequireAdmin()(
		h.requirePerm(auth.PermCatalogWrite, func(w http.ResponseWriter, r *http.Request) {
			if formMediaType(r) == "multipart/form-data" {
				// Spooling to disk is for a form the CSRF layer issued: a request
				// with no token cookie cannot pass it, so do not read it.
				if _, err := r.Cookie(h.csrf.CookieName()); err != nil {
					http.Error(w, "This form has expired. Reload the page and try again.", http.StatusForbidden)
					return
				}
			}
			kit.ServeHTTP(w, r)
		}),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			kit.ServeHTTP(w, r)
			return
		}

		_, pattern := routes.Handler(r)
		switch pattern {
		case productFileUpload:
			// Oversized anonymous browser uploads get a bare refusal, not a
			// product page. This check does not look up the product or read.
			if r.ContentLength > h.downloadBodyLimit() && formMediaType(r) == "multipart/form-data" {
				if _, ok := h.currentSession(r); !ok {
					closeUnreadBody(w, r)
					http.Error(w, "Request body too large.", http.StatusRequestEntityTooLarge)
					return
				}
			}
			// An auth rejection must not drain a streamed upload on HTTP/1.
			if r.ContentLength < 0 {
				closeUnreadBody(w, r)
			}
			// Check credentials before reading, whether or not the sender
			// declared multipart. The registered route still rechecks auth
			// after the upload, which can take long enough to lose permission.
			largeUpload.ServeHTTP(w, r)
		case productImageUpload:
			// A streamed upload's size cannot be known without reading it.
			if formMediaType(r) == "multipart/form-data" && r.ContentLength < 0 {
				user, ok := h.currentSession(r)
				if !ok || !user.Can(auth.PermCatalogWrite) || user.MustChangePassword {
					closeUnreadBody(w, r)
					http.Error(w, "Sign in required.", http.StatusUnauthorized)
					return
				}
			}
			kit.ServeHTTP(w, r)
		default:
			kit.ServeHTTP(w, r)
		}
	})
}

// formMediaType is the request's media type, lower-cased. It only chooses which
// pre-read checks gateUploads runs; the body caps are decided by the kit from
// its own parse of the header (BodyLimit.MultipartMaxBytes), so a disagreement
// here can reorder an auth check but never admit a larger body.
func formMediaType(r *http.Request) string {
	rawType := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(rawType)
	if err != nil {
		// net/http can still recognise the leading type when parameters fail.
		return strings.ToLower(strings.TrimSpace(strings.SplitN(rawType, ";", 2)[0]))
	}
	return mediaType
}

package handler

import (
	"context"
	"errors"
	"math"
	"mime"
	"net/http"
	"strings"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/justinas/nosurf"
)

type uploadTooLargeKey struct{}

func uploadTooLarge(r *http.Request) bool {
	tooLarge, _ := r.Context().Value(uploadTooLargeKey{}).(bool)
	return tooLarge
}

func closeUnreadBody(w http.ResponseWriter, r *http.Request) {
	// Do not drain the rejected body to reuse an HTTP/1 connection.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
}

const (
	productImageUpload = "POST /admin/products/{id}/image"
	productFileUpload  = "POST /admin/products/{id}/files"

	// Large files may spool to disk, but only after the account is checked.
	downloadMultipartMemory = 1 << 20
	defaultFormLimit        = 256 << 10
)

// limitBodies runs before nosurf, which reads forms looking for the CSRF token.
// The mux's actual match selects the exception; a nearby path gets no larger cap.
func (h *Handler) limitBodies(next http.Handler, routes *http.ServeMux) http.Handler {
	// The envelope allowance must not wrap an unusually large configured cap.
	downloadBodyLimit := min(h.downloadMaxBytes(), math.MaxInt64-4096) + 4096
	largeUpload := middleware.RequireAdmin(h.users, h.log)(
		h.requirePerm(auth.PermCatalogWrite, func(w http.ResponseWriter, r *http.Request) {
			mediaType := formMediaType(r)
			if mediaType != "multipart/form-data" {
				// A non-file form is buffered, never spooled. Do not give it
				// the allowance reserved for streaming files to disk.
				serveBoundedForm(w, r, next, mediaType, defaultFormLimit, 0, nil)
				return
			}
			if _, err := r.Cookie(nosurf.CookieName); err != nil {
				http.Error(w, "This form has expired. Reload the page and try again.", http.StatusForbidden)
				return
			}
			serveBoundedForm(w, r, next, mediaType, downloadBodyLimit, downloadMultipartMemory, func() bool { return true })
		}),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		_, pattern := routes.Handler(r)
		switch pattern {
		case productFileUpload:
			// Oversized anonymous browser uploads get a bare refusal, not a
			// product page. This check does not look up the product or read.
			if r.ContentLength > downloadBodyLimit && formMediaType(r) == "multipart/form-data" {
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
			mediaType := formMediaType(r)
			mayUpload := func() bool {
				user, ok := h.currentSession(r)
				return ok && user.Can(auth.PermCatalogWrite) && !user.MustChangePassword
			}
			// A streamed upload's size cannot be known without reading it.
			if mediaType == "multipart/form-data" && r.ContentLength < 0 && !mayUpload() {
				closeUnreadBody(w, r)
				http.Error(w, "Sign in required.", http.StatusUnauthorized)
				return
			}
			serveBoundedForm(w, r, next, mediaType, maxImageBytes+1024, maxImageBytes, mayUpload)
		default:
			serveBoundedForm(w, r, next, formMediaType(r), defaultFormLimit, 0, nil)
		}
	})
}

func formMediaType(r *http.Request) string {
	rawType := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(rawType)
	if err != nil {
		// net/http can still recognise the leading type when parameters fail.
		return strings.ToLower(strings.TrimSpace(strings.SplitN(rawType, ";", 2)[0]))
	}
	return mediaType
}

func serveBoundedForm(w http.ResponseWriter, r *http.Request, next http.Handler, mediaType string, limit, multipartMemory int64, mayRenderOversize func() bool) {
	tooLarge := func() {
		closeUnreadBody(w, r)
		if mediaType == "multipart/form-data" && mayRenderOversize != nil && mayRenderOversize() {
			rejected := r.WithContext(context.WithValue(r.Context(), uploadTooLargeKey{}, true))
			rejected.Body, rejected.ContentLength = http.NoBody, 0
			rejected.Form, rejected.PostForm, rejected.MultipartForm = nil, nil, nil
			next.ServeHTTP(w, rejected)
			return
		}
		http.Error(w, "Request body too large.", http.StatusRequestEntityTooLarge)
	}
	if r.ContentLength > limit {
		tooLarge()
		return
	}
	if mediaType == "multipart/form-data" && multipartMemory == 0 {
		http.Error(w, "Multipart forms are not accepted on this route.", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, limit)
	// Parse once here so a truncated body cannot be mistaken for a bad token.
	var err error
	switch mediaType {
	case "application/x-www-form-urlencoded":
		err = r.ParseForm()
	case "multipart/form-data":
		err = r.ParseMultipartForm(multipartMemory)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
	default:
		err = nil
	}
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			tooLarge()
		} else {
			http.Error(w, "That form could not be read.", http.StatusBadRequest)
		}
		return
	}
	next.ServeHTTP(w, r)
}

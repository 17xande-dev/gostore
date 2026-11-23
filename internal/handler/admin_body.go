package handler

import (
	"errors"
	"math"
	"mime"
	"net/http"
	"strings"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/middleware"
	"github.com/justinas/nosurf"
)

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
	largeUpload := middleware.RequireAdmin(h.users, h.log)(
		h.requirePerm(auth.PermCatalogWrite, func(w http.ResponseWriter, r *http.Request) {
			mediaType := formMediaType(r)
			if mediaType != "multipart/form-data" {
				// A non-file form is buffered, never spooled. Do not give it
				// the allowance reserved for streaming files to disk.
				serveBoundedForm(w, r, next, mediaType, defaultFormLimit, 0)
				return
			}
			if _, err := r.Cookie(nosurf.CookieName); err != nil {
				http.Error(w, "This form has expired. Reload the page and try again.", http.StatusForbidden)
				return
			}
			// The envelope allowance must not wrap an unusually large configured cap.
			limit := min(h.downloadMaxBytes(), math.MaxInt64-4096) + 4096
			serveBoundedForm(w, r, next, mediaType, limit, downloadMultipartMemory)
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
			// Check credentials before reading, whether or not the sender
			// declared multipart. The registered route still rechecks auth
			// after the upload, which can take long enough to lose permission.
			largeUpload.ServeHTTP(w, r)
		case productImageUpload:
			serveBoundedForm(w, r, next, formMediaType(r), maxImageBytes+1024, maxImageBytes)
		default:
			serveBoundedForm(w, r, next, formMediaType(r), defaultFormLimit, 0)
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

func serveBoundedForm(w http.ResponseWriter, r *http.Request, next http.Handler, mediaType string, limit, multipartMemory int64) {
	if r.ContentLength > limit {
		http.Error(w, "Request body too large.", http.StatusRequestEntityTooLarge)
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Request body too large.", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "That form could not be read.", http.StatusBadRequest)
		}
		return
	}
	next.ServeHTTP(w, r)
}

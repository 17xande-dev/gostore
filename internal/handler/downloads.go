package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/downloads"
	"github.com/17xande-dev/gostore/internal/middleware"
)

// RegisterDownloads mounts the buyer's side of a digital product.
//
// These routes are outside the CSRF group on purpose, for the same reason the
// payment callback is: they are safe GETs carrying their own credential, and
// nosurf sets a token cookie on every response it handles. A download link
// arrives from an email, often in a different browser from the one that bought
// it, and there is no session to protect.
//
// The token in the path IS the authentication. There is no account, no login and
// nothing else to check — which is why it is 32 bytes of crypto/rand, stored only
// as a hash, and why the link is the thing a buyer must keep.
func (h *Handler) RegisterDownloads(mux *http.ServeMux) {
	mux.HandleFunc("GET /downloads/{token}", h.downloadIndex)
	mux.Handle("GET /downloads/{token}/{fileID}",
		h.limits.download(http.HandlerFunc(h.downloadFile)))
}

type downloadsPage struct {
	page
	Entitlement downloads.Entitlement
	Files       []catalog.File
	Token       string
}

// downloadIndex lists the files a token grants.
func (h *Handler) downloadIndex(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	e, err := h.grants.Lookup(r.Context(), token)
	if err != nil {
		h.downloadError(w, r, err)
		return
	}

	files, err := h.grants.Files(r.Context(), e)
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	p := h.newPage(r, e.ProductTitle)
	h.render(w, r, http.StatusOK, "downloads", downloadsPage{
		page:        p,
		Entitlement: e,
		Files:       files,
		Token:       token,
	})
}

// downloadFile authorises one file, records the click, and then produces the
// bytes.
//
// The order is the whole design. Authorisation and recording happen in one
// transaction before anything is served, so a download cannot be handed over
// without being counted, and a revoked entitlement is refused at the moment of
// the click rather than whenever a cached page is next loaded.
//
// Then one of two endings, chosen by what the storage backend can do rather than
// by configuration the handler reads:
//
//   - A bucket signs a URL good for a few minutes and the browser is redirected
//     to it. The bytes never pass through this process, so a 2 GB video costs no
//     server bandwidth and gets range requests and resume for free.
//   - A directory cannot sign anything, so the file is streamed from here.
//
// Both endings are exercised by the tests, because a fake that could only do one
// would leave the other unproven.
func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	e, err := h.grants.Lookup(r.Context(), token)
	if err != nil {
		h.downloadError(w, r, err)
		return
	}

	fileID, err := strconv.ParseInt(r.PathValue("fileID"), 10, 64)
	if err != nil {
		h.downloadNotFound(w, r)
		return
	}

	access, err := h.grants.Authorize(r.Context(), e,
		fileID, middleware.ClientIP(r, h.cfg.TrustProxyIP), r.UserAgent())
	if err != nil {
		h.downloadError(w, r, err)
		return
	}
	h.deliver(w, r, e, access.File)
}

// streamDownload serves the bytes from this process, for a backend that cannot
// sign a URL.
//
// No range support, deliberately. Implementing it properly means parsing the
// header, handling multipart ranges and getting 416 right, and http.ServeContent
// already does all of that — but it needs an io.ReadSeeker, which a bucket object
// is not. Rather than have one ending support resume and the other not depending
// on which backend is configured, this stays simple and the bucket is the answer
// for a shop selling large files.
func (h *Handler) streamDownload(w http.ResponseWriter, r *http.Request, f catalog.File) {
	body, size, err := h.files.Open(r.Context(), f.ObjectKey)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", attachment(f.OriginalFilename))
	// Nothing between here and the buyer may keep a copy: the URL is a bearer
	// credential, and a shared cache holding the response would serve it to the
	// next person who guessed the link.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, body); err != nil {
		// The status and headers are long gone, so there is no error to return —
		// only a log line. A client that hung up mid-download is ordinary and not
		// worth an error level.
		h.logger(r).Info("download stream ended early", "file", f.ID, "error", err)
	}
}

// downloadError turns this package's refusals into pages a buyer can act on.
//
// A revoked entitlement says so, because its holder did buy this and a blank 404
// reads as a broken site. Everything else is a flat 404: a token that names
// nothing must not be distinguishable from one that was never valid, or the page
// becomes an oracle for guessing.
func (h *Handler) downloadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, downloads.ErrRevoked):
		h.clientError(w, r, http.StatusForbidden, "This download has been withdrawn",
			"The link is no longer active. If you believe this is a mistake, reply to your "+
				"order confirmation and we will look into it.")
	case errors.Is(err, downloads.ErrNotPaid):
		h.clientError(w, r, http.StatusForbidden, "This order is not paid",
			"The download becomes available once payment has been confirmed.")
	case errors.Is(err, downloads.ErrNotFound):
		h.downloadNotFound(w, r)
	default:
		h.serverError(w, r, err)
	}
}

func (h *Handler) downloadNotFound(w http.ResponseWriter, r *http.Request) {
	h.clientError(w, r, http.StatusNotFound, "No such download",
		"That link does not match anything we have. Check that it was copied in full — "+
			"a download link is long, and mail clients sometimes break one across lines.")
}

// attachment builds a Content-Disposition that makes a browser save the file.
//
// The same stripping as the presigned path, and it has to be: the filename came
// from an upload form, so a quote or a newline in it would end the quoted string
// or inject a second header field.
func attachment(filename string) string {
	clean := make([]rune, 0, len(filename))
	for _, r := range filename {
		if r == '"' || r == '\\' || r < 0x20 || r > 0x7e {
			continue
		}
		clean = append(clean, r)
	}
	name := string(clean)
	if name == "" {
		name = "download"
	}
	return fmt.Sprintf("attachment; filename=%q", name)
}

// checkoutDownload serves a file straight after payment, authorised by the cart
// cookie instead of by the emailed token.
//
// It exists because the token cannot be shown on that page: it is minted, put
// into the confirmation email and forgotten, and only its hash is stored. Rather
// than weaken that by keeping the plaintext, this route uses the credential the
// page already relies on — the cart cookie, which is what identifies the order
// whose name, address and total the page is showing. A buyer who has just paid
// gets their files without waiting for mail, and nothing new is granted.
//
// Two checks that make it safe: the entitlement must belong to the *latest order
// placed from this cart*, and the file must be one that entitlement grants. Both
// are done in SQL against the ids rather than trusted from the URL.
func (h *Handler) checkoutDownload(w http.ResponseWriter, r *http.Request) {
	token := h.tokenFromCookie(r)
	if token == "" {
		h.downloadNotFound(w, r)
		return
	}
	order, err := h.orders.LatestForCart(r.Context(), token)
	if err != nil {
		h.downloadNotFound(w, r)
		return
	}

	e, err := h.grants.ForOrderID(r.Context(), order.ID, r.PathValue("entitlementID"))
	if err != nil {
		h.downloadError(w, r, err)
		return
	}
	fileID, err := strconv.ParseInt(r.PathValue("fileID"), 10, 64)
	if err != nil {
		h.downloadNotFound(w, r)
		return
	}

	access, err := h.grants.Authorize(r.Context(), e,
		fileID, middleware.ClientIP(r, h.cfg.TrustProxyIP), r.UserAgent())
	if err != nil {
		h.downloadError(w, r, err)
		return
	}
	h.deliver(w, r, e, access.File)
}

// deliver produces the bytes for an already-authorised file: a redirect to a
// signed URL where the backend can sign one, and a stream from here where it
// cannot. Shared by both entry points so the two cannot drift.
func (h *Handler) deliver(w http.ResponseWriter, r *http.Request, e downloads.Entitlement, f catalog.File) {
	log := h.logger(r).With("entitlement", e.ID, "file", f.ID, "product", e.ProductID)

	link, ok, err := h.files.PresignGet(r.Context(), f.ObjectKey, f.OriginalFilename, blob.DefaultPresignTTL)
	if err != nil {
		log.Error("could not produce a download", "error", err)
		h.serverError(w, r, err)
		return
	}
	if ok {
		log.Info("download authorised", "delivery", "redirect")
		// 302 rather than 301: the signed URL expires, and a permanent redirect is
		// exactly the thing a browser is entitled to remember for ever.
		http.Redirect(w, r, link, http.StatusFound)
		return
	}
	log.Info("download authorised", "delivery", "stream")
	h.streamDownload(w, r, f)
}

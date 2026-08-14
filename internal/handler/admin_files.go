package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/downloads"
)

// Downloadable file uploads for a digital product.
//
// The ordering rule is the image path's, and the reason is the same: put the
// object first, record the row second, so no failure leaves a row pointing at
// bytes that are not there. What is different is everything about the size.
//
// # Memory is bounded, but it is not free — and disk is not bounded at all
//
// The image path reads the whole upload into memory, which is right for a
// five-megabyte photograph and catastrophic for a two-gigabyte video. Here
// ParseMultipartForm spools anything over a megabyte to a temporary file, and that
// file is streamed into storage from there. Memory therefore does not track the
// file size, and that was measured rather than assumed: a 477 MB upload grew the
// server by 75 MB and a 1.43 GB upload by 65 MB. See blob.uploadPartSize, which is
// pinned precisely so that figure stays a constant.
//
// Two costs worth stating rather than glossing:
//
//   - Disk. The file exists twice for the length of the request, once in the
//     server's temporary directory and once in storage. Worth knowing before
//     pointing a shop selling large video at a container with a small writable
//     layer.
//   - A held-open request. A slow upload occupies a connection for its whole
//     duration, and a platform with a request timeout will cut it.
//
// Avoiding both would mean the browser uploading straight to the bucket, which
// needs a CORS policy on the bucket, a widened connect-src, a JavaScript uploader
// and an orphan sweep. Deliberately not built: uploads here are rare and done by
// one operator watching them.
//
// # And no allow-list
//
// blob.Validate refuses anything that is not a known image type, because the image
// bucket is public and a bucket serving evil.html is a cross-site scripting hole
// on a hostname the shop owns. None of that applies here: the download bucket is
// private, every read is authorised, the Content-Type served comes from the stored
// column rather than the file, and the response carries
// X-Content-Type-Options: nosniff and an attachment disposition. Refusing an
// unusual format would only stop a shop selling what it sells.
func (h *Handler) adminProductFileUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := h.digitalProduct(w, r)
	if !ok {
		return
	}
	maxBytes := h.downloadMaxBytes()

	// The +4096 is headroom for the multipart envelope — boundaries, headers, the
	// other fields — so a file of exactly the limit is not refused for the wrapper
	// around it.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+4096)
	// A small in-memory limit and everything else to a temporary file. This is not
	// the streaming part — that is FormFile below — it only governs the small
	// fields.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		h.fileProblem(w, r, p.ID, "That file is too large, or the upload was interrupted. The limit is "+
			catalog.HumanBytes(maxBytes)+".")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		h.fileProblem(w, r, p.ID, "Choose a file to upload.")
		return
	}
	defer file.Close()

	// Sniff the leading bytes, then rewind. multipart.File is an io.ReadSeeker
	// whichever way the part was spooled, so this costs nothing.
	//
	// Rewinding rather than wrapping the part in a bufio.Reader: a wrapper hides
	// the io.Seeker and io.ReaderAt that the storage client looks for, and there is
	// no reason to take that away from it.
	head := make([]byte, blob.SniffLimit)
	n, err := io.ReadFull(file, head)
	switch {
	case n == 0:
		// Covers both an empty part and a read that failed before producing
		// anything; either way there is nothing to store.
		h.fileProblem(w, r, p.ID, "That file is empty.")
		return
	case err != nil && err != io.ErrUnexpectedEOF:
		// A short file is not an error — ErrUnexpectedEOF just means it is smaller
		// than the sniff window, which a text file or a small PDF often is.
		h.serverError(w, r, err)
		return
	}
	head = head[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		h.serverError(w, r, err)
		return
	}

	// ParseMultipartForm has already spooled the part to a temporary file, so its
	// length is known and can be checked before a byte is sent to storage. Passing
	// a real size also lets the backend upload in one pass rather than buffering
	// chunks to discover where the stream ends.
	if header.Size > maxBytes {
		h.fileProblem(w, r, p.ID, "That file is "+catalog.HumanBytes(header.Size)+
			", and the limit is "+catalog.HumanBytes(maxBytes)+".")
		return
	}

	contentType := http.DetectContentType(head)
	ext := blob.DownloadExtension(contentType, header.Filename)
	key, err := blob.DownloadKey(p.ID, ext)
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	if err := h.files.Put(r.Context(), key, file, header.Size, contentType); err != nil {
		if errors.Is(err, blob.ErrDownloadsNotConfigured) {
			h.fileProblem(w, r, p.ID, "Download storage is not configured, so files cannot be uploaded. "+
				"Set DOWNLOAD_ENDPOINT or DOWNLOAD_DIR.")
			return
		}
		h.serverError(w, r, err)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	if title == "" {
		// The uploaded name is a decent default and a bad requirement: an operator
		// uploading twenty files should not have to name each one before it will
		// save. They can rename afterwards.
		title = header.Filename
	}

	f, err := h.cat.AddFile(r.Context(), catalog.File{
		ProductID:        p.ID,
		Title:            title,
		ObjectKey:        key,
		OriginalFilename: header.Filename,
		ContentType:      contentType,
		SizeBytes:        header.Size,
	}, h.submittedVariantIDs(r, p))
	if err != nil {
		// The object is stored and the row is not, so the bytes are an orphan.
		// Deleting them here would race a retry that may already be uploading the
		// same key, and the cost of leaving them is disk. Logged with the key so it
		// can be found.
		h.logger(r).Error("uploaded a download but failed to record it",
			"product", p.ID, "key", key, "error", err)
		h.serverError(w, r, err)
		return
	}

	h.logger(r).Info("download file added",
		"product", p.ID, "file", f.ID, "key", key, "bytes", f.SizeBytes, "type", contentType)
	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

// adminProductFileUpdate renames a file and rewrites which variants include it.
func (h *Handler) adminProductFileUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.digitalProduct(w, r)
	if !ok {
		return
	}
	id, ok := h.fileID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.badForm(w, r)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	if title == "" {
		h.fileProblem(w, r, p.ID, "A file needs a title — it is what the buyer sees.")
		return
	}
	position, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("position")))
	if err != nil || position < 0 {
		h.fileProblem(w, r, p.ID, "Position must be a whole number, 0 or more.")
		return
	}

	if _, err := h.cat.UpdateFile(r.Context(), catalog.File{
		ID: id, ProductID: p.ID, Title: title, Position: position,
	}, h.submittedVariantIDs(r, p)); err != nil {
		h.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

// adminProductFileDelete removes a file and then its object.
//
// The row goes first, which is the reverse of the upload and right for the same
// reason: the end state has no file either way, so the failure worth designing
// against is a live row pointing at bytes that are gone.
//
// Deleting a file somebody has already bought is *allowed*, and that is a decision
// rather than an oversight. Their entitlement keeps working and simply lists one
// file fewer — the join through variant_files finds nothing, so nothing 500s — but
// they have quietly lost something they paid for, and the shop owner will not be
// the one who notices. Refusing it outright would leave no way to remove a file
// uploaded by mistake, so the admin warns on the button instead and the download
// count next to each file is there to be looked at first.
func (h *Handler) adminProductFileDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.digitalProduct(w, r)
	if !ok {
		return
	}
	id, ok := h.fileID(w, r)
	if !ok {
		return
	}

	key, err := h.cat.DeleteFile(r.Context(), p.ID, id)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	if err := h.files.Delete(r.Context(), key); err != nil {
		// Not a failure of the request: the row is gone, which is what was asked
		// for. The object is an orphan, logged with its key.
		h.logger(r).Error("removed a download row but not its object",
			"product", p.ID, "key", key, "error", err)
	}
	h.logger(r).Info("download file removed", "product", p.ID, "file", id, "key", key)
	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

type downloadStatsPage struct {
	page
	Product catalog.Product
	Stats   downloads.Stats
}

// adminProductDownloads is the shop owner's view of how a digital product is
// actually being used.
func (h *Handler) adminProductDownloads(w http.ResponseWriter, r *http.Request) {
	p, err := h.cat.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	stats, err := h.grants.Stats(r.Context(), p.ID)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin_downloads", downloadStatsPage{
		page:    h.newPage(r, "Downloads — "+p.Title),
		Product: p,
		Stats:   stats,
	})
}

// adminEntitlementRevoke withdraws one buyer's download, and adminEntitlementRestore
// puts it back.
//
// These are the only mutating routes under /admin/orders, and phase 7 deliberately
// forbade any. That rule exists so there can be no button recording money that
// never arrived; revoking a download changes no financial fact about the order —
// the status, the total and the gateway record are all untouched — so it is
// outside what the rule was protecting. The test that enforced "no POST here" is
// narrowed to the order's own state rather than deleted.
func (h *Handler) adminEntitlementRevoke(w http.ResponseWriter, r *http.Request) {
	h.setEntitlement(w, r, h.grants.Revoke, "revoked")
}

func (h *Handler) adminEntitlementRestore(w http.ResponseWriter, r *http.Request) {
	h.setEntitlement(w, r, h.grants.Restore, "restored")
}

func (h *Handler) setEntitlement(w http.ResponseWriter, r *http.Request,
	apply func(ctx context.Context, orderID, id string) error, what string) {
	orderID, id := r.PathValue("id"), r.PathValue("entitlementID")

	if err := apply(r.Context(), orderID, id); err != nil {
		if errors.Is(err, downloads.ErrNotFound) {
			h.notFound(w, r)
			return
		}
		h.serverError(w, r, err)
		return
	}
	// Logged at info because it is a deliberate act with a consequence for a
	// customer, and the question afterwards is always when it happened.
	h.logger(r).Info("entitlement "+what, "order", orderID, "entitlement", id)
	http.Redirect(w, r, "/admin/orders/"+orderID, http.StatusSeeOther)
}

// digitalProduct loads the product a file route names and refuses one that is not
// a download, answering the request itself on any failure.
//
// The kind check is server-side and not merely a hidden form field, because a
// hand-crafted POST is the whole reason to have it: attaching files to a physical
// product would create rows nothing ever reads and objects nothing ever deletes.
func (h *Handler) digitalProduct(w http.ResponseWriter, r *http.Request) (catalog.Product, bool) {
	p, err := h.cat.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.storeError(w, r, err)
		return catalog.Product{}, false
	}
	if !p.Digital() {
		h.clientError(w, r, http.StatusConflict, "That product is not a download",
			"Files can only be attached to a product whose kind is digital. Change the kind "+
				"on the product form first.")
		return catalog.Product{}, false
	}
	return p, true
}

func (h *Handler) fileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("fileID"), 10, 64)
	if err != nil {
		h.notFound(w, r)
		return 0, false
	}
	return id, true
}

// submittedVariantIDs reads the ticked variants, resolved against the product's
// own list.
//
// Resolving here rather than letting the insert refuse is the same stance the
// category checkboxes take: an unknown id would raise a foreign key violation,
// which arrives as a 500 with no field to hang a message on. The SQL checks
// ownership too, so this is defence in depth rather than the only guard.
//
// A file with no variants ticked is legal and means nobody can download it yet.
// That is a state the admin shows rather than refuses, because uploading first and
// deciding afterwards is how somebody actually works.
func (h *Handler) submittedVariantIDs(r *http.Request, p catalog.Product) []string {
	submitted := r.PostForm["variant"]
	out := make([]string, 0, len(submitted))
	for _, id := range submitted {
		for _, v := range p.Variants {
			if v.ID == id {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// fileProblem re-renders the product form with a message, at 422 rather than a
// redirect, so the operator sees what went wrong next to the form that caused it.
func (h *Handler) fileProblem(w http.ResponseWriter, r *http.Request, productID, message string) {
	p, err := h.cat.Get(r.Context(), productID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	form := h.productForm(r, p, cats, false, nil)
	form.Errors = map[string]string{"file": message}
	h.attachFiles(w, r, &form)
	h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", form)
}

// downloadMaxBytes is the configured cap, with the package default when config
// has not been loaded — which is the case in a handler test that builds a
// config.Config by hand.
func (h *Handler) downloadMaxBytes() int64 {
	if h.cfg.DownloadMaxBytes > 0 {
		return h.cfg.DownloadMaxBytes
	}
	return blob.DefaultMaxDownloadBytes
}

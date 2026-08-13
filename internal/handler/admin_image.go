package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Product image uploads.
//
// The order of operations is the only interesting thing here, and it is chosen so
// that no failure leaves a product pointing at nothing:
//
//  1. Read and *prove* the upload is an image — sniffed magic bytes, not the
//     filename and not the browser's Content-Type.
//  2. Put the new object under a fresh key.
//  3. Point the product at it.
//  4. Only then delete the object it used to own.
//
// A failure at 2 or 3 leaves the old image in place and working. A failure at 4
// leaves an orphaned object, which costs a few kilobytes and is logged. The
// opposite order — delete first — would turn any later failure into a product with
// a broken image, which is the one outcome worth designing against.
//
// A fresh key per upload is also what makes this work behind a CDN: replacing an
// image produces a new URL, so the new photograph is visible immediately, with no
// cache purge that this store has no credentials to perform.

// maxImageBytes caps the request body. The multipart reader gets a limit too, but
// this one is applied to the connection so an oversized upload is refused while it
// is still arriving rather than after it has all been buffered.
const maxImageBytes = blob.MaxUploadBytes

func (h *Handler) adminProductImageUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.cat.Get(r.Context(), id)
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	// A body bigger than the cap is cut off here, and ParseMultipartForm then
	// reports the truncation as a malformed form — which is why the message below
	// mentions the size rather than the syntax.
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+1024)
	if err := r.ParseMultipartForm(maxImageBytes); err != nil {
		h.imageProblem(w, r, p.ID, "That file is too large, or the upload was interrupted. The limit is "+
			humanBytes(maxImageBytes)+".")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("image")
	if err != nil {
		h.imageProblem(w, r, p.ID, "Choose an image file to upload.")
		return
	}
	defer file.Close()

	// Buffered whole rather than streamed, deliberately: the content type has to be
	// sniffed from the leading bytes *before* anything is written to a public
	// bucket, and the object's size has to be known to store it. Both are safe to
	// do in memory only because of the cap above.
	body, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if int64(len(body)) > maxImageBytes {
		h.imageProblem(w, r, p.ID, "That image is larger than "+humanBytes(maxImageBytes)+".")
		return
	}
	if len(body) == 0 {
		h.imageProblem(w, r, p.ID, "That file is empty.")
		return
	}

	contentType, ext, err := blob.Validate(body)
	if err != nil {
		// The filename is echoed because with several files selected it is the only
		// way to tell which one was refused.
		h.log.Warn("refused an image upload", "product", p.ID,
			"filename", header.Filename, "error", err)
		h.imageProblem(w, r, p.ID, "That file is not an image the store can serve. Accepted: "+
			strings.Join(blob.SupportedTypes(), ", ")+".")
		return
	}

	key, err := blob.ImageKey(p.ID, ext)
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	if _, err := h.blob.Put(r.Context(), key, bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		if errors.Is(err, blob.ErrNotConfigured) {
			h.imageProblem(w, r, p.ID, "Image uploads are not configured on this deployment. "+
				"Set the BLOB_* variables, or paste an image URL instead.")
			return
		}
		// A storage fault is a server fault, and the operator gets a page that says
		// so rather than a form implying they did something wrong.
		h.serverError(w, r, err)
		return
	}

	previous := p.ImageKey
	if _, err := h.cat.SetImage(r.Context(), p.ID, key); err != nil {
		// The object is stored but nothing references it. Logged as an orphan rather
		// than deleted, because a delete here could just as easily fail and the
		// operator's next attempt should not be racing this one.
		h.log.Error("uploaded an image but failed to record it", "product", p.ID, "key", key, "error", err)
		h.serverError(w, r, err)
		return
	}

	// Last, and only now that the product points at the new object.
	h.deleteObject(r, previous, p.ID)

	h.log.Info("product image uploaded", "product", p.ID, "key", key,
		"bytes", len(body), "content_type", contentType)
	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

func (h *Handler) adminProductImageDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.cat.Get(r.Context(), id)
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	// The row is cleared first here, unlike an upload: the product ends with no
	// image either way, so the failure worth avoiding is a cleared object with a row
	// still pointing at it.
	if _, err := h.cat.ClearImage(r.Context(), p.ID); err != nil {
		h.storeError(w, r, err)
		return
	}
	h.deleteObject(r, p.ImageKey, p.ID)

	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
}

// deleteObject removes an object this store owned, if there was one. It never
// fails the request: by the time it is called the database already says the object
// is not referenced, so the worst case is an orphan, and that is a logged
// housekeeping problem rather than something to show an operator mid-task.
func (h *Handler) deleteObject(r *http.Request, key, productID string) {
	if key == "" {
		return
	}
	if err := h.blob.Delete(r.Context(), key); err != nil {
		h.log.Error("failed to delete a replaced product image; it is now an orphaned object",
			"product", productID, "key", key, "error", err)
	}
}

// imageProblem re-renders the edit page with a message about the upload. It is a
// 422 rather than a redirect, because the operator needs to read something and a
// redirect would throw the message away.
func (h *Handler) imageProblem(w http.ResponseWriter, r *http.Request, productID, message string) {
	cats, ok := h.categories(w, r)
	if !ok {
		return
	}
	p, err := h.cat.Get(r.Context(), productID)
	if err != nil {
		h.storeError(w, r, err)
		return
	}
	errs := validate.FormErrors{}
	errs.Add("image", message)
	h.render(w, r, http.StatusUnprocessableEntity, "admin_product_form", h.productForm(r, p, cats, false, errs))
}

// humanBytes renders a byte count for a form message. It handles whole mebibytes
// and nothing else, because that is the only size this is ever called with.
func humanBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib && n%mib == 0 {
		return strconv.FormatInt(n/mib, 10) + " MB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

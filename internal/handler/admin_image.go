package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/17xande-dev/gostore/internal/admin"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/validate"
)

// Product image uploads from the admin's form. The rules — what counts as an
// image, and the order of storing the new one and deleting the old — are
// admin.ReplaceProductImage's, shared with the MCP endpoint's upload URL; this
// file is the form, and the messages a person reads when it refuses.

// maxImageBytes caps the image; the pre-CSRF body cap also allows for its envelope.
const maxImageBytes = blob.MaxUploadBytes

func (h *Handler) adminProductImageUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.cat.Get(r.Context(), id)
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	tooLarge := func() {
		h.imageProblem(w, r, p.ID, "That image is larger than "+humanBytes(maxImageBytes)+".")
	}
	// This branch also serves rejected requests whose bodies were discarded
	// before CSRF. Nothing below it should run for such a request.
	if uploadTooLarge(r) {
		tooLarge()
		return
	}

	// The pre-CSRF body cap owns multipart parsing and temp-file cleanup.
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
	_, err = admin.ReplaceProductImage(r.Context(), h.cat, h.blob, h.log, p, body)
	switch {
	case errors.Is(err, admin.ErrImageTooLarge):
		tooLarge()
		return
	case errors.Is(err, admin.ErrImageEmpty):
		h.imageProblem(w, r, p.ID, "That file is empty.")
		return
	case errors.Is(err, blob.ErrUnsupportedType):
		// The filename is echoed because with several files selected it is the only
		// way to tell which one was refused.
		h.log.Warn("refused an image upload", "product", p.ID,
			"filename", header.Filename, "error", err)
		h.imageProblem(w, r, p.ID, "That file is not an image the store can serve. Accepted: "+
			strings.Join(blob.SupportedTypes(), ", ")+".")
		return
	case errors.Is(err, blob.ErrNotConfigured):
		h.imageProblem(w, r, p.ID, "Image uploads are not configured on this deployment. "+
			"Set the BLOB_* variables, or paste an image URL instead.")
		return
	case err != nil:
		// A storage fault is a server fault, and the operator gets a page that says
		// so rather than a form implying they did something wrong.
		h.serverError(w, r, err)
		return
	}
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
	admin.DeleteImageObject(r.Context(), h.blob, h.log, p.ImageKey, p.ID)

	http.Redirect(w, r, "/admin/products/"+p.ID+"/edit", http.StatusSeeOther)
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
		return strconv.FormatInt(n/mib, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

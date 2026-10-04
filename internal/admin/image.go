package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
)

// Product images: the rules for accepting one, shared by the admin's upload form
// and the MCP endpoint's upload URL.

var (
	// ErrImageEmpty is an upload with no bytes in it.
	ErrImageEmpty = errors.New("admin: the image is empty")
	// ErrImageTooLarge is an upload over blob.MaxUploadBytes.
	ErrImageTooLarge = errors.New("admin: the image is too large")
)

// CheckImage proves an upload is an image the store will serve — sniffed magic
// bytes, never the filename or the sender's Content-Type — and within the size
// cap. Its errors are ErrImageEmpty, ErrImageTooLarge and blob.ErrUnsupportedType.
//
// Separate from ReplaceProductImage so a caller can refuse a bad file before
// committing to anything, as the MCP's upload does before spending its URL.
func CheckImage(body []byte) error {
	switch {
	case len(body) == 0:
		return ErrImageEmpty
	case int64(len(body)) > blob.MaxUploadBytes:
		return ErrImageTooLarge
	}
	if _, _, err := blob.Validate(body); err != nil {
		return err
	}
	return nil
}

// ReplaceProductImage stores body as the product's image and returns the
// product as stored.
//
// The order is chosen so that no failure leaves a product pointing at nothing:
//
//  1. Check the upload is an image (CheckImage).
//  2. Put the new object under a fresh key.
//  3. Point the product at it.
//  4. Only then delete the object it used to own.
//
// A failure at 2 or 3 leaves the old image in place and working. A failure at 4
// leaves an orphaned object, which costs a few kilobytes and is logged. The
// opposite order — delete first — would turn any later failure into a product
// with a broken image, which is the one outcome worth designing against.
//
// A fresh key per upload is also what makes this work behind a CDN: replacing an
// image produces a new URL, so the new photograph is visible immediately, with
// no cache purge that this store has no credentials to perform.
//
// Besides CheckImage's errors, blob.ErrNotConfigured means the deployment has
// nowhere to put images; anything else is a server fault.
func ReplaceProductImage(ctx context.Context, cat *catalog.Store, images blob.Storage, log *slog.Logger, p catalog.Product, body []byte) (catalog.Product, error) {
	if err := CheckImage(body); err != nil {
		return catalog.Product{}, err
	}
	contentType, ext, _ := blob.Validate(body)

	key, err := blob.ImageKey(p.ID, ext)
	if err != nil {
		return catalog.Product{}, err
	}
	if _, err := images.Put(ctx, key, bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		return catalog.Product{}, fmt.Errorf("admin: store image: %w", err)
	}

	stored, err := cat.SetImage(ctx, p.ID, key)
	if err != nil {
		// The object is stored but nothing references it. Logged as an orphan
		// rather than deleted, because a delete here could just as easily fail and
		// the operator's next attempt should not be racing this one.
		log.Error("uploaded an image but failed to record it", "product", p.ID, "key", key, "error", err)
		return catalog.Product{}, err
	}

	// Last, and only now that the product points at the new object.
	DeleteImageObject(ctx, images, log, p.ImageKey, p.ID)

	log.Info("product image uploaded", "product", p.ID, "key", key,
		"bytes", len(body), "content_type", contentType)
	return stored, nil
}

// DeleteImageObject removes an object this store owned, if there was one. It
// never fails its caller: by the time it is called the database already says
// the object is not referenced, so the worst case is an orphan, and that is a
// logged housekeeping problem rather than something to show anyone mid-task.
func DeleteImageObject(ctx context.Context, images blob.Storage, log *slog.Logger, key, productID string) {
	if key == "" {
		return
	}
	if err := images.Delete(ctx, key); err != nil {
		log.Error("failed to delete a replaced product image; it is now an orphaned object",
			"product", productID, "key", key, "error", err)
	}
}

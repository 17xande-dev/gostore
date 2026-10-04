package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/17xande-dev/gostore/internal/admin"
	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
)

// Product images, in two steps, so the image never passes through a tool call:
//
//  1. create_image_upload issues a single-use URL for one product, minutes long.
//  2. The client sends the file's bytes to it over plain HTTP — PUT or POST,
//     the raw file as the body, e.g. curl -T photo.jpg <url>.
//
// Bytes in a tool call would be base64 in a JSON-RPC message, which an AI client
// has to produce as text: hundreds of thousands of characters for an ordinary
// photograph. Having the store fetch an image from a URL it is given would make
// anyone holding a token able to send the server's requests wherever they
// liked. An upload URL is neither: the client brings the bytes, and the store
// fetches nothing.
//
// The URL is the credential, in its path like a buyer's download link, because
// the sender may be a shell command with nothing else to go on. It is checked
// before the body is read, spent only once the body is proved an image (so a
// wrong file does not cost the client a new URL), and it stops working with the
// API token it was issued against. What is accepted is admin.CheckImage's rule,
// the same as the admin form's.

// UploadPath is where upload URLs live, under the MCP endpoint.
const UploadPath = "/mcp/images/"

type imageUploadInput struct {
	ProductID string `json:"product_id"`
}

// ImageUpload is how to send one image.
type ImageUpload struct {
	UploadURL     string    `json:"upload_url" jsonschema:"send the image file's raw bytes here, as the whole request body"`
	Method        string    `json:"method" jsonschema:"PUT (POST is accepted too)"`
	ExpiresAt     time.Time `json:"expires_at" jsonschema:"the URL stops working after this, and after one successful upload"`
	MaxBytes      int64     `json:"max_bytes"`
	AcceptedTypes []string  `json:"accepted_types" jsonschema:"detected from the file's contents; the filename and Content-Type are ignored"`
	Example       string    `json:"example" jsonschema:"a curl command that sends a local file"`
}

func (s *Server) createImageUpload(ctx context.Context, _ auth.User, in imageUploadInput) (ImageUpload, error) {
	rec, ok := tokenFrom(ctx)
	if !ok {
		// Unreachable behind add, which puts the token there; fail closed if not.
		return ImageUpload{}, errors.New("not authenticated")
	}
	p, err := s.d.Catalog.Get(ctx, in.ProductID)
	if err != nil {
		return ImageUpload{}, err
	}
	token, expires, err := s.d.Users.IssueImageUpload(ctx, rec.ID, p.ID)
	if err != nil {
		return ImageUpload{}, err
	}
	url := s.d.BaseURL + UploadPath + token
	return ImageUpload{
		UploadURL:     url,
		Method:        http.MethodPut,
		ExpiresAt:     expires,
		MaxBytes:      blob.MaxUploadBytes,
		AcceptedTypes: blob.SupportedTypes(),
		Example:       "curl --fail -T photo.jpg " + url,
	}, nil
}

// UploadHandler receives the bytes for an upload URL. Mount it at
// UploadPath+"{token}" for PUT and POST.
func (s *Server) UploadHandler() http.Handler {
	return http.HandlerFunc(s.upload)
}

// uploadResult is the upload's answer, in the shape of a tool result: the
// product it changed and where its image now is.
type uploadResult struct {
	ProductID string `json:"product_id,omitempty"`
	ImageURL  string `json:"image_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	// Checked before the body is read: a request with nothing behind it does
	// not get to make the server buffer five megabytes first.
	if _, ok := s.uploadGrant(w, r, s.d.Users.ImageUploadFor, token); !ok {
		return
	}

	// Buffered whole, as the admin form's upload is: the type has to be sniffed
	// before anything reaches a public bucket, and the size known to store it.
	// Safe in memory only because of the cap.
	r.Body = http.MaxBytesReader(w, r.Body, blob.MaxUploadBytes)
	body, err := io.ReadAll(r.Body)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeUpload(w, http.StatusRequestEntityTooLarge, uploadResult{Error: "the image is larger than the limit of 5 MB"})
		return
	case err != nil:
		writeUpload(w, http.StatusBadRequest, uploadResult{Error: "the upload was interrupted"})
		return
	}
	if err := admin.CheckImage(body); err != nil {
		status, msg := uploadRefusal(err)
		writeUpload(w, status, uploadResult{Error: msg})
		return
	}

	// Only now spent: the file is good, so the URL is used up whether or not
	// storing it then works — a retry asks for a new one.
	grant, ok := s.uploadGrant(w, r, s.d.Users.SpendImageUpload, token)
	if !ok {
		return
	}
	p, err := s.d.Catalog.Get(r.Context(), grant.ProductID)
	if errors.Is(err, catalog.ErrNotFound) {
		writeUpload(w, http.StatusNotFound, uploadResult{Error: "the product no longer exists"})
		return
	}
	if err != nil {
		s.uploadFailed(w, grant, err)
		return
	}
	stored, err := admin.ReplaceProductImage(r.Context(), s.d.Catalog, s.d.Images, s.d.Log, p, body)
	switch {
	case errors.Is(err, blob.ErrNotConfigured):
		writeUpload(w, http.StatusServiceUnavailable, uploadResult{Error: "image uploads are not configured on this store"})
		return
	case err != nil:
		s.uploadFailed(w, grant, err)
		return
	}
	s.d.Log.Info("mcp: image uploaded", "product", p.ID, "user", grant.UserID, "token", grant.APITokenID)
	writeUpload(w, http.StatusOK, uploadResult{ProductID: p.ID, ImageURL: s.imageURL(stored.ImageKey)})
}

// imageURL is an image's address for a client somewhere else. Images stored on
// this server's disk have a path rather than a URL — right for the store's own
// pages, useless to a client that is not one of them — so a path is made
// absolute on the store's origin. A bucket's URL is already absolute.
func (s *Server) imageURL(key string) string {
	u := s.d.Images.URL(key)
	if strings.HasPrefix(u, "/") {
		return s.d.BaseURL + u
	}
	return u
}

// uploadGrant looks the upload up with find and checks the account may still
// change the catalog. A false return means the request has been answered.
//
// Every kind of unusable URL gets the one 404, as every unusable token gets the
// one 401; a store fault is a 500, for verify's reason.
func (s *Server) uploadGrant(w http.ResponseWriter, r *http.Request, find func(context.Context, string) (auth.ImageUpload, error), token string) (auth.ImageUpload, bool) {
	grant, err := find(r.Context(), token)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeUpload(w, http.StatusNotFound, uploadResult{Error: "this upload URL is unknown, expired or already used; ask create_image_upload for a new one"})
		return grant, false
	case err != nil:
		s.d.Log.Error("mcp: cannot look up an image upload", "error", err)
		writeUpload(w, http.StatusInternalServerError, uploadResult{Error: "internal error"})
		return grant, false
	}
	// The role now, not when the URL was issued. A demotion deletes the
	// account's tokens and these with them, so this is belt and braces.
	if !grant.Role.Can(auth.PermCatalogWrite) || !grant.Role.Can(auth.PermAPITokens) {
		s.d.Log.Warn("mcp: image upload refused for role", "user", grant.UserID, "role", grant.Role)
		writeUpload(w, http.StatusForbidden, uploadResult{Error: "your role cannot change product images"})
		return grant, false
	}
	return grant, true
}

// uploadRefusal phrases admin.CheckImage's refusals for the client.
func uploadRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, admin.ErrImageEmpty):
		return http.StatusBadRequest, "the request body is empty; send the image file as the body"
	case errors.Is(err, admin.ErrImageTooLarge):
		return http.StatusRequestEntityTooLarge, "the image is larger than the limit of 5 MB"
	default:
		return http.StatusUnsupportedMediaType, "that is not an image the store can serve; accepted: JPEG, PNG, GIF, WebP"
	}
}

func (s *Server) uploadFailed(w http.ResponseWriter, grant auth.ImageUpload, err error) {
	s.d.Log.Error("mcp: image upload failed", "product", grant.ProductID, "user", grant.UserID, "error", err)
	writeUpload(w, http.StatusInternalServerError, uploadResult{Error: "internal error; the store's log has the details"})
}

func writeUpload(w http.ResponseWriter, status int, res uploadResult) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(res)
}

// tokenKey carries the calling API token into a tool's context. Only
// create_image_upload needs it — an upload URL is issued against the token, so
// that revoking the token revokes the URL — and threading it through every
// tool's signature for one tool's sake would be noise.
type tokenKey struct{}

func withToken(ctx context.Context, rec auth.APIToken) context.Context {
	return context.WithValue(ctx, tokenKey{}, rec)
}

func tokenFrom(ctx context.Context) (auth.APIToken, bool) {
	rec, ok := ctx.Value(tokenKey{}).(auth.APIToken)
	return rec, ok && rec.ID != ""
}

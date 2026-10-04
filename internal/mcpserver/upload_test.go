package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tinyPNG is a 1×1 PNG: the smallest thing the image sniffing will accept.
var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

// uploadFixture is an admin connected to the endpoint, with one product.
func uploadFixture(t *testing.T) (*fixture, *mcp.ClientSession, Product, string) {
	t.Helper()
	f := newFixture(t)
	_, token := f.account(t, "admin@example.com", auth.RoleAdmin)
	s := f.connect(t, token)
	var p Product
	if msg := call(t, s, "create_product", map[string]any{"title": "Blue Mug"}, &p); msg != "" {
		t.Fatalf("create_product: %s", msg)
	}
	return f, s, p, token
}

// newUpload asks for an upload URL and points it at the test server, which is
// not at the fixture's BaseURL.
func (f *fixture) newUpload(t *testing.T, s *mcp.ClientSession, productID string) string {
	t.Helper()
	var up ImageUpload
	if msg := call(t, s, "create_image_upload", map[string]any{"product_id": productID}, &up); msg != "" {
		t.Fatalf("create_image_upload: %s", msg)
	}
	if !strings.HasPrefix(up.UploadURL, "https://shop.test"+UploadPath+auth.ImageUploadPrefix) {
		t.Fatalf("upload_url = %q, want one at the store's base URL", up.UploadURL)
	}
	if !strings.Contains(up.Example, up.UploadURL) {
		t.Errorf("the example %q does not use the URL", up.Example)
	}
	return f.srv.URL + strings.TrimPrefix(up.UploadURL, "https://shop.test")
}

func send(t *testing.T, method, url string, body []byte) (int, uploadResult) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// A lie, on purpose: the type is sniffed from the bytes, never taken from
	// what the sender says.
	req.Header.Set("Content-Type", "image/png")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out uploadResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: response is not JSON: %v", method, url, err)
	}
	return res.StatusCode, out
}

func TestImageUpload_SetsTheImageOnceThenTheURLIsSpent(t *testing.T) {
	f, s, p, _ := uploadFixture(t)
	url := f.newUpload(t, s, p.ID)

	status, out := send(t, http.MethodPut, url, tinyPNG)
	if status != http.StatusOK || out.ProductID != p.ID || out.ImageURL == "" {
		t.Fatalf("upload = %d %+v", status, out)
	}
	var got Product
	call(t, s, "get_product", map[string]any{"id": p.ID}, &got)
	if got.ImageURL != out.ImageURL {
		t.Errorf("get_product image_url = %q, want %q", got.ImageURL, out.ImageURL)
	}
	if len(f.images.Keys()) != 1 {
		t.Errorf("stored objects = %v, want one", f.images.Keys())
	}

	// Single use: the same URL a second time is refused, and stores nothing.
	if status, _ := send(t, http.MethodPut, url, tinyPNG); status != http.StatusNotFound {
		t.Errorf("second upload to a spent URL = %d, want 404", status)
	}
	if len(f.images.Keys()) != 1 {
		t.Errorf("a spent URL stored another object: %v", f.images.Keys())
	}
}

// Disk-stored images have a path, not a URL; a client somewhere else needs the
// store's origin in front of it.
func TestImageUpload_APathImageURLIsMadeAbsolute(t *testing.T) {
	f, s, p, _ := uploadFixture(t)
	f.images.Base = "/images"
	status, out := send(t, http.MethodPut, f.newUpload(t, s, p.ID), tinyPNG)
	if status != http.StatusOK || !strings.HasPrefix(out.ImageURL, "https://shop.test/images/products/") {
		t.Fatalf("upload = %d %+v, want an absolute image_url", status, out)
	}
	var got Product
	call(t, s, "get_product", map[string]any{"id": p.ID}, &got)
	if got.ImageURL != out.ImageURL {
		t.Errorf("get_product image_url = %q, want %q", got.ImageURL, out.ImageURL)
	}
}

// A new image replaces the old, and the old object is deleted only after —
// the admin form's order, because it is the admin form's code.
func TestImageUpload_ReplacesThePreviousImage(t *testing.T) {
	f, s, p, _ := uploadFixture(t)
	_, first := send(t, http.MethodPut, f.newUpload(t, s, p.ID), tinyPNG)
	status, second := send(t, http.MethodPost, f.newUpload(t, s, p.ID), tinyPNG)
	if status != http.StatusOK || second.ImageURL == first.ImageURL {
		t.Fatalf("replacement = %d %+v (first %+v)", status, second, first)
	}
	if keys := f.images.Keys(); len(keys) != 1 {
		t.Errorf("stored objects = %v, want only the new one", keys)
	}
	if len(f.images.Deleted()) != 1 {
		t.Errorf("deleted = %v, want the first image", f.images.Deleted())
	}
}

// A wrong file is refused before the URL is spent, so the client can correct
// itself without asking for a new one.
func TestImageUpload_RefusalsDoNotSpendTheURL(t *testing.T) {
	f, s, p, _ := uploadFixture(t)
	url := f.newUpload(t, s, p.ID)

	for name, tc := range map[string]struct {
		body []byte
		want int
	}{
		"html":      {[]byte("<html><script>alert(1)</script></html>"), http.StatusUnsupportedMediaType},
		"empty":     {nil, http.StatusBadRequest},
		"too large": {append(append([]byte{}, tinyPNG...), make([]byte, blob.MaxUploadBytes)...), http.StatusRequestEntityTooLarge},
	} {
		if status, out := send(t, http.MethodPut, url, tc.body); status != tc.want || out.Error == "" {
			t.Errorf("%s: %d %+v, want %d with a reason", name, status, out, tc.want)
		}
	}
	if len(f.images.Keys()) != 0 {
		t.Fatalf("a refused upload stored something: %v", f.images.Keys())
	}
	if status, _ := send(t, http.MethodPut, url, tinyPNG); status != http.StatusOK {
		t.Errorf("the good file after the refusals = %d, want 200", status)
	}
}

// An upload URL is only as good as the API token it was issued against.
func TestImageUpload_RevokingTheTokenRevokesTheURL(t *testing.T) {
	f, s, p, token := uploadFixture(t)
	url := f.newUpload(t, s, p.ID)

	rec, user, err := f.users.APITokenUser(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.users.RevokeAPIToken(t.Context(), user.ID, rec.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := send(t, http.MethodPut, url, tinyPNG); status != http.StatusNotFound {
		t.Errorf("upload after the token was revoked = %d, want 404", status)
	}
}

func TestImageUpload_UnknownAndDeletedProductURLsAre404(t *testing.T) {
	f, s, p, _ := uploadFixture(t)
	url := f.newUpload(t, s, p.ID)
	if msg := call(t, s, "delete_product", map[string]any{"id": p.ID}, nil); msg != "" {
		t.Fatalf("delete_product: %s", msg)
	}
	if status, _ := send(t, http.MethodPut, url, tinyPNG); status != http.StatusNotFound {
		t.Errorf("upload for a deleted product = %d, want 404", status)
	}

	for _, bad := range []string{auth.ImageUploadPrefix + "nope", "gst_looks-like-an-api-token", auth.ImageUploadPrefix} {
		if status, _ := send(t, http.MethodPut, f.srv.URL+UploadPath+bad, tinyPNG); status != http.StatusNotFound {
			t.Errorf("upload to %q = %d, want 404", bad, status)
		}
	}

	if msg := call(t, s, "create_image_upload", map[string]any{"product_id": "00000000-0000-0000-0000-000000000000"}, nil); msg != "not found" {
		t.Errorf("create_image_upload for no product: %q, want not found", msg)
	}
}

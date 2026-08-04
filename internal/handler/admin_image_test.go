package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/catalog"
	"github.com/17xande-dev/gostore/internal/config"
)

// A real JPEG header, because the handler sniffs magic bytes and would refuse
// anything else — which is the behaviour under test.
var testJPEG = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01"), bytes.Repeat([]byte{0x42}, 200)...)

// uploadShop is a signed-in admin with one product and object storage configured.
func uploadShop(t *testing.T) (*shop, catalog.Product) {
	t.Helper()

	s := newStore(t, func(c *config.Config) {
		// UploadsEnabled reads this, so the form only offers an upload when storage
		// exists — the fake stands in for the storage itself.
		c.Blob.Endpoint = "localhost:9000"
	})
	signIn(t, s.srv)

	p, err := s.catalog.Create(t.Context(), catalog.Product{
		Kind: "book", Slug: "a-book", Title: "A Book", Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, p
}

// uploadImage posts a multipart form the way a browser does, with a CSRF token.
func uploadImage(t *testing.T, s *shop, productID, filename string, body []byte, contentType string) (*http.Response, string) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("csrf_token", csrfToken(t, s.srv)); err != nil {
		t.Fatalf("write field: %v", err)
	}
	// The part's own Content-Type is set to whatever the caller claims, so the tests
	// can lie about it and prove the handler does not care.
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="image"; filename="` + filename + `"`}
	h["Content-Type"] = []string{contentType}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		s.srv.URL+"/admin/products/"+productID+"/image", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", s.srv.URL)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return do(t, s.srv, req)
}

func TestProductImage_Upload(t *testing.T) {
	s, p := uploadShop(t)

	res, body := uploadImage(t, s, p.ID, "cover.jpg", testJPEG, "image/jpeg")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d, want 303: %s", res.StatusCode, body)
	}
	if got, want := res.Header.Get("Location"), "/admin/products/"+p.ID+"/edit"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	keys := s.images.Keys()
	if len(keys) != 1 {
		t.Fatalf("%d objects stored, want 1", len(keys))
	}
	obj, _ := s.images.Get(keys[0])
	if !bytes.Equal(obj.Body, testJPEG) {
		t.Error("the stored bytes are not the bytes uploaded")
	}
	if obj.ContentType != "image/jpeg" {
		t.Errorf("stored content type = %q", obj.ContentType)
	}

	// The product now owns the object: both the URL to serve it from and the key
	// that says this store may delete it.
	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ImageKey != keys[0] {
		t.Errorf("ImageKey = %q, want %q", stored.ImageKey, keys[0])
	}
	if stored.ImageURL != s.images.URL(keys[0]) {
		t.Errorf("ImageURL = %q, want the public URL %q", stored.ImageURL, s.images.URL(keys[0]))
	}
	if !stored.HasImage() {
		t.Error("HasImage() is false after an upload")
	}
	if stored.HasForeignImage() {
		t.Error("an uploaded image is reported as foreign")
	}

	// And the edit page shows it, with a remove button now that there is an object
	// to remove.
	_, page := get(t, s.srv, "/admin/products/"+p.ID+"/edit")
	if !strings.Contains(page, `src="`+stored.ImageURL+`"`) {
		t.Errorf("the edit page does not show the image: %s", page)
	}
	if !strings.Contains(page, "/image/delete") {
		t.Error("the edit page offers no way to remove the image")
	}
	// There is no image URL field at all any more.
	if strings.Contains(page, `name="image_url"`) {
		t.Error("the product form offers an image URL field")
	}
}

func TestProductImage_ReplacingDeletesTheOldObject(t *testing.T) {
	s, p := uploadShop(t)

	if res, body := uploadImage(t, s, p.ID, "first.jpg", testJPEG, "image/jpeg"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("first upload = %d %s", res.StatusCode, body)
	}
	first, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	second := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 100)...)
	if res, body := uploadImage(t, s, p.ID, "second.png", second, "image/png"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("second upload = %d %s", res.StatusCode, body)
	}

	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ImageKey == first.ImageKey {
		t.Fatal("replacing the image reused the key, so a CDN would keep serving the old one")
	}
	if !strings.HasSuffix(stored.ImageKey, ".png") {
		t.Errorf("the new key %q does not carry the new type's extension", stored.ImageKey)
	}

	// The old object is gone, and gone *by name* — the delete happened rather than
	// the object merely being absent.
	if deleted := s.images.Deleted(); len(deleted) != 1 || deleted[0] != first.ImageKey {
		t.Errorf("Deleted() = %v, want [%s]", deleted, first.ImageKey)
	}
	if keys := s.images.Keys(); len(keys) != 1 || keys[0] != stored.ImageKey {
		t.Errorf("stored keys = %v, want just the new one", keys)
	}
}

func TestProductImage_RefusesAnythingThatIsNotAnImage(t *testing.T) {
	s, p := uploadShop(t)

	// The bucket is publicly readable, so HTML on it is a cross-site scripting hole
	// on a hostname the shop owns. The filename and the claimed Content-Type both
	// say image/jpeg, and neither is consulted.
	cases := map[string][]byte{
		"html": []byte("<!doctype html><script>alert(1)</script>"),
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"text": []byte("this is definitely a picture, trust me"),
		"pdf":  []byte("%PDF-1.7\n%\xc7\xec\x8f\xa2\n"),
	}
	for name, body := range cases {
		res, page := uploadImage(t, s, p.ID, "cover.jpg", body, "image/jpeg")
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", name, res.StatusCode)
			continue
		}
		if !strings.Contains(page, "not an image the store can serve") {
			t.Errorf("%s: the form does not explain the refusal", name)
		}
	}

	if keys := s.images.Keys(); len(keys) != 0 {
		t.Errorf("a refused upload reached storage: %v", keys)
	}
	if stored, err := s.catalog.Get(t.Context(), p.ID); err != nil || stored.ImageURL != "" {
		t.Errorf("a refused upload changed the product: %+v, %v", stored, err)
	}
}

func TestProductImage_RefusesEmptyAndOversized(t *testing.T) {
	s, p := uploadShop(t)

	res, page := uploadImage(t, s, p.ID, "empty.jpg", nil, "image/jpeg")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an empty file = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(page, "empty") && !strings.Contains(page, "not an image") {
		t.Errorf("the form does not explain the refusal: %s", page)
	}

	// One byte past the cap. The body limit is on the connection, so this is refused
	// while it is still arriving rather than after it has all been buffered.
	huge := append(append([]byte{}, testJPEG...), bytes.Repeat([]byte{0}, int(blob.MaxUploadBytes))...)
	res, _ = uploadImage(t, s, p.ID, "huge.jpg", huge, "image/jpeg")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an oversized file = %d, want 422", res.StatusCode)
	}
	if keys := s.images.Keys(); len(keys) != 0 {
		t.Errorf("an oversized upload reached storage: %v", keys)
	}
}

func TestProductImage_Remove(t *testing.T) {
	s, p := uploadShop(t)
	if res, body := uploadImage(t, s, p.ID, "cover.jpg", testJPEG, "image/jpeg"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d %s", res.StatusCode, body)
	}
	uploaded, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	res, body := post(t, s.srv, "/admin/products/"+p.ID+"/image/delete", url.Values{})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d %s", res.StatusCode, body)
	}

	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ImageURL != "" || stored.ImageKey != "" {
		t.Errorf("the product still references an image: %+v", stored)
	}
	if deleted := s.images.Deleted(); len(deleted) != 1 || deleted[0] != uploaded.ImageKey {
		t.Errorf("Deleted() = %v, want [%s]", deleted, uploaded.ImageKey)
	}
	if keys := s.images.Keys(); len(keys) != 0 {
		t.Errorf("the object survived: %v", keys)
	}
}

func TestProductImage_CannotBeSetByURL(t *testing.T) {
	// Pasting a URL used to be allowed and is not any more: bytes on somebody else's
	// server can change or vanish, and a product page with a broken picture is worse
	// than one with none. The form offers no field, and hand-crafting the parameter
	// must not work either.
	s, p := uploadShop(t)

	_, page := get(t, s.srv, "/admin/products/"+p.ID+"/edit")
	if strings.Contains(page, `name="image_url"`) {
		t.Error("the product form still offers an image URL field")
	}

	form := url.Values{
		"title":     {p.Title},
		"slug":      {p.Slug},
		"kind":      {p.Kind},
		"image_url": {"https://someone-elses-site.example/cover.jpg"},
		"active":    {"1"},
	}
	if res, body := post(t, s.srv, "/admin/products/"+p.ID, form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d %s", res.StatusCode, body)
	}

	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ImageURL != "" {
		t.Errorf("ImageURL = %q; a hand-crafted image_url parameter was accepted", stored.ImageURL)
	}
	if stored.HasImage() {
		t.Error("the product has an image it was never given")
	}
}

func TestProductImage_ForeignImageIsFlaggedAndRemovable(t *testing.T) {
	// A row from before pasting stopped being allowed: a URL with no object behind
	// it. The store cannot delete those bytes and the CSP will not load them, so the
	// admin says so and offers to clear it — rather than a migration having silently
	// blanked somebody's catalog.
	s, p := uploadShop(t)

	if _, err := s.catalog.SetImage(t.Context(), p.ID, "https://someone-elses-site.example/cover.jpg", ""); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stored.HasForeignImage() {
		t.Fatal("a URL with no object is not reported as foreign")
	}

	// Matched on a phrase that does not straddle the template's line wrapping — the
	// rendered HTML keeps the source's newlines, so "no longer supported" is split
	// across two lines and would not be found.
	_, page := get(t, s.srv, "/admin/products/"+p.ID+"/edit")
	if !strings.Contains(page, "a URL pointing outside the store") {
		t.Errorf("the edit page does not flag the foreign image: %s", page)
	}

	// Clearing it works and asks storage to delete nothing, since there is no object.
	if res, body := post(t, s.srv, "/admin/products/"+p.ID+"/image/delete", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove = %d %s", res.StatusCode, body)
	}
	cleared, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cleared.HasImage() {
		t.Errorf("the foreign image was not cleared: %q", cleared.ImageURL)
	}
	if deleted := s.images.Deleted(); len(deleted) != 0 {
		t.Errorf("storage was asked to delete something for a URL it never held: %v", deleted)
	}
}

func TestProductImage_FormSaveDoesNotClobberAnUpload(t *testing.T) {
	// UpdateProduct does not write either image column, so saving the product form
	// cannot disturb the picture whatever it submits. That replaced a
	// read-then-preserve dance in the handler.
	s, p := uploadShop(t)
	if res, body := uploadImage(t, s, p.ID, "cover.jpg", testJPEG, "image/jpeg"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d %s", res.StatusCode, body)
	}
	uploaded, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	form := url.Values{
		"title":  {"A Renamed Book"},
		"slug":   {p.Slug},
		"kind":   {p.Kind},
		"active": {"1"},
		// image_url deliberately absent, as the form has no such field.
	}
	if res, body := post(t, s.srv, "/admin/products/"+p.ID, form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d %s", res.StatusCode, body)
	}

	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != "A Renamed Book" {
		t.Errorf("the edit did not apply: Title = %q", stored.Title)
	}
	if stored.ImageURL != uploaded.ImageURL || stored.ImageKey != uploaded.ImageKey {
		t.Errorf("saving the product form lost the image: %q %q, want %q %q",
			stored.ImageURL, stored.ImageKey, uploaded.ImageURL, uploaded.ImageKey)
	}
	if keys := s.images.Keys(); len(keys) != 1 {
		t.Errorf("the object was disturbed: %v", keys)
	}
}

func TestProductImage_UploadsNeedASessionAndAToken(t *testing.T) {
	s, p := uploadShop(t)

	// No CSRF token: a multipart post is as forgeable as any other.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("image", "cover.jpg")
	part.Write(testJPEG)
	mw.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		s.srv.URL+"/admin/products/"+p.ID+"/image", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", s.srv.URL)
	if res, _ := do(t, s.srv, req); res.StatusCode != http.StatusForbidden {
		t.Errorf("upload without a CSRF token = %d, want 403", res.StatusCode)
	}
	if keys := s.images.Keys(); len(keys) != 0 {
		t.Errorf("an untokened upload reached storage: %v", keys)
	}

	// And without a session at all.
	other := newStore(t, func(c *config.Config) { c.Blob.Endpoint = "localhost:9000" })
	res, _ := uploadImage(t, other, p.ID, "cover.jpg", testJPEG, "image/jpeg")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/login" {
		t.Errorf("unauthenticated upload = %d %q, want 303 /admin/login",
			res.StatusCode, res.Header.Get("Location"))
	}
}

func TestProductImage_UnconfiguredStorageSaysSo(t *testing.T) {
	// No BLOB_ENDPOINT: the form offers a pasted URL and explains itself rather than
	// showing an upload button that could only fail.
	s := newStore(t) // Blob unconfigured
	signIn(t, s.srv)

	p, err := s.catalog.Create(t.Context(), catalog.Product{
		Kind: "book", Slug: "a-book", Title: "A Book", Active: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, page := get(t, s.srv, "/admin/products/"+p.ID+"/edit")
	if strings.Contains(page, `enctype="multipart/form-data"`) {
		t.Error("an upload form is offered with no storage configured")
	}
	if !strings.Contains(page, "No image storage is configured") {
		t.Errorf("the page does not explain why: %s", page)
	}
	// And there is no fallback to offer: with no storage, a product has no image.
	if strings.Contains(page, `name="image_url"`) {
		t.Error("the page offers an image URL field")
	}
}

func TestProductImage_UnknownProductIs404(t *testing.T) {
	s, _ := uploadShop(t)

	for _, id := range []string{"3f2504e0-4f89-41d3-9a0c-0305e82c3301", "not-a-uuid"} {
		res, _ := uploadImage(t, s, id, "cover.jpg", testJPEG, "image/jpeg")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("upload to %s = %d, want 404", id, res.StatusCode)
		}
	}
}

func TestProductImage_StorefrontShowsTheUploadedImage(t *testing.T) {
	s, p := uploadShop(t)
	if _, err := s.catalog.CreateVariant(t.Context(), catalog.Variant{
		ProductID: p.ID, SKU: "BOOK-1", PriceCents: 24900, StockQty: 3, Active: true,
	}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	if res, body := uploadImage(t, s, p.ID, "cover.jpg", testJPEG, "image/jpeg"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d %s", res.StatusCode, body)
	}
	stored, err := s.catalog.Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The storefront links straight at the bucket's public URL — the bytes never
	// come through Go, which is the whole point of a public bucket.
	_, page := get(t, s.srv, "/products/a-book")
	if !strings.Contains(page, `src="`+stored.ImageURL+`"`) {
		t.Errorf("the product page does not show the uploaded image: %s", page)
	}
	if !strings.Contains(stored.ImageURL, "https://images.example/") {
		t.Errorf("ImageURL = %q, want the public base", stored.ImageURL)
	}
}

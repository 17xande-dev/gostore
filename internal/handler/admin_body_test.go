package handler

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/blob"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/17xande-dev/websec/webtest"
)

type bodyProbe struct {
	io.Reader
	calls int
}

func (b *bodyProbe) Read(p []byte) (int, error) {
	b.calls++
	if b.Reader == nil {
		return 0, errors.New("request body must not be read")
	}
	return b.Reader.Read(p)
}

func TestLoginRejectsMultipartWithoutReading(t *testing.T) {
	checkTemp := webtest.NoTempFiles(t)
	s := newStore(t)
	body := &bodyProbe{}
	r := httptest.NewRequest(http.MethodPost, s.srv.URL+"/admin/login", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
	r.Header.Set("Origin", s.srv.URL)
	w := httptest.NewRecorder()
	s.srv.Config.Handler.ServeHTTP(w, r)
	checkTemp()
	if w.Code != http.StatusUnsupportedMediaType || body.calls != 0 {
		t.Errorf("multipart login: status %d, reads %d; want 415, 0", w.Code, body.calls)
	}
}

func TestLoginCapsDeclaredAndStreamedForms(t *testing.T) {
	const limit = 256 << 10
	s := newStore(t)
	token := csrfToken(t, s.srv)
	tokenCookie, ok := s.srv.browser.With(t).Cookie(s.handler.csrf.CookieName())
	if !ok {
		t.Fatal("login form did not set a CSRF cookie")
	}
	for _, declared := range []bool{true, false} {
		form := url.Values{
			"csrf_token": {token}, "email": {testEmail}, "password": {testPassword},
		}
		body := webtest.NewCountingReader(io.MultiReader(
			strings.NewReader(form.Encode()+"&padding="), webtest.Filler(4<<20),
		))
		r := httptest.NewRequest(http.MethodPost, s.srv.URL+"/admin/login", body)
		if declared {
			r.ContentLength = limit + 1
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", s.srv.URL)
		r.AddCookie(&http.Cookie{Name: s.handler.csrf.CookieName(), Value: tokenCookie})
		w := httptest.NewRecorder()
		s.srv.Config.Handler.ServeHTTP(w, r)
		maxRead := int64(limit + (64 << 10))
		if declared {
			maxRead = 0
		}
		if w.Code != http.StatusRequestEntityTooLarge || body.N() > maxRead {
			t.Errorf("declared=%t: status %d, read %d bytes; want 413, at most %d bytes", declared, w.Code, body.N(), maxRead)
		}
	}
	if n, err := s.users.CountSessionsForUser(t.Context(), s.owner.ID); err != nil || n != 0 {
		t.Fatalf("oversized forms created %d sessions (error %v)", n, err)
	}
	signIn(t, s.srv)
}

func TestProductUploadBodyLimits(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		s, p := uploadShop(t)
		atLimit := make([]byte, maxImageBytes)
		copy(atLimit, testJPEG)
		res, _ := uploadImage(t, s, p.ID, "cover.jpg", atLimit, "image/jpeg")
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("image at limit: status %d, want 303", res.StatusCode)
		}
		path := "/admin/products/" + p.ID + "/image"
		checkOversizedUpload(t, s, path, "image", 6<<20, true,
			http.StatusUnprocessableEntity, "That image is larger than 5 MiB.", p.Title)
		checkOversizedUpload(t, s, path, "image", 6<<20, false,
			http.StatusRequestEntityTooLarge, "Request body too large.", "")
		got, err := s.catalog.Get(t.Context(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obj, ok := s.images.Get(got.ImageKey); !ok || !bytes.Equal(obj.Body, atLimit) || len(s.images.Keys()) != 1 {
			t.Fatal("refused upload changed image storage")
		}
	})
	t.Run("download", func(t *testing.T) {
		const limit = 2 << 20 // Exercise temp-file cleanup without a 2 GiB fixture.
		checkTemp := webtest.NoTempFiles(t)
		d := newDigitalShop(t, func(c *config.Config) { c.DownloadMaxBytes = limit })
		signIn(t, d.srv)
		filesBefore := len(d.files.Keys())
		atLimit := bytes.Repeat([]byte("a"), limit)
		res, _ := uploadFile(t, d.shop, d.product.ID, "at-limit.txt", atLimit, nil)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("download at limit: status %d, want 303", res.StatusCode)
		}
		checkOversizedUpload(t, d.shop, "/admin/products/"+d.product.ID+"/files", "file", 3<<20, true,
			http.StatusUnprocessableEntity, "That file is too large, or the upload was interrupted. The limit is 2 MiB.", d.product.Title)
		if len(d.files.Keys()) != filesBefore+1 {
			t.Fatal("refused upload changed download storage")
		}
		// The higher allowance is only for multipart bodies that can spool: any
		// other media type on the upload routes keeps the default form cap.
		signedInCookies := csrfPage(t, d.srv).Request.Cookies()
		for _, route := range []string{"/files", "/image"} {
			for _, ct := range []string{"application/x-www-form-urlencoded", "application/json", "text/plain"} {
				form := &bodyProbe{}
				r := httptest.NewRequest(http.MethodPost, d.srv.URL+"/admin/products/"+d.product.ID+route, form)
				r.ContentLength = defaultFormLimit + 1
				r.Header.Set("Content-Type", ct)
				r.Header.Set("Origin", d.srv.URL)
				for _, c := range signedInCookies {
					r.AddCookie(c)
				}
				w := httptest.NewRecorder()
				d.srv.Config.Handler.ServeHTTP(w, r)
				if w.Code != http.StatusRequestEntityTooLarge || form.calls != 0 {
					t.Errorf("%s %s: status %d, reads %d; want 413, 0", route, ct, w.Code, form.calls)
				}
			}
		}
		checkTemp()
	})
	t.Run("default download cap", func(t *testing.T) {
		d := newDigitalShop(t)
		signIn(t, d.srv)
		path := "/admin/products/" + d.product.ID + "/files"
		filesBefore := len(d.files.Keys())
		// Assert the real default without allocating or reading a 2 GiB body.
		checkOversizedUpload(t, d.shop, path, "file", int64(blob.DefaultMaxDownloadBytes)+1<<20, true,
			http.StatusUnprocessableEntity, "That file is too large, or the upload was interrupted. The limit is 2 GiB.", d.product.Title)
		checkOversizedUpload(t, d.shop, path, "file", int64(blob.DefaultMaxDownloadBytes)+1<<20, false,
			http.StatusRequestEntityTooLarge, "Request body too large.", "")
		if len(d.files.Keys()) != filesBefore {
			t.Fatal("refused upload changed download storage")
		}
	})
}

func checkOversizedUpload(t *testing.T, s *shop, path, field string, size int64, signed bool, status int, message, title string) {
	t.Helper()
	var data bytes.Buffer
	m := multipart.NewWriter(&data)
	form := csrfPage(t, s.srv)
	if err := m.WriteField("csrf_token", form.Page().Token()); err != nil {
		t.Fatal(err)
	}
	part, err := m.CreateFormFile(field, "oversized.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(make([]byte, min(size, 6<<20))); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	body := &bodyProbe{Reader: &data}
	r := httptest.NewRequest(http.MethodPost, s.srv.URL+path, body)
	r.ContentLength = max(int64(data.Len()), size)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("Origin", s.srv.URL)
	if signed {
		for _, c := range form.Request.Cookies() {
			r.AddCookie(c)
		}
	}
	w := httptest.NewRecorder()
	s.srv.Config.Handler.ServeHTTP(w, r)
	if w.Code != status || !strings.Contains(w.Body.String(), message) || body.calls != 0 {
		t.Fatalf("oversized upload: status %d, reads %d; want %d with %q and zero reads", w.Code, body.calls, status, message)
	}
	if title != "" && (!strings.Contains(w.Body.String(), title) || !csrfFieldRE.MatchString(w.Body.String())) {
		t.Fatal("oversize response did not render the editable product page")
	}
}

func TestDownloadUploadGateRefusesBeforeReading(t *testing.T) {
	d := newDigitalShop(t)
	path := "/admin/products/" + d.product.ID + "/files"
	probe := func(path string, htmx bool, includeCSRF bool, cookies []*http.Cookie) (*httptest.ResponseRecorder, int) {
		t.Helper()
		body := &bodyProbe{}
		r := httptest.NewRequest(http.MethodPost, d.srv.URL+path, body)
		r.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
		r.Header.Set("Origin", d.srv.URL)
		if htmx {
			r.Header.Set("HX-Request", "true")
		}
		for _, c := range cookies {
			if c.Name != d.handler.csrf.CookieName() || includeCSRF {
				r.AddCookie(c)
			}
		}
		w := httptest.NewRecorder()
		d.srv.Config.Handler.ServeHTTP(w, r)
		return w, body.calls
	}
	_ = csrfToken(t, d.srv)
	tokenCookie, ok := d.srv.browser.With(t).Cookie(d.handler.csrf.CookieName())
	if !ok {
		t.Fatal("login form did not set a CSRF cookie")
	}
	anonymous := []*http.Cookie{{Name: d.handler.csrf.CookieName(), Value: tokenCookie}}
	if w, reads := probe(path, true, true, anonymous); w.Code != http.StatusUnauthorized || w.Header().Get("HX-Refresh") != "true" || reads != 0 {
		t.Errorf("anonymous htmx upload: status %d, headers %v, %d reads", w.Code, w.Header(), reads)
	}
	for _, route := range []string{path, "/admin/products/not-found/files"} {
		if w, reads := probe(route, false, true, anonymous); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" || reads != 0 {
			t.Errorf("anonymous upload to %s: status %d, headers %v, %d reads", route, w.Code, w.Header(), reads)
		}
	}
	const viewerEmail = "viewer@example.com"
	mustAccount(t, d.shop, viewerEmail, testPassword, auth.RoleViewer)
	signInAs(t, d.srv, viewerEmail, testPassword)
	if w, reads := probe(path, false, true, csrfPage(t, d.srv).Request.Cookies()); w.Code != http.StatusForbidden || reads != 0 {
		t.Errorf("viewer upload: status %d, %d reads", w.Code, reads)
	}
	signIn(t, d.srv)
	signedInCookies := csrfPage(t, d.srv).Request.Cookies()
	if w, reads := probe(path, false, false, signedInCookies); w.Code != http.StatusForbidden || reads != 0 {
		t.Errorf("signed-in upload without CSRF cookie: status %d, %d reads", w.Code, reads)
	}

	var payload bytes.Buffer
	m := multipart.NewWriter(&payload)
	if err := m.WriteField("csrf_token", "not-the-token"); err != nil {
		t.Fatal(err)
	}
	part, err := m.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "notes"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, d.srv.URL+path, &payload)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("Origin", d.srv.URL)
	for _, c := range signedInCookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	filesBefore := len(d.files.Keys())
	d.srv.Config.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(d.files.Keys()) != filesBefore {
		t.Fatalf("signed-in upload with a bad CSRF token: status %d; want 403 with no upload", w.Code)
	}
}

// An anonymous caller who holds a CSRF cookie (any page issues one) and streams
// a multipart body to the image route must be refused 401 before the kit reads
// any of it: the kit's form parse would otherwise buffer up to the image limit
// in memory for someone who is not signed in.
func TestAnonymousStreamedImageUploadIsRefusedWithoutReading(t *testing.T) {
	s, p := uploadShop(t) // signed in, so the cookie jar holds a CSRF cookie
	path := s.srv.URL + "/admin/products/" + p.ID + "/image"
	signedInCookies := csrfPage(t, s.srv).Request.Cookies()
	for _, ct := range []string{"multipart/form-data; boundary=x", "Multipart/Form-Data; boundary=x"} {
		body := &bodyProbe{}
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.ContentLength = -1 // streamed: the size cannot be known without reading
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Origin", s.srv.URL)
		for _, c := range signedInCookies {
			if c.Name != s.handler.session.Name() { // anonymous: keep the CSRF cookie only
				r.AddCookie(c)
			}
		}
		w := httptest.NewRecorder()
		s.srv.Config.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Sign in required.") || body.calls != 0 {
			t.Errorf("%s: status %d body %q, reads %d; want 401 \"Sign in required.\", 0 reads",
				ct, w.Code, w.Body.String(), body.calls)
		}
		// HTTP/1 (httptest's request) must not keep the connection to drain an
		// unread streamed body; the kit does the same for declared sizes.
		if got := w.Header().Get("Connection"); got != "close" {
			t.Errorf("%s: Connection = %q on the refusal, want close", ct, got)
		}
	}
}

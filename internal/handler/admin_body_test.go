package handler

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/auth"
	"github.com/17xande-dev/gostore/internal/config"
	"github.com/justinas/nosurf"
)

type bodyProbe struct {
	io.Reader
	calls, bytes int
}

func (b *bodyProbe) Read(p []byte) (int, error) {
	b.calls++
	if b.Reader == nil {
		return 0, errors.New("request body must not be read")
	}
	n, err := b.Reader.Read(p)
	b.bytes += n
	return n, err
}

func TestLoginRejectsMultipartWithoutReading(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s := newStore(t)
	body := &bodyProbe{}
	r := httptest.NewRequest(http.MethodPost, s.srv.URL+"/admin/login", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
	r.Header.Set("Origin", s.srv.URL)
	w := httptest.NewRecorder()
	s.srv.Config.Handler.ServeHTTP(w, r)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusUnsupportedMediaType || body.calls != 0 || len(entries) != 0 {
		t.Errorf("multipart login: status %d, reads %d, temp files %d; want 415, 0, 0", w.Code, body.calls, len(entries))
	}
}

func TestLoginCapsDeclaredAndStreamedForms(t *testing.T) {
	const limit = 256 << 10
	s := newStore(t)
	token := csrfToken(t, s.srv)
	for _, declared := range []bool{true, false} {
		body := &bodyProbe{Reader: strings.NewReader(url.Values{
			"csrf_token": {token}, "email": {testEmail}, "password": {testPassword},
		}.Encode() + "&padding=" + strings.Repeat("x", 4<<20))}
		r := httptest.NewRequest(http.MethodPost, s.srv.URL+"/admin/login", body)
		if declared {
			r.ContentLength = limit + 1
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", s.srv.URL)
		for _, c := range s.srv.Client().Jar.Cookies(r.URL) {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		s.srv.Config.Handler.ServeHTTP(w, r)
		maxRead := limit + (64 << 10)
		if declared {
			maxRead = 0
		}
		if w.Code != http.StatusRequestEntityTooLarge || body.bytes > maxRead {
			t.Errorf("declared=%t: status %d, read %d bytes; want 413, at most %d bytes", declared, w.Code, body.bytes, maxRead)
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
		res, _ = uploadImage(t, s, p.ID, "large.jpg", append(atLimit, make([]byte, 1<<20)...), "image/jpeg")
		if res.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("image over limit: status %d, want 413", res.StatusCode)
		}
		if len(s.images.Keys()) != 1 {
			t.Fatal("refused upload changed image storage")
		}
	})
	t.Run("download", func(t *testing.T) {
		const limit = 2 << 20 // Exercise temp-file cleanup without a 2 GiB fixture.
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		d := newDigitalShop(t, func(c *config.Config) { c.DownloadMaxBytes = limit })
		signIn(t, d.srv)
		filesBefore := len(d.files.Keys())
		atLimit := bytes.Repeat([]byte("a"), limit)
		res, _ := uploadFile(t, d.shop, d.product.ID, "at-limit.txt", atLimit, nil)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("download at limit: status %d, want 303", res.StatusCode)
		}
		res, _ = uploadFile(t, d.shop, d.product.ID, "large.txt", append(atLimit, make([]byte, 1<<20)...), nil)
		if res.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("download over limit: status %d, want 413", res.StatusCode)
		}
		if len(d.files.Keys()) != filesBefore+1 {
			t.Fatal("refused upload changed download storage")
		}
		// The higher allowance is only for multipart bodies that can spool.
		form := &bodyProbe{}
		r := httptest.NewRequest(http.MethodPost, d.srv.URL+"/admin/products/"+d.product.ID+"/files", form)
		r.ContentLength = defaultFormLimit + 1
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range d.srv.Client().Jar.Cookies(r.URL) {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		d.srv.Config.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge || form.calls != 0 {
			t.Errorf("download URL-encoded form: status %d, reads %d; want 413, 0", w.Code, form.calls)
		}
		if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
			t.Fatalf("upload left temp files: %v, %v", entries, err)
		}
	})
}

func TestDownloadUploadGateRefusesBeforeReading(t *testing.T) {
	d := newDigitalShop(t)
	path := "/admin/products/" + d.product.ID + "/files"
	probe := func(path string, htmx bool, includeCSRF bool) (*httptest.ResponseRecorder, int) {
		t.Helper()
		body := &bodyProbe{}
		r := httptest.NewRequest(http.MethodPost, d.srv.URL+path, body)
		r.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
		r.Header.Set("Origin", d.srv.URL)
		if htmx {
			r.Header.Set("HX-Request", "true")
		}
		for _, c := range d.srv.Client().Jar.Cookies(r.URL) {
			if c.Name != nosurf.CookieName || includeCSRF {
				r.AddCookie(c)
			}
		}
		w := httptest.NewRecorder()
		d.srv.Config.Handler.ServeHTTP(w, r)
		return w, body.calls
	}
	_ = csrfToken(t, d.srv)
	if w, reads := probe(path, true, true); w.Code != http.StatusUnauthorized || w.Header().Get("HX-Refresh") != "true" || reads != 0 {
		t.Errorf("anonymous htmx upload: status %d, headers %v, %d reads", w.Code, w.Header(), reads)
	}
	for _, route := range []string{path, "/admin/products/not-found/files"} {
		if w, reads := probe(route, false, true); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" || reads != 0 {
			t.Errorf("anonymous upload to %s: status %d, headers %v, %d reads", route, w.Code, w.Header(), reads)
		}
	}
	const viewerEmail = "viewer@example.com"
	mustAccount(t, d.shop, viewerEmail, testPassword, auth.RoleViewer)
	signInAs(t, d.srv, viewerEmail, testPassword)
	if w, reads := probe(path, false, true); w.Code != http.StatusForbidden || reads != 0 {
		t.Errorf("viewer upload: status %d, %d reads", w.Code, reads)
	}
	signIn(t, d.srv)
	if w, reads := probe(path, false, false); w.Code != http.StatusForbidden || reads != 0 {
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
	for _, c := range d.srv.Client().Jar.Cookies(r.URL) {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	filesBefore := len(d.files.Keys())
	d.srv.Config.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(d.files.Keys()) != filesBefore {
		t.Fatalf("signed-in upload with a bad CSRF token: status %d; want 403 with no upload", w.Code)
	}
}

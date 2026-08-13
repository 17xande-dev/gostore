package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestNotFound_UnknownURLGetsThePage(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	for _, path := range []string{"/nope", "/deep/nested/nonsense", "/products/no-such-product"} {
		res, body := get(t, srv, path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
		// The status is the part that must be right; the page is the part that
		// makes it useful.
		if !strings.Contains(body, "Page not found") {
			t.Errorf("GET %s did not get the 404 page:\n%s", path, excerpt(body))
		}
		if !strings.Contains(body, `action="/products"`) {
			t.Errorf("GET %s: the 404 page offers no way onward", path)
		}
		if !strings.Contains(body, "<html") {
			t.Errorf("GET %s: the 404 is not a rendered page", path)
		}
	}
}

func TestNotFound_OutOfRangePageGetsThePage(t *testing.T) {
	// The 404s the store raises itself, rather than the ones the mux raises, go
	// through the same page — otherwise a mistyped ?page= looks like a different
	// kind of failure from a mistyped URL.
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	for _, path := range []string{"/products?page=900", "/products?page=nonsense"} {
		res, body := get(t, srv, path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
		if !strings.Contains(body, "Page not found") {
			t.Errorf("GET %s did not get the 404 page", path)
		}
	}
}

func TestNotFound_ByteEndpointsStayPlain(t *testing.T) {
	// A missing asset answers with the plain 404, deliberately. Nothing is going to
	// read an HTML page out of an <img> tag or a <script> src, and sending one only
	// makes the failure bigger.
	srv, _ := newStorefront(t, testConfig(), "")

	res, body := get(t, srv, "/static/no-such-file.css")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET a missing asset = %d", res.StatusCode)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("a missing asset served an HTML page:\n%s", excerpt(body))
	}
}

func TestNotFound_IsOverridable(t *testing.T) {
	dir := t.TempDir()
	writeOverride(t, dir, "not_found.html", `{{define "not_found"}}MY OWN 404{{end}}`)

	srv, _ := newStorefront(t, testConfig(), dir)

	res, body := get(t, srv, "/nope")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("an overridden 404 answered %d", res.StatusCode)
	}
	if got := strings.TrimSpace(body); got != "MY OWN 404" {
		t.Errorf("the override did not take: %q", excerpt(got))
	}
}

func TestNotFound_BrokenTemplateStillAnswers404(t *testing.T) {
	// The status is what a crawler and a browser act on, so a theme that cannot
	// render must not turn a missing page into a 200 with an empty body. This is
	// why notFound renders directly rather than through h.render, which logs the
	// failure and leaves the status alone.
	dir := t.TempDir()
	writeOverride(t, dir, "not_found.html", `{{define "not_found"}}{{.NoSuchField}}{{end}}`)

	srv, _ := newStorefront(t, testConfig(), dir)

	res, body := get(t, srv, "/nope")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("a broken 404 template answered %d, want 404", res.StatusCode)
	}
	// Specifically Go's plain 404, which is what proves the fallback ran rather
	// than the override having quietly rendered to nothing.
	if !strings.Contains(body, "404 page not found") {
		t.Errorf("the plain fallback did not run: %q", excerpt(body))
	}
}

func TestNotFound_ReachesUnknownPathsUnderTheFirstPartyGroup(t *testing.T) {
	// /admin/ and /cart/ are subtree patterns on the outer mux, so they always
	// match and hand over — which means an unknown path beneath them never reaches
	// the outer catch-all. Without a catch-all inside that group too, /cart/nonsense
	// would be the one URL in the store still answering Go's plain 404.
	srv, _ := newServer(t)

	for _, path := range []string{"/cart/nonsense", "/admin/nonsense"} {
		res, body := get(t, srv, path)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
		if !strings.Contains(body, "Page not found") {
			t.Errorf("GET %s did not get the 404 page:\n%s", path, excerpt(body))
		}
	}
}

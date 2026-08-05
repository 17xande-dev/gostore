package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The asset set is loaded once per process, so a test that changes STATIC_DIR has to
// go through SetStaticDir, which discards what is loaded. Nothing in production calls
// it more than once — the directory is fixed for the life of the server.
func withStaticDir(t *testing.T, dir string) {
	t.Helper()
	previous := staticDir
	SetStaticDir(dir)
	t.Cleanup(func() { SetStaticDir(previous) })
}

func TestAssets_BundledImagesAreServed(t *testing.T) {
	withStaticDir(t, "")
	srv, _ := newStorefront(t, testConfig(), "")

	// The logo and the placeholder ship in the binary: a store with no configuration
	// at all still has a mark in its header and a picture on every product card.
	for name, wantType := range map[string]string{
		"logo.svg":        "image/svg+xml",
		"placeholder.svg": "image/svg+xml",
		"htmx.min.js":     "javascript",
	} {
		url := assetURL(name)
		if strings.Contains(url, "missing") || strings.Contains(url, "unavailable") {
			t.Errorf("assetURL(%q) = %q", name, url)
			continue
		}

		res, body := get(t, srv, url)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", url, res.StatusCode)
			continue
		}
		if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, wantType) {
			t.Errorf("%s: Content-Type = %q, want %q", name, ct, wantType)
		}
		if !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: Cache-Control = %q", name, res.Header.Get("Cache-Control"))
		}
		if body == "" {
			t.Errorf("%s served an empty body", name)
		}
	}
}

func TestAssets_UnlistedExtensionsAreNotServed(t *testing.T) {
	withStaticDir(t, "")
	srv, _ := newStorefront(t, testConfig(), "")

	// The provenance note lives in the same directory and is embedded with everything
	// else, but its extension is not in the content-type map, so it is not published.
	// That is what keeps a file dropped into that directory from becoming a URL.
	if res, _ := get(t, srv, "/static/README.md"); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /static/README.md = %d, want 404", res.StatusCode)
	}
	if url := assetURL("README.md"); !strings.Contains(url, "missing") {
		t.Errorf("assetURL(README.md) = %q, want a visibly missing URL", url)
	}
}

func TestAssets_StaticDirShadowsABundledFile(t *testing.T) {
	// The whole point: rebranding is dropping a logo.svg into a directory, not forking
	// the project.
	dir := t.TempDir()
	const ours = `<svg xmlns="http://www.w3.org/2000/svg"><title>OUR BRAND</title></svg>`
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), []byte(ours), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	withStaticDir(t, "")
	bundledURL := assetURL("logo.svg")

	withStaticDir(t, dir)
	srv, _ := newStorefront(t, testConfig(), "")

	overriddenURL := assetURL("logo.svg")
	if overriddenURL == bundledURL {
		t.Fatal("the override has the same URL as the bundled file, so a cache would keep serving the old logo")
	}

	res, body := get(t, srv, overriddenURL)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", overriddenURL, res.StatusCode)
	}
	if !strings.Contains(body, "OUR BRAND") {
		t.Errorf("the override was not served: %s", body)
	}

	// And the page references it, so replacing the file is the whole operation.
	_, page := get(t, srv, "/products")
	if !strings.Contains(page, overriddenURL) {
		t.Errorf("the page does not reference the overridden logo: %s", page)
	}
}

func TestAssets_StaticDirCanAddNewNames(t *testing.T) {
	// An adopter whose overridden template references hero.jpg needs somewhere to put
	// hero.jpg. Shadowing alone would not be enough.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hero.png"), []byte("\x89PNG\r\n\x1a\nnot-really"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	withStaticDir(t, dir)
	srv, _ := newStorefront(t, testConfig(), "")

	url := assetURL("hero.png")
	if strings.Contains(url, "missing") {
		t.Fatalf("assetURL(hero.png) = %q", url)
	}
	res, _ := get(t, srv, url)
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d", url, res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestAssets_StaticDirCannotPublishAnythingItLikes(t *testing.T) {
	// The extension map is the gate on the override directory too. Dropping an .html
	// or a .php in there must not make it a URL on the store's own origin.
	dir := t.TempDir()
	for _, name := range []string{"evil.html", "shell.php", "notes.txt", "secrets.env"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("<script>alert(1)</script>"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	withStaticDir(t, dir)
	srv, _ := newStorefront(t, testConfig(), "")

	for _, name := range []string{"evil.html", "shell.php", "notes.txt", "secrets.env"} {
		if url := assetURL(name); !strings.Contains(url, "missing") {
			t.Errorf("assetURL(%q) = %q, want it unserved", name, url)
		}
		res, _ := get(t, srv, "/static/"+name)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET /static/%s = %d, want 404", name, res.StatusCode)
		}
	}
}

func TestAssets_MissingStaticDirIsABootFailure(t *testing.T) {
	// Reported at startup rather than as a page full of broken links.
	withStaticDir(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if err := CheckAssets(); err == nil {
		t.Error("CheckAssets accepted a directory that does not exist")
	}
}

func TestAssets_PlaceholderShownForAProductWithNoImage(t *testing.T) {
	withStaticDir(t, "")
	srv, store := newStorefront(t, testConfig(), "")
	stock(t, store)

	// Every card carries an image, so a catalog of mixed products does not reflow
	// into a mess where some have pictures and some do not.
	placeholder := assetURL("placeholder.svg")
	_, list := get(t, srv, "/products")
	if !strings.Contains(list, placeholder) {
		t.Errorf("the listing shows no placeholder for an imageless product: %s", list)
	}
	_, page := get(t, srv, "/products/sample-tee")
	if !strings.Contains(page, placeholder) {
		t.Errorf("the detail page shows no placeholder: %s", page)
	}
}

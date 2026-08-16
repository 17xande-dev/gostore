package handler

import (
	"github.com/17xande-dev/gostore/internal/blob"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTemplates_EmbeddedDefaultsRender(t *testing.T) {
	tmpl, err := ParseTemplates("", blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	// Every page the handlers name must exist and execute, or the failure only
	// shows up as a 500 on a live request.
	pages := map[string]any{
		"index":              indexPageData{page: page{Title: "Home", StoreName: "Test Store"}},
		"admin_products":     productsPage{page: page{Title: "Products", StoreName: "Test Store"}},
		"admin_product_form": productFormPage{page: page{Title: "New product", StoreName: "Test Store"}, IsNew: true},
		"admin_login":        loginPage{page: page{Title: "Sign in", StoreName: "Test Store"}},
	}
	for name, data := range pages {
		w := httptest.NewRecorder()
		if err := tmpl.Render(w, http.StatusOK, name, data); err != nil {
			t.Errorf("render %s: %v", name, err)
			continue
		}
		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("%s: Content-Type = %q", name, got)
		}
		if !strings.Contains(w.Body.String(), "Test Store") {
			t.Errorf("%s: the store name from config is not in the page", name)
		}
	}
}

func TestParseTemplates_FontStylesheetLinkedOnlyWhenConfigured(t *testing.T) {
	tmpl, err := ParseTemplates("", blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	// The default is no web font at all, so the head must carry no third-party
	// stylesheet. A store that never configured one should have nothing in its
	// markup pointing off this origin.
	w := httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, "index", indexPageData{page: page{StoreName: "Test Store"}}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(w.Body.String(), "typekit") {
		t.Error("a font stylesheet is linked with FONT_CSS_URL unset")
	}

	// And with one configured, it is linked — the <link> form only. This is the
	// half that pairs with the CSP: widening style-src achieves nothing if no page
	// actually asks for the kit.
	w = httptest.NewRecorder()
	data := indexPageData{page: page{
		StoreName:  "Test Store",
		FontCSSURL: "https://use.typekit.net/abc1def.css",
	}}
	if err := tmpl.Render(w, http.StatusOK, "index", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<link rel="stylesheet" href="https://use.typekit.net/abc1def.css">`) {
		t.Errorf("the font stylesheet is not linked: %s", body)
	}
	// Never as a script: a font service's JS loader would need script-src widened
	// and a nonce for the inline snippet, which this project does not offer.
	if strings.Contains(body, `<script src="https://use.typekit.net`) {
		t.Error("the font kit is loaded as a script")
	}
}

func TestParseTemplates_OverrideDirWins(t *testing.T) {
	dir := t.TempDir()
	override := `{{define "admin_products"}}OVERRIDDEN{{end}}`
	if err := os.WriteFile(filepath.Join(dir, "admin_products.gohtml"), []byte(override), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tmpl, err := ParseTemplates(dir, blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	w := httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, "admin_products", productsPage{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "OVERRIDDEN" {
		t.Errorf("body = %q, want the override", got)
	}

	// Templates the override directory says nothing about must still come from
	// the embedded defaults.
	w = httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, "admin_product_form", productFormPage{IsNew: true}); err != nil {
		t.Fatalf("render default: %v", err)
	}
	if !strings.Contains(w.Body.String(), "New product") {
		t.Error("the embedded product form was lost when an override was loaded")
	}
}

// The point of THEME_RELOAD: editing a template in the override directory shows on
// the next refresh, without a restart.
func TestSetReload_PicksUpAnEditWithoutReparsing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "admin_products.gohtml")
	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}FIRST{{end}}`), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tmpl, err := ParseTemplates(dir, blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	tmpl.SetReload(true)

	if got := render(t, tmpl, "admin_products"); got != "FIRST" {
		t.Fatalf("body = %q, want FIRST", got)
	}

	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}SECOND{{end}}`), 0o600); err != nil {
		t.Fatalf("rewrite override: %v", err)
	}
	if got := render(t, tmpl, "admin_products"); got != "SECOND" {
		t.Errorf("body = %q, want SECOND: the edit needed a restart to appear", got)
	}
}

// Without it, the set is what was read at startup — which is what a deployment
// wants, and what makes a broken override a boot failure rather than a 500.
func TestParseTemplates_WithoutReloadAnEditIsNotPickedUp(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "admin_products.gohtml")
	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}FIRST{{end}}`), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tmpl, err := ParseTemplates(dir, blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}SECOND{{end}}`), 0o600); err != nil {
		t.Fatalf("rewrite override: %v", err)
	}
	if got := render(t, tmpl, "admin_products"); got != "FIRST" {
		t.Errorf("body = %q, want the set read at startup", got)
	}
}

// A theme being edited is a theme that is broken half the time. A save mid-edit is
// an error on that request — never a half-written page — and fixing the file is the
// whole recovery.
func TestSetReload_ABrokenEditIsAnErrorAndRecoversOnTheNextSave(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "admin_products.gohtml")
	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}GOOD{{end}}`), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tmpl, err := ParseTemplates(dir, blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}
	tmpl.SetReload(true)
	if got := render(t, tmpl, "admin_products"); got != "GOOD" {
		t.Fatalf("body = %q, want GOOD", got)
	}

	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}{{if}}`), 0o600); err != nil {
		t.Fatalf("write broken override: %v", err)
	}
	w := httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, "admin_products", productsPage{}); err == nil {
		t.Error("a template that does not parse rendered without an error")
	}
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing written", w.Body.String())
	}

	// Fixing the file is all it takes; nothing has to be restarted to recover.
	if err := os.WriteFile(file, []byte(`{{define "admin_products"}}FIXED{{end}}`), 0o600); err != nil {
		t.Fatalf("write fixed override: %v", err)
	}
	if got := render(t, tmpl, "admin_products"); got != "FIXED" {
		t.Errorf("body = %q, want FIXED", got)
	}
}

func render(t *testing.T, tmpl *Templates, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, name, productsPage{}); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return strings.TrimSpace(w.Body.String())
}

func TestRender_UnknownTemplateWritesNothing(t *testing.T) {
	tmpl, err := ParseTemplates("", blob.NewFake())
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	w := httptest.NewRecorder()
	if err := tmpl.Render(w, http.StatusOK, "no_such_template", nil); err == nil {
		t.Fatal("expected an error for an unknown template, got nil")
	}
	// Buffering first is the point: a failed render must not have already sent
	// a partial page with a success status.
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing written", w.Body.String())
	}
}

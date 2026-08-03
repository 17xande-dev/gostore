package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTemplates_EmbeddedDefaultsRender(t *testing.T) {
	tmpl, err := ParseTemplates("")
	if err != nil {
		t.Fatalf("ParseTemplates: %v", err)
	}

	// Every page the handlers name must exist and execute, or the failure only
	// shows up as a 500 on a live request.
	pages := map[string]any{
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

func TestParseTemplates_OverrideDirWins(t *testing.T) {
	dir := t.TempDir()
	override := `{{define "admin_products"}}OVERRIDDEN{{end}}`
	if err := os.WriteFile(filepath.Join(dir, "admin_products.html"), []byte(override), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	tmpl, err := ParseTemplates(dir)
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

func TestRender_UnknownTemplateWritesNothing(t *testing.T) {
	tmpl, err := ParseTemplates("")
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

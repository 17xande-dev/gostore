// Package handler renders the store's HTML: full pages and the fragments htmx
// swaps into them.
package handler

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/17xande-dev/gostore/internal/catalog"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Templates is the parsed template set: the embedded defaults, with same-named
// files from an override directory layered on top.
type Templates struct{ t *template.Template }

// ParseTemplates parses the embedded defaults and then, when overrideDir is
// set, re-parses same-named files from that directory over them — a later
// definition of a template name replaces an earlier one. Adopters restyle the
// store by dropping files into a directory instead of forking the project.
//
// Overrides are read at startup, so changing a file needs a restart but never a
// rebuild.
func ParseTemplates(overrideDir string) (*Templates, error) {
	t, err := template.New("gostore").Funcs(funcs()).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("handler: parse embedded templates: %w", err)
	}

	if overrideDir != "" {
		matches, err := filepath.Glob(filepath.Join(overrideDir, "*.html"))
		if err != nil {
			return nil, fmt.Errorf("handler: scan TEMPLATE_DIR: %w", err)
		}
		if len(matches) > 0 {
			if t, err = t.ParseFiles(matches...); err != nil {
				return nil, fmt.Errorf("handler: parse override templates: %w", err)
			}
		}
	}
	return &Templates{t: t}, nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		// Amounts are cents everywhere in Go; the decimal point exists only in
		// the rendered page.
		"money": catalog.FormatPrice,
		// asset resolves a vendored file to its content-addressed URL.
		"asset": assetURL,
	}
}

// Render executes one named template. It renders into a buffer first, so a
// template error becomes a 500 instead of a half-written page that already
// claimed 200.
func (t *Templates) Render(w http.ResponseWriter, status int, name string, data any) error {
	var buf bytes.Buffer
	if err := t.t.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("handler: render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

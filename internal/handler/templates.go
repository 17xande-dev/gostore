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
	"strings"
	texttemplate "text/template"

	"github.com/17xande-dev/gostore/internal/catalog"
)

//go:embed templates/*.html templates/*.txt
var templatesFS embed.FS

// Templates is the parsed template set: the embedded defaults, with same-named
// files from an override directory layered on top.
//
// There are two sets, because email needs both halves of a message and they must
// not be escaped the same way. Pages and HTML mail parts go through
// html/template, which escapes; the plain-text mail part goes through
// text/template, which does not — running a receipt through the HTML escaper
// would put `&amp;` in front of a customer.
type Templates struct {
	t    *template.Template
	text *texttemplate.Template
}

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
	text, err := texttemplate.New("gostore").Funcs(funcs()).ParseFS(templatesFS, "templates/*.txt")
	if err != nil {
		return nil, fmt.Errorf("handler: parse embedded text templates: %w", err)
	}

	if overrideDir != "" {
		if err := overlay(overrideDir, "*.html", func(files []string) (err error) {
			t, err = t.ParseFiles(files...)
			return err
		}); err != nil {
			return nil, err
		}
		if err := overlay(overrideDir, "*.txt", func(files []string) (err error) {
			text, err = text.ParseFiles(files...)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return &Templates{t: t, text: text}, nil
}

// overlay finds override files matching a pattern and hands them to parse. A
// directory with none is not an error: an adopter overriding one page should not
// have to supply an email template as well.
func overlay(dir, pattern string, parse func([]string) error) error {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return fmt.Errorf("handler: scan TEMPLATE_DIR for %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil
	}
	if err := parse(matches); err != nil {
		return fmt.Errorf("handler: parse override templates: %w", err)
	}
	return nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		// Amounts are cents everywhere in Go; the decimal point exists only in
		// the rendered page.
		"money": catalog.FormatPrice,
		// asset resolves a vendored file to its content-addressed URL.
		"asset": assetURL,
		// linebreaks renders multi-line text — an address, typed into a textarea —
		// as HTML.
		"linebreaks": linebreaks,
	}
}

// linebreaks escapes text and then turns its newlines into <br>, in that order.
// Escaping first is what makes returning template.HTML safe here: the only markup
// in the result is the tags this function put there.
func linebreaks(s string) template.HTML {
	escaped := template.HTMLEscapeString(s)
	// Textareas submit CRLF, so collapse that before looking for bare newlines or
	// every line would gain a stray carriage return.
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	return template.HTML(strings.ReplaceAll(escaped, "\n", "<br>"))
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

// String executes one named HTML template and returns it, for an email body
// rather than a response.
func (t *Templates) String(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("handler: render %s: %w", name, err)
	}
	return buf.String(), nil
}

// Text executes one named text template — the plain-text half of an email.
func (t *Templates) Text(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.text.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("handler: render text %s: %w", name, err)
	}
	return buf.String(), nil
}

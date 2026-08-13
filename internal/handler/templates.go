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
	"sync"
	texttemplate "text/template"

	"github.com/17xande-dev/gostore/internal/blob"
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
	// mu guards the two sets, which reload replaces on the fly.
	mu   sync.RWMutex
	t    *template.Template
	text *texttemplate.Template

	// What a reload needs to parse the set again. Kept here rather than passed in,
	// because a render is the only thing that triggers one and it has neither.
	overrideDir string
	images      blob.Storage
	reload      bool
}

// ParseTemplates parses the embedded defaults and then, when overrideDir is
// set, re-parses same-named files from that directory over them — a later
// definition of a template name replaces an earlier one. Adopters restyle the
// store by dropping files into a directory instead of forking the project.
//
// Overrides are read at startup, so changing a file needs a restart but never a
// rebuild — unless SetReload has been called, which is what the development
// stack does so that a refresh is enough.
//
// images resolves a product's image key to the URL it is served at. It is the
// storage backend, so a template can render an image without the row having
// recorded where the bytes happen to live today.
func ParseTemplates(overrideDir string, images blob.Storage) (*Templates, error) {
	tmpl := &Templates{overrideDir: overrideDir, images: images}
	if err := tmpl.parse(); err != nil {
		return nil, err
	}
	return tmpl, nil
}

// SetReload makes every render re-read the override directory first, so editing a
// template and refreshing the page shows the change without a restart.
//
// Development only, and not because of taste: it reparses every template on every
// request, and it moves a syntax error in an override from a boot failure to a 500
// on whichever page happens to use it. Call it before serving; the flag is read
// without synchronisation, unlike the template set it governs.
func (t *Templates) SetReload(on bool) { t.reload = on }

// parse builds both sets from the embedded defaults with the override directory
// layered on top, and installs them. It replaces the sets only on success, so a
// reload that hits a half-saved override leaves the last good ones in place and
// fails that request rather than emptying the template set.
func (t *Templates) parse() error {
	html, err := template.New("gostore").Funcs(funcs(t.images)).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("handler: parse embedded templates: %w", err)
	}
	text, err := texttemplate.New("gostore").Funcs(funcs(t.images)).ParseFS(templatesFS, "templates/*.txt")
	if err != nil {
		return fmt.Errorf("handler: parse embedded text templates: %w", err)
	}

	if t.overrideDir != "" {
		if err := overlay(t.overrideDir, "*.html", func(files []string) (err error) {
			html, err = html.ParseFiles(files...)
			return err
		}); err != nil {
			return err
		}
		if err := overlay(t.overrideDir, "*.txt", func(files []string) (err error) {
			text, err = text.ParseFiles(files...)
			return err
		}); err != nil {
			return err
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.t, t.text = html, text
	return nil
}

// current returns the two sets, reparsing them first when reloading is on. Both
// are returned together because a reload replaces both, and a caller holding one
// from before it and one from after would be reading two different themes.
func (t *Templates) current() (*template.Template, *texttemplate.Template, error) {
	if t.reload {
		if err := t.parse(); err != nil {
			return nil, nil, err
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.t, t.text, nil
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

func funcs(images blob.Storage) template.FuncMap {
	return template.FuncMap{
		// Amounts are cents everywhere in Go; the decimal point exists only in
		// the rendered page.
		"money": catalog.FormatPrice,
		// asset resolves a bundled file to its content-addressed URL.
		"asset": assetURL,
		// image resolves a product's image key to where it is served from. A
		// function rather than a field on the product, so it cannot be forgotten:
		// there is nothing for a handler to populate and therefore nothing to leave
		// unpopulated.
		"image": func(key string) string {
			if key == "" {
				return ""
			}
			return images.URL(key)
		},
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

// Execute renders one named template to bytes, writing nothing. Splitting this
// out of Render is what lets a caller turn a template failure into a 500: nothing
// has been sent at the point it returns an error, so the status is still the
// caller's to choose.
func (t *Templates) Execute(name string, data any) ([]byte, error) {
	html, _, err := t.current()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := html.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("handler: render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// Render executes one named template and writes it. It renders into a buffer
// first, so a template error becomes the caller's problem to answer rather than a
// half-written page that already claimed 200.
func (t *Templates) Render(w http.ResponseWriter, status int, name string, data any) error {
	body, err := t.Execute(name, data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

// String executes one named HTML template and returns it, for an email body
// rather than a response.
func (t *Templates) String(name string, data any) (string, error) {
	html, _, err := t.current()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := html.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("handler: render %s: %w", name, err)
	}
	return buf.String(), nil
}

// Text executes one named text template — the plain-text half of an email.
func (t *Templates) Text(name string, data any) (string, error) {
	_, text, err := t.current()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := text.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("handler: render text %s: %w", name, err)
	}
	return buf.String(), nil
}

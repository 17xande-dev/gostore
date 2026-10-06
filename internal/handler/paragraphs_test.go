package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/17xande-dev/gostore/internal/catalog"
)

func TestParagraphs(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"one line":   {"A demo garment.", "<p>A demo garment.</p>"},
		"two blocks": {"First.\n\nSecond.", "<p>First.</p><p>Second.</p>"},
		// A textarea submits CRLF; a stray \r must not survive into the page.
		"crlf":        {"First.\r\n\r\nSecond.", "<p>First.</p><p>Second.</p>"},
		"line break":  {"By Ps André Olivier.\nSoftcover.", "<p>By Ps André Olivier.<br>Softcover.</p>"},
		"blank lines": {"First.\n  \n\n\nSecond.\n", "<p>First.</p><p>Second.</p>"},
		"empty":       {"  \n\n ", ""},
		// Escaped before any tag is added, so a description cannot inject markup.
		"escaped": {"<script>x</script>\n\n& more", "<p>&lt;script&gt;x&lt;/script&gt;</p><p>&amp; more</p>"},
	} {
		if got := string(paragraphs(tc.in)); got != tc.want {
			t.Errorf("%s: paragraphs(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

// The product page is where descriptions are read, and a description typed as
// several paragraphs used to run together into one.
func TestStorefront_ProductDescriptionKeepsItsParagraphs(t *testing.T) {
	srv, store := newStorefront(t, testConfig(), "")
	p, err := store.Create(t.Context(), catalog.Product{
		Slug: "keeping-hope-alive", Title: "Keeping Hope Alive", Active: true,
		Description: "By Ps André Olivier.\n\nHope is an essential ingredient.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVariant(t.Context(), catalog.Variant{
		ProductID: p.ID, SKU: "KHA", PriceCents: 18000, StockQty: 3, Active: true,
	}); err != nil {
		t.Fatal(err)
	}

	res, body := get(t, srv, "/products/keeping-hope-alive")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", res.StatusCode)
	}
	if !strings.Contains(body, "<p>By Ps André Olivier.</p><p>Hope is an essential ingredient.</p>") {
		t.Error("the description's two paragraphs are not rendered as two paragraphs")
	}
}

package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The URL building is a plain function of the parameters and three numbers, so it
// is tested without a database. Query-string assembly is where encoding bugs live,
// which is why it is here and not in a template.

func TestParseSearch(t *testing.T) {
	cases := []struct {
		query      string
		wantQ      string
		wantCats   []string
		wantPage   int
		wantServed bool
	}{
		{"", "", nil, 1, true},
		{"q=quiet", "quiet", nil, 1, true},
		{"q=++quiet++", "quiet", nil, 1, true},
		// Under two characters is no search at all: a trigram index cannot help
		// below three, and one letter matches most of a catalog.
		{"q=a", "", nil, 1, true},
		{"q=ab", "ab", nil, 1, true},
		{"category=books&category=apparel", "", []string{"books", "apparel"}, 1, true},
		// A stray empty value is not a filter that matches nothing.
		{"category=", "", nil, 1, true},
		{"page=3", "", nil, 3, true},
		{"page=0", "", nil, 1, false},
		{"page=-2", "", nil, 1, false},
		{"page=two", "", nil, 1, false},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/products?"+tc.query, nil)
			got, served := parseSearch(r)
			if served != tc.wantServed {
				t.Fatalf("served = %v, want %v", served, tc.wantServed)
			}
			if !served {
				return
			}
			if got.Query != tc.wantQ {
				t.Errorf("Query = %q, want %q", got.Query, tc.wantQ)
			}
			if got.Page != tc.wantPage {
				t.Errorf("Page = %d, want %d", got.Page, tc.wantPage)
			}
			if strings.Join(got.Categories, ",") != strings.Join(tc.wantCats, ",") {
				t.Errorf("Categories = %v, want %v", got.Categories, tc.wantCats)
			}
		})
	}
}

func TestSearchParams_URLKeepsEveryFilter(t *testing.T) {
	p := searchParams{Query: "quiet machine", Categories: []string{"books", "apparel"}, Page: 2}

	got := p.url(3)
	for _, want := range []string{"q=quiet+machine", "category=books", "category=apparel", "page=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("page URL %q is missing %q", got, want)
		}
	}

	// Page 1 is the bare URL rather than ?page=1, so the catalog has one address
	// rather than two a crawler would index separately.
	if got, want := p.url(1), "/products?category=books&category=apparel&q=quiet+machine"; got != want {
		t.Errorf("page 1 URL = %q, want %q", got, want)
	}
	if got := (searchParams{}).url(1); got != "/products" {
		t.Errorf("the bare catalog URL is %q", got)
	}
}

func TestPaginate(t *testing.T) {
	t.Run("one page has no links", func(t *testing.T) {
		got := paginate(searchParams{}, 1, 1, 5)
		if got.HasPrev || got.HasNext {
			t.Error("a single page offers prev/next")
		}
		if got.FirstIndex != 1 || got.LastIndex != 5 {
			t.Errorf("showing %d–%d of 5", got.FirstIndex, got.LastIndex)
		}
	})

	t.Run("middle page", func(t *testing.T) {
		got := paginate(searchParams{}, 2, 3, 60)
		if !got.HasPrev || !got.HasNext {
			t.Error("a middle page is missing prev or next")
		}
		if got.PrevURL != "/products" || got.NextURL != "/products?page=3" {
			t.Errorf("prev = %q, next = %q", got.PrevURL, got.NextURL)
		}
		// 24 to a page: page 2 of 60 shows 25–48.
		if got.FirstIndex != 25 || got.LastIndex != 48 {
			t.Errorf("showing %d–%d of 60", got.FirstIndex, got.LastIndex)
		}
		if len(got.Links) != 3 {
			t.Fatalf("%d page links, want 3", len(got.Links))
		}
		if !got.Links[1].Current {
			t.Error("the current page is not marked")
		}
	})

	t.Run("the window stays a fixed width", func(t *testing.T) {
		// Deep in a long catalog the pager must not grow a link per page.
		got := paginate(searchParams{}, 50, 100, 2400)
		if len(got.Links) != maxPageLinks {
			t.Fatalf("%d page links, want %d", len(got.Links), maxPageLinks)
		}
		if got.Links[0].Number != 46 || got.Links[maxPageLinks-1].Number != 54 {
			t.Errorf("the window is %d–%d, want it centred on 50",
				got.Links[0].Number, got.Links[maxPageLinks-1].Number)
		}

		// And it stays inside the range at either end rather than shrinking.
		first := paginate(searchParams{}, 1, 100, 2400)
		if len(first.Links) != maxPageLinks || first.Links[0].Number != 1 {
			t.Errorf("at the start the window is %d links from %d", len(first.Links), first.Links[0].Number)
		}
		last := paginate(searchParams{}, 100, 100, 2400)
		if len(last.Links) != maxPageLinks || last.Links[maxPageLinks-1].Number != 100 {
			t.Errorf("at the end the window ends at %d", last.Links[maxPageLinks-1].Number)
		}
	})
}

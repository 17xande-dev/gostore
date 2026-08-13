package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Search, filtering and pagination for the one catalog route. Three parameters in
// any combination — `q` searches, `category` narrows, `page` pages — so a bare
// /products still means everything and nothing about the plain catalog changed.
//
// The URLs are built here rather than in the templates, with net/url, so every
// active filter survives every page link. Query-string assembly in a template is
// where encoding bugs live, and this project has already paid for that lesson once
// in the PayFast signature.

// pageSize is how many products a page holds. It is not configurable: a store that
// wants a different number is a store overriding the templates anyway, and one more
// environment variable to explain buys nothing.
const pageSize = 24

// minQueryLen is the shortest search worth running. A trigram index cannot help
// below three characters, and a one-character search matches most of a catalog, so
// anything shorter is treated as no search at all rather than as a search that
// disappoints.
const minQueryLen = 2

// searchParams is the catalog request as asked for, already cleaned up.
type searchParams struct {
	Query      string
	Categories []string
	Page       int
}

// parseSearch reads the three parameters. It reports ok=false only for a page
// number that cannot be served, which the caller turns into a 404 — the same
// stance as an inactive product being a 404 rather than an unlinked page. Clamping
// to the last page instead would make ?page=900 a silent success and let a crawler
// index one catalog under endless URLs.
func parseSearch(r *http.Request) (searchParams, bool) {
	q := r.URL.Query()

	p := searchParams{
		Query: strings.TrimSpace(q.Get("q")),
		Page:  1,
	}
	if utf8.RuneCountInString(p.Query) < minQueryLen {
		p.Query = ""
	}

	// Repeated parameters, which is what a checkbox list submits natively. Blank
	// values are dropped so a stray `?category=` is not a filter matching nothing.
	for _, slug := range q["category"] {
		if slug = strings.TrimSpace(slug); slug != "" {
			p.Categories = append(p.Categories, slug)
		}
	}

	if raw := q.Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return p, false
		}
		p.Page = n
	}
	return p, true
}

// values renders the parameters back into a query string, which is what makes a
// page link keep the search and the filters that produced it.
func (p searchParams) values() url.Values {
	v := url.Values{}
	if p.Query != "" {
		v.Set("q", p.Query)
	}
	for _, slug := range p.Categories {
		v.Add("category", slug)
	}
	return v
}

// filtered reports whether the shopper asked for anything, which decides between
// "nothing for sale yet" and "nothing matched".
func (p searchParams) filtered() bool { return p.Query != "" || len(p.Categories) > 0 }

// url renders the catalog URL for a page under these parameters. Page 1 is the
// bare URL rather than ?page=1, so the catalog has one address rather than two
// that a crawler would index separately.
func (p searchParams) url(page int) string {
	v := p.values()
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/products"
	}
	return "/products?" + v.Encode()
}

// facet is one category as the filter form shows it: always offered, whether or
// not the current search hits it.
type facet struct {
	Slug     string
	Name     string
	Selected bool
}

// pageLink is one entry in the pager.
type pageLink struct {
	Number  int
	URL     string
	Current bool
}

// pagination is everything the pager template needs, already resolved. It carries
// URLs rather than parameters because assembling them is the handler's job.
type pagination struct {
	Page       int
	Pages      int
	Total      int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	Links      []pageLink
	AllURL     string // the catalog with this page's filters cleared
	FirstIndex int    // 1-based index of the first product on this page
	LastIndex  int
}

// maxPageLinks is how many numbered links the pager shows before it starts
// eliding. Nine keeps the control a fixed width, which matters more than reaching
// every page by one click on a catalog this size.
const maxPageLinks = 9

// paginate builds the pager for one page of results. It is a plain function of
// three numbers so it can be tested without a database or a request.
func paginate(p searchParams, page, pages, total int) pagination {
	out := pagination{
		Page: page, Pages: pages, Total: total,
		HasPrev: page > 1,
		HasNext: page < pages,
		AllURL:  "/products",
	}
	if out.HasPrev {
		out.PrevURL = p.url(page - 1)
	}
	if out.HasNext {
		out.NextURL = p.url(page + 1)
	}
	if total > 0 {
		out.FirstIndex = (page-1)*pageSize + 1
		out.LastIndex = min(page*pageSize, total)
	}

	// A window around the current page, shifted to stay inside the range, so the
	// pager is the same width wherever the shopper is.
	first, last := 1, pages
	if pages > maxPageLinks {
		first = max(1, page-maxPageLinks/2)
		last = min(pages, first+maxPageLinks-1)
		first = max(1, last-maxPageLinks+1)
	}
	for n := first; n <= last; n++ {
		out.Links = append(out.Links, pageLink{Number: n, URL: p.url(n), Current: n == page})
	}
	return out
}

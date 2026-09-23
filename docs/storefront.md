# Storefront, cart and checkout

## Storefront

| Route | Serves |
|---|---|
| `GET /` | The index: the store name, a line to replace, and the newest few products |
| `GET /products?q=…&category=…&page=…` | The catalog, optionally searched, filtered and paged |
| `GET /products/{slug}` | One product, with its variants |

### The index page

Deliberately almost empty, and meant to be replaced. It exists so that a fresh
deployment has a working front door rather than a `404`, and it carries the least
that is still a page: the store name from `STORE_NAME`, one plain line, the four
newest products, and a link to the catalog.

The products are **example content, not a feature**. They are the newest four —
there is no `featured` flag, no extra column and nothing to tick in the admin,
because a front page that needs curating before it works is a front page that
ships empty. They share the catalog's card grid (`product_grid`), so restyling a
card changes both pages rather than one of them.

Replacing the whole thing is one `pages/index.gohtml` in `TEMPLATE_DIR` defining
`content`. See [Theming](theming.md#theming).

One thing to know before adding to it: `/` is served outside the CSRF layer,
because it carries no form and therefore sets no cookie — the only HTML page in
the store that sets none at all. **A form added to the index would be refused with
a `403`.** Link to a page that has one instead.

### When a page is not found

Every HTML surface answers a missing page with the same rendered `404`: an unknown
URL, a withdrawn or misspelled product, a `?page=` past the end of the catalog, an
order id that is not one. It says what happened and offers a search box and a way
back, because the one page a visitor reaches by accident should not be a dead end.

Two deliberate exceptions:

- **Byte endpoints stay plain.** A missing `/static/…` or `/images/…` answers with
  Go's one-line `404`, since nothing reads an HTML page out of an `<img>` tag.
- **A broken theme still answers `404`.** If an overridden `not_found` fails to
  render, the plain one is sent rather than an empty `200` — the status is what a
  browser and a crawler act on.

Installing it costs a `"/"` pattern on the mux, which is how a custom not-found
handler is done in Go: `ServeMux` has no `NotFoundHandler` to set. One consequence
is that a known path requested with an unregistered method now answers `404` rather
than `405`, because a pattern that matches beats one that would have matched under
a different method.

The two catalog routes below are read-only, and both answer twice over: a full page for an ordinary visit, and a
bare fragment when htmx asks (`HX-Request: true`, unless `HX-Boosted` says the browser is
replacing the whole document). One URL serves the store and an embedder, so there is no
second API to keep in step.

**A request from another origin gets no cookies at all** — that is what makes the catalog
droppable into someone else's page. A *first-party* visit does pass through the CSRF layer,
because the product page carries an add-to-cart form and a form needs a token; it never picks
up a cart cookie there. The two chains serve identical HTML.

Only active products with at least one active variant appear; an inactive product is a `404`,
not an unlinked page. Sold-out variants are *shown* and marked unavailable rather than
hidden, because a size vanishing from a selector reads as a bug to whoever is looking for it.

### Search and filtering

Three parameters on the one catalog route, in any combination: `q` searches, `category`
narrows, `page` pages. A bare `/products` still means everything, so nothing about the plain
catalog changed.

**Search matches words and spellings both, because neither alone is enough.** Postgres
full-text handles the first: a generated `tsvector` column with the title weighted above the
description, queried through `websearch_to_tsquery`, so "books" finds a title containing
"book". It cannot survive a typo. `pg_trgm` handles the second — trigram similarity finds
"quiet machnie" — and has no idea "books" and "book" are the same word. Each covers the other's
blind spot, and results are ordered by whichever of the two scores is higher.

The costs, stated because they are the ones an adopter will meet:

- **`pg_trgm` must exist.** It is a core contrib extension, present in the Postgres image this
  repo runs, and *trusted* since PostgreSQL 13, so the database owner can create it without
  superuser. Every managed host worth naming permits it. The migration creates it — see the
  fixed-schema rule under [Migrations](development.md#migrations).
- **A query under two characters is treated as empty**, because a trigram index cannot help
  below three and a one-letter search returns the whole catalog regardless.
- **English is hard-coded** in the `to_tsvector` configuration. Stemming is
  language-specific, and a store selling in another language wants that word changed.

**Selecting several categories widens the results, it does not narrow them.** So
`?category=books&category=apparel` returns both. The opposite reading — products that are
simultaneously a book and apparel — is
almost always empty, because these are kinds of thing rather than facets like size and colour.
The filter list itself always shows every category in its configured order, whether or not the
current search hits it: a list that reshapes itself as you type moves the option you were
reaching for.

**Pagination is `LIMIT`/`OFFSET`, 24 to a page**, and the total is counted in the same query as
the page rather than a second one that could disagree with it. A page past the end is a `404`,
for the same reason an inactive product is: it stops `?page=900` from being a silent success
that a crawler will happily index. The cost of offset is that a deep page scans and discards
rows on the way to its window — cheap at the size of catalog this store is for, and the reason
a cursor was not worth the complexity here.

**None of it needs JavaScript.** The filter is an ordinary GET form whose checkboxes share the
name `category`, which is how one form produces repeated parameters without help; the page
links are ordinary links. htmx then upgrades both to swap just the results list and push the
URL, so the address bar always describes what is on screen and a search is a shareable link.

### Embedding the catalog elsewhere

Set `EMBED_ORIGINS` to the origins allowed to fetch the fragments, and they can be dropped
into a page on another domain:

```html
<div hx-get="https://store.example.com/products" hx-trigger="load"></div>
```

That is the whole integration. It works because the fragments need no cookie: `EMBED_ORIGINS`
controls both the CORS allowance and the CSP's `frame-ancestors`, and no credentialed CORS
header is ever sent, so a permissive origin list cannot become a way to act as somebody.

Everything from "add to cart" onward stays first-party on the store's own domain. That keeps
the cart cookie first-party and sidesteps `SameSite=None`, third-party cookie blocking and
iframe checkout entirely — the split is a feature of the design, not a limitation of it.

Concretely: the embedded fragment carries **no** add-to-cart form, and links to the store's
own product page instead. A cart form on another origin could not work anyway — `SameSite=Lax`
withholds the cookie on a cross-site post, and the CSRF origin check would refuse it.

**The embedded fragment carries no search box, filter or page links either**, for a different
reason: those controls push the URL they navigate to, and inside somebody else's page that
would rewrite *their* address bar. An embedder gets the first page and a link through to the
full catalog on the store's own domain, which is where searching belongs. That link matters —
an embedded fragment silently showing 24 products out of 200 would look like the whole shop.
Searching and filtering are first-party, on the same reads-anywhere, writes-first-party line
everything else here follows.

## Cart

| Route | Does |
|---|---|
| `GET /cart` | The cart page (or its body, for htmx) |
| `GET /cart/status` | The "N items in your cart" fragment |
| `POST /cart/items` | Add a variant |
| `POST /cart/items/{variantID}` | Set a quantity; **0 removes the line** |
| `DELETE /cart/items/{variantID}` | Remove a line (what htmx sends) |

A cart is a database row keyed by an opaque 24-byte random token that is also the cookie
value — `HttpOnly`, `SameSite=Lax`, 30 days, scoped to `/cart`. Not a signed cart carried in
the cookie: prices and stock are live server-side truth that has to be re-read on every
render anyway, so reading the cart from the database is not extra work, it is the same work.
The token is unguessable rather than signed, because holding one grants nothing beyond one
anonymous basket.

Consequences worth knowing before changing any of it:

- **The cart holds quantities, not prices.** Every render prices the lines from the catalog
  as it stands, so a price change or a sell-out shows up next time the cart is looked at.
  Snapshotting happens when the order is created, not before.
- **Withdrawn or sold-out lines stay visible** and are marked unavailable, with the reason,
  and they block checkout. A line vanishing between page loads reads as a bug — or worse, as
  a silent change to the total.
- **Stock is checked against the resulting total**, so two adds of three cannot smuggle six
  past a limit of four. A refusal says how many are actually left.
- **A cart row is created on the first add**, not on the first visit, so browsing leaves no
  trail of empty carts.
- **A stale cookie starts a fresh cart** rather than an error page, for shoppers returning
  after the cleanup job has been through.
- **Without JavaScript everything still works**: forms post and redirect, and the remove
  button submits quantity 0. With htmx, adding swaps a small status block so the shopper
  keeps their place, and quantity changes swap the cart body.
- Deleting a variant in the admin **empties it from carts** (`ON DELETE CASCADE`), because an
  abandoned cart must not stop the shop owner editing the catalog. `order_items` deliberately
  does the opposite: purchase history is not rewritable.

## Checkout

| Route | Does |
|---|---|
| `GET /cart/checkout` | The shipping form, alongside what is being bought |
| `POST /cart/checkout` | Creates a **pending** order and hands over to the gateway |
| `GET /cart/checkout/success` | The gateway's `return_url` — **informational only** |
| `GET /cart/checkout/cancel` | The gateway's `cancel_url` |
| `GET /cart/checkout/status` | The QR hand-over's poll — reports the order's status, grants nothing |
| `POST /payments/{gateway}/callback` | The only thing that can mark an order paid |

**Checkout lives under `/cart`, not at `/checkout`.** The cart cookie is scoped to `/cart`
so the catalog pages stay genuinely cookie-free and embeddable, and a page at `/checkout`
would therefore never be sent the token identifying the basket it is meant to be checking
out. Nesting it costs a URL segment; the alternatives were giving the catalog a cookie back
or issuing a second one.

The order of events matters more than the routes do:

- **An order is a snapshot.** A cart holds quantities and prices everything live; an order
  copies the title, options and unit price in as they were. A later price rise, rename or
  withdrawal cannot rewrite what somebody bought.
- **The total is computed from the catalog inside the transaction that creates the order**,
  never from the figure the submitted page happened to be showing. That total is what the
  gateway is asked for and what its notification is checked against, so it has to be a number
  the database agrees with.
- **Stock does not move at checkout.** It moves when the money arrives. An abandoned checkout
  therefore holds no inventory, which is the right trade for a small shop: two people can
  reach a payment page for the last item, and the second one is refunded rather than everyone
  being blocked by carts nobody will pay for.
- **The cart survives checkout** and is emptied when payment succeeds, so backing out of the
  gateway's page leaves the basket intact.
- **`/cart/checkout/success` grants nothing.** A shopper can navigate there without paying, so
  it says the payment is being confirmed rather than that it succeeded. It names the order —
  the cart cookie identifies it, and a reference is what a customer needs to quote.

**Which gateway** is a field on the checkout form, resolved before the order row is written —
`orders.gateway` records it, and the callback later refuses to settle the order unless it
agrees. A store with one gateway renders it as a hidden field and asks nothing; with two it
renders radios. An unrecognised name is refused rather than defaulted, because sending
somebody to pay through a provider they did not choose is not an improvement on an error
message.

The hand-over itself takes one of two shapes, and each has a consequence for the CSP:

- **A form post** (PayFast) is a real cross-origin submission, not a redirect, so the
  gateway's origin must be in `form-action`, and the submit-on-load script is a **file**
  (`/static/redirect.js`) because `script-src 'self'` forbids the inline script that would
  otherwise do it. Without JavaScript the form's button is the whole mechanism, and it says
  so.
- **A link and a QR code** (SnapScan) needs nothing in `form-action` — a top-level navigation
  is not governed by it — but the QR image is served by the gateway, so its origin must be in
  `img-src`. Both origins are declared by the gateway itself through `Gateway.CSP()` rather
  than configured, because a missing one is refused by the browser and nowhere else.

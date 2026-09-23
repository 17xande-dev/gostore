# Theming

A theme is two directories: templates that override the embedded ones by name, and assets
that override the bundled ones by name. Nothing is forked, nothing is rebuilt, and anything
you do not override keeps coming from the binary.

| | Points at | Overrides |
|---|---|---|
| `TEMPLATE_DIR` | a directory shaped like [`internal/handler/templates/`](../internal/handler/templates) | templates, by the path of the file — `pages/cart.gohtml` replaces `pages/cart.gohtml` |
| `STATIC_DIR` | a flat directory of assets | bundled files, by filename |

`make up` and `make run` already set both, at [`theme/templates`](../theme) and
[`theme/static`](../theme), with **`THEME_RELOAD=true`** — so writing a theme is editing a file
and refreshing the page. Both directories are empty on a clean checkout, which is why the
store looks the same until you put something in one.

## Writing one

Start from a default rather than from nothing — the defaults are meant to be read:

```sh
cp internal/handler/static/styles.css theme/static/styles.css     # restyle
mkdir -p theme/templates/pages                                    # the path is the contract
cp internal/handler/templates/pages/products.gohtml theme/templates/pages/
```

Then edit and refresh. Deleting your copy puts the default back, also without a restart.
Keep the subdirectory: an override is found at the same path it has in the defaults, and a
`products.gohtml` at the top of `TEMPLATE_DIR` is a file nothing looks for.

Most themes never need a template at all. **The CSS is the intended level to work at**:
every colour, size and spacing value in the default theme is a custom property in one
`:root` block at the top of `styles.css`, and no colour literal appears anywhere below it.
Rebranding is editing a dozen values.

```css
/* theme/static/styles.css — the whole theme, for many stores */
:root {
  --paper: #fffdf8;  --paper-sunk: #f4efe4;
  --ink: #241f18;    --ink-soft: #5c5348;  --ink-faint: #8d8375;
  --rule: #e2d9c8;
  --accent: #7a2e1f; --accent-ink: #fffdf8;  /* buttons, links, prices */
  --warn: #8a2f1d;
  --font: "Iowan Old Style", Georgia, serif;
  --radius: 0;       --page: 1000px;         /* square corners, narrower column */
}
```

The full set is `--paper`, `--paper-sunk`, `--ink`, `--ink-soft`, `--ink-faint`, `--rule`,
`--accent`, `--accent-ink`, `--warn`; `--font`, `--font-mono`, `--text`, `--text-small`,
`--text-large`, `--title`, `--title-page`; `--gap-xs` through `--gap-xl`; `--radius`,
`--radius-lg`, `--measure`, `--page`. Overriding just these in a file that then `@import`s
nothing means you are also replacing the rest of the stylesheet — so either copy the whole
default and edit its `:root`, or write your own from scratch. There is no cascade between
the bundled file and yours: same name, one wins.

## Web fonts

**No web font in the default theme.** The system font stack is what every operating system
already has, it loads instantly, and it puts no third-party request on the page. Two ways to
change that.

**Self-host.** Drop the `.woff2` into `STATIC_DIR` — that is one of the reasons new names are
served — and reference it from your stylesheet with a `/static/...` URL. Nothing else changes:
the file is served from this origin, so the CSP already allows it and no config is involved.
Fonts under an open licence, which is most of Google's, can be used this way; `.woff2` alone
is enough for every browser this project supports.

**Use a hosted service.** Adobe Fonts requires this — its licence has no self-host tier — and
it needs two variables:

```sh
FONT_ORIGINS=https://use.typekit.net,https://p.typekit.net
FONT_CSS_URL=https://use.typekit.net/abc1def.css
```

`FONT_CSS_URL` is the stylesheet the default layout links from its `<head>`. `FONT_ORIGINS`
widens the CSP, and it widens **two** directives, because a hosted font is two fetches: the
browser fetches the stylesheet declaring the fonts (`style-src`) and then the font files that
stylesheet names (`font-src`). Typekit splits those across two hosts, which is why both are
listed — a kit that loads and *still* renders in the fallback font almost always means
`p.typekit.net` is missing. Google Fonts is the same shape, with
`fonts.googleapis.com,fonts.gstatic.com`.

Set both or neither. `FONT_ORIGINS` alone allows a font nothing asks for; `FONT_CSS_URL` alone
is a boot failure, because the CSP would block the stylesheet and the only symptom in a browser
is a console warning.

Widening the CSP and linking the kit makes the family *available* — it does not apply it. Set
`--font` in a `styles.css` under `STATIC_DIR` to use it.

Three things worth knowing before choosing the hosted route:

- **Only the `<link>` embed is supported.** Adobe's Web Project panel offers a JavaScript
  loader by default; pick the CSS embed instead. The loader needs `script-src` widened,
  `connect-src` opened for its config fetch, and a nonce for both the inline `<script>` it
  gives you and the inline `<style>` it injects. See
  [Response headers](security.md#response-headers) for why that answer is no.
- **`script-src` stays `'self'` either way.** A font origin cannot run JavaScript on any page
  of this store, the checkout included. That is what makes this a narrow widening.
- **It puts a third-party request on every page, including the checkout**, and the font CDN
  sees your visitors' IP addresses. That is a real consideration under the GDPR — a German
  court has ruled against a site for exactly this with Google Fonts — and a reason to prefer
  self-hosting where the licence allows it.

## Overriding templates

Overriding is per *path*: a file at `pages/index.gohtml` under `TEMPLATE_DIR` replaces the
definitions in the embedded `pages/index.gohtml` and leaves everything else alone. Put it
in the wrong subdirectory and it is simply not found — nothing warns, because a directory
of files the server has never heard of is a normal thing for a theme directory to contain.

The directory decides two things, so it is worth knowing before copying anything:

| Directory | Holds | Wrapped in |
|---|---|---|
| `layouts/` | `public.gohtml`, `admin.gohtml` — each defining `layout` | — |
| `partials/` | pieces parsed into **every** page: `document_head`, `csrf`, `err`, `product_grid`, `error_reference` | — |
| `pages/` | the storefront, and the admin sign-in page | `layouts/public.gohtml` |
| `admin/` | everything behind the admin login | `layouts/admin.gohtml` |
| `mail/` | `email_order_paid.gohtml` and the two `.txt` bodies | nothing — a message is not a page |

**Each page is parsed into a set of its own**, which is the thing to understand before
writing a theme. A definition in `pages/products.gohtml` reaches the catalog and no other
page, so a page can fill in a block, restyle a partial for itself, or define a fragment,
without any of it leaking into the cart. The cost is the other side of the same coin: a
page can only call a partial, its own layout, or something it defines itself — a call to
anything else renders as a `500` on that page, because Go resolves template names when a
template runs and not when it is parsed. Anything two pages need belongs in `partials/`.

**A page file defines `content`**, and the layout renders it. Two blocks exist:

| Block | Filled by | Is |
|---|---|---|
| `content` | every page, always | The page itself. A page that defines none refuses the boot |
| `nav_extra` | `pages/products.gohtml`, if you want it | An addition to the site nav. Empty by default: the catalog's filter form is rendered above the grid instead, and two copies of one search form on one page is two search boxes |

Moving the filter form into the header is the two-definition case the blocks exist for. A
`pages/products.gohtml` in `TEMPLATE_DIR` holding nothing but this does it:

```html
{{define "content"}}
<h1>All products</h1>
<div id="products">{{template "products_list" .}}</div>
{{end}}

{{define "nav_extra"}}{{template "products_filters" .}}{{end}}
```

Nothing else in the catalog is touched: `products_filters`, `products_list` and
`products_pager` keep coming from the default file, since an override replaces the
definitions it names and no others.

The pages, and the fragments each one owns — a fragment is what an htmx swap renders, so
overriding one changes both the full page and the swap, which is what keeps the two
consistent:

| File | Also defines | Is |
|---|---|---|
| `pages/index.gohtml` | | The front page. Small on purpose — this is the one most shops replace outright |
| `pages/products.gohtml` | `products_list`, `products_filters`, `products_pager` | The catalog, the results inside it, the search and category form, and the page links |
| `pages/product.gohtml` | `product_detail`, `add_to_cart` | The product page, its body, and the variant/quantity form |
| `pages/cart.gohtml` | `cart_items`, `cart_status` | The cart, the lines htmx swaps, and the count a product page shows after an add |
| `pages/checkout.gohtml` | `checkout_form` | The checkout, and the form htmx swaps back with its errors |
| `pages/checkout_redirect.gohtml`, `pages/checkout_success.gohtml`, `pages/checkout_cancel.gohtml` | | The hand-over to the gateway, and the two pages a shopper comes back to |
| `pages/downloads.gohtml` | | The page a buyer reaches from their confirmation email |
| `pages/not_found.gohtml` | | The 404, which every mistyped URL and withdrawn product lands on |
| `pages/error_client.gohtml`, `pages/error_server.gohtml` | | The 4xx and 5xx pages. See [When something goes wrong](operations.md#when-something-goes-wrong) |
| `pages/admin_login.gohtml` | | The sign-in page. In `pages/` deliberately: it uses the public layout, so it cannot offer to sign out of a session it does not have |
| `admin/admin_*.gohtml` | `variant_errors`, `product_image`, `product_files` | The admin. The files keep their `admin_` prefix because a page's file name is the name it is rendered by, and `admin/products.gohtml` would collide with the catalog |
| `partials/product_grid.gohtml` | | The card grid, **shared by the catalog and the index**. Override this to restyle a product card everywhere it appears — overriding `products_list` alone changes the catalog only |
| `partials/document.gohtml` | | Everything from the doctype to `</head>`, on every page of both layouts. Two htmx settings in it are load-bearing; keep them |
| `mail/*` | | See [Email templates](email.md#email-templates) |

Four things to know before writing one:

- **Class names are the contract** between the templates and the stylesheet. They describe
  what a thing is — `.product-card`, `.site-header`, `.price`, `.field` — so that overriding
  one side and not the other keeps working. Change the markup and keep the names, or change
  both together.
- **Every form needs `{{template "csrf" .CSRFToken}}`**, or it gets a `403`. See
  [CSRF](security.md#csrf).
- **Templates get exactly the data the handler passes.** Every page embeds `.Title`,
  `.StoreName`, `.Currency` and `.CSRFToken`, plus its own — `.Products`, `.Product`,
  `.Cart`, `.Order`. The functions available are `money` (cents → a displayed amount),
  `asset` (a bundled or overridden file → its hashed URL), `image` (a product's image key →
  where it is served from) and `linebreaks`.
- **A field or a template name that does not exist is a `500` on that page**, not a refused
  boot: Go checks both when a template *runs*, not when it is parsed. This is the reason
  `nav_extra` is filled by the catalog's own file rather than called from the layout —
  `{{template "products_filters" .}}` in the site nav renders on the catalog and breaks
  every other page, because none of their data carries `.Search` or `.Facets`. Render each
  page you have touched before shipping the theme.

## Reloading, and not reloading

`THEME_RELOAD=true` re-reads both directories on **every request**. It is for writing a
theme and for nothing else, and a deployment must leave it off:

- It reparses every template and re-reads every asset per request.
- It moves a *later* mistake — a file saved half-written while the server is up — from
  impossible to a `500` on whichever page uses it. A theme that is already broken at startup
  still fails the boot either way: both directories are validated once before anything
  serves, and a template that does not parse or a `STATIC_DIR` that cannot be read
  **refuses to start**. That is the behaviour you want in front of customers, where the
  alternative is finding out from the first shopper.

Either way the theme is read from disk at runtime, so shipping a change is replacing files
and restarting — never a rebuild. The server logs a warning at startup whenever reloading is
on, so a deployment that has it by accident says so.

One thing does not need reloading to appear: **asset URLs carry a hash of the file's
contents** (`/static/styles.css?v=fc1508f97297`), so a replaced stylesheet is a different
URL and no cache can serve the old one. That is what makes a refresh enough rather than a
hard reload.

## Bundled assets

Four things ship inside the binary: htmx, `redirect.js`, a `logo.svg` and a
`placeholder.svg`. A store with no configuration at all therefore has a mark in its header
and a picture on every product card.

These are **not** product images. A product image is uploaded, keyed and deleted by the
application; these are replaced by an operator. Keeping them apart means a sweep over
uploaded objects can never consider a logo an orphan.

**Override any of them with `STATIC_DIR`**, which is to assets what `TEMPLATE_DIR` is to
templates: a file there shadows a bundled one of the same name, and a new name is served
too — so an overridden template can reference its own `hero.png`. Read at startup, so a
change needs a restart and never a rebuild — or no restart either, under
[`THEME_RELOAD`](#reloading-and-not-reloading). Rebranding is dropping a `logo.svg` into a
directory.

The defaults are deliberately generic: the logo has no text, because the store's name comes
from `STORE_NAME` and a name baked into a logo would be wrong for every adopter but one.

Everything is served from this origin, which is why the CSP needs no allowance beyond
`'self'` for any of it. htmx in particular is vendored rather than loaded from a CDN, so the
store works offline and no third-party origin sits near the payment path. `redirect.js` is a
file for the same reason: with no `'unsafe-inline'`, an inline script would simply be
blocked, leaving the shopper on a page waiting for something the browser refused to run.

Asset URLs carry a hash of the contents (`/static/logo.svg?v=fc1508f97297`) and are served
`immutable`, so a replacement appears immediately rather than after a cache expires.

**Only extensions in the content-type map are served** — images, CSS, JS and fonts. That is
what keeps a note left in either directory from becoming a URL, and it applies to
`STATIC_DIR` too, so an `.html` or a `.php` dropped there is not published. See
[`internal/handler/static/README.md`](../internal/handler/static/README.md).

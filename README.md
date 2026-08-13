# gostore

A small, self-hostable online store written in Go: `html/template` fragments for an
htmx frontend, PostgreSQL for storage, and [PayFast](https://payfast.io) for payments.
Stdlib-first, with a deliberately tiny dependency surface.

> **Status: early.** The skeleton (config, migrations, container stack, health check), the
> catalog (products, variants, seed command, admin CRUD), admin authentication, the
> storefront, the cart, checkout against PayFast, order emails, the admin's order views,
> product image uploads, the hardening pass, categories, and catalog search with filtering
> and pagination all work. What remains is the publishing checklist — see the build order
> below.
>
> The compose stack ships a **published development password** (`gostore`) and PayFast's
> **published sandbox credentials**, so `make up` gives you a working admin and a working
> checkout. Replace them, along with `SESSION_SECRET`, before anyone else can reach the
> deployment — see [Admin](#admin) and [Payments](#payments).

## Why

Go has no maintained open-source store, and no PayFast integration at all. This aims to
be honest and good at one thing: a small catalog of physical goods — books and apparel —
sold in ZAR, with variants, stock, and a spare admin UI. It is not a general commerce
platform.

## Quickstart

```sh
git clone https://github.com/17xande-dev/gostore
cd gostore
make up
curl localhost:8080/healthz   # -> ok
make seed                     # load the demo catalog
open http://localhost:8080/admin      # sign in with the development password: gostore
```

`make up` starts Postgres, [mailpit](http://localhost:8025) (captures outgoing email),
[MinIO](http://localhost:9001) (S3-compatible object storage) and the server. Migrations
are applied automatically on boot. It also mounts [`theme/`](theme) into the server with
reloading on, so a stylesheet or template dropped in there takes effect on the next page
refresh — see [Theming](#theming).

Other useful targets:

| Target | Does |
|---|---|
| `make down` | Stop the stack (`make down ARGS=-v` also deletes the data volumes) |
| `make run` | Run the server on the host against the compose Postgres |
| `make seed` | Load a products JSON file (`SEED_FILE=...`, default `testdata/products.json`) |
| `make test` | Run every test, including the database-backed ones |
| `make hashpw` | Prompt for a password and print `ADMIN_PASSWORD_HASH` + `SESSION_SECRET` |
| `make psql` | Open a `psql` shell on the compose database |
| `make logs` | Follow the server logs |
| `make migrate` | Apply pending migrations without starting the server |
| `make migrate-status` | Show which migrations have been applied |
| `make check-config` | Validate the full server configuration and exit |
| `make sqlc` | Regenerate the stores' query code after editing SQL (`make sqlc-install` first) |

## Configuration

Everything comes from the environment; see [`.env.example`](.env.example) for the full
list with defaults.

| Var | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | **yes** | — | Postgres connection string |
| `ADMIN_PASSWORD_HASH` | **yes** | — | argon2id hash of the admin password (`make hashpw`); a bcrypt one still verifies |
| `SESSION_SECRET` | **yes** | — | 32+ random bytes, base64, signs the session cookie |
| `SESSION_SECRET_PREVIOUS` | no | — | The outgoing secret during a rotation; still verifies, never signs |
| `SESSION_TTL_HOURS` | no | `24` | How long a sign-in lasts |
| `PAYFAST_MERCHANT_ID` | **yes** | — | From the PayFast dashboard |
| `PAYFAST_MERCHANT_KEY` | **yes** | — | From the PayFast dashboard |
| `PAYFAST_PASSPHRASE` | no | — | The account's salt passphrase; must match the dashboard exactly |
| `PAYFAST_SANDBOX` | no | `true` | `false` takes real money |
| `PAYFAST_NOTIFY_URL` | no | derived | Override when PayFast cannot reach `BASE_URL` (a tunnel) |
| `PAYFAST_ALLOWED_CIDRS` | no | published ranges | Override the source ranges; `any` disables the check |
| `TRUST_PROXY_IP` | no | `false` | Believe `X-Forwarded-For`; only with a proxy that replaces it |
| `PORT` | no | `8080` | Listen port |
| `BASE_URL` | no | `http://localhost:8080` | Public origin, for absolute URLs |
| `STORE_NAME` | no | `gostore` | Displayed store name |
| `CURRENCY` | no | `ZAR` | Currency code (PayFast requires `ZAR`) |
| `EMBED_ORIGINS` | no | — | Origins allowed to fetch and frame the catalog fragments |
| `TEMPLATE_DIR` | no | — | Directory of templates that override the embedded defaults |
| `STATIC_DIR` | no | — | Directory of assets that override the bundled ones (logo, placeholder, CSS) |
| `THEME_RELOAD` | no | `false` | Re-read those two directories on every request, so a theme edit needs a refresh and not a restart. Development only |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |
| `SHUTDOWN_TIMEOUT_SECONDS` | no | `15` | Grace period for in-flight requests |
| `SMTP_HOST` | no¹ | — | Mail relay. With no mail configured, receipts are logged and dropped |
| `EMAIL_FROM` | no¹ | — | Sender address |
| `SMTP_PORT` | no | `587` | `465` with `SMTP_TLS=tls`, `1025` for mailpit |
| `SMTP_TLS` | no | `starttls` | `starttls`, `tls` (implicit) or `none` (development only) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | no | — | Omit both for a relay that authenticates by address |
| `EMAIL_REPLY_TO` | no | — | When replies should not go to `EMAIL_FROM` |
| `ORDER_NOTIFY_EMAIL` | no | — | Sends a copy of each paid order to whoever packs it |
| `IMAGE_DIR` | no³ | — | Store images in this directory, served by this server |
| `BLOB_ENDPOINT` | no³ | — | Object storage host[:port], no scheme |
| `BLOB_BUCKET` | no² | — | Bucket name |
| `BLOB_ACCESS_KEY_ID` / `BLOB_SECRET_ACCESS_KEY` | no² | — | Credentials |
| `BLOB_PUBLIC_BASE_URL` | no² | — | Where images are **read** from — not where they are written |
| `BLOB_REGION` | no | `auto` | What R2 wants; GCS and MinIO ignore it |
| `BLOB_USE_TLS` | no | `true` | `false` only for a MinIO on the same machine |
| `RATE_LIMIT_LOGIN_PER_MINUTE` | no | `10` | Per client IP; `0` disables |
| `RATE_LIMIT_CHECKOUT_PER_MINUTE` | no | `20` | Per client IP; `0` disables |
| `RATE_LIMIT_CALLBACK_PER_MINUTE` | no | `120` | Per client IP; `0` disables |
| `CART_TTL_DAYS` | no | `60` | How long an untouched cart survives |

¹ `SMTP_HOST` and `EMAIL_FROM` are individually optional but must be set **together** — a
half-configured relay is a boot failure, because the alternative is a receipt that silently
never arrives.

² The `BLOB_*` set is all-or-nothing for the same reason: `BLOB_ENDPOINT` with any of the
others missing refuses to boot rather than failing at the first upload.

³ `IMAGE_DIR` and `BLOB_ENDPOINT` are the two image backends and are mutually exclusive —
both set refuses to boot, because which one wins would otherwise be a guess. With neither,
products cannot have images and the admin says so.

## Admin

The admin lives at `/admin`, behind one password. Generate the two values it needs:

```sh
make hashpw            # prompts, echoes nothing, prints both vars
```

Set them in the environment and restart. The plain password is never stored or
configured — only its argon2id hash — so a leaked env file or a `ps` listing does not hand
over the credential. See [Hardening](#password-hashing) for the parameters and for why a
bcrypt hash from an older deployment still works.

**One operator, no sessions table.** A session is a cookie signed by
[gorilla/securecookie](https://github.com/gorilla/securecookie) whose payload is the
expiry. The expiry is inside the signed value, not just in the cookie's metadata, so a
client cannot extend it, and the signature covers the cookie's name as well as its value.
Verifying costs no database round trip and expired sessions need no cleanup job.

Rotating the secret does not sign you out: set `SESSION_SECRET` to the new value and
`SESSION_SECRET_PREVIOUS` to the old one, deploy, and drop the previous value once
`SESSION_TTL_HOURS` has passed. Existing sessions keep verifying; new ones are signed with
the new secret only.

The trade is per-session revocation: there isn't any, short of rotating the secret without
a previous value, which signs *everything* out. Wanting to revoke one session, or wanting a
second admin with different permissions, is the documented point at which this should
become a `sessions` table with a real user model — not something to bolt onto the cookie.

Notes:

- The cookie is `HttpOnly`, `SameSite=Lax`, scoped to `/admin`, and `Secure` whenever
  `BASE_URL` is `https://` — so it is never sent with the cookie-free embeddable catalog
  fragments.
- htmx requests that have lost their session get `401` and `HX-Refresh: true` instead of a
  redirect, because swapping a login page into a fragment produces a broken hybrid.
- Login is rate limited to 10 attempts a minute per IP; see [Hardening](#hardening).

## CSRF

Every state-changing request — admin or cart — needs a
[nosurf](https://github.com/justinas/nosurf) token, submitted as a `csrf_token` form field
(the `{{template "csrf" .CSRFToken}}` partial) or an `X-CSRF-Token` header. A request without
one gets `403`.

CSRF is mounted over groups of routes rather than the whole server, for a reason worth
knowing before adding any: nosurf sets a token cookie on every response it handles, and a
catalog fetched from another origin must stay cookie-free. So `/admin`, `/cart` and
*first-party* catalog pages go through it — the last of those because the product page
carries an add-to-cart form — while the same catalog pages fetched cross-origin do not.
Grouping also means the payment callback, which cannot carry a token and must never require
one, will be exempt by not being in a group at all, rather than by an exempt-path string that
has to keep matching the route.

The trap this design walks into, if you extend it: **a page rendered outside the CSRF layer
gets an empty `.CSRFToken`**, and every form on it is then refused with a `403`. Any new page
carrying a form has to be inside one of these groups.

nosurf also requires that an unsafe request identify its origin, via `Sec-Fetch-Site`,
`Origin` or `Referer`. Browsers always send at least one; **`curl` does not**, so manual
testing needs `-H "Origin: http://localhost:8080"` or the request is rejected before the
token is even examined.

The origin nosurf compares against is built from the request's `Host` and a scheme it
assumes is `https` unless told otherwise, so the scheme is taken from **`BASE_URL`** rather
than from the connection: behind a TLS-terminating proxy the connection is plain HTTP while
the browser's origin is `https`. Getting `BASE_URL` wrong therefore breaks every admin form
with a `403`, not just absolute links.

## Hardening

### Rate limits

Per client IP, on three surfaces, with a token bucket from
[`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) and the keying and
eviction written here — the algorithm is the part with the clock edge cases already found
in it, and a bucket per client with bounded memory is where the decisions are.

| Route | Default | Why |
|---|---|---|
| `POST /admin/login` | 10/min | Brute force. argon2id's cost makes each attempt expensive, but cost is not a limit |
| `POST /cart/checkout` | 20/min | Order-row spam, loose enough that double-clicking never trips it |
| `POST /payments/{gw}/callback` | 120/min | **The reason the limiter exists**: unauthenticated, and every accepted request makes the store POST to the gateway — an amplifier |

The burst is a third of the allowance (minimum 2), so `10/min` means three attempts
immediately and then one every six seconds. A refusal is `429` with `Retry-After`. Limits
are applied on the line that registers each route, not wrapped around a prefix, for the
same reason `RequireAdmin` is: a prefix wrapper is one refactor away from silently not
covering a new route.

**The callback's `429` is not a contradiction of the always-`200` rule.** `200` means
*read and decided*, so a gateway does not retry a forgery. A throttled request has not
been read, and a retry is exactly what should happen — hence the limiter sits in front of
the handler and answers `429`, which PayFast reads and honours.

Only the POST on `/admin/login` is limited. Limiting the GET would lock an operator out of
the page carrying the message explaining why.

Idle buckets are evicted on a lazy sweep during an ordinary request, so there is no
goroutine to own and shut down for a map that is usually tiny, and the map cannot grow
without bound as an attacker cycles addresses.

**Catalog search is deliberately not limited**, and it is worth being explicit since it is the
one read that costs more than a primary-key lookup: every surface above is a `POST`, and
`GET /products` is none of them. A search is a bounded index scan over a small table, it holds
no lock and writes nothing, and the page it returns is cacheable — so the limiter would mostly
be throttling a crawler doing something harmless. That reasoning depends on the catalog staying
small; a store whose search starts showing up in slow-query logs should give `/products` its
own bucket, which is one line where the route is registered.

### Password hashing

argon2id, via `x/crypto/argon2` — no new dependency, since `x/crypto` was already here for
bcrypt. Parameters are RFC 9106's second recommendation: 64 MiB, three passes, four lanes,
encoded into the hash as a standard PHC string:

```
$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
```

Because the parameters live in the hash, raising them later needs no migration: existing
hashes keep verifying at their own settings and the next `make hashpw` writes the new ones.

**A bcrypt hash still verifies.** `CheckPassword` dispatches on the prefix, so an existing
deployment's `ADMIN_PASSWORD_HASH` keeps working and moves to argon2id whenever the
operator next runs `make hashpw`. New hashes are argon2id only.

Two details that are defence rather than decoration:

- Verification **caps the memory a stored hash may request** at 1 GiB. Without that, a
  mistyped `ADMIN_PASSWORD_HASH` claiming `m=4194304` would try to allocate four gibibytes
  on the first login attempt — a denial of service delivered by a typo.
- The hash is **parsed at boot**, so an unreadable one is a startup failure naming the
  problem instead of an admin who can never sign in and nothing in the logs to say why.

### Response headers

```
Content-Security-Policy: default-src 'self'; img-src 'self' <bucket> https: data:;
  style-src 'self' 'unsafe-inline'; script-src 'self'; form-action 'self' <gateway>;
  base-uri 'none'; object-src 'none'; frame-ancestors <embed origins or 'none'>
Permissions-Policy: geolocation=(), camera=(), microphone=(), payment=()
Referrer-Policy: strict-origin-when-cross-origin
X-Content-Type-Options: nosniff
Strict-Transport-Security: max-age=63072000; includeSubDomains   (https deployments only)
```

`img-src` is `'self'` plus the bucket and nothing else, because a product image is always
bytes this store holds. Every other directive is closed to this origin, with no
`'unsafe-inline'` anywhere — which is worth knowing before writing a theme:

- **`style-src 'self'`** — it used to carry `'unsafe-inline'`, because restyling through
  `TEMPLATE_DIR` had no other legal way to apply CSS: there was no stylesheet and no way for
  an adopter to add one. Both exist now — a bundled `styles.css` and `STATIC_DIR` to replace
  it — so the concession is gone. **An overriding template cannot use `style` attributes or
  `<style>` blocks**; put the CSS in a stylesheet in `STATIC_DIR`. (Email bodies still use
  inline styles, because no CSP has ever applied to them.)
- **`script-src 'self'`** — same rule for scripts. A theme's JavaScript is a `.js` file in
  `STATIC_DIR`, referenced with `{{asset "yours.js"}}`; an inline `<script>` is simply not
  run.

HSTS is sent only when `BASE_URL` is `https://`: browsers ignore it over plain HTTP, and
sending it from a development server would pin a rule making the next plain-HTTP project
on that port unreachable. There is deliberately no `preload` directive — that list has a
slow exit, and it is the operator's decision rather than this project's.

### Overselling

Stock is taken at payment, never reserved at checkout, so two shoppers can both reach a
payment page for the last item and both pay. The second order is still recorded **paid** —
the money was taken, and refusing to record it would lose the sale *and* still be oversold
— and `orders.oversold` is set in the same transaction, so an order is never
paid-but-unflagged.

It shows as `OVERSOLD` in `/admin/orders` and as a prominent block on the order page, as
well as in the owner's notification email. Before this phase it existed only in the logs
and that email, which is the wrong place for something needing reconciliation: an email is
read once and a log is not read at all.

Nothing here refunds anything. Refunds happen in the gateway's dashboard, because this
schema models a forward payment only — see the plan's note on when to reconsider
off-the-shelf.

### Cart cleanup

Carts untouched for `CART_TTL_DAYS` are deleted on boot and then daily, by a goroutine in
the server process. It runs in every instance rather than being elected to one, and that is
fine because the work is a single idempotent `DELETE`: two instances produce the same end
state as one, and the second finds nothing to do. Electing a leader would need coordination
this store has no other use for, and a cron container would break the one-binary
deployment story.

A failed sweep is logged and retried at the next tick. A cleanup that fails is a table that
grows a little longer, which is not worth waking anyone for.

## Catalog

A **product** is a catalog entry; a **variant** is what a customer actually buys, and
carries the price and the stock count. A product with no options — a single-edition book —
still has exactly one variant, with size and colour left blank. That is deliberate: cart,
order and stock code then never branches on "has options versus not".

Prices are **integer cents** everywhere in the code and the database. The decimal point
exists only in forms and rendered pages, because a float total rounded differently from a
payment gateway's amount string is a real and hard-to-find class of bug.

Manage the catalog at `/admin/products`.

### Categories

**A category is a row, not a string on the product.** Two tables: `categories`, and a
`product_categories` join.

| Column | Is |
|---|---|
| `slug` | The public parameter — `/products?category=books`. Unique |
| `name` | What a shopper reads |
| `position` | The display order. Sorting by `name` would put "Apparel" ahead of "Books" for ever, and a shop owner wants their own order |

**A product may be in several categories**, which is why there is a join table rather than a
column. A book that is also a gift belongs in both, and making a shop owner choose is a
decision the store has no business making for them. The cost is one extra query wherever
categories are read — paid in the admin, and deliberately not on the storefront cards, which
do not show them.

**Deleting a category unlinks its products; it never deletes them.** The cascade is on the
join table alone. This is the same stance as refusing to delete a product an order references:
a taxonomy edit must not be able to remove things people bought. The cost is that deleting an
unused category looks like it did nothing, so the admin says how many links it removed.

Manage them at `/admin/categories`.

### Product images

**A product image is always bytes this store holds.** There is no way to point a product at
a URL on somebody else's server: those bytes can change or vanish without warning, and a
product page with a broken picture is worse than one with none. The admin has no URL field,
and hand-crafting the parameter does nothing — `UpdateProduct` does not write either image
column.

Two backends, mutually exclusive, both behind `blob.Storage`:

| | Set | Image URL | Suits |
|---|---|---|---|
| **Object storage** | `BLOB_*` | the bucket's public hostname | anything scaled out; R2, GCS interop, MinIO |
| **A local directory** | `IMAGE_DIR` | `/images/...`, served by this server | one instance with a persistent volume |
| **Neither** | — | products have no images, and the admin says so | a catalog that does not need pictures |

`IMAGE_DIR` is the simpler shape: one binary, one directory, no object storage to run. Its
limitation is worth stating plainly because it is the thing that will bite — **two instances
do not share a directory.** Behind a load balancer, or on a platform that scales to zero and
restarts elsewhere, an image uploaded by one instance is a 404 from the other. Use a bucket
there.

With a bucket, **images are served straight from it and never proxied through the store**,
so the bucket must be publicly readable at `BLOB_PUBLIC_BASE_URL` and a CDN in front of it
does the work. With `IMAGE_DIR` the server serves them itself, from a same-origin path — which
is why `img-src` can be `'self'` with no external origin allowed at all.

**`products.image_key` is the only thing stored** — the URL is computed when a page is
rendered, by resolving the key against whichever backend is running. That is what makes the
same row work on a development machine serving from a directory and in production serving
from R2: **switching backends needs no data migration.** Storing the URL as well would bake
one deployment's answer into every row.

A product with no image gets a bundled placeholder rather than a gap, so a catalog of mixed
products keeps its shape while photographs are still being taken.

Two consequences worth knowing:

- **Uploads are validated on their sniffed magic bytes**, not the filename and not the
  browser's `Content-Type`, and the stored extension comes from the sniffed type too. A
  publicly readable bucket that will serve `evil.html` because somebody named their upload
  that is a cross-site scripting hole on a hostname you own — and the same is true of a
  directory this server serves. JPEG, PNG, GIF and WebP; 5 MB.
- **Replacing an image writes a new key**, so the new photograph is visible immediately.
  A stable key would need a CDN purge on every replacement — an operation this store has
  no credentials for and no way to verify — and until it happened the old picture would
  keep being served.

With a bucket, `BLOB_ENDPOINT` and `BLOB_PUBLIC_BASE_URL` are separate values because the
address a bucket is *written* through and the address it is *read* from are routinely
different:
R2 writes to `<account>.r2.cloudflarestorage.com` and reads from a custom domain, and in
the compose stack the server writes to `minio:9000` while your browser reads from
`localhost:9000`. Only the operator knows the second one, so it is not derived.

**Why minio-go and not the AWS SDK.** Since `aws-sdk-go-v2/service/s3` v1.73.0 every
`PutObject` carries a CRC32 checksum by default, which
[broke R2, GCS interop and older MinIO](https://github.com/aws/aws-sdk-go-v2/discussions/2960) —
the three stores this targets — while being correct for the one it is least likely to be
pointed at. minio-go speaks the conservative subset all of them agree on.

The upload order is chosen so no failure leaves a product pointing at nothing: store the
new object, point the product at it, and only then delete the old one. A failure part-way
leaves the previous image working, or at worst an orphaned object that costs a few
kilobytes and is logged.

**How images load** is the browser's own lazy loading and nothing else — no
`IntersectionObserver`, no JavaScript, no CSP directive involved. The catalog grid marks
everything after the first row `loading="lazy"`; the first four cards are left eager, because
lazy-loading an image that is already on screen defers exactly the picture that decides how
fast the page *feels* loaded. Four is a deliberate guess: the grid asks for as many columns as
fit, so the real count is a CSS decision the server cannot see, and four over-fetches slightly
on a phone and under-fetches on a wide monitor. A product page's own photograph is eager and
`fetchpriority="high"`, being the one image that page is about.

There are **no `width` and `height` attributes**, and that is not an oversight. The fixed-ratio
frame (`aspect-ratio: 4 / 5`) already reserves the space before any bytes arrive, so there is
no layout shift left to prevent — and the store never records a photograph's real dimensions,
so any numbers put there would be a guess about an image that is going to be cropped to the
frame anyway.

### Seeding

`cmd/seed` loads a plain products JSON file:

```json
[
  {
    "categories": ["books"],
    "slug": "the-quiet-machine",
    "title": "The Quiet Machine",
    "description": "Paperback, 248 pages.",
    "active": true,
    "variants": [
      { "sku": "BOOK-TQM-PB", "size": "", "color": "", "price_cents": 24900, "stock_qty": 12, "active": true }
    ]
  }
]
```

`slug` may be omitted and is then derived from the title. Seeding is rerunnable: products
match on `slug` and variants on `sku`, so a second run updates titles and prices rather
than duplicating rows — and it leaves `stock_qty` on rows that already exist alone, since a
fixture is a starting point and not the truth about inventory. Variants missing from the
file are not deleted.

`categories` is a list of category slugs, and any that do not exist yet are created, named by
title-casing the slug — `gift-cards` becomes "Gift Cards". That keeps a fixture
self-contained — seeding a fresh database needs no prior trip to the admin — at the cost of a
typo becoming a new category rather than an error. A category that already exists is left
exactly as it is, name and position included, so re-seeding never undoes an edit.
Give it a proper name in the admin afterwards — products stay linked through that rename,
because the link is by id. Changing the *slug* is the one to think about, since that is what
filter URLs carry.

**There is no `image_url` field**, and an unknown field is an error rather than being
ignored — so a file carrying one is rejected with the field named. A fixture cannot upload
bytes, so the only thing it could set is a URL to somebody else's server, which is exactly
what is no longer allowed. Re-seeding therefore never disturbs an uploaded image.

```sh
make seed                              # testdata/products.json
make seed SEED_FILE=my-catalog.json
```

`testdata/products.json` is generic sample data: fictional titles, no real contact details.

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

Replacing the whole thing is one file defining `index` in `TEMPLATE_DIR`. See
[Theming](#theming).

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
  fixed-schema rule under [Migrations](#migrations).
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

The hand-over to the gateway is a real cross-origin form post, not a redirect, which has two
consequences worth knowing before touching the CSP: the gateway's origin must be in
`form-action`, and the submit-on-load script is a **file** (`/static/redirect.js`) because
`script-src 'self'` forbids the inline script that would otherwise do it. Without JavaScript
the form's button is the whole mechanism, and it says so.

## Payments

PayFast is the only gateway, behind a small `payment.Gateway` interface so adding another is
code and no migration — the order's `gateway_*` columns are deliberately gateway-neutral.

### Setting it up

1. Get a merchant id and key from the [PayFast dashboard](https://sandbox.payfast.co.za) —
   the sandbox's have no relationship to a live account's.
2. Set a **salt passphrase** in the dashboard and put the same value in
   `PAYFAST_PASSPHRASE`. Set on one side only, every signature fails.
3. Leave `PAYFAST_SANDBOX=true` until a full sandbox payment has worked end to end.
4. Make sure PayFast's servers can reach the callback. `notify_url` is derived from
   `BASE_URL`, which on a laptop is `localhost` and unreachable from the internet — so local
   testing needs a tunnel, and `PAYFAST_NOTIFY_URL` is where its hostname goes.

Then, in order: place an order, pay it on the sandbox, and check that the order is `paid` and
stock has moved. Replaying the captured notification body with `curl` must not move stock a
second time.

### How a notification is authenticated

The customer's browser returning to `return_url` proves nothing. The **ITN** — PayFast's
form-encoded POST to `notify_url` — is the only statement about a payment this store trusts,
and it passes four independent checks before anything happens:

1. **The signature recomputes** over the fields exactly as received, in the order received.
2. **The source IP** is one of PayFast's published ranges.
3. **PayFast confirms it**, when the exact bytes received are posted back to
   `/eng/query/validate`.
4. **The merchant id** is ours.

None of them is sufficient alone. The signature can be produced by anyone holding the
passphrase, an IP can be spoofed or shared with whoever else is behind the same proxy, and the
server-to-server check proves the data is PayFast's but not that it was meant for this store.

Then the handler does what only it can: find the order, check the amount against the order's
own total, and stop a replay from decrementing stock twice.

**The callback always answers `200`.** A gateway retries anything else, and a notification
that fails validation is not "try again later" — it is forged or broken, and neither improves
on the third attempt. Rejections are logged in full, naming the check that failed, and
dropped. It is also outside the CSRF group by *not being in it* rather than by an exempt-path
string that has to keep matching the route.

### The signature, and why it is spelled out in code

Three details account for nearly every PayFast integration failure, and
[`internal/payment/payfast`](internal/payment/payfast) says so in its package comment:

- **The field order is the order they were submitted in, not alphabetical.** Sorting produces
  a signature PayFast rejects. This is why `payment.Field` is a slice and never a map anywhere
  near a signature.
- **`urlencode` is PHP's**, which every reference implementation uses. Go's `url.QueryEscape`
  is nearly the same and differs over `~`, and one character is a failed signature with no
  diagnostic beyond "mismatch".
- **Outgoing and incoming disagree about blank fields.** They are excluded when building the
  redirect form and *included* when verifying a notification, because that is what PayFast's
  own code does in each direction. Building the form sidesteps it by not submitting blanks at
  all.

`TestPayFast_SignatureMatchesKnownVector` pins both the parameter string and its digest.
**Put that string through [PayFast's signature tool](https://developers.payfast.co.za) before
taking real money** — no test suite can do that step, and the cost of skipping it is every
payment being rejected.

## Orders and email

Two pages, both read-only:

| Route | Shows |
|---|---|
| `GET /admin/orders` | Recent orders, newest first |
| `GET /admin/orders/{id}` | What to pack, where it goes, and what the gateway said |

**There are no buttons on either page, deliberately.** An order records something that
happened, and the only thing allowed to change one is an authenticated gateway notification.
A "mark as paid" button in the admin would be a way to record money that never arrived. A test
asserts that no route under `/admin/orders` accepts a `POST`, so adding one is a decision
somebody has to make on purpose.

The order page shows the **snapshot** — the title, options and unit price as they were when
the order was placed — so renaming, repricing or withdrawing a product afterwards does not
rewrite what somebody bought. It also shows the raw gateway notification, for the day a
customer and a bank disagree about what happened.

The list is capped at the 200 most recent. Products are a small fixed set; orders accumulate
forever, so "the catalog is small" does not carry over to this table.

### What gets sent, and when

When an authenticated notification says an order is paid, two emails go out — and both go out
**after** the order is recorded paid. That ordering is the whole point: a mail server having a
bad afternoon must never be able to lose a sale. Nothing in the mail path can fail the payment
callback.

- **The customer** gets a receipt, once. `orders.emailed` records it, so a replayed gateway
  notification does not send a second copy. Both a plain-text and an HTML part are sent; the
  plain-text one is not optional, because a receipt has to arrive readable in a client that
  refuses HTML.
- **`ORDER_NOTIFY_EMAIL`**, if set, gets a work order: what to pack, where it goes, and a link
  to the admin page. It also carries the **oversell warning**, which otherwise exists only in
  the logs — the person who has to tell a customer their item is gone should not have to find
  that in a log aggregator.

They are two separate sends rather than one message with two recipients: a receipt and a work
order say different things, and one of them failing should not suppress the other.

With no SMTP configured the server starts, warns loudly, and logs every message it drops.
That is deliberate — the shop's job is to take an order and record it, and that does not
depend on a mail server.

### Email templates

`email_order_paid.txt`, `email_order_paid.html` and `email_order_notify.txt`, overridable
from `TEMPLATE_DIR` like any other template. The `.txt` files go through **`text/template`**
and the rest through `html/template`, which is not a detail: running a receipt through the
HTML escaper puts `&amp;` in front of a customer.

The HTML part is deliberately primitive — table layout, inline styles, no external CSS and no
images. Mail clients are twenty years behind browsers, and a receipt that renders everywhere
beats one that looks better in three clients and breaks in the rest.

### Overselling

Two people can pay for the last item, because stock is only taken at payment. When a
decrement would go negative the order is still recorded paid — the money has been taken, and
refusing to record it would lose the sale *and* still be oversold — and the event is logged at
error level. Surfacing it in the admin's order view is part of the hardening phase.

### Money

Integer cents everywhere in Go and in the database; a decimal string only at the gateway
boundary and in rendered pages. A float total rounded differently from a gateway's amount
string is a real and hard-to-find class of bug, and the amount comparison in the callback is
exactly where it would bite.

### Theming

A theme is two directories: templates that override the embedded ones by name, and assets
that override the bundled ones by name. Nothing is forked, nothing is rebuilt, and anything
you do not override keeps coming from the binary.

| | Points at | Overrides |
|---|---|---|
| `TEMPLATE_DIR` | a directory of `*.html` and `*.txt` files | templates, by the name they `{{define}}` |
| `STATIC_DIR` | a directory of assets | bundled files, by filename |

`make up` and `make run` already set both, at [`theme/templates`](theme) and
[`theme/static`](theme), with **`THEME_RELOAD=true`** — so writing a theme is editing a file
and refreshing the page. Both directories are empty on a clean checkout, which is why the
store looks the same until you put something in one.

#### Writing one

Start from a default rather than from nothing — the defaults are meant to be read:

```sh
cp internal/handler/static/styles.css theme/static/styles.css     # restyle
cp internal/handler/templates/products.html theme/templates/      # re-mark-up the catalog
```

Then edit and refresh. Deleting your copy puts the default back, also without a restart.

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

**No web fonts in the default**, and adding one is not just a CSS edit: `style-src` is
`'self'` and fonts fall to `default-src 'self'`, so a font from another origin is blocked by
the CSP — silently, in the browser's console. Self-host the
`.woff2` in `STATIC_DIR` — that is one of the reasons new names are served — and reference
it from your stylesheet with a `/static/...` URL.

#### Overriding templates

Overriding is per *template name*, not per file or per page. A file defining
`{{define "products_list"}}` replaces the catalog listing everywhere it appears, including
inside the default full page and in the htmx fragment responses — which is what keeps a
themed store consistent between a full page load and a swap.

The names, and which file they live in:

| File | Defines | Is |
|---|---|---|
| `layout.html` | `head`, `foot` | The page chrome: `<head>`, header, footer. Override these two and every page follows |
| | `adminnav`, `csrf`, `err` | The admin nav, the hidden CSRF field, and one field's error message |
| `index.html` | `index` | The front page. Small on purpose — this is the one most shops replace outright |
| `not_found.html` | `not_found` | The 404 page, which every mistyped URL and withdrawn product lands on |
| `products.html` | `products`, `products_list`, `products_filters`, `products_pager` | The catalog page, the results inside it, the search and category form, and the page links |
| | `product_grid` | The card grid, **shared by the catalog and the index**. Override this to restyle a product card everywhere it appears — overriding `products_list` alone changes the catalog only |
| `product.html` | `product`, `product_detail`, `add_to_cart` | The product page, its body, and the variant/quantity form |
| `cart.html` | `cart`, `cart_items`, `cart_status` | The cart page, the lines htmx swaps, and the header count |
| `checkout.html` | `checkout`, `checkout_form`, `checkout_redirect`, `checkout_success`, `checkout_cancel` | The checkout, and the pages a shopper comes back to |
| `admin_*.html` | `admin_login`, `admin_products`, `admin_product_form`, `admin_categories`, `admin_category_form`, `admin_orders`, `admin_order`, `variant_errors`, `product_image` | The admin |
| `email_order_paid.{html,txt}`, `email_order_notify.txt` | same names, minus the extension for the HTML one | See [Email templates](#email-templates) |

Three things to know before writing one:

- **Class names are the contract** between the templates and the stylesheet. They describe
  what a thing is — `.product-card`, `.site-header`, `.price`, `.field` — so that overriding
  one side and not the other keeps working. Change the markup and keep the names, or change
  both together.
- **Every form needs `{{template "csrf" .CSRFToken}}`**, or it gets a `403`. See
  [CSRF](#csrf).
- **Templates get exactly the data the handler passes.** Every page embeds `.Title`,
  `.StoreName`, `.Currency` and `.CSRFToken`, plus its own — `.Products`, `.Product`,
  `.Cart`, `.Order`. The functions available are `money` (cents → a displayed amount),
  `asset` (a bundled or overridden file → its hashed URL), `image` (a product's image key →
  where it is served from) and `linebreaks`. A template naming a field that does not exist
  fails the render, which with reloading on is a `500` on that page and, without it, a
  refused boot.

#### Reloading, and not reloading

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

### Bundled assets

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
[`internal/handler/static/README.md`](internal/handler/static/README.md).

## Dependencies

The objection this project has is to **frameworks**, not libraries: something that owns the
shape of the application, dictates its architecture and ages on someone else's schedule
would defeat the point of a stdlib-shaped `net/http` and `html/template` design. A small,
single-purpose, widely-reviewed library that does one thing is a different proposition, and
is preferred over hand-rolling anything security-sensitive or fiddly. The standard mechanics
of a website — password hashing, CSRF tokens, session signing, migrations — are solved
problems, and a local reimplementation read by one person is not an improvement on one read
by thousands.

The counterweight is the Go idiom that a little copying beats a little dependency. Where a
package is a thin wrapper over something the stdlib already does, writing or copying those
few dozen lines avoids inheriting a release cadence and a transitive graph. The deciding
question is the depth of the problem, not the size of the dependency.

| Dependency | For |
|---|---|
| [`jackc/pgx/v5`](https://github.com/jackc/pgx) | Postgres driver and pool. No cgo, so the binary stays static |
| [`pressly/goose/v3`](https://github.com/pressly/goose) | Migrations: advisory locking, `NO TRANSACTION` support, and a CLI for the day one needs hand-holding |
| [`justinas/nosurf`](https://github.com/justinas/nosurf) | CSRF tokens and origin checks |
| [`gorilla/securecookie`](https://github.com/gorilla/securecookie) | Signing the admin session cookie, including key rotation |
| [`golang.org/x/crypto`](https://pkg.go.dev/golang.org/x/crypto) | `argon2` for admin password hashing, `bcrypt` to keep older hashes verifying |
| [`golang.org/x/time`](https://pkg.go.dev/golang.org/x/time/rate) | The token bucket behind the rate limits |
| [`wneessen/go-mail`](https://github.com/wneessen/go-mail) | Sending email: MIME, RFC 2047 subjects, quoted-printable, STARTTLS and implicit TLS |
| [`minio/minio-go/v7`](https://github.com/minio/minio-go) | Object storage over the S3 API — R2, GCS interop, MinIO |

Everything else is stdlib so far, by decision rather than by rule. Notably **not** taken:
a router (`ServeMux` does method and wildcard patterns), a validation library (struct tags
fight the per-field messages these forms need), and a decimal type (money is
integer cents). A UUID library is not needed *directly* — the database generates ids — and
`google/uuid` now arrives indirectly with minio-go, which is fine.

### Build-time tools

Separate from the table above, because nothing here links into the binary:

| Tool | For |
|---|---|
| [`sqlc`](https://sqlc.dev) | Generates the stores' row structs and scan code from the SQL |

sqlc is pinned in the **Makefile**, not as a `go tool` directive in `go.mod`. That is a
deliberate trade: `go tool` would pin the version alongside the code, but it also puts about
forty indirect modules — a MySQL driver, antlr, cel-go — into the file this README points at
as the project's dependency statement, and grows `go.sum` from 62 lines to 165. For something
that never reaches the binary, keeping `go.mod` a truthful description of what the server
depends on is worth more than the convenience. `make sqlc-install` installs the pinned
version; CI runs it before anything else.

### Decisions still open

Recorded here so they are decided deliberately rather than by default:

| Decision | Candidate | When |
|---|---|---|
| Server-side sessions | [`alexedwards/scs`](https://github.com/alexedwards/scs) | Only if a second admin or immediate revocation is ever needed — the same trigger as adding a `sessions` table |
| `AND` category filtering | a second parameter, or a toggle in the filter form | When a shop's categories overlap enough that widening the results is the wrong default |
| Accent-insensitive search | `unaccent`, behind an `IMMUTABLE` wrapper so it can be indexed | When a catalog carries accented titles and "cafe" failing to find "café" starts costing sales |
| Keyset pagination | a cursor on the ranking and title | When a catalog is deep enough that discarding rows to reach a late page is measurable |
| Tuned trigram thresholds | `AfterConnect` on the pgx pool | When the defaults visibly over- or under-match; they are session settings, so they belong on the connection, not in a query |

## Deploying

The binary is static and the image is distroless, so it runs anywhere a container does.
Four things are all that separate "runs on a managed platform" from "runs on a VM behind
a reverse proxy", and the server does all four: it reads `PORT` from the environment,
takes a single `DATABASE_URL`, logs JSON to stdout, and serves `GET /healthz`.

Migrations run on boot, before the server accepts traffic, guarded by a Postgres advisory
lock so several instances starting at once cannot race. Where you would rather migrate as
its own deploy step, run the same image with `-migrate` first and start the server after
it exits; `-migrate-status` prints what has been applied.

`-migrate` and `-migrate-status` read `DATABASE_URL` and nothing else. A schema change has
no payment gateway and no session, so a migration job — a CI step, an init container, a
release command — should not have to be trusted with the live merchant key and the session
secret in order to run an `ALTER TABLE`. Give that step the database URL alone.

The cost of that is real and worth knowing: a deployment whose payment or admin config is
broken no longer finds out when migrations run, but when the server starts, with the schema
already moved. `-check-config` is that check made deliberate — it validates the whole
environment, touches nothing, and exits. Run it *before* `-migrate` in a deploy and a
missing `PAYFAST_MERCHANT_KEY` fails while the database is still untouched:

```sh
gostore -check-config && gostore -migrate && exec gostore
```

Terraform for a Google Cloud deployment (Cloud Run, private-IP Cloud SQL, Secret
Manager, Artifact Registry) lives in [`infra/terraform`](infra/terraform/README.md).

## Development

```sh
make test          # TEST_DATABASE_URL defaults to the compose database
go test ./...      # database-backed tests skip when TEST_DATABASE_URL is unset
```

Database tests create a dedicated schema per test and drop it on cleanup, so they never
interfere with each other or with development data.

### The store layer

Queries live in `internal/db/queries/*.sql`; [sqlc](https://sqlc.dev) reads them **against the
migrations** and generates `internal/db/gen`. The stores in `internal/{catalog,cart,orders}`
call the generated methods and map rows onto the domain types.

```
internal/db/migrations/*.sql   the schema (goose runs these; sqlc reads them)
internal/db/queries/*.sql      the queries, annotated with -- name: X :one
internal/db/gen/               generated. Do not edit; `make sqlc` rewrites it
internal/{catalog,cart,orders} the stores: mapping, error translation, transactions
```

**Add or change a query:** edit the `.sql` file, run `make sqlc`, then use the new method.
CI runs `sqlc diff` and fails if the checked-in generated code is stale, so a query edited
without regenerating cannot reach `main` as code that quietly runs the old statement.

What this buys, and it is one specific thing: **a column can no longer land in the wrong
field.** The mapping is by name, checked at compile time, instead of a positional `Scan`
against a hand-maintained column list. `orders` has seven consecutive `TEXT` columns —
`customer_name`, `customer_email`, `customer_phone`, `shipping_address` and three
`gateway_*` — where a reordering used to compile, run, and file the phone number as the
address.

What it does not buy: the interesting parts are still hand-written, and deliberately so.
Transactions (`orders.MarkPaid`, `catalog.Upsert`) are orchestrated in Go, because the
control flow — lock, check, loop, decide — is the logic. Error translation is hand-written,
because turning `23505` into "that slug is taken, and here is the field to highlight" is
domain knowledge no generator has.

Three things worth knowing before touching `sqlc.yaml`:

- **`uuid` is overridden to `string`**, so ids are one type everywhere: in a URL path, in a
  form field, in a template. The cost is that a malformed id reaches Postgres and returns
  error `22P02`, which each store's `translate()` maps to `ErrNotFound`. That replaced a
  hand-written `isUUID` check.
- **`SELECT *` is deliberate** on single-table queries. sqlc expands it against the real
  schema at generation time, so the column list cannot drift from the table.
- **A cast can be load-bearing.** `(v.active AND p.active)::bool` looks redundant — neither
  column is nullable — but sqlc cannot prove that and would otherwise hand the store a
  `*bool`, implying a third state that does not exist.

### Migrations

The migrations are also sqlc's idea of the schema, so a migration and the generated code
change together: add a column, run `make sqlc`.

Numbered `.sql` files in `internal/db/migrations`, managed by
[goose](https://github.com/pressly/goose), embedded into the binary and applied in one
transaction each.

To add one, create `NNNN_name.sql` with a version above every existing file and a
`-- +goose Up` section:

```sql
-- +goose Up
ALTER TABLE products ADD COLUMN subtitle TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE products DROP COLUMN subtitle;
```

Four rules, each with teeth:

- **Never edit a migration that has been applied anywhere.** goose records versions, not
  checksums, so an edited file is silently skipped and the schema quietly diverges from
  the one in the repository. Add a new migration instead.
- **Number above every existing file.** Out-of-order migrations are rejected, because a
  file numbered below one already applied would produce a different schema depending on
  when you first ran it — unacceptable in a project other people run against their data.
- **Down sections are for local development.** Production is forward-only: rolling a
  schema back over live orders loses money, not just columns.
- **Create extensions in a named schema.** `CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA
  public`, never the bare form. An extension's *objects* are schema-scoped but its *name* is
  database-global, so the bare statement installs into whatever the `search_path` leads with
  and then does nothing on the next database whose path leads elsewhere — where the operators
  are simply missing. The database-backed tests are exactly that case: each one runs in its own
  schema, so the bare form lands in whichever test happened to run first and disappears with
  it. The symptom is `operator does not exist: text <% text` in tests that pass individually
  and fail as a suite.

Statements that cannot run inside a transaction — `CREATE INDEX CONCURRENTLY`, most
notably — need `-- +goose NO TRANSACTION` at the top of the file.

The first rule has been broken exactly once, deliberately: `0001_init.sql` was rewritten and
four follow-on migrations folded into it, before the project was published and while its only
database was a development one. If you are reading this in a released version, that window is
closed — the rule is absolute now, and a database from before the rewrite is recreated with
`make down ARGS=-v && make up && make seed`.

The files are ordinary goose migrations, so the `goose` CLI works against this directory
unchanged when a migration needs to be inspected or applied by hand.

## Build order

1. **Skeleton** — config, migration runner, compose, Dockerfile, `/healthz`, CI ← *done*
2. **Catalog** — products and variants, seed command, admin CRUD ← *done*
3. **Admin auth** — signed session cookie, `RequireAdmin`, `cmd/hashpw` ← *done*
4. **Storefront reads** — `/products` pages and fragments, vendored htmx, CORS ← *done*
5. **Cart** — cookie-keyed server-side cart, add/update/remove ← *done*
6. **Checkout + PayFast** — orders, signature, ITN validation ← *done*
7. **Order emails + admin orders** — go-mail, receipts, `/admin/orders` ← *done*
8. **Images** — `blob` package, admin upload to R2/GCS/MinIO ← *done*
9. **Hardening** — rate limits, argon2id, CSP review, oversell flagging, cart cleanup ← *done*
10. **Categories** — schema reset, `categories` + join table, `kind` retired, CRUD ← *done*
11. **Search and filtering** — full-text plus trigram, category filters, pagination, images ← *done*
12. Publish

## Licence

[MIT](LICENSE).

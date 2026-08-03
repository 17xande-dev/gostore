# gostore

A small, self-hostable online store written in Go: `html/template` fragments for an
htmx frontend, PostgreSQL for storage, and [PayFast](https://payfast.io) for payments.
Stdlib-first, with a deliberately tiny dependency surface.

> **Status: early.** The skeleton (config, migrations, container stack, health check), the
> catalog (products, variants, seed command, admin CRUD), admin authentication, the
> storefront, the cart, checkout against PayFast, order emails, the admin's order views and
> product image uploads all work. Hardening — rate limits, a CSP review, the cart cleanup
> job — is next; see the build order below.
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
are applied automatically on boot.

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
| `make sqlc` | Regenerate the stores' query code after editing SQL (`make sqlc-install` first) |

## Configuration

Everything comes from the environment; see [`.env.example`](.env.example) for the full
list with defaults.

| Var | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | **yes** | — | Postgres connection string |
| `ADMIN_PASSWORD_HASH` | **yes** | — | bcrypt hash of the admin password (`make hashpw`) |
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
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |
| `SHUTDOWN_TIMEOUT_SECONDS` | no | `15` | Grace period for in-flight requests |
| `SMTP_HOST` | no¹ | — | Mail relay. With no mail configured, receipts are logged and dropped |
| `EMAIL_FROM` | no¹ | — | Sender address |
| `SMTP_PORT` | no | `587` | `465` with `SMTP_TLS=tls`, `1025` for mailpit |
| `SMTP_TLS` | no | `starttls` | `starttls`, `tls` (implicit) or `none` (development only) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | no | — | Omit both for a relay that authenticates by address |
| `EMAIL_REPLY_TO` | no | — | When replies should not go to `EMAIL_FROM` |
| `ORDER_NOTIFY_EMAIL` | no | — | Sends a copy of each paid order to whoever packs it |
| `BLOB_ENDPOINT` | no² | — | Object storage host[:port], no scheme. Without it, images are pasted URLs |
| `BLOB_BUCKET` | no² | — | Bucket name |
| `BLOB_ACCESS_KEY_ID` / `BLOB_SECRET_ACCESS_KEY` | no² | — | Credentials |
| `BLOB_PUBLIC_BASE_URL` | no² | — | Where images are **read** from — not where they are written |
| `BLOB_REGION` | no | `auto` | What R2 wants; GCS and MinIO ignore it |
| `BLOB_USE_TLS` | no | `true` | `false` only for a MinIO on the same machine |

¹ `SMTP_HOST` and `EMAIL_FROM` are individually optional but must be set **together** — a
half-configured relay is a boot failure, because the alternative is a receipt that silently
never arrives.

² The `BLOB_*` set is all-or-nothing for the same reason: `BLOB_ENDPOINT` with any of the
others missing refuses to boot rather than failing at the first upload.

## Admin

The admin lives at `/admin`, behind one password. Generate the two values it needs:

```sh
make hashpw            # prompts, echoes nothing, prints both vars
```

Set them in the environment and restart. The plain password is never stored or
configured — only its bcrypt hash — so a leaked env file or a `ps` listing does not hand
over the credential.

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
- Login has no rate limit until the hardening phase. The bcrypt comparison makes each
  attempt cost real time, which is not the same thing.

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

## Catalog

A **product** is a catalog entry; a **variant** is what a customer actually buys, and
carries the price and the stock count. A product with no options — a single-edition book —
still has exactly one variant, with size and colour left blank. That is deliberate: cart,
order and stock code then never branches on "has options versus not".

Prices are **integer cents** everywhere in the code and the database. The decimal point
exists only in forms and rendered pages, because a float total rounded differently from a
payment gateway's amount string is a real and hard-to-find class of bug.

Manage the catalog at `/admin/products`. `image_url` is a pasted URL for now;
upload-to-object-storage arrives in a later phase.

### Product images

Two ways to give a product a picture, and the store treats them as genuinely
different things:

- **A pasted URL.** Works with no configuration at all, and is a complete answer for a
  shop whose photographs are already hosted somewhere. The store does not own those
  bytes and will never try to delete them.
- **An upload**, when the `BLOB_*` variables are set. The store owns the object and
  deletes it when the image is replaced or removed.

`products.image_key` is what separates the two: empty for a pasted URL, set for an
uploaded object. That is a column rather than a prefix-match on the URL because the first
pasted URL that happened to sit under the same prefix would otherwise make the store
delete somebody else's file. The admin form shows the pasted field *or* an upload plus a
remove button, never both, so the two states cannot be mixed into one confusing third.

**Images are served straight from the bucket, never proxied through the store.** That is
why the bucket has to be publicly readable at `BLOB_PUBLIC_BASE_URL`, and why the bytes
never touch Go on a read — a CDN in front of the bucket does the work.

Two consequences worth knowing:

- **Uploads are validated on their sniffed magic bytes**, not the filename and not the
  browser's `Content-Type`, and the stored extension comes from the sniffed type too. A
  publicly readable bucket that will serve `evil.html` because somebody named their upload
  that is a cross-site scripting hole on a hostname you own. JPEG, PNG, GIF and WebP;
  5 MB.
- **Replacing an image writes a new key**, so the new photograph is visible immediately.
  A stable key would need a CDN purge on every replacement — an operation this store has
  no credentials for and no way to verify — and until it happened the old picture would
  keep being served.

`BLOB_ENDPOINT` and `BLOB_PUBLIC_BASE_URL` are separate values because the address a
bucket is *written* through and the address it is *read* from are routinely different:
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

### Seeding

`cmd/seed` loads a plain products JSON file:

```json
[
  {
    "kind": "book",
    "slug": "the-quiet-machine",
    "title": "The Quiet Machine",
    "description": "Paperback, 248 pages.",
    "image_url": "",
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

```sh
make seed                              # testdata/products.json
make seed SEED_FILE=my-catalog.json
```

`testdata/products.json` is generic sample data: fictional titles, no real contact details.

## Storefront

| Route | Serves |
|---|---|
| `GET /products` | The catalog |
| `GET /products/{slug}` | One product, with its variants |

Both are read-only, and both answer twice over: a full page for an ordinary visit, and a
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

The default templates are plain, unstyled and meant to be replaced. Set `TEMPLATE_DIR` to
a directory containing `*.html` files; any file whose name matches an embedded template
replaces it, and the rest fall back to the defaults. Overrides are read at startup, so a
change needs a restart but never a rebuild.

Overriding is per *template*, not per page: a file defining `{{define "products_list"}}`
replaces the catalog listing wherever it appears, including inside the default full page.

### Static assets

htmx is vendored into the binary rather than loaded from a CDN, so the store works offline,
the CSP stays `script-src 'self'`, and no third-party origin sits near the payment path.
Asset URLs carry a hash of the file's contents (`/static/htmx.min.js?v=71ea67185bfa`) and are
served `immutable`, so upgrading the file invalidates every cached copy by itself. See
[`internal/handler/static/README.md`](internal/handler/static/README.md) for the provenance
of the vendored bytes and how to verify a replacement.

`redirect.js` is ours rather than vendored: it submits the payment hand-over form on load,
and it is a file for the same reason htmx is one — with no `'unsafe-inline'`, an inline script
or an `onload` attribute would simply be blocked, leaving the shopper on a page waiting for
something the browser refused to run.

Files in that directory are served from an **explicit list**, so leaving a note or a
half-finished experiment there does not publish it.

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
| [`golang.org/x/crypto/bcrypt`](https://pkg.go.dev/golang.org/x/crypto/bcrypt) | Admin password hashing |
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
| Password hashing algorithm | argon2id, via [`alexedwards/argon2id`](https://github.com/alexedwards/argon2id) or `x/crypto/argon2` | OWASP puts argon2id first and bcrypt second. bcrypt at cost 12 is the current choice because its hash string is self-describing; revisit if that stops being a good enough reason |
| Per-IP rate limiting | `golang.org/x/time/rate` for the buckets, hand-written keying and eviction | Phase 9. The limiter is the deep part; a map with a sweep is not |
| Server-side sessions | [`alexedwards/scs`](https://github.com/alexedwards/scs) | Only if a second admin or immediate revocation is ever needed — the same trigger as adding a `sessions` table |

## Deploying

The binary is static and the image is distroless, so it runs anywhere a container does.
Four things are all that separate "runs on a managed platform" from "runs on a VM behind
a reverse proxy", and the server does all four: it reads `PORT` from the environment,
takes a single `DATABASE_URL`, logs JSON to stdout, and serves `GET /healthz`.

Migrations run on boot, before the server accepts traffic, guarded by a Postgres advisory
lock so several instances starting at once cannot race. Where you would rather migrate as
its own deploy step, run the same image with `-migrate` first and start the server after
it exits; `-migrate-status` prints what has been applied.

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

Three rules, each with teeth:

- **Never edit a migration that has been applied anywhere.** goose records versions, not
  checksums, so an edited file is silently skipped and the schema quietly diverges from
  the one in the repository. Add a new migration instead.
- **Number above every existing file.** Out-of-order migrations are rejected, because a
  file numbered below one already applied would produce a different schema depending on
  when you first ran it — unacceptable in a project other people run against their data.
- **Down sections are for local development.** Production is forward-only: rolling a
  schema back over live orders loses money, not just columns.

Statements that cannot run inside a transaction — `CREATE INDEX CONCURRENTLY`, most
notably — need `-- +goose NO TRANSACTION` at the top of the file.

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
9. Hardening ← *next*
10. Publish

## Licence

[MIT](LICENSE).

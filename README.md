# gostore

A small, self-hostable online store written in Go: `html/template` fragments for an
htmx frontend, PostgreSQL for storage, and [PayFast](https://payfast.io) for payments.
Stdlib-first, with a deliberately tiny dependency surface.

> **Status: early.** The skeleton (config, migrations, container stack, health check),
> the catalog (products, variants, seed command, admin CRUD) and admin authentication
> work. Storefront pages, cart, checkout and the PayFast integration are being built in
> that order — see the build order below.
>
> The compose stack ships a **published development password** (`gostore`) so `make up`
> gives you a working admin. Replace it, and `SESSION_SECRET`, before anyone else can
> reach the deployment — see [Admin](#admin).

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

## Configuration

Everything comes from the environment; see [`.env.example`](.env.example) for the full
list with defaults.

| Var | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | **yes** | — | Postgres connection string |
| `ADMIN_PASSWORD_HASH` | **yes** | — | bcrypt hash of the admin password (`make hashpw`) |
| `SESSION_SECRET` | **yes** | — | 32+ random bytes, base64, signs the session cookie |
| `SESSION_TTL_HOURS` | no | `24` | How long a sign-in lasts |
| `PORT` | no | `8080` | Listen port |
| `BASE_URL` | no | `http://localhost:8080` | Public origin, for absolute URLs |
| `STORE_NAME` | no | `gostore` | Displayed store name |
| `CURRENCY` | no | `ZAR` | Currency code (PayFast requires `ZAR`) |
| `TEMPLATE_DIR` | no | — | Directory of templates that override the embedded defaults |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |
| `SHUTDOWN_TIMEOUT_SECONDS` | no | `15` | Grace period for in-flight requests |

PayFast and object-storage settings arrive with their phases.

## Admin

The admin lives at `/admin`, behind one password. Generate the two values it needs:

```sh
make hashpw            # prompts, echoes nothing, prints both vars
```

Set them in the environment and restart. The plain password is never stored or
configured — only its bcrypt hash — so a leaked env file or a `ps` listing does not hand
over the credential.

**One operator, no sessions table.** A session is a signed cookie: base64 of its expiry,
plus an HMAC-SHA256 of that expiry under `SESSION_SECRET`. The expiry is inside the signed
value, not just in the cookie's metadata, so a client cannot extend it. Verifying costs no
database round trip and expired sessions need no cleanup job.

The trade is revocation: **the only way to sign every session out is to change
`SESSION_SECRET`** and restart. Wanting to revoke one session, or wanting a second admin
with different permissions, is the documented point at which this should become a
`sessions` table with a real user model — not something to bolt onto the cookie.

Notes:

- The cookie is `HttpOnly`, `SameSite=Lax`, scoped to `/admin`, and `Secure` whenever
  `BASE_URL` is `https://` — so it is never sent with the cookie-free embeddable catalog
  fragments.
- htmx requests that have lost their session get `401` and `HX-Refresh: true` instead of a
  redirect, because swapping a login page into a fragment produces a broken hybrid.
- **CSRF protection is not wired yet** (`nosurf` arrives with the checkout phase), and
  login has no rate limit until the hardening phase. Both are tracked; neither is done.

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

### Theming

The default templates are plain, unstyled and meant to be replaced. Set `TEMPLATE_DIR` to
a directory containing `*.html` files; any file whose name matches an embedded template
replaces it, and the rest fall back to the defaults. Overrides are read at startup, so a
change needs a restart but never a rebuild.

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

### Migrations

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
4. Storefront reads (`/products` fragments) and htmx ← *next*
5. Cart
6. Checkout + PayFast
7. Order emails + admin orders
8. Images (object storage)
9. Hardening
10. Publish

## Licence

[MIT](LICENSE).

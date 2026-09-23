# Developing gostore

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
| [`golang.org/x/crypto`](https://pkg.go.dev/golang.org/x/crypto) | `argon2` for admin password hashing, `bcrypt` to keep older hashes verifying |
| [`golang.org/x/time`](https://pkg.go.dev/golang.org/x/time/rate) | The token bucket behind the rate limits |
| [`17xande-dev/mailer`](https://github.com/17xande-dev/mailer) | Sending email, behind one `Sender` interface — the SMTP transport, the XOAUTH2 token dance for Exchange, and the fake the handler tests inject. Shared with another application, which is why it is a module rather than an `internal/` package. It pulls in [`wneessen/go-mail`](https://github.com/wneessen/go-mail) for MIME, RFC 2047 subjects, quoted-printable, STARTTLS and implicit TLS |
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
| Server-side sessions | [`alexedwards/scs`](https://github.com/alexedwards/scs) | **Closed: not taken.** The trigger arrived and the answer was a hand-rolled `admin_sessions` table. scs keys an opaque blob by token with no user column, so "end every session belonging to this account" — the one operation that reopened the question — cannot be expressed against its schema without querying around the abstraction. Its other features (flash data, idle-vs-absolute timeouts) go unused here, and dropping the signed cookie removed a dependency rather than adding one |
| `AND` category filtering | a second parameter, or a toggle in the filter form | When a shop's categories overlap enough that widening the results is the wrong default |
| Accent-insensitive search | `unaccent`, behind an `IMMUTABLE` wrapper so it can be indexed | When a catalog carries accented titles and "cafe" failing to find "café" starts costing sales |
| Keyset pagination | a cursor on the ranking and title | When a catalog is deep enough that discarding rows to reach a late page is measurable |
| Tuned trigram thresholds | `AfterConnect` on the pgx pool | When the defaults visibly over- or under-match; they are session settings, so they belong on the connection, not in a query |
| Local object storage (dev and the tunnel deployment) | [versitygw](https://github.com/versity/versitygw) as the server, [rclone](https://rclone.org) for backups, `aws-cli` for bucket setup | **Decided, not yet done** (2026-09-23). MinIO's community edition was archived in February 2026, its images stopped receiving patches in October 2025, and `minio/minio` and `minio/mc` were deleted from Docker Hub on 2026-09-11. Until the migration, `compose.yaml` and `deploy/tunnel` pull the last builds from `quay.io/minio/*` — frozen, unpatched, and at risk of disappearing the same way. versitygw was chosen for Apache-2.0 licensing, several maintainers, publishing to both Docker Hub and GHCR, and serving public reads through a bucket policy at the same path-style URLs, so `BLOB_PUBLIC_BASE_URL` keeps its shape. `deploy/standard` is unaffected: it uses R2, and the `minio-go` client library is still maintained |

## Build order

1. **Skeleton** — config, migration runner, compose, Dockerfile, `/healthz`, CI ← *done*
2. **Catalog** — products and variants, seed command, admin CRUD ← *done*
3. **Admin auth** — `RequireAdmin`, `cmd/hashpw`; the signed cookie it started with was replaced in 11.6 ← *done*
4. **Storefront reads** — `/products` pages and fragments, vendored htmx, CORS ← *done*
5. **Cart** — cookie-keyed server-side cart, add/update/remove ← *done*
6. **Checkout + PayFast** — orders, signature, ITN validation ← *done*
7. **Order emails + admin orders** — go-mail, receipts, `/admin/orders` ← *done*
8. **Images** — `blob` package, admin upload to R2/GCS/MinIO ← *done*
9. **Hardening** — rate limits, argon2id, CSP review, oversell flagging, cart cleanup ← *done*
10. **Categories** — schema reset, `categories` + join table, `kind` retired, CRUD ← *done*
11. **Search and filtering** — full-text plus trigram, category filters, pagination, images ← *done*
11.5. **Administrator accounts** — `admin_users`, roles, the last-owner guards ← *done*
11.6. **Sessions and setup** — `admin_sessions`, the `/admin/setup` claim, no credential in the environment ← *done*
11.7. **Authorization** — a permission named on every route registration, `must_change_password` ← *done*
11.8. **Account management** — `/admin/users`, own-password change ← *done*
11.9. **SnapScan** — a second gateway, a registry, a QR hand-over; the `payment.Gateway` interface widened to admit a provider that is not PayFast-shaped ← *done*
12. Publish

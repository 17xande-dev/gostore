# gostore

A small, self-hostable online store written in Go: `html/template` fragments for an
htmx frontend, PostgreSQL for storage, and [PayFast](https://payfast.io) for payments.
Stdlib-first, with a deliberately tiny dependency surface.

> **Status: early.** The skeleton (config, migrations, container stack, health check), the
> catalog (products, variants, seed command, admin CRUD), administrator accounts with roles,
> the storefront, the cart, checkout against PayFast, order emails, the admin's order views,
> product image uploads, the hardening pass, categories, and catalog search with filtering
> and pagination all work. What remains is the publishing checklist — see the
> [build order](docs/development.md#build-order).
>
> There is no default admin password. `make up` prints a one-time setup token, and
> `/admin/setup` exchanges it for the first administrator account — see
> [Admin](docs/admin.md#admin). The compose stack does ship PayFast's **published sandbox
> credentials**, so the checkout works out of the box; replace them before anyone else
> can reach the deployment — see [Payments](docs/payments.md#payments).

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
docker compose logs server | grep setup_token   # the one-time token for the first admin
open http://localhost:8080/admin      # redirects to /admin/setup; paste the token there
```

`make up` starts Postgres, [mailpit](http://localhost:8025) (captures outgoing email),
and the server. Images and purchased files live in `.local/images` and
`.local/downloads`, shared with host-side `make run` and `make seed`. Migrations
are applied automatically on boot. It also mounts [`theme/`](theme) into the server with
reloading on, so a stylesheet or template dropped in there takes effect on the next page
refresh — see [Theming](docs/theming.md#theming).

### Run the Go dev server on your host

Use this instead of `make up` when you want `go run` on the host (for example, for a
debugger). Docker Compose still provides Postgres and Mailpit; no `.env` or manual
database setup is needed on a clean checkout:

```sh
make run                         # starts Postgres + Mailpit, migrates, serves :8080
make seed                        # optional: load the demo catalog
```

Wait for the migration messages and `http://localhost:8080/healthz` to respond. On a
fresh database, copy the `setup_token` printed by the server, open
`http://localhost:8080/admin`, and use it to create the first administrator. Mail sent
locally is captured at [http://localhost:8025](http://localhost:8025). Stop the server
with Ctrl-C; `make down` stops the Compose services. Use `make down ARGS=-v` only when
you want to delete the local database and start over.

If startup reports a database connection or authentication error, check whether another
Postgres is occupying port 5432 and inspect `docker compose logs postgres`. To discard a
broken local database and recreate it, run `make down ARGS=-v` followed by `make run`;
this deletes all local database data.

Production templates use **Cloudflare R2**: a public image bucket and a separate
private download bucket. They also require `EMAIL_QUEUE_KEY` (`openssl rand -hex 32`)
to encrypt pending email jobs. Keep this key with your backups. The development
stack supplies a public development-only key.

Other useful targets:

| Target | Does |
|---|---|
| `make down` | Stop the stack (`make down ARGS=-v` also deletes the data volumes) |
| `make run` | Run the server on the host against the compose Postgres |
| `make seed` | Load a products JSON file (`SEED_FILE=...`, default `testdata/products.json`) |
| `make test` | Run every test, including the database-backed ones |
| `make hashpw` | Prompt for a password and print an argon2id hash — a lockout-recovery path, not part of setup |
| `make psql` | Open a `psql` shell on the compose database |
| `make logs` | Follow the server logs |
| `make migrate` | Apply pending migrations without starting the server |
| `make migrate-status` | Show which migrations have been applied |
| `make check-config` | Validate the full server configuration and exit |
| `make sqlc` | Regenerate the stores' query code after editing SQL (`make sqlc-install` first) |

## Documentation

| | |
|---|---|
| [Configuration](docs/configuration.md) | Every environment variable, `KEY_FILE` for secrets, and where configuration and secrets live |
| [Admin and accounts](docs/admin.md) | The first administrator, roles, managing accounts, API tokens |
| [MCP](docs/mcp.md) | Letting an AI assistant manage the store: connecting a client, the tools, the security model |
| [Catalog](docs/catalog.md) | Products, variant options, categories, images, digital downloads, seeding |
| [Storefront, cart and checkout](docs/storefront.md) | The index page, search and filtering, embedding the catalog, the cart, checkout |
| [Payments](docs/payments.md) | PayFast and SnapScan: setting up, going live, how notifications are authenticated |
| [Orders and email](docs/email.md) | What gets sent and when, Microsoft Exchange Online, email templates, money |
| [Theming](docs/theming.md) | Writing a theme, web fonts, overriding templates, bundled assets |
| [Security](docs/security.md) | CSRF, rate limits, password hashing, response headers, overselling |
| [Operations](docs/operations.md) | When something goes wrong, and logging |
| [Deploying](docs/deploy/README.md) | Two ready-made Compose deployments, backups, and running it anywhere else |
| [Developing gostore](docs/development.md) | The store layer, migrations, dependencies, and the build order |
| [Deprecations](docs/deprecations.md) | What is kept only for compatibility, to remove at the next major version |

## Licence

[MIT](LICENSE).

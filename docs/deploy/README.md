# Deploying

gostore runs as a small Docker Compose stack on one server. Pick one of two ready-made
deployments; each is a directory in [`deploy/`](../../deploy) that you copy to the
server, fill in one `.env`, and start with `docker compose up -d`.

| | [Standard](standard.md) | [Tunnel](tunnel.md) |
|---|---|---|
| **Directory** | [`deploy/standard`](../../deploy/standard) | [`deploy/tunnel`](../../deploy/tunnel) |
| **How requests get in** | Caddy, with a Let's Encrypt certificate | A Cloudflare Tunnel — no open ports at all |
| **Product images** | Cloudflare R2 | MinIO, on the same server |
| **Mail** | Any SMTP provider | Microsoft 365, through Microsoft Graph |
| **Pick it when** | You have a VPS with a public address. The default | The server has no public address — a home lab, an office — or you would rather open no ports; your domain is on Cloudflare; your mail is Microsoft 365 |

Both run Postgres beside the store, and both take a nightly database backup to the
server's own disk with the `backup.sh` in their directory. That covers a bad migration or
a mistaken delete, not losing the server — [Backups](backups.md) copies them off it and
covers restoring.

## The `.env`

Everything a deployment needs, credentials included, goes in the `.env` beside its
`compose.yaml`, made readable by you alone:

```sh
cp .env.example .env && chmod 600 .env
```

Each directory's `.env.example` holds only what that deployment needs, and
`compose.yaml` derives what it can — `BASE_URL` from `DOMAIN`, `DATABASE_URL` from
`POSTGRES_PASSWORD` — so values that must agree cannot disagree. Every other setting the
server takes is in [Configuration](../configuration.md) and can be added to the same
file. Where a deployment wants credentials kept out of the environment altogether, each
one can arrive as a file instead; see
[Where configuration and secrets live](../configuration.md#where-configuration-and-secrets-live).

## Publishing an image

The image is built on a workstation and pushed to GHCR by hand — `make publish`,
usually from a tagged release (`git tag v1.2.3 && git push --tags`). There is no
CI workflow doing this on push; see the Makefile's `publish` target for what it
refuses to do (a dirty tree, an untagged commit) and why.

GHCR makes a newly pushed package private. Set it public in the package's settings
after the first publish, or the server cannot pull it without a token.

## Running it anywhere else

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

Behind a proxy, set `CLIENT_IP_SOURCE` to match it — the PayFast callback's source check
and the rate limits both depend on the client address being real. See
[Configuration](../configuration.md).

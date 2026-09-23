# Deploying

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

Terraform lives in [`infra/terraform`](../../infra/terraform/README.md): a Vultr instance
plus Docker Compose for production, and a Proxmox VM for staging, both built on a
shared cloud-init module. An apply provisions the box but leaves it idle: the
store's credentials live in `pass` and never go through Terraform, and
`make secrets ENV=staging` (or `prod`) writes them to the box over SSH and starts
the stack — see [Where configuration and secrets live](../configuration.md#where-configuration-and-secrets-live).

The image is built on a workstation and pushed to GHCR by hand — `make publish`,
usually from a tagged release (`git tag v1.2.3 && git push --tags`). There is no
CI workflow doing this on push; see the Makefile's `publish` target for what it
refuses to do (a dirty tree, an untagged commit) and why.

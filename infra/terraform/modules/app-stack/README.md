# app-stack module

Shared by [`../../vultr`](../../vultr/README.md) (production) and
[`../../proxmox`](../../proxmox/README.md) (staging). Not a root module —
it has no provider block and creates no resources; it's `templatefile()`
three times over, producing the cloud-init user-data both callers hand
their instance/VM resource.

None of it contains a secret. See "Secrets" below for how they arrive.

## What it renders

- **`.env`** ([`templates/env.tftpl`](templates/env.tftpl)) — the server's
  configuration, following `.env.example` at the repo root: every setting
  except the credentials, including the identifiers that sit beside them (a
  merchant id, an access key id).
- **`docker-compose.yml`** ([`templates/docker-compose.yml.tftpl`](templates/docker-compose.yml.tftpl))
  — three services always (`postgres`, `server`, `caddy`), plus `minio` and
  `minio-init` when `image_backend = "minio"`. `server` publishes no host
  port; `caddy` is the only container reachable from outside the VM, on 80
  and 443. Its top-level `secrets:` block declares one Compose secret per
  file in `/opt/gostore/secrets`, and each service lists only the ones it
  needs.
- **`Caddyfile`** ([`templates/Caddyfile.tftpl`](templates/Caddyfile.tftpl))
  — one site (`domain` -> `server:8080`), plus a second (`images_domain` ->
  `minio:9000`) when `image_backend = "minio"`. Written only when
  `ingress = "caddy"`; a tunnel deployment has no Caddy to configure.
- **`backup.sh`** ([`templates/backup.sh.tftpl`](templates/backup.sh.tftpl)),
  **`gostore-backup.service`** and **`gostore-backup.timer`** — a nightly
  `pg_dump`, gzipped and pushed to object storage via `mc`, on a systemd
  timer rather than a container: it's one command on a schedule, and a timer
  already exists on the box for free. See "Database backups" below for what
  this does and doesn't cover.
- **`cloud-init.yaml`** ([`templates/cloud-init.yaml.tftpl`](templates/cloud-init.yaml.tftpl))
  wraps everything above (base64-encoded, written to `/opt/gostore/` and
  `/etc/systemd/system/`, `.env` and `backup.sh` at `0600`/`0700`) and, in
  `runcmd`: installs Docker from its official apt repository, formats
  `data_device` as ext4 if it isn't already, mounts it at
  `/mnt/gostore-data`, creates the empty, root-only `/opt/gostore/secrets`,
  and enables the backup timer. It does **not** start the stack — that waits
  for `make secrets`; see "Secrets". Nothing else is installed on the host —
  `mc` runs in a container; see "Database backups".

## Database backups

`backup_endpoint`/`backup_bucket`/`backup_access_key_id`/`backup_secret_access_key`
work exactly like the `blob_*` set: `image_backend = "r2"` forwards real
Cloudflare credentials for a second bucket, `image_backend = "minio"` reuses
the self-hosted MinIO the module already runs, in a bucket of its own that
`minio-init` creates private. `pg_dump` runs inside the `postgres` container
via `docker compose exec`, over its own Unix socket — the official image
trusts local connections by default, so `backup.sh` never needs
`postgres_password` at all.

A dump is only uploaded once it is known to be whole. The script runs under
`set -o pipefail`, so a failed `pg_dump` fails the run rather than letting
gzip's success stand in for it; and it checks the dump ends with pg_dump's
own `-- PostgreSQL database dump complete` marker before uploading anything.
Both checks sit above the prune, so a bad night leaves every older backup
where it was instead of uploading nothing and letting retention erode the
real ones.

`mc` runs in a container on the stack's compose network rather than on the
host, because that is the only place staging's `minio` hostname resolves;
R2 is reached the same way, over the container's ordinary internet egress.
Retention is `mc rm --older-than backup_retention_days` after each verified
upload, not a separate prune job: no local state tracks what's already been
deleted, so a changed retention value takes effect immediately, backdated
over whatever is already in the bucket.

**This is snapshots, not point-in-time recovery.** A schedule of
`backup_schedule` (daily by default) means losing up to a day of orders in
the worst case, not losing nothing. Restoring is the reverse of the backup,
as root in `/opt/gostore` on the box. The secret is read from its file the
way `backup.sh` reads it, so it never lands in shell history — use
`minio_root_password` and `http://` on staging, `backup_secret_access_key`
and `https://` on prod:

```sh
export MC_HOST_backup="https://<key-id>:$(cat secrets/backup_secret_access_key)@<endpoint>"
docker run --rm --network gostore_default -e MC_HOST_backup \
  quay.io/minio/mc:latest cat backup/gostore-backups/<file>.sql.gz \
  | gunzip | docker compose exec -T postgres psql -U gostore gostore
```

## Secrets

No credential is an input to this module, and none appears in anything it
renders — not the `.env`, not the Compose file, not the cloud-init payload a
provider keeps. What the module decides is *which* secrets a stack needs,
from the features switched on: one mail secret by `mail_transport` —
`smtp_password` for a relay with a login, `smtp_oauth_client_secret` for
XOAUTH2, `graph_client_secret` for Graph — the SnapScan keys only when
`snapscan_snap_code` is set, `tunnel_token` only with a tunnel, and so on. That one list feeds both the Compose file's
`secrets:` block and the `secret_names` output, so what gets pushed and what
Compose expects cannot drift apart.

The values come from `pass`, written onto the box by `make secrets`
([`infra/push-secrets.sh`](../../../push-secrets.sh)) as one file each in
`/opt/gostore/secrets`: the directory root-only, each file `0400` and owned
by uid 65532. That uid is distroless's nonroot, which the server and
`cloudflared` run as; Postgres, MinIO and the `mc` job start as root and read
the files regardless. Each reaches its container as a Compose secret,
bind-mounted read-only at `/run/secrets/<name>`, and the server reads it as
`KEY_FILE` (see `secretKeys` in `internal/config`). So a credential is in
neither the container's configuration nor its environment, and
`docker inspect` shows only the paths.

Two consequences worth knowing:

- **Cloud-init no longer starts the stack.** Compose refuses a secret whose
  file does not exist, and the files only arrive with the first
  `make secrets`, which starts it.
- **Rotation recreates the containers.** A container holds the file it
  started with, and the server reads its secrets once, at boot, so
  `make secrets` ends with `docker compose up -d --force-recreate`.
  `postgres_password` is the exception that needs more: Postgres applies it
  only when the data directory is first created, so rotating it means
  `ALTER USER` first.

## Ingress: `caddy` or `tunnel`

`caddy` (the default) is the deployment that needs nothing from Cloudflare:
Caddy terminates TLS with Let's Encrypt, publishes 80 and 443, and reverse-
proxies to `server:8080`. It is the default precisely because gostore is
published for other people to run, and requiring a Cloudflare account to
deploy at all would be a strange thing for a store to insist on.

`tunnel` swaps Caddy for a `cloudflared` container that dials out. What
disappears is the interesting part: no published ports, no certificate, no
ACME, no inbound firewall rule, and nothing for a port-forward to expose on a
private network. Routing lives in the tunnel's ingress rules, which belong to
[`../cloudflare-tunnel`](../cloudflare-tunnel), not to a file on the box. The
connector token is generated by Cloudflare and read back through a data
source, so no human ever pastes it — and it reaches the box like every other
secret, via `make secrets`, which reads it from the caller's `tunnel_token`
output. This module only mounts it.

The choice also sets `CLIENT_IP_SOURCE`, because it decides which header in
front of the app was written by something that cannot be lied to:

- `caddy` -> `forwarded`. Caddy discards an incoming `X-Forwarded-For` unless
  `trusted_proxies` is configured, so the leftmost entry is the true peer.
- `tunnel` -> `cloudflare`. `CF-Connecting-IP` holds exactly one address, and
  the tunnel being the only way in is what makes it trustworthy. Reading
  `X-Forwarded-For` here would be wrong rather than merely conservative:
  Cloudflare *appends* to it, so its leftmost entry is client-supplied.

Note `acme_email` is unused under `tunnel` — Cloudflare terminates TLS at its
edge, so the box never asks anyone for a certificate.

## The two things each caller decides for itself

`image_backend`: `"r2"` in production, where real Cloudflare credentials get
forwarded through as `blob_*`; `"minio"` in staging, where this module
provisions the container itself and generates nothing for the caller to
supply. And `ingress`, above. Everything else — mail, PayFast/SnapScan, the
store name, the backup timer — is identical in shape between callers; see each
root module's `variables.tf` for the values that actually differ.

## Why this is a module and not copy-pasted into both root configs

The two callers' compute is genuinely different — a `vultr_instance` and a
`proxmox_virtual_environment_vm` share no schema — so root modules for each
provider, not one root module with a `count` or a provider alias, are the
right split. But everything past "here is a Linux box," the entire app
stack, is meant to stay identical between production and staging apart from
where images live. A module is what keeps that guarantee: a change to how
Caddy is configured, or which Postgres image tag runs, is one edit here
instead of two edits that can quietly drift apart.

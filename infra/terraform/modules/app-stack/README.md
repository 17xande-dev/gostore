# app-stack module

Shared by [`../../vultr`](../../vultr/README.md) (production) and
[`../../proxmox`](../../proxmox/README.md) (staging). Not a root module —
it has no provider block and creates no resources; it's `templatefile()`
three times over, producing the cloud-init user-data both callers hand
their instance/VM resource.

## What it renders

- **`.env`** ([`templates/env.tftpl`](templates/env.tftpl)) — every
  environment variable the server reads, following `.env.example` at the
  repo root. `DATABASE_URL` always points at the compose network's own
  `postgres` service; that's not configurable by either caller, on the same
  grounds the dev `compose.yaml` hardcodes it.
- **`docker-compose.yml`** ([`templates/docker-compose.yml.tftpl`](templates/docker-compose.yml.tftpl))
  — three services always (`postgres`, `server`, `caddy`), plus `minio` and
  `minio-init` when `image_backend = "minio"`. `server` publishes no host
  port; `caddy` is the only container reachable from outside the VM, on 80
  and 443.
- **`Caddyfile`** ([`templates/Caddyfile.tftpl`](templates/Caddyfile.tftpl))
  — one site (`domain` -> `server:8080`), plus a second (`images_domain` ->
  `minio:9000`) when `image_backend = "minio"`.
- **`backup.sh`** ([`templates/backup.sh.tftpl`](templates/backup.sh.tftpl)),
  **`gostore-backup.service`** and **`gostore-backup.timer`** — a nightly
  `pg_dump`, gzipped and pushed to object storage via `mc`, on a systemd
  timer rather than a container: it's one command on a schedule, and a timer
  already exists on the box for free. See "Database backups" below for what
  this does and doesn't cover.
- **`cloud-init.yaml`** ([`templates/cloud-init.yaml.tftpl`](templates/cloud-init.yaml.tftpl))
  wraps everything above (base64-encoded, written to `/opt/gostore/` and
  `/etc/systemd/system/`, `.env` and `backup.sh` at `0600`/`0700`) and, in
  `runcmd`: installs Docker from its official apt repository, installs `mc`
  as a checksum-verified static binary, formats `data_device` as ext4 if it
  isn't already, mounts it at `/mnt/gostore-data`, runs `docker compose up
  -d`, and enables the backup timer.

## Database backups

`backup_endpoint`/`backup_bucket`/`backup_access_key_id`/`backup_secret_access_key`
work exactly like the `blob_*` set: `image_backend = "r2"` forwards real
Cloudflare credentials for a second bucket, `image_backend = "minio"` reuses
the self-hosted MinIO the module already runs, in a bucket of its own that
`minio-init` creates private. `pg_dump` runs inside the `postgres` container
via `docker compose exec`, over its own Unix socket — the official image
trusts local connections by default, so `backup.sh` never needs
`postgres_password` at all. Retention is `mc rm --older-than
backup_retention_days` on every run, not a separate prune job: no local
state tracks what's already been deleted, so a changed retention value takes
effect immediately, backdated over whatever is already in the bucket.

**This is snapshots, not point-in-time recovery.** A schedule of
`backup_schedule` (daily by default) means losing up to a day of orders in
the worst case, not losing nothing. Restoring is the reverse of the backup:

```sh
mc cat gostore-backup/gostore-backups/<file>.sql.gz | gunzip | \
  docker compose exec -T postgres psql -U gostore gostore
```

## The one thing each caller decides for itself

`image_backend`: `"r2"` in production, where real Cloudflare credentials get
forwarded through as `blob_*`; `"minio"` in staging, where this module
provisions the container itself and generates nothing for the caller to
supply. Everything else — mail, PayFast/SnapScan, the store name, TLS via
Caddy — is identical in shape between the two; see each root module's
`variables.tf` for the values that actually differ.

## Why this is a module and not copy-pasted into both root configs

The two callers' compute is genuinely different — a `vultr_instance` and a
`proxmox_virtual_environment_vm` share no schema — so root modules for each
provider, not one root module with a `count` or a provider alias, are the
right split. But everything past "here is a Linux box," the entire app
stack, is meant to stay identical between production and staging apart from
where images live. A module is what keeps that guarantee: a change to how
Caddy is configured, or which Postgres image tag runs, is one edit here
instead of two edits that can quietly drift apart.

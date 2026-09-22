# Infrastructure

Two independent root modules, one per environment and cloud:

- **[`vultr/`](vultr/README.md)** — production. One Ubuntu instance running
  the app, Postgres, and Caddy under Docker Compose, plus a Block Storage
  volume for Postgres's data. Product images live in Cloudflare R2.
- **[`proxmox/`](proxmox/README.md)** — staging, on a Proxmox VE node. Same
  shape, minus the Cloudflare account: images live in a self-hosted MinIO
  container instead.
- **[`modules/app-stack/`](modules/app-stack/README.md)** — shared by both.
  Provider-agnostic: it renders the `docker-compose.yml`, `.env`, `Caddyfile`,
  and a nightly `pg_dump`-to-object-storage backup timer that ship as
  cloud-init, and creates no resources itself.

This replaced a Google Cloud config (Cloud Run, Cloud SQL, Secret Manager,
Artifact Registry) — see either root module's README for what a plain VM
plus Docker Compose does and doesn't give you compared to that.

Each root module has its own state and its own `terraform.tfvars`; there is
nothing to `init` at this level; `cd` into `vultr/` or `proxmox/` first.

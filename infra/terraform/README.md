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
  cloud-init, and creates no resources itself. Its `ingress` variable picks
  between Caddy terminating TLS on the box and a Cloudflare Tunnel dialling
  out, which also decides where the app reads the client IP from.
- **[`modules/cloudflare-tunnel/`](modules/cloudflare-tunnel/README.md)** —
  the tunnel, its ingress rules and its DNS records, for a root module that
  sets `ingress = "tunnel"`. Currently wired into `proxmox/` only.

This replaced a Google Cloud config (Cloud Run, Cloud SQL, Secret Manager,
Artifact Registry) — see either root module's README for what a plain VM
plus Docker Compose does and doesn't give you compared to that.

Each root module has its own state and its own `terraform.tfvars`; there is
nothing to `init` at this level; `cd` into `vultr/` or `proxmox/` first.

**No store secret goes through Terraform.** They live in `pass` on the
operator's machine as `gostore/<env>/<name>`, and
[`../push-secrets.sh`](../push-secrets.sh) — `make secrets ENV=staging` or
`ENV=prod` from the repo root — writes them to the VM over SSH and starts the
stack. Terraform only decides which secrets an environment needs, in each
root module's `secret_names` output. An apply therefore leaves the VM
provisioned but idle until that first push.

**To deploy, follow a guide:** [Deploying to Proxmox](../../docs/deploy/proxmox.md)
for staging. Production's guide is still to come; until then the steps are in
[`vultr/README.md`](vultr/README.md). The READMEs in this directory are what the
Terraform creates and why.

# Infrastructure (Proxmox) — staging

Terraform for the staging deployment: one Ubuntu VM on a Proxmox VE node,
running the app, Postgres, MinIO, and Caddy as containers under Docker
Compose. Same shape as [`../vultr`](../vultr/README.md), production, with one
deliberate difference — see "Product images" below.

## What this creates

- **`main.tf`** — downloads Ubuntu 24.04's cloud image onto the node once,
  uploads this config's rendered cloud-init as a *vendor-data* snippet (see
  the comment on `proxmox_virtual_environment_file.vendor_data` for why
  vendor-data and not user-data), and creates the VM: a boot disk from that
  image, a second disk for Postgres's and MinIO's data, and cloud-init
  network/SSH-key configuration.
- **No secrets.** There is no `secrets.tf` and no credential variable: the
  store's secrets live in `pass` and reach the VM through `make secrets`, never
  through Terraform — see "Secrets" below. The `outputs.tf` values that script
  reads (`ssh_target`, `app_name`, `secret_names`, `tunnel_token`) are the only
  interface between the two.
- **`../modules/app-stack`** — the same module `../vultr` uses, rendering
  the `docker-compose.yml`, `.env`, `Caddyfile`, and nightly backup timer
  that ship as vendor-data. This root module fixes `image_backend = "minio"`,
  which also means the backup timer needs no separate credentials — see the
  module README.
- **`../modules/cloudflare-tunnel`** — only when `ingress = "tunnel"`. Its
  connector token is the one secret Terraform does hold, because Cloudflare
  hands it over when the tunnel is created; `make secrets` reads it from the
  `tunnel_token` output. See "Ingress" below.

## Product images: self-hosted MinIO, not Cloudflare R2

Production uses R2 per this project's storage preference. Staging doesn't
have a Cloudflare account of its own, so it gets what development already
has: a MinIO container, created by `../modules/app-stack` when
`image_backend = "minio"`, with a `minio-init` job that creates the bucket
and sets it public-read — the same shape as the repo root's `compose.yaml`.
Caddy fronts it at `images_domain`, playing the role a CDN or custom domain
plays in front of R2 in production; see `BLOB_PUBLIC_BASE_URL` in
`.env.example`.

## Why bpg/proxmox

Two Terraform providers target Proxmox VE: `telmate/proxmox`, older and more
widely referenced but not actively maintained, and `bpg/proxmox`, its modern
replacement. This uses `bpg/proxmox` for one specific feature this config
depends on: a `snippets` file resource that uploads arbitrary content (this
module's rendered cloud-init) to the node, which is what `vendor_data_file_id`
below needs to exist at all.

## Three things Proxmox needs configured before `apply`

**An API token with enough privilege.** `PVEVMAdmin` alone is not enough:
downloading the Ubuntu image and uploading the snippet need `Datastore.*` and
`Sys.Modify` too. The provider documents a role with the full set — create it
on the node and give the token's user that role.

**A storage backend with the `Snippets` content type enabled**, matching
`snippets_storage` (default `local`). Datacenter -> Storage -> (your
storage) -> Edit -> Content, tick Snippets. Without it,
`proxmox_virtual_environment_file.vendor_data` fails to upload and nothing
else in this config can proceed — the VM's cloud-init has nowhere to come
from.

**SSH to the node, as `proxmox_ssh_username` (default `root`), through your
ssh-agent.** Two things here are not possible through the Proxmox API, so the
provider does them over SSH: uploading the snippet, and importing the Ubuntu
image as the VM's boot disk. With API-token authentication it has no other
credentials to try. The node must accept a key your agent holds; a non-root
user also needs passwordless `sudo` for `pvesm`, `qm`, and `tee` into the
snippets storage path.

## Going live — staging never does

`payfast_sandbox` defaults to `true` here, unlike `../vultr` where it has no
default at all. Staging exists to test the checkout flow, not to take money;
there's no reason it would ever need `false`, so the safe default is simply
correct rather than something to force a decision about.

## Ingress: Caddy, or a Cloudflare Tunnel

`ingress` defaults to `caddy`, which terminates TLS on the VM with Let's
Encrypt. On a Proxmox node behind a home or office router that means a
port-forward for 80 and 443, a public address, and a certificate renewal that
quietly stops working the day the forward is removed.

`ingress = "tunnel"` is the better fit here, and it is what staging is for
proving. `cloudflared` dials out to Cloudflare, so the VM publishes **no
ports at all**: no port-forward, no public address, no inbound firewall rule,
no ACME. Both hostnames go through it — the store, and the MinIO bucket that
serves product images, replacing the second Caddy site so
`BLOB_PUBLIC_BASE_URL` keeps working unchanged.

It also makes the client IP trustworthy. With the tunnel as the only way in,
`CF-Connecting-IP` cannot have been set by anyone but Cloudflare's edge, so
the module sets `CLIENT_IP_SOURCE=cloudflare` — which the PayFast callback's
source-IP check and the per-IP rate limits both depend on. See
[`../modules/cloudflare-tunnel`](../modules/cloudflare-tunnel/README.md) for
what gets created and which token scopes it needs.

Turning it on is three variables and an exported token:

```sh
export CLOUDFLARE_API_TOKEN=...   # Account: Cloudflare Tunnel:Edit, Zone: DNS:Edit
# in terraform.tfvars:
#   ingress               = "tunnel"
#   cloudflare_account_id = "..."
#   cloudflare_zone_id    = "..."
```

**Destroy order matters.** Terraform cannot delete a tunnel that still has a
connector attached, so `docker compose down` on the VM before
`terraform destroy`.

**Switching an existing `caddy` deployment has one manual step.** Caddy mode
has you create A records for `domain` and `images_domain` by hand, and
Cloudflare will not create a CNAME at a name that already has a record — so
the apply fails until they are gone. Delete both records in the dashboard
first; the site is unreachable for the minute between that and the apply
finishing. A fresh deployment that starts on `tunnel` has no such records and
skips this entirely.

## Networking

`ip_address` defaults to `dhcp`; `terraform.tfvars.example` sets a static
address instead, which is usually the right call for staging — DHCP means
re-editing `domain`'s DNS record (or a hosts-file entry, if this Proxmox
node is only reachable on a private network) every time the VM is recreated.
With `ingress = "tunnel"` the DNS records are Terraform's and point at the
tunnel rather than at this VM, so the VM's own address stops being something
anything outside the node needs to know.

This config does not touch Proxmox's own firewall subsystem. If SSH needs to
be restricted, do it at the network's edge (a router ACL, a VPN-only
management network) or configure the Proxmox firewall by hand — a
`proxmox_virtual_environment_firewall_*` resource is a reasonable follow-up
if this stops being a same-network-as-the-operator setup.

## What this is not

Same caveat as `../vultr`: updating the running image is a manual `docker
compose pull && docker compose up -d` over SSH, or a `terraform apply` that
recreates the VM (the second disk survives; a few minutes of downtime
doesn't).

Database backups aren't a gap here, though — see `../modules/app-stack`'s
README. `backup_retention_days` defaults to 7 rather than `../vultr`'s 30:
staging's backups exist to test the restore path and cover a bad seed or
migration, not to be a record anyone needs a month of.

## Secrets

Every credential the store needs lives in `pass` under `gostore/staging/`,
and reaches the VM only through `make secrets ENV=staging`: read from `pass`
on your machine, sent over SSH stdin, written as one `0400` file each into
`/opt/gostore/secrets`. None of them passes through Terraform, its state, or
the vendor-data snippet Proxmox stores. Terraform's part is deciding *which*
secrets this environment needs, from the features switched on, in its
`secret_names` output.

The entries, created once (the first line of an entry is the value, so notes
can go below it):

```sh
# Generated — nobody chooses these. -n keeps them URL-safe, which matters:
# database_url embeds postgres_password, and the backup embeds MinIO's.
pass generate -n gostore/staging/postgres_password 40
pass generate -n gostore/staging/setup_token 40
pass generate -n gostore/staging/minio_root_password 40

# From PayFast. Staging runs on the sandbox, so these are PayFast's
# published sandbox values: 46f0cd694581a and jt7NOE43FZPn.
pass insert gostore/staging/payfast_merchant_key
pass insert gostore/staging/payfast_passphrase

# Only if smtp_username is set, i.e. the relay authenticates:
pass insert gostore/staging/smtp_password
```

`database_url` is not an entry: `make secrets` builds it from
`postgres_password`, so the two can never disagree. With SnapScan on, it also
needs `snapscan_api_key` and `snapscan_webhook_auth_key` (plus
`snapscan_validation_key` if `snapscan_validation = true`). Run
`make secrets` without them and it lists what is missing, with the command to
create each, and pushes nothing.

Terraform's own credentials are separate, and also in `pass`: the Proxmox API
token, and the Cloudflare token when `ingress = "tunnel"`.

**Rotation** is `pass generate -f` or `pass insert -f`, then `make secrets`
again, which rewrites the files and recreates the containers. Except for
`postgres_password`: Postgres reads it only when the data directory is first
created, so changing it in `pass` does not change the database. Run
`ALTER USER gostore PASSWORD '…'` inside the container first, then update
`pass` and push.

## Usage

```sh
cd infra/terraform/proxmox
cp terraform.tfvars.example terraform.tfvars   # non-secret settings only
export TF_VAR_proxmox_api_token="$(pass show gostore/staging/proxmox_api_token)"
# If ingress = "tunnel":
export CLOUDFLARE_API_TOKEN="$(pass show gostore/staging/cloudflare_api_token)"
terraform init
terraform plan
terraform apply

# The VM is now provisioned but idle — the stack cannot start before its
# secrets exist. This pushes them and starts it:
cd ../../..
make secrets ENV=staging            # HOST=ubuntu@<address> if ip_address = "dhcp"
```

Point `domain` and `images_domain`'s DNS records at the VM's address (from
`ip_address`, or the `ip_addresses` output if you left it on `dhcp`) before
applying, or right after — Caddy retries the ACME HTTP-01 challenge until
both resolve.

The one-time admin setup token for `/admin/setup` is in `pass`:

```sh
pass show gostore/staging/setup_token
```

State is local by default (`terraform.tfstate`), gitignored. It holds no
store credentials, but it does hold the tunnel's connector token when
`ingress = "tunnel"`. Protect and back up the state file.

## Portability

The `proxmox_*` resources in `main.tf` are specific to this provider.
`../modules/app-stack` is not — see its own notes, and `../vultr`'s README.

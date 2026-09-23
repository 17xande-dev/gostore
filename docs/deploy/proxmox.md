# Deploying to Proxmox (staging)

This guide takes you from nothing to a running staging store on a Proxmox VE node: an
Ubuntu VM running the app, Postgres and MinIO under Docker Compose, reachable at your
domain. It uses [`infra/terraform/proxmox`](../../infra/terraform/proxmox/README.md),
whose README explains what gets created and why.

**Pick an ingress first**, because a few steps differ:

- **`tunnel` (recommended for a home or office node).** `cloudflared` dials out to
  Cloudflare, so the VM needs no port forward, no public address and no certificate, and
  Terraform creates the DNS records. Needs your domain to be a Cloudflare zone.
- **`caddy` (the default).** Caddy terminates TLS on the VM with Let's Encrypt. Needs
  ports 80 and 443 forwarded to the VM and DNS records you create yourself. No Cloudflare
  account needed.

Steps marked *(tunnel)* or *(caddy)* apply only to that choice.

Throughout, remember the rule from [Deploying](README.md#where-each-value-goes):
configuration goes in `terraform.tfvars`, Terraform's own credentials are exported from
`pass` into your shell, and the store's secrets go in `pass` and reach the VM through
`make secrets`. Nothing secret goes in `terraform.tfvars`, and there is no `.env` to edit.

## 1. What you need

On your machine:

- **Terraform** 1.5 or newer, **make**, and a checkout of this repo.
- **`pass`**, initialised with your GPG key (`pass init <your-gpg-id>`).
- **An ssh-agent holding two keys** (they may be the same key): one the Proxmox node
  accepts, and one you will install on the VM. Check with `ssh-add -l`. The Terraform
  provider reaches the node *only* through the agent — it ignores `~/.ssh/config`.

Elsewhere:

- **A published image** on GHCR, and **public** — a private package cannot be pulled by
  the VM. See [Publishing an image](README.md#publishing-an-image).
- **A domain**, with two hostnames to spend: one for the store (say
  `staging.example.com`) and one for product images (`images-staging.example.com`).
  *(tunnel)* The domain must be a zone on your Cloudflare account.
- **An SMTP relay** the store can send through. The server refuses to start without one.

## 2. Prepare the Proxmox node (once)

**An API token for Terraform.** On the node, as root. `PVEVMAdmin` alone is not enough —
downloading the Ubuntu image and uploading the cloud-init snippet need `Datastore.*` and
`Sys.Modify` as well — so create the role the provider documents:

```sh
pveum user add terraform@pve
pveum role add Terraform -privs "Datastore.Allocate Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Pool.Allocate Sys.Audit Sys.Console Sys.Modify SDN.Use VM.Allocate VM.Audit VM.Clone VM.Config.CDROM VM.Config.Cloudinit VM.Config.CPU VM.Config.Disk VM.Config.HWType VM.Config.Memory VM.Config.Network VM.Config.Options VM.Migrate VM.Monitor VM.PowerMgmt User.Modify"
pveum aclmod / -user terraform@pve -role Terraform
pveum user token add terraform@pve gostore --privsep=0
```

The last command prints a token id and a secret. Together they are the token Terraform
uses, in the form `terraform@pve!gostore=<secret>` — you will put it in `pass` in step 4.

**Snippets enabled on the `local` storage.** Terraform uploads the VM's cloud-init as a
snippet, and new Proxmox installs do not allow them. In the web UI: Datacenter → Storage →
`local` → Edit → Content, and tick **Snippets**, keeping what is already ticked.
`pvesm status --content snippets` should now list `local`. (The same storage needs
**ISO image** ticked, which it is by default; the Ubuntu image lands there.)

**SSH to the node as root, through your agent.** Two things Terraform does here cannot be
done through the Proxmox API — uploading the snippet, and turning the Ubuntu image into the
VM's disk — so the provider does them over SSH. Make sure the node accepts a key your agent
holds:

```sh
ssh-copy-id root@<node-address>
ssh root@<node-address> pvesm status   # must work without a password prompt
```

To use a non-root user instead, it needs passwordless `sudo` for `pvesm`, `qm`, and `tee`
into the snippets path — see the
[provider's SSH notes](https://github.com/bpg/terraform-provider-proxmox/blob/v0.66.3/docs/index.md#ssh-user) —
and you set `proxmox_ssh_username` in step 5.

## 3. Cloudflare *(tunnel)*

- **Account id and zone id.** Both are in the right-hand column of the zone's Overview page
  in the Cloudflare dashboard. They are identifiers, not secrets — they go in
  `terraform.tfvars`.
- **An API token** (My Profile → API Tokens → Create Token → Custom), with exactly:
  Account → **Cloudflare Tunnel: Edit**, and Zone → **DNS: Edit** limited to your zone.
  Not the Global API Key, which can do anything to every zone on the account.

## 4. Put the secrets in `pass`

Every entry lives under `gostore/staging/`. The first line of an entry is the value, so you
can keep notes on the lines below it.

**Terraform's credentials** — exported into your shell when you apply (step 6):

```sh
pass insert gostore/staging/proxmox_api_token       # terraform@pve!gostore=<secret>
pass insert gostore/staging/cloudflare_api_token    # (tunnel) the token from step 3
```

**The store's secrets** — pushed to the VM by `make secrets` (step 8), never seen by
Terraform:

```sh
# Generated: nobody chooses these. -n keeps them to letters and digits, which matters —
# the database URL embeds postgres_password, and the backup embeds MinIO's.
pass generate -n gostore/staging/postgres_password 40
pass generate -n gostore/staging/setup_token 40
pass generate -n gostore/staging/minio_root_password 40

# PayFast. Staging runs on the sandbox, so use PayFast's published sandbox values:
#   merchant key 46f0cd694581a, passphrase jt7NOE43FZPn
pass insert gostore/staging/payfast_merchant_key
pass insert gostore/staging/payfast_passphrase

# Only if your relay needs a login (you will set smtp_username in step 5):
pass insert gostore/staging/smtp_password
```

With SnapScan switched on you would also need `snapscan_api_key` and
`snapscan_webhook_auth_key`. If you are unsure whether you have them all, carry on:
`make secrets` checks before it pushes anything, and prints the command to create
whatever is missing.

## 5. Write `terraform.tfvars`

```sh
cd infra/terraform/proxmox
cp terraform.tfvars.example terraform.tfvars
```

Edit it. Nothing in it is secret; a complete tunnel setup looks like this:

```hcl
# The node
proxmox_endpoint = "https://pve.example.internal:8006"
proxmox_node     = "pve"             # the node's name, as the web UI shows it

# The VM. A static address is usually right: DHCP means you must look the
# address up (and pass HOST= to make secrets) every time the VM is recreated.
ip_address     = "192.168.1.50/24"
ip_gateway     = "192.168.1.1"
ssh_public_key = "ssh-ed25519 AAAA... you@yourhost"   # installed for the `ubuntu` user

# The store
container_image = "ghcr.io/17xande-dev/gostore:latest"
base_url        = "https://staging.example.com"
domain          = "staging.example.com"
images_domain   = "images-staging.example.com"
store_name      = "Your Store (staging)"
acme_email      = "ops@example.com"  # Let's Encrypt contact; required even with a tunnel, where it is unused

# Mail
smtp_host     = "smtp.example.com"
smtp_port     = 587
smtp_username = "orders@example.com" # set => gostore/staging/smtp_password must exist
email_from    = "orders@example.com"

# Ingress (tunnel). Leave these three out for caddy.
ingress               = "tunnel"
cloudflare_account_id = "0123456789abcdef0123456789abcdef"
cloudflare_zone_id    = "fedcba9876543210fedcba9876543210"
```

**Required:** `proxmox_endpoint`, `proxmox_node`, `ssh_public_key`, `container_image`,
`base_url`, `domain`, `images_domain`, `store_name`, `acme_email`, `smtp_host`,
`email_from` — and `proxmox_api_token`, which does **not** go here: it comes from your
shell in step 6. `base_url`'s host must be `domain`.

**Worth knowing about, all optional:**

| Variable | Default | When to change it |
|---|---|---|
| `proxmox_ssh_username` | `root` | Terraform SSHes to the node as a non-root sudo user (step 2) |
| `proxmox_ssh_address` | detected | the address the provider detects for the node is not the one you reach it on |
| `proxmox_insecure` | `true` | set `false` once the node has a real TLS certificate |
| `vm_storage` / `image_storage` / `snippets_storage` | `local-lvm` / `local` / `local` | your node names its storage differently |
| `network_bridge` | `vmbr0` | the VM belongs on another bridge |
| `cpu_cores` / `memory_mb` | `2` / `4096` | sizing |
| `boot_disk_gb` / `data_disk_gb` | `20` / `20` | the data disk holds Postgres and MinIO |
| `payfast_sandbox` | `true` | never, on staging |
| `backup_retention_days` | `7` | how long nightly database dumps are kept |

## 6. Apply

From `infra/terraform/proxmox`, export Terraform's credentials and apply. `head -n1` takes
only the value, in case an entry has notes below it:

```sh
export TF_VAR_proxmox_api_token="$(pass show gostore/staging/proxmox_api_token | head -n1)"
export CLOUDFLARE_API_TOKEN="$(pass show gostore/staging/cloudflare_api_token | head -n1)"   # (tunnel)
ssh-add -l          # the key the node accepts must be listed
terraform init
terraform plan      # read it: a VM, the Ubuntu image, the snippet — and the tunnel with its DNS records (tunnel)
terraform apply
```

Do not also set `proxmox_api_token` in `terraform.tfvars`: that file takes precedence over
`TF_VAR_*`.

When it finishes, the VM exists and is booting, but the store is **not running yet**. It
cannot start before its secrets are on the box, and they are not until step 8.

## 7. DNS and ports *(caddy)*

Point A records for both `domain` and `images_domain` at the address the VM is reachable
on from the internet, and forward ports **80 and 443** from your router to the VM's
`ip_address`. Caddy retries its Let's Encrypt challenge until both resolve, so this can
happen before or after step 8.

*(tunnel)* Nothing to do: Terraform created both DNS records, pointing at the tunnel.

## 8. Push the secrets and start the store

From the **repo root**:

```sh
make secrets ENV=staging                            # static ip_address
make secrets ENV=staging HOST=ubuntu@192.168.1.73   # ip_address = "dhcp": the VM's address
```

It connects as `ubuntu` with your agent's key, and in order:

1. waits for the VM's first boot to finish — that installs Docker, and takes a few minutes
   the first time;
2. reads every secret this configuration needs from `pass` — if any is missing it lists
   them all, with the command to create each, and pushes nothing;
3. writes each one to `/opt/gostore/secrets` on the VM, readable only by root and the
   store's containers;
4. starts the stack.

## 9. Check it, and claim the admin account

```sh
curl https://staging.example.com/healthz     # -> ok
pass show gostore/staging/setup_token
```

Open `https://staging.example.com/admin`. With no account yet it redirects to
`/admin/setup`; paste the token and choose an address and a password. That account is the
`owner`, and the token is spent. See [Admin and accounts](../admin.md) for the rest.

If `/healthz` does not answer, look on the box:

```sh
ssh ubuntu@192.168.1.50
cd /opt/gostore
sudo docker compose ps
sudo docker compose logs server      # the server says exactly which setting it refused
```

## Day two

**Shipping a new image.** Staging runs `:latest`, so after `make publish`:

```sh
ssh ubuntu@192.168.1.50 'cd /opt/gostore && sudo docker compose pull server && sudo docker compose up -d server'
```

Changing `container_image` in `terraform.tfvars` does **not** reach a running VM: its
cloud-init ran once, on first boot. Edit the `image:` line in
`/opt/gostore/docker-compose.yml` on the box instead, then pull and restart as above.

**Rotating a secret.** `pass generate -f …` or `pass insert -f …`, then
`make secrets ENV=staging` again; it rewrites the files and recreates the containers.
`postgres_password` needs one more step first, because Postgres only reads it when the
database is created: run
`sudo docker compose exec postgres psql -U gostore -c "ALTER USER gostore PASSWORD '<new>'"`
on the box, then update `pass`, then push.

**Backups.** A nightly `pg_dump` goes to a private bucket in the VM's own MinIO, kept for
`backup_retention_days`. The restore command is in the
[app-stack module's README](../../infra/terraform/modules/app-stack/README.md#database-backups).

**Rebuilding the VM loses its data.** Resizing a disk, or changing CPU or memory, happens
in place. But anything that makes Terraform *replace* the VM destroys both of its disks —
the data disk belongs to the VM, unlike production's separate Vultr volume — and with it
the database and MinIO, backups included. Fine for staging; know it before you do it.

**Tearing it down.** *(tunnel)* Stop the stack first — Terraform cannot delete a tunnel
that still has a connector attached:

```sh
ssh ubuntu@192.168.1.50 'cd /opt/gostore && sudo docker compose down'
cd infra/terraform/proxmox && terraform destroy
```

*(tunnel)* If you ever switch an existing `caddy` deployment to `tunnel`, delete the A
records you made in step 7 first: Cloudflare will not create a CNAME where a record
already exists.

## When a step fails

| Symptom | Likely cause |
|---|---|
| `apply` fails uploading the snippet | Snippets not enabled on `snippets_storage`, or Terraform cannot SSH to the node — check `ssh-add -l` and step 2 |
| `apply` fails with a permission error | The token's role lacks a privilege — use the role from step 2 |
| `make secrets` lists missing entries | Exactly what it says; create them and run it again — nothing was pushed |
| `make secrets` says cloud-init failed | First boot broke on the VM: `ssh ubuntu@… sudo cloud-init status --long` |
| The server container keeps restarting | It refused a setting; `sudo docker compose logs server` names it |
| The VM cannot pull the image | The GHCR package is private — make it public |
| *(caddy)* No certificate | DNS not pointing at you yet, or 80/443 not forwarded |

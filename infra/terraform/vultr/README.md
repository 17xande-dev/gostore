# Infrastructure (Vultr) — production

Terraform for the production deployment: one Ubuntu instance running the app,
Postgres, and Caddy as three containers under Docker Compose, plus a Block
Storage volume for Postgres's data and a firewall group. Product images live
in Cloudflare R2, which this Terraform does not provision — see "Product
images" below.

This replaced a Google Cloud config (Cloud Run, Cloud SQL, Secret Manager,
Artifact Registry). Nothing here is a managed equivalent of those services —
see "What this is not" before treating it as a drop-in replacement.

## What this creates

- **`main.tf`** — a firewall group (SSH from `ssh_allowed_subnet`, 80 and 443
  open for everyone), an SSH key, the instance itself, and a Block Storage
  volume for `/var/lib/postgresql/data`, attached but kept independent of the
  instance's boot disk so replacing the instance can never take the database
  with it.
- **No secrets.** There is no `secrets.tf` and no credential variable: the
  store's secrets live in `pass` and reach the instance through
  `make secrets`, never through Terraform — see "Secrets" below. The
  `outputs.tf` values that script reads (`ssh_target`, `app_name`,
  `secret_names`) are the only interface between the two.
- **`../modules/app-stack`** — renders the actual cloud-init payload (a
  `docker-compose.yml`, a `.env`, a `Caddyfile`, and the nightly backup
  timer) that the instance boots with. Shared with `../proxmox`, the staging
  config; see that module's README for what it does. This root module fixes
  `image_backend = "r2"`.

## Product images: Cloudflare R2, not Vultr Object Storage

Per this project's storage preference (R2 before GCS, before local/OSS,
before Amazon), images move to R2 rather than to Vultr's own S3-compatible
object storage, even though compute is moving to Vultr. The two are
independent: `internal/blob` speaks the S3 API against whatever endpoint
`BLOB_ENDPOINT` names, so nothing about the app cares which provider that is.

Terraform does not create the bucket or its API token. That mirrors how the
GCP config always treated PayFast's credentials: infrastructure is
Terraform's job, an account with a provider is a human's. Create the R2
bucket and a scoped API token by hand (Cloudflare dashboard: Account Home ->
R2 -> Manage API Tokens). The endpoint, bucket and access key id go in
`terraform.tfvars` — an access key id is an identifier, useless without its
secret. The secret goes in `pass` as `gostore/prod/blob_secret_access_key`,
and never through Terraform.

## Going live: `payfast_sandbox` has no default

The server defaults `PAYFAST_SANDBOX` to `true` so a first afternoon with the
project can't charge a real card. That default is wrong here, so this file
requires you to say which you mean — see `variables.tf`. Turning it off is
two changes, not one: `payfast_merchant_id` in `terraform.tfvars`, and the
merchant key and passphrase in `pass`, also need to be your own, or the
server refuses to start with live mode and a sandbox merchant id.

`CLIENT_IP_SOURCE` needs no variable here: Caddy is the only thing between the
internet and the app container, and it replaces `X-Forwarded-For` rather than
appending to it, so `forwarded` is always the right answer. See
`internal/middleware/clientip.go` for what a forged `X-Forwarded-For` could
otherwise do to the PayFast callback's source-IP check and to per-IP rate
limiting.

Putting Cloudflare in front of this instance changes that answer to
`cloudflare` — the edge appends to `X-Forwarded-For`, so its leftmost entry
becomes whatever the client sent.

## What this is not

**No managed database, no autoscaling, no zero-downtime deploy.** Postgres is
a container on the same box as the app. A `terraform apply` that changes
`user_data` (almost anything in `variables.tf`) recreates the instance from
scratch; the Block Storage volume survives that, so Postgres's data does, but
there is a few minutes of downtime while the new instance boots, installs
Docker, and starts the stack.

**Database backups are snapshots, not Cloud SQL's managed continuous
backup.** A systemd timer runs `pg_dump` nightly (`backup_schedule`,
`03:15` UTC by default) into a second R2 bucket — see
`../modules/app-stack`'s README for exactly what it does and the restore
command. `backups = "enabled"` on the instance is separate and snapshots the
boot disk only, not the Block Storage volume Postgres actually writes to; it
covers "the instance is gone," the timer covers "restore last night's data."

**Deploying a new image tag is a manual step**, same as any single-VM
Compose deployment:

```sh
ssh root@<instance_ip>
cd /opt/gostore
# edit docker-compose.yml's image: line, or re-render it via terraform apply
docker compose pull
docker compose up -d
```

Migrations still run on the app's own boot, guarded by the advisory lock
described in the root README — a second instance is never running here to
race against, but the mechanism costs nothing to leave on.

## Secrets

Every credential the store needs lives in `pass` under `gostore/prod/`, and
reaches the instance only through `make secrets ENV=prod`: read from `pass`
on your machine, sent over SSH stdin, written as one `0400` file each into
`/opt/gostore/secrets`. None of them passes through Terraform, its state, or
the cloud-init payload Vultr keeps and serves back from its metadata address.
Terraform's part is deciding *which* secrets this environment needs, in its
`secret_names` output.

The entries, created once (the first line of an entry is the value, so notes
can go below it):

```sh
# Generated — nobody chooses these. -n keeps them URL-safe: database_url
# embeds postgres_password in a URL.
pass generate -n gostore/prod/postgres_password 40
pass generate -n gostore/prod/setup_token 40

# From PayFast — your live values once payfast_sandbox = false; until then
# PayFast's published sandbox ones, 46f0cd694581a and jt7NOE43FZPn.
pass insert gostore/prod/payfast_merchant_key
pass insert gostore/prod/payfast_passphrase

# The secret halves of the two R2 tokens; their key ids go in tfvars.
pass insert gostore/prod/blob_secret_access_key
pass insert gostore/prod/backup_secret_access_key

# Only if smtp_username is set, i.e. the relay authenticates:
pass insert gostore/prod/smtp_password
```

`database_url` is not an entry: `make secrets` builds it from
`postgres_password`, so the two can never disagree. With SnapScan on, it also
needs `snapscan_api_key` and `snapscan_webhook_auth_key` (plus
`snapscan_validation_key` if `snapscan_validation = true`). Run
`make secrets` without them and it lists what is missing, with the command to
create each, and pushes nothing.

Terraform's own credential is separate, and also in `pass`: the Vultr API key.

**Rotation** is `pass generate -f` or `pass insert -f`, then `make secrets`
again, which rewrites the files and recreates the containers. Except for
`postgres_password`: Postgres reads it only when the data directory is first
created, so changing it in `pass` does not change the database. Run
`ALTER USER gostore PASSWORD '…'` inside the container first, then update
`pass` and push.

## Usage

```sh
cd infra/terraform/vultr
cp terraform.tfvars.example terraform.tfvars   # non-secret settings only
export VULTR_API_KEY="$(pass show gostore/prod/vultr_api_key)"
terraform init
terraform plan
terraform apply

# The instance is now provisioned but idle — the stack cannot start before
# its secrets exist. This pushes them and starts it:
cd ../../..
make secrets ENV=prod
```

Point `domain`'s DNS A/AAAA record at the `instance_ip` output before
applying, or right after — Caddy retries the ACME HTTP-01 challenge until it
resolves, but it can't get a certificate before then.

The one-time admin setup token for `/admin/setup` is in `pass`:

```sh
pass show gostore/prod/setup_token
```

State is local by default (`terraform.tfstate`), gitignored. It holds no
store credentials; protect and back up the state file anyway, and use a
backend with appropriate access controls before sharing this deployment.

## Portability

The `vultr_*` resources in `main.tf` are specific to this provider, same caveat the GCP config carried for `google_*` resources.
`../modules/app-stack` is not: it has no provider block and produces plain
cloud-init YAML, which is why `../proxmox` reuses it rather than duplicating
the compose file and Caddyfile by hand.

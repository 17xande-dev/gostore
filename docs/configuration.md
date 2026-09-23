# Configuration

Everything comes from the environment; see [`.env.example`](../.env.example) for the full
list with defaults.

## Where configuration and secrets live

`.env.example` is the tracked template: copy it to `.env` and edit the values
for the environment you run. It contains published PayFast sandbox credentials
and local Postgres/MinIO defaults, not private credentials. A real `.env` may
contain passwords, API keys, and a database URL with a password. It is
gitignored; also keep it readable only by its owner (`chmod 600 .env`). Do not
commit real values to `.env.example` or to either `terraform.tfvars.example`.

**Any credential can instead arrive as a file.** For each of the twelve
secrets the server takes — `DATABASE_URL`, `SETUP_TOKEN`, the payment keys,
the mail and storage secrets; `secretKeys` in `internal/config` is the list —
`KEY_FILE` names a file whose contents are the value. That keeps it out of the
process environment, which `docker inspect` shows, and lets a deployment mount
it as a Compose secret. Setting both `KEY` and `KEY_FILE` is refused at boot,
and so is a file that cannot be read.

For the Terraform VPS deployments, there are three kinds of value, and each
lives in exactly one place:

- **Configuration** — domain, image tag, email addresses, bucket names, the
  payment sandbox switch, and identifiers such as a merchant id or an access
  key id — goes in the local, gitignored `terraform.tfvars`.
- **Terraform's own credentials** — `VULTR_API_KEY`, `CLOUDFLARE_API_TOKEN`,
  and `TF_VAR_proxmox_api_token` — live in `pass` and are exported into the
  shell that runs Terraform. Do not also assign `proxmox_api_token` in
  `terraform.tfvars`: that file takes precedence over `TF_VAR_*`.
- **The store's runtime secrets** — the Postgres password, the setup token,
  the payment keys, the mail and storage secrets — live in `pass` as
  `gostore/<env>/<name>` and **never go near Terraform**. Terraform decides
  which ones an environment needs; `make secrets ENV=staging` (or `prod`)
  reads them from `pass` and writes them to the VM over SSH, one file each.

On the VM those files sit in `/opt/gostore/secrets` — root-only, each `0400`
and owned by uid 65532, the distroless user the server runs as — and reach
the containers as Compose secrets, which the server reads as `KEY_FILE`.
Nothing secret is in `/opt/gostore/.env`, in the Compose file, in the
cloud-init payload the provider keeps, or in Terraform state; the one
exception is a Cloudflare Tunnel's connector token, which Cloudflare hands to
Terraform when it creates the tunnel. There is no need to install `pass` or
copy a GPG key onto the VPS. What still exposes the running credentials is
root on the box, since the Docker socket is root-equivalent, and the GPG key
that unlocks your store. [Deploying](deploy/README.md) has the step-by-step
guides, including the entries each environment needs.

| Var | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | **yes** | — | Postgres connection string |
| `SETUP_TOKEN` | no | generated | The one-time token that claims the first account. 32+ characters. Generated and logged on first boot if unset |
| `SESSION_TTL_HOURS` | no | `24` | How long a sign-in lasts |
| `PAYFAST_MERCHANT_ID` | one gateway² | — | From the PayFast dashboard. Its presence switches PayFast on |
| `PAYFAST_MERCHANT_KEY` | with PayFast | — | From the PayFast dashboard |
| `PAYFAST_PASSPHRASE` | no | — | The account's salt passphrase; must match the dashboard exactly |
| `PAYFAST_SANDBOX` | no | `true` | `false` takes real money. Set it explicitly when deploying — see [Going live](payments.md#going-live) |
| `PAYFAST_NOTIFY_URL` | no | derived | Override when PayFast cannot reach `BASE_URL` (a tunnel) |
| `PAYFAST_ALLOWED_CIDRS` | no | published ranges | Override the source ranges; `any` disables the check |
| `SNAPSCAN_SNAP_CODE` | one gateway² | — | From SnapScan merchant support. Its presence switches SnapScan on. **No sandbox: this takes real money** |
| `SNAPSCAN_API_KEY` | with SnapScan | — | Reads payments back from SnapScan's API, which is how a notification is confirmed |
| `SNAPSCAN_WEBHOOK_AUTH_KEY` | with SnapScan | — | The shared secret a notification's HMAC is computed with |
| `SNAPSCAN_VALIDATION_KEY` | no | — | Secure QR Payload key; signs the amount and reference in the payment URL |
| `CLIENT_IP_SOURCE` | no | `remote` | Where the client address is read from: `remote`, `forwarded` (leftmost `X-Forwarded-For`, only with a proxy that replaces it) or `cloudflare` (`CF-Connecting-IP`) |
| `PORT` | no | `8080` | Listen port |
| `BASE_URL` | no | `http://localhost:8080` | Public origin, for absolute URLs |
| `STORE_NAME` | no | `gostore` | Displayed store name |
| `CURRENCY` | no | `ZAR` | Currency code. Both gateways settle in `ZAR` only, and the server refuses to start on anything else |
| `EMBED_ORIGINS` | no | — | Origins allowed to fetch and frame the catalog fragments |
| `FONT_ORIGINS` | no | — | Origins a web font may be loaded from. Widens the CSP's `font-src` **and** `style-src`. See [Web fonts](theming.md#web-fonts) |
| `FONT_CSS_URL` | no | — | A hosted font service's stylesheet, linked from the default layout. Its origin must be in `FONT_ORIGINS` |
| `TEMPLATE_DIR` | no | — | Directory of templates that override the embedded defaults |
| `STATIC_DIR` | no | — | Directory of assets that override the bundled ones (logo, placeholder, CSS) |
| `THEME_RELOAD` | no | `false` | Re-read those two directories on every request, so a theme edit needs a refresh and not a restart. Development only |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |
| `LOG_FORMAT` | no | `json` | `gcp` renames `level`/`msg` to `severity`/`message` for Cloud Logging. See [Logging](operations.md#logging) |
| `SHUTDOWN_TIMEOUT_SECONDS` | no | `15` | Grace period for in-flight requests |
| `SMTP_HOST` | **yes**¹ | — | Mail relay |
| `EMAIL_FROM` | **yes**¹ | — | Sender address |
| `SMTP_PORT` | no | `587` | `465` with `SMTP_TLS=tls`, `1025` for mailpit |
| `SMTP_TLS` | no | `starttls` | `starttls`, `tls` (implicit) or `none` (development only) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | no | — | Omit both for a relay that authenticates by address |
| `SMTP_OAUTH_TENANT_ID` / `SMTP_OAUTH_CLIENT_ID` / `SMTP_OAUTH_CLIENT_SECRET` | no⁵ | — | Authenticate with XOAUTH2 instead of a password. See [Microsoft Exchange Online](email.md#microsoft-exchange-online) |
| `GRAPH_TENANT_ID` / `GRAPH_CLIENT_ID` / `GRAPH_CLIENT_SECRET` | no⁶ | — | Send through Microsoft Graph instead of SMTP; wins when set. See [Microsoft Exchange Online](email.md#microsoft-exchange-online) |
| `EMAIL_REPLY_TO` | no | — | When replies should not go to `EMAIL_FROM` |
| `ORDER_NOTIFY_EMAIL` | no | — | Sends a copy of each paid order to whoever packs it |
| `IMAGE_DIR` | **yes**³ | — | Store images in this directory, served by this server |
| `BLOB_ENDPOINT` | **yes**³ | — | Object storage host[:port], no scheme |
| `BLOB_BUCKET` | no² | — | Bucket name |
| `BLOB_ACCESS_KEY_ID` / `BLOB_SECRET_ACCESS_KEY` | no² | — | Credentials |
| `BLOB_PUBLIC_BASE_URL` | no² | — | Where images are **read** from — not where they are written |
| `BLOB_REGION` | no | `auto` | What R2 wants; GCS and MinIO ignore it |
| `BLOB_USE_TLS` | no | `true` | `false` only for a MinIO on the same machine |
| `DOWNLOAD_DIR` | no⁴ | — | Store purchased files in this directory — **never** served publicly |
| `DOWNLOAD_ENDPOINT` | no⁴ | — | Private bucket host[:port], no scheme |
| `DOWNLOAD_BUCKET` | no⁴ | — | Bucket name; must not be `BLOB_BUCKET` |
| `DOWNLOAD_ACCESS_KEY_ID` / `DOWNLOAD_SECRET_ACCESS_KEY` | no⁴ | — | Credentials |
| `DOWNLOAD_PUBLIC_ENDPOINT` | no | — | The address a *browser* reaches the bucket at, when it differs from `DOWNLOAD_ENDPOINT`. Development stacks only |
| `DOWNLOAD_REGION` | no | `auto` | As `BLOB_REGION` |
| `DOWNLOAD_USE_TLS` | no | `true` | As `BLOB_USE_TLS` |
| `DOWNLOAD_MAX_BYTES` | no | `2GiB` | Cap on one uploaded file; accepts `500MB`, `2G`, or plain bytes |
| `RATE_LIMIT_LOGIN_PER_MINUTE` | no | `10` | Per client IP; `0` disables |
| `RATE_LIMIT_CHECKOUT_PER_MINUTE` | no | `20` | Per client IP; `0` disables |
| `RATE_LIMIT_CALLBACK_PER_MINUTE` | no | `120` | Per client IP; `0` disables |
| `RATE_LIMIT_STATUS_PER_MINUTE` | no | `30` | The QR hand-over page's payment-status poll, per IP |
| `RATE_LIMIT_DOWNLOAD_PER_MINUTE` | no | `60` | Download links per IP; each click mints a signed URL |
| `CART_TTL_DAYS` | no | `60` | How long an untouched cart survives |

² **At least one payment gateway must be configured**, and both may be. Set
`PAYFAST_MERCHANT_ID` (with its key), or `SNAPSCAN_SNAP_CODE` (with its API and webhook
keys), or both — with both, the checkout asks the shopper which they would like to use. A
store with neither refuses to start, because the alternative is a shop that serves a
catalog perfectly and fails at the one moment that matters.

¹ **Mail is required**, and both must be set. This reverses an earlier position that a store
with no mail server should still boot and drop receipts loudly. What changed is a fact rather
than an opinion: a digital download's link lives in the confirmation email and **only its hash
is stored**, so an unconfigured relay does not lose a receipt — it takes money for a file the
buyer can then never reach, unrecoverably. The old argument was right about a shop selling
parcels and wrong about one that *can* sell downloads, and any deployment might.

² The `BLOB_*` set is all-or-nothing for the same reason: `BLOB_ENDPOINT` with any of the
others missing refuses to boot rather than failing at the first upload.

³ **One image backend is required**, and the two are mutually exclusive — both set refuses to
boot because which one wins would otherwise be a guess, and neither set refuses because a
catalog whose products cannot have pictures is not a shop anybody buys from. The bar is
deliberately low: `IMAGE_DIR` is one path and needs nothing running.

⁴ The `DOWNLOAD_*` set is the **private** store for purchased files, and is separate from the
image one in every respect. `DOWNLOAD_DIR` and `DOWNLOAD_ENDPOINT` are mutually exclusive;
the bucket set is all-or-nothing; with none of it, the shop sells no digital products and
the admin says so. Two overlaps refuse to boot, and both would otherwise publish files
somebody paid for: a `DOWNLOAD_DIR` that is, contains, or sits inside `IMAGE_DIR` — which is
served publicly at `/images/` — and a `DOWNLOAD_BUCKET` equal to `BLOB_BUCKET`, which is
anonymously readable.

⁵ **All three or none.** A half-configured app registration refuses to boot, because the
alternative is a store that starts, takes an order, and only then finds it cannot
authenticate — with the buyer's download link in the message it failed to send. Setting them
alongside `SMTP_PASSWORD` also refuses: which one authenticates would be a guess, and the
loser would sit in the environment looking live. `SMTP_USERNAME` becomes required, because
XOAUTH2 authenticates as a named mailbox.

⁶ **All three or none, and it wins over SMTP when set.** `SMTP_HOST` does not need to be
configured at all for a Graph-only deployment — `EMAIL_FROM` alone satisfies the mail
requirement in that case. A half-configured Graph registration refuses to boot for the same
reason a half-configured XOAUTH2 one does.

# No credential is an input to this module. Every secret the stack needs is
# written onto the box from pass by `make secrets` and handed to the containers
# as a Compose secret — see the module README. What the module decides is
# which secrets an environment needs, and it says so in the secret_names
# output, which is what `make secrets` reads.
#
# Identifiers that sit beside those secrets — a merchant id, an access key id,
# a snap code — are ordinary variables here. None grants access on its own.

variable "app_name" {
  type    = string
  default = "gostore"
}

variable "data_device" {
  description = "Block device holding persistent data (Postgres, and MinIO's if used), e.g. /dev/vdb on Vultr or /dev/sdb on Proxmox. Formatted ext4 and mounted at /mnt/gostore-data on first boot if it isn't already — never reformatted on a later apply, so resizing the disk in place doesn't lose data."
  type        = string
}

variable "container_image" {
  description = "Full image reference to run, e.g. ghcr.io/you/gostore:v1"
  type        = string
}

variable "base_url" {
  type = string
}

variable "domain" {
  description = "Public hostname the store is served on. Must match base_url's host. With ingress = \"caddy\" it is the site Caddy requests a certificate for; with \"tunnel\" it is the hostname the caller's tunnel ingress rules route, and this module only uses it for the Caddyfile it then does not write."
  type        = string
}

variable "acme_email" {
  description = "Contact address for Let's Encrypt account registration and expiry notices. Unused when ingress = \"tunnel\": Cloudflare terminates TLS at its edge, so the box never asks anyone for a certificate."
  type        = string
  default     = ""
}

# --- ingress ---
#
# How a request reaches the app, which is the one part of this stack that is
# not the same everywhere:
#
#   "caddy"  — Caddy terminates TLS with Let's Encrypt and reverse-proxies to
#              the app. Publishes 80 and 443, so the box needs those open and
#              a public address. Works with no Cloudflare account at all,
#              which is why it is the default.
#   "tunnel" — cloudflared dials out to Cloudflare and the app is reached
#              through it. No inbound ports, no certificates, no ACME, and
#              nothing to firewall. The caller creates the tunnel; its
#              connector token is one of the secrets `make secrets` pushes,
#              read from the caller's tunnel_token output. See
#              ../cloudflare-tunnel.
#
# The choice also decides CLIENT_IP_SOURCE, because it decides which header
# in front of the app is trustworthy — see env.tftpl.
variable "ingress" {
  type    = string
  default = "caddy"
  validation {
    condition     = contains(["caddy", "tunnel"], var.ingress)
    error_message = "ingress must be \"caddy\" or \"tunnel\"."
  }
}

variable "store_name" {
  type = string
}

variable "currency" {
  type    = string
  default = "ZAR"
}

variable "log_format" {
  description = "json everywhere here — LOG_FORMAT=gcp is for Cloud Logging's field names, which nothing on this stack reads."
  type        = string
  default     = "json"
}

# --- database (self-hosted Postgres, one container per environment) ---

variable "postgres_data_mount" {
  description = "Host path bind-mounted into the postgres container. The caller formats and mounts the actual disk; this module only points compose at it."
  type        = string
  default     = "/mnt/gostore-data/postgres"
}

# --- payments ---

variable "payfast_sandbox" {
  description = "No default, on purpose — see the root modules' variables.tf for why."
  type        = bool
}

variable "payfast_merchant_id" {
  description = "An account identifier, not a credential — it appears in every payment form a buyer's browser posts. The merchant key and passphrase are the secrets."
  type        = string
}

variable "snapscan_snap_code" {
  description = "Empty runs without SnapScan. There is no sandbox for it — see .env.example. When set, snapscan_api_key and snapscan_webhook_auth_key become required secrets."
  type        = string
  default     = ""
}

variable "snapscan_validation" {
  description = "Whether SnapScan's Secure QR Payload validation is enabled on the account. true adds snapscan_validation_key to the required secrets."
  type        = bool
  default     = false
}

# --- mail ---
#
# Three ways to send, matching the three the server supports:
#
#   "smtp"         — a relay, authenticated by password (smtp_username set,
#                    so smtp_password is a required secret) or by network
#                    address (smtp_username empty).
#   "smtp_xoauth2" — a relay that takes an OAuth token instead of a password,
#                    which is what Microsoft Exchange Online wants. Needs
#                    smtp_username (XOAUTH2 authenticates as a named mailbox)
#                    and the app registration's two ids; its client secret is
#                    the smtp_oauth_client_secret secret. Never smtp_password
#                    too — the server refuses the pair.
#   "graph"        — Microsoft Graph over HTTPS, no SMTP at all. The app
#                    registration's two ids here, and its client secret as the
#                    graph_client_secret secret.
#
# The rules that span variables — smtp_host required unless graph, the ids
# required for the oauth modes — are preconditions on the user_data output,
# because variable validation cannot see other variables on Terraform 1.5.
# They fail the plan just the same.
variable "mail_transport" {
  type    = string
  default = "smtp"
  validation {
    condition     = contains(["smtp", "smtp_xoauth2", "graph"], var.mail_transport)
    error_message = "mail_transport must be \"smtp\", \"smtp_xoauth2\" or \"graph\"."
  }
}

variable "smtp_host" {
  description = "Required for \"smtp\" and \"smtp_xoauth2\"; ignored for \"graph\"."
  type        = string
  default     = ""
}

variable "smtp_oauth_tenant_id" {
  description = "\"smtp_xoauth2\" only: the Entra tenant id. An identifier, not a secret."
  type        = string
  default     = ""
}

variable "smtp_oauth_client_id" {
  description = "\"smtp_xoauth2\" only: the app registration's client id. An identifier; its secret is smtp_oauth_client_secret in pass."
  type        = string
  default     = ""
}

variable "graph_tenant_id" {
  description = "\"graph\" only: the Entra tenant id. An identifier, not a secret."
  type        = string
  default     = ""
}

variable "graph_client_id" {
  description = "\"graph\" only: the app registration's client id. An identifier; its secret is graph_client_secret in pass."
  type        = string
  default     = ""
}

variable "smtp_port" {
  type    = number
  default = 587
}

variable "smtp_tls" {
  description = "starttls | tls | none"
  type        = string
  default     = "starttls"
}

variable "smtp_username" {
  description = "\"smtp\": empty for a relay that authenticates by network address; when set, smtp_password becomes a required secret. \"smtp_xoauth2\": required — the mailbox XOAUTH2 authenticates as. Ignored for \"graph\"."
  type        = string
  default     = ""
}

variable "email_from" {
  type = string
  validation {
    condition     = trimspace(var.email_from) != ""
    error_message = "email_from must not be empty — required together with smtp_host."
  }
}

variable "order_notify_email" {
  type    = string
  default = ""
}

# --- product images ---
#
# Two shapes, matching the two backends the server actually supports:
#
#   "r2"    — object storage the caller already has credentials for (Cloudflare
#             R2 in production). This module only forwards BLOB_*; it does not
#             provision the bucket, on the same grounds the GCP version never
#             provisioned PayFast credentials: a Cloudflare account is a human
#             decision, not infrastructure this Terraform owns. The access key
#             id is a variable; its secret is blob_secret_access_key in pass.
#   "minio" — a MinIO container the app-stack module itself adds to the compose
#             file and fronts through Caddy, for an environment with no object
#             storage account of its own (staging). The app authenticates as
#             MinIO's root user, whose password is minio_root_password in pass.
variable "image_backend" {
  type = string
  validation {
    condition     = contains(["r2", "minio"], var.image_backend)
    error_message = "image_backend must be \"r2\" or \"minio\"."
  }
}

variable "blob_endpoint" {
  description = "Required when image_backend = \"r2\". Ignored for \"minio\", which always talks to the local minio service."
  type        = string
  default     = ""
}

variable "blob_bucket" {
  type    = string
  default = "gostore-images"
}

variable "blob_access_key_id" {
  description = "Required when image_backend = \"r2\". An identifier; the matching secret is blob_secret_access_key in pass."
  type        = string
  default     = ""
}

variable "blob_region" {
  type    = string
  default = "auto"
}

variable "blob_public_base_url" {
  description = "Where a browser fetches images from. Required for \"r2\" (your R2 custom domain or pub-*.r2.dev address). Computed automatically for \"minio\" from images_domain."
  type        = string
  default     = ""
}

variable "images_domain" {
  description = "Only used when image_backend = \"minio\": the hostname Caddy fronts the MinIO bucket on, e.g. images-staging.example.com. Needs its own DNS record and certificate, separate from domain."
  type        = string
  default     = ""
}

variable "minio_data_mount" {
  type    = string
  default = "/mnt/gostore-data/minio"
}

# --- database backups ---
#
# A systemd timer, not a container, on the grounds that this is one command
# on a schedule and a timer already exists on the box for free — no scheduler
# dependency to add or a long-running container to keep healthy.
#
# The destination follows image_backend, on the same reasoning it already
# encodes: "r2" means the caller has a real object storage account and a
# second bucket on it, whose secret is backup_secret_access_key in pass;
# "minio" means it doesn't, and this module points the timer at the same
# self-hosted MinIO it already runs, in a bucket of its own that minio-init
# creates private.
#
# Snapshots only, not point-in-time recovery — see the module README.

variable "backup_bucket" {
  type    = string
  default = "gostore-backups"
}

variable "backup_endpoint" {
  description = "Required when image_backend = \"r2\". Ignored for \"minio\"."
  type        = string
  default     = ""
}

variable "backup_access_key_id" {
  description = "Required when image_backend = \"r2\". An identifier; the matching secret is backup_secret_access_key in pass."
  type        = string
  default     = ""
}

variable "backup_retention_days" {
  description = "mc prunes objects older than this on every run — no local state tracks what's already been deleted."
  type        = number
  default     = 14
}

variable "backup_schedule" {
  description = "systemd OnCalendar expression, e.g. \"*-*-* 03:15:00\" for daily at 03:15 UTC."
  type        = string
  default     = "*-*-* 03:15:00"
}

variable "app_name" {
  type    = string
  default = "gostore"
}

# Johannesburg, matching the GCP config's africa-south1 default — the store's
# own default currency is ZAR. Override for a different customer base.
variable "region" {
  type    = string
  default = "jnb"
}

# 2 vCPU / 4GB: three containers share this box (app, Postgres, Caddy), and
# Postgres alone wants headroom Cloud Run's separate, autoscaled instance
# never had to share. Size up here rather than trimming a container's limits.
variable "plan" {
  type    = string
  default = "vc2-2c-4gb"
}

variable "data_volume_gb" {
  description = "Block Storage volume for Postgres's data directory, kept separate from the instance's boot disk so resizing or recreating the instance never touches it."
  type        = number
  default     = 20
}

variable "ssh_public_key" {
  description = "Public key installed on the instance for admin access. Generate a dedicated one — `ssh-keygen -t ed25519 -f gostore-prod`, don't reuse a personal key."
  type        = string
}

# No default, and not a CIDR string: Vultr's firewall rule schema wants the
# network address and prefix length as separate fields, which a parsed CIDR
# would just be reconstructing. 0.0.0.0/0 is wide open — narrow this to
# wherever you actually SSH from before the first apply.
variable "ssh_allowed_subnet" {
  type    = string
  default = "0.0.0.0"
}

variable "ssh_allowed_subnet_size" {
  type    = number
  default = 0
}

variable "container_image" {
  description = "Full image reference to run, e.g. ghcr.io/you/gostore:v1"
  type        = string
}

variable "base_url" {
  description = "Public origin of the store; becomes BASE_URL"
  type        = string
}

variable "domain" {
  description = "Hostname Caddy requests a certificate for and reverse-proxies to the app. Must match base_url's host. Needs an A/AAAA record pointing at this module's instance_ip output before the first boot, or Let's Encrypt's HTTP-01 challenge fails."
  type        = string
}

variable "acme_email" {
  description = "Contact address for the Let's Encrypt account Caddy registers."
  type        = string
}

variable "store_name" {
  type = string
}

variable "currency" {
  type    = string
  default = "ZAR"
}

# Deliberately no default, in either direction — see the module README.
variable "payfast_sandbox" {
  description = "true runs against PayFast's sandbox and takes no real money; false takes real payments. No default: say which you mean."
  type        = bool
}

# PayFast's own published sandbox credentials — the same ones the Makefile
# and compose.yaml default to. They take no real money. Replace all three
# with your own before setting payfast_sandbox = false; the server refuses to
# start with live mode and a sandbox merchant id.
variable "payfast_merchant_id" {
  type      = string
  default   = "10000100"
  sensitive = true
}

variable "payfast_merchant_key" {
  type      = string
  default   = "46f0cd694581a"
  sensitive = true
}

variable "payfast_passphrase" {
  type      = string
  default   = "jt7NOE43FZPn"
  sensitive = true
}

variable "snapscan_snap_code" {
  description = "Empty runs without SnapScan. No sandbox exists for it — any value here takes real money."
  type        = string
  default     = ""
}

variable "snapscan_api_key" {
  type      = string
  default   = ""
  sensitive = true
}

variable "snapscan_webhook_auth_key" {
  type      = string
  default   = ""
  sensitive = true
}

variable "snapscan_validation_key" {
  type      = string
  default   = ""
  sensitive = true
}

variable "smtp_host" {
  description = "Mail relay hostname. Required: the server refuses to boot without it."
  type        = string
  validation {
    condition     = trimspace(var.smtp_host) != ""
    error_message = "smtp_host must not be empty — a required variable set to \"\" would deploy a store that cannot send receipts."
  }
}

variable "smtp_port" {
  type    = number
  default = 587
}

variable "smtp_tls" {
  description = "starttls (default, port 587) | tls (implicit, port 465) | none"
  type        = string
  default     = "starttls"
}

variable "smtp_username" {
  type    = string
  default = ""
}

variable "smtp_password" {
  type      = string
  default   = ""
  sensitive = true
}

variable "email_from" {
  description = "Envelope and header From for every message the store sends. Required."
  type        = string
  validation {
    condition     = trimspace(var.email_from) != ""
    error_message = "email_from must not be empty — SMTP_HOST and EMAIL_FROM are required together."
  }
}

variable "order_notify_email" {
  type    = string
  default = ""
}

# --- Product images: Cloudflare R2 ---
#
# Not provisioned by this Terraform, on the same grounds the GCP config never
# provisioned PayFast credentials: a Cloudflare account and its API tokens
# are a human decision, not infrastructure. Create the bucket and an R2 API
# token (Account Home -> R2 -> Manage API Tokens) by hand, then fill these in.
variable "blob_endpoint" {
  description = "<account-id>.r2.cloudflarestorage.com"
  type        = string
}

variable "blob_bucket" {
  type    = string
  default = "gostore-images"
}

variable "blob_access_key_id" {
  type      = string
  sensitive = true
}

variable "blob_secret_access_key" {
  type      = string
  sensitive = true
}

variable "blob_region" {
  description = "R2 wants the literal string \"auto\"."
  type        = string
  default     = "auto"
}

variable "blob_public_base_url" {
  description = "Where a browser fetches images from: your R2 custom domain, or the bucket's pub-*.r2.dev address."
  type        = string
}

# --- Database backups: a second R2 bucket ---
#
# Can be the same Cloudflare account as blob_*, but ideally its own bucket
# and its own scoped API token — a token that can only reach gostore-backups
# is one that a compromised server can't use to overwrite the image bucket
# too, or vice versa.
variable "backup_endpoint" {
  description = "<account-id>.r2.cloudflarestorage.com — often identical to blob_endpoint, same account."
  type        = string
}

variable "backup_bucket" {
  type    = string
  default = "gostore-backups"
}

variable "backup_access_key_id" {
  type      = string
  sensitive = true
}

variable "backup_secret_access_key" {
  type      = string
  sensitive = true
}

variable "backup_retention_days" {
  type    = number
  default = 30
}

variable "backup_schedule" {
  description = "systemd OnCalendar expression."
  type        = string
  default     = "*-*-* 03:15:00"
}

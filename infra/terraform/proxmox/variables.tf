# --- Proxmox connection and node placement ---

variable "proxmox_endpoint" {
  description = "e.g. https://proxmox.example.internal:8006"
  type        = string
}

variable "proxmox_api_token" {
  description = "\"user@realm!token-id=uuid\", from Datacenter -> Permissions -> API Tokens. Needs enough privilege to create VMs and upload files — see the README."
  type        = string
  sensitive   = true
}

variable "proxmox_insecure" {
  description = "true skips TLS verification, for a node with a self-signed certificate. Set false once you've installed a real one."
  type        = bool
  default     = true
}

variable "proxmox_node" {
  description = "The Proxmox node (host) to place the VM on."
  type        = string
}

variable "vm_storage" {
  description = "Datastore ID for the VM's disks, e.g. local-lvm."
  type        = string
  default     = "local-lvm"
}

variable "image_storage" {
  description = "Datastore ID to download the Ubuntu cloud image into. Must have the \"ISO image\" content type enabled — this is where the qcow2 lands even though it isn't an ISO; see main.tf."
  type        = string
  default     = "local"
}

variable "snippets_storage" {
  description = "Datastore ID to upload the rendered cloud-init file into. Must have the \"Snippets\" content type enabled in Proxmox's storage config, which local (directory) storage supports and most others don't."
  type        = string
  default     = "local"
}

variable "network_bridge" {
  type    = string
  default = "vmbr0"
}

variable "ip_address" {
  description = "\"dhcp\", or a static address as CIDR, e.g. 192.168.1.50/24. ip_gateway is required with a static address."
  type        = string
  default     = "dhcp"
}

variable "ip_gateway" {
  type    = string
  default = ""
}

variable "cpu_cores" {
  type    = number
  default = 2
}

variable "memory_mb" {
  type    = number
  default = 4096
}

variable "boot_disk_gb" {
  type    = number
  default = 20
}

variable "data_disk_gb" {
  description = "Second disk, for Postgres's and MinIO's data — see main.tf for why it's separate from the boot disk."
  type        = number
  default     = 20
}

variable "ssh_public_key" {
  description = "Installed for the ubuntu user via Proxmox's own cloud-init user-data, not the app-stack module's — see main.tf."
  type        = string
}

# --- app-stack passthrough — same shape as ../vultr/variables.tf ---

variable "app_name" {
  type    = string
  default = "gostore"
}

variable "container_image" {
  type = string
}

variable "base_url" {
  type = string
}

variable "domain" {
  description = "Hostname Caddy requests a certificate for. Needs a DNS record (or a hosts-file entry, for a Proxmox that's only reachable on a private network) pointing at this VM before the first boot."
  type        = string
}

variable "acme_email" {
  type = string
}

variable "store_name" {
  type = string
}

variable "currency" {
  type    = string
  default = "ZAR"
}

# Defaults true here, unlike ../vultr: staging has no reason to ever take a
# real payment, so the safe default is the right one instead of something to
# force a decision about.
variable "payfast_sandbox" {
  type    = bool
  default = true
}

# PayFast's published sandbox merchant id. An identifier, not a secret — it
# is in every payment form a buyer's browser posts. The merchant key and
# passphrase that go with it are secrets, so they live in pass
# (gostore/staging/payfast_merchant_key and payfast_passphrase); for the
# sandbox, store PayFast's published values there — see the README.
variable "payfast_merchant_id" {
  type    = string
  default = "10000100"
}

variable "snapscan_snap_code" {
  description = "Empty runs without SnapScan. Setting it makes snapscan_api_key and snapscan_webhook_auth_key required in pass."
  type        = string
  default     = ""
}

variable "snapscan_validation" {
  description = "Whether Secure QR Payload validation is enabled on the SnapScan account; true also requires snapscan_validation_key in pass."
  type        = bool
  default     = false
}

variable "smtp_host" {
  type = string
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
  type    = string
  default = "starttls"
}

variable "smtp_username" {
  description = "Empty for a relay that authenticates by network address. Setting it makes smtp_password required in pass."
  type        = string
  default     = ""
}

variable "email_from" {
  type = string
  validation {
    condition     = trimspace(var.email_from) != ""
    error_message = "email_from must not be empty — SMTP_HOST and EMAIL_FROM are required together."
  }
}

variable "order_notify_email" {
  type    = string
  default = ""
}

# --- Product images: self-hosted MinIO ---
#
# Staging has no Cloudflare account of its own, unlike ../vultr — see that
# module's README. images_domain needs its own DNS record, same as domain.
variable "images_domain" {
  type = string
}

# --- ingress ---
#
# "caddy" (the default) terminates TLS on the VM with Let's Encrypt, which
# needs this box reachable from the internet on 80 and 443 — on a Proxmox node
# behind a home or office router that means a port-forward, and a certificate
# renewal that quietly stops working if it is ever removed.
#
# "tunnel" runs cloudflared instead. It dials out, so staging needs no
# port-forward, no public address and no inbound firewall rule at all, and
# CF-Connecting-IP becomes trustworthy because the tunnel is the only way in.
# It needs a Cloudflare account, a zone, and CLOUDFLARE_API_TOKEN exported.
variable "ingress" {
  type    = string
  default = "caddy"
  validation {
    condition     = contains(["caddy", "tunnel"], var.ingress)
    error_message = "ingress must be \"caddy\" or \"tunnel\"."
  }
}

variable "cloudflare_account_id" {
  description = "Required when ingress = \"tunnel\". An identifier rather than a secret, so it lives in terraform.tfvars."
  type        = string
  default     = ""
}

variable "cloudflare_zone_id" {
  description = "Zone that domain and images_domain belong to. Required when ingress = \"tunnel\"."
  type        = string
  default     = ""
}

variable "blob_bucket" {
  type    = string
  default = "gostore-images"
}

# --- Database backups: a second, private bucket on the same self-hosted MinIO ---
#
# No credentials to supply here — see ../modules/app-stack, which reuses the
# generated minio_root_password. Shorter retention than ../vultr's default:
# staging's backups exist to test the restore path and cover a bad seed or
# migration, not to be a record anyone needs a month of.
variable "backup_bucket" {
  type    = string
  default = "gostore-backups"
}

variable "backup_retention_days" {
  type    = number
  default = 7
}

variable "backup_schedule" {
  description = "systemd OnCalendar expression."
  type        = string
  default     = "*-*-* 03:15:00"
}

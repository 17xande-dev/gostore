data "vultr_os" "ubuntu" {
  filter {
    name   = "name"
    values = ["Ubuntu 24.04 LTS x64"]
  }
}

resource "vultr_ssh_key" "main" {
  name    = "${var.app_name}-prod"
  ssh_key = var.ssh_public_key
}

resource "vultr_firewall_group" "main" {
  description = "${var.app_name}-prod"
}

resource "vultr_firewall_rule" "ssh" {
  firewall_group_id = vultr_firewall_group.main.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = var.ssh_allowed_subnet
  subnet_size       = var.ssh_allowed_subnet_size
  port              = "22"
  notes             = "ssh"
}

resource "vultr_firewall_rule" "http" {
  firewall_group_id = vultr_firewall_group.main.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = "0.0.0.0"
  subnet_size       = 0
  port              = "80"
  notes             = "http, for the ACME challenge — Caddy redirects it to https"
}

resource "vultr_firewall_rule" "https" {
  firewall_group_id = vultr_firewall_group.main.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = "0.0.0.0"
  subnet_size       = 0
  port              = "443"
  notes             = "https"
}

# Cloud-init user-data, shared with the Proxmox staging config — see
# ../modules/app-stack. image_backend = "r2" is fixed here, not exposed as a
# variable: production has a real Cloudflare account, staging doesn't, and
# that's a decision this file makes rather than one an operator repeats.
module "app_stack" {
  source = "../modules/app-stack"

  app_name        = var.app_name
  container_image = var.container_image
  base_url        = var.base_url
  domain          = var.domain
  acme_email      = var.acme_email
  store_name      = var.store_name
  currency        = var.currency

  postgres_password = random_password.postgres.result
  setup_token       = random_password.setup_token.result

  # Vultr's first (and here, only) attached Block Storage volume always
  # appears as /dev/vdb — a platform convention, not something either
  # provider's resource schema exposes as an attribute to reference instead.
  data_device = "/dev/vdb"

  payfast_sandbox      = var.payfast_sandbox
  payfast_merchant_id  = var.payfast_merchant_id
  payfast_merchant_key = var.payfast_merchant_key
  payfast_passphrase   = var.payfast_passphrase

  snapscan_snap_code        = var.snapscan_snap_code
  snapscan_api_key          = var.snapscan_api_key
  snapscan_webhook_auth_key = var.snapscan_webhook_auth_key
  snapscan_validation_key   = var.snapscan_validation_key

  smtp_host          = var.smtp_host
  smtp_port          = var.smtp_port
  smtp_tls           = var.smtp_tls
  smtp_username      = var.smtp_username
  smtp_password      = var.smtp_password
  email_from         = var.email_from
  order_notify_email = var.order_notify_email

  image_backend          = "r2"
  blob_endpoint          = var.blob_endpoint
  blob_bucket            = var.blob_bucket
  blob_access_key_id     = var.blob_access_key_id
  blob_secret_access_key = var.blob_secret_access_key
  blob_region            = var.blob_region
  blob_public_base_url   = var.blob_public_base_url

  backup_endpoint          = var.backup_endpoint
  backup_bucket            = var.backup_bucket
  backup_access_key_id     = var.backup_access_key_id
  backup_secret_access_key = var.backup_secret_access_key
  backup_retention_days    = var.backup_retention_days
  backup_schedule          = var.backup_schedule
}

resource "vultr_instance" "main" {
  plan              = var.plan
  region            = var.region
  os_id             = data.vultr_os.ubuntu.id
  label             = "${var.app_name}-prod"
  hostname          = "${var.app_name}-prod"
  ssh_key_ids       = [vultr_ssh_key.main.id]
  firewall_group_id = vultr_firewall_group.main.id

  # Instance-level snapshots. This covers the boot disk, not the Block
  # Storage volume below — see the README's backup caveat.
  backups = "enabled"

  user_data = module.app_stack.user_data
}

# Kept separate from the instance's boot disk on purpose: resizing the plan
# or replacing the instance (a new os_id, a botched apply) must not be able
# to take Postgres's data with it. Not `prevent_destroy` — Vultr's API
# refuses to delete an attached volume anyway, so detach-then-destroy is
# already a deliberate two-step action.
resource "vultr_block_storage" "data" {
  region               = var.region
  size_gb              = var.data_volume_gb
  label                = "${var.app_name}-prod-data"
  attached_to_instance = vultr_instance.main.id
}

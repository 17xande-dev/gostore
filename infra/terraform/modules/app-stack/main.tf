# Renders the cloud-init user-data both root modules (vultr/, proxmox/) hand
# their instance resource. No provider block, no resources of its own — this
# is pure `templatefile()`, so the module needs nothing installed to plan.
#
# The two callers differ in exactly one place: where product images live.
# Production (vultr/) has a Cloudflare R2 account and passes image_backend =
# "r2" with real BLOB_* credentials; staging (proxmox/) has neither, so it
# passes "minio" and this module adds a MinIO container plus a second Caddy
# site fronting it, the same shape docker-compose.yaml uses in development.
locals {
  blob_use_tls = var.image_backend == "r2" ? true : false

  blob_endpoint = var.image_backend == "r2" ? var.blob_endpoint : "minio:9000"

  blob_access_key_id = (
    var.image_backend == "r2" ? var.blob_access_key_id : var.app_name
  )

  blob_secret_access_key = (
    var.image_backend == "r2" ? var.blob_secret_access_key : var.minio_root_password
  )

  blob_public_base_url = (
    var.image_backend == "r2"
    ? var.blob_public_base_url
    : "https://${var.images_domain}/${var.blob_bucket}"
  )

  # Same "r2 means the caller has an account, minio means it doesn't" split
  # as the blob_* locals above, applied to where backups go instead of where
  # images live.
  backup_use_tls = var.image_backend == "r2" ? true : false
  backup_scheme  = local.backup_use_tls ? "https" : "http"

  backup_endpoint = var.image_backend == "r2" ? var.backup_endpoint : "minio:9000"

  backup_access_key_id = (
    var.image_backend == "r2" ? var.backup_access_key_id : var.app_name
  )

  backup_secret_access_key = (
    var.image_backend == "r2" ? var.backup_secret_access_key : var.minio_root_password
  )

  env_file = templatefile("${path.module}/templates/env.tftpl", {
    app_name                  = var.app_name
    base_url                  = var.base_url
    store_name                = var.store_name
    currency                  = var.currency
    log_format                = var.log_format
    postgres_password         = var.postgres_password
    setup_token               = var.setup_token
    payfast_sandbox           = var.payfast_sandbox
    payfast_merchant_id       = var.payfast_merchant_id
    payfast_merchant_key      = var.payfast_merchant_key
    payfast_passphrase        = var.payfast_passphrase
    snapscan_snap_code        = var.snapscan_snap_code
    snapscan_api_key          = var.snapscan_api_key
    snapscan_webhook_auth_key = var.snapscan_webhook_auth_key
    snapscan_validation_key   = var.snapscan_validation_key
    smtp_host                 = var.smtp_host
    smtp_port                 = var.smtp_port
    smtp_tls                  = var.smtp_tls
    smtp_username             = var.smtp_username
    smtp_password             = var.smtp_password
    email_from                = var.email_from
    order_notify_email        = var.order_notify_email
    blob_endpoint             = local.blob_endpoint
    blob_bucket               = var.blob_bucket
    blob_access_key_id        = local.blob_access_key_id
    blob_secret_access_key    = local.blob_secret_access_key
    blob_region               = var.blob_region
    blob_use_tls              = local.blob_use_tls
    blob_public_base_url      = local.blob_public_base_url
  })

  compose_file = templatefile("${path.module}/templates/docker-compose.yml.tftpl", {
    app_name            = var.app_name
    container_image     = var.container_image
    postgres_password   = var.postgres_password
    postgres_data_mount = var.postgres_data_mount
    image_backend       = var.image_backend
    minio_root_password = var.minio_root_password
    minio_data_mount    = var.minio_data_mount
    blob_bucket         = var.blob_bucket
    backup_bucket       = var.backup_bucket
  })

  backup_sh = templatefile("${path.module}/templates/backup.sh.tftpl", {
    app_name                 = var.app_name
    backup_scheme            = local.backup_scheme
    backup_endpoint          = local.backup_endpoint
    backup_access_key_id     = local.backup_access_key_id
    backup_secret_access_key = local.backup_secret_access_key
    backup_bucket            = var.backup_bucket
    backup_retention_days    = var.backup_retention_days
  })

  backup_service = templatefile("${path.module}/templates/gostore-backup.service.tftpl", {})

  backup_timer = templatefile("${path.module}/templates/gostore-backup.timer.tftpl", {
    backup_schedule = var.backup_schedule
  })

  caddyfile = templatefile("${path.module}/templates/Caddyfile.tftpl", {
    acme_email    = var.acme_email
    domain        = var.domain
    image_backend = var.image_backend
    images_domain = var.images_domain
  })

  cloud_init = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    data_device         = var.data_device
    postgres_data_mount = var.postgres_data_mount
    image_backend       = var.image_backend
    minio_data_mount    = var.minio_data_mount
    env_b64             = base64encode(local.env_file)
    compose_b64         = base64encode(local.compose_file)
    caddyfile_b64       = base64encode(local.caddyfile)
    backup_sh_b64       = base64encode(local.backup_sh)
    backup_service_b64  = base64encode(local.backup_service)
    backup_timer_b64    = base64encode(local.backup_timer)
  })
}

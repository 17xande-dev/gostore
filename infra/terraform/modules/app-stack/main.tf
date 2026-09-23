# Renders the cloud-init user-data both root modules (vultr/, proxmox/) hand
# their instance resource. No provider block, no resources of its own — this
# is pure `templatefile()`, so the module needs nothing installed to plan.
#
# Callers differ in two places, and only two.
#
# Where product images live: vultr/ has a Cloudflare R2 account and passes
# image_backend = "r2"; proxmox/ has none, so it passes "minio" and this module
# adds a MinIO container plus a second Caddy site fronting it, the same shape
# docker-compose.yaml uses in development.
#
# And how a request gets in: ingress = "caddy" terminates TLS on the box with
# Let's Encrypt, "tunnel" runs cloudflared and publishes no ports at all. That
# also decides CLIENT_IP_SOURCE, because it decides which header in front of
# the app was written by something that cannot be lied to.
#
# No secret passes through here. The module decides which secrets the stack
# needs and names them; `make secrets` supplies them from pass.
locals {
  blob_use_tls = var.image_backend == "r2" ? true : false

  blob_endpoint = var.image_backend == "r2" ? var.blob_endpoint : "minio:9000"

  blob_access_key_id = (
    var.image_backend == "r2" ? var.blob_access_key_id : var.app_name
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

  # --- which secrets this environment needs ---
  #
  # Decided once, here, from which features are switched on, and read by both
  # the compose template and the secret_names output. That is what keeps the
  # files `make secrets` writes and the files Compose expects from drifting
  # apart: there is only one list.
  #
  # On minio the app authenticates as MinIO's root user, so its storage secret
  # and the backup's are both the MinIO root password rather than secrets of
  # their own.
  blob_secret   = var.image_backend == "r2" ? "blob_secret_access_key" : "minio_root_password"
  backup_secret = var.image_backend == "r2" ? "backup_secret_access_key" : "minio_root_password"

  # KEY -> the secret file the server reads it from as KEY_FILE.
  server_secret_candidates = {
    DATABASE_URL           = { name = "database_url", on = true }
    SETUP_TOKEN            = { name = "setup_token", on = true }
    PAYFAST_MERCHANT_KEY   = { name = "payfast_merchant_key", on = true }
    PAYFAST_PASSPHRASE     = { name = "payfast_passphrase", on = true }
    BLOB_SECRET_ACCESS_KEY = { name = local.blob_secret, on = true }
    # One mail credential at most, by transport. XOAUTH2 must never get
    # smtp_password as well: the server refuses the pair as ambiguous.
    SMTP_PASSWORD             = { name = "smtp_password", on = var.mail_transport == "smtp" && var.smtp_username != "" }
    SMTP_OAUTH_CLIENT_SECRET  = { name = "smtp_oauth_client_secret", on = var.mail_transport == "smtp_xoauth2" }
    GRAPH_CLIENT_SECRET       = { name = "graph_client_secret", on = var.mail_transport == "graph" }
    SNAPSCAN_API_KEY          = { name = "snapscan_api_key", on = var.snapscan_snap_code != "" }
    SNAPSCAN_WEBHOOK_AUTH_KEY = { name = "snapscan_webhook_auth_key", on = var.snapscan_snap_code != "" }
    SNAPSCAN_VALIDATION_KEY   = { name = "snapscan_validation_key", on = var.snapscan_snap_code != "" && var.snapscan_validation }
  }
  server_secret_env = { for key, s in local.server_secret_candidates : key => s.name if s.on }

  # Every secret some container mounts.
  compose_secrets = sort(distinct(concat(
    ["postgres_password"],
    values(local.server_secret_env),
    var.image_backend == "minio" ? ["minio_root_password"] : [],
    var.ingress == "tunnel" ? ["tunnel_token"] : [],
  )))

  # Plus the one only the host reads: backup.sh runs on the host, not in a
  # container, so on r2 its secret is pushed but mounted nowhere.
  secret_names = sort(distinct(concat(local.compose_secrets, [local.backup_secret])))

  env_file = templatefile("${path.module}/templates/env.tftpl", {
    base_url             = var.base_url
    store_name           = var.store_name
    currency             = var.currency
    log_format           = var.log_format
    payfast_sandbox      = var.payfast_sandbox
    payfast_merchant_id  = var.payfast_merchant_id
    snapscan_snap_code   = var.snapscan_snap_code
    mail_transport       = var.mail_transport
    smtp_host            = var.smtp_host
    smtp_port            = var.smtp_port
    smtp_tls             = var.smtp_tls
    smtp_username        = var.smtp_username
    smtp_oauth_tenant_id = var.smtp_oauth_tenant_id
    smtp_oauth_client_id = var.smtp_oauth_client_id
    graph_tenant_id      = var.graph_tenant_id
    graph_client_id      = var.graph_client_id
    email_from           = var.email_from
    order_notify_email   = var.order_notify_email
    blob_endpoint        = local.blob_endpoint
    blob_bucket          = var.blob_bucket
    blob_access_key_id   = local.blob_access_key_id
    blob_region          = var.blob_region
    blob_use_tls         = local.blob_use_tls
    blob_public_base_url = local.blob_public_base_url
    ingress              = var.ingress
  })

  compose_file = templatefile("${path.module}/templates/docker-compose.yml.tftpl", {
    app_name            = var.app_name
    container_image     = var.container_image
    postgres_data_mount = var.postgres_data_mount
    image_backend       = var.image_backend
    minio_data_mount    = var.minio_data_mount
    blob_bucket         = var.blob_bucket
    backup_bucket       = var.backup_bucket
    ingress             = var.ingress
    compose_secrets     = local.compose_secrets
    server_secret_env   = local.server_secret_env
  })

  backup_sh = templatefile("${path.module}/templates/backup.sh.tftpl", {
    app_name              = var.app_name
    backup_scheme         = local.backup_scheme
    backup_endpoint       = local.backup_endpoint
    backup_access_key_id  = local.backup_access_key_id
    backup_secret_file    = local.backup_secret
    backup_bucket         = var.backup_bucket
    backup_retention_days = var.backup_retention_days
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
    ingress             = var.ingress
    env_b64             = base64encode(local.env_file)
    compose_b64         = base64encode(local.compose_file)
    caddyfile_b64       = base64encode(local.caddyfile)
    backup_sh_b64       = base64encode(local.backup_sh)
    backup_service_b64  = base64encode(local.backup_service)
    backup_timer_b64    = base64encode(local.backup_timer)
  })
}

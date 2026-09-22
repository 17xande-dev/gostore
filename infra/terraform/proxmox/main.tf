locals {
  static_ip = var.ip_address != "dhcp"
}

# Proxmox has no equivalent of Vultr's marketplace OS list — it boots
# whatever disk image you give it. This downloads Ubuntu's own published
# cloud image once; `overwrite = false` means a later apply never re-fetches
# it just because the URL was touched.
resource "proxmox_virtual_environment_download_file" "ubuntu" {
  content_type = "iso"
  datastore_id = var.image_storage
  node_name    = var.proxmox_node
  url          = "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
  file_name    = "noble-server-cloudimg-amd64.qcow2.img"
  overwrite    = false
}

# Cloud-init user-data, shared with the Vultr production config — see
# ../modules/app-stack. image_backend = "minio" is fixed here, not exposed as
# a variable: staging has no Cloudflare account, production does, and that's
# a decision this file makes rather than one an operator repeats.
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

  # The second disk below attaches as the guest's first scsi data disk,
  # which the virtio-scsi driver Proxmox uses enumerates as /dev/sdb —
  # boot disk /dev/sda is scsi0.
  data_device = "/dev/sdb"

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

  image_backend       = "minio"
  images_domain       = var.images_domain
  blob_bucket         = var.blob_bucket
  minio_root_password = random_password.minio_root.result

  backup_bucket         = var.backup_bucket
  backup_retention_days = var.backup_retention_days
  backup_schedule       = var.backup_schedule
}

# This is cloud-init *vendor-data*, not user-data: Proxmox's own cloud-init
# integration (the user_account/ip_config block below) generates the
# user-data that installs the SSH key, and vendor-data is cloud-init's
# documented mechanism for a second, independently-authored config that
# merges with it — the runcmd and write_files lists concatenate rather than
# one replacing the other. Using it instead of user_data_file_id means the
# ubuntu user's key still gets installed even though this module also
# supplies a full cloud-init document.
resource "proxmox_virtual_environment_file" "vendor_data" {
  content_type = "snippets"
  datastore_id = var.snippets_storage
  node_name    = var.proxmox_node

  source_raw {
    data      = module.app_stack.user_data
    file_name = "${var.app_name}-staging-vendor-data.yaml"
  }
}

resource "proxmox_virtual_environment_vm" "main" {
  name      = "${var.app_name}-staging"
  node_name = var.proxmox_node
  tags      = ["gostore", "staging"]

  agent {
    enabled = true
  }

  cpu {
    cores = var.cpu_cores
  }

  memory {
    dedicated = var.memory_mb
  }

  disk {
    datastore_id = var.vm_storage
    file_id      = proxmox_virtual_environment_download_file.ubuntu.id
    interface    = "scsi0"
    size         = var.boot_disk_gb
  }

  # Postgres's and MinIO's data, kept on its own disk on the same grounds as
  # the Block Storage volume in ../vultr: resizing or rebuilding the VM must
  # not be able to take the database or the image bucket with it.
  disk {
    datastore_id = var.vm_storage
    interface    = "scsi1"
    size         = var.data_disk_gb
  }

  network_device {
    bridge = var.network_bridge
  }

  operating_system {
    type = "l26"
  }

  initialization {
    datastore_id        = var.vm_storage
    vendor_data_file_id = proxmox_virtual_environment_file.vendor_data.id

    ip_config {
      ipv4 {
        address = var.ip_address
        gateway = local.static_ip ? var.ip_gateway : null
      }
    }

    user_account {
      username = "ubuntu"
      keys     = [var.ssh_public_key]
    }
  }
}

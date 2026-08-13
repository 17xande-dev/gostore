locals {
  database_url = "postgres://${google_sql_user.app.name}:${random_password.db_app.result}@${google_sql_database_instance.main.private_ip_address}:5432/${google_sql_database.app.name}?sslmode=require"
}

resource "google_secret_manager_secret" "db_root_password" {
  secret_id = "${var.app_name}-db-root-password"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "db_root_password" {
  secret      = google_secret_manager_secret.db_root_password.id
  secret_data = random_password.db_root.result
}

resource "google_secret_manager_secret" "database_url" {
  secret_id = "${var.app_name}-database-url"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "database_url" {
  secret      = google_secret_manager_secret.database_url.id
  secret_data = local.database_url
}

# Session secret, admin password hash, PayFast credentials, SMTP password:
# generated/rotated out of band (make hashpw, PayFast dashboard, etc.) and
# populated into these secrets by hand or by a deploy script — Terraform
# only owns the container, not values that come from a human decision.
resource "google_secret_manager_secret" "session_secret" {
  secret_id = "${var.app_name}-session-secret"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "admin_password_hash" {
  secret_id = "${var.app_name}-admin-password-hash"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "payfast_merchant_id" {
  secret_id = "${var.app_name}-payfast-merchant-id"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "payfast_merchant_key" {
  secret_id = "${var.app_name}-payfast-merchant-key"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "payfast_passphrase" {
  secret_id = "${var.app_name}-payfast-passphrase"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "smtp_password" {
  secret_id = "${var.app_name}-smtp-password"
  replication {
    auto {}
  }
}

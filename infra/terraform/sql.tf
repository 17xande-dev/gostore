# Superuser password. Provisioning-only — never read by the running app,
# never placed in .env. Stored solely in Secret Manager for admin access.
resource "random_password" "db_root" {
  length  = 32
  special = false
}

# Application user's password. This is what DATABASE_URL actually uses.
resource "random_password" "db_app" {
  length  = 32
  special = false
}

resource "google_sql_database_instance" "main" {
  name             = "${var.app_name}-db"
  database_version = "POSTGRES_18"
  region           = var.region

  depends_on = [google_service_networking_connection.private_services]

  settings {
    tier    = var.db_tier
    edition = var.db_edition

    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
      ssl_mode        = "ENCRYPTED_ONLY"
    }

    backup_configuration {
      enabled = true
    }
  }

  root_password = random_password.db_root.result

  # Instances can't be recreated implicitly without data loss; require an
  # explicit `terraform destroy` intent rather than a plan-time surprise.
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_sql_database" "app" {
  name     = var.app_name
  instance = google_sql_database_instance.main.name
}

resource "google_sql_user" "app" {
  name     = var.app_name
  instance = google_sql_database_instance.main.name
  password = random_password.db_app.result
}

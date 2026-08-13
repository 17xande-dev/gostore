resource "google_service_account" "run" {
  account_id   = "${var.app_name}-run"
  display_name = "Cloud Run runtime SA for ${var.app_name}"
}

resource "google_project_iam_member" "run_cloudsql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.run.email}"
}

resource "google_secret_manager_secret_iam_member" "run_secret_access" {
  for_each = toset([
    google_secret_manager_secret.database_url.secret_id,
    google_secret_manager_secret.session_secret.secret_id,
    google_secret_manager_secret.admin_password_hash.secret_id,
    google_secret_manager_secret.payfast_merchant_id.secret_id,
    google_secret_manager_secret.payfast_merchant_key.secret_id,
    google_secret_manager_secret.payfast_passphrase.secret_id,
    google_secret_manager_secret.smtp_password.secret_id,
  ])
  secret_id = each.key
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.run.email}"
}

resource "google_cloud_run_v2_service" "main" {
  name     = var.app_name
  location = var.region

  template {
    service_account = google_service_account.run.email

    vpc_access {
      network_interfaces {
        network    = google_compute_network.main.id
        subnetwork = google_compute_subnetwork.run.id
      }
      egress = "PRIVATE_RANGES_ONLY"
    }

    containers {
      image = var.container_image

      env {
        name = "DATABASE_URL"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.database_url.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "SESSION_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.session_secret.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "ADMIN_PASSWORD_HASH"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.admin_password_hash.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "PAYFAST_MERCHANT_ID"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.payfast_merchant_id.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "PAYFAST_MERCHANT_KEY"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.payfast_merchant_key.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "PAYFAST_PASSPHRASE"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.payfast_passphrase.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "SMTP_PASSWORD"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.smtp_password.secret_id
            version = "latest"
          }
        }
      }

      env {
        name  = "BASE_URL"
        value = var.base_url
      }
      env {
        name  = "STORE_NAME"
        value = var.store_name
      }
      env {
        name  = "CURRENCY"
        value = var.currency
      }

      # Cloud Logging reads "severity" and "message"; slog writes "level" and
      # "msg". Without this every line files under DEFAULT severity, so a
      # severity>=ERROR filter matches nothing and alerting never fires.
      env {
        name  = "LOG_FORMAT"
        value = "gcp"
      }
    }
  }

  depends_on = [google_secret_manager_secret_version.database_url]
}

resource "google_cloud_run_v2_service_iam_member" "public" {
  location = google_cloud_run_v2_service.main.location
  name     = google_cloud_run_v2_service.main.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

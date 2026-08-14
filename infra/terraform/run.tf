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

      # Whether this shop is really open. The server defaults it to true so a
      # laptop cannot charge a card; here it is a required variable, because a
      # production deployment silently running against the sandbox is the same
      # mistake pointing the other way. See variables.tf.
      env {
        name  = "PAYFAST_SANDBOX"
        value = tostring(var.payfast_sandbox)
      }

      # Cloud Run is always behind Google's front end, so this is not a choice
      # about topology — it is a statement of fact about where the request came
      # from. Two things break without it, and both are quiet:
      #
      #   - The payment callback's source-IP check compares PayFast's published
      #     ranges against r.RemoteAddr, which here is Google's address and never
      #     PayFast's. Every genuine notification would be rejected — so with
      #     PAYFAST_SANDBOX=false and this unset, the store takes real money and
      #     records none of it. That is worse than the sandbox bug above.
      #   - Per-IP rate limiting keys every request in the world to one bucket,
      #     so one attacker can exhaust the admin login allowance for everybody.
      #
      # The cost is that X-Forwarded-For's leftmost entry is what a client sent
      # if the platform appends rather than replaces. That weakens the source-IP
      # check specifically, which is why it is defence in depth: a forged
      # notification still needs a valid signature and still has to be confirmed
      # by PayFast's own servers. See internal/middleware/clientip.go.
      env {
        name  = "TRUST_PROXY_IP"
        value = "true"
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

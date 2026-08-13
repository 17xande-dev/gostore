output "cloud_run_url" {
  value = google_cloud_run_v2_service.main.uri
}

output "db_private_ip" {
  value = google_sql_database_instance.main.private_ip_address
}

output "artifact_registry_repo" {
  value = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.main.repository_id}"
}

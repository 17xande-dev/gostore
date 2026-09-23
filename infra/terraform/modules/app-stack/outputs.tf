output "user_data" {
  description = "Cloud-init user-data to hand the instance/VM resource. Contains no secrets."
  value       = local.cloud_init
}

output "secret_names" {
  description = "Every secret file this environment's stack needs under /opt/gostore/secrets — the list `make secrets` pushes, and the only one there is."
  value       = local.secret_names
}

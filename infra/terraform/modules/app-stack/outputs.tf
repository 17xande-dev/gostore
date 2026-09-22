output "user_data" {
  description = "Cloud-init user-data to hand the instance/VM resource."
  value       = local.cloud_init
}

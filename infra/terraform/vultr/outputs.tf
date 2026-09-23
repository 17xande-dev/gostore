output "instance_ip" {
  description = "Point domain's DNS record here before the first boot — Caddy's ACME HTTP-01 challenge needs it resolvable."
  value       = vultr_instance.main.main_ip
}

output "ssh_command" {
  value = "ssh root@${vultr_instance.main.main_ip}"
}

# --- read by `make secrets ENV=prod` (infra/push-secrets.sh) ---

output "ssh_target" {
  description = "Where `make secrets` pushes to."
  value       = "root@${vultr_instance.main.main_ip}"
}

output "app_name" {
  description = "The Postgres user and database name, which `make secrets` needs to build database_url."
  value       = var.app_name
}

output "secret_names" {
  description = "The secret files this stack needs, one per line — exactly what `make secrets` pushes."
  value       = join("\n", module.app_stack.secret_names)
}

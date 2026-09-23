output "ip_addresses" {
  description = "Populated once the QEMU guest agent reports in after first boot — re-run `terraform refresh` if this is empty right after apply."
  value       = proxmox_virtual_environment_vm.main.ipv4_addresses
}

output "ssh_command" {
  value = var.ip_address != "dhcp" ? "ssh ubuntu@${split("/", var.ip_address)[0]}" : "ssh ubuntu@<ip from ip_addresses output>"
}

# --- read by `make secrets ENV=staging` (infra/push-secrets.sh) ---

output "ssh_target" {
  description = "Where `make secrets` pushes to. Empty with DHCP, since the address is not known to Terraform in advance — pass HOST=ubuntu@<address> instead."
  value       = local.static_ip ? "ubuntu@${split("/", var.ip_address)[0]}" : ""
}

output "app_name" {
  description = "The Postgres user and database name, which `make secrets` needs to build database_url."
  value       = var.app_name
}

output "secret_names" {
  description = "The secret files this stack needs, one per line — exactly what `make secrets` pushes."
  value       = join("\n", module.app_stack.secret_names)
}

# The one secret that does not start in pass: Cloudflare generates it when the
# tunnel is created. `make secrets` reads it from here.
output "tunnel_token" {
  value     = var.ingress == "tunnel" ? module.tunnel[0].token : ""
  sensitive = true
}

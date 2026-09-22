output "ip_addresses" {
  description = "Populated once the QEMU guest agent reports in after first boot — re-run `terraform refresh` if this is empty right after apply."
  value       = proxmox_virtual_environment_vm.main.ipv4_addresses
}

output "ssh_command" {
  value = var.ip_address != "dhcp" ? "ssh ubuntu@${split("/", var.ip_address)[0]}" : "ssh ubuntu@<ip from ip_addresses output>"
}

output "setup_token_command" {
  description = "Retrieves the one-time token /admin/setup exchanges for the first admin account."
  value       = "ssh ubuntu@<instance ip> 'sudo grep SETUP_TOKEN /opt/gostore/.env'"
}

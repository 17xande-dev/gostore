output "instance_ip" {
  description = "Point domain's DNS record here before the first boot — Caddy's ACME HTTP-01 challenge needs it resolvable."
  value       = vultr_instance.main.main_ip
}

output "ssh_command" {
  value = "ssh root@${vultr_instance.main.main_ip}"
}

output "setup_token_command" {
  description = "Retrieves the one-time token /admin/setup exchanges for the first admin account."
  value       = "ssh root@${vultr_instance.main.main_ip} 'grep SETUP_TOKEN /opt/gostore/.env'"
}

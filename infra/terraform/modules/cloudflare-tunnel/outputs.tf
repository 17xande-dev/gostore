output "token" {
  description = "Connector token for `cloudflared tunnel run`. The root module re-exports it for `make secrets`, which writes it to the box as a secret file — it never goes into cloud-init."
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.main.token
  sensitive   = true
}

output "tunnel_id" {
  description = "Useful for `cloudflared tunnel info` and for finding the tunnel in the Zero Trust dashboard."
  value       = cloudflare_zero_trust_tunnel_cloudflared.main.id
}

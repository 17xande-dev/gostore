variable "name" {
  description = "Tunnel name, as it appears in the Zero Trust dashboard. One per environment — a tunnel is not something two deployments should share, since its ingress rules are global to it."
  type        = string
}

variable "account_id" {
  description = "Cloudflare account the tunnel belongs to. Not a secret — an identifier, so it belongs in terraform.tfvars rather than the environment."
  type        = string
}

variable "zone_id" {
  description = "Zone the hostnames below live in. Also an identifier, not a secret."
  type        = string
}

# Hostname -> local service, in order. The catch-all is added by the module
# rather than asked for, because a tunnel config whose last rule has a hostname
# is rejected by Cloudflare, and that is not an error worth making anyone meet.
#
# The service addresses are compose service names: cloudflared shares the
# stack's network, so http://server:8080 resolves through Docker's embedded
# DNS exactly as Caddy's reverse_proxy target did.
variable "routes" {
  description = "Public hostnames this tunnel serves, each mapped to a service address on the compose network."
  type = list(object({
    hostname = string
    service  = string
  }))

  validation {
    condition     = length(var.routes) > 0
    error_message = "routes must name at least one hostname — a tunnel that routes nothing is a connector with nowhere to send traffic."
  }
}

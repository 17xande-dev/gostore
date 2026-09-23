terraform {
  required_version = ">= 1.5"

  required_providers {
    # bpg/proxmox rather than the older telmate/proxmox: it's the actively
    # maintained one, and the only one of the two with a `snippets` file
    # resource — which is how this config gets its own cloud-init content
    # onto the node at all (see main.tf's vendor_data_file_id).
    proxmox = {
      source  = "bpg/proxmox"
      version = "~> 0.66.0"
    }
    # Only used when ingress = "tunnel". Terraform configures a provider
    # whether or not any resource uses it, which is why api_token below comes
    # from the environment and defaults to empty rather than being required:
    # an ingress = "caddy" deployment should not have to hold a Cloudflare
    # credential to plan.
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.25"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "proxmox" {
  endpoint  = var.proxmox_endpoint
  api_token = var.proxmox_api_token
  insecure  = var.proxmox_insecure
}

# Reads CLOUDFLARE_API_TOKEN from the environment, on the same grounds
# ../vultr gives for VULTR_API_KEY: an API token belongs in a shell, not in a
# .tfvars file that gets copied around or, on a bad day, committed.
#
# Scope it to what this config actually does — Account: Cloudflare Tunnel:Edit
# and Zone: DNS:Edit on the one zone — rather than using a Global API Key,
# which can do anything to every zone on the account.
provider "cloudflare" {}

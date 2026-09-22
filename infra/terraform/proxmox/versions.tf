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

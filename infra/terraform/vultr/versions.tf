terraform {
  required_version = ">= 1.5"

  required_providers {
    vultr = {
      source  = "vultr/vultr"
      version = "~> 2.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# Reads VULTR_API_KEY from the environment. Deliberately not a Terraform
# variable: a personal access token belongs in a shell, not in a .tfvars file
# that gets copied around or, on a bad day, committed.
provider "vultr" {
  rate_limit  = 700
  retry_limit = 3
}

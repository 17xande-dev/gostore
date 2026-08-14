variable "project_id" {
  description = "GCP project ID (e.g. gostore-prod-479)"
  type        = string
}

variable "region" {
  description = "Region for every regional resource"
  type        = string
  default     = "africa-south1"
}

variable "app_name" {
  description = "Base name used to prefix most resources"
  type        = string
  default     = "gostore"
}

variable "db_tier" {
  description = "Cloud SQL machine tier"
  type        = string
  default     = "db-f1-micro"
}

variable "db_edition" {
  description = "Cloud SQL edition; db-f1-micro requires ENTERPRISE"
  type        = string
  default     = "ENTERPRISE"
}

variable "container_image" {
  description = "Full image reference to deploy to Cloud Run, e.g. REGION-docker.pkg.dev/PROJECT/gostore/gostore:TAG"
  type        = string
}

variable "base_url" {
  description = "Public origin of the store; becomes BASE_URL"
  type        = string
}

variable "store_name" {
  type = string
}

# Deliberately no default, in either direction.
#
# The server defaults this to true, so that a first afternoon with the project
# cannot charge a real card. That default is right for a laptop and wrong here:
# a production deployment that quietly runs against the sandbox takes no money
# and looks like it works, which is the mirror of the failure the default exists
# to prevent — and it was this file's actual behaviour until now, because it
# never set the variable at all.
#
# So terraform refuses to plan until somebody says which one they mean. There is
# no safe default for "is this shop really open".
variable "payfast_sandbox" {
  description = "true runs against PayFast's sandbox and takes no real money; false takes real payments. No default: say which you mean."
  type        = bool
}

variable "currency" {
  type    = string
  default = "ZAR"
}

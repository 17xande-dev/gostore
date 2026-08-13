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

variable "currency" {
  type    = string
  default = "ZAR"
}

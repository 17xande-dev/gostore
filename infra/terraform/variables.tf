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

# Images and mail are required by the server, so these are required here too —
# with the exception of the two that have a sensible answer rather than a
# decision behind them.

variable "blob_public_base_url" {
  description = <<-EOT
    Where a browser fetches product images from, if not the bucket's own public
    hostname. Set this to a CDN or a custom domain in front of the bucket. Null
    uses https://storage.googleapis.com/BUCKET, which works and is not fast.

    It is separate from the endpoint because the address a bucket is written
    through and the one it is read from are routinely different.
  EOT
  type        = string
  default     = null
}

# No default, on the same grounds as payfast_sandbox: there is no relay this
# project can guess, and a store that cannot send a receipt cannot deliver a
# digital download at all — the link lives in that email and nowhere else.
variable "smtp_host" {
  description = "Mail relay hostname. Required: the server refuses to boot without it."
  type        = string

  validation {
    condition     = trimspace(var.smtp_host) != ""
    error_message = "smtp_host must not be empty — a required variable set to \"\" would deploy a store that cannot send receipts."
  }
}

variable "smtp_port" {
  description = "Mail relay port. 587 for STARTTLS, 465 for implicit TLS."
  type        = number
  default     = 587
}

variable "smtp_username" {
  description = "Mail relay username. Empty for a relay that authenticates by network address."
  type        = string
  default     = ""
}

variable "email_from" {
  description = "Envelope and header From for every message the store sends. Required."
  type        = string

  validation {
    condition     = trimspace(var.email_from) != ""
    error_message = "email_from must not be empty — SMTP_HOST and EMAIL_FROM are required together."
  }
}

variable "order_notify_email" {
  description = "Where a copy of each paid order goes — whoever packs the parcel. Empty sends only the customer's receipt."
  type        = string
  default     = ""
}

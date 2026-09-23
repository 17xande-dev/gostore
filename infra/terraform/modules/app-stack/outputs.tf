output "user_data" {
  description = "Cloud-init user-data to hand the instance/VM resource. Contains no secrets."
  value       = local.cloud_init

  # The mail rules that span variables, which variable validation cannot
  # express on Terraform 1.5. Each mirrors a refusal the server makes at boot
  # (internal/config), so a mistake fails the plan here rather than surfacing
  # later as a container that will not start.
  precondition {
    condition     = var.mail_transport == "graph" || trimspace(var.smtp_host) != ""
    error_message = "smtp_host is required unless mail_transport = \"graph\": a store that cannot send a receipt cannot deliver a digital download either."
  }
  precondition {
    condition     = var.mail_transport != "smtp_xoauth2" || (var.smtp_username != "" && var.smtp_oauth_tenant_id != "" && var.smtp_oauth_client_id != "")
    error_message = "mail_transport = \"smtp_xoauth2\" needs smtp_username (the mailbox XOAUTH2 authenticates as), smtp_oauth_tenant_id and smtp_oauth_client_id."
  }
  precondition {
    condition     = var.mail_transport != "graph" || (var.graph_tenant_id != "" && var.graph_client_id != "")
    error_message = "mail_transport = \"graph\" needs graph_tenant_id and graph_client_id."
  }
}

output "secret_names" {
  description = "Every secret file this environment's stack needs under /opt/gostore/secrets — the list `make secrets` pushes, and the only one there is."
  value       = local.secret_names
}

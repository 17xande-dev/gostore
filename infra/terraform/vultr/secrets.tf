# Generated values only — nothing here is a human decision, so nothing here
# is a variable. Contrast with payfast_merchant_key or smtp_password in
# variables.tf, which come from an account somebody else set up.

resource "random_password" "postgres" {
  length  = 32
  special = false
}

# Claims the first admin account at /admin/setup. See the module README for
# why this is generated rather than left to a container log line.
resource "random_password" "setup_token" {
  length  = 43
  special = false
}

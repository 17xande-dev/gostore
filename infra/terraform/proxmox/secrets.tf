resource "random_password" "postgres" {
  length  = 32
  special = false
}

resource "random_password" "setup_token" {
  length  = 43
  special = false
}

# MinIO's root credentials. Generated, not a variable, on the same grounds as
# the two above — nobody chooses this value, it just needs to exist.
resource "random_password" "minio_root" {
  length  = 32
  special = false
}

resource "google_project_service" "apis" {
  for_each = toset([
    "run.googleapis.com",
    "sqladmin.googleapis.com",
    "artifactregistry.googleapis.com",
    "secretmanager.googleapis.com",
    "servicenetworking.googleapis.com",
    "vpcaccess.googleapis.com",
  ])
  service            = each.key
  disable_on_destroy = false
}

# Custom-mode network so subnets are explicit per region, rather than
# inheriting the auto-mode "default" network's /20 in every region at once.
resource "google_compute_network" "main" {
  name                    = "${var.app_name}-vpc"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.apis]
}

# Direct VPC egress needs a subnet in the same region as the Cloud Run
# service; Cloud SQL's private IP is separate (see the peering below) and
# does not live in this subnet's range.
resource "google_compute_subnetwork" "run" {
  name          = "${var.app_name}-run-${var.region}"
  network       = google_compute_network.main.id
  region        = var.region
  ip_cidr_range = "10.10.0.0/20"
}

# Reserved range + peering for Cloud SQL's private IP. This is the one-time
# "private services access" setup — Google's managed Cloud SQL network gets
# peered into this VPC so the instance's private IP is reachable from it.
resource "google_compute_global_address" "private_services" {
  name          = "${var.app_name}-private-services"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 16
  network       = google_compute_network.main.id
}

resource "google_service_networking_connection" "private_services" {
  network                 = google_compute_network.main.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_services.name]
  depends_on              = [google_project_service.apis]
}

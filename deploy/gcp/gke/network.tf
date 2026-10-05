resource "google_compute_network" "main" {
  name                    = "${var.name_prefix}-vpc"
  auto_create_subnetworks = false
}

locals {
  # The workspace egress policy (deploy/kubernetes/base) allows every IPv4 address outside
  # these ranges, as fixed blocks with no `except`: Dataplane V2 lets a workspace reach every
  # node through any allowed block that contains a node address, and an `except` does not stop
  # it (ADR 0106, addendum of 2026-10-03). So the cluster's own ranges must lie inside them.
  egress_denied = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"]

  cluster_ranges = {
    node_cidr          = var.node_cidr
    pod_cidr           = var.pod_cidr
    service_cidr       = var.service_cidr
    control_plane_cidr = var.control_plane_cidr
  }

  # [first, last] address of a CIDR as numbers, for the containment test below; Terraform has
  # no CIDR-contains function.
  cidr_bounds = {
    for c in distinct(concat(local.egress_denied, values(local.cluster_ranges))) : c => [
      sum([for i, o in split(".", split("/", c)[0]) : tonumber(o) * pow(256, 3 - i)]),
      sum([for i, o in split(".", split("/", c)[0]) : tonumber(o) * pow(256, 3 - i)]) + pow(2, 32 - tonumber(split("/", c)[1])) - 1,
    ]
  }

  ranges_outside_egress_denied = [
    for name, c in local.cluster_ranges : "${name} = ${c}" if !anytrue([
      for d in local.egress_denied :
      local.cidr_bounds[c][0] >= local.cidr_bounds[d][0] && local.cidr_bounds[c][1] <= local.cidr_bounds[d][1]
    ])
  ]
}

resource "google_compute_subnetwork" "nodes" {
  name                     = "${var.name_prefix}-nodes"
  network                  = google_compute_network.main.id
  region                   = var.region
  ip_cidr_range            = var.node_cidr
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = var.pod_cidr
  }
  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = var.service_cidr
  }

  lifecycle {
    precondition {
      condition     = length(local.ranges_outside_egress_denied) == 0
      error_message = "Outside RFC 1918 and 100.64.0.0/10: ${join(", ", local.ranges_outside_egress_denied)}. The workspace egress policy would let workspaces reach these addresses, and on Dataplane V2 every node (ADR 0106, addendum of 2026-10-03)."
    }
  }
}

# Egress to the internet leaves from one reserved address, so a git host or an API that
# allowlists by source address can be told which one. The nodes have no external IP.
resource "google_compute_address" "nat" {
  name   = "${var.name_prefix}-nat"
  region = var.region
}

resource "google_compute_router" "main" {
  name    = "${var.name_prefix}-router"
  network = google_compute_network.main.id
  region  = var.region
}

resource "google_compute_router_nat" "main" {
  name                               = "${var.name_prefix}-nat"
  router                             = google_compute_router.main.name
  region                             = var.region
  nat_ip_allocate_option             = "MANUAL_ONLY"
  nat_ips                            = [google_compute_address.nat.self_link]
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}

# Private Service Access, for Cloud SQL's private IP.
resource "google_compute_global_address" "private_service_access" {
  name          = "${var.name_prefix}-psa"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  address       = split("/", var.private_service_access_cidr)[0]
  prefix_length = tonumber(split("/", var.private_service_access_cidr)[1])
  network       = google_compute_network.main.id
}

resource "google_service_networking_connection" "private_service_access" {
  network                 = google_compute_network.main.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_service_access.name]
}

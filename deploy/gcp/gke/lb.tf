# What the global external Application Load Balancer needs from outside the cluster: a
# reserved address, the certificate and its map, and the DNS records. The load balancer
# itself is built by GKE's Gateway controller from deploy/kubernetes/components/gke,
# which names the address and the map below.

locals {
  preview_enabled = var.preview_domain != ""
}

resource "google_compute_global_address" "lb" {
  name = "${var.name_prefix}-lb"
}

data "google_dns_managed_zone" "main" {
  name = var.dns_zone
}

# DNS authorisation, so the certificate can cover the preview wildcard (an HTTP challenge
# cannot) and is issued before traffic points here.
resource "google_certificate_manager_dns_authorization" "fqdn" {
  name   = "${var.name_prefix}-fqdn"
  domain = var.fqdn
}

resource "google_certificate_manager_dns_authorization" "preview" {
  count  = local.preview_enabled ? 1 : 0
  name   = "${var.name_prefix}-preview"
  domain = var.preview_domain
}

resource "google_dns_record_set" "auth_fqdn" {
  managed_zone = data.google_dns_managed_zone.main.name
  name         = google_certificate_manager_dns_authorization.fqdn.dns_resource_record[0].name
  type         = google_certificate_manager_dns_authorization.fqdn.dns_resource_record[0].type
  ttl          = 300
  rrdatas      = [google_certificate_manager_dns_authorization.fqdn.dns_resource_record[0].data]
}

resource "google_dns_record_set" "auth_preview" {
  count        = local.preview_enabled ? 1 : 0
  managed_zone = data.google_dns_managed_zone.main.name
  name         = google_certificate_manager_dns_authorization.preview[0].dns_resource_record[0].name
  type         = google_certificate_manager_dns_authorization.preview[0].dns_resource_record[0].type
  ttl          = 300
  rrdatas      = [google_certificate_manager_dns_authorization.preview[0].dns_resource_record[0].data]
}

resource "google_certificate_manager_certificate" "main" {
  name = "${var.name_prefix}-cert"
  managed {
    domains = concat([var.fqdn], local.preview_enabled ? ["*.${var.preview_domain}"] : [])
    dns_authorizations = concat(
      [google_certificate_manager_dns_authorization.fqdn.id],
      local.preview_enabled ? [google_certificate_manager_dns_authorization.preview[0].id] : [],
    )
  }
}

resource "google_certificate_manager_certificate_map" "main" {
  name = "${var.name_prefix}-certmap"
}

resource "google_certificate_manager_certificate_map_entry" "fqdn" {
  name         = "${var.name_prefix}-fqdn"
  map          = google_certificate_manager_certificate_map.main.name
  hostname     = var.fqdn
  certificates = [google_certificate_manager_certificate.main.id]
}

resource "google_certificate_manager_certificate_map_entry" "preview" {
  count        = local.preview_enabled ? 1 : 0
  name         = "${var.name_prefix}-preview"
  map          = google_certificate_manager_certificate_map.main.name
  hostname     = "*.${var.preview_domain}"
  certificates = [google_certificate_manager_certificate.main.id]
}

resource "google_dns_record_set" "fqdn" {
  managed_zone = data.google_dns_managed_zone.main.name
  name         = "${var.fqdn}."
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_global_address.lb.address]
}

resource "google_dns_record_set" "preview" {
  count        = local.preview_enabled ? 1 : 0
  managed_zone = data.google_dns_managed_zone.main.name
  name         = "*.${var.preview_domain}."
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_global_address.lb.address]
}

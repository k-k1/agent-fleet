# Application-layer encryption of Secrets in etcd (ADR 0106 decision 6): every workspace's
# DEK and minted tokens sit in a Secret, which is only base64 without it.
resource "google_kms_key_ring" "gke" {
  name     = "${var.name_prefix}-gke"
  location = var.region
}

resource "google_kms_crypto_key" "gke_secrets" {
  name            = "${var.name_prefix}-gke-secrets"
  key_ring        = google_kms_key_ring.gke.id
  rotation_period = "7776000s"

  lifecycle {
    prevent_destroy = true
  }
}

# Every grant here names one principal on one resource (ADR 0106 decision 12). No grant
# may name the workspace namespace or its service account, directly or through a
# principalSet: a workspace has no cloud identity (decision 7).

# --- the CP ----------------------------------------------------------------------

resource "google_service_account" "cp" {
  account_id   = "${var.name_prefix}-cp"
  display_name = "Agent Fleet control plane (${var.name_prefix})"
}

# Workload Identity: the CP's Kubernetes service account, and nothing else, acts as it.
resource "google_service_account_iam_member" "cp_workload_identity" {
  service_account_id = google_service_account.cp.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${local.workload_pool}[${local.cp_namespace}/${local.cp_ksa}]"

  # The pool is a string built from the project ID, so nothing else orders this after the
  # cluster, and the pool exists only once the first Workload Identity cluster does:
  # before that the grant fails with "Identity Pool does not exist".
  depends_on = [google_container_cluster.main]
}

# Cloud SQL roles exist only at project level; the condition narrows them to this
# deployment's instance.
resource "google_project_iam_member" "cp_sql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = google_service_account.cp.member

  condition {
    title      = "${var.name_prefix}-pg only"
    expression = "resource.type == \"sqladmin.googleapis.com/Instance\" && resource.name == \"projects/${var.project_id}/instances/${google_sql_database_instance.main.name}\""
  }
}

resource "google_project_iam_member" "cp_sql_instance_user" {
  project = var.project_id
  role    = "roles/cloudsql.instanceUser"
  member  = google_service_account.cp.member

  condition {
    title      = "${var.name_prefix}-pg only"
    expression = "resource.type == \"sqladmin.googleapis.com/Instance\" && resource.name == \"projects/${var.project_id}/instances/${google_sql_database_instance.main.name}\""
  }
}

# Stale() compares the launched image with the tag's current digest through the
# registry's API, with the CP's own identity (decision 9).
resource "google_artifact_registry_repository_iam_member" "cp_reader" {
  count      = var.artifact_registry_repository == null ? 0 : 1
  location   = var.artifact_registry_repository.location
  repository = var.artifact_registry_repository.repository
  role       = "roles/artifactregistry.reader"
  member     = google_service_account.cp.member
}

# --- the nodes -------------------------------------------------------------------

# Pulling images is the node's job (decision 9); the pods' own identities play no part.
resource "google_service_account" "nodes" {
  account_id   = "${var.name_prefix}-nodes"
  display_name = "Agent Fleet GKE nodes (${var.name_prefix})"
}

resource "google_artifact_registry_repository_iam_member" "nodes_reader" {
  count      = var.artifact_registry_repository == null ? 0 : 1
  location   = var.artifact_registry_repository.location
  repository = var.artifact_registry_repository.repository
  role       = "roles/artifactregistry.reader"
  member     = google_service_account.nodes.member
}

# The minimum a node needs: log and metric writing, and from 1.33 the autoscaler's
# metrics too (autoscaling.sites.writeMetrics), which logWriter + metricWriter lack. The
# role has no resource narrower than the project. Never grant this account the node
# service agent's role (container.defaultNodeServiceAgent) instead.
resource "google_project_iam_member" "nodes_default_role" {
  project = var.project_id
  role    = "roles/container.defaultNodeServiceAccount"
  member  = google_service_account.nodes.member
}

# --- GKE's own service agent -----------------------------------------------------

# Encrypts and decrypts the cluster's Secrets with the key of kms.tf, and nothing else.
resource "google_kms_crypto_key_iam_member" "gke_secrets" {
  crypto_key_id = google_kms_crypto_key.gke_secrets.id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = "serviceAccount:service-${data.google_project.current.number}@container-engine-robot.iam.gserviceaccount.com"
}

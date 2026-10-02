terraform {
  required_version = ">= 1.6"

  # The state bucket is the operator's, named at init:
  #   terraform init -backend-config="bucket=<bucket>" -backend-config="prefix=<name_prefix>"
  backend "gcs" {}

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 3.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

data "google_client_config" "current" {}

# Only for the StorageClass (storage.tf). The endpoint is the cluster's private one when
# enable_private_endpoint is set, so `terraform apply` must then run from a network the
# authorised networks admit.
provider "kubernetes" {
  host                   = "https://${google_container_cluster.main.endpoint}"
  token                  = data.google_client_config.current.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.main.master_auth[0].cluster_ca_certificate)
}

data "google_project" "current" {
  project_id = var.project_id
}

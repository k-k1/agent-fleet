# Offline checks of the cluster resource against mocked providers: no project, no
# credentials. Needs Terraform 1.11 or later (state_key). Run from deploy/gcp/gke after
# `terraform init -backend=false`:
#   terraform test
#
# The live failures they pin are in the runbook's P1 and P11
# (deploy/kubernetes/README.md).

# Defaults only where the provider validates a value the random mock would not pass.
mock_provider "google" {
  mock_data "google_project" {
    defaults = {
      number = "123456789012"
    }
  }
  mock_resource "google_service_account" {
    defaults = {
      email  = "af-example-sa@example-project.iam.gserviceaccount.com"
      member = "serviceAccount:af-example-sa@example-project.iam.gserviceaccount.com"
      name   = "projects/example-project/serviceAccounts/af-example-sa@example-project.iam.gserviceaccount.com"
    }
  }
  mock_resource "google_compute_network" {
    defaults = {
      id = "projects/example-project/global/networks/af-example"
    }
  }
  mock_resource "google_certificate_manager_dns_authorization" {
    defaults = {
      dns_resource_record = [{
        name = "_acme-challenge.agent-fleet.example.com."
        type = "CNAME"
        data = "example.authorize.certificatemanager.goog."
      }]
    }
  }
}

mock_provider "kubernetes" {}

variables {
  project_id  = "example-project"
  region      = "europe-west1"
  node_zones  = ["europe-west1-b", "europe-west1-c"]
  name_prefix = "af-example"

  dns_zone       = "example-zone"
  fqdn           = "agent-fleet.example.com"
  preview_domain = "preview.agent-fleet.example.com"
}

override_resource {
  target = google_container_cluster.main
  values = {
    endpoint = "192.0.2.10"
    master_auth = [{
      cluster_ca_certificate = "Y2E="
    }]
    master_version = "1.35.8-gke.1000000"
  }
}

# GKE takes min_master_version as the version to create at, and a prefix the channel no
# longer offers fails the create. The cluster is created at the channel's default.
run "creates_at_the_channel_default" {
  command = plan

  assert {
    condition     = google_container_cluster.main.min_master_version == null
    error_message = "min_master_version must stay unset: GKE creates the cluster at it, and an old prefix fails the create"
  }
}

# NodeLocal DNSCache answers from the node, which the workspace egress-dns policy does not
# admit (P11). GKE turns it on by default for new clusters, so it is set explicitly.
run "node_local_dns_cache_off" {
  command = plan

  assert {
    condition     = google_container_cluster.main.addons_config[0].dns_cache_config[0].enabled == false
    error_message = "addons_config.dns_cache_config.enabled must be false (P11)"
  }
}

# A create at a version the floor accepts.
run "accepts_a_master_at_or_above_the_floor" {
  command = apply
}

# A create the channel would put below the floor fails after the create, in its own state.
run "rejects_a_master_below_the_floor" {
  command   = apply
  state_key = "below_floor"

  override_resource {
    target = google_container_cluster.main
    values = {
      endpoint = "192.0.2.10"
      master_auth = [{
        cluster_ca_certificate = "Y2E="
      }]
      master_version = "1.32.9-gke.1000000"
    }
  }

  expect_failures = [google_container_cluster.main]
}

# On the cluster created above (1.35.8), a plan checks the floor before anything changes.
run "a_plan_rejects_an_existing_master_below_the_floor" {
  command = plan

  variables {
    min_master_version = "1.36"
  }

  expect_failures = [google_container_cluster.main]
}

run "a_plan_accepts_an_existing_master_of_the_same_minor" {
  command = plan

  variables {
    min_master_version = "1.35"
  }
}

run "the_floor_is_major_minor" {
  command = plan

  variables {
    min_master_version = "1.35.8"
  }

  expect_failures = [var.min_master_version]
}

# Offline checks of the cluster resource against mocked providers: no project, no
# credentials. Needs Terraform 1.11 or later (state_key). Run from deploy/gcp/gke:
#   terraform init -backend=false -test-directory=offline-tests
#   terraform test -test-directory=offline-tests
#
# Not in tests/: init parses that directory on every run, and Terraform before 1.7 rejects
# mock_provider there, which would break `terraform init` for the module's 1.6 floor.
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

# The workspace egress policy allows every address outside RFC 1918 and 100.64.0.0/10, so a
# cluster range outside them would be reachable from workspaces, its nodes included (ADR 0106,
# addendum of 2026-10-03).
run "accepts_cluster_ranges_inside_the_denied_ranges" {
  command = plan

  variables {
    node_cidr          = "100.64.0.0/20"
    pod_cidr           = "192.168.0.0/17"
    service_cidr       = "172.31.240.0/20"
    control_plane_cidr = "10.255.255.240/28"
  }
}

run "rejects_a_public_service_range" {
  command = plan

  variables {
    service_cidr = "34.118.224.0/20"
  }

  expect_failures = [google_compute_subnetwork.nodes]
}

run "rejects_a_node_range_just_past_rfc1918" {
  command = plan

  variables {
    node_cidr = "172.32.0.0/20"
  }

  expect_failures = [google_compute_subnetwork.nodes]
}

run "rejects_a_pod_range_wider_than_its_rfc1918_block" {
  command = plan

  variables {
    pod_cidr = "10.0.0.0/7"
  }

  expect_failures = [google_compute_subnetwork.nodes]
}

# The small profile (README, "Small profile"): one zone, a zonal cluster, no managed
# Prometheus, Cloud SQL ZONAL.
run "small_profile_plans" {
  command = plan

  variables {
    node_zones             = ["europe-west1-b"]
    zonal_cluster          = true
    system_machine_type    = "e2-standard-2"
    workspace_machine_type = "n2-standard-4"
    sql_availability_type  = "ZONAL"
    managed_prometheus     = false
  }

  assert {
    condition     = google_container_cluster.main.location == "europe-west1-b"
    error_message = "a zonal cluster's location is its zone"
  }

  # One e2-medium leaves the CP Pending next to kube-system (measured); the profile must not
  # drift back to it.
  assert {
    condition     = google_container_node_pool.system.node_config[0].machine_type == "e2-standard-2"
    error_message = "the small profile's system pool must be e2-standard-2"
  }

  assert {
    condition     = google_container_cluster.main.monitoring_config[0].managed_prometheus[0].enabled == false
    error_message = "managed_prometheus = false must reach the cluster"
  }

  assert {
    condition     = google_sql_database_instance.main.settings[0].availability_type == "ZONAL"
    error_message = "sql_availability_type = ZONAL must reach the instance"
  }
}

run "defaults_stay_regional" {
  command = plan

  assert {
    condition     = google_container_cluster.main.location == "europe-west1"
    error_message = "the default is a regional cluster"
  }

  assert {
    condition     = google_container_cluster.main.monitoring_config[0].managed_prometheus[0].enabled == true
    error_message = "managed Prometheus stays on by default"
  }
}

run "zonal_cluster_needs_exactly_one_zone" {
  command = plan

  variables {
    zonal_cluster = true
  }

  expect_failures = [google_container_cluster.main]
}

# `terraform destroy` must not trip over the CP-owned database or Service Networking's
# lingering "in use" (#1732); the runbook's "Tearing down" describes these.
run "destroy_policies_are_set" {
  command = plan

  assert {
    condition     = google_sql_database.agentfleet.deletion_policy == "ABANDON" && google_sql_user.cp.deletion_policy == "ABANDON"
    error_message = "the database and its IAM user go with the instance, not by DROP"
  }

  assert {
    condition     = google_service_networking_connection.private_service_access.deletion_policy == "REMOVE_PEERING"
    error_message = "ABANDON would leave the peering and block the VPC delete"
  }
}

# The egress archive is opt-in: by default no bucket, no grant, no setting for the CP.
run "egress_archive_off_by_default" {
  command = plan

  assert {
    condition     = length(google_storage_bucket.egress_archive) == 0 && length(google_storage_bucket_iam_member.cp_archive_creator) == 0 && length(google_storage_bucket_iam_member.cp_archive_viewer) == 0
    error_message = "the archive creates nothing unless egress_archive is set"
  }

  assert {
    condition     = output.egress_archive_env == "" && output.egress_archive_bucket == ""
    error_message = "the CP gets no AF_EGRESS_ARCHIVE_URL unless egress_archive is set"
  }
}

run "egress_archive_creates_a_private_bucket" {
  command = plan

  variables {
    egress_archive        = true
    egress_archive_prefix = "fleet"
  }

  assert {
    condition     = google_storage_bucket.egress_archive[0].uniform_bucket_level_access == true && google_storage_bucket.egress_archive[0].public_access_prevention == "enforced"
    error_message = "the archive bucket is never public"
  }

  assert {
    condition     = google_storage_bucket.egress_archive[0].force_destroy == false && length(google_storage_bucket.egress_archive[0].lifecycle_rule) == 0
    error_message = "nothing deletes archived days unless egress_archive_retention_days asks"
  }

  assert {
    condition     = google_storage_bucket_iam_member.cp_archive_creator[0].role == "roles/storage.objectCreator" && google_storage_bucket_iam_member.cp_archive_viewer[0].role == "roles/storage.objectViewer"
    error_message = "the CP gets objectCreator and objectViewer on the bucket"
  }

  assert {
    condition     = output.egress_archive_env == "AF_EGRESS_ARCHIVE_URL=gs://af-example-egress-archive-example-project/fleet"
    error_message = "cp.env line names the created bucket and the prefix"
  }
}

run "egress_archive_existing_bucket_is_not_managed" {
  command = plan

  variables {
    egress_archive                 = true
    egress_archive_existing_bucket = "my-archive"
  }

  assert {
    condition     = length(google_storage_bucket.egress_archive) == 0 && google_storage_bucket_iam_member.cp_archive_viewer[0].bucket == "my-archive"
    error_message = "an existing bucket gets the grants only"
  }

  assert {
    condition     = output.egress_archive_env == "AF_EGRESS_ARCHIVE_URL=gs://my-archive"
    error_message = "cp.env line names the existing bucket"
  }
}

run "egress_archive_retention_is_opt_in" {
  command = plan

  variables {
    egress_archive                = true
    egress_archive_retention_days = 400
  }

  assert {
    condition     = length([for r in google_storage_bucket.egress_archive[0].lifecycle_rule : r if one(r.condition).age == 400 && one(r.action).type == "Delete"]) == 1
    error_message = "retention days become an age rule"
  }
}

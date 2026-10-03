locals {
  # Kubernetes object names the manifests use (deploy/kubernetes); the IAM bindings below
  # and the kustomize output name the same ones.
  cp_namespace   = "${var.name_prefix}-cp"
  ws_namespace   = "${var.name_prefix}-ws"
  cp_ksa         = "af-cp"
  storage_class  = "${var.name_prefix}-workspace"
  workload_pool  = "${var.project_id}.svc.id.goog"
  workspace_pool = "workspace"

  # [major, minor] of the floor, compared with the cluster's in its postcondition.
  version_floor = [for p in split(".", var.min_master_version) : tonumber(p)]
}

resource "google_container_cluster" "main" {
  name     = "${var.name_prefix}-gke"
  location = var.region

  node_locations = var.node_zones

  # No min_master_version: GKE creates the cluster at that version, matched as a prefix
  # against what the channel offers today, so a floor written there fails the create once
  # the channel drops it. The cluster starts at the channel's default; the floor is the
  # postcondition below.
  release_channel {
    channel = var.release_channel
  }

  network         = google_compute_network.main.id
  subnetwork      = google_compute_subnetwork.nodes.id
  networking_mode = "VPC_NATIVE"
  ip_allocation_policy {
    cluster_secondary_range_name  = "pods"
    services_secondary_range_name = "services"
  }

  # Dataplane V2 enforces NetworkPolicy; without an enforcing CNI the policies of
  # deploy/kubernetes are accepted and do nothing (ADR 0106 decision 7).
  datapath_provider = "ADVANCED_DATAPATH"

  private_cluster_config {
    enable_private_nodes    = true
    enable_private_endpoint = var.enable_private_endpoint
    master_ipv4_cidr_block  = var.control_plane_cidr
  }

  # Only the operators' networks reach the endpoint, never Google Cloud's public ranges
  # (which would include other customers' VMs) and never the pod range.
  master_authorized_networks_config {
    gcp_public_cidrs_access_enabled = false
    dynamic "cidr_blocks" {
      for_each = var.authorized_networks
      content {
        cidr_block   = cidr_blocks.value.cidr_block
        display_name = cidr_blocks.value.display_name
      }
    }
  }

  database_encryption {
    state    = "ENCRYPTED"
    key_name = google_kms_crypto_key.gke_secrets.id
  }

  workload_identity_config {
    workload_pool = local.workload_pool
  }

  gateway_api_config {
    channel = "CHANNEL_STANDARD"
  }

  # NodeLocal DNSCache answers a pod's lookups from the node, and the workspace namespace's
  # DNS policy admits kube-dns pods only, so with the cache on every lookup is dropped
  # (P11). GKE enables it by default on new clusters. Changing it recreates the nodes.
  addons_config {
    dns_cache_config {
      enabled = false
    }
  }

  # The kubelet's read-only port serves pod data unauthenticated, and NetworkPolicy
  # always lets a pod reach its own node.
  node_pool_defaults {
    node_config_defaults {
      insecure_kubelet_readonly_port_enabled = "FALSE"
    }
  }

  maintenance_policy {
    recurring_window {
      start_time = var.maintenance_window.start_time
      end_time   = var.maintenance_window.end_time
      recurrence = var.maintenance_window.recurrence
    }
  }

  # GKE creates this default pool before Terraform removes it. It runs as the nodes'
  # own service account too: the Compute Engine default account may be disabled or hold no
  # role, and the cluster would then fail before the real pools exist.
  remove_default_node_pool = true
  initial_node_count       = 1
  node_config {
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  deletion_protection = true

  lifecycle {
    # Clusters created while min_master_version was set keep it in state; the provider
    # never reads it back and an unset value upgrades nothing, so a diff would be noise.
    ignore_changes = [min_master_version]

    # P1 / ADR 0106 decision 5: the PersistentVolume deletion-protection finalizer that
    # Destroy relies on. Checked on every plan of an existing cluster and after a create.
    postcondition {
      # master_version reads like "1.35.8-gke.1000000".
      condition = (
        tonumber(split(".", self.master_version)[0]) > local.version_floor[0] ||
        (tonumber(split(".", self.master_version)[0]) == local.version_floor[0] &&
        tonumber(split(".", self.master_version)[1]) >= local.version_floor[1])
      )
      error_message = "The control plane runs ${self.master_version}, below min_master_version ${var.min_master_version} (ADR 0106 decision 5). Pick a release channel that offers it, or upgrade the cluster."
    }
  }

  depends_on = [
    google_kms_crypto_key_iam_member.gke_secrets,
    google_project_iam_member.nodes_default_role,
  ]
}

# The CP and the cluster's own pods.
resource "google_container_node_pool" "system" {
  name       = "system"
  cluster    = google_container_cluster.main.id
  node_count = var.system_node_count

  node_config {
    machine_type    = var.system_machine_type
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    labels = {
      "agent-fleet.io/pool" = "system"
    }
    workload_metadata_config {
      mode = "GKE_METADATA"
    }
    shielded_instance_config {
      enable_secure_boot = true
    }
    kubelet_config {
      insecure_kubelet_readonly_port_enabled = "FALSE"
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# Workspaces only (AF_K8S_NODE_SELECTOR=agent-fleet.io/pool=workspace). GKE_METADATA is
# what keeps the node's credentials from every pod here (ADR 0106 decision 7).
resource "google_container_node_pool" "workspace" {
  name    = local.workspace_pool
  cluster = google_container_cluster.main.id

  autoscaling {
    min_node_count = var.workspace_min_nodes
    max_node_count = var.workspace_max_nodes
  }

  node_config {
    machine_type    = var.workspace_machine_type
    disk_size_gb    = var.workspace_boot_disk_gb
    disk_type       = "pd-balanced"
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    labels = {
      "agent-fleet.io/pool" = local.workspace_pool
    }
    workload_metadata_config {
      mode = "GKE_METADATA"
    }
    shielded_instance_config {
      enable_secure_boot = true
    }
    # Node disk (ADR 0106 decision 13): logs capped and rotated, and the kubelet starts
    # evicting before the disk is full rather than at the hard threshold.
    kubelet_config {
      insecure_kubelet_readonly_port_enabled = "FALSE"
      container_log_max_size                 = "50Mi"
      container_log_max_files                = 3
      eviction_soft {
        nodefs_available  = "15%"
        imagefs_available = "15%"
      }
      eviction_soft_grace_period {
        nodefs_available  = "1m"
        imagefs_available = "1m"
      }
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

variable "project_id" {
  description = "The Google Cloud project the deployment lives in."
  type        = string
}

variable "region" {
  description = "Region of the cluster, Cloud SQL, Cloud NAT and the subnet."
  type        = string
}

variable "node_zones" {
  description = "Zones the node pools use. A workspace's volumes are zonal, so a workspace starts only in the zone its volumes were created in (ADR 0106 decision 4)."
  type        = list(string)
}

variable "name_prefix" {
  description = "Per-deployment prefix of every resource name, and of the two namespaces. Workload Identity treats equal namespace and service account names in any cluster of the project as one identity, so two deployments in one project must not share it (ADR 0106 decision 7)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,14}[a-z0-9]$", var.name_prefix))
    error_message = "name_prefix: 3-16 characters, lower case letters, digits and '-', starting with a letter. It is part of service account ids, which are limited to 30 characters."
  }
}

# --- network -------------------------------------------------------------------

variable "node_cidr" {
  description = "Primary range of the subnet: the nodes."
  type        = string
  default     = "10.10.0.0/20"
}

variable "pod_cidr" {
  description = "Secondary range for pods."
  type        = string
  default     = "10.20.0.0/14"
}

variable "service_cidr" {
  description = "Secondary range for Services."
  type        = string
  default     = "10.24.0.0/20"
}

variable "control_plane_cidr" {
  description = "The /28 the control plane's private endpoint uses."
  type        = string
  default     = "172.16.0.32/28"
}

variable "private_service_access_cidr" {
  description = "Range reserved for Private Service Access (Cloud SQL's private IP)."
  type        = string
  default     = "10.30.0.0/20"
}

variable "enable_private_endpoint" {
  description = "true: the control plane has no public endpoint at all, and kubectl and this Terraform must run from inside the VPC (or a network peered or connected to it). false: the public endpoint exists but only authorized_networks reach it."
  type        = bool
  default     = false
}

variable "authorized_networks" {
  description = "CIDRs allowed to reach the control-plane endpoint, typically the operators' egress addresses. Must not include the pod range. An empty list with enable_private_endpoint = false leaves the public endpoint reachable from no one."
  type = list(object({
    cidr_block   = string
    display_name = string
  }))
  default = []
}

# --- cluster -------------------------------------------------------------------

variable "zonal_cluster" {
  description = "false: a regional cluster (control plane replicated across the region, nodes in every node_zones zone). true: a zonal cluster in the one zone of node_zones, which falls under GKE's free tier for the management fee and has a single-zone control plane. Changing it replaces the cluster."
  type        = bool
  default     = false
}

variable "managed_prometheus" {
  description = "Google Cloud Managed Service for Prometheus collection. GKE Standard turns it on for new clusters; false drops its per-sample charge."
  type        = bool
  default     = true
}

variable "min_master_version" {
  description = "Lowest control-plane version accepted, as MAJOR.MINOR. 1.33 is where the PersistentVolume deletion-protection finalizer that Destroy relies on is stable (ADR 0106 decision 5). A check on every plan and apply, not the version the cluster is created at: that is the release channel's default."
  type        = string
  default     = "1.33"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+$", var.min_master_version))
    error_message = "min_master_version is MAJOR.MINOR, for example 1.33."
  }
}

variable "release_channel" {
  description = "GKE release channel."
  type        = string
  default     = "REGULAR"
}

variable "maintenance_window" {
  description = "The recurring window in which GKE may upgrade, as an RFC 5545 RRULE with a start and end time (UTC). Node upgrades outside a planned drain cut live sessions (README, \"Upgrading nodes\")."
  type = object({
    start_time = string
    end_time   = string
    recurrence = string
  })
  default = {
    start_time = "2026-01-04T18:00:00Z"
    end_time   = "2026-01-04T22:00:00Z"
    recurrence = "FREQ=WEEKLY;BYDAY=SA"
  }
}

variable "system_machine_type" {
  description = "Machine type of the system pool, which runs the CP and the cluster's own pods."
  type        = string
  default     = "e2-standard-2"
}

variable "system_node_count" {
  description = "Nodes per zone in the system pool."
  type        = number
  default     = 1
}

variable "workspace_machine_type" {
  description = "Machine type of the workspace pool. A workspace pod must fit on one node with its sizing's limits."
  type        = string
  default     = "n2-standard-8"
}

variable "workspace_min_nodes" {
  description = "Lower bound of the workspace pool's autoscaler, per zone."
  type        = number
  default     = 0
}

variable "workspace_max_nodes" {
  description = "Upper bound of the workspace pool's autoscaler, per zone."
  type        = number
  default     = 4
}

variable "workspace_boot_disk_gb" {
  description = "Boot disk of a workspace node. It holds the images (the workspace image is several gigabytes), container logs, and every pod's /tmp and ephemeral storage."
  type        = number
  default     = 200
}

variable "storage_class_disk_type" {
  description = "Persistent Disk type behind the workspaces' claims (the StorageClass's `type`)."
  type        = string
  default     = "pd-balanced"
}

# --- database ------------------------------------------------------------------

variable "sql_tier" {
  description = "Cloud SQL machine tier."
  type        = string
  default     = "db-custom-1-3840"
}

variable "sql_availability_type" {
  description = "REGIONAL (a standby in another zone) or ZONAL."
  type        = string
  default     = "REGIONAL"
}

variable "sql_backup_retention_days" {
  description = "Automated backups kept, and the point-in-time recovery window in days (at most 7 on the Enterprise edition)."
  type        = number
  default     = 7
}

# --- ingress -------------------------------------------------------------------

variable "dns_zone" {
  description = "Name of the existing Cloud DNS managed zone (in this project) the records go into. The zone is a precondition the operator brings."
  type        = string
}

variable "fqdn" {
  description = "The Console's host name, inside dns_zone, without a trailing dot."
  type        = string
}

variable "preview_domain" {
  description = "Parent of the preview subdomains (AF_PREVIEW_DOMAIN), inside dns_zone, without a trailing dot. Empty: no wildcard certificate or record, path-mode previews only."
  type        = string
  default     = ""
}

# --- images --------------------------------------------------------------------

variable "artifact_registry_repository" {
  description = "The Artifact Registry repository the images are pulled from, as location and repository id. null when the images come from a public registry; the reader grants are then skipped."
  type = object({
    location   = string
    repository = string
  })
  default = null
}

variable "workspace_image" {
  description = "Workspace image tag (AF_K8S_WORKSPACE_IMAGE), written into the kustomize output."
  type        = string
  default     = "ghcr.io/k-k1/agent-fleet/workspace:VERSION"
}

output "cluster_name" {
  value = google_container_cluster.main.name
}

output "get_credentials" {
  description = "Command that points kubectl at the cluster."
  value       = "gcloud container clusters get-credentials ${google_container_cluster.main.name} ${var.zonal_cluster ? "--zone ${var.node_zones[0]}" : "--region ${var.region}"} --project ${var.project_id}${var.enable_private_endpoint ? " --internal-ip" : ""}"
}

output "nat_address" {
  description = "The one address workspaces' and the CP's internet traffic leaves from."
  value       = google_compute_address.nat.address
}

output "lb_address" {
  value = google_compute_global_address.lb.address
}

output "cloud_sql_instance" {
  value = google_sql_database_instance.main.connection_name
}

output "cp_database_user" {
  value = google_sql_user.cp.name
}

# deploy/kubernetes/overlays/<yours>/deployment.yaml, filled in. The image is a tag; the
# CP pins its digest at each start.
output "kustomize_deployment" {
  description = "deployment.yaml for a copy of deploy/kubernetes/overlays/gke."
  value       = <<-EOT
    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: af-deployment
      annotations:
        config.kubernetes.io/local-config: "true"
    data:
      cpNamespace: ${local.cp_namespace}
      wsNamespace: ${local.ws_namespace}
      storageClass: ${local.storage_class}
      workspaceImage: ${var.workspace_image}
      cpGoogleServiceAccount: ${google_service_account.cp.email}
      cloudSqlUser: ${google_sql_user.cp.name}
      cloudSqlInstance: ${google_sql_database_instance.main.connection_name}
      gatewayAddressName: ${google_compute_global_address.lb.name}
      certificateMapName: ${google_certificate_manager_certificate_map.main.name}
  EOT
}

# A line for the overlay's cp.env: empty (the CP's archive stays off) unless egress_archive.
output "egress_archive_env" {
  description = "AF_EGRESS_ARCHIVE_URL=gs://... for cp.env, or empty when egress_archive is false."
  value       = local.archive_on ? "AF_EGRESS_ARCHIVE_URL=${local.archive_url}" : ""
}

output "egress_archive_bucket" {
  description = "The archive bucket's name, or empty when egress_archive is false."
  value       = local.archive_bucket
}

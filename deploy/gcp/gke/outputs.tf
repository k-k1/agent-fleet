output "cluster_name" {
  value = google_container_cluster.main.name
}

output "get_credentials" {
  description = "Command that points kubectl at the cluster."
  value       = "gcloud container clusters get-credentials ${google_container_cluster.main.name} --region ${var.region} --project ${var.project_id}${var.enable_private_endpoint ? " --internal-ip" : ""}"
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
      podCIDR: ${var.pod_cidr}
      serviceCIDR: ${var.service_cidr}
      nodeCIDR: ${var.node_cidr}
      controlPlaneCIDR: ${var.control_plane_cidr}
      cpGoogleServiceAccount: ${google_service_account.cp.email}
      cloudSqlUser: ${google_sql_user.cp.name}
      cloudSqlInstance: ${google_sql_database_instance.main.connection_name}
      gatewayAddressName: ${google_compute_global_address.lb.name}
      certificateMapName: ${google_certificate_manager_certificate_map.main.name}
  EOT
}

# The StorageClass of ADR 0106 decision 4. Each setting is one the adapter requires, and
# the CP checks the class at boot.
resource "kubernetes_storage_class_v1" "workspace" {
  metadata {
    name = local.storage_class
  }
  storage_provisioner = "pd.csi.storage.gke.io"
  # The volume is created in the zone the pod is scheduled to, not before.
  volume_binding_mode = "WaitForFirstConsumer"
  # The home grows (ResizeHome); a claim never shrinks.
  allow_volume_expansion = true
  # Destroy removes the disk with the claim. Retain would let the disk, its data and its
  # bill outlive Destroy.
  reclaim_policy = "Delete"
  parameters = {
    type = var.storage_class_disk_type
  }

  depends_on = [google_container_node_pool.system]
}

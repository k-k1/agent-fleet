# The optional bucket of the CP's raw egress event archive (AF_EGRESS_ARCHIVE_URL). With
# egress_archive = false nothing here exists and the CP is not told about it.
locals {
  archive_on     = var.egress_archive
  archive_create = var.egress_archive && var.egress_archive_existing_bucket == ""
  archive_bucket = !local.archive_on ? "" : local.archive_create ? google_storage_bucket.egress_archive[0].name : var.egress_archive_existing_bucket
  archive_url    = !local.archive_on ? "" : var.egress_archive_prefix == "" ? "gs://${local.archive_bucket}" : "gs://${local.archive_bucket}/${var.egress_archive_prefix}"
}

resource "google_storage_bucket" "egress_archive" {
  count    = local.archive_create ? 1 : 0
  name     = "${var.name_prefix}-egress-archive-${var.project_id}"
  location = var.egress_archive_location == "" ? var.region : var.egress_archive_location

  # Never public, whatever a later ACL or IAM grant says.
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  # Destroy refuses a bucket that still holds archived days: they are the only copy of
  # events the CP has purged.
  force_destroy = false

  # Expiry only when asked for.
  dynamic "lifecycle_rule" {
    for_each = var.egress_archive_retention_days == null ? [] : [var.egress_archive_retention_days]
    content {
      condition {
        age = lifecycle_rule.value
      }
      action {
        type = "Delete"
      }
    }
  }

  lifecycle {
    precondition {
      condition     = length(var.name_prefix) + length(var.project_id) + 15 <= 63
      error_message = "The archive bucket name \"<name_prefix>-egress-archive-<project_id>\" is over 63 characters: name an existing bucket with egress_archive_existing_bucket."
    }
  }
}

# Bucket-level, never project-level: the CP can write and read back this bucket's objects
# and nothing else. objectCreator writes a new object (the sink never overwrites), and
# objectViewer is what the next run needs to find and verify a day already written; without
# it that read is a 403 and the purge stops at that day.
resource "google_storage_bucket_iam_member" "cp_archive_creator" {
  count  = local.archive_on ? 1 : 0
  bucket = local.archive_bucket
  role   = "roles/storage.objectCreator"
  member = google_service_account.cp.member
}

resource "google_storage_bucket_iam_member" "cp_archive_viewer" {
  count  = local.archive_on ? 1 : 0
  bucket = local.archive_bucket
  role   = "roles/storage.objectViewer"
  member = google_service_account.cp.member
}

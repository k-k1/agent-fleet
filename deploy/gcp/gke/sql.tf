# The CP's store: Postgres on a private IP, reached only through the Cloud SQL Auth Proxy
# beside the CP with automatic IAM database authentication (ADR 0106 decision 8). No
# password exists for the CP's user, so none is in this state or in the cluster.
resource "google_sql_database_instance" "main" {
  name             = "${var.name_prefix}-pg"
  database_version = "POSTGRES_16"
  region           = var.region

  settings {
    edition           = "ENTERPRISE"
    tier              = var.sql_tier
    availability_type = var.sql_availability_type

    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
      ssl_mode        = "ENCRYPTED_ONLY"
    }

    # Backups with point-in-time recovery (ADR 0106 decision 13). An upgrade takes an
    # on-demand backup first as well (README, "Upgrading the CP").
    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      start_time                     = "02:00"
      transaction_log_retention_days = var.sql_backup_retention_days
      backup_retention_settings {
        retained_backups = var.sql_backup_retention_days
      }
    }

    database_flags {
      name  = "cloudsql.iam_authentication"
      value = "on"
    }

    deletion_protection_enabled = true
  }

  deletion_protection = true

  depends_on = [google_service_networking_connection.private_service_access]
}

resource "google_sql_database" "agentfleet" {
  name     = "agentfleet"
  instance = google_sql_database_instance.main.name

  # The CP's IAM user creates and owns this database on first start, so Terraform's
  # DROP fails ("must be owner of database"). It goes away with the instance anyway.
  deletion_policy = "ABANDON"
}

# The IAM database user bound to the CP's service account. It owns nothing yet: the
# runbook's one-time grant gives it the database ("Database bootstrap").
resource "google_sql_user" "cp" {
  name     = trimsuffix(google_service_account.cp.email, ".gserviceaccount.com")
  instance = google_sql_database_instance.main.name
  type     = "CLOUD_IAM_SERVICE_ACCOUNT"

  # Owns the database (see above), so a DROP ROLE fails the same way; dropped with the
  # instance.
  deletion_policy = "ABANDON"
}

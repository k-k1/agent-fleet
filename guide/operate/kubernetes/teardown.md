---
audience: "whoever removes a GKE deployment (an SRE with admin rights on the Google Cloud project and the cluster)"
source_of_truth: "the Terraform under `deploy/gcp/gke/` and the manifests under `deploy/kubernetes/` (they win where this page disagrees)"
---

# Tear down

English | [日本語](teardown.ja.md)

Remove a GKE deployment down to the database, the cluster and the network, and clean up what is left to delete by hand.
The commands assume the shell variables of [Build on GKE](gke.md#shell-variables) and that `kubectl` points at this cluster.

## How it works

| What you do | What happens |
|---|---|
| Destroy every member's workspace in the Console | The disks go with their claims |
| Take backups if you need them | Deleting the overlay deletes the `af-cp-data` disk too, so keep what you want to keep first |
| `kubectl delete -k` the overlay | The namespaces, the CP and its disk go |
| Lift the deletion protections and `terraform destroy` | Cloud SQL, the cluster, the network and the rest go |
| Delete the snapshots and their schedule by hand | Nothing keeps billing |

Cloud SQL, the cluster and the KMS key are protected against deletion on purpose.

## Tear it down

1. Destroy every member's workspace in the Console first, so the disks go with their claims
2. Take the backups of step 2 of [Upgrade the CP](upgrade.md#upgrade) if anything may be wanted later: deleting the overlay deletes the claim `af-cp-data`, and with `reclaimPolicy: Delete` its disk
3. Note the CP's disk first ([Find the CP's disk](backup.md#find-the-cps-disk)): a disk the StorageClass made is named after its volume, not after `af-cp-data`, so only its recorded name finds it. Then delete the overlay and check that no volume or disk of the deployment is left behind (a `Retain` volume from an unfinished restore keeps its disk, named `$PREFIX-cp-data-…`), and delete any that remain
   ```bash
   # first, read PV, HANDLE, ZONE and DISK with the block of "Find the CP's disk"
   kubectl delete -k "$OVERLAY"
   kubectl get pv | grep "$PREFIX-cp"
   gcloud compute disks describe "$DISK" --zone "$ZONE" --project "$PROJECT"   # must answer "not found"
   gcloud compute disks list --project "$PROJECT" --filter="name~$PREFIX-cp-data"   # restored or rehearsal disks
   ```
4. Lift every protection Terraform set, then apply and destroy

| Resource | What to lift |
|---|---|
| The cluster | `deletion_protection = false` |
| The Cloud SQL instance | **Both** `deletion_protection = false` (Terraform's own guard) and `settings.deletion_protection_enabled = false` (the Cloud SQL API's, which refuses the delete on its own) |
| The KMS key | Remove `prevent_destroy` |

```bash
$TF apply
$TF destroy
```

## Settings that keep the destroy from tripping

Three resources are set so the destroy does not trip over Google Cloud behaviour:

| Resource | Setting | Why |
|---|---|---|
| `google_sql_database.agentfleet`, `google_sql_user.cp` | `deletion_policy = "ABANDON"` | The CP's database user owns the database it creates on first start, so Terraform cannot drop either; both are removed with the Cloud SQL instance, and nothing is left of them |
| `google_service_networking_connection.private_service_access` | `deletion_policy = "REMOVE_PEERING"` | Service Networking keeps reporting the connection in use for a long time after the instance is gone; this removes the VPC peering from the network instead, so the VPC delete is not blocked (`ABANDON` would leave the peering in place and block it) |

`ABANDON` skips the API call whenever Terraform deletes the resource, not only in a full destroy.

- Removing the database or user from the configuration, destroying either alone, or renaming one (which replaces it) while the instance stays leaves the old database, its data and the old IAM database user in Cloud SQL, no longer managed by Terraform
- To delete one on purpose, resolve its ownership and dependencies, set `deletion_policy = "DELETE"` and apply first, or clean up by hand (and `terraform import` if it should be managed again)

A deployment created before these settings needs the `$TF apply` above to record them in state before `$TF destroy`. If the VPC delete is still refused because of the peering, delete it by hand and destroy again:

```bash
gcloud compute networks peerings delete servicenetworking-googleapis-com --network "$PREFIX-vpc" --project "$PROJECT"
```

## Seen on a real teardown

On one zonal cluster with the small profile:

- With the deletion policies above, the destroy skipped the database and user (0 s), removed the Cloud SQL instance in about 2.5 minutes, and deleted the service networking connection, the address range, the subnet and the VPC without the long stall and without a manual peering delete
- The first `$TF destroy` can fail once on `google_certificate_manager_certificate_map` ("already being used by" a target HTTPS proxy). The Gateway controller removes that proxy asynchronously after the cluster is gone; run `$TF destroy` again after a minute or so. It depends on timing: of three teardowns it failed on one, and the others finished in a single run
- `terraform init` can also fail once with "connection reset by peer" while downloading a provider; run it again
- `kubectl delete -k` exits with status 1 with `NotFound` for the workspace namespace's network policies when that namespace was deleted first. Nothing is left behind; it is harmless
- The KMS key ring and the key's name cannot be used again after a destroy: the key's versions are only scheduled for destruction (after 30 days, Cloud KMS's default, since `kms.tf` does not set `destroy_scheduled_duration`) and a key ring can never be deleted. Anyone who re-creates a stack with the same `name_prefix` hits an "already exists" error on `kms.tf`; use another prefix, or import the key ring and key into the new state

## What you delete by hand

- KMS keys are not deleted by Google Cloud; only their versions are scheduled for destruction
- The snapshots of the CP's disk and the snapshot schedule outlive the destroy; delete them with `gcloud compute snapshots delete` and `gcloud compute resource-policies delete` once unwanted

Those are the only manual leftovers: the database and user go with the instance, and the reserved private-service-access address range is a Terraform resource that the destroy deletes.

## Related

- [Pause and see the bill](pause.md): lower the bill with the data kept
- [Back up and restore](backup.md)
- [Use idle stop and force-stop](../run/idle-stop.md)

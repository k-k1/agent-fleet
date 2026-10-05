# deploy/gcp/gke — Terraform around a GKE cluster

> **Preview.** The `kubernetes` runtime on GKE is a preview, not yet supported for production;
> its known limits and what is still being measured are in the runbook's opening note.

What a GKE deployment of the `kubernetes` runtime profile needs outside the cluster's own
manifests: the VPC, Cloud NAT with a static address, the GKE Standard cluster (Dataplane V2,
Secrets encrypted with Cloud KMS, a private or authorised-network control plane, a workspace
node pool on the GKE metadata server), the StorageClass, Cloud SQL on a private IP, the load
balancer's address with its Certificate Manager certificate and DNS records, and the IAM grants
([ADR 0106](../../../docs/decisions/0106-kubernetes-runtime.md) decision 12).

The procedure — preconditions, `terraform init` with your state bucket, the database grant,
the manifests, and day-2 operations — is the runbook,
[deploy/kubernetes/README.md](../../kubernetes/README.md).

| File | What |
|---|---|
| `versions.tf` | Terraform and provider versions, the `gcs` backend (bucket given at `init`) |
| `variables.tf` | Inputs; `terraform.tfvars.example` shows a filled-in set |
| `network.tf` | VPC, subnet with the pod and service ranges, Cloud Router and NAT, Private Service Access |
| `kms.tf` | The key that encrypts the cluster's Secrets |
| `gke.tf` | The cluster and its `system` and `workspace` node pools |
| `storage.tf` | The workspaces' StorageClass |
| `sql.tf` | Cloud SQL for Postgres, the database, the CP's IAM database user |
| `lb.tf` | The global address, Certificate Manager, the DNS records |
| `iam.tf` | Service accounts and grants, each to one principal on one resource |
| `outputs.tf` | Includes `kustomize_deployment`, the `deployment.yaml` for the overlay |
| `offline-tests/` | `terraform test` against mocked providers: the version floor and the DNS cache setting of the cluster |

The tests need Terraform 1.11 or later (`mock_provider`, and `state_key` in a run block); the
module itself runs on 1.6, which is why they are not in `tests/`, the directory every
`terraform init` parses. Run them from this directory:

```bash
terraform init -backend=false -test-directory=offline-tests   # fetches the providers once
terraform test -test-directory=offline-tests                  # no project, credentials or network
```

## Cost

**Measured on the acceptance cluster** ([#1468](https://github.com/k-k1/agent-fleet/issues/1468)),
from the Cloud Billing detailed export: **asia-northeast1**, list prices, **JPY, before
credits**, 2026-10. Per-day figures are the
hourly rate of a clean window × 24 — running: four hours with the CP up and no workspace node;
paused: sixteen hours with everything below at zero. Other regions and currencies scale with
their rates.

The shape measured is this module's defaults: a regional cluster with nodes in two zones; the
system pool at one e2-standard-2 per zone; the workspace pool n2-standard-8, autoscaled 0–4 per
zone, with a 200 GB pd-balanced boot disk; Cloud SQL `db-custom-1-3840` `REGIONAL` (HA) with
10 GB SSD; the global external Application Load Balancer; Cloud NAT.

| Item | Running, no workspace (¥/day) | Paused (¥/day) |
|---|---:|---:|
| System pool, e2-standard-2 ×2 (core + RAM) | 649 | 0 |
| Cloud SQL HA, vCPU + RAM | 663 | 0 |
| Cloud SQL storage (regional) | 22 | 22 |
| GKE regional cluster management fee | 377 | 377 |
| pd-balanced capacity (node boot disks while running; the claims) | 181 | 49 |
| Load balancer forwarding rule minimum (global) | 94 | 94 |
| Private Service Connect partner endpoint | 38 | 38 |
| Cloud NAT address and gateway | 30 | 19 |
| Managed Service for Prometheus samples | 58 | 0 |
| Network Intelligence Center | 23 | 0 |
| DNS, KMS, Secret Manager, BigQuery, Cloud Storage | ~1 | ~1 |
| **Total** | **~2,140** | **~600** |

Over 30 days that is **~¥64,000 running with no workspace** and **~¥18,000 paused**, of which
the management fee alone is ~¥11,300.

**A running workspace** puts a workspace node up: one n2-standard-8 is **~¥78/hour** (measured:
8 core-hours at ¥6.385 plus 32 GiB-hours at ¥0.852), plus its 200 GB boot disk while the node
exists. The node is billed whole, whether one workspace runs on it or as many as its allocatable
resources hold; the pool goes back to zero nodes when none runs. Every member's two claims
(50 GB home, 5 GB state, pd-balanced) bill **always**, stopped or not, as an EBS home does on
ECS. A full acceptance day with a workspace node up about 18.5 hours came to ¥2,325.

Not measured: egress (negligible in the test), Cloud Logging volume under real use, and the
growth of the CP disk's snapshots.

**Against ECS.** The ECS deployment's standing cost is ~$107/month
([deploy/aws/ecs, "Cost & ephemerality"](../../aws/ecs/README.md#cost--ephemerality), Tokyo
region): the GKE defaults cost about four times that idle. The gap is the high-availability
choices, not Kubernetes itself — Cloud SQL `REGIONAL`, a system node in each of two zones, the
regional cluster's management fee (one zonal cluster per billing account falls under GKE's free
tier) — and the workspace pool's whole-node billing, where ECS bills each workspace's own
Fargate task (1 vCPU + 2 GB). Some of it is already a variable (`sql_availability_type`,
`sql_tier`, `system_machine_type`, `node_zones`, `workspace_machine_type`; a workspace pod must
still fit on one node); the cluster itself is always regional. A small, zonal profile and its
estimate are [#1728](https://github.com/k-k1/agent-fleet/issues/1728).

**Paused** means the CP scaled to 0, both node pools at 0 nodes and Cloud SQL stopped; the data
is kept. What still bills is in the right-hand column: the management fee, the forwarding rule,
the Private Service Connect endpoint, the NAT address, the disks (the claims and the CP's own)
and Cloud SQL's storage. Only deleting them stops those. The procedure, and its trap — the
workspace pool has no taint, so a system pool at 0 lets the autoscaler start a workspace node
for the cluster's own pods — is [#1639](https://github.com/k-k1/agent-fleet/issues/1639).

# deploy/gcp/gke — Terraform around a GKE cluster

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
| `tests/` | `terraform test` against mocked providers (no project, no credentials): the version floor and the DNS cache setting of the cluster |

`terraform init -backend=false && terraform test` runs the tests offline.

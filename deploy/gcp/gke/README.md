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

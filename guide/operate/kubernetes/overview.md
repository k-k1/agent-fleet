---
audience: "whoever stands up and runs Agent Fleet on a Kubernetes cluster (an SRE with admin rights on the cluster and the cloud)"
source_of_truth: "the manifests under `deploy/kubernetes/` and the Terraform under `deploy/gcp/gke/` (they win where this page disagrees)"
---

# The Kubernetes deployment at a glance

English | [日本語](overview.ja.md)

Before you run Agent Fleet on a Kubernetes cluster, learn what gets built where and what the cluster itself has to guarantee.
The Control Plane (CP) runs as a Deployment in the cluster, and each workspace is a StatefulSet at 0 or 1 replicas with two PersistentVolumeClaims (claims).

> [!WARNING]
> **Preview.** Accepted on a GKE Standard cluster, not yet supported for production. Known limits: no browser features on this runtime (below), and the regional SSD disk quota on GKE ([Disk quota (GKE)](#disk-quota-gke)).

## How it works

| What you do | What happens |
|---|---|
| Apply the Terraform in `deploy/gcp/gke` (on GKE) | The VPC, subnet and Cloud NAT with a static address, the cluster and its two node pools, the KMS key for Secrets, Cloud SQL, the load balancer's address, certificate and DNS records, the StorageClass and the IAM grants are created |
| `kubectl apply -k` the overlay | The two namespaces, the CP and its Services, the CP's own disk (`af-cp-data`), RBAC, NetworkPolicies, quota, the Gateway and its policies are created |
| A member starts a workspace in the Console | The CP creates, per workspace, a StatefulSet, a Service, a Secret and two claims, and now and then a one-shot erase pod — all labelled `agent-fleet.io/workspace=<name>` |

## What the directories hold

| Path | What it is |
|---|---|
| `deploy/kubernetes/base/` | Plain manifests for any cluster: the two namespaces, the CP's Deployment, Services, service account and RBAC, the workspace NetworkPolicies, an example ResourceQuota and LimitRange |
| `deploy/kubernetes/components/params/` | Writes the values of an overlay's `deployment.yaml` (namespaces, address ranges, StorageClass, image) into the manifests |
| `deploy/kubernetes/components/gke/` | What GKE adds: the Cloud SQL Auth Proxy sidecar, Workload Identity, the workspace node pool, the global external Application Load Balancer (Gateway API) and the CP namespace's ingress policy |
| `deploy/kubernetes/overlays/gke/` | The overlay to copy for a GKE deployment |
| `deploy/kubernetes/overlays/generic/` | The overlay to copy for any other cluster |
| `deploy/gcp/gke/` | Terraform for everything around a GKE cluster |

## No browser features

Chromium's sandbox cannot start in a workspace pod (Pod Security `restricted`), and running it unsandboxed is not done either.

- The CP says so for every workspace it starts here (`AF_BROWSER_UNAVAILABLE`)
- The Console greys out the browser pane with the reason; the Agent's browser routes and the af browser tools answer `browser_unavailable`
- `workspace-agent browser-smoke` fails with that explanation instead of Chromium's EOF
- The lightweight preview still works
- Nothing on the cluster side to configure; do not add a seccomp profile or capability to work around it

## Preconditions

None of these can be checked by the CP; a cluster that misses one runs, and is not isolated. On GKE, Terraform meets every item below.

| # | Precondition | What breaks without it |
|---|---|---|
| P1 | Kubernetes **1.33 or later**. Terraform creates the cluster at its release channel's default version and checks this floor (`min_master_version`) on every plan | The PersistentVolume deletion-protection finalizer Destroy relies on is stable from 1.33 |
| P2 | A CNI that **enforces NetworkPolicy** (Dataplane V2 on GKE) | The policies are accepted and enforce nothing |
| P3 | A StorageClass with `volumeBindingMode: WaitForFirstConsumer`, `allowVolumeExpansion: true`, `reclaimPolicy: Delete`, backed by **block storage** (not NFS / Filestore) | With `Retain` the disk and its bill outlive Destroy |
| P4 | **Application-layer encryption of Secrets** (Cloud KMS on GKE) | Every workspace's DEK and tokens are in a Secret |
| P5 | The control-plane endpoint is **private, or limited by authorised networks that exclude the pod range**. A public endpoint open to all is not supported | Workspaces reach the API server |
| P6 | The kubelet's **read-only port is off** (GKE's default, and Terraform sets it), and **no `hostNetwork` service** runs on workspace nodes | A pod can always reach its own node |
| P7 | On GKE, **every node pool that can run a workspace uses `GKE_METADATA`**, and workspaces are scheduled only there (`AF_K8S_NODE_SELECTOR`) | Workspaces reach the node's credentials |
| P8 | **No IAM grant names the workspace namespace or its service account**, directly or through a `principalSet`, and the namespace names carry this deployment's own prefix | Workload Identity treats equal names in any cluster of the project as one identity |
| P9 | **Members have no Kubernetes API access** to the workspace namespace | Creating pods there is reading every member's home and Secret |
| P10 | The cluster is **IPv4 single-stack** | The egress policy allows no `::/0`, so IPv6 egress is denied rather than unfiltered |
| P11 | **NodeLocal DNSCache is off** (GKE turns it on for new clusters; Terraform sets it off), or a policy for its address is added | The DNS policy allows `kube-dns` pods only and `169.254.0.0/16` is denied, so with the cache on every lookup from a workspace fails |
| P12 | A **Postgres** the deployment provides (Cloud SQL on GKE) | The CP's database |
| P13 | **A DNS zone** for the Console's name, and a **state bucket** for Terraform, both the operator's | Terraform needs them for the certificate and its state |
| P14 | `AF_MASTER_KEY` is generated and kept **outside the database and its backups** | As on every target, losing it is a crypto-shred |
| P15 | The **node, pod, service and control-plane ranges lie inside RFC 1918 or `100.64.0.0/10`** (Terraform checks it on every plan) | Below |

On P15:

- The egress policy allows every address outside those ranges and `169.254.0.0/16`, as fixed blocks with no `except`; `deploy/kubernetes/egress-blocks.py` writes them
- On Dataplane V2 any allowed block that contains a node address lets workspaces reach every node, own and others, and an `except` does not stop it
- A cluster with a range elsewhere (privately used public addresses, GKE's default `34.118.224.0/20` Service range) must cut that range out of the blocks itself

Changing P11 on an existing cluster takes about 10 minutes and may recreate the nodes depending on the GKE version. On 1.35 it was seen to remove the `node-local-dns` pods and recreate no node and restart no workspace.

## Disk quota (GKE)

Every node's boot disk and every claim is a `pd-balanced` disk, and `pd-balanced` counts against the regional **Persistent Disk SSD quota** (`SSD_TOTAL_GB`), which is 500 GB per region on a fresh project.

When the quota is used up the autoscaler cannot add a node: the scale-up fails with `QUOTA_EXCEEDED` and the workspace pod stays `Pending`.

A steady-state estimate of what the deployment can reach, from the Terraform variables (`deploy/gcp/gke/variables.tf`), the CP's claim sizes and the default `pd-balanced`:

```
zones x (system_node_count x 100 + workspace_max_nodes x workspace_boot_disk_gb)
  + 20                                    the CP's disk (af-cp-data)
  + workspaces x (AF_K8S_HOME_GIB + AF_K8S_STATE_GIB)    50 + 5 by default, more after a resize
  + 20 during a disk rehearsal            the restored copy of the CP's disk
```

- The system pool's 100 GB is GKE's default boot disk, which Terraform does not change
- With the defaults and two zones (`system_node_count = 1`, `workspace_max_nodes = 4` per zone, `workspace_boot_disk_gb = 200`) the nodes alone can reach 2 x (100 + 4 x 200) = 1800 GB
- On a fresh project 500 GB is already gone with two system nodes, one workspace node and one workspace (2 x 100 + 200 + 20 + 55 = 475 GB): the second workspace node never comes

Raise the quota before going live, or lower `workspace_max_nodes` / `workspace_boot_disk_gb`, and leave headroom above the sum rather than a quota that fits it exactly.

| Why headroom | What |
|---|---|
| Node upgrades | Both pools upgrade automatically, and a surge upgrade adds a node per zone (GKE's default surge is one) with that pool's boot disk; with no room for it the upgrade fails with `QUOTA_EXCEEDED` |
| Other disks | Other deployments and VMs in the region count against the same quota |
| Stopped workspaces | They keep their claims, so count every workspace, not the running ones |

With another `storage_class_disk_type` the claims count against that type's quota instead (for example `pd-standard` against `DISKS_TOTAL_GB`).

Check it:

```bash
gcloud compute regions list --project <project> --flatten=quotas \
  --filter='name=<region> AND quotas.metric=SSD_TOTAL_GB' \
  --format='table(name,quotas.metric,quotas.usage,quotas.limit)'
```

Raise it on the Cloud Console's Quotas page (IAM & Admin > Quotas & system limits, "Persistent Disk SSD (GB)" for the region), or through the Cloud Quotas API:

```bash
gcloud services enable cloudquotas.googleapis.com --project <project>
gcloud beta quotas preferences create --project <project> --service compute.googleapis.com \
  --quota-id SSD-TOTAL-GB-per-project-region --dimensions region=<region> \
  --preferred-value <GB> --preference-id <prefix>-ssd-total-gb \
  --justification "Agent Fleet on GKE: node boot disks and workspace volumes"
gcloud beta quotas preferences describe <prefix>-ssd-total-gb --project <project>   # granted value
```

An increase to 2000 GB has been approved within a minute; that is not a promise, and a larger request may go to review.

## Tools on your machine

| Tool | For |
|---|---|
| `gcloud` with the `gke-gcloud-auth-plugin` component | `kubectl` needs it to sign in to GKE (`gcloud components install gke-gcloud-auth-plugin`) |
| `terraform` 1.6 or later | `deploy/gcp/gke` |
| `kubectl` | Its built-in kustomize is enough |
| `psql` | The one-time database grant |

## Rules for changing the manifests

- **Never add a NetworkPolicy to the workspace namespace that is broader than the ones here.** Policies only add: one that allows more to "all pods" undoes the isolation. A private destination a deployment needs (a LAN engine, an internal git host) gets one policy per destination, selecting the workspace pods, naming that address and port
- **Never widen the CP's Role or ClusterRole**, and never give it rights on Namespaces or NetworkPolicies. A compromised CP reaches what its role reaches
- **Never add the main port (8099) to `af-cp-internal`.** The internal Service is the workspaces' way back to the CP and must reach the workspace-only listener alone
- **Never lower the workspace namespace's Pod Security level.** If a workspace feature needs more than `restricted`, it is not offered on this target (the Chromium sandbox's `SYS_ADMIN` is one)
- **Never force-delete a workspace pod** (`--force --grace-period=0`). It frees the name without proof the process stopped, and two agents can then share one home. What to do for a node that stopped answering is in [Replace nodes and recover from a stopped node](nodes.md#a-node-that-stopped-answering)

## Pages in this group

The commands on the operating pages assume the shell variables of the GKE page (`PROJECT`, `REGION`, `PREFIX`, `TF`) and that `kubectl config current-context` is this cluster ([Build on GKE](gke.md#3-point-kubectl-at-this-cluster)).

| Page | What you can do |
|---|---|
| [Build on GKE](gke.md) | Stand it up on GKE, from Terraform to checking the load balancer |
| [Build on another cluster](other-clusters.md) | Stand it up on a cluster other than GKE from the generic overlay |
| [Upgrade the CP](upgrade.md) | Take backups, move to a new version, and go back if you must |
| [Back up and restore](backup.md) | Know what has to survive, schedule snapshots, and rehearse a restore |
| [Set up alerts](alerts.md) | Put the watches into Cloud Monitoring |
| [Replace nodes and recover from a stopped node](nodes.md) | Replace nodes as planned, recover from a node that stopped answering, clean up what Destroy left |
| [Pause and see the bill](pause.md) | Put it to sleep with its data kept, and know the shape of the bill |
| [Tear down](teardown.md) | Remove the deployment and clean up what is left by hand |

## Related

- [Choose where to deploy](../choose.md)
- [Deployment targets — what exists where](../../ref/deploy-targets.md)
- [Handle the deployment keys](../secure/deployment-keys.md)

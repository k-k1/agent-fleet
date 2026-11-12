---
audience: "whoever manages the cost of a GKE deployment (an SRE with admin rights on the Google Cloud project and the cluster)"
source_of_truth: "`deploy/gcp/gke/pause.sh` and the Terraform under `deploy/gcp/gke/` (they win where this page disagrees)"
---

# Pause and see the bill

English | [日本語](pause.ja.md)

Put an idle GKE deployment to sleep with its data kept, and wake it again. This page also shows what the bill is made of and what keeps billing while paused.
The commands assume the shell variables of [Build on GKE](gke.md#shell-variables) and that `kubectl` points at this cluster.

## How it works

| What you do | What happens |
|---|---|
| `pause.sh --status` | Prints what is running and what still bills; changes nothing |
| `pause.sh --yes` | The CP to 0 replicas, the workspace pods gone, the workspace pool to 0 nodes, the system pool to 0, Cloud SQL stopped |
| `pause.sh --up` | Reverses it: Cloud SQL, system pool, workspace pool autoscaling, CP |

## Pause

`deploy/gcp/gke/pause.sh` does it in the order that matters, in a shell whose kubectl points at this cluster ([3. Point kubectl at this cluster](gke.md#3-point-kubectl-at-this-cluster)):

```bash
deploy/gcp/gke/pause.sh --project "$PROJECT" --location "$LOCATION" --prefix "$PREFIX" --status
deploy/gcp/gke/pause.sh --project "$PROJECT" --location "$LOCATION" --prefix "$PREFIX" --yes
deploy/gcp/gke/pause.sh --project "$PROJECT" --location "$LOCATION" --prefix "$PREFIX" --up
```

| Option | Meaning |
|---|---|
| `--project` | Google Cloud project (Terraform's `project_id`) |
| `--location` | The cluster's location: its region, or its zone for a zonal cluster (`zonal_cluster = true`) |
| `--prefix` | Terraform's `name_prefix` |
| `--status` | Print what is running and what still bills; change nothing |
| `--yes` | Actually pause. Without it, the pause only prints the plan |
| `--up` | Resume. Starts at once, with no `--yes` |
| `--dry-run` | Print every write without making it (reads still run) |
| `--stop-workspaces` | Scale the running workspaces to 0 (their sessions end). Without it a running workspace makes the pause refuse |
| `--keep-db` | Leave Cloud SQL running (it is stopped by default) |
| `--system-nodes N` | (`--up`) System nodes per zone when no record exists (Terraform's `system_node_count`) |
| `--workspace-min N` / `--workspace-max N` | (`--up`) Autoscaling minimum and maximum per zone when no record exists (default 0 and 4) |
| `--wait SEC` | How long to wait for workspace pods to go away (default 900) |

- It stops before any write when a read fails, or when the kubectl context's server and CA are not this cluster's. Run `get-credentials` first; a DNS-endpoint context is refused
- Cloud SQL, unlike RDS, does not restart itself after 7 days

### What happens while paused

- **The workspace pool has no taint.** With the system pool at 0 the autoscaler would start a workspace node for kube-system's pending pods, so the script switches that pool's autoscaling off and resizes it to 0 itself
- While paused, `terraform plan` shows the pool's autoscaling and the system pool's size as drift; `--up` closes it
- The node counts (per zone) are read from the pools' instance groups and kept in one annotation step on the CP namespace (`agent-fleet.io/pause-*`). An existing record is never overwritten, so pausing again after an interruption keeps the original sizes
- `--up` restores them in full, finishing a half-done resize, and removes the record once the CP is back. If the namespace was deleted, `--up` needs `--system-nodes N` (and optionally `--workspace-min` / `--workspace-max`)

### What keeps billing while paused

Only deletion stops these:

- the GKE management fee
- the load balancer's forwarding rule
- the Private Service Connect endpoint
- Cloud NAT and its reserved address
- persistent disks (every workspace's two claims, the CP's `af-cp-data`)
- Cloud SQL storage

To delete them, [Tear down](teardown.md).

## The shape of the bill

| Part | What |
|---|---|
| **A floor** | The cluster management fee, the system pool, Cloud SQL, Cloud NAT and its address, the load balancer |
| **Per-workspace capacity while running** | The workspace pool scales with the running workspaces |
| **Two persistent disks per workspace** | They bill while stopped, as an EBS home does |

A cluster you already run removes the cluster from the floor.

## What the defaults cost

Measured with `deploy/gcp/gke`'s defaults: asia-northeast1, list prices, JPY before credits. Per-day figures are the hourly rate of a clean window × 24 (running: four hours with the CP up and no workspace node; paused: sixteen hours with everything below at zero). Other regions and currencies scale with their rates.

The shape measured:

- a regional cluster with nodes in two zones
- the system pool at one e2-standard-2 per zone
- the workspace pool n2-standard-8, autoscaled 0–4 per zone, with a 200 GB pd-balanced boot disk
- Cloud SQL `db-custom-1-3840` `REGIONAL` (HA) with 10 GB SSD
- the global external Application Load Balancer, Cloud NAT

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

Over 30 days that is **~¥64,000 running with no workspace** and **~¥18,000 paused**, of which the management fee alone is ~¥11,300.

**A running workspace** puts a workspace node up.

- One n2-standard-8 is **~¥78/hour** (8 core-hours at ¥6.385 plus 32 GiB-hours at ¥0.852), plus its 200 GB boot disk while the node exists
- The node is billed whole, whether one workspace runs on it or as many as its allocatable resources hold; the pool goes back to zero nodes when none runs
- Every member's two claims (50 GB home, 5 GB state, pd-balanced) bill **always**, stopped or not
- A full acceptance day with a workspace node up about 18.5 hours came to ¥2,325

### At creation the workspace pool is not at zero

- The pool is created with a node (the configuration sets no initial size, so GKE's default of one per zone applies; observed, not traced in GKE's documentation)
- The autoscaler removes the idle node only after a delay: on the small profile (one zone) it has scaled to 0 about 12 minutes after the pool came up
- Until then one workspace node bills at the hourly rate above (~¥39/hour for `n2-standard-4`); with nodes in two zones, twice that
- The delay is not a fixed 12 minutes. The node has also still been there after 20 minutes and after 70, because about 15 `kube-system` pods (`kube-dns`, `metrics-server`, `konnectivity-agent` and others) had been scheduled onto it while the system pool was still coming up, and the autoscaler did not move them. The first workspace pod was scheduled onto that node
- Treat it as standing cost for the length of a trial; `pause.sh` takes the pool to 0 explicitly

`kubectl get nodes -L agent-fleet.io/pool` shows whether it has gone.

Not measured: egress (negligible in the test), Cloud Logging volume under real use, and the growth of the CP disk's snapshots.

### Against ECS

- The ECS deployment's standing cost is ~$107/month in the Tokyo region. Converted at ¥150 to the dollar (an assumption, not part of the measurement) that is ~¥16,000/month, so the GKE defaults cost roughly four times as much idle
- The gap is the high-availability choices, not Kubernetes itself: Cloud SQL `REGIONAL`, a system node in each of two zones, the regional cluster's management fee (one zonal cluster per billing account falls under GKE's free tier)
- The workspace pool's whole-node billing adds to it, where ECS bills each workspace's own Fargate task (1 vCPU + 2 GB)
- Most of it is a variable (`zonal_cluster`, `sql_availability_type`, `sql_tier`, `system_machine_type`, `node_zones`, `workspace_machine_type`, `managed_prometheus`); a workspace pod must still fit on one node

## What the small profile costs

An estimate for [A small profile for a trial](gke.md#a-small-profile-for-a-trial), from the unit prices measured above (asia-northeast1, JPY), running with no workspace, per day.

- The measured prices are before credits; the Small column assumes GKE's free tier (a monthly credit per billing account) is still unspent, so the management fee is 0
- If it is used up by another zonal or Autopilot cluster, add ~¥377/day

| Item | Defaults | Small | Basis |
|---|---:|---:|---|
| System pool | 649 | ~325 | one e2-standard-2 instead of two |
| Cloud SQL vCPU + RAM | 663 | ~331 | `ZONAL` is half of `REGIONAL` |
| GKE management fee | 377 | 0 | one zonal cluster per billing account is free; a second one pays |
| Managed Prometheus samples | 58 | 0 | off |
| Everything else | 393 | 393 | left unchanged: disks and SQL storage shrink a little, not counted |
| **Total** | **~2,140** | **~1,050** | |

- That is about ¥31,500 per 30 days against ¥64,000, and still roughly twice the ECS standing cost (~¥16,000 at the conversion above)
- Paused, the management fee goes too: about ¥220 per day
- A workspace node on `n2-standard-4` is about ¥39/hour (half of n2-standard-8, as the machine's core-hours and GiB-hours scale)
- The Cloud SQL and system pool figures are scaled from the measured ones, not billed. The Network Intelligence Center charge (~¥23/day) has no variable

## Related

- [Use idle stop and force-stop](../run/idle-stop.md): pausing the deployment and stopping workspaces automatically
- [Tear down](teardown.md)
- [Choose where to deploy](../choose.md)

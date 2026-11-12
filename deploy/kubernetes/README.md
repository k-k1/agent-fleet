# Deploy on Kubernetes (the `kubernetes` runtime profile)

The manifests for running Agent Fleet on a Kubernetes cluster: a kustomize base, the GKE
component, and the overlays to copy (`overlays/gke`, `overlays/generic`). Terraform for what a
GKE deployment needs around the cluster is in [`deploy/gcp/gke/`](https://github.com/k-k1/agent-fleet/tree/main/deploy/gcp/gke). The
procedures are the guide pages below.

| Page | What it covers |
|---|---|
| [The Kubernetes deployment at a glance](../../guide/operate/kubernetes/overview.md) | What each directory holds, what builds what, the preconditions P1–P15, the GKE disk quota, the tools you need, the rules for changing the manifests, no browser features |
| [Build on GKE](../../guide/operate/kubernetes/gke.md) | The project, Terraform, the small profile, kubectl, the database grant, the overlay, the egress archive, the Secret, the load balancer, the checks |
| [Build on another cluster](../../guide/operate/kubernetes/other-clusters.md) | `overlays/generic` on a cluster other than GKE |
| [Upgrade the CP](../../guide/operate/kubernetes/upgrade.md) | Backing up, upgrading, going back, rolling back past the home layout |
| [Back up and restore](../../guide/operate/kubernetes/backup.md) | What has to survive, the snapshot schedule, a disk from a snapshot as a volume, the database and disk restore rehearsals |
| [Set up alerts](../../guide/operate/kubernetes/alerts.md) | Cloud Monitoring alerts, and Destroy's residues in the audit log |
| [Replace nodes and recover from a stopped node](../../guide/operate/kubernetes/nodes.md) | Planned node upgrades, unplanned drains, a node that stopped answering, residue cleanup, node disk |
| [Pause and see the bill](../../guide/operate/kubernetes/pause.md) | `deploy/gcp/gke/pause.sh`, the shape of the bill, the measured cost of the defaults and the small profile |
| [Tear down](../../guide/operate/kubernetes/teardown.md) | Removing a deployment, the Terraform deletion policies, the manual leftovers |

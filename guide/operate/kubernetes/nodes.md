---
audience: "whoever maintains the nodes of a Kubernetes deployment (an SRE with admin rights on the cluster and the cloud)"
source_of_truth: "the manifests under `deploy/kubernetes/` and the CP's kubernetes adapter (they win where this page disagrees)"
---

# Replace nodes and recover from a stopped node

English | [日本語](nodes.ja.md)

Replace nodes without cutting members' sessions by surprise, get workspaces safely back from a node that stopped answering, and clean up what Destroy left behind.
The commands assume the shell variables of [Build on GKE](gke.md#shell-variables) and that `kubectl` points at this cluster.

## How it works

| What happens or what you do | What follows |
|---|---|
| Replace a node as planned | Cordon it, stop its workspaces through the CP, drain it. Members start their workspaces again on their next use |
| An automatic upgrade, auto-repair or preemption deletes the pods | Each running workspace is cut and returns by itself on another node, with no CP action |
| A node stops answering | The CP does not replace the pod. The operator frees it after getting proof from Google Cloud that the VM stopped |
| Destroy writes "NOT deleted" to the audit log | Check what is left and delete it by hand |

The CP has no right over Nodes and needs none for any of this.

## Replace nodes as planned

A drain is not a stop. It deletes a workspace's pod but leaves `replicas: 1`, so the StatefulSet recreates the pod on another node at once: the member's session is cut, the workspace comes back by itself, and its capacity keeps billing. So:

1. **Cordon** the node, so nothing new is placed on it
   ```bash
   kubectl cordon <node>
   ```
2. **Stop its workspaces through the CP**, those still starting included. List them
   ```bash
   kubectl -n "$PREFIX-ws" get pods -l agent-fleet.io/workspace --field-selector spec.nodeName=<node> \
     -o custom-columns=POD:.metadata.name,WORKSPACE:.metadata.labels.agent-fleet\\.io/workspace
   ```
   and use **"Force-stop the workspace"** in each member's detail ([Force-stop a workspace](../../admin/resources/idle-stop.md#force-stop-a-workspace)). Wait until the list is empty: the CP's Stop returns only when the pod is gone
3. **Drain**
   ```bash
   kubectl drain <node> --ignore-daemonsets --delete-emptydir-data
   ```
4. Upgrade or replace the node; **uncordon** it when it returns
   ```bash
   kubectl uncordon <node>
   ```

- Members start their workspaces again on their next use
- Workspace pods carry `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, so the autoscaler never removes a node under a live session; the pool shrinks as workspaces stop

### Hold GKE's automatic upgrades

GKE upgrades nodes by itself inside the maintenance window (`maintenance_window`). To upgrade only by the procedure above, add a maintenance exclusion with the scope "no minor or node upgrades" for the period you want to control, and run the node upgrade yourself:

```bash
gcloud container clusters update "$PREFIX-gke" --region "$REGION" --project "$PROJECT" \
  --add-maintenance-exclusion-name hold-nodes \
  --add-maintenance-exclusion-start <start> --add-maintenance-exclusion-end <end> \
  --add-maintenance-exclusion-scope no_minor_or_node_upgrades
```

## Unplanned drain

An automatic upgrade outside your control, a node auto-repair or a preemption deletes the pods on the node.

- Each running workspace is cut and returns by itself on another node — through starting to running, with no CP action
- Members see their sessions end and reconnect
- Nothing needs doing unless a workspace stays starting; then look at its pod's events

## A node that stopped answering

The CP never replaces a pod on a node that stopped answering: that is the price of "at most one agent per home".

- The workspace stays starting, or stopped with a pod that blocks Start, Clean home and Destroy
- Stop, Recreate and Clean home return errors
- Recovery is the operator's, **and it starts with proof that the old process cannot run**

1. Find the VM behind the node, and confirm **from Google Cloud, not from Kubernetes**, that it is stopped or gone
   ```bash
   gcloud compute instances describe <node> --zone <zone> --project "$PROJECT" --format='value(status)'
   # TERMINATED, or "not found", is proof. RUNNING, or no answer, is not.
   ```
   If the VM is running but unreachable (a network partition), or its state cannot be read, **do nothing**: the agent may still be writing to the home
2. Only then, either delete the Node object (`kubectl delete node <node>`), or mark it out of service so the volumes detach
   ```bash
   kubectl taint nodes <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute
   ```
   Deleting a Node object does not stop its VM, and the taint detaches volumes — which is why step 1 comes first
3. The StatefulSet recreates the pod elsewhere (in the volume's zone). Remove the taint once the node is gone or repaired

### On GKE

Where the node pool has auto-repair enabled (the default; on GKE Standard it can be turned off), leave the VM alone and let auto-repair act.

- GKE starts a repair once a node has reported NotReady for about ten minutes. It then drains the node, waiting up to an hour for the drain, and recreates its VM under the same name ([node auto-repair](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/node-auto-repair), "Repair criteria" and "Node repair process")
- The pod goes with the old node, the workspace's Stop settles, and the member's next start runs again
- To hurry it, stop or delete the node's VM yourself; a stopped VM cannot run the old agent. `instances.stop` on a member of a managed instance group triggers the group's repair, which recreates the VM ([instance group repair](https://docs.cloud.google.com/compute/docs/instance-groups/about-repair), "Automatically repair a failed VM"). GKE then deletes the old Node object and its pods

Where auto-repair is off, or does not act, the procedure above applies as written: proof first, then the taint.

Single observations on GKE 1.35, not guarantees:

| What happened | What was seen |
|---|---|
| A VM stop | An ACPI shutdown of about 110 s, which terminates the pods before the node goes |
| A guest power-off | The instance group recreated it about 6 s later |
| A kubelet stop with the VM running, then an operator's VM stop | The group's repair followed within seconds. It deleted the VM in the same second as the taint, so that run could not show which of the two freed the pod |
| A kubelet stop, left alone | Node auto-repair started 11m15s after NotReady and finished 2m44s later. The workspace's pod was gone 14m37s after the cut |

## Clean up what Destroy left

When Destroy cannot confirm that something is gone, it says so in the audit log (`workspace.destroy … NOT deleted: …`). How to find them is in [Set up alerts](alerts.md#see-what-destroy-left-behind).

| Residue | What to do |
|---|---|
| `pvc:<namespace>/<name>` | `kubectl -n <namespace> get pvc <name>`. If it is stuck terminating, a pod still uses it (`kubectl -n <namespace> get pods -l agent-fleet.io/workspace=<workspace>`): wait for that pod, or follow the procedure above for its node |
| `pv:<name>` | `kubectl get pv <name>`. While it exists, the disk behind it may too: `kubectl get pv <name> -o jsonpath='{.spec.csi.volumeHandle}'` names it. When the volume object is gone, check the disk is too with `gcloud compute disks list --project "$PROJECT" --filter="name~<pv name>"` |
| `statefulset:<namespace>/<name>` | Destroy kept it on purpose: it holds the inventory (as an annotation) of claims and volumes that were not confirmed gone. Clear each one it lists as above, **then** delete it: `kubectl -n <namespace> delete statefulset <name>` |

A periodic sweep catches what nobody read in the audit log:

- disks no node uses: `gcloud compute disks list --project "$PROJECT" --filter="-users:*"`
- volumes in `Released`: `kubectl get pv | grep Released`
- StatefulSets in the workspace namespace whose member no longer exists

## Node disk

Workspace nodes have a large boot disk (`workspace_boot_disk_gb`) because it holds the images, container logs, and every pod's `/tmp` and ephemeral storage.

- Container logs are capped (50 MiB, 3 files)
- The kubelet starts evicting below 15% free
- A pod's ephemeral-storage limit is checked periodically, not enforced as it is written, so a fast writer can still fill the disk before it is evicted. The [disk alert](alerts.md#the-alerts-to-set-up) is what tells you

## Related

- [Set up alerts](alerts.md)
- [The Kubernetes deployment at a glance](overview.md#rules-for-changing-the-manifests): why a workspace pod is never force-deleted
- [Force-stop a workspace](../../admin/resources/idle-stop.md#force-stop-a-workspace)

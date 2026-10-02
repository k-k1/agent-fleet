# Agent Fleet on Kubernetes — the `kubernetes` runtime profile

The runbook for running Agent Fleet on a Kubernetes cluster: the CP as a Deployment in the
cluster, each workspace a StatefulSet at 0 or 1 replicas with two PersistentVolumeClaims.
The design, and the reason behind every rule below, is
[ADR 0106](../../docs/decisions/0106-kubernetes-runtime.md); the decision numbers in this
file refer to it.

> **Status.** Built, not yet applied to a live cluster. GKE Standard is the first and only
> cluster the profile is to be verified on; the acceptance run, including the live harness
> and the reachability probes of decision 7, is tracked in issue #1468. Until it has run,
> treat every step here as unproven.

| Path | What it is |
|---|---|
| `base/` | Plain manifests for any cluster: the two namespaces, the CP's Deployment, Services, service account and RBAC, the workspace NetworkPolicies, an example ResourceQuota and LimitRange |
| `components/params/` | Writes the values of an overlay's `deployment.yaml` (namespaces, address ranges, StorageClass, image) into the manifests |
| `components/gke/` | What GKE adds: the Cloud SQL Auth Proxy sidecar, Workload Identity, the workspace node pool, the global external Application Load Balancer (Gateway API) and the CP namespace's ingress policy |
| `overlays/gke/` | The overlay to copy for a GKE deployment |
| `overlays/generic/` | The overlay to copy for any other cluster |
| [`../gcp/gke/`](../gcp/gke/) | Terraform for everything around a GKE cluster |

What builds what:

| Built by | What |
|---|---|
| Terraform (`deploy/gcp/gke`) | VPC, subnet, Cloud NAT with a static address, the cluster and its two node pools, the KMS key for Secrets, Cloud SQL, the load balancer's address, certificate and DNS records, the StorageClass, the IAM grants |
| These manifests (`kubectl apply -k`) | Namespaces, the CP and its Services, RBAC, NetworkPolicies, quota, the Gateway and its policies |
| The CP, at run time | Per workspace: a StatefulSet, a Service, a Secret, two claims, and now and then a one-shot erase pod — all labelled `agent-fleet.io/workspace=<name>` |

## Preconditions

None of these can be checked by the CP; a cluster that misses one runs, and is not isolated.
Terraform meets every GKE item below.

| # | Precondition | Why (ADR 0106) |
|---|---|---|
| P1 | Kubernetes **1.33 or later** | the PersistentVolume deletion-protection finalizer Destroy relies on is stable from 1.33 (decision 5) |
| P2 | A CNI that **enforces NetworkPolicy** (Dataplane V2 on GKE) | without one the policies are accepted and enforce nothing (decision 7) |
| P3 | A StorageClass with `volumeBindingMode: WaitForFirstConsumer`, `allowVolumeExpansion: true`, `reclaimPolicy: Delete`, backed by **block storage** (not NFS / Filestore) | decision 4; with `Retain` the disk and its bill outlive Destroy (decision 5) |
| P4 | **Application-layer encryption of Secrets** (Cloud KMS on GKE) | every workspace's DEK and tokens are in a Secret (decision 6) |
| P5 | The control-plane endpoint is **private, or limited by authorised networks that exclude the pod range**. A public endpoint open to all is not supported | decision 7 |
| P6 | The kubelet's **read-only port is off** (GKE's default, and Terraform sets it), and **no `hostNetwork` service** runs on workspace nodes | a pod can always reach its own node (decision 7) |
| P7 | On GKE, **every node pool that can run a workspace uses `GKE_METADATA`**, and workspaces are scheduled only there (`AF_K8S_NODE_SELECTOR`) | the node's credentials (decision 7) |
| P8 | **No IAM grant names the workspace namespace or its service account**, directly or through a `principalSet`, and the namespace names carry this deployment's own prefix | Workload Identity treats equal names in any cluster of the project as one identity (decision 7) |
| P9 | **Members have no Kubernetes API access** to the workspace namespace. Creating pods there is reading every member's home and Secret | decision 7 |
| P10 | The cluster is **IPv4 single-stack**. The egress policy allows no `::/0`, so IPv6 egress is denied rather than unfiltered | decision 7 |
| P11 | **NodeLocal DNSCache is off**, or a policy for its address is added. The DNS policy allows `kube-dns` pods only; `169.254.0.0/16` is denied | decision 7 |
| P12 | A **Postgres** the deployment provides (Cloud SQL on GKE) | decision 8 |
| P13 | **A DNS zone** for the Console's name, and a **state bucket** for Terraform, both the operator's | decision 12 |
| P14 | `AF_MASTER_KEY` is generated and kept **outside the database and its backups** | as on every target; losing it is a crypto-shred |

Tools on the operator's machine: `gcloud`, `terraform` (1.6 or later), `kubectl` (its built-in
kustomize is enough), `psql` for the one-time database grant.

## GKE

### 1. The project, once

Enable the APIs Terraform uses, with your own credentials:

```bash
gcloud services enable container.googleapis.com compute.googleapis.com \
  sqladmin.googleapis.com servicenetworking.googleapis.com certificatemanager.googleapis.com \
  dns.googleapis.com cloudkms.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  --project <project>
```

The Cloud DNS managed zone for your domain must already exist in the project (P13). Create the
state bucket if you do not have one, with versioning on:

```bash
gcloud storage buckets create gs://<state-bucket> --project <project> --location <region> \
  --uniform-bucket-level-access
gcloud storage buckets update gs://<state-bucket> --versioning
```

Images: push `control-plane` and `workspace` to an Artifact Registry repository (and set
`artifact_registry_repository`, so the node pool and the CP get reader on it), or pull them
from the public registry (`ghcr.io/k-k1/agent-fleet/…`) and leave it `null`.

### 2. Terraform

```bash
cd deploy/gcp/gke
cp terraform.tfvars.example terraform.tfvars      # fill it in; it is gitignored
terraform init -backend-config="bucket=<state-bucket>" -backend-config="prefix=<name_prefix>"
terraform apply
```

- `name_prefix` names every resource and the two namespaces (`<prefix>-cp`, `<prefix>-ws`).
  Two deployments in one project need two prefixes (P8).
- `authorized_networks` is who may reach the control-plane endpoint. The provider that creates
  the StorageClass talks to that endpoint, so `apply` must run from an authorised network — or,
  with `enable_private_endpoint = true`, from inside the VPC.
- The ranges (`node_cidr`, `pod_cidr`, `service_cidr`, `control_plane_cidr`,
  `private_service_access_cidr`) cannot change after the cluster exists. If one is outside RFC
  1918 or `100.64.0.0/10`, check that the egress policy still excepts it: the four ranges
  Terraform prints are excepted; the Private Service Access range is excepted only through RFC
  1918.
- The certificate is issued once the DNS authorisation records resolve. `gcloud
  certificate-manager certificates describe <prefix>-cert` shows its state; it is usually
  `ACTIVE` within an hour.

What it grants (decision 12; each to one principal on one resource): the CP's service account
gets Cloud SQL client and instance user, conditioned to this instance, and Artifact Registry
reader on the repository; the node service account gets Artifact Registry reader, log writer and
metric writer; GKE's service agent gets encrypt/decrypt on the Secrets key; the CP's Kubernetes
service account `<prefix>-cp/af-cp` may act as the CP's service account. Nothing names the
workspace namespace.

### 3. The database, once

The CP's database user is an IAM user with no password and, at first, no rights. Give it the
database, as the built-in `postgres` user, through a Cloud SQL Auth Proxy on your machine:

```bash
gcloud sql users set-password postgres --instance <prefix>-pg --prompt-for-password
cloud-sql-proxy --private-ip "$(terraform output -raw cloud_sql_instance)" &   # from inside the VPC
psql "host=127.0.0.1 user=postgres dbname=agentfleet" <<SQL
GRANT "$(terraform output -raw cp_database_user)" TO postgres;
ALTER DATABASE agentfleet OWNER TO "$(terraform output -raw cp_database_user)";
ALTER SCHEMA public OWNER TO "$(terraform output -raw cp_database_user)";
SQL
```

The instance has no public IP, so the proxy runs from a machine in the VPC (a short-lived VM,
or Cloud Shell with a VPC connection). Keep the `postgres` password in your vault, not here.

### 4. The overlay

```bash
cp -r deploy/kubernetes/overlays/gke deploy/kubernetes/overlays/<prefix>
terraform -chdir=deploy/gcp/gke output -raw kustomize_deployment \
  > deploy/kubernetes/overlays/<prefix>/deployment.yaml
```

Then edit, in the copy:

- `cp.env`: `PUBLIC_BASE_URL` and `PUBLIC_DOMAIN` (the `fqdn`), `AF_PREVIEW_DOMAIN` (Terraform's
  `preview_domain`, or empty), the sign-in settings, `SUPER_ADMIN_EMAILS`. Keep
  `AF_TRUSTED_PROXY_HOPS=2`: the load balancer appends `<client>, <load balancer>`; add one per
  proxy you put in front of it.
- `kustomization.yaml`: `images:` — the CP image and the release tag.

Keep this copy in your own repository or vault, not in a working copy of this one.

### 5. Secrets

The CP reads one Secret, `af-cp-secrets`, which these manifests never contain. Create it once
the namespace exists:

```bash
kubectl apply -k deploy/kubernetes/overlays/<prefix>
kubectl -n <prefix>-cp create secret generic af-cp-secrets \
  --from-literal=AF_MASTER_KEY="$(cat master-key)" \
  --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
  --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
```

Until it exists the CP pod waits in `CreateContainerConfigError` and starts by itself once it
does. Generate `AF_MASTER_KEY` with `head -c 32 /dev/urandom | base64`, as for compose
([deploy/compose/README.md](../compose/README.md)), and keep its only other copy in your vault
(P14). Other sign-in secrets (`AF_OIDC_<ID>_CLIENT_SECRET`, `GITHUB_OAUTH_CLIENT_SECRET`) go in
the same Secret; rotate one with `kubectl create secret … --dry-run=client -o yaml | kubectl
apply -f -` and `kubectl -n <prefix>-cp rollout restart deployment/af-cp`.

### 6. Check it

```bash
kubectl -n <prefix>-cp rollout status deployment/af-cp
kubectl get ns <prefix>-ws -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'   # restricted
kubectl -n <prefix>-cp get gateway af-cp        # PROGRAMMED True, ADDRESS = lb_address
curl -sS https://<fqdn>/readyz
```

What the CP's account may do — every line must answer as shown:

```bash
SA=system:serviceaccount:<prefix>-cp:af-cp
kubectl auth can-i create statefulsets -n <prefix>-ws --as $SA        # yes
kubectl auth can-i delete pods -n <prefix>-ws --as $SA                # yes (the erase pod)
kubectl auth can-i patch namespaces --as $SA                          # no
kubectl auth can-i create networkpolicies -n <prefix>-ws --as $SA     # no
kubectl auth can-i list storageclasses --as $SA                       # no
kubectl auth can-i get storageclass/<prefix>-workspace --as $SA       # yes
kubectl auth can-i create pods -n <prefix>-cp --as $SA                # no
```

Then sign in, start a workspace, and check that its pod runs on the workspace pool:
`kubectl -n <prefix>-ws get pods -o wide -l agent-fleet.io/workspace`.

## Other clusters

`overlays/generic` is the base with nothing provider-specific. Copy it, and:

- set `deployment.yaml` from your cluster: the namespaces (with your own prefix), a StorageClass
  that meets P3, the pod, service, node and control-plane ranges;
- put the store's DSN in `af-cp-secrets` as `AF_DATABASE_URL`;
- if workspaces must run on particular nodes, set `AF_K8S_NODE_SELECTOR` in `cp.env`; if the
  images need a pull secret, create it in the workspace namespace and set
  `AF_K8S_IMAGE_PULL_SECRET`;
- put your cluster's ingress in front of the Service `af-cp` (port 8099), with WebSockets and a
  long idle timeout, and set `AF_TRUSTED_PROXY_HOPS` to the number of proxies that append to
  `X-Forwarded-For`;
- add, in the CP namespace, a policy like `components/gke/cp-networkpolicy.yaml` that admits
  only your ingress to port 8099 — on a cluster that runs other workloads, anything that
  reaches that port can name any user;
- meet P1–P14 yourself. Nothing else checks them.

## Rules for the manifests

- **Never add a NetworkPolicy to the workspace namespace that is broader than the ones here.**
  Policies only add: one that allows more to "all pods" undoes the isolation. A private
  destination a deployment needs (a LAN engine, an internal git host) gets one policy per
  destination, selecting the workspace pods, naming that address and port.
- **Never widen the CP's Role or ClusterRole**, and never give it rights on Namespaces or
  NetworkPolicies. A compromised CP reaches what its role reaches.
- **Never add the main port (8099) to `af-cp-internal`.** The internal Service is the
  workspaces' way back to the CP and must reach the workspace-only listener alone.
- **Never lower the workspace namespace's Pod Security level.** If a workspace feature needs
  more than `restricted`, it is not offered on this target (the Chromium sandbox's `SYS_ADMIN`
  is one: ADR 0106 open question 1).
- **Never force-delete a workspace pod** (`--force --grace-period=0`). It frees the name
  without proof the process stopped, and two agents can then share one home. The procedure for
  a node that stopped answering is below.

## Operating it (decision 13)

### Upgrading the CP

The CP migrates its database at start and **cannot be downgraded**. The Deployment's strategy is
`Recreate`, so two CPs never run against one database.

1. Take a backup and wait for it:
   ```bash
   gcloud sql backups create --instance <prefix>-pg --description "before <version>"
   ```
2. Set the new tag under `images:` in your overlay and apply:
   ```bash
   kubectl apply -k deploy/kubernetes/overlays/<prefix>
   kubectl -n <prefix>-cp rollout status deployment/af-cp
   ```
3. Going back means **restoring that backup and the previous image together**. The previous image
   on the migrated database is not a rollback:
   ```bash
   kubectl -n <prefix>-cp scale deployment/af-cp --replicas=0
   gcloud sql backups list --instance <prefix>-pg
   gcloud sql backups restore <backup-id> --restore-instance <prefix>-pg
   # set the previous tag in the overlay, then
   kubectl apply -k deploy/kubernetes/overlays/<prefix>
   ```
   `kubectl apply` sets replicas back to 1.

The workspace image follows `workspaceImage` in `deployment.yaml`. A running workspace keeps the
image it started with; the next start pins the tag's current digest, and the Console marks the
workspaces still on an older one as stale.

### Backups, point-in-time recovery and the restore rehearsal

Terraform turns on automated backups and point-in-time recovery (`sql_backup_retention_days`).
The homes are not backed up in this version: a disk lost is a home lost.

Rehearse a restore once after standing up, and after any change to the database's settings,
without touching the live instance:

```bash
gcloud sql instances clone <prefix>-pg <prefix>-pg-rehearsal \
  --point-in-time "$(date -u -d '-15 min' +%Y-%m-%dT%H:%M:%SZ)"
# connect to the clone as in "The database, once" and check that the data is there:
#   SELECT count(*) FROM identity;  SELECT max(at) FROM audit_log;
gcloud sql instances delete <prefix>-pg-rehearsal
```

Record the date and how long the clone took; that is your recovery time.

### Alerts

Logs go to Cloud Logging. Set these up in Cloud Monitoring:

| Alert | How |
|---|---|
| The CP is not ready (its database included) | an uptime check on `https://<fqdn>/readyz` |
| The certificate is about to expire | the same uptime check, with SSL certificate validation and its expiry alert |
| A workspace stays `Pending` | a log-based metric on the `FailedScheduling` events of the workspace namespace, alerting when it persists for 10 minutes; the start dialog shows the same reason |
| The quota refused a start | a log-based metric on events whose message contains `exceeded quota` in the workspace namespace |
| A workspace node is short of disk | the node condition `DiskPressure`, or `kubernetes.io/node/ephemeral_storage/allocatable_bytes` against use, on the workspace pool |
| Destroy left something behind | Destroy writes its residues to the audit log, which lives in the database, not in Cloud Logging. Check after removing members — `SELECT at, target, detail FROM audit_log WHERE action = 'workspace.destroy' AND detail LIKE '%NOT deleted:%'` — and see "Residue cleanup" |

### Upgrading nodes (planned)

A drain is not a stop. It deletes a workspace's pod but leaves `replicas: 1`, so the StatefulSet
recreates the pod on another node at once: the member's session is cut, the workspace comes back
by itself, and its capacity keeps billing. So:

1. **Cordon** the node, so nothing new is placed on it:
   `kubectl cordon <node>`
2. **Stop its workspaces through the CP**, those still starting included. List them:
   ```bash
   kubectl -n <prefix>-ws get pods -l agent-fleet.io/workspace --field-selector spec.nodeName=<node> \
     -o custom-columns=POD:.metadata.name,WORKSPACE:.metadata.labels.agent-fleet\\.io/workspace
   ```
   and use **Force-stop the workspace** in each member's detail (guide: admin/02). Wait until the
   list is empty: the CP's Stop returns only when the pod is gone.
3. **Drain**: `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data`
4. Upgrade or replace the node; **uncordon** it when it returns: `kubectl uncordon <node>`.

Members start their workspaces again on their next use. The CP needs no right over Nodes for
any of this.

Workspace pods carry `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, so the
autoscaler never removes a node under a live session; the pool shrinks as workspaces stop.

GKE upgrades nodes by itself inside the maintenance window (`maintenance_window`). To upgrade
only by the procedure above, add a maintenance exclusion with the scope "no minor or node
upgrades" for the period you want to control, and run the node upgrade yourself:

```bash
gcloud container clusters update <prefix>-gke --region <region> \
  --add-maintenance-exclusion-name hold-nodes \
  --add-maintenance-exclusion-start <start> --add-maintenance-exclusion-end <end> \
  --add-maintenance-exclusion-scope no_minor_or_node_upgrades
```

### Unplanned drain

An automatic upgrade outside your control, a node auto-repair or a preemption deletes the pods
on the node. Each running workspace is cut and returns by itself on another node — through
`starting` to `running`, with no CP action. Members see their sessions end and reconnect. Nothing
needs doing unless a workspace stays `starting`; then look at its pod's events.

### A node that stopped answering

The CP never replaces a pod on a node that stopped answering: that is the price of "at most one
agent per home". The workspace stays `starting` (or `stopped` with a pod that blocks Start,
Clean home and Destroy), and Stop, Recreate and Clean home return errors. Recovery is the
operator's, **and it starts with proof that the old process cannot run**:

1. Find the VM behind the node, and confirm **from Google Cloud, not from Kubernetes**, that it is
   stopped or gone:
   ```bash
   gcloud compute instances describe <node> --zone <zone> --format='value(status)'
   # TERMINATED, or "not found", is proof. RUNNING, or no answer, is not.
   ```
   If the VM is running but unreachable (a network partition), or its state cannot be read,
   **do nothing**: the agent may still be writing to the home.
2. Only then, either delete the Node object (`kubectl delete node <node>`), or mark it out of
   service so the volumes detach:
   ```bash
   kubectl taint nodes <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute
   ```
   Deleting a Node object does not stop its VM, and the taint detaches volumes — which is why
   step 1 comes first.
3. The StatefulSet recreates the pod elsewhere (in the volume's zone). Remove the taint once the
   node is gone or repaired.

### Residue cleanup

When Destroy cannot confirm that something is gone, it says so in the audit log
(`workspace.destroy … NOT deleted: …`), as one of:

| Residue | What to do |
|---|---|
| `pvc:<namespace>/<name>` | `kubectl -n <namespace> get pvc <name>`. If it is stuck terminating, a pod still uses it (`kubectl -n <namespace> get pods -l agent-fleet.io/workspace=<workspace>`): wait for that pod, or follow the procedure above for its node |
| `pv:<name>` | `kubectl get pv <name>`. While it exists, the disk behind it may too: `kubectl get pv <name> -o jsonpath='{.spec.csi.volumeHandle}'` names it. When the volume object is gone, check the disk is too with `gcloud compute disks list --filter="name~<pv name>"` |
| `statefulset:<namespace>/<name>` | Destroy kept it on purpose: it holds the inventory (as an annotation) of claims and volumes that were not confirmed gone. Clear each one it lists as above, **then** delete it: `kubectl -n <namespace> delete statefulset <name>` |

A periodic sweep catches what nobody read in the audit log: disks no node uses
(`gcloud compute disks list --filter="-users:*"`), volumes in `Released`
(`kubectl get pv | grep Released`), and StatefulSets in the workspace namespace whose member no
longer exists.

### Node disk

Workspace nodes have a large boot disk (`workspace_boot_disk_gb`) because it holds the images,
container logs, and every pod's `/tmp` and ephemeral storage. Container logs are capped (50 MiB,
3 files), and the kubelet starts evicting below 15% free. A pod's ephemeral-storage limit is
checked periodically, not enforced as it is written, so a fast writer can still fill the disk
before it is evicted — the disk-pressure alert above is what tells you.

### The bill

Its shape follows [docs/build/09 §9.8](../../docs/build/09-deploy.md): a **floor** (the cluster
management fee, the system pool, Cloud SQL, Cloud NAT and its address, the load balancer),
**per-workspace capacity while running** (the workspace pool scales with the running
workspaces), and **two persistent disks per workspace that bill while stopped**, as an EBS home
does. A cluster you already run removes the cluster from the floor. The measured numbers come
with the acceptance run (#1468).

## Tearing down

Cloud SQL, the cluster and the KMS key are protected against deletion on purpose. To remove a
deployment: destroy every member's workspace in the Console first (so the disks go with their
claims), `kubectl delete -k` the overlay, then set `deletion_protection = false` on the cluster
and the instance, remove the key's `prevent_destroy`, and `terraform destroy`. KMS keys are not
deleted by Google Cloud, only their versions are scheduled for destruction.

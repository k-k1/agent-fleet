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
| These manifests (`kubectl apply -k`) | Namespaces, the CP, its Services and its own disk (`af-cp-data`), RBAC, NetworkPolicies, quota, the Gateway and its policies |
| The CP, at run time | Per workspace: a StatefulSet, a Service, a Secret, two claims, and now and then a one-shot erase pod — all labelled `agent-fleet.io/workspace=<name>` |

## Preconditions

None of these can be checked by the CP; a cluster that misses one runs, and is not isolated.
Terraform meets every GKE item below.

| # | Precondition | Why (ADR 0106) |
|---|---|---|
| P1 | Kubernetes **1.33 or later**. Terraform creates the cluster at its release channel's default version and checks this floor (`min_master_version`) on every plan | the PersistentVolume deletion-protection finalizer Destroy relies on is stable from 1.33 (decision 5) |
| P2 | A CNI that **enforces NetworkPolicy** (Dataplane V2 on GKE) | without one the policies are accepted and enforce nothing (decision 7) |
| P3 | A StorageClass with `volumeBindingMode: WaitForFirstConsumer`, `allowVolumeExpansion: true`, `reclaimPolicy: Delete`, backed by **block storage** (not NFS / Filestore) | decision 4; with `Retain` the disk and its bill outlive Destroy (decision 5) |
| P4 | **Application-layer encryption of Secrets** (Cloud KMS on GKE) | every workspace's DEK and tokens are in a Secret (decision 6) |
| P5 | The control-plane endpoint is **private, or limited by authorised networks that exclude the pod range**. A public endpoint open to all is not supported | decision 7 |
| P6 | The kubelet's **read-only port is off** (GKE's default, and Terraform sets it), and **no `hostNetwork` service** runs on workspace nodes | a pod can always reach its own node (decision 7) |
| P7 | On GKE, **every node pool that can run a workspace uses `GKE_METADATA`**, and workspaces are scheduled only there (`AF_K8S_NODE_SELECTOR`) | the node's credentials (decision 7) |
| P8 | **No IAM grant names the workspace namespace or its service account**, directly or through a `principalSet`, and the namespace names carry this deployment's own prefix | Workload Identity treats equal names in any cluster of the project as one identity (decision 7) |
| P9 | **Members have no Kubernetes API access** to the workspace namespace. Creating pods there is reading every member's home and Secret | decision 7 |
| P10 | The cluster is **IPv4 single-stack**. The egress policy allows no `::/0`, so IPv6 egress is denied rather than unfiltered | decision 7 |
| P11 | **NodeLocal DNSCache is off** (GKE turns it on for new clusters; Terraform sets it off. On an existing cluster the change takes about 10 minutes and may recreate the nodes depending on the GKE version; on 1.35 it removed the `node-local-dns` pods and recreated no node and restarted no workspace, measured 2026-10-03), or a policy for its address is added. The DNS policy allows `kube-dns` pods only; `169.254.0.0/16` is denied, so with the cache on every lookup from a workspace fails | decision 7 |
| P12 | A **Postgres** the deployment provides (Cloud SQL on GKE) | decision 8 |
| P13 | **A DNS zone** for the Console's name, and a **state bucket** for Terraform, both the operator's | decision 12 |
| P14 | `AF_MASTER_KEY` is generated and kept **outside the database and its backups** | as on every target; losing it is a crypto-shred |

Tools on the operator's machine: `gcloud` with the `gke-gcloud-auth-plugin` component
(`gcloud components install gke-gcloud-auth-plugin`, which `kubectl` needs to sign in to GKE),
`terraform` (1.6 or later), `kubectl` (its built-in kustomize is enough), `psql` for the
one-time database grant.

## GKE

Every command below runs **from the repository root**, and every `gcloud` command names the
project explicitly, so a different default project in your `gcloud` configuration cannot be
the one that is changed. Set these once per shell:

```bash
PROJECT=<project>          # Terraform's project_id
REGION=<region>            # Terraform's region
PREFIX=<name_prefix>       # Terraform's name_prefix
TF="terraform -chdir=deploy/gcp/gke"
```

### 1. The project, once

Sign in twice: `gcloud` for the commands below, and Application Default Credentials for
Terraform's providers.

```bash
gcloud auth login
gcloud auth application-default login
gcloud auth application-default set-quota-project "$PROJECT"
```

Enable the APIs Terraform uses:

```bash
gcloud services enable container.googleapis.com compute.googleapis.com \
  sqladmin.googleapis.com servicenetworking.googleapis.com certificatemanager.googleapis.com \
  dns.googleapis.com cloudkms.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  --project "$PROJECT"
```

The Cloud DNS managed zone for your domain must already exist in the project (P13). Create the
state bucket if you do not have one, with versioning on:

```bash
gcloud storage buckets create gs://<state-bucket> --project "$PROJECT" --location "$REGION" \
  --uniform-bucket-level-access
gcloud storage buckets update gs://<state-bucket> --versioning --project "$PROJECT"
```

Images: push `control-plane` and `workspace` to an Artifact Registry repository (and set
`artifact_registry_repository`, so the node pool and the CP get reader on it), or pull them
from the public registry (`ghcr.io/k-k1/agent-fleet/…`) and leave it `null`.

### 2. Terraform

```bash
cp deploy/gcp/gke/terraform.tfvars.example deploy/gcp/gke/terraform.tfvars   # fill it in; gitignored
$TF init -backend-config="bucket=<state-bucket>" -backend-config="prefix=$PREFIX"
$TF apply
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
- `min_master_version` is a floor, not the version the cluster is created at: the cluster starts
  at the release channel's default, and every plan fails if the control plane runs below the
  floor (P1).
- The certificate is issued once the DNS authorisation records resolve.
  `gcloud certificate-manager certificates describe $PREFIX-cert --project "$PROJECT"` shows its
  state; it is usually `ACTIVE` within an hour.

What it grants (decision 12; each to one principal on one resource): the CP's service account
gets Cloud SQL client and instance user, conditioned to this instance, and Artifact Registry
reader on the repository; the node service account gets Artifact Registry reader and the minimum
node role (`roles/container.defaultNodeServiceAccount`: logs, metrics, and from 1.33 the
autoscaler's metrics); GKE's service agent gets encrypt/decrypt on the Secrets key; the CP's
Kubernetes service account `<prefix>-cp/af-cp` may act as the CP's service account. Nothing names
the workspace namespace.

### 3. Point kubectl at this cluster

Terraform does not touch your kubeconfig. **Before any `kubectl` command**, fetch this cluster's
credentials and check that the current context is it — otherwise the namespaces, RBAC and
Secrets below land on whatever cluster your kubeconfig pointed at:

```bash
eval "$($TF output -raw get_credentials)"     # gcloud container clusters get-credentials …
kubectl config current-context                # gke_<project>_<region>_<prefix>-gke
kubectl get nodes -L agent-fleet.io/pool      # the system and workspace pools
```

Do the same in every new shell before the procedures under "Operating it".

### 4. The database, once

The CP's database user is an IAM user with no password and, at first, no rights. Give it the
database, as the built-in `postgres` user, through a Cloud SQL Auth Proxy:

```bash
gcloud sql users set-password postgres --instance "$PREFIX-pg" --project "$PROJECT" --prompt-for-password
DBUSER="$($TF output -raw cp_database_user)"
cloud-sql-proxy --private-ip "$($TF output -raw cloud_sql_instance)" &   # from inside the VPC
psql "host=127.0.0.1 user=postgres dbname=agentfleet" <<SQL
GRANT "$DBUSER" TO postgres;
ALTER DATABASE agentfleet OWNER TO "$DBUSER";
ALTER SCHEMA public OWNER TO "$DBUSER";
SQL
```

The instance has no public IP, so the proxy runs from a machine in the VPC (a short-lived VM,
or Cloud Shell with a VPC connection). Keep the `postgres` password in your vault, not here.

### 5. The overlay

```bash
cp -r deploy/kubernetes/overlays/gke "deploy/kubernetes/overlays/$PREFIX"
$TF output -raw kustomize_deployment > "deploy/kubernetes/overlays/$PREFIX/deployment.yaml"
```

Then edit, in the copy:

- `cp.env`: `PUBLIC_BASE_URL` and `PUBLIC_DOMAIN` (the `fqdn`), `AF_PREVIEW_DOMAIN` (Terraform's
  `preview_domain`, or empty), the sign-in settings, `SUPER_ADMIN_EMAILS`. Keep
  `AF_TRUSTED_PROXY_HOPS=2` (see "The load balancer").
- `kustomization.yaml`: `images:` — the CP image and the release tag.
- `base/cp-data.yaml` asks 20 GiB for the CP's own disk; patch it in the copy if the internal
  git provider will hold more.

Keep this copy in your own repository or vault, not in a working copy of this one.

### 6. Secrets

The CP reads one Secret, `af-cp-secrets`, which these manifests never contain. Create it once
the namespace exists:

```bash
kubectl config current-context                # still this cluster (step 3)
kubectl apply -k "deploy/kubernetes/overlays/$PREFIX"
kubectl -n "$PREFIX-cp" create secret generic af-cp-secrets \
  --from-literal=AF_MASTER_KEY="$(cat master-key)" \
  --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
  --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
```

Until it exists the CP pod waits in `CreateContainerConfigError` and starts by itself once it
does. Generate `AF_MASTER_KEY` with `head -c 32 /dev/urandom | base64`, as for compose
([deploy/compose/README.md](../compose/README.md)), and keep its only other copy in your vault
(P14). Other sign-in secrets (`AF_OIDC_<ID>_CLIENT_SECRET`, `GITHUB_OAUTH_CLIENT_SECRET`) go in
the same Secret; rotate one with `kubectl create secret … --dry-run=client -o yaml | kubectl
apply -f -` and `kubectl -n "$PREFIX-cp" rollout restart deployment/af-cp`.

### 7. The load balancer

The Gateway `af-cp` (`components/gke/gateway.yaml`) makes GKE build a **global external**
Application Load Balancer — not the classic one, which closes even an active WebSocket at the
backend timeout. Terminals, the mirror and the browser pane are WebSockets, so two limits matter:

- **An idle WebSocket** (a terminal nobody types in, a quiet session) is closed after the
  backend service's `timeoutSec`. The default is 30 seconds; the `GCPBackendPolicy` `af-cp` in
  the same file sets 3600. To change it, edit `timeoutSec` there and `kubectl apply -k` the
  overlay; GKE updates the backend service within a few minutes
  (`gcloud compute backend-services list --project "$PROJECT"` shows the value).
- **An active WebSocket** is closed after 24 hours whatever `timeoutSec` says. That cut cannot
  be configured away; the Console has to reconnect.

Whether an idle terminal survives the timeout in practice, and whether the Console reconnects
transparently after either cut, is **not yet measured** (ADR 0106 open question 3, #1468). Test
both before relying on long-lived terminals.

**`AF_TRUSTED_PROXY_HOPS` stays 2.** The load balancer appends `<client>, <load balancer>` to
whatever `X-Forwarded-For` it receives, and the CP counts from the right. With nothing in front
of the load balancer, 2 names the real client. Raising it for a CDN or proxy in front is only
safe when the load balancer cannot be reached except through that proxy (for example a Cloud
Armor policy admitting only the proxy's addresses) **and** you have checked which entries the
proxy appends or replaces. Otherwise a client that sends its own `X-Forwarded-For` straight to
the load balancer chooses the address the CP sees, and walks past a tenant's network
restriction. This tree supports no proxy in front of the load balancer.

### 8. Check it

```bash
kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
kubectl get ns "$PREFIX-ws" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'   # restricted
kubectl -n "$PREFIX-cp" get pvc af-cp-data    # Bound
kubectl -n "$PREFIX-cp" get gateway af-cp     # PROGRAMMED True, ADDRESS = lb_address
curl -sS https://<fqdn>/readyz
```

What the CP's account may do — every line must answer as shown:

```bash
SA=system:serviceaccount:$PREFIX-cp:af-cp
kubectl auth can-i create statefulsets -n "$PREFIX-ws" --as $SA       # yes
kubectl auth can-i delete pods -n "$PREFIX-ws" --as $SA               # yes (the erase pod)
kubectl auth can-i patch namespaces --as $SA                          # no
kubectl auth can-i create networkpolicies -n "$PREFIX-ws" --as $SA    # no
kubectl auth can-i list storageclasses --as $SA                       # no
kubectl auth can-i get "storageclass/$PREFIX-workspace" --as $SA      # yes
kubectl auth can-i create pods -n "$PREFIX-cp" --as $SA               # no
```

NodeLocal DNSCache is off (P11); this prints nothing:

```bash
kubectl -n kube-system get pods -l k8s-app=node-local-dns
```

Then sign in, start a workspace, and check that its pod runs on the workspace pool and resolves
names:

```bash
kubectl -n "$PREFIX-ws" get pods -o wide -l agent-fleet.io/workspace
kubectl -n "$PREFIX-ws" exec <pod> -- getent hosts github.com     # an address, not empty
```

In that workspace's terminal, the home has to be `dev`'s and writable by nobody else, or
`af-gcloud-exec` and `af-aws-exec` refuse to keep their state under it (ADR 0107):

```bash
stat -c '%U:%G %A' /home/dev    # dev:dev drwxr-sr-x — no w for group or other (the s, from fsGroup, is harmless)
```

The home is the directory `.af-home` of the home claim, which an init container running as `dev`
creates — and moves an earlier home's files into — before the agent starts; the claim's root
stays `root:dev` with group write, as the kubelet leaves it. A pod stuck in `Init` names the reason
in `kubectl logs <pod> -c home-layout`; rolling the CP back past this layout is covered under
"Rolling back past the home layout".

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
  `X-Forwarded-For` — counting a proxy only if the CP cannot be reached except through it, as
  "The load balancer" explains;
- give the claim `af-cp-data` a backup, as "What has to survive" does on GKE;
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

The commands below assume the shell variables of "GKE" and that `kubectl config current-context`
is this cluster (step 3).

### What has to survive

Two things hold the deployment's state, and they belong together:

- **the database** (Cloud SQL): every row;
- **the CP's own disk**, the claim `af-cp-data` at `WS_DATA` (`/var/lib/af-cp`): the internal git
  provider's bare repositories and LFS objects, and the git token key. The database's list of
  repositories is worth nothing without them, and a new key invalidates every git token handed
  out.

`AF_MASTER_KEY` is the third, and lives in your vault (P14). The homes are not backed up in this
version: a disk lost is a home lost.

A backup taken **with the CP stopped** is consistent across the first two; a scheduled one is
not (the disk's snapshot and the database's backup run at different moments, so a repository
pushed in between can be in one and not the other).

To find the CP's disk — and to find it **again after a restore**, which replaces it:

```bash
PV="$(kubectl -n "$PREFIX-cp" get pvc af-cp-data -o jsonpath='{.spec.volumeName}')"
HANDLE="$(kubectl get pv "$PV" -o jsonpath='{.spec.csi.volumeHandle}')"   # projects/<p>/zones/<z>/disks/<name>
ZONE="$(echo "$HANDLE" | cut -d/ -f4)"; DISK="${HANDLE##*/}"
```

### A disk from a snapshot, as a volume

Both the rehearsal and the rollback below turn a snapshot into a disk and hand it to Kubernetes
as a pre-created volume. Such a volume carries no zone of its own, unlike the ones the
StorageClass provisions, so the PersistentVolume must name it: without `nodeAffinity` the pod
can be scheduled into another zone of the system pool, where the disk cannot attach, and it
never starts. Set `NAME` (the new disk and volume, **unique per restore** — a fixed name collides
with the disk a previous restore put into service), `CLAIM` (the claim it is for) and `SNAP` (the
snapshot), with `ZONE` from above. It comes in two parts, so a procedure can make the disk before
it changes anything else.

The disk:

```bash
gcloud compute disks create "$NAME" --zone "$ZONE" --project "$PROJECT" \
  --source-snapshot "$SNAP" --type pd-balanced
SIZE="$(gcloud compute disks describe "$NAME" --zone "$ZONE" --project "$PROJECT" --format='value(sizeGb)')"
```

The volume, for the claim `$CLAIM`:

```bash
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $NAME
spec:
  capacity: { storage: ${SIZE}Gi }
  accessModes: ["ReadWriteOnce"]
  persistentVolumeReclaimPolicy: Retain      # until it is confirmed; see each procedure
  storageClassName: $PREFIX-workspace
  claimRef: { namespace: $PREFIX-cp, name: $CLAIM }
  csi:
    driver: pd.csi.storage.gke.io
    volumeHandle: projects/$PROJECT/zones/$ZONE/disks/$NAME
    fsType: ext4
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - { key: topology.gke.io/zone, operator: In, values: ["$ZONE"] }
YAML
```

### Backups, point-in-time recovery and the restore rehearsal

Terraform turns on the database's automated backups and point-in-time recovery
(`sql_backup_retention_days`). Give the CP's disk a snapshot schedule once, after the first
start, and again whenever a restore replaced the disk (the schedule belongs to the disk, not to
the claim):

```bash
gcloud compute resource-policies create snapshot-schedule "$PREFIX-cp-data" --project "$PROJECT" \
  --region "$REGION" --daily-schedule --start-time 02:30 --max-retention-days 14   # once
gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
  --resource-policies "$PREFIX-cp-data"
```

Scheduled snapshots expire by themselves; the on-demand ones below stay until you delete them
(`gcloud compute snapshots delete <name> --project "$PROJECT"`).

Rehearse a database restore once after standing up, and after any change to the database's
settings, without touching the live instance:

```bash
gcloud sql instances clone "$PREFIX-pg" "$PREFIX-pg-rehearsal" --project "$PROJECT" \
  --point-in-time "$(date -u -d '-15 min' +%Y-%m-%dT%H:%M:%SZ)"
# connect to the clone as in "The database, once" and check that the data is there:
#   SELECT count(*) FROM identity;  SELECT max(at) FROM audit_log;
gcloud sql instances delete "$PREFIX-pg-rehearsal" --project "$PROJECT"
```

Rehearse the disk the same way: restore a snapshot beside the live one, **read it**, and remove
it. Creating a disk proves nothing about what is on it. Before taking the snapshot, push a
sentinel commit to a test repository through the internal git provider and note its commit id;
if you use LFS, track one file with LFS in that commit and note its oid and size (`git lfs
ls-files -l` shows the oid, `wc -c` the size). Checking every LFS object present is not enough
on its own: a snapshot with no LFS objects at all passes it. Then, with the live claim untouched:

```bash
NAME="$PREFIX-cp-data-rehearsal-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data-rehearsal SNAP=<snapshot>
SENT_REPO="git/<tenant slug>/<test repo>.git" SENT_REF=refs/heads/<branch> SENT_COMMIT=<commit id>
LFS_OID=<oid, or empty without LFS> LFS_SIZE=<bytes, or empty>
LFS_PATH=""; [ -n "$LFS_OID" ] && LFS_PATH="$SENT_REPO/lfs/objects/${LFS_OID:0:2}/${LFS_OID:2:2}/$LFS_OID"
# ... both parts of "A disk from a snapshot, as a volume" ...
IMAGE="$(kubectl -n "$PREFIX-cp" get deployment af-cp -o jsonpath='{.spec.template.spec.containers[?(@.name=="cp")].image}')"
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $CLAIM, namespace: $PREFIX-cp }
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: $PREFIX-workspace
  volumeName: $NAME
  resources: { requests: { storage: ${SIZE}Gi } }
---
apiVersion: v1
kind: Pod
metadata: { name: af-cp-data-rehearsal, namespace: $PREFIX-cp }
spec:
  restartPolicy: Never
  nodeSelector: { agent-fleet.io/pool: system }
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    seccompProfile: { type: RuntimeDefault }
  containers:
    - name: check
      image: $IMAGE
      command: ["sh", "-c"]
      args:
        - |
          set -e; cd /data; ls -la
          test -d git || { echo "FAIL: no git/"; exit 1; }
          for r in git/*/*.git; do git --git-dir="\$r" fsck --no-dangling >/dev/null; echo "fsck ok \$r"; done
          test "\$(git --git-dir="$SENT_REPO" rev-parse "$SENT_REF")" = "$SENT_COMMIT" \
            || { echo "FAIL: sentinel commit"; exit 1; }
          echo "sentinel commit ok"
          if [ -n "$LFS_PATH" ]; then
            test -f "$LFS_PATH" || { echo "FAIL: sentinel LFS object missing"; exit 1; }
            test "\$(wc -c < "$LFS_PATH")" -eq "$LFS_SIZE" || { echo "FAIL: sentinel LFS size"; exit 1; }
            test "\$(sha256sum "$LFS_PATH" | cut -c1-64)" = "$LFS_OID" || { echo "FAIL: sentinel LFS hash"; exit 1; }
            echo "sentinel lfs ok"
          fi
          find git -path '*/lfs/objects/*' -type f > /tmp/lfs; n=0
          while read -r f; do
            test "\$(sha256sum "\$f" | cut -c1-64)" = "\$(basename "\$f")" || { echo "FAIL: \$f"; exit 1; }
            n=\$((n + 1))
          done < /tmp/lfs
          echo "lfs objects present and intact: \$n"
      env: [{ name: HOME, value: /tmp }]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: { drop: ["ALL"] }
      volumeMounts:
        - { name: data, mountPath: /data, readOnly: true }
        - { name: tmp, mountPath: /tmp }
  volumes:
    - name: data
      persistentVolumeClaim: { claimName: $CLAIM, readOnly: true }
    - name: tmp
      emptyDir: {}
YAML
kubectl -n "$PREFIX-cp" get pvc "$CLAIM"                 # Bound, to $NAME
kubectl -n "$PREFIX-cp" wait --for=jsonpath='{.status.phase}'=Succeeded pod/af-cp-data-rehearsal --timeout=600s
kubectl -n "$PREFIX-cp" logs af-cp-data-rehearsal       # "fsck ok" per repo, "sentinel commit ok", "sentinel lfs ok"
kubectl -n "$PREFIX-cp" get pod af-cp-data-rehearsal -o wide   # its node is in $ZONE
```

The check passes only when the claim bound, the pod ran in the disk's zone as uid 1000 and ended
`Succeeded`, every repository passed `fsck`, the sentinel commit is the one you noted, and — with
LFS — the sentinel's object is at its path with the size and hash you noted, besides every other
object matching its name. The first time, prove the check can fail: delete the pod and apply it
again with one character of `SENT_COMMIT` changed, then with one character of `LFS_OID` changed
(the object is then missing), then with a wrong `LFS_SIZE`; each run must end `Failed` with its
`FAIL:` line. Then remove all of it —
the volume is `Retain`, so the disk stays until deleted:

```bash
kubectl -n "$PREFIX-cp" delete pod af-cp-data-rehearsal
kubectl -n "$PREFIX-cp" delete pvc "$CLAIM"
kubectl delete pv "$NAME"
gcloud compute disks delete "$NAME" --zone "$ZONE" --project "$PROJECT"
```

Record the dates and how long each restore took; that is your recovery time.

### Upgrading the CP

The CP migrates its database at start and **cannot be downgraded**. The Deployment's strategy is
`Recreate`, so two CPs never run against one database.

1. Stop the CP and **wait until its pod is gone** (scaling is asynchronous):
   ```bash
   kubectl -n "$PREFIX-cp" scale deployment/af-cp --replicas=0
   kubectl -n "$PREFIX-cp" wait --for=delete pod -l app.kubernetes.io/name=af-cp --timeout=300s
   ```
2. Back up both halves, and wait for each:
   ```bash
   gcloud sql backups create --instance "$PREFIX-pg" --project "$PROJECT" --description "before <version>"
   gcloud compute snapshots create "$PREFIX-cp-data-before-<version>" --project "$PROJECT" \
     --source-disk "$DISK" --source-disk-zone "$ZONE"
   ```
3. Set the new tag under `images:` in your overlay and apply; `kubectl apply` sets replicas
   back to 1:
   ```bash
   kubectl apply -k "deploy/kubernetes/overlays/$PREFIX"
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   ```

Going back means **restoring both backups of step 2 and the previous image together**. The
previous image on the migrated database is not a rollback:

1. Stop the CP and wait for its pod to be gone, as in step 1. Do not start the restore before:
   a CP still running writes into the database being replaced.
2. The database:
   ```bash
   gcloud sql backups list --instance "$PREFIX-pg" --project "$PROJECT"
   gcloud sql backups restore <backup-id> --restore-instance "$PREFIX-pg" --project "$PROJECT"
   ```
3. The disk, only if the internal git provider was used since the snapshot: keep the current
   disk, make a new one from the snapshot, and only then point the claim at it. If the new disk
   cannot be made, nothing has changed yet.
   ```bash
   OLD_PV="$PV" OLD_DISK="$DISK"                      # from "What has to survive"
   NAME="$PREFIX-cp-data-restore-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data SNAP="$PREFIX-cp-data-before-<version>"
   # ... the disk part of "A disk from a snapshot, as a volume" ...
   kubectl patch pv "$OLD_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'   # keep the old disk
   kubectl -n "$PREFIX-cp" delete pvc af-cp-data
   # ... the volume part of "A disk from a snapshot, as a volume" ...
   ```
   To back out before step 4: delete the new volume, then recreate the claim bound to the old
   one (`kubectl patch pv "$OLD_PV" -p '{"spec":{"claimRef":null}}'`, then a claim
   `af-cp-data` with `volumeName: $OLD_PV`).
4. Set the previous tag in the overlay and `kubectl apply -k` it. The claim is recreated and
   binds to the restored volume; check that, and that the CP runs in the disk's zone:
   ```bash
   kubectl -n "$PREFIX-cp" get pvc af-cp-data          # Bound, VOLUME = $NAME
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   kubectl -n "$PREFIX-cp" get pods -o wide -l app.kubernetes.io/name=af-cp   # a node in $ZONE
   ```
5. Once the restored deployment is confirmed (sign in, clone a repository), put the disks back in
   order: remove the old one, give the restored volume the StorageClass's `Delete` again (so
   Destroy and teardown remove it as they remove any other), and move the snapshot schedule to
   the new disk:
   ```bash
   kubectl delete pv "$OLD_PV"
   gcloud compute disks delete "$OLD_DISK" --zone "$ZONE" --project "$PROJECT"
   kubectl patch pv "$NAME" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
   # re-read PV, HANDLE, ZONE and DISK with the block of "What has to survive", then:
   gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
     --resource-policies "$PREFIX-cp-data"
   ```

The workspace image follows `workspaceImage` in `deployment.yaml`. A running workspace keeps the
image it started with; the next start pins the tag's current digest, and the Console marks the
workspaces still on an older one as stale.

#### Rolling back past the home layout

From the version that fixed #1543 on, a workspace's home is the directory `.af-home` of its home
claim, and the first start under it moves an earlier home there, recording that on the state
claim (ADR 0106, addendum of 2026-10-03). **An earlier CP does not know this, and rolling back
past it is not safe for workspaces that have started since:** its pod mounts the claim's root as
the home, so the member finds an almost empty home with their files hidden in `.af-home`. Its Clean
home and administrator's Clean home remove `.af-home` whole — the migrated home, logins included;
its Recreate removes only the root's `repos`, leaving the migrated `~/repos` in place and hidden.

So, when the CP has to go back past that version:

1. Stop every workspace first, and start, Recreate or clean none of them under the earlier CP
   until its home is moved back.
2. To move one back: snapshot its home disk, then, with the workspace stopped, run a pod from the
   workspace image with the workspace pod's security context (as `dev`), the home claim mounted at
   `/v` and the state claim at `/s`, and in it run this as a script (`sh -s`):
   ```bash
   V=/v R=/s/wipe/layout
   set -eu
   cd "$V"
   # Stopped after the last move: only the record is left to remove.
   if [ "$(cat -- "$R" 2>/dev/null)" = reverting ] && [ ! -e .af-home ] && [ ! -L .af-home ]; then rm -f -- "$R"; exit 0; fi
   [ -d .af-home ] && [ ! -L .af-home ] || { echo ".af-home is missing or not a directory"; exit 1; }
   [ -f "$R" ] && [ ! -L "$R" ] || { echo "no layout record at $R"; exit 1; }
   st=$(cat -- "$R")
   case "$st" in
     done)
       for e in .[!.]* ..?* *; do
         [ -e "$e" ] || [ -L "$e" ] || continue
         case "$e" in .af-home|lost+found) ;; *) echo "the root holds $e besides the home: see step 3"; exit 1;; esac
       done ;;
     reverting) ;;  # a run stopped halfway: carry on
     *) echo "the layout record is not done or reverting"; exit 1 ;;
   esac
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}
     case "$n" in .af-home|lost+found) echo "the home holds $n, a name the root keeps"; exit 1;; esac
     if [ -e "$n" ] || [ -L "$n" ]; then echo "$n is both in the home and on the root"; exit 1; fi
   done
   # Only now, with every check passed, mark the move; replaced whole, never truncated.
   if [ "$st" = done ]; then
     if [ -e "$R.tmp" ] || [ -L "$R.tmp" ]; then echo "$R.tmp is in the way"; exit 1; fi
     { echo reverting > "$R.tmp" && mv -fT -- "$R.tmp" "$R"; } || { echo "cannot mark $R"; exit 1; }
   fi
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}; ro=
     # Moving a directory rewrites its "..", which needs write permission on it.
     if [ -d "$e" ] && [ ! -L "$e" ] && [ ! -w "$e" ]; then chmod u+w -- "$e"; ro=1; fi
     mv -T -- "$e" "$n"
     [ -z "$ro" ] || chmod u-w -- "$n"
   done
   rmdir .af-home
   rm -f -- "$R"
   ```
   It checks everything before it marks or moves anything, and moves nothing onto an existing name. If it
   stops halfway, the record says `reverting`: fix what it printed and run it again, and it
   carries on. Until it has finished, start the workspace under neither CP — this version refuses
   a `reverting` record, an earlier one would mount the half-moved root. A read-only directory it
   stopped on in the middle of its move may be left writable. Once the record is gone, the next
   start under this version migrates the home again.
3. If a workspace has run under the earlier CP anyway, this version refuses to start it again:
   `kubectl logs <pod> -c home-layout` says the claim's root holds entries besides the home. The
   root then holds what the earlier CP's session wrote and `.af-home` the migrated home. With the
   workspace stopped and its disk snapshotted, decide in the same kind of pod which copy of each
   entry to keep, and leave the root holding only `.af-home` and `lost+found` — or move the home
   back as in step 2.

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
   kubectl -n "$PREFIX-ws" get pods -l agent-fleet.io/workspace --field-selector spec.nodeName=<node> \
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
gcloud container clusters update "$PREFIX-gke" --region "$REGION" --project "$PROJECT" \
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
   gcloud compute instances describe <node> --zone <zone> --project "$PROJECT" --format='value(status)'
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
| `pv:<name>` | `kubectl get pv <name>`. While it exists, the disk behind it may too: `kubectl get pv <name> -o jsonpath='{.spec.csi.volumeHandle}'` names it. When the volume object is gone, check the disk is too with `gcloud compute disks list --project "$PROJECT" --filter="name~<pv name>"` |
| `statefulset:<namespace>/<name>` | Destroy kept it on purpose: it holds the inventory (as an annotation) of claims and volumes that were not confirmed gone. Clear each one it lists as above, **then** delete it: `kubectl -n <namespace> delete statefulset <name>` |

A periodic sweep catches what nobody read in the audit log: disks no node uses
(`gcloud compute disks list --project "$PROJECT" --filter="-users:*"`), volumes in `Released`
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

## The live harness

ADR 0106 decision 10's live harness runs the adapter against this cluster from a developer's
machine: `TestKubernetesLive*` in `control-plane/internal/runtime/runtime_kubernetes_live_test.go`.
It is skipped (and says so) unless `AF_K8S_LIVE=1`. It creates workspaces named `live-…` with
real disks in the workspace namespace and removes each one when its test ends, by Destroy and
then by `kubectl` for anything left; it checks on Google Cloud that the disks are gone too.

The adapter runs with **the CP's own RBAC**: a short-lived token of the `af-cp` service account,
so a permission missing from `cp-rbac.yaml` fails the run (the harness also checks that the token
cannot list nodes). The harness's own checks, its cleanup and what stands for someone else (a
pod deleted behind the CP's back, the runbook's operator) use `kubectl` with your kubeconfig.

```bash
# in a shell with kubectl pointed at this cluster (step 3)
W=$(mktemp -d); umask 077
kubectl -n "$PREFIX-cp" create token af-cp --duration=1h > "$W/cp-token"
kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$W/ca.crt"
export AF_K8S_LIVE=1
export AF_K8S_LIVE_SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
export AF_K8S_LIVE_CA_FILE=$W/ca.crt AF_K8S_LIVE_TOKEN_FILE=$W/cp-token
export AF_K8S_NAMESPACE=$PREFIX-ws AF_K8S_STORAGE_CLASS=$PREFIX-workspace AF_K8S_SERVICE_ACCOUNT=af-workspace
export AF_K8S_WORKSPACE_IMAGE=<the image of cp.env>
export AF_K8S_NODE_SELECTOR=agent-fleet.io/pool=workspace AF_K8S_HOME_GIB=10 AF_K8S_STATE_GIB=5
export AF_K8S_LIVE_GCE_PROJECT=$PROJECT                     # optional: check the disks on Google Cloud
# decision 7's probes: the deployment's own addresses (host:port, comma-separated)
export AF_K8S_LIVE_PROBE_OPEN=af-cp-internal.$PREFIX-cp.svc:8098
export AF_K8S_LIVE_PROBE_CLOSED=af-cp.$PREFIX-cp.svc:8099,<Cloud SQL private IP>:5432,<an unused node-range address>:22
cd control-plane
go test -count=1 -run 'TestKubernetesLive(Lifecycle|StartStopRaces|NetworkProbes)' -v -timeout 50m ./internal/runtime/
go test -count=1 -run 'TestKubernetesLive(HomeWipes|EraseHomeRestart|DestroyRestart)' -v -timeout 50m ./internal/runtime/
```

Before it acts, the harness checks that `kubectl`'s current context names the same API server and
CA as `AF_K8S_LIVE_SERVER` and `AF_K8S_LIVE_CA_FILE`, and pins every call to that context. Each
command has its own time limit, so a hung step fails the test and its cleanup runs; `go test`'s
own `-timeout` does not run cleanups, so keep it well above the run, and if it ever fires, do the
check below by hand.

The token lives an hour, and so does a Google Cloud access token behind `kubectl`: run the
scenarios in groups, as above, minting a fresh token for each. Each group takes 5–15 minutes.
A scenario whose race or window was not observed in a run (a Stop that met no pod being created,
an erase that finished before the restart) fails as `INCONCLUSIVE` rather than passing.

The network probes expect every node's ports closed, the pod's own node included, and
`AF_K8S_LIVE_PROBE_INTERNET` (default `1.1.1.1:443`) open as the control.

Two scenarios act on nodes and need `AF_K8S_LIVE_DISRUPTIVE=1` as well. Run them only where no
member's session can be cut. Both need the workspace pool to be able to add a node: on Google
Cloud a node's boot disk (`workspace_boot_disk_gb`) counts against the region's `SSD_TOTAL_GB`
quota with `pd-balanced`, and a scale-up refused by the quota leaves the harness's pod pending
(the autoscaler's `FailedScaleUp` event says so). `AF_K8S_LIVE_GUARD_POD=<namespace>/<pod>`
names a pod the scenario must leave alone, such as a member's workspace on the nodes it
cordons: the test fails if its UID, node or restart count changes.

- `TestKubernetesLivePlannedUpgrade` cordons `AF_K8S_LIVE_CORDON_NODES` (comma-separated) for the
  test and checks that a Start lands elsewhere; it does not drain.
- `TestKubernetesLiveNodeUnreachable` cuts off the node its workspace lands on and follows "A node
  that stopped answering". Cordon every other workspace node with `AF_K8S_LIVE_CORDON_NODES`, so
  that the autoscaler adds a node for it. The test refuses a node created before it began, and it
  cordons that node itself before checking its pods. Every pod on it must be the harness's own, a
  DaemonSet's, a static pod, or one named in `AF_K8S_LIVE_NODE_ALLOW_PODS` (`<namespace>/<name
  prefix>`, comma-separated; on GKE, `kube-system/konnectivity-agent-` is placed on new nodes).
  It takes three commands with `{node}` and `{zone}` filled in. The stop must cut the node off
  **without a shutdown the guest sees**. `gcloud compute instances stop` is an ACPI shutdown:
  the node terminates its pods and reports them before it goes, so Stop rightly settles, and the
  harness reports that run as `INCONCLUSIVE`. A power-off without a shutdown does not get there
  on GKE either: the node pool's instance group recreates a VM that terminated within seconds,
  and the node controller then deletes the old Node object and its pods, so Stop settles with no
  operator action (also `INCONCLUSIVE`). On GKE the scenario needs a node whose VM keeps running
  but stops answering.
  ```bash
  export AF_K8S_LIVE_NODE_STOP_CMD='<cut {node} off without a guest shutdown>'
  export AF_K8S_LIVE_NODE_STATUS_CMD='gcloud compute instances describe {node} --zone {zone} --project '"$PROJECT"' --format="value(status)"'
  export AF_K8S_LIVE_NODE_START_CMD='gcloud compute instances start {node} --zone {zone} --project '"$PROJECT"
  ```

Afterwards, check that nothing of the harness is left:
`kubectl -n "$PREFIX-ws" get sts,pvc,pods,svc,secrets | grep live-`, `kubectl get pv | grep live-`
and `gcloud compute disks list --project "$PROJECT" --filter="-users:*"`.

## Tearing down

Cloud SQL, the cluster and the KMS key are protected against deletion on purpose. To remove a
deployment:

1. Destroy every member's workspace in the Console first, so the disks go with their claims.
2. Take the backups of "Upgrading the CP" step 2 if anything may be wanted later: deleting the
   overlay deletes the claim `af-cp-data`, and with `reclaimPolicy: Delete` its disk.
3. `kubectl delete -k "deploy/kubernetes/overlays/$PREFIX"`, then check that no volume of the
   deployment is left behind (a `Retain` volume from an unfinished restore keeps its disk):
   `kubectl get pv | grep "$PREFIX-cp"` and
   `gcloud compute disks list --project "$PROJECT" --filter="name~$PREFIX-cp-data"` — delete any
   that remain.
4. Lift every protection Terraform set, then apply: on the cluster `deletion_protection = false`;
   on the Cloud SQL instance **both** `deletion_protection = false` (Terraform's own guard) and
   `settings.deletion_protection_enabled = false` (the Cloud SQL API's, which refuses the delete
   on its own); on the KMS key, remove `prevent_destroy`.
   ```bash
   $TF apply
   $TF destroy
   ```

KMS keys are not deleted by Google Cloud, only their versions are scheduled for destruction. The
snapshots of the CP's disk and the snapshot schedule outlive the destroy; delete them with
`gcloud compute snapshots delete` and `gcloud compute resource-policies delete` once unwanted.

---
audience: "whoever stands up Agent Fleet on GKE Standard (an SRE with admin rights on the Google Cloud project and the cluster)"
source_of_truth: "the Terraform under `deploy/gcp/gke/` and the manifests under `deploy/kubernetes/` (they win where this page disagrees)"
---

# Build on GKE

English | [日本語](gke.ja.md)

Build the cluster and everything around it with Terraform, apply the manifests, and get to the point where the Console answers on `https://<fqdn>`.
The preconditions the cluster must meet (P1–P15) are in [The Kubernetes deployment at a glance](overview.md#preconditions); on GKE, Terraform meets all of them.

## How it works

| What you do | What happens |
|---|---|
| 1. Sign in to the project, enable the APIs, prepare a state bucket | Terraform can run |
| 2. `terraform apply` | The VPC, the cluster and its two node pools, Cloud SQL, the KMS key, the load balancer's address, certificate and DNS, the StorageClass and the IAM grants are created |
| 3. Point kubectl at this cluster | Every later `kubectl` reaches this cluster and no other |
| 4. Grant the database (once) | The CP's IAM database user owns the database |
| 5. Copy and edit the overlay | The deployment's own settings (URL, sign-in, image version) are set |
| 6. Apply the overlay and create the Secret | The CP starts |
| 7. Know the load balancer | You know when a WebSocket is cut |
| 8. Check it | You know the rights, the isolation, name resolution and the home's owner are right |

## Shell variables

Every command below runs **from the repository root**, and every `gcloud` command names the project explicitly, so a different default project in your `gcloud` configuration cannot be the one that is changed. Set these once per shell:

```bash
PROJECT=<project>          # Terraform's project_id
REGION=<region>            # Terraform's region
PREFIX=<name_prefix>       # Terraform's name_prefix
TF="terraform -chdir=deploy/gcp/gke"
```

## 1. Prepare the project (once)

1. Sign in twice: `gcloud` for the commands below, and Application Default Credentials for Terraform's providers
   ```bash
   gcloud auth login
   gcloud auth application-default login
   gcloud auth application-default set-quota-project "$PROJECT"
   ```
2. Enable the APIs Terraform uses
   ```bash
   gcloud services enable container.googleapis.com compute.googleapis.com \
     sqladmin.googleapis.com servicenetworking.googleapis.com certificatemanager.googleapis.com \
     dns.googleapis.com cloudkms.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
     --project "$PROJECT"
   ```
3. Check the regional SSD quota against what the deployment can reach, and raise it now if it falls short ([Disk quota (GKE)](overview.md#disk-quota-gke)): a fresh project's 500 GB does not hold a second workspace node with the defaults
4. Check that the Cloud DNS managed zone for your domain already exists in the project (P13)
5. Create the state bucket if you do not have one, with versioning on
   ```bash
   gcloud storage buckets create gs://<state-bucket> --project "$PROJECT" --location "$REGION" \
     --uniform-bucket-level-access
   gcloud storage buckets update gs://<state-bucket> --versioning --project "$PROJECT"
   ```
6. Decide where the images come from

| Source | Setting |
|---|---|
| Your own Artifact Registry repository | Push `control-plane` and `workspace` and set `artifact_registry_repository`, so the node pool and the CP get reader on it |
| The public registry (`ghcr.io/k-k1/agent-fleet/…`) | Leave `artifact_registry_repository` `null` |

## 2. Apply Terraform

```bash
cp deploy/gcp/gke/terraform.tfvars.example deploy/gcp/gke/terraform.tfvars   # fill it in; gitignored
$TF init -backend-config="bucket=<state-bucket>" -backend-config="prefix=$PREFIX"
$TF apply
```

The variables in `terraform.tfvars` to watch:

| Variable | What to watch |
|---|---|
| `name_prefix` | Names every resource and the two namespaces (`<prefix>-cp`, `<prefix>-ws`). Two deployments in one project need two prefixes (P8) |
| `authorized_networks` | Who may reach the control-plane endpoint. The provider that creates the StorageClass talks to that endpoint, so `apply` must run from an authorised network — or, with `enable_private_endpoint = true`, from inside the VPC |
| `node_cidr`, `pod_cidr`, `service_cidr`, `control_plane_cidr`, `private_service_access_cidr` | Cannot change after the cluster exists. The first four must lie inside RFC 1918 or `100.64.0.0/10`, and a plan fails otherwise (P15). Keep the Private Service Access range there too, since the egress policy denies it only through those ranges |
| `min_master_version` | A floor, not the version the cluster is created at: the cluster starts at the release channel's default, and every plan fails if the control plane runs below the floor (P1) |

The certificate is issued once the DNS authorisation records resolve. This shows its state; it is usually `ACTIVE` within an hour:

```bash
gcloud certificate-manager certificates describe $PREFIX-cert --project "$PROJECT"
```

### What Terraform grants

Each grant goes to one principal on one resource. Nothing names the workspace namespace.

| Principal | What it gets |
|---|---|
| The CP's service account | Cloud SQL client and instance user, conditioned to this instance, and Artifact Registry reader on the repository |
| The node service account | Artifact Registry reader and the minimum node role (`roles/container.defaultNodeServiceAccount`: logs, metrics, and from 1.33 the autoscaler's metrics) |
| GKE's service agent | Encrypt/decrypt on the Secrets key |
| The CP's Kubernetes service account `<prefix>-cp/af-cp` | May act as the CP's service account |

### A small profile for a trial

For a trial or a single team. The commented block at the end of `terraform.tfvars.example` sets all of it:

| Setting | Value | The defaults instead |
|---|---|---|
| `node_zones`, `zonal_cluster` | one zone, `true` | a regional cluster with nodes in two zones |
| `sql_availability_type`, `sql_tier` | `ZONAL`, `db-custom-1-3840` (the smallest dedicated-core tier Postgres offers) | `REGIONAL` |
| `system_machine_type`, `system_node_count` | `e2-standard-2`, 1 | e2-standard-2 in each of two zones |
| `workspace_machine_type` | `n2-standard-4` | n2-standard-8 |
| `managed_prometheus` | `false` | on |

What it gives up:

- **Control-plane high availability.** A zonal cluster's API server is unavailable during its upgrades, when running workspaces keep running but cannot be managed
- **Database high availability.** Cloud SQL `ZONAL` has no standby, so a zone outage or a maintenance restart takes the CP down until the instance returns. Backups and point-in-time recovery stay on
- **Zone-failure tolerance of every kind.** The CP, its disk, the workspaces' volumes and nodes are all in one zone; in a zone outage running workspaces stop with it

`zonal_cluster` replaces the cluster, so decide before the first `apply`; moving an existing deployment is a rebuild.

- A workspace pod's limits must fit the `n2-standard-4`'s ~4 vCPU / 16 GiB
- With a single e2-medium system node (about 0.94 vCPU allocatable) the CP stays `Pending` ("Insufficient cpu") and its claim stays `Pending` with it. That is why the profile uses `e2-standard-2`. Changing `system_machine_type` on a running cluster is an in-place node pool update of about 6 minutes
- The profile has been stood up once, on a zonal cluster (the first `apply` took about 18 minutes, and the CP was healthy about 14 minutes after the overlay was applied). Other zones, a regional variant and other regions are untried, and so are shared-core Cloud SQL tiers (`db-g1-small`)

The cost estimate is in [Pause and see the bill](pause.md#what-the-small-profile-costs).

## 3. Point kubectl at this cluster

Terraform does not touch your kubeconfig. **Before any `kubectl` command**, fetch this cluster's credentials and check that the current context is it — otherwise the namespaces, RBAC and Secrets below land on whatever cluster your kubeconfig pointed at:

```bash
eval "$($TF output -raw get_credentials)"     # gcloud container clusters get-credentials …
kubectl config current-context                # gke_<project>_<region>_<prefix>-gke
kubectl get nodes -L agent-fleet.io/pool      # the system and workspace pools
```

Do the same in every new shell before the procedures on the operating pages.

## 4. Grant the database (once)

The CP's database user is an IAM user with no password and, at first, no rights. Give it the database, as the built-in `postgres` user, from a temporary pod inside the cluster.

- The instance has a private IP only, a workspace is outside the VPC, and `cloud-sql-proxy` is not installed there, so the grant runs from a pod on the cluster's nodes (a short-lived `postgres:17-alpine` pod with a restricted security context)
- Keep the `postgres` password in your vault, not here

### With a terminal

```bash
gcloud sql users set-password postgres --instance "$PREFIX-pg" --project "$PROJECT" --prompt-for-password
DBUSER="$($TF output -raw cp_database_user)"
DBHOST="$(gcloud sql instances describe "$PREFIX-pg" --project "$PROJECT" --format='value(ipAddresses[0].ipAddress)')"
# The statements to paste into psql below (the pod knows nothing of $DBUSER):
echo "GRANT \"$DBUSER\" TO postgres; ALTER DATABASE agentfleet OWNER TO \"$DBUSER\"; ALTER SCHEMA public OWNER TO \"$DBUSER\";"
# The CP namespace does not exist yet (step 6 creates it); `default` does.
kubectl -n default run psql-grant -it --rm --restart=Never --image=postgres:17-alpine \
  --override-type=strategic \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":70,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"psql-grant","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' \
  -- psql "host=$DBHOST user=postgres dbname=agentfleet sslmode=require" -W
```

1. At the `Password:` prompt type the `postgres` password
2. Paste the printed statements at the `psql` prompt. Expect `GRANT ROLE`, `ALTER DATABASE` and `ALTER SCHEMA` back; any `ERROR` means the grant is not done
3. Leave with `\q`

- Interactive `psql` does not exit on a SQL error, so read the replies
- The password goes to `psql` over the pod's terminal only: it is in no command line, pod spec or API object
- The pod is removed on exit (`--rm`); if the session dropped, delete it with `kubectl -n default delete pod psql-grant`

### Without a terminal (a script, CI, an agent's shell)

`-it` and `-W` need a terminal, so the command above cannot run there. Instead, store the password in a temporary Secret, run a non-interactive pod that takes `PGPASSWORD` from it and the statements from `-c`, then delete both. The password stays out of argv and out of the pod spec.

The first line of the block above (`gcloud sql users set-password … --prompt-for-password`) needs a terminal too: without one, set the password with `--password` (it is in that one `gcloud` call's argument list for a moment), and set a new random value afterwards.

```bash
PWDIR="$(mktemp -d)"; chmod 700 "$PWDIR"
cleanup() {
  rm -rf "$PWDIR"                                   # the local copy first: it needs no API
  kubectl -n default delete pod psql-grant --ignore-not-found --wait=false --request-timeout=30s \
    || echo "could not delete pod psql-grant: delete it by hand"
  kubectl -n default delete secret psql-grant-pw --ignore-not-found --request-timeout=30s \
    || echo "WARNING: secret psql-grant-pw may remain (it holds the postgres password): delete it by hand"
}
trap cleanup EXIT                                   # runs under `set -e` too
read -rs -p 'postgres password: ' PGPW; echo        # or read it from your vault
printf '%s' "$PGPW" > "$PWDIR/PGPASSWORD"; unset PGPW   # printf is a builtin: not in any argv
kubectl -n default create secret generic psql-grant-pw --from-file=PGPASSWORD="$PWDIR/PGPASSWORD"
kubectl -n default run psql-grant --restart=Never --image=postgres:17-alpine \
  --override-type=strategic \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":70,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"psql-grant","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}},"env":[{"name":"PGPASSWORD","valueFrom":{"secretKeyRef":{"name":"psql-grant-pw","key":"PGPASSWORD"}}}]}]}}' \
  -- psql -v ON_ERROR_STOP=1 "host=$DBHOST user=postgres dbname=agentfleet sslmode=require" \
  -c "GRANT \"$DBUSER\" TO postgres; ALTER DATABASE agentfleet OWNER TO \"$DBUSER\"; ALTER SCHEMA public OWNER TO \"$DBUSER\";"
kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/psql-grant --timeout=120s \
  || echo "grant pod did not succeed"
kubectl -n default logs psql-grant        # expect GRANT ROLE, ALTER DATABASE, ALTER SCHEMA
```

- The `EXIT` trap removes the password file first, then tries each delete on its own, so one failing `kubectl delete` (expired login, no connection) does not skip the rest; if the Secret could not be deleted it says so and you delete it by hand
- The trap runs even when `wait` times out or a command fails under `set -e`; the `|| echo` keeps `logs` reachable so the `ERROR` can be read before the trap fires
- The password file sits in a mode-700 directory on your machine for the length of the run; use a FIFO or your vault's own file mechanism if that is not acceptable
- If the pod ends `Failed`, `logs` shows the `ERROR` and the grant is not done
- Afterwards set a new `postgres` password (`gcloud sql users set-password … --prompt-for-password`, or a random value you do not keep): the old one was in a Secret object for the length of the run

Only the variant without a terminal has been run as written (with the `read` line replaced by a generated password). The interactive `-it … -W` form has not been run exactly as written.

## 5. Prepare the overlay

```bash
cp -r deploy/kubernetes/overlays/gke "deploy/kubernetes/overlays/$PREFIX"
$TF output -raw kustomize_deployment > "deploy/kubernetes/overlays/$PREFIX/deployment.yaml"
```

Then edit, in the copy:

| File | What to change |
|---|---|
| `cp.env` | `PUBLIC_BASE_URL` and `PUBLIC_DOMAIN` (the `fqdn`), `AF_PREVIEW_DOMAIN` (Terraform's `preview_domain`, or empty), the sign-in settings, `SUPER_ADMIN_EMAILS`. Keep `AF_TRUSTED_PROXY_HOPS=2` ([7. Know the load balancer](#7-know-the-load-balancer)) |
| `kustomization.yaml` | `images:` — the CP image and the release tag |
| `base/cp-data.yaml` | Asks 20 GiB for the CP's own disk; patch it in the copy if the internal git provider will hold more |

### Keep it outside the clone

Keep this copy in your own repository or vault, not in a working copy of this one. Moved out of the clone, two things in `kustomization.yaml` must change:

- **The paths.** `resources: ../../base` and `components: ../../components/gke` and `../../components/params` are relative to `deploy/kubernetes/overlays/<name>/` inside this repository and resolve to nothing elsewhere. Point them at the clone's `deploy/kubernetes/base` and `deploy/kubernetes/components/…` with **relative** paths from your overlay: `kubectl kustomize` rejects an absolute directory reference (`new root '…' cannot be absolute`). Check with `kubectl kustomize <your overlay>` before applying
- **`newName`.** The copied `images:` entry carries a placeholder `newName` (an Artifact Registry path). With the public images (`ghcr.io/k-k1/agent-fleet/…`) delete the `newName` line and keep `newTag`, so the name stays the public one; with your own Artifact Registry repository set `newName` to it

### Archive egress events (optional)

The CP can write each past UTC day of raw egress events to Cloud Storage before it purges them (`AF_EGRESS_ARCHIVE_URL`; [Control outbound traffic](../secure/egress.md#keep-the-records)). It is off by default: with `egress_archive = false` the plan has no bucket, no grant and no new setting for the CP.

| Variable | Default | Meaning |
|---|---|---|
| `egress_archive` | `false` | `true` turns it on |
| `egress_archive_existing_bucket` | `""` | Name of a bucket you already have. The module then manages only the CP's grants on it. Empty: the module creates `<name_prefix>-egress-archive-<project_id>` |
| `egress_archive_prefix` | `""` | Object prefix inside the bucket (the CP already writes under `egress/`). Segments of `A-Za-z0-9._-` joined by single slashes (`team/prod`); no `.`/`..` segment, no `#?%`, no slash at either end |
| `egress_archive_location` | `""` | Location of a created bucket; empty is `region` |
| `egress_archive_retention_days` | `null` | Delete archived objects after this many days (created bucket only). `null` never deletes: the archive is the only copy once the events are purged |

- A created bucket has uniform bucket-level access and public access prevention enforced, and `force_destroy = false`: Destroy stops at a bucket that still holds archived days
- The CP's existing Google service account (the Workload Identity one; no second identity) gets `roles/storage.objectCreator` and `roles/storage.objectViewer` **on that bucket only**
- The viewer role is not optional: the next run reads back a day it already wrote to verify it, and without it that read is a 403 and the purge stops at that day
- A bucket you name is not modified apart from those two members; keep its public access prevention on yourself

`cp.env` is touched only after `terraform output` succeeded: drop the old key and add the current line only when there is one. An empty output means the archive is off, and a key left behind would keep the CP archiving to a bucket it no longer has grants on.

```bash
if line=$($TF output -raw egress_archive_env); then
  sed -i '/^AF_EGRESS_ARCHIVE_URL=/d' "deploy/kubernetes/overlays/$PREFIX/cp.env"
  [ -z "$line" ] || printf '%s\n' "$line" >> "deploy/kubernetes/overlays/$PREFIX/cp.env"
else
  echo "terraform output failed: cp.env left as it was" >&2; false
fi
```

- A failed read (backend error, wrong `-chdir`, output not created yet) is never taken for "archive off": that would delete the key and make the next apply purge events unarchived. The block exits non-zero on a failed read: do not re-apply then
- Then re-apply the overlay. `kustomize` refuses a `cp.env` that repeats the key, so do not append twice
- Not yet verified against a real bucket: the first archive, a second run that finds and verifies the objects, and the 403 when the viewer role is missing. The offline tests check the plan only

## 6. Apply the overlay and create the Secret

The CP reads one Secret, `af-cp-secrets`, which these manifests never contain. Create it once the namespace exists:

```bash
kubectl config current-context                # still this cluster (step 3)
kubectl apply -k "deploy/kubernetes/overlays/$PREFIX"
kubectl -n "$PREFIX-cp" create secret generic af-cp-secrets \
  --from-literal=AF_MASTER_KEY="$(cat master-key)" \
  --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
  --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
```

- Until it exists the CP pod waits in `CreateContainerConfigError`, and starts by itself once it does
- Generate `AF_MASTER_KEY` with `head -c 32 /dev/urandom | base64`, as for compose, and keep it somewhere durable that outlives this cluster (Secret Manager or your vault), not only in the Kubernetes Secret or a file on your machine (P14)
- It is needed again whenever the CP is recreated — a new cluster, or `af-cp-secrets` deleted — and a different key cannot read what the old one encrypted
- Other sign-in secrets (`AF_OIDC_<ID>_CLIENT_SECRET`, `GITHUB_OAUTH_CLIENT_SECRET`) go in the same Secret
- Rotate one with `kubectl create secret … --dry-run=client -o yaml | kubectl apply -f -` and then `kubectl -n "$PREFIX-cp" rollout restart deployment/af-cp`

What the keys must be is in [Handle the deployment keys](../secure/deployment-keys.md).

## 7. Know the load balancer

The Gateway `af-cp` (`components/gke/gateway.yaml`) makes GKE build a **global external** Application Load Balancer — not the classic one, which closes even an active WebSocket at the backend timeout. Terminals, the mirror and the browser pane are WebSockets, so two limits matter:

| WebSocket | When it is cut |
|---|---|
| Idle (a terminal nobody types in, a quiet session) | After the backend service's `timeoutSec`. The default is 30 seconds; the `GCPBackendPolicy` `af-cp` in the same file sets 3600 |
| Active | After 24 hours whatever `timeoutSec` says. That cut cannot be configured away; the Console has to reconnect |

- To change `timeoutSec`, edit it in that file and `kubectl apply -k` the overlay; GKE updates the backend service within a few minutes (`gcloud compute backend-services list --project "$PROJECT"` shows the value)
- The Console pings on the same socket, a round-trip ping every 5 seconds and a heartbeat every 15. A terminal nobody typed in has stayed connected for more than 67 minutes past the 3600-second `timeoutSec`, because the pings kept the connection from looking idle
- A browser may slow or stop those timers for a hidden tab, a frozen page or a sleeping machine; what happens then has not been checked
- The 24-hour cut has not been waited out, but a forced cut (a CP restart) has: the terminal pane shows `[disconnected]`, and clicking it reattaches within a few seconds with the shell and its tmux session intact, without reloading the page. A cut by the load balancer reaches the browser as the same unclean close, so the same recovery applies

### `AF_TRUSTED_PROXY_HOPS` stays 2

The load balancer appends `<client>, <load balancer>` to whatever `X-Forwarded-For` it receives, and the CP counts from the right. With nothing in front of the load balancer, 2 names the real client.

- Raising it for a CDN or proxy in front is only safe when the load balancer cannot be reached except through that proxy (for example a Cloud Armor policy admitting only the proxy's addresses) **and** you have checked which entries the proxy appends or replaces
- Otherwise a client that sends its own `X-Forwarded-For` straight to the load balancer chooses the address the CP sees, and walks past a tenant's network restriction
- This setup supports no proxy in front of the load balancer

## 8. Check it

```bash
kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
kubectl get ns "$PREFIX-ws" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'   # restricted
kubectl -n "$PREFIX-cp" get pvc af-cp-data    # Bound
kubectl -n "$PREFIX-cp" get gateway af-cp     # PROGRAMMED True, ADDRESS = lb_address
curl -sS https://<fqdn>/readyz
```

**Poll `/readyz` itself; `PROGRAMMED True` is not enough.** A rollout that is done does not mean the load balancer is:

- It has taken about 5 to 6 more minutes after the CP was ready before `/readyz` returned 200, and `curl` printed `000` (no connection) in the meantime, although DNS already pointed at the new address
- The Gateway has turned `PROGRAMMED True` and `/readyz` still answered `000` for about 4 minutes after that

Retry the request until it answers:

```bash
until [ "$(curl -sS -m 10 -o /dev/null -w '%{http_code}' https://<fqdn>/readyz)" = 200 ]; do sleep 30; done
```

`kubectl -n "$PREFIX-cp" get gateway af-cp -w` shows the Gateway's own state. A `kubectl wait --for=condition=Programmed gateway/af-cp` would stop too early for the same reason (it has not been tried).

### What the CP's account may do

Every line must answer as shown:

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

### NodeLocal DNSCache is off

That is P11; this prints nothing:

```bash
kubectl -n kube-system get pods -l k8s-app=node-local-dns
```

### Where the workspace runs, and name resolution

Sign in, start a workspace, and check that its pod runs on the workspace pool and resolves names:

```bash
kubectl -n "$PREFIX-ws" get pods -o wide -l agent-fleet.io/workspace
kubectl -n "$PREFIX-ws" exec <pod> -- getent hosts github.com     # an address, not empty
```

### No node answers a workspace

No node answers a workspace, its own node included (P15). The pod itself reports `open` or `closed`.

- Every node line must say `closed`, and the last line, the control, `open`
- An `ERROR` line, or no node lines at all, means nothing was checked

```bash
nodes=$(kubectl get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}') \
  && [ -n "$nodes" ] || echo "ERROR: could not list the nodes"
probe() {  # <ip> <port>
  kubectl -n "$PREFIX-ws" exec <pod> -- bash -c \
    "if timeout 3 bash -c 'exec 3<>/dev/tcp/$1/$2' 2>/dev/null; then echo '$1:$2 open'; else echo '$1:$2 closed'; fi" \
    || echo "$1:$2 ERROR: exec failed"
}
for ip in $nodes; do for port in 22 10250 10256; do probe "$ip" "$port"; done; done
probe 1.1.1.1 443
```

### The home's owner

In that workspace's terminal, the home has to be `dev`'s and writable by nobody else, or `af-gcloud-exec` and `af-aws-exec` refuse to keep their state under it:

```bash
stat -c '%U:%G %A' /home/dev    # dev:dev drwxr-sr-x — no w for group or other (the s, from fsGroup, is harmless)
```

- The home is the directory `.af-home` of the home claim, which an init container running as `dev` creates — and moves an earlier home's files into — before the agent starts
- The claim's root stays `root:dev` with group write, as the kubelet leaves it

## When it goes wrong

| Symptom | Cause and what to do |
|---|---|
| The CP pod stays in `CreateContainerConfigError` | `af-cp-secrets` does not exist yet. Create it in [6.](#6-apply-the-overlay-and-create-the-secret) and the pod starts by itself |
| `curl https://<fqdn>/readyz` prints `000` | The load balancer is not ready yet. It takes minutes; wait with the loop in [8.](#8-check-it) |
| The certificate does not turn `ACTIVE` | The DNS authorisation records do not resolve yet; usually within an hour |
| A workspace pod stays `Pending` and the scale-up says `QUOTA_EXCEEDED` | The SSD quota is used up. [Disk quota (GKE)](overview.md#disk-quota-gke) |
| A workspace pod is stuck in `Init` | `kubectl logs <pod> -c home-layout` names the reason. After a rollback, see [Roll back past the home layout](upgrade.md#roll-back-past-the-home-layout) |
| Every lookup from a workspace fails | NodeLocal DNSCache is on (P11) |
| `kubectl kustomize` says `new root '…' cannot be absolute` | The overlay's paths are absolute. [Keep it outside the clone](#keep-it-outside-the-clone) |

## Related

- [The Kubernetes deployment at a glance](overview.md)
- [Back up and restore](backup.md): give the CP's disk a snapshot schedule after the first start
- [Set up alerts](alerts.md)
- [Set up how people sign in](../signin/overview.md)

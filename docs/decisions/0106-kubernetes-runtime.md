# 0106. Agent Fleet runs on Kubernetes as a `kubernetes` runtime profile, verified first on GKE; until it ships, the answer on Google Cloud is compose on a GCE VM

English | [日本語](0106-kubernetes-runtime.ja.md)

- Status: **proposed** (2026-10-02). Nothing is built or measured yet; the facts below come from
  reading the code, the documentation and the Kubernetes and Google Cloud references, and each
  one that has not been run says so.
- Tracking: #1092
- Reopens: the shelving of Kubernetes in [docs/log/35](../log/35-packaging.md) §35.3-5 and
  `docs/log/roadmap.md` P3-10 ("Helm chart shelved until there is demand; the AWS answer is
  ECS + CFN", 2026-07-21). The **Helm chart itself stays shelved** (decision 12).
- Related: [0045](0045-ec2-persistent-workspace.md) (ecs-ec2, its keep area, and decision 10-1:
  a new substrate is a new profile) / [0047](0047-tenant-network-restriction.md) (the client
  address behind proxies) / [0087](0087-efs-metadata-io.md) (what a network file system costs a
  home) / [0104](0104-long-lived-member-workspace.md) (why a workspace has a persistent home) /
  [docs/build/21](../build/21-add-a-deploy-target.md) (what a target must provide)

## Context

### The demand the shelving waited for

A user whose platform is Google Cloud and Kubernetes asked whether Agent Fleet supports it. For
them the AWS targets are the obstacle: running Agent Fleet today means standing up an AWS
account, CloudFormation and ECS next to the platform they already operate. The shelving decision
in docs/log/35 was "until there is demand"; this is that demand, and it names both halves —
Google Cloud, and Kubernetes.

### What is already portable

- **The per-target differences that matter sit behind the `Runtime` port**
  (`control-plane/internal/runtime/runtime.go`) and the optional capabilities of
  [21 §21.3](../build/21-add-a-deploy-target.md). The few places that compare profile names
  (the native session quota, the Console's slot-pool screen) are listed in
  [21 §21.1](../build/21-add-a-deploy-target.md) and none of them is needed here. The workspace
  image is the same artefact everywhere.
- **The CP boots with no AWS credentials** on `AF_RUNTIME=docker`. Its store is SQLite or plain
  Postgres; the only RDS-specific piece is the optional Secrets Manager rotation reader. Login is
  generic OIDC, and Google is already a first-class provider.
- **Every AWS dependency outside the runtime is gated** by an environment variable or the profile,
  and switches off on compose and native: Cost Explorer showback ([0048](0048-member-cloud-cost.md)),
  Polly, the managed GPU engine mode, the Cloud Map dial fallback (`agent_dial.go`).

### What is AWS-only

The `ecs` and `ecs-ec2` adapters, the CloudFormation stacks, the cost view, Polly and the managed
GPU engines. Of these only the adapter stands between a Kubernetes cluster and a working
deployment; the rest are features a deployment can be without.

### The docker profile already runs on any Linux VM

`deploy/aws/ec2-single` is "compose on one VM" with an AWS-shaped provisioning script. The docker
adapter uses only the standard library and the docker CLI. A GCE VM running the same compose
bundle needs no adapter code — only a runbook, one egress default, and the Google Cloud specifics
of the network in front of it (decision 1).

### What any new substrate must meet

- **The image has no init process.** docker supplies one with `--init`, ECS with
  `initProcessEnabled`. Without one the agent is PID 1 and nobody reaps the processes every CLI
  and tmux exit leaves behind.
- **Two persistent areas**: the home at `/home/dev` and the Claude state at `/var/lib/af/claude`,
  kept apart so that the file browser cannot reach the second and a home reset leaves the login
  alone ([21 §21.2](../build/21-add-a-deploy-target.md)).
- **Recreate and Clean home are not "delete the home".** Recreate removes `~/repos`; Clean home
  removes everything except the seven `homeKeep` entries — the logins, connections and identity
  (`internal/runtime/home_wipe.go`, `runtime_docker.go`). ecs-ec2 already moves those seven out
  of the home into a keep area (`AF_WS_KEEP`, handled by `workspace/entrypoint.sh`), which is
  what lets it delete a whole home volume.
- **The per-start environment carries bearer tokens**, not only the DEK and `AGENT_TOKEN`:
  `workspace_lifecycle.go` mints the internal git, memo, schedule, MCP, docs, engine and OAuth
  tokens into it. ECS passes those as plain task-definition environment today and only the DEK
  and `AGENT_TOKEN` through SSM.
- **The workspace calls the CP back** at `AF_CP_BASE_URL`, which is the public base URL
  (`workspace_lifecycle.go`, `workspace/agent/docs_sync.go`), and the egress proxy, when used,
  runs in the CP. The same variable builds the "Open in Console" links of the Discord and Slack
  notifications (`workspace/agent/internal/bridge/format.go`), so it has to stay public.
- **`starting` stops a Start.** The start handler returns at once, without calling
  `Runtime.Start`, when the state is `running` or `starting` (`workspace_handlers.go`), and
  Recreate and Clean home run Stop, the wipe and that handler in one request. A stop that is still
  in progress must therefore never read as `starting`, or the start that follows it is dropped.
- **No added capabilities on the cloud targets.** docker adds `SYS_ADMIN` to the bounding set for
  Chromium's sandbox; Fargate adds nothing ([07 §7.2](../build/07-security.md)). A substrate that
  forbids added capabilities is therefore at Fargate's level, not below it.

## Decisions

### 1. Google Cloud is answered in two steps; compose on a GCE VM comes first

The first answer is the docker profile on one GCE VM, the counterpart of `deploy/aws/ec2-single`:
a runbook and a `gcloud`-only provisioning script under `deploy/gcp/gce-single/`. It needs no
adapter code, so the user who asked can run Agent Fleet on their own platform while the
Kubernetes profile is built. Its one code change is the egress default:

- **The egress allowlist** gains `.googleapis.com`, as `.amazonaws.com` is there for the AWS
  tooling (`control-plane/egress_policy.go`).

The runbook defines two front ends, and does not support stacking them:

| Front end | `AF_TRUSTED_PROXY_HOPS` | Egress address | Notes |
|---|---|---|---|
| **Caddy on the VM** (the default) | 1, as in compose: stock Caddy replaces an incoming `X-Forwarded-For` with the peer it saw | the VM's reserved static external IP, which is its egress as well — traffic from a VM with an external IP does not go through Cloud NAT | a wildcard certificate for preview subdomains needs DNS-01, which `caddy:2-alpine` lacks; the runbook states the limit |
| **A global external Application Load Balancer**, Caddy removed | 2: the load balancer appends `<client>, <load balancer>`, and `clientip.go` counts from the right | Cloud NAT with a reserved static IP; the VM has no external IP | Certificate Manager with DNS authorisation gives the wildcard certificate. An **active** WebSocket is closed after 24 hours regardless of settings; an **idle** one after the backend service's `timeoutSec` (30 s by default), so the runbook raises it |

The classic Application Load Balancer is not used: it closes even active WebSockets at
`timeoutSec`.

What this step does not give is what compose never gives: one host, no scale-out, and a VM that
bills while its workspaces are stopped.

### 2. The profile is `kubernetes`, not `gke`

The new profile speaks only the standard Kubernetes API. It names no Google Cloud API. What is
specific to GKE — the disk type, the node pools, Workload Identity, the load balancer — is chosen
by the StorageClass, the IaC and the runbook, not by the adapter.

The inquiry named Kubernetes as well as Google Cloud, and a team that runs Kubernetes runs it
somewhere: EKS, AKS and on-premises clusters get the same profile. A `gke` profile would buy
direct use of Google Cloud APIs the first phases do not need, and answer every other cluster with
"not supported". GKE Standard is the first and, until measured elsewhere, the only verified
cluster.

It is a profile of its own, per [0045](0045-ec2-persistent-workspace.md) decision 10-1. `k8s` is
accepted as an alias.

### 3. A workspace is a StatefulSet at 0 or 1 replicas, with its own Service

Each workspace is a StatefulSet named deterministically from the workspace key, scaled between 0
and 1 — the scale-to-zero shape of the ECS service at desired 0/1. A StatefulSet, not a bare Pod
and not a Deployment, because under normal controller operation it **runs at most one pod per
ordinal**: it does not start a replacement until the old pod is gone. The ECS adapters report
`starting` while an old task drains behind a new one, because Service Connect could send one
client's requests to two agents (`serviceRolledOut` in `runtime_ecs.go`); here that overlap does
not occur.

**Stop** sets `replicas: 0`, with `terminationGracePeriodSeconds` from `AF_STOP_GRACE_SEC` — the
same two-stage stop the agent already expects — and **returns only when the pod is gone**, as
`docker stop` does. It waits the grace plus a short margin, which keeps Recreate's Stop, wipe and
Start inside the ingress timeout, and returns an error if the pod is still there by then (a node
that stopped answering, below). A Stop that returns success therefore leaves `stopped`, never a
pod that is still terminating.

**Start** first confirms that no pod exists and returns an error if one does. Then it writes the
workspace's Secret (decision 6), and then the pod template and `replicas: 1` in one update. The
template carries a start generation annotation and the image pinned by digest (decision 9), so
every start produces a new controller revision even when nothing else changed. Because no pod
exists when the Secret is rewritten, no container of an earlier start can read the new values. A
Start that fails after writing the Secret leaves no pod running, and the next Start rewrites it.

**State** is read from the substrate on every call, so a restarted or second CP recovers
everything by name ([21 §21.2](../build/21-add-a-deploy-target.md)):

| State | Condition |
|---|---|
| `none` | no StatefulSet |
| `stopped` | `replicas` is 0 and no pod exists |
| `running` | `replicas` is 1; `status.observedGeneration` has reached `metadata.generation`; and there is a pod that is of `status.updateRevision`, carries the template's current start generation, has no `deletionTimestamp`, and is Ready |
| `starting` | `replicas` is 1 and the `running` condition does not hold — a pod being scheduled, pulling or booting, a controller that has not yet observed the latest template, a rollout in progress |
| `stopped` | `replicas` is 0, whether or not a pod is still terminating |

Readiness is the agent's own `/healthz`, as a readiness probe, so Ready means what `running`
means on the other targets: the agent answers. The generation checks are what keep the old agent
from passing for the new one: right after Start writes the template, the controller may not have
processed it, and `status.updateRevision` still names the old revision. Requiring the observed
generation and the pod's start generation closes that window whatever the controller's timing.

`replicas: 0` with a pod still terminating reads as `stopped` rather than `starting`, so that a
Start is never dropped by the handler's early return; Start's own check then refuses to launch
over the old pod, and the member sees an error to retry instead of a start that silently did not
happen. With Stop waiting for the pod, that state is only seen when a Stop failed.

**A pod on a node that stops answering is not replaced** — the price of the at-most-one
guarantee. The CP's start deadline (`start_deadline.go`) does not end that case: it does not stop
a workspace whose task still counts as running, and setting replicas to 0 does not remove a pod
from a node that cannot be reached. The CP never force-deletes a pod: that frees the name without
proof that the process is dead, and could run two agents on one home. Recovery is the operator's,
by the cluster's own procedure (removing the node, or the out-of-service taint for a non-graceful
node shutdown), and the runbook describes it.

`shareProcessNamespace: true` supplies the init process: the pod's pause container becomes PID 1
and reaps orphans, as tini does under docker. This ADR keeps the shared image unchanged rather
than adding an init to it.

Each workspace has a ClusterIP Service, and `Endpoint()` is its cluster DNS name over HTTP inside
the cluster, guarded by `AGENT_TOKEN` as on every target. Unlike a Service Connect alias, that
name resolves for a Service created after the CP started, so the workaround in `agent_dial.go` has
no counterpart here.

### 4. Two volumes the CP creates: the home, and the state that survives a home reset

Each workspace has two PersistentVolumeClaims, created by the CP under deterministic names:

- **home**, mounted at `/home/dev`;
- **state**, mounted twice through `subPath`: at `/var/lib/af/claude` for the Claude state, and
  at the keep path with `AF_WS_KEEP` set, so that `workspace/entrypoint.sh` moves the seven
  `homeKeep` entries out of the home exactly as it does on ecs-ec2.

The logins normally live on the state volume, but not always: a tool that writes through
write-to-tmp-then-rename replaces the link in the home with a plain file, and the entrypoint
moves the newer copy back only at the next start (`workspace/entrypoint.sh`). So, like ecs-ec2
(`runtime_ecs_ec2_home_wipe.go`), every wipe keeps the seven `homeKeep` names at the top of the
home whatever each one is, and the home operations become:

| Operation | How |
|---|---|
| `WipeHome(repos)` (Recreate) | the CP records the wipe, with a generation number, on the StatefulSet as an annotation and returns; the next Start adds an init container, from the same image, that removes `~/repos` before the agent starts and writes the generation it carried out into the home. A pod restarted later finds that generation and removes nothing, so the annotation never has to be cleared by a template change, which would roll the pod. This is ecs-ec2's "mark, and Start removes it" and returns well inside the ingress timeout |
| `WipeHome(clean)` (Clean home) | the same, removing everything at the top of the home except the seven `homeKeep` names |
| `EraseHome()` (an administrator's Clean home) | with the workspace stopped, the CP runs a one-shot pod from the same image that mounts the home claim, removes what `WipeHome(clean)` removes, and exits; the CP waits for it, which may take as long as Destroy does. The claim itself is kept |
| `ResizeHome()` | raise the home claim's request (decision 4, below) |
| `Destroy()` | decision 5 |

The CP creates the claims itself, rather than through `volumeClaimTemplates`, because a
template's size cannot change after the StatefulSet is created and the home has to grow. Explicit
claims also keep their lifecycle — created on first start, kept on stop, removed by Destroy — in
the adapter's code rather than in a retention policy.

**Access mode `ReadWriteOncePod`** where the CSI driver supports it (GKE's Persistent Disk driver
does), else `ReadWriteOnce`. `ReadWriteOnce` only restricts a volume to one node, not one pod; the
isolation of decision 7 rests on each claim being referenced by one StatefulSet and on members
having no Kubernetes API access, and `ReadWriteOncePod` adds the storage's own guarantee where it
exists.

Block storage, not ReadWriteMany (Filestore, NFS, EFS): [0087](0087-efs-metadata-io.md) measured
what a network file system costs a home that runs git all day, and ecs-ec2 exists because an EBS
home was 8–30× faster on small-file writes ([09 §9.5](../build/09-deploy.md)).

The StorageClass is a deployment setting, and the adapter requires of it:

- **`volumeBindingMode: WaitForFirstConsumer`**, so the volume is created in the zone where the
  pod is scheduled. A new claim stays `Pending` until then, which is normal: Start creates the
  claims and the StatefulSet together and never waits for `Bound` first.
- **`allowVolumeExpansion: true`.** A claim cannot shrink, so the sizing profile reports
  `DiskGrowOnly`. `ResizeHome` raises the request and reports from the claim's status and
  conditions; on a stopped workspace the file system grows at the next mount, which it reports as
  growing, not done.
- **`reclaimPolicy: Delete`**, or Destroy cannot remove the disk (decision 5).

The volume is zonal: a workspace starts only in its volume's zone, and losing that zone makes the
home unreachable until it returns — the exposure an ecs-ec2 home has without its backup.

The pod runs as the image's `dev` uid with `fsGroup` set to its gid, so a fresh volume is
writable without an init container running as root.

### 5. Destroy removes the claims, and reports what it cannot confirm

Destroy stops the workspace and waits for its pod to be gone (a pod still using a claim holds it
through the claim-protection finalizer), then deletes the StatefulSet, the Service, the Secret and
both claims, and waits a bounded time for the claims to disappear. Every step is idempotent, so a
Destroy interrupted halfway is completed by running it again.

Before deleting each claim it records the volume bound to it. With the read-only cluster access
of decision 8 it then confirms that the PersistentVolume object is gone — with
`reclaimPolicy: Delete`, the volume object is removed only after the disk behind it is deleted —
and returns as a known residue ([21 §21.2](../build/21-add-a-deploy-target.md)) every claim or
volume that did not disappear in time, or that it could not read. The runbook makes `Delete` a
precondition, and the CP checks the configured StorageClass at boot; with `Retain`, the disk, its
data and its bill outlive Destroy, and the audit log says so.

### 6. Secrets are referenced, never written into the pod spec

The whole per-start environment the factory receives — the DEK, `AGENT_TOKEN`, and the minted
tokens of the context section — goes into one per-workspace Secret, rewritten at every start, and
reaches the container through `envFrom`. The pod template carries only static, non-secret
settings. This is stricter than ECS, which puts only the DEK and `AGENT_TOKEN` behind SSM.

`secretKeyRef` hides a value from the pod spec, not from someone who can read Secrets or create
pods in the namespace. Decision 7 is what keeps those rights away from members. Secrets in etcd
are only base64 unless the cluster encrypts them; the runbook makes application-layer secret
encryption (Cloud KMS on GKE) a precondition.

### 7. Isolation meets every row of 07 §7.2

**One workspace namespace per deployment.** Tenants and members are separated by the CP, as on
every other target, not by namespaces. Members never get Kubernetes API access to the workspace
namespace; anyone who can create pods there can mount any member's claim and read any Secret, so
that right belongs to the CP and the cluster's administrators alone. The operator sets a
ResourceQuota and LimitRange on the namespace, which caps what the stopped workspaces' claims and
the running pods may take together; a start refused by the quota surfaces as the boot phase.

| Concern | `kubernetes` |
|---|---|
| Files between users | one pair of claims per workspace, referenced by that workspace's StatefulSet alone, `ReadWriteOncePod` where supported |
| Process and memory | one pod per workspace, with requests and limits from the workspace sizing |
| Network | the policies below |
| Privileges | not privileged, `runAsNonRoot`, no added capabilities by default (Fargate's level). `SYS_ADMIN` for Chromium's sandbox is an opt-in where the cluster's policy allows it |
| Cloud identity | `automountServiceAccountToken: false`; the workspace's service account is bound to no cloud identity. On GKE every node pool that can run a workspace uses the GKE metadata server (`GKE_METADATA`), and workspaces are scheduled only onto such pools, so the metadata server never hands a pod the node's credentials. The CP's own Workload Identity is a separate binding (decision 8) |
| Sensitive state | the state volume at `/var/lib/af/claude` and the keep path, as on ecs-ec2 |

**Network policies.** NetworkPolicy only allows; a deny is the absence of an allow, and any other
policy selecting the same pods adds to it. The workspace namespace therefore holds exactly these,
and the runbook forbids adding broader ones:

| From → to | Allowed |
|---|---|
| any → workspace agent port | from the CP's pods only |
| workspace → cluster DNS | yes |
| workspace → CP | yes, to the CP's internal Service (decision 8) |
| workspace → the internet | yes, as `0.0.0.0/0` with every private and link-local range excepted: RFC 1918, `100.64.0.0/10`, `169.254.0.0/16` (the metadata address included). That excludes the nodes, the control-plane endpoint, the pod and service ranges, and the rest of the VPC — Cloud SQL's private address among them |
| workspace → a private address the deployment needs (a LAN engine, an internal git host) | only as an explicit, per-destination rule the operator adds |

NetworkPolicy is enforced only by a CNI that implements it (Dataplane V2 on GKE). A cluster
without one accepts the policies and enforces none of them, so the runbook states it as a
precondition. NetworkPolicy also always lets a pod reach the node it runs on, so the node itself
must not offer an unauthenticated service to pods: the runbook requires the kubelet's read-only
port to be off (GKE's default) and no `hostNetwork` service on workspace nodes, and the live
harness probes the node, the control-plane endpoint and a VPC address from inside a pod. Outbound traffic is open, as on ECS: the egress proxy, when the template
environment points sessions at it, can be bypassed by a process that ignores the proxy variables
([07 §7.8](../build/07-security.md)).

### 8. The CP runs in the cluster; workspaces reach it by an internal address

The CP is a Deployment at one replica in its own namespace, with an internal Service. Its service
account has a Role in the workspace namespace, and a ClusterRole that only reads:

| Kind | Verbs |
|---|---|
| StatefulSets, Services, PersistentVolumeClaims, Secrets | get, list, create, update, patch, delete |
| Pods | get, list, watch — State, TaskCounter and BootPhase read the pod the controller made |
| Events | get, list — the reason a pod cannot be scheduled or pulled, for the boot phase |
| NetworkPolicies | none: they are static, applied with the manifests |
| StorageClasses (ClusterRole) | get, limited by `resourceNames` to the configured class — the boot check of decision 4 |
| PersistentVolumes (ClusterRole) | get — Destroy's confirmation that the disk is gone (decision 5). A volume's object names its disk and claim, nothing in it |

Its store is any Postgres the deployment provides; on Google Cloud the runbook uses Cloud SQL,
reached through Workload Identity bound to the CP's service account alone.

**The workspace's way back to the CP.** `AF_CP_BASE_URL` is the public base URL today, which from
inside the cluster means leaving through NAT and coming back through the load balancer — a route
decision 7 closes for private addresses anyway. The variable has two uses, though: API calls, and
the links in notifications, which a browser opens. So the CP is told its internal Service URL as
well, and the adapter passes it as a second variable, `AF_CP_INTERNAL_URL`, next to an unchanged
`AF_CP_BASE_URL`. About fifteen places in the agent read `AF_CP_BASE_URL` today (the credential
helper, the docs sync, engines, MCP, chat, browser, AWS and branch rules among them). Each one
that sends a request prefers the internal URL when it is set; each one that builds a link for a
person keeps the public one. Sorting them is part of phase 1. If the template environment sets the egress proxy, `NO_PROXY` gets the
internal Service name, so those calls do not go through the proxy. Whether the template environment (`Config.ExtraEnv`, passed today
by docker and native only) reaches the pod is decided with the adapter
([21 §21.2](../build/21-add-a-deploy-target.md)).

Running the CP outside the cluster was not chosen: it would need a reachable API server,
credentials to it and a route to every workspace Service, all of which the cluster gives the CP
for free.

### 9. Images: pulled by the node, pinned at start, compared as fingerprints

- **Pulling is the node's job.** On GKE the node pool's service account reads Artifact Registry;
  on other clusters the deployment names an `imagePullSecrets` entry. The pod's own identity
  plays no part.
- **Start pins the digest.** The CP resolves the configured tag to a digest at each start and
  writes `image@sha256:…` into the template. A node's cache can then never run an older image
  under the same tag, and the template records what this start launched.
- **`Stale()`** follows the rules at the head of `runtime_ecs_stale.go`: fingerprint both sides
  the same way (unwrap a multi-platform index, drop attestation manifests), record the launched
  fingerprint as a template annotation, compare it with the tag's current fingerprint through the
  registry's v2 API, and answer false when in doubt. The CP authenticates to the registry with its
  own identity (Workload Identity on GKE, a pull secret elsewhere), separate from the node's.

### 10. The adapter talks to the API server over HTTPS with the standard library, without client-go

The adapter uses the standard library's HTTP client against the in-cluster API server over HTTPS,
verifying it with the service account's CA, and hand-written types for the fields it reads and
writes. The service account token is a projected token that the kubelet rotates, so the adapter
re-reads it from its file rather than caching it at boot, and retries once with a fresh read on a
401. The surface is the kinds of decision 8; client-go would bring a dependency tree larger than
the rest of the CP's for it, and the docker adapter already shows the house preference: the
standard library and the substrate's own interface.

The cost is that the types are ours to keep correct. Tests run the adapter against recorded API
server responses, and a live harness, gated like the ecs-ec2 one (`AF_ECS_EC2_LIVE=1`), runs it
against a real cluster: Stop then Start at once, a State read between Start's write and the
controller's next status update, a Stop while `starting`, a CP restart in the middle of a start,
both home wipes with a keep file replaced by a plain file, `EraseHome`, a resize while stopped,
and Destroy.

### 11. What the first version claims

| Capability ([21 §21.3](../build/21-add-a-deploy-target.md)) | First version |
|---|---|
| `Runtime`, `runtimeDestroyer` | yes |
| `SizingProfile()` | yes — CPU and memory as requests and limits, disk as the claim size, `DiskGrowOnly` |
| `CostProfile()` | yes, with `Runtime: "kubernetes"` and nothing available, so that version info names the profile instead of falling back to `local` (`cost_profile.go`) |
| `WorkspaceImage()` | yes — the configured image, for the banner and version info |
| `Stale()` | yes (decision 9) |
| `BootPhase()` | yes — from the pod's conditions, its containers' waiting reasons and the namespace's events (scheduling, quota, pulling, starting) |
| `TaskCounter` | yes — pods whose workspace container is running, **whether or not they are Ready**. A running agent that has stopped answering is still a task, and the start deadline must not stop it; an unreadable answer is an error, which the deadline treats as running |
| `WipeHome()`, `EraseHome()`, `ResizeHome()` | yes (decision 4) |
| `DocsMounter` | no — the guide is fetched from `GET /internal/docs`, as on ECS |
| `BeginHibernate()`, `BackupHome()`, `HomeBackups()` | no — VolumeSnapshot is the natural mechanism, later work |
| Golden seeding, the slot pool | no |

Each row gets an assertion in `capabilities_test.go` or `runtime_test.go`, in both directions.

### 12. Infrastructure is Terraform on the Google Cloud side and plain manifests in the cluster; the Helm chart stays shelved

- `deploy/kubernetes/` holds plain manifests with a kustomize base: the namespaces, the CP's
  Deployment, Service, service account and Role, the network policies of decision 7, and an
  example ResourceQuota. Any cluster can apply them.
- `deploy/gcp/gke/` holds Terraform for what a GKE deployment needs around the cluster: the VPC,
  the cluster with Dataplane V2, secret encryption and a workspace node pool on the GKE metadata
  server, the StorageClass of decision 4, Cloud SQL, Cloud NAT with a static address, Cloud DNS,
  the load balancer and Certificate Manager.

Terraform because Google Cloud's own template service is closing: Deployment Manager lost support
on 2026-04-01, refuses new users from 2026-06-30 and shuts down after 2027-06-30, and its
successor, Infrastructure Manager, runs Terraform. It is the repository's first Terraform, and an
operator's first new tool for it.

The AWS targets stay on CloudFormation. Moving them would rewrite seven stacks (about 3,600
lines), the checks built on them (`cfn-equiv.py`, `cfn-contract.py`, the tag-fence test) and the
standup, update and teardown scripts, and every running deployment would have to import its
resources into Terraform state — a migration with no user asking for it. Two IaC languages is
the cost of meeting each cloud in its own; the shared part is the CP and the image, not the
templates.

The Helm chart is not built. The inquiry asked for Kubernetes support, not for a chart, and a
kustomize base serves the same clusters without a second packaging format to keep in step. It is
reopened when someone asks to install Agent Fleet through Helm.

### Out of scope

- **Parity with ecs-ec2**: hibernation, cross-zone backup and golden seeding. On Kubernetes,
  VolumeSnapshot and a claim's `dataSource` replace most of ecs-ec2's state machine, so they are
  a rewrite, not a port — a decision of its own when needed.
- **Renaming** the AWS-named types the CP exposes (`EC2PoolStatus`, `WorkspaceSlot.InstanceType`,
  the AWS line items of `CostProfile`). Nothing here claims them, so nothing forces the rename.
- **A cost view** (Cloud Billing export to BigQuery), **Cloud Text-to-Speech**, **managed GPU
  engines on Google Cloud**. A GCE GPU VM declared as an `external` engine row works today.
- **The user-facing Google Cloud tooling** inside workspaces (a `gcloud` counterpart of
  `af-aws-exec`).
- **A namespace per tenant.** One workspace namespace per deployment is the first version
  (decision 7).

## Rejected

- **A `gke` profile.** Covered by decision 2: it gives up every other cluster to use APIs the
  first version does not need.
- **Cloud Run.** No persistent block volume for the home, and request-oriented lifetimes for
  sessions that run for hours. It cannot meet [0104](0104-long-lived-member-workspace.md).
- **A VM pool on GCE, porting ecs-ec2.** ecs-ec2's state machine is built on EBS attachment, SSM
  commands and instance tags. GCE has no clean counterpart to SSM SendCommand, and Kubernetes
  already does the scheduling and volume attachment the pool hand-builds.
- **A ReadWriteMany home on Filestore.** Covered by decision 4 and [0087](0087-efs-metadata-io.md).
- **Deleting the home claim for Recreate and Clean home.** It would delete the logins with it,
  and Recreate keeps everything but `~/repos`. Decision 4 moves the logins out instead.
- **An operator with a custom resource.** It would make the CP a controller with a reconcile loop
  and a CRD to version. The adapter model — stateless, everything found by name — already holds
  on two cloud targets.
- **client-go.** Covered by decision 10.
- **A Helm chart now.** Covered by decision 12.

## Consequences

- A fifth adapter to keep in step with the `Runtime` contract, and no coverage from the fleet E2E
  suite, which boots the docker profile only. A kind cluster in CI (the runners have Docker) is
  the way to close that, decided with the harness.
- The CP learns a second address for itself, and the agent a second variable for it (decision 8),
  which only this profile sets.
- A Stop waits for the pod to be gone, so a node that stops answering turns Stop, Recreate and
  Clean home into errors until an operator acts.
- A node that stops answering leaves its workspace `starting` until an operator acts.
- An operator on Google Cloud learns Terraform and kustomize, where an AWS operator needs only
  the AWS CLI.
- A home lives in one zone until backup exists.
- The GCE step ships without adapter code, so the Google Cloud specifics of decision 1 are proven
  before any adapter code depends on them.

## Open questions (decide after measuring)

1. **Chromium's sandbox without `SYS_ADMIN`.** Fargate runs without it; whether the browser pane
   behaves the same on a GKE pod is measured, not assumed.
2. **Start latency.** The workspace image is several gigabytes. Node image caching, and on
   Autopilot the node scale-up, decide whether a start fits the CP's expectations; measure a cold
   and a warm start the way [09 §9.5](../build/09-deploy.md) broke down Fargate's.
3. **The WebSocket behind the load balancer.** Which `timeoutSec` covers an idle terminal, and
   whether the Console reconnects a terminal transparently at the 24-hour cut.
4. **GKE Autopilot.** The Fargate-like option. It forbids `SYS_ADMIN` and scales nodes on demand;
   verified after Standard.

## Phases

| Phase | What | Done when |
|---|---|---|
| 0 | Decision 1: the GCE runbook and script, the egress default, the guide pages | a session runs on a GCE VM behind Caddy, and behind a global external Application Load Balancer, where an idle terminal outlives the default `timeoutSec` and a cut connection reconnects |
| 1 | Decisions 2–12: the adapter, `deploy/kubernetes/`, `deploy/gcp/gke/` | on GKE Standard the live harness of decision 10 passes, and the isolation rows and network policies of decision 7 are checked from inside a pod, including the node, the control-plane endpoint and a VPC address |
| 2 | Autopilot, and whatever of "Out of scope" is asked for | each its own issue |

## What would make us revisit it

- A user who needs Helm specifically (decision 12).
- A cluster where the standard API is not enough for the home — for example, no StorageClass with
  expansion and `WaitForFirstConsumer`.
- Measurements showing that start latency on Kubernetes is far from ECS's, which would put a pool
  of pre-warmed nodes back on the table.
- Tenants that must be separated by the cluster rather than by the CP, which would bring a
  namespace per tenant back.

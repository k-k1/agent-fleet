# 0106. Agent Fleet runs on Kubernetes as a `kubernetes` runtime profile, verified first on GKE; until it ships, the answer on Google Cloud is compose on a GCE VM

English | [日本語](0106-kubernetes-runtime.ja.md)

- Status: **proposed** (2026-10-02). Nothing is built or measured yet; the facts below come from
  reading the code and the documentation, and each one that has not been run says so.
- Tracking: #1092
- Reopens: the shelving of Kubernetes in [docs/log/35](../log/35-packaging.md) §35.3-5 and
  `docs/log/roadmap.md` P3-10 ("Helm chart shelved until there is demand; the AWS answer is
  ECS + CFN", 2026-07-21). The **Helm chart itself stays shelved** (decision 10).
- Related: [0045](0045-ec2-persistent-workspace.md) (ecs-ec2 and decision 10-1, a new substrate
  is a new profile) / [0047](0047-tenant-network-restriction.md) (the client address behind
  proxies) / [0087](0087-efs-metadata-io.md) (what a network file system costs a home) /
  [0104](0104-long-lived-member-workspace.md) (why a workspace has a persistent home) /
  [docs/build/21](../build/21-add-a-deploy-target.md) (what a target must provide)

## Context

### The demand the shelving waited for

A user whose platform is Google Cloud and Kubernetes asked whether Agent Fleet supports it. For
them the AWS targets are the obstacle: running Agent Fleet today means standing up an AWS
account, CloudFormation and ECS next to the platform they already operate. The shelving decision
in docs/log/35 was "until there is demand"; this is that demand, and it names both halves —
Google Cloud, and Kubernetes.

### What is already portable

- **The core never asks which cloud it is on.** Everything that differs per target sits behind
  the `Runtime` port (`control-plane/internal/runtime/runtime.go`) and the optional capabilities
  of [21 §21.3](../build/21-add-a-deploy-target.md). The workspace image is the same artefact
  everywhere ([21 §21.1](../build/21-add-a-deploy-target.md)).
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
bundle needs no code — only a runbook and the Google Cloud specifics of the network in front of
it (decision 1).

### Three facts about the workspace that any new substrate must meet

- **The image has no init process.** docker supplies one with `--init`, ECS with
  `initProcessEnabled`. Without one the agent is PID 1 and nobody reaps the processes every CLI
  and tmux exit leaves behind. Kubernetes has no such flag, and adding tini to the image is ruled
  out by [21 §21.1](../build/21-add-a-deploy-target.md).
- **Two persistent areas**: the home at `/home/dev` and the Claude state at `/var/lib/af/claude`,
  kept apart so that the file browser cannot reach the second and a home reset leaves the login
  alone ([21 §21.2](../build/21-add-a-deploy-target.md)).
- **No added capabilities on the cloud targets.** docker adds `SYS_ADMIN` to the bounding set for
  Chromium's sandbox; Fargate adds nothing ([07 §7.2](../build/07-security.md)). A substrate that
  forbids added capabilities is therefore at Fargate's level, not below it.

## Decisions

### 1. Google Cloud is answered in two steps; compose on a GCE VM comes first

The first answer is the docker profile on one GCE VM, the counterpart of `deploy/aws/ec2-single`:
a runbook and a `gcloud`-only provisioning script under `deploy/gcp/gce-single/`, with Caddy and
Let's Encrypt in front. It ships without touching the CP, so the user who asked can run Agent
Fleet on their own platform while the Kubernetes profile is built. It also settles the Google
Cloud specifics that do not depend on the runtime, and the Kubernetes profile inherits them:

- **The egress allowlist** gains `.googleapis.com`, as `.amazonaws.com` is there for the AWS
  tooling (`control-plane/egress_policy.go`).
- **A Google Cloud load balancer in front** caps a WebSocket's lifetime at the backend service's
  `timeoutSec` (30 s by default), which would cut every terminal; the runbook raises it. It also
  appends both the client and its own address to `X-Forwarded-For`, so the deployment sets
  `AF_TRUSTED_PROXY_HOPS=2`, not 1. `clientip.go` already counts from the right; no code changes.
- **A fixed egress address** is Cloud NAT with a reserved static IP — the counterpart of the
  retained NAT EIP.
- **Preview subdomains** need a wildcard certificate. The stock `caddy:2-alpine` has no DNS
  plugin, so the runbook uses Certificate Manager's DNS authorisation when a load balancer is in
  front, and documents the limit when Caddy is.

What this step does not give is what compose never gives: one host, no scale-out, and a VM that
bills while its workspaces are stopped.

### 2. The profile is `kubernetes`, not `gke`

The new profile speaks only the standard Kubernetes API: `apps/v1` StatefulSet, `v1` Service,
PersistentVolumeClaim and Secret, `networking.k8s.io/v1` NetworkPolicy. It names no Google Cloud
API. What is specific to GKE — the disk type, the node pool, Workload Identity, the load balancer —
is chosen by the StorageClass, the IaC and the runbook, not by the adapter.

The inquiry named Kubernetes as well as Google Cloud, and a team that runs Kubernetes runs it
somewhere: EKS, AKS and on-premises clusters get the same profile. A `gke` profile would buy
direct use of Google Cloud APIs the first phases do not need, and answer every other cluster with
"not supported". GKE Standard is the first and, until measured elsewhere, the only verified
cluster.

It is a profile of its own, per [0045](0045-ec2-persistent-workspace.md) decision 10-1. `k8s` is
accepted as an alias.

### 3. A workspace is a StatefulSet at 0 or 1 replicas, with its own Service

Each workspace is a StatefulSet named deterministically from the workspace key, scaled between 0
and 1 — the same scale-to-zero shape as the ECS service at desired 0/1. The substrate is the
source of truth ([21 §21.2](../build/21-add-a-deploy-target.md)): `State` is read from the
StatefulSet's replicas and its pod's phase and readiness, so a restarted or second CP recovers
everything by name.

A StatefulSet, not a bare Pod and not a Deployment, because it **guarantees at most one pod per
ordinal**. The ECS adapters report `starting` for as long as an old task drains behind a new one,
because Service Connect could send one client's requests to two agents (`serviceRolledOut` in
`runtime_ecs.go`). A StatefulSet never runs the new pod before the old one is gone, so that
window does not exist. The price is the other side of the same guarantee: a pod on a node that
stops answering is not replaced until the node is declared gone and the pod deleted. The CP's
start deadline (`start_deadline.go`) bounds that as it bounds every other stuck `starting`.

Each start writes the pod template (the per-start environment of
[21 §21.2](../build/21-add-a-deploy-target.md) can differ between starts) and the replica count
in one update. `Stop` sets replicas to 0 with `terminationGracePeriodSeconds` from
`AF_STOP_GRACE_SEC`, the same two-stage stop the agent already expects.

`shareProcessNamespace: true` supplies the init process: the pod's pause container becomes PID
1 and reaps orphans, as tini does under docker. The image is unchanged.

Each workspace has a ClusterIP Service, and `Endpoint()` is its cluster DNS name. Unlike a
Service Connect alias, that name resolves for a Service created after the CP started, so the
workaround in `agent_dial.go` has no counterpart here.

### 4. The home is a ReadWriteOnce volume the CP creates, not a shared file system

Each workspace has two PersistentVolumeClaims, one per persistent area. The CP creates them
itself under deterministic names, rather than through the StatefulSet's `volumeClaimTemplates`,
because a template's size cannot change after creation and its claims outlive the StatefulSet
with no owner the CP can see. The StorageClass is a deployment setting; on GKE the runbook uses
a `pd-balanced` class with `WaitForFirstConsumer` and volume expansion allowed.

ReadWriteOnce block storage, not ReadWriteMany (Filestore, NFS, EFS): [0087](0087-efs-metadata-io.md)
measured what a network file system costs a home that runs git all day, and ecs-ec2 exists
because an EBS home was 8–30× faster on small-file writes ([09 §9.5](../build/09-deploy.md)).
A ReadWriteOnce volume also follows one pod, which is what a home is.

What this gives without further work: `ResizeHome` by expanding the claim; `WipeHome` and
`EraseHome` by deleting and recreating a claim while the workspace is stopped; `Destroy` by
deleting the StatefulSet, the Service, both claims and the Secret. The volume is zonal, so a
workspace starts only in its volume's zone, and losing that zone makes the home unreachable until
it returns — the same exposure as an ecs-ec2 home before its backup (decision 9).

The pod runs as the image's `dev` uid with `fsGroup` set to its gid, so a fresh volume is
writable without an init container running as root.

### 5. Secrets are referenced, never written into the pod spec

The DEK and `AGENT_TOKEN` go into a per-workspace Secret and reach the container through
`secretKeyRef`. The pod spec, which anyone with read access to the namespace can see, carries
only the reference — the counterpart of ECS's SSM SecureString reference
([09 §9.5](../build/09-deploy.md)). Secrets in etcd are only base64 unless the cluster encrypts
them; the runbook makes application-layer secret encryption (Cloud KMS on GKE) a precondition.

### 6. Isolation meets every row of 07 §7.2

| Concern | `kubernetes` |
|---|---|
| Files between users | one pair of volumes per workspace, mounted by that pod alone |
| Process and memory | one pod per workspace, with requests and limits from the workspace sizing |
| Network | workspaces live in their own namespace with a default-deny NetworkPolicy. Ingress to the agent port is admitted from the CP's pods only; egress to the node metadata address is denied |
| Privileges | not privileged, `runAsNonRoot`, no added capabilities by default (Fargate's level). `SYS_ADMIN` for Chromium's sandbox is an opt-in where the cluster's policy allows it |
| Cloud identity | `automountServiceAccountToken: false`; the workspace's service account is bound to no cloud identity. On GKE, Workload Identity Federation is required so that the metadata server never hands a pod the node's credentials |
| Sensitive state | the second volume at `/var/lib/af/claude`, as on every target |

NetworkPolicy is enforced only by a CNI that implements it (Dataplane V2 on GKE). A cluster
without one silently accepts the policies and enforces none of them, so the runbook states it as
a precondition, and the CP logs a warning at boot when it can tell that no enforcement is present.

### 7. The CP runs in the cluster and holds a namespaced role

The CP is a Deployment at one replica in its own namespace. Its service account has a Role in
the workspace namespace covering exactly the kinds of decision 2, and no ClusterRole. Its store
is any Postgres the deployment provides; on Google Cloud the runbook uses Cloud SQL.

Running it outside the cluster was not chosen: it would need a reachable API server, credentials
to it and a route to every workspace Service, all of which the cluster gives the CP for free.

### 8. The adapter talks to the API server over plain HTTP, without client-go

The adapter uses the standard library's HTTP client with the in-cluster service account token
and CA, and hand-written types for the fields it reads and writes. The surface is five kinds and
four verbs. client-go would bring a dependency tree larger than the rest of the CP's for that
surface, and the docker adapter already shows the house preference: the standard library and the
substrate's own interface.

The cost is that the types are ours to keep correct. Tests run the adapter against recorded API
server responses, and a live harness, gated like the ecs-ec2 one (`AF_ECS_EC2_LIVE=1`), runs it
against a real cluster.

### 9. What the first version claims

| Capability ([21 §21.3](../build/21-add-a-deploy-target.md)) | First version |
|---|---|
| `Runtime`, `runtimeDestroyer` | yes |
| `SizingProfile()` | yes — CPU and memory as requests and limits, disk as the claim size |
| `Stale()` | yes — the image digest from the registry's v2 API (Artifact Registry speaks it) |
| `BootPhase()` | yes — from the pod's conditions (scheduling, pulling, starting) |
| `TaskCounter` | yes — the StatefulSet's ready replicas |
| `WipeHome()`, `EraseHome()`, `ResizeHome()` | yes (decision 4) |
| `DocsMounter` | no — the guide is fetched from `GET /internal/docs`, as on ECS |
| `CostProfile()` | no — a cost view for Google Cloud is the billing export, later work |
| `BeginHibernate()`, `BackupHome()`, `HomeBackups()` | no — VolumeSnapshot is the natural mechanism, later work |
| Golden seeding, the slot pool | no |

Each row gets an assertion in `capabilities_test.go` or `runtime_test.go`, in both directions.

### 10. Infrastructure is Terraform on the Google Cloud side and plain manifests in the cluster; the Helm chart stays shelved

- `deploy/kubernetes/` holds plain manifests with a kustomize base: the namespaces, the CP's
  Deployment, service account and Role, the default-deny policies. Any cluster can apply them.
- `deploy/gcp/gke/` holds Terraform for what a GKE deployment needs around the cluster: the VPC,
  the cluster with Dataplane V2, Workload Identity and secret encryption, Cloud SQL, Cloud NAT
  with a static address, Cloud DNS, the load balancer and Certificate Manager.

Terraform because Google Cloud has no native template language left: Deployment Manager is
retired and its successor, Infrastructure Manager, runs Terraform. It is the repository's first
Terraform, and an operator's first new tool for it.

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

## Rejected

- **A `gke` profile.** Covered by decision 2: it gives up every other cluster to use APIs the
  first version does not need.
- **Cloud Run.** No persistent block volume for the home, and request-oriented lifetimes for
  sessions that run for hours. It cannot meet [0104](0104-long-lived-member-workspace.md).
- **A VM pool on GCE, porting ecs-ec2.** ecs-ec2's state machine is built on EBS attachment, SSM
  commands and instance tags. GCE has no clean counterpart to SSM SendCommand, and Kubernetes
  already does the scheduling and volume attachment the pool hand-builds.
- **A ReadWriteMany home on Filestore.** Covered by decision 4 and [0087](0087-efs-metadata-io.md).
- **An operator with a custom resource.** It would make the CP a controller with a reconcile loop
  and a CRD to version. The adapter model — stateless, everything found by name — already holds
  on two cloud targets.
- **client-go.** Covered by decision 8.
- **A Helm chart now.** Covered by decision 10.

## Consequences

- A fifth adapter to keep in step with the `Runtime` contract, and no coverage from the fleet E2E
  suite, which boots the docker profile only. A kind cluster in CI (the runners have Docker) is
  the way to close that, decided with the harness.
- An operator on Google Cloud learns Terraform and kustomize, where an AWS operator needs only
  the AWS CLI.
- A home lives in one zone until backup exists.
- The GCE step ships with no code change, so the Google Cloud specifics of decision 1 are proven
  before any adapter code depends on them.

## Open questions (decide after measuring)

1. **Chromium's sandbox without `SYS_ADMIN`.** Fargate runs without it; whether the browser pane
   behaves the same on a GKE pod is measured, not assumed.
2. **Start latency.** The workspace image is several gigabytes. Node image caching, and on
   Autopilot the node scale-up, decide whether a start fits the CP's expectations; measure a cold
   and a warm start the way [09 §9.5](../build/09-deploy.md) broke down Fargate's.
3. **The WebSocket lifetime behind the load balancer.** Which `timeoutSec` to set, and whether the
   Console reconnects a terminal transparently when it is reached.
4. **Egress enforcement.** The allowlist is the CP's proxy. Whether the template environment
   (`Config.ExtraEnv`, today passed by docker and native only) reaches the pod is decided with the
   adapter ([21 §21.2](../build/21-add-a-deploy-target.md)).
5. **GKE Autopilot.** The Fargate-like option. It forbids `SYS_ADMIN` and scales nodes on demand;
   verified after Standard.

## Phases

| Phase | What | Done when |
|---|---|---|
| 0 | Decision 1: the GCE runbook and script, the egress default, the guide pages | a session runs on a GCE VM behind Caddy, and behind a load balancer with the terminal surviving past `timeoutSec`'s default |
| 1 | Decisions 2–9: the adapter, `deploy/kubernetes/`, `deploy/gcp/gke/` | a member's workspace starts, stops, resizes and is wiped on GKE Standard, with the isolation rows of decision 6 checked from inside a pod |
| 2 | Autopilot, and whatever of "Out of scope" is asked for | each its own issue |

## What would make us revisit it

- A user who needs Helm specifically (decision 10).
- A cluster where the standard API is not enough for the home — for example, no StorageClass with
  ReadWriteOnce volumes and expansion.
- Measurements showing that start latency on Kubernetes is far from ECS's, which would put a pool
  of pre-warmed nodes back on the table.

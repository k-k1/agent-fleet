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

**The workspace must not reach the VM's metadata server.** On EC2 the docker adapter's
`AWS_EC2_METADATA_DISABLED` and the host's hop limit of 1 keep a workspace away from the instance
role (`runtime_docker.go`, `ec2-single/cfn.yaml`). GCE has no hop limit and no SDK-wide switch:
any process that can reach `169.254.169.254` with `Metadata-Flavor: Google` gets the VM service
account's token. So the provisioning script does two things. The VM runs with no service account,
or one holding no roles — the DNS record and the certificate need none on the VM, since Caddy uses
HTTP-01 and the operator creates the record with their own credentials. And a host firewall rule,
installed at every boot, drops traffic from every Docker bridge to every metadata address the VM
has: `169.254.169.254`, and `fd20:ce::254` where the VM has IPv6. A service account removed does
not make the instance's and the project's custom metadata private, so the rule is what keeps
workspaces away from it. Phase 0 is not done until a request from inside a workspace to each of
those addresses fails.

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

**Stop** sets `replicas: 0`, with `terminationGracePeriodSeconds` from `stopGraceSec()` and
`AGENT_STOP_GRACE_SEC` from `agentStopGraceSec()` in the pod's environment, as every other adapter
injects it (`runtime_docker.go`) — the same two-stage stop the agent already expects, with the same
120-second ceiling, so that one `AF_STOP_GRACE_SEC` stays valid on every target. Stop **returns only when the stop has settled**, as
`docker stop` does. Settled means three things, checked in this order: the controller has observed
the generation Stop wrote (`status.observedGeneration`), it reports no replicas
(`status.replicas` is 0), and no pod of the workspace exists. The first two matter because a
controller that read `replicas: 1` just before the Stop can still be creating a pod, which would
then appear after a check that saw none. Stop waits the grace plus a short margin, which keeps
Recreate's Stop, wipe and Start inside the ingress timeout, and returns an error if the stop has
not settled by then (a node that stopped answering, below).

**Start** first requires the same settled condition — or no StatefulSet at all — and returns an
error otherwise. Then it writes the workspace's Secret (decision 6), and then the pod template and
`replicas: 1` in one update, and returns: like the ECS adapters it does not wait for the agent's
`/healthz`, and `State` follows the convergence ([21 §21.2](../build/21-add-a-deploy-target.md)).
The template carries a start generation annotation and the image pinned by digest (decision 9), so
every start produces a new controller revision even when nothing else changed. Because no pod
exists when the Secret is rewritten, no container of an earlier start can read the new values. A
Start that fails after writing the Secret leaves no pod running, and the next Start rewrites it.

**State** is read from the substrate on every call, so a restarted or second CP recovers
everything by name ([21 §21.2](../build/21-add-a-deploy-target.md)):

| State | Condition |
|---|---|
| `none` | no StatefulSet |
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
happen. With Stop waiting for the stop to settle, that state is only seen when a Stop failed.

The price is that `stopped` no longer proves that nothing runs, and the CP's callers use the state
as exactly that proof: after a failed Stop they go on to wipe a home unless
`runtime.WorkspaceAlive(State())` says otherwise (`workspace_handlers.go`,
`workspace_lifecycle.go`). So the adapter's own destructive operations do not trust the state.
`WipeHome` only records a mark, and the wipe runs at the next Start, which itself requires a
settled stop. `EraseHome` and `Destroy` check the settled condition themselves before touching a
volume, and return an error when it does not hold.

**A pod on a node that stops answering is not replaced** — the price of the at-most-one
guarantee. The CP's start deadline (`start_deadline.go`) does not end that case: it does not stop
a workspace whose task still counts as running, and setting replicas to 0 does not remove a pod
from a node that cannot be reached. The CP never force-deletes a pod: that frees the name without
proof that the process is dead, and could run two agents on one home. Recovery is the operator's,
and it starts with that proof: first make sure the old process cannot run — the VM stopped or
deleted, confirmed from the cloud provider, not from Kubernetes — and only then remove the Node
or apply the out-of-service taint. Deleting a Node object does not stop its VM, and the taint
detaches volumes; during a network partition, where the VM's state cannot be confirmed, nothing is
done. The runbook describes the procedure in that order.

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
| `EraseHome()` (an administrator's Clean home) | after checking the settled stop itself, the CP runs a one-shot erase pod from the same image — a deterministic name and label, `restartPolicy: Never` — that mounts the home claim, removes what `WipeHome(clean)` removes, and exits. The CP waits for it, which may take as long as Destroy does, reads its result, and deletes it. The claim itself is kept |
| `ResizeHome()` | raise the home claim's request (see the StorageClass requirements below) |
| `Destroy()` | decision 5 |

The init container and the erase pod run a shell command the CP builds, as ecs-ec2 builds the one
it sends over SSM (`homeWipeCommand` in `runtime_ecs_ec2_home_wipe.go`): `sh`, `find` and `rm`
from the image, and the keep list from Go's `homeKeep`. The image is unchanged, and the keep list
has one source in the CP; a test pins it to the entrypoint's defaults (`AF_WS_KEEP_DIRS`,
`AF_WS_KEEP_FILES`), which hold the same seven names.

An administrator's Clean home runs under a five-minute budget once it leaves the request
(`homeEraseBudget` in `workspace_lifecycle.go`). An erase that outlives it is an error, and the
erase pod keeps running; the next `EraseHome` finds it by name and waits for it.

A finished erase pod is not removed by anyone else, and while it exists it holds the home claim
through the claim-protection finalizer. So the erase pod is found by its name whenever it matters:
`EraseHome` waits for one still running and deletes one that finished (a CP that died halfway is
resumed this way); `Start` refuses while one runs and deletes one that finished; `Destroy`
deletes it before the claims. It is not a workspace pod: the settled-stop check and `State` look
only at the StatefulSet's own pods.

The CP creates the claims itself, rather than through `volumeClaimTemplates`, because a
template's size cannot change after the StatefulSet is created and the home has to grow. Explicit
claims also keep their lifecycle — created on first start, kept on stop, removed by Destroy — in
the adapter's code rather than in a retention policy.

**Access mode `ReadWriteOncePod`** where the CSI driver supports it (to be confirmed for GKE's
Persistent Disk driver on the pinned version in phase 1), else `ReadWriteOnce`. `ReadWriteOnce` only restricts a volume to one node, not one pod; the
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

The pod runs as the image's `dev` uid with `fsGroup` set to its gid, which should make a fresh
volume writable without an init container running as root. That is expected, not measured: the
state volume is mounted through `subPath`, and the kubelet creates a `subPath` directory itself.
The live harness checks that `dev` can write to every mount of a new workspace, the keep links
included. Running anything as root is not a fallback: the `restricted` level of decision 7 forbids
it in init containers too, and the level is not lowered to make room. If the `subPath` layout fails
that check, the layout changes instead: the state volume is mounted once, at a path of its own,
with `CLAUDE_CONFIG_DIR` and `AF_WS_KEEP` pointed at two directories under it. The entrypoint only
moves the keep entries when `AF_WS_KEEP` already exists as a writable directory
(`workspace/entrypoint.sh`), so an init container from the same image, running as `dev`, creates
both directories first. The harness then starts that layout from an empty state volume and checks
that all seven keep entries end up as links into it.

### 5. Destroy removes the claims, and reports what it cannot confirm

Destroy stops the workspace and requires the settled stop of decision 3 (a pod still using a claim
holds it through the claim-protection finalizer, and so does a leftover erase pod, which it
deletes). Then:

1. It writes an inventory onto the StatefulSet as an annotation: each claim's UID and the name of
   the volume bound to it. The StatefulSet is the last thing Destroy deletes, so a CP that dies
   anywhere after this step finds the inventory again — a volume name is generated by the
   provisioner and cannot be listed back through the namespaced role.
2. It deletes the Service, the Secret and both claims, and waits a bounded time for the claims to
   disappear.
3. With the read-only cluster access of decision 8 it confirms that each inventoried
   PersistentVolume object is gone — with `reclaimPolicy: Delete` and the PersistentVolume
   deletion-protection finalizer (stable in Kubernetes 1.33, and honoured by the CSI
   provisioner), the volume object is removed only after the disk behind it is deleted — and
   returns as a known residue
   ([21 §21.2](../build/21-add-a-deploy-target.md)) every claim or volume that did not disappear
   in time, or that it could not read.
4. It deletes the StatefulSet **only when every claim and volume of the inventory is confirmed
   gone**. Otherwise it leaves the StatefulSet, inventory and all, and returns it as a residue too.
   The CP records the residue only after Destroy returns (`workspace_lifecycle.go` deletes the
   workspace row and hands the list back afterwards), so deleting the only record of a volume
   still on disk before then would let a crash lose track of it for good. A re-run reads the
   existing inventory and adds to it, never replacing it with one rebuilt from claims that are
   already gone.

The runbook makes `Delete` and the finalizer's Kubernetes version preconditions, and the CP checks
the configured StorageClass at boot. On a cluster without the finalizer, a volume object that is
gone is not proof that the disk is, and Destroy reports such volumes as unconfirmed. With `Retain`,
the disk, its data and its bill outlive Destroy, and the audit log says so.

Residues are strings of the form `statefulset:<namespace>/<name>`, `pvc:<namespace>/<name>` and
`pv:<name>`, so the audit log names exactly what to look for. Every step is idempotent and starts
from what the substrate shows, so a Destroy interrupted before it returned — the CP crashed, or
the stop did not settle — is completed by running it again. Once Destroy has returned a residue,
the CP deletes the workspace row and there is nothing left to run it on; the StatefulSet that kept
the inventory is then the record, and removing what it names is a runbook procedure.

### 6. Secrets are referenced, never written into the pod spec

The whole per-start environment the factory receives — the DEK, `AGENT_TOKEN`, and the minted
tokens of the context section — goes into one per-workspace Secret, rewritten at every start, and
reaches the container through `envFrom`. The pod template carries only static, non-secret
settings. It is not the ECS arrangement in either direction: ECS keeps the DEK and `AGENT_TOKEN`
behind SSM SecureString parameters with their own IAM, and the other tokens in the task
definition's plain environment. Here all of them sit in one Secret, readable by whoever can read
Secrets in the namespace — narrower than a plain environment, wider than a per-parameter IAM
grant.

`secretKeyRef` hides a value from the pod spec, not from someone who can read Secrets or create
pods in the namespace. Decision 7 is what keeps those rights away from members. Secrets in etcd
are only base64 unless the cluster encrypts them. The runbook makes application-layer secret
encryption (Cloud KMS on GKE) a precondition, and the Terraform of decision 12 turns it on; the CP
cannot see the setting, so on other clusters it rests on the runbook alone.

### 7. Isolation meets every row of 07 §7.2

**One workspace namespace per deployment.** Tenants and members are separated by the CP, as on
every other target, not by namespaces. Members never get Kubernetes API access to the workspace
namespace; anyone who can create pods there can mount any member's claim and read any Secret, so
that right belongs to the CP and the cluster's administrators alone. The operator sets a
ResourceQuota and LimitRange on the namespace, which caps what the stopped workspaces' claims and
the running pods may take together; a start refused by the quota surfaces as the boot phase.

**The workspace namespace enforces the `restricted` Pod Security Standard**, set by a namespace
label the manifests apply and the CP has no right to change. The CP can create pods there (the
StatefulSets, and the erase pod), and a CP that is compromised could otherwise create a
privileged or `hostPath` pod and reach the node and everything else on it. With the label, the
API server refuses such a pod whoever asks. What a compromised CP still reaches is the namespace:
every workspace's Secret, so every DEK and every minted token — the same reach it has on every
target, where it holds the master key. A dedicated cluster bounds it there; on a cluster shared
with other workloads, the Pod Security label and the namespaced role are what keep it there.

| Concern | `kubernetes` |
|---|---|
| Files between users | one pair of claims per workspace, referenced by that workspace's StatefulSet alone, `ReadWriteOncePod` where supported |
| Process and memory | one pod per workspace, with CPU and memory requests and limits from the workspace sizing, and an `ephemeral-storage` request and limit. That limit mitigates, it does not isolate: the kubelet measures use periodically and evicts afterwards, so a fast writer can still press the node's disk before it is caught, and other workspaces on the node can see write failures or evictions. Decision 13 adds the rest: free-space headroom on workspace nodes, container log limits, and a disk-pressure alert. `/tmp` is an `emptyDir` on the node's disk with a `sizeLimit`; it goes with the pod, which is the reason ecs-ec2 needs a tmpfs, so a tmpfs is not needed here |
| Network | the policies below |
| Privileges | what `restricted` allows and nothing more: not privileged, `runAsNonRoot`, no added capabilities, no `hostNetwork`, `hostPID` or `hostPath` — Fargate's level. The first version offers no `SYS_ADMIN` opt-in for Chromium's sandbox, since `restricted` forbids it (open question 1) |
| Cloud identity | `automountServiceAccountToken: false`; the workspace's service account is bound to no cloud identity. On GKE every node pool that can run a workspace uses the GKE metadata server (`GKE_METADATA`), and workspaces are scheduled only onto such pools, so the metadata server never hands a pod that is not on the host network the node's credentials — and `restricted` keeps every workspace pod off it. No IAM grant may name the workspace namespace or its service account as a principal, directly or through a `principalSet`; and since Workload Identity treats the same namespace and service account names in any cluster of a project as one identity, the namespace names carry a per-deployment prefix. The CP's own Workload Identity is a separate binding (decision 8) |
| Sensitive state | the state volume at `/var/lib/af/claude` and the keep path, as on ecs-ec2 |

**Network policies.** NetworkPolicy only allows; a deny is the absence of an allow, and any other
policy selecting the same pods adds to it. The workspace namespace therefore holds exactly these,
and the runbook forbids adding broader ones:

| From → to | Allowed |
|---|---|
| any → workspace agent port | from the CP's pods only |
| workspace → cluster DNS | yes |
| workspace → CP | yes, to the internal port of the CP's internal Service only (decision 8) |
| workspace → the internet | yes, as `0.0.0.0/0` with two sets of ranges excepted. The fixed special-use ranges: RFC 1918, `100.64.0.0/10`, `169.254.0.0/16` (the metadata address included). And the ranges this deployment actually uses, which the manifests take as parameters: the pod, service and node ranges and the control-plane endpoint. The fixed list alone is not enough — GKE Standard's default service range from 1.29 is `34.118.224.0/20`, and GKE can use privately used public ranges for pods and nodes |
| workspace → a private address the deployment needs (a LAN engine, an internal git host) | only as an explicit, per-destination rule the operator adds |

NetworkPolicy is enforced only by a CNI that implements it (Dataplane V2 on GKE). A cluster
without one accepts the policies and enforces none of them, so the runbook states it as a
precondition. The control-plane endpoint is private, or limited by authorised networks that exclude the pod
range; a public endpoint open to all is not supported. NetworkPolicy also always lets a pod reach
the node it runs on, so the node itself
must not offer an unauthenticated service to pods: the runbook requires the kubelet's read-only
port to be off (GKE's default) and no `hostNetwork` service on workspace nodes, and the live
harness probes the node, the control-plane endpoint, a service address and a VPC address from
inside a pod. Outbound traffic is open, as on ECS: the egress proxy, when the template
environment points sessions at it, can be bypassed by a process that ignores the proxy variables
([07 §7.8](../build/07-security.md)).

### 8. The CP runs in the cluster; workspaces reach it by an internal address

The CP is a Deployment at one replica in its own namespace, with an internal Service. Its service
account has a Role in the workspace namespace, and a ClusterRole that only reads:

| Kind | Verbs |
|---|---|
| StatefulSets, Services, PersistentVolumeClaims, Secrets | get, list, create, update, patch, delete |
| Pods | get, list, watch — State, TaskCounter and BootPhase read the pod the controller made. create, delete — only for the erase pod of decision 4; the CP never deletes a workspace pod (decision 3) |
| Events | get, list — the reason a pod cannot be scheduled or pulled, for the boot phase |
| NetworkPolicies | none: they are static, applied with the manifests |
| StorageClasses (ClusterRole) | get, limited by `resourceNames` to the configured class — the boot check of decision 4 |
| PersistentVolumes (ClusterRole) | get — Destroy's confirmation that the disk is gone (decision 5). A volume's object names its disk and claim, nothing in it |

Its store is any Postgres the deployment provides. On Google Cloud that is Cloud SQL on a private
IP, reached through the Cloud SQL Auth Proxy as a sidecar of the CP pod, with automatic IAM
database authentication: the proxy authorises the connection with the CP's Workload Identity and
logs in as the IAM database user bound to it, and the CP's DSN points at the sidecar on loopback
with no password. That identity is bound to the CP's service account alone.

**The workspace's way back to the CP.** `AF_CP_BASE_URL` is the public base URL today, which from
inside the cluster means leaving through NAT and coming back through the load balancer — a route
decision 7 closes for private addresses anyway.

The internal route must not open the CP itself to the workspace. The CP is reachable only through
the ingress today ([09 §9.3](../build/09-deploy.md)), and two things rest on that: `AUTH=proxy`
trusts the identity header and does not strip it, and `clientip.go` reads `X-Forwarded-For` by
hop count without checking who sent it. A workspace connecting to the CP's ordinary port could
name any user and any client address. So the CP serves a **second listener** for workspaces only,
and the internal Service targets that port alone. It carries only the routes the agent calls,
each authenticated by its own bearer token (the docs, memo, schedule, MCP, engine, git and
credential endpoints — the list is fixed in phase 1); the Console, the admin API and the login
routes are not served on it. It ignores identity headers and forwarding headers, taking the
connection's own address as the client. Phase 1 tests that a forged identity header and a forged
`X-Forwarded-For` sent to it are ignored, and that a Console route answers 404 there.

The variable has two uses, too: API calls, and the links in notifications, which a browser opens.
So the adapter passes the internal URL as a second variable, `AF_CP_INTERNAL_URL`, next to an
unchanged `AF_CP_BASE_URL`. The CP reads that URL from its own environment, where the manifests
of decision 12 set it from the internal Service's name and port, as `PUBLIC_BASE_URL` is set
today.

This is an exception to the invariant of [09 §9.3](../build/09-deploy.md) — the CP is reachable
only through the ingress — and the exception is exactly the second listener: nothing a browser or
an administrator uses is reachable another way. About fifteen places in the agent read `AF_CP_BASE_URL` today (the credential
helper, the docs sync, engines, MCP, chat, browser, AWS and branch rules among them). Each one
that sends a request prefers the internal URL when it is set; each one that builds a link for a
person keeps the public one. Two kinds fit neither and are part of the same work: the explicit
environment allowlists that codex applies to its stdio MCP children
(`internal/mcpreg/attach.go`, `internal/chatx/chat_providers.go`), which must forward the new
variable, and the browser's list of forbidden destinations (`internal/browserx/browser_types.go`),
which must name both URLs. Sorting them is part of phase 1. If the template environment sets the egress proxy, `NO_PROXY` gets the
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
re-reads it from its file rather than caching it at boot, and retries once with a fresh read
on a 401. The surface is the kinds of decision 8; client-go would bring a dependency tree larger than
the rest of the CP's for it, and the docker adapter already shows the house preference: the
standard library and the substrate's own interface.

The cost is that the types are ours to keep correct. Tests run the adapter against recorded API
server responses, and a live harness, gated like the ecs-ec2 one (`AF_ECS_EC2_LIVE=1`), runs it
against a real cluster: Stop then Start at once, a Stop while the controller is creating a pod
(its create request held back), a State read between Start's write and the
controller's next status update, a Stop while `starting`, a CP restart in the middle of a start,
both home wipes with a keep file replaced by a plain file, `EraseHome` with a CP restarted while the erase pod runs,
a resize while stopped, and Destroy with a CP restarted after the claims are gone, and again with a volume left behind,
across the boundary between Destroy returning and the CP recording the residue. It also covers a
node made unreachable — Stop returns an error, the runbook's recovery is followed, and the
workspace starts again — a running workspace's pod deleted behind the CP's back, as an unplanned
drain does, which must return through `starting` to `running` on the same start generation without
any CP action and without the start deadline, the reaper or a Start interfering; and a new
workspace whose every mount `dev` can write (decision 4); and a planned upgrade, where a Start
issued while the node is cordoned lands elsewhere.

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
| `MachineProfile()` | no — a pod has no machine of its own to name |
| `AcquireOperationFence`, `StartFencer` | no — nothing of a workspace's lifecycle lives on the CP's host, and Start leaves no uncommitted launch behind |
| `LaunchBudgeter` | no — Start does no work in the background after it returns |
| `BeginHibernate()`, `BackupHome()`, `HomeBackups()` | no — VolumeSnapshot is the natural mechanism, later work |
| Golden seeding, the slot pool | no |

Each row gets an assertion in `capabilities_test.go` or `runtime_test.go`, in both directions.

### 12. Infrastructure is Terraform on the Google Cloud side and plain manifests in the cluster; the Helm chart stays shelved

- `deploy/kubernetes/` holds plain manifests with a kustomize base: the namespaces, the CP's
  Deployment, Service, service account and Role, the network policies of decision 7, and an
  example ResourceQuota. Any cluster can apply them.
- `deploy/gcp/gke/` holds Terraform for what a GKE deployment needs around the cluster: the VPC,
  the cluster (at least Kubernetes 1.33, for decision 5) with Dataplane V2, secret encryption, a
  private or restricted control-plane endpoint, and a workspace node pool on the GKE metadata
  server; the StorageClass of decision 4; Cloud SQL on a private IP; Cloud NAT with a static
  address; the load balancer and Certificate Manager; and the IAM grants, each to one principal
  on one resource — the CP's identity gets Cloud SQL client and instance user, and Artifact
  Registry reader for `Stale()`; the node pool's service account gets Artifact Registry reader and
  log and metric writer. The state backend is a bucket the operator names, and the DNS zone is a
  precondition the operator brings.

Terraform because Google Cloud's own template service is closing: Deployment Manager lost support
on 2026-04-01, refuses new users from 2026-06-30 and shuts down after 2027-06-30
([Google's deprecation notice](https://docs.cloud.google.com/deployment-manager/docs/deprecations)), and its
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

### 13. Operating it: upgrades, backups, alerts and the bill

A runtime is only half of what the user asked for; the other half is running it. The runbook owns
the procedures, and this ADR fixes what they must cover:

- **Upgrading the CP.** The CP applies its migrations at start and cannot be downgraded
  ([09 §9.7](../build/09-deploy.md)). Its Deployment uses the `Recreate` strategy, so two CPs never
  run against one database during a rollout. An upgrade takes a Cloud SQL backup first; going back
  means restoring that backup with the previous image. `AF_MASTER_KEY` is kept outside the
  database and its backups, as on every target.
- **Backups.** Cloud SQL automated backups with point-in-time recovery, and a restore rehearsed
  once in phase 1. The homes are not backed up in the first version (out of scope).
- **The workspace image.** A new image reaches a workspace at its next start; `Stale()` shows which
  ones are behind.
- **Alerts.** Logs go to the cluster's logging (Cloud Logging on GKE). The CP's `/readyz` is its
  readiness probe and the database alarm; the runbook adds alerts for pods `Pending` for long,
  quota refusals, Destroy residues in the audit log and certificate expiry.
- **Nodes under live sessions.** A workspace pod carries
  `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, so the autoscaler does not evict a
  running session to shrink the pool; the pool shrinks as workspaces stop. A drain is not a Stop:
  it deletes the pod but leaves `replicas: 1`, so the StatefulSet recreates the pod on another
  node at once — the session is cut, the workspace comes back by itself, and its capacity keeps
  billing. So a planned node upgrade first cordons the node, so nothing new is placed on it, then
  stops the node's workspaces through the CP — those starting included — and waits for each
  settled stop, and only then drains; the node is uncordoned when it returns, and the members
  start their workspaces again on their next use. The CP needs no right over Nodes for this; the
  operator runs the cordon. An
  unplanned drain (an automatic upgrade outside the window, a node repair) gives the cut and the
  restart, which the runbook says, and the harness checks both.
- **Node disk.** Workspace nodes keep free-space headroom, container logs are capped and rotated,
  and disk pressure on a workspace node raises an alert (decision 7).
- **The bill.** Its shape follows [09 §9.8](../build/09-deploy.md): a floor (the cluster, the CP's
  node, Cloud SQL, Cloud NAT, the load balancer), per-workspace capacity while running, and two
  persistent disks per workspace that bill while stopped, as an EBS home does. A cluster the user
  already runs removes the cluster from the floor. Phase 1 fills in the numbers, including the
  24/7 row for a broken idle-stop, the way 09 §9.8 does for AWS.

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
- A node that stops answering leaves its workspace `starting` while it should run, and after a
  Stop `stopped` with a pod that blocks Start, `EraseHome` and Destroy — until an operator acts.
- The CP grows a second listener for workspaces (decision 8), on this profile only.
- The CP cannot read the network policies or the cluster's settings, so a policy added later that
  widens what workspaces reach, or a cluster whose CNI stops enforcing, goes unnoticed by the
  product. The harness's reachability probes are what an operator can rerun.
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
| 0 | Decision 1: the GCE runbook and script, the egress default, the guide pages | a session runs on a GCE VM behind Caddy, and behind a global external Application Load Balancer, where an idle terminal outlives the default `timeoutSec` and a cut connection reconnects; a request from a workspace to the metadata server fails; the documents of [21 §21.5](../build/21-add-a-deploy-target.md) list the runbook |
| 1 | Decisions 2–13: the adapter, the CP's internal listener, the agent's URL split, `deploy/kubernetes/`, `deploy/gcp/gke/` | on GKE Standard the live harness of decision 10 passes; the isolation rows and network policies of decision 7 are checked from inside a pod, including the node, the control-plane endpoint and a VPC address; the runbook covers decision 13, with a rehearsed restore and the cost table; the finishing list of [21 §21.8](../build/21-add-a-deploy-target.md) is done, and [09 §9.3](../build/09-deploy.md) states the second listener's exception |
| 2 | Autopilot, and whatever of "Out of scope" is asked for | each its own issue |

## What would make us revisit it

- A user who needs Helm specifically (decision 12).
- A cluster where the standard API is not enough for the home — for example, no StorageClass with
  expansion and `WaitForFirstConsumer`.
- Measurements showing that start latency on Kubernetes is far from ECS's, which would put a pool
  of pre-warmed nodes back on the table.
- Tenants that must be separated by the cluster rather than by the CP, which would bring a
  namespace per tenant back.

## Note — the deploy trees (2026-10-02)

Issue #1467. Nothing above is changed; this records how `deploy/kubernetes/` and
`deploy/gcp/gke/` carry decisions 7, 8, 12 and 13 where the decisions leave a choice.

1. **The load balancer is split along the cluster's edge.** Terraform holds what lives outside
   the cluster — the global address, the Certificate Manager certificate (DNS-authorised, so the
   preview wildcard is covered) and its map, the DNS records. The load balancer itself is built by
   GKE's Gateway controller from `components/gke` (class `gke-l7-global-external-managed`, the
   non-classic one decision 1 requires), with a `GCPBackendPolicy` raising `timeoutSec` to 3600 as
   the starting point for open question 3, and a `HealthCheckPolicy` on `/healthz`. A Terraform
   backend service would need the NEGs the cluster creates, so it could not be built in one
   apply.
2. **Ports.** The CP's main port is 8099, the workspace-only listener `AF_CP_INTERNAL_LISTEN=:8098`,
   and the agent port 7700 (as on ECS). The workspace policies admit 7700 from the CP's pods and
   allow 8098 to them.
3. **The CP namespace has an ingress policy on GKE** that admits Google's front-end ranges to 8099
   and workspace pods to 8098. Decision 7's table governs the workspace namespace and is
   unchanged; this one keeps other workloads on a shared cluster off the port that trusts
   forwarding headers ([09 §9.3](../build/09-deploy.md)).
4. **IAM.** Cloud SQL client and instance user exist only at project level, so their grants carry an
   IAM condition naming the instance. The node service account gets the minimum node role
   (`roles/container.defaultNodeServiceAccount`) in place of log and metric writer: from 1.33 a
   node also needs `autoscaling.sites.writeMetrics`, which those two lack. It has no narrower
   resource than the project.
5. **The StorageClass is Terraform's** (through the Kubernetes provider), as decision 12 lists it;
   so `terraform apply` must reach the control-plane endpoint.
6. **The CP has a disk of its own.** `WS_DATA` holds the internal git provider's repositories, LFS
   objects and git token key, so it is a PersistentVolumeClaim (`af-cp-data`), backed up with the
   database, not scratch.

## Amendment (2026-10-02) — the workspace listener's route list (#1464)

Decision 8's list is fixed as `workspaceRoutes` in `control-plane/workspace_listener.go`: the
docs, branch-rules, MCP-registry and AWS-profiles pulls, the memo and schedule routes the agent
calls (not `/internal/memo-categories`, which no agent code calls), the two git-OAuth refreshes,
the engine token, catalogue, props and gateway, and internal git with LFS. The listener
dispatches through the main listener's mux and refuses any pattern not on the list, so the
handlers are registered once. `AF_CP_INTERNAL_URL` is injected only alongside `AF_CP_BASE_URL`,
because the bridge tokens come with that. Internal git keeps its public clone URL (people clone
it from outside the cluster): the agent rewrites the workspace's git onto the internal URL with
`url.<internal>/git/.insteadOf` and stores the git token under both hosts, and an LFS batch
answered on the workspace listener returns transfer URLs on it. The
`NO_PROXY` entry for the internal Service is left to the adapter.

## Addendum (2026-10-02) — what the core adapter settled where the decisions left a choice

Written with the core adapter (#1465, `control-plane/internal/runtime/runtime_kubernetes*.go`).
The decisions do not change; these are the choices they left open, and each can be revisited in
the live acceptance (#1468).

- **Claim access mode is `ReadWriteOnce` for now.** Decision 4 prefers `ReadWriteOncePod` where
  the CSI driver supports it, and that is to be confirmed on GKE. A claim with an access mode the
  driver lacks stays `Pending`, which would make every start `starting` until the deadline, so the
  safer mode ships first.
- **The template environment reaches the pod** (decision 8's open question): `Config.ExtraEnv` —
  `WS_ENV` and the egress proxy variables — goes into the workspace's Secret with the per-start
  environment, since an operator's `WS_ENV` may hold a credential. The template's own `env` keeps
  `CLAUDE_CONFIG_DIR`, `AF_WS_KEEP` and `AGENT_STOP_GRACE_SEC`, and `env` wins over `envFrom`.
  When that environment sets the egress proxy and `AF_CP_INTERNAL_URL` is set, the URL's host is
  appended to `NO_PROXY` and `no_proxy`, so calls to the CP's internal listener bypass the proxy.
- **Start on a StatefulSet already at replicas 1 does nothing and succeeds**, as every adapter does
  for `running` and `starting`; the Secret is not rewritten under a pod that may read it. The
  settled-stop requirement of decision 3 applies to a StatefulSet at replicas 0.
- **Digest pinning reads the registry as the CP**: the pull secret's entry for the registry host,
  else the pod's Workload Identity token for Artifact Registry and Container Registry hosts only,
  else anonymously. A tag that cannot be resolved fails the Start; a reference that already
  carries a digest is used as written, which is the operator's way around a registry the CP
  cannot read.
- **Object names**: a CP workspace name longer than 40 characters keeps a prefix and gains a hash
  of the whole, so the pod name and the `controller-revision-hash` label stay within 63.

## Addendum (2026-10-02) — what the home operations and Destroy settled

Written with #1466 (`runtime_kubernetes_home.go`, `runtime_kubernetes_destroy.go`,
`runtime_kubernetes_stale.go`). The decisions do not change, with one exception, flagged first.

- **The wipe record lives on the state claim, not in the home** (decision 4 says the init
  container "writes the generation it carried out into the home"). A record in the home is a
  file the member can delete or edit, and a pod restarted after that — a drain, a node repair —
  would remove their work again. The record is a `wipe` subPath of the state claim that only the
  init container and the erase pod mount; the workspace container cannot reach it. The behaviour
  decision 4 asks for is unchanged: a restarted pod finds the generation and removes nothing.
- **The marks are one annotation per kind** (`agent-fleet.io/home-wipe-repos`, `…-clean`) holding
  the generation of the latest request of that kind, plus a counter. A Recreate requested after a
  Clean home therefore cannot overwrite the pending Clean home. The init container checks every
  number before it removes anything, and stops the pod on a record it cannot read.
- **A finished erase pod found by `EraseHome` is deleted and the erase is run again**, since its
  result belongs to a call that has already returned. A running one is waited for. The erase pod
  runs the image of the workspace's last start, or the configured one pinned at the time.
- **Destroy's inventory** records each claim's UID and volume, and whether the volume carried a
  deletion-protection finalizer (`external-provisioner.volume.kubernetes.io/finalizer`, or the
  in-tree `kubernetes.io/pv-controller`). A volume without one is reported as a residue even once
  its object is gone. An inventory that cannot be parsed is neither trusted nor overwritten, and
  the StatefulSet stays. So does a claim that is missing and not in the inventory.
- **The StorageClass boot check warns and does not refuse to start.** A class the CP cannot read
  is often RBAC applied after the CP, and each requirement it misses already shows where it bites:
  a claim that stays `Pending`, a refused resize, a Destroy residue.
- **`ResizeHome` reports `same` only when the claim's capacity has reached its request** and no
  `Resizing` / `FileSystemResizePending` condition is set; until then it reports `growing`. Its
  write is conditional on the request it read, since the API server lets a request go down while
  it stays above the capacity, and two saves may race.
- **An erase pod is finished only when every container reports terminated**, not when its phase
  says so: an eviction writes phase `Failed` before the kubelet kills the container. Start goes on
  past a finished one only once the pod object is gone, which is the kubelet's confirmation —
  `ReadWriteOnce` keeps a claim on one node, not to one pod.
- **Destroy deletes each claim on the condition that it is the version it recorded**, so a claim
  bound after the read is recorded before it goes. A claim recorded without a volume counts as
  unknown, whatever its annotations, and keeps the StatefulSet: the PV controller saves a volume's
  claim reference before the claim's volume name, a pre-bound or static volume needs no
  provisioner, and the namespaced role cannot list volumes to look. The inventory is keyed by claim
  UID, so a claim recreated under the same name never erases what was recorded about the first;
  an unknown resolves only when the same UID is read again with its volume. A workspace whose pod
  never got a volume therefore leaves its StatefulSet as a residue for the runbook.

## Addendum (2026-10-03) — the home is a directory of its claim (#1543)

Decision 4 mounts the home claim at `/home/dev`, and it still is; what is mounted there is now
the claim's directory `.af-home`, through `subPath`, rather than its root. With `fsGroup` the
kubelet leaves a claim's root `root:dev`, mode `2775` (measured on GKE), and only its owner or
root can change that, which nothing under `restricted` is. The group-writable home made
`cloudexec.PrivateDir` (ADR 0107) refuse the state of `af-gcloud-exec` and `af-aws-exec` alike.

- **An init container from the same image, running as `dev`, makes the directory** before any
  container mounts it (`homeLayoutScript`, `runtime_kubernetes_home.go`): a `subPath` the kubelet
  has to create itself is root's again.
- **A claim of the earlier layout keeps its files**: the same step moves every entry of the root
  into `.af-home.new` and renames that into place last, so an interrupted start carries on and the
  home never shows half of its files. `lost+found` stays on the root. An entry it cannot move, or
  that would land on one already there, stops the pod rather than leave the home without it.
- **Which layout a claim has is recorded on the state claim** (`layout` beside the wipe record:
  none, moving, done), not read from the root: in the earlier layout every name there was the
  member's to create. A `.af-home` or `.af-home.new` the member made is refused before anything
  moves. A claim recorded as migrated is refused when its root holds anything besides the home or
  the home is gone, and so is one whose record is missing or unreadable — the erase pod of a home
  without a state claim included. Refusing stops the pod with the reason in its log.
- **Group write is removed at every start** from the home and the directories above the Agent's
  state (`~/.local`, `~/.local/state`, `~/.local/state/agent-fleet`), which `PrivateDir` walks,
  since a recursive `fsGroup` change sets it, stopping at the first link on the way; the member's
  other files are left alone.
- **Rolling the CP back past this is not safe** for a workspace that has started since: an earlier
  CP mounts the root as the home, hiding the migrated files; its Clean home and administrator's
  Clean home remove `.af-home` whole, and its Recreate removes the root's `repos`, not the hidden one.
  The runbook's "Rolling back past the home layout" moves a home back first; this version refuses
  a claim an earlier one has used again (the residue check above).
- **The wipe init container mounts the home the same way and runs after the layout; the erase pod
  runs the layout and the erase in its one container**, on the claim's root, because a failed init
  container leaves the main one waiting and an erase pod is finished only when every container
  has terminated.
- The state claim's `subPath` directories stay as the kubelet makes them. Nothing private lives
  under them: the cloud wrappers' state is under `~/.local/state`, which is not a keep entry.

## Addendum (2026-10-03) — workspace egress never covers a node address (#1578)

Decision 7 allows workspace egress as `0.0.0.0/0` with the special-use ranges and the
deployment's own ranges excepted, and relies on that to keep every node but a pod's own out of
reach. On GKE Dataplane V2 the exception does not hold for node addresses. Measured on the
acceptance cluster (GKE 1.35.8, `anetd` = Cilium 1.18.7):

- **A workspace pod reached every node.** TCP to `:22`, `:10250` and `:10256` of its own node and
  of the two others connected; an unused address in the node range did not. Nothing
  unauthenticated answered (the kubelet returns 401, sshd wants a key), but the reach was there.
- **GKE adds a node-identity selector to every ipBlock whose cidr contains a node address, and
  `except` cannot take it away.** `cilium policy selectors` showed three selectors for the
  `0.0.0.0/0` block: `cidr:0.0.0.0/0`, `reserved:world` and a `remote-node` one, each with the
  excepted ranges as `cidr:<range>` DoesNotExist. The `remote-node` selector matches identities
  1 (host), 6 (remote-node) and 7 (kube-apiserver), which carry no `cidr:` labels, so no except
  removes them; the workspace endpoint's policy map allowed egress to host and remote-node on
  every port. Upstream Cilium 1.18 adds only `reserved:world` for a `/0` prefix
  (`pkg/policy/api/cidr.go`); the node selector is GKE's, and GKE's documentation does not
  mention it.
- **It follows containment, not `/0`.** Splitting the block into `0.0.0.0/1` and `128.0.0.0/1`
  left every node open: the `remote-node` selector moved to `0.0.0.0/1`, the half holding the node
  range, while `128.0.0.0/1` and the CP namespace's `35.191.0.0/16` and `130.211.0.0/22` got
  none.
- **Blocks that contain no node address close it.** The same allow written as the 47 blocks
  that make up the complement of RFC 1918, `100.64.0.0/10` and `169.254.0.0/16`, with no
  `except`, produced 47 `cidr:` selectors and no node or world selector. From the workspace pod
  every node's three ports then timed out, own node included, and nothing else changed: DNS,
  public addresses on both sides of `128.0.0.0`, and the CP's internal port stayed open; the
  metadata address, the API Service, the control-plane endpoint, Cloud SQL and the CP's main
  port stayed closed.

Decided (the user's choice among three):

- **The egress policy is that fixed complement**, written by `deploy/kubernetes/egress-blocks.py`,
  which also fails when the file drifts from it. The manifests no longer take the deployment's
  ranges as parameters: an `except` has to lie inside its block, so which block a range belongs
  to would depend on its value.
- **The deployment's node, pod, service and control-plane ranges must lie inside RFC 1918 or
  `100.64.0.0/10`.** Terraform fails a plan otherwise; the runbook makes it precondition P15 for
  other clusters, which must cut any other range (privately used public addresses, GKE's default
  `34.118.224.0/20` Service range) out of the blocks themselves.
- Rejected: Terraform computing a per-deployment block list (keeps the parameters, but leaves the
  generic overlay to hand computation), and a VPC firewall rule from the pod range to the node
  range (never sees a pod's traffic to its own node, and with one pod range for all pools it would
  also cut metrics-server off from the workspace nodes' kubelets).

With this policy the sentence "NetworkPolicy also always lets a pod reach the node it runs on" no
longer describes Dataplane V2, where a workspace reaches no node at all; P6 stays, for CNIs where
it does. The runbook's "Check it" probes every node from a workspace pod.

Reported upstream (2026-10-03): <https://issuetracker.google.com/issues/569041167>.

## Note (2026-10-03) — the WebSocket behind the load balancer, measured (#1468)

On open question 3, from the live acceptance run on GKE Standard behind the global external
Application Load Balancer, with the backend's `timeoutSec` at 3600 (the `GCPBackendPolicy` of the
2026-10-02 note on the deploy trees):

- **The Console's pings kept an untouched terminal from looking idle.** A terminal nobody typed
  in stayed connected for more than 67 minutes past the 3600-second `timeoutSec`. The Console
  pings on the same socket on timers, a round-trip ping every 5 seconds and a heartbeat every 15
  (`console/src/terminal/term.ts`), and during this run that traffic kept the connection busy.
- **Not measured:** a hidden tab, a frozen page or a sleeping machine, where the browser may slow
  or stop those timers; the 24-hour cut of an active WebSocket; and whether the Console reconnects
  the terminal transparently after a cut. Those parts of open question 3 stay open.

The runbook's "The load balancer" says the same.

## Addendum (2026-10-04) — no browser features on this runtime (open question 1, #1606)

On open question 1, measured on the GKE acceptance cluster of #1468 (2026-10-03, GKE 1.35.8, the
workspace image built from `develop`, the workspace namespace at Pod Security `restricted`):

- **A sandboxed Chromium does not start in a workspace pod.** `workspace-agent browser-smoke`, the
  production pipe-CDP launcher, fails with `Chromium stopped during Target.setDiscoverTargets:
  EOF`; `chromium --headless=new --dump-dom` with the sandbox prints `The setuid sandbox is not
  running as root` and `Zygote process exited prematurely`. The same command with `--no-sandbox`
  dumps the DOM.
- **Why:** in the pod `NoNewPrivs: 1` (the setuid `chrome-sandbox` cannot elevate), `CapEff: 0`,
  `Seccomp: 2` (RuntimeDefault), and `unshare -U` is refused with `Operation not permitted`, so
  neither the setuid sandbox nor the user-namespace sandbox is available. Under `docker` the
  same `unshare -U` succeeds and the sandboxed launch works.

**Decision: the `kubernetes` runtime offers no browser features** — no browser pane, no Chromium
attachments, no headless Chromium for agents — and says so instead of failing silently. Rejected:
a `Localhost` seccomp profile or cluster settings that allow user namespaces (they widen the
attack surface of a node kernel shared by several members' workspaces), and `--no-sandbox` on this
runtime (an unsandboxed renderer gives untrusted web content code execution as the member's dev
user). GKE Sandbox (gVisor) remains a possible later path, as its own issue.

How it is carried out:

- The adapter decides, in one place (`runtime.BrowserUnavailable`, implemented only by the
  kubernetes runtime). The CP puts the runtime id on the workspace payload as
  `browserUnavailable`, in every state, and on the pod template as `AF_BROWSER_UNAVAILABLE`; it
  also refuses `/api/browser/*` and the browser WebSockets itself with `409 browser_unavailable`.
- The Agent obeys the variable rather than probing: every browser route answers the same
  refusal, the launcher refuses before Chromium starts (`AF_CHROMIUM_NO_SANDBOX` cannot override
  it), the af MCP browser tools return the explanation with what to do instead, and
  `browser-smoke` fails with the reason. A probe was not chosen because the outcome is a policy,
  not a measurement to repeat, and on `native` a userns probe can fail where an AppArmor-profiled
  Chromium still runs.
- The Console greys out "open in pane" with the reason, opens a session row's port in the
  lightweight preview, and replaces a browser or attachment pane restored from a layout with the
  reason. The lightweight preview is unaffected.
- The variable takes effect at a workspace's next start, since the pod template is written then.
  Every other runtime is unchanged.

The guide's `ref/browser-pane.md` ("Where there is no browser pane"), `ref/deploy-targets.md`,
the agent-facing `workspace/notes/browser.md` and `deploy/kubernetes/README.md` say the same.

## Note (2026-10-04) — the af MCP server no longer lists the browser tools here (#1614)

The seven browser tools (`list_chromium_targets` … `set_chromium_control_mode`) were still in the
af MCP server's `tools/list` and only refused when called. The Agent now passes
`--browser-unavailable <runtime>` to every `mcp-stdio` it configures (the session-side af server
for every kind, and the assistant's), and the server leaves those tools out of `tools/list`; a call
that names one anyway still answers `browser_unavailable`. The argv carries it rather than the
variable because the variable does not reach every agent's MCP children: codex starts them
default-deny and muse, cursor, kiro and copilot are handed an explicit environment.

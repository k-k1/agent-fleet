# 0122. Workspace egress is recorded and decided at the network: an egress gateway on the route out, instead of proxy variables a process can ignore

English | [日本語](0122-egress-gateway.ja.md)

- Status: **proposed** (2026-10-23). Documentation only: nothing below is built. The code and
  template facts were read from the tree on this date. Squid's behaviour was read from its
  published documentation and an AWS blog post, **not run**; no Docker and no cloud account were
  available while writing this.
- Follow-ups: #1181, #2603, #2604, #2657, #2658, #2674, #2687, #2699
- Related: [0047](0047-tenant-network-restriction.md) (inbound IP restriction; this record does
  not touch it) / #1056 (credential injection, needs this fence first) / #1067 (why an OS sandbox
  is not the answer)

## Context

The egress forward proxy (`control-plane egress-proxy`, docs/log/20) records and can block, but
only what reaches it. Reaching it depends on `http_proxy` / `https_proxy` in the workspace
environment, so a process that ignores them leaves unseen. The requirement behind this record:
**every workspace connection to the outside must be recorded; unrecorded passthrough is not
acceptable for a deploying organisation.** Today:

- No template runs the proxy (`deploy/compose`, `deploy/aws`, `deploy/kubernetes`, `deploy/gcp`).
- Nothing forces traffic through it: docker's per-user network is not internal, the ECS
  workspace security group has no egress rule, and the Kubernetes `egress-internet` policy admits
  every address outside RFC 1918.
- The variables do not reach every target. `main.go` puts them in `mgr.extraEnv`; docker, native
  and Kubernetes read it (`runtime.go`, `runtime_native.go`, `runtime_kubernetes.go`), but
  `ecsFactory.New` (`runtime_ecs.go`) uses only the per-start env, so ECS and ecs-ec2 workspaces
  get no proxy variables at all.
- Observations can be lost. `egressBatcher.flush` empties the batch before the POST and drops it
  on a transport error or any non-2xx answer (`egress_proxy.go`).
- Observations carry a host and a count, not which workspace made the connection.

Constraints found while looking at the targets:

- The AWS stack owns its VPC and a NAT gateway with a retained Elastic IP, so a deploying organisation can
  allow-list a stable egress address (`00-network.yaml`). ECS tasks use `awsvpc`: each has its
  own ENI and private IP.
- The GKE stack owns Cloud NAT with a reserved address (`network.tf`).
- On compose the CP runs on the host network and reaches each workspace through a published
  `127.0.0.1` port. A docker `--internal` network breaks published ports, and a bridge with
  masquerade turned off is not a fence: it stops NAT, not routing, and leaves the host, the
  metadata address and docker's embedded DNS reachable.
- Cloud NAT and the AWS NAT gateway forward and count; neither decides by host name.

## Decision

### 1. The network is the fence, always on, in log-only mode too

Workspace traffic leaves through one egress gateway, and nothing else routes out. A process that
ignores every proxy variable therefore still passes the gateway, or does not pass at all. The
workspace needs no configuration for that, and the `http_proxy` variables stop being load-bearing.

The variables stay, though: the gateway also serves them as an explicit proxy (decision 2). A
proxy-aware client names the host itself in `CONNECT`, which closes the gap Encrypted Client
Hello leaves in SNI reading, and an explicit proxy is where #1056 (credential injection) will
live.

### 2. The gateway is off-the-shelf software, not our own proxy

The first choice is **Squid** in intercept mode: port 443 redirected to an `https_port … intercept
ssl-bump` listener, `ssl_bump peek` at step 1 to read the TLS Client Hello's SNI, then `splice`
(tunnel, no decryption) for an allowed name or `terminate` for a denied one; port 80 through an
intercept `http_port` that sees the `Host` header. TLS is never decrypted, so certificate-pinning
tools and `SSL_CERT_FILE` are unaffected.

The same Squid also listens as an explicit forward proxy (`http_port`, the address
`AF_EGRESS_PROXY_ADDR` names), so proxy-aware and proxy-ignoring traffic share one allowlist, one
decision and one log. Whether one Squid runs both listener kinds as intended is **not tested**.

`nftables` on the same host covers what Squid does not see: other TCP and UDP are logged and
dropped by default, QUIC (UDP 443) is dropped so clients fall back to TCP, and the log records
the attempt.

### 3. What log-only and enforce mean now

- **log-only:** Squid splices every name and logs it with the verdict it *would* have had. The
  fence still blocks what it cannot record (non-HTTP, direct connections to an address with no
  name are logged as such).
- **enforce:** a name outside the allowlist is terminated.

The documents currently say log-only "never blocks"; they change (see Consequences).

### 4. The control plane keeps the policy and the record; a small agent on the gateway connects them

The allowlist, the proposal and approval flow and the Admin Egress tab stay as they are. A small
agent on the gateway (a) turns the effective policy into Squid's allow file and reloads it, and
(b) ships the log to the existing ingest endpoint **from a file offset**, so a failed POST is
retried instead of dropped. The events gain the source address, so the CP can attribute them
(see Open points). `control-plane egress-proxy` is retired once every target uses the gateway.

### 5. Per target

| Target | Gateway | Fence |
|---|---|---|
| ecs, ecs-ec2 | an EC2 instance in an Auto Scaling group of one, replacing the NAT gateway as the private route table's default route; keeps the retained Elastic IP | the workspace security group admits only the gateway, the CP and the AWS endpoints |
| GKE | **open** (see below) | **open** |
| compose (docker) | **open** (see below) | **open** |

## Rejected

- **Proxy variables as the only route** (today): bypassable, so unrecorded.
- **A docker internal network with the CP reaching the agent by container IP**, or **masquerade
  off**, or **host iptables alone**: the first is a large change to how the CP connects and still
  needs a gateway; the second is not a fence; the third needs root on the host and does not cover
  INPUT, DNS or IPv6. A gateway the network routes through replaces all three.
- **The cloud NAT gateway alone** (AWS, GCP): counts and forwards, cannot decide or log by host.
- **AWS Network Firewall**: a managed alternative that decides by SNI and logs. Not chosen as the
  base because it is AWS-only, charges per endpoint-hour and per GB, and gives GKE and compose
  nothing. It remains a swap-in for the AWS gateway.
- **A VyOS-style router appliance**: one design for every target, but name-based rules reduce to
  DNS-resolved addresses, and shipping and running a router is a larger burden than one Squid.
- **Our own transparent proxy** (reading the Client Hello ourselves): the work Squid already does.
- **Running `control-plane egress-proxy` and Squid side by side for good**: two decision paths
  that can disagree on one allowlist. The own proxy stays only until a target moves to the
  gateway (docker and GKE, whose gateways are open points, keep it meanwhile).
- **A per-agent OS sandbox** (#1067): not available inside the Workspace container.

## Consequences

- **The gateway is on the path of every connection.** One instance is a single point of failure:
  the Auto Scaling group replaces it, and the route and the Elastic IP must follow the
  replacement. Throughput is bounded by the instance type. This is operations we did not have
  with a managed NAT gateway.
- **Egress through a workspace that has no recorded identity is attributable only by address.**
  See Open points.
- **A name is what the client says it is.** The SNI is the client's claim; a hostile process can
  claim an allowed name while connecting to another address (domain fronting). The record shows the
  claimed name and the destination address. Squid's host verification may narrow this; not
  evaluated here.
- **No SNI, no name.** Encrypted Client Hello and a connection to a bare address carry no usable
  name: they are recorded by address and, in enforce, not allowed.
- **Non-HTTP no longer works**: git over SSH and direct DNS to a resolver of the workspace's
  choosing. Either is a follow-up.
- **The documents that change when this is built:** `guide/operate/04-secure.md` ("Operating
  egress control": log-only no longer "blocks nothing", and the "not built yet" note),
  `docs/build/07-security.md` §7.8, `docs/build/09-deploy.md` (it says only docker and native
  receive the variables, and Kubernetes also does), `docs/build/03-control-plane.md`, and the
  Japanese twins.
- **SSM login must be checked against a real account** before the gateway ships: the pinned
  session-manager plugin honours `HTTPS_PROXY` (read, not run), and with the gateway it needs no
  variable at all, but the SSO start-URL host (usually under `awsapps.com`) is not in
  `defaultEgressAllowlist`.

## Open points

1. **GKE.** Pod traffic leaves from the nodes. Routing a workspace node pool through a
   forwarding VM needs a route scoped by network tag, and the control plane's own pods must not
   be caught by it. GKE's own FQDN-aware network policy and Cloud NGFW are the alternatives; not
   evaluated.
2. **compose.** A gateway container or VM that the per-user networks route through, without
   changing how the CP reaches the agent. Needs a host to try it on.
3. **Attribution.** Squid logs the source address. For the CP to name the workspace it must
   record each workspace's address at start; for ecs-ec2 the task ENI address is available, for
   the others it is not designed.
4. **Non-HTTP**: whether SSH git is allowed through to named hosts, and how direct DNS is
   recorded (Route 53 Resolver query logging on AWS).
5. **Pause and resume.** `deploy/aws/ecs/pause.sh` deletes the NAT gateway to save its hourly
   charge (#2386). The gateway instance needs the same treatment, and a resume must restore the
   route and the Elastic IP before any workspace starts.
6. **Existing deployments**: moving the default route from the NAT gateway to the gateway is a
   change to a running VPC, with a rollback.
7. **The egress address a deploying organisation allow-lists.** Today it is the one retained Elastic IP on the NAT
   gateway (`NatEip`, `DeletionPolicy: Retain`), and `NatEipAllocationId` is meant for a new
   stand-up only: handing it to a running stack makes CloudFormation replace the NAT gateway
   (measured on a deployed stack with `--no-execute-changeset`). The gateway instance has to take
   over that same address, which cannot be attached to the instance while the NAT gateway still
   holds it, so the cut-over has an egress gap unless it is planned (a second address for the
   switch, or a short maintenance window).
8. **One gateway is one Availability Zone.** The NAT gateway is already single-AZ; a gateway
   instance in an Auto Scaling group of one keeps that. A gateway per zone gives the deploying organisation two
   egress addresses to allow-list, so the choice has to be made before the address is handed out.

## Notes

- Squid `ssl_bump`: https://www.squid-cache.org/Doc/config/ssl_bump/ (peek, splice, terminate;
  SNI read without decryption; intercept listener).
- AWS Security Blog, "How to add DNS filtering to your NAT instance with Squid".
- The `peek`/`splice` allowlist form (the published example is a denylist) and the QUIC and
  Encrypted Client Hello behaviour are **not measured**.

## Addendum (2026-10-24) — open points 1 and 3: the GKE gateway and attribution

The decisions above stand. This addendum covers open points 3 (attribution) and 1 (GKE), and
records the maintainer's answer to open point 4.
It also records one bypass on AWS that the gateway alone does not close. Sources: Google's
published documentation, read on this date and listed at the end, and the tree at this date.
**Nothing here was run**: no cluster, no account, no Docker. #2658 is the run that checks the
GKE part.

### Attribution: events carry a raw address; the CP keeps a time-ranged address table

**What the gateway sees.** On ECS and ecs-ec2, each task has its own ENI (`awsvpc`), and the
gateway in the same VPC sees that address unchanged. On GKE, it does not by default. Google's
IP masquerade agent page says that for destinations outside the non-masquerade list (RFC 1918,
100.64.0.0/10 and the reserved test and documentation blocks), "GKE changes source Pod IP
addresses to source node IP addresses". Every workspace on one node would reach a gateway as
the node's address.

**Turning that off for this route only is not possible.** Masquerade depends only on the
destination. The `ip-masq-agent` ConfigMap (`nonMasqueradeCIDRs`) applies to the whole cluster:
the page names no node-pool scope, and its DaemonSet has no node selector. It is not needed
either. `nonMasqueradeCIDRs: [0.0.0.0/0]` keeps pod source addresses for every destination, and
the stack's Cloud NAT already translates every range, pod ranges included
(`source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"`, `network.tf`). The
system pool's pods therefore still leave through the same reserved address, and the gateway sees
workspace pod addresses. `--disable-default-snat` does the same thing but only on a cluster with
no `ip-masq-agent` DaemonSet, and only after the nodes are replaced. The ConfigMap is the lever.

**The fence must not depend on SNAT.** The route that takes workspace traffic to the gateway is
scoped to the workspace node pool's network tag, not to the pod source range. If the ConfigMap
is ever lost, traffic is then attributed to the node, but it is still recorded. A route matched
on the pod range would let the node-addressed packets take the default route out, unrecorded.

**The record.** Squid's event gains the source address and the time. The CP stores them as they
arrive and attributes them through a table it keeps:

| Column | Meaning |
|---|---|
| workspace (membership) | whose address it was |
| address | IPv4 as the gateway sees it |
| from / until | the interval the address belonged to that workspace; `until` empty while it does |
| ref | what was asked: task ARN, pod UID, container ID |

Attribution is a join on address and time, never a lookup of the address alone, because
addresses are reused: a stopped task's ENI address or a deleted pod's IP can be handed out again
at once. The join's result is stored with the event, but the raw address is never discarded. An
event that matches no row is shown as **unattributed**, and one that matches two rows is shown as
**ambiguous**, with both candidates. Neither is dropped, and neither is guessed. Addresses the
stack owns (nodes, slot instances, the CP) are a separate class, "infrastructure", not
"unattributed".

**Where each runtime fills the table** (`control-plane/internal/runtime/`):

- **ECS and ecs-ec2.** Today neither describes a task. `State()` reads only the service
  (`describeService`), and `Start` returns once the service is at desired count 1, before a task
  runs. A record taken at `Start` is therefore not enough: the ECS service scheduler replaces a
  failed task on its own, with a new ENI and a new address, and the CP never calls `Start` for it.
  The table is filled from `DescribeTasks` on the service's tasks: the ENI attachment's
  `privateIPv4Address`, from `createdAt` to `stoppedAt`. The task's ENI is created after
  `createdAt` and is gone by `STOPPED` (the task lifecycle as documented; not measured), so two
  tasks' intervals for one address cannot overlap.
  It runs as a reconcile loop, plus an immediate lookup when an event arrives with an address the
  table does not know. Stopped tasks stay describable only for a while, so the loop runs well
  inside that window. For ecs-ec2, the slot instance's own address is infrastructure. Platform
  traffic also leaves from the task ENI (image pulls from ECR, logs, SSM secrets; the stack has
  only an S3 gateway endpoint, `00-network.yaml`). The gateway therefore has to allow those
  hosts, and they appear in the workspace's record.
- **Kubernetes and GKE.** `workspacePods` already lists the StatefulSet's pods by label. The
  table needs `metadata.uid`, `status.podIP` and `status.startTime`, which `kPodStatus` does not
  decode yet. `from` is the pod's start, and `until` is when the CP sees the pod gone. A
  StatefulSet pod keeps its name across a restart but gets a new UID and address, so the row is
  keyed by UID. Polling sees a deletion late, so a watch on the workspace pods is preferred. An
  overlap that slips through still shows as ambiguous, not as the wrong workspace.
- **docker.** The per-user network (`ensureNetwork`, created at `Start`, removed at `Stop`)
  belongs to one user. The row records the network's subnet and the container's address on it,
  from `Start` (after `docker run`, through `docker network inspect` / `docker inspect`) until
  `Stop`. The subnet attributes even when the container's address changes. docker's gateway is
  still open point 2, so this applies only once a gateway sees addresses on that network.
- **native**: no gateway in this record. It is not part of the table.

The gateway agent stays simple. It ships the address and the time, and the CP does the joining,
because only the CP knows the workspaces.

### GKE gateway: the same Squid on a forwarding VM, routed by the workspace pool's tag

Four candidates were compared against the requirement: every connection recorded, by name where
there is one, in log-only too.

| Candidate | Decides by | Records | Against the requirement |
|---|---|---|---|
| **FQDN network policy** + **NetworkPolicy logging** | DNS answers the pod received; raw IPs never match | 5-tuples, allowed and/or denied; no host name | Fails. Log-only would have to allow everything, which leaves only IP records. Allow-union means `egress-internet` (all non-private addresses) already overrides it. |
| **Cloud NGFW**, FQDN objects + firewall rules logging | Addresses resolved on a schedule; no wildcards, at most 32 addresses per name, results vary by region (Google names `googleapis.com` as a case that hits these) | 5-tuples | Fails for the same reason: names reduce to addresses. NGFW Enterprise (URL filtering, IPS) was not evaluated: it needs per-zone firewall endpoints. |
| **Secure Web Proxy, next-hop mode** | SNI `host()` in a `sessionMatcher`, without TLS inspection | Cloud Logging transaction log with the source IP | Meets it for HTTP(S) and TCP. It drops other traffic "without notification", so drops are not recorded. One per VPC per region. Which egress address it uses was not found. |
| **Squid on a forwarding VM** (decision 2) | SNI peek and splice, `Host` on port 80 | Squid's log, plus nftables for the rest | Meets it, with the same software, agent and log format as AWS. |

**Chosen as the GKE row of decision 5: Squid on a forwarding VM**, the same gateway as AWS.

- The workspace node pool gets a network tag. A static route `0.0.0.0/0` for that tag, with a
  priority below the default route's 1000, has the gateway VM as its next hop. The VM needs IP
  forwarding, and it carries no tag itself.
- Subnet and peering routes win over a custom static route, so the private control-plane
  endpoint and the node subnet are unaffected. The metadata server is not routed. The system
  pool, which runs the CP, is untagged and keeps the default route.
- The gateway VM, being untagged, leaves through the default route, so Cloud NAT gives it the
  reserved address. The egress address the deploying organisation allow-lists does not change.
- The workspace nodes' own traffic also takes the route: image pulls through Private Google
  Access, and the logging and monitoring agents. A more specific route for Google API ranges
  would also let workspaces reach any Google API unrecorded, so it is not added. That traffic is
  recorded as infrastructure, and the gateway allows the Google hosts the nodes need. Which hosts
  those are is measured in #2658.
- The explicit-proxy listener is reached at the gateway VM's private address. The
  `egress-internet` NetworkPolicy has to admit that one `/32` and port. A `/32` that is not a
  node does not meet the remote-node selector issue of ADR 0106.
- This adds `ip-masq-agent` with `nonMasqueradeCIDRs: [0.0.0.0/0]`, from the attribution section.

**Secure Web Proxy in next-hop mode stays a swap-in for this VM**, as AWS Network Firewall is for
the AWS gateway. It decides by SNI without decrypting, logs the client address, and is managed.
It is not the base for three reasons: its drops are silent, the allowlist would have to be
translated into its policy rules through the Google API, and it gives AWS and compose nothing.
**NetworkPolicy logging stays useful as a second line:** deny-only logging records attempts the
NetworkPolicy blocks (RFC 1918, the metadata range) cheaply. It is not the record of egress.

Still open on GKE: the HA shape. A managed instance group of one, with the route following the
instance, or an internal passthrough load balancer as next hop. Also the throughput sizing.
Both belong to #2658.

### AWS: the S3 gateway endpoint is a passthrough the gateway does not close

`S3Endpoint` (`00-network.yaml`) routes the S3 prefix list off the private route table and has
no endpoint policy, which means full access. A workspace can therefore reach any S3 bucket in
the region without passing the NAT gateway today, and without passing the egress gateway that
decision 5 puts in its place. The endpoint exists so that ECR layer bytes stay off the NAT
gateway. The fix, an endpoint policy limited to the buckets the stack needs, or no route for
the workspace subnets, is #2657. Even with a policy, access through the endpoint is not
attributed per workspace.

### AWS: no S3 gateway endpoint for now (decided with the maintainer, 2026-10-25)

`S3Endpoint` is removed from `00-network.yaml` (#2657). A policy on it was written and reviewed
(commit `6829e6e`, in the history of PR #2697): the ECR layer bucket, the AL2023 repositories and
the models bucket, the latter limited to this account's buckets. It does not do what the
first plan assumed. A request the policy refuses does not fall back to the NAT gateway, because
the S3 prefix-list route is more specific than the default route; it is AccessDenied, which would
break any same-region S3 use by a workspace. So S3 now leaves through the NAT gateway like other
egress, and through the egress gateway once decision 5 lands, where it can be recorded. The
cost: ECR layer bytes cross the NAT, about 918 MiB compressed per Fargate workspace start
(`deploy/aws/ecs/README.md`, start-latency measurement), at NAT data-processing rates. The end
state, an endpoint limited to ECR layers and the stack's buckets with workspace S3 through the
gateway's explicit proxy, is #2700 and lands with the AWS gateway's first stage. Not yet verified
on a deployed account.

### Open point 4: SSH git and DNS (decided with the maintainer, 2026-10-24)

- **SSH git is off by default and a deploying organisation can turn it on per host.** The gateway
  has no name for SSH. When the organisation names an SSH host in the allowlist, nftables admits
  port 22 to the addresses that host resolves to on the gateway, and logs the destination address
  and the time. This is a decision by address, not by name: in log-only too, the record for SSH
  is an address, attributed like any other event. With no SSH host named, port 22 is dropped
  and the attempt is logged.
- **DNS: direct queries to any resolver other than the VPC's or the cluster's are dropped and
  logged** (port 53, and 853 for DNS over TLS), with nftables on the gateway. The content of
  queries to the stack's own resolver is not recorded. Route 53 Resolver query logging and
  Cloud DNS logging are not part of this record.

### AWS first stage: its acceptance includes what #2604 left unverified

The fix for #2604 (PR #2611) is on `main`, but no deployed stack can exercise it. A sandbox
deployment of `main` had no `AF_EGRESS_PROXY_ADDR` on the CP, and the ingress stack has no
parameter that sets it. No parameter and no launcher are added for `control-plane egress-proxy`,
which is being retired (decision 4). Instead, the acceptance of the gateway's first AWS stage
includes, on a stack with egress configured:

- a started ecs workspace and a started ecs-ec2 workspace have the proxy variables in their task
  definition;
- the task-role credential path (`169.254.170.2`) and the instance metadata path still work.

#2604 stays open until that run.

### Sources (read 2026-10-24)

- GKE, "IP masquerade agent" (concept) and "Configure an IP masquerade agent" (how-to):
  default SNAT to the node address, the default non-masquerade list, `nonMasqueradeCIDRs:
  0.0.0.0/0`, `--disable-default-snat`, Cloud NAT for pod ranges.
- VPC, "Routes": tag-scoped static routes, order of evaluation (policy-based, subnet, custom).
- Cloud NGFW, "Fully qualified domain name objects overview": resolution and its limits.
- Secure Web Proxy: "Overview", "Deploy Secure Web Proxy as next hop", "Security rules"
  (SNI `host()` without TLS inspection in next-hop mode), "View proxy transaction logs"
  (`source_ip`).

## Addendum (2026-10-24) — decision 4: the gateway agent is built

The agent of decision 4 exists as `control-plane egress-agent` (#2683, code in
`control-plane/internal/egressagent`). Squid itself, nftables and the deploy templates are not
part of it. **It was not run against a real Squid**: the parser and the rotation handling are
tested with fixture lines and files.

- **Shape.** A subcommand of the CP binary, as `egress-proxy` and `efs-home-op` are, so the
  gateway gets the same binary. The package imports nothing from the CP's `main`, so it can
  become its own binary if the size ever matters on the gateway.
- **Policy.** The agent polls `GET /internal/egress/policy` and writes two files with a temp file
  and a rename: the allow file (dstdomain entries, reduced to the minimal sorted set, because
  Squid warns about an entry another one covers and the defaults have such pairs) and a mode
  include holding the `ssl_bump` and `http_access` lines for log-only or enforce. It reloads
  Squid when a file's bytes changed, and once at start, since a previous run may have written
  the files without a reload that worked; that reload uses the files on disk and does not wait
  for the CP. An unreachable CP, a non-200 answer or an empty
  allowlist leaves the files as they are.
- **Log.** The agent owns Squid's `logformat` (`af1`, documented in the package): the time the
  connection started (`%tS`, not the time the line was written: a tunnel is logged when it
  closes, possibly after the workspace released its address), source and server address,
  Squid's status and bump mode, and the SNI and request domain, URL-encoded so a hostile SNI
  cannot add fields. Each line is one event. Its id is the file's generation plus the line's byte
  offset. The generation is a random id the agent writes into the file's extended attribute
  `user.af-egress.gen` the first time it sees the file: it survives a rename and a restart, and
  a newly created file has none. Nothing derived from the file itself is unique: a new file can
  reuse a deleted one's inode, the birth time has the kernel clock's few-millisecond resolution
  (or is not recorded), and two files can start with the same line. A collision makes the agent
  resume at the wrong offset and the CP drop real events as duplicates. The agent therefore needs
  write access to Squid's log files and a filesystem with user xattrs, and refuses to start
  without them. A generation is fsynced before any event carries it, so a host crash cannot
  leave lines on disk whose generation was lost and is then assigned again. The offset is committed only after a 2xx. A line whose tail is damaged is still sent with its time and source
  and no name. Rotation must be Squid's own (`logfile_rotate` >= 1); `copytruncate` is not
  supported.
- **Verdict.** `allowed` is the policy's verdict on the name, and false when Squid denied or
  terminated the connection. In log-only it is the verdict the connection would have had. It is
  computed with the policy current when the line is shipped, so a line written just before a
  policy change can carry the new verdict.
- **One matcher.** The allowlist matcher moved to `control-plane/internal/egressallow`, shared by
  the CP and the agent.

## Addendum (2026-10-25) — retention of raw events (#2675)

The decisions above stand. `egress_event` and `workspace_address` were never deleted; at 100
events/s the former takes about 8.6M rows a day. The maintainer decided:

- **Raw events are kept 30 days**, set by `AF_EGRESS_EVENT_RETENTION_DAYS` (`0` keeps them
  forever). `egress_daily` is not touched.
- **A purger deletes in bounded batches** (5000 rows per statement, oldest first, a short pause
  between them) on SQLite and Postgres. It runs on one CP at a time under the CP lease
  `egress-event-purge`, as the address recorder does.
- **`workspace_address` is kept at least as long as the events it attributes.** A closed
  interval is deleted only when its end is older than the purge cutoff, and events go first. An
  open interval is never deleted.
- **The bound on resends.** Dedup on `id` only holds while the row exists, and the gateway agent
  resends from its committed offset, so after a long outage it can resend lines older than the
  window. Ingest therefore refuses to store, and to count into `egress_daily`, an event that
  carries an `id` and whose `at` is older than now minus the retention, or older than the
  **purge floor**. The floor is a monotone cutoff kept in the database (`cp_counter`,
  `egress-event-floor`): a purge raises it, and commits it, before it deletes, and
  `RecordEgressEvent` reads it in the same transaction as the insert (`FOR SHARE` on Postgres, so
  a raise waits for ingests in flight). So a deleted event is refused by every CP whatever its
  clock or its retention setting, and a later longer retention, or `0`, never lowers it. It logs
  how many it refused and still answers 2xx, so the gateway does not retry for ever. Events
  without an `id` (the own proxy's) were never deduplicated and are not affected.
  **Consequence:** an event arriving later than the retention window after it happened is
  lost, from the raw table and from the aggregate. That is the price of not keeping a table of
  purged ids.
- **No archive in this change.** Archiving to object storage before the purge is #2699.

## Addendum (2026-10-25) — the address recorder reads ECS once per cluster

The recorder asked ECS about each workspace's own service (`ListTasks` RUNNING and STOPPED,
then `DescribeTasks`), about three requests per running workspace per sweep against the
account-shared ECS read throttle. A factory that implements `runtime.AddressSnapshotter` now
serves a whole sweep from one read per cluster: `ListTasks` per desired status (paged),
`DescribeTasks` in batches of 100, grouped by the task's `service:<name>` group. A factory
without it, and every other runtime, keeps the per-workspace path.

The interval rules do not change. An open ref the cluster listing does not return is
described by ARN, as before, and MISSING stays unresolved. A failed page or batch fails the
read of every workspace in that cluster for the sweep, so none of their intervals is closed
on a partial listing. Follow-ups: #2674 (acceptance on a real account).

## Addendum (2026-10-25) — workspace subnets, and the S3 endpoint back on the private route table (#2716)

The decisions above stand, and so does the one of 2026-10-25 that a policy-limited endpoint
must not decide a workspace's S3. What changes is where the workspaces sit:

- **Workspace task ENIs have subnets of their own**, one per AZ, on a workspace route table
  whose only route out is the NAT gateway. Decision 5's gateway replaces the NAT as the default
  route of *this* route table only, as the GKE design scopes the route to the workspace node
  pool; the CP, the slot instances and the engines stay off the gateway. The CP is handed the
  subnets as `AF_ECS_WORKSPACE_SUBNETS` (30-ingress `WorkspaceSubnets`, from 00-network's output).
- **The S3 gateway endpoint is back on the private route table only**, with the policy reviewed
  in commit `6829e6e` (ECR layers, the AL2023 repositories, the models bucket) plus the SSM
  Agent update buckets (`amazon-ssm-<region>`, `aws-ssm-<region>`), which the slot and engine
  instances need for `AWS-UpdateSSMAgent`, and the ECS agent download buckets
  (`amazon-ecs-agent`, `amazon-ecs-agent-<region>`), which ecs-init reads when its cache is
  missing. It sits behind `S3EndpointEnabled`, which stays
  `false` on a running stack until every workspace has been restarted into the workspace
  subnets, and is the rollback. Nothing a user
  drives runs on that route table, so a refusal there breaks no workspace. On ecs-ec2 the ECS
  agent pulls the image through the slot instance's ENI, so a cold pull stays on the endpoint.
- **Fargate workspaces move too.** They gain nothing on pulls (Fargate pulls through the task
  ENI), but left on the private route table their own S3 would meet the endpoint's policy, and
  one route table for every workspace is what the gateway needs.
- **An AZ with no workspace subnet is refused**, never filled from the private subnets, and gets
  no new home.

Open point 5 is unchanged in substance: `pause.sh` drops both default routes through
`NatEnabled`. Not verified on a deployed account (cold pull through the endpoint, a
workspace's S3 through the NAT, the CP reaching the agent across the subnets). Follow-ups: #2716.

## Addendum (2026-10-25) — optional archive of raw events before the purge (#2699)

The decisions above stand. A deployment that must keep the per-connection record longer than
the retention can set `AF_EGRESS_ARCHIVE_URL` (`s3://bucket/prefix` or `file:///dir`; empty,
the default, is off). The maintainer decided:

- **One gzip JSON-lines object per UTC day**, partitioned `egress/yyyy=/mm=/dd=`, holding the
  day's events and the `workspace_address` rows whose interval meets the day, so a day can be
  read and attributed (with the address supplements, below). A `header` line opens it and a `manifest` line (counts, id and time
  range, schema version) closes it, so a partial or wrong object is detectable. Layout and
  operation: `docs/build/03-control-plane.md` §3.8.
- **Never delete what was not archived.** Only whole UTC days before the cutoff are archived, so
  with an archive the cutoff is the UTC midnight at or before now minus the retention. The floor
  is raised to it before the archive is read, so a late event for an archived day is refused,
  not deleted unarchived. Days are archived oldest first; the first failed or unconfirmed day
  stops the deletable range at its start, and the next run retries. Storage is a three-method
  interface (`Exists`, `Get`, an atomic `Put`) with S3 and local-directory implementations; a day whose
  object exists is verified (header, counts, gzip checksum) and not rewritten, which makes the crash-after-write retry safe and keeps a
  delete cut off midway from replacing a full day with the remainder.
- **Address rows are archived by version.** The floor does not close `workspace_address` to writers, so the closed rows to delete are written as a supplement object right before, and exactly those row versions are deleted. Read-back collapses the address rows of the day objects and supplements to one per (ref, address, from), taking the smallest non-zero `until` (0 is open, as the store only ever shortens an end), and attributes after that.
- **Read-back is offline** (download, attribute by source and time). Archived events are not
  re-ingested: the ingest bound above is unchanged, so the archive and `egress_daily` never
  double count.

Follow-ups: #2720 (a GCS sink).

## Addendum (2026-10-25) — AWS first stage: `EgressRoute` and the gateway (#2717)

Decision 5 for ecs and ecs-ec2 is built as a choice, not a replacement: `00-network`'s
`EgressRoute` (`nat-gateway`, the default, or `gateway`) decides the default route of the
**workspace** route table only (#2716). The CP, the slot instances and the engines keep the NAT
gateway, which therefore stays in both modes. Not run on a real account.

- **The route follows replacement through a fixed interface.** The stack owns `GatewayEni`
  (source/destination check off) and its Elastic IP; the workspace route table points at it,
  and each instance of the Auto Scaling group of one attaches it at boot as its second
  interface and sends all its traffic out of it. The route, the explicit proxy address and the
  egress address never move, and the instance role needs no route or address permissions,
  only `AttachNetworkInterface` on that interface. The price: one AZ (the interface's), and a
  blackholed route while a replacement boots. Rejected: an instance that rewrites the route
  and re-associates the address at boot (more permissions, and the stack and the instance then
  both own the route), and a single EC2 instance with the interface as its primary (no
  replacement path that CloudFormation can perform with a fixed interface).
- **Squid** runs the explicit proxy and the two intercept listeners (80, 443) on one instance,
  with the lines of `egressagent/doc.go`. The explicit port is an `ssl-bump` port as well:
  `ModeEnforce` decides CONNECT by SNI in `ssl_bump`, and a CONNECT on a port without
  `ssl-bump` would pass `http_access` unchecked. Nothing is forwarded (`ip_forward=0`); the
  security group admits only the workspace subnets on 80/443/3128, so other protocols are
  dropped unrecorded in this stage. Whatever the mode, Squid refuses loopback, link-local
  (the gateway's own metadata service, whose role a workspace could otherwise read through
  the explicit proxy), the VPC and ports other than 80/443, and nftables drops Squid's own
  connections to 169.254.0.0/16. Until the agent's first policy, Squid refuses everything: a
  replacement instance or an unreachable CP fails closed, not into log-only.
- **The agent** is the CP binary copied out of the CP image, run as Squid's user; its
  configuration (image, public URL) comes from an SSM parameter 30-ingress writes, rechecked
  every 10 minutes, so a release updates it without replacing the instance.
- **`AF_EGRESS_TOKEN`** is generated by Secrets Manager in `00-network`; only its ARN travels
  (30-ingress `Secrets`). It reaches the CP and the agent and **never a workspace**: it is the
  credential that lets a caller write egress events. Workspaces get the proxy variables of
  `AF_EGRESS_PROXY_ADDR`, with `169.254.170.2`, `169.254.169.254` and the CP's internal host in
  `NO_PROXY` (#2604's acceptance, still to be run).
- Open points 5 and 7 stay open for the gateway: `pause.sh` refuses `EgressRoute=gateway` and
  `standup.sh` warns (#2729); workspaces egress from the gateway's own Elastic IP, not the NAT
  gateway's, and moving one address between the two is #2728.

Follow-ups: #2728, #2729, #2687 (real Squid; the README gives the steps on this instance).

## Addendum (2026-10-25) — a GCS archive sink (#2720)

`AF_EGRESS_ARCHIVE_URL` also takes `gs://bucket/prefix`: a third implementation of the archive
sink interface (#2699), through the official `cloud.google.com/go/storage` client, which is the
only new client library (`google.golang.org/api` comes in with it as a direct import). Layout and the verify-and-never-rewrite rules are unchanged.

- **Credentials are Application Default Credentials only** (Workload Identity on GKE); there is
  no key-file setting. The role is `roles/storage.objectCreator` plus `roles/storage.objectViewer`
  on the bucket. The sink never lists, deletes or overwrites.
- **A key is never replaced.** The upload carries `ifGenerationMatch=0`, so a second Put to a
  key fails with 412 and the object there stays. The 412 is always an error, even when the
  stored length and CRC32C equal what was sent: a retry after a lost response looks like that
  and so does a CRC32C collision, and a nil would skip the archiver's verify and database
  comparison. The next run finds the key with `Exists` and verifies it, as for S3.
- **Confirmed means the stored object matches.** The upload is sent with the body's CRC32C
  (the service rejects other bytes) and Put compares the finalized object's length and CRC32C
  before returning nil. A failed copy cancels the upload instead of closing it, since closing
  finalizes what was sent.
- **Not run against real GCS.** The tests use a fake of the JSON API behind
  `STORAGE_EMULATOR_HOST`. The CP binary grew by the client and its gRPC and OpenTelemetry
  dependencies (sizes in the PR); the release build uses the `disable_grpc_modules` tag, which drops the unused gRPC transport.

## Addendum (2026-10-26) — the CP's own hosts are always in the served policy (#2742)

On the sandbox (ecs-ec2, `EgressRoute=gateway`, log-only) the deployment's own console host was
would-block for every workspace connection. ECS workspaces have only the public URL
(`AF_CP_BASE_URL`); `NO_PROXY` gains the CP host only from `AF_CP_INTERNAL_URL`, which only
Kubernetes sets; and the gateway intercepts 443 on the workspace route table, so `NO_PROXY` alone
would not have kept the traffic away from Squid either. In enforce, workspaces would have lost
their CP.

- **The policy the CP serves always admits the hosts of `PUBLIC_BASE_URL` and
  `AF_CP_INTERNAL_URL`** (`cpEgressHosts`), as exact hosts next to the product defaults, outside
  the allowlist table. No admin edit can remove them; the Admin list and `list_allowlist` show
  them as built-in. Exact hosts only, so enforce is not opened for their parent domain. This holds
  for every runtime, because the gateway agent and the own forward proxy both enforce what this
  endpoint serves: docker and native workspaces reach the public URL through the proxy as well.
- **Not done here: an in-VPC CP URL for ECS** (option b in #2742). It would keep CP traffic off
  the gateway, but it needs a CP internal listener, a security group rule and template bytes,
  and it would not remove the need for the above: the public URL stays in use for links a person
  opens and as the internal git host.
- Not run on a real account; the enforce check on the sandbox is still owed.

Follow-ups: #2746

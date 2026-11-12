# Deploy on ECS with CloudFormation

The CloudFormation templates (`cfn/`) and the scripts (`standup.sh`, `update.sh`, `release-ecr.sh`,
`pause.sh`, `teardown.sh`, …) for the native AWS adapter (`ecs` / `ecs-ec2`): one ECS service per
workspace, scaled to zero when it stops. The procedures that use them are in the operator guide:

| Page | What it covers |
|---|---|
| [Understand the ECS layout](../../../guide/operate/aws-ecs/overview.md) | What CloudFormation builds and what the Control Plane builds, the stacks and their deploy order |
| [Build on ECS](../../../guide/operate/aws-ecs/install.md) | Account prerequisites (service-linked roles, cost allocation tags, capabilities), the ECR push, the `30-ingress` stand-up |
| [Turn on optional features with stack parameters](../../../guide/operate/aws-ecs/options.md) | Preview subdomains, keys at rest on KMS (and running `af-cp` as a one-off task), remote MCP, provided routes, timers and outside engines, WAF |
| [Set up the alarm for an unreachable database](../../../guide/operate/aws-ecs/alarms.md) | `CpAlarmEmail`, the `20-platform` redeploy, recovery |
| [Size workspaces](../../../guide/operate/aws-ecs/workspace-size.md) | Workspace CPU, memory and working disk |
| [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md) | Scheduled execution |
| [Fix the egress IP address](../../../guide/operate/aws-ecs/egress-ip.md) | The address a customer allow-lists |
| [Control egress through the gateway](../../../guide/operate/aws-ecs/egress-gateway.md) | `EgressRoute=gateway` |
| [Add Japanese speech](../../../guide/operate/aws-ecs/tts.md) | The VOICEVOX engine (`cfn/50-tts.yaml`) |
| [Add inference engines](../../../guide/operate/aws-ecs/engines.md) | The fleet's own engines (`cfn/60-engines.yaml`) |
| [Use the EC2 slot pool](../../../guide/operate/aws-ecs/slot-pool.md) | `WsRuntime=ecs-ec2` |
| [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md) | Outdated and unresponsive slots, moving retained slots onto new user data |
| [Upgrade on ECS](../../../guide/operate/aws-ecs/upgrade.md) | `update.sh`, the manual steps, rollback, `clear-params.sh`, slot AMIs, upgrading from v0.34.x |
| [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md) | EFS backup and restore |
| [Keep costs down](../../../guide/operate/aws-ecs/cost.md) | Cost and ephemerality |
| [Stand up, pause and tear down with scripts](../../../guide/operate/aws-ecs/lifecycle.md) | The lifecycle scripts |
| [Tear down](../../../guide/operate/aws-ecs/teardown.md) | Teardown and the orphan sweep |
| [IAM permissions for deploying](../../../guide/operate/aws-ecs/iam.md) | The deploying principal's least privilege, the `CpTaskRole` policy budget |
| [Keep templates under the size limit](../../../guide/operate/aws-ecs/template-size.md) | The 51,200-byte limit and the S3 hand-off |
| [Troubleshoot ECS](../../../guide/operate/aws-ecs/troubleshooting.md) | Build, parameter, upgrade and start problems by symptom, including a cold start answering `starting` |

The parameter references stay next to the templates they describe:
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md) (`30-ingress`) and
[`cfn/PARAMETERS-60-engines.md`](cfn/PARAMETERS-60-engines.md) (`60-engines`).

## Egress IP (the address a customer allow-lists)

With the default `EgressRoute=nat-gateway`, a deployment egresses from **exactly one
address**: the Elastic IP on `00-network`'s NAT gateway. Everything outbound to the internet
goes through it — every workspace's git, model API and package traffic, and the CP's own
calls — because the slots and the CP task sit in private subnets with **no public IPv4**
(`AssignPublicIp: DISABLED`; the slot launch template deliberately assigns none, ADR 0045
decision 3-3). The only VPC endpoint is the S3 gateway endpoint on the private route table,
which reaches AWS's own S3 without an address of its own (#2716). So this is the value to
hand to a corporate firewall or a SaaS IP allow-list. With `EgressRoute=gateway` workspaces
egress from a second address, the gateway's own (`GatewayEipAllocationId`): register both
("Egress gateway", "Two egress addresses", #2728).

```bash
alloc=$(aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text)
aws ec2 describe-addresses --allocation-ids "$alloc" --query "Addresses[0].PublicIp" --output text
```

⚠️ **`describe-addresses` also shows the ALB's addresses** (`ServiceManaged: alb`).
Those are inbound, AWS-owned, and not contractually stable — never publish them.
Inbound is the FQDN's job.

**Keeping the address across a rebuild.** `NatEip` carries `DeletionPolicy: Retain`
and `UpdateReplacePolicy: Retain`, so no stack operation — including a full
`teardown.sh` — can release it. Capture the allocation id and pass it back at the next
stand-up:

**`capture-env.sh` carries it.** It saves stack **Parameters**, and after a stand-up that
created the EIP itself `NatEipAllocationId` *is* the empty parameter that selected the
`CreateNatEip` branch — the real id exists only as the stack **Output**. So `save_params`
applies one rule (added 2026-09-02): **an Output whose key matches a parameter that was
captured empty supplies the value.** The id lands in the capture like any other parameter,
and the next stand-up hands it back.

🔴 **A capture taken before that date has no `NatEipAllocationId` line, and standing up
from it takes a second EIP.** The empty parameter selects `CreateNatEip` again, the
retained address is orphaned, and the value every customer allow-listed changes silently
(plus ~$3.6/month per orphan). Measured on 2026-09-02, before the fix: one round trip left
one unassociated `af-…-nat-eip` behind and the capture had no `NatEipAllocationId` line.
**Look at the capture before standing up from an old one:**

```bash
grep '^NatEipAllocationId=' ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

Missing or empty, and the deployment is **still up** → re-capture (`capture-env.sh
--force`), or read the Output and write the line by hand:

```bash
aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text
# -> NatEipAllocationId=eipalloc-... into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

Already torn down → the retained address is still in the account, so find it by tag rather
than letting the next stand-up allocate a fresh one:

```bash
aws ec2 describe-addresses --output text --query \
  "Addresses[].[AllocationId,PublicIp,AssociationId,Tags[?Key=='Name']|[0].Value]"
# the row named af-<stack>-nat-eip with no association id is the one that was retained
```


```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM --parameter-overrides NatEipAllocationId=eipalloc-...
```

🔴 **Do NOT set that parameter on a running deployment — not even to the address the
stack already created.** Measured on a live deployment (2026-08-26, via
`--no-execute-changeset`): CFN sees the `AllocationId` property's source change from
`!GetAtt NatEip.AllocationId` to a parameter, evaluates it `Static` /
`RequiresRecreation: Always`, and plans `ReplaceAndDelete` on the **NAT gateway** —
with the same allocation still attached to the old one. Egress drops and the create
cannot succeed. The parameter is for a *fresh* stand-up, where it is an ordinary
create. On a live stack, retention alone already guarantees the address.

⚠️ **One NAT means one AZ.** `00-network` puts a single NAT gateway in the first AZ
for cost; if that AZ is lost, *every* workspace loses egress, including those in the
second AZ. Adding a NAT per AZ is the fix — and it makes the deployment egress from
**two** addresses. Decide before the allow-list is published: an unattached EIP costs
~$3.6/month, so allocating the second address up front and registering both is the
cheap way to keep that door open.

## Egress gateway (`EgressRoute=gateway`, ADR 0122)

`00-network`'s `EgressRoute` chooses where the **workspace** route table's default route goes
(#2717). The CP, the slot instances, the engines and the home task stay on the NAT gateway
either way: only workspaces go through the gateway.

| | `nat-gateway` (default) | `gateway` |
|---|---|---|
| Recording | none: the Admin Egress tab stays empty | every workspace connection, by name (SNI / Host) and address, attributed to the workspace (#2674). The Admin Egress tab shows counts per host; the attributed events are `GET /api/admin/egress/events` (super_admin), not shown in the Console |
| Allowlist | not enforced | log-only, or enforce (a name outside it is refused) |
| Hourly cost | the NAT gateway (~$0.045–0.062/h by region) | the NAT gateway (the CP and hosts still use it) + the gateway instance (`t3.small` ≈ $0.02–0.03/h) + its Elastic IP ($0.005/h) |
| Per-GB cost | NAT data processing ($0.045–0.062/GB) on all workspace traffic, S3 included | none for workspace traffic (normal data transfer only; same-region S3 transfer is free) |
| Operations | managed by AWS | an instance to run: AMI updates replace it, a replacement leaves workspaces without egress for a few minutes |
| Availability | AWS-managed, one AZ | one instance in one AZ (an Auto Scaling group of one replaces it) |
| Non-HTTP egress (SSH git, other ports) | works | enforce: only named destinations (`tcp:` entries, below), the rest dropped and recorded; log-only (the default mode): passed and recorded as would-deny |
| Workspace egress address | `NatEip` | the gateway's own address, a second one to allow-list (#2728) |

**How it works.** An Auto Scaling group of one AL2023 instance in `PublicSubnet0`. At boot it
attaches `GatewayEni`, a network interface the stack owns: the workspace route table points at
it, its Elastic IP (`GatewayEip`) is the workspace egress address, and its private address is
the explicit proxy (`EgressProxyAddr`, `:3128`). A replacement instance attaches the same
interface, so neither the route, the proxy address nor the egress address ever moves. Squid
intercepts port 80 from the workspace subnets and serves the explicit proxy; intercepted 443 goes
to `af-cp egress-relay`, which hands it to Squid as a CONNECT to the address the workspace
dialled (#2759). Other TCP goes to `af-cp egress-relay --mode tcp` (below). Nothing is forwarded (`ip_forward=0`). `af-cp egress-agent` (the CP
binary, copied out of the CP image) runs as user `squid`, writes Squid's allow and mode files
from the CP's policy and ships the log to `/internal/egress` with `AF_EGRESS_TOKEN`, which the
stack generates in Secrets Manager. The
CP gets `AF_EGRESS_PROXY_ADDR` and `AF_EGRESS_TOKEN` (30-ingress `EgressProxyAddr` /
`EgressTokenSecretArn`), so Fargate and ecs-ec2 workspace task definitions carry the proxy
variables, with the task-role credential endpoint (`169.254.170.2`), instance metadata
(`169.254.169.254`) and the CP's internal host in `NO_PROXY`, and the address recorder
(#2674) runs. The token itself never reaches a workspace: it is the agent's credential.

**Non-HTTP TCP (SSH git, databases; #3369).** `GatewaySg` admits all TCP from the workspace subnets
and the decision is nftables' and the passthrough relay's. **In enforce**, nothing leaves except
HTTP(S) until an administrator adds a **`tcp:` allowlist entry**; **in log-only (the default mode)**
every TCP port still passes and is recorded as refused, so nothing is closed until you switch to
enforce. Entries are added in the Admin Egress tab, or proposed by a member and approved by an
administrator: `tcp:git.example.com:22`, `tcp:db.example.com:1433`, or a range
`tcp:db.example.com:11000-11999` for a service that redirects clients (at most 2048 ports; an exact
host or an IPv4 address; never 53, 80, 443, 853 or 3128). The agent resolves the host every 30 s and
the relay admits connections to the addresses it has resolved to in the last 15 minutes, on those
ports. Every connection is recorded by address and port and attributed like the others
(`GET /api/admin/egress/events`; the port is part of the destination). In **enforce** an unnamed
destination is dropped and recorded; in **log-only** it is passed and recorded as refused, so the
Admin tab shows what enforce would cut. The VPC, link-local (the metadata service), loopback, this
host and ports 53 / 853 are refused in both modes. UDP and ICMP stay dropped by the security group,
unrecorded. **Before cut-over, the destination's own firewall must admit the gateway's Elastic IP**
(the workspace egress address) on those ports; a managed database that redirects to a port range
needs that range admitted and named. The relay's log is `/var/log/squid/af-tcp.log` on the instance
and `journalctl -u af-egress-relay-tcp` its service. Not run on a real stack yet (ADR 0122,
addendum 2026-11-05).

**A denied TCP flow looks connected at first.** The redirect happens before the relay decides, so
the relay accepts the workspace's connection, reads the original destination (`SO_ORIGINAL_DST`),
and only then refuses it by closing the socket without dialling out. The workspace's handshake
has therefore completed: `nc -z host port` or a bare `connect()` reports success, and a later
read or write can fail (EOF, or a reset such as `Connection reset by peer`; which one depends on
timing and unread data, and a first small write can still succeed locally). On the sandbox it
was a reset with 0 bytes received. The gateway makes no outbound connection. A "connected" probe
is not evidence of "allowed". To tell, read the relay log, `af-tcp.log`: a connection whose
decision could be recorded is a line ending `<verdict> <entry>`. `deny` is a refusal: in enforce
any destination not named by a `tcp:` entry, and in **both** modes a fenced destination (the VPC,
link-local, loopback, this host, ports 53 / 853 and the like) or any destination while no policy
has been loaded yet. `would-deny` is a connection log-only passed, `deny-cap` one refused because
the relay's connection limit was full, and `admit` an allowed one. No line is written when the
original destination cannot be read or the log cannot be written. The Control Plane builds the
admin events (`GET /api/admin/egress/events`, the Egress tab) from that log and receives them
asynchronously; they are not the same lines.

**The gateway's input chain.** `nft list ruleset` on the gateway shows `chain input { ...
policy accept; }`, which does not mean the instance is open. `GatewaySg` admits all TCP from the
workspace subnets, so the host filters itself: the `input` chain (`/etc/af-egress/nft.conf`,
table `inet af_egress`) accepts, for packets arriving from a workspace subnet on the gateway's
interface, only established/related traffic, flows redirected by the `prerouting` chain
(`ct status dnat`) and TCP port 3128 (the explicit proxy). Anything else from those subnets is
logged with the prefix `af-egress-input-drop ` (rate-limited to 10 per second; read it with
`journalctl -k`) and dropped by the final `drop` rule. Every rule matches workspace-subnet
sources only, so the chain's `policy accept` applies to everyone else (the instance's own
traffic, the VPC's). The `forward` chain
is `policy drop` and `ip_forward` is 0: nothing is routed through the gateway.

**Turning it on** (an existing stack keeps `nat-gateway` until you do this). Allocate the
workspace address first and have it allow-listed (next paragraph), then:

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM --parameter-overrides EgressRoute=gateway \
  GatewayEipAllocationId=eipalloc-... GatewayEipAddress=<its public address>
./update.sh --profile <p> --region <r>    # 20-platform (the token read) and 30-ingress
```

Without the two `GatewayEip*` parameters the stack allocates the address itself (and retains
it), so it is only known once workspaces already egress from it.

The route moves to `GatewayEni` as soon as the first command finishes, before the instance
has attached it: workspaces have no egress until the instance is up (a few minutes, see
`/var/log/af-egress-gateway.log` on it), and the agent has nothing to ship until `update.sh`
has written its configuration (`/af-egress/<network stack>/agent`) and fetched a first policy:
until then Squid refuses everything (also after an instance replacement), so a gateway that
cannot reach the CP stays closed rather than open. Until that first policy, `af-egress-install`
fails and runs again 30 s later (a missing configuration fails at once; a missing policy after
a 30 s wait for the agent), so the gap after `update.sh` writes the configuration is about a
minute, plus the first pull of the CP image and the CP's own start. Afterwards it runs every
10 minutes, which is how a new CP image reaches the agent (#2758). Running workspaces get the proxy variables
on their next Start (until then they are intercepted). The agent is
the CP binary copied out of the CP image, so the gateway pulls that image: `GatewayInstanceType`
must always match `GatewayAmiId`'s architecture, and has to match the CP image's only when the
image is single-arch. When the CP image is a
multi-arch index, the gateway pulls the variant for its own architecture, so an x86_64 gateway
ran fine next to an arm64 CP (30-ingress `CpArch`) in the #3380 sandbox run.

**Back to the NAT gateway** is a maintenance window, not the same two commands in reverse: a
running workspace keeps `HTTP(S)_PROXY` pointing at the gateway until it is restarted, and once
the gateway is gone every proxy-aware client in it times out. So take the proxy away first:

1. Clear the proxy on 30-ingress, everything else at its previous value:
   `./clear-params.sh --profile <p> --region <r> --stack <ingress stack> EgressProxyAddr
   EgressTokenSecretArn` builds and prints the change set (expect `Modify` of `TaskDef`, the CP
   task definition, and of `Service`, which follows its new revision, and the two keys going to
   empty; every other parameter unchanged), then run the `--execute` command
   it prints (or `--yes` to skip the look)
   (not `update.sh`, which would hand the gateway's outputs back while it exists, and not
   `deploy --parameter-overrides EgressProxyAddr= EgressTokenSecretArn=`, which did not clear
   them in the #2763 rehearsal: "Upgrade (runbook)").
2. Stop and start every running workspace (`pause.sh --status` lists them) and check that its
   task definition no longer has the variables:
   `aws ecs describe-task-definition --task-definition <family> --query "taskDefinition.containerDefinitions[0].environment[?name=='HTTPS_PROXY']"` is empty.
   Their traffic is still intercepted by the gateway meanwhile.
3. Deploy 00-network with `EgressRoute=nat-gateway` (`--capabilities CAPABILITY_IAM`). The
   workspace route moves back to the NAT gateway; `GatewayEip` is retained.
4. Run `update.sh` (it finds the network's outputs and 30-ingress's values both empty and changes
   nothing else).

**Pausing (#2729).** `pause.sh` stops the gateway through `00-network`'s `GatewayEnabled`:
`false` scales the group to zero instances (never by hand with `aws autoscaling`, which the
next deploy of the stack undoes). `GatewayEni` and `GatewayEip` stay, so `--up` brings back the
same route target, proxy address and egress address; the Elastic IP keeps billing ($0.005/h).
The gateway goes before the CP, and only after two things are confirmed: no Workspace task is
left in the cluster, whatever its service's desired count (`AF_WS_TASK_WAIT_SEC`, default 600),
and, with Squid stopped so every tunnel is logged, the CP has accepted the whole log (each log
file's size is under the offset the agent committed in `state.json`; `AF_GATEWAY_DRAIN_SEC`,
default 300). The scale-in destroys the instance's disk, so if either cannot be confirmed
`pause.sh` fails and leaves the gateway and the CP running. While the gateway is off the
workspace route **blackholes**: that is harmless only because no workspace runs, so do not
start one on a stack with `GatewayEnabled=false`. `--up` starts it in the same deploy as the NAT
gateway and waits for it twice: before the CP starts, until an instance has attached
`GatewayEni` and Squid and `af-egress-agent` run; after the CP is up, until the agent's current
run has applied a policy (written the mode file and logged `policy applied`, or `reloaded Squid
with the policy on disk` for a run that started while the CP was down; both only after Squid's
reload succeeded), and the last such line is no older than the policy files. Only the CP can give it that policy, and Squid refuses
everything until then. A gateway that does not come up keeps the CP down;
`AF_GATEWAY_WAIT_SEC` (default 1200) bounds each wait. The checks on the instance run through
SSM Run Command: the operator needs `ssm:SendCommand` (document `AWS-RunShellScript`, on the
instance tagged `af-egress-gateway=<network stack>`) and `ssm:GetCommandInvocation`; the
instance role already has `AmazonSSMManagedInstanceCore`. `--status` shows the gateway. `standup.sh` overrides a captured
`GatewayEnabled=false` to `true` and waits for the first policy at its end.

**Two egress addresses (#2728, decided 2026-10-25).** With the gateway a deployment egresses
from two permanent addresses: the CP, the slot hosts and the engines from the NAT gateway's
(`NatEip`), every workspace from the gateway's. An organisation that allow-lists the deployment
registers both. The workspace address is handled like the NAT's:

- **Allocate it before the switch** so it can be allow-listed while nothing uses it:
  `aws ec2 allocate-address --domain vpc --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=Name,Value=af-<network stack>-egress-gateway-eip}]'`
  gives the `AllocationId` and `PublicIp` to pass as `GatewayEipAllocationId` and
  `GatewayEipAddress` (both, or neither: the template's Rules refuse one alone). The ALB
  admits the gateway by `GatewayEipAddress`, which CloudFormation cannot read from an
  allocation it did not create. A handed-in address is never created, replaced or released by
  the stack.
- **It survives a rebuild.** An address the stack allocated itself is retained
  (`DeletionPolicy: Retain`); the outputs `GatewayEipAllocationId` and `GatewayEipAddress`
  carry it whoever allocated it, and `capture-env.sh` fills the two parameters from them when
  they were captured empty, as it does `NatEipAllocationId`. With `EgressRoute=gateway`,
  `standup.sh` checks before building that the allocation still exists, that its address is the captured one, and that
  nothing but this deployment's gateway interface holds it.
- **Handing back a retained address.** A gateway that was switched back to `nat-gateway`, or a
  torn-down deployment, leaves its address allocated and unassociated (it keeps billing,
  $0.005/h). Find it by its tag and pass it with the next switch to `gateway` or the next
  stand-up, instead of letting the stack allocate a new one:

  ```bash
  aws ec2 describe-addresses --output text --query \
    "Addresses[?Tags[?Key=='Name' && Value=='af-<network stack>-egress-gateway-eip']].[AllocationId,PublicIp,AssociationId]"
  # one row with AssociationId None: pass AllocationId and PublicIp as
  # GatewayEipAllocationId= GatewayEipAddress= (with EgressRoute=gateway), or write both lines
  # into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network before standup.sh
  ```
- 🔴 **Set the two parameters when the gateway is created** (the switch to `gateway`, or a
  stand-up), not on a stack that already runs it. There the change replaces `GatewayEipAssoc`,
  so the interface's address is re-associated while workspaces use it. That is not measured.
  An address the stack created is already retained without them.
- **One address for everything instead** (option B, only for an organisation that cannot
  register a second address): put the gateway in a subnet routed through the NAT gateway, so
  Squid's connections leave from `NatEip`. That costs NAT data processing on all workspace
  traffic again ($0.045–0.062/GB), the per-GB saving above. It is not built. Moving `NatEip`
  itself onto the gateway (option C) is rejected: the NAT would need a new address for the CP
  and hosts, so the allow-list changes anyway, after a window with no egress at all.

**Egress event archive (optional, #2732).** The CP can write each past UTC day of raw egress
events to S3 before it purges them (`AF_EGRESS_ARCHIVE_URL`, #2699; `docs/build/03-control-plane.md`
§3.8). It is off until 10-data owns a bucket for it:

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM            # the S3 endpoint policy admits the bucket
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --parameter-overrides EgressArchive=create
./update.sh --profile <p> --region <r>     # 20-platform's CP grant, then 30-ingress's URL
```

`standup.sh` does the same on a rebuild when the capture has `EgressArchive=create`. The bucket
(`af-<data stack>-egress-archive-<account id>-<region>`) is encrypted, TLS-only, unversioned, expires archived days
after `EgressArchiveRetentionDays` (365) and is **retained** when the stack is deleted (a rebuild reuses it with `EgressArchive=existing`, which `standup.sh` sets by itself). The CP
task role gets `s3:PutObject` / `s3:GetObject` on `egress/*` and `s3:ListBucket` on that bucket
only, and the endpoint policy admits the same bucket for this account. Only a bucket made this
way works: a bucket of your own is refused at the endpoint. Parameters, the grant and the
reasoning: `cfn/PARAMETERS.md`, "The egress archive". Not yet run on a real account.

**Limits of this first stage.** Squid, the agent, the relay and this configuration, pausing and
resuming included, were verified on a test deployment only, **not yet in production**.

- **Raw events are kept 30 days.** The CP deletes raw egress events (and the address intervals
  that attribute them) older than `AF_EGRESS_EVENT_RETENTION_DAYS` (default 30, `0` keeps them
  forever); the per-host daily counts stay. The ECS templates have no parameter for it, so a
  deployment keeps the default. Use the archive above to keep the raw days longer.

- **Enforce decides on the TLS name (SNI), not on the address.** A workspace can send an allowed
  SNI to any public address on port 443, through the explicit proxy and, since #2759, on the
  intercepted path too. It cannot reach another port, loopback, link-local or the VPC that way,
  and a name outside the allowlist is refused whatever address it is sent to. Binding a name to
  its addresses would mean decrypting, which the gateway never does.
- **Intercepted 443 passes `af-cp egress-relay`.** Squid's own intercepting HTTPS port re-resolves
  the SNI and refuses the connection when the workspace's address is not in its own answer (409,
  "Host header forgery" in `cache.log`), whatever `host_verify_strict` says. Names whose DNS
  answers rotate (S3, ECR, STS) failed that way most of the time. The relay is in the CP image and
  starts with the agent, so before the first install a fresh gateway refuses intercepted 443
  (it refuses everything until its first policy anyway). A new CP image restarts it, which
  cuts the intercepted TLS connections open at that moment. It holds at most 4,096 connections,
  closes one 10 s after Squid ended its side, and closes one whose write stalls for 2 minutes.

**Checking the agent against a real Squid (#2687).** On a sandbox with `EgressRoute=gateway`:

1. Find the instance and open a shell:
   `aws ec2 describe-instances --filters Name=tag:af-egress-gateway,Values=<network stack> Name=instance-state-name,Values=running --query 'Reservations[].Instances[].InstanceId' --output text`,
   then `aws ssm start-session --target <id>`.
2. Boot and services: `sudo tail -50 /var/log/af-egress-gateway.log`;
   `systemctl status squid af-egress-agent af-egress-relay af-egress-relay-tcp af-egress-nft af-egress-install.timer`;
   `sudo squid -k parse` (prints the parsed configuration, fails on an error);
   `ip route show default` (the first default route is the attached interface).
3. Policy: `sudo cat /var/lib/af-egress-agent/af-allow.txt /var/lib/af-egress-agent/af-mode.conf`
   (the agent's header and the CP's allowlist, not the boot placeholder, which refuses
   everything: `# bootstrap: closed until the first policy`);
   `journalctl -u af-egress-agent` shows the reload.
4. From a workspace terminal: `curl -sI https://example.com` (explicit proxy) and
   `curl -sI --noproxy '*' https://example.com` (intercepted). On the gateway,
   `sudo tail /var/log/squid/af-egress.log` shows one `af1` line each, with the workspace's
   address, `TCP_TUNNEL` and the SNI. `sudo getfattr -n user.af-egress.gen /var/log/squid/af-egress.log`
   shows the generation, `sudo cat /var/lib/af-egress-agent/state.json` the committed offset,
   the Admin Egress tab the host's count, and `GET /api/admin/egress/events` the two events,
   attributed.
5. Rotation: `sudo squid -k rotate`, make one more request, and check that the agent finished
   `af-egress.log.0` and continued on the new file (no duplicate, no gap in the events).
6. Metadata: from a workspace, `curl -s -x <EgressProxyAddr> -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token`
   must be refused (Squid's `to_linklocal`, and nftables drops Squid's own connections to
   `169.254.0.0/16`); otherwise a workspace could read the gateway role's credentials.
7. Enforce: switch the mode in the Admin Egress tab, wait for the agent's reload
   (`af-mode.conf` now has `ssl_bump terminate all`), and check that a name outside the
   allowlist fails from the workspace (explicit and intercepted) and is logged as denied.

## Workspace size (CPU / memory / working disk)

Three `30-ingress` parameters set what every workspace gets by default; per-user
overrides live in the Console (Settings → members → "Set limits") and are clamped by the
tenant cap. Design and measurements: `docs/log/63`, decisions in `docs/decisions/0044`.

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsTaskCpu` | `AF_ECS_TASK_CPU` | `1024` | Fargate units; 1024 = 1 vCPU |
| `WsTaskMemory` | `AF_ECS_TASK_MEMORY` | `2048` | MiB |
| `WsDiskGiB` | `AF_ECS_WS_DISK_GB` | `50` | 21–200 sets ephemeral storage; 0 = Fargate's free 20 GiB (turns cache relocation off) |

**Only specific (cpu, memory) pairs exist**, and the steps are not uniform — 8 vCPU moves
in 4096 MiB steps and 16 vCPU in 8192, while 0.25 vCPU accepts only 512/1024/2048. The
full matrix was measured against the ECS API and is in `docs/log/63` §63.2. The CP snaps any
request onto a valid pair, so an odd value never reaches a task definition; the visible
effect is that **raising CPU can also raise memory** (4 vCPU cannot run below 8 GiB).

The working disk is **not persistent** — it is wiped when the workspace stops, like every
other task-local disk on Fargate. Only the EFS home survives. `WsDiskGiB` is also the
switch for moving small-file trees off EFS onto that disk: the workspace entrypoint
relocates the caches (Go build cache, `uv`, Go modules) and the Agent relocates build
artifacts (`node_modules`, `target`, `.venv`, `build`) as each working copy is created.
Both arm only when the disk is **30 GiB or more**, because the measured caches do not fit
alongside the image in 20 GiB. On EFS these trees are 8–30× slower to write (`docs/log/63`
§63.4).

**The default is 50 GiB, so relocation is on.** It used to be 0, which meant the feature
could never arm and every deployment kept building on EFS. Above the free 20 GiB you pay
about **$0.097/GiB-month, and only while the task runs** — roughly $2.9/month for a
workspace that never stops, well under a dollar for one that idles out. A deployment that
does not want it sets `WsDiskGiB=0` and behaves exactly as before. **Existing stacks keep
the value they were deployed with**: CloudFormation stores the parameter, so a stack
created with `0` stays at `0` until you update it — pass `WsDiskGiB=50` explicitly (or
accept the new default in the console) when you update.

Above 200 GiB the CP switches the working disk to an **ECS-managed EBS volume**, which
needs an ECS infrastructure role passed as `AF_ECS_INFRA_ROLE` (policy
`AmazonECSInfrastructureRolePolicyForVolumes`). The reference stacks do not create that
role — without it a >200 GiB request silently falls back to the free default. 🚧 This path
is untested on real infrastructure; ephemeral storage covers everything up to 200 GiB.

## Scheduled execution (timed wakes)

The CP scheduler wakes a stopped workspace at the appointed time and injects the prompt
(`docs/log/38`). It is **on by default** on every runtime and is a no-op while no schedule
exists. Four `30-ingress` parameters exist so a deployment can switch it off or give a
slow-booting substrate more room; **leave one empty to keep the product default** rather
than pinning a copy in the stack.

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `SchedulerInterval` | `AF_SCHEDULER_INTERVAL` | `1m` | Tick cadence. `0` hard-disables timed wakes: nothing fires, and the Console hides the Schedules section |
| `ScheduleWakeTimeout` | `AF_SCHEDULE_WAKE_TIMEOUT` | `300s` | How long one fire waits for the woken Agent. See the warning below |
| `ScheduleSettle` | `AF_SCHEDULE_SETTLE` | `5m` | How long the woken workspace is held up afterwards so the reaper cannot stop it mid-turn |
| `ScheduleJitter` | `AF_SCHEDULE_JITTER` | `2m` | Deterministic spread for cron fires so a 09:00 crowd does not wake at once. `0` = exact slot |

⚠️ **Do not lower `ScheduleWakeTimeout` on `ecs-ec2` without measuring.** Its clock starts
at the **wake**, roughly 20 seconds before the adapter's own `Agent healthy Ns after Start`
log does — the slot's `StartInstances`, ECS registration and home mount all run in the
background convergence — so the budget is tighter than that line reads. On a live
deployment the *same* workspace, on the *same* schedule and the *same* slot, took **65 to
131 seconds** on five consecutive mornings; the old 90s budget therefore delivered on two
of them and silently dropped three. A budget that lands inside the spread makes a stopped
workspace's morning schedule fire at random. An undelivered fire is now re-attempted on
later ticks for 15 minutes, but each retry re-wakes the workspace, so the budget should
still be big enough to win on the first attempt.

## Optional: Japanese speech (the VOICEVOX engine, `cfn/50-tts.yaml`)

Read-aloud works on every deployment without this stack: Polly does it, and the CP's `auto`
routing sends everything there whenever no engine is reachable. What `50-tts` adds is the
VOICEVOX (Zundamon) voice, as an ECS **Fargate** service that is **normally scaled to zero**
— an engine left running is about **$90/month** at 2 vCPU / 4 GiB against a deployment whose
whole run rate is around $165/month, and it is silent most of the day. ADR 0070 has the
prices, the break-even against Polly and the reasoning; this section is what to type.

**Turning it on** (`standup.sh` builds `50-tts` when, and only when, a `params/50-tts` file
exists in the captured state — the same shape as the pool layer):

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/50-tts
# Every parameter has a default, so this file may hold nothing but the namespace name;
# standup.sh overrides NetworkStackName / PlatformStackName / ServiceConnectNamespace
# from what it already knows about the deployment.
ServiceConnectNamespace=af.internal
CpuArchitecture=X86_64
TaskCpu=2048
TaskMemory=4096
# UseSpot=on   # ~70% cheaper, an interruption just falls back to Polly (see below)
```

`standup.sh` then carries the pinned image into ECR with `crane` during its images step,
deploys `50-tts` after the pool and before `30-ingress`, scales the new service to 0, and
hands `30-ingress` the stack's `TtsEcsService` / `VoicevoxUrl` outputs. On an existing
deployment the same steps by hand are: deploy `20-platform` (it creates the repository),
`crane copy` the image, deploy `50-tts`, `ecs update-service --desired-count 0`, then
re-deploy `30-ingress` with the two parameters (that last step replaces the CP task).

⚠️ **The image has to be in ECR before `50-tts` is created, not after.** CloudFormation
blocks on ECS service stabilisation, so a service created against an empty repository
leaves the stack in `CREATE_IN_PROGRESS` repeating `CannotPullContainerError` and no later
step can rescue it — the deploy never returns. That is why the repository is a
`20-platform` resource even though everything else about it belongs here (measured on this
template's first stand-up). Creating the stack therefore always costs one cold start: CFN
waits for the first task to run, and only then is the service scaled back to 0.

Things worth knowing before you enable it:

- **The placement is spelled out in the template and must stay spelled out.** Normally
  `LaunchType: FARGATE`; with `UseSpot=on` an explicit `CapacityProviderStrategy` instead,
  because the two are mutually exclusive. Saying NEITHER is what breaks it: the API default
  is EC2, which puts a 2 vCPU engine inside the workspace slot pool, breaks the sweeper's
  "an instance with zero ECS tasks is idle" premise, and takes a seat somebody's Workspace
  needed. (Measured 2026-09-07: an explicit `FARGATE_SPOT` strategy with no launch type
  places on Spot, so it is as binding as the launch type.)
- **`UseSpot=on` is a knob, not the default.** Fargate Spot is around 70 % cheaper and an
  interruption costs nothing but a fallback to Polly — the engine holds no state, and the
  controller already treats a task that disappears as a replacement rather than a failed
  start (`tts.engine.replaced`). It is off by default because idle-stop has already made
  the bill small, and a capacity shortfall shows up as an engine that never arrives.
  ⚠️ Switching it changes the service's placement, which CloudFormation applies by
  **replacing the service** — and a newly created service starts at desired 1, so expect a
  cold start and scale it back to 0.
- **`DesiredCount` is deliberately absent from the template.** CloudFormation starts a new
  service at 1 and then leaves the count out of every later update, which is what lets the
  admin toggle own it. Declaring `0` instead would reset the count on every stack update —
  i.e. stop the engine in the middle of somebody listening.
- **The engine is addressed by a Cloud Map DNS name, not Service Connect.** While it is at
  desired 0 the name simply has no A record, `Ready` is false and Polly reads.
- **Every start pays the full image pull** (about 2 GB; Fargate keeps no image cache).
  Measured on this stack: **70–77 s from desired 1 to a running engine**, of which 34 s is
  the pull, plus another 67–73 s before the Cloud Map A record appears. Polly reads
  throughout.
- The engine holds no per-tenant state — the reading dictionaries are applied client-side —
  so destroying it loses nothing.
- **One huge synthesis request kills the engine, and the CP now refuses one.** Measured
  2026-09-07: a **single 684-character request OOM-kills it at 4 GiB** (exit 137), and the
  `/version` health check stays HEALTHY through it; ECS then replaces the task and Polly
  reads for ~2.5 minutes, for everybody. The CP caps one request at
  **`AF_TTS_MAX_CHARS` characters (default 300, `0` = no cap)** and answers `413
  tts_text_too_long` above it — far above what the Console can send (it splits at 60) and
  just past what `ttsHTTP`'s 30 s timeout allows anyway (a 228-character sentence takes
  26 s). This is a guard rail for scripts, not a product limit.
- **Concurrency queues, it does not scale.** Measured at 2 vCPU / 4 GiB: four concurrent
  328-character requests came back at 40 s, 80 s, 119 s and 159 s — the engine serialises
  them, so a second listener does not halve anyone's latency, it doubles the tail.

Only `30-ingress`'s two parameters are needed on the CP side; `AF_TTS_ECS_CLUSTER` and
`AF_TTS_ECS_REGION` ride on the existing `AF_ECS_*`, and `CpTaskRole` already carries the
`ecs:DescribeServices` / `ecs:UpdateService` this needs. See
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#speech-the-voicevox-engine).

**Who moves the desired count** (ADR 0070 P1). The admin panel's toggle now stores one of
three modes — `off`, `on`, `ondemand` — and on a managed engine an unset value means
`ondemand`. In that mode the CP starts the engine once read-aloud demand builds up and stops
it once nobody is listening, and **every automatic start and stop is in the audit log**
(`tts.engine.auto`, actor `tts-controller`). Nothing needs configuring for the defaults; each
is overridable as a CP environment variable, and none of them are set by these templates:

| Variable | Default | What it does |
|---|---|---|
| `AF_TTS_ECS_CONTROL_INTERVAL_SEC` | 30 | How often the controller looks. **`0` switches the controller off entirely** — the mode is then only what routing reads, and nothing moves the desired count but the toggle. |
| `AF_TTS_ECS_START_CHARS` | 2000 | Characters of read-aloud demand inside the window that buy a start (about one agent answer). |
| `AF_TTS_ECS_WINDOW_SEC` | 300 | The rolling window those characters are counted in. |
| `AF_TTS_ECS_IDLE_SEC` | 1800 | Stop after this long with nobody listening; **`0` = never stop**. Clamped to no less than the start deadline. |
| `AF_TTS_ECS_START_DEADLINE_SEC` | 300 | A start that has not become `running` by then is treated as failed, returned to 0, and reported with the reason ECS gives. |
| `AF_TTS_ECS_FAIL_COOLDOWN_SEC` | 900 | How long a failed start blocks the next one, doubling per consecutive failure (capped at 16×). Without it a repeating failure pays a 2 GB pull per attempt. |
| `AF_TTS_MAX_CHARS` | 300 | Ceiling on ONE synthesis request, in characters; `0` = no cap. Not a controller knob — it protects the engine from the single oversized request that OOM-kills it (see above). |
| `AF_TTS_ECS_OFF_GRACE_SEC` | 60 | How long an explicit "disable" waits before the desired count moves. Routing switches to Polly immediately; this only debounces the ECS write, so turning it off and straight back on costs no cold start. |

## Optional: the fleet's own inference engines (`cfn/60-engines.yaml`)

🚧 **New with ADR 0071 (P0: `llm`; P1: `image`).** This stack runs **inference on a GPU that
only exists while somebody is using it**. It has two independent roles:

| Role | Engine | What a member gets | Cold start |
|---|---|---|---|
| `llm` | llama.cpp (`llama-server`) | `llamacpp/<model>` in opencode's launch picker | ~527–586 s |
| `image` | ComfyUI (`comfy`) | the `generate_image` tool, provider `comfy` — generate, edit and inpaint | the instance dominates (8–88 s) |

Each is optional on its own (without `<Role>Enabled=true` that role's service is not
created at all), and **the two never share an instance**: CUDA does not slow down when VRAM runs
out, it crashes, and the two measured footprints — 20.9 GB for the 30B, 7.4 GB for SDXL — do
not both fit on one L4. Everything here is opt-in, because unlike the speech engine it is not
cheap when it is up: **g6.xlarge (NVIDIA L4, 24 GB) is $1.26/hour all in**, or $918/month if
it never stops. ADR 0071 has the pricing, the alternatives that were rejected and every
measurement; this section is what to type.

**Why an instance and not Fargate.** Fargate has no GPU and never has (AWS Fargate FAQ;
containers-roadmap #88, open since 2019), so the engine runs on an EC2 box. Until ADR 0077 the
box was bought by ECS Managed Instances; since then **the Control Plane buys it itself** with
`CreateFleet(type=instant)` against a launch template this stack owns, and terminates it when
the engine goes idle. The property ADR 0070's design rests on — no task, no instance — is kept
by the Control Plane rather than delegated (ADR 0077 decisions 1 and 5).

### Before you start

1. **G-family vCPU quota.** A new account has **zero**, and the increase is not
   auto-approved: it opens a support case that takes hours to days. Ask for **16**
   (`L-DB2E81BA`, "Running On-Demand G and VT instances") if you run both roles — 8 builds two
   g6.xlarge, but an instance that has just been terminated keeps its 4 vCPU counted for the
   7–8 minutes it spends shutting down, so 16 is one drain's worth of headroom. The quota is a ceiling and
   not a reservation — nothing is billed for headroom — so asking for 24 costs the same and
   covers a redeploy overlapping a drain. Check with:
   ```bash
   aws service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA --query 'Quota.Value'
   ```
2. **Only one stack may own the cluster's capacity-provider associations.** The API replaces
   the list rather than adding to it, so if the measurement harness
   (`harness/engprobe.yaml`) is deployed, take it down first —
   `deploy/aws/ecs/harness/probe-managed-instances.sh down`. 60-engines still owns that list
   since ADR 0077, even though the engines are no longer in it: somebody has to hold `FARGATE`
   and `FARGATE_SPOT` for the rest of the deployment.
   **And `AWSServiceRoleForEC2Fleet` has to exist** — see the prerequisites above.
3. **The images have to be in ECR before the stack is created**, for exactly the reason
   `50-tts` documents above (CloudFormation blocks on service stabilisation and no later step
   can rescue an empty repository). `standup.sh` does it in the images step; by hand it is
   `crane copy ghcr.io/ggml-org/llama.cpp:server-cuda <acct>.dkr.ecr.<region>.amazonaws.com/af-llamacpp:server-cuda`
   and, for the image role,
   `crane copy ghcr.io/k-k1/agent-fleet/comfyui:v0.37.0 <acct>.dkr.ecr.<region>.amazonaws.com/af-comfyui:v0.37.0`.
   It is also worth doing for a measured reason: **GHCR through this NAT runs at 12–14 MB/s**,
   which put 178 seconds of a 527-second cold start into the pull alone. (`standup.sh` copies
   the image-role image only when `ImageEnabled=true` — an LLM-only deployment does not pay for
   an image it will never start.)
   🔴 `server-cuda` is a floating upstream tag, and llama.cpp's own build decides how tool calls
   parse (`PARAMETERS-60-engines.md` "LlmImageTag" has the measurement). `standup.sh --llm-digest
   sha256:<...>` pins the exact content that copy fetches without touching `LlmImageTag`; leave
   it unset and the copy is exactly what it always was.
   `standup.sh --comfy-digest sha256:<...>` does the same for the ComfyUI copy, whose tag a
   re-dispatch of `comfyui-image.yml` can overwrite, and `update.sh --comfy-digest` for the copy
   it makes when it moves a stack off an earlier `ImageComfyImageTag` default.

### Turning it on

`standup.sh` builds `60-engines` when, and only when, a `params/60-engines` file exists in
the captured state — the same shape as the pool and speech layers:

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/60-engines
# Every parameter has a default; standup.sh overrides NetworkStackName /
# PlatformStackName / ServiceConnectNamespace from what it already knows.
ServiceConnectNamespace=af.internal
# Whether each role exists at all: its service, its DNS name, its row in the engine table.
# This is the WHOLE switch since ADR 0072 P6 - nothing here says what the engine loads.
LlmEnabled=true
# What the box IS - the ladder of GPU rungs, of which the first is the default (ADR 0074), or
# the same list with a `buy` column (ADR 0075). Since ADR 0077 this is the ONLY place the box's
# shape is declared: the template has no instance-requirement parameters left, and a deployment
# that declares neither has no way to start an engine at all. More than one type per rung, or a
# shortfall in one AZ is the whole of "the engine never arrives"; keep the intended instance
# cheapest. PARAMETERS-60-engines.md, "The instance classes" and "The offers", have the argument.
LlmInstanceClasses=l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.17
# The box's root volume (GiB). The models land on it, so it has to hold every model the role
# pulls from S3 plus the images.
LlmStorageGiB=120
# The image role, if you want it. Leave this out and no image service exists.
ImageEnabled=true
```

`standup.sh` then copies the images into ECR, generates the llm engine's own API key into SSM
(`/af-ws/engine-llm-key`, a SecureString) if it is not there, deploys `60-engines` after the
speech engine and before `30-ingress`, scales each **newly created** service to 0, and hands
`30-ingress` the stack's `EnginesSsmParam` output. The image role has no key of its own:
ComfyUI has no authentication option at all, so its security group — 8080 from the CP and
nothing else — is the whole of its access control.

**The first stand-up is ONE pass, and it stands up empty.** It used to be two: the bucket and
the service that serves out of it are created by the same stack, so on the very first deploy
there was nothing to load and CloudFormation blocked on ECS service stabilisation — measured on
this stack's first real create, the fetch sidecar answered `Key … does not exist` and the task
crash-looped every 60 seconds while the stack sat in `CREATE_IN_PROGRESS`, with no later step
able to rescue it. Since ADR 0072 the sidecar treats "nothing staged" as success and the engine
idles, so `LlmEnabled=true` on an empty bucket gives a role that comes up, is scaled to 0, and
waits for its first model. Adding a role later is the same one line.

🔴 **Upgrading a deployment that predates ADR 0072 P6: add `<Role>Enabled=true` before you
update.** `LlmModelS3Key` / `ImageModelS3Key` used to decide whether the role's service existed,
and they are gone. `standup.sh` translates a captured key into the switch; `update.sh` and a
hand-run `cloudformation deploy` pass no parameters at all, so without that line the update
**deletes the running engine service** — silently, as an ordinary stack update.
`cfn/PARAMETERS-60-engines.md`, "Upgrading: the model parameters are gone".

### Getting a model into the catalogue

Two steps since ADR 0072, and they are separate on purpose: **staging** puts the bytes in S3,
**enabling** offers them. "The file is in the bucket" and "members may use it" are different
facts, and deriving the second from the first is what ADR 0053 forbids.

1. stage the file — **Settings → Admin → engines → "take one in from Hugging Face"** does it
   for you since ADR 0072 P4 (search for a model, or paste `owner/name` or a model-page URL; the
   control plane reads the sha256, the size and the licence from that source's API and starts the
   ingest task). The search (ADR 0072 decision 11) is filtered to what the engine can load —
   GGUF repositories for `llm`, text-to-image for `image` — and each hit says up front whether
   it is gated, what licence it carries and how many downloads, likes and trending points it
   has. With the instance empty it is a **ranking** (downloads / trending / likes) instead, which is
   the way in when no name is in hand. The
   `run-task` below is the same thing by hand, for a deployment whose control plane cannot reach
   the internet;
2. switch the row on in the same panel. It arrives DISABLED whichever way it was staged —
   "the file is in the bucket" and "members may use it" are different facts.

**Where the file goes** is ComfyUI's layout (ADR 0071 decision 6, ADR 0072 decision 2), so one
tree serves all three engines: `llm/<name>.gguf`, `llm/loras/`, `image/checkpoints/`,
`image/loras/`, `image/vae/`, `image/text_encoders/`, `image/diffusion_models/`. The instance mirrors
the bucket — `image/checkpoints/x.safetensors` becomes `/models/image/checkpoints/x.safetensors`
— so the key IS the path and there is no second name to keep in step.

⚠️ **A deployment staged before ADR 0072 has `image/sd_xl_base_1.0.safetensors` at the top
level.** Move it with a server-side copy (no NAT, no re-download) and correct the catalogue row's
key in the admin panel in the SAME change — the row points at the key verbatim, and a row whose
file is not there fails in the fetch sidecar at the next cold start, not at deploy time:

```bash
aws s3 cp "s3://$BUCKET/image/sd_xl_base_1.0.safetensors" \
          "s3://$BUCKET/image/checkpoints/sd_xl_base_1.0.safetensors"
aws s3 rm "s3://$BUCKET/image/sd_xl_base_1.0.safetensors"
```

The engine never talks to Hugging Face. Models are staged into the stack's S3 bucket once,
by a Fargate task, and the engine pulls from S3 at start:

```bash
# The sha256 is siblings[].lfs.sha256 from https://huggingface.co/api/models/<repo>?blobs=true
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition af-af-ecs-engines-ingest \
  --network-configuration 'awsvpcConfiguration={subnets=[subnet-…],securityGroups=[sg-…],assignPublicIp=DISABLED}' \
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF/resolve/main/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"}]}]}'
```

The image role is the same task with a different URL and key — measured end to end at
**161 s to fetch SDXL's 6.94 GB, sha256 verified, then 46 s to S3**:

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors"},
        {"name":"SHA256","value":"31e35c80fc4829d14f90153f4c74cd59c90b779f6afe05a74cd6120b893f7e5b"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/sd_xl_base_1.0.safetensors"}]}]}'
```

A **second** checkpoint is the same call again with another URL and key. Pick it by ADR 0072
decision 10 — Apache-2.0 or OpenRAIL, not gated, and it has to fit an L4 without quantising:

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/<owner>/<repo>/resolve/main/<file>.safetensors"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/<file>.safetensors"}]}]}'
```

The image role's engine is ComfyUI, so a split model (FLUX.2 klein, Z-Image — a
`--diffusion-model` + text encoders + VAE rather than one SDXL-shaped checkpoint) is fine too:
ingest it into `image/diffusion_models/`, `image/text_encoders/`, `image/vae/` respectively,
matching the layout above. This is `comfy`'s job specifically (ADR 0072 phase P2, measured —
"measurements that settled a question", 2026-09-08); the earlier `sdcpp` engine could only ever
take a single-file checkpoint, and ADR 0083 retired it.

⚠️ **The container overrides above take ONE string per command**, because the containers'
`EntryPoint` is already `["sh","-c"]`. Passing `["sh","-c", "<script>"]` becomes
`sh -c sh -c <script>`, which does nothing and exits 0 — it reads as a successful ingest that
fetched nothing (measured).

⚠️ **Budget an unpredictable amount of time for that fetch, and give the checksum.** Measured
over four pulls of two files through the same NAT, Hugging Face delivered between **4 and
236 MB/s**, and nothing about the file, the client or the path predicted which — the same
SDXL checkpoint came at 236 MB/s to one instance and 39.6 MB/s to another. That unpredictability
is the whole reason models are staged in S3, which was **104–147 MB/s on every attempt**. The
sha256 matters for the same reason: a truncated 17 GB GGUF loads and then answers nonsense,
and "the file got bigger" is what a truncated download looks like too.

A gated repository (`gated: auto` on the HF API — FLUX.1-dev, SD 3.5) additionally needs the
operator to have accepted the licence on Hugging Face and that account's token registered in
**Settings → Admin → engines → "Hugging Face token"** (ADR 0072 phase P5 — no CloudFormation
round trip: the control plane keeps the token sealed in its own database and writes it into a
secret this stack always creates). The token is read by the **ingest task only**; it never
reaches an engine instance, and the control plane cannot read back what it wrote
(`PutSecretValue` without `GetSecretValue`).

### What a member sees

**The `llm` role:** nothing to configure. The Agent asks the CP which engines exist, writes the
chat ones into opencode's global config as a provider, and `llamacpp/<model>` appears in the
launch picker — **while the GPU instance is still asleep**, which is why the ids come from the
catalogue and never from the engine. Picking one and sending the first message starts the instance
and holds the request until it answers; the answer arrives on the **first attempt**, with no
retry, which is the observation that verifies ADR 0071 decision 5.

Since ADR 0072 P1 `llama-server` runs as a **router**: every enabled model appears in the
picker with **its own context window**, and the request's `model` chooses between them. Two
things follow, and both are the administrator's to weigh. Every enabled GGUF is synced onto the
instance, so the cold start grows with their total size (the panel estimates it per model), and
`LlmModelsMax` (default **1**) caps how many are held at once — under that cap a request for the
other model waits for the current answer to finish and then pays a full reload. The panel names
the model in VRAM and counts the changes.

**The `image` role:** it becomes a provider of the existing `generate_image` tool (ADR 0069),
which is **off by default and turned on per user** in Settings → Agents ("画像生成"). Once the
engine exists it is what `generate_image` uses by default, ahead of the Codex route, because
this one spends the fleet's own hardware instead of the member's ChatGPT plan quota — and it
can `edit` and `inpaint`, which the Codex route cannot. Nothing appears in the launch picker:
an image engine is not something to hold a conversation with. A call to a stopped engine takes
several minutes and the tool emits a progress notification every 10 seconds while it waits,
which is what keeps opencode's 60-second per-call ceiling from cutting it off.

### Things worth knowing

- **`DesiredCount` is absent from the template, deliberately**, for the same reason as
  `50-tts`: CloudFormation would otherwise reset it on every stack update, i.e. kill a GPU instance
  in the middle of an answer.
- **The default capacity-provider strategy is empty and must stay empty.** With one in place,
  any service that does not spell out `LaunchType: FARGATE` lands on the GPU instance.
- **A stopped engine keeps billing for several minutes.** Measured drain after `desired 0`:
  **427 and 463 seconds** on a GPU instance (93 s on a CPU one). Shortening the idle window only
  ever recovers window time, never that.
- **The cold start is minutes, not seconds.** Measured from S3, image already in ECR:
  **527 s** to a listening engine — 178 s of image pull and 179 s of S3 fetch running
  concurrently, then 267 s loading 18.5 GB into VRAM. The CP holds the request open across
  all of it (see below), so a person sees a slow first answer rather than an error.
- **The image engine's idle window is half the llm role's (900 s)**: an image is a request with
  no conversation around it. (The retired stable-diffusion.cpp engine measured 195 s from task
  creation to listening, 512px in 7.8 s and 1024px in 20.8–21.0 s on an L4, 7.4 GB of VRAM — the
  numbers this window was chosen against. `comfy`'s own are not re-measured here, ADR 0083
  unresolved question 1.)
- **ComfyUI's `/system_stats`, not `/health`.** It answers unauthenticated and immediately
  (measured), which is a liveness check rather than a checkpoint-loaded one — `comfy` has no
  single loaded checkpoint to report on, it builds a per-request workflow graph and picks the
  checkpoint the graph names.
- **Everything the controller does automatically is in the audit log** (`engine.llm.auto` /
  `engine.image.auto`, actor `engine-<role>-controller`), like the speech engine's.

The CP side takes exactly **one** parameter, `EnginesSsmParam`: 30-ingress has around 9 KB of
template budget left and six knobs per engine would eat a third of it, so the engine table
lives in SSM under `/af-ws/` — which is already the whole of `CpTaskRole`'s SSM read scope, so
no permission is added. Everything else is a CP environment variable, none of them set by
these templates:

| Variable | Default | What it does |
|---|---|---|
| `AF_ENGINE_WAKE_TIMEOUT` | 900 | How long one **streaming** request may be held open while the engine comes up. Above the measured 527 s cold start with room for a slow pull. |
| `AF_ENGINE_PLAIN_HOLD` | 45 | The same, for a **non-streaming** request, which has no heartbeat and so cannot outlast whatever proxy is in front. Must stay below the front end's idle timeout — 30-ingress sets the ALB's to 60 s. Past it the answer is `503 engine_waking` + `Retry-After`, which the caller retries. Never raises the wait above `AF_ENGINE_WAKE_TIMEOUT`. |
| `AF_ENGINE_LLM_CONTROL_INTERVAL_SEC` | 30 | How often the controller looks. **`0` switches it off entirely.** |
| `AF_ENGINE_LLM_IDLE_SEC` | the stack's `LlmIdleSec` (1800) | Stop after this long with nobody asking; **`0` = never stop**. Clamped to no less than the start deadline. |
| `AF_ENGINE_LLM_START_DEADLINE_SEC` | the stack's `LlmStartDeadlineSec` (900) | A start that has not become `running` by then is treated as failed. ⚠️ **Must exceed the real cold start.** ADR 0070's 300 s default would record every GPU start as a failure and double the cooldown away. |
| `AF_ENGINE_LLM_FAIL_COOLDOWN_SEC` | 900 | How long a failed start blocks the next one, doubling per consecutive failure (capped at 16×). |
| `AF_ENGINE_LLM_WINDOW_SEC` | 300 | The rolling window demand is counted in. One request is one unit of demand — for inference the request *is* the intent, unlike speech where Polly stands in while the engine starts. |

Every `AF_ENGINE_LLM_*` variable above has an `AF_ENGINE_IMAGE_*` twin with the same meaning,
taking its default from the image role's own stack parameters (`ImageIdleSec` 900,
`ImageStartDeadlineSec` 900). The name in the middle is the engine's key, so a third role
would follow the same pattern. `AF_ENGINE_WAKE_TIMEOUT` is shared by all of them.

### Holding the first request (why there is no retry)

A cold engine takes minutes and opencode gives up at 300 seconds — but **not on elapsed
time**. Measured three ways on opencode 1.18.29 (the release pinned then; the current 2.x has not been re-measured): holding the connection with nothing
sent is cut at 300.1 s; sending the 200 and the headers and then nothing is cut at 306.9 s;
sending the headers and then one SSE **comment** line every 10 seconds is not cut at all, and
a 400-second wait produced the answer with no resend. So the gateway answers a streaming
request immediately with `200 text/event-stream` and heartbeats until the engine's first byte
arrives. **300 seconds is the maximum silence, not the maximum wait.** `503` + `Retry-After`
is kept for non-streaming requests and genuine start failures only: a 503 spends one of the
client's finite retries, and how many it has is not something this side knows.

⚠️ **A non-streaming request cannot be held for 900 seconds, and it is not the CP that decides
that.** Measured on a live deployment 2026-09-07: the first `generate_image` against a stopped
image engine came back as `POST /engine/image/v1/images/generations 503 59.998s`, against the
CP's own 900-second bound — the ALB's `idle_timeout.timeout_seconds` (60 in 30-ingress) closes
a connection that has carried no response byte for a minute, and the engine needed 165 s. The
streaming path never noticed because its heartbeat is a byte every 10 s, which is exactly what
an idle timer watches for. So the non-streaming hold is folded below the front end
(`AF_ENGINE_PLAIN_HOLD`, 45 s) and answers `503 engine_waking` + `Retry-After`; the Workspace's
`comfy` provider keeps asking inside its own 16-minute budget, so one tool call still returns
one picture. **Behind a stricter proxy than this repository's ALB, lower `AF_ENGINE_PLAIN_HOLD`
to match** — the CP has no way to ask what that timeout is.

## Optional: EC2 slot pool (`WsRuntime=ecs-ec2`)

🚧 **Stood up and run in a sandbox, including a production-shaped VPC — but never with
real users.** `40-ec2-pool` has been deployed as a stack and real workspaces have run on it
end to end, first in a public subnet (docs/log/64 §64.16, §64.17), then in a **private subnet
behind a NAT gateway** (§64.19), and then with **four slots across two AZs, three users
starting at once, and the sweep loop left running for eighteen minutes** (§64.20). The
task-ENI trap below does not reproduce behind a NAT — there is no public IPv4 to lose.
Fargate (`WsRuntime=ecs`) stays the default and is untouched by this profile.

⚠️ **Multiple AZs: three things to know.** An EBS home cannot leave its AZ, so the CP
starts a slot in the AZ that user's home is already in.

1. **Every AZ you list in `AF_ECS_SUBNETS` needs its own EFS mount target**, or a task
   landing there cannot mount the credentials filesystem and never comes up.
2. **New homes are spread across the AZs you list**, fewest-homes-first, so losing one AZ
   does not take out everybody (ADR 0045 decision 16). It costs slots: a home in one AZ can only
   use a slot in that AZ, so free slots elsewhere are no use to it and the pool grows
   instead of reusing. Reusing a free slot still wins over balancing. If an AZ runs out of
   the slot type, a **new** home falls back to another; an **existing** one fails, because
   it cannot move (decision 15).
3. **To move someone to another AZ there is no "move".** Hibernate their home and start it
   again with free capacity only where you want them — the snapshot has no AZ. docs/log/64
   §64.20.7 has the runbook, including the AWS CLI form for moving one person to a
   named AZ.

**Losing an AZ is a different question from moving.** A home cannot be evacuated, so the
answer has to be in place before the bad day: turn on **home backups** (below). docs/log/64
§64.21 walks through what the adapter actually does during an AZ outage, what the operator
should do first (drop the failed subnet from `AF_ECS_SUBNETS` and restart the CP), and how
to rebuild a home from a backup afterwards.

**What you get, and what you do NOT.** Measured through the adapter on real AWS, a warm
Start is 43–110s against Fargate's ~105s — **the start latency is not the reason to switch**
(the earlier 22–27s figure was measured without Service Connect, which this product needs).
What the pool actually buys is **I/O and persistence** (small-file writes 8–30× faster than
EFS, and a home that really survives) and **sizes above Fargate's 16 vCPU / 120 GiB /
200 GiB ephemeral ceiling**.

**What changes.** A workspace stops being "a Fargate task with an EFS home" and becomes
"a task on a general-purpose EC2 *slot*, with the user's own EBS volume attached to it".
Slots are not owned by anyone: on Start the CP picks a free one, attaches that user's
volume at `/dev/sdf`, mounts it over SSM and pins the task there with an
`ec2InstanceId ==` placement constraint; on Stop it unmounts, detaches and hands the slot
back. **One slot serves one user at a time** (`ADR 0045` decision 8).

| | Fargate (`ecs`) | EC2 pool (`ecs-ec2`) |
|---|---|---|
| Warm Start | ~105s | **84–110s** — *not* an improvement worth switching for (docs/log/64 §64.17.5, §64.19.2) |
| Home | EFS — small files are 8–30× slower | **EBS gp3** — 2,000 small files in 0.04s vs 30.7s |
| Size | 74 discrete (cpu, memory) pairs, ≤16 vCPU / 120 GiB | instance types; the task reserves nothing and gets the instance |
| Resources per workspace | 2 (service + EFS access points) | 6 (also instance, volume, container-instance registration, task def) |
| Idle cost | EFS (what you use) | EBS (what you **provision**) + any hot slots |

**Stand-up**

```bash
aws cloudformation deploy --stack-name af-ecs-ec2-pool \
  --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
# then point the CP at it (this is the whole switch, and the whole rollback):
aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
```

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` switches the adapter. Rolling back is this value |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | `SlotLaunchTemplateId` output of `40-ec2-pool`. The CP refuses to boot without it on this profile |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | `m7i.large:8192:2,…` | `instanceType:memoryMiB[:vcpu]`, ascending — or several named **classes**, see below. The vCPU field is optional and display-only (the Console shows which instance a memory number lands on) |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | `""` | `SlotAmiIdArm64` output of `40-ec2-pool`. Required only when a class declares `arm64` — the CP refuses to boot otherwise. Initial/fallback only: the CP follows Amazon's SSM parameter itself (#3318) |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | `""` (the first class) | Where a member with no per-user and no per-tenant choice lands |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Hard cap **across all classes**. Start fails at the cap rather than growing the bill |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | How long an instance may sit with no task before it is **stopped**. Ends the compute charge; the root volume keeps billing. `0` = never |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0` (off) | The next step on the same clock: past it the instance is **terminated** and its root volume goes too. `0` means instances are kept forever, so retained roots grow to `Ec2MaxSlots` — see below. `14400` (4h) recommended |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | Per-user home volume (gp3) |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | **Default** for how long a home may sit unopened before it is snapshotted and its volume deleted. A tenant overrides it from the Console. Nothing is destroyed — the snapshot completes before the volume goes, and the next start restores it. See below |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | How much of a slot is held back from the workspace so the instance's own daemons cannot be starved by it. `auto` = a flat 2 GiB at every rung (an 8 GiB slot's workspace gets 6 GiB; a fifth of the rung left too little and a box thrashed). `off` = uncapped (what every deployment did before 0.12.5, and what melted one). A number = that many MiB; `0` reserves nothing and caps the workspace at the full rung (not the same as `off`, and not auto). See below |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | How long a slot may be EC2-`running` while the cluster cannot reach its agent before the CP gives up on it and rebuilds the workspace elsewhere. `0` = wait as long as the caller allows |
| — | `AF_ECS_EC2_DARK_SLOT_ACTION` | `detect` | What the sweeper does about a slot that holds a home while its ECS and SSM agents are both silent (ADR 0121). `detect` tags the instance `af-dark-since` and writes one `pool.slot_dark_detected` audit row per episode; `off` disables the scan. `rescue` / `quarantine` are not implemented yet and run as `detect` |
| — | `AF_ECS_EC2_DARK_AFTER_SEC` | `600` | How long the slot must stay dark before it is reported. Raised to at least the boot grace and `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` + `AF_ECS_EC2_SWEEP_SEC`. `0` = off |
| — | `AF_ECS_EC2_DARK_BOOT_GRACE_SEC` | `300` | A slot launched or started more recently than this is never called dark (boot and wake look dark for a while) |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1` (on) | Keep the golden snapshot in step with the workspace image without anyone re-baking by hand (ADR 0045 decision 9-1). Set `0` and it becomes your job on every release |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | How often the baker looks. It advances one step per look, so this is also how fast a bake progresses |

**What a workspace actually gets (`Ec2HostReserveMb`).** A slot's memory is not all the
workspace's: dockerd, containerd, the ECS agent, SSM and the EFS stunnel live on the same
instance, and the rung overstates what the machine has (an "8192 MiB" m7i.large reports 7784).
So the container is capped at the rung less a reserve, and the Console prints the number
the workspace can actually spend with the instance beside it — `6 GiB (of 8 GiB)`.

⚠️ **Turning it off is not free.** Uncapped, one workspace taking several GB of anonymous
memory can push the instance into refault thrash: with no swap the kernel throws away page
cache, every daemon spends its time re-reading its own executable off disk, and the slot
stops answering the cluster entirely while still looking healthy to EC2. That happened on
a live deployment (docs/log/64 §64.40) — nobody could start that workspace again for hours.

⚠️ **Changing the reserve replaces running tasks.** It is part of the task definition, so
the first start after the change registers a new revision — the same as an image change.

**Alarms (`PoolAlarmEmail` on `40-ec2-pool`).** Off by default. When set, the stack creates
an SNS topic and two CloudWatch alarms on the pool's I/O — a deep EBS queue sustained for
15 minutes, and an instance that has spent a fifth of its EBS byte credits. Both were
unmistakable during that incident and nobody was looking. ⚠️ **They cannot be scoped to
this stack**: the metrics are dimensioned by volume and instance id, the pool creates those
at runtime, and Metrics Insights filters on dimension values rather than tags — so the
queries are account-wide within the region. Turn them on where the account is the
deployment's own.

**Offering a choice of machine (docs/log/70).** `Ec2SlotTypes` takes several named ladders,
`id|label|arch|<ladder>` separated by `;`, and a tenant administrator then picks one per
tenant and per member. Memory still picks the rung *within* the class, so "8 GB" keeps
meaning the same thing. **The shipped default already declares two**, because "cost over
speed" is a legitimate choice and one version of it costs nothing to offer:

```
standard|Standard (Intel)|x86_64|m7i.large:8192:2,…;saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,…
```

Measured in ap-northeast-1 on 2026-08-22 — the price from the Pricing API, the speed
from this repository's own build on each family (`harness/bench-instance-classes.sh`,
docs/log/70 §70.3):

| against m7i | $/hour | build time | **cost per build** |
|---|---|---|---|
| m6i | −4.8% | +13% | +7.3% |
| **m8g** (Graviton4) | **−11.0%** | **−29%** | **−37.1%** |
| m7g (Graviton3) | −19.0% | −10% | −27.4% |
| m6g (Graviton2) | **−24.0%** | **+32%** | ±0 |

⚠️ **The cheapest instance per hour is not the cheapest instance.** m6g bills 24% less per hour
and takes 32% longer, which nets out to no saving at all on work that keeps the CPU
busy — and m8g is *both* cheaper and faster than m7i, so there is no reason to run m7g.

⚠️ **Which column matters depends on the member.** A slot bills for the wall-clock time
it is RUNNING, and a workspace spends most of that idle (reading, thinking, waiting on a
model), so a mostly-reading member really does pay 24% less on m6g. A member who builds
all day pays in time instead. That is the whole reason this is a per-member setting —
and the reason a class's LABEL should carry the trade-off ("Cheapest — older, slower")
rather than just the discount.

**m6i is the one that needs nothing.** Same architecture as m7i, so: the same workspace
image, the same AMI, the same golden snapshot, and a member's home keeps working when they
move between the two. −4.8% for no prerequisites and no migration. Graviton is where the
real money is, and it is also where every prerequisite below lives.

⚠️ Adding a class moves nobody. `Ec2DefaultSlotClass` decides where members with no choice
of their own land, and it defaults to the FIRST class. A running stack also keeps its own
`Ec2SlotTypes` on a redeploy unless you pass the parameter, so upgrading the CP does not
put a picker in front of anybody.

Before declaring an `arm64` class, in this order:

1. **The workspace image needs an arm64 manifest.** Without it the slot launches fine and
   the task cannot pull anything that runs on it. (`crane manifest <image>` — a single
   `manifest.v2+json` is one architecture; a manifest list / OCI index is what you want.)
2. **Set `Ec2SlotAmiArm64`.** A launch template pins one AMI and Graviton cannot boot the
   x86_64 ECS-optimized one; the CP passes this as a `RunInstances` ImageId override.
   `aws ssm get-parameter --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id --query Parameter.Value --output text`.
   This value is only the initial and fallback AMI: the CP reads the same SSM parameter itself and launches every slot with it, so patching needs no deploy (#3318).
3. **Expect a second golden bake.** A golden is a home full of binaries, so there is one
   per architecture; until the arm64 one is baked, new members on an arm64 class start
   from an empty home (slower, not broken). The pool screen lists them per architecture.

Adding a class moves nobody by itself. Moving a member *across architectures* is safe but
not free: their home is kept and its architecture-dependent contents (`~/.local` CLIs, nvm
node, Chromium) are reinstalled on the next start, and anything they built under `~/repos`
(`node_modules`, `target`, `.venv`) has to be reinstalled by them. The Console says so
before saving.

**Hibernating long-unused homes (opt-in).** An EBS home bills for what it is *provisioned*
at, whether or not anyone opens it. With this enabled, a home that nobody has opened for
that long is captured as a snapshot and its volume deleted; the owner's next Start rebuilds
it from the snapshot. For a 20 GiB-used / 50 GiB-provisioned home that is
**$4.80 → $1.00 a month**, against ~122s on the return and a slightly slower first day
(ADR 0045 decision 4).

- **It hibernates; it never destroys.** This is the only automatic path in the product that
  moves someone's home, so it stays reversible — and off by default.
- It is a **third** timer after the two above: the person goes idle → the workspace stops →
  the slot sleeps → (days later) the home becomes a snapshot. Set it in days, not minutes.
- **Per tenant, with this env as the deployment default** (ADR 0045 decision 14).
  Settings → Admin → a tenant → *Hibernate unused homes* takes a duration string
  (`720h`); empty follows this env and `0` means never for that tenant. The trigger lives
  in the idle-stop reaper, so **`AF_IDLE_SWEEP_INTERVAL=0` disables hibernation too**.
- **Two ways to turn it on, and the tenant one needs no deploy.** A retention window is
  usually the tenant's answer, so setting it in the Console is both quicker and more
  correct. Use `Ec2HibernateAfterSec` when you want a floor for tenants that never choose.
  ⚠️ Until that parameter existed the deployment default could not be set on ECS at all —
  there was no CFN parameter, and a hand-edited task definition is overwritten by the next
  deploy — so a deployment reading "default 0 (off)" was reading the *only* value it had.
- A snapshot of a 45 GiB home takes 30–40 minutes; the sweeper advances one step per pass
  and the state lives entirely in AWS tags, so a CP restart mid-way resumes rather than
  strands. If the owner comes back first, the hibernation is abandoned and the volume is
  simply reattached.

**Spare copies of each home (opt-in).** An EBS home lives in exactly ONE Availability
Zone and cannot be evacuated, so losing that zone loses the home with it. Backups are the
only copy that is not in the zone — snapshots are regional.

| Env | Default | Notes |
|---|---|---|
| `AF_ECS_EC2_BACKUP_EVERY_SEC` | `0` (off) | **Default** interval; a tenant overrides it in Settings → Admin → the tenant → *Keep a spare copy of each home* |
| `AF_ECS_EC2_BACKUP_KEEP` | `3` | How many completed copies to keep per home. Snapshots are incremental, so copy 2 costs only what changed |

- Taken **while the home is in use** — a backup is crash-consistent, the same picture a
  power cut leaves. Quiescing would mean taking a working person's home away on a timer.
- **Never restored automatically.** A backup is older than the home by definition; handing
  somebody a silently older home is worse than telling an operator to decide. The restore
  runbook is docs/log/64 §64.21.4.
- The trigger is the idle-stop reaper, so `AF_IDLE_SWEEP_INTERVAL=0` turns backups off too.
- The Slots tab shows, per home, how old its newest spare copy is — and says so loudly when
  there is none.
- **Clean home does not delete them**, and nothing else does as a side effect. A tenant
  administrator deletes a member's copies on purpose from the member detail (*Delete backups*,
  audited as `workspace.delete_backups`) — the offboarding step for a home that must not
  survive anywhere, taken after Clean home: while the home exists, the schedule keeps
  taking copies of it. Destroying the workspace deletes them too. Clean home itself deletes the
  home volume and its hibernation snapshots; the member's logins and connections are on EFS
  and stay.

**Baking the workspace image into the slot AMI: tried, measured, removed.** A slot's root
volume IS the image cache, so baking the image in does remove the pull (31.8s → **0.185s**,
measured) — and makes the slot **slower overall**, because a private AMI's root is lazily
loaded from a fresh snapshot: the instance took ~56s longer to join the cluster and a new user's
first start measured **179–192s against 144s** on the stock ECS-optimized AMI. The script
and the CP-side reporting were removed rather than left as a not-recommended option; the
measurement and the reasoning are in docs/log/64 §64.24 / ADR 0045 decision 19. **`SlotAmiId` stays
at its default** (the ECS-optimized AMI's SSM parameter; since #3318 the CP also reads that parameter
itself at launch, so slots follow it without a deploy — decision 7).

**A slot that cannot mount a home is quarantined** (`af-role=quarantined`, ADR 0045 decision 20):
it leaves the pool so nobody else lands on it, its home is detached and freed for another
slot, and the instance is stopped. It stays on the Slots tab with the reason, because it still
holds its root volume — **remove it with "Terminate" at the end of its row** once you have
taken what you need from it (ADR 0045 decision 30; nothing collects one by itself, since both
sweeper walks filter on `af-role=slot`). The failure that made this necessary was a
wedged kernel holding a deleted volume's NVMe namespace, which no amount of retrying fixes.

The button is `DELETE /api/admin/ec2-pool/slots/{id}` (super_admin), and it is the only route
in the product that deletes a machine on request. It accepts a box of this pool tagged
`af-role=quarantined` with no home attached and no live claim, and refuses everything else —
a working slot included, which leaves through `Ec2SlotTerminateAfterSec` instead. The
quarantine reason goes into the audit log before the instance (and its tags) disappear.

**A wedged slot** (running, status checks `ok`, `agentConnected=false`, SSM silent, a home still
attached) is stopped with **Stop (force)** on its row
(`POST /api/admin/ec2-pool/slots/{id}/force-stop`, super_admin, audit `pool.slot_force_stop`; #2449). It
refuses a slot whose ECS agent is connected and SSM online unless the body says
`{"override_healthy": true}`, and refuses a stopped or quarantined slot; ends the occupant's Start (the
runtime's own Stop); cancels the CP's own pending SSM commands (those commented
`agent-fleet slot volume`); and calls `StopInstances` gracefully, repeating it with `Force` after 3
minutes (only on an instance still `stopping` from the same launch) or at once on a second press while
`stopping`. Graceful first because a wedged box may still have the home mounted and Force is a power
cut. The slot is **not** quarantined and the home stays attached: the member's next Start wakes the
same stopped slot, the reboot clears the thrash, and its ECS agent reports a task that was still
STOPPING (#2447). Evidence-keeping quarantine for dark slots is #2450. Needs `ssm:ListCommands`,
`ssm:CancelCommand` and `ssm:DescribeInstanceInformation` on the CP task role (`20-platform`,
`SsmSlotForceStop`; apply that stack before using the button). A task still STOPPING on a slot whose
agent is disconnected is shown on the row. Not verified against a live AWS account.

**Golden snapshot: skip boot-install for new users.** A brand-new home pays boot-install
(4 CLIs 41s + rtk 1s + agy 6s = 48s) and a cold npm cache. Bake one home that has already
paid it and every later user starts from that copy (ADR 0045 decision 9).

**The CP does this by itself** (decision 9-1, `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` to switch off).
When the image it runs has no golden, it boots a reserved seed through the ordinary Start
path, captures its home as a *candidate*, boots a second reserved member from that
candidate, and only publishes it once that one comes up. It will not start while the pool
has fewer than two free slots, and it gives up on an image after two failed candidates —
the pool panel says which of those is happening. Everything below is the manual path, for
a deployment that has turned it off (or for baking one on the spot):

```bash
# 1. create a seed member, start their workspace from the Console, let it finish booting
# 2. stop it and wait for the sweeper to STOP THE SLOT (see below — the home stays attached)
./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
# 3. destroy the seed workspace:  DELETE /api/admin/workspaces {tenant_slug,user_key}
```

- **A seed member has to be somebody who can sign in.** There is no admin "start
  this member's workspace" — `/api/workspace/start` always resolves to the caller's
  own identity — so an invite-only address nobody holds gives you a membership whose
  workspace can never be created. Adding an account you DO hold to a throwaway tenant
  is the cheap way to get the fresh membership a seed needs.
- **Step 3 is a membership of your own, and that is allowed.** The roster refuses only
  your *last* membership (docs/log/61 §61.10.6), so you take yourself off the throwaway
  tenant from the Console — *Remove member* with *destroy the workspace and home*
  ticked, or remove then destroy. Until that rule was narrowed, every self-removal was
  refused and a deployment with a single administrator could not free the seed's slot at
  all — which then blocks the automatic bake, because it will not start with fewer than
  two free slots.
- **Step 2 waits for the slot to stop, not for the volume to detach.** A Stop keeps the
  home attached on purpose (the attachment *is* that user's slot), and the sweeper only
  stops the instance — it logs `stopping slot <id> (home stays attached)`. Nothing in the
  normal lifecycle ever detaches it, so waiting for `available` never ends. Baking off a
  **stopped** slot is correct: that shutdown unmounted the filesystem, which is the same
  reasoning `releaseSlot` uses when it skips the umount on a stopped slot.
- **Do not clone repositories into the seed.** `~/repos` lives on the home volume, so
  anything cloned there is handed to every new user. Bake boot-install and nothing else.
- **Re-bake on every release that moves the image or a CLI pin.** The CP compares the
  `af-image` tag against the image it runs and **refuses a stale golden**, falling back to
  an empty home — new users just get the slow first start, and the CP logs why. Forgetting
  to re-bake cannot silently hand anyone old CLIs.
- One golden per pool, shared. It carries no `af-membership` tag, so destroying a
  workspace never touches it.

**Destroying a workspace (irreversible).** `DELETE /api/admin/workspaces
{tenant_slug,user_key}` deletes the home and every per-membership resource — on this
profile the EBS volume, any hibernation snapshot, the slot claim, the ECS service, both
EFS access points and both SSM parameters. It only accepts a membership that has already
been removed, and it **overrides the deletion locks of ADR 0028** (those live inside the
home, which is unreadable while the workspace is stopped). Removing a membership on its
own still keeps everything, as before; `{"purge": true}` on that call does both at once.
⚠️ On Fargate the same call cannot delete the EFS *directories* behind the access points —
they survive, and keep billing. The response and the audit entry list what was left.

**Operational facts worth knowing before you turn it on**

- **A workspace keeps its slot while it is stopped, and the slot goes to sleep with it.**
  Stopping a workspace does not detach its home ("lazy release"): the attachment IS the
  affinity, so the same person comes back to the same slot without re-attaching or
  re-mounting. After `Ec2SlotSleepSec` (default 15m) the sweeper **stops** that slot, so
  the image cache survives on its root volume. A stopped slot costs only that volume
  ($3.84/month at 40 GiB) instead of ~$95 for a running one.
- **Stopping ends the compute charge; only terminating ends the ROOT VOLUME charge.**
  `Ec2SlotTerminateAfterSec` (default `0` = off) is the next step on the same clock: past
  it the instance is terminated and its root volume goes with it. ⚠️ Leave it off and the
  number of retained roots only ever grows, with `Ec2MaxSlots` as its ceiling — so raising
  `Ec2MaxSlots` to serve more people also signs you up for that many root volumes,
  permanently and whether or not anyone is working (30 × 40 GiB ≈ **$115/month**). That was
  measured on a live deployment, where the only cure was terminating instances by hand
  (docs/log/64 §64.32). **14400 (4h) is the recommended value**, and it costs your users 25
  seconds: come back the same day and you wake the instance (~110s), come back tomorrow and one
  is built for you (~135s).
- **Two different idle timers, in series.** `AF_WS_IDLE_TIMEOUT` / the per-tenant
  `ws_idle_timeout` is the product's existing idle-stop: it watches the person and stops
  their *workspace* (every runtime has it). `Ec2SlotSleepSec` only starts counting after
  that, and it stops the *slot*. Someone who walks away is therefore idle-stopped on the
  tenant's timeout and their instance sleeps 15 minutes later.
- **Slots are reclaimed only at the cap.** Below `Ec2MaxSlots` a new user gets a new
  slot; at the cap the longest-dormant occupant **of the same instance type** is evicted
  (a workspace with a running task is never touched). So `Ec2MaxSlots` bounds how many
  people work *at once*, `Ec2SlotSleepSec` bounds how many instances are *running*, and
  `Ec2SlotTerminateAfterSec` bounds how many *exist* — without the third, "how many
  exist" is `Ec2MaxSlots` too.
- **At the cap, an instance of the wrong size is removed rather than handed over.** An instance
  type is not something a running instance can change, so if the cap is held entirely by sizes
  a member cannot run on, eviction has nothing to offer. Rather than fail their start, the
  CP terminates one dormant instance of a size they cannot use — an empty one first, otherwise
  the longest-dormant — and builds theirs in its place. What that costs its former owner is
  the image cache (~110s becomes ~135s on their return), never their home: it is detached
  first and never deleted. ⚠️ This is **not** gated on `Ec2SlotTerminateAfterSec`: that
  parameter is about idle cost, this only ever runs when the alternative is a member who
  cannot start at all — and its default of `0` is exactly the deployment that sits at the
  cap. If nothing can be freed (everything running, or claimed by a start in flight) the
  start still fails, and says the pool is full.
- **An EMPTY slot sleeps on the same timer.** A slot whose home has been released — by an
  eviction, a size/class change, a `Destroy`, or the golden bake finishing with its seed
  and probe — belongs to nobody, and `Ec2SlotSleepSec` stops it too. ⚠️ Before docs/log/64
  §64.31 only *occupied* slots were ever stopped, so a released instance ran until an operator
  noticed: measured on a live deployment, three empty `m*.large` up for over 24h with zero
  tasks, at ~$95/month each.
- **No hot spare is kept, and no warm floor either.** The first person of the morning
  wakes a stopped slot (~110s) or, if the pool has none, pays the full ~135s to build one.
  Keeping an empty slot *hot* would save them ~92s (43s vs 135s) for a full instance-hour,
  every hour — `Ec2SlotSleepSec=0` buys that if you want it. Keeping stopped instances around
  instead ("keep at least N warm") buys much less than it looks: a dormant instance is only
  reusable **by its owner** (its home is still attached, so `freeSlots` never offers it to
  anyone else), and making one generic means the next user pays the wake plus the attach
  plus the mount SSM round trip — 123–143s against 135s for a fresh instance. So there is no
  floor parameter; `Ec2SlotTerminateAfterSec` alone decides how long an instance is kept.
- **AZ is destiny.** An EBS volume cannot leave its AZ, so a user is pinned to the AZ
  their home was created in. If no slot can be run there, that user cannot start.
- **A slot's root volume is shared with whoever had it before.** `/tmp` is a tmpfs
  (nothing lands on disk, capped size; mounted `exec,nosuid,nodev`, `AF_ECS_EC2_TMP_OPTS`) and the ECS task-cleanup window is shortened to
  5m, but the image cache and container write layers are genuinely shared.
- **Patching slots = updating this stack** (the AMI parameter resolves at update time)
  and letting the old slots go. That is the operational cost the EC2 launch type adds.
  The same holds for the user data (§Moving retained slots onto new user data).
  **Kernel patch cadence:** a slot runs the kernel of the AMI it was launched from and is
  never patched in place. Refresh the AMI (update this stack) on the cadence your security
  policy sets for the kernel, then replace the old slots as below. The task adds
  `SYS_ADMIN` (below), so a kernel fix for a namespace or netfilter bug matters here more
  than on a stricter profile.
- **Credentials still live on EFS.** The auth/identity set (`homeKeep`: `.config`,
  `.ssh`, `.git-credentials`, `.gitconfig`, `.claude`, `.claude.json`, `.codex` — under
  100 MiB) is kept on an EFS access point and symlinked into home by the entrypoint, so
  losing one single-AZ volume does not take the user's logins with it.
- **The working disk (`WsDiskGiB`) does not apply.** `AF_WS_SCRATCH` is not injected on
  this profile: home is already local EBS, so there is nothing to relocate off EFS.

### Moving retained slots onto new user data

The workspace task adds `SYS_ADMIN` so Chromium's setuid `chrome-sandbox` can create its
namespaces. Two slot settings in the user data go with it, and **both apply only to slots
launched after the template carries them, so a slot launched earlier must be replaced
(a new launch template version, then the steps below) before they hold**:

- `user.max_user_namespaces=0` (persisted in `/etc/sysctl.d/99-af-userns.conf` and applied
  before the ECS agent configures; the user data stops on failure, and an `ecs.service`
  drop-in refuses to start the ECS agent unless the value is 0, also after a reboot): no unprivileged user namespaces, so `SYS_ADMIN`
  stays reachable only through the setuid helper. A task running on an old slot keeps
  the default limit and can create user namespaces.
- the home volume mounted `nosuid,nodev` by `af-mount`. An already mounted home keeps its
  old options until it is unmounted and mounted again (a slot move does that).

`kernel.unprivileged_bpf_disabled` (1) and `kernel.perf_event_paranoid` (2) were measured
already strict on AL2023 and are not set. The task definition gains the capability on a
workspace's next Start (a new revision); no slot change is needed for that part.

`ECS_AWSVPC_BLOCK_IMDS=true` in the slot's `ecs.config` is what keeps a workspace task off the
slot's instance profile (`SlotRole`) through IMDS; the launch template's hop limit does not,
because an `awsvpc` task's ENI reaches IMDS directly. The ECS agent reads `ecs.config` when the
slot is launched, so **a slot launched before the template carried it stays open until it is
replaced**, and nothing replaces one by itself: a workspace **Stop → Start goes back to the
same slot** (its home stays attached, see "A workspace keeps its slot while it is stopped" above).
Until then the Agent's own isolation still holds for every SDK that honours
`AWS_EC2_METADATA_DISABLED`, but not for a tool that calls IMDS directly. To finish the move:

1. **Update the pool stack.** `update.sh` does it (§One command). By hand:
   `aws cloudformation deploy --stack-name <pool stack> --template-file cfn/40-ec2-pool.yaml
   --capabilities CAPABILITY_NAMED_IAM` (parameters keep their previous values). The CP
   launches slots from the template's `$Latest`, so only new slots change.
2. **Nothing to do for most slots: a slot below `$Latest` is replaced by itself.** When a
   workspace starts and its home is on a slot launched from an older launch template version
   (or another template), the Start treats the slot as reserved: the same move as step 3,
   with no notice in the member's WS bar (the Start just takes a little longer). A free slot
   below `$Latest` is terminated by the sweeper like a reserved one. The Slots tab marks such
   a slot "older than $Latest · replaced at next start". Each automatic move is in the audit
   log as `pool.slot_replace_auto` (actor `slot-auto-replace`; the detail names the workspace,
   the old version, `$Latest` and the new slot). Two differences from a reservation: if no new
   slot can be launched (capacity, quota) the Start **does not fail** — the workspace stays on
   its old slot and the CP retries at a Start at least 10 minutes later — and a slot whose
   version cannot be read is left alone. To switch it off (for example while a new template
   is being validated) set `AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS=false` on the Control Plane;
   reservations then still work as below.
   **Reserve the old slots for replacement** in the Console when a workspace must
   never be put back on its old slot (a reservation never falls back: if no new slot can be
   launched, that Start fails): Settings → Admin → the Slots tab
   (super_admin). Each slot shows the launch template version it was launched from and
   whether that is older than `$Latest`; "Reserve all N for replacement…" lists the slots
   and the workspaces on them before it reserves exactly those below `$Latest` (each one is
   re-checked when it is written, and each reservation is in the audit log as
   `pool.slot_replace_reserve`). "Replace at next start" / "Cancel replacement" on a row does
   one slot. The reservation is a tag on the instance (`af-slot-replace`); it moves nobody
   by itself and touches no running session.
3. **Users stop and start their workspaces** when it suits them. For a reserved slot, their
   WS bar says the next start moves to a new slot (not for an automatic move). That Start launches a new slot of the workspace's class from
   the template's `$Latest` first, then moves the home off the reserved slot through the
   same release the sweeper uses (unmount before detach; refused while a task runs) and
   terminates the old instance; the home volume is never deleted. If the new slot cannot
   be launched (capacity, quota) the Start fails with the reason, the home stays where it
   was and the reservation stays: it never falls back to the reserved slot. A reserved
   slot with no home is never handed to anybody and the sweeper terminates it.
   Do not terminate slot instances by hand while a home is attached.
   `Ec2SlotTerminateAfterSec` is not needed for this; it is a standing cost trade-off
   (see `cfn/PARAMETERS.md`), not a migration tool.
4. **Verify from a shell session in a workspace on a new slot**: the IMDSv2 token request
   `curl -s -o /dev/null -m 3 -w '%{http_code}' -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token`
   must not answer `200` (it times out: `000`), also with a clean environment
   (`env -i`), and `aws sts get-caller-identity` must fail with "Unable to locate
   credentials". Check that slots still join the cluster and SSM still reaches them (the
   Slots tab, a workspace Start, the home mount), and that the workspace's logs still arrive.

## Cost & ephemerality

**Standing cost with every workspace stopped and nobody logged in** — list prices
pulled from the AWS Pricing API for **ap-northeast-1**, 730 h/month, 2026-08-15
(scale by your region's rates; us-east-1 is roughly 30% lower):

| Always-on | Rate | $/month |
|---|---|---|
| NAT Gateway ×1 | $0.062/h | **45.3** |
| **CP/Console Fargate task ×1** (0.5 vCPU + 1 GB, `DesiredCount: 1`) | $0.05056/vCPU-h + $0.00553/GB-h | **22.5** |
| RDS db.t4g.micro Single-AZ | $0.025/h | **18.3** |
| ALB ×1 | $0.0243/h (+ $0.008/LCU-h, ~0 when idle) | **17.7** |
| RDS storage 20 GiB | $0.138/GB-mo | 2.8 |
| Secrets Manager (RDS-managed password) | $0.40/secret | 0.4 |
| Cloud Map private namespace (Route 53 private zone) | — | 0.5 |
| ECR (CP + WS ≈ 1.05 GB) | $0.10/GB-mo | 0.1 |
| EFS (every user's home persists) | $0.36/GB-mo | usage |
| EFS backups (`Persistence=retain` only) | $0.06/GB-mo warm backup storage | usage |
| **Total** | | **≈ $107/mo + EFS** |

Stopped workspaces cost **nothing** (that is scale-to-zero working); ACM, the NAT's EIP and standard SSM parameters are free. A *running*
workspace adds Fargate at **$0.0616/h** (1 vCPU + 2 GB) — ~$11/mo at 8 h × 22 days,
$45/mo if left on around the clock.

So **the floor is four always-on pieces that do not care how many users you have**.
The lever is NAT: a **NAT instance** (t4g.nano + EBS + its public IPv4 ≈ $8/mo)
cuts ~$37/mo, at the price of running it yourself. Dropping the CP task to
256/512 saves another ~$11 (unverified that it fits). ALB and RDS are effectively
the floor — Aurora Serverless v2 costs more at its 0.5-ACU minimum.

**One or two users: [`../ec2-single`](../ec2-single/) is cheaper**, and that is what
it exists for. A t3.medium + 30 GB gp3 + EIP is ~$47/mo **with the workspaces
included** (t3.large, the default, ~$87/mo), versus ~$72/mo for a trimmed ECS
deployment that still bills workspace hours on top. ECS wins on cost only around
**8–10 concurrent users**, where the single VM has to be sized for everyone's peak
24/7 while Fargate bills only the hours each workspace actually runs — plus the
isolation ECS gives you regardless of price.

For iteration, **deploy → E2E → `delete-stack`** and keep stacks short-lived.

- NAT is needed because workspaces egress to **git / Anthropic** (public internet),
  and every private-subnet task also reaches ECR / CloudWatch Logs / SSM through it
  (there is no public IP on those tasks) — so the NAT is on the **workspace start
  path**, not just the user's internet access.
  - **Workspaces have subnets of their own** (#2716): `00-network` gives each AZ a workspace
    subnet on a workspace route table whose only route out is the NAT, and the CP puts
    workspace task ENIs there (`AF_ECS_WORKSPACE_SUBNETS`, from 30-ingress's
    `WorkspaceSubnets`). The **S3 gateway endpoint** is back on the **private route table
    only** (CP, ecs-ec2 slot instances, engines, the home task), with a policy that admits
    ECR layers, the AL2023 repositories, the SSM Agent update and ECS agent download buckets
    and the models bucket, and nothing else. A refused
    same-region request there is AccessDenied, not NAT traffic, which is why no workspace may
    sit on that route table: a workspace's own S3 always leaves through the NAT (later the
    egress gateway, ADR 0122), where it can be recorded (#2657).
    **ecs-ec2 cold pulls stay off the NAT**: the ECS agent pulls through the slot instance's
    ENI in a private subnet, so the ~918 MiB compressed workspace image (see the start-latency
    measurement below) goes through the endpoint. **Fargate cannot split**: it pulls through
    the task ENI, so a Fargate workspace start still pays NAT data processing for its layers.
    The models bucket reads of `60-engines` use the endpoint, and so do the CP's writes to the
    optional raw egress event archive bucket ("Egress event archive" below, #2732); a bucket
    the policy does not name is refused.
    The endpoint has a switch, `S3EndpointEnabled` (default `true`). It must stay `false`
    while any workspace still runs in a private subnet, because the policy refuses that
    workspace's own S3 (AccessDenied) until it is restarted into a workspace subnet.
    **Moving a running stack** (a maintenance window; every running workspace is restarted;
    from v0.34.x, "Upgrading from v0.34.x" under the upgrade runbook is the same with the backup
    and the commands):
    1. Deploy `cfn/00-network.yaml` with `S3EndpointEnabled=false` through a change set
       (`aws cloudformation deploy … --capabilities CAPABILITY_IAM --parameter-overrides
       S3EndpointEnabled=false --no-execute-changeset`, then `describe-change-set`). It adds six
       resources: two subnets, the workspace route table, its default route and two
       associations. A stack that already has an S3 endpoint (any v0.34.x) also shows *Remove*
       `S3Endpoint`; that is expected and puts private-subnet S3 on the NAT for the window.
       Anything else modified or replaced: stop. Otherwise execute it. Never skip this step
       with a deploy at the defaults: `S3EndpointEnabled=true` would *Modify* `S3Endpoint` to
       the restricted policy while every workspace is still in a private subnet.
    2. Run `update.sh`. It passes the network stack's `WorkspaceSubnets` to the CP
       (`AF_ECS_WORKSPACE_SUBNETS`). New Starts now go to the workspace subnets, and it ends
       by listing the running workspaces still in a private subnet.
    3. Stop and start every workspace that was running (`pause.sh --status` lists them, or
       `aws ecs list-services` with `desiredCount > 0`). A running task keeps its ENI: it
       moves only on a new Start. Check that each task's ENI is in a workspace subnet:
       `aws ecs describe-tasks --cluster <c> --tasks <t> --query
       "tasks[].attachments[].details[?name=='subnetId'].value"`.
    4. Deploy `cfn/00-network.yaml` with `S3EndpointEnabled=true` (one *Add*: `S3Endpoint`).
       Private-subnet S3 moves from the NAT route to the endpoint route, and open TCP
       connections do not survive that, so do it when no large model fetch/ingest or image
       pull is running.
    **Rollback**: `S3EndpointEnabled=false` puts every private-subnet S3 request back on the
    NAT; once the workspace subnets exist (after step 1) that is a stack update that removes
    only `S3Endpoint`. Leave the workspace subnets and
    route table in place while any workspace service may still use them; removing them is a
    template change, not a parameter. A new stack (`standup.sh` on no existing network stack)
    gets the endpoint on from the start, before any workspace exists. `pause.sh` and
    `standup.sh` pass `S3EndpointEnabled=false` to a live network stack that has never had
    the switch, so neither turns it on as a side effect, and `pause.sh --up` warns when the
    network has workspace subnets the CP is not using.
    While paused (`NatEnabled=false`) S3 is still reachable from the private subnets through
    the endpoint (for what its policy admits), unlike ECR's API, logs and the workspace route
    table's way out.
    **Interface** endpoints (ecr.api, ecr.dkr, logs, ssm) are left out on purpose: $0.01/AZ/hour
    × 2 AZ each adds up past the NAT Gateway they would relieve.
  - What still crosses the NAT: git / Anthropic / npm / package registries (the real
    developer traffic), plus the small ECR auth+manifest, Logs and SSM calls.
  - **What that costs per start (Fargate), and why `ecs-ec2` is the answer at high start
    volume.** Fargate keeps no image cache, so every start pulls the whole image
    (~918 MiB compressed) through the NAT, and NAT data processing is billed per GB.
    Example, at $0.045–0.062 per GB processed: 918 MiB ≈ 0.9 GB ≈ **$0.04–0.06 per
    start**, so 100 starts a day (about 3,000 a month) is roughly **$120–180 a month**
    on top of the gateway's own hourly charge. The rate varies by region and changes:
    take it from the "NAT Gateway" section of
    [Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/) for your region; the figures
    here are an illustration, not a quote. On `ecs-ec2` the image cache is the slot's own
    root volume, so restarting on an existing slot that already holds the same image avoids
    the layer pull: a cached start measured **~65 MiB of NAT traffic in total** in a sandbox.
    A new slot, a recreated one after retirement, a cache miss or an image update pulls the
    layers again, but through the S3 gateway endpoint (the workspace-subnet bullet above,
    #2716; verified on a test deployment, not yet in production), so only the small ECR auth and manifest
    calls cross the NAT. (The golden snapshot is a different thing: a home that already paid
    CLI boot-install, which saves time, not NAT bytes.) Where many starts a day are expected,
    prefer `ecs-ec2`. The `60-engines` ingest downloads from Hugging Face / Civitai cross the
    NAT at the same per-GB rate; the engines' reads of the models bucket use the endpoint.
    The choice between a NAT gateway and an egress gateway (#2717) is §Egress gateway.
  - Replacing the NAT Gateway with a **NAT instance** (t4g.nano $3.9 + 8 GB gp3
    $0.8 + its public IPv4 $3.7 ≈ **$8/mo**, vs **$45** for the managed gateway) is
    the biggest single lever, at the price of running it yourself (source/dest check
    off, iptables MASQUERADE, ASG for replacement + route rewrite).
- Persistence: `10-data.yaml` takes a **`Persistence` parameter** — `delete`
  (default, sandbox-ephemeral: EFS/RDS dropped with the stack, no backups) or
  `retain` (production: EFS `Retain` + daily AWS Backup kept `EfsBackupRetentionDays`
  (7), RDS `Snapshot` + 7-day backups + deletion protection). **Deploy production with
  `Persistence=retain`.** Backup and restore: §EFS backup and restore.

## EFS backup and restore

`Retain` on the EFS only means the file system outlives a deleted stack. What it holds
is lost the moment something inside a *running* file system is deleted or overwritten:
on `ecs` every member's home (`/home/<membership>`) and Claude state
(`/claude-config/<membership>`); on `ecs-ec2` the Claude state and the kept dotfiles
(`/home-keep/<membership>`) — the home itself is on EBS there (§EC2 slot pool, "Spare
copies of each home"). So under `Persistence=retain` `10-data` also declares:

| Resource | What it is |
|---|---|
| `EfsBackupVault` | vault `<data stack>-efs-<8 hex digits of the stack id>` (e.g. `af-ecs-data-efs-1a2b3c4d`; the stack output `EfsBackupVaultName`). `DeletionPolicy: Retain` — AWS refuses to delete a vault that still holds recovery points. Vault names stop at 50 characters, so under `retain` the data stack name is limited to 37 (`standup.sh` checks) |
| `EfsBackupPlan` | one rule, daily at 17:00 UTC (02:00 JST), each point kept `EfsBackupRetentionDays` (default **7**, the same as RDS) |
| `EfsBackupSelection` + `EfsBackupRole` | this file system only, backed up with the AWS-managed backup/restore policies |

`Persistence=delete` creates none of it. Backups are incremental after the first full
copy, crash-consistent (taken while workspaces are running), and **never restored
automatically** — a restore is always one of the operator steps below.

Because the vault is `Retain`, it outlives its stack: `teardown.sh` without
`--purge-retained` keeps it, and so does switching a stack from `retain` to `delete`.
The stack id in its name keeps a later stand-up under the same stack name from
colliding with it, and `teardown.sh` lists every `<data stack>-efs-*` vault in its plan
and sweep whatever the persistence. One case still collides: switching the *same* stack
`retain` → `delete` → `retain` (the stack id is unchanged). Delete the kept vault first
(§Teardown, step 9 shows how).

**Turning it on for a deployment that already runs.** `update.sh` does not touch
`10-data`, so the backups arrive only when that stack is updated by hand. Without
`--parameter-overrides`, every parameter keeps its current value. Create the change set
without executing it, read it, then execute it:

```bash
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \
  --profile <p> --region <r>
# the command prints the change set's ARN
aws cloudformation describe-change-set --change-set-name <arn> \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table
aws cloudformation execute-change-set --change-set-name <arn>
aws backup list-backup-plans --query "BackupPlansList[].BackupPlanName"   # af-ecs-data-efs-daily
```

⚠️ Execute only if the change set adds the four `EfsBackup*` resources and nothing else —
in particular no `Modify` of `FileSystem`. A stack update moves the live file system to
whatever `EfsThroughputMode` says (see the warning in `10-data.yaml`), and any
`Replacement` there is a new, empty file system.

**The Console says so too.** `update.sh` and `standup.sh` write the data stack's state into
the ingress stack's `EfsBackup` parameter (`present` / `missing` / `off`; empty = never
filled), the CP receives it as `AF_EFS_BACKUP`, and on `ecs` / `ecs-ec2` a value of
`missing` puts a banner on every section of the Admin screen for super_admins. No CP
permission is involved: the CP reads an environment variable, never CloudFormation or AWS
Backup. The banner is only as fresh as the last `update.sh`, so after executing the change set
above run `update.sh` once more to clear it. An unreadable data stack leaves
the parameter as it was, and an empty one shows nothing.

**On demand** (before an upgrade, or before touching someone's home by hand):

```bash
EFS=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsId'].OutputValue" --output text)
ROLE=$(aws cloudformation describe-stack-resource --stack-name af-ecs-data \
  --logical-resource-id EfsBackupRole --query StackResourceDetail.PhysicalResourceId --output text)
VAULT=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text)
ACCT=$(aws sts get-caller-identity --query Account --output text)
aws backup start-backup-job --backup-vault-name "$VAULT" \
  --resource-arn "arn:aws:elasticfilesystem:<r>:$ACCT:file-system/$EFS" \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --lifecycle DeleteAfterDays=7
```

**Pick a recovery point** (newest last):

```bash
aws backup list-recovery-points-by-backup-vault --backup-vault-name "$VAULT" \
  --query 'sort_by(RecoveryPoints,&CreationDate)[].[CreationDate,Status,RecoveryPointArn]' --output text
```

**Restore one member's directory.** AWS Backup never restores over live data: a
restore into the same file system lands under a new top-level directory,
`/aws-backup-restore_<timestamp>/`, beside the originals, keeping the paths below it.
Up to five paths per job:

```bash
aws backup start-restore-job --recovery-point-arn <rp-arn> \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --resource-type EFS \
  --metadata '{"file-system-id":"<efs-id>","newFileSystem":"false","Encrypted":"true",
    "PerformanceMode":"generalPurpose",
    "ItemsToRestore":"[\"/home/<membership>\",\"/claude-config/<membership>\"]"}'
aws backup describe-restore-job --restore-job-id <id>    # wait for COMPLETED
```

The restored copy is outside every access point, so no workspace can see it. Putting it
back needs a mount of the file system root — a one-off Fargate task in the private
subnets with the workspace security group, an EFS volume on `$EFS` with root directory
`/` and transit encryption on, running as root:

1. Stop that member's workspace and ask them not to start it, so nothing writes to the
   directory while it is replaced.
2. In the task: `mv /mnt/home/<membership> /mnt/home/<membership>.before-restore`, then
   `cp -a /mnt/aws-backup-restore_<ts>/home/<membership> /mnt/home/` (the same for
   `claude-config` / `home-keep`). Ownership comes back as it was backed up, which is the
   access point's uid/gid.
3. Start the workspace, have the member confirm, then delete `.before-restore` and the
   `aws-backup-restore_*` directory — EFS bills both until then.

**Restore the whole file system.** The same command without `ItemsToRestore` restores
everything into `/aws-backup-restore_<timestamp>/` on the live file system; put back
what is needed with the same one-off task, workspaces stopped. Restore into a new file
system (`"newFileSystem":"true"`, no `file-system-id`) only when the live one itself is
gone. Moving the deployment onto a new file system is not a template change — `EfsId`
is exported to the stacks above and the Control Plane has created access points on the
old ID — so even then, copy the directories onto the file system the stacks name
rather than re-pointing them.

**Cost** (ap-northeast-1, AWS Pricing API, 2026-09): warm backup storage **$0.06 per
GB-month**; a restore **$0.024 per GB**, plus **$0.60 per item-level restore job**.
Seven daily points of a file system that changes little cost roughly one full copy plus
the week's changes — for a 20 GB file system about $1.2–2/month. After the stack is
deleted, the points already taken keep expiring on their own schedule; `teardown.sh
--purge-retained` deletes them and the vault at once.

⚠️ The restore steps above are written from the AWS Backup documentation and have not
yet been run end to end on a real deployment (#1235).

## Lifecycle scripts (stand up / pause / tear down)

The runbooks in this file are the source of truth; these four are their executable
form (docs/log/73). A deployment is addressed the way `update.sh` already addresses one —
by **AWS profile** — and everything else is read from the live deployment, which
matters because two deployments built from these same templates can differ in ways a
hardcoded default gets wrong (one names its pool stack `af-ecs-pool`, another
`af-ecs-ec2-pool`).

```bash
./capture-env.sh --profile <p> --region <r>            # record a live deployment
./pause.sh       --profile <p> --region <r> [--up]     # stop / resume (data stays)
./teardown.sh    --profile <p> --region <r> [--yes]    # delete everything
./standup.sh     --profile <p> --region <r> [--yes]    # build it back
```

- **`pause.sh` stops or deletes everything that bills by the hour, in the one order that strands nothing.**
  Workspaces, then the slots and the engines' GPU boxes (waited for, or ended directly with
  `--fast`), then the CP, then a sweep that terminates any engine box still alive — once the
  CP is gone nothing else ever will, and a g6.xlarge left up is ~$28 a day. The RDS instance
  is stopped next (`--keep-db` leaves it) and started first on `--up`, before the CP. The NAT
  gateway goes last (`--keep-nat` leaves it) and comes back first on `--up`, because the CP's
  own image pull and logs go through it.
  - **The NAT gateway has no "stop", so it is deleted — through `00-network`'s `NatEnabled`
    parameter, never with `delete-nat-gateway`** (the stack would keep a dead physical id and the
    next update would fail). `pause.sh` runs `cloudformation deploy` of `cfn/00-network.yaml`
    with `NatEnabled=false` / `true`; every other parameter keeps its live value. A round trip
    is 1–2 minutes each way and the egress IP is unchanged, because `NatEip` is retained.
    Nothing in a private subnet reaches ECR, logs or the internet while the gateway is gone.
    A capture taken while paused records `NatEnabled=false`; `standup.sh` overrides it to
    `true`. It deploys the template of *this checkout*, so a network stack older than it gains
    the workspace subnets (#2716) on a pause or a resume. It gains the S3 endpoint only off
    (`S3EndpointEnabled=false`, passed when the stack has never had the switch), and `--up`
    warns, before any workspace can start, when the CP is not using the network's workspace
    subnets: run `update.sh`, then follow the migration in the NAT bullet below.
    With `EgressRoute=gateway` (ADR 0122) the egress gateway is scaled to zero through
    `GatewayEnabled=false` after the Workspaces and before the CP; `--up` starts it with the NAT
    gateway and waits for it before and after the CP ("Egress gateway", "Pausing"). Its
    interface and Elastic IP are never touched.
  - 🔴 **AWS starts a stopped RDS instance again after 7 days** (event `RDS-EVENT-0154`) and
    cannot be told not to. `10-data` (`RdsRestopOnForcedStart`, default `true`) therefore carries
    an EventBridge rule (`RDS-EVENT-0154` forced start, and `RDS-EVENT-0088` started, which covers
    a start longer than the Lambda's ~46 minutes of waiting and retries) and a small Lambda that
    stop it again, which restarts the clock. They act only on an instance tagged `af-paused=true`
    — the tag means "paused", so remove it (or run `--up`) to keep the database up on purpose.
    `pause.sh` sets the tag before it stops the database and removes it at the start of `--up`
    (also with `--keep-db`); a tag it cannot write fails the run (the database is still stopped).
    RDS has no compare-and-stop, so the race with `--up` is bounded, not excluded: the Lambda's last
    tag read and its stop call use 3 s connect / 5 s read timeouts and no SDK retries, `--up` waits
    20 s (`AF_TAG_GRACE_SEC`) after clearing the tag before it reads the database status (a database
    found stopping is started again), and reads it once more after the CP is up, failing when it is
    not available. A timeout limits how long the caller waits, not whether a request already sent
    takes effect, so a stop whose response was lost can still land after `--up`; that shows up as the
    failure above, and running `--up` again starts the database. The deployment needs a `10-data` deploy once to
    get them; `--status` names the Lambda when it exists, and still warns about a running
    database under a paused CP.
    The database is a plain RDS PostgreSQL instance; an Aurora cluster would need
    `RDS-EVENT-0153` and the cluster calls, which nothing here has.

- **`capture-env.sh` first, always.** It writes the parameters each stack was deployed
  with to `~/.config/agent-fleet/deploy/<profile>.<region>/` (outside the repository —
  the values are account-specific). The templates live here; **what was passed to them
  lives only inside the deployment**, and `delete-stack` takes it away. `teardown.sh`
  refuses to run without it, and `standup.sh` has nothing to deploy with.
  A parameter captured **empty** takes its value from the stack **Output** of the same
  name if there is one — an empty parameter often means "the stack made this itself", and
  then the id of what it made exists only as an Output (`NatEipAllocationId`, above).
- 🔴 **A capture proves the parameters, not the image it points at.** Three things line
  up here, and none of them is a trap on its own: `dev-deploy.sh` **re-tags the workspace
  image inside ECR** on the runs that do not re-bake it (so that tag's workspace never
  reaches GHCR); `capture-env.sh` **refuses to refresh** without `--force`, so the
  recorded `AF_IMAGE_TAG` silently stays at whatever was live the day you captured; and
  the ECR repositories in `20-platform` are `EmptyOnDelete: true`, so teardown deletes
  the images. The capture file then survives, intact, pointing at a tag whose workspace
  image exists nowhere. `standup.sh`'s preflight does say so — by name — but by then
  there is nothing left to copy back. So the freshness is kept where it rots:
  `dev-deploy.sh` writes the new tag into the capture as its last step, `capture-env.sh`
  records `AF_IMAGE_RECOVERABLE=yes|no|unknown` (is this tag still pullable from GHCR?),
  and `teardown.sh` says which of the three you are in **before** the confirmation —
  the last moment a `crane copy` out of ECR still works. `unknown` means `crane` was not
  installed: not measured, and deliberately not rounded down to "gone".
  What `./capture-restore-test.sh` pins (fake `aws` / `crane`, no AWS — it never creates,
  deletes or updates anything; the 198 calls it makes are reads against the fake): the
  `yes` / `no` / `unknown` decision and what `capture-env.sh` writes and refuses, and the
  three sentences `teardown.sh` prints before the confirmation. **`dev-deploy.sh` is never
  executed by it** — that script needs `gh`, `git ls-remote` and `update.sh` — so the
  follow-the-live-tag step is pinned in two weaker pieces instead: `af_env_set` as a unit,
  and a source check that the call to it is still on `dev-deploy.sh`'s success path.
  Deleting that call turns the source check red; **it does not prove the deployed script
  ran**, and nothing here proves a real round trip. That one is still only ever exercised
  against a live deployment.
- **Nothing happens without `--yes`.** They print the plan (and, for teardown, an
  inventory of what would go) and exit. With `--yes` on a terminal they also make you
  type the FQDN.
- **Secrets are neither captured nor restored.** The SSM SecureStrings are not CFN
  parameters; `standup.sh` only checks that they *exist*. Teardown keeps `/af-cp/*` by
  default so the same account can be redeployed into.
- **Which deployment is the development one is not in this repository.** `dev-deploy.sh`
  (docs/log/73) only touches a deployment whose local capture says `AF_DEV_DEPLOY=1`.
- The order these run in is what the stub test in `deploy/local/ecs-lifecycle-stub-test.sh`
  pins — see the next section for why each step is where it is.

## Teardown

> The executable version is `./teardown.sh --profile <p> --region <r>` (above). Read this section
> anyway: the script does exactly these steps, and when one fails you are back here.

Most of what a live deployment owns is **not** in the stacks: the Control Plane
creates workspace services, EFS access points, SSM parameters and — under
`WsRuntime=ecs-ec2` — slot instances, home EBS volumes and golden snapshots at
runtime. Deleting the stacks first leaves those behind and then **stalls on the
dependencies they hold**. The order below was measured end-to-end on 2026-08-22
against two real `ecs-ec2` deployments (one `Persistence=delete`, one `=retain`)
and ended with every sweep counter at zero in both accounts.

**1. Stop the Control Plane first.** Everything in steps 2–7 is *its* bookkeeping,
and it launches slots on demand — a running CP can recreate what you just deleted.

```bash
CL=af-af-ecs-platform      # cluster = af-<platform stack name>
aws ecs update-service --cluster $CL --service af-af-ecs-ingress-cp --desired-count 0
```

**2. Workspace services** — `af-ws-*`, one per member that ever started a
workspace. `delete-service --force` also removes the service's Cloud Map entry, so
do not delete that by hand (you get `ServiceNotFound`).

```bash
for s in $(aws ecs list-services --cluster $CL --query 'serviceArns[]' --output text | tr '\t' '\n' | grep /af-ws-); do
  aws ecs update-service --cluster $CL --service "$s" --desired-count 0 >/dev/null
  aws ecs delete-service --cluster $CL --service "$s" --force >/dev/null
done
```

**3. Slot instances and home volumes** (`ecs-ec2` only). Terminating a slot takes
its root volume but **not the home volume** — homes are detached and kept on
purpose (delayed return), so they survive and keep billing.

```bash
IDS=$(aws ec2 describe-instances --filters Name=tag:Name,Values='af-slot-*' \
  Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
aws ec2 terminate-instances --instance-ids $IDS && aws ec2 wait instance-terminated --instance-ids $IDS
# then every leftover af-ws-*-home volume
aws ec2 delete-volume --volume-id <vol-id>
```

**4. Container instances that stay registered.** After the EC2 instances are gone,
the cluster can still list them (3 of 4 did, in one account) and the platform
stack's cluster delete fails on that. `aws ecs deregister-container-instance
--cluster $CL --container-instance <arn> --force` each one.

**5. EFS access points** — `describe-access-points --file-system-id <efs>`, delete
each. ⚠️ **Miss these and `delete-stack af-ecs-data` stalls.** The filesystem id is
the `EfsId` output of `af-ecs-data` (*not* `FileSystemId` — a sweep script keyed on
the wrong name silently deletes nothing).

**6. SSM `/af-ws/*`** (per-workspace DEK/token). `/af-cp/*` are the out-of-band CP
secrets you created *before* `30-ingress`: keep them to redeploy into the same
account, delete them to rehearse the prerequisites from scratch.

**7. Golden snapshots** — `describe-snapshots --owner-ids self`, delete each
(50 GiB per baked home, one per image tag).

**8. The stacks, in reverse order, one at a time.**

```bash
for s in af-ecs-ingress af-ecs-tts af-ecs-pool af-ecs-platform af-ecs-data af-ecs-network; do
  aws cloudformation delete-stack --stack-name $s
  aws cloudformation wait stack-delete-complete --stack-name $s || break   # stop on the first failure
done
```

⚠️ **Issue them one at a time and wait.** CloudFormation **cancels** the delete of
an exporting stack while an importer still exists (`Cannot delete export … as it is
in use by …`) — fire all five together and the last ones silently do nothing while
your wait loop spins on a delete that was already cancelled. `af-ecs-pool` exists
only under `ecs-ec2` and `af-ecs-tts` only where speech was enabled; each stack name
is whatever you deployed `40-ec2-pool.yaml` / `50-tts.yaml` as. Scale the engine
service to 0 before deleting `50-tts`: its Cloud Map service cannot be deleted while a
task is still registered, and a task on its way down is exactly that.
Wall clock: ~10 min (`delete` persistence) to ~16 min (`retain`, RDS snapshotting).

**9. `Persistence=retain` needs four extra steps** — a plain `delete-stack` fails
on the first one:

```bash
aws rds modify-db-instance --db-instance-identifier <db> --no-deletion-protection --apply-immediately
# after af-ecs-data is deleted:
aws rds delete-db-snapshot --db-snapshot-identifier af-ecs-data-snapshot-db-<suffix>  # DeletionPolicy: Snapshot leaves one
aws efs delete-file-system --file-system-id <efs>                                     # DeletionPolicy: Retain keeps it
# each EFS backup vault (DeletionPolicy: Retain) cannot be deleted until its recovery points are:
aws backup list-backup-vaults --query "BackupVaultList[?starts_with(BackupVaultName,'af-ecs-data-efs-')].BackupVaultName"
for rp in $(aws backup list-recovery-points-by-backup-vault --backup-vault-name <vault> \
    --query 'RecoveryPoints[].RecoveryPointArn' --output text); do
  aws backup delete-recovery-point --backup-vault-name <vault> --recovery-point-arn "$rp"
done
aws backup delete-backup-vault --backup-vault-name <vault>                            # once the points are gone
```

**10. Task definitions.** They cost nothing but they outlive every stack.
⚠️ **`list-task-definitions --family-prefix af-ws` reported 0 while 9 ACTIVE
revisions existed** — enumerate without the prefix and count. Deregister every
ACTIVE revision, then `delete-task-definitions` the INACTIVE ones in batches of 10.
⚠️ `--max-items` with `--output text` appends the pagination token (`None`) as a
line of its own; pass that through to `delete-task-definitions` and the whole batch
fails. Filter with `grep '^arn:'`.

**11. The ACM validation CNAME survives the certificate.** `30-ingress` deletes the
cert and the ALB alias record, but `_<hash>.<fqdn>` stays in the hosted zone
forever. Harmless — but leave it and the next deployment's certificate validates
off a record you never created, so an issuance bug goes unnoticed. Delete it with
`change-resource-record-sets` (the DELETE needs the **exact** TTL and value; TTL is
300).

**12. What deliberately survives** anything above: the hosted zone itself, the
`/af-cp/*` secrets if you kept them, **the NAT gateway's Elastic IP**, **the egress
gateway's Elastic IP** (`GatewayEip`, when the stack allocated it for `EgressRoute=gateway`),
**the egress archive bucket** (`EgressArchiveBucket`, with `EgressArchive=create`) and
the `60-engines` models bucket — and nothing else. All of them are `Retain` on purpose (the
addresses so a rebuild egresses from the same ones, see §Egress IP and §Egress gateway; the
buckets so what they hold outlives the stacks), and `teardown.sh` touches none of them. An unattached EIP is billed (~$3.6/month each) until you release it, and the
buckets bill for what they hold. Finishing with a deployment for good is therefore one
extra deliberate step per address and bucket: `aws ec2 release-address --allocation-id
eipalloc-...` for each EIP, and empty then delete each bucket (`aws s3 rb s3://<bucket>
--force`). Sweep to confirm the rest: EC2
instances / volumes / snapshots / AMIs / EIPs, ECS clusters + task definitions,
EFS, RDS instances + manual snapshots, ALB, ACM, NAT, ECR, Cloud Map namespaces,
`/af` log groups, `af-*` IAM roles and instance profiles, non-default VPCs.

⚠️ **ECR cannot be preserved.** `af-control-plane` and `af-workspace` are resources
of `20-platform` with `EmptyOnDelete: true` — the stack delete takes the
repositories *and every image in them*. Redeploying starts from `release-ecr.sh` /
`crane copy` again.

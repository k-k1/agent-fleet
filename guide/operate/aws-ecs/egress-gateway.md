---
audience: "a deployment administrator of an AWS ECS deployment who routes workspace egress through the egress gateway to record and restrict it"
source_of_truth: "`deploy/aws/ecs/cfn/00-network.yaml` (`EgressRoute`, `Gateway*`), `deploy/aws/ecs/pause.sh`, `standup.sh` and `clear-params.sh`, and the Control Plane's `af-cp egress-agent` / `egress-relay`"
---

# Control outbound traffic with a gateway

English | [日本語](egress-gateway.ja.md)

Route the outbound traffic of an ECS deployment's workspaces through the egress gateway.
Every connection is then recorded with its destination's name, and an allowlist can restrict it. Running the allowlist is covered in [Control outbound traffic](../secure/egress.md).

## How it works

| What you do | What happens |
|---|---|
| Allocate the workspace address first and have it allow-listed | The organisation on the other side can admit the address before the switch |
| Deploy `00-network` with `EgressRoute=gateway`, then run `update.sh` | The workspace route points at the gateway, and the Control Plane hands workspaces the proxy |
| Choose the mode and the allowlist in Admin → "**Egress**" | The gateway's agent writes that policy into Squid, records connections and ships them to the Control Plane |
| Pause with `pause.sh` | Workspaces stop, the record is shipped, then the gateway goes to zero instances |

The Control Plane, the slot instances, the inference engines and the home task leave through the NAT gateway either way. Only workspaces go through the gateway.

## How it differs from the NAT gateway

`00-network`'s `EgressRoute` chooses where the default route of the **workspace** route table goes.

| | `nat-gateway` (default) | `gateway` |
|---|---|---|
| Recording | None: Admin → "Egress" stays empty | Every workspace connection, by name (SNI / Host) and address, attributed to the workspace. "Egress" shows counts per host; the attributed events are `GET /api/admin/egress/events` (super_admin), not shown in the Console |
| Allowlist | Not enforced | Log-only, or enforce (a name outside it is refused) |
| Hourly cost | The NAT gateway (about $0.045–0.062/h by region) | The NAT gateway (the Control Plane and hosts still use it) + the gateway instance (`t3.small`, about $0.02–0.03/h) + its Elastic IP ($0.005/h) |
| Per-GB cost | NAT data processing ($0.045–0.062/GB) on all workspace traffic, S3 included | None for workspace traffic (normal data transfer only; same-region S3 is free) |
| Operations | Managed by AWS | One more instance to run. An AMI update replaces it, which leaves workspaces without egress for a few minutes |
| Availability | AWS-managed, one AZ | One instance in one AZ, replaced by an Auto Scaling group of one |
| Non-HTTP egress (SSH git, other ports) | Works | Enforce: only destinations named by `tcp:` entries; the rest dropped and recorded. Log-only (the default mode): passed and recorded as would-deny |
| Workspace source address | `NatEip` | The gateway's own address, a second one to allow-list |

## Under the hood

- An Auto Scaling group of one AL2023 instance in `PublicSubnet0`
- At boot the instance attaches `GatewayEni`, a network interface the stack owns. The workspace route table points at it, its Elastic IP (`GatewayEip`) is the workspace source address, and its private address is the explicit proxy (`EgressProxyAddr`, `:3128`)
- A replacement instance attaches the same interface, so the route, the proxy address and the source address never move
- Squid intercepts port 80 from the workspace subnets and serves the explicit proxy. Intercepted 443 goes to `af-cp egress-relay`, which hands it to Squid as a CONNECT to the address the workspace dialled. Other TCP goes to `af-cp egress-relay --mode tcp`
- Nothing is forwarded (`ip_forward=0`)
- `af-cp egress-agent` (the Control Plane binary, copied out of its image) runs as user `squid`. It writes Squid's allow and mode files from the Control Plane's policy and ships the log to `/internal/egress`, authenticated with `AF_EGRESS_TOKEN`, which the stack generates in Secrets Manager
- The Control Plane gets `AF_EGRESS_PROXY_ADDR` and `AF_EGRESS_TOKEN` (`30-ingress`'s `EgressProxyAddr` / `EgressTokenSecretArn`). Fargate and `ecs-ec2` workspace task definitions carry the proxy variables, with the task-role credential endpoint (`169.254.170.2`), instance metadata (`169.254.169.254`) and the Control Plane's internal host in `NO_PROXY`
- The token never reaches a workspace: it is the agent's credential

## Let non-HTTP TCP through

SSH git and database connections. `GatewaySg` admits all TCP from the workspace subnets; nftables and the relay decide.

| Mode | What happens |
|---|---|
| Enforce | Nothing but HTTP(S) leaves until an administrator adds a **`tcp:` entry**. An unnamed destination is dropped and recorded |
| Log-only (the default mode) | Every port passes and is recorded as would-deny. Nothing closes until you switch to enforce |

- Entries are added in Admin → "**Egress**", or proposed by a member and approved by an administrator. For example `tcp:git.example.com:22`, `tcp:db.example.com:1433`
- A service that redirects clients to other ports can take a range, `tcp:db.example.com:11000-11999`: at most 2048 ports, an exact host or an IPv4 address, never 53, 80, 443, 853 or 3128
- The agent resolves the host every 30 s, and the relay admits connections to the addresses it resolved to in the last 15 minutes, on those ports
- Every connection is recorded by address and port and attributed like the others (the port is part of the destination)
- The VPC, link-local (the metadata service), loopback, this host and ports 53 / 853 are refused in both modes
- UDP and ICMP stay dropped by the security group, unrecorded
- **Before the switch, have the destination's firewall admit the gateway's Elastic IP (the workspace source address) on those ports.** A managed database that redirects to a port range needs that range admitted, and named in the entry
- The relay's log is `/var/log/squid/af-tcp.log` on the instance, and its service is `journalctl -u af-egress-relay-tcp`
- This path has not been run on a real deployment yet

### A denied TCP flow looks connected at first

The redirect happens before the relay decides, so the relay accepts the workspace's connection, reads the original destination (`SO_ORIGINAL_DST`), and only then refuses it by closing the socket without dialling out.

- The workspace's handshake has completed: `nc -z host port` or a bare `connect()` reports success
- A later read or write fails (EOF, or a reset such as `Connection reset by peer`). Which one depends on timing and unread data, and a first small write can still succeed locally
- "Connected" is not evidence of "allowed". Read the relay log, `af-tcp.log`

A connection whose decision could be recorded is a line ending `<verdict> <entry>`.

| verdict | Meaning |
|---|---|
| `deny` | Refused. In enforce, any destination not named by a `tcp:` entry. In **both** modes, a fenced destination (the VPC, link-local, loopback, this host, ports 53 / 853 and the like) and any destination while no policy has been loaded yet |
| `would-deny` | A connection log-only passed |
| `deny-cap` | Refused because the relay's connection limit was full |
| `admit` | Allowed |

- No line is written when the original destination cannot be read or the log cannot be written
- The admin events (`GET /api/admin/egress/events`, "Egress") are built by the Control Plane from that log, asynchronously. They are not the same lines

### The gateway's input chain

`nft list ruleset` on the gateway shows `chain input { ... policy accept; }`, which does not mean the instance is open.

- The `input` chain (`/etc/af-egress/nft.conf`, table `inet af_egress`) accepts, for packets arriving from a workspace subnet on the gateway's interface, only established/related traffic, flows redirected by the `prerouting` chain (`ct status dnat`) and TCP port 3128 (the explicit proxy)
- Anything else from those subnets is logged with the prefix `af-egress-input-drop ` (rate-limited to 10 per second; read it with `journalctl -k`) and dropped by the final `drop` rule
- Every rule matches workspace-subnet sources only, so `policy accept` applies to everyone else (the instance's own traffic, the VPC's)
- The `forward` chain is `policy drop` and `ip_forward` is 0: nothing is routed through the gateway

## Turn it on

An existing stack keeps `nat-gateway` until you do this. Allocate the workspace address first and have it allow-listed ([Register two addresses](#register-two-addresses)).

1. Deploy `00-network`, then run `update.sh`

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM --parameter-overrides EgressRoute=gateway \
     GatewayEipAllocationId=eipalloc-... GatewayEipAddress=<its public address>
   ./update.sh --profile <p> --region <r>    # 20-platform (the token read) and 30-ingress
   ```

2. Wait until the gateway passes traffic (the table below)
3. Running workspaces get the proxy variables on their next start. Until then they are intercepted

Without the two `GatewayEip*` parameters the stack allocates the address itself (and retains it), so you learn the address only once workspaces already egress from it.

The route moves to `GatewayEni` as soon as the first command finishes. After that:

| Stage | Workspace egress |
|---|---|
| Until the instance has attached `GatewayEni` (a few minutes; `/var/log/af-egress-gateway.log` on it) | None |
| Until `update.sh` has written the agent's configuration (`/af-egress/<network stack>/agent`) and the agent has fetched a first policy | Squid refuses everything. A gateway that cannot reach the Control Plane stays closed rather than open |
| After the first policy | As the policy says |

- Until that first policy, `af-egress-install` fails and runs again 30 s later (a missing configuration fails at once; a missing policy after a 30 s wait for the agent). The gap after `update.sh` writes the configuration is about a minute, plus the first pull of the Control Plane image and the Control Plane's own start
- Afterwards it runs every 10 minutes, which is how a new Control Plane image reaches the agent
- After an instance replacement, too, everything is refused until the first policy

The agent is the Control Plane binary copied out of the Control Plane image, so the gateway pulls that image.

- `GatewayInstanceType` must always match `GatewayAmiId`'s architecture
- It has to match the Control Plane image's CPU architecture only when that image is single-arch. With a multi-arch image the gateway pulls the variant for its own architecture, so an x86_64 gateway runs fine next to an arm64 Control Plane (`30-ingress`'s `CpArch`)

## Register two addresses

With the gateway a deployment egresses from two permanent addresses.

| Source | Address |
|---|---|
| The Control Plane, the slot hosts, the inference engines | The NAT gateway's `NatEip` ([Pin the outbound IP address](egress-ip.md)) |
| Every workspace | The gateway's `GatewayEip` |

An organisation that allow-lists the deployment registers both. The workspace address is handled like the NAT's.

### Allocate it before the switch

Then it can be allow-listed while nothing uses it.

```bash
aws ec2 allocate-address --domain vpc --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=Name,Value=af-<network stack>-egress-gateway-eip}]'
```

- Pass the `AllocationId` and `PublicIp` it gives as `GatewayEipAllocationId` and `GatewayEipAddress`
- Both, or neither: the template's Rules refuse one alone
- The ALB admits the gateway by `GatewayEipAddress`, and CloudFormation cannot read the address of an allocation it did not create, so pass the address too
- A handed-in address is never created, replaced or released by the stack

### It survives a rebuild

- An address the stack allocated itself is retained (`DeletionPolicy: Retain`)
- The outputs `GatewayEipAllocationId` and `GatewayEipAddress` carry it whoever allocated it, and `capture-env.sh` fills the two parameters from them when they were captured empty (the same rule as `NatEipAllocationId`)
- With `EgressRoute=gateway`, `standup.sh` checks before building that the allocation still exists, that its address is the captured one, and that nothing but this deployment's gateway interface holds it

### Hand back a retained address

A gateway that was switched back to `nat-gateway`, or a torn-down deployment, leaves its address allocated and unassociated (it keeps billing, $0.005/h). Find it by its tag and pass it with the next switch to `gateway` or the next stand-up, instead of letting the stack allocate a new one.

```bash
aws ec2 describe-addresses --output text --query \
  "Addresses[?Tags[?Key=='Name' && Value=='af-<network stack>-egress-gateway-eip']].[AllocationId,PublicIp,AssociationId]"
# one row with AssociationId None: pass AllocationId and PublicIp as
# GatewayEipAllocationId= GatewayEipAddress= (with EgressRoute=gateway), or write both lines
# into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network before standup.sh
```

> [!CAUTION]
> **Set the two parameters when the gateway is created** (the switch to `gateway`, or a stand-up), not on a stack that already runs it. There the change replaces `GatewayEipAssoc`, so the interface's address is re-associated while workspaces use it. That has not been measured. An address the stack created is already retained without them.

### When you need a single address

For an organisation that cannot register a second address.

| Option | What it means | Status |
|---|---|---|
| Put the gateway in a subnet routed through the NAT gateway | Squid's connections leave from `NatEip`. All workspace traffic pays NAT data processing ($0.045–0.062/GB) again | Not built |
| Move `NatEip` onto the gateway | The NAT needs a new address, so the allow-list changes anyway, after a window with no egress at all | Rejected |

## Go back to the NAT gateway

Not the same two commands in reverse: plan a maintenance window. A running workspace keeps `HTTP(S)_PROXY` pointing at the gateway until it is restarted, and once the gateway is gone every proxy-aware client in it times out. So take the proxy away first.

1. Clear the proxy on `30-ingress`, everything else at its previous value

   ```bash
   ./clear-params.sh --profile <p> --region <r> --stack <ingress stack> EgressProxyAddr EgressTokenSecretArn
   ```

   It builds and prints the change set. Check for a `Modify` of `TaskDef` (the Control Plane task definition) and of `Service`, which follows its new revision, and the two keys going to empty, then run the `--execute` command it prints (`--yes` skips the look).
   - Not `update.sh`: it would hand the gateway's outputs back while the gateway exists
   - Not `deploy --parameter-overrides EgressProxyAddr= EgressTokenSecretArn=`: that did not clear them
2. Stop and start every running workspace (`pause.sh --status` lists them) and check that its task definition no longer has the variables. Their traffic is still intercepted by the gateway meanwhile

   ```bash
   aws ecs describe-task-definition --task-definition <family> --query "taskDefinition.containerDefinitions[0].environment[?name=='HTTPS_PROXY']"
   ```

   Empty means it is gone.
3. Deploy `00-network` with `EgressRoute=nat-gateway` (`--capabilities CAPABILITY_IAM`). The workspace route moves back to the NAT gateway; `GatewayEip` is retained
4. Run `update.sh`. It finds the network's outputs and `30-ingress`'s values both empty and changes nothing else

## Pause it

`pause.sh` stops the gateway through `00-network`'s `GatewayEnabled`: `false` scales the Auto Scaling group to zero instances.

- Never scale it by hand with `aws autoscaling`: the next deploy of the stack undoes it
- `GatewayEni` and `GatewayEip` stay, so `--up` brings back the same route target, proxy address and source address. The Elastic IP keeps billing ($0.005/h)
- While the gateway is off the workspace route **blackholes**. That is harmless only because no workspace runs: do not start one on a stack with `GatewayEnabled=false`

The order on the way down:

1. Workspaces stop. The gateway goes before the Control Plane, and only after two things are confirmed
   - No Workspace task is left in the cluster, whatever its service's desired count (`AF_WS_TASK_WAIT_SEC`, default 600 seconds)
   - With Squid stopped so every tunnel is logged, the Control Plane has accepted the whole log: each log file's size is under the offset the agent committed in `state.json` (`AF_GATEWAY_DRAIN_SEC`, default 300 seconds)
2. The scale-in destroys the instance's disk, so if either cannot be confirmed `pause.sh` fails and leaves the gateway and the Control Plane running

`--up` starts the gateway in the same deploy as the NAT gateway and waits for it twice.

| When | What it waits for |
|---|---|
| Before the Control Plane starts | An instance has attached `GatewayEni`, and Squid and `af-egress-agent` run |
| After the Control Plane is up | The agent's current run has applied a policy (written the mode file and logged `policy applied`, or `reloaded Squid with the policy on disk` for a run that started while the Control Plane was down; both only after Squid's reload succeeded), and the last such line is no older than the policy files |

- Only the Control Plane can give it that policy, and Squid refuses everything until then
- A gateway that does not come up keeps the Control Plane down. `AF_GATEWAY_WAIT_SEC` (default 1200 seconds) bounds each wait
- The checks on the instance run through SSM Run Command. The operator needs `ssm:SendCommand` (document `AWS-RunShellScript`, on the instance tagged `af-egress-gateway=<network stack>`) and `ssm:GetCommandInvocation`. The instance role already has `AmazonSSMManagedInstanceCore`
- `--status` shows the gateway
- `standup.sh` overrides a captured `GatewayEnabled=false` to `true` and waits for the first policy at its end

## Keep the egress record in S3

The Control Plane can write each past UTC day of raw egress events to S3 before it purges them (`AF_EGRESS_ARCHIVE_URL`). It is off until `10-data` owns an S3 bucket for it.

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM            # the S3 endpoint policy admits the bucket
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --parameter-overrides EgressArchive=create
./update.sh --profile <p> --region <r>     # 20-platform's CP grant, then 30-ingress's URL
```

- `standup.sh` does the same on a rebuild when the capture has `EgressArchive=create`
- The bucket is `af-<data stack>-egress-archive-<account id>-<region>`: encrypted, TLS-only, unversioned, and archived days expire after `EgressArchiveRetentionDays` (default 365)
- It is **retained** when the stack is deleted. A rebuild reuses it with `EgressArchive=existing`, which `standup.sh` sets by itself
- The Control Plane task role gets `s3:PutObject` / `s3:GetObject` on `egress/*` and `s3:ListBucket` on that bucket only, and the endpoint policy admits the same bucket for this account
- Only a bucket made this way works. A bucket of your own is refused at the endpoint
- The parameters and the grant are in "The egress archive" in `deploy/aws/ecs/cfn/PARAMETERS.md`
- Not yet run on a real account

## Limits

Squid, the agent, the relay and this configuration, pausing and resuming included, were verified on a test deployment only, **not yet in production**.

- **Raw events are kept 30 days.** The Control Plane deletes raw egress events (and the address intervals that attribute them) older than `AF_EGRESS_EVENT_RETENTION_DAYS` (default 30; `0` keeps them forever). The per-host daily counts stay. The ECS templates have no parameter for it, so a deployment keeps the default. Use the S3 record above to keep the raw days longer
- **Enforce decides on the TLS name (SNI), not on the address.** A workspace can send an allowed SNI to any public address on port 443, through the explicit proxy and on the intercepted path. It cannot reach another port, loopback, link-local or the VPC that way, and a name outside the allowlist is refused whatever address it is sent to. Binding a name to its addresses would mean decrypting, which the gateway never does
- **Intercepted 443 passes `af-cp egress-relay`.** Squid's own intercepting HTTPS port re-resolves the SNI and refuses the connection when the workspace's address is not in its own answer (409, "Host header forgery" in `cache.log`). Names whose DNS answers rotate (S3, ECR, STS) failed that way most of the time
- The relay is in the Control Plane image and starts with the agent, so until the first install a fresh gateway refuses intercepted 443 (it refuses everything until its first policy anyway)
- A new Control Plane image restarts the relay, which cuts the intercepted TLS connections open at that moment
- The relay holds at most 4,096 connections. It closes one 10 s after Squid ended its side, and one whose write stalls for 2 minutes

## Check that the gateway works

On a deployment with `EgressRoute=gateway`:

1. Find the instance and open a shell

   ```bash
   aws ec2 describe-instances --filters Name=tag:af-egress-gateway,Values=<network stack> Name=instance-state-name,Values=running --query 'Reservations[].Instances[].InstanceId' --output text
   aws ssm start-session --target <id>
   ```

2. Boot and services
   - `sudo tail -50 /var/log/af-egress-gateway.log`
   - `systemctl status squid af-egress-agent af-egress-relay af-egress-relay-tcp af-egress-nft af-egress-install.timer`
   - `sudo squid -k parse` (prints the parsed configuration, fails on an error)
   - `ip route show default` (the first default route is the attached interface)
3. The policy
   - `sudo cat /var/lib/af-egress-agent/af-allow.txt /var/lib/af-egress-agent/af-mode.conf` shows the agent's header and the Control Plane's allowlist. If it is still the boot placeholder (`# bootstrap: closed until the first policy`, which refuses everything), no policy has arrived yet
   - `journalctl -u af-egress-agent` shows the reload
4. From a workspace terminal, run `curl -sI https://example.com` (explicit proxy) and `curl -sI --noproxy '*' https://example.com` (intercepted). On the gateway, check:
   - `sudo tail /var/log/squid/af-egress.log` shows one `af1` line each, with the workspace's address, `TCP_TUNNEL` and the SNI
   - `sudo getfattr -n user.af-egress.gen /var/log/squid/af-egress.log` shows the generation, `sudo cat /var/lib/af-egress-agent/state.json` the committed offset
   - Admin → "Egress" shows the host's count, and `GET /api/admin/egress/events` the two events, attributed to the workspace
5. Rotation: `sudo squid -k rotate`, make one more request, and check that the agent finished `af-egress.log.0` and continued on the new file (no duplicate, no gap in the events)
6. Metadata: from a workspace, the request below must be refused. Otherwise a workspace could read the gateway role's credentials

   ```bash
   curl -s -x <EgressProxyAddr> -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token
   ```

   Squid's `to_linklocal` refuses it, and nftables drops Squid's own connections to `169.254.0.0/16`.
7. Enforce: switch the mode in Admin → "Egress", wait for the agent's reload (`af-mode.conf` now has `ssl_bump terminate all`), and check that a name outside the allowlist fails from the workspace (explicit and intercepted) and is logged as denied

## Related

- [Control outbound traffic](../secure/egress.md): running the allowlist and the mode
- [Pin the outbound IP address](egress-ip.md)
- [Stand up, pause and tear down with the scripts](lifecycle.md)
- [Keep costs down](cost.md)

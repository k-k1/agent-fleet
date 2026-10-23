# 0119. Workspace egress is recorded and decided at the network: an egress gateway on the route out, instead of proxy variables a process can ignore

English | [日本語](0119-egress-gateway.ja.md)

- Status: **proposed** (2026-10-23). Documentation only: nothing below is built. The code and
  template facts were read from the tree on this date. Squid's behaviour was read from its
  published documentation and an AWS blog post, **not run**; no Docker and no cloud account were
  available while writing this.
- Follow-ups: #1181, #2603, #2604
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

- The AWS stack owns its VPC and a NAT gateway with a retained Elastic IP, so customers can
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

## Notes

- Squid `ssl_bump`: https://www.squid-cache.org/Doc/config/ssl_bump/ (peek, splice, terminate;
  SNI read without decryption; intercept listener).
- AWS Security Blog, "How to add DNS filtering to your NAT instance with Squid".
- The `peek`/`splice` allowlist form (the published example is a denylist) and the QUIC and
  Encrypted Client Hello behaviour are **not measured**.

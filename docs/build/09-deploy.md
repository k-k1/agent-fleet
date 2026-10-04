---
audience: "someone adding a deployment target or an adapter"
source_of_truth: "the code plus each runbook (`deploy/*/README.md`)"
updated: "2026-10"
---

# 09. Deployment — the forms, the adapters, the environment index

English | [日本語](09-deploy.ja.md)

**The actual commands live in the runbooks and are not duplicated here.** This chapter
is the map: what forms exist, what gets swapped, and which knob controls it. What each
target can and cannot do (several users, per-user limits, engines, cost attribution) is
[ref/deploy-targets](../../guide/ref/deploy-targets.md); how to add a target is
[21](21-add-a-deploy-target.md).

## 9.1 The deployment forms

| Form | Summary | State | Runbook |
|---|---|---|---|
| **local dev** | The CP as a host process. `run-dev.sh` is one entry point with subcommands (`local`, `wsl`, `native`, and `reset`, which wipes the data); `restart-cp.sh` swaps only the CP. `AUTH=dev` for one person, `oauth` when shared | ✅ in use for development and small shared setups | the comments at the top of [run-dev.sh](../../deploy/local/run-dev.sh) and [restart-cp.sh](../../deploy/local/restart-cp.sh); the reflect-a-change table is [10](10-development.md) |
| **wsl (personal)** | `run-dev.sh wsl`: the local form preset for WSL2 with a native dockerd, `AUTH=dev` | ✅ personal use | [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| **native** | No Docker: the CP and Console run as host processes and the workspace runs in a bubblewrap sandbox on a downloaded rootfs. **Single user only** — the `native` runtime refuses to start unless `AUTH=dev` | ✅ shipped as a package | [deploy/native/README.md](../../deploy/native/README.md) |
| **compose** | The self-hosting mainline: a CP container plus Caddy for automatic TLS. The CP binds loopback, and **the compose definition contains the three constraints** of driving the host's Docker daemon from a container: the host network, `DATA_DIR` mounted at the same absolute path, and the docker group id | ✅ | [deploy/compose/README.md](../../deploy/compose/README.md) |
| **aws — ECS** | Static infrastructure from CloudFormation, per-workspace resources from the CP's own ECS adapter. A workspace is a task on Fargate (`ecs`, the template default) or on an EC2 slot taken from a pool (`ecs-ec2`) | ✅ `ecs-ec2` runs the production deployment; `ecs` has been proven in a sandbox from deploy through end-to-end to teardown | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| **aws — ec2-single** | compose on one EC2 VM | ✅ the host of the "start from the release bundle on a clean host" gate | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |
| **kubernetes** | The CP as a Deployment in the cluster; each workspace a StatefulSet at 0 or 1 replicas with two PersistentVolumeClaims, created by the CP's own `kubernetes` adapter over the standard API. Plain manifests with a kustomize base (`deploy/kubernetes/`), and Terraform for what a GKE deployment needs around the cluster (`deploy/gcp/gke/`) | ◐ built, not yet run on a live cluster; the acceptance run on GKE Standard is #1468 | [deploy/kubernetes/README.md](../../deploy/kubernetes/README.md) |

- **ec2-single is compose on a VM** — the `docker` runtime, not a separate profile.
- **kubernetes is one profile for every cluster.** It names no cloud API; what is GKE's — the
  disk type, the node pools, Workload Identity, the load balancer — is chosen by the StorageClass,
  the Terraform and the GKE overlay ([decisions/0106](../decisions/0106-kubernetes-runtime.md) decision 2).
- The authentication modes are [07 §7.3](07-security.md) and are not repeated here.

## 9.2 Ports and adapters — which knob swaps what

**The core is the same artefact on every target**; only interface seams inside the CP
change (the seam list is [01 §1.6](01-architecture.md)). This section is the knobs:

| Seam | Knob | Choices |
|---|---|---|
| `Runtime` / `RuntimeFactory` | `AF_RUNTIME` | empty, `local`, `docker` = Docker Engine (the default) / `ecs`, `aws` = ECS on Fargate / `ecs-ec2` = ECS on EC2 slots from a pool (no alias) / `native`, `wsl` = sandboxed host processes, **which requires `AUTH=dev`** / `kubernetes`, `k8s` = a StatefulSet per workspace on a Kubernetes cluster. **An unknown value fails fast at boot** (`unknown AF_RUNTIME profile`; `runtime.NewFactory`) |
| `Store` | `AF_DB` (the SQLite path) / `AF_DATABASE_URL`, or `AF_DB_HOST` and the other `AF_DB_*` | SQLite (default, pure Go) or Postgres |
| `KeyCustodian` | `AF_MASTER_KEY`, then `AF_KEY_CUSTODIAN` | key set = the local custodian, or AWS KMS with `AF_KEY_CUSTODIAN=kms` + `AF_KMS_KEY_ID` (the master key stays required); unset = no encryption, development only. Vault is 📋 ([decisions/0005](../decisions/0005-envelope-custodian.md)) |
| `AuthGateway` | `AUTH` | `dev` (the default when unset) / `oauth` (what the compose and AWS templates set) / `proxy` ([07 §7.3](07-security.md)) |
| Engines | the engine table: `AF_ENGINES_SSM_PARAM` or `AF_ENGINES_JSON`, plus a row per role from `AF_LLM_URL` / `AF_COMFY_URL` | on AWS, engines the CP starts on demand; anywhere, a server already running on the network, named by URL |
| Ingress / TLS | outside the CP | Caddy (compose) / Tailscale Funnel (local) / ALB + ACM (aws) / the cluster's ingress, on GKE a global external Application Load Balancer through the Gateway API + Certificate Manager (kubernetes) |

## 9.3 Ingress, and the one-way-in invariant

**The invariant: the CP is reachable only through the ingress.** On one host the CP
binds loopback — the image and compose set `CP_ADDR=127.0.0.1:8099`. The code's default
(`:8080`) and `run-dev.sh`'s (`:8099`) bind every interface, which is fine for one
person on a development host and wrong for anything shared. On AWS the CP task binds
`0.0.0.0` inside its own network interface, and its security group admits only the
load balancer's. On `kubernetes` the CP pod binds every interface too, and the main Service
is what the load balancer reaches; on GKE a NetworkPolicy admits only the load balancer to that
port.

**The one exception is the workspace-only listener** ([decisions/0106](../decisions/0106-kubernetes-runtime.md) decision 8).
Where workspaces cannot reach the CP through the ingress — on `kubernetes` they call it back by an
address inside the cluster — the CP serves them on a second port, `AF_CP_INTERNAL_LISTEN` (`:8098`
in `deploy/kubernetes/`), and the internal Service (`af-cp-internal`) targets that port alone; the
CP injects its address as `AF_CP_INTERNAL_URL` next to an unchanged `AF_CP_BASE_URL`. It carries
only the routes the agent calls, each authenticated by its own per-membership bearer token
(`workspaceRoutes` in `control-plane/workspace_listener.go`), and answers 404 for everything
else: the Console, the admin API, the login routes and the egress proxy's `/internal/egress*`.
It has no auth gate, drops the identity header and every forwarding header, and takes the
connection's own address as the client whatever `AF_TRUSTED_PROXY_HOPS` says. Nothing a browser
or an administrator uses is reachable another way. Unset, there is no second port and nothing
changes.

The ingress terminates TLS and forwards. With `AUTH=oauth` the CP authenticates for
itself; only with `AUTH=proxy` does the ingress inject the identity header.

| Ingress | When | Notes |
|---|---|---|
| **Caddy** | the compose default | point the DNS for `PUBLIC_DOMAIN` at it and certificates are obtained and renewed automatically, WebSockets included. Caddy and the CP both use the host network, so it reaches the CP's loopback port. A site with its own proxy can drop it (the second alternative in the `Caddyfile`) |
| **Tailscale Funnel** | one local form | Funnel → `127.0.0.1:8099` directly |
| **ALB + ACM** | aws | TLS only — authentication stays in the CP. `30-ingress.yaml`'s `AuthMode` allows `oauth` (the default) or `dev`; the templates configure no load-balancer OIDC |
| **Global external Application Load Balancer + Certificate Manager** | kubernetes on GKE | built by GKE's Gateway controller (class `gke-l7-global-external-managed`; the classic one closes even an active WebSocket at the backend timeout), with the address, certificate and DNS from `deploy/gcp/gke`. Two hops in `AF_TRUSTED_PROXY_HOPS`: it appends `<client>, <load balancer>`. The backend timeout is raised so an idle terminal is not cut at 30 s |

- **Whenever the ingress changes, `PUBLIC_BASE_URL` must change with it** — it is what
  the OAuth redirect is built from, and the `https` prefix is what makes a Secure cookie
  possible.
- **Every proxy in front of the CP is one hop in `AF_TRUSTED_PROXY_HOPS`** (default 0 =
  trust `RemoteAddr`). The compose example env and the AWS template set 1; a CDN in
  front of the ALB makes it 2. The client address that tenant network restrictions see
  depends on it ([07 §7.3](07-security.md)).

## 9.4 Environment variable index

**The values, how to generate them and the caveats live in the annotated example env
files** ([compose](../../deploy/compose/.env.example),
[local](../../deploy/local/oauth.env.example)); the AWS templates set their own from
stack parameters ([PARAMETERS.md](../../deploy/aws/ecs/cfn/PARAMETERS.md)). This is only
an index. The value in parentheses is the code's default when the variable is unset.

| Group | Variables | Role | Detail |
|---|---|---|---|
| CP core | `CP_ADDR` (`:8080`) · `CONSOLE_DIR` · `AF_RUNTIME` (`local`) · `AF_DB` (`<WS_DATA>/control-plane.db`) · `PUBLIC_BASE_URL` · `AF_PREVIEW_DOMAIN` · `AF_TRUSTED_PROXY_HOPS` (0) | where it listens, what it serves, which adapter, the public URL, preview subdomains, the client address | this chapter |
| Workspace listener | `AF_CP_INTERNAL_LISTEN` (unset = none) · `AF_CP_INTERNAL_URL` (unset = workspaces use `AF_CP_BASE_URL`) | the workspace-only port and the URL workspaces reach it by (`deploy/kubernetes/`: `:8098` and `http://af-cp-internal.<cp-ns>.svc:8098`). The clone URLs and `AF_INTERNAL_GIT_HOST` stay public; the Agent rewrites the workspace's git onto the URL (`url.<internal>/git/.insteadOf`) with the git credential stored for both hosts, and LFS answers on the workspace listener point back at it. It takes effect only with `PUBLIC_BASE_URL` set, and from the next workspace Start | §9.3 / [decisions/0106](../decisions/0106-kubernetes-runtime.md) |
| Workspace template | `WS_IMAGE` · `WS_DATA` (`/tmp/af-data`) · `WS_MEMORY` (`1g`) · `AF_MAX_WORKSPACE_MEM` · `WS_AGENT_PORT` (7700, the base of the per-workspace ports) · `WS_AGENT_HOST` (`127.0.0.1`) · `WS_JVM_DIR` · `WS_ENV` · `WS_SESSION_CMD` | the common template the CP fills in when starting a workspace. `WS_ENV` reaches `docker` and `native` workspaces only; the ECS runtimes do not pass it on | [04](04-agent.md) |
| L1 auth | `AUTH` (`dev`) · `DEV_USER` (`dev`) · `AUTH_EMAIL_HEADER` (`X-Forwarded-Email`) · `GOOGLE_OAUTH_CLIENT_ID/SECRET` · `AF_GITHUB_LOGIN_CLIENT_ID/SECRET` (or `GITHUB_OAUTH_CLIENT_ID/SECRET`) with `AF_GITHUB_ALLOWED_ORGS` and the other `AF_GITHUB_*` · `AF_OIDC_PROVIDERS` + `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST,LABEL_JA,LABEL_EN,SCOPES,PROMPT,LINK_CLAIM,ALLOWED_EMAILS,ALLOWED_DOMAINS,ALLOWED_TIDS}` · `AF_COOKIE_SECRET` · `AF_SESSION_TTL` (168h) · `AF_OAUTH_ALLOWED_{EMAILS,DOMAINS,EMAILS_FILE}` | Console login, with `AUTH=oauth` refusing to boot without a working provider. An OIDC provider must declare `TRUST`, and GitHub needs `AF_GITHUB_ALLOWED_ORGS`; either is disabled otherwise. **A sign-in is refused unless some way in admits it**: these allowlists, a tenant's roster, a tenant's auto-join domains or an approved tenant IdP. With none of them, every login is denied | [07 §7.3](07-security.md) / [decisions/0043](../decisions/0043-login-idp.md) |
| Provisioning and roles | `AF_PROVISION` (`auto`) · `SUPER_ADMIN_EMAILS` | how an unknown identity is admitted; who is a deployment administrator | [06](06-data.md) |
| At-rest encryption | `AF_MASTER_KEY` | unset means plaintext (development only). **Losing it is a crypto-shred** — keep it in a vault separate from the data | [07 §7.6](07-security.md) |
| Git provider OAuth | **there are none** | a tenant administrator registers the apps in the Console. `BITBUCKET_OAUTH_KEY/SECRET` are no longer read, and `GITHUB_OAUTH_CLIENT_ID` is for sign-in only | [decisions/0052](../decisions/0052-tenant-git-oauth.md) |
| Scale-to-zero and showback | `AF_AUTOSTART` (on) · `AF_SESSION_IDLE_TIMEOUT` (1h) · `AF_INTERACTION_IDLE_TIMEOUT` (the session value) · `AF_WS_IDLE_TIMEOUT` (2h) · `AF_PRESENCE_IDLE_TIMEOUT` (30m) · `AF_IDLE_SWEEP_INTERVAL` (1m) · `AF_STOP_GRACE_SEC` (30, at most 120) · `AF_USAGE_SAMPLE_INTERVAL` (5m) | auto-start, idle stop, the grace period, usage sampling. For the idle timeouts, the sweep and the usage sampler, `0` means off | [03](03-control-plane.md) |
| MCP | `AF_MCP_ENABLED` | whether `/mcp` exists at all; only the exact string `true` enables it | [08](08-integrations.md) |
| Egress | `AF_EGRESS_LISTEN` (`:3128`) · `AF_EGRESS_TOKEN` · `AF_EGRESS_{INGEST,POLICY}_URL` · `AF_EGRESS_PROXY_ADDR` · `AF_EGRESS_ENFORCE` · `AF_EGRESS_ALLOWLIST` | the forward-proxy subcommand and the CP's aggregation. `AF_EGRESS_PROXY_ADDR` injects the proxy variables into `docker` and `native` workspaces only | [07 §7.8](07-security.md) |
| Postgres | `AF_DATABASE_URL`, or `AF_DB_{HOST,PORT,USER,PASSWORD,NAME,SSLMODE}`, and **where the password really lives**: `AF_DB_PASSWORD_SECRET_ARN` / `AF_DB_PASSWORD_SECRET_KEY` | only when the store is Postgres. The parts are composed into a DSN; the ARN is what lets a rotated password be picked up without replacing the task (§9.9) | [06](06-data.md) |
| ECS adapter | `AF_ECS_{CLUSTER,REGION,SUBNETS,SECURITY_GROUP,NAMESPACE_ARN,EFS_ID,EXEC_ROLE,TASK_ROLE,INFRA_ROLE,LOG_GROUP,WORKSPACE_IMAGE,TASK_CPU,TASK_MEMORY,WS_DISK_GB,POSIX_UID,POSIX_GID,START_TIMEOUT_SEC}` | the coordinates of the static infrastructure the templates built. Read by `ecs` and `ecs-ec2` alike | [ecs runbook](../../deploy/aws/ecs/README.md) |
| EC2 slot pool | `AF_ECS_EC2_LAUNCH_TEMPLATE` (required) · `AF_ECS_EC2_SLOT_TYPES` · `AF_ECS_EC2_DEFAULT_SLOT_CLASS` · `AF_ECS_EC2_AMI_ARM64` · `AF_ECS_EC2_MAX_SLOTS` (8) · `AF_ECS_EC2_HOME_GB` (50) · `AF_ECS_EC2_SLOT_SLEEP_SEC` (900) · `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` (0 = never) · `AF_ECS_EC2_HIBERNATE_AFTER_SEC` (0 = off) · `AF_ECS_EC2_BACKUP_EVERY_SEC` (0 = off) · `AF_ECS_EC2_BACKUP_KEEP` (3) · `AF_ECS_EC2_GOLDEN_AUTOBAKE` (on), and the sweep and timing knobs `AF_ECS_EC2_*_SEC` | `ecs-ec2` only: the slot types and cap, the home size, and the idle tiers of §9.5 | [ecs runbook](../../deploy/aws/ecs/README.md) §Optional: EC2 slot pool / [decisions/0045](../decisions/0045-ec2-persistent-workspace.md) |
| Workload AWS identity | `AF_WS_WORKLOAD_AWS` (off) | `1` hands the ECS task role back to sessions and terminals and stops suppressing the SDKs' instance-metadata lookup. It never removes a network protection (`ECS_AWSVPC_BLOCK_IMDS`, IMDS hop limit 1, a `DOCKER-USER` reject), so it does not give a docker workspace the host's instance profile; that needs a credential path the operator permits separately. Off, the Agent unsets `AWS_CONTAINER_CREDENTIALS_*` / `AWS_CONTAINER_AUTHORIZATION_TOKEN*` and sets `AWS_EC2_METADATA_DISABLED=true` for everything it starts (on docker the CP starts the container with it), so a tool with no member profile fails instead of running as the workload. Set on the CP; it reaches every runtime at the next workspace Start (docker: a running container keeps its environment until Stop → Start) | [guide member/10](../../guide/member/10-integrations.md) |
| Engines | `AF_ENGINES_SSM_PARAM` / `AF_ENGINES_JSON` · `AF_LLM_URL` · `AF_COMFY_URL` / `AF_COMFY_API_KEY` · `AF_ENGINE_API_KEY_<KEY>` · `AF_ENGINE_<KEY>_{CONTROL_INTERVAL_SEC,WINDOW_SEC,IDLE_SEC,START_DEADLINE_SEC,FAIL_COOLDOWN_SEC}` · `AF_ENGINE_ECS_CLUSTER` · `AF_ENGINE_WAKE_TIMEOUT` (900 s) · `AF_ENGINE_PLAIN_HOLD` · `AF_REMOTE_ENGINE_{URL,TOKEN,KEYS}` | the engine table, the engine controller, the gateway's hold on a cold engine, and borrowing another deployment's engines | [decisions/0071](../decisions/0071-self-hosted-inference-engines.md) / [0076](../decisions/0076-external-image-engine-on-lan.md) / [0077](../decisions/0077-engine-boxes-bought-by-cp.md) / [0079](../decisions/0079-remote-engine-from-another-deployment.md) |
| Speech | `AF_VOICEVOX_URL` (`http://127.0.0.1:50021`) · `AF_TTS_ECS_SERVICE` and the other `AF_TTS_ECS_*` · `AF_TTS_MAX_CHARS` (300) · `AF_POLLY_{REGION,ENGINE}` | a VOICEVOX by URL, or the one on ECS that the CP scales from zero; Amazon Polly | [decisions/0070](../decisions/0070-tts-ondemand-engine.md) |
| Containerless adapter | `AF_NATIVE_AGENT_BIN` (`workspace-agent` on `PATH`) · `AF_NATIVE_ROOTFS` · `AF_NATIVE_BWRAP` | where the agent binary lives; the rootfs that switches on the bubblewrap sandbox | [native runbook](../../deploy/native/README.md) |
| Kubernetes adapter | `AF_K8S_NAMESPACE` · `AF_K8S_WORKSPACE_IMAGE` · `AF_K8S_STORAGE_CLASS` · `AF_K8S_HOME_GIB` · `AF_K8S_STATE_GIB` · `AF_K8S_IMAGE_PULL_SECRET` · `AF_K8S_NODE_SELECTOR` · `AF_K8S_SERVICE_ACCOUNT` (`default`) | `kubernetes` only: the workspace namespace, image and StorageClass, the claims' default sizes, the node pool, and the workspaces' way back to the CP (the workspace listener row, §9.3) | [kubernetes runbook](../../deploy/kubernetes/README.md) / [decisions/0106](../decisions/0106-kubernetes-runtime.md) |
| Inside the workspace (injected by the CP; **an operator never sets these**) | `AGENT_TOKEN` · `AF_SECRET_KEY` · `AGENT_STOP_GRACE_SEC` · `AGENT_SESSION_CMD` · `CLAUDE_CONFIG_DIR` · `AF_AGENT_SELF_UPDATE_ALLOWED` · `AF_CP_BASE_URL` with the per-feature tokens (`AF_DOCS_TOKEN`, `AF_MCP_TOKEN`, `AF_MEMO_TOKEN` …) · on `native` also `AGENT_ADDR`, `AF_TMUX_SOCKET` and `AGENT_DOCS_DIR` · where set (on `kubernetes`) also `AF_CP_INTERNAL_URL`, which requests prefer over `AF_CP_BASE_URL` while links for people keep the public one | CP ↔ agent authentication, the DEK, the grace period, the agent's routes back to the CP (`manager.workspaceExtraEnv`). The token and the DEK travel as a 0600 env file on `docker`, as SSM SecureString task secrets on ECS, and as a per-workspace Secret on `kubernetes` | [04](04-agent.md) / [07 §7.5](07-security.md) |

How to check this index is complete: **the variable names are their own grep anchors.**
Cross-check what the CP reads (`envx.Or`, `envx.DurationOr`, `runtime.EnvInt`,
`os.Getenv`) against the example env files. `run-dev.sh` names what it passes in its
`exec env` block, but that block is not a filter — every exported variable reaches the
CP — and it sets its own defaults (`CP_ADDR=:8099`, `WS_MEMORY=5g`).

**`0` does not mean "off" everywhere.** The idle timeouts, the idle sweep and the
background loops documented as switchable (`AF_USAGE_SAMPLE_INTERVAL`,
`AF_CLOUD_COST_INTERVAL`, `AF_GIT_GC_INTERVAL`, `AF_SCHEDULER_INTERVAL`) are read with
`intervalOff` and parse `0` as off; any other duration read with `envx.DurationOr`
(`AF_SESSION_TTL`, `AF_SCHEDULE_SETTLE` …) treats `0` as unset and uses the default.

**How a JDK is provided differs by runtime — never assume `/usr/lib/jvm` is populated.**
`WS_JVM_DIR` is bind-mounted read-only at `/usr/lib/jvm` on `docker` and on `native` in
its rootfs mode; the ECS runtimes have no such mount, so that directory can be empty.
The runtime-independent answer is `~/.local/share/agent-fleet/jvm` on the home volume,
which `workspace-agent install-jdk <major>` fills with Temurin from Adoptium. Choosing a
Java version in the Console makes the entrypoint install any missing one and put it on
`JAVA_HOME`. `GET /env/toolchains` offers what is on disk in either directory plus what
can be installed (`java_available`); **choosing one that is not installed offers a
button that installs it there and then** (`POST /env/jdk-install`, then poll with
`GET`). After installation `resolvedToolchains` globs the directories at every start,
so it takes effect **from the next session** without a restart.

## 9.5 The AWS target

Seven CloudFormation stacks, in deploy order `00-network → 10-data → 20-platform →
(40-ec2-pool) → (50-tts) → (60-engines) → 30-ingress`; the ones in parentheses are
optional. The runbook's "Stack decomposition" says what each one owns.

- **The ownership boundary**: the templates build **static infrastructure once**.
  Per-workspace resources are **created at runtime by the CP under deterministic names**
  — the adapter is stateless (everything is found by name or tag) and the templates
  never churn.
- **The common mapping**: one workspace = one ECS service at desired 0 or 1
  (scale-to-zero); the agent token and the DEK are SSM SecureString parameters, **so the
  DEK appears in the task definition only as a reference and never as plaintext**; the
  CP reaches the agent over Service Connect; the CP itself is a Fargate service in
  `30-ingress` at `desiredCount 1`, with RDS Postgres as its store.
- **`ecs` (Fargate)**: the home is an EFS access point with a fixed root and uid/gid.
  Fargate keeps no image cache, so every start pulls the image cold.
- **`ecs-ec2` (the EC2 slot pool, [decisions/0045](../decisions/0045-ec2-persistent-workspace.md))**:
  - **A slot is an EC2 instance serving one member at a time.** The CP buys it itself
    (`RunInstances` from `40-ec2-pool`'s launch template — no Auto Scaling group, no
    capacity provider) up to `AF_ECS_EC2_MAX_SLOTS`, and pins the task to it with an
    `ec2InstanceId ==` placement constraint. The slot's root volume is the image cache.
  - **The home is the member's own gp3 EBS volume.** The CP attaches it at `/dev/sdf`
    and mounts it over SSM. Credentials (`/var/lib/af/claude`) and the `keep` area stay
    on EFS.
  - **A new home starts from a golden snapshot** — a home that has already run the
    boot-time install, baked by the CP whenever the workspace image changes and refused
    when it does not match the running image.
  - **Idle is tiered.** Stop sets desired 0 and leaves the home attached, so the member
    returns to the same slot. After `AF_ECS_EC2_SLOT_SLEEP_SEC` a free slot is stopped
    (the root volume still bills). After `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` it is
    terminated, the home detached first. After `AF_ECS_EC2_HIBERNATE_AFTER_SEC` (or a
    tenant's own limit) the home is snapshotted and the volume deleted; the next start
    restores it. `AF_ECS_EC2_BACKUP_EVERY_SEC` takes a spare snapshot of a home in use,
    the only way back from losing an Availability Zone, since an EBS volume cannot leave
    its zone.
  - **Why a deployment chooses it: I/O, a home that really persists, and sizes above
    Fargate's ceiling — not start time.** Measured through the adapter, a warm start
    takes 43–110 s against Fargate's ~105 s; small-file writes on the EBS home are 8–30×
    faster than on EFS.
- **Every adapter reports the Runtime contract's `starting` state** while a start is
  converging, and callers neither re-start nor idle-stop it then. The local adapters
  (`docker`, `native`) report it while Start's marker is armed and the agent has not
  answered `/healthz` yet: Start waits for the agent only for a grace (the adapter's
  default, or `AF_AGENT_HEALTH_WAIT_SEC`) and then returns, and `State` keeps saying
  `starting` until the agent answers or the marker's deadline passes — the longer of
  `AgentBootBudget` and that grace, counted from when Start armed it
  (`runtime_health.go`). **On ECS, Start returns without waiting for the agent**: on
  `ecs` once the service's desired count is set, and on `ecs-ec2` possibly earlier — when
  the slot is still starting, waking or registering, the placement finishes in the
  background (`finishStart`) and the claim on the home keeps the state at `starting`.
  Either way the Console observes convergence by polling `GET /api/workspace`. A synchronous wait cannot come
  back: a cold start outlives the load balancer's 60 s idle timeout and turns into a
  504.
- **The Fargate start has been broken down**: of a ~101 s warm-home restart, the image
  pull is ~35 s, so lazy image loading (SOCI) was rejected; the rest is task creation,
  the network interface, the EFS mount and the entrypoint.
- **Engines are separate stacks.** `50-tts` is VOICEVOX on Fargate, scaled from zero by
  the CP. `60-engines` is llama.cpp and ComfyUI as ECS services on GPU instances that the
  CP buys with EC2 Fleet when a request arrives. Neither depends on the workspace
  runtime; the CP finds them through the engine table.
- The cost characteristics are §9.8.

## 9.6 Parity and differences

The workspace image and the agent are the same artefact on every target — that is the
point of the split. The capability matrix is
[ref/deploy-targets](../../guide/ref/deploy-targets.md); what follows is the
substrate underneath it.

| Aspect | docker / compose | native | ecs (Fargate) | ecs-ec2 | kubernetes |
|---|---|---|---|---|---|
| Scale-to-zero | stop / start the container | stop / start the processes | desired 0/1 | desired 0/1, then the idle tiers of §9.5 | replicas 0/1 |
| Isolation | the container boundary, sharing a kernel | a bubblewrap sandbox; one user | a task with no shared host | a task on an instance no one else uses at the same time | a pod under the `restricted` Pod Security Standard, sharing a node's kernel with other workspaces |
| Egress | the container network, optionally the forward proxy ([07 §7.8](07-security.md)) | the host's | security groups | security groups | NetworkPolicies, then the cluster's NAT (Cloud NAT on GKE) |
| Home storage | a local directory, fast | a local directory | EFS: **metadata-heavy work such as git is slow** | EBS; credentials on EFS | a block-storage volume per workspace, zonal; logins and Claude's state on a second one |
| Infrastructure privilege | the Docker socket is host-root equivalent ([07 §7.1](07-security.md)) | the user's own account | a minimal task role, no instance metadata | a minimal task role | a Role in the workspace namespace and a read-only ClusterRole; no cloud identity for workspaces |

**The idle logic is common**; each Runtime absorbs how "stopped" is implemented.

## 9.7 Backup, restore and upgrade — the assumptions

- **On one host, `WS_DATA` (`DATA_DIR` in compose) is everything you must preserve**:
  the database, every user's home including the encrypted store `secrets.enc`, the
  plaintext agent state, the wrapped DEKs, and the Caddy certificates. Only what can be
  re-provisioned (`shared/jvm`) is excluded.
- **`AF_MASTER_KEY` goes in neither the data directory nor the backup** — keep it
  separately. Losing it makes every backup undecryptable. Conversely, **the archive
  contains plaintext agent state, so the archive itself needs protecting.**
- A restore may land under a different parent path: the CP re-points at start, **but the
  basename is a contract**.
- **Upgrades apply the embedded migrations automatically at start and cannot be
  downgraded** — always back up first.
- **On AWS `WS_DATA` holds nothing** (`30-ingress` points it at `/tmp`). The state is
  RDS, EFS and, on `ecs-ec2`, the members' EBS homes, and they are not protected alike —
  **the templates declare backups for RDS and EFS only with `Persistence=retain`, and
  the EBS home backups are off by default.** Retaining a resource when its stack is
  deleted is not a backup:
  - RDS: `Persistence=retain` in `10-data` turns on 7-day automated backups, a final
    snapshot and deletion protection.
  - EFS: `Persistence=retain` keeps the file system when the stack is deleted and adds
    a daily AWS Backup plan (points kept `EfsBackupRetentionDays`, 7 by default) in a
    vault of its own. Restores are manual — one member's directories or the whole file
    system, into a directory beside the live data and copied back: the ecs runbook's
    §EFS backup and restore.
  - EBS homes (`ecs-ec2`): only the optional home backups of §9.5
    (`AF_ECS_EC2_BACKUP_EVERY_SEC`, off by default).
- **On ECS an upgrade is not only the application's tag.** A release can also need a new
  ECR repository and an image nothing has copied in yet (the engines' fetch and ingest
  steps live in `af-engine-tools`), so `update.sh` holds one order: **the repository
  (20-platform, through a change set it prints and executes only when nothing is
  replaced) → the image (`crane copy` from GHCR) → the stack that names it
  (60-engines)**. Reversed, nothing fails at the time: the stack deploys and the fetch
  containers sit in `CannotPullContainerError` while the service reports a steady state.
  What the script deliberately does not do is *bake* an image — if GHCR has not got the
  tag either it stops and names the workflow that makes it.
- The actual procedures (`backup.sh`, `restore.sh`, upgrade, air-gapped) are the
  [compose runbook](../../deploy/compose/README.md) and, for ECS, the
  [ecs runbook](../../deploy/aws/ecs/README.md) §Upgrade.

## 9.8 Cost characteristics

The AWS forms bill in a different **shape**. A single VM is **near-flat regardless of
headcount**; ECS is **a standing floor plus headcount × hours**, where scale-to-zero does
the work. **The choice follows that shape**; the absolute numbers below only support
it.

> **Assumptions**: unless marked *measured*, these are list prices from the AWS Pricing
> API for ap-northeast-1 (Tokyo), 730 h/month, as of 2026-08 — the ecs runbook's "Cost
> & ephemerality" and [decisions/0045](../decisions/0045-ec2-persistent-workspace.md).
> us-east-1 is roughly 30% lower. No reserved instances or savings plans are applied.
> **The agent subscriptions are each user's own and are not included at all.**

### 9.8.1 A single VM — flat

| Item | Monthly | Note |
|---|---|---|
| The instance, a disk of 30 GB and a static IP | ≈ $87 (t3.large, the default) / ≈ $47 (t3.medium) | the template offers t3.medium, t3.large and t3.xlarge |
| DNS zone | $0.50 | the template needs an existing Route53 hosted zone |
| **Total** | **≈ $88/month** | **it does not change as people are added** — until the RAM runs out |

**RAM is the limit, not CPU.** Subtract what the CP, Caddy and the OS need and
divide what is left by `WS_MEMORY`: that is how many workspaces can run at their limit
at once. The compose example sets 5g, which leaves room for one on a t3.large, so a VM
for a team is sized for everyone's peak — and the runbook tells you to lower the limit
on a t3.medium.

Properties to watch:

- **A burstable instance family drops to its baseline** once the CPU credits are gone.
  Sustained heavy builds want a fixed-performance family.
- **Scale-to-zero does not help.** Idle-stop only stops the workspace containers; the VM
  is still billed. **Cutting weekend cost means stopping the VM itself.**
- **It is a single point of failure**, and the only isolation is the container boundary.

### 9.8.2 ECS — a floor plus usage

**The standing floor, payable with every workspace stopped:**

| Item | Monthly | Note |
|---|---|---|
| NAT gateway | $45 | workspaces reach git and the model APIs through it, and it is on the start path (ECR, logs, SSM). A NAT instance (≈ $8) is the biggest single lever |
| The CP task, 24/7 (0.5 vCPU / 1 GB) | $23 | |
| Database (RDS db.t4g.micro, 20 GB) | $21 | why state survives a CP task replacement |
| Load balancer | $18 | plus usage |
| Secrets, service discovery, registry | ≈ $1 | |
| EFS | by usage | every home on `ecs`; credentials only on `ecs-ec2` |
| EFS backups | by usage | `Persistence=retain` only: $0.06/GB-month of backup storage, 7 daily points |
| **Floor** | **≈ $107/month + EFS** | |

**Per workspace:**

| | `ecs` (Fargate, 1 vCPU / 2 GB) | `ecs-ec2` (m7i.large, 2 vCPU / 8 GB) |
|---|---|---|
| Running | $0.0616/h | $0.130/h (m7i is $0.0651 per vCPU-hour at every size) |
| Weekdays, 8 h × 22 days | ≈ $11 | ≈ $23 |
| 24/7 (idle stop not working) | ≈ $45 | ≈ $95 |
| While stopped | EFS, for what the home uses | the home's EBS, for what it **provisions** ($0.096/GB-month; 50 GB = $4.80), plus the slot's root volume until the slot is terminated (100 GB = $9.60) |

- **The 24/7 row is also the bill when scale-to-zero breaks.** Whether the idle settings
  actually work is therefore a **cost** concern as much as an operational one.
- **EBS bills what is provisioned, EFS what is used**: the break-even is a home 26.7%
  full ($0.096 / $0.36). Hibernation turns a 50 GB home with 20 GB in it from $4.80 into
  a $1.00 snapshot.
- **EFS I/O is a line of its own**, and on `ecs-ec2` too, because the credentials stay
  there. On the production deployment, one day of *measured* elastic-throughput I/O
  (2026-09-17, CloudWatch × unit price) came to about $0.10 per workspace-hour, which is
  about $135/month once multiplied by an *estimated* 1,341 workspace-hours a month. The
  CloudWatch method itself was checked against Cost Explorer on the previous day's
  window, where the two agreed to 0.12%
  ([decisions/0087](../decisions/0087-efs-metadata-io.md), which also records the
  fixes that had not reached production yet).
- **A GPU engine left running dwarfs everything else**: a g6.xlarge is $1.26/h (≈ $918
  a month). The CP starts engines on demand and stops them when idle, and `pause.sh`
  sweeps any engine instance still alive.
- **Most of a small deployment's bill is the floor, not people.** *Measured* on a sandbox
  over 2026-08-01 to 16: at most 22.3% of the bill could be attributed to a member; the
  rest was NAT, DNS, tax, EFS, the CP, the load balancer, the database and public IPv4
  ([decisions/0048](../decisions/0048-member-cloud-cost.md)). On `ecs-ec2` the Console
  shows each member's actual spend from cost allocation tags; the tagging for `ecs`
  ships unverified on real hardware.

### 9.8.3 Choosing between them

- **One or two people, or a team that fits one VM → ec2-single** (compose on AWS). It is
  cheaper outright, and a VM for everyone's peak stays flat.
- **ECS catches up on cost at roughly 8–10 concurrent users** (an estimate from the list
  prices above), where the VM has to be sized for everyone's peak around the clock and
  ECS bills only the hours each workspace runs.
- **Below that, you choose ECS for what it gives regardless of price**: task-level
  isolation, per-user fault isolation, rolling image replacement, and a tighter
  metadata and role posture (§9.6). **Scale-to-zero is not what makes it cheap — it is
  what dilutes the floor.**
- **Between the two ECS runtimes**, `ecs-ec2` costs more per running hour for a bigger
  box, and buys I/O, persistence and size (§9.5). It also adds four more resources per
  workspace to operate.
- **Check the estimates against the actual bill** — Cost Explorer, and on `ecs-ec2` the
  Console's per-member cost view.

## 9.9 Health, readiness, and a credential that moves

**`/healthz` is liveness only.** It writes the literal `ok` and touches nothing else —
no database, no filesystem, no adapter. `deploy/local/restart-cp.sh` compares its body
to that string verbatim, and the ALB target group health-checks it. Treat it as a frozen
contract.

**`/readyz` is the one that consults the store.** It pings the metadata store with a two
second budget and answers `503 database unavailable` when it cannot. It is reachable
without a session, because a monitor cannot sign in, and the body deliberately carries
nothing an unauthenticated caller should not see.

**The ALB stays on `/healthz` on purpose.** Pointing it at `/readyz` would let a
momentary database unavailability kill the CP task, and the CP runs at `desiredCount 1`
— a permanent restart risk in exchange for a self-heal the CP now performs by itself
([decisions/0065](../decisions/0065-db-credential-rotation.md)).

### Why the database password cannot be an environment variable on RDS

An ECS task definition's `secrets` block is resolved **once, when the task starts**. RDS
rotates its managed master password on a schedule (seven days by default), so a
long-running task ends up presenting a password the database has stopped accepting, and
every query fails with `28P01`. **Nothing about this is visible from outside**: the
process is up, so `/healthz` is `ok`, so the target is healthy, so the service is at
steady state.

So the CP treats the injected value as a bootstrap hint and re-reads
`AF_DB_PASSWORD_SECRET_ARN` from Secrets Manager when Postgres refuses it, retrying
inside the connector. Two things have to be true for that to work:

1. **`CpTaskRole` — not the execution role — needs `secretsmanager:GetSecretValue`.** The
   execution role's copy is what injects the variable at start and does nothing
   afterwards. When the task role lacks it, the CP keeps running on the injected value
   and logs `DB_SECRET_REFRESH_FAILED`; the mechanism is gone but nothing breaks until
   the next rotation.
2. **`AF_DB_PASSWORD_SECRET_ARN` has to be set.** Unset means the injected value is all
   there is — correct for compose, on-prem and SQLite, and a latent outage on RDS.

### Making it audible

The CP logs `DB_UNAVAILABLE` when it cannot open a connection. `30-ingress.yaml` turns
that into the CloudWatch metric `AgentFleet/<stack>/DbUnavailable` **unconditionally**,
and `CpAlarmEmail` — empty by default — subscribes an address to an alarm on it.

**Set it.** On 2026-09-01 the only record that a production deployment was returning 500
to every caller was a line in a log group nobody had reason to open. Recovery of last
resort, four minutes and no interruption because it is blue/green:

```sh
aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
```

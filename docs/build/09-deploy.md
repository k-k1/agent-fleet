---
audience: "someone adding a deployment target or an adapter"
source_of_truth: "the code plus each runbook (`deploy/*/README.md`)"
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
| `AuthGateway` | `AUTH` | `oauth` (what the compose and AWS templates set) / `proxy` ([07 §7.3](07-security.md)) / `dev`. No default: outside `native`/`wsl` an unset `AUTH` is refused, and `dev` there needs `AF_ALLOW_INSECURE_DEV_AUTH=1` |
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

On `docker` (compose) it is optional, for a host whose public front end does not accept the
Docker bridges (a tunnel or reverse proxy bound to one host interface): the CP binds the docker0
gateway address (`AF_CP_INTERNAL_LISTEN=172.17.0.1:8098`), and workspaces reach it as
`AF_CP_INTERNAL_URL=http://host.docker.internal:8098`, a name the adapter maps with
`--add-host host.docker.internal:host-gateway` whenever the URL uses it. The docker0 address is
local to the host, so every per-user network reaches it without crossing Docker's inter-network
isolation, and no LAN or tailnet interface carries it; a wildcard bind on `docker` is logged as a
warning at start. It is off by default (the address depends on the host's Docker and firewall
configuration, and an address that does not exist stops the CP). Runbook:
[`deploy/compose/README.md`](../../deploy/compose/README.md) "Workspace listener".

The ingress terminates TLS and forwards. With `AUTH=oauth` the CP authenticates for
itself; only with `AUTH=proxy` does the ingress inject the identity header.

| Ingress | When | Notes |
|---|---|---|
| **Caddy** | the compose default | point the DNS for `PUBLIC_DOMAIN` at it and certificates are obtained and renewed automatically, WebSockets included. Caddy and the CP both use the host network, so it reaches the CP's loopback port. A site with its own proxy can drop it (the second alternative in the `Caddyfile`) |
| **Tailscale Funnel** | one local form | Funnel → `127.0.0.1:8099` directly |
| **ALB + ACM** | aws | TLS only — authentication stays in the CP. `30-ingress.yaml`'s `AuthMode` allows only `oauth` (`dev` is refused behind https, #3485); the templates configure no load-balancer OIDC |
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
| CP core | `CP_ADDR` (`:8080`) · `CONSOLE_DIR` · `AF_RUNTIME` (`local`) · `AF_DB` (`<WS_DATA>/control-plane.db`) · `PUBLIC_BASE_URL` · `AF_PREVIEW_DOMAIN` · `AF_DEV_IDP` (unset; `0` locks the IdP off, otherwise the Admin modal decides and the default is on) · `AF_TRUSTED_PROXY_HOPS` (0) | where it listens, what it serves, which adapter, the public URL, preview subdomains, the development OIDC IdP ([ADR 0116](../decisions/0116-dev-oidc-idp.md); needs `PUBLIC_BASE_URL`; also `GET`/`PUT /api/admin/dev-idp`), the client address | this chapter |
| Workspace listener | `AF_CP_INTERNAL_LISTEN` (unset = none) · `AF_CP_INTERNAL_URL` (unset = workspaces use `AF_CP_BASE_URL`) | the workspace-only port and the URL workspaces reach it by (`deploy/kubernetes/`: `:8098` and `http://af-cp-internal.<cp-ns>.svc:8098`; `docker`, optional: the docker0 gateway address, e.g. `172.17.0.1:8098`, and `http://host.docker.internal:8098`). The clone URLs and `AF_INTERNAL_GIT_HOST` stay public; the Agent rewrites the workspace's git onto the URL (`url.<internal>/git/.insteadOf`) with the git credential stored for both hosts, and LFS answers on the workspace listener point back at it. It takes effect only with `PUBLIC_BASE_URL` set, and from the next workspace Start | §9.3 / [decisions/0106](../decisions/0106-kubernetes-runtime.md) |
| Workspace template | `WS_IMAGE` · `WS_DATA` (`/tmp/af-data`) · `WS_MEMORY` (`1g`) · `AF_MAX_WORKSPACE_MEM` · `AF_MAX_WORKSPACE_CPU` (16384 Fargate units) · `AF_MAX_WORKSPACE_DISK_GB` (1024) · `WS_AGENT_PORT` (7700, the base of the per-workspace ports) · `WS_AGENT_HOST` (`127.0.0.1`) · `WS_JVM_DIR` · `WS_ENV` · `WS_SESSION_CMD` | the common template the CP fills in when starting a workspace. `WS_ENV` reaches `docker`, `native` and Kubernetes workspaces; the ECS runtimes do not pass it on | [04](04-agent.md) |
| L1 auth | `AUTH` (`dev`) · `DEV_USER` (`dev`) · `AUTH_EMAIL_HEADER` (`X-Forwarded-Email`) · `GOOGLE_OAUTH_CLIENT_ID/SECRET` · `AF_GITHUB_LOGIN_CLIENT_ID/SECRET` (or `GITHUB_OAUTH_CLIENT_ID/SECRET`) with `AF_GITHUB_ALLOWED_ORGS` and the other `AF_GITHUB_*` · `AF_OIDC_PROVIDERS` + `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST,LABEL_JA,LABEL_EN,SCOPES,PROMPT,LINK_CLAIM,ALLOWED_EMAILS,ALLOWED_DOMAINS,ALLOWED_TIDS}` · `AF_COOKIE_SECRET` · `AF_SESSION_TTL` (168h) · `AF_OAUTH_ALLOWED_{EMAILS,DOMAINS,EMAILS_FILE}` | Console login, with `AUTH=oauth` refusing to boot without a working provider. An OIDC provider must declare `TRUST`, and GitHub needs `AF_GITHUB_ALLOWED_ORGS`; either is disabled otherwise. **A sign-in is refused unless some way in admits it**: these allowlists, a tenant's roster, a tenant's auto-join domains or an approved tenant IdP. With none of them, every login is denied | [07 §7.3](07-security.md) / [decisions/0043](../decisions/0043-login-idp.md) |
| Provisioning and roles | `AF_PROVISION` (`auto`) · `SUPER_ADMIN_EMAILS` | how an unknown identity is admitted; who is a deployment administrator | [06](06-data.md) |
| At-rest encryption | `AF_MASTER_KEY` | unset means plaintext (development only). **Losing it is a crypto-shred** — keep it in a vault separate from the data | [07 §7.6](07-security.md) |
| Git provider OAuth | `AF_GITHUB_BUILTIN_APPS` (on; `off` withdraws the built-in GitHub apps) | a tenant administrator picks or registers the apps in the Console. `BITBUCKET_OAUTH_KEY/SECRET` are no longer read, and `GITHUB_OAUTH_CLIENT_ID` is for sign-in only | [decisions/0052](../decisions/0052-tenant-git-oauth.md) |
| Scale-to-zero and showback | `AF_AUTOSTART` (on) · `AF_SESSION_IDLE_TIMEOUT` (1h) · `AF_INTERACTION_IDLE_TIMEOUT` (the session value) · `AF_WS_IDLE_TIMEOUT` (2h) · `AF_PRESENCE_IDLE_TIMEOUT` (30m) · `AF_IDLE_SWEEP_INTERVAL` (1m) · `AF_STOP_GRACE_SEC` (30, at most 120) · `AF_USAGE_SAMPLE_INTERVAL` (5m) | auto-start, idle stop, the grace period, usage sampling. For the idle timeouts, the sweep and the usage sampler, `0` means off | [03](03-control-plane.md) |
| MCP | `AF_MCP_ENABLED` | whether `/mcp` exists at all; only the exact string `true` enables it | [08](08-integrations.md) |
| MCP OAuth | `AF_MCP_OAUTH` (`1` enables; needs `AF_MCP_ENABLED=true` and an `https` `PUBLIC_BASE_URL`) · `AF_MCP_OAUTH_TRUSTED_HOSTS` (`claude.ai,claude.com,chatgpt.com`) | the Control Plane as the OAuth authorization server of its own `/mcp`; the second variable is the fallback for the hosts that may receive the sign-in redirect and serve client metadata (a super admin's saved list in the Admin modal wins) | [decisions/0118](../decisions/0118-mcp-oauth.md) |
| Watch webhooks | `AF_GITHUB_WEBHOOK_SECRET` (the deployment GitHub App's webhook secret; unset leaves `/webhooks/github` off) | pokes for the watch engine, public repositories only ([ADR 0133](../decisions/0133-external-watch-engine.md) §4). Rate limits are per process, so with several Control Plane replicas the effective limit is multiplied by their number. | [05](05-api.md) |
| Egress | `AF_EGRESS_LISTEN` (`:3128`) · `AF_EGRESS_TOKEN` · `AF_EGRESS_{INGEST,POLICY}_URL` · `AF_EGRESS_PROXY_ADDR` · `AF_EGRESS_ENFORCE` · `AF_EGRESS_ALLOWLIST` · `AF_EGRESS_EVENT_RETENTION_DAYS` (days raw gateway events are kept, default 30, `0` = forever) · `AF_EGRESS_ARCHIVE_URL` (`s3://bucket/prefix`, `gs://bucket/prefix` or `file:///dir`: archive each UTC day of raw events there before the purge deletes it; empty = off; `gs://` needs `roles/storage.objectCreator` + `objectViewer` on the bucket through Application Default Credentials, i.e. Workload Identity on GKE; see [03 §3.8](03-control-plane.md)) | the forward-proxy subcommand and the CP's aggregation. `AF_EGRESS_PROXY_ADDR` injects the proxy variables into every target: `docker`, `native` and Kubernetes pass the whole deployment env (`WS_ENV` included) on, while `ecs` and `ecs-ec2` take only the proxy variables from it and still do not pass `WS_ENV` on. On docker, Kubernetes, ecs and ecs-ec2 the CP internal host joins `NO_PROXY`, and on ecs / ecs-ec2 so do `169.254.170.2` and `169.254.169.254` | [07 §7.8](07-security.md) |
| Postgres | `AF_DATABASE_URL`, or `AF_DB_{HOST,PORT,USER,PASSWORD,NAME,SSLMODE}`, and **where the password really lives**: `AF_DB_PASSWORD_SECRET_ARN` / `AF_DB_PASSWORD_SECRET_KEY` | only when the store is Postgres. The parts are composed into a DSN; the ARN is what lets a rotated password be picked up without replacing the task (§9.9) | [06](06-data.md) |
| ECS adapter | `AF_ECS_{CLUSTER,REGION,SUBNETS,SECURITY_GROUP,NAMESPACE_ARN,EFS_ID,EXEC_ROLE,TASK_ROLE,INFRA_ROLE,LOG_GROUP,WORKSPACE_IMAGE,TASK_CPU,TASK_MEMORY,WS_DISK_GB,POSIX_UID,POSIX_GID,START_TIMEOUT_SEC}` | the coordinates of the static infrastructure the templates built. Read by `ecs` and `ecs-ec2` alike | [ecs runbook](../../deploy/aws/ecs/README.md) |
| EC2 slot pool | `AF_ECS_EC2_LAUNCH_TEMPLATE` (required) · `AF_ECS_EC2_SLOT_TYPES` · `AF_ECS_EC2_DEFAULT_SLOT_CLASS` · `AF_ECS_EC2_AMI_ARM64` · `AF_ECS_EC2_MAX_SLOTS` (8) · `AF_ECS_EC2_HOME_GB` (50) · `AF_ECS_EC2_SLOT_SLEEP_SEC` (900) · `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` (0 = never) · `AF_ECS_EC2_HIBERNATE_AFTER_SEC` (0 = off) · `AF_ECS_EC2_BACKUP_EVERY_SEC` (0 = off) · `AF_ECS_EC2_BACKUP_KEEP` (3) · `AF_ECS_EC2_GOLDEN_AUTOBAKE` (on), and the sweep and timing knobs `AF_ECS_EC2_*_SEC` | `ecs-ec2` only: the slot types and cap, the home size, and the idle tiers of §9.5 | [ecs runbook](../../deploy/aws/ecs/README.md) §Optional: EC2 slot pool / [decisions/0045](../decisions/0045-ec2-persistent-workspace.md) |
| Workload AWS identity | `AF_WS_WORKLOAD_AWS` (off) | `1` hands the ECS task role back to sessions and terminals and stops suppressing the SDKs' instance-metadata lookup. It never removes a network protection (`ECS_AWSVPC_BLOCK_IMDS`, IMDS hop limit 1, a `DOCKER-USER` reject), so it does not give a docker workspace the host's instance profile; that needs a credential path the operator permits separately. Off, the Agent unsets `AWS_CONTAINER_CREDENTIALS_*` / `AWS_CONTAINER_AUTHORIZATION_TOKEN*` and sets `AWS_EC2_METADATA_DISABLED=true` for everything it starts (on docker the CP starts the container with it), so a tool with no member profile fails instead of running as the workload. Set on the CP; it reaches every runtime at the next workspace Start (docker: a running container keeps its environment until Stop → Start) | [guide member/16](../../guide/member/16-cloud-accounts.md) |
| Claude bedrock route gate (acceptance run only) | `AF_WS_CLAUDE_BEDROCK_ROUTE` (off) | `1` makes every workspace start carry `AF_CLAUDE_BEDROCK_ROUTE=1`, which opens the claude `bedrock` route that its Agent otherwise refuses until the live acceptance (ADR 0128 decision 14). A switch for that acceptance run only, not a release setting: lifting the gate is a separate change. Exactly `1`; any other value (`true`, `0`) adds nothing. Set on the CP; it reaches every runtime (docker, ECS EC2/Fargate, Kubernetes, native) at the next workspace Start; a workspace that is already running or starting keeps its environment on every runtime, so it needs Stop → Start after the CP restarts with the switch | [ADR 0128](../decisions/0128-harness-route-maker.md) · [guide ref/agents](../../guide/ref/agents.md) |
| opencode bedrock route gate (acceptance run only) | `AF_WS_OPENCODE_BEDROCK_ROUTE` (off) | `1` makes every workspace start carry `AF_OPENCODE_BEDROCK_ROUTE=1`, which opens the opencode `bedrock` route (Terminal only) that its Agent otherwise refuses until its own live acceptance (ADR 0128 decision 14). Independent of the claude switch above; the same rules: acceptance run only, exactly `1`, reaches every runtime at the next workspace Start, a running workspace needs Stop → Start | [ADR 0128](../decisions/0128-harness-route-maker.md) · [guide ref/agents](../../guide/ref/agents.md) |
| Engines | `AF_ENGINES_SSM_PARAM` / `AF_ENGINES_JSON` · `AF_LLM_URL` · `AF_COMFY_URL` / `AF_COMFY_API_KEY` · `AF_ENGINE_API_KEY_<KEY>` · `AF_ENGINE_<KEY>_{CONTROL_INTERVAL_SEC,WINDOW_SEC,IDLE_SEC,START_DEADLINE_SEC,FAIL_COOLDOWN_SEC}` · `AF_ENGINE_ECS_CLUSTER` · `AF_ENGINE_WAKE_TIMEOUT` (900 s) · `AF_ENGINE_PLAIN_HOLD` · `AF_REMOTE_ENGINE_{URL,TOKEN,KEYS}` | the engine table, the engine controller, the gateway's hold on a cold engine, and borrowing another deployment's engines | [decisions/0071](../decisions/0071-self-hosted-inference-engines.md) / [0076](../decisions/0076-external-image-engine-on-lan.md) / [0077](../decisions/0077-engine-boxes-bought-by-cp.md) / [0079](../decisions/0079-remote-engine-from-another-deployment.md) |
| Speech | `AF_VOICEVOX_URL` (`http://127.0.0.1:50021`) · `AF_TTS_ECS_SERVICE` and the other `AF_TTS_ECS_*` · `AF_TTS_MAX_CHARS` (300) · `AF_POLLY_{REGION,ENGINE}` | a VOICEVOX by URL, or the one on ECS that the CP scales from zero; Amazon Polly | [decisions/0070](../decisions/0070-tts-ondemand-engine.md) |
| Containerless adapter | `AF_NATIVE_AGENT_BIN` (`workspace-agent` on `PATH`) · `AF_NATIVE_ROOTFS` · `AF_NATIVE_BWRAP` | where the agent binary lives; the rootfs that switches on the bubblewrap sandbox | [native runbook](../../deploy/native/README.md) |
| Kubernetes adapter | `AF_K8S_NAMESPACE` · `AF_K8S_WORKSPACE_IMAGE` · `AF_K8S_STORAGE_CLASS` · `AF_K8S_HOME_GIB` · `AF_K8S_STATE_GIB` · `AF_K8S_IMAGE_PULL_SECRET` · `AF_K8S_NODE_SELECTOR` · `AF_K8S_SERVICE_ACCOUNT` (`default`) · `AF_K8S_HOME_SHARED_FS` (unset; `1` when the StorageClass backs homes with one shared filesystem, so the home-nearly-full notice is withheld) | `kubernetes` only: the workspace namespace, image and StorageClass, the claims' default sizes, the node pool, and the workspaces' way back to the CP (the workspace listener row, §9.3) | [kubernetes runbook](../../deploy/kubernetes/README.md) / [decisions/0106](../decisions/0106-kubernetes-runtime.md) |
| Inside the workspace (injected by the CP; **an operator never sets these**) | `AGENT_TOKEN` · `AF_SECRET_KEY` · `AGENT_STOP_GRACE_SEC` · `AGENT_SESSION_CMD` · `CLAUDE_CONFIG_DIR` · `AF_AGENT_SELF_UPDATE_ALLOWED` · `AF_CP_BASE_URL` with the per-feature tokens (`AF_DOCS_TOKEN`, `AF_MCP_TOKEN`, `AF_MEMO_TOKEN` …) · on `native` also `AGENT_ADDR`, `AF_TMUX_SOCKET` and `AGENT_DOCS_DIR` · where set (on `kubernetes`, optionally on `docker`) also `AF_CP_INTERNAL_URL`, which requests prefer over `AF_CP_BASE_URL` while links for people keep the public one | CP ↔ agent authentication, the DEK, the grace period, the agent's routes back to the CP (`manager.workspaceExtraEnv`). The token and the DEK travel as a 0600 env file on `docker`, as SSM SecureString task secrets on ECS, and as a per-workspace Secret on `kubernetes` | [04](04-agent.md) / [07 §7.5](07-security.md) |

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
which `workspace-agent install-jdk <major>` fills with Temurin from Adoptium
(`api.adoptium.net`) and `install-jdk corretto-<major>` with Amazon Corretto
(`corretto.aws`); both hosts are in the default egress allowlist with every other
installer download host (`workspace/agent/testdata/installer-egress-hosts.txt`, checked
against `defaultEgressAllowlist` from both modules). A selection is a JDK id: a bare major is Temurin,
`corretto-<major>` is Corretto (`workspace/agent/internal/jdkvendor`). Choosing a
Java version in the Console makes the entrypoint install any missing one and put it on
`JAVA_HOME`. `GET /env/toolchains` offers what is on disk in either directory plus what
can be installed (`java_available`); **choosing one that is not installed offers a
button that installs it there and then** (`POST /env/jdk-install`, then poll with
`GET`). After installation `resolvedToolchains` globs the directories at every start,
so it takes effect **from the next session** without a restart. A major that is in both
directories resolves to the newer patch (the `release` file's `SEMANTIC_VERSION`, or
Corretto's `version.txt`; a tie
keeps `/usr/lib/jvm`), so installing an installed major again is an update. The
toolchains GET also reports `java_versions` (JDK id → resolved version) and stays offline;
`GET /env/jdk-latest?major=<id>` asks the vendor (Adoptium, or the corretto.aws redirect) (cached 6 h, a failure 10 min) and answers
`update_available`, which is when the Console offers the update button. For a major that
is not installed it still answers `latest`, the version Install would fetch, and the
Console names it on the button. `GET /env/node-latest?major=N` is the Node.js twin
(nodejs.org's index, versions with nvm's `v` prefix). Both accept only the majors the
picker offers, because each one is a cache key (`version_lookup.go`). The toolchains GET
reports `node_versions` too (major → the patch `nodeBinFor` resolves), and the Node row
offers the same update. `install-node` adds each patch next to the old ones, so it marks
its trees (`.agent-fleet-installed`) and the entrypoint runs `prune-node-versions` right
after it, before any session: per major, marked patches older than the newest go. Unmarked
trees (an nvm install, or one written before the marker existed) are never removed. The
marker only says who installed a tree, so pinned versions stay regardless: the tree
install-node returned (`--keep`), an exact saved selection, and exact versions named by
an nvm alias (symlinked aliases included) or by `~/.nvmrc` / `~/repos/*/.nvmrc` (read as
nvm does: comments, blank and `key=value` lines skipped). Every tree is checked before
anything is removed: a top-level entry that is a symlink, a directory symlink inside a
tree, an absolute symlink, or a relative one whose target climbs above its own tree at any
step stops the whole pass (Node's own trees hold only a few relative file links within
themselves). A major whose newest has no
executable `bin/node` is skipped.

`GET /env/toolchain-installs` lists every JDK tree (home volume and `/usr/lib/jvm`) and
every Node major under `~/.nvm` with version, size and whether it may be removed;
`DELETE /env/jdk/{major}` / `DELETE /env/node/{major}` remove home-volume trees only
(`toolchain_uninstall.go`). They refuse the selected major (an exact `22.23.4` counts as
22) unless an independent `/usr/lib/jvm` copy remains — a real directory, with no symlink
on the path above it, outside home, sharing no directory (device and inode) with the home
copy so a bind mount of it does not count, that
passes the same self-contained link check as the Node prune and has a working `bin/java`
(Temurin 21/25 do; Temurin 8's directory link means it is no fallback) — and refuse
while an install runs, including one started from a terminal: install-jdk / install-node
hold a flock on their root for the whole install. The check and a rename into
`<root>/.agent-fleet-trash` happen under that flock, the installer's mutex and the lock
the selection PUT takes (which now also writes `toolchains.json` atomically); the delete
runs outside them. Only the trash directory is ever swept, only when it is a real
directory, and it is left in place once empty. The listing and DELETE share one "root
busy" test, so a row never offers what the DELETE would refuse.

## 9.5 The AWS target

Seven CloudFormation stacks, in deploy order `00-network → 10-data → 20-platform →
(40-ec2-pool) → (50-tts) → (60-engines) → 30-ingress`; the ones in parentheses are
optional. The operator guide's [ECS layout page](../../guide/operate/aws-ecs/overview.md) says what each one owns.

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
  [ECS upgrade page](../../guide/operate/aws-ecs/upgrade.md).

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

## 9.10 The ECS runbook's developer sections

These sections were in `deploy/aws/ecs/README.md` until its procedures moved to the operator
guide (`guide/operate/aws-ecs/`). They are kept here verbatim because only someone changing
Agent Fleet runs them, or because they are the history behind a rule the guide now states
without its story: the prove-out sequence and E2E completion gate of the ECS adapter, the
51,200-byte template incident and its round trip, the cold-start measurement, and the
development deployment (`dev-deploy.sh`), which bakes `main` through this repository's
workflows. Headings are one level down; links into `docs/log/` are plain text.


### Prove-out sequence

Runs from a host (or an Agent Fleet workspace — dogfooding) with **dedicated
sandbox** credentials. AWS API calls are just HTTPS; the heavy compute (ECS tasks,
RDS) lives in AWS, so this host's build/OOM limits don't gate it.

- **S0** — `aws sts get-caller-identity` in the sandbox; pick a region.
- **S1** — deploy `00-network.yaml`; confirm the deploy → `delete-stack` loop and
  the standing cost (NAT is the main one, see below). **proven.**
- **S1.5** — deploy `10-data.yaml` (EFS+RDS) and `20-platform.yaml` (ECR/cluster/SC
  namespace/IAM); confirm and tear down. **proven.**
- **S2** — with ECR up (from `20`), push the CP + Workspace images (§ECR push below).
  ECS can't pull until they're in ECR.
- **S3** — deploy `30-ingress.yaml`; the CP/Console service boots, CP-native
  Google OAuth login works, RDS reachable.
- **S4** — with `runtime_ecs.go` (stage 2) implemented, the CP **dynamically provisions a
  workspace** (exercises the real adapter) → the E2E gate below.

CFN authoring (S1–S3) and the stage-2 Go code are **parallel tracks**; they meet at S4.

### E2E completion gate (frozen spec §20b.7.14)

1. login (CP-native Google OAuth) → tenant → workspace Start (Service desired 1, RUNNING, healthz).
2. session create → terminal attach (CP→Service Connect→Agent) → I/O.
3. Stop (desired 0) → re-Start resumes home/claude state (EFS persists).
4. P3-9 reaper: idle → stage-1 halt → stage-2 desired 0; no fire while attached.
5. **no plaintext secret**: `describe-task-definition` shows the DEK only as an SSM
   `valueFrom` ARN, never as an `environment` value.

### A development deployment: `dev-deploy.sh`

`update.sh` deploys a **released** tag. A deployment kept for development wants the
opposite: whatever is on `main` right now, without cutting a version. That is
`dev-deploy.sh` (docs/log/73), and it ends by calling `update.sh` — the pre-flight checks
above are the reason, and writing a second copy of them for the dev path would mean the
checks are what drifts.

```bash
./dev-deploy.sh --profile <p> --region <r>            # origin/main → the dev deployment
./dev-deploy.sh --profile <p> --region <r> --dry-run  # just the plan
```

It names the tag (`<next patch>-dev-<sha8>`), bakes it through `dev-image.yml` (GHCR,
never a release tag), copies it into ECR **index and all**, and moves `ImageTag`.
Three things it decides for you:

- **Whether the workspace image needs baking at all.** Only when `workspace/` changed
  since the deployed tag; otherwise the workspace image is re-tagged inside ECR, which
  copies no bytes. Baking it costs ~10 minutes of QEMU.
- **That the golden snapshot does not have to be re-baked** when those bytes are
  identical (docs/log/72 §72.6.4 paid 10 minutes and two slots for exactly that).
- **That this is not somebody's live deployment.** It refuses any `Fqdn` but the dev
  one unless `--allow-fqdn` says otherwise: moving `ImageTag` puts a "restart required"
  badge in front of everyone running a workspace there.
- **Whether the engine tools image has to be carried over.** `af-engine-tools` holds the
  fetch sidecar and the ingest steps (ADR 0072), it is not part of `dev-image.yml`, and
  only `standup.sh` used to copy it — so a dev deploy left the engines' fetch containers
  with nothing to pull. Now: the tag the engines stack asks for is carried into ECR when
  it is not there, and a per-commit tag is baked when `deploy/aws/ecs/engine-tools/` has
  changed since the deployed commit. 🔴 It cannot set `EngineToolsImageTag` itself (it
  only runs `update.sh` on the ingress stack), so in that second case it prints the tag
  to set in `params/60-engines` — otherwise the deployment looks current and runs the
  previous release's scripts.

⚠️ It bakes **origin's** ref. Local commits that are not pushed are silently not in it.


### The 51,200-byte wall (why a deploy can stop working without anyone touching it)

CloudFormation refuses a template body over **51,200 bytes**; anything larger has to be
handed over through S3. `cfn/30-ingress.yaml` crossed that line on **2026-09-01** (PR #277,
RDS managed-password rotation: 50,191 → 54,681 bytes), and at that moment **every path that
deploys it stopped working** — `standup.sh` (build it) and `update.sh` (every release,
production included), because both call the same `cloudformation deploy --template-file`.

It was found on 2026-09-02, by the first stand-up since. Three things kept it hidden:

1. **The AWS CLI refuses on file size before it calls the API.** The failure is a CLI error,
   not a CloudFormation one, so there is nothing in the stack events to find.
2. **`update.sh` looked healthy.** It only ever overrides `ImageTag`, so people think of it
   as a parameter change — but it passes the whole template on every run, so it was equally
   broken.
3. **The stand-up path had never been exercised end to end.** Creating `30-ingress` is the
   only operation that had not been run since the growth.

★ The general rule this is an instance of: **a deployment script is only verified along the
paths somebody actually runs.** Green unit tests do not touch it, and a path that is skipped
because "it obviously still works" is exactly the one that rots. That is why the fix is not
just "big templates go to S3":

- `env.sh`'s **`af_cfn_deploy` is the only entry point** to `cloudformation deploy`, and it
  **measures the file on every call**. Remembering that "30-ingress is the big one" would
  reproduce the incident on whichever template grows next.
- The staging bucket lives in **20-platform** (created before 30-ingress, outlives it;
  putting it in 30-ingress would need the bucket to deploy the stack that creates it).
  Objects expire after 7 days.
- `teardown.sh` **empties the bucket before deleting 20-platform** — CloudFormation cannot
  delete a non-empty bucket, and a teardown that dies half-way is how "the create path is
  never exercised" gets reproduced.
- `deploy/local/ecs-lifecycle-stub-test.sh` cases **3b / 3c** run both of those on every CI
  run, against a deliberately padded template. A fix nobody executes is the original bug.

#### Back inside the wall (2026-09-04)

The S3 route is the safety net, not the resting state — it needs a bucket that only exists
once 20-platform is up, it constrains the teardown order, and its failures surface as CLI
errors with nothing in the stack events. So `30-ingress.yaml` was put back under the limit:
**54,681 → 40,493 bytes**, with no parameter, default or resource changed.

What moved was prose. Parameter `Description:` bodies were 21,630 bytes — **40% of the
template** — and they are the one part that does not have to reach the API to do its job;
they now live in [`cfn/PARAMETERS.md`](../../deploy/aws/ecs/cfn/PARAMETERS.md), in full, with the template
keeping a line or two per parameter.

⚠️ **A YAML comment costs exactly what a `Description:` costs.** The limit is on the
template *body*, so `#` is not a cheaper place to put a paragraph — moving one there saves
zero bytes. Long explanations have to leave the file, not change syntax inside it.

`ecs-lifecycle-stub-test.sh` case **3b-2** now fails the build when any `cfn/*.yaml` exceeds
51,200 bytes. It is a hard failure on purpose: the growth in 2026-09-01 was silent for three
months, and a warning would have been too. Crossing the wall again should be a deliberate
edit to that constant, visible in the diff — while `af_cfn_deploy` keeps measuring at run
time, so a template that does cross it still deploys instead of dying.

#### 30-ingress may cross the wall again (2026-11-11, #3550)

Staying inside the wall had run out of road: `30-ingress.yaml` sat 200 bytes under its
50,176-byte test budget, the prose had already left (above), and every new parameter —
a `Parameters` entry, a condition, an `Environment` line — costs 300–500 bytes, so each PR
that added one failed CI and was trimmed by hand. Only nested stacks or a second template
would make room without S3, and moving resources between stacks replaces them; the ALB and
its listeners must not be replaced, so that is out.

So the S3 route stopped being only a safety net **for this one template**: 30-ingress is
always deployed after 20-platform's bucket exists (stand-up order 00 → 10 → 20 → images →
40 → 30), which is the property the pre-bucket templates lack. What changed:

- `cfn-cp-settings-env-test.py` budget for 30-ingress: 51,200 − 1,024 → **65,536** (about
  14 KiB, thirty-odd parameters of room). Raising it again stays a deliberate edit.
- case **3b-2** exempts 30-ingress from the 51,200 limit (the others keep it);
  `cfn-template-size-test.py` keeps the pre-bucket templates 4 KiB under it.
- Every hand-typed `aws cloudformation deploy` of 30-ingress (`--template-file`) in this
  README, `PARAMETERS.md` and `release-ecr.sh`'s hint now carries
  `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` (the AWS CLI refuses a direct upload over
  51,200 bytes before calling the API), and `cfn-template-size-test.py` fails on one that
  does not. `CFN_BUCKET` is the 20-platform output `CfnTemplatesBucket`:
  `CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text)`.
- The template itself did not change: no logical ID, parameter or resource moved, so an
  existing stack updates in place.

#### Round trip, measured (2026-09-02)

Teardown → stand-up had never been done end to end. It has now, on the dev deployment, at
`0.14.3-dev-ecbbcc59`:

| Step | Result |
|---|---|
| `dev-image.yml`, `image=both`, 2 architectures | ~11 min; both images are `[amd64, arm64]` indexes |
| `standup.sh` 00 → 10 → 20 → images → 40 → 30 | `30-ingress` went through S3, `CREATE_COMPLETE` |
| CP boot | `metadata store: postgres`, `/readyz` 200 |
| Agent, both architectures | golden bake booted real workspaces: `Agent healthy` 99 s (arm64) / 101 s (x86_64) |
| CP → Agent | the Cloud Map fallback fired and succeeded: `did not resolve through the Service Connect alias -> using 10.20.x.x:7700 from Cloud Map` |
| `teardown.sh` | 5 stacks gone; sweep and an independent count both show 0 stacks / 0 NAT / 0 RDS / 0 ALB / 0 EC2 / 0 EFS / 0 ECR |

⚠️ **Check `/readyz`, not `/healthz`.** `/healthz` answers 200 with a dead database — that is
how the RDS rotation outage stayed silent for so long. `/readyz` is the one that touches it.

Two rough edges found on the way, **both since closed**:

- **`standup.sh`'s preflight compares the capture against the templates** (parameters with
  no `Default` must be present). It previously checked only that the capture *files existed*,
  so a capture older than the templates would fail **after** 00–20 were already built — the
  most expensive moment to find out. It no longer counts those parameters by a fixed
  indentation either: it takes the depth of the first key under `Parameters:`, and **says so
  when it reads none at all**. A parser that quietly reports "0 required" is the same shape
  as the wall above — a check that is not running, with nothing to see.
- **`teardown.sh`'s bucket-emptying step prints why it failed.** It used to swallow stderr
  (`>/dev/null 2>&1 || echo "(skip: …)"`), so "the bucket was already gone" and "it could
  not be emptied" looked identical — and the next thing to fail is the 20-platform delete,
  by which point the reason has been thrown away.

### Known behavior: a cold Start answers `starting`, not `running`

A cold workspace Start pays a full image pull on every launch (Fargate keeps no
image cache between tasks). Measured end-to-end in the sandbox (2026-08-15,
`docs/log/62` §62.9): **126s on a fresh home, 128–218s on a warm one** — the earlier
"~100s" was optimistic. `POST
/api/workspace/start` does **not** wait for that: it returns as soon as the ECS
service sits at `desiredCount 1`, with the live state — normally `starting`.
Poll `GET /api/workspace` for the transition to `running`; the Console already
does (4s, and it walks the cold start to `running` without a reload).

Older builds blocked the request on the Agent readiness wait
(`AF_ECS_START_TIMEOUT_SEC`, then 90s) and so outlived the ALB's **60s** idle
timeout — the *HTTP request* died with a **504** while the workspace converged
fine behind it. Fixed in `control-plane/runtime_ecs.go` (`watchReady`): the
readiness poll now runs in the background for a log line only
(`ecs start: service <name> Agent healthy <n>s after Start`), on a budget
deliberately longer (default **300s**) than any request may take. `30-ingress.yaml`
now spells the ALB's `idle_timeout` out at its default 60s so the ceiling is
visible. 🚧 Verified by unit tests only — not re-run against a live ECS stack.

Note `running` here means the ECS **task** is RUNNING, which is a moment before
the entrypoint has finished and the Agent answers. A create issued in that window
still gets `502 workspace agent unreachable`; retry.

**The split has been measured** (2026-08-15,
`docs/log/62-ecs-start-latency.md` §62.9–62.10),
and the answer was not the image. A warm-home Stop→Start costs **~101s**: **4–8s**
for ECS to create the task, **16s** provision (ENI attach), **35s** image pull
(918 MiB compressed, 34 layers), **~25s** EFS mount + container create, **21s**
entrypoint. Image pull is ~35% of that — under the 40% decision gate and under 40s
absolute — so SOCI lazy loading was **rejected**.

⚠️ **Benchmarking caveat, learned the hard way**: restarting a workspace *right
after* stopping it (within a minute or two) makes ECS take **40–143s** just to
create the task, because the previous task is still being torn down while the new
deployment lands on top of it. That is a property of the measurement loop, not of
production (a workspace is stopped by the reaper and started minutes-to-hours
later). **State the stop→start gap whenever you quote a restart number.**
Re-measure the same way if any of this changes:

```bash
# image pull window (and the surrounding task lifecycle)
aws ecs describe-tasks --cluster <cluster> --tasks <task-arn> \
  --query 'tasks[0].{created:createdAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt}'
# entrypoint cost (boot-install of the pinned CLIs on a fresh home is NOT a pull)
aws logs tail <ws-log-group> --since 10m
```

Note the workspace image pushed here is the **lean** variant
(`BAKE_AGENT_CLIS=0`), so a *fresh home* additionally pays a one-time
npm/GitHub boot-install inside the entrypoint (~60s for all CLIs, measured in
`workspace/entrypoint.sh`). That cost is network, not image pull, and it does
not recur — `~/.local` persists on EFS. So measure **two** scenarios and judge
on the second: a first Start on an empty home (pull + boot-install) and a
Stop→Start on a warm home (pull only). The warm one is what scale-to-zero pays
every time.

To isolate the pull with no EFS/entrypoint noise, register a throwaway task
definition for the same image with `entryPoint` overridden (`run-task
--overrides` can override `command` but **not** `entryPoint`, and
`entrypoint.sh` ends in `exec "$@"`, so overriding the command alone still runs
the whole entrypoint). Every `run-task` is cold by construction — Fargate keeps
no image cache between tasks. The full recipe, the delta arithmetic and the
decision gate are in
`docs/log/62-ecs-start-latency.md` §62.7
(conclusion on SOCI: conditional yes, gated on this measurement).

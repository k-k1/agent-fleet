---
audience: "someone asking \"which file does that?\""
source_of_truth: "the code"
updated: "2026-09"
---

# 90. Code map

English | [日本語](90-code-map.ja.md)

This chapter gives **grep starting points**, not an inventory. A subsystem's design lives
in its own chapter. This one only says where to start looking. The files and packages
named below are examples, and every table here is incomplete. The complete list is always
the directory itself (`ls`), or the table in the code that a row names.

## 90.1 Top level

What each top-level directory is for is in
[10 §10.1](10-development.md#101-repository-layout-responsibilities-only), and which of
them are Go modules is in [10 §10.4](10-development.md#104-testing). This chapter goes one
level down. Two things outside those directories are also worth knowing:

| Path | What to look for there |
|---|---|
| `.github/workflows/` | CI (`ci.yml`, `docs.yml`, `e2e.yml`), the contract workflows (`*-contract.yml`), and the image and release workflows. What each runs is [10 §10.4](10-development.md#104-testing) |
| `.githooks/pre-commit` | The forbidden-token scan over what you stage. It runs the same scanner as `ci.yml`'s `release-scan` job (`deploy/release/scan-forbidden.sh`) |

## 90.2 Two rules that hold in both Go modules

**A route's handler.** Each binary builds its route table in `buildMux` (`routes.go`).
`buildMux` registers some routes itself and hands the rest to per-feature functions
elsewhere in the module (for example `registerEngineRoutes` in the CP,
`browserx.RegisterRoutes` in the agent). So grep the method and path across the whole
module, `"GET /api/admin/engines"` say: the line that registers it names the handler,
and the handler's name tells you the file or package (`sessionx.HandleCreateSession` is
in `internal/sessionx`). `testdata/routes.golden` is the route table as
`TestRouteTableGolden` builds it in its test configuration. It is a quick way to see what
a module serves, but not every route: in the CP, the engine gateway's routes are
registered only when an engine is configured, and the golden has none of them. What each
route is for is [05](05-api.md).

**A package under `internal/` never imports `package main`.** What it needs from `main`
is handed to it. The packages with the widest seams declare that need in their own
`deps.go`, and `main` supplies it once at boot from a `*_wiring.go` or `*_seam.go` file
(for example `mcp_wiring.go`, `tenant_wiring.go` and `runtime_seam.go` in the CP, and
`session_wiring.go` and `browser_seam.go` in the agent). Others take it as a constructor
argument: `internal/auth` gets the tenant-secret opener through
`auth.NewTenantIdPRegistry`, called from the CP's `main.go`. So when a call ends at one of
these injected function fields or interfaces, look at the wiring file or the
constructor's caller on the `main` side. The rules for these seams are in
[decisions/0067](../decisions/0067-parallel-refactor.md) (decision
5). Why there are two binaries, and how they are layered, is in
[decisions/0012](../decisions/0012-go-internal-refactor.md).

## 90.3 `control-plane/`

`main.go` wires the binary; `control-plane egress-proxy` runs the egress forward proxy
from the same binary. Most of the CP is `package main` at the module root, one concern per
group of files that share a name prefix. The families that could be cut loose are under
`internal/`: `ls control-plane/internal` is the list, and the comments at the top of a
package's files (and its `deps.go`, where it has one) say what it holds. The CP image is `control-plane/Dockerfile`, built with the repository root
as its context (the root `.dockerignore`).

| Concern | Where to start |
|---|---|
| Sign-in | `oauth.go` (the flow and the login session), `tenant_login.go` (per-tenant login rules), `oauth_link.go` (linking a second sign-in method). The IdP adapters and the registry of tenant-defined providers are `internal/auth` |
| Git-provider and Jira OAuth | `oauth_bitbucket.go`, `oauth_github_device.go`, `oauth_jira.go`, `tenant_git_oauth*.go`, `git_oauth_bridge.go` ([08 §8.4.1](08-integrations.md#841-the-oauth-apps-belong-to-the-tenant)) |
| Tenants, identities, memberships | `resolver.go`, `manager.go`, `pat.go`, `system_tenant.go`. The tenant and membership HTTP layer is `internal/tenantsrv` |
| Workspace lifecycle | `workspace_*.go`, `agent_client.go`, `agent_dial.go`. The runtime adapters (docker, native, ecs, ecs-ec2) are `internal/runtime` ([03 §3.3](03-control-plane.md#33-the-manager-and-the-runtime-abstraction)) |
| Relaying to the agent | `proxy.go` (REST, the SSE stream, the terminal WebSocket), `preview*.go`, `browser.go`, `fs_file_proxy.go` |
| The push channel to the Console | `events.go` |
| Store | `internal/store`: `store.go` is the port, `store_sql.go` the shared SQL, `store_sqlite.go` and `store_postgres.go` the dialects, each embedding its own migration directory ([06 §6.1](06-data.md#61-store-layout)) |
| Keys | `custodian.go`, `dek.go` |
| Internal git | `internal_git*.go`, `git_http.go`, `git_lfs*.go`, `git_gc.go` ([91](91-internal-git.md)) |
| Egress | `egress*.go` ([03 §3.8](03-control-plane.md#38-the-cps-half-of-egress-control)) |
| Endpoints the agent calls under `/internal/` | `*_bridge.go`, for example docs, memos, schedules and AWS profiles ([05 §5.2](05-api.md#52-the-internal-surface)) |
| Audit, metrics, usage, cost | `audit.go` (the read side; the write side is `auditActionTarget` in `proxy.go`), `claude_audit.go`, `metrics.go`, `usage*.go`, `cloudcost.go`, `cost_*.go` |
| Memos, notifications, schedules | `memo*.go`, `notification.go`, `schedule*.go`, `scheduler*.go` ([03 §3.6](03-control-plane.md#36-the-memo-queue), [§3.7](03-control-plane.md#37-background-jobs)) |
| MCP | `internal/mcpsrv` (the tool server at `/mcp` and the tenant's server distribution) and `mcp_wiring.go` ([03 §3.5](03-control-plane.md#35-the-mcp-server)) |
| Self-hosted engines | `engines.go` and `engine_*.go`; the gateway a workspace calls is `engine_gateway.go` ([03 §3.9](03-control-plane.md#39-self-hosted-engines)) |
| Text-to-speech | `tts*.go`, `enkana*.go` |
| Sharing and handoff | `session_share*.go`, `session_handoff.go` |
| Work items | `workitems*.go` |
| The guide in containers | `workspace_docs.go` (staging) and `docs_bridge.go` (the pull path) |
| Idle stop | `reaper.go` (with `connRegistry`), `session_activity.go`, `idle_forecast.go` |

## 90.4 `workspace/agent/`

`main.go` boots the agent. `cli.go` holds the subcommand table (`subcommands`): the
installers, `af-db`, `aws-exec`, `mcp-stdio`, and the hooks and shims the agent runs
itself. Most features live in `internal/`; the root keeps the wiring files, the HTTP
handlers that tie several families together, and features small enough to stay there.
`ls workspace/agent/internal` is the package list, and the comments at the top of a
package's files (and its `deps.go`, where it has one) say what it holds.

| Concern | Where to start |
|---|---|
| Sessions: lifecycle, tmux, driver switching, turns, IO, transcript, titles, spawn, peers | `internal/sessionx`. The session model (wire, metadata, the `Kind*` constants) is `internal/session`, and the live state store is `internal/status` |
| One agent kind | `internal/agents/<kind>`, one package per agent CLI; shell and ssm are in `internal/sessionx/agent_shell_ssm.go`. Kinds are registered in `agentRegistry` (`internal/sessionx/agent.go`). The shared interfaces are `internal/agents`. Adding a kind: [20](20-add-an-agent.md) and [04 §4.3](04-agent.md#43-the-pattern-for-integrating-a-kind) |
| The managed-only kinds (`ManagedOnly` in `internal/agents`) | lcpp: `internal/harness` and its MCP client `internal/mcpc`. muse: `internal/msp` (the protocol) |
| Chat and assistants | `internal/chatx`, `internal/assistants`; `assistants.go` embeds the built-in knowledge (`knowledge/af-usage.md`) |
| Chat bridge | `internal/bridge`, `bridge_operator.go`, `connections_slack.go` |
| Browser pane | `internal/browserx` ([04 §4.10](04-agent.md#410-the-browser-manager)) |
| Agent memory | `internal/memoryx` |
| Usage ledger | `internal/usagex`, `usage_*.go` |
| Git and the filesystem | `internal/gitx`, `fs*.go`, `fetch_loop.go`, `cred_helper.go`, `connections.go`, `repo_jobs.go`, `worktree_*.go`, `svn*.go` |
| MCP | `internal/mcpx` (the in-container `af` server, `workspace-agent mcp-stdio`, and the registry's REST face), `internal/mcpreg` (the registry and the per-CLI config writers), `internal/mcpproj` and `internal/projcfg` (a working copy's own project-scope files) |
| Instructions and skills | `agent_instructions.go`, `internal/userinstr`, `internal/mdblock`, `internal/fleetskills` |
| Toolchains and installers | `env_*.go`, `jdk*.go`, `node_install.go`, `install_*.go`, `*_install_http.go`, `internal/afdb` (`af-db`) |
| AWS | `internal/awsx` (`aws-exec`), `ssm_instances.go` |
| Image generation | `internal/imagegen` |
| Self-hosted engines | `engines.go` |
| Fleet session graph | `internal/fleetgraph` |
| Work items | `workitems*.go`, `connections_jira.go` |
| Secrets | `internal/secrets` ([04 §4.8](04-agent.md#48-secrets--the-agents-responsibility)) |
| Terminal and preview | `terminal*.go`, `preview.go` |
| Clean-up | `cleanup_*.go`, `leftovers.go`, `tool_caches.go`, `cli_version_prune.go` |

Other packages hold features of their own, for example `branchrule` (branch naming),
`uiprefs` (saved UI preferences) and `statemig` (a one-time move of the agent's state).
Small shared helpers include `httpx`, `paths`, `fstore`, `pathguard`, `filemeta`,
`tmuxx` and `transcript`. `wiretest` and `ingresstest` are imported only by tests.

## 90.5 `console/src/`

The directory map is §2.2 of [02](02-console.md), and the
rest of that chapter is the design around it. The feature directories are
`ls console/src/features`. To find your way in:

- **A request to the backend**: grep the path under `console/src/`. `core/api/client.ts`
  wraps `fetch`, and several features keep their calls in an `api.ts` of their own.
- **A screen string**: grep the text in `lib/i18n/locales/`, then its key in the `.tsx`.
- **Something that differs per agent kind**: `agents/registry.ts`.
- **Checks that run a real browser** (`pdf:check`, `doc:check`, the screenshots and the
  scroll checks): the scripts in `console/package.json`, which live under
  `console/scripts/`.

## 90.6 `workspace/` outside the agent

How the image is built, and what is baked or installed on demand, is
[04 §4.9](04-agent.md#49-the-workspace-image-and-its-entrypoint).

| File | What it is |
|---|---|
| `Dockerfile` | The workspace image. It is built with `workspace/` as its context |
| `entrypoint.sh` | Seeding at start, then `exec` of the image's command, `workspace-agent`. The guide is not its job: it is mounted, or the agent pulls it (`docs_sync.go`) |
| `workspace-notes.md` | The operating policy every container gets: the short, always-loaded part (prohibitions and traps) |
| `notes/` | Its topic files (`/usr/local/share/agent-fleet/notes/` in the image), the procedures the policy's index points at |
| `af-db.sh`, `af-aws-exec.sh`, `af-scratch.sh`, `af-arch-repair.sh` | The `af-*` commands on `PATH`. The first two only exec `workspace-agent` |
| `gh-auth-wrapper.sh`, `svn-auth-wrapper.sh` | Installed as `gh` and `svn` ahead of the real binaries, so both find the credentials saved through the Console |
| `jvm.Dockerfile` | Builds the shared JDK directory (`deploy/local/provision-jvm.sh`) |
| `opencode-plugin/`, `tmux.conf` | The opencode plugin and the tmux configuration |
| `.dockerignore` | ⚠️ It excludes `**/*.md` and then re-includes the ones the build needs. **Read it before adding a markdown file the image or the agent binary embeds** |

## 90.7 `deploy/`

The directory map is [deploy/README.md](../../deploy/README.md), and each deployment form's
runbook is the README in its directory ([09](09-deploy.md)). Where to start inside it:

| For | Where to start |
|---|---|
| The start scripts for development | `deploy/local/` (§10.3 of [10](10-development.md)) |
| The AWS templates | `deploy/aws/ecs/cfn/`, one numbered template per layer, with their parameters in `cfn/PARAMETERS.md` |
| The engine images this repository builds | `deploy/aws/ecs/comfyui/` and `deploy/aws/ecs/engine-tools/` |
| Probes and benchmarks against real AWS | `deploy/aws/ecs/harness/` |
| Building the release artifacts | `deploy/release/build.sh`, the single entry point. The native package's builders are `deploy/release/native/` |
| The public distribution repository, and release notes | `deploy/release/dist-repo/` is the seed of that repository; `deploy/release/notes/` holds the release notes of each version, in English and Japanese |
| The forbidden-token scanner | `deploy/release/scan-forbidden.sh` and its Go module, `deploy/release/scan/` |

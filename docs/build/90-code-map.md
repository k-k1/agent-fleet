# 90. Code map

English | [日本語](90-code-map.ja.md)

Audience: someone asking "which file does that?"
Source of truth: the code
Updated: 2026-07

> ⚠️ **This is the only file on this shelf allowed to enumerate paths and package
> names**, and it is here **on the assumption that it goes stale**. **A PR that moves
> paths must update it.**

It gives **grep starting points**, not an inventory. Where a subsystem's design lives is
the chapter it belongs to; this only says where to start looking.

## 90.1 Top level

| Directory | Contents |
|---|---|
| `console/` | The Console SPA (§90.4, designed in [02](02-console.md)) |
| `control-plane/` | The CP — one Go module, one binary; the egress proxy is a subcommand (§90.2) |
| `workspace/` | The workspace image: `agent/` plus the Dockerfile and entrypoint (§90.3, §90.5) |
| `deploy/` | Per-form runbooks and definitions |
| `e2e/`, `console-e2e/` | Fleet and UI end-to-end tests ([10 §10.4](10-development.md)) |
| `docs/` | The shelves, cut by reader ([CONVENTIONS](../CONVENTIONS.md)) |

## 90.2 `control-plane/` — one `package main`, split by responsibility

`main.go` wires; `routes.go` registers routes per feature. Start there.

| Concern | Files |
|---|---|
| Login and the auth gate | `oauth*.go` — the generic OIDC client, the GitHub adapter (it is not OIDC), the tenant login rules, and tenant-defined providers with their runtime registry |
| Git-provider OAuth | `oauth_bitbucket.go`, `oauth_github_device.go`, `git_oauth_bridge.go`, `tenant_git_oauth*.go` — **all CP-side now** ([08 §8.4.1](08-integrations.md)) |
| Tenants, identities, memberships | `tenants.go`, `resolver.go`, `manager.go`, `pat.go` |
| Workspace lifecycle | `workspace_*.go`, `agent_client.go`, `dek.go` |
| Runtime adapters | `runtime.go` plus `runtime_{docker,ecs,ecs_ec2,native}.go` |
| Relaying | `proxy.go` (REST / SSE / terminal), `preview.go`, `browser.go`, `events.go` |
| Store | `store.go` and `store_{sqlite,sql,postgres}.go`, with both migration directories embedded |
| Crypto | `custodian.go` |
| Internal git | `internal_git*.go`, `git_http.go`, `git_lfs*.go`, `git_gc.go` ([91](91-internal-git.md)) |
| Egress | `egress*.go` |
| Audit, metrics, usage | `audit.go`, `claude_audit.go`, `metrics.go`, `usage.go` |
| Memos, notifications, schedules | `memo*.go`, `notification.go`, `schedule*.go`, `scheduler*.go` |
| MCP | `mcp.go`, `mcp_era.go`, `mcp_server*.go` |
| Role-scoped docs | `workspace_docs.go` (staging) and `docs_bridge.go` (the pull path) |
| Idle stop | `reaper.go`, with the connection registry |

## 90.3 `workspace/agent/` — HTTP wiring plus an `internal/` domain layer

The root package owns HTTP, the Console wire format and subsystem wiring. Shared models
and per-agent implementations live under `internal/`.

| Concern | Files |
|---|---|
| Start-up and routes | `main.go`, `routes.go` |
| Sessions | `session_*.go` — lifecycle, tmux, driver switching, the driver-independent turn API, IO, status, transcript, titles |
| Chat and assistants | `chat*.go`, `assistants.go`, `chat_report.go` |
| Chat bridge | `bridge_*.go` |
| Browser pane | `browser_*.go` ([04 §4.10](04-agent.md)) |
| Agent memory | `memory_*.go` |
| Usage ledger | `usage_*.go` |
| Git and the filesystem | `git*.go`, `fs*.go`, `fetch_loop.go`, `cred_helper.go`, `connections.go` |
| MCP | `mcp_*.go` (the implementation is `internal/mcpreg`) |
| Terminal and preview | `terminal.go`, `preview.go` |
| Instructions and toolchains | `agent_instructions.go`, `agent_rtk.go`, `env_*.go`, `jdk_install_http.go` |

| `internal/` package | Responsibility |
|---|---|
| `session` | The session wire format and metadata, kinds, drivers, ids, persistence |
| `agents` | The read interface, the managed driver and thread handle, the notification seam |
| `agents/<kind>` | One package per kind — **this is the pattern to copy when adding one** ([20](20-add-an-agent.md)) |
| `bridge` | Chat-bridge delivery |
| `mcpreg` | The MCP registry, including the per-CLI materialisers |
| `hostcaps` | Detecting what the host can run, so an unusable kind is hidden rather than offered |
| `userinstr`, `mdblock` | The user-instruction source of truth, and the markdown block this product owns inside a shared file |
| `status`, `tmuxx`, `transcript` | The shared state store, exact tmux operations and probing, the transcript wire format |
| `httpx`, `gitx`, `fstore`, `paths`, `secrets`, `notice` | The small shared utilities |

## 90.4 `console/src/`

Designed in [02](02-console.md); this is only the layout. `features/` holds 19
feature-sliced directories, each with its components, store, API slice and CSS. The
rest is `app/`, `core/{api,store}/`, `layout/`, `terminal/`, `agents/`, `ui/`, `lib/`,
`types/`, `styles/`.

**File counts go stale immediately** — measure with `find console/src/features -type f`
rather than trusting a number written here.

## 90.5 `workspace/` outside the agent

| File | Responsibility |
|---|---|
| `Dockerfile` | The workspace image. **The distribution default bakes no agent CLIs** ([04 §4.9](04-agent.md)) |
| `entrypoint.sh` | Start-up seeding and launching the agent. **Distributing the operating guide is the agent's job**, not the entrypoint's |
| `workspace-notes.md` | The operating policy delivered into every container |
| `opencode-plugin/`, `tmux.conf`, `vendor/` | The plugin, tmux configuration, and a home for static binaries |
| `.dockerignore` | ⚠️ It excludes `**/*.md` and then re-includes what is embedded — **a trap worth reading before editing** |

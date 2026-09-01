---
audience: "someone touching an API boundary"
source_of_truth: "the code (this is the map and the invariants; individual request and response shapes are code-as-contract)"
updated: "2026-07"
---

# 05. API boundaries and relaying — the code is the contract

English | [日本語](05-api.ja.md)

There are two boundaries: the **public** one (Console ↔ CP) and the **internal** one
(CP ↔ workspace agent). There are roughly 300 routes on the CP and 200 on the agent —
approximate, because they keep growing; count them with
`grep -c HandleFunc control-plane/routes.go workspace/agent/routes.go`. Enumerating
them all is unmaintainable, so this chapter is strictly a **map**: group → a
representative path → where the detail is.

**The internal Go refactor holds full wire compatibility as a hard constraint** — no
path, JSON shape or error-code string changes — **so nothing here goes stale because of
it.**

## 5.1 The public surface

Reached after the L1 gate. Authorisation is "your own resources only", plus the
membership check (§5.4).

| Group | Representative paths | Handled | Detail |
|---|---|---|---|
| identity / tenant | `GET /api/whoami`, `GET /api/tenants` | CP | [03](03-control-plane.md) |
| workspace | `GET /api/workspace`, `POST /api/workspace/{start,stop,recreate,clean-home}`, `GET /api/workspace/stats` | CP (Runtime) | [03](03-control-plane.md) |
| sessions | `GET/POST /api/sessions`; lifecycle `POST …/{stop,halt,recreate,archive,restore,fork,start}`; semantic operations `POST …/{turn,respond,settings,driver}`; terminal operations `POST …/{input,paste-image}`; `GET …/{status,output,messages,settings}`; title and branch; `GET /api/sessions/archived`; the deletion lock | create / fork / start are CP → agent; the rest is relayed | [04](04-agent.md) |
| ↳ the optional fork body | `POST …/fork` with `{"at": <anchorId>, "include": bool}` **branches from a past message** ([decisions/0039](../decisions/0039-fork-at-message.md)); omitting it copies the whole conversation, which is the backward-compatible behaviour. **Malformed JSON is a 400 — it never silently falls back to copying everything.** An unusable anchor and an unsupported kind have their own error codes | the CP passes the body through | [04](04-agent.md) |
| repos (SCM) | `GET/POST /api/repos`; a working copy with no upstream, `POST /api/repos/init` (just mkdir plus `git init`, so it is **synchronous** and does not go through the import job); the per-repo operations; the deletion lock | relayed | [04](04-agent.md) |
| fs | `GET /api/fs/{tree,file,download,changes,linemarks}`, `PUT /api/fs/file`, `POST /api/fs/{upload,mkdir,newfile,rename,delete,suggest-edit}` | relayed | [04](04-agent.md) / [decisions/0027](../decisions/0027-markdown-code-editor.md) |
| connections | `GET /api/connections`, the git and agent connection endpoints, `GET /api/git-oauth` | relayed — **except that both git-provider OAuth flows are handled by the CP itself** ([decisions/0052](../decisions/0052-tenant-git-oauth.md)) | [08](08-integrations.md) |
| chat / assistants | `/api/chat/conversations*` (streaming is SSE), `POST /api/chat/ask`, `/api/assistants*` | relayed | [04](04-agent.md) |
| env / settings | toolchains, UI preferences, one-button JDK install, workspace settings, per-agent usage chips, the rtk toggle and its savings history | workspace settings are CP; the rest is relayed | [04](04-agent.md) |
| memo | `GET/POST/PATCH/DELETE /api/memos*`, `POST /api/memos/flush` | CP (only the flush reaches the agent) | [03](03-control-plane.md) |
| notifications | `GET /api/notifications`, `POST /api/notifications/{seen,usage-observations}` | CP | [03](03-control-plane.md) |
| schedules | list, runs, patch and delete, pause / resume / run-now. **The Console lists and manages only; creation goes through the MCP tool** | CP | [decisions/0021](../decisions/0021-scheduled-execution.md) |
| cleanup | the survey, deletion, and the archive shelf with restore | relayed | [04](04-agent.md) |
| agent memory | roots, snapshots, diff, tree, export; snapshot, restore, import, apply; settings | relayed | [decisions/0022](../decisions/0022-agent-memory-management.md) |
| MCP registry | list, create, update, delete, test, refresh, enable, secrets | relayed (the agent owns it) | [decisions/0031](../decisions/0031-mcp-registry.md) |
| usage series | `GET /api/usage/series` | relayed | [decisions/0029](../decisions/0029-usage-accounting.md) |
| pat | `GET/POST/DELETE /api/pat*` | CP | [07 §7.6](07-security.md) |
| ssm | profiles and hosts, plus a per-session login | CP (database) + agent (the session) | [08](08-integrations.md) |
| internal git | the management API plus smart HTTP and LFS | CP, **not through the agent** | [91](91-internal-git.md) |
| admin | tenants, sessions, usage, audit, host, egress; limits, roles; the tenant's git OAuth apps | CP, role-gated | [03](03-control-plane.md) |
| MCP | `POST /mcp` (Streamable HTTP JSON-RPC, bearer PAT, excluded from the auth gate) | CP | [decisions/0006](../decisions/0006-mcp-unified.md) |
| preview | `GET /preview/{port}/{rest...}` | CP → agent | §5.3 |
| browser | `POST/GET/DELETE /api/browser/pages*`, `GET /ws/browser` | CP → agent | §5.3 / [decisions/0018](../decisions/0018-container-browser-pane.md) |
| WebSocket | `GET /ws/terminal` | CP → agent | §5.3 |
| internal (agent → CP, per-membership tokens) | memos, schedules, MCP servers, docs; **and a Bitbucket refresh proxy**, which lets the tenant's client secret stay in the CP ([decisions/0052](../decisions/0052-tenant-git-oauth.md)) | CP | [08](08-integrations.md) |
| auth and the rest | login and OAuth, health and readiness ([09 §9.9](09-deploy.md)), the egress and docs internal endpoints, and `/` — the Console bundle, `no-store` | CP | [07](07-security.md) |

- Long operations — start, clone — are **synchronous plus polling**; a job queue was
  considered and not adopted.
- `GET /api/workspace/stats` is assembled by the CP reading the cgroup directly, and it
  reports OOM evidence both for a child process inside the container and for the
  container as a whole ([decisions/0014](../decisions/0014-agent-exit-recording.md)).
  Per-session exit reasons ride on the session list.

## 5.2 The internal surface

- **The agent is not exposed.** It is reachable only from the CP, over loopback and its
  dedicated network.
- Every request carries a per-container bearer token that the CP injects at start. The
  agent verifies it on everything but the health check, **in constant time**
  ([07 §7.5](07-security.md)).
- **Path convention: the CP strips `/api` and forwards the rest unchanged.** The
  agent-specific surfaces are the PTY and browser WebSockets, the ephemeral page API,
  and the preview sub-proxy.

The session API separates **semantic** operations from **terminal** ones. Turn, respond
and settings are driver-independent, and the agent dispatches them either to the
managed structured API or to keystrokes in a TUI. Input, output and the PTY socket are
**only** for a session that has a pane. Switching driver stops and resumes the same
conversation, and returns a busy error mid-turn rather than doing it anyway.

## 5.3 The five relay paths

| Path | In → out | Character |
|---|---|---|
| **REST** | `/api/*` → the agent's same path | Mutating calls are audited on a 2xx (§5.5). A workspace that is not running gives a 409 |
| **SSE** | the chat stream → the agent | Flushed per chunk |
| **WS** | `/ws/terminal` → the agent's PTY | Checks running (**it never auto-starts**), then relays both ways: binary is PTY output, text is input and resize. Connection tracking keeps the workspace warm, which is what the reaper reads |
| **browser** | the page API and socket → the agent's | After the membership and running checks it adds only the bearer. **It does not interpret the bodies or the text frames**, and binary JPEG frames are relayed latest-only. Only a *visible* viewer keeps the workspace warm |
| **preview** | `/preview/{port}/…` → the agent → `127.0.0.1:{port}` in the container | Adds the forwarding headers; the agent strips the authorization before proxying. A new tab cannot carry a header, so the tenant falls back to a query parameter. **Limitation: HTTP only — no WebSocket or SSE, so no HMR.** The app must honour the forwarded prefix |

## 5.4 Cross-cutting rules

- **Choosing a tenant**: a header, with a query fallback for WebSockets, preview and
  new tabs. One membership resolves automatically; several with none specified is a
  409; one you do not belong to is a 403.
- **Error shape**: JSON with a **stable error-code string** — these are constants and
  part of the wire contract.
- **Authorisation**: your own workspace, repositories and sessions only. Admin APIs are
  role-gated, and a tenant administrator sees only their own tenant.
- The Console bundle is served `no-store`, so a deployment is live immediately.

## 5.5 Where audit is written

The REST proxy records **mutating** operations on a 2xx: filesystem writes, repository
clone and delete, git operations, session create / fork / stop. On top of that the
admin API, the MCP write tools and system actions such as the reaper record from inside
their own handlers. The schema is [06](06-data.md); the operational view is
[07 §7.7](07-security.md).

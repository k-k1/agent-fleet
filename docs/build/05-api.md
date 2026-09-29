---
audience: "someone touching an API boundary"
source_of_truth: "the code (this is the map and the invariants; individual request and response shapes are code-as-contract)"
updated: "2026-09"
---

# 05. API boundaries and relaying — the code is the contract

English | [日本語](05-api.ja.md)

There are two boundaries: the **public** one (Console ↔ CP) and the **internal** one
(CP ↔ workspace agent). Around them sit the calls a workspace makes into the CP (§5.2).
The CP registers about 550 routes and the agent about 320, spread over many files. The
complete list is `testdata/routes.golden` in each module. `TestRouteTableGolden`
regenerates it, and a diff there is the review signal for a route change. The CP's list
is taken with `AF_MCP_ENABLED=true`. It leaves out the routes that exist only on some
deployments: the engine gateway, and the native self-update. Enumerating every route
here would be unmaintainable, so this chapter is strictly a **map**: group → representative
paths → who handles it → where the detail is.

Changing a path, a JSON key or an error-code string is a wire change. The shapes are pinned
by the goldens next to the route lists: `testdata/wire.golden` (the key sets of the DTOs the
Console reads) and `testdata/wiremap.golden` (the JSON write sites that still return a map).

## 5.1 The public surface

Reached after the L1 gate ([07 §7.3](07-security.md)). Authorisation is "your own resources
only", plus the membership check (§5.4). "Relayed" means the CP forwards the call to the
agent unchanged (§5.3). "CP" means the CP answers it itself, usually from its database.

| Group | Representative paths | Handled | Detail |
|---|---|---|---|
| identity / tenant | `GET /api/whoami`, `GET /api/tenants`, `GET /api/version`, `GET/DELETE /api/me/login-methods` | CP | [03](03-control-plane.md) |
| events | `GET /api/events` (SSE; see below) | CP | [ADR 0084](../decisions/0084-engine-indicator-and-tenant-gate.md) |
| workspace | `GET /api/workspace`, `POST /api/workspace/{start,stop,recreate,clean-home,attention}`, `GET /api/workspace/{stats,machine}` | CP (Runtime). `stats` asks the agent where the CP cannot read the host's cgroup; `machine` merges the agent's measurement with what the configuration declares | [03](03-control-plane.md) |
| sessions | `GET/POST /api/sessions`; `DELETE /api/sessions/{name}` (to the trash; `?stop=1` stops it first; `POST …/stop` is its old name); lifecycle `POST …/{halt,recreate,archive,restore,fork,start,lock}`; semantic `POST …/{turn,respond,settings,driver,plan-respond,carried-answer}`; terminal `POST …/{input,paste-image}`; `GET …/{status,output,messages,settings,skills,plan-file}`; `POST …/{keep-awake,stop-after-turn}`; title, branch, marks, translation, handoff proposal; `GET /api/sessions/archived` | create, fork, start and carried-answer are CP handlers (the session quota, auto-start), which then relay. The rest is relayed | [04](04-agent.md) / [ADR 0101](../decisions/0101-session-delete-via-trash.md) |
| ↳ the optional fork body | `POST …/fork` with `{"at": <anchorId>, "include": bool}` **branches from a past message** ([ADR 0039](../decisions/0039-fork-at-message.md)). Omitting it copies the whole conversation, the backward-compatible behaviour. **Malformed JSON is `400 bad_request`; it never silently falls back to copying everything.** An unusable anchor is `400 fork_bad_anchor`; a kind or execution method without point forks is `400 fork_at_unsupported` | the CP passes the body through | [04](04-agent.md) |
| sharing and handoff | `/api/session-shares*`, `/api/shared-sessions*` (messages, marks, proposals), `/api/session-share-proposals*`, `/api/session-handoff-offers*`, `GET /api/sessions/{name}/handoff-recipients` | CP. The ACL lives in its database, and the CP reads the owner's agent itself (§5.2) | [ADR 0050](../decisions/0050-transcript-marks.md) / [ADR 0057](../decisions/0057-member-handoff.md) |
| repos (SCM) | `GET/POST /api/repos`; `POST /api/repos/svn`; the import jobs `GET /api/repo-jobs`, `DELETE /api/repo-jobs/{id}`; a working copy with no upstream, `POST /api/repos/init` (`{name}` → `201 {repo}`; just mkdir plus `git init`, so it is **synchronous** and skips the import job); `/api/repos/{name}/{status,branches,checkout,fetch,ff,parent-ff,changes,diff,log,graph,show,stage,unstage,discard,commit,identity,prompt-templates,recreate,lock}`; `/api/repos/{name}/{svn-update,svn-cleanup,svn-auth}`; project MCP `/api/repos/{name}/mcp*`; `GET/PUT /api/git/identity` | relayed | [04](04-agent.md) / [ADR 0059](../decisions/0059-repo-import-jobs.md) |
| branch naming | `GET /api/repos/{name}/branch-rule` (the effective rule; `?refresh=1` reads Bitbucket again), `POST /api/repos/{name}/branch-name` (`{item?, session?, kind?, slug?}` → `{name, name_empty, base, base_branch, kind, provisional, warnings, sources}`), `POST /api/repos/{name}/branch-name/check` (`{name}` → `{warnings}`; never refuses), `GET/PUT /api/branch-rules/user` (the user layer, a store of its own, not ui-prefs), `POST /api/branch-rules/preview` (`{template, items}` → `{names}`; the work-items settings' preview over the user and built-in layers only). A work-item launch sends `work_item` on `POST /api/sessions` for the session meta, and `POST /api/sessions/{name}/suggest-branch` also answers `kind` and `slug` | relayed | [ADR 0103](../decisions/0103-branch-naming-rules.md) |
| fs | `GET /api/fs/{tree,search,file,download,changes,linemarks}`, `PUT /api/fs/file`, `POST /api/fs/{upload,mkdir,newfile,rename,resolve,suggest-edit}`, `DELETE /api/fs/delete` | relayed. The CP checks `PUT /api/fs/file`'s envelope and the running state first | [04](04-agent.md) / [ADR 0027](../decisions/0027-markdown-code-editor.md) |
| connections | `GET /api/connections`; per-host git `PUT/DELETE /api/connections/git/{host}`; the agent-CLI logins (`/api/connections/{claude,codex,opencode,agy,cursor,kiro,muse}/…`); the ops, chat-bridge, SVN, Jira and llama.cpp credentials; `GET /api/git-oauth` (which OAuth buttons this tenant can offer) | relayed. **Except:** the git-provider OAuth flows (GitHub's device flow, Bitbucket's code grant and callback) and Jira's OAuth start and callback are handled by the CP itself ([ADR 0052](../decisions/0052-tenant-git-oauth.md)). A login's start and poll legs answer `409 workspace_starting` until the workspace has settled, because their state lives only in the agent process | [08](08-integrations.md) |
| work items | `GET /api/work-items`, `POST /api/work-items/{refresh,search,detail,comment}`, `/api/work-item-queries*`, `/api/work-item-sessions*` | CP. Saved queries and the ledger are its rows; fetching is the CP calling the agent (§5.2); detail and comment are relayed only on a person's press | [ADR 0061](../decisions/0061-work-item-inbox.md) |
| chat / assistants | `/api/chat/conversations*` (streaming is SSE; deletion lock `POST …/{id}/lock`), `POST /api/chat/ask`, `/api/assistants*` | relayed | [04](04-agent.md) |
| image generation | `GET /api/imagegen/{status,jobs,props,history,knowledge}`, `POST /api/imagegen/{jobs,queue,groups/{id}}`, `DELETE /api/imagegen/jobs/{id}`, `POST/PUT /api/imagegen/knowledge`; the studios `/api/imagegen/studios*` (`bind`, `press`, `rewind`, `draft-log`, `persona`; a studio `PUT` carries `If-Match`) | relayed, all plain REST: enqueueing answers at once and the pane polls. The agent's blocking `POST /imagegen/generate` is the MCP tool's door and has no CP route | [ADR 0081](../decisions/0081-image-generation-pane.md) / [ADR 0100](../decisions/0100-image-generation-studio.md) |
| engines (member) | `GET /api/engines/status` | CP; the REST fallback of the `engines` event stream | [ADR 0084](../decisions/0084-engine-indicator-and-tenant-gate.md) |
| AWS login | `GET /api/aws-login`, `POST /api/aws-login/{id}/{start,cancel}`, `GET /api/aws-login/{id}/attempts/{attempt}`, `GET /api/aws-login/profiles`, `POST /api/aws-login/profiles/{name}/start` and its attempt poll | relayed. Start and the attempt polls answer `409 workspace_starting` like the connection logins | [ADR 0102](../decisions/0102-aws-login-through-the-console.md) |
| env / settings | `/api/env/{toolchains,ui-prefs,databases,jdk-install,node-install,tool-versions}`, `GET/PUT /api/env/ws-settings`, `POST /api/env/ws-settings/preview/reissue`, `/api/{claude,codex}/settings`, `GET /api/{claude,codex,copilot,muse}/usage` (agy's is `GET /api/connections/agy/usage`), `/api/agents/rtk`, `GET /api/agents/rtk/gain`, `GET /api/agents/{kind}/models`, `/api/user-notes*`, `GET /api/ai-assist/resolution` | `ws-settings` is the CP's (editable while stopped, applied at start); the rest is relayed | [04](04-agent.md) |
| memo | `GET/POST/PATCH/DELETE /api/memos*`, `/api/memo-categories*`, `POST /api/memos/flush`, `POST /api/memos/paste-image`, `GET /api/memos/images/{file}`, `POST /api/memos/images/gc` | CP; flush and the image attachments reach the agent | [03](03-control-plane.md) |
| notifications | `GET /api/notifications`, `POST /api/notifications/{seen,usage-observations}` | CP; it drains the agent's outbox (§5.2) | [03](03-control-plane.md) |
| schedules | `GET /api/schedules`, `GET …/{id}/runs`, `PATCH/DELETE …/{id}`, `POST …/{id}/{pause,resume,run-now}`. **The Console lists and edits only; creation goes through the MCP tool** | CP (database and scheduler) | [ADR 0021](../decisions/0021-scheduled-execution.md) |
| cleanup and the trash | `GET /api/sessions/{usage,cleanup}`; the trash `GET /api/cleanup/archives`, `POST …/archives/{id}/restore`, `DELETE …/archives/{id}`, `DELETE /api/cleanup/archives?older_than_days=N`; `GET /api/cleanup/usage`, `DELETE /api/cleanup/cache/{feature}`, `/api/cleanup/{tool-caches,leftovers}*` | relayed | [ADR 0101](../decisions/0101-session-delete-via-trash.md) |
| fleet graph | `GET /api/fleet-graph` | relayed | [ADR 0096](../decisions/0096-fleet-session-graph.md) |
| agent memory | `GET /api/agents/memory/{roots,snapshots,diff,tree,export}`, `POST …/{snapshots,restore,import,import/apply}`, `PUT …/settings` | relayed | [ADR 0022](../decisions/0022-agent-memory-management.md) |
| MCP registry | `GET/POST /api/mcp-servers`, `PUT/DELETE …/{id}`, `POST …/{test,tenant-refresh}`, `POST …/{id}/enabled`, `PUT …/{id}/secrets` | relayed (the agent composes the effective registry). The tenant's distributed set is the admin API's `/api/admin/mcp-servers*` | [ADR 0031](../decisions/0031-mcp-registry.md) |
| usage and cost | `GET /api/usage/series`, `GET /api/usage/me/hourly`, `GET /api/cost/{profile,me}` | the series is relayed; the hourly occupancy and the cloud cost are CP | [ADR 0029](../decisions/0029-usage-accounting.md) / [ADR 0066](../decisions/0066-uptime-heatmap.md) / [ADR 0048](../decisions/0048-member-cloud-cost.md) |
| pat | `GET/POST /api/pat`, `DELETE /api/pat/{id}` | CP | [07 §7.6](07-security.md) |
| ssm | `GET/POST/PUT/DELETE /api/ssm/{profiles,hosts}*`, `POST /api/ssm/instances`, `GET /api/sessions/{name}/ssm-login` | profiles and hosts are CP; the instance lookup resolves the profile on the CP and then relays; the login status is relayed | [08](08-integrations.md) |
| egress (member) | `GET /api/egress/check`, `POST /api/egress/propose` | CP. A proposal only creates a *proposed* allowlist entry; approval is the super_admin's | [07 §7.8](07-security.md) |
| TTS | `POST /api/tts/{synthesize,wake}`, `GET /api/tts/{status,speakers,dict}` | CP (it calls the voice engine) | [ADR 0070](../decisions/0070-tts-ondemand-engine.md) |
| internal git | `/api/internal-git/repos*` (management and read-only browsing), `/git/{slug}/{repo...}` (smart HTTP), `/git/{slug}/{repo}/info/lfs/*` | CP, **not through the agent** | [91](91-internal-git.md) |
| admin | tenants (members, limits, login rules, network, slot class, sign-in providers, git OAuth apps), memberships and workspaces, user limits and roles, host, EC2 pool, workspace sizing, usage and cloud cost, sessions, audit, egress, MCP distribution, branding, TTS, engines (`/api/admin/engines*`) | CP, role-gated (§5.4) | [03](03-control-plane.md) |
| MCP | `/mcp` (Streamable HTTP JSON-RPC, bearer PAT, outside the session gate). Registered only with `AF_MCP_ENABLED=true` | CP | [03 §3.5](03-control-plane.md) / [ADR 0006](../decisions/0006-mcp-unified.md) |
| preview | `/preview/{port}/{rest...}` (`/preview/{port}` answers 301 to add the slash); host mode `{slug}-{port}.<AF_PREVIEW_DOMAIN>` with `/preview-auth` and `/preview-open`; `GET /api/preview/shared` | CP → agent `/proxy/{port}/…` | §5.3 / [ADR 0062](../decisions/0062-preview-subdomain.md) |
| browser | `POST /api/browser/pages`, `GET/DELETE /api/browser/pages/{id}`, `GET /api/browser/attach-targets`, `/api/browser/attachments*`, `GET /ws/browser`, `GET /ws/browser-attachments`; `GET /open/browser-attachment/{id}` serves the Console shell | CP → agent | §5.3 / [ADR 0018](../decisions/0018-container-browser-pane.md) / [ADR 0038](../decisions/0038-chromium-attach-view.md) |
| terminal | `GET /ws/terminal?session=&tenant=` | CP → agent `/ws/pty` | §5.3 |
| the rest | `GET /api/drawio/stencils*` (a CP-side cache; [ADR 0046](../decisions/0046-drawio-viewer.md)); `GET /api/update/status`, `POST /api/update/apply` (native only; [ADR 0025](../decisions/0025-native-auto-update.md)); login and OAuth (`/login`, `/login/{slug}`, `/oauth2/{login,callback,logout,link}`); `GET /healthz` and `GET /readyz` ([09 §9.9](09-deploy.md)); `/manifest.webmanifest` and `/brand/*`; `/agent-fleet*` (a 302 to the same path at the root, for old bookmarks); `/` (the Console) | CP | [07](07-security.md) |

- **`GET /api/events`** is one SSE connection per tab. It replaces the Console's permanent
  polls. Every 4 s it frames only the streams whose JSON changed:
  `data: {"stream": <name>, "data": <the matching REST body>}`. The streams are `workspace`,
  `stats`, `sessions`, `notifications`, `workitems` and `engines`, and a comment ping goes
  out after 20 s of silence. A CP without the route answers 404, and the Console falls back
  to polling the REST equivalents. The stream never counts as activity for idle-stop.
- **Long operations answer at once and are polled.** Workspace start returns and the
  Console watches its state. Clone and svn checkout are import jobs (`202 {job}`, polled on
  `GET /api/repo-jobs`). Image generation enqueues, and the pane polls the queue. There is
  no general job queue.
- **Auto-start.** When `AF_AUTOSTART` is on (the default), session create, fork, start,
  carried-answer and `POST /api/ssm/instances` start a stopped workspace first. Nothing else
  wakes one: not a terminal attach, not `attention`, not the event stream.
- **Held routes.** A model can take longer to answer than the ingress idle timeout (60 s), so
  `POST /api/chat/conversations/{id}/{compact,plan/refresh}`, `POST /api/chat/ask` and
  `POST /api/fs/suggest-edit` answer a request carrying `Accept: text/event-stream` with 200,
  a `: keepalive` comment every 20 s, and one final frame
  `data: {"status": <the status>, "body": <the JSON body>}`. Without that header they answer
  plain JSON (the MCP `ask_assistant` tool relies on this). The CP relays them
  through its flushing stream proxy (`httpx.HeldOpen` on the agent side).
- `GET /api/workspace/stats` answers `{running: true, mem_used, mem_max?, cpu_pct?,
  oom_kill_total?, oom_recent?}` while the workspace runs. On `docker` the CP reads the
  container's cgroup from the host. Elsewhere it asks the agent's `GET /workspace/stats`.
  `oom_recent` is derived on the CP from a rise in the kill counter, on both paths. A
  stopped workspace answers `{running: false}`. Only on `docker` can the CP inspect the
  stopped container, so only there does it add `oom_killed` and `exit_code`
  ([ADR 0014](../decisions/0014-agent-exit-recording.md)). Per-session exit reasons
  (`exitReason`, `exitCode`, `exitSignal`) ride on each element of `GET /api/sessions`.
- `GET /api/sessions` is served from the agent while the workspace runs, and from the CP's
  mirror of the last list while it is stopped, so stopped sessions stay visible and
  resumable.

## 5.2 The internal surface

- **Reachability.** The agent listens on `AGENT_ADDR` (`:7700` by default). On `docker` that
  port is published on the host's loopback only, `native` binds loopback, and the AWS
  targets restrict it with security groups ([07 §7.2](07-security.md)). Processes inside the
  workspace call it on loopback too: the af MCP server and the hooks use the same
  `AGENT_TOKEN`.
- Every request carries a per-container bearer token that the CP injects at start. The
  agent checks it on everything except `/healthz`, **in constant time**, and answers
  `401 unauthorized` otherwise ([07 §7.5](07-security.md)).
- **Path convention: the CP strips `/api` and forwards the rest unchanged**
  (`/api/sessions/x/halt` → `<agent>/sessions/x/halt`). **The CP's route table is an explicit
  allowlist**: an agent route with no line in `control-plane/routes.go` cannot be reached
  from the Console. The relay rejects `.`, `..` and empty interior path segments with 400.
  It does not follow the agent's redirects. It adds `X-AF-Relay: cp`, a hint for the
  agent's log that the agent never decides anything on.
- The agent-specific surfaces are `/ws/pty`, `/ws/browser`, `/ws/browser-attachments`,
  `/browser/*` and `/proxy/{port}/{rest...}`.

The session API separates **semantic** operations from **terminal** ones. Turn, respond and
settings are driver-independent: the agent sends them to a managed driver's structured API,
or as keystrokes to a TUI. `/input` takes a prompt on either driver, but raw `keys` and `seq`
only on a TUI. `/output` needs a kind that has a transcript, and the PTY socket exists only
for a session that has a pane. `/driver` moves one conversation between execution methods by
stopping and resuming it. It refuses a kind that lacks the target method
(`400 driver_unsupported`) and a session mid-turn (`409 busy_switch`).

**Calls the CP makes itself.** No Console route leads to these:

- `GET /sessions` — the list the CP mirrors into its database.
- `GET /workspace/{stats,machine}`.
- `GET /notifications` and `POST /notifications/ack` — the notification outbox, drained by
  the CP.
- `POST /work-items/fetch` — the timer-driven fetch of the saved queries the CP owns.
- `GET /sessions/catalog`, `GET /share-operations/{key}` and
  `GET /sessions/{name}/handoff-context` — sharing and handoff.
- `POST /engine/usage` and `POST /engine/catalog-changed` — the engine gateway's token count,
  and the model catalogue's change notice.
- `POST /assistant-turns` — scheduled assistant runs.

The af MCP server inside the workspace has doors of its own that the CP does not route
(`POST /imagegen/generate`, `GET /sessions-idempotency/{key}`, `POST /chat/report`, …).

**Calls from the workspace into the CP.** All of them sit outside the session gate and
authenticate themselves. Where a token is per membership, it is signed for one membership,
and the CP resolves that membership on each call.

| Path | Credential | Purpose |
|---|---|---|
| `/internal/memos*`, `/internal/memo-categories*` | `AF_MEMO_TOKEN` | the operator's memo tools |
| `/internal/schedules*` | `AF_SCHEDULE_TOKEN` | the operator's schedule tools, including create |
| `GET /internal/mcp-servers` | `AF_MCP_TOKEN` | the poll for the tenant's distributed MCP servers |
| `GET /internal/docs` | `AF_DOCS_TOKEN` | the role-scoped guide as a tar.gz ([04 §4.9](04-agent.md)) |
| `GET /internal/aws-profiles` | `AF_AWS_PROFILES_TOKEN` | the member's profiles, written into `~/.aws/config` |
| `POST /internal/git-oauth/{bitbucket,jira}/refresh` | `AF_GIT_OAUTH_TOKEN` | the refresh grant runs on the CP, so the tenant's client secret stays there ([ADR 0052](../decisions/0052-tenant-git-oauth.md)) |
| `POST /internal/engine/token`, `GET /internal/engine/catalog` | `AF_ENGINE_ISSUE_TOKEN` | a session-scoped engine token, and the engine catalogue the launch menu reads |
| `/engine/{key}/v1/{path...}`, `GET /engine/{key}/props` | the session-scoped engine token (`AF_ENGINE_TOKEN` in the session) | **the engine gateway**, the only way a workspace reaches a self-hosted engine. Registered only on a deployment with engines. A streaming request gets 200 at once and a comment line every 10 s while the engine starts. A non-streaming one is held below the ingress idle timeout, then gets `503 engine_waking` with `Retry-After` ([ADR 0071](../decisions/0071-self-hosted-inference-engines.md)) |
| `/mcp` | a PAT | the MCP endpoint (§5.1) |
| `/git/{slug}/{repo...}` | a git token, basic auth | the internal git provider ([91](91-internal-git.md)) |

The egress proxy, not a workspace, posts to `POST /internal/egress` and reads
`GET /internal/egress/policy` with the deployment-wide `AF_EGRESS_TOKEN`
([07 §7.8](07-security.md)).

## 5.3 The five relay paths

| Path | In → out | Character |
|---|---|---|
| **REST** | `/api/*` → the agent's same path | Only mutating calls count as activity; GET polling never keeps a workspace warm. Mutating calls are audited on a 2xx (§5.5). The relay itself does not check the state: a workspace that is not running answers `502` ("workspace agent unreachable"). A write while the workspace is stopping is `409 workspace_stopping`. A delete of a session or repository that a schedule still uses is `409 schedule_in_use` |
| **SSE** | the chat stream and the held routes → the agent | Flushed per chunk |
| **WS** | `/ws/terminal` → the agent's `/ws/pty` | Checks the state first (**it never auto-starts**): `409 workspace_starting` or `409 workspace_stopped`. Then it relays both ways: binary is PTY output, text is input and resize. A close frame from either side is passed through. Only keystroke frames count as presence for idle-stop; pings, resizes and an open socket do not |
| **browser** | the page and attachment API and their sockets → the agent's | After the membership and running checks it adds only the bearer. **It does not interpret the bodies or the text frames**, and binary JPEG frames are relayed latest-only. Only a *visible* viewer keeps the workspace warm |
| **preview** | `/preview/{port}/…` or `{slug}-{port}.<AF_PREVIEW_DOMAIN>` → the agent's `/proxy/{port}/…` → `127.0.0.1:{port}` in the container | One reverse proxy for both URL shapes, so WebSocket and streaming responses pass through. It sets `X-Forwarded-{Host,Proto}`, and `X-Forwarded-Prefix` on the path route, where the app must honour the prefix. The agent strips `Authorization`, and the CP's own login cookies never reach the app. On the path route a new tab cannot carry a header, so the tenant comes from `?tenant=`, then from a per-port cookie. The host route has a handshake and cookie of its own (`/preview-auth`), outside the Console's session gate. Vite's HMR through it is not yet confirmed (#968; [ADR 0062](../decisions/0062-preview-subdomain.md)) |

## 5.4 Cross-cutting rules

- **Choosing a tenant**: the `X-AF-Tenant` header, falling back to `?tenant=` for WebSockets,
  preview and new tabs. One membership resolves automatically. Several with none specified
  is `409 tenant_selection_required`. One you do not belong to is `403 forbidden_tenant`.
  A tenant whose network rule refuses the caller's address answers `403 ip_not_allowed`.
- **Error shape**: `{"error": {"code": <string>, "message": <string>}}` from both the CP and
  the agent. The **code is the contract**: the Console localises it as `err.<code>`
  (`console/src/lib/i18n/locales/*/errors.ts`), so renaming one is a wire change on both
  sides. Many are declared in each module's `errcodes.go`. The usual statuses: 401
  `unauthenticated`; 403 (membership, network, role); 409 (tenant selection, workspace
  state, a busy session); 429 (`quota_sessions` and the other quotas, unlimited by default;
  a few rate limits); 502 when the relay cannot reach the agent; 503 `engine_waking` or
  `engine_off` from the engine gateway.
- **Authorisation**: your own workspace, repositories and sessions only. Admin APIs are
  role-gated. A super_admin sees the whole deployment. A tenant_admin sees only their own
  tenant, checked inside the handler for per-tenant routes. The engine list and ingest
  routes also admit a tenant_admin whose tenant the operator allowed to ingest.
- **Caching**: an ordinary `200` JSON `GET` gets a weak `ETag`, and an unchanged body answers
  `304` (`etagJSON`). A response marked `no-store`, a body over 4 MiB, a handler that flushes
  mid-response, SSE and file downloads pass through untouched. The Console's unhashed entry
  points (`index.html` and the like) are served `no-store`, so a deployment is live at the
  next load. The content-hashed files under `/assets/` are cached for a year as immutable.

## 5.5 Where audit is written

The REST relay records **mutating** operations on a 2xx (`auditActionTarget`). The target
comes from the URL path or query. The one exception is `PUT /api/fs/file`: the CP has already
checked its JSON body, and uses the body's `path`. File contents are never stored in the audit
log:

- filesystem writes (`fs.*`);
- repository clone, svn checkout and delete, and import-job cancel (`repo.*`);
- git commit, discard, checkout, fetch, ff and parent-ff (`git.*`);
- memory snapshot, import and restore (`memory.*`);
- AWS login start and cancel (`aws.login.*`);
- session create, fork and delete (`session.*`; `POST …/stop` is recorded as the delete it is).

It also records two special cases:

- `GET /api/agents/memory/export` — the one audited read, since it carries personal memory
  out of the environment;
- a `PUT /api/fs/file` that failed with `write_state_unknown`.

On top of that, the admin API, the MCP write tools and system actions such as the reaper
record from inside their own handlers. The schema is [06](06-data.md); the operational view
is [07 §7.7](07-security.md).

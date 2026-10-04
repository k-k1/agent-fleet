---
audience: "someone adding an external provider or a CLI agent"
source_of_truth: "the code (this is a map of the methods and the intent)"
updated: "2026-09"
---

# 08. External integrations

English | [日本語](08-integrations.ja.md)

Everything about talking to an outside provider is collected here. **Two patterns
recur**, and knowing which one you are in decides most of the design:

- **(a) No callback required** — a device flow, pasting a code back, a pasted token, or
  a connection the workspace opens outwards and keeps open. It works regardless of what
  sits at the edge.
- **(b) The CP owns a callback** — which means the CP needs a reachable public URL
  (`PUBLIC_BASE_URL`) and the redirect URI must be registered at the provider exactly.

**When considering a new provider, the house rule is to look for (a) first.** Only three
callbacks exist today: Console sign-in (`/oauth2/callback`), Bitbucket
(`/api/oauth/bitbucket/callback`) and Jira (`/api/oauth/jira/callback`). Every agent CLI
fits (a).

## 8.1 The integrations

| Party | Purpose | Method | Callback | Credential stored |
|---|---|---|---|---|
| Google, GitHub, any OIDC provider | L1 Console sign-in | OAuth authorisation code / OIDC, natively in the CP | CP | a signed cookie; the apps' client secrets are CP environment variables or a sealed tenant row (§8.2) |
| GitHub | git auth | a pasted token, or **a device flow run by the CP using the tenant's app** | none | the encrypted store |
| Bitbucket | git auth | a pasted email + token, or **an authorisation code flow with a CP-owned callback, using the tenant's app** | CP | the encrypted store; the refresh goes through the CP (§8.4.1) |
| Jira | the work-item inbox | a pasted email + API token, or **Atlassian OAuth (3LO) with a CP-owned callback, using the tenant's app** | CP | the encrypted store (§8.4.2) |
| SVN servers | checkouts | a pasted username + password | none | the encrypted store, fed to `svn` by a wrapper |
| Internal git | git hosting | a per-membership HMAC token | — | derived by the CP, never stored there; seeded into the agent's store ([91](91-internal-git.md)) |
| Agent CLIs (claude, codex, opencode, cursor, kiro, agy, muse, copilot) | L2 agent auth | the CLI's own login driven by the agent, a pasted key, or another connection (§8.5, §8.6) | none | the CLI's own credentials file, or the encrypted store (§8.5, §8.6) |
| External MCP clients | driving the fleet | a bearer PAT | — | only a hash, in the database ([06](06-data.md)) |
| MCP servers a user or tenant adds | tools for the agents | whatever the server wants, written into each CLI's config | — | each CLI's own config file; per-user header secrets in the encrypted store (§8.7) |
| AWS, the user's accounts | SSM sessions, `af-aws-exec` | SSO device code, started from the Console | none | the SSO cache **inside the workspace; the CP never sees it** (§8.8) |
| AWS, the deployment's own | the ECS runtimes, engines, cost, speech | the SDK with the CP's IAM role | — | nothing (§8.8) |
| Engines — the deployment's own, an external one, or another deployment's | local LLM and image generation | a gateway token the agent buys from the CP, per session or per workspace; the CP adds the upstream credential | none | upstream keys in the CP environment or SSM; the borrowing token in the CP environment; a member's own llama.cpp server in the encrypted store (§8.9) |
| Hugging Face, Civitai | model ingest | an API token | none | a sealed deployment setting (§8.9) |
| Discord, Slack | the chat bridge | a bot token (+ Slack's app-level token), outbound WebSocket | none | the encrypted store (§8.10) |
| PagerDuty, Grafana | the ops-tool MCP servers | an API token | none | the encrypted store (§8.10) |
| models.dev | model prices | an anonymous GET, once a day | none | — |
| Caddy / ALB / Tailscale Funnel | ingress and TLS | infrastructure, outside the code | — | — ([09 §9.3](09-deploy.md)) |

"The encrypted store" is the agent's `secrets.enc`, one per membership
([04 §4.8](04-agent.md)).

The design principle for connections: **a member's secret passes through the CP but is
never held or interpreted by it** ([07 §7.6](07-security.md)). What the CP does hold are
the deployment's and the tenants' own app credentials — OAuth client secrets, the model
ingest tokens — sealed with the master key. Connection state is exposed in one place,
`GET /api/connections`, which is a relay to the agent and so answers 502 while the
workspace is stopped. A provider API called only for display (the account name) is hit
**once per connection and cached in the store** — never on the polling loop.

## 8.2 Console sign-in (L1)

Implemented natively in the CP; the flow, the allowlist, the providers and the gate's
defences are [07 §7.3](07-security.md). What matters here:

- `AUTH=oauth` refuses to start without `AF_COOKIE_SECRET`, `PUBLIC_BASE_URL` and at
  least one provider: Google (`GOOGLE_OAUTH_CLIENT_ID` / `_SECRET`), any OIDC issuer
  (`AF_OIDC_PROVIDERS` with `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST}`), or
  GitHub (`AF_GITHUB_LOGIN_CLIENT_ID` / `_SECRET`, falling back to
  `GITHUB_OAUTH_CLIENT_*`, and enabled only when `AF_GITHUB_ALLOWED_ORGS` is set). A
  tenant may add its own IdPs as rows, whose secret is sealed
  ([07 §7.3.1](07-security.md)).
- Every provider shares one redirect URI, `<PUBLIC_BASE_URL>/oauth2/callback`, which must
  be registered at the provider **exactly**.
- No provider credential is kept after sign-in, with one exception: the GitHub adapter
  holds the user's access token **in memory** to re-check organisation membership.
- The GitHub **sign-in** app has nothing to do with the GitHub **git** app of §8.3.

## 8.3 GitHub

- **Pasted token**: `PUT /api/connections/git/github.com`, served to git by the
  credential helper as `x-access-token` + token.
- **Device flow** (the primary path): `POST /api/connections/git/github/oauth/{start,poll}`,
  run by the CP with the client id from **the tenant's row** (§8.4.1). No secret is
  needed, and **the app must have device flow enabled**. The user approves the code in
  their browser; the Console polls the CP, and each poll is one token request to
  GitHub. The scope is `repo workflow` (`ghDeviceScope`); a token issued earlier keeps
  the scope it had. The resulting token goes to the agent through the same entry point
  a pasted token uses. Needing no callback, **it works behind any edge**.
- The flow lives in the CP's memory (`ghDeviceFlows`): only the user who started it may
  poll it, and it must finish on the CP instance that started it.
- Listing remote repositories uses REST (`/user/repos`, most recently updated first);
  listing branches uses GraphQL, newest commit first.
- The connections accept only `github.com` and `bitbucket.org`; any other host is
  `bad_host`. **GitHub Enterprise and GitLab have no connection.**

### Making `gh` work without a separate login

`gh` does not read git's credential helper; it looks only at `GH_TOKEN` /
`GITHUB_TOKEN` or its own config. Left alone, a user with a token already stored in
Connections would still have to run `gh auth login`.

To avoid that, the image installs a **thin wrapper** as `/usr/local/bin/gh`
(`workspace/gh-auth-wrapper.sh`) and moves the real binary to `/usr/local/libexec/gh`.
On every call the wrapper runs `git credential fill` for `github.com` — **the same helper
git uses**, `workspace-agent cred` — and exports the token as `GH_TOKEN` before exec'ing
the real `gh`. Everyone gets a working `gh` with no extra login, **at the same freshness
as git**, and it self-heals across rotation because it fetches each time.

**Limits worth knowing:**

- **Scope**: the token carries the device flow's scope. Most of `gh` works;
  organisation-level calls may fail for want of `read:org`. There is no setting to
  widen it — a user who needs more pastes a token with more scope.
- **GitHub Enterprise is not covered** — the wrapper injects a github.com token only.
- **An explicit token wins**: if `GH_TOKEN` or `GITHUB_TOKEN` is already set, the
  wrapper does not override it.
- **Cost**: one helper invocation per `gh` call, comparable to a git push or fetch.
- **Home shadowing**: a real `gh` in the user's own `~/.local/bin` would come first on
  `PATH` and hide the wrapper, so the entrypoint removes a non-symlink there at start.
- **Only in the image**: the `native` target has no wrapper.

## 8.4 Bitbucket

- **Pasted**: an Atlassian email plus an API token (Basic auth for the REST API). For
  git, an email username is rewritten to `x-bitbucket-api-token-auth`; an app-password
  account name is passed through as it is.
- **OAuth**: `GET /api/connections/git/bitbucket/oauth/start` returns the authorise URL;
  the provider redirects to `GET /api/oauth/bitbucket/callback`. The consumer's key and
  secret are read from **the tenant's row**
  ([decisions/0052](../decisions/0052-tenant-git-oauth.md)). Without `PUBLIC_BASE_URL`
  the start fails with `no_public_base_url`. The browser's own CP session carries the
  callback through the gate, so **no exemption is needed**. The token goes to the agent
  (`PUT /connections/git/bitbucket/oauth`) without the key or secret.
  **The tenant travels with the state.** The callback is a plain redirect from the
  provider and carries no tenant header, so the CP keeps the user and tenant in memory
  under the state (`bbFlows`); resolving the tenant any other way could exchange the
  code against **another tenant's app**.
- **Refresh**: access tokens expire, so the credential helper renews with the stored
  refresh token when fewer than two minutes remain, and serves `x-token-auth` + token.
  There is one helper for every host, `workspace-agent cred`; `bitbucket-cred` survives
  only as an alias for old git configs.
- Listing remotes reads `GET /2.0/user/workspaces`, then each workspace's repositories
  (capped at 500, most recently updated first). The old `?role=member` listing answers
  410.

### 8.4.1 The OAuth apps belong to **the tenant**

The apps are rows in `tenant_git_oauth` — one each for `github`, `bitbucket` and `jira`
— registered by a tenant administrator from the Console
(`/api/admin/tenants/{slug}/git-oauth`). The secret is write-only and sealed with the
master key. **The environment is not read**: `GITHUB_OAUTH_CLIENT_ID` is now for
*sign-in* only (§8.2), and `BITBUCKET_OAUTH_KEY` / `_SECRET` are referenced from
nowhere.

- **The CP runs GitHub's device flow.** The agent used to, using a client id from the
  container's environment — but **that environment is fixed at container start, and
  there are four runtime implementations**, so making it per-tenant would have required
  restarting everybody's workspace for a change to land.
- **Whether to show a button is answered by a CP-native endpoint**, `GET /api/git-oauth`.
  `GET /api/connections` is a relay to the agent and fails while the workspace is
  stopped, so it cannot decide that. Saving the resulting token still needs a running
  workspace.
- **The CP runs the refresh too**: `POST /internal/git-oauth/bitbucket/refresh` and
  `POST /internal/git-oauth/jira/refresh` (`git_oauth_bridge.go`), authenticated with the
  per-membership `AF_GIT_OAUTH_TOKEN`. The tenant comes from the token, never from the
  request. It used to hand the key and secret to the agent, which meant **the tenant's
  client secret was copied into every member's encrypted store**. Now the agent sends
  the refresh token and the CP adds the secret.
  **The refresh token does not move** — it stays in the workspace and the CP does not
  store it, preserving "the CP passes secrets through but does not hold them". Old
  copies of the key and secret are destroyed **once the bridge has succeeded once**, and
  kept until then as a fallback. Deleting the tenant's app makes the next refresh fail
  with `not_configured`.
  The bridge's coordinates live in the encrypted store (`GitOAuthBridge`), **not in the
  environment**: the credential helper is a separate process started by git, whose
  environment cannot be guaranteed.

### 8.4.2 Jira

Jira feeds the work-item inbox ([decisions/0061](../decisions/0061-work-item-inbox.md)),
not git, but shares the machinery above.

- **Pasted**: `PUT /api/connections/jira` with an email and an API token.
- **OAuth (3LO)**: `POST /api/connections/jira/oauth/start` → the provider →
  `GET /api/oauth/jira/callback` (`oauth_jira.go`). The scopes include `offline_access`,
  the refresh token rotates, and the refresh goes through the CP bridge. It is a separate
  Atlassian app from Bitbucket's.
- The API is reached through `api.atlassian.com/ex/jira/{cloudId}`;
  `PUT /api/connections/jira/site` picks the site.

The inbox is fetched **by the agent with credentials it already holds** — the GitHub
token, the Bitbucket connection, the Jira connection — on a schedule the CP drives. The
CP keeps only non-secret metadata (`work_item_cache`); nothing is a webhook.

## 8.5 Claude authentication and onboarding

**The method**: the CLI's own subscription sign-in, `claude auth login --claudeai`. The
agent drives a PTY, extracts the authorise URL, the Console shows it, the user approves
in their own browser and pastes the code back, and **the CLI itself writes its
credentials file** (`$CLAUDE_CONFIG_DIR/.credentials.json`, with a refresh token). The
endpoints are `POST /api/connections/claude/{start,complete}` and
`DELETE /api/connections/claude`. Status is `claude auth status`, disconnect is
`claude auth logout`. Running `/login` by hand in a terminal still works.

- **The foundation, established by measurement**: the subscription flow's redirect URI
  is a **hosted code-display page — it does not depend on a localhost callback at all**,
  so it works headless and remote unconditionally.
- **"It shows the login method chooser" is an onboarding problem, not an authentication
  one.** Even when `claude auth status` reports being logged in, a missing
  `hasCompletedOnboarding` makes the interactive TUI re-run its wizard, whose first step
  is choosing a login method — so it *looks* unauthenticated. The fix is to seed
  `hasCompletedOnboarding` and the folder's `hasTrustDialogAccepted` at every session
  start (`ensureFolderTrusted`); **skipping permissions does not skip those**.
  With `CLAUDE_CONFIG_DIR` set, **`.claude.json` is read from there too** — writing the
  one in the home has no effect.
- **Expiry is visible.** `CredentialExpiry` reads the credentials file's expiry; the
  status carries `expires_at` / `days_left` / `expired`, an expired session shows the
  `auth` state, and sends to it are refused with `auth_expired`. Re-authenticating is a
  disconnect plus the same flow. A token in the agent's own environment
  (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`) overrides the file.
- **`claude auth status` is slow** (21–28 s measured), so the status probe runs off the
  request path with its own budget.
- **Lessons, kept here so they are not repeated:**
  1. Injecting a setup token through `CLAUDE_CODE_OAUTH_TOKEN` works **headless only —
     the interactive TUI does not read it**.
  2. A synthesised credentials file with no refresh token is **rejected** by the
     interactive TUI.
  3. `ANTHROPIC_AUTH_TOKEN` authenticates but is billed as API usage, which can disable
     subscription features.
  - **The judgement lesson**: neither the status command nor the banner proves
    authentication works. **Only a real prompt and a real answer do.** And **auth and
    onboarding are different things**
    ([decisions/0002](../decisions/0002-claude-auth-onboarding.md)).

## 8.6 The other agent CLIs

Which kinds can sign in, and how a member does it from the Console, is
[ref/agents](../../guide/ref/agents.md#how-to-sign-in); this section is only the
contract underneath. **No kind needs a CP callback.** A kind's credential reaches it in
one of three ways:

- **The CLI's own login, driven by the agent**, after which the CLI writes its own
  credentials: codex, cursor, kiro, agy, muse, and opencode's account sign-in.
- **A key the member pastes**: opencode's provider keys are kept in the encrypted store
  and injected into the environment; codex's and muse's API keys are handed to the CLI,
  which writes its own file.
- **Another connection**: copilot rides the GitHub connection (§8.3); lcpp uses the
  deployment's engine (§8.9) or the member's own llama.cpp server.

| Kind | Endpoints (under `/api/connections/`) | Where the credential lives |
|---|---|---|
| codex | `codex/api-key`, `codex/device/{start,poll}`, `DELETE codex` | `~/.codex/auth.json`, the CLI's own |
| opencode | `PUT opencode`, `DELETE opencode/{env}`, `opencode/oauth/{start,poll,cancel}`, `DELETE opencode/oauth`, `opencode/serve/restart` | keys in the encrypted store; the account in opencode's own database |
| cursor | `cursor/{start,poll}`, `DELETE cursor` | `~/.config/cursor/auth.json`, the CLI's own |
| kiro | `kiro/{start,poll}`, `DELETE kiro`, `kiro/install` | the CLI's own database |
| agy | `agy/{start,complete}`, `DELETE agy` | the CLI's own token file |
| muse | `muse/{start,poll,api-key}`, `DELETE muse`, `muse/install` | `~/.config/muse/auth.json`, the CLI's own |
| copilot | — (its state is a field of `GET /api/connections`) | the GitHub connection; the Managed child gets it as `COPILOT_GITHUB_TOKEN` |
| lcpp | `PUT lcpp`, `DELETE lcpp`, `lcpp/check` | the member's own server in the encrypted store; the deployment's engine token is not stored |

How the logins are driven, which is where they break:

- A login's state lives only in the agent's memory, so the CP relays these flows through
  `restLoginFlow`, which refuses while the workspace is still converging rather than let
  a retiring task lose the flow.
- codex (`--device-auth`), cursor, kiro and agy run on a PTY, and the agent scrapes the
  URL (and code) off the screen. agy has no headless login at all: the agent walks its
  interactive TUI, including its onboarding. muse runs over **a pipe, not a PTY** — on a
  PTY it waits for Enter. opencode's account flow goes through `opencode serve`'s own
  HTTP API, not a screen.
- **codex**: injecting the credential by environment does not work (`codex login status`
  stays logged out), so both paths make the CLI write its own file. The CLI polls OpenAI
  itself. When the ChatGPT account has device-code login switched off, the start fails
  with `no_url`. The connected card reads the email and plan from `auth.json`'s
  `auth_mode` and id-token claims.
- **opencode**: the environment name must match `^[A-Z][A-Z0-9_]{1,63}$`. **The key is
  never put on the command line**: a Terminal (CLI) session receives it through
  `tmux new-session -e`, and a Managed session through the environment of the shared
  `opencode serve` daemon, which is why changing a key needs `serve/restart`. No
  plaintext auth file is created ([04 §4.3](04-agent.md)).
- **muse**: saving an API key is refused with `account_login_present` while an account is
  signed in.

## 8.7 MCP — the outward contract

| Surface | Who connects | Auth | Scope |
|---|---|---|---|
| The CP's `/mcp` | an external Claude client | bearer PAT, exempt from the login gate | member tools, plus admin read/write tools **with the role resolved live on every call** |
| The agent's `mcp-stdio`, assistant side | the in-container assistant chat | the agent's own token, from the same container | read-only by default; `--write` advertises the fleet operations |
| The agent's `mcp-stdio`, session side | every agent CLI in a session | the agent's own token, forwarded to the child | a narrow set, below |
| MCP servers a user or tenant adds | every agent CLI | whatever the server wants | — |

**The CP's endpoint** exists only when `AF_MCP_ENABLED=true`. It is POST-only JSON-RPC
answered with JSON (no SSE) and speaks both protocol eras
([decisions/0032](../decisions/0032-mcp-2026-07-28.md)). A PAT is stored as a hash
(`GET|POST /api/pat`, `DELETE /api/pat/{id}`) and carries a scope clamped to the role's
ceiling; the tenant always comes from the token. The member tools drive, launch, clean
up and meter your own sessions and manage memos; the admin tools read workspaces, usage,
egress and the audit log, and write only to stop a workspace or session, set a quota, or
*propose* an allowlist change, each write audited with the PAT as the actor. The
dangerous tier (key rotation, recreating workspaces, bulk stops) is not planned
([decisions/0006](../decisions/0006-mcp-unified.md)). A client is configured as:

```json
{"mcpServers":{"agent-fleet":{"type":"http","url":"<PUBLIC_BASE_URL>/mcp","headers":{"Authorization":"Bearer <PAT>"}}}}
```

**The session side** is the built-in server (`mcpreg` `BuiltinAF`), registered under a
per-boot name `af_<8 hex>` so a repository's own `af` cannot shadow it. It is written
into the native config of claude, opencode, codex, cursor, kiro, agy and copilot, sent
over the wire to muse, and called in-process by lcpp. Because codex starts MCP children
with an empty environment, `AGENT_TOKEN`, `AGENT_ADDR`, `AF_SESSION_NAME`,
`AF_CP_BASE_URL` and `AF_MEMO_TOKEN` are forwarded explicitly. What it advertises is
assembled in `mcpStdioToolList`:

| Group | When |
|---|---|
| handoff, `af_report`, `af_stop_after_turn`; session status and usage; memos (list, add, update); `branch_name` (the branch-name resolver, ADR 0103); `memory_index` / `memory_search` / `memory_read` / `memory_save` / `memory_forget` (AF-owned memory shared by every kind, ADR 0108) | always |
| the Chromium attach tools | started with `--chromium-attach`, which the built-in registration always passes (`mcp-stdio --self-report --chromium-attach`) |
| `list_peer_sessions`, `send_to_peer_session`, `peek_session_output` (read-only) | the user's peer-messaging setting |
| launching and steering sessions (`create_session` …); driving reaches only the caller's own children | the user's session-spawn setting |
| `generate_image` | the user's image-generation setting, then per `tools/list` |
| the image studio's tools | when the session is bound to a studio |

Anything not advertised is **refused on call as well**, checked against the last
`tools/list`; a watcher sends `notifications/tools/list_changed` when the set changes.
Deleting memos or sessions is never exposed here.

**Servers a user or tenant adds** ([decisions/0031](../decisions/0031-mcp-registry.md))
are written into each CLI's own global config; the agent removes only names it wrote
itself. Tenant servers come from `GET /internal/mcp-servers` (`AF_MCP_TOKEN`), and the
agent keeps its last copy when the CP is unreachable. The built-in ops servers
(PagerDuty, Grafana, CloudWatch, AWS) run as `workspace-agent mcp-run <id>`, which
injects the stored credential at spawn.

## 8.8 AWS

Two unrelated things talk to AWS, and they must not be confused: the CP using the
deployment's own account, and a member using theirs.

**The deployment's own account** (the CP's IAM role; nothing is stored). Production runs
`ecs-ec2`. The runtimes register task definitions, upsert services, create EFS access
points and inject secrets as SSM SecureString parameters; `ecs-ec2` adds a persistent
EBS home per user, the EC2 slot pool, snapshots and SSM `SendCommand`
([09 §9.5](09-deploy.md)). The CP also buys engine instances with EC2 Fleet
(`engineFleetAPI`, [decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.md)), and
calls Cloud Map, Cost Explorer, Polly, S3, Secrets Manager and CloudWatch Logs. Each
SDK client sits behind an interface (`ecsAPI`, `ec2API`, `engineFleetAPI` …) so it can be
tested. The CP never calls Bedrock.

**A member's accounts** (pattern (a); **no AWS secret is stored in, or reaches, the
CP**):

- The profiles are rows in the CP (`ssm_profile`, plus `ssm_host` for SSM targets;
  [06 §6.2](06-data.md)). The workspace fetches them from `GET /internal/aws-profiles`
  (`AF_AWS_PROFILES_TOKEN`) and writes a managed block into `~/.aws/config`.
- **An SSM session** runs `aws sso login --use-device-code --no-browser` and then
  `aws ssm start-session` inside the workspace; the Console polls
  `GET /api/sessions/{name}/ssm-login` for the device code.
- **`af-aws-exec`** asks the Console for the login instead of starting one
  ([decisions/0102](../decisions/0102-aws-login-through-the-console.md)): the device code
  starts only when the member presses the button (`/api/aws-login/…`). The code and URL
  pass through the CP to the browser and are audited; the tokens do not. The child runs
  with the container and instance-metadata credentials removed, so it cannot fall back
  to the workload role.
- The SSO cache lives in `~/.aws/sso/cache/` inside the workspace.

The key custodian can be AWS KMS (`AF_KEY_CUSTODIAN=kms`; `KeyCustodian`, [07 §7.6](07-security.md)).

## 8.9 Engines

Engines are model servers the deployment provides — llama.cpp, ComfyUI, an
OpenAI-compatible image API. **A workspace never talks to an engine in the
deployment's table directly**, and never holds an upstream credential:

- The CP injects a per-membership issuing token, `AF_ENGINE_ISSUE_TOKEN`, at workspace
  start. With it the agent buys, from `POST /internal/engine/token`, a gateway token
  bound to the membership and one engine key, and also to one session when the caller
  names one. The routes that name none — opencode's shared Managed daemon, image
  generation and the boot-time probes — get a token for the whole workspace.
  `engineToken` caches it until half its lifetime. opencode receives it as `AF_ENGINE_TOKEN`; lcpp and image generation, which run inside
  the agent, use it directly. Either way the call goes to the CP's gateway,
  `/engine/{key}/v1/…`, which is exempt from the login gate. The CP adds the upstream
  credential: `AF_ENGINE_API_KEY_<KEY>` in its environment for an external row, or an SSM
  SecureString for one it manages
  ([decisions/0083](../decisions/0083-openai-compat-image-provider.md)).
- **A member's own llama.cpp server** (§8.6, lcpp) is the exception: the agent reaches
  its URL directly, with the optional API key from the encrypted store, and the CP is not
  involved. When it is set, it wins over the deployment's engine.
- **Borrowing another deployment's engines** is CP to CP, outbound only
  ([decisions/0079](../decisions/0079-remote-engine-from-another-deployment.md)). The
  borrower sets `AF_REMOTE_ENGINE_URL`, `AF_REMOTE_ENGINE_TOKEN` (issued by the lender's
  super_admin with `POST /api/admin/engines/issue-token`) and optionally
  `AF_REMOTE_ENGINE_KEYS`; it exchanges the issuing token for engine tokens it keeps
  only in memory, and polls the lender's `GET /internal/engine/catalog`. The lender
  stores nothing new. The procedure is
  [operate/08](../../guide/operate/08-borrowed-engine.md).
- **Model ingest** downloads from Hugging Face and Civitai with a deployment-wide token,
  set by a super_admin and sealed in a settings row; on AWS it is also copied into the
  stack's Secrets Manager secret for the ingest task, which the CP writes but never
  reads.
- Image generation through codex or agy spends that CLI's own sign-in (§8.6); no key is
  involved ([decisions/0069](../decisions/0069-image-generation-providers.md)).

## 8.10 Chat bridges and ops tools

- **Discord and Slack** ([decisions/0020](../decisions/0020-chat-bridge.md)): each user
  registers their own bot, and **the agent** — not the CP — opens an outbound WebSocket
  and keeps it open: Discord's Gateway with a bot token, Slack's Socket Mode with a bot
  token and an app-level token. No webhook and no public URL, so the bridge is live only
  while the workspace runs. The CP relays `PUT|DELETE /api/connections/{discord,slack}`
  and their inspection endpoints. A send-only Teams slot is designed but not built.
- **PagerDuty and Grafana**: a pasted API token in the encrypted store
  (`PUT /api/connections/{pagerduty,grafana}`), used by the ops MCP servers of §8.7.
  CloudWatch and AWS store only a profile name and use the member's AWS profiles
  (§8.8).

# 0138. Model credentials and harnesses: an inventory, sharing rules, deployment and tenant restrictions, and the Settings layout

English | [日本語](0138-model-credentials.ja.md)

- Status: **proposed** (2026-11-14). Nothing is built. Every `file:line` was read on `1c92e98f9`; ADR 0136 was read
  from its open PR (#4137, branch head of 2026-11-14). **What is measured and what is not.** Measured means read in
  the tree: where each credential is stored, which code reads it, which checks exist. Nothing was run against a live
  Workspace or deployment, and no AWS call was made. What a vendor's CLI or endpoint does (which host a login talks
  to, whether a CLI can be pointed at another credential file) is **not measured** unless a cited ADR measured it,
  and is marked so where a decision rests on it. Reviewed 2026-11-14 by a separate session (codex `gpt-6.1-sol`):
  round 1, 13 findings; round 2, 5 findings; each checked against the tree and folded into this text.
- Tracking: #4159. Refs #3282.
- Follow-ups: none filed yet. The phases (§8) depend on which options the owner takes, so their issues are filed when
  the decisions are accepted; until then the open questions (§10) are tracked on #4159.
- Related: [0128](0128-harness-route-maker.md) (harness, route, maker; this record answers its open questions 1 and
  2) / [0129](0129-provided-model-routes.md) (provided routes, grants, budgets; this record amends the last bullet of
  its D3) / ADR 0136 (pi; proposed, PR #4137) / [0084](0084-engine-indicator-and-tenant-gate.md) (the tenant gate,
  its tri-state idiom and its four places) / [0069](0069-image-generation-providers.md) (credential tiers) /
  [0122](0122-egress-gateway.md) (the egress gateway) / [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.md)
  (confirm before use) / [0029](0029-usage-accounting.md) (the token ledger)

## Context

Settings > Connections > Agents (`console/src/features/settings/agents/AgentsTab.tsx:AgentsTab`) is one card per
harness, and each card holds that harness's logins and keys. That shape assumes one credential per harness, and it no
longer holds:

- The **Anthropic API key** is entered on the OpenCode card (the `anthropic` entry of `OC_PRESETS`,
  `agents/OpencodeCard.tsx:31`), stored in opencode's key map (`secrets.Data.Opencode`,
  `workspace/agent/internal/secrets/secrets.go:488`), and read by claude's `anthropic-api` route (`loadAPIKey`,
  `workspace/agent/internal/agents/claude/route.go:80`). The Claude Code card shows no key at all
  (`agents/ClaudeCard.tsx:ClaudeCard`), and the refusal a member gets without a key tells them to go to the OpenCode
  card (`workspace/agent/internal/mcpx/mcp_fleet_spawn.go:283`).
- **pi** (ADR 0136, proposed) will run on an OpenAI API key and an Anthropic API key, the same kinds of key codex,
  opencode and claude use, and on a ChatGPT login of its own.
- **Bedrock** runs on an AWS profile marked as a model route on the AWS tab (ADR 0128 decision 11) or on a route the
  deployment provides (ADR 0129), neither of which is on the Agents tab.
- The Agents tab also holds things that are not credentials at all: install and update, resume, notifications,
  remote control, launch defaults, the session settings block and the image-generation order.

The user also wants deployments and tenants to **restrict** which credentials may be used. At minimum:

- **No subscriptions**: the Claude subscription, the ChatGPT login, opencode Zen/Go and the CLI-locked logins of
  muse, agy, copilot, cursor and kiro forbidden for a whole deployment or for one tenant.
- **API or Bedrock only**: metered API keys and/or Bedrock, personal or provided; with the variants **Bedrock only**
  (data must stay in the customer's AWS) and **provided routes only** (no personal credential at all).
- **Mixed**: the deployment allows everything, a tenant narrows it, and a member can never widen it.

Nothing like that exists today (§1.4).

## 1. Inventory (measured)

Paths are relative to `workspace/agent/` unless they start with `console/`, `control-plane/` or `deploy/`.
"Console UI" names the place under Settings > Connections unless it says otherwise.

### 1.1 Credentials

| # | Credential | Where it is stored | Used today by | Could be used by | CLI-locked | Route id | Console UI |
|---|---|---|---|---|---|---|---|
| 1 | Claude subscription login | claude's own `.credentials.json` under `CLAUDE_CONFIG_DIR` (`internal/agents/claude/authexpiry.go:135`, `credsPath`), written by the CLI's login (`claude/auth.go:44`, `HandleStart`) | claude sessions; the assistant chat and one-shots on the claude backend (`internal/chatx/chat_backends.go:24`) | claude only: the vendor reserves the OAuth login for its own applications (ADR 0128 decision 4; ADR 0136 Context) | yes | `anthropic-subscription` (`claude/route.go:39`), only when selected explicitly; an empty selection has no route (ADR 0128 decision 2) | Agents > Claude Code card: connect, paste code, read-only plan label, re-auth, disconnect (`agents/ClaudeCard.tsx:36`, `:152`) |
| 2 | Legacy Claude OAuth token | `secrets.Data.Claude` (`internal/secrets/secrets.go:482`); only cleared on disconnect (`claude/auth.go:125`) | none found | none | — | none | none |
| 3 | Anthropic API key | `secrets.Data.Opencode["ANTHROPIC_API_KEY"]` in the encrypted store (`secrets.go:488`; written by `opencode/auth.go:231`, `HandlePutConn`) | claude on `anthropic-api`, into that pane only (`claude/route.go:193-201`); opencode, which gets every stored key in its environment, the shared `serve` included (`opencode/auth.go:50`, `keyEnv`) | pi on `anthropic-api` (ADR 0136 decision 6, its P3) | no | `anthropic-api` for claude, bound as `ANTHROPIC_API_KEY@<sha256 prefix>` (`claude/route.go:260`, `BindRoute`); none for opencode | Agents > **OpenCode** card > API keys, "Anthropic" preset (`OpencodeCard.tsx:31`) |
| 4 | ChatGPT login (codex) | `~/.codex/auth.json`, `auth_mode: chatgpt`, written by `codex login --device-auth` (`codex/auth.go:179`) | codex; image generation's codex provider (`internal/imagegen/codex.go:6`, `:139`); the assistant chat's codex backend (`chat_backends.go:25`) | codex only (the file is codex's) | yes | `chatgpt-subscription` is in the catalogue (`internal/routes/routes.go:73`) but codex declares no routes and records none (`codex/transcript.go:291-295`) | Agents > Codex card, "ChatGPT subscription" (`agents/CodexCard.tsx:51`, `:200`) |
| 5 | OpenAI API key (codex's copy) | the same `~/.codex/auth.json`, `auth_mode: apikey`, via `codex login --with-api-key` (`codex/auth.go:138`) | codex (and whatever codex runs: image generation, the chat backend) | codex only: codex ignores `OPENAI_API_KEY` in its environment (`codex/auth.go:25-26`) | yes | `openai-api` is in the catalogue (`routes.go:75`), declared by no harness | Agents > Codex card, "OpenAI API key" (`CodexCard.tsx:85`, `:166-185`) |
| 6 | OpenAI API key (opencode's copy) | `secrets.Data.Opencode["OPENAI_API_KEY"]` | opencode (`keyEnv`) | pi on `openai-api` (ADR 0136 decision 6, P1) | no | none | Agents > OpenCode card > API keys, "OpenAI" preset (`OpencodeCard.tsx:32`) |
| 7 | opencode.ai account login | opencode's own database, held by its `serve` (`opencode/oauth.go:3-6`, `:405-410`); the Agent does not store it | opencode | opencode only | yes | none | Agents > OpenCode card, account login, shown only when the chosen billing uses opencode.ai (`OpencodeCard.tsx:320`, `:422-458`) |
| 8 | OpenCode Zen / Go key | `secrets.Data.Opencode["OPENCODE_API_KEY"]` (`opencode/auth.go:27`); one key pays for Zen and Go (`opencode/catalog.go:5-9`); dropped when the billing choice is `own` or `free` (`opencode/auth.go:59-67`) | opencode | opencode (pi lists Zen/Go as a provider; ADR 0136 does not offer it) | no | `opencode-zen`, `opencode-go`: ledger labels from the provider id opencode wrote (`internal/usagex/ledger.go:146-148`), not declared by `Routes()` | Agents > OpenCode card: the billing choice `off/own/free/go/zen` (`OC_ROUTES`, `OpencodeCard.tsx:59`), the "go" preset, the workspace id row (`:195`) |
| 9 | Other provider keys (OpenRouter, Gemini, Sakana, custom names) | `secrets.Data.Opencode`, any name matching `^[A-Z][A-Z0-9_]{1,63}$` (`opencode/auth.go:23`) | opencode | opencode | no | none | Agents > OpenCode card > API keys (`OpencodeCard.tsx:33-36`, `:473-517`) |
| 10 | Muse login (account or Meta API key) | `~/.config/muse/auth.json` (`muse/auth.go:59`), told apart by `mechanism` (`muse/auth.go:16`); an API key through `muse auth set --api-key-stdin` (`:301`). A `META_API_KEY` in the Agent's own environment overrides the login and is reported, not removed (`:120-130`) | muse; the chat backend (`chat_backends.go:30`) | muse only | yes | none | Agents > Muse card: sign in, API key with a metered warning (`agents/MuseCard.tsx:117`, `:157`, `:258-259`) |
| 11 | Antigravity (agy) Google login | `~/.gemini/antigravity-cli/antigravity-oauth-token` (`agy/auth.go:36-38`) | agy; image generation's agy provider (`imagegen/agy.go:113`); the chat backend (`chat_backends.go:27`) | agy only | yes | none | Agents > Antigravity card (`agents/AgyCard.tsx:34`, `:48`); the "gcp-project" method is disabled (`:156-161`) |
| 12 | GitHub connection (Copilot) | the git connection, read through `gh auth token` (`copilot/auth.go:43`) | copilot (`COPILOT_GITHUB_TOKEN`, `copilot/driver.go:308`); git hosting | copilot only, as a model credential | in effect: it is the member's GitHub login, and the model plan is GitHub's | none | Agents > Copilot card shows status and links to Git (`agents/CopilotCard.tsx:39`, `:54`); the login is Git > GitHub (`connect/GitTab.tsx:320`) |
| 13 | Cursor login | `~/.config/cursor/auth.json` (`cursor/auth.go:4`); an API key is deferred (`:104`) | cursor; the chat backend (`chat_backends.go:28`) | cursor only | yes | none | Agents > Cursor card (`agents/CursorCard.tsx:27`) |
| 14 | Kiro login | `~/.local/share/kiro-cli/data.sqlite3` (`kiro/auth.go:4`); an API key is deferred (`:110`) | kiro | kiro only | yes | none | Agents > Kiro card, after install (`agents/KiroCard.tsx:52`, `:100`) |
| 15 | AWS profile marked as a model route | no secret: the profile is the CP's Settings record (`ssm_profile.model_route`, `control-plane/internal/store/migrations-pg/0107_ssm_profile_model_route.sql`), read by the Agent (`internal/awsx/profiles.go:77`, `:179`); short-lived credentials are resolved in the route gateway's memory (`internal/awsx/route.go:67-110`) | claude on `bedrock` (`claude/route.go:338`, `bedrockPlan`) and opencode on `bedrock` (`opencode/route.go:83`, `:202-232`), each behind an acceptance switch (`AF_CLAUDE_BEDROCK_ROUTE`, `AF_OPENCODE_BEDROCK_ROUTE`) | pi (ADR 0136, P2); codex only for OpenAI-family models (ADR 0129 H) | no | `bedrock`, bound as `<profile id>@<region>#<fingerprint>` (`routes.go:204-236`) | **AWS** tab (`ssm` section), the "model route" checkbox (`workspace/SsmTab.tsx:665-675`); the launch dialog lists marked profiles (`console/src/features/repos/bedrockProfiles.ts:46`) |
| 16 | Deployment-provided Bedrock | no stored secret: the CP's own identity assumes a route role (`control-plane/internal/routegateway/sts.go:55`); the Agent holds an issuing token `AF_ROUTE_ISSUE_TOKEN` (`internal/routegw/cproutes.go:100`) | claude on `bedrock-deployment`, behind `AF_PROVIDED_ROUTES=1` and `AF_CLAUDE_BEDROCK_DEPLOYMENT_ROUTE` (`claude/route.go:145`) | opencode, codex (ADR 0129 Q6, Q7), pi (ADR 0136 P2) | no | `bedrock-deployment`, bound as `<source id>#<fingerprint>` (`control-plane/internal/providedroutes/providedroutes.go:102`) | **none** in the Console: the super_admin and tenant_admin REST exists (`control-plane/provided_routes.go:77-84`), the screens do not (ADR 0129, Q1 part 2) |
| 17 | Tenant-provided Bedrock | store only: `provided_route_source` rows (`control-plane/internal/store/migrations-pg/0111_provided_routes.sql:15`), no REST handler; the gateway refuses it (`control-plane/internal/routegateway/gateway.go:276-278`) | none | claude, opencode, codex, pi (ADR 0129 Q5) | no | `bedrock-tenant` (catalogue only) | none |
| 18 | Deployment engine (self-hosted) | a CP-issued engine token (`control-plane/engine_token.go:46`) | lcpp (`engines.go:1328-1336`); opencode's engine provider (`opencode/engine.go:42`); image generation's engine provider | pi (ADR 0136 P3) | no | `af-engine-llm` (ledger label; recorded by lcpp) | Admin > engines, engine tokens (`console/src/features/settings/admin/AdminTab.tsx:87-88`); Tenant > engine access |
| 19 | Member's llama.cpp | `secrets.Data.Lcpp` (`secrets.go:492`), written by `PUT /connections/lcpp` (`connections.go:651-654`); wins over the engine (`engines.go:1322-1326`) | lcpp (`lcpp/driver.go:642`); the chat backend | lcpp, and any harness that speaks OpenAI Chat Completions (not designed) | no | `member-llamacpp` (ledger label, `lcpp/store.go:310-319`) | Agents > llama.cpp card (`agents/LcppCard.tsx:170-190`) |
| 20 | pi's ChatGPT login (proposed) | one login file per Workspace, written by AF's flow around `pi auth login openai` (ADR 0136 decision 12) | — | pi only | yes | `chatgpt-subscription` (ADR 0136 decision 6) | a pi card (ADR 0136 decision 12) |
| 21 | Google Vertex | nothing: catalogue id only (`routes.go:78`); no Vertex code found | none | claude (ADR 0128 decision 5) | — | `vertex` | none |

Two observations come out of rows 4 and 20: the same ChatGPT account would be signed in twice, once for codex and
once for pi, in two files neither harness can share (ADR 0136 rejects a copy of the login). Whether pi could read
codex's file is **not measured**.

### 1.2 What else consumes a credential

Sessions are not the only callers:

| Feature | Credentials it uses | Where chosen |
|---|---|---|
| Assistant chat, `ask_assistant`, title and branch suggestions | the chat backend of claude, codex, opencode, agy, cursor, lcpp or muse (`internal/chatx/chat_backends.go:24-30`); a conversation pinned to an unavailable backend **falls back** to another at send time (`chat_providers.go:57-60`) | the assistant settings; availability is `claude auth status` / `codex login status`, cached one minute (`chat_providers.go:61-62`) |
| Image generation | codex's ChatGPT login, agy's login, the deployment engine (`imagegen/imagegen.go:464-478`, `:527-528`) | Agents tab, "image generation" and its provider order (`agents/AgentsTab.tsx:253-278`) |

The token ledger rows of these features carry a `feature` dimension; whether they carry a route is **not measured**.

### 1.3 Route ids and who declares them

From `control-plane/internal/agentroutes/catalog.json` (generated from `internal/routes/routes.go:66-79`):

| Route id | Wire | Credential tier | Declared by (`Routes()`) |
|---|---|---|---|
| `anthropic-subscription` | anthropic-messages | existing-connection | claude |
| `anthropic-api` | anthropic-messages | member-key | claude |
| `bedrock` | bedrock | aws-profile-ref | claude, opencode |
| `bedrock-deployment` | bedrock | deployment | claude |
| `bedrock-tenant` | bedrock | tenant-key | none |
| `vertex` | vertex | tenant-key | none |
| `chatgpt-subscription` | openai-responses | existing-connection | none (pi, proposed) |
| `openai-api` | openai-responses | member-key | none (pi, proposed) |
| `opencode-zen`, `opencode-go` | per-model | existing-connection | none (ledger labels) |
| `af-engine-llm` | openai-chat | deployment | none (ledger label) |
| `member-llamacpp` | openai-chat | member-key | none (ledger label) |

agy, codex, copilot, cursor, kiro, lcpp, muse, shell and ssm declare nothing. Seven of the credentials in §1.1 (rows 7,
9, 10, 11, 12, 13, 14) have **no route id at all**.

### 1.4 Controls that exist today

| Layer | Control | Where | What it covers |
|---|---|---|---|
| operator | acceptance switches `AF_WS_CLAUDE_BEDROCK_ROUTE`, `AF_WS_OPENCODE_BEDROCK_ROUTE` | `control-plane/workspace_lifecycle.go:572`, `:576` | open one harness × route row each; not release settings |
| operator | `AF_PROVIDED_ROUTES` / CFN `ProvidedRoutes` | `control-plane/internal/routegateway/gateway.go:48`; `deploy/aws/ecs/cfn/30-ingress.yaml:310` | provided routes exist at all |
| operator, super_admin | egress allowlist | `control-plane/egress_policy.go:21` (`defaultEgressAllowlist`), `:103` (`egressBuiltin`) | by host, **deployment-wide only** (`control-plane/egress_member.go:188`: "EffectiveAllowlist ignores scope"); the Anthropic hosts and `.amazonaws.com` are built in and cannot be removed |
| super_admin | engine use per tenant, `*bool`, nil = allowed | `control-plane/limits.go:135-136` (ADR 0084 decision 7) | the self-hosted engine only |
| super_admin | provided-route grant and ceiling, `bool`, default off | `control-plane/provided_routes.go:82` (ADR 0129 D3) | `bedrock-deployment` only |
| tenant_admin | provided-route policy, member grant and budget | `control-plane/provided_routes.go:83-84` | `bedrock-deployment` only |
| tenant_admin | engine access per member | `control-plane/engine_access.go:70` | the engine only |
| tenant_admin | member defaults (agent, model, driver, budget) | `console/src/features/settings/tenant/tenantMemberDefaults.tsx:43` | a default, not a restriction; no route |
| member | kind on/off, `CreatePolicy.EnabledPref` | `internal/agents/createpolicy.go:18`; only lcpp sets it (`lcpp/create_policy.go:13`) | refuses a create 403 |
| member | opencode billing `off` | `opencode/catalog.go:44-63`; `console/src/agents/registry.ts:586` | opencode as a whole |
| member | hidden models | `internal/sessionx/model_deny.go:82` (`HiddenModelsRaw`); `console/src/lib/settings/prefs.ts:201` | models, enforced at launch and in MCP `list_models` |
| — | **any allow or deny of kinds, routes or credential classes per deployment or tenant** | **not found** | — |

Gaps the restrictions have to close, each measured:

- The Console offers claude only when the subscription login is connected (`console/src/agents/registry.ts:266`). A
  member with only an Anthropic API key or a marked AWS profile cannot pick claude, although the Agent would run it.
- A session that starts a session through the af MCP tool calls the Agent's own `POST /sessions`
  (`internal/mcpx/mcp_client.go:270`); the CP's pre-wake checks (`control-plane/workspace_handlers.go:1171-1180`)
  never see it.
- An empty route selection cannot be proven after launch (ADR 0128, note of 2026-11-05, P4a): nothing readable says
  which credential an empty-selection pane inherited.
- The ledger record has `Route` and `Billing` but no connection field (`internal/usagex/ledger.go:172-173`), and
  codex, muse, agy, copilot, cursor and kiro turns carry no route.
- The CP holds no member model credential and no per-route token usage of one; its usage tables are Workspace
  occupancy (`control-plane/usage.go`) and provided-route calls (`provided_route_call`). The token ledger is in the
  Workspace (ADR 0029).

## 2. Vocabulary

| Word | Meaning | Relation to the other three |
|---|---|---|
| **Credential** | What lets a call through to a model, and who pays for it: a login (a subscription or a CLI account), an API key, an AWS profile, a provided source, a self-hosted endpoint. On screen it stays "connection" where it already is (glossary) | Held by the member (or by the tenant or deployment for a provided source). A **route** names a credential's way to a model; a **harness** uses it only through a route it declares; a **policy** decides whether it may be used |
| **Harness** | The program that runs the agent loop, today's session kind (ADR 0128 decision 1). On screen "Agent" | Declares the routes it can run on. Owns nothing in this model except the CLI-locked logins it keeps by itself |
| **Route** | Where model calls go and with which credential (ADR 0128 decision 1), identified by a route id | The join of the other three: one credential (or credential slot), declared by one or more harnesses, judged by the policy. Every policy check is evaluated on a route id |
| **Policy** | Which routes may be used, set in layers by the operator, super_admin, tenant_admin and member | Reads only route ids, through each id's class and destination (D1). Never names a harness: a harness is usable when at least one route it declares is allowed and its credential is held |

Two attributes of a route make the policy writable by a person:

- **Credential class**: `subscription` (a plan or account login, CLI-locked or not: rows 1, 4, 7, 8 for Go, 10 for
  the account login, 11, 12, 13, 14, 20), `member-key` (a metered key: rows 3, 5, 6, 8 for Zen, 9, 10 for the API
  key), `member-cloud` (row 15), `provided` (rows 16, 17), `self-hosted` (row 18, and row 19 only when its endpoint is
  approved, below). Row 8 is split by route:
  Zen bills the key per call (the ledger writes a priced Zen call as `metered`, `usagex/ledger.go:137`,
  `BillingOfOpencodeCall`), Go is a subscription.
- **Destination**: who receives the prompts. A vendor (Anthropic, OpenAI, Google, Meta, GitHub, Cursor, Kiro,
  opencode.ai, OpenRouter, Sakana), Amazon Bedrock in an AWS account, the deployment's engine, an approved
  self-hosted endpoint, or `unclassified` (a destination AF cannot vouch for).
- **A destination is only as good as the endpoint it is pinned to.** The member's llama.cpp connection accepts any
  `http(s)` URL, with an optional key (`connections.go:629-641`), so a vendor or a proxy can be entered there and
  would be mislabelled `self-hosted`. Under any destination restriction, a member endpoint is therefore
  `unclassified`, unless its host is an **approved self-hosted endpoint**; it is then `self-hosted` with that
  destination, and the connection binds to the host it was approved for. Approval follows the same intersection as
  the policy (D7): the operator ceiling names the host patterns that may be approved at all (none when unset under a
  ceiling that restricts destinations), the super_admin narrows that set per deployment and per tenant, and the
  tenant_admin approves only hosts inside what is left. A host that matches a vendor destination's hosts in the
  catalogue can never be approved as self-hosted at any layer, so an excluded vendor cannot be relabelled.
  The same rule holds for any credential whose endpoint can be overridden: a vendor key keeps its vendor's
  destination only while the harness pins the vendor's endpoint (as claude's routes pin `ANTHROPIC_BASE_URL` and
  refuse a settings file that sets it, ADR 0128 notes of 2026-11-04 and 2026-11-05); an override makes it
  `unclassified`.

**Maker** (ADR 0128) is unchanged and is not a policy axis: Claude on Bedrock and Claude on the Anthropic API share a
maker and differ in destination, and the destination is what a data-residency rule is about. **Source** (ADR 0129
decision 1: member, tenant, deployment) is folded into the class. **Billing** (ADR 0128 decision 8) stays a per-call
fact, not a policy input, because it is known only after the call.

**ADR 0128 open question 1 (route naming).** The screen word was settled in its P1b note ("Model route" / モデルの
ルート; Japanese prose writes 経路, glossary). This record settles the **ids**: D2.
**ADR 0128 open question 2 (tenant allow or deny).** Answered: yes, for every route, member credentials included
(D7). This **amends ADR 0129 D3's last bullet**, which kept routes on the member's own credential outside tenant
control.

## 3. Decisions on credentials and sharing

### D1. The unit of control is the route, written as class × destination

- **D1a. Harness.** Simple, and matches the kind list the launch dialog already filters. It cannot say "claude, but
  not its subscription", which is the first configuration the user asked for.
- **D1b. Route id.** Exact, and it is what every check evaluates. As the admin's unit it is a list of a dozen ids
  that grows with every harness row, and a new id is either allowed by surprise or denied by surprise.
- **D1c. Credential class × destination, evaluated on route ids** *(recommended)*. The catalogue assigns every route
  id exactly one class and one destination (D2), except the routes it marks **per-connection** (`member-llamacpp`,
  `custom-api`, and any route whose endpoint the member can set), whose class and destination come from the
  validated endpoint the connection is bound to (§2). The unit evaluated is therefore the route id plus, for those
  routes, its endpoint binding. A policy layer is a set of allowed classes and a set of allowed
  destinations, each defaulting to "all"; a route is allowed when its class and its destination are both allowed at
  every layer. The configurations are short: "no subscriptions" removes one class and one destination (§6);
  "Bedrock only" keeps one destination; "provided only" keeps one class. A route id added later inherits the rule through its class and
  destination; one with no assignment is `unclassified` and refused wherever any restriction is set (fail closed).
  Cost: the catalogue has to be kept honest, and a reviewer of a new route id checks two attributes.

Per-route-id exceptions ("all subscriptions except Copilot") are not in this record (open question 1).

### D2. Every credential path has a route id (ADR 0128 open question 1)

A policy can only judge what has a name. Today seven credentials have none (§1.3), so a "no subscriptions" rule could
not see muse or cursor at all.

- Each kind declares, besides the routes a member may select (`RouteAdapter.Routes()`), the **fixed routes** its
  rules resolve to (ADR 0128 decision 4's `ResolveRoute`). A kind with no adapter declares fixed routes only; its
  sessions are recorded on them.
- The chat backends and the image-generation providers declare their routes the same way.
- **Naming rule.** An id names the credential, not the harness: `<destination>-<subscription|api>` where the
  credential is a vendor plan or key (`anthropic-subscription`, `openai-api`, `openrouter-api`, `gemini-api`,
  `sakana-api`, `meta-api`), and `<harness>-login` only where the login exists for one harness and its plan is not a
  separable product (`muse-login`, `agy-login`, `copilot-login`, `cursor-login`, `kiro-login`, `opencode-account`).
  A custom opencode key is `custom-api`, destination `unclassified`. Existing ids keep their names. opencode's free
  models carry no credential but still send prompts to opencode.ai; the ledger files them under `opencode-zen`
  (provider `opencode`, `usagex/ledger.go:146-148`), so they follow Zen's class and destination. Whether a free call
  always reports that provider is **not measured**.
- **A kind with more than one login mode resolves per mode, at execution.** muse resolves to `muse-login` or
  `meta-api` from its `mechanism` (`muse/auth.go:96-130`, with `META_API_KEY` in the Agent's environment counting as
  `meta-api`, since it overrides the login); codex resolves to `chatgpt-subscription` or `openai-api` from
  `auth_mode`. The mode is read when the session, chat call or image call starts, never assumed from the kind.
- One route id can be served by two credential copies: codex's own API key (row 5) and the shared OpenAI key (row 6)
  are both `openai-api`; the connection reference tells them apart (D5).

### D3. One stored credential serves every harness that can use it

- **D3a. Each harness keeps its own copy** (today for the OpenAI key, rows 5 and 6). A member who uses two harnesses
  pastes a key twice, revokes it twice, and sees spend split by harness rather than by key.
- **D3b. One credential per provider account, used by every harness that declares its route**, each through its own
  isolation (ADR 0128 decision 7: A1 per-session environment for a member key, A2 the gateway for anything broader)
  *(recommended)*. The member decides per credential which harnesses may use it ("Use for", D6); the default is every
  harness that can, which is what opencode and claude already do with the Anthropic key.
- **D3c. Harnesses share CLI login files** (pi reading codex's `auth.json`, opencode reading claude's OAuth file).
  Rejected: the Claude OAuth login is for Claude Code only (ADR 0128 decision 4); a refresh by one process can
  invalidate another's copy where the provider rotates refresh tokens (ADR 0136, rejected options); and the file
  formats are each vendor's.

Under D3b, CLI-locked logins (rows 1, 4, 5, 7, 10, 11, 13, 14, 20) stay where their CLI keeps them, one per harness,
and are labelled as such (D6). codex's API key stays its own copy until a measurement shows a way to hand codex a
shared key per session (a per-session `CODEX_HOME` or a `model_providers` entry, ADR 0128 decision 5; **not
measured**), because codex ignores `OPENAI_API_KEY` in its environment (`codex/auth.go:25-26`).

One consequence to state on screen: opencode's shared `serve` receives **every** stored provider key
(`opencode/auth.go:50`), so under D3b a key a member enters "for claude" also reaches opencode unless "Use for"
excludes it. Today that is silent.

### D4. Revocation: refuse what is bound, stop what runs, never move a session

What exists: a claude `anthropic-api` session is bound to the key's fingerprint, so a replaced or removed key refuses
its next start (`claude/route.go:260`, ADR 0128 note of 2026-11-04, round 1 (5)); a running pane keeps the key it
started with until it stops; opencode's `serve` keeps its environment until restarted (`OpencodeRestartRow`,
`OpencodeCard.tsx:157`).

- **D4a. Leave it as it is.** A removed key keeps working in running opencode serves and claude panes for as long as
  they live.
- **D4b. Refuse bound sessions at their next start, stop running sessions on the route guard sweep, restart the
  shared daemons without the credential** *(recommended)*. Removing a credential extends `RouteWatcher`
  (`SweepRouteGuards`, `internal/sessionx/session_route.go:147`) to "the bound credential is gone or replaced"; the
  session is halted (resumable, as the route guard already does) and the notice names the credential. A shared
  `serve` or app-server is replaced by D12's **security restart**, not by the ordinary one.
- The delete and replace dialogs say what will happen before it does: "Used by claude (2 stored sessions), opencode
  (shared server)". A replacement is a new fingerprint: bound sessions are refused, never moved (ADR 0128 decision 3).
- Revocation at the vendor (a key revoked in the vendor's console) is not visible until a call fails with an
  authentication error; nothing polls for it.

### D5. Spend is attributed to the credential, not only to the route

- **D5a. Route and billing only** (today). Two Anthropic keys, or codex's key and the shared OpenAI key, are one row.
- **D5b. A connection reference on every ledger row** *(recommended)*: the non-secret binding the route already
  computes (`ANTHROPIC_API_KEY@<fingerprint>`, `<profile>@<region>#<fingerprint>`, `<source id>#<fingerprint>`).
  A CLI-locked login or key gets a **stable, non-secret binding** of the same kind: a hash of the account id where the
  login file carries one (as ADR 0136 decision 6 requires for pi's ChatGPT login), a fingerprint of the key for an
  API-key login (codex's row 5, muse's API key), and otherwise a generation number the Agent bumps whenever a login
  replaces the file. A token refresh keeps the binding; a login to another account or a new key changes it. Which
  login files carry a stable account id is **not measured** for claude, codex, muse, agy, cursor and kiro. The binding
  is captured when the call runs (ADR 0128 decision 8), so a ledger record written late, after a revocation or a
  replacement, keeps the binding it ran on. It joins the rollup key beside `Route` and `Billing`, with the
  migration ADR 0128 decision 8 describes for those (once raw rows age out, a key without it cannot be split again).
  The Model credentials page (D16) shows this month's estimated spend per credential, split by harness.
- **D5c. A spend counter per credential**, kept apart from the ledger. Rejected: a second source of truth.
- Provided routes keep the CP's own per-call table (`provided_route_call`, ADR 0129 D4/D5); their budget is the CP's.

### D6. How the UI says "only this harness can use this login"

Every credential card carries a **Usable by** line computed from the catalogue: the harnesses that declare the
credential's route and can take this credential. A CLI-locked login shows one harness and the reason in words ("Only
Codex can use this login: the Codex CLI keeps it"), and its controls stay on that card. Where one account is signed
in twice (the ChatGPT login of codex and of pi), both logins are listed under OpenAI as two entries, each labelled
with its harness; the page does not suggest they are one.

## 4. Decisions on deployment and tenant restrictions

### D7. Layers, and how they combine

| Layer | Who | Scope | Can |
|---|---|---|---|
| 1 | operator | the deployment | set the **ceiling**: allowed classes and destinations (D9) |
| 2 | super_admin | the deployment, and each tenant | narrow layer 1 deployment-wide and per tenant; grant provided routes (ADR 0129 D3) |
| 3 | tenant_admin | their tenant | narrow layer 2 for the tenant; budgets and member grants for provided routes (ADR 0129 D3) |
| 4 | member | themselves | switch harnesses off, hide models, choose "Use for" (D3) |

- **D7a. Intersection, deny wins** *(recommended)*. The effective set is the intersection of every layer's allowed
  set. A layer can only remove; nothing below can put back what a layer above took away. A provided route also
  needs ADR 0129's grant and a budget above zero, so for those routes the policy is an outer filter and the grant an
  inner one.
- **D7b. Override** (each layer replaces the one above). Rejected: a member could widen a tenant's rule.
- **D7c. Allow and deny lists at each layer with precedence rules.** Rejected for now: the same configurations are
  expressible with D7a, and precedence rules are where an auditor stops being able to read the result.

Layer 3 is tenant-wide in this record. Narrowing per member or per role is open question 2; ADR 0129's per-member
grant stays the only per-member control, for provided routes.

### D8. Defaults, and the relation to ADR 0084 and ADR 0129

- **No restriction is the default at every layer**: all classes and destinations allowed, which is today's
  behaviour. This is ADR 0084 decision 7's rule for a switch that **removes an existing capability**: absent means
  allowed, and an upgrade changes nothing.
- Provided routes keep ADR 0129 D3's opposite default, because they are a **new capability**: off until a super_admin
  grants them and a tenant_admin gives a budget. The two rules do not conflict: the policy says what is permitted,
  the grant says what is offered.
- **The policy is not stored in `tenantLimits`.** That handler rewrites the whole blob and writes no audit row (ADR
  0084 decision 7, `control-plane/internal/tenantsrv/tenants.go`). The policy gets its own rows and endpoints, each
  write audited, as ADR 0129's grant and budget writes are. **The three administered values are kept apart**: the
  deployment-wide rule (super_admin), each tenant's cap (super_admin) and each tenant's own rule (tenant_admin) are
  separate rows with separate, role-scoped endpoints, and the approved self-hosted endpoints of §2 likewise per
  layer. A tenant_admin writing or clearing the tenant's rule never touches the super_admin's cap for that tenant,
  and the reverse; the effective policy is computed from all of them at read time. That separation is what makes
  D7's intersection hold in storage.
- The engine's tenant gate (`allow_engine_llm`, `limits.go:135`) stays where it is. It is read as part of the
  `self-hosted` class for the deployment's engine: allowed only if both say so.

### D9. Where a deployment sets it

- **D9a. An environment variable on the Control Plane**, surfaced by each target's deploy configuration (on ECS, a
  CFN parameter beside `ProvidedRoutes`, `deploy/aws/ecs/cfn/30-ingress.yaml:310`, documented in `deploy/aws/ecs/cfn/PARAMETERS.md`).
  Readable from the stack, unchangeable from the Console, holds even if the database is edited. Needs a redeploy to
  change.
- **D9b. A super_admin setting in the database.** Flexible and audited, but a super_admin can widen it, and an
  auditor has to read the database to know the deployment's rule.
- **D9c. Both: the variable is the ceiling, the super_admin setting narrows within it** *(recommended)*. The
  super_admin UI shows the ceiling read-only, with its source ("set by the deployment"). An unparsable or unknown
  value **refuses CP start** rather than reading as "allow all". Docker and native targets set the same variable in
  their environment file. The variable's format is the class and destination sets of D1 (for example
  `classes=provided` or `destinations=aws-bedrock`); its exact spelling is settled in phase M1.

### D10. A harness whose every route is forbidden

A harness is judged on the routes its credentials actually resolve to, not on its name. agy, copilot, cursor and kiro
have one route each (§1.1), all `subscription`. muse and codex have two each: muse's account login (`muse-login`,
`subscription`) or its Meta API key (`meta-api`, `member-key`), told apart by `mechanism` (`muse/auth.go:96-130`);
codex's ChatGPT login (`chatgpt-subscription`) or its API-key login (`openai-api`), told apart by `auth_mode` in its
`auth.json` (`codex/auth.go:19-27`). So under "no subscriptions" a muse signed in with an API key and a codex signed in
with an API key stay usable, and the same holds for the codex image provider, which accepts an API-key login
(`imagegen/codex.go:150-153`). claude on a subscription-only Workspace has nothing left.

- **D10a. Hide it.** Clean, but a member who used it yesterday does not know why it is gone, and an admin cannot
  check the effect from a member's screen.
- **D10b. Show it disabled with the reason and the deciding layer** *(recommended for layers 2 and 3)*: "Not allowed
  by your tenant: subscription logins". In the launch dialog, the repository launch menus, the schedule editor and the
  Agents page. The login controls for the forbidden route are removed from its card (an API-key path that is allowed
  stays); install stays only while some route of the harness is allowed, because a CLI installed but not launchable
  invites the hand login D13 cannot block.
- **D10c. Keep it installable but not launchable.** Rejected for the same reason.
- **Hidden when the operator ceiling (layer 1) excludes every route it has**: nothing in the deployment can change
  it, so showing it disabled is noise; the admin's policy page lists it.
- claude stays launchable whenever any of its routes is allowed and its credential held. That needs the Console's
  availability to follow routes instead of the subscription login (`registry.ts:266`), in phase M2.
- MCP: the kind list a session sees (`create_session`'s enum) omits a kind with no allowed route; a create naming one
  is refused with `route_forbidden` and the deciding layer.

### D11. The empty selection under a restriction

ADR 0128 decision 2 keeps "empty means today's rules", and those rules cannot be proven after launch (§1.4). Whatever
replaces them must not move a member onto a metered bill they did not choose.

- **D11a. Leave empty selections alone.** Every kind without an adapter, and every claude session started without a
  route, would escape the policy.
- **D11b. Under any restriction, every create resolves to an explicit route and stores it, without changing who
  pays** *(recommended)*. In order: the member's **default route** for the harness (a setting the member chose, D16,
  which counts as naming the route), if allowed and its credential held; else, for a kind whose credential resolves to
  exactly one route (agy, copilot, cursor, kiro, muse and codex by their current login mode, lcpp by its connection),
  that route, if allowed; else a refusal (`route_required`) that lists the allowed routes. The first allowed route is
  **never** picked on the member's behalf: under "no subscriptions" an empty claude create is refused, not moved to
  `anthropic-api`. With no restriction at any layer, today's behaviour is unchanged.
- **D11c. Refuse empty selections everywhere.** Rejected: it changes every existing launch for deployments that
  restrict nothing.
- **pi keeps ADR 0136 decision 6**: an empty selection resolves to `chatgpt-subscription` when the member is signed in
  **and that route is allowed**; otherwise it is refused (`route_required`), and a metered route is only ever taken
  when it is named (directly or as the member's default route). This record adds the policy condition and changes
  nothing else in pi's rule.

**A session that already exists is judged on launch-time evidence, never by re-running the create rule.** Re-resolving
it from today's credential or default could change who pays: a codex session that ran on ChatGPT with no route, whose
login was later replaced by an API key, would resolve to `openai-api`; an lcpp session would move between the
member's endpoint and the deployment's engine when the connection changes; a changed default route would move a
legacy session. So:

- From M0 on, every launch records the route it resolved to and its binding (D5) in the session, even when the
  selection is empty. That record, not the current credential, is what the policy judges at resume, and a resume
  whose current credential no longer matches it is a replacement (D4): refused, never moved.
- A session launched before M0 has no such record. Under a restriction its resume is refused with the reason, and the
  member either chooses a route explicitly, which is a recorded transition with the same steps and consent as the
  route switch (ADR 0128 decision 10), or recreates the session. With no restriction, it resumes as today.
- A **running** session follows the same rule on the sweep: judged on its recorded route and binding; with no record,
  halted under a restriction, since it cannot be proven.

### D12. Where the policy is enforced

The CP is the gate where it can see the request; the Agent is the gate for everything, because some creates never
pass the CP. Everything Agent Fleet itself runs on a model credential (sessions, the assistant chat and one-shots,
image generation, shared daemons) is inside this rule; D13's exception covers only what the member runs by hand.

**Delivery and freshness.**

- The CP gives each policy a **revision**, increasing per member. The Agent accepts a policy only with a revision
  higher than the one it holds, and keeps the held one on disk, so a restart, an out-of-order answer or a stale cache
  can never restore a weaker rule.
- The Workspace receives the effective policy at start (environment); the Agent then pulls it from a new internal
  endpoint, like `/internal/route/check` (ADR 0129, Q1 part 2), **at least every 30 seconds**. The CP also says in
  the answer whether any layer restricts anything for this member, and sends the deployment's ceiling separately.
- **The maximum restriction delay**, with the CP reachable, is one pull interval (30 s) for new work and one pull
  interval plus one reconciler tick for running work (the tick's interval is not measured here). For a shared daemon
  it adds the time to stop the old daemon after the abort (below), not the 60 s drain of an ordinary restart. That
  figure is what the policy page states.
- **When pulls fail**: if the last held policy restricts anything, or the deployment has a ceiling, and no pull has
  succeeded for 15 minutes (the route gateway's rule for its Settings pull, ADR 0128 note of 2026-11-05, P2a), new
  work is refused with "the policy cannot be confirmed" until a pull succeeds; running sessions are left alone. If
  the last held policy restricts nothing and there is no ceiling, an unreachable CP blocks nothing: a tightening made
  during the outage applies at the first successful pull. The window is the outage, and the policy page says so.
- **Upgrade**: an Agent that does not report the policy capability is not trusted with a restricted member. The CP
  refuses its creates for a restricted member before waking, and the session-spawned path on such an Agent is the
  same unenforced window as an outage until the image is upgraded; the release notes say so.

**The points.**

| Point | Today | Proposed |
|---|---|---|
| CP create: Console and REST | static route check and provided-route refusal before waking (`control-plane/workspace_handlers.go:1171-1180`) | plus the policy, before waking |
| CP MCP `create_session` (hosted) | static route argument check (`control-plane/internal/mcpsrv/mcp_kinds.go:92`); provided-route refusal (`mcpsrv/mcp.go:763-767`, `control-plane/mcp_wiring.go:138`) | plus the policy |
| Schedules: new, reused, rotated | at save: kind (`checkScheduleKind`, `control-plane/schedule.go:198`) and static route (`scheduleRouteError`, `:212`); provided grants and budget **at fire only** (`scheduler_wake.go:90-93`) | the policy at save (a forbidden route is not saved) and at fire (a tightening after save refuses the fire and sends a notice) |
| Agent create, every path | `createRun.checkRoute` (`internal/sessionx/session_route.go:102`); a session's `create_session` arrives here only (`internal/mcpx/mcp_client.go:270`) | plus the policy; **the authoritative check** inside the Workspace |
| Start and resume of a stored session, Terminal and Managed, a driver switch | `startRefusalOf` (`session_route.go:130`) | plus the policy (D11 for empty selections) |
| A session already running | `SweepRouteGuards` (`session_route.go:147`) for settings files; it skips every session with no route (`:150`) | the sweep covers **every** live session, with or without a stored route: a nil-route session is judged on its launch-time record (D11) and halted when that is forbidden, or when there is no record under a restriction. Halted means resumable, with a `route-stopped` notice naming the layer |
| Input to a live Managed runtime | the send gate | each delivered message (member, peer, scheduled, queued) re-checks the policy; on a forbidden route the message is held and the member told why, never delivered to a runtime about to be halted |
| Route switch (ADR 0128 decision 10; ADR 0129 D6) | `checkRouteSwitch` (`internal/sessionx/session_route_switch.go:142`), target from `RouteSwitcher.SwitchTarget` (`claude/route.go:591`) | the target is filtered by the policy; no allowed target means no offer. **Never** a fallback to another route |
| Fork, move, recreate | the inherited selection is re-validated (ADR 0128 decision 13) | the re-validation includes the policy |
| Credential entry | any login or key can be added | the Agent refuses to start a login, or store a key, whose route is forbidden; a login flow already running when the policy tightens is cancelled (the Agent kills the login process it spawned), and a credential a flow still wrote is kept inactive (D14). A connection with uses other than models (the GitHub connection: git **and** Copilot) is never refused for its other uses: only its model route is judged |
| Provided routes | CP gateway per call: grant, budget, contract (ADR 0129) | plus the policy, per call |
| Deployment engine | ADR 0084's four places | plus the `self-hosted` class |

**Agent Fleet's own utilities: resolve, bind and isolate at execution.** Filtering a menu or dropping a key from an
environment does not hold a route. D14 keeps forbidden logins on disk on purpose; the claude chat runs with the normal
`CLAUDE_CONFIG_DIR` (`chatx/chat_providers.go:2732-2742`); the codex chat shares `~/.codex/auth.json`
(`chat_providers.go:663-677`); opencode keeps its own account login and picks a `small_model` and a default provider
by itself (`opencode/route.go:18-41` records why opencode's Bedrock route had to pin the provider rather than filter
the menu). So every utility call is treated as a launch:

- **Resolve** the route from the credential the call will actually use (the login mechanism or `auth_mode`, the
  provider of the model and of any small or title model), and refuse the call when it is forbidden.
- **Bind** it: the call runs on that credential only, as a session launch does (ADR 0128's pins: unset the other
  credential variables, pin the endpoint, and for opencode an inline configuration whose `enabled_providers` lists
  only allowed providers, with a file that sets `enabled_providers` refused as opencode's route already refuses it).
- **Isolate** shared daemons the same way: the shared opencode `serve` and codex app-server run with only the
  credentials and providers the policy and "Use for" allow, and are replaced when either narrows.
- **A security restart, not the ordinary one.** The ordinary restart drains before stopping, waits up to 60 s and
  then aborts every turn still running (`opencode/serve.go:569-601`, `drain` at `:641-676`), and an adopted daemon,
  which the Agent did not start, is not replaced at all (`Restart` returns false when `cmd` is nil) and keeps its old
  environment. None of that is a revocation. When a credential, a provider or a "Use for" entry is withdrawn from a
  shared daemon: admission of new turns to it closes at once; the turns of sessions using the withdrawn credential
  are aborted at once, without a drain; the daemon is stopped and replaced by one started with the narrowed
  credentials, tagged with a credential generation; a daemon of an older or unknown generation (an adopted one
  included) is never adopted or reused under a restriction but stopped; and if it cannot be stopped or the
  replacement fails, the harness is unavailable, with the reason, until it succeeds. Because one `serve` carries one
  set of keys (ADR 0128 decision 5), **the other sessions on that daemon are interrupted too**: their turn ends as
  interrupted by a credential change, they stay on their own route, and they continue on the new daemon. A daemon per
  credential, which would leave them untouched, is the alternative ADR 0128 decision 5 already names; it is not
  taken here because its memory cost is not measured.
- **Re-check** on every model or provider change and on every fallback: the chat's send-time fallback
  (`chat_providers.go:57-60`) and the image provider order (`imagegen/imagegen.go:527-528`) move only to a backend
  whose resolved route is allowed; none left is an error the member sees.
- **On tightening**, utility calls in flight on a newly forbidden route are cancelled, and daemons restarted.
- **Removing a harness from a credential's "Use for"** is a revocation for that harness (D4): its bound sessions are
  halted and refused, its daemons restarted without the credential.

### D13. What cannot be enforced, and what is detected instead

**A hand login in the Workspace cannot be blocked.** The Agent, every CLI and every shell run as the member's own user
(ADR 0129, Context: the whole Workspace runs as the member). A member can open a shell session and run `codex login`,
type `/login` into a claude pane, export an API key, or download and run any CLI. The Agent-side checks of D12 bind
Agent Fleet's own launch paths, not the member. The policy page says so in one sentence, and this record does not
claim otherwise.

What can be done:

- **Detect, in the places AF knows.** The connection status already reads the CLI login files
  (`GET api/connections`, `console/src/features/settings/parts/useConnections.ts:68`). The Agent reports to the CP,
  periodically and at each change, every credential it finds in the locations of §1.1 (its own store, the CLIs' login
  files, its own environment such as `META_API_KEY`), each as **route id, class and destination, plus a state**:
  *retained* (present before the restriction and held inactive by D14, which is compliant) or *new* (appeared while
  the restriction was in force, which AF's own entry points refuse, so it came from a hand login or an AF version that
  did not enforce). No secret, no account id, no email. Only *new* forbidden credentials are shown as findings.
- **What detection does not see**: a CLI pointed at another home directory, a key exported in a shell, a CLI
  downloaded and run by hand, and any model use from a shell. Detection is a signal, not an inventory.
- **Account.** Every turn of an Agent Fleet session carries a route (D2, D11); a turn recorded on a forbidden or
  unresolved route under a restriction is flagged in the same report. The route is what AF launched with, not proof
  of the credential a CLI used after the member altered it by hand (`/login` inside a pane). Use outside sessions is
  not in the ledger at all.
- **Narrow the network, per deployment.** The egress gateway (ADR 0122) in enforce mode is the only control outside
  the member's reach. Its limits, measured: it is deployment-wide, with no tenant scope (`egress_member.go:188`); it
  decides by host on the client's SNI claim; the Anthropic hosts and `.amazonaws.com` are built-in entries
  (`egress_policy.go:21`, `:103`); and the effective policy is the union of the built-ins and every active admin
  allowlist entry (`control-plane/egress.go:141-145`), so removing a host from the built-ins alone does not keep it
  out: a super_admin could add it back. Proposed: the **operator ceiling** produces a **final deny filter**, applied
  after that union (built-ins, the admin allowlist, static seeds) and in what is pushed to the proxy, which no admin
  entry can override. Which hosts each vendor's login and inference use is **not measured**; whether a Claude
  subscription and an Anthropic API key reach the same API host is not measured, and if they do, egress can deny
  "Anthropic" but not "the Anthropic subscription". Broad entries stay open for other reasons (`.amazonaws.com` for
  Bedrock and the AWS tooling, `.api.aws`, `.github.com` for git), and connections already open when the filter
  changes are not measured either. **Egress therefore narrows; it does not prove** that no other model is used.
- **Provided routes only** is the one configuration where the credential itself is out of the member's hands (the CP
  holds it). Spend on the deployment's or tenant's account is controlled; other model use is narrowed only as far as
  egress can, with the limits above.

### D14. Credentials already stored when a policy tightens

- **D14a. Kept, unusable, marked** *(recommended)*. The member sees the credential with "Not allowed by <layer>; kept
  but not used" and may delete it themselves. No new credential of a forbidden route can be added through the Console
  or the Agent's endpoints (D12, "Credential entry"). Loosening the policy makes it usable again without signing in
  again.
- **D14b. Hidden.** Rejected: the member cannot see or remove a credential they hold.
- **D14c. Deleted.** Rejected: the credentials live in the member's Workspace (the encrypted store, the CLIs' own
  files), not in the CP; deleting them would be an admin reaching into a member's Workspace, irreversibly, for a rule
  that may be reverted.

The admin sees counts and members from D13's presence report, retained and new apart, never the credentials.

### D15. Audit: how an admin proves the restriction holds

- Every policy write (deployment, tenant) writes an audit row with before and after, as ADR 0129's grant and budget
  writes do.
- The admin policy page shows, per tenant: the effective policy, its revision and the layer each part comes from; the
  harnesses and routes it leaves; the stated maximum restriction delay (D12); members with forbidden credentials
  present, *retained* and *new* apart (D13); and flagged turns (D13). The deployment's
  ceiling is shown with its source.
- The proof is the phase gates of §8, run on a live deployment, each with a negative control. A screen that says
  "forbidden" is not evidence; a refused launch on every entry point is.

## 5. Information architecture

### D16. Three pages where there is one

**Settings > Connections > Model credentials** (new; personal). Grouped by destination, each credential a card with
its status, **Usable by** (D6), **Use for** (D3), its policy state (D10, D14) and this month's spend (D5):

| Group | Cards |
|---|---|
| Anthropic | Claude subscription login (Claude Code only); Anthropic API key (Claude Code, opencode, pi) |
| OpenAI | ChatGPT login for Codex (Codex only); ChatGPT login for pi (pi only); Codex's API key login (Codex only); OpenAI API key (opencode, pi) |
| opencode.ai | account login (opencode only); Zen/Go key and workspace id; the billing choice |
| Google | Antigravity login (Antigravity only; also image generation); Gemini key (opencode) |
| Meta | Muse login or API key (Muse only) |
| GitHub, Cursor, Kiro | Copilot (through the GitHub connection, linked to Git); Cursor login; Kiro login |
| Other providers | OpenRouter, Sakana, custom keys (opencode) |
| Amazon Bedrock | marked AWS profiles, read-only with a link to the AWS tab where the mark stays; provided routes, read-only with this month's budget and spend (ADR 0129 Q2) |
| Self-hosted | your llama.cpp; the deployment's engine (read-only status) |

**Settings > Connections > Agents** (harness only). Per harness: install and update; enable (the `CreatePolicy`
switch generalised to every kind); **default route** (new: `agentLaunchDefaults` has no route,
`console/src/lib/settings/prefs.ts:190`); launch defaults and hidden models; resume behaviour; notifications;
remote control; RTK; thinking and streaming. One line, "Runs on", lists the routes it can use now, linked to Model
credentials.

**Settings > Sessions** (new; personal group). The session block now on the Agents tab
(`AgentsTab.tsx:171-250`): stopped-session archive period, default spend cap, peer messaging, child sessions and
their limit, PR feedback, session search.

**Settings > Image generation** (new; personal group). The switch and provider order now on the Agents tab
(`AgentsTab.tsx:253-278`), each provider row with its credential and policy state. It is a feature that uses
credentials, not a credential and not a harness.

**Tenant settings > Model access** (new; manage group). The tenant policy (classes, destinations) with what the
deployment allows shown above it; approved self-hosted endpoints within the deployment's set (§2); provided routes (ADR 0129's tenant view, budgets, member grants); the presence
report and flagged turns (D13, D15).

**Admin > Model access** (new; deployment group). The ceiling, read-only with its source; the deployment-wide policy;
per-tenant caps and provided-route grants; approved self-hosted endpoints; the deployment source (ADR 0129).

**The launch dialog** picks the harness, then the route, then the model:

1. Harnesses with no allowed route are disabled with the reason (D10); hidden if the ceiling excludes them.
2. The route list is the harness's declared routes ∩ the effective policy ∩ the credentials held. Each route left
   out for a reason is shown disabled with it ("No key: add one in Model credentials", "Not allowed by your tenant").
   The preselected route is the member's default route for the harness, else the first allowed. Execution-method
   limits are shown per route (claude's routes are Terminal only, ADR 0128 decision 5).
3. The model list is the route's (the existing `routeModelFilter`, `console/src/features/repos/LaunchModal.tsx:836-850`).

## 6. The configurations, worked through

Two presets are named, because #4159 lists opencode Zen with the subscriptions while Zen is a metered key (D1):

- **No subscriptions**, the preset #4159 asks for: classes − {`subscription`} **and** destinations − {opencode.ai}.
  It removes Zen, Go, the opencode.ai account and opencode's free models with the plan logins. It is the same two
  removals at either layer.
- **No plan logins**: classes − {`subscription`} only. Zen stays, as a metered key like any other.

| Configuration | Set as | What members see | Enforced by | What remains |
|---|---|---|---|---|
| No subscriptions (deployment) | ceiling: the "No subscriptions" preset | agy, copilot, cursor, kiro hidden; claude and codex offered on their API or Bedrock routes; muse offered with an API-key login; opencode with its own provider keys only, without Zen, Go or the account; image generation on codex with an API-key login, and on the engine; the chat on backends whose resolved route is allowed | the Agent at create, start, sweep, input, switch, chat, image generation and shared daemons; the CP before waking | a hand login in a shell (D13): reported as *new*, not blocked |
| No subscriptions (one tenant) | tenant policy: the same preset | as above, but disabled with "Not allowed by your tenant" instead of hidden | the same | the same |
| No plan logins | either layer: classes − {`subscription`} | as above, with Zen available | the same | the same |
| Bedrock only | destinations = {Amazon Bedrock} | claude and opencode on `bedrock` / `bedrock-deployment` (and codex on OpenAI-family Bedrock models once ADR 0129 Q7 lands); every vendor key and login unusable; a member llama.cpp unusable unless its endpoint is approved and the destination list includes it | the same; at the ceiling, also egress's final deny filter (D13), which narrows but does not prove | the member's own AWS account is allowed unless `member-cloud` is removed too |
| Provided only | classes = {`provided`} | only routes the CP holds, within the ADR 0129 grant and budget | the same; the CP gateway per call | other model use, narrowed only as far as egress can (D13) |
| Mixed | ceiling all; a tenant narrows | each tenant sees its own rule | the same | — |

## 7. What changes for existing records

- ADR 0128: open questions 1 and 2 are answered here (§2, D2, D7). Decision 2 ("empty means today's rules") holds
  only where no restriction applies (D11).
- ADR 0129 D3's last bullet ("routes on the member's own credential stay outside tenant control") is amended by D7.
- ADR 0136: pi's routes are classified as `subscription` (`chatgpt-subscription`) and `member-key` (`openai-api`,
  `anthropic-api`) and enter the policy like any other.

## 8. Phases

Each phase lists what is verified on a live deployment, with its negative control. A phase is not done on unit tests
alone, and no phase depends on a later one: what a page shows before the phase that builds its data is left out of
that page, not stubbed.

| Phase | Content | Verified live | Negative control |
|---|---|---|---|
| M0 | Route ids for every credential path, resolved per login mode (D2); class and destination in the catalogue, with per-connection routes; the stable connection binding on ledger rows (D5) and its rollup migration; the launch-time route and binding recorded in every session (D11); the kind catalog reports which routes are usable now. No behaviour change | One turn per kind on a dev Workspace writes the expected route and binding, and the session records them, muse and codex in both login modes; one call of each chat backend and image provider does too; the rollup keeps two Anthropic keys apart | A login replaced by another account (or a new key) changes the binding, while a token refresh does not; nothing a member could launch before is refused |
| M1 | The policy: CP store, REST, audit rows, revisions; the operator variable and CFN parameter (D9); the Agent's pull and freshness rules; every point of D12 **except "Use for"**, including the utilities' resolve-bind-isolate and the shared daemons; credential entry and in-flight logins (D12, D14); the empty-selection rule (D11) | On a dev deployment, tenant A on "No subscriptions": a claude subscription launch is refused from the Console, REST, the hosted MCP tool, a session's `create_session` and a schedule (at save, and at fire after a later tightening), each naming the layer; claude on `anthropic-api` starts; an empty claude create is refused, not moved to the API; muse and codex on API-key logins start, on account logins are refused; the assistant chat answers on an allowed backend or says none is left; image generation skips a ChatGPT-mode codex and agy; a running subscription session, one with an explicit route and one with none, is halted within the stated delay and its resume refused; a queued message to a live Managed runtime is held; the switch offer lists no forbidden target. **Retained-auth isolation**: a Workspace holding both an allowed Anthropic API key and a retained claude subscription login, and an allowed OpenRouter key beside a retained opencode.ai account login with opencode.ai forbidden, forces the chat, the title and small-model calls and the backend fallback; the ledger rows show the allowed route and binding every time. Image generation with codex in API-key mode succeeds. A chat call and an image call in flight when A is tightened are cancelled within the stated delay. A pre-M0 nil-route codex session whose login was replaced by an API key is refused on resume, not resumed on `openai-api` | Tenant B, unrestricted, launches the same sessions. The CP made unreachable for over 15 minutes refuses new work for A and not for an unrestricted member; a replayed older policy revision is ignored; loosening A works within the stated delay without a restart; with the ceiling at `classes=provided`, a super_admin's REST call to widen it is refused; a GitHub git login still works under "No subscriptions". With the allowed key removed, the chat refuses rather than using the retained login. A tenant_admin clearing the tenant's rule leaves the super_admin's cap in force; a tenant_admin approving a host the ceiling excludes, or a vendor host, as self-hosted is refused |
| M2 | The Console: Model credentials, Agents (with default route), Sessions, Image generation; tenant and admin Model access pages with policy, ceiling and provided routes (ADR 0129's Q2 screens can land here); the launch dialog's harness → route → model with reasons; claude's availability by route | Headless Chromium on a dev deployment in each configuration of §6, screenshots kept with the PR; a member with only an Anthropic key launches claude from the dialog | A forbidden route shown disabled cannot be submitted by editing the request (the Agent refuses it, M1); an unrestricted member sees no reason labels |
| M3 | Sharing: "Use for" per credential (D3) and its enforcement; the Anthropic key shown under Anthropic (the store slot unchanged first; a provider-keyed store with a migration after); delete and replace dialogs with "used by"; revocation of D4 | Remove the Anthropic key with one claude and one opencode session running: claude is halted with the reason and refused on resume; opencode's serve is replaced by the security restart, the session using the key is aborted at once and refused, and the removed key is unusable within the stated delay. Replace the key: bound sessions refused, new ones bound to the new fingerprint. Remove opencode from the key's "Use for": opencode loses it, claude keeps it | A long OpenRouter turn on the same serve is interrupted visibly (not silently lost), stays on its route and continues on the new serve; claude sessions on other keys are untouched; an adopted serve is stopped, not reused; a forced replacement failure leaves opencode unavailable with the reason, never on the old daemon; nothing moves to another route; per-credential spend matches the ledger |
| M4 | Detection and network: the presence report with retained and new states (D13), flagged turns, their sections on the admin and tenant pages; egress's final deny filter from the ceiling | A hand `codex login` in a shell under "No subscriptions" appears as *new* within one report interval; on an egress-enforced sandbox with the ceiling at Bedrock only, a hand vendor login fails at the network while a Bedrock route turn answers (needs real AWS; an acceptance run, not CI) | A credential kept from before the restriction shows as *retained*, not as a finding; a super_admin allowlist entry re-adding an excluded vendor host has no effect; whether a connection opened before the filter survives it is measured and written down |

pi's routes enter at M0's level when ADR 0136's P1 lands; they need no phase of their own here.

## 9. Rejected options

- **The harness as the policy unit** (D1a) and **the route id as the admin's unit** (D1b).
- **Override instead of intersection** (D7b): a lower layer could widen.
- **The policy in `tenantLimits`** (D8): the handler rewrites the blob and audits nothing.
- **Shared CLI login files** (D3c).
- **Deleting stored credentials when a policy tightens** (D14c).
- **Enforcing in the Console or the CP only.** A session's `create_session` and any caller holding the Agent's bearer
  reach the Agent's create directly (§1.4).
- **Claiming that a hand login can be blocked.** It cannot from inside the Workspace (D13).
- **Resolving an empty selection to the first allowed route** (D11): it would move a member onto a metered bill they
  did not choose.
- **Filtering menus or dropping keys as the enforcement for utilities and daemons** (D12): the forbidden login is
  still on disk, and the CLI picks its own provider.
- **Taking excluded hosts out of the egress built-ins only** (D13): the admin allowlist is added after them.
- **Re-resolving an existing session from today's credential or default** (D11): it can change who pays.
- **The ordinary daemon restart as a revocation** (D12): it drains for up to 60 s and leaves an adopted daemon on its
  old environment.

## 10. Open questions

1. Per-route exceptions in a policy ("no subscriptions except Copilot")?
2. May a tenant_admin narrow the policy per member or per role, beyond ADR 0129's per-member grant?
3. Does a destination need a Region (for example, Bedrock in one Region only, for data residency)? ADR 0129's sources
   carry a Region set; a member's profile carries a Region.
4. Egress per tenant: ADR 0122 has none, and D13's network control is deployment-wide.
5. On a forbidden credential found present (D13): report only, or also stop that member's sessions?
6. Can codex take a shared OpenAI key per session, and can pi use codex's ChatGPT login? Both **not measured** (D3).

## Consequences

- Every credential, the CLI-locked ones included, gets a route id, and every session turn is recorded on one; spend
  can be read per credential.
- A deployment and a tenant can forbid classes of credential and destinations; nothing below can widen it; Agent
  Fleet's own launch paths, the assistant chat and image generation all honour it, and a hand login is reported, not
  pretended away.
- The Agents tab becomes a harness page; credentials move to a page by destination; session settings and image
  generation get pages of their own.
- With no restriction set, nothing a member does today changes, except that the Anthropic key appears where a
  member would look for it.

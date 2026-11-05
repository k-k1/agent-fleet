# 0128. Harness, route and maker: a model route as a second axis beside the session kind

English | [日本語](0128-harness-route-maker.ja.md)

- Status: **accepted** (2026-11-03). The user took decisions A–C below, the recommended option in each. Nothing is
  built yet. Every `file:line` was read on `c08c6a4b5`. The environment variables and settings named for
  Claude Code's Bedrock, Vertex and gateway routes come from the vendor's documentation and are **not measured**
  under AF's launch flags; every harness × route row in decision 5 is a candidate until its live gate (decision 14)
  passes. Reviewed 2026-11-03 by a separate session (codex `gpt-6.1-sol`) in three rounds (17, 7 and 0 findings); every
  finding was checked against the tree and is folded into this text.
- Follow-ups: #3280, #3281, #3282, #3283 (P0, P1, P2, P4; P3 is #1585)
- Related: [0069](0069-image-generation-providers.md) (credential tiers, `AWSProfileRef`) /
  [0071](0071-self-hosted-inference-engines.md) (the engine gateway and its tokens) /
  [0084](0084-engine-indicator-and-tenant-gate.md) (the engine's four gates) /
  [0093](0093-lcpp-agent-kind.md) (`lcpp`) /
  [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.md) (an AWS profile that asks before use) /
  [docs/log/129](../log/129-pi-agent-kind.md) (pi, Stage 0) / #1585 (pi as a kind)

## Context

A session kind (`claude`, `opencode`, `codex`, …) names the CLI or in-house loop that drives a session. Several kinds
can already reach more than one endpoint or bill, but each does it its own way, and nothing above the kind knows
which one a turn used:

- **opencode** chooses Zen, the Go subscription or a direct provider through the model id prefix
  (`workspace/agent/internal/agents/opencode/catalog.go:5`), and the model can be changed mid-session
  (`opencode/driver.go:990`, `opencode/serveapi.go:393`).
- **codex** signs in with a ChatGPT subscription or an API key, once per `CODEX_HOME`
  (`workspace/agent/internal/agents/codex/auth.go:19-27`).
- **muse** has a subscription login and an API-key login, told apart by `mechanism`, not by the presence of a key
  (`workspace/agent/internal/agents/muse/auth.go:16`).
- **`lcpp`** runs on the deployment's engine or on the member's own llama.cpp connection
  (`secrets.Data.Lcpp`, `harnessEngineToken`, `workspace/agent/engines.go:1312-1315`). The choice is Workspace-wide:
  the member connection, when set, wins for every `lcpp` session.
- **claude** runs on the subscription login, and even there a turn can be billed: past a window, usage credits
  continue at API rates (`claude/abort.go:140-148` already recognises the `/usage-credits` text).

What is missing is a common contract for **choosing** a route, **checking** that the session may use it, and
**accounting** for it. The gaps show in the code:

- `usageCatalogProviders` (`workspace/agent/usage_catalog.go:45`) picks the price table from the kind alone.
  `usagex.Record` (`workspace/agent/internal/usagex/ledger.go:83`) carries `Kind` and `Model`, the rollup key
  (`usage_rollup.go:43`, `keyOf` at `:192`) aggregates on them, and the estimate is priced after aggregation
  (`usage_series.go:275`). A subscription turn and a metered turn of the same model are indistinguishable once
  rolled up.
- `modelKindVendor` (`model_provider.go:122`) maps `kiro` to `amazon-bedrock`, a route standing in for a maker.
- `keepInMenu` (`opencode/catalog.go:146-165`) hides 121 `amazon-bedrock/…` models because no credential can reach a
  session, and names the missing piece: a provider layer "which gives Bedrock a connection and a credential of its
  own".
- `LimitWindow` / `LimitSpend` (`workspace/agent/internal/agents/usagelimit.go:15`) say whether waiting clears a
  limit. They are not a contract type, and codex reports every limit as a window with an account-wide reset
  (`codex/capabilities.go:91`).

Traps that make the change unsafe to do ad hoc:

- Claude Code prefers `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` from the environment over its OAuth file
  (`claude/authexpiry.go:138-142`). A key in the Workspace's own environment would move every `claude` session onto
  the metered bill.
- The Claude auth guard is Workspace-wide: `AuthExpired()` reads the shared OAuth file and the Agent's own environment
  (`claude/authexpiry.go:187`), and both the send gate (`sessionx/session_io.go:1153`) and the live-state display
  (`WireLive`, `claude/claude.go:195`) consult it. A session given an API key would still be refused while the OAuth login is expired.
- Shared daemons read credentials once, at start: opencode `serve` takes provider keys from its environment
  (`opencode/serve.go:159`, `:338`), and Managed codex runs on one app-server (`codex/serve.go:157`,
  `codex/driver.go:173`). A per-session environment does not reach them.
- The engine token is not session-bound in practice: it lives 30 days (`control-plane/engine_token.go:85`), nothing
  revokes it when a session ends, and Managed opencode uses the workspace-scoped one (`engines.go:663`).

## Decision

### 1. Three words

| Word | Meaning | Examples |
|---|---|---|
| **Harness** | The program that runs the agent loop. This is today's session kind, unchanged. | Claude Code, opencode, codex, pi, `lcpp` |
| **Route** | Where the model calls go and with which credential. | Anthropic subscription, Anthropic API, Amazon Bedrock, Google Vertex, OpenCode Zen, ChatGPT subscription, the deployment's engine, a member's llama.cpp |
| **Maker** | Who built the model. Already resolved by `model_provider.go`; unchanged. | Anthropic, OpenAI, Zhipu |

"Provider" is not used for the new axis: `model_provider.go` and the model picker already use it for the maker.
How a turn was **billed** is a fourth fact, recorded per turn (decision 8), because one route can bill in more than
one way.

### 2. `kind` stays; the route is a new, optional selection

The kind string is not split and no `claude-bedrock`-style kinds are added. The tree branches on the kind in 61
places (`grep -rn "case session.Kind\|== session.Kind" --include=*.go workspace/agent control-plane | grep -v _test`
on `c08c6a4b5`), and those branches are about the harness, not the endpoint.

`session.Meta` gains an optional route selection (decision 3). **Empty means "keep today's resolution rules"**: the
model id prefix for opencode, the signed-in mechanism for codex and muse, the member connection or the engine for
`lcpp`, the OAuth file or environment for claude. Empty is not a fixed default route; what the rules resolved to is
recorded per turn (decision 8).

### 3. A selection names a route and a credential, and never falls back silently

A route catalogue entry records:

- `id` (`anthropic-subscription`, `anthropic-api`, `bedrock`, `vertex`, `opencode-zen`, `opencode-go`,
  `chatgpt-subscription`, `openai-api`, `af-engine-llm`, `member-llamacpp`, …);
- the wire protocol (Anthropic Messages, OpenAI Responses, OpenAI Chat Completions, Bedrock, Vertex);
- the credential tier, using ADR 0069's tiers (an existing connection, a member key, an `AWSProfileRef`, a tenant
  key) — the tier says who owns the credential, not which one.

A session's **selection** is the route id plus a stable reference to the chosen connection (for Bedrock, the profile
and the region, as `AWSProfileRef` already carries them, ADR 0069 decision 3). The selection is stored in the session.
When the referenced connection is deleted, expires or is repointed, the session stops with a visible reason; it is
never moved to another connection or to the Workspace default, because that would move its bill to another account.

The Agent owns the catalogue. The Control Plane embeds a generated snapshot, the way `agentkinds/catalog.json` is
generated and kept equal by a test.

### 4. Harness × route is an allow-list declared by the harness

A harness that accepts an explicit selection implements an optional interface:

```go
type RouteAdapter interface {
	Routes() []string                                              // routes this harness may run on
	ApplyRoute(m session.Meta, sel RouteSelection) (RoutePlan, error) // env, args, config, daemon choice
	ResolveRoute(m session.Meta, turn TurnInfo) ResolvedRoute         // what a turn actually ran on
}
```

A kind without the interface accepts no explicit selection; its turns are still recorded with whatever route its
rules resolved to. The allow-list is not a cartesian product, for terms as well as technique: the Claude
subscription login is for Claude Code only (docs/log/129 §0), so `anthropic-subscription` is never offered to opencode
or pi.

### 5. How each harness isolates a route

A per-session route only holds if the credential reaches **that session and no other**. Each harness row names its
mechanism, and every execution method the harness has today must pass the live gate (adding one, such as a managed
claude, is its own scope and gate):

| Harness | Candidate routes (none measured) | Isolation |
|---|---|---|
| claude | `anthropic-subscription`, `anthropic-api`, `bedrock`, `vertex`, a gateway | Terminal only: claude has no managed driver (`managedDrivers`, `sessionx/session_turn.go:36`). Per-session environment for the pane (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_USE_BEDROCK=1`, `CLAUDE_CODE_USE_VERTEX=1`, `ANTHROPIC_BASE_URL` / `ANTHROPIC_BEDROCK_BASE_URL`, vendor docs) |
| codex | `chatgpt-subscription`, `openai-api` | Terminal: a per-session `CODEX_HOME` or `model_providers` entry. Managed: one app-server per credential, or the gateway; a session-side `CODEX_HOME` does not reach the shared app-server |
| opencode | its own providers, `af-engine-llm` | the provider is per request, but the **key** is per `serve` process: one `serve` per credential, or the gateway. Two sessions on the same provider with different AWS profiles cannot share a `serve` |
| muse | subscription, API key | one host process per session already; per-session login state needs measuring |
| `lcpp` | `af-engine-llm`, `member-llamacpp` | in-process, so a per-session connection can be passed; today the resolver reads the Workspace-wide member connection on every turn (`lcpp/driver.go:634`), so an explicit selection has to be handed to it. An empty selection keeps today's rule |
| pi | `chatgpt-subscription`, `openai-api`, `bedrock`, `af-engine-llm` | pi's own provider setting (not built; #1585) |
| agy, kiro, cursor, copilot | one each today | no change |

The route list for each harness belongs in `guide/ref/agents.md` once rows are measured. This table is the plan, not
the reference (`docs/CONVENTIONS.md` §6).

### 6. A route that can change mid-session

opencode can change its model, and with it the provider, during a session. With an **explicit** selection, a model
change is limited to models of the same route, and a change that would cross routes is **refused** for now. The only
route change this record allows on a session with an explicit selection is decision 10's switch; whether a member may
change routes at other times is open question 4, and allowing it later reuses decision 10's validation and state
transition. With an **empty** selection the route is resolved per turn, as today, and recorded per turn.

### 7. Credential delivery — Decision A

- **A1, per-session environment.** The route's key goes into the launch environment, the way opencode's provider keys
  already ride `LaunchPlan.Env` for a Terminal pane (`opencode/opencode.go:127-131`). The agent's shell can read the
  key. For an AWS profile it breaks the rule that AF never hands AWS credentials to a session
  (`opencode/catalog.go:156-158`). It does not reach a shared daemon (decision 5).
- **A2, an AF gateway.** The Agent (or the Control Plane) holds the credential and gives the session a token for an AF
  endpoint. The engine token is the nearest precedent, but **not** a model to copy as it is: it lives 30 days, is not
  revoked when a session ends, and is workspace-scoped for Managed opencode (see Context). A route token must be bound
  to the connection, the route and the session's incarnation; revoked when the session ends, the selection changes, or
  the connection is changed or revoked; and every upstream call it makes re-checks the approval it depends on (ADR
  0126's approval is per session and profile, at most 8 hours or until the session ends, `0126…md:123`). The cost is a
  proxy on the streaming hot path.
- **Taken (recommended): both, by tier.** A1 for a member key the member pasted for their own Terminal sessions, which is
  the same exposure the opencode keys have today. A2 is required for any credential that can do more than call a
  model (an `AWSProfileRef`, a tenant key) and for any route on a shared daemon whose credential differs from the
  daemon's. The global environment never carries a route credential (the `authexpiry.go` trap).

### 8. The ledger records route and billing per turn

- **Route and billing are separate fields.** `Route` is what the call ran on. `Billing` is how it was charged:
  `subscription-included`, `subscription-extra` (usage credits past a window, still on the OAuth login), `metered`,
  `self-hosted`, `free`, or `unknown`. A call whose billing cannot be told from evidence is `unknown`, never
  "included".
- **Billing can change inside one logical turn.** The fold merges several assistant responses into one logical turn
  (`usage_fold.go:309`, `:398-407`), and a tool loop can cross a window: its first model calls included, its later
  ones on usage credits. Billing is therefore recorded per model call, and a logical turn whose calls differ is
  written as one ledger row per billing segment, sharing the logical turn's id and keeping the call count and dedup
  keys. When the segment boundary cannot be proven, the whole turn is `unknown`.
- **Recorded when the turn runs, not when it is folded.** Today the fold builds a ledger row from the `session.Meta`
  it reads later (`usage_fold.go:777`, `usageRecordOf` at `:833`), so a turn folded after a switch would be filed under
  the new route. The resolved route and billing are captured at turn start (or when the runtime reports them), switch
  boundaries are persisted, and auxiliary calls (titles, the assistant chat) are captured the same way.
- **Aggregation keeps them.** `usageKey` gains `Route` and `Billing`; the rollup format and its migration, the
  API/Console aggregates and the price lookup (route first, `usageCatalogProviders` as the fallback for an unresolved
  route) change together. Once raw rows age out, a key without them cannot be split again.
- **History.** Existing rows are classified only where evidence exists; the rest stay `unknown`.
- **Budgets.** The session budget is computed on its own path (`usage_spend.go:85`). This record does not change what
  an existing budget counts; whether a budget counts real charges or API-equivalent estimates is stated in the UI, and
  changing it is a separate decision.

### 9. The Claude auth guard becomes per route

`AuthExpired()`, the send gate and launch judge the session's resolved route, not the Workspace's OAuth file. A
session on `anthropic-api` or `bedrock` is not refused because the subscription login expired, and recovery after
re-authentication is per route. This is part of P1, not a follow-up: without it the first API session is blocked by
an unrelated login.

### 10. Switching away from a stopped subscription — Decision B

The use case: a session stops on its subscription window and the member wants to keep going.

The offer appears only when the session's resolved route is a subscription, the stop has evidence **on that route**,
and the limit is one that waiting clears. Limit state and reset times are kept per connection and route, and transient
throttling (for example Bedrock's) is handled apart from window limits.

- **B1, manual switch.** The Console offers "continue on <metered route>" when the harness declares one and the member
  holds its credential. Accepting relaunches the session on that route.
- **B2, usage credits, as an account setting.** Usage credits are enabled, funded and capped on the Claude account,
  not per session: once enabled, every request past the window on that login is billed, which includes the member's
  other sessions and the assistant chat. AF does not toggle them. B2 is two separate things: a link to the account
  setting, stating that it affects every session on the login; and, when credits are already enabled, resuming this
  session on them (billed as `subscription-extra`). A session-only credit control would need its own measurement and
  live gate first, and none is known to exist.
- **B3, opt-in automatic.** As B1 without asking, behind a per-member daily spend cap that cannot be empty.
- **B4, none.**
- **Taken (recommended): B1, and B2 as described (link plus resume on credits already enabled); B3 only in a later ADR,
  after B1 has been used.**

A switch is an exclusive state transition, and the gate covers every step: take the session's lock; cancel the
rate-limit resume booking (`sessionx/rate_limit_resume.go`), failing visibly as a move already does
(`rate_limit_cleanup.go`) so a stale "carry on" never lands on the metered route; hold pending input; stop the old
runtime; store the new selection; resume; and on any failure roll the selection back and say so. A crash in the middle
is recovered from the persisted step.

Prompt-cache sharing across the switch is not guaranteed (the cache is scoped to the account and an identical
prefix), so the first turn after it may pay cache creation on the whole context, which costs more than plain input on
Anthropic's 5-minute cache. The offer says "may cost more", and the ledger records the real cache read/creation of
that turn; the claim is measured per route pair before it is stated as a figure.

### 11. Bedrock and Vertex — Decision C

A Bedrock route runs on an `AWSProfileRef` (ADR 0069) and therefore on A2 (decision 7).

- **C1:** any profile the member has signed in to.
- **C2:** a profile the member marks as a model route, documented to carry only `bedrock:InvokeModel*`, and subject
  to ADR 0126's confirm-before-use when the profile asks for it, re-checked per upstream call (decision 7).
- **Taken (recommended): C2.** A route is used on every turn, so a broad profile there would be the widest standing grant in
  the Workspace.

Once a route supplies the credential, opencode's `amazon-bedrock` stopgap (`keepInMenu`) is replaced by the route's
availability instead of `AF_OPENCODE_SHOW_BEDROCK`.

### 12. Who refuses what

The catalogue snapshot and the Console picker are presentation; the checks are enforced on both sides.

- **Control Plane**, on every create path (Console, REST, MCP `create_session`, schedules), before waking a
  Workspace: what the snapshot can prove statically, as `CheckCreateKind` does (`agentkinds/agentkinds.go:88`) — an
  unknown route, a route the harness or driver does not declare, a tenant gate that is closed.
- **Agent**, after the wake and before anything runs, and again at every switch: everything that needs live state —
  the model against the route (live model lists are the Agent's, `liveCatalogs`, `sessionx/session_create.go:373`),
  whether the caller may use the referenced connection, and the connection's availability and expiry. The static list
  is checked again too, since the snapshot may be older than the Agent.
- **Permission is use, not ownership.** A member connection may be used by its owner only; another member's private
  connection is refused. A tenant connection (ADR 0069's tenant key, configured once by an administrator) may be used
  by members the tenant allows. A deployment-provided route (the engine) follows its own gates.
- **Existing gates are inherited, not reopened.** A route on the deployment's engine keeps all four of ADR 0084's gates,
  `allow_engine_llm` included (`0084…md:312`). Whether a tenant administrator may allow or deny the new routes is the
  open part (ADR 0084 keeps the engine switch with super_admin, `:297`).

### 13. Entry points and inheritance

The selection is accepted wherever kind and model are: the Console's launch modal, the REST create body, MCP
`create_session` (its schema lists kind and model one by one, `control-plane/internal/mcpsrv/mcp.go:707`), and
schedules for a new, a reused and a rotated session (`control-plane/schedule.go:34`, `scheduler_reuse.go:441`).

| Operation | Selection |
|---|---|
| Resume, and a change of execution method | kept |
| Fork within the same kind (`forkMeta`, `sessionx/session_handlers.go:1616`) | inherited, re-validated |
| Handoff or move to another kind (`moveCreateReq`, `sessionx/session_move.go:605`) | chosen again, validated for the new harness |

### 14. Every row is gated by a live turn

A harness × route row ships only after live turns, in Terminal and in Managed where the harness has both, record:
the route that actually answered (from the transcript or the response headers), the billing the ledger wrote, and a
negative control that fits the route:

- a route with a credential: the same launch with the credential removed fails, visibly;
- a route without one (opencode's free models, a member's llama-server with no key): the same launch with the endpoint
  unreachable fails, visibly.

Either way, the control also shows that nothing fell back to another route silently. This is what keeps decision 5's
table from becoming a menu of models that fail on the first message, which is what `keepInMenu` exists to prevent.

## Phases

| Phase | Content | Gate |
|---|---|---|
| P0 | Route and billing captured per turn; rollup key, migration and aggregates; price lookup by route; terms in `guide/ref/glossary.md` | No behaviour change; history left `unknown` where unproven; a subscription and a metered turn of one model stay apart in the rollup |
| P1 | `RouteAdapter`; selection stored and validated (decision 12); claude `anthropic-subscription` / `anthropic-api` in Terminal through A1; per-route auth guard (decision 9) | Decision 14 for both rows; an API session runs while the OAuth login is expired |
| P2 | The A2 gateway and its token contract; claude `bedrock`; opencode Bedrock through the route; per-credential daemons | Decision 14 in every execution method each harness has; C2's profile check; token revoked at session end |
| P3 | pi as a kind, if #1585 adopts it | #1585's Stage 0b, then decision 14 per route |
| P4 | Decision 10's B1 offer and its state transition; B2's account-setting link and resume on enabled credits | A live window stop, switched, resumed; the old resume booking cancelled; a forced failure rolled back |

## Rejected options

- **One kind per harness × route** (`claude-bedrock`, `pi-openai`). The combinations multiply, and every new kind has
  to be registered, gated, priced and shown, while the kind branches describe the harness and gain nothing.
- **Splitting the kind string** (`claude/bedrock`). It changes the meaning of a field that the Control Plane,
  schedules, the MCP tools and stored sessions all read, for a gain the optional selection gives without breaking
  anything.
- **A single global route setting per Workspace.** It cannot express "subscription for interactive work, API for a
  scheduled batch", and for claude it reproduces the environment-precedence trap.
- **Recording the route from the session's metadata at fold time.** It files turns from before a switch under the
  route after it (decision 8).

## Open questions

1. The Console wording for "route" (an i18n term in `console/src/lib/i18n/locales/`, chosen in P1).
2. Whether a tenant administrator may allow or deny routes per tenant (decision 12).
3. How Bedrock throttling is shown, given that it is neither a window nor a spend limit (decision 10).
4. Whether a route may be changed outside decision 10's offer, for example on an explicit resume.
5. Whether a budget should count real charges or API-equivalent estimates (decision 8).

## Consequences

- A session gains one optional selection; with it empty, today's resolution rules run unchanged, and the turn's
  resolved route is recorded.
- Cost reporting can tell subscription, usage-credit and metered use apart once P0 lands, for turns recorded after it.
- Bedrock and Vertex become possible for claude without a new kind, and opencode's Bedrock models come back through
  the same mechanism.
- Each new route on each harness costs one adapter row, an isolation mechanism and one live gate, not a new kind.

## Notes

- 2026-11-03, P0a (#3280): `Route` and `Billing` are on `usagex.Record` and in the rollup key, read from per-call
  evidence only. opencode's route is the `providerID` opencode wrote on that call's message (`opencode` →
  `opencode-zen`, `opencode-go` → `opencode-go`; any other provider stays empty). `lcpp` writes the serving
  connection (`af-engine-llm` or `member-llamacpp`, billing `self-hosted`) on each assistant record when the call
  runs; older log lines stay empty. Empty `Billing` reads as `unknown`. A logical turn whose calls disagree on
  route or billing is written unresolved instead of split (the split of decision 8 is left to P0b, where a kind
  can prove a segment boundary). Price lookup tries the route's models.dev provider first, then the kind's table.
- 2026-11-03, P0b (#3285): billing is recorded where a call's own record proves it, and left unresolved otherwise.
  **codex** stays unresolved: the `rate_limits` block of a `token_count` event is the session's latest snapshot re-sent
  with every event (upstream keeps it in session state and clones it into each event), so a stale plan snapshot rides on
  a call made after an API-key switch or on credits; nothing in the event says it is fresh or belongs to that call. It
  needs a per-call signal from codex. **opencode**: a priced `opencode` (Zen)
  call is `metered`; a zero cost, Go, and direct providers stay unresolved (Go can spill onto the balance without the
  message saying so). **claude** and **muse** stay unresolved: the claude transcript line carries no login or
  usage-credit signal, and for both the only other evidence is the current credential file or environment, which a
  switch (`/login` in the pane, `muse auth set`) changes under a running process, so a launch-time snapshot is not
  evidence either. Both need a per-call signal from the harness. **Split**: a logical turn whose calls disagree is
  written as one row per run of calls with the same route and billing when every call proves its billing; one unproven
  call leaves the whole turn unresolved. The rows share `Idx` and the call id (the aggregation counts one call), the
  first keeps its place in the `(ref, idx)` sequence and the others carry a key of their own. The turn's input and
  cache figures replace per call, so they sit on the segment of the call that last reported them and the rows sum to
  the unsplit totals. Subagent transcripts are not split. Not run against a live workspace.
- 2026-11-04, P1a (#3281): the contract, with no harness behaviour change. `workspace/agent/internal/routes` holds the
  route catalogue (id, wire protocol, credential tier; the ids are the ones the ledger already records) and the
  `Selection` type (route id and an opaque connection reference); `agents.RouteAdapter` (decision 4) and an optional
  `RouteChecker` for the live half of decision 12 sit in `agents`. `session.Meta.Route` is a pointer with `omitempty`,
  so a stored session and a create without a selection read, write and behave as before. No harness implements
  `RouteAdapter`, so an explicit selection is refused everywhere (`route_unsupported`, or `bad_route` for an empty or
  unknown id), never ignored. The CP embeds `control-plane/internal/agentroutes/catalog.json`, generated from the
  catalogue and the harness allow-lists by `TestRouteCatalogSnapshot` (`-update-route-catalog`), and checks it before
  waking a Workspace on the Console/REST create, MCP `create_session` (`route`, `route_connection`) and schedules (new,
  reused and rotated alike: the one check at the top of the fire, plus create and update; an assistant schedule refuses
  a route because it would be ignored). The Agent repeats the static list and runs the live checks after the wake, on
  create, fork and recreate (inherited and re-validated) and at a move's request. A move never carries the source's
  selection: the request names the target's own. The allow-list is per kind, not per driver, for now: decision 5's
  Terminal-only claude rows are the adapter's to enforce when one exists. The idempotency fingerprint now includes the
  route, only when present. Not built: any adapter, the Console picker and the per-route auth guard (P1b).
- 2026-11-04, P1b (#3281): claude implements `RouteAdapter` and `RouteChecker` for `anthropic-subscription` and
  `anthropic-api`, Terminal only (a managed create naming a route is refused by the create's own driver check). The
  credential rides this pane only: `anthropic-api` puts the member's Anthropic API key connection (the provider-key
  entry `ANTHROPIC_API_KEY`, read from the member's own store, so permission to use it is ownership; any other
  connection reference is `route_connection_forbidden`, a missing key `route_connection_unavailable`) into
  `LaunchPlan.Env`, and both routes put `env -u` in front of the CLI for what would override them: the other route's
  credential (`CLAUDE_CODE_OAUTH_TOKEN` for the API route; `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` for the
  subscription route) and `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`,
  `CLAUDE_CODE_USE_VERTEX`. The key is never in the command string and never in the Agent's environment. The guard
  (decision 9) is `AuthExpiredFor(meta)`: no selection answers exactly `AuthExpired()`; `anthropic-api` is never
  expired by the OAuth file; `anthropic-subscription` judges the file as if no token were in the environment, since
  the launch removes them. It is asked by the live state, the send gate, the initial-prompt delivery, the peek check
  and the mirror chip. A stored selection is re-checked at every start (`startRefusalOf`). The ledger: claude's usage
  turns of an explicit selection carry its route, and `anthropic-api` turns are `metered`; `subscription` turns stay
  billing-unresolved, as usage credits can bill the same login. The selection cannot change while the session
  exists, which is what makes it per-call evidence until P4's switch persists its boundaries. Subagent transcripts are
  not stamped. The live kind catalog (`GET /agents/kinds`) lists a kind's `routes`, and the Console launch dialog
  shows a picker only for such a kind, in a Terminal launch. Open question 1 is answered for now as "model route"
  (en) / "モデルのルート" (ja), which keeps the glossary's "route" / ルート. Not measured, and the reason this is a
  candidate until decision 14: whether the TUI asks to approve a custom API key on first use, whether the OAuth file
  stays unused when `ANTHROPIC_API_KEY` is set, and what the transcript or headers say about the route that answered.
- 2026-11-04, P1b review round 1 (#3316): five corrections to the note above. (1) `env -u` does not reach the settings
  files' `env` blocks and `apiKeyHelper`, which the CLI re-applies after start, so an explicit claude route is refused
  (`route_settings_conflict`, naming the file) when a user, project, local or managed settings file sets the route's
  credential or endpoint variables; checked at create, at every start and at the launch, where the working copy is
  known. A file edited while the pane runs is not covered (#3317). (2) The ledger's real source is
  `UsageSource`, not `UsageTurns`; both stamp the route now. (3) A scheduled fire naming a route is refused before
  anything is sent when the existing target is not on it: the Agent's session wire carries `route`, and a target with
  none, another one, or an Agent that does not report it all refuse. (4) An API-key session is never auto-resumed on
  the OAuth login's renewal and gets no login moment on the wire; recovering from a bad key is a new session. (5) The
  stored connection reference is bound to the key instance (`ANTHROPIC_API_KEY@<sha256 prefix>`, no secret), so
  replacing or removing the key in Settings refuses the next start instead of moving the session to another bill. A
  running pane keeps the key it started with until it stops.
- 2026-11-04, P1b review round 2 (#3316): a settings file edited while the pane runs is now covered by stopping, not
  by pinning: `agents.RouteWatcher` (claude: the same settings check) is swept on the reconciler tick
  (`SweepRouteGuards`), and a running session whose route no longer holds is halted like the stop button (resumable)
  and a `route-stopped` notification names the file; starting it again is refused while the cause remains. The
  window is one tick. `CLAUDE_CODE_USE_FOUNDRY` joins the unset list and the settings check. A scheduled reuse
  fire compares a bound connection reference exactly (a bare slot compares by slot), and `BindRoute` re-checks a
  fingerprint against the single key read it binds. #3317 now only tracks the optional flag-layer pin.
- 2026-11-05, #3344 (live check of P1b): (1) the CLI's "custom API key" prompt (No preselected = the claude.ai login)
  is answered before launch: the anthropic-api launch records the bound key's last 20 characters in
  `customApiKeyResponses.approved` of the CLI's own `.claude.json` (and withdraws it from `.rejected`), through the
  same file the folder-trust step already writes; a write that fails refuses the launch. (2) The "both logins" banner
  is accepted, documented in guide/ref/agents. (3) The no-key refusal and the `route_connection` descriptions name
  Settings > Connections > Agents > the OpenCode card > API keys (Anthropic preset). (4) A "credit balance" abort on an
  anthropic-api session is no longer a limit episode (no `spend_limit`, queued messages deliver); a "spend limit" text
  and every other route keep today's behaviour.
- 2026-11-05, #3317: measured (claude 2.1.318, local server, temporary config dir): the `--settings` flag layer accepts
  empty values and outranks the user/project/local files (a user-settings `ANTHROPIC_BASE_URL` beat the process
  environment; an empty flag-layer value beat that; edits to user settings while a stream-json session ran changed neither
  endpoint nor credential headers; an empty `ANTHROPIC_AUTH_TOKEN` sends no Authorization header; with no login, a
  pinned-empty subscription launch reports "Not logged in" instead of using a user-settings key). So the launch now pins
  everything `env -u` removes, plus `apiKeyHelper`, to empty inside the one existing `--settings` object. Not pinnable:
  managed settings (the CLI's documentation ranks them above the flag layer; not measured here, no root to write /etc) and the anthropic-api key itself (never on a command line); those stay with
  the launch refusal and the `RouteHeld` sweep, which is unchanged. The measurements ran `claude -p --input-format stream-json --output-format stream-json`, not the interactive TUI pane
  Fleet launches (unmeasured; the settings layering is the same code path by the CLI's documentation, but that is not
  evidence), and not a multi-hour watch. The "Not logged in" case is not reachable in Fleet: `settingsConflict` refuses
  such a launch first; it only shows the pin's precedence.
- 2026-11-05, P2a (#3282): the A2 gateway and its token contract, with one upstream (Amazon Bedrock) and no harness
  wired to it. **Where it lives: the Agent**, in `workspace/agent/internal/routegw`, on a loopback listener of its own
  (not the Agent's mux, whose bearer is in every session's shell). The credential, the SSO cache and ADR 0126's grants are
  all in the Workspace, so a Control Plane gateway would have to be handed the credential or ask the Agent for it on every
  call; the Agent holds it in its own memory and the session never sees it, and the hot path is a loopback hop. Revocation
  is a local file or map read, with no push to a remote proxy. Fargate/ECS: the Agent is in the same container as the session,
  so it works there as it does elsewhere, and the egress to `bedrock-runtime.<region>.amazonaws.com` is the Workspace's own.
  **Token**: random, held as a hash in the Agent's memory only (an Agent restart ends every token), bound to the session's
  incarnation (`awsx.Incarnation`, ADR 0126's key), the route, the connection (`<Settings profile id>@<region>`,
  `routes.AWSConnection`) and the profile's identity; at most 8 hours; one live token per session. It is minted only by
  the Agent's own launch code from the session's stored selection (`Gateway.Issue`); there is no HTTP route that mints. **Every
  call** re-checks, in order: the token, that the session exists in the same incarnation, that its stored selection is still
  this route and connection, that the Settings profile is still the member's own, marked, unchanged and in a usable
  region, and that ADR 0126's approval is held (a grant file read; when it is missing the request is filed, at most once per
  10 s per session and profile, and the call is refused). A failure other than "Settings unreadable" or "approval missing"
  revokes the token on the spot. **Settings** are pulled at most every 30 s for this check and the gateway refuses once
  none has succeeded for 15 minutes. **C2**: Settings > AWS profiles/SSM gains a "model route" mark (`modelRoute`, migration
  0122, carried by the bridge and by settings export/import); an unmarked profile is refused visibly, at issue and at every
  call. The mark is the member's statement that the role carries only `bedrock:InvokeModel*`; AF cannot read the role. The
  gateway forwards only `invoke`, `invoke-with-response-stream`, `converse` and `converse-stream`, so the profile's other
  permissions are unreachable through it. **Upstream**: the harness speaks Bedrock's wire with the route token as its
  bearer; the gateway signs with SigV4 (hand-written, pinned to AWS's published vector and to the SDK signer's output), streams
  the response with a flush per read, and never forwards the caller's `Authorization`. Credentials come from `PlanExec`
  (what af-aws-exec runs, with the login forced off), are cached in memory until 5 minutes before they expire and are never
  logged or handed to a session. **Ledger**: each forwarded call writes `feature=route.gateway`, `Route=bedrock`,
  `Billing=metered`, with the counts read from Bedrock's response (headers for `invoke`, the invocation metrics or the
  `metadata` event for streams; unknown stays `none`, never 0), priced from `amazon-bedrock`. Not measured (no live AWS
  here): everything decision 14 asks, SigV4 acceptance by Bedrock itself, Claude Code's behaviour with
  `AWS_BEARER_TOKEN_BEDROCK` and a base URL on loopback, the event-stream framing of a real response, and the behaviour of a
  real SSO login and STS check on this path. Later slices (not built): claude `bedrock` RouteAdapter; opencode Bedrock
  through the route in place of `keepInMenu`; per-credential `serve` / app-server; hooks that revoke at session stop; the
  Console picker. When a harness is wired, its own transcript rows and the gateway's must not both be counted
  (the gateway's carry `feature=route.gateway`).
- 2026-11-05, P2a review round 1 (#3349): one limit stated against decision 5. The route token is a bearer on a
  loopback port, and a Workspace runs every session as one user, so another session of the same member that can read this
  session's process environment can present the token while this session lives; the call is then checked against the
  token owner's session (and its approval), and its ledger row is filed under the owner. A token cannot outlive its
  owner (the incarnation check revokes it), and it never reaches another member. P2a accepts this for the slice because no
  harness holds a token yet; it is **not** accepted as the final design. Before a harness is wired the slice has to
  narrow it: an eager `RevokeSession` on session stop (the registry has it, nothing calls it yet), a shorter TTL, and a
  caller binding (a Unix socket with peer credentials mapped to the session's process tree). Also: the gateway sends
  response headers as soon as Bedrock answers, drops the query string (no operation takes one), and resolves
  credentials outside its lock, one resolution per profile at a time.
- 2026-11-05, P4a (#3283): the backend of decision 10's B1. `GET /sessions/{name}/route-switch` answers whether a switch
  would be accepted (`allowed`, the target, a refusal code, the two caveats of the offer, and B2's account-setting
  link); `POST` does it. The harness declares the target through `agents.RouteSwitcher` (claude: `anthropic-api`). Allowed
  only for a Terminal session whose route is the subscription, with **limit evidence on that route** (the kind's
  `AtLimit`: a window; a spend limit is refused) and the session idle, stopped or at its limit; decision 12's checks
  (permission is use, key held and unreplaced, start accepted) run before anything is stopped. **Empty selection
  (decisions 2, 6)**: offered only when claude can show the subscription is what it runs on, by the evidence an
  explicit `anthropic-subscription` launch relies on (no token, key or endpoint override in the Agent's environment, an
  OAuth record stored, no settings file of the user or the working copy that sets one); otherwise refused as
  `route_switch_route_unproven`. The stored selection is explicit afterwards, and the turns before the boundary stay
  unresolved in the ledger, as they always were. **The transition**: the session's lifecycle gate is taken exclusively
  for the whole switch, so a member message, a peer message or a scheduled prompt waits there and lands after it;
  the step record (`session-route-switch`) is persisted at begun, booking, stopped, stored and resumed; the booking id
  goes into the record, is recorded as cancelled and deleted at the CP (a delete the CP did not confirm is retried by
  the sweep and reported as `bookingCancelPending`, and a fire that arrives first is refused by `refuseCancelledResume`
  at the input gate, also for any resume booking the switched session's episode does not hold); the old pane is
  stopped; the selection and the boundary (`Meta.RouteSwitches`) are stored in one write; the session starts on the
  new route; the old episode closes. A failure rolls back: a pane the new route started is stopped, the old selection
  is restored, the resume is booked again, and a session that was running is started again on the old route, else it
  is left stopped on it, so the stored selection is always the route a start would use. A crash is recovered at boot from
  the meta and the process, not the step alone (`RecoverRouteSwitches`); a runtime is never started at boot. **Ledger**:
  claude's usage turns are filed under the selection in force at their own timestamp (`Meta.RouteAt`), a turn whose
  timestamp cannot be read after a switch stays unresolved, and a limit recorded before the boundary is not evidence
  on the new route (`AtLimit`), so the stale transcript tail opens no episode. **B2**: only the link
  (`https://claude.ai/settings/usage`, unmeasured), with `accountSettingAffectsAll`; "resume on credits already
  enabled" is not built because nothing the Agent reads says whether credits are enabled (the transcript carries the
  window error, never the account setting), and AF never toggles them. The resume after the switch is the relaunch
  only: no prompt is typed, so the Console's next message (or a one-click "carry on", P4b) lands on the new route.
  Not measured: everything decision 14 asks of a live window stop.

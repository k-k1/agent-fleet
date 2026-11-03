# 0128. Harness, route and maker: a model route as a second axis beside the session kind

English | [日本語](0128-harness-route-maker.ja.md)

- Status: **proposed** (2026-11-03). Nothing is built yet. This record is the design only, and decisions A–C below
  wait for the user. Every `file:line` was read on `c08c6a4b5`. The environment variables and settings named for
  Claude Code's Bedrock, Vertex and gateway routes come from the vendor's documentation and are **not measured**
  under AF's launch flags; every harness × route row in decision 5 is a candidate until its live gate (decision 14)
  passes. Reviewed 2026-11-03 by a separate session (codex `gpt-6.1-sol`); its 17 findings were checked against the
  tree and are folded into this text.
- Follow-ups: filed as issues when the decisions are taken (`docs/CONVENTIONS.md` §10), one per phase.
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
  (`secrets.Data.Lcpp`, `harnessEngineToken`, `workspace/agent/engines.go:1312`).
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
  (`claude/authexpiry.go:187`), and both the send gate (`sessionx/session_io.go:1153`) and launch (`claude/claude.go:195`)
  consult it. A session given an API key would still be refused while the OAuth login is expired.
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
mechanism, and both Terminal and Managed must pass the live gate:

| Harness | Candidate routes (none measured) | Isolation |
|---|---|---|
| claude | `anthropic-subscription`, `anthropic-api`, `bedrock`, `vertex`, a gateway | per-session environment for a Terminal pane (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_USE_BEDROCK=1`, `CLAUDE_CODE_USE_VERTEX=1`, `ANTHROPIC_BASE_URL` / `ANTHROPIC_BEDROCK_BASE_URL`, vendor docs); a Managed claude is one process per session, so the same |
| codex | `chatgpt-subscription`, `openai-api` | Terminal: a per-session `CODEX_HOME` or `model_providers` entry. Managed: one app-server per credential, or the gateway; a session-side `CODEX_HOME` does not reach the shared app-server |
| opencode | its own providers, `af-engine-llm` | the provider is per request, but the **key** is per `serve` process: one `serve` per credential, or the gateway. Two sessions on the same provider with different AWS profiles cannot share a `serve` |
| muse | subscription, API key | one host process per session already; per-session login state needs measuring |
| `lcpp` | `af-engine-llm`, `member-llamacpp` | already per session (`harnessEngineToken`) |
| pi | `chatgpt-subscription`, `openai-api`, `bedrock`, `af-engine-llm` | pi's own provider setting (not built; #1585) |
| agy, kiro, cursor, copilot | one each today | no change |

The route list for each harness belongs in `guide/ref/agents.md` once rows are measured. This table is the plan, not
the reference (`docs/CONVENTIONS.md` §6).

### 6. A route that can change mid-session

opencode can change its model, and with it the provider, during a session. With an **explicit** selection, a model
change is limited to models of the same route; a change that would cross routes is a route switch (decision 10) and
goes through the same checks. With an **empty** selection the route is resolved per turn, as today, and recorded per
turn.

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
- **Recommended: both, by tier.** A1 for a member key the member pasted for their own Terminal sessions, which is
  the same exposure the opencode keys have today. A2 is required for any credential that can do more than call a
  model (an `AWSProfileRef`, a tenant key) and for any route on a shared daemon whose credential differs from the
  daemon's. The global environment never carries a route credential (the `authexpiry.go` trap).

### 8. The ledger records route and billing per turn

- **Route and billing are separate fields.** `Route` is what the turn ran on. `Billing` is how that turn was charged:
  `subscription-included`, `subscription-extra` (usage credits past a window, still on the OAuth login), `metered`,
  `self-hosted`, `free`, or `unknown`. A turn whose billing cannot be told from evidence is `unknown`, never
  "included".
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
- **B2, usage credits on the same login.** For claude, continuing on the subscription login with usage credits
  (billed as `subscription-extra`) is a third choice beside waiting and switching.
- **B3, opt-in automatic.** As B1 without asking, behind a per-member daily spend cap that cannot be empty.
- **B4, none.**
- **Recommended: B1 and B2 now; B3 only in a later ADR, after B1 has been used.**

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
- **Recommended: C2.** A route is used on every turn, so a broad profile there would be the widest standing grant in
  the Workspace.

Once a route supplies the credential, opencode's `amazon-bedrock` stopgap (`keepInMenu`) is replaced by the route's
availability instead of `AF_OPENCODE_SHOW_BEDROCK`.

### 12. Who refuses what

The catalogue snapshot and the Console picker are presentation; the checks are enforced on both sides.

- **Control Plane**, on every create path (Console, REST, MCP `create_session`, schedules): an unknown route; a route
  the harness or driver does not declare; a model outside the route; a connection reference the caller does not own;
  a tenant gate that is closed. It refuses before waking a Workspace, as `CheckCreateKind` does.
- **Agent**, at launch and at every switch: the same list again (the snapshot may be older than the Agent), plus the
  connection's live availability and expiry.
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
| P2 | The A2 gateway and its token contract; claude `bedrock`; opencode Bedrock through the route; per-credential daemons | Decision 14 in both execution methods; C2's profile check; token revoked at session end |
| P3 | pi as a kind, if #1585 adopts it | #1585's Stage 0b, then decision 14 per route |
| P4 | Decision 10's offer and its state transition | A live window stop, switched, resumed; the old resume booking cancelled; a forced failure rolled back |

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

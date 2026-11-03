# 0128. Harness, route and maker: a model route as a second axis beside the session kind

English | [日本語](0128-harness-route-maker.ja.md)

- Status: **proposed** (2026-11-03). Nothing is built yet. This record is the design only, and decisions A–C below
  wait for the user. Every `file:line` was read on `c08c6a4b5`. The environment variables and settings named for
  Claude Code's Bedrock, Vertex and gateway routes come from the vendor's documentation and are **not measured**
  under AF's launch flags; every harness × route row in decision 4 is a candidate until its live gate (decision 11)
  passes.
- Related: [0069](0069-image-generation-providers.md) (credential tiers, `AWSProfileRef`) /
  [0071](0071-self-hosted-inference-engines.md) (the engine gateway and its session-scoped tokens) /
  [0093](0093-lcpp-agent-kind.md) (`lcpp`, a harness welded to one route) /
  [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.md) (an AWS profile that asks before use) /
  [docs/log/129](../log/129-pi-agent-kind.md) (pi, Stage 0) / #1585 (pi as a kind)

## Context

A session kind (`claude`, `opencode`, `codex`, …) names the CLI or in-house loop that drives a session. Two other
facts are folded into it today, and the code already shows the strain:

- **Which endpoint and which bill.** `usageCatalogProviders` (`workspace/agent/usage_catalog.go:45`) picks the price
  table from the kind alone, so a `claude` session on Bedrock would be priced as Anthropic's API. `usagex.Record`
  (`workspace/agent/internal/usagex/ledger.go:83`) carries `Kind` and `Model` and nothing else, so a turn included
  in a subscription and a turn billed per token are recorded the same way.
- **Who made the model.** `model_provider.go` already resolves the maker, for the brand mark in the model picker,
  and its header says outright that an id prefix names the billing route, not the maker. `modelKindVendor`
  (`model_provider.go:122`) still maps `kiro` to `amazon-bedrock`, a route standing in for a maker.
- **The Bedrock stopgap.** `keepInMenu` (`workspace/agent/internal/agents/opencode/catalog.go:146-165`) hides 121
  `amazon-bedrock/…` models because no credential can reach a session, and names the missing piece: a provider
  layer "which gives Bedrock a connection and a credential of its own".
- **`lcpp`** (ADR 0093) is our own loop and the self-hosted engine in one kind. The same engine already reaches
  opencode as a provider (`syncEngineProviders`, `workspace/agent/engines.go:323`), so a route can already cross
  harnesses; it just has no name.
- **Subscription and metered limits are already told apart**: `LimitWindow` / `LimitSpend`
  (`workspace/agent/internal/agents/usagelimit.go`). What is missing is a way to act on the difference.
- **pi** (docs/log/129) is a harness that speaks to many providers. Without a second axis it can only join as
  one kind per provider.

Two traps make the change unsafe to do ad hoc:

- Claude Code prefers `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` from the environment over its own OAuth file
  (`workspace/agent/internal/agents/claude/authexpiry.go:138-142`). An API key in the Workspace's own environment
  would silently move every `claude` session onto the metered bill.
- codex ignores an env-injected `OPENAI_API_KEY` and keeps one `auth.json` per `CODEX_HOME`
  (`workspace/agent/internal/agents/codex/auth.go:25`), so "subscription for this session, API for that one"
  cannot be an environment variable for codex.

## Decision

### 1. Three words

| Word | Meaning | Examples |
|---|---|---|
| **Harness** | The program that runs the agent loop. This is today's session kind, unchanged. | Claude Code, opencode, codex, pi, `lcpp` |
| **Route** | Where the model calls go, with which credential, billed how. | Anthropic subscription, Anthropic API, Amazon Bedrock, Google Vertex, OpenCode Zen, ChatGPT subscription, the deployment's engine |
| **Maker** | Who built the model. Already resolved by `model_provider.go`; unchanged. | Anthropic, OpenAI, Zhipu |

"Provider" is not used for the new axis: `model_provider.go` and the model picker already use it for the maker.

### 2. `kind` stays; the route is a new, optional field

`session.Meta` gains `Route string`. Empty means the kind's default route, which is what every kind uses today, so
every existing session, API caller and schedule keeps its behaviour. The kind string is not split and no
`claude-bedrock`-style kinds are added: the tree branches on the kind in about 60 `case`/`==` sites and references
it in about 525 places, and every combined kind would be one more copy of all of them.

### 3. A route is a catalogue entry

A route records:

- `id` (`anthropic-subscription`, `anthropic-api`, `bedrock`, `vertex`, `opencode-zen`, `chatgpt-subscription`,
  `openai-api`, `af-engine-llm`, …);
- the wire protocol (Anthropic Messages, OpenAI Responses, OpenAI Chat Completions, Bedrock, Vertex);
- the billing kind: `subscription`, `metered`, `self-hosted` or `free`;
- the credential tier, using ADR 0069's tiers: an existing connection, a member key, an `AWSProfileRef`, or a
  tenant key.

The Agent owns the catalogue. The Control Plane embeds a generated snapshot, the way `agentkinds/catalog.json` is
generated and kept equal by a test.

### 4. Harness × route is an allow-list declared by the harness

A harness that offers more than one route implements an optional interface:

```go
type RouteAdapter interface {
	Routes() []string                                        // routes this harness may run on
	ApplyRoute(m session.Meta, route string) (RoutePlan, error) // env, args, config patch
}
```

A kind without the interface has exactly one implicit route, and the Console shows no route picker for it. The
allow-list is not a cartesian product, for terms as well as for technique: the Claude subscription login is for
Claude Code only (docs/log/129 §0), so `anthropic-subscription` is never offered to opencode or pi.

Candidate rows (none measured):

| Harness | Candidate routes | How it would be applied |
|---|---|---|
| claude | `anthropic-subscription` (default), `anthropic-api`, `bedrock`, `vertex`, a gateway | OAuth file; `ANTHROPIC_API_KEY`; `CLAUDE_CODE_USE_BEDROCK=1`; `CLAUDE_CODE_USE_VERTEX=1`; `ANTHROPIC_BASE_URL` / `ANTHROPIC_BEDROCK_BASE_URL` (vendor docs) |
| codex | `chatgpt-subscription` (default), `openai-api` | a per-session `CODEX_HOME` or a `model_providers` entry; needs measuring |
| opencode | its own providers, `af-engine-llm` | already carried in the model id; per-request, so the shared `serve` daemon is not a blocker |
| pi | `chatgpt-subscription`, `openai-api`, `bedrock`, `af-engine-llm` | pi's own provider setting (not built; #1585) |
| agy, kiro, cursor, copilot, muse | one each | no change |

The route list for each harness belongs in `guide/ref/agents.md` once rows are measured. This table is the plan,
not the reference.

### 5. Credential delivery — Decision A

How the credential reaches the session:

- **A1, per-session environment.** The route's key goes into the launch environment, the way opencode's provider
  keys already ride `LaunchPlan.Env` (`opencode/opencode.go:127-131`). It is simple, but the agent's shell can read
  the key, and for an AWS profile it breaks the rule that AF never hands AWS credentials to a session
  (`catalog.go:156-158`).
- **A2, an AF gateway.** The Agent (or the Control Plane) holds the credential and gives the session a short-lived,
  session-scoped token for an AF endpoint. Engine tokens already work this way (`engineSessionEnv`,
  `engines.go:669`). The cost is a proxy on the streaming hot path.
- **Recommended: both, by tier.** A1 is allowed for a member key the member pasted for their own sessions, which is
  the same exposure the opencode keys have today. A2 is required for any credential that can do more than call a
  model: an `AWSProfileRef` and a tenant key. The global environment never carries a route credential (the
  `authexpiry.go` trap).

### 6. The usage ledger records the route

`usagex.Record` gains `Route` and `Billing`. The price lookup tries the route first and keeps
`usageCatalogProviders` as the fallback for an empty route. A `subscription` row keeps its tokens and shows its
cost as "included", never as a measured $0. This lands first and on its own (P0), because it corrects today's
display without changing what runs.

### 7. Switching between subscription and metered — Decision B

The use case: a session hits its 5-hour window, and the member wants to keep going on the API.

- **B1, manual.** On a `LimitWindow` stop, the Console offers "continue on <metered route>" when the harness
  declares one and the member has its credential. Accepting relaunches the session with `--resume` on the other
  route. Never offered on `LimitSpend`, where moving to another bill is not what the member asked for.
- **B2, opt-in automatic.** As B1, but without asking, behind a per-member daily spend cap that cannot be empty.
- **B3, none.**
- **Recommended: B1 now; B2 only in a later ADR, after B1 has been used.** Rules that hold for B1:
  - the route is fixed at launch, so a switch is a relaunch, and it is recorded in the ledger;
  - the prompt cache does not carry over, so the first metered turn reads the whole context at full price, and the
    offer says so;
  - the assistant chat (`ClaudeChat`, `workspace/agent/internal/chatx/chat_providers.go:341`) stays on its
    default route unless it is given a route explicitly.

### 8. Bedrock and Vertex — Decision C

A Bedrock route runs on an `AWSProfileRef` (ADR 0069) and therefore on A2 (decision 5).

- **C1:** any profile the member has signed in to.
- **C2:** a profile the member marks as a model route, documented to carry only `bedrock:InvokeModel*`, and subject
  to ADR 0126's confirm-before-use when the profile asks for it.
- **Recommended: C2.** A route is used on every turn, so a broad profile there would be the widest standing grant in
  the Workspace.

Once a route supplies the credential, opencode's `amazon-bedrock` stopgap (`keepInMenu`) is replaced by the route's
availability instead of `AF_OPENCODE_SHOW_BEDROCK`.

### 9. pi joins as one harness

pi is the harness this axis pays off most for: one kind, with its routes declared through decision 4. Whether pi is
adopted at all stays with #1585 (Stage 0b); this record only makes sure that adopting it does not mean one kind per
provider.

### 10. `lcpp` is unchanged for now

`lcpp` stays a kind. The deployment's engine becomes the route `af-engine-llm`, which opencode already uses in
effect. Whether `lcpp`'s loop should later accept other routes (any OpenAI-compatible endpoint) is left for a later
record.

### 11. Every row is gated by a live turn

A harness × route row ships only after one live turn on it records: the route that actually answered (from the
transcript or the response headers), the usage the ledger wrote, and a negative control (the same launch with the
credential removed fails, and fails visibly). This is what keeps decision 4's candidate table from becoming a menu
of models that fail on the first message, which is exactly what `keepInMenu` exists to prevent.

## Phases

| Phase | Content | Gate |
|---|---|---|
| P0 | `Route` / `Billing` in the ledger; price lookup by route; terms in `guide/ref/glossary.md` | No behaviour change; the ledger tests cover an empty route |
| P1 | `RouteAdapter`; claude `anthropic-subscription` / `anthropic-api` through per-session environment (A1) | Decision 11 for both rows |
| P2 | The A2 gateway; claude `bedrock`; opencode Bedrock through the route instead of the stopgap | Decision 11; C2's profile check |
| P3 | pi as a kind, if #1585 adopts it | #1585's Stage 0b, then decision 11 per route |
| P4 | B1, the "continue on a metered route" offer | A live window stop, relaunched and resumed |

## Rejected options

- **One kind per harness × route** (`claude-bedrock`, `pi-openai`). The combinations multiply, and every one of them
  is another copy of every `kind` branch.
- **Splitting the kind string** (`claude/bedrock`). It changes the meaning of a field that the Control Plane,
  schedules, the MCP tools and stored sessions all read, for a gain the optional field gives without breaking
  anything.
- **A single global route setting per Workspace.** It cannot express "subscription for interactive work, API for a
  scheduled batch", and for claude it reproduces the environment-precedence trap.

## Open questions

1. The Console wording for "route" (an i18n term in `console/src/lib/i18n/locales/`, chosen in P1).
2. Whether a tenant administrator may allow or deny routes per tenant, the way `allow_engine_llm` gates the engine
   (ADR 0084).
3. How a Bedrock throttling error maps onto `LimitWindow` / `LimitSpend`, or whether it needs a third kind.
4. Whether a route may be changed outside the B1 offer (for example, on an explicit resume).

## Consequences

- A session gains one optional field; nothing changes for a session that leaves it empty.
- Cost reporting becomes truthful about subscription versus metered use as soon as P0 lands.
- Bedrock and Vertex become possible for claude without a new kind, and opencode's Bedrock models come back through
  the same mechanism.
- Each new route on each harness costs one adapter row and one live gate, not a new kind.

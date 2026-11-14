# 0136. pi as a session kind (`pi`): Managed only, one RPC child per session, a route on every session

English | [日本語](0136-pi-agent-kind.ja.md)

- Status: **proposed** (2026-11-14). The owner decided on 2026-11-12 to adopt pi as a kind (#1585). The
  decisions below are this record's proposals until the owner accepts them; the questions only the owner can
  answer are listed under Open questions and filed as `needs-decision` issues. Nothing is built. Every `file:line`
  was read on `7ab3e1141`.
  **What is measured and what is not.** Measured (pi 1.0.0, 2026-10-03, `docs/log/129`): the RPC operations,
  `agent_settled` and the abort ordering, af MCP over stdio, `get_session_stats`, an approval extension, and the
  token comparison with codex. Read from pi 1.5.2's documentation and package source (the current `latest`,
  fetched 2026-11-14, not run): provider variables, Bedrock's bearer and `baseUrl`, the agent directory,
  `auth.json` locking, session storage, the MCP file. Each statement of the second kind is marked *(docs)* where it
  carries a decision. Stage 0b (real-repository tasks) was not run; it is no longer a gate (owner, 2026-11-12) and
  nothing in this record depends on it.
- Follow-ups: (filled in when the issues are filed)
- See also: [0128](0128-harness-route-maker.md) (harness × route; pi is its P3) /
  [0129](0129-provided-model-routes.md) (provided Bedrock routes) /
  [0095](0095-muse-agent-kind.md) (the nearest precedent: a managed-only, per-session-child kind on a vendor
  protocol) / [0093](0093-lcpp-agent-kind.md) (managed-only) / [0019](0019-copilot-agent-kind.md) (a
  per-session child over stdio JSON-RPC) / [0015](0015-agent-managed-driver.md) (the managed driver contract) /
  [0124](0124-second-refactor.md) Phase 3 ("Phase 3 finishes before pi or rovo is built"; it has) /
  `docs/log/129-pi-agent-kind.md` (Stage 0)

## Context

pi (`@earendil-works/pi-coding-agent`, MIT) is a small coding agent: four built-in tools, a system prompt of about
1k tokens, extensions in TypeScript, and many providers. Stage 0 ran it against codex on the same ChatGPT model
(`gpt-5.5`, medium, 3 small tasks × 2 runs): both 6/6 correct; pi used about 1/16 of codex's total input tokens and,
priced at list price, about 1/3 of the cost (about 1/2.9 with the workspace guide appended to pi). Those are
measurements on toy tasks; they say nothing about quality on a real repository.

What makes pi a kind worth adding is less the price than the shape. `pi --mode rpc` is a JSONL protocol on
stdin/stdout that carries every operation a managed driver needs, measured in Stage 0 (G1–G4): `prompt`, `steer`,
`follow_up`, `abort`; `agent_end` per low-level run and `agent_settled` once pi will not continue on its own;
`get_session_stats` with tokens and `contextUsage`; and `extension_ui_request` / `extension_ui_response`, through
which an extension can ask the client to confirm a tool call. One process serves one session.

ADR 0128 already reserved pi a row (decision 5: `chatgpt-subscription`, `openai-api`, `bedrock`, `af-engine-llm`)
and a phase (P3). Since then the pieces a per-session route needs have been built for opencode: the route
gateway with a loopback listener and `/proc` attribution for harnesses that cannot speak a Unix socket (P2c), and a
private runtime per routed Managed session registered as the session's root
(`agents.RegisterRuntimeRoots`, `internal/agents/route_gateway.go:95`; #4004, PR #4006; its live gate is still
open). pi comes after that gate.

Facts this record has to respect:

- **The Claude subscription is not a route for pi.** Anthropic reserves OAuth sign-in for Claude Code and its own
  applications; third-party harnesses use API keys (#1585, comment of 2026-10-03). ADR 0128 decision 4 already keeps
  `anthropic-subscription` off every harness but claude.
- **The ChatGPT subscription is endorsed for pi**, in OpenAI's posts and its Codex for Open Source page, for an
  individual's own account; not for resale or a multi-user service (#1585, same comment). That is wording, not a
  contract clause.
- **In pi 1.x ChatGPT sign-in and the OpenAI API key are the same provider (`openai`)** (`docs/log/129` §1). A
  session meant to run on one of them can silently run on the other unless only one credential is reachable.
- **pi reads ambient cloud credentials** *(docs)*: Bedrock accepts `AWS_PROFILE`, static keys, ECS task credentials
  and IRSA as well as `AWS_BEARER_TOKEN_BEDROCK`. A pi process that inherits any of them can bypass the gateway.
- **pi's agent directory holds everything** *(docs)*: `PI_CODING_AGENT_DIR` (default `~/.pi/agent`) contains
  `settings.json`, `auth.json`, `models.json`, `mcp.json`, extensions, skills and the user `AGENTS.md`; sessions go
  under `sessions/` unless `--session-dir` says otherwise.
- **`auth.json` is locked** *(source)*: pi 1.5.2's `core/auth-storage.js` takes a `proper-lockfile` lock on the
  resolved path, "so symlinked aliases of auth.json share one lock". A token refresh by one process is therefore
  serialised with another process's read through the same file.
- **Outbound by default** (`docs/log/129` §1): install telemetry and provider attribution headers are on
  (`enableInstallTelemetry`, `PI_TELEMETRY`), a version check calls `pi.dev`, and `/share` publishes a session.
- **Project trust** (`docs/log/129` §1): in RPC mode, with the default `defaultProjectTrust: "ask"`, pi silently
  skips trust-gated project resources (`.pi/settings.json`, `.pi/mcp.json`, project extensions); `AGENTS.md` and
  `CLAUDE.md` are read regardless.
- **The kind registry is ready.** ADR 0124 Phase 3 closed (#2872, #2927): adding a stub kind was measured to touch
  16 files outside its package (ADR 0124's 2026-10-29 addendum), and `TestKindLiteralNewKindCounted` (`kind_literal_ceiling_test.go:234`) already simulates `KindPi`.
  The checklist is `docs/build/20-add-an-agent.md` §20.7.

## Decision

### 1. The kind is `pi`

`session.KindPi = "pi"`, label `pi`. The launch suffix, short label, colour and CSS class follow §20.7 and are chosen
when the kind is registered (the colour is measured against the existing kinds, as ADR 0095 decision 1 did).
`pi` is not a maker: `model_provider.go` resolves the maker from the model id (`openai/…`, `amazon-bedrock/…`), never
from the kind.

### 2. Managed only

Options:

- **(a) Managed only** (`Caps().ManagedOnly`), the shape `lcpp` and `muse` have.
- **(b) Both routes**: Managed plus a tmux pane running pi's TUI.
- **(c) Terminal only.**

**Taken: (a).** The RPC protocol carries every operation the driver needs (Stage 0 G1–G4), so a pane would buy a
second UI and a screen contract nobody has measured (`docs/log/129` §5 lists the TUI as unmeasured): pane mode,
modals, the prompt blocker, interrupt keys, progress signals (§20.7's Terminal rows). A pane also makes the route
harder to hold: the member can type `/login` or `/model` into it and move the session to another credential, which
a managed child never offers. (c) throws away the protocol that made pi worth adding.

The managed-only gate is shared and already paid for (ADR 0095 decision 2: create defaults to `managed`, a `tui`
target is refused, the Console derives `terminalDriver: false`). Reopened when members ask for the TUI **and** a
TUI contract is measured, including how a pane keeps its route.

### 3. One `pi --mode rpc` child per session

`Capabilities.ProcessModel = "per-session-child"`. pi has one active session per process (`switch_session` replaces
it), so a shared daemon would be a pool of children anyway; and a route is held per process (decision 6).

- The child is started the way #4004's private `opencode serve` is (`agents/opencode/route_serve.go`): on a pinned
  OS thread with `Pdeathsig`, so it dies with the Agent, whose gateway alone held its route token; reaped on every
  path, the failure path included (§20.3); registered through `agents.RegisterRuntimeRoots`, so the gateway's
  caller check treats it, and the tools it spawns, as the session's tree.
- `Resume` starts it, `DropHandle` and `Shutdown` stop it (close stdin and send SIGTERM, which start an orderly
  shutdown; a second signal exits at once *(docs)*), and reconcile replaces one that died. The handle scaffold is
  the shared `agents/managed` `Lifecycle[H]`.
- Launch flags: `--mode rpc`, `--session-dir <shared>` and `--session <file>` on resume (decision 5), `--no-approve`
  (decision 7), `-e <AF's approval extension>` (decision 9), and the route's `--model` (decision 6). Nothing secret
  is on the command line: `--api-key` is never used, since it would land in `/proc/*/cmdline`.

The footprint is not measured (a Node process per session). P0 measures it idle and during a turn; if it is high
enough to matter, a pool is reconsidered then, not now.

### 4. Driving the protocol

The client follows the ACP client's shape (`agents/acp`): id-correlated responses on one side, events on the other.
**A pending response never blocks the event reader**, because `abort` answers only after `agent_settled`
(measured, G1): a client that waits for the response before it reads events deadlocks.

| AF operation | pi |
|---|---|
| `Send` | `prompt`. A response whose `data.disposition` is `handled` started no run: the turn ends at once, without waiting for `agent_settled` *(docs)* |
| `Steer` (turn running) | `steer`: delivered before the next model call (measured) |
| a message sent while a turn runs, not as a steer | held in AF's queue and sent as a new `prompt` after `agent_settled`. pi's `follow_up` is not used: it folds the follow-up into the same run (one `agent_end`, measured), so two AF turns would become one turn in the status store and the ledger |
| turn end | **`agent_settled` only.** `agent_end` can be followed by retries, compaction or steering *(docs)* |
| `Interrupt` | `abort`; the turn ends at `agent_settled` and is marked cancelled; the response that follows is a formality. No `agent_settled` within a bound → kill the child and reconcile from the session file |
| `UpdateSettings` | `set_model` (within the route, decision 6), `set_thinking_level` |
| snapshot | `get_state` and `get_messages` |
| `Respond` | `extension_ui_response` (decision 9) |

`DynamicModel` and `DynamicEffort` are declared; `DynamicMode` is not (pi has no plan mode on the wire; reading
something else as one is how a capability table starts lying, ADR 0095 decision 13). `Fork` is pi's `fork` /
`clone` and is left for P3 (decision 13).

### 5. The conversation id is captured, and the transcript is pi's session file

pi mints the session id and file; `get_state` returns `sessionId` and `sessionFile` *(docs)*. The driver stores
both on `session.Meta` when the session is created (captured, not imposed: §20.1's preferred kind, so no
recovery path is needed), and resumes with `--session <file>`. Sessions are written under one AF-chosen
`--session-dir` per Workspace, not under the per-session agent directory of decision 6, so they survive that
directory being rebuilt.

The canonical read source is the session JSONL (format documented in pi's `session-format.md`) at rest, and the
RPC events while the child runs; AF does not copy the conversation (ADR 0015 decision 3). Recreate and fork build a
fresh `Meta` from a field whitelist (ADR 0095 decision 4), so a stored file is not carried by accident.

### 6. Every pi session has a route, held by a per-session agent directory and environment

pi implements `agents.RouteAdapter` (`internal/agents/route.go:21`) and `RouteModelOwner` (`:145`).

**An empty selection is not a route rule of its own.** ADR 0128 decision 2 keeps "empty means today's rules" for
existing kinds; pi has no today. At create, an empty selection resolves to `chatgpt-subscription` when the member
is signed in, and is otherwise refused (`route_required`); the resolved selection is stored, so every pi session
carries an explicit one from then on. A metered route is only ever taken when it is named.

**Isolation.** Each session gets its own agent directory, written by AF before the child starts:
`PI_CODING_AGENT_DIR=<agent state>/pi/sessions/<session>/agent`, holding `settings.json` (decision 7),
`mcp.json` (decision 10), `models.json` only where the route needs one, the instructions (decision 11), and
`auth.json` **only for the route that uses a stored login**. The child's environment is the Agent's minus every
provider credential and cloud-credential variable pi reads at the pinned version (its documented provider table,
`ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_AUTH_TOKEN`, the federation variables, `AWS_PROFILE`, the static AWS keys,
`AWS_CONTAINER_CREDENTIALS_*`, `AWS_WEB_IDENTITY_TOKEN_FILE`, the Vertex variables), plus the route's own. The
strip list is generated from the pinned version's documentation and checked by the contract workflow at every
pin bump, because a provider added upstream is a new way around the route.

| Route | Credential and tier (ADR 0128 decision 7) | How it reaches the child | Phase |
|---|---|---|---|
| `chatgpt-subscription` | The member's own pi sign-in to ChatGPT (existing connection). One login file per Workspace, written by AF's connection flow (decision 12), hidden from the file browser | The session's `auth.json` is a symlink to the login file; pi's lock on the resolved path serialises refreshes *(source)*. No `OPENAI_API_KEY` in the environment | P1 |
| `openai-api` | The member's OpenAI API key (member key, A1) | `OPENAI_API_KEY` in the child's environment only. No `auth.json`, so the subscription login is not reachable through pi's own resolution | P1 |
| `bedrock`, `bedrock-tenant`, `bedrock-deployment` | ADR 0128 C2 profile / ADR 0129 provided routes (A2) | The gateway's loopback listener (`IssueLoopback`, `route_gateway.go:31`); pi is a Node client with no documented Unix-socket transport. `AWS_BEARER_TOKEN_BEDROCK` = the route token, `AWS_REGION`, and `models.json` setting the `amazon-bedrock` provider's `baseUrl` to the gateway *(docs: bearer and `baseUrl` are supported; the wire is `bedrock-converse-stream`, which the gateway already forwards)* | P2 |
| `anthropic-api` | The member's Anthropic API key (member key, A1). Allowed by Anthropic's terms for third-party harnesses; not in ADR 0128's pi row, added here as a candidate | `ANTHROPIC_API_KEY` only | P3 |
| `af-engine-llm` | The deployment's engine | Not until the engine token is session-bound: today it lives 30 days and is workspace-scoped (ADR 0128 Context). Through the gateway or a session-bound token, whichever ADR 0128's later work gives | P3 |
| `anthropic-subscription` | — | **Never** (Context) | — |
| pi's other providers (GitHub Copilot, OpenCode Zen/Go, OpenRouter, …) | — | Not offered; each would need its own terms check and row | — |

**The model belongs to the route.** `RouteModelOwner` checks `<provider>/<id>`: `openai/…` for the two OpenAI
routes, `amazon-bedrock/…` for Bedrock, `anthropic/…` for `anthropic-api`. `set_model` across providers is refused
(ADR 0128 decision 6). The model menu is the route's: `get_available_models` from a short-lived child on the
route's agent directory, cached, stale-if-error.

**What isolation does not claim.** Every session runs as the same user. An agent in a session on `openai-api` that
sets `PI_CODING_AGENT_DIR` to another directory, or reads the login file, reaches the subscription; `pi auth
print-bearer-token` prints it in one command. The subscription login has the exposure codex's `auth.json` has
today; the per-session directory guarantees that **pi itself** runs on the selected route, which is what billing
and the ledger need, not that a hostile agent cannot reach the member's other credentials. Only the A2 routes
(Bedrock) carry the gateway's caller binding.

### 7. AF owns the agent directory's settings, and outbound reporting is off

`settings.json` in each session's agent directory is written by AF, and these are clamped:

- `enableInstallTelemetry: false`, and `PI_TELEMETRY=0` in the environment (telemetry and provider attribution
  headers off), `PI_SKIP_VERSION_CHECK=1`. pi is never updated by itself: no `pi update`, and the pin is AF's
  (decision 14).
- `defaultProjectTrust: "never"`, and `--no-approve` on the command line. Project-local pi resources (`.pi/`
  settings, extensions, `mcp.json`, `models.json`) are not loaded, because a project's `models.json` could redirect
  a provider's `baseUrl` and a project extension is arbitrary code that could replace AF's approval extension or
  the route. `AGENTS.md` / `CLAUDE.md` in the working copy are still read, as pi does regardless of trust. Whether
  members may opt a repository in is an open question (2).
- `/share` and `/bug` publish a session. A prompt that starts with either is refused by the driver with a visible
  reason; P0 measures whether pi's built-in slash commands are reachable through RPC `prompt` at all.
- The member's own `~/.pi/agent` (if they installed pi themselves) is never read by a session and never written by
  AF.

### 8. Usage and context

- **Per call, from the session file.** Each assistant message in pi's session JSONL carries that call's usage
  (Stage 0 read the first call's from it). `UsageReader` reads those rows; `get_session_stats` after each
  `agent_settled` is the cross-check, not the source. `UsageMeasured` is declared `exact` only after P1's gate shows
  the two agree on a multi-call turn; until then it is `partial`.
- **Route and billing per call** (ADR 0128 decision 8): the route is the session's stored selection (it cannot
  change while the session exists, as for claude's P1b). Billing: `openai-api` and `anthropic-api` are `metered`;
  `chatgpt-subscription` stays unresolved, since nothing in a call's record says whether a plan window or credits
  paid for it; Bedrock calls are counted by the gateway's rows and the fold drops pi's own (`dropGatewayCounted`).
  pi's own `cost` field is not used for pricing; AF's route-first lookup is.
- **Context gauge.** Live: `contextUsage{tokens, contextWindow, percent}` from `get_session_stats` (measured, G3;
  absent when no model is selected). At rest: the last call's usage and the model's window. This is a live overlay
  in the shape of muse's (`sessionx/session_usage.go`), until the registry gives overlays a hook.
- A ChatGPT window limit is a `UsageLimiter` only once its error is measured; until then a limit stops the turn
  with pi's message and no auto-resume is booked.

### 9. Approvals through an extension AF ships

AF ships a small extension (TypeScript, no build step; Stage 0 G4 used 8 lines) loaded with `-e` on every launch.
On each tool call it asks `ctx.ui.confirm`, which reaches the driver as `extension_ui_request{method:"confirm"}`;
the driver raises an `Interaction` of kind `approval` and answers with `extension_ui_response` (measured: a denial
blocks the call and the model sees it, an approval runs it). When the session skips permissions, the extension
allows without asking.

`Driver.Capabilities().Permissions` is true when this is built. `Caps().PermissionChoice` is set only after the
whole round trip has been driven from the Console on the real CLI (§20.4). Other `extension_ui_request` methods
come only from AF's own extension (decision 7 keeps every other out); an unknown blocking request is answered
cancel and logged, never left pending. `Questions` stays false: pi has no built-in question tool.

### 10. af MCP over stdio, with no token in the file

The session's `mcp.json` lists the af server as stdio, `workspace-agent mcp-stdio --self-report …`
(`mcpreg/builtin.go`), with `exposure: "direct"`. The child inherits `AF_SESSION_NAME`, `AGENT_TOKEN` and
`AGENT_ADDR`, so no token is written (measured on 1.0.0, G2; re-measured on the pin in P0). pi joins `writerTable`
(`internal/mcpreg/materialize.go:98`) with a writer for that file; member and tenant HTTP servers write their
headers as `${VAR}` references, which pi expands *(docs)*. `exposure: "direct"` for every server AF writes:
pi's default `codemode` runs scripts in workers of up to 256 MB each *(docs)*, a cost the shared host does not need
for a handful of tools. `Caps().MCPClient = "yes"` after P1's gate.

### 11. Instructions and skills

The fleet's instructions go to `AGENTS.md` in the session's agent directory, which pi reads as user-level
context *(docs)*; the skills to its `skills/`. pi is listed in `instrSupportedKinds` and `SkillsLayouter`. Stage
0's pi+guide control already measured the cost of appending the workspace guide (about 3.6k tokens on the first
request).

### 12. Signing in

A pi connection card in Settings: **ChatGPT** through `pi auth login openai --no-browser` (device code) driven by
the Agent, the URL paste-back as the fallback (pi's callback is fixed to `127.0.0.1:1455`, `docs/log/129` §1); and
the API keys of decision 6 as member keys. The connection status is `pi auth check --provider openai --json` on the
login file. Login and status routes are relayed by the Control Plane like codex's (`restLogin`). The login file and
every session agent directory are added to the file browser's denylist. Whether the ChatGPT route is offered on a
multi-member deployment is an open question (1); until it is answered, the route is offered only where an operator
turns on a deployment switch for it (named in P1), the way ADR 0128's Bedrock routes started behind
`AF_OPENCODE_BEDROCK_ROUTE`.

### 13. What the kind declares

| Declaration | Value | When |
|---|---|---|
| `Caps()` | `ManagedOnly`, `CanTranscript`; `MCPClient: yes` | P1, after its gate |
| | `PermissionChoice` | P1, after the Console round trip |
| | `CanFork`, `CanForkAt` | P3 (`fork` / `clone`, the new file captured as in decision 5) |
| `Capabilities()` | `per-session-child`, `Steer`, `DynamicModel`, `DynamicEffort`, `Permissions`; not `DynamicMode`, `Questions`, `Fork` (until P3), `EventReplay`, `EphemeralThread`, `TUIAttach` | P1 |
| Optional interfaces | `ManagedLifecycler`, `ConnectionStatuser`, `ModelLister`, `SkillsLayouter`, `Instructions`, `UsageReader`, `UsageMeasurer`, `ContextReporter`, `MoveTarget`, `RouteAdapter`, `RouteModelOwner`, `RouteChecker` | P1 |
| | `UsageLimiter`, `AuthRecoverer` | when the error each depends on is measured |
| | `NoHeadlessChat` (no assistant-chat backend in v1) | P1 |
| Registry and snapshots | `Kind*`, `kindTable`, `agentreg`, `managedDrivers`, `kindTable.ts`, the kind and route catalogue snapshots, the MCP tool goldens, `guide/ref/agents.md` | P1 |

The kind adds no kind-literal branch outside `internal/agents` (`kindLiteralCeiling` stays where it is); behaviour
goes behind the interfaces above.

### 14. Install and pin

pi is MIT, so unlike the proprietary CLIs it may be in the distributed image. It is installed with
`npm install --ignore-scripts` at an exact version into its own prefix, and launched by absolute path (Stage 0 found
an nvm-only install missing from a Terminal session's `PATH`). The pin is an ARG in `workspace/Dockerfile`, a
`versions.json` entry, a row in the CLI manifest (or the hand-kept tables, if #4135 has not merged), and a
`pi-contract.yml` that runs the live contract on a pin bump. pi needs Node ≥ 22.19 *(package.json)*; the image's
`node:22` base is checked at the pin.

## Phases

| Phase | Content | Gate |
|---|---|---|
| P0 | Probe on the pinned version, no product code, written up as a journal: idle and in-turn RSS of one child; the protocol of decision 4 on that version (abort ordering, `disposition`); `auth.json` symlink and lock under two children refreshing; which credential `openai` uses when both a login and `OPENAI_API_KEY` exist; the strip list against a child that tries each ambient source; Bedrock `baseUrl` + bearer against a stand-in gateway on loopback (path, `Authorization`, the calling process); `/share` through RPC; MCP env inheritance; the instructions path; per-call usage against `get_session_stats` | Every item answered or the decision it feeds amended by a dated note |
| P1 | The kind, the driver, the transcript, `chatgpt-subscription` and `openai-api`, the connection card, approvals, af MCP, usage, instructions, install and pin, guide rows | ADR 0128 decision 14 for both routes (the route that answered; the billing the ledger wrote; the negative control: the same launch without the credential fails visibly, and a session on `openai-api` with a signed-in login still bills the key); a turn interrupted mid-tool leaves no child process; an approval answered from the Console; the af MCP tool called by the model |
| P2 | `bedrock`, `bedrock-tenant`, `bedrock-deployment` through the gateway | Decision 14 per route; the gateway's caller check accepts the child and its tools and refuses another session's process; an ambient AWS credential planted in the Agent's environment is not used |
| P3 | `anthropic-api`; `af-engine-llm` once a session-bound credential exists; fork; whatever P1's measurements deferred (`UsageLimiter`, `AuthRecoverer`) | Decision 14 per route; a fork resumes with the parent's context |

Every phase's live gate needs a real account and is marked `needs-live-run`; unit tests against a stand-in pi do not
close one.

## Rejected options

- **A pane with pi's TUI as the only route, or as a second one in v1.** Decision 2.
- **One shared agent directory and environment flags per session.** The ChatGPT login and an API key sit on one
  provider, so which one a session used would depend on pi's internal precedence, which can change in any release;
  and a project's or member's `models.json` would apply to every session.
- **A copy of the ChatGPT login per session.** Two copies refresh independently, and where the provider rotates
  refresh tokens, the copy that refreshes first invalidates the others. One file, symlinked and locked, keeps one
  writer at a time.
- **The ChatGPT subscription through the route gateway (A2).** It would bind the token to the session, but it
  means proxying a private ChatGPT backend endpoint on the hot path for a credential the member already holds in
  this Workspace, as codex does. Reconsidered if a provided (tenant) subscription ever exists, which open question 1
  would have to allow first.
- **Using pi's `follow_up` for queued messages.** Decision 4.
- **Loading project `.pi/` resources by default.** Decision 7.

## Open questions

1. **May the ChatGPT subscription route be offered on a multi-member deployment?** OpenAI's endorsement covers an
   individual's own account, not a multi-user service. Options: (a) everywhere, each member signing in with their
   own account (as codex's sign-in already works); (b) only behind a deployment switch an operator turns on;
   (c) only on single-member deployments. Owner's decision.
2. **May a member opt a repository into pi's project resources (`.pi/`)?** Options: (a) never (decision 7's
   default); (b) per repository, with the route-bearing files (`models.json`, provider settings) still ignored;
   (c) per repository, everything. Owner's decision.

## Consequences

- An eighth Managed driver, whose fixed prompt cost measured about 1.2k tokens (Stage 0), on routes the member
  chooses per session, with the route recorded per call.
- pi is the first harness that declares `chatgpt-subscription` and `openai-api` in the route catalogue.
- pi sessions do not see the member's own pi setup or a repository's `.pi/` resources; a member who relies on pi
  extensions or packages does not get them in AF.
- Each pin bump re-checks the strip list and the protocol; pi's release pace is the drift risk, as copilot's
  weekly releases are.

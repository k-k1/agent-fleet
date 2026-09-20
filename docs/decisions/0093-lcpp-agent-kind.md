# 0093. A harness of our own on the self-hosted llama.cpp engine as a session kind (`lcpp`) — the first kind without a CLI, staged behind a kind-independent core

English | [日本語](0093-lcpp-agent-kind.ja.md)

- Status: **proposed** (2026-09-19). Nothing is implemented. Every `file:line` below was read on
  `951bb402` (develop at the time); the inventory behind it, table by table, is
  `docs/log/99-lcpp-agent-kind.md`, which stays the working record while this ADR carries the
  decisions and the options rejected.
  🟢 **Reviewed 2026-09-19, before Phase 0** (the Review section at the end, by a separate session,
  every anchor re-read against the tree). Two premises of the first draft were overturned and are
  corrected in place below, each marked "the first draft said …": `dispatchMCPStdio` is not a pure
  switch (Context, Decision 6), and `ProcessModel` reaches no Console consumer (Decision 4). The
  terminal-route gate turned out to be five sites, not three (Decision 2). Open questions 3–5 are
  closed there; 1 and 2 stay hardware-only.
  🟢 **Plan approved by the user 2026-09-19** (the staging of Decision 9, Phase 0 first). Implementation
  runs in child sessions driven and reviewed by a parent session; each phase lands through its own
  PR. Status moves to *adopted* when Phase 2 is merged, or the ADR records where it stopped.
- The request is one sentence: **can our own harness — a process that talks to llama-server's API
  directly instead of driving a vendor CLI — be a session kind of Agent Fleet, and at what cost?**
- See also: [0015](0015-agent-managed-driver.md) (the managed driver contract this kind implements
  without a child process) / [0026](0026-kiro-agent-kind.md) (kiro — the most recent kind, the
  template for "what a kind touches") / [0071](0071-self-hosted-inference-engines.md) (the engine,
  its cold start, the gateway) / [0072](0072-engine-model-catalog.md) (the catalogue whose
  `context_tokens` is the fourth hop of the window's one-way trip) / [0079](0079-remote-engine-from-another-deployment.md)
  (borrowed engines, whose `engine_waking` this kind must not mistake for success) /
  [0084](0084-engine-indicator-and-tenant-gate.md) (the tenant gate the kind's availability rides on)

## Context

### Why this came up

The context-window accounting for the self-hosted llm engine has been fixed repeatedly and never
landed. The cause is structural, not a bug in any one fix: the window travels **one way over four
hops** — `store.EngineModel.ContextTokens` → the active set's `c` → the sidecar's preset →
llama-server's `--ctx-size` — and nothing comes back. The description is in our own code,
`workspace/agent/internal/agents/opencode/window.go:27-33`. Fill is guessed by
`usagex.WindowGuess` (`workspace/agent/internal/usagex/usage.go:75-89`), which reads a self-hosted
model id as an unknown non-Claude model and answers 200,000.

The reference that works is NousResearch/hermes-agent (Python, MIT): it owns llama-server as a
child process and talks to the OpenAI-compatible API directly. What makes it work is not a formula
but one line, `agent/conversation_loop.py:419-437` — right before compacting, holding the token
count it is about to send, it goes and widens the server's window. One process knows both the
server's window and the conversation's length, so it can write that line. **Its growth ladder is
not portable here**: their bounce is a local process restart in seconds; ours is an ECS task
replacement in minutes, on Tokyo GPUs that run dry even on-demand. We decide the window **once,
with headroom**, and never grow it.

### What llama-server offers (confirmed in its README)

`POST /v1/chat/completions/input_tokens` (the exact count before sending), `GET /props`
(`default_generation_settings.n_ctx` — the window that actually attached), `POST /v1/messages` and
`/v1/messages/count_tokens` (Anthropic-compatible), `POST /slots/{id}?action=save|restore`,
`POST /v1/chat/completions/control {reasoning_end}`, `--reasoning-budget N` /
`--no-reasoning-preserve` (preserve is the default — thinking accumulates in the history), and
`-fit` (default on; **it only moves arguments that were not set**: an explicit `-c` leaves the
window alone and spills weights to the CPU instead).

**llama-server's built-in MCP client (`--mcp-servers-config`) and `--tools` / `--agent` are
unusable here.** They run inside the GPU container; our tools must run in the Workspace with the
session's own credentials. Different box, so delegation is impossible in principle, and `/tools`
is documented as "Please do NOT use this endpoint in a downstream application". **The MCP client
is ours to write.**

### What the repository already has, measured

- The Go contract a kind implements: the read layer `Agent` (six methods,
  `workspace/agent/internal/agents/agents.go:141-161`) and the managed layer `Driver` /
  `ThreadHandle` (seven methods, `driver.go:132-165`). `Capabilities.ProcessModel` is a three-value
  enum: `shared-daemon` / `per-session-child` / `tui` (`driver.go:146`).
- Kinds are registered in `session.go:19-29` and `sessionx/agent.go:28-38`; **an unregistered kind
  is silently normalised to `claude`** (`agent.go:49`), not rejected. Managed drivers are the map
  at `sessionx/session_turn.go:31-37`. No kind today is managed-only: the create path only knows
  the refusal "this kind has no managed driver" (`session_handlers.go:649-668`), never the reverse.
- The engine gateway (`control-plane/engine_gateway.go`) has **no path allow-list**; `serve()`
  (`:467`) never looks at the path. What limits it is `/v1/` applied twice: the mux pattern
  `/engine/{key}/v1/{path...}` (`:211`) and `engineUpstreamPrefix` prepending `/v1/` again
  (`:1172`). So `/v1/chat/completions`, `/v1/models`, `/v1/messages` (named at `:1086`),
  `/v1/messages/count_tokens`, `/v1/chat/completions/input_tokens` and `/v1/chat/completions/control`
  pass; **`/props`, `/slots`, `/tokenize` do not** — they live at llama-server's root and `/v1/props`
  is a 404. Streaming responses get `stream_options.include_usage` injected (`:597`), and every
  request is the demand signal that buys the box (`:540`).
- The `af` MCP server is `dispatchMCPStdio(line []byte) []byte`
  (`workspace/agent/internal/mcpx/mcp_stdio.go:243`). **The first draft called it a pure switch over
  one JSON-RPC line and was wrong**: besides the process-global permission set (`parseStdioFlags`,
  `:131`) it reads the owning session, conversation, Chromium, peer, image and spawn state, writes
  progress and list-change notifications asynchronously through the process-wide stdout writer, and
  `tools/list` starts a once-only process-wide watcher (`:79-210`, `:479-544`). It is callable
  in-process only behind a request-scoped dispatch context that also owns notification output and
  the watcher's lifetime.
- There is no MCP *client* in Go. `mcpreg/probe.go` speaks `initialize` → `initialized` →
  `tools/list` over stdio and Streamable HTTP (`:332-344`, `:483-498`) and stops there; the only
  `tools/call` caller is an e2e test.
- Existing kinds weigh 2,100–5,950 non-test source lines each (agy 2,109 … codex 5,948).

## Decision

### Decision 1 — the kind is `lcpp`; `native`, `llama`, `llm`, `llamacpp`, `engine` and `rovo` are not available

`native` is the desktop Runtime adapter (`AF_RUNTIME=native`, docs/log/34 and 42) — a different
axis, but one that would share every log line and document with a kind of the same name. `llama` is
already a model-family key resolving to Meta (`workspace/agent/model_provider.go:68`), and a kind
that runs Qwen under the name `llama` lies to the member. `llm` is the engine key and `llamacpp` the
engine's provider id (`control-plane/engines.go:50`, `:81`); opencode's model ids are
`llamacpp/<model>`, so kind and provider would spell the same. `engine` is the box. `operator` is an
origin (`session.go:45-53`), `af` a reserved MCP server name (`mcpreg/def.go:55`), `rovo` the
reserved ninth kind of docs/log/74.

`lcpp` is the customary abbreviation of llama.cpp and says what the kind is: a harness that depends
on llama-server's API (`/props`, `/slots`, reasoning control), not on the model family.
`session.KindLcpp`, `.kind-lcpp`, `short: "lc"`, `launchSuffix: "-lc"` (free among
`""/-cx/-cu/-ag/-cp/-ki/-oc/-sh`). Label `llama.cpp`. No brand icon — the harness is ours — so a
codicon like shell/ssm; the tenth colour is fixed by rendering both themes before the build, as
docs/log/74 §8.2 did for the third blue.

### Decision 2 — `lcpp` is the first managed-only kind; there is no Terminal (CLI) route

There is nothing to put in a tmux pane. `BuildLaunch` returns an error, `POST /sessions` defaults
`driver` to `managed` for this kind, `POST /sessions/{name}/driver` refuses `tui` with 400, and the
Console gains one descriptor flag saying "no terminal route". **The first draft named three readers;
the Review found five** (`LaunchModal.tsx:776-803`, `StartModal.tsx:303-316`, quick launch in
`RepoRowConnected.tsx:178-184`, the driver-switch menu and action in `SessionMenu.tsx:179-190` /
`useSessionActions.tsx:262-287`) plus the server-side managed→TUI transition
(`session_driver.go:62-105`); the handoff modal needs no route label because it uses the generic
create path, which only has to select managed for this kind. A paneless session is not new —
managed sessions already are; what is new is that this one can never go back to a pane.

A REPL (`workspace-agent lcpp-tui`) would give a Terminal route. It would be a second UI for a kind
whose mirror already shows everything, so it is not built.

### Decision 3 — the harness writes the transcript; one record yields two representations

Every other kind reads a store the CLI writes. `lcpp` writes its own: `AgentDataDir()/lcpp/sessions/<sid>.jsonl`,
append-only, one record per user turn / assistant turn (text, reasoning, tool_calls) / tool result /
system note (compaction, model change) / usage. From one record the harness derives **the OpenAI
`messages` array it sends next turn** (the canonical history) and **`transcript.Turn` for the
mirror** (the only normalisation target — anything else loses sharing, marks and changed-files at
once). Reader and writer are the same code, so mirror parity is total by construction.

Append-only is kept by the writer: compaction **adds** a system note plus a summary and never deletes
earlier lines; the outgoing `messages` is built from the last compaction note onward. Past turns'
reasoning is dropped from the outgoing array (llama-server's `reasoning-preserve` default would
otherwise stack it) and kept in the file. Fork and fork-at cut this file at an anchor into a new
sid, the way claude does; there is no "this route cannot fork" case because there is one route.

### Decision 4 — the managed driver runs in-process: a fourth `ProcessModel`, no supervisor

`Resume(m)` returns a handle (goroutine and channels), not a child process. Nothing of the opencode
Supervisor skeleton (ensure / adopt / generation / drain) applies; what remains is the turn
goroutine's lifetime, settling a turn the Agent was holding when it restarted (`TurnUnknown` →
`Snapshot` reads the JSONL tail: closed on an assistant record = completed, open on a tool_call =
aborted), and a context cancel in `shutdown.go`. `Capabilities.ProcessModel` gains `"in-process"`;
`tuiMemoryCost` is empty. **The first draft assumed the fourth value could break a Console consumer;
the Review found none**: `ProcessModel` never crosses the Agent API and is only populated by the
managed drivers (`driver.go:142-157`). Adding the value breaks nothing, and equally drives no UI —
the terminal-route gate of Decision 2 is a descriptor flag, not this enum.

The seven handle methods: **Send** runs one loop; **Steer** queues a user message for the next tool
boundary (shown as `Queued`); **Interrupt** cancels the context, optionally after `POST
/v1/chat/completions/control {reasoning_end}` so a thinking model closes cleanly; **UpdateSettings**
applies model / effort (`reasoning_budget`) / mode (plan = write tools removed) from the next turn,
so `DynamicModel`, `DynamicEffort` and `DynamicMode` are all true (the llama-server router holds
several models under `--models-max`); **Respond** is the return value of the `ask_user` tool;
**Events** and **Snapshot** are plain.

State is not detected, it is emitted: the harness writes `status.Persist(sid, working|idle|question)`
itself and rides the generic `DriveState` path claude uses. One state is missing from the
vocabulary — **the box waking**, minutes long on the first turn. v1 shows it as `working` with a
"waiting for the engine (n s)" last-say line; a `waking` state changes Console vocabulary and is
deferred. The client must not read a streamed 200 as success before the first token arrives
(ADR 0079's flattened `engine_waking`).

### Decision 5 — the tools are ours, so approvals are real

Every existing kind's tool loop, built-in tools (read / write / edit / bash / glob / grep / ls),
permission model, output caps, cwd confinement, question and plan tools, system-prompt assembly and
compaction live **inside the CLI** and appear in no kind contract. For `lcpp` all of it is ours.
The one thing this buys that no kind has: **the executor is us, so an approval genuinely stops the
tool** — `Permissions: true` and `Caps.PermissionChoice: true` can be declared as measured
(docs/log/76's rule) rather than claimed. The instructions layer (fleet notes, user instructions,
the project's `AGENTS.md` / `CLAUDE.md`) is read by the harness into the system prompt in the
existing order fleet → user → project → rtk; `instrSupportedKinds` does not list `lcpp` because there
is no file to write. rtk is a wrapper in front of the bash tool's exec — no hook, no plugin, no
file. Skills are foreign entries only (SKILL.md read by injection, docs/log/50 §8).

Tool-call formatting depends on llama-server's chat template and differs by model family (the
JSON breaks differently under Qwen, GLM, Llama). **The kind supports one or two families**, named
in the guide, not "whatever the catalogue holds".

### Decision 6 — `af` tools in-process; external MCP through a client of our own; a kind that is known but never materialised

The 72 `af` tools do not go through MCP at all: the harness calls `dispatchMCPStdio` directly.
**The first draft said this needs only the process-global flags turned into a per-call option
struct, and was wrong** (Review): the dispatcher also depends on per-process session / conversation
/ Chromium / peer / image / spawn state, on the stdout writer for asynchronous notifications and on
a once-only watcher (`mcp_stdio.go:79-210`, `:479-544`). The in-process call is valid only after a
request-scoped dispatch context owns those too; until then, and as the fallback if that refactor is
refused, `af` tools go over loopback HTTP like every other CLI's do. Tool names and descriptions
stay as they are either way, so the per-session description cost is unchanged.

External servers (tenant-distributed, project, member-registered) get a real client: stdio and
Streamable HTTP, both the 2026-07-28 stateless convention and the 2025-06-18 `initialize` one
(symmetric with what our own server accepts), `tools/call`, `notifications/tools/list_changed`,
per-server lifetime (a stdio child dies with the session) and the per-kind timeout. `mcpreg/probe.go`
is the seed.

The registry is read in memory. `lcpp` joins `mcpreg.knownKinds` (so a member can enable a server
for it) but not `MaterializedKinds` (there is no file to write) — the first kind in that state.
Its project-scope spelling does not exist; it reads other kinds' `.mcp.json` through `mcpproj` and is
not a copy target.

### Decision 7 — the window: one read-only pass-through on the CP, the exact count before every send, one-shot compaction

- **Knowing the window.** `GET /props` is the truth and cannot pass today's gateway. Add
  `GET /engine/{key}/props` to the CP: read-only, same session token, no `/v1/` prefix, about a
  hundred lines. This is **the return trip the four hops never had**, and it is not kind-specific:
  it serves P0 and P1 as well, and lets `syncEngineProviders` (`workspace/agent/engines.go:287`)
  overwrite `limit.context` with the real `n_ctx` while the box is awake, which fixes the opencode
  route too.
- **Knowing the fill.** `POST /v1/chat/completions/input_tokens` with the exact `messages` about to
  go: the count includes the chat template. It passes the gateway's model check and, being demand,
  wakes a sleeping box.
- **Deciding.** `(input_tokens + reserved output) > window × threshold` → one summary turn. The window
  is `/props` when reachable, else the catalogue's `context_tokens`. With an exact input count, an
  error in the window shifts compaction a little early or late; it cannot reproduce "25k shown as
  13% full while compacting every turn".
- **No growth ladder** (Context).

### Decision 8 — usage is exact and the kind never calls `WindowGuess`

llama-server's `usage` is exact and arrives on streams too. `lcpp` enters `usageMeasuredForKind`'s
**exact** set — the first self-hosted route to do so on its own numbers — and writes
`LiveInfo.Context` with `WindowSource="recorded"`. The engine's GPU-time usage already reaches the
Agent (`control-plane/engine_usage.go:365` → `workspace/agent/engines.go:788`); the catalogue's
`llamacpp` provider row says "billed by GPU time, token price 0" so the same tokens are not counted
twice.

### Decision 9 — staged: the CP pass-through first, then P1 as a kind-independent core, then P2 only on a passing measurement

- **Stage 0 — Decision 7's pass-through** (half a day). Required by every option; no reason not to.
- **Stage 1 — P1**: an `internal/harness`-shaped package holding the LLM client, the tool loop and
  tools, the MCP client and the context management, used from chatx's `ChatProvider`
  (`chat_providers.go:44, 70, 110, 149`). This is where "does coding work actually run on the
  self-hosted engine, and under which families do tool calls stay intact" gets measured on hardware.
- **Stage 2 — P2**: the kind wiring, the driver, the transcript writer, usage, tests, guide — wrapping
  the same core. Entered only if Stage 1 passes: (a) a 20-turn real task completes on the chosen
  family, (b) members accept the wake and re-prefill fixed costs, (c) fewer incidents than the
  opencode route are plausible.

If the motive is the window alone, **stop after Stage 0**. If self-hosted use stays occasional
single questions, stop after P0 (a `Send`-only provider, one to two days).

### Decision 10 — nothing to pin, install, log in to or watch for drift; the contract test moves to the engine

No Dockerfile ARG, no `versions.json` row, no `env_tool_versions` row, no cli-drift or release
watcher, no login routes on either `routes.go`, no connection card beyond "a chat engine is in the
catalogue, warm or not, default model", no `fs.go` deny-list entry (the engine token stays in
memory, fetched through the same func-var seam as `opencode.EngineEnv`). The one drift axis that
remains is llama-server's own API and tool-call output, which move with llama.cpp's version in the
engine image: a contract test against a real engine, opt-in like the live tests, replaces the TUI
string-contract tests.

## Alternatives rejected

- **Use llama-server's MCP client / `--tools` / `--agent`.** Wrong box (Context). Not a cost saving,
  an impossibility.
- **Port hermes-agent's growth ladder.** A bounce is minutes here and the GPU may not come back.
- **A Terminal route through a REPL.** A second UI for a kind the mirror covers (Decision 2).
- **Name it `native` / `llama` / `llm` / `llamacpp` / `engine`.** Decision 1.
- **A kind for the window's sake.** Stage 0 alone gives the return trip; the kind adds nothing to
  that. The kind is justified only by wanting sessions on the self-hosted engine that do not pass
  through opencode.
- **Go straight to P2.** More than half the cost is the agent core, which cannot be judged until it
  runs on the chosen family; P2's own increment (12–14 session-days) would be spent before knowing.
- **Read the registry by materialising a file for `lcpp`.** There is no reader for it; a file
  nobody reads is a drift source.

## Consequences

- Six firsts, each with a design cost named above: managed-only kind; transcript writer; in-process
  `ProcessModel`; tools executed by us; known-but-not-materialised MCP kind; exact usage on a
  self-hosted route.
- Fixed costs a member will feel and the guide must state: the box waking (minutes, first turn) and
  re-prefill of the whole history every turn once the box is replaced (tens of seconds at 30B / 30k;
  `/slots` save/restore cannot cross boxes and does not pass the gateway anyway). Neither is worse
  than the opencode route; both are the engine's, not the kind's.
- The estimate is more likely to be exceeded than undercut on its two largest items: the Review
  finds E (tools) likely above 3,000 lines and F (MCP client) above 1,200, because the request-scoped
  dispatch refactor above lands in F and the smallest complete kind is already 2,109 lines. The ranges
  stand; the direction is recorded.
- The estimate (details and the per-item table in docs/log/99 §8): **22–27 session-days, roughly
  9,000–13,000 lines with tests**, one lane ≈ 5 weeks, three lanes ≈ 2–3 weeks. Of that, the core
  shared with P1 (LLM client, loop and tools, MCP client, context) is 12–15 days; **P2's own
  increment is 12–14 days**. P0 is 1–2 days; P1 is 10–13.
- What a kind no longer needs to be: pinned, installed, logged in, string-contract-tested. What it
  newly depends on: the engine image's llama.cpp version.

## Phases

| Phase | Content | Gate to the next |
|---|---|---|
| 0 | `GET /engine/{key}/props` on the CP; `syncEngineProviders` overwrites `limit.context` while awake | none — ship |
| 1 | P1: `internal/harness` core + chatx provider; family chosen; hardware measurement | Decision 9's (a)(b)(c) |
| 2 | P2: kind wiring, driver, transcript writer, usage, contract test, guide, this ADR to *adopted* | — |

## Open questions (answer before Phase 1)

1. Which one or two model families, and whether llama-server's `tool_calls` stay well-formed JSON
   on them across a 20-turn task.
2. Whether the llama.cpp version baked in the engine image actually has
   `/v1/chat/completions/input_tokens` and `/control` (confirmed in the README; an older build may
   lack them, which changes Decision 7).
3. Whether `dispatchMCPStdio`'s globals can become per-call without touching the 72 tool bodies; if
   not, `af` tools go over loopback HTTP like everything else. → closed in Review.
4. Where the Console reads `Capabilities.ProcessModel` (what the fourth value breaks). → closed in
   Review.
5. Where the "no terminal route" flag has to be read (launch modal, driver switch, handoff modal).
   → closed in Review.

## Sources checked (2026-09-19, `951bb402`)

`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go,session_handlers.go}` ·
`workspace/agent/internal/mcpx/mcp_stdio.go` · `workspace/agent/internal/mcpreg/{def.go,materialize.go,probe.go}` ·
`workspace/agent/internal/chatx/chat_providers.go` · `workspace/agent/internal/agents/opencode/window.go` ·
`workspace/agent/internal/usagex/usage.go` · `workspace/agent/{engines.go,agent_models.go,agent_instructions.go,agent_rtk.go,usage_fold.go,model_provider.go}` ·
`control-plane/engine_gateway.go` · `control-plane/internal/mcpsrv/{mcp.go,mcp_server.go}` · `console/src/agents/registry.ts` ·
`console/src/lib/agentModels.ts` · `docs/log/{32,36,40,43,74}-*-agent-kind.md` · `docs/log/34-native-runtime.md`.

## Review (2026-09-19, before Phase 0)

### Confirmed

- The gateway has no path allow-list in `serve`, but the only registered route is
  `/engine/{key}/v1/{path...}` and a local llama.cpp target is built as the engine URL plus
  `/v1/` plus that captured path (`control-plane/engine_gateway.go:211,467-583,1102-1117,1169-1177`).
  A borrowed engine substitutes the far gateway's `base_url`, not the llama-server root
  (`control-plane/engine_gateway.go:1094-1117`). Therefore `/props` and `/slots` have no escape
  hatch; Decision 7 and Stage 0 still stand.
- Unknown kinds still normalize to Claude (`workspace/agent/internal/sessionx/agent.go:25-53`), and
  the exact usage set is still only Claude, Codex and OpenCode
  (`workspace/agent/usage_fold.go:201-212`). `ProcessModel` still documents exactly
  `shared-daemon`, `per-session-child` and `tui` (`workspace/agent/internal/agents/driver.go:142-157`).
- Recounting non-test Go source under `internal/agents/<kind>` gives agy 2,109, cursor 2,753,
  copilot 2,892, kiro 2,896, opencode 5,171, claude 5,853 and codex 5,948 lines. The scale used by
  docs/log/99 §8 is reproducible.
- The non-test `"kiro"` inventory found no omitted kind branch in handoff, spawn, shared-view or
  fork-at: those paths are capability/registry/generic paths (`workspace/agent/internal/sessionx/session_handlers.go:482-529,1002-1103`,
  `workspace/agent/internal/sessionx/session_spawn.go:294-363`,
  `control-plane/session_share.go:645-670`). Scheduled launch is already in the table
  (`control-plane/scheduler_wake.go:278-285`). Name searches found no existing `lcpp`, `lc`, `-lc`,
  `KindLcpp` or `kind-lcpp` outside this proposal, and after `git fetch origin`, neither ADR 0093
  nor docs/log 99 exists on `origin/develop`.

### Broken assumptions

- `dispatchMCPStdio` is not a pure one-line-in/one-line-out switch. Besides the permission globals
  set by `parseStdioFlags`, it reads the owning session, conversation, Chromium, peer, image and
  fleet-spawn process state; `tools/list` starts a process-wide once-only watcher, and progress or
  list-change notifications write asynchronously through the process-wide stdout writer
  (`workspace/agent/internal/mcpx/mcp_stdio.go:79-123,125-210,243-297,385-416,479-544,2252-2518,3236-3277`).
  Decision 6's direct in-process call is valid only after a request-scoped dispatch context also
  owns notification output and watcher lifetime. Merely replacing `parseStdioFlags` with an option
  struct is insufficient; loopback remains the fallback.
- `Capabilities.ProcessModel` is not serialized to or read by Console at all; it is currently only
  populated by managed drivers (`workspace/agent/internal/agents/driver.go:142-157`,
  `workspace/agent/internal/agents/{codex,opencode,kiro,cursor,copilot}/driver.go`). Adding
  `in-process` breaks no Console consumer today. If it is intended to control UI, a wire field and
  a consumer are additional work; `tuiMemoryCost` is unrelated descriptor data
  (`console/src/agents/registry.ts:123-127`).
- The §3 table omits the report reconciler from its implementation checklist. Its two-tick settle
  rule exists for polling TUIs (`workspace/agent/internal/chatx/chat_report_reconcile.go:45-54`);
  `lcpp` must emit the ordinary turn-end marker as §4.8 says. This is a verification/test point,
  not a new kind-name branch. No other omission was found in the requested handoff, spawn,
  scheduled-launch, shared-view and fork-at paths.
- Estimate E is more likely to exceed 3,000 source lines: it combines a confined filesystem
  editor, shell cancellation, output/binary limits, approval policy, planning/question state and a
  parallel tool loop, while the smallest complete existing kind is already 2,109 non-test lines.
  Estimate F is also biased upward beyond 1,200 lines because it includes two transports, two MCP
  protocol eras, reconnect/lifetime/notifications, and the request-scoped refactor above rather
  than only extending the probe (`workspace/agent/internal/mcpreg/probe.go:332-344,483-498`). The
  review does not replace the estimates; it records that both ranges have more upside than downside.

### Answered

- Questions 1 and 2 remain hardware checks: model-family tool-call integrity and the endpoints in
  the baked llama.cpp image cannot be established from this tree.
- Question 3: no, not by changing the flag globals alone. The in-process route needs the wider
  request-scoped boundary listed above; otherwise use loopback
  (`workspace/agent/internal/mcpx/mcp_stdio.go:125-210,479-544`).
- Question 4: nowhere in Console today; `ProcessModel` does not cross the Agent API
  (`workspace/agent/internal/agents/driver.go:142-157`).
- Question 5: the new terminal-route capability must gate both launch forms
  (`console/src/features/repos/LaunchModal.tsx:776-803`, `console/src/features/repos/StartModal.tsx:303-316`),
  quick launch (`console/src/features/repos/RepoRowConnected.tsx:178-184`), the driver-switch menu
  and action (`console/src/features/sessions/SessionMenu.tsx:179-190`,
  `console/src/features/sessions/useSessionActions.tsx:262-287`), and the server-side managed-to-TUI
  transition (`workspace/agent/internal/sessionx/session_driver.go:62-105`). Handoff uses the same
  generic create path and therefore needs the target kind to select managed, not a separate route
  label (`console/src/features/sessions/HandoffModal.tsx:75-84,119-130`).

## Implementation record for phases 0 and 1 (2026-09-20)

Phases 0 and 1 are on develop (#761 and #767 for phase 0 and its follow-up; #762, #764, #766 and #770
for phase 1's segments D/F/E/G). **No decision text changed.** What follows records what the
implementation and the live runs added to those decisions, and what they overturned.

### Two premises that turned out to be wrong

- 🔴 **Decision 6's "the `af` tools run over loopback HTTP, the same as every other CLI" is wrong in
  its second half.** `mcpreg/builtin.go:45-50`'s `BuiltinAF` is a **stdio** ServerDef
  (`runArgs: ["mcp-stdio", "--self-report", "--chromium-attach"]`), and the Agent has no HTTP MCP
  entry point at all. **Every other CLI reaches af over stdio too.** "The same as every other CLI"
  was the right intent; only the stated mechanism was wrong. #764 connects through the existing
  stdio ServerDef with its generic client, so `internal/mcpc` holds no af-specific code. The Review's
  conclusion — that calling `dispatchMCPStdio` in-process is out of scope — still stands.
- 🔴 **Decision 7's "`/props` reports the window the engine actually started with" does not hold in
  router mode.** Measured live (#770): `GET /engine/llm/props` answers 200 with `role: "router"`,
  `model_path: "none"` and `default_generation_settings.n_ctx = 0`. A llama-server holding several
  models at once (`--models-max`) describes the ROUTER there, not a loaded model. The real number is
  in `{engine}/v1/models`'s `data[].meta.n_ctx`. #767 closes this while keeping phase 0's own gate
  (records no demand, wakes nothing): only when `n_ctx == 0` does `props()` read `/v1/models` itself
  and add one key. **Reading `/v1/models` through the gateway's ordinary route is not an option** —
  `serve()` records demand (`engine_gateway.go:540`) and calls `ensureReady` (`:1018`), which buys a
  GPU box to answer a question about a window.

### One condition the decisions did not state

- 🔴 **A BORROWED row (ADR 0079) needs the same change on the LENDING deployment.** A borrowed row's
  `/props` goes to the path the far side declared in its token answer — the far deployment's own
  `/engine/{key}/props`. A lending Control Plane that predates this answers Go's stock
  `404 page not found` (observed on 2026-09-19; cleared when sandbox was redeployed). #767
  deliberately never augments a borrowed row locally: reading the far side's `/v1/models` would go
  through the far `serve()`, i.e. **buy a GPU box on the lending deployment**. So the borrowing side
  can only read the real value once the lender ships the same change.

### Measurements

| Item | Measured | Note |
|---|---|---|
| Unresolved question 2: `/v1/chat/completions/input_tokens` | **Exists.** `{"input_tokens":61,...}`, matching the same request's `prompt_tokens` | 🔴 The field is **`input_tokens`**, not the README's `tokens`/`n_tokens` |
| Unresolved question 2: `/v1/chat/completions/control` | **Exists.** Wants a `model` and an in-flight completion id | `Interrupt` is phase 2, so it is not in the public API |
| Unresolved question 1: family | **Qwen3** (`qwen3.8-27b-uncensored-q4_k_m`): 16 round trips across 2 projects, `tool_calls` valid JSON on every turn, no mangled names, no swapped arguments even with 4 parallel calls | ⚠️ **One family, few runs.** Other families were left unmeasured to avoid evicting the shared GPU box (`role: router`, `max_instances: 1`) |
| Window | Real value 262144, **exactly matching** the catalogue's declared 262144 | See below |
| Wake (true cold start) | **4–5 minutes** (buying the box plus syncing the model); `engine_waking` retried correctly across it | Phase 1-D's 34.8 s was a box already warming. The decision text's "minutes" was right |
| Compaction | `input_tokens` climbed 93→663 and fired at the threshold for real; full went 29→30 entries, i.e. **nothing past was dropped** | Run with the window forced to 900: filling the real 262144 only burns shared GPU, and what is under test is the threshold arithmetic and the wire shape |

🔴 **An honest note about the motivation.** The real window and the declared one agreed. This ADR's
motivating defect — the window travelling one way through four hops and drifting — **did not show up
as actual harm in this one sample**. That does not make phase 0 pointless (having no way to check was
itself the problem), but the strength of the motivation should be marked down to match the evidence.

### What only a live engine found

Three defects survived every scripted-client test and appeared only against the real engine. They are
recorded as a worked example of decision 5's consequence: part of this kind's cost is that **we own
this class of problem** once we are the executor.

- The send right after compaction was rejected by Qwen's chat template in two places: folding the
  still-unanswered user turn into the summary left the send ending on a system message
  (`No user query found in messages`), and the summary rode as a SECOND system message mid-list
  (`System message must be at the beginning`).
- chatx's P0 provider dropped the last stored history entry unconditionally. Only 2 of `prov.Send`'s
  6 call sites satisfy the premise it assumed; compaction and a report auto-turn silently lost the
  very turn being summarised. **`lcpp` is the first provider that assembles history itself**, which
  is why the problem appears here and nowhere else.

### Deliberately left outside phase 1

- **`chat_providers_lcpp.go` (the P0 provider) is not wired to E/F/G.** Putting the P0 provider behind
  the tool loop changes how assistant chat behaves, which is a product decision beyond phase 1.
- ⚠️ **Summary QUALITY is model-dependent.** This quantised 27B sometimes produced a thin summary —
  once it echoed the upcoming turn's own question instead of summarising. The mechanics
  (append-only, a single leading system message, a trailing user turn, real token counts) all held,
  so this is not a harness defect. It is material for choosing a family in phase 2.
- The approval gate's zero value is **fail-closed** (`Runtime.Approve == nil` declines every `Mutates`
  call); unattended execution passes `AutoApprove` explicitly. Decision 5's "approval really does stop
  a tool" is implemented so that forgetting to wire it fails loudly rather than passing silently.

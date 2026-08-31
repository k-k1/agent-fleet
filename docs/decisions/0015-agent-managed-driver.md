# 0015. A managed driver for agent control — a shared runtime with structured RPC by default, the read layer preserved, the CLI route kept permanently

English | [日本語](0015-agent-managed-driver.ja.md)

- Status: decided; P1–P3 implemented (2026-07-15) — P1 (extended Codex observation), P1.5 (the
  Console's receiving side plus the Driver-layer interface), P2 (OpenCode managed — the first
  appearance of Driver / RuntimeSupervisor / the turn state machine / Interaction / reconciliation;
  managed creation unlocked, new opencode sessions default to managed; the measurements are in
  docs/27 §12.2), and P3 (Codex managed — the second Driver, daemon drain, the new default, and
  mutually exclusive switching in both directions; measurements in docs/27 §12.3)
- See also: [27-agent-managed-driver.md](../log/27-agent-managed-driver.md) (the design proper) /
  [0012-go-internal-refactor.md](0012-go-internal-refactor.md) (the Agent interface in `internal/agents` — the read layer this decision builds on) /
  [0014-agent-exit-recording.md](0014-agent-exit-recording.md) (the pane wrapper — moved into the supervisor when managed)

## Context

All three agents are controlled the same way: a TUI inside tmux, input by send-keys, scraping
capture-pane, injected hooks, and parsing the native store. The Codex TUI's
model-switching-by-itself bug (at 93–99% utilisation it fires `ThreadSettings` repeatedly →
switches to a lighter model unintentionally → compacts; the interim `hide_rate_limit_model_nudge`
toggle landed on main in `9414525`) exposed the limit: **when you answer a TUI-specific dialogue
by pressing keys, AF cannot detect or control it as a structured event**. Meanwhile Codex CLI
0.144.3's `app-server` (bidirectional JSON-RPC) already runs as one resident process per
workspace (`codex_appserver.go`, the read-only observer for compaction detection, `fa7e47d`),
and OpenCode has `opencode serve` (HTTP + SSE) with officially supported concurrent TUI use.
Only Claude has no single-process aggregation point (the Agent SDK / stream-json spawn a child
per session, and Remote Control has no public local API).

The design was drawn up independently in two parallel sessions (sol=A / fable=B), and the user
adjudicated between them.

## Decision

1. **Driver policy, per agent**: **Codex and OpenCode use managed (a shared runtime with
   structured RPC) as the default, first driver**, with **a user-selectable CLI (TUI) route kept
   permanently**. On the CLI route the TUI is the writer, AF observes read-only through the
   shared runtime, and chat⇔terminal keeps working. Switching is mutually exclusive on Codex
   (via stop→drain→resume); OpenCode needs no exclusion (serve serialises, TUIAttach is
   possible). **Claude stays on the terminal CLI as it is** (CLI operations such as answering
   compact are operationally necessary; the Session Manager / idle eviction idea is frozen and
   preserved in docs/27 appendix A). The TUI route is not removed (it stays a maintained
   surface).
2. **The skeleton is built bottom-up**: the existing read normalisation layer (`Agent`,
   `TranscriptData`, `WireLive` in `internal/agents`) is preserved untouched, and on top of it we
   add the Driver layer (per thread:
   Send/Steer/Interrupt/UpdateSettings/Respond/Events/Snapshot) and the RuntimeSupervisor layer
   (daemon start, restart, generation, drain). Process management is separated out of the Driver
   (a part taken from proposal A).
3. **Recording is split three ways, with no double persistence**: read = the native store is
   canonical (rollout JSONL / SQLite / `<sid>.jsonl`), live = runtime events, write = the
   structured API. What AF persists is only operational metadata that contains no conversation
   content (turn state transitions, the ClientMessageID ledger, the Interaction audit, generation
   history). History compatibility follows automatically from not moving the canonical store.
4. **A turn state machine plus ClientMessageID**:
   `queued/starting/running/waiting_interaction/interrupting/completed/failed/cancelled/unknown`
   are explicit, and resends are made idempotent by a ClientMessageID that AF assigns. On a
   disconnect the turn falls to unknown and is recovered by **a shared reconciliation procedure**
   (bump the generation → snapshot → reconcile against native history → snapshot to the Console →
   resubscribe to live). It reconciles against a snapshot, not by replaying events.
5. **Interaction, generalised**: approvals, questions and plan confirmations are structured as
   an `Interaction` (Decision: allow/deny/cancel/answer, plus Scope: once/turn/thread). The
   initial implementation covers questions only (all three run in bypass mode for approvals).
6. **Applying auth and config changes goes through generation + drain only**: re-login, config
   changes and daemon updates are applied by the process-regeneration path, "bring up a new
   generation and drain the old" (Codex = restart the daemon then re-resume every thread;
   OpenCode injects by env and so must restart, taking the same path). We do not bet on hot
   reload.
7. **The wire stays the existing generic `/sessions` surface**: no per-agent REST
   (`POST /claude/sessions/...` and the like). The Console holds no agent-specific knowledge and
   decides what to draw from Capabilities (Steer/Fork/DynamicModel/DynamicEffort/DynamicMode/
   Permissions/Questions/EventReplay/EphemeralThread/TUIAttach).
8. **Assistant chat is not folded in**: the three one-shots (`codex exec` / `claude -p` /
   `opencode run`, isolated in a separate home) stay. Deferred until we can confirm that
   per-thread config reproduces the equivalent of an isolated home (no user MCP started, history
   not polluted); the future home for it is `EphemeralThread`.
9. **Order of work**: P1 extended Codex observation (read-only, to separate the originating bug
   into "the TUI layer" versus "a server-side reroute") → P1.5 the Console managed-session UI →
   P2 OpenCode managed (the first instance of the Driver type, and the safest since no exclusion
   is needed) → P3 make Codex managed the default, plus the driver selection UI and exclusive
   switching.

## Options rejected

- **Hybrid writing (TUI and AF both writing)**: rollout writes, model settings and turn state
  all conflict. A single writer per thread is an invariant, and coexistence is limited to
  "writer = TUI, observer = AF" (the CLI route).
- **Replacing the read layer with a new event journal / read model** (proposal A): duplicating
  the canonical store adds gaps and mismatches to manage, and breaks history compatibility for
  past sessions. Adopted in the reduced form of "preserve the read layer, supplement with
  operational metadata".
- **A per-agent REST surface** (suqhrov's Claude proposal): per-agent APIs multiply into three
  sets and contradict a unified Driver.
- **A shared ControllerLease mechanism** (proposal A): OpenCode needs no lease (concurrent use is
  officially supported) and Codex only needs exclusive switching, so a shared mechanism is
  overkill. Reduced to a controller field per thread plus per-agent allowed transitions.
- **Abolishing the Codex/OpenCode TUIs entirely**: nearly settled at one point, then turned into
  keeping a user-selectable CLI route (as an operational fallback, and as an explicit memory
  trade-off the user makes).
- **Adopting the Claude Session Manager (stream-json child processes + idle eviction)
  immediately**: it does help memory (evicting idle children), but it is mutually exclusive with
  operations that need direct CLI interaction, so it is frozen. The design is preserved in the
  appendix.
- **Splitting the kind (a new `codex-app` kind and so on)**: transcript, settings, auth and
  models are shared, so the driver is a field on `session.Meta` and the kind is not split.

## Consequences

- The originating bug (the model switching by itself) disappears structurally in a managed
  session (AF is the only writer). The CLI route is covered on a second front by the
  `hide_rate_limit_model_nudge` toggle (which stays permanently).
- Memory: measured on Codex, one TUI session is about 280 MiB versus a shared app-server at
  about 129 MiB plus per thread. Managed by default carries the majority, and only those who
  choose the CLI pay the extra ~230 MiB (showing the cost in the selection UI is under
  consideration). Claude does not improve.
- WireLive's heuristics (hook state, rollout healing, the pending probe, usage parsing,
  capture-pane) are replaced by event-driven handling when managed. The share for the CLI route
  and for Claude remains as a maintained surface.
- Making the Console work without panes (the mirror as the primary UI, Interaction responses,
  attachments via the API, moving exit recording into the supervisor) is the critical path
  before P2.
- The top thing to verify before implementing — resuming a server-created thread in the TUI —
  works, and demonstrated bidirectional managed→TUI→managed switching plus compatibility with
  old TUI rollouts. The measurements and E2E, including redelivery of a question request,
  restating policy after a resume, and settling on "interrupted" after killing the daemon, are
  recorded in docs/27 §12.3.

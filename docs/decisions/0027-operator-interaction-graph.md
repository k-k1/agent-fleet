# 0027. Visualise the operator↔session exchange as an SVG sequence diagram

English | [日本語](0027-operator-interaction-graph.ja.md)

- Status: **adopted, work started** (Phase 0, the contract frozen). The design is [docs/44](../log/44-operator-interaction-graph.md).
- See also: [0015](0015-agent-managed-driver.md) (the managed driver) / [0021](0021-scheduled-execution.md) (scheduled execution) /
  docs/30 (session completion reports → the fleet and the operator) / [history/19](../log/19-assistant-chat.md) (assistant-chat)

## Context

The fleet operator (an af_write conversation) drives several sessions with `create_session` and
`send_to_session`, and each session returns completion or abnormal termination to the conversation
as a `role:"report"` message (docs/30). There was **no diagram that shows this exchange as a
chronological back-and-forth at a glance**, so understanding "what did I just send, which are
running, when did they come back" meant reading back up the chat lines.

## Decision

**Visualise it as a hand-written SVG UML sequence diagram, one per operator conversation.**

1. **Drawing is hand-written SVG (mermaid is not adopted).** This feature's value is in colouring
   live state, animating what is running, clicking a node to open the session, and following the
   `--kind-*` theme — none of which a static mermaid diagram provides. The "pure-function layout
   plus inline SVG" of the SCM commit graph (`CommitGraph` / `lib/gitgraph.ts`) is reused as the
   structural template.
2. **Dispatch edges are persisted in a new dispatch ledger.** The two directions are stored
   asymmetrically: reports (session → operator) persist in the conversation as `role:"report"`, but
   dispatches (operator → session) exist only in the arm store and are erased when the report is
   delivered. Alongside `armSessionReport()` we append
   `{ts,session,sessionKind,kind,dir,excerpt}` to
   `~/.config/agent-fleet/operator-graph/<conv>.jsonl`. It is read back with
   `GET /api/chat/conversations/{id}/dispatches`.
3. **The scope is one operator conversation.** That matches the data model (`report_to` = a
   conversation id) directly, and it opens from the af_write button in the ChatView header.
4. **Contract first, then three tracks in parallel.** P0 freezes the REST DTO, the TS types
   (`console/src/types/opgraph.ts`, import only) and the skeleton of the doc/ADR; P1 runs
   S-BE (Go), S-LOGIC (`lib/opgraph.ts`) and S-VIEW (the view, pane wiring and i18n) in parallel
   worktrees. The shared glue (the pane union, Pane.tsx, paneTitle, ChatView, i18n) is owned
   exclusively by S-VIEW, so conflicts are confined to one merge point.

### Options rejected

- **mermaid's `sequenceDiagram`** (minimal code, using an existing dependency): static, and weak on
  live state, run animation, click-to-launch and theme injection. It would be the strongest choice
  for a cheap first attempt, but it does not fit this feature's point (a live overview).
- **A node-link (radial hub) diagram**: the topology looks good, but it is poor at expressing "an
  exchange is a back-and-forth in time", and a force-directed layout is heavy to implement.
- **Using the same diagram as a fleet-wide overview**: reconciling several conversations and laying
  them out is heavy, and it dilutes the point of a sequence diagram. Split out as **a separate
  diagram and a separate task**, correlating the commit graph, sessions and agents.
- **Reconstructing dispatch edges from existing data with no ledger**: only the report side can be
  drawn, leaving the outbound half missing. About 30 lines of ledger makes it complete, so we
  persist the dispatch side too.

## Consequences

- The addition is `operator_graph.go` on the Agent side (the ledger and reading it back), the
  `recordDispatch` calls alongside in `session_handlers.go`/`session_io.go`, the routes (both the
  agent and the CP allowlist), and the Console (`types/opgraph.ts`, `lib/opgraph.ts`,
  `features/opgraph/*`, pane wiring, the ChatView entry point, i18n). The existing report and
  notification paths are unmodified.
- **A deliberate limit**: the ledger only covers what happens after it is introduced (past
  dispatches cannot be recovered; reports are already history). Managed reports have no body. An
  opencode af_write conversation is not linked because `report_to` is not injected (inheriting the
  limitation in docs/30).

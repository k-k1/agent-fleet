# 0115. Move a session to another agent kind without an LLM: a rendered transcript file, pointed at by the first prompt

English | [日本語](0115-move-session-to-another-agent.ja.md)

- Status: **accepted** (2026-10-20; proposed 2026-10-15). P2a and P2b are built and were used once
  on a live fleet; see the implementation record at the end. Follow-ups: #2233, #2252
- Tracking: #2182. Builds on #2183 (the shared Markdown renderer, `transcript.Markdown`).
- See also: [0039](0039-fork-at-message.md) (fork) /
  [0049](0049-session-changed-files.md) (the changed-files list) /
  [0042](0042-user-instructions.md) (instructions delivered per kind)

## Context

A session that hits its agent's usage limit stops until the window resets — days, for a weekly
cap. Two ways to continue elsewhere exist, and both fall short:

- **Hand over to another agent** asks the operator assistant to read the source and write a
  summary. That is an LLM call that can hit a limit itself, it is lossy, and what survives depends
  on the operator's judgement.
- **Fork** (ADR 0039) carries the whole conversation, but only into the same agent kind, which is
  the kind that just ran out, and only for the kinds that can fork.

P1 (#2183) gave every transcript-capable kind one renderer from the normalized
`transcript.Turn` to Markdown, with secrets scrubbed before layout. This record decides how that
file becomes the start of a session on a **different** kind.

### What each kind's transcript yields today

Read from `Agent.Transcript` / `claude.CollectAllTurns` on develop (2026-10-15). Tool output is
capped by the parser, not by the renderer, and a move cannot recover what a parser already cut.
"runes" and "bytes" are as the code counts them: `transcript.CapOutput` counts runes, the
parsers with their own `outClip` / `toolOutputMax` slice bytes. A cut output ends with an
omission marker.

| Kind | Source | Tool output | Usage-limit detection (`limited`) |
|---|---|---|---|
| claude | session jsonl via `CollectAllTurns`, compaction summaries included | at most 4000 runes | yes |
| codex | rollout | at most 4000 runes | managed only |
| opencode | managed and a Terminal on the shared server: the v2 HTTP API; otherwise the SQLite store | at most 4000 runes | no |
| cursor | Terminal: transcript file. **managed: the driver's memory, empty once stopped** | Terminal: none. managed: at most 4000 bytes | no |
| kiro | managed: memory while alive, v2 JSONL after | at most 4000 bytes | no |
| agy | conversation jsonl (+ DB probe for a pending question) | at most 4000 bytes | no |
| copilot | `events.jsonl` | at most 4000 bytes | no |
| lcpp | its own store | at most 4000 runes | no |
| muse | its own store + commentary log | at most 4000 runes | yes |
| shell / ssm | none | — | — |

Consequences: the move is offered for every `CanTranscript` kind, but only claude, managed codex
and muse can **trigger** it from `limited`. A managed cursor session must be moved while its
handle is alive, because the conversation does not exist anywhere else. A read that fails (an
opencode v2 server that does not answer, say) returns no turns, which the move must refuse rather
than write an empty file (decision 6).

## Decisions

1. **The handover is a file the target reads, named in its first prompt.** The Agent renders the
   source, writes it, and starts the target with a short prompt that says where the file is, what
   it is, and to read the head before doing anything. The conversation never passes through an
   LLM on the way.
2. **Rejected: synthesizing the target CLI's native store.** Every store is a private,
   version-drifting contract (the opencode v2 store replaced its schema outright), and claude's
   thinking blocks are signed, so a synthesized history is either rejected or silently replayed
   without them. Writing a store is safe only within one kind, where fork already does it, each
   kind its own way (ADR 0039; for example the CLI's fork API for codex and opencode, a truncated
   jsonl for claude's point fork, Fleet's own store for lcpp).
3. **Rejected: sending the whole history as the first turn.** It overflows small context windows
   (lcpp) and makes the first turn's cost proportional to the history. A file lets the target read
   the head and page into the rest only when needed.
4. **Rejected: an LLM summary.** That is the existing handover. It stays, for when a summary is
   what the user wants; the move is the deterministic alternative next to it.
5. **Same-kind targets are not offered.** The move exists to get away from a kind's quota, and a
   session of the same kind draws on the same account. Where the kind can fork (`CanFork`), the
   dialog points at fork, which carries more (native history, thinking, tool state); for the
   kinds that cannot (cursor, kiro and agy today), it says only that another kind is needed.
6. **File layout: the state first, the history after.** A target that reads only the first
   screen must already know what to do. In order:
   1. A header: source session, kind, model, working folder, branch, the time of the move, and
      one sentence saying this file is the whole record and the source's own files must not be
      opened.
   2. **Where it stopped**: the last user message verbatim (by decision 7's rule, an Agent-internal
      prompt is not it), the last assistant text, an unanswered question if any, instructions
      that were queued but never reached the transcript (`TranscriptData.Queued` /
      `QueuedItems`), peer messages waiting in the source's pending spool (sender, intent and
      body), and whether the last turn was cut off by the limit.
   3. **Working copy state** at the moment of the move: `git status --porcelain`, the diff stat,
      and the branch's commits ahead of its upstream.
   4. **Changed files** of the session (the ADR 0049 list, Agent-side aggregation).
   5. **Open threads**: children the source started and their states, and the source's pending
      handoff proposals, by name (decision 12).
   6. **The conversation**, filtered by decision 7 and rendered by decision 8.

   A source whose read yields no turns is refused with a reason, never written as an empty file.
7. **Keep what the target needs to continue, drop what it provably does not.** The question for
   every part is "would the target act differently without it?". The rules are mechanical, by
   part kind, tool and position; no LLM and no guess at importance. When the data cannot show
   that a part is safe to drop, it is kept.
   - **Kept in full:** user messages, questions with their options and answers, plans, peer and
     operator messages, compaction summaries, assistant text, and `userfile` parts (paths and
     caption: "fix this image" needs the path).
   - **Kept as one line (name and label):** every tool call that is not on the read-only list
     below, and every `error` part. The data does not say whether a shell command or an MCP call
     changed something (`Part` has no side-effect field, and `File` is set only for recognised
     edits), and dropping a finished side effect invites the target to repeat it. An edit keeps
     its verb and file; its diff is in the working copy and in 6.3 and 6.4.
   - **Kept as instruction and result:** a delegation, without the subagent's own tool calls.
   - **Dropped:** thinking and sidechains (as in P1); usage and cost rows (`CostOnly`); model
     badges; tool output outside the last 3 turns; tool calls on an explicit per-kind list of
     read-only tools (file reads, searches, listings, web fetches), maintained next to each
     parser's tool names; and prompts the Agent itself injected **whose text matches a known
     Agent prompt** (the usage-limit resume, `isRateLimitResumePrompt`, and its siblings).
     Provenance alone is not enough: `Turn.Source` is `schedule` for both the Agent's resume and
     a user's own schedule, operator, peer, chat-bridge and spawn messages are tagged too, and
     the tag ledger keeps only recent bodies. A turn of unknown origin is kept.
   - **Unknown part kinds** are kept as the renderer writes them.
   - **The exception is where it stopped:** the last 3 turns keep every tool call with its output.
     A turn cut off by the limit is usually mid-investigation, and the output is what the target
     would otherwise run again.
   - **Size:** 256 KiB of final Markdown (UTF-8 bytes, measured on the rendered file) is a soft
     target. Over it, the oldest assistant texts are cut to their first paragraph, oldest first,
     each cut leaving a visible marker. User messages, the last 3 turns and sections 6.1–6.5 are
     never cut, so the file can still exceed the target; it is then written as is and the dialog
     shows its size. Refusing would leave the user with no way forward, and the target pages
     into the file anyway.
   - 256 KiB and 3 are starting values, not measurements. They are constants in one place, to be
     revised after the first live moves.
   - Secrets are always scrubbed (`bridge.ScrubSecrets`), as in P1.
8. **No new on-disk format; one in-memory one, rendered once in full.** The normalized
   `transcript.Turn` is already the kind-neutral representation, and the file the target reads is
   Markdown, because an LLM reads Markdown best. A JSON handover format would be a second
   contract to keep in step with the renderer. In between, the Agent builds a `Handover` value:
   the parts of decision 6, with decision 7 applied to the value itself (dropped parts removed,
   `Output` / `Edits` cleared where only the call is kept). It is then rendered with
   `ToolOutput: true` throughout, because `ToolOutput: false` hides more than tool output: it
   also drops compaction summaries, delegation prompts and results, and question previews.
   Tests check both the value (what was kept) and the final Markdown (that compaction summaries,
   delegations and previews survive, and its byte size). A machine-readable form, if one is
   ever needed, is a second rendering of the same value.
9. **The file lives in the target's throwaway directory**: `~/.af-work/<target>/handover-<source>.md`
   (`paths.SessionWorkDir`). It is not in the working copy, so it can never be committed. This
   holds for managed targets too, where `$AF_WORK_DIR` is not set: the path is computed, not
   read from the environment.
   - **Only kinds that can read it are offered as targets.** lcpp's builtin file tools refuse any
     path outside the working folder (`harness/cwd.go` `resolvePath`), so lcpp is not a target in
     P2a. cursor and kiro are measured in P2a (plan mode and the default permission mode); a kind
     that cannot read the file without a prompt the user did not expect is left out the same
     way. Pasting only the head into the prompt is not a fallback: the target would have no way
     to reach the history, and the user would not know.
   - **The file is part of the target's conversation, not scratch**: the target reads the head
     first and pages into the rest later, and its first prompt names the path. So moving the
     target to the trash archives the file with the session and restoring it puts the file back
     (`archiveSessionForDelete` gains it as a payload with its destination, the way it carries
     claude's jsonl). Without that, `removeSessionSideFiles` deletes `~/.af-work/<target>` and a
     restored target points at a file that no longer exists. Stopping or archiving the target
     without deleting it leaves the file in place, so only the trash path needs this.
   - **A chain of moves copies the earlier records forward.** When the source was itself a move
     target (A → B, now B → C), B's conversation holds only the pointer to `handover-A.md` and
     whatever B happened to read of it. So the move copies B's own handover files into C's
     directory next to `handover-B.md`, and the new file's header lists them as the earlier
     history. C then owns every file it depends on, and deleting A or B later takes nothing C
     needs. The trash rule above covers all of them.
10. **The target runs in the same working copy**, as fork does. The source is frozen first
    (decision 11), so two writers never share it; the `shared-working-copy` warning is not added.
11. **Freezing the source.** The move must leave exactly one session that can act on the working
    copy, against every path that can wake the source, not just the next tick. The move has two
    states recorded in the source's meta, `moving: <target>` while it runs and `movedTo: <target>`
    once the target exists, and both close the source's input.
    1. **A per-session gate, not the meta lock.** The meta lock (`sessionLockMu`) is one mutex for
       every session, so it cannot be held across a send or a CP call. Each session gets its own
       lifecycle gate instead. Every path that can start a turn or a process on the source holds
       it shared from its check through the point where the input is committed: `/input` and
       `/turn` (including the managed route that resumes a stopped session and then sends), start
       and resume, peer delivery (`deliverPendingPeer`, which goes through `/input`), report-back,
       scheduled deliveries, and a driver's own queue pump before it hands a queued item to the
       runtime (the cursor `pump` runs outside any HTTP handler). The move takes the gate
       exclusively, so it waits for inputs already accepted to finish committing, and none can
       start after.
       - **One lease per input operation.** These paths call one another (`deliverPendingPeer` →
         `HandleSessionInput`, the managed `/input` → `Resume` → `Send`). A Go `RWMutex` forbids
         recursive read locks: an inner `RLock` queued behind a waiting writer deadlocks the outer
         holder. So the public entry takes the shared lease once and passes it inward; the inner
         steps are internal functions that require a lease and never take the gate themselves.
       - **The pump holds its own lease, not the caller's.** For an input, "committed" means
         accepted into the driver's queue; the caller releases its lease there and does not wait
         for the pump while holding it. The pump takes a lease of its own for each item it hands
         to the runtime. An item accepted before the move is then either already handed over or
         still in the queue, where the snapshot reads it.
    2. **Under the exclusive gate, check the state, then close input.** The source must be idle,
       `limited` or `spendLimit`. A working source is refused (the user stops it or lets the turn
       end); a pending question, plan or permission is refused too, since the move would silently
       abandon it. Then `moving: <target>` is written. From here every gated path refuses with
       `session_moved` naming the target; a peer message is refused so its sender learns the new
       name. A refused delivery of the source's own usage-limit booking is recorded on the move,
       for decision 11.4.
    3. **Snapshot, render, write, create — and only then stop.** With input closed the source can
       start nothing, so it stays alive while the move works: its handle still holds a managed
       cursor's transcript and the driver queue. Read the transcript, `Pending`, `Queued` /
       `QueuedItems`, the pending peer spool (`agents.PendingPeers`, held apart from the driver
       queue) and the working copy (decision 6), render, write the file (the write is complete
       and synced before anything else happens), and create the target. The source is stopped
       only after the commit (11.5). cursor and kiro drop their queue (`q.DropAll`) on stop, and
       by then everything in it is in the file. Stopping consumes a stop-after-turn arm.
    4. **Failure and rollback.** If anything fails before the target is created (no turns, a
       write error, a create that the Agent refused), the move is rolled back: the source was
       never stopped and its queue never dropped, so nothing is lost; the file is removed and
       `moving` is cleared. The usage-limit episode is left as it was, with one repair: if the
       source's booking fired during the move and the gate refused it (11.2), that once-schedule
       is spent, and the existing watch would not book again while the episode still holds its id
       (`scheduleRateLimitResume` returns early when `ScheduleID` is set). The rollback therefore
       clears the spent id and books the resume again explicitly, at the original instant or, if
       that has passed, as soon as the scheduler allows. If the create may have started a target
       (a timeout, an error after the session was written), `moving` and the file both stay: the
       source remains closed, and its row offers "retry" (which reuses the same file) and "cancel
       the move", instead of guessing which side may act.
    5. **Commit.** Once the target exists, `moving` becomes `movedTo`, the spooled peer messages
       are taken out of the source's spool (they are in the file, decision 6.2; their senders are
       not told), and the source is stopped. Then the usage-limit watch is settled:
       - `rateLimitTick` works on a `ListMetas` snapshot, so a tick that read the meta before the
         move can still book after it. The tick therefore re-reads the meta under the gate
         immediately before `putRateLimitSchedule` and gives up when `moving` or `movedTo` is set.
       - Cancelling a booking must be able to fail visibly: `deleteRateLimitSchedule` returns
         its error, and a schedule id whose cancellation is unconfirmed is kept in a durable
         cleanup record separate from the episode. The episode closes for display; the id is
         dropped only when the CP confirms the delete or reports the schedule gone. Each tick
         retries the cleanup records, so a cancellation that failed while the CP was unreachable
         is finished after the CP returns, across an Agent restart, without waking the source.
         A booking that fires before that is refused by the gate.
    6. **Only the user reopens a moved source**, by an explicit "resume here anyway" that clears
       `movedTo`.
    7. **What the user sees afterwards.** The source is stopped, not archived, and its row says
       where the conversation went. A managed cursor source's mirror is empty once stopped; the
       handover file is then the only record, and the dialog says so before the move. A moved
       source is otherwise an ordinary stopped session: the stopped-session TTL archives it
       like any other.
12. **Ownership does not move.** Children and pending handoff proposals keep naming the source.
    Moving them is a separate, harder change (the spawn ledger, the child limit and the peer ACL
    are all keyed by name). The file lists them (decision 6.5) so the target knows they exist;
    the move dialog warns when children are still running. Their report-back to the source is
    refused by decision 11.2, so the user reads them in the Console.
13. **A turn cut off mid-edit is shown before the move, not fixed.** The dialog shows the
    working copy's dirty state (the same data as decision 6.3). The user decides; the Agent does
    not commit, stash or reset anything.
14. **Instructions and skills are not copied.** Fleet's policy and the user's instructions already
    reach every kind (ADR 0042). Project instruction files differ by kind (`CLAUDE.md` vs
    `AGENTS.md`); the header names the files the source's kind reads that the target's kind does
    not, so the target can read them itself. Skills are not translated between CLIs.
15. **The target never reads the source CLI's private state.** The prompt says so, the file
    carries no path into a CLI store (the header omits `TranscriptData.Path`), and the target's
    only input is the rendered file.

## Phases

- **P2a — the move.** `POST /api/sessions/{name}/move {kind, model?, driver?}` on the Agent: take
  the gate and check the state, then close input (decision 11.1–11.2), snapshot, build and render
  (decisions 6–8), write (9) and create the target with `initial_prompt` (1) while the source is
  still alive (11.3), then commit and stop the source, or roll back (11.4–11.5). The CP route
  allow-list gains it. Console: a session-menu item next to the existing handover, a dialog with
  the agent picker (other kinds that can read the file, with `get_agent_usage` where the kind has
  a source), the dirty state, queued instructions, spooled peer messages and running children.
  Tests: the filter and size rules on the value and on the final Markdown; an input that holds its
  lease while the move waits for the gate, and then calls inward, completes, and so does the move;
  an input held between its gate check and its commit, and a queue pump about to commit, either
  finish before the snapshot or are refused; a tick racing the move books nothing that survives; a
  cancellation that fails while the CP is unreachable is finished after an Agent restart without
  waking the source; every input path refuses a moving or moved source; an empty transcript, a
  write error and a refused create each roll back cleanly, with the source's queue (including an
  unsent ordinary user input) intact; a booking that fired during a move that then rolled back is
  booked again; a source with spooled peer messages carries them into the file once; a stopped
  managed cursor source is refused with a reason; a target moved to the trash and restored still
  has its handover files; after A → B → C, deleting A and B leaves C's files readable.
- **P2b — the trigger.** When a session shows `limited` (or `spendLimit`), the mirror's limit
  banner offers the move with candidates that have quota left. Only claude, managed codex and
  muse reach this state today.
- **Not in P2:** moving ownership (decision 12); measuring the bounds in decision 7 on real moves.

## Consequences

- A limited session can continue on another kind in one action, without any model call before
  the target's own first turn.
- The target starts with less than the source had: no thinking, no native tool state, no tool
  output before the last turns, and no record of read-only calls. That is the price of not
  touching native stores, and why fork stays the answer within one kind where it exists. A target
  that needs a file the source read reads it again; the working copy is the same.
- A moved source cannot be woken by anything but its user. That is stricter than any other
  stopped session, and it is what keeps one writer per working copy.

## Notes

- 2026-10-16: `MarkdownOptions.ToolOutput bool` no longer exists (commit 2518bab71); it is
  `Tools ToolDetail`. Where decision 8 says `ToolOutput: true`, read `Tools: ToolsFull`: any
  lesser level still drops compaction summaries, delegation prompts and results and option
  previews. The builder is `sessionx.BuildHandover` / `RenderHandover` (#2182, P2a step 1).

## Addendum (2026-10-16) — the source gate as built (P2a step 2, #2182)

What decision 11 asks of the source is in place on the Agent; the `/move` endpoint that drives
it is not yet.

- **The gate** is `internal/lifegate`: one gate per session, with a shared `Lease` per input
  operation and an exclusive `Lock` for the move. It prefers a waiting writer, so a steady
  trickle of inputs cannot hold a move off, which is also what makes a nested take a deadlock.
  The public entry takes the lease once; an inner step takes a `*lifegate.Lease` parameter and
  checks it (`Check`), and a lease carried on a request's context is borrowed by `Enter`
  (`deliverPendingPeer` → `/input`). The closed state is kept in memory as well as on the meta,
  because a meta read that lands inside an unrelated rewrite of the file does not parse; a meta
  that exists and still cannot be read after retrying refuses (`ErrMetaUnreadable`) rather than
  admits, since after a restart it is the only record of a move. The cursor pump releases its
  lease once `session/prompt` is on the child's stdin.
- **Gated paths:** `/input` (every body form, peer messages included), `/turn` (every op: on
  Managed each one resumes the runtime), `/start`, `/recreate`, `/driver`, `/respond`,
  `/answer-question`, `/plan-respond`, `/carried-answer`, the chat bridge's inbound text and
  button answers, the abort / auth auto-resumes (through `injectSessionPrompt`), the launch-task
  delivery (`typeInitialPrompt`), `deliverPendingPeer` (the lease is taken before the message
  leaves the spool), the usage-limit sweep, and the cursor queue pump. A refusal is `409
  session_moved` with the target in `error.target`.
- **Start states:** `stopped` is accepted next to idle, `limited` and `spendLimit`. A source
  parked at a weekly limit is usually stopped by the idle reaper long before anyone moves it,
  and a stopped session can start nothing either. A pending interaction is still refused, and
  so is a stopped **Managed cursor** source, whose conversation lived only in its handle.
- **The sweep** holds the session's lease for its whole step, not only around
  `putRateLimitSchedule`: the episode file that records a booking is then written before a
  move can close input, so a commit always finds the id to cancel.
- **Cancellations** go through `cancelRateLimitSchedule`: the id is written to
  `session-rate-limit-cleanup` before the CP is called and dropped only when the CP confirms (a
  404 counts), retried at the start of every sweep, with no expiry. Commit records the id before
  it closes the episode, and fails with nothing changed if the record cannot be written.
- **A refused booking and a rollback** are ordered by one lock: a rollback repairs, clears
  `moving` and reopens under it. A refusal that reaches its record after the rollback (the
  request stalled in between) finds the source open and books the resume again itself. The
  sweep's step takes the same per-session episode lock (after its lease), because shared leases
  do not exclude each other and the repair holds none.
- A confirmed cancellation stays in the record, marked done, while the session's episode still
  names the id: a commit that recorded it and stopped leaves a moving source whose rollback must
  still see that id as dead after the sweep cancelled it.
- **Every transition is on disk before it returns**: `moving`, `movedTo` and their clearing go
  through `session.WriteMetaChecked` (a temporary file renamed over the meta, every error
  returned). The in-memory mirror changes, and the episode closes, only after that write
  succeeded; a failed write leaves the move where it was.
- **The move's primitives** are `beginSessionMove`, `rollback` and `commit` (sessionx,
  `session_move_gate.go`) plus `reopenMovedSession` for decision 11.6. Commit changes the meta,
  closes the episode and cancels its booking; stopping the source and taking its spooled peer
  messages are left to the move, which owns their order.
- Only the cursor pump takes a lease of its own; the other Managed drivers' pumps are a
  follow-up.
- A refused booking is recorded even when the meta cannot be read at that moment (it is read
  under the meta lock first), and a sweep that finds a recorded refusal on an open session books
  the resume again, so a record the rollback missed is not a dead id for good.
- **Amendment to decision 11.2** (2026-10-16): the user accepted the start states above — a move
  may also start from `stopped`, except a stopped Managed cursor source.

## Addendum (2026-10-17) — every Managed pump takes its own lease (#2236)

- Every Managed driver's queue pump now takes a lease of its own per entry, before the entry
  leaves the queue, and releases it where the runtime holds the input:
  - cursor, kiro, copilot: `session/prompt` is on the child's stdin.
  - codex: `turn/start` is answered.
  - opencode: the request carrying the input (`/message`, or `/prompt` on 2.x) is written to
    serve. The lease spans `waitIdle`, so a move waits out another client's turn for at most the
    60 s drain rather than snapshot an entry the pump is about to send.
  - lcpp: the user turn is appended to the store, which is this kind's transcript.
  - muse: `turn/start` is answered. A `queued` disposition means the host holds the input behind
    a running turn, and a source with a running turn cannot be moved (11.2). `accept` no longer
    runs the pump on the caller's goroutine, since the caller holds a lease and a second one
    taken there would be a nested take.
- Releasing the lease there is early for the state check: the runtime's own record (copilot's
  `events.jsonl`, opencode's store, muse's `turn/started`) can still show the previous turn's idle
  when the queue no longer holds the entry. So `beginSessionMove` also asks the Managed handle,
  under the exclusive gate, whether it holds an entry it took and has not settled
  (`ManagedInFlight`; entries still queued do not count), and refuses the source as `working`
  if so.
- A pump the gate refuses leaves its entry queued and starts again on `lifegate.Reopen`; a meta
  it cannot read makes it look again after a second.
- `POST /sessions/{name}/settings` holds the lease and refuses a moving or moved source with
  `session_moved`: the change would land on a session whose conversation has gone. `GET` holds
  the lease through `Resume` on an open session, and on a moving or moved one answers from the
  meta's model, effort and mode without starting a runtime.

## Addendum (2026-10-17) — the move endpoint as built (P2a step 3a, #2182)

- **Routes** (Agent, and the same paths in the CP allow-list): `POST /sessions/{name}/move
  {kind, model?, driver?}`, `GET …/move/preview`, `POST …/move/retry`, `POST …/move/cancel`,
  and `POST …/move/reopen` for decision 11.6's "resume here anyway". The move and the retry
  create a session, so the CP sends them through fork's quota check. All but the preview are
  audited as `session.move[.retry|.cancel|.reopen]`.
- **The target's name is booked before begin**, so that `moving` can name it and the file can be
  written into `~/.af-work/<target>` before the create. The create runs in-process through the
  same path as `POST /sessions`, with the booked name in an unexported field that no client can
  set.
- **Targets:** claude, codex, opencode, copilot, agy and muse. lcpp is left out (decision 9). cursor
  and kiro stay off behind `moveUnmeasuredAllowed` until the decision 9 measurement is done
  (#2252). A request for the source's own kind is refused with `move_same_kind`, and `canFork`
  says whether to offer fork instead.
- **Uncertain create:** a create is rolled back only when it answered and left nothing under the
  target's name (no meta, no pane). An unknown outcome, or a failure after the target's meta was
  written, keeps `moving` and the file. The move is recorded in the state dir (`session-move`)
  with the request, the file and the spooled peer ids, so retry and cancel work after an Agent
  restart. Retry commits when the target exists, and otherwise creates it again under the same
  name from the same file. Cancel refuses while the target exists.
- **The target's meta decides.** A move commits only once the target's meta is on disk: the move's
  create writes it checked, before the first prompt is sent, and a 2xx without it leaves the move
  pending. A target that runs under the name without a meta (an orphan pane or runtime) is not
  created again by retry; cancel stops it. The booked name is held against every allocation, in
  memory and through the pending move's record, until the move ends.
- **A meta file that exists and does not read** is never taken for absent: retry and cancel
  refuse it, and only a target with no meta file at all is stopped as an orphan. The source's
  `moving` holds the target's name as well as the record does, so an Agent that stopped between
  begin and the first record write does not hand the name out again.
- **The first prompt is part of the move.** It is recorded by target (`session-move-delivery`)
  before the create, and kept until the target's `InitialPromptState` says delivered. While it
  does not, the move's answer and the source's preview report it, and
  `POST /sessions/{target}/move/deliver` sends it again on the user's word. It is never resent
  by itself, since an unknown delivery may have arrived. On a Terminal target the resend runs
  the create's own delivery in the background (readiness wait, Enter nudge, evidence of a started
  turn), and typed keys without evidence stay unknown. While the source's move has not created
  the target, the record is kept and deliver is refused.
- **After the commit**, the record stays, marked `committed`, until the spooled peer messages are
  taken and the source is stopped. An Agent that stops in between finishes those steps at boot
  (`RecoverMoves`), on retry, or before a reopen. Reopen runs under the move's operation lock.
- **The preview is scrubbed** like the file, and the git reads are bounded in time and bytes, with
  the cut stated.
- **Decision 14's header:** each kind's project instruction files are a fixed table
  (`projectInstructionFiles`). It lists only files a kind is known to load, so a target is never
  wrongly assumed to have read one. The header names those of the source's files that exist in
  the working folder and that the target's kind does not load.
- **Not built here:** archiving the handover files with a trashed target, and putting them back
  on restore (P2a step 3b). Also not built: the Console dialog, and `moving` / `movedTo` on the
  session row (#2254).

## Addendum (2026-10-17) — the Console dialog and the session row (P2a step 4, #2182, #2254)

- **The row wire** carries `moving` and `movedTo` on the Agent's `session.Session` and the CP's
  `sessionWire`. They have no DB-mirror column: retry, cancel, reopen and the resend all need the
  running Agent, so a stopped Workspace's mirrored list does not show them.
- **The menu** offers "Move to another agent…" right after the handoff, for the same sessions
  (a transcript, a folder, a running Workspace). On a moving or moved source the ordinary resume
  is not offered, since the Agent refuses it with `session_moved`; the same dialog then holds
  retry and cancel (decision 11.4), or the target, the first prompt's resend and "resume here
  anyway" (decision 11.6). The row's second line says where the conversation went, or that the
  move is unfinished.
- **The picker** offers the preview's eligible kinds, minus those the connection check reports
  as not connected, and lists every other kind with its reason. It shows usage only for the kinds
  the Console already reads it for (the WS bar's claude, codex and muse sources), read once when
  the dialog opens. Model and driver default as the new-session dialog does for the working copy.

## Addendum (2026-10-17) — the limit banner (P2b, #2182)

- **There was no limit banner in the mirror to extend**, only the state chip, so P2b adds one band
  (`MoveLimitBanner`) under the other mirror bands. It shows for `limited` and `spend_limit` on a
  running Workspace, never for a moving or moved source, and only when the preview (no refusal)
  leaves a connected target that is not the source's own kind and not already at its cap. Its
  button opens the existing dialog flagged as coming from the limit.
- **Quota ordering is display only.** From the banner the picker lists kinds with quota left first
  (least used first), then kinds with no usage source or reading, then those at 100% in a window.
  The first of them is the default until the user chooses. The usage is the same three sources as
  the WS bar; a kind without one is judged neither way and stays selectable and unmarked.

## Addendum (2026-10-20) — implementation record and the first live check (#2182)

- **Built.** P2a and P2b are merged: #2234 (the handover builder), #2237 (the source's lifecycle
  gate), #2250 (the other Managed pumps take the gate too), #2253 (handover files in the trash),
  #2255 (the move endpoint), #2260 (the Console dialog and the session-row wire) and #2264 (the
  limit-banner trigger). The change that appends this note also renders the inputs a stop discarded
  under "where it stopped" and writes the file's own headings and labels in the display language
  (the Agent's saved language, as the Markdown export reads it); #2233 stays open for the
  read-only tool lists, which need measured data.
- **Checked live.** The owner moved a claude session to opencode on a real Agent and Console. The
  target read the handover file from its first prompt and continued; the source stopped and showed
  where it went, with "resume here anyway"; the picker showed the usage marks. No problem was
  reported.
- **Not yet checked live:** the rollback and recovery paths; a source that really hit its limit and
  was moved from the banner; a chain A→B→C; trashing and restoring a target; cursor and kiro as
  targets (#2252).

## Addendum (2026-10-20) — kiro is a target, cursor stays off (#2252)

- **Measured.** Kiro, Managed, default permission mode (kiro-cli 2.32.0, claude-haiku-4.5): a
  first-turn message naming a file under `~/.af-work/<its own name>/`, outside its working
  folder, made it read the file and return the file's random token, with no approval prompt.
  Cursor through the bare CLI (`cursor-agent -p --plan --trust --model auto`, 2026.10.18): it read
  a file outside its cwd under `~/.af-work/` and returned the token, with no approval.
- **Not measured.** Kiro Terminal (TUI). Cursor Managed: the driver run answered "Upgrade your
  plan to continue" on its first turn (account plan), so the driver's trust handling and flags are
  unmeasured. Cursor Terminal.
- **Inference, not measurement: kiro Terminal.** The target list is per kind, so a move to kiro
  may also start the Terminal driver. Managed was measured in the default mode; the exact
  permission flag (whether `--trust-all-tools` was passed) was not independently recorded, so
  the result is not tied to the "skip permission confirmation" setting being off. With that
  setting off, both kiro drivers drop `--trust-all-tools`, and whether either reads an outside
  file unprompted then is unmeasured. The Terminal chat is only expected to behave like Managed
  because the two share kiro's tool permissions; that is inference, not a run. If it does prompt,
  the prompt appears in the target's own view for the user to answer; the first prompt waits
  there.
- **Decision.** Kiro is now a move target. Cursor stays off, reported as `not_measured`, until it
  can be measured through the Managed driver. Follow-ups: #2252.

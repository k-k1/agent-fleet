# 0039. Point at a conversation's fork point with a kind-specific opaque anchor, and allow jsonl surgery for claude only

English | [日本語](0039-fork-at-message.ja.md)

- Status: adopted; P1–P5 implemented (the contract plus four kinds [claude/codex/opencode/copilot]
  plus the Console route plus "carry on from here". All four verified against the real CLIs)
- See also: [55-fork-at-message.md](../log/55-fork-at-message.md) /
  [history/fork-from-chat.md](../log/fork-from-chat.md) (the old judgement this ADR replaces) /
  [27-agent-managed-driver.md](../log/27-agent-managed-driver.md) /
  [0029-usage-accounting.md](0029-usage-accounting.md) (how the `handoff` origin is handled)

## Context

We want to select a past user message in the mirror and start a new session carrying the context up
to that point.

The existing `POST /sessions/{name}/fork` can only fork a whole conversation, and it is not even
called from the Console (the mirror's fork button was folded into the handoff modal, which is a
different thing — *an LLM summary*). Forking at a point was rejected once in 2026-06 on the grounds
of "Claude Code does not support it / the only anchor is `idx` / modifying the jsonl is fragile".

Measurements in 2026-08 changed that premise. codex has `lastTurnId` (inclusive) on the app-server's
`thread/fork`, and opencode has `messageID` (exclusive) on `POST /session/{id}/fork` — both
**officially**. claude has a hidden flag `--resume-session-at <message id>`, but it is **print mode
only** and AF, which only starts the TUI, cannot use it. On the other hand, we measured that the
difference between the jsonl claude's own fork writes and the original file is **only the
`sessionId` field** (`uuid` and `parentUuid` stay as they were). The details and how to reproduce it
are in docs/55 §55.2 / §55.11.

## Decision

1. **The fork point is named by a kind-specific opaque ID (an anchor).** `AnchorID` is added to
   `transcript.Turn` and holds: claude = the message uuid, codex = the turn id, opencode = the
   message id, copilot = the event id. The transcript line number `Idx` is not used as a permanent
   anchor, because compaction moves it.
2. **The Console does not interpret the anchor.** It just hands the string it received straight back
   to the fork API; kind-specific knowledge, including absorbing the inclusive/exclusive difference,
   is confined to the Agent side.
3. **Do not fork from a turn with an empty anchor.** Do not substitute a guess from `Idx`. Not
   offering the fork affordance is the correct behaviour.
4. **v1's semantics are fixed to one thing: "up to just before the selected user message".** The
   fork opens in the state just before that message was typed, and the original message text goes
   into the composer as a draft (it is not sent). "Include that message and its reply" is an option
   for v1.1, expressed by shifting the anchor one step later.
   *v1.1 as implemented (adopted)*: the resolver absorbs it via `agents.ForkPoint{Anchor, Include}`,
   and the meaning of `Meta.ForkAt` (keep everything before this value) does not change. The default
   is "redo" — the most common use is right after taking a wrong direction, and there you want to
   discard the fork point's message too. Injecting the draft is limited to "redo", because with
   "carry on from here" the message remains in the fork and would appear twice.
   Two asymmetries remain, and neither is hidden because both are the engines' own constraints:
   **"carry on from here" on the last exchange is a whole-conversation fork** (it resolves to `""`
   and goes down the existing path), and **"redo" on the first exchange cannot be expressed on codex
   alone** (an empty `lastTurnId` there means "the whole thing", so we refuse).
5. **The fork API is widened with an optional body `{at}`.** Omitting `at` is the old
   whole-conversation fork, preserving backward compatibility. An anchor that cannot be resolved
   **fails with a 4xx** (it does not fall back to a whole-conversation fork).
   *A correction made during implementation*: the error code was split in two by meaning —
   `fork_at_unsupported` (this kind or execution method simply does not have point forking, i.e. the
   route should not have been offered) and `fork_bad_anchor` (the feature exists but this fork point
   cannot be used). The former wants localised boilerplate, the latter is a state problem, and the
   user's next action differs (give up versus reload). The `draft` flag that returned the fork
   point's original message text was dropped (the Console already uses it for rendering, so there is
   no point returning it from the server).
6. **codex and opencode are implemented with the official parameters only.** No unofficial rollout or
   store manipulation.
7. **claude is allowed jsonl surgery.** Copy the original jsonl, take the lines before the cut point,
   rewrite **only** `sessionId`, and place it as the new sid's file. **No other field is touched.**
   `buildProgram` resumes if its own jsonl exists, so the launch side needs no changes.
8. **The cut point is restricted to a genuine user prompt line, and failing the check is a
   failure.** Tool results also arrive as `type:"user"`, so they are excluded from the candidates,
   and we confirm that no `tool_use` is left without its `tool_result` after the cut. On failure we
   suggest a whole-conversation fork and **never silently fork the whole thing**.
9. **claude's surgery rides the drift detection for CLI pin updates.** "A truncated jsonl can be
   resumed, and only the truncated history is visible" is confirmed against the real CLI on every
   version. *Implementation*: `TestContractLiveClaudeForkAt` (the `clicontract` tag, riding on
   `claude-tui-contract.yml`).
10. **Whether an execution method is possible is answered by the kind (there is no global managed
    condition).** For opencode and codex the only place to pass a fork point is the runtime API, so
    managed is required; but claude has no managed driver at all and cuts its own transcript, so the
    TUI is the only route. Requiring managed uniformly in the handler would reject claude forever, so
    the resolver returns `ErrForkAtRoute`, and the Console carries the same distinction in
    `caps.forkAtManagedOnly`.
    *Ordering*: the fork point is resolved **before** `ForkSource`. Telling a session on a different
    route "there is no forkable conversation yet" only makes the user try to add more conversation,
    which does not fix it.
11. **The kinds covered are claude / codex / opencode / copilot.** kiro is out of scope because the
    CLI assigns the ID, and agy because of its SQLite store; `Caps.CanForkAt` stays false for both.
    *P4b investigation (2026-08-09)*: **cursor is confirmed impossible** — its transcript lines carry
    only `{role, message.content}` with no `uuid`/`parentUuid`/`sessionId` (the parser does not read
    them either), so there is no permanent ID usable as a fork point. "Claude Code-compatible JSONL"
    means the shape is similar, not that the identifiers are the same. Substituting a line number was
    already rejected by decision 1. **copilot was implemented** (events.jsonl is what it is restored
    from — even leaving `session.db` unmodified, the truncated events.jsonl determined the context).
    The only difference is that the unit is a whole directory; otherwise it is the same shape as
    claude. **`session.db` is copied and not touched**: the moment you start rewriting a file whose
    meaning you do not know, this surgery changes from "rewriting something readable into the same
    shape" into "owning another product's internal state". The index (`session-store.db`) is not
    written either — resuming an unregistered session-state makes copilot register it itself
    (measured). The material is prepared on **both** the TUI and managed routes (forget managed and
    there is no sid, so it falls to `session/new` and the fork opens as an empty conversation).
12. **A session grown from a fork inherits the same `handoff` origin as the existing fork.** It is
    not mixed into "the number a person opened" (ADR 0029 §6).
13. **Forking and handoff coexist as separate features.** Handoff = summarise and pass to a different
    agent; fork = duplicate the context as it is on the same agent. The UI makes this difference
    explicit.

## Options not taken

### Prepare claude's material with the official flag (`-p --resume-session-at`) too

If the first instruction were made a required input at fork time, we could officially produce a
truncated jsonl with
`claude -p --resume <src> --resume-session-at <uuid> --fork-session --session-id <new> "<instruction>"`
(measured to work). The advantage is avoiding unofficial writes entirely.

The reason not to take it is that **that one turn runs in full, headlessly**. Tools run, it takes
minutes, and it can fail. What the user should see as the result of "fork" is a new session where
nothing has been instructed yet; running a full turn behind the scenes and then opening it is a
different feature. On top of that, `--resume-session-at` is itself a hidden flag absent from
`--help`, so "official, therefore safe" does not differ from the surgery by as much as it sounds.
**The surgery is nothing but selecting lines and rewriting `sessionId`**, and we have measured that
the output has the same shape as the official fork's.

This option is not discarded, though; it stays in docs/55 §55.5 as an alternative route — an escape
hatch for when the drift detection breaks.

### Use the transcript line number `idx` as the anchor

It already reaches the Console and needs almost no extra implementation, but compaction moves it.
For a fork — a one-off but recoverable operation — **the user cannot notice** having forked from the
wrong point (plausible-looking history comes with it). We do not adopt an anchor that is silently
wrong.

### Substitute a rewind within the same session (opencode's `revert`, claude's `/rewind`)

Either the original conversation is lost, or the original session's state is changed. It does not
satisfy the requirement itself, which is "try a different direction while keeping the original".
Rewinding is a different feature and out of scope for this ADR.

### Substitute the handoff's summary

Already implemented, at no extra cost. But a summary drops context and loses the wording of the
original instruction. For "start again from that instruction", what was lost is often exactly the
reason for forking.

### Absorb the inclusive/exclusive difference per kind on the Console side

codex is inclusive, opencode is exclusive, and claude wants "the uuid of the preceding line" — three
different things. Putting that in the Console breaks the front end every time a kind is added.
Confine it to `ResolveForkAt` on the Agent side (as the `agents.Agent` split intends).

### Extend `agents.Forker` to carry point forking

Adding a method to the existing `Forker` breaks compilation for kinds that implement only
whole-conversation forking. `ForkAtResolver` is a separate interface, and whether it is implemented
is expressed by `Caps.CanForkAt`.

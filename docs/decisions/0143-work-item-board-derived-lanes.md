# 0143. A work-item board: a card is a work item, a lane is derived at read time from its sessions and pull requests, and nothing about a lane is stored

English | [日本語](0143-work-item-board-derived-lanes.ja.md)

- Status: **proposed** (2026-11-24). A study for #3135; nothing is built. The decisions below are
  recommendations, and the owner confirms them through the open questions (#5051). Nothing was
  measured: every `file:line` was read at `e13c56ec5`, no tracker or live fleet was queried, and the
  cost figures in decision 5 are bounds read from code, not observations.
- Follow-ups: #5051 (open questions), #5052 (PR facts on links), #5053 (observe without notifying),
  #5054 (the board itself).
- Related: [0061](0061-work-item-inbox.md) (decisions 1, 2, 6, 8, 9, 15, and the 2026-10-25 note on
  the human-gated merge) / [0078](0078-sessions-overview-pane.md) (decisions 1, 5, 11) /
  [0096](0096-fleet-session-graph.md) / [0073](0073-session-spawned-sessions.md) /
  [0055](0055-idle-stop-and-carried-interactions.md) / [0097](0097-session-retention.md) /
  [0104](0104-long-lived-member-workspace.md) / [0111](0111-member-collaboration.md) (the tenant view,
  #3136, keeps its rules). Issues: #3134 (links), #3129 (PR feedback), #3131 (merge lifecycle),
  #1086 (session lanes).

## Context

A member's work is a ticket, a session and a pull request, and the Console shows them in three places:
the work-item rail (ADR 0061), the session list and overview (ADR 0078), and a PR badge on a session
row. Nothing says "this ticket is waiting for review" or "this ticket's session needs you". #3134 and
#3129 have since landed the two inputs a board needs. What the tree has now, and what it lacks:

1. **Links exist and are mirrored.** A session holds many links with a role (`started`, `created`,
   `working`, `reviewing`, `watching`; `workspace/agent/internal/session/links.go:21-25`). The CP
   mirrors them into `session_work_link` (SQLite 0118/0119, Postgres 0103/0104), so they are
   readable while the Workspace is stopped.
2. **The mirror does not hold activity.** `SessionRow` carries `State` (`running` | `stopped`) and
   `Carried` (an interaction still waiting when the session was folded away;
   `control-plane/internal/store/store_session_index.go:11-19`). Working, question, plan, permission
   and blocked exist only in the live Agent list, and are classified in the Console by `stateInfo()`
   (`console/src/lib/sessionview.ts:149`), the one derivation ADR 0078 decision 5 says must not be
   duplicated.
3. **A link carries a PR's state and nothing else.** `WorkItemLink.State` is `open`, `draft`,
   `merged` or `closed`. CI, changes-requested and conflict are on the wire only for the PR of the
   session's *current branch* (`branchpr.PR`, `ChangesRequested`/`Conflict`, `branchpr.go:59-60`).
   `Ready()` (`branchpr.go:118`), `ReviewDecision` and `Mergeable` are `json:"-"`: the verdict that
   decides "ready to merge" never leaves the Agent.
4. **A PR a session has left is not read again unless a switch is on.** The Agent reads a link's PR
   state "only while a session is on its branch" (`links.go:57`). The only code that reads held PR
   links by number is the PR-feedback tick, and it returns first when PR feedback is off
   (`sessionx/pr_feedback.go:503-510`), which is the default. A merged PR would stay `open` on its
   link for good.
5. **The inbox is stale while stopped, by design, and omits done items by default.** The CP draws its
   cache with "last fetched" times (ADR 0061 decisions 1, 15). The default query is "assigned to me
   and not done" (decision 8), so an item that is done in the tracker leaves the cache. `Done`
   cannot be read from the cache alone.
6. **There are two ledgers.** `work_item_session` is written only by the Console's launch
   (ADR 0061 decision 9). `session_work_link` is written by the Agent for every role. Only the
   second sees a PR a session opened or a child that watches a ticket.

The question is how a card and a lane are defined so that they follow the sources of truth, and what
has to be added so that the sources say enough.

## Decisions

### 1. A card is one work item. Sessions and PRs attach to it through links.

- **Identity** is `(provider, case-folded key)`, the same fold `session_work_link` uses.
- **Attachment.** A session is attached to every non-PR item it holds a link to with a *driving
  role*: `started`, `working` or `created`. A PR link of that session is attached to the same cards.
  A PR link whose session holds no driving item link is its own card (an inbox PR row, or a PR
  opened with no ticket). `watching` and `reviewing` links do not attach a session to a card: they
  show as a chip on the card of the item they name, and they do not move its lane (open question 3).
- **Orphans.** A session with no driving link is its own card, keyed by workspace and session name.
- **A card exists** when at least one of these holds: an inbox row, a driving session, a PR link. A
  `watching` link to an item that is in none of those creates no card. The board stores no card.

### 2. A lane is a pure function of a card's facts. First match wins.

| # | Lane | A card is here when |
|---|------|---------------------|
| 1 | **Done** | no open PR, and (a PR is merged, or the tracker says done) |
| 2 | **Needs you** | a driving session is alive in question, plan, permission or blocked, or is stopped with a carried interaction; or an open PR has failed CI, changes requested or a conflict |
| 3 | **Ready to merge** | at least one open PR, none a draft, and every one `ready` |
| 4 | **In review** | any open PR that is not a draft |
| 5 | **In progress** | a driving session that is not archived, or an open draft PR |
| 6 | **To do** | an inbox row, no driving session, no PR |

- **Attention beats PR state beats activity**, as the issue asks. The two rules that need a reason:
  - **Done beats Needs you.** A merged card with a session still asking a question stays in Done.
    The question is on the session's own chip and in the session list; a merged card must not keep
    a lane open that the tracker has closed.
  - **Unknown is never good.** A PR with no observation has empty `checks`, and an empty value is not
    green (the comment on `branchpr.PR` already says so). It is not `ready`, so it lands in In review.
- **`limited` is not attention.** A session waiting for a usage window to reset (`limited`)
  needs nobody; it counts as In progress. A session in `blocked` is attention, as `stateInfo` says.
- **A PR closed unmerged** leaves the card's PR set; the card falls to In progress or To do on the
  other facts.
- **Tracker state.** Done by the tracker reads the provider-independent state ADR 0061 decision 9.5
  fixed (a Jira status *category*, not a name). An item not in the inbox has no known tracker state,
  and unknown is not done.
- **A lane is never a stored value.** There is no `lane` column, no per-card override and no history.
  Recomputing it on every read is what keeps it from disagreeing with the session and the PR.

### 3. Derive in the Console, from data it already holds, with one function.

`laneOf(card)` is a pure function in the Console over the session list (live, or mirrored while
stopped, each with its links) and the inbox rows. The attention test calls the same classification
`stateInfo()` uses, via a small shared helper, so the board and the list can never name the same
session differently (ADR 0078 decision 5). No new endpoint and no CP-side derivation.

The CP cannot derive lanes by itself yet: it holds no activity (fact 2), and giving it activity means
a mirror write on every state change. That cost buys the tenant overview (#3136) and nothing a member
needs now. When #3136 needs server-side lanes, it can reuse the function's rules, not its code.

### 4. The PR facts have to reach the link. This is the one build dependency.

Lanes 2, 3 and 4 need, per PR link, what facts 3 and 4 show is missing. Add to a PR link on the
Agent wire a small facts object: `checks` (`success` | `failure` | `pending` | empty),
`changesRequested`, `conflict`, `ready` and `observedAt`. Mirror them into `session_work_link` as
additive, defaulted columns (#5052).

- **`ready` is the Agent's own `PR.Ready()` verdict**, sent as a boolean. The Console must not rebuild
  it from checks, review and mergeable: that is a second definition of "ready to merge", and the
  Agent already uses the first for the `pr.ready` notice and the merge lifecycle.
- **Only enumerations and a timestamp.** No check names, thread text or reviewer names (ADR 0061
  decision 2). The failing check *names* stay Agent-private, as `branchpr.PR` keeps them.
- The migration adds columns with defaults and nothing is dropped or rewritten, so it ships like
  0119 did.

### 5. Observing a held PR is separate from notifying about it.

Today one switch gates both (fact 4). Split them (#5053): the read of held PR links by number runs
whenever the Workspace runs, for sessions with a driving PR link, under the caps the PR-feedback tick
already has (`prFeedbackMaxLinks`, the cache's TTLs); injecting a notice stays opt-in. Without this,
the board shows merged PRs as In review.

The cost is bounded by those caps per session and by the cache TTL per PR; whether to run it always or
only after the board has been opened is open question 4. Nothing here reads while the Workspace is
stopped (ADR 0055): see decision 6.

### 6. A stopped Workspace draws the same board, labelled with when it was true.

The mirror gives links, link state and `carried`; the facts of decision 4 give PR conditions as of
`observedAt`; the inbox cache gives To do and tracker state as of its fetch time.

- Every card shows **"as of hh:mm"**: the oldest `observedAt` among the facts that put it in its lane
  (ADR 0061 decision 1's rule, "each row carries last fetched"). When no fact has a time, nothing is
  shown, never an invented one (ADR 0078 decision 11).
- Attention while stopped is only `carried`. A session that was working when the Workspace stopped is
  stopped, so its card reads In progress with a stopped chip, not Needs you.
- **Age never orders cards or moves a lane.** It is shown, not used.

### 7. Actions reuse what exists. The board adds no write.

Start a session from a To do card (ADR 0061 decision 20's launch), open a session beside the board
(ADR 0078 decision 3), open the PR (decision 24.1), link and unlink (#3134's Console paths), and the
detail modal's human-gated Merge button (the 2026-10-25 note) on a Ready to merge card.

- **No drag.** A lane is derived, so a card cannot be moved: nothing could hold the move.
- **No tracker write-back.** Starting a session does not move a Jira ticket to In Progress, and a lane
  change writes nothing. ADR 0061 decision 6 stands: any write is explicit, per action, and never
  reachable by an agent. A transition button, if wanted, is its own ADR.

### 8. Where it lives: a second view of the sessions overview, not a new pane kind.

The overview pane (ADR 0078) already owns cards, `stateInfo`, the session menu and the two entry
points. The board is a *group by* choice in its head: repository (today) or lane. A work-item card is
a different card, but it reuses the chip, the menu and the open-beside behaviour. ADR 0049 decision 4
says not to mint a pane kind lightly, and ADR 0078 had to argue its exception; this has none. The alternative is open question 2.

### 9. Personal first; the tenant view keeps its own rules.

The board shows one member's own workspace, sessions and inbox. Nothing crosses members. #3136 owns the
tenant overview under ADR 0111's opt-in, reduced-on-the-server rules, and it may reuse the lane rules
(decision 3) without reusing this view.

## Phases

Each part leaves `main` releasable alone.

1. **P1 — facts (#5052, #5053).** Additive wire and columns; observation split. Nothing visible
   changes except that a session row's link states stay correct.
2. **P2 — the function (#5054, part 1).** `laneOf` and card assembly with tests on fixtures that cover
   every row of the table and each precedence rule. Unreachable from the UI.
3. **P3 — the view (#5054, part 2).** The group-by-lane view, "as of" times and the actions of
   decision 7.

## Open questions

These are the owner's, and are collected on #5051. Each has a recommendation above.

1. **Fold #1086.** Session lanes with a manual override that clears on a state change would need a
   stored pin and the lane it replaced: the opposite of decision 2. Recommended: close #1086 into this
   study, and add a session-only override later if sessions with no ticket need one.
2. **A second view or its own pane** (decision 8).
3. **Do `watching` and `reviewing` move a card** (decision 1)? Recommended: no. The alternative is
   that `reviewing` counts on the card of the PR it reviews only.
4. **Always observe, or only once the board has been opened** (decision 5).
5. **Paused sessions.** A stopped, unarchived session keeps its card In progress until ADR 0097's
   retention archives it. A months-old paused session therefore holds a card. This ADR does not add a
   threshold, because a number chosen without data would decide what the member sees. Look at real
   boards after P3 and decide then.

## Alternatives set aside

- **Stored lanes with drag, as a kanban.** A stored lane drifts from the session and the PR it
  summarises, and ADR 0078 decision 11 already chose to show nothing over a value nobody can vouch for. It also needs a
  rule for when a human move loses to an observed fact, which is #1086's override problem again.
- **Derive in the CP.** Needs activity mirrored on every state change (decision 3). Revisit with #3136.
- **Rebuild "ready" in the Console** from checks, review and mergeable. A second definition that will
  drift from `PR.Ready()` (decision 4).
- **A card per session, with items as chips.** That is the overview today (ADR 0078). It cannot say
  that a ticket with three sessions and two PRs is waiting for review.
- **A card per PR.** A ticket with no PR yet would have no card, and To do could not exist.
- **Source cards from `work_item_session`.** It records Console launches only (fact 6), so a PR a
  session opened and a child's watch would be missing. It stays the duplicate-start guard of ADR 0061
  decision 9.
- **Write the lane back to the tracker.** Decision 7.

## Consequences

- A lane always agrees with the session chip and the PR badge, because it is computed from the same
  inputs on every read. It is only as fresh as the last observation, and says so.
- The board is wrong in a known way until P1 ships: without decisions 4 and 5, a PR that left its
  session's branch stays In review after a merge. P3 must not ship before P1.
- Adding a lane means editing one function and one table.
- One more field set rides every session listing's links, bounded to enumerations and a timestamp.

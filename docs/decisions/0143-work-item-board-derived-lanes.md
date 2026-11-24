# 0143. A work-item board: a card is a work item, a lane is derived at read time from its sessions and pull requests, and nothing about a lane is stored

English | [日本語](0143-work-item-board-derived-lanes.ja.md)

- Status: **proposed** (2026-11-24). A study for #3135; nothing is built. The decisions below are
  recommendations, and the owner confirms them through the open questions (#5051). Nothing was
  measured: every `file:line` was read at `e13c56ec5`, no tracker or live fleet was queried, and the
  cost figures in decision 5 are bounds read from code, not observations.
- Follow-ups: #5051 (open questions), #5052 (PR facts on links), #5053 (observe without notifying),
  #5054 (the board itself), #5062 (link kind).
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
   `working`, `reviewing`, `watching`; `workspace/agent/internal/session/links.go:21-25`). The Agent
   returns them on its live session list; the CP writes them into `session_work_link` (SQLite
   0118/0119, Postgres 0103/0104) in the same transaction as the session rows
   (`ReplaceSessions`), so they are readable while the Workspace is stopped. A link the Agent has just
   made is in that table only after the CP's next mirror of the list.
2. **The mirror does not hold activity.** Of what `SessionRow` carries (identity, origin, links and more;
   `control-plane/internal/store/store_session_index.go:11-32`), the only activity information is
   `State` (`running` | `stopped`) and `Carried` (an interaction still waiting when the session was
   folded away). Working, question, plan, permission and blocked exist only in the live Agent list, and are classified in the Console by `stateInfo()`
   (`console/src/lib/sessionview.ts:149`), the one derivation ADR 0078 decision 5 says must not be
   duplicated.
3. **A link carries a PR's state and nothing else.** `WorkItemLink.State` is `open`, `draft`,
   `merged` or `closed`. For a session's PR, CI, changes-requested and conflict are on the wire only
   for the PR of its *current branch* (`branchpr.PR`, `ChangesRequested`/`Conflict`,
   `branchpr.go:59-60`). An open PR that is a row of the member's inbox has its own route: the Agent
   fills `Checks` and `Mergeable` for the row and the CP serves them (`work_item_cache.checks` and
   `.mergeable`, migration 0074; `workitems_prstatus.go:34-45`). `ReviewDecision` and `Mergeable` on
   `branchpr.PR` are `json:"-"`, and `Ready()` (`branchpr.go:118`) is a method over them: the Agent
   uses it for its own `pr.ready` notice, but no list the Console reads has a `ready` boolean.
4. **A PR a session has left is not written back to its link unless a switch is on.** There are three
   paths. The PR of the session's current branch is read with the session list and written to the
   link (`sessionx/session_work_links.go:181-225`). A held link off the branch is read by number and
   written back by the PR-feedback tick (reads `pr_feedback.go:242-260`, write-back
   `pr_feedback.go:603-635`), and the tick returns first when PR feedback is off
   (`pr_feedback.go:503-511`), which is the default. The message queue's PR snapshot also reads held
   links by number (`sessionx/queue_pr_snapshot.go`), but only to answer that queue's delivery, and
   it writes nothing back. A merged PR a session has left would stay `open` on its link for good.
5. **The inbox is stale while stopped, by design, and absence from it proves nothing.** The CP serves
   its cache with fetch times (ADR 0061 decisions 1, 15); the rail draws one aggregate time across
   the enabled queries (`WorkItemsSection.tsx:294-296,326-332`), not one per row. A row leaves the cache when the
   query that returned it next succeeds and no longer returns it. The default query differs by
   provider (a Jira query excludes done items; a GitHub one is `is:open` and `involves:@me`; a
   Bitbucket one starts from reviews addressed to the member), so a missing row is not evidence that
   an item is done. For a pull request the tracker state is coarser still: GitHub maps closed to
   `done` whether merged or not (`workitems.go:460-472`), and a draft is `in_progress`.
6. **There are two ledgers.** `work_item_session` is written only by the Console's launch
   (ADR 0061 decision 9). `session_work_link` carries the Agent's links of every role, written by the
   CP's mirror. Only the second sees a PR a session opened or a child that watches a ticket.

The question is how a card and a lane are defined so that they follow the sources of truth, and what
has to be added so that the sources say enough.

## Decisions

### 1. A card is one work item. Sessions and PRs attach to it through links.

- **Identity** is `(provider, case-folded key)`, the same fold `session_work_link` uses.
- **Driving role.** `started`, `working` or `created`. Only a link with a driving role attaches
  anything to a card or moves a lane; `watching` and `reviewing` links show as a chip on the card of
  the item they name and do nothing else (open question 3).
- **Attachment.** A session is attached to every non-PR item it holds a driving link to. A PR link
  attaches to those same cards only when the PR link itself has a driving role: a session that
  drives ticket A and merely watches PR B does not let B's failing CI move A. A session whose
  driving link is a PR, with no driving ticket, is attached to that PR's own card. An inbox PR row,
  or a PR opened with no ticket, is its own card.
- **Orphans.** A session with no driving link (including one that only watches or reviews) is its own
  card, keyed by workspace and session name, and counts as its own driving session in decision 2.
- **A card exists** when at least one of these holds: an inbox row that is not an ended pull request
  (`done` for a PR row means merged or closed, fact 5); a driving session; a PR link with a driving
  role that is open or merged. A PR that closed unmerged, or an ended PR row with no `merged` link,
  creates no card by itself, and a `watching` link to an item that is in none of those creates none
  either. A driving session of such a PR still has its own card (or its ticket's), so it is not
  lost. The board stores no card.
- **One PR, one entry.** A PR is identified by `(provider, case-folded key)`. When the same PR is an
  inbox row and a link, or the link of several sessions, merge them: each fact comes from the
  freshest observation (`observedAt`, or the row's fetch time), `merged` is final and wins over any
  older `open`, and a fact only one source has (checks from the cache, the review decision from the
  Agent) is taken from that source, never defaulted. The same PR is never counted as two open PRs.
  A derived verdict does not outlive the facts it was built from: a link's `ready` is not used while
  a newer observation of the same PR (an inbox row's checks or mergeable) says it is not green or not
  mergeable, because the Console must not recompute `ready` and so cannot repair it, only ignore it.

### 2. A lane is a pure function of a card's facts. First match wins.

| # | Lane | A card is here when |
|---|------|---------------------|
| 1 | **Done** | no open PR, and one of: the tracker says the ticket is done; a PR is merged and no driving session is alive or holds a carried interaction; the card is a PR whose own state is merged |
| 2 | **Needs you** | a driving session is in an attention state (below); or an open PR has failed CI, changes requested or a conflict |
| 3 | **Ready to merge** | at least one open PR, none a draft, and every one `ready` |
| 4 | **In review** | any open PR that is not a draft |
| 5 | **In progress** | a driving session that is not archived, or an open draft PR |
| 6 | **To do** | an inbox row, no driving session, no PR |

"Open" means not merged and not closed, so a draft and a PR whose state was never observed are open.
A PR-only card (decision 1) takes its PR's own state, so a merged PR is Done for it whatever its
session does.

- **Attention beats PR state beats activity**, as the issue asks. The rules that need a reason:
  - **Done by the tracker beats Needs you, once no PR is open.** A ticket the tracker calls done, with
    no open PR, stays in Done even if a session is still asking a question; with an open PR it is
    judged by rows 2 to 5 like any other card: the question is on the session's own chip and in the session
    list, and the card must not keep a lane open that the tracker has closed.
  - **A merged PR alone does not close a ticket while a session is alive.** A ticket with PR A merged
    and another session now asking a question about PR B is not finished: the tracker has not said so
    and the live session is more work. A session folded away with a carried interaction is the same
    unfinished work, so it holds the card too; without any live or carrying driving session, a
    merged PR and no open one is Done. A PR-only card has no ticket and is Done when its PR is
    merged.
  - **Unknown is never good.** An open, non-draft PR with no observation has empty `checks`, and an
    empty value is not green (the comment on `branchpr.PR` already says so). It is not `ready`, so
    unless a higher row matches it lands in In review. A PR whose state was never observed counts
    as open, which is the known stale case decisions 4 and 5 remove.
- **What attention is** is not listed here. A session is in attention when `stateInfo()` gives it the
  `question` colour class and not `limited`. Live, that covers question, plan, permission, blocked,
  auth, spend limit and input not delivered; stopped, it covers a carried interaction, and not a
  session whose folder is gone, which `stateInfo` shows as dead and which cannot be answered. That
  exclusion holds where the list says so (`resumable: false` on the live list). The stopped mirror
  does not know whether the folder survives and serves such a session as resumable
  (`workspace_handlers.go:1143-1214`), so a stopped Workspace can still show it in Needs you. The board calls the shared helper, so it follows `stateInfo` when that
  changes. `limited` (waiting for a usage window to reset) needs nobody and counts as In progress.
- **A PR closed unmerged** leaves the card's PR set, and the card is judged again from the top of the
  table on what is left. A PR-only card with nothing else disappears (decision 1).
- **Tracker state.** Done by the tracker reads the provider-independent state ADR 0061 decision 9.5
  fixed (a Jira status *category*, not a name), and only for a ticket or issue. An item not in the
  inbox has no known tracker state, and unknown is not done. An inbox *pull request* row is read as
  follows: `open` is an open PR, `in_progress` is a draft, and `done` only means ended, merged or
  not (fact 5), so it is not evidence of Done. A PR-only card is Done only when a session's link
  says `merged`; an ended PR row with no such link has no card.
- **Inbox pull requests supply facts too.** An open PR that is only an inbox row, with no session,
  takes its checks and mergeable from the cache (fact 3), so a failing CI on a review request lands
  in Needs you and an open PR with none of it in In review. It has no review decision there, so it
  can never be `ready` from the cache alone.
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
  decision 2). The failing check *names* stay off the links and lists, as `branchpr.PR` keeps them; the
  PR-feedback notice still delivers them to the session it is for.
- The migration adds columns with defaults and nothing is dropped or rewritten, so it ships like
  0119 did.

### 5. Observing a held PR is separate from notifying about it.

Today one switch gates both (fact 4). Split them (#5053): the read of held PR links by number runs
whenever the Workspace runs, for sessions with a driving PR link, under the caps the PR-feedback tick
already has (`prFeedbackMaxLinks`, the cache's TTLs); injecting a notice stays opt-in. Without this,
the board shows merged PRs as In review.

The observation the tick does today has two gaps the board cannot live with
(`pr_feedback.go:168-189`). It keeps the first `prFeedbackMaxLinks` links by role and age per
session, so a sixth open PR is never read while the first five stay open. And it skips a link once
it is `merged` or `closed`, so a PR closed and then reopened is never read again. The observation
must therefore rotate through links within the cap, ordered by the time of the last *attempt* rather
than the last success, so a link whose read keeps failing (a lost permission, say) cannot hold the
front forever; and re-read a `closed` link slowly (`merged` is final). #5053 carries both.

The cost is bounded by those caps per session and by the cache TTL per PR; whether to run it always or
only after the board has been opened is open question 4. Nothing here reads while the Workspace is
stopped (ADR 0055): see decision 6.

### 6. A stopped Workspace draws the same board, labelled with when it was true.

The mirror gives links, link state and `carried`; the facts of decision 4 give PR conditions as of
`observedAt`; the inbox cache gives To do and tracker state as of its fetch time.

- Every card shows **"as of hh:mm"**: the oldest time among the facts that put it in its lane. A fact
  from a link has its `observedAt`; a fact from an inbox row has that row's fetch time, which the CP
  stores per row but the rail does not show (it draws one aggregate), so the board needs the CP to
  serve it per row (#5054). This is ADR 0061 decision 1's rule, "each row carries last fetched", at
  card level. When no fact has a time, nothing is shown, never an invented one (ADR 0078
  decision 11).
- Attention that comes from a *session* while stopped is only `carried`. The dead-folder exclusion of
  decision 2 cannot apply, because the mirror cannot tell. A session that was working when the
  Workspace stopped is stopped, so it gives its card In progress with a stopped chip, not Needs you,
  when no higher row of the table matches. Attention that comes from a *PR* stays, as the PR facts last recorded with their `observedAt`:
  a card with a failing CI the cache or the link recorded before the stop is still in Needs you, and
  says as of when.
- **Age never orders cards or moves a lane.** It is shown, not used.

### 7. Actions reuse what exists. The board adds no write.

Start a session from a To do card (ADR 0061 decision 20's launch), open a session beside the board
(ADR 0078 decision 3), open the PR (decision 24.1), link and unlink (#3134's Console paths), and the
detail modal's human-gated Merge button (the 2026-10-25 note) on a Ready to merge card. The lane
and the button are not the same test: `ready` is the Agent's approximation that the notice and the
lane use, while the modal also disables Merge for a PR that is behind its base, and checks the
member's permission (`console/src/features/workitems/read.ts:323-379`). A Ready to merge card may
therefore open a modal whose Merge button is disabled with a reason, or absent altogether (no
permission, no head commit or no allowed merge method give no button and no reason), as the modal
already decides.

- **No drag.** A lane is derived, so a card cannot be moved: nothing could hold the move.
- **No tracker write-back.** Starting a session does not move a Jira ticket to In Progress, and a lane
  change writes nothing. ADR 0061 decision 6 stands: any write is explicit, per action, and never
  reachable by an agent. A transition button, if wanted, is its own ADR.

### 8. Where it lives: a second view of the sessions overview, not a new pane kind.

The overview pane (ADR 0078) already owns cards, `stateInfo`, the session menu and the two on-screen
buttons that open it. The board is a *group by* choice in its head: repository (today) or lane. A work-item card is
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
   retention archives it. A months-old paused session therefore holds a card. The opposite error is
   in the table too: a stopped session with no carried interaction cannot be told from a finished
   one, so a ticket whose only PR is merged reads Done even if that session was only paused before
   opening the next PR. The tracker's own done is the exact signal, and a Done card keeps the session's
   stopped chip, so the member sees it. This ADR does not add a
   threshold, because a number chosen without data would decide what the member sees. Look at real
   boards after P3 and decide then.
6. **No tracker write-back** (decision 7). Confirm that no lane change writes to a tracker.

## Known limits

- **A link whose kind is unknown.** A `started` link from a launch carries no URL or kind, and its
  state is empty until the PR's branch is observed, so the Agent cannot tell a PR key from an issue
  key of the same shape (`links.go:49-59`, `pr_feedback.go:146-176`). The board treats such a link as
  the item it names, not as a PR, and it is not observed by number. This is no worse than the tree
  today; #5062 records the kind.
- **Not measured.** Every claim about cost and staleness above is read from code.

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

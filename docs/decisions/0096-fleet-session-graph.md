# 0096. The fleet's session graph is one figure — lanes are sessions, the horizontal axis is time — and only lineage is kept in a permanent ledger

English | [日本語](0096-fleet-session-graph.ja.md)

- Status: **adopted, implemented** (P0–P2, 2026-09-20). P1 ran as three parallel lanes (S-BE,
  S-LOGIC, S-VIEW) and P2 merged them. Five post-ship UI adjustments **added the ways in to
  decision 10 and decisions 14–16** (2026-09-20, §101.11), and a further pass **amended
  decisions 11 and 12, supplemented 16 and added decision 17** (2026-09-21, §101.12). **Decision
  8-2 was then amended** after a real 78-lane fleet showed the arrows from outside the figure
  running its full height (2026-09-21, §101.13). What is left is P3 (filter by conversation id,
  the cross-tenant overview, collapsing a spawn's excerpt onto its arrow) and measuring whether
  the activity ledger needs write buffering. The design and every measurement are in
  [docs/101](../log/101-fleet-session-graph.md).
- What it replaces: [0027](0027-operator-interaction-graph.md) (the vertical operator↔session sequence
  diagram, of which only the P0 contract freeze landed) becomes **superseded**.
- See also: [0041](0041-cross-session-messaging.md) decision 9 (a peer message cannot be attributed to a
  conversation — this is what established that the fleet-wide figure is needed) /
  [0073](0073-session-spawned-sessions.md) (`origin_session` lineage — **this ADR reverses its "no ledger" call**) /
  [0035](0035-session-report-v2-ledger.md) (the instruction ledger, `instr-ledger`) /
  [0078](0078-sessions-overview-pane.md) (the card overview; it states the correlation figure is a different thing) /
  [0049](0049-session-changed-files.md) decision 4 (do not mint a PaneKind lightly) /
  [0087](0087-efs-metadata-io.md) (a deployment where write frequency is a bill)

## Context

**This figure is a hole three ADRs have deliberately left open.** 0027 sent "the fleet-wide overview" away
as a different figure and a different task; 0041 decision 9 went as far as **establishing that it is
needed** (a peer message has no conversation to belong to, so `operator-graph/<conv>.jsonl` cannot express
it); 0078 drew the line in its rejected options ("a different thing from the card overview"). It has been
open for three months.

In the same months, the shape of what there is to see changed. When 0027 was written the fleet was radial —
one operator driving N sessions — and the round trips fit a sequence diagram. Since then ADR 0073 (a session
starts a session) and 0041 (sessions talk to each other directly) have landed, and **families are now two
and three levels deep**. A parent starts a child, the child reports back, siblings message each other. That
shape cannot be read "who said what to whom" first; it has to be read **when, and how many, were alive at
once**.

The user asked for it in the shape of GitHub's network graph:

```
Session A     ○--*-----+-------+-----+--×
Session A-c1      +-----⤴       ↿     |
Session A-c2      +-------------+     ⇂
Session D                   ○--------+----------×
```

Against that, **what can actually be drawn today is limited** (measured 2026-09-20; details in docs/101 §1).

| Element | Today |
|---|---|
| ○ birth, × death | **There** (`session.Meta`'s `CreatedAt` / `StoppedAt`; an abnormal end carries `ExitReason`) |
| + branch (spawn / fork / handoff) | **There** (`Origin` + `OriginSession`, ADR 0073 — though `ForkFrom` is a conversation id that needs a reverse lookup) |
| ↿ ⇂ peer arrows | **Absent.** `session_peer.go` persists nothing at all (only the rate limit, in memory) |
| → instruction / ← report | **Partly** in `instr-ledger` as `delivered_at` / `reported_at` — not used as the figure's source, for the reason below |
| Activity band (when it was working) | **Absent.** No state history is kept anywhere, and `tokenSpends` is a list of numbers with no timestamps |
| Anything older than 7 days | **Gone.** A stopped session's meta is pruned at `session.StoppedTTL()` (7 days by default), taking its lineage with it |

One premise has also changed under 0027: the dispatch ledger its decision 2 proposed to create was **already
built afterwards** by ADR 0035 (report v2, 2026-07-29) as `instr-ledger/<session>.json`. Starting work on
0027 as written would mint a second ledger for the same thing.

## Decisions

### Decision 1 — one figure. This ADR replaces 0027's vertical sequence diagram

Build a single figure: **lanes are sessions (vertical), time runs horizontally**. 0027 becomes superseded,
and `console/src/types/opgraph.ts` — its only landed artefact, imported by nothing — is replaced by this
ADR's P0 contract.

- **Why transpose it.** The number of lanes (sessions) is bounded, in the tens; time is unbounded. Putting
  **the bounded axis on the vertical** is why a network graph reads the way it does. A sequence diagram
  scrolls along time, so "how many are running right now" never fits one screen — and since 0073 and 0041
  stretched families two and three deep, that one screen is exactly what there is to read.
- **Why not keep both.** Two figures means two ledgers, two pure layout functions, two pane kinds, two i18n
  sets — and, as 0041 decision 9 settled, the 0027 side would **remain unable to express peer messages**.
  That is a figure that cannot be fixed, kept under maintenance.
- Nothing is lost from 0027's "one operator conversation's round trips": it comes back as **a filter by
  conversation id** (keep only the lanes that conversation touched).

### Decision 2 — two ledgers: lineage is permanent, activity rotates

```
~/.config/agent-fleet/fleet-graph/lineage.jsonl                 # append-only, permanent (never pruned)
~/.config/agent-fleet/fleet-graph/activity-<YYYY-MM-DD>.jsonl   # daily rotation, 30 days by default
```

- **Do not mix two retention periods in one file.** Rotating the activity lines would take the lineage lines
  with them; keeping everything forever would let the activity lines grow without bound. The line counts
  differ by orders of magnitude too — a few lines per session for its whole life, against a few thousand
  lines a day (the estimate is docs/101 §3).
- **Writes are single-line `O_APPEND`**, never `fstore`'s read-modify-write (memory
  `fstore-no-read-modify-write`: a plain read→modify→write drops a concurrent writer's line). An append has
  no branch point, so several writers (HTTP handlers, the reconciler, the observation seam) coexist without
  a lock protocol.
- **Reads are windowed.** With daily files, "the last 24 hours" opens one or two of them. Following ADR 0087
  (EFS metadata IO is what made the whole production deployment slow), **nothing here multiplies file
  count**: no per-session file, no file per event.
- **Time is spelled differently in the ledger and on the wire, and the server always converts.** Ledger
  lines carry **RFC3339 (milliseconds)** — these are jsonl files people grep, in the **same format** the
  repository's other ledgers (`Meta`, `instr-ledger`) use. Only the precision goes up, because a design
  that writes solely on change can produce **two transitions inside one second**, and at second precision
  (which `instr-ledger` deliberately chose, for its own reasons) their order is lost. The DTO (`FleetGraphPage`) carries **unix millis as
  numbers**, so the browser never parses a date. Without fixing the direction here, the type file says
  millis while the documented ledger lines say RFC3339, and **which one S-BE writes is decided by whichever
  document it read**.
- **The daily file's date is UTC.** A reader picks files out of a millis window; the workspace's clock is
  local (policy). Let those disagree and the boundary day is silently skipped.

### Decision 3 — the activity band is written from observed state transitions only; transcripts are never scanned

The session list (`GET /sessions`) **already derives live state for every session**. Write one line
**only when it changes**: the transition (`working → idle → question → limited …`) and the time.

- **Why not transcripts.** Deriving activity from turn timestamps means reading each kind's own store
  (claude's jsonl, codex's rollout, opencode's SQLite …), so **claude fills in and every other kind stays
  blank** — the wall ADR 0078's P1.1 is still stuck behind. Live state is a predicate every kind already
  goes through, so writing transitions is **kind-agnostic** and costs O(transitions): a few lines per turn.
- **Why no new polling.** The shape ADR 0078 worked hardest to avoid is "hit `/messages` per card" = 4
  seconds × number of sessions. This decision **rides an observation that already happens** and appends one
  line; it adds no timer and no process.
- 🔥 **The observation interval is "how often somebody looked", and it is not constant** (measured, docs/101
  §2). With a Console open it is 4 seconds; with nobody watching, the control plane's idle-stop reaper looks
  every minute (`AF_IDLE_SWEEP_INTERVAL`, default `1m`, `control-plane/main.go`); **on a deployment with the
  reaper switched off there is no observation at all**. The Agent has no standing sweep over all sessions —
  `chat_report_reconcile.go`'s sweep visits **only sessions with an open instruction row**.
- Therefore: **an unobserved stretch is drawn as "unknown", not as "not working"** (hatched, not filled).
  This is the second application of the rule report v2 paid dearly for — "**no file means unknown, it does
  not mean idle**" (docs/log/51).
- Right after an Agent restart, write every session's current state once (a `resync` line), so the stretch
  across the restart correctly stays unknown.
- **The state vocabulary is frozen as a ledger-specific `LedgerState`.** Reusing `SessionState`
  (`types/session.ts`) fails twice over: it carries the Console row's convention that **`""` means idle**,
  and it lacks both the states the agents emit around a stopped turn — `limited`, `blocked`, `auth`,
  `spend_limit`, `failed`, `aborted` (`agents/notify.go`) — and `compacting` (`agents/codex`: an
  auto-compaction, which is unambiguously working). In TypeScript `SessionState | string` collapses to
  `string` and **constrains nothing**, so it is written out as a literal union.
- 🔥 **An unrecognised spelling becomes `unknown`, never `idle`.** `""` → `idle` is right, but folding an
  *unknown* state into idle falls the dangerous way: the moment a kind starts reporting a new busy state,
  those stretches are recorded as having done nothing (`compacting` is real, and this nearly lost it). The
  raw spelling is kept in `raw`.
- **One rule, two implementations.** Normalisation runs on the writer's side (the Agent, in Go) *and* on
  the reader's (S-LOGIC, in TS), because the live `Session.state` that feeds the current band is
  `SessionState | string` and carries the same `""` and the same open-ended spellings. Since the table
  exists in two languages, **the same fixture pins it in the Go suite and in vitest** — let them drift and
  the band's colour disagrees with the row's chip in a way that looks like a rendering bug.
- **The state → band (`SegmentKind`) mapping is part of the contract too** (`SegmentKindByState`):
  `working` / `compacting` → `active`; `idle` / `failed` / `aborted` → `idle`; `question` / `plan` /
  `permission` / `blocked` / `auth` / `limited` / `spend_limit` → `waiting`; `unknown` → `unknown`. The
  exact word survives on
  `GraphSegment.state` — **the band is for colour, the state is for the tooltip** — which is why `limited`
  needs no band of its own.
- **Preventing duplicate lines is the writer's job.** The observation is driven by the list handler (a GET),
  and **a Console (4 s) and the reaper (1 min) can hit it at the same time**. The writer keeps each
  session's last state in process and writes **only on a change** (serialized per session with a mutex).
  `StateEvent.from` comes from that map and is **omitted when the map has no entry** — an absent `from`
  means "unknown before this", not "idle before this". A restart empties the map, which is exactly where
  `resync` marks the boundary.

### Decision 4 — all three round-trip arrows (instruction, report, peer) are written in one line format; `instr-ledger` is not read

```jsonc
{"ts":"…","ev":"instruct","from":"conv:<id>","to":"<session>","source":"operator","excerpt":"…"}
{"ts":"…","ev":"report","from":"<session>","to":"conv:<id>","kind":"answer-ready"}
{"ts":"…","ev":"peer","from":"<session>","to":"<session>","intent":"request"}
```

**The `from` / `to` vocabulary splits into what becomes a lane and what does not.** Only a session name
becomes a lane; `conv:<id>` (a chat conversation, i.e. the operator), `user` (the Console composer or the
terminal's keyboard), `schedule` (scheduled execution, ADR 0021) and `bridge:discord` / `bridge:slack`
**have no lane**. Exchanges with those are drawn as arrows leaving the top and bottom edges of the figure
(decision 8-2).

- **`instr-ledger` is not the figure's source because it is a work list, not a history.** Closed rows are
  kept only for the newest 20 (`instrClosedKeep`), and `cancelled` / `reopened` rewrite state in place. As a
  source for a figure that means **old instructions quietly disappear and past times move on a reopen**.
- **Peer messages are not in it, and never will be.** ADR 0041 decision 4 (never touch the arm) is an
  unavoidable requirement for the AF path, so a peer send will not mint a ledger row. Routing one of the
  three arrows differently would give the figure two readers.
- `instr-ledger` stays the authority on **whether a report is owed**. This ADR changes nothing there — it
  does not touch that state machine, so it cannot introduce the class of accident that loses a report.
- Excerpts are **≤140 chars, single line, display-only** (0027 decision 2's policy, carried over), sanitized
  at render, under docs/30's prompt-injection stance.

### Decision 5 — a lane's id is the session name (the random slug); the display name is separate

Ledger lines key on the session **name** alone. The name is a random slug and is **never reused** (memory
`session-slug-immutable-managed-no-env`), so a line survives renames, handoffs and forks. What the figure
shows is `Display` (title → claude label → `repo@MMDD-HHMM`), honouring the existing rule that **the slug is
never surfaced to a human on its own** (`session.Display`'s comment).

### Decision 6 — lineage lines are not pruned automatically; they die only when a person deletes

ADR 0073 concluded "if lineage must outlive the meta, that needs a ledger — not in this ADR", and
**docs/log/94 turned that price into something users can see** (delete one session and its children's
nesting and colour are lost on the spot). This ADR reverses the call, because for an overview figure **losing
lineage is losing a line**: the moment the 7-day prune (`session.StoppedTTL()`) takes a parent, the root of
its children's branch goes with it.

- `lineage.jsonl` is **exempt from the stopped-session TTL prune**.
- But `DELETE /sessions/{name}?reclaim=1` and the cleanup deletion paths **do remove the line**. A deletion
  should be a deletion; "I deleted it and it is still in the figure" is not what a user expects.
- A line carries `{ts, ev, name, kind, repo, origin, originSession, display}` and **no prompt text and no
  report text**. What ought to disappear on deletion is never stored in the first place.
- 🔴 **Amendment, 2026-09-20 (ADR 0097 landed the same day)**: the motivation for this decision —
  **the 7-day auto-prune erasing lineage** — no longer happens: [0097](0097-session-retention.md) removed
  that sweep's deletion outright (the TTL now writes back `Archived = true`). **The decision stands**: the
  rule that a delete erases lineage holds, and so do the ledger's other reasons to exist (activity and
  arrows were never in `Meta`; archived rows are skipped by the list, so the live map cannot supply them;
  pre-feature sessions need the back-fill). Two things did change: (1) the "only the TTL prune keeps it"
  carve-out below now has **no callers** — every remaining `RemoveMeta` path is a person's delete; (2) the
  skeleton no longer stops at 7 days, because an archived meta is never forgotten, so decision 8's third
  tier reaches further back.
- 🔥 **Lineage dies wherever a PERSON's action forgets the meta** (settled during P1). `session.RemoveMeta`
  has five call sites, and **only the 7-day automatic prune (the list handler) keeps the lineage**. The
  other four erase it: `DELETE /sessions/{name}` with or without `reclaim`, **`/stop`** (whose own comment
  says it is the Console's delete), and a session removed along with its working copy (a path the deletion
  lock also refuses, i.e. the product already treats it as deletion).
  ⚠️ **Do not encode this as a list of call sites.** Pin it so a new caller is noticed — count them in a
  test, or move the erasure into `RemoveMeta` itself.
- 🔥 **Deleting leaves the activity lines** (up to 30 days, until they rotate). With the lineage gone, the
  `peer` / `report` lines naming that id survive alone, and the naive reading turns **a message between two
  sessions into an arrow from outside the figure** (no lane can be built, so it falls through to an
  external actor). Rewriting 30 append-only files to chase it is the opposite of ADR 0087, so instead **an
  id with no lineage is drawn as an *erased lane*: a labelled row with no line** (`erased`). It gets **no activity bands**: its state
  lines survive until they rotate, but painting them rebuilds the picture the person asked to be rid of.
  The arrows stay for a different reason — dropping them would make the OTHER lane's message look like it
  came from outside the figure. All that is left to label it with is the slug, so **the localized "deleted" wording is S-VIEW's to add** — the
  existing rule that a slug is never shown to a human on its own (`session.Display`) holds here too. What
  disappears is the **content** — display name, repository, lineage — while the bare id lingers for the
  activity retention. That asymmetry is deliberate, and the label says so rather than hiding it.

### Decision 7 — scope is one workspace; one read endpoint on the Agent, one line on the CP allow-list

Add `GET /api/fleet-graph?since=…&until=…` to the Agent and register it on the control plane's **explicit
allow-list** (memory `cp-rest-proxy-allowlist`: the CP does not pass requests through by default).
Cross-tenant overview is the administrators' table (`GET /api/admin/sessions`), a different reader's job.

🔥 **Lineage is not clipped to the window.** The response's `lineage` is the union of three things: (1)
every event inside the window; (2) **every lineage event of every lane that overlaps the window** — birth,
convid, death, revive and archived alike, however old; (3) the `birth` of those lanes' ancestors, for
family ordering (decision 9). Sending only the birth in (2) loses runs: a lane born on day 1, stopped on
day 2 and resumed on day 3 arrives at a day-4 window with neither its death nor its revive, its `runs`
collapse to one, and **the whole day it spent stopped disappears from the figure**. Lineage is a few lines
per session in a permanent ledger, so clipping it saves nothing.
Clipping naively leaves **a lane born three days before a 24-hour window with no kind, no origin and no
label** — the live `Session` has no `origin` (only `originSession`), so the Console cannot fill it in. An
ancestor that does not itself overlap the window is context for ordering and labels; it gets no lane.

### Decision 8 — the time axis is linear; the default window is the last 24 hours, with "now" at the right edge; looking back may be bounded

- Zoom and pan move the window. Live lanes pulse at the right edge (carrying over 0027 decision 1's reason
  for hand-written SVG: live state, the running animation and click-to-open are not available in a static
  figure).
- **How far back you can look may be bounded** (agreed with the user, 2026-09-20). Panning left decays in
  three steps: **activity (arrows, bands) for 30 days → a skeleton of lineage and birth/death only → before
  installation, the 7 days back-filled from `Meta`**. Where each step drops is **drawn explicitly** (a
  boundary line saying "skeleton only from here"), never a silent fade.
- **Rejected: collapsing empty stretches** (squeezing intervals where every lane is idle). In a figure whose
  point is the activity band, collapsing the gaps deletes the single most readable fact — **that it was
  stopped there**.
- **Rejected: an evenly spaced commit-order axis** (what GitHub's network graph actually uses). It deletes
  the intervals — "that instruction took 40 minutes to come back" — and a time axis is the request itself.

### Decision 8-2 — a sender that is not a session gets no lane; it is an arrow leaving the figure

Exchanges with a conversation (`conv:<id>`), a person (`user`), scheduled execution (`schedule`) and the
bridges (`bridge:*`) are drawn as **arrows descending from the top edge (instructions) and leaving through
it (reports)**. Which conversation it was is carried by the arrow's colour and its tooltip, and clicking it
opens that conversation.

```
        ↓instruction(conv)  ↑report   ↓schedule

Session A   ○--*-------+--------+------+--×
Session A-c1     +------⤴
Session A-c2     +--------------+
Session D              ○----------+--------×
```

- **Why not a lane.** A lane in this figure is the band of something that is **born and dies**, and the x
  axis means its lifetime. A conversation, a person and the scheduler have no birth or death (they are
  always there), so giving them lanes would make **the same horizontal line carry two meanings**. Keep the
  axis meaning one thing.
- Rejected: pinning conversations as lanes at the top (closest to ADR 0027's sequence diagram). Round trips
  would close as lane-to-lane lines, but the axis splits in meaning as above.
- 🔥 **When the counterpart has no row, the rule depends on the kind of arrow** (settled during P1). A
  **round trip** (instruct / report / peer) whose counterpart is an **internal** lane excluded by the window
  or a filter is **dropped**: keeping it as `fromRow:null` would be indistinguishable from an external
  sender, which is the misattribution decision 6 spends a paragraph preventing. A **lineage** edge (spawn /
  fork / handoff) does the opposite — it **stays, with `fromRow:null`**, and the view draws it as decision
  9's **mark for a missing parent**. `variant` tells the two apart mechanically. "When and from whom it was
  born" is the figure's whole point, so branches silently vanishing as the window narrows is not acceptable.
- Rejected: dropping the arrows and marking the lane instead. Density goes down, but **whether a report came
  back** stops being legible at a glance — the same reason 0041 decision 10 refused to defer visualising a
  peer arrival ("invisible exactly where a human most wants to see it").

🔥 **"Leaves through the top of the figure" must not be implemented literally** (the 2026-09-21
amendment, from the user's report). The first version put the external end at **the canvas's top edge**
(`TOP_PAD - 14`) and drew a line from there down to the lane. At nine lanes — the fixture's size — that
line is short and nothing looks wrong. On a real **78-lane fleet it was a 2,649px vertical line through a
2,688px figure, 51 times over**, and the figure became a picket fence with the lanes lost behind it
(measured by `console/scripts/fleetgraph/arrows.mjs`).

**The external end sits 16px above the lane it touches** (less than one 34px row, so it cannot be read as
belonging to the row above). "It came from outside" is carried by **the dot at the stub's far end, the
arrow's colour and its tooltip** — all of which work at any fleet size. The top edge itself never carried
information ("it came from the top of a 2,700px canvas" says nothing). **Lane-to-lane arrows stay as long
as they are**: those connect two real rows, and family order keeps most of them adjacent (measured: of 57,
only 5 spanned more than three rows, and all five were peer messages).

📌 **A nine-lane fixture cannot show this class of defect at all.** Geometry that only breaks at scale has
to be measured at scale (`server.mjs --fleet-lanes N`).

### Decision 9 — lanes are ordered by family; the click rules are borrowed from 0078 unchanged

Children directly under their parent, siblings oldest first, roots newest first (ADR 0078 decision 6's rule).
**A child keeps its parent's id and its `rootId` even when the parent has no lane in this window**
(`GraphLane.parent` / `rootId` / `depth`): tie them to "the parent is drawn" and a child is promoted to a
root the moment its parent slides off the left edge, so **siblings drift apart as the window moves**. The
ordering key is `rootId`, not whether an ancestor happens to be visible.

**When a parent's lineage has been deleted** (decision 6) the chain stops there: **that unreachable
parent's id becomes `rootId`**, and families are ordered by **the oldest birth known inside each** — the
root's own birth may not exist, while "roots newest first" needs a key that always does. Rows indent by
`depth` (ancestors the lineage could name), so **a depth-2 row may stand with no depth-1 row above it**: a
missing parent is marked at the left edge, never repaired by promoting the child to a root.
Clicking opens **beside if there is room, in the same pane on a phone, in a separate pane with a modifier or
middle click** (0078 decision 3 as revised). Do not build a second way to do the same gesture.

### Decision 10 — add one pane kind, `fleetgraph`; three ways in, and a round trip with the overview

It meets ADR 0049 decision 4's exception (do not mint a PaneKind lightly) the same way 0078 did: it is a
surface you keep watching, it cannot live in a modal, it belongs in the layout and the URL, and it pops out.

🔥 **Deciding where the way in is belongs to this decision** (added 2026-09-20). The first version said only
"add a pane kind", and it shipped reachable from **the leader key `g f` and the command palette alone** —
with **no button anywhere**. Its benchmark, 0078 decision 8, has three ways in: a button on the workspace
bar, the layout map at the top of the rail, and `g s`. This figure gets the same three.

| Way in | Where |
|---|---|
| Workspace-bar button | `WsBar.tsx`, next to "Sessions" and "Images" |
| The rail's layout map | `LayoutMap` — which only appears with two or more panes |
| Leader key / command palette | `g f` |

🔥 **The layout map alone is not enough**: it hides itself while there is a single pane, which is exactly
when someone reaches for the overview (memo `sessions-overview-pane`). That is why the bar button exists.

**The sessions overview (`sessions`) and this figure switch to each other.** They are two views of one
"look at the fleet" surface, so the default is a **swap inside the same pane** (`setPaneTarget`), with
Ctrl/⌘ and the middle button opening a new one — decision 3 of 0078's modifier rule, borrowed. Both panes
carry the other's button: one direction only would leave the way back out of the figure missing again.

### Decision 11 — hand-written SVG, laid out by a pure function

`console/src/lib/fleetgraph.ts` (pure, `.ts`) plus `features/fleetgraph/FleetGraphView.tsx`. The SCM commit
graph (`lib/gitgraph.ts` / `features/scm/CommitGraph.tsx`) is the structural template — pure-function layout
plus inline SVG — the same choice as 0027 decision 1. Colours come from the kind palette, with every twin
grep-checked (memory `kind-color-css-checklist`).

🔥 **The activity band, however, is coloured by STATE and not by kind** (user's call, the 2026-09-21
amendment). It takes `stateInfo()`'s own colours: **working = `--accent`, waiting = `--warn`, idle =
`--muted2`**. The first version painted that one band in the agent's kind colour, which **disagreed with
the state chip on the same row** (decision 15): a codex lane sitting on a question drew a green bar next
to an amber chip — the figure and the chip saying different things about the same instant. **Which agent
it is stays readable** from the lane LINE (`--lane-color`) and the kind icon in the label column.
- Consequence (intended): the band covers the line, so **the kind colour is largely invisible over a
  stretch the session was working**. Reading "who is running" off a colour is the label column's job now.
- Check it with numbers, not eyes: have **the browser resolve** the band's `fill` and
  `.session-state.working`'s `color` and compare them (`console/scripts/fleetgraph/check.mjs`). A test
  that compared two `var()` names passes while the two variables point at different colours.

### Decision 12 — the line style says whether it is still there: stopped is dashed, archived is faintly dashed, gone ends at the ×

A lane's horizontal line is drawn four ways (the user's instruction, 2026-09-20).

| State | Line | Meaning |
|---|---|---|
| Running | solid, with the activity band | running now (pulses at the right edge) |
| Stopped (resumable) | **dashed** from the × to the right edge | still listed. **It can be resumed** |
| Archived | **ends at the ×** (no line and no band past it) | folded away; that it is restorable is said **in words by the label column's chip** |
| Pruned / deleted | **ends** at the × (the line does not continue) | gone; only the lineage line remains |

🔥 **Archived moved from "faintly dashed" to "ends there" on 2026-09-21** (user's instruction), for two
reasons. ① **A lane somebody folded away was still drawn to the right-hand edge** — taking as much of the
figure's width as a live one, with both the faint dashes and a grey `SegmentKind: "archived"` band. ② **The
line style no longer has to carry that distinction**: decision 15 put a state chip in the label column, and
it says "archived" in a word, which reads more reliably than the difference between two weights of dash.
**A stopped (resumable) lane keeps its dashes**: that one says something you can act on right now.
`"archived"` was **removed from `SegmentKind`** — a vocabulary word nothing produces any more leaves a
branch in the view that can never run.

**One rule: dashed = still there (resumable); ending = gone, or folded away.** In every case the × is not "when it ended"
but "**when its end was first observed**" (`Meta.StoppedAt` is filled lazily — the same property as the
observation story in decision 3), and the figure says so in the ×'s tooltip.

🔥 **One lane's life is a sequence of RUNS.** Resuming a stopped session **clears** `Meta.StoppedAt`
(the list in `sessionx/session_handlers.go`, `session_tmux.go`, `session_driver.go`), so ○──×──(dashed)──○──×
all belong to one lane. The ledger gains `ev:"revive"` and `GraphLane` carries `runs: LaneRun[]`. A model
that holds a single death cannot say which stretch a second × ends, and **a stretch that died and was
resumed renders as the dashed "stopped" tail**.
- Limit (intended): the back-fill from `Meta` **cannot reconstruct past stop/resume cycles** (only the
  latest `StoppedAt` survives there). Lanes older than the feature are drawn as a single run.
- 🔥 **Archiving a LIVE session writes `death` first, then `archived`** (settled during P1).
  `HandleArchiveSession` kills the pane to fold the session away, but the only place that notices an ended
  slot — the list handler — skips archived rows (`if m.Archived { continue }`), so the death would never be
  written. A ledger holding only `[birth, archived]` leaves the run open on the reader's side: **a solid
  line to the right edge, no ×, indistinguishable from running**. The reader also closes an open run at the
  `archived` timestamp, but that is a defence for back-filled and older ledgers; writing the death is what
  is actually correct.
- **A newest run still open on a lane that is gone** (the Agent died before writing a death, or the session
  was deleted) is **cut at the last moment anything was observed** for it (`LaneRun.cut`): a hollow ×, and
  unknown after it. Defining `gone` as "ends at its last ×" alone leaves this case with no end at all.

- Stopped sessions are **drawn by default**. ADR 0078's list defaults to running-only, but that is a
  cross-section of *now*; this figure is the *elapsed* — hiding stopped sessions from the past empties it.
- Archived ones are drawn by default too, with a toggle to hide them. The toggle lives in `PaneContent`
  (memory `sessions-overview-pane`: per-pane settings held in React state are lost when a tab switches).

### Decision 13 — `ForkFrom`'s reverse lookup is solved by burning the session's own conversation id into the lineage `birth` line

`Meta.ForkFrom` is a **conversation id** (claude = sid, opencode = `ses_…`, codex = uuid), not a session
name, so it cannot be tied to a lane as is (docs/101 §101.1). The lineage ledger's `birth` line carries that
session's own conversation id alongside, and matching is done against **the id that was in force at that
time**.

- 🔥 **The id burned in must come through the same resolution as that kind's `Forker.ForkSource`.** That
  function is what produces `ForkFrom`'s value, and **every kind resolves it from an observed store**:
  claude uses `LiveSID()` (where it is actually writing after a drift), codex the per-slot id its hook
  recorded (`sids.Read`), opencode the current conversation in its store. Burning in the id AF passed at
  launch leaves **codex and opencode fork edges permanently unmatched**. A kind that cannot resolve one at
  birth leaves it empty and a `convid` line fills it in later.
- ⚠️ claude carries a separate trap: right after a fork it reads the *source* session's transcript until
  its own jsonl materialises (`internal/sessionx/session_transcript.go`). Inferring the id from where the
  transcript is puts the parent's id on the child's line and **the edge points at itself**. Always resolve
  through `ForkSource`, never from an observed transcript.
- The conversation id can change mid-life: when claude relaunches itself, `--session-id` structurally drops
  out of the argv and it **starts writing under a new random id** (measured on 2.1.239; the `claude-sid`
  ledger in `internal/agents/claude/sid.go` exists to track exactly this). A `convid` line records the
  drift.
- A pruned parent still has its lineage line (decision 6), so **a fork older than 7 days still draws a
  line**. That only holds because decision 6 is in.

### Decision 14 — families fold from the parent's row; everything with a parent folds

**The fold does not sort by origin** (user's call, 2026-09-20). `session` (spawn), `handoff` (fork) and
`user` + `originSession` (a session a person launched from a handoff proposal, ADR 0073) — **every lane
that carries an `originSession`** folds under its parent. The figure's order is already decided by the
`originSession` chain alone (decision 9), so picking origins for the fold alone would make **part of a row
of siblings disappear** while the rest stayed.

- **The filtering is in the pure function** (`buildFleetGraph`'s `collapsed`), dropping rows and
  renumbering `row`, exactly as `showArchived` does. 🔥 A second filter in the view would hide a bug in
  the builder behind the client's own filter.
- **The fold state lives on `PaneContent`** (`fleetgraph.collapsed` / `sessions.collapsed`). React state
  snaps back on a tab switch, which unmounts the view (memo `sessions-overview-pane`).
- **A folded parent's row says "+3"** (`GraphLane.hiddenDescendants`). With no mark, the figure reads as
  "this session had no children". The count covers **every depth**, and an outer parent counts what an
  inner fold already hid — the outer one is what is swallowing them.
- **The "+" appears only when pressing it does something** (`GraphLane.hasChildren`). A parent whose only
  children are outside the window would otherwise offer a control that does nothing.
- **A fold hangs off a DRAWN parent only.** If the parent has no row in this window (off the left edge,
  lineage deleted), its children are not hidden: there would be no press on screen to bring them back.
- **The sessions overview (0078) gets the same control** (user's call). `overview.ts`'s `foldFamilies` is
  the same rule as a pure function. The heading's "{alive} running / {n} total" stays **pre-fold** — a
  number that dropped on every fold would read as sessions ending.

### Decision 15 — the label column's state comes from `stateInfo()`, as its fourth consumer

The figure says state in the colour of its activity bands, but the label column carried only a kind icon
and a name. It gets **a chip for the state right now** (user's request, 2026-09-20).

🔥 **Do not derive state a second time.** `console/src/lib/sessionview.ts`'s `stateInfo()` is the
authority, and the rail row, the pane head and the overview card all read it (ADR 0078 decision 5). This
figure becomes the **fourth consumer**, of the same function and the same `.session-state` markup. A
derivation of its own would end with this figure saying "idle" about a session the rest of the Console
calls rate-limited.

- **`GraphLaneKnown.state` (`LedgerState`) is not enough.** That is the ledger's vocabulary: it exists
  only for live lanes and carries none of what `stateInfo` picks up — **a reserved resume instant, an
  expired login, a pending handoff, the reason an agent died**. The ledger's words stay where they are,
  on the bands and their tooltips.
- **A lane the live list does not carry** (archived — the list handler skips it — or deleted) has nobody
  to ask, so the chip says **the presence word the line style already encodes**. Never a guess at a state
  nobody reported.

### Decision 16 — the time axis is pinned to the top; sideways movement moves by the distance travelled

The first version drew the scale at the **bottom of the canvas** and turned **every** wheel event,
vertical included, into a time pan (user's report, 2026-09-20). Both break when the figure gets big.

- **The scale is a strip pinned to the top** (`position: sticky; top: 0`). At the bottom it is only
  readable after scrolling — that is, **only when the lanes do not fit**, which is exactly when a scale
  is wanted.
- **A vertical wheel goes back to the lane list.** The list is what actually overflows, and spending that
  gesture on time left **no way at all to reach the rows below the fold**.
- **Sideways movement maps px onto time** (`deltaX`, and a drag's travel, times `span / width`). A fixed
  12% of the window per wheel EVENT threw the window days away on one trackpad flick (dozens of events).
- **The canvas drags to pan** (`grab` / `grabbing`). Past `DRAG_SLOP_PX` the click the browser synthesizes
  at the end is **swallowed in the capture phase** — otherwise ending a drag opens the lane it began on.
- **The page is re-fetched once the gesture settles** (`FETCH_SETTLE_MS`). One flick used to ask
  `/api/fleet-graph` for a page on every frame. The first load is not delayed.
- 🔥 **An empty window is a place, not an error state.** Replacing the whole figure with an empty-state
  card took the axis (where am I?) and the gesture handlers (how do I get back?) with it — one flick left
  a pane whose only working control was "reset". The body is always drawn; the card sits under the axis.

**The 2026-09-21 supplement — make it a surface you grab** (user's instruction).

- **A drag moves both axes.** Sideways is time; up and down scrolls the lane list, by moving `scrollTop`
  by hand — the canvas carries `touch-action: none` (below), so the browser no longer does it for us.
- **Two fingers pinch the time axis.** The **time axis only** (user's call): scaling the row height too
  would drag the label column, the chips and the fold controls along with it.
- 🔥 **`touch-action` is `none`, not `pan-y`.** A gesture the browser keeps for itself is a gesture it also
  **stops delivering as pointer events**. Handling both the pinch and the vertical drag means taking the
  whole surface. **The label column stays `auto`**, so an ordinary flick over the names still scrolls
  natively — this does not take everything.
- 🔥 **It collides with the phone's "swipe left to change session".** The canvas opts out by name with
  `data-no-swipe` (`app/swipeGuard.ts`). Escaping by passing for a horizontal scroller instead would make
  the decision depend on what happens to be in the figure.
- **The window's arithmetic moved to `features/fleetgraph/viewport.ts`** (`clampWindow`, `panByPx`,
  `zoomAt`, `pinchSpanFactor`), because **that is where the signs collect**: a wheel moves the VIEWPORT, a
  drag moves the CONTENT, and a pinch's finger ratio is INVERTED to become a span factor. Three gestures,
  three directions — and **getting one wrong only ever looks like "it does not move"**, because the clamp
  pins the window against "now". Each direction has a failing test.
- **Zoom is anchored under the pointer or the fingers** (`zoomAt`'s `fraction`). Fixed at the right edge,
  the instant being pinched slides away from the fingers. The **clamp still beats the anchor**: when the
  right edge would pass "now", the anchor is what gives.

### Decision 17 — a lane in the figure does not open on click; the NAME is what opens a session

The canvas's lanes (the line, the activity band, the row's hit band) **open nothing on click or tap**
(user's instruction, 2026-09-21). **The label column's name opens it**, still by decision 9's rule: beside
when there is room, a new pane on Ctrl/⌘ or the middle button.

- **The reason is decision 16.** The canvas became **a surface you grab**. When the surface you grab and
  the surface you open are the same, **a finger meaning to pan opens a session** — and since that opens a
  pane, it also loses the place in the figure you were reading. The capture-phase click swallow already
  covers a press that MOVED; a press that does not move looks exactly like a deliberate tap, so the two
  roles are split by place instead.
- **Arrows stay clickable** (they open a conversation or a lane). Aiming at a thin line is deliberate and
  cannot be confused with a pan.
- **The row's hover highlight stays.** It says which row the pointer is on, not that the row can be
  pressed, and it is what lets the eye follow one lane across a dense figure.

## Options rejected

- **Read `instr-ledger` as the figure's ledger**: decision 4 — a work list, not a history (20 closed rows,
  times that move on reopen, peers structurally absent).
- **Keep 0027's sequence diagram as a second figure**: decision 1 — it would be a figure that cannot be
  fixed, kept under maintenance.
- **Derive the activity band from transcript turn times**: decision 3 — claude fills in and the other kinds
  stay blank (ADR 0078 P1.1's wall). Looking retroactive is what makes it dangerous.
- **Leave lineage in `Meta` alone** (ADR 0073's original call): decision 6 — accepting that the prune erases
  lines only held while there was no overview figure.
- **Fold it into the card overview (0078)**: 0078 itself rejected this. The grid is a cross-section of *now*;
  this figure is the *elapsed*.
- **mermaid `gantt` / `sequenceDiagram`**: the lane-as-a-band shape is close, but crossing arrows, live
  state, click-to-open a session and `--kind-*` theme following are all unavailable (0027 decision 1's
  survey, carried over).
- **A standing sweep over all sessions in the Agent**: it would close the observation gap (decision 3) at the
  cost of touching tmux and files while nobody is watching. Leaving the gap **unfilled and drawn as unknown**
  is both cheaper and more honest.

## Consequences

- **Eight write points and one read endpoint.** A one-line append is colocated at: (1) create
  (`session_handlers.go`); (2) stop/exit; (3) **resume**; (4) **archive / restore**
  (`HandleArchiveSession` / `HandleRestoreSession`); (5) the state observation (the list handler); (6) peer
  send (`session_peer.go`); (7) instruction delivery (`session_io.go`); (8) report delivery (the sink in
  `chat_report_reconcile.go`). `GET /api/fleet-graph` reads them. The existing report, notification and arm
  paths are **untouched**.
  - 🔥 Miss (3) and (4) and **`revive` and `archived` have no writer while the implementation looks
    finished**. `runs[]` and `presence:"archived"` exist in the type, but unwritten they never occur:
    `runs` stays a single entry and decision 12's resumed stretch renders as the dashed tail again.
  - 🔥 (3)'s condition is "**the slot became alive again**", not "a path that clears `StoppedAt`".
    `grep 'StoppedAt = ""'` finds **four** sites, and the fourth — `HandleRestoreSession` — only puts the
    session back in the list **still stopped** (`wireSession(m, false)`). Writing a `revive` there grows a
    run that never ran. Restore writes (4)'s `archived:false` and nothing else.
- **No added polling** (decision 3). The ledgers are on the order of a few hundred KB a day (docs/101 §3).
- **Limit (intended)**: panning left decays in three steps (decision 8) — activity for 30 days, then a
  skeleton of lineage and birth/death, then the 7 days back-filled from `Meta`. That back-fill runs once at
  first start, so **the skeleton covers the past 7 days immediately**.
- **Limit (intended)**: observation resolution depends on the deployment (4 s / 1 min / none). The figure
  does not hide that — it hatches it.
- **Limit (intended)**: a child's **launch task text is not drawn**. The spawn arrow already comes from
  `birth`'s `origin` / `originSession`, so writing the same exchange again as an `instruct` would draw the
  arrow **twice**. Showing the text needs a rule that collapses a spawn and an instruct at the same instant
  between the same pair into one arrow labelled with the excerpt — P3.
- **Stopped and archived sessions are drawn too** (decision 12), so there are **more lanes** than the list
  shows (0078 defaults to running-only). Family ordering (decision 9) and the dashed styles are what is
  meant to carry that density; measure after implementation to decide whether lanes need folding or
  virtualisation.
- 0027 becomes superseded, `types/opgraph.ts` is retired at P0, and docs/44 gets a 🔴 amendment.

## Phases

- **P0, contract freeze (done)**: the REST DTO, `console/src/types/fleetgraph.ts` (import-only), the ledger line
  formats, this ADR and docs/101. `types/opgraph.ts` retires here.
- **P1, three in parallel** (no concurrent builds on this memory-constrained host; parallelism ≤3): S-BE (Go:
  the two ledgers, the eight write points, the API, the CP allow-list) / S-LOGIC (`lib/fleetgraph.ts` plus
  vitest) / S-VIEW (the view, pane wiring, i18n, rendering against a fixture). The shared glue (pane union,
  `Pane.tsx`, `paneTitle`, i18n) belongs to S-VIEW alone, so conflicts close at a single merge.
- **P2, integration**: merge BE → LOGIC → VIEW, swap the fixture for the real layout function, and run every
  gate (`go test` / `tsc` / `vitest` / `vite build` / `i18n:lint`) plus a rendering check in headless
  Chromium.
- **P3 (outside this ADR)**: filtering by conversation id (absorbing 0027's "one conversation's round
  trips"), a cross-tenant administrators' overview, and revisiting whether a standing sweep should close the
  observation gap.

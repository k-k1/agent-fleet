# 0096. The fleet's session graph is one figure — lanes are sessions, the horizontal axis is time — and only lineage is kept in a permanent ledger

English | [日本語](0096-fleet-session-graph.ja.md)

- Status: **proposed**. The design and the measurements are [docs/101](../log/101-fleet-session-graph.md).
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

### Decision 4 — all three round-trip arrows (instruction, report, peer) are written in one line format; `instr-ledger` is not read

```jsonc
{"ts":"…","ev":"instruct","from":"operator:<conv>","to":"<session>","source":"operator","excerpt":"…"}
{"ts":"…","ev":"report","from":"<session>","to":"operator:<conv>","kind":"answer-ready"}
{"ts":"…","ev":"peer","from":"<session>","to":"<session>","intent":"request"}
```

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

### Decision 7 — scope is one workspace; one read endpoint on the Agent, one line on the CP allow-list

Add `GET /api/fleet-graph?since=…&until=…` to the Agent and register it on the control plane's **explicit
allow-list** (memory `cp-rest-proxy-allowlist`: the CP does not pass requests through by default).
Cross-tenant overview is the administrators' table (`GET /api/admin/sessions`), a different reader's job.

### Decision 8 — the time axis is linear; the default window is the last 24 hours, with "now" at the right edge

- Zoom and pan move the window. Live lanes pulse at the right edge (carrying over 0027 decision 1's reason
  for hand-written SVG: live state, the running animation and click-to-open are not available in a static
  figure).
- **Rejected: collapsing empty stretches** (squeezing intervals where every lane is idle). In a figure whose
  point is the activity band, collapsing the gaps deletes the single most readable fact — **that it was
  stopped there**.
- **Rejected: an evenly spaced commit-order axis** (what GitHub's network graph actually uses). It deletes
  the intervals — "that instruction took 40 minutes to come back" — and a time axis is the request itself.

### Decision 9 — lanes are ordered by family; the click rules are borrowed from 0078 unchanged

Children directly under their parent, siblings oldest first, roots newest first (ADR 0078 decision 6's rule).
Clicking opens **beside if there is room, in the same pane on a phone, in a separate pane with a modifier or
middle click** (0078 decision 3 as revised). Do not build a second way to do the same gesture.

### Decision 10 — add one pane kind, `fleetgraph`

It meets ADR 0049 decision 4's exception (do not mint a PaneKind lightly) the same way 0078 did: it is a
surface you keep watching, it cannot live in a modal, it belongs in the layout and the URL, and it pops out.

### Decision 11 — hand-written SVG, laid out by a pure function

`console/src/lib/fleetgraph.ts` (pure, `.ts`) plus `features/fleetgraph/FleetGraphView.tsx`. The SCM commit
graph (`lib/gitgraph.ts` / `features/scm/CommitGraph.tsx`) is the structural template — pure-function layout
plus inline SVG — the same choice as 0027 decision 1. Colours come from the kind palette, with every twin
grep-checked (memory `kind-color-css-checklist`).

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

- **Six write points and one read endpoint.** Create (`session_handlers.go`), stop/exit, the state
  observation (the list handler), peer send (`session_peer.go`), instruction delivery (`session_io.go`) and
  report delivery (the sink in `chat_report_reconcile.go`) each get a colocated one-line append;
  `GET /api/fleet-graph` reads them. The existing report, notification and arm paths are **untouched**.
- **No added polling** (decision 3). The ledgers are on the order of a few hundred KB a day (docs/101 §3).
- **Limit (intended)**: activity (arrows, bands) exists **only from installation forward**. Lineage and
  birth/death can be back-filled once at first start from the existing `Meta`, so **the skeleton covers the
  past 7 days immediately**.
- **Limit (intended)**: observation resolution depends on the deployment (4 s / 1 min / none). The figure
  does not hide that — it hatches it.
- 0027 becomes superseded, `types/opgraph.ts` is retired at P0, and docs/44 gets a 🔴 amendment.

## Phases

- **P0, contract freeze**: the REST DTO, `console/src/types/fleetgraph.ts` (import-only), the ledger line
  formats, this ADR and docs/101. `types/opgraph.ts` retires here.
- **P1, three in parallel** (no concurrent builds on this memory-constrained host; parallelism ≤3): S-BE (Go:
  the two ledgers, the six write points, the API, the CP allow-list) / S-LOGIC (`lib/fleetgraph.ts` plus
  vitest) / S-VIEW (the view, pane wiring, i18n, rendering against a fixture). The shared glue (pane union,
  `Pane.tsx`, `paneTitle`, i18n) belongs to S-VIEW alone, so conflicts close at a single merge.
- **P2, integration**: merge BE → LOGIC → VIEW, swap the fixture for the real layout function, and run every
  gate (`go test` / `tsc` / `vitest` / `vite build` / `i18n:lint`) plus a rendering check in headless
  Chromium.
- **P3 (outside this ADR)**: filtering by conversation id (absorbing 0027's "one conversation's round
  trips"), a cross-tenant administrators' overview, and revisiting whether a standing sweep should close the
  observation gap.

# 0078. The overview of running sessions is one pane kind, and it borrows the row's parts and its menu as they are

English | [日本語](0078-sessions-overview-pane.ja.md)

- Status: **adopted, P0 implemented** (2026-09-12). The study and the measurements are [docs/96](../log/96-sessions-overview.md).
- See also: [0049](0049-session-changed-files.md) decision 4 (**do not mint a PaneKind lightly** — this ADR argues the exception) /
  [0036](0036-working-sets.md) (a working set is a display filter; this view follows it) /
  [0055](0055-idle-stop-and-carried-interactions.md) (one definition of "busy") /
  [0027](0027-operator-interaction-graph.md) rejected option "fleet-wide overview" and [0041](0041-cross-session-messaging.md) decision 9 (**a different thing** — see Options rejected) /
  [0017](0017-keyboard-system.md) (registering in the command table)

## Context

Past five or six sessions, the left pane's rows make you read one line at a time to learn which
session is stuck on a question and which is running. A row shows what fits in a 339px rail
(measured in ADR 0061 decisions 14–16), and its state chip folds down to a single icon. The
Console has no surface that shows everything at once; the one thing close to it is the
administrators' table (`GET /api/admin/sessions`, cross-tenant, polled every 5 s), which is for a
different reader.

The parts, on the other hand, are all there.

- The row's state derivation `stateInfo()` (`console/src/lib/sessionview.ts`) is shared "so the
  left pane's rows and the pane header render a session identically".
- The right-click menu `SessionMenu.tsx` was lifted out of the row "so the SAME items appear
  wherever a session is shown", and the tabbed grid's tabs already use it.
- The session store is kept fresh by SSE and 4-second polling.

## Decisions

### Decision 1 — a new pane kind, `sessions` (not a modal)

Add `{ kind: "sessions"; showStopped: boolean }` to `PaneContent`.

- **Why a pane.** A surface you watch does not fit a modal, which blocks everything else while
  it is open. As a pane it gets splitting, tabs, pop-out, layout persistence and the phone's
  one-pane view **from the existing machinery**. The column count following "the pane's width"
  (decision 4) is a pane property too.
- **Relation to 0049 decision 4.** 0049 says "when a strip on an existing pane will do, do not
  add a kind" — not "never add a kind". This view has no existing pane to ride on.
- All eight registration points for a kind are touched (the union, the stored-form validator,
  the same-target check, the render switch, the title, the mini-map abbreviation, pop-out
  eligibility, i18n). **Skipping the validator (`migrate.ts`) turns the pane into a blank
  terminal on reload** — the same drill as adding the shared-session kind.
- `showStopped` lives in the pane's **content**, not in React state: a tab switch unmounts this
  view, and a toggle that snaps back on every switch reads as broken. The same-target check
  compares the kind alone, so a layout holds one overview whatever its toggle says.

### Decision 2 — running sessions by default; stopped ones by a per-pane toggle

The point is "see what is running", so stopped rows stay out by default and draw dimmed when the
toggle adds them. Mixed in permanently, ten stopped sessions and three running ones carry the
same weight and bury what you came to see.

### Decision 3 — a card opens its session **beside** the grid, and **in this pane** on a phone, where there is no beside

A click goes through `openSessionFromList(s, split, running)` (the single entry the row and the
palette use); what decides `split` is whether there is room beside. The grid is the thing being
watched, and on a wide screen a click that replaced it would need a "back" every time.

- **Wide screens**: a plain click opens beside (`split=true`) and the grid stays put.
- **Phones** (`max-width: 760px`, `MOBILE_QUERY` in `lib/device.ts`): there is no beside.
  `openInNew` splits the column into two stacked cells there, so the click yields **two
  half-height panes** — the last thing anyone wants on a phone. A plain tap opens the session
  **in this pane**, and the device's **Back button** returns to the grid: every layout commit
  pushes a history entry (`layout/store.ts`), so back restores the snapshot that still holds the
  grid. `showStopped` lives in the pane's content (decision 1), so the toggle comes back with it.
- **Ctrl / ⌘ / middle-click mean "open in another pane" at every width**, the meaning they carry
  everywhere else in the Console (rail rows, repo rows, chat). On a desktop that is the same
  result as a plain click, but a modifier whose meaning flips with the viewport is worse to learn
  than one that is redundant.
- **The tabbed layout is not a second branch.** `openInTab` collapses `split` either way into "a
  new tab in the same cell", so on a phone in tab mode this already reads as "opens full screen,
  the grid one tab away". The card looks only at the width; the mode is `layout/ops.ts`' business.
- An already-open pane is focused instead (`sameTarget` dedupes).

### Decision 4 — CSS Grid `auto-fill, minmax(240px, 1fr)`; narrow widths through a container query

- flex-wrap follows the width too, but the last row's cards stretch to fill it, which reads as
  "those sessions are bigger". Grid's auto-fill keeps every card in a row the same width and
  leaves the last row alone.
- Narrow-width folding (dropping the toggle's wording) uses `@container paneview`, not `@media`:
  a popped-out tab is narrow at any window size, and a pane in a side column is narrow in a wide window. The container is declared
  on the view's own root, as the mirror and the terminal do.

### Decision 5 — state comes from `stateInfo()` as its third consumer; no second derivation

The card's state chip, colour class and kind icon come from the same helpers as the row and the
pane head. A card has room, so the wording the row folds away ("Working…", "Ready") is **shown
in full**. Only the states that need the person now (question / plan / permission) colour the
whole frame, in the chip's own colour.

### Decision 6 — order by stage only (waiting → running → stopped); newest first inside a stage, fixed

The palette's `sortSessionsByAttention` is reused, but the "when did it last start waiting"
ledger (`waiting.ts`, which says "nothing but the palette's ordering may use it") is **not passed
in**. The palette can freeze its order the moment it opens; a grid stays open, and cards that
change square every time a question is answered cannot be watched. Crossing a stage (a question
arrived, a question was answered) is the one move worth the jump, because it points at the card
to go to next.

### Decision 7 — the operations are `SessionMenu`, borrowed whole; not one item is duplicated

Right-click, the ⋯ button and the Menu key / Shift+F10 open the same `SessionMenu`. Only the
placement is the caller's (at the cursor, or under the ⋯), and those are the two placements the
tab and the row already have. `useSessionActions()` is called once in the container, as
ProjectTree does.

### Decision 8 — three entries: the action bar's button, the rail's layout map, and the leader `g s`

The layout map (`LayoutMap`) **hides itself while there is one pane**, so a button only there is
missing in the most ordinary moment for wanting the overview (found while implementing). The
button sits with "Split right / Split down / Close all" on the action bar — the overview is a pane.

## Options rejected

- **A modal** (the shape of Cleanup / Archived): cheap, wrong for watching (decision 1).
- **Widening the left pane into cards**: collides with the withdrawn left-pane IA. The rail is
  rows; surfaces go on the right.
- **flex-wrap**: the last row stretches (decision 4).
- **"Last utterance" / "last updated" in v1**: not in the DTO. The transcript is heavy through
  the mirror, and a one-line summary added by the Agent has to be carried through four places in
  the control plane's relay (a field missing from `sessionWire` is dropped silently, docs/94
  §94.10). Deferred to P1.
- **Reordering inside a stage by the waiting ledger**: decision 6.
- **Doubling as the fleet overview diagram**: the "conversations × sessions × messages"
  relationship diagram that ADR 0027 split off and ADR 0041 decision 9 established the need for
  is **a different thing**. This view is a grid of cards; lineage is shown only as the left
  spine's colour (ADR 0073). Whether the diagram is needed is unchanged by this ADR.

## Impact

- Console only. No server change, no additional polling.
- Touched: `layout/{types,migrate,ops}.ts`, `features/panes/{Pane,LayoutMap,paneTitle}`,
  `features/overview/` (new: view, card, pure functions, CSS, opener), `app/WsBar.tsx`,
  `features/keys/commands.ts`, i18n (ja/en).
- Tests: pure (filter, stages, stability) and DOM (opens beside / in this pane on a phone, with
  the modifier and the wheel still opening another / three menu routes / a dead session does not
  open). The look was measured in headless Chromium against the README stub
  (docs/96).

## Phases

- **P0 (this ADR, done)**: all of the above.
- **P1**: a "last line" on the card (the Agent adds a one-line summary to the DTO; four relay
  points in the control plane). Text filtering inside the grid. A resume button directly on a
  stopped card.
- **P2**: grouping cards by lineage (children under their parent). The relationship diagram
  (the docs/44 follow-up) stays outside this ADR.

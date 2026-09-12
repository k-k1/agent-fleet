# 0078. The overview of running sessions is one pane kind, and it borrows the row's parts and its menu as they are

English | [日本語](0078-sessions-overview-pane.ja.md)

- Status: **adopted, P0 and P1①② implemented** (2026-09-12). The same day, on the user's feedback,
  **decision 3 was revised** (phones), **decision 6 was revised** and **decisions 9–11 added**
  (repository headings, family order, the card's shape, waiting elapsed), then **decisions 12 and
  13 added** (the card's last utterance; the meta row rebuilt around the mirror's gauge and token
  trend — both across Agent → control plane → Console). The study and the
  measurements are [docs/96](../log/96-sessions-overview.md).
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

### Decision 6 — the stage belongs to the FAMILY; inside a family it is parent → children (revised 2026-09-12)

Originally the stage (waiting → running → stopped) was per card, newest first inside a stage.
The user's requirement — "sessions with a parent-child relation should read parent → child,
stopped ones too" — cannot live with that: the moment a child asks a question it climbs a stage
on its own and leaves its parent behind.

The rule now (`orderByFamily` in `features/overview/overview.ts`):

- **The stage is carried by the family** (the tree `originSession` links), and a family's stage
  is its members' lowest. A family holding a session that waits for a person leads the group,
  and **a family is never split**.
- **Inside a family: parent → children**, depth first. Siblings go **oldest first** (inside one
  family the order IS the spawn order, so a new child appends at the end); roots go **newest
  first** — separate pieces of work, in the order the grid always used.
- **A stopped child sits under its parent** as well. It is absent only while "Show stopped" is
  off; no stage ever pulls it away from its parent. Showing and hiding stopped sessions is
  decision 2's toggle's job.
- The "when did it last start waiting" ledger (`waiting.ts`, "nothing but the palette's ordering
  may use it") is **not passed to the ordering** — decision 11 uses it for display only. The
  palette can freeze its order the moment it opens; a grid stays open, and cards that change
  square every time a question is answered cannot be watched.
- A child whose parent is not on this grid (started in another repository, or the parent was
  deleted) is **a root where it stands**: a grid cannot show one card under two headings. The
  lineage spine's colour (ADR 0073) is what says they are related.

### Decision 7 — the operations are `SessionMenu`, borrowed whole; not one item is duplicated

Right-click, the ⋯ button and the Menu key / Shift+F10 open the same `SessionMenu`. Only the
placement is the caller's (at the cursor, or under the ⋯), and those are the two placements the
tab and the row already have. `useSessionActions()` is called once in the container, as
ProjectTree does.

### Decision 8 — three entries: the action bar's button, the rail's layout map, and the leader `g s`

The layout map (`LayoutMap`) **hides itself while there is one pane**, so a button only there is
missing in the most ordinary moment for wanting the overview (found while implementing). The
button sits with "Split right / Split down / Close all" on the action bar — the overview is a pane.

### Decision 9 — headings are REPOSITORIES, identified by their remote, never by folder name (added 2026-09-12)

The cards sit under one heading per project, and the identity is **`remote` (the host) +
`remotePath` (`owner/name`)**, not the folder.

- **Folder names are the user's.** One repository is cloned twice as `app` and `app-review`, and
  each `app@wip-*` worktree is yet another folder. "The same project" cannot be told from names.
- So the Agent's `GET /api/repos` **gained `remotePath`** (`gitx.gitRemotePath` — what follows
  the host in the origin URL, credentials and a trailing `.git` removed). `remote` was
  documented as "the host; no path/token", so **carrying the path is a change to that
  decision**; credentials still never ride along (`SSHToHTTPS` drops an scp-form `git@`, and any
  remaining `user:pass@` is cut with the host). The control plane passes `GET /api/repos`
  through, so no relay point had to be added.
- **A working copy with no remote** (a local-only git repo, an SVN copy) groups by its **base
  working copy's name** (a worktree under its parent's). Sessions in no working copy (a shell in
  home) fall into a trailing "other" heading — where the rail's tree puts them too.
- Headings are **ordered by name and stay put**. A project holding a waiting session is not
  lifted: whole sections moving would change which box is "the second one down" every time.
  Where to go next is said by decision 6's family stage and the card's warm frame.

### Decision 10 — the card's top right is the state; right of the branch is the parent diff (added 2026-09-12)

- **The state chip goes at the end of the head row** (right of the title, left of the ⋯). On a
  grid of cards the eye lands on the top right first, and a whole row is saved, so a card does
  not grow taller in a one-column pane. The chip never shrinks: when width runs out **the title
  ellipsizes first** (which session it is can be read from the line below; a half-drawn state
  cannot).
- **A worktree's distance from its parent goes right of the branch**, as the very same chip with
  the very same wording as the rail's repo row (`parentSyncLabel` / `parentSyncTitle` in
  `features/repos/parentSync.ts`, lifted out of `RepoRow` for this). "親+2・FF可" must not mean
  two things in one Console.
- The remaining badges (lock, keep-awake, stop-armed, shared) stay on a row of their own,
  rendered **only on the cards that have one**.
- **The pane-ordinal badges are not shown** (the user's call, 2026-09-12). Which pane holds a
  session is the rail row's job; on a grid it is a row of numbers. That a card IS open still
  reads from its frame (`.open`) and the hover cross-highlight.

### Decision 11 — "waiting for" comes from the two existing ledgers; when they do not know, show nothing (added 2026-09-12)

The card shows how long it has been since the session last started waiting for a person, but
**the DTO has no such timestamp** (`Session` carries neither `updatedAt` nor a state-entered
instant — checked when deciding). The two ledgers the command palette already composes are
reused (the max of `waitingAtFromNotifications` and `observedWaitingAt`):

- The notification ledger is server-side with a `createdAt`, so it survives a reload and reaches
  another device.
- This tab's observations fill the gaps notifications leave (kinds that raise none, waits older
  than the retention window).
- **When neither knows, nothing is shown.** An invented "0m" would let a made-up number decide
  what to answer first.
- While waiting it reads "waiting {d}"; once answered the same instant reads "{d} since your
  reply" — how long it has been working since you replied. **It is never used for ordering**
  (decision 6).
- If that is not accurate enough, **P1 adds `waitingSince` to the Agent's DTO** (a field missing
  from the control plane's `sessionWire` is dropped silently, so it lands in four places —
  docs/94 §94.10).

### Decision 12 — the bottom of the card is one line of what the agent last SAID, capped by the Agent (added 2026-09-12, P1①)

The state chip only says what a session is doing. **What it is doing about is nowhere on the
card** — and that is exactly what the grid is read for. The opening line of the **last assistant
utterance** in the transcript goes **below** the meta row, at the foot of the card.

- **The Agent is the source.** The `Session` DTO holds no last utterance (the same check as
  decision 11). `claude.TailFacts` (`tailfacts.go`) builds it from claude's transcript onto the
  DTO, and it reaches the Console through the control plane's `sessionWire`. **A field missing from that relay is
  dropped silently**, so it went into four places (the struct, the contract table, the relay
  round-trip test, the golden — docs/94 §94.10).
- **The capping is on the Agent's side** (one line, whitespace collapsed, **120 runes**). A card
  ellipsizes one line at any width, so anything beyond that is **payload nobody reads, carried
  for every session every 4 seconds**. The Console renders the string it is handed and neither
  reshapes nor interprets it.
- **Not paying for it on the poll** is the heart of this decision. It reads **one tail window**
  of the transcript (`transcriptTailWindow` = 512 KiB) and **never widens to the whole file** the
  way `lastLineWhere` does — widening is the same rut as re-reading codex's entire rollout on
  every poll, and a single turn filling that window with nothing but tool records is not rare.
  On top of that it memoizes by mtime (the arrangement `ctxCache` uses), so an unchanged
  transcript costs **one stat**.
- **When the window holds no utterance, the line already known is KEPT** rather than blanked. It
  is still true — nothing newer has been said — and a card that empties itself halfway through a
  long turn reads as "this session went quiet".
- **claude first.** Every other kind sends "" and the row is **not drawn at all** (it takes no
  space). Their transcripts live in different places and each needs its own measurement, so they
  go to **P1.1**.
- **Shown on stopped cards too.** For a stopped session the last thing it said is the only clue
  left, and the cards that need it are the ones nobody has reopened.
- **Display only.** It is a fragment of an answer with no turn boundary and no timestamp, so it
  may not feed ordering, state or notifications (the same line decisions 6 and 11 draw).
- **It grows the card by exactly one line** (measured 97px → 118px). Wrapping is forbidden: a
  card that grows and shrinks by two lines with how much a session happened to say makes the
  grid's rows jump. Narrowness was measured **both ways** — a thin column (318px) and a phone
  (376px) — per decision 4 and §96.7. A grid row sizes to its tallest card, so what grows is not
  one card but **that row**.

### Decision 13 — the meta row names the model that ANSWERED, and the context and token figures are the mirror's own parts (added 2026-09-12, the user's call)

The **kind is dropped from the meta row** and replaced by **the model that last answered**. The
context percentage stops being text: the card carries **the mirror's own `ContextBar`** — the
gauge plus the token-spend sparkline.

- **The kind does not need spelling out.** The coloured square in the head already says which
  agent this is, and on a grid the word was a column of "Claude". The kind stays as the square's
  tooltip.
- **"The model that answered" is `context.model`**, read off the newest assistant turn, not the
  `model` the session was launched with. A session whose model was switched mid-conversation has
  to read on the grid as **what is running in it now**. It falls back to the launch model only
  until the session has answered once.
- **The gauge is borrowed, not rebuilt** — the same reason as decisions 5 and 7. Two arithmetics
  for "how full is it" would drift apart, and the card would contradict the chat. The card
  changes only the **scale**: the labels are always the short forms (`ctx` / `token`) and the row
  wraps into two (overview.css). A card is **never wide** — the grid's floor is 240px — so the
  pane-width `@container paneview` fold the mirror uses is not enough here.
- **The trend had no source in the DTO.** The mirror's sparkline is built from per-turn spend in
  the transcript, and the list holds no transcript. **`tokenSpends` was added to the Agent** (the
  same four relay points as decision 12).
- **One point per REPLY.** claude writes one reply as several rows — the text, each tool call,
  the follow-ups — and the mirror's `groupTurns` folds that run into one block. The Agent folds
  it the same way (output sums; input and newly-cached come from the **last** row; cache READS
  are not spend), or one session's card and its chat would draw different shapes.
- **A reply the window cut in half is dropped** (only one tail window is read — decision 12): a
  truncated reply would draw as a small turn, which is a lie. Under two points the known series
  is kept, since the sparkline cannot draw fewer either.
- **Capped at 24 points.** The card's sparkline is about 120px wide; beyond that they are pixels
  nobody can tell apart, carried per session every 4 seconds.
- **The card grows from 97px to 160px** (measured, the same at all three widths). **Fewer cards
  fit on screen** — about six or seven down to four on a phone. That is the trade the user asked
  for, written down here.

## Options rejected

- **A modal** (the shape of Cleanup / Archived): cheap, wrong for watching (decision 1).
- **Widening the left pane into cards**: collides with the withdrawn left-pane IA. The rail is
  rows; surfaces go on the right.
- **flex-wrap**: the last row stretches (decision 4).
- **"Last utterance" / "last updated" in v1**: not in the DTO. The transcript is heavy through
  the mirror, and a one-line summary added by the Agent has to be carried through four places in
  the control plane's relay (a field missing from `sessionWire` is dropped silently, docs/94
  §94.10). Deferred to P1 (→ built in decision 12).
- **Capping the last utterance on the Console's side**: it carries exactly as much. A card shows
  one line, so the cut is only worth anything **before** the wire (decision 12).
- **Wrapping the last utterance to two lines**: the card would grow and shrink with how much was
  said and the grid's rows would jump. Pinned to one ellipsized line (decision 12).
- **Leaving the context as text ("context 12%")**: the first version judged that a card had no
  room for the segmented bar. Measured, it fits at 274px. Replaced with `ContextBar` on the
  user's instruction (decision 13).
- **Drawing a card-sized gauge of our own**: a second arithmetic for "how full is it", which
  would drift (the reason behind decisions 5 and 7; decision 13).
- **Building the token trend in the Console from the transcript**: the list holds no transcript,
  and a `/messages` call per card is a 4-second poll times the number of sessions — the shape
  this ADR avoids above all (decision 13).
- **One trend point per transcript row**: simpler, but the mirror folds a run of rows into one
  block, so the same conversation would be drawn two different ways (decision 13).
- **The newest tool call ("editing foo.ts") / the question text while waiting** (P1's ② and ③):
  the user chose ①. ② says what a session is doing more directly, but it is only filled in when
  the tail of the transcript IS a run of tool records, and it competes with ① for the same single
  line. Decide once both have been measured.
- **Reordering inside a stage by the waiting ledger**: decision 6 (display only, decision 11).
- **Grouping projects by folder name**: the name is the user's, and a second clone of one
  repository would be split into a second project (decision 9).
- **Lifting the heading of a project that holds a waiting session**: whole sections moving
  changes where every box is (decision 9).
- **Filling the waiting time in with "0m"**: turning what the ledgers do not know into a number
  puts a lie under the decision of what to answer first (decision 11).
- **Doubling as the fleet overview diagram**: the "conversations × sessions × messages"
  relationship diagram that ADR 0027 split off and ADR 0041 decision 9 established the need for
  is **a different thing**. This view is a grid of cards; lineage is shown only as the left
  spine's colour (ADR 0073). Whether the diagram is needed is unchanged by this ADR.

## Impact

- Almost Console-only. The **Agent gains one field** (`gitx.Repo.RemotePath`, decision 9), and
  since the control plane passes `GET /api/repos` through, no relay point was added. Still no
  additional polling.
- **Decisions 12 and 13 cross all three legs** (Agent → control plane → Console): the Agent gains
  `session.Session.LastSay` and `TokenSpends` plus `claude.TailFacts` (new, `tailfacts.go`), the
  control plane two `sessionWire` fields (plus the contract table, the round-trip test and the
  golden), the Console two type keys and two rows on the card. **Still no additional polling** —
  they ride the existing 4 s sessions list, both facts come out of **one scan** of the
  transcript's tail, and an unchanged transcript costs one stat (decision 12).
- **The card is taller** (97px → 160px, measured), so fewer of them fit on screen at once.
- Touched: `layout/{types,migrate,ops}.ts`, `features/panes/{Pane,LayoutMap,paneTitle}`,
  `features/overview/` (new: view, card, pure functions, CSS, opener), `app/WsBar.tsx`,
  `features/keys/commands.ts`, `features/repos/{store,parentSync,RepoRow}`, i18n (ja/en),
  `workspace/agent/internal/gitx/git.go`.
- Tests: pure (filter, grouping, family order, stability, elapsed wording) and DOM (opens beside
  / in this pane on a phone, with the modifier and the wheel still opening another / the state
  chip in the head / the parent-diff chip / waiting elapsed / three menu routes / a dead session
  does not open), Go (`gitRemotePath`). The look was measured in headless Chromium against the
  README stub (docs/96).

## Phases

- **P0 (done)**: decisions 1–8 (2026-09-12, #567; the revision of decision 3 in #574).
- **P0.1 (done, this revision)**: decisions 9–11 — repository headings, family order, the card's
  shape, waiting elapsed.
- **P1① (done, this revision)**: the card's last utterance — decision 12. claude only.
- **P1② (done, this revision)**: the meta row rebuilt, with the mirror's gauge and token trend —
  decision 13.
- **P1.1**: the last utterance for the other kinds (codex's rollout, opencode's SQLite, …; their
  transcripts live in different places, so each needs its own measurement). The rest of P1 — ②
  the newest tool call and ③ the question text while waiting (decision 12's rejected options).
  `waitingSince` in the DTO, if decision 11's accuracy falls short. Text filtering inside the
  grid. A resume button directly on a stopped card.
- **P2**: the relationship diagram (the docs/44 follow-up) stays outside this ADR.

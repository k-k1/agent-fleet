# 0050. Transcript marks are addressed by "a root derived from the original turn + a quote + an occurrence number", stored on the Agent and delivered to shares by a separate path

English | [日本語](0050-transcript-marks.ja.md)

- Status: **adopted** (2026-08-19; P0–P2 implemented, not yet merged to develop). The record of the investigation is [docs/69](../log/69-transcript-marks.md).
- See also: [0039-fork-at-message.md](0039-fork-at-message.md) (the transcript's anchor is `anchorId`, not `idx`) /
  [docs/59](../log/59-session-sharing.md) §2 and §3 (RW is propose→approve; the shared DTO drops coordinates) /
  [0049-session-changed-files.md](0049-session-changed-files.md) (add a surface to an existing pane rather than a new PaneKind; do not add polling)

## Context

There is no way to point at "here" in a long conversation. Plans can be commented on from the DocView
side (`planComments.ts`), but nothing can be left on the conversation itself, i.e. the mirror's
transcript.

Meanwhile nearly all the parts exist. Positioning is done by `viewer/quoteMarks.ts` (equivalent to a
TextQuoteSelector), already in production use for plan comments; the transcript's rendering layer is
a **shared asset** between the mirror and the shared view; and `anchorId` is **already in the shared
DTO's allowlist**. The route for delivering owner-side state to a share has its precedent in handoff
proposals. All that is new to decide is where it is stored and how sharing works.

## Decision 1 — the anchor is a root derived from `anchorId`. Neither `idx` nor a group-relative part number

- `idx` moves under compaction (the Agent says so itself in `transcript.go`). A mark landing at a
  shifted position is **plausibly wrong** — the same reason fork chose `anchorId`.
- ⚠️ **A block-relative (`Group`) part number cannot be used either.** `groupTurns()` concatenates the
  parts of consecutive turns with the same role, so the number depends on "how many lines were folded
  into that block". The mirror and the shared view each have their own independent tail window
  (`WINDOW=400`), and **when a window starts in the middle of a block the numbers differ between the
  two sides**. That produces "one off, but only on the share" with no visible reproduction condition.
- Settled: a mark holds `{turn, part, kind, quote, nth, color}`, where `turn` is the `anchorId`, or
  `h:<hash of the body>` if there is none (FNV-1a — all we need is "both sides derive the same value
  from the same string"; no cryptographic strength is required). `part` is the number within the
  original turn (the turn's own body is `-1`).
  **`nth` is counted only within the rendered text of that one root (`"<turn>#<part>"`).**
- The root is carried to the rendering layer as `Group.origins?: string[]` (parallel to `parts[i]`).
  **Do not grow it on `Part`** — `partsOf()` returns `t.parts` by reference, so rewriting a part
  pollutes the turn state being held.

## Decision 2 — only "body text that passes through the shared DTO" can be marked. Do not let the allowlist be bypassed via the quote

The shared DTO deliberately drops `cwd`, `file` and the coordinates of edits. A mark's `quote` travels
to the share so the position can be restored, so **marking a file chip or a diff line would resurrect
the dropped coordinates as a `quote`** (it goes on the network even if it is not displayed).

- Rather than checking at send time, **make it structurally impossible to draw**: the roots a mark can
  be drawn on are the turn body `text` and parts with `kind=text`/`plan`/`answer`/`output`/`prompt`.
  File chips, `ToolTrace` paths, `ContextLine`, diff lines and the changed-files strip **do not show
  the selection pill**.
- **The same table is held in three places: the Console, the Agent and the CP.** Restricting where
  marks can be drawn is the Console's job, but the `kind` is checked again by the Agent when saving
  and by the CP when relaying. One side loosening is not enough to leak.
- **Rejected**: a net where "the CP drops, at relay time, any mark whose `anchorId` is absent from the
  share's transcript window". That would mean re-fetching the transcript every time marks are
  relayed, colliding head-on with "do not add per-surface round trips to the owner's workspace per
  share recipient" (decision 3).

## Decision 3 — the store is on the Agent (`session-marks/<name>.json`); do not duplicate bodies into the CP's database

The same placement as handoff proposals. `GET/POST/DELETE /sessions/{name}/marks`.
⚠️ **Register the same paths in the CP's `routes.go` too** — adding them only to the Agent means they
do not even reach the owner's own Console (a new Agent REST needs **both** the agent and the CP — a
hole we keep falling into).

For shares, a new `GET /api/shared-sessions/{id}/marks` is added, modelled on `handoffProposals`
(authorise → **the same rate-limit bucket as the transcript** → is the owner's workspace running →
`ownerGET` → the allowlisted DTO). **No new polling is created** — the mirror rides the transcript's
poll and the shared view rides the existing handoff-proposals poll, with the actual round trip thinned
to every 15 seconds.

## Decision 4 — an RW share writes directly, with no approval. A mark is an annotation, not an operation

The "propose → the owner approves" of [docs/59](../log/59-session-sharing.md) §2 is required because a
proposal **makes the agent act** (a side effect that uses someone else's session and tokens). A mark
does not reach the agent and does not enter the transcript. Queueing each one for approval would only
dilute what approval means.

- RW can write directly, but **only their own marks** (adding, and deleting ones they added).
- **The owner can delete anyone's marks** (they are stored in the owner's workspace). RO gets 403.
- The `id` is assigned by the caller and the Agent is create-only. A resend is a no-op, so no
  `X-Agent-Fleet-Operation-ID` ledger is needed (that exists for side effects that are a problem when
  executed twice).
- The ACL is evaluated every time. Someone demoted to RO cannot write from that moment. Marks they
  already wrote remain.

## Decision 5 — do not put colour and author on the same axis

Colour is what the user chooses to give meaning (important / needs checking / read later); the author
is a fact. Forcing them onto one axis makes colour meaningless in a conversation with three share
recipients.

- Colour = the `<mark>` background (four colours, with both light and dark in `tokens.css`).
- Author = an underline (a colour per author, drawn with `box-shadow: inset` — `border-bottom` changes
  the line height and makes the body jump) plus a card shown when the mark is clicked. **The main
  route is a list strip** — directly under the mirror's head, placed exactly like `FileChangeStrip`
  (as in 0049, **no new PaneKind**).
- No underline for the owner (slot 0). In a session used by one person, underlining everything is
  noisy, and who drew it only matters once a second person appears.
- The author's name itself does not appear in the body text (it destroys readability).

## Decision 6 — show the author's login id to other share recipients too. Do not hide it; announce it when the share is created

Being able to judge who drew a mark is the requirement, so it is not hidden. But share recipient A
learning share recipient B's login id is **a new exposure** (the owner shares individually, so A may
not know B exists). Rather than exposing it silently, `ShareCreateModal` states in one line that
"share recipients will see each other's names on marks".

**Rejected**: showing other recipients only as "a share recipient". It fails the requirement (knowing
who drew it), and the information disappears exactly where it is needed — a conversation with several
recipients.

## Decision 7 — separate the capability for display and for editing; do not use a `readOnly` flag

Without `TranscriptCaps.marks`, marks are not drawn and the selection pill does not appear. Per the
convention in `capabilities.ts`, **operational elements for an absent capability are not drawn** ("shown
but unpressable" looks like a bug, and a disabled button invites support questions).

But "can read, cannot draw" (an RO share recipient) does not split on the presence of a capability —
**displaying** marks is needed by readers too. That is carried in the wiring by `canEdit` (whether to
show the pill) and `canRemove(mark)` (whether to show a delete route for that mark).

## Impact

- The wire gains three Agent routes and three CP routes (the owner proxy) plus three (the share
  relay). The transcript DTO is untouched.
- `groupTurns()` gains one parallel array. The wire types of `Part` and `Turn` are unchanged.
- ⚠️ Delete the marks file on session deletion and slot reuse (`?reclaim=1`). Forgetting means **the
  previous session's marks appear on the new session**. `session-handoffs/` has the same structure,
  and checking during implementation showed it **was not being deleted**, so that was closed at the
  same time (`removeSessionSideFiles`). Neither goes into the cleanup archive — they are annotations
  about a conversation that is being deleted, and are not wanted even on restore.
- The stages are P0 (owner, mirror only) → P1 (deliver to shares) → P2 (RW drawing, author display,
  the list strip). All are implemented, and the checks on real hardware (long-press selection versus
  horizontal swipe on a phone, measuring the contrast, sharing between two accounts) are recorded in
  [docs/69](../log/69-transcript-marks.md) §69.11.

# 0049. Build the list of files a session changed by reconciling the transcript against git, and put it directly under the mirror's head

English | [日本語](0049-session-changed-files.ja.md)

- Status: **adopted** (2026-08-17). The record of the investigation is [docs/68](../log/68-session-changed-files.md).
- See also: [0046-drawio-viewer.md](0046-drawio-viewer.md) (**add a surface to an existing pane rather than a new PaneKind**) /
  [0039-fork-at-message.md](0039-fork-at-message.md) (the transcript's anchor) /
  [docs/59](../log/59-session-sharing.md) §3 (the shared DTO drops coordinates)

## Context

The Console has four surfaces that show "changed files" (the mirror's tool rows, the left rail's
Files → Changes, the SCM pane, and the command palette's `changed`), but all of them are on the
working-copy or repository axis and **not one is on the session axis**. Since several sessions use
the same working copy in turn, none of the existing surfaces can answer "what did this session
change?".

Meanwhile the wire vocabulary `transcript.Part` has had `File` (what was edited) and `Edits`
(before/after) from the start, and the Agent fills them in for claude / codex / opencode. The
Console's type even declares `file?: string` — **but it is never read anywhere in `console/src`.**
Half the plumbing already exists.

## Decision 1 — the list means **the transcript reconciled against git**; the transcript is the population, git is each row's state

The transcript alone leaves "edits that were already undone" sitting there, and git alone **erases
the session axis itself** (it becomes a rehash of the existing `FilesChanges`). Neither is anything
but a lie on its own.

- **The population of rows is the transcript's `Part.File`** (a record that the agent edited it).
- **Each row's state is decided by reconciling against `GET /fs/changes`** (unstaged / staged /
  untracked / no difference in the working tree / outside the working copy).
- ⚠️ **Do not delete rows with "no difference".** Deleting them produces "I just changed that and it
  is not in the list", which from the user's point of view is broken. Keep them, greyed out.
- Subdividing "no difference" is P2 (decision 12). Better to present it honestly undivided than to
  assert something false in P0.

## Decision 2 — the reconciliation key is `(repo, rel)`. Do not join on the browse-relative path

`toBrowseRel()`'s output is relative to `browseRoot()` (replaceable with `AF_BROWSE_ROOT`), while
`fs/changes`'s `path` is **always** `repos/<repo>/<rel>`. They match by default, but **they diverge
silently** in a deployment where `AF_BROWSE_ROOT` points somewhere other than home.

- The wire carries `repo` and `rel` **explicitly**. The reconciliation uses those two.
- The browse-relative `path` is carried only to hand to FileView.

## Decision 3 — aggregate **on the Agent side, over the whole transcript**. Include it in `/messages`; do not add an endpoint

The mirror only holds the transcript in a tail window (`WINDOW = 400`). Aggregating on the client
means **the count is short on a long session and grows every time you scroll up** — the kind of
breakage that is silently wrong.

- ToDos already set the precedent (`claude.CollectTasks(lines)` → `resp["tasks"]`, and `td.Tasks` on
  the generic path). `resp["files"]` is added in **the same shape**.
- No new polling. A second poll produces a desynchronisation where the strip and the tool rows are
  looking at different moments.
- The scanning cost rides the existing pass rather than adding a new one (we already sweep every line
  several times).

## Decision 4 — it goes in **a strip directly under the mirror's head**; no new `PaneKind`

One row is inserted into the `ViewHead → ContextBar → TaskChecklist` sequence.

- **The template is `TaskChecklist`** (re-keyed with `key={session}`, open/closed saved per session in
  `localStorage`, `DisclosureContent`). Something next to it that collapses by different rules looks
  like a different feature.
- **No PaneKind such as `sessionfiles` is added** — the same conclusion 0046 reached for `.drawio`:
  adding one surface is cheaper. It can be added when someone asks for a permanent tab (the strip's
  shape does not change).
- The default click is the diff; `Ctrl/⌘+Enter` opens in another pane (the palette's convention).
  ⚠️ **An untracked file has an empty diff**, so open the file itself (following the trap
  `FilesChanges` fell into).

## Decision 5 — add a fourth mode, `session`, to the command palette

Reuse the `changed` mode's rows and operations exactly, replacing only the population with the active
session's. The strip is the surface for "noticing while you look"; the palette is for "jumping
without lifting your hands" — two entrances to the same list. When there is no target session, or the
kind does not support it, **the mode is not offered**.

## Decision 6 — where the capability is absent, **do not show it** (do not show the strip at all)

Following the constitution in `transcript/capabilities.ts` (absent capability = NOT RENDERED).

- **The strip is not shown in the shared session view.** The shared DTO keeps the diff bodies but
  **drops the paths**, so there are no coordinates to open. What is added to `caps` is an optional
  field, and the shared side does not fill it.
- For kinds that do not emit `File` (kiro / agy / shell / ssm) the strip itself is not drawn.
  **An empty "0 items" strip cannot be told apart from "unsupported" and "genuinely zero".**

## Decision 7 — wire `caps.openDiff` into the mirror (closing an existing hole)

`ToolTrace`'s "expand in place" is a degraded path *for the shared view, which has no panes*, yet the
mirror runs through it too because `MirrorView`'s `caps` lacks `openDiff`. The mirror is on the side
that has panes, so pass it. Opening from the strip and from the tool rows then behaves identically.

## Decision 8 — fill in `Part.File` for cursor and copilot and add them to the supported kinds (done in P1)

Both have `path` / `file_path` / `target_file` in their inputs but were **folding them into a display
`Info` string and discarding them**. kiro and agy do not have them and are out of scope.

**Measurement (2026-08-17, against real transcripts left on disk) changed two estimates:**

- **before/after arrive too**, so `+N −M` can be shown ("we will not show diffs" is withdrawn).
  cursor `Write` = `{"path","contents"}`; copilot `edit` = `{"path","old_str","new_str"}`.
- ⚠️ **Detect by an allowlist of names. Do not use "anything that is not read is an edit".** With the
  latter, in a version where a name changed, **a file merely `Read`/`view`ed appears among the
  "changed files"** — the list falls to the side of silently lying. A miss in the allowlist merely
  means "the row does not appear".
- ⚠️ **On cursor's managed (ACP) path, do not look at names.** ACP has **the protocol itself
  classifying** via `tool_call.kind` (`read`/`edit`/`delete`/`move`/…) and `locations`. `title` is a
  display string like "Write /tmp/x", and recovering the name from it would be exactly the kind of
  string contract this feature is trying to avoid. before/after are taken **from the shape of the
  input alone**.

## Decision 9 — **count** forked history and subagent edits alike

- A fork carries the context over, i.e. it is the ground the session stands on, and for review
  purposes it is more correct to include it. If a boundary turns out to be needed, the room to cut at
  `ForkAt`'s anchor remains.
- Sidechains count too (what we want is what changed, not who touched it). They carry a marker and
  are surfaced in P1.

## Decision 10 — do not read "no Edits" as a deletion. Only a parser that knows declares a `Verb`

An optional `Verb` (`add` / `edit` / `delete`) was added to `transcript.Part`. codex reads
`*** Delete File:` so it can say — but applying the same inference to **kinds that do not carry diff
bodies at all** (cursor / copilot) makes **every file that agent touched a "deletion"**. The default
when we do not know is `edit`, and never `delete`.

For the same reason codex's rename sets `File` to **the destination path** (previously it was
`"<src> → <dst>"`, a display string and not coordinates you can open). The arrow stays in `Info`.

## Decision 11 — folding is incremental on the assumption of appending; three conditions invalidate it

`+N −M` is a line diff, so recounting the whole transcript on every poll does not work. The
transcript is append-only, so **only what follows the folded position** is folded. It is rebuilt in
three cases: the transcript's path changed, **the first record's fingerprint changed** (rewritten at
the same length), or it became shorter than what was folded.

⚠️ **For store-derived kinds, do not fold the last turn as final.** opencode keeps adding parts to an
existing message, so folding it **counts the same edit on every poll**. A mutable tail is folded into
a copy.

## Decision 12 — in P2 only **"committed" is asserted**; "reverted" is not

Decision 1 intended to split "no difference" into **committed / reverted**. Implementing it showed
that only one side can be split, so **only one side is done**.

- **Committed can be asserted** — there is the fact that the path appeared in
  `git log --since=<the session's creation time> --name-only`.
- **"Reverted" cannot be asserted** — there are other reasons for having no diff and not appearing in
  a commit (it was in a commit before the session started / a different working copy / a rename /
  it fell outside the `--max-count` window). Showing all of those as "reverted" would mean **the UI
  asserting something it has no grounds for**. The rest stays "no difference".

⚠️ **The grounds are a timestamp, so commits from another session running in parallel in the same
working copy are included too.** The harm is small because what it is reconciled against is limited to
**files this session edited** ("a file I touched was subsequently committed" is true regardless of who
committed it).

It lives at `GET /sessions/{name}/committed` (not on `/messages` — it runs git, so it would burden
every poll, and it need only be fetched when the strip needs it). If it is not a git working copy, if
the time cannot be read, or if the command fails, it **returns empty** (the badge simply does not
appear).

## Decision 13 — the route from the rail is "open the mirror and expand the strip"

Since no dedicated `PaneKind` is created (decision 4), the row menu's "changed files" **opens the
mirror after first writing the strip's per-session open/closed state (localStorage)**. It is the
cheapest shape that satisfies "I want to peek without opening the mirror" without adding a surface,
and the strip's implementation needs to know nothing about it.

## Impact

- Agent: `CollectFiles` in `session_transcript.go` (the claude path plus the generic path).
- Console: a new `features/mirror/FileChangeStrip.tsx`, two additions to `MirrorView.tsx`'s `caps`
  (`openDiff` / `sessionFiles`), one mode in `CommandPalette.tsx`, and i18n in both en and ja.
- The existing surfaces (`FilesChanges` / `ChangesView` / the palette's `changed`) are **unchanged**.
  They coexist as things on a different axis.

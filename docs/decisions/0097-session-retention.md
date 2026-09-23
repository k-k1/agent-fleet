# 0097. Retaining stopped sessions — the window stops deleting and starts shelving

English | [日本語](0097-session-retention.ja.md)

- Status: **accepted** (2026-09-20). Built in the same change. Every "exists" / "does not exist"
  claim was checked by grep against `b70737b6` merged with `origin/develop`, and the behaviour is
  pinned by `TestSessionLockSurvivesTTLSweep` — including the positive control that the test goes
  red when the archive write is mutated back into a delete.
- Number: 0096 is taken by another lane that has not merged yet, so this is 0097.
- Related: [0028](0028-deletion-lock.md) (the deletion lock reaches automatic deletion too —
  decision 4 removes one of its premises) / [0073](0073-session-spawned-sessions.md) decisions 6
  and 13 (the child-session budget, and archiving being a person's action — decision 1 leaves the
  budget's self-healing to this sweep) / the investigation is
  [docs/log/102](../log/102-session-retention.md)

## Context

A stopped (exited) session stayed in the list, and once `AF_SESSION_STOPPED_TTL` (7d by default)
had passed the listing handler deleted it, meta and all. Held against archive and delete, that
automatic prune did not fit.

**It was the only deletion in the product that skipped the safety net.** We declare that a
deletion always goes through the gz archive first (the header of `cleanup_ops.go`, the cleanup
modal, and the user guide's "you can get it back if you delete too much"), and the manual
`delete_session` does exactly that. The prune called `RemoveMeta` directly, and took
`MaybePruneWorktree` down with it on the same line. **The automatic path was more destructive
than the manual one, with no way back.**

**And it reclaimed nothing.** The prune removed the meta only: the transcript jsonl, the terminal
history, the handoff proposals, the marks and the cached translations all stayed
(`removeSessionSideFiles` is reachable from `?reclaim=1` alone). **The way back to the
conversation was destroyed while the data stayed on disk.**

**It also read backwards to the user.** The guide's table makes "stop" sound like the safest
thing and "archive" like tidying away, whereas in fact **archiving is what keeps a session
forever and leaving it stopped is what destroys it, irrecoverably, in seven days.** The figure 7
appeared nowhere a user could see.

The full comparison, and the four things that looked broken but were already handled (restore
resets the clock, the lock covers automatic deletion, the budget ignores archived children, the
CP quota counts only live ones), are in docs/log/102 §102.2.

## Decision

### Decision 1 — the window stops deleting; it moves the row to the shelf

`StoppedTTL` expiring now writes `Archived = true`. The meta and the transcript stay, and the
user restores it from the archive list. **No irreversible deletion happens in this product
without a person asking for one.**

The ground for it is counting what the automatic deletion actually bought. Two of the three
things are bought by moving alone; the third was never bought at all.

| What the automatic deletion bought | Does moving alone buy it? |
|---|---|
| The active list does not fill up with stopped rows | Yes (they go to the shelf) |
| A child session's slot frees itself | Yes (`countChildren` ignores archived rows, so the slot frees on day 7 — **the same moment as before**) |
| Disk reclaimed | **Never bought it** (the jsonl and the side files stayed) |

ADR 0073's "three things that free a slot" changes in **wording only** (TTL prune expiring →
auto-archive). **The refusal sentence "a child left stopped frees its slot in 7 days" stays
true**, at the same moment.

The clock starts where it started. While the workspace is down nothing stamps `StoppedAt`, so the
window opens when it next wakes and a `GET /sessions` arrives — the table in docs/log/75 survives
unchanged.

### Decision 2 — deleting is a person's action, and reclaiming always goes through the gz archive

The only thing that removes a session's substance (meta + jsonl + side files) is
`delete_session` (`DELETE ?reclaim=1`), which keeps bundling through `archiveSessionForDelete`
first. The cleanup modal's shelf is its one entrance. **"A deletion always goes through the gz
archive" becomes true without exception** — the automatic path used to sit outside that promise.

### Decision 3 — no clock on the shelf, and no `ArchivedAt` / `ArchivedBy`

The first proposal was two stages (auto-archive at 7 days, auto-reclaim 7 days later). It is not
taken, and not only because it contradicts decision 2: **it silently breaks what a manual archive
means.** A user archives a session because they want to keep it, and putting a clock on the shelf
**destroys the thing they deliberately shelved**. Saving that requires telling "arrived
automatically" apart from "a person put it here", which drags in an `ArchivedAt` stamp, a
migration for existing rows, and a guard against doing both transitions in one sweep. Dropping
the second stage **drops all of it**.

`Archived` stays the single bool it has always been.

### Decision 4 — a locked stopped row is exempt from the sweep itself

The deletion lock (ADR 0028) lets reversible operations through (halt / archive), so now that
this sweep archives, the lock does not have to refuse it. It still does, because **pinning a row
says "I want to keep seeing this"**, and the shelf is out of sight — moving it there defeats the
point of the pin. ADR 0028 decision 2 (make the lock reach automatic deletion) now **covers a
smaller set**: the worktree auto-prune and the manual deletions.

### Decision 5 — worktrees leave the sweep and belong to the cleanup modal's stage ②

The prune used to call `MaybePruneWorktree` on its way out. Deleting the working copy of a
session that is still restorable is the worse failure (restoring it leaves nowhere to work), so
the sweep does not touch working copies. Worktree removal is left to the cleanup survey that
already exists, where a person decides against a safety grade. **The price is that worktrees
accumulate more than before if nobody sweeps**, and we accept it.

### Decision 6 — put the seven days where a user can read it

Something automatic that a user cannot see is the same hole ADR 0073 refused as an "invisible
limit". The guide's "stopping and tidying up sessions" table says that a stopped row moves to the
archive when the window is up, and that **nothing removes it from there on its own**.

## Rejected

### Auto-archive at 7 days, auto-reclaim 7 days later (two stages)

The reason is decision 3. The essential defect is that a manual archive becomes time-limited, and
even with the extra field, the migration and the guard, **an irreversible deletion nobody asked
for merely moves seven days later**. The disk it reclaims is a new gain rather than the status
quo, because the prune never reclaimed any — and decision 2's manual path already offers it.

### Zero automation (do not even move)

A stopped child keeps holding its slot, and **a child cannot archive itself** (ADR 0073 decision
13 — archiving is a Console-only action by a person). If nobody opens the Console, the parent can
never `create_session` again. Taking this would mean dropping stopped children from
`countChildren`, i.e. re-deciding ADR 0073 decision 6, and undermining its ground that a stopped
child holds a conversation that can be resumed. That is outside this ADR.

### Route the prune through the gz archive and keep deleting

Inconsistency 1 is fixed, 3 is not. From the user's side a session still vanishes after seven
days, and **nothing tells them it went to the bin** (the bin lives inside the cleanup modal and
raises no notification). The shelf sits next to the list and has a restore path — a visible
reprieve is the cheaper answer.

### Reclaim the jsonl too, so the automatic deletion becomes a real cleanup

Disk goes down, but it is still an unrecoverable deletion performed automatically, and a more
thorough one. Inconsistency 2 was not "the automation is half-done" — it was a symptom of there
being no reason to delete automatically at all.

## Consequences

- **Worktrees stop disappearing on their own** (decision 5). What used to be dragged down with a
  stopped session moves to the cleanup modal's stage ②. It goes in the release notes as a
  behaviour change.
- **The shelf only grows.** Archived rows were already TTL-exempt and only grew, so this is not a
  regression, but it becomes the only thing that grows. The cleanup modal reclaims it.
- **A child's slot frees at the same moment as before.** The refusal text, the tool descriptions
  and `list_child_sessions` need no change.
- **Five comments stop being true** (the `StoppedTTL` doc, `countChildren`,
  `mcpListChildSessions`, the header and the archived branch of `session_cleanup.go`). Left
  standing, the next design builds on them — `cleanup_ops.go`'s "slot names get REUSED" is how
  ADR 0086 decision 3 had to be rewritten. They are fixed in the same change.
- **Two `clean.reason.*` strings** (ja / en), plus the user guide and `docs/build/04-agent` in
  both languages.

## Open

- **A shared session that gets shelved answers 409 `owner_session_archived` to its viewers.**
  While this was a deletion the meta disappeared outright, so what a viewer sees changes from
  "gone" to "the owner folded it away" — not a regression. Whether sharing should hold the sweep
  off (i.e. whether a share is a statement of intent to keep) waits on how sharing is actually
  used.
- **The shelf's inventory is invisible.** A count and a size in the cleanup modal would let
  someone decide to reclaim. Not in this ADR: decision 2's entrance already exists, so this is a
  display question, not a feature.

## Addendum (2026-09-23) — the cache of deleted sessions is the one delete without the gz archive

Decision 2 says a deletion always goes through the gz archive. PR #919 adds a delete that does
not: the cleanup modal's **"Cache of deleted sessions"** removes `~/.cache/agent-fleet/pasted/<sid>`
(files pasted or attached into a session), `pasted/chat-<id>` (the same for an assistant chat) and
`codex-view-image/<sid>` (images codex's view_image read) once nothing can refer to them.

**Why this is an exception and not a breach.** Decision 2 protects *a session's substance* — what
a restore brings back. These directories belong only to sessions that are already beyond restore.
There are two rules, one per kind of owner (`internal/sessionx/cache_orphans.go`):
- **A session's directory** (`pasted/<sid>`, `codex-view-image/<sid>`) is offered only when its UUID
  is in **no session meta** (live, stopped or shelved) **and in no archive in the trash**. A session
  name is a random slug that is never reused and the UUID is a pure function of (dir, name), so such
  a UUID can never be named again. Anything a trashed session could still need stays until that
  archive is purged.
- **An assistant chat's directory** (`pasted/chat-<id>`) is offered only when the conversation file
  **provably does not exist** (ENOENT). Chats are not sessions and have no trash — deleting a chat
  removes its file outright — so there is no archive to consult; a file that exists but cannot be
  parsed keeps its directory.
- A directory of any other name is never offered. Archiving the images instead would not work anyway: they do not compress, and a restore
reads a whole archive into memory.

**What still holds.**
- It is **a person's action**, like every other delete here: nothing removes these on a timer.
- It is graded **safe** because "provably done" is that grade's other half, and unreachable is
  provable. It is the only safe row that cannot be undone, so its action label, its reason and
  the confirm dialog all say so.
- The scan **deletes nothing it cannot prove unreachable**. An unreadable meta or archive stops it
  outright. A directory the walk could not read to the end — an I/O error, or the entry budget
  running out — is neither counted nor deleted. Both meta names protect a directory: the file name
  the paste endpoint keys by, and the name inside it that codex keys by.
- **It cannot act outside the cache.** The feature directory must not itself be a symlink, and it
  is pinned by file descriptor (`os.Root`) for the whole scan and delete, so a swap in between
  cannot aim the delete elsewhere. Symlinks further up (a `~/.cache` kept on persistent storage via
  `AF_WS_KEEP_DIRS`) are trusted: they are the workspace's own setup.
- **It is bounded.** One entry budget (500,000 by default) pays for everything — listing the cache,
  every meta and archive read for reachability, every chat lookup, every entry walked. A scan that
  runs out says so: the cleanup row is marked partial (or becomes a keep row if nothing could be
  decided), and a delete reports what it took so the next survey shows the rest.
- **It does not race a restore.** A restore reads the archive and writes the transcripts outside
  the cleanup lock, then — under it — checks that the archive still exists and writes the metas
  back. The purge and the cache delete take the same lock. So a delete can never scan a session
  that is in neither place, and a restore that lost a race to a purge fails instead of bringing a
  conversation back without its files.

`generated/` is outside this: pictures are products, and they already age out after 30 days.
The Open item about the shelf's inventory is partly answered: Settings → Machine now shows the
trash's size and opens the cleanup modal.

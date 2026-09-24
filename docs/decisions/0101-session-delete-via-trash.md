# 0101. Every session delete goes through the trash — and deleting a worktree is not deleting its sessions

English | [日本語](0101-session-delete-via-trash.ja.md)

- Status: **accepted** (2026-09-24). Built in the same change. The design history, the inventory of
  entry points and the trash measurements are in
  [docs/log/115](../log/115-session-delete-via-trash.md). The user took the recommended option on all
  six decisions of §115.7.
- Follow-ups: #950
- Related: [0097](0097-session-retention.md) (retaining stopped sessions — this ADR makes its decision 2,
  "deleting is a person's action, and reclaiming always goes through the gz archive", true without
  exception) / [0028](0028-deletion-lock.md) (deletion lock) / [0096](0096-fleet-session-graph.md)
  decision 6 (a person's delete also erases lineage — unchanged)

## Context

Three routes forgot a session's meta, and only `DELETE /sessions/{name}?reclaim=1` went through the trash
(the gz archives under `~/.local/share/agent-fleet/cleanup/`).

- `POST /sessions/{name}/stop` forgot the meta, kept the transcript and side files, and skipped the trash.
  It could also remove the worktree it was the last session of, through `MaybePruneWorktree`. Its callers
  were the shell / ssm row "Delete", the bulk tidy of a repository row and of orphans, "Delete the working
  copy" (the shell/ssm part of all of these), and the image studio's "Switch agents" (an AI session).
- `DELETE /repos/{name}?prune_sessions=1` (cleanup modal ② and MCP `delete_worktree`) forgot the metas of
  the stopped sessions in that worktree, skipping the trash.

The same "delete" in the user's eyes could or could not be undone depending on where it was pressed. The
deletes that could not be undone did not reclaim space either (the transcript stayed), and they made the
session's pasted images cache-orphan candidates at once (ADR 0097 addendum). The guide said "Deleting too
much is recoverable"; the shell delete's confirmation said "This can't be undone". ADR 0097 decision 2's
"true without exception" did not hold.

Stopping, deleting and tidying worktrees were also tangled both ways: deleting a session removed a
worktree, and deleting a worktree removed sessions.

## Decision

### Decision 1 — there is exactly one way to forget a session's meta: move it to the trash

The Agent's `trashSession` becomes the only route.

1. Settle usage with `finalizeSessionUsage`.
2. Put the meta and the transcript (claude) into one gz archive. **The terminal history does not go in.**
   It is short-lived on purpose (`/tmp` by default; its retention is the tenant's setting
   `AF_TERMINAL_HISTORY_RETENTION_DAYS`, where 0 is a policy of deleting it), and moving it into a trash
   with no expiry would break that policy. Restoring a shell / ssm from the trash brings back its row (meta).
3. Only once the archive is written, remove the transcript, meta, lineage, side files, terminal history,
   managed ledger and status records. **If the archive fails, nothing is removed.**
4. Leave the worktree alone (decision 3).

Deletes of one name run one at a time, and archive ids never overwrite each other. Right before removing, under the
meta lock, it checks again for a lock, a running session, or a transcript that grew since it was archived; any of
them removes nothing and withdraws the archive (409 `session_resumed`). A deleted meta is not written back by a
stale snapshot in that process (only a restore from the trash brings it back). See the review record,
docs/log/115-review.md.

`POST /sessions/{name}/stop` and `DELETE /sessions/{name}` (with or without `reclaim`) both go through it.
`DELETE` refuses a live session with 409; `/stop` and `DELETE …?stop=1` first stop it the way halt does.
`/stop` stays as a compatibility name so that an older Console is safe too; a new Console uses
`DELETE …?stop=1`. The CP audits `DELETE /api/sessions/{name}` as `session.delete` (and `/stop` under the
same name).

### Decision 2 — the row menus keep their items; the shell / ssm "Delete" moves to the trash

AI session rows still get no "Delete" (shelve, then delete from the shelf — the two steps stay). The shell /
ssm "Delete", and the shell/ssm handling of the bulk tidy, the orphan tidy and "Delete the working copy", go
through decision 1. The image studio's "Switch agents" **stops** the previous session instead of deleting it (`/halt`; develop's c7fe0ccd9 made the same call first).

### Decision 3 — deleting a session never deletes a worktree

`MaybePruneWorktree` is removed. Worktrees are removed only by cleanup ②, "Delete the working copy" in the
left pane, and MCP `delete_worktree`. The cost: a worktree whose last shell session was deleted stays for
cleanup ② (the same cost ADR 0097 decision 5 accepted).

### Decision 4 — deleting a worktree shelves the stopped sessions in it

After removing the working copy, `DELETE /repos/{name}` handles the stopped sessions in it as below. With or
without `prune_sessions` makes no difference (the parameter is accepted and ignored).

| Session | Handling |
|---|---|
| live | the delete is refused with 409 (as before) |
| locked | the delete is refused with 403 (as before) |
| on the shelf | untouched |
| stopped AI session | **moved to the shelf** |
| stopped shell / ssm | **moved to the trash** (decision 1 — a shell without a working directory is not worth shelving), BEFORE the working copy is removed; if that fails, the working copy is not removed either (500 `sessions_trash_failed`) |

The person deleting a worktree decided about the working copy, not about throwing conversations away.
Throwing one away is a separate operation: deleting it from the shelf. The confirmation does not offer a
"shelf / trash" choice (shelf to trash is one press).

A working-copy delete runs from its guards to settling its sessions inside a deletion gate that the lock endpoints
also take, so a lock set during the delete either takes effect before it or waits until it is over.

A session whose working folder is gone can still be read, on the shelf or in the list, but not resumed
("Folder missing").

### Decision 5 — operator permissions and approvals do not change

`delete_session` and `delete_worktree` stay visible only to an operator with `writeEnabled()` and still ask
for approval in turns driven from Discord. Only `delete_worktree`'s description changes, to match decision 4.
No new tools.

### Decision 6 — no expiry on the trash; add a person-pressed "delete permanently: older ones"

As ADR 0097 decisions 2 and 3 say, nothing is deleted automatically. The cleanup modal's trash tab gets
"Delete permanently: older than N days" (default 30, count and size in the confirmation), and the trash
figure in Settings → Machine gains the count and the oldest date.

### Decision 7 — existing meta-less transcripts are not migrated

Transcripts left by `/stop`, `prune_sessions` and the TTL prune before ADR 0097 cannot be put back into the
trash: their names are unknown. They cannot be told apart from transcripts that were never fleet sessions
(claude run by hand in a shell, for example) either. Count them first, then decide (docs/log/115 §D7 (a)–(c)). **The counting is not in this change**: unless
claude's sid re-mapping (`LiveSID`), forks and subagent files are handled correctly the count itself would be
wrong, so it comes separately.

## Rejected

- **Switch only the Console to `DELETE ?reclaim=1` and keep `/stop`.** Older Consoles and other callers keep
  deleting irreversibly.
- **Trash the sessions of a deleted worktree.** The person deleting a worktree did not say to throw the
  conversations away.
- **An expiry on the trash.** Collides with ADR 0097 decisions 2 and 3.
- **Add "Delete" to AI session rows.** Breaks the two steps through the shelf, and is not needed for the goal
  (every delete goes through the trash).

## Consequences

- shell / ssm deletes become restorable; the trash grows a little.
- Deleting a session leaves its worktree; cleanup ② gets a few more candidates.
- Deleting a worktree keeps its conversations on the shelf.
- The cache orphan rule does not change: deleted sessions sit in the trash, so their pasted images are
  protected until the archive is purged.
- The audit action changes from `session.stop` to `session.delete`.
- ADR 0097's background ("the only one") and decision 5, and ADR 0028's "worktree auto prune", get correcting
  addenda.

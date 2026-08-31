# 0028. The deletion lock — enforce it in the Agent's REST layer, and make it apply to automatic deletion too

English | [日本語](0028-deletion-lock.ja.md)

- Status: **adopted and implemented**. The design is [docs/45](../log/45-deletion-lock.md).
- See also: [0012](0012-go-internal-refactor.md) (the Agent's internal structure) / [0021](0021-scheduled-execution.md) (automatic execution) /
  `workspace/agent/cleanup_archive.go` (cleanup and the gz safety net — it has no design document of its own)

## Context

Sessions, working copies (worktrees) and assistant conversations **can be deleted in several
ways**. Beyond the deletions a person presses (the row menu in the Console, the cleanup modal),
there is the 7-day TTL automatic prune of stopped sessions, the automatic prune of a worktree
whose sessions are gone, the operator (MCP `delete_session` / `delete_worktree`) and requests
arriving through the chat bridge. There was no way for a user to say "I do not want this deleted",
so a conversation worth keeping or a long-lived working copy could disappear in automatic tidying.

## Decision

**Give each kind of target a `locked` flag, and refuse in the Agent's REST handler.**

1. **Enforcement is in the REST layer.** Disabling the Console's button is only an aid — the
   operator, the bridge and plain REST all go through the same handler, so stopping it there stops
   it uniformly regardless of the entrance. The refusal is `403` with the stable codes `locked` /
   `locked_sessions`.
2. **It applies to automatic deletion too.** Letting the TTL prune and the automatic worktree prune
   through would make the lock nothing more than a mis-click guard. What we want to protect is
   *not disappearing as time passes*, so the automatic paths are precisely what must be covered.
3. **`force` does not override a lock.** `force=true` on a dirty worktree expresses "delete it,
   I know there are uncommitted changes", but a lock expresses "do not delete this", and the one
   set later is the stronger. The only way past it is to unlock.
4. **Reversible operations are not blocked.** `archive` (restorable) and `halt` (the row remains)
   pass. A lock stops only operations that destroy the thing itself — otherwise it becomes "once I
   lock it I cannot tidy up", and nobody uses it.
5. **It is stored on whatever naturally owns the target.** A session's is in `Meta`, a
   conversation's in the conversation JSON. Only a working copy has no AF-owned metadata, so it
   goes in an external ledger, `~/.config/agent-fleet/locks.json`, keyed by **absolute path**
   (the automatic prune knows only the directory, not the name).
6. **The cleanup review does not hide them — it shows them as `keep`.** Removing them from the
   candidate list makes it impossible to tell why things are not being tidied, and leads the
   operator to propose a tool call that will 403.

## Impact

- Three new APIs (`POST /{sessions|repos|chat/conversations}/…/lock`) plus registration in the CP
  allowlist.
- `Session.locked` / `Repo.locked` / `ConversationMeta.locked` are added to the wire
  (`omitempty`, compatible with existing clients).
- The Console shows a lock badge on the row, switches the menu, disables the delete item and
  excludes them from bulk operation counts.

## Options rejected

- **Disabling it in the Console only**: the operator and plain REST walk straight past. It is not
  protection.
- **Reinterpreting "locked" as "archived"**: archive is reversible hiding, which means something
  else. Relying on its side effect of TTL exclusion (archives are not pruned) is accidental
  protection, and the intent cannot be read from it.
- **Putting a lock file inside the working copy**: it dirties `git status`, and it is deleted along
  with the worktree.
- **Adding a confirmation dialogue on every deletion**: it does nothing for automatic deletion, and
  only adds confirmation fatigue on the manual side.

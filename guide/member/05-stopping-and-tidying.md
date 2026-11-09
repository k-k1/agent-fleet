---
audience: "anyone stopping, resuming or clearing away sessions"
---

# 05. Stopping, resuming and tidying up

English | [日本語](05-stopping-and-tidying.ja.md)

This chapter has moved into task pages:

- [Stop and resume a session](../user/sessions/stop-resume.md)
- [Stop when done, or keep a session awake](../user/sessions/auto-stop.md)
- [Clear away finished sessions](../user/sessions/cleanup.md)
- [Stop a session that spends too much with a budget](../user/sessions/budget.md)
- [Hand off to another session or agent](../user/sessions/handoff.md)

## Stopping and tidying up sessions

Which operation to choose, and how to stop a session: [Stop and resume a session](../user/sessions/stop-resume.md).

### Tidying up in bulk (cleanup)

The Clean up window, its safety ratings and the trash: [Clear away finished sessions](../user/sessions/cleanup.md#tidy-up-in-bulk-cleanup).

### Clearing one finished job (deleting a working copy)

[Clear away finished sessions](../user/sessions/cleanup.md#delete-one-jobs-working-copies).

### A spend budget — pausing a session that spends too much

[Stop a session that spends too much with a budget](../user/sessions/budget.md).

## When you can — and can't — resume

Resuming, and recreating a deleted working copy: [Stop and resume a session](../user/sessions/stop-resume.md#resume-a-session).

### A claude session whose conversation log was deleted

claude keeps each conversation as a log in the workspace and, left to itself, deletes logs older
than 30 days. Agent Fleet turns that age-based deletion off in the workspace's claude settings
(in new and existing workspaces alike), so a session you stopped (not archived, not deleted)
keeps its conversation however long it sits. Which logs go is decided by Agent Fleet's own
lifecycle (archive → delete → cleanup), not by claude's age sweep. A retention period you set in
claude yourself is never overwritten, though a short one brings the age-based deletion back, and
setting it to `0` makes claude stop saving conversations at all.

On top of that, Agent Fleet keeps its own backup of a stopped session's log, out of claude's reach.
When you resume a session whose log is gone, the backup is put back and the conversation
continues with its history; the conversation then notes that it was restored, since its latest part
may be missing if the backup is older than the lost log. A log that still exists is not replaced by the backup, except one that holds no conversation yet: that one is set aside and the backup is restored. There is one
backup per session, removed when you delete the session; backups set aside by **"Start a new
conversation"** stay until the session is deleted, and backups of sessions deleted while the
Agent was not running are not cleaned up automatically. The backup is made while the session is
stopped, so a log that disappears before the first backup (a session stopped only seconds ago, or
one that last ran before this existed) cannot be recovered this way.

A deleted session's conversation lives in the cleanup archive, which always includes a snapshot
of the backup. Restoring the archive puts back the live log if it is complete and newer than the
backup, otherwise the backup; the other is kept next to it. If a log cannot be read (for any
reason other than having just vanished), the delete is refused.

If there is no usable backup either (deleted before this protection existed, or by a retention
period of your own), **"Resume"** does not silently start an empty conversation under the old
session. It shows **"This session's conversation log was deleted…"** with a **"Start a new
conversation"** button. Pressing it starts a fresh conversation in the same session (for a forked
session, without copying its source again), and sets any older log or backup aside instead of
restoring from it. If that cannot be done, the button reports an error and nothing changes. A
session that never exchanged a message resumes as before. Limit: a deleted log is recognised only
for sessions Agent Fleet has a record of a finished turn for. An old session with no such record
is indistinguishable from one that never talked, so if its log was already gone it still starts
empty without a warning.

### The warning when the branch has been swapped

[Stop and resume a session](../user/sessions/stop-resume.md#the-row-shows-a-warning-icon-and-a-branch-name).

## Continuing elsewhere — handoff, branch, move

[Hand off to another session or agent](../user/sessions/handoff.md).

### Handing a conversation off (handoff)

[Hand off to another session or agent](../user/sessions/handoff.md#hand-off).

### Moving a session to another agent (move)

[Hand off to another session or agent](../user/sessions/handoff.md#move-to-another-agent).

## Keeping a session awake

[Stop when done, or keep a session awake](../user/sessions/auto-stop.md#keep-a-session-awake).

## Having a session stop when it is done

[Stop when done, or keep a session awake](../user/sessions/auto-stop.md#stop-when-it-is-done).

## Locking something against deletion

[Clear away finished sessions](../user/sessions/cleanup.md#lock-something-against-deletion).

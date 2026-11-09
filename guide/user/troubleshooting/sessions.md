---
audience: "anyone whose session is missing, will not resume, or whose sharing or scheduled run does not behave"
source_of_truth: "the session rows in the Console's left pane and the messages they show"
---

# Find a missing session or one that will not resume

English | [日本語](sessions.ja.md)

Find the cause and the fix when a session has vanished from the list, will not resume, stopped partway, or when sharing or a scheduled run does not work.

## Sessions or repositories have vanished from the left pane

First check that the bar at the very top of the left pane says **"All"**.
With a working set selected, repositories, conversations and sessions outside that set are not shown. Nothing was deleted: switch back to "All" and they return.

If it is still missing, check these:

- **Was it archived?** You can restore it from the archive list
- **Was it tidied away by cleanup?** A deleted session can be brought back from the trash

Both are explained in [Clear away finished sessions](../sessions/cleanup.md#archive-and-restore).

## "Couldn't refresh the list — showing the last known one"

This appears under **"Repositories"** in the left pane. Nothing is gone: the repository list could not be read just now, so the last one is shown.

- If the workspace simply could not be reached, the Console keeps retrying and the list comes back by itself
- If the workspace answered with an error, nothing retries. With no list to show, it says **"Couldn't load the repository list"**. Reload the page, or check your access to this workspace

## "Folder missing — can't resume"

The working folder the session was started in is gone. This usually follows deleting a working copy together with its worktree.
You can recreate the lost worktree at its old path and bring the sessions back: see [Stop and resume a session](../sessions/stop-resume.md#folder-missing-cant-resume-and-the-row-will-not-open).

## A claude session will not resume because its conversation log was deleted

Pressing Resume shows **"This session's conversation log was deleted, so it can't be resumed. …"** and **"Start a new conversation"**.
claude's conversation log is gone, and Agent Fleet has no backup of it either.

This is rare, because Agent Fleet protects conversations in two ways:

- The workspace's claude settings stop claude from deleting old logs by age
- Agent Fleet keeps a backup of each stopped session's log and puts it back on resume when the log is gone. The conversation then says **"This conversation was recovered from the Agent's backup because its log was deleted. …"**

The message appears when the log vanished before the first backup, or when you lowered `cleanupPeriodDays` in your own claude `settings.json`.
Setting `cleanupPeriodDays` to `0` makes claude stop saving conversations at all.

- **"Start a new conversation"** continues in the same session without the old context. Any old log or backup that was left is set aside, not deleted
- If you lowered `cleanupPeriodDays` yourself, remove it

An old session for which Agent Fleet has no record of a finished turn cannot be told apart. It starts with an empty conversation without a warning, even if its log is gone.

## "Couldn't restore the conversation log from the backup"

There is a backup, but putting it back failed: an unreadable folder, a full disk, or another log that appeared meanwhile.
The backup is kept, so fix the cause and resume again.
An empty log that was in the way may have been moved aside as `.superseded.<number>`.

## A turn was cut off partway

A provider error, a dropped connection or a usage limit can end a turn partway. You get a notification, so the conversation is not left half-done without you knowing.

A cut-off that time will cure is resumed from where it stopped by these settings. All of them are on by default.

| Setting | What it resumes |
|---|---|
| **"Auto-resume a cut-off turn"** in ⚙ Settings → Agents | A Claude turn cut off by a dropped connection, a temporary rate limit or a timeout. Up to twice in a row |
| **"Auto-resume after a usage limit resets"** in ⚙ Settings → Agents | Claude and Codex (Managed) stopped at a usage limit. Once, when the limit resets |
| **"Auto-resume interrupted turns"** in ⚙ Settings → Assistant | A turn of a session the assistant directed. Up to twice in a row |

A cut-off caused by the request itself, an exhausted balance or an over-long prompt fails again if repeated, so nothing resumes it.
Read the notification or the error, fix the cause, then send again.

## A session cannot be moved to another agent

A session that is working, or that has a question, plan or approval waiting for you, cannot be moved. Let the turn end or stop it, and answer first.

- A stopped Managed Cursor session cannot be moved, because its conversation is gone
- shell and ssm sessions have no conversation to move

Which sessions can be moved is in [Hand off to another session or agent](../sessions/handoff.md#which-sessions-can-be-moved).

## Cleanup will not remove something

Cleanup leaves alone the candidates rated **"Keep"**. The row says why.

| Reason | What to do |
|---|---|
| Running | Stop the session first |
| Uncommitted or unpushed | Commit or push, and it becomes "Safe" or "Review" |
| Delete-locked | Clear the lock from the session's ⋯ menu |

How to remove it anyway, and how to bring back what you removed, is in [Clear away finished sessions](../sessions/cleanup.md#when-it-goes-wrong).

## You shared it, but the recipient sees nothing or only old conversations

- **Check the scope you shared.** A session share covers that one session. If you give each session its own worktree, share at **project** scope (the base and the sessions in the worktrees under it) so everything shows
- **Archived sessions drop off the recipient's list.** The share itself remains, so restoring the session from the archive makes it visible again
- **While your workspace is stopped, the recipient cannot read the history.** Their screen says so
- The list refreshes now and then. To update it right away, press **"Refresh"** in the shared session

How to share is in [Working with others](../../member/12-collaboration.md#sharing-a-conversation-shared-sessions).

## You sent something from a shared session, but the agent does nothing

With **"Can propose"**, what you type **does not reach the agent until the owner approves it**.
The owner's shared session shows **"N pending approval(s)"**: ask them to check it and press **"Approve and send"**.
With **"View only"** there is no composer at all.

If a send could not be confirmed, it is not sent again automatically, so it cannot arrive twice. Use **"Check result"** to see where it stands.

## A scheduled run does not fire

Check its row under **"Schedules"** in the left pane.

| You see | What it means and what to do |
|---|---|
| A **"Paused"** tag | It is paused. **"Resume"** in the row menu brings it back |
| **"Skipped"** in the **"Run history"** | It was passed over: the previous run was still going, the target conversation was busy or gone, the tenant had reached the number it may run at once, and so on. Hover over the row to see the reason |
| The time or the prompt is wrong | Fix it under **"Details / edit"** in the row menu |
| There is no "Schedules" section | Scheduled runs are disabled on this deployment |

**"Run now"** checks that it works the same way a timed run does. It can take about a minute to start.
The details are in [Running many sessions](../../member/11-many-sessions.md#scheduled-runs).

## No notifications arrive

- Check that the browser allows notifications from the Console
- While that session's screen is open, its notifications are not shown
- shell and ssm sessions do not send state notifications
- A window showing **"Live updates paused — click to resume"** stopped updating after a while with no activity. Click it to resume

Turning kinds of notification on and off is in [Read a session's state](../sessions/status.md#get-notifications).

## claude does not know `/list-agents`, or says it cannot message another session

Claude Code's own messaging between sessions is turned off in Agent Fleet, because turning it on also brings back Claude's usage telemetry.

Use Agent Fleet's instead: turn on **"Messages between sessions"** in ⚙ Settings → Agents (off by default).
It reaches other kinds of agent, and stopped sessions too.

If it still does not work, check whether the session was already running before you turned it on.
The setting applies to sessions started afterwards, so a running session does not change until it restarts.
The full comparison is in [Running many sessions](../../member/11-many-sessions.md#how-this-differs-from-claude-codes-own-version).

## Related

- [Stop and resume a session](../sessions/stop-resume.md)
- [Clear away finished sessions](../sessions/cleanup.md)
- [Read a session's state](../sessions/status.md)
- [Hand off to another session or agent](../sessions/handoff.md)

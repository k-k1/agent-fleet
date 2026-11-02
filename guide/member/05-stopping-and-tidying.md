---
audience: "anyone stopping, resuming or clearing away sessions"
---

# 05. Stopping, resuming and tidying up

English | [日本語](05-stopping-and-tidying.ja.md)

This chapter covers the end of a session's life: stopping and resuming, handing a conversation off to another agent, keeping a session awake or having it stop when it is done, locking something against deletion, and clearing away what has piled up.

## Stopping and tidying up sessions

The operations live in the session row's **⋯ menu** (or right-click). When in doubt, choose
**"Stop"**. It keeps both the conversation and the list entry, which makes it the safest operation.

| What you want to do | Operation | Afterwards | Reversible? |
|---|---|---|---|
| Pause the work for now and continue later | **Stop** | Stays in the list as "Stopped" | Open it from the list to resume |
| Clear a finished job out of the everyday list | **Archive** | Hidden from the list with the conversation kept | Can be restored from the archive list |
| Start just the conversation over in the same place | **Recreate** | Archives the current conversation and opens a new one | The old conversation can be restored from the archive |
| Remove a throwaway shell / SSM from the list | **Delete** | Moves to the trash and leaves the list | Can be restored from the cleanup trash |

Which operations appear depends on the session's kind and state. For example, AI sessions show
"Archive", while throwaway shell / SSM show "Delete". To get rid of an AI session, archive it and
delete it from the archive list. **Every session delete, whichever way you do it, stashes the
session in the trash first.** Deleting a session also never deletes its working copy (worktree):
working copies are removed only by "Delete the working copy" below or by cleanup ②.

**A session left stopped moves to the archive on its own after 7 days** (change the period, or turn it off, in [Settings → Agents](20-settings.md#agents); shortening it archives the sessions already past the new period on the next list refresh), so the everyday list
does not fill up with work that is over. It is a move, not a deletion: the conversation is intact
and you restore it from the archive list exactly as if you had archived it yourself. **Nothing
leaves the archive on its own** (apart from the scratch folder, 7 days after archiving, see below): a session is only ever removed for good when someone deletes it
from the cleanup modal, and even then it goes to the bin first. So, to keep something, archive it
(or lock it, which also keeps the row in the everyday list); to be rid of it, delete it. Leaving
it alone never destroys it.

**Tabs of an archived (or deleted) session close on their own.** That holds when you tidied up
from another tab, from another device or in bulk from cleanup, and a notice then says the tab was
closed (tidying up in this window closes it right there, with no notice). Only that session's own
screen (terminal / chat) closes: files and plans you opened from it stay where they are.

**Stopping every session under one working copy at once.** Right-click the repository or worktree
row and choose **"Stop the sessions below"**. The modal lists the sessions running in that copy and
in the copies the left pane nests under it, one line each, and stops them in one press. Each line
says what it will get: **"stop now"**, or **"after the turn"** for a session that is mid-reply,
which is armed to stop when its turn ends so nothing half-written is lost. Tick **"Stop the
running sessions right away too (N)"** to cut those off instead. A session waiting on a question,
a limit or an expired sign-in is always stopped right away (there is no turn to wait for), and
shell / ssm rows and sessions pinned awake start unticked, since ticking one is your say-so. Stopping
is reversible: the conversation stays and the session can be resumed.

### Tidying up in bulk (cleanup)

When things get untidy, open the cleanup modal from the trash icon **"Open cleanup (survey &
tidy)"** in the **Repositories** heading. It is split into two stages so that **the riskier
things stop for you first**.

- **① Tidy sessions**: **"Tidy all"** archives stopped AI sessions and moves shell / SSM to the trash.
- **② Delete working copies and branches**: **"Delete all safe"** removes only what was
  judged safe. In the **Candidates** tab, **"Select all safe"** ticks every safe row in one click and
  **"Clean up selected"** removes what you ticked, so you can also go row by row.
- **Cache of deleted sessions**: files you pasted or attached into a session, and images codex
  looked at, stay on disk after the session is deleted. Once the session (or assistant chat) is
  gone **and not in the trash**, nothing can refer to them again; they are listed here, one row per
  kind with its size, and **"Delete all"** removes them. Anything a trashed session, or a fork made
  from the session (live or trashed), could still need is left alone.

Every candidate carries a **safety** rating and a reason.

| Safety | Meaning |
|---|---|
| **Safe** | Merged and clean; nothing is lost by removing it |
| **Review** | A stopped session (its reason says so when all its pull requests were finished, at least one merged, when last seen), a clean but unmerged worktree, and the like. Look before you decide |
| **Keep** | Running, uncommitted / unpushed, or delete-locked. Cleanup leaves it alone |

To clear a **Keep**, stop it or push first (as a last resort, force-delete from the Console).
To make sure a session is never swept up, use the
[delete lock](#locking-something-against-deletion).

**Deleting too much is recoverable.** Sessions and branches are stashed in the **trash** before
they are removed. The **"Trash (restore)"** tab of the cleanup modal **restores** them, and
**"Delete permanently"** reclaims the space once you are sure (if a restore of that archive stopped
part way, restore it again first, because deleting it is refused until then). The trash never empties on its
own; when it has grown, **"Delete permanently: older than 30 days"** in the trash tab reclaims it in
one go (its size is also shown in **Settings → Machine**). **Deleting a worktree goes
through the trash too.** Just before the delete it records the commit, the branch and the uncommitted
changes (untracked files included, ignored ones not), and protects the commit from git's garbage
collection. **Restoring** the entry recreates the worktree at the same path with those changes (all
unstaged) and puts the sessions that delete shelved back in the list. When it cannot come back as it
was (the branch moved after the delete, or another working copy has it checked out), the restore
creates nothing and opens the recreate dialog instead (the same one as "Recreate working copy",
below), which brings it back on a new branch. A worktree comes back at the commit it was on, with
its uncommitted changes when the row says so. **Deleting it permanently** from the trash removes the protection, and from then on it
cannot be brought back. The worktree is not deleted if the record cannot be written (a full disk), or if it holds a
repository git does not track (a clone inside it, whose contents the trash cannot keep). A new file
that was staged comes back as an untracked file.
The stopped AI sessions that lived in it **move to the archive** (the conversation stays
readable, but it cannot resume until the folder is recreated; see below), and shell / SSM move to the trash. **Deleting the cache of deleted sessions cannot be undone either** (it skips the trash), but
it only ever covers sessions that are already gone for good.

Archived sessions are a "shelf" that cleanup does not touch (restore them from the archive
browser). The conversation stays on the shelf, but the session's scratch folder (`~/.af-work/<session name>`,
what agents use as `$AF_WORK_DIR`) is removed 7 days after archiving; restoring clears the date (restore within 7
days and the folder is kept; one already removed is created afresh at the next launch). A locked session's
folder is kept while it is locked; once unlocked, the next pass removes it if the 7 days are already up. "Delete old ones" in the archive list deletes items older than 30 days in one go (they go to the trash too).

You can also ask the Fleet Operator to do the same survey and tidy-up from chat
([11](11-many-sessions.md)).

### Clearing one finished job (deleting a working copy)

Cleanup surveys the **whole workspace**. When what you want to clear is **one finished job** (a
parent worktree and the worktrees of the child sessions it spawned), right-click that row and
choose **"Delete the working copy"**. Only the local working copy is removed: history and the
remote stay. The copies the left pane nests under that row are listed as
they are, so **archiving the sessions and deleting the copies happen in one go** (stopped AI sessions
go to the archive, shell / SSM to the trash).

- **Only rows that lose nothing are ticked for you** (already in the parent, nothing uncommitted,
  nobody working in them). A row with uncommitted or unmerged work stays empty, so **ticking it is
  the "delete it anyway" confirmation**.
- **Rows with a running session are not a dead end.** Tick **"Stop the running sessions first
  (N)"** and the modal stops them as part of the same tidy-up, ticking for you the rows that only
  needed stopping (a turn in flight is cut off). A row that would still lose work once stopped
  becomes tickable but stays unticked; ticking it is the same "delete it anyway" confirmation. **Rows locked against deletion cannot be
  ticked**; the reason is shown under the row. Remove the lock first.
- Ticking **"Delete the merged branches too"** also removes the throwaway `temp/…` branches (the
  trash can restore them). Adding **"Delete the branch on the remote (origin) too"** removes it on
  origin as well. **That one cannot be undone.** A branch that is not in the parent's history
  cannot be deleted through this route at all.

### A spend budget — pausing a session that spends too much

A session can carry a **spend budget** in US dollars. When its **estimated** spend reaches the
budget, the session **stops after the turn it is running** (the same resumable stop as "stop after
this turn") and the notification centre says so (**"Stopped by its budget"**). If one turn alone
runs on to **twice** the budget, the session is halted at once, mid-turn, and the notification says
**halted mid-turn**.

- **Setting it.** In the start dialog under **More → Budget (USD)**; later from the session's
  menu, **"Spend budget…"**. Settings › Agents › **"Budget for new sessions"** is the default for
  every launch that names none, including sessions started by `create_session` and by schedules.
  0 means no budget.
- **Seeing it.** The chat's context row shows **"≈$1.84 / $5.00"**, amber from 80% and red at the
  budget. Press it to change the budget. For a session that started others with `create_session`,
  **"children ≈$X"** sits beside it: their spend is shown here but **not** counted against this
  session's budget, because each child has its own.
- **Carrying on.** A stopped row shows the budget mark. **"Raise budget & resume…"** (in the row's
  menu and on the notification) sets a higher budget and resumes in one step; it only accepts a
  budget above what is already spent. Resuming without raising it gives the session one more turn,
  after which it stops again.
- **What the figure is.** An estimate at list price from the session's own transcript, or the
  CLI's own reported cost where it gives one (opencode); **not your bill**. For Claude Code it
  includes the session's subagents and Workflow agents, which Claude Code records in transcripts
  of their own. The usage view does not count those yet, so there a session that delegated work
  shows less than its budget figure. On a subscription plan
  it is the API-equivalent figure. A model with no price cannot be estimated, so a budget has no
  effect on it (the dialog says so); kinds whose transcript carries no token counts (Kiro, Cursor, Antigravity)
  and the self-hosted engine ($0) are never stopped by one. A fork starts from $0: the history it
  copied is not charged to it.

## When you can — and can't — resume

Stopped sessions can be opened and resumed with a click. However, claude / codex / cursor / copilot / kiro / agy / opencode / lcpp / muse
**cannot resume if the working folder they were launched in is gone**. In that case the state
display becomes **"Folder missing — can't resume"**, and the row is struck through and can no
longer be clicked ("Can't resume — the working folder no longer exists"). The typical case is
after deleting a working copy together with its worktree. shell falls back to home and resumes
if its working folder is missing.

**A deleted worktree can be recreated at its original path.** In the archive list, the heading of
a deleted worktree's folder offers **"Recreate working copy"**; a session whose working copy is
gone has **"Recreate working copy (worktree)…"** in its ⋯ menu, which opens the same dialog. It looks for the branch the folder
last held, in this order, and creates the worktree at the same path from the first it finds: the
state at the delete (when the trash recorded it; uncommitted changes come back too) → the
local branch → the remote branch → the commit recorded when the branch was deleted from the Console
→ the head commit a merge commit still names (a GitHub PR merge, for example). If none is left, it
starts a new branch of the same name off the parent. Once it is back, choose the sessions to
restore. With the folder back where it was, every kind of session resumes its conversation as
before.

- If another working copy has that branch checked out, a new branch is started at the same commit.
- A folder of the same name may have been reused by earlier work on a branch of the same name.
  Sessions whose start branch differs from the recreated one are flagged; check them against their
  dates before restoring.
- Uncommitted changes come back only from "the state at the delete", for deletes the trash
  recorded. Ignored files (`node_modules`, `.env`, …) never come back, and nothing can be
  recreated once the parent working copy is gone.
- Right-click a working copy's row and choose **"Archived sessions of this working copy"** to open
  the archive for that folder only.

When you open a stopped session, the history is first shown read-only, and **"Resume"** restarts
the conversation. After resuming, it continues with the execution method that was saved.
**Ctrl+click** (or middle-click) opens it **in a new pane** while keeping your
current view ([02 Console basics](02-console-basics.md#arranging-multiple-views-panes)).

If you switched the model, effort or plan mode inside the terminal, the session **resumes on the
last settings the conversation recorded** rather than the ones it was launched with. Except for
claude, codex and kiro, a model switch with no message sent after it is not recorded by the CLI
itself, so the session resumes on its launch model.

Even while the workspace is stopped, the list itself stays visible. You can't operate on the
contents, but you can check "which sessions were there" even from a phone.

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

A session row can show a **warning icon and a branch name**. This is a sign that the session's
working copy **has switched** from the branch it was launched on to a different branch.
The working tree the running agent sees may have been swapped out, and its edits and diffs may
no longer line up. If this doesn't ring a bell, check whether an unintended branch switch has
happened. For parallel work, giving each session its own worktree avoids this confusion.

## Continuing elsewhere — handoff, branch, move

There are three ways to carry a conversation into a new session. They differ in who prepares what
the new session starts from, and in what happens to the original.

| | Handoff | Branch here | Move |
|---|---|---|---|
| Where | **⋯** menu, **"Hand off to another agent…"** | **"Branch here"** under one of your past messages ([04 Conversation](04-conversation.md#branch-from-a-past-message)) | **⋯** menu, **"Move to another agent…"** |
| Who prepares the context | The Fleet Operator writes a summary, and you review it | Nobody: the conversation is copied as it is | The Workspace writes the conversation to a handover file; no AI summarises it |
| Target agent | Any connected agent | The same agent | Another kind of agent only |
| The original session | Stays as it is | Stays as it is | Stops and takes no more input |
| What carries over | Key points, unfinished tasks, changed files, next steps | By default the conversation before that message, word for word, with the message left as a draft; **Continue after it** also carries that message and its reply | The conversation, where it stopped, the working copy's state, queued instructions; the new session works in the same working copy |

### Handing a conversation off (handoff)

From the **⋯** menu of any session that has a conversation (every agent kind; not shell or ssm),
choose **"Hand off to another agent…"** and pick the **target agent** in the handoff modal that
opens. The workspace has to be running; the session itself may be stopped. Rather than handing
over the whole original
conversation as is, the **fleet operator** reads the source session's situation and drafts a
handoff proposal: the key points, unfinished tasks, changed files, and next steps.

You review the proposal, the working folder, and the agent to start, and the new session is
created only after you explicitly agree. The original session stays as it is. In a long
conversation, this lets you hand off to another agent or try a different direction without
using up the new conversation's context. The proposal card, and the session's ⋯ menu, also offer **"Hand off to a member"**,
which offers the session to someone it is shared with instead
([12](12-collaboration.md#handing-a-session-to-another-member)).

**When you want the conversation itself rather than a summary, branch instead of handing
off.** In the chat view, **"Branch here"** on one of your past messages
([04 Conversation](04-conversation.md#branch-from-a-past-message)) opens a new session with the
conversation up to that point copied as-is, on the same agent. The exact wording and the
fine details survive, which is what you want for "redo it from that instruction".

### Moving a session to another agent (move)

When a session has run out of its agent's usage limit, or you simply want to carry on in another
kind, choose **"Move to another agent…"** in its **⋯** menu (next to the handoff). Unlike the
handoff, no AI writes a summary: the Workspace writes the conversation to a **handover file**
(where it stopped, the working copy's state, the changed files, the conversation itself, with
secrets masked), starts the agent you pick **in the same working copy**, and tells it to read
that file first. The original session then **stops and takes no more input**, so only one
session works on the working copy.

**Which sessions can be moved.** Only a session with a conversation the Workspace can read, so
not shell or ssm. It has to be idle, stopped, or stopped at a usage or spend limit: a move is
refused while a turn is running, or while a question, a plan or a permission prompt is waiting
for you. Let the turn end or stop it, and answer first. Some kinds cannot be moved in some states
(see the Agents reference above).

A session stopped at a usage limit (or a spend limit) also shows a band above its chat with a
**"Move to another agent"** button; it opens the same dialog with the agents that still have
quota left in front (and marked). Agents whose usage is not shown stay selectable, unmarked. The
band is not shown when no agent can take the session, or every candidate is at its own cap.
When the move completes, the automatic resume that was booked for the limit's reset is
cancelled, so the original does not start again on its own.

The dialog shows, before you confirm:

- **The agents it can move to**: which kinds can be a source or a target is in the
  [Agents reference](../ref/agents.md#moving-a-session-to-another-agent). The same
  agent is never offered (it draws on the same account's quota); to continue in the same kind,
  use **"Branch here"** ([04 Conversation](04-conversation.md#branch-from-a-past-message)) where the kind supports it. Agents that cannot read the file, or
  are not connected, are listed with the reason. For claude, codex, muse, copilot and agy the
  dialog also shows each agent's current usage (copilot's as its one monthly pool); for the
  others it says nothing about quota.
- **"Model"** and **"Execution method"** for the new session, as when you start one.
- **Uncommitted changes** in the working copy. The move never commits, stashes or discards them;
  the new session takes them over as they are, so tidy them first if you need to.
- Instructions still queued, messages from other sessions not yet delivered (both go into the
  file), the file's size, and **child sessions still running**. Children keep belonging to the
  original session: their reports no longer reach it, so check them in the Console.
- For a Managed cursor session: its conversation view is empty once it stops, and the file is
  then the only record.

**The handover file** is saved in the new session's own scratch folder, outside the working copy,
so it is never committed; the dialog shows its name and size. Besides the conversation it records where
the session stopped, including instructions that were queued but never delivered and inputs a
stop threw away. Your last three messages keep every tool call with its output; before them,
tool calls that only read are left out and the others keep one line without their output.
Thinking is left out. Past about 256 KiB the oldest agent replies are cut to their first
paragraph; your own messages and the last three exchanges are never cut, so the file can still
be larger, and the dialog says so. Strings that look like secrets are masked. Its headings
follow the Console's display language. When the original agent followed project instruction
files the new one does not load, the file names them. The file goes to the trash with the new
session and comes back when you restore it.

Afterwards the original row reads **"Moved to …"** and opens the new session. Its menu offers
**"Resume here anyway…"**, which opens the original's input again without stopping the new one;
use only one of them from then on. If the move stopped without knowing whether the new session
started, the row reads **"Move to … unfinished"**, and the dialog offers **"Retry"** (same file,
same name) and **"Cancel the move"**. When the new session's first prompt is not known to have
arrived, the dialog offers to resend it; resending may deliver it twice. The workspace has to be
running.

## Keeping a session awake

Idle workspaces stop on their own. If you have something long running that must not be
interrupted, **Keep awake** in the ⋯ menu exempts the session from idle-stop for a set
number of hours, and the row shows how much of that is left.

It is deliberately time-boxed. An exemption you set once and forget is how a workspace
ends up running for a month.

## Having a session stop when it is done

The opposite case: you want the session to **stop once the work is finished**: you left a
long job running overnight and would rather it did not hold the workspace up afterwards.

There are two ways to ask, and both set the same thing:

- **Tell the session**: "stop when this is done". It arms the stop itself.
- **Stop when this turn ends** in the ⋯ menu: set it yourself. Cancelling is in the same place.

An armed row carries a badge. The stop happens **after the running turn ends**: it will not
stop while a question is waiting for you or while work is still going. It is the resumable
stop, so the conversation stays and Start picks it up where it left off, and you get one
notification when it happens.

**Sending a new instruction releases it.** "Stop when you are done" was said about the work
in front of you, not about whatever you ask for next.

This is tidying rather than cost control: a session that has finished is not what keeps the
workspace awake, so stopping it does not change when the workspace stops. What you get is
the memory it was holding, back right away, and a list where "finished" reads as "stopped".
Idle auto-stop does the same thing on a delay, except that it skips a session while you
have it open, so stopping one while you watch it work is something only this can do.

## Locking something against deletion

**Lock against deletion** in the ⋯ menu marks a session as one that must not go away.
A locked session carries a badge saying so, and is **skipped by cleanup and by
automatic tidying**, both the [bulk cleanup](#tidying-up-in-bulk-cleanup) and anything that would
have removed it on its own.

Use it for the conversation you will need at the end of the quarter, and for the
worktree you have not finished with. Cleanup refuses to remove a worktree that still
holds a locked session, and tells you to unlock it first rather than quietly working
around you.

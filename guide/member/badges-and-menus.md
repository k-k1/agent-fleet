# Icons, badges, and menus — common Console reference

English | [日本語](badges-and-menus.ja.md)

Audience: anyone wondering what a mark on the screen means
Source of truth: the Console itself — if a screen disagrees with this page, the screen is right
Updated: 2026-08

> Audience: anyone who wants to check what the marks in the left pane mean, or what a right-click can do.
> Menu items vary with the target's type, its state, your permissions, and the workspace state.

## Session display

The colored icon at the start of a row indicates the kind: `claude`, `codex`, `cursor`, `copilot`, `kiro`, `agy`, `opencode`, `shell`, or `ssm`.
Hover over the state icon at the end of the row to see the state name. States that need action from you are
shown as text as well, not just an icon.

| State | Meaning |
|---|---|
| Working… | The agent is processing |
| Question | Waiting for an answer to a question |
| Plan ready | Waiting for the plan to be approved or rejected |
| Awaiting permission | Waiting for permission for a command run, an edit, etc. |
| Waiting for limit reset · 19:50 | A usage limit stopped the turn. The time is when the automatic resume is booked (omitted when none is) |
| Spend limit — needs a raise | The spend / credit limit was reached. Waiting will not clear it — the limit has to be raised or credit added |
| Ready | Ready to take the next instruction |
| Ready · running in background | Accepts input, but background processing is still running |
| Running | A shell / ssm with no detailed progress state is running |
| Stopped | The process is stopped |
| Folder missing — can't resume | The working folder is gone and the same conversation can't be resumed |
| Ended (out of memory) | May have been force-killed by a memory limit or similar |
| Force-killed / Crashed | A SIGKILL, a signal, a non-zero exit, or similar was detected |

The speaker icon means an answer is being read aloud; the warning plus a branch name means the working copy has
switched to a branch different from the one it started on.

A row can also carry **"Shared"** (visible to another member —
[02](02-sessions.md#sharing-a-conversation-shared-sessions)) and **"Delete-locked"** (excluded from deletion and
automatic tidying — [02](02-sessions.md#tidying-up-in-bulk-cleanup)).

## Repository status display

| Badge | Meaning |
|---|---|
| Uncommitted | There are uncommitted changes |
| = parent | Same commit as the parent working copy |
| unmerged N | There are N worktree-specific commits not contained in the parent |
| parent+N, FF ok | The worktree's HEAD is contained in the parent, which is N commits ahead. **"Fast-forward from the parent"** in the menu brings them in ([04](03-code.md)) |
| diverged N↕M, no FF | Both the worktree and the parent have their own commits; a merge or rebase is needed |
| n/a | The relation to the parent can't be determined (detached HEAD etc.) |
| ↑N | N commits ahead of origin |
| ↓N FF ok | origin is N commits ahead and can be fast-forwarded |
| ↑N ↓M needs merge | Diverged from origin; a merge or rebase is needed |

`●N` is the number of running sessions in that working copy; a plain number is the count of stopped sessions.
A collapsed parent repository also aggregates the sessions of the worktrees under it.

## Other badges

- Colored `1`, `2`… — the number of the pane it is shown in. Press to jump to that pane.
- "Untracked", "Added", "Modified", "Renamed", "Deleted" — the file's Git change type.
- The number on an assistant row — the conversation's message count.
- The spinning icon / check on an assistant row — generating an answer / waiting.
- "Awaiting approval", "Approved", "Rejected" on a plan card — the decision on the plan.
- "LFS pointer" — a file with only the Git LFS pointer present, not the actual content.
- **"From the operator", "Scheduled", "Manual run", "Auto-resume", "From <name>"** in the chat view — where a
  prompt you did not type came from: the fleet operator, a schedule, an auto-resume after an interruption, and
  [a message from another session](02-sessions.md#messages-between-sessions).
- **"Paused"** on a schedule row — that schedule is suspended ([11](08-organising.md)).
- **"N awaiting approval"** on shared sessions — proposals from a recipient are waiting for you
  ([02](02-sessions.md#sharing-a-conversation-shared-sessions)).
- **"Safe" / "Review" / "Keep"** in the cleanup modal — whether it is fine to tidy away
  ([02](02-sessions.md#tidying-up-in-bulk-cleanup)).

## Right-click menus

### Sessions

Depending on state and kind you'll see resume, re-login to SSM, stop, open the remote session, copy the ID,
rename, rename the branch, hand the conversation off, **Share…**, **set / clear the delete lock**,
archive / delete, and recreate. For a session that doesn't belong to a repository, **assignment to a working
set** appears here too (an item shown ticked but unclickable is one that follows its repository or conversation
automatically).
Archive keeps the conversation but hides it from the list; recreate archives the current conversation and starts
a new one in the same place. When there is no working folder, resume, handoff, and recreate are not shown.

### Repositories / worktrees

You can open the commit graph, open the folder, commit changes, switch branches, copy the branch name,
Fast-Forward (on a worktree, **"Fast-forward from the parent"**), project settings, **Share…**, **assignment to
a working set**, launch a session by kind, and delete the working copy. A normal click expands / collapses the row.
Ctrl / ⌘+click or middle-click opens the commit graph in a new pane.

### Files / folders

You can create a new file, create a new folder, copy the name, copy the relative path, rename, and delete.
Files additionally show "Open in reader" and "Download". To hand a file to a session or an assistant, open the
file and use "Send" in the viewer.

### Assistants

New chat and open in a new pane are shown. Assistants you created also show edit and delete.
Built-in assistants cannot be edited or deleted. A conversation row offers rename, the delete lock, and
**assignment to a working set**.

### Commit graph

You can view a commit's details, switch to a branch that references it, check it out as a detached HEAD, and
create a new branch based at that commit. While viewing a submodule, the branch-changing items are not shown.

## Buttons on a pane

At the top right of a pane you get, depending on its content, **toggle wrapping**, **pop out into another tab**
(the pane moves into a browser tab of its own) and **close** (middle-click / Ctrl+click closes without
confirmation). Panes that cannot be popped out don't show the button
([03](05-terminal.md#arranging-multiple-views-panes)).

### Tabs (tabbed grid)

Right-clicking **a session's tab** gives the same menu as that session's row in the left pane
([03](05-terminal.md#arranging-multiple-views-panes)). Tabs that are not sessions, and every tab while the
workspace is stopped, keep the browser's own menu.

## When a menu doesn't appear

- While the workspace is stopped, menu items that run inside the workspace are disabled or hidden.
- Operations the kind doesn't support, or that don't fit the current state, are not shown.
- A right-click menu closes on an outside click, Escape, or when the window loses focus, and the focus goes
  back to the item you opened it from.
- Every one of these menus also opens from the keyboard with the **Menu key** (or **Shift+F10**) while the item
  has focus.

---

Related: [Sessions](02-sessions.md) · [Repositories and git](03-code.md) · [Files](04-files.md)

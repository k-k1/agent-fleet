# Icons, badges, and menus — common Console reference

English | [日本語](badges-and-menus.ja.md)

> Audience: anyone who wants to check what the marks in the left pane mean, or what a right-click can do.
> Menu items vary with the target's type, its state, your permissions, and the workspace state.

## Session display

The colored icon at the start of a row indicates the kind: `claude`, `codex`, `opencode`, `shell`, or `ssm`.
Hover over the state icon at the end of the row to see the state name. States that need action from you are
shown as text as well, not just an icon.

| State | Meaning |
|---|---|
| Working… | The agent is processing |
| Question | Waiting for an answer to a question |
| Plan ready | Waiting for the plan to be approved or rejected |
| Awaiting approval | Waiting for permission for a command run, an edit, etc. |
| Ready | Ready to take the next instruction |
| Ready · running in background | Accepts input, but background processing is still running |
| Running | A shell / ssm with no detailed progress state is running |
| Stopped | The process is stopped |
| Folder missing — can't resume | The working folder is gone and the same conversation can't be resumed |
| Ended (out of memory) | May have been force-killed by a memory limit or similar |
| Force-killed / Crashed | A SIGKILL, a signal, a non-zero exit, or similar was detected |

The speaker icon means an answer is being read aloud; the warning plus a branch name means the working copy has
switched to a branch different from the one it started on.

## Repository status display

| Badge | Meaning |
|---|---|
| Uncommitted | There are uncommitted changes |
| = parent | Same commit as the parent working copy |
| unmerged N | There are N worktree-specific commits not contained in the parent |
| merged | The worktree's HEAD is contained in the parent in Git history |
| diverged N↕M | Both the worktree and the parent have their own commits |
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

## Right-click menus

### Sessions

Depending on state and kind you'll see resume, re-login to SSM, stop, open the remote session, copy the ID,
rename, rename the branch, fork the conversation, archive / delete, and recreate.
Archive keeps the conversation but hides it from the list; recreate archives the current conversation and starts
a new one in the same place. When there is no working folder, resume, fork, and recreate are not shown.

### Repositories / worktrees

You can open source control, open the folder, commit changes, switch branches, copy the branch name,
Fast-Forward, launch a session by kind, and delete the working copy. A normal click expands / collapses the row.
Ctrl / ⌘+click or middle-click opens source control in a new pane.

### Files / folders

You can create a new file, create a new folder, copy the name, copy the relative path, rename, and delete.
Files additionally show "Open in reader" and "Download". To hand a file to a session or an assistant, open the
file and use "Send" in the viewer.

### Assistants

New chat and open in a new pane are shown. Assistants you created also show edit and delete.
Built-in assistants cannot be edited or deleted.

### Commit graph

You can view a commit's details, switch to a branch that references it, check it out as a detached HEAD, and
create a new branch based at that commit. While viewing a submodule, the branch-changing items are not shown.

## When a menu doesn't appear

- While the workspace is stopped, menu items that run inside the workspace are disabled or hidden.
- Operations the kind doesn't support, or that don't fit the current state, are not shown.
- A right-click menu closes on an outside click, Escape, or when the window loses focus.

---

Related: [Sessions](02-sessions.md) · [Repositories and git](04-git.md) · [Files](05-files.md)

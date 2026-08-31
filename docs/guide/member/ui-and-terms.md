# UI and terminology — Console common reference

English | [日本語](ui-and-terms.ja.md)

> Audience: anyone who wants to check where things live in the Console, or what a word in
> this guide refers to. For individual step-by-step operations see each chapter; for the
> meaning of indicators and menus see
> [Icons, badges, and menus](badges-and-menus.md).

## Screen layout

From top to bottom, the Console consists of the **bar at the very top of the screen**, the
**workspace action bar**, and below them the **left pane** and the **main area**. On a
smartphone the left pane is hidden; open it with the **≡ (menu)** at the top left of the
screen.

### The top bar and the workspace action bar

The bar at the very top of the screen holds tenant switching, notifications, settings, and
account actions. The workspace action bar is the strip for operating your own
**workspace** — status, start/stop, "Start", Preview, pane splitting, and so on.

### Left pane

While the workspace is running, the sections appear in this order.

Pinned at the very top is the **working set** switcher, which narrows the pane down to one
piece of work ([02](02-sessions.md#narrowing-the-view-with-working-sets)). Below it, the
sections appear in this order.

| Section | Contents |
|---|---|
| Assistants | Chat that doesn't use a repository, plus purpose-specific assistants |
| Memo queue | Memos of instructions to send later in a batch |
| Schedules | Scheduled runs and their history (only where scheduled execution is enabled — [11](11-fleet-operator.md)) |
| Repositories | Cloned working copies. Under each row, the sessions and worktrees running at that location |
| Other sessions | shell, SSM, home-launched sessions, and others not belonging to a repository |
| Shared sessions | Sessions you shared, and sessions other members shared with you (hidden when there are none — [02](02-sessions.md#sharing-a-conversation-shared-sessions)) |
| Files | A browser for finding and opening files in a repository or home |

While the workspace is stopped, **Session history** is shown in place of the sessions under
each repository. You can review the history, but operations that need the workspace, such as
resuming, are done after starting it (the working set switcher and shared sessions still
work while it is stopped).

### Main area and panes

This is where whatever you selected in the left pane is displayed. A session's chat /
terminal, commit graph, changes, files, diffs, assistant chat, and more open here. It can
be split into multiple **panes**; the colored number in the left pane is the number of the
pane displaying that item. You can choose between **split panes** and a **tabbed grid**, and
a pane can be popped out into its own browser tab
([03](03-terminal.md#arranging-multiple-views-panes)).

## Terminology

| Term | What it means in this guide |
|---|---|
| Workspace | The user's private work environment. Holds repositories, work in progress, and sessions. Runs as a dedicated container behind the scenes |
| Workspace action bar | The row of controls at the top of the screen for starting/stopping the workspace, "Start", and so on |
| Session | A unit of conversation, working location, and execution state corresponding to one task. Separate from whether it has a terminal |
| Execution method | The path by which Agent Fleet runs an agent and delivers instructions to it. Internally called a driver |
| Managed | A method that uses a shared execution runtime and is operated from the chat view. Has no terminal |
| Terminal (CLI) | A method where you directly operate the agent's CLI and its interactive screen inside a terminal |
| Agent | A CLI coding AI such as claude, codex, or opencode |
| Assistant | A purpose-specific chat with no terminal. A feature separate from sessions |
| Repository | Git repositories in general. Also refers to the "Repositories" section in the UI |
| Working copy | The folder of a repository inside the workspace that you actually edit |
| worktree | An independent working copy created via a Git mechanism. Prevents interference during parallel work |
| Parent | The working copy a worktree was created from. Serves as the comparison target in status displays |
| Pane | One subdivision of the main area |
| Commit graph | The screen that handles the commit history graph, branches, fetch, and so on |
| Memo queue | A feature that stores instructions temporarily and sends them to a session later in a batch |
| Working set | A grouping of repositories, conversations, sessions and schedules by piece of work, used to narrow down the left pane. It does not move or copy anything |
| Shared session | Showing a session's — or a project's — conversation to another member of the same tenant, read-only. Permission is either "view only" or "may propose" |
| Cleanup | The modal that inspects stopped sessions, stale worktrees and merged branches and tidies them away. Deleted sessions and branches are stashed in the **trash** and can be restored |
| Browser pane | A method that renders a web service started inside the workspace (`127.0.0.1:{port}`) in a Console pane, with display and interaction (clicking, typing, scrolling, etc.). HMR, WebSocket, SSE, cookies, and redirects all work. "Open in pane" in the UI. localhost only — external URLs cannot be opened ([08](08-advanced.md)) |
| Lightweight preview | Opens the same web service in another tab under a `/preview/{port}/` sub-path. WebSocket and SSE pass through, but an app that emits absolute-path assets breaks ([08](08-advanced.md)) |
| Preview subdomains | A `https://<random>-<port>.<domain>/` URL issued every time the workspace starts. The app is served at the root and several ports are open at once. Issued only on deployments that configure it ([08](08-advanced.md)) |

## Writing conventions

- On-screen names are written like **Repositories**, **Files**, **Sessions**.
- CLIs and session kinds are written as `claude`, `codex`, `opencode`, `shell`, `ssm`.
- When referring to the product or to a speaker, we may write Claude, Codex.
- UI buttons are written like "Clone"; Git commands and generic operations like `git clone`.
- An **icon** is a picture-only display, a **status display** is a colored display that
  shows a state, and a **badge** is a small auxiliary display such as a count or a pane
  number.

---

For those who want to know how it works: [dev/02 Console design](../../dev/02-console.md)

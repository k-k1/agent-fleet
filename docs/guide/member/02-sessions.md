# 02. Sessions — launch, switch, and stop AI conversations

English | [日本語](02-sessions.ja.md)

> Audience: members who work with sessions day to day. Covers creating new sessions, reading
> their state, pausing and tidying up work, the conditions for resuming, and duplicating a
> conversation and renaming branches.

A session bundles one job you delegate to the AI into a single unit — its **conversation,
working location, and execution state**. It is a separate concept from whether a terminal
exists: Codex / opencode / GitHub Copilot / Kiro also run as sessions under managed execution,
without a black screen. In the left pane, sessions appear under the **repository** that matches
their working location; those that don't belong to a repository appear under **Other sessions**.
You can have multiple sessions in parallel, each with its own independent conversation and
working folder.

## Session types

- **claude** — launches Claude Code.
- **codex** — launches Codex.
- **cursor** — launches Cursor (needs a Cursor plan; appears once connected — [06](06-agents.md)).
- **copilot** — launches GitHub Copilot (rides on the GitHub connection — [06](06-agents.md)).
- **kiro** — launches Kiro (needs a device-flow sign-in; appears once connected — [06](06-agents.md)).
- **agy** — launches Antigravity (experimental slot; appears once connected).
- **opencode** — launches OpenCode.
- **shell** — a plain shell (bash). Opens right away from "Start".

claude / codex / cursor / opencode / copilot / kiro / agy appear in "Start" once you connect the corresponding
agent (connections: [06 Agents](06-agents.md)). (**ssm**, which logs in to another host, is
covered in [08 Advanced usage](08-advanced.md).)

## Execution method — Managed and Terminal (CLI)

On the start screen for Codex / cursor / opencode / GitHub Copilot / Kiro you can choose the **execution
method**. This is the difference in the path Agent Fleet uses to run the agent and deliver your
instructions (internally, the "driver"). It chooses **how a session of the same kind is run** —
it does not give the conversation a separate storage location or a separate working folder.

- **Managed (recommended, default)** — Agent Fleet controls the agent directly.
  You operate it through the chat view; there is no terminal. Codex / opencode run on a shared
  execution runtime and have no per-session CLI process, so they save memory and suit
  parallel work (GitHub Copilot, cursor, and Kiro run a dedicated per-session process even when managed,
  so their memory use is on par with Terminal (CLI)).
- **Terminal (CLI)** — launches the agent's CLI per session, and you can operate its
  interactive screen directly from the terminal. Suited to cases that need CLI-specific screens
  or commands; each session uses extra memory.

New Codex / cursor / opencode / GitHub Copilot / Kiro sessions default to managed. claude / agy use
Terminal (CLI), and shell / SSM use only the terminal path. For kinds that support managed
execution, you can switch the execution method from the session's ⋯ menu whenever the session
is not stopped and the agent is not in the middle of processing. **The conversation carries over
as is.** You can also open the chat view from Terminal (CLI), but managed execution has no
terminal screen.

A Terminal (CLI) screen is kept alive behind the scenes even if you close the browser. You
never need to operate that keep-alive mechanism yourself.

## Creating a new session — "Start" at the top of the screen

**"+ Start"** in the workspace action bar is the entry point for launching. Pressing it opens the
**"Start"** screen, where you begin by choosing **where to work**.

- **Chat (assistant)** — starts a simple chat that doesn't use a repository
  ([07 Chat and memos](07-chat-memo.md)).
- **Launch an agent in a repository** — search for and pick a cloned repository, and you move
  straight on to **"Start working"** (agent, model, worktree, first prompt). This is the same
  screen as "Launch" on a repository row (see below).
- **Clone a new repository…** — sits below the repository list. Clone by picking from a
  connection or entering a URL by hand; when it completes you continue straight into
  "Start working" (you can specify a new branch, and the worktree folder name is generated
  automatically).
- **Launch an agent in home** — runs an agent in home (~).
  For drafts, research, and throwaway work.
- **shell** — pressing it opens bash immediately.
- **SSM — log in to another host** — shown when SSM is configured; pick the host to log in to
  and connect.

**"Back to Start"** at the bottom left of each step (or the browser back button) takes you one
step back. Sessions are named automatically (repository name + timestamp) and can be renamed
later (see below).

You can press "Start" even while the workspace is stopped. After you confirm, it **starts the
workspace, and the "Start" screen opens automatically once it's ready** — no need to wait for
startup and press the button again.

### Worktrees and new branches — the key to parallel work

When you want to advance multiple tasks in the same repository at the same time, a single
working folder makes them trample each other. By default, Agent Fleet prevents this by carving
out an independent working copy (worktree) per session.

- In **"Start working"** — opened from "Start" or from "Launch" on the base repository — the
  **"Location"** row states in one line where the session will run. Open it with **"Change"**
  to choose between **"New worktree"** (default) and **"Directly in this copy"**.
- With "New worktree" you specify the base branch and an optional branch name. If the branch
  name is empty you get a provisional name `temp/…`; if you enter one, the worktree folder
  name is generated automatically from the branch name.
- Launching from an existing worktree row launches directly in that worktree. Create new
  worktrees from the base repository.

### Working folder — starting inside a subfolder

**"Working folder"**, under **"More"** in "Start working", narrows where the agent starts: leave it empty to start
at the working copy root (the default), or pick a folder beneath it — useful in a monorepo where
a task only concerns `console/` or `apps/web`. Type the path or press **"Browse"** to walk the
repository's folders.

- With "New worktree" the same relative path is resolved **inside the newly created worktree**,
  not in the base repository. If that branch does not have the folder, the launch is refused and
  nothing is created.
- The session still belongs to the working copy: it stays grouped under the same repository, and
  branch switching, cleanup and worktree deletion behave exactly as before.
- The last folder you launched in is remembered per repository and pre-filled next time.

The standalone "Clone" under **Repositories** in the left pane also lets you specify a different
folder name when you specify a new branch or when a working copy with the same name already
exists ([04](04-git.md)). This is a separate path from creating a worktree in "Start working".

## Reading state — badges and notifications

In each row of the list, the colored icon at the front shows the agent kind, and the state icon
at the end shows the current state. Hover over the state icon to see the state name. States that
need action from you — **Question**, **Plan ready**, **Awaiting permission** — also show text.
Active sessions refresh automatically every 4 seconds.

| State display | Meaning |
|--------|------|
| Working… | The agent is working (spinning) |
| Question | It has asked you something and is waiting for a reply |
| Plan ready | A proposed plan is waiting for your review |
| Awaiting permission | It is asking for permission to act |
| Ready | Idle, waiting for your next instruction |
| Ready · running in background | Awaiting input, but something is still running behind the scenes |
| Running | shell and the like (kinds with no working / awaiting-input distinction) |
| Stopped | Not running (click to open and resume) |
| Folder missing — can't resume | The working folder is gone and it can't be resumed (see below) |
| Ended (out of memory) | May have been force-terminated, e.g. by the memory limit |
| Force-killed / Crashed | SIGKILL, a signal, a non-zero exit, etc. was detected |

For the other marks shown on a row — colored pane numbers, reading aloud, branch-switch
warnings, and so on — see [Icons, badges, and menus](badges-and-menus.md).

When a state changes while you're not watching, a **browser notification** appears (suppressed
while you have that screen open). When the work pauses — the session becomes Ready — you're
notified with **"A reply is ready"**; when a question arrives, with **"A question is waiting"** —
the session name is included in the body. This suits use cases like waiting for a reply on your
phone during a commute (shell / ssm don't notify).

## Stopping and tidying up sessions

The operations live in the session row's **⋯ menu** (or right-click). When in doubt, choose
**"Stop"**. It keeps both the conversation and the list entry — the safest operation.

| What you want to do | Operation | Afterwards | Reversible? |
|---|---|---|---|
| Pause the work for now and continue later | **Stop** | Stays in the list as "Stopped" | Open it from the list to resume |
| Clear a finished job out of the everyday list | **Archive** | Hidden from the list with the conversation kept | Can be restored from the archive list |
| Start just the conversation over in the same place | **Recreate** | Archives the current conversation and opens a new one | The old conversation can be restored from the archive |
| Remove a throwaway shell / SSM from the list | **Delete** | Disappears from the list | Cannot be undone |

Which operations appear depends on the session's kind and state. For example, AI sessions show
"Archive", while throwaway shell / SSM show "Delete". Log files may remain after deletion, but
the session cannot be brought back to the list.

### Tidying up in bulk (cleanup)

When things get untidy, open the cleanup modal from the trash icon **"Open cleanup (survey &
tidy)"** in the **Repositories** heading. It is split into two stages so that **the riskier
things stop for you first**.

- **① Tidy sessions** — **"Tidy all"** archives stopped AI sessions and deletes shell / SSM.
- **② Delete working copies and branches** — **"Delete the safe ones"** removes only what was
  judged safe.

Every candidate carries a **safety** rating and a reason. You can also pick them off one by one.

| Safety | Meaning |
|---|---|
| **Safe** | Merged and clean — nothing is lost by removing it |
| **Review** | A stopped session, a clean but unmerged worktree, and the like. Look before you decide |
| **Keep** | Running, uncommitted / unpushed, or delete-locked. Cleanup leaves it alone |

To clear a **Keep**, stop it or push first (as a last resort, force-delete from the Console).
The **⋯** menu on a session offers a **delete lock**, which takes it out of cleanup's reach.

**Deleting too much is recoverable.** Sessions and branches are stashed in the **trash** before
they are removed. The **"Trash (restore)"** tab of the cleanup modal **restores** them, and
**"Delete permanently"** reclaims the space once you are sure. **Only deleting a worktree cannot
be undone** — and even then only the working copy goes; the history, the remote and the branch
remain.

Archived sessions are a "shelf" that cleanup does not touch (restore them from the archive
browser). "Delete old ones" in the archive list removes items older than 30 days from the list.

You can also ask the fleet operator to do the same survey and tidy-up from chat
([11](11-fleet-operator.md)).

## When you can — and can't — resume

Stopped sessions can be opened and resumed with a click. However, claude / codex / cursor / copilot / kiro / agy / opencode
**cannot resume if the working folder they were launched in is gone**. In that case the state
display becomes **"Folder missing — can't resume"**, and the row is struck through and can no
longer be clicked ("Can't resume — the working folder no longer exists"). The typical case is
after deleting a working copy together with its worktree. shell falls back to home and resumes
if its working folder is missing.

When you open a stopped session, the history is first shown read-only, and **"Resume"** restarts
the conversation. After resuming, it continues with the execution method that was saved.
**Ctrl+click** (or middle-click) opens it **in a new pane** while keeping your
current view ([03 Terminal](03-terminal.md)).

Even while the workspace is stopped, **the list itself stays visible**. You can't operate on the
contents, but you can check "which sessions were there" even from a phone.

### The warning when the branch has been swapped

A session row can show a **warning icon and a branch name**. This is a sign that the session's
working copy **has switched** from the branch it was launched on to a different branch.
The working tree the running agent sees may have been swapped out, and its edits and diffs may
no longer line up. If this doesn't ring a bell, check whether an unintended branch switch has
happened. For parallel work, giving each session its own worktree avoids this confusion.

## Narrowing the view with working sets

Once you are carrying several pieces of work, the left pane fills up with the repositories and
conversations of all the others. A **working set** groups **repositories, conversations,
sessions and schedules by piece of work and switches what the left pane shows**. Nothing is
moved or copied — only **what you see** changes.

- The bar pinned at the top of the left pane (**"All"** by default) switches between groups. It
  stays visible however far the pane scrolls, so before you panic that "a session disappeared",
  you can see which group you are looking at.
- **"Manage groups…"** creates, renames and deletes them. Creating one also switches to it, so
  you can go straight to assigning rows. **Deleting a group never deletes its contents**
  (repositories, conversations, sessions).
- Assignment happens on the rows themselves, from the right-click (**⋯**) menu: base repository
  rows, conversation rows, and sessions that don't belong to a repository.
- **What follows on is included automatically.** A worktree follows its parent repository, a
  session follows its repository, and a schedule follows the conversation that created it, so
  there is nothing to reassign (those show as already ticked and cannot be unticked there).
- A repository cloned while a group is active, and a conversation started there, join that group
  automatically.
- The setting is yours and **follows you to your other devices** (it is not shared with other
  members).
- From the keyboard, the leader key **`w` → `w`** cycles through the groups.

## Handing a conversation off (handoff)

From the **⋯** menu of a running claude, codex, cursor, copilot, kiro, agy, or opencode session, choose
**"Hand off to another agent…"** and pick the **target agent** in the handoff modal that
opens. Rather than handing over the whole original
conversation as is, the **fleet operator** reads the source session's situation and drafts a
handoff proposal: the key points, unfinished tasks, changed files, and next steps.

You review the proposal, the working folder, and the agent to start, and the new session is
created only after you explicitly agree. The original session stays as it is. In a long
conversation, this lets you hand off to another agent or try a different direction without
using up the new conversation's context.

**When you want the conversation itself rather than a summary, branch instead of handing
off.** In the chat view, **"Branch here"** on one of your past messages
([07 Chat](07-chat-memo.md#branch-from-a-past-message)) opens a new session with the
conversation up to that point copied as-is, on the same agent. The exact wording and the
fine details survive, which is what you want for "redo it from that instruction".

## Messages between sessions

One of your running sessions can send **a single short message** to another. If a handoff
passes the whole context, this passes one line.

It earns its keep in situations like these:

- A session working in another worktree landed a change that breaks yours — "I just rebased
  develop; pull `api.ts` before you carry on"
- A decision the other session was blocked on got settled — "auth is OAuth, go ahead"
- A long-running job reports its result back to the session you are watching

**It is off by default.** Turn it on in **Settings > Agents > Session > "Messages between
sessions"**. The change applies to **sessions started from then on**; sessions already running
keep their current tools until they restart.

Once it is on, an agent sends a message when it judges one is needed, and you can also ask for
one ("tell the session next door what we just did").

- **Messages cross agent kinds** — claude to codex, opencode to cursor, and so on.
- **They reach a stopped session** — it is resumed first, then the message is delivered.
- **Delivery is confirmed; being read or acted on is not.** A reply is not guaranteed either.
- **Only plain text travels.** No conversation history, no files. To pass the context itself,
  use the handoff or the branch above.
- **Raw shell sessions (shell / ssm) can neither send nor receive**, because the text sent to
  them would run verbatim as a command.

The receiving agent is told explicitly that the message is *not* an instruction from you. A
message from another session **cannot stand in for your approval** (it can't answer a pending
permission prompt) and **is not a reason to edit configuration or `CLAUDE.md`**. Commands
written in the body arrive as plain text and are not executed. The chat view badges each one
with its sender, so it never blends into what you typed.

### How this differs from Claude Code's own version

Claude Code has a feature for the same purpose (cross-session messaging — `/list-agents` and
`SendMessage`), but **it is disabled in Agent Fleet**. Enabling it also switches Claude's
usage telemetry back on, which we chose not to make the default for a self-hosted fleet. That
is why `/list-agents` does nothing here; use the Agent Fleet version above instead.

| | Claude Code's | Agent Fleet's |
|---|---|---|
| Which agents you can reach | claude only | claude / codex / opencode / cursor / kiro / agy / copilot |
| A stopped session | Can't be reached | **Resumed, then delivered** |
| Record of it | One collapsed line in the terminal | A badge with the sender in the chat view |
| Sessions on another machine or the web | Can reply to them | **Not supported** (same workspace only) |
| Holding or refusing on the receiving side | Available | Not yet — only the workspace-wide on/off |

## Sharing a conversation (shared sessions)

You can show a session's conversation to another member of the same tenant, **read-only** —
"keep an eye on where this investigation goes", "while you review it, here is how we got here".

**Sharing it**

1. Choose **"Share"** from the **⋯** menu of a session row or a repository row (you can also
   create one from ⚙ "Shares" in the left pane's **Shared sessions** section → **"New share…"**).
2. There are three kinds of **target**.
   - **Session** — that one session.
   - **Project** — the base working copy **plus the sessions in the worktrees under it**. If you
     give each session its own worktree, this is usually the one you want.
   - **WT** — a single worktree.
3. Pick the **recipient** by searching for their email address.
4. Pick the **permission**.
   - **View only** — reading, nothing else.
   - **May propose** — the recipient can **propose** something to send, and **it does not reach
     the agent until you approve it**.
5. **"Unshare"** in the share list ends it.

**Being shared with**

They appear under **Shared sessions** in the left pane, grouped by owner and project. The
conversation opens read-only; with "may propose" there is a composer for sending the owner a
proposal (it says up front that nothing is sent until the owner approves). While the owner's
workspace is stopped, the history cannot be read. The list refreshes on its own, but
**"Refresh"** pulls it right now.

If the session proposed a **handoff to a new session**, that card appears in the shared
conversation too, at the point it was proposed, with the title and the full prompt. Only the
owner can edit it, discard it, or launch from it, so those buttons are not shown to you.

**Approving** (as the owner): the Shared sessions section shows **"N awaiting approval"**;
review the content and choose **"Approve and send"** or **"Reject"**.

> **Sharing exposes the whole conversation** — your prompts, the agent's replies, and tool
> output. Secrets that ended up in the conversation are not detected for you. The recipient can
> save what they see, so **unsharing cannot recall the copy they already have.** Think once about
> what is on screen before you share. Archiving a session drops it from the recipient's list (the
> rule itself remains, so restoring it makes it visible again).

## Changing the title and branch name

- **Rename** — changes the identifying name in the list. Saving it empty reverts to the automatic name (repository name + timestamp). **"Ask AI to suggest"** has a name proposed from the conversation contents; adopt it with "Use this".
- **Rename the branch** — appears only for sessions running in a worktree. Renames that worktree's branch (the folder — that is, the session — stays as is). Buttons let you swap the `feat/` `fix/` `refactor/` `chore/` `docs/` prefixes, and **"Ask AI to suggest"** proposes a branch name from the conversation. Use it to give a meaningful name later to a session started under a provisional name (`temp/…`).

---

For those who want to know how it works: [dev/04 Workspace Agent (session model)](../../dev/04-workspace-agent.md)

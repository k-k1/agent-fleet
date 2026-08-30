# 09. Troubleshooting — fixes by symptom and FAQ

English | [日本語](11-troubleshooting.ja.md)

Audience: anyone whose screen is not doing what they expected
Source of truth: the Console itself — if a screen disagrees with this page, the screen is right
Updated: 2026-08

> Audience: members when something isn't working. Start with the "by symptom" index and look
> for your situation; if nothing matches, check the FAQ at the end. Each entry links to the
> relevant chapter of the main guide.

## Index by symptom

### Selecting claude shows a login screen ("Select login method")

In most cases the connection itself is still alive. This isn't an authentication problem —
the interactive screen is just redoing its onboarding, and it usually clears if you
**resume** the session or use **"Recreate (current conversation goes to the archive)"**
from the ⋯ menu. The traditional manual `/login` inside the terminal works alongside this too.
You can check the connection status in ⚙ Settings → the "Agents" tab ([06](06-agents.md)).

### claude doesn't know `/list-agents`, or says it can't message another session

Claude Code's own cross-session messaging is **deliberately disabled** in Agent Fleet, because
enabling it also switches Claude's usage telemetry back on. Agent Fleet has its own version of
the same thing: turn on **Settings > Agents > Session > "Messages between sessions"** (off by
default; it applies to sessions started from then on). It reaches other agent kinds as well as
claude, and reaches stopped sessions too. The full comparison is in
[02 Sessions](02-sessions.md#how-this-differs-from-claude-codes-own-version).

If the agent still seems to lack the tools after you turn it on, check whether that session was
already running beforehand — a running session keeps its current tools until it restarts.

### Sessions or repositories have vanished from the left pane

**Suspect a working set filter first.** When the bar at the top of the left pane shows
anything other than **"All"**, repositories, conversations and sessions outside that group
are simply not displayed (nothing was deleted). Switch the bar back to "All" and they
return ([02](02-sessions.md#narrowing-the-view-with-working-sets)).

If it is still missing, check whether it was **archived** (AI sessions can be restored from
the archive browser) or tidied away by **cleanup** (deleted sessions can be restored from the
trash) — [02](02-sessions.md#tidying-up-in-bulk-cleanup).

### A session won't resume / is shown struck through

If the state is **"Folder missing — can't resume"**, that session's **working folder is gone**
(typically after deleting the whole worktree). claude / codex / cursor / copilot / kiro / agy / opencode cannot resume from this state.
Start the same work over as a new session. shell falls back to home and resumes if the
working folder is missing ([02](02-sessions.md)).

### I shared it, but the recipient sees nothing — or only old conversations

- **Check the scope you shared.** A session share covers that one session. If you give each
  session its own worktree, the base working copy may hold only the older conversations, so
  sharing at **project** scope (the base plus the sessions in the worktrees under it) matches
  how the work is actually laid out.
- **Archived sessions drop off the recipient's list.** The share rule itself remains, so
  restoring it from the archive makes it visible again.
- **While the owner's workspace is stopped, the history cannot be read** — the recipient sees
  a note saying so.
- The list refreshes periodically; **"Refresh"** in Shared sessions pulls it right now
  ([02](02-sessions.md#sharing-a-conversation-shared-sessions)).

### I sent something from a session shared with me, but the agent does nothing

With the **may-propose** permission, what you type **does not reach the agent until the owner
approves it**. The owner's Shared sessions section shows **"N awaiting approval"** — ask them
to review it and "Approve and send". With **view only** there is no composer in the first
place.

If the outcome of a send could not be confirmed, nothing is re-sent automatically, on purpose.
Use **"Check outcome"** to find out where it stands
([02](02-sessions.md#sharing-a-conversation-shared-sessions)).

### Cloning fails

- Private repositories need a GitHub / Bitbucket **connection** first (⚙ Settings → "Git hosting" tab). Check whether it says "Not connected".
- Double-check the spelling of the URL and branch name.
- Submodules are fetched best-effort after the clone; even if they fail, the parent clone itself succeeds ([04](03-code.md)).

### A session says the submodules are missing / broken

- A large submodule may not finish fetching inside the launch. The fetch keeps going in the
  background, and the notification center tells you when it lands ([04](03-code.md)).
- A submodule left half-fetched is repaired the next time a session launches in that working
  copy. To fix it right away, run `git submodule update --init --recursive` there in a terminal.

### Authentication errors on a private repository

In ⚙ Settings → the "Git hosting" tab, check that the provider in question is **"Connected"**.
If it isn't, connect via OAuth or a token. Once connected, authentication is transparent, so
from then on you can clone / push without entering tokens ([04](03-code.md) · [06](06-agents.md)).

### The browser pane shows `target-unreachable` / goes blank or 404

- **`target-unreachable`** means the browser started, but the connection to that port/path hasn't been established yet.
  It also appears while a dev server is **still starting up**. Check the port number and path, and that the server has really started;
  once it's up press **"Reload"**, and if the state persists, press **"Reconnect"**.
- **Listening on `127.0.0.1` (localhost) is enough for it to reach.** The browser connects directly to `127.0.0.1`
  from inside the same workspace, so a loopback-only listen opens without issues (`0.0.0.0` for external exposure is not needed).
  If it still can't reach, verify that the server really is up on that port and that you didn't mistype the port number.
- **A blank page / 404** tends to happen when you open an app that loads assets by absolute path in the **lightweight preview**. In that case
  switch the display to **"Open in pane"** (browser pane). The browser pane handles absolute assets and redirects
  just like ordinary localhost browsing, so most apps render as-is. If you really must use the lightweight preview,
  set `server.forward-headers-strategy=framework` (or `native`) for Spring Boot,
  or adjust the base path on the app side for anything else ([08](10-integrations.md)).

### HMR (live reload) doesn't work / no automatic refresh

- The **lightweight preview** is for one-off HTTP checks and **does not support WebSocket / SSE**. That's why Vite / React
  **HMR (hot reload) does not work** there. When you need HMR, use **"Open in pane"** (browser pane).
  In the browser pane, HMR, WebSocket, and SSE work just like plain localhost ([08](10-integrations.md)).

### The browser pane shows `crashed` / `disconnected`, or keeps dying

- **`disconnected`** means the communication channel (WebSocket) dropped — not necessarily an abnormal exit. Check that the
  workspace is running and press **"Reconnect"**.
- **`crashed`** means the browser inside the workspace terminated abnormally. Reopen it with **"Reconnect"**.
- **If it keeps dying within a short time**, workspace memory pressure is the likely suspect. Clean up heavy builds, watchers, and
  browser panes left open, and keep in mind that browser panes are limited to **2** at a time. For how to check memory and
  what to do about it, also see the FAQ entry "Builds die / freeze from running out of memory" below ([08](10-integrations.md)).

### No notifications arrive

They won't appear if the browser's notification permission is off (you're asked to allow it the first time). Also, notifications are
**suppressed** while you have that session's screen open. shell / ssm are outside the scope of state notifications
([02](02-sessions.md)).

### I want to connect codex, but approving doesn't get me anywhere

To connect with a ChatGPT subscription, you must first **turn on "Enable device code
authentication for Codex" under ChatGPT's "Settings > Security"**. If it's off, approving
won't get you any further ([06](06-agents.md)).

### Cleanup won't remove some things

Candidates rated **Keep** are left alone by cleanup, and the row states why.

- **Running** — stop the session first.
- **Uncommitted / unpushed** — commit or push and it becomes Safe or Review. If you must drop
  it anyway, force-delete from the Console.
- **Delete-locked** — clear the lock from the session's ⋯ menu.

Note that **only deleting a worktree cannot be undone** (the working copy goes; the history,
the remote and the branch stay). Deleted sessions and branches are stashed in the **trash**,
so the "Trash (restore)" tab of the cleanup modal brings them back
([02](02-sessions.md#tidying-up-in-bulk-cleanup)).

### A model I want isn't offered, or launching with it is refused

It may be excluded under **"Models you don't use"** in ⚙ Settings → Agents. An excluded model
disappears from the launch dialog, the default-model setting and the list an assistant picks
from, and **launching with that name is refused as well** (including a schedule's model field
and launches via an assistant). Remove the exclusion on that agent's card
([06](06-agents.md)).

To use a specific Claude release (a full id such as `claude-opus-4-8`), register it under
**"Extra Claude models"** on the same screen and it joins the choices.

### An MCP server I registered isn't available in sessions or assistants

Work through ⚙ Settings → MCP servers in this order
([12](12-settings.md#mcp-servers)).

1. Is it **enabled**? Disabled keeps the definition but hands it to nobody.
2. Do the **targets** include "sessions" / "assistants"? With both cleared it goes nowhere.
3. Did you narrow **target agents**? Leaving it empty covers every agent.
4. **Sessions pick it up from the next session you start** — a running session doesn't change
   until it restarts.
5. Press **"Connection test"** and see whether a server name and tool count come back.
6. Does it say it is unused because the name collides with a tenant distribution? The tenant
   entry wins.
7. On an egress-restricted deployment the host may still be **awaiting approval** (the card
   shows the request flow).

### An agent stopped with "your login has expired"

The error in the chat view offers **"Re-authenticate"**. Connect again from there and **that
session resumes exactly where it left off**. The agent's card in ⚙ Settings → Agents also
shows the connection state ([06](06-agents.md)).

### A Claude session shows "Login expired — sign in again", or sending starts nothing

**This workspace's Claude login has expired.** The credentials are still on disk, so the
terminal looks like an ordinary ready prompt — but nothing you send there ever starts a turn.
That is why the Console shows the **Login expired** chip instead of Ready and refuses the send
outright (without the refusal, a prompt looks delivered and then simply never runs). Fix it from
⚙ Settings → Agents → the Claude card's **Re-authenticate**. You don't need to stop the session;
it continues once you are signed in again.

The same card warns ahead of time with **"Expires in N day(s)"** (within three days). The CLI's
own warning only appears with a day left and disappears after 15 seconds, so this card is the
place where you can still notice it before it bites.

### The assistant (operator) stopped carrying things forward on its own

It hit the **automatic reply limit**. As a runaway guard, it can only run so many turns in a
row with no message from you (default 10, max 50), and **it resumes when you send the next
message**. The limit is in ⚙ Settings → Assistant (it cannot be unlimited).

If you only want normal completions to stay quiet, turn on **"Quiet completion reports"** in
the same tab: the card and the notification still arrive, but no automatic turn runs
([11](08-organising.md)).

### The chat says the context went over the limit and won't answer

The conversation grew too long. Either **"Compact"** at the right of the context bar (carry a
summary forward and continue) or open a **new chat** and hand over just the essentials.
Starting an exchange above 90% auto-compacts first (can be turned off in ⚙ Settings →
Assistant), but exceeding the limit before that point produces this state. Lowering the
**automatic compaction threshold** in ⚙ Settings → Assistant makes it less likely to recur
([07](07-chat-memo.md#when-a-conversation-gets-long-a-context-rule-of-thumb)).

### A scheduled run doesn't fire

Check its row in the **Schedules** section of the left pane.

- A **"Paused"** tag means it is suspended — **"Resume"** in the row menu brings it back.
- `skipped_*` entries in the **run history** mean the previous run was still going (the
  overlap policy) or the target conversation was busy.
- The firing time, timezone and prompt are visible and editable under **"Details & edit"** in
  the row menu (only the advanced fields — session mode, reuse and so on — are changed from
  the operator chat).
- **"Run now"** exercises the same path as a timed firing (allow up to about a minute).
- If there is no Schedules section at all, scheduled execution is disabled on this deployment
  ([11](08-organising.md#scheduled-runs)).

### Ctrl+C doesn't work in the terminal / I can't copy-paste

Working as intended. In the terminal, Ctrl+C is passed to the program as an interrupt (SIGINT). **Copy is
automatic on select, or Ctrl+Shift+C; paste with right-click / middle-click / Ctrl+Shift+V**
([03](05-terminal.md)).

### App shortcuts such as the command palette don't work

While a terminal has focus, a setting in ⚙ Settings → Keys may be handing your keys to the
terminal.

- **"Prioritise the terminal over the app while a terminal has focus"** — every Ctrl-key goes
  to the terminal. Only the leader survives on the app side, so open the palette with the
  default **Ctrl+K → ;** (**⌘K → ;** on macOS).
- **"Pass every key to shell / SSM terminals"** — even the leader and the palette go through.
  Move focus to another pane to get the app operations back.

**?** opens the list of what is bound to what ([03](05-terminal.md#shortcuts),
[12](12-settings.md#keys)).

### On a phone, the keyboard pops up on its own / it's hard to operate

The **control key row** under the terminal (`Esc` `Tab` arrows `^C` `⏎`) sends keys without
bringing up the soft keyboard. You can scroll back through past output with a **one-finger vertical swipe**. Toggle the left pane
with the **≡ (menu)** at the top left of the screen ([03](05-terminal.md)).

### Only the terminal stays dark on the light theme

Known behavior. Even after switching themes, the terminal (the black screen) background stays dark. The file
viewer and other screens follow the theme ([03](05-terminal.md)).

### The workspace has stopped without me noticing

It **stops automatically** after a while with no activity (idle auto-stop). Press "Start" in the workspace action bar
once more, and your session list, clones, and connections all come back as they were ([01](01-first-day.md)).

## FAQ

**Q. Can I get a deleted session back?**
If you only hid it with "Archive", you can bring it back with "Restore" in the archived list. With "Stop
(resumable later)" it remains as stopped and can be resumed with a click. "Delete" for shell / ssm
cannot be undone (the conversation log files themselves remain). For the differences, see
[02 Sessions](02-sessions.md).

**Q. Can I use it from both my work PC and my home PC?**
Yes. Your login and connections, as well as display settings like font / text size, are stored on the server
and follow you on other devices and browsers ([03](05-terminal.md)).

**Q. If I stop the workspace, is my work lost?**
No. Clones, uncommitted changes, the session list, and connections all remain. However,
uncommitted changes exist only inside the workspace, so commit / push for long-term safekeeping
([01](01-first-day.md) · [04](03-code.md)).

**Q. Is it OK to run several tasks at the same time?**
Yes. If each session works in its own independent worktree, edits won't collide. Worktrees are
created by default ([02](02-sessions.md) · [04](03-code.md)).

**Q. What's the difference between "Stop", "Delete", "Archive", and "Recreate"?**
Stop = just pause (resumable), Delete = remove from the list (throwaway kinds), Archive = hide (restorable),
Recreate = a new conversation in the same place (the current conversation goes to the archive). Details in [02](02-sessions.md).

**Q. opencode doesn't show up among the new-session kinds**
It can't be selected until you've registered at least one API key. Register a key for opencode in
⚙ Settings → the "Agents" tab ([06](06-agents.md)).

**Q. If I unshare it, does it disappear from their side too?**
No. The recipient can save what was displayed, so **unsharing only ends further access** — a
copy they already saved cannot be recalled. Before sharing, check what the conversation
exposes (secrets in it are not detected for you). See
[02](02-sessions.md#sharing-a-conversation-shared-sessions).

**Q. Does "Models you don't use" stop the billing?**
It prevents picking one by accident; it is not a hard billing guard. The model disappears from
the launch dialog, the settings and the assistant's list, and launching it by name is refused
— but **it cannot stop the CLI's own commands, such as typing `/model` inside the terminal**
([06](06-agents.md)).

**Q. Builds die / freeze from running out of memory**
The host is shared and memory-constrained. For Node builds, raise the heap only for the command that needs it
(e.g. `NODE_OPTIONS=--max-old-space-size=2048`), keep test-runner parallelism low, and don't leave watchers or
dev servers running. With Gradle, run `./gradlew --stop` when you're done.
Run heavy builds one at a time.

**Q. I can't find `git-delta` or `sudo`**
They are deliberately left out of the workspace image (to preserve isolation, among other reasons). Tools you need
can be installed into your user area yourself (`pip install --user` persists).

---

If this doesn't solve it, ask your team admin or IT department, including the symptom and
(if any) the message that was shown. The internals are covered in the developer docs [dev/](../build/README.md).

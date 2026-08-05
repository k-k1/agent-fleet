# 09. Troubleshooting — fixes by symptom and FAQ

English | [日本語](09-troubleshooting.ja.md)

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

### A session won't resume / is shown struck through

If the state is **"Folder missing — can't resume"**, that session's **working folder is gone**
(typically after deleting the whole worktree). claude / codex / cursor / copilot / kiro / agy / opencode cannot resume from this state.
Start the same work over as a new session. shell falls back to home and resumes if the
working folder is missing ([02](02-sessions.md)).

### Cloning fails

- Private repositories need a GitHub / Bitbucket **connection** first (⚙ Settings → "Git hosting" tab). Check whether it says "Not connected".
- Double-check the spelling of the URL and branch name.
- Submodules are fetched best-effort after the clone; even if they fail, the parent clone itself succeeds ([04](04-git.md)).

### A session says the submodules are missing / broken

- A large submodule may not finish fetching inside the launch. The fetch keeps going in the
  background, and the notification center tells you when it lands ([04](04-git.md)).
- A submodule left half-fetched is repaired the next time a session launches in that working
  copy. To fix it right away, run `git submodule update --init --recursive` there in a terminal.

### Authentication errors on a private repository

In ⚙ Settings → the "Git hosting" tab, check that the provider in question is **"Connected"**.
If it isn't, connect via OAuth or a token. Once connected, authentication is transparent, so
from then on you can clone / push without entering tokens ([04](04-git.md) · [06](06-agents.md)).

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
  or adjust the base path on the app side for anything else ([08](08-advanced.md)).

### HMR (live reload) doesn't work / no automatic refresh

- The **lightweight preview** is for one-off HTTP checks and **does not support WebSocket / SSE**. That's why Vite / React
  **HMR (hot reload) does not work** there. When you need HMR, use **"Open in pane"** (browser pane).
  In the browser pane, HMR, WebSocket, and SSE work just like plain localhost ([08](08-advanced.md)).

### The browser pane shows `crashed` / `disconnected`, or keeps dying

- **`disconnected`** means the communication channel (WebSocket) dropped — not necessarily an abnormal exit. Check that the
  workspace is running and press **"Reconnect"**.
- **`crashed`** means the browser inside the workspace terminated abnormally. Reopen it with **"Reconnect"**.
- **If it keeps dying within a short time**, workspace memory pressure is the likely suspect. Clean up heavy builds, watchers, and
  browser panes left open, and keep in mind that browser panes are limited to **2** at a time. For how to check memory and
  what to do about it, also see the FAQ entry "Builds die / freeze from running out of memory" below ([08](08-advanced.md)).

### No notifications arrive

They won't appear if the browser's notification permission is off (you're asked to allow it the first time). Also, notifications are
**suppressed** while you have that session's screen open. shell / ssm are outside the scope of state notifications
([02](02-sessions.md)).

### I want to connect codex, but approving doesn't get me anywhere

To connect with a ChatGPT subscription, you must first **turn on "Enable device code
authentication for Codex" under ChatGPT's "Settings > Security"**. If it's off, approving
won't get you any further ([06](06-agents.md)).

### Ctrl+C doesn't work in the terminal / I can't copy-paste

Working as intended. In the terminal, Ctrl+C is passed to the program as an interrupt (SIGINT). **Copy is
automatic on select, or Ctrl+Shift+C; paste with right-click / middle-click / Ctrl+Shift+V**
([03](03-terminal.md)).

### On a phone, the keyboard pops up on its own / it's hard to operate

The **control key row** under the terminal (`Esc` `Tab` arrows `^C` `⏎`) sends keys without
bringing up the soft keyboard. You can scroll back through past output with a **one-finger vertical swipe**. Toggle the left pane
with the **≡ (menu)** at the top left of the screen ([03](03-terminal.md)).

### Only the terminal stays dark on the light theme

Known behavior. Even after switching themes, the terminal (the black screen) background stays dark. The file
viewer and other screens follow the theme ([03](03-terminal.md)).

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
and follow you on other devices and browsers ([03](03-terminal.md)).

**Q. If I stop the workspace, is my work lost?**
No. Clones, uncommitted changes, the session list, and connections all remain. However,
uncommitted changes exist only inside the workspace, so commit / push for long-term safekeeping
([01](01-first-day.md) · [04](04-git.md)).

**Q. Is it OK to run several tasks at the same time?**
Yes. If each session works in its own independent worktree, edits won't collide. Worktrees are
created by default ([02](02-sessions.md) · [04](04-git.md)).

**Q. What's the difference between "Stop", "Delete", "Archive", and "Recreate"?**
Stop = just pause (resumable), Delete = remove from the list (throwaway kinds), Archive = hide (restorable),
Recreate = a new conversation in the same place (the current conversation goes to the archive). Details in [02](02-sessions.md).

**Q. opencode doesn't show up among the new-session kinds**
It can't be selected until you've registered at least one API key. Register a key for opencode in
⚙ Settings → the "Agents" tab ([06](06-agents.md)).

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
(if any) the message that was shown. The internals are covered in the developer docs [dev/](../../dev/README.md).

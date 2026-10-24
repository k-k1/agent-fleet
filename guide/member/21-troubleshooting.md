---
audience: "anyone whose screen is not doing what they expected"
updated: "2026-10"
---

# 21. Troubleshooting — fixes by symptom and FAQ

English | [日本語](21-troubleshooting.ja.md)

Start with the "by symptom" index and look for your situation; if nothing matches,
check the FAQ at the end. Each entry links to the relevant chapter of the main guide.

## Index by symptom

### Selecting claude shows a login screen ("Select login method")

In most cases the connection itself is still alive. This isn't an authentication problem:
the interactive screen is just redoing its onboarding, and it usually clears if you
**resume** the session or use **"Recreate (current conversation goes to the archive)"**
from the ⋯ menu. The traditional manual `/login` inside the terminal works alongside this too.
You can check the connection status in ⚙ Settings → the "Agents" tab ([09](09-agents.md)).

### claude doesn't know `/list-agents`, or says it can't message another session

Claude Code's own cross-session messaging is **deliberately disabled** in Agent Fleet, because
enabling it also switches Claude's usage telemetry back on. Agent Fleet has its own version of
the same thing: turn on **Settings > Agents > Session > "Messages between sessions"** (off by
default; it applies to sessions started from then on). It reaches other agent kinds as well as
claude, and reaches stopped sessions too. The full comparison is in
[11 Running many sessions](11-many-sessions.md#how-this-differs-from-claude-codes-own-version).

If the agent still seems to lack the tools after you turn it on, check whether that session was
already running beforehand: a running session keeps its current tools until it restarts.

### A **Restart needed** badge sits next to the power button

The backend was updated while your workspace was running. Nothing is broken and nothing has to
happen now: the badge offers **Restart now** (a stop→start, which stops running sessions;
they stay resumable) and goes away once the workspace is back on the current version
([01](01-first-day.md#a-new-version-is-available-and-the-restart-needed-badge)).

### The workspace went from "starting" to "stopped" on its own

A start that does not finish within the deployment's limit (30 minutes by default) is stopped
automatically, so it stops holding capacity. The notification center (the bell) then shows
**"Workspace start stopped automatically"** with the last step the start reached; on AWS it
often says why no machine could take the workspace. Press **Start** to try again; if it stops
the same way, tell your administrator: they see the same reason on your entry in the member
list until you start again.

### Sessions or repositories have vanished from the left pane

**Suspect a working set filter first.** When the bar at the top of the left pane shows
anything other than **"All"**, repositories, conversations and sessions outside that group
are simply not displayed (nothing was deleted). Switch the bar back to "All" and they
return ([11](11-many-sessions.md#narrowing-the-view-with-working-sets)).

If it is still missing, check whether it was **archived** (AI sessions can be restored from
the archive browser) or tidied away by **cleanup** (deleted sessions can be restored from the
trash). See [05](05-stopping-and-tidying.md#tidying-up-in-bulk-cleanup).

If the **Repositories** section of the left pane says **"Couldn't refresh the list — showing the
last known one"**, nothing is gone: the Console could not read the repository list just now and
shows the last one it had. While the workspace runs, the list comes back by itself.

### A session won't resume / is shown struck through

If the state is **"Folder missing — can't resume"**, that session's **working folder is gone**
(typically after deleting the whole worktree). claude / codex / cursor / copilot / kiro / agy / opencode / lcpp / muse cannot resume from this state.
Start the same work over as a new session. shell falls back to home and resumes if the
working folder is missing ([03](03-starting-sessions.md)).

### Resuming says "the conversation log was deleted"

claude deletes conversation logs older than `cleanupPeriodDays` (30 days by default) when it
starts. The workspace sets that to 36500 so stopped sessions keep their logs, but a log deleted
earlier, or a lower value in your own claude `settings.json`, can still delete a log. The Agent
keeps a copy of each stopped session's log and puts it back when you resume, so this message only
appears when no copy exists (the log vanished before the first copy, or before the feature) — the
copy cannot be brought back from the Console by hand. If resuming instead fails with "could not
restore the conversation log from the backup", the restore itself failed (the Agent log has the
reason: an unreadable folder, a full disk, or a log that appeared meanwhile). The backup is kept;
a stub log that was in the way may have been moved aside as `.superseded.<number>`. Resume again
after fixing the cause. Choose **"Start a new conversation"** to continue in
the same session without the old context, and remove a low `cleanupPeriodDays` if you set one
(see [03](03-starting-sessions.md)).

### I shared it, but the recipient sees nothing — or only old conversations

- **Check the scope you shared.** A session share covers that one session. If you give each
  session its own worktree, the base working copy may hold only the older conversations, so
  sharing at **project** scope (the base plus the sessions in the worktrees under it) matches
  how the work is actually laid out.
- **Archived sessions drop off the recipient's list.** The share rule itself remains, so
  restoring it from the archive makes it visible again.
- **While the owner's workspace is stopped, the history cannot be read**: the recipient sees
  a note saying so.
- The list refreshes periodically; **"Refresh"** in Shared sessions pulls it right now
  ([12](12-collaboration.md#sharing-a-conversation-shared-sessions)).

### I sent something from a session shared with me, but the agent does nothing

With the **may-propose** permission, what you type **does not reach the agent until the owner
approves it**. The owner's Shared sessions section shows **"N awaiting approval"**. Ask them
to review it and "Approve and send". With **view only** there is no composer in the first
place.

If the outcome of a send could not be confirmed, nothing is re-sent automatically, on purpose.
Use **"Check outcome"** to find out where it stands
([12](12-collaboration.md#sharing-a-conversation-shared-sessions)).

### Cloning fails

- Private repositories need a GitHub / Bitbucket **connection** first (⚙ Settings → "Git hosting" tab). Check whether it says "Not connected".
- Double-check the spelling of the URL and branch name.
- Submodules are fetched best-effort after the clone; even if they fail, the parent clone itself succeeds ([06](06-code.md)).

### A session says the submodules are missing / broken

- A large submodule may not finish fetching inside the launch. The fetch keeps going in the
  background, and the notification center tells you when it lands ([06](06-code.md)).
- A submodule left half-fetched is repaired the next time a session launches in that working
  copy. To fix it right away, run `git submodule update --init --recursive` there in a terminal.

### Authentication errors on a private repository

In ⚙ Settings → the "Git hosting" tab, check that the provider in question is **"Connected"**.
If it isn't, connect via OAuth or a token. Once connected, authentication is transparent, so
from then on you can clone / push without entering tokens ([06](06-code.md) · [09](09-agents.md)).

### The browser pane shows `target-unreachable` / goes blank or 404

- **`target-unreachable`** means the browser started, but the connection to that port/path hasn't been established yet.
  It also appears while a dev server is **still starting up**. Check the port number and path, and that the server has really started;
  once it's up press **"Reload"**, and if the state persists, press **"Reconnect"**.
- **Listening on `127.0.0.1` (localhost) is enough for it to reach.** The browser connects directly to `127.0.0.1`
  from inside the same workspace, so a loopback-only listen opens without issues (`0.0.0.0` for external exposure is not needed).
  If it still can't reach, verify that the server really is up on that port and that you didn't mistype the port number.
- **The lightweight preview or a preview subdomain shows a 502 page** that says which kind of failure it is:
  *nothing is listening on the port* (start the app, reload); *the port is listening but does not speak HTTP*
  (a database, SSH, gRPC over h2c: the preview carries HTTP and WebSocket only); *the port is serving HTTPS*
  (turn off the dev server's HTTPS to preview it); or *the workspace is not running or cannot be reached*.
  The page follows the browser's language (Japanese or English). The browser pane connects directly, so its
  failure stays `target-unreachable`, but when the Agent finds the port is listening without speaking HTTP, or serving
  HTTPS, the pane's message says so instead of the generic one (nothing listening keeps the generic message).
- **A blank page / 404** tends to happen when you open an app that loads assets by absolute path in the **lightweight preview**.
  Its URL is a **sub-path** (`/preview/{port}/`), so the moment the app asks for `/static/...` it lands outside.
  **If preview subdomains are issued on your deployment, open it there first**: the app is served at the root, so
  the problem cannot arise. Otherwise switch to **"Open in pane"** (browser pane). If you really must use the
  lightweight preview, set `server.forward-headers-strategy=framework` (or `native`) for Spring Boot,
  or adjust the base path on the app side for anything else ([14](14-preview-browser.md)).

### The preview URL I gave a colleague returns 404

- **A preview subdomain URL changes every time the workspace restarts.** Pasting
  `https://<random>-3000.…` as-is guarantees a 404 on the next start. Hand out the link from the
  **"Share"** button in the preview popover instead: **that one survives restarts** and sends the
  visitor to whatever the URL is at the time they open it.
- If they see **nothing under "Shared with you", or a 404**, check that **"Show it to your tenant"**
  is on under Settings › Preview subdomains. Turning it off closes the preview
  **from their very next request**, even mid-session; that is deliberate.
- **"Stopped" on their side** means your workspace is not running. **They cannot start your
  workspace**. Start it and tell them.
- Removing the port from the exposed-ports list also produces a 404 ([14](14-preview-browser.md)).

### HMR (live reload) doesn't work / no automatic refresh / the shell renders but no data

- **If this is Next.js (15.2 and later, including 16.x) opened on a preview subdomain**, suspect this first.
  **Only the layout shell renders and not one row of the data fetched from the API appears** (not blank, no
  error), so it looks like a bug in your own app, yet `127.0.0.1:<port>` shows it correctly. The dev server
  **blocks cross-origin access to `/_next/*` by default**, so HMR never connects and hydration never finishes.
  `⚠ Blocked cross-origin request to Next.js dev resource` in the dev server log confirms it. Add
  **`allowedDevOrigins`** to `next.config.ts` (the URL changes on every start, so a wildcard is required;
  see [14](14-preview-browser.md) for the exact form).
- **The lightweight preview and the preview subdomains both pass WebSocket and SSE through.** If HMR still does not
  connect, the dev server may be **embedding its own port into the client** (Vite does this). Then the app needs a
  setting such as `server.hmr.clientPort: 443`.
- To avoid adding configuration, or to isolate the cause, use **"Open in pane"** (browser pane). A browser inside
  the workspace opens `127.0.0.1` directly, so HMR, WebSocket and SSE behave exactly like plain localhost
  ([14](14-preview-browser.md)).

### Every API call in the preview returns 401 (`preview requires sign-in`)

- **Did you repoint the dev server's proxy at the preview URL?** Keep Vite's `server.proxy` `target` and
  Next.js's `rewrites()` `destination` as **`http://127.0.0.1:8080`**. The proxy is a **server-side** call made
  by the dev server inside the container; it does not carry the login state your browser has, so pointing it at
  a preview URL gets it rejected at authentication. Assuming the frontend "cannot see" 8080 is the usual way
  into this mistake ([14](14-preview-browser.md)).
- **If the browser is calling the 8080 URL directly**, note that signing in is per port. Open
  `https://<random>-8080.…` **in a tab once** to sign in there too. And if the page on 3000 calls 8080
  directly, turn on **"Allow calls between ports"** in the settings (off by default).

### The browser pane shows `crashed` / `disconnected`, or keeps dying

- **`disconnected`** means the communication channel (WebSocket) dropped; it is not necessarily an abnormal exit. Check that the
  workspace is running and press **"Reconnect"**.
- **`crashed`** means the browser inside the workspace terminated abnormally. Reopen it with **"Reconnect"**.
- **If it keeps dying within a short time**, workspace memory pressure is the likely suspect. Clean up heavy builds, watchers, and
  browser panes left open, and keep in mind that browser panes are limited to **2** at a time. For how to check memory and
  what to do about it, also see the FAQ entry "Builds die / freeze from running out of memory" below ([14](14-preview-browser.md)).

### No notifications arrive

They won't appear if the browser's notification permission is off (you're asked to allow it the first time). Also, notifications are
**suppressed** while you have that session's screen open. shell / ssm are outside the scope of state notifications
([03](03-starting-sessions.md)).

### I want to connect codex, but approving doesn't get me anywhere

To connect with a ChatGPT subscription, you must first **turn on "Enable device code
authentication for Codex" under ChatGPT's "Settings > Security"**. If it's off, approving
won't get you any further ([09](09-agents.md)).

### Cleanup won't remove some things

Candidates rated **Keep** are left alone by cleanup, and the row states why.

- **Running**: stop the session first.
- **Uncommitted / unpushed**: commit or push and it becomes Safe or Review. If you must drop
  it anyway, force-delete from the Console.
- **Delete-locked**: clear the lock from the session's ⋯ menu.

Deleted sessions, branches and worktrees are stashed in the **trash**, so the "Trash
(restore)" tab of the cleanup modal brings them back (a worktree at the commit it was on, with
its uncommitted changes when the row says so)
([05](05-stopping-and-tidying.md#tidying-up-in-bulk-cleanup)).

### A model I want isn't offered, or launching with it is refused

It may be excluded under **"Models you don't use"** in ⚙ Settings → Agents. An excluded model
disappears from the launch dialog, the default-model setting and the list an assistant picks
from, and **launching with that name is refused as well** (including a schedule's model field
and launches via an assistant). Remove the exclusion on that agent's card
([09](09-agents.md)).

To use a specific Claude release (a full id such as `claude-opus-4-8`), register it under
**"Extra Claude models"** on the same screen and it joins the choices.

### An opencode session says "working" but nothing arrives

Look for the band above the conversation: **"The provider returned an error and the agent is
retrying (attempt N): …"**. opencode is retrying a provider error; the message says which. Rate
limits and overloads usually pass, so wait or stop the turn. Errors that cannot pass (region or
privacy setting, quota or billing, a bad key) end the turn as failed by themselves and the band
says so: fix the cause on the provider account (for "requires Global regions", its privacy /
region setting) and send again ([09](09-agents.md)).

The band appears in **Managed** sessions. A **Terminal (CLI)** session has no band; opencode's
own retry output is in the terminal.

### The launch dialog offers only "Default"

The agents with a live catalog (codex / opencode / copilot / cursor / kiro / agy / lcpp / muse) fetch their
model list when the dialog opens, so **"Loading models…" for a moment is normal**. Wait for
it before concluding anything. What the picker says once it settles is the answer:

| It says | What to do |
|---|---|
| **Every model is excluded in settings** | ⚙ Settings → Agents → that card → "Models you don't use" |
| **The current billing choice leaves no model to pick** | ⚙ Settings → Agents → opencode → "opencode.ai billing" ([09](09-agents.md)) |
| **Only the default model is available (check this agent's connection and plan)** | The agent answered with an empty catalog. Check the sign-in and the plan on that card. On a Copilot Free plan an empty list is normal: that plan offers Auto alone. |
| **Couldn't fetch the model list — the workspace may have only just started** | The request never landed. The Agent isn't listening yet in the first seconds after a start: **wait half a minute and reopen the dialog** (each open tries again). |

### An MCP server I registered isn't available in sessions or assistants

Work through ⚙ Settings → MCP servers in this order
([20](20-settings.md#mcp-servers)).

1. Is it **enabled**? Disabled keeps the definition but hands it to nobody.
2. Do the **targets** include "sessions" / "assistants"? With both cleared it goes nowhere.
3. Did you narrow **target agents**? Leaving it empty covers every agent.
4. **Sessions pick it up from the next session you start**: a running session doesn't change
   until it restarts.
5. Press **"Connection test"** and see whether a server name and tool count come back.
6. Does it say it is unused because the name collides with a tenant distribution? The tenant
   entry wins.
7. On an egress-restricted deployment the host may still be **awaiting approval** (the card
   shows the request flow).

**Connection test passes (or now fails, saying the server rejected the session id it had just
issued), but the session reports `Session not found`.** The test's messages are in Japanese
whatever the display language. The server keeps MCP sessions in
memory (a stateful Streamable HTTP server) and runs as **several instances without session
affinity**: `initialize` lands on one instance, which issues the session id, and the next
request lands on another that has never heard of it. Agent Fleet can't fix the server. The
Connection test now sends several session-bound requests and reports this case itself; an
older build could pass it by luck.

- Make the server **stateless** (for example a stateless Streamable HTTP mode), or run a
  **single instance**, or route every request carrying the same `Mcp-Session-Id` to the same
  instance (session-id-aware routing).
- A Connection test over one reused connection can look fine while real clients, which open several parallel connections, fail: the balancer keeps a reused connection on one instance. That is why the test opens a **new connection for each session-bound check**.
- **Load-balancer cookie stickiness doesn't help**: Claude Code doesn't send cookies back on
  MCP requests, so the balancer never sees its sticky cookie again.
- If an unknown session gets **HTTP 404**, Claude Code re-initializes by itself. If it gets
  **HTTP 400**, it does not recover: **restart the session** once the server is fixed.
  (Measured on Claude Code 2.1.303 against a local repro server.)

### An agent stopped with "your login has expired"

The error in the chat view offers **"Re-authenticate"**, which opens ⚙ Settings → Agents
scrolled to that agent's card. Connect again there and **that session resumes exactly where it
left off**. The agent's card in ⚙ Settings → Agents also
shows the connection state ([09](09-agents.md)).

Once you are signed in again, **that same error turns into "Re-authenticated"** (the button
goes away) and **the session picks up where it stopped by itself** (Claude, with automatic
resume on; the mirror shows the continuation carrying an auto-resume badge). The failure
itself stays red: it did happen, so it is not erased. If nothing continues after a while, send
"continue" in that conversation and it resumes from the same point.

In the **Assistant** chat the same failure ("OAuth session expired and could not be refreshed")
shows a short "Your Claude login has expired" notice with **Re-authenticate** (opens the Claude
card) and **Retry** (sends the failed message again once you are signed in). The original error
is under "Details".

### A Claude session shows "Login expired — sign in again", or sending starts nothing

**This workspace's Claude login has expired.** The credentials are still on disk, so the
terminal looks like an ordinary ready prompt, but nothing you send there ever starts a turn.
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
([11](11-many-sessions.md)).

### The chat says the context went over the limit and won't answer

The conversation grew too long. Either **"Compact"** at the right of the context bar (carry a
summary forward and continue) or open a **new chat** and hand over just the essentials.
Starting an exchange above 90% auto-compacts first (can be turned off in ⚙ Settings →
Assistant), but exceeding the limit before that point produces this state. Lowering the
**automatic compaction threshold** in ⚙ Settings → Assistant makes it less likely to recur
([10](10-chat-memo.md#when-a-conversation-gets-long-a-context-rule-of-thumb)).

### A scheduled run doesn't fire

Check its row in the **Schedules** section of the left pane.

- A **"Paused"** tag means it is suspended; **"Resume"** in the row menu brings it back.
- `skipped_*` entries in the **run history** mean the previous run was still going (the
  overlap policy) or the target conversation was busy.
- The firing time, timezone and prompt are visible and editable under **"Details & edit"** in
  the row menu (only the advanced fields, session mode, reuse and so on, are changed from
  the operator chat).
- **"Run now"** exercises the same path as a timed firing (allow up to about a minute).
- If there is no Schedules section at all, scheduled execution is disabled on this deployment
  ([11](11-many-sessions.md#scheduled-runs)).

### Ctrl+C doesn't work in the terminal / I can't copy-paste

Working as intended. In the terminal, Ctrl+C with nothing selected is passed to the program as an
interrupt (SIGINT); with a selection it copies. **Copy is automatic on select, or Ctrl+C with a
selection / Ctrl+Shift+C; paste with right-click / middle-click / Ctrl+V / Ctrl+Shift+V**.

- Ctrl+C right after a mouse drag interrupts: the drag already copied the selection when you let
  go.
- Ctrl+C and Ctrl+V copy and paste only while **"Ctrl+C / Ctrl+V copy and paste"** is on in
  ⚙ Settings → Display (on by default). With it off both keys go to the program; Ctrl+Shift+C /
  Ctrl+Shift+V always work.

See [08](08-terminal.md#copy-paste).

### App shortcuts such as the command palette don't work

While a terminal has focus, a setting in ⚙ Settings → Keyboard may be handing your keys to the
terminal.

- **"Prefer the terminal over the app while the terminal is focused"**: every Ctrl-key goes
  to the terminal. Only the leader survives on the app side, so open the palette with the
  default **Ctrl+K → ;** (**⌘K → ;** on macOS).
- **"Pass all keys to shell / SSM terminals (incl. Ctrl+K, Ctrl+P)"**: even the leader and the palette go through.
  Move focus to another pane to get the app operations back.

**?** opens the list of what is bound to what ([02](02-console-basics.md#shortcuts),
[20](20-settings.md#keyboard)).

### "Your home disk is nearly full"

The notice appears when less than 2 GiB is free or more than 90 % of the home is used, and goes
away once more than 3 GiB is free and use is below 85 %. Installs and agents can stop working
when the disk fills. Free space from ⚙ Settings → Machine, or ask a tenant administrator to grow
the home. You see it only where the home is a volume of your own (ECS on EC2); on shared storage
the numbers would include other people's files, so no notice is shown
([20](20-settings.md#machine)).

### Starting a session says the agent's CLI could not be installed

**"This agent's CLI could not be installed when the workspace started …"** means the install of
that CLI failed at this start and the CLI is not there, so the session is refused. The message
says why:

| The message ends with | What to do |
|---|---|
| … because the home disk is full | Free some space, then restart the workspace. |
| … because of a network error | Restart the workspace to try again. |
| … because the download did not match its checksum | Restart the workspace to try again. |
| … See Settings › Toolchains and the workspace log | Read the reason in ⚙ Settings → Toolchains ([20](20-settings.md#toolchains)). |

Your tenant administrator sees the same failure on your card in the member list.

### A session cannot be moved to another agent

The move refuses while the session is working, or while a question, plan or approval waits for
you: let the turn end or stop it, and answer first. A stopped Managed Cursor session cannot be
moved, because its conversation is gone. shell and ssm sessions have no conversation to move. See
[05](05-stopping-and-tidying.md#moving-a-session-to-another-agent-move).

### AWS commands say "Unable to locate credentials", or `af-aws-exec` / `af-gcloud-exec` stops

- **"Unable to locate credentials"** in a workspace means the command found no credentials of
  yours: the workspace's own role is withheld from your shell. Do not configure credentials. Run
  the command as you with the profile from ⚙ Settings:
  `af-aws-exec --profile <name> --account <id> -- <command>`. A plain `aws --profile` is only for
  the read-only lookups that chapter 15 allows after its isolation check
  ([16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools)).
- **`af-gcloud-exec` exit code 3** means a login only when the wrapper says so ("the login was
  requested in the Agent Fleet Console …", "Google Cloud login required …"). Log in from the toast
  or ⚙ Settings, or run the terminal command it printed, then run the command again. Otherwise the
  status is the command's own: read that command's error
  ([16](16-cloud-accounts.md#when-it-stops)).

### Signing in on a preview URL shows an Agent Fleet error page

These pages come from the development OIDC IdP
([14](14-preview-browser.md#signing-in-with-google-on-a-preview-url-development-oidc-idp)).

| Page | Cause and fix |
|---|---|
| **Can't start the sign-in** | The request was refused. Check that the client ID and callback URL set in the app belong to this workspace, then sign in again from the app. |
| **Callback address not allowed** | The app's callback URL is not this workspace's current preview URL or `http://localhost:<port>`. Fix it in the app and sign in again. |
| **Sign-in confirmation is no longer valid** | The confirmation was refused, expired, or was already used. Sign in again from the app. |
| **Can't complete the sign-in** | A problem on the server. Try again in a moment; if it keeps failing, ask the deployment's administrator. |
| **Sign in from your app** | You opened one step of the sign-in on its own. Start from the app you are previewing. |

### A preview URL says the port changed, too many ports are open, or the workspace must restart

These messages come from the preview, not from the app:

| Message | What to do |
|---|---|
| **"The listener on port N changed or stopped. Reopen the port from the Console."** | The server on that port was restarted or replaced. Open the port again from the Console (the port chip or Preview). |
| **"Too many temporarily opened ports. Close a server or add the port to the preview ports in Settings."** | Stop a server you no longer need, or list the port under ⚙ Settings → Preview subdomains. |
| **"Opening port N is not available until the workspace is restarted (its agent is older than this feature)."** | Restart the workspace. |

See [14](14-preview-browser.md#preview-subdomains-only-where-they-are-issued).

### On a phone, the keyboard pops up on its own / it's hard to operate

The **control key row** under the terminal (`Esc` `Tab` arrows `^C` `⏎`) sends keys without
bringing up the soft keyboard. You can scroll back through past output with a **one-finger vertical swipe**. Toggle the left pane
with the **≡ (menu)** at the top left of the screen ([08](08-terminal.md)).

### Only the terminal stays dark on the light theme

This is known behavior. Even after switching themes, the terminal (the black screen) background stays dark. The file
viewer and other screens follow the theme ([08](08-terminal.md)).

### The workspace has stopped without me noticing

It **stops automatically** after a while with no activity (idle auto-stop). Press "Start" in the workspace action bar
once more, and your session list, clones, and connections all come back as they were ([01](01-first-day.md)).

## FAQ

**Q. Can I get a deleted session back?**
If you only hid it with "Archive", you can bring it back with "Restore" in the archived list. With "Stop
(resumable later)" it remains as stopped and can be resumed with a click. "Delete" for shell / ssm
cannot be undone (the conversation log files themselves remain). For the differences, see
[03 Starting sessions](03-starting-sessions.md).

**Q. Can I use it from both my work PC and my home PC?**
Yes. Your login and connections, as well as display settings like font / text size, are stored on the server
and follow you on other devices and browsers ([08](08-terminal.md)).

**Q. If I stop the workspace, is my work lost?**
No. Clones, uncommitted changes, the session list, and connections all remain. However,
uncommitted changes exist only inside the workspace, so commit / push for long-term safekeeping
([01](01-first-day.md) · [06](06-code.md)).

**Q. Is it OK to run several tasks at the same time?**
Yes. If each session works in its own independent worktree, edits won't collide. Worktrees are
created by default ([03](03-starting-sessions.md) · [06](06-code.md)).

**Q. What's the difference between "Stop", "Delete", "Archive", and "Recreate"?**
Stop = just pause (resumable), Delete = remove from the list (throwaway kinds), Archive = hide (restorable),
Recreate = a new conversation in the same place (the current conversation goes to the archive). Details in [03](03-starting-sessions.md).

**Q. opencode doesn't show up among the new-session kinds**
On its card in ⚙ Settings → Agents, **"Use opencode"** must be on, and the **"opencode.ai
billing"** choice must leave something to launch: **"Free models only"** needs nothing,
**"Go (subscription)"** and **"Zen (metered)"** need an opencode.ai sign-in or key, and
**"None (my own keys)"** needs a provider key of your own or one of the fleet's engines. With
billing **"Off"**, or "Use opencode" off, it never appears, key or not
([09](09-agents.md#opencode)).

**Q. If I unshare it, does it disappear from their side too?**
No. The recipient can save what was displayed, so **unsharing only ends further access**. A
copy they already saved cannot be recalled. Before sharing, check what the conversation
exposes (secrets in it are not detected for you). See
[12](12-collaboration.md#sharing-a-conversation-shared-sessions).

**Q. Does "Models you don't use" stop the billing?**
It prevents picking one by accident; it is not a hard billing guard. The model disappears from
the launch dialog, the settings and the assistant's list, and launching it by name is refused,
but **it cannot stop the CLI's own commands, such as typing `/model` inside the terminal**
([09](09-agents.md)).

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
(if any) the message that was shown. The internals are covered in the developer documentation.

## Reporting a bug or requesting a feature

If the problem looks like it's in Agent Fleet itself, rather than your team's setup, file it as
a GitHub issue at <https://github.com/k-k1/agent-fleet/issues>. From a terminal session you can
also ask the agent you're using to do this for you.

**The repository is public.** Before pasting in logs, screenshots, or error text, strip out
secrets, internal hostnames/URLs, tenant or customer names, and anything else about your
organisation's environment that shouldn't be visible outside it. Describe what happened in
general terms instead.

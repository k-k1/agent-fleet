---
audience: "anyone whose screen is not doing what they expected"
---

# 21. Troubleshooting — fixes by symptom and FAQ

English | [日本語](21-troubleshooting.ja.md)

Start with the "by symptom" index: it is grouped by area, and each group opens with a table from what
you see to its entry. If nothing matches, check the FAQ at the end. Each entry links to the relevant
chapter of the main guide.

| The problem is with … | Group |
|---|---|
| The power button, a start that stopped, the disk, installed tools | [Workspace: start, stop and disk](#workspace-start-stop-and-disk) |
| A missing or non-resuming session, sharing, cleanup, schedules, notifications | [Sessions: resuming, sharing, tidying](#sessions-resuming-sharing-tidying) |
| Connecting an agent, an expired login, the models offered | [Agents: sign-in and models](#agents-sign-in-and-models) |
| The assistant chat and the fleet operator | [Assistants](#assistants) |
| Cloning, submodules, private-repository sign-in | [Repositories and git](#repositories-and-git) |
| The browser pane, the lightweight preview, preview subdomains | [Preview and browser](#preview-and-browser) |
| MCP servers, cloud-account commands, Jira, apps that connect from outside | [Integrations: MCP, cloud accounts, Jira, outside apps](#integrations-mcp-cloud-accounts-jira-outside-apps) |
| Keys, copy and paste, the theme, the phone layout | [Terminal and phone](#terminal-and-phone) |

## Workspace: start, stop and disk

The workspace itself: its power button, its start, its disk and the tools installed into it.

| You see | Entry |
|---|---|
| **Restart needed** badge by the power button | [details](#a-restart-needed-badge-sits-next-to-the-power-button) |
| A start turned into "stopped" by itself | [details](#the-workspace-went-from-starting-to-stopped-on-its-own) |
| The workspace has stopped without me noticing | [details](#the-workspace-has-stopped-without-me-noticing) |
| "Your home disk is nearly full" | [details](#your-home-disk-is-nearly-full) |
| A session says the agent's CLI could not be installed | [details](#starting-a-session-says-the-agents-cli-could-not-be-installed) |

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

### The workspace has stopped without me noticing

It **stops automatically** after a while with no activity (idle auto-stop). Press "Start" in the workspace action bar
once more, and your session list, clones, and connections all come back as they were ([01](01-first-day.md#at-the-end-of-the-day-is-it-ok-to-stop-the-workspace)).

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

## Sessions: resuming, sharing, tidying

A session that is missing, will not resume, will not reach another member or will not be tidied away.

| You see | Entry |
|---|---|
| Sessions or repositories vanished from the left pane | [details](#sessions-or-repositories-have-vanished-from-the-left-pane) |
| A session won't resume / is struck through | [details](#a-session-wont-resume-is-shown-struck-through) |
| Resume says the conversation log was deleted | [details](#resuming-says-the-conversation-log-was-deleted) |
| A session cannot be moved to another agent | [details](#a-session-cannot-be-moved-to-another-agent) |
| Cleanup won't remove something | [details](#cleanup-wont-remove-some-things) |
| The recipient of a share sees nothing, or old conversations | [details](#i-shared-it-but-the-recipient-sees-nothing-or-only-old-conversations) |
| Sent from a shared session, the agent does nothing | [details](#i-sent-something-from-a-session-shared-with-me-but-the-agent-does-nothing) |
| A scheduled run doesn't fire | [details](#a-scheduled-run-doesnt-fire) |
| No notifications arrive | [details](#no-notifications-arrive) |
| `/list-agents` unknown, or can't message another session | [details](#claude-doesnt-know-list-agents-or-says-it-cant-message-another-session) |

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
shows the last one it had. When the workspace could not be reached, the Console keeps retrying
and the list comes back by itself. When the workspace answered with an error instead (with no
list to show, the section says **"Couldn't load the repository list"**), nothing retries: reload
the page, or check your access to this workspace.

### A session won't resume / is shown struck through

If the state is **"Folder missing — can't resume"**, that session's **working folder is gone**
(typically after deleting the whole worktree). claude / codex / cursor / copilot / kiro / agy / opencode / lcpp / muse cannot resume from this state.
Start the same work over as a new session. shell falls back to home and resumes if the
working folder is missing ([05](05-stopping-and-tidying.md#when-you-can-and-cant-resume)).

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
(see [05](05-stopping-and-tidying.md#a-claude-session-whose-conversation-log-was-deleted)).

### A session cannot be moved to another agent

The move refuses while the session is working, or while a question, plan or approval waits for
you: let the turn end or stop it, and answer first. A stopped Managed Cursor session cannot be
moved, because its conversation is gone. shell and ssm sessions have no conversation to move. See
[05](05-stopping-and-tidying.md#moving-a-session-to-another-agent-move).

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
approves it**. The owner's Shared sessions section shows **"N pending approval(s)"**. Ask them
to review it and "Approve and send". With **view only** there is no composer in the first
place.

If the outcome of a send could not be confirmed, nothing is re-sent automatically, on purpose.
Use **"Check result"** to find out where it stands
([12](12-collaboration.md#sharing-a-conversation-shared-sessions)).

### A scheduled run doesn't fire

Check its row in the **Schedules** section of the left pane.

- A **"Paused"** tag means it is suspended; **"Resume"** in the row menu brings it back.
- `skipped_*` entries in the **run history** mean the previous run was still going (the
  overlap policy) or the target conversation was busy.
- The firing time, timezone and prompt are visible and editable under **"Details / edit"** in
  the row menu (only the advanced fields, session mode, reuse and so on, are changed from
  the operator chat).
- **"Run now"** exercises the same path as a timed firing (allow up to about a minute).
- If there is no Schedules section at all, scheduled execution is disabled on this deployment
  ([11](11-many-sessions.md#scheduled-runs)).

### No notifications arrive

They won't appear if the browser's notification permission is off (you're asked to allow it the first time). Also, notifications are
**suppressed** while you have that session's screen open. shell / ssm are outside the scope of state notifications
([03](03-starting-sessions.md#reading-state-badges-and-notifications)).

### claude doesn't know `/list-agents`, or says it can't message another session

Claude Code's own cross-session messaging is **deliberately disabled** in Agent Fleet, because
enabling it also switches Claude's usage telemetry back on. Agent Fleet has its own version of
the same thing: turn on **Settings > Agents > Session > "Messages between sessions"** (off by
default; it applies to sessions started from then on). It reaches other agent kinds as well as
claude, and reaches stopped sessions too. The full comparison is in
[11 Running many sessions](11-many-sessions.md#how-this-differs-from-claude-codes-own-version).

If the agent still seems to lack the tools after you turn it on, check whether that session was
already running beforehand: a running session keeps its current tools until it restarts.

## Agents: sign-in and models

Connecting an agent, its login running out, and the models it offers.

| You see | Entry |
|---|---|
| claude shows a login screen ("Select login method") | [details](#selecting-claude-shows-a-login-screen-select-login-method) |
| Approving codex gets nowhere | [details](#i-want-to-connect-codex-but-approving-doesnt-get-me-anywhere) |
| An agent stopped with "your login has expired" | [details](#an-agent-stopped-with-your-login-has-expired) |
| A Claude session shows "Login expired", sending starts nothing | [details](#a-claude-session-shows-login-expired-sign-in-again-or-sending-starts-nothing) |
| A model is missing, or launching with it is refused | [details](#a-model-i-want-isnt-offered-or-launching-with-it-is-refused) |
| The launch dialog offers only "Default" | [details](#the-launch-dialog-offers-only-default) |
| opencode says "working" but nothing arrives | [details](#an-opencode-session-says-working-but-nothing-arrives) |

### Selecting claude shows a login screen ("Select login method")

In most cases the connection itself is still alive. This isn't an authentication problem:
the interactive screen is just redoing its onboarding, and it usually clears if you
**resume** the session or use **"Recreate (current conversation goes to the archive)"**
from the ⋯ menu. The traditional manual `/login` inside the terminal works alongside this too.
You can check the connection status in ⚙ Settings → the "Agents" tab ([09](09-agents.md#claude)).

### I want to connect codex, but approving doesn't get me anywhere

To connect with a ChatGPT subscription, you must first **turn on "Enable device code
authentication for Codex" under ChatGPT's "Settings > Security"**. If it's off, approving
won't get you any further ([09](09-agents.md#codex)).

### An agent stopped with "your login has expired"

The error in the chat view offers **"Re-authenticate"**, which opens ⚙ Settings → Agents
scrolled to that agent's card. Connect again there and **that session resumes exactly where it
left off**. The agent's card in ⚙ Settings → Agents also
shows the connection state ([09](09-agents.md#supported-agents-and-how-to-choose)).

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

The launch dialog warns about the expired login too, but still lets you launch (you can run
`/login` in the pane). A session launched with a first prompt while the login is expired does not
receive that prompt: a toast with **Re-authenticate** says the first prompt was not sent, once.
Re-authenticate, then send the task again.

The same card warns ahead of time with **"Expires in N day(s)"** (within three days). The CLI's
own warning only appears with a day left and disappears after 15 seconds, so this card is the
place where you can still notice it before it bites.

### A model I want isn't offered, or launching with it is refused

It may be excluded under **"Models to exclude"** in ⚙ Settings → Agents. An excluded model
disappears from the launch dialog, the default-model setting and the list an assistant picks
from, and **launching with that name is refused as well** (including a schedule's model field
and launches via an assistant). Remove the exclusion on that agent's card
([09](09-agents.md#claude)).

To use a specific Claude release (a full id such as `claude-opus-4-8`), register it under
**"Additional Claude models"** on the same screen and it joins the choices.

### The launch dialog offers only "Default"

The agents with a live catalog (codex / opencode / copilot / cursor / kiro / agy / lcpp / muse) fetch their
model list when the dialog opens, so **"Loading models…" for a moment is normal**. Wait for
it before concluding anything. What the picker says once it settles is the answer:

| It says | What to do |
|---|---|
| **Every model is excluded in settings** | ⚙ Settings → Agents → that card → "Models to exclude" |
| **The current billing choice leaves no model to pick** | ⚙ Settings → Agents → opencode → "opencode.ai billing" ([09](09-agents.md#opencode)) |
| **Only the default model is available (check this agent's connection and plan)** | The agent answered with an empty catalog. Check the sign-in and the plan on that card. On a Copilot Free plan an empty list is normal: that plan offers Auto alone. |
| **Couldn't fetch the model list — the workspace may have only just started** | The request never landed. The Agent isn't listening yet in the first seconds after a start: **wait half a minute and reopen the dialog** (each open tries again). |

### An opencode session says "working" but nothing arrives

Look for the band above the conversation: **"The provider returned an error and the agent is
retrying (attempt N): …"**. opencode is retrying a provider error; the message says which. Rate
limits and overloads usually pass, so wait or stop the turn. Errors that cannot pass (region or
privacy setting, quota or billing, a bad key) end the turn as failed by themselves and the band
says so: fix the cause on the provider account (for "requires Global regions", its privacy /
region setting) and send again ([09](09-agents.md#when-the-provider-returns-an-error)).

The band appears in **Managed** sessions. A **Terminal (CLI)** session has no band; opencode's
own retry output is in the terminal.

## Assistants

The assistant chat and the fleet operator.

| You see | Entry |
|---|---|
| The assistant stopped carrying things forward on its own | [details](#the-assistant-operator-stopped-carrying-things-forward-on-its-own) |
| The chat says the context went over the limit | [details](#the-chat-says-the-context-went-over-the-limit-and-wont-answer) |

### The assistant (operator) stopped carrying things forward on its own

It hit the **automatic reply limit**. As a runaway guard, it can only run so many turns in a
row with no message from you (default 10, max 50), and **it resumes when you send the next
message**. The limit is in ⚙ Settings → Assistant (it cannot be unlimited).

If you only want normal completions to stay quiet, turn on **"Quiet completion reports"** in
the same tab: the card and the notification still arrive, but no automatic turn runs
([11](11-many-sessions.md#session-reports-and-auto-reply)).

### The chat says the context went over the limit and won't answer

The conversation grew too long. Either **"Compact"** at the right of the context bar (carry a
summary forward and continue) or open a **new chat** and hand over just the essentials.
Starting an exchange above 90% auto-compacts first (can be turned off in ⚙ Settings →
Assistant), but exceeding the limit before that point produces this state. Lowering the
**automatic compaction threshold** in ⚙ Settings → Assistant makes it less likely to recur
([10](10-chat-memo.md#when-a-conversation-gets-long-a-context-rule-of-thumb)).

## Repositories and git

Cloning, submodules and authentication on a private repository.

| You see | Entry |
|---|---|
| Cloning fails | [details](#cloning-fails) |
| Submodules missing or broken | [details](#a-session-says-the-submodules-are-missing-broken) |
| Authentication error on a private repository | [details](#authentication-errors-on-a-private-repository) |

### Cloning fails

- Private repositories need a GitHub / Bitbucket **connection** first (⚙ Settings → "Git hosting" tab; [06](06-code.md#connect-a-git-provider)). Check whether it says "Not connected".
- Double-check the spelling of the URL and branch name.
- Submodules are fetched best-effort after the clone; even if they fail, the parent clone itself succeeds ([06](06-code.md#submodules-and-git-lfs)).

### A session says the submodules are missing / broken

- A large submodule may not finish fetching inside the launch. The fetch keeps going in the
  background, and the notification center tells you when it lands ([06](06-code.md#submodules-and-git-lfs)).
- A submodule left half-fetched is repaired the next time a session launches in that working
  copy. To fix it right away, run `git submodule update --init --recursive` there in a terminal.

### Authentication errors on a private repository

In ⚙ Settings → the "Git hosting" tab, check that the provider in question is **"Connected"**.
If it isn't, connect via OAuth or a token. Once connected, authentication is transparent, so
from then on you can clone / push without entering tokens ([06](06-code.md#connect-a-git-provider)).

## Preview and browser

The browser pane, the lightweight preview and preview subdomains. The explanations are in [chapter 14](14-preview-browser.md); these entries are the quick diagnosis.

| You see | Entry |
|---|---|
| Browser pane `target-unreachable`, blank page, 404 or 502 | [details](#the-browser-pane-shows-target-unreachable-goes-blank-or-404) |
| The preview URL I gave a colleague returns 404 | [details](#the-preview-url-i-gave-a-colleague-returns-404) |
| HMR doesn't work, or the shell renders but no data | [details](#hmr-live-reload-doesnt-work-no-automatic-refresh-the-shell-renders-but-no-data) |
| Every API call in the preview returns 401 | [details](#every-api-call-in-the-preview-returns-401-preview-requires-sign-in) |
| Browser pane `crashed` / `disconnected`, or keeps dying | [details](#the-browser-pane-shows-crashed-disconnected-or-keeps-dying) |
| An Agent Fleet error page when signing in on a preview URL | [details](#signing-in-on-a-preview-url-shows-an-agent-fleet-error-page) |
| A preview URL says the port changed, too many ports, or restart | [details](#a-preview-url-says-the-port-changed-too-many-ports-are-open-or-the-workspace-must-restart) |

### The browser pane shows `target-unreachable` / goes blank or 404

- **`target-unreachable`** means the browser started but the port and path do not answer yet; a dev
  server that is still starting up looks the same. Check the port number and path, then press
  **"Reload"**, and **"Reconnect"** if it stays. Listening on `127.0.0.1` is enough. The pane's
  other states are in [14](14-preview-browser.md#status-display-and-recovery).
- **A 502 page from the lightweight preview or a preview subdomain** says which failure it is:
  *nothing is listening on the port* (start the app, reload); *the port is listening but does not speak HTTP*
  (a database, SSH, gRPC over h2c: the preview carries HTTP and WebSocket only); *the port is serving HTTPS*
  (turn off the dev server's HTTPS to preview it); or *the workspace is not running or cannot be reached*.
  The page follows the browser's language (Japanese or English). The browser pane connects directly, so its
  failure stays `target-unreachable`.
- **A blank page / 404** usually means an app that loads assets by absolute path (`/static/...`) was opened in
  the **lightweight preview**, whose URL is a sub-path (`/preview/{port}/`). Open it on the preview subdomain
  if your deployment issues one, or switch to **"Open in pane"**; the fixes on the app's side are in
  [14](14-preview-browser.md#viewing-a-web-app-you-started-browser-pane-and-lightweight-preview).

### The preview URL I gave a colleague returns 404

- **A preview subdomain URL changes every time the workspace restarts**, so a pasted
  `https://<random>-3000.…` returns 404 after the next start. Hand out the link from the **"Share"** button
  in the preview popover: it survives restarts ([14](14-preview-browser.md#showing-it-to-someone-else-three-steps)).
- Nothing under **"Shared with you"**, or a 404: check that **"Show it to your tenant"** is on under
  Settings › Preview subdomains. Turning it off closes the preview **from their very next request**, even
  mid-session; that is deliberate.
- **"Stopped"** on their side means your workspace is not running, and **they cannot start it**. Start it and
  tell them.
- Removing the port from the exposed-ports list also produces a 404.

### HMR (live reload) doesn't work / no automatic refresh / the shell renders but no data

- **Next.js (15.2 and later, including 16.x) opened on a preview subdomain: only the layout shell renders and
  no row of API data appears** (not blank, no error), yet `127.0.0.1:<port>` shows it correctly. The dev
  server blocks cross-origin access to `/_next/*` by default, so HMR never connects and hydration never
  finishes; `⚠ Blocked cross-origin request to Next.js dev resource` in the dev server log confirms it. Add
  `allowedDevOrigins` with a wildcard, since the URL changes on every start ([14](14-preview-browser.md#what-the-app-has-to-get-right)
  has the exact form).
- **The lightweight preview and the preview subdomains both pass WebSocket and SSE through.** If HMR still does
  not connect, the dev server may be **embedding its own port into the client** (Vite does this). Then the app
  needs a setting such as `server.hmr.clientPort: 443`.
- To avoid adding configuration, or to isolate the cause, use **"Open in pane"** (browser pane): a browser
  inside the workspace opens `127.0.0.1` directly, so HMR, WebSocket and SSE behave exactly like plain
  localhost.

### Every API call in the preview returns 401 (`preview requires sign-in`)

- **Did you repoint the dev server's proxy at the preview URL?** Keep Vite's `server.proxy` `target` and
  Next.js's `rewrites()` `destination` as **`http://127.0.0.1:8080`**: the proxy is a server-side call made
  by the dev server inside the container and carries no browser login
  ([14](14-preview-browser.md#what-the-app-has-to-get-right)).
- **If the browser calls the 8080 URL directly**, signing in is per port: open `https://<random>-8080.…`
  **in a tab once** to sign in there too. If the page on 3000 calls 8080 directly, also turn on
  **"Allow calls between ports"** in the settings (off by default).

### The browser pane shows `crashed` / `disconnected`, or keeps dying

- **`disconnected`** means the channel (WebSocket) dropped, not necessarily a crash: check that the workspace
  is running and press **"Reconnect"**. **`crashed`** means the browser inside the workspace terminated
  abnormally; **"Reconnect"** reopens it ([14](14-preview-browser.md#status-display-and-recovery)).
- **If it keeps dying within a short time**, workspace memory pressure is the likely suspect. Clean up heavy
  builds, watchers and browser panes left open (at most **2** at a time). How to check memory is under
  [Machine](20-settings.md#machine) and in the FAQ entry "Builds die / freeze from running out of memory" below.

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

## Integrations: MCP, cloud accounts, Jira, outside apps

MCP servers, cloud-account commands, Jira and apps that connect from outside.

| You see | Entry |
|---|---|
| An MCP server I registered isn't available | [details](#an-mcp-server-i-registered-isnt-available-in-sessions-or-assistants) |
| An MCP server row says "Sign in again" or "More permission needed" | [details](#an-mcp-servers-row-says-sign-in-again-more-permission-needed-or-shows-an-unfinished-sign-in) |
| An outside app connects but shows no tools | [details](#an-outside-app-connects-but-shows-no-tools) |
| Jira: boards, sprints and epics "not available" | [details](#jira-says-boards-sprints-and-epics-are-not-available) |
| `jira issue create` timed out (exit status 124) | [details](#jira-issue-create-or-comment-add-timed-out-with-exit-status-124) |
| `jira` exited with status 3 | [details](#jira-exited-with-status-3-the-description-was-not-set) |
| AWS "Unable to locate credentials", or `af-aws-exec` / `af-gcloud-exec` stops | [details](#aws-commands-say-unable-to-locate-credentials-or-af-aws-exec-af-gcloud-exec-stops) |

### An MCP server I registered isn't available in sessions or assistants

Work through ⚙ Settings → MCP servers (used by sessions) in this order
([20](20-settings.md#mcp-servers-used-by-sessions)).

1. Is it **enabled**? Disabled keeps the definition but hands it to nobody.
2. Do the **targets** include "sessions" / "assistants"? With both cleared it goes nowhere.
3. Did you narrow **target agents**? Leaving it empty covers every agent.
4. **Sessions pick it up from the next session you start**: a running session doesn't change
   until it restarts.
5. Press **"Test connection"** and see whether a server name and tool count come back.
6. Does it say it is unused because the name collides with a tenant distribution? The tenant
   entry wins.
7. On an egress-restricted deployment the host may still be **awaiting approval** (the card
   shows the request flow).

**The connection test passes (or now fails, saying the server rejected the session id it had just
issued), but the session reports `Session not found`.** The server keeps MCP sessions in
memory (a stateful Streamable HTTP server) and runs as **several instances without session
affinity**: `initialize` lands on one instance, which issues the session id, and the next
request lands on another that has never heard of it. Agent Fleet can't fix the server. The
connection test now sends several session-bound requests and reports this case itself; an
older build could pass it by luck.

- Make the server **stateless** (for example a stateless Streamable HTTP mode), or run a
  **single instance**, or route every request carrying the same `Mcp-Session-Id` to the same
  instance (session-id-aware routing).
- A connection test over one reused connection can look fine while real clients, which open several parallel connections, fail: the balancer keeps a reused connection on one instance. That is why the test opens a **new connection for each session-bound check**.
- **Load-balancer cookie stickiness doesn't help**: Claude Code doesn't send cookies back on
  MCP requests, so the balancer never sees its sticky cookie again.
- If an unknown session gets **HTTP 404**, Claude Code re-initializes by itself. If it gets
  **HTTP 400**, it does not recover: **restart the session** once the server is fixed.
  (Measured on Claude Code 2.1.303 against a local repro server.)

### An MCP server's row says "Sign in again", "More permission needed" or shows an unfinished sign-in

These are the states of an **OAuth** server's row in ⚙ Settings → MCP servers (used by sessions)
([20](20-settings.md#mcp-servers-used-by-sessions)). An OAuth server is handed to sessions and assistants only while you are
signed in, and a session picks the change up from the next one you start.

| The row says | What it means | What to do |
|---|---|---|
| **Sign in again** | The saved sign-in can no longer be renewed. | Press **Sign in again**. Until you do, the server is not handed out, and a session that is already running gets an error from the relay telling it to sign in again in the Console. |
| **More permission needed** | The server answered that a tool needs a permission you did not grant, and the call failed with the server's own answer. | Press **Approve the extra permission**: it signs in again, asking for that permission on top of what you already granted. |
| An **unfinished sign-in** note | A sign-in was started and never came back, often because the server's authorization page refused this deployment as a client. | Press **Sign in** again; if the row offers **Retry with dynamic registration**, try that. If it keeps failing, ask the server's operator to trust this deployment's host (for another Agent Fleet deployment: its administrator adds the host to the trusted client hosts). |

### An outside app connects but shows no tools

Open **Recent calls** under the app (⚙ Settings → Outside access → MCP tokens & apps → **Connected apps**) and check the HTTP status of
its requests and how many tools the `tools/list` request returned. Press **Refresh** if the list looks empty, and
reconnect from the app if needed. The details are in ([19](19-outside-access.md#recent-calls-when-an-app-connects-but-shows-no-tools)).

### Jira says boards, sprints and epics are "not available"

An OAuth connection to Jira that lacks the **Jira Software scopes** cannot use boards, sprints and epics. The
Jira card in ⚙ Settings → Issue tracker says **"Boards, sprints and epics: not available."** and what is missing;
`jira init` and the board, sprint and epic commands fail with an authorization error, and `jira` prints the same
explanation first. Your tenant administrator adds the scopes to the Atlassian app and ticks the Jira Software
option on the tenant's Jira card; then **reconnect Jira** once. Do not keep retrying `jira init` meanwhile.
Viewing, listing, creating and commenting on issues do not need them ([15](15-external-services.md#use-jira-in-a-session)).

### `jira issue create` (or `comment add`) timed out with exit status 124

Status **124** means the wrapper cut the command off at its time limit (120 seconds by default for a command
that changes something, when it has no terminal). **The issue or comment may already exist**, so do not run it
again straight away. When it knows the project and the summary, `issue create` also searches for issues you
created in the last 15 minutes with that summary and prints "possible matches" or "no matching issue found";
the search can fail ("could not look for the issue"), so do not rely on it. Check Jira first, for example with the JQL
`project = PROJ AND creator = currentUser() AND created >= -15m`, and create it again only if it is not there.
Commands that only read are re-run by themselves after a timeout or a connection reset
([15](15-external-services.md#use-jira-in-a-session)).

### `jira` exited with status 3: the description was not set

Status **3** from `jira issue create` or `jira issue edit -b` means the only part that failed is the description,
and `jira` printed which issue it concerns. After a `create` **the issue exists**, so do not create it again. After
an `edit` the other changes you asked for were applied. Either way, set the description with
`jira issue edit PROJ-1 --no-input -b "…"` ([15](15-external-services.md#use-jira-in-a-session)).

### AWS commands say "Unable to locate credentials", or `af-aws-exec` / `af-gcloud-exec` stops

- **"Unable to locate credentials"** in a workspace means the command found no credentials of
  yours: the workspace's own role is withheld from your shell. Do not configure credentials. Run
  the command as you with the profile from ⚙ Settings:
  `af-aws-exec --profile <name> --account <id> -- <command>`. A plain `aws --profile` is only for
  the read-only lookups that chapter 16 allows after its isolation check
  ([16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools)).
- **`af-gcloud-exec` exit code 3** means a login only when the wrapper says so ("the login was
  requested in the Agent Fleet Console …", "Google Cloud login required …"). Log in from the toast
  or ⚙ Settings, or run the terminal command it printed, then run the command again. Otherwise the
  status is the command's own: read that command's error
  ([16](16-cloud-accounts.md#when-it-stops)).

## Terminal and phone

Keys, copy and paste, the theme and the phone layout.

| You see | Entry |
|---|---|
| Ctrl+C doesn't work, can't copy and paste in the terminal | [details](#ctrlc-doesnt-work-in-the-terminal-i-cant-copy-paste) |
| App shortcuts such as the command palette don't work | [details](#app-shortcuts-such-as-the-command-palette-dont-work) |
| On a phone the keyboard pops up / hard to operate | [details](#on-a-phone-the-keyboard-pops-up-on-its-own-its-hard-to-operate) |
| Only the terminal stays dark on the light theme | [details](#only-the-terminal-stays-dark-on-the-light-theme) |

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

### On a phone, the keyboard pops up on its own / it's hard to operate

The **control key row** under the terminal (`Esc` `Tab` arrows `^C` `⏎`) sends keys without
bringing up the soft keyboard. You can scroll back through past output with a **one-finger vertical swipe**. Toggle the left pane
with the **≡ (menu)** at the top left of the screen ([02](02-console-basics.md#using-it-on-a-phone)).

### Only the terminal stays dark on the light theme

This is known behavior. Even after switching themes, the terminal (the black screen) background stays dark. The file
viewer and other screens follow the theme ([20 Display](20-settings.md#display)).

## FAQ

**Q. Can I get a deleted session back?**
If you only hid it with "Archive", you can bring it back with "Restore" in the archived list. With "Stop
(resumable later)" it remains as stopped and can be resumed with a click. "Delete" for shell / ssm
cannot be undone (the conversation log files themselves remain). For the differences, see
[05](05-stopping-and-tidying.md#stopping-and-tidying-up-sessions).

**Q. Can I use it from both my work PC and my home PC?**
Yes. Your login and connections, as well as display settings like font / text size, are stored on the server
and follow you on other devices and browsers ([20](20-settings.md#opening-it-and-how-it-is-organised)).

**Q. If I stop the workspace, is my work lost?**
No. Clones, uncommitted changes, the session list, and connections all remain. However,
uncommitted changes exist only inside the workspace, so commit / push for long-term safekeeping
([01](01-first-day.md#at-the-end-of-the-day-is-it-ok-to-stop-the-workspace) · [06](06-code.md#push-and-authentication)).

**Q. Is it OK to run several tasks at the same time?**
Yes. If each session works in its own independent worktree, edits won't collide. Worktrees are
created by default ([03](03-starting-sessions.md#worktrees-and-new-branches-the-key-to-parallel-work) · [06](06-code.md#launch-a-session-from-a-repository-row)).

**Q. What's the difference between "Stop", "Delete", "Archive", and "Recreate"?**
Stop = just pause (resumable), Delete = remove from the list (throwaway kinds), Archive = hide (restorable),
Recreate = a new conversation in the same place (the current conversation goes to the archive). Details in [05](05-stopping-and-tidying.md#stopping-and-tidying-up-sessions).

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

**Q. Does "Models to exclude" stop the billing?**
It prevents picking one by accident; it is not a hard billing guard. The model disappears from
the launch dialog, the settings and the assistant's list, and launching it by name is refused,
but **it cannot stop the CLI's own commands, such as typing `/model` inside the terminal**
([09](09-agents.md#claude)).

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

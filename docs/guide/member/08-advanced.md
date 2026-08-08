# 08. Going further — browser pane / lightweight preview, external integrations, other hosts, environment settings

English | [日本語](08-advanced.ja.md)

> Audience: members who are comfortable with the basics and want to go one step further. This chapter covers
> checking services running inside your workspace (browser pane / lightweight preview), remote control from your
> local Claude (MCP), logging in to other in-house hosts (SSM), and environment settings plus recreating the
> workspace. It's fine to read only the parts you need, when you need them.

## Viewing a web app you started — browser pane and lightweight preview

You can check web services started inside the workspace (dev servers of all kinds, Spring Boot, any web app)
on the spot, **with no extra port publishing and no container rebuild**. There are two methods.

Enter a port number in the **port input field** on the right of the workspace action bar, and you can choose
**"Open in pane"** (browser pane) or **"Lightweight preview"** (both only while the workspace is running).

- **Browser pane ("Open in pane")** — a browser inside the workspace opens `127.0.0.1:{port}` directly, and
  **only its rendering and input** are mirrored into a Console pane. You can click, scroll, type ASCII/Japanese,
  go back/forward, reload, and navigate paths, and **HMR (hot reload), WebSocket, SSE, cookies, redirects, and
  absolute-path assets** all work just like ordinary localhost. Use this when you want to touch the screen and
  verify it.
- **Lightweight preview** — a simple HTTP proxy that opens the port in a **new tab**, for quick checks. It suits
  **one-time looks** at a JSON health endpoint or a simple static page. **HMR / WebSocket / SSE do not work**,
  and rendering is not guaranteed for apps that depend on the root path, absolute assets, redirects, or cookie
  paths.

**When in doubt, use "Open in pane".** The lightweight preview is a resource-saving exception for when you can
settle for "just looking at an HTTP response once".

On a touch screen such as a tablet, **swipe to scroll** (a flick keeps coasting after you lift your finger),
**tap to click**, **press and hold to drag** (text selection, sliders), and **pinch with two fingers to zoom**.
A pinch re-lays the page out at a narrower width rather than stretching the picture, so text stays legible at
its own size, and pinching back returns you to the original view. **Double-tap** jumps between the fit-to-width
view and life size.

A tap does **not** raise the keyboard — it would appear every time you pressed a link or a button. To type
into a field on the page, tap the field and then open the keyboard with the **keyboard button** at the bottom
left of the pane; it stays open while you keep tapping the page.

### How to use it

1. Have a shell or an agent start the dev server. Make the server listen on `127.0.0.1` inside the workspace
   (or on all interfaces).
2. On the **desktop / tablet** workspace action bar, enter a port number from `1..65535` (excluding `7700`,
   which the Agent itself uses) in the port field, plus a path starting with `/` if needed (no host or external
   URL).
3. Normally choose **"Open in pane"**; if all you need is a single HTTP check, choose **"Lightweight preview"**.

For example, once you start an API server on 8080 in a shell, enter `8080` in the port field and press
"Open in pane", and the app appears in a pane. There is no need to ask IT to open extra ports.

### Examples by setup

| Setup | Example input | Which to open with |
|------|--------|----------------|
| **Node / Vite** | `5173` + `/` | Uses HMR (WebSocket), so **browser pane**. |
| **Spring Boot** | `8080` + `/` or `/actuator/health` | Screens involving redirects, absolute `/assets/*`, and cookies: **browser pane**. Just a one-time look at the health JSON: lightweight preview. |
| **API only** | `8080` + `/api/health` | One-time JSON / status checks: **lightweight preview**. SSE, auth cookies, redirects, and interactive checks: **browser pane**. |
| **Frontend + API (multiple ports)** | frontend `5173` / API `8080` | Open the frontend's `5173` in a **browser pane**. From there, fetch / WebSocket / SSE to another port (`8080`) works (as in a normal browser, CORS configuration is required). If you also want to see the API, open `8080` in a second pane. |

> **Spring Boot links / redirects** — to have them resolve correctly, set
> `server.forward-headers-strategy=framework` (or `native`) on the app side.

### Status display and recovery

When the browser pane fails to render properly, a status appears in the pane.

| Status | Meaning | What to do |
|------|------|------|
| `target-unreachable` | The browser started, but the connection to that port/path hasn't been established yet. **Waiting for the dev server to start** is also this state. | Check the port number, the path, and whether the server is listening; once it's up, press **"Reload"**. If it persists, press **"Reconnect"**. |
| `disconnected` | Communication with the pane (WebSocket) was lost. This is not necessarily a browser crash. | Check that the workspace is running and connectivity is back, then press **"Reconnect"**. |
| `crashed` | The browser inside the workspace terminated abnormally and cannot continue that display. | Reopen with **"Reconnect"**. If it keeps happening, check the workspace's memory usage and the target app ([09](09-troubleshooting.md)). |

If you try to open it while the workspace is stopped or starting, a dedicated notice appears. Reopen once the
workspace is running.

### Limits and transience (good to know)

- You can have at most **2 open per workspace**, the display size is at most **1600×1200**, and rendering is at
  most **12fps**. It is not suited to video playback or high-frame-rate checks.
- If you **switch the pane to another view / send it to the back**, rendering stops and the Page is kept for
  about **60 seconds**. Return quickly and the same state continues; after a while it is recreated from the
  saved port/path.
- On a **workspace Stop → Start** or a Console reload, browser panes that were on display are **automatically
  recreated** from the same port/path, but cookies and half-typed input do not come back.
- This is **not a general-purpose browser for opening external URLs** (it is localhost-only; there is no host
  field, only a port and path). Nor is it a **full replacement for browser devtools** with DOM / Network /
  Sources. The pane's "Console" lets you view and copy that page's `error` / `warn` logs and the like
  (up to 200 entries; not stored persistently).

> **Smartphones are not supported in the current version.** At around 390px width (phones), the entry point in
> the action bar overflows off screen and you cannot start. Please use a **desktop or tablet**.

## Driving your workspace from an external Claude (MCP)

From Claude Code / Claude Desktop on your local PC you can **remotely drive** sessions in your workspace.
Think "from my own Claude while I'm out, check on the session running in the company workspace and send it the
next instruction". Issue the token for this in **⚙ Settings → the "MCP tokens" tab**.

1. Choose a **name** (e.g. `laptop-claude`), a **scope**, and an **expiry**, then press **"Issue token"**.
   - Scopes are **read (view only)** / **write (drive sessions; the default)** / **admin:dangerous (elevated / admin)**. You cannot pick a scope beyond your own permissions. If all you want is to drive sessions remotely, write is enough.
   - Expiry is 90 days (default) / 30 days / 365 days / no expiry.
2. On issue you'll see **"Token issued (you can't see it again once you close this)."** — **the token is shown
   only this once**. Save it with "Copy token".
3. The same screen also shows a **`.mcp.json`** template for your local Claude Code. Copy it with
   "Copy .mcp.json" and save it at the project root (or add `agent-fleet` to an existing file). The endpoint is
   `/mcp`, with `Authorization: Bearer <token>` in the header.

Tokens you no longer need can be **revoked** ("Revoke") on the same screen (once revoked, connections using
that token are rejected from the next attempt).

## Connecting Discord / Slack (chat bridge)

Connect your own Discord / Slack bot from ⚙ Settings → the **"Chat"** tab in the Connections group, and session
progress reaches your chat even while you're away from your desk — and you can steer sessions right from your
replies.

- **Connecting** — Discord takes **a single Bot token** (the card's wizard walks you through validation →
  inviting it to your server → picking a channel, and a test notification arrives on connect). Slack takes two:
  a Bot token (`xoxb-…`) and, if you want two-way operation, an App-level token (`xapp-…`).
  You can also connect both at the same time.
- **What arrives** — a thread is created per session, and you receive "Answer ready", "Questions & plan
  approvals", "Permission requests", "Abnormal exits", and "Session reports" (each type has its own toggle).
  With the opt-in **full-text mode**, the response body itself is delivered (secrets such as tokens are
  automatically redacted).
- **Driving from chat** — turn on **"Reply to steer"** (opt-in) and replies in the thread become input to that
  session as-is. Questions can be answered with choice buttons, plan approvals with "Approve / Reject" buttons,
  and permission requests with "Allow / Deny" buttons (button coverage varies by agent kind).
- **Fleet operator** — write in the standing thread "🛰 Fleet Operator" to talk with the
  [11 fleet operator](11-fleet-operator.md) from chat (the same conversation as the operator on the Console
  side). Destructive operations initiated from chat (deletion etc.) pause for an "Approve / Reject" button
  before executing.
- **Just want to silence notifications** — under Personal → the "Notifications" tab, **Service notifications**
  lets you turn off delivery without disconnecting.

## Logging in to another in-house host (SSM)

You can log in to EC2 instances in your company's AWS via AWS SSM Session Manager. Configuration lives in
**⚙ Settings → the "AWS SSM" tab**, split into **two layers**.

- **Profile (shared settings)** — the access portal (IAM Identity Center) and account/role. A bundle of SSO settings reused across multiple hosts. Create one of these first.
- **SSM host (individual)** — an alias for the login target → instance ID. For authentication you just pick a profile.

**No AWS secrets are stored in Agent Fleet.** Login happens at session start via the device-code flow — you
approve the **`aws sso login`** URL shown in the terminal in your browser — and short-lived credentials are held
only inside the workspace.

Once registered, connect from the workspace action bar via **"Start" → "SSM — log in to another host"**,
choosing the **target host**.
If authentication is needed, the `aws sso login` URL appears on a confirmation screen; approve it in another tab
(never enter a code / URL you don't recognize).

## Environment settings and recreating the workspace

In **⚙ Settings → the "Toolchains" tab** you can adjust the workspace environment. Changes **apply to sessions /
shells started afterwards** (running ones and existing processes pick them up after you stop and then start the
workspace again).

- **Time zone (TZ)** — the default is Japan time. Applying a change requires stopping and starting the workspace.
- **Node.js / Java (JAVA_HOME)** — pick the versions to use.
- **Agent CLI updates** — "Update the agent CLIs and rtk to the latest on start" (covers claude / opencode / codex / cursor / GitHub Copilot / Antigravity (agy) / rtk). Default is OFF (pinned to the versions baked into the image). Kiro is not part of this toggle — its version is fixed by the image rebuild / on-demand install and its own auto-update is kept off.

### Recreating the workspace (danger zone)

In **⚙ Settings → the "Danger zone" tab** is **"Recreate the workspace"**. It discards the
container and rebuilds it from the latest image; pressing **"Recreate"** shows a confirmation. What stays and
what goes is as follows.

- **What is lost** — running sessions, and **cloned repositories (`~/repos`, including uncommitted changes)**.
  `~/repos` is the **only** thing deleted.
- **What stays** — everything else in your home (`~`) remains. Logins and connections (GitHub / Bitbucket /
  Claude etc.), `~/.local` (claude / node etc.), and your settings and caches are preserved, because the home
  volume is reattached to the recreated container.

In short: "**only `~/repos` is deleted, and the container is rebuilt from the latest image. The rest of home
(logins, connections, `~/.local`, etc.) stays**". Use it when you want to pick up an image update or the
environment is broken. **Uncommitted changes are lost**, so push / commit before running it ([04](04-git.md)).

### Cleaning home (an even deeper reset)

Since recreating deletes only `~/repos`, it won't fix problems on the home side (a broken claude install in
`~/.local`, corrupted caches or config files, and so on). In that case use **"Clean home"**, in the same
"Danger zone" tab. It deletes **your entire home except logins and connections** (`~/repos`, `~/.local`, caches,
settings) and rebuilds from the latest image — a deeper reset.

- **What stays** — logins and connections (GitHub / Bitbucket / Claude) **only**.
- **What is lost** — running sessions, cloned repositories, and **everything else in home**, including
  `~/.local`, caches, and settings.

Try "Recreate" first to see if it fixes things, and use "Clean" only when that doesn't.
Both operations **lose uncommitted changes**.

---

For those who want the internals: browser pane and lightweight preview, MCP, and SSM are in [dev/08 External integrations](../../dev/08-integrations.md); recreate behavior is in [dev/04 Workspace Agent](../../dev/04-workspace-agent.md)

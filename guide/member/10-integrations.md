---
audience: "anyone connecting the workspace to something outside it"
updated: "2026-08"
---

# 10. Going further — browser pane / lightweight preview, external integrations, other hosts, environment settings

English | [日本語](10-integrations.ja.md)

It's fine to read only the parts you need, when you need them.

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
- **Lightweight preview** — opens the port in a **new tab**. **WebSocket and SSE do pass through** (HMR works
  too, depending on the app's own configuration), but the URL is a **sub-path** (`/preview/{port}/`), so an app
  that emits **absolute paths** like `/static/...`, or a screen that depends on the root path or a cookie path,
  will break.
- **Preview subdomains** — on some deployments a URL such as
  `https://xxxxxxxx-3000.<preview domain>/` is **issued automatically every time the workspace starts**. The app
  is served **at the root**, so the sub-path problem above cannot happen, and **several ports** (3000 and 8080,
  say) are open at the same time. See "Preview subdomains" below.

**When in doubt:** if a preview URL has been issued, that is the most straightforward option. Otherwise use
"Open in pane" when you want to touch the screen, and the lightweight preview when one look at an HTTP response
is enough.

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

### Preview subdomains (only where they are issued)

A URL containing a random label is issued **every time the workspace starts**.

```
https://k7f2q9x1w3ub5nzt0abc-3000.pv.example.com/   → port 3000 (e.g. React / Next.js)
https://k7f2q9x1w3ub5nzt0abc-8080.pv.example.com/   → port 8080 (e.g. Spring Boot)
```

- **Where to find them** — open "Preview" on the workspace action bar; they are listed per port under
  **Preview URLs (this start)**. Click to open in a new tab, or use "Copy" to take the URL.
- **They change on every start**, and stop working when the workspace stops (the old URL returns 404). Assume
  any document you paste one into goes stale quickly.
- **Signing in is required by default.** The first visit bounces through the Console login once and comes back.
- **You choose which ports are exposed.** The default is `3000, 8080`; change it under Settings ›
  **Preview subdomains**. A port that is not listed has no URL — the list is what keeps an admin console you
  did not mean to expose off the internet.

#### What the app has to get right

- **Do not hard-code `http://localhost:8080` as the API origin.** From the browser's point of view `localhost`
  is **the machine of the person looking at the screen**. Route `/api` to 8080 through the dev server instead
  (Vite's `server.proxy`, Next.js's `rewrites()`), and the same configuration works both on your own PC and in
  the preview.
- ⚠️ **Leave the proxy's target (`destination` / `target`) as `http://127.0.0.1:8080`.** Rewriting it to the
  preview URL — the natural move if you assume the frontend "cannot see" 8080 — makes **every API call return
  401 (`preview requires sign-in`)**. The proxy is a **server-side** call made by the dev server inside the
  container, and it does not carry the login state your browser has.
- If the page on 3000 really must call 8080 **directly**, turn on **"Allow calls between ports"** in the
  settings (off by default).
- Set `server.forward-headers-strategy=framework` for Spring Boot. Next.js Server Actions are validated
  correctly over this path as well.
- ⚠️ **Next.js (15.2 and later, including 16.x) needs `allowedDevOrigins`.** The dev server **blocks
  cross-origin access to `/_next/*` by default**, so on a preview subdomain **only the layout shell renders and
  none of the data fetched from the API appears** — not blank, no error, so it looks like a bug in your own app.
  The giveaway is `⚠ Blocked cross-origin request to Next.js dev resource` in the dev server log.
  **The URL changes on every start, so use a wildcard.**

  ```ts
  // next.config.ts
  allowedDevOrigins: ["127.0.0.1", "*.pv.example.com"],  // the domain is in AF_PREVIEW_DOMAIN
  ```
- `AF_PREVIEW_URL_3000`, `AF_PREVIEW_URL_8080` and `AF_PREVIEW_DOMAIN` are present inside the container. Pass
  them to anything that reads its own public URL from the environment, such as `NEXTAUTH_URL`.

#### Showing it to someone else (three steps)

What you use depends on who you want to show it to. **Each step opens it wider.**

1. **Only you** — do nothing (the default).
2. **Colleagues in the same tenant** — turn on **"Show it to your tenant"** under
   Settings › Preview subdomains.
   - They open it **after signing in to the Console** (nobody outside the tenant can see it).
   - **This does NOT return to off when the workspace stops or restarts.** Turn it off yourself
     when you are done.
   - Hand them the link from the **"Share"** button. **That link keeps working across restarts** —
     a raw `https://xxxx-3000.…` URL starts returning 404 the next time your workspace starts.
   - In their Console it appears under **"Shared with you"** in the preview popover. **While your
     workspace is stopped it shows "Stopped" and cannot be opened** — they cannot start your
     workspace, so ask them to ping you if they need it running.
   - ⚠️ **Your workspace will not idle-stop while someone has it open, and that running time is
     billed to you.** (A page left open and untouched does eventually stop.)
3. **People outside the tenant** — **Open without signing in** lets anyone with the URL open it. It
   **always returns to off when the workspace stops or restarts** (and the URL changes).
- **The URL you currently have is shown under Settings › Preview subdomains, as
  "Current URL".** While the workspace is stopped it says none is issued — but the domain it will
  use is written just below.
- If a URL went to the wrong place, press **"Discard and mint a new one"**. Tabs that are open now start
  returning 404 immediately. ⚠️ **Pressing it while the workspace is stopped does nothing**, because
  there is no issued URL to discard (the next start gets a new one anyway).

### Examples by setup

| Setup | Example input | Which to open with |
|------|--------|----------------|
| **Node / Vite** | `5173` + `/` | Uses HMR (WebSocket), so **browser pane**. |
| **Spring Boot** | `8080` + `/` or `/actuator/health` | Screens involving redirects, absolute `/assets/*`, and cookies: **browser pane**. Just a one-time look at the health JSON: lightweight preview. |
| **API only** | `8080` + `/api/health` | One-time JSON / status checks: **lightweight preview**. SSE, auth cookies, redirects, and interactive checks: **browser pane**. |
| **Frontend + API (multiple ports)** | frontend `5173` / API `8080` | **Preview subdomains are the best fit if you have them** (each port gets its own URL). Otherwise open the frontend's `5173` in a **browser pane**; fetch / WebSocket / SSE to another port (`8080`) works from there (as in a normal browser, CORS configuration is required). |
| **React 3000 + Spring Boot 8080** | `3000` / `8080` | Preview subdomains serve both at the root. Routing the API through the dev server's proxy onto `/api` is the least trouble — the same configuration then works on your own PC too. |

> **Spring Boot links / redirects** — to have them resolve correctly, set
> `server.forward-headers-strategy=framework` (or `native`) on the app side.

### Status display and recovery

When the browser pane fails to render properly, a status appears in the pane.

| Status | Meaning | What to do |
|------|------|------|
| `target-unreachable` | The browser started, but the connection to that port/path hasn't been established yet. **Waiting for the dev server to start** is also this state. | Check the port number, the path, and whether the server is listening; once it's up, press **"Reload"**. If it persists, press **"Reconnect"**. |
| `disconnected` | Communication with the pane (WebSocket) was lost. This is not necessarily a browser crash. | Check that the workspace is running and connectivity is back, then press **"Reconnect"**. |
| `crashed` | The browser inside the workspace terminated abnormally and cannot continue that display. | Reopen with **"Reconnect"**. If it keeps happening, check the workspace's memory usage and the target app ([09](11-troubleshooting.md)). |

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

> **On a smartphone**, the action bar has no "Preview" button: tap **⋯** at its right end instead — the port
> and path fields and "Open in pane" are in the popover it opens.

## Operating a browser the agent opened

When an agent is driving its own browser (Chromium) inside the workspace and reaches something
**only a person can do** — signing in, a one-time code, ticking a consent box — it can hand that
page over to you. This is a different thing from the browser pane above: there you open your own
local web app, here you take over a page the agent already has open.

- A link appears in the agent's message: **"Open the browser and operate it (opens as a pane in this
  tab)"**. **You are the one who clicks it** — nothing opens until you do, and it opens as a pane in
  the tab you are already in, not in a new one.
- It starts in **View only**. The picture is live, but clicks, scrolling and keystrokes are not
  delivered. Once the agent hands control over it becomes **User control** and you can operate it.
  (**If it feels unresponsive, this is why** — the pane says so at the top.)
- What you are being asked to do is shown as a **Requested browser action**. **Action complete** or
  **Cancel action** tells the agent how it ended. Pressing them reports *what you did*, not that the
  site's own processing succeeded — which is also why an agent must not make the final send, buy or
  consent click for you.
- **Close view** only stops showing it. **The agent's browser, its page and its session stay open.**
- You can reopen an attachment from **Preview** in the workspace action bar, under **Attached
  browsers**. If the same Chromium has other tabs, **Switch to another tab** moves between them.

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
  [08 fleet operator](08-organising.md) from chat (the same conversation as the operator on the Console
  side). Destructive operations initiated from chat (deletion etc.) pause for an "Approve / Reject" button
  before executing.
- **Just want to silence notifications** — under Personal → the "Notifications" tab, **Service notifications**
  lets you turn off delivery without disconnecting.

## Logging in to another in-house host (SSM)

You can log in to EC2 instances in your company's AWS via AWS SSM Session Manager. Configuration lives in
**⚙ Settings → the "AWS profiles/SSM" tab**, split into **two layers**.

- **Profile (shared settings)** — the access portal (IAM Identity Center) and account/role. A bundle of SSO settings reused across multiple hosts. Create one of these first.
- **SSM host (individual)** — an alias for the login target → instance ID. For authentication you just pick a profile.

Each profile row has **Log in**, which signs you in to IAM Identity Center for that profile without leaving the
Console. It opens a login window; the sign-in code is created only when you press **Log in** there, and only that
window shows it. The login serves SSM sessions and `af-aws-exec` for that profile. The button is off for a profile
without both an account and a role, and for one whose name another label also maps to (see below).
Beside the label, a badge shows the login state: **Signed in**, **Renews on use** (the access token has expired;
while the portal session is open, the next use renews it) or **Not signed in**. It shows no time left: the
workspace knows only the access token's expiry (about an hour), not when the portal session ends.

**Before a login ends — only for a login that cannot renew.** A normal login from Settings renews itself on use
until the portal session ends, and that end is recorded nowhere the workspace can read, so such a login is **not
warned about in advance**: when the portal ends it, the next command asks for a login as below. Only when the cached
login has nothing to renew it with (no refresh token, or its sign-in client registration has expired) is its end
known; then the Console warns once per profile, about 10–15 minutes before: a toast "Your AWS login ends at …" (and
a notification), with **Log in** opening the same login window. The warning goes once you log in again; closing the
toast hides it for that end in that tab. A profile that is not in the managed block (see `af-aws-exec --list`) is
never warned about.

**Logging out of one profile.** A row that is signed in (or renews on use) has **Log out**, both here and in the
popover of the WS bar's AWS badge. After you confirm, the workspace ends that profile's login with AWS and deletes
its cached login and role credentials; your other profiles stay signed in. Credentials a running command already
received stay valid until they expire — AWS cannot recall them — so stop that command if it matters. If AWS cannot
be reached or refuses (it does when the access token has already expired, as for a **Renews on use** row), the
workspace is signed out all the same and the Console says so; the login may then stay valid at AWS
until it ends. Do not use `aws sso logout` for this: it signs out every profile at once, whatever `--profile` says.

Every profile and host row has **Edit**, which opens the same form filled in and saves it in place. Edit rather
than delete and re-add: a host refers to its profile by an internal ID, so a re-added profile is a new one.
A profile that hosts still use cannot be deleted — the page names those hosts; edit them to pick another
profile, or delete them, first. A host that was left without a profile before this rule (its row says so) is
fixed the same way: edit it and pick a profile.
A profile's workspace name comes from its label, and the login belongs to that name: changing the label, or the
start URL / SSO region, means logging in again: the form warns you, and the row shows **Log in again** until you log in from it. The workspace's `~/.aws/config` picks up
the change within 5 minutes, or at once when you press **Log in**; sessions already open keep the old settings.

**No AWS secrets are stored in Agent Fleet.** Login happens at session start via the device-code flow — you
approve the **`aws sso login`** URL shown in the terminal in your browser — and short-lived credentials are held
only inside the workspace.

Once registered, connect from the workspace action bar via **"Start" → "SSM — log in to another host"**,
choosing the **target host**.
If authentication is needed, the `aws sso login` URL appears on a confirmation screen; approve it in another tab
(never enter a code / URL you don't recognize).

### Using the profiles from the terminal, SDKs and build tools

Your profiles are also written into **`~/.aws/config`**, so `aws --profile <name>`, the AWS SDKs and build tools
(Gradle, Maven, CDK, Terraform, …) can select them by name without you copying anything. Prefer `--profile` (or a
tool's own profile setting) to `AWS_PROFILE=<name>`: keys already exported in the shell (`AWS_ACCESS_KEY_ID` and
friends) win over `AWS_PROFILE`, so it does not pin who a command runs as.

- **The name** is the profile's label with every character other than letters, digits and `._@-` replaced by
  `-` (label `prod app` → profile `prod-app`). `af-aws-exec --list` prints each name with its account, role and
  label — pick by those, not by the name alone.
- If two labels map to the same name (`prod app` and `prod-app`), **neither is exported** and `--list` says so:
  either one could be the wrong account. Rename one of them in Settings.
- The profiles sit in a **managed block** at the end of the file, between two `# agent-fleet` marker lines. Edit
  them in Settings, not inside the block — the block is rewritten, and so is anything `aws configure set` writes
  into it. Everything outside it is yours and is kept.
  Only profiles with both an **account and a role** are exported. With neither, `aws --profile <name>` would not
  use SSO at all and would quietly run as the workspace's own role; with only one it would fail. `--list` says
  which is missing.
  If a `[DEFAULT]` line in `~/.aws/config` would break a profile (a different region, a `role_arn` that would make
  every profile assume that role, …), that profile is not exported, and `af-aws-exec --list` names the line.
  If you already defined a profile with the same name yourself (in `~/.aws/config` or `~/.aws/credentials`),
  **your definition is used** and ours is left out. A profile labelled `default` is never exported: it would
  change what every command without a profile runs as.
- Changes arrive **within about five minutes**, at the next workspace start, or immediately when you run
  `af-aws-exec`.

**Logging in from a terminal.** The profile row's **Log in** (above) does this in the Console. In a terminal, plain
`aws sso login` opens a callback on `127.0.0.1` inside the workspace, which
your browser cannot reach. Use the device-code flow instead:

```sh
aws sso login --profile <name> --use-device-code --no-browser
```

Open the URL it prints and approve the code — only a code you started yourself just now. The login is shared with
SSM sessions of the same profile, so logging in once covers both.

**Running one command as you: `af-aws-exec`.** The workspace can have an AWS identity of its own (a *workload
role*), and the machine underneath can have one too. In a container workspace (docker or AWS ECS, Agent Fleet
0.26.0 or later) your sessions and terminals do not get either: the Agent keeps the workspace's credentials variables
out of everything it starts, and the SDKs' instance metadata lookup is switched off (`AWS_EC2_METADATA_DISABLED=true`).
A tool that ignores that variable is stopped only by the host's network block, which holds once your administrator has
finished the 0.26.0 migration ([operator guide 04](../operate/04-secure.md), "Other operational controls"). (A
workspace that runs directly on your own machine is left as it is: an instance role there is your machine's, and the
SDKs still find it.) So a
command that names no profile at all — a bare `aws …`, an SDK's default credential chain, a build tool with no
profile setting — fails with "Unable to locate credentials" (or its SDK's wording) instead of running as the
workspace. (A named profile that is misspelled or logged out fails with its own error.) Your administrator can let
the workspace use its own task role again (on AWS ECS); then such a command quietly runs as that role, in another
account. Either way, "Unable to locate credentials" means "name your profile", not "configure credentials": do not
answer it with `aws configure`, `aws login` (the CLI's own hint) or keys in a `[default]` section of `~/.aws`, and do not look for credentials elsewhere.
For deployments, writes and anything else whose account matters, pass your credentials explicitly:

```sh
af-aws-exec --profile <name> -- ./gradlew deploy
af-aws-exec --profile <name> -- npx cdk deploy
```

**Example: a build tool with an S3 upload plugin.** A Gradle deploy task built on an AWS plugin (an S3 upload
task, for instance) with no profile in the build script asks the SDK's default chain: environment variables, JVM
system properties, the `default` profile in `~/.aws`, then the container credentials and instance metadata. In a
session, `./gradlew uploadArtifact` therefore stops with "Unable to load AWS credentials from any provider in the
chain" rather than uploading the artifact as the workspace's role. Run it as you, naming the account:

```sh
af-aws-exec --profile <name> --account <id> --region <region> -- ./gradlew uploadArtifact
```

The plugin then finds the profile's short-lived credentials in its environment, the first place the chain looks,
and the "running as" line shows who uploads. If the build script names a profile itself (`profileName = "prod"`,
say), see "could not be found" below.

**A read-only lookup with `aws --profile`.** `af-aws-exec` stays the way to run deployments, writes, build tools,
SDK programs and scripts: it checks the account (`--account`), hands short-lived credentials to tools whose SDK cannot
read an SSO profile (older SDKs such as the AWS SDK for Java v1, common in Gradle/Maven plugins), asks you in the
Console when a login is missing, refuses a profile name that means different identities to different tools, pins the
region with `--region` and removes endpoint overrides. A quick look-up with the AWS CLI itself (`describe-*`,
`list-*`, `s3 ls`) may name one of your Settings profiles directly, but only in a shell where this check prints
`isolated`. It prints one word and nothing of the environment or of the error:

```sh
if err=$(env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true \
           aws sts get-caller-identity 2>&1 >/dev/null); then st=0; else st=$?; fi
err=${err#"${err%%[![:space:]]*}"}
if [ "$st" = 253 ] && [ "$(printenv AWS_EC2_METADATA_DISABLED)" = true ] && [ "${AF_WS_WORKLOAD_AWS:-}" != 1 ] \
   && [ -z "$(env | cut -d= -f1 | grep -E '^AWS_(CONTAINER_|CONFIG_FILE$|SHARED_CREDENTIALS_FILE$|ENDPOINT_URL)')" ] \
   && case $err in "Unable to locate credentials"* | \
        "aws: [ERROR]: An error occurred (NoCredentials): Unable to locate credentials"*) true ;; *) false ;; esac
then echo isolated; else echo not-isolated; fi; unset err st
```

`isolated` means `AWS_EC2_METADATA_DISABLED=true` is exported, so the CLI does not ask instance metadata, the environment holds no workload credentials and no AWS
config-file or endpoint overrides, and the CLI's default chain (with `AWS_PROFILE` set aside and configured endpoints
ignored) ended in its own "no credentials" error (exit 253), so a misspelled or logged-out profile fails instead of answering for another account. Anything else — your
own default credentials, an expired session, a failing `credential_process`, a network error — prints `not-isolated`, and so normally do a workspace
on your own machine, a deployment that hands the task role back and a workspace not started again since the upgrade.
The check reads what this shell would hand the CLI; it does not prove the runtime, the version or the host's network
block. Where it prints `not-isolated`, run lookups through `af-aws-exec` too. Where it prints `isolated`, a direct
lookup still needs all of these, or it goes through `af-aws-exec`:

- The profile is one `af-aws-exec --list` shows as exported, chosen there by its account and role (not by the name
  alone). If you do not know which account, find out first. When `--list` warns that its names come "from an earlier
  sync; not checked against Settings now", nothing is verified: use `af-aws-exec`. A profile `--list` does not show, marks as not exported,
  or one you defined yourself (`role_arn`, `credential_process`) → `af-aws-exec`.
- Name the region and ignore configured endpoints:
  `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true aws --profile <name> --region <region> ec2 describe-instances`, with no
  `--endpoint-url`. A stale `AWS_REGION` in your shell beats the profile's region, and an `endpoint_url` in your
  `~/.aws/config` (left by an emulator setup, say) would send the request somewhere other than AWS.
- Run it in the same shell you checked.
- An SSO token error means the login is missing; plain `aws --profile` does not ask the Console. Rerun with
  `af-aws-exec`, which does.
- A profile `af-aws-exec` refused is refused here too; plain `--profile` is no way around it.

Agents in the workspace follow the same rule.

- It passes the profile's **short-lived** credentials to that one command through its environment only —
  `af-aws-exec` itself writes them nowhere and prints nothing but the identity the command runs as. (The AWS CLI
  keeps its own login and role caches under `~/.aws`, as it always does.)
- The profile must have an **account and role** set in Settings. A profile that also carries `role_arn` (even
  empty) or a `web_identity_token_file` path is refused because the AWS CLI would then not use its SSO login. One that also carries
  `source_profile`, `credential_source`, `credential_process`, static keys or an empty `web_identity_token_file`
  (next to `role_arn` or not) (in `~/.aws/config` or
  `~/.aws/credentials`) is refused too: the AWS CLI would still use SSO, but other SDKs and tools use those first,
  so the one name would mean different identities to different tools. A tool that syncs credentials into
  `~/.aws/credentials` under the same name (yawsso, for example) causes this; sync to another name. Keys under a
  `[DEFAULT]` section count, since the CLI applies them to every profile, and so does an empty value. The credentials
  are obtained through the profile's SSO login alone — from a minimal config holding only its SSO settings, with
  endpoint overrides ignored — and then checked with AWS to be a session of that profile's permission-set role in
  that account. `--profile default` is refused: name the SSO profile.
- **A profile that is not SSO.** Some accounts are reached only through a profile of your own in `~/.aws`: one that
  assumes a role from a `source_profile` (`role_arn` + `source_profile`), or one with a `credential_process`.
  `af-aws-exec` runs such a profile when you name its account with `--account`, which is required here:

  ```sh
  af-aws-exec --profile deploy-target --account <id> --region <region> -- ./deploy.sh
  ```

  The AWS CLI resolves the profile from your own files, with its own ways to the workload role (the container
  credentials variables, IMDS) and endpoint overrides switched off, and the command runs only if AWS reports the
  credentials in that account (and, for a role, as a session of that role). A `credential_process` is your own
  program and runs as you wrote it: `af-aws-exec` checks the account of what it returns, not where it came from. It
  is refused, before anything is fetched, when the profile or any profile in its `source_profile` chain (a
  `[DEFAULT]` section included) sets `credential_source` (that takes the workspace's own credentials),
  `web_identity_token_file`, `login_session` or `mfa_serial` (nobody can answer the MFA prompt when an agent runs the
  command), when a profile in the chain names more than one way to get credentials (for example keys, even only a
  session token or keys in `~/.aws/config`, beside a `credential_process` or on a role profile: the AWS CLI and
  other SDKs would not agree on which to use), when the SSO profile a chain ends in is incomplete, or when the chain
  is broken. Keep one way per profile: keys go in a profile of their own, named as `source_profile`. The one
  exception is a role profile you name that is its own `source_profile` and holds the keys itself. When a
  `credential_process` fails, its output is not shown; run it yourself to see why. Only temporary credentials are passed: a profile that resolves to long-lived keys is
  refused, so use the keys to assume a role instead. If the chain ends in an SSO profile whose login is missing, the
  command exits with code 3 and the `aws sso login` command for that SSO profile (at a terminal it starts the login
  itself); the Console is not asked. These profiles do not appear in `af-aws-exec --list`, and they cannot share a
  name with a Settings profile. The source keys stay in your `~/.aws` files as before; `af-aws-exec` never hands them
  to the command.
- The workload role is **blocked** for that command: if the login is missing or expired, it fails instead of
  falling back. At a terminal it starts the device-code login for you.
- **When an agent's command needs the login**, it asks you in the Console instead: a toast at the bottom of the
  screen names the profile, its account and role from Settings, and which session asks. Press **Log in** to open the
  login window, check what it is for, and press **Log in** there; only then is a sign-in code created, and only
  that window shows it. Check the code, press **Sign in and approve**, and approve it on the page that opens. The
  agent's command waits about a minute and a half (a few seconds when it is not run by an agent session, such
  as a script in a shell session) and continues once you approve; if it has given up by then, the agent runs it again. **Close** keeps the request: it stays in the Console on your other devices too, so on a device
  whose browser cannot sign in, close it and press **Log in** in the Console on another one. **Cancel the request**
  is for a login you do not want: it withdraws the request, and for about a minute that profile is not asked for
  again. Closing the toast only hides it in that tab. **Log in** on the profile's row in Settings works at any time, also
  during that minute. This covers your Settings profiles; for a profile you defined
  yourself, or with `--no-login`, the command exits with code 3 and the login command to run in a terminal.
- The command gets an AWS config that defines **only the profile you chose** (it hands back the same short-lived
  credentials), no credentials file, and no `AWS_ENDPOINT_URL*` overrides. A tool that names that same profile
  works. A tool that names a different one — Terraform's `profile = "staging"`, `cdk deploy --profile staging`,
  `AWS_PROFILE=staging` in a script — fails with "The config profile (staging) could not be found" instead of
  quietly running as that other profile. If `af-aws-exec` cannot keep that config private (a home directory other
  users can write to, for example) it gives the command an empty AWS config instead, with a warning: still isolated,
  only a tool naming the same profile will not find it. If a script inside the command
  replaces `AWS_ACCESS_KEY_ID` (after an `assume-role`, say), the chosen profile stops resolving there rather than
  quietly meaning the new account; use the new credentials without `--profile`.
- **When you see "could not be found"**, the tool is asking for another profile. Remove that profile setting from
  the tool, or run it under that profile (`af-aws-exec --profile staging …`). Do not add `--keep-aws-config` to get
  past it: that hands the tool your own `~/.aws` files and endpoint settings again, and it would then run as the
  profile it names. Keep `--keep-aws-config` for tools that need other settings from those files.
- **Region**: `--region` if you give it, otherwise a region already exported in your shell (`AWS_REGION`, then
  `AWS_DEFAULT_REGION`), otherwise the profile's. The command gets it in both `AWS_REGION` and
  `AWS_DEFAULT_REGION`, and the "running as" line shows it. A stale
  `AWS_REGION` in your shell beats the profile's region, so give `--region` for deployments.
- `--account <id>` refuses to run unless the profile is that AWS account. Put it in scripts, runbooks and agent
  instructions for anything that deploys, so a wrong profile name stops before anything happens. For a profile that
  is **not** one of your Settings profiles (one you defined yourself) `--account` is required.
- A name that means two things is refused: two Settings labels that map to it, or your own `~/.aws` definition of
  a Settings profile's name with a different account, role or sign-in portal.
- Credentials last as long as the SSO role session, or the assumed role's session (often one hour). A longer
  command fails when they expire rather than switching identity.

## Running commands in Google Cloud as you (af-gcloud-exec)

Your Google Cloud profiles live in **⚙ Settings → the "Google Cloud" tab**. A profile says which project a
command points at and as whom it acts:

- **Label** — the display name. The profile's **name**, the one commands use, is made from it and shown next to
  it: lowercase letters, digits and `-` (every other run of characters becomes `-`, a name that would start with
  a digit gets `p` in front, and a label with nothing usable in it, such as a Japanese one, gets `p-` and a short
  code). If two labels make the same name (`Prod` and `prod`), **neither is available** in the workspace and the
  row says so; rename one.
- **Login method** — a Google account, including Google Workspace and Cloud Identity accounts.
- **Project** — the project ID (not the number) commands point at by default.
- **Quota project** (optional) — the project billed for API quota; the project itself when left blank. Some
  APIs need one with a personal login, and your account needs permission to use services on it.
- **Account** (optional) — the Google account to sign in as; a sign-in as anyone else is refused. Left blank,
  you choose at the first login.
- **Impersonate service account** (optional) — commands act as this service account through your login. Your
  account needs the Service Account Token Creator role on it. This is the way to act as a service account:
  service-account keys are not accepted anywhere.
- **Region / Zone** (optional).

**No Google credentials are stored in Agent Fleet.** The login lives inside your workspace, in a gcloud store
of the workspace's own. It is separate from the `gcloud` you run in a terminal: logging in to one does not log
in to the other, and a profile never reads or changes your own gcloud configuration, logins or application
default credentials. A change in Settings reaches the workspace within about five minutes, or at once when you
run `af-gcloud-exec`. Changing a profile's account, changing its label so that its name changes, or deleting
and adding it again, resets which account it uses: unless the account it names is already logged in in the workspace, the next run asks for a
login. Export and import carry the profiles (see [12 Settings](12-settings.md#export-import)).

### Running a command as a profile

```sh
af-gcloud-exec --list
af-gcloud-exec --profile <name> --project <project-id> -- gcloud compute instances list
af-gcloud-exec --profile <name> --project <project-id> -- terraform plan
af-gcloud-exec --profile <name> --project <project-id> -- kubectl get pods
```

- `--list` prints each profile's name, project, account ("chosen at the first login" until then), the service
  account it impersonates and its label, and names the profiles that are not available, with the reason. Choose
  by project and account, not by the name alone.
- `--project` must be the profile's project, or the command is refused. A Google login is not bound to a
  project, so this only checks that you and the profile agree on where the command points by default: a
  command's own `--project`, or a project written in a Terraform configuration, still wins.
- The command gets a short-lived **access token** of the profile and nothing else of yours: no gcloud login, no
  application default credentials, and nothing from the machine's own Google identity. `af-gcloud-exec` prints
  "profile … runs as <account> in project …; the token is valid for N more minutes" (`-q` leaves it out); the
  token itself is never printed.
- The token lasts what remained when it was handed over: at least ten minutes, at most about an hour. It is
  **not renewed during the command**, so a long `terraform apply` or a `kubectl` watch that outlives it fails.
  Split long work into shorter runs.
- The first run installs the Google Cloud SDK (one pinned version, with the GKE auth plugin) into your home: about
  85 MB to download and about 510 MB on disk, kept across stops and a Recreate. The first run of each gcloud
  command after that is a few seconds slower once. The **Toolchain** tab's table of tool versions then shows
  gcloud. A plain `gcloud` in a terminal is that same program, but with your own login, if any, not a profile's.
- For `kubectl`, fetch the cluster's entry once through the profile
  (`af-gcloud-exec --profile <name> --project <project-id> -- gcloud container clusters get-credentials <cluster> --location <location>`),
  then run `kubectl` through `af-gcloud-exec` too: the GKE auth plugin asks gcloud for the token, and only inside
  `af-gcloud-exec` does gcloud have one.

**Which tools use the token.**

| Tool | With `af-gcloud-exec` |
|---|---|
| `gcloud`, including `gcloud storage` | yes |
| `kubectl` against a GKE cluster | yes, through the GKE auth plugin |
| Terraform's Google provider | yes, with the quota project |
| `bq` | yes. It comes with the SDK but is not on the path: run `~/.local/share/agent-fleet/google-cloud-sdk/bin/bq` |
| `gsutil` | **no**. It ignores the token: without a gsutil (boto) configuration of your own it sends its requests without any login, so a public bucket answers; with one, it can act as whatever identity that configuration holds. Use `gcloud storage` |
| Google's client libraries (Go, Python, Node, …) | only when the program hands them the token |

A program built on a client library stops with an error such as "File … was not found" or "no such file or
directory" for its default credentials. That is deliberate: it would otherwise find another identity (your own
gcloud login, or the machine's). The program has to take the token itself — in Python
`google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"])`, in Go
`option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`.
In Node, create the `OAuth2Client` from the same version of `google-auth-library` the client library uses; one
from another major version is accepted without an error and sends no login at all. Agents ask you before changing
your code that way.

### Logging in

At a terminal, `af-gcloud-exec` starts the Google sign-in itself when the profile has no usable login. It prints
a URL: open it in your browser, sign in (as the profile's account, if it names one), and paste the
**verification code** the page shows back into **that** terminal. Paste only a code from a sign-in you started
yourself just now.

An agent's command cannot sign in for you. It exits with code 3 and the message "Google Cloud login required …
log in from a terminal with:" followed by the command to run, which looks like this:

```sh
af-gcloud-exec --profile <name> --project <project-id> --login -- true
```

Run it in a terminal of your own — a shell session, or in Claude Code type it after `!` at the prompt — then tell
the agent to run its command again. `--no-login` makes `af-gcloud-exec` exit with code 3 instead of prompting,
even at a terminal.

**When a login ends.** Google can refuse a stored login: it was revoked, or your organisation requires you to
sign in again after a set time (session length). The next run that needs a fresh token then asks for a login as
above, and that login really signs you in again instead of reusing what was stored. The workspace **cannot warn
you before** such an end: the time is not recorded anywhere it can read. Until the token already handed out has
less than ten minutes left, runs keep working, so the request for a login can come up to about 50 minutes after
the end.

### When it stops

| What you see | What it means |
|---|---|
| exit code 3, "Google Cloud login required" | The profile has no login yet, or Google refused the stored one. Log in as above. Exit 3 always means a login. |
| "is for project X, not Y; --project must be the profile's project" | The wrong profile for this project. Check `--list`. |
| "no Google Cloud profile …" or "not exported: …" | The name is not one of your available profiles: add or fix it in Settings. |
| "gcloud could not mint a token: …" | Not a login problem: no permission (also on the service account to impersonate), an API not enabled, or the network. The message says which. |
| "the token gcloud minted is valid for only …" | gcloud could not hand out a token with ten minutes left. Try again in a minute. |
| "this deployment does not export Google Cloud profiles" | Your deployment does not offer Google Cloud profiles. |
| "the profile changed in Settings while this run started; run it again" | Run it again; check `--list` if the account or project now differs. |
| "waiting for another af-gcloud-exec or a profile sync …" | A login in a terminal, or another run, is using the workspace's gcloud store. It continues when that ends. |

**What it does not guarantee.** The command runs as you and can read your home, your own gcloud directory
included: `af-gcloud-exec` keeps Google's standard ways of finding credentials (gcloud's and the client
libraries') from finding anything but the token it hands over; it does not make your files unreadable, and a tool
that looks elsewhere, such as `gsutil` with its own configuration, is not covered. On a workspace that runs directly on a Google Cloud VM, a
program that ignores those standard ways could still reach the VM's own identity.

## Environment settings and recreating the workspace

In **⚙ Settings → the "Toolchains" tab** you can adjust the workspace environment. Changes **apply to sessions /
shells started afterwards** (running ones and existing processes pick them up after you stop and then start the
workspace again).

- **Time zone (TZ)** — the default is Japan time. Applying a change requires stopping and starting the workspace.
- **Node.js / Java (JAVA_HOME)** — pick the versions to use. The Java list also offers versions that are
  **not in this workspace yet**; picking one shows an **Install** button that fetches it right there (about
  200MB, into your home volume, so it survives restarts). Sessions started after it finishes get it as
  `JAVA_HOME` — no stop and start needed.
- **Agent CLI updates** — "Update the agent CLIs and rtk to the latest on start" (covers claude / opencode / codex / cursor / GitHub Copilot / Antigravity (agy) / rtk). Default is OFF (pinned to the versions baked into the image). Kiro is not part of this toggle — its version is fixed by the image rebuild / on-demand install and its own auto-update is kept off. Neither is Muse Code: it is installed from its card at the build this image pins and never updates itself; when a newer pinned build arrives with the image, the card offers **"Update Muse Code"** ([06](06-agents.md#muse-code)). lcpp has no CLI in the workspace to update — its engine belongs to the deployment.

### Recreating the workspace (danger zone)

In **⚙ Settings → the "Danger zone" tab** is **"Recreate the workspace"**. It discards the
container and rebuilds it from the latest image; pressing **"Recreate"** shows a confirmation. What stays and
what goes is as follows.

Not every deployment offers this tab. Recreating and cleaning home remove files from the home
itself, and on some deployments the home is out of the control plane's reach
([ref/deploy-targets](../ref/deploy-targets.md)); there the tab is not shown. Stopping and starting
the workspace from the workspace bar works everywhere.

- **What is lost** — running sessions, and **cloned repositories (`~/repos`, including uncommitted changes)**.
  `~/repos` is the **only** thing deleted.
- **What stays** — everything else in your home (`~`) remains. Logins and connections (GitHub / Bitbucket /
  Claude etc.), `~/.local` (claude / node etc.), and your settings and caches are preserved, because the home
  volume is reattached to the recreated container.

In short: "**only `~/repos` is deleted, and the container is rebuilt from the latest image. The rest of home
(logins, connections, `~/.local`, etc.) stays**". Use it when you want to pick up an image update or the
environment is broken. **Uncommitted changes are lost**, so push / commit before running it ([04](03-code.md)).

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

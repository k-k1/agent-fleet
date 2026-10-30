---
audience: "anyone looking at a web app running in the workspace, or at a browser an agent opened"
---

# 14. Preview and browser — browser pane, preview URLs, dev sign-in, an agent's browser

English | [日本語](14-preview-browser.ja.md)

This chapter covers seeing an app that runs in the workspace: previewing a web app in the browser pane, signing in with Google on a preview URL (the development IdP), and operating a browser an agent opened.

## Viewing a web app you started — browser pane and lightweight preview

You can check web services started inside the workspace (dev servers of all kinds, Spring Boot, any web app)
on the spot, **with no extra port publishing and no container rebuild**. There are two methods.

Open **"Preview"** on the workspace action bar and enter a port number under **"Open a specific port"**. The
primary button opens the default target. That is **"Open in new tab"** (the port's preview subdomain) whenever
this start has preview URLs, even for a port you have not listed, which then opens for you alone. Otherwise it is
**"Open in pane"** (browser pane), or **"Lightweight preview"** where there is no browser pane. The other targets
sit beside the primary button, greyed out when they are not available. All of them work only while the workspace
is running. A loopback URL
such as `http://127.0.0.1:3000/` in an agent's reply or a rendered document opens the same way when you click it
([07](07-files.md#links-in-rendered-markdown)).

- **Browser pane ("Open in pane")**: a browser inside the workspace opens `127.0.0.1:{port}` directly, and
  **only its rendering and input** are mirrored into a Console pane. You can click, scroll, type ASCII/Japanese,
  go back/forward, reload, and navigate paths, and **HMR (hot reload), WebSocket, SSE, cookies, redirects, and
  absolute-path assets** all work just like ordinary localhost. Use this when you want to touch the screen and
  verify it.
- **Lightweight preview**: opens the port in a **new tab**. **WebSocket and SSE do pass through** (HMR works
  too, depending on the app's own configuration), but the URL is a **sub-path** (`/preview/{port}/`), so an app
  that emits **absolute paths** like `/static/...`, or a screen that depends on the root path or a cookie path,
  will break.
- **Preview subdomains**: on some deployments a URL such as
  `https://xxxxxxxx-3000.<preview domain>/` is **issued automatically every time the workspace starts**. The app
  is served **at the root**, so the sub-path problem above cannot happen, and **several ports** (3000 and 8080,
  say) are open at the same time. See "Preview subdomains" below.

**When in doubt:** if a preview URL has been issued, that is the most straightforward option. Otherwise use
"Open in pane" when you want to touch the screen, and the lightweight preview when one look at an HTTP response
is enough.

> **On a deployment that runs workspaces on Kubernetes (a preview runtime) or on ECS Fargate there is no browser pane.** "Open in pane" is
> greyed out, and its tooltip and the hint under it say why: Chromium's sandbox cannot start in the
> restricted container a workspace runs in, and the deployment does not run Chromium without it. A port in a
> session row opens in the lightweight preview instead, and a browser pane kept in your layout shows the
> reason with a button for the same page in the lightweight preview. Agents cannot hand you a browser
> page there either (next section). Details: [browser-pane.md](../ref/browser-pane.md#where-there-is-no-browser-pane).

On a touch screen such as a tablet, **swipe to scroll** (a flick keeps coasting after you lift your finger),
**tap to click**, **press and hold to drag** (text selection, sliders), and **pinch with two fingers to zoom**.
A pinch re-lays the page out at a narrower width rather than stretching the picture, so text stays legible at
its own size, and pinching back returns you to the original view. **Double-tap** jumps between the fit-to-width
view and life size.

A tap does **not** raise the keyboard, because it would appear every time you pressed a link or a button. To type
into a field on the page, tap the field and then open the keyboard with the **keyboard button** at the bottom
left of the pane; it stays open while you keep tapping the page.

### How to use it

1. Have a shell or an agent start the dev server. Make the server listen on `127.0.0.1` inside the workspace
   (or on all interfaces).
2. Open **"Preview"** on the workspace action bar and, under **"Open a specific port"**, enter a port number from
   `1..65535` (excluding `7700`, which the Agent itself uses), plus a path starting with `/` if needed (no host
   or external URL).
3. Press the primary button, or **Enter** in either field, to open the default target. Choose **"Open in pane"**
   to touch the screen, or **"Lightweight preview"** for a single HTTP check.

For example, once you start an API server on 8080 in a shell, enter `8080` under "Open a specific port" and press
"Open in pane", and the app appears in a pane. There is no need to ask IT to open extra ports.

### Preview subdomains (only where they are issued)

A URL containing a random label is issued **every time the workspace starts**.

```
https://k7f2q9x1w3ub5nzt0abc-3000.pv.example.com/   → port 3000 (e.g. React / Next.js)
https://k7f2q9x1w3ub5nzt0abc-8080.pv.example.com/   → port 8080 (e.g. Spring Boot)
```

- **Where to find them**: open "Preview" on the workspace action bar; they are listed per port under
  **Preview URLs (this start)**. Click to open in a new tab, or use "Copy" to take the URL.
- **They change on every start**, and stop working when the workspace stops (the old URL returns 404). Assume
  any document you paste one into goes stale quickly.
- **Signing in is required by default.** The first visit bounces through the Console login once and comes back.
- **You choose which ports are exposed.** The default is `3000, 8080`; change it under Settings ›
  **Preview subdomains**. A port that is not listed has no URL **for anyone but you** (see the next point); the list is
  what keeps an admin console you did not mean to expose away from other people.
- **Any other port opens for you alone.** A port the workspace is listening on but that is not listed (Vite on
  `5173`, a throwaway server on `4000`) opens on its subdomain from the port chip in the session row or from the
  Preview popover, with no setting to change. The click is a plain click on the chip when a preview URL is
  issued, and a menu behind the chip's `⋯` (or right-click, or long-press) offers browser pane, subdomain and
  lightweight preview side by side. This opening is **yours only**, public mode and tenant sharing included, and
  it ends when that server stops: a different process that later takes the port is not reachable until you open
  it again. It also ends when the workspace stops or restarts and when you press "Discard and mint a new one",
  because it belongs to the URL of that start. At most **16** ports can be open this way per workspace; past
  that the page says "Too many temporarily opened ports" until a server stops or you list the port. It needs
  the port to answer plain HTTP (a database, SSH or an HTTPS-only dev server is refused with the reason), and
  it does not set `AF_PREVIEW_URL_<port>`. The development OIDC IdP accepts a sign-in callback on such a port
  for you alone, and only while the opening lasts
  ([below](#signing-in-with-google-on-a-preview-url-development-oidc-idp)). List the port (the menu's **Expose
  this port…**) when the app has to know its public URL, or someone else should see it or sign in to it.

#### What the app has to get right

- **Do not hard-code `http://localhost:8080` as the API origin.** From the browser's point of view `localhost`
  is **the machine of the person looking at the screen**. Route `/api` to 8080 through the dev server instead
  (Vite's `server.proxy`, Next.js's `rewrites()`), and the same configuration works both on your own PC and in
  the preview.
- ⚠️ **Leave the proxy's target (`destination` / `target`) as `http://127.0.0.1:8080`.** Rewriting it to the
  preview URL (the natural move if you assume the frontend "cannot see" 8080) makes **every API call return
  401 (`preview requires sign-in`)**. The proxy is a **server-side** call made by the dev server inside the
  container, and it does not carry the login state your browser has.
- If the page on 3000 really must call 8080 **directly**, turn on **"Allow calls between ports"** in the
  settings (off by default).
- Set `server.forward-headers-strategy=framework` for Spring Boot. Next.js Server Actions are validated
  correctly over this path as well.
- ⚠️ **Next.js (15.2 and later, including 16.x) needs `allowedDevOrigins`.** The dev server **blocks
  cross-origin access to `/_next/*` by default**, so on a preview subdomain **only the layout shell renders and
  none of the data fetched from the API appears** (not blank, no error), so it looks like a bug in your own app.
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

1. **Only you**: do nothing (the default).
2. **Colleagues in the same tenant**: turn on **"Show it to your tenant"** under
   Settings › Preview subdomains.
   - They open it **after signing in to the Console** (nobody outside the tenant can see it).
   - **This does NOT return to off when the workspace stops or restarts.** Turn it off yourself
     when you are done.
   - Hand them the link from the **"Share"** button. **That link keeps working across restarts**:
     a raw `https://xxxx-3000.…` URL starts returning 404 the next time your workspace starts.
   - In their Console it appears under **"Shared with you"** in the preview popover. **While your
     workspace is stopped it shows "Stopped" and cannot be opened**. They cannot start your
     workspace, so ask them to ping you if they need it running.
   - ⚠️ **Your workspace will not idle-stop while someone has it open, and that running time is
     billed to you.** (A page left open and untouched does eventually stop.)
3. **People outside the tenant**: **Open without signing in** lets anyone with the URL open it. It
   **always returns to off when the workspace stops or restarts** (and the URL changes).
- **The URL you currently have is shown under Settings › Preview subdomains, as
  "Current URL".** While the workspace is stopped it says none is issued, but the domain it will
  use is written just below.
- If a URL went to the wrong place, press **"Discard and mint a new one"**. Tabs that are open now start
  returning 404 immediately. ⚠️ **Pressing it while the workspace is stopped does nothing**, because
  there is no issued URL to discard (the next start gets a new one anyway).

To sign in with Google on a preview URL, see [Signing in with Google on a preview URL](#signing-in-with-google-on-a-preview-url-development-oidc-idp) below.

### Examples by setup

| Setup | Example input | Which to open with |
|------|--------|----------------|
| **Node / Vite** | `5173` + `/` | Uses HMR (WebSocket), so **browser pane**. |
| **Spring Boot** | `8080` + `/` or `/actuator/health` | Screens involving redirects, absolute `/assets/*`, and cookies: **browser pane**. Just a one-time look at the health JSON: lightweight preview. |
| **API only** | `8080` + `/api/health` | One-time JSON / status checks: **lightweight preview**. SSE, auth cookies, redirects, and interactive checks: **browser pane**. |
| **Frontend + API (multiple ports)** | frontend `5173` / API `8080` | **Preview subdomains are the best fit if you have them** (each port gets its own URL). Otherwise open the frontend's `5173` in a **browser pane**; fetch / WebSocket / SSE to another port (`8080`) works from there (as in a normal browser, CORS configuration is required). |
| **React 3000 + Spring Boot 8080** | `3000` / `8080` | Preview subdomains serve both at the root. Routing the API through the dev server's proxy onto `/api` is the least trouble, and the same configuration then works on your own PC too. |

> **Spring Boot links / redirects**: to have them resolve correctly, set
> `server.forward-headers-strategy=framework` (or `native`) on the app side.

### Status display and recovery

When the browser pane fails to render properly, a status appears in the pane.

| Status | Meaning | What to do |
|------|------|------|
| `target-unreachable` | The browser started, but the connection to that port/path hasn't been established yet. **Waiting for the dev server to start** is also this state. | Check the port number, the path, and whether the server is listening; once it's up, press **"Reload"**. If it persists, press **"Reconnect"**. |
| `target-unreachable`, "listening but does not speak HTTP" | Something listens on the port, but it is not an HTTP server. The browser pane shows HTTP only. | Check that the port is the web server's, not a database's or a debugger's. |
| `target-unreachable`, "serving HTTPS" | The dev server on that port serves HTTPS. | Turn off the dev server's HTTPS, then press **"Reconnect"**. |
| `disconnected` | Communication with the pane (WebSocket) was lost. This is not necessarily a browser crash. | Check that the workspace is running and connectivity is back, then press **"Reconnect"**. |
| `crashed` | The browser inside the workspace terminated abnormally and cannot continue that display. | Reopen with **"Reconnect"**. If it keeps happening, check the workspace's memory usage and the target app ([21](21-troubleshooting.md)). |

If you try to open it while the workspace is stopped or starting, a dedicated notice appears. Reopen once the
workspace is running.

### Limits and transience (good to know)

- The number of panes open at once, the display size and the frame rate are capped (the numbers are in
  [limits](../ref/limits.md#fixed-in-the-product)). It is not suited to video playback or high-frame-rate checks.
- If you **switch the pane to another view / send it to the back**, rendering stops and the page is kept for a
  short while. Return quickly and the same state continues; after that it is recreated from the saved
  port/path.
- On a **workspace Stop → Start**, a Console reload or **"Reconnect"**, browser panes that were on display are **automatically
  recreated** from the same port/path, but cookies and half-typed input do not come back.
- This is **not a general-purpose browser for opening external URLs** (it is localhost-only; there is no host
  field, only a port and path). Nor is it a **full replacement for browser devtools** with DOM / Network /
  Sources. The pane's "Console" lets you view and copy that page's `error` / `warn` logs and the like
  (a bounded number of entries; not stored persistently).

> **On a smartphone**, the action bar has no "Preview" button: tap **⋯** at its right end instead; the port
> and path fields are under "Open a specific port" in the popover it opens, with the same buttons.

## Signing in with Google on a preview URL (development OIDC IdP)

Google accepts only redirect URIs registered on its OAuth client, and a preview URL changes at every start.
Where the deployment turns it on, the Control Plane can stand in for Google **during development**: a sign-in
server of its own at `https://<console>/dev-idp`. Your app changes **configuration only** (issuer, client id,
client secret); no code, and nothing to register in the Google Cloud console.

- **Availability**: a deployment administrator can turn it off in the Admin modal, and the
  operator can lock it off with `AF_DEV_IDP=0` (ADR 0116). It is on by default; while off, every
  `/dev-idp/…` URL is a 404 and nothing is injected. **It also needs preview subdomains**: on a deployment
  without a preview domain the IdP is not served and the section below does not appear. There is **no
  per-workspace switch**: wherever the IdP is offered, the values below reach every workspace at every start
  and sign-in simply works. Settings › Preview subdomains shows the issuer, the discovery URL and the client id
  with copy buttons. The client secret is not shown there: read `AF_DEV_IDP_CLIENT_SECRET` inside the
  container. The client id and secret stay the same across restarts; **"Issue a new secret…"** replaces both
  (the old pair stops working at once, and the new one reaches the container at its next start, so stop and
  start the workspace). A production configuration must never trust this issuer.

- **What you get**: `AF_DEV_IDP_ISSUER`, `AF_DEV_IDP_WELL_KNOWN` (the discovery URL), `AF_DEV_IDP_CLIENT_ID` and
  `AF_DEV_IDP_CLIENT_SECRET` inside the container, next to `AF_PREVIEW_URL_*`. The client is **yours alone**: its
  id is bound to your workspace and the tokens are issued for that id only.
- **Who signs in**: whoever is signed in to the Console. There is no password prompt. If you have signed in
  to the Console with Google, the app sees your **real Google `sub` and address**, so the user rows it already
  has still match. Otherwise it sees your Console address and a stable `sub` of the form `af-…`. A colleague
  opening your shared preview ([above](#showing-it-to-someone-else-three-steps)) signs in as themselves.
- **Where it may send the browser back to**: only your **current** preview URL on a **listed port**, the same
  URL on a port you opened for yourself without listing it (while that opening lasts, and only when you are the
  one signing in), and `http://localhost:<port>` / `http://127.0.0.1:<port>` (so the browser pane works too).
  Anything else, including the URL from before a restart and a colleague signing in on a port you did not list,
  gets an error page and is never redirected to. Set the app's own URL from
  `AF_PREVIEW_URL_<port>` (for example `AUTH_URL`) so the callback it sends is exactly that.

Settings › Preview subdomains › **"App configuration examples"** writes the configuration for you, for Spring
Boot, Auth.js, Go, Better Auth or plain environment lines. Pick the **Form** (for Spring Boot "dev profile
(recommended)", "One file with defaults" or "Environment only"; for Auth.js v5 or next-auth v4), the **Port**
(one of your listed ports), whether to **Include preview settings**, and whether the **Values** reference
environment variables or are filled in. The client secret is always an environment reference. The two
examples below show the idea.

**Auth.js (NextAuth)**, v5: point the Google provider at the dev issuer when the variables exist.

```ts
// auth.ts
import NextAuth from "next-auth"
import Google from "next-auth/providers/google"

export const { handlers, auth } = NextAuth({
  providers: [
    Google(
      process.env.AF_DEV_IDP_ISSUER
        ? {
            issuer: process.env.AF_DEV_IDP_ISSUER,
            clientId: process.env.AF_DEV_IDP_CLIENT_ID,
            clientSecret: process.env.AF_DEV_IDP_CLIENT_SECRET,
          }
        : {},
    ),
  ],
})
```

In v4 it is `GoogleProvider({ wellKnown: process.env.AF_DEV_IDP_WELL_KNOWN, clientId, clientSecret })`; a generic
OIDC provider with `issuer` and the same two values works as well.

**Spring Boot**: keep the registration called `google` and move its provider.

```yaml
# application-devidp.yml  (activate with SPRING_PROFILES_ACTIVE=devidp, in the workspace only)
spring:
  security:
    oauth2:
      client:
        registration:
          google:
            client-id: ${AF_DEV_IDP_CLIENT_ID}
            client-secret: ${AF_DEV_IDP_CLIENT_SECRET}
            scope: openid,email,profile
        provider:
          google:
            issuer-uri: ${AF_DEV_IDP_ISSUER}
      resourceserver:
        jwt:
          issuer-uri: ${AF_DEV_IDP_ISSUER}   # only for a backend that verifies the ID token itself
```

With `server.forward-headers-strategy=framework` (above) Spring builds the callback from the preview host.

> ⚠️ **Never let a production configuration trust this issuer.** It will vouch for anyone who can sign in to
> the Console. Keep the settings in a profile or in variables that exist only in the workspace; every token also
> carries `af_dev: true`, so a backend can refuse it.

**Test personas.** To try role checks (admin, viewer, …) define personas under Settings › Preview subdomains ›
"Test personas". Once the workspace has one, the sign-in shows a confirmation page where you pick yourself or a
persona. A persona's address is always `<handle>@example.test` and its `sub` starts with `persona:`, so it can
never be a real member; the token audit names the persona. A workspace holds at most 20 personas, and a handle
is 1 to 32 characters of `a-z`, `0-9`, `.`, `_` and `-`, starting with a letter or digit. The page accepts one
answer, within 5 minutes; **Cancel** sends the app back an `access_denied` error. A sign-in with `prompt=none`
still passes silently as yourself, and a workspace with no persona never sees the page.

Good to know: tokens last 10 minutes and the sign-in code 60 seconds. **Only sign-in is replaced**: you do not
get Google API access tokens (Calendar, Drive, …), and there is no `picture`. The sign-in request may not
carry `prompt` other than `none`, `max_age`, or a `response_mode` other than `query`; the app gets an
`invalid_request` error back (some Google examples add `prompt: "consent"`, so drop it for the dev issuer).
The signing key changes every 30 days and the old one stays in the JWKS for 24 hours, so let the library fetch
keys from the discovery URL rather than pinning one. To rotate the credentials, use "Issue a new secret…"
(above): it takes effect immediately for the endpoints, but a running container keeps the values it started
with until the next start.

### Using it from an app on your own machine

An app running on your own PC at `http://localhost:<port>` can sign in through the same IdP, **also while the
workspace is stopped** (ADR 0116).

1. In a workspace terminal, read the three values: `printenv AF_DEV_IDP_ISSUER AF_DEV_IDP_CLIENT_ID AF_DEV_IDP_CLIENT_SECRET`. Settings › Preview subdomains shows the issuer and client id too; the secret exists
   only in the container, so start the workspace once to read it. Set the three as environment variables on your
   machine.
2. Make the app's callback `http://localhost:<port>/<callback path>` (`127.0.0.1` works too, any port).
3. In the browser on that machine, stay signed in to the Console. The sign-in needs no other login.

- **Treat the secret like a password**: keep it out of git, chat and screenshots. Settings › Preview subdomains ›
  "Issue a new secret…" revokes every copy at once. It issues a new client id as well as a new secret: the
  workspace needs a restart to get them, and your machine needs both new values.
- **It does not work while** the IdP is switched off for the deployment (in the Admin modal or with
  `AF_DEV_IDP=0`), or on a deployment without a preview domain (the issuer then answers 404).
- **A redirect to the preview URL still needs the workspace running**; only `localhost` / `127.0.0.1` is
  accepted while it is stopped.
- **One configuration for both places**: Settings › Preview subdomains › "App configuration examples" generates
  the snippets, with a variant that reads the environment variables. The idea is a fallback: use the dev issuer
  only when the variables exist. Spring Boot: put it in the `devidp` profile
  (`SPRING_PROFILES_ACTIVE=devidp`, recommended: the production configuration never names the dev issuer).
  Auth.js: `process.env.AF_DEV_IDP_ISSUER ? { issuer, clientId, clientSecret } : {}`, as in the example above.
  Never put the dev issuer into the production configuration.

## Operating a browser the agent opened

When an agent is driving its own browser (Chromium) inside the workspace and reaches something
**only a person can do** (signing in, a one-time code, ticking a consent box), it can hand that
page over to you. This is a different thing from the browser pane above: there you open your own
local web app, here you take over a page the agent already has open. Like the browser pane, it does not
exist on a deployment that runs workspaces on Kubernetes (a preview runtime) or on ECS Fargate.

- A link appears in the agent's message: **"Open the browser and operate it (opens as a pane in this
  tab)"**. **You are the one who clicks it**: nothing opens until you do, and it opens as a pane in
  the tab you are already in, not in a new one.
- It starts in **View only**. The picture is live, but clicks, scrolling and keystrokes are not
  delivered. Once the agent hands control over it becomes **User control** and you can operate it.
  (**If it feels unresponsive, this is why**: the pane says so at the top.)
- What you are being asked to do is shown as a **Requested browser action**. **Action complete** or
  **Cancel action** tells the agent how it ended. Pressing them reports *what you did*, not that the
  site's own processing succeeded. That is also why an agent must not make the final send, buy or
  consent click for you.
- **Close view** only stops showing it. **The agent's browser, its page and its session stay open.**
- You can reopen an attachment from **Preview** in the workspace action bar, under **Attached
  browsers**. If the same Chromium has other tabs, **Switch to another tab** moves between them.

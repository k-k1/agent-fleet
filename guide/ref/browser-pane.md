---
audience: "everyone, and specifically an agent deciding how to show a running web app to a person"
source_of_truth: "this file for terminology, states and recovery; limits.md for the numbers; the Console for what a button is called"
updated: "2026-10"
---

# Browser pane — the usage contract

English | [日本語](browser-pane.ja.md)

Two different things can show a web app running inside a Workspace, and picking the
wrong one wastes a round trip. This is the contract for both.

## The two routes

| Term | Contract |
|---|---|
| **Browser pane** (the button says "open in pane") | Chromium inside the Workspace opens `http://127.0.0.1:{port}{path}` directly and relays rendering and input to a Console pane. Use it for HMR, WebSocket, SSE, cookies, redirects, absolute asset paths, and anything the person needs to operate. |
| **Lightweight preview** | A low-cost HTTP proxy opened in a new tab. Only for looking at JSON, a health endpoint or a simple static page once. No HMR, no WebSocket, no SSE; apps that depend on the root path, absolute assets, redirects or cookie paths are not guaranteed to work. |
| **Page** | The temporary Chromium Page and its own browser context that the pane owns. How many may exist per Workspace is in [limits](limits.md#fixed-in-the-product); they share no cookies or storage. |

**When in doubt, use the browser pane.** The lightweight preview is the cheap
exception for "I just need to see one HTTP response".

How to open either one (the port and path fields, which button is the default where) is a member
procedure: [member/10 Going further](../member/10-integrations.md#how-to-use-it). The port `7700` cannot be
opened, because the Agent itself uses it.

## Which route, by example

| Setup | Input | Choose |
|---|---|---|
| Node / Vite | `5173` + `/` | Browser pane: the HMR WebSocket needs it. |
| Spring Boot | `8080` + `/`, or `/actuator/health` | Browser pane for screens with redirects, absolute `/assets/*` or cookies. Lightweight preview to read health JSON once. |
| API only | `8080` + `/api/health` | Lightweight preview for a one-off JSON or status check. Browser pane for SSE, auth cookies, redirects, or anything interactive. |
| Frontend + API | frontend `5173`, API `8080` | Open the frontend's `5173` in the pane. Fetch, WebSocket and SSE to another loopback port work, but CORS still applies exactly as in a normal browser. Open the API as a second Page if needed. |

## States and recovery

| State | Means | What to do |
|---|---|---|
| `target-unreachable` | Chromium started, but nothing answers HTTP at that port and path. Waiting for a dev server to boot looks like this too. When the port is listening but does not speak HTTP, or serves HTTPS, the pane says so (the Agent probes the port once per failed navigation); with nothing listening the message stays generic. | Check the port, the path and that the server is listening; **Reload** once it is up. **Reconnect** if it persists. |
| `disconnected` | The browser WebSocket between Console, Control Plane and Agent dropped. This does not necessarily mean Chromium died. | Check the Workspace is running and connectivity is back, then **Reconnect**. |
| `crashed` | Chromium inside the Workspace died and the existing Page cannot continue. | **Reconnect** to get a new Page. If it repeats, look at the Workspace's memory use and at the app itself. |

Connecting while the Workspace is stopping or starting shows its own overlay; wait
until it is running, then reconnect.

Reconnect, a Console reload and a Workspace stop/start all create a *new* Page at the
current port and path. Cookies, storage and half-typed input are not restored.

## Lifecycle and resource limits

- A visible browser connection keeps the Workspace warm.
- When the pane is hidden or the Console's browser tab goes to the background,
  rendering stops. The Page is held for a grace period (see [limits](limits.md#fixed-in-the-product)) and then released. On return you
  get the same Page if it was held, or a new one built from the saved port and path.
- Removing the pane's identity from the layout releases the Page. Where the identity
  survives (closing the last pane back to an empty view), the grace period applies
  instead, and the Page still counts against the limit during that grace period.
- A Workspace stop discards the temporary Page but keeps the port and path in the
  layout; after start, a visible pane rebuilds itself from the same target.
- Ceilings (Chromium processes, concurrent Pages, viewport, frame rate) are in
  [limits](limits.md#fixed-in-the-product). It is not built for video or high-frame-rate checking.

## Scope and known limits

- **This is not a general browser.** Top-level navigation is restricted to loopback
  HTTP(S) and redirects to the outside are stopped.
- The Console drawer keeps a bounded number of console messages and uncaught errors
  ([limits](limits.md#fixed-in-the-product)) for the connected Page, surfacing `error` and `warn` first. It is not a persistent log and
  not a substitute for DevTools: there is no DOM, Network, Sources or Storage.
- Upload and download, clipboard, drag and drop, audio, video, WebRTC, permission
  prompts and multiple tabs are all out of scope.
- **On a smartphone the entry point moves into `⋯`.** At phone widths the workspace
  action bar has no Preview button; `⋯` at its right end opens a popover that holds the
  same port / path fields and "Open in pane". From there it is the same as on a
  desktop: the toolbar, tapping the canvas, Japanese input, the Console drawer.

## Where there is no browser pane

A deployment that runs workspaces on the `kubernetes` runtime (a preview, see
[deployment targets](deploy-targets.md)) or on `ecs` (Fargate) offers **no browser features at
all**: no browser pane, no Chromium attachments for agents, no headless Chromium. A workspace pod
on `kubernetes` runs under the Pod Security `restricted` level, which sets NoNewPrivs (the setuid
`chrome-sandbox` cannot elevate) and the runtime's default seccomp profile (no user namespaces).
A Fargate task cannot be given `SYS_ADMIN`, so the same default seccomp profile refuses the
namespaces the setuid helper would create. Either way Chromium's sandbox cannot start; running
Chromium without its sandbox was decided against (ADR 0106, addendum 2026-10-04). `ecs-ec2`,
`docker` and `native` are not affected.

What you see instead:

- In **Preview**, "open in pane" is greyed out; its tooltip and the hint under it name the reason.
  The **lightweight preview** works as everywhere else.
- A port in a session row opens in the lightweight preview (a new tab), not in a pane.
- A browser pane restored from a saved layout shows the reason in place of the page, with a
  button that opens the same port and path in the lightweight preview. A Chromium attachment
  pane restored the same way shows the reason only, since it has no local port to open.
- Agents are not offered the browser tools at all; one that calls them by name anyway gets
  `browser_unavailable` with the same explanation.

The decision comes from the deployment's runtime, so it is the same for every member of it and
shows even while the workspace is stopped.

## For agents working inside a Workspace

You have no tool that opens, drives or sees this pane; it belongs to the person. Tell
them the exact port and path and point them at Preview → open in pane. Never claim a
UI "looks right" on the basis of a pane you cannot see; if you need to verify
something yourself, drive your own headless Chromium and say that is what you did.
Where `AF_BROWSER_UNAVAILABLE` is set in your environment there is neither a pane nor a headless
Chromium ([above](#where-there-is-no-browser-pane)): verify with tests and HTTP checks, offer the
person the lightweight preview, and say plainly that nothing was checked visually.

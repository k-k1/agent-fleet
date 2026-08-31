---
audience: "everyone, and specifically an agent deciding how to show a running web app to a person"
source_of_truth: "this file for terminology, flow, states and limits; the Console for what a button is called"
updated: "2026-08"
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
| **Page** | The temporary Chromium Page and its own browser context that the pane owns. At most 2 per Workspace, and they share no cookies or storage. |

**When in doubt, use the browser pane.** The lightweight preview is the cheap
exception for "I just need to see one HTTP response".

## The flow

1. Have a shell or an agent start the dev server, listening on `127.0.0.1` inside the
   Workspace (or on all interfaces).
2. On desktop or tablet, open **Preview** from the workspace action bar.
3. Enter a port in `1..65535` (except `7700`) and a path that starts with `/`. There
   is no host field — external URLs are not accepted.
4. Normally choose **open in pane**: back, forward, reload, changing port or path,
   clicking, scrolling and typing all happen inside the pane.
5. Choose the **lightweight preview** only for a plain HTTP check.
6. If an overlay appears, follow the state table below. If the server was started
   afterwards, **Reload** first; **Reconnect** rebuilds the connection or Chromium.
7. Check the **Console** drawer's badge for `warn` and `error` from the page.

Reconnect, a Console reload and a Workspace stop/start all create a *new* Page at the
current port and path. Cookies, storage and half-typed input are not restored.

## Which route, by example

| Setup | Input | Choose |
|---|---|---|
| Node / Vite | `5173` + `/` | Browser pane — the HMR WebSocket needs it. |
| Spring Boot | `8080` + `/`, or `/actuator/health` | Browser pane for screens with redirects, absolute `/assets/*` or cookies. Lightweight preview to read health JSON once. |
| API only | `8080` + `/api/health` | Lightweight preview for a one-off JSON or status check. Browser pane for SSE, auth cookies, redirects, or anything interactive. |
| Frontend + API | frontend `5173`, API `8080` | Open the frontend's `5173` in the pane. Fetch, WebSocket and SSE to another loopback port work, but CORS still applies exactly as in a normal browser. Open the API as a second Page if needed. |

## States and recovery

| State | Means | What to do |
|---|---|---|
| `target-unreachable` | Chromium started, but nothing answers HTTP at that port and path. Waiting for a dev server to boot looks like this too. | Check the port, the path and that the server is listening; **Reload** once it is up. **Reconnect** if it persists. |
| `disconnected` | The browser WebSocket between Console, Control Plane and Agent dropped. This does not necessarily mean Chromium died. | Check the Workspace is running and connectivity is back, then **Reconnect**. |
| `crashed` | Chromium inside the Workspace died and the existing Page cannot continue. | **Reconnect** to get a new Page. If it repeats, look at the Workspace's memory use and at the app itself. |

Connecting while the Workspace is stopping or starting shows its own overlay — wait
until it is running, then reconnect.

## Lifecycle and resource limits

- A visible browser connection keeps the Workspace warm.
- When the pane is hidden or the Console's browser tab goes to the background,
  rendering stops. The Page is held for 60 seconds and then released. On return you
  get the same Page if it was held, or a new one built from the saved port and path.
- Removing the pane's identity from the layout releases the Page. Where the identity
  survives — closing the last pane back to an empty view — the 60-second rule applies
  instead, and the Page still counts against the limit during that grace period.
- A Workspace stop discards the temporary Page but keeps the port and path in the
  layout; after start, a visible pane rebuilds itself from the same target.
- Ceilings: one Chromium process per Workspace, 2 concurrent Pages, viewport at most
  `1600×1200` (DPR 1), at most 12 fps while visible (JPEG quality 70). Not built for
  video or high-frame-rate checking.

## Scope and known limits

- **This is not a general browser.** Top-level navigation is restricted to loopback
  HTTP(S) and redirects to the outside are stopped.
- The Console drawer keeps at most 200 console messages and uncaught errors for the
  connected Page, surfacing `error` and `warn` first. It is not a persistent log and
  not a substitute for DevTools — there is no DOM, Network, Sources or Storage.
- Upload and download, clipboard, drag and drop, audio, video, WebRTC, permission
  prompts and multiple tabs are all out of scope.
- **Smartphones cannot start this flow.** At a 390×844 viewport the `⋯` in the
  workspace action bar overflows and overlaps other controls, so it cannot be tapped.
  Everything after that point works — the toolbar, tapping the canvas, Japanese input,
  the Console drawer — but there is no way in, so do not tell a phone user to open the
  pane. Desktop and tablet only.

## For agents working inside a Workspace

You have no tool that opens, drives or sees this pane; it belongs to the person. Tell
them the exact port and path and point them at Preview → open in pane. Never claim a
UI "looks right" on the basis of a pane you cannot see — if you need to verify
something yourself, drive your own headless Chromium and say that is what you did.

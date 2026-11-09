---
audience: "anyone whose web app started in the workspace does not show in the browser pane or a preview"
source_of_truth: "the Console's browser pane and preview popover, and the error pages the preview serves"
---

# Fix a preview or browser pane that does not show

English | [日本語](preview.ja.md)

Find the cause and the fix when an app does not show, does not update, or will not sign in on the browser pane, the lightweight preview or a preview subdomain.
How to use each of them is in [Preview and browser](../../member/14-preview-browser.md).

## The browser pane shows `target-unreachable`

The browser started, but the port and path do not answer yet. It also shows while a dev server is still starting.

1. Check the port number and the path
2. Press **"Reload"**
3. If it stays, press **"Reconnect"**

An app listening on `127.0.0.1` (localhost) is reachable. The pane's other states are in [Preview and browser](../../member/14-preview-browser.md#status-display-and-recovery).

## The lightweight preview or a preview subdomain shows a 502 page

The page says what happened. It follows the browser's language (Japanese or English).

| The page says | What to do |
|---|---|
| "Nothing is listening on port N inside the workspace. Start the app and reload." | Start the app, then reload |
| "Port N is listening but does not speak HTTP — …" | Databases, SSH and the like cannot be previewed. The preview carries HTTP and WebSocket only |
| "Port N is serving HTTPS — …" | Turn off the dev server's HTTPS |
| "The workspace is not running or cannot be reached." | Start the workspace |

The browser pane connects to the app directly, so it never shows this page. Its failure shows as `target-unreachable` in the pane.

## The page is blank or 404

This usually happens when an app that loads files by absolute path (such as `/static/...`) is opened in the **lightweight preview**, whose URL is a sub-path (`/preview/{port}/`).

- If preview subdomains are available, open it there
- If not, switch to the browser pane with **"Open in pane"**

How to fix it on the app's side is in [Preview and browser](../../member/14-preview-browser.md#viewing-a-web-app-you-started-browser-pane-and-lightweight-preview).

## The preview URL you gave a colleague returns 404

| Cause | What to do |
|---|---|
| A preview subdomain URL changes every time the workspace starts again | Hand out the link from **"Share"** in the preview popover. It keeps working across restarts |
| **"Show it to your tenant"** is off | Turn it on in ⚙ Settings → Preview subdomains. Turning it off hides the preview from their next request, even while they have it open |
| **"Shared with you"** on their side says **"Stopped"** | Your workspace is stopped. They cannot start it, so start it and tell them |
| You removed the port from the list in Settings | Put it back |

## It does not update, or only the frame shows with no data

| What you see | Cause and what to do |
|---|---|
| Next.js (15.2 and later) on a preview subdomain shows the layout frame but not a single row of data | The dev server refuses cross-origin access to `/_next/*` by default, and its log shows `⚠ Blocked cross-origin request to Next.js dev resource`. Add a wildcard to `allowedDevOrigins` in `next.config.ts` |
| The page reloads itself instead of updating in place | A WebSocket with no traffic was closed partway |
| HMR does not connect | The dev server may embed its own port into the client. Add a setting such as `server.hmr.clientPort: 443` |

The lightweight preview and the preview subdomains carry WebSocket and SSE. How to write the settings is in [Preview and browser](../../member/14-preview-browser.md#what-the-app-has-to-get-right).

To avoid adding settings, or to narrow the cause down, use **"Open in pane"**. In the browser pane a browser inside the workspace opens `127.0.0.1` directly, so HMR, WebSocket and SSE behave as on plain localhost.

## Every API call in the preview returns 401 (`preview requires sign-in`)

- **Check that you did not point the dev server's proxy at the preview URL.** Keep Vite's `server.proxy` `target` and Next.js's `rewrites()` `destination` at `http://127.0.0.1:8080`. The proxy call leaves from inside the workspace and carries no browser sign-in
- **If the browser calls the 8080 URL directly**, each port needs its own sign-in. Open `https://<random>-8080.…` in a tab once and sign in there. If the page on 3000 calls 8080, also turn on **"Allow calls between ports"** in ⚙ Settings → Preview subdomains (off by default)

## The browser pane shows `crashed` or `disconnected`, or keeps dying

| State | What to do |
|---|---|
| `disconnected` | Usually just a dropped connection. Check that the workspace is running and press **"Reconnect"** |
| `crashed` | The browser inside the workspace died. **"Reconnect"** opens it again |
| It dies again and again within a short time | Memory is short. Cut down heavy builds, watchers and browser panes left open (two at a time at most) |

You can check memory in [See your machine and its usage](../workspace/machine.md#see-the-usage).

## Signing in on a preview URL shows an Agent Fleet error page

These pages appear when your app uses the development sign-in (OIDC). How to use it is in [Preview and browser](../../member/14-preview-browser.md#signing-in-with-google-on-a-preview-url-development-oidc-idp).

| Page | Cause and what to do |
|---|---|
| "Can't start the sign-in" | The request was refused. Check that the client ID and callback URL set in the app belong to this workspace, then sign in again from the app |
| "Callback address not allowed" | The callback URL is neither this workspace's current preview URL nor `http://localhost:<port>`. Fix it in the app and sign in again |
| "Sign-in confirmation is no longer valid" | The confirmation was refused, expired, or already used. Sign in again from the app |
| "Can't complete the sign-in" | A problem on the server. Try again in a moment, and if it keeps failing, contact the deployment's administrator |
| "Sign in from your app" | You opened one step of the sign-in on its own. Start from the app you are previewing |

## A preview URL says the port changed, too many ports are open, or a restart is needed

These messages come from the preview, not from your app.

| Message | What to do |
|---|---|
| "The listener on port N changed or stopped. Reopen the port from the Console." | The server on that port restarted or was replaced. Open it again from the Console's port chip or Preview |
| "Too many temporarily opened ports. Close a server or add the port to the preview ports in Settings." | Stop servers you no longer need, or register the port in ⚙ Settings → Preview subdomains |
| "Opening port N is not available until the workspace is restarted (its agent is older than this feature)." | Restart the workspace |

## Related

- [Preview and browser](../../member/14-preview-browser.md)
- [See your machine and its usage](../workspace/machine.md)

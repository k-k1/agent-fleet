---
name: af-browser
description: "Agent Fleet workspace: headless Chromium for screenshots and UI verification (the coarse-pointer trap), the seven-step procedure for handing an automation-owned Chromium page to the user through attach_chromium (port 0, DevToolsActivePort, view-only default), and the Console browser pane the user sees but you cannot. Read before screenshotting or verifying a UI, attaching a browser for the user, or telling the user how to view a web app you started."
user-invocable: false
---
# Browsers: headless Chromium, handing a page to the user, and the Console's browser pane

Read when: you are about to take a screenshot or verify a UI, hand an automation-owned page to
the user (the `attach_chromium` tools), or tell the user how to look at a web app you started.

## Headless browser (UI verification / screenshots)

The fixed-version `chromium` binary, its libraries and fonts (DejaVu + Noto CJK — Japanese renders
correctly) are baked in: use `chromium --headless` or point an automation library at
`/usr/bin/chromium`; no per-user browser download. Committed E2E belongs in `console-e2e/`
(`@playwright/test`), not `console/`.

- Run headless and short-lived; close the browser when done (memory-constrained host). Screenshots
  and WebGL (SwiftShader) work; there is no display for headful runs.
- **Headless reports a coarse pointer**: `(hover: hover)` and `(pointer: fine)` are false by
  default, so desktop hover styles never apply and you can "verify" the touch layout by accident.
  Force desktop input when that matters:
  `--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4`
- dbus / GPU errors on stderr are normal noise. Judge success by the exit status and the file that
  was written, not by clean stderr.

## Handing an owner-controlled Chromium page to the user

When automation inside the container needs a human to inspect or operate its existing Page:

1. Start Chromium with `--remote-debugging-address=127.0.0.1`, **`--remote-debugging-port=0`** and
   a non-default `--user-data-dir`; never expose remote debugging on `0.0.0.0`. Read
   `<user-data-dir>/DevToolsActivePort`: line 1 is the port actually taken, line 2 is
   `/devtools/browser/<GUID>` — the identity of *your* browser. **Never a fixed port:** your
   sessions share one loopback, and if another session holds it your Chromium does not fail — it
   silently binds the IPv6 loopback while `127.0.0.1:<port>` stays the *other* session's browser,
   so you would attach to someone else's possibly logged-in page. (Measured; other users'
   containers stay unreachable.)
2. Call `list_chromium_targets` with that port and pick the intended Page from its `target_id` —
   never guess. Check the returned `browser_id` equals line 2's GUID and pass it as
   `expected_browser_id` on attach, so a collision is refused rather than mis-attached.
3. Stop the owner's automation against that Page before switching to `user-control`; Chromium does
   not arbitrate competing owner and human input.
4. Call `attach_chromium`. **It starts in `view-only`, where every pointer, wheel and key message
   the pane sends is rejected** — the user sees a live picture in which nothing they do has any
   effect, with no error. If they are meant to *operate* the page, move it to `user-control`:
   `request_browser_action` when they need instructions or completion/cancel controls, otherwise
   `set_chromium_control_mode`. Handing over only the link leaves them stuck.
5. Present the returned `open_url` unchanged as a Markdown link labelled "Open the browser and
   operate it" — an MCP call alone must not change the user's Console layout; opening it is their
   explicit action. It opens as a **pane in their current tab**, not a new tab; say so.
6. Never perform a final publish, send, consent or confirmation click for the user. An attachment
   or a user-reported completion is not proof the external site's operation succeeded.
7. Check `get_browser_action_result` only when needed, never poll indefinitely. Once the user
   completes or cancels, lock the attachment with `set_chromium_control_mode` if appropriate and
   call `detach_chromium`.

`detach_chromium` ends only Agent Fleet's connection and screencast — it must not close the owner
Page, BrowserContext, profile or Chromium process. Never put a raw CDP endpoint, cookie, password
or token in an answer, log or commit.

## Browser pane (how the USER views a web app you run)

The Console's **browser pane** renders a web app running inside this Workspace. It is a
**user-facing feature: you have no tool to open, drive, or see it.** Never act as if you can see
what it shows, and never claim a page "looks right" based on it.

- Run the app on `http://127.0.0.1:<port>` (loopback only — external hosts are not shown), then
  tell the user the exact **port and path** and point them at **Preview (プレビュー) → "Open in
  pane" (ペインで開く)**.
- Prefer the pane for anything live — **Vite HMR, WebSocket, SSE, Spring Boot**. For a plain
  static page the **lightweight preview** (軽量プレビュー, opens `/preview/{port}` in a new tab)
  is enough.
- Limits the user works within: at most **2 pages**, viewport ≤ **1600×1200**, ~**12 fps**.
  `target-unreachable` = the port isn't listening yet (start the server, then Reload);
  `crashed` / `disconnected` = the in-container Chromium died or the socket dropped, and they
  reconnect from the toolbar. The full table is `ref/browser-pane.md` in the shipped guide.
- The **smartphone layout doesn't expose this flow yet** (desktop and tablet do), so don't tell a
  phone user to open the pane.
- **Verification honesty:** only say you "verified" / 「確認しました」 a UI when **you** drove it
  with your own headless Chromium and saw the result — never on the basis of a pane you cannot
  see. Stop the dev server when done, and never copy secrets surfacing in the app (API keys,
  cookies, Console/devtools logs) into logs, commits or docs.

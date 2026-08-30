# 0018. The in-container browser pane — run Chromium inside the workspace and relay only the picture

English | [日本語](0018-container-browser-pane.ja.md)

- Status: adopted; MVP implemented (2026-07-18, verified in the W5 integration)
- See also: [31-container-browser-pane.md](../log/31-container-browser-pane.md) (the implementation contract and the staged plan) /
  [31-container-browser-pane-ux-contract.md](../log/31-container-browser-pane-ux-contract.md) (the usage contract) /
  [build/05 §5.3](../build/05-api.md) (the existing port preview) /
  [0007-opencode-web-via-pk-webui.md](0007-opencode-web-via-pk-webui.md) (the known limits of the sub-path approach)

## Context

The existing port preview opens `/preview/{port}/` from the Console in a new tab and relays HTTP
from the CP through the Workspace Agent to `127.0.0.1:{port}` inside the container. That is
enough for a simple page, but for checking a Node or Spring Boot app under development it runs
into:

- The app does not know about the `/preview/{port}/` sub-path. `/assets/*`, redirects, cookie
  paths and clients that assume `location.origin` all break.
- The preview is HTTP only; it does not relay Vite's HMR WebSocket or SSE.
- An iframe on the same origin as the Console hands an arbitrary app under development the
  boundary of the Console's DOM, API and service worker. Tightening the sandbox instead breaks
  fetch/storage for ordinary SPAs.
- A dedicated subdomain is a strong option at a production entrance such as AWS, but bringing
  wildcard DNS, TLS and Host routing into today's local/standalone-Docker setup first is far too
  much for the goal.

What is actually needed is not "expose a container port to an external browser" but **open
localhost faithfully in the container's own browser and operate that picture in a Console pane**.

## Decision

1. Bake Chromium into the workspace image; the Workspace Agent starts it lazily and supervises
   it. At most one browser process per workspace, with an independent BrowserContext + Page per
   browser pane.
2. Chromium opens `http://127.0.0.1:{port}{path}` directly. The target app's HTTP / WS / SSE /
   cookies stay inside the container's loopback; the existing `/preview/{port}` is not involved.
3. The Agent sends a Chrome DevTools Protocol (CDP) screencast to the Console, newest frame
   first, and accepts viewport, mouse, wheel, keyboard, IME-committed text and navigation
   operations from it.
4. Console ↔ CP ↔ Agent is relayed over one authenticated WebSocket. The CP interprets neither
   frames nor input; it relays bidirectionally at the same membership / workspace resolution
   boundary as the existing terminal WS. Raw CDP is not exposed.
5. `PaneContent` persists only `{kind:"browser", port, path}`. The `browserId` the Agent returns
   is ephemeral and is not saved into the layout or localStorage. The Console's BrowserService
   owns the Page and the socket keyed by `paneId` and destroys them when the pane goes away.
6. Navigation is restricted to loopback HTTP(S) initially. `file:`, `chrome:`, `data:`, the
   host's internal metadata endpoint, raw CDP, arbitrary file selection and downloads are not
   allowed. Opening an external URL in a normal browser is offered as a separate, explicit
   action.
7. The existing port preview stays as the lightweight new-tab route. Even if a dedicated preview
   origin is set up at the AWS entrance in future, the browser pane remains as the foundation for
   visual verification, automation and screenshots by agents.

### How Chromium is distributed (the W1 spike)

The workspace image uses Debian bookworm-security's `chromium` rather than the Playwright
distribution. The `CHROMIUM_VERSION` build ARG pins it down to the Debian revision, and
`chromium`, `chromium-common` and `chromium-sandbox` are installed at the same version. On both
amd64 and arm64 the executable is `/usr/bin/chromium`. To update, confirm the same version has
been published for both architectures, raise the ARG, and pass the image smoke test.

The comparison, 2026-07-18:

| Approach | multi-arch / executable | Rough distribution size | Update and implementation properties |
|---|---|---|---|
| Debian package (adopted) | bookworm amd64 / arm64, `/usr/bin/chromium` | totals for browser/common/sandbox at 150.0.7871.124: amd64 98.8 MiB download, 333.2 MiB installed; arm64 93.9 MiB download, 338.0 MiB installed | apt resolves the OS dependencies and the sandbox, and the Agent can start CDP directly from a fixed path |
| Playwright's Chromium | supports Debian 12 x86-64 / arm64, versioned paths under a cache | the official material's Chromium example is about 281 MiB (unpacked; varies by version) | needs the Playwright version and the browser revision kept in sync, plus separate normalisation from the cache layout to a fixed executable name |

The Debian "installed" figures come from the package metadata; they are not the final image delta,
which shares libraries with the existing image and is compressed into Docker layers. The real
image increment and runtime memory are settled by the W5 multi-arch build and measurement on real
hardware. The W1 image smoke test verifies the package revision, `chromium --version`, the setuid
helper at `root:root 4755`, that there is no other setuid/setgid binary, `NoNewPrivs=0`, that
`dev(1000)` has no `SYS_ADMIN` in its effective set while the helper has it in its bounding set,
a headless start with the sandbox enabled, and a screenshot of a fixed page that uses Japanese
fonts. It then starts the product Agent's pipe-CDP path under the same `--cap-add=SYS_ADMIN` as
the product Docker runtime and renders two Pages simultaneously. Both product and smoke use
`--disable-dev-shm-usage`, so neither depends on Docker/ECS's small `/dev/shm`, nor on a network
install after the container starts.

## Options rejected

- **Putting the current `/preview/{port}` in an iframe as is**: same-origin privileges are far
  too strong. A strict sandbox breaks an SPA's networking and storage; a loose one fails to
  isolate the Console from arbitrary code.
- **Extending the generic proxy to rewrite WS/SSE/HTML**: worth doing, but it cannot fully absorb
  the root path, cookies, service workers, CSP and app-specific redirects. Not the main route for
  checking an unmodified development server.
- **A Chromium process per pane**: simple isolation, but multiple processes are heavy in a
  memory-constrained workspace. BrowserContext is not treated as a security boundary — it is
  state separation within one user's workspace.
- **Xvfb + noVNC**: it can show the browser chrome and DevTools, but transport, input and
  resolution management are heavy, and it is overkill for showing a web app in a pane. Held back
  until there is a genuine "full remote desktop" requirement.
- **Relaying raw CDP to the Console**: the Console would hold strong browser control, and the
  protocol compatibility, authorisation and secret-exposure surfaces all grow. Only the
  high-level operations the Agent permits are exposed.

## Consequences

- The workspace image gets bigger, and keeping Chromium updated against vulnerabilities joins the
  release work.
- Screen transport uses more bandwidth and CPU than the terminal, so the number of simultaneous
  Pages, the resolution, the fps and the JPEG quality have to be limited, and hidden panes have
  to be stopped.
- It is not fully local-browser-compatible. The MVP is limited to display, ordinary input,
  navigation and the Console log; upload/download, clipboard, audio, video, DevTools and multiple
  tabs come later.
- A Page is ephemeral, so after a workspace Stop, an Agent restart or a Chromium crash it is
  recreated from the same port/path. Persisting the web app's own state is left to that app.

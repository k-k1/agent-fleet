# 0007. Serve opencode web through pk-opencode-webui

English | [日本語](0007-opencode-web-via-pk-webui.ja.md)

- Status: **withdrawn (2026-07)** — the implementation (baking in pk-opencode-webui,
  `opencode serve` + `bun serve-ui.ts`, the `/ocweb` proxy, the Console toggle) was removed in
  `temp/remove-opencode-web-opencode-web-ui`. opencode is used through the TUI in tmux (CLI)
  only. What follows is kept as the design record of the time.
- See also: [reference/preview.md](../build/05-api.md) (the same proxy mechanism and WS constraints) / [HANDOFF §opencode](../HANDOFF.md) / it would have sat next to the rtk toggle ([the agent settings tab](../HANDOFF.md))

## Context

The request was to use opencode not only through the TUI in tmux (bridged PTY/xterm) but also
through **opencode's own web UI** (`opencode web`). But core opencode web **assumes it is at the
root `/`** and does not work under agent-fleet's sub-path preview
(`{extPrefix}/preview/{port}/`, `reference/preview.md`). Confirmed on a real machine:

- The HTML from `opencode web` emits **absolute root paths** such as `src="/assets/index-*.js"`.
- At runtime the API/SSE/WS base is
  `location.hostname.includes("opencode.ai") ? "http://localhost:4096" : location.origin` —
  i.e. **`location.origin`, with no path**. Code-split chunks are absolute too, from Vite's
  `base=/`.
- → Placed under a sub-path, the browser fetches `/assets/...` and `/event` from **the Console
  origin's root**, which collides and breaks.

The upstream documentation says as much: "the official OpenCode web UI assumes it runs at root
path `/`".

## Options considered

| Option | Feasible | Why rejected |
|---|---|---|
| **A. Core web + rewrite in the proxy** | ❌ | The assets can be made relative, but the runtime `location.origin` base for API/SSE/WS and Vite's absolute chunks are **build-time constants**. Fixing them in the proxy means regex surgery on a minified bundle that changes every release — brittle and unmaintainable |
| **B. Per-host/subdomain preview** | ✅ (works with core) | Put opencode web at the root of some origin and it runs unmodified. But agent-fleet would need **wildcard DNS + TLS + Host-based routing** (a sizeable piece of new infrastructure) |
| **C. Put pk-opencode-webui in front** | ✅ | **Adopted** (below) |

## Decision

**Adopt C** — put [`prokube/pk-opencode-webui`](https://github.com/prokube/pk-opencode-webui)
(MIT; a **prefix-aware reimplementation of the web UI** in Bun/SolidJS) in front of
`opencode serve`. A spike confirmed the crux:

- Started with `BASE_PATH=/preview/9999/`, the HTML it emits uses **relative URLs
  (`./entry.js`) plus `<base href="/preview/9999/">`** — the most robust way to follow a prefix.
  It actually solves the base-path problem core could not.
- The bundled `docker/serve-ui.ts` (Bun.serve) serves dist, proxies the API (`API_URL`) and
  **relays WS upgrades internally** (browser ⟷ pk-webui ⟷ `opencode serve`).

### Attendant decisions

1. **A dedicated endpoint `/ocweb/` (do not reuse `/preview/{port}`).** Reason: the existing
   preview **strips `/proxy/{port}` and forwards at the root** (`reference/preview.md`), whereas
   a prefix-aware UI needs **the external path passed through unchanged** — the semantics are
   inverted. The port stays out of the URL too (it is one resident service per workspace, so the
   agent resolves the internal port). `BASE_PATH = {extPrefix}/ocweb/`.
2. **One per workspace** (not per session). `opencode serve` is a server holding several
   sessions and pk-webui is a multi-project UI, so this is the natural shape. It coexists with
   the existing opencode tmux slots as a separate lineage.
3. **Bake it into the image.** bun **cannot come from apt** (there is no official .deb), so the
   runtime is `npm install -g bun` (put on the same line as the existing
   `npm i -g claude/opencode/codex`, which is the most consistent place), and dist is built in a
   multi-stage `FROM oven/bun:* AS builder`. serve-ui.ts depends on `Bun.serve` and so does not
   run under node — **the bun runtime is required**.

## Architecture (four layers, implemented in stages)

```
browser {origin}{extPrefix}/ocweb/<path>
  → CP   /ocweb/<path>            control-plane: a dedicated handler (rtFor auth + Bearer + X-Forwarded-* + WS)
                                   note: the path is NOT stripped — /ocweb/<path> goes to the agent as is
  → Agent /ocweb/<path>           workspace/agent: httputil.ReverseProxy (WS capable) to 127.0.0.1:<pkPort>/ocweb/<path>
  → pk-webui :<pkPort>            BASE_PATH={extPrefix}/ocweb/ · API_URL=http://127.0.0.1:4096
  → opencode serve :4096          the headless API (127.0.0.1)
```

| # | Layer | Work |
|---|---|---|
| 1 | image | bun (`npm i -g bun`) + a multi-stage build of pk-webui → bake into `/opt/opencode-web` (dist + serve-ui.ts + shared) |
| 2 | agent | opencode web lifecycle (`opencode serve` + `bun serve-ui.ts`); a persistent toggle in `~/.config/agent-fleet/opencode-web.json` (off by default); `GET/PUT /agents/opencode-web` (state + internal port); a reverse proxy for `/ocweb/<rest>` |
| 3 | CP | a dedicated `/ocweb/<rest>` handler (**WS capable**, path preserving, passing `externalPrefix` through so BASE_PATH can be computed) |
| 4 | Console | in the opencode section of the "agents" tab: on/off plus "open opencode web" (`/ocweb/` in a new tab) |

## Consequences and risks

- **WS support in the CP** is added for `/ocweb` specifically, but it is also a step towards
  lifting the "no WS/SSE" constraint in `reference/preview.md` (the same mechanism could be
  widened to the generic preview).
- **Feature drift in a third-party UI**: pk-webui is a reimplementation of the official UI, so
  there is a feature gap and a cost to keeping up (MIT, maintained, v0.9.2/2026-05). Vendor it
  at a pinned commit and update deliberately.
- **Image bloat**: the bun runtime plus roughly 30 MB of dist (the Shiki language chunks).
- **`opencode serve` is unsecured by default** (it warns that `OPENCODE_SERVER_PASSWORD` is
  unset). It binds 127.0.0.1 and is only reachable through the authenticated `/ocweb` preview,
  so it is not exposed — and no extra port is published.
- **`extPrefix≠""` (deployments where Caddy or similar strips a sub-path) is follow-up work.**
  The current deployment serves the CP at the root (`PUBLIC_BASE_URL` has no path ⇒
  extPrefix=""), so `BASE_PATH=/ocweb/` holds.

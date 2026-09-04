---
audience: "someone changing the Console — the browser side"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-07"
---

# 02. Console (React + Vite + zustand)

English | [日本語](02-console.ja.md)

## 2.1 Stack and design principles

A React 19 + Vite 6 + TypeScript + zustand 5 SPA. The CP serves the built bundle
statically ([05 §5.4](05-api.md)); it talks to the backend over REST, SSE, and two
WebSockets (terminal and browser). A full rebuild at feature parity in 2026-07 removed
the God-context structure — the reasoning is
[decisions/0011](../decisions/0011-console-rebuild.md). The principles:

- **A store per domain, subscribed by selector.** No single context, no `bump*()`
  counters, no ref mirrors (§2.3).
- **Layout arithmetic is pure functions** (`console/src/layout/`). Side effects —
  persistence, history, xterm — belong to stores and services (§2.4).
- **Cohesion by feature**: endpoint calls, state, UI and CSS live together under
  `features/<x>/`. CSS is co-located plain CSS (no CSS Modules; collisions are avoided
  by a class-prefix convention).
- **StrictMode-proof**: no module-level mutable singletons, no wired-once flags. Every
  `wire*()` returns its unsubscribe.
- **Every URL is relative to `document.baseURI`.** Absolute paths are forbidden — this
  runs behind a path-stripping proxy.

## 2.2 Directory responsibilities (`console/src/`)

| Directory | Responsibility |
|---|---|
| `app/` | The shell and the two top bars, plus the viewport. Owns the boot order (resolve tenant → restore that tenant's layout → UI preferences → start polling) and the wiring of history, drawer and notifications |
| `core/api/` | `client.ts`, the single point that wraps `fetch` (§2.3). Each feature re-exports only its own slice |
| `core/store/` | The foundation stores: tenant (selection, memberships) and workspace (state machine plus start/stop) |
| `layout/` | The pure-function pane layout engine (types / ops / migrate) plus the layout store. Covered by vitest (§2.4) |
| `terminal/` | `term.ts`, where all the xterm knowledge lives, plus `service.ts` — the only entry point to xterm |
| `ui/` | Primitives: Button, Modal, Section, Icon, FileIcon, Toast, Confirm, EmptyState, Sparkline… |
| `features/*` | The 19 features (below) |
| `styles/` | `tokens.css`, the only home for theme variables, plus a reset |
| `agents/` | `registry.ts`, the single source of truth for agent kinds (§2.4) |
| `lib/` | Pure logic and small hooks: the commit-graph lane DAG, file icons and metadata, terminal colour, project grouping, settings sync. Where testable functions go |
| `types/` | Cross-cutting domain types |

| Feature | Role |
|---|---|
| `panes` | The pane host (flat-absolute rendering, drag-swap, drop-to-split), the pane, and the layout map |
| `terminal` | The terminal view (a resident PTY), the mobile key row, the onboarding card |
| `browser` | The browser pane (canvas, toolbar, IME), its controller and registry keyed by pane id |
| `sessions` | Session rows, the create / rename / archive modals, the action hooks, the 4-second polling store, browser notifications on state change |
| `repos` | Clone / launch / branch modals, the repo and directory pickers, the repo store |
| `project` | The left pane's working-copy tree: base clone plus worktrees grouped per project, with sessions and files nested underneath. Also the home for sessions that belong to no repository |
| `files` | Shared file-tree state and its refresh signal |
| `scm` | Source control, changes, commit detail, working diff, commit graph, git diff |
| `viewer` | File, code, Markdown (with mermaid), Marp, image, diff and document viewers |
| `editor` | CodeMirror-based editing: dirty tracking, following external changes, fetching and applying AI suggestions |
| `mirror` | The chat mirror of a session's transcript, and the context bar |
| `chat` | The assistant chat (a headless CLI conversation, streamed over SSE) and its left-pane section |
| `memo` | The memo queue: its section, the AI organise modal, sending a selection |
| `schedules` | The scheduled-run section and its detail modal |
| `keys` | The keyboard system: a capture-phase dispatcher, the command palette, which-key and cheat sheet, rebinding |
| `notifications` | The notification centre: unread state, the toast log, the sound toggle |
| `usage` | The per-feature token usage dashboard |
| `auth` | The re-login modal shown when the session expires |
| `settings` | The settings dialog (three groups × 24 tabs, §2.5), the admin dialog, connection-state polling |

## 2.3 State and server sync

- The stores are split per domain, and **selector subscription is what structurally
  prevents "a 4-second poll re-renders the whole screen"**. Code outside React reaches
  them through `getState()` / `setState()`.
- Stores talk to each other by subscription. For example the shell refreshes repos,
  sessions, files and chat on the workspace's stopped ↔ running **transition edge**,
  ignoring the indeterminate states in between.
- `features/files/sessionRefresh.ts` is the same shape over the session list: on a
  session's **busy → not-busy edge** (working/compacting or backgroundBusy clearing —
  the end of a turn) it fires a *scoped* files refresh, `refreshUnder("repos/<copy>")`.
  The tree re-reads only the directories on screen under that prefix, and the changes
  view swaps its list without blanking it — this is what stops files an agent created
  or deleted from staying invisible until someone finds the refresh button. The session
  list arrives over push/poll anyway, so the trigger costs no extra traffic; firings
  coalesce per working copy and keep a 3-second minimum gap. ★ A failed re-read (5xx,
  dropped fetch) MUST be swallowed and the current rows kept: writing the failure back
  as an empty listing would empty the tree at the end of every turn.
- Polling: workspace every 4 s, sessions every 4 s, repos every 60 s, resource stats
  every 4 s. A trailing `…` marks an optimistic in-flight state, which both the buttons
  and the poller treat as busy and leave alone.
- `core/api/client.ts` wraps `window.fetch` and injects the tenant header on every
  request (WebSockets, new tabs and downloads fall back to a query parameter —
  [05 §5.4](05-api.md)). A 401 redirects to the login landing exactly once. Error-code
  translation and the SSE, multipart and download helpers are all here. Long operations
  are run synchronously and polled; there is no job queue.

## 2.4 Panes, layout and the terminal service

- A layout is at most 4 columns of 1–2 panes each. `Pane.content` is a discriminated
  union. **`session` lives on the pane, not on the content**: switching views keeps the
  PTY socket and the scrollback alive but hidden, so going back to the terminal shows
  the same session.
- **The pane-id contract is a hard invariant.** A pane's id *is* the terminal's
  identity: the xterm instance, the WebSocket and the DOM node are keyed by it.
  Swapping and drop-splitting move a pane **keeping the same id**; renumbering or
  duplicating is forbidden, because a new id builds a new xterm and a new WebGL
  context, and **the terminal you just moved comes up blank**. The pure functions in
  `layout/ops.ts` and their tests enforce this.
- `layout/ops.ts` is `Layout in → Layout out`. A no-op returns the input by reference,
  so the caller can skip the commit on `next === cur`. The layout store's `commit()` is
  **the only path that mutates**, and it does the state-only history push (the URL never
  changes) and the per-tenant persistence.
- **Tab order is most-recently-used.** `lastUsedAt` is not only for LRU eviction; it
  decides **what to show when the visible tab goes away**. Closing, moving or detaching
  all select the remaining tab you looked at last — open a file from the mirror, close
  it, and you are back in the mirror. Stamps are strictly monotonic within a page
  session so two touches in the same millisecond cannot tie.
- **The terminal service is the only entry point to xterm.** One subscription to the
  layout store reconciles "dispose the terminal of a pane that left the layout". The
  contents of `term.ts` are hard-won domain knowledge and should be treated as
  untouchable: zombie-socket detection via a heartbeat (text frames are out-of-band
  control, binary is PTY output), WebGL rendering with context-loss recovery, keyboard
  lock while focused, clipboard integration that copies on drag-select, and soft-keyboard
  fitting. The DOM container is resident — toggled hidden, never re-parented, which is
  what the flat-absolute pane host requires.
- The browser registry is keyed by pane id too and owns the page, socket and canvas.
  **Only `{kind, port, path}` is persisted**; the ephemeral browser id is not. Hiding
  sets visibility false, the page is destroyed after 60 seconds, and showing it again —
  or a reload, or a workspace restart — rebuilds it from the port and path.
- `agents/registry.ts` is the **single source of truth for the nine kinds**. Each has
  one descriptor (display, an availability predicate, a capability set), and the UI
  branches on capabilities. **Adding an agent means adding a descriptor.**
- **Display names come in three widths**, and the internal identifiers are immutable
  lowercase: a two-letter `short` for cramped badges, a compact `label` for pane headers
  and session rows, and a full `displayName` for launch and settings cards. Write
  display code through the helpers, never by reading a raw label or hard-coding a name.

## 2.5 Information architecture

- **Two bars.** The top one holds the app name, the tenant picker (hidden when you
  belong to one), the appearance popover, the account menu, settings, and — for a
  deployment administrator only — admin. Below it, the workspace bar holds state and
  Start/Stop, the resource chip, port preview, the per-agent usage chips, and the split
  controls.
- **Left pane**: the layout map, three resident sections (assistants, memo queue,
  project tree), and a catch-all for sessions outside any repository. The flat
  Sessions / Repos / Files sections were folded into the project tree — a project-first
  IA.
- **Main area**: the pane host.
- **History navigation** pushes the layout into `history.state` **without changing the
  URL** (a path-stripping proxy makes URL paths unusable). Back and forward restore the
  layout and the mobile drawer. "Back closes the modal" belongs to the shared modal
  layer, and drill-downs stack on it.
- **A horizontal swipe on a phone rotates through running sessions** when the drawer is
  closed. The order is the sessions list as returned, filtered by the working set — so
  it matches what the left pane shows. A swipe starting at the left edge yields to the
  drawer. Swipes are skipped when they start on a surface that has its own horizontal
  gesture.

  > **This is where a subtle bug lived, and the fix is worth understanding.** The
  > horizontal-scroll test cannot use the computed value of `overflow-x`: CSS resolves
  > `visible` to `auto` when the other axis is not visible, so a purely vertical
  > scroller reads as `auto` too. One unbreakable string in a transcript — a digest, a
  > URL with a query — pushed the mirror body sideways and **killed swiping for that
  > one session entirely**: the container is an ancestor, so every touch was rejected,
  > and scrolling the offending line off screen did not shrink `scrollWidth`. The fix
  > is two-sided: stop producing the overflow (`overflow-wrap: anywhere` in the
  > transcript), and declare the surface as vertical-only, so horizontal overflow there
  > is by definition an accident. **The test itself was not loosened**, so genuinely
  > bidirectional surfaces like code and diff views are unaffected.

- **The settings dialog** is a three-group rail × 24 tabs (the old single row of six did
  not scale; mobile drills rail → content). **Two of them appear only where the capability
  exists**: cloud cost on a deployment with an AWS bill, preview subdomains on one that
  issues them — a tab whose every control would be inert is not shown at all. Admin
  functions are **not** mixed in — they are a separate dialog, reachable only by a
  deployment administrator.
- **The admin and tenant-settings dialogs share one shell and one rail.** The admin
  rail has two levels, and opening a tenant swaps the whole rail into that tenant.
  **One tenant's surface is a single component both dialogs point at** — it is the same
  tenant seen from two entrances, so the IA is not duplicated. Which one you get is
  decided by a server-provided flag alone.

## 2.6 The display system

- **Theme**: `styles/tokens.css` is the only home for the variables. A helper writes
  `data-theme` and the region variables; surface colours are tinted per theme so a light
  theme does not end up with unreadable dark bars. **Known limit: xterm has no light
  theme** — the terminal stays dark even in light mode.
- **Agent kind colours** come from `--kind-*` in `tokens.css`, which is the **only hue
  source**. Consumers use `var(--kind-*)` and `color-mix(…)` for tints; **no CSS file
  contains a colour literal for a kind.**
- **Icons split by role**: chrome is monochrome and follows `currentColor`; file types
  are coloured SVGs resolved by extension.
- **UI preferences are stored per user on the server.** localStorage is the immediate
  cache, a debounced PUT persists, and boot merges **server-wins** — so your settings
  follow you to another browser or device, and still work if the fetch fails.
- **Phones are for monitoring plus light operation.** Every branch is confined to one
  media query so the desktop DOM and CSS are untouched.

## 2.7 Build, and the hard constraints

- `vite build`; in development, `--watch` plus a browser reload — the CP does not need
  restarting ([10](10-development.md)). Mermaid and Marp are heap-hungry, so the Node
  heap is raised **per command**, not globally. Sourcemaps are off (generating them has
  overflowed the heap before).
- Mermaid and Marp are **lazily imported chunks**, kept out of the main bundle.
- **A Marp trap worth knowing**: even with maths disabled it *statically* requires
  MathJax (~43 MB) and KaTeX, so an untreated production build hangs during minify. An
  alias swaps in a stub to keep it out of the bundle. **This is a fixed constraint —
  removing the alias kills the build.**
- The CP serves the bundle with `Cache-Control: no-store`, so a deployment is live
  immediately ([05 §5.4](05-api.md)).
- **Tests** are vitest over pure logic only — layout operations, library functions,
  store transitions — capped at two workers for a shared host's memory. DOM and visual
  behaviour are verified by looking at a browser ([10](10-development.md)).

## 2.8 Known debt (no behavioural impact)

[decisions/0011](../decisions/0011-console-rebuild.md) carries the live status. In
short: the mirror view is still a faithful port and wants breaking up; the commit
graph, git diff and viewers are verbatim; extracted CSS has unused selectors to prune;
the legacy button compatibility shim wants folding into the UI primitive; the layout
persistence key keeps its old name (the older one is still read for migration); and
sending a launch prompt to a non-chat kind waits for the TUI to come alive, which is a
stopgap.

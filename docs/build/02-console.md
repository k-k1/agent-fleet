---
audience: "someone changing the Console — the browser side"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-09"
---

# 02. Console (React + Vite + zustand)

English | [日本語](02-console.ja.md)

## 2.1 Stack and design principles

A single-page app in React, Vite, TypeScript and zustand; the versions are the ones in
`console/package.json`. The CP serves the built bundle (§2.7). The Console talks to the
CP over:

- REST under `api/`;
- one SSE push stream, `api/events`, which carries the frequently changing state (§2.3);
- SSE responses for the assistant chat's turns and for the routes that hold a request
  open while a model answers (`fetchHeld` in `core/api/client.ts`);
- three WebSockets: `ws/terminal` (the PTY), `ws/browser` (the browser pane) and
  `ws/browser-attachments` (an attached Chromium view).

The route table and the wire rules belong to [05](05-api.md). The structure dates from a
rebuild at feature parity in 2026-07 that removed the God-context design; the reasoning
is [decisions/0011](../decisions/0011-console-rebuild.md). The principles:

- **A store per domain, subscribed by selector.** No single context, no `bump*()`
  counters, no ref mirrors (§2.3).
- **Layout arithmetic is pure functions** (`console/src/layout/`). Side effects —
  persistence, history, xterm — belong to stores and services (§2.4).
- **Cohesion by feature**: endpoint calls, state, UI and CSS live together under
  `features/<x>/`. CSS is co-located plain CSS (no CSS Modules; collisions are avoided
  by a class-prefix convention).
- **StrictMode-proof**: the app renders under `React.StrictMode`. Every `wire*()` /
  `start*()` returns its cleanup and is called from an effect, so the double mount
  leaves exactly one subscription. There are no wired-once flags.
- **Every URL is relative to `document.baseURI`** (`rel()` in `core/api/client.ts`;
  `base: "./"` in `vite.config.js`). Absolute paths are forbidden: the Console may run
  behind a path-stripping proxy, and `index.html` sets a `<base>` so that relative URLs
  resolve under the mount.

## 2.2 Where things live (`console/src/`)

| Directory | Responsibility |
|---|---|
| `app/` | `main.tsx` (the entry point), the shell (`App.tsx`), the top bar and the workspace bar, the working-set switcher, the viewport and the phone gestures. The shell owns the boot order: the wiring and the pollers start, then tenant → UI preferences → that tenant's layout |
| `core/api/` | `client.ts`: the one place that wraps `fetch` (§2.3) |
| `core/push/` | The push channel: `events.ts` is the transport, `wire.ts` applies each stream's frames to its store |
| `core/store/` | The foundation stores: tenant (selection, memberships, whoami), workspace (state machine plus start/stop), the left rail, the stats feed |
| `core/auth/` | Latches for "the login session expired" and "this tenant needs another sign-in method". They stay outside React so non-React code can trip them |
| `layout/` | The pure pane-layout engine (`types` / `ops` / `migrate`), the layout store, history, pop-out tabs (§2.4) |
| `terminal/` | `term.ts`, where all the xterm knowledge lives, and `service.ts`, the only entry point to it |
| `agents/` | `registry.ts`, one descriptor per session kind (§2.4) |
| `ui/` | Primitives, for example Button, Modal, Section, Icon, FileIcon, the toast and confirm providers, the model picker |
| `features/*` | One directory per feature (below) |
| `lib/` | Pure logic and small hooks, for example the commit-graph lanes, file icons and metadata, terminal tints, UI-preference sync (`settings.ts`), working sets. i18n lives in `lib/i18n/` (§2.8) |
| `styles/` | `tokens.css`, the only home for theme variables, plus `base.css` (the reset) |
| `types/` | Cross-cutting domain types, for example sessions, chat and memos |
| `test/` | The dom test setup, and static checks over the whole source tree (for example no raw control characters, no duplicate sibling keys) |

**`features/`** holds one directory per feature: its components, usually a `store.ts`,
often an `api.ts`, and its CSS. The list is `ls console/src/features`. What the product
offers, screen by screen, is [guide/ref/features.md](../../guide/ref/features.md); neither
is repeated here. To find your way in, the directories fall into a few kinds, for example:

- **What a pane shows**: `scm`, `viewer`, `editor`, `mirror` (the chat mirror of a
  session), `browser`, `overview` (the sessions overview,
  [ADR 0078](../decisions/0078-sessions-overview-pane.md)), `fleetgraph` (the fleet session
  graph, [ADR 0096](../decisions/0096-fleet-session-graph.md)), `gallery`
  ([ADR 0080](../decisions/0080-image-gallery-pane.md)), `imagegen` (the image-generation
  studio, [ADR 0081](../decisions/0081-image-generation-pane.md) and
  [ADR 0100](../decisions/0100-image-generation-studio.md)).
- **What the left pane shows**: `project` (the working-copy tree), `chat` (assistants),
  `memo`, `workitems`, `schedules`, `sharing`.
- **Bars, dialogs and cross-cutting systems**: `panes` (the pane host), `sessions`,
  `repos`, `settings`, `notifications`, `keys` (the keyboard system and command palette),
  `auth`, `engines` (the engine indicator), `usage`, `cost`.

Two conventions recur in them:

- **`open.ts` is apart from the view.** A pane-backed feature exports its `open*()` from
  its own module, because the keyboard command table imports it, and importing the view
  would drag its rendering and CSS into every bundle that has a menu.
- **`api.ts` builds on `core/api/client.ts`; shapes may sit in `wire.ts`.** `client.ts`
  reads `localStorage` and replaces `window.fetch` when it is imported, so it does not
  load in the node test project. A pure module that needs only the types imports
  `wire.ts`.

## 2.3 State and server sync

- The stores are split per domain, and **selector subscription is what structurally
  prevents "every push frame re-renders the whole screen"**. Code outside React reaches
  them through `getState()` / `setState()`.
- Stores talk to each other by subscription. For example the shell refreshes repos,
  sessions, files and chat on the workspace's stopped ↔ running **transition edge**
  (`wireWorkspaceRefresh` in `app/App.tsx`), ignoring the indeterminate states in between.
- **Push first, polling as the fallback.** `core/push/events.ts` holds one `api/events`
  stream per tab and hands each frame to its handlers; the streams are the
  `PushStream` type there, and the wire format is [05 §5.1](05-api.md#51-the-public-surface).
  The pollers for the same data stay running and skip their tick while `pushHealthy()` is
  true, so a broken stream, or a CP without the route, loses nothing. A poller also drops
  its own result when a push frame for the same stream landed while it was in flight
  (`pushStamp`). Every (re)connect re-reads whoami and the session list, because a frame
  is sent only when something changes. Data outside the push streams is polled on its own
  schedule (repos every 60 s, for example).
- **Workspace state** is the CP's value (`running`, `starting`, `stopped`, `none`) or the
  client's `unknown` when the read failed. A trailing `…` marks an optimistic in-flight
  state, which both the buttons and the poller treat as busy and leave alone.
- `features/files/sessionRefresh.ts` watches the session list: on a session's
  **busy → not-busy edge** (working/compacting or backgroundBusy clearing — the end of a
  turn) it fires a *scoped* files refresh, `refreshUnder("repos/<copy>")`. The tree
  re-reads only the directories on screen under that prefix, and the changes view swaps
  its list without blanking it. Without it, files an agent created or deleted stay
  invisible until someone presses refresh. The session list arrives anyway, so the trigger
  costs no extra traffic; firings coalesce per working copy and keep a minimum gap.
  **A failed re-read (5xx, dropped fetch) must be swallowed and the current rows kept**:
  writing the failure back as an empty listing would empty the tree at the end of every
  turn.
- Events lead and intervals are the safety net; the timings live in
  `features/files/refreshPolicy.ts`. Two cases the turn-end edge cannot reach get their
  own trigger: **mid-turn**, the working copies of running sessions are re-read on an
  interval (the timer stops when nothing is running, and never fires on a hidden tab or a
  stopped workspace); **on tab or window return**, what is on screen is revalidated,
  rate-limited, under the same gates as `features/editor/probe.ts`. The latter is also the
  only trigger that covers a session with no state model (shell, SSM) or a change made
  outside Agent Fleet. Rows an automatic re-read added are highlighted for a few seconds
  (`.fs-new`).
- **`core/api/client.ts` is the only door to the network.** It replaces `window.fetch`, so
  every request — including a bare `fetch` — carries the `X-AF-Tenant` header. WebSockets,
  new tabs and downloads cannot, and pass `?tenant=` instead
  ([05 §5.4](05-api.md#54-cross-cutting-rules)). The rules a caller relies on:
  - `api()` **does not reject on an HTTP error**; it resolves with `{error: {code, …}}`.
    Only a network failure rejects. Check `r.error`, or a failure passes as success.
  - `api()` answers a `304` with the object it returned last time, so **treat its results
    as immutable**.
  - A `401` does not navigate away. It trips a latch (`core/auth/authExpired.ts`) and the
    re-login dialog (`features/auth/AuthExpiredModal.tsx`) opens; running terminals keep
    working. The terminal socket bypasses the wrapper, so on a drop it probes one API call
    to find out whether the cause was the login.
  - The user-facing text for an error code is `errText()`, from the `err.<code>` catalogue
    keys (§2.8).
- `lib/attention.ts` reports real interaction to the CP at most once a minute while the
  tab is visible, so someone who is only reading does not look idle and get their
  workspace stopped.

## 2.4 Panes, layout and the terminal service

- **A layout separates geometry from runtime** (`layout/types.ts`, layout version 3).
  A **Cell** is geometry: the React key, activation, the ordinal badge, the drop target.
  A **View** is the runtime identity: the tab, the xterm and its WebSocket, the browser
  controller, the dirty editor. `View.content` is a discriminated union of what the view
  renders (terminal, file, scm, the sessions overview, the studio, …). **`session` lives
  on the View, not on the content**: switching what a view shows keeps the PTY socket and
  scrollback alive but hidden, so going back to the terminal shows the same session.
- **Two layout profiles**, chosen by a device-local preference: `split`, up to 4 columns
  of 1–2 cells with one view each; and `tabs`, up to 3 columns, where each cell holds tabs
  (24 views in all). Each profile keeps its own saved layout, so switching destroys
  nothing.
- **The id contract is a hard invariant.** Swapping, drop-splitting and moving a tab
  keep both the View id and the Cell id; renumbering or duplicating is forbidden. A new
  View id builds a new xterm and a new WebGL context, and **the terminal you just moved
  comes up blank**. The pure functions in `layout/ops.ts` and their tests enforce this.
- `layout/ops.ts` is `Layout in → Layout out`. A no-op returns the input by reference,
  so the caller can skip the commit on `next === cur`. The layout store's `commit()` is
  **the only path that mutates**: it pushes the layout into `history.state` (the URL
  never changes) and persists it.
- **Persistence is per user and tenant, per tab.** The key is built by `LKEY_NEW` in
  `layout/migrate.ts`. A tab's own layout is in `sessionStorage`, so two tabs keep
  different layouts; `localStorage` holds the last one written, to seed a new tab. What is
  read back is untrusted JSON: `migrate.ts` validates it per content kind, and **an
  unknown kind loads as a blank terminal**.
- **Adding a content kind** touches, for example, the union in `layout/types.ts`, the
  validator in `layout/migrate.ts`, `sameTarget` in `layout/ops.ts` (which decides when a
  second open focuses the existing view), the render switch in `features/panes/Pane.tsx`,
  the titles in `features/panes/paneTitle.ts`, and the feature's `open.ts`. Miss the
  validator and the view vanishes on reload.
- **Tab order is most-recently-used.** `lastUsedAt` is not only for eviction; it decides
  **what to show when the visible tab goes away**. Closing, moving or detaching all
  select the remaining tab you looked at last — open a file from the mirror, close it,
  and you are back in the mirror. Stamps are strictly monotonic within a page session so
  two touches in the same millisecond cannot tie.
- **Pop-out**: a view can move to its own browser tab (`?pane=<nonce>`,
  `layout/popout.ts`). A popped-out tab does not write the shared `localStorage` seed.
- **The terminal service is the only entry point to xterm** (`terminal/service.ts`). One
  subscription to the layout store disposes the terminal of a view that left the layout.
  `term.ts` is hard-won domain knowledge, to be changed with care: zombie-socket
  detection by a heartbeat on the data channel (text frames are out-of-band control,
  binary frames are PTY output), WebGL rendering with context-loss recovery, the Keyboard
  Lock while focused, copy-on-select clipboard integration, and soft-keyboard fitting.
  All panes are flat, absolutely positioned children of one host
  (`features/panes/PaneHost.tsx`). A terminal's container must hold exactly one `.xterm`
  (`terminal/paneContainer.dom.test.tsx`); an off-screen terminal gives its WebGL context
  back, because browsers cap live contexts per tab.
- The browser registry (`features/browser/controller.ts`, wired in `service.ts`) is keyed
  by view id too and owns the page, the socket and the canvas. **Only `{kind, port, path}`
  is persisted**; the page id is not. A hidden page is destroyed after 60 seconds, and
  showing it again — or a reload, or a workspace restart — rebuilds it from the port and
  path. How the browser pane is meant to be used is
  [guide/ref/browser-pane.md](../../guide/ref/browser-pane.md).
- **`agents/registry.ts` has one descriptor per session kind** — the agents plus `shell`
  and `ssm` (`SESSION_KINDS` in `types/session.ts`). A descriptor holds the display
  names, an availability predicate and a capability set, and the UI branches on
  capabilities rather than on kind names. Adding a kind starts with a descriptor; the
  other places that name kinds include the colours (§2.6) and
  [guide/ref/agents.md](../../guide/ref/agents.md), whose capability rows
  `agents/guideTable.test.ts` checks against the descriptors.
- **Display names come in three widths**, and the internal identifiers are immutable
  lowercase: a two-letter `short` for cramped badges, a compact `label` for pane headers
  and session rows, and a full `displayName` for launch and settings cards. Write display
  code through the helpers in `lib/sessionkind.ts` (`kindShort`, `kindLabel`,
  `kindDisplayName`), never by reading a raw label or hard-coding a name.

## 2.5 Information architecture

The screens themselves are described for members in `guide/member/`; this section is the
shape a change has to fit.

- **Two bars.** The top bar holds, for example, the app name, the tenant picker (hidden
  when you belong to one tenant), the notification centre, the engine indicator, the
  appearance popover and the account menu (guides, settings, tenant settings, admin,
  sign-out). Below it, the workspace bar holds state and Start/Stop, resource and usage
  chips, port preview and the split controls.
- **Left pane**: the layout map, the working-set switcher, then the resident sections in
  the order `app/App.tsx` renders them (assistants, work items, the memo queue, the
  project tree and more). The project tree is the centre — a project-first IA: working
  copies grouped per project, with their sessions and files nested underneath. Sessions
  outside any repository, shared sessions and a global file browser sit below it.
- **Main area**: the pane host.
- **History navigation** pushes the layout into `history.state` **without changing the
  URL** (a path-stripping proxy makes URL paths unusable). Back and forward restore the
  layout and the phone drawer. "Back closes the modal" belongs to the shared modal layer
  (`ui/Modal` with `lib/backClose.ts`), and drill-downs stack on it. The only URLs the
  Console reads are entry points: `?session=` (a notification link), `?pane=` (a pop-out)
  and `open/browser-attachment/{id}`.
- **A horizontal swipe on a phone rotates through running sessions** when the drawer is
  closed. The selection rule is `features/sessions/rotate.ts` and the gesture is
  `app/swipeGestures.ts`. The order is the session list as returned, filtered by the
  working set, so it matches what the left pane shows. A swipe starting at the left edge
  yields to the drawer. Swipes are skipped on a surface with its own horizontal gesture
  (`app/swipeGuard.ts`: the browser pane, inputs, horizontal scrollers,
  `[data-no-swipe]`).
  - **The horizontal-scroller test cannot use the computed `overflow-x`**: CSS computes
    `visible` to `auto` when the other axis is not visible, so a purely vertical scroller
    reads as `auto` too. One unbreakable string in a transcript used to push the whole
    mirror sideways and kill swiping for that session. The fix is two-sided: the
    transcript wraps (`overflow-wrap: anywhere`), and a vertically read surface declares
    `[data-swipe-y]`, so horizontal overflow there is by definition an accident. **Do not
    loosen the test itself**: code and diff views genuinely pan both ways.
- **Three settings dialogs**: personal settings, tenant settings (a tenant
  administrator's) and admin (the deployment administrator's). Which tabs exist and where
  they sit is [guide/ref/settings.md](../../guide/ref/settings.md). The rules for a change:
  - Personal settings is a grouped rail (`GROUPS` in
    `features/settings/SettingsDialog.tsx`); a phone drills rail → content. Section keys
    are deep-link ids (`openSettings(section)`), so keep them when the rail is
    reorganised; `settingsRail.dom.test.tsx` pins which group a section sits in.
  - A tab whose capability the deployment lacks is not shown at all — for example cloud
    cost without an AWS bill, and preview subdomains where none are issued.
  - Admin functions are a separate dialog, never mixed into personal settings.
  - **The tenant settings and admin dialogs share one shell, and one tenant's surface is
    one component** (`features/settings/tenant/tenantScope.tsx`). The admin rail has two
    levels; opening a tenant swaps the whole rail into that tenant. It is the same tenant
    seen from two entrances, so the IA is not duplicated.
  - What a dialog shows or hides is guidance only. **The server decides permissions.**

## 2.6 The display system

- **Theme**: `styles/tokens.css` is the only home for the variables (`:root` is dark,
  `[data-theme=light]` overrides). `applyTheme()` in `lib/settings.ts` writes
  `data-theme` and the region variables; surface colours are tinted per theme so a light
  theme does not end up with unreadable dark bars. highlight.js follows the theme through
  `--hl-*` variables. **Known limit: the terminal has no light theme** — it stays dark in
  light mode.
- **Agent kind colours** come from `--kind-*` in `tokens.css`, for both themes. CSS uses
  `var(--kind-*)`, and `color-mix(…)` for tints; **a CSS file does not repeat a kind's
  colour literal**. The one copy outside `tokens.css` is `lib/termcolor.ts`, which mixes the
  dark values into terminal backgrounds and has to change with them. A new kind's hue is
  checked against the existing hues and the semantic colours in both themes
  (`console/scripts/kindcolor/`).
- **Icons split by role**: chrome is monochrome codicons following `currentColor`; file
  types are coloured SVGs resolved by extension (`lib/fileicons.ts`, `ui/FileIcon.tsx`).
- **UI preferences are stored per user on the server** (`GET/PUT /api/env/ui-prefs`,
  `lib/settings.ts`). `localStorage` is the immediate cache and a debounced `PUT`
  persists. At boot `hydrateUIPrefs()` merges with the server copy winning, except for a
  local change not yet saved, which stands and goes out with the next save. A read that
  failed is never taken for an empty server. Some keys are **device-local** and never leave
  the browser — the theme, the surface colours, the layout profile and the read-aloud
  switch, for example (`DEVICE_LOCAL`). When saving fails, the Console says so
  (`PrefsSyncBanner`).
- **Phones are for monitoring plus light operation.** The phone breakpoint is 760 px
  (`MOBILE_QUERY` in `lib/device.ts`, repeated in the CSS media queries). Phone-only
  behaviour stays inside those branches so the desktop DOM and CSS are untouched.

## 2.7 Build, serving and the hard constraints

- `npm run build` is `vite build`; `npm run dev` is `vite build --watch`, and a browser
  reload picks up the change — the CP does not need restarting
  ([10](10-development.md)). Mermaid and Marp are heap-hungry, so the scripts raise the
  Node heap **per command**, not globally. Sourcemaps are off (generating them has
  overflowed the heap before).
- The heavy renderers are **lazily imported chunks**, kept out of the main bundle — for
  example Mermaid, Marp, pdf.js, the office-document converter (WASM) and the CodeMirror
  language packs.
- **A Marp trap worth knowing**: even with maths disabled it *statically* requires
  MathJax (~43 MB) and KaTeX, so an untreated production build hangs during minify. An
  alias in `vite.config.js` swaps in `marp-math-stub.js`. **This is a fixed constraint —
  removing the alias kills the build.**
- **Serving.** The CP serves the directory `CONSOLE_DIR` names — the build output
  `console/dist` in development — from `registerStatic` in `control-plane/routes.go`.
  Everything under `assets/` is cached by the browser as immutable, and everything else
  (the shell, `version.json`, `sw.js`, the manifest) is `no-store`. The header values
  belong to [05 §5.4](05-api.md#54-cross-cutting-rules). What that asks of a change:
  - **A file under `assets/` must change its name whenever its bytes change.** Vite's
    content hashes do this; a copied directory puts a version in its path, as the pdf.js
    character maps do (`assets/pdfjs/<version>/`, the `afPdfjsAssets` plugin).
  - A deployment reaches a tab on its next load of the shell. A tab that stays open finds
    out through `version.json`, which the build writes and `lib/useUpdateCheck.tsx` polls,
    and offers a reload.
  - The CP rewrites `index.html` and the manifest per request when the deployment is
    branded (`control-plane/brand.go`).
  - `public/sw.js` exists only for the Web Share Target. It caches no app shell and
    intercepts nothing else; keep it that way, or the rules above stop holding.

## 2.8 Text and i18n

- The catalogue is `lib/i18n/locales/{ja,en}/<domain>.ts`. Japanese is the master: a key
  is `keyof` the Japanese catalogue, and each English file is typed against its Japanese
  counterpart, so the type check fails on a missing or extra key. The default locale is
  `ja`. Details and the reasoning: [decisions/0016](../decisions/0016-i18n.md).
- React code uses `useT()`; code outside React uses `t()`. Error codes map to
  `err.<code>` keys in `errors.ts`.
- Japanese belongs in the catalogue and in user-visible strings, never in a comment
  (`AGENTS.md`). `npm run i18n:lint` fails on raw Japanese in JSX text or string literals.
  Files listed in `scripts/i18n-lint-pending.json` are a backlog that only warns, and a
  file leaves that list once it is clean; `// i18n-exempt` marks text that is never
  translated.

## 2.9 Tests and checks

Run them from `console/` (`AGENTS.md` explains why the repository root gives a wrong
result).

- **vitest has two projects** (`console/vite.config.js`). `node`, the default, runs
  `*.test.ts(x)`: pure logic — layout operations, parsers, stores — and components
  rendered to static markup. `dom` runs `*.dom.test.tsx` under jsdom, with
  `src/test/domSetup.ts`, for tests that mount components. The split exists because
  standing up jsdom costs about 1.3 s per test file; running everything under jsdom was
  measured at 10.8 s → 51.3 s. Workers are capped at two for the shared host's memory.
- **Typecheck**: `npm run typecheck` (`scripts/typecheck.mjs`).
- **Lint**: `npm run lint` is oxlint with one rule, `react/rules-of-hooks`
  (`.oxlintrc.json`). A hook below an early return has taken the whole Console black.
- **CI** (`.github/workflows/ci.yml`, job `console`) runs typecheck, lint, i18n lint,
  vitest, two checks in real headless Chromium (`pdf:check`, `doc:check`) and the build.
- The other scripts in `package.json` (screenshots, mirror and viewer scroll checks, the
  contrast check and more) drive a real headless browser too, but they are run by hand and
  are not in CI.
- **End to end**: `console-e2e/` (Playwright), a real browser through the CP to a real
  container ([10 §10.4](10-development.md#104-testing)).

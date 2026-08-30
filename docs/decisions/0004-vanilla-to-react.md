# 0004. Console stack — adopt React + Vite

English | [日本語](0004-vanilla-to-react.ja.md)

- Status: decided (Phase 3 / the full Console rebuild)
- See also: [build/02 Console](../build/02-console.md) (formerly HANDOFF §6.10.1) / [history/console-redesign](../log/console-redesign.md) (the diagnostic brief written at the time)

## Context

React was the settled stack from the beginning
([requirements §1.6, now build/01 §1.1](../build/01-architecture.md#11-what-it-is-and-how-it-is-delivered)),
but the Phase 1 MVP shipped as **minimal vanilla JS** (`app.js`, 617+ lines). As features
arrived (SCM / Files / Admin / Connections / the tenant picker) the information architecture
broke down — no navigation, a row of cryptic icons, a mix of sidebar and overlay, an overloaded
header. For the rebuild we compared "stay vanilla / a lightweight framework from a CDN / adopt
React properly". The brief of the time (old doc 18) weighted ease of distribution highly
(serve from disk, no-store, instant reflection) and **recommended staying vanilla**.

## Decision

**Adopt React + Vite** (`console/src` → `console/dist`, served by the CP with
`Cache-Control: no-store`). Being able to build the grown feature set — the VS Code-style IA
with a left activity rail and a single main area, SCM, the file viewer, settings, admin — beat
the convenience of having no build step. The distribution concern is absorbed by baking dist
into the image.

- IA: a two-row bar (TOP = app name / tenant picker / whoami / settings / admin) plus a left
  pane with three sections (Sessions/Repos/Files), with main switching on selection. Overlays
  were abolished in favour of view switching.
- Front-end-only tweaks reflect via `vite build --watch` plus a browser reload (no CP restart).

## Consequences

- The old vanilla code was parked in `console/legacy-phase1/` for a while and deleted once the
  port was complete. The current Console is the source of truth for behaviour (HANDOFF §6.10.1).
- There is one more build step, but `run-dev.sh` absorbs it with
  `NODE_OPTIONS=--max-old-space-size=3072 npm run build` (to avoid the mermaid heap OOM). P3-10
  packaging ships dist inside the image.

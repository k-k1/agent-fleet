# README screenshot harness

Captures the screenshots used by the root `README.md` and by the dist repo's
`README.md` / `README.ja.md`, straight into `docs/img/`.

```bash
npm --prefix console run build          # console/dist must exist (the real bundle)
node console/scripts/shots/capture.mjs --locale ja
node console/scripts/shots/capture.mjs --locale en
```

## How it works

- `server.mjs` serves `console/dist` (the **real** Console bundle) and answers the
  Control Plane's API surface from `fixtures.mjs`. No CP, no workspace agent, no
  Docker, no database. `/api/events` deliberately 404s so the Console falls back to
  its REST pollers, which is the path the stub feeds. `ws/terminal` is a ~40-line
  WebSocket server that replays a canned PTY screen, so terminal panes render like a
  live attach.
- `capture.mjs` starts the stub, drives headless Chromium over raw CDP (Node 22's
  global `WebSocket` — no Playwright/Puppeteer), seeds `localStorage` the way a
  returning user's browser would look (locale, theme, the saved pane layout, rail
  section fold state), then screenshots each scene as WebP at `deviceScaleFactor: 2`.
- Scenes live at the top of `capture.mjs`: a pane layout + a viewport, optionally a
  `settings` section to pre-select and an `action` snippet evaluated after boot (the
  launch-dialog scene clicks its way into the agent picker; the usage scene opens
  Settings › Usage and switches the range to 30 days).

## Rules for the fixtures

- **Everything is fictional.** Invented repo names, session titles, commits, authors
  and a scripted conversation, under `demo@example.com` / tenant `demo`. Never point
  this at a real fleet — published screenshots must not carry a tenant name, an
  address, a private repo, or an agent account's usage numbers.
- Fixture shapes follow the real wire contracts (`console/src/types/session.ts`,
  `console/src/features/repos/store.ts`,
  `workspace/agent/internal/transcript/transcript.go`, `console/src/lib/gitgraph.ts`).
  When one of those changes, the affected pane renders empty instead of failing —
  check the shot, not just the exit code.
- The stub logs `[stub] unhandled: <path>` for any API path it does not know, which is
  how you find an endpoint a new view needs.

## Publishing

`deploy/release/publish-dist.sh --seed` pushes `docs/img/*.webp` to the dist repo
under the same path, so both READMEs reference them relatively.

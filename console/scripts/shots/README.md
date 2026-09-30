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
  Settings › Usage and switches the range to 30 days). `settle` is the wait after that
  action, so it does nothing in a scene without one.
- A scene whose picture is drawn after boot (Mermaid, the draw.io viewer) sets `ready`: an
  expression polled until true, for at most 30 s. The run fails rather than write a
  half-drawn picture — a fixed wait is not enough on a busy host.

## Rules for the fixtures

- **Everything is fictional.** Invented repo names, session titles, commits, authors
  and a scripted conversation, under `demo@example.com` / tenant `demo`. Never point
  this at a real fleet — published screenshots must not carry a tenant name, an
  address, a private repo, or an agent account's usage numbers. That covers third-party
  image models too: a real checkpoint's name beside a licence line reads as an endorsement
  and a licence claim, so the studio's models are invented (`sdxl` / `flux1` are Agent
  Fleet's own family ids and stay).
- Fixture shapes follow the real wire contracts (`console/src/types/session.ts`,
  `console/src/features/repos/store.ts`,
  `workspace/agent/internal/transcript/transcript.go`, `console/src/lib/gitgraph.ts`).
  When one of those changes, the affected pane renders empty instead of failing —
  check the shot, not just the exit code.
- The stub logs `[stub] unhandled: <path>` for any API path it does not know, which is
  how you find an endpoint a new view needs.
- `server.mjs --idle` (or `SHOTS_IDLE=1`) serves the mirror session idle with no pending
  question. The README shot wants the live question card, but that card locks the composer,
  so anything that exercises the composer itself (the skill picker's tiers, say) needs this.

## Features-page stills

Three scenes are for the features page on agent-fleet.org rather than the README: the file
viewer (`files` — a Markdown note with its Mermaid diagram beside a draw.io diagram), the
work-item inbox (`workitems` — CI and conflict marks, and a pull request's detail panel) and the
image-generation studio (`imagegen`). They land in `docs/img/` like the README shots and ship the
same way:

```bash
node console/scripts/shots/capture.mjs --locale en --only files,workitems,imagegen
node console/scripts/shots/capture.mjs --locale ja --only files,workitems,imagegen
```

The site takes them from a release of the distribution repository, so a change here reaches the
page with the next release.

## Guide shots

Two scenes are for the user guide rather than the README — the sessions overview and the
fleet graph — and land in `guide/assets/` (shipped with the guide by `stage-docs.sh`):

```bash
node console/scripts/shots/capture.mjs --locale ja --only overview,fleetgraph --out guide/assets
node console/scripts/shots/capture.mjs --locale en --only overview,fleetgraph --out guide/assets
```

The container is shared, so pass `--port` / `--cdp-port` that nothing else is listening on.

## Publishing

`deploy/release/publish-dist.sh --seed` pushes `docs/img/*.webp` to the dist repo
under the same path, so both READMEs reference them relatively.

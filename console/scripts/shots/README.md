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
- `server.mjs --idle` (or `SHOTS_IDLE=1`) serves the mirror session idle with no pending
  question. The README shot wants the live question card, but that card locks the composer,
  so anything that exercises the composer itself (the skill picker's tiers, say) needs this.

## Guide shots

Two scenes are for the user guide rather than the README — the sessions overview and the
fleet graph — and land in `guide/assets/` (shipped with the guide by `stage-docs.sh`):

```bash
node console/scripts/shots/capture.mjs --locale ja --only overview,fleetgraph --out guide/assets
node console/scripts/shots/capture.mjs --locale en --only overview,fleetgraph --out guide/assets
```

The container is shared, so pass `--port` / `--cdp-port` that nothing else is listening on.

## Demo recording

The README's "A day with Agent Fleet" is also a ~45-second animated WebP,
`docs/img/demo-en.webp` / `demo-ja.webp`:

```bash
npm --prefix console run build          # console/dist must exist (the real bundle)
pip install --user pillow               # the encoder; there is no ffmpeg in the workspace
node console/scripts/shots/demo.mjs --locale en
node console/scripts/shots/demo.mjs --locale ja
```

- `server.mjs --demo` swaps in `demo-fixtures.mjs`, a fleet with state: POST /api/sessions
  creates the session and its worktree the way the Agent would, and `POST /__demo/phase`
  moves the story from "just launched" to "one finished, one waiting on you".
- `demo.mjs` plays the scenario through the real UI — the issue tracker's Start, the launch
  dialog, the rail, the sessions overview, the changed-files panel — with CDP input, records
  it with `Page.startScreencast`, and hands the frames to `demo-encode.py`. A step whose
  control never appears fails the run instead of recording a skipped step.
- `demo-overlay.js` draws what the Console cannot: a cursor (headless has none), the caption
  band under the Console, and the phone. The phone's Slack thread is **redrawn**, not
  recorded: its texts and buttons are the chat bridge's own strings
  (`workspace/agent/internal/bridge/format.go`, `slack_interact.go`,
  `workspace/agent/internal/sessionx/bridge_answer.go`), so update them together.
- `--keep-frames` leaves the raw PNG frames in the temp directory it prints, for checking a
  single moment.

## Publishing

`deploy/release/publish-dist.sh --seed` pushes `docs/img/*.webp` to the dist repo
under the same path, so both READMEs reference them relatively. The demo recordings
(`demo-*.webp`) are left out: the dist READMEs do not show them.

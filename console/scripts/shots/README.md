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
- A scene whose `action` switches the UI into a state sets `verify`: an expression polled after
  `settle` until true, for at most 20 s. An action that misses its target does nothing and still
  yields a plausible picture of the default state, so the run fails instead of writing it. Check
  the content, not just the control: the usage scenes require the pressed 30-day button *and*
  `.usage-stage[data-shown-hours="720"]`, because the view keeps the previous range's data up
  while the new one loads. Select the target by a `data-*` attribute rather than its label.

## Rules for the fixtures

- **Everything is fictional.** Invented repo names, session titles, commits, authors
  and a scripted conversation, under `demo@example.com` / tenant `demo`. Never point
  this at a real fleet — published screenshots must not carry a tenant name, an
  address, a private repo, or an agent account's usage numbers. That covers third-party
  image models too: a real checkpoint's name beside a licence line reads as an endorsement
  and a licence claim, so the studio's models are invented (`sdxl` / `flux1` are Agent
  Fleet's own family ids and stay). Agent model ids are the exception: the sessions run real,
  current models (`claude-opus-5-5`, `gpt-6-luna`, `gemini-4-argon-medium`) by the maintainer's
  choice. Move them to the newest ids the agents offer (`list_models` per kind) when the shots
  are retaken.
- Fixture shapes follow the real wire contracts (`console/src/types/session.ts`,
  `console/src/features/repos/store.ts`,
  `workspace/agent/internal/transcript/transcript.go`, `console/src/features/scm/gitgraph.ts`).
  When one of those changes, the affected pane renders empty instead of failing —
  check the shot, not just the exit code.
- The stub logs `[stub] unhandled: <path>` for any API path it does not know, which is
  how you find an endpoint a new view needs.
- `server.mjs --idle` (or `SHOTS_IDLE=1`) serves the mirror session idle with no pending
  question. The README shot wants the live question card, but that card locks the composer,
  so anything that exercises the composer itself (the skill picker's tiers, say) needs this.

## Features-page stills

Eight scenes are for the features page on agent-fleet.org rather than the README:

| Scene | Shows |
|---|---|
| `files` | the file viewer: a Markdown note with its Mermaid diagram beside a draw.io diagram |
| `workitems` | the work-item inbox: CI and conflict marks, new-activity dots, comment counts, and a mergeable pull request's detail panel with Merge |
| `imagegen` | the image-generation studio |
| `memory` | Settings › Agent memory: memories written by sessions of several kinds, one opened |
| `inbox` | the member inbox: a request waiting, one taken with its session, a message |
| `sessionmenu` | the session menu: move, handoff, share and the three Markdown exports |
| `move` | the dialog that moves a session to another agent, with the work that goes along |
| `skills` | Settings › Skills: personal skills by scope, with a skill and a command of one name shown as shadowed |

They land in `docs/img/` like the README shots and ship the same way:

```bash
node console/scripts/shots/capture.mjs --locale en --only files,workitems,imagegen,memory,inbox,sessionmenu,move,skills
node console/scripts/shots/capture.mjs --locale ja --only files,workitems,imagegen,memory,inbox,sessionmenu,move,skills
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

Scenes marked `guide: true` are for the rewritten guide and the public docs site (ADR 0131). The
default run skips them, so name them in `--only` and pass `--out`. A scene with its own stub data (`stubEnv`:
`aws-toast`, `ssm-hosts`, `ssm-start`, `gcp-settings`, `gcp-badge`, `gcp-toast`, `chat-settings`, `ops-settings`,
`images-gallery`, `images-settings`, the workspace pages' `ws-recreate`, `ws-machine`, `ws-toolchains`,
`ws-databases`, the sessions pages' `sessions-budget`, the conversation pages' `conv-plan`, `conv-skills`, `conv-queue`, `conv-fork` and `conv-translate`, the code pages' `code-git-hosting`, `code-internal-repos`, `code-clone`, the agent pages' `agents-*` scenes, the file pages' `files-edit`, the preview pages' `preview-settings` and `preview-devidp`, the fleet pages' `fleet-wset`, `fleet-settings`, `fleet-operator`, `fleet-report` and `fleet-schedule`, the team pages' `team-handover`, `team-share`, `team-inbox-compose` and `team-limits`, the chat pages' `chatpages-*`, the outside-access pages' `outside-*`, the settings pages' `settings-*`, the memory pages' `memory-*` and `memio-*` scenes, the admin pages' `admin-*` scenes, the operate pages' `operate-brand`, `operate-slots` and `operate-gpu-*`, and the getting-started pages' `start-welcome` and `start-restart`) runs on its own:

```bash
node console/scripts/shots/capture.mjs --locale ja --theme light --only aws-settings,aws-badge --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only tracker-settings --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only aws-toast --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only ssm-hosts --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only ssm-start --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only gcp-settings --out <dir>   # and gcp-badge, gcp-toast
node console/scripts/shots/capture.mjs --locale ja --theme light --only chat-settings --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only ops-settings --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only images-studio,images-form --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only images-gallery --out <dir>   # and images-settings
node console/scripts/shots/capture.mjs --locale ja --theme light --only ws-recreate --out <dir>  # and ws-machine, ws-toolchains, ws-databases, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only conv-plan --out <dir>    # and every other conv-* scene, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only sessions-start,sessions-launch,sessions-location,sessions-issue,sessions-status,sessions-issue-list,sessions-pr --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only sessions-cleanup,sessions-stop-below,sessions-resume,sessions-menu,sessions-move,sessions-handoff --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only sessions-budget --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only code-git-hosting --out <dir>  # and code-internal-repos, code-clone, code-gitflow, code-repo-rows, code-repo-menu, code-changes, code-svn, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only agents-claude --out <dir>  # and every other agents-* scene, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only files-menu,files-view,files-drawio,files-send,files-terminal,preview-pop --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only files-edit --out <dir>  # and preview-settings, preview-devidp, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only fleet-overview,fleet-graph --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only fleet-wset --out <dir>  # and fleet-settings, fleet-operator, fleet-report, fleet-schedule, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only team-handover --out <dir>  # and team-share, team-inbox, team-inbox-compose, team-limits, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only chatpages-chat --out <dir>  # and every other chatpages-* scene, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only outside-token-issued --out <dir>  # and outside-apps, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only admin-audit --out <dir>  # and admin-usage-monthly, admin-mcp-form, admin-ext-memory, admin-watch-sources, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only settings-display --out <dir>  # and settings-notifications, settings-tts, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only start-tour,start-palette,start-appearance,start-phone --out <dir>
node console/scripts/shots/capture.mjs --locale ja --theme light --only start-welcome --out <dir>  # and start-restart, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only memio-hub --out <dir>  # and memio-snapshots, memio-external, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only memory-view --out <dir>  # and memory-changes, memory-settings, memory-lint, memory-proposal, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only admin-roster --out <dir>  # and every other admin-* scene, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only admin-res-limits --out <dir>  # and admin-res-engine-access, admin-res-member, admin-res-size, admin-res-idle, admin-res-stop, one per run
node console/scripts/shots/capture.mjs --locale ja --theme light --only operate-gpu-engine --out <dir>  # and operate-gpu-history, operate-gpu-catalog, operate-gpu-tts, one per run
```

- `clip` crops to the union of the selectors' boxes plus `pad`: the component and the control that
  opens it, and nothing around them. A clip that matches nothing fails the run instead of writing
  the whole page.
- `stubEnv` changes what `server.mjs` serves for the whole run. `aws-toast` adds a pending login
  request, whose toast would cover every other scene, so a scene like that has to run on its own.

## Demo recordings

Scripted scenarios played through the real Console, written as animated WebP to
`docs/img/demo-<scenario>-<locale>.webp`:

| Scenario | Shows | Used by |
|---|---|---|
| `day` | the README's "A day with Agent Fleet": two issues to Claude Code and Codex in worktrees, a permission allowed from Slack on a phone, the overview and a worktree's diff (~45 s) | README |
| `phone` | the Console in a phone's browser: answer a waiting session's question with a tap (portrait, ~18 s) | landing page |
| `review` | a session starts a reviewer of another kind, gets one report back, and the fleet graph draws it (~27 s) | landing page |
| `plan` | approve a plan card, then stage and commit from the Changes pane (~24 s) | landing page |
| `unattended` | a schedule wakes a stopped workspace; a usage limit is waited out and resumed (~27 s) | landing page |
| `orchestrate` | one Claude session splits a design review across Codex, Antigravity and Muse Code, gathers three reports, and the fleet graph draws four lanes (~31 s) | landing page (features) |
| `sre` | the SRE assistant reads PagerDuty and CloudWatch (read-only); the fleet operator starts the fix session (~33 s) | landing page (features) |

```bash
npm --prefix console run build          # console/dist must exist (the real bundle)
pip install --user pillow               # the encoder; there is no ffmpeg in the workspace
node console/scripts/shots/demo.mjs --scenario day --locale en    # and ja, and each scenario
```

- A scenario is one file, `demo/<scenario>.mjs`. Its `fixtures()` half runs inside
  `server.mjs --demo <scenario>`: a fleet with state that answers only the routes the story
  changes (the rest falls through to `fixtures.mjs`), moved on by the requests the Console
  really sends (a launch, an answer, an approval, a commit) and by `POST /__demo/phase`. Its
  `meta` / `seed()` / `script()` half runs in `demo.mjs`: the viewport, the browser state a
  returning user would have, and the steps. `demo/kit.mjs` holds what they share.
- `demo.mjs` drives the page with CDP input, records it with `Page.startScreencast`, and hands
  the frames to `demo-encode.py`. A step whose control never appears fails the run instead of
  recording a skipped step.
- `demo-overlay.js` draws what the Console cannot: a cursor or a finger (headless has none), the
  caption band under the Console, a clock for stories that span hours, and the phone in `day`.
  That phone's Slack thread is **redrawn**, not recorded: its texts and buttons are the chat
  bridge's own strings (`workspace/agent/internal/bridge/format.go`, `slack_interact.go`,
  `workspace/agent/internal/sessionx/bridge_answer.go`, and the kind's product name from
  `workspace/agent/internal/kinds/kinds.go`), so update them together.
- `sre` streams the assistants' replies: a scenario's `stream()` answers a route as Server-Sent
  Events (the chat's `POST …/stream`), frame by frame.
- `orchestrate` and `sre` press the rail's repos Refresh behind the scenes after a worktree is
  created: the Console otherwise picks new worktrees up on its 60-second poll, and until then a
  new session sits under "other sessions".
- `unattended` runs the page on a story clock (its `seed().init` replaces `Date`), so relative
  labels such as "started 2 hours ago" agree with the scene rather than with the machine.
- `--keep-frames` leaves the raw PNG frames in the temp directory it prints, for checking a
  single moment.

## Publishing

`deploy/release/publish-dist.sh --seed` pushes `docs/img/*.webp` to the dist repo
under the same path, so both READMEs reference them relatively. The demo recordings
(`demo-*.webp`) are left out: the dist READMEs do not show them.

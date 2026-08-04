# Mirror scroll-landing harness

Asserts the three things the mirror's scroll must do, against the **real** Console
bundle in headless Chromium:

1. opening a session that already has history lands at the **true bottom**;
2. expanding a 作業過程 disclosure while parked there **keeps the reader's place**;
3. a wheel-up **stops** auto-follow, shows 最新へ, and does not yank them back.

```bash
npm --prefix console run build        # console/dist must exist (the real bundle)
npm --prefix console run mirror:scroll
node console/scripts/mirror-scroll/check.mjs --runs 5 --scenario mermaid
```

Exit status is the check.

## Why this is not a unit test

The failure is a layout-timing one, so jsdom cannot see it. The transcript's turn bodies
are filled by `MarkdownView` into `innerHTML` from a **passive** effect, so at the moment
`MirrorView` pins the bottom from its layout effect the turns are still empty —
essentially all of a transcript's height arrives afterwards, in several steps (parse →
highlight → mermaid → image decode → fonts). Anything that decides "is the user at the
bottom" from geometry sampled when the **scroll event** is dispatched can mistake that
growth for the user scrolling up, and every re-pin path is disarmed from then on.

`--cpu 4` (the default) throttles the main thread, because that window between the
programmatic scroll and its event is exactly what a modest or busy machine widens. Without
it a broken build often lands correctly by luck on an idle headless run — which is why the
symptom always read as intermittent. Measured: with throttling the pre-fix bundle failed
3/3 with the view stranded 1246 px above the end; the fixed one passes.

## How it works

- `stub.mjs` serves `console/dist` and answers the Control Plane's API surface from the
  screenshot harness's fixtures (`../shots/fixtures.mjs`) — no CP, no workspace agent, no
  Docker. Only the transcript endpoint is its own: a synthetic idle transcript whose shape
  is the parameter (`--turns`, `--images`, `--imgdelay`, `--mermaid`).
- `check.mjs` drives headless Chromium over raw CDP (Node's global `WebSocket`, no
  Playwright/Puppeteer — same technique as `../shots/capture.mjs`), seeds the localStorage a
  returning user would have, and **clicks the session's row in the left pane** rather than
  deep-linking, because opening from the rail is the reported path.
- Scenarios live at the top of `check.mjs`. `switch` seeds the pane with a *different*
  session first, so a reused pane (the D&D / open-another-session path) is covered too;
  its transcript uses different line indices, or the incoming session would inherit the
  previous one's anchored reply and hide a bug.

Fixture shapes follow the same wire contracts as the screenshot harness
(`workspace/agent/internal/transcript/transcript.go`). If one changes, the transcript
renders empty rather than failing — check the reported `turns=` count, not just the exit
code.

# Agent instructions

## Commits

Before creating a commit, read the "Commits & PRs" section of `CONTRIBUTING.md` and
follow the message format and attribution rules described there.

These in particular are mandatory.

- The subject line takes the form `<type>(<scope>): <summary>`.
- **Write the subject summary and the body in Japanese.** (This is the maintainer's
  working convention for the repository's own history; outside contributors may use
  English — see CONTRIBUTING.)
- For a bug fix or a behaviour change, the body states the root cause, the fix, and
  how it was verified.
- A commit an agent authored or materially contributed to carries a
  `Co-Authored-By` trailer at the end, separated by a blank line.
- `Co-Authored-By` names the model that actually did the work, not the CLI.
- The address is the model vendor's `noreply@<vendor domain>`.
- Do not add a `Claude-Session:` trailer. It is tolerated when Claude Code adds one
  itself over a Remote Control connection.
- Immediately before committing, re-read the finished message and confirm it meets
  the convention.

## Running the Console tests

**Always run them with `console/` as the working directory.** Invoking `npx vitest`
from the repository root makes npx download a different vitest — the root has neither
a `package.json` nor `node_modules` — and start it without reading
`console/vite.config.js`. With the config inert the environment falls back to node, so
DOM tests fail with `document is not defined` and `--project` reports "project not
found". That looks like a broken config, so watch out for it.

```
cd console
npm test                       # every project
npx vitest run --project=node  # pure logic (the default)
npx vitest run --project=dom   # render tests (jsdom)
npx vitest run src/features/viewer/FileView.dom.test.tsx   # a single file also works
```

The tests are split into two projects (`console/vite.config.js`). Standing up the
jsdom environment costs about 1.3 s per test file, so node stays the default and only
tests that actually mount components opt in via `*.dom.test.tsx`.

## Showing this repository's UI to the user

To have the user look at a change in the Console (`console/`), serve the dev server on
`127.0.0.1:<port>` and tell them **the exact port and path**, pointing them at
"Preview → open in pane".

- Prefer the browser pane (open in pane) for a Vite dev server with HMR or any screen
  that uses WebSockets. The lightweight preview is enough for plain HTTP pages.
- The browser pane is a Console feature for the user; **agents have no tool to open or
  view it**. Never guess what it is showing.
- You may claim you "verified" something only when you rendered and checked it
  yourself with headless Chromium (`/usr/bin/chromium`). Keep the user's pane and your
  own headless verification separate.
- Stop the dev server when you are done; don't leave it resident (the shared host is
  memory constrained).
- Never copy secrets that surface in API keys, cookies or Console logs into logs or
  documents.

The authority on usage (terminology, recommended flow, states, constraints) is
`docs/31-container-browser-pane-ux-contract.md`.

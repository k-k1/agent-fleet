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

Real host names, customer names and the like must never enter the history — the
scanner behind `ci.yml`'s `release-scan` job only says so after the merge. Run the
same check on what you are about to commit:

```
git config core.hooksPath .githooks   # once per clone; worktrees share it
.githooks/pre-commit                  # or just run it by hand, any time
```

Anything it flags is fixed in the file *before* the commit, never in a follow-up
one. Don't reach for `--no-verify`.

## Comments

**Write every comment in English** — Go, TypeScript, CSS, SQL, shell alike. (Commit
messages stay Japanese; see above.) Japanese belongs in user-visible strings, i18n
catalogues, test fixtures and golden files — never in a comment. When a comment has to
name a Japanese UI label, give the English term and put the literal in parentheses only
when the reader needs it to find the string.

A comment earns its place by saying what the code cannot:

- **Why this exists, and what breaks without it.**
- **Invariants, prohibitions, preconditions** — "never compare versions here", "blocking
  here is latency before that key echoes back", "when in doubt, answer false".
- **The one line of evidence that makes a rule stick** — "measured: re-pushing provenance
  alone moves the digest". Without it the next reader deletes the rule.
- Non-obvious trade-offs, and pointers to `docs/decisions/` or `guide/ref/`.

Leave out:

- **Chronology and attribution** — "originally only docker had one", "#343 found that…",
  "removed on 2026-07-28", "was: `map[string]any{…}`". That history lives in `docs/log/`.
- **Restating what the next few lines plainly do.**
- Emphasis theatre — ★, 🔴, bold shouting, the same warning three times.
- Change logs in a file header.

When a paragraph of history carries nothing but a reason, keep the reason as one sentence
and drop the story. Go doc comments still open with the identifier's name.

The house style is already in the tree: `control-plane/reaper.go` and
`control-plane/internal/runtime/runtime_ecs_stale.go` are written to it.

## `console/node_modules` in a worktree

Installing it is ~350 MB per worktree, and sessions usually get one worktree each. When this
worktree's lockfile matches the parent clone's, share the parent's tree instead:

```
cd console
cmp -s package-lock.json ~/repos/agent-fleet/console/package-lock.json \
  && ln -s ~/repos/agent-fleet/console/node_modules node_modules
```

`npm run build` and the whole node project resolve through the link (measured).

⚠️ **The `viewer` dom tests do not.** They import assets out of `node_modules` by URL
(`…?url`), and Vite's default `server.fs.allow` is the project root — through the link
those resolve *outside* this worktree and are refused with `Error: Denied ID …`. It
reads as a broken viewer and is not: the same tests pass against a real install
(measured, whole suite green). Everything else is unaffected, so only reach for a real
install (`rm -rf node_modules` — no trailing slash — then `npm ci --prefer-offline`)
when you touch the viewer or need those files green.

- **Remove the link before any `npm ci` / `npm install`**: `rm -rf node_modules` — *without* a
  trailing slash. `npm ci` through the link empties the parent's `node_modules` and breaks
  every other session that shares it, and `rm -rf node_modules/` deletes through the link the
  same way.
- If the lockfiles differ, don't link: `npm ci --prefer-offline` (the `~/.npm` cache is warm).
- `npm install <pkg>` replaces the link with a real tree. That is fine — it is just no longer
  shared, so don't be surprised by the disk.

The same question for the other ecosystems (what a worktree already shares, what it duplicates,
and the measurements) is `docs/build/93-worktree-deps.md`.

## Running the Go tests

The two Go modules are separate; run each from its own directory. Both suites build and run
packages in parallel, which is the usual way to exhaust memory on this host — cap it when the
container is busy.

```
(cd control-plane   && go test ./...)      # add -p 2 when memory is tight
(cd workspace/agent && go test ./...)
```

Postgres-backed tests skip themselves unless `AF_TEST_DATABASE_URL` is set; there is no local
database (and no Docker) in the workspace, so leave them skipped. The full build/reflect matrix
is `docs/build/10-development.md`.

## Verifying your own work

- **Take exit codes without a pipe** (`cmd > out; echo $?`). `| tail` returns tail's `0` — a red
  run looks green — and `| grep -v` under `pipefail` returns `1` when it matches nothing, so an
  all-green run looks red. Both directions have cost us real time here.
- **Before reporting "0 hits" or "green", show that the tool caught something it should catch.**
  An empty result and a scanner that never ran look identical, and so do a check that passed and a
  check that matched nothing.

The long form — 30 rules, each with the concrete defect behind it — is in the developer work
journal for the 2026-09 parallel refactor.

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
`guide/ref/browser-pane.md`.

# 0124. Second refactor: ratchets first, one place to register a change, agent kinds as plug-ins

English | [日本語](0124-second-refactor.ja.md)

- Status: **accepted** (2026-10-28). A plan; nothing is built yet. Phases 0–3 are the current
  scope, in that order. Phases 4–6 are recorded so they are not rediscovered, and run only when
  their churn justifies them.
- Follow-ups: #2853, #2854, #2855, #2856, #2857, #2858, #2859, #2860, #2861, #2862, #2863, #2864,
  #2865, #2866, #2867, #2868, #2869, #2870, #2871, #2872, #2873, #2874, #2875, #2876
- Related: [0067](0067-parallel-refactor.md) (the first wave, its verification practice and what it
  left) / [0012](0012-go-internal-refactor.md) (the layering, the two binaries, no shared module) /
  [0011](0011-console-rebuild.md) (the feature-sliced Console)

## Context

Measured on `main` at `5bcfc0110` (2026-10-28). "package main" means the non-test `.go` files
directly under a module directory, the yardstick 0067 used. The commands are under "Measuring".

| | End of 0067 (2026-09-04) | Now |
|---|---|---|
| CP package main | 96 files / 28,321 lines | **198 / 75,582** |
| Agent package main | 64 / 15,373 | **129 / 37,322** |
| Console `src/` (non-test TS/TSX) | 122,094 lines | **179,895** |

🔴 **Correction (2026-10-28):** the Console figure is **180,004** lines. The command behind 179,895
dropped every path containing `test`, which also removed the helper files under `console/src/test/`;
"non-test" here means excluding `*.test.*` only. The command is added under "Measuring".

3,898 non-merge commits landed in between. The regrowth is not one bad family. The engine family
(ADR 0071–0099) is 44 files / 24.0k lines of the CP's main; the rest is a dozen features (OAuth,
egress, the dev IdP, inbox, scheduler, …) that each landed in main because nothing asked them to go
anywhere else. **0067 shrank main and installed nothing that keeps it small**, and the result held
for under two months.

Size is not what slows a change down, though. Hot files and registering one thing in several places
are:

| What you add | Places you edit today | Churn since 2026-09-04 |
|---|---|---|
| An Agent route the CP relays | the agent's route and its golden, then up to four in the CP: `routes.go`, `testdata/routes.golden`, the audit switch `auditActionTarget` (`proxy.go`), `memberOriginRoutes` (`member_origin.go`) | `routes.go` 119 commits, `routes.golden` 127; 334 hand-mirrored `, rest)` lines |
| An MCP tool | five places in `internal/mcpx/mcp_stdio.go` (5,001 lines): the schema table, the shared argument struct, `mcpCheckCallArgs`, the advertise gate and the call gate, the switch arm — plus a second definition in `control-plane/internal/mcpsrv/mcp.go` | 93 commits |
| A Console preference | five spots spread over ~1,800 lines of `lib/settings.ts` | 57 commits |
| An agent kind | muse touches 139 non-test, non-Markdown files outside its own two packages | — |

Duplication has started to produce defects, not only extra work:

- **The two MCP tool definitions have drifted.** The agent's operator-side `create_session`
  description lists every kind except muse (`mcp_stdio.go:2293`). The two copies are written in
  different languages on purpose (`mcp_stdio.go:722-735`: session-facing text is English to save
  tokens, the operator's stays Japanese); that part is not the defect.
- **copilot, cursor and kiro's `driver.go` are 83–90% the same** once the kind name is normalised;
  their `acp.go` differ in 12 of 207 lines. Cross-cutting fixes have had to land in all seven
  managed drivers at once (`c70e8cb0a`, `2a9b6bbaf`).
  🔴 **Correction (2026-10-28):** "83–90%" and "12 of 207 lines" came from an unstated method and one
  pair. With the three kind names normalised to one token and comments and blank lines dropped, the
  code of `acp.go` is 95–100% shared between each pair, and `driver.go` shares 69–83% of the larger
  file's lines (87–92% of the smaller's). The command is under "Measuring".
- **The kind registry sits at the top of the agent's import graph** (`internal/sessionx/agent.go:34`),
  so the packages below it keep their own kind lists. Kind-literal branches: 219 in the agent
  outside `internal/agents`, 66 in the Console outside `agents/registry.ts`, 51 `.kind-<kind>`
  selectors in 6 CSS files.

Missing checks, found in passing:

- Nothing verifies that every CP relay exists in the agent's route table, or that every mutating
  relay has an audit action.
- `console/scripts/css-dup.mjs` exists but CI never runs it. 67 class names start rules in more than
  one file; `.repo-identity` is styled with different values in `scm/scm.css` and
  `settings/parts/common.css`, so load order decides.
- Two 0067 alias files survive in the Console (`features/settings/mcpWire.ts`, `mcpForm.tsx`). 0067's
  "zero aliases" counted Go only.

CI is green on `main` and takes about six minutes, so the gates this plan leans on can be trusted.

## Decision

1. **Ratchets before moves.** Every phase leaves behind a check that fails when what it fixed comes
   back. A ratchet must not become a hot file of its own: it changes when someone does the thing it
   guards (adds a file to main, adds a kind branch), never on every feature PR. Hence a file
   allowlist for package main, not a line budget.
2. **Choose by churn × places touched, not by size.** A large cohesive package that rarely changes
   (`imagegen` and its ComfyUI code) waits. The question for each candidate is how many places the
   next feature has to edit, and how often that happens.
3. **A pure move and a change are never the same PR** (0067 Decision 9). A whole-file move shows
   zero content change under `--find-renames`. A file split keeps the multiset of top-level
   declarations and the multiset of string literals (go/ast `BasicLit`, or the TS equivalent)
   identical before and after — the check 0067's journal adopted after a mechanical rename
   rewrote an MCP description.
4. **Split a hot file only while no open PR edits it.** Git carries a feature branch's edits through
   a whole-file rename; it does not carry them through one file split into ten, and every hunk
   becomes a conflict. Before a split, list the open PRs that touch the file; split in the gap and
   merge promptly.
5. **Find what reads the files you move, across modules.** Go tests read Console sources by path
   (the `settings.ts` drift tests, `lib/fleetgraph.states.json`, the locale `errors.ts`);
   `console_routes_test.go` needs the Console's API paths as string literals; some Console tests read
   Go sources. A move updates both sides in the same PR.
6. **Do not move** the CP's `manager` (159 methods over 36 files) or the workspace family at its
   core, handler structs, unexported errors, or JSON blobs (0067's do-not-move shapes). Everything
   reaches them, and moving them changes lock scope or wiring without removing a single
   registration step.
7. **Operating model.** One issue per work package and one PR per issue, implemented by child
   sessions, with the files each issue owns named in the issue. A reviewer on a different model
   reads every PR before merge; the maintainer merges. Phase 1 items own disjoint files and run side
   by side. Items that change a registration point (Phases 2 and 3) run one at a time per hot file.
8. **A live pass closes Phase 3.** Kind changes touch session start, resume and shutdown: create,
   chat, stop and resume one session of every kind on a real deployment. Both of 0067's live passes
   found a defect that every gate had passed.

### Phase 0: ratchets and missing checks

- CP tests: every relay exists in the agent's route table; every mutating relay has an audit action.
- Ratchets: a package-main file allowlist in both Go modules; a ceiling on kind-literal branches in
  the agent and the Console that fails on an increase.
  🔴 **Addendum (2026-10-28):** the ceiling's kind set comes from the kind catalog, or the test fails
  when its list differs from the catalog. A hard-coded list never counts a new kind's branches, which
  is exactly the growth the ratchet exists to stop.
- Console: `css-dup` in CI as a ceiling at today's count; an import-boundary lint (`lib/` may not
  import `features/`, with today's exceptions listed); delete the two alias files.
- Remove dead code in both Go modules (for example `dockerContainerID` in `metrics.go`,
  `memClampNote` in `mem.go`, `sortQueriesByPosition` in `workitems.go` have no callers).
- Fix the muse omission in the operator's `create_session` description.

### Phase 1: in-package file splits (pure moves)

- `internal/runtime/runtime_ecs_ec2.go` (7,066 lines) along its existing `// ---` sections.
- `internal/store/store_sqlite.go` into `store_<domain>.go`, the shape newer store code already has.
- `engine_admin.go` into routes, models and ingest.
- `internal/mcpx/mcp_stdio.go` into one file per tool family, plus the Agent client and caller
  resolution.
- Console: `app/WsBar.tsx` into `app/wsbar/`; `adminEngineAdd.tsx` into its five components;
  `engineTypes.ts` into types and hook.
- The catch-all locale files into per-feature files (`tools.ts` holds 476 `mem.*` keys; `admin.ts`
  holds 426 engine and catalogue keys).

### Phase 2: one place to register

- **CP relays become a declarative table**: one row `{method, path, audit, signed}` per relay, one
  file per domain. Registration, the audit classification and member-origin signing are derived
  from it, on top of Phase 0's two tests and the existing `routes.golden`. Generating the table from
  the agent's routes is a later step, not this one.
- **One `toolSpec{schema, gate, args, handler}` per MCP tool**, kept with its family after the Phase 1
  split. The kind enumeration in tool schemas and descriptions, on both the CP and the agent side,
  is built from the kind catalog (`agentkinds/catalog.json`). The descriptions remain two texts for
  two audiences.
- **One descriptor per Console preference** (key, default, scope, normaliser). The server-sync
  engine, the code that applies theme and fonts, and per-feature option lists leave `settings.ts`.
  The Go drift tests that read `settings.ts` change in the same PR.

### Phase 3: agent kinds as plug-ins

- A shared ACP base for copilot, cursor and kiro: first the near-identical `acp.go`, `mirror.go` and
  `progress_managed.go`, then the driver body with per-kind hooks.
- The scaffolding the seven managed drivers repeat (`NewDriver`, `handleFor`,
  `ManagedAlive`/`Busy`/`InFlight`, `DropHandle`, `RemoveLedger`, `ReconcileManaged`,
  `AbortManaged`) becomes shared.
- The kind registry moves below `sessionx`, so every package can consult it. Lifecycle (reconcile,
  abort, shutdown), connection status, live and modal probes, skills layout and model listing
  become optional `Agent` interfaces, and `main.go`, `shutdown.go`, `connections.go`,
  `session_io.go` and `chat_providers.go` loop over the registry instead of switching on the kind.
  `internal/agents` imports `bridge`, so watch for import cycles.
- The CP (`mcpsrv/mcp.go`, `tenant_member_defaults.go`, `limit_board.go`) and the Console (seven
  hand-kept kind lists, `isDynamic` in `lib/agentModels.ts`) read the catalog and the registry's
  capabilities; per-kind CSS becomes one custom property per kind.
- **Target: a new kind touches at most 40 non-test, non-Markdown files outside its own packages**
  (muse: 139). Phase 3 finishes before pi or rovo is built.

### Phases 4–6: recorded, run when churn justifies them

- **4 — packages.** The CP's engine domain moves into `internal/engine` behind an interface the
  package defines; the HTTP handlers stay in main. First separate the TTS lifecycle
  (`engine_control.go`, `engine_ecs.go`) from the engine lifecycle, and make
  `errcodes_catalog_test.go` read the new package, or the engine's error codes (about fifty)
  silently leave the check. About 15 engine files (~6k lines) reference none of `manager`,
  `config`, the auth helpers or the HTTP error helpers, and go first. In the agent: the main
  families with little reach-back (fs, jira with workitems, usage, svn); `createSession` (about 600
  lines) broken into stages as a logic change of its own.
- **5 — Console data layer.** A typed `api.ts` per feature and shared load and save hooks, starting
  with settings (146 of the Console's 223 explicit `any`); single-consumer `lib/` modules back into
  their features; CSS duplicates to zero with `css-dup` failing CI; badge, chip and card primitives
  in `ui.css`.
- **6 — CI and shell.** One CLI manifest: the version is a single Dockerfile `ARG`, but each CLI is
  also listed in six or seven hand-kept tables (the `setup-agent-cli` action, `cli-drift-check.sh`,
  `cli-pin-bump.sh`, `cli-release-watch.yml`, the `install_*.go` code). The eight
  `*-contract.yml` as one reusable workflow. Boot-install out of `entrypoint.sh` into
  `workspace-agent`, one CLI at a time, each verified by a live boot.

**Addendum (2026-10-29) — Phase 4 narrowed to "4a".** The maintainer narrowed Phase 4 to the two
areas whose churn justifies it now (Decision 2: churn × places touched). Measured on `origin/main`,
non-merge commits, 2026-10-29:

| Area | Non-test files / lines | Commits 90d | Commits 30d |
|---|---|---|---|
| CP engine (`engine*.go`) | 44 / ~24k | 196 | 13 |
| TTS (`tts*.go`, `engine_control.go`, `engine_ecs.go`) | 6 / ~2.7k | 27 | 0 |
| agent fs | 11 / ~4.2k | 40 | 13 |
| **agent jira + workitems** | 16 / ~4.1k | 60 | **42** |
| agent usage | 8 / ~2.5k | 52 | 12 |
| agent svn | 4 / ~1.8k | 13 | 3 |
| **`createSession`** (`internal/sessionx/session_handlers.go:710-1287`) | 1 func, 577 lines | — | **21** |

4a is the agent's jira and workitems families out of package main as pure moves (#2929; the jira
family, with the Jira REST client it calls from `connections_jira.go`, goes to `internal/jirax`
first because workitems depends on it) and `createSession` broken into stages (#2930). The CP
engine and TTS, fs, usage and svn stay deferred until their churn justifies them.

## Rejected

- **0067's alias transfer under a commander session.** Phase 1 splits stay inside one package and
  Phases 2–3 change shapes rather than move them, so there are no aliases to collect, and nothing a
  hub would coordinate that the issue list does not.
- **A line budget for package main.** Every feature that grows a main file would edit it, which
  makes the ratchet itself a hot file.
- **Splitting `manager`.** Its 159 methods are the CP's runtime state and its locks. Extracting them
  changes lock scope — a behaviour change — and removes no registration step.
- **Generating TS types, or a shared Go module, now** (0012's condition for revisiting). 372 JSON
  write sites still return `map[string]any` (agent 223, CP 149), which a generator cannot type, and
  the agent image's build context is `workspace/agent/` alone.
  🔴 **Correction (2026-10-28):** 372 is the number of grouped entries in the two `wiremap.golden`
  files (agent 223, CP 149); each entry folds identical (file, function, shape) sites into one line
  with an `xN` count. The write sites number **426** (agent 264, CP 162).
- **A refactor freeze.** Around seventy commits a day land on `main`; a freeze stalls all of them to
  protect a handful of hot files. Decision 4 protects exactly those files for exactly as long as a
  split takes.

## Consequences

- Until Phase 0 lands, new main files and kind branches keep arriving. Phase 0 goes first because
  every later phase is measured against it.
- The ratchets will refuse some legitimate changes. The allowlist or ceiling is then edited in that
  PR, with the reason in its description.
- Phase 3 touches session start, resume and shutdown for every kind and carries `needs-live-run`.

## Measuring

Run from the repository root.

```sh
# package main: files / lines (run per module dir)
find control-plane -maxdepth 1 -name '*.go' ! -name '*_test.go' | wc -l
find control-plane -maxdepth 1 -name '*.go' ! -name '*_test.go' -exec cat {} + | wc -l

# kind-literal branches, agent (outside internal/agents) and Console (outside agents/registry.ts)
K='claude|codex|opencode|muse|cursor|kiro|copilot|agy|lcpp'
KC='Claude|Codex|OpenCode|Opencode|Muse|Cursor|Kiro|Copilot|Agy|Lcpp|LCPP'
find workspace/agent -name '*.go' ! -name '*_test.go' ! -path '*/internal/agents/*' ! -path '*/testdata/*' -print0 \
  | xargs -0 grep -hE "(case .*(\"($K)\"|Kind($KC)\b))|((==|!=) *(\"($K)\"|session\.Kind($KC)\b|Kind($KC)\b))|((\"($K)\"|Kind($KC)\b) *(==|!=))" | wc -l
find console/src \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.*' ! -path '*/agents/registry.ts' -print0 \
  | xargs -0 grep -hE "(===|!==) *[\"'](${K})[\"']|[\"'](${K})[\"'] *(===|!==)|case [\"'](${K})[\"']" | wc -l
grep -rhoE "\.kind-($K)\b" console/src --include='*.css' | wc -l

# files a kind touches outside its own packages (muse)
git grep -l -i -w muse -- ':!*_test.go' ':!*.test.ts' ':!*.test.tsx' ':!*.md' \
  ':!workspace/agent/internal/agents/muse' ':!workspace/agent/internal/msp' | wc -l

# hand-mirrored relays
grep -cE ', rest\)' control-plane/routes.go
```

Added 2026-10-28 with the corrections above:

```sh
# Console non-test TS/TSX lines (excludes *.test.* only)
find console/src \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.*' -exec cat {} + | wc -l

# map[string]any write sites: grouped golden entries, then sites (the sum of xN)
for f in control-plane/testdata/wiremap.golden workspace/agent/testdata/wiremap.golden; do
  grep -vE '^(#|$)' "$f" | awk '{n=1; for (i=1;i<=NF;i++) if ($i ~ /^x[0-9]+$/) n=substr($i,2)+0; s+=n} END {print NR, s}'
done

# code shared by the ACP drivers: kind names to one token, comments and blank lines dropped
python3 - <<'PY'
import difflib, itertools, re
def norm(p):
    return [l for l in (re.sub(r'(?i)copilot|cursor|kiro', 'KIND', re.sub(r'//.*', '', x)).strip()
                        for x in open(p)) if l]
d = 'workspace/agent/internal/agents/'
for f in ('acp.go', 'driver.go'):
    for a, b in itertools.combinations(('copilot', 'cursor', 'kiro'), 2):
        A, B = norm(f'{d}{a}/{f}'), norm(f'{d}{b}/{f}')
        m = sum(x.size for x in difflib.SequenceMatcher(None, A, B, autojunk=False).get_matching_blocks())
        print(f, a, b, f'{m / max(len(A), len(B)):.0%} of larger, {m / min(len(A), len(B)):.0%} of smaller')
PY
```

## Addendum (2026-10-28) — the css-dup ceiling counts what the script counts

Context's "67 class names start rules in more than one file" is not a number `css-dup.mjs`
prints, and it was not reproducible from it. The Phase 0 ceiling (#2855) therefore uses the
script's own two counts on `main` at the time: 200 class names defined in two or more files, of
which 36 are bare (`.name {}` alone) in two or more files. CI runs `npm run css:dup:ci`
(`--max-dups=200 --max-bare=36`); either count growing fails. A count that falls does not: the
script prints a hint and lowering the ceiling is a separate, optional edit, so a PR that clears a
duplicate in passing never has to touch `package.json` (Decision 1).

## Addendum (2026-10-28) — the ratchets count the registry's kinds

The Phase 0 ratchets are in place (#2854). `main_files_test.go` in each Go module holds
package main to `testdata/main-files.txt`. The kind-literal ceilings
(`workspace/agent/kind_literal_ceiling_test.go`, `console/src/test/kindLiteralCeiling.test.ts`) use the
expressions above but take the kinds from the registry (`sessionx.RegisteredKinds()`; the Console's
`SESSION_KINDS`, held equal to the catalog) minus the non-agent `shell` and `ssm`, and match Go
identifiers as `Kind<kind>` in any case, so a new kind is counted from the PR that registers it. The
`K`/`KC` lists above are today's expansion of that set; both give 219 and 66. As with css-dup, a count
above the ceiling fails and one below it only logs a hint to lower it.

## Addendum (2026-10-29) — "files a new kind touches" is measured by adding one

The Phase 3 target's figure is redefined (#2927); the target itself, at most 40, is unchanged.
The `git grep -l -i -w muse` command under "Measuring" counts every file that names muse —
comments, workflows, goldens, UI strings — and not the files a new kind has to edit: it moved
only from 152 to 145 across Phase 3, which removed 85 Go and 9 Console kind branches.

**New definition.** The number of files that adding the stub kind `zz` changes outside its own
package (`workspace/agent/internal/agents/zz`), excluding tests (`*_test.go`, `*.test.ts(x)`) and
Markdown, once both Go modules' tests, the Console's typecheck and vitest, and docs-check pass.
The stub is the smallest kind the checklist (`docs/build/20-add-an-agent.md` §20.7) allows: it
registers, launches `bash -l` on the Terminal route, and has no Managed driver, MCP, instruction
target, sign-in or pinned CLI. It is never merged. The recipe is `scripts/stub-kind-count.sh`
(`--verify` runs the suites), described in §20.8 of that chapter.

**Measured at `aee59bfbe`: 16** (25 including tests and Markdown).

| Category | Files | Which |
|---|---|---|
| Go | 5 | `session.go` (`Kind*`), `agentreg.go`, `kinds.go`, `agent_instructions.go` (`instrUnsupported`), `session_move.go` (`moveUnmeasuredTargets`) |
| TS | 3 | `types/session.ts` (`SessionKind`, `SESSION_KINDS`), `agents/kindCatalog.ts` (`FALLBACK_KIND_FACTS`), `agents/registry.ts` (descriptor) |
| CSS | 1 | `styles/tokens.css` (two theme tokens and the `.kind-` map line) |
| Data | 7 | `agentkinds/catalog.json` and the MCP tool goldens `testdata/mcp_tools/{list.json,calls.txt}` of the agent's `mcpx` and the CP's `mcpsrv` (all five regenerated, not hand-edited), the `sessions.ts` locale in `en` and `ja` |
| CI and scripts | 0 | |
| Tests (not counted) | 7 | `kinds_test.go`, `managed_runtime_test.go`, `prompt_paste_test.go`, `session_move_kinds_test.go`, `kind_lists_test.go`, `derivedKindLists.test.ts`, `availability.test.ts` (the stub needs no connection, so it is offered with shell) |
| Markdown (not counted) | 2 | `guide/ref/agents.md` and `.ja.md` |

A guard or the compiler forced 13 of the 16. The other three follow the guide: the two locale
files (the launch hint has to be a message key, though an existing one would compile) and the
move list (an unclassified kind reads `not_an_agent`, which is false for an agent). Each
kind-specific row of §20.7 adds files the stub does not exercise; most of them are the
pinned-CLI registration in CI and `deploy/local` that Phase 6 already names. The edits a registry
could absorb are #2933.

## Addendum (2026-10-29) — the managed drivers' `threadHandle` methods stay per kind

Phase 3's managed scaffold (#2869) shares the package-level functions only. The methods the
handles repeat are **not extracted** (#2918).

**Measured at `2012d2229`.** Copilot, cursor and kiro embed `acp.Handle`, so there are five
implementations: acp, codex, opencode, muse, lcpp. Method bodies compared with comments and blank
lines dropped and `h.tq()` read as `h.q`:

| Identical in | Methods |
|---|---|
| 5 of 5 | `Events`, `RemoveQueued`, `DismissDiscard` |
| 4 (all but muse) | `emit`, `Send`, `parkForMove`, `Abort` |
| 4 (all but lcpp) | `Snapshot`, `Alive` |
| 3 | `SendQueued` (acp, codex, lcpp); `Interrupt`, `interruptAll` (codex, opencode, muse); `Busy`, `HoldsEntry`, `Steer` (acp, opencode, lcpp) |
| different in every kind that has it | `accept`, `pump`, `runTurn`, `Respond`, `UpdateSettings` |

The nine methods identical in four or more come to about 60 lines per kind. The issue measured
seven methods and about 45 lines at `29f1b6b57`, before #2869 moved `Alive` and `Abort` onto the
handle.

**Cost.** Every one of them reads the handle's lock, queue, state, events, alive or running
fields, so sharing them needs an embedded core with those fields exported. That means rewriting
683 `h.mu` sites, tests included (codex 150, opencode 181, muse 264, lcpp 88), plus the other field
accesses, and redoing the interface-assertion proof for four more kinds. Under Decision 2, about
60 lines per kind does not pay for that churn.

```
for k in codex opencode muse lcpp; do cat workspace/agent/internal/agents/$k/*.go | grep -o '\bh\.mu\b' | wc -l; done
```

**Reopen** only if a cross-kind fix has to land in these methods again.

## Addendum (2026-10-30) — workspace MCP tools decode into their own argument struct (#2902)

The union `mcpCallArgs` is gone. `toolSpec.handle` is a `toolHandler`, built by
`handler(func(req, p, a *T) []byte)`: the call check decodes the arguments into a fresh `T`
that the handler reads, and `T` lives in the tool's family file. A new argument therefore edits
that file only. `noArgs` marks a handler that reads none, and `ownArgs` keeps its meaning (a type
error is the handler's, not a refusal ahead of it).

Two constraints came out of it. The type-error text is wire (`invalid argument "name" for ...`,
the call golden pins it), and `encoding/json` puts an embedded type's name into
`UnmarshalTypeError.Field` (`nameArg.name`), so the args structs repeat a shared field instead of
embedding it; `TestToolSpecsAreWellFormed` refuses an embedded field. `TestMCPArgsSchemaAndDecoderAgree`
now holds each tool's args struct to exactly the properties that tool advertises, in both
directions, so a field that drifts back into a shared struct fails it.

One edge moved: a `tools/call` for a name with no spec used to get a type-mismatch refusal
decoded through the union ahead of `unknown tool`; it now gets `unknown tool` directly, because
there is no struct to decode into. A name that is not a tool is not in any golden.

## Addendum (2026-10-30) — rate-limit resume asks `agents.UsageLimiter` (#2925, first slice)

`internal/sessionx/rate_limit_resume.go` held 7 of the kind-literal branches counted after
Phase 3: which kinds the sweep watches, the claude-only pane menu read, and the at-limit and
reset-instant questions. They are now `agents.UsageLimiter` (`agents/usagelimit.go`), implemented
by claude, codex and muse; `kindLiteralCeiling` goes 133 → 126. None of the seven was "one
product's behaviour" that has to stay: the claude details (banner-only reset trust for a limit
without a menu) moved into the claude package with the capability. A kind without the capability
is not watched, which is what the old `switch` returned for every other kind.
`TestUsageLimitTraitsMatchOldSwitch` holds the answers to a copy of the old switch over every
registered kind and driver. The remaining sites stay in #2925.

## Addendum (2026-10-30) — four registry-restating tests retired (#2933, first slice)

`TestLabels`, the nil rows of `wantRuntime`, `literalKinds` and `moveClassification` /
`TestMoveTargetListsAreUnchanged` no longer need an edit when a kind is added: each now derives
its rows from the registry or from the production list it checked, and keeps only what is
measured or shipped (labels, runtime function identities, `pasteKinds`, the three move refusals).
`scripts/stub-kind-count.sh` on the refreshed `zz.patch`: 22 files with tests and Markdown (was
25; test files 5 → 1), 17 non-test, non-Markdown files (was 16). The extra file is
`sessionx/testdata/create_refusals.golden`, which lists the accepted kinds and which the script now
regenerates; without it the count read 16. Not a full `--verify` (busy host): the stub's clone
passes the `kinds`, `sessionx`, `agentreg`, `mcpx` and root packages of the agent and the CP's
`mcpsrv` and `agentkinds`. `usageMeasuredByKind` and `noChatProvider` stay: nothing declares their
"none" answer per kind, and a case line for it would raise the kind-literal ceiling.

## Note (2026-10-31): login recovery through `agents.AuthRecoverer` (#3021 stage 3)

codex gained the live `auth` state, the `auth_expired` send refusal and the auto-resume after a
re-authentication without a new `kind == "claude"` branch. `agents.AuthRecoverer` is the small
per-kind seam: `LastFailure` (the newest turn ended on the login, with its time) and
`CredentialsWrittenAt` (a credentials file's mtime). `sessionx/auth_recover.go` reads it by exact
registry key; claude does not implement it and keeps `TerminalTraits.AuthExpired` / `claude.Abort`.
The rule is the one claude already had: a session is resumed once the credentials were written
after the failed turn, never on the CLI's own "login status". `authResumeState.Kind` records which
kind's file the episode is judged on (empty = claude, so stored episodes and claude's tests are
untouched). For recoverer kinds a repeated login failure moves the episode clock to the new
failure, so a login that was "renewed" with something still revoked does not loop on the 5-minute
backoff until the cap.

Follow-ups: #3122 (the Console's `auth_expired` toast and re-auth target are still Claude-only).

## Note (2026-11-01): `instrUnsupported` retired (#2933, second slice)

"This kind cannot take agent instructions, and why" is now the kind's own declaration:
`agents.InstructionsUnsupported` (`InstructionsUnsupported() string`, the reason code), implemented
by `cursor` in `internal/agents/cursor/instructions.go` together with the measurement that used to
sit on the slice. `agent_instructions.go` derives the screen rows, `GET /agents/kinds`'
`instructionsReason` and the tri-state answer from the registry by exact key, so an alias or an
unknown kind answers as before. `TestInstrUnsupportedMatchesOldList` holds the answers to a literal
copy of the old slice over every registered kind. `scripts/stub-kind-count.sh` on the refreshed
`zz.patch` (the stub now declares its answer inside its own package): 21 files with tests and
Markdown (was 22), 16 non-test, non-Markdown files (was 17); Go 5 → 4. Not a full `--verify` run.
Still hand-kept: `session.Kind*`, `agentreg`, `kinds.registered`, `moveUnmeasuredTargets`
and the Console lists.

Follow-ups: #2933.

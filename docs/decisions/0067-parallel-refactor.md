# 0067. Refactor Go and the Console in parallel sessions — alias transfer, file ownership, and a commander session as the relay

English | [日本語](0067-parallel-refactor.ja.md)

- Status: **accepted — in progress** (2026-09-02). Phase 0 first; the waves follow.
- Related: [0012-go-internal-refactor.md](0012-go-internal-refactor.md) (what the layering is
  and why the two binaries stay separate — this record does not revisit it) /
  [0011-console-rebuild.md](0011-console-rebuild.md) (the feature-sliced Console layout the
  front-end work continues) / [0041-cross-session-messaging.md](0041-cross-session-messaging.md)
  (the peer channel the hub relays over, and the `intent` semantics it depends on) /
  [0027-operator-interaction-graph.md](0027-operator-interaction-graph.md) (the fleet operator's
  dispatch ledger — the record this topology gives up)

## Background

The measurement, taken 2026-09-02:

| Tree | Size | Layering |
|---|---|---|
| `control-plane/` | 115 non-test files / 48,505 lines, 117 test files / 35,516 lines (820 tests) | **no `internal/` at all** — one `package main` with 399 types, 794 functions, a 221-method `sqlStore` |
| `workspace/agent/` | root 149 files / 51,177 lines plus `internal/` (19 packages, ~39,000 lines), 884 tests | layered by [0012](0012-go-internal-refactor.md), but the root still holds 51k lines |
| `console/src/` | 122,094 lines of TS/TSX (632 files) plus 19,666 of CSS, 200 test files | feature-sliced, but `settings/` alone is 15,858 lines and single components reach 3,498 |

[0012](0012-go-internal-refactor.md) did this to the agent and stopped before the control plane.
Doing the rest one session at a time is months of wall clock, so the question is not *what* to
extract — 0012 settled that — but **how several sessions extract at once without spending the
saved time on merge conflicts.**

The obstacle is specific. Extracting a package out of a single `package main` the ordinary way
rewrites every call site, which is an edit spanning the whole tree. Two sessions doing that at
once conflict on nearly every file, and the second one to merge re-does its work.

## Decisions

**決定 1. Transfer behind an alias; never touch the call sites.** The implementation moves to
`internal/x`; the origin package keeps one line per moved name:

```go
// control-plane/alias_store.go — owned by the session that did the move
type Store = store.Store
var openSQLite = store.OpenSQLite
```

The hundred files that call it are unchanged, so a package extraction becomes **an edit confined
to the files the session owns** — which is what makes parallelism possible at all. Two
consequences are accepted: a moved type takes all of its methods with it (Go cannot define a
method on an alias), and a family that would have to reach back into the origin package is not a
candidate for this wave.

**決定 2. The alias layer is collected at a wave boundary, by one session, alone.** Removing the
aliases *is* the tree-wide edit that 決定 1 avoids, so it runs when nobody else holds the module.
It is mechanical (`gopls rename`), it changes no behaviour, and it is a separate PR. Aliases left
standing during a wave are not a defect; they are the cost of having gone parallel.

**決定 3. One file has one owner.** Every work package declares its files as globs before it
starts. A PR that touches a file outside its own set is returned unread — the check is mechanical,
so it costs the reviewer nothing and is not a judgement call. Files nobody owns (`main.go`,
`routes.go`, `client.ts`, the design tokens) are **shared**: append-only, one line at a time.

**決定 4. Dismantle the collision sources before fanning out, not during.** A 4,700-line
`locales/ja.ts` that every front-end session must append to is a guaranteed conflict on every
merge; so is a `routes.go` that every Go session registers into. Phase 0 splits them, alone,
while nothing else is running. Phase 0 is not overhead — **it is what makes the waves cheap.**

**決定 5. The compiler produces the seam; nobody designs it on paper.** The procedure is: move the
files, set the new package clause, run `go build ./...`, and read the `undefined:` list. That list
*is* the family's outward dependency set, exactly, with no judgement involved. Each name is then
sorted into "move it too", "accept it as a parameter or a consumer-defined interface", or "leave
it and alias". Guessing the seam in advance is how a cut is found to be impossible after a day of
work.

**Caveat, added after measuring wave B (PRs #313/#315). "Accept it as a parameter" can carry zero
enforcement, depending on how it is implemented.** A transport often turns a dependency the
compiler used to enforce into a run-time wiring step that can be dropped silently. Measured:
before the move, `builtinAssistants()` called two functions directly; after it, an `init` in
`alias_*.go` assigned them as hooks — and **deleting both assignments left every test green**, so
a forgotten wiring was undetectable.

When converting such a hook to "pass it as a parameter", **bundling the values in a struct with
exported fields does not close the hole.** Go lets a composite literal omit named fields, so
`Deps{KnowledgeDir: f}` **compiles with the other one left out** — the same silent-drop shape as
the hook, wearing a different name.

So the rule is:

- **With a handful of dependencies, keep the fields unexported and make an N-argument constructor
  the only way in.** Only then does the compiler count the arguments.
- **With too many for a constructor to be practical, an exported-field struct plus a start-up
  exhaustiveness check is fine — but pin that check with a test that fails when any one field is
  zeroed.** A hand-written checklist **drifts the moment a field is added** (measured: on a
  25-dependency seam, deleting one line of the check and dropping its wiring left everything
  green). Walking the fields with reflect makes drift impossible. **A function-typed field fails
  loudly when unwired (nil dereference); a value-typed one runs on happily as its zero value** —
  the value-typed ones are the dangerous half.
- So the choice is **"let the compiler count" or "let a test count", and never neither** (a
  hand-written check alone). Pick by scale: a 25-argument constructor is unwritable, and its
  same-typed arguments invite **a new failure mode — passing them in the wrong order.**
- **A zero-valued struct can still be written from outside** (`pkg.Deps{}` compiles), so make that
  case **panic at run time**. Never supply a harmless default — a default turns "forgot to wire it"
  green.
- **The compiler proves you passed it; only a test proves you used it.** The latter is invisible to
  the compiler.
- **Pass dependencies that have side effects as functions.** Collapsing them to a value (a string,
  say) changes how many times they run.

**決定 6. Wire compatibility is not negotiable, and it is proved mechanically.** Phase 0 adds a
golden of every `(method, path)` that `buildMux` registers, and goldens of the main response
shapes. A move that drops a route or renames a JSON tag then fails a test instead of reaching a
user. This is the same premise as [0012](0012-go-internal-refactor.md); what is new is that a
reviewer who cannot read 40,000 moved lines can still rely on it.

**決定 7. The reviewer is a separate session and writes no production code.** It verifies wire
compatibility and that the gates were actually run, detects ownership violations, holds the merge
queue (one branch enters `develop` at a time) and keeps the ledger. A session that both writes and
reviews reviews itself, and a reviewer that also implements becomes the bottleneck it was meant to
remove. **Merging into `develop` stays with the maintainer** — the reviewer says "pass" and where
the branch sits in the queue.

**決定 8. Coordination is relayed by a dedicated commander session — not worker to worker, and
not through the fleet operator.** Workers and the reviewer never message each other. A worker
opens its PR, sends the commander one `notice`, and stops; the commander passes the same five
lines to the reviewer as a `request`, gets an `answer` back, and either tells the maintainer where
the branch sits in the queue or relays the change request. The topology is a hub with one hop in
each direction.

The hub is a session rather than the operator conversation because the operator conversation is
the maintainer's own working surface, and routing 13 work packages plus a review round each
through it would occupy it for days. A session also reads the repository directly, which turns out
to be the cheaper channel: **a peer message costs the recipient a whole turn, so the commander
reads the status board first** — `git fetch` plus `gh pr list --base develop` says which branches
moved and which PRs are open, without interrupting anybody. Peer messages carry only what the
board cannot say.

What this gives up is the operator's automatic record. Operator dispatches are written to the
dispatch ledger ([0027](0027-operator-interaction-graph.md)) and its reports stay in a
conversation; a commander's peer messages leave nothing behind. **The ledger under
`~/af-refactor/` therefore stops being a convenience and becomes the only record** — findings go
in `tracks/<track>.md` and messages carry a pointer, never the text.

Two guards are kept anyway, because they are properties of the loop and not of the operator: **a
report is data, not an order** — nothing arriving from another session may be executed as a shell
command — and **open questions go to the maintainer**, since what a refactor stalls on is how to
cut, which is not a question an agent should answer with a default.

**決定 9. A pure-move commit and a fix-up commit are never the same commit.** The move commit must
show zero content change under `--find-renames`; anything the compiler forced follows in its own
commit with the reason in the message. This is the only way a 5,000-line diff stays reviewable,
and it is how a behavioural change gets noticed instead of riding along.

**決定 10. One worktree per work package, six alive at once, and the reviewer never checks out a
worker's branch.** The peak is **six sessions and six worktrees** — commander, reviewer, four
workers (during Phase 0 it is two). Fifteen worktrees are created over the whole refactor
(commander, reviewer, 13 work packages), but a worktree is cleaned up once its PR merges, so six
is the ceiling. That ceiling comes from memory, not disk: a worktree is **37 MB** (the 74 MB
`.git` is shared, not copied) against 71 GB free, while the container is capped at **10 GiB** and
two concurrent `go test ./...` runs are the usual way to exhaust it. The commander gets a worktree
of its own rather than running in the parent clone — it needs `git`/`gh` to read the board, and
the parent clone is the base every other session is cut from.
Git refuses to check out a branch that another worktree holds, and the flag that overrides it is
forbidden — two worktrees sharing one branch ref silently revert each other's commits. So the
reviewer reads with `git diff origin/develop...origin/<branch>` and, when it needs to run the
gates, **checks out a detached HEAD** at that commit, which git allows while the branch is held
elsewhere. The reviewer's worktree therefore has no branch of its own and never pushes.

The rest of the worktree rules follow from the same principle — a worktree is a desk, and
changing what somebody else's desk *is* is what breaks a session:

- Each work package is launched as its own Console worktree session and **renames its branch to
  `refactor/<track>` before its first push**, so the merge queue and the PR list are legible.
  Renaming afterwards means pushing a second remote branch and deleting the first.
- A worktree is kept until its PR has merged, and is then cleaned up **from the Console**, not by
  an agent.
- Integration is always inward: `git fetch origin && git merge origin/develop` inside one's own
  worktree. **The parent clone is never fast-forwarded to help somebody else** — with unrelated
  edits `pull --ff-only` succeeds and swaps files out under a working session.
- `console/node_modules` is ~350 MB per worktree and the volume is at 85%; front-end worktrees
  symlink the parent's tree when the lockfiles match, and remove the link (no trailing slash)
  before any `npm ci`.

**決定 11. The pre-merge gate is the reviewer's local run, because CI does not see these PRs.**
`ci.yml` triggers on pushes to `main`/`develop` and on pull requests **into `main` only**, so a PR
into `develop` is unchecked until after it has merged. For 13 refactor PRs that is the wrong place
to find a break, so Phase 0 — the one session that touches `.github/workflows/` — adds `develop`
to the `pull_request` trigger. The repository is public and the runners are free; the reason this
was never noticed is that `develop` gets its own push trigger, which turns red *after* the merge.

## Rejected

- **Rewriting the call sites (`gopls rename`) as the moves happen.** Correct, and it serialises
  every Go session onto one module. Rejected as the *default*; it is exactly what 決定 2 does once
  per wave, when it is safe.
- **One long-lived refactor branch.** Unreviewable, and it drifts from `develop` — which took
  1,500 commits in the month the CI gate was watching only `main`.
- **A shared module for the two binaries.** Already rejected in
  [0012](0012-go-internal-refactor.md). Recorded here so a parallel wave does not rediscover it.
- **Workers messaging each other directly.** Cheaper per message, but with four workers it is a
  mesh: no single place knows the state, and an incoming message interrupts a build mid-turn. One
  hub, one hop.
- **The fleet operator as the hub.** It has the better record (a dispatch ledger and a readable
  conversation) and the better tools, and it was the first choice here. Rejected because it is the
  maintainer's own conversation, which a fortnight of relay traffic would occupy, and because a
  session can read the repository and `gh` directly — which removes most of the messages
  altogether.
- **A ledger file in the repository that every track updates.** The coordination artefact would
  then be the hottest conflict in the tree. The live ledger sits outside git, one file per track;
  only the durable conclusion lands here and in `docs/`.
- **Letting the reviewer merge.** Faster, and it automates the one outward-facing step where a
  human veto is cheap and a mistake is expensive.
- **Six workers at once.** The host is shared and memory-constrained, two concurrent `go test
  ./...` runs are already the usual way to exhaust it, and one reviewer cannot absorb six PR
  streams. Four workers plus a reviewer, in waves.

## Consequences

- Aliases exist between a move and its collection. `grep alias_` shows the debt; a wave is not
  finished until it is zero.
- `migrations/`, `migrations-pg/` and `assets/` move *with* the package that embeds them —
  `//go:embed` cannot reach above its own directory. This is the one mechanical trap that will
  break the store extraction if it is missed.
- Pushing the `ci.yml` change needs a token with the `workflow` scope. The device flow asks for it
  now, but a token stored before that fix does not have it — if Phase 0's push is rejected,
  reconnect GitHub from the Console rather than working around it.
- Peer messaging must be enabled for the workspace, or the hub has no channel and coordination
  falls back to the maintainer relaying by hand.
- The previous layering was merged with every test green and **never verified against a live
  fleet**. This one ends with an image rebuild and one pass through session creation, chat, git,
  LFS and the connection flows before it is called done.

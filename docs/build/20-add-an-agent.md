---
audience: "someone integrating a new CLI coding agent"
source_of_truth: "the existing `internal/agents/<kind>` packages — copy the closest one"
updated: "2026-09"
---

# 20. Adding an agent kind

English | [日本語](20-add-an-agent.ja.md)

**Which surfaces a kind fills follows from which drivers it has**, and within that they
are the same every time. So are the mistakes. This chapter is both lists.

Before writing code, read [04 §4.3](04-agent.md) for the shape and
[ref/agents.md](../../guide/ref/agents.md) for what the existing kinds actually support.

## 20.1 Decide three things first

**1. Which drivers?** A **Terminal (CLI)** route runs the CLI's own screen in a tmux
pane: the kind's `BuildLaunch` returns the pane program. A **Managed** route has no
pane: a `Driver` (`internal/agents/driver.go`) runs turns through a structured API.
There are three shapes, and each exists today — which kind has which is
[ref/agents](../../guide/ref/agents.md):

- **Terminal only** — no `Driver`.
- **Both** — more work, since every surface below is filled for each route.
- **Managed only** — `Caps().ManagedOnly` is set. `BuildLaunch` always returns
  `ErrNoTerminalRoute`, `POST /sessions/{name}/driver` refuses a `tui` target, and
  create defaults an unspecified driver to `managed`. This fits a kind with no pane
  program at all, such as one that runs inside the agent.

A Managed route also picks a **process model** (`Capabilities.ProcessModel`): a daemon
shared per workspace, a child process per session, or code inside the agent itself. The
kinds that use each, and the protocols they speak, are the table in
[04 §4.3](04-agent.md).

**2. How is the conversation id held?** This is the decision that causes silent
breakage later, so make it deliberately. **Prefer capturing it** (the CLI mints it and
every event re-records it). If you impose it, **ship the recovery path in the same
change**. The rule for that path, and which kind does what, are
[04 §4.2](04-agent.md) ("Conversation ids: captured and imposed").

**3. What proves it works?** Not the CLI's own status output, and not a banner. **A real
prompt producing a real answer.** This has been wrong enough times to be a rule
([08 §8.5](08-integrations.md)).

## 20.2 The surfaces to fill

Fill the rows for the drivers your kind has. The paths are under `workspace/agent/`
unless they name another tree.

| Surface | Where | Notes |
|---|---|---|
| The kind constant and its registration | `Kind*` in `internal/session/session.go`; `agentRegistry` in `internal/sessionx/agent.go`; for Managed, `managedDrivers` in `internal/sessionx/session_turn.go` | **A kind missing from `agentRegistry` silently becomes claude**: `NormalizeKind` and `AgentOf` fall back to it |
| Capabilities | `Caps()` in your package; `Capabilities()` on your `Driver` | **Do not set a capability the member sees until you have driven it end to end.** `Capabilities()` declares what the driver implements (§20.4) |
| Terminal launch | your package's `BuildLaunch`, returning `agents.LaunchPlan` | Environment goes in `LaunchPlan.Env`, **never prefixed onto the command** (§20.3). A managed-only kind returns `ErrNoTerminalRoute` |
| Managed runtime | your `Driver`: `Resume` returns a `ThreadHandle` | Its shape follows the process model ([04 §4.3](04-agent.md)) |
| Which driver a caller picks | Console: `managedDriver` and `terminalDriver` in `console/src/agents/registry.ts`. The in-container MCP `create_session` (`mcpStdioCall`). The CP: `create_session` in `control-plane/internal/mcpsrv/mcp.go` and `injectDriver` in `control-plane/scheduler_wake.go` | The agent defaults an unspecified driver to `tui`, so **a kind with both drivers that should start Managed is added to every caller**. A managed-only kind is defaulted by the agent, but the Console still needs `terminalDriver: false` |
| Live state | hooks, a plugin, a poll of the CLI's store, or runtime events | Normalise into the status store: working / idle / question, plus `plan` and `permission` ([04 §4.4](04-agent.md)) |
| Transcript | a reader behind `Agent.Transcript` | **Where the CLI keeps a readable, stable native store, read it; never copy the conversation into a store of ours.** The parsers stay separate. A kind with no such store owns one, as the two exceptions in [04 §4.3](04-agent.md) do |
| Sign-in | the agent's `/connections/<kind>/…` handlers, **and** each route relayed by name in `control-plane/routes.go` (`restLogin` for a login flow) | No kind needs a CP callback ([08 §8.6](08-integrations.md)); a kind with nothing to sign in to has no flow |
| Credential location and the filesystem denylist | your package, plus `fsDeny` (`fs.go`) | Anything the CLI writes credentials or state into must be **hidden from the file browser** |
| MCP | `internal/mcpreg`: a writer in `writerFor` and an entry in `MaterializedKinds` when the CLI reads a config file; `ServedKinds` when servers reach it another way (on the wire, or in-process); `knownKinds`, and `mcpKnownKinds` in `control-plane/internal/mcpsrv/mcp_server.go` | Each CLI has its own config shape and placeholder dialect. A config-file kind whose CLI can run in CI goes into `mcp-config-contract.yml` too |
| Agent instructions | `agent_instructions.go`: `instrSupportedKinds` and the per-kind apply, or `instrUnsupported` with a reason code | The Console's list of targets is built from these two lists. If the CLI has no per-user place, **list it with the reason** rather than silently dropping it. A kind that writes its own system prompt reads the layers there instead (lcpp does, each turn, through `harness.SystemPrompt`); list it in `instrSupportedKinds` with the `prompt` delivery, no path and `Applied` true, so its row is the switch the harness reads, and give it a preview case |
| Console descriptor | `SESSION_KINDS` in `console/src/types/session.ts`, and one descriptor in `console/src/agents/registry.ts` | The descriptor's `caps` decide the affordances. Some screens still switch on the kind name, so grep them (below) |
| Version pin | an ARG in `workspace/Dockerfile`, the `versions.json` it writes, and a row in `deploy/local/cli-drift-check.sh` | [10 §10.2.1](10-development.md). A kind that runs no vendor CLI has nothing to pin |
| A contract workflow | its own file under `.github/workflows/`, for a kind whose CLI or host the workspace image pins | **One file per agent**, registered with the release watcher. lcpp pins nothing in the image and has a manual check instead (§20.5) |

The table is not a complete list of where kind names appear. Some lists are still kept
by hand: for example the bracketed-paste kinds in `internal/sessionx/session_io.go`,
`usageMeasuredForKind` in `usage_fold.go`, and `isDynamic` (the kinds with a live model
catalogue) in `console/src/lib/agentModels.ts`.
Grep `workspace/agent`, `control-plane` and `console/src` for an existing kind with the
same drivers as yours, and decide each hit.

## 20.3 The traps that have actually bitten

Every one of these cost real debugging time. The first three are the launch contracts in
[04 §4.3](04-agent.md), which holds their evidence.

- ⚠️ **Environment reaches the process through `tmux new-session -e`**: put it in
  `LaunchPlan.Env` and `startSessionTmux` passes it. **Never prefix secrets onto the
  command** — a prefix lands in `/proc/*/cmdline` and in tmux's `pane_start_command`,
  readable by anything in the workspace.
- ⚠️ **Reap your children.** The agent is not PID 1. `Start()` without a matching wait
  leaks a PID **forever**, and the path that leaks is always the failure path — "kill it
  on a start timeout and return". Two runtimes shipped that bug.
- ⚠️ **Nested hook schemas parse when written flat, and then never fire.** No error, no
  log — resume silently starts a new conversation instead.
- ⚠️ **tmux target matching is a prefix match.** Use `session.ExactTarget` (`=<name>`)
  for session targets, or you will eventually kill the wrong session. `capture-pane`
  does not accept that form and needs a pane target (`internal/tmuxx`).
- ⚠️ **A model that only exists in a picker is not a model id.** If the picker, or another
  caller such as MCP `create_session`, can send a label or abbreviation your launch path
  cannot accept, add the kind to the resolution in `HandleCreateSession`
  (`resolveLiveModel`), which **refuses before the clone or worktree happens** — an invalid model that only fails
  after launch leaves debris behind. The refusal needs a readable catalogue: when it
  cannot be read, the value passes through and the start proceeds. Which kinds are
  resolved, and the rest of the rule, are [04 §4.2](04-agent.md) ("Other session
  operations").
- ⚠️ **Free plans are a different product.** copilot's Free plan offers only Auto, and
  Auto rejects `--effort` (`internal/agents/copilot/program.go`); cursor's Free plan
  cannot launch a named model (`internal/agents/cursor/models.go`). A flag passed
  unconditionally fails to start **for exactly the users least able to diagnose it**.
- ⚠️ **A trust or onboarding prompt is not authentication.** A CLI can be signed in and
  still show a wizard, which looks identical to being signed out
  ([08 §8.5](08-integrations.md)).
- ⚠️ **Answer modals by key sequence, never by typing the label**, and verify **through
  the delivery layer** — a sequence that is right in a probe can still not reach the
  agent ([92](92-driving-a-tui.md)).

## 20.4 Do not set a capability you have not driven

`Caps()` and the Console descriptor's `caps` are not documentation: the Console shows or
hides controls by them, and the server refuses by them. For example, create answers
`permission_choice_unsupported` for a kind without `PermissionChoice`. The rule this
repository learned: **a capability the member sees is set only when the path has been
driven end to end on the real runtime** — the vendor CLI or host, or for an in-process
kind the real engine. The specific case: allowing the permission prompt to be skipped
requires that **a pending approval can actually be answered from the Console**. Removing
the flag is easy for any kind — but a session stopped at a dialog **the user cannot see
or answer** is, from their side, indistinguishable from a hang.

A `Driver`'s `Capabilities()` is a different claim: it declares what the driver
implements. Its one reader outside tests is `GET /sessions/{name}/settings`, which passes
on `DynamicModel`, `DynamicEffort` and `DynamicMode` (`internal/sessionx/session_turn.go`).
muse shows the difference: its driver sets `Permissions`, because the approval path is
built and tested, while its `Caps()` leaves `PermissionChoice` false, because a muse
session in a workspace was measured to raise no approval at all (`muse/driver.go`,
`muse/muse.go`). A field that does reach the Console is held to the member's bar.

The same applies to the other direction: **when a capability is genuinely absent, do not
render the control at all.** A button that does nothing is worse than no button.

Two checks hold [ref/agents.md](../../guide/ref/agents.md) to the code, each over a named
set of rows:

- `scripts/docs-check.py` (its `ref` check, run on every PR by `docs.yml`) requires a
  column for every `Kind*` constant, and the rows in `CAPS_ROWS` to match `Caps()`
  exactly. Every `Caps` field is either in `CAPS_ROWS` or excused in `CAPS_UNMAPPED`.
- `console/src/agents/guideTable.test.ts` matches the rows in `ROW_TO_CAP` against the
  descriptors' `caps`, and names the rows it leaves out in `UNMAPPED_ROWS`, each with a
  reason.

Neither reads a `Driver`'s `Capabilities()`, and neither checks an unmapped row. Those
cells are yours to get right.

## 20.5 Verification, and why the workflow is per agent

Local tests are not enough: CI builds the pinned version while a self-updating workspace
runs the latest, and the headless smoke test draws no TUI. Why, and why that makes one
workflow file per agent a rule, is [10 §10.4](10-development.md) ("Detecting upstream CLI
breakage").

So a kind whose CLI or host the workspace image pins needs a **contract workflow of its
own**. What it can check depends on the kind. Most drive the real CLI with a test
credential. `muse-contract.yml` needs none: it checks the host's protocol schema and the
shape of its version line, and never runs a turn.

lcpp is the exception. It runs no vendor CLI, and the llama.cpp server it talks to comes
with the engine — the deployment's, or the member's own server
([08 §8.6](08-integrations.md)) — not with the workspace image, so no workflow and no
release watcher covers it. Its contract with that server's API is an opt-in live test run
by hand against a real engine (`internal/harness/live_contract_test.go`, build tag
`contract_manual`).

Register it with the daily release watcher, `cli-release-watch.yml`, so that a published
version change dispatches it. `cli-drift.yml` only reports pins that fall behind; it
dispatches nothing. Registering takes four places:

- the kind in `KINDS` of `deploy/local/cli-release-edges.sh`;
- its row in `deploy/local/cli-drift-check.sh`;
- its lines in the workflow's state, edge and dispatch steps;
- in the contract itself, a success step that runs
  `deploy/local/cli-release-state.sh set tested <kind> <version>`. **Without it, the
  watcher sees the release as new every day and dispatches it every day.**
  Record the version in the same shape as the watcher's latest for that kind: muse's
  carries its build id (`1.4.0-R4302.1`), so cutting it to `1.4.0` leaves the edge open.

For the pin to be bumped automatically once that contract passes
([10 §10.2.2](10-development.md#1022-automated-agent-cli-bumps-cli-pin-bumpyml)), add the
kind to `KINDS` in `deploy/local/cli-pin-bump.sh` (plus a `resolve_<kind>` that fetches
and checks its checksums, if it pins any) and its contract's `name:` to the
`workflow_run` list of `cli-pin-bump.yml`.

Dispatch unattended only when the credential can be supplied unattended. **A credential
that rotates through an interactive refresh is recorded as "seen" and dispatched by
hand**, and so is a release that arrives while its credential is not configured.
"seen" never advances "tested": conflating "we noticed a new version" with "we tested it"
is how a regression ships.

## 20.6 Finishing

A kind is not done when it runs. It is done when:

1. `Caps()` matches what you drove on the real runtime, and a `Driver`'s
   `Capabilities()` matches what the driver implements (§20.4);
2. [ref/agents.md](../../guide/ref/agents.md) has its column filled, and the two checks
   in §20.4 pass;
3. [member/06-agents](../../guide/member/06-agents.md) tells a user how to connect it,
   using the Console's own words (`console/src/lib/i18n/locales/`);
4. if the workspace image pins its CLI or host, its contract workflow exists, is
   registered with the release watcher, and has passed against a real release; otherwise
   a check against what it does depend on has passed, run by hand if no workflow can run
   it (§20.5);
5. if anything was settled that could plausibly be reopened — why this driver, why this
   id strategy — [decisions/](../decisions/) has the record.

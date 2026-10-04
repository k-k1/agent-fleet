# 0109. Session-aware tool policies: one decision point in the Agent, an enforcement point per kind, the stricter layer wins

English | [日本語](0109-session-aware-tool-policies.ja.md)

- Status: **proposed** (2026-10-04). Nothing is built yet. This record is the design only. Each kind's
  interception seam is cited from this repository at the commit it was written on. The hook vocabularies of
  Claude Code 2.1.288 and codex 0.160.0 (the versions pinned in `workspace/Dockerfile:320,322`) were read as
  strings in the installed binaries, and **their behaviour under AF's launch flags is not measured**. The one
  figure measured here is the cost of starting the Agent binary as a hook (§ Performance budget).
- Tracking: #1055
- Related: [0056](0056-tool-permission-choice.md) (each kind's own permission prompt, and the skip-by-default
  choice this record sits on top of) / [0055](0055-idle-stop-and-carried-interactions.md) (approvals carried
  across a stop) / [0103](0103-branch-naming-rules.md) (how tenant data already reaches the Agent, and the
  layering precedent) / [0093](0093-lcpp-agent-kind.md) (lcpp, the one kind whose tools AF runs itself) /
  [0095](0095-muse-agent-kind.md) (muse, whose approvals never fire in a Workspace) /
  [build/07 Security](../build/07-security.md) §7.1–7.2 (the container is the boundary; same-uid secrets are an
  accepted limit)

## Context

Whether a tool call needs approval today is whatever the kind's own permission mode does. ADR 0056 lets a
member turn the skip flag off per kind or per session, and the default stays "skip" for every kind
(`workspace/agent/internal/agents/agents.go:25-68`). That choice is a per-kind, per-tool switch. Nothing in
Agent Fleet sees the **sequence** of tool calls in a session, so these rules cannot be expressed:

- "a `git push` after the agent downloaded an npm package in the same session needs approval";
- "at most N tool calls in this session";
- "only edit files this session created";
- "ask before any tool that runs a command or touches the file system".

The issue asks for exactly these, stacked deployment → user → session with the stricter layer winning, after
Omnigent (Databricks, Apache 2.0, alpha). It also asks for the design first.

### What exists today (measured from the code)

Go paths in this record are relative to `workspace/agent/` (and `internal/agents/<kind>/` in the matrix)
unless they start with another top-level directory.

- **No layer ever denies a CLI's tool call.** Every hook AF ships only records state and exits with nothing on
  stdout (`workspace/agent/internal/sessionx/session_status.go:62-208`). The managed drivers that receive
  approval requests either hand them to a human or approve them unread: codex
  (`internal/agents/codex/appclient.go:280-311`, `approvalPolicy:"never"` at `driver.go:128-133`) and opencode
  (`internal/agents/opencode/driver.go:1273-1290`, which parses only the id and replies `always`).
- **Claude already runs three PreToolUse hooks** through the `settings.json` AF manages:
  `AskUserQuestion`, `ExitPlanMode` and `Write|Edit|MultiEdit|NotebookEdit|Bash`. The last one records the
  tool about to run so the permission card can name it (`internal/agents/claude/hooks.go:34,46-128`).
  PostToolUse with an empty matcher fires after every tool as a heartbeat (`hooks.go:96`). Each hook is a
  `workspace-agent session-status <state>` process (`hooks.go:28-30`) that writes files under the Agent's
  state directory and POSTs best-effort to the local Agent (`internal/chatx/chat_report.go:200-208`).
- **codex gets hooks only on the Terminal route**, as `-c hooks.<Event>=…` for UserPromptSubmit, Stop,
  PreCompact and PostCompact (`internal/agents/codex/program.go:43-50,78-85`). The shared `codex app-server`
  is started with no `-c` (`serve.go:285`).
- **Two approval surfaces exist in the Console.** The Terminal (CLI) route shows `PermissionCard` and answers
  by sending keys to the CLI's own menu (`console/src/features/mirror/parts/pendingCards.tsx:77-119`,
  `MirrorPendingCards.tsx:103-114`). The Managed route turns a request into an `Interaction` of kind
  `approval` carrying an `ApprovalRequest` (summary, tool, command, parsed stages;
  `internal/agents/driver.go:103-171`), shows `ApprovalCard` (`pendingCards.tsx:122-185`) and answers through
  `POST /sessions/{name}/respond`, which returns 501 `respond_unsupported` for a Terminal session
  (`internal/sessionx/session_turn.go:294-298`). Status `permission` raises a `permission-request` notice that
  reaches Console notifications and the chat bridge (`session_status.go:300-308`).
- **lcpp is the only real gate.** AF's own harness runs the tool, so `approve()` really blocks, and an unset
  approver declines every mutating call (`internal/harness/approval.go:1-20`).
- **Nothing counts tool calls.** Transcripts are read on demand, not tailed (`internal/agents/claude/transcript.go`,
  `codex/transcript.go`, `opencode/transcript.go`). The Console polls every 1.2 s while working
  (`console/src/features/mirror/pollCadence.ts:21-23`). Usage accounting folds tokens only
  (`workspace/agent/usage_fold.go`). Managed drivers emit no tool events (`agents.Event` at `driver.go:214-219`).
- **Tenant data already reaches the Agent by pull.** Branch rules are fetched from `GET /internal/branch-rules`
  every five minutes and cached, fail-open (`internal/branchrule/tenant.go:7-34`). Egress has a
  deployment-wide allowlist and mode, served from `GET /internal/egress/policy`, and enforce blocks at the
  proxy only (build/07 §7.8). There is no deployment- or tenant-level lock over the ADR 0056 choice: the
  control plane never mentions it.
- **The same uid runs the agent and the Agent.** `AGENT_TOKEN` and `AF_SECRET_KEY` are in every agent shell,
  and the agent can write the Agent's state directory and claude's `settings.json` (build/07 §7.2).

### What each kind offers as an interception point

**Block** means the kind asks something AF controls *before* the tool runs, can be told no, and the arguments
are visible at that point. **Observe** means AF can see the call only once it has started or finished.
**None** means AF sees nothing structured.

| Kind (execution method) | Seam | Class | Arguments visible | Evidence |
|---|---|---|---|---|
| claude (Terminal) | PreToolUse command hook; the decision vocabulary in 2.1.288 is `allow` / `deny` / `ask` / `defer` | **block** | yes (`tool_name`, `tool_input`) | AF already injects PreToolUse (`hooks.go:34,69-80`); vocabulary from the binary's error string "Valid types are: allow, deny, ask, defer" |
| codex (Terminal) | PreToolUse hook through `-c hooks.PreToolUse=…`. In 0.160.0 a hook may only **deny**, and needs a reason; `ask` and `allow` are rejected as unsupported. A PermissionRequest hook may deny an approval | **block** (deny only) | yes (`tool_name`, `tool_input`) | AF already injects other events this way (`program.go:43-50`); the binary contains "PreToolUse hook returned unsupported permissionDecision:ask" and "…permissionDecision:deny without a non-empty permissionDecisionReason" |
| codex (Managed) | app-server approval requests, auto-approved today; hooks are not set on this route | **observe** today; block needs either hooks in the thread config or an approval policy other than `never` | approval params carry the command | `appclient.go:280-311`, `driver.go:128-133`; docs/log/76 parks the policy change as P1 |
| opencode (Managed) | `permission.asked` → `POST /permission/{id}/reply`; plugin hook `tool.execute.before` (AF ships one that rewrites arguments) | **observe** today; block needs either opencode's permission config set to ask, so every call reaches the reply, or a plugin that throws. Neither is measured | plugin: yes; `permission.asked`: AF parses only the id | `opencode/driver.go:1273-1290`, `workspace/opencode-plugin/rtk.ts:24-38` |
| opencode (Terminal) | `--auto`; the pane is read for prompts | **observe** (plugin seam as above) | pane text | `opencode/program.go:31-36`, `screen.go:3-16` |
| cursor, kiro, copilot (Managed, ACP) | `session/request_permission`, answered by `Respond` with an allow or reject option | **block, only when the skip flag is off**. With the default `--force` / `--trust-all-tools` / `--allow-all` no request arrives, and the class falls to **observe** (`session/update` `tool_call`) | yes (`rawInput`; copilot parses only title and command) | cursor `driver.go:375-376,913-927,1143-1189`; kiro `driver.go:470-471,1068-1090,1296-1345`; copilot `driver.go:374-377,905-919,986-1037` |
| copilot (Terminal) | user-scope `preToolUse` hook file, already used for rtk; its output may carry `permissionDecision` | **block** (not measured) | yes | `copilot/rtk.go:8-44` |
| kiro (Terminal) | the binary has PreToolUse hook triggers; not wired | **observe** today; block not measured | — | `kiro/state.go:14-17` |
| cursor (Terminal) | `hooks.json` `beforeShellExecution` exists in the CLI; not wired, and it covers shell only | **observe** today | — | ADR 0023; `cursor/modal.go:3-24` |
| agy (Terminal only) | none; a pending permission is read from its conversation DB | **observe** (`transcript_full.jsonl`) | best effort | `agy/pending.go:3-16,222-230`, `agy/agy.go:96` |
| muse (Managed only) | MSP `approval/requested` with a deny choice, but under the permanent `--disable-sandbox` every call resolves `allow:policy` before the approval layer | **observe** (`item/*` notifications); the protocol could block, the Workspace never asks | richest (`ToolName`, argv per stage) | `muse/muse.go:32-52`, `muse/handle.go:803-841,1507-1518` |
| lcpp | AF's own harness | **block** (real, fail-closed) | yes | `harness/approval.go:1-20` |
| shell, ssm | no agent | **none** | — | — |

Two things follow. Every seam that can block today, except lcpp, is **a hook or protocol message the CLI
chooses to send**, run as the agent's uid. And on the ACP kinds the seam only exists when the CLI's own skip
flag is off, which ADR 0056 leaves on by default.

## Decisions

### 1. One policy decision point in the Agent; an enforcement point per kind

The **decision point** is a component of the Agent process. It holds each live session's effective policy
and its tool-call ledger (decision 4), and answers one question: *given this session's history, may this
normalised tool call run: allow, deny, or ask the member?*

**Enforcement points** are thin, per kind, and hold no policy. Each one turns the kind's seam into a call to
the decision point and turns the answer back into the kind's vocabulary:

- **claude Terminal:** a PreToolUse hook with an empty matcher (every tool) runs
  `workspace-agent policy-check`. That subcommand reads the hook JSON, asks the local Agent, and prints
  `permissionDecision`. `allow` is never printed: an allowed call returns no decision (`defer` or nothing,
  whichever P0 measures to leave the CLI's own mode in charge), so a policy can only add restriction and
  never widens what the CLI's permission mode would ask.
- **codex Terminal:** the same subcommand injected as `-c hooks.PreToolUse=…`. codex accepts only `deny`, so
  an `ask` verdict is held inside the hook (decision 6).
- **ACP kinds (Managed):** the driver's `onServerRequest` asks the decision point before it raises an
  `Interaction`. Allowed requests are answered by the driver; asks become the existing approval
  `Interaction`; denials send the reject option. This only works with the CLI's skip flag off, so a session
  that has an active blocking policy is launched with it off, and the decision point answers in its place
  everything its policies allow (decision 7).
- **lcpp:** `approve()` consults the decision point before its own gate.
- **Kinds that can only observe** (agy, muse, opencode, codex Managed, and kiro and cursor Terminal until
  their hooks are measured) feed the ledger from what they already read after the fact. Decision 7 says what
  that is allowed to mean.

**The decision point lives in the Agent, not the CP.** The hook runs in the container and must answer in
milliseconds (§ Performance budget). A round trip to the CP would put the network inside every tool call.
docs/log/20 rejected a push from a hook to the CP because Agent→CP was not reachable then. The workspace-only
listener that now carries the Agent's pulls is built for periodic fetches, not for a per-call hot path. The
CP is the source of policy, not the judge of each call.

Every enforcement point normalises the call into one shape before it asks: `{session, kind, tool, category,
argv stages, paths, mcp server}`. The **category** is a closed set: `shell`, `edit`, `read`, `web`, `mcp`,
`agent`, `other`. Each kind maps its own tool names onto it (claude `Bash` → `shell`, `Write|Edit|MultiEdit|
NotebookEdit` → `edit`; ACP `kind` values map the same way). Policies are written against categories, argv
and paths, never against one CLI's tool names. A tool name no mapping knows is `other`, and a policy that
fails closed treats `other` as matching every category it names, the same rule as an unparseable command
(decision 2).

### 2. The policy model: a small built-in set, declarative, no user code

A policy is a **built-in type with parameters**, stored as data (YAML in the editor, JSON on the wire). There
is no expression language, no script, no user-supplied binary. In a multi-tenant deployment the decision
point runs inside every member's Agent, and code from one member or tenant must never run in another's
decision path.

The first four types, one per example in the issue:

| Type | Parameters | Verdict |
|---|---|---|
| `tool_call_cap` | `max` (per session); optional `categories` | after `max` counted calls, `deny` (or `ask`; open question 6) |
| `approval_gate` | `after`: a matcher; `then`: a matcher; `verdict` (`ask` / `deny`) | once a call matching `after` has been allowed in the session, every call matching `then` gets `verdict` |
| `path_scope` | `allow`: path globs relative to the working copy, and/or `created_by_session: true`; `categories` (default `edit`) | an edit outside the scope gets `deny` or `ask` |
| `ask_on_categories` | `categories` (e.g. `shell`, `edit`) | every matching call gets `ask` |

A **matcher** is a closed grammar: a category, a tool name, an argv prefix such as `["git","push"]` or
`["npm",["i","install","ci","add"]]`, a path glob. The built-in example "push after download" is an
`approval_gate` with `after` = {npm, pnpm, yarn, pip, uv add/install; curl, wget} and `then` = `git push`.

**Shell commands are parsed, and an unparseable command matches.** A `shell` call's command line is split
into stages and argv with a shell parser in the Agent (as `ApprovalRequest.Stages` already does for the card).
A command the parser cannot reduce to plain argv (`eval`, `$(…)` producing the program name, base64 piped to
`sh`) **counts as matching every `shell` matcher** of a policy that fails closed. This turns obfuscation into
an approval, not a bypass. It is still not a sandbox: decision 9.

**"Created by the session"** is known from the ledger: an edit-category call whose target did not exist when
the decision point allowed it records the path as created by the session. A file created by a `shell` command
is not seen. So a `path_scope` with `created_by_session` limits structured edits only, and its editor says so.

User-authored policies (new matchers, new combinations) come later, stay in the same grammar, and still run
no code (§ Phases).

### 3. Stacking: deployment → tenant → user → session, and the stricter value wins

Policies are set at four layers. **No layer can loosen what a layer above it set.**

| Layer | Who sets it | Where it lives | How it reaches the Agent |
|---|---|---|---|
| deployment | super_admin | CP database | pull, like branch rules (`GET /internal/tool-policies`, every 5 minutes, last copy cached) |
| tenant | tenant admin | CP database | the same response, resolved for the member's tenant |
| user | the member, in Settings | CP (so it follows the member across workspaces) | the same response |
| session | whoever launches the session (Console, `create_session`, a schedule) | `session.Meta`, copied on restart and fork like `SkipPermissions` | at launch |

Whether the tenant layer is wanted is open question 1. The issue names three layers; AF has tenants between
deployment and user.

**Stricter wins, per type:** caps take the minimum; `approval_gate` and `ask_on_categories` take the union;
`path_scope` takes the intersection; for a single call the verdict order is `deny` > `ask` > no decision. A
session-layer policy can add gates and lower caps but cannot remove a gate or raise a cap set above it. A
child session started by another session inherits the parent's session layer and may only add to it, so an
agent cannot launch an unrestricted child to do what it was refused.

**The policy is fixed at launch and tightened live.** The effective set is computed when the session starts.
A later pull that adds or tightens a policy applies to the running session on its next call. A pull that
loosens one applies to sessions launched afterwards, so a CP blip or an edit cannot open a running session up
mid-turn.

### 4. State: a per-session ledger owned by the decision point, written by the check itself

The decision point keeps, per session, a **ledger**: counts per category, the gate flags that are set, the
paths the session created, and the last N normalised calls (for the card and for audit). It is updated
**synchronously in the check**. A call the decision point allows is recorded before the answer goes back, so
the next call sees it. Transcripts are not the source. They are read on demand with seconds of lag, and a
policy that waited for them would let the gated `git push` run before the `npm install` it follows was ever
read.

- What counts is **the attempt**, not the result. A call is recorded when allowed, whether or not it then
  succeeded. For gates and caps that is the stricter reading. PostToolUse, which already fires for every
  claude tool, may enrich the record (exit status) but never clears a flag.
- The ledger is persisted per session under the Agent's state directory, so an Agent restart does not reset a
  cap, and it is deleted with the session. Fork copies it; a new session starts empty.
- For observe-only kinds the ledger is fed from the events their drivers already read (ACP `tool_call`,
  `events.jsonl`, `item/*`, transcripts), late, and marked as observed rather than checked.

### 5. Approvals surface on the existing cards

An `ask` verdict becomes one of the two approval surfaces the Console already has. A third one is not built.

- **Where the kind can ask natively** (claude Terminal with `ask`, if P0 measures that it prompts under the
  skip flag; open question 9), the CLI shows its own menu and the existing `PermissionCard` answers it by
  keys. The card gains a line naming the policy that asked.
- **Everywhere else** the decision point raises an **AF-held approval**: an `Interaction` of kind `approval`
  with the existing `ApprovalRequest` (summary, tool, command, stages), plus a new optional `policy` field
  (the policy's id and one-line reason). It renders as the existing `ApprovalCard` and raises the existing
  `permission-request` notice, so the chat bridge and Console notifications carry it unchanged. Because the
  Agent, not the CLI, owns this interaction, `POST /sessions/{name}/respond` accepts it for Terminal sessions
  too. The 501 stays for every other interaction of a Terminal session.
- **Scope** reuses `once` / `turn` / `thread`. Whether a member may answer "always" for a policy-held
  approval (thread scope, which would disarm a gate for the session) is open question 5.
- **Only a person answers.** No af MCP tool answers an approval, and a parent session cannot approve for its
  child ([guide/member/02-sessions.md](../../guide/member/02-sessions.md) already says so for CLI approvals). An approval can be carried across a
  stop as ADR 0055 does.

### 6. Holding an approval inside a hook, and the timeout

For codex, and for claude if `ask` does not prompt under the skip flag, the hook process itself waits while
the AF-held approval is open, and then prints `deny` or no decision. That bounds an approval by the CLI's hook
timeout. An internal schema string in claude 2.1.288 caps a hook's timeout at 600 s, which is not measured; codex's
is not known. When
the timeout or the policy's own `approval_timeout` (lower) expires unanswered, the verdict is **deny**, with
a reason the model reads ("approval timed out; ask the user"), and the card is withdrawn. Whether an
unattended session (a schedule, a child) should rather stop and wait is open question 4.

### 7. Kinds that cannot block

A policy carries `enforcement: block | observe`. **block** (the default for every built-in type) needs a kind
whose enforcement point can block:

- **At launch**, the Agent computes the effective set. If a `block` policy applies and the kind cannot block
  on this execution method, the launch is refused with a reason (`policy_requires_blocking_kind`), the same
  way ADR 0056 refuses `permission_choice_unsupported`. This applies to every launch path (Console,
  `create_session`, schedules, restart, fork), because the Agent resolves it, as in ADR 0056 decision 3.
- **ACP kinds** with a `block` policy are launched with the skip flag off. The decision point then answers
  every request the policies allow, so the member sees what the skip flag would have shown, and only the calls
  a policy asks about reach a card.
- **observe** policies run on every kind. The ledger is fed late; a breach raises a notice and an audit
  record and may interrupt the turn (open question 7), but it cannot prevent the call that breached.

Whether refusing the launch is the right product answer, rather than launching with a visible "observe only"
badge, is open question 3.

### 8. Failure mode, per policy

Each policy carries `on_error: closed | open`. The built-in default is **closed** for `tool_call_cap`,
`approval_gate` and `path_scope`, and **open** for `ask_on_categories`. `ask_on_categories` is a
convenience, and failing it closed would turn an outage into a wall of denials.

- **The hook cannot reach the Agent** (the Agent is down or restarting): the hook reads a per-session snapshot
  file the Agent writes at launch and on every change. The snapshot says whether any closed policy applies.
  If one does, the hook denies with a reason; if none does, it returns no decision.
- **The CP is unreachable:** the last pulled copy stays in force (as branch rules do). A deployment that has
  never pulled a copy starts with the built-in defaults, which are empty (open question 8).
- **A command cannot be parsed:** decision 2 (it matches).
- **The decision point panics or times out internally:** treated as unreachable.

### 9. Security: a guard-rail at the agent's uid, not a sandbox

This layer catches accidents and prompt-injected sequences ("download this, then push"). It cannot stop an
agent that sets out to defeat it, because the agent runs at the same uid as everything that enforces it
(build/07 §7.2). Specifically:

- **An agent editing its own policy.** No policy is writable from inside the workspace: the Agent exposes no
  route that changes a policy, and the source is the CP. The agent can still write the ledger file, the pulled
  cache and claude's `settings.json` (including `disableAllHooks`). So the claude enforcement hook is placed
  where the agent cannot write it: the image's managed settings under `/etc/claude-code/`, root-owned. Whether
  a managed hook survives a user-scope `disableAllHooks` in 2.1.288 is not measured (P0). The ledger and cache
  are integrity-checked only against accidents. Against a determined agent they are not protected, and the
  guide says so.
- **Prompt injection.** The deny reason the model reads names the policy and says "ask the user". It never
  explains how the rule is matched, so an injected instruction cannot learn the exact obfuscation. The card
  shows the command as written plus its parsed stages, so the member approves what will run, not the agent's
  description of it.
- **Bypass through a kind that cannot block.** Decision 7 refuses the launch, on every path including a child
  started through `create_session`. A nested CLI started inside a governed session (`claude -p` from Bash)
  inherits `AF_SESSION_NAME` and the managed hook, so it is checked against the parent's ledger. A shell
  session is a person's terminal and is not governed.
- **Bypass through the shell.** A `shell` call can do anything an `edit` call can, and more. `path_scope` and
  `created_by_session` see structured edits only. A policy that must also hold for the shell has to ask on
  `shell` too. The editor shows that trade-off rather than implying containment.
- **The decision point is not a judge.** It runs no model and calls no network; a policy cannot ask an LLM
  whether a call is safe.

### Performance budget

The check sits on every tool call. Measured: starting `workspace-agent --version` takes 17–33 ms (median
22 ms, 20 runs, one workspace, 2026-10-04). Claude already pays that once per tool for the PostToolUse
heartbeat, and again for each PreToolUse matcher. The budget:

- **decision point:** ≤ 2 ms per check at p95, in memory, no disk on the allow path except the ledger append;
- **hook end to end** (process start, local HTTP, answer): ≤ 50 ms at p95 added per tool call;
- **one process per tool call.** The enforcement hook absorbs the existing `permtool` recording, so the
  number of claude PreToolUse processes per call does not grow.

P0 measures both against a real session. A budget it cannot meet blocks P0 rather than being raised quietly.

### Out of scope

- Network egress per session. build/07 §7.8 owns egress, and a policy here does not replace the fence that is
  missing there.
- Model or token budgets. Usage limits already exist elsewhere.
- Rewriting a tool's arguments. rtk does that and a policy only decides.

## Rejected

- **Prompting the model with the rules.** This is what the issue wants to replace. A rule in a prompt is a
  request, and an injected instruction outranks it.
- **Deciding in the CP per call.** That needs an Agent→CP round trip inside the hook budget on every tool
  call, and a CP outage would then stop every governed session (decision 8 keeps it to the last copy).
- **Deriving state from transcripts.** They are read on demand with seconds of lag (decision 4), and the
  gated call would run first.
- **A general policy language (OPA/Rego, CEL) or user scripts in P0.** Rego needs a runtime per Agent and a
  language the member must learn. Scripts would run one tenant's code in another's decision path. CEL is
  sandboxed and remains a candidate for user-authored policies (open question 10).
- **Turning on each CLI's own strictest mode instead.** That is the ADR 0056 switch. It cannot express
  sequences or caps, differs per kind, and on several kinds cannot be inspected.
- **Answering ACP requests while keeping the skip flag on.** With the flag on, no request is ever sent
  (measured for cursor, `workspace/agent/internal/agents/cursor/driver.go:20-24`).

## Consequences

- A new hot path in front of every tool call of a governed session. A bug there stops work in every such
  session, which is why the budget and the fail mode are per policy and measured.
- Sessions with a `block` policy run their ACP CLI with the skip flag off. The decision point becomes the
  approver of everything its policies allow, so each kind's own "ask" set must be mapped (P1 measures per kind).
- The claude enforcement hook moves into the image's managed settings, so it is present in every claude in the
  workspace, including a member's own terminal `claude` with no session. With no `AF_SESSION_NAME` it returns
  no decision at once.
- `respond` accepts AF-held approvals for Terminal sessions; the Console's approval card is shown for a
  Terminal session for the first time.
- `guide/ref/agents.md` gains a row "Tool policies: block / observe" per kind, checked against the kinds'
  capability flag by `scripts/docs-check.py`, like the permission-choice row.

## Open questions for the user

These are product decisions this record does not make.

1. **Layers.** Is a tenant layer wanted between deployment and user (decision 3), or exactly the issue's
   deployment → user → session?
2. **Who may set the session layer.** Only the member in the launch dialog, or also a parent session through
   `create_session` and a schedule? Decision 3 lets them add restrictions only.
3. **Kinds that cannot block.** Refuse the launch (decision 7, as written), or launch with an "observe only"
   badge and a warning?
4. **Unanswered approvals.** Deny after the timeout (decision 6, as written), or keep the session waiting
   (and for how long) in unattended sessions such as schedules and children?
5. **"Always allow" on a policy-held approval.** Allow `thread` scope, which disarms that gate for the
   session, or only `once`?
6. **The cap is reached.** Deny every further call, ask to raise it, or stop the session?
7. **Observe-only breaches.** Notify only, or also interrupt the turn?
8. **Defaults.** Ship with no policy enabled (as written), or with one deployment default such as the
   push-after-download gate?
9. **Measured before P0 commits** (a measurement, not a decision, listed so it is not forgotten): whether
   claude's PreToolUse `ask` prompts under `--dangerously-skip-permissions`; whether `defer` leaves the
   CLI's mode in charge; whether a managed-settings hook survives `disableAllHooks`; codex's hook timeout.
10. **User-authored policies.** The same closed grammar extended, or CEL later?

## Phases

| Phase | What | Done when |
|---|---|---|
| P0 | The decision point and ledger; `tool_call_cap` and `approval_gate`; **claude Terminal only**; deployment and user layers; AF-held or native ask on the existing cards; fail mode; the snapshot | in a real claude session `npm install x` then `git push` raises a card naming the gate, deny reaches the model, a cap denies the N+1th call; the open-question-9 measurements are written into this record; the budget holds |
| P1 | `path_scope`, `ask_on_categories`; the session layer; lcpp; cursor, kiro and copilot Managed with the skip flag off; the launch refusal of decision 7; the `guide/ref/agents.md` row | the same scenarios pass on each listed kind; a `block` policy refuses an agy launch |
| P2 | codex Terminal (deny, held ask) and Managed; opencode; copilot, kiro and cursor Terminal hooks; observe-only feed for agy and muse; the tenant layer if wanted | each kind's row in the matrix is measured, not inferred |
| P3 | User-authored declarative policies (open question 10) | — |

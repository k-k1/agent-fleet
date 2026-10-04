# 0109. Session-aware tool policies: one decision point in the Agent, an enforcement point per kind, the stricter layer wins

English | [日本語](0109-session-aware-tool-policies.ja.md)

- Status: **proposed** (2026-10-04). Nothing is built yet. This record is the design only. Each kind's
  interception seam is cited from this repository at the commit it was written on. The hook vocabularies of
  Claude Code 2.1.288 and codex 0.160.0 (the versions pinned in `workspace/Dockerfile:320,322`) were read as
  strings in the installed binaries, and **their behaviour under AF's launch flags is not measured**. A string
  in a binary is not proof that a call is actually stopped, so **every "block" in this record except lcpp is a
  candidate** until its measurement gate (decision 10) passes. The one figure measured here is the cost of
  starting the Agent binary as a process (§ Performance budget).
- Tracking: #1055
- Related: [0056](0056-tool-permission-choice.md) (each kind's own permission prompt, and the skip-by-default
  choice this record sits on top of) / [0055](0055-idle-stop-and-carried-interactions.md) (what survives a
  stop: the fact of an approval, not its answer) / [0035](0035-session-report-v2-ledger.md) (a ledger whose
  identity and delivery had to be made idempotent; decision 4 has the same shape) /
  [0103](0103-branch-naming-rules.md) (how tenant data already reaches the Agent, and the layering precedent) /
  [0093](0093-lcpp-agent-kind.md) (lcpp, the one kind whose tools AF runs itself) /
  [0095](0095-muse-agent-kind.md) (muse, whose approvals never fire in a Workspace) /
  [build/07 Security](../build/07-security.md) §7.1–7.2 (the container is the boundary; same-uid secrets are an
  accepted limit) / #1054 (a per-session spend budget, which raises the same parent-and-child question as the
  cap here)

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
  rtk's PreToolUse/Bash hook rewrites commands (`git push` → `rtk git push`) when it is switched on
  (`internal/agents/claude/settings.go:198-210`).
- **codex gets hooks only on the Terminal route**, as `-c hooks.<Event>=…` for UserPromptSubmit, Stop,
  PreCompact and PostCompact (`internal/agents/codex/program.go:43-50,78-85`). The shared `codex app-server`
  is started with no `-c` (`internal/agents/codex/serve.go:285`).
- **`/etc/claude-code/` holds only `CLAUDE.md`** (`workspace/Dockerfile:722-723`). There is no managed
  `managed-settings.json` in the image today.
- **Two approval surfaces exist in the Console.** The Terminal (CLI) route shows `PermissionCard` and answers
  by sending keys to the CLI's own menu (`console/src/features/mirror/parts/pendingCards.tsx:77-119`,
  `MirrorPendingCards.tsx:103-114`). The Managed route turns a request into an `Interaction` of kind
  `approval` carrying an `ApprovalRequest` (summary, tool, command, parsed stages;
  `internal/agents/driver.go:103-171`), shows `ApprovalCard` (`pendingCards.tsx:122-185`) and answers through
  `POST /sessions/{name}/respond`, which returns 501 `respond_unsupported` for a Terminal session
  (`internal/sessionx/session_turn.go:294-298`). Status `permission` raises a `permission-request` notice that
  reaches Console notifications and the chat bridge (`session_status.go:300-308`). The only production code
  that fills `ApprovalRequest.Stages` copies muse's own argv from MSP (`internal/agents/muse/handle.go:833-835`);
  AF has no shell parser.
- **lcpp is the only real gate, and only for mutating tools.** AF's own harness runs the tool, so `approve()`
  really blocks, and an unset approver declines every mutating call (`internal/harness/approval.go:1-20`).
  `approve()` is called for `Mutates` tools only (`internal/harness/loop.go:457-458`); reads are not seen.
- **Nothing counts tool calls.** Transcripts are read on demand, not tailed (`internal/agents/claude/transcript.go`,
  `codex/transcript.go`, `opencode/transcript.go`). The Console polls every 1.2 s while working
  (`console/src/features/mirror/pollCadence.ts:21-23`). Usage accounting folds tokens only
  (`workspace/agent/usage_fold.go`). Managed drivers emit no tool events (`agents.Event` at `driver.go:214-219`).
- **Tenant data already reaches the Agent by pull.** Branch rules are fetched from `GET /internal/branch-rules`
  every five minutes and cached, fail-open (`internal/branchrule/tenant.go:7-34`). Egress has a
  deployment-wide allowlist and mode, served from `GET /internal/egress/policy`, and enforce blocks at the
  proxy only (build/07 §7.8). There is no deployment- or tenant-level lock over the ADR 0056 choice: the
  control plane never mentions it.
- **A stop carries the fact of an approval, not its answer.** ADR 0055 decision 13 degrades a carried
  permission to the fact alone, because the process that asked is gone.
- **The same uid runs the agent and the Agent.** `AGENT_TOKEN` and `AF_SECRET_KEY` are in every agent shell,
  and the agent can write the Agent's state directory and claude's `settings.json` (build/07 §7.2).

### What each kind offers as an interception point

**Block** means the kind asks something AF controls *before* the tool runs, can be told no, and the arguments
are visible at that point. **Observe** means AF can see the call only once it has started or finished.
**None** means AF sees nothing structured. "Candidate" means the seam exists in code or in a binary's strings
but whether it stops **every** call under AF's real launch flags is not measured; decision 10 lists what must
be measured before a candidate counts as block.

| Kind (execution method) | Seam | Class | Arguments visible | Evidence |
|---|---|---|---|---|
| claude (Terminal) | PreToolUse command hook; the decision vocabulary in 2.1.288 is `allow` / `deny` / `ask` / `defer` | **block candidate** (P0 gate) | yes (`tool_name`, `tool_input`) | AF already injects PreToolUse (`hooks.go:34,69-80`); vocabulary from the binary's error string "Valid types are: allow, deny, ask, defer" (not measured) |
| codex (Terminal) | PreToolUse hook through `-c hooks.PreToolUse=…`. The 0.160.0 strings say a hook may only **deny**, with a reason, and reject `ask` and `allow`; a PermissionRequest hook appears able to deny an approval | **block candidate, deny only** (P2 gate) | yes (`tool_name`, `tool_input`) | AF already injects other events this way (`program.go:43-50`); the binary contains "PreToolUse hook returned unsupported permissionDecision:ask" and "…permissionDecision:deny without a non-empty permissionDecisionReason" (not measured) |
| codex (Managed) | app-server approval requests, auto-approved today; hooks are not set on this route | **observe** today; block needs either hooks in the thread config or an approval policy other than `never` | approval params carry the command | `appclient.go:280-311`, `driver.go:128-133`; docs/log/76 parks the policy change as P1 |
| opencode (Managed) | `permission.asked` → `POST /permission/{id}/reply`; plugin hook `tool.execute.before` (AF ships one that rewrites arguments) | **observe** today; block needs either opencode's permission config set to ask, so every call reaches the reply, or a plugin that throws. Neither is measured | plugin: yes; `permission.asked`: AF parses only the id | `opencode/driver.go:1273-1290`, `workspace/opencode-plugin/rtk.ts:24-38` |
| opencode (Terminal) | `--auto`; the pane is read for prompts | **observe** (plugin seam as above) | pane text | `opencode/program.go:31-36`, `screen.go:3-16` |
| cursor, kiro, copilot (Managed, ACP) | `session/request_permission`, answered by `Respond` with an allow or reject option | **observe** with the default skip flag. With it off, **block candidate** (P1 gate): a request arrives only for calls the CLI decides to ask about, and calls its own allowlist or an earlier "allow always" covers may never be sent | yes (`rawInput`; copilot parses only title and command) | measured for cursor: no request under `--force` (`cursor/driver.go:20-24`). Request handling: cursor `driver.go:375-376,913-927,1143-1189`; kiro `driver.go:470-471,1068-1090,1296-1345`; copilot `driver.go:374-377,905-919,986-1037` |
| copilot (Terminal) | user-scope `preToolUse` hook file, already used for rtk; its output may carry `permissionDecision` | **block candidate** (P2 gate) | yes | `copilot/rtk.go:8-44` |
| kiro (Terminal) | the binary has PreToolUse hook triggers; not wired | **observe** today | — | `kiro/state.go:14-17` |
| cursor (Terminal) | `hooks.json` `beforeShellExecution` exists in the CLI; not wired, and it covers shell only | **observe** today | — | ADR 0023; `cursor/modal.go:3-24` |
| agy (Terminal only) | none; a pending permission is read from its conversation DB | **observe** (`transcript_full.jsonl`) | best effort | `agy/pending.go:3-16,222-230`, `agy/agy.go:96` |
| muse (Managed only) | MSP `approval/requested` with a deny choice, but under the permanent `--disable-sandbox` every call resolves `allow:policy` before the approval layer (measured, ADR 0095) | **observe** (`item/*` notifications); the protocol could block, the Workspace never asks | richest (`ToolName`, argv per stage) | `muse/muse.go:32-52`, `muse/handle.go:803-841,1507-1518` |
| lcpp | AF's own harness | **block** for mutating tools (real, fail-closed); every other tool needs a new check before `tool.Run` | yes | `harness/approval.go:1-20`, `harness/loop.go:457-458` |
| shell, ssm | no agent | **none** | — | — |

Two things follow. Every seam that might block, except lcpp, is **a hook or protocol message the CLI chooses
to send**, run as the agent's uid, so whether it covers every call is a property of the CLI that has to be
measured, not read from AF's handler. And on the ACP kinds the seam only exists when the CLI's own skip flag is
off, which ADR 0056 leaves on by default.

## Decisions

### 1. One policy decision point in the Agent; an enforcement point per kind

The **decision point** is a component of the Agent process. It holds each live session's effective policy
and its tool-call ledger (decision 4), and answers one question: *given this session's history, may this
normalised tool call run: no objection, deny, or ask the member?* It never answers "allow" in the sense of
overriding the CLI: **a policy only adds restriction**, and the member's ADR 0056 choice stays in force.

**Enforcement points** are thin, per kind, and hold no policy. Each one turns the kind's seam into a call to
the decision point and turns the answer back into the kind's vocabulary:

- **claude Terminal:** a PreToolUse hook with an empty matcher (every tool) runs
  `workspace-agent policy-check`. That subcommand reads the hook JSON, asks the local Agent, and prints
  `deny` or `ask`, or nothing. "No objection" is printed as no decision (`defer` or nothing, whichever the P0
  gate measures to leave the CLI's own mode in charge).
- **codex Terminal:** the same subcommand injected as `-c hooks.PreToolUse=…`. codex appears to accept only
  `deny`, so an `ask` verdict is held inside the hook (decision 6).
- **ACP kinds (Managed):** the driver's `onServerRequest` asks the decision point before it raises an
  `Interaction`. The composition rule keeps the member's choice:
  - a deny verdict sends the reject option; an ask verdict becomes the approval `Interaction`, marked with the
    policy;
  - with no objection, the request is answered **the way the member's own permission choice would have
    answered it**. If the session's resolved ADR 0056 choice was "skip", and the skip flag is off only so that
    the seam exists (decision 7), the driver answers allow on the member's behalf, which is what the skip flag
    would have done. If the member chose prompts, or the session is in plan mode, the original `Interaction`
    goes to the member unchanged.
- **lcpp:** a check before **every** `tool.Run`, not only inside `approve()`, which sees mutating tools alone.
  `approve()` keeps its own gate after the check.
- **Kinds that can only observe** (agy, muse, opencode, codex Managed, kiro and cursor Terminal, and every
  candidate whose gate has not passed) feed the ledger from what they already read after the fact. Decision 7
  says what that is allowed to mean.

**The decision point lives in the Agent, not the CP.** The hook runs in the container and must answer in
milliseconds (§ Performance budget). A round trip to the CP would put the network inside every tool call.
docs/log/20 rejected a push from a hook to the CP because Agent→CP was not reachable then. The workspace-only
listener that now carries the Agent's pulls is built for periodic fetches, not for a per-call hot path. The
CP is the source of policy, not the judge of each call.

Every enforcement point normalises the call into one shape before it asks: `{session, call id, kind, tool,
category, argv stages, paths, mcp server}`. **`tool` is a normalised id**, not a CLI's raw name: `shell`,
`edit.write`, `edit.patch`, `read.file`, `web.fetch`, `mcp:<server>/<tool>`, and so on. The **category** is a
closed set: `shell`, `edit`, `read`, `web`, `mcp`, `agent`, `other`. Each kind maps its own tool names onto
both (claude `Bash` → `shell`, `Write` → `edit.write`; ACP `kind` values map the same way). Policies are
written against categories, normalised ids, argv and paths, never against one CLI's tool names. A tool name no
mapping knows is `other`, and a policy that fails closed treats `other` as matching every category it names,
the same rule as an unparseable command (decision 2).

### 2. The policy model: a small built-in set, declarative, no user code

A policy is a **built-in type with parameters**, stored as data (YAML in the editor, JSON on the wire). There
is no expression language, no script, no user-supplied binary. In a multi-tenant deployment the decision
point runs inside every member's Agent, and code from one member or tenant must never run in another's
decision path.

The first four types, one per example in the issue:

| Type | Parameters | Verdict |
|---|---|---|
| `tool_call_cap` | `max` (per session); optional selector (`categories`, normalised ids) | after `max` counted calls matching the selector, `deny` (or `ask`; open question 6) |
| `approval_gate` | `after`: a matcher; `then`: a matcher; `verdict` (`ask` / `deny`) | once a call matching `after` has been allowed in the session, every call matching `then` gets `verdict`. Inside one call, a `then` stage after or alongside an `after` stage counts too (below) |
| `path_scope` | `allow`: path globs relative to the working copy, and/or `created_by_session: true`; `categories` (default `edit`) | an edit outside the scope gets `deny` or `ask` |
| `ask_on_categories` | `categories` (e.g. `shell`, `edit`) | every matching call gets `ask` |

A **matcher** is a closed grammar: a category, a normalised tool id, an argv prefix such as `["git","push"]`
or `["npm",["i","install","ci","add"]]`, a path glob. The built-in example "push after download" is an
`approval_gate` with `after` = {npm, pnpm, yarn, pip, uv add/install; curl, wget} and `then` = `git push`.

**Shell commands are parsed by a new, static parser in the Agent.** None exists today (the only production
`Stages` comes from muse's MSP argv). The parser never runs anything. Its grammar:

- **understood:** simple commands with literal words, pipelines (`|`), lists (`&&`, `||`, `;`, `&`),
  subshells and groups, redirections, `env VAR=x cmd`, and `sh -c` / `bash -c` with a **literal** argument,
  which is parsed recursively. Ordinary compound commands (`cd x && npm ci && npm test | tee log`) parse.
- **known wrappers are unwrapped** before matching: `rtk`, `env`, `nice`, `time`, `timeout`, `nohup`,
  `xargs` (its command argument), `npx`/`pnpm dlx` (the package run). The `rtk git push` rewrite therefore
  still matches `git push`. Whether the policy hook sees the command before or after the rtk hook rewrites
  it is measured in the P0 gate; matching is the same either way.
- **unknown:** any stage whose program name is not a literal word (`$cmd`, `$(…)`, backquotes), `eval`,
  `sh -c` with a non-literal argument, a here-document or pipe feeding a shell (`base64 -d | sh`), and
  `source`/`.` of a file. An unknown stage **counts as matching every `shell` matcher** of a policy that fails
  closed: obfuscation becomes an approval, not a bypass. It is still not a sandbox (decision 9).

**Sequences inside one call count.** The stages of one call are evaluated in their list order, with parallel
stages (`&`, `|`) treated as happening together. If a `then` stage comes after or alongside an `after` stage,
the whole call gets the gate's verdict, so `npm install x && git push` on an empty ledger still asks. A call
that sets a flag and is allowed sets it for the calls that follow.

**Approval fatigue is a real cost of "unknown matches".** A session that uses many unparseable commands sets
its gates early and then asks on every `then` call. The options are recorded rather than chosen here: unknown
sets `after` flags (as written, the strict reading); unknown asks for itself but does not set flags; or the
approval card for an unknown call lets the member say which reading applies (open question 11).

**"Created by the session"** has two states. When the decision point lets an edit-category call create a path
that does not exist, the path is **pending** for the session. It becomes **owned** only when the completion
event of that same call id confirms success and the file exists (claude's PostToolUse; ACP `tool_call_update`
with status completed). A pending path is not owned: if the call was refused or failed, and another session
creates the path, the first session's later edits are outside its scope. The **first** creating edit is
allowed by `created_by_session` only when the path is absent and inside the working copy. Paths are resolved
before matching: relative to the session's cwd, cleaned and `..` resolved. A target that exists is resolved
with `realpath`, so a link inside the working copy that points at a file outside it is outside the scope. A
target that does not exist yet has its nearest existing ancestor resolved. A path that resolves outside the
working copy is never in scope. A link swapped between the check and the write (TOCTOU) is not caught: this
is a guard-rail, not a sandbox (decision 9). A delete followed by a re-creation through an
edit tool starts a new pending state. A rename or a creation done by a `shell` command is not seen, so ownership
does not follow it. A kind whose completion events cannot confirm a call cannot offer `created_by_session` as
block; it falls to observe for that policy.

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

**Stacking keeps every constraint; it does not fold them into one number.** Each policy from each layer is
evaluated on its own, and a call's verdict is the strictest of all verdicts (`deny` > `ask` > no objection).
A deployment cap of 100 on all calls and a user cap of 10 on `shell` are two constraints that both hold, not a
cap of 10. Two policies are merged only when they have the same type and the same selector, and then each
parameter takes the stricter value: the lower `max`, the intersection of `path_scope` allows, `deny` over
`ask`, `block` over `observe`, `closed` over `open`. Different gates are never merged.

**Who may undo a verdict.** An approval answers exactly the policies that asked, and the card names each of
them by id. A `once` approval lets this call through. A broader scope (`turn`, `thread`) may only disarm a
policy whose layer the answering person may edit, so a member can disarm their own user- or session-layer
gate for the session but not a deployment or tenant gate. A cap is never raised from a card in the first
version. Editing the layer that set it loosens the policy, and a loosening reaches only sessions launched
afterwards (below). A session that has reached its cap therefore continues only through an **explicit
exception**: a person who may edit that layer grants a raise for that one session, the grant is audited, and
the session's snapshot and effective set are updated (a loosening may be written after it takes effect,
because a crash then falls back to the stricter state). Whether to offer the exception at all is open
question 6.

**The policy is fixed at launch and tightened live.** The effective set is computed when the session starts.
A later pull that adds or tightens a policy applies to the running session on its next call, subject to
decision 7 when the session cannot enforce it. A pull that loosens one applies to sessions launched
afterwards, so a CP blip or an edit cannot open a running session up mid-turn. Resume, restart and recreate
are not launches in this sense: they keep the session's effective set (with every tightening since), and only
the exception above loosens it.

**A policy added to a running session starts from the session's whole history.** The ledger keeps a compact
record of every call of the session, not only the last N, so a policy that a pull adds, or whose selector
changes (which makes it a new policy), is initialised by replaying that record: a push-after-download gate
added after the session ran `npm install` is armed at once. Where the history is incomplete (calls an
observe-only seam missed, or a session started before the ledger existed), a closed gate starts with its flag
set, and a closed cap starts **exhausted**: it denies until a person resets it or grants the exception above,
because counting only what was replayed could allow a call known to exceed the cap. Only an observe cap, or a
cap with `on_error: open`, counts the replayed calls and is marked *partial* on the card and in the audit
record; a partial cap may undercount, and the guide says so.

### 4. State: a per-session ledger owned by the decision point, updated atomically

The decision point keeps, per session, a **ledger**: counts per constraint, the gate flags that are set,
the paths pending and owned (decision 2), the units still held and the attempts still `awaiting`, and a compact record of
every normalised call of the session (for the card, for audit, and to initialise a policy added later,
decision 3). Transcripts are not the source. They are read on demand with seconds of lag, and a
policy that waited for them would let the later `git push` run before the earlier `npm install` was ever read.

**One check is one atomic step per session.** Checks for one session are serialised. Each check evaluates,
reserves, persists, and only then answers. Parallel tool calls in one turn therefore cannot both read the same
remaining count.

- **Idempotency.** A *retry* here means the same check delivered again (an HTTP resend, the CLI re-running the
  hook for the same call), never the model running the tool again, which produces a new call id. The key is
  the session, the CLI's incarnation (its process start), the call id the kind provides (claude and codex
  `tool_use_id`, ACP `toolCallId`, lcpp's call id) and a hash of the normalised input; each record also holds
  the policy version it was judged under. A retry with the same key counts nothing but is **evaluated again
  under the current policy**, so a tightening that arrived in between applies to it, and a call that was
  already counted stays counted. The same call id with a different input, or in another incarnation, is
  denied. A kind without a stable id has every check counted (the stricter error).
- **What counts.** A call is counted when the decision point raises no objection or the member approves it.
  "Attempt" means *passed by the decision point*, not *executed*: a call the CLI then refuses, or that fails,
  still counts and still sets flags. A call the decision point denies, or whose approval is denied, times out
  or is cancelled, counts nothing and sets no flag.
- **A call record and its attempts are different things.** The **call record** lives for the key's lifetime
  and holds at most one unit of every cap it counts against; its unit is `held`, `committed` or `released`,
  and it is committed at most once. An **attempt** is one check of that record (the first delivery, a retry, an
  approval) and is what may be `awaiting`. While a unit is held, parallel calls cannot overshoot.
  - **No objection:** the count and the `after` flags are committed and fsynced **before** the reply goes
    back, inside the serialised step, so the next check, parallel or not, always sees them.
  - **Ask:** the attempt is `awaiting` and the record's unit is held. On approval the record is evaluated
    again against the ledger as it is now, **excluding the unit the record itself holds**: other records'
    units and the current `max` decide. So with `max=1`, approving the one awaiting call lets it through. Then
    the unit is committed; a denial, timeout or cancel releases it.
  - **A retry of a committed record** keeps its count and is evaluated the same way, excluding its own unit,
    so a lost reply followed by a retry never consumes a second unit. If the current policy now says `ask`,
    that is a new approval attempt on the same committed record (no new unit); `deny` denies it, and the count
    stays.
  - **A retry of a released record** is a new attempt that may hold a unit again.
- **Asks in P0 are AF-held only.** A native ask (claude's own prompt) is answered by keys in the CLI, so the
  Agent never learns the answer and could not close the record. Native asks wait for a gate (decision 10,
  item 8) that shows the native allow, deny and cancel can be tied to the call id and that the record can be
  re-evaluated just before the call runs.
- **Persistence.** The ledger is an append-only file per session under the Agent's state directory. A state
  change is fsynced before an answer that relies on it goes back, so a reply the CLI received always has its
  commit on disk, and a unit held with no reply sent is released on restart (nothing ran). On restart, every
  `awaiting` attempt is released and its approval id expires: approvals belong to the Agent's **boot
  generation**, and a new generation never honours an old one. The waiting hook is a separate process the CLI
  started and may still be alive; decision 6 says how it ends. A retry of that call is a new attempt and, if it
  still needs one, a new card. A stored `ask` is never handed out again.
- **Restart, resume, recreate** keep the same session and reload its ledger. A ledger that is missing or
  fails to parse, for an AF session (every one is marked, decision 8), is treated by every closed policy as *caps
  exhausted, flags set*. A notice is raised. A person can reset the state, but only the state of policies in
  the layers that person may edit (decision 3): a member resets user- and session-layer counts and flags, and
  a deployment or tenant policy's state is reset by its own administrators. An observed (late) entry from an
  observe-only kind is marked as observed, not checked.
- **The trash** ([ADR 0101](0101-session-delete-via-trash.md)). The ledger travels with the session: it is
  not removed before the session's archive is written, a restore brings back the session's effective set,
  counts and flags as they were (a restore is not a reset), and a purge removes it. A restore never revives
  the old snapshot or an old approval id: the relaunch writes a new snapshot, and open approvals were released
  when the session stopped.

**Lineage: children and forks.** What a new session inherits differs by how it was started:

| How | Policies | Ledger |
|---|---|---|
| resume, restart, recreate | the same | the same ledger |
| fork | the parent's session layer | the gate flags and owned paths are copied; how caps are counted is open question 12 |
| child through `create_session` | the parent's session layer, which it can only add to | the gate flags are copied, so a child the parent asks to `git push` after the parent's download still asks; how caps are counted is open question 12 |
| a new session | none from another session | empty |

Until open question 12 is answered, caps are counted per session, and this record **does not claim** that a
parent cannot exceed its cap through children or parallel forks. #1054 asks the same question for spend.

### 5. Approvals surface on the existing cards

An `ask` verdict becomes one of the two approval surfaces the Console already has. A third one is not built.

- **Where the kind can ask natively** (claude Terminal with `ask`), from P1 and only after decision 10's
  item 8: the CLI shows its own menu and the existing `PermissionCard` answers it by keys. The card gains a
  line naming the policies that asked. In P0 every ask is AF-held (decision 4).
- **Everywhere else** the decision point raises an **AF-held approval**: an `Interaction` of kind `approval`
  with the existing `ApprovalRequest` (summary, tool, command, stages), plus a new optional `policies` field
  (each asking policy's id, layer and one-line reason). It renders as the existing `ApprovalCard` and raises
  the existing `permission-request` notice, so the chat bridge and Console notifications carry it unchanged.
  Because the Agent, not the CLI, owns this interaction, `POST /sessions/{name}/respond` accepts it for
  Terminal sessions too. The 501 stays for every other interaction of a Terminal session.
- **Scope** reuses `once` / `turn` / `thread`, limited by decision 3's rule on who may disarm what.
- **Only a person answers.** No af MCP tool answers an approval, and a parent session cannot approve for its
  child ([guide/member/02-sessions.md](../../guide/member/02-sessions.md) already says so for CLI approvals).
- **A stop does not carry an approval as a grant.** As in ADR 0055 decision 13, only the fact that an approval
  was pending survives. On resume, an answer to the old id is refused; the CLI's next attempt at the call is a
  new check, and if it still needs approval, a new card.

### 6. Holding an approval inside a hook, and the timeouts

In P0 every ask on claude, and on any kind until its native-ask gate (decision 10, item 8) passes, is held
inside the hook: codex has no native ask at all. The hook process itself waits while the AF-held approval is
open, and then prints `deny` or no decision.

- **The hook and the Agent are separate processes.** `workspace-agent policy-check` is a command the CLI
  executes (like the existing hooks, `hooks.go:28-30`), so the Agent can crash or restart while the CLI and
  the waiting hook live on. The hook waits on the Agent with the approval id and the Agent's boot generation.
  If the Agent answers that the generation is unknown, or cannot be reached until the hook's deadline, the
  hook prints `deny` with a reason and exits. An approval resumes only under a new approval id, through a new
  attempt.

- **AF's deadline is shorter than the CLI's.** The injected hook configuration sets the CLI's hook timeout
  explicitly, and the hook gives up at that timeout minus a margin (10 s in the first version), printing
  `deny` with a reason the model reads ("approval timed out; ask the user") and withdrawing the card. The
  policy's own `approval_timeout` may be shorter still. An internal schema string in claude 2.1.288 caps a hook
  timeout at 600 s, which is not measured; codex's is not known.
- **What the CLI does when a hook is killed, times out, or exits non-zero is a gate, not an assumption.** If a
  kind runs the call in any of those cases, it cannot host a held ask safely: for that kind `ask` policies
  are enforced only through a native ask, or the kind falls to observe for them (decision 10).
- **The call waits; the rest of the turn may.** The call whose hook is waiting does not run until the member
  answers or the deadline passes. Whether the CLI meanwhile runs sibling calls of the same turn, and what
  happens to a shell already running, is the kind's own scheduling and is measured (decision 10, item 9). The
  card promises only that this call waits.
- **Stop, interrupt and restart release the waiter.** The hook is told to deny, the card is withdrawn, and the
  record is released (decision 4). Decision 5 says what survives.

Whether an unattended session (a schedule, a child) should rather stop and wait is open question 4.

### 7. Kinds that cannot block, and capability changes

A policy carries `enforcement: block | observe`. **block** (the default for every built-in type) needs a kind
whose enforcement point has passed its gate for that policy type on this execution method.

- **At launch**, the Agent computes the effective set. If a `block` policy applies and the kind cannot block
  it, the launch is refused with a reason (`policy_requires_blocking_kind`), the same way ADR 0056 refuses
  `permission_choice_unsupported`. This applies to every launch path (Console, `create_session`, schedules,
  restart, fork, resume), because the Agent resolves it, as in ADR 0056 decision 3. **It ships in P0**, for
  every kind: otherwise a deployment's P0 policy would be bypassed by launching any other kind.
- **ACP kinds** with a `block` policy are launched with the skip flag off, once their P1 gate has shown that
  every call reaches the request with that configuration. The no-objection path then answers as decision 1
  says.
- **A running session that gains a block policy it cannot enforce** (a pull adds one, or tightens an observe
  policy to block, on an observe-only kind, or on an ACP session running with its skip flag on) is **stopped**:
  the Agent interrupts the turn and stops the session, with a notice that names the policy. On a kind with a
  blocking seam the next check is denied while the stop completes. On an observe-only kind there is no check
  to deny, so a call already in flight, or one racing the interrupt, may still run; the gap is measured
  (decision 10, item 9) and stated in the guide. Resuming it goes through the launch check, which relaunches an ACP kind with the flag off
  or refuses a kind that cannot block. A switch of execution method (Terminal ↔ Managed) goes through the same
  check.
- **observe** policies run on every kind whose class is observe or block. The ledger is fed late; a breach
  raises a notice and an audit record and may interrupt the turn (open question 7), but it cannot prevent the
  call that breached. On a kind whose class is **none** (shell, ssm), an observe policy is **not measurable**:
  the session is badged as such, and nothing pretends it is observed.

Whether refusing the launch is the right product answer, rather than launching with a visible "observe only"
badge, is open question 3.

### 8. Failure mode, per policy

Each policy carries `on_error: closed | open`. The built-in default is **closed** for `tool_call_cap`,
`approval_gate` and `path_scope`, and **open** for `ask_on_categories`. `ask_on_categories` is a
convenience, and failing it closed would turn an outage into a wall of denials.

**The snapshot** lets the hook decide when the Agent cannot answer. It is a per-session file the Agent writes
with the session id, a generation number and the policy version, and it says whether any closed policy
applies.

- **It is written atomically** (temporary file, fsync, rename) and **before** the state it describes takes
  effect. A launch waits for it; a tightening is written to the snapshot first and applied after. If the write
  fails, the launch is refused. For a running session the order is: apply the tightening in memory, stop the
  session and confirm its CLI has exited, and refuse to resume it until the snapshot is persisted. The
  window that remains is a crash of the Agent between the pull and the confirmed stop: while the Agent is
  down, the hook falls back to the older snapshot. That window is bounded by the Agent's restart (which
  re-applies the current copy before it answers any check) and is stated, not hidden. Apart from it, a
  snapshot is never looser than what the decision point is applying.
- **Every AF session is marked from launch**, whether or not a policy applies yet: the marker is the
  `AF_SESSION_NAME` it already carries, and the Agent writes a snapshot (empty if nothing applies) before
  the CLI starts. A first policy reaching a running session therefore never meets a session the hook would
  treat as ungoverned.
- **The hook cannot reach the Agent** (it is down or restarting): if the snapshot says a closed policy applies,
  the hook denies with a reason. If the session is marked and the snapshot is missing, unreadable, partial or
  for another session, the hook denies. Only a process with no `AF_SESSION_NAME` (a member's own terminal
  `claude`), or a snapshot that says no closed policy applies, gets no decision.
- **The CP is unreachable:** the last pulled copy stays in force for as long as the CP stays unreachable (as
  branch rules do), and the Console shows when the copy was last fetched. A deployment that has never pulled a
  copy starts with the built-in defaults, which are empty (open question 8). Whether a copy older than some
  bound should fail closed is open question 13.
- **A command cannot be parsed:** decision 2 (it matches).
- **The decision point panics or times out internally:** treated as unreachable.

The snapshot, like the ledger, is writable at the agent's uid: decision 9.

### 9. Security: a guard-rail at the agent's uid, not a sandbox

This layer catches accidents and prompt-injected sequences ("download this, then push"). It cannot stop an
agent that sets out to defeat it, because the agent runs at the same uid as everything that enforces it
(build/07 §7.2). Specifically:

- **An agent editing its own policy.** No policy is writable from inside the workspace: the Agent exposes no
  route that changes a policy, and the source is the CP. The agent can still write the ledger, the snapshot,
  the pulled cache and claude's `settings.json` (including `disableAllHooks`), and can unset
  `AF_SESSION_NAME` in a shell it starts. So the claude enforcement hook is placed where the agent cannot
  write it: a new, root-owned managed settings file under `/etc/claude-code/` in the image (today that
  directory holds only `CLAUDE.md`). Whether a managed hook survives a user-scope `disableAllHooks` in 2.1.288
  is a P0 gate. The ledger, snapshot and cache are checked for integrity against accidents only. Against a
  determined agent they are not protected, and the guide says so.
- **Prompt injection.** The deny reason the model reads names the policy and says "ask the user". It never
  explains how the rule is matched, so an injected instruction cannot learn the exact obfuscation. The card
  shows the command as written plus its parsed stages, so the member approves what will run, not the agent's
  description of it.
- **Bypass through a kind that cannot block.** Decision 7 refuses the launch, on every path including a child
  started through `create_session`, and stops a running session that gains a policy it cannot enforce. A
  nested CLI started inside a governed session (`claude -p` from Bash) inherits `AF_SESSION_NAME` and the
  managed hook, so it is checked against the parent's ledger. A shell session is a person's terminal and is not
  governed.
- **Bypass through children.** Gate flags are inherited (decision 4). Caps are not pooled until open question
  12 is answered, and the guide must not claim they are.
- **Bypass through the shell.** A `shell` call can do anything an `edit` call can, and more. `path_scope` and
  `created_by_session` see structured edits only. A policy that must also hold for the shell has to ask on
  `shell` too. The editor shows that trade-off rather than implying containment.
- **The decision point is not a judge.** It runs no model and calls no network; a policy cannot ask an LLM
  whether a call is safe.

### 10. Measurement gates before a kind counts as block

A kind moves from candidate to block for a policy type only when a real session, under the launch flags AF
actually uses (skip flag, trust, rtk on and off), shows all of the following. A kind that fails one, or cannot
be measured, stays observe.

1. **Coverage.** Every call reaches the seam: each category (`read`, `edit`, `shell`, `web`, `mcp`, `agent`),
   repeated calls, calls the CLI's own allowlist or an earlier "allow always" permits, and calls made by the
   CLI's sub-agents.
2. **Verdicts.** `deny` stops the call and the model reads the reason; no output leaves the CLI's own mode in
   charge; `ask` (where offered) prompts, including under the skip flag.
3. **Failures.** What happens when the hook times out, is killed, exits non-zero, or prints malformed output:
   whether the call runs. This decides whether a held ask (decision 6) is possible for the kind.
4. **Tamper resistance** (claude): whether the managed hook still fires after a user-scope `disableAllHooks`.
5. **Ordering.** Whether the policy hook sees the command before or after another hook rewrites it (rtk).
6. **Identity.** Whether the call id is stable across the check, the completion event and a retry.
7. **Budget.** The figures in § Performance budget.
8. **Native ask** (before a kind's own prompt replaces an AF-held approval): the native allow, deny and cancel
   can be tied to the call id, and the record can be re-evaluated just before the call runs.
9. **Concurrency and stop.** Whether the CLI runs sibling calls while one hook waits, what an interrupt or
   stop does to a call in flight, and, for observe-only kinds, how many calls can pass between a stop decision
   and the CLI's exit.
10. **Agent-only restart.** With the CLI and a waiting hook alive, restart only the Agent: the hook ends with
   `deny`, the old approval id is refused, and the CLI's next attempt gets a new card.

| Kind | Gate in | Notes |
|---|---|---|
| claude Terminal | P0 | items 1–7, 9 and 10; item 8 before native ask (P1) |
| lcpp | P1 | 1, 2 and 7 only; the harness is AF's own |
| cursor, kiro, copilot Managed | P1 | 1 with the skip flag off is the deciding item |
| codex Terminal, copilot Terminal | P2 | 2 is expected to show deny only for codex |
| codex Managed, opencode, kiro and cursor Terminal | P2 | the seam itself has to be built first |

### Performance budget

The check sits on every tool call. Measured: starting `workspace-agent --version` takes 17–33 ms (median
22 ms, 20 runs, one workspace, 2026-10-04). That is process start only, not a check with HTTP and persistence;
the budget below is what P0 must measure, not what this figure proves.

- **decision point:** ≤ 2 ms per check at p95 for evaluation, in memory. The fsynced ledger append
  (decision 4) and, for `path_scope`, the `stat`/`realpath` of the target are measured and reported
  separately per category, because their cost depends on the disk.
- **hook end to end** (process start, local HTTP, answer): ≤ 50 ms at p95 added per tool call. Time spent
  waiting for a person is excluded.
- **processes per call.** The policy check is one process per call. For `edit` and `shell` calls it absorbs
  the existing `permtool` recording, so those calls gain no process. `read`, `web`, `mcp` and `other` calls,
  where `permtool` never fires, gain one. The other claude hooks (AskUserQuestion, ExitPlanMode, rtk, the
  PostToolUse heartbeat) stay separate processes. P0 reports the total added cost per category.

A budget P0 cannot meet blocks P0 rather than being raised quietly.

### Out of scope

- Network egress per session. build/07 §7.8 owns egress, and a policy here does not replace the fence that is
  missing there.
- Model or token budgets (#1054).
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
- **Auto-approving every policy-clean ACP request.** That would silently remove the prompts a member chose
  with ADR 0056 (decision 1 keeps them).

## Consequences

- A new hot path in front of every tool call of a governed session. A bug there stops work in every such
  session, which is why the budget and the fail mode are per policy and measured.
- Sessions with a `block` policy run their ACP CLI with the skip flag off. When the member's choice was
  "skip", the decision point answers what the skip flag would have, so each kind's own "ask" set must be
  mapped (the P1 gate).
- The claude enforcement hook moves into a new managed settings file in the image, so it is present in every
  claude in the workspace, including a member's own terminal `claude` with no session. With no
  `AF_SESSION_NAME` it returns no decision at once.
- A running session can be stopped by a policy change it cannot enforce (decision 7).
- `respond` accepts AF-held approvals for Terminal sessions; the Console's approval card is shown for a
  Terminal session for the first time.
- `guide/ref/agents.md` gains a row "Tool policies: block / observe / none" per kind, checked against the
  kinds' capability flag by `scripts/docs-check.py`, like the permission-choice row.

## Open questions for the user

These are product decisions this record does not make. The measurements this design depends on are not here;
they are decision 10's gates.

1. **Layers.** Is a tenant layer wanted between deployment and user (decision 3), or exactly the issue's
   deployment → user → session?
2. **Who may set the session layer.** Only the member in the launch dialog, or also a parent session through
   `create_session` and a schedule? Decision 3 lets them add restrictions only.
3. **Kinds that cannot block.** Refuse the launch (decision 7, as written), or launch with an "observe only"
   badge and a warning?
4. **Unanswered approvals.** Deny after the timeout (decision 6, as written), or keep the session waiting
   (and for how long) in unattended sessions such as schedules and children?
5. **"Always allow" on a policy-held approval.** Allow `turn` / `thread` scope at all? If so, is decision 3's
   boundary right: a member may disarm their own user- and session-layer gates, never a deployment or tenant
   gate?
6. **The cap is reached.** Deny every further call (as written), or stop the session? And is the per-session
   exception of decision 3 offered at all, and to whom: only a person who may edit the layer that set the
   cap, or also the member for a deployment or tenant cap?
7. **Observe-only breaches.** Notify only, or also interrupt the turn?
8. **Defaults.** Ship with no policy enabled (as written), or with one deployment default such as the
   push-after-download gate?
9. *(moved to decision 10: measurements are gates, not decisions.)*
10. **User-authored policies.** The same closed grammar extended, or CEL later?
11. **Unparseable shell commands.** Do they set gate flags (as written), only ask for themselves, or let the
    member choose on the card (decision 2)?
12. **Lineage.** Are caps pooled across a parent, its children and its forks, or counted per session (as
    written)? The same answer should serve #1054's spend budget.
13. **A stale policy copy.** Keep the last copy in force indefinitely while the CP is unreachable (as
    written), or fail closed after a bound?

## Phases

| Phase | What | Done when |
|---|---|---|
| P0 | Decision 10's gates for claude Terminal. The decision point with the atomic, idempotent ledger and the snapshot. `tool_call_cap` and `approval_gate` (with in-call sequences and the parser). Deployment and user layers by pull. AF-held approvals on the existing cards. The launch refusal of decision 7 for **every** other kind, and the stop on an unenforceable change | the gates are written into this record; parallel calls cannot overshoot a cap and a retried check does not double-count; with `max=1` an approved awaiting call runs, and a retry after its reply was lost passes without consuming a second unit; a closed cap added to a session with incomplete history denies; in a real claude session `npm install x && git push` and the two as separate calls both raise a card naming the gate, deny reaches the model, a cap denies the N+1th call; launching any other kind under a block policy is refused; the budget holds |
| P1 | `path_scope` (with pending and owned paths), `ask_on_categories`; the session layer; lcpp with a check before every `tool.Run`; cursor, kiro and copilot Managed once their gate passes; the `guide/ref/agents.md` row | each listed kind passes its gate and the same scenarios |
| P2 | codex Terminal (deny, held ask) and Managed; opencode; copilot, kiro and cursor Terminal hooks; observe-only feed for agy and muse; the tenant layer if wanted | each kind's row in the matrix is measured, not inferred |
| P3 | User-authored declarative policies (open question 10) | — |

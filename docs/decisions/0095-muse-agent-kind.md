# 0095. Meta's Muse Code as a session kind (`muse`) — a vendor protocol instead of a TUI contract, behind three live gates

English | [日本語](0095-muse-agent-kind.ja.md)

- Status: **proposed** (2026-09-20), **Phase 1 gates A, B1 and B2 all answered** (2026-09-20 — the
  last three sections). None of the kind itself is implemented, and gate A's deployment changes
  have been taken back out again (the gate-A-artefacts section says why). Every `file:line` below was read on
  `06ea94d3` (develop at the time). Everything marked ◎ was measured in a Workspace container on
  **Muse Code 1.3.0-R3401.1** installed into a throwaway directory; △ is the vendor documentation
  only; × is not measured. The probe is reproducible from the last section.
- The request is one sentence: **can Meta's coding agent Muse Code become the tenth session kind,
  and at what cost?**
- See also: [0015](0015-agent-managed-driver.md) (the managed driver contract this kind implements) /
  [0019](0019-copilot-agent-kind.md) (externally minted session ids) /
  [0023](0023-cursor-agent-kind.md) (the pin / auto-update / plan-gating checklist) /
  [0026](0026-kiro-agent-kind.md) (the most recent shipped kind — the template for "what a kind
  touches") / [0093](0093-lcpp-agent-kind.md) (managed-only kind groundwork: whichever of the two
  lands first pays for it) / `docs/log/74-rovo-agent-kind.md` (the watershed table this one answers)

## Context

### What Muse Code is

A terminal coding agent released by Meta Superintelligence Labs on 2026-08-05, powered by
Muse Spark (default model `muse-spark-1.2`; 1.3 rolling out since 2026-09-02). One statically
linked binary, macOS / Linux / Windows, x86 and aarch64. It is the first entrant whose **session
protocol is a published product surface** rather than a debug seam, and the first whose own feature
set overlaps Agent Fleet's: it has its own subagents, workflows, cross-session messaging, skills and
memory.

### The watershed table (◎ measured 2026-09-20 / △ docs only / × not measured)

| Watershed | Verdict | Evidence |
|---|---|---|
| Managed contract | **◎ the best any kind has offered** | `muse serve` is newline-delimited JSON-RPC 2.0 over **stdio**. `muse schema generate-json-schema` exports MSP v1 offline: **47 methods, 31 notifications, 31 errors, 234 types**, fingerprint `sha256:7469c9e3…`. Driven end to end unauthenticated: `initialize` → `initialized` → `session/start` → `turn/start` |
| State detection | **◎ a contract, not a string** | `session/statusChanged` carries `running` / `idle` / `notLoaded`. No TUI scraping, no hook file, no footer heuristic |
| Session id | **◎ AF mints it** | `session/start.sessionId` took a UUIDv7 we minted and used it verbatim, on the wire and in the on-disk path. `muse exec --session-id <uuid>` is the same field |
| Read source | **◎ append-only** | `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` — **one file per root session, subagent records interleaved into it** under their own `stream.id` (gate B1; there is no `subagent/<id>/` directory). `session/start`'s result returns that `path`, so AF never has to guess it |
| Version pin + sha256 | **◎ both, anonymously** | Channel manifest (`api.meta.ai/muse-code/channels/muse-stable`, HTTP 200 anonymous) names a **version-addressable** release manifest which publishes url + **sha256** + size per platform, `aarch64_linux` included. The artifact itself served HTTP 206 anonymously — no account is needed to bake |
| Auto-update suppression | ◎ | `MUSE_NO_AUTO_UPDATE=1`, one environment variable. With it set and the binary in place, the launcher **only reads**: `muse --version` succeeded from a read-only install directory |
| Cost of building the harness | **◎ ≈ $0** | `--provider echo` is a deterministic built-in provider. `muse exec --provider echo --json` ran a complete session with no credential. The account is needed for acceptance, not for construction |
| Host footprint | ◎ | An idle `muse serve` host is **≈ 73 MiB RSS** (74,924 KB); **under load, host plus children peaked at 137–147 MiB** (gate B1, the maximum on a turn that spawned a subagent). Compare the registry's `tuiMemoryCost`: claude 230 MiB, opencode 300 MiB (`console/src/agents/registry.ts:227,469`) |
| **OS sandbox** | **🔴 ◎ cannot run here, permanently** | Linux sandboxing is bubblewrap (38 `bwrap` / 50 `seccomp` strings in the binary; the docs say "needs a working bubblewrap and a non-musl build. Without it, every sandboxed shell command aborts as an environment failure"). Gate A settled it with a real `bwrap`: a user namespace is creatable and grants all 41 capabilities, yet **`mount(2)` and `move_mount(2)` return EACCES regardless of capabilities** while `fsopen` / `open_tree` succeed — the signature of **AppArmor's `docker-default` profile**, not of seccomp or capabilities. Muse also ships an *embedded* bwrap, so the binary's absence was never the blocker |
| **Shared repository state** | **🔴 ◎ it writes there** | `muse exec -w create` chose `<repo>/.muse/worktrees/<date>-<hash>` as the workspace root, created `.muse/.session-worktree-reservations/`, and **appended `/.muse/worktrees/` to `.git/info/exclude`**. In a linked worktree that file is the parent clone's, shared with every other session |
| **Foreign personal context** | **🔴 ◎ on by default, and it reaches the model** | First run printed `Including your Codex personal rules and 5 skills`. Gate B1 measured the consequence: with foreign context left alone, a real turn **answered with the contents of `~/.claude/CLAUDE.md`** — a member's Claude Code rules go to Meta. The flag that stops it, `--no-foreign-personal-context`, exists on `muse exec` but **not on `muse serve`** (measured: `unknown option`); the settings keys that do work are `context.foreign_personal_rules` and `context.foreign_personal_skills`, **both** needed |
| Feature overlap | ⚠️ △ | Subagents (8 per tree by default, `agents.execution_capacity` 1–64), four background observer agents that each make their own model calls, workflows (1,000 children lifetime), a **user-wide session-name namespace** and peer messaging — all invisible to Agent Fleet's registry, mirror and usage ledger |
| Auth | **◎ device code** | `muse login` prints `https://auth.meta.com/oauth/device/?code=XXXX-XXXX` and polls — no TTY, no local callback, the same start→poll shape cursor and kiro already use. It writes `~/.config/muse/auth.json`. An API key (`META_API_KEY` or `muse auth set`) always **overrides** the account login, so on a subscription it must never be set |
| Billing | **◎ on the wire** | Pay-as-you-go per token, or a flat subscription whose quota `usage/read` / `usage/changed` report as `{tier, weekly{usedPercent}, window{usedPercent, windowDurationMins: 300}}`. Measured on Everyday Usage: 10 prompts moved the 5-hour window 0 % → 6 %. `model/list` prices are **`cost: null`** for all four models |
| Real-turn behaviour | **◎ measured** | Gate B1: turns, tool calls, an approval round trip, a `userInput` round trip, a subagent, an interrupt, a failure, a resume and per-completion token usage — see the gate B1 section. The one thing the wire does **not** carry is subagent and observer usage |
| TUI text contract | × | Not measured, and Decision 2 makes it unnecessary |

### What the repository already has

- Nine kinds (`workspace/agent/internal/session/session.go:20-28`), registered twice:
  `sessionx/agent.go:28-38` for the read layer and `sessionx/session_turn.go:31-37` for the five
  managed drivers. **An unregistered kind is silently normalised to claude**, not rejected
  (`AgentOf` at `sessionx/agent.go:40-45`, `NormalizeKind` at `:49-54`).
- The contract a kind implements: `Agent` — six methods (`agents/agents.go:141-161`) — and
  `Driver` + `ThreadHandle` — seven methods (`agents/driver.go:132-165`).
  `Capabilities.ProcessModel` is `shared-daemon` | `per-session-child` | `tui` (`driver.go:146`).
- `usageMeasuredForKind` (`workspace/agent/usage_fold.go:204-212`) has exactly three exact kinds
  (claude, codex, opencode), copilot partial, the rest none.
- MCP: `mcpreg.knownKinds` is seven kinds (`mcpreg/def.go:57-61`) and `MaterializedKinds` writes a
  native config per kind (`mcpreg/materialize.go:47,74-87`).
- Instruction distribution is six kinds (`workspace/agent/agent_instructions.go:83`).
- Console: the descriptor table `console/src/agents/registry.ts`, the dynamic-model list
  `console/src/lib/agentModels.ts:34-35`, the colour tokens `console/src/styles/tokens.css:109-117`
  (dark) and `:259-`(light), the usage stack order `console/src/features/usage/colors.ts:81`.
- `"muse": "meta"` is **already** a model-family prefix in `workspace/agent/model_provider.go:69`.
- Versions are pinned as Dockerfile `ARG`s (`workspace/Dockerfile:318-321` for the four npm CLIs —
  claude, opencode, codex, copilot — and `:404-411` for cursor's versioned tarball) and surfaced by
  `workspace/agent/env_tool_versions.go`.

## Decision

### Decision 1 — the kind is `muse`

`musecode` is long; `meta` is the vendor, not the agent, and would read as "the Meta provider" next
to `model_provider.go`'s vendor ids; `spark` is the model. `muse` already exists in that same table
as a model-family prefix resolving to Meta (`model_provider.go:69`) — the exact situation `codex`
has lived in since it became a kind, so it is precedent rather than a collision.

`session.KindMuse`, label `Muse Code`, `assistantName: "Muse"`, `short: "mu"`, `launchSuffix: "-mu"`
(free among `""/-cx/-cu/-ag/-cp/-ki/-oc/-sh`, and `-lc` if ADR 0093 lands first), `cssClass: "muse"`.

**The colour is a separate decision from the hue.** Meta blue would be the third blue next to
`--kind-agy: #4285f4` and `--kind-ssm: #6d8bf5` (`tokens.css:112,117`). Whether the brand value can
be used at all is settled by rendering both themes and measuring ΔE2000 against the nine existing
kinds before the build, the way docs/log/74 §8.2 settled the third blue. The stacked usage chart is
a narrower problem than that: `KIND_STACK_ORDER` (`colors.ts:81`) holds the seven chat kinds and
**not** shell or ssm, so what must not be adjacent there is agy and muse — two blues, not three.

### Decision 2 — `muse` is managed-only: MSP over stdio, no Terminal (CLI) route

MSP already carries everything a pane would give us and more (statuses, approvals, steering, fork,
usage, skills, model list), so a tmux pane would buy a second UI and a string contract to maintain.
**The create path is part of that gate, and it defaults the wrong way today.**
`POST /sessions` normalises an empty *and* an explicit `tui` driver to `""`
(`sessionx/session_handlers.go:650-668`), so for this kind `""` must resolve to `managed` and an
explicit `tui` must be refused with 400. Every caller that sends no driver at all — handoff, spawn,
REST and MCP create, and the Control Plane scheduler, whose own kind list
(`control-plane/scheduler_wake.go:277-285`) decides paneless launches — would otherwise ask for a
pane that cannot exist.

This is the same shape ADR 0093 Decision 2 proposes for `lcpp`; the two share one cost — the
"no terminal route" gate is five Console sites plus the server-side managed→TUI transition
(`console/src/features/repos/{LaunchModal.tsx,StartModal.tsx,RepoRowConnected.tsx}`,
`console/src/features/sessions/{SessionMenu.tsx,useSessionActions.tsx}`,
`workspace/agent/internal/sessionx/session_driver.go:62-105`) plus the create-path default above.
**Whichever of 0093 and 0095 lands first pays for it; the second gets it free.** If neither has
landed when this kind starts, the gate is in this kind's estimate.

### Decision 3 — `per-session-child`, one `muse serve` per session

MSP hosts several sessions per process (`session/list`, per-connection auto-subscribe), so a shared
daemon is possible. v1 does not take it, and the reason is **which knobs are host-wide, not that
any knob is** — approval mode is per session on the wire, so it alone would not decide this.
Measured from `muse serve --help`, the host fixes for its whole lifetime: the sandbox posture,
`--sandbox-network`, `--disable-write`, `--disable-shell`, **`--trust-workspace`** and session
durability. **One of those is already per session here and cannot be anything else**: trust is a
decision about a *working copy*, and every Agent Fleet session has its own — a shared host would
trust the first session's repository on behalf of every repository loaded after it. The rest is a
forward constraint rather than a present one, and the ADR should not overstate it: Agent Fleet's
session-level axes today are `Meta.Mode` and `Meta.SkipPermissions`
(`session.go:377-390`), of which only approval has a wire equivalent; a read-only posture mapped
onto `--disable-write` / `--disable-shell` would need them per session, and on a shared host it
could not have them. Two lesser reasons follow: a host crash would take every session on it, and
stopping one session would mean draining a process that others are loaded in. At **73 MiB idle** (measured) a child per session is affordable — a third of
claude's pane. `Capabilities.ProcessModel = "per-session-child"`, the same value cursor, kiro and
copilot already use, so no enum change is needed.

The shared daemon is re-evaluated, not rejected forever: the condition is a measured per-session
footprint under load (not idle) high enough to matter, plus a way to express per-session write /
shell / trust posture on the wire.

### Decision 4 — the transcript: MSP live, the session JSONL at rest

`item/started` / `item/delta` / `item/completed` feed `transcript.Turn` while the host is up;
when it is not, the read layer parses
`~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl`, which is append-only and holds the
same records. **AF does not compute that path**: `session/start`'s result returns it, so it is
stored on `session.Meta` at creation (the date partition otherwise makes discovery a dated guess —
the trap kiro hit with cwd+mtime, ADR 0026 decision 6).

**The session id is stored, not derived.** AF's usual trick — a deterministic UUIDv5 of
(dir, slot name), as kiro's `slotSid` does — cannot be used here: the schema states that a retained
or reserved id is rejected with `commandRejected` / `session_id_conflict`, so an id is good exactly
once. AF mints one per session and keeps it on `Meta` beside the path. What discards it is **not**
`ClearResume` — that method is never called outside its own interface declaration and one agy test —
but the fact that recreate and fork build a fresh `session.Meta` from an explicit whitelist of
fields (`sessionx/session_handlers.go:1381-1390` and `:1083-1098`), so a field nobody lists is
structurally not inherited. The inverse is the thing to guard: a future path that copies `Meta`
wholesale would carry a single-use id forward and make the next `session/start` fail with
`session_id_conflict`. Whether Muse accepts a v5 at all is unmeasured — the
probe passed a v7, which the schema names as the server's own default.

**Subagent records are in the parent's file, not under `subagent/<id>/`** — measured in gate B1,
correcting this decision's first draft. A session that ran one subagent produced exactly one
`session.jsonl`, with the child's records interleaved and distinguished by
`stream: {kind: "session", id: <child session id>}`; there is no per-child directory anywhere in
the store. That makes the at-rest reader simpler (one file to tail) and is also what makes the
accounting fix of Decision 10 possible at all. v1 renders subagent activity as tool-shaped items
on the parent turn and does not open a pane per child.

### Decision 5 — the sandbox is turned off, and the approval gate goes with it

`muse serve --disable-sandbox`. Measured, bubblewrap cannot build a sandbox in a Workspace
container (`move_mount` → EACCES), and with the sandbox on and unusable **every shell command the
agent runs fails** — the kind would not do any work at all. ⚠️ Gate B1 ran that case and the
failure is *narrower and quieter* than the vendor's "aborts as an environment failure": the
`toolCall` item comes back `status: "failed"` with
`visibleOutput: "bwrap: Failed to make / slave: Permission denied"` while **`turn/completed` says
`terminal: "completed"`**. A session launched without the flag therefore looks healthy in the
Console and silently accomplishes nothing, so the driver asserts the flag at spawn instead of
trusting it. Approvals are
orthogonal and stay on: approval mode is selected per session on the wire, and AF answers
`approval/requested` through `approval/decide`, mapping the launch-time permission choice
(docs/log/76) onto `untrusted` | `on-request` | `never`.

> 🔴 **Corrected by measurement, P2-6 (2026-09-21). "Approvals are orthogonal and stay on" is
> false, and it is the one sentence in this ADR that a reader must not act on.** Approvals are
> not orthogonal to the sandbox at all: `--disable-sandbox` resolves the host's committed
> permission profile to `filesystem.mode: "unrestricted"` with no rules and
> `local_command_network.mode: "enabled"`, and with nothing restricted every tool call resolves
> `policy_decision: "allow:policy"` before the approval layer is consulted. Measured with real
> turns under `approvalMode: "onRequest"`, a muse session ran an in-workspace `tool:bash` **and**
> wrote a file outside `workspaceRoot` entirely, with zero `approval/requested`. It is also not
> narrowable: `--disable-write`, `--disable-shell` and `--sandbox-network <mode>` leave that
> profile byte-identical while this flag is set. So the sandbox waiver takes the tool gate with
> it, `Caps.PermissionChoice` is false, and the guide says a muse session reaches this container
> the way `shell` does. The full record, including what the wire still honours, is in P2-6.

This is a waiver, and the ADR states it as one: inside a Workspace the container **is** the
boundary, and the same is already true of every other kind, none of which sandboxes itself.

**The waiver is permanent under the current Workspace host contract — gate A confirmed it with a
real `bwrap` and named the mechanism.** Permanent because nothing *we* ship can change it, not
because no change is conceivable. The denial is not a missing binary and not an artefact of one
mount API: inside a user namespace this container refuses `move_mount` (the new API, which
util-linux prefers) **and** the classic `mount(2)` that bubblewrap itself calls, both with EACCES.
Gate A closed the remaining ambiguity by varying capabilities: `fsopen` and `open_tree` go from
EPERM to success once the namespace grants `CAP_SYS_ADMIN`, while `mount` and `move_mount` stay
EACCES **whatever the capabilities are** — so the refusal is neither a capability check (EPERM) nor
seccomp (capability-blind), but **AppArmor's `docker-default` profile**, applied by the container
runtime and unchangeable from inside. Two further findings make the point independent of that
policy: muse **embeds its own bubblewrap**, so no system `bwrap` was ever the blocker, and Debian's
`bwrap` lacks `--ro-bind-symlink`, which muse requires, so muse would reject it anyway. Only a
change in what the *host* permits (LSM policy, seccomp, capabilities) would reopen this — see the
gate A section for the full matrix.

### Decision 6 — AF owns `~/.config/muse/settings.json`, and clamps eight behaviours in it

The file requires `"schema_version": 1` or **every command fails at startup**, so it is written, not
merged blindly. **There is exactly one writer, and it is muse's own.** The clamps below
and the `mcp_servers` block of Decision 11 are two blocks of the same file, and Muse writes it too
(measured: a first run created `settings.json` **and** its own `~/.config/muse/.settings.json.lock`).
Three writers on one file with two locks is a lost update, so **one owner writes this file**: a
muse-specific settings writer, serialised read-merge-rename under the existing `materializeMu`
(`mcpreg/materialize.go:94-109`) rather than a second mutex beside it, and it **preserves every key
it does not own** — a member's own `tui`, model defaults and telemetry keys survive an AF write.

Two things follow that the first draft got wrong, and both are design work, not wording:

- **It is its own writer, not the JSON MCP materialiser with a second block.** That materialiser
  returns early when the server set did not change (`mcpreg/materialize_json.go:110-112`) and
  removes its key entirely when the set is empty — correct for MCP, fatal for clamps, which must be
  written on a kind with zero MCP servers. Muse therefore gets its own writer inside `mcpreg` that
  merges clamps in one pass. (Gate B1 removed the `mcp_servers` half of this writer's job — see
  Decision 11: the wire route is honoured, so the file never needs an AF-written server block.)
- **The clamps are fail-close, and the gate belongs inside `Resume`.** Today `Materialize` logs and
  swallows every failure by design — "a session must still launch when its MCP config could not be
  updated" — and `StartManagedSession` calls it without looking at the result before `Resume`
  (`mcpx/mcp_materialize.go:30-41`). For MCP that is the right trade; for these clamps it is not,
  because they are the safety mechanism: a failed write would start a Muse session with eight
  subagents, four observers, workflows, the approval judge and foreign context all enabled.
  **`StartManagedSession` is the wrong place to enforce it**, because it is not the only way a child
  is spawned: `Resume` is also reached directly from the turn, answer, carried-session and bridge
  paths, and from each kind's `ReconcileManaged` at Agent start (`workspace/agent/main.go:189-193`,
  which runs after the best-effort `MaterializeAll`). A restart or a dead child would then start an
  unclamped Muse. So the write happens inside the muse driver's `Resume`, before the child is
  spawned, and returns an error that refuses the start. The precedent is kiro's `ensureSettings`
  (`agents/kiro/program.go:196-208`) with its two defects fixed: not a `sync.Once`, and not
  swallowing the error. Boot-time `MaterializeAll` keeps its best-effort contract for every kind.

**The lock protocol is measured, not guessed** (gate B1, by syscall): `open(".settings.json.lock",
O_RDWR|O_CREAT, 0666)`, `flock(LOCK_EX)` — advisory BSD flock, blocking, no `LOCK_NB` — then a
temp file in the same directory, `fchmod`, `fsync`, `rename` over the target, `fsync` of the
directory, `flock(LOCK_UN)`. AF takes exactly that. Two measured details change the design:
**muse reads the file before taking the lock**, so its own update is a read-merge-write with an
unprotected read and AF must re-read and verify after writing rather than assume its merge
survived; and **muse blocks indefinitely** on a held lock, so AF must not hold it across anything
slow.

AF sets (spellings and effects as measured in gate B1 — the gate B1 section carries the table,
including which two clamps are still spelling-only):

1. `agents.execution_capacity` — a small cap. Left alone, one session may run eight agents on a
   memory-constrained shared host.
2. Background observers off. Four of them each make their own model calls; on a metered account they
   are invisible spend, and on a prompt-quota subscription they are invisible quota — **and,
   measured, they cost turn latency: 17.9 s versus 4.5 s for the same one-line answer**, because
   the end-of-turn gate waits for them (`eot_gate_ms: 11518`). No settings key was found; the route
   that worked is `MUSE_EXPERIMENTAL_{SKILL,GOAL,VERIFY,TODO,MEMORY,SCOPE}_REMINDER=0` in the
   child's environment.
3. Workflows off — `run.workflow_trigger_mode: "off"`, measured: `muse.workflow` leaves the tool
   list (`auto` would let a session spawn up to 1,000 children).
4. Worktree isolation off, and `-w` never passed. Decision 7. Also
   `run.subagent_delegation_mode: "off"` when a deployment wants no children at all — measured: all
   six `muse.subagent_*` tools disappear, which is the cheapest way to make the ledger of
   Decision 10 exact.
5. Foreign personal context off. Reading `~/.claude` and `~/.codex` silently mixes another kind's
   instruction layer into this one, and those directories are off-limits by workspace policy.
   **This is not hypothetical: measured, a real turn answered with the contents of
   `~/.claude/CLAUDE.md`** — the member's Claude Code rules went to Meta.
   **The settings file is the only route**: `--no-foreign-personal-context` exists on `muse exec`
   but **not on `muse serve`** (measured — `muse serve … --no-foreign-personal-context` answers
   `unknown option`), and Decision 2 makes `serve` the only process AF runs. It takes **two keys**,
   `context.foreign_personal_rules: false` and `context.foreign_personal_skills: false`; one
   without the other leaves the other half on.
6. The approval judge off (`--approval-judge off`, or `MUSE_DISABLE_APPROVAL_JUDGE`). It is **on by
   default** and makes its own model call for every Prompt-bound approval — on a metered account
   that is spend the member never asked for, and Decision 5 keeps approvals on, so it fires often.
   ⚠️ Like clamp 5 this flag is absent from `muse serve`, and gate B1 found **no** settings key for
   it, so the env variable is the only candidate and its effect is unmeasured.
7. The bundled skills that reach outside this session. `muse skills list` ships `resume-claude`,
   `resume-codex`, `import`, `migrate` and `read-session`, whose stated job is to read Claude Code's
   or Codex's transcripts, memory notes and MCP servers — clamp 5 governs *discovery* of foreign
   rules, while these are foreign readers invoked on demand into exactly the directories workspace
   policy puts off-limits. It also ships `daemon` and `host-manager`, which stand up long-running
   processes outside AF's session bookkeeping, and `slack-connector`, which is an egress path.
   ⚠️ **The disable key is version-fragile** (measured): `muse skills disable bundled:resume-claude`
   writes `skills.activation.bundled["bundled://muse-core/skills/resume-claude/SKILL.md"] = "off"` —
   keyed by a pack-qualified *path*, not by skill id, so a rename or move in 1.4 silently
   re-enables it. This belongs in the drift check, not only in gate B1.
8. **A non-contributor model by default — but the member owns the choice.** Gate B1: the host's
   default model is `muse-spark-1.3-contributor`, whose catalogue description reads "Your content,
   including inter-session messages, may be used for product improvement". There is no settings key
   for it — the route is `session/start.modelId` (and `session/setModel`), which is honoured for
   every model call including after a resume that passes no model.

   This is the one clamp that is **not** AF's to decide silently, and the user settled it
   (2026-09-20): **it is a member-facing setting in Settings → Agents**, not a hidden pin. AF
   defaults it to the non-contributor model — the safe direction, and the one a member who never
   opens settings gets — and the contributor variants stay selectable for a member who wants them
   (they are the same model at the same price; the difference is only what Meta may do with the
   content). The clamp's mechanism is unchanged; what changes is that the value comes from the
   member's setting rather than from a constant, and that the setting says in plain words what
   choosing a contributor model means. Consequences: `console/src/features/settings/agents/`
   gains the control (and its i18n in both languages), the guide explains the trade-off, and the
   launch-defaults store carries it the way it already carries model and effort per kind
   (`console/src/lib/settings.ts`). Everything else in this list stays a clamp AF sets.
   ⚠️ The session's *stored metadata* and every `session` projection keep reporting the contributor
   id (measured in all five sessions), so the Console must take the model from
   `session/tokenUsage.modelId`, never from the session projection.

**Two silent failure modes make a behavioural test per key mandatory** (gate B1): a misspelling
inside a section muse parses strictly makes `muse serve` **exit rc=3 before `initialize`**, while a
misspelling anywhere else starts the host happily with the clamp **simply not in effect** and no
diagnostic at all. "We wrote the JSON" is not evidence that a clamp is on.

Muse's own peer messaging and session-name authority (`~/.local/share/muse/session-name-authority/`,
user-wide) are **not** wired to AF's cross-session messaging in v1: two peer channels with one
namespace, one of them invisible in the mirror, is how `native-peer-channel-invisible-in-mirror`
happened. The guide says they exist and that AF does not see them.

### Decision 7 — the kind edits working-copy files and nothing else: no branches, no worktrees, no repository-wide metadata

Editing tracked files in its own working copy is the job, and is not what this decision restrains.
What it forbids is the **project and version-control surface around** those files: repository-global
metadata, branches and worktrees. It is explicitly **not** a rule about every path outside the
working copy — Decisions 4, 6 and 9 require Muse to write `~/.config/muse` and
`~/.local/share/muse`, which are the kind's own state and are governed by the deny-list instead.
Measured, a Muse worktree run rewrites
`.git/info/exclude`, which in a linked worktree is the parent clone's file, shared with every other
session. So: `-w` is never passed, worktree isolation is off (Decision 6), and the kind is declared
as one that creates no branches and no worktrees. A member who wants parallel writers gets
AF's own worktrees, which is what they are for.

**Gate B1 measured the other half of this claim, which the ADR previously had to assume.** Six real
turns across five sessions with `-w` never passed — a shell tool call, a subagent that wrote a file,
an interrupt, a resume — wrote nothing into the working copy except the files the agent was asked
to create: no `.muse/`, no `.muse/.session-worktree-reservations/`, `.git/info/exclude`
byte-identical to git's default, `git worktree list` still a single entry. The shared-state hazard
belongs to `-w`, and not passing it is sufficient.

### Decision 8 — deployment: bake the pinned binary, guard the shadow in `~/.local/bin`

**Muse Code is proprietary, so the distributed image does not contain it — the same rule Claude
Code, Copilot CLI and Antigravity already live under.** `ARG BAKE_AGENT_CLIS=0` is the Dockerfile's
default and its comment gives the licence reason (`Dockerfile:69-77`: proprietary CLIs are treated
as not redistributable, so a stray `docker build` cannot ship one),
`deploy/compose/release.sh:53-55` makes lean the distribution default, and `NOTICE:57-62` states the
consequence to the reader: proprietary agent CLIs are not bundled and deployments fetch them at
first start. So there are **two variants and this ADR decides both**, rather than deciding the one
that happens to be convenient:

- **The shipped one (`BAKE_AGENT_CLIS=0`)**: the entrypoint installs from the pin in
  `/usr/local/share/agent-fleet/versions.json`, verifying the release manifest's sha256. Being able
  to fetch it anonymously (measured) is what makes this viable at all. **It installs into
  `~/.local`, the same place the vendor's own installer uses** (`workspace/Dockerfile:71`,
  `entrypoint.sh:299-349`), which has one consequence this ADR got backwards below. What AF puts
  there is **the binary, not the vendor's bash launcher** (gate A: the manifest artifact *is* the
  binary and runs standalone), so AF's own copy has no self-update path at all.
  ⚠️ **It is not unconditional, and as of the gate-A-artefacts review it is not in the tree
  either.** Gate A shipped it as an explicit opt-in (`AF_MUSE_BOOT_INSTALL=1`, default off) because
  299 MiB on every fresh container, for a kind that does not exist before Phase 2, is the same bill
  that already moved kiro (855 MiB) off unconditional boot-install — and then the same reasoning
  removed the opt-in as well, since nothing can reach it before the kind exists. **Phase 2 writes
  kiro's shape instead**: a per-user on-demand `workspace-agent install-muse`. What the removed code
  knew, written down so Phase 2 does not have to rediscover it: the artifact URL is
  `https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=<ver>&file=<asset>`
  with `<asset>` ∈ `muse-x86-linux` | `muse-aarch64-linux`, the sha256 to verify is
  `artifacts.{x86_linux,aarch64_linux}.checksum` from the version-addressed release manifest, and
  the downloaded file is the binary, installed under the name `muse`.
- **`BAKE_AGENT_CLIS=1`** (self-hosted deployments that want a fast first start): `ARG MUSE_VERSION`
  + sha256 per arch verified at build, the runtime laid out as the launcher expects
  (`muse-bin-<version>` plus `.muse-version` beside the launcher) under `/usr/local/share/muse`.
  Measured, that layout works from a read-only directory.

Both share `MUSE_NO_AUTO_UPDATE=1` from the entrypoint, a row in `env_tool_versions.go`, and a
`NOTICE` entry — which is not paperwork here but the file that tells a reader which proprietary
CLIs a deployment fetches.

Two costs are named rather than discovered later. **Size**: ≈ 299 MiB (x86_64) / ≈ 269 MiB
(aarch64) — exactly 313,800,920 B and 281,942,104 B per the release manifest. That is a download on
every fresh container that opts in (19 s measured), and image growth in the baked one; it is also
the reason the opt-in above is off by default. **The shadow, and how not to detect it wrongly**: the vendor installer's default target is
`~/.local/bin/muse` — and in the shipped variant that is *also where AF puts it*, so **a check for
the path would report AF's own binary**. The hazard is not the location but the provenance: a
member who runs the one-line installer once ends up on an unmanaged, self-updating build that
survives a recreate. So the check is **version identity** — does `muse --version` match the pin in
`versions.json`? — and the repair already exists: the entrypoint re-pins `~/.local` back to the
pinned version on a start where self-update is off (`entrypoint.sh:336-352`, the same hole kiro's
launch guard closed). The connection card reports a version mismatch, not a path. Gate A measured
both directions: a shadow reporting a drifted version was replaced, and a shadow reporting the
**pinned** version was left alone. ⚠️ One detail the implementation must not get wrong —
`muse --version` prints `Muse Code 1.3.0 (1.3.0-R3401.1)`, so the comparison has to take the
parenthesised build id; the `tr -dc '0-9.'` idiom the agy block uses would drop `-R3401.1` and
mismatch on every start.

### Decision 9 — the credential is an account login by device code; the API key is the fallback

**Corrected by gate B1, and it changes the connection card rather than merely annotating it.**
`muse login` is a **device-code** flow: it prints `https://auth.meta.com/oauth/device/?code=XXXX-XXXX`
and polls until the member approves it in whatever browser they already have. No TTY, no local
callback, nothing this container cannot do — the same **start → poll** shape cursor's and kiro's
connection cards already implement (`control-plane/routes.go:869-875` is kiro's start/poll/delete
precedent). Measured end to end on a real subscription: the managed route ran turns and
`turn/completed` reported `terminal: "completed"`.

🔴 **On a subscription, AF must never write an API key.** `muse auth set --api-key-stdin` and
`META_API_KEY` both **override** the stored account login, which silently moves the member from
their flat-rate plan onto metered billing. So the API key stays as the pay-as-you-go path only, and
`muse auth set --api-key-stdin` remains the way to enter one: `muse auth --help` prints the usage as
`muse auth set [--provider <PROVIDER>] --api-key-stdin`, and `muse auth set --help` lists it as the
one accepted way to pass a secret, "never taken as a command-line argument, so it never lands in
shell history" (both measured on 1.3.0). Either route writes `~/.config/muse/auth.json`.

**Two paths join the file deny-list, not one**: `~/.config/muse` (the credential) *and*
`~/.local/share/muse` — which holds every session's full transcript and the user-wide session-name
authority. The precedent is exact: `fs.go:131-137` already denies `.local/share/opencode`, `.codex`
and `.kiro` for the same reason, the credential *and* the session store.

Not `META_API_KEY` in the child's environment: the reason cursor refused env
injection (ADR 0023) applies unchanged, and an API key always overrides a stored session, so an
env-level key would silently defeat a member who later signs in. The connection card is therefore
**one input** — simpler than every kind except none — with `muse logout` behind the disconnect.

**Gate B2 is closed "yes"**: a subscription needs no browser onboarding on this machine, so
subscriptions are in scope for v1 and the guide does not have to carry a "pay-as-you-go only"
caveat. The card is therefore *two* affordances, not one input: "sign in" (device code, start →
poll, the default) and "use an API key" (one secret, metered), with `muse logout` behind the
disconnect.

### Decision 10 — usage and the model list ride the protocol

`usage/read`, `session/tokenUsage` and `session/contextUsage` exist on the wire, and the accounting
matrix of gate B1 has now run. **The verdict is `MeasuredPartial`** (`usage_fold.go:204-212`), and
the reason is not the one this decision feared. Cached input is cleanly separated
(`cacheReadTokens` inside `inputTokens`, with a server-derived counted-once `promptTokens`), the
cumulative block never needs differencing, a failed turn reports nothing, an interrupted turn
reports exactly what it spent, and `session/resume` replays **no** usage events, so nothing doubles.

🔴 **What is missing is ownership.** Subagent and observer model calls are *never* folded into
`session/tokenUsage` — the schema says so in one line and gate B1 measured it: a turn with one
subagent reported 88,077 prompt tokens on the wire while the durable log recorded 116,816 across
six calls, two of them owned by `subagent-1` (`owner_type: "native_child"`). Folding the
notifications alone under-reports such a turn by 24 %. Exactness therefore costs either a
durable-log fold (`goal_usage_attribution` records, which do carry the owner) or the clamps of
Decision 6 — with subagents and observers off, the wire numbers *are* complete, which is the cheap
v1: `MeasuredPartial` in the switch, clamps on, and the door to exact left open.

**One chip has no source and one has a shape the ADR had wrong.** The cost estimate reads a kind →
models.dev provider table (`workspace/agent/usage_catalog.go:45-55`) that has no Meta row, and the
authenticated catalogue reports **`cost: null`** on all four models, so there is still no price per
token and v1 ships a token ledger with no cost chip. The quota, though, *is* on the wire:
`usage/read` and the unsolicited `usage/changed` carry
`{tier, weekly{resetsAtMs, usedPercent}, window{resetsAtMs, usedPercent, windowDurationMins: 300}}`
— the shape `get_agent_usage` already speaks (claude / codex / agy), not the TUI-only `/upgrade`
this decision assumed. ⚠️ But it is the *host's last observation*, not a query: measured,
`usage/read` answers `{}` until a completion has been seen, so under Decision 3's one-host-per-session
shape **a freshly launched session has no quota chip until its first turn finishes**.

`model/list` backs the picker, which means `agentModels.ts:34-35`'s `isDynamic` must list `muse`.
That one line is the recurring miss of every new kind (copilot, cursor), and it presents as "the
model picker only shows the default".

### Decision 11 — MCP rides the wire: `session/start.config.mcpServers`, and the shared settings file is left to the member

**Gate B1 settled the choice this decision left open, in favour of the cheaper route.** Passing one
stdio server only in `session/start.config.mcpServers` — with
`capabilities.requestedCapabilities: ["sessionMcp"]`, granted, and `MUSE_ENABLE_SESSION_MCP` unset —
produced a live connection: the server logged muse's handshake
(`clientInfo {"name":"tbh","version":"0.1.0"}`, MCP `2025-06-18`), received `MUSE_SESSION_ID` and the
config's `env` additions, and its tool reached the model as `mcp__<server>.<tool>`. `mode: "optional"`
is accepted on the wire.

So **AF materialises MCP per session on the wire and writes no `mcp_servers` block at all.** Three
consequences: the settings writer of Decision 6 carries clamps only, the lost-update surface shrinks
with it, and per-session server sets — which a user-wide file cannot express — become possible.
⚠️ **Servers start at the first turn, not at `session/start`** (measured: three session-only runs
spawned nothing), so any health check at session creation reads "not connected" forever.

The file route remains **documented, not written**: a member's own `mcp_servers` block is preserved
by AF's writer like every other key it does not own. Its shape, for that reason, still matters:

`mcp_servers` is a block inside the same `settings.json`, with `transport: stdio | streamable_http`,
`command`/`args`/`env` or `url`/`headers`, `enabled`, and **`mode`, which defaults to `required` —
a required server that fails to start aborts the whole run**. AF materialises **every** server as
`mode: optional`, with no member-facing choice: a broken tenant server must not make the agent
refuse to start, and the registry has nowhere to put the choice — `secrets.MCPServer`
(`workspace/agent/internal/secrets/secrets.go:302-324`) has `enabled`, `targets`, `kinds` and
`timeoutMs` but no `mode`, so offering it means a new field plus wire, Console and stored-definition
migration. That is named as out of scope here rather than discovered in Phase 2. AF's own servers go
on the wire as `mode: optional` for the same reason: a broken tenant server must not stop the agent
from starting.

`${VAR}` interpolation exists and `MUSE_SESSION_ID` is passed to stdio servers (measured). `muse` joins
`knownKinds` (`mcpreg/def.go:57-61`) and `MaterializedKinds` (`materialize.go:47`). For project
scope it joins `mcpproj`'s `kindInfos` (`mcpproj/inspect.go:50-58`) with
**`HasProjectScope: false`** — the shape agy already has — because no Muse project-scope spelling is
documented. `fileSpecs` (`inspect.go:36-44`) gains no row, so muse is neither inspected nor a copy
target; that is a static fact about the kind, not a runtime fallback to another kind's file.
Hooks (`.muse/hooks.json`, 15 lifecycle events including `Stop` and `Notification`) are **not** used:
the protocol already reports what a hook would, and a hook file in the repo is shared state.

### Decision 12 — the project layer costs one host-wide decision (`--trust-workspace`); the user layer is one file both apply paths have to share

Project scope is **not** free, which the first two drafts had wrong. Muse reads the repository's own
`AGENTS.md` / `CLAUDE.md` only after the workspace is trusted, and measured, an untrusted session
says so and carries on without them: `rules file at …/AGENTS.md exists, but the workspace is
untrusted, so it is skipped for this session; restart with --trust-workspace`. Trust is **not on the
wire** — the string `trust` does not occur anywhere in the MSP schema (measured, 0 occurrences) —
so the only way to have project rules is `muse serve --trust-workspace`, which is host-wide and
therefore, under Decision 3's one-host-per-session shape, a per-session decision AF makes when it
spawns the child. (`muse exec` does take it per run — measured, `workspace trust: trusted
source=run-flag` — so this is a property of `serve`, not of the product.) It also **raises the price
of the shared daemon** Decision 3 leaves open for re-evaluation: one host would then have to trust
every repository, or serve none of them their own rules.

Two consequences follow. **AF passes `--trust-workspace`** for a working copy the member launched a
session in — refusing it would silently drop the repository's own `AGENTS.md`, which is where this
project keeps its conventions. And the same flag loads **the repository's skills**, so a clone can
contribute behaviour: that is the documented `.agents/skills/` mechanism, it is the same trust
decision Console already makes when it launches any kind in that working copy, and the guide says
it plainly rather than leaving it implicit.

Note for the guide: the discovery order is a **precedence**, not a union. Measured, with both files
present, `CLAUDE.md is ignored this session because AGENTS.md takes precedence` — harmless here,
but a member whose repository keeps the real text in `CLAUDE.md` and a stub in `AGENTS.md` loses it
without an error.

The other two layers are not solved by Decision 6's switch, and they are **two mechanisms, not
one**. Turning off foreign personal context stops Muse reading `~/.claude` and `~/.codex`; it
delivers neither. `agent_instructions.go:119-146` applies them separately — `ApplyFleetNotes` per
kind for the fleet policy (claude's arrives as a file under `/etc` instead, a different route
again), and `ApplyUserInstructions` per kind for the member's own text, each with its own target
and its own artefact. `instrSupportedKinds:83` is the list of kinds that have both.

**Gate B1 measured both targets, and the answer is that muse has one rules file and one skills
root.** Markers planted in candidate locations and read back out of the model's own answer, with the
syscall trace as the second witness:

- **User-scope rules: `~/.config/muse/AGENTS.md`** (`$XDG_CONFIG_HOME/muse/AGENTS.md`). Planted
  there, its text came back in the answer. `~/.config/muse/CLAUDE.md` is probed too, so the project
  layer's "AGENTS.md wins" precedence probably applies here as well — untested. Nothing under
  `$museHome` is read.
- **The fleet-topics route already works**: a hand-dropped `~/.config/muse/skills/<name>/SKILL.md` is
  listed by `muse skills list --source user` with no install step and no lock-file entry, so
  `fleetskills.Apply` (`agent_instructions.go:135-141`, today claude / codex / opencode) needs
  nothing new for muse.
- 🔴 **But `ApplyFleetNotes` and `ApplyUserInstructions` must share that one `AGENTS.md`.** Every
  other kind gets either a directory of steering files (kiro: `agent-fleet-guide.md` and
  `agent-fleet-user.md`) or two separate artefacts. Muse has one file, so Phase 2 must decide
  explicitly: AF owns `~/.config/muse/AGENTS.md` outright with delimited sections (fleet policy
  first, as `applyInstructionsLocked` orders them) and the guide says so, or AF merges into a
  member's own text by markers. This is the one place muse's instruction layer is *more* awkward
  than kiro's.

So `muse` joins `instrSupportedKinds` (`agent_instructions.go:83`) in Phase 2 with both apply paths,
and the Console's per-kind distribution status comes with it.

⚠️ One scope note for the guide: `foreign_personal_*` governs the **personal** layer only. With both
clamps on and the workspace trusted, muse still probes the *repository's* `.claude/CLAUDE.md`,
`.claude/skills/`, `.codex/skills/` and `.claude-plugin/plugin.json` (measured). In this repository
those exist and are read as project context.

### Decision 13 — the capability declaration, and the one capability that is genuinely new: `Permissions`

`Capabilities` is not just `ProcessModel`, and the earlier drafts decided only that. MSP carries
`turn/steer`, `session/fork`, `session/setModel` and `session/setReasoningEffort`, so `Steer`,
`Fork`, `DynamicModel` and `DynamicEffort` are true, and `muse --help` exposes
`--reasoning-effort none|minimal|low|medium|high|xhigh|max|ultra`. **None of that is new**: opencode
already declares all of them (`agents/opencode/driver.go:73-84`), and codex all but one.

**`DynamicMode` is false.** `ThreadSettings.Mode` is AF's plan mode — the comment says
`"plan" | "normal"` (`agents/driver.go:37`) — and MSP has no method that sets it; `session/setApprovalMode`
changes the approval posture, which Decision 5 already maps to the launch-time permission choice.
Reading one wire method as two different AF axes is how a capability table starts lying.

**`Permissions: true` is the new thing, and it is not a wiring job.** Every managed kind today
declares it false, and `Interaction.Kind` is documented as `"question" (future: "approval" |
"plan")` with the note that "all three kinds run with approvals bypassed"
(`agents/driver.go:42-52`, the sentence itself at `:43`). So declaring it means **building AF's first approval interaction** —
wire type, Console card, and the answer path back through `approval/decide`. ADR 0093 is already
paying part of that bill: `workspace/agent/internal/harness/approval.go:18` names decision 5's
`Permissions: true` as the property that kind sells, and that package is on develop. Whichever lands
first pays, as with the managed-only gate.

**`Caps.PermissionChoice` is the other half, and it is a different struct.** The read layer's
`Caps` gates the create request: `POST /sessions` refuses `skip_permissions=false` for any kind
whose `Caps().PermissionChoice` is false (`sessionx/session_handlers.go:643-647`,
`agents/agents.go:86-92`). Decision 5 keeps approvals on, so without that flag the launch flow
cannot even ask for them. It is also what `guide/ref`'s capability tables are checked against, so
it lands in the documentation in the same change.

`Questions` is separately true and is a *different channel from approvals*: `userInput/requested` →
`userInput/answer`, with `userInput/settled` closing it. An approval asks "may I run this"; a
user-input request asks the member a question. AF has the question kind already; the approval kind
is the one being built.

**Gate B1 ran both round trips, and two measured details belong in the driver rather than in a
surprise.** First, **the host delivers both as notifications**, not as the server-initiated
`approval/request` / `userInput/request` the schema also declares — a client that answers only the
request form leaves the turn parked at `attention: ["approvalPending"]` forever (observed). Second,
**both re-deliver**, and answering twice returns `-32056 userInputAlreadySettled` carrying
`settlement.outcome: "answered"`; the handler must be idempotent and read that error as success.

The shapes themselves are kind to us. An approval carries `toolName`, `rawArgs`,
`judgeEscalated`, `protectedWrite`, a `subject` (`kind: "shell"`, `command`, and `stages[]` with the
parsed `argv` per stage) and exactly **two** `availableChoices` in `onRequest` mode — `allow_once`
(`decision: "approved"`, `scope: "once"`) and `abort` (`acceptsFeedback: true`). So the first
permission card needs no scope selector, and open question 5 is answered: `subject.command` plus the
stage argv is the whole render. `requirementId` must be echoed back verbatim — it is the multi-stage
race guard. And a `userInput` question maps onto AF's existing interaction field for field:
`{id, header, question, selection: {mode: "single"}, options: [{label, description}]}`.

## What Phase 2 touches

The checklist below is the one docs/log/43 §4 and docs/log/74 §8 turned into a rule after two kinds
shipped with pieces missing; every anchor was re-read on `06ea94d3`. It is the estimate's basis, and
none of it is optional.

| Area | Sites |
|---|---|
| Kind identity | `session.go:20-28`, `sessionx/agent.go:28-38` (registry; the silent claude fallback is `AgentOf:40-45` and `NormalizeKind:49-54`), `sessionx/session_turn.go:31-37` (managed driver map), `workspace/agent/main.go:189-193` (the per-kind `ReconcileManaged` goroutine — mandatory for a per-session-child kind), `sessionx/turn_end_poll.go:62-66`, `connections.go:54-60`, `sessionx/session_skills.go:80-91` (muse has skills), `control-plane/enkana_dict.go:199-205` |
| Managed-only gate | `sessionx/session_handlers.go:650-668` (create default), `session_driver.go:62-105`, five Console launch / driver-switch sites, `control-plane/scheduler_wake.go:277-285` |
| Connection + login | connection status, login routes on **both** `routes.go` files (Agent and CP — kiro's precedent is start, poll *and* delete, `control-plane/routes.go:869-875`), and the CP REST proxy allow-list, whose omission is how a usage chip silently never appears |
| Model + vendor | `console/src/lib/agentModels.ts:34-35` (`isDynamic`), `workspace/agent/model_provider.go:122` (`modelKindVendor`), and the models REST switch `workspace/agent/agent_models.go:40-83` |
| Usage | `usage_fold.go:204-212`, the cost table `usage_catalog.go:45-55`, the usage stack colour `console/src/features/usage/colors.ts:81` |
| Instructions | both apply paths in `agent_instructions.go:119-146` and the list at `:83`; the targets are measured (Decision 12) — `~/.config/muse/AGENTS.md`, shared by both paths, plus `fleetskills.Apply` into `~/.config/muse/skills` |
| MCP — **four** separate lists (but no file dialect: Decision 11 puts servers on the wire) | the registry (`mcpreg/def.go:57-61`, `mcpreg/materialize.go:47,74-87`, `mcpproj/inspect.go:36-44,50-58`); the **local** `af` server (`mcpx/mcp_stdio.go`: the `list_models` descriptor, the `a.Kind != …` validation, and the `driver = "managed"` list — cited by symbol because these three moved by six lines between `06ea94d3` and `73ac5cdc`); the **CP** MCP tools (`control-plane/internal/mcpsrv/mcp.go:295,473,487,518-550` — description, schema and runtime validation are three edits, not one); and `mcpsrv/mcp_server.go:70-74`'s `mcpKnownKinds`, a fourth copy of the same list. Tool descriptions are a fixed per-session token cost, so they are edited, not grown |
| Console surface | `console/src/types/session.ts:9-12` (`SessionKind` and the display order — nothing renders without it), `console/src/agents/registry.ts` descriptor, `console/src/lib/settings.ts:958-966` launch defaults, the `LaunchDefaults` kind union `console/src/features/settings/agents/AgentCardParts.tsx:76`, a new `MuseCard.tsx` wired from `features/settings/agents/AgentsTab.tsx:250`, `features/settings/workspace/EnvTab.tsx`, `console/src/features/settings/mcp/mcpWire.ts:9` (`MCP_KINDS`, mirrors the Go list), `ScheduleDetailModal.tsx`'s `AGENT_KINDS`, `features/mirror/{turnTime.ts:10,FileChangeStrip.tsx:69}`, `features/repos/ProjectActionPanels.tsx:34`, `console/src/lib/brandicons.ts:36` plus the icon asset itself, `console/src/lib/termcolor.ts:19`, and the colour twins across `tokens.css` and the five feature stylesheets (docs/log/74 §9.3) |
| Deployment + CI | all of it is Phase 2's, since the gate-A pin came back out: a per-user on-demand install of kiro's shape (`workspace-agent install-muse`), the pin in **`/usr/local/share/agent-fleet/versions.json`** and the `ARG` behind it, the entrypoint's version-identity re-pin (`entrypoint.sh:299-352`; `MUSE_NO_AUTO_UPDATE` is already there), `workspace/Dockerfile`'s `BAKE_AGENT_CLIS=1` path, `env_tool_versions.go`, **`NOTICE`** (proprietary CLIs are listed there, not bundled), `deploy/local/cli-drift-check.sh`, and the release / drift workflows and setup action that carry every other pinned CLI |
| Text | `bridge/format.go`'s `kindLabel`, the Console i18n catalogues (en + ja), the user guide, and `guide/ref`'s capability tables — which `scripts/docs-check.py` checks against `Caps()` in both languages |
| Tests | the MSP schema-fingerprint drift test, route and contract tests, and the e2e smoke that pins the baked version string |

## Alternatives rejected

- **A Terminal (CLI) route in v1.** A second UI plus a TUI string contract, for a kind whose protocol
  already exposes more than the pane does (Decision 2).
- **One shared `muse serve` daemon for all sessions.** Sandbox posture and durability are fixed per
  host, so sessions would inherit each other's posture (Decision 3). Revisit only if per-session
  73 MiB ever stops being affordable.
- **`META_API_KEY` in the environment.** Silently overrides a stored sign-in and repeats the exposure
  argument cursor already settled (Decision 9).
- **Letting Muse's subagents, workflows, observers and peer messaging run as shipped.** Unbounded fan-out
  and invisible spend on a shared host, duplicating four Agent Fleet features with a second,
  unobservable implementation (Decision 6).
- **Wiring Muse's peer messaging to AF's.** One namespace, two channels, one of them invisible in the
  mirror.
- **The community ACP adapter** (`muse-code-acp`) as the managed seam. A third-party translation layer
  in front of a first-party protocol that is versioned and fingerprinted; it can only lose.
- **An unpinned, self-updating per-user install.** Not on-demand installation itself — Phase 2 takes
  kiro's on-demand shape deliberately, because 299 MiB on every fresh container for a kind most
  members will not use is the bill that already moved kiro off boot-install. The location is not the
  difference either; the shipped variant also installs under `~/.local` (Decision 8). What is
  rejected is dropping the **pin**: the install must re-pin to `versions.json` and the connection
  card must compare `muse --version`'s parenthesised build id against it.
- **Adopting now on the strength of the protocol.** The three gates below are cheap and every one of
  them is about something no amount of reading settles.

## Consequences

- **What the kind gets for free that others paid for**: no TUI string-contract tests, no hook file, no
  polling state detection, no session-id discovery, no transcript reverse-engineering, and —
  because `--provider echo` exists — a credential-free harness for **transport, transcript, status
  and steering**. That last one has a boundary worth stating: echo runs no tools and assembles no
  rules, so approvals, `userInput`, dynamic model and effort, the accounting matrix and the
  instruction layers all need a key. "$0 to build" is true of the plumbing, not of the whole kind.
- **What it owes**: the sandbox waiver (Decision 5), a settings file it must not corrupt (Decision 6),
  a vendor whose feature set overlaps ours (Decision 6, and a guide section explaining what AF does
  not see), and a beta that moved 1.2 → 1.3 in one month.
- **Drift has a lock**: `muse schema` is offline and the release manifest carries
  `msp_schema_fingerprint`. A test asserting the baked binary's fingerprint equals the one the
  generated types were built from turns a silent protocol change into a red build. No other kind has
  this.
- **Estimate**: managed-only, no TUI assets, **22–33 session-days in the table, 23–35 expected today**
  (the managed-only gate is still unpaid — see below) — the sum of the table, with no
  rounding applied to make a tidier headline. It has moved every round, and that is the honest
  signal: 14–20 (arithmetic wrong, rows short) → 15–23 → 20–31 → 22–33, as each review found work the
  table did not have. The last move is mostly one line — building AF's first approval interaction —
  and one correction downward, since the dynamic axes turned out to be `UpdateSettings`, already
  charged in the driver row. The anchor
  for the scale is the kind-wide inventory — 90 Go files and 41 Console files mention `kiro` today,
  and existing kinds are 2,100–5,950 non-test lines each — but the split below is what the number
  is made of, and it is the part to argue with:

  | Work package | Days |
  |---|---|
  | MSP client, generated types, the fingerprint drift test | 3–4 |
  | Driver + `ThreadHandle` (7 methods), status, steering, interrupt, resume reconciliation | 3–4 |
  | Transcript: live items + the at-rest JSONL, subagent items | 2–3 |
  | Settings single writer + 7 clamps + the fail-close wiring in `Resume` + the MCP dialect | 3–4 |
  | **AF's first approval `Interaction` kind** (wire, Console card, answer path) + the `userInput` question channel + the permission-choice mapping | 3–5 |
  | `Meta.Effort` wiring and the Console model / effort controls (`UpdateSettings` itself is in the driver row) | 1 |
  | Connection card, both `routes.go`, the REST allow-list, deny-list | 1–2 |
  | Usage + model list + the accounting tests | 1–2 |
  | Instruction layers: both apply paths and the distribution status (Decision 12) | 1–2 |
  | Deployment **both variants** (boot-install + bake), pin, sha256, shadow guard, `env_tool_versions`, `NOTICE`, release / drift CI | 2–3 |
  | Console surface, i18n, guide, `guide/ref` capability tables | 2–3 |

  **ADR 0093's state changes this, and it is half-landed**: `workspace/agent/internal/harness/` is
  on develop (its approval work names the same `Permissions: true`), while `session.KindLcpp` does
  not exist yet. So today the managed-only gate of Decision 2 is unpaid and the expected figure is
  **23–35 days**, the table plus that gate.

  **That asymmetry is a recommendation, not a symmetry.** The ADR says "whichever lands first pays"
  twice, but only one of the two is actually moving. If 0093 lands first, muse's marginal cost drops
  by the managed-only gate and by as much of the approval interaction as 0093's harness turns out to
  cover — **19–28 days** with the gate free and the approval row still ours, and lower still if that
  row is genuinely shared. Sequencing 0093 ahead of muse is therefore worth roughly a working week,
  and it is the order this ADR recommends. The direction of the error is still upward: the largest single
  unknown is how much of MSP's 47 methods and 31 notifications the driver actually has to implement
  to be correct rather than merely working, and three review rounds have each moved the number the
  same way.
- If Phase 1's gates fail, the sunk cost is this ADR and the probe — no code.

## Phases

| Phase | Content | Gate to the next |
|---|---|---|
| 0 | The probe in this ADR (done 2026-09-20): install, MSP drive, pin/checksum, worktree and foreign-context behaviour, footprint | — |
| 1 | **Gate A — ✅ done 2026-09-20** (see the gate A section): the waiver is permanent and the denier is named (AppArmor `docker-default`), plus two premises corrected (muse embeds its own bwrap; Debian's lacks `--ro-bind-symlink`). Decision 8's shipped path ran end to end and turned up a real defect: the sha256 check was decorative at all five boot-install sites. Its deployment artefacts have since been removed again — see the gate-A-artefacts section. **Gate B1 — ✅ done 2026-09-20** (gate B1 section): 10 subscription prompts bought the accounting matrix (cache, subagent and observer attribution, a failed and an interrupted turn, post-resume, cumulative vs per-turn), `model/list`, an `approval` round trip, a `userInput` round trip, a subagent, the loaded RSS, the measured effect of five clamps and the spelling of six, the lock protocol by syscall, the instruction targets, the MCP wire route, and what a `-w`-less run writes. **Gate B2 — ✅ "yes"**: `muse login` is a device code, so subscriptions are in scope | **All three answered.** What remains for the user, not for a measurement: accepting the Decision 6 clamps (including clamp 8, a policy choice about product-improvement data) and the spend |
| 2 | Implementation: kind wiring, MSP client and generated types, driver, transcript, usage, the settings clamp writer, connection card, deployment, guide, this ADR to *adopted* | — |

## Open questions

Five of the seven are answered; what is left is answered in Phase 2, not by another gate.

1. ✅ The accounting matrix of Decision 10 — **`MeasuredPartial`**, because subagent and observer
   usage never reaches the wire (gate B1-1). Not because of the cache, and not because of cumulative
   folding: both of those are clean.
2. ✅ Where Muse reads **user-scope** rules — `~/.config/muse/AGENTS.md`, one file for both of AF's
   apply paths, plus `~/.config/muse/skills` for the fleet topics (gate B1-5).
3. ✅ Subscription vs pay-as-you-go (gate B2) — **device code, subscriptions are in scope.**
4. ✅ `model/list` returns four models once authenticated (`source: providerCatalog`,
   `contextLimit 1007997`, `outputLimit 128000`, `cost: null`). Not plan-dependent in any way we saw,
   but 🔴 the **default is `muse-spark-1.3-contributor`**, which is what clamp 8 exists for.
5. ✅ How an approval renders — `subject.command` plus `subject.stages[].argv`, two choices, no scope
   selector (gate B1-2).
6. Whether `session/list` on a per-session host can see other AF sessions' Muse sessions (one store,
   one user) — and if so, that the Console never offers them. **Still open**; note that with one host
   per session the store is shared even though the hosts are not.
7. Which path `session/start.workspaceRoot` gets when the session has a `Meta.Subdir`: the working
   copy or the subdirectory. It decides what `--trust-workspace` covers, and `--allow-workspace-switch`
   exists, so the wrong answer is recoverable but confusing. **Still open.**
8. New, from gate B1: **muse's own `cron_*` tools survive every clamp measured.** An agent that can
   schedule its own future runs sits beside Agent Fleet's scheduler with no key found to stop it —
   Phase 2 either finds the key or says in the guide that AF does not see those runs.

## Reproducing the probe (2026-09-20, Muse Code 1.3.0-R3401.1)

```bash
curl -fsSL https://dev.meta.ai/install.sh -o install.sh          # launcher only; reads plainly
MUSE_INSTALL_DIR=$HOME/muse-probe MUSE_NO_MODIFY_PATH=1 \
MUSE_NO_AUTO_UPDATE=1 MUSE_LOGIN=0 bash install.sh               # ~300 MiB, no account
curl -s https://api.meta.ai/muse-code/channels/muse-stable       # version + manifest_url, anonymous
~/muse-probe/muse schema generate-json-schema --out ./msp        # 47 methods, offline
~/muse-probe/muse exec --provider echo --json "say hi"           # a full session, no credential
~/muse-probe/muse serve --disable-sandbox                        # JSON-RPC lines on stdin/stdout
unshare --user --map-root-user --mount --propagation unchanged \
  strace -e trace=move_mount mount -t tmpfs none /tmp/x          # new API: move_mount → EACCES
LIBMOUNT_FORCE_MOUNT2=always unshare --user --map-root-user --mount \
  --propagation unchanged strace -e trace=mount \
  mount -t tmpfs none /tmp/x                                     # bwrap's API: mount(2) → EACCES
~/muse-probe/muse auth set --help                                # the flag is mandatory
```

Gate A added (2026-09-20). `apt` needs root, so the probe extracts the same package by hand:

```bash
curl -fsSL -o bw.deb http://deb.debian.org/debian/pool/main/b/bubblewrap/\
bubblewrap_0.12.0-1~deb13u1_amd64.deb
dpkg-deb -x bw.deb root/                                         # no root needed
./root/usr/bin/bwrap --ro-bind / / --dev /dev true               # Failed to make / slave: EACCES
strace -f -e trace=mount,move_mount,open_tree,fsopen,unshare,clone \
  ./root/usr/bin/bwrap --ro-bind / / --dev /dev true             # clone OK, first mount() EACCES
cat /proc/self/attr/current                                      # docker-default (enforce)
unshare --user --map-root-user --mount --propagation unchanged \
  grep CapEff /proc/self/status                                  # 000001ffffffffff = all caps
./root/usr/bin/bwrap --ro-bind-symlink /etc /etc true            # Unknown option (muse requires it)
~/muse-probe/muse sandbox --help                                 # windows check|setup ONLY
```

Gate B1 added (2026-09-20). The driver is `~/msp2.py` (a 150-line MSP client that answers the
server's notifications) with one script per scenario; what matters is reproducible without it:

```bash
~/muse-probe/muse login                                          # device code, no TTY, no callback
# The two free oracles. The first names a misspelled key; the second shows a clamp's EFFECT
# without spending a subscription prompt, because an echo run records the assembled toolset.
echo '{"schema_version":1,"settings":{"agents":{"zzz":1}}}' > d.json
~/muse-probe/muse config validate --plane defaults --file d.json # unknown_member location=…
HOME=/tmp/fh XDG_CONFIG_HOME=/tmp/fh/cfg XDG_DATA_HOME=/tmp/fh/data \
  ~/muse-probe/muse exec --provider echo "hi"                    # then grep toolset.active_tools
                                                                 # in the session.jsonl it wrote
# The clamps that bite, and how the failure modes differ
printf '%s' '{"schema_version":1,"agents":{"zzz":1}}' > $XDG_CONFIG_HOME/muse/settings.json
~/muse-probe/muse serve --disable-sandbox </dev/null; echo $?    # 3, "malformed settings file"
printf '%s' '{"schema_version":1,"contxt":{"foreign_personal_skills":false}}' > …/settings.json
~/muse-probe/muse skills list --source user                      # silent: the clamp is simply off
# The settings lock, and the negative control that proves it is honoured
strace -f -e trace=%file,%desc ~/muse-probe/muse skills disable bundled:resume-claude \
  --scope built-in                                               # flock(LOCK_EX) on .lock, then rename
python3 -c 'import fcntl;f=open(".settings.json.lock","r+");fcntl.flock(f,fcntl.LOCK_EX);input()' &
~/muse-probe/muse skills enable bundled:resume-claude --scope built-in  # blocks until released
# Where the rules come from: plant markers, then ask the model which ones it can see
echo AFPROBE-USER > ~/.config/muse/AGENTS.md                     # user scope, delivered
echo AFPROBE-PROJECT > <ws>/AGENTS.md                            # project scope, needs --trust-workspace
```

⚠️ Run the real turns with a **throwaway `HOME`** carrying fake `~/.claude` / `~/.codex` markers.
With foreign personal context left on, a real turn ships the contents of `~/.claude/CLAUDE.md` to
Meta — that is the measurement, and it must not be made with the member's own files.

## Sources checked (2026-09-20, `06ea94d3`)

`workspace/agent/internal/session/session.go` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go,session_handlers.go}` ·
`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/mcpreg/{def.go,materialize.go}` ·
`workspace/agent/internal/mcpproj/inspect.go` · `workspace/agent/internal/secrets/secrets.go` ·
`workspace/agent/internal/bridge/format.go` ·
`workspace/agent/{usage_fold.go,agent_instructions.go,model_provider.go,env_tool_versions.go,fs.go}` ·
`control-plane/{routes.go,scheduler_wake.go}` · `control-plane/internal/mcpsrv/mcp.go` ·
`workspace/Dockerfile` · `console/src/agents/registry.ts` · `console/src/lib/{agentModels.ts,settings.ts,brandicons.ts,termcolor.ts}` ·
`console/src/styles/tokens.css` · `console/src/features/usage/colors.ts` ·
`docs/log/{36,40,43,74}-*-agent-kind.md` · `docs/decisions/0093-lcpp-agent-kind.md` ·
Meta's own documentation at `dev.meta.ai/docs/muse-code/{,auth,subscriptions,permissions,interactive,workflows,session-messaging,rewind,configuration,extending,changelog}`.

## Review round 1 (2026-09-20, a separate codex / gpt-5.6-sol session)

Fifteen findings, every anchor re-read against the tree before acting on it. Four were structural
and are corrected in the decisions above rather than noted here: the create path defaults an empty
driver to TUI (Decision 2), `muse auth set` needs `--api-key-stdin` (Decision 9), the deny-list owes
`~/.local/share/muse` as well (Decision 9), and `mcpproj` does not fall back to another kind's file
(Decision 11). Decision 3's rationale was internally weak and was rewritten around the flags that
are genuinely host-wide; Decisions 6, 10, 11 and 12, the Phase 2 checklist, the phase gates and the
estimate table are all products of this round.

Two corrections went the other way, and the review found them: this ADR had said the stacked chart
must keep *three* blues apart when `KIND_STACK_ORDER` holds neither shell nor ssm (two), and had
cited `workspace/Dockerfile:318-320` as three npm CLIs when it is four at `:318-321`.

The review confirmed, by re-reading: the nine kinds, `Agent`'s six methods, `ThreadHandle`'s seven,
the exact/partial usage sets, the seven MCP kinds, the six instruction kinds, the registry's RSS
figures, the model prefix and the colour tokens; that the English and Japanese texts carry the same
claims and numbers; and that `scripts/docs-check.py` is green. It did not log in, run a real turn,
test a subscription, run a real `bwrap`, or re-verify the vendor documentation — which is exactly
the boundary Phase 1 exists to cross.

## Review round 2 (2026-09-20, the same session, against the corrected text)

Twelve findings; the corrections above are the answer to all of them, and three were errors the
first round's own fixes introduced. **Decision 7's rewrite had over-reached** — forbidding "any path
outside this session's working copy" contradicted Decisions 4, 6 and 9, which require writing Muse's
own state directories. **Decision 6 was fail-open**: `Materialize` logs and swallows failures by
design and `StartManagedSession` never looks at the result (`mcpx/mcp_materialize.go:30-41`), so a
failed write would have started a session with every clamp off; and layering the clamps onto the
JSON materialiser would not have written them at all on a kind with no MCP servers
(`materialize_json.go:110-112`). **The estimate table did not add up**: the same nine rows summed to
15–23 while the headline said 14–20, so the headline moved rather than the rows.

Decision 3's rationale was still too strong — Agent Fleet has no per-session write / shell / trust
posture today, only `Meta.Mode` and `Meta.SkipPermissions` (`session.go:377-390`) — and is now
argued from the one knob that genuinely cannot be shared (workspace trust, because every session is
a different working copy). Decision 12 described one distribution mechanism where the tree has two
(`agent_instructions.go:119-146`). Decision 5's "permanent" is now qualified by the host contract it
depends on. The Phase 2 checklist gained the MCP kind enums in both MCP servers, the models REST
switch, five Console sites it had missed, and the deployment and CI rows that Decision 8 implied but
the table omitted; `routes.go`'s kiro precedent is start, poll and delete (`:869-875`), not start
alone.

Confirmed unchanged: Decision 2's create-path anchors and the scheduler's, Decision 11's
`MCPServer` and `mcpproj` findings, the English/Japanese correspondence, and `docs-check` green.

## Review round 3 (2026-09-20, a fresh claude / opus session)

A third reader, told which findings were already closed and asked to spend its effort on premises
nobody had questioned. Fifteen findings, fourteen of them right. Three changed a decision outright:

- **The clamp switch AF needs does not exist on the process AF runs.**
  `--no-foreign-personal-context` is a `muse exec` flag; `muse serve` answers `unknown option`
  (reproduced). Decision 6's "or its settings equivalent" was not an alternative — it is the only
  route, and the key's spelling is now a gate item rather than an assumption.
- **The project instruction layer was never free** (Decision 12). An untrusted workspace skips
  `AGENTS.md` and says so, and `trust` does not appear in the MSP schema at all, so it can only be a
  host flag. That turns "nothing to do" into a decision to pass `--trust-workspace`, and into an
  admission that doing so also loads the repository's skills.
- **Fail-close in `StartManagedSession` would not have held.** `Resume` is reached from the turn,
  answer, carried-session and bridge paths and from `ReconcileManaged` at Agent start, so the gate
  moved inside the driver's `Resume`, with kiro's `ensureSettings` as the precedent and its
  `sync.Once`-and-swallow as the anti-pattern.

The rest added what the ADR had not decided at all: the whole `Capabilities` declaration
(Decision 13 — as written then, "the first kind with every dynamic axis true"; round 4 overturned
that framing and left `Permissions` as the only new capability), the approval judge's per-approval model call and the bundled
`resume-claude` / `resume-codex` / `import` / `migrate` skills as clamps 6 and 7, the single-use
session id (Decision 4 — AF's deterministic UUIDv5 cannot be reused), the missing cost and quota
sources (Decision 10), `session/start.config.mcpServers` as a possible second MCP route
(Decision 11), a fourth copy of the MCP kind list (`mcpsrv/mcp_server.go:70-74`) and a dozen more
sites for the checklist. The estimate rose again, to 20–31 days, because the table was still short
of the work.

One finding was declined: the `muse auth set` usage quoted in Decision 9 is verbatim from
`muse auth --help` (the reviewer read `muse auth set --help`, which prints a different usage line).
Both were checked; the text now says which command it quotes.

Not re-verified by this round: login, a real turn, token accounting, a subscription credential, a
real `bwrap`, an end-to-end MSP drive, and the release manifest — the same boundary as before.

## Review round 5 (2026-09-20, the same opus session) — the conclusion holds

Asked specifically whether anything overturns the conclusion, the answer was **no**: managed-only,
and deciding adoption on the three Phase 1 gates, both survived re-examination, and the residual
risk sits in gate B1 where a failure costs only this ADR and the probe. Round 4's fifteen findings
were confirmed closed. Twelve findings remain, one of them structural.

- **Decision 8 had the shipped variant's install path wrong, and the error broke its own shadow
  guard.** The lean boot-install puts the CLI in `~/.local` — the same directory the vendor's
  installer uses — so "the connection card reports a `~/.local/bin/muse` it finds" would have
  reported AF's own binary. The check is now version identity against `versions.json`, and the
  entrypoint's existing re-pin (`entrypoint.sh:336-352`) is the repair. The rejected kiro-shape
  alternative was re-argued from pinning rather than from location, since the location is now the
  same.
- **`Caps.PermissionChoice` was missing entirely** (zero occurrences before this round). Without it
  `POST /sessions` refuses `skip_permissions=false`, so Decision 5's "approvals stay on" could not
  even be requested at launch. It is `Caps`, not `Capabilities` — two structs, and Decision 13 had
  only decided one of them.
- Three gate holes, all cheap to close where they are: the **loaded** RSS Decision 3's own
  re-evaluation clause asks for; the **shipped** deployment path, which gate A's image build can
  exercise for free; and the **effect** of each clamp rather than only its key spelling.

Also corrected: the estimate now states both the table (22–33) and today's expectation (23–35), and
says plainly that sequencing ADR 0093 first is worth about a working week rather than repeating a
symmetry that does not exist. Three anchor slips (`fileSpecs`, `driver.go`, and the three
`mcp_stdio.go` lines that moved between `06ea94d3` and `73ac5cdc`), a stale sentence in Decision 6
that round 4 had only half-fixed, and an English paraphrase presented as a quotation of a Japanese
comment.

Independently re-measured this round and matching the text: the release manifest (anonymous, sha256
for all seven artefacts, 299.3 / 268.9 MiB) and — the one worth naming — **the drift lock actually
works**: the manifest's `msp_schema_fingerprint` equals the fingerprint `muse schema` writes
locally. Of every file this ADR cites, exactly one changed between `06ea94d3` and `73ac5cdc`.

## Review round 4 (2026-09-20, the same opus session)

Fifteen findings, and the three that matter most are all cases of this ADR reading the tree
optimistically.

- **The distributed image does not bake proprietary CLIs, and Decision 8 assumed it did.**
  `BAKE_AGENT_CLIS=0` is the Dockerfile default, `release.sh` makes lean the distribution default,
  and `NOTICE` tells the reader that proprietary agent CLIs are fetched at first start. Muse Code is
  proprietary, so the shipped shape is boot-install; Decision 8 now decides both variants, and
  `NOTICE` is a checklist row.
- **`DynamicMode` was wrong.** `ThreadSettings.Mode` is AF's plan mode (`"plan" | "normal"`,
  `agents/driver.go:37`), not the approval posture that `session/setApprovalMode` changes. One wire
  method had been read as two AF axes.
- **`Permissions: true` is not a wiring job.** `Interaction.Kind` is `"question"` today, with
  approval and plan marked future, and every managed kind declares `Permissions: false`. Declaring
  it builds AF's first approval interaction — which is also why the estimate moved.

The rest was mostly the ADR claiming firsts it had not earned: opencode already declares
`Steer` / `Fork` / `DynamicModel` / `DynamicEffort` / `DynamicMode` / `Questions`, so the only new
capability is `Permissions`; ADR 0093 is **half** landed (`internal/harness/` is on develop,
`session.KindLcpp` is not), which changes who pays for the approval work; `ClearResume` is dead
code and what actually drops a field on recreate is the whitelist rebuild of `session.Meta`; the
bundled-skill clamp was short by `daemon`, `host-manager`, `slack-connector`, `create-plugin`,
`read-session` and `doctor`, and its disable key is a pack-qualified path, so it belongs in the
drift check; and the "$0 harness" line needed the boundary that echo runs no tools and assembles no
rules.

One finding went the other way and is recorded because the reviewer volunteered it: round 3's
`muse auth set` complaint was withdrawn after re-measuring — the ADR's quotation was right.

Not re-verified: the same boundary as rounds 2 and 3, plus this round's capability claims are a
schema-to-type comparison rather than a running driver.

## Phase 1 gate A measurements (2026-09-20)

Gate A ran on `e627536f`. **Decision 5's waiver is confirmed permanent and Decision 8's shipped path
now runs end to end**, so both decisions' text is updated above rather than only annotated here.
Everything below was measured in **a real Workspace container** — amd64, Debian trixie, AppArmor
profile `docker-default (enforce)`, seccomp filter mode 2 — which is the environment the answer is
about. Nothing here needed a credential.

### A-1: bubblewrap cannot work here, and three premises were wrong about why

`bwrap` came from Debian trixie's own package (`bubblewrap_0.12.0-1~deb13u1_amd64.deb`,
sha256 `70aca4fa…`, `bubblewrap 0.12.0`) — the same package `workspace/Dockerfile` now bakes.

`bwrap --ro-bind / / --dev /dev true` exits 1 with `bwrap: Failed to make / slave: Permission
denied`, and so do `--unshare-user`, `--unshare-all` and the bare `--ro-bind / /`. Under strace the
picture is exact:

```
clone(CLONE_NEWNS|CLONE_NEWUSER|SIGCHLD)                          = 202910   <- succeeds
mount(NULL, "/", NULL, MS_REC|MS_SILENT|MS_SLAVE, NULL)           = -1 EACCES
```

**It fails earlier than this ADR said** — not at `move_mount` attaching a bind, but at the very first
call, making `/` rslave, before any mount is created. The syscall matrix says who is refusing.
Inside `unshare --user --map-root-user --mount` the process is uid 0 with
`CapEff: 000001ffffffffff` (all 41 capabilities, `CAP_SYS_ADMIN` included):

| syscall | outside a userns (uid 1000, `CapEff: 0`) | inside the userns (uid 0, all caps) |
|---|---|---|
| `mount(NULL, "/", …, MS_REC\|MS_SLAVE, …)` | **EACCES** | **EACCES** |
| `mount("none", "/tmp", "tmpfs", …)` | **EACCES** | **EACCES** |
| `fsopen("tmpfs", 0)` | EPERM | **OK** (fd 3) |
| `open_tree(AT_FDCWD, "/", OPEN_TREE_CLONE\|AT_RECURSIVE)` | EPERM | **OK** (fd 3) |
| `move_mount(…)` | EPERM | **EACCES** |

🔴 **The denier is the LSM, and it can now be named: AppArmor's `docker-default` profile.** The two
calls that merely *create a detached handle* go EPERM → success the moment we hold `CAP_SYS_ADMIN`,
which proves both that the capability layer is satisfied and that seccomp is not filtering the new
mount API. The two calls that *attach or alter a mount* return **EACCES regardless of capabilities** —
a pattern neither layer can produce: a kernel capability check fails with EPERM (as `fsopen` outside
the userns shows), and a seccomp `ERRNO` filter is capability-blind, so it could not have let
`fsopen` through only in the second column. AppArmor's mount mediation is what returns EACCES. You
can build a detached mount tree here; you can never attach it. Nothing shipped from this repository
can change that — only the container runtime's profile can, which is exactly the "host contract"
Decision 5 names.

Two premises in this ADR were wrong, and both cut the same way:

- 🔴 **"`bwrap` is absent from the image" was never the operative blocker.** Muse **carries its own
  embedded bubblewrap** — `bubblewrap built for TBH`, `__tbh_internal_bwrap`, `TBH_BWRAP_EXE`,
  `--tbh-bwrap-selection-v1` in the binary, and its internal dispatcher answers
  `tbh: invalid private Linux invocation markers` when invoked without the private markers. Its error
  text says so too: "no capability-valid system bwrap was found on PATH **and no usable embedded
  fallback is available**". Installing a system `bwrap` neither enables nor is required by muse's
  sandbox.
- 🔴 **Debian's `bwrap` could not satisfy muse even if mounts were permitted.** Muse requires
  `--perms`, `--ro-bind-data` **and `--ro-bind-symlink`** ("selected Bubblewrap lacks required
  --ro-bind-symlink support"). Trixie's 0.12.0 has the first two and answers
  `bwrap: Unknown option --ro-bind-symlink` to the third, so muse would reject it during its own
  availability probe.

Replaying muse's own probe argv (reconstructed from the binary: `--ro-bind / / --dev /dev --bind
<probe> <probe> --proc /proc --unshare-pid --unshare-net --new-session --die-with-parent --chdir
<probe> /bin/sh -c 'printf ok > "$1" && printf no > "$2"'`) fails at the same first call.

So gate A's expected answer holds, by three independent routes rather than one. **`bwrap` is baked
anyway** (`workspace/Dockerfile`) so the day the host's LSM policy changes the re-measurement is one
command, and the Dockerfile comment says plainly that it is there for that and not to enable a
sandbox.

⚠️ **A trap for whoever re-measures: do not answer this question from CI.**
`deploy/local/e2e-smoke.sh` runs the image with `--cap-add=SYS_ADMIN` on a runner that does not apply
`docker-default`. A `bwrap` that works there says nothing about a Workspace, and would look like
Decision 5 being overturned.

**Not reproduced, and deferred to gate B1**: the vendor's "every sandboxed shell command aborts as an
environment failure". It needs a shell tool call, and the free path cannot produce one —
`--provider echo` never emits a tool call (the run completes with `echo: <the prompt>` as its whole
output), and `muse sandbox` turns out to be `windows check|setup` **only**, so there is no Linux
preflight to ask. The mechanism above makes the claim very likely; it is still unmeasured.

One thing the echo run did show, and it belongs to Decision 10 rather than here: a single echo turn
scheduled and started **two background observer agents** (`reminder.agent.skill-reminder`,
`reminder.agent.verify-reminder`); the second ended `invalid run configuration: provider does not
support base instructions` only because the echo provider has no instructions to assemble. With a
real provider those are model calls Agent Fleet does not see.

### A-2: the shipped deployment path, run end to end

Both manifests answer **anonymously, HTTP 200**: the channel (254 B) names `1.3.0-R3401.1`, and the
version-addressed release manifest (1,852 B) carries `artifacts.x86_linux` = checksum `71b089d0…` /
size **313,800,920 B** and `artifacts.aarch64_linux` = `5e5ea2a3…` / **281,942,104 B** (the 299 MiB
and 269 MiB this ADR quotes). Its `msp_schema_fingerprint` is `sha256:7469c9e3…` — the same
fingerprint the Phase 0 probe got from `schema generate-json-schema` offline.

**The artifact is the binary, not the launcher.** Its checksum equals the sha256 of the
`muse-bin-<version>` file the vendor installer leaves on disk, and it runs standalone
(`Muse Code 1.3.0 (1.3.0-R3401.1)`). The vendor's `~/.local/bin/muse` is a *bash launcher* that polls
the channel hourly and rewrites itself. So AF installs **the binary under that name**, and the
self-update path simply does not exist for AF's own copy; `MUSE_NO_AUTO_UPDATE=1` is there for a
vendor launcher that shadows it.

Measured with the real `entrypoint.sh` block, the real CDN and a throwaway `HOME`:

| Check | Result |
|---|---|
| Default (no opt-in) | Silent no-op, 4 ms |
| Boot-install, empty home | **19 s** for 313,800,920 B; `[entrypoint] boot-install muse 1.3.0-R3401.1` |
| sha256 of the installed file | `71b089d0…` = the manifest value |
| `muse --version` vs the pin | `Muse Code 1.3.0 (1.3.0-R3401.1)` — build id matches `versions.json` |
| Second start | `boot-install: muse already present (skip)` |
| Repin, shadow at a drifted version | Detected and replaced with the pinned build |
| Repin, shadow at the **same** version | Left alone — the check is version identity and is blind to the path |

The last two rows are Decision 8's shadow rule working in both directions, which is the point: a
path check would have reported AF's own binary. Note the version string is
`Muse Code 1.3.0 (1.3.0-R3401.1)`, so the comparison must take the parenthesised build id — the
`tr -dc '0-9.'` idiom the agy block uses would drop `-R3401.1` and mismatch forever.

🔴 **The boot-install's sha256 verification was decorative — at all five call sites.** The pattern is

```sh
( set -e
  echo "${sha}  artifact" | sha256sum -c - >/dev/null
  install -D -m 0755 artifact "$HOME/.local/bin/…"
) && echo "boot-install ok" || echo "WARN: failed"
```

and POSIX says `-e` is ignored for "any command of an AND-OR list other than the last". The subshell
*is* that left operand, so **the `set -e` written inside it does nothing**. Measured on bash 5.2.37
and on trixie's dash: with a checksum deliberately changed by one character, the run printed
`WARNING: 1 computed checksum did NOT match`, **installed the artifact anyway, and reported success**.
It applied to rtk, agy, cursor, rtk's self-update and muse. Fixed by making the check explicit
(`|| exit 1`, which is unaffected by errexit) rather than by restructuring — ⚠️ moving the subshell
into `if ( set -e; … ); then` does **not** help, because an `if` condition is another context where
`-e` is ignored. After the fix the negative control reports WARN and installs nothing, and the
positive control still installs.

🔴 **Decision 8's "boot-installs at container start" is changed to an explicit opt-in**
(`AF_MUSE_BOOT_INSTALL=1`, default off). At 299 MiB and with no `kind=muse` before Phase 2, an
unconditional boot-install would make every fresh container in the fleet download a CLI nobody can
use — which is the same reasoning that already moved kiro (855 MiB) off unconditional boot-install
and onto a per-user on-demand install. Phase 2 should take kiro's route
(`workspace-agent install-muse`) rather than turn this flag on.

### The baked image

`dev-image.yml` baked `workspace` from this branch (amd64) and the published image carries all three
changes: `/usr/bin/bwrap` (80,248 B, byte-identical in size to the extracted package),
`MUSE_NO_AUTO_UPDATE=1` in the image config, and `versions.json` with `muse=1.3.0-R3401.1` and
`muse_sha256=71b089d0…` (29 keys). Verified with `crane config` / `crane export` rather than
`docker run`, since a Workspace has no Docker.

⚠️ **Unrelated breakage found on the way, and it will bite develop next.** The first bake failed at
`CHROMIUM_VERSION=153.0.8010.47-2~deb13u1`: `E: Version … was not found`. The `.deb` is still in the
security pool, but Debian's *index* now lists only `153.0.8010.52-1~deb13u1`, which landed between
develop's last green bake (05:38) and this one (06:30). Nothing to do with this ADR — the bake above
used `bake_optional_tools=false` to step around it — but the chromium pin (and its `chromium_cft` /
`chromium_dl` partners) needs its own bump.

### What gate A did not do

No login, no real turn, no subscription, no `-w`, and no `kind` wiring — all Phase 2 or gate B1.
Muse was run only with `--provider echo` against a throwaway repository.

## Phase 1 gate B1 / B2 measurements (2026-09-20)

Gate B1 ran on `11099efd` against **Muse Code 1.3.0-R3401.1** with a real Muse Code
subscription (tier `27681…`, Everyday Usage) signed in with **`muse login`'s device code** — see
B1-0. Everything below is `muse serve` over stdio with `modelId: muse-spark-1.3` passed
explicitly on every `session/start`; `-w` was never passed, `muse auth set` was never run and
`META_API_KEY` was never set (either would override the account login and drop the subscription
onto metered billing).

**How the runs were isolated, because it decides what the numbers mean.** `HOME` was a throwaway
directory carrying *fake* `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md` markers, so the
foreign-personal-context measurement could never ship the member's real files to Meta;
`XDG_DATA_HOME` was throwaway, so no probe session entered the member's store; `XDG_CONFIG_HOME`
stayed the real one, because a copied or symlinked `auth.json` risks rotating the member's
refresh token. The workspace was a throwaway git repository, never this one.

**Cost: 10 subscription prompts** (plus two free runs — the echo provider and a turn that failed
before any model call). The 5-hour window moved **0 % → 6 %** and the weekly block 0 % → 2 %, so
on this tier the window is worth far more than the documented "10–50 prompts per 5 hours"; the
percentages are integers and the unit is not stated, so this is a floor, not a conversion.

### B1-0: the credential is a device code, not a browser handoff — gate B2 answers "yes"

`muse login` prints a URL and an eight-character code (`https://auth.meta.com/oauth/device/?code=XXXX-XXXX`)
and polls; no TTY, no local callback, no browser in this container. That is the **same start →
poll shape cursor and kiro's connection cards already implement**, and it produced a working
subscription session: `turn/completed` with `terminal: "completed"` on the managed route.

So **gate B2 closes "yes"** and Decision 9 is corrected rather than annotated: the credential is
an account login by device code, the API key is the *fallback*, and the ADR's earlier "a
subscription needs browser onboarding this container does not have" was wrong. The deny-list and
the "one input" connection card are unchanged except that the input is a code the member pastes
into the browser they already have, not a secret they paste into AF.

### B1-1: the accounting matrix — `MeasuredPartial`, and the reason is not the cache

`session/tokenUsage` fires **once per model completion**, not once per turn, and carries both the
raw provider counters and the server-derived counted-once numbers:

| Property | Measured |
|---|---|
| Cache | **Separated and inside `inputTokens`**: a second call reported `inputTokens 21857`, `cachedTokens`/`cacheReadTokens` `20721`, `cacheWriteTokens 0`, and `promptTokens 21857`. So `promptTokens == inputTokens` for this provider and cache is a *subset* — adding them double-counts |
| Per-turn vs cumulative | Both, in one event. A two-call turn emitted `promptTokens` 20776 then 21857, and the second event's `cumulative.promptTokens` was **42633 = 20776 + 21857**. Fold the per-event values **or** take the last `cumulative`, never both |
| Context vs spend | `session/contextUsage.usedTokens` was **22252** on the same event whose `cumulative.totalTokens` was **44007** — occupancy is the last call, not the running total. A ledger that feeds the context chip from cumulative reads 2× |
| A failed turn | `turn/completed` with `terminal: "failed"`, `error{kind: "modelError", message, retryable: false}` and no `session/tokenUsage` at all, and `usage/read` stayed `{}`. **A failed turn contributes nothing** |
| An interrupted turn | `turn/interrupt` → `turn/completed` with `terminal: "cancelled"`, `reason: "cancelled after tool result reconciliation"`, and exactly the one `session/tokenUsage` for the call already made. **No double count, nothing lost** — but the prompt was spent |
| After `session/resume` | Resumed in a **fresh host process**: `session/resume` replays history items and **zero `session/tokenUsage` events** (the only notification was `session/branchChanged`), and the next turn's `cumulative.promptTokens` was 41236 = 20764 (before the restart) + 20472. **Cumulative survives a restart and does not re-count** |
| Subagents | 🔴 **Excluded from the wire numbers.** See below |
| Background observers | 🔴 **Excluded from the wire numbers.** See below |

🔴 **The trap is not cumulative-vs-delta; it is ownership.** A turn that spawned one subagent
(`muse.subagent_spawn`) emitted **four** `session/tokenUsage` events summing to
`cumulative.promptTokens 88,077` — while the durable log for the same turn records **six** model
calls: four owned by `main-root` and two owned by `subagent-1`
(`owner_type: "native_child"`, `input_tokens` 14,294 and 14,445). The child's **28,739 prompt
tokens never appear on the wire**, so folding the notifications alone under-reports that turn by
**24 %** (88,077 of 116,816). The schema says so in one line —
"Subagent/workflow-child usage is never folded in — it rides the owning items" — and the
measurement is what makes it actionable. The same holds for the four background observers: a
turn with them enabled emitted **one** `session/tokenUsage` and three `reminderChild` items whose
model calls are nowhere in it.

The at-rest log *does* carry the attribution: every model completion is preceded by a
`goal_usage_attribution` record with
`owner{owner_id, owner_type: main_root | native_child, requester_kind}` and a `quantity` block
(⚠️ each is emitted twice, once with `reported: false` and zeros — a naive fold must skip those).

**So Decision 10 lands `MeasuredPartial`, and that is now a measured verdict rather than a
caution.** Two routes to exact exist, and both are Phase 2 decisions: fold
`goal_usage_attribution` out of the session JSONL instead of the notifications, or rely on
Decision 6's clamps (subagents and observers off) and accept that "exact" then means "exact for a
clamped host" — which the clamps do enforce, but a member who is later allowed to raise
`agents.execution_capacity` silently turns the ledger partial again. The honest v1 is
`MeasuredPartial` with the clamps on.

Two more numbers Decision 10 needs: `cost` is **`null` on all four models** in the authenticated
catalogue, so there is still no price per token from the vendor; and `usage/read` answers `{}`
until this host has observed a completion — the subscription window is **not** a query to Meta
but the last frame this process saw. With one `muse serve` per session (Decision 3), **a
freshly-launched session has no quota chip until its first turn completes**, and then
`usage/changed` pushes `{tier, weekly{resetsAtMs, usedPercent}, window{resetsAtMs, usedPercent,
windowDurationMins: 300}}` unprompted.

### B1-2: approvals arrive as a notification, not as the request the schema also declares

The round trip works and is cheap to wire, but the first draft of a driver will hang on it. MSP
declares `approval/request` as a *server-initiated request* — and what the host actually sent was
the **`approval/requested` notification**. A client that answers only the request form stalls the
turn forever (measured: the turn sat at `attention: ["approvalPending"]` until the host was
killed).

```
session/statusChanged  {"status":"running","attention":["approvalPending"]}
approval/requested     {approvalId, currentRequirementId{approvalId,sourceIndex}, toolName:"bash",
                        judgeEscalated:false, protectedWrite:false, rawArgs:"{\"command\":…}",
                        subject:{kind:"shell", command:"…", stages:[{argv:["echo","…"],
                                 argvComplete:false, position:1, totalStages:1,
                                 resolution:{kind:"unresolved"}}]},
                        availableChoices:[{choiceId:"allow_once",decision:"approved",scope:"once"},
                                          {choiceId:"abort",decision:"abort",scope:"once",
                                           acceptsFeedback:true}]}
approval/decide        → {status:"accepted", terminal:true}   then approval/updated, approval/resolved
```

Three things for Decision 13. `attention: ["approvalPending"]` is a **first-class status signal**,
so AF's "waiting for permission" state needs no inference. `availableChoices` in `onRequest` mode
is exactly **two** — allow once, or abort with optional feedback — so the first permission card
needs no scope selector and the ADR's open question 5 is answered: `subject.command` plus the
`stages[]` argv is all a card has to render. And `requirementId` must be echoed back verbatim;
it is the multi-stage race guard.

`userInput` is a genuinely separate channel and maps onto AF's existing question interaction
field for field:

```
userInput/requested  {userInputId, toolName, questions:[{id:"color_pref", header:"Color",
                      question:"Which colour do you prefer?", selection:{mode:"single"},
                      options:[{label:"Red (Recommended)",description:"…"},{label:"Blue",…}]}]}
userInput/answer     {userInputId, answers:[{questionId, selectedLabel}], commandId, sessionId}
                     → accepted, then userInput/settled
```

⚠️ Both channels **re-deliver**: the same `userInput/requested` arrived twice, and answering the
second time returned `-32056 userInputAlreadySettled` with `settlement.outcome: "answered"`. AF's
handler must be idempotent and treat that error as success, not as a failure to report.

### B1-3: the clamps — five spellings confirmed, four effects measured, and two silent failure modes

The keys are in `~/.config/muse/settings.json` and the spellings are no longer guesses:

| Clamp | Key, measured | Effect, measured |
|---|---|---|
| Subagent fan-out cap | `agents.execution_capacity` (integer ≥ 1; `0` is rejected) | Spelling proven by `muse serve` refusing to start on any *other* member of `agents`; the cap's effect on concurrent children is **not** measured |
| Subagents off entirely | `run.subagent_delegation_mode: "off"` | ◎ **all six `muse.subagent_*` tools disappear** from the model's tool list (28 → 21 tools, same prompt, same session shape) |
| Workflows off | `run.workflow_trigger_mode: "off"` | ◎ `muse.workflow` leaves `toolset.active_tools` (23 → 22), measured **for free on `--provider echo`**, whose durable log records the assembled toolset |
| Foreign personal context off | **two keys, not one**: `context.foreign_personal_rules: false` **and** `context.foreign_personal_skills: false` | ◎ both directions. With them unset, a real turn answered with the content of the fake `~/.claude/CLAUDE.md` — **the member's Claude Code rules reach Meta**. With `foreign_personal_rules: false` the same prompt answered with the project file only, and the syscall trace shows `$HOME/.claude` and `$HOME/.codex` are no longer opened. `foreign_personal_skills: false` alone drops the two foreign skills from `muse skills list` and leaves the rules |
| Bundled foreign-reader skills off | `skills.activation.bundled["bundled://muse-core/skills/<id>/SKILL.md"]: "off"` | ◎ (Phase 0) `muse skills list` reports `off`. Still keyed by a pack-qualified path, so still a drift-check item |
| Background observers off | ⚠️ **not a settings key we could find**: `run.reminder_observers` is a real field of the runtime's `RunConfigurationSettings` but the enterprise validator rejects it, and no settings.json spelling bit. What did work is the env route — `MUSE_EXPERIMENTAL_{SKILL,GOAL,VERIFY,TODO,MEMORY,SCOPE}_REMINDER=0` | ◎ and it is worth more than spend: with observers on, a one-line answer turn ran **17.9 s** — 6.3 s to the answer, then an `eot_gate_ms: 11518` end-of-turn gate while three `reminderChild` agents (`skill-reminder`, `goal-reminder`, `verify-reminder`) made their own model calls. With them off the same prompt completed in **4.5 s**. Turn latency, not just invisible quota |
| Approval judge off | ⚠️ unmeasured. `--approval-judge` exists on `muse` and `muse exec` but **not on `muse serve`**, the same shape as `--no-foreign-personal-context`; `MUSE_DISABLE_APPROVAL_JUDGE` is in the binary. In the one approval we took, `judgeEscalated` was `false` | — |
| **8th clamp — pin a non-contributor model** | `session/start.modelId` per session (there is no settings key for it) | ◎ honoured for every model call, including after a resume that passed no `modelId` (`session/tokenUsage.modelId: "muse-spark-1.3"`, durable `run_model` record `source: "startup"`). 🔴 **But the session's stored metadata and every `session` projection say `muse-spark-1.3-contributor`** — the host default — in `session/listChanged` and in `session/resume`'s `session.modelId`, in all five sessions measured. A model chip fed from the session projection would tell the member their code is going to product improvement when it is not, and the inverse mistake is the dangerous one |

🔴 **Two silent failure modes, and they pull in opposite directions.** A misspelling *inside* a
section muse parses strictly kills the host: `{"agents":{"zzz":1}}` makes `muse serve` exit
**rc=3** with `load settings for serve composition: malformed settings file … unknown field
`zzz`, expected `execution_capacity`` before `initialize` — so AF's driver must treat a settings
write as something that can make the child refuse to boot, and surface it as a connection error
rather than a hang. A misspelling *anywhere else* is **completely silent**: an unknown top-level
section, or `context.foreign_personal_skil`, starts the host happily with the clamp simply not in
effect (measured, with the correct spelling as the positive control in the same table). There is
no diagnostic between those two behaviours, which is exactly why Decision 6 puts a fail-close in
front of the write — and why the clamp set needs a **behavioural** test per key (the free
`--provider echo` toolset oracle covers two of them), not a "we wrote the JSON" test.

**The settings file's lock protocol, by syscall** (`config/src/settings.rs:140` per muse's own
log line):

```
open(".settings.json.lock", O_RDWR|O_CREAT, 0666)   ← sidecar, never the target
flock(fd, LOCK_EX)                                  ← advisory BSD flock, blocking, no LOCK_NB
lstat("settings.json"); open(".settings.json.tmp-<pid>-0", O_WRONLY|O_CREAT|O_EXCL)
write; fchmod 0644; fsync; rename(tmp, "settings.json"); fsync(dirfd)
flock(fd, LOCK_UN)
```

AF takes the same protocol: `flock(LOCK_EX)` on `.settings.json.lock`, write a temp file in the
same directory, `rename`, fsync the directory. Two details the implementation must not miss.
**muse reads `settings.json` *before* taking the lock** (measured: the read is 11 syscalls ahead
of the `flock`), so its own update is a read-merge-write whose read is unprotected — the file is
not safe against a concurrent writer even when both sides use the lock, and AF must therefore
re-read and verify after writing rather than assume its merge survived. And **muse blocks
indefinitely** on a held lock (negative control: holding `LOCK_EX` from another process stalled
`muse skills enable` for 8 s and it proceeded the moment the lock was released), so AF holding
the lock across anything slow hangs every `muse` command the member types.

**There is a second, better-shaped route that Phase 2 should cost out: the enterprise
configuration planes.** `muse config status` resolves two system files — measured by syscall,
`/etc/muse/enterprise-defaults.json` and `/etc/muse/enterprise-policy.json`, opened through a
hardened `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)` — and
`muse config validate --plane defaults|policy` is a **free, offline spelling oracle** that names
the exact failing member (`unknown_member location=settings.agents.execution_capacity`,
`semantic_invalid`, `wrong_type`). Five of the eight clamps validate in the defaults plane
(`agents.execution_capacity`, `run.workflow_trigger_mode`, `run.subagent_delegation_mode`,
`context.foreign_personal_rules`, `context.foreign_personal_skills`,
`skills.activation.bundled.<id>`), each reported `binds=defaults user_overridable=true`. Inside a
Workspace AF cannot write `/etc`, so this is an **image** decision, not a runtime one: baking the
clamps into the image would remove the per-user settings writer, the lock, the lost-update
window and the fail-close from Decision 6 altogether — several days of the estimate. Three
caveats keep it out of this ADR's decision: the planes sit behind
`MUSE_EXPERIMENTAL_ENTERPRISE_CONFIG`, the `policy` plane (the non-overridable one) admits only
`{settings.capability_ceilings, privacy, model_egress}` and its `privacy.foreign_personal_rules`
value vocabulary is not discoverable from outside (bool and fifteen plausible strings all
rejected), and a member could still override a `defaults`-plane value in their own settings.

### B1-4: `session/start.config.mcpServers` works — Decision 11's second route is the one to take

Measured with a fake stdio MCP server that logs its own argv, environment and handshake. Passing
it only on the wire, with `capabilities.requestedCapabilities: ["sessionMcp"]` (granted;
`MUSE_ENABLE_SESSION_MCP` was **not** set), the model's tool list came back containing
**`mcp__afprobe.af_probe_ping`**, and the server's log shows muse connected as
`clientInfo {"name":"tbh","version":"0.1.0"}` with MCP protocol `2025-06-18`, passing
`MUSE_SESSION_ID` and the config's `env` additions. `mode: "optional"` was accepted on the wire.

⚠️ **The server starts at the first turn, not at `session/start`** — three separate
`session/start`-only runs spawned nothing at all, which is what made this look unsupported at
first. Anything that health-checks MCP at session creation will read "not connected" forever.

So Decision 11 is decided the cheap way: **AF passes MCP servers per session on the wire and
writes no `mcp_servers` block into the shared settings file.** The settings writer then carries
clamps only, the lost-update surface shrinks to the clamps, and per-session server sets — which
the file route cannot express at all — become possible. The `mcp_servers` file route stays
documented as what a member's own configuration may contain, and AF preserves it.

### B1-5: the instruction layers — one user-scope rules file, and the fleet route already exists

Measured by planting distinct markers and asking the model which ones it can see, with the
syscall trace as the second witness:

- **User scope: `~/.config/muse/AGENTS.md`** (i.e. `$XDG_CONFIG_HOME/muse/AGENTS.md`). Planted
  there, its content came back in the model's answer. `~/.config/muse/CLAUDE.md` is probed too,
  so the same "AGENTS.md wins" precedence as the project layer is likely, and is untested.
  Nothing under `$museHome` (`~/.local/share/muse/AGENTS.md`) was read.
- **Project scope** confirmed again end to end: with `--trust-workspace`, the repository's
  `AGENTS.md` reached the model and `CLAUDE.md` did not.
- **The fleet route needs no new mechanism.** A hand-dropped
  `~/.config/muse/skills/<name>/SKILL.md` is listed by `muse skills list --source user` with no
  install step and no lock-file entry, so AF's existing `fleetskills.Apply(dir, topics)` —
  already used for claude, codex and opencode (`agent_instructions.go:135-141`) — works on muse
  unchanged.
- 🔴 **But muse has exactly one user-scope rules file, and AF's two apply paths want two
  artefacts.** Every other kind gets a directory (kiro's `~/.kiro/steering/agent-fleet-{guide,user}.md`)
  or two distinct files. For muse, `ApplyFleetNotes` and `ApplyUserInstructions` must **share
  `AGENTS.md`**, which means AF owns that file outright (delimited sections, fleet policy first)
  and a member's own text in it is at risk. That is a decision Phase 2 has to make explicitly —
  merge with markers, or own the file and say so in the guide — and it is the one place where
  muse's instruction layer is *more* awkward than kiro's, not less.
- ⚠️ One scope note for the guide: `foreign_personal_*` governs the **personal** layer only.
  With the clamps on and the workspace trusted, muse still probes the *repository's* own
  `.claude/CLAUDE.md`, `.claude/skills/`, `.codex/skills/` and `.claude-plugin/plugin.json`. In
  this repository that is real content, read as project context.

### B1-6: the loaded footprint, and what a `-w`-less run writes

**Decision 3's re-evaluation clause is satisfied and the answer is "keep the child per session".**
Peak RSS of the host plus its children, sampled every 0.5 s through each turn: **137–147 MiB**
(the maximum, 147,372 KiB, was the turn that spawned a subagent), against **73 MiB idle**. Ten
sessions of that shape is ~1.4 GiB, the same order as five claude panes at the registry's
230 MiB — affordable, and still a third of what a shared daemon would save.

**Decision 7 no longer rests on the `-w` measurement alone.** Six real turns across five sessions
— a shell tool call, a subagent that wrote a file, an interrupt, a resume — with `-w` never
passed, wrote **nothing** into the working copy beyond the files the agent was asked to create:
no `.muse/`, no `.muse/worktrees/`, no `.muse/.session-worktree-reservations/`,
`.git/info/exclude` byte-identical to git's own default, and `git worktree list` still one entry.
The shared-state hazard is specific to `-w`, and not passing it is sufficient.

### B1-7: the sandbox, with the sandbox on (gate A's deferred item)

One turn on a host started **without** `--disable-sandbox`, in a container with no system
`bwrap` at all (so this is muse's *embedded* bubblewrap, the one gate A found in the binary):

```
item/completed  {kind:"toolCall", tool:"bash", status:"failed",
                 failureReason:"process exited with status 1",
                 visibleOutput:"bwrap: Failed to make / slave: Permission denied"}
turn/completed  {terminal:"completed"}          ← the TURN succeeds
```

The vendor's wording is "every sandboxed shell command aborts as an environment failure"; what
actually happens is narrower and, for AF, worse: **the tool call fails and the turn completes
normally.** The agent explained the failure in prose and stopped. So a Muse session launched
without `--disable-sandbox` would look healthy in the Console — running turns, producing answers
— while every shell command it tries fails with `Failed to make / slave`. That is the same first
`mount(NULL, "/", …, MS_REC|MS_SLAVE)` EACCES gate A traced, reached from the opposite
direction and without any system `bwrap` involved, which independently confirms A-1's embedded-bwrap
finding. Decision 5 is unchanged; what this adds is that the failure is silent at the turn level,
so **the driver should assert the flag at spawn rather than trust it**.

### What gate B1 did not measure

The effect of `agents.execution_capacity` on concurrent children; the approval judge's own model
call (no settings or `serve` route found, and `judgeEscalated` was false in the one approval
taken); whether `sessionMcp` is *required* for the wire MCP route (it was requested and granted
in every run); `session/fork`, `turn/steer`, `session/setModel`, `session/setReasoningEffort` and
`session/list` cross-session visibility; the `~/.config/muse/CLAUDE.md` precedence; a
pay-as-you-go account's `usage/read`; and `muse`'s own `cron_*` tools, which stay in the tool
list under every clamp measured — an agent that can schedule its own future runs, beside Agent
Fleet's own scheduler, is a **ninth clamp candidate with no known key**.

## Gate A's artefacts: what earns its place in the trunk, and what comes out

Gate A landed five changes on `develop` for a kind that does not exist. Re-examined one at a
time, with the measurement that decides each:

1. 🔴 **`bubblewrap` in `workspace/Dockerfile` — removed.** The stated reason was "so the day the
   host's LSM policy changes, the re-measurement is one command". It does not survive contact
   with gate A's own findings: the blockers are *three*, and two of them are inside muse (it
   embeds its own bubblewrap; Debian's 0.12.0 lacks the `--ro-bind-symlink` muse requires), so a
   system `bwrap` is not the thing that would be re-measured. The re-measurement is already
   one command without it — gate A itself did it with `dpkg-deb -x`, no root — and B1-7 has now
   reproduced the denial *through muse's embedded copy*, with no system `bwrap` present, which is
   the configuration the fleet actually ships. Against that, every Workspace image carries a
   package forever; muse's own probe looks for a capability-valid system `bwrap` **on PATH**
   first, so baking one changes which rejection muse produces; and `deploy/local/e2e-smoke.sh`
   runs the image with `--cap-add=SYS_ADMIN` and no AppArmor profile, where that `bwrap` can go
   *green* and read as Decision 5 being overturned. A package whose only effect on the fleet is
   to make one CI job misleading is not worth an image slot. The knowledge stays in this ADR,
   where the next reader will actually find it.
2. 🔴 **`ARG MUSE_VERSION` + both sha256 + the `versions.json` `muse` / `muse_sha256` keys —
   removed**, together with the entrypoint's `AF_MUSE_BOOT_INSTALL` block (item 3 below) they
   only exist for. An unowned pin is the worst of both worlds: `deploy/local/cli-drift-check.sh`
   does not know about `muse`, so nothing tells us when 1.4 ships, and the pin is stale by
   construction; while a Phase 2 that follows kiro's per-user on-demand route — which gate A
   itself recommends — will not use this shape. What the code carried and the ADR did not is now
   written down instead: the artifact URL is
   `https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=<ver>&file=<asset>`
   with `<asset>` ∈ `muse-x86-linux` | `muse-aarch64-linux`, the sha256 is
   `artifacts.{x86_linux,aarch64_linux}.checksum` from the version-addressed release manifest,
   and the installed file is the binary itself under the name `muse`.
3. **The `e2e-smoke.sh` muse rows — removed with them, and the handoff's worry about them was
   wrong.** Those assertions compare the Dockerfile `ARG` with the baked `versions.json`: two
   places in our own tree, no network, so an upstream manifest move could never have turned them
   red. They are not a hazard; they are simply the test of a pin that is leaving.
4. **`ENV MUSE_NO_AUTO_UPDATE=1` — kept.** It is one line, it costs nothing, and its benefit is
   present-tense and independent of the kind: any member who runs the vendor's one-line installer
   today lands a bash launcher in `~/.local/bin/muse` that rewrites itself hourly
   (`MUSE_UPDATE_INTERVAL_SECONDS=3600`, measured). Suppressing that is worth having whether or
   not `kind=muse` ever exists.
5. **The `entrypoint.sh` `set -e` fix — kept, and out of scope for this review.** It is a real
   defect fix unrelated to muse: in `( set -e … ) && ok || WARN` the subshell is the left operand
   of an AND-OR list, where POSIX says `-e` is ignored, so the sha256 check at five boot-install
   sites installed unverified artifacts and reported success.

**On the shape of the gate itself.** The parent's self-assessment — that what paid off was
*running the thing we would ship* and what did not was *re-confirming what we had already
measured* — is right, and B1 repeats the pattern in both directions. The expensive-and-worthless
item here would have been the sandbox reproduction (B1-7) if it had gone the way the ADR
predicted: the ADR had already written the expected answer, and a confirmation would have bought
nothing. It earned its prompt only because the answer was **not** the predicted one — the turn
completes, so the failure is invisible at the level AF reports on. That is the rule worth
extracting, and it is not "don't re-measure": a check earns its place when *both* outcomes change
something. The clamp measurements are the clean example — "the key writes but does not bite" and
"the key kills the host" are both real, both were found, and neither was predicted. Baking
`bubblewrap` is the counter-example, because no outcome of that build changed a decision.

## Is Phase 2 decidable now?

**Yes, for everything except the price.** Gate A answered the sandbox permanently, gate B1
answered the accounting, approvals, user input, the instruction targets, the clamps, the MCP
route, the loaded footprint and the `-w`-less write surface, and gate B2 closes "yes" (device
code). Nothing measured overturns the conclusion: managed-only over MSP, `per-session-child`,
clamps in `settings.json`, `--disable-sandbox`, `--trust-workspace`.

What moved in the estimate, net roughly zero with the risk redistributed:

| Change | Days |
|---|---|
| MCP dialect drops out of the settings writer (wire route measured working, B1-4) | −0.5 |
| Settings writer is smaller but the lock is now specified, plus a behavioural test per clamp key (B1-3) | +0.5 |
| Approval + userInput wire shapes are known, including the notification delivery and `-32056` idempotency; AF's question interaction maps field-for-field | −0.5 |
| Usage: `MeasuredPartial` v1 is cheaper than `MeasuredExact`, but the model-id projection trap and the empty-until-first-turn quota chip are new Console work | +0.5 |
| Instructions: the fleetskills route works unchanged; the shared `AGENTS.md` ownership decision is new | ±0 |
| Deployment: whatever Phase 2 builds is now unwritten again (gate A's opt-in comes out) | +0.5 |

So the table stays **22–33 session-days**, **23–35 expected today** with the managed-only gate
unpaid, and **19–28 if ADR 0093 lands first** — and that recommendation is unchanged and now
better supported, because every 0093-shared item (the managed-only gate, the approval
interaction) was confirmed to be exactly the work muse needs.

**Two things a human, not a measurement, had to decide before Phase 2 starts; one is now settled.**
The clamps carry a privacy edge: the default model is `muse-spark-1.3-contributor`, whose own
description says "Your content, including inter-session messages, may be used for product
improvement". 🟢 **Settled 2026-09-20: the member chooses, in Settings → Agents, with the
non-contributor model as the default** (Decision 6, clamp 8) — AF neither hides the choice nor
makes it silently, and the guide explains what picking a contributor model means. What remains is
the spend: on a subscription the invisible quota is observers and subagents, which the clamps close;
on a metered account there is still **no price per token from the vendor** (`cost: null`), so the
cost chip cannot ship in v1 and the guide has to say why.

## Phase 2 implementation record (2026-09-21)

Phase 2 started on 2026-09-21, in the order of the work-package table. This section carries what
the implementation measured that the decisions above did not know, one entry per landing.

### P2-1: the MSP client, generated types and the drift lock (`workspace/agent/internal/msp/`)

The first work package. 234 types, 47 methods, 31 notifications and 31 error codes are rendered
from the vendor's own offline export by `internal/msp/schemagen`, so the wire vocabulary is not
hand-kept. The drift lock the Consequences section promised is three checks, and only the third
needs a binary: the generated file matches the checked-in bundle, the bundle's fingerprint matches
the constant, and — with a binary present — the installed binary exports the same fingerprint.
Each has a negative control, because a check that never ran and a check that passed look the same.

Three measurements corrected or extended the decisions.

**1. `--provider echo` does not reach `muse serve`, so "≈ $0 to build" stops at the turn.** The
watershed table's harness row, and the Consequences bullet that qualifies it, both rest on
`--provider echo`. Measured on 1.3.0-R3401.1: `--provider <MODE>` is an **`exec` startup flag**
and `muse serve --help` has no equivalent — its only posture flags are the sandbox ones,
`--trust-workspace` and `--no-session-log`. Over the wire, `session/start` *accepts*
`providerId: "echo"` and `modelId: "echo"` and records both on the session it returns
(`"providerId": "echo"`, `"modelId": "echo"`), and the turn then ends
`turn/completed.error.kind = "authRequired"`, `"not logged in: run /login to add an API key"`.

So the credential-free surface is real but narrower than the row claims: `initialize`, the
capability grant, `session/start` and the transcript path are free; **every turn costs a
credential**, and Decision 2 makes `serve` the only process AF runs. The consequence is a test
double rather than a caveat — `internal/msp/msptest` is an in-process host over pipes, which is
also what lets the suite run in CI, where the proprietary binary will never be. The live tests
stay behind `MUSE_LIVE=1` and stop short of a turn.

**2. The host emits a notification the stable surface does not declare: `session/started`.** The
bundle declares 31 notifications and this is not one of them; the string occurs exactly once in
240 KB of schema, inside `session/listChanged`'s own description ("row birth stays on
`session/started`, unload pairs with `session/closed`"). Measured, a `session/start` over `serve`
emits `session/started` *before* its response. The generated notification table is therefore a
decode map, **not an allow-list**: the dispatcher logs and drops an undeclared method instead of
treating it as a protocol error, and the driver must not assume the 31 are all it will see.

**3. The host validates UUIDv7 strictly, which turns Decision 4 into a test.** A `commandId`
carrying a v4 is refused `-32602 invalidParams`, `"invalid session/start commandId: expected
UUIDv7"`. AF mints its own (`msp.NewCommandID`, RFC 9562 v7, no new dependency), and the live
suite asserts both directions: the host takes ours verbatim on `session/start.sessionId`, and it
refuses a v4 — without the second, the first would pass against a host that validated nothing.

### The estimate after 0093, corrected: 22–33 days, not 19–28

ADR 0093 reached *adopted* on 2026-09-21, and the projection above assumed that lands two things.
It landed one.

- **The managed-only gate is genuinely free.** 0093 introduced `Caps.ManagedOnly`
  (`agents/agents.go:97-105`) as a kind-independent flag, and the server-side managed→TUI
  transition now branches on it (`sessionx/session_driver.go:73`), as do the Console sites. For
  muse that is one line in `Caps()`.
- **None of the approval `Interaction` was paid.** lcpp declares `Permissions: false` and routes
  its own approval gate through the existing question kind — its own comment says so
  (`agents/lcpp/driver.go:57-59`, and `approve` at `:586` builds `Kind: "question"`) — and
  `agents/driver.go:49` still reads `"question" (future: "approval" | "plan")`. The other half,
  `Caps.PermissionChoice`, was already true for claude, cursor, kiro, copilot and agy before 0093,
  so it was never muse's to pay either.

So the 3–5 day approval row stays whole, and the expected figure is the table's **22–33
session-days**. Whether muse follows lcpp in mapping approvals onto the question kind is a Phase 2
design decision, not a saving to assume: Decision 13 measured an `approval/requested` carrying
`toolName`, `rawArgs`, `judgeEscalated`, `protectedWrite` and `subject.stages[].argv`, and folding
that into a two-option question discards it.

### P2-2: the kind, the driver and the thread (`workspace/agent/internal/agents/muse/`)

The second work package: `session.KindMuse`, both registries, the four lifecycle switches, the
boot reconcile, shutdown, the deny-list — and the driver itself, with all seven ThreadHandle
methods, the status mapping, the approval and user-input round trips, steering, interrupt and
resume reconciliation. The transcript, the settings-file clamps, usage, the connection card,
the Console surface and deployment are still ahead.

**The approval interaction is the question kind for now, and that is a debt, not a decision.**
Decision 13 wants AF's first approval `Interaction`; decision 5 keeps approvals on, so a
session that cannot answer one is a session that cannot run a tool. Until the approval kind
exists, the driver builds a question-kind Interaction from the pending approval — the same
reuse kiro's ACP `session/request_permission` and lcpp's own gate already make. What that
costs is exact and worth writing down: the wire carries `toolName`, `rawArgs`,
`judgeEscalated`, `protectedWrite` and the parsed `argv` of every stage, and a two-option
question keeps only the command line. `Capabilities.Permissions` therefore stays **false**, and
the approval work package upgrades both together.

Three more capabilities are declared false for the same reason — a cap is a claim about a path
that was measured end to end. `CanTranscript`, `CanFork` and `CanForkAt` wait for their own
packages; `DynamicMode` is false permanently, because MSP has no method that sets AF's plan
mode and `session/setApprovalMode` is a different axis.

**A clamp route the ADR did not have: `MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL`.**
Decision 6's clamp 5 is the one with a privacy cost — without it a real turn assembles the
member's own `~/.claude/CLAUDE.md` into the model input — and the ADR recorded the settings
file as its *only* route, because `--no-foreign-personal-context` is absent from `muse serve`.
The binary's own string table names an environment variable for it, and it works: measured on
`muse exec` with markers planted in a throwaway HOME, setting it to `1` removes both the
"Including your Claude Code and Codex personal rules" banner and every trace of the planted
marker from the durable log, while `0` and unset both bring them back. The driver sets it at
spawn, together with the six observer variables and `MUSE_DISABLE_APPROVAL_JUDGE`. ⚠️ The
measurement is on `exec`; the equivalent check on `serve` needs a real turn, so the settings
work package still owes a behavioural test per key — which it owed anyway.

**A generator defect the wire would have shown first.** `RequestReceipt` is a named empty
object, and the type generator had been rendering it the way it renders an untyped
`properties: {}` *property* — as `json.RawMessage`, whose zero value marshals to `""`. AF would
have answered every must-answer server request with a string instead of `{}`. Named empty
objects now render as `struct{}`, and a test pins the marshalling.

Verification is `msptest` plus a live suite behind `MUSE_LIVE=1`. The live half covers exactly
what the fake host cannot — that the spawn argv, the child environment, the handshake and
`session/start` work against the vendor's own binary, that a second Resume reuses the live host
rather than spawning a second child for one session, that a Resume after a drop *reloads* the
stored session instead of silently splitting the member's history, and that a killed child
turns into a dead handle. It stops before a turn, so it spends no quota.

### P2-3: the transcript, and a correction to decision 4's at-rest half

The third work package: the live `item/*` stream, the mirror's turn model, subagent items —
and one premise of decision 4 that measurement overturned.

🔴 **`session.jsonl` does not hold the wire's records.** Decision 4 reads: "when it is not [up],
the read layer parses `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl`, which is
append-only and holds the same records." Measured on a real run, it is append-only and it is
not the same records. It is an event-sourced **runtime** log in muse's internal vocabulary:
`runtime.session` / `runtime.session.task` envelopes carrying `started`,
`model_request_configured`, `provider_request_options_configured`, `model_response_created`,
`assistant_message_committed`, `goal_usage_attribution`, `terminal` and so on — 43 of the 68
records in a single one-prompt echo session were `runtime.session`. There is no `Item` in the
file. A reader for it would be exactly the transcript reverse-engineering the Consequences
section claims this kind does not need, against an internal format with no stability promise.

The protocol's own answer is `session/read`, which returns `SessionHistory.items` — the stable
surface. It is not usable here for a reason that is about Agent Fleet, not about muse:
`Agent.Transcript` is called from the usage aggregation as well as the mirror
(`sessionx/session_usage.go`), so a fleet-wide usage query would spawn one 73 MiB host per
muse session.

**So the at-rest half is AF's own store**, the shape ADR 0093 decision 3 already uses for lcpp:
an append-only log of the items AF saw, under `muse-transcripts/<slot sid>.jsonl`, written from
the live stream and read back by `Transcript`. The difference from lcpp is worth stating, since
the two look alike: there the store IS the conversation, here the host owns it and this is a
mirror of what AF observed. What that costs is a turn that ran while AF was not watching —
which under decision 2 (managed-only, AF is the only writer) can only happen if the Agent died
mid-turn, and `session/read` on the next Resume is the documented way to backfill it. Nothing
in the decision's reasoning about the session id changes; only the file it named.

Three smaller findings the implementation pinned:

- **Items are revised, so the store folds on read, not on write.** An `item/started`, any
  number of `item/delta`s and an `item/completed` share an `itemId` and carry a rising
  `revision`. Keeping one line per item would mean a read-modify-write, which loses a
  concurrent append on a crash; appending every observation and folding by revision on read
  does not. Order is first-seen order, because sorting by revision or id reshuffles a turn
  whose tool call completed after the text that follows it.
- **Deltas are not persisted.** The `item/completed` that follows carries the whole text, so
  writing every fragment would multiply the store by the streaming granularity and then throw
  it away. They live in memory and `Transcript` overlays them, which is what makes the mirror
  stream.
- **Turn grouping is not `turnId`.** A user message opens a user turn and closes the open
  assistant one; assistant-side items fold into a single assistant turn. Items do carry a
  `turnId`, but a steered turn carries items from before and after the injection and a
  compaction between turns carries none at all, so grouping on it would drop them.

`Caps.CanTranscript` is therefore true, and the guide's capability table says a stopped muse
session shows its history — with the footnote that says whose copy it is.

### P2-4: the settings writer, the clamps, and the fail-close in `Resume`

The fourth work package. `~/.config/muse/settings.json` now has exactly one AF writer, it
merges rather than replaces, it takes muse's own lock protocol, it verifies outside the lock,
and a failure refuses the session start from inside the driver's `Resume` — not
`StartManagedSession`, because Resume is also reached from the turn, answer, carried-session
and bridge paths and from the boot-time `ReconcileManaged`.

**All six settings clamps are now verified behaviourally, and it cost no quota.** Decision 6
says a behavioural test per key is mandatory, because a misspelling inside a strictly parsed
section kills the host with rc=3 while a misspelling anywhere else is completely silent. Four
free oracles cover it: `muse config validate --plane defaults` (offline, names the exact
failing member), `muse exec --provider echo`'s durable log (records the assembled toolset),
`muse skills list` (reports each activation), and `muse serve` answering `initialize` (the file
did not stop the host booting). Each test carries its own control.

**One methodology trap worth writing down: the toolset oracle is blind without
`--trust-workspace`.** Measured, an untrusted workspace reports "Agent delegation: auto
unavailable" and the six `muse.subagent_*` tools never appear at all — 23 tools with the
workflow tool present and no subagent tools, clamped or not. A clamped-versus-unclamped
comparison run that way shows no subagent tools in either arm and "proves" a clamp that was
never exercised. With the flag the driver actually passes, the unclamped control is **29 tools
including 6 subagent tools and 1 workflow tool**, and the clamped arm is **22 with neither**.

Three corrections to decision 6's own clamp list:

- 🔴 **Three of the named skills do not exist.** Clamp 7 names `daemon`, `host-manager` and
  `slack-connector` as bundled skills that stand up long-running processes or open an egress
  path. On 1.3.0-R3401.1 `muse skills list` has no such skills, in any source. An activation
  entry for a skill that does not exist is accepted just as silently as a misspelled path, so
  writing them would have produced three dead keys that look like protection.
- 🔴 **`read-session` is not a foreign reader.** Clamp 7 groups it with `resume-claude`,
  `resume-codex`, `import` and `migrate` as skills "whose stated job is to read Claude Code's
  or Codex's transcripts". Its own description says the opposite: it locates and reads **Muse's
  own** session logs, and states "never probe ~/.claude, ~/.codex, or ~/.grok for Muse context,
  even when quoted content mentions them". Clamping it would remove a working feature for a
  reason that does not apply to it, so AF leaves it on and a test pins that.
- **`materializeMu` no longer applies.** Decision 6 says the writer should serialise under the
  MCP materialiser's existing mutex. Decision 11 then removed the `mcp_servers` half of this
  writer's job, so AF has exactly one writer for this file and the coupling would buy nothing;
  the writer has its own mutex, and the cross-process coordination is muse's lock file.

Two details from the lock protocol are in the code because the measurement said so: AF **waits**
on a held lock rather than failing fast (failing would refuse a launch because the member
happened to run a muse command), and it **verifies after releasing** rather than trusting the
merge, because muse reads the file eleven syscalls before it takes the lock. A single lost
update is retried once; a second failure refuses the start.

⚠️ What is still owed: clamp 5's equivalent check over `muse serve` (the free oracle runs
through `muse exec`, and `serve` reads the same file but assembles context at turn time, so the
`serve` arm is argued rather than measured), and clamp 6's effect — the approval judge has no
settings key and no `serve` flag, so `MUSE_DISABLE_APPROVAL_JUDGE` is set on the strength of
its name and needs an approval to observe.

### P2-5: AF's first approval `Interaction`, and the card that answers it

The fifth work package, and decision 13's own: `agents.InteractionApproval` exists, muse raises
it, the read layer sends it out as `pendingApproval`, and the mirror answers it allow/deny
through `/respond`. `Capabilities.Permissions` is now **true** — the first kind to declare it.

**The survey that justified building it rather than reusing the question kind.** Agent Fleet
already had a permission surface: `SessionState` has had a `permission` value since the hook
route, and `PermissionCard` renders one. It cannot serve a managed kind, and not for a reason
that could be patched: measured in the tree, its three buttons answer by driving a tmux modal
— `sendKeys(["Enter"])`, `["Down","Enter"]`, `["Down","Down","Enter"]` — and a managed session
has no pane for the keys to land in. That is the mechanism behind decision 13's "or not even
there, for managed", and behind `Caps.PermissionChoice`'s condition that an approval must be
answerable *from the Console*. Until this package, muse met that condition only by folding
approvals into the question kind, which kept the command line and discarded the tool name, the
protected-write marking, the judge escalation and the parsed argv of every stage.

So the approval is its own kind, with its own payload and its own verb:

| | question | approval |
|---|---|---|
| asks | choose an answer | may this tool run |
| refusing | the agent carries on | this tool stops |
| carries | `[]transcript.Question` | summary, tool, command, per-stage argv, protectedWrite, judgeEscalated |
| answered by | `decision: "answer"` + picks | `decision: "allow"` / `"deny"` |
| wire key | `pendingQuestions` | `pendingApproval` |

**Two existing consumers had to learn the difference, and both were silently wrong before.**
`applyManagedAnswerAll` (the operator's full-form answer tool) and `applyManagedQuestion` (the
chat bridge's buttons) both guard on `Kind != "question"` and report "no pending question" /
"already answered". Against an approval those are not merely unhelpful, they are the wrong
fact: the session is blocked on a tool and the operator is told nothing is waiting. Both now
name the approval and point at the control that answers it.

The card offers exactly **allow and deny** — no scope selector, no "always allow". That is not
a simplification: gate B1 measured `onRequest` mode presenting exactly two choices,
`allow_once` and `abort`, so a third button would promise a persistence the runtime never
agreed to. `pendingApproval` is also withheld from a stopped session, the same rule
`pendingQuestions` follows: a card nobody can answer is worse than no card.

⚠️ The card's rendering is covered by a dom test, not by a screenshot — no muse session can be
launched yet (the binary is not installed and the kind is not in the launch menu), so nothing
in this package was seen on screen. The visual check belongs with the Console surface package.

### P2-6: the connection status, the login routes, and a login that must not have a terminal

The sixth work package: what `GET /connections` says about muse, the device-code sign-in
(start → poll), the API-key fallback, the disconnect, the same four paths on the Control
Plane's proxy allow-list, and the credential gate in `Resume`.

**Muse has no way to ask whether it is signed in.** No `whoami`, no `auth status`, and nothing
on the wire either — the 47 methods carry no auth verb at all. What it has is one file,
`~/.config/muse/auth.json`, so that is what AF reads. That turns out better than the precedent
rather than worse: kiro and codex each spawn a child per probe and therefore cache the answer
for 30 seconds, while a file read is cheap enough to do on every `/connections` poll, so a
sign-in shows up on the next one instead of up to half a minute later.

Three measurements decide how it is read, and each is a way a reasonable reader is confidently
wrong. All three are on 1.3.0-R3401.1, and none of them cost a prompt.

1. 🔴 **`api_key`'s presence does not mean metered billing — the account login writes one.** A
   real device-code sign-in leaves `access_token`, `api_base_url`, `api_key`,
   `mechanism: "oauth"`, `obtained_via: "device_code"`, `user_email` and `user_full_name` side
   by side. `muse auth set --api-key-stdin` leaves `api_key` **alone**, with no `mechanism` and
   no `obtained_via`. So a card keyed on the key's presence would tell every subscription
   member they were being billed per use — the exact inversion of what decision 9 exists to
   protect. The discriminator is `mechanism`, and the Agent derives `metered` from it so no
   surface has to know this.
2. 🔴 **`muse logout` does not remove the file.** It leaves `{"schema_version":1,
   "providers":{}}` behind, mode 600. Connectedness is therefore "`providers.meta` carries a
   credential", never "the file exists" — the latter reads as signed in forever after the first
   logout.
3. 🔴 **`muse auth set` REPLACES the whole provider entry.** Measured over a device-code-shaped
   file, it left `api_key` as the only key: the access token, the mechanism and the e-mail all
   gone. An API key written over an account login does not merely take priority for the next
   turn, it destroys the sign-in. So the card's API-key route **refuses** while an account login
   is stored (`409 account_login_present`) instead of warning: the member disconnects first, and
   the two-step is the confirmation.

**🔥 The login is the first in this tree that must run OFF a terminal, and a PTY silently breaks
it.** Every existing connection card drives its CLI through `agents.StartFlow`, which is
`pty.Start`. Measured on a PTY, `muse login` prints the device URL and then **stops** at
"Press Enter to open it in your browser:" — it never begins polling Meta, and there is no
browser in this container to open, nor anything to press the key. Off a TTY the same binary
prints the URL and goes straight to "Waiting for approval…", self-polling exactly the way
kiro's and cursor's flows do. `agents.StartPipeFlow` is that shape: stdin is `/dev/null`,
stdout and stderr are one pipe, and everything else — `Clean`, `WaitFor`, the flow store and
its TTL reaping — is unchanged. Its test carries the PTY arm as the control, because a pipe
flow that quietly fell back to a PTY passes every assertion made on the pipe arm alone.

Three smaller things the implementation settled:

- **The poll's signal is the credential, not the child's last line.** Muse writes auth.json
  before it says anything, and its wording is not a contract. But a child that has **exited**
  without writing one is a finished failure (an expired code, a refused approval), and saying
  so ends the card's poll instead of spinning to a 15-minute deadline for something that can no
  longer happen. `Flow.Ended` is what reports it; an unknown flow id stays "not yet", because
  the TTL reaper reaches that state too and it says nothing about the approval.
- **`FlowStore` needed a `Get`.** A poll that only wants to look at a flow it will keep waiting
  on cannot use `Take` and then `Put` it back: `Put` mints a **fresh** id, orphaning the one the
  client is polling with. This was written the wrong way first and the second poll of every
  login would have found nothing.
- **`META_API_KEY` is reported, not acted on.** Muse's own `login --help` says it "always takes
  priority over the account login", so a deployment that sets it makes `connected: true`,
  `metered: false` and a per-use bill all true at once. AF neither injects one (decision 9 
  settled that) nor strips a member's — stripping would silently override a deliberate choice —
  so `env_key` exists to make the contradiction visible, because its failure mode is an invoice
  rather than an error.

**`Resume` now refuses without a credential**, after the clamp gate rather than before it: the
clamps are the safety mechanism and must be applied on every path that could spawn a host, so a
sign-in that arrives later must not find an unclamped file waiting for it. Without the gate an
unauthenticated session accepts `session/start` and then ends **every** turn `authRequired`
(P2-1) — healthy in the Console, unable to answer.

⚠️ **The card itself moved to the Console-surface package, and the work-package table is wrong
about that.** The table puts "connection card" in this row, but a card cannot be rendered
honestly without three things the table files under Console surface: `SessionKind` (nothing
renders without it), the registry descriptor `kindDisplayName` and the badge read from, and the
kind's colour — which on the `--kind-lcpp` precedent means a measured hue clearing the ten
existing kinds *and* the semantic colours in both themes, plus a headless render. Attempted
here, the card would have shown an uncoloured, unlabelled badge. So this row landed the whole
server half — status, the four routes on both `routes.go` files, the CP allow-list — and the
Console surface package gains the card, where it also gets the screenshot that P2-5's approval
card is still owed.

### P2-6 addendum: the two turn-only homework items, and what the first turn found instead

The Consequences above left two things owed that no free oracle could answer: clamp 5's
equivalent check over `muse serve`, and clamp 6's effect. Five real prompts were spent (the
member authorised four to six). One is answered; the other turned out to be unanswerable, for a
reason worth more than the answer.

The instrument, because it is reusable: a throwaway `HOME` carrying **fake** `~/.claude` and
`~/.codex` markers, `XDG_DATA_HOME` in the throwaway so the durable log is readable, and
`XDG_CONFIG_HOME` pointed at the member's **real** `~/.config` so muse finds their credential
without it being copied — a copy can take a token refresh with it and sign the member out of the
original. The member's `settings.json` was never written; it carries no foreign-context clamps,
which is what makes the environment variable isolable as the only difference between arms.

**✅ Homework 1 — clamp 5 works over `serve`, and it was argued for a real reason.** With
`MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=0` the planted `~/.claude/CLAUDE.md` marker
appears in the durable log; with `=1` it does not. The env route is now measured on `serve`, not
inferred from `exec`.

Two methodological findings came with it, and both would have produced a false green:

- 🔴 **The model's reply is not the oracle — the durable log is.** Asked to list the tokens it
  could see, the model answered without the marker in *both* arms. A test reading the reply would
  have found the two arms identical and "proved" a clamp that was never exercised.
- **The banner is not the oracle over `serve` either.** "Including your Claude Code and Codex
  personal rules" never appears in a `serve` durable log, in either arm; on `exec` it was one of
  the two signals. Only the planted marker distinguishes them.
- The `~/.codex/AGENTS.md` marker appeared in **neither** arm, so what `serve` assembles is
  narrower than the banner's wording implies. Not concluded as "Codex is not read" — the path it
  would read was not established, only that this one was not it.

**🔴 Homework 2 — clamp 6 cannot be measured, because no approval can happen.** A turn that ran
`echo` got its tool executed with zero `approval/requested`, under an explicit
`approvalMode: "onRequest"`. The durable log says why, and the answer is not the mode:
`runtime.session.permission_profile_committed` records `approval: "on_request"` (so AF's mode was
accepted), while the tool call carries `policy_decision: "allow:policy"` — a policy allowed it
before the approval layer. The committed `resolved_snapshot` for AF's exact launch is:

```
approval: "on_request"
filesystem: { mode: "unrestricted", rules: [], workspace_roots: [], protected_metadata: false }
local_command_network: { mode: "enabled", targets: [] }
reviewer: "human"
```

Five session-start-only arms (free, no turns) isolate the cause and close the escape routes:

| launch | filesystem | rules | local network |
|---|---|---|---|
| `--disable-sandbox --trust-workspace` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --sandbox-network proxy-only` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --disable-write` | `unrestricted` | 0 | `enabled` |
| `--disable-sandbox --trust-workspace --disable-write --disable-shell` | `unrestricted` | 0 | `enabled` |
| `--trust-workspace --sandbox-network restricted` (no waiver) | `managed` | 6 | `restricted` |

So: **`--disable-sandbox` is the whole cause, and it overrides every narrower posture flag.**
`--trust-workspace` changes the profile not at all — its help text says it only "load[s] each
session workspace's skills and rules", which matches, and AF's reason for passing it is intact.
A second turn confirmed the consequence past any doubt about workspace trust: a `tool:write_file`
to `/tmp`, **outside `workspaceRoot` entirely**, also resolved `allow:policy` and wrote the file
with no approval.

The chain is therefore closed and permanent under the current host contract: gate A's AppArmor
finding forces `--disable-sandbox`; that flag flattens the permission profile; a flat profile
policy-allows every tool; a policy-allowed tool never reaches the approval layer. `MUSE_DISABLE_
APPROVAL_JUDGE` stays set on the strength of its name and stays **unverifiable here** — not
merely unverified.

Three smaller wire facts from the same arms, all free:

- **`ApprovalMode` has four values on the wire** — `allowAll`, `promptUnmatched`, `onRequest`,
  `denyUnmatched` — and the host's own `component_ceilings.approval` lists three
  (`on_request`, `prompt_unmatched`, `allow_all`), so `denyUnmatched` is on the protocol but not
  in this backend's ceiling. AF maps two and says why it maps no more.
- **The two layers disagree about the mode.** The started session echoes what was asked
  (`session.approvalMode.mode`, source `startup`) for all four values, while the committed
  enforcement profile reports `approval: "on_request"` for all four. Which governs is not
  settled; under the waiver it does not matter, and AF keeps sending the mode because it costs
  nothing and is already right if the posture ever changes.
- `muse serve --help` states outright that "Approval mode ... is selected on the wire, so there
  is no approval flag here", which confirms the route AF uses is the only one.

**What landed as a result.** `Caps.PermissionChoice` is now **false** — the member-visible half,
a launch control whose two settings would behave identically. `Capabilities.Permissions` stays
**true**: it declares that the driver supports the approval Interaction kind, which is built,
tested and correct, and it has no consumer outside the Agent process, so it promises the member
nothing. The approval card and interaction code stay exactly as P2-5 left them, ready for a
posture that can raise one. `guide/ref/agents.md` gains footnote 13 in both languages, and it is
the one row where a dash means less safety rather than a missing feature: a muse session reaches
this container the way `shell` does.

### P2-7: deployment — the pin, the on-demand install, and the drift lock that costs nothing

The seventh work package, and the one decision 8 had already thought through: `workspace-agent
install-muse` in kiro's shape, the pin in `versions.json` with its sha256, the `BAKE_AGENT_CLIS=1`
path, the `NOTICE` entry, the `env_tool_versions` row, the drift row and a release contract.

The pin is `1.3.0-R3401.1`, and it is **verified rather than transcribed**: the local probe
binary's sha256 equals the release manifest's `artifacts.x86_linux.checksum`
(`71b089d0…e2a33`, 313,800,920 B), and the manifest's `msp_schema_fingerprint` equals
`msp.SchemaFingerprint` as generated in P2-1 byte for byte. Two independent checks that the
number written into the Dockerfile describes the file the code was built against.

**No launch guard, and that is a consequence of decision 2 rather than an omission.** kiro
re-pins on every launch because its pane program can prepend a guard; muse is managed-only, so
there is no pane program at all. So the HTTP route is the *only* thing that performs an upgrade
or repairs a shadow, and the driver's `Resume` refuses with a message naming the subcommand
instead of stalling for minutes on a download the member did not ask for.

**🔴 The version comparison is the trap decision 8 warned about, and it reaches further than the
installer.** `muse --version` prints `Muse Code 1.3.0 (1.3.0-R3401.1)` and the pin is the
parenthesised build id. Bare-semver extraction yields `1.3.0`, which never equals the pin — so
every check reads "stale" and re-downloads 299 MiB, forever. That regexp is not hypothetical: it
is `extractVer`, the Agent's own shared version extraction, which the settings UI's tool-versions
row goes through. So `toolSpec` gained a `VerRe` hook and the muse row sets it; a test pins that
`extractVer` and `museParseVersion` still *disagree* on the measured line, so the justification
cannot rot into a stale comment.

⚠️ And a smaller version of the same trap, found by writing the wrong thing first: one regexp
with an alternation (`\(…\)|semver`) does **not** work, because the bare version sits to the LEFT
of the parenthesised one on the real line and a match is chosen by position before alternative.
Two regexps tried in order. The table test caught it on the first run.

**The drift lock is the only credential-free agent contract in the repository**, which is the
property this ADR named as muse's own. `muse-contract.yml` verifies three things against the real
proprietary binary and spends nothing: the manifest's fingerprint equals the generated constant,
the installed binary *exports* the same fingerprint (`muse schema`, offline), and
`muse --version` still carries the build id in parentheses. Because none of it needs a
credential, the release watcher dispatches it **unattended** — unlike cursor and kiro, whose
release edges are held for a deliberate manual run. What it deliberately never does is run a
turn: over `serve` that needs a member's personal subscription.

Two smaller things the implementation settled:

- **A mutation sweep found the checksum unverified — by the tests, not by the code.** Deleting
  the `verifySha256` call left every other test in the new file green. That is the shape of a
  defect this repository has already shipped (the boot-install `set -e` incident, where five
  sha256 checks were decorative at once), so the download URL became an injectable var and the
  gate now has a test that feeds a file with the wrong hash and requires a refusal, with the
  matching hash as its control.
- **muse is the one drift row with no `setup-agent-cli` branch, on purpose.** Its contract
  installs the artifact itself, because verifying the manifest's own checksum and fingerprint IS
  the check — handing that to the shared installer would move the thing under test out of the
  test. Both files say so, since the action's comment claims parity with the drift targets.

### P2-8: the Console surface — a colour chosen by sweep, and decided by looking at it

The eighth work package: `SessionKind`, the registry descriptor, the kind colour and its seven
CSS twins, the connection card, the i18n catalogues, the bridge label and the guide. With it the
kind is offered in the launch menu for the first time — gated on the two things that must both be
true, the binary installed and a credential stored.

**The colour was swept, not picked.** `console/scripts/kindcolor/muse.mjs` scores a candidate
against the ten existing `--kind-*` hues plus the semantic colours sharing the same screens, in
both themes, and reports the worst case. Three constraints on the search matter more than the
arithmetic, and each came from a run that produced a wrong answer:

- **A chroma band.** Unconstrained, the sweep's top results were all dusty pinks at chroma 20 —
  they score well precisely because a muted colour is far from every saturated one, and they would
  have been a third muted badge beside copilot and opencode. The band is measured from the shipped
  kinds (`--chroma`), with the two greys deliberately outside it.
- **A hue exclusion around the semantic colours.** The lcpp round rejected red categorically — a
  badge in the error hue reads as an error state whatever its dE — so the hue is excluded rather
  than allowed in on a good number. ⚠️ Written inverted the first time, which excluded everything
  *except* the semantic hues; the sweep then answered in the error red's own hue, and the printed
  table is what showed it. A silent filter would have been believed.
- **The picture, for the last step.** The sweep's own answer for the light theme was `#5f376b`
  (min dE 15.9). Rendered, it is a grey plum: beside kiro's vivid violet and cursor's magenta it
  reads as a third grey rather than as a coloured peer — the same mistake the chroma band exists
  to prevent, one step further in. `#6b2d7e` costs 3 points of dE and buys a colour, and its
  margin is still twice the precedent floor. **dE ranked the candidates; looking at them decided
  between the ones it could not tell apart.**

Final: `#dbabe6` dark (min dE 17.9 vs cursor, contrast 7.11) / `#6b2d7e` light (min dE 12.9 vs
kiro, contrast 7.21). Not Meta's brand blue, deliberately — agy and ssm hold that region, and this
palette has always chosen distinguishability over brand fidelity.

**The render harness found a defect in itself, twice**, which is the argument for keeping it
(`console/scripts/kindcolor/chips.mjs`): it parsed the token values out of the real stylesheet and
(1) a `--name: value;` regexp started matching inside a COMMENT — the comments explain each hue by
naming the surfaces it was measured against, ending in `--bg/--panel/--bar/--active-bg: 7.11` —
and ran its value past the next declaration, swallowing `--kind-lcpp` entirely; then (2) the
variables were injected through an inline `style=""` attribute, which several token values
terminate early because they are font stacks containing double quotes, so **every** `--kind-*`
was dropped and the page rendered eleven colourless chips while the semantic swatches (declared
before the fonts) looked fine. Nothing looked broken either time. A count assertion caught the
first; the second was caught only by looking at the image.

**A defect in the shipped transcript, found by working the checklist rather than by a test.** The
mirror's turn footer is `endTs || ts`, and muse folds a whole assistant turn into ONE row carrying
only the first item's time — so a 90-second turn was stamped 90 seconds early. `turnTime.ts`'s own
header says the footer "used to show when the turn *started* instead of when the answer landed";
muse had quietly reintroduced it. The fold now advances `EndTS` on every item, and muse joins
opencode and copilot in that comment's third family.

Two things the package deliberately did not do. There is **no `LaunchDefaults` block on the card**:
muse's model and effort controls are their own work package and its permission choice is absent by
measurement, so the group's only row would be an inert "Default" picker. And **muse is not in
`MCP_KINDS` / `mcpreg.knownKinds`** — which surfaced an item no work package owns: decision 11 puts
MCP servers on the wire in `session/start.config.mcpServers`, the settings writer correctly writes
no `mcp_servers`, and **nothing sends them**. The guide's "Receives integration (MCP) servers" row
is honest at `—`, and the wire half remains unbuilt.

### P2-9: the model and effort controls, and the default nobody would have chosen

The ninth work package: `model/list` behind `GET /agents/muse/models`, the reasoning-effort
list, the two Console controls, and one thing the work-package table did not have.

🔴 **The launch default was an opt-in to data sharing, and it was invisible.** Decision 6's
clamp 8 says AF defaults to a non-contributor model. Nothing in the table owned it, and the
shape everything else uses would have shipped it wrong: a launch with no model chosen sends no
`modelId`, the host applies its own default, and measured on the live catalogue that default
is `muse-spark-1.3-contributor` — `isDefault: true`, and the only rows carrying a `description`
at all are the two contributor ones, whose text is "Your content, including inter-session
messages, may be used for product improvement." A member who never opened the picker would
have had every conversation used that way, with nothing on any screen saying so.

So "the member chose no model" resolves in the **driver**, not in the Console: `session/start`
names the newest row the vendor makes no such claim about, and `UpdateSettings`' `ClearModel`
resolves to the same one rather than to the host's. That placement is the point — a scheduled
run, an MCP-created session and the chat bridge all reach `openSession` and none of them reads
a Console setting. The Console's stored default therefore stays the empty string, which is
both safe and version-proof; pinning `muse-spark-1.3` into `DEFAULT_AGENT_LAUNCH` would have
gone stale at the next release. The contributor twins stay selectable, because clamp 8 was
deliberately softened to leave the member the choice, and the picker now carries the sentence
that says what the choice is.

The predicate is a union — the `-contributor` suffix **or** a description naming product
improvement. Measured, the two agree exactly, so it is redundant today. It is a union because
they fail in opposite directions (a renamed suffix leaves the sentence, a reworded sentence
leaves the suffix) and the two errors do not cost the same: a false positive costs a model AF
will not pick for you, a false negative costs your conversations.

**The effort list is the generated enum, not a copy.** The generator now emits a `…Values`
slice for every string enum in the bundle, so `msp.ReasoningEffortValues` is what the picker
offers AND what the driver validates against — a value the vendor adds in 1.4 cannot end up
offered-but-refused, and the fingerprint lock already covers it. A test walks the offered list
through the driver's own validator, with an undeclared value as its control.

Three smaller things:

- **Catalogue reads reuse a live session's host.** `model/list` is a query — no `commandId`, no
  durable record — so a running session can answer it, and only a workspace with none pays a
  process start. A probe host's argv deliberately drops `--trust-workspace`: trust is a decision
  about a working copy and a catalogue query has none. It still applies the clamps first, which
  is the driver's invariant rather than a need this path has.
- 🔴 **A segmented control cannot hold this catalogue, and what it does instead is not clip.**
  Rendered headless with the real four ids, `.choice-seg` takes the whole row, wraps to a second
  line and draws over the row's own "Default model" label; the nine effort values do the same.
  The count rule that has always guarded this (`> 8`) passes four ids of 26 characters, so it
  gained a width clause. Assumed as "it overflows", measured as "it covers the label" — the
  screenshot is what told the two apart.
- **Two shipped defects found on the way.** `DEFAULT_AGENT_LAUNCH` had no `lcpp` row, so lcpp's
  saved launch defaults were discarded on every reload — the exact failure its own comment
  records for copilot, shipped again. And this card's three warning strings were written with
  markdown asterisks that nothing in Settings renders, so `**per use**` reached the member
  verbatim; the house pattern is a separate `_strong` key, and these are now plain text. A dom
  assertion that the card renders no `**` covers all of them.

⚠️ The mutation sweep was worth its cost again: of eleven mutations, ten failed a test as they
should and one passed green — the assertion for the contributor note matched the word
"contributor" in the picker's own option labels, so deleting the note entirely changed nothing.
It asserts on the note's own sentence now.

### P2-10: usage — a ledger with no price, a chip with no query, and two kinds nobody could see

The tenth work package: the accounting declaration, the subscription-quota chip, the MCP
surfaces, and the usage view's own colours.

**The token ledger needed almost nothing, because the transcript already carries the numbers.**
`applyUsage` folds `item.Usage` onto its turn (P2-3), and the fold, the watermark and the
per-turn attribution are the shared ones. What this package adds is the *declaration*:
`usageMeasuredForKind` returns **`MeasuredPartial`**, and the reason is worth keeping in the
code rather than only here — the wire's own numbers are clean (cached input separated, no
differencing, a failed turn reporting nothing, a resume replaying nothing), so what is partial
is OWNERSHIP. Gate B1 measured one turn reporting 88,077 prompt tokens on the wire while the
host's durable log recorded 116,816 across six calls, two of them a subagent's. AF's clamps
turn subagents and observers off, which makes the wire complete *in practice* — and that is
exactly why it stays partial: a clamp is a setting, and this field describes the source.

**No cost estimate, stated in the table rather than left as an absence.** `usageCatalogProviders`
gains a comment and no row: models.dev has no entry for Muse Code's models, and the vendor's
own catalogue reports `cost: null` on all four (measured again this round). Both ends are
empty, so the kind ships a token ledger with no cost chip; a guessed provider would put a
number on the screen that nobody charged. An absent row looks identical to an oversight, which
is what the comment is for.

**The quota chip is an observation, not a query, and that difference is the whole design.**
`usage/read` and the unsolicited `usage/changed` carry the same object, so AF records both into
one process-wide value — what they describe is the ACCOUNT, and under decision 3's
one-host-per-session shape five sessions all report the same subscription. Three things follow:

- Newest wins **by the host's own `observedAtMs`**, not by arrival. Two hosts can deliver out
  of order, and a chip that walks backwards reads as usage being refunded.
- `GET /muse/usage` asks a live host when it has nothing cached, and **spawns nothing**: a
  quota reading is not worth a 299 MB process start.
- 🔴 `{ok: false, authed: true}` is a real state, not a failure. Measured, `usage/read` answers
  `{}` until that host has seen a completion, so a workspace whose muse sessions have all just
  started has no reading — and reporting 0% used there would tell the member they had their
  whole week left. The WsBar's existing "authed but unavailable" path already renders exactly
  that (a "—" chip), so the shape was chosen to land in it.

The window's length is a wire field rather than a constant (`windowDurationMins`, measured 300)
and it is carried through instead of assumed, which is why muse's first row is labelled
"current window" where claude's and codex's say "5-hour".

**The usage view could not see lcpp either.** `KIND_STACK_ORDER` is the list of kinds the chart
gives a colour; a kind missing from it falls into the grey "other" fold. It had seven entries,
and lcpp's consumption has been folded into "other" since ADR 0093 — invisible rather than
wrong, which is why nobody noticed. Adding two kinds to a palette that was *ordered* for
CVD-adjacency is not an append, so the search was redone as a script that can be re-run
(`console/scripts/kindcolor/usageorder.mjs`): every permutation of the nine, scored on its
worst adjacent pair under normal vision and all three dichromacies in both themes.

- Appending the two the obvious way scores **worst adjacent ΔE 10.8** — lcpp's yellow against
  muse's lilac, which collapse under tritanopia. Nobody would have thought to check that pair.
- The searched order scores **17.0**, which is also better than the seven-kind order it
  replaces (13.0 under CVD). The binding pairs are kiro|copilot in normal vision and
  copilot|agy under tritanopia.
- The bands were then rendered and looked at, in both themes and all four visions. The numbers
  rank the permutations; the picture is what confirms a pair the arithmetic passed does not
  read as one block — this palette's own history (the two greys) is why that step exists.

Two smaller things: `list_models` refused `kind=muse` outright, which is the model-list
package's own miss found from the MCP side; and `get_session_usage`'s description now says what
muse's numbers do and do not include, because an assistant reading `cumulative` has no other
way to know a subagent's tokens are missing.

**Not in this package, and not forgotten:** the context-usage gauge. `session/contextUsage`
exists on the wire, but it only fires around a turn, so declaring `contextBar` would be a
capability claimed from a schema rather than measured end to end — the rule the other four
false caps already follow.

### P2-11: the instruction layers — one file, two blocks, and the text that is not ours

The eleventh work package, and the one decision 12 left as an explicit choice: muse has ONE
user-scope rules file and both of AF's apply paths have to share it.

**The answer is markers, not ownership.** Decision 12 offered two: AF owns
`~/.config/muse/AGENTS.md` outright with delimited sections, or merges into the member's own
text by markers. The second, for the reason decision 6 gives for `settings.json` — the file is
not AF's. It is where a member writes their own rules for Muse Code, with or without Agent
Fleet, and owning it outright deletes that text on the next reconcile. The repository has
already paid for the other answer once (docs/log/60 damage 1, where AF `cp -f`'d a CLI's file
away on every start).

That makes this codex's situation exactly, so it is codex's mechanism exactly: `mdblock`, one
`AGENTS.md`, two AF-owned blocks in reconcile's call order (fleet → user), everything outside
the markers untouched. `mdblock` exists so the spelling of those markers cannot drift per kind,
and using it here is what stops muse becoming the seventh copy of strip-and-append.

Three details are in the code because the measurement said so:

- **The distribution status measures the BLOCK, not the file.** muse's `AGENTS.md` exists as
  soon as the fleet policy lands, so `fileExists` — which is what kiro and copilot use, because
  their artefacts are one file each — would report the member's instructions as delivered
  before they were written. The same trap agy and codex already avoid.
- **AF writes `AGENTS.md` and never `CLAUDE.md`.** muse probes both, and the measured project-
  layer precedence is "AGENTS.md wins, CLAUDE.md is skipped this session". Writing both would
  mean writing a file whose content muse announces it is discarding. A test pins that the
  second file is not created.
- **The skills half needed no code.** `fleetskills.Apply` into `~/.config/muse/skills` is the
  whole of it: measured again this round against the real binary, `muse skills list --source
  user` lists a dropped `SKILL.md` with no install step and no lock-file entry.

Verification is the two apply paths through `reconcileAgentInstructions` plus a live check that
spends nothing: the real writers into a throwaway HOME, then the vendor's own
`muse skills list --source user` as the authority on whether the topic file is actually
registered. ⚠️ Written the wrong way first — the draft also ran `muse config validate --file`
against `settings.json`, which is not what that verb takes (it validates an enterprise config
*document*, `{schema_version, settings}`), and a second copy of the command ran without the
throwaway environment at all, i.e. against the member's own home. Both are the same mistake:
reaching for an oracle by name instead of by what it answers.

### P2-12: MCP on the wire — the residual no work package owned, and the spelling that is not the documented one

P2-8 surfaced this one rather than closing it: decision 11 puts integration servers in
`session/start.config.mcpServers`, the settings writer correctly writes no `mcp_servers`
block, and **nothing sent them**. `internal/agents/muse/` carried zero mentions of
`mcpServers`. This package is that gap.

🔴 **The wire's transport spelling is not the one decision 11 documents, and getting it wrong
costs the whole session.** Decision 11 describes the settings-file block as
`transport: stdio | streamable_http`, which is correct for that file — and the wire union's
HTTP arm is `streamableHttp`. The union is **closed** (`x-msp-openness: "closed"`, the
schema's own ruling), so an undeclared value is not "that server did not start": it fails
`session/start` decode, and the session with it. Measured against the real host this round:

```
-32602 invalid session/start config: mcpServers does not match the supported shape
```

So a member with one HTTP integration would have had *every* muse session refuse to start —
and the ADR's own text is what would have led anyone there. The fix is to stop transcribing:
the generator now emits a closed union's discriminator constants
(`SessionMCPServerConfigTransportStdio` / `…StreamableHTTP`), flattening a union had been
dropping the one `const` that tells the arms apart. The live test carries both arms — AF's
real serialisation accepted, and the file spelling refused as the control, because a host that
accepted any string would pass the positive test on its own.

**Every server rides as `mode: optional`, with no member-facing choice.** The wire default is
`required`, and a required server that fails to start aborts the run — so one tenant
integration with a dead endpoint would stop the member's agent from starting. The registry has
nowhere to put the choice (`secrets.MCPServer` has `enabled`, `targets`, `kinds`, `timeoutMs`
and no `mode`), which decision 11 already names as out of scope.

🔴 **`MaterializedKinds` is not the list this needed, and reading it as one has already shipped
a bug.** It means "whose native config file does af write", and muse has none — adding it
there would report a permanent `skipped` for a fully served kind. But `selfReportToolAvailable`
was using it as a stand-in for "does this session get the af MCP server", which for muse is
**true**. That is precisely the mistake `peerTargetAllowed`'s own header records (lcpp's
absence from the same list silently forbade peer messages to it, found live on 2026-09-21), so
the answer is a second list that says what it means: `mcpreg.ServedKinds`. Without it a muse
session is told to call `af_report` and has no such tool — a failure whose only symptom is a
report that never arrives.

⚠️ The mutation sweep earned its keep twice in this package. One mutation did not compile, so
it proved nothing and had to be re-run in a form that did (an unused variable is not a
measurement). The other passed green: swapping `ServedKinds` back to `MaterializedKinds`
changed nothing any test checked, because the self-report hint had no test at all. It has one
now, with shell/ssm as the other side of the pair.

What muse deliberately does NOT get: a row in `fileSpecs` (`mcpproj`), so it is neither
inspected for project-scope servers nor a copy target, and `HasProjectScope: false` — agy's
shape. No Muse project-scope spelling is documented and none was measured; that is a static
fact about the kind rather than a runtime fallback to another kind's file.

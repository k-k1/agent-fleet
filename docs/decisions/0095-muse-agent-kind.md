# 0095. Meta's Muse Code as a session kind (`muse`) — a vendor protocol instead of a TUI contract, behind three live gates

English | [日本語](0095-muse-agent-kind.ja.md)

- Status: **proposed** (2026-09-20). Nothing is implemented. Every `file:line` below was read on
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
| Read source | **◎ append-only** | `~/.local/share/muse/sessions/YYYY/MM/DD/<sid>/session.jsonl` (subagents under `subagent/<id>/`). `session/start`'s result returns that `path`, so AF never has to guess it |
| Version pin + sha256 | **◎ both, anonymously** | Channel manifest (`api.meta.ai/muse-code/channels/muse-stable`, HTTP 200 anonymous) names a **version-addressable** release manifest which publishes url + **sha256** + size per platform, `aarch64_linux` included. The artifact itself served HTTP 206 anonymously — no account is needed to bake |
| Auto-update suppression | ◎ | `MUSE_NO_AUTO_UPDATE=1`, one environment variable. With it set and the binary in place, the launcher **only reads**: `muse --version` succeeded from a read-only install directory |
| Cost of building the harness | **◎ ≈ $0** | `--provider echo` is a deterministic built-in provider. `muse exec --provider echo --json` ran a complete session with no credential. The account is needed for acceptance, not for construction |
| Host footprint | ◎ | An idle `muse serve` host is **≈ 73 MiB RSS** (74,924 KB). Compare the registry's `tuiMemoryCost`: claude 230 MiB, opencode 300 MiB (`console/src/agents/registry.ts:227,469`) |
| **OS sandbox** | **🔴 ◎ cannot run here** | Linux sandboxing is bubblewrap (38 `bwrap` / 50 `seccomp` strings in the binary; the docs say "needs a working bubblewrap and a non-musl build. Without it, every sandboxed shell command aborts as an environment failure"). `bwrap` is absent from the image, and in this container a user namespace is creatable but **`move_mount` returns EACCES** — bubblewrap cannot attach its mounts |
| **Shared repository state** | **🔴 ◎ it writes there** | `muse exec -w create` chose `<repo>/.muse/worktrees/<date>-<hash>` as the workspace root, created `.muse/.session-worktree-reservations/`, and **appended `/.muse/worktrees/` to `.git/info/exclude`**. In a linked worktree that file is the parent clone's, shared with every other session |
| **Foreign personal context** | **⚠️ ◎ on by default** | First run printed `Including your Codex personal rules and 5 skills`. It discovers `~/.claude` and `~/.codex` skills and rules unless foreign context is turned off — and the flag that does it, `--no-foreign-personal-context`, exists on `muse exec` but **not on `muse serve`** (measured: `unknown option`) |
| Feature overlap | ⚠️ △ | Subagents (8 per tree by default, `agents.execution_capacity` 1–64), four background observer agents that each make their own model calls, workflows (1,000 children lifetime), a **user-wide session-name namespace** and peer messaging — all invisible to Agent Fleet's registry, mirror and usage ledger |
| Auth | △ | Browser sign-in or an API key; `META_API_KEY`, or `muse auth set`, stored at `~/.config/muse/auth.json`. An API key always wins over a browser session. Managed (MMA) accounts must use a key |
| Billing | △ | Pay-as-you-go per token, or a flat subscription in three tiers whose quota is counted in **prompts per 5 hours**, valid only through the CLI while signed in with a Meta Model API account |
| Real-turn behaviour | **× not measured** | `turn/start` came back `{"error":{"kind":"authRequired","retryable":false}}`. Token usage, the model catalogue (`model/list` answered `{"models":[],"source":"bundledCatalog"}` unauthenticated), the approval round trip and subagent events all need one credential |
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
once. AF mints one per session, keeps it on `Meta` beside the path, and `ClearResume` discards both
so a recreate starts a genuinely new session. Whether Muse accepts a v5 at all is unmeasured — the
probe passed a v7, which the schema names as the server's own default. Subagent transcripts live under
`subagent/<id>/session.jsonl`; v1 renders subagent activity as tool-shaped items on the parent turn
and does not open a pane per child.

### Decision 5 — the sandbox is turned off, and the approval gate is what remains

`muse serve --disable-sandbox`. Measured, bubblewrap cannot build a sandbox in a Workspace
container (`move_mount` → EACCES), and with the sandbox on and unusable **every shell command the
agent runs aborts as an environment failure** — the kind would not work at all. Approvals are
orthogonal and stay on: approval mode is selected per session on the wire, and AF answers
`approval/requested` through `approval/decide`, mapping the launch-time permission choice
(docs/log/76) onto `untrusted` | `on-request` | `never`.

This is a waiver, and the ADR states it as one: inside a Workspace the container **is** the
boundary, and the same is already true of every other kind, none of which sandboxes itself.

**The waiver is expected to be permanent under the current Workspace host contract, and the
measurement says why** — permanent because nothing *we* ship can change it, not because no change
is conceivable. The denial is not a
missing binary and not an artefact of one mount API: inside a user namespace this container refuses
`move_mount` (the new API, which util-linux prefers) **and** the classic `mount(2)` that bubblewrap
itself calls, both with EACCES — measured by forcing the old path with
`LIBMOUNT_FORCE_MOUNT2=always`. Baking `bwrap` therefore only confirms a result already taken; that
is what Phase 1 gate A is for, and the expected outcome is that Decision 5 becomes permanent and is
recorded as such. Only a change in what the *host* permits (LSM policy, seccomp, capabilities)
would reopen it.

### Decision 6 — AF owns `~/.config/muse/settings.json`, and clamps seven behaviours in it

The file requires `"schema_version": 1` or **every command fails at startup**, so it is written, not
merged blindly. **There is exactly one writer, and it is the MCP materialiser's.** The clamps below
and the `mcp_servers` block of Decision 11 are two blocks of the same file, and Muse writes it too
(measured: a first run created `settings.json` **and** its own `~/.config/muse/.settings.json.lock`).
Three writers on one file with two locks is a lost update, so the settings writer is a single
serialised read-merge-rename owner under the existing `materializeMu`
(`mcpreg/materialize.go:94-109`) rather than a second mutex beside it, and it **preserves every key
it does not own** — a member's own `tui`, model defaults and telemetry keys survive an AF write.

Two things follow that the first draft got wrong, and both are design work, not wording:

- **It is a settings writer, not the JSON MCP materialiser with a second block.** That materialiser
  returns early when the server set did not change (`mcpreg/materialize_json.go:110-112`) and
  removes its key entirely when the set is empty — correct for MCP, fatal for clamps, which must be
  written on a kind with zero MCP servers. Muse therefore gets its own writer inside `mcpreg` that
  merges clamps and `mcp_servers` in one pass.
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

The lock claim is deliberately weak: a `.settings.json.lock` file appearing proves Muse *has* a lock
protocol, not which one. Gate B1 traces the syscalls of a settings update and a deliberate
contention, and AF takes the same protocol — guessing `flock` because other `.lock` files use it is
how two writers end up politely ignoring each other.

AF sets:

1. `agents.execution_capacity` — a small cap. Left alone, one session may run eight agents on a
   memory-constrained shared host.
2. Background observers off. Four of them each make their own model calls; on a metered account they
   are invisible spend, and on a prompt-quota subscription they are invisible quota.
3. Workflows off (`auto` would let a session spawn up to 1,000 children).
4. Worktree isolation off, and `-w` never passed. Decision 7.
5. Foreign personal context off. Reading `~/.claude` and `~/.codex` silently mixes another kind's
   instruction layer into this one, and those directories are off-limits by workspace policy.
   **The settings file is the only route**: `--no-foreign-personal-context` exists on `muse exec`
   but **not on `muse serve`** (measured — `muse serve … --no-foreign-personal-context` answers
   `unknown option`), and Decision 2 makes `serve` the only process AF runs. The key's spelling is a
   gate B1 item; the binary carries both `allow_foreign_configuration` and
   `foreign_personal_fallback`, so it is not guessable from the flag name.
6. The approval judge off (`--approval-judge off`, or `MUSE_DISABLE_APPROVAL_JUDGE`). It is **on by
   default** and makes its own model call for every Prompt-bound approval — on a metered account
   that is spend the member never asked for, and Decision 5 keeps approvals on, so it fires often.
7. The bundled skills that read other agents. `muse skills list` ships `resume-claude`,
   `resume-codex`, `import` and `migrate`, whose stated job is to read Claude Code's or Codex's
   transcripts, memory notes and MCP servers. Clamp 5 governs *discovery* of foreign rules and
   skills; these are foreign readers invoked on demand, and they reach exactly the directories
   workspace policy puts off-limits.

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

### Decision 8 — deployment: bake the pinned binary, guard the shadow in `~/.local/bin`

The release manifest publishes url + sha256 + size per platform, so the established pattern applies
unchanged: `ARG MUSE_VERSION` + sha256 per arch, verified at build, the runtime laid out as the
launcher expects (`muse-bin-<version>` plus `.muse-version` beside the launcher) under
`/usr/local/share/muse`, `MUSE_NO_AUTO_UPDATE=1` exported by the entrypoint, and
`env_tool_versions.go` gaining a row. Measured, that layout survives a read-only directory.

Two costs are named rather than discovered later. **Image size**: ≈ 299 MiB (x86_64) / ≈ 269 MiB
(aarch64) per image — well under the 855 MiB that pushed kiro to on-demand installation, but not
free. **The shadow**: the vendor installer's default target is `~/.local/bin/muse`, which wins on
PATH and survives a recreate — the same trap a stale `~/.local/bin/workspace-agent` set for the
Agent. A member who runs the one-line installer once pins themselves to an unmanaged, self-updating
build for good, so the connection card reports the shadow when it sees one.

### Decision 9 — the credential is a stored API key, entered once

**`muse auth set --api-key-stdin`** — the flag is not optional. `muse auth --help` prints the usage
as `muse auth set [--provider <PROVIDER>] --api-key-stdin`, and `muse auth set --help` lists it as
the one accepted way to pass a secret, "never taken as a command-line argument, so it never lands
in shell history" (both measured on 1.3.0). It writes `~/.config/muse/auth.json`.

**Two paths join the file deny-list, not one**: `~/.config/muse` (the credential) *and*
`~/.local/share/muse` — which holds every session's full transcript and the user-wide session-name
authority. The precedent is exact: `fs.go:131-137` already denies `.local/share/opencode`, `.codex`
and `.kiro` for the same reason, the credential *and* the session store.

Not `META_API_KEY` in the child's environment: the reason cursor refused env
injection (ADR 0023) applies unchanged, and an API key always overrides a stored session, so an
env-level key would silently defeat a member who later signs in. The connection card is therefore
**one input** — simpler than every kind except none — with `muse logout` behind the disconnect.

Open: a member on a Muse Code *subscription* (as opposed to pay-as-you-go) signs in through the
browser during CLI onboarding, which is a flow this container does not have. Phase 1 gate **B2**
decides whether a subscription credential can be produced elsewhere and pasted, or whether
subscriptions are out of scope for v1.

### Decision 10 — usage and the model list ride the protocol

`usage/read`, `session/tokenUsage` and `session/contextUsage` exist on the wire, so `muse` is
expected to enter `usageMeasuredForKind`'s **exact** set (`usage_fold.go:206`) — but an unmeasured
"exact" is precisely the lie that switch exists to prevent, and **one successful turn is not the
evidence that settles it**. The gate is the accounting matrix of Phase 1 gate B1: cached input
counted separately, subagent and observer calls attributed (or provably excluded), a failed and an
interrupted turn, the numbers after `session/resume` (no double count), and whether the values are
cumulative or per-turn — folding a cumulative counter as a delta is how a ledger silently doubles.
Anything short of that lands `MeasuredPartial`, which is honest, rather than `MeasuredExact`, which
would not be.
**Two chips have no source yet, and the ADR should not pretend otherwise.** The cost estimate reads
a kind → models.dev provider table (`workspace/agent/usage_catalog.go:48-55`) that has no Meta row,
so a price per token has to come from somewhere before a spend figure can be shown. And the
subscription's quota is counted in prompts per five hours, which is the shape of `get_agent_usage`
(claude / codex / agy), not of a token ledger — and Muse exposes it through `/upgrade` in the TUI,
a surface `serve` does not have. v1 may well ship with a token ledger and no cost or quota chip;
that is a decision for Phase 2, named here so it is not discovered as a missing feature.

`model/list` backs the picker, which means `agentModels.ts:34-35`'s `isDynamic` must list `muse`.
That one line is the recurring miss of every new kind (copilot, cursor), and it presents as "the
model picker only shows the default".

### Decision 11 — MCP: a new dialect that writes into a shared settings file

`mcp_servers` is a block inside the same `settings.json`, with `transport: stdio | streamable_http`,
`command`/`args`/`env` or `url`/`headers`, `enabled`, and **`mode`, which defaults to `required` —
a required server that fails to start aborts the whole run**. AF materialises **every** server as
`mode: optional`, with no member-facing choice: a broken tenant server must not make the agent
refuse to start, and the registry has nowhere to put the choice — `secrets.MCPServer`
(`workspace/agent/internal/secrets/secrets.go:302-324`) has `enabled`, `targets`, `kinds` and
`timeoutMs` but no `mode`, so offering it means a new field plus wire, Console and stored-definition
migration. That is named as out of scope here rather than discovered in Phase 2.

**There may be a second route, and Phase 1 decides between them.** `session/start` and
`session/resume` take a `config` object whose only admitted member today is `mcpServers`
(`SessionConfig`, measured in the schema). If a per-session server list on the wire is honoured,
MCP never has to enter the shared settings file at all — the clamps still do, but the writer stops
being a merge of two independent blocks, and the "settings writer + MCP dialect" work package
shrinks. Gate B1 sends one server both ways and keeps whichever the runtime actually connects.

`${VAR}` interpolation exists and `MUSE_SESSION_ID` is passed to stdio servers. `muse` joins
`knownKinds` (`mcpreg/def.go:57-61`) and `MaterializedKinds` (`materialize.go:47`). For project
scope it joins `mcpproj`'s `kindInfos` (`mcpproj/inspect.go:50-58`) with
**`HasProjectScope: false`** — the shape agy already has — because no Muse project-scope spelling is
documented. `fileSpecs` (`inspect.go:35-43`) gains no row, so muse is neither inspected nor a copy
target; that is a static fact about the kind, not a runtime fallback to another kind's file.
Hooks (`.muse/hooks.json`, 15 lifecycle events including `Stop` and `Notification`) are **not** used:
the protocol already reports what a hook would, and a hook file in the repo is shared state.

### Decision 12 — the project layer costs one host-wide decision (`--trust-workspace`); the user and fleet layers are a measured gap, and `instrSupportedKinds` waits for them

Project scope is **not** free, which the first two drafts had wrong. Muse reads the repository's own
`AGENTS.md` / `CLAUDE.md` only after the workspace is trusted, and measured, an untrusted session
says so and carries on without them: `rules file at …/AGENTS.md exists, but the workspace is
untrusted, so it is skipped for this session; restart with --trust-workspace`. Trust is **not on the
wire** — the string `trust` does not occur anywhere in the MSP schema (measured, 0 occurrences) —
so the only way to have project rules is `muse serve --trust-workspace`, which is host-wide and
therefore, under Decision 3's one-host-per-session shape, a per-session decision AF makes when it
spawns the child.

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

**Where Muse's targets are is not established.** Probing an echo-provider run, trusted and
untrusted, for the paths it opens produced only a probe for a project-local `.agents` directory,
because rule assembly does not run on that provider. So `muse` joins `instrSupportedKinds` **only
once both targets are measured, separately** (Phase 1 gate B1, in the same run as the accounting
matrix), and Phase 2 carries both apply paths and the Console's per-kind distribution status;
until then the kind ships with project instructions only, and the guide says so rather than leaving
a member to assume the fleet policy reached it.

### Decision 13 — the capability declaration: the first kind with every dynamic axis true, and a question channel that is not the approval channel

`Capabilities` is not just `ProcessModel`, and the earlier drafts decided only that. MSP carries
`session/setModel`, `session/setReasoningEffort` and `session/setApprovalMode` with matching
`*Changed` notifications (measured in the schema), and `muse --help` exposes
`--reasoning-effort none|minimal|low|medium|high|xhigh|max|ultra`. So `DynamicModel`,
`DynamicEffort` and `DynamicMode` are all true, alongside `Steer` (`turn/steer`), `Fork`
(`session/fork`) and `Permissions` (`approval/*`) — **the first kind for which every one of them
holds**. `Meta.Effort` and the Console's effort and mode controls therefore light up on day one
rather than being dark fields.

`Questions` is separately true and is a *different channel from approvals*:
`userInput/requested` → `userInput/answer`, with `userInput/settled` closing it. An approval asks
"may I run this"; a user-input request asks the member a question. Mapping both onto the mirror's
permission card would lose that distinction, so the driver declares both `Permissions` and
`Questions` and routes them to the two interaction kinds AF already has.

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
| Usage | `usage_fold.go:204-212`, the cost table `usage_catalog.go:48-55`, the usage stack colour `console/src/features/usage/colors.ts:81` |
| Instructions | both apply paths in `agent_instructions.go:119-146` and the list at `:83`, once Decision 12's targets are measured |
| MCP — **four** separate lists | the registry (`mcpreg/def.go:57-61`, `mcpreg/materialize.go:47,74-87`, `mcpproj/inspect.go:36-44,50-58`); the **local** `af` server (`mcpx/mcp_stdio.go:1853`, kind validation `:2575-2576`, managed-driver list `:2759-2762`); the **CP** MCP tools (`control-plane/internal/mcpsrv/mcp.go:295,473,487,518-550` — description, schema and runtime validation are three edits, not one); and `mcpsrv/mcp_server.go:70-74`'s `mcpKnownKinds`, a fourth copy of the same list. Tool descriptions are a fixed per-session token cost, so they are edited, not grown |
| Console surface | `console/src/types/session.ts:9-12` (`SessionKind` and the display order — nothing renders without it), `console/src/agents/registry.ts` descriptor, `console/src/lib/settings.ts:958-966` launch defaults, the `LaunchDefaults` kind union `console/src/features/settings/agents/AgentCardParts.tsx:76`, a new `MuseCard.tsx` wired from `features/settings/agents/AgentsTab.tsx:250`, `features/settings/workspace/EnvTab.tsx`, `console/src/features/settings/mcp/mcpWire.ts:9` (`MCP_KINDS`, mirrors the Go list), `ScheduleDetailModal.tsx`'s `AGENT_KINDS`, `features/mirror/{turnTime.ts:10,FileChangeStrip.tsx:69}`, `features/repos/ProjectActionPanels.tsx:34`, `console/src/lib/brandicons.ts:36` plus the icon asset itself, `console/src/lib/termcolor.ts:19`, and the colour twins across `tokens.css` and the five feature stylesheets (docs/log/74 §9.3) |
| Deployment + CI | `workspace/Dockerfile` (ARG, sha256 per arch, runtime layout), the entrypoint's `MUSE_NO_AUTO_UPDATE` and shadow check, `env_tool_versions.go`, and the release / drift workflows and setup action that carry every other pinned CLI |
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
- **On-demand per-user installation (the kiro shape).** Justified at 855 MiB, not at 299, and it would
  put a self-updating binary in the member's home — the very shadow Decision 8 guards against.
- **Adopting now on the strength of the protocol.** The three gates below are cheap and every one of
  them is about something no amount of reading settles.

## Consequences

- **What the kind gets for free that others paid for**: no TUI string-contract tests, no hook file, no
  polling state detection, no session-id discovery, no transcript reverse-engineering, and — because
  `--provider echo` exists — a harness that can be built and regression-tested **without a credential
  or a token budget**. Of the nine existing kinds, none had all six.
- **What it owes**: the sandbox waiver (Decision 5), a settings file it must not corrupt (Decision 6),
  a vendor whose feature set overlaps ours (Decision 6, and a guide section explaining what AF does
  not see), and a beta that moved 1.2 → 1.3 in one month.
- **Drift has a lock**: `muse schema` is offline and the release manifest carries
  `msp_schema_fingerprint`. A test asserting the baked binary's fingerprint equals the one the
  generated types were built from turns a silent protocol change into a red build. No other kind has
  this.
- **Estimate**: managed-only, no TUI assets, **20–31 session-days** — the sum of the table, with no
  rounding applied to make a tidier headline. It has moved twice, and both moves are the point: the
  first draft said 14–20 while its own rows added to 15–23, and the rows themselves were then short
  of the work the reviews found (the fail-close wiring, the seven clamps, the question channel, the
  dynamic axes, the instruction layers, the CI). The anchor
  for the scale is the kind-wide inventory — 90 Go files and 41 Console files mention `kiro` today,
  and existing kinds are 2,100–5,950 non-test lines each — but the split below is what the number
  is made of, and it is the part to argue with:

  | Work package | Days |
  |---|---|
  | MSP client, generated types, the fingerprint drift test | 3–4 |
  | Driver + `ThreadHandle` (7 methods), status, steering, interrupt, resume reconciliation | 3–4 |
  | Transcript: live items + the at-rest JSONL, subagent items | 2–3 |
  | Settings single writer + 7 clamps + the fail-close wiring in `Resume` + the MCP dialect | 3–4 |
  | Approval round trip, the separate `userInput` question channel, the permission-choice mapping | 2–3 |
  | The dynamic axes of Decision 13 (model / effort / mode) and their Console controls | 1–2 |
  | Connection card, both `routes.go`, the REST allow-list, deny-list | 1–2 |
  | Usage + model list + the accounting tests | 1–2 |
  | Instruction layers: both apply paths and the distribution status (Decision 12) | 1–2 |
  | Deployment (bake, pin, sha256, shadow guard, `env_tool_versions`, release / drift CI) | 1–2 |
  | Console surface, i18n, guide, `guide/ref` capability tables | 2–3 |

  **21–33 days if ADR 0093 has not landed** when this starts, because the managed-only gate
  (Decision 2) is then unpaid. The direction of the error is still upward: the largest single
  unknown is how much of MSP's 47 methods and 31 notifications the driver actually has to implement
  to be correct rather than merely working, and three review rounds have each moved the number the
  same way.
- If Phase 1's gates fail, the sunk cost is this ADR and the probe — no code.

## Phases

| Phase | Content | Gate to the next |
|---|---|---|
| 0 | The probe in this ADR (done 2026-09-20): install, MSP drive, pin/checksum, worktree and foreign-context behaviour, footprint | — |
| 1 | **Gate A** (½ day): bake `bwrap` and run it on a real Workspace image. Both mount APIs are already denied here, so this confirms rather than explores; the expected answer is that Decision 5's waiver is permanent and recorded. **Gate B1** (2–2½ days): one API key, and the accounting matrix of Decision 10 — cache, subagents, a failed and an interrupted turn, post-resume, cumulative vs per-turn — plus `model/list`, one `approval/requested` round trip, one `userInput/requested` round trip, one subagent, the fleet and user instruction targets of Decision 12 (separately), the settings keys for the seven clamps of Decision 6 (their spelling is not guessable from the flag names), the settings-file lock protocol by syscall, whether `session/start.config.mcpServers` is honoured (Decision 11's second route), and what a **normal** run — no `-w` — writes into a working copy, since Decision 7 currently rests on the `-w` measurement alone. **Gate B2** (½ day): can a *subscription* credential be obtained elsewhere and entered here, given the browser onboarding this container cannot run | A and B1 answered; the user accepts the Decision 6 clamps and the spend. B2 may answer "no" — then v1 is pay-as-you-go only, stated in the guide, and Phase 2 proceeds |
| 2 | Implementation: kind wiring, MSP client and generated types, driver, transcript, usage, settings + MCP dialect, connection card, deployment, guide, this ADR to *adopted* | — |

## Open questions (answer in Phase 1)

1. The accounting matrix of Decision 10, in full — not one turn. It decides `MeasuredExact` versus
   `MeasuredPartial`, and a wrong answer here is a ledger that silently doubles.
2. Where Muse reads **user-scope** rules, so Decision 12 can put `muse` in `instrSupportedKinds`.
   Not settled by the probe: the echo provider never assembles rules.
3. Subscription vs pay-as-you-go (gate B2): can a subscription credential exist without the browser
   onboarding this container cannot run? If not, v1 is pay-as-you-go only and the guide says so.
4. Does `model/list` return a catalogue once authenticated, and is it plan-dependent the way copilot's
   and cursor's are (the "named models unavailable on Free" class of failure)?
5. How an approval renders: `approval/requested` carries staged shell review data; which of it the
   mirror's permission card can show without a new card type.
6. Whether `session/list` on a per-session host can see other AF sessions' Muse sessions (one store,
   one user) — and if so, that the Console never offers them.

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
(Decision 13 — muse is the first kind with every dynamic axis true, and `userInput/*` is a question
channel distinct from approvals), the approval judge's per-approval model call and the bundled
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

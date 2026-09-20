# 0095. Meta's Muse Code as a session kind (`muse`) — a vendor protocol instead of a TUI contract, behind three live gates

English | [日本語](0095-muse-agent-kind.ja.md)

- Status: **proposed** (2026-09-20), **Phase 1 gate A answered** (2026-09-20 — the last section).
  None of the kind itself is implemented; gate A landed only the deployment changes its measurement
  needed. Every `file:line` below was read on
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
| **OS sandbox** | **🔴 ◎ cannot run here, permanently** | Linux sandboxing is bubblewrap (38 `bwrap` / 50 `seccomp` strings in the binary; the docs say "needs a working bubblewrap and a non-musl build. Without it, every sandboxed shell command aborts as an environment failure"). Gate A settled it with a real `bwrap`: a user namespace is creatable and grants all 41 capabilities, yet **`mount(2)` and `move_mount(2)` return EACCES regardless of capabilities** while `fsopen` / `open_tree` succeed — the signature of **AppArmor's `docker-default` profile**, not of seccomp or capabilities. Muse also ships an *embedded* bwrap, so the binary's absence was never the blocker |
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
once. AF mints one per session and keeps it on `Meta` beside the path. What discards it is **not**
`ClearResume` — that method is never called outside its own interface declaration and one agy test —
but the fact that recreate and fork build a fresh `session.Meta` from an explicit whitelist of
fields (`sessionx/session_handlers.go:1381-1390` and `:1083-1098`), so a field nobody lists is
structurally not inherited. The inverse is the thing to guard: a future path that copies `Meta`
wholesale would carry a single-use id forward and make the next `session/start` fail with
`session_id_conflict`. Whether Muse accepts a v5 at all is unmeasured — the
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

### Decision 6 — AF owns `~/.config/muse/settings.json`, and clamps seven behaviours in it

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
  ⚠️ **It is not unconditional.** Gate A shipped it as an explicit opt-in
  (`AF_MUSE_BOOT_INSTALL=1`, default off) because 299 MiB on every fresh container, for a kind that
  does not exist before Phase 2, is the same bill that already moved kiro (855 MiB) off
  unconditional boot-install. **Phase 2 should follow kiro the rest of the way** — a per-user
  on-demand `workspace-agent install-muse` — rather than flip this flag on.
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
a kind → models.dev provider table (`workspace/agent/usage_catalog.go:45-55`) that has no Meta row,
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
documented. `fileSpecs` (`inspect.go:36-44`) gains no row, so muse is neither inspected nor a copy
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

**Where Muse's targets are is not established.** Probing an echo-provider run, trusted and
untrusted, for the paths it opens produced only a probe for a project-local `.agents` directory,
because rule assembly does not run on that provider. So `muse` joins `instrSupportedKinds` **only
once both targets are measured, separately** (Phase 1 gate B1, in the same run as the accounting
matrix), and Phase 2 carries both apply paths and the Console's per-kind distribution status;
until then the kind ships with project instructions only, and the guide says so rather than leaving
a member to assume the fleet policy reached it.

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
| Instructions | both apply paths in `agent_instructions.go:119-146` and the list at `:83`, once Decision 12's targets are measured |
| MCP — **four** separate lists | the registry (`mcpreg/def.go:57-61`, `mcpreg/materialize.go:47,74-87`, `mcpproj/inspect.go:36-44,50-58`); the **local** `af` server (`mcpx/mcp_stdio.go`: the `list_models` descriptor, the `a.Kind != …` validation, and the `driver = "managed"` list — cited by symbol because these three moved by six lines between `06ea94d3` and `73ac5cdc`); the **CP** MCP tools (`control-plane/internal/mcpsrv/mcp.go:295,473,487,518-550` — description, schema and runtime validation are three edits, not one); and `mcpsrv/mcp_server.go:70-74`'s `mcpKnownKinds`, a fourth copy of the same list. Tool descriptions are a fixed per-session token cost, so they are edited, not grown |
| Console surface | `console/src/types/session.ts:9-12` (`SessionKind` and the display order — nothing renders without it), `console/src/agents/registry.ts` descriptor, `console/src/lib/settings.ts:958-966` launch defaults, the `LaunchDefaults` kind union `console/src/features/settings/agents/AgentCardParts.tsx:76`, a new `MuseCard.tsx` wired from `features/settings/agents/AgentsTab.tsx:250`, `features/settings/workspace/EnvTab.tsx`, `console/src/features/settings/mcp/mcpWire.ts:9` (`MCP_KINDS`, mirrors the Go list), `ScheduleDetailModal.tsx`'s `AGENT_KINDS`, `features/mirror/{turnTime.ts:10,FileChangeStrip.tsx:69}`, `features/repos/ProjectActionPanels.tsx:34`, `console/src/lib/brandicons.ts:36` plus the icon asset itself, `console/src/lib/termcolor.ts:19`, and the colour twins across `tokens.css` and the five feature stylesheets (docs/log/74 §9.3) |
| Deployment + CI | both variants of Decision 8: **`/usr/local/share/agent-fleet/versions.json`** (the only place the lean pin lives), the entrypoint's boot-install and re-pin (`entrypoint.sh:299-352`, plus `MUSE_NO_AUTO_UPDATE` and the version-identity check) and `workspace/Dockerfile`'s `BAKE_AGENT_CLIS=1` path, `env_tool_versions.go`, **`NOTICE`** (proprietary CLIs are listed there, not bundled), and the release / drift workflows and setup action that carry every other pinned CLI |
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
- **Per-user on-demand installation of the kiro shape.** The location is not the difference — the
  shipped variant also installs under `~/.local` (Decision 8). The difference is **pinning**: kiro's
  bundle self-updates and is installed on the member's demand, while the boot-install re-pins to
  `versions.json` on every start. An unpinned, self-updating agent binary is what is rejected here,
  not a home directory.
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
| 1 | **Gate A — ✅ done 2026-09-20** (see the gate A section): `bwrap` baked and run; the waiver is permanent and the denier is named (AppArmor `docker-default`), plus two premises corrected (muse embeds its own bwrap; Debian's lacks `--ro-bind-symlink`). Decision 8's shipped path ran end to end — sha256 verified, `muse --version` matching the pin, shadow repin both ways — and turned up a real defect: the sha256 check was decorative at all five boot-install sites. **Gate B1** (2–2½ days): one API key, and the accounting matrix of Decision 10 — cache, subagents, a failed and an interrupted turn, post-resume, cumulative vs per-turn — plus `model/list`, one `approval/requested` round trip, one `userInput/requested` round trip, one subagent, **the RSS of a host under load** (Decision 3 currently rests on an idle 73 MiB and its own re-evaluation clause asks for the loaded figure), **the observed effect of each of the seven clamps** rather than only the spelling of their keys — a key that writes but does not bite is worse than no clamp, because Decision 6 puts a fail-close in front of it — the fleet and user instruction targets of Decision 12 (separately), the settings keys for the seven clamps of Decision 6 (their spelling is not guessable from the flag names), the settings-file lock protocol by syscall, whether `session/start.config.mcpServers` is honoured (Decision 11's second route), and what a **normal** run — no `-w` — writes into a working copy, since Decision 7 currently rests on the `-w` measurement alone. **Gate B2** (½ day): can a *subscription* credential be obtained elsewhere and entered here, given the browser onboarding this container cannot run. ⚠️ B1 carries nine items and the lock-protocol trace alone is half a day; if it overruns, the items that may move to Phase 2 are the MCP second route and the clamp key spellings, never the accounting matrix | A and B1 answered; the user accepts the Decision 6 clamps and the spend. B2 may answer "no" — then v1 is pay-as-you-go only, stated in the guide, and Phase 2 proceeds |
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
7. Which path `session/start.workspaceRoot` gets when the session has a `Meta.Subdir`: the working
   copy or the subdirectory. It decides what `--trust-workspace` covers, and `--allow-workspace-switch`
   exists, so the wrong answer is recoverable but confusing.

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

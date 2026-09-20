# 0095. Meta's Muse Code as a session kind (`muse`) — a vendor protocol instead of a TUI contract, behind two live gates

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
| **Foreign personal context** | **⚠️ ◎ on by default** | First run printed `Including your Codex personal rules and 5 skills`. It discovers `~/.claude` and `~/.codex` skills and rules unless `--no-foreign-personal-context` is passed |
| Feature overlap | ⚠️ △ | Subagents (8 per tree by default, `agents.execution_capacity` 1–64), four background observer agents that each make their own model calls, workflows (1,000 children lifetime), a **user-wide session-name namespace** and peer messaging — all invisible to Agent Fleet's registry, mirror and usage ledger |
| Auth | △ | Browser sign-in or an API key; `META_API_KEY`, or `muse auth set`, stored at `~/.config/muse/auth.json`. An API key always wins over a browser session. Managed (MMA) accounts must use a key |
| Billing | △ | Pay-as-you-go per token, or a flat subscription in three tiers whose quota is counted in **prompts per 5 hours**, valid only through the CLI while signed in with a Meta Model API account |
| Real-turn behaviour | **× not measured** | `turn/start` came back `{"error":{"kind":"authRequired","retryable":false}}`. Token usage, the model catalogue (`model/list` answered `{"models":[],"source":"bundledCatalog"}` unauthenticated), the approval round trip and subagent events all need one credential |
| TUI text contract | × | Not measured, and Decision 2 makes it unnecessary |

### What the repository already has

- Nine kinds (`workspace/agent/internal/session/session.go:20-28`), registered twice:
  `sessionx/agent.go:28-38` for the read layer and `sessionx/session_turn.go:31-37` for the five
  managed drivers. **An unregistered kind is silently normalised to claude**, not rejected
  (`sessionx/agent.go:41-53`).
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
`--sandbox-network`, **`--disable-write`**, **`--disable-shell`**, **`--trust-workspace`** and
session durability. Agent Fleet varies exactly those per session — the launch flow's permission
choice and read-only/plan postures are session-level decisions — so a shared host would make one
session's posture the posture of every session started after it. Two lesser reasons follow: a host
crash would take every session on it, and stopping one session would mean draining a process that
others are loaded in. At **73 MiB idle** (measured) a child per session is affordable — a third of
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
the trap kiro hit with cwd+mtime, ADR 0026 decision 6). Subagent transcripts live under
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

**The waiver is expected to be permanent, and the measurement says why.** The denial is not a
missing binary and not an artefact of one mount API: inside a user namespace this container refuses
`move_mount` (the new API, which util-linux prefers) **and** the classic `mount(2)` that bubblewrap
itself calls, both with EACCES — measured by forcing the old path with
`LIBMOUNT_FORCE_MOUNT2=always`. Baking `bwrap` therefore only confirms a result already taken; that
is what Phase 1 gate A is for, and the expected outcome is that Decision 5 becomes permanent and is
recorded as such. Only a change in what the *host* permits (LSM policy, seccomp, capabilities)
would reopen it.

### Decision 6 — AF owns `~/.config/muse/settings.json`, and clamps five behaviours in it

The file requires `"schema_version": 1` or **every command fails at startup**, so it is written, not
merged blindly. **There is exactly one writer, and it is the MCP materialiser's.** The clamps below
and the `mcp_servers` block of Decision 11 are two blocks of the same file, and Muse writes it too
(measured: a first run created `settings.json` **and** its own `~/.config/muse/.settings.json.lock`).
Three writers on one file with two locks is a lost update, so the settings writer is a single
serialised read-merge-rename owner: it extends the existing `materializeMu`
(`mcpreg/materialize.go:94-109`) rather than adding a second mutex beside it, takes Muse's own lock
file while it writes, and **preserves every key it does not own** — a member's own `tui`, model
defaults and telemetry keys survive an AF write.

AF sets:

1. `agents.execution_capacity` — a small cap. Left alone, one session may run eight agents on a
   memory-constrained shared host.
2. Background observers off. Four of them each make their own model calls; on a metered account they
   are invisible spend, and on a prompt-quota subscription they are invisible quota.
3. Workflows off (`auto` would let a session spawn up to 1,000 children).
4. Worktree isolation off, and `-w` never passed. Decision 7.
5. Foreign personal context off (`--no-foreign-personal-context`, or its settings equivalent).
   Reading `~/.claude` and `~/.codex` silently mixes another kind's instruction layer into this one,
   and those directories are off-limits by workspace policy.

Muse's own peer messaging and session-name authority (`~/.local/share/muse/session-name-authority/`,
user-wide) are **not** wired to AF's cross-session messaging in v1: two peer channels with one
namespace, one of them invisible in the mirror, is how `native-peer-channel-invisible-in-mirror`
happened. The guide says they exist and that AF does not see them.

### Decision 7 — the kind edits working-copy files and nothing else: no branches, no worktrees, no repository-wide metadata

Editing tracked files in its own working copy is the job, and is not what this decision restrains.
What it forbids is everything *around* the files: repository-global metadata, branches, worktrees
and any path outside this session's working copy. Measured, a Muse worktree run rewrites
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

**`muse auth set --api-key-stdin`** — the flag is not optional, the binary's own usage is
`muse auth set [--provider <PROVIDER>] --api-key-stdin` and the key "is never passed on the command
line" (measured on 1.3.0). It writes `~/.config/muse/auth.json`.

**Two paths join the file deny-list, not one**: `~/.config/muse` (the credential) *and*
`~/.local/share/muse` — which holds every session's full transcript and the user-wide session-name
authority. The precedent is exact: `fs.go:131-137` already denies `.local/share/opencode`, `.codex`
and `.kiro` for the same reason, the credential *and* the session store.

Not `META_API_KEY` in the child's environment: the reason cursor refused env
injection (ADR 0023) applies unchanged, and an API key always overrides a stored session, so an
env-level key would silently defeat a member who later signs in. The connection card is therefore
**one input** — simpler than every kind except none — with `muse logout` behind the disconnect.

Open: a member on a Muse Code *subscription* (as opposed to pay-as-you-go) signs in through the
browser during CLI onboarding, which is a flow this container does not have. Phase 1 gate B decides
whether a subscription credential can be produced elsewhere and pasted, or whether subscriptions are
out of scope for v1.

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

`${VAR}` interpolation exists and `MUSE_SESSION_ID` is passed to stdio servers. `muse` joins
`knownKinds` (`mcpreg/def.go:57-61`) and `MaterializedKinds` (`materialize.go:47`). For project
scope it joins `mcpproj`'s `kindInfos` (`mcpproj/inspect.go:50-58`) with
**`HasProjectScope: false`** — the shape agy already has — because no Muse project-scope spelling is
documented. `fileSpecs` (`inspect.go:35-43`) gains no row, so muse is neither inspected nor a copy
target; that is a static fact about the kind, not a runtime fallback to another kind's file.
Hooks (`.muse/hooks.json`, 15 lifecycle events including `Stop` and `Notification`) are **not** used:
the protocol already reports what a hook would, and a hook file in the repo is shared state.

### Decision 12 — the project instruction layer is free; the user and fleet layers are a measured gap, and `instrSupportedKinds` waits for it

Project scope needs nothing: Muse reads the repository's own `AGENTS.md` / `CLAUDE.md` (its
documented discovery order is `AGENTS.md`, `CLAUDE.md`, `.agents/AGENTS.md`, `.claude/CLAUDE.md`,
after the workspace is trusted), which this repository already has.

The other two layers are not solved by Decision 6's switch. Turning off foreign personal context
stops Muse reading `~/.claude` and `~/.codex`; it does **not** deliver Agent Fleet's fleet policy or
the member's own instructions, which for the six kinds in `instrSupportedKinds`
(`workspace/agent/agent_instructions.go:83`) are written into a per-kind user-scope file. **Where
that file is for Muse is not established**: probing an echo-provider run, trusted and untrusted, for
the paths it opens produced only a probe for a project-local `.agents` directory, because rule
assembly does not run on that provider. So `muse` joins `instrSupportedKinds` **only once the
user-scope target is measured** (Phase 1 gate B1 does it in the same run as the accounting matrix);
until then the kind ships with project instructions only, and the guide says so rather than leaving
a member to assume the fleet policy reached it.

## What Phase 2 touches

The checklist below is the one docs/log/43 §4 and docs/log/74 §8 turned into a rule after two kinds
shipped with pieces missing; every anchor was re-read on `06ea94d3`. It is the estimate's basis, and
none of it is optional.

| Area | Sites |
|---|---|
| Kind identity | `session.go:20-28`, `sessionx/agent.go:28-38`, `sessionx/session_turn.go:31-37` (managed driver map) |
| Managed-only gate | `sessionx/session_handlers.go:650-668` (create default), `session_driver.go:62-105`, five Console launch / driver-switch sites, `control-plane/scheduler_wake.go:277-285` |
| Connection + login | connection status, login routes on **both** `routes.go` files (Agent and CP — `control-plane/routes.go:869-873` is kiro's precedent), and the CP REST proxy allow-list, whose omission is how a usage chip silently never appears |
| Model + vendor | `console/src/lib/agentModels.ts:34-35` (`isDynamic`), `workspace/agent/model_provider.go:122` (`modelKindVendor`) |
| Usage | `usage_fold.go:204-212`, the usage stack colour `console/src/features/usage/colors.ts:81` |
| MCP | `mcpreg/def.go:57-61`, `mcpreg/materialize.go:47,74-87`, `mcpproj/inspect.go:50-58`, and the kind enums inside the `af` MCP tool descriptions (`control-plane/internal/mcpsrv/mcp.go:295,473,487,518`) — which are a fixed per-session token cost, so they are edited, not grown |
| Console surface | `console/src/agents/registry.ts` descriptor, `console/src/lib/settings.ts:963` launch defaults, `console/src/lib/brandicons.ts:36`, `console/src/lib/termcolor.ts:19`, `console/src/styles/tokens.css` (dark + light twins) |
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
- **Adopting now on the strength of the protocol.** The two gates below are cheap and both are about
  things no amount of reading settles.

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
- **Estimate**: managed-only, no TUI assets, ≈ **14–20 session-days**, by work package. The anchor
  for the scale is the kind-wide inventory — 90 Go files and 41 Console files mention `kiro` today,
  and existing kinds are 2,100–5,950 non-test lines each — but the split below is what the number
  is made of, and it is the part to argue with:

  | Work package | Days |
  |---|---|
  | MSP client, generated types, the fingerprint drift test | 3–4 |
  | Driver + `ThreadHandle` (7 methods), status, steering, interrupt, resume reconciliation | 3–4 |
  | Transcript: live items + the at-rest JSONL, subagent items | 2–3 |
  | Settings single writer + clamps + the MCP dialect | 2–3 |
  | Approval round trip and the permission-choice mapping | 1–2 |
  | Connection card, both `routes.go`, the REST allow-list, deny-list | 1–2 |
  | Usage + model list + the accounting tests | 1–2 |
  | Deployment (bake, pin, sha256, shadow guard, `env_tool_versions`) | 1 |
  | Console surface, i18n, guide, `guide/ref` capability tables | 1–2 |

  **Add 1–2 days if ADR 0093 has not landed** when this starts, because the managed-only gate
  (Decision 2) is then unpaid. The direction of the error is upward: the largest single unknown is
  how much of MSP's 47 methods and 31 notifications the driver actually has to implement to be
  correct rather than merely working.
- If Phase 1's gates fail, the sunk cost is this ADR and the probe — no code.

## Phases

| Phase | Content | Gate to the next |
|---|---|---|
| 0 | The probe in this ADR (done 2026-09-20): install, MSP drive, pin/checksum, worktree and foreign-context behaviour, footprint | — |
| 1 | **Gate A** (½ day): bake `bwrap` and run it on a real Workspace image. Both mount APIs are already denied here, so this confirms rather than explores; the expected answer is that Decision 5's waiver is permanent and recorded. **Gate B1** (1–1½ days): one API key, and the accounting matrix of Decision 10 — cache, subagents, a failed and an interrupted turn, post-resume, cumulative vs per-turn — plus `model/list`, one `approval/requested` round trip, one subagent, and the user-scope instruction target of Decision 12. **Gate B2** (½ day): can a *subscription* credential be obtained elsewhere and entered here, given the browser onboarding this container cannot run | A and B1 answered; the user accepts the Decision 6 clamps and the spend. B2 may answer "no" — then v1 is pay-as-you-go only, stated in the guide, and Phase 2 proceeds |
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

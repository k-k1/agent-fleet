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
- Versions are pinned as Dockerfile `ARG`s (`workspace/Dockerfile:318-320` for the three npm CLIs,
  `:404-411` for cursor's versioned tarball) and surfaced by `workspace/agent/env_tool_versions.go`.

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
kinds before the build, the way docs/log/74 §8.2 settled the third blue; `KIND_STACK_ORDER`
(`colors.ts:81`) must not place three blues adjacently.

### Decision 2 — `muse` is managed-only: MSP over stdio, no Terminal (CLI) route

MSP already carries everything a pane would give us and more (statuses, approvals, steering, fork,
usage, skills, model list), so a tmux pane would buy a second UI and a string contract to maintain.
This is the same shape ADR 0093 Decision 2 proposes for `lcpp`; the two share one cost — the
"no terminal route" gate is five Console sites plus the server-side managed→TUI transition
(`console/src/features/repos/{LaunchModal.tsx,StartModal.tsx,RepoRowConnected.tsx}`,
`console/src/features/sessions/{SessionMenu.tsx,useSessionActions.tsx}`,
`workspace/agent/internal/sessionx/session_driver.go:62-105`). **Whichever of 0093 and 0095 lands
first pays for it; the second gets it free.** If neither has landed when this kind starts, the gate
is in this kind's estimate.

### Decision 3 — `per-session-child`, one `muse serve` per session

MSP hosts several sessions per process (`session/list`, per-connection auto-subscribe), so a shared
daemon is possible. v1 does not take it, for two reasons that are in the protocol's own words: the
host's **sandbox posture and session durability are fixed for its lifetime and are not negotiable
over the wire** (`muse serve --help`), and one AF session's workspace root, deny-list and approval
posture must never be decided by another's. At **73 MiB idle** (measured) a child per session is
affordable — a third of claude's pane. `Capabilities.ProcessModel = "per-session-child"`, the same
value cursor, kiro and copilot already use, so no enum change is needed.

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
boundary, and the same is already true of every other kind, none of which sandboxes itself. It is
re-examined if `bwrap` is ever baked and the host allows it (Phase 1 gate A).

### Decision 6 — AF owns `~/.config/muse/settings.json`, and clamps five behaviours in it

The file requires `"schema_version": 1` or **every command fails at startup**, so it is written, not
merged blindly, and read-modify-write goes through the store discipline (a bare `os.WriteFile` loses
a concurrent session's keys). AF sets:

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

### Decision 7 — nothing this kind does may touch the repository or another session's desk

Measured, a Muse worktree run rewrites `.git/info/exclude`, which in a linked worktree is the parent
clone's file. So: `-w` is never passed, worktree isolation is off (Decision 6), and the kind is
declared as one that creates no branches and no worktrees. A member who wants parallel writers gets
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

`muse auth set` (key on stdin) writing `~/.config/muse/auth.json`, and `~/.config/muse` joins the
file deny-list. Not `META_API_KEY` in the child's environment: the reason cursor refused env
injection (ADR 0023) applies unchanged, and an API key always overrides a stored session, so an
env-level key would silently defeat a member who later signs in. The connection card is therefore
**one input** — simpler than every kind except none — with `muse logout` behind the disconnect.

Open: a member on a Muse Code *subscription* (as opposed to pay-as-you-go) signs in through the
browser during CLI onboarding, which is a flow this container does not have. Phase 1 gate B decides
whether a subscription credential can be produced elsewhere and pasted, or whether subscriptions are
out of scope for v1.

### Decision 10 — usage and the model list ride the protocol

`usage/read`, `session/tokenUsage` and `session/contextUsage` exist on the wire, so `muse` is
expected to enter `usageMeasuredForKind`'s **exact** set (`usage_fold.go:206`) — but only after one
real turn is measured; an unmeasured "exact" is precisely the lie that switch exists to prevent.
`model/list` backs the picker, which means `agentModels.ts:34-35`'s `isDynamic` must list `muse`.
That one line is the recurring miss of every new kind (copilot, cursor), and it presents as "the
model picker only shows the default".

### Decision 11 — MCP: a new dialect that writes into a shared settings file

`mcp_servers` is a block inside the same `settings.json`, with `transport: stdio | streamable_http`,
`command`/`args`/`env` or `url`/`headers`, `enabled`, and **`mode`, which defaults to `required` —
a required server that fails to start aborts the whole run**. AF materialises with `mode: optional`
unless the member says otherwise; a broken tenant server must not make the agent refuse to start.
`${VAR}` interpolation exists and `MUSE_SESSION_ID` is passed to stdio servers. `muse` joins
`knownKinds` (`mcpreg/def.go:57`) and `MaterializedKinds` (`materialize.go:47`); project scope has
no documented Muse spelling, so `mcpproj` reads other kinds' files for it and is not a copy target.
Hooks (`.muse/hooks.json`, 15 lifecycle events including `Stop` and `Notification`) are **not** used:
the protocol already reports what a hook would, and a hook file in the repo is shared state.

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
- **Estimate**: managed-only, no TUI assets, ≈ **12–18 session-days**. It is not smaller than kiro's
  because the savings (no pane, no scraping) are spent on the settings dialect, the MSP client and
  types, the approval round trip, the subagent rendering and deployment. Anchored on the kind-wide
  inventory: 90 Go files and 41 Console files mention `kiro` today, and existing kinds are
  2,100–5,950 non-test lines each.
- If Phase 1's gates fail, the sunk cost is this ADR and the probe — no code.

## Phases

| Phase | Content | Gate to the next |
|---|---|---|
| 0 | The probe in this ADR (done 2026-09-20): install, MSP drive, pin/checksum, worktree and foreign-context behaviour, footprint | — |
| 1 | **Gate A**: bake `bwrap` and retest the sandbox on a real Workspace image; Decision 5 stands or is withdrawn. **Gate B**: one API key, one real turn — `usage/read` numbers, `model/list`, an `approval/requested` round trip, one subagent, and whether a subscription credential can be entered at all. 1–2 session-days | Both gates answered; the user accepts the Decision 6 clamps and the spend |
| 2 | Implementation: kind wiring, MSP client and generated types, driver, transcript, usage, settings + MCP dialect, connection card, deployment, guide, this ADR to *adopted* | — |

## Open questions (answer in Phase 1)

1. Real-turn token accounting: does `usage/read` give input/output/cached separately, and does it
   include the subagents' and observers' calls? (It decides whether `MeasuredExact` is honest.)
2. Subscription vs pay-as-you-go: can a subscription credential exist without the browser onboarding
   this container cannot run? If not, v1 is pay-as-you-go only and the guide says so.
3. Does `model/list` return a catalogue once authenticated, and is it plan-dependent the way copilot's
   and cursor's are (the "named models unavailable on Free" class of failure)?
4. How an approval renders: `approval/requested` carries staged shell review data; which of it the
   mirror's permission card can show without a new card type.
5. Whether `session/list` on a per-session host can see other AF sessions' Muse sessions (one store,
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
  strace -e trace=move_mount mount -t tmpfs none /tmp/x          # move_mount → EACCES
```

## Sources checked (2026-09-20, `06ea94d3`)

`workspace/agent/internal/session/session.go` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go}` ·
`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/mcpreg/{def.go,materialize.go}` ·
`workspace/agent/{usage_fold.go,agent_instructions.go,model_provider.go,env_tool_versions.go}` ·
`workspace/Dockerfile` · `console/src/agents/registry.ts` · `console/src/lib/agentModels.ts` ·
`console/src/styles/tokens.css` · `console/src/features/usage/colors.ts` ·
`docs/log/{36,40,43,74}-*-agent-kind.md` · `docs/decisions/0093-lcpp-agent-kind.md` ·
Meta's own documentation at `dev.meta.ai/docs/muse-code/{,auth,subscriptions,permissions,interactive,workflows,session-messaging,rewind,configuration,extending,changelog}`.

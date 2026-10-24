---
audience: "everyone; most often a member asking \"why did it stop?\" and an administrator asking \"what should I set?\""
source_of_truth: "this table for the fixed values; the limit's own screen for anything a tenant or deployment sets"
updated: "2026-10"
---

# Limits and defaults

English | [日本語](limits.ja.md)

This page collects the limits in one place because a limit is only ever met at the worst possible moment,
and the reader then needs three things at once: what the ceiling is, who can raise it,
and what happens when it is reached.

## Fixed in the product

These do not vary by deployment.

| Limit | Value | What happens at it |
|---|---|---|
| Session title | 80 characters | Control characters are stripped and the title is truncated. **Enforced identically at every layer on purpose**; see below |
| Browser pane: Chromium processes | 1 per workspace | — |
| Browser pane: concurrent pages | 2 per workspace | A third cannot be opened until one is released |
| Browser pane: viewport | 1600×1200 (DPR 1) | Larger is clamped |
| Browser pane: frame rate | 12 fps while visible (JPEG quality 70) | Not usable for video |
| Browser pane: hidden page retention | 60 seconds | After that the page is released and rebuilt from the saved port and path on return |
| Browser pane: console messages kept | 200 | Oldest are dropped; this is not a persistent log |
| Folder zip download: files / folders / depth | 20,000 / 4,000 / 32 levels | Refused with 413 and a message naming the limit; nothing is truncated (ADR 0111) |
| Folder zip download: bytes read | 512 MiB | Counted on the bytes actually read, not the listing, so a file that grows mid-build is caught; refused with 413 |
| Folder zip download: entries looked at / summed name length | 100,000 / 4 MiB | Refused with 413 |
| Folder zip download: reading time | 45 s | Refused with 413 (below the load balancer's 60 s idle timeout, because nothing is sent until the archive is built). Cooperative: checked between reads and after the last one, so a call already stuck on a hung volume holds the build slot until it returns |
| Folder zip download: transfer time | 15 min | The connection is closed; a stalled client cannot hold the one build slot longer |
| Folder zip download: concurrent builds | 1 per workspace | A second press waits up to 2 s, then 503 "busy"; each build holds a temp file of up to the byte limit on the home volume |
| A "working" session with no sign of life | 1 hour | It stops keeping the Workspace awake. Only for the session kinds whose progress the Agent observes; the time counts from the last output, state change or (for some kinds) running tool, not from the start of the turn. A session whose progress cannot be read keeps holding, and the "do not auto-stop" pin and background work are exempt ([operate/03](../operate/03-run.md#what-keeps-a-workspace-awake) lists the kinds) |
| A question, plan or permission left by a stopped session | 14 days | The carried card is dropped ([04](../member/04-conversation.md#questions-and-plans-left-by-a-stopped-session)) |
| Agent memory: one memory | 200 KiB body, 2,000-character description | Refused ([member/18](../member/18-agent-memory.md)) |
| Agent memory: the index a session receives | described part 24 KiB by default (the `memory_index` tool's `budget` takes 4–64 KiB), each description cut to 150 characters; then names only, within 8 KiB | Past the names budget the rest is only counted ("and N more") |
| Agent memory: Changes / Forgotten | the Console's Changes view shows the newest 100; `af-memory changes` lists 30 by default and `--limit` up to 300; 300 forgotten memories at a time | Older ones are not listed |
| Agent memory: suggestions from sessions | 250 review calls a day, one at a time; 100 proposals waiting | "Today's review limit has been reached" / "The pending proposals are full" |
| Jumping to an older turn from a search hit | 8,000 transcript positions back, in at most 4 requests | The turn is not reached; a notice says it is too far back, and scrolling up loads it |

> **Why the title limit is worth naming.** It used to differ per layer: a handoff
> proposal accepted 512 bytes, showed it on the card and in the launch dialog and let
> you edit it, and then session creation alone refused it at 80, surfacing as
> "failed to start worktree: title is too long". A limit enforced at different values
> in different layers produces failures that appear only at one specific moment, which
> is the hardest kind to diagnose.

## Set by the deployment

Defaults are the Control Plane's own; `deploy/compose/.env.example` annotates most of them.

| Limit | Default | Set by |
|---|---|---|
| Workspace memory | 1g (the Compose example and the local scripts set 5g; on ECS it comes from the `WsTaskMemory` parameter, 2048 MiB) | `WS_MEMORY` |
| Workspace working disk (Fargate) | 50 GiB | `AF_ECS_WS_DISK_GB` |
| Per-user home volume (EC2 target) | 50 GiB | `AF_ECS_EC2_HOME_GB` |
| Graceful stop | 30 s | `AF_STOP_GRACE_SEC` |
| Start timeout (AWS targets) | 300 s | `AF_ECS_START_TIMEOUT_SEC` |
| Stopped session kept in the list | 7 days | Each user's **Settings → Agents → Session** (1 / 3 / 7 / 14 / 30 days, or off); `AF_SESSION_STOPPED_TTL` is the deployment default for users who leave it at Default; after that it moves to the archive, never deleted, and its working copy stays; a session locked against deletion stays in the list |
| Cloud-cost window | 7 days | `AF_CLOUD_COST_WINDOW_DAYS` |
| A parent session and all its live children quiet, before the parent gets one notice naming the idle children | 15 minutes | `AF_STALLED_TREE_MINUTES` in the workspace's environment; `0` turns it off |
| Idle sweep | on | `AF_IDLE_SWEEP_INTERVAL`: **`0` switches the reaper off entirely**, so nothing is ever stopped for being idle |
| Idle session halted after | 1 hour | `AF_SESSION_IDLE_TIMEOUT`; `0` turns it off |
| Session waiting on a decision (question, approval) halted after | the session value | `AF_INTERACTION_IDLE_TIMEOUT` |
| Idle Workspace stopped after | 2 hours | `AF_WS_IDLE_TIMEOUT`; `0` turns it off |
| Background work keeps a Workspace awake for | 2 hours | `AF_BACKGROUND_HOLD_MAX`; `0` = unbounded |
| A terminal with no typing still counts as someone there for | 30 minutes | `AF_PRESENCE_IDLE_TIMEOUT` |
| A Workspace still starting is stopped after | 30 minutes | `AF_WORKSPACE_START_DEADLINE`; `0` turns it off ([operate/03](../operate/03-run.md#a-start-that-never-finishes)) |
| Home and state volumes (kubernetes) | 50 GiB and 5 GiB | `AF_K8S_HOME_GIB`, `AF_K8S_STATE_GIB` |

The self-hosted engines' own auto-stop is not in this table: **Stop after** is set per role under
Admin → Inference engines, and a saved value wins over the engine stack's default from the
controller's next pass ([operate/07](../operate/07-engines.md#inference-engines-self-hosted-gpus-superadmin-only)).

## Set per tenant

There is no fixed number to quote: a tenant administrator sees the values in force
under **Tenant settings → Limits & idle**, and a deployment administrator sets them.
Read the screen rather than this page.

- Workspaces per tenant, and sessions per member
- Workspace size (CPU, memory, disk) where the target supports it;
  see [deploy-targets.md](deploy-targets.md)
- Idle auto-stop: session halt, halt when awaiting a decision, the background-work hold and the
  Workspace stop; `0` is off (unbounded for the hold). An empty field takes the deployment default
  above, except halt when awaiting a decision: empty, it follows the tenant's own session halt when
  that is set, and only otherwise the deployment default ([admin/02](../admin/02-limits.md#idle-auto-stop))
- Whether the tenant's administrators may take engine models in (off unless granted), and whether
  the tenant may use the self-hosted engines at all, per role (llm / image): allowed unless
  switched off

## The limit board: what it collects and how it reads

The limit board (**Settings → Limit board**, [12 Working with others](../member/12-collaboration.md))
shows where members stand on their agents' usage limits. This is its full coverage; anything not
in the table is **unknown**, never "available".

| Agent | Account quota | Session limit state |
|---|---|---|
| Claude | 5-hour and weekly windows | yes |
| Codex | 5-hour and weekly windows | Managed sessions only |
| Muse | its windows, with their length | yes |
| Copilot | credit pools | unknown |
| Antigravity (agy) | not collected: reading it means scraping the agent's screen | unknown |
| every other agent | unknown | unknown |

Session limit states are: hit a limit (with the automatic resume time when one was scheduled),
spend limit (never with a resume time: waiting does not clear it), blocked, and sign-in needed.
A session that hit a limit says nothing about other models or agents.

| Rule | Value |
|---|---|
| A value is | the provider's last real observation, a lower bound: "at least X% (N min ago)" |
| Marked stale after | 60 minutes |
| Past the reset time | "reset not yet observed", never 0% |
| Bands | under 50% / under 80% / under 95% / 95% or more, per unit, never combined |
| Roster page | 50 members (at most 100) |
| Members named in one audience | 100 |
| Default | hidden, per tenant membership; the tenant switch is on |

Where values come from: while a workspace runs, the idle-stop sweep reads what the agents have
already captured, at its own interval (one minute by default); an open Console tab also sends
what its usage chips read. **Neither is promised**: a workspace the sweep does not reach (it is
stopped, the deployment runs no sweep, or the tenant has none of the idle-stop, hibernate or
backup features on) with no browser open is not updated, and its values turn stale or stay unknown.
A failed refresh never makes an old value look new, and signing in to another provider account
drops the previous account's values.

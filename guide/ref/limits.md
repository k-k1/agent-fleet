---
audience: "everyone; most often a member asking \"why did it stop?\" and an administrator asking \"what should I set?\""
source_of_truth: "this table for the fixed values; the limit's own screen for anything a tenant or deployment sets"
updated: "2026-08"
---

# Limits and defaults

English | [日本語](limits.ja.md)

Collected in one place because a limit is only ever met at the worst possible moment,
and the reader then needs three things at once: what the ceiling is, who can raise it,
and what happens when it is reached.

## Fixed in the product

These do not vary by deployment.

| Limit | Value | What happens at it |
|---|---|---|
| Session title | 80 characters | Control characters are stripped and the title is truncated. **Enforced identically at every layer on purpose** — see below |
| Browser pane: Chromium processes | 1 per workspace | — |
| Browser pane: concurrent pages | 2 per workspace | A third cannot be opened until one is released |
| Browser pane: viewport | 1600×1200 (DPR 1) | Larger is clamped |
| Browser pane: frame rate | 12 fps while visible | Not usable for video |
| Browser pane: hidden page retention | 60 seconds | After that the page is released and rebuilt from the saved port and path on return |
| Browser pane: console messages kept | 200 | Oldest are dropped; this is not a persistent log |

> **Why the title limit is worth naming.** It used to differ per layer: a handoff
> proposal accepted 512 bytes, showed it on the card and in the launch dialog and let
> you edit it — and then session creation alone refused it at 80, surfacing as
> "failed to start worktree: title is too long". A limit enforced at different values
> in different layers produces failures that appear only at one specific moment, which
> is the hardest kind to diagnose. One value, one place.

## Set by the deployment

Defaults come from [deploy/compose/.env.example](../../deploy/compose/.env.example),
which stays the source of truth.

| Limit | Default | Set by |
|---|---|---|
| Workspace memory | 5g | `WS_MEMORY` |
| Workspace working disk (Fargate) | 50 GiB | `AF_ECS_WS_DISK_GB` |
| Per-user home volume (EC2 target) | 50 GiB | `AF_ECS_EC2_HOME_GB` |
| Graceful stop | 30 s | `AF_STOP_GRACE_SEC` |
| Start timeout (AWS targets) | 300 s | `AF_ECS_START_TIMEOUT_SEC` |
| Cloud-cost window | 7 days | `AF_CLOUD_COST_WINDOW_DAYS` |
| Idle sweep | on | `AF_IDLE_SWEEP_INTERVAL` — **`0` switches the reaper off entirely**, so nothing is ever stopped for being idle |

## Set per tenant

There is no fixed number to quote: a tenant administrator sees the values in force
under **Tenant settings → Limits & idle**, and a deployment administrator sets them.
Read the screen rather than this page.

- Workspaces per tenant, and sessions per member
- Workspace size (CPU, memory, disk) where the target supports it —
  [deploy-targets.md](deploy-targets.md)
- Idle auto-stop threshold

## Status

Two rules for anyone extending this page. A limit enforced in more than one layer must
state **every** layer, for the reason above. And a limit nobody measures must say so
rather than being given a plausible-looking number — an invented default is worse than
an admitted gap, because it stops people from checking.

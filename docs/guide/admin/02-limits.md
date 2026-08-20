# Resource limits, idle auto-stop, and the sessions overview

English | [日本語](02-limits.ja.md)

As the team grows, the questions become "who is using how much of the resources" and "can we stop a
runaway". This chapter sorts out what you (tenant_admin) can see, what you can adjust, and what you
ask IT for.

## Two kinds of limits

There are limits **that apply to the whole tenant** and limits **that apply to an individual
member**.

### Tenant-wide limits (set by super_admin)

Tenant-wide limits and idle auto-stop are **set by a super_admin only** — the settings section lives
in the Admin modal and never appears for you. What you can read is the current values, under
**Tenant › Limits & idle** in the tenant settings modal (member count, running workspaces and
"Limits — Workspace / Session"). Just know what they contain.

- **Max workspaces** — the number of workspaces that can run concurrently in this tenant
  (`0` = unlimited).
- **Max sessions** — the number of sessions that can run concurrently.
- **Max internal repositories / Max LFS size** — the number of repositories that can live in the
  built-in git and the total LFS capacity.
- **Idle auto-stop** — how long before neglected sessions and workspaces are stopped automatically
  (next section).

The "Limits — Workspace: X / Session: Y" shown under **Tenant › Limits & idle** is the value
currently in effect. When you want it changed, ask your IT department / deployment administrator
([operator/README.md](../operator/README.md)).

### Per-member session limits (you can set these)

What you can adjust is the per-member **session limit**. Pressing **"Set limits"** in the
"Operations" section of the member detail lets you enter "Max sessions (0 = unlimited)"; once
saved, it applies to that person only. Members with a personal limit show "s≤ N" on their member
row in the roster.

Between a personal session limit and the tenant-wide limit, whichever is stricter kicks in first.

### Workspace size (memory, CPU, working disk)

The same "Set limits" panel also decides how big that member's workspace is. The three axes are
independent.

| Axis | Unit | When 0 |
|---|---|---|
| Memory | MB | deployment default |
| CPU | Fargate CPU units (1024 = 1 vCPU) | deployment default |
| Working disk | GB | the deployment default (50 GiB on the reference AWS stack) |

The **S / M / L / XL / 2XL** buttons are a shortcut that fills all three at once. You can also enter
them individually.

On AWS **only specific memory/CPU combinations exist** (4 vCPU cannot run with less than 8 GB, for
example). An entry that is not a valid combination is rounded up to the nearest valid size when you
save, which is why **raising CPU can also raise memory**. The response after saving shows the values
that will actually be applied.

Changes take effect **from the next workspace start**; a running workspace is not resized.

> **The working disk does not persist.** Its contents are wiped when the workspace stops — only the
> home directory (`~`) survives. It is a place for build output and caches, not for storing things.

Build output goes there **by itself**: caches (Go build cache, `uv`, Go modules) are moved at
workspace start, and `node_modules` / `target` / `.venv` / `build` are pointed at it as each repo or
worktree is created. That is what makes installs and builds several times faster on AWS, where the
home directory is network storage. It only happens when the working disk is **30 GB or more** — below
that there is no room, and everything stays in the home directory as before.

## Idle auto-stop

This is the mechanism that automatically folds up neglected environments (the settings are on the
tenant-wide side, so they are super_admin's domain). It has two stages.

- **Session halt after** — a neglected session that no one has open is folded to
  "stopped (resumable)" once it exceeds this time. The conversation log remains, so it can be
  resumed later.
- **Workspace stop after** — a workspace with no one connected and no running sessions has its
  whole container stopped once it exceeds this time.

The time format is `30m` (minutes), `2h` (hours), `90s` (seconds). Empty follows the deploy
default (disabled by default), and `0` means explicitly disabled. For the details of the behavior,
see [dev/03 §3.7 Background jobs](../../dev/03-control-plane.md).

Even when stopped, the work itself (the contents of home) remains. The next time members need it,
they just start it again from the Console.

## Viewing a member's resources

Opening a member detail shows the state of that member's workspace. At the top is whether it is
Running or Stopped, and below that, **"Workspace resources"** with 3 meters.

- **Memory** — usage, limit, and utilization.
- **CPU** — utilization ("1 core = 100%", so 200% means using the equivalent of 2 cores).
- **Disk** — home usage and allocation. Even while stopped, disk usage alone is still shown.

The meters change color as utilization rises (warning → danger), so pressure is visible at a
glance. Values refresh every few seconds.

## Viewing sessions / getting the whole picture

**"Sessions"** in the member detail lists that person's sessions along with their kind
(claude / shell, etc.), what they are working on, their state (running / stopped, etc.), and start
time. This is view-only — there is no button to stop an individual session (if you want to stop
the whole environment, use force-stop in the next section).

When you want to survey the whole tenant, open **"Sessions"** in the rail. It shows
**only the sessions running right now** in the tenant, aggregated across users (stopped ones are
checked in each member detail). You can filter by "User / label / repository", and the total
("N running") appears at the top right. It is well suited to a first-thing-in-the-morning check of
what is running and whether resources are under pressure.

## Force-stopping a workspace

When you want to stop a runaway session, or an environment left occupying resources, use
**"Force-stop the workspace"** in the "Operations" section of the member detail (it can only be
pressed while the workspace is running). After a confirmation dialog, that member's workspace
container stops. **The work (home) is not lost.** The member can start it again from the Console.
It is strictly a "pause for now" operation, not destructive.

Note that the "Clean home" button, which cleans home itself, is super_admin only and is not shown
to you. **Situations that need heavier measures** — the container is broken and restarting doesn't
fix it, host-side intervention is needed — **are the domain of your IT department / deployment
administrator** ([operator/README.md](../operator/README.md)).

## What members experience when a limit is hit

Limits take effect at the moment a member tries to "start something new". Things already running
are not suddenly stopped. To members it looks like this.

- **Session count limit** — trying to create a new session is rejected, and the Console shows
  "You've reached the limit on concurrently running sessions. Stop one of the running sessions
  before creating another." (internally an HTTP 429).
- **Workspace count limit (tenant-wide)** — trying to bring up a new workspace while at the limit
  is likewise rejected with a 429.
- **Internal repository count / LFS capacity limits** — creation / uploads beyond the limit are
  refused (repository creation with a conflict error, LFS with a capacity-exceeded error).

In other words, members end up in a "tidy up what you have and you can continue" state. If someone
hits limits constantly, revisit their personal session limit, or talk to IT about raising the
tenant-wide limits.

## How to act when resources are under pressure

When people start saying "it's heavy" or "it's slow", triaging in this order is fastest.

1. **Survey the whole picture under "Sessions"** — see what is running and how many, and
   whether it is skewed toward a particular person or repository.
2. **Open the likely member's detail and check the meters** — if memory or disk is in the danger
   zone (the color has changed), that person's environment is the main cause of the pressure. CPU
   spikes momentarily, so judge by whether it stays high.
3. **Act if needed** — for a temporary runaway, the default is to have the member stop the session
   themselves. Only when they can't be reached and it sits neglected, or it is clearly out of
   control, pause it with "Force-stop the workspace".
4. **If it is chronic, adjust with limits** — if a particular person constantly runs too much,
   consider a personal session limit; if the whole tenant is chronically under pressure, consider
   revisiting the tenant limits or idle auto-stop (super_admin / IT).

Host-wide memory status (including other tenants) is visible only to super_admin. When "the host is
heavy" across tenants, your screen alone can't settle it, so share it with IT.

---

- Read next: [03 Audit and usage](03-audit-usage.md)
- Back to: [01 Member management](01-members.md)

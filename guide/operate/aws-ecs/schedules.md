---
audience: "a deployment administrator of an AWS ECS deployment who tunes how workspaces are woken at a set time"
source_of_truth: "the parameters of `deploy/aws/ecs/cfn/30-ingress.yaml` and the Control Plane's scheduler (`control-plane/main.go`)"
---

# Turn on timed wakes

English | [日本語](schedules.ja.md)

Let members' schedules wake a stopped workspace at the appointed time and run the prompt.
The scheduler is on by default. This page covers switching it off, and giving a slow-booting deployment more time.

## How it works

| What you do | What happens |
|---|---|
| Nothing | The scheduler ticks every minute, and does nothing while no schedule exists |
| Set `SchedulerInterval=0` | No timed wake fires at all, and the Console hides "Schedules" |
| Raise `ScheduleWakeTimeout` | A workspace that boots slowly is still reached on the first attempt |

How members create and use schedules is in [Run at a set time](../../user/fleet/schedules.md).

## Parameters

`30-ingress` has four parameters. **Leave one empty to keep the product default.** A copy pinned in the stack stops following the product when its default changes.

| Parameter | Control Plane variable | Default | What it is |
|---|---|---|---|
| `SchedulerInterval` | `AF_SCHEDULER_INTERVAL` | `1m` | Tick cadence. `0` hard-disables timed wakes: nothing fires, and the Console hides "Schedules" |
| `ScheduleWakeTimeout` | `AF_SCHEDULE_WAKE_TIMEOUT` | `300s` | How long one fire waits for the woken Agent. Read the warning below |
| `ScheduleSettle` | `AF_SCHEDULE_SETTLE` | `5m` | How long the woken workspace is held up afterwards, so idle stop cannot stop it mid-turn |
| `ScheduleJitter` | `AF_SCHEDULE_JITTER` | `2m` | A deterministic spread for cron fires, so a 09:00 crowd does not wake at once. `0` fires on the exact slot |

## Do not shorten the wait on `ecs-ec2`

On `ecs-ec2`, do not lower `ScheduleWakeTimeout` without measuring.

- Its clock starts at the **wake**, roughly 20 seconds before the Control Plane's own `Agent healthy Ns after Start` log line: the slot's start, ECS registration and home mount all run in the background
- On one deployment the same workspace, on the same schedule and the same slot, took **65 to 131 seconds** on five consecutive mornings. A 90-second budget silently dropped three of the five
- A budget inside that spread makes a stopped workspace's morning schedule fire at random
- An undelivered fire is re-attempted on later ticks for 15 minutes, but each retry wakes the workspace again, so make the budget big enough to win on the first attempt

## Related

- [Run at a set time](../../user/fleet/schedules.md): members create schedules
- [Use idle stop and force-stop](../run/idle-stop.md)
- [Set the default workspace size](workspace-size.md)

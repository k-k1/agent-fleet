---
audience: "Deployment administrators who decide how workspaces and the deployment stop"
source_of_truth: "The Control Plane's reaper, usage sampler, start deadline and silence detection (`control-plane/`), `deploy/compose/.env.example`"
---

# Use idle stop and force-stop

English | [日本語](idle-stop.ja.md)

Sessions and workspaces nobody uses stop by themselves. Starts that never finish and Agents that stop answering can be caught and stopped too, and a deployment nobody will use for a while can be paused as a whole.
The deployment decides how long things wait and how they stop.

## How it works

| What you do | What happens |
|---|---|
| Nothing | Unused sessions and workspaces stop after the deployment's default times. Starts that never finish stop too |
| Change the deployment defaults with environment variables | Every tenant's default changes. Tenants can override them in the admin screens |
| Turn on stopping a silent Agent | Workspaces left "Not responding" past the limit are stopped |
| Force-stop a workspace from the admin screens | That workspace stops for certain |
| Pause the deployment with `pause.sh` | What is billed by the hour stops, and the data stays |

The default times are in [Limits and defaults](../../ref/limits.md#set-by-the-deployment). What tenant administrators see is in [Stop workspaces left idle](../../admin/resources/idle-stop.md).

## Set the idle auto-stop times

| Environment variable | What stops |
|---|---|
| `AF_SESSION_IDLE_TIMEOUT` | Sessions nobody uses |
| `AF_INTERACTION_IDLE_TIMEOUT` | Sessions halted on a question or an approval |
| `AF_WS_IDLE_TIMEOUT` | Workspaces with nothing running |
| `AF_BACKGROUND_HOLD_MAX` | The longest background work can keep a workspace up. `0` means unbounded |
| `AF_PRESENCE_IDLE_TIMEOUT` | The grace during which a terminal with no keystrokes still counts as present |

- `0` in the environment turns it off for the whole deployment
- Per-tenant values are the four fields under "**Idle auto-stop**" in the admin limits (**Session halt after**, **Halt when awaiting a decision**, **Max hold by background work**, **Workspace stop after**). Empty follows the deployment default; `0` turns it off for that tenant only
- `deploy/compose/.env.example` annotates what each variable means

### What counts as present

A session with a terminal open in the Console is kept from stopping only while that terminal has keystrokes within the presence grace.

| Counts as present | Does not count |
|---|---|
| Keystrokes and pastes in the terminal | The terminal's automatic replies (focus reports, device attribute and cursor position replies, colour query replies) |
| The last click, scroll, keystroke or paste in a visible browser pane | The Console reconnecting a terminal pane it keeps open |
| | Switching tabs so that a pane is shown again |

A tab left open without typing, or a tab in front on a screen that never locks, does not hold a workspace.

### What wakes a stopped workspace

A stopped workspace comes back when a member presses "**Start**". Two more things start it automatically:

| Mechanism | When it starts the workspace |
|---|---|
| `AF_AUTOSTART` (on by default) | Only on an action that clearly means "use it now": creating, forking or resuming a session, answering a carried-over question, discovering SSM nodes |
| The tenant's **Start at sign-in** (off by default; [Start the workspace at sign-in](../../admin/resources/start-at-signin.md)) | Once, in a visible tab, after a real interaction or right after arriving by sign-in or a typed address. A reload does not count |

- Opening a terminal, browsing or reloading the Console does not start it; the answer is only that it is stopped
- `AF_AUTOSTART=0` turns Start at sign-in off too
- The Control Plane checks the tenant switch, the member's opt-out and `max_workspaces`

## What keeps a workspace awake

| What | How long it keeps the workspace up |
|---|---|
| Background work (a dev server or a `run_in_background` task started by an idle session) | Up to the background hold limit. Past it, it no longer keeps the workspace up |
| A Terminal session shown as "working" | Only while it shows signs of life. With no change for the "no sign of life" limit ([Fixed in the product](../../ref/limits.md#fixed-in-the-product)) it stops counting |
| The "Keep awake" pin | No limit. Use it for genuinely long jobs |

The "no sign of life" limit caps time without change, not total working time. What counts as a sign of life depends on the agent.

| Agent | Sign of life |
|---|---|
| Terminal claude, agy | Output, a state change, a running tool process |
| Terminal codex, cursor, copilot, kiro, opencode | Pane redraws only (spinners and elapsed-time updates included) |
| Managed cursor, kiro, codex, opencode | The session's last message, or a running tool (an unfinished tool call; for cursor and kiro also tool processes under the runtime) |
| Managed lcpp | Always alive while a turn runs. Only a row left "working" after the turn ends lapses |
| Managed muse, copilot | Not subject to the limit |

- For agents judged by pane redraws only, a silent tool that leaves the pane still can lapse, and a wedged session whose spinner keeps turning is held. Use the "Keep awake" pin for long silent work
- In managed sessions, a long call to an external MCP server shows as unfinished for the whole call (not yet checked for cursor)
- When the Agent cannot tell what is running (a restart, a reconnect, a failed process-table read), it keeps holding
- shell and ssm never show "working". muse and lcpp have no Terminal execution method
- Background work and the "Keep awake" pin are never subject to this limit

## Stop starts that never finish

A workspace still starting past the start deadline is stopped. On ECS it is usually a cluster that cannot place the task. This keeps it from holding capacity; once the cause is fixed, the member can press "**Start**" again.

- The deadline counts from the last start. When the Control Plane first sees the workspace starting later than that (after a Control Plane restart, say), it counts from then
- The check runs on each usage sample (5 minutes), so the stop can come up to one sample late
- The member gets a notification in the notification centre that the start was stopped, with the last start phase (on `ecs-ec2`, why the task could not be placed). Tenant administrators see the same reason on the roster row and the member detail until the next start. The Control Plane logs `start-deadline: stopped …`

| Setting | Meaning |
|---|---|
| `AF_WORKSPACE_START_DEADLINE` | The deadline. `0` turns it off. On `ecs-ec2` it is never shorter than the pool's own start budget (`AF_ECS_EC2_CLAIM_TTL_SEC`, `AF_ECS_EC2_WAIT_SEC`) |
| `AF_USAGE_SAMPLE_INTERVAL=0` | It runs on the usage sampler, so this turns it off too |

## Detect a silent Agent

When the Agent of a running workspace stops answering, it is marked, not stopped.
Workspace containers have no ECS health check, and ECS reports the task as running whatever the Agent is doing. This detection is what notices.

1. The Control Plane records every answer from the Agent (Console polls, relays, the usage sampler, health probes)
2. When the runtime says `running` but nothing has answered for `AF_AGENT_SILENT_AFTER`, it probes `/healthz` directly (at most once every 30 seconds per workspace)
3. Only when that probe fails too is the workspace marked

| When marked | Where it shows |
|---|---|
| Member | The "**Not responding**" badge |
| Tenant administrators | The member detail, and one notification per outage |
| Records | The log's `agent silent: …`, the audit log's `workspace.agent_silent` / `workspace.agent_recovered` |

- `AF_AGENT_SILENT_AFTER` defaults to `3m`; `0` turns it off. If nothing has answered since the Control Plane first saw the workspace running, it waits twice as long
- State is held per Control Plane replica, and a restarted replica starts over from the grace period, so a deploy does not set off alarms all at once
- A workspace nobody has open is reached only by the usage sampler's rounds. With `AF_USAGE_SAMPLE_INTERVAL=0`, only the checks made while someone is polling remain

`AF_AGENT_SILENT_AFTER` can be set only in the Control Plane's environment: `.env` on Compose, `cp.env` on Kubernetes (GKE included). The ECS templates have no parameter for it, so ECS runs with the default `3m`.

## Stop a silent Agent automatically

Off by default. Set `AF_AGENT_SILENT_STOP_AFTER` (a Go duration such as `30m`) and the Control Plane stops a workspace that has stayed marked "**Not responding**" longer than that.

| Type | Where to set it |
|---|---|
| ECS | The `AgentSilentStopAfter` parameter of `30-ingress.yaml` |
| Docker Compose | `.env` |
| Kubernetes (GKE included) | `cp.env`, the source of the `af-cp-env` ConfigMap |

- The time counts from when that replica marked the workspace (a restart starts it over)
- It is never shorter than `10m`; a smaller value is logged and raised to `10m`. `AF_AGENT_SILENT_AFTER` must be on
- The check runs on the usage sampler's rounds, so the stop normally comes within `AF_USAGE_SAMPLE_INTERVAL` after the limit. It comes later when the safeguards below hold it back, when the lifecycle lock is busy, or when a round did not finish

### What it checks before stopping

- It takes the same locks as an administrator's stop (the member's start lock, the lifecycle lease, the runtime's operation lock), then decides again
- It confirms that the workspace is still `running` (`starting` is excluded, the member's own restart included) and that a fresh `/healthz` probe fails again
- Two replicas never stop the same workspace; the later one sees it stopped and does nothing

Safeguards against the Control Plane itself being cut off:

- It stops only when the round finished **and** at least one other running workspace answered
- It stops at most two per round (longest silence first)
- On a deployment with a single running workspace, it marks but never stops

A stop leaves `workspace.agent_silent_stop` in the audit log (actor `system`), `agent silent stop: …` in the log, a notification to the member, and "Stopped automatically" on the tenant roster until the next start.

> [!WARNING]
> **The Control Plane cannot reliably tell a dead Agent from a broken network path.**
> Checking for other answers only rules out the whole Control Plane being cut off. When only the path to one host or one workspace breaks, a workspace that is actually healthy, with sessions running, is stopped once the limit passes.
> Running sessions stop as in an ordinary stop (they can be resumed). Choose the time with that in mind; `30m` is a reasonable start.

## Force-stop a workspace

`docker compose down` does not stop members' workspaces; they are not compose services.

| Want to | How |
|---|---|
| Stop one workspace for certain | A deployment administrator force-stops it from the Console's admin screens ([Force-stop a workspace](../../admin/resources/idle-stop.md#force-stop-a-workspace)) |
| Take the whole host down for maintenance | Stop the Control Plane and Caddy, then `docker stop` the remaining `af-ws-*` containers separately ([Find the cause from the symptom](../diagnose/checklist.md)) |

## Pause the deployment while nobody uses it

Even with every workspace stopped, the Control Plane, the database, the NAT gateway and the load balancer still cost money.
When nobody will use the deployment for a while, `pause.sh` on AWS and GKE stops or deletes what is billed by the hour. The data stays. `--up` brings it back.

| Option | Meaning |
|---|---|
| (without `--yes`) | Only prints the plan |
| `--dry-run` | Prints the writes without making them |
| `--status` | Only prints what is running and what is still billed; changes nothing |
| `--keep-db` | Leaves the database running |
| `--up` | Brings the deployment back |

| Type | What it stops | What stays billed |
|---|---|---|
| AWS (`ecs` / `ecs-ec2`): `deploy/aws/ecs/pause.sh` | Running workspaces (their sessions end), slots and engine GPU machines, the Control Plane, the RDS instance, the NAT gateway (`--keep-nat` keeps it) | The load balancer, EFS, database storage |
| GKE (`kubernetes`): `deploy/gcp/gke/pause.sh` | The Control Plane (scaled to 0), the workspace and system node pools, Cloud SQL | The GKE management fee, the load balancer's forwarding rule, the Private Service Connect endpoint, Cloud NAT and its address, persistent disks, Cloud SQL storage |

On AWS:

- The NAT gateway is removed through the network stack's `NatEnabled` parameter. Delete it by hand and the stack keeps pointing at a gateway that no longer exists
- The egress address is kept, so the source IP is the same after `--up`. While there is no gateway, private subnets cannot reach the outside
- AWS starts a stopped RDS instance by itself after 7 days. The data stack's re-stop mechanism (`RdsRestopOnForcedStart`, on by default) stops it again while the pause tag is present. An older data stack gets it with one deploy of that stack
- On a deployment with an egress gateway (`EgressRoute=gateway`), the gateway instance is stopped too, before the Control Plane (its address is kept). `--up` waits until the gateway passes traffic
- `--up` brings back the NAT gateway, the database and the Control Plane. Members start their own workspaces
- `--fast` stops slots and engines directly instead of having the Control Plane put them to sleep

On GKE:

- Run it in a shell where kubectl points at that cluster. If the context's server and CA are not that cluster's, it refuses
- It refuses while workspaces are running. `--stop-workspaces` stops them
- Unlike RDS, Cloud SQL does not start itself again
- Node counts are recorded in the Control Plane's namespace and restored by `--up`. Until then, `terraform plan` reports a node-pool diff

The order each script follows, why, and what to check afterwards are in the runbooks: [ECS, "Lifecycle scripts"](../../../deploy/aws/ecs/README.md#lifecycle-scripts-stand-up--pause--tear-down) and [Pause and see the bill](../kubernetes/pause.md).

### Delete the deployment

To get as close to zero cost as possible, delete the deployment (`teardown.sh` on AWS, `terraform destroy` on GKE). What remains depends on the type.

| Type | What remains after deletion |
|---|---|
| AWS with `Persistence=retain` | Unless you pass `--purge-retained`: the database's final snapshot, the EFS file system and its backup vault (their storage keeps costing) |
| GKE | The Control Plane disk's snapshot. Deletion protection must be removed first |

What goes and what stays is in the runbooks: [ECS, "Teardown"](../../../deploy/aws/ecs/README.md#teardown) and [Tear down](../kubernetes/teardown.md).

## Related

- [Stop workspaces left idle](../../admin/resources/idle-stop.md): the tenant administrator's outlook and force-stop
- [Stop when done, or keep a session awake](../../user/sessions/auto-stop.md): the member's "Keep awake" pin
- [Limits and defaults](../../ref/limits.md)

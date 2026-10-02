---
audience: "someone adding a way to run workspaces"
source_of_truth: "`control-plane/internal/runtime/` — the `Runtime` port and `NewFactory` in `runtime.go`, the optional capabilities the CP probes for, and the adapters themselves (`runtime_*.go`)"
updated: "2026-10"
---

# 21. Adding a deployment target

English | [日本語](21-add-a-deploy-target.ja.md)

A deployment target is a profile value that `runtime.NewFactory`
(`control-plane/internal/runtime/runtime.go`) accepts, with an adapter behind it. Today
the switch knows `docker` (also `local` and the empty value), `native` (`wsl`), `ecs`
(`aws`), `ecs-ec2` and `kubernetes` (`k8s`). What each one is and how it is run is [09](09-deploy.md) and
[01 §1.6](01-architecture.md); what each one supports is
[ref/deploy-targets](../../guide/ref/deploy-targets.md); the operator's choice is
[operate/01](../../guide/operate/01-choose.md). This chapter covers what a new adapter
has to provide, and what has to change around it.

Read the existing adapters before writing one. They differ more than a new one usually
needs to:

- **`runtime_docker.go`** is the one to start with: every required method, and nothing
  the substrate does not need.
- **`runtime_native.go`** shows what a target without a container does for itself: a
  pidfile instead of `docker inspect`, an OS-level lock next to the database lease, and a
  boot-phase file so that a different request can read it back.
- **`runtime_ecs_ec2*.go`** claims almost every optional capability at once. It shows how
  far a target can go, not the minimum.

## 21.1 What a target is

- **A profile in `NewFactory`'s switch, an adapter, the optional capabilities it claims,
  a tree under `deploy/`, and a way to ship it.** The first three are code in
  `internal/runtime/`; the rest is §21.5.
- **The core is shared.** The CP, the Console, the agent and the workspace image are the
  same on every target. The shipped `native` package runs that image's rootfs under
  bubblewrap. Its plain mode, a host-built agent with no image
  (`AF_NATIVE_AGENT_BIN`), is the `run-dev.sh native` development workflow. **If your
  target needs a different workspace image, stop**: that breaks the one property that
  makes any of this portable.
- **The core asks the adapter; it rarely asks the profile name.** Behaviour is chosen by
  the optional interfaces of §21.3, not by comparing strings. The exceptions exist and
  are small. For example, `main.go` sets `mgr.nativeRuntime`, which skips the session
  quota, and the Console shows the slot-pool screen only when the runtime is `ecs-ec2`.
  Before assuming your profile is not named anywhere, grep `control-plane/*.go` for
  `AF_RUNTIME` and `console/src` for the runtime ids.
- **An unknown profile fails fast at boot** (`unknown AF_RUNTIME profile`); only the
  empty value means `docker`. Keep that: a deployment that silently ran on the wrong
  substrate would be far worse than one that refuses to start.
- **A new substrate is a new profile, not a flag on an existing one.** `ecs` and
  `ecs-ec2` are separate on purpose, so a deployment can fall back by changing one value
  instead of reverting code
  ([decisions/0045](../decisions/0045-ec2-persistent-workspace.md) decision 10-1).

## 21.2 The required contract

The `Runtime` interface is `Start`, `Stop`, `State`, `Endpoint`, `Token` and `Name`, and
its doc comment is the contract. These are the parts that are easy to miss:

| Obligation | What it means |
|---|---|
| **Start commits the launch; the agent's readiness is not its result** | `Start` runs inside an HTTP request. It may wait a courtesy grace on the agent's `/healthz` (the local adapters do, via `WaitAgentHealthy`), and that wait must fit the ingress in front of it. **Exhausting that grace while the request is still live is not a Start failure**: returning an error marks a booting workspace as failed while it runs. A cancelled request or a lost lease is different: docker returns the error, and native also aborts its uncommitted spawn. The ECS adapters return before the agent can answer and converge in the background; how early each returns is [09 §9.5](09-deploy.md) ([03 §3.3](03-control-plane.md), the head of `runtime_health.go`) |
| **Report `starting` honestly** | `starting` means a launch or rollout is under way and the workspace cannot yet be treated as one stable running agent. That is usually "not reachable yet", but not always: the ECS adapters keep reporting `starting` while an old task is still draining behind a new one that already answers, because Service Connect could route one client's requests to two agents with different in-memory state (`serviceRolledOut` in `runtime_ecs.go`). **While a workspace is `starting`, callers must neither start it again nor idle-stop it**, so a `starting` that never converges would be a workspace nobody can operate. Two bounds exist. The local adapters time-box their own window (`AgentBootBudget`, then `running`). The CP puts a ceiling over every adapter (`start_deadline.go`): a workspace still `starting` `AF_WORKSPACE_START_DEADLINE` (30 minutes) after the later of its last Start and the CP's first sighting is stopped, unless a task is running — by the adapter's own count where it implements `runtime.TaskCounter`, otherwise by the agent answering. That ceiling is what ends the ECS case, where a task ECS refuses to place keeps the service `starting` for as long as desired is 1 (`ecs-ec2` also reports the reason as a boot phase, `notePlacementBlocked`). Keep your own normal launch well inside the ceiling, make `Stop` work on a `starting` workspace — the ceiling calls it there — if `Start` leaves launch work running in the background that `Stop` does not cancel, implement `runtime.LaunchBudgeter` so the ceiling never fires inside it, and if your `starting` can cover a task that already runs, implement `runtime.TaskCounter` |
| **Lifecycle state lives on the substrate** | The manager builds a Runtime from the database row (`manager.runtimeFor`), may cache it, and may rebuild it at any time. A restarted CP or a second replica holds a different value. Whether a workspace exists, runs or is starting must therefore be recoverable from the substrate by a deterministic name, a tag or a file. The existing adapters use `docker inspect`, a pidfile, the ECS service and EC2 tags ([09 §9.5](09-deploy.md)). Process-local scratch is allowed for what may be lost, if you say what losing it costs: native keeps an uncommitted spawn in the value to clean it up, and `ecs-ec2` keeps its provisioning phase in a process-wide map that another replica cannot see |
| **Two-stage graceful stop** | Signal, wait `AF_STOP_GRACE_SEC` (30 by default), then kill. The agent gets `AGENT_STOP_GRACE_SEC`, derived as that grace less a 5-second margin, so it can interrupt its panes and let tmux exit first; the derivation floors at 5 seconds, so a grace set below 10 leaves no margin (`stopGraceSec` / `agentStopGraceSec` in `runtime_docker.go`, [03 §3.3](03-control-plane.md)) |
| **An endpoint the CP can always reach** | `Endpoint()` has to be reachable from wherever your target runs the CP: the same host for docker and native, which return a loopback address, and every CP replica on ECS. That includes a workspace created after the CP started. On ECS a Service Connect alias does not resolve for a service added later, and `agent_dial.go` exists to cover the gap |
| **Deliver the env you were built with, at every start** | The factory's `New(ws, secretKey, extraEnv)` carries the DEK and per-workspace variables, and they can differ from one start to the next (an unattended scheduler wake, a preview slug). Container env is fixed at the instant of the start, so it has to go in there. The deployment-wide template env (`Config.ExtraEnv`: `WS_ENV` and the egress proxy variables) is a separate input, and today only docker and native pass it on ([09 §9.4](09-deploy.md)); decide deliberately whether yours does. **Never put the DEK or `AGENT_TOKEN` where the substrate can show it**: docker passes them in a 0600 env file rather than on the command line, and ECS passes an SSM reference rather than the value ([09 §9.5](09-deploy.md), [07 §7.6](07-security.md)) |
| **Two persistent areas, surviving a stop** | The home at `/home/dev`, and Claude's state at `/var/lib/af/claude` (`CLAUDE_CONFIG_DIR`). The second one is kept apart from the home so that the file browser cannot reach it and a reset of the home leaves the Claude login alone. **Operations that change the home have to reach the real home**, wherever your target keeps it. The CP never removes anything from a home itself: Recreate and Clean home go through the adapter's `WipeHome` / `EraseHome` (`internal/runtime/home_wipe.go`), because a path on the CP's own disk is the home only for docker and native, and removing a path that does not exist succeeds. Claim those ports only if your adapter can reach the home; without them the CP refuses the operations before it stops anything and the Console hides them (21.3). How each existing target stores them is [01 §1.6](01-architecture.md) and [07 §7.2](07-security.md) |
| **Destroy** | `runtimeDestroyer` is required of every adapter and asserted in `runtime.go`. It removes the home and every per-membership resource you created. Its `[]string` result lists what you **know** you could not remove, so it reaches the audit log instead of an operator assuming the data is gone |
| **Per-user isolation, or refuse to run shared** | Answer every row of [07 §7.2](07-security.md) for your target. Where you cannot, do what `native` does: the factory refuses any `AUTH` other than `dev`, because without a container boundary nothing separates users |

## 21.3 Optional capabilities: claim what is true

The CP asks for most per-target behaviour with a type assertion (`rt.(X)` on a Runtime,
`m.rtFactory.(X)` on the factory) and branches on the answer. **An adapter that does not
claim a capability still compiles**, and the CP silently takes that capability's own
fallback: it may hide a feature, switch to another delivery path, or show a default that
describes some other target. So claiming one, or not, is a statement about your
substrate.

These are the capabilities at the time of writing, with what the CP does without each.
The interfaces are the real list: grep `control-plane/*.go` for `rt.(` and
`rtFactory.(`, and grep for a method name to find the adapters that implement it and are
worth reading. Which target supports what, as a user sees it, is
[ref/deploy-targets](../../guide/ref/deploy-targets.md).

| Capability | Declared in | Without it |
|---|---|---|
| `SizingProfile()`: what CPU, memory and disk mean here | `workspace_sizing.go` (`sizingProfiler`) | you are described as docker |
| `CostProfile()`: is there a bill, and what it covers | `cost_profile.go` (`costProfiler`) | no cost view, and version info reports the runtime as `local` |
| `WorkspaceImage()` | an inline interface in `main.go` and `version_info.go` | the startup banner names the docker template's image, and version info leaves the workspace image out |
| `DocsMounter`: the guide is bind-mounted | `internal/runtime/runtime.go` | the container pulls it from `GET /internal/docs` ([04 §4.9](04-agent.md)). Claiming it without a host path the container can see leaves the guide empty |
| `Stale()`: would a stop and start run different code | `workspace_stale.go` | never reported stale |
| `BootPhase()` | an inline interface in `workspace_handlers.go` | the start dialog shows no phase |
| `AcquireOperationFence` / `StartFencer` | `internal/runtime/runtime.go` | the database lease alone. An adapter whose lifecycle resource lives on the CP's host needs an OS-level fence as well |
| `MachineProfile()`, `ResizeHome()` | `workspace_machine.go`, `workspace_home_resize.go` | no machine to name, no disk to grow |
| `BeginHibernate()`, `BackupHome()` | `reaper.go` (idle tiers 3 and 4) | the tiers do not exist for you ([03 §3.7](03-control-plane.md)) |
| `WipeHome()`: a member's Recreate and Clean home, removed where the home is and within the member's request | `internal/runtime/home_wipe.go` (`homeWiper`) | both are refused before the workspace is stopped, and the Console has no Danger zone |
| `EraseHome()`: an administrator's Clean home, removed now and left stopped | `internal/runtime/home_wipe.go` (`homeEraser`) | refused before anything is stopped, and not offered in the member detail |
| `HomeBackups()`, `DeleteHomeBackups()`: copies of a home kept outside it | `internal/runtime/home_wipe.go` (`homeBackupKeeper`) | no backups to show or delete. Clean home never deletes them; Destroy does |
| `GoldenBakePool` / `GoldenSeedRuntime` | `internal/runtime/runtime_ecs_ec2_golden.go` | no golden snapshot is baked |
| `PoolStatus`, `TerminateQuarantinedSlot`, `MaxSlots` | `workspace_lifecycle.go`, `limits.go` | no pool screen, and no check of tenant quotas against a fixed pool |

Only some of these are pinned. `Runtime`, `RuntimeFactory`, `runtimeDestroyer` and the
golden interfaces have compile-time `var _ X = (*T)(nil)` assertions; hibernation is
checked at compile time through an interface value (`runtime.Hibernating`, asserted in
`runtime_seam.go`), the home ports are asserted in `internal/runtime/home_wipe.go`, and
`internal/runtime/capabilities_test.go` asserts which adapters must **not** claim
`DocsMounter`, `GoldenBakePool` or one of the home ports. The rest, sizing and
cost included, are matched only at run time. For each capability you claim or decline,
add an assertion or a test case that fails if that changes.

## 21.4 What is not the adapter's job

- **Idle decisions.** The reaper is common: the adapter supplies `State` and performs
  `Stop`. Only the tiers that need a substrate of their own (hibernation, cross-zone
  backup) are capabilities ([03 §3.7](03-control-plane.md)).
- **Authentication and tenancy.** Both are resolved long before a Runtime is built.
- **Engines.** A deployment has engines because its engine table declares them, not
  because of its runtime. No `control-plane/engine_*.go` file reads `AF_RUNTIME`
  ([09 §9.2](09-deploy.md), [03 §3.9](03-control-plane.md)).
- **Egress policy.** The allowlist, the proxy and the enforce switch are the CP's. What
  your target does own is the network the workspace sits in (what it can reach, and who
  can reach its agent) and whether the proxy variables reach the container (§21.2;
  [07 §7.2](07-security.md), [07 §7.8](07-security.md)).
- **The workspace image** (§21.1).

## 21.5 Outside the adapter: a deploy tree, a runbook, a way to ship

- **`deploy/<target>/`** holds the scripts and templates, with the runbook as its
  `README.md` beside them. [deploy/README.md](../../deploy/README.md) indexes the trees.
- **The runbook reaches a workspace only if `deploy/release/stage-docs.sh` names it.**
  That script's `RUNBOOKS` map is explicit on purpose, not a glob, and it also rewrites
  the guide's links to the runbook and writes the runbook index. Add your runbook to all
  three.
- **Shipping.** `deploy/release/build.sh` builds the compose bundle and images
  (`--compose`) and the native tarball and rootfs (`--native`); the AWS form publishes
  images with `deploy/aws/ecs/release-ecr.sh`. A target that needs a new artefact adds it
  there rather than growing a script of its own.
- **Infrastructure as code lives in the target's own tree, in the provider's own language.** The
  AWS targets are CloudFormation; `deploy/gcp/gke/` is Terraform, next to the plain manifests of
  `deploy/kubernetes/` ([decisions/0106](../decisions/0106-kubernetes-runtime.md) decision 12).
- **New environment variables** go into [09 §9.4](09-deploy.md).

## 21.6 Cost and latency are part of the design

- **The bill to quote to yourself is the one where idle-stop is broken.** A target that
  bills per task needs idle-stop to work for its numbers to hold; the "24/7" row of
  [09 §9.8](09-deploy.md) is that bill.
- **Break a number down before choosing a remedy for it.** The Fargate start was assumed
  to be dominated by the image pull, and lazy image loading was investigated. Measured,
  the pull was the smaller share, so it was not adopted; the first-start 504 was a
  synchronous wait longer than the load balancer's idle timeout, which is why `Start`
  does not wait ([09 §9.5](09-deploy.md) holds the measured breakdown).

## 21.7 Verification

- **`internal/runtime/capabilities_test.go`** fails when an adapter claims staged docs or
  the golden bake that it should not. Add yours there.
- **`internal/runtime/runtime_test.go`** checks that the docker aliases and `ecs` / `aws`
  build their adapters and that an unknown profile is rejected; `native` has its own
  factory tests in `runtime_native_test.go`, and `ecs-ec2` has no case there. Add one
  for your profile.
- **`scripts/docs-check.py` compares the first column of
  [ref/deploy-targets](../../guide/ref/deploy-targets.md) (both languages) with the case
  labels of `NewFactory`**, but only for the profiles named in its
  `unknown AF_RUNTIME profile … (want …)` error. Add your profile to that message, or the
  check never sees it.
- **The fleet E2E suite does not exercise your adapter.** It drives the CP through the
  public API only, but it boots the docker profile and needs docker and a built
  workspace image (`e2e/fleet_test.go`). The AWS adapters have harnesses of their own:
  the `ecs-ec2` live tests (`AF_ECS_EC2_LIVE=1`, set up by `deploy/aws/ecs/harness/`),
  and `deploy/local/ecs-lifecycle-stub-test.sh` for the order the deploy scripts act in.
- **Stand one up and run a real session on it.** The ECS adapters' comments record
  defects that no test showed and a real deployment did. The Service Connect gap in
  `agent_dial.go` is one of them.

## 21.8 Finishing

1. `NewFactory` accepts the profile, the unknown-profile error names it, and
   `runtime_test.go` covers it.
2. [ref/deploy-targets](../../guide/ref/deploy-targets.md) has its row in the first table
   and its column in the capability table, including the honest "—" cells, in both
   languages.
3. The chapters that own per-target facts have your target: [01 §1.6](01-architecture.md),
   [07 §7.2](07-security.md), and in [09](09-deploy.md) the forms (§9.1), the knob (§9.2),
   the environment (§9.4) and the parity table (§9.6).
4. [operate/01](../../guide/operate/01-choose.md) says **when to choose it**, and, if it
   is not the obvious choice, when not to.
5. The runbook sits next to the scripts it operates, and `stage-docs.sh` ships it (§21.5).
6. [decisions/](../decisions/) records why it exists as a separate target rather than a
   flag on an existing one. That question is asked every time.

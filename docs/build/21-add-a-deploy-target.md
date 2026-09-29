---
audience: "someone adding a way to run workspaces"
source_of_truth: "`control-plane/internal/runtime/` — the `Runtime` port and `NewFactory` in `runtime.go`, the optional capabilities the CP probes for, and the adapters themselves (`runtime_*.go`)"
updated: "2026-09"
---

# 21. Adding a deployment target

English | [日本語](21-add-a-deploy-target.ja.md)

A deployment target is a profile value that `runtime.NewFactory`
(`control-plane/internal/runtime/runtime.go`) accepts, with an adapter behind it. Today
the switch knows `docker` (also `local` and the empty value), `native` (`wsl`), `ecs`
(`aws`) and `ecs-ec2`. What each one is and how it is run is [09](09-deploy.md) and
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
| **Start commits; it does not wait for the agent** | `Start` runs inside an HTTP request, so it returns once the launch is committed, never after the ingress idle timeout. A readiness overrun is **not** an error: returning one marks a booting workspace as failed while it runs ([03 §3.3](03-control-plane.md)) |
| **Report `starting` honestly** | All four adapters report it. On docker and native it covers the window where the process is up and the entrypoint has not yet reached the agent; on ECS, a service that is still converging. **While a workspace is `starting`, callers must neither start it again nor idle-stop it.** The adapter has to time-box it, because a `starting` that never converges is a workspace nobody can operate |
| **Keep no state in the Runtime value** | The manager builds a Runtime from the database row (`manager.runtimeFor`), may cache it, and may rebuild it at any time. A restarted CP or a second replica holds a different value. Find everything on the substrate by a deterministic name, a tag or a file, and write anything another request must read back to the substrate too. The existing adapters use `docker inspect`, a pidfile, the ECS service and EC2 tags ([09 §9.5](09-deploy.md)) |
| **Two-stage graceful stop** | Signal, wait `AF_STOP_GRACE_SEC`, then kill, and hand the agent a **shorter** grace (`AGENT_STOP_GRACE_SEC`) so it can interrupt its panes and let tmux exit first ([03 §3.3](03-control-plane.md)) |
| **An endpoint the CP can always reach** | `Endpoint()` has to resolve from every CP replica, including for a workspace created after the CP started. On ECS, a Service Connect alias does not do that for a service added later, and `agent_dial.go` exists to cover the gap |
| **Deliver the env you were built with, at every start** | The factory's `New(ws, secretKey, extraEnv)` carries the DEK and per-workspace variables, and they differ from one start to the next (an unattended scheduler wake, a preview slug, the egress proxy variables). Container env is fixed at the instant of the start, so it has to go in there. **Never put the DEK or `AGENT_TOKEN` where the substrate can show it**: docker passes them in a 0600 env file rather than on the command line, and ECS passes an SSM reference rather than the value ([09 §9.5](09-deploy.md), [07 §7.6](07-security.md)) |
| **Two persistent areas, surviving a stop** | The home at `/home/dev`, and Claude's state at `/var/lib/af/claude` (`CLAUDE_CONFIG_DIR`). The second one is kept apart from the home so that the file browser cannot reach it and a reset of the home leaves the Claude login alone. **Operations that change the home have to reach the real home**, wherever your target keeps it. How each existing target stores them is [01 §1.6](01-architecture.md) and [07 §7.2](07-security.md) |
| **Destroy** | `runtimeDestroyer` is required of every adapter and asserted in `runtime.go`. It removes the home and every per-membership resource you created. Its `[]string` result lists what you **know** you could not remove, so it reaches the audit log instead of an operator assuming the data is gone |
| **Per-user isolation, or refuse to run shared** | Answer every row of [07 §7.2](07-security.md) for your target. Where you cannot, do what `native` does: the factory refuses any `AUTH` other than `dev`, because without a container boundary nothing separates users |

## 21.3 Optional capabilities: claim what is true

The CP asks for most per-target behaviour with a type assertion (`rt.(X)` on a Runtime,
`m.rtFactory.(X)` on the factory) and branches on the answer. **An adapter that does not
claim a capability still compiles; the feature is simply absent.** So claiming one is a
statement about your substrate.

What the four adapters claim at the time of writing. The interfaces are the real list:
grep `control-plane/*.go` for `rt.(` and `rtFactory.(`.

| Capability | Declared in | Claimed by | Without it |
|---|---|---|---|
| `SizingProfile()`: what CPU, memory and disk mean here | `workspace_sizing.go` (`sizingProfiler`) | all four factories | you are described as docker |
| `CostProfile()`: is there a bill, and what it covers | `cost_profile.go` (`costProfiler`) | all four factories | no cost view, and version info reports the runtime as `local` |
| `WorkspaceImage()` | an inline interface in `main.go` and `version_info.go` | `ecs`, `ecs-ec2` | the startup banner names the docker template's image, and version info leaves the workspace image out |
| `DocsMounter`: the guide is bind-mounted | `internal/runtime/runtime.go` | docker, native | the container pulls it from `GET /internal/docs` ([04 §4.9](04-agent.md)). Claiming it without a host path the container can see leaves the guide empty |
| `Stale()`: would a stop and start run different code | `workspace_stale.go` | all four | never reported stale |
| `BootPhase()` | an inline interface in `workspace_handlers.go` | native, `ecs-ec2` | the start dialog shows no phase |
| `AcquireOperationFence` / `StartFencer` | `internal/runtime/runtime.go` | native | the database lease alone. An adapter whose lifecycle resource lives on the CP's host needs an OS-level fence as well |
| `MachineProfile()`, `ResizeHome()` | `workspace_machine.go`, `workspace_home_resize.go` | `ecs-ec2` | no machine to name, no disk to grow |
| `BeginHibernate()`, `BackupHome()` | `reaper.go` (idle tiers 3 and 4) | `ecs-ec2` | the tiers do not exist for you ([03 §3.7](03-control-plane.md)) |
| `GoldenBakePool` / `GoldenSeedRuntime` | `internal/runtime/runtime_ecs_ec2_golden.go` | `ecs-ec2` | no golden snapshot is baked |
| `PoolStatus`, `TerminateQuarantinedSlot`, `MaxSlots` | `workspace_lifecycle.go`, `limits.go` | `ecs-ec2` | no pool screen, and no check of tenant quotas against a fixed pool |

The claiming direction is pinned with `var _ X = (*T)(nil)` next to the declaration.
The not-claiming direction cannot be written that way, so
`internal/runtime/capabilities_test.go` asserts it. Add your adapter to both.

## 21.4 What is not the adapter's job

- **Idle decisions.** The reaper is common: the adapter supplies `State` and performs
  `Stop`. Only the tiers that need a substrate of their own (hibernation, cross-zone
  backup) are capabilities ([03 §3.7](03-control-plane.md)).
- **Authentication and tenancy.** Both are resolved long before a Runtime is built.
- **Engines.** A deployment has engines because its engine table declares them, not
  because of its runtime. No `control-plane/engine_*.go` file reads `AF_RUNTIME`
  ([09 §9.2](09-deploy.md), [03 §3.9](03-control-plane.md)).
- **Egress policy.** The proxy variables reach every workspace through `extraEnv`. What
  your target does own is the network the workspace sits in: what it can reach, and who
  can reach its agent ([07 §7.2](07-security.md), [07 §7.8](07-security.md)).
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
- **`internal/runtime/runtime_test.go`** checks which adapter each profile builds and
  that an unknown profile is rejected. Add your profile's case.
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

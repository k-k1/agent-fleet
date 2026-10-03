---
audience: "everyone; decisive for operate/"
source_of_truth: "this table; the rows are checked against the runtime profiles the Control Plane accepts"
updated: "2026-10"
---

# Deployment targets — what exists where

English | [日本語](deploy-targets.ja.md)

One core runs on every target; only the edge adapter changes. What differs is
therefore small and specific — and worth stating precisely, because "it works on my
deployment" is the most expensive kind of documentation error here.

| Target | A workspace is | Home lives on | Choose it when |
|---|---|---|---|
| docker | a container on the host's Docker daemon | a bind-mounted directory on the host | the on-prem default: one host, a team sharing it |
| native | sandboxed host processes, no Docker at all | a directory on the host | Docker cannot be installed (a plain WSL2 machine). **Single user only** — without container isolation it refuses to run in a shared mode |
| ecs | a task on AWS ECS / Fargate | EFS | AWS, without managing instances |
| ecs-ec2 | a task on an EC2 slot taken from a pool | a per-user EBS volume | AWS, when start latency and disk performance matter enough to manage instances |
| kubernetes | a pod of its own StatefulSet on a Kubernetes cluster | a per-user persistent volume (block storage), plus a second one for logins and Claude's state | you already run Kubernetes, or want Agent Fleet on Google Cloud with workspaces that scale to zero. GKE Standard is the first cluster it is verified on |

`docker` also answers to `local`, `ecs` to `aws`, `native` to `wsl`, and `kubernetes` to `k8s`. Anything else
is rejected at boot rather than quietly defaulting. `ecs` and `ecs-ec2` are separate
profiles on purpose, not a flag: the EC2 pool trades a proven two-resource workspace
for a six-resource one, so a deployment opts in and can fall back by changing this one
value instead of reverting code.

## Capability differences

| Capability | docker | native | ecs | ecs-ec2 | kubernetes |
|---|:--:|:--:|:--:|:--:|:--:|
| Several users, mutually invisible | ✓ | — | ✓ | ✓ | ✓ |
| Per-user CPU / memory limits | ✓ | — | ✓ | ✓ | ✓ |
| Per-user disk sizing | — | — | ✓ | ✓ | ✓¹¹ |
| Idle auto-stop | ✓ | ✓ | ✓ | ✓ | ✓ |
| Stop / start preserving home | ✓ | ✓ | ✓ | ✓ | ✓ |
| The user guide inside the container | ✓¹ | ✓¹ | ✓² | ✓² | ✓² |
| Browser pane | ✓ | ✓³ | ✓ | ✓ | ✓¹² |
| Cost attribution per member | — | — | ✓ | ✓ | — |
| An image engine the deployment provides | ✓⁴ | ✓⁴ | — | ✓⁵ | ✓⁴ |
| A chat engine the deployment provides | ✓⁶ | ✓⁶ | — | ✓⁶ | ✓⁶ |
| A member's Recreate and Clean home (Danger zone) | ✓ | ✓ | ✓⁷ | ✓¹⁰ | ✓¹³ |
| Clean home by an administrator (offboarding) | ✓ | ✓ | ✓⁷ | ✓⁸ | ✓¹³ |
| Deleting the backup copies of a member's home | — | — | — | ✓⁹ | — |

¹ Staged on the host and bind-mounted at start.

² There is no host path to mount into a task, so the container fetches the identical
tree from the Control Plane over an internal endpoint instead. One tree, two delivery
mechanisms.

³ The lean image used by `native` does not bake Chromium; it is downloaded on demand
the first time.

⁴ A ComfyUI **already running on your own network**, pointed at with one environment
variable — or several engine rows side by side: a LAN ComfyUI, a borrowed engine, an
OpenAI-compatible server ([operate/07](../operate/07-image-engine.md)).

⁵ The fleet's own GPU, bought when something asks for it. There is no equivalent on
Fargate. On every target a session can also generate images on **the member's own CLI
plan** (Codex / Antigravity); this row is about an engine the deployment provides.

⁶ On `ecs-ec2`, the fleet's own GPU. On `docker`, `native` and `kubernetes`, a llama.cpp
**already running on your own network**, pointed at with one environment variable
([operate/09](../operate/09-llm-lan.md)).

⁷ The Control Plane cannot mount the member's EFS home itself, so it starts a short task the
stack declares (`HomeOpsTaskDef` in `30-ingress`) that mounts the file system and removes the
files. A Fargate task takes a few minutes to start, so these finish after the button has been
answered. A member's Recreate and Clean home show the starting dialog ("removing what Recreate /
Clean home deletes") until the workspace is up again; if the removal fails the workspace stays
stopped and the reason is shown, to the member and on their row in the administrator's member
list, until the next start (a Control Plane restart after the failure does not lose it). An
administrator's Clean home and **Destroy workspace** answer straight away, and their outcome is
written to the audit log when the task has finished. Destroy now removes the member's EFS
directories as well, instead of listing them as left over. A stack from before this task (no
`AF_ECS_HOME_TASK`) does not offer these buttons, as before. On `ecs-ec2` the home itself is on
EBS, but the Claude state and the kept logins and connections are on EFS: **Destroy workspace**
removes those with the same task, so there too it answers straight away and writes its outcome
to the audit log.
While a task runs, a start and any second operation on that home are refused, even across a
Control Plane restart. What a restart during the task does lose is the step after it: a member's
workspace is not started again (press Start once the task has finished), the reason for a failure
is not shown, a Destroy leaves the workspace row (run Destroy again; it is safe to repeat), and
the audit log has the request but no outcome. The task's own log (the workspace log group, stream
prefix `home-ops`) says how it ended.
The home is released only when the Control Plane sees its task stopped. If that can no longer
happen — ECS has forgotten a task nobody checked on for over an hour, or the answer to starting it
was lost — the home stays refused with "an operation on this workspace's home is still running",
and the Control Plane log names the record to clear. An operator who has checked in ECS that no
task started by `af-home/<membership>` is running deletes the SSM parameter
`/af-ws/<workspace>/home-task`, and the home is usable again.

⁸ Deletes the member's home volume and its hibernation copies; the next start builds a
fresh home, as for a new member. On this target the logins, connections and Claude state
are kept on EFS, outside the volume, so they survive as they do everywhere else. A file
among them that a tool replaced since the workspace last started is on the volume until
the next start, and goes with it.

⁹ Only `ecs-ec2` keeps backup copies of a home, and only when the operator has turned
backups on. Clean home leaves them: deleting them is a separate action in the member's
detail. Discarding the workspace deletes them as well.

¹⁰ The request marks the home and returns; the start it triggers removes the files after
the home is mounted and before the workspace runs, so the starting dialog shows the removal
as a step of the start, and a large home makes that start longer. This works whether the
member's machine was asleep, the home was detached, or it had been put away as a
hibernation copy. If the removal fails, the workspace stays stopped and the next start
tries again; it never starts with what was to be removed. While a start is still in
progress both are refused without stopping anything; press again once it has started.
Clean home keeps the logins and connections by name, including one a tool replaced since
the last start. After Clean home the first start reinstalls the agent CLIs, as on `docker`.

¹¹ The size of the home volume. It can grow, never shrink.

¹² As on `ecs`, without the extra privilege `docker` grants Chromium's sandbox; whether the pane
behaves the same on every cluster is still being measured.

¹³ A member's Recreate and Clean home mark the home and return; the next start removes the files
before the workspace runs, as on `ecs-ec2`. An administrator's Clean home removes them at once,
with the workspace stopped. Both keep the logins, connections and Claude's state, which live on a
second volume of their own. The home volume itself is kept.

## Where the procedure lives

Until [operate/](../operate/README.md) is written, the runbooks are still in the
repository next to what they operate:

| Target | Runbook |
|---|---|
| docker (compose) | [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | [deploy/native/README.md](../../deploy/native/README.md), and [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) for a personal WSL2 machine |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| a single EC2 VM running compose | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |
| kubernetes (GKE, and other clusters) | [deploy/kubernetes/README.md](../../deploy/kubernetes/README.md) |

`ec2-single` is not a separate runtime profile — it is `docker` on a VM, and it exists
because "AWS" and "manage instances yourself" are independent choices.

## What is the same everywhere

The workspace image and the agent inside it are the same artefact on every target;
that is the whole point of the split. Isolation strength, storage performance and how
egress is controlled are where the targets genuinely differ, and those are properties
of the substrate rather than of Agent Fleet.

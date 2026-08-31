# Deployment targets — what exists where

English | [日本語](deploy-targets.ja.md)

Audience: everyone; decisive for [operate/](../operate/README.md)
Source of truth: this table; the rows are checked against the runtime profiles the Control Plane accepts
Updated: 2026-08

One core runs on every target; only the edge adapter changes. What differs is
therefore small and specific — and worth stating precisely, because "it works on my
deployment" is the most expensive kind of documentation error here.

| Target | A workspace is | Home lives on | Choose it when |
|---|---|---|---|
| docker | a container on the host's Docker daemon | a bind-mounted directory on the host | the on-prem default: one host, a team sharing it |
| native | sandboxed host processes, no Docker at all | a directory on the host | Docker cannot be installed (a plain WSL2 machine). **Single user only** — without container isolation it refuses to run in a shared mode |
| ecs | a task on AWS ECS / Fargate | EFS | AWS, without managing instances |
| ecs-ec2 | a task on an EC2 slot taken from a pool | a per-user EBS volume | AWS, when start latency and disk performance matter enough to manage instances |

`docker` also answers to `local`, `ecs` to `aws`, and `native` to `wsl`. Anything else
is rejected at boot rather than quietly defaulting. `ecs` and `ecs-ec2` are separate
profiles on purpose, not a flag: the EC2 pool trades a proven two-resource workspace
for a six-resource one, so a deployment opts in and can fall back by changing this one
value instead of reverting code.

## Capability differences

| Capability | docker | native | ecs | ecs-ec2 |
|---|:--:|:--:|:--:|:--:|
| Several users, mutually invisible | ✓ | — | ✓ | ✓ |
| Per-user CPU / memory limits | ✓ | — | ✓ | ✓ |
| Per-user disk sizing | — | — | ✓ | ✓ |
| Idle auto-stop | ✓ | ✓ | ✓ | ✓ |
| Stop / start preserving home | ✓ | ✓ | ✓ | ✓ |
| Role-scoped documentation in the container | ✓¹ | ✓¹ | ✓² | ✓² |
| Browser pane | ✓ | ✓³ | ✓ | ✓ |
| Cost attribution per member | — | — | ✓ | ✓ |

¹ Staged on the host and bind-mounted at start.

² There is no host path to mount into a task, so the container fetches the identical
subset from the Control Plane over an internal endpoint instead. Same decision, two
delivery mechanisms, one implementation of "what may this role see".

³ The lean image used by `native` does not bake Chromium; it is downloaded on demand
the first time.

## Where the procedure lives

Until [operate/](../operate/README.md) is written, the runbooks are still in the
repository next to what they operate:

| Target | Runbook |
|---|---|
| docker (compose) | [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | [deploy/native/README.md](../../deploy/native/README.md), and [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) for a personal WSL2 machine |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| a single EC2 VM running compose | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |

`ec2-single` is not a separate runtime profile — it is `docker` on a VM, and it exists
because "AWS" and "manage instances yourself" are independent choices.

## What is the same everywhere

The workspace image and the agent inside it are the same artefact on every target;
that is the whole point of the split. Isolation strength, storage performance and how
egress is controlled are where the targets genuinely differ, and those are properties
of the substrate rather than of Agent Fleet.

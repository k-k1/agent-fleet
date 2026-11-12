---
audience: "a deployment administrator of an AWS ECS deployment who sets the default CPU, memory and working disk of a workspace"
source_of_truth: "the parameters of `deploy/aws/ecs/cfn/30-ingress.yaml` and the Control Plane's ECS adapter (`control-plane/internal/runtime/runtime_ecs.go`)"
---

# Set the default workspace size

English | [日本語](workspace-size.ja.md)

Set the CPU, memory and working disk every workspace of an ECS deployment gets by default.
A tenant administrator changes the size per member in the Console ([Set the workspace size](../../admin/resources/workspace-size.md)); that value is clamped by the tenant cap.

## How it works

| What you do | What happens |
|---|---|
| Set three `30-ingress` parameters | The Control Plane uses them as each workspace's default |
| Give the working disk 30 GiB or more | Build caches and artifacts move off EFS onto the working disk |
| Ask for an odd CPU and memory pair | The Control Plane snaps it onto a pair ECS accepts |

## Parameters

| Parameter | Control Plane variable | Default | What it is |
|---|---|---|---|
| `WsTaskCpu` | `AF_ECS_TASK_CPU` | `1024` | Fargate units; 1024 is 1 vCPU |
| `WsTaskMemory` | `AF_ECS_TASK_MEMORY` | `2048` | MiB |
| `WsDiskGiB` | `AF_ECS_WS_DISK_GB` | `50` | 21–200 sets the working disk (ephemeral storage); 0 is Fargate's free 20 GiB (cache relocation off) |

## CPU and memory pairs

ECS accepts only specific CPU and memory pairs, and the steps are not uniform.

| CPU | Memory it accepts, for example |
|---|---|
| 0.25 vCPU | 512, 1024 and 2048 MiB only |
| 8 vCPU | steps of 4096 MiB |
| 16 vCPU | steps of 8192 MiB |

- The Control Plane snaps any request onto a valid pair, so an odd value never reaches a task definition
- The visible effect is that **raising CPU can also raise memory**: 4 vCPU cannot run below 8 GiB

## The working disk

The working disk is **not persistent**: it is wiped when the workspace stops. Only the EFS home survives.

`WsDiskGiB` is also the switch for moving small-file trees off EFS onto that disk.

| What moves | Who moves it |
|---|---|
| The Go build cache, `uv`, Go modules | The workspace entrypoint |
| `node_modules`, `target`, `.venv`, `build` | The Agent, as each working copy is created |

- Both arm only when the disk is **30 GiB or more**: the caches do not fit alongside the image in 20 GiB
- On EFS these trees are 8–30× slower to write
- The default is 50 GiB, so relocation is on

### Cost

- Above the free 20 GiB you pay about $0.097 per GiB-month, **only while the task runs**
- Roughly $2.9 a month for a workspace that never stops, under a dollar for one that idles out
- A deployment that does not want it sets `WsDiskGiB=0` and behaves as before, with no relocation

### Existing stacks keep their value

CloudFormation stores the parameter, so a stack created with `0` stays at `0` when you update it. To get 50 GiB, pass `WsDiskGiB=50` explicitly when you update.

## Disks above 200 GiB

Above 200 GiB the Control Plane switches the working disk to an ECS-managed EBS volume.

- It needs an ECS infrastructure role (policy `AmazonECSInfrastructureRolePolicyForVolumes`) passed as `AF_ECS_INFRA_ROLE`
- The reference stacks do not create that role. Without it a request above 200 GiB silently falls back to the free default
- This path is untested on real infrastructure. Ephemeral storage covers everything up to 200 GiB

## Related

- [Set the workspace size](../../admin/resources/workspace-size.md): the size per member (tenant administrator)
- [Keep costs down](cost.md)
- [Limits and defaults](../../ref/limits.md)

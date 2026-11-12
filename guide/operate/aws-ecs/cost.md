---
audience: "a deployment administrator who estimates and lowers the monthly cost of an AWS ECS deployment"
source_of_truth: "the templates in `deploy/aws/ecs/cfn/` (the NAT gateway and S3 gateway endpoint in `00-network.yaml`, `Persistence` in `10-data.yaml`) and AWS's price lists"
---

# Keep costs down

English | [日本語](cost.ja.md)

Know what an ECS deployment costs while nobody uses it and what grows with use, and pick the levers that lower it.
Figures are list prices for Tokyo (ap-northeast-1) at 730 hours a month. Prices change: recompute them with your region's rates. us-east-1 is roughly 30% lower.

## How it works

| What you do | What happens |
|---|---|
| Nothing | A stopped workspace costs nothing. Four always-on pieces cost about $107 a month, plus EFS usage |
| Pause with `pause.sh` for a period of no use | The NAT, the Control Plane, RDS compute, slots and GPUs stop; only the ALB and storage keep billing |
| Run a deployment with many starts on `ecs-ec2` | The slot's image cache cuts the NAT traffic each start pays |
| Build production with `Persistence=retain` | EFS and RDS outlive a stack deletion and are backed up daily |

## The standing cost

With every workspace stopped and nobody logged in:

| Always-on | Rate | $/month |
|---|---|---|
| NAT Gateway ×1 | $0.062/h | **45.3** |
| **Control Plane / Console Fargate task ×1** (0.5 vCPU + 1 GB, `DesiredCount: 1`) | $0.05056/vCPU-h + $0.00553/GB-h | **22.5** |
| RDS db.t4g.micro Single-AZ | $0.025/h | **18.3** |
| ALB ×1 | $0.0243/h (+ $0.008/LCU-h, ~0 when idle) | **17.7** |
| RDS storage 20 GiB | $0.138/GB-mo | 2.8 |
| Secrets Manager (RDS-managed password) | $0.40/secret | 0.4 |
| Cloud Map private namespace (Route 53 private zone) | — | 0.5 |
| ECR (Control Plane + workspace ≈ 1.05 GB) | $0.10/GB-mo | 0.1 |
| EFS (every user's home persists) | $0.36/GB-mo | usage |
| EFS backups (`Persistence=retain` only) | $0.06/GB-mo warm backup storage | usage |
| **Total** | | **≈ $107/mo + EFS** |

- Stopped workspaces cost **nothing**. ACM, the NAT's EIP and standard SSM parameters are free
- A **running** workspace adds Fargate at **$0.0616/h** (1 vCPU + 2 GB): about $11 a month at 8 h × 22 days, $45 a month if left on around the clock
- The floor is the four always-on pieces, which do not care how many users you have

## Levers that lower it

| Lever | Saving | Price |
|---|---|---|
| Replace the NAT gateway with a **NAT instance** (t4g.nano $3.9 + 8 GB gp3 $0.8 + its public IPv4 $3.7 ≈ **$8 a month**) | about $37 a month (the managed gateway is **$45**) | You run it yourself: source/dest check off, iptables MASQUERADE, an ASG for replacement plus a route rewrite |
| Drop the Control Plane task to 256/512 | about $11 a month | Not verified that it fits |
| Pause for a period of no use | The NAT, Control Plane, RDS compute, slots and GPUs | The ALB, EFS and RDS storage stay ([Stand up, pause and tear down with the scripts](lifecycle.md)) |
| For a trial, deploy → E2E → `delete-stack` and keep stacks short-lived | Nearly everything | You rebuild each time |

The ALB and RDS are effectively the floor. Aurora Serverless v2 costs more at its 0.5-ACU minimum.

### One or two users: a single EC2 instance is cheaper

| Setup | Per month |
|---|---|
| A single EC2 instance (`deploy/aws/ec2-single`): t3.medium + 30 GB gp3 + EIP, **workspaces included** | about $47 |
| The same with t3.large (the default) | about $87 |
| A trimmed ECS deployment | about $72, plus workspace hours |

ECS wins on cost only around **8–10 concurrent users**: the single VM has to be sized for everyone's peak 24/7, while Fargate bills only the hours each workspace runs. ECS also gives you its isolation regardless of price.

## Traffic through the NAT

Workspaces reach git and Anthropic (the public internet), so a NAT is needed. Private-subnet tasks have no public IP, so they also reach ECR, CloudWatch Logs and SSM through it: the NAT is on the **workspace start path**, not just the member's internet access.

| Traffic | Path |
|---|---|
| git, Anthropic, npm, package registries (the real developer traffic) | NAT |
| The small ECR auth and manifest, Logs and SSM calls | NAT |
| Image layers for a Fargate workspace | NAT (pulled through the task ENI, so it cannot be split) |
| Image layers for an `ecs-ec2` workspace | S3 gateway endpoint (pulled through the slot's ENI) |
| `60-engines` reads of the models bucket, and writes of the egress record to S3 | S3 gateway endpoint |
| Hugging Face / Civitai downloads in a `60-engines` ingest | NAT |

Interface endpoints (ECR, Logs, SSM and the like) are left out on purpose: $0.01/AZ/hour × 2 AZ each adds up past the NAT gateway they would relieve.

### What each start costs

Fargate keeps no image cache, so every start pulls the whole image (about 918 MiB compressed) through the NAT, and NAT data processing is billed per GB.

- At $0.045–0.062 per GB, 918 MiB ≈ 0.9 GB is **$0.04–0.06 per start**
- 100 starts a day (about 3,000 a month) is roughly **$120–180 a month** on top of the gateway's hourly charge
- The rate varies by region and changes. Take it from the "NAT Gateway" section of [Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/); these figures are an illustration, not a quote

On `ecs-ec2` the slot's own root volume is the image cache.

- Restarting on a slot that already holds the same image avoids the layer pull. A cached start measured about 65 MiB of NAT traffic in total on a test deployment
- A new slot, a recreated one, a cache miss or an image update pulls the layers again, but through the S3 gateway endpoint; only the small ECR auth and manifest calls cross the NAT (verified on a test deployment, not yet in production)
- Where many starts a day are expected, prefer `ecs-ec2`
- The golden snapshot is a different thing: a home that already paid the boot-time install. It saves time, not NAT bytes

How a NAT gateway compares with the egress gateway is in [Control outbound traffic with a gateway](egress-gateway.md#how-it-differs-from-the-nat-gateway).

## Turn on the S3 gateway endpoint

Workspaces have subnets of their own.

- `00-network` gives each AZ a workspace subnet on a workspace route table whose only way out is the NAT, and the Control Plane puts workspace task ENIs there (`AF_ECS_WORKSPACE_SUBNETS`, from `30-ingress`'s `WorkspaceSubnets`)
- The S3 gateway endpoint is on the **private route table only** (the Control Plane, `ecs-ec2` slot instances, the inference engines, the home task). Its policy admits ECR layers, the AL2023 repositories, the SSM Agent update and ECS agent download buckets and the models bucket, and nothing else
- A refused same-region request there is AccessDenied, not NAT traffic, which is why no workspace may sit on that route table. A workspace's own S3 always leaves through the NAT (or the egress gateway), where it can be recorded
- The endpoint has a switch, `S3EndpointEnabled` (default `true`). Keep it `false` while any workspace still runs in a private subnet: the policy refuses that workspace's own S3 until it is restarted into a workspace subnet

### Move a running stack

Plan a maintenance window; every running workspace is restarted.

1. Deploy `cfn/00-network.yaml` with `S3EndpointEnabled=false` through a change set

   ```bash
   aws cloudformation deploy … --capabilities CAPABILITY_IAM --parameter-overrides \
     S3EndpointEnabled=false --no-execute-changeset
   ```

   Then read it with `describe-change-set`.
   - It adds six resources: two subnets, the workspace route table, its default route and two associations
   - A stack that already has an S3 endpoint also shows *Remove* `S3Endpoint`. That is expected, and puts private-subnet S3 on the NAT for the window
   - Anything else modified or replaced: stop. Otherwise execute it
   - Never skip this step with a deploy at the defaults: `S3EndpointEnabled=true` would *Modify* `S3Endpoint` to the restricted policy while every workspace is still in a private subnet
2. Run `update.sh`. It passes the network stack's `WorkspaceSubnets` to the Control Plane (`AF_ECS_WORKSPACE_SUBNETS`). New starts now go to the workspace subnets, and it ends by listing the running workspaces still in a private subnet
3. Stop and start every workspace that was running (`pause.sh --status` lists them, or `aws ecs list-services` with `desiredCount > 0`). A running task keeps its ENI and moves only on a new start. Check that each task's ENI is in a workspace subnet

   ```bash
   aws ecs describe-tasks --cluster <c> --tasks <t> --query \
     "tasks[].attachments[].details[?name=='subnetId'].value"
   ```

4. Deploy `cfn/00-network.yaml` with `S3EndpointEnabled=true` (one *Add*: `S3Endpoint`). Private-subnet S3 moves from the NAT route to the endpoint route, and open TCP connections do not survive that, so do it when no large model fetch or ingest, or image pull, is running

| Case | What happens |
|---|---|
| Rolling back | `S3EndpointEnabled=false` puts every private-subnet S3 request back on the NAT. After step 1 that is a stack update that removes only `S3Endpoint`. Leave the workspace subnets and route table in place while any workspace may still use them; removing them is a template change, not a parameter |
| A new stack | `standup.sh` with no existing network stack turns the endpoint on from the start, before any workspace exists |
| Pause and resume | `pause.sh` and `standup.sh` pass `S3EndpointEnabled=false` to a live network stack that has never had the switch, so neither turns it on as a side effect. `pause.sh --up` warns when the network has workspace subnets the Control Plane is not using |
| While paused (`NatEnabled=false`) | S3 is still reachable from the private subnets through the endpoint (for what its policy admits), unlike ECR's API, logs and the workspace route table's way out |

## Decide whether data survives

`10-data.yaml`'s `Persistence` parameter decides.

| Value | What it means |
|---|---|
| `delete` (default) | Throwaway, for trials: EFS and RDS go with the stack, and there are no backups |
| `retain` | Production: EFS `Retain` plus a daily AWS Backup kept `EfsBackupRetentionDays` (7) days; RDS `Snapshot`, 7-day backups and deletion protection |

**Deploy production with `Persistence=retain`.** Backup and restore are in [Back up and restore EFS](backup.md).

## Related

- [Stand up, pause and tear down with the scripts](lifecycle.md)
- [Control outbound traffic with a gateway](egress-gateway.md)
- [Set the default workspace size](workspace-size.md)
- [Choose where to deploy](../choose.md)

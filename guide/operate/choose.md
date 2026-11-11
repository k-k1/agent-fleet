---
audience: "someone deciding how and where to run a deployment"
source_of_truth: "ref/deploy-targets.md for what each target supports; this page for how to choose between them"
---

# Choose where to deploy

English | [日本語](choose.ja.md)

Decide what Agent Fleet runs on. You can change it later, but not for free, so spend twenty minutes on it first.

## How it works

| What you decide | What decides it |
|---|---|
| The target (compose, native, ec2-single, ecs, ecs-ec2, kubernetes) | compose on one Linux host unless you have a reason; otherwise the table below |
| On AWS, ec2-single or ECS | Not cost, but whether you need isolation and per-member fault isolation |
| On ECS, Fargate or ecs-ec2, and the egress route | How often workspaces start, and whether you want outbound traffic recorded and restricted |

What each target can do is in [Deployment targets](../ref/deploy-targets.md). This page is only how to choose.

## Start with compose

**Unless you have a specific reason not to, choose compose on one Linux host.** It is the default, the most used, and the one every other target is described against.
All it needs is one host with Docker, a domain name, and an hour.

## When to choose another target

| If | Choose | Why |
|---|---|---|
| Docker cannot be installed on the machine | **native** | No containers. **Single user only**: without a container boundary it refuses to start in a shared mode |
| The machine is a Mac (Apple Silicon) | **compose** with the macOS overlay, or **native** inside a Linux VM | **Single user only**, on `127.0.0.1`. Checked with colima, OrbStack and Docker Desktop, and natively in Lima and OrbStack VMs. Intel Macs are not covered. See "macOS" in the compose runbook |
| You want it on AWS, the team is small, and cost matters | **ec2-single** | It is compose on a VM, not a separate runtime |
| You need task-level isolation, per-member fault isolation, rolling image replacement | **ecs** | What you buy is isolation, not savings |
| All of the above, plus fast starts and disk performance | **ecs-ec2** | A pool of instances with a persistent disk per member |
| Your platform is Kubernetes or Google Cloud, and you want workspaces that scale to zero | **kubernetes** (preview) | On a cluster you run, a workspace is a pod with a persistent volume per member. GKE Standard comes first |

## Choose on AWS cost

ECS is often assumed to be cheaper; it is the other way round. Monthly figures:

| Team size | ec2-single | ECS (weekdays, 8 hours) | Verdict |
|---|---|---|---|
| up to 5 | **$87** (t3.large, with a 30 GB disk and a static IP) | $154 | **ec2-single, no contest**. ECS cannot earn back its $110 floor |
| up to 15 | $283 (8 vCPU / 32 GB) | $240 | Roughly even. Decide on operational effort and isolation needs |
| 20 | $283–368 | **$285** | About the same. This is the crossover, and past it things other than cost decide |
| 20, running 24/7 | $283 | $830 | If it never idles, one VM is far cheaper |

- **ECS almost never wins on cost.** You choose ECS for task-level isolation, per-member fault isolation, rolling image replacement, and a tighter instance metadata and role posture
- Scale-to-zero is not what makes it cheap; it is what dilutes ECS's premium down to "about the same as a VM, at twenty people"
- A small single team goes with **ec2-single**. It is compose on a VM, so you run it the way you run compose
- If you need isolation or per-member availability, choose **ECS**, accepting the $110 floor and the fact that the figures above only hold while idle auto-stop actually works

### Fargate starts are billed by the NAT

This cost is not in the table above.

- A Fargate workspace on AWS keeps no image cache, so **every start pulls the whole workspace image (about 918 MiB compressed) through the NAT gateway**
- The NAT bills the data it processes. At $0.045–0.062 per GB, that is about $0.04–0.06 per start, or about $120–180 a month at 100 starts a day
- Rates differ by region. Check the NAT Gateway section of [Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/) for yours
- Model downloads for self-hosted engines (`60-engines`) from Hugging Face or Civitai cross the NAT too

Where starts are frequent, choose **ecs-ec2**.

| How it starts | What it pulls |
|---|---|
| On an existing slot that already has the same image | No layers again. About 65 MiB of NAT traffic in total |
| On a new slot, a recreated slot, a cache miss, or after an image update | The image again. But workspaces have subnets of their own, so the layers come through a free S3 VPC endpoint rather than the NAT. This was checked on a test deployment only |

- The golden snapshot is something else. It shortens the CLI install time, not NAT traffic
- Workspaces can also leave through an egress gateway instead of the NAT gateway (next section)
- The setup is in [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md)

### Choose the egress route on AWS

`00-network`'s `EgressRoute` decides how **workspaces** reach the internet. The Control Plane and the AWS hosts use the NAT gateway either way.

| | NAT gateway (`nat-gateway`, the default) | Egress gateway (`gateway`) |
|---|---|---|
| Recording | None. The **Admin** "Egress" tab stays empty | Every workspace connection, by name and address, attributed to the workspace. The "Egress" tab shows counts per host; the attributed events are at `GET /api/admin/egress/events` |
| Allowlist | Not enforced | log-only or enforce |
| Hourly cost | The NAT gateway | The NAT gateway (still used by the Control Plane and hosts), plus a small instance and its address |
| Per-GB cost | NAT data processing on all workspace traffic, S3 included | None for workspace traffic, only normal data transfer |
| Operations | Managed by AWS | An instance to run. While it is replaced, workspaces cannot reach out for a few minutes |
| Availability | AWS-managed, one Availability Zone | One instance in one Availability Zone, replaced automatically |
| Non-HTTP traffic (SSH git, databases) | Works | In enforce, only what an administrator names by destination and port (`tcp:` entries) passes. Recorded in either mode |
| Address for the other side's allowlist | The NAT gateway's | Two: the NAT gateway's (Control Plane and hosts) and the gateway's (workspaces). You can allocate and register the latter before switching |

- The egress gateway is new and was checked on a test deployment only. **It is not yet used in production**
- `pause.sh` stops the gateway too, and brings it back with the same source address
- Setup and limits: "Egress gateway" in [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md)

## When Kubernetes, and when not

> [!NOTE]
> The `kubernetes` runtime is a preview, not yet supported for production.
> Its known limits, what a load-balancer cut does to a terminal, and its cost are listed in [Deployment targets](../ref/deploy-targets.md).

**Choose it when**

- Your organisation already runs things on a Kubernetes cluster
- You are on Google Cloud and want what ECS gives on AWS: a pod per workspace, isolated by the cluster, with capacity only while it runs

**What you take on**

- Terraform and kustomize to learn, if you do not use them yet
- The same kind of floor cost as ECS: a node pool, a managed database, a load balancer
- The preconditions the cluster must meet for workspaces to be isolated. The runbook checks them off one by one

**Do not choose it**

- To save money on a small team (one VM is cheaper on any cloud)
- On a cluster you cannot dedicate a namespace and a node pool to
- On a cluster whose network plugin does not enforce NetworkPolicy

A home lives in one zone and is not covered by backups.

## What is painful to change later

| What | Why |
|---|---|
| `AF_MASTER_KEY` | Lose it and every stored credential is permanently undecryptable. It goes in a vault separate from the data, never into a backup |
| The absolute `DATA_DIR` path | Restores depend on its basename. Moving it is a procedure, not a settings change |
| Upgrades | They are one-way. There is no downgrade, so back up first every time |

The domain, the sign-in methods, limits and sizing are ordinary settings you can change later.

## Related

- [Self-hosting at a glance](overview.md#where-the-commands-are): the runbook for each target
- [Install Agent Fleet](install.md)
- [Deployment targets](../ref/deploy-targets.md)

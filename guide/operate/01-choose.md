---
audience: "someone deciding how and where to run a deployment"
source_of_truth: "ref/deploy-targets.md for what each target supports; this page for how to choose between them"
---

# 01. Choosing a deployment target

English | [日本語](01-choose.ja.md)

One company runs one deployment. This page is about deciding **what it runs on**.
You can revisit the choice later, but not for free, so it is worth twenty
minutes now.

What each target can actually do is
[ref/deploy-targets.md](../ref/deploy-targets.md). This page is only the decision.

## Start here

**Unless you have a specific reason not to, choose compose on one Linux host.** It is
the default, the best-trodden path, and the one every other target is compared
against. A single host with Docker, a domain name, and an hour is the whole
prerequisite list.

Reasons to choose something else:

| If | Then | Why |
|---|---|---|
| Docker cannot be installed on the machine | **native** | Containerless. **Single user only**: without container isolation it refuses to run in a shared mode |
| You want it on AWS, small team, cost matters | **ec2-single** | It *is* compose, on a VM. Not a separate runtime |
| You need task-level isolation, per-user fault isolation, rolling image replacement | **ecs** | You are buying isolation, not saving money |
| You need the above, plus fast starts and disk performance | **ecs-ec2** | A pool of instances with a persistent per-user disk |
| Your platform is Kubernetes, or Google Cloud, and you want workspaces that scale to zero | **kubernetes** (preview) | A workspace is a pod with a persistent per-user volume on a cluster you run; GKE Standard first |

## What ECS actually costs you

The usual assumption here is backwards.

| Team size | ec2-single | ECS (weekdays, 8h/day) | Verdict |
|---|---|---|---|
| up to 5 | **$67** (t3.large) | $154 | **ec2-single, no contest**: ECS cannot earn back its $110 floor |
| up to 15 | $283 (8 vCPU / 32 GB) | $240 | roughly even; decide on operational effort and isolation needs |
| 20 | $283–368 | **$285** | **about the same**: this is the crossover, and past it non-cost factors dominate |
| 20, running 24/7 | $283 | $830 | if it never idles, consolidating onto a VM wins by a mile |

**There is almost no case where ECS wins on cost.** You choose ECS for task-level
isolation, per-user fault isolation, rolling image replacement, and a tighter instance
metadata / role posture. Scale-to-zero is not what makes it cheap; it is what dilutes
the premium down to "about the same as a VM, at twenty people".

So:

- **Small, single team → ec2-single.** It is compose on a VM; the operational model you
  already know.
- **Isolation or per-user availability requirements → ECS**, accepting the floor cost
  and the fact that idle auto-stop has to actually work for the numbers above to hold.

### Fargate starts are billed by the NAT

The table above does not include this. On the AWS stack a Fargate workspace keeps no image
cache, so **every start pulls the whole workspace image (about 918 MiB compressed) through
the NAT gateway**, and the NAT bills data processing per GB. As an illustration at
$0.045–0.062 per GB, that is roughly $0.04–0.06 per start, or about $120–180 a month at 100
starts a day. Rates differ by region; check the NAT Gateway section of
[Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/) for yours. Model downloads for
self-hosted engines (`60-engines`) from Hugging Face or Civitai cross the NAT too.

Where starts are frequent, choose **ecs-ec2**: the image cache lives on the slot's own disk,
so restarting on an existing slot that already has the same image avoids the pull (a cached
start measured about 65 MiB of NAT traffic in total). A new slot, a slot recreated after
retirement, a cache miss or an image update pulls the image again, but on ecs-ec2 the layers
come through a free S3 VPC endpoint rather than the NAT, because workspaces have subnets of
their own (#2716; not yet measured on a deployed stack). The golden snapshot is separate: it
saves the CLI boot-install time, not NAT traffic. Planned, not yet available: a choice between
a NAT gateway and an egress gateway (#2717). The setup details are in
[deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md).

### Choosing the egress route on AWS

`00-network`'s `EgressRoute` decides how **workspaces** reach the internet. The Control Plane and
the AWS hosts stay on the NAT gateway either way.

| | NAT gateway (`nat-gateway`, the default) | Egress gateway (`gateway`) |
|---|---|---|
| Recording | nothing: the Admin Egress tab stays empty | every workspace connection, by name and address, attributed to the workspace |
| Allowlist | not enforced | log-only, or enforce |
| Hourly cost | the NAT gateway | the NAT gateway (still used by the Control Plane and hosts) plus a small instance and its address |
| Per-GB cost | NAT data processing on all workspace traffic, S3 included | none for workspace traffic, only normal data transfer |
| Operations | managed by AWS | an instance to run; replacing it leaves workspaces without egress for a few minutes |
| Availability | AWS-managed, one Availability Zone | one instance in one Availability Zone, replaced automatically |
| Non-HTTP traffic (SSH git) | works | blocked |
| Address to allow-list | the NAT gateway's | a separate address for workspaces |

The egress gateway is new and **not yet verified on a real account**; `pause.sh` does not
support it yet. Setup and limits: "Egress gateway" in
[deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md).

## When Kubernetes, and when not

> **Preview.** The `kubernetes` runtime is a preview, not yet supported for production. Its known
> limits, what a load-balancer cut does to a terminal and what it costs are listed once, in
> [Deployment targets](../ref/deploy-targets.md).

Choose **kubernetes** when a Kubernetes cluster is already how your organisation runs things,
or when you are on Google Cloud and want what ECS gives on AWS: a pod per workspace, isolated by
the cluster, with capacity only while it runs. It brings what a cluster brings: Terraform and
kustomize to learn if you do not use them yet, a node pool, a managed database, a load
balancer (the same kind of floor as ECS's), and a list of preconditions the cluster must meet
for workspaces to be isolated, which the runbook checks off one by one.

Do not choose it to save money on a small team (one VM is cheaper on any cloud), on a cluster
you cannot dedicate a namespace and a node pool to, or on a cluster whose network plugin does
not enforce NetworkPolicy. A home lives in one zone and is not backed up in this version.

## Where the procedure is

The commands live next to the thing they operate, and they ship inside the release
bundle, so a customer with the tarball and no repository still has them:

| Target | Runbook |
|---|---|
| compose | [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | [deploy/native/README.md](../../deploy/native/README.md) |
| a personal WSL2 machine | [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| ec2-single | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |
| kubernetes (preview) | [deploy/kubernetes/README.md](../../deploy/kubernetes/README.md) |

Inside a workspace, the same files are staged under `operate/runbooks/` beside this
shelf.

**This shelf does not repeat those commands.** It tells you what each step decides and
what to watch for. [02 Install](02-install.md) walks the compose path in that spirit.
If a command here ever contradicts the script it describes, the script is right and
this page has a bug.

## What you cannot change later without pain

- **`AF_MASTER_KEY`.** Lose it and every stored credential is permanently
  undecryptable. It goes in a vault separate from the data, and never into a backup.
- **The `DATA_DIR` absolute path.** Restores depend on its basename; moving it is a
  procedure, not an edit.
- **Upgrades are one-way.** There is no downgrade. Back up first, every time.

Everything else (the domain, the sign-in providers, limits, sizing) is ordinary
configuration.

# 01. Choosing a deployment target

English | [日本語](01-choose.ja.md)

Audience: someone deciding how and where to run a deployment
Source of truth: [ref/deploy-targets.md](../ref/deploy-targets.md) for what each target supports; this page for how to choose between them
Updated: 2026-08

One company runs one deployment. This page is about deciding **what it runs on** —
which is a choice you can revisit later, but not for free, so it is worth twenty
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
| Docker cannot be installed on the machine | **native** | Containerless. **Single user only** — without container isolation it refuses to run in a shared mode |
| You want it on AWS, small team, cost matters | **ec2-single** | It *is* compose, on a VM. Not a separate runtime |
| You need task-level isolation, per-user fault isolation, rolling image replacement | **ecs** | You are buying isolation, not saving money |
| You need the above, plus fast starts and disk performance | **ecs-ec2** | A pool of instances with a persistent per-user disk |

## What ECS actually costs you

Be clear-eyed about this, because the usual assumption is backwards.

| Team size | ec2-single | ECS (weekdays, 8h/day) | Verdict |
|---|---|---|---|
| up to 5 | **$67** (t3.large) | $154 | **ec2-single, no contest** — ECS cannot earn back its $110 floor |
| up to 15 | $283 (8 vCPU / 32 GB) | $240 | roughly even; decide on operational effort and isolation needs |
| 20 | $283–368 | **$285** | **about the same** — this is the crossover, and past it non-cost factors dominate |
| 20, running 24/7 | $283 | $830 | if it never idles, consolidating onto a VM wins by a mile |

**There is almost no case where ECS wins on cost.** You choose ECS for task-level
isolation, per-user fault isolation, rolling image replacement, and a tighter instance
metadata / role posture. Scale-to-zero is not what makes it cheap — it is what dilutes
the premium down to "about the same as a VM, at twenty people".

So:

- **Small, single team → ec2-single.** It is compose on a VM; the operational model you
  already know.
- **Isolation or per-user availability requirements → ECS**, accepting the floor cost
  and the fact that idle auto-stop has to actually work for the numbers above to hold.

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

Inside a workspace, the same files are staged under `operate/runbooks/` beside this
shelf.

**This shelf does not repeat those commands.** It tells you what each step decides and
what to watch for — [02 Install](02-install.md) walks the compose path in that spirit.
If a command here ever contradicts the script it describes, the script is right and
this page has a bug.

## What you cannot change later without pain

- **`AF_MASTER_KEY`.** Lose it and every stored credential is permanently
  undecryptable. It goes in a vault separate from the data, and never into a backup.
- **The `DATA_DIR` absolute path.** Restores depend on its basename; moving it is a
  procedure, not an edit.
- **Upgrades are one-way.** There is no downgrade. Back up first, every time.

Everything else — the domain, the sign-in providers, limits, sizing — is ordinary
configuration.

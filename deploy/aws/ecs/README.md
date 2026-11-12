# Deploy on ECS with CloudFormation

The CloudFormation templates (`cfn/`) and the scripts (`standup.sh`, `update.sh`, `release-ecr.sh`,
`pause.sh`, `teardown.sh`, …) for the native AWS adapter (`ecs` / `ecs-ec2`): one ECS service per
workspace, scaled to zero when it stops. The procedures that use them are in the operator guide;
rows that link a section below are the ones still waiting to move there:

| Page | What it covers |
|---|---|
| [Understand the ECS layout](../../../guide/operate/aws-ecs/overview.md) | What CloudFormation builds and what the Control Plane builds, the stacks and their deploy order |
| [Build on ECS](../../../guide/operate/aws-ecs/install.md) | Account prerequisites (service-linked roles, cost allocation tags, capabilities), the ECR push, the `30-ingress` stand-up |
| [Turn on optional features with stack parameters](../../../guide/operate/aws-ecs/options.md) | Preview subdomains, keys at rest on KMS (and running `af-cp` as a one-off task), remote MCP, provided routes, timers and outside engines, WAF |
| [Set up the alarm for an unreachable database](../../../guide/operate/aws-ecs/alarms.md) | `CpAlarmEmail`, the `20-platform` redeploy, recovery |
| [Set the default workspace size](../../../guide/operate/aws-ecs/workspace-size.md) | Workspace CPU, memory and working disk |
| [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md) | Scheduled execution |
| [Pin the outbound IP address](../../../guide/operate/aws-ecs/egress-ip.md) | The address a customer allow-lists |
| [Control outbound traffic with a gateway](../../../guide/operate/aws-ecs/egress-gateway.md) | `EgressRoute=gateway` |
| [Add Japanese read-aloud](../../../guide/operate/aws-ecs/tts.md) | The VOICEVOX engine (`cfn/50-tts.yaml`) |
| [Add inference engines](../../../guide/operate/aws-ecs/engines.md) | The fleet's own engines (`cfn/60-engines.yaml`) |
| [Use the EC2 slot pool](#optional-ec2-slot-pool-wsruntimeecs-ec2) | `WsRuntime=ecs-ec2` |
| [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md) | Outdated and unresponsive slots, moving retained slots onto new user data |
| [Upgrade on ECS](../../../guide/operate/aws-ecs/upgrade.md) | `update.sh`, the manual steps, rollback, `clear-params.sh`, slot AMIs, upgrading from v0.34.x |
| [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md) | EFS backup and restore |
| [Keep costs down](../../../guide/operate/aws-ecs/cost.md) | Cost and ephemerality |
| [Stand up, pause and tear down with the scripts](../../../guide/operate/aws-ecs/lifecycle.md) | The lifecycle scripts |
| [Tear a deployment down](../../../guide/operate/aws-ecs/teardown.md) | Teardown and the orphan sweep |
| [IAM permissions for deploying](../../../guide/operate/aws-ecs/iam.md) | The deploying principal's least privilege, the `CpTaskRole` policy budget |
| [Keep templates under the size limit](../../../guide/operate/aws-ecs/template-size.md) | The 51,200-byte limit and the S3 hand-off |
| [Troubleshoot ECS](../../../guide/operate/aws-ecs/troubleshooting.md) | Build, parameter, upgrade and start problems by symptom, including a cold start answering `starting` |

The parameter references stay next to the templates they describe:
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md) (`30-ingress`) and
[`cfn/PARAMETERS-60-engines.md`](cfn/PARAMETERS-60-engines.md) (`60-engines`).

## Egress IP (the address a customer allow-lists)

Moved to the guide: [Pin the outbound IP address](../../../guide/operate/aws-ecs/egress-ip.md).

## Egress gateway (`EgressRoute=gateway`, ADR 0122)

Moved to the guide: [Control outbound traffic with a gateway](../../../guide/operate/aws-ecs/egress-gateway.md).

## Workspace size (CPU / memory / working disk)

Moved to the guide: [Set the default workspace size](../../../guide/operate/aws-ecs/workspace-size.md).

## Scheduled execution (timed wakes)

Moved to the guide: [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md).

## Optional: Japanese speech (the VOICEVOX engine, `cfn/50-tts.yaml`)

Moved to the guide: [Add Japanese read-aloud](../../../guide/operate/aws-ecs/tts.md).

## Optional: the fleet's own inference engines (`cfn/60-engines.yaml`)

Moved to the guide: [Add inference engines](../../../guide/operate/aws-ecs/engines.md).

## Optional: EC2 slot pool (`WsRuntime=ecs-ec2`)

🚧 **Stood up and run in a sandbox, including a production-shaped VPC — but never with
real users.** `40-ec2-pool` has been deployed as a stack and real workspaces have run on it
end to end, first in a public subnet (docs/log/64 §64.16, §64.17), then in a **private subnet
behind a NAT gateway** (§64.19), and then with **four slots across two AZs, three users
starting at once, and the sweep loop left running for eighteen minutes** (§64.20). The
task-ENI trap below does not reproduce behind a NAT — there is no public IPv4 to lose.
Fargate (`WsRuntime=ecs`) stays the default and is untouched by this profile.

⚠️ **Multiple AZs: three things to know.** An EBS home cannot leave its AZ, so the CP
starts a slot in the AZ that user's home is already in.

1. **Every AZ you list in `AF_ECS_SUBNETS` needs its own EFS mount target**, or a task
   landing there cannot mount the credentials filesystem and never comes up.
2. **New homes are spread across the AZs you list**, fewest-homes-first, so losing one AZ
   does not take out everybody (ADR 0045 decision 16). It costs slots: a home in one AZ can only
   use a slot in that AZ, so free slots elsewhere are no use to it and the pool grows
   instead of reusing. Reusing a free slot still wins over balancing. If an AZ runs out of
   the slot type, a **new** home falls back to another; an **existing** one fails, because
   it cannot move (decision 15).
3. **To move someone to another AZ there is no "move".** Hibernate their home and start it
   again with free capacity only where you want them — the snapshot has no AZ. docs/log/64
   §64.20.7 has the runbook, including the AWS CLI form for moving one person to a
   named AZ.

**Losing an AZ is a different question from moving.** A home cannot be evacuated, so the
answer has to be in place before the bad day: turn on **home backups** (below). docs/log/64
§64.21 walks through what the adapter actually does during an AZ outage, what the operator
should do first (drop the failed subnet from `AF_ECS_SUBNETS` and restart the CP), and how
to rebuild a home from a backup afterwards.

**What you get, and what you do NOT.** Measured through the adapter on real AWS, a warm
Start is 43–110s against Fargate's ~105s — **the start latency is not the reason to switch**
(the earlier 22–27s figure was measured without Service Connect, which this product needs).
What the pool actually buys is **I/O and persistence** (small-file writes 8–30× faster than
EFS, and a home that really survives) and **sizes above Fargate's 16 vCPU / 120 GiB /
200 GiB ephemeral ceiling**.

**What changes.** A workspace stops being "a Fargate task with an EFS home" and becomes
"a task on a general-purpose EC2 *slot*, with the user's own EBS volume attached to it".
Slots are not owned by anyone: on Start the CP picks a free one, attaches that user's
volume at `/dev/sdf`, mounts it over SSM and pins the task there with an
`ec2InstanceId ==` placement constraint; on Stop it unmounts, detaches and hands the slot
back. **One slot serves one user at a time** (`ADR 0045` decision 8).

| | Fargate (`ecs`) | EC2 pool (`ecs-ec2`) |
|---|---|---|
| Warm Start | ~105s | **84–110s** — *not* an improvement worth switching for (docs/log/64 §64.17.5, §64.19.2) |
| Home | EFS — small files are 8–30× slower | **EBS gp3** — 2,000 small files in 0.04s vs 30.7s |
| Size | 74 discrete (cpu, memory) pairs, ≤16 vCPU / 120 GiB | instance types; the task reserves nothing and gets the instance |
| Resources per workspace | 2 (service + EFS access points) | 6 (also instance, volume, container-instance registration, task def) |
| Idle cost | EFS (what you use) | EBS (what you **provision**) + any hot slots |

**Stand-up**

```bash
aws cloudformation deploy --stack-name af-ecs-ec2-pool \
  --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
# then point the CP at it (this is the whole switch, and the whole rollback):
aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
```

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` switches the adapter. Rolling back is this value |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | `SlotLaunchTemplateId` output of `40-ec2-pool`. The CP refuses to boot without it on this profile |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | `m7i.large:8192:2,…` | `instanceType:memoryMiB[:vcpu]`, ascending — or several named **classes**, see below. The vCPU field is optional and display-only (the Console shows which instance a memory number lands on) |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | `""` | `SlotAmiIdArm64` output of `40-ec2-pool`. Required only when a class declares `arm64` — the CP refuses to boot otherwise. Initial/fallback only: the CP follows Amazon's SSM parameter itself (#3318) |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | `""` (the first class) | Where a member with no per-user and no per-tenant choice lands |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Hard cap **across all classes**. Start fails at the cap rather than growing the bill |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | How long an instance may sit with no task before it is **stopped**. Ends the compute charge; the root volume keeps billing. `0` = never |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0` (off) | The next step on the same clock: past it the instance is **terminated** and its root volume goes too. `0` means instances are kept forever, so retained roots grow to `Ec2MaxSlots` — see below. `14400` (4h) recommended |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | Per-user home volume (gp3) |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | **Default** for how long a home may sit unopened before it is snapshotted and its volume deleted. A tenant overrides it from the Console. Nothing is destroyed — the snapshot completes before the volume goes, and the next start restores it. See below |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | How much of a slot is held back from the workspace so the instance's own daemons cannot be starved by it. `auto` = a flat 2 GiB at every rung (an 8 GiB slot's workspace gets 6 GiB; a fifth of the rung left too little and a box thrashed). `off` = uncapped (what every deployment did before 0.12.5, and what melted one). A number = that many MiB; `0` reserves nothing and caps the workspace at the full rung (not the same as `off`, and not auto). See below |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | How long a slot may be EC2-`running` while the cluster cannot reach its agent before the CP gives up on it and rebuilds the workspace elsewhere. `0` = wait as long as the caller allows |
| — | `AF_ECS_EC2_DARK_SLOT_ACTION` | `detect` | What the sweeper does about a slot that holds a home while its ECS and SSM agents are both silent (ADR 0121). `detect` tags the instance `af-dark-since` and writes one `pool.slot_dark_detected` audit row per episode; `off` disables the scan. `rescue` / `quarantine` are not implemented yet and run as `detect` |
| — | `AF_ECS_EC2_DARK_AFTER_SEC` | `600` | How long the slot must stay dark before it is reported. Raised to at least the boot grace and `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` + `AF_ECS_EC2_SWEEP_SEC`. `0` = off |
| — | `AF_ECS_EC2_DARK_BOOT_GRACE_SEC` | `300` | A slot launched or started more recently than this is never called dark (boot and wake look dark for a while) |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1` (on) | Keep the golden snapshot in step with the workspace image without anyone re-baking by hand (ADR 0045 decision 9-1). Set `0` and it becomes your job on every release |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | How often the baker looks. It advances one step per look, so this is also how fast a bake progresses |

**What a workspace actually gets (`Ec2HostReserveMb`).** A slot's memory is not all the
workspace's: dockerd, containerd, the ECS agent, SSM and the EFS stunnel live on the same
instance, and the rung overstates what the machine has (an "8192 MiB" m7i.large reports 7784).
So the container is capped at the rung less a reserve, and the Console prints the number
the workspace can actually spend with the instance beside it — `6 GiB (of 8 GiB)`.

⚠️ **Turning it off is not free.** Uncapped, one workspace taking several GB of anonymous
memory can push the instance into refault thrash: with no swap the kernel throws away page
cache, every daemon spends its time re-reading its own executable off disk, and the slot
stops answering the cluster entirely while still looking healthy to EC2. That happened on
a live deployment (docs/log/64 §64.40) — nobody could start that workspace again for hours.

⚠️ **Changing the reserve replaces running tasks.** It is part of the task definition, so
the first start after the change registers a new revision — the same as an image change.

**Alarms (`PoolAlarmEmail` on `40-ec2-pool`).** Off by default. When set, the stack creates
an SNS topic and two CloudWatch alarms on the pool's I/O — a deep EBS queue sustained for
15 minutes, and an instance that has spent a fifth of its EBS byte credits. Both were
unmistakable during that incident and nobody was looking. ⚠️ **They cannot be scoped to
this stack**: the metrics are dimensioned by volume and instance id, the pool creates those
at runtime, and Metrics Insights filters on dimension values rather than tags — so the
queries are account-wide within the region. Turn them on where the account is the
deployment's own.

**Offering a choice of machine (docs/log/70).** `Ec2SlotTypes` takes several named ladders,
`id|label|arch|<ladder>` separated by `;`, and a tenant administrator then picks one per
tenant and per member. Memory still picks the rung *within* the class, so "8 GB" keeps
meaning the same thing. **The shipped default already declares two**, because "cost over
speed" is a legitimate choice and one version of it costs nothing to offer:

```
standard|Standard (Intel)|x86_64|m7i.large:8192:2,…;saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,…
```

Measured in ap-northeast-1 on 2026-08-22 — the price from the Pricing API, the speed
from this repository's own build on each family (`harness/bench-instance-classes.sh`,
docs/log/70 §70.3):

| against m7i | $/hour | build time | **cost per build** |
|---|---|---|---|
| m6i | −4.8% | +13% | +7.3% |
| **m8g** (Graviton4) | **−11.0%** | **−29%** | **−37.1%** |
| m7g (Graviton3) | −19.0% | −10% | −27.4% |
| m6g (Graviton2) | **−24.0%** | **+32%** | ±0 |

⚠️ **The cheapest instance per hour is not the cheapest instance.** m6g bills 24% less per hour
and takes 32% longer, which nets out to no saving at all on work that keeps the CPU
busy — and m8g is *both* cheaper and faster than m7i, so there is no reason to run m7g.

⚠️ **Which column matters depends on the member.** A slot bills for the wall-clock time
it is RUNNING, and a workspace spends most of that idle (reading, thinking, waiting on a
model), so a mostly-reading member really does pay 24% less on m6g. A member who builds
all day pays in time instead. That is the whole reason this is a per-member setting —
and the reason a class's LABEL should carry the trade-off ("Cheapest — older, slower")
rather than just the discount.

**m6i is the one that needs nothing.** Same architecture as m7i, so: the same workspace
image, the same AMI, the same golden snapshot, and a member's home keeps working when they
move between the two. −4.8% for no prerequisites and no migration. Graviton is where the
real money is, and it is also where every prerequisite below lives.

⚠️ Adding a class moves nobody. `Ec2DefaultSlotClass` decides where members with no choice
of their own land, and it defaults to the FIRST class. A running stack also keeps its own
`Ec2SlotTypes` on a redeploy unless you pass the parameter, so upgrading the CP does not
put a picker in front of anybody.

Before declaring an `arm64` class, in this order:

1. **The workspace image needs an arm64 manifest.** Without it the slot launches fine and
   the task cannot pull anything that runs on it. (`crane manifest <image>` — a single
   `manifest.v2+json` is one architecture; a manifest list / OCI index is what you want.)
2. **Set `Ec2SlotAmiArm64`.** A launch template pins one AMI and Graviton cannot boot the
   x86_64 ECS-optimized one; the CP passes this as a `RunInstances` ImageId override.
   `aws ssm get-parameter --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id --query Parameter.Value --output text`.
   This value is only the initial and fallback AMI: the CP reads the same SSM parameter itself and launches every slot with it, so patching needs no deploy (#3318).
3. **Expect a second golden bake.** A golden is a home full of binaries, so there is one
   per architecture; until the arm64 one is baked, new members on an arm64 class start
   from an empty home (slower, not broken). The pool screen lists them per architecture.

Adding a class moves nobody by itself. Moving a member *across architectures* is safe but
not free: their home is kept and its architecture-dependent contents (`~/.local` CLIs, nvm
node, Chromium) are reinstalled on the next start, and anything they built under `~/repos`
(`node_modules`, `target`, `.venv`) has to be reinstalled by them. The Console says so
before saving.

**Hibernating long-unused homes (opt-in).** An EBS home bills for what it is *provisioned*
at, whether or not anyone opens it. With this enabled, a home that nobody has opened for
that long is captured as a snapshot and its volume deleted; the owner's next Start rebuilds
it from the snapshot. For a 20 GiB-used / 50 GiB-provisioned home that is
**$4.80 → $1.00 a month**, against ~122s on the return and a slightly slower first day
(ADR 0045 decision 4).

- **It hibernates; it never destroys.** This is the only automatic path in the product that
  moves someone's home, so it stays reversible — and off by default.
- It is a **third** timer after the two above: the person goes idle → the workspace stops →
  the slot sleeps → (days later) the home becomes a snapshot. Set it in days, not minutes.
- **Per tenant, with this env as the deployment default** (ADR 0045 decision 14).
  Settings → Admin → a tenant → *Hibernate unused homes* takes a duration string
  (`720h`); empty follows this env and `0` means never for that tenant. The trigger lives
  in the idle-stop reaper, so **`AF_IDLE_SWEEP_INTERVAL=0` disables hibernation too**.
- **Two ways to turn it on, and the tenant one needs no deploy.** A retention window is
  usually the tenant's answer, so setting it in the Console is both quicker and more
  correct. Use `Ec2HibernateAfterSec` when you want a floor for tenants that never choose.
  ⚠️ Until that parameter existed the deployment default could not be set on ECS at all —
  there was no CFN parameter, and a hand-edited task definition is overwritten by the next
  deploy — so a deployment reading "default 0 (off)" was reading the *only* value it had.
- A snapshot of a 45 GiB home takes 30–40 minutes; the sweeper advances one step per pass
  and the state lives entirely in AWS tags, so a CP restart mid-way resumes rather than
  strands. If the owner comes back first, the hibernation is abandoned and the volume is
  simply reattached.

**Spare copies of each home (opt-in).** An EBS home lives in exactly ONE Availability
Zone and cannot be evacuated, so losing that zone loses the home with it. Backups are the
only copy that is not in the zone — snapshots are regional.

| Env | Default | Notes |
|---|---|---|
| `AF_ECS_EC2_BACKUP_EVERY_SEC` | `0` (off) | **Default** interval; a tenant overrides it in Settings → Admin → the tenant → *Keep a spare copy of each home* |
| `AF_ECS_EC2_BACKUP_KEEP` | `3` | How many completed copies to keep per home. Snapshots are incremental, so copy 2 costs only what changed |

- Taken **while the home is in use** — a backup is crash-consistent, the same picture a
  power cut leaves. Quiescing would mean taking a working person's home away on a timer.
- **Never restored automatically.** A backup is older than the home by definition; handing
  somebody a silently older home is worse than telling an operator to decide. The restore
  runbook is docs/log/64 §64.21.4.
- The trigger is the idle-stop reaper, so `AF_IDLE_SWEEP_INTERVAL=0` turns backups off too.
- The Slots tab shows, per home, how old its newest spare copy is — and says so loudly when
  there is none.
- **Clean home does not delete them**, and nothing else does as a side effect. A tenant
  administrator deletes a member's copies on purpose from the member detail (*Delete backups*,
  audited as `workspace.delete_backups`) — the offboarding step for a home that must not
  survive anywhere, taken after Clean home: while the home exists, the schedule keeps
  taking copies of it. Destroying the workspace deletes them too. Clean home itself deletes the
  home volume and its hibernation snapshots; the member's logins and connections are on EFS
  and stay.

**Baking the workspace image into the slot AMI: tried, measured, removed.** A slot's root
volume IS the image cache, so baking the image in does remove the pull (31.8s → **0.185s**,
measured) — and makes the slot **slower overall**, because a private AMI's root is lazily
loaded from a fresh snapshot: the instance took ~56s longer to join the cluster and a new user's
first start measured **179–192s against 144s** on the stock ECS-optimized AMI. The script
and the CP-side reporting were removed rather than left as a not-recommended option; the
measurement and the reasoning are in docs/log/64 §64.24 / ADR 0045 decision 19. **`SlotAmiId` stays
at its default** (the ECS-optimized AMI's SSM parameter; since #3318 the CP also reads that parameter
itself at launch, so slots follow it without a deploy — decision 7).

**A slot that cannot mount a home is quarantined** (`af-role=quarantined`, ADR 0045 decision 20):
it leaves the pool so nobody else lands on it, its home is detached and freed for another
slot, and the instance is stopped. It stays on the Slots tab with the reason, because it still
holds its root volume — **remove it with "Terminate" at the end of its row** once you have
taken what you need from it (ADR 0045 decision 30; nothing collects one by itself, since both
sweeper walks filter on `af-role=slot`). The failure that made this necessary was a
wedged kernel holding a deleted volume's NVMe namespace, which no amount of retrying fixes.

The button is `DELETE /api/admin/ec2-pool/slots/{id}` (super_admin), and it is the only route
in the product that deletes a machine on request. It accepts a box of this pool tagged
`af-role=quarantined` with no home attached and no live claim, and refuses everything else —
a working slot included, which leaves through `Ec2SlotTerminateAfterSec` instead. The
quarantine reason goes into the audit log before the instance (and its tags) disappear.

**A wedged slot** (running, status checks `ok`, `agentConnected=false`, SSM silent, a home still
attached) is stopped with **Stop (force)** on its row
(`POST /api/admin/ec2-pool/slots/{id}/force-stop`, super_admin, audit `pool.slot_force_stop`; #2449). It
refuses a slot whose ECS agent is connected and SSM online unless the body says
`{"override_healthy": true}`, and refuses a stopped or quarantined slot; ends the occupant's Start (the
runtime's own Stop); cancels the CP's own pending SSM commands (those commented
`agent-fleet slot volume`); and calls `StopInstances` gracefully, repeating it with `Force` after 3
minutes (only on an instance still `stopping` from the same launch) or at once on a second press while
`stopping`. Graceful first because a wedged box may still have the home mounted and Force is a power
cut. The slot is **not** quarantined and the home stays attached: the member's next Start wakes the
same stopped slot, the reboot clears the thrash, and its ECS agent reports a task that was still
STOPPING (#2447). Evidence-keeping quarantine for dark slots is #2450. Needs `ssm:ListCommands`,
`ssm:CancelCommand` and `ssm:DescribeInstanceInformation` on the CP task role (`20-platform`,
`SsmSlotForceStop`; apply that stack before using the button). A task still STOPPING on a slot whose
agent is disconnected is shown on the row. Not verified against a live AWS account.

**Golden snapshot: skip boot-install for new users.** A brand-new home pays boot-install
(4 CLIs 41s + rtk 1s + agy 6s = 48s) and a cold npm cache. Bake one home that has already
paid it and every later user starts from that copy (ADR 0045 decision 9).

**The CP does this by itself** (decision 9-1, `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` to switch off).
When the image it runs has no golden, it boots a reserved seed through the ordinary Start
path, captures its home as a *candidate*, boots a second reserved member from that
candidate, and only publishes it once that one comes up. It will not start while the pool
has fewer than two free slots, and it gives up on an image after two failed candidates —
the pool panel says which of those is happening. Everything below is the manual path, for
a deployment that has turned it off (or for baking one on the spot):

```bash
# 1. create a seed member, start their workspace from the Console, let it finish booting
# 2. stop it and wait for the sweeper to STOP THE SLOT (see below — the home stays attached)
./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
# 3. destroy the seed workspace:  DELETE /api/admin/workspaces {tenant_slug,user_key}
```

- **A seed member has to be somebody who can sign in.** There is no admin "start
  this member's workspace" — `/api/workspace/start` always resolves to the caller's
  own identity — so an invite-only address nobody holds gives you a membership whose
  workspace can never be created. Adding an account you DO hold to a throwaway tenant
  is the cheap way to get the fresh membership a seed needs.
- **Step 3 is a membership of your own, and that is allowed.** The roster refuses only
  your *last* membership (docs/log/61 §61.10.6), so you take yourself off the throwaway
  tenant from the Console — *Remove member* with *destroy the workspace and home*
  ticked, or remove then destroy. Until that rule was narrowed, every self-removal was
  refused and a deployment with a single administrator could not free the seed's slot at
  all — which then blocks the automatic bake, because it will not start with fewer than
  two free slots.
- **Step 2 waits for the slot to stop, not for the volume to detach.** A Stop keeps the
  home attached on purpose (the attachment *is* that user's slot), and the sweeper only
  stops the instance — it logs `stopping slot <id> (home stays attached)`. Nothing in the
  normal lifecycle ever detaches it, so waiting for `available` never ends. Baking off a
  **stopped** slot is correct: that shutdown unmounted the filesystem, which is the same
  reasoning `releaseSlot` uses when it skips the umount on a stopped slot.
- **Do not clone repositories into the seed.** `~/repos` lives on the home volume, so
  anything cloned there is handed to every new user. Bake boot-install and nothing else.
- **Re-bake on every release that moves the image or a CLI pin.** The CP compares the
  `af-image` tag against the image it runs and **refuses a stale golden**, falling back to
  an empty home — new users just get the slow first start, and the CP logs why. Forgetting
  to re-bake cannot silently hand anyone old CLIs.
- One golden per pool, shared. It carries no `af-membership` tag, so destroying a
  workspace never touches it.

**Destroying a workspace (irreversible).** `DELETE /api/admin/workspaces
{tenant_slug,user_key}` deletes the home and every per-membership resource — on this
profile the EBS volume, any hibernation snapshot, the slot claim, the ECS service, both
EFS access points and both SSM parameters. It only accepts a membership that has already
been removed, and it **overrides the deletion locks of ADR 0028** (those live inside the
home, which is unreadable while the workspace is stopped). Removing a membership on its
own still keeps everything, as before; `{"purge": true}` on that call does both at once.
⚠️ On Fargate the same call cannot delete the EFS *directories* behind the access points —
they survive, and keep billing. The response and the audit entry list what was left.

**Operational facts worth knowing before you turn it on**

- **A workspace keeps its slot while it is stopped, and the slot goes to sleep with it.**
  Stopping a workspace does not detach its home ("lazy release"): the attachment IS the
  affinity, so the same person comes back to the same slot without re-attaching or
  re-mounting. After `Ec2SlotSleepSec` (default 15m) the sweeper **stops** that slot, so
  the image cache survives on its root volume. A stopped slot costs only that volume
  ($3.84/month at 40 GiB) instead of ~$95 for a running one.
- **Stopping ends the compute charge; only terminating ends the ROOT VOLUME charge.**
  `Ec2SlotTerminateAfterSec` (default `0` = off) is the next step on the same clock: past
  it the instance is terminated and its root volume goes with it. ⚠️ Leave it off and the
  number of retained roots only ever grows, with `Ec2MaxSlots` as its ceiling — so raising
  `Ec2MaxSlots` to serve more people also signs you up for that many root volumes,
  permanently and whether or not anyone is working (30 × 40 GiB ≈ **$115/month**). That was
  measured on a live deployment, where the only cure was terminating instances by hand
  (docs/log/64 §64.32). **14400 (4h) is the recommended value**, and it costs your users 25
  seconds: come back the same day and you wake the instance (~110s), come back tomorrow and one
  is built for you (~135s).
- **Two different idle timers, in series.** `AF_WS_IDLE_TIMEOUT` / the per-tenant
  `ws_idle_timeout` is the product's existing idle-stop: it watches the person and stops
  their *workspace* (every runtime has it). `Ec2SlotSleepSec` only starts counting after
  that, and it stops the *slot*. Someone who walks away is therefore idle-stopped on the
  tenant's timeout and their instance sleeps 15 minutes later.
- **Slots are reclaimed only at the cap.** Below `Ec2MaxSlots` a new user gets a new
  slot; at the cap the longest-dormant occupant **of the same instance type** is evicted
  (a workspace with a running task is never touched). So `Ec2MaxSlots` bounds how many
  people work *at once*, `Ec2SlotSleepSec` bounds how many instances are *running*, and
  `Ec2SlotTerminateAfterSec` bounds how many *exist* — without the third, "how many
  exist" is `Ec2MaxSlots` too.
- **At the cap, an instance of the wrong size is removed rather than handed over.** An instance
  type is not something a running instance can change, so if the cap is held entirely by sizes
  a member cannot run on, eviction has nothing to offer. Rather than fail their start, the
  CP terminates one dormant instance of a size they cannot use — an empty one first, otherwise
  the longest-dormant — and builds theirs in its place. What that costs its former owner is
  the image cache (~110s becomes ~135s on their return), never their home: it is detached
  first and never deleted. ⚠️ This is **not** gated on `Ec2SlotTerminateAfterSec`: that
  parameter is about idle cost, this only ever runs when the alternative is a member who
  cannot start at all — and its default of `0` is exactly the deployment that sits at the
  cap. If nothing can be freed (everything running, or claimed by a start in flight) the
  start still fails, and says the pool is full.
- **An EMPTY slot sleeps on the same timer.** A slot whose home has been released — by an
  eviction, a size/class change, a `Destroy`, or the golden bake finishing with its seed
  and probe — belongs to nobody, and `Ec2SlotSleepSec` stops it too. ⚠️ Before docs/log/64
  §64.31 only *occupied* slots were ever stopped, so a released instance ran until an operator
  noticed: measured on a live deployment, three empty `m*.large` up for over 24h with zero
  tasks, at ~$95/month each.
- **No hot spare is kept, and no warm floor either.** The first person of the morning
  wakes a stopped slot (~110s) or, if the pool has none, pays the full ~135s to build one.
  Keeping an empty slot *hot* would save them ~92s (43s vs 135s) for a full instance-hour,
  every hour — `Ec2SlotSleepSec=0` buys that if you want it. Keeping stopped instances around
  instead ("keep at least N warm") buys much less than it looks: a dormant instance is only
  reusable **by its owner** (its home is still attached, so `freeSlots` never offers it to
  anyone else), and making one generic means the next user pays the wake plus the attach
  plus the mount SSM round trip — 123–143s against 135s for a fresh instance. So there is no
  floor parameter; `Ec2SlotTerminateAfterSec` alone decides how long an instance is kept.
- **AZ is destiny.** An EBS volume cannot leave its AZ, so a user is pinned to the AZ
  their home was created in. If no slot can be run there, that user cannot start.
- **A slot's root volume is shared with whoever had it before.** `/tmp` is a tmpfs
  (nothing lands on disk, capped size; mounted `exec,nosuid,nodev`, `AF_ECS_EC2_TMP_OPTS`) and the ECS task-cleanup window is shortened to
  5m, but the image cache and container write layers are genuinely shared.
- **Patching slots = updating this stack** (the AMI parameter resolves at update time)
  and letting the old slots go. That is the operational cost the EC2 launch type adds.
  The same holds for the user data ([Move slots onto new user data](../../../guide/operate/aws-ecs/slot-maintenance.md#move-slots-onto-new-user-data)).
  **Kernel patch cadence:** a slot runs the kernel of the AMI it was launched from and is
  never patched in place. Refresh the AMI (update this stack) on the cadence your security
  policy sets for the kernel, then replace the old slots as below. The task adds
  `SYS_ADMIN` (below), so a kernel fix for a namespace or netfilter bug matters here more
  than on a stricter profile.
- **Credentials still live on EFS.** The auth/identity set (`homeKeep`: `.config`,
  `.ssh`, `.git-credentials`, `.gitconfig`, `.claude`, `.claude.json`, `.codex` — under
  100 MiB) is kept on an EFS access point and symlinked into home by the entrypoint, so
  losing one single-AZ volume does not take the user's logins with it.
- **The working disk (`WsDiskGiB`) does not apply.** `AF_WS_SCRATCH` is not injected on
  this profile: home is already local EBS, so there is nothing to relocate off EFS.

### Moving retained slots onto new user data

Moved to the guide: [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md#move-slots-onto-new-user-data).

## Cost & ephemerality

Moved to the guide: [Keep costs down](../../../guide/operate/aws-ecs/cost.md).

## EFS backup and restore

Moved to the guide: [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md).

## Lifecycle scripts (stand up / pause / tear down)

Moved to the guide: [Stand up, pause and tear down with the scripts](../../../guide/operate/aws-ecs/lifecycle.md).

## Teardown

Moved to the guide: [Tear a deployment down](../../../guide/operate/aws-ecs/teardown.md).

## Notes

- Deploying principal (sandbox): broad create rights for VPC/ECS/EFS/RDS/ALB/**IAM**/
  ECR/SSM are fine in a throwaway account; for least privilege see
  §Minimal IAM above.
- The Google OAuth client used for the CP-native OAuth login is throwaway — delete it after testing.
- **`AuthMode=dev`** (30-ingress parameter) no longer exists: the CP refuses `AUTH=dev`
  behind the https URL this stack sets (#3485). A sandbox stack that used it must switch to
  `AuthMode=oauth` (see PARAMETERS.md, `AuthMode`).
- Naming prefix `af-` on every resource so a sandbox shared with other work stays
  greppable and safe to sweep.

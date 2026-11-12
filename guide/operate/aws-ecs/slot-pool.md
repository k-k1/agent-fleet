---
audience: "Operators of an AWS ECS deployment moving workspaces from Fargate onto a pool of EC2 slots (ecs-ec2)"
source_of_truth: "The parameters of `deploy/aws/ecs/cfn/40-ec2-pool.yaml` and `30-ingress.yaml`, `deploy/aws/ecs/cfn/PARAMETERS.md`, and the Control Plane's ecs-ec2 adapter (`control-plane/internal/runtime/runtime_ecs_ec2*.go`)"
---

# Start using the EC2 slot pool

English | [日本語](slot-pool.ja.md)

On an ECS deployment, run workspaces on a pool of EC2 instances (`WsRuntime=ecs-ec2`) instead of Fargate.
The home moves to EBS, so small-file I/O gets much faster, and workspaces can be larger than Fargate allows. Fargate (`WsRuntime=ecs`) stays the default: nothing changes until you do what this page describes.

## How it works

| What you do | What happens |
|---|---|
| Build the `40-ec2-pool` stack | The slot launch template and roles exist. There are no instances yet |
| Update `30-ingress` with `WsRuntime=ecs-ec2` | The Control Plane starts workspaces on slots |
| Nothing | The first start creates a slot and attaches a per-user EBS home. Slots nobody uses are stopped |
| Set `30-ingress` back to `WsRuntime=ecs` | Back on Fargate. That is the only setting to switch |

Slots belong to nobody. On every start the Control Plane picks a free slot, attaches that user's volume at `/dev/sdf`, mounts it over SSM and pins the task to that instance. One slot serves one user at a time.

## Compare it with Fargate

**Start latency is not the reason to switch.** What the pool buys is I/O, a home that really persists, and workspaces larger than Fargate allows.

| | Fargate (`ecs`) | EC2 pool (`ecs-ec2`) |
|---|---|---|
| Warm start | about 105 s | 84–110 s. Not a difference worth switching for |
| Home | EFS. Small files are 8–30× slower | EBS gp3. 2,000 small files in 0.04 s (30.7 s on EFS) |
| Size | Fixed CPU and memory pairs only, up to 16 vCPU and 120 GiB | Set by the instance type. The task reserves nothing and gets the instance |
| Resources per workspace | 2 (the service and the EFS access points) | 6 (also the instance, the volume, the container-instance registration and the task definition) |
| Cost while unused | EFS (what you use) | EBS (what you **provision**) plus any running slots |

## Before you use several AZs

An EBS home cannot leave its AZ. The Control Plane starts a slot in the AZ that user's home is in.

| Rule | What it means |
|---|---|
| Every AZ needs an EFS mount target | Logins live on EFS, so a task landing in an AZ with no mount target never comes up |
| New homes are spread across the AZs | Homes go to the AZ with the fewest homes first, so losing one AZ does not take out everybody. In exchange a home cannot use free slots in another AZ, and the pool tends to grow |
| Reusing a free slot wins | Spreading only decides in which AZ a new slot is created |
| An AZ has no capacity for the slot type | A new home falls back to another AZ. An existing home cannot move, so its start fails |

How to move someone to another AZ, and what to do when you lose one, is in [Move or lose an AZ](#move-or-lose-an-az).

## Build the pool

1. Deploy `40-ec2-pool`, then update `30-ingress` with `WsRuntime=ecs-ec2`. Pass the `SlotLaunchTemplateId` output of `40-ec2-pool` as `Ec2SlotLaunchTemplate`

   ```bash
   aws cloudformation deploy --stack-name af-ecs-ec2-pool \
     --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
   # then point the CP at it (this is the whole switch, and the whole rollback):
   aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
     --capabilities CAPABILITY_NAMED_IAM --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
   ```

2. Start one workspace and check that a slot appears under **Admin** → **Slots**. How to read that page is in [Replace and stop slots](slot-maintenance.md)
3. To go back, update `30-ingress` with `WsRuntime=ecs`

**Home contents do not move in either direction.** Fargate homes are created on EFS and `ecs-ec2` homes on EBS, separately.

| Switch | Member's home |
|---|---|
| Fargate → `ecs-ec2` | Starts from a new EBS home (a copy of the golden, if there is one). The EFS home stays but is not used |
| `ecs-ec2` → Fargate | Back to the earlier EFS home. The EBS home stays and keeps billing but is not used |

- Before switching, have members commit and push their work. They move any other files in their home themselves

- `30-ingress` is over 51,200 bytes, so it is deployed through S3 ([Keep templates under the size limit](template-size.md))
- With `Ec2SlotLaunchTemplate` empty, the Control Plane refuses to boot on this profile
- Once the pool exists, `update.sh` updates its stack too ([Upgrade on ECS](upgrade.md))

## Parameters

These are `30-ingress` parameters. Rows marked "—" are Control Plane environment variables only.

| Parameter | Environment variable | Default | What it does |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` switches the adapter. Rolling back is this value |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | The `SlotLaunchTemplateId` output of `40-ec2-pool`. Required on this profile |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | Two Intel classes | `instanceType:memoryMiB[:vcpu]`, ascending. Named classes are possible too ([Choose the slots' machine types](slot-types.md)) |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | empty | Required only for an arm64 class ([Choose the slots' machine types](slot-types.md)) |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | empty (the first class) | The class a member with no per-user and no per-tenant choice lands on |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Cap on instances across all classes. At the cap a start fails rather than growing the bill |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | Seconds an instance may sit with no task before it is **stopped**. Ends the compute charge; the root volume keeps billing. `0` = never |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0` (off) | The next step on the same clock. Past it the instance is **terminated** and its root volume goes too. `14400` (4 h) recommended |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | GiB of each user's home volume (gp3) |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | The deployment default for hibernating unused homes ([Set up home hibernation and spare copies](slot-homes.md)) |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | How much of a slot's memory is kept from the workspace ([What a workspace actually gets](#what-a-workspace-actually-gets)) |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | Seconds a slot may be EC2-`running` while the cluster cannot reach its agent before the Control Plane gives up and rebuilds the workspace elsewhere. `0` = wait as long as the caller allows |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1` (on) | Bake the golden snapshot automatically ([Golden snapshot](#golden-snapshot)) |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | How often the baker looks. It advances one step per look, so this is also how fast a bake progresses |

The settings for finding a slot that does not answer (`AF_ECS_EC2_DARK_SLOT_ACTION`, `AF_ECS_EC2_DARK_AFTER_SEC`, `AF_ECS_EC2_DARK_BOOT_GRACE_SEC`) are in [Notice a slot that does not answer](slot-maintenance.md#notice-a-slot-that-does-not-answer).

`40-ec2-pool` has the slot root volume size `SlotRootVolumeGiB` (default 100) and the alarm address `PoolAlarmEmail`.

## What a workspace actually gets

Not all of a slot's memory is the workspace's. dockerd, containerd, the ECS agent, SSM and the EFS stunnel run on the same instance. The rung also overstates the machine (an "8192 MiB" m7i.large reports 7784 MiB).

So the container is capped at the rung less a reserve. The Console prints what the workspace can actually spend, with the instance beside it: `6 GiB (of 8 GiB)`.

| `Ec2HostReserveMb` | Workspace cap |
|---|---|
| `auto` (default) | 2 GiB off every rung. An 8 GiB slot gives 6 GiB |
| A number | That many MiB off. `0` takes nothing off and caps at the full rung (not the same as `off`) |
| `off` | Uncapped |

- **Do not turn it off.** Uncapped, one workspace taking several GB of anonymous memory can push the instance into refault thrash: with no swap the kernel throws away page cache, and every daemon spends its time re-reading its own executable off disk. The slot stops answering the cluster while still looking healthy to EC2. This happened on a live deployment, and nobody could start that workspace again for hours
- A rung too small to take the reserve off is left uncapped (and the log says so)
- **Changing the reserve replaces running tasks.** It is part of the task definition, so the first start after the change registers a new revision, the same as an image change
- A value it does not recognise is treated as `auto`

## Get the I/O alarms

Put an email address in `PoolAlarmEmail` on `40-ec2-pool` and the stack creates an SNS topic and two CloudWatch alarms. Off by default.

| Alarm | Fires when |
|---|---|
| `af-<stack name>-slot-io-queue` | Some EBS volume's queue depth stays above 2 for 15 minutes (three 5-minute periods) |
| `af-<stack name>-slot-io-credits` | Some instance's EBS byte credits (`EBSByteBalance%`) stay below 80% for 10 minutes |

- Healthy slots keep their credits at 99%. Both alarms are unmistakable when a workspace drives its instance into refault thrash
- **They cannot be scoped to this stack.** The metrics are per volume and instance id, which the pool creates as it runs, and Metrics Insights cannot filter on tags, so the queries cover the whole account in the region. Turn them on where the account is the deployment's own
- The email subscription is active only once you confirm it from the email that arrives
- When one fires, see [Stop a slot that does not answer](slot-maintenance.md#stop-a-slot-that-does-not-answer)

## Golden snapshot

A brand-new home pays for installing the four CLIs and the rest (boot-install, about 48 s) and an empty npm cache on its first start. Bake one home that has already paid it and every later new user starts from that copy. That copy is the golden snapshot.

### Let the Control Plane bake it

By default the Control Plane bakes it itself.

1. When the image it runs has no golden, it starts a reserved seed member through the ordinary start path
2. It captures that home as a candidate
3. It starts a second reserved member from the candidate, and publishes the candidate once that one comes up

| Situation | What happens |
|---|---|
| Fewer than two free slots | It does not start. It never evicts anyone to bake |
| Two candidates failed on the same image | It stops trying on that image |
| No golden, or a stale one | New homes are built empty. The first start is slower; nothing breaks |

The **Golden snapshot** part of **Admin** → **Slots** says which of these is happening. There is one golden per architecture, shared by everyone in the pool ([Add a Graviton (arm64) class](slot-types.md#add-a-graviton-arm64-class)). It carries no `af-membership` tag, so destroying a workspace never touches it.

### Bake it by hand

With `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` this is your job on every release. A deployment that bakes automatically can still use it to bake one on the spot.

1. Prepare a seed member, start their workspace from the Console and wait until it has finished booting
   - The seed has to be **an account you can sign in with**. There is no way for an administrator to start someone else's workspace. Adding an account you hold to a throwaway tenant is the cheap way
   - **Do not clone repositories into the seed.** `~/repos` is on the home, so anything there is handed to every new user
2. Stop the workspace and wait for the sweeper to **stop the slot**. The log says `stopping slot <id> (home stays attached)`
   - Do not wait for the volume to detach. Stopping keeps the home attached, so it never becomes `available`. Baking off a stopped slot is correct
3. Bake. `--image` must be exactly the Control Plane's `AF_ECS_WORKSPACE_IMAGE`

   ```bash
   ./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
   ```

4. Destroy the seed workspace. In the Console, remove yourself from the throwaway tenant and tick **Also destroy their workspace and home (irreversible)** in the confirmation. The API is `DELETE /api/admin/workspaces {tenant_slug,user_key}`
   - The roster refuses only your **last** membership
   - Until the seed is destroyed its slot stays taken, and the automatic bake does not start without two free slots

- **Re-bake on every release that moves the image or a CLI version.** The Control Plane compares the `af-image` tag against the image it runs and builds an empty home instead of using a stale golden. Forgetting to re-bake can never hand anyone old CLIs
- `bake-golden.sh` also takes `--pool` (default `AF_ECS_EC2_POOL`, else `AF_ECS_CLUSTER`) and `--region`

## Move or lose an AZ

### Move a user to another AZ

There is no operation that moves an EBS volume to another AZ. Moving means rebuilding from a snapshot. A snapshot has no AZ, so home hibernation does exactly that.

1. Stop the workspace
2. Hibernate the home. A super_admin puts a short value (such as `1h`) in the tenant's **Hibernate unused homes** under **Admin**, and the sweeper picks it up. **This applies to everyone in the tenant**. Put the value back afterwards
3. Start it while free slots exist only in the AZ you want. The home comes back in the AZ of a free slot (or, if there is none, the AZ where a slot could be created)

You cannot name the target AZ. To move one person to a named AZ, rebuild the volume with the AWS CLI. Check that each command succeeded before the next, and stop at the first failure. Wherever you stop, the original volume is still there.

1. Stop the workspace and check that the home shows **detached** in the homes list on the **Slots** page. Ask the member not to start it until you are done (with the tenant's **Start at sign-in** on, signing in starts it too)
2. Find the original volume, and check that it is `available` and attached to nothing (`Attachments` empty)

   ```bash
   vol=$(aws ec2 describe-volumes --filters Name=tag:af-workspace,Values=<ws> Name=tag:af-role,Values=home \
     --query 'Volumes[0].VolumeId' --output text)
   aws ec2 describe-volumes --volume-ids "$vol" --query 'Volumes[0].[State,AvailabilityZone,Attachments]'
   ```

3. Snapshot it and wait for completion

   ```bash
   snap=$(aws ec2 create-snapshot --volume-id "$vol" --query SnapshotId --output text)
   aws ec2 wait snapshot-completed --snapshot-ids "$snap"
   ```

4. Create the new volume in the target AZ and wait until it is `available`. Its `af-role` is `home-moving`, so the Control Plane cannot see it yet

   ```bash
   new=$(aws ec2 create-volume --availability-zone <target-az> --snapshot-id "$snap" --volume-type gp3 --encrypted \
     --tag-specifications 'ResourceType=volume,Tags=[{Key=af-pool,Value=<pool>},{Key=af-role,Value=home-moving},
       {Key=af-workspace,Value=<ws>},{Key=af-membership,Value=<membership>},{Key=af-tenant,Value=<tenant>},{Key=Name,Value=<ws>-home}]' \
     --query VolumeId --output text)
   aws ec2 wait volume-available --volume-ids "$new"
   ```

5. Take the home role off the original volume, then give it to the new one. In this order the Control Plane never sees two homes

   ```bash
   aws ec2 create-tags --resources "$vol" --tags Key=af-role,Value=home-retired
   aws ec2 create-tags --resources "$new" --tags Key=af-role,Value=home
   ```

6. The member starts the workspace. Check that their files are there and that the homes list shows the target AZ
7. Only then delete the original volume and the snapshot

   ```bash
   aws ec2 delete-volume --volume-id "$vol"
   aws ec2 delete-snapshot --snapshot-id "$snap"
   ```

- **The Control Plane finds a home by `af-membership` and `af-role=home`.** With two `af-role=home` volumes for the same member it uses whichever it finds first. Keep the order of step 5
- If you stop before step 5, deleting the new volume undoes everything. After step 5, swap the two tags back
- A snapshot of a 45 GiB home takes 30–40 minutes. Right after the restore, the parts you touch are slower

### When you lose an AZ

A home cannot be evacuated, so the answer has to be in place before the bad day. Turn on [spare copies](slot-homes.md#keep-a-spare-copy-of-each-home).

| Who or what | What happens |
|---|---|
| Users whose home is in that AZ | Cannot start. Their start sits at starting and then falls back to stopped |
| Logins | Safe. EFS is a regional resource with a mount target in every AZ |
| The sweeper | Leaves an instance that merely does not answer alone, and does not detach a home it could not unmount |
| New users | Fall back to another AZ only when `RunInstances` returns a capacity error |

1. Remove the failed AZ's subnet from the Control Plane's `AF_ECS_SUBNETS` and restart the Control Plane. New homes and slots stop choosing that AZ
   - On the reference stacks `AF_ECS_SUBNETS` comes from the `PrivateSubnets` output of `00-network`. No parameter removes one, and a task definition edited by hand is reverted by the next deploy
2. If the AZ does not come back, rebuild the home from a spare copy ([Restore a home from a spare copy](slot-homes.md#restore-a-home-from-a-spare-copy))

## Know this before you turn it on

### Stopped workspaces and their slots

- **A stopped workspace keeps its slot.** The home stays attached, so the same person comes back to the same slot without re-attaching or re-mounting
- After `Ec2SlotSleepSec` (default 15 minutes) the sweeper **stops** that slot. The image cache survives on its root volume. A stopped slot costs only that volume (about $9.6 a month at the default 100 GiB, against about $95 running)
- An empty slot whose home was released (by an eviction, a size or class change, a destroy, or the golden bake) is stopped on the same timer

### Two idle timers

| Timer | Watches | Stops |
|---|---|---|
| `AF_WS_IDLE_TIMEOUT` / the tenant's idle stop | The person | The workspace (every runtime has it) |
| `Ec2SlotSleepSec` | Time since the workspace stopped | The slot's instance |

Someone who walks away is idle-stopped on the tenant's timeout, and their instance sleeps 15 minutes later. The tenant's idle stop is in [Stop workspaces left idle](../../admin/resources/idle-stop.md).

### Three limits

| Parameter | Bounds |
|---|---|
| `Ec2MaxSlots` | How many people **work** at once |
| `Ec2SlotSleepSec` | How many instances are **running** |
| `Ec2SlotTerminateAfterSec` | How many instances **exist**. Off, this is `Ec2MaxSlots` too |

- **Stopping ends the compute charge; only terminating ends the root volume charge.** With `Ec2SlotTerminateAfterSec` off, retained root volumes only grow, up to `Ec2MaxSlots`. Raising `Ec2MaxSlots` signs you up for that many root volumes, whether or not anyone is working. On a live deployment the only cure was terminating instances by hand
- **`14400` (4 hours) is recommended.** It costs your users about 25 seconds: come back the same day and you wake the instance (about 110 s), come back tomorrow and a new one is built (about 135 s)
- Slots are reclaimed only at the cap. Below it a new user gets a new slot. At the cap, the longest-dormant occupant **of the same instance type** is evicted. A workspace with a running task is never touched
- **At the cap, an instance of the wrong size is removed rather than handed over.** When the cap is held entirely by sizes a member cannot run on, the Control Plane terminates one dormant instance of such a size (an empty one first, otherwise the longest-dormant) and builds theirs in its place. Its former owner loses only the image cache (their next start takes about 135 s instead of about 110 s), never the home: it is detached first and not deleted. This runs regardless of `Ec2SlotTerminateAfterSec`
- When nothing can be freed (everything running, or claimed by a start in flight) the start fails and says the pool is full

### No spare slot is kept

The first person of the morning wakes a stopped slot (about 110 s) or, if there is none, has one built (about 135 s).

- Keeping an empty slot running would save them about 92 s (43 s against 135 s) for a full instance-hour, every hour. `Ec2SlotSleepSec=0` buys that if you want it
- There is no setting to keep a few stopped instances around. A stopped instance is reusable only by its owner (the home is still attached), and handing it to someone else means a wake, an attach and a mount, 123–143 s, no better than a new one

### About slots

- **A user cannot leave their AZ.** They are pinned to the AZ their home was created in. If no slot can run there, that user cannot start
- **A slot's root volume is shared with whoever had it before.** `/tmp` is a tmpfs (nothing lands on disk, capped size, `exec,nosuid,nodev`, `AF_ECS_EC2_TMP_OPTS`) and the ECS task-cleanup wait is cut to 5 minutes, but the image cache and container write layers are genuinely shared
- **Patching slots means updating this stack and letting the old slots go.** A slot runs the kernel of the AMI it was launched from and is never patched in place; replace slots on the cadence your security policy sets for the kernel. The task adds `SYS_ADMIN`, so a kernel fix for a namespace or netfilter bug matters more here than elsewhere. Replacing slots is in [Replace and stop slots](slot-maintenance.md)
- **Logins stay on EFS.** `.config`, `.ssh`, `.git-credentials`, `.gitconfig`, `.claude`, `.claude.json` and `.codex` (under 100 MiB together) are kept on an EFS access point and linked into the home by the entrypoint. Losing one single-AZ volume does not take the user's logins with it
- **The working disk (`WsDiskGiB`) does not apply.** The home is already local EBS, so `AF_WS_SCRATCH` is not passed

## Related

- [Choose the slots' machine types](slot-types.md)
- [Set up home hibernation and spare copies](slot-homes.md)
- [Replace and stop slots](slot-maintenance.md)
- [Troubleshoot the slot pool](slot-troubleshooting.md)
- [Set the default workspace size](workspace-size.md): the Fargate size
- [Keep costs down](cost.md)

---
audience: "Operators (super_admin) of an ecs-ec2 deployment whose slots or homes misbehave"
source_of_truth: "The Console's Admin → Slots, and the Control Plane's ecs-ec2 adapter (`control-plane/internal/runtime/runtime_ecs_ec2*.go`) and its log"
---

# Troubleshoot the slot pool

English | [日本語](slot-troubleshooting.ja.md)

On an `ecs-ec2` deployment, find the cause and the fix from the symptom when slots or homes misbehave.
Build and start problems common to every ECS deployment are in [Troubleshoot ECS](troubleshooting.md).

## How it works

| What you do | What you learn or what happens |
|---|---|
| Look at **Admin** → **Slots** | Each slot's state, quarantined slots and slots that do not answer, and the golden snapshot's status |
| Remove a quarantined slot with **Terminate** | The instance and its root volume are deleted, and the billing stops |
| Stop a slot that does not answer with **Stop (force)** | The instance stops, and the next start brings the same slot back healthy |
| Find the symptom in the tables below | The cause, and which page's steps fix it |

How to read the page is in [Replace and stop slots](slot-maintenance.md).

## A slot that cannot mount a home (quarantine)

A slot that could not mount a home is quarantined by the Control Plane. This is for failures no retry fixes, such as a wedged kernel holding a deleted volume's NVMe namespace.

| What quarantine does | What it means |
|---|---|
| Takes it out of the pool | Its tag becomes `af-role=quarantined`, and nobody else lands on it |
| Detaches its home | The home is free to use another slot |
| Stops the instance | The root volume stays and keeps billing |

Nothing removes a quarantined slot by itself. It stays on the **Slots** page with its reason.

1. Open **Admin** → **Slots**. With quarantined slots, a count appears at the top and their rows say **quarantined**
2. If there is anything to examine, do it first. The instance and its root volume cannot be brought back once terminated
3. Press **Terminate** at the end of the row, check the reason in the confirmation and terminate it

- The quarantine reason goes into the audit log before the instance and its tags disappear
- This button is the only route in the product that deletes a machine on request (`DELETE /api/admin/ec2-pool/slots/{id}`, super_admin)
- It accepts only an instance of this pool tagged `af-role=quarantined`, with no home attached and no live claim. A working slot is refused; it leaves through `Ec2SlotTerminateAfterSec` instead

## A slot that does not answer

EC2 says it is running and its status checks are `ok`, but the ECS agent is not connected, SSM is silent, and a home is still attached. Stop it with **Stop (force)**. The steps and the refusal codes are in [Stop a slot that does not answer](slot-maintenance.md#stop-a-slot-that-does-not-answer).

How to notice one before anybody starts is in [Notice a slot that does not answer](slot-maintenance.md#notice-a-slot-that-does-not-answer).

## The Control Plane does not boot

| Symptom | Cause and fix |
|---|---|
| The Control Plane does not boot after switching to `ecs-ec2` | `Ec2SlotLaunchTemplate` is empty. Pass the `SlotLaunchTemplateId` output of `40-ec2-pool` ([Build the pool](slot-pool.md#build-the-pool)) |
| The log says `slot class "…" is arm64 but AF_ECS_EC2_AMI_ARM64 is empty` and it does not boot | An arm64 class is declared and `Ec2SlotAmiArm64` is empty ([Add a Graviton (arm64) class](slot-types.md#add-a-graviton-arm64-class)) |
| A class you wrote does not appear | Its `arch` is neither `x86_64` nor `arm64`, or it has no usable rung. Such a class is dropped ([Write the classes](slot-types.md#write-the-classes)) |

## A workspace does not start

| Symptom | Cause and fix |
|---|---|
| The start fails with `slot pool is full` | The pool is at `Ec2MaxSlots` and no dormant slot can be reclaimed: everything is running, or claimed by a start in flight. Raise `Ec2MaxSlots` ([Three limits](slot-pool.md#three-limits)) |
| The start fails with `no configured AZ (…) could take a new … slot` | No AZ could launch an instance of that type (capacity or quota). Try later, or use another class |
| Only one user can never start | No slot can run in the AZ their home is in. A home cannot leave its AZ ([Move or lose an AZ](slot-pool.md#move-or-lose-an-az)) |
| The start fails with `no workspace subnet in … (AF_ECS_WORKSPACE_SUBNETS)` | An AZ of `AF_ECS_SUBNETS` has no workspace subnet. Add one for every AZ |
| Only tasks landing in one AZ never come up | That AZ has no EFS mount target, so the logins cannot be mounted ([Before you use several AZs](slot-pool.md#before-you-use-several-azs)) |
| On an arm64 class the slot launches but the task does not run | The workspace image has no arm64 manifest ([Add a Graviton (arm64) class](slot-types.md#add-a-graviton-arm64-class)) |
| A start sits at starting for a long time, then the workspace is rebuilt on another slot | The slot was EC2-`running` but the cluster could not reach its agent. After `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` (default 300 seconds) the Control Plane gives up on it and rebuilds elsewhere |
| A member moved to a class of another architecture takes minutes longer to start | Architecture-dependent tools are being reinstalled. The member reinstalls what they built under `~/repos` ([Move a member to a class of another architecture](slot-types.md#move-a-member-to-a-class-of-another-architecture)) |

## Slow or heavy

| Symptom | Cause and fix |
|---|---|
| A new member's first start is slow | No golden snapshot is used, and the home is built empty. The reason is in the **Golden snapshot** part of **Slots** ([Golden snapshot](slot-pool.md#golden-snapshot)) |
| The golden part keeps waiting for free slots | A bake needs two free slots. If you baked by hand and did not destroy the seed, destroy it ([Bake it by hand](slot-pool.md#bake-it-by-hand)) |
| The golden part says it is not used because it was baked from another image | The image changed. On a deployment that bakes automatically, wait. With `AF_ECS_EC2_GOLDEN_AUTOBAKE=0`, re-bake with `bake-golden.sh` |
| The slot I/O alarms fire | A workspace used up its instance's memory and the instance is thrashing. If `Ec2HostReserveMb` is `off`, put it back to `auto`, and force-stop a slot that does not answer ([What a workspace actually gets](slot-pool.md#what-a-workspace-actually-gets)) |
| A start from a hibernated home is slow | It is being restored from the snapshot. That start takes about 2 minutes longer, and the disk is slower for a few hours afterwards |

## Cost

| Symptom | Cause and fix |
|---|---|
| The EBS bill does not go down when nobody is working | `Ec2SlotTerminateAfterSec` is `0`, so stopped slots' root volumes are kept up to `Ec2MaxSlots`. Set it to `14400` ([Three limits](slot-pool.md#three-limits)) |
| Homes nobody uses keep costing | Hibernation is off. Set it per tenant or as the deployment default ([Hibernate unused homes](slot-homes.md#hibernate-unused-homes)) |

## Hibernation and spare copies

| Symptom | Cause and fix |
|---|---|
| Neither hibernation nor spare copies ever happen | `AF_IDLE_SWEEP_INTERVAL=0`. Both are triggered by the idle-stop sweeper |
| One tenant's homes are never hibernated | That tenant has `0` ([Set them per tenant](slot-homes.md#set-them-per-tenant)) |
| The **Spare copy** column stays at **none** | Neither the tenant nor the deployment default (`AF_ECS_EC2_BACKUP_EVERY_SEC`) sets an interval ([Keep a spare copy of each home](slot-homes.md#keep-a-spare-copy-of-each-home)) |
| A removed member's copies stay | Clean home does not delete them. **Delete backups** or destroying the workspace does ([Remove a member](../../admin/members/remove.md)) |
| After losing an AZ, users with a home there cannot start | Take the failed AZ out first, then rebuild from a spare copy if it does not come back ([When you lose an AZ](slot-pool.md#when-you-lose-an-az), [Restore a home from a spare copy](slot-homes.md#restore-a-home-from-a-spare-copy)) |

## Related

- [Replace and stop slots](slot-maintenance.md)
- [Start using the EC2 slot pool](slot-pool.md)
- [Set up home hibernation and spare copies](slot-homes.md)
- [Troubleshoot ECS](troubleshooting.md)

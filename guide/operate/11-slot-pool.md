---
audience: "a deployment administrator running the ecs-ec2 target, where workspaces run on a pool of EC2 slots"
source_of_truth: "`deploy/aws/ecs/README.md` for the commands; the Admin → Slots screen for what each slot is doing"
---

# 11. The ecs-ec2 slot pool

English | [日本語](11-slot-pool.ja.md)

On `ecs-ec2` a workspace runs as a task on an EC2 slot taken from a pool, and its home is an EBS
volume that stays with its slot between starts. A super_admin manages the slots under
**Admin → Slots**.

## ecs-ec2: replacing slots after a launch template change

A slot reads its user data only when it is launched, and a stopped workspace goes back to the
same slot on its next Start, so a change to the slot launch template (a release's security
setting, for example) does not reach the slots you already have. Replace them from the
Console instead of terminating instances by hand.

**Slots below `$Latest` are replaced automatically.** When a workspace starts and its home is on
such a slot, that start moves it to a new slot exactly as a reservation does, but without the WS
bar notice, and a free slot below `$Latest` is terminated by the sweeper. If no new slot can be
launched (capacity, quota) the start still succeeds on the old slot, and starts within the next
10 minutes stay on it without trying again. Each automatic move is in the audit log (`pool.slot_replace_auto`). The Slots tab shows
**older than $Latest · replaced at next start** on these slots. Set
`AF_ECS_EC2_AUTO_REPLACE_OUTDATED_SLOTS=false` on the Control Plane to turn this off. Reserve slots
yourself when a workspace must never be put back on its old slot (then a start that cannot launch
a new slot fails instead):

1. **Admin → Slots** (super_admin). The **Template** column shows the launch template
   version each slot was launched from, marked **older than $Latest** when it is.
2. **Reserve all N for replacement…** lists the slots below `$Latest` and the workspace on each,
   and reserves exactly those once you confirm. **Replace at next start** on a row reserves one
   slot (a reserved row then shows the label "replaced at next start"); **Cancel replacement** takes it back. Every reservation is in the audit log
   (`pool.slot_replace_reserve` / `pool.slot_replace_cancel`, with the workspace it moves).
3. Nothing happens to a running workspace. Its member sees **Moves to a new slot** in the WS bar,
   and their next stop → start launches a new slot from `$Latest`, moves the home onto it and
   terminates the old one. That start is about 25 seconds longer than usual. Files, repos and
   logins come along: the home is never deleted.
4. If no new slot can be launched (capacity, quota), the start fails with the reason and the
   reservation stays. The member is never put back on the reserved slot; retry later. A
   reserved slot with no workspace on it is terminated by the sweeper.

`deploy/aws/ecs/README.md`, "Moving retained slots onto new user data", has the details and how
to verify the result.

## ecs-ec2: memory protection for the management plane

The slot user data gives the SSM agent, docker
and containerd a systemd `MemoryMin` (192 / 256 / 128 MiB), and a small helper service
(`af-protect-mgmt`) finds the ECS agent, which runs as a docker container rather than under
`ecs.service`, and gives its scope a 192 MiB `memory.min`. The helper re-applies this every 30
seconds, so it follows agent restarts, and also lowers the OOM score of the ECS and SSM agents
(on the SSM agent process that is already running at first boot too). `system.slice` carries the
832 MiB total so the floors count. Under memory pressure the kernel then reclaims the workspace
container's pages first, so a memory-hungry workspace is much less likely to leave a slot whose
agents stop answering while EC2 still reports it healthy. The total sits inside the host reserve
(`AF_ECS_EC2_HOST_RESERVE_MB`, at least 1024 MiB by default); if you set the reserve below 832,
the protection is larger than the reserve. This is a launch template change: it creates a new
`$Latest`, and existing slots pick it up only through the replacement above. It needs cgroup v2
(the Amazon Linux 2023 default).

## ecs-ec2: stopping a wedged slot

A slot can be **wedged**: the EC2 instance is running and its status checks are `ok`, but its ECS
agent is not connected, SSM no longer answers, and a member's home may still be attached. Every Start
for that member then waits or fails. The Slots tab shows such a row as running with **(not accepting
tasks yet)**, and gives it **Stop (force)** (super_admin).

1. **Admin → Slots → Stop (force)** on the row, then confirm. The dialog names the
   workspace on the slot. The server refuses a slot whose ECS agent is connected **and** whose SSM is
   online (`slot_healthy`): the action is for slots that do not answer. A stopped slot
   (`slot_not_running`) and a quarantined one (`slot_quarantined`) are refused too. The API has an explicit
   override for the rare case where you must stop a healthy one; the Console never sends it, and a
   workspace running on that slot is stopped with it.
2. It ends the member's Start on that slot (their workspace reads as stopped), cancels the commands the
   Control Plane queued on the instance (a pending umount or mount is what holds the home's lock), and
   stops the instance. The stop is a normal shutdown first, so the kernel can unmount the home; if the
   instance has not stopped after 3 minutes the Control Plane repeats it with Force, and pressing the
   button again while the instance is `stopping` forces it at once. A timer never acts on an instance
   that has been started again since.
3. **The slot is not quarantined and the home stays on it.** Once the slot row shows asleep
   (stopped), the member's next Start wakes this same slot through the ordinary path. **Wait for
   that**: a Start made while the instance is still `stopping` gives up waiting after about two minutes
   and leaves the workspace on "starting" until its claim expires; if that happens, stop the workspace
   and start it again. The reboot clears whatever thrashed, and the slot's ECS agent
   reconnects and reports a task that was still `STOPPING` on it. Keeping a box as evidence is what
   quarantine is for, and a dark slot is not sent there on its own.
4. The result is on the page after the call (commands cancelled, whether Force was used) and in the audit
   log (`pool.slot_force_stop`, with the workspace, both health signals and the counts).
   The Control Plane role needs `ssm:ListCommands`, `ssm:CancelCommand` and
   `ssm:DescribeInstanceInformation` for this (statement `SsmSlotForceStop` in `20-platform`):
   **update the `20-platform` stack before using the button**, or the health check fails with
   AccessDenied and the action cannot run.

**A task still STOPPING.** When a slot's agent cannot report a stop, ECS keeps the workspace's previous
task as `STOPPING`, and the member's next Start waits for it (the service allows one task at a time). The
row shows **task STOPPING N min** for a slot whose agent is not connected. Once the instance is running
again its agent reports the stop, which is what the member's next Start after a force-stop does; you can
also start the instance from the EC2 console. The Control Plane does not do that on its own.

# 0121. Dark slots on `ecs-ec2`: the sweeper detects a slot whose agents went silent while it holds a home, rescues the home under the workspace's own lock, and quarantines only a box that goes dark twice

English | [日本語](0121-dark-slot-quarantine.ja.md)

- Status: **proposed** (2026-10-22). Documentation only: nothing below is built. The code facts
  were read from the tree on this date (`main` at `637379388`; the admin force-stop of #2449 is
  PR #2474 and is **not** in that tree). The AWS behaviour named under "Open questions" was
  **not measured**.
- Follow-ups: #2450
- Related: [0045](0045-ec2-persistent-workspace.md) decisions 3-3 (never stop a slot that holds
  a task ENI), 20 / 23 / 30 (quarantine keeps the evidence; nothing collects it; the operator
  terminates it), 22 (the idle-stop timer) / [0012](0012-go-internal-refactor.md) (the adapter
  holds no database, so state is tags and callbacks). Issues #2446, #2447, #2449, #2452, #2462.

## Context

A slot on `ecs-ec2` can stop answering while EC2 still says `running` and every status check
passes. Production, 2026-10-21: a slot thrashed for about twenty minutes under memory pressure
(EBS reads from ~100 to ~190k per five minutes, CPU 35–95 %); its ECS agent disconnected, its
SSM agent's last ping came a minute before the member's Stop, and the member could not start
again until an operator cancelled the pending umount and stopped the instance by hand. The
earlier incident of the same shape (docs/log/64 §64.40) is why `abandonLostSlot` exists.

What the tree already does about a dark slot, and where it stops:

| Where | What it does | Gap |
|---|---|---|
| `waitSlotRegistered` (a Start) | after `slotLostAfter` (300 s) of EC2-`running` without ECS registration, `errSlotLost` | only a Start that is *waiting on this slot* ever looks; nothing looks while the member is away |
| `abandonLostSlot` | tag `quarantined`, force-deregister the container instance, wait for the task ENI to go, `StopInstances` with Force, detach the home; all under the Start's own ownership checks (`ifOwned`, #2462) | reachable only from that Start |
| `quarantineSlot` | the same for a failed mount | mount failures only |
| `sweepVolume` | `if live workspace: leave it alone` (`DesiredCount`, `RunningCount` or `PendingCount` above 0) | a workspace that is nominally running on a dark slot is exactly what it skips |
| `sweepVolume`, dormant branch | stop the slot after `slotSleepAfter`, but never one that holds a task ENI (decision 3-3) | a dark slot keeps its task ENI, so this logs "leaving it running for now" forever |
| `sweepGhostInstances` | deregister container instances whose EC2 instance is **gone** (`ghostAfter`, 1 h) | not a running box |
| `stuckStoppingReason` (#2447, in `main`) | name a task stuck `STOPPING` on a disconnected slot as a blocked start phase | detection only |
| #2474 (open) | an operator stops one slot: cancel the CP's SSM commands, end the occupant's Start, `StopInstances`; the slot stays in service and the next Start wakes it | manual |

So the missing piece is *the look while nobody is starting*, and what to do on seeing it. Four
things make that harder than it sounds, and the first three were all learned the hard way:

1. **A box that is not answering the cluster cannot be asked anything.** SSM is dark too, so
   every step that runs a command on it (umount) hangs; a Start that waits on it cannot finish
   inside the ingress's 60 s (#2446).
2. **Stopping it is not free.** A running task's ENI is still attached. A stop in that window
   brings the box back multi-ENI and, on a deployment without NAT, without egress, so its agent
   never reconnects (decision 3-3, reproduced through the sweeper's own path). The sweeper
   therefore refuses to stop such a box, which is why a dark slot sits there for hours.
   `abandonLostSlot` is the code that gets round it (deregister first, so ECS lets go of the ENI).
3. **Whatever acts on the slot races the people who use it.** #2474 started as "quarantine the
   slot" and took four review rounds to find where that race lives (see decision 3); it was
   narrowed to "stop it, change nothing else" and the quarantine was moved here.
4. **The dark signal is cheap to fake.** An agent restart, a boot, a wake, an instance reboot
   all look dark for a while. Acting on one of those throws away a healthy box.

## Decisions

### 1. Detection: one predicate, a durable anchor, and the sweeper does not touch a slot a Start owns

A slot is **dark** when, at one sweep, all of these hold, and it **has been dark** when the same
is true after `darkAfter` has passed since the anchor:

- EC2 `running` (a `pending` or `stopping` box is booting or being stopped, not dark);
- its ECS container instance is `ACTIVE` with `agentConnected=false`, or is not registered;
- SSM does not list it with `PingStatus=Online` (`ConnectionLost`, `Inactive`, or absent);
- it has been launched or started for at least `darkBootGrace` (`LaunchTime`, which moves on every
  start; the boot and wake waits of `waitSlotRegistered` measure ~20–35 s and are not
  repeated here);
- for a slot whose workspace service is **up** (`DesiredCount > 0`), the CP cannot reach the
  workspace Agent either (the probe `State()` already uses). A container whose ECS agent is
  dark can still be serving its member, and stopping that is not a rescue.

One function (`slotDark`) computes it, and #2474's health gate becomes its negation: the manual
action refuses a slot the automatic one would also call healthy. Two definitions would drift.

**The anchor** is the point the `darkAfter` clock runs from, and it must survive a CP restart
without a database (ADR 0012):

- the SSM `LastPingDateTime` when SSM lists the box (durable, AWS's own);
- else the `StoppingAt` of a task stuck `STOPPING` on the container instance (#2447's signal; a
  task stuck for longer than `stuckStoppingGrace` on a disconnected agent is also evidence that
  satisfies the ECS half at once);
- else the first sighting, stamped as the tag `af-dark-since` on the instance, written by one pass
  and acted on by a later one. This is the shape of `af-slot-idle-since` (decision 22), and it is
  what makes two replicas reach one verdict. The stamp is cleared only by a pass that finds the
  slot healthy, so a flapping agent does not reset the clock on every blink.

**Thresholds.** `darkAfter` defaults to **10 minutes**, above every wait a healthy box has
(hot slot: first poll; wake: ~20 s; new box: ~35 s; `placementBlockedGrace`, 120 s, is about a
*message* for a wait the CP itself creates, not about a box) and above `slotLostAfter` (300 s),
which governs a Start already waiting. `ghostAfter` (1 h) is a different object (a container
instance whose EC2 instance is **gone**) and is not reused. The default is a guess until the
open questions are answered; it is an env value (`AF_ECS_EC2_DARK_AFTER_SEC`), 0 = off.

**Who is in scope.** Only a slot with a **home attached and no live claim**, the same population
`sweepVolume` walks. A live claim means a Start is converging on it and owns the verdict through
`slotLostAfter`; the sweeper skips it exactly as `sweepVolume` does. A free dark slot has nobody to
rescue; it follows the ordinary free-slot rules, plus the pool view marks it.

*Alternatives rejected.* A new CloudWatch-driven detector: needs a second source of truth the CP
does not own, and still has to answer the same race questions. A short threshold (the
`placementBlockedGrace` 120 s): that constant is sized for a message, and acting on it would
stop boxes that are merely waking. Counting a CP-side miss counter: lost on restart, which the
tag anchor is not.

### 2. The action ladder: rescue first, quarantine only for a box that goes dark twice

The member needs their home on a working box; the operator needs evidence only when the *box*
is at fault. Memory thrash is usually the *workload's* fault, and moving the same workload to a
new box thrashes there too, so quarantining on the first episode costs a root volume that bills
until an operator terminates it (decision 30), a slot under the cap, and a longer Start (a
new box), to preserve evidence of something the logs already tell. So:

- **L0. Observe.** The first pass stamps the anchor and the pool view shows "dark for N min". In
  the first release this is the whole behaviour (decision 5).
- **L1. Rescue**, once dark for `darkAfter`: take the workspace's home off the box and leave the
  box in service, **in the order `abandonLostSlot` already proves**: end any Start (as a Stop:
  `abortStarts`, desiredCount 0, claim dropped); force-deregister the container instance (ECS
  gives up the task and its ENI, which also **clears the stuck `STOPPING` task** of #2447);
  wait, bounded, for the task ENI to go; `StopInstances` (graceful, then Force on a stop that has
  not finished, never on an instance started again since, as in #2474); detach the home
  (ordinary, the box is off). The role stays `slot` and `af-wedge-at` is stamped. The member's
  next Start finds a homeless home and places it like any other (this very box is a free
  stopped slot by then, and may well be the one it lands on, after the reboot).
- **L2. Quarantine** the box instead of leaving it in service when it goes dark **again**
  within `darkRepeatWindow` (default 24 h) of its last `af-wedge-at`, or when an L1 rescue
  could not finish (the ENI never went, the stop never completed). Quarantine is the
  existing `abandonLostSlot` tail (`af-role=quarantined` with a reason, kept stopped, never
  collected; terminating it stays decision 30's button). One repeat is the cheapest honest
  evidence the *box* is the problem.

*Alternatives rejected.* **Always quarantine** (the first design of #2474): see the argument above
and decision 4. **Always force-stop and wake the same box** (what #2474 does for an operator):
safe for a box with no task ENI, but for a dark running workspace the ENI is attached, so the
stop is the multi-ENI trap (item 2 of the Context) and the wake can fail for lack of egress;
it also leaves the stuck task for the agent to report, which needs that wake to work. Fine as an
operator's choice, wrong as the automatic default. **Stop only the instance and leave the home
attached** (decision 3-3's cautious reading): leaves the member blocked, since their next Start
finds the home on a box with a stuck STOPPING task and waits on it (#2447). **Deregister without
stopping**: strands a running box from ECS, which then bills with no tenant.

### 3. Moving the home: where the race lives, and the invariant

L1 and L2 both end with the home off the box, so every way a placement can choose a slot has to
be accounted for. The inventory (all in `runtime_ecs_ec2*.go`), with the question for each: *who
else can choose this slot between the sweeper's read and its write?*

| Path | Chooses | Today's fence | Competes for the dark slot? |
|---|---|---|---|
| `placeHome` same type, home attached | the slot the home is on (affinity) | reads reservation/outdated at entry; the home's own Start count | **yes, the home's own workspace only** |
| `placeHome` mismatch → release in the background (`releaseFrom`) | the slot the home is on | `releaseSlotSince`, fenced on the Start count | the same workspace only |
| `placeHome` reserved / outdated → `replaceReservedSlot` | a **new** slot, then terminates the old | `slotNowReserved` after the claim (and before moving) | no (new slot); terminates the old one |
| `placeHome` free slots → attach → claim | a free slot from `freeSlots` | list filtered on `af-role=slot` and `slotExcluded`; `slotNowReserved` after the attach and after the claim | no: a dark slot with a home is not free |
| `placeHome` eviction (`evictLongestIdle`) | the longest-idle **occupied** slot, released through the occupant's own runtime | `slotNowReserved` after the attach; the release is fenced on the occupant's Start count and mount lock | **yes: another member's Start can pick a dormant dark slot as its victim** |
| `placeHome` `growPool` → claim | a **brand-new** slot | none after the claim | no (new) |
| `adoptEarlier` | the slot an earlier attempt launched | `adoptableReplacement` verdict; `slotNowReserved` after the claim | no (it holds the same workspace's claim) |
| `releaseAndReplace` / the `errSlotLost` re-place | the slot after `abandonLostSlot` | `startSuperseded` at each boundary | the same workspace |
| #2462's `Stop` (`abortStarts`) | none: moves the Start count | `homeLockClaim` | the same workspace |
| claim TTL (`claimTTL`, 300 s) | none: an expired claim is ignored by `claimLive` | | what lets a Start that was overtaken leave a claim behind, harmlessly, for at most one TTL |

**The invariant, in one sentence:** *every actor on a slot that holds a home (its own workspace's
Start and Stop, another member's eviction of it, the sweeper) reaches the slot through that home's
locks and Start count, and re-reads the count before each destructive call (`underStart`,
`ifOwned`); a slot with no home is never the target of L1 or L2.*

That is why this design acts only on slots with a home and no live claim (decision 1), and why it
does not need a fence on the *other* members' free-slot and new-slot paths. The one other-member
path that does reach an occupied slot is eviction, and it already goes through the occupant's
locks; it must additionally skip a slot stamped `af-dark-since`, because releasing a dark slot
runs an umount that cannot run (#2446). #2474's first version quarantined
by a tag and then read the holders, which is the right shape for a slot that **may be free**; the
four rounds (the findings are labelled R2–R9, as in that review) showed every place that shape has to be completed, and each is a free-slot or
new-slot path that the occupied-slot rule makes unreachable:

- **R3 / R3b** (tag written after the occupant was stopped; a placement that listed its
  candidates before the tag and attached after it): both are *another* Start choosing a slot the
  operator was acting on. For an occupied slot the choosers are the occupant's own workspace and
  an evicting Start, both serialised by the occupant's lock and count; the occupant's Stop
  (`abortStarts`) is the whole fence.
- **R7** (a brand-new slot from `growPool` had no check after its claim): the sweeper never acts
  on a new slot under a live claim, and a slot younger than `darkBootGrace` is not dark.
- **R8** (a cleanup that deleted the home's claim by volume id and so deleted a *newer* Start's
  claim): every deletion of a claim by the sweeper goes through `unclaimUnlessSuperseded`, with the
  Start count read **at the sweeper's decision**, not at the cleanup's entry.
- **R9** (a quarantined replacement slot was retired like an unused one, which **terminates** it
  and destroys the evidence): `retireUnusedReplacement` and `terminateSlot` must never be
  reachable for a slot whose role is `quarantined`; checked in `terminateSlot` itself, so no
  caller has to remember.
- **R2** (a quarantined slot that also carried a replacement reservation was sent through
  `replaceReservedSlot`, which terminates the old box): the order in `slotTypeMatches` is
  *quarantined first*, then reserved, then outdated.

Three rules follow for the code that will implement this, and each is cheap to test:

1. `slotTypeMatches` treats a quarantined slot as a plain mismatch **before** it reads the
   reservation or the template, so a home left attached to a quarantined box (an L2 whose
   detach failed) is released and re-placed rather than woken (`wake`) on a box the CP is no
   longer allowed to command (`SendCommand` is granted to `af-role=slot` only). `releaseSlot` waits
   for a box that is still `stopping` before it detaches.
2. Eviction skips a slot stamped `af-dark-since`.
3. The sweeper's L1 and L2 begin with the occupant's `Stop` semantics and re-check the Start count
   before **each** destructive call, as `abandonLostSlot` does under #2462; a Start that began after
   the decision wins, and the next pass looks again.

*Alternatives rejected.* **Fence every placement path with a post-claim quarantine check** (what
#2474 tried): correct, but it spreads one concern over seven call sites and each of the four
rounds found one that was missed; with the occupied-slot rule the sites are unreachable, and an
unreachable check is dead code that is wrong the day the rule changes. **Reuse the replacement
reservation tag as the fence**: its two-sided checks are already there, but its end of life is
`terminateSlot`, which destroys the evidence L2 exists to keep.

### 4. Who owns what: sweeper, Start, admin

- **Start** owns the slot while it holds a live claim; its own `slotLostAfter` (300 s) is its
  verdict, and `abandonLostSlot` its action. Unchanged.
- **The sweeper** owns a slot with a home and no live claim. It is a third party to the
  workspace, so it acts as a Stop would: under `homeLockClaim`, from a Start count read at the
  decision. If a Start has begun since, the sweeper steps aside and the Start's own path takes it.
- **The admin action** (#2474) is the same code from a person: it applies the same predicate as a
  *gate* (a healthy slot is refused unless overridden) and, being manual, may also run on a
  slot the sweeper has not yet judged. It stays a stop with the home left alone. The ADR 0045
  note and the guide say it can hit the multi-ENI trap when a task ENI is attached; this record
  does not change it, and Open question 1 asks whether it should refuse then.
- **Two replicas** both detect and both stamp the same anchor; the action runs under the
  workspace's `cp_lease` and the shared Start count, so a second replica finds either the count
  moved or the lease held. Every step is idempotent (`StopInstances`, a deregistration of an
  unregistered instance, a detach of a detached volume).
- **A CP that restarts** mid-rescue resumes from AWS: the anchor and `af-wedge-at` are tags, the
  home lease expires (`homeLeaseTTL`, 90 s), the step that did not finish is re-derivable (the box
  still running with a deregistered container instance is a dark slot again, and the next pass
  continues L1). Nothing is held in memory (ADR 0012).
- **Audit** is a callback, because the adapter has no database: the same shape as
  `Config.OnSlotAutoReplace`, with actor `system` and actions `pool.slot_dark_detected`
  (once per episode), `pool.slot_dark_rescued`, `pool.slot_dark_quarantined`.

### 5. Surfaces and rollout

- **Pool view**: a slot's `dark_since` and the rung it is on (observing / rescued / quarantined),
  next to the existing `stopping_task` of #2474; the `registered` flag stays as it is.
- **Member**: a rescued workspace reads `stopped`; a Start behind a rescue reads the existing
  blocked-phase line (#2447) instead of `starting` for nothing. No new member screen.
- **Operator**: the audit actions above, one log line per rung, and the guide's pool section.
- **Rollout**, because the thresholds are a guess: `AF_ECS_EC2_DARK_SLOT_ACTION` is
  `off | detect | rescue | quarantine`, default **`detect`** in the first release (anchor,
  pool view, audit `detected`, no AWS write besides the stamp). `rescue` is enabled per deployment
  after the detect-only window has shown no false positive; L2 is behind `quarantine`.
  The default is changed in a release note, not silently.
- **IAM**: new instance tag keys (`af-dark-since`, `af-wedge-at`) are added to the CP role's tag
  key allow-list (`20-platform`), alongside the existing `af-slot-idle-since`; and `ssm:DescribeInstanceInformation`
  (#2474 adds it already). Every `ec2:StopInstances` and `DetachVolume` call site is entered in
  `deploy/local/cfn-cp-ec2-scope-test.py`.

## Open questions (each needs a live run)

1. **#2474's stop with a task ENI attached.** The manual force-stop does not deregister first, so
   on a dark running workspace it is the multi-ENI trap. Does a restarted box regain egress on
   a deployment with and without NAT? If not, the manual action should refuse (or deregister
   first) while a task ENI is attached, which is a change to #2474, not to this record.
2. **Benign dark windows.** How long can the ECS agent or the SSM agent be silent on a healthy
   box (agent self-update, docker restart, an instance reboot, a short stall)? `darkAfter` 10 min
   is a guess until the detect-only window answers it.
3. **SSM freshness.** How often is `LastPingDateTime` refreshed, and when does `PingStatus` turn
   `ConnectionLost`? The anchor is only as good as that, and the documented cadence is not a
   measurement.
4. **Does deregistration clear a stuck task here?** #2447 observed that *starting the box* did.
   `abandonLostSlot`'s force-deregister is believed to make ECS give the task up; confirm with a
   task that is `STOPPING` on a dark box.
5. **Does Force corrupt a home?** `abandonLostSlot` orders the detach after the stop for that
   reason; measure an ext4 / XFS journal replay after a Force stop of a thrashing box.
6. **Is the workspace-Agent probe cheap and honest for a thrashing box?** Decision 1 leans on it
   for a service that is nominally up; if it times out slowly the sweep pass needs a budget.
7. **Do `MemoryMin` and the host reserve (#2452, the memory-pressure response) change the base
   rate?** If dark episodes become rare, L2's repeat rule may never fire and `darkRepeatWindow` can
   be dropped.

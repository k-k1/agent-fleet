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
| `waitSlotRegistered` (a Start) | polls `registeredSlots` every 3 s for `slotLostAfter` (300 s, counted as 100 polls), then checks once that EC2 says `running`, and answers `errSlotLost` | only a Start that is *waiting on this slot* ever looks; nothing looks while the member is away. The 300 s is a poll count, not a measured continuous darkness |
| `abandonLostSlot` | tag `quarantined`, force-deregister the container instance, wait for the task ENI to go, `StopInstances` with Force, detach the home, with a forced detach as the fallback; every step under the Start's own ownership check (`ifOwned`, #2462) | reachable only from that Start |
| `quarantineSlot` | for a failed mount: tag `quarantined`, mount lock, bounded umount over SSM, detach, drop the claim, graceful `StopInstances`. It does **not** deregister, and it needs SSM | mount failures only; not usable on a dark box |
| `sweepVolume` | `if live workspace: leave it alone` (`DesiredCount`, `RunningCount` or `PendingCount` above 0) | a workspace that is nominally running on a dark slot is exactly what it skips |
| `sweepVolume`, dormant branch | stop the slot after `slotSleepAfter`, but never one that holds a task ENI (decision 3-3) | a dark slot keeps its task ENI, so this logs "leaving it running for now" forever |
| `sweepGhostInstances` | deregister container instances whose EC2 instance is **gone** (`ghostAfter`, 1 h) | not a running box |
| `stuckStoppingReason` (#2447, in `main`) | name a task stuck `STOPPING` on a disconnected slot as a blocked start phase | detection only |
| #2474 (open) | an operator stops one slot: cancel the CP's SSM commands, end the occupant's Start, `StopInstances`; the slot stays in service and the next Start wakes it. Its gate refuses a slot whose ECS agent is connected **and** SSM Online | manual; no ENI check, no deregistration |

So the missing piece is *the look while nobody is starting*, and what to do on seeing it. Four
things make that harder than it sounds, and the first three were all learned the hard way:

1. **A box that is not answering the cluster cannot be asked anything.** SSM is dark too, so
   every step that runs a command on it (umount) hangs; a Start that waits on it cannot finish
   inside the ingress's 60 s (#2446).
2. **Stopping it is not free.** A running task's ENI is still attached. A stop in that window
   brings the box back multi-ENI and, on a deployment without NAT, without egress, so its agent
   never reconnects (decision 3-3, reproduced through the sweeper's own path). The sweeper
   therefore refuses to stop such a box, which is why a dark slot sits there for hours.
   `abandonLostSlot` is the code that gets round it (deregister first, so ECS lets go of the
   ENI): it was written from the 2026-08 incident and the ENI half was observed by hand then;
   the rest of what deregistration does is **not** established (see the open questions).
3. **Whatever acts on the slot races the people who use it.** #2474 started as "quarantine the
   slot" and took four review rounds to find where that race lives (see decision 3); it was
   narrowed to "stop it, change nothing else" and the quarantine was moved here.
4. **The dark signal is cheap to fake.** An agent restart, a boot, a wake, an instance reboot
   all look dark for a while, and an anchor that is older than the darkness proves nothing.
   Acting on one of those throws away a healthy box.

## Decisions

### 1. Detection: one predicate, an anchor that proves continuous darkness, and the sweeper does not touch a slot a Start owns

A slot is **dark** at one sweep when all of these hold:

- EC2 `running` (a `pending` or `stopping` box is booting or being stopped, not dark);
- its ECS container instance is `ACTIVE` with `agentConnected=false`, or is not registered;
- SSM lists it with a `PingStatus` other than `Online` (`ConnectionLost`, `Inactive`), or does
  not list it;
- it was launched or started at least `darkBootGrace` ago (`LaunchTime`, which moves on every
  start; default 300 s, above the measured boot and wake waits of ~35 s and ~20 s);
- for a slot whose workspace service is **up** (`DesiredCount > 0`), a probe of the workspace
  Agent also fails. A container whose ECS agent is dark can still be serving its member, and
  stopping that is not a rescue. `State()` does not probe the Agent today (it derives the state
  from the volume, the claim and the ECS service), so this probe is a **new piece**; until it
  exists, a slot whose service is up stays at L0 (observe) and is never rescued automatically.

**A read that fails is not an answer.** An ECS, EC2 or SSM call that errors, throttles or times
out makes the pass *unknown* for that slot: it neither stamps, clears nor acts. "SSM does not
list it" is an answer only when the call succeeded.

**The anchor.** The first pass that finds the whole predicate true in a launch epoch writes the
instance tag `af-dark-since` as `<time>/<LaunchTime>`, *always* (not only as a fallback), because
other code excludes a slot by that tag (decision 3). A later pass acts when the predicate still
holds and `now − stamp ≥ darkAfter`. The stamp is cleared by a pass that finds the slot healthy,
and a stamp whose `LaunchTime` is not the current one is stale and rewritten, so every start
re-arms the clock.

**No external timestamp shortens the wait.** An SSM `LastPingDateTime` and a task's `StoppingAt` (#2447)
are *not* evidence of how long the whole predicate has held: `StoppingAt` is when the stop was asked, not
when the agent disconnected, and a `LastPing` can be days old on a box whose ECS agent was fine until a
minute ago. The only start of the clock is the first sweep that observed every condition at once, so the
first episode on a box is acted on `darkAfter` after that observation (at most `sweepEvery` later than the
box went dark). Both timestamps are shown on the pool view as explanation and used for nothing else. The
cost is up to one sweep interval of extra latency, and it buys that a short ECS disconnect on a box whose
SSM was already offline cannot read as ten minutes.

**Thresholds.** `darkAfter` defaults to **10 minutes** as an env value (`AF_ECS_EC2_DARK_AFTER_SEC`,
0 = off). It must be at least `darkBootGrace` and at least `slotLostAfter + sweepEvery`; a
deployment that sets them inverted is clamped up, with a log line, at start-up. It sits above every
wait a healthy box has (hot slot: the first poll; wake ~20 s; new box ~35 s) and above `slotLostAfter`
(300 s), which governs a Start already waiting. `placementBlockedGrace` (120 s) is about a
*message* for a wait the CP itself creates, and `ghostAfter` (1 h) is about a container instance
whose EC2 instance is **gone**; neither is reused. The default is a guess until the open questions
are answered.

**Who is in scope.** Only a slot with a **home attached and no live claim**, the same population
`sweepVolume` walks. A live claim means a Start is converging on it and owns the verdict through
`slotLostAfter`; the sweeper skips it exactly as `sweepVolume` does. A free dark slot has nobody
to rescue; it follows the ordinary free-slot rules, and the pool view marks it.

*Alternatives rejected.* A CloudWatch-driven detector: a second source of truth the CP does not
own, and the same race questions to answer. A short threshold: acts on boxes that are merely
waking. A CP-side miss counter: lost on restart. The anchor as the SSM or ECS timestamp alone:
see above.

### 2. The action ladder: rescue first, quarantine only for a box that goes dark twice or cannot be rescued

The member needs their home on a working box; the operator needs evidence only when the *box* is
at fault. Memory thrash is usually the *workload's* fault, and moving the same workload to a new
box thrashes there too, so quarantining on the first episode costs a root volume that bills until
an operator terminates it (decision 30), a slot under the cap and a longer Start, to preserve
evidence of something the logs already tell. So:

- **L0. Observe.** The stamp is written and the pool view shows "dark for N min". In the first
  release this is the whole behaviour (decision 5).
- **L1. Rescue**, once dark for `darkAfter`: take the home off the box and leave the box in service.
  Each step is a gate for the next, never a sequence to push through:
  1. **prepare**, under the claim lock alone (decision 4, lock order): take ownership, write the rescue
     marker, end any Start as a Stop does (`abortStarts`, desiredCount 0, claim dropped), and, still holding
     the claim lock with the token valid, **list the CP's own pending SSM commands on the box and fix that
     set of command ids**. Then release the claim lock. Nothing in this step waits for the mount lock;
  2. cancel exactly that set of ids, **with no home lock held**. They hold the home's mount lock for as long
     as they live, so the cancel has to be able to arrive before the rescue asks for that lock. The set is
     never re-listed afterwards: a later Start of the member sends commands after the token was taken, and
     those are not this rescue's to cancel. A resumed rescue may list again only by re-taking the claim
     lock and finding the count still `g_r`, and cancels only what that listing returns;
  3. take the mount lock with a bounded wait; if it is not had within the bound, stop for this pass (the
     marker stays, the next pass retries). Every destructive call from here runs under the mount lock and
     re-verifies the token under the claim lock nested inside it;
  4. write `deregistering`, then force-deregister the container instance, and **verify that the task ENI is physically gone**
     (`DescribeInstances`) within a bound. Deregistration is not assumed to do anything else: AWS
     documents that it does not stop the task's processes;
  5. only with the ENI gone, write `stopping` and call `StopInstances` (graceful; Force on a stop that has not finished,
     never on an instance started again since, as in #2474), and **verify `stopped`**;
  6. only with the box stopped, an ordinary detach of the home, preceded by the `detaching` marker phase
     (decision 4). The role stays `slot`; the finalizer of decision 4 then records the episode.

  If the ENI does not go, or the box does not stop, within the bounds, the rescue is **blocked**:
  nothing further is done, the box is left as it is, the marker keeps its phase, and the pool view
  and the audit say so. A blocked rescue is retried every pass, and is the operator's to resolve
  (the manual force-stop or the Terminate button) unless the next rung is enabled.
- **L2. Quarantine** instead of leaving the box in service when it goes dark **again** within
  `darkRepeatWindow` (default 24 h) of its last `af-wedge-at`, **or** when a rescue has been blocked
  for `rescueBlockedAfter` (default 30 min). Quarantine tags the box `af-role=quarantined` with a
  reason, keeps it, and never collects it (decision 30). For a blocked rescue it also permits the one
  exception `abandonLostSlot` already makes, a **forced detach** of the home from a box that will not
  stop. This is a last resort chosen as policy, not a safe operation: AWS documents that detaching a
  volume that is still in use can corrupt the filesystem and require a repair. The damage is neither
  bounded nor measured here, and recovery means a filesystem check and, if that fails, the home's most
  recent backup or hibernation snapshot (decision 17 keeps copies). The alternative is a member blocked
  indefinitely, and the choice between them is the operator's, made once per deployment by enabling
  `quarantine` mode, and only after the measurement and the recovery procedure of Open question 5 exist.

A task still `STOPPING` is **not assumed to be cleared** by the rescue. Whether it is, is Open question
4; if it persists, the member's next Start shows #2447's blocked-phase line. #2447's remedy (start the
old instance once so its agent reports) was measured on a box that had **not** been deregistered, and it
is not assumed to work on a deregistered container instance; that is an acceptance condition of Open
question 4.

*Alternatives rejected.* **Always quarantine** (the first design of #2474): the argument above and
decision 3. **Always force-stop and wake the same box** (what #2474 does for an operator): safe for a
box with no task ENI, but for a dark running workspace the ENI is attached, so the stop is the multi-ENI
trap and the wake can fail for lack of egress; fine as an operator's choice, wrong as the automatic
default. **Stop the instance and leave the home attached**: the member stays blocked, since the next
Start finds the home on a box with a stuck STOPPING task and waits on it (#2447). **Deregister without
stopping**: strands a running box from ECS, which then bills with no tenant.

### 3. Moving the home: every actor on an occupied slot, and the invariant

L1 and L2 both end with the home off the box, so every way anything can touch a slot that holds a
home has to be accounted for. `releaseSlot` and `terminateSlot` are the two verbs; this is every caller
in `runtime_ecs_ec2*.go`, the actor behind it, and what it shares with L1.

**Actors that reach an occupied slot**

| Actor | Verb on the slot | What serialises it with L1 | Rule for a dark slot |
|---|---|---|---|
| the occupant's own Start, same-type affinity (`placeHome`) | claims it, wakes it | the home's Start count and claim lock | the one actor a marker does not refuse: it overtakes L1 by moving the count, and L1 stops at its next check; its own `slotLostAfter` verdict stays for a slot it holds a claim on |
| the occupant's own Start, size/class mismatch (`placeHome` → `releaseAndReplace`) | `releaseSlotSince` in the background | `startSuperseded` at each boundary; the mount lock | same |
| the occupant's own Start, reserved/outdated (`replaceReservedSlot`) | `moveHomeOff` (`releaseSlot`) then `terminateSlot` on the **old** box | the same count and mount lock | same; `terminateSlot` refuses a quarantined box (rule 4) |
| another member's eviction (`evictLongestIdle`) | `victim.releaseSlot` on the longest-idle occupant | the victim's own runtime: its mount lock and Start count | **refused** at `releaseSlotSince` while a rescue marker exists (rule 2); skipped at selection as an optimisation |
| another member's `makeRoom` | `victim.releaseSlot`, then the **caller's** `terminateSlot` of the box | the victim's mount lock and count; the terminate itself is not fenced | **refused** the same way, and its `terminateSlot` re-reads the box's marker and stamp (rule 2); a dark dormant occupant has no running task, so `tasks[id] > 0` does not exclude it |
| the golden baker (`ReleaseForBake`, and the seed's and probe's Start/Stop) | `releaseSlot` on the seed's or probe's home | the same home locks and count; the baker's own bake lease does **not** serialise it with L1 | no special case: the seed and the probe are ordinary workspaces; a rescued one fails the bake, which the baker's own deadline already handles |
| hibernation (`hibernate`, from the reaper) | `releaseSlot` as its first step | the same | **refused** the same way and retried; after L1 the home is detached and hibernation proceeds |
| `Destroy`, `EraseHome` (a member or admin action) | `Stop` then `releaseSlot` | the same; the `Stop` moves the count | their `Stop` overtakes L1; their umount then waits on a box that cannot answer, as today. L1 cancels the CP's pending commands first so the lock is not held for the umount's life |
| `sweepVolume` drift and terminate stages | `releaseSlot`, then `terminateSlot` | the same | **refused** the same way (the dark scan runs first in the pass) |
| `sweepFreeSlots` | stop / terminate a slot with no home | none needed | a free dark slot is not an L1 target; the pool view marks it |
| #2474, the operator's force-stop | the occupant's `Stop`, cancel, `StopInstances` | the same count protocol (decision 4) | allowed; it is the operator's call |

**Paths that choose a slot that holds no home yet** (and so cannot be a dark occupied slot):
`placeHome` free-slot attach (a list filtered on `af-role=slot` and `slotExcluded`; one re-read before
the attach, one after the attach and the claim), `growPool` → claim (a brand-new slot; no re-read), and
`adoptEarlier` (a slot an earlier attempt launched, including one that died before its claim was
written or whose claim expired; one re-read after the claim). Claim TTL (`claimTTL`, 300 s) is not an
actor: an expired claim is ignored by `claimLive`, which is what lets an overtaken Start leave a claim
behind, harmlessly, for at most one TTL.

**The invariant, in one sentence:** *every actor on a slot that holds a home reaches it through that
home's mount lock and Start count and re-reads the count before each destructive call; every automatic
releaser other than the occupant's own Start is refused, at its destructive entry and under that same lock, while
a rescue marker exists for the home; and a slot with no home is never an L1 or L2 target.*

That is why this design acts only on slots with a home and no live claim (decision 1), and why it
does not need a fence on the free-slot and new-slot paths. #2474's first version quarantined by a tag
and then read the holders, the right shape for a slot that **may be free**; its four rounds (findings
labelled R2–R9, as in that review) showed every place that shape has to be completed:

- **R3** (the tag was written after the occupant was stopped) has two halves. Another member choosing
  the slot cannot happen for an occupied slot (the table). A **new Start of the occupant's own
  workspace** can, and is exactly what the ownership protocol of decision 4 is for.
- **R3b** (a placement that listed its candidates before the tag and attached after it) and **R7** (a
  brand-new slot with no re-read after its claim) are free-slot and new-slot paths: unreachable for an
  occupied slot, and a slot younger than `darkBootGrace` is not dark.
- **R8** (a cleanup that deleted the home's claim by volume id and so deleted a *newer* Start's claim):
  every claim deletion by the sweeper is guarded by the ownership token of decision 4, which is
  read under the lock, never reused from the decision.
- **R9** (a quarantined replacement slot was retired like an unused one, which **terminates** it and
  destroys the evidence) and **R2** (a quarantined slot that also carried a replacement reservation was
  sent through `replaceReservedSlot`, which terminates the old box): rules 1 and 4 below.

Four rules follow for the code that will implement this, each cheap to test:

1. `slotTypeMatches` treats a quarantined slot as a plain mismatch **before** it reads the reservation
   or the template, so a home left attached to a quarantined box (an L2 whose detach failed) is
   released and re-placed rather than woken on a box the CP is no longer allowed to command
   (`SendCommand` is granted to `af-role=slot` only). `releaseSlot` waits for a box that is still
   `stopping` before it detaches.
2. The exclusion is **at the destructive entry, under the home's mount lock**, not at candidate selection.
   A candidate picked by `evictLongestIdle` or `makeRoom` before the rescue began, and a rescue that
   writes its marker after that, would otherwise meet in `releaseSlotSince`, which reads the count at its
   own entry and does not move it. So `releaseSlotSince` (the one entry every `releaseSlot` caller shares)
   refuses while a rescue marker exists for the home and the caller is not that rescue. The rescue writes its
   marker under the claim lock after confirming the home is still attached, and takes the mount lock only
   afterwards; a releaser that already holds the mount lock finishes first (its pending SSM commands are
   cancelled in step 2, so it cannot hold the lock for the life of an umount), and the rescue then finds the
   home gone and stops. For `makeRoom` the
   box's `terminateSlot` after the victim's release re-reads the box's marker and stamp (an automatic-intent
   refusal, rule 4); that is safe because a rescue needs a home attached under the lock, and the victim's
   home is gone by then. The one actor the marker does **not** refuse is the occupant's own Start, including
   `releaseAndReplace` and `replaceReservedSlot`: it overtakes the rescue by moving the count (decision 4),
   and the rescue then stops at its next check. Candidate-selection skips (`af-dark-since`) are an
   optimisation on top, never the fence.
3. Ownership follows decision 4, and the check and the destructive call share one lease. A lock that
   cannot be taken, a count that cannot be read, or a lease that lapsed **aborts** the step: the
   fallback of `ifOwned`, which runs the step unguarded when the lock cannot be taken, is not used.
4. `terminateSlot` takes an intent. **Automatic** callers (the sweeper's terminate stage,
   `replaceReservedSlot`, `retireUnusedReplacement`, `makeRoom`) refuse a box whose role is
   `quarantined`. The operator's `TerminateQuarantinedSlot` (decision 30), which is authorised and
   audited, is the one caller that passes the explicit intent, so the only evidence-deleting action
   still works.

*Alternatives rejected.* **Fence every placement path with a post-claim quarantine check** (what #2474
tried): correct, but it spreads one concern over seven call sites and each of four rounds found one
that was missed; with the occupied-slot rule those sites are unreachable, and an unreachable check is
dead code that is wrong the day the rule changes. **Reuse the replacement reservation tag as the
fence**: its two-sided checks are already there, but its end of life is `terminateSlot`, which destroys
the evidence L2 exists to keep. **Refuse `terminateSlot` for quarantined boxes everywhere**: breaks the
operator's Terminate button.

### 4. Who owns what, the ownership token, and what survives a restart

- **Start** owns the slot while it holds a live claim; its `slotLostAfter` is its verdict and
  `abandonLostSlot` its action. Unchanged.
- **The sweeper** owns a slot with a home and no live claim. It is a third party to the workspace.
- **The admin action** (#2474) is the same code from a person: it takes the same ownership and stops at
  the same gates (decision 2, steps 3 and 4). Moving it onto that code, so that it also refuses or
  deregisters first while a task ENI is attached, is part of the #2450 implementation: it is the
  same ENI gate, and the verification belongs with it. Today #2474's gate is only "ECS agent connected
  and SSM Online", and neither its guide nor its 0045 note mentions the ENI risk; switching its gate to
  `slotDark` (boot grace, durations, the Agent probe) is a behaviour change this record proposes, not
  one that exists.

**Lock order.** Everywhere, the mount lock (`homeLockMount`) is outer and the claim lock (`homeLockClaim`) is inner,
as in `quarantineSlot` today (mount, then `ifOwned`). Nothing waits for the mount lock while holding the claim lock:
the rescue's prepare step takes the claim lock alone and lets go of it before it asks for the mount lock, and the
cancel in step 2 needs neither. The marker and the token are what carry the fence across the gap between the
two; each destructive call re-checks the token under the claim lock nested inside the mount lock.

**The ownership token.** The decision pass records the Start count `g_d`, the home's volume id and the
launch epoch. To begin L1, **under the home's claim lock and lease, in one step**: read the count `g`;
require `g == g_d`; if so, bump it to `g_r` (the increment a Stop makes) and write the rescue marker
with `g_r`. Otherwise something has acted since the decision (a member's Stop, a new Start, a Destroy)
and the sweeper steps aside; the next pass decides again. Before each later destructive call, under the
lock and lease, require the count to be exactly `g_r` and the marker to be present and to name the same
home and launch epoch; any other value means somebody else owns the home now, and the rescue stops,
drops the marker and leaves the rest. Calling Stop and then checking the count would be wrong in both directions: if a new Start (`g_d + 1`) had
landed first, our Stop's own bump would abort it, and reusing `g_d` after our own bump would read our own
Stop as an overtaking. So the check of `g_d` and the bump are one locked step, and the token is the `g_r` it
produced.

**The rescue marker** is an instance tag,
`af-rescue = <home volume id>/<launch epoch>/<phase>/<g_r>/<began>/<blocked_since>`, written **before the first
side effect**. Phases: `begun`, `deregistering`, `deregistered`, `stopping`, `stopped`, `detaching`, `detached`. Every AWS
side effect on the box is preceded by the phase that names it (`deregistering`, `stopping`, `detaching`), written under
the strict conditions, so a crash between the call and the next write still leaves a marker that says the box
was acted on. The two times persist across a restart
(`rescueBlockedAfter` runs from `blocked_since`, written when a step first fails its bound; `af-dark-since` is
not that time). The marker is what survives a restart; the detection predicate cannot, because a box that is
`stopping` or `stopped`, or whose SSM came back after the deregistration, is no longer "dark" by definition.

A **resume scan**, separate from detection, lists every instance carrying a marker whatever its state and
continues the rescue's own steps only if all of these hold: the same volume is attached to this box; the box's `LaunchTime` equals the marker's epoch **in
every phase, `stopped` included**; and the Start count is still `g_r`. The phase is a record of what this
rescue did, not evidence of the box's present state: before any step that depends on the state of the box,
and in particular **immediately before the detach**, EC2 is asked and must say `stopped`. A box that was
stopped, restarted by an operator from the EC2 console (which moves `LaunchTime` and not the Start count),
and found by the scan is therefore never detached from while running. A marker that fails these conditions is
not discarded unread: it goes to the finalizer below.

**Ownership of the marker.** The rescue may only change or remove **its own exact marker**: a removal is a
conditional `DeleteTags` carrying the value that rescue wrote (EC2 deletes a tag by value only if the value
matches), and an update replaces a value it read. A scan that finds the count moved, the epoch changed or
the home gone removes that marker the same way, so a late-resuming old rescue can never delete a newer
rescue's marker (the R8 shape).

**Two jobs with two sets of conditions.** The *recovery* of a rescue (continuing a phase that touches the
box or the home) needs the strict match above: same volume, same `LaunchTime`, count still `g_r`, and EC2
saying `stopped` before the detach. The *finalizer* only records that an episode happened. It touches neither
the box's state nor the volume, only tags, so it needs no count or epoch match:

- Before each of the deregistration, the stop and the detach, the marker is advanced to the phase that names
  it (`deregistering`, `stopping`, `detaching`: the intent), under the strict conditions; after the call
  succeeds, to the completed phase (`deregistered`, `stopped`, `detached`, the last also recording `detached_at`).
- A marker whose phase is beyond `begun` (any intent or completed phase) means the box may have been acted on,
  so it is an episode whatever became of the rescue: the finalizer writes `af-wedge-at = marker.began`, **only if
  that is later than the `af-wedge-at` already there**, and then removes its own exact marker. A marker still
  at `begun` has taken no action on the box (its preparation touched the Start and the claim only) and is just
  removed. `CreateTags` overwrites unconditionally, so "only if later" needs a fence: every finalizer's read,
  compare and write of `af-wedge-at` runs under a per-instance lease (`ec2-slot-wedge/<instance id>`, from the
  same `cp_lease` store as the home locks), shared by all rescues of that box whatever the home or epoch, with
  the lease re-checked by the clock before the write. If the lease cannot be had the marker stays and the next
  pass retries, so a late, old write cannot land after a newer one.
- `detaching` with the volume no longer on this box is decided by the count: still `g_r` means nobody else
  could have moved it (every other releaser is refused while the marker exists, rule 2), so it was this
  rescue and it is complete; a moved count means the occupant's own Start moved it, and the episode is
  recorded the same way. Either way the repeat window is correct, and the rescue's own remaining steps are
  dropped.

So a crash between any two steps loses **no episode**; what is dropped, when somebody else got there first, is
the rest of the rescue, never the record that the box went dark. A rescue that has begun is not cancelled by
the agents coming back, only overtaken by a person or a Start. The home lease (`homeLeaseTTL`, 90 s)
expires on a dead CP, so the replica that resumes can take it.

- **Two replicas** both detect and both stamp the same value; ownership is taken in the locked step,
  so the second finds the count moved and steps aside. Every step is idempotent (`StopInstances`, a
  deregistration of an unregistered instance, a detach of a detached volume).
- **Audit** is a callback, because the adapter has no database: the same shape as
  `Config.OnSlotAutoReplace`, actor `system`, actions `pool.slot_dark_detected` (once per episode),
  `pool.slot_dark_rescued`, `pool.slot_dark_blocked`, `pool.slot_dark_quarantined`.

### 5. Surfaces and rollout

- **Pool view**: a slot's `dark_since` and the rung it is on (observing / rescuing / blocked / rescued
  / quarantined), next to the existing `stopping_task` of #2474; `registered` stays as it is.
- **Member**: a rescued workspace reads `stopped`; a Start behind a rescue reads #2447's blocked-phase
  line instead of `starting` for nothing. No new member screen.
- **Operator**: the audit actions above, one log line per rung, and the guide's pool section.
- **Rollout**, because the thresholds are a guess: `AF_ECS_EC2_DARK_SLOT_ACTION` is
  `off | detect | rescue | quarantine`, default **`detect`** in the first release (stamp, pool view,
  audit `detected`, no other AWS write). `rescue` is enabled per deployment once the detect-only
  window has shown no false positive; L2, including the forced-detach exception, is behind
  `quarantine`. The default changes in a release note, never silently.
- **IAM**: new instance tag keys (`af-dark-since`, `af-wedge-at`, `af-rescue`) in the CP role's
  tag-key allow-list (`20-platform`), beside the existing `af-slot-idle-since`;
  `ssm:DescribeInstanceInformation` (#2474 adds it already); `ecs:DeregisterContainerInstance` is
  already granted. Every `ec2:StopInstances` and `DetachVolume` call site is entered in
  `deploy/local/cfn-cp-ec2-scope-test.py`.

## Open questions (each needs a live run)

1. **A stop with a task ENI attached, on #2474's path.** Does a restarted box regain egress on a
   deployment with and without NAT? If not, the manual action must refuse or deregister first while an
   ENI is attached. The #2450 implementation owns this check and the change to #2474's action
   (decision 4); no separate issue is filed.
2. **Benign dark windows.** How long can the ECS agent or the SSM agent be silent on a healthy box
   (agent self-update, docker restart, an instance reboot, a short stall)? `darkAfter` 10 min is a
   guess until the detect-only window answers it.
3. **SSM freshness.** How often is `LastPingDateTime` refreshed, and when does `PingStatus` turn
   `ConnectionLost`? It no longer gates anything (decision 1 uses no external timestamp to shorten the wait), but the pool view shows it, and a corroborating signal would be welcome.
4. **What deregistration does to the task.** The 2026-08 incident observed the ENI going after the
   force-deregistration; whether a task stuck `STOPPING` is released, or its processes keep running as
   orphans (which is what AWS documents for deregistration), is not established here. #2447 observed
   that starting the box cleared it, on a box that had not been deregistered. Acceptance: on a deregistered container instance, does starting the box make its agent reconnect and report the task, or is the task already gone? The design relies on neither.
5. **Does a forced detach, or a Force stop, corrupt a home?** Measure an ext4 / XFS journal replay after
   each on a thrashing box. This gates `quarantine` mode.
6. **Is a workspace-Agent probe cheap and honest on a thrashing box, and where does the CP reach the
   Agent from?** Decision 1 needs it for a service that is up; if it times out slowly the sweep pass
   needs a budget.
7. **Do `MemoryMin` and the host reserve (#2452, the memory-pressure response) change the base
   rate?** If dark episodes become rare, L2's repeat rule may never fire and `darkRepeatWindow` can be
   dropped.

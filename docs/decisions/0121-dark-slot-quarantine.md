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

External timestamps shorten the wait only when they prove **both** halves independently: an
SSM `LastPingDateTime` older than `darkAfter` *and* a task stuck `STOPPING` on this container
instance (`StoppingAt`, #2447) older than `darkAfter`, both from the current launch epoch and
both confirmed by this pass. One of them alone proves nothing about the other half: an SSM agent
that has been offline for days on a box whose ECS agent was fine until a minute ago would
otherwise read as ten dark minutes.

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
  1. take ownership (decision 4), write the rescue marker, cancel the CP's own pending SSM commands
     on the box (they hold the home's mount lock for as long as they live);
  2. end any Start as a Stop does (`abortStarts`, desiredCount 0, claim dropped);
  3. force-deregister the container instance, and **verify that the task ENI is physically gone**
     (`DescribeInstances`) within a bound. Deregistration is not assumed to do anything else: AWS
     documents that it does not stop the task's processes;
  4. only with the ENI gone, `StopInstances` (graceful; Force on a stop that has not finished,
     never on an instance started again since, as in #2474), and **verify `stopped`**;
  5. only with the box stopped, an ordinary detach of the home. The role stays `slot`, `af-wedge-at`
     is stamped, the marker is removed.

  If the ENI does not go, or the box does not stop, within the bounds, the rescue is **blocked**:
  nothing further is done, the box is left as it is, the marker keeps its phase, and the pool view
  and the audit say so. A blocked rescue is retried every pass, and is the operator's to resolve
  (the manual force-stop or the Terminate button) unless the next rung is enabled.
- **L2. Quarantine** instead of leaving the box in service when it goes dark **again** within
  `darkRepeatWindow` (default 24 h) of its last `af-wedge-at`, **or** when a rescue has been blocked
  for `rescueBlockedAfter` (default 30 min). Quarantine tags the box `af-role=quarantined` with a
  reason, keeps it, and never collects it (decision 30). For a blocked rescue it also permits the one
  exception `abandonLostSlot` already makes, a **forced detach** of the home from a box that will not
  stop. That is a yanked disk under a workspace nobody can reach, and the trade-off is explicit: the
  loss is bounded by that workspace's unflushed writes (ext4 and XFS replay their journal on the next
  mount), against a member blocked indefinitely. It is enabled only by `quarantine` mode, after the
  measurement in Open question 5.

A task still `STOPPING` is **not assumed to be cleared** by the rescue. Whether it is, is Open question
4; if it persists, the member's next Start shows #2447's blocked-phase line and the existing remedy
applies (start the old instance once so its agent reports; the guide says how).

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
| the occupant's own Start, same-type affinity (`placeHome`) | claims it, wakes it | the home's Start count and claim lock | L1's ownership overtakes it; the Start's own `slotLostAfter` verdict stays for a slot it holds a claim on |
| the occupant's own Start, size/class mismatch (`placeHome` → `releaseAndReplace`) | `releaseSlotSince` in the background | `startSuperseded` at each boundary; the mount lock | same |
| the occupant's own Start, reserved/outdated (`replaceReservedSlot`) | `moveHomeOff` (`releaseSlot`) then `terminateSlot` on the **old** box | the same count and mount lock | same; `terminateSlot` refuses a quarantined box (rule 4) |
| another member's eviction (`evictLongestIdle`) | `victim.releaseSlot` on the longest-idle occupant | the victim's own runtime: its mount lock and Start count | **skip** a slot stamped `af-dark-since` or carrying a rescue marker |
| another member's `makeRoom` | `victim.releaseSlot`, then the **caller's** `terminateSlot` of the box | the victim's mount lock and count; the terminate itself is not fenced | **skip** (a dark dormant occupant has no running task, so `tasks[id] > 0` does not exclude it) |
| the golden baker (`ReleaseForBake`, and the seed's and probe's Start/Stop) | `releaseSlot` on the seed's or probe's home | the same home locks and count; the baker's own bake lease does **not** serialise it with L1 | no special case: the seed and the probe are ordinary workspaces; a rescued one fails the bake, which the baker's own deadline already handles |
| hibernation (`hibernate`, from the reaper) | `releaseSlot` as its first step | the same | **skip** and retry; after L1 the home is detached and hibernation proceeds |
| `Destroy`, `EraseHome` (a member or admin action) | `Stop` then `releaseSlot` | the same; the `Stop` moves the count | their `Stop` overtakes L1; their umount then waits on a box that cannot answer, as today. L1 cancels the CP's pending commands first so the lock is not held for the umount's life |
| `sweepVolume` drift and terminate stages | `releaseSlot`, then `terminateSlot` | the same | **skip** a dark-stamped slot (the dark scan runs first in the pass) |
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
releaser skips a slot that is dark-stamped or under a rescue marker; and a slot with no home is never an
L1 or L2 target.*

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
2. Every automatic releaser in the table skips a slot stamped `af-dark-since` or carrying a rescue
   marker.
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

**The rescue marker** is an instance tag, `af-rescue = <home volume id>/<launch epoch>/<phase>/<g_r>`,
written **before the first side effect**, with phases `begun`, `deregistered`, `stopped`. It is what
survives a restart; the detection predicate cannot, because a box that is `stopping` or `stopped`, or
whose SSM came back after the deregistration, is no longer "dark" by definition. A **resume scan**,
separate from detection, lists every instance carrying a marker whatever its state (`running`,
`stopping`, `stopped`) and continues the next phase only if all of these still hold: the same volume is
still attached to this box; the launch epoch is unchanged for the phases before `stopped`; and the
count is still `g_r`. If the volume is gone or is another home, the box was started again, or the count
moved, the marker is dropped and the old rescue is **not** applied to the new situation. A rescue that has
begun is not cancelled by the agents coming back, only overtaken by a person or a Start. The home lease
(`homeLeaseTTL`, 90 s) expires on a dead CP, so the replica that resumes can take it.

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
   `ConnectionLost`? The early-arm rule of decision 1 is only as good as that.
4. **What deregistration does to the task.** The 2026-08 incident observed the ENI going after the
   force-deregistration; whether a task stuck `STOPPING` is released, or its processes keep running as
   orphans (which is what AWS documents for deregistration), is not established here. #2447 observed
   that starting the box cleared it. The design does not rely on either.
5. **Does a forced detach, or a Force stop, corrupt a home?** Measure an ext4 / XFS journal replay after
   each on a thrashing box. This gates `quarantine` mode.
6. **Is a workspace-Agent probe cheap and honest on a thrashing box, and where does the CP reach the
   Agent from?** Decision 1 needs it for a service that is up; if it times out slowly the sweep pass
   needs a budget.
7. **Do `MemoryMin` and the host reserve (#2452, the memory-pressure response) change the base
   rate?** If dark episodes become rare, L2's repeat rule may never fire and `darkRepeatWindow` can be
   dropped.

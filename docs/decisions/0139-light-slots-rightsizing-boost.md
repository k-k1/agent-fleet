# 0139. Light slot classes, a usage history, right-sizing and a boost for `ecs-ec2` workspaces

English | [日本語](0139-light-slots-rightsizing-boost.ja.md)

- Status: **accepted** (2026-11-17; proposed 2026-11-16). Documentation only: nothing below is built. Every code fact was read
  from the tree at `f21201685` on this date. **What is measured and what is not.** Prices and
  per-instance performance quoted from [70](../log/70-slot-instance-classes.md) §70.3 are that journal's
  Pricing API reads and benchmarks (ap-northeast-1, 2026-08-22). **No price for a `medium`, a `t4g` or a
  `t3` instance was read for this record**, and no AWS call was made: every such figure below is an
  estimate from list-price proportionality and is marked as one. No decision rests on an estimate alone;
  the ones that wait on a measurement say so. Open questions 1 and 2 and the candidates' prices were
  measured on 2026-11-17 on a development deployment; the results are in the addendum, and the body keeps
  the text as accepted. Reviewed 2026-11-16 by a separate session (codex
  `gpt-6.1-sol`): rounds 1 and 2, 11 and 3 findings, each checked against the tree and folded into
  this text.
- Tracking: #4340.
- Follow-ups: #4332, #4333, #4334, #4335
- Related: [0045](0045-ec2-persistent-workspace.md) decision 8 (a slot is exclusive to one member;
  co-tenancy rejected) and decision 21 (size is one axis, memory; the ladder is declared by the operator) /
  [0074](0074-engine-instance-classes.md) (a stored choice wins; a change reaches the NEXT instance) /
  [0120](0120-workspace-memory-pressure.md) (the PSI sampler and the `mem_pressure` axis) /
  [0048](0048-member-cloud-cost.md) (per-member cost from the `af-membership` tag) /
  [70](../log/70-slot-instance-classes.md) (slot classes, arch portability of home)

## Context

The request: "put light members on something cheaper, and change members' instance types cleverly from
what they actually use." Two ideas were on the table — pack several light members onto one shared
instance, and right-size each member automatically — and a third came out of the discussion: a
member-pressed **boost**.

### What the code says today

- **What a member's next start runs on is resolved in the CP, not in the runtime.**
  `withResolvedSize` (`workspace_lifecycle.go`), which every start and resize path goes through, sets
  the memory requirement (`resolveWorkspaceSize`: the member's `mem_limit`, clamped by the tenant's and
  the deployment's maximum) and the class (`resolveSlotClass`: member → tenant default → deployment
  default, each one both declared by the deployment and in the tenant's `AllowedSlotClasses`; when no
  declared class is allowed at all, it returns the deployment default anyway, so a Start never fails for
  want of a class). The
  runtime's `rungFor(classID, memBytes)` (`runtime_ecs_ec2_pool.go`) then only picks the smallest rung of
  that class whose declared memory is at least the requirement. **A requirement of 0 means the smallest
  rung of the class** (ADR 0045 decision 21).
- **The Console does not ask either of them.** The member list and the member detail recompute the box
  from the stored `mem_limit` and class with `slotFor` / `ladderFor` (`console/src/features/settings/parts/adminShared.ts`)
  over the ladder that `SizingProfile` declares.
- **The class and the requirement are per member** (`limits.go`: `SlotClass`; tenants carry
  `AllowedSlotClasses`), and **a change takes effect at the next container start**
  ([70](../log/70-slot-instance-classes.md) §70.4.3). Moving to another rung is therefore already "the
  next Start takes a slot of the new type and the home EBS is attached there". How long that takes today
  is **not** decision 8's 22–27 s: ADR 0045 decisions 10-5 and 12 corrected it, with Service Connect, to
  43–110 s.
- **The workspace is capped at the rung minus a flat host reserve**: `workspaceMemCapMiB` returns
  declared MiB − `ec2HostReserveFloorMiB` (2048). The reserve is that large because it has to absorb both
  the daemons (measured ~1.2–1.4 GiB) and the gap between a rung's declared memory and the box's real
  MemTotal (an "8192 MiB" m7i.large reports 7784 MiB, measured). It does not shrink with the box.
- **The shipped ladder starts at `large`** (`30-ingress.yaml` `Ec2SlotTypes`: `standard` = m7i,
  `saver` = m6i, both x86_64, 8/16/32 GiB).
- **The Agent already measures the workspace**: `resources.Read()` (CPU from `cpu.stat` `usage_usec`,
  memory) and `mempressure.Current()` (PSI `some`/`full` avg10/avg60, working set, refault rate) behind
  the Stats endpoint, which the CP passes through to the Console (`agent_client.go`). **The CP keeps no
  history** of those figures; its usage sampler (`usage.go`, every 5 minutes by default,
  `AF_USAGE_SAMPLE_INTERVAL`) credits running seconds only. The Agent's own `mempressure.Series` keeps
  about four days of per-minute lines, on the workspace, for threshold fixing (ADR 0120 decision 8).
- **The CPU figure is not a fixed-window measurement.** `cpuMeter` (`resources.go`) returns the delta
  since the previous Stats call by anyone, so its window is ~4 s while someone has the Console open and
  whatever the gap was otherwise. PSI is the kernel's latest avg10/avg60. The Stats client gives up after
  5 s (`agentStatsClient`), so a busy box is the likeliest to go unanswered.
- **The root volume is per slot**, `SlotRootVolumeGiB` default 100 (~$9.6/month while the slot exists,
  [70](../log/70-slot-instance-classes.md) §70.3). An idle slot is terminated on a timer only when
  `Ec2SlotTerminateAfterSec` is set (ADR 0045 decision 23; default 0 = never); independently of it,
  `makeRoom` (`runtime_ecs_ec2_slots.go`) terminates a free or dormant box of another size when the pool
  cap would otherwise fail a Start.

## Decision 1 — no co-tenancy for now. ADR 0045 decision 8 still holds, and the first saving is taken with smaller exclusive slots

**What co-tenancy saves is real, and it grows as boxes shrink.** On-demand price is linear in vCPU and
memory within a family (measured for m6g, m7g, m8g, m6i and m7i in
[70](../log/70-slot-instance-classes.md) §70.3), so the box's memory itself costs the same whether it is
split or not. What packing saves is everything paid **per box**: the root volume (ADR 0045 decision 8
names it) and the host reserve. The reserve is a flat 2 GiB of the declared memory, so it is a quarter of
an 8 GiB rung and **half of a 4 GiB rung**. For example (estimated, not measured), seven 4 GiB workspace
caps need seven 8 GiB exclusive boxes (56 GiB) but fit, with no overcommit, in one 32 GiB box (28 GiB of
caps plus one 2 GiB reserve). Overcommit — caps adding up to more than the box — would save more, and is
the one thing ruled out on its own: the incident behind ADR 0120 was **one member alone** driving an
8 GiB slot into a twenty-minute refault thrash.

It is still rejected for now, because of what it costs, read from the tree:

- **ENI density.** Workspaces are `awsvpc` (`runtime_ecs_ec2_taskdef.go`), one ENI per task, so a box
  holds only a handful of tasks without ENI trunking.
- **Every "one slot = one home" rule** — `freeSlots`, `releaseSlot`, `af-slot-idle-since`, idle stop
  (a shared box stops only when everyone is idle), quarantine (one quarantine takes N members down),
  eviction.
- **Cost attribution.** ADR 0048 attributes a slot to a member by `af-membership`; a shared box is
  shared cost, which ADR 0048 decision 4 refuses to distribute.
- **Isolation.** The root volume, the kernel and the write layers become shared between members (ADR
  0045 decision 8's "prices that remain" multiply by N), and across tenants it would be unacceptable.

A separate Docker host mixed into the pool is worse still: the CP runs exactly one `AF_RUNTIME`, and the
`docker` adapter assumes the Docker daemon is local to the CP.

Decision 2 takes the first, cheaper step with none of those costs: a member whose workspace fits in
2 GiB moves from an 8 GiB box to a 4 GiB one. What it leaves on the table is exactly the per-box reserve
and root, which co-tenancy would save on top.

**Reopen when** decision 3's history shows many members whose sustained working set fits the smallest
light box with room to spare, so that the per-box reserve and root are what is left to save. Even then
the shape is a same-tenant shared slot inside `ecs-ec2`, never a second runtime.

## Decision 2 — light classes are separate, operator-declared, opt-in classes. The shipped ladder does not change

The operator declares light classes in `AF_ECS_EC2_SLOT_TYPES` with the existing
`id|label|arch|type:memMiB[:vcpu]` syntax. **Nothing new is needed in the parser or the launch path.**

🔴 **A light rung never goes into an existing class.** Because a requirement of 0 resolves to the
smallest rung, adding `medium` under `standard` would silently move every member who never chose a size
onto a 4 GiB box with a 2 GiB workspace. A light box is something a member or an administrator
chooses (or decision 4 proposes), never something a ladder edit does to them.

Candidates, to be measured before any is recommended in the guide:

| Class (proposal) | arch | Rungs | Why |
|---|---|---|---|
| `light-arm` | arm64 | m8g.medium (1 vCPU / 4 GiB), m8g.large | `medium` exists only in the Graviton m families; m8g is the fastest per dollar in §70.3.2 |
| `burst-arm` | arm64 | t4g.medium (2 / 4 GiB), t4g.large (2 / 8 GiB) | Burstable: a workspace is mostly idle (§70.3.3), and the baseline is what an idle box needs |
| `burst-x86` | x86_64 | t3.medium, t3.large | The only light option that keeps home on x86_64 (no arch move, §70.5) |
| `mem-arm` (option) | arm64 | r8g.medium (1 / 8 GiB) | Memory, not CPU, is what a light workspace runs out of |

- 🔴 **A 4 GiB rung caps the workspace at 2048 MiB** (4096 − 2048; the reserve already absorbs the
  MemTotal gap, so nothing more is taken off). The daemons get the box's real MemTotal − 2048 MiB, which
  on a 4 GiB box is also not measured. Whether one agent session plus the Agent fits in 2 GiB is **not
  measured**; that measurement decides whether the 4 GiB rungs are offered at all, or whether light starts
  at 8 GiB (measured in the addendum: light work fits, a large Go build does not). Lowering the reserve on small boxes is not part of this decision: it was raised to 2 GiB
  because a box thrashed with less.
- **Burstable classes run in `unlimited` credit mode.** Neither the slot launch template
  (`40-ec2-pool.yaml`) nor the CP sets a `CreditSpecification` today, so a t-class gets the EC2 default,
  which is documented as `unlimited` for t4g and t3 (confirmed for t4g in the addendum, open question
  2). `standard` mode would throttle a build to the baseline exactly when the member is waiting on it; `unlimited` bills surplus credits instead, which shows up in the member's
  cost line (ADR 0048) and in the history (decision 3) as sustained CPU, which is decision 4's signal to
  move them off a burstable class.
- **The arm64 classes inherit everything [70](../log/70-slot-instance-classes.md) needed for m8g**
  (multi-arch image, arm64 AMI, a golden per arch, the one-time home re-install of §70.5). The x86 burst
  class needs none of that.
- **Fragmentation** ([70](../log/70-slot-instance-classes.md) §70.7): each class keeps its own spare
  slots, so a light class can add retained roots on top of the ones the existing classes already keep. A
  light member's saving per month is of the same order as one retained root (estimated: a `medium` at half
  of m8g.large's measured $0.11594/h is ~$0.058/h, so ~$9.3 saved over 160 h against an m8g.large; a root
  is ~$9.6/month). Whether a light class pays off therefore depends on how many extra slots it leaves
  standing and how many members use it, and the guide recommends `Ec2SlotTerminateAfterSec` next to the
  classes, because that is the timer that stops idle spare slots accumulating.
- **No shipped default** (the rule of ADR 0074 decision 1): the example ladder is a proposal until the
  prices and the 2 GiB question are measured on a deployment.

## Decision 3 — the CP keeps a per-member usage history

Right-sizing and the guide's advice both need "how much does this member actually use", and today nobody
can answer it after the fact.

- **What**: one row per member, per day, **per box the member actually ran on** (instance type, class,
  and the override that put them there, if any — decision 4). A day with a boost in it has two rows.
  Each row holds running minutes, **measured minutes per signal**, CPU p50/p95, working set p95/max,
  PSI `full` avg60 max and minutes above the ADR 0120 warning threshold, refault rate p95, and minutes
  above the class's burst baseline.
- **The box comes from the launch, not from the settings.** The runtime records the instance type it
  actually placed the task on when a Start succeeds; the row is keyed by that. Reading it back from
  `withResolvedSize` would be wrong between a change and the next Start, when the settings already name
  the new box and the member is still on the old one.
- **Fixed windows, aggregated in the Agent.** The CPU figure on the Stats endpoint has no fixed window,
  and PSI is a point reading (Context), so neither can be sampled into minutes from the CP. The Agent
  already writes one line per minute (`mempressure.Series`); it adds CPU from `usage_usec` deltas over
  the same minute, and exposes a summary of the minutes since a cursor the CP passes back. The CP pulls
  that on the usage sampler's tick and folds it into the day row. Raw minutes are not stored in the CP.
- **Running time comes from the CP; measurements from the Agent.** The CP records every launch — start,
  stop, the box and the override that chose it — and running and boosted minutes are read from those
  records, never from Agent samples. Every Agent minute line carries the launch's id, handed to it at
  Start, so a minute is attributed to the box it was measured on.
- **The stop boundary.** A workspace that starts and stops between two sampler ticks is never polled
  while running. So the CP makes one bounded, best-effort pull just before it stops a task, and pulls
  whatever is still behind the cursor at the next Start: the series lives on home, which survives the
  stop, and the launch id keeps those minutes on the old box rather than the new one. Minutes that fall
  out of the series' ~4-day window before that happen are lost, and stay running-but-unmeasured.
- **CPU is normalised only when the rung declares its vCPU** (the field is optional,
  `AF_ECS_EC2_SLOT_TYPES` `type:memMiB[:vcpu]`); otherwise the column is NULL. The burst baseline is not
  something the CP knows today: a burstable rung that wants the burst column declares its baseline in the
  ladder (a new optional field), and without it the column is NULL.
- **Absent is not zero** (the rule of `memPressure` in `agent_client.go`): minutes the Agent did not
  measure are running minutes but not measured minutes, and a signal the Agent cannot read stays NULL.
  Because a busy box is the likeliest to go unanswered, decision 4 uses coverage as a condition, not a
  footnote.
- **Retention** 90 days, the window decision 4 needs with margin.
- **Visibility**: the member sees their own; a tenant admin sees their tenant's (member detail, next to
  the cost line of ADR 0048 decision 12). It holds no content, only resource figures.

## Decision 4 — right-sizing proposes first, applies only where a tenant turns it on, and never stops a running workspace

The recommender reads decision 3's history and produces, per member, **a target rung** (a class and a
memory requirement).

- **Up fast, down slow.** Up: PSI `full` above the ADR 0120 warning threshold for a sustained period on
  two days within seven, or the working set p95 above 85% of the cap. Down: working set p95 below 50% of
  the next rung down's cap **and** CPU p95 below 40% for 14 active days. Every threshold is provisional
  until phase 0 has data (as in ADR 0120 decision 8).
- **Down needs coverage.** A day counts toward a downsize only if its measured minutes are at least a
  fixed share of its running minutes (provisional: 80%) for every signal the rule reads, and only rows on
  the member's current box count. Missing minutes never make a member look lighter; an upsize can act on
  what was measured.
- **Within one architecture.** An automatic move never crosses x86_64 ↔ arm64: that move re-installs
  part of home (§70.5) and is a member's or an administrator's choice. Across architectures the
  recommender only proposes.
- **Sustained CPU above the baseline on a burstable class proposes a move off it**: to a declared,
  allowed, same-arch, non-burstable class whose rung is at least the current memory. When there is none —
  the shipped classes start at 8 GiB, or the tenant allows only burstable classes — the recommender only
  proposes and warns; it never moves the member out of what the tenant allows.
- **The administrator's stored choice is not overwritten.** The result is an `auto` override stored
  beside it, with the reason and the history window that produced it. Clearing it restores the stored
  choice.
- **One resolver, in the CP.** Overrides are applied in `withResolvedSize`, before the runtime's
  `rungFor` picks the rung, and are re-checked there on every Start against the classes the deployment
  declares now, the tenant's `AllowedSlotClasses` now, the tenant's and the deployment's memory maximum
  (`resolveWorkspaceSize`'s clamps) and the architecture of the stored choice. An override that fails a
  check is not applied and is reported, the way `resolveSlotClass` reports a class it cannot honour; it
  never falls through to the deployment default, which could be on another architecture. That strictness
  is the override path's contract; the existing resolution of the stored choice, including its last-resort
  fall back to the deployment default (Context), is not changed by this record. `rungFor` stays a pure
  rung picker.
- **The Console is told, not left to recompute.** The sizing API returns, per member, three things:
  the stored choice, the box the next Start will use (with the override that decides it and until when),
  and the box the running workspace is on. `slotFor` / `ladderFor` keep drawing the ladder, but they no
  longer decide what a member "gets".
- **New bounds**: a tenant minimum and maximum rung that no override leaves.
- **Applied at the next Start only.** A change never stops or restarts a running workspace; the member is
  told in advance ("from your next start: m8g.large, because …").
- **Phases**: 0 — history only (decision 3). 1 — proposals shown to the tenant admin and the member,
  applied by a click. 2 — automatic, per tenant, off by default, with a per-member opt-out.

## Decision 5 — a boost: the member asks for one rung up, for a limited time

- **What**: "Boost" on the member's workspace screen takes **the next rung up from the box the next
  Start would otherwise use** (the `auto` override's box if there is one, else the stored choice), in that
  box's class, for N hours. It is resolved by decision 4's resolver.
- **Stored apart from `auto`, with an order.** `boost` and `auto` are two separate overrides with their
  own lifetimes, resolved **boost > auto > stored choice > tenant default**. The recommender may rewrite
  `auto` while a boost is live; that changes what the member returns to, not the boost. When the boost
  expires, the next Start lands on whatever `auto` (or the stored choice) says at that moment.
- **When it applies**: at the next Start. If the workspace is running, the screen compares the boost's
  target with **the box the workspace is running on**: only when the target is bigger does it offer
  "restart into the bigger box now", saying that a restart ends the sessions running in it. When the
  target is the same or smaller (a workspace still on an expired boost's box after `auto` moved down, for
  instance), it says the boost applies from the next start and offers no restart. It never restarts on
  its own.
- **Ending**: at expiry the `boost` override is removed. A running workspace is never shrunk under the
  member, so it can keep the bigger box after expiry until it stops. Boosting while a boost is live
  extends it and does not climb further; boosting after expiry, even while still on the old boost's box,
  is a new boost, computed from the next-start box as above.
- **What the monthly bound counts.** The tenant admin turns boost on and sets the highest rung and a
  monthly budget of **boosted running hours**: hours the member actually ran on a box a boost put them on,
  read from the CP's launch records (decision 3), **including hours after expiry**. Granted hours that were never started do not count. When the budget is spent the
  button is disabled; a workspace already running on a boosted box is still not stopped, and the overrun
  is shown to the tenant admin and on the cost line.
- **Where it meets ADR 0120**: when the pressure responder warns or refuses a new session, its notice
  offers the boost instead of only "close a session".

## Consequences

- Nothing changes for a deployment that declares no light class and leaves the tenant switches off.
- The history adds one Agent call per running workspace per usage-sampler tick and one row per member,
  per day, per box.
- `withResolvedSize` gains the overrides in front of the stored choice; the Agent gains a per-minute
  summary endpoint; the CP records every launch (box, override, start and stop) and pulls the Agent's
  minutes at a Stop and at the next Start as well as on the tick. The Console's size display and the
  slots tab show where an override came from (`auto` or `boost`) and until when, or a member cannot tell
  why their box changed.

## Options discarded

- **Co-tenancy on a shared slot, or a Docker host mixed into the pool** — decision 1.
- **Adding `medium` to the existing classes** — silently moves every member with no stored size
  (decision 2).
- **Resizing a stopped slot in place (`ModifyInstanceAttribute`)** — slots are pooled, and taking a slot
  of the target type is already what Start does; an in-place change would leave the pool with a slot of a
  type no class declares.
- **Resizing a running workspace automatically** — ends the member's sessions without their consent.
- **The CP reading prices from the Pricing API to rank rungs** — the ladder's order is the operator's
  declaration (ADR 0045 decision 21); the recommender moves by rung, not by dollars.

## Addendum — measured on a development deployment (2026-11-17)

Measured on a development ECS deployment (`ecs-ec2`) running main `b5ec6c91b`. The three classes of
decision 2 were added temporarily to the ladder as separate classes and then reverted. Full report:
#4332 (comment of 2026-11-17).

- **Open question 1 — answered: 4 GiB rungs are usable for light members, not for large builds.**
  - On m8g.medium and t4g.medium, MemTotal was 3812 / 3836 MiB, and both the workspace's `memory.max`
    and docker's `HostConfig.Memory` were 2048 MiB, as decision 2 computed. Idle use was about 590 MiB.
  - A real agent session with the Agent ran git clone, `go vet`, `npm ci` and `tsc` to completion.
  - `go build ./...` of `control-plane` reached the cap (`memory.peak` 2048). The kernel OOM-killed
    `compile` inside the memcg: 3 kills on m8g.medium and 1 on t4g.medium, with up to 1.7 GiB
    anon RSS. The agent and the Agent were never killed and stayed responsive.
  - Container PSI avg10 peaks were some 6.03 / full 1.93 on m8g.medium and 1.91 / 1.13 on t4g.medium.
  - The host daemons (envoy, containerd, dockerd, ecs-agent, efs-proxy, the SSM agent, systemd units)
    used about 600 MiB together, and host MemAvailable never fell below 1139 MiB. They fit in what the
    box has left.
  - So the guide may offer 4 GiB rungs, labelled for light work. A member who builds large projects
    starts at 8 GiB.
- **Open question 2 — answered for t4g.** A CP-launched t4g.medium slot reported `unlimited`
  (`describe-instance-credit-specifications`). t3 was not launched, because the test home is arm64.
  The account's default credit specification is `unlimited` for both t3 and t4g.
- **Prices** (Pricing API, ap-northeast-1, Linux, shared tenancy, effective 2026-11-01, USD/h):

  | Type | USD/h |
  |---|---|
  | m8g.medium | 0.05797 |
  | m8g.large | 0.11594 |
  | t4g.medium | 0.0432 |
  | t4g.large | 0.0864 |
  | t3.medium | 0.0544 |
  | t3.large | 0.1088 |
  | r8g.medium | 0.07106 |

  The proportionality estimate in decision 2 holds: m8g.medium is exactly half of m8g.large. t4g.medium
  costs about 37% of m8g.large, so over 160 h it saves about $11.6 against an m8g.large. That is more
  than one retained root (~$9.6/month), as long as no surplus credits are billed.
- **A size of 0 stays on the default class.** With no class override, a workspace landed on m8g.large,
  the smallest rung of the default class, not on a light rung.

## Addendum — phase 0 of decision 3 built (2026-11-17)

The usage history of decision 3 is built (#4333). What the build settled where the decision left
room, and what it did not do:

- **Launch records.** The CP writes a row per container launch (`workspace_launch`): start, stop, the
  box and the override. It is written **before** `Start`, so a CP that dies between `Start` and a later
  write still knows the id the Agent stamps; a `Start` that fails closes it at its own start (zero
  seconds). The box is the runtime's `MachineProfile()`, the same resolution placement uses, and only a
  runtime that names a box takes part (`ecs-ec2`). A stop the CP did not issue is closed at the launch's
  last-seen sampler tick, so the gap to the next start is not counted. A workspace found running with
  no record (a CP crash mid-start, or one that predates the ledger) is **adopted** under the override
  `adopted`: running minutes, none measured, because no Agent knows its id.
- **The override column exists and is always empty** until decision 4's right-sizing and boost set it
  (#4334, #4335); a non-empty one splits the day row.
- **Agent.** The sampler that already writes `mempressure.Series` adds the launch id (`AF_LAUNCH_ID`),
  the window's CPU from `usage_usec` deltas it reads itself (never through the shared Stats meter) and the
  mean refault rate. `GET /workspace/usage-windows?since=` serves the lines after a cursor, from both
  series generations; lines with no launch id are skipped and the cursor moves past them.
- **CP.** The cursor is the Agent's own unix second, per member. A pull folds and moves the cursor in
  one transaction and only if the cursor still holds the value it started from, so a tick, a Stop and
  the next Start cannot count a minute twice; a pull that loses the race drops what it fetched. The Stop's
  pull is bounded (3 s) and best effort, and is skipped where the Agent is the reason for the stop (the
  silent-Agent and start-deadline stops). The pull after a Start retries for up to six minutes until the
  new container's Agent answers.
- **Day rows.** Days are **UTC**, like `usage_daily`: a running interval is split at UTC midnight, a
  measured window belongs to the day it ended in. The key is member, day, instance type, class and
  override. Quantiles are kept as fixed-bucket histograms (`usagehist`), because a quantile cannot be
  merged across pulls: a read interpolates inside a bucket (about 8% wide for memory) and is capped by the
  exact max where one is kept (CPU, working set, not refault). A signal with no measured minute is `null`, and CPU as a share of the box, and minutes above
  a baseline, are `null` unless the rung declares its vCPU (and its baseline: a fourth, optional field of
  `AF_ECS_EC2_SLOT_TYPES`, `type:memMiB:vcpu:baselinePct`).
- **Warning threshold.** "Minutes above the ADR 0120 warning threshold" is counted as PSI `some` avg10
  at or above 10 in the window's highest sample, the stage-1 value of ADR 0120 decision 2, which that ADR
  calls provisional.
- **API.** `GET /api/usage/me/history` (own), `GET /api/admin/usage/history?tenant=` (tenant admin;
  super admin may omit the tenant) and `GET /api/admin/tenants/{slug}/members/{key}/usage-history`.
  Retention is 90 days.
- **Not built.** The Console view (decision 3 says "welcome but optional"), right-sizing and the boost.
  Not verified on a real deployment.

## Open questions

1. ~~Does one agent session plus the Agent fit in a 2 GiB cap, and do the daemons fit in what a 4 GiB box
   has left?~~ Answered in the addendum: yes for light work, no for large builds.
2. ~~Is `unlimited` what a t-class slot actually gets on this deployment with no `CreditSpecification`
   set?~~ Answered in the addendum for t4g (`unlimited`). t3 is unmeasured, but the account default is `unlimited`.
3. The thresholds and the coverage share of decision 4, from phase 0 data.

## Addendum — decision 5 built, apart from the hour count (2026-11-17)

#4335 built the boost, and nothing in the decisions changed. What is in the tree and how it reads the text above:

- **One resolver.** `withResolvedSize` now lays `overlaySizeOverrides` over the stored choice
  (`workspace_boost.go`). The overrides sit in `workspace_size_override`, one row per (membership, source), so a
  `boost` and a later `auto` have their own lifetimes; only `boost` has a writer. A boost row stores the target
  rung (class and declared MiB) computed when it was pressed and is re-checked on every start: the tenant has
  boost on, the class is declared and allowed, it is the next-start box's class, the rung is declared and bigger
  than the next-start box, and it is within the tenant's and the deployment's memory maximum and the tenant's
  highest boost rung. A boost that fails is not applied and says why; it never falls to the deployment default.
- **The sizing API is `GET /api/workspace/sizing`** (the member's own workspace): stored, next-start (with
  `source` and `until`) and running boxes, whether a restart is offered, and the boost state. The running box
  is read from the Agent's measurement; when it cannot be told, no restart is offered. The admin roster rows
  carry `next_start` from the same resolver.
- **Expiry** is inert, not a write: an expired row is never applied, so the next start lands on what the stored
  choice says, and a running workspace is not touched.
- **The monthly budget is stored and its check exists behind `boostHoursCounter`, but the hours are not
  counted.** Counting them needs the CP's launch records (decision 3, #4333), which are not on `main`. Until a
  counter is wired the budget is not enforced, and both screens say so.
- **ADR 0120's warning does not exist yet** (phase 0 measures only), so the boost is offered next to the
  pressure chart on the Machine tab, above a provisional `some` level of 20%, instead of inside a notice.

## Addendum — the history view in the Console (2026-11-18)

The optional view of decision 3 is built (#4414): a table of the day rows under the running-time grid, on the member's own page and on the admin member detail. A `null` figure reads "not measured", never 0.

## Addendum — the tenant roll-up in the Console (2026-11-18)

The tenant-wide `GET /api/admin/usage/history?tenant=` is shown too (#4493): under the tenant admin's running-time grid, with a member column (and a tenant column for a super admin's deployment-wide view), in the same range as the grid. A `null` figure still reads "not measured".

## Addendum — the boosted hours are counted (2026-11-24)

#4417 wires the counter the decision 5 addendum left out, and the monthly budget is enforced.

- **What counts.** `launchBoostHours` sums the launch records (decision 3) whose `override` is `boost`, each
  clipped to the current UTC month. A launch runs from the Start request (the box's boot is included) to its
  stop, or to its last sighting while it is still open. It keeps `boost` until it stops, so the hours after
  expiry are in the count.
- **What does not.** A boost whose Start failed was closed at its own start. A launch the start deadline
  stopped is closed at its last sighting, so one that was never placed adds nothing for the minutes it waited.
  A launch the sampler adopted with no record of its start (`adopted`) is not counted: what chose its box is
  unknown.
- **One answer per Start.** The Start resolves the box once and takes both the runtime and the launch's
  `override` from that answer, so a boost ending mid-start cannot record an unboosted box as boosted.
- **A wrong inferred stop is taken back.** ecs-ec2 answers `none` when one read of the home volume fails, and
  the sampler can read `stopped` between a recorded launch and its Start. When the same workspace is seen
  running within an hour of such a stop (a launch closed at its own last sighting, with nothing open since),
  the launch is reopened rather than replaced by an `adopted` one.
- **Only with the usage sampler.** The sampler is what moves an open launch's last sighting. With
  `AF_USAGE_SAMPLE_INTERVAL=0` no counter is wired, and the budget reads as not counted, as before.
- At the budget a new boost and an extension are both refused (`boost_budget_spent`). Showing the overrun to the
  tenant admin and on the cost line is the next part of #4417.

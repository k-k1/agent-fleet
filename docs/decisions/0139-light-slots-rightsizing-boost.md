# 0139. Light slot classes, a usage history, right-sizing and a boost for `ecs-ec2` workspaces

English | [日本語](0139-light-slots-rightsizing-boost.ja.md)

- Status: **proposed** (2026-11-16). Documentation only: nothing below is built. Every code fact was read
  from the tree at `f21201685` on this date. **What is measured and what is not.** Prices and
  per-instance performance quoted from [70](../log/70-slot-instance-classes.md) §70.3 are that journal's
  Pricing API reads and benchmarks (ap-northeast-1, 2026-08-22). **No price for a `medium`, a `t4g` or a
  `t3` instance was read for this record**, and no AWS call was made: every such figure below is an
  estimate from list-price proportionality and is marked as one. No decision rests on an estimate alone;
  the ones that wait on a measurement say so.
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

- **A member's box is `rungFor(classID, memBytes)`** (`runtime_ecs_ec2_pool.go`): the smallest rung of
  the member's class whose declared memory is at least the member's memory requirement. **A requirement
  of 0 means the smallest rung of the class** (ADR 0045 decision 21).
- **The class and the requirement are per member** (`limits.go`: `SlotClass`; tenants carry
  `AllowedSlotClasses`), and **a change takes effect at the next container start**
  ([70](../log/70-slot-instance-classes.md) §70.4.3). Moving to another rung is therefore already just
  "the next Start takes a different slot and the home EBS is attached there": 22–27 s on a hot slot,
  ~50 s from a stopped one (ADR 0045 decision 8, measured).
- **The workspace is capped at the rung minus a flat host reserve**: `ec2HostReserveFloorMiB = 2048`,
  because the daemons were measured at ~1.2–1.4 GiB and a rung overstates MemTotal by ~5%
  (`workspaceMemCapMiB`). The reserve does not shrink with the box.
- **The shipped ladder starts at `large`** (`30-ingress.yaml` `Ec2SlotTypes`: `standard` = m7i,
  `saver` = m6i, both x86_64, 8/16/32 GiB).
- **The Agent already measures the workspace**: `resources.Read()` (CPU from `cpu.stat` `usage_usec`,
  memory) and `mempressure.Current()` (PSI `some`/`full` avg10/avg60, working set, refault rate) behind
  the Stats endpoint, which the CP passes through to the Console (`agent_client.go`). **The CP keeps no
  history.** The Agent's own `mempressure.Series` keeps about four days, on the workspace, for threshold
  fixing (ADR 0120 decision 8).
- **The root volume is per slot**, `SlotRootVolumeGiB` default 100 (~$9.6/month while the slot exists,
  [70](../log/70-slot-instance-classes.md) §70.3), and it is reclaimed only when
  `Ec2SlotTerminateAfterSec` is set (ADR 0045 decision 23; default 0 = never).

## Decision 1 — no co-tenancy. ADR 0045 decision 8 still holds, and the saving is taken with smaller exclusive slots

Packing light members onto one instance saves money only through **overcommit** — caps that add up to
more than the box. On-demand price is linear in vCPU and memory within a family (measured for m6g, m7g,
m8g, m6i and m7i in [70](../log/70-slot-instance-classes.md) §70.3), so eight 4 GiB caps on a 32 GiB box
cost exactly what eight 4 GiB boxes cost. And memory is the one resource a workspace cannot overcommit:
the incident behind ADR 0120 was **one member alone** driving an 8 GiB slot into a twenty-minute refault
thrash.

What co-tenancy would cost, read from the tree:

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

**Reopen only if** the history of decision 3 shows many members whose sustained working set is well
below the smallest exclusive box that exists. Even then the shape is a same-tenant shared slot inside
`ecs-ec2`, never a second runtime.

## Decision 2 — light classes are separate, operator-declared, opt-in classes. The shipped ladder does not change

The operator declares light classes in `AF_ECS_EC2_SLOT_TYPES` with the existing
`id|label|arch|type:memMiB[:vcpu]` syntax. **Nothing new is needed in the parser or the launch path.**

🔴 **A light rung never goes into an existing class.** Because a requirement of 0 resolves to the
smallest rung, adding `medium` under `standard` would silently move every member who never chose a size
onto a 4 GiB box with a ~1.8 GiB workspace. A light box is something a member or an administrator
chooses (or decision 4 proposes), never something a ladder edit does to them.

Candidates, to be measured before any is recommended in the guide:

| Class (proposal) | arch | Rungs | Why |
|---|---|---|---|
| `light-arm` | arm64 | m8g.medium (1 vCPU / 4 GiB), m8g.large | `medium` exists only in the Graviton m families; m8g is the fastest per dollar in §70.3.2 |
| `burst-arm` | arm64 | t4g.medium (2 / 4 GiB), t4g.large (2 / 8 GiB) | Burstable: a workspace is mostly idle (§70.3.3), and the baseline is what an idle box needs |
| `burst-x86` | x86_64 | t3.medium, t3.large | The only light option that keeps home on x86_64 (no arch move, §70.5) |
| `mem-arm` (option) | arm64 | r8g.medium (1 / 8 GiB) | Memory, not CPU, is what a light workspace runs out of |

- 🔴 **A 4 GiB rung leaves the workspace about 1.8 GiB** (4096 − 2048 reserve, less the ~5% MemTotal
  overstatement). Whether one agent session plus the Agent fits in that is **not measured**; that
  measurement decides whether the 4 GiB rungs are offered at all, or whether light starts at 8 GiB.
  Lowering the reserve on small boxes is not part of this decision: it was raised to 2 GiB because a
  box thrashed with less.
- **Burstable classes run in `unlimited` credit mode.** Neither the slot launch template
  (`40-ec2-pool.yaml`) nor the CP sets a `CreditSpecification` today, so a t-class gets the EC2 default,
  which is documented as `unlimited` for t4g and t3 (not confirmed on this deployment, open question 2). `standard` mode would throttle a build to the baseline exactly when
  the member is waiting on it; `unlimited` bills surplus credits instead, which shows up in the member's
  cost line (ADR 0048) and in the history (decision 3) as sustained CPU, which is decision 4's signal to
  move them off a burstable class.
- **The arm64 classes inherit everything [70](../log/70-slot-instance-classes.md) needed for m8g**
  (multi-arch image, arm64 AMI, a golden per arch, the one-time home re-install of §70.5). The x86 burst
  class needs none of that.
- **Fragmentation** ([70](../log/70-slot-instance-classes.md) §70.7): each class keeps its own spare
  slots. A light member's compute is roughly the same order as one retained root volume (estimated: a
  `medium` at half of m8g.large's measured $0.11594/h is ~$0.058/h, ~$9.3 for 160 h; a root is ~$9.6/month),
  so **light classes pay off only on a deployment that sets `Ec2SlotTerminateAfterSec`**. The guide says so
  next to the classes.
- **No shipped default** (the rule of ADR 0074 decision 1): the example ladder is a proposal until the
  prices and the 1.8 GiB question are measured on a deployment.

## Decision 3 — the CP keeps a per-member usage history

Right-sizing and the guide's advice both need "how much does this member actually use", and today nobody
can answer it after the fact.

- **What**: one row per member per day: active minutes, CPU p50/p95 (as a fraction of the rung's vCPU),
  working set p95/max, PSI `full` avg60 max and minutes above the ADR 0120 warning threshold, refault
  rate p95, the rung and class the minutes ran on, and the minutes on a burstable class above its
  baseline.
- **How**: the CP samples the Stats endpoint of running workspaces on its existing sweep (one call per
  running workspace per sweep; the Agent already computes every figure) and folds the samples into the
  day row. Raw samples are not stored.
- **Absent is not zero** (the rule of `memPressure` in `agent_client.go`): a sweep with no answer adds no
  minute, and an Agent that cannot measure leaves the column NULL.
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
- **Within one architecture.** An automatic move never crosses x86_64 ↔ arm64: that move re-installs
  part of home (§70.5) and is a member's or an administrator's choice. Across architectures the
  recommender only proposes.
- **Burstable sustained CPU moves a member off a burstable class** to the same-arch non-burstable class
  of equal memory.
- **The administrator's stored choice is not overwritten.** The result is an *override* stored beside it
  (`auto`, with the reason and the history window that produced it), and the box is resolved as override
  → stored choice → tenant default in **one place**, the same function every placement and every screen
  already goes through (`rungFor`). Clearing the override restores the stored choice. The bounds an
  override can never leave are the tenant's `AllowedSlotClasses` plus a new tenant minimum and maximum
  rung.
- **Applied at the next Start only.** A change never stops or restarts a running workspace; the member is
  told in advance ("from your next start: m8g.large, because …").
- **Phases**: 0 — history only (decision 3). 1 — proposals shown to the tenant admin and the member,
  applied by a click. 2 — automatic, per tenant, off by default, with a per-member opt-out.

## Decision 5 — a boost: the member asks for one rung up, for a limited time

- **What**: "Boost" on the member's workspace screen takes the next rung up in the member's own class
  (or a rung the tenant admin allowed) for N hours. It is a time-limited override of the same kind as
  decision 4's, resolved in the same place, so the two cannot disagree about which box a member gets.
- **When it applies**: at the next Start. If the workspace is running, the screen offers "restart into
  the bigger box now" and says that a restart ends the sessions running in it. It never restarts on its
  own.
- **Ending**: at expiry the override is removed and the next Start goes back. A running workspace is
  never shrunk under the member. Boosting while boosted extends; it does not climb further.
- **Bounds**: the tenant admin turns it on, and sets the highest rung and the boosted hours per member
  per month. Boosted hours show in the history (decision 3) and the cost line already shows the money.
- **Where it meets ADR 0120**: when the pressure responder warns or refuses a new session, its notice
  offers the boost instead of only "close a session".

## Consequences

- Nothing changes for a deployment that declares no light class and leaves the tenant switches off.
- The history adds one sweep call per running workspace and one row per member per day.
- `rungFor` gains the override in front of the stored choice; the Console's size display and the slots tab
  must show where an override came from (`auto` or `boost`) and until when, or a member cannot tell why
  their box changed.

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

## Open questions

1. Does one agent session plus the Agent fit in ~1.8 GiB (a 4 GiB rung)? Decides whether 4 GiB rungs are
   offered (decision 2).
2. Is `unlimited` what a t-class slot actually gets on this deployment with no `CreditSpecification`
   set? (decision 2)
3. The thresholds of decision 4, from phase 0 data.

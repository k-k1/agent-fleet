# 0075. An engine buys from the top of the offers that clear its VRAM — Spot and on-demand in one list

English | [日本語](0075-engine-purchase-offers.ja.md)

- Status: **proposed** (2026-09-11). Nothing is implemented.
- **Nothing was measured for this document.** Every number says where it comes from —
  (a) measurements in ADR 0070, 0071, 0072 and 0074, (b) facts read out of this repository's
  code and templates on 2026-09-11, (c) things known only as AWS's published specification and
  **not confirmed on this deployment** (the billing of an interrupted Spot hour, FIS's Spot
  interruption action, how `UpdateService` treats a capacity provider strategy). Nothing in (c)
  is load-bearing; what depends on it is listed under "Open questions", each naming the
  decision that waits on it.
- What the operator asked for is three rules and one exception — **(1) buy whatever clears the
  required VRAM, cheapest first, without distinguishing on-demand from Spot; (2) if Spot cannot
  be had, take on-demand; (3) a Spot box dying suddenly is acceptable, and rule 2 rebuilds it.
  The exception: the llm role is on-demand only, while the image and tts roles mix Spot in.**
- 🔴 **This is a draft.** The stage where an implementation corrects the text — ADR 0074's P0
  and P1 — has not happened yet. Every decision carries a line saying **what would make it
  change**. A decision without one should not be read as anything but written-before-measuring.
- Related: [0074-engine-instance-classes.md](0074-engine-instance-classes.md) decision 1 (the
  operator declares the ladder), decision 2 (a stored choice wins), decision 3 (nothing declared
  means ECS is never called), decision 4 (a change reaches the next box; wait for the old one to
  leave), decision 5 (describe → copy → update, re-applied just before a start), decision 6 (the
  VRAM warning), decision 7 (a badge for a non-default rung), decision 10, decision 11, and the
  three Spot appendices at its end /
  [0071-self-hosted-inference-engines.md](0071-self-hosted-inference-engines.md) decision 1 (GPUs
  are bought as ECS Managed Instances), decision 2 (one capacity provider per role), decision 5
  (wake and hold), decision 7 (`draining`) /
  [0070-tts-ondemand-engine.md](0070-tts-ondemand-engine.md) decision 1 (the placement is always
  spelled out) and `UseSpot` /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 21 (the numbers
  in a ladder are declared by the operator; neither EC2 nor the Pricing API is asked) /
  [0072-engine-model-catalog.md](0072-engine-model-catalog.md) decision 7 (the stack is a SEED) /
  [0048-member-cloud-cost.md](0048-member-cloud-cost.md) decision 15 (the cost-allocation tags)

## Background

### Four reasons none of the three rules can hold today

**1. The purchase type is fixed when a capacity provider is created and cannot move at runtime.**
`capacityOptionType` **does not exist** in the type `UpdateCapacityProvider` accepts
(`InstanceLaunchTemplateUpdate`) — confirmed while implementing ADR 0074 decision 5. Changing it
in place with CloudFormation ends in `UPDATE_FAILED`: `CloudFormation cannot update a stack when
a custom-named resource requires replacing.` (ADR 0074, "Spot への切り替えは CloudFormation が
拒む"). Changing the NAME along with it does go through as a replacement (0.19.0's
`ImageCapacityOptionType`), but that is **a CloudFormation update of 176 seconds out and 147
seconds back** (ADR 0074, "Spot への置き換えを live で流した") — not a way to answer "Spot was
not available, take on-demand" **at runtime**.

**2. ECS has no automatic Spot → on-demand fallback.** A `capacityProviderStrategy` is a
**weighting**, not an ordered fallback. If the task cannot be placed it simply is not placed,
and the reason appears in the service events. Measured: the image role was switched to Spot and
turned on for 17 minutes; ECS tried about every five minutes, four times, and answered
`UnfulfillableCapacity` every time (ADR 0074, "箱は取れなかった。しかも第 3 のエラーコード
だった"). **Nobody falls back for you.**

**3. A human picks the box, and the Control Plane does not know prices.** ADR 0045 decision 21
and ADR 0074 decision 1 settled that the numbers in a ladder are the operator's declaration and
that the CP asks neither EC2 nor the Pricing API. `usdPerHour` is **display-only**; nothing
computes with it (`engine_class.go`). And ADR 0074 **rejected** "the CP picks a rung to fit the
model" — because the bill goes up silently. There is no place in the current design for the
words "cheapest first" to live.

**4. An interruption is only visible as a failed start.** Both `draining` (ADR 0071 decision 7)
and the drain wait (ADR 0074 decision 4) watch a box **this side set to desired 0**. A Spot
interruption takes the box away with nobody having moved the desired count — all the controller
sees is `running` reverting to `starting` (desired 1, running 0), and nothing is said until
`StartDeadlineSec` (900 s by default) expires. **Neither the engine table nor the controller has
a word for an interruption.**

### What the code and the templates say (read 2026-09-11)

- **A row of the engine table holds exactly one capacity provider** (`engineDef.CapacityProvider`
  in `engines.go`). Both the `draining` test (`engine_ecs.go`) and the check for which container
  instance is this engine's box (`box()`) resolve through that single name.
- **The service names its provider** (`60-engines.yaml`'s `CapacityProviderStrategy`). Since
  0.19.0 that name is `!Ref ImageCapacityProvider` and therefore follows the resource
  (`PARAMETERS-60-engines.md`, "The capacity providers" — restating it as a `!Sub` string
  **fails silently when the name moves**).
- **The CP already has `UpdateService`** (starting and stopping move the desired count). Writing
  a strategy needs no new IAM.
- **A rung is applied as a four-field read-modify-write**, idempotently, again just before every
  start (ADR 0074 decision 5, `startGate` in `engine_class.go`). **That re-application targets
  the capacity provider only** and never the service — which is exactly why ADR 0074 rejected
  "declare one provider per rung and swap the service's strategy": **updating a service's
  strategy starts a new deployment and kills a generation in flight.**
- **The only part of an engine table row that can be carried into a running process is the
  ladder** (`engine_table_reload.go`). A change to service, url, health, provider, capacity
  provider, idle or deadline is merely logged as needing a restart. 🔴 **Capacity provider name
  being in that list became a real defect during the Spot switch** (ADR 0074, "provider の名前が
  変わると、走っている CP は箱を見失う" — the rung application went to the deleted old name and
  got a 400, `box` read `null`, and the panel still said "running on l4"). A fix taking that name
  live is in flight on another lane. The decisions here do **not** depend on it, but **once there
  are two providers per role this field had better be live** (see phase P0).
- **TTS is Fargate and its placement is written by CloudFormation** (`UseSpot` in `50-tts.yaml`:
  `on` drops `LaunchType` and writes a `FARGATE_SPOT` strategy). The CP only moves the desired
  count.

### Spot facts already measured (ADR 0074's appendices; only cited here)

| Fact | Source |
|---|---|
| `capacityOptionType` is create-only. An in-place CFN update fails; **changing the name with it works as a replacement** (176 s out, 147 s back) | 0074, "Spot への切り替えは CloudFormation が拒む", "Spot への置き換えを live で流した" |
| Three failure codes are measured: `UnfulfillableCapacity`, `InsufficientInstanceCapacity`, `VcpuLimitExceeded` | 0074 (the third) / 0071 (the first two) |
| Some accounts have no Spot service-linked role (`AWSServiceRoleForEC2Spot`) | 0074, "Spot は買えた。ただし変えたものは 2 つある" |
| `get-spot-placement-scores` **answers differently per type SET**: 1/10 for one type, 9/10 for three (g6+g5+g6e). A box arrived **42 seconds** after a 9 was recorded | same |
| Spot prices (same moment, ap-northeast-1): g6.xlarge $0.577/$0.563, g5.xlarge $0.744/$0.785, g6e.xlarge $1.355/$1.351. **A g6e on Spot costs more than a g6 on demand ($1.26 all-in)** | same |
| The Managed Instances management fee is **7.80%** of the on-demand list price (Cost Explorer, both g6.xlarge and g6e.xlarge) | 0074, "`usdPerHour` は Cost Explorer の確定値" |
| **A price being quoted is not evidence of stock** — through the 17 minutes nothing could be bought, the price history kept answering $0.563-0.577 | 0074, "箱は取れなかった" |
| The quotas are separate: on-demand `L-DB2E81BA`, Spot `L-3819A6DF` (default 0). acrt holds 64/64, af-sandbox 8/8 | 0074, "追試", "Spot は買えた" |
| `describe-instances` **cannot enumerate** an MI box but **returns it by id** (the only route to `InstanceLifecycle: spot`) | 0074, "Spot であることの証明は" |
| A replacement box starts with empty storage — re-fetching from S3 measured 6.62 GiB in 66 s (102.7 MiB/s) | 0071 |
| Cold starts: image 165-197 s, llm 527-586 s | 0071 |
| From `mode: on`, a Spot box was ACTIVE in **42 s**, running at 281 s, warm at 307 s, one image at 403 s | 0074, "実測（すべて `mode: on` の 06:01:16Z から）" |

## Decisions

### 1. The ladder becomes a list of OFFERS, gaining a purchase-type column. **The order tried stays the declared order**

ADR 0074's ladder (`<role>InstanceClasses`) gains an eighth field and is renamed `<role>Offers`.

```
id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour|buy
```

`buy` is `od` (the default, omittable) or `spot`. **Because it is only an added field, an
existing ladder reads unchanged as "a list of offers that are all on-demand"** (see the
migration section).

🔴 **The CP does not sort by price. The declared order is the order tried.** "Write them
cheapest first" is the operator's convention; the panel **shows the declared prices side by
side** and says nothing about the order. Three reasons.

- **A row with no price would become a row that can never be bought.** ADR 0074's convention is
  "declare what was billed, or nothing", and a Spot price is **by definition always "not yet"**
  at the start (the Spot box bought in 0074 ran for 403 seconds). Making price the sort key
  sends the row most worth trying to the end of the list.
- **Two declarations by the same person would compete.** Both the order and the price are
  written by the operator. Sorting by price lets one number silently overrule an order the same
  person put there deliberately (say, a widened Spot row first and on-demand after it).
- 🔴 **One row spans a price RANGE.** Managed Instances has no allocation strategy: **the
  cheapest thing that matches is bought** (measured, ADR 0071). A Spot row widened to three types
  costs $0.58 when g6.xlarge is available and $1.36 when it falls to g6e.xlarge — and **the
  latter is more than a g6 on demand ($1.26 all-in)** (measured, ADR 0074). The row can only
  carry one declared price, while the real one is a range.

So the way the price is written is also decided — **`usdPerHour` states the all-in price of the
MOST EXPENSIVE type the offer can buy.** Shading it downward makes the number useless for the
comparison it exists for. "All-in" includes the Managed Instances management fee, taken the way
ADR 0074's "`usdPerHour` は Cost Explorer の確定値" takes it (never copy a list price; measured,
it is 7.80% off).

🔁 **What would change this**: an actual incident where an operator got the order wrong. Then the
CP sorts, and the question "where do the priceless rows go" has to be answered at the same time.

### 2. The required VRAM narrows the offers. The narrowed list is tried from the top

The first half of rule 1 is a **filter**. The candidates are the offers with
**`vramMiB` ≥ the required VRAM**, where the requirement is ADR 0074 decision 6's answer — the
**maximum** over the enabled models (never the sum), including the KV cache for the llm role
(0074's appendix: it is computed from the GGUF header and matched the hardware at 0.00% on both
points).

- **When the requirement is `unknown`, nothing is filtered.** The list is tried from the top.
  ADR 0074 decision 6's "never draw unknown as fits" is a decision **about the warning**, not
  about refusing to buy a box — refusing would leave a deployment holding one unmeasured model
  with an engine that can never start. The warning still reaches the panel and the log.
- **When no candidate remains, nothing starts.** Rather than falling back to the default offer,
  it says "no offer can hold this model". ADR 0074 decision 6 refuses to block, but that is about
  "this may not fit"; "not one declared offer meets the floor" is a settled contradiction.

🔁 **What would change this**: if the KV-inclusive estimate reaches `declared` accuracy for the
image role too (the rest of 0074's open question 7), the unknown case could move from "start at
the top" to "do not filter, warn".

### 3. Two capacity providers per role. One service, as before

CloudFormation creates **both** `af-<stack>-<role>` (on-demand, keeping the historic name) and
`af-<stack>-<role>-spot`. Since the purchase type only goes in at creation, the only way to
choose at runtime is to have both already there. The cluster's `Associations` names both
(⚠️ that list **replaces** rather than adds, so `FARGATE` and `FARGATE_SPOT` must be named
alongside — `PARAMETERS-60-engines.md`, "The capacity providers").

This **overrides ADR 0071 decision 2** ("one capacity provider per role"). Only the count is
overridden: **the substance — two roles never share an instance — is untouched.** The two are
two wallets belonging to the same role, and only one of them ever appears in the service's
strategy.

- **One service, still.** Splitting the service was rejected (below).
- **An engine table row carries two provider names** (the successor of `capacityProvider`). The
  `draining` test and `box()` must **accept either name as this engine's own** — watching only
  one makes a box bought through the other read as `box: null`, which is exactly what happened
  during the rename in 0074.
- **IAM widens to both.** Same shape as ADR 0074 decision 9 (scoped to this stack's resources):
  the second ARN joins the Resource list for `ecs:DescribeCapacityProviders` and
  `ecs:UpdateCapacityProvider`. `ecs:PutClusterCapacityProviders` (cluster-scoped) and
  `iam:PassRole` do not grow.

🔁 **What would change this**: if creating the second provider costs anything or has a side
effect — measured in 0074, **creating one buys nothing** — go back to creating only the declared
purchase types.

### 4. The CP writes the service's `capacityProviderStrategy` **only while running is 0**

This is the whole of the safety argument. ADR 0074 rejected "one provider per rung, swap the
service's strategy" because **the idempotent re-application before every start becomes "kill the
generation in flight"** on a service. That reason fails to apply in exactly one condition:
**when there is no task to replace.**

- The CP writes the strategy on **(a) the start that takes desired 0 → 1** and **(b) the move to
  the next offer after that start failed** (desired still 1, running still 0). Both hand the
  strategy and the desired count to the same `UpdateService` call.
- 🔴 **From the moment running reaches 1 until that engine stops, the strategy does not move.**
  The rung re-application (ADR 0074 decision 5) still flows **to the capacity provider only**.
  The distinction is explicit, and in the implementation it takes the shape of a single function
  that touches the service, with a `running == 0` guard inside it.
- ⚠️ (b) **does start a new deployment.** It is allowed to, because the only task it kills is one
  PENDING task. A side effect: the PRIMARY deployment's `updatedAt` moves, so the
  `StartDeadlineSec` clock switches to the next offer by itself (`describe()` in `engine_ecs.go`
  reads `updatedAt` — written for ADR 0070 decision 6 and useful here for free).

⚠️ **Not verified**: whether an `UpdateService` that changes only the strategy goes through
without `forceNewDeployment`, and whether updating the strategy of a desired-0 service really is
harmless, **have not been measured on this deployment** (open question 1; the first thing P0
measures on hardware).

🔁 **What would change this**: if a strategy update breaks something even at running 0, this
ADR's skeleton is gone. The way back is the rejected "one service per purchase type", which
first has to solve keeping one Cloud Map name.

### 5. Rule 2 — if no box arrives inside the budget, move to the next offer. **The failure code decides how to wait**

The per-offer budget `<role>OfferBudgetSec` (default **180 s**) measures **from setting desired
to 1 until a container instance is ACTIVE**. Once the box is there this clock stops and the
ordinary cold start (measured in 0071 and 0074) takes over. 180 s is a bit over four times the
**42 seconds** the successful case measured (0074) — a multiple of a measurement, not an AWS
specification.

Some cases skip ahead without waiting, because the service event's code says whether waiting can
help — and **all three codes are measured** (see "Failure codes and what they mean").

- **One lap around the list and it gives up**, entering the controller's existing cooldown
  (doubling per failure, four doublings at most) and starting from the top on the next demand.
  That is the only gate against buying forever.
- **One audit line per move** (`engine.<role>.offer`). "Why are we running on the expensive box"
  has to be answerable afterwards.

🔁 **What would change this**: a measured case of `InsufficientInstanceCapacity` recovering after
more than 180 s. Then the budget becomes per-code, lengthened only for codes worth waiting on
(stock is a function of the clock).

### 6. Rule 3 — an interruption is "**there is demand and the box is gone**". Start again from the top of the list

Detection is **the box disappearing**: desired is still 1, running has gone to 0, and no
container instance is registered — with the CP not having moved the desired count. That is
enough for rule 3, because **the list is restarted from the top** whichever way the box was
lost: a Spot interruption and any other AWS-side loss want the same answer.

- 🔴 **`stoppedReason` is read to EXPLAIN, not to conclude.** The CP does not call `DescribeTasks`
  today (only `DescribeServices` and the two container-instance calls). Saying "this was an
  interruption" out loud means adding it, so **whether it earns its keep is decided after P1
  measures** (open question 2). Detection works without it.
- **An interruption is not counted as a failure.** The controller's `failures` (which doubles the
  cooldown) counts starts that failed, not things that were taken away while running. Counting
  interruptions there makes **starts get slower the more Spot is used**.
- ⚠️ But **an offer interrupted twice in a row is skipped for the rest of that demand.** Buying
  a type that keeps being taken away, forever, is this design's most expensive failure shape.

🔁 **What would change this**: if interruptions turn out to be rare (a few a month), the skip is
not needed and only "from the top" remains.

### 7. Read `get-spot-placement-scores` once before trying a Spot offer

**This is the one stated exception to ADR 0045 decision 21 (the CP asks neither EC2 nor the
Pricing API).** Four limits hold it in place.

- **Only for a role that has a Spot offer**, **once per start**, **read-only**.
- **The answer is used for one thing: whether to skip that offer.** Never for a price, a VRAM
  figure or a type choice — those numbers stay the operator's declaration.
- 🔴 **Ask with the provider's own type set.** The 1/10 measured for one type says nothing about
  a provider that buys from three (measured, 0074: 1/10 → 9/10).
- **The threshold is declared by the operator** (`<role>SpotScoreMin`, default **0 = do not
  ask**). There are two data points (a box 42 s after a 9; nothing in 17 minutes at 1), which is
  **not enough to turn it on by default**. With 0 as the default, decision 21's exception only
  ever happens on a deployment that asked for it (the shape of ADR 0074 decision 3).

⚠️ **This widens the CP's IAM with a `Resource: *` action.** `ec2:GetSpotPlacementScores` cannot
be scoped to a resource. That it is read-only, returns none of the account's resources, and is
called only when a role has a Spot offer and a declared threshold, is the whole of the boundary.

🔁 **What would change this**: more than two data points relating a score to actually buying,
with a low score that bought anyway. Then the gate goes and only rule 2's budget remains.

### 8. An administrator's choice becomes a **pin**. Nothing chosen is automatic

ADR 0074 decision 2's setting (`engine_<role>_class`) survives; its meaning shifts by one step.

| Stored value | Behaviour |
|---|---|
| empty (nothing chosen) | **automatic**: filter per decision 2, try in the declared order of decision 1 |
| an offer id | **pinned**: that offer only, and **it never falls through to the next** |

- **Existing deployments migrate by themselves.** An administrator who picked a rung said "run it
  on this one", which is what a pin is. A deployment with nothing chosen becomes automatic.
- **A pin that cannot be filled shows the service events as they are** (the same stance as ADR
  0074 decision 1's last bullet). It does not fall through because a pin is an explicit human
  intention — dropping somebody who said "try the 48 GB card" back to 24 GB silently has the
  shape ADR 0074 decision 4 called the most expensive lie.
- **ADR 0074 decision 7's badge still works**, with the wording moving from "not the default
  rung" to "pinned rather than automatic / back to automatic".

🔁 **What would change this**: an operational incident where a pin could not be filled. Then a pin
becomes a STARTING POINT (never falls below it, may rise above it).

### 9. The llm role gets **no** Spot capacity provider

Rule 4's exception (llm is on-demand only) is held **twice over**.

- **By declaration**: no `spot` row in `LlmOffers`. The CP buys only what the list declares.
- **By template**: `60-engines.yaml` **does not create** a Spot provider for the llm role — the
  same stance 0.19.0 took by adding `ImageCapacityOptionType` and not its llm twin.

The reason is already written in 0074 and `PARAMETERS-60-engines.md`: **Spot's two-minute notice
arrives mid-conversation, and a 527-586-second cold start is what follows it.** An image request
is one call that can be made again; a conversation is not.

🔁 **What would change this**: a design that can hold a second warm box across a conversation
(today it is one box per role), at which point the exception loses its meaning.

### 10. TTS creates no provider; the same rules drive `FARGATE_SPOT` ↔ `FARGATE`

TTS is Fargate, so there is no box to buy. Its offer rows carry **no types — only a purchase type
and a price**. The rules are the same, decision 4 (write the strategy only while running is 0)
included.

- **Polly carries the wait** (ADR 0070 decisions 5 and 16). So rule 2's budget may be shorter
  here: waiting does not produce silence.
- ⚠️ **ADR 0070 decision 1's invariant does not break.** What the CP writes is always an
  **explicit** strategy; what is forbidden is writing neither a `LaunchType` nor a strategy.
- ⚠️ `UseSpot` stays but changes meaning, from "use Spot" to "**put the Spot offer in the list**".
  Measured: flipping `UseSpot` on production was an **in-place** update (about 6 minutes, no
  replacement), though the new deployment came up at desired 1 (the contract of not declaring
  `DesiredCount`). The CP's route only moves the strategy from desired 0, so that side effect
  **should** not appear (open question 3).

🔁 **What would change this**: if Fargate Spot interruptions are frequent in a real deployment,
TTS may be cheaper on plain on-demand than being rebuilt while Polly reads. Settle it by adding
that arithmetic to ADR 0070's break-even (stand the engine up once reading is cheaper than Polly).

### 11. The panel says what it is running on **from the CP's own choice**. EC2 is not asked

The CP knows which offer it passed to `UpdateService`, so saying "running on the Spot l4 offer"
needs no call to EC2.

- 🔴 **Proving it really was Spot needs `describe-instances` by id** (measured, 0074: an MI box
  cannot be enumerated but is returned by id, and `InstanceLifecycle: spot` exists nowhere else).
  **`ec2:DescribeInstances` is not added for that** — decision 21's spirit, and an operator can
  run it by hand (the steps are in `PARAMETERS-60-engines.md`).
- Three things reach the panel: **the current offer** (and its purchase type), **the order tried
  and what each answered** (how many times rule 2 fell through), and **the interruption count**.
  Same stance as ADR 0074 decision 4's "show the three stages as they are".

🔁 **What would change this**: a case on real hardware where "the offer the CP chose" and "the box
actually bought" disagree (as the rename did in 0074). Then one more reconciliation route is owed.

### 12. State explicitly that the pre-start re-application (ADR 0074 decision 5) is for the capacity provider only

ADR 0074 decision 5 applies the rung "when it is saved and again immediately before a start".
Now that this ADR also writes to the service, **the two have to be written down as different
things.**

| Target | When it is written | What happens when it is |
|---|---|---|
| capacity provider (four fields) | on save and **before every start** (idempotent) | nothing moves; only the spec of the next box changes |
| the service's strategy | **only while running is 0** (decision 4) | **a new deployment starts**; any running task is replaced |

CloudFormation putting its declaration back (ADR 0074's open question 2) is absorbed through both
routes — the provider's four fields by the pre-start re-application, the service's strategy by
the start itself. ⚠️ **The remaining window has the same shape**: between CloudFormation reverting
and the next start, the deployment sits at something other than what is declared.

## Rejected alternatives

- **One service per purchase type (`<role>-od` / `<role>-spot`).** The strategy would never be
  written at runtime and decision 4's whole risk would vanish. Rejected for three reasons:
  (a) **an engine has one DNS name** (Cloud Map). Two services registered under it split the
  requests, reproducing exactly what `MinimumHealthyPercent: 0` avoids in
  `PARAMETERS-60-engines.md` — "two tasks behind it split requests between a warm engine and a
  cold one; the caller sees a random 500-second first token". (b) **`engineRuntimeState` is one
  per role**, and the demand window, the controller's goroutine, the warmth and the last-applied
  rung all live in it. Two of them means the CP holds "which one am I now", which is heavier
  state than the one line this ADR writes to a service. (c) The engine table is **one row per
  role** (`engineDef.Service`), a shape four ADRs since 0071 stand on.
- **Move to EC2 Fleet / an Auto Scaling group.** `allocationStrategy` (`capacity-optimized` /
  `lowest-price`) and mixing on-demand with Spot **exist there as AWS features**, which would
  make most of this ADR unnecessary. Rejected because everything that made ADR 0071 decision 1
  choose Managed Instances comes back: owning an AMI and the NVIDIA driver, paying for EBS while
  stopped, and staying outside ADR 0045's slot sweep (`af-role=slot`) and `Ec2MaxSlots`. What
  made ADR 0045 decision 6 reject MI for Workspaces (ECS owns the lifecycle and there is no stop)
  is **an advantage for an engine**. ⚠️ **This is a genuine alternative.** If the list grows past
  five rows, or rule 2's implementation turns into "AWS retries and the CP retries on top of it",
  **revisit this rejection.**
- **Fetch prices from the Pricing API or `describe-spot-price-history` every time.** It overrules
  ADR 0045 decision 21 and ADR 0074 decision 1 together and adds IAM. Before any of that,
  🔴 **the measurement says a price is not evidence of stock** — through the 17 minutes nothing
  could be bought, the price history kept answering $0.563-0.577 (0074). **Fetching more often
  does not improve the ordering**; what improves it is the placement score (decision 7). And the
  Managed Instances fee (measured at 7.80%) is not in the Pricing API, so ordering by its
  numbers can produce **an order the bill disagrees with**.
- **Choose a box per request** (a cheap box for this generation, a big one for this conversation).
  Rejected: there is one box per role, and a cold start is 165-197 s for image and 527-586 s for
  llm. Choosing per request means several boxes at once, which leaves both the G-family quota
  (8-64 vCPU) and ADR 0071 decision 2 (two roles never share a box). Kept as open question 4.
- **Act on Spot's two-minute notice and raise an on-demand box before the interruption.** The
  notice is delivered to the instance (IMDS) or to EventBridge, and both need new wiring outside
  the CP. And **two minutes is not enough** — the image cold start is 165-197 s plus the model
  sync, measured, i.e. longer than the notice. Rule 3 (rebuild when it dies) is cheaper.
- **Let the CP sort the offers by price.** The reasons are in decision 1 (a priceless row becomes
  unbuyable; two declarations by the same person compete; one row spans a price range).

## Which existing decisions this overrides, and how the appendices go on

**An ADR is append-only and immutable** (`docs/CONVENTIONS.md` §5). So not one character of an
existing decision is edited. Each affected ADR gains one section at its end — "**Appendix — ADR
0075 overrode this decision (2026-09-11)**" — saying what changed and linking here, and nothing
else.

| ADR / decision | What changes | What does not |
|---|---|---|
| 0071 decision 2, "one capacity provider per role" | **two per role** (one per purchase type) | the substance: two roles never share an instance |
| 0074 decision 1, "`usdPerHour` is optional and display-only" | **how it is written becomes a convention** (the all-in price of the most expensive type the offer can buy); it is still not a sort key | the CP asks neither EC2 nor the Pricing API |
| 0074 decisions 2 and 7, "the choice names one rung" | the choice becomes a **pin**, and **nothing chosen means automatic** (decision 8) | a stored choice beating the stack's declaration |
| 0074 decision 5, "re-applied idempotently before a start" | stated as **the capacity provider only** (decision 12) | the re-application itself and its failure rule |
| 0045 decision 21, "the CP asks neither EC2 nor Pricing" | **one exception** (`GetSpotPlacementScores`, one read, only for a role with a declared threshold) | the numbers in a ladder being the operator's declaration |
| 0070's `UseSpot` | its meaning moves from "use Spot" to "**put it in the list**" | decision 1 (the placement is always spelled out) is **untouched** |

**What does NOT move is written down too**, so nobody goes looking.

- **ADR 0074 decision 3 ("nothing declared, nothing called") is inherited.** A deployment with no
  offers declared chooses no purchase type either, never touches a strategy, and behaves
  identically down to the bit.
- **ADR 0074 decision 4 (a change reaches the next box; stop → wait for it to leave → start)
  stands.** ⚠️ **The drain wait is needed across purchase types as well** — the silent re-landing
  on the old card has nothing to do with how the box was bought (measured, 0074 P1).
- **ADR 0074 decision 6 (the VRAM warning), decision 10 (`engine_hourly` untouched) and decision
  11 (the task definition does not follow) all stand.**

## The offer format, and migrating from the existing ladder

```
id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour|buy
```

- Separated by `;` or a newline (as ADR 0074's ladder is).
- `buy` is `od` | `spot`. **Absent and empty both mean `od`.**
- **A row of seven fields reads as an on-demand offer unchanged** — i.e. an existing
  `<role>InstanceClasses` **moves to `<role>Offers` without a character changing.**
- During the migration the template reads `<role>InstanceClasses` when `<role>Offers` is empty
  (the shape of the gate ADR 0072 P6 put in front of `<role>ModelS3Key` — **so that a role never
  disappears silently**).
- The stored choice (`engine_<role>_class`) resolves by id, so **nothing happens at migration as
  long as the ids stay.**

An example following what the dev deployment measured (0074's appendix widened Spot to three
types, with on-demand underneath):

```
ImageOffers=spot3|22GB+ Spot (g6/g5/g6e)|22000|g6.xlarge,g5.xlarge,g6e.xlarge|4-8|15000-65536|1.57|spot;
            l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26|od;
            l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91|od
LlmOffers=l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26|od;
          l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91|od
```

🔴 **`spot3`'s declared 1.57 is the most expensive type it can buy** (g6e.xlarge's Spot $1.355
plus the 7.80% fee, approximately). If g6.xlarge is what actually arrives it is about $0.67, and
**that gap is what the row is worth.** ⚠️ **1.57 is arithmetic, not a bill** — the Spot box in
0074 ran for 403 seconds, so there is no Cost Explorer figure yet. **Rewrite it to the billed
number once one exists** (ADR 0074's "`usdPerHour` は Cost Explorer の確定値" convention). And
⚠️ **whether the management fee applies at the same rate to Spot hours has not been confirmed on
this deployment** (open question 5).

## Failure codes and what they mean

The decision is made on the string in the service events. **All three codes are measured.**

| Code | What it points at | Response | Source |
|---|---|---|---|
| `UnfulfillableCapacity` | "your **request configuration**". It says neither "try another AZ" nor "try later". Measured: the same answer four times over 17 minutes | **Move to the next offer without waiting out the budget.** It is not shaped like something waiting fixes | 0074, "箱は取れなかった" |
| `InsufficientInstanceCapacity` | that type, in that AZ, has no stock **right now**. A function of the clock | **Wait out the rest of the budget, then move on** | 0071, "在庫切れでインスタンスが建たない" |
| `VcpuLimitExceeded` | a quota — and **the quotas are per purchase type** (`L-DB2E81BA` / `L-3819A6DF`) | **Skip the offers of that purchase type and go to one of the other.** Do not spend the budget | 0074 P1 / 0071 |
| (no event) | nothing has happened yet | **Wait out the budget** | — |

🔴 **`VcpuLimitExceeded` is where this table earns the most.** Separate quotas mean that while the
on-demand pool is held by a draining box, the Spot pool is free. ⚠️ **The drain wait (ADR 0074
decision 4) is still required** — it is not about the pool but about **landing silently back on
the old card**.

⚠️ **The price of matching on strings.** If AWS rewords a message this table stops working
silently. **The default for a code that matches nothing is "wait out the budget"**, and the fact
that nothing matched is logged — the only handle the next reader will have.

## The time budget, and the worst wait

With `<role>OfferBudgetSec` at 180 s and a three-row list for the image role:

| Case | Time |
|---|---|
| Spot is simply available (reproducing the measurement) | ACTIVE in 42 s, one image at 403 s (0074) |
| Spot answers `UnfulfillableCapacity` (no wait) → on-demand is taken | **seconds** plus the on-demand start (165-197 s cold start plus the model sync) |
| Spot is silent (no event) → the 180 s budget → on-demand | **180 s** plus the start |
| All three rows spend their budget (worst case) | **540 s** plus the start, then cooldown |

🔴 **`StartDeadlineSec` (900 s by default) does not cover that total** — decision 4's side effect
(moving to the next offer moves the PRIMARY deployment's `updatedAt`) switches the clock, so it
is **900 seconds PER OFFER**. That is intended, but it means **nothing bounds "waiting for a
start" as a whole.** Two things bound it instead:

- **One lap around the list, then cooldown** (decision 5).
- **The gateway's `AF_ENGINE_WAKE_TIMEOUT` (900 s) is not changed** (ADR 0071 decision 5). What
  bounds the caller's wait is that, not the sum of the CP's retries. ⚠️ Which means **on a
  deployment where rule 2 falls to the third row, the first request has almost certainly given up
  already**, and the next one meets a warm box. That is the shape ADR 0071 decision 5 already
  chose (`engine_waking` is a code clients retry).

## What an interruption really costs

| Item | Value | Source |
|---|---|---|
| The request in flight | **Lost.** The image role has nothing like Polly to read in its place | ADR 0071 decision 5, contrasted with ADR 0070 decision 5 |
| Re-fetching the models | Local storage, so from scratch. Measured 6.62 GiB in 66 s (102.7 MiB/s). **About 5 minutes for a comfy deployment holding 30 GB** — that 5 minutes is **computed** from the measured rate; the 30 GB sync itself has not been measured | ADR 0071 |
| Cold start | image 165-197 s / llm 527-586 s | ADR 0071 |
| Billing for the interrupted hour | ⚠️ **(c) Understood from AWS's published specification to be uncharged when AWS interrupts, and not confirmed on this deployment.** Nothing here rests on it | — |
| The notice | Two minutes. **Shorter than the image cold start**, so it cannot be used to get ahead (rejected alternative) | (c) plus ADR 0071 |

**So one interruption costs one request plus five to eight minutes of wall clock.** Against that,
one row of the list saves about **$1.26 → $0.67 all-in, roughly $0.59 an hour** on a g6.xlarge.
⚠️ **But acrt's GPU spend over 30 days was $0.58 under `APN1-BoxUsage:g6.xlarge`** (measured
around 0074). **The absolute saving is small.** What this ADR is worth is less the money than
"when there is no stock, an on-demand box comes up by itself" — i.e. **the class of incident
where an engine simply does not start disappears.** Read it in that order.

## Why separate quotas help

- On-demand `L-DB2E81BA` and Spot `L-3819A6DF` are **separate pools** (acrt 64/64, af-sandbox
  8/8; measured, 0074).
- So **rule 2's fallback is real in quota terms too**: while the on-demand pool is held by a
  draining box (measured in 0074 P1: `VcpuLimitExceeded` persisted for more than five minutes
  after ECS deregistered the instance), the Spot pool is free. And the reverse.
- ⚠️ **The Spot pool defaults to 0.** A deployment that declares a `spot` row must check
  `L-3819A6DF` — the provider is created happily at 0 and then **simply never buys** (a
  by-product of 0074's "追試"). `PARAMETERS-60-engines.md`, "The G-family quota", **gains a Spot
  line** (it currently recommends 16 on-demand vCPU and nothing else).

## Open questions (decided after measuring)

1. 🔴 **Whether an `UpdateService` changing only the strategy goes through without
   `forceNewDeployment`**, and whether updating the strategy of a desired-0 / running-0 service
   really is harmless. **Not measured on this deployment.** Depends on: decisions 4 and 5 — the
   skeleton of this ADR. **The first thing P0 measures on hardware.**
2. Whether adding `DescribeTasks` (and one IAM action) to conclude that a loss was an interruption
   earns its keep. Detection works on the box disappearing alone (decision 6); the addition would
   be for the explanation. Depends on: decisions 6 and 11.
3. Whether the CP moving `FARGATE_SPOT` ↔ `FARGATE` collides with the contract of not declaring
   `DesiredCount` (`50-tts.yaml`). Flipping `UseSpot` measured as an in-place update (about six
   minutes), but that is the CloudFormation route. Depends on: decision 10.
4. **Choosing a box per request** (rejected above). Revisit if "one box per role" ever moves.
5. **Whether the Managed Instances fee applies to Spot hours at the same 7.80%.** 0074's
   measurement came from on-demand hours. Depends on: decision 1's pricing convention. **Only
   Cost Explorer's next-day figure can fill it.**
6. **The placement score threshold.** Two points (9 → a box in 42 s; 1 → nothing in 17 minutes).
   Depends on: decision 7. The default of 0 (do not ask) does not move while this is open.
7. **Whether to use Spot Advisor's interruption frequency in the ordering.** The placement score
   says "can it be bought now"; Advisor says "how often is it taken away". The latter is a
   published dataset, and putting it in the CP would be decision 21's **second** exception.
   **Its value cannot be computed until rule 3's real cost (the section above) is measured.**
8. **The relationship with `--models-max`.** ADR 0074 decision 11 settled that the task
   definition does not follow the rung, so `LlmModelsMax` stays 1 however big the offer is. Once
   offers are chosen by VRAM, "a bigger box should hold two" becomes the natural expectation — but
   that lives where ADR 0074's open question 6 lives (the task definition and CloudFormation).
   **This ADR does not move it.**
9. **How often interruptions actually happen.** The one Spot box 0074 bought ran for 403 seconds.
   Whether decision 6's "skip an offer interrupted twice" is needed cannot be known until this is
   filled.

## Phases and what "done" means

### P0 — the image role only: the offer list, two providers, the choice at start, the next offer

Scope: decisions 1, 2, 3, 4, 5, 8, 9, 11, 12. **Interruptions (decision 6) and the placement
score (decision 7) are not in it.**

**Done means**:

1. **A test can say that a deployment declaring no offers makes no additional ECS call**
   (inheriting ADR 0074 decision 3), with a positive control — declare one row and the call
   appears.
2. A test can say an existing `<role>InstanceClasses` string reads **unchanged** as an offer list
   and that every row is `od`.
3. A test can say offers below the required VRAM leave the candidate set, and that an empty
   candidate set does not start.
4. **A test can say no route touches the service while running is 1 or more** (decision 4).
   ⚠️ This is a negative claim, so **take a positive control**: removing the guard makes the test
   fail.
5. A test can say the three service-event codes separate into move-on, wait, and skip-this-
   purchase-type.

**On real hardware (af-sandbox, the image role)**:

| # | What to measure | Verdict |
|---|---|---|
| 1 | 🔴 **Open question 1**: an `UpdateService` carrying only a strategy, at desired 0 | Does it answer 200, or demand `forceNewDeployment`? **If this is red the ADR is rewritten from the skeleton** |
| 2 | Both providers exist and both are in the cluster's list | Two in `describe-clusters`'s `capacityProviders` (creating one is enough, as 0074 measured) |
| 3 | Desired 0 → 1 picks the Spot offer and the box arrives as **`InstanceLifecycle: spot`** | `ec2InstanceId` from `ecs describe-container-instances` → `describe-instances --instance-ids` |
| 4 | Rule 2's fallback | Put a deliberately unbuyable type in the Spot offer (out of stock, or a misspelling) and watch the on-demand box arrive after the budget. **This is P0's positive control** |
| 5 | `box()` accepts a box from **either** provider | `box` in `GET /api/admin/engines` is not `null` (during the rename in 0074 it was) |
| 6 | The rung application lands on **the chosen offer's** provider | Describe both before and after a switch: **not one field moves on the untouched one** |
| 7 | The panel | It names the current offer, its purchase type, and the order tried |

⚠️ **Cost estimate**: one GPU for one to two hours, **$1-3** ($0.67/h if Spot is available,
$1.26/h if not). When the measuring is done set `mode` to off and **watch until the container
instance is gone** ("stopped" and "gone" are different — 0074 P1).

⚠️ **To clear before P0**: this ADR makes it two providers, so **carrying a capacity provider
name live** is the safer ground (today it is only logged as needing a restart, and in 0074 that
gap meant the rung application hitting the old name with a 400, `box` at `null`, and a panel
that lied). That fix is in flight on another lane. **If it is not in, put one line in P0's
deployment steps: `force-new-deployment` the Control Plane** (measured at 217 s, blue/green, so
no outage).

### P1 — detecting an interruption and rebuilding, plus the placement score

Scope: decisions 6 and 7.

**Done means**:

1. A test can say desired 1 / running 0 / no box is treated as an interruption and **does not
   double the cooldown** (decision 6).
2. A test can say an offer interrupted twice in a row is skipped for the rest of that demand.
3. A test can say a deployment with no `<role>SpotScoreMin` declared **never calls**
   `GetSpotPlacementScores` (decision 7's limits).
4. A test can say the type set the score is asked with **equals the offer's own type set**
   (measured in 0074: an answer for one type says nothing about a three-type provider).

**On real hardware — how to cause an interruption**:

- **The intended route is AWS FIS's Spot interruption injection**
  (`aws:ec2:send-spot-instance-interruptions`), which delivers the two-minute notice and then
  interrupts, so rule 3 can be pushed **in its real, notice-carrying shape**.
  🔴 **Not verified**: whether an MI box can be an FIS target has not been measured.
  `describe-instances` **cannot enumerate** MI boxes (it returns them by id), so **whether FIS,
  which resolves targets by tag or filter, can find this box is unknown.** Naming the id directly
  should work, but that is not confirmed either. **Spend P1's first ten minutes here.**
- **The fallback positive control (if FIS cannot be used)**: `ec2 terminate-instances` **by id**,
  the id coming from `ec2InstanceId` in `ecs describe-container-instances` (measured, 0074). It
  is not an interruption, but **"there is demand and the box is gone" has the same shape**, which
  is what decision 6 detects. ⚠️ It cannot measure the notice-carrying route (what lands in
  `stoppedReason`) — in that case open question 2 moves to P2.
- **Measure one interruption's real cost**: interruption → new box → models re-fetched → warm, in
  seconds. The five minutes in "What an interruption really costs" is **computed**, so this is its
  first measurement.

### P2 — TTS

Scope: decision 10. **Start it only after rules 1-3 are through on hardware for the image role.**
TTS is not urgent because Polly carries the wait (ADR 0070 decision 16).

**Done means**: `UseSpot` means "put it in the list", the CP picks `FARGATE_SPOT` from desired 0,
and stands on `FARGATE` when it cannot be had. On hardware, **cause an interruption with FIS and
confirm Polly is reading through it** — i.e. that ADR 0070 decision 16's "preparing; Polly is
reading for now" is also correct during an interruption.

### On who drives the hardware runs

The hardware runs are driven from a **`claude` session**. Running the same steps through another
execution method has produced fabricated results here before (an operational fact about this
deployment, not about AWS). Also **one lane, one deployment**: the dev deployment is shared with
other sessions, so before touching `60-engines`, confirm nobody else is deploying.

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
  spelled out), decision 4 (waiting never means silence — Polly reads), decision 6 (the start
  clock is the PRIMARY deployment's `updatedAt`) and `UseSpot` /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 21 (the vCPU in a
  ladder is declared by the operator; `DescribeInstanceTypes` is not added to ask EC2 — "nor the
  Pricing API" is ADR 0074 decision 1's wording) /
  [0072-engine-model-catalog.md](0072-engine-model-catalog.md) decision 7 (the engine table stays
  static) /
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

**4. An interruption is visible as "a replacement nobody asked for", but nobody moves to the
next offer.** Both `draining` (ADR 0071 decision 7) and the drain wait (ADR 0074 decision 4)
watch a box **this side set to desired 0**. A Spot interruption takes the box away with nobody
having moved the desired count. The controller **already sees that**: `noteReplacement`
(`engine_control.go`) logs and audits (`replaced`) a `running` → `starting` transition at desired
1 as a replacement nobody asked for, and `describe()` notes that the PRIMARY deployment's
`updatedAt` moves when ECS replaces a task underneath, so the start deadline re-arms by itself
(review R2 — the draft said "there is no word for it"; there was). **What is missing is the
response**: ECS tries to place again under the same strategy, i.e. the same provider; when that
cannot be had it is silent until `StartDeadlineSec` (900 s by default), and the expiry is
counted as a failed start that doubles the cooldown. Nothing moves to another offer, and nothing
counts interruptions.

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
  strategy starts a new deployment and kills a generation in flight.** ⚠️ **The published
  specification (c) disagrees**: the `UpdateService` API Reference says of
  `capacityProviderStrategy` that "**this parameter doesn't trigger a new service deployment**"
  (read 2026-09-11; `desiredCount` carries the same sentence, yet ADR 0070 decision 6 measured
  `updatedAt` moving on desired 0 → 1 — so "no deployment" and "the clock does not move" are two
  different claims). Which one holds is open question 1. This ADR does **not** inherit 0074's
  reasoning as a fact (review R1).
- **`describe()` already carries the first three service events** (`engineServiceEventsKept`).
  Decision 5's code-based verdict needs no new API and no IAM. The failed-start cooldown is
  `AF_ENGINE_<ROLE>_FAIL_COOLDOWN_SEC` (default 900 s), doubled per failure, **at most four
  doublings (16×, four hours)** (`engineCooldownMaxDoublings`).
- **The parts of an engine table row that can be carried into a running process are the ladder
  and the capacity provider name** (`engine_table_reload.go`). A change to service, url, health,
  provider, idle or deadline is merely logged as needing a restart. The provider name could not
  be carried when this was drafted, and that became a real defect during the Spot switch (ADR
  0074, "provider の名前が変わると、走っている CP は箱を見失う" — the rung application went to
  the deleted old name and got a 400, `box` read `null`, and the panel still said "running on
  l4"); **#542 (2026-09-11) made it live.** Its shape is one field on `engineECS` holding both
  the "where to write" and the "which box is mine" name, forgetting the box cache and the
  last-applied rung on a rename — decision 3 widens that one field to a pair. ⚠️ **Adopting a
  ladder (zero rungs → N) still needs a restart** (same file: the rung gate is attached at
  construction only). Offers pass the same gate, so declaring `<role>Offers` for the first time
  on a deployment with no ladder costs one `force-new-deployment` of the CP (migration section).
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
- **An engine table row carries two provider names** — `capacityProvider` (on-demand, the
  existing field unchanged) plus `spotCapacityProvider` (empty = no Spot provider). The
  `draining` test and `box()` must **accept either name as this engine's own** — watching only
  one makes a box bought through the other read as `box: null`, which is exactly what happened
  during the rename in 0074. #542 narrowed the provider name to one field on `engineECS` so
  that "where to write" and "which box is mine" can never disagree; with two names they stay
  **one pair in one place**.
- **IAM does not grow.** The Resource for `ecs:DescribeCapacityProviders` /
  `ecs:UpdateCapacityProvider` in `60-engines.yaml` is already the prefix
  `capacity-provider/af-${AWS::StackName}-*` (ADR 0074 decision 9's text says "the two ARNs
  only"; the implementation is a prefix), and `-spot` falls inside it.
  `ecs:PutClusterCapacityProviders` (cluster-scoped) and `iam:PassRole` do not grow either
  (review R4).
- 🔴 **A stack running with `ImageCapacityOptionType=SPOT` migrates in a fixed order.** There
  the resource with logical id `ImageCapacityProvider` is **already named
  `af-<stack>-image-spot`** (0.18.1's replacement; the dev deployment was left in this state —
  0074, "後始末と、残したもの"). CloudFormation creates new resources before it deletes old ones,
  so a second provider of that name cannot be created and the update rolls back. The migration
  is two steps: **(1) an update back to `ImageCapacityOptionType=ON_DEMAND` first (a
  replacement, measured 147 s) → (2) the update to this ADR's template**, which retires the
  `ImageCapacityOptionType` parameter. A deployment at the default (ON_DEMAND) needs only (2):
  one `Add`, no replacement (measured on 0074 open question 1's throwaway stack).

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
- ⚠️ (b) **passes `forceNewDeployment: true` alongside.** Under the published specification (c)
  a strategy update starts no deployment (Background) — so writing the strategy alone leaves the
  task that is stuck PENDING under the old provider **exactly where it is**, never re-placed
  under the new one. `forceNewDeployment` is what re-places it, and it may be passed because
  the only task it kills is one PENDING task. As a side effect the PRIMARY deployment's
  `updatedAt` **should** move, and if it does the `StartDeadlineSec` clock switches to the next
  offer by itself (`describe()` in `engine_ecs.go` reads `updatedAt` — written for ADR 0070
  decision 6). If it does not, the CP keeps its own per-offer clock (open question 1 (c)).
- (a) passes **no** `forceNewDeployment`: the desired 0 → 1 start itself places under the new
  provider.

⚠️ **Not verified** (open question 1; the first thing P0 measures on hardware): (a) whether an
`UpdateService` that swaps the strategy between two MI providers goes through on this service —
the API Reference's list of valid transitions names only Fargate ↔ Auto Scaling group pairs,
and about Managed Instances says only "use the strategy parameter"; (b) whether updating the
strategy of a desired-0 service really is harmless; (c) whether a strategy-only update moves
`updatedAt`.

🔁 **What would change this**: if (a) is refused (an MI-to-MI transition cannot be made through
`UpdateService`), this ADR's skeleton is gone. The way back is the rejected "one service per
purchase type", which first has to solve keeping one Cloud Map name. ⚠️ **Being asked for
`forceNewDeployment` is NOT red** — at running 0 there is nothing for it to kill (review R1; the
draft said "rewrite if it is demanded", which was too strong).

### 5. Rule 2 — if no box arrives inside the budget, move to the next offer. **The failure code decides how to wait**

The per-offer budget `<role>OfferBudgetSec` (default **180 s**) measures **from setting desired
to 1 until a container instance is ACTIVE**. Once the box is there this clock stops and the
ordinary cold start (measured in 0071 and 0074) takes over. 180 s is a bit over four times the
**42 seconds** the successful case measured (0074) — a multiple of a measurement, not an AWS
specification.

Some cases skip ahead without waiting, because the service event's code says whether waiting can
help — and **all three codes are measured** (see "Failure codes and what they mean").

- **One lap around the list and it gives up**, entering the controller's existing cooldown
  (default 900 s, doubling per failure, four doublings at most — four hours) and starting from
  the top on the next demand. That is the only gate against buying forever. **One lap counts as
  ONE failure** (counting per offer would stretch the cooldown by the number of rows).
- **One audit line per move** (`engine.<role>.offer`). "Why are we running on the expensive box"
  has to be answerable afterwards.

🔁 **What would change this**: a measured case of `InsufficientInstanceCapacity` recovering after
more than 180 s. Then the budget becomes per-code, lengthened only for codes worth waiting on
(stock is a function of the clock).

### 6. Rule 3 — an interruption is "**there is demand and the box is gone**". Start again from the top of the list

Detection is **the transition `noteReplacement` already sees** (`running` → `starting` at desired
1; Background, point 4) **plus the container instance being gone** — if only the task was
replaced (an OOM kill, a health check) the box is still there, which is what ADR 0071 P0
measured and not an interruption. The CP has not moved the desired count. That is enough for
rule 3, because **the list is restarted from the top** whichever way the box was lost: a Spot
interruption and any other AWS-side loss want the same answer.

- ⚠️ **ECS starts re-placing without the CP doing anything** — under the same strategy, i.e. the
  same provider. So "from the top" means writing the strategy **only when the first candidate
  differs from the current provider** (running is 0, so decision 4's gate is open); when it is
  the same, the CP writes nothing and waits for ECS's own replacement. The clock has re-armed
  already, because `updatedAt` moved (`describe()`'s note).
- 🔴 **`stoppedReason` is read to EXPLAIN, not to conclude.** The CP does not call `DescribeTasks`
  today (only `DescribeServices` and the two container-instance calls). Saying "this was an
  interruption" out loud means adding it, so **whether it earns its keep is decided after P1
  measures** (open question 2). Detection works without it.
- **An interruption is not counted as a failure.** The controller's `failures` (which doubles the
  cooldown) counts starts that failed, not things that were taken away while running. Counting
  interruptions there makes **starts get slower the more Spot is used**. But **a rebuild after an
  interruption that gets no box by the deadline IS a failed start, and counts as it always has**.
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

- **Polly carries the wait** (ADR 0070 decisions 4 and 16). So rule 2's budget may be shorter
  here: waiting does not produce silence.
- ⚠️ **ADR 0070 decision 1's invariant does not break.** What the CP writes is always an
  **explicit** strategy; what is forbidden is writing neither a `LaunchType` nor a strategy.
- ⚠️ **With `UseSpot=off` the service is created with `LaunchType: FARGATE` and no strategy**
  (`50-tts.yaml`). The published specification (c) lists "Fargate launch type → Fargate capacity
  provider" as a valid transition, so the CP can move it — but then the template and the service
  disagree, and the next CloudFormation update puts `LaunchType` back (decision 12's CFN row). A
  deployment that declares TTS offers has the template write **an explicit `FARGATE` strategy
  (weight 1)** in place of `LaunchType`, so the only thing the CP ever changes is the provider
  name inside a strategy (review R10).
- ⚠️ `UseSpot` stays but changes meaning, from "use Spot" to "**put the Spot offer in the list**".
  Measured: flipping `UseSpot` on production was an **in-place** update (about 6 minutes, no
  replacement), though the new deployment came up at desired 1 (the contract of not declaring
  `DesiredCount`). The CP's route only moves the strategy from desired 0, so that side effect
  **should** not appear (open question 3).

🔁 **What would change this**: if Fargate Spot interruptions are frequent in a real deployment,
TTS may be cheaper on plain on-demand than being rebuilt while Polly reads. Settle it by adding
that arithmetic to ADR 0070's break-even (stand the engine up once reading is cheaper than Polly).

### 11. The panel says what it is running on **from the service's strategy**. EC2 is not asked

The CP knows which offer it passed to `UpdateService` — but **it does not answer from memory.**
The `DescribeServices` response carries the service's `capacityProviderStrategy`, so the one call
the CP already makes every tick yields "the current provider", and the offer is looked up from
that. No EC2, no new IAM. Answering from the remembered choice makes the panel lie the moment
CloudFormation rewrites the service (decision 12's CFN row) — a replay of the 0074 rename, where
`box` was `null` and the panel still said "starting on l4" (review R7). The CP's own choice
lives in the audit log (decision 5).

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
| the service's strategy | **only while running is 0** (decision 4) | under the published specification (c) **nothing moves** (only where the next task is placed changes); in (b) `forceNewDeployment` rides along to re-place the one PENDING task |
| the service's strategy, **written by CloudFormation** | every update that touches the service resource — every release that changes the task definition | **it reverts to the declared provider and a new deployment starts.** A running Spot box is replaced by an on-demand one, and the CP has no part in it |

The running-0 limit survives even if (c) is right: it keeps the CP from ever creating, on its
own side, a state where the running box and the strategy disagree, and passing (b)'s
`forceNewDeployment` at running 1 would be exactly the case 0074 rejected.

CloudFormation putting its declaration back (ADR 0074's open question 2) is absorbed through both
routes — the provider's four fields by the pre-start re-application, the service's strategy by
the next start. ⚠️ **The remaining window has the same shape**: between CloudFormation reverting
and the next start, the deployment sits at something other than what is declared. 🔴 **For the
service the window is a BOX, not a state** — a release's deployment replaces a running Spot box
with an on-demand one, so right after a release the engine runs on the declared provider
(on-demand). The template's declared default is the on-demand provider (the safe side); the
panel says so truthfully through decision 11; the next desired 0 → 1 lets the CP choose from the
top again. Avoiding it would mean taking the service's strategy out of CloudFormation's hands,
and `CapacityProviderStrategy` is the service's mandatory placement (ADR 0070 decision 1), so it
cannot leave. **One on-demand run per release** is accepted (review R7; the reversal condition
is decision 11's).

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
- ⚠️ **Declaring offers for the first time on a deployment that had no ladder costs one CP
  restart** (`force-new-deployment`, measured 217 s, blue/green). The rung gate is attached at
  construction only, and the table reload merely logs zero rungs → N as needing a restart
  (`engine_table_reload.go`; the same rule as 0074). A deployment that already has a ladder
  takes row changes and the `buy` column live.
- 🔴 **A stack at `ImageCapacityOptionType=SPOT` goes back to ON_DEMAND first** (decision 3: a
  provider of that name already exists).

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

🔴 **`StartDeadlineSec` (900 s by default) does not cover that total** — if decision 4 (b)'s
`forceNewDeployment` moves the PRIMARY deployment's `updatedAt`, the clock switches and it is
**900 seconds PER OFFER** (if it does not, the CP keeps a per-offer clock of its own; open
question 1 (c)). Either way **nothing bounds "waiting for a start" as a whole.** Two things bound
it instead:

- **One lap around the list, then cooldown** (decision 5).
- **The gateway's `AF_ENGINE_WAKE_TIMEOUT` (900 s) is not changed** (ADR 0071 decision 5). What
  bounds the caller's wait is that, not the sum of the CP's retries. ⚠️ Which means **on a
  deployment where rule 2 falls to the third row, the first request has almost certainly given up
  already**, and the next one meets a warm box. That is the shape ADR 0071 decision 5 already
  chose (`engine_waking` is a code clients retry).

## What an interruption really costs

| Item | Value | Source |
|---|---|---|
| The request in flight | **Lost.** The image role has nothing like Polly to read in its place | ADR 0071 decision 5, contrasted with ADR 0070 decision 4 |
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

1. 🔴 **Whether an `UpdateService` swapping the strategy between two MI providers goes through**
   ((a); the published specification's (c) "valid transitions" name only Fargate ↔ ASG pairs),
   whether updating the strategy of a desired-0 / running-0 service is harmless ((b)), and
   whether a strategy-only update moves the PRIMARY deployment's `updatedAt` ((c); that "no
   deployment" would mean "no clock movement" is contradicted by the `desiredCount` measurement).
   **Not measured on this deployment.** Depends on: decisions 4 and 5 — the skeleton of this ADR —
   and "The time budget". **The first thing P0 measures on hardware.** ⚠️ Being asked for
   `forceNewDeployment` is not red.
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
10. 🔴 **The orphan box.** After the move from the Spot offer to on-demand (decision 4 (b)), is
    the launch the Spot provider had **already started** cancelled, or does the box arrive late
    and sit with no task to carry until `scaleInAfter` (measured in 0071: a GPU box drains in
    427-463 s)? An MI provider goes to buy when it sees "task cannot be placed", so a box from
    the previous provider arriving right after the move is entirely plausible. Depends on:
    decision 5 (the shorter the budget, the likelier). In P0's hardware run 4, watch the old
    provider's `describe-container-instances` for a few minutes **after** the move (review R8).

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
| 0 | 🔴 **Measure open question 1 for $0, before any code**: create one throwaway MI provider (the same steps as 0074 open question 1; no GPU), then swap the existing image service's strategy to it and back with `UpdateService` at desired 0 | (a) does it answer 200 / (b) is the service intact (`describe-services` shows the strategy as written, desired still 0) / (c) does the PRIMARY deployment's `updatedAt` move. **If (a) is refused the ADR is rewritten from the skeleton.** Being asked for `forceNewDeployment` only means (b)'s route passes it — not red. Afterwards restore the strategy and delete the provider |
| 1 | Migrating a stack at `ImageCapacityOptionType=SPOT` (decision 3's two steps) | The update back to ON_DEMAND goes through, then this ADR's template goes through as an `Add`. **Skipping this rolls back on the name collision** |
| 2 | Both providers exist and both are in the cluster's list | Two in `describe-clusters`'s `capacityProviders` (creating one is enough, as 0074 measured) |
| 3 | Desired 0 → 1 picks the Spot offer and the box arrives as **`InstanceLifecycle: spot`** | `ec2InstanceId` from `ecs describe-container-instances` → `describe-instances --instance-ids` |
| 4 | Rule 2's fallback | Put a deliberately unbuyable type in the Spot offer (out of stock, or a misspelling) and watch the on-demand box arrive after the budget. **This is P0's positive control.** Keep watching the old provider's `describe-container-instances` for a few minutes **after** the move and record whether an orphan box (open question 10) turns up |
| 5 | `box()` accepts a box from **either** provider | `box` in `GET /api/admin/engines` is not `null` (during the rename in 0074 it was) |
| 6 | The rung application lands on **the chosen offer's** provider | Describe both before and after a switch: **not one field moves on the untouched one** |
| 7 | The panel | It names the current offer, its purchase type, and the order tried |

⚠️ **Cost estimate**: one GPU for one to two hours, **$1-3** ($0.67/h if Spot is available,
$1.26/h if not). When the measuring is done set `mode` to off and **watch until the container
instance is gone** ("stopped" and "gone" are different — 0074 P1).

✅ **To clear before P0 (done)**: carrying a capacity provider name live landed as #542
(2026-09-11). Decision 3 widens that one field to a pair. **Declaring offers for the first time
on a deployment that had no ladder** still costs one `force-new-deployment` of the Control Plane
(migration section).

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

## Review (2026-09-11, before P0)

In the manner of ADR 0071's review, the chain "decision → grounds → current state" was checked
against the code, the templates, the cited ADRs and the published specification. Verdict:
**approved (P0 may start). But one premise of the skeleton disagrees with the published
specification (R1), four statements of the current state were stale or wrong (R2-R5), there is
one migration trap (R6) and two design gaps (R7, R8). The text above has been corrected
accordingly** — this ADR is still "proposed" with not one line implemented, so the correction is
0074's "the implementation corrects the text" stage brought forward, with what changed and why
kept here. **Nothing was measured for this section**; re-reading was enough.

### What the review checked

- **R1. The published specification (c) says a strategy update starts no deployment.** The
  `UpdateService` API Reference (read 2026-09-11) says of `capacityProviderStrategy`: "This
  parameter doesn't trigger a new service deployment." Its list of valid transitions names only
  Fargate ↔ Auto Scaling group pairs and, of Managed Instances, says only "use the strategy
  parameter". The draft inherited 0074's rejection reason (strategy update = new deployment =
  a generation killed) as a fact; it is not a measurement, and it disagrees with the
  specification. Decision 4 (b) stood on "a new deployment starts (and `updatedAt` moves as a
  by-product)", so it now **passes `forceNewDeployment: true` explicitly**, and `updatedAt`
  moved to open question 1 (c). The reversal condition was corrected too — being asked for
  `forceNewDeployment` is not red (at running 0 there is nothing to kill); red is only "the
  MI-to-MI transition itself is refused". Hardware run 0 now measures this **for $0 with a
  throwaway provider, before any code**.
- **R2. "No word for an interruption" was wrong.** `noteReplacement` in `engine_control.go`
  already logs and audits (`replaced`) a `running` → `starting` at desired 1 as a replacement
  nobody asked for, and `describe()` already notes that `updatedAt` moves when ECS replaces a
  task. Decision 6 now builds on that transition plus "the box is gone too". And since ECS starts
  re-placing under the same provider without the CP, "from the top" was narrowed to **write only
  when the first candidate differs from the current provider**.
- **R3. The cooldown is not "capped at 4×" but four doublings = 16×.**
  `engineCooldownMaxDoublings = 4`, default 900 s (`AF_ENGINE_<ROLE>_FAIL_COOLDOWN_SEC`), four
  hours at most. Decision 5 corrected, and "one lap = one failure" made explicit.
- **R4. IAM does not grow.** The Resource in `60-engines.yaml` is already the prefix
  `capacity-provider/af-${AWS::StackName}-*`, which `-spot` falls inside. ADR 0074 decision 9's
  text ("the ARNs only") and its implementation differed.
- **R5. Carrying the provider name live landed as #542** (2026-09-11 15:57, after the draft).
  "In flight on another lane" became "done", and that implementation's one-field copy on
  `engineECS` is now decision 3's premise. The same file's rule that **adopting a ladder (zero
  rungs → N) needs a restart** had been dropped by the draft; the migration section has it.
- **R6. A stack at `ImageCapacityOptionType=SPOT` collides on the name.** There the logical id
  `ImageCapacityProvider` is already called `af-<stack>-image-spot` (the dev deployment is in
  that state — 0074, "後始末と、残したもの"). CloudFormation creates before it deletes, so a
  second of that name cannot be made. Decision 3 gained the two-step migration (back to
  ON_DEMAND first, 147 s), and it is hardware run 1.
- **R7. The service's strategy belongs to CloudFormation.** As long as the template declares it,
  every update that touches the service (a release changing the task definition) puts the
  declared provider back and starts a deployment — the running Spot box is replaced on-demand
  with no part played by the CP. The draft's decision 11, "from the CP's own choice", would have
  lied on the first release. Decision 11 now answers **from `DescribeServices`'
  `capacityProviderStrategy`** (no IAM), decision 12's table gained the CFN row, and "one
  on-demand run per release" is written down as accepted.
- **R8. The orphan box.** Whether a launch the Spot provider had already begun is cancelled when
  the strategy moves to on-demand is unknown. Added as open question 10 and to hardware run 4.
- **R9. Three wrong citations.** In 0070, "waiting never means silence — Polly reads" is decision
  4, not 5 (the idle window). 0072 decision 7 is "the engine table stays static"; SEED is 0074
  decision 2's word. 0045 decision 21 is about not adding `DescribeInstanceTypes`; "the Pricing
  API" is 0074 decision 1's wording. Fixed in the header, decision 10 and the interruption-cost
  table.
- **R10. TTS with `UseSpot=off` is created with `LaunchType`.** If the CP moves it to a strategy
  the template and the service disagree and the next CFN update reverts it (R7's shape).
  Decision 10 gained "a deployment declaring offers has the template write an explicit `FARGATE`
  strategy". P2 material; the decision itself stands.

### Per-decision revisions (already applied above)

| Decision | What changed | Why |
|---|---|---|
| Background 4 | "no word" → "there is a word, but no response" | R2 |
| Background (code) | 0074's rejection reason not cited as fact / the three events and the cooldown as they are / #542's shape and the restart for adopting a ladder | R1, R3, R5 |
| 3 | IAM does not grow / table fields `capacityProvider` + `spotCapacityProvider` / two-step migration for SPOT stacks | R4, R5, R6 |
| 4 | (b) passes `forceNewDeployment` explicitly, (a) does not / open question 1 split into (a)(b)(c) / reversal condition narrowed to "the transition is refused" | R1 |
| 5 | cooldown figures / one lap = one failure | R3 |
| 6 | `noteReplacement`'s transition plus the box gone / relation to ECS's own re-placement / a failed rebuild counts | R2 |
| 10 | 0070 decision 4 / an explicit `FARGATE` strategy where `LaunchType` was | R9, R10 |
| 11 | "the CP's choice" → "`DescribeServices`' strategy" | R7 |
| 12 | (c)'s consequence and the CFN row in the table / on-demand right after a release | R1, R7 |
| Migration | CP restart for a deployment with no ladder / SPOT stacks back to ON_DEMAND first | R5, R6 |
| Open questions | 1 rewritten / 10 (the orphan box) added | R1, R8 |
| P0 | hardware runs 0 ($0 throwaway provider) and 1 (migration) added / orphan watch in 4 / "to clear before P0" done | R1, R5, R6, R8 |

### How the implementation splits (P0)

Three places are touched at once (`engine_class.go` / `engine_control.go` / `engine_ecs.go`,
`60-engines.yaml`, `adminEngines.tsx`), and the lanes are cut along **two contracts**.

- **The engine table (template → CP)**: a row gains `spotCapacityProvider` (empty = none) and
  `offers` (empty = read `classes` as all-`od`). No existing field changes.
- **The admin API (CP → Console)**: a row of `GET /api/admin/engines` gains `offers` (the shape
  of `classes` plus `buy`), `offer` (the offer the service's strategy points at now — `{id, buy}`,
  omitted when none) and `offer_trail` (the order tried in this demand — `[{id, buy, result}]`,
  `result` one of `active` | `unfulfillable` | `insufficient` | `quota` | `budget`). `class` /
  `classes` / `class_default` / `class_is_default` **stay as they are** (the pin badge is the
  negation of `class_is_default`).

Four lanes — CP (Go; the CP side of both contracts), CFN (`60-engines.yaml`, PARAMETERS,
migration), Console (the display side of the admin contract), and hardware run 0 (open question
1 for $0 with a throwaway provider; **may start before any code, and if it is red it stops the
other three**). Hardware runs 1 onward wait for all three to land and go serially on one lane.

## Follow-up — P0 hardware run 0: open question 1, measured (2026-09-11, the dev deployment, $0)

Before a single line of P0 code, open question 1 — the one that decides the skeleton — was
measured on the dev deployment's image role. **No GPU was bought**: the service's `desiredCount`
was 0 from first command to last; all that happened is that one throwaway capacity provider was
created, the strategy was moved there and back, and the provider was deleted.
Elapsed: **10 minutes 42 seconds** (18:36:06-18:46:48 JST).

**Verdict: green. The skeleton stands. But decision 4 (a)'s "don't pass force" does not, as written.**

### Preconditions (checked before measuring)

- No session was deploying (`pgrep -af dev-deploy.sh` matched only the `pgrep` itself).
- The image role's service was `desiredCount` 0, `runningCount` 0, `pendingCount` 0, `status` ACTIVE.
- Saved first: strategy `[{capacityProvider: <the image role's provider>, weight: 1, base: 0}]`,
  PRIMARY deployment `id` `ecs-svc/0995585233792941490`, `updatedAt`
  `2026-09-11T15:15:38.591000+09:00`.
- The dev deployment's image provider is `capacityOptionType: SPOT` (still as 0074 "The cleanup,
  and what was left behind" left it). The throwaway was made **`ON_DEMAND`**, so what was measured
  is **a Spot provider to an on-demand provider** — exactly the transition decision 4 (b) performs.

### The steps and what came back

| Time (JST) | What was done | What came back |
|---|---|---|
| 18:36:32 | `create-capacity-provider` (`ON_DEMAND`, every other field copied from the existing image provider) | 200. `status: PROVISIONING` / `updateStatus: CREATE_IN_PROGRESS` |
| 18:37:00 | `describe-capacity-providers` / `describe-clusters --include ATTACHMENTS` | `ACTIVE` / `CREATE_COMPLETE`. The cluster's list went from 4 to **5** (0074's "creating one adds it by itself", reproduced) |
| 18:37:10 | **(a)** `update-service --capacity-provider-strategy ...` (neither `--desired-count` nor `--force-new-deployment`) | **HTTP 400 `InvalidParameterException`** (full text below) |
| 18:37:21 | `describe-services` after that call | neither the strategy nor `updatedAt` (`15:15:38.591`) moved **by one field** |
| 18:37:47 | **(b)** the same update plus `--force-new-deployment` | **200**. The strategy is the throwaway provider, `desiredCount` still 0. New PRIMARY `ecs-svc/5816081513831620451` (`createdAt` = `updatedAt` = `2026-09-11T18:37:48.588000+09:00`, `rolloutState: IN_PROGRESS`); the old `ecs-svc/0995585233792941490` went `ACTIVE` then `DRAINING` |
| 18:39:17 | the same deployment settling | `deployment completed` / `has reached a steady state` (`18:39:17.427`). **89 seconds with not one task to replace** |
| 18:40:32 | `update-service` back to the original (**no force**) | **the same 400. Direction makes no difference** |
| 18:40:34 | `update-service` back to the original (with force) | 200. The strategy matches the saved copy. New PRIMARY `ecs-svc/5931469680924617986` (`createdAt` = `updatedAt` = `2026-09-11T18:40:35.549000+09:00`) |
| 18:42:02 | the same deployment settling | `deployment completed` (`18:42:02.134`). **87 seconds** |
| 18:44:05 / 18:45:58 | `list-container-instances` then `describe-container-instances` | 4 instances. **None carries a `capacityProviderName`** and all registered on 09-06, 09-07 or 10:30 the same day — none belongs to either provider. **Not one new box arrived** |
| 18:45:58 | `delete-capacity-provider` | 200. `DEPROVISIONING` / `DELETE_IN_PROGRESS` |
| 18:46:37 | `describe-clusters` / `describe-capacity-providers` | The list is back to **the original 4**. The throwaway is `INACTIVE` / `DELETE_COMPLETE` (0074's "the trace does remain") |

What (a) returned (kept with its `x-amzn-RequestId`):

```
HTTP 400
{"__type":"InvalidParameterException","message":"When switching from launch type to capacity
provider strategy on an existing service, or making a change to a capacity provider strategy
on a service that is already using one, you must force a new deployment."}
```

### The verdict

- **(a) green.** The transition between two MI providers is not refused as such — the same
  transition went through with 200 once `forceNewDeployment` was added, and the strategy became
  what was written. What was refused is only **the way it was passed** (without force), which is
  the shape review R1 already declared "not red". **The skeleton of this ADR is not rewritten**,
  and there is no need to fall back to the rejected "one service per purchase option".
- **(b) green. The service is unharmed.** The refused call moved nothing (`updatedAt` stayed at
  the saved value). After the round trip the strategy matches the saved copy **exactly**, down to
  `weight` and `base`; `taskDefinition` is identical; `desiredCount` was 0 throughout, as were
  `runningCount` and `pendingCount`; no box ever arrived. The live image provider's
  `describe-capacity-providers` response is **byte-identical before and after**.
- **(c) it moves — but the question could not be asked as posed.** There is **no such path** as
  "an update of the strategy *alone*": (a) refuses it with 400. The path that does work (with
  force) **creates a new PRIMARY deployment**, so the `id` changes and `updatedAt` moves with it.
  Which means that on decision 4 (b)'s path the `StartDeadlineSec` clock (0070 decision 6,
  `describe()` in `engine_ecs.go`) **is reset for certain** — the CP does not need a per-offer
  clock of its own.

### What is new (and it lands on decision 4 (a))

1. **How public spec (c) has to be read.** The `UpdateService` API Reference says
   `capacityProviderStrategy` "doesn't trigger a new service deployment", but that does **not**
   mean "the strategy can be updated without force". On a service already using a strategy, ECS
   refuses a force-less strategy update **at the API's front door, with 400**. Both directions of
   the round trip returned the same wording.
2. 🔴 **Decision 4 (a) says "don't pass `forceNewDeployment`" — but if the strategy ends up
   different from the current one, (a) hits the same 400.** The error text says nothing about
   `desiredCount`; the only condition it names is "a service that is already using one". **A call
   that sets `desiredCount` to 1 buys a box, so this $0 run cannot measure it** (it gets measured
   with the same call in hardware run 3, desired 0 to 1). The implementation can simply be safe:
   **pass `forceNewDeployment` in (a) too when the strategy changes, and don't send
   `capacityProviderStrategy` at all when it doesn't.** running is 0 either way, so there is
   nothing to kill (decision 4's guard is unaffected).
   → **Open question 1 is settled as (a), (b) and (c). What remains is this one point** — whether
   force is required when `desiredCount` 0 to 1 and the strategy travel in the same
   `UpdateService` — and it is carried here, as a new open question, not in the body.
3. ⚠️ **A deployment does not finish instantly even with no task to replace** (89 seconds out,
   87 back). Whether ECS starts placing on the new provider **after** the deployment reaches
   `completed` or **as soon as** the new PRIMARY exists, when `desiredCount` is 1, was **not
   measured**. If it is the former, half of the per-offer budget (decision 5, 180 seconds by
   default) is spent on the switch itself. **Hardware run 4 (the fallback positive control) must
   record the gap between those two timestamps.**
4. Decision 11 confirmed: the service's strategy shows up in `DescribeServices` immediately after
   it is written. A panel that says "what it is running on now" from there does work.

### The cleanup

- The throwaway provider is deleted. The cluster's `capacityProviders` is back to **the original
  four**, and the throwaway remains only as an `INACTIVE` record (as in 0074's follow-up; it does
  not prevent recreating the same name).
- The service's strategy, `taskDefinition` and `desiredCount` are identical to the saved copy, and
  the live image provider is untouched.
- ⚠️ **What did not come back**: the PRIMARY deployment's `id` (`ecs-svc/0995585233792941490` to
  `ecs-svc/5931469680924617986`) and four added service events. Forcing a deployment recreates it,
  so there is nothing to restore. It does not affect starting the engine (`describe()` does not
  remember the PRIMARY by id).
- The raw responses (JSON, and the `--debug` log) are in `~/.cache/adr0075-run0/` of the session
  that measured this.

## Follow-up — what P0's CFN lane handed back to the text (2026-09-11, PR #548)

The template side (decisions 3, 9 and 12; contract A) landed as #548. **Nothing was deployed**
(that is hardware run 1 onward). What the implementation handed back goes here, with the
decisions left as written.

- **Decision 3 — the provider name is no longer computed.** Both are fixed names
  (`af-<stack>-image` / `af-<stack>-image-spot`), so 0074's convention "the name carries the
  purchase type" (the `!If` on `ImageCapacityOptionType`) ends with this ADR.
  `ImageCapacityOptionType` and the `ImageIsSpot` condition are retired. Not one line of IAM
  changed (it falls inside the prefix, as R4 said).
- **Migration — `standup.sh` drops `ImageCapacityOptionType`** (`af_param_drop`, in the same
  column as 0072 P6's retired parameters). The text only says "when the capture is rebuilt", but
  a captured line handed to `cloudformation deploy` as-is is refused as an undeclared key and the
  stand-up fails, so the drop is needed even without a rebuild. ⚠️ **`update.sh` passes no
  parameters to 60-engines**, so **updating a live stack still at `SPOT` (the dev deployment) to
  this template rolls back on the name collision.** P0 hardware run 1 (the one update back to
  `ON_DEMAND`) has to happen **before anyone next runs `dev-deploy.sh` against the dev
  deployment**. No gate was added on the script side.
- **Format — `<role>Offers` must be one line (`;`-separated).** The value goes into the engine
  table's JSON verbatim, so a raw newline cannot travel through the template. "`;` or a newline"
  is the parser's rule, not the parameter's (PARAMETERS, "The offers", carries the ⚠️).
- **Decision 12's "one on-demand run per release" has no test in P0's definition of done.** Add
  one line to hardware run 7 (the panel): right after a release it names the on-demand provider.
- The 0.19.0 release notes (en/ja) were rewritten from "write `ImageCapacityOptionType=SPOT` to
  buy on Spot" to the offer list and the two-step migration. ⚠️ **That wording assumes the CP
  lane lands in the same version** — if it ships without it, reduce the item to "one more
  provider".

## Follow-up — what P0's CP lane handed back to the text (2026-09-11, PR #552)

The CP side (decisions 1, 2, 3, 4, 5, 8, 11 and 12; the CP end of contracts A and B) landed as
#552. Definitions of done 1-5 are pinned in `engine_offer_test.go` with positive controls
(including that removing the guard makes the test fail, and a scan that the strategy is written
in exactly one place in the CP). What the implementation handed back:

- **Decision 5 — the budget starts at the new PRIMARY deployment's `updatedAt`, not at
  "desired set to 1".** Charging hardware run 0's "89 s of deployment with zero tasks" to the
  budget would move to the next offer before the offer had even asked for capacity.
- **Decision 4 (a) — if the strategy differs from the current one, a start passes
  `forceNewDeployment: true` too; if it is the same, `capacityProviderStrategy` is not passed at
  all** (the consequence of run 0; the guard stays). **Moving to the next offer within the same
  provider** passes only `forceNewDeployment` and no strategy — two offers may share one
  provider, and what changed is only the provider's instance requirements.
- **A third layer for decision 9 — an offer with no address leaves the candidates.** On a
  deployment with a `buy=spot` row but an empty `spotCapacityProvider`, that row is not
  addressable, so it is dropped with one log line. This catches a template-side mistake too.
- **Decision 2 — an empty candidate set is also audited** (`engine.<role>.offer`, target=none,
  once per demand).
- **One start path.** The admin API's `mode=on` and the gateway's `ensureStarted` go through the
  same strategy function; they used to move only the desired count, which was a way around
  decision 4 (a).
- The one P0 code path not yet measured is **hardware run 3** (whether force is needed when
  desired 0 → 1 and the strategy travel in the same call); the implementation leans to "needed".

## Follow-up — P0 hardware runs 1-7, measured (2026-09-11, the dev deployment, about $1.45 of GPU)

Right after P0's code (CP, CFN, Console) landed on develop, hardware runs 1 through 7 were done end to
end on the dev deployment's image role. The measured part took 3 hours 24 minutes (20:03-21:32 JST) and
**six GPU boxes, 54 minutes in total**.

**Verdict: all seven green. But "it starts on Spot as declared" does not hold until the three 🔴 below
are fixed** — with the default 180-second budget a Spot offer **cannot structurally succeed** (🔴1), a
Spot quota refusal is none of the codes in decision 5's table (🔴2), and every start **buys two boxes**
(🔴3). None of them changes a decision in the body: they are fixes to the implementation
(`engine_offer.go`, `engine_ecs.go`) and to how decision 5's table is matched.

### The verdicts

| Run | Verdict | Evidence (raw values) |
|---|---|---|
| 1 | 🟢 | The release-generation template that was deployed (`92281943`; normalised, it matches `get-template` exactly) run with `ImageCapacityOptionType=ON_DEMAND` took **169 seconds** (20:03:20-20:06:09). The provider went `af-<stack>-image-spot` → `af-<stack>-image`, `capacityOptionType: ON_DEMAND`, CloudFormation moved the service's strategy with it, `desiredCount` stayed 0. The cluster's list was back to four |
| 2 | 🟢 | The offers template plus `ImageOffers` took **79 seconds** (20:07:12-20:08:31). The cluster's list holds **both** `af-<stack>-image` and `af-<stack>-image-spot` (five). The engine table (SSM) has `spotCapacityProvider`, `offers` and `offerBudgetSec`. Then `dev-deploy.sh` (12 minutes, new CP and Console images); its 60-engines step said `No changes to deploy` — i.e. byte for byte what had just been deployed |
| 3 | 🟢 (on the second attempt) | **26 seconds** after `mode: on` a Spot box (`g6e.xlarge`, `InstanceLifecycle: spot`, 1a), and **5 min 30 s** to `state: running` / `warm: true` / `offer: {id: spot3, buy: spot}`. The first attempt was cut short by the default 180-second budget (🔴1). ⚠️ **No image was generated** — there is no way in from outside to the engine gateway (its token is the Workspace-internal `/internal/engine/token`), and driving somebody else's session was not an option. A generation on a Spot box is already measured in 0074 (one image in 403 s) |
| 4 | 🟢 (via the budget) | With Spot made **unbuyable** (see "How to build the positive control") the start moved to `l4` (od) and an on-demand box (`g6.xlarge`) arrived in **28 seconds** and reached `warm: true`. ⚠️ It moved because the budget ran out, not because the failure code was recognised (🔴2). The orphan box of open question 10 — the old provider's box arriving **after** the move — was not seen (nothing could be bought there in the first place) |
| 5 | 🟢 | In both 3 and 4 `GET /api/admin/engines` never showed `box: null` (Spot: `i-0fb3e7…` `g6e.xlarge`; on-demand: `i-0a4cdf…` `g6.xlarge`). **Both providers' boxes are recognised as its own** |
| 6 | 🟢 | The rung reaches **only the chosen offer's provider**. The moment `spot3` was chosen automatically the Spot side went `acceleratorTotalMemoryMiB` 8000 → 22000 and the **on-demand side's `describe-capacity-providers` response was byte-identical**. Just before the move to `l4` it was the other way round: the on-demand side went 44000 → 22000 while the Spot side kept `g6.4xlarge` at 16 vCPU |
| 7 | 🟢 | `offers` (three rows, with `buy`), `offer` and `offer_trail` match what happened. The trail across the fallback is `[{spot3, spot, budget}, {l4, od, active}]`. ⚠️ No screenshot of the panel (the admin modal cannot be opened by URL; known) |

### 🔴 1. The budget ends on "not RUNNING yet", not on "no box came" — so 180 seconds can never buy Spot

The first start (budget 180 s, the same three offers as now):

```
20:23:41 engines: image: starting on spot3 (22000 MiB VRAM declared)
20:24:07 the Spot box registers as a container instance (g6e.xlarge, InstanceLifecycle: spot)
20:27:11 engines: image: offer spot3 answered budget after 3m2s; trying l4 (od)
20:30:36 engines: image: offer l4 answered budget after 3m2s; trying l40s (od)
```

**The box was there after 26 seconds.** What was cut short is the task on top of it, which spends minutes
pulling the container and starting ComfyUI (0071 measured a 527-586-second cold start). At 180 seconds,
therefore, the walk goes to the end of the list **whether or not Spot can be had** — and buys a box at
every step. That run walked three offers in seven minutes, **bought two boxes and started none**. With
`ImageOfferBudgetSec=900` and the identical offer list, the second attempt stayed on `spot3` and reached
warm, as in the table above.

**The fix (decision 5's text does not change)**: end the budget on "**has a box arrived for this
offer**" — a container instance of that provider appearing, or the task landing on it — and leave the
rest to `StartDeadlineSec`. Today `engineOfferVerdict(events, waited, budget)` looks only at
`waited >= budget` and never reads whether a box exists.

### 🔴 2. A Spot quota refusal is `MaxSpotInstanceCountExceeded`, and it arrives wrapped in another error

Decision 5's table names `UnfulfillableCapacity`, `InsufficientInstanceCapacity` and `VcpuLimitExceeded`.
What Managed Instances actually writes into the service events when it is over the Spot quota is a
**fourth** code, and it comes **wrapped**:

```
(service af-<stack>-image) was unable to place a task. Reason: ResourceInitializationError:
Unable to launch instance(s) for capacity provider af-<stack>-image-spot.
MaxSpotInstanceCountExceeded: Max spot instance count exceeded. RequestId: 3495893a-…
```

The CP matched it against none of the known codes (`offer spot3 has no event matching a known capacity
failure code yet`) and **waited out the whole 15-minute budget** before moving on. Decision 5's "change
how you wait per failure code" therefore **does not work for the quota case** while that line is missing
from the table. Add `MaxSpotInstanceCountExceeded`, and match **as a substring across the wrapper**
(`ResourceInitializationError: …` comes first). ⚠️ Decision 5's branch "`VcpuLimitExceeded` → skip to the
other purchase option" **never fires on this deployment**: over-quota Spot speaks the other word.

### 🔴 3. A 0 → 1 start that also changes the strategy makes ECS buy **two** boxes

Hardware run 0 measured that putting the strategy, `desiredCount: 1` and force into one `UpdateService`
is accepted by the API. It is. **But it does not buy one box.** Both starts reproduced it:

| | The old strategy's (on-demand) box | The new strategy's (Spot) box |
|---|---|---|
| First | `g6.xlarge` registered 20:23:56 → 20:24:00 `stopped 1 pending tasks` | `g6e.xlarge` registered 20:24:07, the task goes here |
| Second | `g6e.xlarge` registered 20:39:36 | `g6e.xlarge` registered 20:39:53, the task goes here |

ECS applies `desiredCount` **first** and places a task under the old strategy, whose provider goes and
buys a box. The new PRIMARY that force created then places it again — **but the box that was bought
stays**. The second run's spare box was **billed for 16 min 28 s** ($0.51, 38% of the day's GPU spend),
and while it sat there `DEREGISTERING` it **blocked the next start through 0074 decision 4's exit wait**
(`class_swap_wait`): a `mode: on` at 20:50:09 did not actually begin for six minutes.

**The fix**: split decision 4 (a)'s start into **two `UpdateService` calls** — (1) the strategy alone
(`desired` still 0, `forceNewDeployment`; the path hardware run 0 measured as harmless), then (2)
`desiredCount: 1` alone once the new PRIMARY exists. When the strategy is already the right one, (1) is
not needed — the implementation's existing branch covers that.

### How to build the positive control — an offer that "cannot be bought" cannot be declared

Run 4 was specified as "write a type that deliberately cannot be bought". **That cannot be written.**
Neither a misspelled type (`g6.xxlarge`) nor requirements that no allowed type satisfies get past
`UpdateCapacityProvider`:

```
HTTP 400 ClientException: No instance types satisfy the instance requirements specified in the
Managed Instances capacity provider.
```

Worse, the CP **logs that failure and starts anyway** (`re-applying the instance class spot3 failed …`),
so the provider keeps **its previous requirements** and a real Spot box arrived while the declaration
said something else. ⚠️ **Rewriting an offer's types changes nothing unless `UpdateCapacityProvider`
accepts it.** What worked instead is **a type that exceeds the quota**: `spot3` as the single type
`g6.4xlarge` (16 vCPU against a Spot quota of 8) — the requirements and the type agree, so the update
goes through, and the refusal arrives at purchase time as 🔴2. **It costs $0 and it fails every time.**

### Everything else (no decision changes, but the next person will hit these)

- **`offerBudgetSec` does not survive a table reload**: `engines: image changed in the table in a way
  this process cannot take live (offer budget) - restart the Control Plane`. The offer list and the
  provider names do go in live (`instance classes re-read from /af-ws/engines: spot3, l4, l40s`).
  Changing the budget costs one `force-new-deployment` of the CP.
- **Two paths start the engine, and `offer_trail` records the same offer twice.** The admin toggle
  (`startEngine`) and the controller's first tick logged the same `starting on spot3` one second apart,
  and the trail became `[spot3 active, spot3 active]`. The damage is that **the budget clock restarts**
  and that the panel shows a second attempt that never happened.
- **When two offers share a provider, `offer` reports the first match.** `l4` and `l40s` are both
  on-demand, so a start running on `l40s` still reports `offer: l4`. Decision 11 says to read it off the
  service's strategy, so this is per spec — but **at the granularity of offers it is wrong**, and only
  the trail says so.
- **`class` and `offer` are not the same thing.** During the fallback `class` stayed at the top offer
  (`spot3`) while `offer` was `l4`. 0074's rung is a choice; 0075's offer is the row it is running on —
  the panel's wording must not blur the two.
- Open question 10 (the orphan box) **did not appear in that shape**. What appeared is 🔴3's "two boxes
  at start": the old provider's box does not arrive after the move, it arrives **together with the start**.

### What the GPU cost

| Box | Type | Purchase | Alive | Approx. |
|---|---|---|---|---|
| 1 | `g6.xlarge` | on-demand (🔴3's spare) | 10m43s | $0.23 |
| 2 | `g6e.xlarge` | Spot | 6m55s | $0.16 |
| 3 | `g6e.xlarge` | on-demand (🔴3's spare) | 16m28s | $0.51 |
| 4 | `g6e.xlarge` | Spot (the box run 3 reached warm on) | 9m6s | $0.21 |
| 5 | `g6e.xlarge` | Spot | 4m4s | $0.09 |
| 6 | `g6.xlarge` | on-demand (run 4's fallback) | 7m6s | $0.15 |

**54 minutes and about $1.34 in total** (plus the 7.80% Managed Instances fee = **about $1.45**).
⚠️ Arithmetic from list prices, not a settled bill (open question 5). **$0.74 of it — 55% — is 🔴3's
spare boxes.**

### The cleanup

- `mode: off`, and **the boxes were watched until they were gone**: no container instance carries a
  capacity provider, `GET /api/admin/engines` has `box: null` and `state: stopped`, and all six
  instances are `terminated` (the last one checked at 21:34:49). The service is at `desiredCount` 0 with
  the on-demand provider in its strategy, and the cluster lists five providers (both engine ones stand).
- `ImageOffers` was left declaring **`spot3` (Spot, three types), `l4` (od) and `l40s` (od)** — the
  continuation of the operator's decision in 0074 to make Spot the dev deployment's default.
  `ImageInstanceClasses` was not touched at all.
- ⚠️ **`ImageOfferBudgetSec=900` was left in place.** Back at the default 180 this deployment's image
  role does not start at all, for the reason in 🔴1.
- ⚠️ Both went in through `--parameter-overrides`, so **neither is in the `params/60-engines` capture**
  (standing the deployment up again with `standup.sh` loses them — the same caveat as 0074's follow-up).
- The raw responses (CloudFormation, ECS, EC2 and SSM JSON, and the CP's log) are in
  `~/.cache/adr0075-run1-7/` of the session that measured this.

## Follow-up — hardware runs 3 and 4, re-run after #561 (2026-09-11, the dev deployment, about $0.38 of GPU)

With the three fixes the previous follow-up asked for (#561) in the CP (`0.19.1-dev-ec3e1bc3`), runs 3
and 4 were done again **with the budget back at 180 seconds**. The declaration is unchanged:
`spot3` (spot, three types) / `l4` (od) / `l40s` (od). Two GPU boxes, 21 minutes, **about $0.38**.

**Verdict: two of the three are fixed. The third is not, and fixing the others opened one new hole.**
The budget now stops when a box arrives (🟢) and the Spot quota word is read in 25 seconds (🟢). But a
start **still buys two boxes** (🔴, and the cause is one layer deeper than the previous diagnosis), and
**the previous offer's events now decide the next offer** (🔴 — the budget used to hide this).

### Run 3 again (start on `spot3`, budget 180 s)

| Check | Result | Evidence |
|---|---|---|
| (a) did the start split into two `UpdateService` calls | 🟢 | The strategy write created a new PRIMARY (`ecs-svc/0129…`, `createdAt 23:15:24.488`) and `desiredCount: 1` followed in a separate call (the old deployment started its task at 23:15:32, eight seconds later) |
| (b) **is there only one box** | 🔴 **Two**, as before | on-demand `i-0a46e6…` registered 23:15:36, Spot `i-05f9f2…` registered 23:15:47. `describe-tasks` settles it: the first task is **`startedBy: ecs-svc/9980…`, the OLD deployment**. So **a new PRIMARY is not enough — while the old deployment is still `ACTIVE`, a `desiredCount: 1` makes ECS place one there too, and that provider buys a box** |
| (c) does it stay on the offer past 180 s and reach warm | 🟢 | `23:16:08 engines: image: offer spot3 produced a box; the start deadline owns the clock from here`. Past 23:18:49, where the budget would have expired, `offer` is still `spot3`, and **by 23:20:34 it is `state: running` / `warm: true`** (within 5 min 11 s of the start). The previous run walked the whole list at the same 180 s and started nothing |
| (d) is `offer_trail` a single row | 🟢 | `[{spot3, spot, active}]`, and one `starting on spot3` in the log (it used to be two, one second apart, with two trail rows) |
| (e) does `box` point at the ACTIVE one | 🟢 | While the on-demand box was `DEREGISTERING`, `box` was `i-05f9f2…` (`g6.xlarge`, `InstanceLifecycle: spot`, `status: ACTIVE`). It is never `null` |

🔴 **How to fix (b)**: raise the gate on the second call from "**the PRIMARY's id changed**" to "**the old
deployment is gone**" (one entry in `deployments`, i.e. `rolloutState: COMPLETED`). Measured, the old
deployment was still `ACTIVE` **2 minutes 35 seconds** later (23:15:51 → observed at 23:18:26), so the
start gets that much slower — that is the price. ⚠️ **"Has a new PRIMARY appeared" is not enough**, and
that is this re-run's main find: the previous follow-up's diagnosis stopped one step short.

### Run 4 again (`spot3` as the single over-quota type `g6.4xlarge`)

| Check | Result | Evidence |
|---|---|---|
| (a) is the quota word read, **without waiting out the budget** | 🟢 | `23:29:39 engines: image: offer spot3 answered quota after 25s` → `23:29:40 trying the offer l4 (od)`. **25 seconds** (it waited the full 15-minute budget last time) |
| (b) is `offer_trail` `[spot3 quota, l4 active]` | 🔴 | It is **`[spot3 quota, l4 quota]`**. `l4` was judged **five seconds** after the move — `answered quota after 5s` — and the run ended with `every offer was tried (spot3=quota l4=quota); giving up on this start` |
| (c) does an orphan box turn up on the Spot side | 🟢 | Watched for minutes: no container instance carries a capacity provider. Spot never bought anything |
| (d) is there only one box | 🟢 | **Zero** (nothing could be bought). This re-run cost **$0** |

🔴 **Why (b) happens**: the verdict reads the **newest three events** of `DescribeServices` and never asks
**which offer, or which provider, they belong to**. When `l4` was judged at 23:29:45 the newest three
still held the Spot quota event from 23:29:24, and that was read as `l4`'s answer. **The fix is already in
the text of the event** — it names the provider:

```
… was unable to place a task. Reason: ResourceInitializationError: Unable to launch instance(s)
for capacity provider af-<stack>-image-spot. MaxSpotInstanceCountExceeded: …
```

Judge only on events that name **the current offer's capacity provider** (or that are newer than
`took()`). ⚠️ The hole was there before as well, but **the 180-second budget always expired first**, so it
could not be seen: fix 1 made the walk fast enough to expose it. It is made worse by `quota` skipping a
whole purchase option (decision 5) — **one misread kills the entire list**.

### The bonus ($0) — a misspelled type no longer starts anyway

With `spot3`'s type set to `g6.xxlarge` (a spelling that does not exist) and `mode: on`:

```
23:35:57 engines: image: the offer spot3 could not be applied to its capacity provider, skipping it:
         updating the capacity provider af-<stack>-image-spot: operation error ECS: Update…
23:35:57 engines: image: starting on l4 (22000 MiB VRAM declared)
```

`offer_trail` is `[{spot3, spot, unusable}, {l4, od, active}]`. **Last time the same situation was logged
and the start continued on the provider's previous requirements** (declaration and reality apart). Now
the offer is skipped. 🟢

### Cost and cleanup

| Box | Type | Purchase | Alive | Approx. |
|---|---|---|---|---|
| 1 | `g6.xlarge` | Spot (the box run 3 reached warm on) | 7m34s | $0.07 |
| 2 | `g6.xlarge` | on-demand (🔴(b)'s spare) | 13m8s | $0.28 |

**21 minutes, about $0.35** (plus the 7.80% Managed Instances fee = **about $0.38**). ⚠️ **The spare box
is 79% of the spend** — worse than last time's 55%: the shorter the successful start, the heavier the
second box weighs.

- `mode: off`, **both boxes `terminated`**, no container instance carries a capacity provider,
  `box: null`, `state: stopped`, `desiredCount` 0.
- Left on the dev deployment: `ImageOffers` as `spot3` (spot, three types) / `l4` (od) / `l40s` (od), and
  **`ImageOfferBudgetSec` back at 180** (run 3 (c) reached warm at 180, so there is no reason to keep
  900). `ImageInstanceClasses` untouched. ⚠️ Both went in through `--parameter-overrides`, so neither is
  in the `params/60-engines` capture.
- The raw responses are in `~/.cache/adr0075-rerun/` of the session that measured this.

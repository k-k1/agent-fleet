# 0077. The Control Plane buys engine boxes itself with EC2 Fleet — ECS runs them with the EC2 launch type, and Managed Instances goes

English | [日本語](0077-engine-boxes-bought-by-cp.ja.md)

- Status: **proposed** (2026-09-12). Nothing is implemented.
- **Not one GPU was bought for this document.** Every number says where it comes from —
  (a) measurements in ADR 0045, 0070, 0071, 0074 and 0075, (b) facts read on 2026-09-12 out of this
  repository's code and templates and out of af-sandbox's read-only APIs, (c) things known only as
  AWS's published specification and **not confirmed on this deployment** (the vocabulary of an EC2
  Fleet `instant` response, the ECS agent's Spot draining, the ENI limit of `awsvpc` on the EC2
  launch type, whether CloudFormation treats `CapacityProviderStrategy` → `LaunchType` as a
  replacement). Nothing in (c) is load-bearing; what depends on it is listed under "Open
  questions", each naming the decision that waits on it.
- **What the operator asked for is unchanged from ADR 0075, to the letter** — (1) buy whatever clears
  the required VRAM, cheapest first, without distinguishing on-demand from Spot; (2) if Spot cannot
  be had, take on-demand; (3) a Spot box dying suddenly is acceptable, and rule 2 rebuilds it. The
  exception: the llm role is on-demand only. **The only thing that changes is who buys the box.**
- 🔴 **This ADR exists because ADR 0075's own revisit condition was met.** Under the rejected
  alternative "move to EC2 Fleet / an Auto Scaling group", 0075 wrote: "if rule 2's implementation
  turns into 'AWS retries and the CP retries on top of it', revisit this rejection." Three rounds
  on hardware (run 0, runs 1-7, the re-run) produced exactly that shape (Background).
- Related: [0075-engine-purchase-offers.md](0075-engine-purchase-offers.md) (the offer list, the
  VRAM filter, pin/automatic and the panel contract are **inherited as they are**; decisions 3, 4,
  5 and 12 are overridden) /
  [0071-self-hosted-inference-engines.md](0071-self-hosted-inference-engines.md) decision 1 (GPUs
  are bought as Managed Instances — **overridden**), decision 2 (one provider per role — **goes**),
  decision 5 (wake and hold), decision 7 (`draining`) /
  [0074-engine-instance-classes.md](0074-engine-instance-classes.md) decision 5 (applying a rung —
  **goes**), decision 9 (IAM — **replaced**) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 6 (no Managed
  Instances for Workspaces — stands), decision 19 (a home-baked AMI is slow — withdrawn, inherited),
  decisions 22, 23 and 29 (the pool's invariants) /
  [0070-tts-ondemand-engine.md](0070-tts-ondemand-engine.md) decision 1 (the placement is always
  spelled out — stands)

## Background

### What ADR 0075's three hardware rounds showed

0075's design — two providers per role, the service's strategy swapped only while running is 0 —
is fully in develop (#548, #549, #552, #561, #564). Every problem the hardware produced comes from
**placing the purchase decision on the ECS service's deployment machinery** (all sources are
0075's appendices).

| Run | What came out | Fix | What the fix cost |
|---|---|---|---|
| 0 ($0) | An `UpdateService` changing the strategy needs `forceNewDeployment` even at desired 0 (HTTP 400) | pass force | — |
| 1-7 ($1.45) | The 180 s budget never looked at "did a box arrive"; the list was walked to the end, two boxes bought, none started | stop the budget when the box arrives | the lookup must name the provider |
| 1-7 | The Spot quota answers `MaxSpotInstanceCountExceeded`, wrapped inside another error; not in the three-code table, so 15 minutes were waited | a fourth code, substring-matched | one more string match |
| 1-7 | Desired 0 → 1 and the strategy in one call: ECS places under the old strategy first and **buys two boxes** | split the start into two calls | — |
| re-run ($0.38) | Even split, while the old deployment is ACTIVE ECS places there too — **still two boxes** | gate on "the old deployment is gone" | **a start at least 2 min 35 s slower** |
| re-run | The previous offer's event (another provider's) was read as the next offer's answer; the start was abandoned | filter events by provider and time | one misread still voids the whole list |
| 1-7 | "An offer that cannot be bought" cannot be declared: `UpdateCapacityProvider` refused with 400 and the CP kept starting | add `unusable` | — |
| 3 | The three-type Spot row delivered a **g6e.xlarge** (not the cheapest g6); Managed Instances has no allocation strategy | cannot be fixed | — |

**The common shape**: ECS sees "a task cannot be placed" and goes to buy a box; the CP **infers the
outcome** from service-event strings and deployment state. Because the buyer and the judge are
different parties, (i) two boxes get bought, (ii) the echo of one judgement is read by the next,
(iii) an unbuyable request silently stays as the previous one. Each fix needed a hardware round
(about $2.2 over three), and each fix uncovered another hole of the same kind.

### What the code and the templates say (read 2026-09-12)

- **The engines are bound to Managed Instances resources** (`60-engines.yaml`): three capacity
  providers (llm, image, image-spot) and their `Associations`, an `InfraRole`
  (`AmazonECSInfrastructureRolePolicyForManagedInstances`), an `InstanceRole` / `InstanceProfile`
  (`AmazonECSInstanceRolePolicyForManagedInstances`), task definitions with
  `RequiresCompatibilities: [ MANAGED_INSTANCES ]`, an anonymous `Host: {}` volume. The services are
  `awsvpc`, Cloud Map A records, `MinimumHealthyPercent: 0`, no placement constraints. The GPU is
  `ResourceRequirements: [{Type: GPU}]`.
- **The Managed Instances AMI is Bottlerocket, and the model directory is an anonymous path with
  the task id in it** (PARAMETERS, "The model volume"; 0071 P0 measurement 4). A `SourcePath` lands
  on the root filesystem and dies with `No space left on device`; the anonymous directory cannot be
  cleaned up, so four starts cost 18.5 GB × 4. The conclusion there: "on Managed Instances a warm
  model volume cannot be built out of host volumes at all".
- **The slot pool already has the CP buying EC2** (`runtime_ecs_ec2.go`): `RunInstances` one at a
  time, a launch template (`40-ec2-pool.yaml`; the AMI is the SSM public parameter
  `…/amazon-linux-2023/recommended/image_id`, resolved by CloudFormation), user data writing
  `ECS_CLUSTER` to join the cluster, tags `af-pool` / `af-role=slot` / `af-slot-size`, a
  registration wait on `ListContainerInstances` every 3 s, and departure as
  `DeregisterContainerInstance(Force)` → `TerminateInstances`. Capacity errors go through
  `isEC2CapacityError` (string match) and on to the next AZ. **No Spot anywhere**
  (`InstanceMarketOptions` / `CreateFleet` appear nowhere in the repository).
- 🔴 **The slot pool's only way of saying "not my box" is a non-empty `capacityProviderName`**
  (`isPoolContainerInstance`, the intake of 0071's review R7(a)). A box the CP buys on EC2 has an
  **empty** `capacityProviderName`, so that test collapses as it stands. The EC2-side walks
  (`freeSlots`, `poolSize`, `sweepFreeSlots`, `makeRoom`) filter on the tag `af-role=slot` and are
  unaffected. What mixes is the ECS side: `registeredSlots`, `sweepGhostInstances`,
  `deregisterSlot`, `slotTaskCounts` — four call sites, one function.
- **A Workspace task is pinned to a box by a task-definition placement constraint
  `memberOf(ec2InstanceId == …)`.** The service is `LaunchType: EC2`, `awsvpc`, desired 1.
- **The CP's IAM** (`20-platform.yaml`, `Ec2SlotPool`): `ec2:RunInstances` / `TerminateInstances` /
  `DescribeInstances` / `CreateTags` and more on `Resource: *` (Describe cannot be scoped; tags are
  the fence), `iam:PassRole` on `role/af-*-slot`. **Neither `ec2:CreateFleet` nor
  `ssm:GetParameter` is there.**
- **One shared cluster.** On af-sandbox (b): four container instances (m7i / m8g slots, no
  `capacityProviderName`) next to five providers (FARGATE, FARGATE_SPOT, llm, image, image-spot).
- **The ECS-optimized GPU AMI's SSM public parameter resolves in ap-northeast-1** (b):
  `/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id` (on 2026-09-12:
  `al2023-ami-ecs-gpu-hvm-2023.0.20260901-kernel-6.1-x86_64-ebs`, ECS agent 1.106.2, Docker
  25.0.16). The AL2 GPU variant resolves too.
- **`AWSServiceRoleForEC2Fleet` does not exist on af-sandbox** (b). `AWSServiceRoleForEC2Spot` was
  created in 0074.
- Of 0075's implementation, **what does not depend on who buys**: offer parsing (the `buy`
  column), the VRAM filter, pin/automatic (`engine_<role>_class`), `offer_trail` and contract B,
  the Console card (#549). What does: the "buy", "wait for the box" and "read the failure code"
  parts of `engine_offer.go`, the strategy write and the provider-named `boxOn()` in
  `engine_ecs.go`, `applyClass` (`UpdateCapacityProvider`) and `startGate` in `engine_class.go`.

### What is known as published specification (c) — not load-bearing

- **EC2 Fleet of type `instant`** "places a **synchronous one-time request** for your desired
  capacity. In the API response, it returns the instances that launched and provides errors for
  those instances that could not be launched." Spot and On-Demand can share one request, several
  instance types and AZs go in the overrides, and Spot may use the `price-capacity-optimized`
  allocation strategy (the most-available pools, then the lowest priced of those). A launch
  template's `ImageId` may be `resolve:ssm:<parameter>` — **for instant fleets only**. The fleet
  is deleted automatically once all its instances are terminated, or if none launched.
- The same page on `RunInstances` Spot: "your Spot Instance request is limited to **one instance
  type and one Availability Zone** … you can't launch Spot Instances and On-Demand Instances in the
  same request … If the Spot capacity pool does not have sufficient Spot Instance capacity for your
  request, **the RunInstances call fails**."
- The ECS agent's `ECS_ENABLE_SPOT_INSTANCE_DRAINING=true` sets the container instance to DRAINING
  on the two-minute Spot notice (the AWS page could not be fetched on 2026-09-12; **unconfirmed**).
- `awsvpc` on the EC2 launch type uses one ENI per task. A g6.xlarge allows 4 ENIs (**unconfirmed**).

### ADR 0071 decision 1's three reasons for Managed Instances, re-read today

| 0071's reason | State on 2026-09-12 |
|---|---|
| **Owning the AMI and the NVIDIA driver** | The AWS-maintained ECS-optimized GPU AMI resolves from an SSM public parameter (b). Nothing is owned. 0045 decision 19 withdrew "bake our own AMI" as slow, so there is no motive to bake either |
| **EBS billed while stopped** | An engine never stops. **It terminates** (as 0045 decision 23 chose for slots). There is no stopped state to bill |
| **Keeping out of ADR 0045's sweep and `Ec2MaxSlots` on every path** | Genuinely needed. But the EC2 side is already out by tag, and the ECS side goes through one function at four sites (Background). Decision 3 moves that test to an attribute |

The own-EC2 route 0071 kept "as the alternative for a deployment whose region has no Managed
Instances" **has existed as an alternative since that day.** What 0045 decision 6 rejected MI for
on Workspaces (no stop, no pointing at an existing volume) was an advantage for engines — but the
advantage is "the instance ceases to exist when the task is gone", not "the purchase decision is
delegated to ECS". With decision 5 giving the terminate to the CP, the advantage stays and only
the delegated judgement comes back.

## Decisions

### 1. The CP buys the box with `CreateFleet(type=instant, TotalTargetCapacity=1)`. One offer row = one call

0075's offer list (`<role>Offers`, eight fields, the `buy` column) is **unchanged**. What changes is
what "trying" a row means: **"put the row's type set into the overrides, make `buy` the
`DefaultTargetCapacityType`, and call one instant fleet."**

- `buy=spot`: `DefaultTargetCapacityType: spot`, `SpotOptions.AllocationStrategy:
  price-capacity-optimized`. 0075 run 3's problem — the three-type Spot row delivered a
  **g6e.xlarge**, not the cheapest — came from Managed Instances having no allocation strategy;
  here it is controllable for the first time.
- `buy=od`: `DefaultTargetCapacityType: on-demand`, `OnDemandOptions.AllocationStrategy:
  prioritized` (the order of the overrides = the declared order).
- The overrides are the product "type × private subnet". The CP does not choose an AZ (the
  slot pool's `spreadAZs` exists for home volumes' AZ affinity; an engine has no home).
- 🔴 **The response is synchronous.** A launched box comes back as its `InstanceId`; otherwise
  `Errors[]` carries an `ErrorCode`. **0075's budget clock, service-event string matching and
  deployment gate all disappear at this one point.** Moving to the next offer happens when "the
  response had no box", and nothing is waited for.
- The budget (`<role>OfferBudgetSec`) shrinks in meaning to **the ceiling on the box registering
  with ECS** (the slot pool's `waitSlotRegistered`, polling every 3 s). Default 300 s (0045 decision
  22 measured boot → ECS registration at 21 s, 77 s with a home-baked AMI; ten-plus times that).
  Past it, terminate the box and move to the next offer.
- An instant fleet deletes itself once its boxes are gone (c). The CP **does not remember** the
  fleet id. It remembers the `InstanceId` and the tags on the box (decision 3).

🔁 **What would change this**: a response on real hardware carrying neither a box nor an error
((c) says it is always one or the other). Then one `DescribeFleets` follow-up is added.

### 2. ECS only runs it. The service is `LaunchType: EC2` with no strategy. Desired rises after the box is here

- The service **spells out** `LaunchType: EC2` (0070 decision 1's invariant — writing neither is what
  is forbidden, and `LaunchType` satisfies it). There is no `CapacityProviderStrategy`. The problem
  of CloudFormation putting the strategy back on every release (0075 decision 12's CFN row)
  **disappears because there is nothing to put back**.
- The task definitions say `RequiresCompatibilities: [ EC2 ]`. `awsvpc`, Cloud Map, the GPU
  `ResourceRequirements`, the fetch sidecar and the idle wrapper are untouched.
- **The placement constraint lives on the service and names the role's attribute**:
  `memberOf(attribute:af-role == engine-<role>)`. Slots write `ec2InstanceId == …` on the task
  definition because "this user on this box" is the point there; an engine changes boxes and must
  not cut a task-definition revision each time. With the role attribute the service never changes.
- 🔴 **The order is the reverse of 0075's.** Under Managed Instances, setting desired to 1 sent ECS
  out to buy a box. Here **the CP buys the box, waits for it to register, and only then sets
  desired to 1**. There is no interval in which desired is 1 and no box exists, so the re-run's
  "who places the PENDING task" problem (the old deployment did) has no shape to take.
- The desired 0 → 1 `UpdateService` carries the desired count only (no strategy, no
  `forceNewDeployment`). 0075 run 0's 400 was about changing the strategy; there is nothing to
  change here.

🔁 **What would change this**: if the attribute placement constraint does not bind on an
EC2-launch-type service (open question 2), write `ec2InstanceId` on the task definition as slots
do and cut a revision per start.

### 3. A box is identified by the ECS attribute `af-role=engine-<role>` and by EC2 tags. The slot pool's test changes

- The launch template's user data writes `ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-<role>"}` into
  `/etc/ecs/ecs.config` (where slots write `ECS_CLUSTER`). One launch template per role
  (`af-<stack>-engine-llm` / `af-<stack>-engine-image`).
- EC2 tags: `af-pool=<cluster>`, `af-role=engine-<role>`, `af-engine-offer=<offer id>`,
  `af-engine-buy=spot|od`, `af-managed-by=agent-fleet`. Following 0045 decision 29's general rule
  ("when the thing is in AWS and its name is in the DB, a cleanup that can only be reached from
  the DB turns 'the row is gone' into a permanent leak"), **the CP must be able to find all its
  boxes from `describe-instances` tags alone, with no memory.** Unlike a Managed Instances box, an
  EC2 box the CP bought is enumerable by `describe-instances` (the reverse of 0071 P1 measurement 2).
- 🔴 **`isPoolContainerInstance` changes from "`capacityProviderName` is empty" to "the `af-role`
  attribute is absent, or is `slot`".** This is the successor of 0071 review R7(a)'s test: a
  Managed Instances box (provider name) and an EC2 engine box (attribute) both leave the pool
  through the same one function. The test pins three kinds of container instance (slot, MI engine,
  EC2 engine); the positive control is that removing the attribute makes an engine box count as a
  slot.
- The EC2-side walks already exclude it by `af-role=slot` (`slotsOfMyType`, `poolSize`,
  `sweepFreeSlots`, `makeRoom`, `PoolStatus`). That is the whole of "keeping out of `Ec2MaxSlots`".
- 0070 decision 1's invariant, "one non-Workspace task mixed in breaks the premise", is kept **on
  the box side**: no Workspace task lands on an engine box (a Workspace's constraint names an
  `ec2InstanceId`), and no engine lands on a Workspace box (decision 2's attribute).

🔁 **What would change this**: if `ECS_INSTANCE_ATTRIBUTES` is not honoured by the agent on the
AL2023 GPU AMI (open question 2), the CP calls `PutAttributes` right after registration.

### 4. An interruption is read as an EC2 fact. The rebuild starts from the top of the list, synchronously

- 0075 decision 6's detection (`running` → `starting` at desired 1, and the box gone) is inherited.
  Here "the box is gone" can be stated from **`describe-instances` state (`shutting-down` /
  `terminated`) and from no box tagged `af-role=engine-<role>` being `running`**. The Managed
  Instances "cannot enumerate" limit does not apply.
- The ECS agent's Spot draining (c) is enabled in the launch template. If the two-minute notice
  puts the instance into DRAINING, the service's task stops first and `noteReplacement` (0075
  Background, point 4) records "a replacement nobody asked for". If it does not work, detection
  still holds on the two conditions above.
- The rebuild is decision 1's call again from the top of the list. Being synchronous, 0075
  decision 6's restriction ("write only when the first candidate differs from the current
  provider", to avoid racing ECS's own re-placement) is unnecessary — ECS buys nothing.
- An interruption is not a failure (0075 decision 6 inherited). A rebuild that gets no box by the
  registration ceiling is.
- **An offer interrupted twice in a row is skipped for the rest of that demand** (0075 decision 6
  inherited).

🔁 **What would change this**: the same as 0075 open question 9 (if interruptions are rare, the
skip is not needed).

### 5. Departure is the CP terminating the box. `draining` is "from the CP's terminate until EC2 says terminated"

- When the idle window (0071 decision 5, 0070 decision 5) closes, the CP sets desired 0 → waits
  for the task to go → `DeregisterContainerInstance(Force)` → `TerminateInstances` (the order of
  the slot pool's `terminateSlot`; 0045 decision 23's reason — never leave an ACTIVE ghost behind
  when it is you who removes the box — applies as written).
- `draining` (0071 decision 7) changes meaning from "Managed Instances drains it out of our hands
  in 427-463 s" to "**from the CP issuing the terminate until EC2 reports `terminated`**". The
  slot measurement (0045 decision 22: stop → terminated in 93 s, a CPU figure) suggests minutes
  on a GPU too, but it is **unmeasured** (open question 6). 0071 decision 7's drain wait (0074
  decision 4) is inherited — no new box while the old one is still `running`.
- 🔴 **The Managed Instances `scaleInAfter` trap disappears.** 0071 P0 measurement 3's "a box left
  under `-1` is never reclaimed even after the value changes, and `terminate-instances` is refused
  by the MI policy" cannot exist once the CP is the terminator. In its place, **a terminate the CP
  forgets leaves a box for ever** — the sweep loop gains one pass in the shape of 0045 decision
  29 (boxes tagged `af-role=engine-*`, `running`, absent from the CP's memory), audited. It
  removes only a box with zero tasks whose registration is older than `ghostAfter` (the caution
  of 0045's `sweepGhostInstances`).

🔁 **What would change this**: a measured terminate → `terminated` above five minutes on a GPU.
Then 0071 decision 7's window arithmetic is rewritten.

### 6. The model directory can be a host volume. P0 keeps it anonymous; P1 measures

- The ECS-optimized AL2023 GPU AMI is not Bottlerocket. `Host: {SourcePath:
  /var/lib/af-engine-models}` simply works, and with the g6 local NVMe mounted from user data
  **the second start on the same box does not re-fetch the models** (structurally impossible on
  Managed Instances — Background).
- The value is limited, though: a box goes on departure (decision 5) and on interruption
  (decision 4). It is warm only when desired goes 0 → 1 inside the idle window — the very case
  0071 decision 5 already decided not to stop in. **So P0 keeps the anonymous `Host: {}`**
  (nothing changes).
- P1 measures: against the measured re-fetch (0071: S3 → local at 104-147 MB/s, 6.94 GB in 65 s),
  how many seconds a host volume saves. If it is a minute, it is not needed.

🔁 **What would change this**: a measured re-fetch above five minutes on a comfy deployment
holding 30 GB (0075's computed "what an interruption costs"). Then P1 moves forward.

### 7. No AMI is owned. The launch template's `ImageId` is `resolve:ssm:` on the SSM public parameter

- `resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id`. Instant
  fleets support it (c). CloudFormation's `AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>` (the
  slots' `SlotAmiId`) resolves "at stack update"; this resolves "at launch" — **the newest AMI
  without a stack update**. Which to use is open question 7 (whether the AMI changing per launch
  is acceptable).
- 0045 decision 19's lesson is inherited: a home-baked AMI is slow (a new user's start 144 s →
  192 s). Nothing is baked.
- 0045 decision 19's trap is inherited too: say in the README and the scripts that the parameter's
  type is "an SSM parameter name, not an AMI id".

🔁 **What would change this**: a day when "the latest GPU AMI" carries an NVIDIA driver version
the engine images do not agree with. Then a knob pinning `…/gpu/<version>` is added.

### 8. The offer list, the VRAM filter, pin/automatic and the panel contract stand. Applying a rung goes

- 0075 decision 1 (declared order is tried order), decision 2 (filter by VRAM), decision 8
  (pin/automatic), decision 11 (the panel says what it runs on — now **from the box's tags**,
  `af-engine-offer` / `af-engine-buy`, not the service) and contract B (`offers` / `offer` /
  `offer_trail`) are **unchanged**. The Console (#549) does not change.
- **0074 decision 5's rung application (the four-field read-modify-write of
  `UpdateCapacityProvider`) goes.** A rung is the type set in the overrides itself; the declaration
  is the request. "Declared and actual drift apart" (0075 runs 1-7's `unusable`) has no structure
  to happen in — a misspelled type is refused **on the spot** by `CreateFleet`. Of `startGate`,
  only the drain wait remains (decision 5).
- The failure-code table (0075, "Failure codes and what they mean") moves from service-event
  strings to **`CreateFleet`'s `Errors[].ErrorCode`**. The vocabulary is measured first (open
  question 3). The shape of the responses is the same (no stock → next; quota → skip that purchase
  type). **"Filter events by offer" (#564) is not needed** — a response contains only its own call.
- The `usdPerHour` convention (0074 decision 1, 0075 decision 1: the all-in price of the most
  expensive type the offer can buy) becomes "the EC2 price itself", since the 7.80% Managed
  Instances fee is gone. Writing the Cost Explorer figure stays the rule.

🔁 **What would change this**: if contract B needs "the box id" (0075 kept `box.provider`
CP-internal), it is added by agreement with the Console lane.

### 9. The llm role stays on-demand only. TTS stays on Fargate

- 0075 decision 9 (no `spot` row for llm) is inherited. There is no second safeguard of "no
  provider is created" here, so **the CP refuses a `spot` row in `LlmOffers`** (dropped at parse
  time, with a log line).
- 0075 decision 10 (TTS) is outside this ADR. TTS is Fargate, and `FARGATE_SPOT` ↔ `FARGATE`
  stays a matter of the service's strategy.

### 10. IAM: the CP gains EC2 Fleet permissions and loses every Managed Instances one

| Added | Removed |
|---|---|
| `ec2:CreateFleet`, `ec2:DescribeFleets`, `ec2:DeleteFleets` (`Resource: *`; a fleet cannot be resource-scoped) | `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` |
| `ec2:CreateLaunchTemplateVersion` is **not** added (CloudFormation owns the template) | `ecs:PutClusterCapacityProviders` |
| `iam:PassRole` on `role/af-*-engine` (`iam:PassedToService: ec2.amazonaws.com`) | `iam:PassRole` on `InfraRole` / `InstanceRole` |
| `iam:CreateServiceLinkedRole` limited to `spot.amazonaws.com` and `AWSServiceRoleForEC2Fleet` | `InfraRole` itself |
| `ssm:GetParameter` is **not** added (`resolve:ssm:` is resolved by EC2; the CP never reads the AMI) | |

`ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` / `CreateTags` are already in
`Ec2SlotPool`; `ecs:ListContainerInstances` / `DescribeContainerInstances` /
`DeregisterContainerInstance` already in `EcsContainerInstances`. A deployment **without** the
slot pool (not ecs-ec2) lacks those two Sids, so 60-engines carries them in the same shape.

⚠️ **On an account without `AWSServiceRoleForEC2Fleet`, the first `CreateFleet` fails** (c).
af-sandbox has none (b). The deployment steps (`standup.sh`) run `create-service-linked-role`
once (the same column as `AWSServiceRoleForEC2Spot`, created by hand in 0074).

🔁 **What would change this**: if a condition key scoping `CreateFleet` to the launch template
ARN (`ec2:LaunchTemplate`) works on real hardware, `Resource: *` is narrowed.

### 11. Migration: remove the Managed Instances resources, add launch templates and an instance role. Two steps, in order

- Removed from `60-engines.yaml`: the three capacity providers, `Associations`' provider entries,
  `InfraRole`, the MI `InstanceRole` / `InstanceProfile`, and the parameters
  `*AllowedInstanceTypes` / `*AcceleratorMemMinMiB` / `*VCpu*` / `*Mem*` / `*StorageGiB` /
  `*UseLocalStorage` / `*ScaleInAfter` (the requirements are **already** in the offer list: 0074
  decision 1 made a rung "a set of requirements" and 0075 added `buy`; the MI four fields were a
  copy of the ladder).
- Added: one launch template per role (AMI, instance profile, `EngineSg`, user data,
  `MetadataOptions HttpTokens: required`, root gp3, tags), `EngineInstanceRole`
  (`AmazonEC2ContainerServiceforEC2Role` + `AmazonSSMManagedInstanceCore`),
  `EngineInstanceProfile`. In the engine table JSON, `capacityProvider` / `spotCapacityProvider`
  become `launchTemplate` (id); `offers` / `offerBudgetSec` / `classes` are unchanged.
- 🔴 **Order**: (1) **with not one box standing** (both roles `mode: off`, no engine box among
  the container instances), (2) apply this template. Deleting a provider goes through when it has
  no box (measured on 0074 open question 1's throwaway: `delete-capacity-provider` at zero boxes
  is `INACTIVE` at once). Whether the service's `CapacityProviderStrategy` → `LaunchType` is a
  replacement in CloudFormation is open question 1 — if it is, the Cloud Map registration blinks.
  At desired 0 no request is lost.
- Removing the providers from `Associations`: the cluster's provider list is **owned by
  60-engines**, so someone must still hold FARGATE / FARGATE_SPOT (`50-tts` uses `FARGATE_SPOT`,
  0070). **`Associations` stays, with `[FARGATE, FARGATE_SPOT]`** (only the providers leave).
- 0075's hardware run 1 (the two-step migration of a SPOT stack) is no longer needed — the
  providers go entirely, so there is no name to collide with.
- Captured `*AllowedInstanceTypes` and friends in `params/60-engines` are dropped by `standup.sh`'s
  `af_param_drop` (the same column as 0075's `ImageCapacityOptionType`). `update.sh` passes no
  parameters, so retired ones fall away silently.
- Template size: three providers (about 3 KB) and the MI parameter block leave, two launch
  templates arrive — **expected to shrink**, but `wc -c` before and after (wall 51,200; now 42,500).

🔁 **What would change this**: if open question 1 says "the service must be recreated", the
migration becomes "delete the service, then create it", and the Cloud Map name is gone for some
minutes. Unlike TTS nobody is waiting (image retries, an llm conversation breaks), so it is done
inside a window with both roles at `mode: off`.

## Rejected alternatives

- **Keep fixing Managed Instances (a fourth round of 0075).** #564 is worth landing (event
  filtering is right), but the remaining cost is structural: a start is at least 2 min 35 s slower
  until the deployment settles, the judgement is inference from strings, and which type a
  three-type Spot row buys cannot be controlled. Three rounds, about $2.2 and a day, produced
  consecutive holes of the same kind. **There is no guarantee a fourth round is the last.**
- **An Auto Scaling group (`maintain`).** Mixed-instances policies and Spot replenishment exist
  there, but "one box per role, the CP holds desired" collides with the ASG's autonomous
  replacement — the same shape 0075 decision 6 struggled with against ECS's own re-placement, now
  against the ASG. `instant` is enough.
- **`RunInstances` (as the slot pool does).** Spot is limited to one type and one AZ, and Spot
  and On-Demand cannot share a request (c). 0074's measurement "widening to three types bought the
  box (1/10 for one type, 9/10 for three)" maps straight onto instant-fleet overrides, so that is
  what is taken.
- **EC2 Fleet of type `request` / `maintain`.** Asynchronous; back to 0075's "infer the outcome".
- **Baking our own AMI** (image layers, models). Withdrawn as slow by 0045 decision 19.
- **Karpenter / EKS.** A different scale. One box per role does not justify another controller.
- **Keeping `ecs:UpdateCapacityProvider` and running MI and EC2 side by side.** Two ways to buy
  keep 0075's judgement holes on one side. **Switch in one move** (migration section).

## Which existing decisions this overrides, and how the appendices go on

An ADR is append-only and immutable (`docs/CONVENTIONS.md` §5). No existing decision's text is
edited; each affected ADR gains "**Appendix — ADR 0077 overrode this decision (date)**" at its end.

| ADR / decision | What changes | What does not |
|---|---|---|
| 0071 decision 1, "GPUs are bought as ECS Managed Instances" | **the CP buys with EC2 Fleet**; the three objections to own EC2 dissolved as in the Background table | "no task, no instance" (guaranteed by the CP in decision 5) / never on the slot pool |
| 0071 decision 2, "one capacity provider per role" | **providers go**; one launch template per role | two roles never share an instance |
| 0071 decision 7, `draining` | from "MI's drain out of our hands" to "the CP's terminate → `terminated`" | the state set and the drain wait |
| 0074 decision 5, "describe → copy → update" | **goes**; a rung is the override type set | the ladder; a stored choice wins |
| 0074 decision 9, "IAM limited to two capacity providers" | **replaced** (decision 10's table) | what cannot be scoped is fenced by tags |
| 0075 decision 3, "two providers per role" | **goes** | the offer format and the `buy` column |
| 0075 decision 4, "the strategy only while running is 0" | **goes** (there is no strategy) | no box swap while running ≥ 1 (decision 5's drain wait) |
| 0075 decision 5, "budget and failure codes" | the budget shrinks to **the registration ceiling**; the codes move to `CreateFleet`'s response | one lap then cooldown / one audit line |
| 0075 decision 6, "interruption" | detection may use `describe-instances` (enumerable) | not a failure / skip after two in a row |
| 0075 decision 11, "the panel answers from the service's strategy" | **from the box's tags** | EC2 is not asked for prices (`describe-instances` is read) |
| 0075 decision 12, "re-application is for the provider / CFN puts the strategy back" | **goes** | — |

**What does NOT move**: 0070 decision 1 (the placement is spelled out — `LaunchType: EC2`
satisfies it), 0045 decision 6 (no MI for Workspaces), 0045 decision 19 (a home-baked AMI is
slow), 0045 decisions 22, 23 and 29 (the pool's invariants — kept from the box side by decision 3),
0074 decisions 1, 2, 3, 6, 7, 10 and 11, 0075 decisions 1, 2, 7, 8, 9 and 10.

## Open questions (in the order they can be measured for $0)

1. 🔴 **Whether CloudFormation updates `CapacityProviderStrategy` → `LaunchType: EC2` in place or
   as a replacement.** The published specification (c) lists "capacity provider → launch type" as
   a valid API transition; how `AWS::ECS::Service` treats it is a separate matter. Measure on a
   throwaway stack (the shape of 0074 open question 1; one service; $0) by **executing** the
   change set (a change set only says `Conditional` — 0074's lesson). Depends on: decision 11.
2. 🔴 **Whether a service placement constraint `memberOf(attribute:af-role == …)` binds on an
   attribute set through `ECS_INSTANCE_ATTRIBUTES`, on the EC2 launch type.** One GPU-less m-class
   box and a task definition with no GPU requirement; under $0.05. Depends on: decisions 2 and 3.
3. 🔴 **The vocabulary of `CreateFleet(instant)`'s `Errors[].ErrorCode`.** With a type above the
   quota (0075 run 4's `g6.4xlarge`), a misspelled type and a type with no stock, what the
   response carries. $0 (nothing launches). Depends on: decisions 1 and 8 (the failure-code table).
4. **Whether the CP can create `AWSServiceRoleForEC2Fleet` itself** (does the limited
   `iam:CreateServiceLinkedRole` go through). $0. Depends on: decision 10.
5. **`awsvpc` on the EC2 launch type: the g6.xlarge ENI limit and the task ENI.** One task per
   box should fit under a limit of 4, but agent settings such as `ECS_AWSVPC_BLOCK_IMDS` are (c).
   Depends on: decision 2. P1's hardware run.
6. **Measured terminate → `terminated` on a GPU** (the length of decision 5's `draining`). P1's
   hardware run.
7. **Resolve the AMI at launch (`resolve:ssm:`) or at stack update (the CloudFormation type).** The
   former tracks the newest; the latter pins a generation per deployment. 0045's slots do the
   latter. Depends on: decision 7.
8. **Mounting the local NVMe.** The AL2023 ECS AMI does not mount the instance store by itself (c).
   If user data mounts it, is it Docker's data-root or the models' host volume? Depends on:
   decision 6. P1.
9. **Whether the ECS agent's Spot draining works on the AL2023 GPU AMI** (c). P2 (the
   interruption run).
10. **Whether `price-capacity-optimized` actually picks a g6** (Managed Instances bought a g6e in
    0075 run 3). P1's hardware run.

## Phases and what "done" means

### P0 — the $0 premises (no code)

Scope: open questions 1, 2, 3 and 4. **If any one is red, this ADR is rewritten decision by
decision** — 1 (the shape of the migration) and 2 (identifying the box) above all.

**Done means**: the four verdicts are appended in the shape of ADR 0075's run 0 (steps, raw
responses, verdict).

### P1 — the image role: implementation and hardware

Scope: decisions 1, 2, 3, 5, 7, 8, 10 and 11 (decision 4's interruption and decision 6's host
volume are not in it).

- CP: replace "buy", "wait for the box" and "read the failure code" in `engine_offer.go` with
  `CreateFleet`. Remove the strategy write and `boxOn()` from `engine_ecs.go`; identify the box by
  attribute and tags. Remove `applyClass` / `UpdateCapacityProvider` from `engine_class.go`. Move
  `isPoolContainerInstance` in `runtime_ecs_ec2.go` to the attribute. Contract A (the table JSON):
  `capacityProvider` / `spotCapacityProvider` → `launchTemplate`.
- CFN: as in the migration section. Rewrite "The capacity providers" and "The offers" in
  PARAMETERS.
- Console: **no change** (contract B is unchanged).

**Done means**:

1. A test can say a deployment declaring no offers makes no additional EC2 call (0074 decision 3
   inherited; positive control).
2. A test can say `CreateFleet`'s `Errors` separate into "next" and "skip this purchase type".
3. A test can say the only route that sets desired to 1 **comes after the box registered** (a
   negative claim — a positive control where removing the guard fails the test).
4. A test can say `isPoolContainerInstance` sorts the three kinds of box (slot, MI, EC2 engine)
   correctly.
5. **On hardware (af-sandbox, the image role, 30 GPU minutes, up to $1)**: the same verdicts as
   0075's runs 3-7, plus **"exactly one box"** (0075 bought two, three times out of three) and
   "the start does not wait for the deployment to settle" (the +2 min 35 s is gone). The type the
   Spot row delivered, the `af-engine-buy` tag, `InstanceLifecycle`.

### P2 — interruption and the host volume

Scope: decisions 4 and 6. 0075 P1's procedure (FIS or `terminate-instances`) is used as written.
**Here `terminate-instances` is not refused by a Managed Instances policy**, so the positive
control is straightforward.

### P3 — the llm role

After P1 and P2 pass for the image role. The difference is one launch template and the `spot`
refusal in `LlmOffers`.

### On who drives the hardware runs

As in 0075 — **a `claude` session drives; one lane, one deployment.** The dev deployment is shared
with other sessions, so confirm nobody is deploying before touching `60-engines`. P0's four items
need only a throwaway stack and read-only APIs, and never touch the deployment.

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
  `…/amazon-linux-2023/recommended/image_id`, resolved by CloudFormation; `MetadataOptions
  HttpTokens: required`; the template's only tag is `af-managed-by`), user data writing
  `ECS_CLUSTER` to join the cluster, the tags `af-pool` / `af-role=slot` / `af-slot-size` **written
  by the CP at `RunInstances`** (not by the template), a registration wait on
  `ListContainerInstances` every 3 s, and departure as `DeregisterContainerInstance(Force)` →
  `TerminateInstances`. Capacity errors go through `isEC2CapacityError` (string match) and on to
  the next AZ. **No Spot anywhere** (`InstanceMarketOptions` / `CreateFleet` appear nowhere in the
  repository). The engine services and the three MI providers already carry the tags `af-pool` /
  `af-role=engine-<role>` (`PropagateTags: SERVICE`), so the vocabulary decision 3 uses is not
  new — what is new is that it lands on an instance the CP can enumerate.
- 🔴 **The slot pool's only way of saying "not my box" is a non-empty `capacityProviderName`**
  (`isPoolContainerInstance`, the intake of 0071's review R7(a)). A box the CP buys on EC2 has an
  **empty** `capacityProviderName`, so that test collapses as it stands. Five EC2-side walks
  (`slotsOfMyType` and through it `freeSlots`, `poolSize`, `sweepFreeSlots`, `makeRoom`,
  `PoolStatus`) filter on **both** `af-pool` and `af-role=slot` and are unaffected. **One does
  not: `sweepSlotOwnerTags` filters on `af-pool` alone** and then writes or strips
  `af-membership` / `af-tenant` on every instance it finds — an engine box tagged `af-pool` would
  be walked by it (decision 3). What mixes on the ECS side is `registeredSlots`,
  `sweepGhostInstances`, `deregisterSlot`, `slotTaskCounts` — four call sites, one function.
- **A Workspace task is pinned to a box by a task-definition placement constraint
  `memberOf(ec2InstanceId == …)`.** The service is `LaunchType: EC2`, `awsvpc`, desired 1.
- **The CP's IAM** (`20-platform.yaml`): Sid `Ec2SlotPool` holds `ec2:RunInstances` /
  `TerminateInstances` / `DescribeInstances` / `CreateTags` and more on `Resource: *` (Describe
  cannot be scoped; tags are the fence); Sid `PassSlotRole` holds `iam:PassRole` on
  `role/af-*-slot` (`iam:PassedToService: ec2.amazonaws.com`); Sid `EcsContainerInstances` holds
  the three container-instance actions. **None of these is conditional — `20-platform.yaml` has
  no `Conditions:` section, so every flavour gets them** (the comment "ecs-ec2 only; harmless on
  Fargate" is prose, not a condition). `ssm:GetParameter` **is** there (Sid `SsmWorkspaceParams`),
  scoped to `parameter/af-ws/*`, so it cannot read the AMI parameter under `/aws/service/`.
  **`ec2:CreateFleet` is nowhere.** The Managed Instances grants (`ecs:DescribeCapacityProviders` /
  `UpdateCapacityProvider` / `PutClusterCapacityProviders`, `iam:PassRole` on `InfraRole` /
  `InstanceRole`) live in **`60-engines.yaml`'s `CpIngestPolicy`**, as unnamed statements attached
  to the CP task role by import — not in 20-platform.
- **The CP's `main` package has no EC2 client.** The engine code's ports are `engineECSAPI`
  (`engine_ecs.go`: DescribeServices / UpdateService / ListContainerInstances /
  DescribeContainerInstances) and `engineCapacityAPI` (`engine_class.go`). The only EC2 port,
  `ec2API`, is unexported in `internal/runtime`, and it is constructed only by the ecs-ec2
  runtime. A `CreateFleet` cannot be added to either engine port as they stand.
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
  parts of `engine_offer.go` (`startOnOffer` / `applyFirstUsableOffer` / `stepOffers` /
  `offerBoxIsUp` / `moveToNextOffer` / `engineOfferVerdict` / `engineEventIsAbout` and the
  run-state clock), the strategy write (`setStrategy` / `writeStrategyOnly`) and the
  provider-named `boxOn()` / `describeBoxes()` in `engine_ecs.go`, `applyEngineClass`
  (`UpdateCapacityProvider`) and `startGate` in `engine_class.go`. `startGate` gates on four
  things in order — no ladder (pass), no candidate after the VRAM filter (refuse), the swap wait
  (the current box's type is not in the first candidate), the rung apply — and only the middle
  two survive here. "One lap then cooldown" lives in the controller (`engine_control.go`), not in
  the gate, and stays.
- **0075 decision 6 (interruption) exists in the code only as `noteReplacement`** — a log line
  and an audit row on `running` → `starting` at desired 1. "Rebuild from the top of the list"
  and "skip an offer interrupted twice in a row" were 0075 P1 and were never reached
  (`engine_offer.go`'s header says so). Decision 4 inherits the design, not an implementation.
- **Contract A (the engine table) is an SSM parameter** (`EnginesParam`, `/af-ws/engines`), whose
  name travels to the CP as an Output/Export and the env var `AF_ENGINES_SSM_PARAM`. No schema and
  no golden pins its field set; what breaks on a rename is the hand-written JSON in
  `engine_gateway_test.go`, `engine_offer_test.go`, `engine_table_reload_test.go` and the emitter
  in `60-engines.yaml`. The three Outputs `Llm` / `Image` / `ImageSpot` `CapacityProviderName` are
  read by six harness scripts (`bench-image-engine`, `probe-fetch-client`, `probe-llm-mount-load`,
  `probe-s3-fetch-tuning`, `probe-s3-mount`, `probe-warm-volume`).
- **`teardown.sh` does not terminate an engine box**: it sets desired 0 and lets Managed
  Instances' drain remove the box while the stacks delete. Slots it terminates itself, by tag.

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
  prioritized`. ⚠️ `prioritized` reads the **`Priority` field of each override**, not the order
  of the list (c) — the CP writes `Priority` 1, 2, … in the declared order. A test pins that the
  priorities follow the declaration.
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
  fleet id. It remembers the `InstanceId` and the tags on the box (decision 3). ⚠️ Whether an
  instant fleet really leaves no record behind is measured in open question 3 (`describe-fleets`
  after the $0 calls); if fleets linger, the CP calls `DeleteFleets(TerminateInstances=false)`
  right after reading the response — `ec2:DeleteFleets` is granted either way (decision 10).

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
- 🔴 **`isPoolContainerInstance` changes from "`capacityProviderName` is empty" to
  "`capacityProviderName` is empty **and** the `af-role` attribute is absent or is `slot`".**
  The provider test stays (a Managed Instances box carries no attribute, so dropping it would
  let an MI box back into the pool — and MI boxes exist until decision 11's migration is done on
  every deployment). This is the successor of 0071 review R7(a)'s test: a Managed Instances box
  (provider name) and an EC2 engine box (attribute) both leave the pool through the same one
  function. The test pins three kinds of container instance (slot, MI engine, EC2 engine); the
  positive control is that removing the attribute makes an engine box count as a slot.
- **The engine's own deregister does not go through `deregisterSlot`** (which applies the pool
  test and would refuse an engine box). The engine runtime deregisters by container-instance ARN
  directly.
- The EC2-side walks already exclude it by `af-pool` + `af-role=slot` (`slotsOfMyType`,
  `poolSize`, `sweepFreeSlots`, `makeRoom`, `PoolStatus`). **`sweepSlotOwnerTags` gains the
  `af-role=slot` filter** it lacks (Background), with a test whose positive control is that
  removing the filter makes the sweep touch an engine-tagged instance. With that, "keeping out of
  `Ec2MaxSlots`" is complete.
- 0070 decision 1's invariant, "one non-Workspace task mixed in breaks the premise", is kept **on
  the box side**: no Workspace task lands on an engine box (a Workspace's constraint names an
  `ec2InstanceId`), and no engine lands on a Workspace box (decision 2's attribute).

🔁 **What would change this**: if `ECS_INSTANCE_ATTRIBUTES` is not honoured by the agent on the
AL2023 GPU AMI (open question 2), the CP calls `PutAttributes` right after registration.

### 4. An interruption is read as an EC2 fact. The rebuild starts from the top of the list, synchronously

- 0075 decision 6's detection (`running` → `starting` at desired 1, and the box gone) is inherited
  **as a design**: in the code it exists only as `noteReplacement`'s log and audit row; the rebuild
  and the skip were 0075 P1 and were never written (Background). P2 builds them here, once.
  "The box is gone" can be stated from **`describe-instances` state (`shutting-down` /
  `terminated`) and from no box tagged `af-role=engine-<role>` being `running`**. The Managed
  Instances "cannot enumerate" limit does not apply.
- ECS deregisters a terminated instance's container instance by itself (c). If it does not, the
  ghost is a registered container instance with the engine attribute and no EC2 behind it — the
  pool's `sweepGhostInstances` no longer sees it (decision 3), so decision 5's sweep covers it.
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
  29, audited, in two directions: (a) an EC2 instance tagged `af-role=engine-*` and `running`,
  with **zero running and pending tasks** on its container instance (read from
  `DescribeContainerInstances`, the way `sweepFreeSlots` / `makeRoom` read `slotTaskCounts`),
  registered longer ago than `ghostAfter`, and with no start in flight for its role — terminated;
  (b) a container instance carrying the engine attribute whose EC2 instance is gone —
  deregistered (the shape of 0045's `sweepGhostInstances`, which checks registration age and
  "instance gone", not task counts; the zero-task caution belongs to the free-slot sweeps).
  Neither direction consults the CP's memory: tags and attributes are enough, as 0045 decision 29
  demands.

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
  Instances fee is gone. Writing the Cost Explorer figure stays the rule. The two places that
  say "inclusive of the fee" (`engine_class.go`'s field comment, PARAMETERS "The offers") change
  with it.

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
| `iam:PassRole` on `role/af-*-engine` (`iam:PassedToService: ec2.amazonaws.com`), as its own statement in the shape of `PassSlotRole` | `iam:PassRole` on `InfraRole` / `InstanceRole` |
| `iam:CreateServiceLinkedRole` with `iam:AWSServiceName` in `[spot.amazonaws.com, ec2fleet.amazonaws.com]` (the second is what creates `AWSServiceRoleForEC2Fleet`) | `InfraRole` itself |
| `ssm:GetParameters` on `parameter/aws/service/ecs/optimized-ami/*` **only if open question 3 says the caller of `CreateFleet` must hold it** for `resolve:ssm:` (c). The CP's existing `ssm:GetParameter` is scoped to `/af-ws/*` and cannot read it | |

`ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` / `CreateTags` are already in
`Ec2SlotPool`; `ecs:ListContainerInstances` / `DescribeContainerInstances` /
`DeregisterContainerInstance` already in `EcsContainerInstances`; **both Sids are unconditional
in `20-platform.yaml` and every flavour has them** (Background), so 60-engines does not repeat
them. What 60-engines edits is its own `CpIngestPolicy`: the Managed Instances statements leave
it, the Fleet statements enter it. The full grant set is **an output of open question 3**: the
$0 calls start with this table, and whatever `AccessDenied` names (the published example policy
for EC2 Fleet grants `ec2:*`, which says nothing about the minimum) is added and recorded.

⚠️ **On an account without `AWSServiceRoleForEC2Fleet`, the first `CreateFleet` fails** (c).
af-sandbox has none (b). The deployment steps (`standup.sh`) run `create-service-linked-role
--aws-service-name ec2fleet.amazonaws.com` once, `|| true` (the same column as
`AWSServiceRoleForEC2Spot`, created by hand in 0074; today `standup.sh` does this only for
`ecs.amazonaws.com`). Not a CloudFormation `AWS::IAM::ServiceLinkedRole` — a role that already
exists fails the stack.

🔁 **What would change this**: if a condition key scoping `CreateFleet` to the launch template
ARN (`ec2:LaunchTemplate`) works on real hardware, `Resource: *` is narrowed.

### 11. Migration: remove the Managed Instances resources, add launch templates and an instance role. Two steps, in order

- Removed from `60-engines.yaml`: the three capacity providers, `Associations`' provider entries,
  `InfraRole`, the MI `InstanceRole` / `InstanceProfile`, the three Outputs
  `LlmCapacityProviderName` / `ImageCapacityProviderName` / `ImageSpotCapacityProviderName`
  (replaced by `LlmLaunchTemplateId` / `ImageLaunchTemplateId`; the six harness scripts that read
  them — Background — move to the new names or are marked as Managed Instances-era), and the
  parameters `*AllowedInstanceTypes` / `*AcceleratorMemMinMiB` / `*VCpuMin` / `*VCpuMax` /
  `*MemMinMiB` / `*MemMaxMiB` / `*UseLocalStorage` / `*ScaleInAfter` (the requirements are
  **already** in the offer list: 0074 decision 1 made a rung "a set of requirements" and 0075
  added `buy`; the MI four fields were a copy of the ladder). ⚠️ **Named exactly, not by glob**:
  `*TaskMemory` and `*GpuCount` also match `*Mem*` / the MI block and **stay** — they feed the
  task definition.
- **`*StorageGiB` stays and changes meaning**: from the MI provider's storage requirement to the
  launch template's **root gp3 size**. The anonymous `Host: {}` volume lands on the root
  filesystem, and the ECS-optimized AMI's default root is 30 GiB (c) — a comfy deployment holding
  30 GB of models would fill it. Same default as today; the name is kept so `params/60-engines`
  captures carry over.
- Added: one launch template per role (AMI, instance profile, `EngineSg`, user data,
  `MetadataOptions HttpTokens: required`, root gp3 of `*StorageGiB`, the tag `af-managed-by`;
  the CP writes the rest of the tags at `CreateFleet`, as the slot pool does at `RunInstances`),
  `EngineInstanceRole` (`service-role/AmazonEC2ContainerServiceforEC2Role` +
  `AmazonSSMManagedInstanceCore`, the slot role's pair), `EngineInstanceProfile`. In the engine
  table JSON, `capacityProvider` / `spotCapacityProvider` become `launchTemplate` (id); `offers` /
  `offerBudgetSec` / `classes` are unchanged. `CpIngestPolicy` swaps its Managed Instances
  statements for the Fleet ones (decision 10).
- 🔴 **Order**: (1) **with not one box standing** (both roles `mode: off`, no engine box among
  the container instances), (2) apply this template. Deleting a provider goes through when it has
  no box (measured on 0074 open question 1's throwaway: `delete-capacity-provider` at zero boxes
  is `INACTIVE` at once). Whether the service's `CapacityProviderStrategy` → `LaunchType` is a
  replacement in CloudFormation is open question 1. ⚠️ **Both services carry an explicit
  `ServiceName`** (`af-<stack>-llm` / `-image`) and `ServiceRegistries`. If it is a replacement,
  CloudFormation creates the new service **before** deleting the old one, and the explicit name
  collides — the update fails and rolls back. Then the migration is the path that already
  exists: `<Role>Enabled=false` (the condition deletes the service; `update.sh` warns about
  exactly this) → apply → `<Role>Enabled=true`. The Cloud Map name is gone in between; at
  desired 0 no request is lost. The services keep `DependsOn: Associations` (it stays).
- Removing the providers from `Associations`: the cluster's provider list is **owned by
  60-engines**, so someone must still hold FARGATE / FARGATE_SPOT (`50-tts` uses `FARGATE_SPOT`
  through its strategy and declares no `Associations` of its own, 0070). **`Associations`
  stays, with `[FARGATE, FARGATE_SPOT]`** (only the providers leave). A deployment without the
  engines stack has no `Associations` at all and 50-tts leans on the cluster's default list —
  unchanged by this ADR, stated so nobody "cleans up" the resource.
- 0075's hardware run 1 (the two-step migration of a SPOT stack) is no longer needed — the
  providers go entirely, so there is no name to collide with.
- Captured `*AllowedInstanceTypes` and friends in `params/60-engines` are dropped with
  `af_param_drop` (defined in `env.sh`, called from `standup.sh`; the same column as 0075's
  `ImageCapacityOptionType`). `update.sh` passes **one** parameter set to 60-engines —
  `<Role>Enabled=true` read back from the live stack, the only path that repairs it — and
  `cloudformation deploy` keeps every unnamed parameter at its previous value, so retired ones
  fall away silently as long as that block is not touched.
- **`teardown.sh` terminates engine boxes by tag** (`af-pool` + `af-role=engine-*`) the way it
  terminates slots, before the stacks go — the CP that would have done it is stopped first
  (step 1), and Managed Instances' drain no longer does it for us.
- Template size: three providers (about 3 KB) and the MI parameter block leave, two launch
  templates arrive — **expected to shrink**, but `wc -c` before and after (wall 51,200; now 42,500;
  the CI case 3b-2 in `ecs-lifecycle-stub-test.sh` fails the build past the wall).

🔁 **What would change this**: if open question 1 says "the service must be recreated", the
migration is the `<Role>Enabled` round trip above, and the Cloud Map name is gone for some
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
   change set (a change set only says `Conditional` — 0074's lesson). ⚠️ The throwaway service
   must carry an **explicit `ServiceName`** and a `ServiceRegistries` entry, as the real ones do
   — a replacement that succeeds on an anonymous service says nothing about ours (decision 11).
   Depends on: decision 11.
2. 🔴 **Whether a service placement constraint `memberOf(attribute:af-role == …)` binds on an
   attribute set through `ECS_INSTANCE_ATTRIBUTES`, on the EC2 launch type.** One GPU-less m-class
   box and a task definition with no GPU requirement; under $0.05. Depends on: decisions 2 and 3.
3. 🔴 **The vocabulary of `CreateFleet(instant)`'s `Errors[].ErrorCode`.** With a type above the
   quota (0075 run 4's `g6.4xlarge`), a misspelled type and a type with no stock, what the
   response carries. $0 (nothing launches). Three things ride on the same calls: (a) **the IAM
   minimum** — the calls run under decision 10's table and whatever `AccessDenied` names is
   added (in particular whether `resolve:ssm:` in the launch template needs `ssm:GetParameters`
   on the public parameter from the caller); (b) **whether an instant fleet that launched nothing
   lingers** in `describe-fleets` (decision 1 remembers no fleet id on the strength of (c));
   (c) that `prioritized` honours the override `Priority` (visible in the fleet's recorded
   config). Depends on: decisions 1, 8 (the failure-code table) and 10.
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

- CP: **a new EC2 port in package `main`** (`engineFleetAPI`: `CreateFleet`, `DescribeInstances`,
  `TerminateInstances`, `CreateTags`, and `DeleteFleets` if open question 3 (b) says so) with a
  fake, constructed on **every** flavour (today only the ecs-ec2 runtime builds an EC2 client;
  the engine code has none — Background). Replace "buy", "wait for the box" and "read the failure
  code" in `engine_offer.go` with one `CreateFleet` per offer. Remove `setStrategy` /
  `writeStrategyOnly` and the provider-named `boxOn()` / `describeBoxes()` from `engine_ecs.go`;
  identify the box by attribute and tags. Remove `applyEngineClass` / `engineCapacityAPI` from
  `engine_class.go`; `startGate` keeps the no-candidate refusal and the swap wait. In
  `runtime_ecs_ec2.go`: `isPoolContainerInstance` gains the attribute clause,
  `sweepSlotOwnerTags` gains the role filter. Contract A (the table JSON): `capacityProvider` /
  `spotCapacityProvider` → `launchTemplate` (the JSON literals in `engine_gateway_test.go`,
  `engine_offer_test.go`, `engine_table_reload_test.go` follow). Decision 5's sweep, both
  directions.
- CFN: as in the migration section, including the Outputs, `CpIngestPolicy`, `standup.sh`'s
  service-linked role and `af_param_drop` column, `teardown.sh`'s engine-box terminate, and the
  six harness scripts. Rewrite "The capacity providers" and "The offers" in PARAMETERS.
- Console: **no change** (contract B is unchanged).

**Done means**:

1. A test can say a deployment declaring no offers makes no additional EC2 call (0074 decision 3
   inherited; positive control).
2. A test can say `CreateFleet`'s `Errors` separate into "next" and "skip this purchase type".
3. A test can say the only route that sets desired to 1 **comes after the box registered** (a
   negative claim — a positive control where removing the guard fails the test).
4. A test can say `isPoolContainerInstance` sorts the three kinds of box (slot, MI, EC2 engine)
   correctly.
5. A test can say `sweepSlotOwnerTags` leaves an instance tagged `af-role=engine-*` alone
   (positive control: removing the filter makes it write).
6. A test can say an `od` offer's overrides carry `Priority` in the declared order.
7. **On hardware (af-sandbox, the image role, 30 GPU minutes, up to $1)**: the same verdicts as
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

## Review (2026-09-12, before P0)

In the manner of ADR 0075's review, the chain "decision → grounds → current state" was checked
against the code, the templates and the cited ADRs. Verdict: **approved (P0 may start). Two
statements of the current state were wrong and one decision stood on them (R1, R2), one EC2-side
walk the text called "unaffected" is affected (R3), two decisions inherited things that do not
exist in the code (R4, R5), the migration had four gaps (R6-R9), and three details of decision 1
were imprecise (R10-R12). The text above has been corrected accordingly** — this ADR is still
"proposed" with not one line implemented, so the correction is the "implementation corrects the
text" stage brought forward, with what changed and why kept here. **Nothing was measured for
this section**; re-reading the code and the templates was enough. Every cross-reference (0045
decisions 6, 19, 22, 23, 29; 0070 decision 1; 0071 decisions 1, 2, 5, 7; 0074 decisions 5, 9;
0075 decisions 1-12 and the three hardware follow-ups) was found where the text says.

### What the review checked

- **R1. "A deployment without the slot pool lacks `Ec2SlotPool` / `EcsContainerInstances`" was
  wrong.** `20-platform.yaml` has no `Conditions:` section; both Sids are on the CP task role on
  every flavour. Decision 10's closing sentence ("60-engines carries them in the same shape")
  drew a conclusion from a false premise; the grant now goes into 60-engines' own
  `CpIngestPolicy`, where the Managed Instances statements it replaces already live (unnamed —
  they are not in 20-platform either). `iam:PassRole` on the slot role is its own Sid
  (`PassSlotRole`), and the engine one follows that shape.
- **R2. "`ssm:GetParameter` is not there" was wrong.** Sid `SsmWorkspaceParams` grants it, scoped
  to `parameter/af-ws/*`. What is true is that the AMI parameter under `/aws/service/` is out of
  that scope. Whether the caller of `CreateFleet` must hold `ssm:GetParameters` for a
  `resolve:ssm:` launch template is (c), and decision 10 had asserted the answer; it is now part
  of open question 3, together with the rest of the IAM minimum (an `AccessDenied` is $0).
- **R3. `sweepSlotOwnerTags` filters on `af-pool` alone.** Five EC2-side walks pair `af-pool`
  with `af-role=slot`; this one does not, and it writes `af-membership` / `af-tenant` on what it
  finds. An engine box tagged `af-pool` (decision 3) would be relabelled by it. Decision 3 gives
  it the role filter, with a test (done item 5). Also corrected: the slot tags come from the CP
  at `RunInstances`, not from `40-ec2-pool.yaml` (whose only tag is `af-managed-by`), and the
  engine services and providers already carry `af-pool` / `af-role=engine-<role>`.
- **R4. Decision 4 "inherits" an interruption handling that was never written.** 0075 decision
  6 exists in the code as `noteReplacement` only (a log line and an audit row); the rebuild from
  the top and the two-in-a-row skip were 0075 P1 and were not reached — `engine_offer.go`'s own
  header says so. The decision now says it inherits the design, and P2 builds it once.
- **R5. `isPoolContainerInstance`'s new rule dropped the provider test.** "Changes from A to B"
  would let a Managed Instances box (no attribute) back into the pool, contradicting the same
  paragraph's "both leave through the same function" and the three-kind test. The rule is now
  "A and B". The same paragraph gained: the engine's deregister does not reuse `deregisterSlot`
  (it applies the pool test), and decision 5's sweep runs in both directions — an EC2 box with
  no tasks, and a container instance with no EC2 — since the pool's `sweepGhostInstances` will no
  longer see engine ghosts. The attribution "zero tasks, the caution of `sweepGhostInstances`"
  was wrong too: that sweep checks registration age and "instance gone"; the task-count caution
  is `sweepFreeSlots` / `makeRoom`'s.
- **R6. Both engine services have an explicit `ServiceName`.** If open question 1 answers
  "replacement", CloudFormation creates before it deletes and the name collides; the update
  rolls back. The migration then uses the `<Role>Enabled=false` → apply → `true` round trip that
  already exists (the condition deletes the service — `update.sh` warns about exactly this). The
  throwaway in open question 1 must carry an explicit name and a `ServiceRegistries` entry, or
  it measures a different service.
- **R7. "`update.sh` passes no parameters" was wrong.** For 60-engines it passes
  `<Role>Enabled=true` read back from the live stack — the only path that repairs that value.
  The mechanism the ADR relied on (`cloudformation deploy` keeps unnamed parameters) is right;
  the sentence was not. `af_param_drop` is defined in `env.sh`.
- **R8. The removal list was a glob that caught what must stay.** `*Mem*` matches `*TaskMemory`,
  and the MI block's neighbour `*GpuCount` feeds the task definition. The list now names the
  parameters. `*StorageGiB` stays and becomes the root volume size — the anonymous model volume
  lands on the root filesystem, and a 30 GiB default root (c) does not hold a comfy deployment's
  models. The three `*CapacityProviderName` Outputs go with the providers, and six harness
  scripts read them; `teardown.sh` relied on Managed Instances' drain to remove the box and now
  terminates by tag, as it does slots.
- **R9. The CP's `main` package has no EC2 client.** The engine ports are ECS-only
  (`engineECSAPI`, `engineCapacityAPI`); the one EC2 port is unexported in `internal/runtime`
  and built only by the ecs-ec2 runtime. P1 adds an `engineFleetAPI` with a fake, on every
  flavour. Contract A has no schema or golden; the JSON literals in three test files are the
  de-facto pin.
- **R10. `prioritized` reads the override `Priority`, not the list order.** Decision 1 said
  "the order of the overrides = the declared order"; the CP now writes `Priority` explicitly
  (done item 6).
- **R11. "An instant fleet deletes itself" is (c) and decision 1 leaned on it** ("the CP does not
  remember the fleet id"). Open question 3 now records `describe-fleets` after the $0 calls; if
  fleets linger, `DeleteFleets` follows each response.
- **R12. The service-linked role's service name.** `AWSServiceRoleForEC2Fleet` is created for
  `ec2fleet.amazonaws.com`, not `spot.amazonaws.com`; the condition key is `iam:AWSServiceName`.
  `standup.sh` today runs `create-service-linked-role` only for `ecs.amazonaws.com`. Not a
  CloudFormation resource — an existing role fails the stack.

### Per-decision revisions (already applied above)

| Decision | What changed | Why |
|---|---|---|
| Background (code) | slot tags come from the CP / `sweepSlotOwnerTags` / the three Sids and `SsmWorkspaceParams` as they are / MI grants live in `CpIngestPolicy` / no EC2 client in `main` / 0075 decision 6 is `noteReplacement` only / `startGate`'s four gates / contract A's home and pins / `teardown.sh` | R1, R2, R3, R4, R7, R8, R9 |
| 1 | `Priority` written explicitly / fleet lingering measured, `DeleteFleets` if so | R10, R11 |
| 3 | provider test kept ("A and B") / engine deregister not through `deregisterSlot` / `sweepSlotOwnerTags` gains the role filter | R3, R5 |
| 4 | "inherited as a design; P2 builds it" / ECS's own deregistration as (c) with the sweep behind it | R4, R5 |
| 5 | the sweep in two directions, task counts from `DescribeContainerInstances`, no memory consulted | R5 |
| 8 | the two "inclusive of the fee" texts change with `usdPerHour` | — |
| 10 | `CpIngestPolicy`, not a repeat of the two Sids / `ssm:GetParameters` conditional on open question 3 / `ec2fleet.amazonaws.com` / IAM minimum as P0 output / `standup.sh`, not a CFN resource | R1, R2, R12 |
| 11 | parameters named, `*StorageGiB` re-meant / Outputs and harness scripts / explicit `ServiceName` and the `<Role>Enabled` round trip / `update.sh` as it is / `teardown.sh` / 50-tts's dependence on `Associations` stated | R6, R7, R8 |
| Open questions | 1: the throwaway carries a name and a registry / 3: IAM minimum, fleet lingering, `Priority` | R2, R6, R10, R11 |
| P1 | `engineFleetAPI` on every flavour / the exact functions / done items 5 and 6 | R3, R9, R10 |

### How the implementation splits

Contract B (CP → Console) does not move, so there is no Console lane. Contract A (template → CP)
changes one field (`capacityProvider` / `spotCapacityProvider` → `launchTemplate`), and that is
the seam between the CP and CFN lanes. Four lanes:

- **P0 hardware ($0, first, and alone).** Open questions 1-4 on af-sandbox with a throwaway
  stack and the read-only APIs. **If 1 or 2 is red the other lanes do not start.** Its output is
  four verdicts in the shape of 0075's run 0, plus the IAM minimum and the fleet-lingering answer
  from open question 3. A `claude` session drives it.
- **CP (Go).** Everything under P1's CP bullet, behind the fake `engineFleetAPI`; done items 1-6.
  It may start on the day P0's 2 and 3 are green, on the strength of the fake, and takes P0's
  vocabulary for the failure-code table when it arrives.
- **CFN + scripts.** Everything under P1's CFN bullet, PARAMETERS, `standup.sh` / `update.sh` /
  `teardown.sh` / the harness scripts, and the appendices on 0071, 0074 and 0075 ("Appendix — ADR
  0077 overrode this decision"). It may start on the day P0's 1 is green (the migration's shape
  depends on it).
- **P1 hardware.** Serial, one lane, one deployment, after CP and CFN have landed: done item 7.
  The dev deployment is shared; `pgrep -af dev-deploy.sh` before touching 60-engines.

P2 (decisions 4 and 6) and P3 (the llm role) follow P1's hardware verdict and are not split
further here.

## Follow-up — what P1's CFN lane handed back to the text (2026-09-12, PR #575)

The CFN lane implemented decision 11 as written (template, scripts, PARAMETERS, the appendices on
0071 / 0074 / 0075). Nothing was deployed and nothing was measured; one AWS call was made,
`validate-template`, which is a read. Four things the implementation says back to this text. **No
decision above is edited** — this section is the record, in the shape of 0075's follow-ups.

1. 🔴 **Retiring `<Role>UseLocalStorage` costs a measured cold start, and decision 11 does not say
   so.** The removal list calls the eight parameters "a copy of the ladder", and seven of them are.
   That one is not: it was the only way to ask for the **instance store** instead of an EBS data
   volume, and turning it on was measured (2026-09-09, ADR 0071's own numbers) at S3 -> disk 1.4x,
   disk -> VRAM **2.9x**, RunTask -> model loaded 527-586 s -> **275 s**, a model swap 276-282 s ->
   **98.5 s**. The launch template this lane wrote does **not** mount the NVMe (open question 8 is
   P1), so a start now runs both halves on EBS bandwidth. The parameter still had to go — there is
   no Managed Instances provider left to ask — but the cost is real and is recorded in
   PARAMETERS under `LlmStorageGiB`. **Open question 8 is not optional work; it is a regression to
   close.**
2. **Decision 10's `iam:PassRole` on `role/af-*-engine` is a convention the template does not
   need.** 20-platform names the slot role by convention to avoid a circular stack import; the
   engine instance role is created in 60-engines itself, so the statement uses
   `!GetAtt EngineInstanceRole.Arn` — the same shape, one resource tighter. (The role is still
   named `af-<stack>-engine`, which the convention would also have matched. `EngineTaskRole` is
   `af-<stack>-engine-task` and would not have been.)
3. **`teardown.sh` already terminated an engine box; what was missing was the words.** Decision 11
   asks for a terminate "by tag `af-pool` + `af-role=engine-*`, the way it terminates slots".
   `list_slots` filters on `af-pool` **alone** — the same one-filter shape review R3 found as a bug
   in `sweepSlotOwnerTags` — so a CP-bought engine box is already in the list step 3 terminates.
   Only a Managed Instances box was invisible there (an AWS-managed account, not enumerable), which
   is why the step read "slots". The lane renamed the step, counted engine boxes separately and
   said why; it did not add a second terminate pass.
4. **The launch templates are created unconditionally**, as the capacity providers were. Decision
   11 does not say, and `<Role>Enabled` would be the obvious condition — but `check-cfn-exports.py`
   fails a template that can export an empty string, and the two `*LaunchTemplateId` Outputs are
   exports. A launch template costs nothing while nothing launches from it.

One more thing worth stating because a later reader will look for it: the **six harness scripts
went both ways at once**. Decision 11 offers "move to the new names **or** mark them Managed
Instances-era"; they were mechanically moved (task definition `EC2`, `run-task` with the launch
type and `attribute:af-role == engine-<role>`) **and** gated with an `exit 2` at the top, because
the move cannot be verified at $0 and every number in their headers is a Managed Instances
measurement. Deleting the gate is part of re-measuring, not part of reading.

### What P0 changed in this lane (2026-09-12, after PR #576)

P0's verdicts landed while this lane's PR was open, and four of them are the CFN lane's to carry.
What was pushed on top of the branch:

1. **The migration is the `<Role>Enabled` round trip, and only that** (open question 1 is 🔴).
   PARAMETERS carried both shapes with "delete the other when the answer lands"; the in-place
   shape is deleted, the round trip keeps its measured 25 s + 48 s, and the `AlreadyExists`
   message is quoted so the failure is recognisable if somebody tries it the other way round.
   ✅ **And the Cloud Map name does not disappear during it** — the `AWS::ServiceDiscovery::Service`
   is a separate resource with no condition, measured keeping the same registry ARN. Decision 11's
   "the Cloud Map name is gone in between" is the one sentence of this ADR that P0 corrected, and
   PARAMETERS now says so where an operator reads it.
2. **`ssm:GetParameters` on `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*` is
   in `CpIngestPolicy`, unconditionally.** Decision 10's conditional row is answered: the CALLER
   of `CreateFleet` resolves `resolve:ssm:`, and without the grant the call fails top-level with
   `SsmAccessDenied` naming neither action nor parameter. The two grants P0 proved unnecessary —
   `ec2:DescribeLaunchTemplates(Versions)` and `ec2:CreateTags` (the request's `TagSpecifications`
   merge with the template's) — were never added and are now recorded as "do not add".
3. **The service-linked-role claim is weakened, not removed.** Decision 10's ⚠️ said the first
   `CreateFleet` fails without `AWSServiceRoleForEC2Fleet`; P0 put three calls through without it.
   `standup.sh` still creates it — cheap insurance, and a *launching* call on an account with
   neither SLR was not measured — but the template comment, `standup.sh`, the README prerequisite
   and PARAMETERS no longer assert a failure. `standup.sh`'s comment also records that a second
   create answers `InvalidInput`, not `EntityAlreadyExists`, which is why the `|| true` cannot be
   replaced by a code match.
4. **Open question 8 is implemented, unverified.** Follow-up item 1 above called the loss of
   `<Role>UseLocalStorage` a regression to close, and it is closed in the launch template rather
   than left to P1's report: the user data mounts the first instance-store NVMe and puts **Docker's
   data-root** on it, which carries the models because an anonymous `host` volume IS a Docker
   volume — no task-definition change. An EBS-only type matches nothing and keeps the root volume;
   the AMI's cached agent and pause images are copied across first; every step is chained so a
   failure leaves Docker where it was. 🔴 **Not measured**: the first P1 run checks
   `df /var/lib/docker` and `docker info | grep "Docker Root Dir"` on the box, because the symptom
   of a silent failure here is only "the start is slow".

Template size after all of it: **40,182 bytes** (was 36,816 in the first push; the wall is 51,200).

### And one from the CP lane (PR #577): the budget's meaning and default

`<Role>OfferBudgetSec` is the only contract-A field besides `launchTemplate` that this ADR moves,
and the CP lane settled it: it bounds **the ECS registration wait for the box that was bought**,
not a per-offer purchase clock — the purchase answers in the call — and the Control Plane's
default is **300 s**, decision 1's number. So the template's `Default` is 300 on both roles, and
the two `Description` lines say what it now bounds.

🔴 **A capture from 0.19.0 carries `180`.** That is the old meaning's default, and left alone it
would silently make the new ceiling shorter than the number the ADR chose. `standup.sh` drops
**exactly 180** and lets the template's 300 stand, printing what it did; any other value is
treated as an operator's choice and passed on. A stale default and a deliberate 180 cannot be
told apart, which is the whole reason the rule is "the old default, and nothing else". The stub
test pins both directions, each with its own positive control.

## Follow-up — what P1's CP lane handed back to the text (2026-09-12, PR #577)

The CP side of P1 (decisions 1, 2, 3, 5, 8, 9 and 11; the CP end of contract A) landed as #577.
Done items 1-6 are pinned in `engine_offer_test.go` and
`internal/runtime/runtime_ecs_ec2_engine_test.go`, each with its positive control — including two
mutations run by hand and reverted: removing the `registered()` guard makes done item 3 fail, and
removing the `af-role` filter makes done item 5 fail. Done item 7 is the hardware lane and is not
in this. What the implementation handed back:

- **The EC2 port is three calls, not five.** P0 measured `CreateTags` to be unnecessary —
  `CreateFleet`'s `TagSpecifications` (ResourceType `instance`) merges with the launch template's
  own tags and lands at launch — and `DeleteFleets` to be unusable: decision 1's
  `DeleteFleets(TerminateInstances=false)` is refused for an instant fleet with
  `NoTerminateInstancesNotSupported`, and a fleet that launched nothing does not appear in an
  unfiltered `describe-fleets` either. Decision 1's "the CP does not remember the fleet id" stands
  as written; the two calls behind it are gone. The port is `CreateFleet` / `DescribeInstances` /
  `TerminateInstances`.
- 🔴 **The failure-code table has a FOURTH answer, and it is the one decision 8 did not
  anticipate: `refused`.** A missing `iam:PassRole` comes back as `UnauthorizedOperation`
  **inside a 200**, in `Errors[]`, in the same shape and the same place as "there was no stock"
  (P0). Read as "next", it walks the whole offer list against a deployment that cannot launch
  anything — once per demand, for ever, with nothing in the log naming the grant. So it stops the
  walk where it is, counts as ONE failed start, and logs the message verbatim; the top-level
  `SsmAccessDenied` is folded into the same answer. A test pins that it is not "next", with the
  identical fixture answering `InsufficientInstanceCapacity` as its positive control.
- **`InvalidFleetConfiguration` is read POSITIONALLY, not semantically.** `Errors[]` carries one
  entry per override, and P0 could not tell a misspelt instance type from a type that is simply
  not offered in that Availability Zone — both produce that code and that message. So the row is
  `unusable` only when EVERY override said it; a minority means the other overrides are still
  worth asking for, and the walk takes the next row instead.
- **`<role>OfferBudgetSec`'s default moves from 180 to 300 seconds** with its meaning (decision 1).
  ⚠️ **A deployment that wrote 180 for the old meaning would give a box 180 seconds to register**,
  which is inside ADR 0045 decision 22's measurement (21 s, 77 s with a home-baked AMI) but leaves
  little room on a slow boot. The CFN lane settled the other half of this independently and the
  two agree: `standup.sh` drops a captured **180 and nothing else**, so the template's 300 stands
  (its own follow-up, above).
- **One field leaves contract B: `class_apply_error`.** It reported "the rung was stored and the
  capacity provider refused it", which cannot happen once the declaration IS the request
  (decision 8): a stored choice is in force the moment it is written. The Console reads the field
  only when present and shows no retry banner without it, so decision 8's "the Console does not
  change" holds — but contract B is "unchanged minus one field that can no longer occur", not
  "unchanged".
- **Decision 3's new filter on `sweepSlotOwnerTags` is `af-role ∈ {slot, quarantined}`, not
  `= slot`.** A quarantined box can still carry a person's `af-membership` (it is stopped, not
  released), and repairing exactly that is what the sweep exists for; narrowing to `slot` alone
  would leave a stale owner tag billing somebody for a box they do not have — the same defect the
  filter was added to prevent, in the other direction.
- **`draining` is answered from EC2 through a function handed to the ECS adapter.** Decision 5
  gives the terminate to the CP and orders it deregister-then-terminate, which means ECS knows
  nothing about the box during the very window the state names. `engineECS` therefore holds no
  EC2 client and no tags: it is given `fleet.live` when the offers are wired, and a Fargate engine
  is given nothing and pays for nothing.
- **Decision 5's sweep needs TWO graces, because the CP has no `ghostAfter` of its own** (that is
  the slot pool's configuration, in another package). At desired 0 a box with no task is ended
  after 2 minutes — that is the ordinary departure, one tick after the task goes — and while the
  service is up a second box with no task is ended after 15 minutes, which is the "two boxes"
  leak of ADR 0075 without ever touching a task that is merely still being placed.
- **Decision 9's refusal is keyed by the ROLE at parse time** (`parseEngineOffers(key, spec)`), so
  it is one function and not a condition at each call site; the reloader uses the same one.
- **A start already in flight is a fourth gate on `startGate`.** Decision 8 leaves the gate with
  the no-candidate refusal and the swap wait; a third was needed. The admin toggle calls the gate
  itself, so without this the toggle's own call would begin the walk again while a box was
  registering and buy a second one — the failure ADR 0075 produced three times out of three.
- **Contract A's final shape, for the CFN lane**: `launchTemplate` (a launch template id `lt-…`
  or a name) replaces `capacityProvider` and `spotCapacityProvider`, which are **ignored** wherever
  a stack still emits them. `offers`, `offerBudgetSec`, `classes` and every other field are
  unchanged. A row with no `launchTemplate` buys no box and recognises none: it keeps serving and
  starts the engine the plain way, which is what a CP upgraded before its stack does.

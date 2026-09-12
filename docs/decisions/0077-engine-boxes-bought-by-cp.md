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
  of the list — the CP writes `Priority` 1, 2, … in the declared order. A test pins that the
  priorities follow the declaration. **Measured (P0 open question 3)**: the priority is honoured
  and recorded in the fleet's configuration.
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
  fleet id. It remembers the `InstanceId` and the tags on the box (decision 3). **Measured (P0
  open question 3)**: an instant fleet does linger, but it is invisible to `describe-fleets` unless
  named by id, and `DeleteFleets(TerminateInstances=false)` is refused for an instant fleet
  (`NoTerminateInstancesNotSupported`). So there is **no follow-up call**: the port is
  `CreateFleet` / `DescribeInstances` / `TerminateInstances`, and nothing accumulates in any list
  the CP reads.

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
  role filter** it lacks (Background) — `af-role` in `{slot, quarantined}`, not `slot` alone: a
  quarantined box is stopped, not released, and can still carry a person's `af-membership` that
  this sweep exists to repair (the CP lane's finding, #577). The test's positive control is that
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
  older than a grace and with no start in flight for its role — terminated. The CP has no
  `ghostAfter` of its own (that is the slot pool's, in another package), so the grace is **two
  minutes at desired 0** (the ordinary departure, one tick after the task goes) and **fifteen
  minutes while the service is up** (0075's "two boxes" leak, without touching a task still being
  placed) — the CP lane's shape, #577;
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
  strings to **`CreateFleet`'s `Errors[].ErrorCode`** — one code per override, inside an HTTP 200
  (measured, P0 open question 3): `MaxSpotInstanceCountExceeded` / `VcpuLimitExceeded` → skip that
  purchase type; `InvalidFleetConfiguration` → `unusable` **only when every override says it**
  (a misspelled type and "not offered in this AZ" are the same sentence, so one override saying
  it is a capacity fact, not a typo); `SpotMaxPriceTooLow` / `InsufficientInstanceCapacity` →
  next; unknown → next, logged. 🔴 **And a class the service-event table never had: an
  authorization failure is shaped exactly like "no capacity"** — `UnauthorizedOperation` arrives
  per override inside the same array (measured with `iam:PassRole` removed), and `SsmAccessDenied`
  top-level. Both **stop the lap**, count as a failure and log the sentence verbatim; treating
  them as "next" would walk the whole list and report "no capacity anywhere" on a misconfigured
  deployment. `--dry-run` does not exercise per-override authorization and is no pre-flight.
  **"Filter events by offer" (#564) is not needed** — a response contains only its own call.
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
| `ec2:CreateFleet` (`Resource: *`; a fleet cannot be resource-scoped). `ec2:DescribeFleets` / `ec2:DeleteFleets` are **never called** (decision 1, measured); #575 grants them anyway, harmless | `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` |
| `ec2:CreateLaunchTemplateVersion` is **not** added (CloudFormation owns the template) | `ecs:PutClusterCapacityProviders` |
| `iam:PassRole` on `role/af-*-engine` (`iam:PassedToService: ec2.amazonaws.com`), as its own statement in the shape of `PassSlotRole` | `iam:PassRole` on `InfraRole` / `InstanceRole` |
| `iam:CreateServiceLinkedRole` with `iam:AWSServiceName` in `[spot.amazonaws.com, ec2fleet.amazonaws.com]` (the second is what creates `AWSServiceRoleForEC2Fleet`) | `InfraRole` itself |
| `ssm:GetParameters` on `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*`, **unconditionally** — measured (P0 open question 3): without it `CreateFleet` fails top-level with `SsmAccessDenied`, naming neither the action nor the parameter. The CP's existing `ssm:GetParameter` is scoped to `/af-ws/*` and cannot read it | |
| `ec2:CreateTags` is **not** needed for the box's tags: `CreateFleet`'s `TagSpecifications` merges with the launch template's tags (measured). `ec2:DescribeLaunchTemplates` / `Versions` are not needed either | |

`ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` / `CreateTags` are already in
`Ec2SlotPool`; `ecs:ListContainerInstances` / `DescribeContainerInstances` /
`DeregisterContainerInstance` already in `EcsContainerInstances`; **both Sids are unconditional
in `20-platform.yaml` and every flavour has them** (Background), so 60-engines does not repeat
them. What 60-engines edits is its own `CpIngestPolicy`: the Managed Instances statements leave
it, the Fleet statements enter it. The full grant set is **an output of open question 3**: the
$0 calls start with this table, and whatever `AccessDenied` names (the published example policy
for EC2 Fleet grants `ec2:*`, which says nothing about the minimum) is added and recorded.

**`AWSServiceRoleForEC2Fleet` is cheap insurance, not a known failure.** The published note that
the first `CreateFleet` fails without it was **not confirmed** (P0 open question 4): three
non-launching calls went through before the role existed, `AWSServiceRoleForEC2Spot` was already
there, and the one launching call ran after the role was created — so "a launching call with
neither role" is unmeasured. The deployment steps (`standup.sh`) still run
`create-service-linked-role --aws-service-name ec2fleet.amazonaws.com` once, `|| true` (the same
column as `AWSServiceRoleForEC2Spot`, created by hand in 0074; before this ADR `standup.sh` did
this only for `ecs.amazonaws.com`). Not a CloudFormation `AWS::IAM::ServiceLinkedRole` — a role
that already exists fails the stack. On af-sandbox the role now exists (created by P0 and left).

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
  is `INACTIVE` at once). **Measured (P0 open question 1): the service's
  `CapacityProviderStrategy` → `LaunchType` is a replacement in CloudFormation**, and because
  both services carry an explicit `ServiceName` (`af-<stack>-llm` / `-image`) CloudFormation
  creates the new service before deleting the old one, the name collides (`AlreadyExists`) and
  the update rolls back, leaving the live service byte-identical. **So the migration is the
  round trip, and only the round trip**: `<Role>Enabled=false` (the condition deletes the
  service; `update.sh` warns about exactly this) → apply the new template → `<Role>Enabled=true`
  — measured at 25 s + 48 s on a throwaway with the same shape. The Cloud Map name is **not**
  gone in between (the `AWS::ServiceDiscovery::Service` is a separate, unconditional resource);
  only the ECS service registering instances into it is, and at desired 0 there are none. The
  services keep `DependsOn: Associations` (it stays).
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

🔁 **What would change this**: fired — open question 1 said "recreated", and the round trip
above is the migration. The ECS service is gone for about a minute; unlike TTS nobody is waiting
(image retries, an llm conversation breaks), so it is done inside a window with both roles at
`mode: off`.

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

## Follow-up — P0: open questions 1-4, measured (2026-09-12, af-sandbox, under $0.01)

The four $0 premises were measured before a line of code, on a throwaway CloudFormation stack with
**its own throwaway ECS cluster and Cloud Map namespace**. Not one command in this run named a real
stack, the real cluster, a real service or a capacity provider: every write named a resource whose
name starts with `af-adr0077-p0-`. **No GPU was bought.** Elapsed: **16 minutes 45 seconds**
(11:50:02-12:06:47 JST).

Two non-GPU boxes were run, not one: open question 2's `t3.small` (3 min 6 s) and — a deliberate
addition — one `t3.small` bought by a *successful* `CreateFleet` under the candidate IAM set
(1 min 6 s). The second box exists because the IAM set was otherwise proved only against calls that
launched nothing, and the measurement below shows that is exactly where `iam:PassRole` is **not**
checked. About 4.2 instance-minutes in total, inside open question 2's own $0.05 allowance.

**Verdict: 1 is 🔴 red, 2 is 🟢 green, 3 is green with three 🔴 corrections to decisions 1, 8 and
10, 4 is 🟢 green.** Open question 1 being red does not rewrite the skeleton — it settles decision
11's migration onto the `<Role>Enabled` round trip the text already carries as its ⚠️ branch. Open
question 2 being green is what the other lanes waited on, so **the CP and CFN lanes may start.**

| # | Verdict | In one line |
|---|---|---|
| 1 | 🔴 | CloudFormation **replaces** the service, and the explicit `ServiceName` collides — `AlreadyExists`, rollback. The `<Role>Enabled=false` → apply → `true` round trip works (25 s + 48 s) |
| 2 | 🟢 | `ECS_INSTANCE_ATTRIBUTES` binds a service placement constraint on the EC2 launch type; RUNNING 28 s after desired 1. Positive control: an unmatched attribute stays at 0 with "MemberOf placement constraint unsatisfied" |
| 3 | 🟢/🔴 | The vocabulary is five per-override codes inside a **200** response — including `UnauthorizedOperation`. `resolve:ssm:` **does** need `ssm:GetParameters` on the caller. The fleet **lingers**, and `DeleteFleets(TerminateInstances=false)` is refused for an instant fleet. `Priority` is honoured and recorded |
| 4 | 🟢 | The conditioned `iam:CreateServiceLinkedRole` creates `AWSServiceRoleForEC2Fleet`. But `CreateFleet` **did not need it** — three calls went through before it existed |

### Open question 1 — the service is replaced, and the name collides

The throwaway service mirrors the real ones: explicit `ServiceName` (`af-adr0077-p0-svc`), a
`ServiceRegistries` entry pointing at a throwaway Cloud Map A-record service, `awsvpc`,
`MinimumHealthyPercent: 0`, `DesiredCount: 0`. The two shapes are switched by a template parameter
resolved through `!If`, which is the same resource-level diff a template edit would produce.

| Time (JST) | What was done | What came back |
|---|---|---|
| 11:50:02 | `create-stack`, `Mode=capacity-provider` | `CREATE_COMPLETE` at 11:51:29. `capacityProviderStrategy: [{FARGATE, weight 1, base 0}]`, `launchType: null`, `placementConstraints: []`, PRIMARY `ecs-svc/3919890012809245997` |
| 11:51:51 | `create-change-set` to `LaunchType: EC2` + `PlacementConstraints` | `Replacement: Conditional`. Per property: `CapacityProviderStrategy` `RequiresRecreation: Never`, **`LaunchType` `Conditionally`**, `PlacementConstraints` `Never` |
| 11:52:20 | **`execute-change-set`** (0074's lesson: the change set is not the answer) | 11:52:26 `Service UPDATE_IN_PROGRESS` — **"Requested update requires the creation of a new physical resource; hence creating one."** 11:52:27 `UPDATE_FAILED` (full text below). 11:52:32 `UPDATE_ROLLBACK_COMPLETE` |
| 11:52:41 | `describe-services` after the rollback | `status: ACTIVE`, strategy back to FARGATE, `desiredCount` 0, **the same PRIMARY `ecs-svc/3919890012809245997`** — the original service was never touched |
| 11:52:55 | `update-stack` with `ServiceEnabled=false` (the condition drops the service) | `UPDATE_COMPLETE` in **25 s**. `describe-services` → `INACTIVE`; `list-services` → `[]` |
| 11:53:30 | `update-stack` with `Mode=launch-type`, `ServiceEnabled=true` | `UPDATE_COMPLETE` in **48 s**. `launchType: EC2`, `capacityProviderStrategy: null`, `placementConstraints: [{memberOf, "attribute:af-role == engine-image"}]`, **the same `serviceRegistries` ARN `srv-5grs42msyxunj3f5`** |

```
Service UPDATE_FAILED
Resource handler returned message: "Resource of type 'AWS::ECS::Service' with identifier
'af-adr0077-p0-svc' already exists." (HandlerErrorCode: AlreadyExists)
```

**Verdict 🔴.** It is a replacement, CloudFormation creates before it deletes, and the explicit
name collides exactly as review R6 predicted. Decision 11's ⚠️ branch is therefore **the only
migration path**, not a fallback: `<Role>Enabled=false` → apply the new template → `true`.
One correction to the text: the round trip does **not** take the Cloud Map name away. The
`AWS::ServiceDiscovery::Service` is a separate resource with no condition on it, so the DNS name
and its ARN survive; what disappears for those seconds is only the ECS service registering
instances into it, and at desired 0 there are none. Decision 11's "the Cloud Map name is gone in
between; at desired 0 no request is lost" is safe but overstated.

**Impact — depends on: decision 11.** Decision 11's 🔁 has fired; its own text already names the
consequence. Nothing else moves: the failed update left the live service byte-identical, so the
migration is recoverable if it is attempted the wrong way round.

### Open question 2 — the attribute binds

One throwaway launch template (`ImageId:
resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id`, resolved to
`ami-0f57b5b58b68248a3`; `HttpTokens: required`; user data writing `ECS_CLUSTER` and
`ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-image"}` into `/etc/ecs/ecs.config`), one `t3.small`,
and the task definition the stack already carries (`awsvpc`, 256/512, no GPU requirement).

| Time (JST) | What was done | What came back |
|---|---|---|
| 11:54:56 | `run-instances`, 1 × `t3.small`, private subnet 1a | `i-05375bcf612c7ec74`, `pending` |
| 11:55:20 | `describe-container-instances` | Registered **24 s after the launch**. `agentConnected: true`, attributes contain **`{"name": "af-role", "value": "engine-image"}`**, and **`capacityProviderName` is absent** |
| 11:55:45 | **positive control**: a second EC2-launch-type service, same task definition, constraint `attribute:af-role == engine-llm`, desired 1 | 11:56:00 `runningCount` 0, `pendingCount` 0, event: *"was unable to place a task because no container instance met all of its requirements. The closest matching (container-instance fb2ddff7…) encountered error `"MemberOf placement constraint unsatisfied."`"* |
| 11:55:47 | the measured service (`== engine-image`) to desired 1 | 11:55:53 "has started 1 tasks"; **`RUNNING` at 11:56:15, 28 s after the call**. `launchType: EC2`, `attachments: [(ElasticNetworkInterface, ATTACHED)]` |
| 11:56:57 | desired 0, then `terminate-instances` | 11:58:02 EC2 `terminated`. The container instance went to **`INACTIVE` by itself within about 30 s** — nothing deregistered it by hand |

**Verdict 🟢.** The agent honours `ECS_INSTANCE_ATTRIBUTES` on the AL2023 ECS AMI, and a *service*
placement constraint reading that attribute binds on the EC2 launch type. The positive control
shows the constraint is genuinely evaluated rather than ignored — the same box, the same task
definition, one word different in the expression, and the task will not place.

**Impact — depends on: decisions 2 and 3.** Both stand as written. Decision 2's 🔁 (write
`ec2InstanceId` on the task definition and cut a revision per start) is **not needed**. Decision
3's 🔁 (`PutAttributes` right after registration) is **not needed**. Two premises of decision 3
were confirmed as side effects: an EC2 box the CP buys carries **no `capacityProviderName`**
(so `isPoolContainerInstance` does collapse as the Background says, and does need the attribute
clause), and `awsvpc` on the EC2 launch type attaches a task ENI without any agent setting.
One item of decision 4 gained support it did not have: ECS **does** deregister a terminated
instance's container instance by itself, measured here at about 30 s on a `t3.small` — that was
(c) in the text.

### Open question 3 — the `CreateFleet` vocabulary, the IAM minimum, the lingering fleet, `Priority`

Every call below ran as a throwaway IAM role (`af-adr0077-p0-fleet`, trusted by this session's own
principal) holding decision 10's table, so that an `AccessDenied` would name what the table is
missing. Not one of the four probing calls launched anything.

| Time (JST) | Call | `Errors[].ErrorCode` / result |
|---|---|---|
| 11:58:49 | Spot `g6.4xlarge` × 2 subnets, `price-capacity-optimized`, decision 10's table verbatim | **top-level failure, no `Errors[]`**: `SsmAccessDenied`, *"Access denied to SSM"* |
| 11:59:16 | the same, after adding `ssm:GetParameters` on `parameter/aws/service/ecs/optimized-ami/*` | **HTTP 200**, `Instances: []`, one entry per override: **`MaxSpotInstanceCountExceeded`** / *"Max spot instance count exceeded"*, `Lifecycle: spot` (the G/VT Spot quota on this account is 8 vCPU; `g6.4xlarge` is 16) |
| 12:00:54 | Spot `g6.xxlarge` (misspelled) | **`InvalidFleetConfiguration`** / *"Your requested instance type (g6.xxlarge) is not supported in your requested Availability Zone (ap-northeast-1a)."* |
| 12:00:57 | Spot `g6.xlarge` × 2 subnets with `MaxPrice: "0.001"` | **`SpotMaxPriceTooLow`** / *"Your Spot request price of 0.001 is lower than the minimum required Spot request fulfillment price of 0.5762."* (0.5633 in the other AZ) |
| 12:01:03 | On-demand `g6.48xlarge` (Priority 1) and `g6.24xlarge` (Priority 2), `OnDemandOptions.AllocationStrategy: prioritized` | **`VcpuLimitExceeded`** / *"You have requested more vCPU capacity than your current vCPU limit of 8 allows for the instance bucket…"*, `Lifecycle: on-demand`. Each error echoes its override **including `Priority: 1.0` / `2.0`** |
| 12:03:34 | On-demand `t3.small` (Priority 1) / `t3.small` other subnet (Priority 2), request-level `TagSpecifications` | **`Errors: []`**, exactly one instance, the **Priority 1** override taken: `i-05bfc8ee328b5a232`, `Lifecycle: on-demand` |

🔴 **Three of these land on the body.**

1. **`UnauthorizedOperation` arrives inside `Errors[]`, per override, in a 200.** With
   `iam:PassRole` removed from the role, the same `SpotMaxPriceTooLow` call came back 200 with
   `ErrorCode: UnauthorizedOperation` and *"is not authorized to perform: iam:PassRole on resource:
   …/af-adr0077-p0-instance"*. **An IAM hole is shaped exactly like "no capacity."** Decision 8's
   failure-code table must sort `UnauthorizedOperation` (and `SsmAccessDenied`, which does arrive
   top-level) as a **hard failure that stops the lap**, never as "try the next offer" — otherwise a
   misconfigured deployment silently walks the whole offer list and reports "no capacity anywhere".
2. **`--dry-run` cannot catch it.** The identical call with `--dry-run`, with `iam:PassRole`
   removed, returned `DryRunOperation` — *"Request would have succeeded, but DryRun flag is set."*
   Dry run does not exercise the per-override authorization. It is not a usable pre-flight for this.
3. **A misspelled type and "this type is not offered in this AZ" are the same code and the same
   sentence.** Decision 8's "a misspelled type is refused **on the spot** by `CreateFleet`" is true
   — but the response does not say "misspelled", so a typo in `<role>Offers` will read as a
   capacity fact and be retried on the next demand for ever. A template-side check on the type
   spelling is worth more than reading the code.

⚠️ **What was not measured**: a genuine out-of-stock refusal. `SpotMaxPriceTooLow` is a price
refusal standing in for one; the real code (`InsufficientInstanceCapacity` or whatever EC2 Fleet
calls it) is still unknown, because provoking it means asking for capacity that might arrive.

#### (a) The IAM minimum

Built up from decision 10's table by adding whatever a denial named. Only **one** grant had to be
added, and one already in the table was proved necessary the hard way.

| Statement | Actions | Resource / condition | Evidence |
|---|---|---|---|
| Fleet | `ec2:CreateFleet`, `ec2:DescribeFleets`, `ec2:DeleteFleets` | `*` | `CreateFleet` and `DescribeFleets` exercised; `DeleteFleets` exercised in cleanup |
| Ec2SlotPool (subset, already granted on every flavour) | `ec2:RunInstances`, `ec2:TerminateInstances`, `ec2:DescribeInstances`, `ec2:DescribeSubnets`, `ec2:CreateTags` | `*` | `TerminateInstances` and `DescribeInstances` exercised under this role. `RunInstances` / `DescribeSubnets` / `CreateTags` were **not** proved necessary — no denial named them |
| PassEngineRole | `iam:PassRole` | `role/af-*`, `iam:PassedToService: ec2.amazonaws.com` | **Proved.** Removing it turns every override into `UnauthorizedOperation` — but **only on a call that would otherwise launch** |
| SsmPublicAmi | **`ssm:GetParameters`** | `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*` | **Proved.** Without it, `CreateFleet` fails top-level with `SsmAccessDenied`, naming neither the action nor the parameter |
| Slr | `iam:CreateServiceLinkedRole` | `iam:AWSServiceName` in `[spot.amazonaws.com, ec2fleet.amazonaws.com]` | **Proved** (open question 4), with a negative control |

**So decision 10's conditional row becomes unconditional**: `ssm:GetParameters` on the public AMI
parameter is required of the **caller** of `CreateFleet`, exactly as (c) suspected. Two grants that
are *not* needed and should not be added: `ec2:DescribeLaunchTemplates` /
`ec2:DescribeLaunchTemplateVersions` (the role held neither and `CreateFleet` resolved the template
by name), and `ec2:CreateTags` for the box's tags — see (d).

#### (b) The instant fleet lingers, and `DeleteFleets(TerminateInstances=false)` is refused

- After a call that launched **nothing**, `describe-fleets --fleet-ids <id>` returns the fleet as
  `FleetState: active`, `ActivityStatus: fulfilled`, `FulfilledCapacity: 0.0`, with its whole
  recorded configuration.
- After the successful call's box was terminated, the fleet stayed **`active` / `fulfilled` /
  `FulfilledCapacity: 1.0`** — a number that is now false — through a further 30 s of polling. It
  does **not** delete itself. Published spec (c) says it does; on this deployment it does not.
- 🔴 `delete-fleets --no-terminate-instances` — the exact call decision 1 names — is refused:
  `NoTerminateInstancesNotSupported`, *"NoTerminateInstances option is not supported for instant
  fleet"*. Only `--terminate-instances` works (`deleted_terminating` → `deleted`), which for the
  CP means the call is only safe on a fleet whose box it has already decided to lose.
- 🟢 **But nothing accumulates on a path the CP walks**: an unfiltered `describe-fleets` returned
  `{"Fleets": []}` throughout, while the by-id calls in the same second returned the fleets — that
  by-id call is the positive control for the empty list. Instant fleets are simply not enumerated.

**So decision 1's sentence survives, for a different reason than it gives.** "The CP does not
remember the fleet id" is right, and no `DeleteFleets` follow-up is needed — not because the fleet
disappears, but because it is invisible to every listing and holds no capacity. `ec2:DeleteFleets`
is still worth granting, but the `TerminateInstances=false` shape the text promises does not exist.

#### (c) `prioritized` and `Priority`

🟢 Recorded and honoured. The on-demand fleet's `describe-fleets` carries
`OnDemandOptions.AllocationStrategy: prioritized` and `Overrides[].Priority` `1.0` / `2.0`, the
errors echo `Priority` per override, and the one successful on-demand call took the **Priority 1**
override (`t3.small` in subnet 1a) with the Priority 2 override untouched. Done item 6 can be
written against this shape.

#### (d) Two things the calls handed back that the text does not have

- **The whole tag set lands in the one `CreateFleet` call.** Request-level `TagSpecifications`
  (`ResourceType: instance`) **merges** with the launch template's own tags: the box came up
  carrying `af-pool`, `af-role=engine-image`, `af-engine-offer`, `af-engine-buy` from the request
  *and* `af-managed-by=agent-fleet` from the template. Decision 3's tag set needs no separate
  `CreateTags` — unlike the slot pool, which tags at `RunInstances`.
- **EC2 writes `aws:ec2:fleet-id` on the box by itself.** So the CP can reach the fleet from the
  box with no memory at all, which is the 0045 decision 29 shape decision 3 asks for, for free.
- `InstanceLifecycle` is **absent** on an on-demand box (it is `spot` on a Spot one — 0075's
  measurement). Done item 7's "`InstanceLifecycle`" check must read "absent" as "on-demand", not
  as missing data.

**Impact — depends on: decisions 1, 8 and 10.** Decision 10 gains `ssm:GetParameters`
unconditionally and can drop the two Describe actions it never needed. Decision 8's failure-code
table gains a class it does not have — *authorization failures inside `Errors[]`* — and must not
treat them as capacity. Decision 1's `DeleteFleets(TerminateInstances=false)` sentence needs
rewriting to "no follow-up is needed; an instant fleet is invisible to `describe-fleets` unless
named by id". **None of the three rewrites a decision's premise**; the body is left as written for
the parent to revise.

### Open question 4 — the CP can create the service-linked role

| Time (JST) | What was done | What came back |
|---|---|---|
| 11:58:49-12:01:03 | the four `CreateFleet` calls above, **with no `AWSServiceRoleForEC2Fleet` in the account** | Every one behaved normally (the SSM denial, then three 200s with per-override errors). **No SLR error of any kind, and the role was not auto-created** |
| 12:02:46 | `create-service-linked-role --aws-service-name ec2fleet.amazonaws.com`, under the conditioned grant | **200.** `AWSServiceRoleForEC2Fleet`, `arn:aws:iam::<account>:role/aws-service-role/ec2fleet.amazonaws.com/AWSServiceRoleForEC2Fleet` |
| 12:02:50 | **negative control**: the same call with `--aws-service-name autoscaling.amazonaws.com` | `AccessDenied` — *"not authorized to perform: iam:CreateServiceLinkedRole on resource: …/AWSServiceRoleForAutoScaling"*. The `iam:AWSServiceName` condition really does the scoping |
| 12:02:52 | the same call a second time | `InvalidInput` — *"Service role name AWSServiceRoleForEC2Fleet has been taken in this account, please try a different suffix."* |

**Verdict 🟢** on the question as posed: the limited `iam:CreateServiceLinkedRole` goes through, and
the condition keeps it limited. Two riders:

- ⚠️ **`standup.sh`'s `|| true` is mandatory, and the error code is `InvalidInput`** — not
  `EntityAlreadyExists`. A script that matches on the code will not recognise it.
- 🔴 **Decision 10's ⚠️ "on an account without `AWSServiceRoleForEC2Fleet`, the first `CreateFleet`
  fails" is not confirmed.** Three `CreateFleet` calls went through before the role existed.
  **Honest limit**: `AWSServiceRoleForEC2Spot` was already present (0074 created it), and none of
  those three calls launched an instance — the one that did launch ran after the SLR existed. So
  "a *launching* `CreateFleet` on an account with neither SLR" was **not** measured, and creating
  the role in `standup.sh` stays the right call. What can be said is that the failure is not at the
  API's front door on this account.

**Impact — depends on: decision 10.** The `standup.sh` line stays; its ⚠️ justification is weaker
than the text claims and should be re-worded to "cheap insurance", not "the first call fails".

### The cleanup

Everything created for this run was deleted and the deletion checked with a listing that also
shows the tool would have found something (the positive control is in the same output):

- `describe-stacks` → the **seven real stacks only** (`af-ecs-network` … `af-ecs-engines`, all
  `CREATE_COMPLETE` / `UPDATE_COMPLETE`); `af-adr0077-p0-oq1` is gone (12:06:47).
- `describe-instances --filters Name=tag-key,Values=af-adr0077-p0` **with no state filter** → the
  two probe boxes, both `terminated`. (An empty answer here would have been the ambiguous one.)
- `describe-launch-templates` → `af-af-ecs-pool-slot` only.
- `list-roles` for `af-*` → the eight real roles only; both throwaway roles and the instance
  profile are gone.
- `list-clusters` → `af-af-ecs-platform` only. `list-namespaces` → `af.internal` only.
- The four instant fleets were deleted (`deleted_terminating`); they held no instances.

**Left behind on purpose: `AWSServiceRoleForEC2Fleet`.** The ADR has `standup.sh` create it, so it
is left in place, as open question 4 allows. Nothing else survives this run.

**Cost**: about 4.2 `t3.small`-minutes ≈ **$0.002**; the Cloud Map private DNS namespace was
created and deleted inside the same hour (a hosted zone deleted within 12 hours is not billed).
Everything else — CloudFormation, ECS, IAM, the six `CreateFleet` calls — is free.

The raw responses are in `~/.cache/adr0077-p0/` of the session that measured this.

## Follow-up — what P1's CP lane handed back to the text (2026-09-12, PR #577)

The CP side of P1 (decisions 1, 2, 3, 5, 8, 9 and 11; the CP end of contract A) landed as #577.
Done items 1-6 are pinned in `engine_offer_test.go` and
`internal/runtime/runtime_ecs_ec2_engine_test.go`, each with its positive control — including two
mutations run by hand and reverted: removing the `registered()` guard makes done item 3 fail, and
removing the `af-role` filter makes done item 5 fail. Done item 7 is the hardware lane and is not
in this. What the implementation handed back:

- **The EC2 port is three calls, not five.** P0 measured `CreateTags` to be unnecessary —
  `CreateFleet`'s `TagSpecifications` (ResourceType `instance`) merges with the launch template's
  own tags and lands at launch — and `DeleteFleets` to be unusable in the shape decision 1 names:
  `TerminateInstances=false` is refused for an instant fleet with `NoTerminateInstancesNotSupported`.
  The fleet does linger (P0 (b)), so the reason decision 1's "the CP does not remember the fleet
  id" survives is not the one the text gives — it is that an instant fleet is not enumerated by an
  unfiltered `describe-fleets` and holds no capacity, so nothing accumulates on a path the CP
  walks. The port is `CreateFleet` / `DescribeInstances` / `TerminateInstances`.
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

## Revisions after P0 and the P1 lanes (2026-09-12)

The three follow-ups above (#575, #576, #577) left the decisions as written and handed their
findings back for revision. This section is that revision, applied **in the decision text above**
in the manner of ADR 0075's review — no premise of any decision moved; what moved is the wording
that P0 measured to be wrong or (c).

| Decision | What changed | Source |
|---|---|---|
| 1 | "if fleets linger, `DeleteFleets`" → measured: they linger but are invisible to `describe-fleets` unless named by id, and `DeleteFleets(TerminateInstances=false)` is refused; **no follow-up call**, the port is three calls / `Priority` is honoured and recorded | P0 open question 3 |
| 3 | `sweepSlotOwnerTags`'s filter is `af-role` in `{slot, quarantined}`, not `slot` alone (a quarantined box still carries the owner tag the sweep repairs) | #577 |
| 5 | the sweep's grace is not the slot pool's `ghostAfter` (another package) but two minutes at desired 0 and fifteen minutes while the service is up | #577 |
| 8 | the failure-code table is written out with the measured vocabulary; `InvalidFleetConfiguration` is `unusable` only when every override says it; **authorization failures inside `Errors[]` are a hard failure that stops the lap**, and `--dry-run` is no pre-flight | P0 open question 3 |
| 10 | `ssm:GetParameters` on the public AMI parameter is **unconditional**; `DescribeFleets` / `DeleteFleets` / `CreateTags` / `DescribeLaunchTemplates` are not needed; the service-linked role is "cheap insurance", the published "first call fails" was not confirmed | P0 open questions 3 and 4 |
| 11 | the migration is the `<Role>Enabled` round trip **and only that** (CloudFormation replaces the service; the explicit name collides; 25 s + 48 s); the Cloud Map name does **not** disappear in between; the 🔁 has fired | P0 open question 1 |

Open question 8 (mounting the instance store) moved from P1's "measure" to P1's scope on the
CFN lane's finding that retiring `<Role>UseLocalStorage` loses a measured 2× on cold start; #575
mounts the NVMe as Docker's data-root from user data, and the P1 hardware run reads
`df /var/lib/docker`.

## Follow-up — what the second CP pass handed back (2026-09-12, PR #584)

P1's hardware run (#583) found three red points on the CP side. All three are fixed here; two of
them say something the text above does not, and this section is the record. **No decision is
edited.** The three are not equal: the first is #577 failing to implement decision 5, the second
and third are the ADR's migration meeting a state it does not describe.

- 🔴 **Decision 5's departure never ran once, and the text is not at fault — the code was.**
  The sweep stands down while a start is in flight, which is right: a box bought seconds ago has
  no task on it BY CONSTRUCTION, and sweeping there would terminate every start. But the flag is
  "a box is bought and the desired count is not written yet", and nothing cleared it on a start
  that SUCCEEDED — only the registration ceiling and the next start did. So after the first
  successful start the sweep returned at its first line for ever: measured twice on hardware,
  `mode=off`, the task gone, the box still running four minutes later, terminated by hand. The
  start now forgets the box the moment it writes the desired count. **What the text should carry
  from this**: the departure has two preconditions that are the same fact from two sides — no
  start in flight, and the desired count already written — and an implementation that keeps only
  the first has no departure at all.
- **A start needed a second guard as a consequence: "the service already wants a task".** The
  admin toggle calls the start unconditionally on `mode=on`, and with the box no longer
  remembered a press on a RUNNING engine would begin the walk again and buy a second GPU. The
  start now reads the service first and returns when `desired >= 1`. This is the successor of the
  same guard ADR 0075 had inside `setStrategy`, which went with it.
- 🔴 **A table with NO ROWS is a state this ADR's own migration creates, and the CP could not
  come back from it.** The `<Role>Enabled` round trip drops the conditional half of 60-engines
  for a minute or two — the engine table among it — and a Control Plane that started inside that
  window read zero rows, built no registry, started no reloader, and answered `{"engines":[]}`
  for the rest of its life. Measured: 86 s of force-new-deployment to recover an engine that
  nothing was wrong with. The registry is now built EMPTY, the reloader runs, and a role that
  appears is adopted through the same function boot builds with; the deployment-wide ingest
  runner comes back the same way (a zero-row table carries no `ingest` block either, so without
  it such a CP could serve every engine and still not take a model in). ⚠️ This overturns the
  reasoning in `engine_table_reload.go`'s header, inherited from ADR 0074 — "registering a role
  from inside a poll would build a controller with none of the state the process assumes". That
  was true while construction lived in the boot loop; it is one function now, so an adopted role
  is built exactly as a boot-time one is. **What the text should carry**: decision 11's migration
  section asks for a window in which no box stands, and it should also say that the CP must
  survive a table with no rows — because the round trip is what produces one.
- **`<Role>Enabled=true` leaves the service at desired 1 with no box.** CloudFormation creates a
  service that omits `DesiredCount` at 1, so the second half of the round trip sits in
  stabilisation until somebody writes 0 (on hardware, another shell did). The controller already
  writes desired 0 whenever the mode is `off`, whoever set the count — that behaviour was there
  and is now pinned by a test. **It did not fire on hardware because of the point above**: that
  CP had no registry, so it had no controller to fire. Nothing new is needed, but the migration
  section's "both roles `mode: off`" is now load-bearing for a second reason: it is what takes
  the stray desired 1 back down without a human.

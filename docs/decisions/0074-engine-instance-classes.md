# 0074. The GPU an engine buys is chosen at runtime, from a ladder the operator declares

English | [日本語](0074-engine-instance-classes.ja.md)

- Status: **accepted; P0 implemented, not yet run on real hardware** (2026-09-10). P1 is not started.
  **Nothing was measured for this document.** Every number says where it comes from —
  (a) measurements in ADR 0071 and 0072, (b) facts read out of the repository's code on the
  same day, (c) things known only as AWS's published specification and **not confirmed on this
  deployment** (g6e's VRAM and price, per-region availability). Nothing in (c) is load-bearing:
  the decisions that depend on it are listed under "Open questions", each naming the decision
  that waits on it.
- Revised the same day after one round of review. **Open questions 2 and 4 became decisions** —
  when to apply is now part of decision 5 (idempotently, just before a start; it costs nothing
  extra, and a failure refuses the start only when the rung differs from the last one applied),
  and decision 1 now says a rung over the quota is not rejected at declaration time (**the
  production quota is 96**, dev and af-sandbox are 8, and the CP cannot read either). The
  shipped ladder is **empty** (decision 1), and decision 4 gained both the reason for always
  waiting (no branch on a number we cannot read) and the price of one switch (15-20 minutes of
  wall clock, $0.3-0.4 at g6.xlarge rates).
- Implemented P0 the same day (no real hardware yet). The implementation corrected the text in
  three places — 🔴 **the rejected alternative was rejected for the wrong reason** (the template
  wall is crossed by `af_cfn_deploy` via S3; the real reason is that decision 5's idempotent
  re-application cannot work on a service), **the copying risk was in
  `InstanceLaunchTemplate`, not `InstanceRequirements`** (the requirement type is the same in
  both directions; the launch template is not, and **two fields cannot be carried**), and **the
  admin `mode=on` route consults the gate too** (without it one button walks around the wait).
  Each is written into the decision it belongs to.
- Related: [0071-self-hosted-inference-engines.md](0071-self-hosted-inference-engines.md)
  decision 2 (one capacity provider per role, the box chosen by a VRAM floor), decision 5,
  decision 9 / [0072-engine-model-catalog.md](0072-engine-model-catalog.md) decision 1,
  decision 7 (the stack is a SEED; a stored choice wins), decision 10 /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 21 (the numbers
  in a ladder are DECLARED by the operator; EC2 is not asked) /
  [0048-member-cloud-cost.md](0048-member-cloud-cost.md) decision 15 (cost allocation tags) /
  [70-slot-instance-classes.md](../log/70-slot-instance-classes.md) (how the slot class ladder
  was implemented)

## Background

The request: "make the GPU instance type the LLM and image roles use changeable. I want to try
a model that temporarily needs much more VRAM. Warn me depending on the model I pick, and let
the instance be scaled up."

Since ADR 0072 took models out of CloudFormation and into a catalogue, **the model can be
swapped at runtime while the box it lands on stays fixed at deployment time**. Taking in
something as heavy as FLUX.1-dev (23.8 GB, measured in 0072 P4) is a Console operation from end
to end — and the card it would run on is still a g6.xlarge (L4), so starting it kills CUDA.

### What the code says today (read 2026-09-10)

- **The box is decided by the capacity provider's `InstanceRequirements`.** `60-engines.yaml`'s
  `LlmAllowedInstanceTypes` (default `g6.xlarge,g5.xlarge`), `LlmAcceleratorMemMinMiB` (21000),
  `LlmVCpuMin/Max` (4/8), `LlmMemMinMiB/MaxMiB` (15000/65536), and the image role's mirror.
  **All CloudFormation parameters — fixed at deployment**, changed only by a stack update.
- **The service names its role's provider** (`CapacityProviderStrategy: [{CapacityProvider:
  !Ref LlmCapacityProvider, Weight: 1}]`), so the provider's requirements are the only door
  to a different box.
- **The 51,200-byte wall.** `60-engines.yaml` is 49.7 KB, with no room for one ~1.5 KB capacity
  provider per rung per role. 🔴 The wall itself CAN be crossed — `af_cfn_deploy` switches to S3
  above it. What cannot be crossed is the same number in
  `deploy/local/ecs-lifecycle-stub-test.sh` case 3b-2, which holds the shipped templates to it
  (see the rejected alternatives).
- **`engineDef` says the vessel belongs to the stack** — "Everything else here (service, URL,
  health, capacity provider, idle, deadline, mode) really is a property of the vessel and stays
  the stack's to declare". This ADR deliberately overturns part of that sentence.
- **The catalogue already has a VRAM column.** `engine_models.vram_mib` is "the operator's own
  measurement, 0 = unmeasured" and is currently **only displayed** as a chip in the admin panel.
  `EngineModelFile.Bytes` is there too, as a declaration (the CP cannot look in S3, so it is
  "the size somebody declared", never one it read).
- **VRAM is a maximum, not a sum.** The llm role runs `LlmModelsMax: 1` ("the router does not
  know about VRAM: a second 30B Q4 on an L4 does not run slowly, it crashes"), and the image
  role's sd-server holds one checkpoint per process. **One enabled model is in VRAM at a time.**
- **A price is hard-coded in the i18n catalogue.** `admin.engines_always_on_note` says
  "$1.26/hour" in both languages. The moment the box becomes selectable, that sentence is false.
- **`engine_hourly`'s primary key is `(engine_key, hour)`.** Which box those hours ran on is
  not recorded.

### What upstream has (read from aws-sdk-go-v2/service/ecs v1.87.0)

- **`UpdateCapacityProvider` can update a Managed Instances provider**
  (`ManagedInstancesProvider *types.UpdateManagedInstancesProviderConfiguration`).
- 🔴 **"These changes only apply to new Amazon ECS Managed Instances, or EC2 instances, not
  existing ones"** — **the running box does not change.** That sentence is the whole of
  decision 4.
- `UpdateManagedInstancesProviderConfiguration` has `InfrastructureRoleArn` and
  `InstanceLaunchTemplate` as **required members**. `InstanceLaunchTemplateUpdate` carries
  `InstanceRequirements`, `NetworkConfiguration`, `StorageConfiguration`,
  `LocalStorageConfiguration`, `Ec2InstanceProfileArn` and more.
- 🔴 The draft's claim that the requirement types differ between read and write is **wrong**
  (checked while implementing): both directions use `InstanceRequirementsRequest`. **The level
  above is what differs** — `InstanceLaunchTemplateUpdate` has no `CapacityOptionType` and no
  `FipsEnabled`. A missed field **disappears silently** (decision 8).

### Traps already paid for (measured in ADR 0071 and 0072)

- **The G-family vCPU quota is 8.** Starting right after a stop puts the draining box's 4 vCPU
  next to the new one and fails with `VcpuLimitExceeded`. Draining measured at 427-477 seconds.
- **MI has no allocation strategy: the CHEAPEST member that fits is bought.**
  `AllowedInstanceTypes` is a FILTER, not an order of preference.
- **An MI box does not appear in an unfiltered `ec2 describe-instances`** (it answers when named
  directly). Whether a box exists is read from ECS's container instances.
- **Cold start: 586 s for llm, 165-197 s for image**; a g6.xlarge is **$1.26/hour**.
- **SSM Standard tier holds 4,096 characters.** The engine table and the active set both live
  under that ceiling.
- **Measured VRAM**: 20,943 MiB for the 30B Q4, 7,379 MiB for SDXL fp16.

## Decisions

### 1. The operator declares the ladder. The CP does not ask EC2

One string per role declares the rungs, in the same style as the slot ladder
(`AF_ECS_EC2_SLOT_TYPES`, ADR 0045 decision 21, docs/log/70): `|` between fields, `;` between
rungs.

```
id|label|vramMiB|instanceTypes|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour
```

```
l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;
l40s|L40S 48GB|44000|g6e.xlarge,g6e.2xlarge|4-8|30000-65536|
```

- **`vramMiB` is one number doing two jobs** — the `AcceleratorTotalMemoryMiB.Min` handed to the
  capacity provider (which box may be bought) and what decision 6's fit check compares a model
  against. It is declared **deliberately below** the card's nominal size, as the existing 21000
  is for an L4's 24 GB, so the check errs toward warning early.
- **`usdPerHour` is optional and display-only.** Neither EC2 nor the Pricing API is asked (ADR
  0045 decision 21 again: it means an IAM action on the CP task role and a stack update for a
  label). **Empty is fine**: with no figure the panel names no price and says only that the
  hourly rate depends on the class. **No number beats a wrong number.**
- **Each rung carries its own vCPU and memory bounds.** A 48 GB card only exists in boxes with
  32-64 GiB of RAM, so one wide range cannot buy the upper rung.
- On a deployment that declares no rung, this feature **does not exist** (decision 3).
- 🔴 **The shipped default is an empty ladder.** The example above is a proposal, not a
  measurement (open question 3). Baking defaults in waits until g6e's VRAM, price and
  availability have been confirmed on this deployment (P1). **A default nobody measured makes
  decision 1's own premise — that the operator declared it — a lie.**
- **A rung above the quota is not rejected at declaration time.** The CP cannot read the
  G-family vCPU quota (reading it means adding `service-quotas` to the IAM grant, the very shape
  decision 1 avoids), and the quota differs per deployment (96 in production, 8 in dev and
  af-sandbox). When an unbuyable rung is chosen, the service events say so
  (`VcpuLimitExceeded`) and the panel shows them as they are.

⚠️ **The "widen `AllowedInstanceTypes` and switch rungs with the VRAM floor alone" variant is
rejected.** It works, but it leans on "the cheapest member that fits is bought", so the day AWS
ships a cheaper 48 GB type the deployment **silently changes box**. A rung is a SET of
requirements, not one floor.

### 2. A stored choice wins; the stack's declaration is the default (a SEED)

The rule is ADR 0072 decision 7's. The choice lives in the setting `engine_<key>_class`, and
**the stack's default rung (the first in the ladder, or an explicit default id) applies only
until somebody chooses.** Neither a CP restart nor a stack update overwrites an administrator's
choice.

This is the same rule 0072 needed to keep models out of CloudFormation, for the same reason:
the worst failure is that **the morning after an administrator moved to 48 GB, an unrelated
stack update silently puts it back to 24 GB** — and nobody sees it until the next cold start
kills CUDA.

### 3. With no ladder and no choice, the CP never calls `UpdateCapacityProvider`

If the ladder is empty, or the choice equals the default and has **never been applied**, the CP
neither reads nor writes the capacity provider. Existing deployments behave identically, and no
deployment that skipped the IAM change logs an `AccessDenied`.

### 4. A rung change reaches the NEXT box only. Switching is stop → wait for it to leave → start

The SDK says the running box does not change, so the flow has three steps and **the screen
shows all three** (announcing "changed" while the old box keeps serving is the most expensive
lie available here).

1. Save the rung. If the engine is not running, that is the end of it: the next start buys the
   new box.
2. If it IS running, make the operator press **"replace it now"** explicitly. That sets
   desired 0.
3. 🔴 **Wait for the container instance to disappear before starting.** Starting sooner puts a
   draining 4 vCPU box next to a new 8 vCPU one, exceeds **the quota, and fails with
   `VcpuLimitExceeded` — silently, as a placement that never happens**: until somebody reads
   the service events it merely looks like a slow start. Draining measured at 427-477 s (about
   8 minutes from desired 0 to the box being gone, on the image role).

**It waits on a deployment with headroom too.** The G-family vCPU quota differs per deployment
(96 in production, 8 in dev and af-sandbox) and **the CP cannot read it** (no `service-quotas`
in the IAM grant, for decision 1's reason). A branch on a number we cannot read **fails silently
exactly on the deployment where the branch is wrong**. One order is safe on both, so that order
is the behaviour. It costs wall clock and nothing else: **the two boxes never bill at once**,
because the new one is bought after the old one is gone.

⚠️ **A replacement buys one cold start** (measured 527-586 s for llm, 165-197 s for image).
Storage is the instance store, so **the new box fetches the model from S3 again** — measured at
6.62 GiB in 66 s (102.7 MiB/s). With the drain, one switch is 15-20 minutes of wall clock and
roughly $0.3-0.4 of GPU time at g6.xlarge rates. The screen says so. It is ADR 0071's
`offGrace: 0` position — changing your mind costs a cold start — applied to the box instead of
the mode.

⚠️ **Saving a rung while the engine is stopped costs nothing at all.** It is absorbed by the
cold start whoever uses it next was going to pay anyway.

**What the implementation added (P0)**:
- The wait is judged from the **box's EC2 type**, read from the container instance's
  `ecs.instance-type` attribute — the only place the CP can read the type of a Managed
  Instances box (it does not appear in `ec2 describe-instances`). A box whose type the selected
  rung does not cover is the previous rung's box, still there.
- 🔴 **The wait is bounded at 20 minutes.** Its end belongs to AWS, and with
  `scaleInAfter: -1` ("never tidy up") an unbounded gate is **an engine that can never start
  again**, with one log line as the evidence. Measured draining is 427-477 s, so 20 minutes
  waits out a slow one and still gives up.
- Replacing is its own act (`POST …/replace-box`) and **does not touch the mode**. Making
  somebody press "off" instead would leave the mode off afterwards, reproducing ADR 0071's trap
  where forgetting to switch it back is indistinguishable from a stopped box.
- 🔴 **The admin `mode=on` route consults the same gate.** It calls `setEnabled(true)` directly,
  so without this one button **walks around the wait and buys the old rung's box**. A gate that
  says wait does not fail the request: the mode is stored and the controller starts the engine
  once the old box has gone.

### 5. Applying it is a read-modify-write: Describe → copy → Update

Because `InstanceLaunchTemplate` is a required member, the CP reads the current configuration
with `DescribeCapacityProviders` and writes it back **with only the four `InstanceRequirements`
fields replaced** (`AllowedInstanceTypes`, `AcceleratorTotalMemoryMiB.Min`, `VCpuCount`,
`MemoryMiB`). Network, storage, instance profile and the GPU declaration (`AcceleratorCount`,
`AcceleratorTypes`, `AcceleratorManufacturers`) **go back exactly as they were read**. The CP
holds no design for the box — giving it one would make two designs, its own and the stack's.

**It is applied at two moments** — when a rung is saved, and **idempotently, just before a start
is decided**. The second exists because CloudFormation puts its own declaration back (a stack
update does not replace the CP, so decision 2's "a stored choice wins" alone would let the next
box be bought on the reverted rung).

**Re-applying costs nothing.** `UpdateCapacityProvider` does not buy a box, it rewrites what the
next box will look like; ECS API calls are free, and writing the same values back touches
neither the running box nor the desired count. The price is one more call on the start path.

🔴 **When re-applying fails**: **if the chosen rung differs from the last one this process
applied, do not start** — starting anyway buys the old box while believing it is the new one,
and with a heavy model CUDA dies and **a whole cold start is thrown away**. If they are the
same, log the failure and start (the box's specification is already right).

**What the implementation found (copying against SDK v1.87.0)**:
- ✅ **`InstanceRequirements` is the same type in both directions**
  (`InstanceRequirementsRequest`). The "missed requirement field" the draft feared is not here:
  the struct that was read is carried whole and four fields are replaced in it.
- 🔴 **The risk is one level up.** `InstanceLaunchTemplate` (read) and
  `InstanceLaunchTemplateUpdate` (write) are **different types**, and **`CapacityOptionType`
  (ON_DEMAND / SPOT) and `FipsEnabled` do not exist on the write type — they cannot be
  carried**. Whether ECS preserves them or resets them is undocumented and unmeasured (open
  question 1). Decision 8's test makes the uncarriable ones a NAMED list, so that the only
  thing that cannot happen is one of them disappearing quietly.
- **The VRAM floor is only expressible on a role that asks for an accelerator**
  (`AcceleratorTotalMemoryMiB` is refused without the accelerator fields, measured in ADR
  0071). A rung declaring 0, and a CPU-only role, **clear** it rather than asking for zero VRAM
  — which would be a filter nothing passes.

### 6. The warning POINTS at "this may not fit". It does not refuse

The test is **max(VRAM demand of the enabled models) against the selected rung's `vramMiB`** —
a maximum and not a sum, for the reason in the background (`LlmModelsMax: 1`, and sd-server's
one checkpoint). For the image role it is the one selected checkpoint.

A model's VRAM demand is answered in three tiers, and **which tier answered is on the screen**.

| Source | What it can say |
|---|---|
| `vram_mib` is declared | The operator's own measurement. Compared directly |
| No declaration, but the files carry `bytes` | **A floor, weights only.** No KV cache, no context. It says "at least N GiB" and nothing more |
| Neither | **Unknown.** It does not say "this fits" |

🔴 **Not drawing "unknown" as "fine" is the substance of this decision.** A 0 is not "needs
0 MiB".

It appears in three places:

- **When a model is enabled** (`PUT /models/{id}` with `enabled: true`) — the one moment a
  human is choosing. If it looks like it will not fit, ask for confirmation, and **offer the
  rung change right there**.
- **On the engine's card, permanently** — if any enabled model exceeds the rung, say so.
- **In the log, once, when a start is decided.** CUDA does not fail in a diagnosable shape, so
  "this start is putting 47,000 MiB on a 44,000 MiB rung" has to be written down beforehand.

**It does not refuse.** Same position as the licence handling (0072 decision 10: the panel
points, it does not decide) — quantisation, `--offload-to-cpu`, or something we do not know
about may well make it fit. It does ask for confirmation.

### 7. Running on a non-default rung is always visible, and one click puts it back

The price of "temporarily try a bigger box" is that **forgetting to put it back keeps costing by
the hour**. It is the same shape of trap as ADR 0071's `off` being a persistent setting rather
than a pause — where forgetting was indistinguishable from a stopped box — so it gets the same
treatment: **when the rung differs from the stack's default, the engine's card carries a badge,
with "back to the default ({id})" next to it.**

And **the hard-coded "$1.26/hour" leaves the i18n catalogue**
(`admin.engines_always_on_note`, both languages). The figure comes from the ladder's
declaration; with nothing declared, no figure is named.

### 8. A missed field must fail a TEST on the day the SDK grows one

Decision 5's read-modify-write silently **drops a constraint** for every field the copier
forgets — lose `BurstablePerformance: excluded` and the GPU requirements still hold, so nothing
looks wrong while the filter has quietly loosened. The SDK grows fields upstream.

So there is a unit test that **walks every field of `InstanceLaunchTemplate` by reflection** and
fails when (a) the copier leaves one zero, or (b) a field has no counterpart on the write type
and is not listed as uncarriable — the same device as `wiremap_convert_test.go` grepping for
`was: map[string]any{…}` to decide which types owe an equivalence proof. On the day a field is
added, what notices is the test, not a person. **The list at implementation time is
`CapacityOptionType` and `FipsEnabled`** (see decision 5).

⚠️ It has a positive control: deleting one line (`NetworkConfiguration`) from the copier does
make this test fail. A test that cannot fail is not a test that passed.

### 9. The IAM grant is scoped to the two capacity providers

It goes next to 60-engines' `CpIngestPolicy`, in the same shape (pinned to this stack's
resources).

- `ecs:DescribeCapacityProviders` / `ecs:UpdateCapacityProvider` — Resource is the ARN of
  **only** the llm and image capacity providers this stack creates.
- `iam:PassRole` — **only** `InfraRole` and `InstanceProfile`'s role, with
  `Condition: {StringEquals: {"iam:PassedToService": "ecs.amazonaws.com"}}`.

⚠️ **This widens the CP's authority.** What decision 5 writes back is "what was read, with four
fields replaced", but the API itself can re-declare a launch template, subnets and security
groups included. That is why both halves are needed: the resource scope, and the CP calling this
API **only with values from the declared ladder** (the operator declared the rungs, so arbitrary
values cannot be written).

### 10. The occupancy table (`engine_hourly`) is left alone

Its primary key is `(engine_key, hour)`, so a rung change mid-hour mixes two boxes into one row.
Fixing that means changing the primary key, and that is a question about the occupancy table,
not about the bill — **the bill's source of truth is Cost Explorer by tag** (ADR 0048 decision
15, `af-role: engine-llm`), which follows a rung change on its own. The change itself is
recorded in the **audit log** (`engine.<key>.class`).

If "which box was it running on at 14:00 last Tuesday" turns into a real question, it gets
designed then, primary key included (open question 5).

### 11. The task definition (`Cpu` / `Memory` / `--models-max`) does not follow the rung

A bigger rung leaves the task at `LlmTaskCpu 4096` / `LlmTaskMemory 14336`. Two reasons:

- **The need is unproven.** An 18.5 GB model loads in a 14,336 MiB task (measured in 0071);
  host RAM is not proportional to the weights.
- Following it means the CP rebuilding container definitions through
  `RegisterTaskDefinition`, which puts **the task definition's design in two places**, the
  stack's and the CP's — the shape decision 5 avoids.

The symptoms when this breaks are written down instead: **the task cannot be placed**
(a `RESOURCE:MEMORY` event) or **the container is OOM-killed**. Either one is a task-definition
question, not a rung question, and it is fixed in CloudFormation (open question 6).

## Rejected

- **One capacity provider per rung per role in CloudFormation, switching the service's
  `capacityProviderStrategy`.** It needs no new IAM (the CP already holds `UpdateService`), and
  the draft rejected it on template size alone. 🔴 **That reason is wrong**: `af_cfn_deploy` in
  `deploy/aws/ecs/env.sh` switches to S3 above 51,200 bytes, and 30-ingress went through it at
  54,681. **The real reason is that decision 5 cannot hold**: re-applying the choice before
  every start is inert on a capacity provider and **forces a new deployment on a service**, so
  "apply again, idempotently" would mean "kill whatever generation is in flight". (The wall is
  still there in another form — `deploy/local/ecs-lifecycle-stub-test.sh` case 3b-2 holds the
  shipped templates inside it — so the implementation moved prose to PARAMETERS-60-engines.md
  to make room.)
- **Leave it a CloudFormation parameter and have the Console only warn.** No IAM change, no
  drift. Rejected for the reason 0072 already wrote down: **making a swap a CloudFormation
  update collides with who actually does it** — an administrator at night while the GPU sleeps,
  not the deployer in the morning. If "try it temporarily" costs a stack update every time,
  nobody tries.
- **Look VRAM and vCPU up with `ec2:DescribeInstanceTypes`.** Authoritative, but it adds an IAM
  action to the CP task role and a stack update for a label — and **the operator writing the
  ladder already knows those numbers** (ADR 0045 decision 21, unchanged).
- **Have the CP pick the rung to match the model.** The bill goes up silently. A person picks
  (the same reason decision 7 exists).
- **Replace the running box automatically on a rung change.** It kills in-flight generations.
  Same position as 0072's "a re-selection applies at the next start; a running engine is not
  swapped", so the replacement is an explicit button (decision 4).

## Open questions (to be settled by measurement)

1. 🔴 **When `UpdateCapacityProvider` is given only part of an `InstanceLaunchTemplate`, are the
   sub-fields left out preserved or cleared?** Decision 5 reads everything and writes everything
   back, so it holds either way — but **whether `DescribeCapacityProviders`' output fits
   `Update`'s input** (the types differ, so it is copied) has not been tried on real hardware.
   Depends on: decisions 5 and 8.
2. **How CloudFormation drift actually shows.** Decision 5's "apply again, idempotently, just
   before a start" covers it, except for **a box bought between the moment a stack update
   reverts the capacity provider and the moment the CP re-applies** (a `mode=on` deployment).
   How long that window really is has not been measured. Depends on: decisions 2 and 5.
3. **g6e's VRAM, price and availability** per region. What the default ladder should say waits
   on this. **The example in this ADR (`l40s|L40S 48GB|44000|…`) is a proposal, not a
   measurement.** The shipped ladder is empty (decision 1). Depends on: decision 1.
4. **Whether the G-family vCPU quota has to be raised — settled (2026-09-10, answered by the
   operator in review).** Production's quota is 96, so rungs above 8 vCPU can be bought.
   **Rejecting them at declaration time is not the design** (decision 1's last bullet): the CP
   cannot read the quota and it differs per deployment. Dev and af-sandbox stay at 8, which is
   why the drain wait (decision 4) is the behaviour everywhere. What is left is an
   experiment-design question: what P1 can actually measure inside af-sandbox's 8.
5. Whether `engine_hourly` should carry the box after all (decision 10).
6. **Whether the task's `Memory` holds for a heavy model** (decision 11). "18.5 GB was fine" is
   as far as the evidence goes.
7. **The estimator for VRAM demand.** Decision 6's second tier is the sum of the weight files
   and no KV cache. Writing a coefficient means measuring one (it depends on context length,
   layer count and quantisation). **Until then it says "a floor" and nothing else.**

## Phases and the definition of done

- **P0 (what this ADR implements)**: the declared ladder (60-engines → the engine table), the
  stored choice, applying it through `UpdateCapacityProvider`, the stop / wait-for-it-to-leave /
  start flow, decision 6's warning, decision 7's badge and the removal of `$1.26`, decision 8's
  reflection test, decision 9's IAM. **Unit tests, no real hardware.**
  Done when: (1) a test can say that a deployment with an empty ladder makes no additional ECS
  call, (2) a test can say that choosing a rung sends the expected `InstanceRequirements` **and
  returns every other field exactly as it was read**, (3) enabling a model that will not fit
  asks for confirmation, and a model with neither `vram_mib` nor `bytes` reads as "unknown".
- **P1 (real hardware)**: switch a rung on af-sandbox, buy the box, and measure **the drain
  wait, `VcpuLimitExceeded` and the cold start**; close open question 3 and settle the ladder's
  defaults. Open questions 1 and 2 close here too. It costs GPU hours, and af-sandbox's quota is
  8, so **actually buying an upper rung there needs an increase** (production is 96).
- **P2 (if it turns out to be needed)**: the task definition following the rung (decision 11,
  open question 6), raising `--models-max` with the rung, and a per-rung breakdown in
  `engine_hourly` (decision 10).

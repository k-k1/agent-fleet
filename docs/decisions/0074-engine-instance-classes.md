# 0074. The GPU an engine buys is chosen at runtime, from a ladder the operator declares

English | [日本語](0074-engine-instance-classes.ja.md)

- Status: **accepted; P0 implemented, P1 measured on real hardware** (2026-09-10).
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
- Added the P1 experiment plan the same day (the section "The P1 experiment"). The operator
  decided to run it **with the quota left at 8**, so what 8 vCPUs can and cannot show is written
  down first — **a 4-vCPU upper rung needs no quota increase**, but then **decision 4's necessity
  cannot be shown by a 4→4 switch** (the two fit in exactly 8), so **the 8-vCPU rung becomes the
  positive control**, and **verifying the machinery needs no bigger card at all** (g5.xlarge
  measures everything if g6e is absent).
- Same day, **P1 was run on real hardware** (af-sandbox, the image role, 100 GPU-minutes, $4.6).
  The results are under "What P1 measured"; **open questions 1, 2 and 3 are closed and
  decisions 4, 5 and 9 changed.**
  🔴 **Two things were broken in the implementation, and neither could be found any other way** —
  `DescribeCapacityProviders` does not accept a cluster and a list of names together (every unit
  test passed because a fake accepts anything), and **calling `UpdateCapacityProvider` on a
  Managed Instances provider is also authorized as `ecs:PutClusterCapacityProviders` on the
  cluster** (an API the CP never calls, so neither the code nor the policy shows it). Decision
  9's "two actions plus PassRole is enough" was wrong.
  🔴 **Decision 4's necessity shows up as something other than the expected
  `VcpuLimitExceeded`** — bypass the gate and the task lands straight back on the old card,
  with no error anywhere. And **waiting for the instance to leave is necessary but not sufficient**:
  EC2 released the vCPU quota more than five minutes after ECS deregistered the instance.
- The same day, **a follow-up was added** (the "Follow-up" section; a throwaway provider,
  **$0 of GPU**). 🔴 **Open question 1's ✅ was an inference** — what was seen was the default
  `ON_DEMAND`, which does not separate "preserved" from "reset" (a flaw the record had already
  spotted for `fipsEnabled`). **Re-measured with the non-default `SPOT`: preserved**, turning the
  inference into a measurement. Also: **`fipsEnabled` cannot be set at all in ap-northeast-1**
  (the homework disappears), 🔴 **`L-DB2E81BB` is not a real quota code** (P1's *correction* was
  itself wrong), and 🔴 **"production's quota is 96" no longer matches** (acrt measures 64
  on-demand and 64 Spot).
- Related: [0071-self-hosted-inference-engines.md](0071-self-hosted-inference-engines.md)
  decision 2 (one capacity provider per role, the instance chosen by a VRAM floor), decision 5,
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
swapped at runtime while the instance it lands on stays fixed at deployment time**. Taking in
something as heavy as FLUX.1-dev (23.8 GB, measured in 0072 P4) is a Console operation from end
to end — and the card it would run on is still a g6.xlarge (L4), so starting it kills CUDA.

### What the code says today (read 2026-09-10)

- **The instance is decided by the capacity provider's `InstanceRequirements`.** `60-engines.yaml`'s
  `LlmAllowedInstanceTypes` (default `g6.xlarge,g5.xlarge`), `LlmAcceleratorMemMinMiB` (21000),
  `LlmVCpuMin/Max` (4/8), `LlmMemMinMiB/MaxMiB` (15000/65536), and the image role's mirror.
  **All CloudFormation parameters — fixed at deployment**, changed only by a stack update.
- **The service names its role's provider** (`CapacityProviderStrategy: [{CapacityProvider:
  !Ref LlmCapacityProvider, Weight: 1}]`), so the provider's requirements are the only door
  to a different instance.
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
  "$1.26/hour" in both languages. The moment the instance becomes selectable, that sentence is false.
- **`engine_hourly`'s primary key is `(engine_key, hour)`.** Which instance those hours ran on is
  not recorded.

### What upstream has (read from aws-sdk-go-v2/service/ecs v1.87.0)

- **`UpdateCapacityProvider` can update a Managed Instances provider**
  (`ManagedInstancesProvider *types.UpdateManagedInstancesProviderConfiguration`).
- 🔴 **"These changes only apply to new Amazon ECS Managed Instances, or EC2 instances, not
  existing ones"** — **the running instance does not change.** That sentence is the whole of
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

- **The G-family vCPU quota is 8.** Starting right after a stop puts the draining instance's 4 vCPU
  next to the new one and fails with `VcpuLimitExceeded`. Draining measured at 427-477 seconds.
- **MI has no allocation strategy: the CHEAPEST member that fits is bought.**
  `AllowedInstanceTypes` is a FILTER, not an order of preference.
- **An MI instance does not appear in an unfiltered `ec2 describe-instances`** (it answers when named
  directly). Whether an instance exists is read from ECS's container instances.
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
  capacity provider (which instance may be bought) and what decision 6's fit check compares a model
  against. It is declared **deliberately below** the card's nominal size, as the existing 21000
  is for an L4's 24 GB, so the check errs toward warning early.
- **`usdPerHour` is optional and display-only.** Neither EC2 nor the Pricing API is asked (ADR
  0045 decision 21 again: it means an IAM action on the CP task role and a stack update for a
  label). **Empty is fine**: with no figure the panel names no price and says only that the
  hourly rate depends on the class. **No number beats a wrong number.**
- **Each rung carries its own vCPU and memory bounds.** A 48 GB card only exists in instances with
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
ships a cheaper 48 GB type the deployment **silently changes instance**. A rung is a SET of
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

### 4. A rung change reaches the NEXT instance only. Switching is stop → wait for it to leave → start

The SDK says the running instance does not change, so the flow has three steps and **the screen
shows all three** (announcing "changed" while the old instance keeps serving is the most expensive
lie available here).

1. Save the rung. If the engine is not running, that is the end of it: the next start buys the
   new instance.
2. If it IS running, make the operator press **"replace it now"** explicitly. That sets
   desired 0.
3. 🔴 **Wait for the container instance to disappear before starting.** Starting sooner puts a
   draining 4 vCPU instance next to a new 8 vCPU one, exceeds **the quota, and fails with
   `VcpuLimitExceeded` — silently, as a placement that never happens**: until somebody reads
   the service events it merely looks like a slow start. Draining measured at 427-477 s (about
   8 minutes from desired 0 to the instance being gone, on the image role).

**It waits on a deployment with headroom too.** The G-family vCPU quota differs per deployment
(96 in production, 8 in dev and af-sandbox) and **the CP cannot read it** (no `service-quotas`
in the IAM grant, for decision 1's reason). A branch on a number we cannot read **fails silently
exactly on the deployment where the branch is wrong**. One order is safe on both, so that order
is the behaviour. It costs wall clock and nothing else: **the two instances never bill at once**,
because the new one is bought after the old one is gone.

⚠️ **A replacement buys one cold start** (measured 527-586 s for llm, 165-197 s for image).
Storage is the instance store, so **the new instance fetches the model from S3 again** — measured at
6.62 GiB in 66 s (102.7 MiB/s). With the drain, one switch is 15-20 minutes of wall clock and
roughly $0.3-0.4 of GPU time at g6.xlarge rates. The screen says so. It is ADR 0071's
`offGrace: 0` position — changing your mind costs a cold start — applied to the instance instead of
the mode.

⚠️ **Saving a rung while the engine is stopped costs nothing at all.** It is absorbed by the
cold start whoever uses it next was going to pay anyway.

**What P1 measured (2026-09-10, the image role)**:

- **The drain took 157 seconds** (`DEREGISTERING` at 133); the second one took 149. That is
  much faster than ADR 0071's 427–477. The 20-minute bound stays reasonable, but its
  justification is now "eight times the measurement", not a guess.
- 🔴 **Bypassing the gate does not first produce a quota error. It produces a silent return to
  the old card.** Forcing `update-service --desired-count 1` without waiting placed the task
  **straight back onto the previous rung's instance** that was still registered (`steady state` in
  64 s). What the provider asks for decides **what is bought**, never **where a task is
  placed**. Nothing appears in the service events or the log, and the panel says "running".
  **Without decision 4 an administrator who raised the rung keeps running on the old card** —
  the most expensive lie available.
- The positive control (`VcpuLimitExceeded`) needs the old instance to be **busy**. With a task
  running on the previous rung's instance, asking for a second one produces it exactly as expected:
  `VcpuLimitExceeded: ... current vCPU limit of 8 ... for the instance bucket`. The P1 plan's
  "start without waiting and it appears" was **wrong**: while an idle instance of any rung is
  registered, ECS does not try to buy a second one at all.
- 🔴 **There is a stretch where waiting was not enough.** Buying an 8 vCPU rung right after the
  4 vCPU instance left ECS kept failing with `VcpuLimitExceeded` for **more than five minutes**
  (gone 09:19:43; failures at 09:19:50 / 09:20:30 / 09:21:11; success 09:26:12, +389 s).
  **EC2 releases the quota later than ECS deregisters the instance**, and "is an instance registered"
  — the only thing the CP can see — is not EC2's accounting. ECS retries, so nothing breaks,
  but **a start that raises the rung costs drain (150 s) + quota wait (up to ~6 min) + cold
  start**, and `StartDeadlineSec` has to exceed the sum (measured 497 s against a 900 s
  default). It does not happen on a 4→4 swap.

**What the implementation added (P0)**:
- The wait is judged from the **instance's EC2 type**, read from the container instance's
  `ecs.instance-type` attribute — the only place the CP can read the type of an MI instance (it
  does not appear in `ec2 describe-instances`). An instance whose type the selected rung does
  not cover is the previous rung's instance, still there.
- 🔴 **The wait is bounded at 20 minutes.** Its end belongs to AWS, and with
  `scaleInAfter: -1` ("never tidy up") an unbounded gate is **an engine that can never start
  again**, with one log line as the evidence. Measured draining is 427-477 s, so 20 minutes
  waits out a slow one and still gives up.
- Replacing is its own act (`POST …/replace-box`) and **does not touch the mode**. Making
  somebody press "off" instead would leave the mode off afterwards, reproducing ADR 0071's trap
  where forgetting to switch it back is indistinguishable from a stopped instance.
- 🔴 **The admin `mode=on` route consults the same gate.** It calls `setEnabled(true)` directly,
  so without this one button **walks around the wait and buys the old rung's instance**. A gate that
  says wait does not fail the request: the mode is stored and the controller starts the engine
  once the old instance has gone.

### 5. Applying it is a read-modify-write: Describe → copy → Update

Because `InstanceLaunchTemplate` is a required member, the CP reads the current configuration
with `DescribeCapacityProviders` and writes it back **with only the four `InstanceRequirements`
fields replaced** (`AllowedInstanceTypes`, `AcceleratorTotalMemoryMiB.Min`, `VCpuCount`,
`MemoryMiB`). Network, storage, instance profile and the GPU declaration (`AcceleratorCount`,
`AcceleratorTypes`, `AcceleratorManufacturers`) **go back exactly as they were read**. The CP
holds no design for the instance — giving it one would make two designs, its own and the stack's.

**It is applied at two moments** — when a rung is saved, and **idempotently, just before a start
is decided**. The second exists because CloudFormation puts its own declaration back (a stack
update does not replace the CP, so decision 2's "a stored choice wins" alone would let the next
instance be bought on the reverted rung).

**Re-applying costs nothing.** `UpdateCapacityProvider` does not buy an instance, it rewrites what the
next instance will look like; ECS API calls are free, and writing the same values back touches
neither the running instance nor the desired count. The price is one more call on the start path.

🔴 **When re-applying fails**: **if the chosen rung differs from the last one this process
applied, do not start** — starting anyway buys the old instance while believing it is the new one,
and with a heavy model CUDA dies and **a whole cold start is thrown away**. If they are the
same, log the failure and start (the instance's specification is already right).

**What P1 measured (2026-09-10)**:

- ✅ **The read-modify-write is faithful.** Moving `l4`→`l40s` changed three of the four fields
  (`allowedInstanceTypes` `g6.xlarge`→`g6e.xlarge`, `acceleratorTotalMemoryMiB.min` 8000→44000,
  `memoryMiB.min` 15000→30000; `vCpuCount` is 4-8 on both rungs) and **nothing else moved** —
  `acceleratorCount`, `acceleratorTypes`, `acceleratorManufacturers`, `burstablePerformance`,
  `ec2InstanceProfileArn`, `localStorageConfiguration`, `networkConfiguration` and
  `instanceMetadataTagsPropagation` all came back identical.
- ✅ **The fields that cannot be carried are not cleared** (open question 1). `capacityOptionType`
  stayed `ON_DEMAND`; a same-values write left the whole `managedInstancesProvider` identical.
  🔴 **This observation had no positive control** (`ON_DEMAND` is the default). The same-day
  follow-up re-measured it with the non-default `SPOT` and confirmed the conclusion holds — see
  the "Follow-up" section.
- ✅ **The re-apply before a start really did repair a CloudFormation revert.** Started from a
  drifted state (choice `l40s`, provider back at the stack's declaration), the CP rewrote the
  rung before starting and **the instance bought was a `g6e.xlarge`**.
- 🔴 **The implementation was wrong here**: it passed `Cluster` and `CapacityProviders` to
  `DescribeCapacityProviders` **together**, which ECS refuses (`InvalidParameterException:
  Cannot specify both capacity providers and cluster in the same request`). The constraint is
  in neither the API reference nor the SDK's comment, and **every unit test passed because the
  fake accepted anything**. It now asks by name and **checks the cluster on the answer**.

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

**What P1 measured (2026-09-10)**: all three places were seen against a real CP — the standing
line on the card, the log line before a start (`starting on l40s (44000 MiB VRAM declared);
largest model juggernaut-xl-v9 wants 6776 MiB (floor)`), and the confirmation when enabling
`flux1-dev` (22,700 MiB) on an 8,000 MiB rung.

🔴 **The weights-only floor is accurate about weights and accounts for 58% of the real
demand.** Against juggernaut-xl-v9's declared floor of 6,776 MiB, sd.cpp reported
`total params memory size = 6624.11MB` (within 2%) — but auto-fit reserved **5,120 MiB more for
compute** (DiT 2,048, Conditioner 2,048, VAE 1,024), for about **11.7 GiB in use**. Read the
floor as "it fits" and you are about 5 GiB short. This is open question 7's first measurement,
and it says the decision to call it nothing but a floor was right.

### 7. Running on a non-default rung is always visible, and one click puts it back

The price of "temporarily try a bigger instance" is that **forgetting to put it back keeps costing by
the hour**. It is the same shape of trap as ADR 0071's `off` being a persistent setting rather
than a pause — where forgetting was indistinguishable from a stopped instance — so it gets the same
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
- 🔴 `ecs:PutClusterCapacityProviders` — **the cluster's ARN** (added by P1, 2026-09-10).
- `iam:PassRole` — **only** `InfraRole` and `InstanceProfile`'s role, with
  `Condition: {StringEquals: {"iam:PassedToService": "ecs.amazonaws.com"}}`.

🔴 **The third one is a permission this ADR did not know about until P1.** ECS authorizes
`UpdateCapacityProvider` **on a Managed Instances provider** as `PutClusterCapacityProviders`
**on the cluster** as well. The CP never calls that API, so grepping the code does not find it:

```
AccessDeniedException: User: .../af-<stack>-cp-task/... is not authorized to perform:
ecs:PutClusterCapacityProviders on resource: .../cluster/af-<platform-stack>
```

⚠️ **It partly betrays this decision's own heading.** The other three are provider-scoped; this
one is **cluster-scoped**, and the same action is how a cluster's whole provider ASSOCIATION
list is replaced. The only thing that makes it acceptable is that 60-engines already owns that
list (README: "⚠️ It owns the cluster's capacity-provider associations"). Leaving it out was
not an option — without it the feature does not work at all.

⚠️ **This widens the CP's authority.** What decision 5 writes back is "what was read, with four
fields replaced", but the API itself can re-declare a launch template, subnets and security
groups included. That is why both halves are needed: the resource scope, and the CP calling this
API **only with values from the declared ladder** (the operator declared the rungs, so arbitrary
values cannot be written).

### 10. The occupancy table (`engine_hourly`) is left alone

Its primary key is `(engine_key, hour)`, so a rung change mid-hour mixes two instances into one row.
Fixing that means changing the primary key, and that is a question about the occupancy table,
not about the bill — **the bill's source of truth is Cost Explorer by tag** (ADR 0048 decision
15, `af-role: engine-llm`), which follows a rung change on its own. The change itself is
recorded in the **audit log** (`engine.<key>.class`).

If "which instance was it running on at 14:00 last Tuesday" turns into a real question, it gets
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
- **Replace the running instance automatically on a rung change.** It kills in-flight generations.
  Same position as 0072's "a re-selection applies at the next start; a running engine is not
  swapped", so the replacement is an explicit button (decision 4).

## Open questions (to be settled by measurement)

1. ✅ **Settled (2026-09-10, measured in P1): the fields that cannot be carried are preserved.**
   `capacityOptionType` stayed `ON_DEMAND`, and a same-values write left the whole
   `managedInstancesProvider` byte-identical. Decision 5 needs no repair.
   ⚠️ **`fipsEnabled` was never set on this deployment, so it was not measured directly** — this
   is an inference from its sibling in the same hole (absent from the write type). Its default
   is false, so a silent reset would be indistinguishable from "was false already". A deployment
   that uses FIPS should confirm this one before switching rungs.
   Whether the copy fits (the types differ) is settled too: it does. But **`Describe` was being
   called wrongly** — see decision 5's P1 measurements. Depends on: decisions 5 and 8.
   🔴 **A follow-up the same day found that this "measurement" was the same inference.**
   `ON_DEMAND` is the **default** for `capacityOptionType`, so the sentence written just above
   about `fipsEnabled` — a silent reset is indistinguishable from "was that already" — applies
   verbatim to it: **the flaw was spotted in the sibling field and the same yardstick was never
   held against the field itself.** Re-measured with a non-default value; it is preserved. The
   `fipsEnabled` homework disappears too, because the field **cannot be set at all** in Tokyo.
   See the "Follow-up" section.
2. ✅ **Settled (2026-09-10, measured in P1): there are two kinds of drift and one of them does
   not happen.**
   - **A plain redeploy does not revert anything.** With no property of the provider changed the
     changeset is empty and CloudFormation does not touch it (1.9 s). The filed worry — "an
     unrelated stack update quietly puts it back the next morning" — **does not occur**.
   - **Change any parameter that touches the provider and the whole `InstanceRequirements` goes
     back to the declaration.** An update that moved `ImageMemMinMiB` by 1 took
     `allowedInstanceTypes` and the VRAM floor back to the defaults with it (48 s). It is
     total, not partial.
   - The re-apply before a start repaired exactly that state (decision 5).
   - **The window that remains** is an instance bought between the CP applying and ECS buying, which
     the CP cannot close (buying is asynchronous). Its length tracks the CFN update (48 s here).
   Depends on: decisions 2 and 5.
3. ✅ **Settled (2026-09-10, measured in P1) — and the shipped ladder stays empty.**
   - **Availability**: `g6e.xlarge` and `g6e.2xlarge` are both offered in ap-northeast-1a and 1c.
   - **VRAM**: the L40S reports **45,457 MiB** (`ggml_cuda_init`). The ladder's 44,000 follows
     the "declare below nominal" rule and is right.
   - **Price** (Pricing API, Tokyo, Linux, on-demand, 2026-09-10): `g6.xlarge` $1.1672,
     `g5.xlarge` $1.4590, `g6e.xlarge` $2.6990, `g6e.2xlarge` $3.2517.
   - 🔴 **Those are not the numbers to put in a ladder.** ADR 0071's measured **$1.26** for a
     g6.xlarge is **8% above** list, which is consistent with the ECS Managed Instances
     management fee being charged on top of EC2. **List price makes the panel cheaper than the
     bill.** One data point does not give a coefficient, so the advice is: **declare what was
     measured, and leave the field empty otherwise.** Cost Explorer's next-day figures are the
     only thing that closes the rest.
   Depends on: decision 1.
4. **Whether the G-family vCPU quota has to be raised — settled (2026-09-10, answered by the
   operator in review).** Production's quota is 96, so rungs above 8 vCPU can be bought.
   **Rejecting them at declaration time is not the design** (decision 1's last bullet): the CP
   cannot read the quota and it differs per deployment. Dev and af-sandbox stay at 8, which is
   why the drain wait (decision 4) is the behaviour everywhere. What is left is an
   experiment-design question: what P1 can actually measure inside af-sandbox's 8.
5. Whether `engine_hourly` should carry the instance after all (decision 10).
6. **Whether the task's `Memory` holds for a heavy model** (decision 11). "18.5 GB was fine" is
   as far as the evidence goes.
7. ~~**The estimator for VRAM demand.**~~ **Resolved for the llm role (2026-09-11, the
   follow-up "open question 7 for the llm role" at the end) — the KV cache is not a coefficient
   but a quantity computed from the GGUF header, and it matched the hardware at both points to
   0.00%. The image role is a different problem, as the follow-up explains.**
   Decision 6's second tier is the sum of the weight files
   and no KV cache. Writing a coefficient means measuring one (it depends on context length,
   layer count and quantisation). **Until then it says "a floor" and nothing else.**
   **First measurement (2026-09-10, P1)**: for SDXL-family weights (juggernaut-xl-v9, fp16) the
   declared floor was 6,776 MiB, the weights really were 6,624 MiB, **compute reserves added
   5,120 MiB** (DiT 2,048, Conditioner 2,048, VAE 1,024) and the total in use was about
   11.7 GiB. **The floor is 58% of the demand.** The reserve should scale with resolution and
   batch, so no coefficient is derived from one point. The llm role (where the KV cache is the
   term that matters) was settled separately, by computing rather than measuring — see the
   follow-up.

## The P1 experiment (with the quota left at 8, 2026-09-10)

The operator has decided **not to raise the G-family vCPU quota**: it stays at 8. That is a
constraint and **also a test of the design** — 8 is the ordinary state of a development
deployment, and a feature that breaks there breaks in production too, in some other shape. What
follows starts from what the number 8 does to this experiment.

### What 8 vCPUs allow and forbid

| Combination | Total vCPU | Possible at 8 |
|---|---|---|
| One g6.xlarge (L4 24GB, 4) | 4 | yes — ordinary running |
| g6.xlarge draining + a new g6e.xlarge (L40S 48GB, 4) | 8 | yes — **exactly at the limit** (i.e. buyable without waiting) |
| g6.xlarge draining + g6e.2xlarge (8) | 12 | no — `VcpuLimitExceeded` (**this is decision 4's positive control**) |
| Both roles on an upper rung at once | over 8 | no — **one role at a time** |

⚠️ Three things follow.

1. **With a 4-vCPU upper rung (g6e.xlarge) the whole experiment fits inside 8.** No quota
   increase is needed.
2. **A 4→4 switch cannot demonstrate that decision 4 is necessary** — the two instances add up to
   exactly 8 and both fit. Showing the need means choosing the **8-vCPU rung (g6e.2xlarge) as
   the new one and starting without waiting** (`update-service --desired-count 1` straight
   through the AWS CLI). That is the positive control, and **if `VcpuLimitExceeded` does not
   appear, the experiment is measuring something else.**
3. **One role at a time**, and it should be **image**: its cold start is 165-197 s against the
   llm role's 527-586 s, so the same facts cost three times the time and money there.

### The candidate rungs, and what to do if g6e is not there

```
ImageInstanceClasses=l4|L4 24GB|21000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;
                     l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536
```

🔴 **Whether `g6e.xlarge` exists in this deployment's region has not been established** (open
question 3). If it does not, the instance simply never arrives — an
`InsufficientInstanceCapacity`, or no matching type at all — **which is indistinguishable from
the feature failing**. Check the offering first:
`aws ec2 describe-instance-type-offerings --location-type availability-zone --filters
Name=instance-type,Values=g6e.xlarge`.

⭐ **Verifying the machinery needs no bigger card.** If g6e is absent or expensive, make the
upper rung **`g5.xlarge` (A10G 24GB, 4 vCPU)**. The VRAM is the same but the TYPE is different,
so applying a rung, the instance changing type, the drain wait and the drift all measure exactly the
same. **The only thing 48 GB is needed for is the single claim that decision 6's warning saved
a start**, and that can be added after everything else has passed.

### The order (parts of it work in no other order)

1. **Deploy 60-engines first** (`Image/LlmInstanceClasses` and the IAM).
2. **Replace the CP** (this branch's control-plane image). The engine table is **read once, at
   startup**, so adding `classes` to it exists nowhere in the product until then — the same trap
   as ADR 0071's "the second pass is not visible until the CP is restarted".
3. Open the Console and check **that the ladder appears at all**. If it does not, step 1 or 2 did
   not land.
4. Then measure.

### What to measure (with the open question each closes)

| # | To measure | Where / how it is judged |
|---|---|---|
| 1 | 🔴 **Open question 1**: are `CapacityOptionType` and `FipsEnabled` preserved or cleared? | `describe-capacity-providers` **before and after** a switch, diffed. If they are cleared, decision 5 needs "write the uncarriable fields back explicitly" — which today it cannot |
| 2 | Whether the read-modify-write actually fits (the types differ, so it is copied) | The switch answers 200 and `instanceRequirements` matches the rung **with every other field still there** |
| 3 | **Whether the IAM grant is enough** | The switch is not `AccessDenied`. A missing PassRole shows up here and nowhere else |
| 4 | 🔴 **Open question 2**: CloudFormation drift | Re-`deploy` 60-engines after a switch, then describe the provider — did it revert? If it did, start the engine once and confirm **the CP re-applies before the next instance is bought** |
| 5 | **Decision 4's drain wait** | "Replace it now" → seconds until the container instance is gone (expect 427-477), and that the CP does not start during it (the log line `holding the start back (class_swap_wait…)`) |
| 6 | **Decision 4's positive control** | Select the 8-vCPU rung and start **without waiting** via `update-service --desired-count 1`. `VcpuLimitExceeded` must appear in the service events. **If it does not, the experiment is wrong** |
| 7 | The cold start on the new rung | The instance's type really changed (`ecs.instance-type` on the container instance), seconds to listening, seconds to fetch the model |
| 8 | Open question 3 (the defaults) | The upper rung's **actual hourly rate** (Cost Explorer by tag, the next day) and its availability. Both go into the ladder's `usdPerHour` |
| 9 | The panel | The "not the default" badge, the replace flow and the VRAM warning **against a real CP** (P0 checked the rendering against a stub) |

### Stop conditions and cost

- **Rough cost**: one 4-vCPU GPU instance for two or three hours, **$3-5** (g6.xlarge at $1.26/hour;
  the upper rung's rate is unconfirmed). ⚠️ **"Stopped" and "gone" are different times** —
  billing continues for about 8 minutes after the service reaches 0. When the measuring is
  finished, set the mode to off **and watch until the container instance disappears**.
- **Stop conditions**: (a) #3 fails → suspect that 60-engines was not redeployed;
  (b) the upper rung's instance does not arrive within 15 minutes → suspect availability or a typo in
  the type, and fall back to `g5.xlarge`; (c) #6's positive control does not fire → **suspect the
  experiment**, and read the real quota with
  `service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA`.
  🔴 **The `L-DB2E81BB` written when this was filed is wrong** — that is the Spot quota (0 on
  this deployment). On-demand G/VT is `L-DB2E81BA`, measured at 8 on af-sandbox.
  🔴 **That correction is wrong too** (same-day follow-up) — `L-DB2E81BB` **does not exist**
  (`NoSuchResourceException` on both accounts). Spot is `L-3819A6DF`. See "Follow-up".
- **Afterwards**: put the rung back to the default (the Console's "Back to the default"), set the
  mode to off, and decide whether the ladder goes back to empty (empty removes the feature).

## What P1 measured (2026-09-10, af-sandbox, the image role)

Run as planned: the image role only, with the quota left at 8. **100 GPU-minutes, $4.6**
(`g6e.xlarge` 90.5 min = $4.07, `g6e.2xlarge` 10.2 min = $0.55 — the measurements themselves
took 13 minutes; the rest is an instance idling while a human was asked to click). The record is
[docs/log/95-engine-instance-classes.md](../log/95-engine-instance-classes.md) (Japanese).

| # | Measured | Result |
|---|---|---|
| 1 | Open question 1 (`capacityOptionType` / `fipsEnabled`) | **Preserved**. `fipsEnabled` inferred, never set here |
| 2 | Does the RMW fit | **It does.** Four fields move, the other eight do not |
| 3 | Is the IAM enough | 🔴 **It was not.** `PutClusterCapacityProviders` is required (decision 9) |
| 4 | Open question 2 (CFN drift) | **A plain redeploy reverts nothing; a provider-touching update reverts all of it.** The re-apply before a start repaired it |
| 5 | The drain wait | **157 s** (149 s the second time). The gate logged `class_swap_wait after admin_on` |
| 6 | The positive control | **It appeared** — but only with the old instance **busy** (the plan's premise was wrong) |
| 7 | Cold start on the new rung | The type really changes. 118 s the first time, 497 s the second (the difference is quota wait) |
| 8 | Open question 3 (defaults) | Availability, VRAM and price all obtained. **List price is 8% under the bill** |
| 9 | The panel | Badge, replace flow, VRAM warning and confirmation all seen against a real CP |

### The two defects only real hardware could show

**Every P0 unit test stayed green through both.**

1. **`DescribeCapacityProviders` was being given a cluster and a list of names.** ECS refuses
   that (`InvalidParameterException: Cannot specify both capacity providers and cluster in the
   same request`) — a constraint in neither the API reference nor the SDK comment, and **a test
   whose fake accepts anything can never notice**. The fake is now as strict as the API, which
   makes every existing test a guard (with a positive control).
2. **`ecs:PutClusterCapacityProviders` is required** (decision 9's correction). It is **an API
   the CP never calls**, so neither the code nor the policy leads to it.

**These two are why P1 exists.** Neither is reachable by reading the implementation, and both
take thirty seconds to find once something is actually started.

### Where the plan was wrong about decision 4

The plan said "start without waiting and `VcpuLimitExceeded` appears". **It did not.** While an
idle instance of the previous rung is registered, ECS does not try to buy a second one — it **places
the task on the old instance**. So the first failure decision 4 prevents is not a quota error but a
**silent return to the old card**: the service reports `steady state`, the panel says running,
and an administrator who thought they had moved up a rung keeps running on the old one. The
positive control was taken instead with the old instance **busy**, asking for a second task.

And **waiting for the instance to leave is necessary but not sufficient.** After the 4 vCPU instance left
ECS, `VcpuLimitExceeded` continued for **more than five minutes** (gone 09:19:43, bought
09:26:12). **EC2 releases the quota later than ECS deregisters.** Budget a rung-raising start as
drain (150 s) + quota wait (up to ~6 min) + cold start.

### Known and deliberately not fixed

- **A rung whose apply failed cannot be retried by choosing it again.** `putClass` stores the
  setting before applying, so a failure still leaves the choice stored, and the Console — seeing
  no change — sends nothing. Recovery is to pick another rung and come back. The order is
  deliberate (so the panel can say the apply failed), so the fix belongs in the Console: a
  retry affordance. → ✅ **Fixed the same day** (see "Follow-up — retrying a rung whose apply
  failed" below).
- **A ladder's `usdPerHour` taken from list price reads cheaper than the bill** (open question 3).

## Follow-up (2026-09-10, same day, $0 of GPU)

Four things were measured after P1, while weighing a move to Spot. **No GPU was bought** — the
live providers were not touched. A **throwaway capacity provider** (`af-spot-probe-0074`) was
created, measured and deleted: five minutes, $0, and the cluster's provider list was identical
before and after.

- 🔴 **Open question 1's "measurement" was an inference.** What P1 saw was `capacityOptionType`
  staying `ON_DEMAND` — but **`ON_DEMAND` is that field's default**, so the observation does not
  separate "preserved" from "reset to the default". The record spotted exactly this flaw for
  `fipsEnabled` and **never held the same yardstick against the field it was reasoning from**.
  ✅ **Re-measured with a non-default value: it is preserved.** A provider created with
  `capacityOptionType: SPOT` survived **two** updates that passed only the eight fields
  `instanceLaunchTemplateUpdate` can carry. **Positive control**: the same updates moved
  `allowedInstanceTypes` (`g6.xlarge`→`g6e.xlarge`) and `acceleratorTotalMemoryMiB`
  (8000→40000) as intended, and nothing else differed — so the update was not a no-op. The
  conclusion stands: decision 5 needs no repair.
- ✅ **The `fipsEnabled` homework disappears in Tokyo.** Creating a provider with
  `fipsEnabled: true` is refused — `ClientException: Managed Instances Provider does not support
  FIPS in this region`. In ap-northeast-1 the field **cannot be set at all**, so the fields that
  cannot be carried are a live concern in zero cases. Deployments in other regions still need it.
- 🔴 **The quota code, corrected again: `L-DB2E81BB` does not exist.** Stop condition (c) of the
  P1 experiment plan and docs/log/95 *correct* the filed code to "`L-DB2E81BB`, the Spot quota
  (0)" — and that correction is itself wrong. Both accounts answer `NoSuchResourceException`.
  ap-northeast-1 has exactly two G/VT quotas: **`L-DB2E81BA` (on-demand) and `L-3819A6DF` (All G
  and VT Spot Instance Requests, default 0)**. **"A 0 came back" does not separate "this is the
  Spot quota" from "this quota does not exist"** — a one-character slip produced a second error of
  the same shape.
- 🔴 **"Production's quota is 96" no longer matches.** Measured: acrt (production) is
  **on-demand 64, Spot 64**; af-sandbox is on-demand 8, Spot 0 (an increase to 16 is pending).
  Decision 1 ("do not reject a declaration on quota grounds") does not rest on these numbers, so
  the decision is unchanged.
- By-product: **a provider with `capacityOptionType: SPOT` can be created while the Spot quota is
  0.** The quota only bites when an instance is launched, so **verifying the configuration side does not
  have to wait for a quota increase.**
- ⚠️ A deleted capacity provider is kept by ECS as an `INACTIVE` record (it does leave the
  cluster's list). Deleting is not quite trace-free.

## Backlog from a Console review of the llm panel (2026-09-10)

Two findings from reading the `llamacpp` panel against this ADR. Neither changes a decision;
both are P2 work.

**1. 🔴 Decision 6's floor counts the weights only, and on the llm role the KV cache is what does
not fit.** `engineModelVramNeed` answers `floor` as the sum of the files' `bytes` — honest about
what it measures, silent about what it omits. A 13.1 GB GGUF registered with a 262,144-token
window reads as "at least about 12,500 MiB", which fits an L4's declared 21,000, while the KV
cache at that window is a multiple of the weights. So the warning can wave through exactly the
case it exists for. The window is already on the row (`context_tokens`), so the material for a
second term is stored; what is missing is a measurement to turn it into a number. Decision 6's
own rule holds over the addition: a demand whose window was never declared stays `unknown`
rather than becoming a confident sum. The image role is unaffected — a checkpoint's VRAM does
not scale with a context window.

**2. The maximum (not the sum) is exact for llm and sd-server, and CONSERVATIVE for comfy.**
Decision 6 reads "for the image role it is the one selected checkpoint", which was true of
sd-server. On an `ImageEngine=comfy` deployment every enabled model is on disk (the fetch
sidecar's `SYNC_ALL`), the checkpoint is chosen per request, and ComfyUI keeps what it has
loaded until it needs the room — so several can be resident at once. **This is deliberately not
changed to a sum**: comfy evicts rather than dies, and a sum would warn on every start of a
deployment with four enabled models, which is the kind of warning nobody reads. What is missing
is one sentence on the panel saying that a comfy engine may hold more than one.

## Phases and the definition of done

- **P0 (what this ADR implements)**: the declared ladder (60-engines → the engine table), the
  stored choice, applying it through `UpdateCapacityProvider`, the stop / wait-for-it-to-leave /
  start flow, decision 6's warning, decision 7's badge and the removal of `$1.26`, decision 8's
  reflection test, decision 9's IAM. **Unit tests, no real hardware.**
  Done when: (1) a test can say that a deployment with an empty ladder makes no additional ECS
  call, (2) a test can say that choosing a rung sends the expected `InstanceRequirements` **and
  returns every other field exactly as it was read**, (3) enabling a model that will not fit
  asks for confirmation, and a model with neither `vram_mib` nor `bytes` reads as "unknown".
- **P1 (real hardware)**: ✅ **done (2026-09-10)**. Rungs were switched on af-sandbox, real
  instances were bought, and the drain wait, `VcpuLimitExceeded` and the cold start were measured;
  open questions 1, 2 and 3 are closed. **The shipped ladder stays empty** (decision 1) — price
  differs from the bill and availability differs per region, so "do not ship a number nobody
  measured" survives P1 intact. Results: "What P1 measured"; record: docs/log/95.
- **P2 (if it turns out to be needed)**: the task definition following the rung (decision 11,
  open question 6), raising `--models-max` with the rung, and a per-rung breakdown in
  `engine_hourly` (decision 10).
  - Added 2026-09-10 from the Console review above: counting the KV cache in decision 6's demand
    (finding 1). Finding 2 — saying on the panel that a comfy engine may hold several models at
    once — is ✅ **done the same day** (`admin.engines_class_vram_many`, shown only where the
    provider is `comfy`); the figure itself stays a maximum.
- **Not a phase — the operational act that is still outstanding.** 🔴 **No deployment declares a
  ladder**, so the class control is on nothing anywhere (decision 3, and the shipped default is
  empty by decision 1) — the feature exists and nobody can see it. Declaring one is a per
  deployment act, in that deployment's `params/60-engines`, and three things decide what to
  write:
  - **The G-family vCPU quota, which is per account and per purchase model.** af-sandbox is
    on-demand 8 / Spot 0 (an increase to 16 pending); acrt is 64 / 64 (measured 2026-09-10 —
    "Follow-up"; the earlier "production is 96" no longer matches). A rung above the quota is
    selectable and simply never places, as `VcpuLimitExceeded` in the service events.
  - **`usdPerHour` is what was BILLED or nothing** (ECS Managed Instances adds a management fee
    of about 8% over the list price — measured). A missing price prints nothing, which beats a
    wrong one.
  - **The first rung must restate what the stack already buys** (`<Role>AllowedInstanceTypes`,
    `AcceleratorMemMinMiB`, `VCpu*`, `Mem*`): it is what "back to the default" returns to, and
    nothing checks that the two agree.

## Follow-up — retrying a rung whose apply failed (2026-09-10)

The first item under "Known and deliberately not fixed" is fixed. **The CP's order is
unchanged** — storing the choice before applying it is exactly what lets the panel say the apply
failed. What was added is the one field needed so a failure is not carried silently, plus the
retry affordance in the Console.

**Why re-picking is not a retry.** Because the save comes first, the GET
`/api/admin/engines` right after a failure already carries the new rung as `class`. The panel's
`<select>` shows the stored rung, and picking it again fires no `change` event — so there is
**exactly one request the UI cannot send**: "the rung already on screen, once more". Recovery
meant moving to another rung and back, and that detour **writes a rung nobody wanted** to the
capacity provider on the way.

What was added:

- `engineRuntimeState.classApplyErr` (`engines.go`), recorded at both exits of `applyClass`
  (`engine_class.go`): set on failure, cleared on success. **In this process's memory only**, for
  the same reason `appliedClass` is, and **its absence reads as "no claim", never as "applied"** —
  a restarted CP must not assert a failure it did not see. What makes that gap safe is that the
  rung is applied again before every start (decision 5).
- `class_apply_error` in the answer (`row`, `engine_admin.go`), present only on a deployment with
  a ladder and only when there is something to report.
- Console: after a refused `PUT …/class`, **re-read the row**. Without that the picker snaps back
  to the old rung although the choice is already stored, so the screen agrees with neither the CP
  nor the provider. Then, only where `class_apply_error` is set, offer "apply it again", which
  **re-sends the rung already selected**.

Pinned by tests:

- Go: `TestPutClassSaysWhyTheApplyFailedAndARetryClearsIt` — a process that has applied nothing
  says nothing; a failure puts the provider's own words in `class_apply_error`; **re-sending the
  same rung** clears it and writes to `UpdateCapacityProvider` a second time. Positive control:
  deleting the one line in `row` fails it (checked).
- dom: the retry appears where the CP reports a failed apply and nowhere else. Positive control:
  removing either the panel block or the `await load()` fails it (both checked).

**Verified by rendering it** (headless Chromium, the real bundle): the red sentence carries the
provider's words, and "apply it again" stands on its own line above the existing "replace it
now" — a different act, so it is not put beside it.

## Follow-up — CloudFormation refuses to switch a provider to Spot (2026-09-11, measured, $0)

Groundwork for moving the `image` role to Spot. The "Follow-up measurements" section measured
the **ECS API** side and showed that `capacityOptionType` survives `UpdateCapacityProvider`. The
**CloudFormation** side — can a stack update change that field at all — was never measured. A
change set that flips `60-engines.yaml`'s `ImageCapacityProvider` to SPOT reads `Modify` /
`Replacement: Conditional` on the live stack too. The CFN documentation says "Some
interruptions" (i.e. no replacement), which contradicts the field's absence from
`UpdateCapacityProvider`'s shape. **Nothing tells you which until you execute it**, so it was
executed somewhere that costs nothing.

Method: one throwaway stack in af-sandbox holding a copy of `ImageCapacityProvider` — the
hardcoded `Name: !Sub "af-${AWS::StackName}-image"` included — plus the three IAM resources it
needs. No service, no `ClusterCapacityProviderAssociations`. Not one instance is launched, so
$0. Ten minutes.

- 🔴 **The in-place edit fails: it goes for a replacement.** The change set is the same shape as
  the live one (`Modify`, `Replacement: Conditional`, `ManagedInstancesProvider` /
  `RequiresRecreation: Conditionally`). Executing it gives `UPDATE_FAILED`:
  `CloudFormation cannot update a stack when a custom-named resource requires replacing. Rename
  af-af-spotprobe-s2hpl5k-image and update the stack again.` → `UPDATE_ROLLBACK_COMPLETE`.
  **Nothing is lost** — the refusal lands BEFORE anything is created, and the provider keeps its
  ARN and its `ON_DEMAND` (compared before and after with `describe-capacity-providers`). On a
  live deployment, though, the whole 60-engines update rolls back, taking anything else in that
  change with it.
- ✅ **Adding one under a different name works** — stage 1 of the safe path. A second resource
  (new logical id, `Name` ending `-image-spot`, SPOT) is `Add`, no replacement,
  `UPDATE_COMPLETE`. It is created even in af-sandbox, whose Spot quota is 0: as the earlier
  follow-up found, the quota bites at launch and not at configuration.
- 🔴 **Creating an MI provider puts it in the cluster's provider list on its own.** The
  throwaway stack has no `Associations` resource anywhere, yet both of its providers appeared in
  `DescribeClusters.capacityProviders` and left when the stack was deleted. "Exactly one stack
  owns the associations" is a rule about who REPLACES the list, not a fence that stops another
  stack adding to it — and the next update of the owning stack silently drops the newcomer.
- Cleanup: after the delete the cluster's list matches the live four exactly, and no IAM role or
  instance profile is left. ECS keeps the providers as `INACTIVE` records, as the earlier
  follow-up already found.

**The order for switching acrt**, written up as a procedure in
`cfn/PARAMETERS-60-engines.md`, "The capacity providers": (1) add the differently-named SPOT
provider and name it in `Associations`; (2) move the image service's `CapacityProviderStrategy`
**and the engine table row's `capacityProvider` in the same change** — the table builds that
string with `!Sub` rather than `!Ref`, so it does not follow the resource, and getting it wrong
fails silently: the Control Plane watches a provider nobody uses, which is where both `draining`
and this ADR's rung application read from; (3) delete the old resource in a later change. **The
`llm` role stays `ON_DEMAND`** — Spot's two-minute notice arrives mid-conversation and a
527-586-second cold start follows it. acrt already holds 64 of `L-3819A6DF`, so no quota case is
needed.

## Follow-up — open question 7 for the image role, points 2 and after (2026-09-11, dev deployment, comfy)

P1's first point came from sd-server (sd.cpp). With the role switched to ComfyUI, SDXL was run on
the l4 rung (g6.xlarge) across resolutions and batch sizes. **Following "do not build a
coefficient from one point", there are now more points — but 🔴 the peak VRAM itself could not be
read. The reason is below.**

### What was measured — all seven points succeeded, no OOM

`sdxl-base-1.0`, 20 steps, one prompt. Seconds are deltas between the nanosecond timestamps of
consecutive generated files, so they include the MCP and HTTP round trips (but not a checkpoint
load: the checkpoint stayed warm).

| size | batch | seconds | produced PNG bytes |
|---|---|---|---|
| 768×768 | 1 | 7.57 | 873,660 |
| 1024×1024 | 1 | ~11.5 | 1,548,285 |
| 1024×1536 | 1 | 15.85 | 2,051,213 |
| 512×512 | 2 | 7.73 | 342,432 / 301,093 |
| 1024×1024 | 2 | 19.07 | 1,707,092 / 1,486,304 |
| 1024×1536 | 2 | 29.32 | 2,165,517 / 2,090,950 |

512×512 batch 1 is left without a time because it could not be separated from the preceding call
in the same turn (it did succeed, at 338,392 bytes). **Nothing OOMs on the l4 rung up to
1024×1536 at batch 2.**

### 🔴 What could not be measured — ComfyUI does not log the VRAM breakdown at INFO

P1's first point was obtainable because sd.cpp writes `total params memory size = 6624.11MB` and
its auto-fit reserve (DiT 2,048 / Conditioner 2,048 / VAE 1,024) to its own stdout. ComfyUI
v0.34.0 does not:

- the measured weights are in one line, `Model loaded: patcher=… model=… ram_mb=… vram_mb=…`,
  emitted through `detail()` in `comfy/internal_logging.py` — whose level is **DETAIL = 15**,
  **below** `logging.INFO` (20);
- `setup_logger` in `app/logger.py` defaults the console to INFO and sends DETAIL only to a file
  inside the container, `comfyui_detail.log`. It never reaches CloudWatch;
- what INFO does carry is `Total VRAM {x} MB, total RAM {y} MB` at startup,
  `Requested to load {ClassName}`, `{n} models unloaded.` and `Prompt executed in N seconds`.

So **a deployment running the image role as comfy cannot answer open question 7 in the same shape
as the first point** (real weights plus the compute reserve). The fix is one thing — adding
`--verbose DETAIL` to the ComfyUI start command in `60-engines.yaml` — but it touches the same
template that is against its size wall (see ADR 0072's follow-up), so it belongs in the pass that
declares the ladder. Recorded here; nothing was changed.

### 🔴 The l4 rung declares 8,000 MiB, which does not describe what actually fits

The same deployment declares `l4` as `{"label":"L4 24GB (g6.xlarge)", "vram_mib": 8000}`. The
label says 24 GB and the card is 24 GB, but **the ladder declares 8,000 MiB**. As a result:

- `vram_fits` is false (`flux1-dev-fp8`'s floor of 16,571 MiB > 8,000);
- and `flux1-dev-fp8` nevertheless generated normally on the l4 rung, twice, at 1024×1024.

Decision 6's gate only runs when a model is ENABLED, so `mode=on` passed straight through and no
harm was done. But a declared value **less than half of the real card** tips the gate the other
way: it says a model does not fit when it does. That is the opposite error from P1's "the floor
explains 58% of real use", and this one is in **the rung's own declaration**. Before a coefficient
for open question 7 is written down, the ladder's `vram_mib` needs a settled meaning — the card's
physical size, or an operational cap — or there is nothing definite to multiply.

## Follow-up — open question 7 for the llm role: the KV cache is **computed**, not measured (2026-09-11, the dev deployment)

Open question 7 said a coefficient could only be written down by measuring. For the llm role
**that was wrong**. The KV cache's size is not a coefficient but a quantity the model's own
header determines, and it agrees with what llama.cpp allocates **down to the last fraction**.
Checked at two points; **both differ by 0.00 MiB (0.00%)**.

```
KV = n_layer × n_head_kv × (key_length + value_length) × ctx × bytes(cache element)
```

which is llama.cpp's own `llama_kv_cache: size = …`.

### The two points

| Model | Family | n_layer | n_head_kv | key/value | ctx | Formula | Measured `KV self size` | Δ |
|---|---|---|---|---|---|---|---|---|
| `qwen2.5-coder-1.5b` | qwen2 | 28 | 2 | 128 / 128 (derived) | 16,384 | 448.00 MiB | **448.00 MiB** | 0.00 |
| `qwen3-coder-30b-a3b` | qwen3moe | 48 | 4 | 128 / 128 (declared) | 32,768 | 3,072.00 MiB | **3,072.00 MiB** | 0.00 |

The log lines, verbatim:

```
llama_kv_cache:      CUDA0 KV buffer size =   448.00 MiB
llama_kv_cache: size =  448.00 MiB ( 16384 cells,  28 layers,  4/1 seqs), K (f16):  224.00 MiB, V (f16):  224.00 MiB
llama_kv_cache: attn_rot_k = 0, n_embd_head_k_all = 128

llama_kv_cache:      CUDA0 KV buffer size =  3072.00 MiB
llama_kv_cache: size = 3072.00 MiB ( 32768 cells,  48 layers,  4/1 seqs), K (f16): 1536.00 MiB, V (f16): 1536.00 MiB
```

`n_slots = 4, kv_unified = 'true'`, but the allocation is **one context's worth** (`16384 cells`
/ `32768 cells`). Multiplying by the slot count would over-estimate it fourfold.

### 🔴 Do not derive `head_dim` from `embedding_length / head_count`

qwen3moe **declares** `attention.key_length` / `value_length` as 128, while
`embedding_length / head_count` is **2048 / 32 = 64**. Preferring the derivation would have put
the 30B's KV cache at **1,536 MiB — half of the real figure**. On a 24 GB card a 1.5 GiB
under-estimate is the difference between "fits" and "does not". **A declared length wins; the
derivation is the fallback for families that declare none** (qwen2 is one). The deployment's own
`print_info: n_embd_head_k = 128` confirms it.

### The header comes from Hugging Face over HTTP Range. No S3 needed

**Hugging Face's API does not carry the formula's inputs** (measured). The `gguf` block holds
`total`, `architecture`, `context_length` and `chat_template` and nothing else, and a GGUF-only
repository's `config` is `{}`. So the file itself has to be read — and since **the CP does not
touch S3** (ADR 0072 review R3: it holds no permission and gains none), what is read is the copy
still at the SOURCE, over the same HTTP the ingest already uses, one Range for the head.

How many bytes that takes was measured: **545 bytes** for the 1.5B and **1,426** for the 30B to
have all four fields. The window is 64 KiB (~45x the worse case), with one retry at 1 MiB and
nothing beyond it — the tokenizer's token array runs to megabytes and is **never** needed.

### The weights side — how much of a floor is decision 6's floor

| Model | File bytes (= the floor) | `CUDA0 model buffer` | `CUDA_Host model buffer` | floor − (CUDA0 + Host) |
|---|---|---|---|---|
| 1.5B | 1,065.56 MiB | 934.70 MiB | 125.19 MiB | **+5.67 MiB** |
| 30B | 17,697.04 MiB | 17,524.43 MiB | 166.92 MiB | **+5.69 MiB** |

So the file's byte count is **very nearly exact as "the weights"** (under 6 MiB out), and the
only thing it got wrong was the split between what lands on the card and what stays on the host
(167 MiB for the 30B, 125 for the 1.5B). This is a different shortfall from P1's image-role
"the floor explains 58% of real use": there the missing part was the compute reserve, here it is
**the KV cache**.

### How much it mattered

Loading the 30B at ctx 32,768 really costs weights 17,524.43 + KV 3,072.00 + compute 116.01 =
**20,712.44 MiB**. The card is 22,563 MiB, so **the real headroom is 1,851 MiB**.

- The old floor-only answer: 17,697 MiB, headroom 4,866. **Under by 3,015 MiB (14.6%).**
- The new weights+KV answer: 20,769 MiB, headroom 1,794. **57 MiB (0.27%) from the truth.**

For the llm role `weights + KV` is an estimate good enough to act on. The compute buffers
(77–116 MiB) are the remaining gap and they are left on the safe side.

### The implementation (`engine_gguf.go`)

Read once at registration and stored as four numbers on the row (`kv_layers`, `kv_heads_kv`,
`kv_key_len`, `kv_value_len`; migration 0062, pg 0047). **Not read per render**, for two
reasons: it would put a network call on the screen that lists every model, and the file is
pinned by sha256, so **its header cannot change under the row**.

The answer's strength gained a rung: `declared` > **`weights_kv`** > `floor` > `unknown`. A
set's verdict is the WEAKEST present — the largest row happening to be measured says nothing
about the row the engine will actually load.

🔴 **The cache's element type is invisible to the CP.** `-ctk` / `-ctv` live in `LlmExtraArgs`,
a CloudFormation parameter that reaches the task definition and never the engine table. f16 is
assumed. A deployment running a quantised KV cache is therefore OVER-estimated — the safe
direction for "does this fit on that card" and the wrong one for "how much is spare". Saying so
beat inventing a field the table has no way to fill.

A row whose header cannot be read (gated with no token, not a GGUF, no upstream ref) and a row
with no declared `context_tokens` stay at the **`floor` they already had**, not at `unknown`.
Hand registration (`POST …/models`) carries no source, so it is out of scope here; closing it
needs a ref field on that route.

### What went wrong while measuring

🔴 **llama.cpp does not print the loader's lines at the default verbosity.** The first start
produced no `llama_kv_cache` and no `load_tensors` at all — 43 lines in total. Raising the child
process's verbosity is what prints `KV self size`, and `LlmExtraArgs` was off limits (it is a
CloudFormation parameter). **The catalogue row's `args` solved it**: `["--verbosity","4"]` on the
row is written into the preset by the fetch sidecar and handed to the child as
`--log-verbosity 4` (confirmed in the log). So an engine's observability can be raised without
touching CloudFormation at all. The price was **one wasted start** — three boxes, two usable.

### The ladder's `vram_mib` is the card's physical size

The question the previous section left open — "there is nothing definite to multiply" — is
settled as **the card's physical quantity**. The `l4` rung is declared from what the hardware
reports, `Total VRAM 22563 MB`: **8000 is simply wrong** and 21000 is only a conservative floor.
Decision 6's gate compares `max(enabled models)` against the rung, so unless the rung is the
card, a judgement like "20,712 against 22,563, 1.8 GB left" cannot be made at all.

## Follow-up — the ladder, declared on the dev deployment (2026-09-11)

The declaration the "not a phase" section asks for, actually put in. **Both roles took it, and
applying a rung was verified to rewrite the capacity provider on hardware.**

### What was declared, and why

```
LlmInstanceClasses=l4|L4 24GB (g6.xlarge)|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91
ImageInstanceClasses=l4|L4 24GB (g6.xlarge)|22000|g6.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB (g6e.xlarge)|44000|g6e.xlarge|4-8|30000-65536|2.91;l40s2x|L40S 48GB 8vCPU (g6e.2xlarge)|44000|g6e.2xlarge|8|30000-65536
```

- **The first rung restates what the stack already buys** — `AllowedInstanceTypes`, `VCpu*` and
  `Mem*` copied across (llm `g6.xlarge,g5.xlarge` / 4-8 / 15000-65536; image `g6.xlarge` / 4-8 /
  15000-65536).
- **The second rung is g6e.xlarge.** At 4 vCPU it fits inside the G-family quota of 8 even with
  the image role holding a box.
- **`vramMiB` is 22000.** The question the previous section left open — the card's physical size
  or an operational cap — is settled as the former. Three numbers are involved: the hardware's
  own `Total VRAM 22563 MB`, **EC2's declared 22,888 MiB**
  (`describe-instance-types`, `GpuInfo.TotalGpuMemoryInMiB`; the same for g6.xlarge and
  g5.xlarge), and the nominal 24 GB. 🔴 **Do not write the nominal figure**: `vramMiB` also
  becomes `AcceleratorTotalMemoryMiB.Min`, so 24576 would match **no instance at all**. 22000
  leaves 888 MiB against EC2's filter and sits just under the real 22,563 as a comparison
  target — the only band that serves both jobs.
- **The image role's l4 rung said 8000.** That was `ImageAcceleratorMemMinMiB` — a *placement
  filter* — copied into a rung, which is exactly what the previous section described as "a
  declared value less than half the real card tips the gate the other way". **Fixing it flipped
  on hardware**: `GET /api/admin/engines` for the image role went `vram_fits: false` →
  **`true`** (against `flux1-dev-fp8`'s 16,571 MiB).

### `usdPerHour` comes from Cost Explorer's actuals

|  | BoxUsage | ECS Managed Instances management | Total | Declared |
|---|---|---|---|---|
| g6.xlarge | $1.1672/h | $0.0910/h | **$1.2582/h** | 1.26 |
| g6e.xlarge | $2.6990/h | $0.2105/h | **$2.9095/h** | 2.91 |

From `ce get-cost-and-usage` over 2026-09-09..11, filtered by `INSTANCE_TYPE` and divided by
`USAGE_TYPE` (4.2181 billed hours of g6.xlarge, 1.4978 of g6e.xlarge — the latter is P1's
90.5 minutes). **g6.xlarge coming out at the 1.26 already declared** is the positive control for
the method. The management fee is **7.80%** of the box on both types, which is the ~8% the
"do not copy a list price" note in `PARAMETERS-60-engines.md` is about. `l40s2x` has no billed
hours, so **its price was left empty** — declaring nothing is the rule.

### 🔴 A ladder in CloudFormation does not reach a RUNNING Control Plane

The declaration went in with one `cloudformation deploy --parameter-overrides`, and SSM's
`/af-ws/engines` carried the new ladder within the same minute (01:14:20Z). **`GET
/api/admin/engines` went on answering with the old one** — l4 still at 8000, the llm role still
with an empty `classes`.

The cause is that `newEngineRegistry` calls `loadEngineTable` exactly once, when the routes are
registered, i.e. at CP start. The table describes the VESSEL, so there is no reload path. A
`update-service --force-new-deployment` of the CP is what published it (01:15:35Z → 01:17:11Z,
**about 100 seconds**, blue/green behind the ALB, so no downtime).

This is an EXCEPTION to what ADRs 0072 and 0074 promise about changing things without a
CloudFormation round trip. A model is the catalogue's and moves without touching the CP; **the
ladder is the vessel's, and it costs one CloudFormation run plus one CP restart**. An operator
who does not know that will hunt for a configuration mistake that is not there.

> ✅ **Fixed (2026-09-11; implemented, not verified on hardware) — the table is re-read.** The
> CP polls the engine table (`AF_ENGINES_SSM_PARAM`) every **10 seconds**
> (`engine_table_reload.go`, on the same constant the pending reader uses). It compares the
> raw TEXT first, so an unchanged parameter costs one `GetParameter` and no parsing — which is
> what makes six calls a minute the right price for something that is almost always unchanged.
>
> **Only the ladder is carried live.** The rest of a row (service, url, health, provider,
> capacity provider, idle, deadline) is baked into objects this process is using right now:
> `engineECS`'s client, the controller's goroutine and its intervals, the capacity client that
> is attached only when a ladder existed at start, the demand window. Swapping them means
> rebuilding the runtime state, which throws away **what only this process knows** — the
> demand counter the controller stops the engine on, the warm model, the rung this process
> last applied — and starts a second controller for the same engine. So a change to any of
> them is **logged as needing a restart**: better than half-applying it silently, because a
> panel whose word disagrees with the deployment's behaviour is exactly this section's defect.
>
> **A row added or removed is logged too, and nothing else.** Unregistering a role that left
> the table would take a GPU away from whoever is using it on the strength of one poll; the
> operator's route out is `mode=off`, which is a decision rather than an inference. 🔴 Going
> from NO ladder to one also needs a restart: the capacity client and the controller's start
> gate are attached at construction only when a ladder exists, so a ladder adopted here would
> be one the panel shows and nothing enforces — decision 4's "the gate is bypassed and it
> lands quietly on the old card".
>
> The swap is behind `classesMu` (an RWMutex) and every reader — controller, gateway, admin —
> goes through `classList()`. A `-race` test swaps the ladder 50 times while it is being read,
> and was checked against the defect: without the lock it really does fail. Three more pin the
> behaviour (a new ladder lands; a broken table keeps the previous one; an unchanged table
> swaps nothing and returns the same slice). **Not verified on hardware**: the next time a
> ladder is changed by CloudFormation, watch `GET /api/admin/engines` catch up within ten
> seconds without the CP being replaced.

### Applying a rung rewrites four fields of the provider

`PUT /api/admin/engines/llm/class {"class":"l4"}`, with `describe-capacity-providers` either
side:

| Field | Before | After |
|---|---|---|
| `allowedInstanceTypes` | `g6.xlarge, g5.xlarge` | `g6.xlarge, g5.xlarge` |
| `acceleratorTotalMemoryMiB.min` | **21000** | **22000** |
| `vCpuCount` | 4-8 | 4-8 |
| `memoryMiB` | 15000-65536 | 15000-65536 |

Only the VRAM floor moved — and that is precisely the value the declaration changed. Decision
1's "four fields are rewritten" holds on the real path. **No box was started on a higher rung**
(GPU cost; P1 already did that).

### The deployment itself

`dev-deploy.sh` re-baked both the CP and the Agent image (`origin/develop` b6feea43,
ImageTag=0.18.1-dev-b6feea43). At its 60-engines step, ADR 0072 phase P6's migration gate —
`update.sh` reading the LIVE stack to translate `<Role>Enabled=true` — **printed nothing at
all**: this deployment already holds `Enabled=true` and no `<Role>ModelS3Key`, so there is
nothing to translate. A silent gate is what a deployment that is already past P6 looks like.

## Follow-up — the ladder reload works (2026-09-11, on hardware, $0)

The gap the previous section (#520) recorded — "a ladder in CloudFormation does not reach a
running Control Plane" — is closed by `engine_table_reload.go`. **Confirmed on hardware.**

A CloudFormation update that changed nothing but the second rung's LABEL in
`LlmInstanceClasses`, watched through `GET /api/admin/engines` **without replacing the CP**:

| Time | Event |
|---|---|
| 02:50:51 | `cloudformation deploy --parameter-overrides LlmInstanceClasses=…` starts |
| 02:51:39 | `Successfully created/updated stack` |
| 02:51:46 | the API's `classes` answers with the new label |

**Seven seconds after the stack finished.** The CP service was still the deployment created at
02:19:57Z, with no `force-new-deployment` in between — the ~100 seconds of blue/green the
previous section needed is gone. The label was put back afterwards, by the same route and again
with no restart.

That only the LADDER is taken live (the ECS client, the controller's goroutine and the demand
window stay put, and a change to any of them is logged as needing a restart) is as implemented;
none of those were touched here.

## Follow-up — the Spot replacement, run on a live deployment (2026-09-11, the dev deployment)

"CloudFormation refuses to switch a provider to Spot" was measured on a **throwaway stack**. Now
that `ImageCapacityOptionType` moves the provider's `Name`, **the same replacement was run once
on the live dev deployment and taken back again** — a rehearsal before acrt.

**The answer splits in two. The replacement machinery worked exactly as designed. And not one
Spot instance was ever bought** — for a reason that is neither of the two failures seen so far,
but a **third error code**.

### The change set — `Replacement: True` as advertised, and the three dependents followed by reference

`ImageCapacityOptionType=SPOT` was the **only** override (`cloudformation deploy` puts every
parameter it is not given at `UsePreviousValue`). Read first with `--no-execute-changeset`, it
held four changes and nothing else:

| Action | Logical id | Type | Replacement | Detail |
|---|---|---|---|---|
| Modify | `ImageCapacityProvider` | `AWS::ECS::CapacityProvider` | **True** | `DirectModification` `Name` **`RequiresRecreation: Always`**; `ManagedInstancesProvider` `Conditionally` |
| Modify | `Associations` | `ClusterCapacityProviderAssociations` | False | `ResourceReference` `CapacityProviders` |
| Modify | `EnginesParam` | `AWS::SSM::Parameter` | False | `ResourceReference` `Value` |
| Modify | `ImageService` | `AWS::ECS::Service` | False | `ResourceReference` `CapacityProviderStrategy` |

**That all three dependents appear as `ResourceReference`** is the evidence that
`PARAMETERS-60-engines.md`'s "keep the table a `!Ref`" is doing its job. As a `!Sub` string the
third row would simply not be in the change set, and the Control Plane would go on watching a
provider nobody uses.

### The events — create the new one, move the dependents, delete the old one in cleanup

The way out took **176 seconds** (05:21:08Z to 05:24:04Z), verbatim:

```
05:21:13 UPDATE_IN_PROGRESS     ImageCapacityProvider  Requested update requires the creation of a new physical resource; hen…
05:21:15 UPDATE_IN_PROGRESS     ImageCapacityProvider  Resource creation Initiated
05:21:25 UPDATE_COMPLETE        ImageCapacityProvider
05:21:26 UPDATE_IN_PROGRESS     Associations
05:21:41 UPDATE_COMPLETE        Associations
05:21:43 UPDATE_IN_PROGRESS     ImageService
05:23:46 UPDATE_COMPLETE        ImageService
05:23:47 UPDATE_IN_PROGRESS     EnginesParam
05:23:49 UPDATE_COMPLETE        EnginesParam
05:23:51 UPDATE_COMPLETE_CLEANUP_IN_PROGRESS
05:23:52 DELETE_IN_PROGRESS     ImageCapacityProvider
05:24:04 DELETE_COMPLETE        ImageCapacityProvider
05:24:04 UPDATE_COMPLETE        af-<engines stack>
```

**Most of it (123 seconds) is the service update**: rewriting `CapacityProviderStrategy` is a new
deployment, and that happens even at desired 0. The old provider is deleted in the **cleanup
phase**, i.e. only after the new one exists and every dependent has moved — not the
"delete then create" the word "replacement" suggests.

**The way back has the same shape and took 147 seconds** (05:42:53Z to 05:45:20Z). 🔴 **The
original name was reused**: ECS keeps the retired provider as an `INACTIVE` record, and that does
not stand in the way of recreating one with the same name. What the throwaway stack saw happens
on a live one too.

### The four things to check

| | before | after (SPOT) | after the way back |
|---|---|---|---|
| provider | `af-…-image` ACTIVE / `ON_DEMAND` | `af-…-image-spot` ACTIVE / **`SPOT`**, the old one **INACTIVE** | `af-…-image` ACTIVE / `ON_DEMAND`, `-spot` INACTIVE |
| the cluster's list | `…-image`, `…-llm` | **`…-image-spot`**, `…-llm` | `…-image`, `…-llm` |
| the service's strategy | `af-…-image` | **`af-…-image-spot`** | `af-…-image` |
| the engine table's `capacityProvider` (SSM) | `af-…-image` | **`af-…-image-spot`** | `af-…-image` |

🔴 **One thing IS lost in a replacement — the rung the Control Plane had applied at runtime.**
The new provider is built **from the template**, so `acceleratorTotalMemoryMiB.min` was born as
`ImageAcceleratorMemMinMiB`'s **8,000**, where the live provider carried the **22,000** the CP
had written for the `l4` rung. Decision 5 re-applies the rung before every start, so it heals —
but **between the stack update and the next start the provider sits below its declared rung**,
and on a deployment with no ladder that difference is permanent.

### 🔴 No instance came, and the code was a third one

`mode=on` went in at 05:24:42Z and came out at 05:41:05Z — **17 minutes**, during which ECS
retried about every five minutes and answered this all four times. **GPU cost $0**: nothing ever
launched.

```
(service af-…-image) was unable to place a task. Reason: ResourceInitializationError:
Unable to launch instance(s) for capacity provider af-…-image-spot.
UnfulfillableCapacity: Unable to fulfill capacity due to your request configuration.
Please adjust your request and try again.
```

**Neither `VcpuLimitExceeded` nor `InsufficientInstanceCapacity`** — not one of the two this ADR
and ADR 0072 have collected, and its wording blames **the request's configuration**, offering
neither another AZ nor a later retry. **What Spot answers when it cannot sell you an instance is
not readable off what on-demand exhaustion looks like.**

Measured and **ruled out**:

- **Not the quota.** `L-3819A6DF` (All G and VT Spot Instance Requests) is **8**, raised from 0,
  and separate from on-demand's `L-DB2E81BA` 8. A g6.xlarge is 4 vCPU, so one fits — and a quota
  refusal comes back under a different code anyway.
- **Not price protection.** An MI provider's `instanceRequirements` carries
  `spotMaxPricePercentageOverLowestPrice`,
  `maxSpotPriceAsPercentageOfOptimalOnDemandPrice` and
  `onDemandMaxPricePercentageOverLowestPrice`, and the template sets none of them. Adding
  `maxSpotPriceAsPercentageOfOptimalOnDemandPrice: 100` (pay up to the on-demand price) through
  Describe → copy → Update changed nothing eight minutes later. It was taken out again, so the
  provider matches the template's declaration.
  ⚠️ A by-product: **`UpdateCapacityProvider`'s own answer does not carry that field back**,
  while the `DescribeCapacityProviders` a moment later does. Reading only the response says it
  did not land.

**Not separated** (both are suspicions, not findings):

- **`AWSServiceRoleForEC2Spot` does not exist in this account** (`iam get-role` answers
  `NoSuchEntity` — what an account that has never launched a Spot instance looks like). MI runs
  under its own infrastructure role, so it is not necessarily needed, and a missing one usually
  comes back as an authorization error.
- **The Spot placement score is low.** g6.xlarge scores **1/10** in both AZs (g5.xlarge also 1,
  g6e.xlarge 3). But **m7i.large scores 1-3 too**, so the whole account reads low and the score
  **cannot separate "this GPU type" from "this account"**.
- 🔴 **A published price is not evidence of stock.** `describe-spot-price-history` kept answering
  $0.577 in ap-northeast-1a and $0.563 in 1c throughout. **The ADR's "30 days at about half the
  list price" never meant the instances could be bought.**

### What the rehearsal says about acrt

- **The machinery works.** The replacement, all four dependents following, the round trip and the
  name reuse were confirmed on a live deployment. There is nothing new to learn by running it on
  acrt.
- **Whether an instance can be bought is a different question, and a rehearsal cannot answer it.**
  Spot stock is a function of account, region and clock; seventeen minutes on af-sandbox says
  nothing about acrt (whose quota is 64).
- 🔴 **So "switch to Spot" is not finished until an instance has been bought after the switch.**
  The dev deployment was put back to `ON_DEMAND`: leaving behind a deployment whose image role
  will not start makes the next session chase a broken engine. That the round trip costs 147
  seconds is now known from hardware.

> ✅ **The next section did buy one, the same day at 06:01Z.** Two things changed — the Spot
> service-linked role was created, and the allowed types went from one to three. **The
> circumstantial evidence points at the type width** (placement score 1 for one type, 9 for
> three). Both of this section's suspicions therefore remain **untested in isolation**.

## Follow-up — Spot did sell us one, but two things changed (2026-09-11, the dev deployment)

After the previous section ended in `UnfulfillableCapacity`, both suspicions were cleared and the
role was woken again. **This time an instance came** — `g6e.xlarge`, ap-northeast-1a,
**`InstanceLifecycle: spot`** — and it generated an image.

🔴 **But two things differ from the previous section, and which one did it was not separated.**

1. **The Spot service-linked role was created.** `AWSServiceRoleForEC2Spot` did not exist in this
   account (the previous section's `NoSuchEntity`). Created at 05:54:34Z with
   `iam create-service-linked-role --aws-service-name spot.amazonaws.com`.
2. **The allowed types went from one to three.** `g6.xlarge` →
   `g6.xlarge,g5.xlarge,g6e.xlarge`.

**The circumstantial evidence points at the second.** `get-spot-placement-scores` moves on the
type set alone:

| asked as | score |
|---|---|
| `g6.xlarge` alone, single AZ | **1 / 10** (both 1a and 1c) |
| `g5.xlarge` alone | 1 |
| `g6e.xlarge` alone | 3 |
| **all three, single AZ** | **9 / 10** (both AZs) |
| all three, region | **9 / 10** |

It was taken once more immediately before `mode: on` (06:01:04Z), recorded as 9, and **an
instance arrived 42 seconds later** — prediction and outcome side by side. **So
`get-spot-placement-scores` is worth asking before switching to Spot** — but **ask it with the
provider's own type set**. The 1 that came back for one type said nothing about a provider that
buys from three.

⚠️ **The score reads low for the whole account too**, so read the *difference* a type set makes
rather than the absolute value (the previous section's `m7i.large` at 1-3 is the example).

### Why three types is safe — the cheapest member is still the intended one

Measured with `describe-instance-types`: all three are **4 vCPU with an instance store**, and
their GPU memory is g6.xlarge **22,888**, g5.xlarge (A10G) **22,888**, g6e.xlarge (L40S)
**45,776** MiB. `AcceleratorMemMinMiB`'s 22000 is met by all three, so **the floor was not
touched**. The Spot prices:

| type | Spot (1a / 1c) |
|---|---|
| g6.xlarge | $0.577 / $0.563 |
| g5.xlarge | $0.7436 / $0.7854 |
| g6e.xlarge | $1.3552 / $1.3507 |

— **the intended g6.xlarge is still the cheapest**, which is what `LlmAllowedInstanceTypes`'
"a fallback belongs above the intended instance, never below it" asks for. ⚠️ Note though that
**g6e's Spot at $1.35 is above g6's on-demand $1.26**. Spot is cheap for the type you got, not
for everything you widened to.

🔴 **The ladder's first rung has to carry the same three types, or widening buys nothing.** Per
decision 5 the Control Plane overwrites the provider's four fields from the rung before every
start, so widening `ImageAllowedInstanceTypes` alone snaps back to one type at start. Both went
in on one parameter update:

```
ImageAllowedInstanceTypes=g6.xlarge,g5.xlarge,g6e.xlarge
ImageInstanceClasses=l4|24GB+ (g6/g5/g6e)|22000|g6.xlarge,g5.xlarge,g6e.xlarge|4-8|15000-65536;l40s|…
```

The label was made honest and **the price dropped** — three mixed types with no billing record of
their own, and "declare what was billed, or nothing" is the rule.

### Measured (all from `mode: on` at 06:01:16Z)

| elapsed | event |
|---|---|
| <= 42 s | the container instance is ACTIVE (`i-0397164cafff94cef`, **g6e.xlarge**, ap-northeast-1a) |
| 281 s | `state: running` |
| 307 s | `warm: true` |
| 403 s | `generate_image` returned one picture (`provider: comfy`, `warnings` empty) |

The replacement's change set and events had the previous section's shape exactly
(`Replacement: True`, the three dependents by `ResourceReference`, create → move → delete in
cleanup, **148 seconds**).

🔴 **Proving it is Spot needs `describe-instances` BY ID.** Until now this ADR and
`PARAMETERS-60-engines.md` have said MI instances run in an AWS-managed account and therefore do
not appear in `describe-instances`. **That is half right** — measured at the same moment:

- `describe-instances --filters Name=instance-type,Values=g6.xlarge,g5.xlarge,g6e.xlarge` → **`[]`**
- `describe-instances --instance-ids i-0397164cafff94cef` → **everything**:
  `InstanceLifecycle: spot`, `SpotInstanceRequestId: sir-…`, the type, the AZ.

**They cannot be enumerated, but they can be described if you know the id** — and the id is the
`ec2InstanceId` on `ecs describe-container-instances`. That is **the only route from the calling
account to a proof that Spot sold you the instance**.

### 🔴 Rename the provider and a running Control Plane loses the box

This is the heaviest finding of the run, and **switching to Spot is precisely its trigger.** The
replacement renames the provider from `af-…-image` to `af-…-image-spot`, and **the only part of
the engine table that can be taken live is the ladder** ("the ladder reload works"); the capacity
provider's name is baked in at start. What actually happened when it was woken without replacing
the CP, straight from the CP's log:

```
06:00:32 engines: image changed in the table in a way this process cannot take live
                  (capacity provider) - restart the Control Plane
06:00:32 engines: image instance classes re-read from /af-ws/engines: l4, l40s, l40s2x
06:01:17 engines: image: re-applying the instance class l4 failed (already applied by this
                  process): updating the capacity provider af-af-ecs-engines-image:
                  … ClientException: The capacity provider could not be updated because it
                  has been deleted.
06:01:17 engines: image: starting on l4 (22000 MiB VRAM declared); largest model
                  flux1-dev-fp8 wants 16571 MiB (floor)
```

**The designed warning fired** (line 1 — the first time that line has been seen on hardware), and
the ladder alone did swap live (line 2). `class_apply_error` reached the panel too: it was on
`GET /api/admin/engines`, carrying ECS's own words. **All of that is as designed.**

🔴 **The problem is that none of it stops anything.** Three disagreements, all observable at the
same moment:

- **The rung never reached hardware.** The apply went to the **deleted old provider** and 400'd,
  so the new one stayed at the template's `ImageAcceleratorMemMinMiB` of **8,000**. The instance
  was bought against the template's floor, not the declared rung's.
- **The box is invisible.** The `box` lookup matches `ci.CapacityProviderName` against the
  **baked-in old name**, so `GET /api/admin/engines` answered `box: null` while a g6e.xlarge was
  running at $1.35/h.
- **And the panel still says "running on l4"** (line 4). Decision 4's "you think you raised the
  rung and keep running on the old card" reappears here **without anyone changing a rung**.

**Replacing the CP fixed all of it.** `update-service --force-new-deployment` (06:17:28Z to
06:21:05Z, **about 217 seconds**, blue/green behind the ALB so no outage). As a positive control,
the rung application alone was driven **without buying a GPU** (`PUT …/image/class
{"class":"l4"}` with the engine at `mode: off` — **$0**):

| | before the restart | after |
|---|---|---|
| `PUT …/class` | 400 `… has been deleted.` | **succeeds** (`class_apply_error: null`) |
| the new provider's `acceleratorTotalMemoryMiB.min` | **8,000** (the template's) | **22,000** (the rung's) |

**The same request went through once a CP restart was put in front of it.** Both the defect and
its repair are shown on hardware.

> 🔴 **So an update that changes `<Role>CapacityOptionType` comes as a PAIR with a CP restart.**
> Same shape as "the ladder goes into CloudFormation and never reaches the running CP", which
> `engine_table_reload.go` closed — but this one **stays open by decision** (swapping the row's
> other fields live would throw away state only this process holds), so **it can only be written
> down as an operating step.**

### Putting it back, and what was left

The image role went back to `mode: off` (its original value) and **the provider was left on
`SPOT`** — this run showed that Spot stock is there once the type list is wide, so it is adopted
as the dev deployment's default (the user's call). The rung was applied to the new provider by
the positive control above, and `class_apply_error` is clear.

⚠️ **The deployment's capture (`params/60-engines`) was NOT updated.** Both parameters went into
the live stack through `cloudformation deploy --parameter-overrides`; `update.sh` and
`dev-deploy.sh` read the live stack and keep them, but **rebuilding from the capture with
`standup.sh` returns to `g6.xlarge` / `ON_DEMAND`.**

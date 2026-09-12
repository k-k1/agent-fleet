# `60-engines.yaml` — parameter reference

The long-form reasoning for every parameter of the self-hosted inference stack (ADR 0071).
The template keeps a one- or two-line `Description:` per parameter — enough to know what to
pass — and this file holds the rest: the measurements, the trade-offs and the traps.

**Why the split.** The same 51,200-byte wall that split
[`PARAMETERS.md`](PARAMETERS.md) off `30-ingress.yaml`. Adding the `image` role (ADR 0071 P1)
took this template past it, and prose is the one part that does not have to travel to the
CloudFormation API to do its job. A YAML comment counts toward the limit exactly like a
`Description:`, so moving a paragraph into a `#` would have saved nothing. Nothing was
shortened in the move.

Order matches the template.

- [What this template learned the hard way](#what-this-template-learned-the-hard-way)
- [Shared](#shared)
- [Upgrading: the model parameters are gone](#upgrading-the-model-parameters-are-gone)
- [The `llm` role (llama.cpp)](#the-llm-role-llamacpp)
- [The `image` role (stable-diffusion.cpp)](#the-image-role-stable-diffusioncpp)
- [The instance classes](#the-instance-classes)
- [The offers](#the-offers)
- [The fetch sidecar](#the-fetch-sidecar)
- [The idle wrapper](#the-idle-wrapper)
- [Editing this template](#editing-this-template)
- [The task roles](#the-task-roles)
- [The models bucket](#the-models-bucket)
- [The engine boxes](#the-engine-boxes)
- [The engine services](#the-engine-services)
- [The G-family quota](#the-g-family-quota)
- [Ingest](#ingest)
- [The model volume](#the-model-volume)
- [The engine table](#the-engine-table)
- [Cost allocation](#cost-allocation--every-billed-resource-in-this-stack-carries-af-role)

## What this stack is

The fleet's own inference engines on GPU, normally scaled to zero (ADR 0071 P0: the `llm`
role, llama.cpp; P1: the `image` role, stable-diffusion.cpp's sd-server). Optional, and each
role is optional on its own: a deployment that does not want self-hosted inference deploys
nothing and no engine model appears in any launch menu.

It imports the VPC, private subnets and the CP security group from 00-network, and the
cluster, execution role, Cloud Map namespace and the af-llamacpp / af-sdcpp ECR repositories
from 20-platform; it does **not** depend on 30-ingress. Deploy it BEFORE 30-ingress and hand
30-ingress this stack's `EnginesSsmParam` output.

## What this template learned the hard way

**Templates are ASCII-only, and CloudFormation is why.** A template body does not survive
non-ASCII: CloudFormation replaces **every non-ASCII codepoint with `?`**, server-side. The
file on disk is fine, the AWS CLI sends correct UTF-8 declaring `charset=utf-8`, and
`get-template` hands back `?`. Measured 2026-09-10 on two live deployments -- both held zero
non-ASCII where the source had 69 -- with the positive control that pins it on the service:
the same CLI, account and machine round-trip the identical string through SSM untouched.

It is not only cosmetic. The `?` lands in parameter `Description` values, which operators read
in the CloudFormation console, and inside the shell embedded in `Mappings` -- this template
shipped four `echo` lines containing a literal `?` to the engine log before anyone noticed. A
non-ASCII character in a path, a pattern or a comparison would be a defect, not a blemish.

Write `-`, `!!`, `->`, `...`, `sec.` instead; comments are English anyway (`AGENTS.md`), so
Japanese belongs in `docs/`. `deploy/local/cfn-ascii-test.sh` enforces this per PR. Dropping
the 281 characters it was written for also freed 454 bytes, which matters here: this template
is the one that lives closest to the 51,200-byte inline limit `af_cfn_deploy` measures.

**Why not Fargate.** Fargate has no GPU (AWS Fargate FAQ; containers-roadmap #88, open since
2019), so ADR 0070's shape — "an ECS service whose desired count is 0 while nobody wants it" —
has to be bought on an instance. It was bought from **ECS Managed Instances** until ADR 0077 and
is bought **by the Control Plane, with EC2 Fleet** since: AWS owning the AMI was the reason, and
the AWS-maintained ECS-optimized GPU AMI gives that without delegating the purchase
([the engine boxes](#the-engine-boxes)).

What the Managed Instances era measured on a real cluster on 2026-09-07 through
`deploy/aws/ecs/harness/engprobe.yaml`, kept because two of the four are about this template
rather than about who buys:

- `ClusterCapacityProviderAssociations` REPLACES the cluster's provider list and requires
  `DefaultCapacityProviderStrategy` — so the full list is named in the template and the default
  strategy is deliberately EMPTY. Put anything in it and a service that forgot its `LaunchType`
  lands on the GPU instance (ADR 0070 decision 1, ADR 0071 decision 1);
- `DesiredCount` on an `ECS::Service` returns to its declared value on EVERY service update —
  see [The engine services](#the-engine-services);
- (historic) a Managed Instances capacity provider was CLUSTER-SCOPED, and
  `AmazonECSInfrastructureRolePolicyForManagedInstances` passed only roles named
  `ecsInstanceRole*`, so the fleet-named instance role needed an explicit `iam:PassRole`
  (`UnauthorizedOperation` otherwise, visible only in CloudTrail);
- (historic) `InstanceRequirements` refused `InstanceGenerations` together with generation-bearing
  type names, and `AcceleratorCount` needed `AcceleratorTypes` alongside it. The equivalent
  constraints on `CreateFleet`'s overrides are ADR 0077 open question 3.

⚠️ **Exactly one stack in a deployment may own the cluster's capacity-provider associations**,
because the API replaces the list rather than adding to it. That stack is this one, even now that
the list is `[FARGATE, FARGATE_SPOT]`. The measurement harness (`harness/engprobe.yaml`) owns the
same list, so take it down first:
`deploy/aws/ecs/harness/probe-managed-instances.sh down`.

## Shared

### `ServiceConnectNamespace`

The private DNS namespace's NAME — it has to be the same value 20-platform was given, because
the namespace is imported by id (which carries no name) while the URL written into the SSM
parameter is built from the name. Wrong here and everything deploys cleanly while the CP's
gateway points at a name that does not resolve.

### `EnginesSsmParamName`

SSM parameter this stack writes the engine table to, and the single value 30-ingress is handed
(ADR 0071 decision 8). It MUST live under `/af-ws/` — the CP task role's SSM read is scoped to
`parameter/af-ws/*` (20-platform, Sid `SsmWorkspaceParams`), and a name outside it deploys
cleanly and then reads AccessDenied at CP start.

## Upgrading: the model parameters are gone

**Nothing in this template says what an engine loads any more** (ADR 0072 decision 1, completed
in phase P6). The catalogue in the Control Plane's database says it, and an administrator edits
it from the admin panel while the GPU is asleep — the act these parameters made into a
CloudFormation run. Eight parameters were retired, in two steps:

| Retired | In | What replaces it |
| --- | --- | --- |
| `LlmModelFile` / `ImageModelFile` | 0.18.0 | nothing — the instance mirrors the bucket's layout |
| `LlmModelS3Key` / `ImageModelS3Key` | P6 | a model's files, in the catalogue row |
| `LlmModelIds` / `ImageModelIds` | P6 | the catalogue row's id |
| `LlmContextTokens` / `LlmMaxOutputTokens` | P6 | the row's own window, per MODEL |

`LlmExtraArgs` / `ImageExtraArgs` **stay**: those are the INSTANCE's flags (`-ngl 99`,
`--diffusion-fa`), not a model's.

🔴 **Before updating a deployment that named a model key, add `<role>Enabled=true`.** Until P6
`<role>ModelS3Key` decided two things — which model was seeded, and **whether the role's service
existed at all** (`HasLlmModel` read the key when `LlmEnabled` was unset). `LlmEnabled` /
`ImageEnabled` is the whole answer now, so a `params/60-engines` that switched a role on by
naming a key and left `Enabled` empty **loses the role**: the service, its Cloud Map name and its
row in the engine table are deleted by an ordinary stack update, silently, taking a running GPU
engine with them. Add the line first:

```
LlmEnabled=true          # and/or ImageEnabled=true
```

**`standup.sh` and `update.sh` both do this translation for you**, from the two places each of
them can see: standup from the capture (`params/60-engines`), and update from the LIVE stack's
own parameters, because it deliberately runs without a capture. Both say on stdout which role
they carried over. standup additionally drops the retired keys (`af_param_drop`, because
`cloudformation deploy` refuses a `--parameter-overrides` key the template does not declare);
update never passes them in the first place.

A deployment where `<role>ModelS3Key` is empty too is not translated, and that is the right
answer rather than a gap: with no key and no `Enabled`, **that role never existed** — there is
no service to lose. A role that has already been through P6 has no `<role>ModelS3Key` parameter
left to read, so nothing is passed and the deploy is what it always was.

⚠️ **A hand-run `cloudformation deploy` has neither.** It passes no parameters, so the stack
falls straight to the new default and the role goes. Edit `params/60-engines` — or add
`--parameter-overrides <role>Enabled=true` — before running one.

🔴 **On the `update.sh` route, set the switch in the SAME update that carries the new template.**
Measured on the dev deployment 2026-09-10 (ADR 0072, "P6 on hardware"), where both `<Role>Enabled`
were empty and both roles stood on their model key alone. Two things came out of it:

- **A separate "harmless" pre-update is refused.** `update-stack --use-previous-template` with
  everything at `UsePreviousValue` and just the two switches set to `true` answers
  `ValidationError … No updates are to be performed`, because on the OLD template `<Role>Enabled`
  is referenced only from `Conditions`: setting it satisfies the first branch of an `!Or` that was
  already true, no resource changes, and CloudFormation will not execute an empty change set. It
  is refused for being too harmless. So:

  ```
  aws cloudformation deploy --stack-name <engines> --template-file cfn/60-engines.yaml \
    --capabilities CAPABILITY_NAMED_IAM --parameter-overrides LlmEnabled=true ImageEnabled=true
  ```

  after which `update.sh` keeps `true` at `UsePreviousValue` like any other parameter.
- **`af_param_drop` is not needed here.** `deploy` only refuses an undeclared key it is *given*,
  and this route gives none — the retired six just disappear. The drop is `standup.sh`'s, which
  passes a captured file on.

What the accident looks like if you skip it: `update.sh` reports an ordinary successful deploy of
60-engines and the two services are gone. Nothing is an error anywhere. (On the run that was
measured the switches had been set first, so the same step printed
`No changes to deploy. Stack … is up to date` — that line is what a deployment which was already
paid for looks like, not a check that anything was.)

**The seed is gone with them.** A Control Plane whose catalogue was empty used to read those
parameters once and create the row the deployment was already serving. Anything that has run a
release between ADR 0071 and P6 therefore has its rows already and notices nothing. A deployment
that jumps from a pre-catalogue Control Plane straight to this one comes up with an EMPTY
catalogue: the role exists, the engine idles, and the panel is where the first model is
registered. That is a supported state, not a broken one.

### 0.20.0: the Control Plane buys the box, and the capacity providers are gone

ADR 0077. The offers list, the VRAM filter, pin/automatic and the panel are unchanged — what
changes is who buys: a `CreateFleet(type=instant)` per offer against a launch template, instead
of a desired count that sent ECS out shopping. For an operator it is **eight retired parameters,
one re-meant parameter and a migration window**:

| | |
| --- | --- |
| Retired | `<Role>AllowedInstanceTypes` / `AcceleratorMemMinMiB` / `VCpuMin` / `VCpuMax` / `MemMinMiB` / `MemMaxMiB` / `UseLocalStorage` / `ScaleInAfter` — the offer carries all of it ([the llm role](#llmacceleratormemminmib-llmallowedinstancetypes-and-the-instance-requirement-pairs)) |
| Re-meant | `<Role>StorageGiB` — the box's ROOT gp3 volume now (and the models land on the instance store when the type has one) |
| Re-meant | `<Role>OfferBudgetSec` — **the ECS registration ceiling** for the box that was bought, not a per-offer purchase clock. Default 180 -> **300**; a captured 180 is dropped by `standup.sh` ([`<Role>OfferBudgetSec`](#roleofferbudgetsec)) |
| New prerequisite | `AWSServiceRoleForEC2Fleet` (`standup.sh` creates it — cheap insurance rather than a hard gate: [the Spot checks](#before-declaring-a-spot-offer-check-these-three)) |
| Migration | with both roles at `mode: off` and no engine box standing — [the procedure](#migrating-a-deployment-that-is-on-managed-instances) |

🔴 **Put this release's Control Plane image on before the stack**, as with the 0.19.0 rename: a
0.19.0 CP reads `capacityProvider` out of the engine table and finds a field that is no longer
written, so it has nothing to buy through while the stack has already dropped the providers.

⚠️ **The instance store moved from a parameter to the launch template.** `<Role>UseLocalStorage`
is gone; the user data mounts the NVMe and puts Docker's data-root on it, which is where the
models end up ([the model volume](#the-model-volume)). Same measured benefit, no knob — and
**unverified on hardware**: the first P1 run has to look at `df /var/lib/docker` on the box.

### 0.19.0: the image role's box comes from an offers list

Three new parameters — `ImageOffers` / `LlmOffers` and `ImageOfferBudgetSec` /
`LlmOfferBudgetSec` — and **`ImageCapacityOptionType` is gone** (ADR 0075). A deployment that
leaves all of them alone is unchanged in behaviour: with no offers declared the Control Plane
reads `<Role>InstanceClasses` as a list of on-demand offers, and with neither declared it does
not ask ECS about capacity at all.

What the list buys is the failure this replaces: a role whose Spot request finds no stock used to
be a role that did not start, with `UnfulfillableCapacity` in the service events and a human in
the loop. Now the Control Plane tries the offers in the order written, gives each one
`<Role>OfferBudgetSec` (180 s by default at that release — ADR 0077 re-meant and re-defaulted
it), and lands on the on-demand one when the Spot one cannot be filled. The saving is secondary and small — in ap-northeast-1 Spot ran 30 days without
one on-demand hour, `g6.xlarge` at $0.45-0.58 (ADR 0074), against a total GPU spend of $0.58 for
those 30 days on acrt. **The point is that the engine starts.**

Three things to do, in this order:

1. 🔴 **If the stack is running `ImageCapacityOptionType=SPOT`, deploy `ON_DEMAND` FIRST**, on
   the template it is still running, and only then this one. The Spot provider is a new resource
   asking for the name the old one currently holds, and CloudFormation creates before it deletes
   — skip the step and the update fails on the name collision and rolls back. The round trip was
   measured at 147 seconds. A deployment on the default (`ON_DEMAND`) needs none of this.
   ⚠️ **This step is for an upgrade that lands on 0.19.0.** Going straight to 0.20.0 there is no
   collision to avoid: every provider is deleted, so no name is asked for twice (ADR 0077
   decision 11). The capture still has to lose the parameter, which `standup.sh` does for you.
2. **Put this release's Control Plane image on before the stack**, as with any provider rename:
   a 0.18.0 CP addresses the deleted name, so the rung never reaches the instance and the panel
   shows no box while one is billing (measured; `force-new-deployment` clears it in 217 s).
   The same applies the first time a deployment with **no** ladder declares offers — that one
   needs a `force-new-deployment` regardless of version, because the rung gate is built at
   construction.
3. **Before writing a `spot` row, check `L-3819A6DF`** (default **0** — a provider is created
   happily without the quota and then never buys anything) and create
   `AWSServiceRoleForEC2Spot` if the account has never launched a Spot instance. Both, and the
   type-set rule that moved a placement score from 1/10 to 9/10, are under
   [before declaring a `spot` offer](#before-declaring-a-spot-offer-check-these-three).

🔴 **The quota being right is not enough, and neither is the price.** Rehearsed on the dev
deployment 2026-09-11 with the quota at 8: the first attempt bought **nothing** for seventeen
minutes, answering `UnfulfillableCapacity` — a third error code, neither `VcpuLimitExceeded` nor
`InsufficientInstanceCapacity` — while `describe-spot-price-history` went on quoting
$0.563-0.577. **A published price is not evidence of stock.** The second attempt, with the
service-linked role created and the type list widened to three, got one in **42 seconds**. That
seventeen-minute wait is exactly what an offer's budget now bounds.

The `llm` role takes the parameters (one format for both roles) but **gets no Spot provider and
should be given no `spot` row**: a two-minute termination notice mid-conversation costs a
527-586-second cold start to recover from.

### 0.19.0: the first update also updates 20-platform

The release that moves the fetch and ingest steps into an image needs a new ECR repository
(`af-engine-tools`), and that repository is 20-platform's. So the first `update.sh` run on this
release deploys 20-platform as well — as a change set it prints before executing, and only when
it replaces nothing (`Add EcrEngineTools` and `Modify CpTaskRole`, no replacement, is what it
looked like on a real deployment). Nothing to do, other than reading the two lines it prints;
later releases skip the step entirely, because an empty change set is exactly the fact that
nothing moved. A change set that WOULD replace something is handed back instead of executed —
replacing an ECR repository throws its images away.

The image itself is carried over in the same run: [The engine tools image](#the-engine-tools-image).

### `LlmEnabled` / `ImageEnabled`

Whether the ROLE exists at all: its service, its Cloud Map name, its row in the engine table.
Only `"true"` creates it; `""` (the default) and `"false"` are the same "no". `""` survives as a
value because a capture taken from a stack that never set it carries the empty line.

Under ADR 0071 an empty model key meant NO SERVICE, because a service whose fetch container
could not find a file never stabilised and CloudFormation blocked on it — the two-pass stand-up.
That is gone: the sidecar treats "nothing staged" as success and the engine idles
([the idle wrapper](#the-idle-wrapper)), so a role can be created in one pass with an empty
catalogue. What stops it costing money is the controller, not the template.

## The `llm` role (llama.cpp)

### `LlmImageTag`

Tag inside the `af-llamacpp` ECR repository. The upstream image
(`ghcr.io/ggml-org/llama.cpp:server-cuda`, 2.47 GB) is copied in with `crane` by `standup.sh`
rather than pulled at task start: measured, GHCR through this NAT runs at 12-14 MB/s, which put
178 of a 527-second cold start into the pull alone.

### Where the model and its window come from (not from here)

A model reaches the bucket through the ingest task — the engine never talks to Hugging Face
(ADR 0071 decision 3: measured at 4-236 MB/s with no way to tell which until you pull, against
S3's steady 104-147 MB/s) — and its S3 keys, its id and its window are the catalogue row's. The
llm role runs llama-server in router mode, where each model carries its own `c` into its preset
section, so a window is per MODEL: ADR 0071's rule that two windows meant two engines and two
provider ids is gone with the parameters that created it.

Both halves of the window travel, and the second is not decoration. Measured against opencode
1.18.29, which is what a workspace drives this engine with:

- a model the client has never heard of and that declares no `limit` is read as **context 0**, and
  auto-compaction is **switched off** at 0. The session then grows until `llama-server` rejects the
  request, with nothing in the UI to explain it;
- the usable window is `context − output cap`, and an output cap of **0 is not "unset"** there — it
  substitutes 32,000. Declaring a 32,768-token context without the output half would leave **768**
  usable tokens, i.e. compaction thrashing from the first turn. That is why the Agent writes both
  or neither, and why the panel's registration form refuses one without the other.

32,768 is what was measured on an L4 (opencode's request is 18.7k tokens with the fleet's
`AGENTS.md` included), and it is what a row registered by hand is worth starting from.

### `LlmExtraArgs`

Extra `llama-server` flags. The defaults are the ones measured on an L4: all layers on the GPU and
the chat template applied, which is what makes tool calls work. The router passes its own command
line down to every model instance it spawns, so these reach each model (measured: `--jinja` and
`--n-gpu-layers 99` appear in `/models`'s `status.args`).

🔴 **Never put `-c` here.** A command-line argument WINS over the preset — that is the router's
documented precedence and it was measured: with `-c 32768` on the router, two models whose preset
sections said `c = 4096` and `c = 384` both came up `--ctx-size 32768`. One flag, and every
model's declared window is silently gone. Windows belong to the catalogue.

### `--no-mmap` is in the default `LlmExtraArgs`, and it is the second-biggest win in this stack

llama.cpp memory-maps the GGUF by default and lets page faults pull it in. On this instance that
reaches only about half of what the disk can do, and `--no-mmap` — a plain sequential read —
gets the rest. Measured cold both ways (two copies of the same 18.5 GB object, each load
preceded by 18.5 GB of other I/O so a 14 GB cgroup cache is cycled and neither is warm):

| | load | effective |
|---|---|---|
| mmap (llama.cpp's default) | 92 s | 201 MB/s |
| **`--no-mmap`** | **50 s** | **371 MB/s** |
| the instance store itself, `dd iflag=direct` | 47 s | 391 MB/s |

**`--no-mmap` essentially saturates the disk; mmap leaves half of it on the table.** Then on the
real deployment, end to end:

| | before | with `--no-mmap` |
|---|---|---|
| cold start (RunTask → model loaded) | 267 s | **209 s** |
| **swap back to the 18.5 GB model** | 98.5 s | **46.9 s** |
| swap to the 1.1 GB model | 3.3 s | 1.6 s |

The swap is the number ADR 0072 decision 3 put a price on, and it has now gone
**276-282 s → 98.5 s → 46.9 s** across the two changes — six times faster than the shape this
stack shipped with, for one parameter default and one capacity-provider flag.

🔴 **The obvious worry does not bite**: without mmap, llama.cpp is not mapping a file it can drop
pages from, so an 18.5 GB model on a task limited to 14,336 MiB looks like it should fail. It
does not — with `-ngl 99` the weights stream tensor-by-tensor into VRAM and the host buffer is
transient. Verified on the deployment's own task definition at its own memory limit, which is
the only place that question could be answered honestly. **A role that did NOT offload every
layer would be a different question**, so anything running without `-ngl 99` should re-measure
before inheriting this default.

### `LlmModelsMax`

How many models the router may hold at once (`--models-max`; upstream's default is 4, `0` =
unlimited). **1 here, deliberately.** The router knows nothing about VRAM: a second 30B Q4 on an
L4 does not make the instance slow, it makes CUDA crash (ADR 0071 decision 2). At 1 the second model's
first request costs an unload plus a load — 267 s of weights into VRAM, measured — which is the
price of this design and is shown in the panel rather than hidden.

Measured about that swap, on llama.cpp b10853 (CPU, two models, `--models-max 1`):

- the eviction **waits for the model to be idle**: a request for the other model was queued for
  48.6 s while an 854-chunk stream finished, and the stream was delivered whole. An in-flight
  generation is not interrupted (ADR 0072 open question 1(c));
- so the waiting request pays "rest of the current answer + load". A STREAMING request is held by
  the gateway's SSE heartbeat and rides it out; a non-streaming one does not — it has no bytes to
  show, so the gateway's own 45-second hold (`AF_ENGINE_PLAIN_HOLD`, deliberately under the ALB's
  60) ends it with a retryable `503 engine_waking`. Raise `LlmModelsMax` only when the models
  genuinely fit together.

### `LlmApiKeySsmParam`

SSM SecureString holding the key llama-server is started with (`--api-key`) and the CP presents
upstream. Reachability is already the whole of the engine's access control (the SG), so this is
the second lock on an API that is unauthenticated by default and has mutating endpoints (ADR
0071 decision 4d). `standup.sh` generates it if it is missing. Empty = no `--api-key`.

### `LlmTaskCpu` / `LlmTaskMemory`

Task vCPU units and memory (MiB). 4096 fills a g6.xlarge; keep it under the instance. The memory
is below the instance's 16 GiB by enough for the ECS agent — a task that asks for all of it never
places, which reads as a capacity problem.

### `LlmGpuCount`

GPUs per instance, and the task's GPU resource requirement (`ResourceRequirements`). `0` is a
CPU instance, for tests only. How many cards the BOX has is the offer's business now
([the offers](#the-offers)); this is what the task asks the agent for once it is on the box.

### `LlmStorageGiB`

**The root gp3 volume of the box the Control Plane buys** (ADR 0077 decision 11). It was the
Managed Instances data volume until then, and the number is deliberately unchanged so a
deployment capture carries over — but what it sizes moved, and the reason is the model
directory: the task's anonymous host volume lands on the root filesystem, where the ECS-optimized
AMI's own default is 30 GiB. A comfy deployment holding 30 GB of checkpoints fills that and the
fetch dies with `No space left on device`, which is what the Managed Instances era measured with
a named `SourcePath` ([the model volume](#the-model-volume)).

⚠️ **On a type that HAS an instance store, the models do not land here at all** — the launch
template's user data puts Docker's data-root on the NVMe, and an anonymous `host` volume is a
Docker volume, so it follows (see [the model volume](#the-model-volume)). This size then covers
the root filesystem and the image layers Docker wrote before the move. On an EBS-only type
(g6e and friends) nothing is mounted and everything is here, which is the case to size for.

🔴 **The instance store is what the retired `<Role>UseLocalStorage` used to buy, and it is worth
roughly half a cold start**: S3 -> disk 1.4x, disk -> VRAM 2.9x, RunTask -> model loaded
527-586 s -> **275 s**, a model swap 276-282 s -> **98.5 s** (measured 2026-09-09 on a g6.xlarge
under Managed Instances). gp3's baseline is 125 MB/s and a bigger volume does not make it faster,
which is the whole of why the NVMe is mounted at all.

### `LlmAcceleratorMemMinMiB`, `LlmAllowedInstanceTypes` and the instance requirement pairs

**Gone in ADR 0077** (0.20.0), with `<Role>UseLocalStorage` and `<Role>ScaleInAfter`. The box is
bought by the Control Plane with one `CreateFleet` per offer, and the offer already carries the
type set and the VRAM floor — the Managed Instances requirement block was a second copy of the
ladder. What each one meant, and where it went:

| Retired | Where the decision lives now |
| --- | --- |
| `<Role>AllowedInstanceTypes` | the offer's `type[,type...]` field ([the offers](#the-offers)) |
| `<Role>AcceleratorMemMinMiB` | the offer's `vramMiB` |
| `<Role>VCpuMin` / `<Role>VCpuMax` / `<Role>MemMinMiB` / `<Role>MemMaxMiB` | the offer's `vcpuMin-vcpuMax` and `memMinMiB-memMaxMiB` |
| `<Role>UseLocalStorage` | nothing yet — see `LlmStorageGiB` above and ADR 0077 open question 8 |
| `<Role>ScaleInAfter` | nothing: the Control Plane terminates the box itself (ADR 0077 decision 5), so there is no "AWS tidies it up after N seconds" to tune. The trap it carried (`-1` keeps a box that re-fetches anyway) goes with it |

`standup.sh` drops all eight from a capture (`af_param_drop`); `update.sh` and a hand-run
`cloudformation deploy` pass no parameters at all, so they simply fall away.
⚠️ **`<Role>TaskMemory`, `<Role>GpuCount` and `<Role>StorageGiB` are NOT in that list** even
though their names look like the block's. They feed the task definition and the root volume.

### `LlmIdleSec`

`AF_ENGINE_LLM_IDLE_SEC` — how long the engine goes unwanted before the controller stops it.
Written into the SSM table rather than passed to 30-ingress, which has no room left for
per-engine parameters (ADR 0071 decision 8).

### `LlmStartDeadlineSec`

How long a start may take before the controller calls it failed. MUST exceed the real cold start
or every start is recorded as a failure and the cooldown doubles away (measured 527 s from S3
with the image pulled over NAT, 586 s with it pulled from ECR; ADR 0070's 300 s default would
fail all of them).

Once this deployment declares an instance-class ladder (`LlmInstanceClasses`, ADR 0074), a start
that changes the rung also has to cover the old instance draining (150 s) plus the EC2 quota release,
which lags the ECS deregistration by up to 6 minutes (measured: `VcpuLimitExceeded` for 389 s
after the old instance had left the cluster), before the cold start even begins — 497 s measured
against the 900 s default. Do not lower this below the default in a deployment that declares a
ladder; a rung change would then be recorded as a failed start every time.

### `LlmMode`

The engine's initial mode, written into the SSM table as the DEFAULT only — once an admin sets
it the stored setting wins, because under on-demand the desired count is not the admin's intent
(ADR 0070 decision 7).

Switching a running engine from `on` back to `ondemand` stops its instance on the controller's next
tick: the demand mark (`engine_<key>_demand_at`) is stale after an admin-driven start, so the
idle rule fires at once. For the `image` role that throws away an instance that just synced up to
48 GB and costs the full sync again. To warm an instance that should then stay on demand, leave the
mode at `ondemand` and wake it with a request through the gateway (measured, ADR 0072 "P2 の残作業
4・5 を実機で押した").

## The `image` role (stable-diffusion.cpp, or ComfyUI since ADR 0072 P2)

A mirror of the `llm` block, and deliberately a mirror rather than a shared set of parameters:
decision 2 is that instance requirements are DECLARED per role, because the floor is a VRAM
number (SDXL fp16 measured 7,379 MiB against the 30B's 20,943) and the two roles must never land
on the same instance — CUDA does not slow down when VRAM runs out, it crashes.

### `ImageEngine`

ADR 0072 decision 4: which server the role actually runs, `sdcpp` (stable-diffusion.cpp, one
checkpoint chosen at start) or `comfy` (this deployment's own ComfyUI image, which switches
checkpoints per REQUEST). One role, one task definition, one service either way — `!If` only
switches the `engine` container's `Image` and `Command` and the engine table's `health`/
`provider` fields, never the shape of the stack. A second container/service pair for `comfy`
would need its own capacity-provider association and can never run at the same time as
`sdcpp`'s (ADR 0071 decision 2 — the two roles never share an instance, let alone the same role run
twice), so there is nothing to gain from one.

`ImageComfyImageTag` (default `v0.34.0`) is the tag inside `af-comfyui`, baked by
`.github/workflows/comfyui-image.yml` (a dedicated `workflow_dispatch`, deliberately NOT part of
`dev-image.yml`/`release.sh` — ComfyUI's pinned upstream revision moves on its own schedule, not
the app's) and copied in by `standup.sh` the same way `af-sdcpp` is, gated on `ImageEngine=comfy`.

### The `image` role's engine table fields, per `ImageEngine`

| | `sdcpp` | `comfy` |
|---|---|---|
| `health` | `/v1/models` | `/system_stats` (unauthenticated, answers immediately — measured) |
| `provider` | `sdcpp` | `comfy` |

Neither engine is a router (`PRESET_FILE`/`ALIAS_FLAG`/`CTX_FLAG` stay empty for both), and the
fetch sidecar does not otherwise know or care which one it is feeding — it already syncs every
ENABLED model's files (`keys.start` then `keys.rest`), not just the selected one, which happens
to be exactly what `comfy` needs (every enabled checkpoint on disk so a request can pick any of
them) and is merely wasted bandwidth for `sdcpp` if an administrator enables more than one
checkpoint while running it. `comfy`'s container command never reads `/models/cmdline`'s
CONTENT (there is no `-m` flag to build — the checkpoint is chosen per request by the `comfy`
provider's own graph JSON), only whether the file is non-empty, which is the same "something is
enabled" gate `sdcpp` uses.

### `ImageImageTag`

Tag inside the `af-sdcpp` ECR repository (the doubled word is the `image` ROLE's container image
tag), used when `ImageEngine=sdcpp`. Upstream is `ghcr.io/leejet/stable-diffusion.cpp:master-cuda`,
2.31 GB and **amd64 only** — there is no arm64 build and G-family instances have no arm64 member
either, so this role is x86_64 by construction (ADR 0071 decision 12). `standup.sh` copies it in
with `crane` before this stack is deployed, and only when `ImageEnabled=true` — the same switch
that decides whether the service exists, read the same way, because a role created against an
empty ECR repository sits in `CREATE_IN_PROGRESS` repeating `CannotPullContainerError` with no
later step able to rescue it.

### `ImageExtraArgs`

Extra `sd-server` flags. `--diffusion-fa` (flash attention) is what the P0 GPU measurement ran
with: 1024px in 20.8-21.0 s and 7.4 GB of VRAM on an L4.

There is no `ImageApiKeySsmParam`, and that is not an omission: stable-diffusion.cpp's server has
no authentication option at all (upstream `examples/server/api.md` documents none), so the
security group — port 8080 from the CP and nothing else — is the whole of this engine's access
control. The llm role's `--api-key` is a second lock that does not exist here.

### `ImageStorageGiB`

The box's root gp3 volume, as [`LlmStorageGiB`](#llmstoragegib). Smaller than the llm role's 120
because the whole role is a 2.3 GB image and a 6.5 GB checkpoint — but not much smaller: the
anonymous host volume is re-allocated per task and never reclaimed, so several starts on one box
each cost a copy. A `comfy` deployment that has enabled 30 GB of checkpoints needs this raised.

### `ImageAllowedInstanceTypes` / `ImageAcceleratorMemMinMiB` and the rest of the requirement block

Gone in ADR 0077, exactly as on the llm role — the offers carry the type set and the VRAM floor
now. See [the llm role's table](#llmacceleratormemminmib-llmallowedinstancetypes-and-the-instance-requirement-pairs).
The measured floor that used to live here is still worth having when writing an offer: SDXL fp16
measured 7,379 MiB, and decision 2's ">= 8 GB" was the default. Note what that does NOT do — a
16 GB T4 clears it, while every image measurement there is (SDXL 8.0 s, Z-Image 10.5 s, klein 4B
4.0 s — ADR 0072) was taken on a 24 GB L4. The type set is what holds the role to measured
hardware; the floor only keeps a card too small to load the checkpoint out.

### `ImageIdleSec`

`AF_ENGINE_IMAGE_IDLE_SEC` — how long the engine goes unwanted before the controller stops it.
HALF the llm role's window on purpose: an image is a 21-second request with no conversation
around it, while an LLM turn sits inside a session that thinks for minutes between requests.
900 s is $0.31 of idle GPU against a ~300-second re-wake, which is the trade this number is
making.

### `ImageStartDeadlineSec`

How long a start may take before the controller calls it failed. sd-server measured 195 s from
task creation to listen (135 s of that a GHCR pull, so less from ECR) — but the dominant term is
how long the INSTANCE takes to appear, measured anywhere from 8 to 88 s and not something a deadline
should be tuned against (ADR 0071, P0 measurement 1). With an `ImageInstanceClasses` ladder
declared, the rung-change budget under `LlmStartDeadlineSec` applies here unchanged.

### `ImageMode`

As `LlmMode`: the initial mode, a default the stored setting overrides — including the `on` →
`ondemand` trap described there, which is where it was actually stepped on.

## The instance classes

`LlmInstanceClasses` / `ImageInstanceClasses` (ADR 0074) are the ladder of GPU rungs an
administrator may switch a role between from the Console, without a stack update. **Both default
to empty, and empty means the feature does not exist**: with neither a ladder nor an offer list
declared the Control Plane never asks EC2 for a box at all, and the role has no way to start
(ADR 0074 decision 3, inherited by ADR 0077).

One rung per `;`, seven `|`-separated fields, the last optional:

```
id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB[|usdPerHour]
```

```
LlmInstanceClasses=l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB|44000|g6e.xlarge,g6e.2xlarge|4-8|30000-65536
```

- **The FIRST rung is the default**, and since ADR 0077 it is the only place the box's shape is
  declared at all (the template's requirement block is gone). It is what the Console offers as
  "back to the default", and what a role with no saved choice starts on.
- **`vramMiB` is both the floor asked of the card (`AcceleratorTotalMemoryMiB.Min`) and what a
  model's demand is compared against** — and it is **the card's physical size**, not an
  operational cap. 🔴 Declare it from what the hardware reports: an L4 says
  `Total VRAM 22563 MB`, so **22000 is the number**. Copying a placement floor into it (the
  retired `<Role>AcceleratorMemMinMiB` was 8000) is a real bug — measured 2026-09-11 on the dev
  deployment, a rung declaring 8000 made the panel say `vram_fits: false` for a model
  that then generated perfectly well on that very card. Shading it downward is just as wrong in
  the other direction once the demand includes the KV cache: a 30B at 32k context really
  occupies 20,712 MiB, which fits 22,563 with 1.8 GB to spare and does NOT fit a declared
  21,000. See ADR 0074, "open question 7 for the llm role".
- **`usdPerHour` is display-only and optional.** Nothing computes with it and neither EC2 nor
  the Pricing API is asked (ADR 0045 decision 21). Leave it out and the panel names no price,
  which beats naming a wrong one.
  **It is the EC2 price itself since ADR 0077** — the 7.80% ECS Managed Instances management
  fee is gone with the providers, so the g6.xlarge figure that used to be declared as $1.26
  (billed, against the Pricing API's $1.1672) is now the EC2 one. The rule that survives is the
  rule that mattered: **declare what the bill said, never a list price**, and rewrite it when
  Cost Explorer has a final figure.
- A malformed rung is dropped with a log line; a malformed PRICE only drops the price.

**What changing a rung does.** A rung IS the request: the Control Plane puts its type set,
VRAM floor, vCPU and memory bounds into the `CreateFleet` overrides for the next box it buys
(ADR 0077 decision 8). Nothing is written to AWS when the choice is saved, and nothing has to be
re-applied before a start — the four-field read-modify-write of `UpdateCapacityProvider` (ADR
0074 decision 5) is gone with the capacity providers, and so is the whole class of failure where
"declared" and "actual" drift apart. A misspelled type is refused **by the `CreateFleet` call
itself**, not by a box that never arrives.

⚠️ **It still reaches the NEXT box only.** A running engine keeps its card until the box is
replaced, and the Control Plane will not buy a new one while the old one is still `running`
(the drain wait, ADR 0071 decision 7 / ADR 0074 decision 4). What changed is why: it is now the
CP's own terminate that has to finish, not an AWS-side scale-in whose clock nobody could see.

⚠️ **The ladder is not checked against the account's G-family vCPU quota**, and it cannot be:
the CP has no `service-quotas` permission and the quota differs per deployment (96 in production,
8 on a fresh account). A rung above it is selectable and simply never buys — which now surfaces
as an error code in the `CreateFleet` response instead of a service event, and the panel shows
it the same way.

**Permissions.** `ec2:CreateFleet` / `DescribeFleets` / `DeleteFleets` and `iam:PassRole` on the
engine instance role, all in this stack's own `CpIngestPolicy`
([the ingest permissions](#the-ingest-permissions)). The three ECS capacity-provider grants the
ladder used to need — `DescribeCapacityProviders`, `UpdateCapacityProvider` and the
cluster-scoped `PutClusterCapacityProviders` nobody could find in the code — are **gone**.

## The offers

`LlmOffers` / `ImageOffers` (ADR 0075) are the ladder above with an eighth field: **how the box
is bought**. The Control Plane narrows the list to the offers whose `vramMiB` covers the demand
of the enabled models and then tries what is left **in the order written**. Since ADR 0077
"trying" one is a single `CreateFleet(type=instant, TotalTargetCapacity=1)`: the row's type set
goes into the overrides, `buy` decides on-demand or Spot, and **the answer comes back in that
one call** — a box id, or an error code. What
[`<Role>OfferBudgetSec`](#roleofferbudgetsec) bounds is the wait after that.

```
id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour|buy
```

```
ImageOffers=spot3|22GB+ Spot (g6/g5/g6e)|22000|g6.xlarge,g5.xlarge,g6e.xlarge|4-8|15000-65536|1.57|spot;l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26|od;l40s|L40S 48GB|44000|g6e.xlarge|4-8|30000-65536|2.91|od
```

⚠️ **One line, separated by `;`.** The value is pasted verbatim into the engine table's JSON, and
a raw newline inside a JSON string is not legal there — the same constraint the ladder has always
had, and the same reason a deployment capture is one `key=value` per line.

- **`buy` is `od` or `spot`; empty and absent both mean `od`.** So a seven-field line is a valid
  offer, which is the whole migration: **`<Role>InstanceClasses` can be copied into
  `<Role>Offers` unchanged** and reads as "all on demand". Leave `<Role>Offers` empty and the
  Control Plane reads the ladder itself the same way — nothing in this template chooses between
  them, and a deployment that declares neither never calls ECS about capacity at all (ADR 0074
  decision 3, inherited).
- 🔴 **`spot` belongs to the `image` role only.** `LlmOffers` takes the field because the format
  is one format, but a two-minute termination notice mid-conversation costs a 527-586-second
  cold start to recover from (ADR 0075 decision 9, inherited). There is no longer a "no Spot
  provider exists" safeguard behind that rule — the Control Plane **drops a `spot` row in
  `LlmOffers` at parse time**, with a log line (ADR 0077 decision 9).
- 🔴 **The list is tried in the order WRITTEN — the Control Plane does not sort by price.**
  Cheapest-first is an operator convention, not a mechanism. Three reasons it stays that way, and
  all three are reasons a sort would hurt: a row with no price would sort last exactly when it is
  the row most worth trying (a Spot row's billed price is by definition unknown at first); the
  order and the prices are written by the same person, so a sort lets one number silently
  overrule a deliberate order; and **one row does not have one price** — a multi-type row is
  whatever the allocation strategy picks, so the three-type Spot row above is $0.67-ish on a
  g6.xlarge and $1.36 on a g6e.xlarge.
- ✅ **A multi-type row is no longer a lottery, though.** Managed Instances had no allocation
  strategy at all, which is how ADR 0075 run 3's three-type Spot row delivered a **g6e.xlarge**
  rather than the cheapest g6. A `CreateFleet` says it: `price-capacity-optimized` for `spot`
  (the most available pools, then the cheapest of those) and `prioritized` for `od`, where the
  Control Plane writes the override `Priority` in the order the types are declared (ADR 0077
  decision 1). **Keep the intended type first.**
- **`usdPerHour` is therefore declared as the price of the DEAREST type the row can buy** —
  **the EC2 price itself since ADR 0077**, because the 7.80% Managed Instances management fee is
  gone with the providers. Never a list price (the rule is under
  [the instance classes](#the-instance-classes)); understating it makes the panel's comparison
  useless in the direction that costs money. ⚠️ The `1.57` above is arithmetic, not a bill: the
  only Spot box this deployment ever bought ran for 403 seconds, so Cost Explorer has nothing
  final yet. **Rewrite it from the bill when there is one.**
- **The id is what an administrator's saved choice names.** Keep ids stable across a migration
  from `<Role>InstanceClasses` and nothing moves for the deployments that had already chosen one;
  an unsaved choice now means *automatic*, and a saved one means *pinned to that offer, with no
  fall-through* (ADR 0075 decision 8).
- A malformed offer is dropped with a log line, as a malformed rung is.

### `<Role>OfferBudgetSec`

🔴 **What this bounds changed in ADR 0077, and the default with it.** It used to be "how long
one offer may wait for an instance before the next is tried", counted from the moment desired
went to 1 — because under Managed Instances nothing said whether a box was coming. A
`CreateFleet` answers in the call, so **moving to the next offer needs no clock at all**; what is
left to wait for is the box this call DID buy **registering with the ECS cluster**. Past that,
the Control Plane terminates it and moves on.

**The default is 300 seconds**, in this template and in the Control Plane, which is ADR 0077
decision 1's number: the slot pool measured boot to ECS registration at 21 s (77 s with a
home-baked AMI), so 300 is more than ten times a healthy registration — and a GPU AMI is not a
slot's AMI, so the margin is deliberate rather than measured. Raise it if a start is ever
recorded as failed with a box that turned out to be fine; past it the box is terminated and the
next offer is tried, so a ceiling that is too low spends money and finds nothing.

🔴 **A deployment captured before 0.20.0 carries `180`, which is the OLD meaning's default.**
`standup.sh` drops exactly that value so the stack falls to the 300 above, and says on stdout
that it did; any other value is treated as a choice and passed on untouched. A deployment that
really wants 180 as a registration ceiling has to set it again after a stand-up — that is the
price of not being able to tell a stale default from a deliberate one.

It is not the whole wait either: with three offers the worst case is three failed purchases plus
a registration plus a cold start, and the bound a caller actually sees is the gateway's
`AF_ENGINE_WAKE_TIMEOUT` (900 s, ADR 0071 decision 5).

⚠️ **The failure codes moved with it.** `UnfulfillableCapacity` / `VcpuLimitExceeded` /
`InsufficientInstanceCapacity` were read out of SERVICE EVENTS, with a wait attached to each;
they are now `Errors[].ErrorCode` in the `CreateFleet` response, read at once. No stock means
the next offer; a quota refusal skips the rest of that purchase type (on-demand and Spot are
separate quotas). The vocabulary is being measured — ADR 0077 open question 3.

## The fetch sidecar

One script for both roles: `deploy/aws/ecs/engine-tools/fetch-models.sh`, baked into the engine
tools image (ADR 0072 decision 1(a); it was inline in the template's `Mappings` until the size
pass below took it out — [The engine tools image](#the-engine-tools-image)). It reads the active
set the Control Plane published to SSM, syncs the files the engine is about to load, and writes
`/models/cmdline` — the model-specific half of the argument list. Everything role-specific is an
environment variable: `ACTIVE_PARAM`, `BUCKET`, `MODELS_DIR`, `PRESET_FILE` and
`ALIAS_FLAG` / `CTX_FLAG`.

🔴 **The S3 layout this script reads is a CONTRACT, and THIS SECTION is where it is written
down.** `llm/<name>.gguf`, `llm/loras/`, `image/checkpoints/`, `image/loras/`, `image/vae/`,
`image/text_encoders/`, `image/diffusion_models/` — ComfyUI's own convention (ADR 0071 decision
6, ADR 0072 decision 2). Four parties depend on it and none of them can see the others: this
script, the ingest task that writes the keys, the `comfy` provider that names them in a graph,
and the Console form an administrator types one into. That is why it stays here rather than
moving into the script with the code: a comment in one of four places is not a contract. The
script points back at this heading instead.

### `SYNC_ALL`: which engines need more than the starting model on the instance

Whether a role syncs the OTHER enabled models is a per-engine question, and it is asked through
`SYNC_ALL` rather than inferred:

| engine | switches models? | `PRESET_FILE` | `SYNC_ALL` | on the instance |
|---|---|---|---|---|
| llama.cpp | yes, it is a router | a path | — | every enabled model |
| sd.cpp | no, one checkpoint at start | `""` | `""` | the starting model only |
| ComfyUI | **yes, per request** | `""` | `"1"` | every enabled model |

Enabling a model OFFERS it and selecting one LOADS it, so a role that cannot switch must not
pay to stage models nobody can reach — S3 to EBS is 92-147 MB/s measured, and that is somebody
else's checkpoint on every cold start. That is why sd.cpp stays at the starting model.

⚠️ **The trap this table exists to prevent.** `/tmp/keys.rest` used to be written only inside
`if [ -n "$PRESET_FILE" ]`, which read as "only a router needs the others" — true while the only
two engines were llama.cpp (a router, and it has a preset) and sd.cpp (neither). **`comfy` broke
the equivalence: it is a router with no preset file.** The image container sets `PRESET_FILE: ""`,
so `keys.rest` was empty and only the starting checkpoint was fetched. Every other model then
400s at the engine with `Value not in list: unet_name: 'flux-2-klein-4b.safetensors' not in []`
— the file is in the active set, in S3, and named correctly in the graph; it was simply never
downloaded. The sidecar printed `every enabled model is on this box` while that was false, which
is why the fetch log read as healthy. (Measured on af-sandbox, ADR 0072 P2 実機検証; the message
now says `sync done`, and `engine-sidecar-test.sh` covers both settings.)

The rule to hold onto: **`PRESET_FILE` says how one engine is CONFIGURED, never what has to be
on the instance.** It still gates the preset file and the `START` re-derivation that goes with it —
those really are router-with-a-preset concerns — and nothing else.

### The START model gates the engine; the rest are synced behind it

Since 2026-09-09 the sidecar does **not** fetch everything before the engine may start. It syncs
the starting model (and every LoRA — they are small and decide what the engine can be asked for),
writes the preset and `/models/cmdline`, **touches `/models/ready`**, and only then fetches the
remaining enabled models. The engine containers wait for that MARKER rather than for the sidecar
to exit, so `DependsOn` on both engines is `START`, not `SUCCESS`.

Why: a router syncs every enabled model (decision 9) but loads one, and the sync is serial. On
the deployment the starting model cost 117 s and the second 5 s — but the cost is per model, so
five enabled models put minutes of weights nobody asked for in front of the first token. This is
the "time wall" that ADR 0072 open question 11 measured at about five models for a ten-minute
cold start; moving it off the critical path is what makes a bigger catalogue survivable.

Measured on the deployment the day it went in, same two models as every other number here:

| | old order | start-first |
|---|---|---|
| sidecar reads the active set | +50 s | +50 s |
| starting model (18.5 GB) on disk | +171 s (after the 1.1 GB one) | **+165 s** |
| **llama-server listening** | +184 s | **+169 s** |
| the other model (1.1 GB) lands | before the engine | **+171 s, engine already up** |
| model in VRAM | +275 s | **+267 s** |

⚠️ **With this catalogue the win is only about 8-15 seconds, and that is the honest number** —
the deferred model is 1.1 GB, so there was barely anything to defer. What the run proves is the
MECHANISM (`engine may start; 1 file(s) still to sync`, then the engine listening four seconds
later, then the second model arriving behind it). The saving is one deferred model's sync time,
so it grows with the catalogue and is worth nothing on a single-model deployment.

Two consequences to keep in mind:

- **A request for a model that has not landed yet fails rather than waits.** llama.cpp's router
  starts happily with preset paths that do not exist (measured, ADR 0072 P1 point 6) and errors
  only on a request for that model. The window is the sync time of the models after the first.
  (Answered by `PENDING_PARAM` below, 2026-09-11: the caller is now told to retry instead.)
- 🔴 **Every exit from the sidecar must leave the marker**, which is why it opens with
  `trap 'touch $MODELS_DIR/ready' EXIT`. Without it a failed fetch — or an empty catalogue —
  leaves the engine waiting out its whole hour-long loop on an instance billing at $1.26/h, and the
  service never stabilises (decision 1(b)). With it, the wrapper finds no `cmdline` and idles,
  which is the case everything downstream already understands.
  `deploy/local/engine-sidecar-test.sh` asserts both the ordering and the marker, and each
  assertion was checked against the defect it is meant to catch.

### `PENDING_PARAM`: what this instance has NOT synced yet

The window above is real and nothing outside the instance could see it, so the sidecar
publishes it: `<EnginesSsmParamName>/<role>/pending`, beside the active set it is working from.

**The contract, and it is the whole of it:**

- a **JSON array of bare S3 keys** — exactly the keys as they appear in the active set, no
  local paths — naming the files this instance still owes;
- written **before** the first fetch (so it names everything), rewritten **after every file
  lands**, and left as `[]` when the instance is in step. `[]` is also what an engine with
  nothing to load writes, so a parameter left behind by a previous instance that died
  mid-sync cannot hold requests for ever;
- Standard tier, so **4,096 characters**. The list is trimmed from the end to fit and says so
  in the log — an under-reported pending list lets a request through, which is the behaviour
  the fleet had before this existed, and the opposite mistake would hold requests for files
  that are already there;
- a failed write is logged and **not** fatal. The sync is what matters; the parameter is an
  optimisation of the refusal.

The Control Plane reads it in `engine_pending.go` and answers `503 engine_waking` — the
retryable refusal every caller in the fleet already handles — for a request naming a model
whose files are on that list, instead of the engine's own bare 400 (ADR 0072 P2 欠落 7). 🔴 A
parameter that is **absent or unreadable is UNKNOWN, never "still syncing"**: a deployment
whose engine stack predates this behaves exactly as it did before, and so does one where SSM
is unreachable.

**To see it happening, read the CP log** for
`engines: <role>: refusing a request for <model> with engine_waking — N file(s) still syncing`,
which carries the role, the model, how many files are left and the `Retry-After` the caller
was given. Throttled to one line per minute per (role, model), because the caller retries
every few seconds for the whole window — so a sync that is still going shows up as a line a
minute, not as a wall. This is the only place the window is visible from outside the
instance: the refusal is a 503 body, and the one caller that hits it in practice
(`generate_image`) retries it for up to a quarter of an hour and returns only the eventual
success, so nothing downstream keeps the body. Lines for two models at once mean two are
syncing, which is the ordinary shape of a cold instance.

`EngineTaskRole` gains `ssm:PutParameter` on the path it already reads, which keeps this
stack's IAM closed inside it (ADR 0071 decision 8).

### `WATCH_SEC`: a model enabled while the instance is up

The sidecar used to exit after its first sync, so a model enabled afterwards reached the
instance only at the next cold start (ADR 0072 open question 3). On the `llm` role that is
"wait for the next start"; on `image` it is worse, because the model appears in
`generate_image`'s list the moment it is enabled and the request then 400s (欠落 8). Forcing
`mode` off and on again — rebuilding the instance and re-syncing 48 GB — was the only way out.

So the sidecar stays resident: every `WATCH_SEC` seconds (60 in both task definitions) it
re-reads the active set and, when the document has CHANGED, syncs whatever is new under the
same `SYNC_ALL` rule as the first pass. What it does not touch is `/models/cmdline`, the preset
file or the marker — those were read by the engine at start, and changing what a running engine
loads is still the next start's business (decision 4).

- `WATCH_SEC=0`, or anything that is not a number, is "sync once and exit" — the old behaviour.
- The files it fetches ride the same `pending` list, so the gateway's wait covers them too and
  ends by itself when the last one lands.
- ⚠️ **What this does NOT fix for `llm`.** A model enabled later lands on disk, but the router
  read its preset at start, so the new section is not there and the model is still unreachable
  until the engine restarts. What the watch buys that role is a start that no longer has to
  download it. For `image` (ComfyUI enumerates its model directories per request) the file
  landing IS the fix.
- 🔴 The key lists live in `${TMPDIR:-/tmp}/engine-fetch.$$`, not in fixed `/tmp` paths. A
  script that exits cannot collide with itself; a resident one can, and it did — a leaked
  watcher from an earlier test run rewrote the lists under a running one and turned every
  later assertion in `engine-sidecar-test.sh` into a different failure.

### Tuning the AWS CLI is NOT worth it — measured

The obvious next lever after start-first sync was the copy itself: 18.5 GB at an effective
161 MB/s looked slow next to the 567 MB/s Mountpoint got off the same class of instance. It is slow,
but **the AWS CLI's settings are not why**. `harness/probe-s3-fetch-tuning.sh`, one instance, the same
object four times, stock first:

| `max_concurrent_requests` / `multipart_chunksize` | | |
|---|---|---|
| stock (10 / 8 MB) | 93 s | 199 MB/s |
| 20 / 16 MB | 85 s | **218 MB/s** |
| 40 / 32 MB | 86 s | 215 MB/s |
| 64 / 64 MB | 85 s | **218 MB/s** |

**It plateaus at concurrency 20 and never moves again** — 40 and 64 buy nothing. The whole
tuning is worth about 9%, i.e. 8 seconds off a 267-second cold start, in exchange for the
sidecar writing an `~/.aws/config` and this template spending budget it does not have. **Not
adopted.**

**s5cmd was then measured, and it revises that reading.** The Go client, same object, same instance,
same task as an `aws s3 cp` control (`harness/probe-fetch-client.sh`):

| | | |
|---|---|---|
| `aws s3 cp` (control) | 115 s | 161 MB/s |
| **s5cmd v2.2.2** | **91 s** | **203 MB/s** |

26% — worth 24 seconds, real but modest. And note where it lands: **203 MB/s, next to the tuned
CLI's 218 MB/s.** Two clients with nothing in common — Python and Go — converging within 7% is
not what a client-side ceiling looks like. 🔴 So **"the CLI is the bottleneck" was wrong**: the
limit is downstream of it, and the likeliest candidate is the write into the instance store,
since the 567 MB/s Mountpoint figure was a read to `/dev/null` with no file being written and
the disk's own direct READ measured 391 MB/s. Not proven — no one has measured the write side on
its own — but a client swap is clearly not where the remaining time is.

**Not adopted.** 24 seconds off 209 needs either a custom image for the fetch sidecar or a
version-pinned binary bootstrapped out of the models bucket (which is how the probe does it,
because the engine subnet has no egress to github — measured), and this template has 38 bytes of
headroom. Worth revisiting only if the cold start becomes the thing that matters again.

And **stock here was 199 MB/s while the real sidecar sees 161 MB/s**. The likely difference is
that on a real cold start the engine image is still being pulled: ECS starts each container as
its own image lands, so the small aws-cli image is fetching while the 2.47 GB llama.cpp image is
still coming down (measured: fetch logging at +54 s, `pullStoppedAt` at +88 s). That contention
is not removable — the pull has to happen.

⚠️ **It is a LITERAL block (`|-`), never a folded one (`>-`).** YAML folding keeps a
MORE-INDENTED line literal, so a continuation line indented to line up with its command silently
keeps its newline — and the shell then reads the second half as a new command. Measured on the
live deployment: a `jq` filter indented under its own `jq -r` ran as `jq -r --arg s "$START"`
(which dumps the whole document) followed by `sh: [(.models[]?|…: command not found`. The
service reached a steady state with the idle placeholder and nothing anywhere said why — one GPU
instance and ten minutes to notice. One command per line, and `deploy/local/engine-sidecar-test.sh`
extracts the script as deployed and runs it (CI runs that).

Four rules, each with a failure behind it:

- **`ParameterNotFound` is EMPTY, not an error.** 60-engines is created BEFORE 30-ingress, so at
  stack-creation time there is no Control Plane and no parameter at all. A `set -e` that failed
  here would bring the two-pass stand-up back in a new shape (ADR 0072 decision 1(b)).
- **Both roles fetch the STARTING model first and every OTHER enabled model behind it** (see
  "The START model gates the engine" below) — the sidecar does not distinguish by role. For
  `sdcpp`, which holds exactly one checkpoint, an administrator who enables more than one pays
  for a sync that never gets used; for the `llm` router and for `comfy` (ADR 0072 P2), every
  enabled model really can be asked for, so the extra sync is not waste. Enabling a model
  OFFERS it; `sdcpp`'s `selected` (or the llm router's `default`) decides what is LOADED first.
- **The llm role's cold start therefore grows with the sum of the enabled GGUFs** — ADR 0072
  decision 9, and what the panel's "sync +N s" estimate is for. The same is true of `comfy`'s
  enabled checkpoints, for the same reason.
- **A file already on the instance is not re-fetched.** The volume is fresh per task today, so this
  is currently a no-op — it is what makes a warm instance worth anything if ADR 0071 decision 7(c)
  ever gets one.

**Router presets (`PRESET_FILE`, the llm role).** With it set the script also writes an INI
file, one section per catalogue model: the section NAME is the catalogue id, `model` is the
path, `c` is that model's window, the model's own `args` become preset keys (`--jinja` →
`jinja = true`, `-ngl 99` → `ngl = 99`) and the starting model gets `load-on-startup = true`.
`/models/cmdline` then holds `--models-preset <file>` and nothing else: `-m`, `--alias` and `-c`
leave the command line entirely (ADR 0072 decision 3), which is why `ALIAS_FLAG` and `CTX_FLAG`
are empty for BOTH roles now. Measured against llama.cpp b10853:

- a preset section whose name matches no file in any model source **defines** a model, so the
  catalogue id is the model id even though the file is called something else entirely;
- the router passes `--alias <section>` to each instance itself, so an `alias` key is not needed;
- a section whose `model` path is missing does NOT stop the router — it starts, lists the model,
  and only a request for that one fails (`500 model name=… failed to load`).

**Pinned LoRAs (ADR 0072 decision 5, the llm half).** A catalogue row of kind `lora` names the
model it belongs to in `base_model`, and the active set carries that as `lo` on the MODEL: a list
of `<key>:<scale>` entries. The script joins them into ONE preset key,
`lora-scaled = /models/llm/loras/a.gguf:1,/models/llm/loras/b.gguf:0.8`. To opencode this is an
ordinary model id that happens to include a fine-tune; choosing adapters per request is the other
half of the decision and is deliberately later (it is the first thing that would make the gateway
rewrite a request body).

🔴 **One key with a comma-separated list, never one key per adapter.** llama.cpp parses a preset
into `std::map<common_arg, std::string>` and its INI reader assigns `parsed[section][key] =
value`, so a second `lora-scaled =` line OVERWRITES the first and two adapters silently become
one. `--lora-scaled FNAME:SCALE,...` is the documented multi-adapter form and the only one that
carries both a path and a strength (read in `common/preset.cpp` and `common/arg.cpp`,
2026-09-11). `--lora-scaled x:1` is exactly `--lora x`, so the scale is always written and there
is one code path rather than two.

⚠️ That also makes `,` and `:` separators rather than characters: an S3 key holding either would
move the boundary between two adapters and load a path nobody named. The Control Plane refuses
such a key when it publishes the active set, for every role.

`jq`, not a JSON-in-shell parser: `public.ecr.aws/aws-cli/aws-cli` carries `jq`, `python3` and
`bash` (verified with `crane export`).

## The idle wrapper

Both engine containers start as `sh -c 'if [ -s /models/cmdline ]; then exec <engine> $(cat
/models/cmdline); else … sleep infinity; fi'`. An engine with nothing to load has to COME UP
rather than fail, or CloudFormation waits on a service that never stabilises.

- **The binary path is spelled out** because the entry point is being overridden. llama.cpp's
  own `ENTRYPOINT` is `["/app/llama-server"]` and `/app` is NOT on `PATH` (measured with
  `crane config ghcr.io/ggml-org/llama.cpp:server-cuda`); sd-server's image entry point is
  `/sd-cli`, the one-shot CLI, and it has no `curl` either — which is why the fetch is a
  separate `aws-cli` container (measured 2026-09-07).
- **sd-server's flags are spelled differently**: `--listen-ip` / `--listen-port`, not `--host` /
  `--port`. `--lora-model-dir` is STATED rather than defaulted, because the default is the
  current directory — `/` here — and that is the leading suspicion for the async job API dying
  in `/proc` (ADR 0072 open question 6).
- **`--models-max` is on the llm wrapper, not in the preset**: it is a property of the INSTANCE (how
  many models fit in this L4's VRAM), not of a model, so it stays a stack parameter
  (`LlmModelsMax`) while everything per-model comes from the catalogue.
- **`$(cat …)` is deliberately unquoted** — the file IS an argument list. The Control Plane
  refuses to publish an S3 key containing whitespace for exactly this reason.
- **`LLAMA_CACHE=/tmp/llama-cache` on the llm container.** `-hf` is never used (ADR 0072
  decision 3) -- every model arrives through the fetch sidecar -- but llama-server writes to its
  cache directory regardless of how the model got there, and the default is under the model
  volume. Pointing it at `/tmp` keeps it off the volume whose free space the next cold start
  depends on.
- **Idling is not free**, and the template is not what makes it cheap. A placeholder container
  reaches RUNNING and never warms, and `running && !warmed` is not a failure state, so the
  controller would re-examine it every five seconds for ever. The Control Plane's
  `decideEngineAction` refuses to start — and stops — an engine whose catalogue is empty
  (`no_model`, ADR 0072 decision 1(c)). Without that half, `mode=on` buys $1.26/hour for
  `sleep infinity`.
- **`comfy`'s wrapper (ADR 0072 P2) does not build a command-line flag for the checkpoint at
  all.** ComfyUI reads `models/checkpoints`, `models/loras`, `models/vae`, `models/text_encoders`
  and `models/diffusion_models` relative to its own working directory — which IS the S3 layout
  ADR 0071 decision 6 already mirrors onto `/models/image`. So the wrapper's entire integration
  is `rm -rf /ComfyUI/models; ln -sfn /models/image /ComfyUI/models` before `exec`: no copying,
  and no need to tell ComfyUI which checkpoint to load, because the `comfy` provider picks one
  per REQUEST in its own graph JSON. `/models/cmdline`'s CONTENT is therefore irrelevant to this
  branch — only its non-emptiness is read, as the same "something is enabled" gate `sdcpp` uses.

  ⚠️ **Both halves of that line are load-bearing, and each is a trap the other creates.**

  - **`rm -rf` FIRST.** ComfyUI's repository TRACKS `models/` — every subdirectory
    (`checkpoints/`, `diffusion_models/`, `text_encoders/`, `vae/`, …) exists in the clone,
    each holding a `put_..._here` placeholder. So `deploy/aws/ecs/comfyui/Dockerfile`'s
    `git clone` bakes a REAL directory, and `ln -sfn` against a real directory links INSIDE
    it (`/ComfyUI/models/image`) instead of replacing it. `-n` does not help: it only changes
    behaviour when the link name is a symlink to a directory. The engine then starts, answers
    `/system_stats`, passes health — and 400s every request with
    `Value not in list: ckpt_name: 'sd_xl_base_1.0.safetensors' not in []`, an EMPTY list,
    because it is reading the clone's placeholder `models/checkpoints/`.
  - **Never a trailing slash.** After the first start `/ComfyUI/models` IS a symlink;
    `rm -rf /ComfyUI/models/` would follow it and delete the CONTENTS of the shared model
    volume — every checkpoint the fetch sidecar just spent minutes pulling from S3.

  **Why the bench did not catch it** (and why "measured on a real GPU" was not enough):
  `harness/bench-image-engine.sh --baked` bind-MOUNTS the models volume onto `/ComfyUI/models`,
  and a mount replaces a directory where a symlink cannot. The bench and the task definition
  differed in exactly this one line, so 13/13 bench scenarios passed against an integration
  path nothing had ever run. Measured on af-sandbox, ADR 0072 P2 実機検証.

  **`--verbose DETAIL` is what puts the VRAM a model actually took into CloudWatch.** ComfyUI
  logs `Model loaded: patcher=… model=… ram_mb=… vram_mb=…` at its own DETAIL level
  (`comfy/model_management.py`, v0.34.0), which sits BELOW `INFO` in
  `LOG_LEVELS = ('DEBUG', 'DETAIL', 'INFO', …)`. Without the flag the console handler is at
  `INFO`, so the line is not written **anywhere**: `main.py` passes an explicit (empty) file-output
  list, which stops `app/logger.py` from ever falling back to its `comfyui_detail.log` default.
  The measured VRAM of a checkpoint is the one number ADR 0074's rung warnings are short of on
  the image side, and it cannot be recovered afterwards.

  The level is deliberately sparse rather than a per-step firehose — in the whole v0.34.0 tree
  there are six `detail(...)` call sites, and the sampler's is guarded by `first_step`, so a
  generation adds about **three lines** (sampler summary, first step, cache evictions) plus one
  per model LOAD, against the 80 `logging.info` sites already writing at `INFO`. At
  `LogRetentionDays` (14 by default) that is not a cost worth a parameter. It also drops nothing:
  DETAIL raises the console level, and `WARNING`/`ERROR` keep going where they went.

## The engine tools image

`af-engine-tools` holds the three scripts the task definitions run: the fetch sidecar and the
ingest task's `fetch` and `upload` steps. Built from `deploy/aws/ecs/engine-tools/` by
`.github/workflows/engine-tools-image.yml`, pushed to GHCR, copied into this deployment's ECR by
`standup.sh`, and named by `EngineToolsImageTag`.

**Why they left the template.** They were 7,810 bytes of inline shell in a file measured against
a 51,200-byte limit (`ecs-lifecycle-stub-test.sh` case 3b-2), and by then everything else that
could move had. The move also ended two third-party `:latest` pulls in a start path
(`public.ecr.aws/aws-cli/aws-cli:latest` and `curlimages/curl:latest`), which is the practice
ADR 0071 decision 6 exists to prevent; there is one pinned image of ours now.

🔴 **What it costs is that the script and the template stop shipping together**, and the cost is
paid by a version number rather than by discipline. `standup.sh` copies images one step BEFORE
it deploys the stack, so "old image, new template" is a real ordering and not a hypothetical —
and its natural failure is the quietest one available here: the old script runs, the service
reaches a steady state, and the engine does the previous release's thing until somebody looks.

So the template declares `ENGINE_TOOLS_CONTRACT` on every container that runs one of the
scripts, each script compares it with the `CONTRACT` file beside it, and a mismatch **exits 78
with three lines in the task's own log, having done nothing**. It catches both directions, and
the unset case (a template that predates all of this) too.

**The number covers the INTERFACE, not the code**: the environment variables each script reads,
the SSM keys they name, and the files they write under `$MODELS_DIR`. Changing what a script
does without changing that interface does not need a bump; adding a required variable does.
Bump `deploy/aws/ecs/engine-tools/CONTRACT` and the `Value` in `60-engines.yaml` in the same
commit — `deploy/local/engine-sidecar-test.sh` fails when they disagree, and also when a
container runs one of the scripts and declares no contract at all.

⚠️ **The idle wrappers did NOT move, and cannot.** They run in the ENGINE's container —
`/app/llama-server`, `/sd-server`, `/ComfyUI/main.py` — and a container runs one image. Two of
those three images (`af-llamacpp`, `af-sdcpp`) are pinned copies of third-party builds that this
repository does not build, so baking a wrapper into them would mean forking them. The 1,318
bytes they occupy stay inline, and [The idle wrapper](#the-idle-wrapper) is still where their
traps are written down.

**To change a script**: edit it, run `deploy/local/engine-sidecar-test.sh` (it runs the real
thing against a stub `aws`), run `engine-tools-image.yml` with a new tag, and set
`EngineToolsImageTag` in `params/60-engines` before the stack update that needs it.

**Getting the image into a deployment is not a hand-run step.** All three routes do it, in the
one order that works — **ECR repository (20-platform) → image (`crane copy`) → stack
(60-engines)**:

| route | what it does |
| --- | --- |
| `standup.sh` | copies the tag `params/60-engines` names, before deploying the stack |
| `update.sh` / `release-ecr.sh` | deploys 20-platform (through a change set it prints), then copies the tag 60-engines asks for when ECR has not got it, then deploys 60-engines |
| `dev-deploy.sh` | the same, plus it BAKES a per-commit tag when `engine-tools/` has moved on |

🔴 Reversed, nothing fails at the time. The stack deploys perfectly and both roles' fetch
containers and the ingest task sit in `CannotPullContainerError` while the service reports a
steady state — which is how this was found, on the deployment, the first time 0.19.0 was put on
one (2026-09-11: GHCR empty, ECR empty, 20-platform not updated, three hand-run steps to get
out of it, and none of them in a script). `deploy/local/ecs-lifecycle-stub-test.sh` case 3i
holds the order, with the two positive controls (swap the steps, drop the copy).

**The order was run once on a real deployment (2026-09-11, `dev-deploy.sh`).** On a deployment
that had already been through the three hand-run steps, so this is what the SECOND and every
later run looks like — three decisions, all of them "nothing to do", printed rather than
assumed:

```
==> af-engine-tools:2026-09-11 is in ECR and engine-tools/ is unchanged - nothing to do
==> plan for <ingress stack> (ImageTag=0.18.1-dev-2e765534):
      1. <platform stack> (20-platform - it owns the ECR repositories)
      3. <tts stack> (50-tts)
      4. af-engine-tools:2026-09-11 into ECR, then <engines stack> (60-engines)
      5. <ingress stack> (30-ingress, ImageTag=0.18.1-dev-2e765534)
==> 20-platform: building a change set for <platform stack>
    Waiting for changeset to be created..
    No changes to deploy. Stack <platform stack> is up to date
    - nothing moved in 20-platform - not deployed
==> engine tools image for <engines stack>: af-engine-tools:2026-09-11
    - af-engine-tools:2026-09-11 is already in ECR
==> cloudformation deploy <engines stack> (60-engines, parameters unchanged)
    No changes to deploy. Stack <engines stack> is up to date
```

Two things worth reading off it. **The 20-platform step is a change set that was never
executed** — an empty one is exactly the fact that the ECR repository is already there, so the
step costs one `create-change-set` and prints why it stopped; the `Add EcrEngineTools` /
`Modify CpTaskRole` pair from the first run does not come back. And **the copy decision is a
line, not a silence**: "already in ECR" is what makes the difference between a skipped copy and
a copy that was never attempted readable afterwards, which is the whole failure mode this
section exists for. `dev-deploy.sh`'s own step 5b says the same thing one level up, in the form
that also covers the bake ("`engine-tools/` is unchanged").

**The one step you still run by hand is the bake**, and only when GHCR has not got the tag:
nothing on a release route may produce an image, so `update.sh` and `release-ecr.sh` stop there
and say to run `engine-tools-image.yml` with that tag first. On a standard release GHCR has it
already — the workflow is dispatched when `deploy/aws/ecs/engine-tools/` changes. Of the three
steps that were run by hand on 2026-09-11 (ADR 0072, "#518 and #512, confirmed on hardware"),
that `gh workflow run engine-tools-image.yml -f tag=<tag>` is the only one left; `update.sh`
does the 20-platform deploy and the `crane copy` itself, in that order.

**On a development deployment `dev-deploy.sh` does the middle two for you.** It bakes a
per-commit tag when `deploy/aws/ecs/engine-tools/` has changed since the deployed commit, and
carries the tag the stack asks for into ECR when that one is simply missing — `standup.sh` was
the only thing copying this image, and a dev deploy does not go through it (found by the
hardware lane before a deployment, 2026-09-11). 🔴 It still cannot set `EngineToolsImageTag`:
it runs `update.sh` on the ingress stack alone. So it prints the tag to set, and the reason —
a deployment that looks current while running the previous release's scripts is exactly what
the contract number cannot catch, because a behaviour change under an unchanged interface is
not a contract change. `deploy/local/dev-deploy-stub-test.sh` pins those decisions.

## Editing this template

It is at the 51,200-byte wall, and a YAML comment costs exactly what a `Description:` does — so
prose lives in this file and the template keeps a line or two per parameter. Two rules that have
each cost a stand-up:

- **Write every parameter in BLOCK form.** The one-line `{ Type: …, Default: … }` map reads as
  REQUIRED to `standup.sh`'s preflight — it looks for a `Default:` on its own line — and one of
  those stops every stand-up with "required parameter … is missing from params/60-engines"
  (measured against the stub test).
- **Move prose out BEFORE adding anything**, and check the size afterwards:
  `wc -c deploy/aws/ecs/cfn/60-engines.yaml`. `deploy/local/ecs-lifecycle-stub-test.sh` case 3b-2
  fails the moment a shipped template crosses the line.
- **Prove the pass changed only prose**: `deploy/local/cfn-equiv.py 60-engines.yaml` compares the
  working tree against `HEAD` with the descriptions dropped and everything else -- including a
  resource's own `Description` -- compared; `--self-test` re-runs its seven positive controls.

### Where the remaining bytes are (2026-09-11, after the move)

Two passes. The first took the PROSE out (49,298 -> 41,133: comments 8,643 -> 2,073,
`Description` 6,115 -> 4,515, every block scalar byte-identical). The second took the SHELL out
into [the engine tools image](#the-engine-tools-image), against option B (an object in the models
bucket that the start path would execute — a security boundary, not a size question) and option C
(an SSM parameter — 4 KB Standard, and the fetch script alone was 6.3 KB by then).

The template was 45,792 bytes when the second pass started, and is 39,592 after it: **6,200
bytes back, 11,608 of headroom**. The shell that is left is the three idle wrappers, and they are
not movable — see the section above for why.

| Block | Bytes | Moved? |
|---|---|---|
| `Mappings.Engine.fetch.script` (the fetch sidecar) | 6,308 | yes |
| ingest `fetch` / `upload` commands | 979 / 523 | yes |
| llm idle wrapper | 383 | no — third-party image |
| image idle wrapper, `comfy` / `sdcpp` | 456 / 479 | no — ditto (and one of three is not worth splitting) |
| engine-table JSON fragments (not shell) | ~1,078 | no |

What the saving is smaller than the shell removed: the template gained a parameter, four `!Sub`
image references and four `ENGINE_TOOLS_CONTRACT` lines. That is the price of the binding, and it
is the part that must not be optimised away.

🔴 **None of it is believable without a real GPU.** The fetch sidecar and the idle wrapper are
precisely the two places ADR 0072 P2 broke on hardware after 13/13 bench scenarios passed, and
this move changes exactly how the first of them is delivered. `engine-sidecar-test.sh` was
pointed at the file BEFORE the script moved, so it still runs the thing the image bakes; what it
cannot see is the image, the pull, the entry point and the contract gate meeting a real task.

## The task roles

**Engine tasks READ the model catalogue; the ingest task WRITES it and holds the Hugging Face
token.** (A third, `EngineInstanceRole`, belongs to the BOX rather than to a task —
[the engine boxes](#the-engine-boxes).) Two task roles on purpose: the credential that can overwrite a model file must not sit on a
instance running a network service, and the operator's HF token must not either.

⚠️ **No `ssmmessages:*` on either.** The measurement harness has it — the only way to reach a
private-subnet engine from a laptop — and a production engine is not something anyone should be
able to open a shell into.

The engine role's `ssm:GetParameter` covers exactly the two parameters this stack's own engines
read (`<EnginesSsmParamName>` and `.../<key>/active`), granted INSIDE this stack: ADR 0071
decision 8's rule that a new permission never leaves the stack that needs it. The Control Plane
side needs nothing new — its `ssm:PutParameter` is already scoped to `/af-ws/*` (20-platform,
Sid `SsmWorkspaceParams`).

## The models bucket

S3 at $0.025/GB-month, read for free through 00-network's gateway endpoint (the fetch never
touches the NAT). **Versioning is off deliberately**: a 17 GB model kept in duplicate for every
re-ingest is a bill nobody meant to sign, and the sha256 identifies a file (ADR 0071 decision 10).
The layout is ComfyUI's, so all three engines read one tree (ADR 0072 decision 2).

Both launch templates are created even when the role is off and nothing is staged: a launch
template costs nothing while nothing launches from it, and an Output that is missing is an empty
export the stack refuses to create at all ([the engine table](#the-engine-table)).

## The engine security group

**Reachability IS the access control** (ADR 0071 decision 4): port 8080, from the Control
Plane's security group and from nothing else. sd-server has no authentication of its own, so
for the image role this is the whole of it; the llm role adds `--api-key` on top
([`LlmApiKeySsmParam`](#llmapikeyssmparam)).

## The engine boxes

**The Control Plane buys the box itself** (ADR 0077): one `CreateFleet(type=instant,
TotalTargetCapacity=1)` per offer, against **one launch template per role**. There are no
capacity providers in this stack any more — the three that stood here (`af-<stack>-llm`,
`-image`, `-image-spot`), the `InfraRole`, the Managed Instances instance role and the three
`*CapacityProviderName` outputs are all gone. What this template owns now:

| Resource | What it is |
| --- | --- |
| `LlmLaunchTemplate` / `ImageLaunchTemplate` | `af-<stack>-engine-llm` / `-engine-image`: AMI, instance profile, `EngineSg`, root volume, and the user data that joins the cluster |
| `EngineInstanceRole` / `EngineInstanceProfile` | `af-<stack>-engine`: `AmazonEC2ContainerServiceforEC2Role` + `AmazonSSMManagedInstanceCore`, the slot role's pair |
| `LlmLaunchTemplateId` / `ImageLaunchTemplateId` | the outputs, and the `launchTemplate` field of the engine table row |

**The two roles still never share a box.** The reason is unchanged (CUDA does not slow down when
VRAM runs out, it crashes; 20.9 GB for the 30B against 7.4 GB for SDXL, ADR 0071 decision 2) and
so is the mechanism's effect — what enforces it is now the service's placement constraint rather
than a provider per role.

### How a box is marked, and why that is three separate things

- **`ECS_INSTANCE_ATTRIBUTES={"af-role":"engine-<role>"}`**, written into `/etc/ecs/ecs.config`
  by the launch template's user data. The service's
  `PlacementConstraints: memberOf(attribute:af-role == engine-<role>)` binds to it, so the engine
  task lands on that box and on no other.
- **EC2 tags**, written by the Control Plane at `CreateFleet` (the template's only tag is
  `af-managed-by`, exactly as `40-ec2-pool.yaml` leaves the slot tags to `RunInstances`):
  `af-pool=<cluster>`, `af-role=engine-<role>`, `af-engine-offer=<offer id>`,
  `af-engine-buy=spot|od`. The panel reads the last two — which offer the box came from is a fact
  about the BOX now, not about the service's strategy (ADR 0077 decision 8).
- **The slot pool's own test**, `isPoolContainerInstance`, which gained "the `af-role` attribute
  is absent or `slot`" alongside the capacity-provider test it already had. Both are needed
  while any deployment still has a Managed Instances box standing.

✅ **A box the CP bought is enumerable.** `describe-instances` filtered by tag returns it — the
reverse of the Managed Instances era, where instances ran in an AWS-managed account and
`describe-instances --filters Name=instance-type,Values=g6.*` answered `[]` while the quota was
fully spent. That is what lets `teardown.sh` terminate engine boxes by tag, and the Control
Plane's sweep find a box no ledger remembers (ADR 0045 decision 29's rule, ADR 0077 decision 5).

### The AMI is a parameter NAME, resolved at launch

```
ImageId: resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id
```

🔴 **That is EC2's `resolve:ssm:`, not CloudFormation's `{{resolve:ssm:…}}` and not an AMI id.**
The string travels to EC2 untouched and is resolved **when the instance launches**, so a box
always gets the current ECS-optimized GPU AMI without a stack update — unlike the slots, whose
`SlotAmiId` is an `AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>` resolved at stack update
(ADR 0045 decision 7). Instant fleets are the only launch path that supports the form. Which of
the two is right for engines is ADR 0077 open question 7; nothing is baked either way, because
ADR 0045 decision 19 measured a home-baked AMI as slow (144 s -> 192 s for a new user's start).

⚠️ **On 2026-09-12 that parameter resolved in ap-northeast-1 to
`al2023-ami-ecs-gpu-hvm-2023.0.20260901-kernel-6.1-x86_64-ebs`** (ECS agent 1.106.2, Docker
25.0.16). It is **not Bottlerocket** — which is what makes a named `SourcePath` host volume
possible at all ([the model volume](#the-model-volume)).

### Migrating a deployment that is on Managed Instances

🔴 **Do it with not one box standing**: both roles at `mode: off` in the Console, and no engine
box among the cluster's container instances. Deleting a capacity provider goes through cleanly
when nothing runs on it (measured on ADR 0074's throwaway stack: `delete-capacity-provider` at
zero boxes is `INACTIVE` at once), and the service is about to be rewritten under it.

🔴 **Then the `<Role>Enabled` round trip, and it is the only path.** ADR 0077 open question 1
measured it on 2026-09-12: CloudFormation treats `CapacityProviderStrategy` -> `LaunchType: EC2`
as a **replacement** (the change set says `Replacement: Conditional` and `LaunchType`
`RequiresRecreation: Conditionally` — which is exactly why 0074's lesson is to EXECUTE it), it
creates before it deletes, and the explicit `ServiceName` collides:

```
Resource handler returned message: "Resource of type 'AWS::ECS::Service' with identifier
'af-<stack>-llm' already exists." (HandlerErrorCode: AlreadyExists)
```

The update fails and rolls back. ✅ The rollback is clean — the live service was measured
byte-identical afterwards, same PRIMARY deployment id — so attempting it the wrong way round
costs a few minutes and nothing else. The path that works, per role:

```
<Role>Enabled=false   # the condition DELETES the service (update.sh warns about exactly this)
cloudformation deploy ...          # this template
<Role>Enabled=true    # the service comes back, on the EC2 launch type
```

Measured at **25 s** for the delete and **48 s** for the create, on a throwaway stack whose
service carried an explicit name and a `ServiceRegistries` entry like the real ones.

✅ **The Cloud Map name does NOT go away in between.** The `AWS::ServiceDiscovery::Service` is a
separate resource with no condition on it, and the round trip was measured keeping the same
registry ARN. What stops for those seconds is the ECS service registering instances into that
name — and at desired 0 there are none, so there is nothing to lose. (ADR 0077 decision 11 says
"the Cloud Map name is gone in between"; that is the one sentence P0 corrected.)

⚠️ **Whatever the answer, the capture drops eight parameters** (`<Role>AllowedInstanceTypes`,
`AcceleratorMemMinMiB`, `VCpuMin`, `VCpuMax`, `MemMinMiB`, `MemMaxMiB`, `UseLocalStorage`,
`ScaleInAfter`). `standup.sh` does it for you with `af_param_drop`; a hand-run
`cloudformation deploy` needs them removed from `params/60-engines`, because the CLI refuses a
key the template does not declare. **`<Role>StorageGiB` is not one of them** — it stays, and its
value now sizes the box's root volume.

⚠️ **`Associations` stays, holding `[FARGATE, FARGATE_SPOT]`.** The cluster's provider list is
owned by this stack and the API REPLACES it rather than adding to it, so dropping the resource
would take `FARGATE_SPOT` away from 50-tts, which reaches it through its own strategy and
declares no associations of its own (ADR 0070). `DefaultCapacityProviderStrategy` stays
deliberately EMPTY: with a default strategy in place, a service that does not spell out its
launch type lands wherever the default points (ADR 0070 decision 1 — one missing line is the
whole of that failure). Both engine services now spell out `LaunchType: EC2`.

### Before declaring a `spot` offer, check these three

**Declaring one is not done when the stack update succeeds** — it is done when a box has been
bought on Spot. Two of the three checks below are unchanged by ADR 0077 (they are facts about
EC2, not about who asked); the first gains a second role.

1. **Two service-linked roles should exist.** `AWSServiceRoleForEC2Spot` for Spot (`iam get-role`
   answers `NoSuchEntity` in an account that has never launched one), and
   **`AWSServiceRoleForEC2Fleet`** for `CreateFleet`. ⚠️ **"Without it the first call fails" is
   not what was measured**: ADR 0077's P0 run put three `CreateFleet` calls through af-sandbox
   before the role existed — though none of them launched an instance, and the Spot SLR was
   already there, so a *launching* call on an account with neither is still unmeasured. Treat it
   as cheap insurance. `standup.sh` creates the fleet one with the ECS one
   (`create-service-linked-role --aws-service-name ec2fleet.amazonaws.com`, idempotent, `|| true`
   — and the `|| true` is mandatory because a second create answers **`InvalidInput`**, not
   `EntityAlreadyExists`); the Spot one is still a manual `aws iam create-service-linked-role
   --aws-service-name spot.amazonaws.com`. Not CloudFormation resources:
   `AWS::IAM::ServiceLinkedRole` fails the stack when the role is already there.
2. 🔴 **Ask `get-spot-placement-scores` WITH THE ROW'S OWN TYPE SET.** One type scored **1/10**
   in both AZs; the same three types scored **9/10**, and an instance arrived 42 seconds after a
   9 was recorded (measured 2026-09-11). A score for one type says nothing about a request that
   names three. Read the *difference* a type set makes rather than the absolute number.
3. 🔴 **Check `L-3819A6DF`** ("All G and VT Spot Instance Requests", default **0**) — see
   [the G-family quota](#the-g-family-quota). Measured Spot prices in Tokyo were g6.xlarge
   $0.563-0.577, g5.xlarge $0.74-0.79, g6e.xlarge $1.35, so g6 stays the intended pick.
   ⚠️ g6e's Spot is ABOVE g6's on-demand $1.26: Spot is cheap for the type you got, not for
   everything you widened to.

✅ **Declaring an offer needs no Control Plane restart**, as long as the CP is 0.20.0 or newer:
the offers ride the engine table and the running CP takes them live on the same poll
(`engine_table_reload.go`). What is no longer a pair with a `force-new-deployment` is a provider
rename — there are no provider names left to rename, which removes the whole class of failure
where the CP watched a provider nobody used and the panel reported `box: null` for a box that was
billing (measured 2026-09-11).

⚠️ **`describe-instances` proves Spot directly now**: `InstanceLifecycle: spot` on an instance
this account owns and can enumerate by tag. The Managed Instances detour — filter returns `[]`,
ask by the `ec2InstanceId` that `describe-container-instances` reports — is no longer needed.

## The engine services

⚠️ **The engine image must ALREADY be in its ECR repository when this stack is created.**
CloudFormation blocks on ECS service stabilisation, so a service that cannot pull leaves the
stack in `CREATE_IN_PROGRESS` indefinitely. That is why the repositories live in 20-platform and
`standup.sh` copies the images in during its images step, one stack earlier than this one
(measured on 50-tts, 2026-09-06). The image role's single `engine` container (named that rather
than `sd` since ADR 0072 P2, now that it can be either binary) pulls from `af-sdcpp` or
`af-comfyui` depending on `ImageEngine`, and `standup.sh` only copies the ONE it needs.

⚠️ **`DesiredCount` is deliberately ABSENT, and this is load-bearing.** From the resource
schema: for a NEW service an unspecified desired count defaults to 1; for an EXISTING one it is
omitted from the update call. So CloudFormation creates the service running and then never
touches the count again — which is what lets the engine be started and stopped out from under
the template. Declaring `0` instead would reset the count on every task-definition or tag
change, i.e. kill a GPU instance mid-answer. `standup.sh` scales the new service to 0 straight after
creation.

⚠️ **`LaunchType: EC2` IS spelled out** (ADR 0077 decision 2). These were the one pair of
services in the deployment allowed to omit it, because a capacity provider strategy and a launch
type are mutually exclusive and they named a provider instead; with the providers gone, ADR 0070
decision 1's invariant — the placement is always declared, never defaulted — is satisfied the
ordinary way. **There is no `CapacityProviderStrategy` left for a release to put back**, which
is the whole of ADR 0075 decision 12's problem gone.

⚠️ **The placement constraint is on the SERVICE, not on the task definition.** A Workspace pins
a task to a box with `memberOf(ec2InstanceId == ...)` on the task definition, because "this user
on this box" is the point there. An engine changes boxes, and a task-definition revision per
start is what that shape would cost — `memberOf(attribute:af-role == engine-<role>)` on the
service never changes.

**`DeploymentConfiguration` is `MinimumHealthyPercent: 0` / `MaximumPercent: 100`** so a
redeploy stops the old task BEFORE starting the new one. One engine has one DNS name, and two
tasks behind it during a rollout split requests between a warm engine and a cold one -- the
caller sees a random 500-second first token. Stopping first costs a gap; overlapping costs a
lie about which engine answered.

**`HealthCheckCustomConfig: { FailureThreshold: 1 }` on the Cloud Map service is not optional**:
Cloud Map refuses to register an ECS service that has no load balancer without it. There is
nothing to tune -- ECS is the only thing that ever reports the instance's health here.

## The G-family quota

Two roles running means two instances, i.e. 8 vCPU of the G-family quota at once — and an instance
that was just stopped holds its 4 vCPU for the 7–8 minutes it spends draining, so waking one
role while the other is on its way out needs 12. A deployment that uses both roles should hold
16 or more (quota `L-DB2E81BA`, which is a support case and not auto-approved).

🔴 **On-demand and Spot are SEPARATE quotas**, and which side an offer buys on does not change
the arithmetic above: a role runs one box, on one side or the other.

| What is bought | Quota | Default | Held by |
| --- | --- | --- | --- |
| on demand | `L-DB2E81BA` "Running On-Demand G and VT instances" | 8 in a fresh account | acrt 64, af-sandbox 8 |
| Spot | `L-3819A6DF` "All G and VT Spot Instance Requests" | **0** | acrt 64, af-sandbox 8 |

⚠️ **A deployment that declares a `spot` offer must check `L-3819A6DF` first.** The stack
deploys perfectly happily with it at 0 and the offer then simply never buys anything (measured,
ADR 0074) — which reads as an engine that will not start. Since ADR 0077 the refusal at least
arrives as an error code in the purchase's own response rather than as a service event nobody
was looking at. `L-DB2E81BB` does not exist; do not look for it.

```
aws service-quotas get-service-quota --service-code ec2 --quota-code L-3819A6DF
```

✅ The separation is also what makes the fall-through real rather than cosmetic: on-demand vCPU
held by a box that is still shutting down (measured at over five minutes of `VcpuLimitExceeded`
after ECS deregistered it, ADR 0074 P1) does not touch the Spot side, and the other way round.

## Ingest

### The Hugging Face token

`HfTokenSecret` is a Secrets Manager secret this stack always creates, read by the INGEST task
only (ADR 0071 decision 3: the token is the operator's, and it never lands on an engine instance).
Without a token only ungated repositories can be ingested, which covers every model in ADR
0072's table except FLUX.1-dev and SD 3.5.

**There is no parameter to set.** The token is registered in the Console (Settings → engines)
and the Control Plane writes it into this secret — ADR 0072 decision 6 as revised: the DB is
the record of truth and Secrets Manager is the carrying path, because `secrets[].valueFrom` is
the only way ECS hands a value to a container without putting it in `environment`, which
`DescribeTasks` returns in clear to anyone holding `ecs:DescribeTasks` (measured 2026-09-09).

**Why the secret exists even when no token does.** A task definition is CloudFormation's and
static, so a `Secrets` block that appeared with the token would put the CloudFormation round
trip back exactly where registering from the Console was meant to remove it. The secret is
created holding the sentinel `-`, the block is always there, and the fetch container reads `-`
as "no token". A stack deleted with a token in it leaves the secret in Secrets Manager's
recovery window; it is unnamed, so a re-created stack makes a new one rather than colliding.

🔴 **The secret is resolved by the EXECUTION role, not the task role.** ECS resolves a
container's `Secrets` before the container exists — and 20-platform scopes that role's
`secretsmanager:GetSecretValue` to `secret:rds!*`, the database password. Without the matching
grant the task dies at startup with `ResourceInitializationError: unable to pull secrets` — not
a 401 on the download, and nothing in the ingest log, because no container ever ran.
`ExecHfTokenPolicy` closes it, scoped to this one secret and attached to the imported exec role
by name the same way `CpIngestPolicy` attaches to the CP's.

The secret's value is the token and nothing else — no `{"HF_TOKEN":"…"}` wrapper and no trailing
newline, since `ValueFrom` with no JSON key passes the whole string through as the environment
variable. A trailing newline travels into the `Authorization: Bearer` header and earns a 401
that reads exactly like an unaccepted licence. (The CP trims what it is given for that reason.)

🔴 **A gated repository refuses with 401 OR 403, and the two send you to different places.**
Measured on the dev deployment 2026-09-10 (ADR 0072, "P5 on hardware"): with a token registered,
FLUX.1-dev went through while `stabilityai/stable-diffusion-3.5-medium` failed on the single log
line `curl: (22) The requested URL returned error: 403`. **401 means the token did not arrive**
— none registered, or the trailing newline above. **403 means it did**: the request authenticated
and that account simply has no access to that repository, so the fix is on Hugging Face — accept
the model's terms with the operator's account (and, for a fine-grained token, grant it read
access to the contents of public gated repos). Accepting the terms and retrying the same file
passed.

### The ingest containers

Two containers, `fetch` then `upload`, sharing a `scratch` volume.

🔴 **`fetch` runs as `User: "0"`.** The shared volume is root-owned and `curlimages/curl` runs
as uid 100, so without it curl cannot write the blob -- and it says so as
`curl: (23) client returned ERROR on write`, which reads like a network fault and is a
permission one (measured).

**`upload` depends on `fetch` with `Condition: SUCCESS`, not `COMPLETE`.** A download that
failed its sha256 must not reach the bucket; `COMPLETE` would upload it and the catalogue would
carry a row for a truncated file. (The ENGINE containers use `START` instead, for the opposite
reason -- see [The fetch sidecar](#the-fetch-sidecar).)

### `IngestCpu` / `IngestMemory` / `IngestDiskGiB`

Fargate sizing for the ingest task. It stages the whole file on disk before uploading, so the
disk is the largest model the deployment can take in — 80 GiB covers the 22 GB FLUX checkpoint
with room for the filesystem.

### The ingest permissions

The Control Plane's ONLY new IAM in this repository (ADR 0072 decision 6), and it lives in this
stack so that a deployment which does not adopt 60-engines gains nothing:

| Action | Scope | Why |
|---|---|---|
| `ecs:RunTask` | this stack's ingest family, on this cluster | starting the fetch |
| `iam:PassRole` | `IngestTaskRole` only | a task cannot be started without passing its role |
| `logs:GetLogEvents` / `DescribeLogStreams` | this stack's log group | WHY a job failed |
| `secretsmanager:PutSecretValue` | `HfTokenSecret` only | carrying a registered token to the ingest task |
| `ec2:CreateFleet` / `DescribeFleets` / `DeleteFleets` | `*` | buying the engine box (ADR 0077 decision 10). A fleet has no ARN to scope to; the fence is the launch template the call may name and the `iam:PassRole` below |
| `iam:PassRole` | `EngineInstanceRole` only, `PassedToService: ec2.amazonaws.com` | the launch template carries the instance profile, so the purchase passes that role — the shape of 20-platform's `PassSlotRole` |
| `iam:CreateServiceLinkedRole` | `iam:AWSServiceName` in `[spot.amazonaws.com, ec2fleet.amazonaws.com]` | the CP's own way out on an account where `standup.sh` never ran |

**What left this policy in ADR 0077**: `ecs:DescribeCapacityProviders`, `ecs:UpdateCapacityProvider`,
the cluster-scoped `ecs:PutClusterCapacityProviders`, and `iam:PassRole` on the Managed Instances
`InfraRole` / `InstanceRole`. `ec2:RunInstances` / `TerminateInstances` / `DescribeInstances` /
`CreateTags` and the three container-instance actions are **not repeated here**: 20-platform
grants them unconditionally, on every flavour (Sids `Ec2SlotPool` and `EcsContainerInstances`).

🔴 **`ssm:GetParameters` on `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/*` is
required of the CALLER**, and it is in the policy unconditionally. Measured 2026-09-12 (ADR 0077
P0, open question 3): without it `CreateFleet` fails **top-level** with `SsmAccessDenied`
("Access denied to SSM"), naming neither the action nor the parameter. The launch template's
`ImageId` is resolved by whoever calls, not by EC2 on its own behalf. The CP's other
`ssm:GetParameter` is scoped to `/af-ws/*` and cannot read a public parameter; note the ARN has
**no account id** in it.

✅ **Two grants that are NOT needed and should not be added** (same run): `ec2:DescribeLaunchTemplates`
/ `DescribeLaunchTemplateVersions` — `CreateFleet` resolved the template by name with neither —
and `ec2:CreateTags`, because the request's own `TagSpecifications` **merge** with the launch
template's: the measured box came up with `af-pool` / `af-role` / `af-engine-offer` /
`af-engine-buy` from the call and `af-managed-by` from the template. The slot pool needs
`CreateTags` because it tags after `RunInstances`; this path does not.

🔴 **`iam:PassRole` is checked only on a call that would otherwise launch.** With it removed, a
`CreateFleet` that had nothing to buy still answered HTTP 200 with per-override capacity errors;
the one that could buy answered 200 with `ErrorCode: UnauthorizedOperation` **inside `Errors[]`**.
So an IAM hole is shaped exactly like "no capacity", and `--dry-run` does not catch it
(`DryRunOperation`, "would have succeeded"). That is the Control Plane's problem to sort — ADR
0077's P0 follow-up hands it to decision 8's failure-code table — but it is also why this policy
is not something to trim by experiment.

**`s3:DeleteObject` is on the INGEST task role, and on nothing else.** `MODE=delete` (ADR 0072
decision 7) is how bytes leave the bucket, and the Control Plane does not hold the permission:
forgetting a catalogue row and deleting the file it points at are different acts by different
principals, which is what lets the Console offer them as two separate presses.

**`GetSecretValue` is deliberately absent.** ADR 0072 decision 6 used to say "the CP never
holds the token"; once the token is registered through the Console that weakens to "the CP
cannot read back what it wrote", and this is what makes that auditable rather than a promise.
It also means the CP has no way to show a registered token, or to check the secret against the
DB — so it writes the value again before every ingest that needs it.

The reads it already had (`DescribeTasks`, `ListTasks`) are what turn a running task into a
finished one. The execution role's PassRole was already granted in 20-platform, so it is not
repeated here.

**Why the log grant is worth an IAM statement.** `DescribeTasks` says a container exited 1.
The log says `ingest: sha256 mismatch: got … want …` or a 401 on a gated repository, and those
two need completely different things from the person reading them — one is "the file changed
upstream", the other is "this deployment has no token". The panel shows that line verbatim.

⚠️ **The Control Plane still never touches S3, and still never needs the token to resolve.**
Measured 2026-09-09: a gated repository answers `api/models/<repo>?blobs=true` ANONYMOUSLY with
its licence, its gating flag and every file's sha256 and size — only the download is 401. So the
CP resolves, and the task (which has the token) fetches. What changed with the Console
registration is the token's storage, not this path.

### Running the ingest task by hand

```
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition af-<stack>-ingest \
  --network-configuration 'awsvpcConfiguration={subnets=[...],securityGroups=[...],assignPublicIp=DISABLED}' \
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[{"name":"URL","value":"https://huggingface.co/…/resolve/main/x.gguf"},
                                    {"name":"SHA256","value":"<siblings[].lfs.sha256 from the HF API>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/x.safetensors"}]}]}'
```

Since ADR 0072 phase P4 the Console does all of that — **Settings → Admin → engines → "take one
in from Hugging Face"** — and the hand-written form above stays for a deployment whose Control
Plane cannot reach the internet, or one that has not adopted the ingest permissions below.

⚠️ **The containers no longer have an `EntryPoint` of `["sh","-c"]`.** Since the scripts moved
into the engine tools image, `Command` is the executable itself (`/opt/af/ingest-upload.sh`), so
a `command` override here is an ARGV ARRAY and not one string. The trap the old shape carried —
passing `["sh","-c",<script>]` and getting `sh -c sh -c <script>`, which does nothing and exits 0,
i.e. reads as success and ran nothing (measured) — is gone with it. The Control Plane never
overrode a command in the first place; it overrides `environment` only, which is unchanged.

**The sha256 is not optional decoration.** "The file got bigger" is what a truncated download
also looks like, and a GGUF that is 99 % there loads and then answers nonsense. The value comes
from the HF API's `?blobs=true` `siblings[].lfs.sha256`, which was verified against a real
`sha256sum` of the downloaded file.

**Where the file goes** is the ComfyUI layout (ADR 0071 decision 6, ADR 0072 decision 2):
`llm/<name>.gguf`, `llm/loras/`, `image/checkpoints/`, `image/loras/`, `image/vae/`,
`image/text_encoders/`, `image/diffusion_models/`. All three engines read the same tree, so a
checkpoint ingested for sd-server is already where ComfyUI would look for it.

## The model volume

Both engine task definitions mount an ANONYMOUS host volume — an empty `Host`, no `SourcePath`.
On an ordinary EC2 box that is **a Docker anonymous volume**: ECS hands the empty `host` volume
to Docker, and Docker puts it under its own data-root (`<data-root>/volumes/<id>/_data`). Three
things follow, and the third is the one that bites:

- **the models land wherever Docker's data-root is**, which is what lets the launch template's
  user data move them to the instance store without the task definition knowing anything about
  it (below). Nothing in `60-engines.yaml`'s task definitions changes for it.
- **on a type with no instance store they are on the root gp3 volume**, sized by
  [`<Role>StorageGiB`](#llmstoragegib). The ECS-optimized AMI's own default root is 30 GiB; this
  template asks for 120 (llm) and 60 (image) instead. A `comfy` deployment holding 30 GB of
  checkpoints needs the image role's raised.
- **a fresh directory per task, still.** An anonymous volume is created per container, so a
  second start on the same box re-fetches everything and the previous copies are not reclaimed
  until the image/volume cleanup runs.

### The instance store, mounted from user data

🔴 **This is ADR 0077 open question 8, brought forward into P1 because leaving it open is a
measured regression** (the retired `<Role>UseLocalStorage` was worth roughly half a cold start —
[`LlmStorageGiB`](#llmstoragegib)). What the launch template's user data does, per role:

```
DEV=$(lsblk -dno NAME,MODEL | awk '/Instance Storage/ {print "/dev/"$1; exit}')
  -> stop docker, mkfs.xfs, copy /var/lib/docker across, mount the NVMe on /var/lib/docker,
     start docker
```

- **Only the first instance-store device is used.** A type with several (g6.12xlarge) leaves the
  rest unmounted; a RAID0 would be the next step and nobody has needed it.
- **An EBS-only type matches nothing and is left alone** — `g6e` sizes have no instance store, so
  they keep the root volume and the paragraph above applies.
- **The copy is not decoration.** The ECS AMI ships cached agent and pause images inside
  `/var/lib/docker`; mounting an empty filesystem over them without copying first would make the
  agent reload them, and `awsvpc` needs the pause image. Every step is chained with `&&` and
  `docker` is started either way, so a failure anywhere leaves Docker on the root volume rather
  than leaving the box without a container runtime.
- **The order works because the ECS agent has not started yet.** User data runs from cloud-init,
  and the AMI's `ecs.service` is ordered `After=cloud-final.service` — the same fact that makes
  writing `/etc/ecs/ecs.config` in user data work at all. Docker, which starts earlier, is
  stopped and restarted explicitly.

⚠️ **Unverified on hardware.** Nothing in this repository has run a GPU box since ADR 0077, so
the first P1 run must check, on the box: `df /var/lib/docker` (the NVMe, not `/dev/nvme0n1p*`),
`docker info | grep "Docker Root Dir"`, and that the engine's first fetch writes at NVMe speed
rather than 125 MB/s. **If it did not mount, the symptom is a slow start and nothing else** —
which is exactly the shape that goes unnoticed, so look rather than assume.

**Why it is still anonymous** (ADR 0077 decision 6): a named `SourcePath` now WORKS — the AL2023
GPU AMI is not Bottlerocket — but the box goes when the engine goes idle (decision 5) or when
Spot takes it (decision 4), so the only case a warm directory would serve is desired 0 -> 1
inside the idle window, which is the case ADR 0071 decision 5 already decided not to stop in.
P1 measures what it would save against the measured re-fetch (S3 -> local at 104-147 MB/s,
6.94 GB in 65 s); if it is a minute, it is not worth having.

### What the Managed Instances era proved

Four
measurements from 2026-09-09, about four minutes of GPU, are why `<Role>ScaleInAfter: -1` was
never the answer there:

- a named `SourcePath` DID persist across two tasks on the same instance (run 1 MISS, run 2 HIT,
  same mtime) — the half everyone assumed was hard;
- **but it landed on 3.1 GB.** `/dev/nvme0n1p8`, a small partition of the ROOT volume, not the
  245 GB data volume the anonymous volumes lived on. An 18.5 GB model reproduced
  `No space left on device` exactly;
- **and the data volume had no nameable path**: the AMI was Bottlerocket, so every top-level
  directory a `SourcePath` could name resolved inside a 2.7 GB read-only dm-verity image, and
  `/._mnt_task` could not even be created. `useLocalStorage` changed what the data volume IS,
  not where a `SourcePath` may point.

So on Managed Instances a warm model volume could not be built out of host volumes at all. On an
EC2 box it can — the root volume is one filesystem and `/var/lib/af-engine-models` is a real
path — which is why ADR 0077 reopens the question instead of inheriting the verdict. The harness
is `harness/probe-warm-volume.sh` (Managed Instances-era, gated: see its header).

## The engine table

**Cloud Map DNS, not Service Connect.** The CP reaches an engine at
`http://<name>.<namespace>:8080` through the VPC resolver, so the gateway needs no sidecar and no
`/etc/hosts` snapshot (ADR 0070 decision 8). At desired 0 the name has no A record at all — the
normal state of an engine nobody is using, not an error.

**Why the table is one SSM parameter.** 30-ingress has about 9.2 KB of template budget left and
six parameters per engine would eat a third of it, so the CP is handed ONE parameter name and
reads the table from SSM at startup. The name has to be under `/af-ws/` — that is the whole of
the CP task role's SSM read scope.

Not a parameter but the stack's real output: the one SSM value 30-ingress is handed, holding 0, 1
or 2 rows (each role is staged independently), which the Control Plane reads once at startup.

**`health` and `provider` on the image row follow `ImageEngine`** (ADR 0072 decision 4): `comfy`
writes `/system_stats` and `comfy`, `sdcpp` writes `/v1/models` and `sdcpp`. Nothing else in the
row changes — `url`, `launchTemplate` and `service` are the same regardless, because it is
still one role, one service.

**Three fields carry how the box is bought** (the contract between this template and the
Control Plane):

| Field | Value | Empty means |
| --- | --- | --- |
| `launchTemplate` | `!Ref <Role>LaunchTemplate`, i.e. the `lt-...` id | the role cannot buy a box at all |
| `offers` | `<Role>Offers` verbatim | read `classes` as the offer list, every row on demand |
| `offerBudgetSec` | `<Role>OfferBudgetSec` | — (a number, never empty) |

🔴 **`launchTemplate` replaced the `capacityProvider` / `spotCapacityProvider` pair** in ADR 0077,
and it is the seam between this template and the Control Plane: a CP that predates the change
looks for the two old names, finds neither, and has nothing to buy through. **Deploy the Control
Plane first.** Written as a `!Ref` and never restated as a `!Sub` string, for the reason the pair
was: a table naming a template that does not exist fails at the first purchase and nowhere else.

The pair's own trap is worth keeping in mind, because it is what this shape removes: two names
travelled together, the box was behind whichever the service's strategy currently named, and a
watch against one of them reported `box: null` for a box that existed and was billing (measured
2026-09-11). There is one name now, and the box is found by its tags rather than by a provider.

**`api`** tells the reader what KIND of endpoint a row is, and it decides two things that would
otherwise be guessed from the key: whether the Agent writes an opencode chat provider for it — an
image engine there would put `sdcpp/sdxl-base-1.0` in the launch menu as something to hold a
conversation with — and whether the gateway counts tokens out of the response (`chat`) or leaves
the accounting to the Agent's `tool.imagegen` row (`images`, ADR 0071 decision 9).

**The image row's health path is `/v1/models`, not `/health`**: stable-diffusion.cpp's server has
no health endpoint at all (upstream `examples/server/api.md`), and `/v1/models` is the cheapest GET
it answers. It only listens once the checkpoint is loaded, so a 200 there really does mean ready.

**The llm row carries `warmPath: /models`**, and the image row carries none. It is where the CP
asks "are there weights in memory", which stopped being the same question as "is it healthy" the
moment the llm role became a router: measured on b10853, a router with an EMPTY model list still
answers `/health` with `{"status":"ok"}`, so a warm flag read from the health check would be true
through the whole 527-second cold start. With `warmPath` set the CP reads `status.value` out of
`/models` instead and calls the engine warm when at least one model is `loaded`. Declared rather
than derived from the provider name, for the usual reason (ADR 0053): a second engine speaking
the same API would otherwise inherit a probe nobody chose for it. `GET /models` neither triggers
an autoload nor resets the router's idle timer (upstream README), so polling it is free.

**A row says nothing about models.** `models`, `contextTokens`, `maxOutputTokens` and
`modelS3Key` were written here until ADR 0072 phase P6 retired them
([above](#upgrading-the-model-parameters-are-gone)); the Control Plane still parses a table that
carries them, because the CP is upgraded before the stack is, and ignores what they say. What is
left is the vessel (service, URL, health path, warm path, the launch template, classes, offers,
idle window, start deadline, mode), which really is the stack's to declare.

**The `ingest` object in the same value** is what the Control Plane needs to START an ingest
and to read why one failed: the task-definition family, the subnets, the security group, the log
group and the token secret. Declared rather than derived (ADR 0053) -- the CP cannot ask
CloudFormation for any of it at request time.

**The outputs are non-empty by construction.** `EnginesSsmParam` is a parameter with a non-empty
default, and the two service names fall back to `"-"` when their role is off, because
`deploy/aws/ecs/check-cfn-exports.py` fails a template that can export an empty string: an empty
`Fn::ImportValue` is a stand-up that dies one stack later with nothing pointing at the cause.

**The active set is a different parameter, and the stack does not write it.** The Control Plane
publishes `<EnginesSsmParamName>/<key>/active` whenever the catalogue changes, and the instance's
fetch sidecar reads it. Two consequences worth stating: the `EngineTaskRole` grants
`ssm:GetParameter` on `<EnginesSsmParamName>/*` (inside this stack, per ADR 0071 decision 8), and
because CloudFormation does not own those parameters, **`teardown.sh` deletes them** — otherwise
a torn-down deployment leaves `/af-ws/engines/*/active` behind for the next one to read.

## Cost allocation — every billed resource in this stack carries `af-role`

The engine hours are a COMPONENT cost: shown as their own line, never apportioned to a member
(ADR 0071 decision 9, ADR 0048 decision 15). What makes that possible is that `af-role` is already
one of the Control Plane's activated cost allocation keys, so nothing new has to be switched on —
and switching a key on is not retroactive, which is why the tags go on before the reading does.

| Resource | `af-role` | How it reaches the bill |
|---|---|---|
| the engine box | `engine-llm` / `engine-image` | the Control Plane writes `af-role` / `af-pool` at `CreateFleet`, as it does for a slot at `RunInstances` — the instance and its root volume are the billed unit |
| `LlmService` / `ImageService` | `engine-llm` / `engine-image` | `PropagateTags: SERVICE` — the task carries the role for anything billed against it |
| `ModelsBucket` | `engine-models` | bucket tags; S3 storage for the staged GGUF / checkpoints |
| `LogGroup` | `engine-logs` | log group tags; CloudWatch ingestion and storage |

Measured on af-sandbox over 2026-09-01..08 (one Cost Explorer request, grouped by `af-role` and
SERVICE): `engine-llm` $5.18, `engine-image` $0.73 — of which $0.37 and $0.05 arrived as "Amazon
Elastic Container Service" rather than EC2, i.e. the Managed Instances fee DID inherit the
service's tags (that fee is gone with ADR 0077, and the same spend now arrives as EC2), and
$0.08 as "EC2 - Other" for the data volume. None of it carries
`af-membership`, so all of it lands in the shared bucket and none of it is ever charged to a
person.

**What still cannot be split**: the ingest job pulls model weights from Hugging Face over the NAT
gateway, and NAT data processing is untaggable (ADR 0048 decision 4). Fetching the same file from
S3 afterwards does not — 00-network's gateway endpoint keeps that off the NAT entirely.

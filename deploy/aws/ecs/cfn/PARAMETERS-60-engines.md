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
- [The fetch sidecar](#the-fetch-sidecar)
- [The idle wrapper](#the-idle-wrapper)
- [Editing this template](#editing-this-template)
- [The task roles](#the-task-roles)
- [The models bucket](#the-models-bucket)
- [The capacity providers](#the-capacity-providers)
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

**Why Managed Instances and not Fargate.** Fargate has no GPU (AWS Fargate FAQ;
containers-roadmap #88, open since 2019), so ADR 0070's shape — "an ECS service whose desired
count is 0 while nobody wants it" — is bought here from ECS Managed Instances instead: AWS owns
the instance, the AMI and the NVIDIA driver, and terminates the instance once the task is gone.

All measured on a real cluster on 2026-09-07 through `deploy/aws/ecs/harness/engprobe.yaml`.

- a Managed Instances capacity provider is CLUSTER-SCOPED: `ClusterName` is mandatory, and
  without it ECS answers "The cluster provided is invalid";
- `AmazonECSInfrastructureRolePolicyForManagedInstances` passes only roles named
  `ecsInstanceRole*`, so the fleet-named instance role needs an explicit `iam:PassRole`
  (measured: `UnauthorizedOperation` otherwise, visible only in CloudTrail);
- `ClusterCapacityProviderAssociations` REPLACES the cluster's provider list and requires
  `DefaultCapacityProviderStrategy` — so the full list is named in the template and the default
  strategy is deliberately EMPTY. Put anything in it and a service that forgot its `LaunchType`
  lands on the GPU instance (ADR 0070 decision 1, ADR 0071 decision 1);
- `InstanceRequirements` refuses `InstanceGenerations` together with generation-bearing type
  names, and `AcceleratorCount` needs `AcceleratorTypes` alongside it;
- `DesiredCount` on an `ECS::Service` returns to its declared value on EVERY service update —
  see [The engine services](#the-engine-services).

⚠️ **Exactly one stack in a deployment may own the cluster's capacity-provider associations**,
because the API replaces the list rather than adding to it. That stack is this one. The
measurement harness (`harness/engprobe.yaml`) owns the same list, so take it down first:
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

### 0.18.1: the image role can be bought on Spot

New parameter, `ImageCapacityOptionType` (`ON_DEMAND` by default, so **nothing changes for a
deployment that leaves it alone**). Set it to `SPOT` and the image role's instance is bought on Spot —
in ap-northeast-1 that was measured at 30 days without one on-demand hour: `g6.xlarge` at
$0.45-0.58 against a $1.1672 list price (ADR 0074). Two things to know before setting it:

- the switch **replaces the capacity provider**, which moves the image service onto it. Do it
  while the image role is stopped, or a generation in flight is lost and the next request pays
  the cold start again. Details and the measurements:
  [the capacity providers](#the-capacity-providers);
- Spot has **its own quota**, `L-3819A6DF`, whose default is 0. The provider is created happily
  without it and then never buys an instance — an engine that will not start, with nothing to read.
  Check it first: `aws service-quotas get-service-quota --service-code ec2 --quota-code
  L-3819A6DF`.

The `llm` role is not offered this and is not going to be: a two-minute termination notice
mid-conversation costs a 527-586-second cold start to recover from.

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

### `LlmAllowedInstanceTypes`

Instance types the llm capacity provider may buy. DECLARED per role (ADR 0071 decision 2),
because the floor is a VRAM number and VRAM is not derivable from vCPU or memory:
Qwen3-Coder-30B-A3B Q4_K_M measured 20,943 MiB, which needs the L4's 22,888 and does not fit
anything smaller in the family.

Default `g6.xlarge,g5.xlarge` — two types, because one type has nowhere to go when EC2 answers
`InsufficientInstanceCapacity: We currently do not have sufficient g6.xlarge capacity in the
Availability Zone you requested`. That is a fact about a type in an AZ, and the VPC that
00-network builds has only two AZs, so the advice attached to the message ("get capacity by not
specifying an Availability Zone") has nothing left to offer: the provider is already handed
both private subnets and has already tried both.

⚠️ **The list is a filter, not an order of preference, and there is no way to make it one.**
Managed Instances has no allocation-strategy field — `aws ecs create-capacity-provider
--generate-cli-skeleton` shows `managedInstancesProvider` carrying nothing but
`instanceRequirements` and the price-protection knobs
(`onDemandMaxPricePercentageOverLowestPrice`), which is also the tell for what selection does
honour. So express the priority as price and let the intended instance be the cheapest member:
with the ADR's table (Tokyo, on-demand + MI management fee per hour) g6.xlarge is $1.258 and
g5.xlarge $1.573, and the L4 instance is what you get while it exists. **A cheaper type added here
becomes the default rather than the fallback** — g4dn.xlarge at $0.765 would win every
placement, and its T4 is half the VRAM and unmeasured for this role. A fallback belongs above
the intended instance, never below it.

Second reason the default holds to 4-vCPU types: a `g6.2xlarge` fills the whole default
8-vCPU G-family quota by itself, and then the other role cannot launch at all.

⚠️ **When that quota bites, the instances spending it are invisible where you would look for them.**
Measured 2026-09-09, chasing a `VcpuLimitExceeded` on a deployment that appeared to have no GPU
instances at all: **Managed Instances runs its instances in an AWS-managed account**, so
`aws ec2 describe-instances --filters Name=instance-type,Values=g6.*` returns **nothing** while
the quota is fully spent. The count that matters is
`aws ecs list-container-instances`/`describe-container-instances` (which does report
`ec2InstanceId` and `ecs.instance-type`). And the quota is not freed the moment a task stops:
a terminating instance holds its vCPUs for a while, so `VcpuLimitExceeded` keeps answering for
minutes after the cluster looks idle. Wait for the container instance to leave the cluster
rather than for the task to stop.

⚠️ `InstanceRequirements` refuses this alongside `InstanceGenerations`.

### `LlmAcceleratorMemMinMiB`

VRAM floor (MiB) written as an instance requirement instead of a sentence in a comment: ADR
0071 decision 2 puts this role at >= 20 GB and the 30B measured 20,943 MiB, so the default is
21,000. Running out of VRAM does not slow CUDA down, it crashes it, which is why the floor
travels with the type list — widen `LlmAllowedInstanceTypes` and this is what still refuses a
card the model cannot load. `0` = do not ask (which is also what `LlmGpuCount: 0` does).

The requirement is `AcceleratorTotalMemoryMiB`, i.e. the total across the accelerators asked
for; with `LlmGpuCount: 1` that is the one card.

### `LlmGpuCount`, `LlmVCpuMin` / `LlmVCpuMax`, `LlmMemMinMiB` / `LlmMemMaxMiB`

GPUs per instance (and the task's GPU resource requirement; 0 = a CPU instance, test only) and the bounds
of the instance requirement.

### `LlmStorageGiB`

EBS data volume per instance, when `LlmUseLocalStorage` is off. It has to hold the image layers plus
every model the role pulls from S3. MI exposes only the SIZE — throughput and IOPS cannot be
asked for (measured against the API), which is why `UseLocalStorage` exists.

### `LlmUseLocalStorage`

Use the instance store instead of an EBS data volume. **On by default since 2026-09-09, when the
measurement ADR 0071 open question 1 asked for was finally taken.** The premise held: on
g6.xlarge the EBS baseline of 125 MB/s bounded both halves of a cold start, and taking it away
roughly halves the whole thing. Measured on the deployment's own `llm` role, same instance, same two
models, against the numbers recorded for the EBS setting:

| | EBS (recorded) | instance store | |
|---|---|---|---|
| S3 → disk, 1.1 GB | 8 s (140 MB/s) | **5 s (223 MB/s)** | 1.6× |
| S3 → disk, 18.5 GB | 159 s (117 MB/s) | **117 s (159 MB/s)** | 1.4× |
| disk → VRAM, 18.5 GB | 267 s | **91 s** | **2.9×** |
| RunTask → model loaded | 527-586 s | **275 s** | ~2× |
| swap to the 1.1 GB model | 10.0-10.1 s | **3.3 s** | 3.0× |
| swap back to the 18.5 GB model | 276-282 s | **98.5 s** | **2.8×** |

Two things worth reading off that table rather than the headline. **The win is the VRAM load,
not the fetch**: taking the EBS write cap away only moved the 18.5 GB fetch 1.4×, because the
next limit — S3 and the CLI, around 160 MB/s — was right behind it. And the swap is where a user
actually feels it: ADR 0072 decision 3 priced "one model per instance, swap on demand" at 276-282
seconds, and it now costs 98.5.

The costs are unchanged and both are real: an instance store is wiped with the instance (so every
cold start still pays a fresh S3 fetch — but so did the EBS data volume, which MI also deletes),
and `LlmStorageGiB` stops meaning anything while this is on. The instance gets whatever the instance
type carries, which on g6.xlarge is 250 GB — measured as a 245 GB ext4 filesystem, i.e. MORE
than the 120 GiB the EBS setting asked for, which is why the disk ceiling on how many models can
be enabled at once went up rather than down (ADR 0072 open question 11).

⚠️ It follows that `*AllowedInstanceTypes` may only name types that HAVE an instance store. Every
g6 and g5 size does; a type without one cannot satisfy the capacity provider.

### `LlmScaleInAfter`

`infrastructureOptimization.scaleInAfter`, in seconds: how long MI leaves an idle instance before
terminating it. `-2` = do not set it, i.e. AWS's default (measured drain: 427 and 463 seconds on
a GPU instance, 93 on a CPU instance). `-1` = never tidy up. `0`-`3600` = that many seconds. It was worth
having as a knob while an instance kept a little longer might have been a warm start; since 2026-09-09
it is not, because a kept instance cannot hold its models at all — see [The model
volume](#the-model-volume), where decision 7(c) is disproven rather than merely unproven. Keeping
a GPU instance past its work now buys the image layers and nothing else, at $1.26/hour, so the AWS
default is the right setting and `-1` is a way to spend money on nothing.

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

### `ImageAllowedInstanceTypes` / `ImageAcceleratorMemMinMiB`

Instance types the image capacity provider may buy. The measured floor is 7,379 MiB of VRAM for
SDXL fp16, so g6f.2xlarge's 5,722 MiB does not fit it in that form — but `--offload-to-cpu` and
quantisation are UNMEASURED (ADR 0071 decision 2, P4), so this is a default rather than a proof
that nothing smaller works.

Default `g6.xlarge,g5.xlarge`, for the reason under `LlmAllowedInstanceTypes`: this is the role
the capacity shortfall was actually observed on, the list is a filter and not a preference
order, and the cheapest member is the one that normally gets bought.

`ImageAcceleratorMemMinMiB` defaults to 8,000 — decision 2's ">= 8 GB", above the 7,379 MiB
measurement. Note what that does NOT do: a 16 GB T4 clears it, while every image measurement
there is (SDXL 8.0 s, Z-Image 10.5 s, klein 4B 4.0 s — ADR 0072) was taken on a 24 GB L4. The
type list is what holds the role to measured hardware; the floor only keeps a card too small to
load the checkpoint out.

### `ImageStorageGiB`

EBS data volume per instance, when `ImageUseLocalStorage` is off. Smaller than the llm role's 120
because the whole role is a 2.3 GB image and a 6.5 GB checkpoint — but not much smaller: the
anonymous host volume is re-allocated per task and never reclaimed (see the llm task definition),
so an instance that MI keeps accumulates one copy per start.

### `ImageUseLocalStorage`

Use the instance store instead of an EBS data volume. Same knob and same open question as the llm
role (ADR 0071 open question 1), and less pressing here: 6.5 GB from S3 at 115-147 MB/s is under
a minute either way.

### `ImageScaleInAfter`

`infrastructureOptimization.scaleInAfter`, in seconds. `-2` = do not set it (AWS's default). `-1`
= never tidy up, which P0 measured to be a trap with an anonymous model volume: the instance is kept
but the next task gets a FRESH empty directory and re-fetches anyway, while the old copies are
never reclaimed. Left at the default until decision 7(c) has a proven warm-instance shape.

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
to empty, and empty means the feature does not exist**: the instance is whatever the parameters above
bought, and the Control Plane never calls `DescribeCapacityProviders` or `UpdateCapacityProvider`
at all.

One rung per `;`, seven `|`-separated fields, the last optional:

```
id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB[|usdPerHour]
```

```
LlmInstanceClasses=l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.26;l40s|L40S 48GB|44000|g6e.xlarge,g6e.2xlarge|4-8|30000-65536
```

- **The FIRST rung is the default**, and it should restate `LlmAllowedInstanceTypes`,
  `LlmAcceleratorMemMinMiB`, `LlmVCpu*` and `LlmMem*`. Nothing checks that it does — the two
  are separate declarations, and the first is what the Console offers as "back to the default".
- **`vramMiB` is both the floor asked of the card (`AcceleratorTotalMemoryMiB.Min`) and what a
  model's demand is compared against** — and it is **the card's physical size**, not an
  operational cap. 🔴 Declare it from what the hardware reports: an L4 says
  `Total VRAM 22563 MB`, so **22000 is the number**. The nearby `LlmAcceleratorMemMinMiB` of
  8000 is a *placement filter* and copying it into a rung is a real bug — measured 2026-09-11
  on the dev deployment, a rung declaring 8000 made the panel say `vram_fits: false` for a model
  that then generated perfectly well on that very card. Shading it downward is just as wrong in
  the other direction once the demand includes the KV cache: a 30B at 32k context really
  occupies 20,712 MiB, which fits 22,563 with 1.8 GB to spare and does NOT fit a declared
  21,000. See ADR 0074, "open question 7 for the llm role".
- **`usdPerHour` is display-only and optional.** Nothing computes with it and neither EC2 nor
  the Pricing API is asked (ADR 0045 decision 21). Leave it out and the panel names no price,
  which beats naming a wrong one.
  ⚠️ **Do not copy a list price into it.** The Pricing API's Tokyo on-demand figure for a
  g6.xlarge is $1.1672, while the bill measured in ADR 0071 was **$1.26** — ECS Managed
  Instances charges a management fee on top of EC2, so a list price makes the panel about 8%
  cheaper than reality (measured 2026-09-10, ADR 0074 P1). Declare what was billed, or nothing.
- A malformed rung is dropped with a log line; a malformed PRICE only drops the price.
- ⚠️ The ladder is not checked against the account's **G-family vCPU quota**, and it cannot be:
  the CP has no `service-quotas` permission and the quota differs per deployment (96 in
  production, 8 on a fresh account). A rung above it is selectable and simply never places —
  which surfaces as `VcpuLimitExceeded` in the service events, where the panel shows it.

**What changing a rung does.** The Control Plane rewrites four fields of the capacity provider's
instance requirements (`AllowedInstanceTypes`, `AcceleratorTotalMemoryMiB.Min`, `VCpuCount`,
`MemoryMiB`) and hands everything else back exactly as it read it. It applies the choice when it
is saved and again immediately before every start, because a stack update puts this template's
declaration back and nothing tells the CP that happened.

⚠️ **It reaches the NEXT instance only** — the API's own words are "These changes only apply to new
Amazon ECS Managed Instances". So a running engine keeps its card until it is replaced, and the
Control Plane will not start a new task while an instance of another rung is still registered: that is
the `VcpuLimitExceeded` above, and it is also how a task would land straight back on the old
card. The wait is bounded at 20 minutes, because `scaleInAfter: -1` would otherwise make it
never end.

**Permissions.** `ecs:DescribeCapacityProviders` + `ecs:UpdateCapacityProvider` on this stack's
own capacity providers, `ecs:PutClusterCapacityProviders` on the cluster, and `iam:PassRole` on
`InfraRole` and `InstanceRole` — the update re-declares the launch template, which names both.
The resource scope IS the boundary: that API can also move subnets and security groups, so it is
pinned to `af-<stack>-*` and the CP only ever sends values that came out of the ladder above.

🔴 **`PutClusterCapacityProviders` is an API the Control Plane never calls.** ECS authorizes an
update of a **Managed Instances** provider as that action **on the cluster**, so without the
grant the update fails with an `AccessDeniedException` naming an action that appears nowhere in
the code:

```
AccessDeniedException: User: …/af-<stack>-cp-task/… is not authorized to perform:
ecs:PutClusterCapacityProviders on resource: …:cluster/af-<platform-stack>
```

Neither the API reference nor the SDK says so; it was measured on a deployment (ADR 0074 P1),
and it is the reason a rung change cannot be proven to work by unit tests. Note what it widens:
that action is also how the cluster's whole provider ASSOCIATION list is replaced, so the grant
is cluster-scoped where the other three are provider-scoped. It is on the same cluster this
stack already owns the associations of (README, "60-engines").

⚠️ **`DescribeCapacityProviders` takes a cluster or a list of names — never both.** A request
carrying both is refused with `InvalidParameterException: Cannot specify both capacity providers
and cluster in the same request`, which is documented nowhere. The CP asks by name and checks
the cluster on the ANSWER instead (`applyEngineClass`); a provider that answered with another
cluster is not written to.

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

🔴 **Only `standup.sh` puts the image where the stack can reach it on the release path.
`update.sh` does not, and neither it nor `dev-deploy.sh` deploys 20-platform** (`dev-deploy.sh`
carries the image, below, but not the repository). So on any deployment that is
UPDATED rather than stood up, three things have to happen by hand, in this order, before the
60-engines update that references a new tag — measured 2026-09-11, when the dev deployment had
neither the GHCR image nor the ECR repository (ADR 0072, "#518 and #512, confirmed on
hardware"):

1. `gh workflow run engine-tools-image.yml -f tag=<tag>` — bakes to GHCR. Touches no
   deployment.
2. `cloudformation deploy` 20-platform, which owns the `af-engine-tools` ECR repository.
   (`update.sh` only deploys 50-tts, 60-engines and 30-ingress, so a repository added to
   20-platform never appears on an updated deployment.) Take a change set first: on the run
   that was measured it was two changes, `Add EcrEngineTools` and `Modify CpTaskRole`, with no
   replacement.
3. `crane copy ghcr.io/<owner>/agent-fleet/engine-tools:<tag> <account>.dkr.ecr.<region>.amazonaws.com/af-engine-tools:<tag>`

Skip them and the update still "succeeds": CloudFormation writes task definitions pointing at
an image that is not there, and both roles' fetch containers and both ingest containers fail
with `CannotPullContainerError`. It is the same trap the sd-server `crane copy` carries in
`standup.sh`, on a path that has no `standup.sh` to carry it.

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
token.** Two roles on purpose: the credential that can overwrite a model file must not sit on a
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

The image role's capacity provider is created even when no image model is staged: a provider
costs nothing while nothing runs on it, it is named in the association list (which is replaced
wholesale, so it cannot be added conditionally without churning the list), and deleting one a
service used is a stack update that has to wait for the service to go first.

## The engine security group

**Reachability IS the access control** (ADR 0071 decision 4): port 8080, from the Control
Plane's security group and from nothing else. sd-server has no authentication of its own, so
for the image role this is the whole of it; the llm role adds `--api-key` on top
([`LlmApiKeySsmParam`](#llmapikeyssmparam)).

## The capacity providers

**One provider per role, and the two roles never share an instance.** CUDA does not slow down when
VRAM runs out, it crashes, and the two measured footprints (20.9 GB for the 30B, 7.4 GB for
SDXL) do not both fit on one L4's 22,888 MiB (ADR 0071 decision 2). Two providers is also what
makes `draining` observable per role — and what makes the deployment want 16 of the G-family
quota, see below.

⚠️ **`ClusterCapacityProviderAssociations` REPLACES the cluster's provider list** — the API is
not additive — so `FARGATE` and `FARGATE_SPOT` have to be named alongside ours or every Fargate
service in the deployment loses its provider. `DefaultCapacityProviderStrategy` is a REQUIRED
property and is deliberately EMPTY: with a default strategy in place, a service that does not
spell out `LaunchType: FARGATE` lands on the GPU instance instead (ADR 0070 decision 1 — one missing
line is the whole of that failure).

⚠️ **Creating a Managed Instances provider ADDS it to the cluster's list by itself.** Measured
2026-09-11: a throwaway stack holding nothing but one provider (no
`ClusterCapacityProviderAssociations` anywhere in it) put its provider into
`DescribeClusters.capacityProviders`, and deleting the stack took it back out. So "one stack
owns the associations" is a rule about who REPLACES the list, not a fence around it — a second
stack that creates a provider against this cluster is visible in the list until the next time
this stack's `Associations` resource is updated, which silently drops it.

### `ImageCapacityOptionType` — Spot for the image role, and why it renames the provider

`ON_DEMAND` (the default) or `SPOT`, for the `image` role only. **The `llm` role is deliberately
not offered it**: Spot's two-minute termination notice arrives mid-conversation, and a
527-586-second cold start is what follows it. An image request is one call that can be made
again.

Switching it **replaces the capacity provider**, and the template is written so that the
replacement can actually happen. Two measurements from 2026-09-11 (a throwaway stack holding a
copy of this resource, `deploy/aws/ecs/harness/probe-capacity-option.yaml`):

- 🔴 **changing the field alone fails.** The change set reads `Modify` /
  `Replacement: Conditional`, which looks survivable, and then execution stops on
  `CloudFormation cannot update a stack when a custom-named resource requires replacing. Rename
  af-<stack>-image and update the stack again.` -> `UPDATE_ROLLBACK_COMPLETE`. The field is
  create-only (`UpdateCapacityProvider`'s `InstanceLaunchTemplateUpdate` has no such member) and
  a provider's ARN is derived from its `Name`, so a replacement is a same-name collision;
- ✅ **changing the field AND the name works.** `Replacement: True`, `Name` /
  `RequiresRecreation: Always`, and the update creates `af-<stack>-image-spot`, moves everything
  that points at it, then deletes the old one in the cleanup phase (`UPDATE_COMPLETE`). The
  round trip back to `ON_DEMAND` works the same way, reusing the original name even though ECS
  still holds the retired provider as an `INACTIVE` record.

So the name carries the option type — but only on the Spot side. `ON_DEMAND` keeps the historic
`af-<stack>-image`, so **a deployment that never asks for Spot is not replaced at all**; the
resolved template is byte for byte what it was (`deploy/local/cfn-equiv.py`).

⚠️ **Switch it while the image role is stopped.** The replacement moves the service's
`CapacityProviderStrategy`, which is a new deployment: an instance that is up drains, and the next
request pays the cold start again (about 195 s plus the model sync). Nothing is lost, but a
generation in flight is.

Everything that names the provider follows the resource — the service's strategy, the cluster
associations, and the engine table row, which is `!Ref ImageCapacityProvider` (on a capacity
provider, `!Ref` is its NAME). 🔴 **Keep it that way.** A table that restates the name as a
`!Sub` fails SILENTLY when the name moves: the Control Plane goes on watching a provider nobody
uses, and that is where both `draining` and the ADR 0074 rung application read from.

⚠️ Spot capacity is a SEPARATE quota, and it bites at launch and not at configuration: a `SPOT`
provider is created happily with the quota at 0 (measured, ADR 0074) and then never buys an instance,
which reads exactly like an engine that will not start. `L-3819A6DF` ("All G and VT Spot
Instance Requests", default 0) is the one to hold — acrt has 64, af-sandbox has 0.
`L-DB2E81BB` does not exist; do not look for it.

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

⚠️ **And no `LaunchType` either**: a capacity provider strategy and a launch type are mutually
exclusive. These are the ONE pair of services in the deployment allowed to omit `LaunchType`,
and only because they name their provider explicitly.

**`DeploymentConfiguration` is `MinimumHealthyPercent: 0` / `MaximumPercent: 100`** so a
redeploy stops the old task BEFORE starting the new one. One engine has one DNS name, and two
tasks behind it during a rollout split requests between a warm engine and a cold one -- the
caller sees a random 500-second first token. Stopping first costs a gap; overlapping costs a
lie about which engine answered.

**`HealthCheckCustomConfig: { FailureThreshold: 1 }` on the Cloud Map service is not optional**:
Cloud Map refuses to register an ECS service that has no load balancer without it. There is
nothing to tune -- ECS is the only thing that ever reports the instance's health here.

## The G-family quota

Two capacity providers means two instances, i.e. 8 vCPU of the G-family quota at once — and an instance
that was just stopped holds its 4 vCPU for the 7–8 minutes it spends draining, so waking one
role while the other is on its way out needs 12. A deployment that uses both roles should hold
16 or more (quota `L-DB2E81BA`, which is a support case and not auto-approved).

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
Both halves of that are measured, and both are counter-intuitive:

- **a named `SourcePath` does NOT work.** `StorageConfiguration.storageSizeGiB` sizes the DATA
  volume Managed Instances attaches (what the container runtime uses); an arbitrary host path
  like `/var/lib/…` lands on the ROOT filesystem, which is much smaller. Measured: with
  `SourcePath` the fetch died with "No space left on device" on a brand-new 120 GiB instance, every
  time, and the service never started at all.
- **the price of the anonymous form is a fresh directory per task.** An instance that MI keeps
  (`scaleInAfter -1`) does NOT skip the S3 fetch on the next start — measured, a restart onto the
  very same instance re-fetched all 18.5 GB (126 s) — and the previous tasks' directories are
  never reclaimed, so four starts on one kept instance filled the disk.

**2026-09-09: decision 7(c)'s "warm instance" is no longer unproven — it is DISPROVEN**, and
`*ScaleInAfter` stays at the AWS default rather than `-1` for a reason that can now be stated
instead of suspected. Four measurements, about four minutes of GPU:

- **why the anonymous form re-fetches**, which used to be an observation without a mechanism. A
  container cannot see the host path of its own bind mount from `df`, but `/proc/self/mountinfo`
  can, and it says: `/._mnt_task/volumes/<TASK-ID>/volumes/models → /models  ext4 /dev/nvme1n1`.
  **The task id is IN the path.** Every task gets a new empty directory by construction; no
  setting changes that, and only a NAMED volume could.
- **a named `SourcePath` does persist.** Two tasks in a row on the same instance, mounting
  `/var/lib/af-warm-models`: run 1 MISS and fetched, run 2 **HIT** with the same mtime. The half
  of decision 7(c) everyone assumed was the hard half works fine.
- **but it lands on 3.1 GB.** That same mount is `/dev/nvme0n1p8`, a small partition on the ROOT
  volume — not `/dev/nvme1n1`, the 245 GB data volume where the anonymous volumes live. The
  1.1 GB probe object fit at 38% full; an 18.5 GB model reproduces the "No space left" above
  exactly. Persistence without capacity is what this question kept mistaking for progress.
- **and the data volume has no nameable path.** The AMI is Bottlerocket: mounting the host root
  gives a 2.7 GB, 100%-full, read-only dm-verity image, and EVERY top-level directory a
  `SourcePath` could name — `/local`, `/mnt`, `/data`, `/opt`, `/var` — resolves inside that
  image rather than into the live host's mounts. `/._mnt_task` cannot even be created
  ("read-only file system"). `useLocalStorage`, which was where this was to be picked up, does
  not change it: it changes what the data volume IS, not where a `SourcePath` may point.

So on Managed Instances a warm model volume cannot be built out of host volumes at all, and
keeping an instance buys only the image layers. The harness is `harness/probe-warm-volume.sh`, which
prints the DEVICE as well as HIT/MISS so the distinction that took three sessions to see is the
first thing the next reader gets. What DID pay off is the other half of ADR 0072 open question
10 — see `LlmUseLocalStorage`.

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
row changes — `url`, `capacityProvider` and `service` are the same regardless, because it is
still one role, one service.

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
left is the vessel (service, URL, health path, warm path, capacity provider, classes, idle
window, start deadline, mode), which really is the stack's to declare.

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
| `LlmCapacityProvider` / `ImageCapacityProvider` | `engine-llm` / `engine-image` | `PropagateTags: CAPACITY_PROVIDER` — the MI instance is the billed unit |
| `LlmService` / `ImageService` | `engine-llm` / `engine-image` | `PropagateTags: SERVICE` — the MI management fee is billed against the task |
| `ModelsBucket` | `engine-models` | bucket tags; S3 storage for the staged GGUF / checkpoints |
| `LogGroup` | `engine-logs` | log group tags; CloudWatch ingestion and storage |

Measured on af-sandbox over 2026-09-01..08 (one Cost Explorer request, grouped by `af-role` and
SERVICE): `engine-llm` $5.18, `engine-image` $0.73 — of which $0.37 and $0.05 arrive as "Amazon
Elastic Container Service" rather than EC2, i.e. the Managed Instances fee DOES inherit the
service's tags, and $0.08 as "EC2 - Other" for the data volume. None of it carries
`af-membership`, so all of it lands in the shared bucket and none of it is ever charged to a
person.

**What still cannot be split**: the ingest job pulls model weights from Hugging Face over the NAT
gateway, and NAT data processing is untaggable (ADR 0048 decision 4). Fetching the same file from
S3 afterwards does not — 00-network's gateway endpoint keeps that off the NAT entirely.

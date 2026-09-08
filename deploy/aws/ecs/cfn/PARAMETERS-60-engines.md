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
- [The seed parameters](#the-seed-parameters)
- [The `llm` role (llama.cpp)](#the-llm-role-llamacpp)
- [The `image` role (stable-diffusion.cpp)](#the-image-role-stable-diffusioncpp)
- [The fetch sidecar](#the-fetch-sidecar)
- [The idle wrapper](#the-idle-wrapper)
- [The engine services](#the-engine-services)
- [The G-family quota](#the-g-family-quota)
- [Ingest](#ingest)
- [The model volume](#the-model-volume)
- [The engine table](#the-engine-table)

## What this template learned the hard way

All measured on a real cluster on 2026-09-07 through `deploy/aws/ecs/harness/engprobe.yaml`.

- a Managed Instances capacity provider is CLUSTER-SCOPED: `ClusterName` is mandatory, and
  without it ECS answers "The cluster provided is invalid";
- `AmazonECSInfrastructureRolePolicyForManagedInstances` passes only roles named
  `ecsInstanceRole*`, so the fleet-named instance role needs an explicit `iam:PassRole`
  (measured: `UnauthorizedOperation` otherwise, visible only in CloudTrail);
- `ClusterCapacityProviderAssociations` REPLACES the cluster's provider list and requires
  `DefaultCapacityProviderStrategy` — so the full list is named in the template and the default
  strategy is deliberately EMPTY. Put anything in it and a service that forgot its `LaunchType`
  lands on the GPU box (ADR 0070 decision 1, ADR 0071 decision 1);
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

## The seed parameters

`LlmModelS3Key` / `LlmModelFile` / `LlmModelIds` / `LlmContextTokens` / `LlmMaxOutputTokens`
and the four `Image*` mirrors are **no longer what the engine loads** (ADR 0072 decision 1).
The catalogue in the Control Plane's database is, and an administrator edits it from the admin
panel while the GPU is asleep — the act these parameters made into a CloudFormation run.

They survive as the **seed** (decision 7). A Control Plane whose catalogue for a role is EMPTY
reads them once and creates the one row the deployment was already serving, so an upgrade from
ADR 0071 comes up unchanged. After that they are ignored, which is the point: the seed must not
overwrite an administrator's choice on every stack update.

⚠️ **The S3 layout move and the seed happen together** (decision 2(f)). The bucket now follows
ComfyUI's layout, so `image/sd_xl_base_1.0.safetensors` belongs under `image/checkpoints/`. Do
the server-side copy AND update `ImageModelS3Key` in the same change: a seed pointing at the old
key produces a row whose file is not there, and the failure surfaces in the fetch sidecar at the
next cold start rather than at deploy time.

`LlmModelFile` / `ImageModelFile` are unused outright. The box mirrors the bucket —
`image/checkpoints/x.safetensors` lands at `/models/image/checkpoints/x.safetensors` — which is
what keeps the active set inside SSM's 4,096 characters and leaves a tree ComfyUI reads
unchanged.

### `LlmEnabled` / `ImageEnabled`

Whether the ROLE exists at all: its service, its Cloud Map name, its row in the engine table.
`""` (the default) means "whatever `*ModelS3Key` said", which is the one release of
compatibility decision 1 allows — an existing `params/60-engines` that switched a role off by
emptying its model key still does.

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

### `LlmModelS3Key`

Key inside this stack's models bucket, e.g. `llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf`. The
engine never talks to Hugging Face (ADR 0071 decision 3 — measured at 4-236 MB/s with no way to
tell which until you pull, against S3's steady 104-147 MB/s), so a model gets there through the
ingest task.

⚠️ **EMPTY (the default) means NO `llm` SERVICE IS CREATED** — only the bucket, the ingest task,
the capacity provider and the roles. That is deliberate and it is the first half of a two-pass
stand-up: CloudFormation blocks on ECS service stabilisation, and a service whose model is not
in the bucket yet can never become stable, so creating it before there is anything to serve
leaves the stack in `CREATE_IN_PROGRESS` with no later step able to rescue it (the same trap
20-platform's ECR repositories exist to avoid). Deploy once with this empty, run the ingest
task, then deploy again with the key.

### `LlmModelIds`

Model ids the gateway advertises for this engine, i.e. what a user picks as `llamacpp/<id>`.
DECLARED, never derived (ADR 0053): the engine is asleep when the launch menu is drawn, and
waking a GPU box to enumerate one model is the opposite of on-demand. The first id is what
`--alias` tells llama-server to answer to.

### `LlmContextTokens` / `LlmMaxOutputTokens`

The context `llama-server` is started with (`-c`) **and** the window the client is told the model
has. One parameter feeds both, so "served" and "advertised" cannot drift — the flag used to live
inside `LlmExtraArgs`, where CloudFormation cannot read it back out to put it in the engine table,
and the table therefore said nothing at all. 32,768 is what was measured on an L4 (opencode's
request is 18.7k tokens with the fleet's `AGENTS.md` included).

⚠️ **Do not also pass `-c` in `LlmExtraArgs`.** The command line is built as
`… -c <LlmContextTokens> <LlmExtraArgs…>`, so a second `-c` wins for the engine while the table
keeps advertising this one — the exact drift the split exists to prevent.

Both numbers travel, and the second is not decoration. Measured against opencode 1.18.29, which is
what a workspace drives this engine with:

- a model the client has never heard of and that declares no `limit` is read as **context 0**, and
  auto-compaction is **switched off** at 0. The session then grows until `llama-server` rejects the
  request, with nothing in the UI to explain it;
- the usable window is `context − output cap`, and an output cap of **0 is not "unset"** there — it
  substitutes 32,000. Declaring a 32,768-token context without the output half would leave **768**
  usable tokens, i.e. compaction thrashing from the first turn. That is why the Agent writes both
  or neither, and why both carry a `MinValue`.

One pair per **engine**, not per model: one `llama-server` process serves one GGUF with one `-c`,
so the several ids in `LlmModelIds` are aliases sharing that window. Two models with different
windows are two engines — two rows in the table, and **two `provider` ids**, because the Agent
keys opencode's provider block by provider id and a second `llamacpp` would overwrite the first.

### `LlmExtraArgs`

Extra `llama-server` flags. The defaults are the ones measured on an L4: all layers on the GPU and
the chat template applied, which is what makes tool calls work. The context is `LlmContextTokens`.

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

⚠️ `InstanceRequirements` refuses this alongside `InstanceGenerations`.

### `LlmGpuCount`, `LlmVCpuMin` / `LlmVCpuMax`, `LlmMemMinMiB` / `LlmMemMaxMiB`

GPUs per box (and the task's GPU resource requirement; 0 = a CPU box, test only) and the bounds
of the instance requirement.

### `LlmStorageGiB`

EBS data volume per box, when `LlmUseLocalStorage` is off. It has to hold the image layers plus
every model the role pulls from S3. MI exposes only the SIZE — throughput and IOPS cannot be
asked for (measured against the API), which is why `UseLocalStorage` exists.

### `LlmUseLocalStorage`

Use the instance store instead of an EBS data volume. ADR 0071 open question 1: on g6.xlarge the
EBS baseline is 125 MB/s, and that one number bounds BOTH the S3 fetch (measured 104-147 MB/s,
i.e. the write side saturates) and the 267-second load of 18.5 GB into VRAM. The instance store
is 250 GB of local NVMe. Off by default until the measurement in the ADR says otherwise — an
instance store is also wiped on every box, which costs a fresh S3 fetch per cold start.

### `LlmScaleInAfter`

`infrastructureOptimization.scaleInAfter`, in seconds: how long MI leaves an idle box before
terminating it. `-2` = do not set it, i.e. AWS's default (measured drain: 427 and 463 seconds on
a GPU box, 93 on a CPU box). `-1` = never tidy up. `0`-`3600` = that many seconds. Worth having
as a knob because a box kept a little longer would be a warm start — but see the task
definition's volume comment: with an anonymous host volume the kept box re-fetches anyway, so
ADR 0071 decision 7(c) is unproven and the default is the right setting today.

### `LlmIdleSec`

`AF_ENGINE_LLM_IDLE_SEC` — how long the engine goes unwanted before the controller stops it.
Written into the SSM table rather than passed to 30-ingress, which has no room left for
per-engine parameters (ADR 0071 decision 8).

### `LlmStartDeadlineSec`

How long a start may take before the controller calls it failed. MUST exceed the real cold start
or every start is recorded as a failure and the cooldown doubles away (measured 527 s from S3
with the image pulled over NAT, 586 s with it pulled from ECR; ADR 0070's 300 s default would
fail all of them).

### `LlmMode`

The engine's initial mode, written into the SSM table as the DEFAULT only — once an admin sets
it the stored setting wins, because under on-demand the desired count is not the admin's intent
(ADR 0070 decision 7).

## The `image` role (stable-diffusion.cpp)

A mirror of the `llm` block, and deliberately a mirror rather than a shared set of parameters:
decision 2 is that instance requirements are DECLARED per role, because the floor is a VRAM
number (SDXL fp16 measured 7,379 MiB against the 30B's 20,943) and the two roles must never land
on the same box — CUDA does not slow down when VRAM runs out, it crashes.

### `ImageImageTag`

Tag inside the `af-sdcpp` ECR repository (the doubled word is the `image` ROLE's container image
tag). Upstream is `ghcr.io/leejet/stable-diffusion.cpp:master-cuda`, 2.31 GB and **amd64 only** —
there is no arm64 build and G-family instances have no arm64 member either, so this role is
x86_64 by construction (ADR 0071 decision 12). `standup.sh` copies it in with `crane` before this
stack is deployed, and only when `ImageModelS3Key` is set.

### `ImageModelS3Key`

Key inside this stack's models bucket, e.g. `image/sd_xl_base_1.0.safetensors`.

⚠️ **EMPTY (the default) means NO `image` SERVICE IS CREATED**, for exactly the reason spelled
out under `LlmModelS3Key`: CloudFormation blocks on ECS service stabilisation, the fetch sidecar
of a service whose model is not in the bucket crash-loops on "Key … does not exist", and the
stack then sits in `CREATE_IN_PROGRESS` with no later step able to rescue it (measured on the llm
role during P0). Deploy once with this empty, run the ingest task, then deploy again with the key.

### `ImageModelFile`

File name the checkpoint is copied to on the box, i.e. `/models/<ImageModelFile>`. sd-server reads
safetensors directly, so this is the file the ingest task uploaded and not a converted form.

### `ImageModelIds`

Model ids the gateway advertises for this engine. DECLARED, never derived (ADR 0053): sd-server
does have a `/sdcpp/v1/capabilities` endpoint, but asking it means waking a $1.26/hour box to
read one string, and the Agent needs this answer while the engine is asleep. The FIRST id is the
provider's default model, and the Agent's `sdcpp` provider keys its capabilities off it (ADR 0069
decision 5 — `Caps` is per (provider, model)).

### `ImageExtraArgs`

Extra `sd-server` flags. `--diffusion-fa` (flash attention) is what the P0 GPU measurement ran
with: 1024px in 20.8-21.0 s and 7.4 GB of VRAM on an L4.

There is no `ImageApiKeySsmParam`, and that is not an omission: stable-diffusion.cpp's server has
no authentication option at all (upstream `examples/server/api.md` documents none), so the
security group — port 8080 from the CP and nothing else — is the whole of this engine's access
control. The llm role's `--api-key` is a second lock that does not exist here.

### `ImageAllowedInstanceTypes`

Instance types the image capacity provider may buy. The measured floor is 7,379 MiB of VRAM for
SDXL fp16, so g6f.2xlarge's 5,722 MiB does not fit it in that form — but `--offload-to-cpu` and
quantisation are UNMEASURED (ADR 0071 decision 2, P4), so this is a default rather than a proof
that nothing smaller works.

### `ImageStorageGiB`

EBS data volume per box, when `ImageUseLocalStorage` is off. Smaller than the llm role's 120
because the whole role is a 2.3 GB image and a 6.5 GB checkpoint — but not much smaller: the
anonymous host volume is re-allocated per task and never reclaimed (see the llm task definition),
so a box that MI keeps accumulates one copy per start.

### `ImageUseLocalStorage`

Use the instance store instead of an EBS data volume. Same knob and same open question as the llm
role (ADR 0071 open question 1), and less pressing here: 6.5 GB from S3 at 115-147 MB/s is under
a minute either way.

### `ImageScaleInAfter`

`infrastructureOptimization.scaleInAfter`, in seconds. `-2` = do not set it (AWS's default). `-1`
= never tidy up, which P0 measured to be a trap with an anonymous model volume: the box is kept
but the next task gets a FRESH empty directory and re-fetches anyway, while the old copies are
never reclaimed. Left at the default until decision 7(c) has a proven warm-box shape.

### `ImageIdleSec`

`AF_ENGINE_IMAGE_IDLE_SEC` — how long the engine goes unwanted before the controller stops it.
HALF the llm role's window on purpose: an image is a 21-second request with no conversation
around it, while an LLM turn sits inside a session that thinks for minutes between requests.
900 s is $0.31 of idle GPU against a ~300-second re-wake, which is the trade this number is
making.

### `ImageStartDeadlineSec`

How long a start may take before the controller calls it failed. sd-server measured 195 s from
task creation to listen (135 s of that a GHCR pull, so less from ECR) — but the dominant term is
how long the BOX takes to appear, measured anywhere from 8 to 88 s and not something a deadline
should be tuned against (ADR 0071, P0 measurement 1).

### `ImageMode`

As `LlmMode`: the initial mode, a default the stored setting overrides.

## The fetch sidecar

One script for both roles, in the template's `Mappings` (ADR 0072 decision 1(a)). It reads the
active set the Control Plane published to SSM, syncs the files the engine is about to load, and
writes `/models/cmdline` — the model-specific half of the argument list. Everything
role-specific is an environment variable: `ACTIVE_PARAM`, `BUCKET`, `MODELS_DIR`, and
`ALIAS_FLAG` / `CTX_FLAG` (llama-server names its model with `--alias` and takes its window as
`-c`; sd-server has neither, so the image role passes both empty).

Three rules, each with a failure behind it:

- **`ParameterNotFound` is EMPTY, not an error.** 60-engines is created BEFORE 30-ingress, so at
  stack-creation time there is no Control Plane and no parameter at all. A `set -e` that failed
  here would bring the two-pass stand-up back in a new shape (ADR 0072 decision 1(b)).
- **Only the STARTING model's files are fetched**, plus every enabled LoRA. S3 to EBS ran at
  92–147 MB/s (measured), so syncing every enabled model would put minutes of somebody else's
  checkpoint into every cold start. Enabling a model OFFERS it; selecting one LOADS it.
- **A file already on the box is not re-fetched.** The volume is fresh per task today, so this
  is currently a no-op — it is what makes a warm box worth anything if ADR 0071 decision 7(c)
  ever gets one.

`jq`, not a JSON-in-shell parser: `public.ecr.aws/aws-cli/aws-cli` carries `jq`, `python3` and
`bash` (verified with `crane export`).

## The idle wrapper

Both engine containers start as `sh -c 'if [ -s /models/cmdline ]; then exec <engine> $(cat
/models/cmdline); else … sleep infinity; fi'`. An engine with nothing to load has to COME UP
rather than fail, or CloudFormation waits on a service that never stabilises.

- **The binary path is spelled out** because the entry point is being overridden. llama.cpp's
  own `ENTRYPOINT` is `["/app/llama-server"]` and `/app` is NOT on `PATH` (measured with
  `crane config ghcr.io/ggml-org/llama.cpp:server-cuda`); sd-server's image entry point is
  `/sd-cli`, the one-shot CLI, so that one always had to be overridden.
- **`$(cat …)` is deliberately unquoted** — the file IS an argument list. The Control Plane
  refuses to publish an S3 key containing whitespace for exactly this reason.
- **Idling is not free**, and the template is not what makes it cheap. A placeholder container
  reaches RUNNING and never warms, and `running && !warmed` is not a failure state, so the
  controller would re-examine it every five seconds for ever. The Control Plane's
  `decideEngineAction` refuses to start — and stops — an engine whose catalogue is empty
  (`no_model`, ADR 0072 decision 1(c)). Without that half, `mode=on` buys $1.26/hour for
  `sleep infinity`.

## The engine services

⚠️ **`DesiredCount` is deliberately ABSENT, and this is load-bearing.** From the resource
schema: for a NEW service an unspecified desired count defaults to 1; for an EXISTING one it is
omitted from the update call. So CloudFormation creates the service running and then never
touches the count again — which is what lets the engine be started and stopped out from under
the template. Declaring `0` instead would reset the count on every task-definition or tag
change, i.e. kill a GPU box mid-answer. `standup.sh` scales the new service to 0 straight after
creation.

⚠️ **And no `LaunchType` either**: a capacity provider strategy and a launch type are mutually
exclusive. These are the ONE pair of services in the deployment allowed to omit `LaunchType`,
and only because they name their provider explicitly.

## The G-family quota

Two capacity providers means two boxes, i.e. 8 vCPU of the G-family quota at once — and a box
that was just stopped holds its 4 vCPU for the 7–8 minutes it spends draining, so waking one
role while the other is on its way out needs 12. A deployment that uses both roles should hold
16 or more (quota `L-DB2E81BA`, which is a support case and not auto-approved).

## Ingest

### `HfTokenSecretArn`

Secrets Manager ARN holding `HF_TOKEN`, read by the INGEST task only (ADR 0071 decision 3: the
token is the operator's, and it never lands on an engine box). Empty = only ungated repositories
can be ingested, which covers every model in the ADR's table except FLUX.1-dev and SD 3.5.

### `IngestCpu` / `IngestMemory` / `IngestDiskGiB`

Fargate sizing for the ingest task. It stages the whole file on disk before uploading, so the
disk is the largest model the deployment can take in — 80 GiB covers the 22 GB FLUX checkpoint
with room for the filesystem.

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

`harness/ingest-model.sh` is the same thing with the sha256 and the license resolved for you;
ADR 0072 phase P4 moves the whole flow into the Console.

⚠️ **Overriding a command here takes ONE string**, because the `EntryPoint` is already
`["sh","-c"]`. Passing `["sh","-c",<script>]` becomes `sh -c sh -c <script>`, which does nothing
and exits 0 — it reads as success and ran nothing (measured).

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
  `SourcePath` the fetch died with "No space left on device" on a brand-new 120 GiB box, every
  time, and the service never started at all.
- **the price of the anonymous form is a fresh directory per task.** A box that MI keeps
  (`scaleInAfter -1`) does NOT skip the S3 fetch on the next start — measured, a restart onto the
  very same instance re-fetched all 18.5 GB (126 s) — and the previous tasks' directories are
  never reclaimed, so four starts on one kept box filled the disk.

ADR 0071 decision 7(c)'s "warm box" therefore stays UNPROVEN, and `*ScaleInAfter` is left at the
AWS default rather than `-1`. Making it work needs the path MI's data volume is actually mounted
at, which is not documented and was not worth another GPU hour to find; `useLocalStorage`, where
the 250 GB instance store is the whole disk, is where to pick it up (ADR 0072 open question 9).

## The engine table

Not a parameter but the stack's real output: the one SSM value 30-ingress is handed, holding 0, 1
or 2 rows (each role is staged independently), which the Control Plane reads once at startup.

**`api`** tells the reader what KIND of endpoint a row is, and it decides two things that would
otherwise be guessed from the key: whether the Agent writes an opencode chat provider for it — an
image engine there would put `sdcpp/sdxl-base-1.0` in the launch menu as something to hold a
conversation with — and whether the gateway counts tokens out of the response (`chat`) or leaves
the accounting to the Agent's `tool.imagegen` row (`images`, ADR 0071 decision 9).

**The image row's health path is `/v1/models`, not `/health`**: stable-diffusion.cpp's server has
no health endpoint at all (upstream `examples/server/api.md`), and `/v1/models` is the cheapest GET
it answers. It only listens once the checkpoint is loaded, so a 200 there really does mean ready.

**The llm row carries `contextTokens` / `maxOutputTokens`** — see those parameters above. Nothing
downstream can ask the engine for them: the box is asleep when the launch menu is drawn, which is
the whole point of an on-demand engine. A row from a stack older than the fields omits them, and
both the gateway and the Agent then say nothing rather than advertising a zero.

**`models`, `contextTokens`, `maxOutputTokens` and `modelS3Key` are SEED fields** since ADR 0072
— see [The seed parameters](#the-seed-parameters). Everything else in the row is a property of
the vessel (service, URL, health path, capacity provider, idle window, start deadline, mode) and
stays the stack's to declare.

**The active set is a different parameter, and the stack does not write it.** The Control Plane
publishes `<EnginesSsmParamName>/<key>/active` whenever the catalogue changes, and the box's
fetch sidecar reads it. Two consequences worth stating: the `EngineTaskRole` grants
`ssm:GetParameter` on `<EnginesSsmParamName>/*` (inside this stack, per ADR 0071 decision 8), and
because CloudFormation does not own those parameters, **`teardown.sh` deletes them** — otherwise
a torn-down deployment leaves `/af-ws/engines/*/active` behind for the next one to read.

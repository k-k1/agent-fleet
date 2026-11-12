---
audience: "a deployment administrator who adds the fleet's own GPU inference engines (`60-engines`) to an AWS ECS deployment"
source_of_truth: "`deploy/aws/ecs/cfn/60-engines.yaml` and `cfn/PARAMETERS-60-engines.md`, `deploy/aws/ecs/standup.sh`, and the Control Plane's engine controller (`control-plane/engines.go`, `engine_gateway.go`)"
---

# Add inference engines

English | [日本語](engines.ja.md)

Add `60-engines`, the stack of the fleet's own GPU inference engines, to an ECS deployment.
A GPU instance exists only while somebody is using it: the Control Plane buys one when a request arrives and terminates it when it goes idle.

## How it works

| What you do | What happens |
|---|---|
| Raise the G-family vCPU quota, put `params/60-engines` in the captured state and run `standup.sh` | The images are copied into ECR, `60-engines` stands up empty, and its services are scaled to 0 |
| Take a model in and enable it in Admin → inference engines | The model lands in the S3 bucket, and members can pick it |
| Nothing | The first request makes the Control Plane buy a GPU instance and start the engine; after a stretch of idle it is terminated |

Choosing the mode, taking models in and reading the state are on the admin-screen pages:

- [Run inference engines on your own GPUs](../engines/gpu.md)
- [Take models into the inference engines](../engines/models.md)
- [Check the inference engines' state and history](../engines/status.md)

## Two roles

| Role | Engine | What a member gets | Cold start |
|---|---|---|---|
| `llm` | llama.cpp (`llama-server`) | `llamacpp/<model>` in opencode's launch picker | about 527–586 s |
| `image` | ComfyUI (`comfy`) | the `generate_image` tool's provider `comfy`: generate, edit and inpaint | the instance dominates (8–88 s) |

- Each is optional on its own. Without `<Role>Enabled=true` that role's service is not created at all
- **The two roles never share an instance.** CUDA does not slow down when VRAM runs out, it crashes, and a 30B model (20.9 GB) and SDXL (7.4 GB) do not both fit on one L4
- It is not cheap while it is up: **a g6.xlarge (NVIDIA L4, 24 GB) is $1.26 an hour all in**, or $918 a month if it never stops

Fargate has no GPU, so the engine runs on an EC2 instance. The Control Plane buys it with `CreateFleet(type=instant)` from a launch template this stack owns, and terminates it when the engine goes idle. The Control Plane keeps "no task, no instance" true.

## Before you start

1. **Raise the G-family vCPU quota.** A new account has **zero**, and the increase is not auto-approved: it opens a support case that takes hours to days
   - Ask for **16** (`L-DB2E81BA`, "Running On-Demand G and VT instances") if you run both roles. 8 builds two g6.xlarge, but an instance that has just been terminated keeps its 4 vCPU counted for the 7–8 minutes it spends shutting down, so 16 gives one instance of headroom
   - The quota is a ceiling, not a reservation, and nothing is billed for headroom. Asking for 24 costs the same and also covers a redeploy overlapping a drain

   ```bash
   aws service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA --query 'Quota.Value'
   ```

2. **Only one stack may own the cluster's capacity-provider associations.** The API replaces the list rather than adding to it. If the measurement harness (`harness/engprobe.yaml`) is deployed, take it down first with `deploy/aws/ecs/harness/probe-managed-instances.sh down`. The engines are not in that list, but `60-engines` is what holds `FARGATE` and `FARGATE_SPOT` for the rest of the deployment
3. **Have the service-linked role `AWSServiceRoleForEC2Fleet`.** `standup.sh` creates it. By hand (ignore the "has been taken" error on a re-run):

   ```bash
   aws iam create-service-linked-role --aws-service-name ec2fleet.amazonaws.com || true
   # and, for a deployment that declares a `spot` offer:
   aws iam create-service-linked-role --aws-service-name spot.amazonaws.com || true
   ```

4. **Put the images in ECR before the stack is created.** The reason is the same as for `50-tts`: a service against an empty repository stalls CloudFormation beyond rescue ([Add Japanese read-aloud](tts.md#turn-it-on)). `standup.sh` does it in its images step. By hand:

   ```bash
   crane copy ghcr.io/ggml-org/llama.cpp:server-cuda <acct>.dkr.ecr.<region>.amazonaws.com/af-llamacpp:server-cuda
   # the image role:
   crane copy ghcr.io/k-k1/agent-fleet/comfyui:v0.37.0 <acct>.dkr.ecr.<region>.amazonaws.com/af-comfyui:v0.37.0
   ```

   - GHCR through the NAT ran at 12–14 MB/s, which put 178 seconds of a 527-second cold start into the pull alone. Copying into ECR pays off
   - `standup.sh` copies the image role's image only when `ImageEnabled=true`

### Pin the image content

| Option | What it pins |
|---|---|
| `standup.sh --llm-digest sha256:<...>` | The content of the llama.cpp copy. `server-cuda` is a floating tag, and llama.cpp's own build decides how tool calls parse. `LlmImageTag` is untouched; without the option the copy is what it always was |
| `standup.sh --comfy-digest sha256:<...>` | The content of the ComfyUI copy, whose tag a re-run of `comfyui-image.yml` can overwrite |
| `update.sh --comfy-digest` | The copy `update.sh` makes when it moves a stack off an earlier `ImageComfyImageTag` default |

The details are under `LlmImageTag` in `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md`.

## Turn it on

`standup.sh` builds `60-engines` when, and only when, a `params/60-engines` file exists in the captured state.

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/60-engines
# Every parameter has a default; standup.sh overrides NetworkStackName /
# PlatformStackName / ServiceConnectNamespace from what it already knows.
ServiceConnectNamespace=af.internal
# Whether each role exists at all: its service, its DNS name, its row in the engine table.
LlmEnabled=true
# What the instance is: the ladder of GPU rungs (the first is the default), or the same list
# with a `buy` column. This is the ONLY place the instance's shape is declared.
LlmInstanceClasses=l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.17
# The instance's root volume (GiB). The models land on it, so it has to hold every model the
# role pulls from S3 plus the images.
LlmStorageGiB=120
# The image role, if you want it. Leave this out and no image service exists.
ImageEnabled=true
```

| Parameter | What it is |
|---|---|
| `<Role>Enabled` | Whether the role exists: its service, its DNS name and its row in the engine table. It does not say what the engine loads |
| `LlmInstanceClasses` | The instance's shape: a ladder of GPU rungs, the first one the default, or the same list with a `buy` column. This is the only place the shape is declared; a deployment that declares neither cannot start an engine at all |
| `LlmStorageGiB` | The root volume (default 120 GiB). It has to hold every model the role pulls from S3 plus the images |

- More than one type per rung, or a shortfall in one AZ, is the whole of "the engine never arrives": keep the intended instance the cheapest. The argument is in "The instance classes" and "The offers" in `PARAMETERS-60-engines.md`
- `standup.sh` copies the images into ECR, generates the `llm` engine's own API key into SSM (`/af-ws/engine-llm-key`, a SecureString) if it is not there, deploys `60-engines` after the read-aloud engine and before `30-ingress`, scales each **newly created** service to 0, and hands `30-ingress` the stack's `EnginesSsmParam` output
- The `image` role has no key of its own. ComfyUI has no authentication option at all, so its security group (8080 from the Control Plane and nothing else) is the whole of its access control

**The first stand-up is one pass, and it stands up empty.** The fetch sidecar treats "nothing staged" as success and the engine idles. `LlmEnabled=true` on an empty bucket gives a role that comes up, is scaled to 0, and waits for its first model. Adding a role later is the same one line.

> [!CAUTION]
> **Upgrading a deployment from before `<Role>Enabled`: add `<Role>Enabled=true` before you update.** `LlmModelS3Key` / `ImageModelS3Key` used to decide whether the role's service existed, and they are gone. `standup.sh` translates a captured key into the switch; `update.sh` and a hand-run `cloudformation deploy` pass no parameters at all, so without that line the update **silently deletes the running engine service**, as an ordinary stack update. The details are in "Upgrading: the model parameters are gone" in `PARAMETERS-60-engines.md`.

## Stage a model into S3 by hand

Getting a model into the catalogue takes two steps, separate on purpose.

1. **Stage**: put the file in S3. Normally you take it in from Admin → inference engines ([Take models into the inference engines](../engines/models.md)). The `run-task` below is the same thing by hand, for a deployment whose Control Plane cannot reach the internet
2. **Enable**: switch the row on in the same panel. A row arrives disabled whichever way it was staged: "the file is in the bucket" and "members may use it" are different facts

### Where the file goes

The keys follow ComfyUI's layout, so one tree serves every engine.

| Key | What it holds |
|---|---|
| `llm/<name>.gguf`, `llm/loras/` | llama.cpp models and LoRAs |
| `image/checkpoints/` | Single-file checkpoints (SDXL and the like) |
| `image/loras/`, `image/vae/`, `image/text_encoders/`, `image/diffusion_models/` | The parts of a split model |

The instance mirrors the bucket: `image/checkpoints/x.safetensors` becomes `/models/image/checkpoints/x.safetensors`. The key is the path, so there is no second name to keep in step.

> [!WARNING]
> An older deployment has `image/sd_xl_base_1.0.safetensors` at the top level. Move it with a server-side copy (no NAT, no re-download) and correct the catalogue row's key in the admin panel **in the same change**. The row points at the key verbatim, and a row whose file is not there fails in the fetch sidecar at the next cold start, not at deploy time.
>
> ```bash
> OLD=image/sd_xl_base_1.0.safetensors NEW=image/checkpoints/sd_xl_base_1.0.safetensors
> size() { aws s3api head-object --bucket "$BUCKET" --key "$1" --query ContentLength --output text; }
> aws s3 cp "s3://$BUCKET/$OLD" "s3://$BUCKET/$NEW" \
>   && new=$(size "$NEW") && old=$(size "$OLD") \
>   && [[ "$new" =~ ^[0-9]+$ ]] && [ "$new" = "$old" ] \
>   && aws s3 rm "s3://$BUCKET/$OLD"      # removed only after the copy succeeded and the sizes match
> ```
>
> The original is removed only when the copy succeeded and the copy has the same size; otherwise nothing is deleted.

### Run the ingest task

The engine never talks to Hugging Face. Models are staged into the stack's S3 bucket once, by a Fargate task, and the engine pulls from S3 at start.

```bash
# The sha256 is siblings[].lfs.sha256 from https://huggingface.co/api/models/<repo>?blobs=true
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition af-af-ecs-engines-ingest \
  --network-configuration 'awsvpcConfiguration={subnets=[subnet-…],securityGroups=[sg-…],assignPublicIp=DISABLED}' \
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF/resolve/main/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"}]}]}'
```

The `image` role is the same task with a different URL and key. SDXL's 6.94 GB took 161 s to fetch and verify, then 46 s to S3.

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors"},
        {"name":"SHA256","value":"31e35c80fc4829d14f90153f4c74cd59c90b779f6afe05a74cd6120b893f7e5b"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/sd_xl_base_1.0.safetensors"}]}]}'
```

A second checkpoint is the same call with another URL and key. Pick one that is Apache-2.0 or OpenRAIL, not gated, and fits an L4 without quantising.

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/<owner>/<repo>/resolve/main/<file>.safetensors"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/<file>.safetensors"}]}]}'
```

- A split model (FLUX.2 klein, Z-Image: a `--diffusion-model` plus text encoders plus a VAE) works too. Ingest the parts into `image/diffusion_models/`, `image/text_encoders/` and `image/vae/`
- **Override `environment` only, as above.** The containers have no `EntryPoint`; each `Command` is the script itself (`/opt/af/ingest-fetch.sh`, `/opt/af/ingest-upload.sh`). If you ever override `command`, it is an argv array (`["/opt/af/ingest-upload.sh"]`), not one shell string
- **The fetch time is unpredictable; always give the checksum.** Through the same NAT, Hugging Face delivered anywhere between 4 and 236 MB/s, and nothing about the file or the path predicted which. S3 ran at 104–147 MB/s on every attempt, which is why models are staged there. A truncated 17 GB GGUF still loads, and then answers nonsense

**A gated repository** (`gated: auto` on the Hugging Face API: FLUX.1-dev, SD 3.5) also needs the operator to have accepted the licence on Hugging Face and that account's token registered in the admin screen ([Take models into the inference engines](../engines/models.md#register-the-tokens)).

- The Control Plane keeps the token sealed in its own database and writes it into a secret this stack always creates. No CloudFormation round trip
- Only the **ingest task** reads the token; it never reaches an engine instance. The Control Plane cannot read back what it wrote (`PutSecretValue` without `GetSecretValue`)

## What members see

| Role | What they see |
|---|---|
| `llm` | Nothing to configure. The Agent asks the Control Plane which engines exist and writes them into opencode's config as a provider, so `llamacpp/<model>` appears in the launch picker, even while the GPU instance is asleep. Picking one and sending the first message starts the instance and holds the request until it answers; the answer arrives on the first attempt, with no resend |
| `image` | A provider of the existing `generate_image` tool, which is **off by default and turned on per member**. Once the engine exists, `generate_image` uses it ahead of the Codex route, and it can edit and inpaint. Nothing appears in the launch picker. A call to a stopped engine takes several minutes, and the tool sends a progress notification every 10 seconds while it waits, which keeps opencode's 60-second per-call ceiling from cutting it off |

`llama-server` runs as a router: every enabled model appears in the picker with its own context window. Two things are the administrator's to weigh:

- Every enabled GGUF is synced onto the instance, so the cold start grows with their total size (the panel estimates it per model)
- `LlmModelsMax` (default **1**) caps how many models are held at once. Under that cap a request for another model waits for the current answer to finish and then pays a full reload. The panel names the model in VRAM and counts the changes

## Things worth knowing

- **`DesiredCount` is absent from the template, deliberately**, for the same reason as `50-tts`: otherwise every stack update resets it and kills a GPU instance in the middle of an answer
- **The default capacity-provider strategy is empty and must stay empty.** With one in place, any service that does not spell out `LaunchType: FARGATE` lands on the GPU instance
- **A stopped engine keeps billing for several minutes.** The drain after `desired 0` took 427 and 463 seconds on a GPU instance (93 s on a CPU one). Shortening the idle window recovers window time only, never that
- **The cold start is minutes, not seconds.** From S3, image already in ECR: 527 s to a listening engine. 178 s of image pull and 179 s of S3 fetch run concurrently, then 267 s load 18.5 GB into VRAM. The Control Plane holds the request open across all of it, so a person sees a slow first answer rather than an error
- **The `image` role's idle window is half the `llm` role's (900 s):** an image is a request with no conversation around it
- **ComfyUI is checked on `/system_stats`, not `/health`.** It answers unauthenticated and immediately, which makes it a liveness check rather than a checkpoint-loaded one: `comfy` builds a workflow per request and uses the checkpoint that workflow names, so there is no single loaded checkpoint
- **Everything the controller does automatically is in the audit log** (`engine.llm.auto` / `engine.image.auto`, actor `engine-<role>-controller`)

## Tune the automatic start and stop

The Control Plane side takes exactly **one** parameter, `EnginesSsmParam`. `30-ingress` has little template budget left, so the engine table lives in SSM under `/af-ws/`, which is already the whole of `CpTaskRole`'s SSM read scope: no permission is added. Everything else is a Control Plane environment variable, and these templates set none of them.

| Variable | Default | What it does |
|---|---|---|
| `AF_ENGINE_WAKE_TIMEOUT` | 900 | How long one **streaming** request may be held open while the engine comes up. Above the 527 s cold start, with room for a slow pull |
| `AF_ENGINE_PLAIN_HOLD` | 45 | The same for a **non-streaming** request, which has no heartbeat and so cannot outlast the proxy in front. Keep it below the front end's idle timeout (`30-ingress` sets the ALB's to 60 s). Past it the answer is `503 engine_waking` with `Retry-After`, which the caller retries. Never exceeds `AF_ENGINE_WAKE_TIMEOUT` |
| `AF_ENGINE_LLM_CONTROL_INTERVAL_SEC` | 30 | How often the controller looks. **`0` switches it off entirely** |
| `AF_ENGINE_LLM_IDLE_SEC` | the stack's `LlmIdleSec` (1800) | Stop after this long with nobody asking. **`0` never stops**. Clamped to no less than the start deadline |
| `AF_ENGINE_LLM_START_DEADLINE_SEC` | the stack's `LlmStartDeadlineSec` (900) | A start that has not become `running` by then is treated as failed. **Keep it above the real cold start**: at 300 s every GPU start would be recorded as a failure and the cooldown would double away |
| `AF_ENGINE_LLM_FAIL_COOLDOWN_SEC` | 900 | How long a failed start blocks the next one, doubling per consecutive failure (capped at 16×) |
| `AF_ENGINE_LLM_WINDOW_SEC` | 300 | The rolling window demand is counted in. One request is one unit of demand: for inference the request is the intent, and there is no Polly to stand in while the engine starts |

- Every `AF_ENGINE_LLM_*` variable has an `AF_ENGINE_IMAGE_*` twin with the same meaning, taking its default from the `image` role's stack parameters (`ImageIdleSec` 900, `ImageStartDeadlineSec` 900)
- The middle of the name is the engine's key, so a third role would follow the same pattern
- `AF_ENGINE_WAKE_TIMEOUT` is shared by all of them

## Hold the first request

A cold engine takes minutes, and opencode gives up at 300 seconds, but on **silence**, not on elapsed time.

- The Control Plane answers a streaming request at once with `200 text/event-stream` and sends a heartbeat (an SSE comment line every 10 seconds) until the engine's first byte arrives
- **300 seconds is the maximum silence, not the maximum wait.** A 400-second wait produced the answer with no resend
- `503` with `Retry-After` is kept for non-streaming requests and genuine start failures only. A 503 spends one of the client's finite retries, and how many it has is not something this side knows

A non-streaming request cannot be held for 900 seconds, and the front end, not the Control Plane, decides that.

- The ALB's `idle_timeout.timeout_seconds` (60 in `30-ingress`) closes a connection that has carried no response byte for a minute. The first `generate_image` against a stopped `image` engine came back as a `503` at 60 seconds, because the engine needed 165 s
- So the non-streaming hold is folded below the front end, `AF_ENGINE_PLAIN_HOLD` (45 s), and answers `503 engine_waking` with `Retry-After`. The workspace's `comfy` provider keeps asking inside its own 16-minute budget, so one tool call still returns one picture
- **Behind a stricter proxy than this ALB, lower `AF_ENGINE_PLAIN_HOLD` to match.** The Control Plane has no way to ask what that timeout is

## Related

- [Run inference engines on your own GPUs](../engines/gpu.md)
- [Take models into the inference engines](../engines/models.md)
- [Check the inference engines' state and history](../engines/status.md)
- [Add Japanese read-aloud](tts.md)
- [Keep costs down](cost.md)

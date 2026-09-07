# 0071. Run self-hosted inference engines (llama.cpp / Stable Diffusion / ComfyUI) on demand, on GPUs — the GPU Fargate cannot sell, in the shape ADR 0070 established

English | [日本語](0071-self-hosted-inference-engines.ja.md)

- Status: **proposed — design only, nothing implemented** (2026-09-06). Every price below
  came from AWS's public price data (Tokyo, on-demand) on that date, every image and model
  figure from the registry and Hugging Face APIs on that date. What was measured in this
  container says so. Written to be reviewed before P0 is built; the Open questions are the
  parts that can still change the shape.
- Revised the next day (2026-09-07) with measurements: open questions 1–3 were measured and
  moved under *Resolved*, and decisions 5, 7 and 8 took their consequences. opencode cuts at
  300 s; Managed Instances start in 109 s and terminate in 93 s on a CPU box; the G-family
  quota was 0 in the test account (increase requested).
- Revised again the same day, once the quota was granted, after **running it end to end on a
  GPU** (g6.xlarge, L4): llama.cpp served Qwen3-Coder-30B-A3B and opencode drove it through
  real tool calls, and sd-server drew SDXL at 1024 px in 21 s. Resolved 4-6 were added and the
  evidence under decisions 2 and 3 was replaced with measurements.
- Later the same day the S3 path and ComfyUI were measured, adding Resolved 7-9. 🔴 **An
  earlier revision said "`-hf` is slow because of its downloader" — that was wrong**: the same
  file pulled with `curl` from Fargate came at 4.2 MB/s, so **what is slow is HF serving that
  file**. Decision 3's reason is now "HF is too slow for the start path, and 50x different from
  file to file" (Resolved 5 was rewritten; in the manner of the frozen journals, the gist of
  the wrong version is kept).
- The same day, a **pre-P0 review** was appended at the end ("Review (2026-09-07, before P0)").
  Two premises fell to the review's own measurements: 🔴 **opencode's 300 s is not a wall-clock
  wall but a cut after 300 s of a silent body — with an SSE comment line every 10 s a 400 s
  hold was answered within a single attempt** (decision 5 is proposed for replacement), and
  🔴 **8 vCPU of quota does build two g6.xlarge; what blocked the second was a draining box
  still holding its quota** (decision 2). Decision 3's corrected reason — "what is slow is HF
  serving that file" — fell to a third data point, the same SDXL at 39.6 MB/s from Fargate.
  The review proposes approval (P0 may start); changing the status line is left to the author.
- Related: [0070-tts-ondemand-engine.md](0070-tts-ondemand-engine.md) (the shape copied here:
  start on demand, stop on idle, Cloud Map names, a pure-function controller, shared cost) /
  [0069-image-generation-providers.md](0069-image-generation-providers.md) (the image
  provider abstraction; a self-hosted engine becomes its fourth layer) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (the slot pool's
  invariants — the reason not to put an engine there is the same as in 0070) /
  [0048-member-cloud-cost.md](0048-member-cloud-cost.md) (shared cost is shown, never
  apportioned) / [0041-cross-session-messaging.md](0041-cross-session-messaging.md) (the
  opt-in gate shape)

## Context

Three requests, all of them "an inference engine the fleet runs itself, usable from inside a
Workspace":

1. **Stable Diffusion**, as a provider behind `generate_image` (ADR 0069).
2. **llama.cpp**, as a model provider opencode can connect to.
3. **ComfyUI**, on the same box — including its UI.

Models are fetched from Hugging Face. ADR 0070 gave VOICEVOX the shape "build it when asked,
tear it down when the room goes quiet". That shape is copied here, but **its first decision
does not copy**. Decision 1 of 0070 puts the engine on Fargate, and **Fargate has no GPU**
(AWS's own FAQ; containers-roadmap #88 has been open since 2019). llama.cpp runs on CPUs, but
for a coding agent the prefill (ingesting a long context) is an order of magnitude slower
there, and 16 vCPUs cost the same as one L4 (Rejected below). So the centre of this ADR is
two questions — **how to obtain a GPU that scales to zero** and **where a 17 GB model is
read from on every start** — and the choice of engines is downstream of them.

### Measured (2026-09-06)

**Ways to get a GPU (Tokyo)**

- **Fargate has no GPU.** The alternatives are ECS on EC2 and **ECS Managed Instances** (MI
  below). MI has AWS own the instance, AMI, NVIDIA driver and patching, and terminates the
  instance once no task needs it. **Available in Tokyo** (one of the six GA regions). AWS
  published a "GPU inference with scale to zero" reference architecture in 2026-08; its own
  measurement is **about 13 minutes from job submission to first output with a 14 GB image**
  (including the 2-minute CloudWatch alarm and CUDA graph compilation), and scale-in is
  "queue empty for 5 minutes → desired 0".
- **On-demand prices (Linux, per hour)** and the **MI management fee (per hour)**, the latter
  after the 35 % G-series cut of 2026-07-01.

  | Instance | vCPU / RAM | GPU | EC2 $/h | MI fee $/h | Total $/h |
  |---|---|---|---|---|---|
  | g6f.large | 2 / 8 GiB | 1/8 of an L4 (≈3 GB) | 0.293 | 0.023 | 0.316 |
  | g6f.xlarge | 4 / 16 GiB | 1/8 of an L4 (≈3 GB) | 0.344 | 0.027 | 0.371 |
  | g6f.2xlarge | 8 / 32 GiB | 1/4 of an L4 (≈6 GB) | 0.689 | 0.054 | 0.743 |
  | g6f.4xlarge | 16 / 64 GiB | 1/2 of an L4 (≈12 GB) | — | 0.107 | — |
  | g4dn.xlarge | 4 / 16 GiB | T4 16 GB | 0.710 | 0.055 | 0.765 |
  | **g6.xlarge** | 4 / 16 GiB | **L4 24 GB** | **1.167** | **0.091** | **1.258** |
  | g6.2xlarge | 8 / 32 GiB | L4 24 GB | 1.418 | — | — |
  | g5.xlarge | 4 / 16 GiB | A10G 24 GB | 1.459 | 0.114 | 1.573 |
  | g6e.xlarge | 4 / 32 GiB | L40S 48 GB | 2.699 | — | — |
  | c8g.4xlarge (CPU, for comparison) | 16 / 32 GiB | none | 0.800 | 0.096 | 0.896 |
  | Fargate 16 vCPU / 32 GiB (comparison) | | none | x86 0.986 / ARM 0.789 | — | — |

  (The g6f GPU fractions are third-party compilations; P0 takes
  `describe-instance-types` → `GpuInfo.Gpus[].MemoryInfo` as the truth.)
- **Storage (Tokyo, per GB-month)**: S3 Standard **$0.025**, EFS Standard **$0.36** (IA
  $0.0272 plus $0.012/GB read), EBS gp3 **$0.096** (third-party transcription; re-check). NAT
  data processing is **$0.062/GB**. `00-network`'s S3 gateway endpoint is free, and S3 traffic
  does not cross the NAT.

**Images (compressed size, registry API)**

| Image | Size | Arch | Note |
|---|---|---|---|
| `ghcr.io/ggml-org/llama.cpp:server-cuda` | **2.47 GB** | amd64 / arm64 | official |
| `ghcr.io/ggml-org/llama.cpp:server` (CPU) | 297 MB | amd64 / arm64 / s390x | official |
| `ghcr.io/leejet/stable-diffusion.cpp:master-cuda` | **2.31 GB** | **amd64 only** | official, ships `sd-server` |
| ComfyUI | **no official image** | | `lecode-official/comfyui-docker:latest` 5.1 GB, `yanwk/comfyui-boot` 3.9–10.8 GB |

**Models (Hugging Face API, `blobs=true`)**

| Use | Repository | File | Size | Gate |
|---|---|---|---|---|
| coding LLM | `unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF` | `…-Q4_K_M.gguf` | **17.3 GiB** | none |
| LLM (small) | `ggml-org/gpt-oss-20b-GGUF` | `gpt-oss-20b-MXFP4.gguf` | 11.3 GiB | none |
| LLM (small) | `bartowski/Meta-Llama-3.1-8B-Instruct-GGUF` | `…-Q4_K_M.gguf` | 4.6 GiB | none |
| image | `stabilityai/stable-diffusion-xl-base-1.0` | `sd_xl_base_1.0.safetensors` | **6.5 GiB** | none |
| image (small) | `stable-diffusion-v1-5/stable-diffusion-v1-5` | `v1-5-pruned-emaonly.safetensors` | 4.0 GiB | none |
| image | `black-forest-labs/FLUX.1-schnell` | `flux1-schnell.safetensors` | 22.2 GiB | **auto** (consent + token) |
| image | `stabilityai/stable-diffusion-3.5-medium` | `sd3.5_medium.safetensors` | 4.8 GiB | **auto** |

**The engines' surfaces (their READMEs, 2026-09-06)**

- `llama-server`: `-hf <repo>[:quant]` downloads straight from HF (cache in `LLAMA_CACHE`,
  gated repos via `HF_TOKEN`). `--api-key`. A **router mode** (`--models-dir`, models loaded
  on request, `--models-max` concurrent). Endpoints `/v1/chat/completions`, `/v1/responses`,
  **`/v1/messages` (Anthropic-compatible, merged 2026-01)**, `/health`.
- `sd-server` (stable-diffusion.cpp): **OpenAI-compatible `/v1/images/generations` and
  `/v1/images/edits` (with `mask`)**, A1111-compatible `/sdapi/v1/txt2img` / `img2img`, and a
  native async job API (`/sdcpp/v1/img_gen` → `/sdcpp/v1/jobs/{id}`). Reads SD1.x / SDXL /
  SD3 / FLUX **as safetensors**, `--offload-to-cpu` to save VRAM. Models are chosen by flags
  at start; the README says nothing about switching at runtime.
- ComfyUI: `/prompt` (workflow JSON), `/history/{id}`, `/view`, WebSocket `/ws`. `--listen` to
  expose it. No authentication. Model layout
  `models/{checkpoints,diffusion_models,text_encoders,vae,loras}`.

**opencode (measured in this container, 1.18.29)**: with
`provider.llamacpp = { npm: "@ai-sdk/openai-compatible", options.baseURL, models }` in an
`opencode.json` pointed at by `OPENCODE_CONFIG`, `opencode models` **listed
`llamacpp/qwen3-coder-30b-a3b`**. The Agent's `models.go` builds the launch menu from that
same command, so **nothing in the backend has to change for the model to appear**. The key
can come from an environment variable via `apiKey: "{env:…}"`.

### What the code says today

- **Workspace egress goes through the CP's egress proxy** (`main.go` injects `http_proxy`;
  `no_proxy` is loopback only). A direct connection to a VPC-internal name still goes via the
  proxy, and under enforce it is blocked unless allow-listed. The Workspace already has a
  route to the CP: `AF_CP_BASE_URL`, the git credential helper (`cred_helper.go`), `/mcp`.
- Workspace tasks are awsvpc with `WsSg` (`AF_ECS_SECURITY_GROUP`) on Fargate and on the EC2
  pool alike.
- The CP has a precedent for a **WebSocket-capable `httputil.ReverseProxy`**
  (`preview_host_serve.go`).
- `tts_ecs.go`'s `state()` / `setEnabled()` (desired 0↔1) and 0070's design (demand clock,
  pure function, failure cooldown, `DescribeServices` cache) do not depend on there being one
  engine.
- MCP tool-call limits differ per kind (0069, resolved question 1): claude effectively
  unbounded, codex `tool_timeout_sec` (af writes 600 s), opencode resets on progress.
- `30-ingress.yaml` is **41,988 bytes**; **9.2 KB** remain under the 51,200 wall.

## Decisions

1. **Buy the GPU through ECS Managed Instances. No self-managed EC2. Never the slot pool.**
   With no GPU on Fargate, 0070's decision 1 is re-read as "**a placement where no instance
   exists while no task does**". MI guarantees that (zero tasks → instance terminated).
   Self-managed EC2 stop/start is rejected (below): owning AMIs and NVIDIA drivers, EBS billed
   while stopped, and care on every path to keep the box outside ADR 0045's sweeps
   (`af-role=slot`) and `Ec2MaxSlots`. The MI capacity providers are **added** to the cluster
   and **`defaultCapacityProviderStrategy` is never set** — the moment it is, a service that
   forgot `LaunchType` lands on the GPU box (the mirror image of 0070 decision 1's "one missing
   line is the whole failure"). The slot pool keeps `LaunchType: EC2` and is untouched.

2. **Two engine roles (`llm`, `image`), one capacity provider each, never co-located.** `llm`
   needs VRAM ≥ 20 GB (Qwen3-Coder-30B-A3B Q4 is 17.3 GiB plus KV cache); `image` needs
   VRAM ≥ 8 GB (SDXL fp16 measured at 7.4 GB — **g6f.2xlarge's 6 GB does not fit it**,
   Resolved 6). Both default to **g6.xlarge (L4 24 GB, $1.26/h all-in)**; the floor for a
   cheaper `image` role is g6f.4xlarge (12 GB), not g6f.2xlarge. **The instance
   requirement is a stack parameter per role** (declared, never inferred — ADR 0053). Not
   sharing a box: running out of VRAM under CUDA does not slow down, it **crashes**. Measured,
   the 30B Q4 takes **20.9 GB** and SDXL **7.4 GB**, so both do not fit one L4's 23 GB. Most of
   the time only one role is awake, so splitting costs almost nothing extra. **But one box per
   role means a deployment that wakes both needs quota for two (16 vCPU)** — at 8 vCPU only one
   g6.xlarge exists, and the second role sat in `VcpuLimitExceeded` for 408 s until the first
   one terminated (Resolved 6). ComfyUI is **a second
   engine of the `image` role** (decision 6) and never runs alongside sd-server (same VRAM).

3. **Models go HF → S3 once, and S3 → local disk on every start. HF is never on the start
   path.** Measurement moved this decision's reason from cost to **speed and unpredictability**
   (Resolved 5): behind the same NAT, HF served SDXL (stabilityai) at **236 MB/s** but the
   Qwen3-Coder GGUF (unsloth) at **9.6 MB/s (31 minutes)** through `llama-server -hf` and at
   **4.2 MB/s (74 minutes)** through `curl` on Fargate. **A 50x difference between files, and
   which one you get is unknown until you pull.** That is not a speed the start path can be
   built on. S3 delivers to the same box at a steady **147 MB/s** (Resolved 8). The cost ($1.05
   of NAT per 17 GB) and keeping a gate token off the node are two further reasons on top of
   it. S3 storage is
   $0.025/GB-month ($2.5/month for a 100 GB catalogue) and reads through the gateway endpoint
   are free. **Ingestion is an async job** (a Fargate CPU task: `huggingface-cli download` →
   `aws s3 cp`) that an admin starts with an HF repo id and file list, and it reports success
   **only after matching the sha256 from HF's LFS metadata** — "a file appeared" is never taken
   as success. `HF_TOKEN` is an **operator secret** read only by the ingestion task (a Secrets
   Manager reference). An engine `aws s3 sync`s its role's prefix onto the MI EBS
   (`storageConfiguration`, size a parameter) at start, and `/health` turns ok only after that.
   EFS is rejected (below).

4. **Workspaces never reach an engine directly. They go through a CP gateway.** The CP grows
   `/engine/llm/v1/*` and `/engine/image/v1/*`, authenticated with the Workspace-originated
   credential `/mcp` and `/git/*` already use (and exempt from the tenant source-IP rule for
   the same reason). The engine security group admits **the CP's SG only** (0070 decision 14).
   Four reasons: (a) no `no_proxy` edit and no allow-list growth — the Workspace→CP route
   exists today; (b) **wake-and-hold** (decision 5) needs a place to hold the request, and only
   the CP can; (c) the CP can count who used how much from the responses' `usage`
   (decision 9); (d) llama-server, sd-server and ComfyUI all **expose mutating endpoints with
   no authentication** — reachability is their whole defence, and Workspaces do not belong
   next to that. `llama-server --api-key` with a CP-only key doubles it. The opencode provider
   is `baseURL = $AF_CP_BASE_URL/engine/llm/v1`, `apiKey = {env:…}`, with the Agent injecting
   the env at launch (where `auth.go`'s `env()` already does).

5. **Demand is the request itself, and the first request is held by the CP until the engine
   is up (wake-and-hold).** Unlike 0070 there is **no Polly to read meanwhile**, so intent and
   outcome need not be separated — the arrival of a request is the intent. A request to an
   engine at desired 0 makes the CP set desired 1, **hold the connection** until `/health` is
   ok, then forward. Past `AF_ENGINE_WAKE_TIMEOUT` (default 600 s) it answers 503 with
   `Retry-After` and a human-readable body (the model sees it too). The image MCP tool emits
   `notifications/progress` every 10 s meanwhile (the existing trick that lifts opencode's 60 s).
   **opencode drops the connection at 300.1 s and re-sends the same request a few seconds
   later** (Resolved 1). So the gateway cuts each attempt at **290 s** with a 503, drives the
   wake independently of the attempt, and lets the re-sent request through to the warm
   engine. The same 300 s also bounds **time to first token** — even with the engine up,
   a prefill longer than 300 s is cut by opencode (one more reason CPUs are rejected).
   "One request buys a 30-minute window" is the opt-in of the person
   who chose that provider or model; there is no break-even analogue to 0070 (the alternative is
   simply "not available").

6. **Three engines — llama.cpp, stable-diffusion.cpp, ComfyUI. The first two are official
   images copied into ECR; ComfyUI is baked by the fleet.** Why these:
   - `llama-server`: an official CUDA image, and beyond OpenAI compatibility it has
     `/v1/responses` and `/v1/messages`, so **the road to codex and claude stays open** (P3).
     Router mode loads several models per request.
   - `sd-server`: an official 2.3 GB CUDA image whose OpenAI-compatible `generations` /
     `edits` (mask) **map one-to-one onto 0069's `Op` generate / edit / inpaint**. It reads
     safetensors unconverted, so the S3 catalogue is shared with ComfyUI.
   - ComfyUI: no official image, so the fleet keeps **a Dockerfile pinned to a ComfyUI tag**,
     in the same manner as `workspace/` (the community images are 4–11 GB and the fleet cannot
     vouch for what is inside). **ComfyUI-Manager is not installed** — arbitrary custom nodes
     are arbitrary code on the fleet's box. The UI is served through a CP reverse proxy
     (`/engine/comfy/`, WebSocket included, the `preview_host_serve.go` shape) into a Console
     pane. The `generate_image` provider `comfy` fills a fleet-owned workflow JSON template with
     prompt / size / seed, posts it to `/prompt` and waits on `/history`.
   - All three read one **model-layout convention** (ComfyUI's `models/…` layout is canonical);
     sd-server's flags point at files inside it.

7. **One controller — `tts_control.go` (0070 P1: `ttsDemand` / `decideEngineAction` /
   `ttsController`) generalised to N engines, with VOICEVOX as one of them.**
   `decideEngineAction` stays a pure function and takes an engine table (service name, URL,
   health URL, idle window, start deadline, cooldown). Demand clocks persist in `SettingsStore`
   (once a minute), `DescribeServices` gets a short TTL cache, start failures are diagnosed from
   `events[]` and cool down, and the idle window applies during `starting` too (0070
   decisions 5, 6, 9, 10). **One MI-specific addition**: after desired goes to 0 the instance
   lingers until MI scales it in, and the fee and EC2 price run until then. The state becomes
   `running | starting | stopped | draining`; `draining` is **93 s** on a CPU box and
   **427 s and 463 s** on GPU boxes (Resolved 4). Modes
   are 0070 decision 7's `off / on / ondemand`, per engine.

8. **The stack is `60-engines.yaml` (optional). What `30-ingress` receives is one SSM parameter
   name.** In `50-tts`'s shape it imports `00-network` and `20-platform` and does not depend on
   `30-ingress`. Contents: two capacity providers (attribute-based selection), the MI
   infrastructure role and instance profile, an S3 bucket (models), the ingestion task
   definition, three engine task definitions and services (**no `DesiredCount`** — 0070
   resolved point 2), three Cloud Map A records, SGs, logs. **There is no room in `30-ingress`
   for two parameters per engine** (six parameters plus conditions plus env come to nearly 3 KB
   of the 9.2 KB left). Instead `60-engines` writes one JSON of service names and URLs to SSM
   and `30-ingress` passes a single `AF_ENGINES_SSM_PARAM`; the CP reads it at boot (SSM reads
   are already in the CP task role). **New IAM is required** — the MI infrastructure role, the
   instance profile, S3 read for the engines, S3 write plus secret read for ingestion. Three
   contracts learned by measuring: a capacity provider is **cluster-scoped** and `ClusterName`
   is mandatory (without it: "The cluster provided is invalid"); the managed policy
   `AmazonECSInfrastructureRolePolicyForManagedInstances` grants `iam:PassRole` **only on roles
   named `ecsInstanceRole*`** (a fleet-named role needs an explicit PassRole on the
   infrastructure role); and `ClusterCapacityProviderAssociations` **replaces the cluster's
   list** and requires `DefaultCapacityProviderStrategy` (pass the full list including
   FARGATE / FARGATE_SPOT, and an **empty** strategy). 0070's "zero new IAM" does not hold here.

9. **Usage is counted; cost is shown as shared.** For llm the gateway records tokens from the
   responses' `usage`, for image the count and pixels, per member (`usagex` gains `engine.llm`
   and a per-provider `tool.imagegen`; ADR 0029's enumeration is appended). No unit price is
   attached (a token here has no dollar value). Instance hours carry the cost-allocation tags
   `af-role=engine-llm` / `engine-image` and are **shown as component cost, never apportioned**
   (ADR 0048).

10. **Provenance is part of the result (0069 decision 11).** Providers are `llamacpp` / `sdcpp`
    / `comfy`; the model is **the file name and its sha256** (the ingestion job also keeps the
    HF repo id and revision). The prompt does leave the container — to a fleet-owned box, and
    that is recorded too.

11. **The licence is a catalogue attribute the operator accepts at ingestion.** SDXL is
    CreativeML OpenRAIL++-M, Llama is the Llama licence, FLUX.1-schnell is Apache-2.0 while
    FLUX.1-dev is non-commercial, Qwen is Apache-2.0. The ingestion job copies HF's
    `cardData.license` into the catalogue, and **gated repos (`gated: auto`) pass only after
    the operator has consented on HF** — the job learns that from a 401/403 and fails with
    those words. The same kind of question as 0070's open question 1: in a multi-tenant
    deployment the operator is not the user.

12. **x86_64 only.** `stable-diffusion.cpp`'s CUDA image is amd64-only and there is no arm64 in
    the G family; the branch 0070 decision 11 kept open ("arm64 after measuring") does not exist
    here.

## Resolved by measurement (2026-09-07)

1. **opencode cuts at 300.1 s and re-sends a few seconds later.** opencode 1.18.29 in this
   container was pointed, through an `@ai-sdk/openai-compatible` provider, at an
   OpenAI-compatible stub that merely holds the response for N seconds. A 150 s hold succeeded
   (both requests — the tools=0 title request and the tools=21 main one — answered, exit 0).
   400 s and 700 s holds were **reset at 300.1 s**, the same request (tools=21) was **re-sent
   3–5 s later**, and that repeated **four times** until the 1200 s cut-off. The consequences
   are in decision 5: 503 at 290 s per attempt, the wake decoupled from the attempt, 300 s to
   first token.
2. **Managed Instances cold start is 109 s on a CPU box, drain 93 s.** A throwaway stack
   (`deploy/aws/ecs/harness/engprobe.yaml`, driven by `probe-managed-instances.sh`) on the
   shared cluster of the test account (`af-sandbox`) ran the 297 MB CPU llama.cpp image on a
   c6a.large (AMI `ecs-managed-instances-standard-x86_64-20260827`), fetching a 1.1 GB model
   from HF at start. From desired 1: **task at +6 s, instance launched at +10 s, pull started
   at +32 s, RUNNING at +68 s (35 s pull), model loaded and listening at +109 s**. From
   desired 0: **tasks gone at +10 s, shutting-down at +79 s, terminated at +93 s**. The 13
   minutes in AWS's reference come from its 2-minute alarm and 14 GB image. The GPU box
   (2.3 GB CUDA image, 17 GB model) is unmeasured — see 3.
3. **The G-family vCPU quota was 0 in the test account.** The increase request (8 vCPU) was
   not auto-approved and became a **support case** (`CASE_OPENED`). It is written into the
   README as a stand-up precondition, and stand-up says so when it is 0. GPU numbers (prefill,
   pull, S3 → local, load into VRAM) come from the same harness once approved.

4. **Cold start and drain on a GPU (g6.xlarge, L4 24 GB).** The AMI is
   `ecs-managed-instances-nvidia-x86_64-20260827` — NVIDIA drivers included, nothing to bake.
   From desired 1: **instance at +15 s, pull started at +43 s, pull done at +214 s (171 s for
   the 2.47 GB `server-cuda`), task RUNNING at +232 s** — the same shape as the CPU box. What
   follows is the problem: **1,846 s to fetch the model and 272 s to load it into VRAM, 2,351 s
   (39 minutes) in total**, which is what Resolved 5 is about. **Drain took 427 s and 463 s**,
   four to five times the CPU box's 93 s: the g6.xlarge and its management fee keep running for
   7-8 minutes after desired 0, so trimming the idle window can only ever recover that much
   ($0.15 of drain against $0.65 for a 30-minute window). On the same box sd-server went from
   **task created to listening in 195 s** (135 s pull plus a 28 s model fetch).
5. 🔴 **HF's delivery speed differs 50x between files, and the Qwen3-Coder GGUF is slow
   whatever pulls it.** Behind the same NAT, `llama-server -hf` pulled 18.5 GB at **9.6 MB/s
   (1,846 s)** while `curl` pulled SDXL's 6.9 GB at **236 MB/s (28 s)**. From those two an
   earlier revision concluded "the 24x is the downloader"; but **the same GGUF pulled with
   `curl` from Fargate came at 4.2 MB/s (4,467 s)**, slower still. What is slow is not the
   client but **HF serving that file (the unsloth repository)**; stabilityai's SDXL was fast
   only because that file happens to be served that way. Decision 3 was originally a cost
   argument ("do not pay $1 and 9 minutes every start"); it is really that **a start takes 39
   to 74 minutes or 4, and you do not know which until you pull**. The design (sync from S3)
   does not change, but the reason does. The wrong version is kept for the next person tempted
   to draw the same conclusion from the same two points.
6. **VRAM and the G-family quota.** Qwen3-Coder-30B-A3B Q4_K_M used **20,943 MiB** and SDXL
   fp16 **7,379 MiB** (6,624 MB of params). So **two roles do not fit on one L4**, and dropping
   `image` to a g6f.2xlarge (6 GB) is not an option (half the answer to the old open question).
   And **8 vCPU of quota only builds one g6.xlarge**: starting `image` while `llm` was up
   produced repeated `VcpuLimitExceeded: your current vCPU limit of 8`, and placement succeeded
   **408 s later**, after the first box terminated. A deployment that wakes both roles asks for
   16 vCPU.

7. **ComfyUI draws SDXL in 8 s on the same L4, at the same 6.9 GB of VRAM as sd-server.** The
   community image `lecode-official/comfyui-docker:latest` (5.1 GB, ComfyUI 0.8.2) served as
   the measuring stick, with SDXL fetched from S3 into `models/checkpoints` and a
   `/prompt` → `/history` → `/view` round trip. From desired 1 it **listened at +504 s**, of
   which **437 s was the 5.1 GB pull (12 MB/s)** — GHCR through the NAT ran at 14 MB/s for
   `server-cuda` too, so **unless the images are copied into ECR the pull is most of the cold
   start**. The first workflow took 42.6 s (model load included); **warm, 1024 px at 20 steps
   took 7.9 s and 8.3 s** — **2.5x faster** than sd-server's 20.8 s. VRAM 6.9 GB. The picture
   was visibly right. The bundled ComfyUI-Manager failed to import (decision 6 leaves it out
   anyway). Re-reading decision 6's order (P1 sd-server, P2 ComfyUI) against the numbers:
   sd-server's advantages are **the smaller image (2.3 GB vs 5.1 GB) and the OpenAI-compatible
   surface**, ComfyUI's are **per-image speed and workflow freedom**; since cost is set by the
   window (decision 5), 13 s per image never reaches the bill. The order stands, but a
   deployment that draws in volume may end up on ComfyUI.
8. **S3 → local disk is a steady 104-147 MB/s (`aws s3 cp` default parallelism).** 6.9 GB in
   45 s, a synthetic 20.8 GB in 198 s, the real 18.5 GB GGUF in 179 s. Slower than HF's fast
   case (236 MB/s) but off the NAT, 10-25x HF's slow case (4-10 MB/s), and **independent of
   the file**.
9. **A production-shaped llm cold start from S3 is 527 s (8.8 minutes).** From desired 1:
   **instance at +8 s, S3 fetch started at +64 s, pull done at +224 s (178 s), the 18.5 GB
   fetched at +243 s (179 s) — the fetch and the image pull run concurrently — llama-server
   started at +260 s, loaded and listening at +527 s** (267 s to load into VRAM). One 4.5th of
   the 2,351 s straight from HF. What remains large is **the 178 s pull (shrinks with the ECR
   copy)** and **the 267 s load (reading from gp3 EBS; local NVMe or a provisioned-throughput
   gp3 may shrink it)** — the S3 fetch is no longer the dominant leg. Consequence for
   decision 5: the first llm request **always crosses the 300 s wall once** (the engine warms
   while opencode re-sends once or twice). The image role on sd-server, at 195 s, fits inside
   the first attempt.

Also measured the same day:

- **The HF API returns what the verification needs.** `?blobs=true` gives
  `siblings[].lfs.sha256` and `cardData.license` (SDXL `openrail++`, FLUX.1-schnell
  `apache-2.0` with `gated: auto`). A 1.1 GB GGUF downloaded and `sha256sum`ed matched the API
  value — decision 3's check is written against those two fields.
- **llama-server (CPU, 1.1 GB model)**: 3 s from start to `/health` ok, `/v1/messages`
  answers 200, a request without `--api-key` gets 401. opencode passed the key through
  `apiKey: "{env:AF_ENGINE_TOKEN}"` and streamed a reply end to end. **But an opencode request
  is 18.7k tokens with the fleet's AGENTS.md, 7.3k even minimal**, and prefill in this
  container (8 vCPU, shared) runs at **18–23 tok/s** for a 1.5B Q4 — 17 minutes for 18.7k
  tokens. A 0.5B Q8 with the minimal config finished in 170 s but emitted the tool call as JSON
  **text** (a limit of the model, not the path). **On the GPU with the real model it worked** —
  below.
- ✅ **opencode → llama.cpp (L4, Qwen3-Coder-30B-A3B Q4) completed a real tool-calling turn.**
  With an SSM port forward standing in for the CP gateway, `opencode run --auto` was asked to
  create hello.txt, read it back and report its size. **In 11 s it called `write` and `bash`,
  the file existed (`hello fleet`, 11 bytes) and the answer said "11 bytes".** Speed:
  **prefill of 23,226 tokens in 11.85 s = 1,960 tok/s** (peak 2,523), **12.1 s to first
  token**, generation at **67.6 tok/s**. The same prefill ran at 18-23 tok/s on CPUs, so this
  is **85-100x**, and 25x of headroom against opencode's 300 s wall.
- ✅ **sd-server (L4, SDXL fp16, 6.9 GB)**: `/v1/images/generations` took **7.8 s at 512 px**
  and **20.8 s and 21.0 s at 1024 px** (default steps); `/v1/images/edits` with a mask took
  **6.4 s at 512 px**. The 1024 px output was visibly a correct picture (the CPU's 4-step
  output was noise). That is **28x the CPU** at 1024 px. ⚠️ The native async job API
  `/sdcpp/v1/img_gen` fails inside the container with
  **`filesystem error: /proc/1/map_files … Operation not permitted`** while the
  OpenAI-compatible surface works — one more reason decision 6 takes the latter.
- **sd-server (CPU, SD1.5 Q4_0, 1.67 GB)**: `/v1/images/generations` took **98 s** at 256 px /
  4 steps and **587 s** at 512 px / 4 steps; `/v1/images/edits` (image + mask) took **307 s** at
  256 px (51 s of it VAE decode). Responses are `data[].b64_json`. The premise that images need
  a GPU is now a number, taken together with the API contract.

## Open questions — to settle before P0 is written

1. **Can the 267 s load into VRAM be cut.** With S3 the fetch is 179 s and the largest leg left
   is reading 18.5 GB off gp3 EBS. P0 measures MI's `LocalStorageConfiguration` (g6's local
   NVMe) and a provisioned-throughput gp3.
2. **Reuse the Workspace credential or mint an engine token.** Reusing the `/git/*` PAT gives
   per-member accounting but not per-session. If per-session is wanted, the Agent mints a
   short-lived token at launch.

## Rejected

- **llama.cpp on CPUs (Fargate 16 vCPU / c8g.4xlarge).** Same price as one L4 ($0.79–0.99/h
  versus $1.26/h); MoE generation (3B active) would be usable, but **a coding agent's 20k-token
  prefill takes minutes on a CPU**. Not cheaper, and slow. The 297 MB CPU image does not change
  that. The measurements back it: 18-23 tok/s of prefill for a 1.5B model in this container
  against **1,960 tok/s for a 30B on one L4**, 18.7k tokens per opencode request, and opencode
  cutting at 300 s — on a CPU the first token never arrives in time.
- **A self-managed GPU EC2 the CP stops and starts.** The slot pool's `StartInstances` /
  `StopInstances` tooling is reusable (and a 110 s resume is measured), but the fleet would own
  the AMI and NVIDIA driver, EBS runs while stopped (200 GB = $19/month), and every path needs
  care to stay outside ADR 0045's sweeps and `Ec2MaxSlots`. And g6's local NVMe is wiped on
  stop, so only the image layers stay warm. **Kept as the fallback** for a deployment in a
  region without MI.
- **Co-locating the engines on one box.** Saves at most $1.26/h; VRAM contention shows up as a
  crash (decision 2).
- **Models on EFS (reusing 10-data).** Mount-and-go with no sync step is attractive, but
  Standard is $0.36/GB-month ($36/month for 100 GB, 14× S3), IA still charges $0.012/GB read
  ($0.20 per 17 GB load), and the throughput is NFS throughput — getting 17 GB into VRAM takes
  an order of magnitude longer than from local NVMe. Revisit if the sync step hurts in P0.
- **Workspaces connecting to the engine directly (Cloud Map name plus `WsSg` in the ingress).**
  Shortest path, but it needs the `no_proxy` change in `main.go`, an allow-list entry under
  enforce, opens unauthenticated engines to Workspaces, and has nowhere to wake-and-hold or to
  count usage — all four of decision 4's reasons missing.
- **Baking models into the image (what AWS's reference recommends).** Less cold-start
  variance, but every added model means baking a 20 GB+ image into ECR and the pull becomes the
  dominant start cost. It also contradicts "fetch from HF and run".
- **Using a community ComfyUI image as is.** The fleet cannot vouch for the contents, and the
  ones bundling ComfyUI-Manager are an arbitrary-code entry point (decision 6).
- **`llama-server -hf` straight from HF.** $1 and 9 minutes through the NAT on every start, and
  the gate `HF_TOKEN` lands on the engine box (decision 3).
- **Lambda / SageMaker Serverless / Bedrock custom models.** A 17 GB GGUF and the ComfyUI UI
  are not functions, and Bedrock already exists as ADR 0069's first layer (a different request
  from "run it ourselves").

## Phases

- **P0 — the substrate, and llm.** `60-engines.yaml` (capacity providers, S3, the ingestion
  task, the llm service), stand-up / update / teardown / capture-env, copying the official
  images into ECR, the CP gateway `/engine/llm/v1/*` (wake-and-hold, streaming, usage
  recording), the generalised controller, the opencode provider injection. Done means:
  **`llamacpp/<model>` appears in opencode's launch menu, the first request to a stopped
  engine answers minutes later, and the instance is gone after 30 quiet minutes.** This yields
  the numbers for open questions 1–3. llm goes first not because the request led with images
  but because **it validates the substrate with the least application code** (zero changes on
  the opencode side).
- **P1 — image (sd-server).** The `image` role's service, the Agent provider `sdcpp` (transport
  via the CP gateway — 0069 decision 3's "the tenant layer goes through the CP"), `Caps` per
  (provider, model file), generate / edit / inpaint, progress notifications. Open question 4.
- **P2 — ComfyUI.** The fleet's image, the `/engine/comfy/` pane, the `comfy` provider with its
  workflow template, mutual exclusion with sd-server.
- **P3 — llm for codex and claude.** codex via `model_providers` with `base_url` and
  `wire_api = "responses"`, claude via `ANTHROPIC_BASE_URL`. Both **change what choosing a
  model means** (billing moves from the user's login to the fleet's box), so the launch menu's
  presentation and consent are designed first. llama.cpp's `/v1/messages` has a known issue
  dropping thinking blocks (#20090), and Claude Code sends many background Haiku requests.
- **P4 — cheaper, with evidence.** MI Spot, shrinking `image` to **g6f.4xlarge** (the measured
  floor is 12 GB; SDXL does not fit a g6f.2xlarge), several models through
  router mode, and image slimming only if P0's numbers say the pull dominates.

## Sources checked (2026-09-06)

- AWS public price data: the EC2 on-demand meteredUnitMaps (Tokyo, Linux), the `AmazonECS`
  offer for ap-northeast-1 (`APN1-ECS-Managed-Instances:<type>-management-hours`), the
  `AmazonEFS` / `AmazonS3` offers for ap-northeast-1. EBS gp3 and NAT rates are third-party
  transcriptions.
- The AWS Fargate FAQ (no GPU), containers-roadmap #88, the ECS Managed Instances GA
  announcement (regions), the 2026-07 GPU management-fee change, the 2026-08 "GPU batch
  inference … with scale to zero" blog (13 minutes, 5-minute scale-in, bake-into-image advice).
- Registry APIs on GHCR / Docker Hub: manifests of `ggml-org/llama.cpp`,
  `leejet/stable-diffusion.cpp`, `lecode-official/comfyui-docker`, `yanwk/comfyui-boot`.
- The Hugging Face API (`?blobs=true`): sizes and gates in the model table above.
- `llama.cpp` `tools/server/README.md`, `stable-diffusion.cpp` `examples/server/api.md` and
  README, ggml-org's "Anthropic Messages API in llama.cpp", `opencode.ai/docs/providers`.
- Measured in this container: a provider pointed at by `OPENCODE_CONFIG` appearing in
  `opencode models` (opencode 1.18.29); opencode's cut-off and re-send against a delaying
  stub; the llama.cpp `b10825` Linux x64 build and the `stable-diffusion.cpp` `master-841`
  Linux x86_64 build run on CPUs; the sha256 check of a GGUF fetched from HF.
- Measured in the `af-sandbox` test account: `deploy/aws/ecs/harness/engprobe.yaml` and
  `probe-managed-instances.sh` (Managed Instances start and drain, the IAM and capacity
  provider contracts, llama.cpp and sd-server end to end on a GPU), `service-quotas` for
  L-DB2E81BA, and SSM port forwarding (`AWS-StartPortForwardingSession` against an ECS Exec
  target). The GPU measurements cost about an hour of g6.xlarge plus 25.4 GB of NAT, ≈$3.
- This repository: `control-plane/main.go` (proxy env), `egress_policy.go`,
  `preview_host_serve.go`, `tts_ecs.go`, `internal/runtime/runtime_ecs.go` (awsvpc, `WsSg`),
  `workspace/agent/internal/imagegen/imagegen.go`,
  `internal/agents/opencode/{auth,models}.go`,
  `deploy/aws/ecs/cfn/{00-network,20-platform,30-ingress,50-tts}.yaml`.

## Review (2026-09-07, before P0)

In the manner of 0070's review: does each decision follow from its evidence, and is any
conclusion drawn that the measurements cannot carry? The verdict first: **approval (P0 may
start) is proposed, on the condition that decisions 5 and 2 are rewritten as proposed below
before any P0 code is written — the review's own measurements overturned their premises.** The
"generalise from two points" error the author already corrected once appears **twice more**:
in the corrected version of decision 3's reason and in decision 2's 8 vCPU sentence. Both were
refuted by data that already existed (the CloudWatch logs and CloudTrail) — no re-measurement
was needed, only a re-reading.

### What the review measured and checked

- **R1. opencode's 300 s is a silence limit, not a wall.** opencode 1.18.29 in this container
  (a Bun binary — the strings carry `BUN_1.2`) was pointed at an OpenAI-compatible stub with
  three behaviours (throwaway under `/tmp`; the main request is tools=21, 70 KB, preceded by
  the tools=0 title request):
  1. **Hold with no bytes** (the ADR's Resolved 1 reproduced): request at 5.7 s → **the same
     request re-sent at 308.0 s, 612.5 s, …** (302-304 s apart; matches the ADR's "reset at
     300.1 s, re-sent seconds later").
  2. **Headers only** (200, `text/event-stream`, then a silent body for 400 s): **re-sent at
     306.9 s**. Headers do not hold it.
  3. **Headers plus an SSE comment line `: ping` every 10 s**, then the real chunks and
     `[DONE]` after 400 s: **no re-send; opencode printed the answer delivered at 405 s
     (`PROBE-OK after 400s`) and exited normally** (the title request arrived the same way).
  So the cut fires when **no body bytes arrive for 300 s**, not on elapsed time. A gateway that
  keeps the connection and writes one line every 10 s carries the 527 s start and the silence
  of prefill **inside a single attempt**. Decision 5's "503 at 290 s, wake decoupled from the
  attempt, ride the re-send" and "300 s to first token" came from reading this mechanism as a
  wall, and **bet on the re-send count being finite** (that count is opencode's / the AI
  SDK's to decide; the ADR's "four times" is the 1,200 s harness cut-off, not a limit — the
  review's reproduction saw **six attempts** (five re-sends) before its 1,900 s cut-off, the
  gaps growing 302, 310, 316, 330 s — waits of 2, 4, 8, 16, 32 s after each cut, the AI SDK's
  exponential backoff — with no sign of giving up; whether there is a cap remains unknown).
- **R2. 8 vCPU builds two g6.xlarge. Only one was built because a draining box was holding
  its quota.** CloudTrail (read-only) `RunInstances` / `TerminateInstances`, re-ordered (JST):
  06:42:25 A launched (llm, straight from HF) → 07:12:32 B launched (by all appearances image's
  first attempt — the run whose fetch sidecar could not write as uid 100, the measurement
  recorded in `engprobe.yaml`'s comment) → **07:17:13 MI terminated B** →
  07:19:24, 07:20:01, 07:20:43 `VcpuLimitExceeded` (A running, **B shutting down and still
  counted at 4 vCPU**: A 4 + B 4 + new 4 > 8) → 07:25:05 A terminated → **07:26:04 C launched
  successfully** — while A's EC2 instance was still on its way out (drain is 427-463 s). So two
  boxes (8 vCPU) do coexist; what cannot be built is a box **inside the 7-8 minutes after one
  was dropped**. "16 vCPU for both roles" survives, but the reason changes from "only one
  builds" to "**headroom for one draining box**". B's 07:12:32 → 07:17:13 (281 s) is also a
  measurement of MI's default clock clearing a box whose task had died (R5).
- **R3. g6f VRAM, official: 5,722 MiB (2xlarge), 11,444 MiB (4xlarge), 2,861 MiB (xlarge and
  large); g6.xlarge 22,888 MiB.** From `describe-instance-types`, replacing the table's
  third-party "≈3 / ≈6 / ≈12 GB". The same call gives g6.xlarge an instance store of **250 GB**
  and an **EBS baseline throughput of 125 MB/s** (g6f.2xlarge 250, g6f.4xlarge 750).
- **R4. Prices match the AWS pricing API.** g6.xlarge $1.1672/h (Tokyo, Linux, on-demand); MI
  fee g6.xlarge $0.0910, g6f.2xlarge $0.0537, g6f.4xlarge $0.1075, c6a.large $0.0116. Total
  $1.258/h, a 30-minute window $0.63 (the text's $0.65 is slightly generous), a 427-464 s
  drain $0.15, always-on 730 h $918/month, NAT for 17 GB $1.05, EFS 100 GB $36, EFS IA reads
  of 17 GB $0.20 — no arithmetic error. The GPU measurement bill: six boxes on CloudTrail add
  up to about 1.5 hours (more than the text's "about one hour"), roughly $3.5 with NAT.
- **R5. The MI API has two knobs the ADR does not mention** (aws-cli 2.36.40 service model):
  `infrastructureOptimization.scaleInAfter` (**seconds before an idle box is cleared**; null =
  default, −1 = never, 0-3,600) and
  `instanceLaunchTemplate.localStorageConfiguration.useLocalStorage` (**use the instance store
  as the data volume and provision no EBS**). `storageConfiguration`, on the other hand, has
  **only `storageSizeGiB`** — no throughput or IOPS; open question 1's "provisioned-throughput
  gp3" is not selectable under MI. The 427-463 s drain was measured with `scaleInAfter` at its
  (undocumented) default, so **calling it a fixed MI cost is premature**.
- **R6. HF's speed cannot be said to be "set by the file" either.** The same log group
  (`/af/af-ecs-engprobe/engine`) holds a third point: **the same SDXL (6,938,078,334 bytes)
  came to the ingestion task (curl on Fargate) in 175 s = 39.6 MB/s** — the file the MI box's
  sidecar (same curl, same NAT) pulled at 236 MB/s. GGUF: 9.6 and 4.2 MB/s; SDXL: 236 and
  39.6 MB/s. Four points support "**4-236 MB/s, and what decides it is unknown**" and no more;
  "the unsloth repository is slow" is a generalisation from two points (the corrected sentence
  had the same shape as the one it replaced). What remains as decision 3's evidence is that S3
  ran at 104-147 MB/s on all three pulls (Resolved 8).
- **R7. The code as it is.** (a) The slot pool's `registeredSlots` and `sweepGhostInstances`
  (`runtime_ecs_ec2.go`) **walk every container instance in the cluster with no filter** — MI
  boxes are in that list (the harness's first run timing a slot box is the mirror image). The
  EC2-tag sweeps (`af-role=slot`, `Ec2MaxSlots`) are unaffected, but these two calls sit
  outside decision 1's "untouched". (b) The CP task role's SSM read is **scoped to
  `parameter/af-ws/*`** (`SsmWorkspaceParams` in `20-platform.yaml`). (c)
  `ec2:DescribeInstances` and `ecs:ListContainerInstances` / `DescribeContainerInstances` are
  on the CP role **unconditionally** (`Ec2SlotPool` / `EcsContainerInstances`), so observing
  `draining` needs no new permission. (d) `/git/*` and `/mcp` both authenticate with a Bearer
  PAT, per member (`git_http.go`, `mcpsrv`) — open question 2's premise holds. (e)
  `30-ingress.yaml` is 41,988 bytes (9,212 left); the three contracts (`ClusterName`
  mandatory, PassRole limited to `ecsInstanceRole*`, associations replace the list with an
  empty default strategy) match what `engprobe.yaml` encodes. (f) `af-sandbox` is as left:
  llama / sd / comfy at desired 0, zero G-family instances, quota 8 (nothing was woken).

### Proposed revisions, decision by decision

- **Decision 1** — append: "ADR 0045 decision 6 rejected MI for Workspaces (ECS owns the
  lifecycle, there is no stop, the volume takes only a size). **An engine holds no state it
  would miss**, so the same properties are an advantage here — a difference of subject, not a
  contradiction." And from R7(a): "The slot pool's container-instance walks (`registeredSlots`,
  `sweepGhostInstances`) see MI boxes too. P0 excludes them by `capacityProviderName` from
  `DescribeContainerInstances` and pins that with a test."
- **Decision 2** — rewrite "at 8 vCPU only one g6.xlarge exists … 408 s" per R2: "8 vCPU
  builds two. But **for the 7-8 minutes after a stop the previous box keeps its 4 vCPU**, so a
  restart of one role that overlaps the other role's start hits `VcpuLimitExceeded`. A
  deployment that uses both roles asks for 16 vCPU as headroom for one draining box." Replace
  the table's g6f VRAM with R3's official values, and soften "g6f.2xlarge's 6 GB does not fit
  it" to "**fp16 without offload does not fit 5,722 MiB** (`--offload-to-cpu` and quantisation
  are unmeasured — P4)": the original open question 2 asked exactly those two, and closing it
  as "not an option" without measuring them is the same kind of leap.
- **Decision 3** — design unchanged; fix the reason per R6: "Behind the same NAT HF ran at
  **4-236 MB/s, 6x apart for the same file** (SDXL: 236 MB/s by curl on the MI box, 39.6 MB/s
  by curl on Fargate). Four points cannot say whether file, path or time decides it; what they
  say is **you do not know until you pull**. S3 ran at 104-147 MB/s on all three pulls." Keep
  Resolved 5's "what is slow is HF serving that file (the unsloth repository)" under 🔴 and note
  that "set by the file" was a two-point generalisation too.
- **Decision 5** — replace (R1): "When a request arrives for an engine at desired 0, the CP
  sets desired 1 and, **for streaming requests, answers at once with 200 and
  `text/event-stream` headers, then writes an SSE comment line every 10 s until the first
  upstream byte**; when the engine starts answering, its stream is joined. The same heartbeat
  covers prefill silence (it runs while waiting for the upstream headers too). So **300 s
  bounds the heartbeat interval, not the attempt.** Past `AF_ENGINE_WAKE_TIMEOUT` the answer
  is 503 + `Retry-After` + body — the path for non-streaming requests and for a wake that
  actually failed, never the normal path. A 503 spends one of the client's finite retries, so
  it does not belong on the normal path." The 600 s default leaves 73 s over the measured
  527 s, not enough for a P0 deployment that still pulls from GHCR (178 s at 14 MB/s) — **900 s
  until re-measured after the ECR copy**, and the controller's start deadline (decision 7) is
  at least that. "One more reason CPUs are rejected" becomes "17 minutes", not "a wall" (the
  rejection stands).
- **Decision 6** — the order stands, but drop "the smaller image" from its reasons: 5.1 GB is
  the community image used for measuring, and the size of the ComfyUI image the fleet bakes
  is unmeasured. What remains is **the smaller surface and code** — OpenAI compatibility maps
  onto 0069's `Op` one-to-one, and none of the workflow template, the WebSocket pane or the
  fleet-built image is needed — the same shape as P0's reason for llm first. "2.5x faster" has
  not been checked for equal steps and sampler; add "like-for-like unverified".
- **Decision 7** — three points. (a) The states keep 0070 decision 7's `stopping` (the OFF
  undo window) and `tts_ecs.go`'s `none`, and **add** `draining` (not four values). (b) **The
  start deadline is per engine and at least the measured cold start** — at 0070's default of
  300 s every GPU start would be a "failure" and the cooldown would double away (the same
  presentation as the `createdAt` incident). (c) The drain was measured at `scaleInAfter`'s
  default (R5), so **P0 measures 0 and −1 once each** ($0.15 each). −1 is material for a
  "keep the box" design: a request that arrives while the box still exists after desired 0
  should come up without the S3 fetch, since **both the image and the model file are on the
  box** (the VRAM load only). "Trimming the idle window can only ever recover the drain" reads
  the drain as pure waste — whether it can be designed in as a warm-box window is one P0
  measurement: stop, then re-request inside the drain.
- **Decision 8** — (a) "SSM reads are already in the CP task role" holds **only under
  `/af-ws/*`** (R7(b)); decide whether the parameter is named `/af-ws/engines` or the
  `20-platform` policy is widened, and write it down. (b) The contrast with 0070 is accurate and
  can be made precise: **the CP side adds zero IAM here too** (UpdateService / DescribeServices
  / DescribeInstances / container-instance reads / SSM read are all present and unconditional);
  the new IAM is the three roles closed inside `60-engines` (infrastructure, instance profile,
  task). (c) The harness shares one TaskRole between ingestion and engines; production splits
  it as the text says — ingestion (write plus secret) and engine (read). `ssmmessages:*` is
  harness-only and stays out of the production roles.
- **Open question 1** — recommendation: **measure `useLocalStorage: true` first.** g6.xlarge's
  EBS baseline of 125 MB/s bounds both the S3 fetch (104-147 MB/s looks like the EBS write
  ceiling, not S3's) and the VRAM load (18.5 GB is at least 148 s even at 125 MB/s). The
  250 GB instance store should move both; provisioned gp3 throughput does not exist under MI
  (R5).
- **Open question 2** — recommendation: **a per-session dedicated token.** Three reasons: (1)
  `usagex` rows are cut per session and a PAT does not match them; (2) a value placed in
  `{env:…}` is visible to the model — a short-lived value good only for `engine:llm` loses
  less when leaked than a PAT that works on git and MCP; (3) it is the shape of 0069
  decision 3's review correction (the tenant layer does not borrow the git bridge; it has its
  own entry).
- **Phases** — P0's "the numbers for open questions 1-3" and P1's "open question 4" are
  pre-revision numbering; the open questions are now 1 and 2 only. P0's measurements are
  spelled out: the `useLocalStorage` cold start, the `scaleInAfter` 0 / −1 drains, a re-request
  inside the drain, the pull from ECR, and the pool walks excluding MI.
- **Resolved 1 and 6** — append R1's and R2's consequences under 🔴 (the text stays).

### Answers to the questions asked

1. Decisions still resting on estimates: decision 2's g6f floor (offload and quantisation
   unmeasured), decision 6's "smaller image" (fleet image unmeasured), decision 7's drain
   (`scaleInAfter` default), decision 5's 600 s (73 s of headroom). **6 and 7 can be measured
   in P0; 2 and 5 are fixed in the text before P0.**
2. Decision 5 does not follow. Resolved 1 says "cut after 300 s of silence", not "cut at 300 s"
   (R1), and the design for a finite re-send count is not written. Holding with a heartbeat
   removes the dependence on re-sends.
3. "HF stays off the start path" is sound. "50x between files" and "unsloth is slow" fall to
   the third point (the same SDXL at 39.6 MB/s) (R6).
4. The order may stand, but for "the smaller surface and code", not "the smaller image".
5. Open question 1: `useLocalStorage`; open question 2: the dedicated token (above).
6. The contrast is accurate (CP side zero, three new roles). The three contracts match the
   harness. Only the SSM premise is conditional on `/af-ws/*`.
7. No arithmetic errors (R4).
8. No contradictions. State that 0045 decision 6 (MI unfit for Workspaces) is about a
   different subject, keep 0070's state set and start deadline, and close the two pool walks
   (R7(a)) in P0.

### Proposed status line

Once decisions 2 and 5 above are reflected in the text, change the status line to
**"approved (P0 may start)" (reviewed 2026-09-07)**. Add to P0's definition of done: "the first
request to a stopped engine returns an answer **within a single attempt**" — the one
observation that verifies the replacement of decision 5.

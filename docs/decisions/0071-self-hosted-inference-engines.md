# 0071. Run self-hosted inference engines (llama.cpp / Stable Diffusion / ComfyUI) on demand, on GPUs — the GPU Fargate cannot sell, in the shape ADR 0070 established

English | [日本語](0071-self-hosted-inference-engines.ja.md)

- Status: **proposed — design only, nothing implemented** (2026-09-06). Every price below
  came from AWS's public price data (Tokyo, on-demand) on that date, every image and model
  figure from the registry and Hugging Face APIs on that date. What was measured in this
  container says so. Written to be reviewed before P0 is built; the Open questions are the
  parts that can still change the shape.
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
   VRAM ≥ 6 GB (SD1.5 and SDXL q8). Both default to **g6.xlarge (L4 24 GB, $1.26/h all-in)**;
   `image` can drop to g6f.2xlarge ($0.74/h) for SD1.5-only deployments. **The instance
   requirement is a stack parameter per role** (declared, never inferred — ADR 0053). Not
   sharing a box: running out of VRAM under CUDA does not slow down, it **crashes**. Most of the
   time only one role is awake, so splitting costs almost nothing extra. ComfyUI is **a second
   engine of the `image` role** (decision 6) and never runs alongside sd-server (same VRAM).

3. **Models go HF → S3 once, and S3 → local disk on every start. HF is never on the start
   path.** Pulling 17 GB from HF each start costs **$1.05** in NAT processing and **9 minutes**
   at 31 MiB/s, and puts HF rate limits and a gate token on the node. S3 storage is
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
   **How long the CP can hold an opencode chat request** (the SDK's client timeout) is measured
   in P0 (open question 1). "One request buys a 30-minute window" is the opt-in of the person
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

7. **One controller, 0070's design generalised to N engines, with VOICEVOX as one of them.**
   `decideEngineAction` stays a pure function and takes an engine table (service name, URL,
   health URL, idle window, start deadline, cooldown). Demand clocks persist in `SettingsStore`
   (once a minute), `DescribeServices` gets a short TTL cache, start failures are diagnosed from
   `events[]` and cool down, and the idle window applies during `starting` too (0070
   decisions 5, 6, 9, 10). **One MI-specific addition**: after desired goes to 0 the instance
   lingers until MI scales it in, and the fee and EC2 price run until then. The state becomes
   `running | starting | stopped | draining`, and P0 measures how long `draining` lasts. Modes
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
   instance profile, S3 read for the engines, S3 write plus secret read for ingestion. 0070's
   "zero new IAM" does not hold here.

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

## Open questions — to settle before P0 is written

1. **How long the CP can hold an opencode request.** Measure the client-side timeout of
   opencode (Bun's fetch, `@ai-sdk/openai-compatible`). If it is shorter than 600 s,
   decision 5's wake-and-hold does not work for llm and the first request answers 503 with the
   model told to **retry in N minutes**.
2. **MI cold start and drain.** Each leg of desired 1 → instance up → 2.3–5 GB pull → 17 GB
   from S3 → `/health` ok, and desired 0 → instance terminated (where the fee stops). AWS's
   reference says 13 minutes, but that includes the alarm and a 14 GB image.
3. **The G-family vCPU quota.** New accounts can have "Running On-Demand G and VT instances"
   at **0**. Write it down as a stand-up precondition, and have stand-up say so when it is 0.
4. **The default size of the `image` role.** Seconds per SDXL image on g6f.2xlarge (6 GB,
   with `--offload-to-cpu`) versus g6.xlarge. ComfyUI's SDXL fp16 uses around 8 GB, so a
   deployment that picks ComfyUI may have g6.xlarge as its floor.
5. **Reuse the Workspace credential or mint an engine token.** Reusing the `/git/*` PAT gives
   per-member accounting but not per-session. If per-session is wanted, the Agent mints a
   short-lived token at launch.

## Rejected

- **llama.cpp on CPUs (Fargate 16 vCPU / c8g.4xlarge).** Same price as one L4 ($0.79–0.99/h
  versus $1.26/h); MoE generation (3B active) would be usable, but **a coding agent's 20k-token
  prefill takes minutes on a CPU**. Not cheaper, and slow. The 297 MB CPU image does not change
  that.
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
- **P4 — cheaper, with evidence.** MI Spot, shrinking `image` to g6f, several models through
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
  `opencode models` (opencode 1.18.29).
- This repository: `control-plane/main.go` (proxy env), `egress_policy.go`,
  `preview_host_serve.go`, `tts_ecs.go`, `internal/runtime/runtime_ecs.go` (awsvpc, `WsSg`),
  `workspace/agent/internal/imagegen/imagegen.go`,
  `internal/agents/opencode/{auth,models}.go`,
  `deploy/aws/ecs/cfn/{00-network,20-platform,30-ingress,50-tts}.yaml`.

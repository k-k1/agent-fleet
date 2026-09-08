# 0071. Run self-hosted inference engines (llama.cpp / Stable Diffusion / ComfyUI) on demand, on GPUs — the GPU Fargate cannot sell, in the shape ADR 0070 established

English | [日本語](0071-self-hosted-inference-engines.ja.md)

- Status: **adopted — P0 and P1 implemented and merged to develop (PR #419)** (2026-09-07; approved by the review the same day, drafted 2026-09-06). Every price below
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
- Revised the same day after that review: decision 5 was replaced with a heartbeat design (the
  300 s is a body-silence limit, not a wall clock), decision 2's "8 vCPU builds one box" and
  decision 3's "it depends on the file" were corrected (both were generalisations from two
  points), and decisions 1, 6, 7 and 8, the open questions and the phases took the review's
  findings. Status changed to approved.
- The same day, **P0 was implemented and verified on real hardware** ("What P0 measured"), and
  then **P1 (the `image` role and the `sdcpp` provider) was implemented and its engine side
  measured** ("What P1 measured"). The Phases entry for P1 records the three things the
  implementation added. 🔴 P0's measurement 10 used `describe-instances` as evidence for "no GPU
  boxes"; it **does not list MI boxes at all** — corrected in P1's measurement 2.
- One correction added the next day (2026-09-08). 🔴 **The provider block P0 and P1 shipped
  declared no window for the model at all** — opencode reads a model with no `limit` as having a
  context of 0, and it **switches auto-compaction off at 0**. Fixed by splitting
  `LlmContextTokens` / `LlmMaxOutputTokens` out of `LlmExtraArgs` ("A correction after P1").
- The next day (2026-09-08) **the rest of P1.5 — showing an engine's current state in the
  Console — was implemented** and decision 13 added. A toggle alone (also P1.5) cannot answer
  "is it safe to switch this off", so the panel gained the start time, the automatic stop time,
  recent demand, the model that is loaded, and an occupancy heatmap. 🔴 Building it revealed that
  **engine uptime was recorded nowhere at all**: the controller read the service every 30 seconds
  and threw the observation away, so that tick became the sampler for a new `engine_hourly`
  table. Most of decision 13 is rules for not writing down what is not known.
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
  | g6f.large | 2 / 8 GiB | 1/8 of an L4 (2,861 MiB) | 0.293 | 0.023 | 0.316 |
  | g6f.xlarge | 4 / 16 GiB | 1/8 of an L4 (2,861 MiB) | 0.344 | 0.027 | 0.371 |
  | g6f.2xlarge | 8 / 32 GiB | 1/4 of an L4 (5,722 MiB) | 0.689 | 0.054 | 0.743 |
  | g6f.4xlarge | 16 / 64 GiB | 1/2 of an L4 (11,444 MiB) | — | 0.107 | — |
  | g4dn.xlarge | 4 / 16 GiB | T4 16 GB | 0.710 | 0.055 | 0.765 |
  | **g6.xlarge** | 4 / 16 GiB | **L4 24 GB** | **1.167** | **0.091** | **1.258** |
  | g6.2xlarge | 8 / 32 GiB | L4 24 GB | 1.418 | — | — |
  | g5.xlarge | 4 / 16 GiB | A10G 24 GB | 1.459 | 0.114 | 1.573 |
  | g6e.xlarge | 4 / 32 GiB | L40S 48 GB | 2.699 | — | — |
  | c8g.4xlarge (CPU, for comparison) | 16 / 32 GiB | none | 0.800 | 0.096 | 0.896 |
  | Fargate 16 vCPU / 32 GiB (comparison) | | none | x86 0.986 / ARM 0.789 | — | — |

  (VRAM figures are the official values the review took from `describe-instance-types`;
  g6.xlarge has 22,888 MiB, a 250 GB instance store and a 125 MB/s EBS baseline.)
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
   line is the whole failure"). The slot pool keeps `LaunchType: EC2` and is untouched. ADR 0045
   decision 6 rejected MI for Workspaces (ECS owns the lifecycle, there is no stop, volumes take
   only a size); **an engine holds no state it cannot lose**, so the same properties are an
   advantage here — a difference of subject, not a contradiction. One caveat: the slot pool's
   container-instance walks (`registeredSlots`, `sweepGhostInstances`) list every box in the
   cluster unconditionally, MI boxes included. P0 excludes them by `capacityProviderName` from
   `DescribeContainerInstances` and pins that with a test (review R7).

2. **Two engine roles (`llm`, `image`), one capacity provider each, never co-located.** `llm`
   needs VRAM ≥ 20 GB (Qwen3-Coder-30B-A3B Q4 is 17.3 GiB plus KV cache); `image` needs
   VRAM ≥ 8 GB (SDXL fp16 measured at 7.4 GB — it does not fit g6f.2xlarge's **5,722 MiB** as
   fp16 without offloading; `--offload-to-cpu` and quantisation are unmeasured, P4). Both
   default to **g6.xlarge (L4 24 GB, $1.26/h all-in)**; the floor for a cheaper `image` role is
   decided after measuring. **The instance
   requirement is a stack parameter per role** (declared, never inferred — ADR 0053). Not
   sharing a box: running out of VRAM under CUDA does not slow down, it **crashes**. Measured,
   the 30B Q4 takes **20.9 GB** and SDXL **7.4 GB**, so both do not fit one L4's 23 GB. Most of
   the time only one role is awake, so splitting costs almost nothing extra. **8 vCPU does build two boxes; but for
   7-8 minutes after a stop the previous box still holds its 4 vCPU**, so restarting one role
   while the other starts collides in `VcpuLimitExceeded` (Resolved 6 and review R2). A
   deployment that wakes both roles asks for 16 vCPU as one drain's worth of headroom. ComfyUI is **a second
   engine of the `image` role** (decision 6) and never runs alongside sd-server (same VRAM).

3. **Models go HF → S3 once, and S3 → local disk on every start. HF is never on the start
   path.** Measurement moved this decision's reason from cost to **speed and unpredictability**
   (Resolved 5): behind the same NAT, HF delivered at **4-236 MB/s**, and **the same file
   differed 6x** (SDXL: 236 MB/s by curl on the MI box, 39.6 MB/s by curl on Fargate; the GGUF:
   9.6 MB/s by `-hf`, 4.2 MB/s by curl on Fargate). Four points cannot say whether file, path
   or time decides it; all they say is **you do not know until you pull**. That is not a speed
   the start path can be built on. S3 delivered **104-147 MB/s** all three times (Resolved 8). The cost ($1.05
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
   ok, then forward. Past `AF_ENGINE_WAKE_TIMEOUT` (default **900 s** — 600 leaves 73 s over the measured 527 s, not enough while P0 still pulls 178 s from GHCR; re-measure after the ECR copy) it answers 503 with
   `Retry-After` and a human-readable body (the model sees it too). The image MCP tool emits
   `notifications/progress` every 10 s meanwhile (the existing trick that lifts opencode's 60 s).
   **opencode cuts and re-sends when no body byte has arrived for 300 s** (Resolved 1, review
   R1) — a silence limit, not a wall clock. So for a streaming request the gateway **answers
   200 with `text/event-stream` headers at once and writes an SSE comment line every 10 s until
   the upstream's first byte arrives**, then splices the engine's stream in. The same heartbeat
   covers prefill silence (it runs while waiting for the upstream headers too). **300 s bounds
   the heartbeat interval, not the attempt.** 503 with `Retry-After` is the path for
   non-streaming requests and for a wake that genuinely failed, never the normal path — a 503
   spends one of the client's finite retries (how many, opencode / the AI SDK decides, and the
   ceiling is unknown). The first draft's "503 at 290 s, ride the re-send" came from reading the
   mechanism as a wall and bet on retries being finite. CPUs are rejected for the 17-minute
   prefill itself, not for any wall.
   "One request buys a 30-minute window" is the opt-in of the person
   who chose that provider or model; there is no break-even analogue to 0070 (the alternative is
   simply "not available").

   🔴 **"The CP holds it" is good for 60 seconds on a non-streaming request (P1 measurement 9,
   2026-09-07).** How long it can be held is decided not by the CP but by **whatever sits in
   front**: 30-ingress's ALB has `idle_timeout.timeout_seconds: "60"` and closes a connection
   that has carried no response byte for a minute. The streaming path could hold for 900 s
   because its 10-second heartbeat was **also defeating that idle timeout** — a side effect of
   the mechanism written for opencode's 300 seconds. Images answer with JSON and have nowhere
   to put a heartbeat, so **the first call to a cold engine always died at 60 s** (measured: the
   CP answered `503 59.998s` while the engine itself was ready at 165 s). So this decision now
   splits by role:
   - **streaming** (`chat`) holds to `AF_ENGINE_WAKE_TIMEOUT`, as the body says;
   - **non-streaming** (`images`) folds its hold **below the front end's idle timeout** and
     answers `503 engine_waking` + `Retry-After` (`AF_ENGINE_PLAIN_HOLD`, default **45 s**,
     never above `AF_ENGINE_WAKE_TIMEOUT`). The side that keeps waiting is the caller: the
     `sdcpp` provider asks again within its own 16-minute budget. What a person sees — one call,
     one picture — is unchanged, because the MCP progress heartbeat keeps the client's clock
     alive and the retries never surface above the tool.
   The **code** on the 503 decides whether to retry (`engine_waking` yes; `engine_off` and
   `engine_unavailable` no). Retrying on the number alone would turn a clear refusal about a
   switched-off engine into sixteen minutes of silence. 502 and 504 are retried as well: they
   are **the front end answering on the gateway's behalf**, which is also what a Control Plane
   too old to know `engine_waking` looks like from here.
   Raising the ALB's `idle_timeout` was rejected: it applies to every route, the same hole
   reopens on any deployment whose front end is not ours (CloudFront, a customer's nginx), and
   **the CP cannot ask what that value is**. Put the mechanism on the side that survives
   whatever is in front.

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
   lingers until MI scales it in, and the fee and EC2 price run until then. `draining` is
   **added** to 0070 decision 7's state set (keeping `stopping`'s undo window and `tts_ecs.go`'s
   `none`). The start deadline is per engine and **at least the measured cold start** — left at
   0070's 300 s default every GPU start would count as failed and the cooldown would double.
   `draining` is **93 s** on a CPU box and **427 s and 463 s** on GPU boxes (Resolved 4) — but
   those were measured with MI's `infrastructureOptimization.scaleInAfter` (null = default, −1 =
   never, 0-3,600 s) at its default, so calling it an MI fixed cost is premature. P0 measures 0
   and −1 once each; −1 is material for a "keep the box" design: a request arriving while the
   box still exists after desired 0 should wake without an S3 fetch, since image and model file
   are both on the box. Modes
   are 0070 decision 7's `off / on / ondemand`, per engine.

8. **The stack is `60-engines.yaml` (optional). What `30-ingress` receives is one SSM parameter
   name.** In `50-tts`'s shape it imports `00-network` and `20-platform` and does not depend on
   `30-ingress`. Contents: two capacity providers (attribute-based selection), the MI
   infrastructure role and instance profile, an S3 bucket (models), the ingestion task
   definition, three engine task definitions and services (**no `DesiredCount`** — 0070
   resolved point 2), three Cloud Map A records, SGs, logs. **There is no room in `30-ingress`
   for two parameters per engine** (six parameters plus conditions plus env come to nearly 3 KB
   of the 9.2 KB left). Instead `60-engines` writes one JSON of service names and URLs to SSM
   and `30-ingress` passes a single `AF_ENGINES_SSM_PARAM`; the CP reads it at boot (the CP task role's SSM
   read is **limited to `/af-ws/*`**, so P0 either names the parameter `/af-ws/engines` or
   widens the `20-platform` policy). **CP-side IAM additions are zero here too** (UpdateService,
   DescribeServices, DescribeInstances and the container-instance reads all exist,
   unconditionally); **the new IAM is three roles closed inside `60-engines`** — the MI
   infrastructure role, the instance profile, and task roles (engines read S3; ingestion writes
   S3 and reads the secret; the harness shares one role but production splits them and never
   grants `ssmmessages:*`). Three
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

13. **Show the current state under the rule "do not write down what you do not know", and record
    the occupancy history by making the controller's own tick the sampler.** The super-admin
    "Inference engines" panel gains when the engine started, when it will stop by itself, what
    has been asked of it lately, which model is loaded, and a heatmap of its history (P1.5). A
    screen with nothing but a toggle cannot answer "is it safe to switch this off" — a
    $1.26/hour box is asleep most of the time, so **everything visible before the button is
    pressed is whatever this panel says**. Most of the design turned out to be rules about what
    NOT to show:
    - **The start time is the box's `registeredAt`** (ECS `describe-container-instances`),
      falling back to the service's `lastStart` only when there is no box. They are **different
      facts**: `lastStart` moves on a stack update or a replaced task, without any box being
      bought. 🔴 Not `ec2 describe-instances` (P1's measurement 2: **an MI box does not appear
      in the listing**, so an EC2-side implementation would answer "no box" for a running GPU).
    - **The stop time is omitted whenever there is no answer.** An engine pinned `on` does not
      stop, so showing a countdown there is a promise of a saving that will not arrive. Same for
      `off`, for an engine that is already stopped, and for one with no demand mark yet — the
      same reason `decideEngineAction` judges nothing on that pass. The threshold lives in one
      function (`engineIdleWindow()`) so the panel's countdown and the controller reach the same
      instant; kept separately they diverge the day one of them forgets the clamp to the start
      deadline.
    - 🔴 **The recent request count exists only in the CP's process memory.** It resets to zero
      when the CP is replaced (only `lastAt` is persisted — decision 6). So when less than a
      full window has been counted, `window_counted_secs` says so and the UI adds "this control
      plane has only been counting for N minutes". **Never write a past it cannot recount as 0**
      — this is the single number on the screen that can be confidently wrong. The persisted
      last-request time beside it is what makes a zero readable.
    - **The models are the stack's declaration** (ADR 0053), never a question put to the engine
      — it is asleep, i.e. unanswerable at exactly the moment somebody comes to look. Whether
      one is loaded is answered by the `warmed()` the controller already maintains (ECS RUNNING
      means "the port is open", and llama-server satisfies that 267 seconds before the weights
      are in VRAM).
    - **The history goes into a new table, `engine_hourly`, written by the controller's tick.**
      Engine uptime was recorded nowhere at all before this: the controller read the service
      every 30 seconds and threw the observation away. Nothing else in the CP looks that often,
      and the AWS call is already paid for, so recording it costs one INSERT.
      - A cell is **three-valued**, and **an hour with no row is UNOBSERVED, i.e. blank**.
        Unlike `usage_hourly` there is no separate heartbeat row: the controller watches one
        engine and cannot half-observe it, whereas the workspace sweep walks every tenant and
        can.
      - ⚠️ **The denominator `observed_secs` is stored.** The tick interval is not constant (5
        seconds while starting or warming), so reconstructing it as `samples x nominal interval`
        **exceeds 100% in exactly the busy hours** somebody opens the panel to look at.
      - ⚠️ **The first tick of a process records nothing**, and one tick claims at most one
        interval. A CP that was down for an hour comes back with a large elapsed time and a
        perfectly valid current state, and attributing that gap to what it happens to see now
        fills the outage with confident colour. The cost is 30 seconds lost per CP start; the
        return is that **a blank stays blank**.
      - ⚠️ **`running` / `starting` / `draining` are separate columns.** The last two bill and
        answer nothing (measured: 165-197 s to start, 427-477 s to drain). Summing them into
        running would claim the engine was serving; dropping them would make spent money vanish.
        The heatmap can show either reading ("able to answer" and "a box existed").
    - **No money is drawn** (ADR 0048 decision 2). An hourly figure could only be seconds times
      a rate somebody typed in once, which is why the existing view is an uptime view and not a
      cost view.

## Resolved by measurement (2026-09-07)

1. **opencode cuts at 300.1 s and re-sends a few seconds later.** opencode 1.18.29 in this
   container was pointed, through an `@ai-sdk/openai-compatible` provider, at an
   OpenAI-compatible stub that merely holds the response for N seconds. A 150 s hold succeeded
   (both requests — the tools=0 title request and the tools=21 main one — answered, exit 0).
   400 s and 700 s holds were **reset at 300.1 s**, the same request (tools=21) was **re-sent
   3–5 s later**, and that repeated **four times** until the 1200 s cut-off. The consequences
   are in decision 5: 503 at 290 s per attempt, the wake decoupled from the attempt, 300 s to
   first token. 🔴 **Review R1 overturned the premise**: the cut fires when **no body byte has
   arrived for 300 s**, not on elapsed time. Headers alone still get re-sent at 306.9 s, but
   headers plus an SSE comment line every 10 s **hold for 400 s with no re-send and the answer
   arrives**. The re-send count has no known ceiling (6 attempts in 1,900 s, exponential
   back-off). The author reproduced it with the same kind of stub: 200 and SSE headers at once,
   `: keepalive` every 10 s, a 400 s hold — **both requests (tools=0 and tools=21) were answered
   at 405 s with no re-send, exit 0**. Decision 5 was replaced with the heartbeat design.
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
   to draw the same conclusion from the same two points. 🔴 **Review R6: the corrected version
   was itself a generalisation from two points** — the same SDXL came at **39.6 MB/s** through
   the ingestion task's curl on Fargate (236 MB/s through curl on the MI box). Neither "unsloth
   is slow" nor "it depends on the file" holds; four points allow only "4-236 MB/s, unknown
   until you pull". Decision 3 now says that.
6. **VRAM and the G-family quota.** Qwen3-Coder-30B-A3B Q4_K_M used **20,943 MiB** and SDXL
   fp16 **7,379 MiB** (6,624 MB of params). So **two roles do not fit on one L4**, and dropping
   `image` to a g6f.2xlarge (6 GB) is not an option (half the answer to the old open question).
   And **8 vCPU of quota only builds one g6.xlarge**: starting `image` while `llm` was up
   produced repeated `VcpuLimitExceeded: your current vCPU limit of 8`, and placement succeeded
   **408 s later**, after the first box terminated. A deployment that wakes both roles asks for
   16 vCPU. 🔴 **Review R2 overturned "only one box"**: re-ordering CloudTrail, the three
   `VcpuLimitExceeded` events (07:19-07:20) fell while a box MI had just retired (terminated
   07:17:13) was still counted for 4 vCPU, and the image box launched at 07:26:04 while the llm
   box was still shutting down. **8 vCPU builds two.** The reason for 16 vCPU becomes "one
   drain's worth of headroom", and decision 2 was corrected accordingly.

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
   sd-server's advantage is **the smallness of surface and code** (OpenAI compatibility maps
   one-to-one onto 0069's `Op`; no workflow template, no WebSocket pane, no fleet-baked image —
   the 5.1 GB is the community image used for measuring, and the fleet's own ComfyUI image is
   unmeasured), ComfyUI's are **per-image speed (whether steps and sampler matched is
   unverified) and workflow freedom**; since cost is set by the window (decision 5), 13 s per
   image never reaches the bill. The order stands, but a
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
   is reading 18.5 GB off gp3 EBS. The review recommends measuring
   **`localStorageConfiguration.useLocalStorage` first**: g6.xlarge's 125 MB/s EBS baseline
   bounds both the S3 fetch (104-147 MB/s looks like the EBS write ceiling, not S3) and the VRAM
   load (18.5 GB is at least 148 s even at 125 MB/s), and the 250 GB instance store would move
   both. A provisioned-throughput gp3 is not available on MI (`storageConfiguration` takes only
   `storageSizeGiB`).
2. **Reuse the Workspace credential or mint an engine token.** Reusing the `/git/*` PAT gives
   per-member accounting but not per-session. The review recommends **a per-session token**:
   (1) `usagex` rows are cut per session, (2) a value placed in `{env:…}` is visible to the model,
   and a short-lived value good only for `engine:llm` loses less when leaked than a PAT that
   works for git and MCP, (3) it takes the shape 0069 decision 3's review correction chose (the
   tenant layer keeps its own entry instead of borrowing the git bridge). The author agrees; P0
   builds it that way.

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

## Measured while writing P0 (2026-09-07)

`60-engines` was deployed for real onto the shared `af-sandbox` cluster while P0 was being
written. The five things the Phases section said P0 would measure are settled here, and **two
of them contradicted what the body expected** — a `Host: {}` volume does not survive a task
even on a box that was deliberately kept (4), and the gain from copying the image into ECR
disappeared inside the variance of placement (1). The whole exercise cost about 1.4 hours of
g6.xlarge, roughly $2.

1. **The production-shaped cold start is 586 seconds** (`desired 1` → listening), with the
   image in ECR, the model in S3 and no box. Broken down: **+88 s to task creation and
   placement**, pull **79 s** (99 s less than GHCR's 178 s), S3 fetch **161 s** (18.5 GB =
   115 MB/s), RUNNING at +350 s, **`model loaded` and listening at +586 s** (236 s loading
   into VRAM). 🔴 **The expectation that re-measuring after the ECR copy would beat 527 s was
   wrong.** The pull really did shrink by 99 s, but placement took 88 s where an earlier start
   the same day took 8 — what dominates is not the pull but **how long it takes for a box to
   exist at all**. `AF_ENGINE_WAKE_TIMEOUT` stays at 900 s (314 s of headroom over 586). Avoiding
   a generalisation from two points, all that can be said is "**500-600 seconds, and it moves
   both ways**".
2. **Drain with the default `scaleInAfter` is 456 seconds** (shutting-down begins at +95-104 s).
   That is the same range as the harness's 427 s and 463 s — a third point in agreement.
3. **`scaleInAfter: -1` really does keep the box.** 583 seconds after `desired 0` it was still
   `running` (the default had terminated at 456 s). 🔴 But **changing `scaleInAfter` afterwards
   does not reclaim a box that already went idle under `-1`** — set to `0`, it was still
   `running` 497 seconds later. What does retire one is
   `ecs update-container-instances-state --status DRAINING`, which had it **shutting down in 90
   seconds** (`ec2 terminate-instances` is refused outright by MI's resource-based policy). CloudFormation's
   `AWS::ECS::CapacityProvider` carries **both** `InfrastructureOptimization.ScaleInAfter` and
   `InstanceLaunchTemplate.LocalStorageConfiguration.UseLocalStorage` — review R5 only
   established that the API had them — so both became stack parameters.
4. 🔴 **Decision 7(c)'s warm box could not be made to work in P0, and is left unproven.** It
   failed twice over:
   - a restart that landed **back on the very same instance** kept by `-1` pulled all 18.5 GB
     from S3 again (126 s; the restart itself took 410 s, 176 s less than the 586 s cold start
     — placement and pull). The cause is `Host: {}`: with an empty host parameter ECS/Docker
     allocates **a fresh anonymous directory per task**, so `/models` was empty as far as the
     new task was concerned;
   - changing it to `Host: { SourcePath: /var/lib/af-engine-models }` then made the service
     fail to start at all — **`No space left on device` on a brand-new 120 GiB box**. 🔴 So
     `StorageConfiguration.storageSizeGiB` sizes the **data volume MI attaches** (the one the
     container runtime uses), while an arbitrary host path like `/var/lib/…` lands on the
     **root filesystem**. Reverted to `Host: {}` with both measurements written down.
   On top of that the anonymous directories are **never reclaimed**: four starts on one box
   kept by `-1` filled its disk with 18.5 GB apiece. P0's answer is therefore "**leave
   `scaleInAfter` at the default**". Making the warm box work needs the path MI's data volume
   is actually mounted at, which is undocumented and not worth another GPU hour; P4 picks it up
   with `useLocalStorage`, where the 250 GB instance store is the whole disk.
5. **That the pool walk sees MI boxes** was confirmed against the real cluster.
   `DescribeContainerInstances` returns, side by side, `i-0abeb…/None` and `i-0075e…/None` (slots,
   `agentConnected=false`) and `i-059d9…/af-af-ecs-engines-llm` (the engine,
   `agentConnected=true`). Without a filter, `registeredSlots` counts the engine box as a free
   slot, and the moment it starts draining `sweepGhostInstances` deregisters it. Whether
   `capacityProviderName` is empty is the only thing that tells them apart, and that is where P0
   cuts (decision 1, review R7(a)).
6. **The engine's own surface works over the production path.** From a throwaway Fargate task
   placed in the CP's security group — not an SSM port-forward, because the production task role
   deliberately has no `ssmmessages:*`: `http://llm.af.internal:8080/health` answered **200**,
   `/v1/chat/completions` answered as `qwen3-coder-30b-a3b` (the `--alias`), and **a request with
   no key got 401**. Both locks — the SG (CP only) and `--api-key` from an SSM SecureString — are
   doing their job.
7. 🔴 **`usage` does not just appear in a stream.** With `stream_options.include_usage` the same
   engine emits `{"choices":[],"usage":{...}}` immediately before `[DONE]`; **without it there is
   no usage chunk at all**. The AI SDK does set it, but counting is the CP's job (decision 9), so
   **the gateway now adds the flag itself**. Writing `measured="none"` rather than zeros when it
   is still absent is the same treatment ADR 0069 gave images: what cannot be measured is not
   recorded as zero.
8. **Two more CloudFormation contracts.**
   (a) **Omitting `DesiredCount` behaves as measured** — neither the update that changed
   `ScaleInAfter` nor the one that replaced the task definition moved `desiredCount` off 0.
   (b) 🔴 **Creating the service before a model exists wedges the stack for good.** Hit for real
   on the first create: the fetch sidecar died with `Key … does not exist`, the task crash-looped
   every 60 seconds, and the stack sat in `CREATE_IN_PROGRESS` — exactly the shape that put the
   ECR repositories in 20-platform. The same stack creates the bucket and the service that reads
   it, so **an empty `LlmModelS3Key` now creates no service**, and the first stand-up is two
   passes: build it empty, ingest, then set the key and build again.
9. **How stand-up passes a secret.** The engine's `--api-key` is machine-generated, so stand-up
   **creates it when it is missing** rather than only checking for it as it does for the other
   secrets. The first implementation used `ssm put-parameter --value <secret>` and was caught by
   an assertion in the stub test this work added: an argument is readable from
   `/proc/<pid>/cmdline` and lands in any shell trace. It goes through
   `--cli-input-json file://…` now (0600, deleted immediately).

10. ✅ **The second half of the definition of done — "the first request to a stopped engine is
    answered on one attempt" — was carried out for real.** P0's code was deployed to
    af-sandbox (`0.16.1-dev-5b62a9b4`) and, with **no GPU box in existence at all** (desired 0,
    `describe-instances` empty, the Cloud Map name unregistered), a **single** curl sent
    `POST https://<fqdn>/engine/llm/v1/chat/completions` with `stream:true`:
    - **`HTTP/2 200` and `content-type: text/event-stream` immediately** (the same second).
    - The CP logged `engine llm: started on demand` in that same second and wrote
      **`: af-engine waking` 51 times, every 10 seconds**. The longest gap between bytes was
      10 s, thirty times inside opencode's 300 s.
    - **512 seconds later the model's answer came down the same connection**, reading
      `ADR0071-ONE-ATTEMPT`, then `[DONE]`. The CP's log has one line —
      `POST /engine/llm/v1/chat/completions 200 8m31.915s` — and **no resend and no 503**.
      Decision 5's replacement is now backed by measurement.
    - The last chunk carried `usage` (24 prompt / 12 completion). **curl never sent
      `stream_options`**, so that is the gateway adding it (see 7).
11. 🔴 **That same run exposed the CP→Agent usage POST failing.**
    `dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host` — a Service Connect alias is not
    DNS; the ECS agent writes it into `/etc/hosts` once, at CP task start, so **a workspace
    created after the CP came up does not resolve**. `agent_dial.go` exists precisely for that
    and carries a Cloud Map fallback, and this call was using a bare `http.Client`. Changed to
    `newAgentTransport()`. It fails silently — one row goes missing — so nothing but running it
    for real would have found it.

12. ✅ **The ingest task was run for real, once.** It pulled SDXL's 6.94 GB from Hugging Face
    in **161 seconds (43 MB/s)**, **matched the sha256 Hugging Face declares** (`31e35c80…`),
    and put it in S3 in **46 seconds**. That exercises the check itself and the
    `DependsOn: SUCCESS` that keeps an unverified file from being uploaded, against a real
    file. It is also a fifth Hugging Face data point, and **43 MB/s** landed inside the
    4-236 MB/s band again — "you cannot tell until you pull" has not broken at five points.
    ⚠️ One operational note: when overriding this task's command through `run-task`, the
    override must be **a single string**, because the `EntryPoint` is already `["sh","-c"]`.
    Passing `["sh","-c",<script>]` becomes `sh -c sh -c <script>`, which **does nothing and
    exits 0** — it looks like success and ran nothing.

13. 🔴 **A managed session got 401 — P0's implementation had missed opencode's default route.**
    Creating an opencode session from the Console and sending "hello" produced
    `APIError (HTTP 401) invalid engine session token`, with `qwen3-coder-30b-a3b` sitting
    correctly in the launch menu, so the config half was working. The CP log has
    `GET /internal/engine/catalog 200` but **no `POST /internal/engine/token` at all** — i.e.
    `BuildLaunch` was never reached. opencode's **managed route runs every session in a
    workspace through one shared `opencode serve` daemon**, whose environment comes from
    `auth.go`'s `env()`. A token put on `LaunchPlan.Env` reaches the tmux route and nothing
    else, so `{env:AF_ENGINE_TOKEN}` stayed empty. Fixed by putting it in `env()`.
    `env()` also had an **early return when no provider keys are stored**, which dropped the
    token for exactly the free-tier and Console-login workspaces (the ordinary case); the
    stored keys and the fleet's own engine are independent, so that went too.
    🔴 **The consequence is that open question 2's premise — per-session attribution — does not
    hold for opencode's default route.** A daemon has no session, so the managed route's token
    is **workspace-scoped** and its usage rows attribute to the member but not to a session;
    only the tmux route keeps session scope. The token's life also went from 24 hours to
    **30 days**: a daemon reads `{env:…}` once at start, so a 24-hour token turns into a 401 in
    the middle of somebody's work that only a daemon restart clears.

Also measured while writing P0:

- **S3 to a box runs at 115-147 MB/s** (18.5 GB in 126 s and in 161 s). The same range as the
  harness's 104-147 MB/s; a fifth data point has not broken "it does not depend on the file".
- **A server-side S3-to-S3 copy moves 17.3 GiB in 52 seconds** (same region, free). Moving an
  already-ingested model between buckets is not comparable to the 31-74 minutes of fetching it
  from Hugging Face again.
- **`crane copy` from GHCR to ECR: 2.59 GB in 179 seconds** (from this container).
- **The launch-menu half** is pinned against the real opencode 1.18.29: handed the config
  `WriteEngineProviders` writes, `opencode models` lists `llamacpp/qwen3-coder-30b-a3b`. The
  engine's host in that test **does not exist**, because the property being checked is that the
  CLI never contacts the provider — the menu is drawn while the engine is asleep (a
  `clicontract`-tagged test).
- 🔴 **That CloudFormation is not putting the desired count back to 1 was confirmed from
  CloudTrail.** Lining up the `UpdateService` calls, the two that replaced the task definition
  both left `desiredCount` **unset**, and the only calls writing `1` were this work's own
  measurement scripts. Answering "did it start by itself?" took both the service's `createdAt`
  (not replaced) and CloudTrail.
- The stub test (`deploy/local/ecs-lifecycle-stub-test.sh`) gained a case 3g pinning the order
  (20 → images → 60 → 30), `CAPABILITY_NAMED_IAM`, the key generation, scaling a newly created
  service to 0, leaving an existing one alone, and not putting the generated key in an argument.

## What P1 measured (2026-09-07)

The `image` role and the `sdcpp` provider were written against the same af-sandbox stack, with
the role actually deployed and driven. One P0 expectation held — **the image role fits inside
one attempt** — and one broke: 🔴 **an MI box does not appear in a `describe-instances`
listing**, so the check P0 relied on to say "no GPU is running" was not evidence of that at
all. The exercise cost 15 minutes of g6.xlarge (launched 13:32:05Z, terminated 13:47:04Z), roughly $0.31.

That evening the CP and the Workspace were rebaked and **the other half — the `generate_image`
path — was driven on real hardware too**. That is measurement 9 onwards, and **two more
expectations broke** there: decision 5's "the CP holds the request" is good for 60 seconds on a
non-streaming request (9), and the failure fell through onto a member's plan quota without
saying so (10). The second g6.xlarge came up at 15:26, again about $0.3. Measurement 13 (after the fix) and 14
(a claude session a user drove themselves) close the definition of done on real hardware.

1. **The image role's cold start is 197 seconds** (`execute-change-set` to
   `listening on: http://0.0.0.0:8080`; image in ECR, checkpoint in S3, no box). Broken down:
   **+57 s to task creation**, +61 s for the box to register as a container instance, pull
   **55 s** (from ECR, against the 135 s the P0 harness spent pulling from GHCR), S3 fetch
   **6.94 GB in 65 s = 107 MB/s**, and **listening at +197 s**. That is a third of the llm
   role's 586 s and it **fits inside opencode's 300 seconds** — measured-and-settled point 9's
   "the image role, on sd-server, fits in the first attempt at 195 s" was right. The whole
   stack update, service creation included, took 5 minutes 4 seconds.
2. 🔴 **An MI box is absent from `ec2 describe-instances` listings; asked for by id, it is
   there.** While the task was RUNNING and `describe-container-instances` was returning
   `i-08a9…/af-af-ecs-engines-image`, calling `describe-instances` **with no filter** and
   counting the raw JSON gave three instances (the slot pool's m8g/m7i) and not that id. Yet
   `describe-instances --instance-ids i-08a9…` returns **g6.xlarge / running /
   `af-role=engine-image`** (`OwnerId` is our own account, `RequesterId` is AWS's, and the tags
   carry `aws:ec2:fleet-id` and `aws:ec2:managed-launch=ecs-managed-instances`). Two
   consequences:
   - **"`describe-instances` shows none, so no GPU is running" does not hold.** P0's
     measurement 10 and the operating notes are written that way, so this corrects them. What
     does confirm a stopped box is **`describe-instances --instance-ids <id>`** (take the id
     from ECS's `describe-container-instances`) or watching the container instance disappear on
     the ECS side. Another "zero results" that had to be doubted at the tool, not the fact.
   - The cost-allocation tagging works — the box carried `af-role=engine-image` (decision 9).
3. **sd-server's surface works over the production path.** From a throwaway Fargate task placed
   in the CP's security group (the production task role gets no `ssmmessages:*`, so this is
   P0's technique again): `GET /v1/models` answered **200 in 4.6 ms**,
   `/v1/images/generations` took **11.2 s at 512px (first call) and 17.5 s at 1024px**, `"n":2`
   at 512px took **10.7 s and returned two images** — 5.4 s each once warm, so the first call's
   11.2 s includes warm-up — and `/v1/images/edits` (image plus mask, multipart) took **5.2 s
   at 512px**. **No key is needed** (200 with no header at all). P0's 7.8 s / 20.8 s were a
   different box on a different day, so all that can be said is a band: **5-11 s at 512px,
   17-21 s at 1024px**.
4. ✅ **`size` is honoured.** The PNG returned for a 1024x1024 request has IHDR
   `00 00 04 00 00 00 04 00` = **1024×1024**, and a 512x512 edit came back **512×512**. ADR
   0069 decision 7's "size is a wish, not a promise" is the permanent story for the Codex
   route, but **this provider returns what was asked for** — which is why `Caps.Sizes` is a
   per-checkpoint list here rather than the empty one that means "the caller cannot pick".
5. **VRAM is 6,624 MB of params** (the same figure as P0). auto-fit put DiT 4,897 MiB,
   Conditioner 1,559 MiB and VAE 159 MiB all on CUDA0, against the L4's 22,369 MiB free. Add
   the llm role's 20.9 GB and it does not fit — decision 2's grounds, measured again on
   another box on another day.
6. **Drain: 71-164 seconds to shutting-down, 477 s to terminated.** At +71 s after
   `desired 0` the box was still ACTIVE; by +164 s it was `shutting-down`. That is the same
   band as P0's "shutting-down begins at +95-104 s", agreeing as a third point.
7. **The CloudFormation change is four additions and two modifications, and it does not touch
   the llm role.** The change set adds `ImageCapacityProvider`, `ImageTaskDef`,
   `ImageDiscovery` and `ImageService`, and modifies `Associations` (the provider list is
   replaced, so it shows up every time) and `EnginesParam`. `LlmService` does not appear at
   all, and llm's desired count stayed 0 — decision 8(a)'s "do not declare `DesiredCount`"
   holding through an update that adds a second role.
8. **The engine table now has two rows**, and the **literal string** SSM was given is what the
   CP's parser test parses — trailing spaces from CloudFormation's folded scalars included. The
   shape a test has to survive is the one CloudFormation emits, not the one a person would type.

9. 🔴 **Driving `generate_image` on a real deployment showed the other half of the definition
   of done — "one call returns a picture" — does NOT hold on the first attempt.** The CP and
   the Workspace were rebaked as `0.16.1-dev-f4a12675` and deployed to af-sandbox, and
   `workspace-agent mcp-stdio --image-gen` was driven from the real workspace (opencode session
   `sh7gxia`). The first call to a stopped image engine, in full (all times 2026-09-07 UTC):
   - 15:26:49 the request. **sdcpp is not streaming**, so the gateway takes the `plain` path.
   - 15:26:50 the CP logs `engine image: started on demand`.
   - **15:27:49 the CP answers `POST /engine/image/v1/images/generations` with 503, in
     `59.998s`.**
   - 15:29:35 the CP logs `engine image: warmed up (ready)` — **the start itself took 165
     seconds**, the same band as P1 measurement 1's 197 s (a second point).
   Sixty seconds flat did not come from the CP, whose own bound is 900. It came from
   **30-ingress's ALB, `idle_timeout.timeout_seconds: "60"`** (confirmed on the live load
   balancer): a connection that has carried no response byte for 60 seconds is closed.
   **The llm role never hit this because of the SSE comment line every 10 seconds** — written
   for opencode's 300-second ceiling, and it turns out to have been defeating the ALB's idle
   timeout at the same time. P0's "512 seconds in one attempt" was riding on that side effect.
   The fix (decision 5's 🔴): the non-streaming hold is folded below whatever sits in front,
   via `AF_ENGINE_PLAIN_HOLD` (default 45 s), and answers `503 engine_waking` + `Retry-After`.
   The one that keeps asking is the `sdcpp` provider, inside its own 16-minute budget, and it
   **rebuilds** rather than replays the request — an edit's body is a multipart document the
   first attempt already read to the end, so a replay would send an empty body on the one
   endpoint that is not JSON. **What a caller sees — one call, one picture — is preserved by
   moving where the waiting happens.**

10. 🔴 **That 60-second failure fell through onto a member's plan quota, silently.** Two ledger
    rows belong to the one tool call: `kind:"sdcpp"` `ok:false` `ms:60000`, and 21 seconds
    later `kind:"agy"` `ok:true` `ms:20880` `images:1` `pixels:1048576`. `auto` is an ORDER of
    ready providers, so the second one runs when the first fails — as designed, but it **spent
    a member's Antigravity quota on an image the fleet's own hardware was two minutes from
    serving**. The reason sdcpp is first in that order (ADR 0069, whose wallet pays) is exactly
    what the fallback inverts. Decision 5's fix closes this path too, since the call no longer
    fails at 60 s. ⚠️ The fallback itself was kept — it is right on a deployment whose engine
    really is down, and refusing would only mean no picture. What was removed is the SILENCE:
    a generation served by a fall-through now carries a warning naming what failed ahead of it
    and saying that it ran on a different account's plan. Whose wallet pays is the one thing
    the built-in order decides, so inverting it quietly is the part that was wrong. Decision
    5's fix also makes "would have worked if we waited" much rarer: the provider now exhausts
    its own 16-minute budget, so a failure there means genuinely unavailable.

11. ✅ **Warm, the whole path works.** From the same session, through the CP gateway, the
    provider and the MCP tool:
    - **generate at 1024×1024 in 23.4 s** (`auto` chose sdcpp; the PNG came back at 1,073,204
      bytes with an IHDR of 1024×1024). **Exactly two progress notifications, 10 s apart**
      (gaps 10.0 / 10.0 / 3.4 s).
    - **edit (multipart) in 5.3 s** and **inpaint (multipart + mask) in 5.0 s**, both 512×512.
      **This is the first time the gateway's Content-Type passthrough met the real thing**, and
      the boundary survived — the third of P1's additions to the body, now measured.
    - The CP logged one line each: `200 23.396s` and `200 5.236s`.
    - **The faces that answer without waking anything were confirmed too**: with zero boxes
      running, `/imagegen/status` returns `provider:"sdcpp"` `ready:true`
      `model:"sdxl-base-1.0"` `ops:[generate,edit,inpaint]` `order:["sdcpp","agy","codex"]`.
      `tools/list` advertises a `provider` enum of `["sdcpp","agy"]` and an `op` enum of
      `["generate","edit","inpaint"]`.

12. ✅ **The ledger is what decision 9 said.** In one day's raw file: **5 `tool.imagegen` rows
    and 0 `engine.image` rows**. The image rows carry `images` and `pixels` (1024² as
    `1048576`, 512² as `262144`), `ref` is the session name, and `measured:"none"` — no token
    exists on this route, and that is not written as zero (the same rule ADR 0069 took).
    ✅ **It also settled one of P0's leftovers: the `engine.llm` rows DO reach the ledger** —
    **55 of them** the same day, with `in`/`out` and `measured:"exact"`, `kind:"opencode"`.
    That is the CP→Agent POST fixed to use `newAgentTransport()` in P0 measurement 11, working
    on the real thing.

13. ✅ **Re-measured after the fix, the other half of the definition of done holds on real
    hardware (2026-09-08).** `0.16.1-dev-2e501835` (decision 5's split) was deployed and
    `generate_image` called once from **zero boxes** — with `sdcpp` named explicitly, so that a
    failure could not spend a member's quota through the fall-through of measurement 10. In UTC:
    - 01:03:30 the request → `engine image: started on demand`
    - 01:04:15 `503 45.012s`, 01:05:06 `503 45.007s`, 01:05:57 `503 45.007s` — **folded three
      times, each below the front end's 60 seconds**
    - 01:06:22 `warmed up (ready)`. **Cold start 172 seconds** (a third point in the same band
      as measurement 1's 197 s and measurement 9's 165 s)
    - 01:06:41 **`200 38.547s`**
    - **One tool call, 191.6 seconds.** 19 progress notifications, every gap 10.0 s, and the
      answer was 1024×1024, 1,073,204 bytes, `provider:"sdcpp"`.
    None of the three retries surfaced above the tool. **"One call returns a picture" holds,
    once the waiting is moved from the CP to the caller.** edit (**5.2 s**) and inpaint
    (**5.0 s**), both 512×512, passed on the same build, and that day's ledger holds
    **three `tool.imagegen` rows, all `kind:"sdcpp"` and `ok:true`** — **no agy row**, i.e. no
    fall-through happened. `engine.image` is still 0 rows.

14. ✅ **The same machinery held on the claude route (2026-09-08, one call a user actually
    made).** Right after the P1.5 toggle was deployed (`0.16.1-dev-c346ad66`) the user had a
    **claude session generate an image over MCP**. The engine was stopped, and the CP log took
    the same shape as 13: 02:22:39 `started on demand` → **`503 45.004s`, `45.002s`,
    `45.003s`** → 02:25:32 `warmed up (ready)` → 02:25:38 **`200 26.325s`** (about 179 seconds
    from the request). Three warm ones followed at `200 5.545s`, `5.436s` and `5.423s`.
    - **Cold start is now 165 / 172 / 173 / 197 seconds across four points.** The band holds.
    - What makes it worth recording is the KIND. claude puts no ceiling on an MCP tool call
      (measured: claude none, codex 300 s, opencode 60 s), so this is the route that does NOT
      depend on the progress heartbeat — and it still took three folds and one answer. Decision
      5's fix is therefore working against the FRONT END's 60 seconds rather than against any
      one client's habits, which rules out 13 having ridden on something opencode-specific.
    - ⚠️ This was not an instrumented run but a user going about their work, so the tool-side
      duration and the notification count were not observed. Only the CP log was.

Also verified in P1:

- **ECS Exec is usable as a harness, but its pty dies on stdin EOF.** Giving
  `aws ecs execute-command --interactive` a `</dev/null` makes a long call **disappear along
  with the session the moment the request is sent** (which is how the first measurement was
  lost). Detach with `setsid`, write to a file, and peek with short execs; `nohup` alone is not
  enough. Run Python with `-u`, or buffered output dies with the process. Note the Workspace
  task role has **no `ssmmessages:*`** — correctly so — hence a temporary inline policy
  `af-adr0071-p1-temp-exec` alongside `enableExecuteCommand`, **both removed afterwards**.
- **A follow-on to "don't read a `describe-instances` listing"** (measurement 2): the engine box
  was again only findable on the ECS side, through `describe-container-instances`.
- **Copying GHCR → ECR took 177 seconds for 2.42 GB** (`crane copy` from this container), the
  same rate as P0's llama.cpp (2.59 GB in 179 s).
- **The `image` role's ECR repository (`af-sdcpp`) lives in 20-platform**, because a repository
  created inside 60-engines would be empty when that same stack's service first tries to pull
  and the CREATE would never converge — the third instance of the shape `af-llamacpp` and
  `af-voicevox` are there for. The copy itself only runs when `ImageModelS3Key` is set: there is
  no reason to pull 2.3 GB into a deployment that only wants an LLM.
- **`60-engines.yaml` hit the 51,200-byte wall.** Adding the image role took it to 55,832 bytes
  and the stub test's gate (case 3b-2) failed. Treated the way 30-ingress was: the long-form
  parameter prose moved to `cfn/PARAMETERS-60-engines.md`, taking the template back to 50,779
  bytes with nothing shortened. `af_cfn_deploy` would have handed it over through S3 anyway, but
  finding out on the day of a deploy is late.


## A correction after P1 — nobody was ever told the engine's window (2026-09-08)

The provider block P0 and P1 shipped writes nothing but `{"name": …}` for the model. What using
it on a real box showed is that this is **not read as "no window declared" but as "the window is
zero"**.

🔴 **Measured against opencode 1.18.29** (af's own provider shape written into an isolated HOME,
then `GET /config/providers` read back): `qwen3-coder-30b-a3b` comes back as
`limit={context:0, output:0}`. Two consequences, both silent:

- **opencode DISABLES auto-compaction when the context is 0.** The conversation grows until the
  `llama-server` started with `-c 32768` rejects the request. Nothing says so on screen.
- **The usable window is `context − output cap`, and an output cap of 0 is not "unset" — it
  substitutes 32000** (`var M7=32000`). So declaring the context alone would leave
  32,768 − 32,000 = **768 tokens** and compaction thrashing from the first turn. **Neither is
  better than one of the two.**

The fix keeps decision 8's shape — declared, never derived. `-c` was buried inside
`LlmExtraArgs`, where CloudFormation cannot read it back out, which is why the engine table could
not carry it. `LlmContextTokens` / `LlmMaxOutputTokens` are split out and the same value feeds
**both** the command line's `-c` and the table. The CP publishes them only when `contextTokens`
> 0, and the Agent writes opencode's `limit` only when both are present (one alone leaves the old
behaviour: no limit at all).

**The window belongs to the ENGINE, not to a model.** One `llama-server` process serves one GGUF
with one `-c`, so the ids listed in `LlmModelIds` are aliases sharing that one window. Two models
with different windows are **two rows** — and ⚠️ **two provider ids**, because the Agent keys
opencode's `provider` block by provider id (`opencode/engine.go`) and a second `llamacpp` would
silently overwrite the first.

It surfaced through a report that "WebFetch does not work with qwen3-coder-30b-a3b in the
sandbox", and **that report's cause was something else**: opencode truncates tool output at 2000
lines / 51,200 bytes and **never cuts inside a line**. Google News's RSS is a single 89 KB line
with no newline in it, so the body handed to the model was **0 bytes** — all that survived was the
instruction to have the Task tool's explore subagent read the saved file, which the 30B local
model could not carry through; it finished by writing an untrue conclusion ("check your internet
connection"). The fetch itself returned 200. That is opencode behaving as specified and outside
this ADR, but **the window is a separate, real hole found while looking into it**.

## Phases

- **P0 — the substrate, and llm.** `60-engines.yaml` (capacity providers, S3, the ingestion
  task, the llm service), stand-up / update / teardown / capture-env, copying the official
  images into ECR, the CP gateway `/engine/llm/v1/*` (wake-and-hold, streaming, usage
  recording), the generalised controller, the opencode provider injection. Done means:
  **`llamacpp/<model>` appears in opencode's launch menu, the first request to a stopped
  engine is answered within one attempt (no re-send), and the instance is gone after 30 quiet
  minutes** — "within one attempt" is the only observation that verifies the replaced
  decision 5. P0 measures: the cold start with `useLocalStorage`, drain with `scaleInAfter` 0
  and −1, a re-request during drain, the pull from ECR, and that the pool walks exclude MI. llm goes first not because the request led with images
  but because **it validates the substrate with the least application code** (zero changes on
  the opencode side).
- **P1 — image (sd-server). Implemented** (2026-09-07; see "What P1 measured"). The `image`
  role's service, the Agent provider `sdcpp` (transport via the CP gateway — 0069 decision 3's
  "the tenant layer goes through the CP"), `Caps` per (provider, model file), generate / edit /
  inpaint, progress notifications. Three things the implementation added to this text:
  - **The engine table gained `api` (`chat` / `images`).** A role's nature is declared rather
    than derived from its key, and it decides two things — whether the Agent writes the engine
    into opencode's config as a provider (writing an image engine there puts a model you cannot
    converse with in the launch menu), and whether the gateway counts the usage (`chat`) or the
    Agent's `tool.imagegen` row does (`images`; decision 9).
  - **The gateway forwards the caller's `Content-Type`.** `/v1/images/edits` is multipart and
    the boundary lives in that header — stamping `application/json` over it breaks the one
    surface that is not JSON.
  - **The `image` role has no `--api-key`.** sd-server has no authentication mechanism at all
    (upstream `examples/server/api.md`), so the security group is the whole of its access
    control, and decision 4(d)'s "second lock" is an llm-role-only story.
  The definition of done was **measured in full on real hardware** — the engine side in 1, 3
  and 4 below, the CP gateway and the Agent provider in 9-12 (`0.16.1-dev-f4a12675` deployed to
  af-sandbox, `generate_image` called from a real workspace's opencode session). 🔴 **The first
  attempt failed** — an ALB cuts a non-streaming request at 60 seconds (measurement 9) — which
  added one fix to P1: decision 5 now splits by role.
- **P1.5 — the two holes real hardware found (2026-09-08).** Decision 5's split (measurement
  9), making the fall-through say so (measurement 10), and **an on/off control in the
  Console**. The last is not a new mechanism: the mode has always been the stored setting
  `engine_<key>_mode`, which the gateway (`503 engine_off`), the catalogue (the engine
  disappears) and the controller (stop it and keep it stopped) all read. Only the WRITING half
  was missing, so the only way to switch an engine off was a stack parameter (`LlmMode` /
  `ImageMode`) — a CloudFormation run, which is not what anyone reaches for while a GPU is
  misbehaving. `GET /api/admin/engines` and `PUT /api/admin/engines/{key}` were added behind
  super_admin, the same shape as TTS's `/api/admin/tts`. One thing differs: **Disabled stops
  the box immediately.** TTS debounces it because a mistaken OFF→ON there costs a 2 GB pull and
  80 seconds; a GPU is $1.26/hour and an undo window is time you pay for.
  🔴 One defect surfaced while building it: `engineRuntimeState.mode` **only consulted the
  stored setting when a controller existed**. Harmless in production, where one always does,
  but it made "what mode is this engine in" depend on an unrelated collaborator. The setting is
  now held by the state itself.
  **The current state was then added to the same screen (decision 13)** — a toggle on its own
  does not tell anyone whether it is safe to switch an engine off. The `GET /api/admin/engines`
  row gained the box's start time, the automatic stop time, recent demand and `warm`, and
  `GET /api/admin/engines/{key}/hourly` plus the new `engine_hourly` table drive an occupancy
  heatmap. The sampler is the controller's own tick, because **engine uptime was recorded
  nowhere at all** until then. The details — never drawing unobserved as stopped, storing the
  denominator, omitting a field rather than guessing it — are in decision 13.
- **P2 — ComfyUI.** The fleet's image, the `/engine/comfy/` pane, the `comfy` provider with its
  workflow template, mutual exclusion with sd-server.
- **P3 — llm for codex and claude.** codex via `model_providers` with `base_url` and
  `wire_api = "responses"`, claude via `ANTHROPIC_BASE_URL`. Both **change what choosing a
  model means** (billing moves from the user's login to the fleet's box), so the launch menu's
  presentation and consent are designed first. llama.cpp's `/v1/messages` has a known issue
  dropping thinking blocks (#20090), and Claude Code sends many background Haiku requests.
- **P4 — cheaper, with evidence.** MI Spot, shrinking `image` to a g6f (g6f.2xlarge only after
  `--offload-to-cpu` and quantisation are measured), several models through
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

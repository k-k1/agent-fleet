# 0072. A model catalogue for the engines — swap the LLM and SDXL models without a CloudFormation run, and pick LoRAs per request

English | [日本語](0072-engine-model-catalog.ja.md)

- Status: **adopted — P0 may start (revised the same day after the 2026-09-08 review; drafted the same day).** Every number
  below is quoted from ADR 0071's measurements; the upstream facts (llama.cpp,
  stable-diffusion.cpp) were read the same day from the repositories' `tools/server/README.md`,
  `examples/server/api.md` and `docs/lora.md`. **Nothing was newly measured for this document**
  — what has to be measured before a decision can stand is listed under *Open questions*, and
  each decision names the question it depends on.
- Revised the same day: **vLLM and ComfyUI were weighed, and the licence, gating and file sizes of
  the candidate models (SD3 / SD3.5 / FLUX.1 / FLUX.2 klein / Z-Image / Qwen-Image) were taken
  from the HF API** (Context: "Candidate models", "vLLM and ComfyUI"). Three consequences:
  **ComfyUI becomes the image role's main engine and its phase moves forward** (decisions 4, 5,
  10; phase P2), decision 4 gains the rule that **a model whose generation exceeds 60 seconds
  cannot be served over a synchronous API**, and **vLLM is rejected** (with the conditions for
  revisiting). The sd-server LoRA path (`<sd_cpp_extra_args>`) is demoted to a fallback.
- The same day, **ComfyUI was measured on the dev deployment's GPU (g6.xlarge, L4)** (*Resolved by
  measurement*; the harness is `deploy/aws/ecs/harness/bench-image-engine.sh`). klein 4B,
  Z-Image-Turbo and SDXL all draw on one box, a warm picture takes 4–11 seconds, and **a switch
  is a re-read from EBS, 1–2.5 minutes**. Open questions 7 and 8 were filled in and decision 10's
  default became klein 4B. 🔴 Two of the runs had to be repeated — `--cache-none` makes every
  prompt re-read the weights from disk, and a second prompt with the same seed hits the output
  cache and "runs" in 0.5 s. Neither was visible until the numbers were.
- The same day, the **review before P0** landed at the end ("Review (2026-09-08, before P0)") and
  the text was revised on it. Four premises fell — 🔴 **the CP task role holds no S3 permission
  at all** (decisions 6 and 7 had the CP reading manifests and deleting S3 files), 🔴 **SSM's
  Standard tier caps a value at 4,096 characters** ("a few KB" was wrong), 🔴 **decision 5's
  "the CP refuses the request" contradicts decision 4's "the gateway does not read the body"**,
  🔴 **decision 10's "klein 4B is the default" is a ComfyUI measurement, not an sd-server one**
  (P0's default is SDXL). Added: three conditions to decision 1 (a wrapper on both roles,
  `ParameterNotFound` means empty, `mode=on` with an empty catalogue does not start), the 4 KB
  rule to decision 2, the PassRole scope and a sharper ADR 0071 decision 8 to decision 6,
  `warm_model` to decision 7, `license_name`, acceptance records and a commercial-use axis to
  decision 10. **The review settled open question 1 entirely on a CPU** (R1). The status became
  adopted.

## Context

ADR 0071 shipped the foundation — inference on the fleet's own boxes — in P0 and P1. The `llm`
role runs Qwen3-Coder-30B-A3B and the `image` role SDXL base 1.0, **one model each**. The
request is: make the models swappable, and make LoRAs applicable.

### What the code says today

- **A model is a CloudFormation parameter.** `60-engines.yaml` has `LlmModelS3Key` /
  `LlmModelFile` / `LlmModelIds` / `LlmContextTokens` / `LlmMaxOutputTokens` / `LlmExtraArgs`, and
  the image role's mirror `ImageModelS3Key` / `ImageModelFile` / `ImageModelIds` /
  `ImageExtraArgs`. They flow into three places: the task definition's command line
  (`-m /models/<file>`, `--alias`, `-c`), the fetch sidecar's `aws s3 cp`, and the SSM engine
  table the CP reads (`models[]`, `contextTokens`). **Swapping a model means editing
  `params/60-engines`, running the ingest task by hand, and updating the stack with
  `standup.sh` or `update.sh`** (`deploy/aws/ecs/README.md`, "Getting a model into the
  catalogue").
- **The CP reads the engine table once, at startup** (`engines.go`: "Read ONCE, at startup, on
  purpose"), on the premise that the table only changes when the stack does and a stack change
  replaces the CP task. The Agent reads `/internal/engine/catalog` at boot, writes the chat
  engines into opencode's `provider` block, and caches the answer for 10 minutes.
- **The `sdcpp` provider sends no `model`.** `sdcpp.go`: "the server holds ONE model, chosen by a
  startup flag, and has no way to switch at request time". `Caps.Sizes` is **guessed** from the
  model id string (`xl` / `1-5`). `generate_image` takes op / provider / prompt / size /
  aspectRatio / background / count / inputs / mask — **neither model nor lora**.
- **The gateway passes path and body through verbatim** (`dial()`: "passed through verbatim");
  the only time it reads the body is to add `stream_options.include_usage`.
- **The window is per engine** (ADR 0071, "the correction after P1"): llama-server is one
  process, one GGUF, one `-c`, so the several ids in `LlmModelIds` are aliases of the same
  window, and two models with different windows are **two rows and two provider ids**.
- **The 51,200-byte wall.** `60-engines.yaml` is 1,065 lines and sits near the wall even after
  its prose moved to `PARAMETERS-60-engines.md` (the 9.2 KB left in `30-ingress` is why the
  engine table went to SSM — decision 8). **A design that adds CloudFormation resources or
  parameters per model was never available.**

### What upstream provides (checked 2026-09-08)

**llama-server (llama.cpp)**

- **Router mode.** `--models-dir PATH` (file names are model names; mmproj and multi-shard
  models take the subdirectory's name), `--models-preset PATH` (an INI: `[*]` for shared
  defaults, `[<id>]` per model, keys being command-line argument names — `c`, `n-gpu-layers`,
  `lora`, `alias`, `model` (path) — plus the preset-only `load-on-startup`, `stop-timeout`,
  `dedup-cache-models`), `--models-max N` (models loaded at once; **default 4, 0 = unlimited**),
  `--models-autoload` (default on: "the model will be loaded automatically if it's not loaded").
  A request is routed by its `model` field (`?model=` for GET), and `GET /models` returns each
  model's `status: {value: "loaded" | …, args: […]}`. Precedence: command line > the model's
  preset > `[*]`.
- **LoRA.** `--lora FNAME` (comma-separated for several), `--lora-scaled FNAME:SCALE`,
  `--lora-init-without-apply`, `GET /lora-adapters` (`id`, `path`, `scale`), `POST /lora-adapters`
  (global scales), and a per-request `lora: [{"id":0,"scale":0.5}]` ("Requests with different
  LoRA configurations will not be batched together").
- 🔴 What the README does not say: whether the router's `/health` is ok with zero models loaded,
  whether a request during autoload **waits** or is refused, **which** model is evicted past
  `--models-max`, and how child processes and ports are allocated. Decision 3 depends on
  measuring those four.

**sd-server (stable-diffusion.cpp)**

- **One process, one checkpoint. No runtime switch.** `model` has no meaning on
  `/v1/images/generations`; `GET /v1/models` returns a fixed `sd-cpp-local`; `GET
  /sdapi/v1/sd-models` lists the one that is loaded; there is no `POST /sdapi/v1/options`.
- **LoRAs go through structured fields.** They live in `--lora-model-dir DIR`, are listed by
  `GET /sdapi/v1/loras` and by `loras[]` of `GET /sdcpp/v1/capabilities` (`name`, `path`), and a
  request names them as `lora: [{"path":…, "multiplier":…, "is_high_noise":…}]` on the sdcpp
  API. **The OpenAI-compatible face takes them too**: a `<sd_cpp_extra_args>{"lora":[…]}
  </sd_cpp_extra_args>` block inside the prompt is extracted, parsed with the sdcpp API's rules,
  and removed from the prompt before generation (`sample_params.sample_steps`, `seed` and
  `negative_prompt` travel through the same hole).
  🔴 **The `<lora:name:1>` prompt syntax is deliberately dead on the server** (api.md:
  "intentionally unsupported in OpenAI API, sdapi, and sdcpp API"). The CLI's `docs/lora.md` uses
  that syntax, so anything written from CLI knowledge is **silently ignored**.
- **How a LoRA is applied**: `--lora-apply-mode immediately | at_runtime`. The default is
  automatic — `at_runtime` if the weights contain any quantised parameter (compatibility and
  precision; slower, more VRAM), otherwise `immediately` (merged into the weights at load;
  fast). SDXL fp16 lands on the latter. **The cost of a request whose LoRA set differs from the
  previous one (a re-merge?) is not documented** — the measurement decision 5 depends on.
- **Split models.** FLUX and SD3 are several files: `--diffusion-model` + `--clip_l` + `--t5xxl`
  + `--vae`. `--type` quantises at load, `--offload-to-cpu` moves weights out of VRAM (the same
  unmeasured pair as ADR 0071 decision 2, P4).
- `/v1/images/generations` is synchronous ("synchronous from the HTTP client's perspective");
  the async `/sdcpp/v1/img_gen` is unusable in a container (ADR 0071's measurement).

**ComfyUI (planned as P2)** reads `models/checkpoints`, `models/loras`, `models/vae`,
`models/text_encoders`, `models/diffusion_models`, and picks checkpoint and LoRAs per workflow.
ADR 0071 decision 6 already makes **that layout the shared model-store convention of all three
engines**.

### Candidate models (checked against the HF API, 2026-09-08)

Licence and gating are `cardData.license` / `license_name` / `gated` of
`https://huggingface.co/api/models/<repo>`; sizes are `siblings[].size` with `?blobs=true`.
**The weight on an L4 (22.9 GB) is an estimate everywhere except ADR 0071's measurement (SDXL
fp16 7.4 GB), and carries a ★.** So is the generation time — the only measurements are SDXL's
21 s (sd-server) and 8 s (ComfyUI).

| Model | Licence | Gated | On an L4 | Verdict |
|---|---|---|---|---|
| SD1.5 | OpenRAIL | no | light | not offered |
| SDXL 1.0 | OpenRAIL++-M | no | fp16 7.4 GB measured | stays the workhorse; largest LoRA ecosystem |
| SD3 Medium | Stability Community | yes | light | not offered — superseded by 3.5, 4.7k downloads |
| SD3.5 Medium (2.5B) | Stability Community (commercial use under $1M revenue) | yes | 5 GB + T5-XXL fp8 ≈5 GB★ | candidate; fits as fp16 |
| SD3.5 Large (8B) | same | yes | fp16 16 GB + T5 → fp8 / GGUF★ | quantised only; Turbo is 4 steps |
| FLUX.1-schnell (12B) | Apache-2.0 | **yes** | 23 GB → fp8 / GGUF★ | fast at 4 steps; most LoRAs target dev |
| FLUX.1-dev / Kontext-dev | flux-1-dev-non-commercial | yes | as above, 20–28 steps ≈60–90 s★ | best quality and LoRAs, but **non-commercial** and the **60-second rule** (decision 4) |
| FLUX.2-dev (32B) | flux-non-commercial | yes | does not fit | out |
| **FLUX.2 [klein] 4B** | **Apache-2.0** | **no** | 7 GB + 7 GB text encoder★ | **candidate for the first default** |
| FLUX.2 [klein] 9B | flux-non-commercial | yes | quantised only★ | behind the 4B |
| **Z-Image-Turbo (6B)** | **Apache-2.0** | **no** | ≈12 GB bf16★ + a Qwen-family encoder | **candidate for the first default**; 8 steps |
| Qwen-Image (20B) | Apache-2.0 | no | 4-bit + a 7B encoder★ | strong text rendering, heavy; later |

Three readings. **Apache-2.0 and ungated is exactly three models — FLUX.2 klein 4B,
Z-Image-Turbo, Qwen-Image**; FLUX.1-schnell has become gated while staying Apache-2.0. **12B
and up is quantised on an L4**: the catalogue holds fp8 or GGUF files, and `text_encoders/`
(T5-XXL, CLIP-L) is shared between SD3.5 and FLUX.1 (decision 2). **A model whose generation
exceeds 60 seconds cannot be served over sd-server's synchronous API** (decision 4).

stable-diffusion.cpp's support (README): SD3/SD3.5, FLUX.1, Qwen-Image (2025-10-12), FLUX.2-dev
(2025-11-30), Z-Image (2025-12-01), FLUX.2-klein (2026-01-18). **It follows — always behind**,
and every official recipe is published as a ComfyUI workflow.

### vLLM and ComfyUI

**vLLM is not adopted now** (reasons under *Options rejected*). In short: **one process, one
model, no router** (a switch is a restart; llama.cpp's router switches in-process), **a 9.7 GB
image** (Docker Hub `vllm/vllm-openai:latest`, compressed — four times llama.cpp's 2.47 GB, so
ADR 0071's 178-second pull grows with it), **30B-class hardly fits an L4** (AWQ / GPTQ / FP8
rather than GGUF; Qwen3-Coder-30B-A3B's official FP8 is about 30 GB), and **its strengths do not
apply** (continuous batching and multi-LoRA pay off on concurrent requests to the same model;
the fleet's engine sleeps most of the time and serves a few sessions while awake).

**ComfyUI becomes the image role's main engine** (decisions 4, 5, 10). ADR 0071's measurements
are the reasons: **2.5× faster on the same L4** (7.9 s against 20.8 s), **checkpoint and LoRAs
chosen per workflow with loaded ones cached** (both sd-server's "one, at the next start" and
the unmeasured LoRA-switch cost disappear), **an asynchronous API** (`/prompt` returns at once,
`/history` is polled in short calls, so the ALB's 60-second idle never applies), and **new
models land there first**. The price: a self-built image, a workflow template per model
family, a `comfy` provider, and an API contract that is not versioned the way OpenAI's is (node
names move between versions — pin the tag and freeze the templates behind golden tests).

## Decisions

1. **A model is data, not a stack resource.** The stack owns the role's **vessel** — capacity
   provider, service, SG, bucket, task definition, ingest task — and **what goes into it is the
   catalogue's business**. The twelve parameters `*ModelS3Key` / `*ModelFile` / `*ModelIds` /
   `*ContextTokens` / `*MaxOutputTokens` / `*ExtraArgs` are on their way out (kept one release
   for compatibility and read as the **seed** of an empty catalogue, decision 7). Three
   reasons: (a) the 51,200-byte wall — six parameters per model, or a service set per row,
   neither fits; (b) a swap is an operational act, not a deployment act — what an admin flips
   in the Console while the GPU is asleep must not be a CloudFormation change set; (c) ADR 0053's
   "declare, never derive" does not say **who** declares. Moving the declaration from CFN to the
   CP's catalogue keeps the rule that the engine is never asked (decision 2).
   - **The two-pass stand-up (ADR 0071 P0's measurement) goes away.** Today a service whose model
     is not in the bucket never stabilises, so `<Role>ModelS3Key` empty = no service on the first
     pass, then ingest, then deploy again. With a catalogue, the fetch sidecar treats "nothing to
     load" as **success**, and the engine container starts through **the same wrapper on both
     roles** (review, decision 1(a)): the sidecar writes `/models/cmdline` from the active set,
     and the container runs `sh -c 'if [ -s /models/cmdline ]; then exec <engine> $(cat
     /models/cmdline); else sleep infinity; fi'`. Every image has `sh` (R5). While P1 is deferred
     the llm role stays on `-m`; the day it moves to the router, only the line the sidecar writes
     changes. The service stabilises, and the gateway **does not wake a role whose catalogue is
     empty** (`503 engine_unavailable`, "this role has no enabled model"). The parameters shrink
     to `LlmEnabled` / `ImageEnabled`. Two more conditions make it hold (review, decision
     1(b)(c)): **the sidecar treats SSM's `ParameterNotFound` as "empty"** — `60-engines` is
     created before `30-ingress`, so at stack creation there is no CP and no active set, and a
     `set -e` tripping there brings the two passes back in another shape; and **the controller
     does not start an engine whose catalogue is empty even at `mode=on`**
     (`engineSnapshot.hasModels`, `decideEngineAction` answering `engineReasonNoModel`) —
     `engine_control.go` polls `running && !warmed` every 5 seconds forever (R6), so a `mode=on`
     deployment would otherwise keep buying a `sleep infinity` box at $1.26/hour. The panel shows
     "no enabled model" in place of the toggle. What disappears is the second pass; **the one
     GPU box bought for stabilisation, ten minutes, stays** (the placeholder carries the `GPU`
     resource requirement too; review, decision 1(d)).

2. **The catalogue's truth is two-layered: S3 says what exists, the CP's database says how it
   is offered. The engine is never asked.**
   - **S3 uses ComfyUI's layout** (ADR 0071 decision 6): `llm/<name>.gguf` (shards under
     `llm/<name>/`), `llm/loras/`, `image/checkpoints/`, `image/loras/`, `image/vae/`,
     `image/text_encoders/`, `image/diffusion_models/`. The existing
     `image/sd_xl_base_1.0.safetensors` moves to `image/checkpoints/` (a server-side copy within
     S3, no NAT).
   - **A manifest `<file>.json` next to every file**, written by the ingest job: `sha256`,
     `bytes`, `source` (URL, HF repo id and revision, or Civitai version id), `license` (HF's
     `cardData.license`), `kind` (`gguf` / `checkpoint` / `lora` / `vae` / `text_encoder` /
     `diffusion_model`), `baseModel` (`sdxl` / `sd35` / `flux1` / `flux2-klein` / `zimage` /
     `qwen-image` / …: what a LoRA **fits**, declared by the operator at ingest), `precision`
     (`fp16` / `fp8` / `q8_0` / `q4_k` …; **12B and up is quantised on an L4**, so the same model
     appears as several files of different precision), `ingestedAt`. `text_encoders/` is shared
     across families — SD3.5 and FLUX.1 read the same T5-XXL and CLIP-L, so one ingest serves
     both `files[]`. "The file exists" is not "usable" —
     **only files with a manifest** are catalogue candidates (the same rule as ADR 0071
     decision 3's "do not trust 'the file got bigger'").
   - **A new table `engine_models` in the CP's database**: `id` (what a user picks —
     `qwen3-coder-30b-a3b`, `sdxl-base-1.0`), `role` (`llm` / `image`), `kind`, `files[]` (S3 keys,
     with the flag each goes to for a split model), `enabled`, `args[]` (per-model extra flags —
     `--vae`, `--type q8_0`, `--offload-to-cpu`), `contextTokens` / `maxOutputTokens` (llm; **per
     model** now, decision 3), `sizes[]` (image; the declaration that replaces `sdcppSizes()`'s
     guess), `description` (**one line the agent reads** — for a LoRA, what the picture becomes),
     `vramMiB` (optional; the operator's measured figure, absent when unmeasured),
     `lastUsedAt`.
   - **The active set — what the box loads now — is written by the CP to SSM**:
     `/af-ws/engines/<key>/active`, a JSON of the enabled models' and LoRAs' S3 keys plus the
     material for the preset. The CP task role **already** has `ssm:PutParameter` on `/af-ws/*`
     (`20-platform`, `SsmWorkspaceParams`), so the CP side adds no IAM; the box side adds
     `ssm:GetParameter` (that path only) to `EngineTaskRole` **inside `60-engines`** (ADR 0071
     decision 8: new IAM stays closed inside the stack). The fetch sidecar reads it, syncs,
     builds the preset and the command line. SSM rather than S3 writes from the CP (a bucket
     policy) because it is **a permission the CP already holds** (confirmed, R2(a)), the value is
     small, and the box does not need a live CP at the moment it reads. 🔴 **The Standard tier
     caps a value at 4,096 characters** (R2(c): 4,200 characters is a `ValidationException`;
     today's engine table is 666 bytes). So the active set carries **only S3 keys, local names,
     flags and the preset's material**; `description` and `license` stay in the database. A test
     pins "an active set of 20 models and 20 LoRAs fits in 4,096 characters", and a design change
     that breaks it is when the Advanced tier (8 KB, $0.05/month) gets decided — not before. The
     sidecar reads the JSON as JSON with `jq` (the `aws-cli` image carries `jq`, `python3` and
     `bash`; R5). The leaf `/af-ws/engines` and the child `/af-ws/engines/<key>/active` coexist
     (R2(b)). A parameter the CP creates lives outside CloudFormation, so **`teardown.sh` deletes
     `/af-ws/engines/*/active`**. The S3 layout migration (`image/…` → `image/checkpoints/…`) and
     decision 7's seed (`modelS3Key`) move **in the same step** — a seed pointing at the old key
     makes the first start come up empty.
   - ADR 0071's engine table (one SSM value, read at startup) **stays, as the infrastructure
     table** — service name, URL, health path, capacity provider, idle, deadline, mode.
     `models[]` and `contextTokens` move to the catalogue; when present in the table they are
     the seed of decision 7.

3. **The llm role runs in router mode; the request's `model` picks the model; the window
   becomes per model.** `llama-server --models-dir /models/llm --models-preset
   /models/llm/presets.ini --models-max <LlmModelsMax>`. The fetch sidecar generates the preset
   from the active set (section name = catalogue id, `model` = path, `c`, `n-gpu-layers`,
   `alias`, fixed LoRAs). `-m`, `--alias` and `-c` leave the command line.
   - **ADR 0071's "correction after P1" stops being a constraint.** The window is declared per
     model as `c`, the catalogue carries `contextTokens` / `maxOutputTokens` per model, and the
     Agent writes opencode's `models.<id>.limit` **per model** (`engineProviderEntry` already
     writes per model; it only has to take the value from the model). "Two windows are two rows
     and two provider ids" is no longer needed.
   - **`LlmModelsMax` defaults to 1.** The router does not know VRAM. An L4 (22.9 GB) holds one
     30B Q4 (20.9 GB); a second one does not slow CUDA down, it **crashes** it (ADR 0071
     decision 2). At 1 a switch is unload-then-load: **a 267-second reload** (ADR 0071: 18.5 GB
     into VRAM) and no crash. Two sessions alternating between two models pay 267 seconds each
     time — that is **this design's price**, shown rather than hidden: the admin panel counts
     "model switches: N". A deployment staging several small models raises it (whether the
     catalogue's `vramMiB` sum fits the box is the operator's call; the CP only helps with the
     addition).
   - **`warm` changes meaning.** Today `/health` ok = weights in VRAM. Under the router, warm is
     "`GET /models` shows the **default model as `loaded`**", and the health path stays `/health`
     (open question 1 measures what it means). The default is the one catalogue entry flagged
     `default`, written into the preset as `load-on-startup = true`. Without it the first request
     pays "box start 527 s + load 267 s".
   - **A request during autoload is held by the heartbeat.** The gateway's streaming path sends an
     SSE comment every 10 seconds until the upstream's first byte (ADR 0071 decision 5), and
     nothing new is needed — **the router queues the request** (R1(b): 200 after `ensure_model:
     waiting until model … is fully loaded`). No `/models/load` branch.
   - **Open question 1's four points were settled by the review on a CPU (R1)**: the router
     starts on an empty `--models-dir` with `/health` ok, autoload waits, `--models-max 1` evicts
     the idle LRU and loads (`evicting idle LRU`), `usage` and `model` (the catalogue id) arrive
     through the router, and the window is per model (`n_ctx` from `/props?model=`). Hence
     🔴 **`/health` is no evidence of warm** (ok with zero models) — today's `warmProbe` pointed at
     the router would always say true. `warm` is `loaded` in `GET /models`, as written. Two
     details: the router answers **400** for an unknown id, and the gateway's `404 model_unknown`
     is its own verdict — do not mix the numbers; the children are **separate processes** on a
     loopback port with no `--api-key` (unreachable from outside the task's netns; written down).
     One GPU question remains — what "idle LRU" does with a model that is **generating** — and it
     is the same observation as P1's definition of done. P1 is deferred by the user's priority
     (images first), no longer by anything unmeasured.

4. **The image role holds one checkpoint; a swap takes effect at the next start; one at a
   time.** sd-server has no switch, extra roles are ruled out by the wall, co-tenancy by VRAM.
   So among the image catalogue's entries **exactly one is `selected`**, by an admin.
   - Changed **while stopped** — the usual state, a $1.26/hour box sleeps — the next request wakes
     the new checkpoint. **No extra wait**: the box fetches from S3 at every start anyway (ADR
     0071 decision 3), and 6.9 GB is 45–60 seconds.
   - Changed **while running**, the admin panel makes the person choose: "at the next stop"
     (default) or "restart now — a generation in flight will fail". Never a silent restart: the
     panel says beforehand that one click kills somebody's 21-second generation. The
     implementation is `UpdateService --force-new-deployment` (the CP already holds
     `UpdateService`), not `desired 0 → 1`, so the controller's state machine (`starting`'s
     deadline, the cooldown) is traversed as usual.
   - **A split model is one entry.** FLUX lists `diffusion_model` / `clip_l` / `t5xxl` / `vae` with
     roles in `files[]`, and the sidecar assembles the `--diffusion-model` … flags. `--type` and
     `--offload-to-cpu` go in `args[]`; shrinking to g6f (ADR 0071 P4) becomes one line there.
   - **A model whose generation exceeds 60 seconds is not served by a synchronous-API engine.**
     As ADR 0071 P1 measurement 9 found, the ALB closes a connection that carries no response
     byte for 60 seconds, and `/v1/images/generations` is one JSON answer with nowhere to put a
     heartbeat. SDXL's 21 seconds never hit it; FLUX.1-dev-class (60–90 s★) and SD3.5 Large
     without Turbo are **cut every time, even on a warm engine**. sd-server's async API was
     broken inside a container (ADR 0071's measurement). So next to `sizes[]` the catalogue
     carries `syncSafe` (may be served over a synchronous API), declared by the operator, and
     sd-server offers nothing else.
     - One hypothesis is kept: that async API failed at `/proc/1/map_files`, and
       `--lora-model-dir` defaults to **the current directory** — with a cwd of `/` the LoRA
       scan walks into `/proc`. One start with `--lora-model-dir /models/image/loras` settles
       it (open question 6). Even if it does, the rule above stays — a working async API is a
       fallback's story, and the main road is the next point.
   - **ComfyUI becomes the image role's main engine (phase P2), and the "one" in this decision
     disappears there.** ComfyUI picks checkpoint and LoRAs per workflow, caches what is loaded,
     and its `/prompt` returns at once with `/history` polled in short calls, so the 60-second
     rule does not apply either. With the same catalogue and the same S3 layout `selected`
     becomes per request. That is why the catalogue is engine-agnostic: **sd-server's limit is
     not baked into the catalogue's shape**. sd-server stays as the light path — official
     2.3 GB image, OpenAI-compatible, no image to build — for a deployment that chooses it
     (decision 10).
     - **The stack gains no role.** One parameter, `ImageEngine` (`sdcpp` / `comfy`), switches
       only the container definition (image, entrypoint, command) and the health path
       (`/v1/models` for sd-server, `/system_stats` for ComfyUI) with `!If`, on the **same** task
       definition, service and Cloud Map name. A second service set does not fit the wall, and
       the two never run at once (ADR 0071 decision 2), so two are not needed. The engine
       table's `provider` becomes `comfy`, and the Agent picks its provider by it. Two
       preconditions (R4): **one more ECR repository, `af-comfyui`, in `20-platform`** (there are
       five today and none for ComfyUI; 22.5 KB of room), with the self-built image baked in CI
       like `af-workspace` and copied by `standup.sh`'s images step; and **the budget does not
       fit as it stands** — `60-engines.yaml` is 50,774 bytes, about 3,000 freed by the retired
       parameters against about 5,450 added (4,350 with the sidecar held once in `Mappings`).
       Moving the two volume measurement notes and the `run-task` recipe (3 KB and a bit) out
       of the 15.5 KB of comments into `PARAMETERS-60-engines.md` makes room. **Move first, add
       after** (P0's first step).
     - 🔴 **P0's image role is still sd-server, so P0's default is SDXL** (review, decision
       4(b)). Decision 10's klein 4B is a ComfyUI measurement; sd.cpp claims klein but the
       split-model flag assembly is unmeasured with it, and stepping on that in P0 stalls the
       definition of done on an sd-server problem. The second checkpoint P0's definition of done
       needs is **another SDXL-family checkpoint** (an OpenRAIL++-M fine-tune, chosen by decision
       10's rules), ingested first. klein becomes the default in P2, once ComfyUI is in.

5. **A LoRA is a catalogue entry, chosen per request. The agent can only name what the
   catalogue has.**
   - **image.** The box syncs the **enabled** entries of `image/loras/` and starts with
     `--lora-model-dir /models/image/loras`. `generate_image` gains two arguments: `model` (enum =
     the enabled checkpoints; on the `sdcpp` provider that is **one today** — decision 4 — but the
     argument's shape allows several from the start, for the Codex / agy routes and for ComfyUI)
     and `loras: [{name, weight}]` (`name`'s enum = only the LoRAs whose **`baseModel` matches the
     selected checkpoint**; `weight` 0–2, default 1). The tool description lists the catalogue's
     `description` lines, so the agent can choose "`watercolor-v2` for a watercolour look".
     - **The main road is ComfyUI** (decision 4): the `comfy` provider puts the checkpoint name
       and a chain of `LoraLoader` nodes (name, strength) into the family's workflow template
       and posts it to `/prompt`. A LoRA is a node swapped in, so the cost of a changing set is
       ComfyUI's cache's business, and sd-server's merge behaviour (open question 2) need not
       be measured.
     - **The fallback is sd-server** (an `ImageEngine=sdcpp` deployment): the Agent's `sdcpp`
       provider appends
       `<sd_cpp_extra_args>{"lora":[{"path":"<file>","multiplier":<w>}]}</sd_cpp_extra_args>` to
       the prompt. **The gateway is not involved** (still verbatim — decision 4 of ADR 0071 reads
       the body for usage only). ⚠️ **A prompt that already contains `<sd_cpp_extra_args>` is
       refused**: this hole carries not only `seed` and `sample_steps` but `lora.path`, a file path
       on the server. The prompt is written by the model, and anything the model has read can end
       up in it (the stance of ADR 0071 decision 4(d)). This path is built after ComfyUI, and
       only if a deployment needs it.
     - A LoRA whose `baseModel` does not match (an SD 1.5 LoRA on SDXL) either produces a
       **silently broken picture** or is ignored with a tensor-name warning; to the user both
       read as "did nothing". So **the Agent does not offer it in the enum and refuses it when
       assembling the request**. 🔴 **Not the CP** (review, decision 5): the LoRA sits inside
       the prompt's `<sd_cpp_extra_args>` or inside the workflow JSON, so a CP refusal means
       reading the body, which contradicts decision 4's "verbatim". The second line of defence is
       **the box**: only enabled LoRAs are synced, so a name outside the enum fails at the engine,
       and `--lora-model-dir` with `models/loras/` bounds the path.
     - **The arguments fit ADR 0069** (R7): `imagegen.Request.Model` already exists, and `Caps`
       gains `Loras []{name, description, baseModel}`. Two rules: **an argument appears only
       when there is really a choice** (as `provider` does — `model` appears only when the
       fleet's engine has two or more enabled checkpoints, its values are catalogue ids only, the
       Codex / agy fixed models are never mixed into the enum, and a `model` given with a
       non-fleet provider is refused by name); **an enum cannot depend on another argument**, so
       the `loras` enum is every enabled LoRA, each description states its `baseModel`, and the
       Agent checks the combination.
     - Depends on open question 2: the cost of a per-request change of LoRA set under
       `immediately` (a re-merge would be seconds, against a 21-second generation), and the VRAM
       delta of `at_runtime` (7.4 GB plus something surely fits an L4, perhaps not a g6f). The
       result decides the default `--lora-apply-mode` written into the catalogue's `args[]`.
   - **llm.** Two stages. **First, fixed LoRAs in the preset** (a catalogue model's `loras[]`
     becomes `lora = <path>` in its section — "this model comes with this fine-tune", an ordinary
     model id from opencode's side). **Later, virtual model ids** (`<base>+<lora-set>` as a
     separate catalogue entry; the gateway rewrites `model` to the base and adds the request's
     `lora: [{id, scale}]`). The second comes later because the AI SDK's openai-compatible
     provider sends no `lora`, so **the gateway would rewrite the body** — the first breach of
     `dial()`'s "verbatim". Reading the body for usage has a precedent; rewriting is a different
     promise, broken only once the need has been measured.
   - **LoRAs are ingested like everything else** (decision 6). They are on HF and on Civitai, an
     SDXL LoRA is 50–400 MB and does not register in the sync time. Licences are handled as for
     checkpoints (ADR 0071 decision 11).

6. **Ingest becomes the CP's job, started from the Console. The hand-run `run-task` remains but
   is no longer the main road.** `POST /api/admin/engines/models/ingest` (super_admin) takes
   `source` (`{url}` / `{hf: {repo, file, revision}}` / `{civitai: {versionId, file}}`), `role`,
   `kind`, `id`, `baseModel`, `licenseAccepted: true`, and the CP:
   - **resolves `sha256` and `license` from the HF API** (`siblings[].lfs.sha256` with
     `?blobs=true`, `cardData.license` — verified against a real checksum in ADR 0071). A gated
     repository (`gated: auto`) is recognised by 401/403 and the failure **says so in those
     words** — "accept the licence on Hugging Face first" (ADR 0071 decision 11);
   - **starts the ingest task with `ecs:RunTask`**. The CP task role has no `ecs:RunTask` (R3: it
     holds `CreateService` … `ListTasks` and lacks only `RunTask`), so it gains `ecs:RunTask`
     (limited to the ingest family) and `iam:PassRole` (**`IngestTaskRole` only** — the execution
     role's PassRole is already in `PassTaskRoles`). This is the **first breach** of ADR 0071
     decision 8's "zero CP IAM additions", but it lives in an `AWS::IAM::Policy` inside
     `60-engines` (`Roles:` = the role name cut out of `20-platform`'s `CpTaskRoleArn`), so **a
     deployment without the stack gains nothing**. ADR 0071 decision 8 is sharpened to: "another
     stack may add to the CP's role only **permissions that reach that stack's own resources**
     (RunTask limited to the family, PassRole limited to the ingest role), never `Resource: *`".
     EventBridge was considered as the alternative that keeps the CP at zero (the CP writes
     `/af-ws/engines/ingest/job`, an `AWS::Events::Rule` in `60-engines` runs the task under a
     role of its own) and rejected: at-least-once delivery can run a job twice and a failure
     surfaces only in CloudTrail (review, decision 6(c)). The subnets and SG RunTask needs are
     written by `60-engines` into the engine table's `ingest` block. Egress from the CP to the
     HF API is a precondition; a deployment that restricts outbound traffic falls back to the
     hand-run ingest;
   - 🔴 **the CP does not read the manifest — it cannot** (R3: the CP task role has no S3 action at
     all, and none is added). The row is built from **what the CP itself resolved from HF (sha256,
     licence, bytes) and the exit codes in `DescribeTasks` (`fetch` SUCCESS → `upload` SUCCESS)**;
     the sha256 check was done by `fetch`, and that is enough. The manifest is **for the box**. The
     row is created as `enabled: false` — **ingested is not offered**; an admin enables it. In
     progress and failure reasons (sha256 mismatch, 401, disk) show on the panel's row.
   - **Civitai** (the de-facto home of SDXL LoRAs): downloads authenticate with an
     `Authorization: Bearer` API key; the sha256 is `files[].hashes.SHA256` of the
     `model-versions` API. The key is an operator secret in Secrets Manager like HF's, read by
     the ingest task only. 🔴 **Civitai's API could not be confirmed today** (the developer site
     answered 404) — open question 4. Until then the `{url}` source with an explicit sha256 is
     the road.
   - As an intermediate step, `harness/ingest-model.sh <role> <hf-repo> <file>` comes first — a
     thin script that resolves sha256 and licence and assembles the `run-task`, the operator's
     tool until the Console road exists.

7. **The catalogue is dynamic and has three readers. The engine table stays static.**
   - **The gateway** reads the catalogue per request (database; 10-second in-process cache). For
     llm, a `model` absent from the catalogue is `404 model_unknown` — the router is never left to
     match a file name on disk: **what is not in the catalogue does not exist**.
   - **The Agent's** `/internal/engine/catalog` answers from the catalogue, with per-model
     `context_tokens` / `max_output_tokens` / `description`, and for image a `loras[]` (name,
     description, baseModel) next to `models[]`. The 10-minute TTL stays, but **the CP pushes
     `POST /engine/catalog-changed` to every Workspace's Agent on a change** (the same reverse
     path and authentication as `POST /engine/usage`). The Agent re-runs `syncEngineProviders()`
     and `ApplyEngineChange()` makes a running serve daemon re-read its config — that function
     **already exists** "for a catalogue that changes later". A Workspace the push did not reach
     catches up on the TTL.
   - **The admin panel** (`GET /api/admin/engines`) shows `models[]` **as the catalogue sees it**:
     id, enabled, selected (image), default (llm), window, `vramMiB`, last used, licence. The
     actions are enable/disable, select, default, delete. 🔴 **Deleting the S3 files is the ingest
     task's job, not the CP's** (R3: the CP has no `DeleteObject`) — the same `RunTask` with
     `MODE=delete`, which is where the **manifest first** order is kept (a file deleted before its
     manifest stays a candidate whose sync then fails). Until P4, a sibling of
     `harness/ingest-model.sh` does it. ADR 0071 decision 13's "models are the stack's
     declaration" becomes "models are the catalogue's declaration", and `warm` means what
     decision 3 says.
   - **The model that is warm now (`warm_model`) is the CP's to know** — as the model of **the
     last successful request**, read off the `model` in `POST /engine/usage` (never asked of the
     engine; ADR 0053). It goes on the catalogue row, and **the default for a request without
     `model` is decided server-side: the warm model if there is one, else the catalogue's
     default** — the agent is not made to reason about temperature (review, answer 7; a switch
     is a 1–2.5 minute re-read, *Resolved* 5). A request that switched gets a warning:
     "switched model, +N s".
   - **The seed (decision 1's compatibility)**: when the CP starts with an empty catalogue and
     the engine table carries `models[]`, it creates **one row** from that id, its
     `contextTokens`, and a `modelS3Key` that `60-engines` adds to the table. A deployment
     running today has today's model in the catalogue the moment the CP comes up; nothing
     changes for it.

8. **Provenance and usage carry the model and the LoRAs.** `generate_image`'s result has `model`
   = the checkpoint id and `provenance` with `loras: [{name, weight}]` and `sha256` (from the
   manifest; ADR 0071 decision 10's "file name and sha256"). The llm usage row's `model` is what
   the router returns in the response (= the catalogue id). `engine_hourly` (ADR 0071 decision
   13) is untouched — uptime is a property of the box, not of the model.

9. **The effect on the cold start is stated in numbers, and only enabled entries are synced.**
   S3 → EBS is a steady 104–147 MB/s (ADR 0071 measurement 8), so the llm sync time is **the sum
   of the enabled GGUFs' sizes** — 179 seconds per 18.5 GB. Enabling is "load", not "ingest": five
   entries in the catalogue with one enabled is today's 527 seconds. The admin panel shows
   "sync +N s (estimate)" next to the toggle (the size is in the manifest; as with
   `observed_secs`, **it says estimate**). `useLocalStorage` (ADR 0071 open question 1) stays as
   it is.

10. **Model selection policy — the first default is chosen from "Apache-2.0, ungated, fits an
    L4 without quantisation"; gated and non-commercial models enter only by an operator's
    explicit act.** From the table in Context:
    - **The default is FLUX.2 [klein] 4B, with Z-Image-Turbo second** (both Apache-2.0, ungated,
      and they fit an L4 as bf16), next to the workhorse SDXL. Decided by measurement (*Resolved*
      3): a warm 1024px picture is klein **4.0 s** (VRAM 11.6 GB), Z-Image **10.5 s** (12.2 GB),
      SDXL 8.0 s (6.9 GB). Loading is klein 106–110 s and Z-Image 133–154 s, **paid on every
      switch** (*Resolved* 5), so the lighter one is the default. Both pictures are right to the
      eye; Z-Image leans photographic.
    - **SD1.5 and SD3 Medium are not offered.** The former is superseded by SDXL, the latter by
      SD3.5.
    - **Gated models (all of SD3.5, all of FLUX.1, FLUX.2 dev and klein 9B) can be ingested only
      on a deployment whose operator accepted the terms on HF and placed an `HF_TOKEN`** (ADR
      0071 decisions 3 and 11, unchanged). "Gated" is the setting "distribute only to accounts
      that accepted the owner's terms" (`gated: auto` grants on acceptance); anonymous or
      unaccepted tokens get 401/403. On a multi-tenant deployment **the operator takes on the
      terms on behalf of every member**, so the ingest UI states that sentence and requires
      `licenseAccepted` (decision 6). FLUX.1-schnell has become gated while staying Apache-2.0 —
      **licence and gating are separate axes**, and the catalogue keeps them in separate
      columns.
    - **Non-commercial models (FLUX.1-dev / Kontext-dev / FLUX.2-dev / klein 9B) are never the
      default.** Ingest is not refused, but the catalogue's `license` shows on the panel row and
      in `generate_image`'s provenance (decision 8). 🔴 **The manifest and `engine_models` carry
      three fields: `license`, `license_name` and the URL** (R8: FLUX.1-dev and SD3.5 have
      `cardData.license` = `"other"`, and the substance is in `license_name`; copying `license`
      alone would list the two non-commercial models as just "other"). The values are a
      **snapshot** at ingest time, unmoved by later edits to the HF card. Acceptance records
      **who and when** (`licenseAcceptedBy` / `licenseAcceptedAt`, in the manifest and the audit
      log). And a **`commercialUse` axis** is shown: FLUX.1-dev's non-commercial terms make a
      deployment that sells access to members the operator's own violation, so next to "ingest
      is not refused" stands "not on a commercial deployment", derived from the licence name
      (Stability's $1M revenue line goes in the same column). An `HF_TOKEN` is bound to a
      personal account and dies when the person who accepted leaves — an organisation account's
      token is the recommendation (review, decision 10).
    - **12B and up is ingested as quantised files** (`precision`, decision 2). A 23 GB fp16 body
      does not fit an L4's VRAM: it pays 180 seconds from S3 and the load into VRAM, then
      crashes.
    - **The 60-second rule** (decision 4): a model without `syncSafe` is not offered on an
      sd-server deployment.

## Resolved by measurement (2026-09-08, the dev deployment's g6.xlarge)

The harness is `deploy/aws/ecs/harness/bench-image-engine.sh` (+ `.py`). It runs **one task with
RunTask** on the image role's capacity provider — four containers: fetch → ComfyUI → the bench
client → upload to S3 — and never uses ECS Exec. ComfyUI is the community image
`ghcr.io/lecode-official/comfyui-docker:latest` (5.36 GB compressed, **v0.8.2** baked in), copied
to ECR, **checked out at v0.34.0 at start** (1–2 s) plus `pip install -r requirements.txt`
(19–24 s). The text encoder is fp8 (`qwen_3_4b_fp8_mixed`, 5.6 GB). The numbers are from the
last two phases (stock flags, `--highvram`) of four runs on the same box shape; the pictures
were checked by eye.

1. **klein 4B, Z-Image-Turbo and SDXL draw on one L4.** All 26 pictures succeeded, nothing
   crashed. The workflows are the official templates' subgraphs in API form
   (`bench-image-engine.py`): klein 4B distilled at 4 steps, cfg 1; Z-Image-Turbo at 8 steps,
   cfg 1, shift 3.
2. **`qwen_3_4b.safetensors` has the same sha256 in the Z-Image and the klein repositories**
   (`6c671498…`). Decision 2's "`text_encoders/` is shared across families" is now a fact.
3. **A warm picture (1024px)**: SDXL 20 steps **8.0 s** (ADR 0071 measurement 7 said 7.9),
   Z-Image-Turbo **10.4–10.6 s**, klein 4B **3.7–4.0 s**, SDXL 512px 2.1–2.7 s. SDXL with a
   LoRA is 7.8–8.1 s: **a LoRA is free once warm**. Stock flags and `--highvram` do not differ.
4. **VRAM**: SDXL 6.9 GB, Z-Image 12.2 GB (17.5 GB with the stock flags, which keep the text
   encoder too), klein 11.6–13.2 GB. The three cannot sit in VRAM together; ComfyUI swaps.
5. 🔴 **A switch is a re-read from EBS every time, 1–2.5 minutes.** The first visit and the
   return visit cost the same: SDXL 56–63 s, klein 106–110 s, Z-Image 133–154 s. The box's
   15 GB of RAM cannot keep three models (27 GB), so a model leaving VRAM goes back to disk.
   **Decision 4's "switch per request" holds, but a request that switches pays 10–30 warm
   pictures.** The dominant term is the EBS gp3 read (12.3 GB in 133 s = 92 MB/s) — one more
   place where ADR 0071 open question 1's `useLocalStorage` (instance-store NVMe) would pay.
6. **The 60-second rule (decision 4) hits the switching request on ComfyUI** — every warm
   picture is under 11 s, but a request that includes a switch exceeds 60 s. It is harmless
   only because `/prompt` → `/history` is asynchronous; on a synchronous-API engine the
   switching request is always cut.
7. **The cold start, from RunTask**: pull starts at +33 s, the 5.4 GB (compressed) image from
   ECR takes **205 s**, S3 → EBS **33.3 GB in 332–360 s** (92–100 MB/s, ADR 0071 measurement 8's
   band, concurrent with the pull), checkout + pip 20–26 s, ComfyUI answers `/system_stats`
   36–45 s later, the first SDXL takes 63 s — **526 s to the first picture**. The same order as
   the llm role's 527 s (ADR 0071), and the sum of the synced models' sizes is what moves it
   (decision 9).
8. 🔴 **Never pass `--cache-none`.** The loader nodes' outputs — the models themselves — are
   not cached, so **every prompt re-reads the weights from disk** (a warm SDXL took 57 s,
   Z-Image 134–147 s, and VRAM fell back to 280 MiB after each run). `--highvram` does not
   help. The first two runs were measured that way.
9. 🔴 **Sending the same graph twice hits the output cache: 0.5 s and nothing executed.** A
   warm measurement needs a different seed. That invalidated the third run.
10. **The box**: a g6.xlarge registers **15,000 MiB** with ECS. **A second task on the same box
    fails with `No space left`** (the anonymous host volume is never reclaimed — ADR 0071
    decision 7's note). A task placed on a box that had just stopped one sat **9.5 minutes in
    PENDING** before its pull started (33 s on a fresh box). Waiting for Managed Instances to
    reclaim the box is faster.
11. **Ingest (HF → S3) ran at 7.8–44 MB/s** with no way to predict which (12.3 GB in 283 s,
    5.6 GB in 722 s), then 27–93 s to S3 — four more points for ADR 0071 decision 3.

Consequences: decision 10's default is **klein 4B** (3). Decision 4's ComfyUI-first stands, with
the price of "switch per request" (5) added to the text, and the CP's `warm_model` **decides the
default for a request without `model` server-side** (decision 7 — the shape the review's answer 7
corrected "put it in the description" into). Open question 7 is closed; 8 is half closed (the size of a self-built image is still
unmeasured — the same PyTorch + CUDA base as the community image means a 5 GB class and a
200-second pull, and the 20–26 s checkout + pip and its NAT dependency go away).

## Options rejected

- **vLLM as the llm role's engine (for now).** One process, one model, no router — a switch is a
  restart, **a step back from llama.cpp for this ADR's purpose**. The 9.7 GB image quadruples
  the pull, the 4-bit MoE quantisations that would fit a 30B on an L4 are less mature than
  GGUF, and its strengths (continuous batching, dynamic multi-LoRA) pay off on concurrent
  requests to the same model, which an engine that sleeps most of the time never sees. **Two
  conditions for revisiting**: five or more concurrent sessions on the same model as the norm,
  or a model with no GGUF. The catalogue's `kind` can hold a `safetensors` LLM, so vLLM can join
  that day as a second `llm` engine on the same S3 layout.

- **A row (service set) per model in `60-engines`.** Does not fit the wall, and if it did, one
  sleeping service per model and two boxes when two models wake. The router answers for llm,
  "the next start" for image, on the same box.
- **Swapping through a new task-definition revision.** Gives the CP `RegisterTaskDefinition` and
  drifts from the task definition CloudFormation owns; the next stack update silently puts it
  back.
- **Switching through `model` on `/v1/images/generations`.** sd-server gives it no meaning
  (upstream api.md). Sending it would suggest a switch exists — the reason today's `sdcpp.go`
  does not send it.
- **The `<lora:name:1>` prompt syntax.** The server ignores it on purpose (upstream api.md). The
  CLI documentation is written in that syntax, so **this is the first trap anyone steps on**.
- **Letting the agent write LoRA paths or arbitrary weights.** `lora.path` is a file path on the
  server and can point outside the catalogue. Enum only.
- **Deriving the catalogue from S3 (`ListObjects` → `/v1/models`).** "Exists" and "offered" are
  different (ingested but unverified, licence not accepted, does not fit VRAM), and ADR 0053 says
  never derive. Manifest plus `enabled`, two steps.
- **The box pulling from HF / Civitai directly.** ADR 0071 decision 3 stands (4–236 MB/s with no
  way to know which, and a token on the box).
- **The gateway reading the catalogue from SSM.** An SSM call per request (the reason
  `engines.go` reads once at startup). SSM carries only **the active set the box reads**; the CP
  itself reads its database.
- **EFS for the catalogue.** Rejected in ADR 0071.

## Open questions — measure 8 and 9 before P2 (2 and 6 only when an sd-server deployment needs them)

1. ~~**The router's four points** (decision 3 depends on them)~~ **Resolved (review R1, on a
   CPU)**: `/health` ok while empty, autoload waits, `--models-max 1` evicts the idle LRU and
   loads, `usage` and `model` arrive through the router, the window is per model. One GPU
   check remains — what "idle LRU" does with a model that is generating — and it is the same
   observation as P1's definition of done.
2. **sd-server's LoRA** (decision 5 depends on it): the cost of a changing LoRA set under
   `immediately`, the VRAM of `at_runtime`, whether `<sd_cpp_extra_args>` also works on
   `/v1/images/edits` (multipart), and what a `baseModel` mismatch does (silent breakage or a
   warning). The `<sd_cpp_extra_args>` path and the mismatch can be measured on the CPU SD1.5
   Q4.
3. **Syncing into a running box.** Can a newly enabled model be added to a running box
   **without waiting for the next start** — a resident sidecar re-syncing the active set, and
   does the router rescan `--models-dir` (or is there a reload endpoint)? If not, llm also
   swaps "at the next start" to begin with, and this is P4.
4. **Civitai's API** (decision 6): the authentication form (header or `?token=`),
   `files[].hashes.SHA256`, the `model.type` and `baseModel` values.
5. ~~**`engine_models` as a table or as a settings-store value.**~~ **A table (review, answer
   6)**. Two writers (the admin's toggles and the ingest job's `ingesting → ready / failed`), and
   one JSON read whole and written whole has no CAS, so one of them loses; `files[]` / `args[]` /
   `sizes[]` differ per row and the list, the seed and delete are all row operations; a
   two-dialect migration has `engine_hourly` as precedent. `lastUsedAt` is **not in P0** (no
   UPDATE per request; the once-a-minute shape of `engine_<key>_demand_at` can add it in P4).
6. **Why sd-server's async API failed** (decision 4's hypothesis): does `/sdcpp/v1/img_gen` work
   when `--lora-model-dir` is given explicitly? If so, an sd-server deployment can drop the
   60-second rule — without changing the order (ComfyUI first).
7. ~~**Measuring the default** (decision 10)~~ **Resolved** (*Resolved* 3–5). What remains is the
   two gated ones — SD3.5 Medium and FLUX.1-dev — on a deployment that has an `HF_TOKEN`.
8. **ComfyUI's own image** (phase P2) — half resolved (*Resolved* 7 and 8): the community image
   is 5.36 GB compressed and a 205-second pull, and its baked v0.8.2 needed the checkout + pip
   at start (20–26 s, NAT-dependent). A self-built image pins v0.34.0 and carries no Manager.
   The GGUF-reading node (`ComfyUI-GGUF`) was not needed (fp8 safetensors sufficed); bundling
   one means **a pinned revision written into the Dockerfile** (ADR 0071 decision 6).
9. **Can the switch re-read (*Resolved* 5) be shortened? — two candidates, one GPU hour, before
   P2** (review, answer 7 and R9). (1) `useLocalStorage` (ADR 0071 open question 1): the
   capacity provider's `LocalStorageConfiguration` is not create-only, so `ImageUseLocalStorage=true`
   lands in one stack update (R9). It pays not only on the switch (12.3 GB at 92 MB/s → NVMe)
   but on **the cold start's 33 GB S3 → EBS at 332–360 s**, a number pinned to EBS's write
   ceiling (125 MB/s baseline on a g6.xlarge). (2) **Solve it with RAM**: the three models
   (27 GB) leave the page cache because the box has 15 GB; `ImageMemMinMiB=30000` selects a
   g6.2xlarge (32 GiB) with no code change. If a switch becomes RAM → VRAM (seconds),
   `useLocalStorage` is back to being a start-time question. `bench-image-engine.sh` measures
   both as they are, and since the answer changes P2's definition of done (the price of a
   switch), it is measured **before P2**.

## Phases

- **P0 — the catalogue's foundation.** The first step is moving 3 KB of comments out of
  `60-engines.yaml` into `PARAMETERS-60-engines.md` (R4: move first, add after). Then
  `engine_models` (a table, open question 5) and the seed, manifests, the S3 layout migration
  (in the same step as the seed), the active set in SSM (the 4,096-character rule and its test)
  and a role-independent fetch sidecar (one script: sync, `/models/cmdline`, `ParameterNotFound`
  as empty), the wrapper entrypoint on both roles, the controller's "empty catalogue does not
  start", the shrink to `LlmEnabled` / `ImageEnabled` with the old parameters kept for one
  release, `teardown.sh`'s SSM cleanup, the admin API (list, enable, select) and the panel's
  `models[]`, `/internal/engine/catalog` from the catalogue plus the `catalog-changed` push,
  `sdcpp`'s `Caps.Sizes` from the declaration, and a second SDXL-family checkpoint ingested.
  **Definition of done: re-select the image checkpoint in the Console from SDXL to the second
  one and, with no CloudFormation touched, the next start returns a picture from it (the stack's
  last-updated timestamp in `describe-stacks` has not moved); a `mode=on` engine with an empty
  catalogue does not start; at stack creation (no CP, no active set) both roles' services
  stabilise.**
- **P1 — llm in router mode.** Open question 1 is settled (R1), so nothing blocks it; it is
  deferred by the user's priority (images first). Preset generation, per-model windows,
  `LlmModelsMax`, the redefinition of `warm`, opencode's provider with per-model `limit`.
  **Definition of done: two `llamacpp/` models in the launch menu, each usable in turn, and the
  reload of a switch answered on the first attempt** (ADR 0071 P0's observation, taken across a
  switch).
- **P2 — ComfyUI (ADR 0071's P2, moved forward to here).** Open question 9 first, one GPU hour.
  The self-built image (pinned tag, no Manager, open question 8; including the `20-platform` ECR
  repository and the CI bake), the `ImageEngine=comfy` `!If` (decision 4), the `comfy` provider
  (generate / edit / inpaint mapped onto per-family workflow templates, driving `/prompt` →
  `/history` → `/view` with progress notifications), templates for five families — SDXL,
  SD3.5, FLUX.1, FLUX.2 klein, Z-Image — kept in the repository behind golden tests, and
  `generate_image`'s `model` argument (enum = the enabled checkpoints; several for the first
  time — and **the description names the model that is warm now**: a switch is a 1–2.5 minute
  re-read, so the agent can prefer the warm one when the default will do; *Resolved* 5). The
  pane (`/engine/comfy/` over WebSocket) is **not included** — `generate_image` needs only the API;
  the screen is P5. **Definition of done: on one box, SDXL and klein 4B (or Z-Image-Turbo)
  alternate per request and return pictures with no service restart in between, and 1024px
  SDXL comes back in the 8-second range of ADR 0071 measurement 7.**
- **P3 — LoRA.** On ComfyUI: the image role's `loras/` sync, `generate_image`'s `loras`, the
  `LoraLoader` chain in the templates, refusal on a `baseModel` mismatch; fixed preset LoRAs
  for llm. sd-server's `<sd_cpp_extra_args>` path only when an `ImageEngine=sdcpp` deployment
  needs it, after open question 2. **Definition of done: the same prompt and seed give a
  different picture with and without the LoRA, and an SD 1.5 LoRA does not appear in the enum
  on SDXL.**
- **P4 — ingest from the Console.** The `ingest` API, the RunTask IAM (inside `60-engines`;
  PassRole for the ingest role only), HF sha256 / `license` / `license_name` resolution, the
  gating sentence and licence-acceptance UI (who and when, the commercial axis; decision 10),
  progress and failure display, the ingest task's `MODE=delete` (decision 7), Civitai (after
  open question 4). Until then, `harness/ingest-model.sh`.
- **P5 — syncing into a running box (open question 3), virtual model ids for llm (the second
  half of decision 5), the ComfyUI pane, sd-server's async API (open question 6).**

## Sources checked (2026-09-08)

- llama.cpp `tools/server/README.md` (the router section, the LoRA section, `--alias`)
- stable-diffusion.cpp `examples/server/api.md` (the three API families, `sd_cpp_extra_args`,
  the `lora[]` shape, the unsupported `<lora:>` syntax, the fixed `/v1/models` answer),
  `examples/server/README.md`, `docs/lora.md` (`--lora-model-dir`, `--lora-apply-mode`),
  `README.md` (`--diffusion-model` / `--clip_l` / `--t5xxl` / `--vae` / `--type` /
  `--offload-to-cpu`)
- this repository: `deploy/aws/ecs/cfn/60-engines.yaml`, `PARAMETERS-60-engines.md`,
  `control-plane/engines.go`, `engine_gateway.go`, `engine_admin.go`, `workspace/agent/engines.go`,
  `internal/imagegen/sdcpp.go`, `internal/agents/opencode/engine.go`, `20-platform.yaml`
  (`SsmWorkspaceParams`), ADRs 0053, 0069, 0071
- Civitai's REST API reference: the wiki points at a new home, and that home answered 404
  (open question 4)
- For the same-day revision: HF's `api/models/<repo>` (`cardData.license` / `license_name` /
  `gated`, `siblings[].size` with `?blobs=true`) for eleven repositories — SD3, SD3.5 Medium and
  Large, FLUX.1-dev / schnell / Kontext-dev, FLUX.2-dev / klein 4B / klein 9B, Qwen-Image,
  Z-Image-Turbo — Docker Hub's `vllm/vllm-openai:latest` (`full_size` 9.7 GB), and the dated
  model-support history in stable-diffusion.cpp's `README.md`

## Review (2026-09-08, before P0)

In the manner of 0071's review: does each decision follow from its evidence, and is any
conclusion drawn that the measurements cannot carry? This is a different session from the
author's, so the text's "a permission the CP already holds", "a few KB" and "the CP rejects
it" were **re-read against the code and the AWS APIs**. The verdict first: **approval (P0 may
start) is proposed, on the condition that decisions 2, 5, 6, 7 and 10 are rewritten as
proposed below before any P0 code is written — the review overturned their premises, or they
rest on a permission the CP does not have.** The four points of open question 1 that the
author deferred to "measure first" **were all settled on this container's CPU** (R1) — P1 is
postponed by the user's decision, but nothing blocks it any more. The heaviest oversight in
the text is that **the CP task role holds no S3 permission at all**, and decision 6 (read the
manifest) and decision 7 (delete the S3 file) were written without noticing (R3).

### What the review measured and checked

- **R1. The four router-mode points (open question 1), measured on CPU.** The official
  llama.cpp build `b10853` (`llama-b10853-bin-ubuntu-x64`, `version: 0.4.0-dev`) with
  `ggml-org/models`' `tinyllamas/stories260K.gguf` (**1.1 MB**) placed twice as `alpha.gguf` /
  `beta.gguf`, started with `--models-dir` + `--models-preset` (`[*] c = 512`, `[alpha]
  c = 256, load-on-startup = true`) + `--models-max 1`. Every number is from that setup.
  1. **(a) An empty `--models-dir` starts, and `/health` is `{"status":"ok"}`.** `/models`
     and `/v1/models` are `{"data":[]}`. A request for an unknown id is **`400 "model 'nope'
     not found"`** (not 404); a request without `model` is `400 "model name is missing from
     the request"`. Decision 1's "the llm role listens empty" holds. 🔴 At the same time,
     **`/health` is no evidence of warmth** (ok with zero models) — decision 3's "warm means
     `loaded` in `GET /models`" is right, and today's code that reads `/health` as warm would
     lie under the router.
  2. **(b) A request during autoload waits.** Asking for the unloaded `beta` logs
     `ensure_model: waiting until model name=beta is fully loaded...` and then **returns 200
     with a body** (not refused). Decision 3's "hold with the heartbeat" suffices; the branch
     that would call `/models/load` first is not needed.
  3. **(c) With `--models-max 1`, asking for the second model unloads the first and loads
     the second.** Log: `models_max reached, request for name=beta queued at position 1` →
     `tick: evicting idle LRU name=alpha for a queued request` → `stopping model instance
     name=alpha`. `/models` flips to `alpha: unloaded / beta: loaded`, and asking for `alpha`
     flips it back (200 both ways). Unload precedes load, so there should be no interval with
     VRAM doubled — **the GPU confirmation remains** (and what "idle LRU" does to a model
     mid-generation is unmeasured). The child is a **separate process** the router spawns on a
     free loopback port (`--port 51219`) with `--alias alpha --ctx-size 256 --model …` — shown
     verbatim in `/models`' `status.args`. With `--api-key` given to the router, **the child
     does not receive it** and its port's `/health` answered 200 without a key — reachable
     only inside the task's netns.
  4. **(d) `stream_options.include_usage`'s `usage` arrives through the router**, in the last
     chunk before `[DONE]` (`"usage":{"completion_tokens":4,"prompt_tokens":43,…}`, once).
     Streaming and non-streaming both return **`model` as the catalogue id** (`beta` /
     `alpha`). **The window is per model**: `/props?model=alpha` reports `n_ctx: 256`, `beta`
     `512` (from `[*]`).
- **R2. Three SSM facts.** (a) The CP task role holds `ssm:PutParameter` / `GetParameter` /
  `GetParameters` / `DeleteParameter` / `AddTagsToResource` on `parameter/af-ws/*`
  (`SsmWorkspaceParams` in `20-platform.yaml`) — decision 2's "already held" is correct. (b)
  **A leaf and a child coexist**: creating `/af-ws/<x>` then `/af-ws/<x>/child`, and the
  reverse order, both succeeded (created and deleted on the dev deployment). Putting
  `/af-ws/engines/<key>/active` under `/af-ws/engines` works. (c) 🔴 **The Standard tier caps
  a value at 4,096 characters** — a 4,200-character `PutParameter` was refused with
  `ValidationException: Standard tier parameters support a maximum parameter value of 4096
  characters`. Advanced is 8 KB at $0.05 per parameter-month. Today's engine table is 666
  bytes. The text's "a few KB" was written without knowing the 4 KB wall.
- **R3. The CP task role as it is (`20-platform.yaml`).** ECS: `CreateService` /
  `UpdateService` / `DeleteService` / `DescribeServices` / `ListServices` /
  `RegisterTaskDefinition` / `DeregisterTaskDefinition` / `DescribeTaskDefinition` /
  `DescribeTasks` / `ListTasks` / `TagResource` (resource `*`); **no `ecs:RunTask`**.
  `iam:PassRole`: `ExecRole` and `WsTaskRole` (to `ecs-tasks`) and `af-*-slot` (to `ec2`).
  **Not one S3 action** — neither `GetObject` nor `DeleteObject` on the models bucket. So
  decision 6 needs `ecs:RunTask` (family-scoped) and `iam:PassRole` for **`IngestTaskRole`
  only** — the execution role's PassRole already exists, and the text's "the ingest task role
  and the execution role" is half redundant. Meanwhile decision 6's "create the
  `engine_models` row after the `upload` container has written the manifest" (the CP reading
  the manifest) and decision 7's "delete (the S3 file too)" **use a permission the CP does
  not have**.
- **R4. The template budget (question 3).** `60-engines.yaml` is **50,774 bytes (426
  left)**. The twelve parameters to retire are **2,558 bytes** (llm's six 1,475, image's four
  1,083); the old command lines (`-m` / `--alias` / `-c`) and the table's `models` /
  `contextTokens` about 450 — **roughly 3,400 bytes freed**. What is added, estimated:
  `LlmEnabled` / `ImageEnabled` 350; `EngineTaskRole`'s `GetParameter` 250; the `ImageEngine`
  and `ImageComfyTag` parameters plus a condition plus four `!If`s (image, entrypoint,
  command, the table's `health`) 1,100; the CP-facing `AWS::IAM::Policy` 800 and the table's
  `ingest` block 250; the generalised fetch sidecar (SSM read, `jq`, loop, preset, command
  line) about 1,800 against today's 648, **+2,300 when copied into both roles**; the
  placeholder wrapper ×2 400 — **about 5,450 in total**. Net: **about 2,000 bytes over the
  wall**. With the sidecar body held once in `Mappings` and pulled by `!FindInMap` from both
  roles it shrinks to +1,200 and the overrun to about 950. **The change set does not fit as
  is** — but comments are **15,568 bytes** and parameter Descriptions about 5,500, so moving
  the two measured notes on the volumes and the harness-facing `run-task` recipe (a little
  over 3 KB) to `PARAMETERS-60-engines.md` makes room. `env.sh`'s `af_cfn_deploy` switches
  to S3 past 51,200, but CI's case 3b-2 fails on the wall, so that is not the route. One
  more: **there is no `EcrComfyUri` in `20-platform`** (its ECR repositories are
  `af-control-plane` / `af-workspace` / `af-voicevox` / `af-llamacpp` / `af-sdcpp`).
  Decision 4's `!If` exists only once `20-platform` (22,530 bytes, room to spare) gains a
  repository and `standup.sh`'s images step copies the fleet-built image in.
- **R5. What is inside the images.** sd-server's `master-cuda` is based on
  `nvidia/cuda:*-cudnn-runtime-ubuntu*` (upstream `docker/Dockerfile.cuda`; `ENTRYPOINT
  /sd-cli`, only `libgomp1` added by apt), so **`sh` is there** — decision 1's placeholder
  (`sh -c '… sleep infinity'`) can be written against the same image. The fetch sidecar's
  `public.ecr.aws/aws-cli/aws-cli:latest` ships **`jq`, `python3`, `bash` and `sh`** (checked
  with `crane export`), so the active set can stay JSON and be read in the sidecar.
- **R6. The controller as it is (`engine_control.go`).** `decideEngineAction` does nothing
  under `mode=on` with desired ≥ 1 (`engineReasonOn`). The start deadline applies **only to
  ondemand while `starting`**; a service that is `running` with `warmed()` false forever is
  **not a failure and is polled every 5 s indefinitely** (`engineControlBusyInterval` in
  `tick`). Once decision 1's placeholder (`sleep infinity`) reaches RUNNING, an ondemand
  deployment is harmless as long as the gateway never wakes it, but **under `mode=on` an
  administrator keeps buying a "no model" box at $1.26/hour and the panel says "starting"
  forever**.
- **R7. `generate_image` as it is.** `imagegen.Request` **already has `Model`** ("Empty means
  the provider's own default") and `Caps(model)` is per (provider, model) — decision 5's
  `model` argument fits 0069's shape. Nothing like `loras` exists in `Request` or `Caps`. The
  tool's arguments follow the rule "**offered only when there is a real choice**" (`provider`
  only with two or more routes; `aspect_ratio` as the union across routes), and there is **no
  `model` argument today**. The Agent's catalogue sits on the tools/list path (**every turn**)
  with a 10-minute TTL; the Agent's `POST /engine/usage` (`routes.go`) **already exists** as
  the CP → Agent reverse path — decision 7's `catalog-changed` can take the same shape.
  `opencode.ApplyEngineChange` and the per-model `engineProviderEntry` exist as the text says.
- **R8. HF today.** FLUX.1-schnell: `license: apache-2.0`, `gated: auto` (as the text says);
  FLUX.2 klein 4B and Z-Image-Turbo: `apache-2.0`, `gated: false`. 🔴 **FLUX.1-dev and SD3.5
  Medium report `cardData.license` as `"other"`**, with the substance in `license_name`
  (`flux-1-dev-non-commercial-license` / `stabilityai-ai-community`). If decision 2's manifest
  copies only `license` (`cardData.license`), **the two non-commercial models come out
  labelled "other"**.
- **R9. `useLocalStorage` is an update, not a replacement.** In the
  `AWS::ECS::CapacityProvider` schema the create-only properties are `Name`, `ClusterName`
  and `InstanceLaunchTemplate/FipsEnabled` (`CapacityOptionType` conditionally);
  `LocalStorageConfiguration` is not among them. `ImageUseLocalStorage=true` goes in with one
  stack update and the association list does not move — open question 9's measurement is
  "flip one parameter, run the harness once".
- **R10. The dev deployment was not woken.** No GPU measurement (cost $0). The SSM parameters
  created for R2 were deleted.

### Proposed revisions, decision by decision

- **Decision 1** — five points. (a) **With P1 postponed, the P0 llm role stays on `-m` and
  cannot "listen empty"** (R1(a) is a property of the router). P0 gives both roles the same
  wrapper: the sidecar writes `/models/cmdline` from the active set, and the engine container
  starts with `sh -c 'if [ -s /models/cmdline ]; then exec … $(cat /models/cmdline); else
  sleep infinity; fi'` (R5: every image has `sh`). The day the llm role moves to the router
  (P1), the one line the sidecar writes becomes `--models-dir … --models-preset …`; the
  wrapper is unchanged. (b) **Treat `ParameterNotFound` as "empty"** — `60-engines` is built
  before `30-ingress`, so at stack creation no CP exists and no active set has been written.
  If `set -e` catches that, the two-pass stand-up returns in a new shape. (c) **Add "do not
  start when the catalogue is empty" to the controller** (R6): `engineSnapshot` carries
  `hasModels`, and `decideEngineAction` answers `engineReasonNoModel` even under `mode=on`.
  The panel shows "no model enabled" instead of a toggle. The gateway's `503
  engine_unavailable` alone leaves the `mode=on` hole open. (d) Stand-up **still buys one GPU
  box for stabilisation, as today** (the placeholder carries the `GPU`
  `ResourceRequirements` too) — what disappears is the second pass, not the first ten minutes
  and $0.2. Write it down. (e) Under those conditions the two-pass stand-up really does go.
- **Decision 2** — (a) The IAM premise is correct (R2(a)), and adding the box-side
  `GetParameter` to `EngineTaskRole` inside `60-engines` matches 0071 decision 8's "new IAM
  closes inside the stack". (b) 🔴 **Change "a few KB" to "4,096 characters"** (R2(c)). Keep
  only S3 keys, local names, flags and preset material in the active set; `description` and
  `license` stay in the DB. Pin with a test that "an active set of 20 models and 20 LoRAs
  fits in 4,096 characters", and decide on the Advanced tier ($0.05/month) **when** a design
  change breaks that. (c) Leaf and child coexist (R2(b)); the name can stay. (d) A parameter
  the CP creates is outside CloudFormation — state that `teardown.sh` deletes
  `/af-ws/engines/*/active`. (e) The sidecar can read JSON with `jq` (R5); no need for a
  line-oriented form. (f) The S3 layout move (`image/…` → `image/checkpoints/…`) and decision
  7's seed (`modelS3Key`) move **in the same step** — a seed pointing at the old key makes
  the first start fetch nothing.
- **Decision 3** — the "depends on open question 1" caveat **can go** (R1). The router meets
  the requirements: empty is ok, autoload waits, `--models-max 1` evicts the LRU before
  loading, `usage` and `model` arrive, the window is per model. The text's redefinition of
  `warm` (`loaded` in `GET /models`) became mandatory with R1(a) — today's `warmup`, reading
  `/health`, would always say true under the router. Two details to add: `404 model_unknown`
  is the gateway's own verdict and the router itself says **400** (either is fine; do not mix
  the numbers); the children run on free loopback ports without `--api-key` (acceptable —
  unreachable outside the task's netns — but write it down). One GPU question remains: what
  "idle LRU" does to a model **mid-generation** (does it wait).
- **Decision 4** — (a) The `ImageEngine` `!If` comes after R4's budget is cleared, and
  presupposes **one ECR repository in `20-platform` and an addition to `standup.sh`'s images
  step** (the fleet-built image is baked in CI and copied in, as `af-workspace` is; 0071
  decision 6). (b) 🔴 **Decision 10's "default klein 4B" is a ComfyUI measurement, not an
  sd-server one.** P0's image role is sd-server, so **P0's default is SDXL** (the only
  single-file checkpoint measured on sd-server). sd.cpp claims klein support, but the
  split-model flag assembly (decision 4's "a split model is one entry") is unmeasured with it, and stepping on
  that in P0 stalls the definition of done on an sd-server problem. (c) **The "second
  checkpoint" P0's definition of done needs is undecided.** The only single-file
  sd-server-ready checkpoint in the bucket is SDXL base; Z-Image and klein are split models.
  Ingest another SDXL-family file first (an OpenRAIL++ fine-tune, or the Refiner) — chosen by
  decision 10's rules. (d) `UpdateService --force-new-deployment` is within the CP's existing
  permissions (R3).
- **Decision 5** — 🔴 **"the CP rejects it at request time" cannot coexist with decision 4's
  "the gateway does not read the body".** On the sd-server route the LoRA sits inside the
  prompt's `<sd_cpp_extra_args>`; on the ComfyUI route inside the workflow JSON's nodes — for
  the CP to reject, it would have to parse the prompt or the graph. Move the rejection to
  **the Agent** (refuse names outside the enum and `baseModel` mismatches at assembly) and
  **the box** (only enabled LoRAs are synced, so an absent name fails in the engine;
  `--lora-model-dir` and `models/loras/` bound the path), and delete the CP line. `model` /
  `loras` fit 0069 (R7): `Request.Model` exists; add `Loras []{name, description, baseModel}`
  to `Caps`. Two rules to write: **an argument is offered only when there is a real choice**
  (as with `provider` — `model` appears only when the fleet engine has two or more enabled
  checkpoints, and its values are catalogue ids only; the Codex / agy fixed models are not
  mixed into the enum; `model` given with a non-fleet provider is refused by name); **an enum
  cannot depend on another argument**, so `loras`' enum is every enabled LoRA, the
  description names each one's `baseModel`, and the pairing check is the Agent's.
- **Decision 6** — (a) `ecs:RunTask` really is absent (R3), so it is needed. `iam:PassRole`
  is for **`IngestTaskRole` only** — `ExecRole`'s PassRole is already in `PassTaskRoles`. Fix
  "the ingest task role and the execution role". (b) 🔴 **The CP cannot read the manifest**
  (no S3 permission). Build the row from **the sha256, license and bytes the CP itself
  resolved from HF, plus `DescribeTasks`' exit codes (`fetch` SUCCESS → `upload` SUCCESS)** —
  that is enough (`fetch` has already verified the sha256). State that the manifest is what
  the box reads and the CP never does. (c) The alternative that keeps "zero CP IAM" is
  **EventBridge**: the CP writes `/af-ws/engines/ingest/job` (existing PutParameter), an
  `AWS::Events::Rule` in `60-engines` (`aws.ssm` Parameter Store Change) calls `RunTask`
  with a role inside the stack, and the CP follows with `ListTasks --family`. It keeps the CP
  side at zero, but delivery is at-least-once (a job can run twice) and failures appear only
  in CloudTrail. **The recommendation is to accept the text's `AWS::IAM::Policy` and revise
  0071 decision 8 precisely**: "another stack may add to the CP role only **permissions that
  act on that stack's own resources** (family-scoped RunTask, PassRole limited to the ingest
  role) — never `Resource: *`". (d) CP egress to the HF API is presupposed (beyond
  30-ingress's NAT). A deployment whose own front narrows egress falls back to hand-run
  ingestion.
- **Decision 7** — 🔴 **"delete (the S3 file too)" is not something the CP can do** (R3).
  Give the ingest task a `MODE=delete` and delete through the same `RunTask` (the
  manifest-first order is kept there), or leave it to a sibling of
  `harness/ingest-model.sh` until P4. The reverse path for the push notification exists (R7).
- **Decision 10** — (a) 🔴 The manifest and `engine_models` carry **all three of `license`,
  `license_name` and a URL** (R8: the two non-commercial models are `license: other`). The
  value taken at ingest is a **snapshot** that does not move when the HF card changes — as
  the text says. (b) Record **who accepted and when** (`licenseAcceptedBy` / `At` in the
  manifest and the audit log). (c) Fitness for a multi-tenant deployment (question 8): "the
  operator takes on the terms on behalf of every member" is the correct reading, and it goes
  into the UI as that one sentence. Add a **`commercialUse` axis** — FLUX.1-dev's
  non-commercial clause makes "a deployment that serves members for a fee" the operator's
  own violation, so next to "ingestion is not refused" show "must not be added if this
  deployment is commercial", derivable from the licence name (Stability's $1M revenue line
  goes in the same column). (d) `HF_TOKEN` is tied to a personal account — it lapses when the
  person who agreed leaves. Recommend an organisation account's token.
- **Open question 1** — **settled** (R1). What remains is one GPU check of (c), which is the
  same observation as P1's definition of done (the reload on swap).
- **Open question 5** — **a table.** Three reasons: (1) there are two writers (the
  administrator's toggles and the ingest job's transitions `ingesting → ready / failed`); a
  single `SettingsStore` JSON is read-whole-write-whole with no CAS, and concurrent writes
  lose one; (2) `files[]` / `args[]` / `sizes[]` differ in shape per row, and the panel's
  list, the seed and deletion are all row operations; (3) the two-dialect migration has the
  `engine_hourly` precedent (`migrations/0055` and `migrations-pg/0040`) with a known cost.
  **Do not carry `lastUsedAt` in P0** (avoid a per-request UPDATE; it can be added in P4 in
  the shape of `engine_<key>_demand_at`'s once-a-minute throttle).
- **Open question 9** — **worth measuring; one GPU hour covers two things.** (1)
  `useLocalStorage`: per R9 it goes in with one parameter. It affects not only the swap
  (12.3 GB at 92 MB/s → NVMe) but **the cold start's S3 → EBS 33 GB at 332-360 s**, a number
  pinned to the EBS write ceiling (g6.xlarge's 125 MB/s baseline). (2) Measure **solving it
  with RAM** alongside: the three models (27 GB) fall out of the page cache because the box
  has 15 GB; raising `ImageMemMinMiB` to 30,000 selects a g6.2xlarge (32 GiB) and changes no
  code. If the swap becomes RAM → VRAM (seconds), `useLocalStorage` goes back to being about
  start-up time only. `bench-image-engine.sh` measures both as it is (two phases include the
  swaps). Order: before P2 — independent of building the ComfyUI image, and it changes P2's
  definition of done (the price of a swap).
- **Phases** — P0 gains: the wrapper entrypoint, the `ParameterNotFound` handling, the
  controller's "do not start when empty", the comment move to `PARAMETERS-60-engines.md` (R4),
  the second checkpoint, `teardown.sh`'s SSM clean-up, and the S3 move done together with the
  seed. P1 is no longer bound by open question 1 (R1). P2 includes the `20-platform`
  repository and CI for the fleet-built image. P4 includes `MODE=delete`.

### Answers to the questions asked

1. **Decision 2's SSM closes** (R2(a)) — zero additions on the CP side, one `GetParameter`
   statement inside `60-engines` on the box side, consistent with 0071 decision 8. What does
   not close is **the size of the value**: 4,096 characters (R2(c)).
2. **It stabilises, under three conditions** (decision 1's revisions (a)(b)(c)): both roles on
   the wrapper, `ParameterNotFound` as empty, the `mode=on` hole closed. There is no container
   health check (ECS's RUNNING means "the essential container started") and that is all
   CloudFormation waits for, so `sleep infinity` stabilises. The second pass goes; the one GPU
   box for stabilisation stays.
3. **It does not fit.** 3,400 freed against 5,450 added (4,350 with `Mappings` sharing),
   about 1-2 KB over (R4). Moving 3 KB of the 15.5 KB of comments to
   `PARAMETERS-60-engines.md` makes it fit. `20-platform` also needs an ECR repository.
4. **The rejection sits in the wrong place** (decision 5's revision) — the CP does not read
   the body, so the Agent and the box reject. `model` fits `Request.Model`, `loras` fits with
   an addition to `Caps` (R7). Arguments only "when there is a real choice", values only the
   fleet's catalogue ids.
5. **There is an alternative** (EventBridge), not recommended. Accept the `AWS::IAM::Policy`
   and revise 0071 decision 8 to "permissions that act only on that stack's resources may be
   added". PassRole for the ingest role only (R3). **No S3 read or write is added** — the CP
   does not read the manifest, and deletion is the ingest task's (decisions 6 and 7 revised).
6. **A table** (open question 5 above). No `lastUsedAt` in P0.
7. **The description alone is not enough.** "The model that is warm now" is something the CP
   can hold as **the model of the last successful request**, from `POST /engine/usage`'s
   `model` (never asked of the engine; 0053). Expose it on the catalogue row as `warm_model`
   and **decide the default for an unspecified `model` server-side: the warm one if any, else
   the catalogue default** — do not make the agent reason about temperature. A request that
   swapped returns "model switched, +N s" in `warnings`. `useLocalStorage` is worth measuring
   (open question 9 above; one GPU hour, alongside the RAM option).
8. **Sound, with three additions** (decision 10's revision): carry `license_name`, record the
   acceptance, and add the `commercialUse` axis with the "if this deployment is commercial"
   sentence.

### Proposed status line

Once decisions 2, 5, 6, 7 and 10 above are reflected in the text, change the status line to
**"approved (P0 may start)" (reviewed 2026-09-08)**. Strike open question 1 as "settled
(review R1)". Add to P0's definition of done: **"an engine in `mode=on` does not start while
the catalogue is empty"** and **"at stack creation (no CP, no active set) both roles'
services stabilise"** — the former is R6's hole, the latter is the observation of decision
1's own claim.

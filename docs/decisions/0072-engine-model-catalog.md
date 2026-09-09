# 0072. A model catalogue for the engines — swap the LLM and SDXL models without a CloudFormation run, and pick LoRAs per request

English | [日本語](0072-engine-model-catalog.ja.md)

- Status: **P0, P1 and P4 implemented and verified on hardware (2026-09-08..09). Of P5, only
  registering the Hugging Face token from the Console (open question 12) is implemented, and
  not yet verified on hardware (2026-09-09, "P5 implementation"). P2, P3 and the rest of P5
  are not started.**
  Drafting, review, revision and implementation all happened the same day. **As drafted**, every
  number was quoted from ADR 0071's measurements and the upstream facts (llama.cpp,
  stable-diffusion.cpp) were read that day from the repositories' `tools/server/README.md`,
  `examples/server/api.md` and `docs/lora.md`: **nothing was newly measured for the draft**, and
  what had to be measured before a decision could stand is listed under *Open questions*, each
  decision naming the question it depends on. What was measured afterwards is in two sections —
  *Resolved by measurement* (ComfyUI) and *P0 measurements* (the implementation).
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
- **P0 was implemented and verified on real hardware the same day** (the "P0 measurements"
  section): the catalogue table, the seed, the active set in SSM, one role-independent fetch
  sidecar, both roles' idle wrapper, `no_model`, the admin API and panel, the `catalog-changed`
  push, and declared `sizes[]`. Of the definitions of done, **the first and third were driven on
  hardware** (same prompt and seed, SDXL to Juggernaut-XL v9, with `describe-stacks` reporting an
  unchanged last-update time; and the services stabilising with no Control Plane and no active
  set); the second is unit-tested only. 🔴 Three things bit on the way: **a YAML folded block
  left the sidecar doing nothing** (measurement 2), **nothing stopped a box that reaches RUNNING
  and never warms** (4), and **the straightforward active set did not fit 4,096 characters at 20
  models and 20 LoRAs** (1). Two things P0 needed that the decisions did not name — a way to
  create a catalogue row, and a harness for seeing a picture — are at the end of that section.
- The same day, **P1 (the llm role's router mode) was implemented and measured, on a CPU and on
  hardware** (the "P1 measurements" section): preset generation, syncing every enabled model,
  `LlmModelsMax`, the redefinition of `warm`, and per-model windows. **All three definitions of done were driven on
  hardware** (two GGUFs usable on one box, the swap answering on one attempt, and — once the
  second row was registered and enabled in the Console — two models in the launch menu with a
  session started on the second). Only creating the row needs a person: it is a super_admin
  screen, the same wall P0 hit. 🔴 Three points of the
  text were corrected by measurement: **`--models-dir` is not used** (it would list names the
  catalogue does not hold and count `llm/loras/` as a model), **`-c` must leave `LlmExtraArgs`**
  (a command-line flag beats the preset, so one `-c` gives every model the same window), and
  **warm is "any model loaded", not "the default model loaded"** (a box that swapped models would
  read as answering-but-not-warm, and the `unwarmed` rule would stop it mid-conversation).
- The next day (2026-09-09), **P4 was implemented and pressed on hardware** (*P4 measurements*,
  *P4 on hardware*). **All three halves of the definition of done passed** — one ungated model
  (491 MB, 72 seconds from button to row), the gated refusal (which starts no task at all), and
  a gated ingest (FLUX.1-dev, 23.8 GB, 15 minutes 51 seconds). 🔴 **Six defects appeared that
  only pressing could find, one of which had killed an entire path**: `HfTokenSecretArn` alone
  does not start the ingest task, because ECS resolves `Secrets` as the **execution role** and
  that role had no grant. The other five: a finished ingest does not put its row on the list; a
  failure reaches a Japanese screen in English (the guard read only constants, and these codes
  were literals); a free-text filename, and shards in the picker; an upsert on an existing id;
  and **no way at all to ask for `purge`**. Four things hardware needed that the decisions did
  not name: the candidate-listing API, the window read off the model with the output cap as a
  fraction, the `source` column, and a two-step delete.
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
   becomes per model.** `llama-server --models-preset /models/llm/presets.ini --models-max
   <LlmModelsMax>`. The fetch sidecar generates the preset from the active set (section name =
   catalogue id, `model` = path, `c`, `n-gpu-layers`, fixed LoRAs). `-m`, `--alias` and `-c`
   leave the command line.
   - 🔴 **No `--models-dir`** (P1 measurements 1 and 3). The draft used it alongside the preset,
     but a directory scan makes the FILE NAME the model name: ids the catalogue does not hold
     would appear in `/models` (against decision 7), and the `llm/loras/` subdirectory would be
     counted as one model (upstream reads a subdirectory as a multi-shard GGUF). A preset section
     **defines** a model whether or not a file of that name exists, so the catalogue id is the
     model id. `alias` is not needed either — the router passes `--alias <section>` itself.
   - 🔴 **`-c` must never be in `LlmExtraArgs`** (measurement 2). The router hands its own command
     line to every child and a command-line argument beats the preset, so a single `-c 32768`
     erases every model's declared window (measured: two models declaring 4096 and 384 both came
     up at 32768). The template's Description and the parameter reference say so as a prohibition.
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
     "`GET /models` shows **at least one model as `loaded`**", and the health path stays
     `/health`. The default is the one catalogue entry flagged `default`, written into the preset
     as `load-on-startup = true`. Without it the first request pays "box start 527 s + load
     267 s".
     - 🔴 **Not "the DEFAULT model is loaded"** (P1 measurement 4). Under `--models-max 1` a box
       serving the other model has unloaded the default, so tying warm to the default reads as
       answering-but-not-warm — and `running && !warmed` for 900 seconds is what P0's `unwarmed`
       rule stops, mid-conversation. `sleeping` and `loading` are not warm either: both mean the
       next request pays for weights.
     - The engine table declares it as `warmPath` (`/models` for llm, empty for image) rather than
       deriving it from the provider name (ADR 0053). `GET /models` triggers no autoload and does
       not reset the router's idle timer, so polling it every 30 seconds is free (measurement 5).
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
     One question about "idle LRU" and a model that is **generating** remained, and
     🔴 **it is settled too (P1 measurement 7): the eviction waits.** The interrupted stream was
     delivered whole and the waiting request paid "the rest of that answer plus the load" (48.6
     seconds of waiting, on a CPU). So the price of `--models-max 1` is a wait rather than a
     broken answer. Streaming rides it out on the heartbeat; **a non-streaming request cannot** —
     the gateway's own 45-second hold (`AF_ENGINE_PLAIN_HOLD`, deliberately inside the ALB's 60)
     ends it first with a retryable `503 engine_waking`.

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
   - 🔴 **A gated repository publishes its metadata anonymously** (P4 measurement 1):
     `api/models/<repo>?blobs=true` answers with the licence, the gating flag and **every file's
     sha256 and size** with no token, and only the DOWNLOAD is 401. So the CP resolves without
     ever holding the operator's token, and a gated repository on a deployment with no token is
     refused BEFORE a task is started — nine minutes of Fargate ending in a 401 costs money and
     explains nothing.
   - **Revised (2026-09-09, open question 12; implemented)**: ~~the token is configured outside
     the CP, as the `HfTokenSecretArn` CloudFormation parameter~~ → **the record of truth for
     the token is the CP's own database (sealed with the `custodian`), and Secrets Manager is
     the transport for a value ECS can be handed no other way. The CP holds `PutSecretValue` on
     that one secret and never `GetSecretValue`** — it cannot read back what it wrote, so "the
     CP does not hold the token" **weakens but does not vanish**. Only the ingest task reads the
     value, and the anonymous-metadata point above stays exactly as it is (**only the
     registration path changes**). The stack creates the secret **always**, holding the sentinel
     `-`, so the `Secrets` block is always there and the CloudFormation round trip is gone.
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

9. **The effect on the cold start is stated in numbers, and what is synced differs by role.**
   S3 → EBS is a steady 104–147 MB/s (ADR 0071 measurement 8; P1 measurement 9 adds 116.7 and
   139.7 MB/s), so the llm sync time is **the sum of the enabled GGUFs' sizes** — 179 seconds per
   18.5 GB.
   - **The llm role (a router) syncs every enabled model.** It is expected to answer for any of
     them, and a model whose file is missing does not wait — it fails with 500 (P1 measurement 6).
     So on this role enabling a model literally means "the next start takes N seconds longer".
   - **The image role syncs only the selected checkpoint.** sd-server holds one, so there is no
     reason to put somebody else's checkpoint into every cold start.
   - The admin panel shows "sync +N s (estimate)" next to the toggle. **The size is declared when
     the row is registered** — the CP cannot look in S3 (R3) — so it lives in `files[].bytes`,
     the same posture as the manifest, and as with `observed_secs` **it says estimate**.
   `useLocalStorage` (ADR 0071 open question 1) stays as it is.

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

11. **Sources can be searched. "You already know the repository name" must not be the only way
    in.** (Added 2026-09-09, P5. P4 turned a free-text FILENAME — where a typo and a repository
    that does not carry the file are the same refusal — into a picker; **the same hole was still
    open on the repository name**: look `owner/name` up in another window, then paste it.)
    - `POST …/ingest/search` (super_admin), taking `q` and a `source` that says which of Hugging
      Face and Civitai to ask (HF by default). **Both APIs answer anonymously** (P4 measurements
      1 and 2), so search needs a token no more than resolving does — decision 6's "the CP only
      reads the source" extends unchanged.
    - **Filtered per role** (measured 2026-09-09): `filter=gguf` for llm, `pipeline_tag=text-to-image`
      for image, ordered by `sort=downloads&direction=-1`, 20 at most. Civitai uses
      `types=Checkpoint` (image); `types=LORA` follows in P3. The filter exists for the same
      reason `ingest/files` has one — **show no dead ends**: a repository this engine cannot
      load is a refusal from `resolve` a moment later.
    - 🔴 **One read carries everything the verdict needs, but none of it can be passed through.**
      `expand[]` yields `gated`, `cardData`, `downloads`, `likes`, `lastModified` (and `gguf` for
      llm). But **`cardData` carries `extra_gated_prompt` and `gguf` carries the whole
      `chat_template`** (measured: FLUX.1-dev's gating prompt and Qwen2.5-Coder's chat template
      each exceed 1 KB on their own). **The CP copies `license`, `license_name`, `gguf.total` and
      `gguf.context_length` and nothing else** — a screen that draws 20 rows does not carry 20
      kilobytes nobody reads.
    - **A result is a destination, not an ingest.** Picking one feeds the existing
      `ingest/files` → `ingest/resolve` → acceptance → `ingest`. **The sha256 and the licence of
      record are what `resolve` read**; the list's values are a draft (HF cards move). For
      Civitai the hit carries `modelVersions[0].id` — an ingest wants the **version id, not the
      model id**.
    - **Search is not a precondition for ingest.** Typing `owner/name` or a URL stays. A
      deployment with closed egress loses search too, and there decision 6's hand-run route
      simply goes back to being the main one.

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

## P0 measurements (2026-09-08, the dev deployment)

Measured while implementing P0, on the development deployment (`af-sandbox` / ap-northeast-1).
Of the three definitions of done the phases section gave, **the first and the third were driven
on real hardware and the second is unit-tested only** (why: 10). About forty minutes of
g6.xlarge, under $1.

1. **The Control Plane seeded the catalogue from the stack and published the active set**
   (decisions 2 and 7). Its own log at start:
   `engines: llm active set published to /af-ws/engines/llm/active (152 bytes)` and
   `engines: image active set published … (132 bytes)`. The documents:

   ```
   {"v":1,"key":"llm","start":"qwen3-coder-30b-a3b","models":[{"id":"qwen3-coder-30b-a3b",
    "f":["llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"],"c":32768}]}
   {"v":1,"key":"image","start":"sdxl-base-1.0","models":[{"id":"sdxl-base-1.0",
    "f":["image/checkpoints/sd_xl_base_1.0.safetensors"]}]}
   ```

   **132–152 characters against the 4,096 of R2(c).** The pinned "20 models and 20 LoRAs" comes
   to 3,200 — but the straightforward shape **did not fit at 4,280**, even while obeying
   decision 2's "S3 keys and flags only". It took two more cuts: LoRAs became bare S3 keys (the
   box scans a directory, so it needs no name and no description) and a flagless file became a
   bare string. 4 KB is not headroom to be careful with; it is what decides the shape.

2. 🔴 **The sidecar came up doing nothing. A YAML FOLDED block (`>-`) does not fold a
   more-indented line.** The jq filter, indented to line up under its own `jq -r`, kept its
   newline, so the shell ran `jq -r --arg s "$START"` (which dumps the whole document) and then
   looked for a command called `[(.models[]?|…`. `/models/cmdline` stayed empty, the engine came
   up as the placeholder, **the service reached a steady state and nothing anywhere said why**.
   That is a GPU box and ten minutes spent arriving at "no picture, no reason". Fixed by a
   literal block (`|-`). What stops it happening again is
   `deploy/local/engine-sidecar-test.sh`, which **pulls the script out of the template as it is
   actually deployed and runs it** against a stub `aws` and the real `jq`: shell inside a
   CloudFormation `Mappings` entry has no type check, no linter, and its only feedback is a GPU
   box ten minutes later.

3. **Decision 1(b) and 1(d) were observed as they stand.** A task that started during the stack
   update (10:17:25), before the Control Plane had been replaced and before any active set
   existed, logged
   `engine fetch: no active set at /af-ws/engines/image/active - this engine has nothing to load`,
   idled, and **the service reached a steady state**. That is the third definition of done —
   "with no Control Plane and no active set, both roles' services stabilise" — and it was not
   even contrived. The two-pass stand-up is gone.

4. 🔴 **The same observation exposed a hole. That task stays RUNNING and never warms.** The
   `no_model` rule of decision 1(c) does not fire, because the catalogue is not empty. The start
   deadline only covers `starting` (R6) and `running && !warmed` is not a failure, so $1.26/hour
   runs with nothing to stop it. Added: **RUNNING past the start deadline without ever having
   warmed is a failed start that happened to reach RUNNING** — stop it, count it, cool down. The
   clock does NOT start at "it is not warm right now": it starts at the latest of the
   deployment's creation, **when this Control Plane started watching**, and when the engine was
   last warm. Anything else stops a healthy engine over one failed probe or one CP replacement.

5. **The switch works on real hardware (definition of done 1).** Same prompt, same seed 42, same
   512×512:

   | | SDXL base 1.0 | Juggernaut-XL v9 |
   |---|---|---|
   | S3 → EBS | 6,938,078,334 B in **64 s** (108 MB/s) | 7,105,348,188 B in **39 s** (182 MB/s) |
   | cmdline | `-m /models/image/checkpoints/sd_xl_base_1.0.safetensors` | `-m /models/image/checkpoints/juggernaut_xl_v9.safetensors` |
   | VRAM | 6,624 MB | the same family, so much the same |
   | first 512px after loading | **11.4 s** | **21.5 s** |
   | PNG | 455,316 B | 430,587 B (**visibly a different picture**) |

   And **`describe-stacks` reports `LastUpdatedTime` as `2026-09-08T10:49:24.476Z` both before
   and after** — "the checkpoint changed without touching CloudFormation" is literally true. The
   first-picture times (11.4 s / 21.5 s) are longer than ADR 0071's warm 512px of 7.8 s; that is
   the one-off cost carried by the first generation after a load, and with two points it is a
   range and nothing more.
   - 🔴 **Stated plainly: the switch was made by publishing the active set by hand, not by
     pressing the Console's button.** Driving the super-admin screen needs a browser session and
     this measurement was made from AWS credentials alone. That the hand-written document is
     **byte-identical to what the CP's `publishActiveSet` writes** is pinned by
     `TestEngineActiveSetForTheDevDeployment`. The only link not driven on hardware is "the admin
     route writes the row", which is `TestEngineAdminModelLifecycle`'s job; the CP's own publish
     is observed above.

6. **Ingesting the second checkpoint** (Juggernaut-XL v9, CreativeML OpenRAIL-M, not gated, one
   file): 7,105,348,188 bytes from Hugging Face in **163 s** (43.6 MB/s), sha256 matched, **48 s**
   to S3. One more point for ADR 0071 decision 3's "Hugging Face delivers at 4–236 MB/s and
   nothing predicts which".
   🔴 **For sd-server it has to be a SINGLE-file checkpoint.** The klein 4B and Z-Image files in
   the bucket are split models and sd.cpp's flag assembly for them is unmeasured (decision 4(b))
   — that is ComfyUI's job in P2.

7. **The S3 layout move** (`image/sd_xl_base_1.0.safetensors` → `image/checkpoints/…`) is a
   server-side copy: instant, and it never touches the NAT. `ImageModelS3Key` was updated in the
   same step, per decision 2(f) — a seed still pointing at the old key produces a row whose file
   is not there, and the first start fails in the fetch sidecar.

8. 🔴 **The G-family vCPU quota of 8 was hit.** Waking a box straight after stopping one fails
   placement for minutes with `VcpuLimitExceeded: your current vCPU limit of 8`, because the
   draining box still holds its 4 vCPU. It is exactly what `PARAMETERS-60-engines.md`'s "The
   G-family quota" describes, and the practical consequence is that **a start/stop/start
   verification cannot proceed until the drain (456 s measured) is over**.

9. 🔴 **This deployment's image role was in `mode=on`**, set by somebody before this work.
   `aws ecs update-service --desired-count 0` is undone within thirty seconds by
   `engine image: start (admin_on)`. **The mode is a Control Plane settings row and AWS
   credentials cannot change it** — stopping it needs the Console's toggle. It is the mirror of
   ADR 0071's "`off` is a persistent setting, not a pause": so is `on`.

10. **The second definition of done — a `mode=on` engine does not start with an empty catalogue —
    was not driven on hardware**, because emptying the catalogue needs the super-admin screen. It
    is pinned by four cases in `TestDecideEngineAction` (not started when empty; stopped if it is
    up; not started even with demand over the threshold; `off` still wins the audit reason), and
    the `unwarmed` rule of 4 by four more in the same table.

### What P0 needed that the decisions did not name

- **A way to create a catalogue row.** The seed makes exactly ONE row per role (decision 7) and
  the other way to make one is P4's ingest API — so with only the toggles there is **nothing to
  switch to, and definition of done 1 is unreachable**. The half of P4 that is not "fetch from
  Hugging Face" — writing down what a file already in the bucket is — was added as
  `POST /api/admin/engines/{key}/models`. It needs no `ecs:RunTask` and no PassRole, so the CP's
  IAM does not grow, and the row is always created disabled. Its pair, `DELETE …/models/{id}`,
  forgets the ROW only (there is no `s3:DeleteObject` — decision 7).
- **`harness/probe-image-engine.sh`.** The engine is on a private subnet and admits only the CP's
  security group, so the only ways to see a picture are a member's `generate_image` and a task
  inside the VPC. The first was not available for this measurement (5), so the second was built.
  It borrows the ingest task definition — two containers, a shared volume, S3 write is exactly
  the shape needed, and the template has 820 bytes of room for a new resource.

## P1 measurements (2026-09-08, this container's CPU and the dev deployment)

Measured while implementing P1, the llm role's router mode. **Upstream behaviour was settled on a
CPU first and only the expensive observations were bought on a GPU** — how the router reads its
command line and its preset does not need CUDA. The CPU runs used llama.cpp's official build
`b10853` (the one review R1 used) with stories260K and Qwen2.5-0.5B-Instruct Q4_K_M; the hardware
runs used `af-sandbox` / ap-northeast-1's g6.xlarge for about half an hour (roughly $0.6).

**All three definitions of done were driven on hardware.** The first and third came from the
engine probe (10 below); the second — two `llamacpp/` models in the launch menu — was driven by
**the user registering and enabling the second row in the Console and starting a session on it**
(15). Only that one link needs a person: creating a row is a super_admin screen and AWS
credentials cannot drive it (the same wall as P0 measurement 5).

### Settled on a CPU

1. **A preset alone is enough, and the section name becomes the model id.** Through upstream's
   "if the key does not correspond to an existing model, give at least the model path" path, a
   section `[qwen3-coder-30b-a3b]` with `model = /models/llm/Qwen3-…-Q4_K_M.gguf` is addressable
   **by the catalogue id, whatever the file is called**. Hence 🔴 **`--models-dir` is not
   used** — the drafted decision 3 named both, but a directory scan turns file names into model
   names, so (a) names the catalogue does not hold appear in `/models` (colliding with decision
   7's "what is not in the catalogue does not exist") and (b) the `llm/loras/` subdirectory is
   **counted as one model** (upstream reads a subdirectory as a split GGUF). A preset-only router
   has neither problem.
2. 🔴 **A command-line `-c` beats the preset's per-model `c`.** Started with `-c 32768`, two
   models declaring `c = 4096` and `c = 384` were both spawned **`--ctx-size 32768`** (visible in
   `/models`'s `status.args`). That is upstream's stated precedence — command line > per model >
   `[*]` — and removing it gives 4096 / 384 as declared. This deployment's `LlmExtraArgs` was
   `-ngl,99,-c,32768,--jinja`, so **P1's first step was taking `-c` out**: one flag erases every
   declared window and nothing says so.
3. **The router's command line is inherited by its children.** `-ngl 99 --jinja` appear verbatim
   in each instance's `status.args`. `--alias` is **added by the router itself**, so the preset
   does not need it (the drafted decision 3 listed `alias`; measurement says it is redundant).
4. 🔴 **An empty router still answers `/health` with `{"status":"ok"}`** (R1(a), re-confirmed).
   `status.value` has five values — `loaded` / `unloaded` / `loading` / `sleeping` /
   `downloading` — and `sleeping` (the `--sleep-idle-seconds` auto-unload, disabled by default at
   -1) is also a state where the next request pays for weights. So warm is **at least one
   `loaded`**. 🔴 **Not "the default is `loaded`"**, which is what the drafted decision 3 said: a
   box that swapped models under `--models-max 1` would read as answering-but-not-warm, and 900
   seconds of `running && !warmed` is exactly what P0's `unwarmed` rule stops — **mid-conversation.**
5. **`GET /models` triggers no autoload and does not reset the router's idle timer** (upstream's
   exemption list), which makes it safe as the target of a probe that runs every 30 seconds.
   `GET /props?model=` **does** trigger a load, so it must not be used for that.
6. **A preset entry whose file is missing does not stop the router.** It starts, lists the model,
   and only a request for that one fails with `500 model name=… failed to load`. An unknown id is
   **400** (as in R1).
7. 🔴 **`--models-max 1` evicts only an IDLE model — it waits for a generation to finish.** With
   900 tokens streaming from one model, a request for the other logged `models_max reached …
   queued at position 1` and sat there for **48.6 seconds** before `evicting idle LRU` moved, and
   **all 854 chunks of the interrupted stream were delivered**. That settles open question 1(c):
   **the answer is "it waits"**, and the price is paid by the waiting request instead ("the rest
   of that answer plus the load"). 🔴 The first attempt at this measurement was a false positive:
   the "interrupted" stream ended after 14 chunks — and so did the control run with nobody
   interrupting, because the model simply stopped. Only with `ignore_eos` (854 chunks, 64.8 s)
   was there a window to interrupt. **"It was cut off" is like "zero hits": check the instrument
   before believing it.**

### Measured on hardware (the dev deployment, g6.xlarge)

8. **Taking in the second GGUF** (Qwen2.5-Coder-1.5B-Instruct Q4_K_M, Apache-2.0, ungated):
   **1,117,320,768 bytes from Hugging Face in 35 s** (31.9 MB/s), sha256 matched, **2 s** to S3.
   One more point for ADR 0071 decision 3's "Hugging Face is unpredictable" (inside the 4–236 MB/s
   band).
9. **The sidecar wrote the router's preset on the real box**, and its log is the evidence:
   `llm/Qwen3-…-Q4_K_M.gguf 18556689568 bytes in 159s` (116.7 MB/s),
   `llm/qwen2.5-coder-1.5b-…gguf 1117320768 bytes in 8s` (139.7 MB/s),
   `preset /models/llm/presets.ini holds 2 model(s), 'qwen3-coder-30b-a3b' loaded at startup`,
   `cmdline = --models-preset /models/llm/presets.ini`. **Decision 9's "the total size lands on
   the cold start" is literal**: 167 seconds for two models against 159 for one.
10. **A switch answers on one attempt** (definition of done 1). Two runs agreed:

    | request | run 1 | run 2 | what happened |
    |---|---|---|---|
    | `qwen3-coder-30b-a3b` (warm) | **0.7 s** | **0.4 s** | loaded at startup |
    | `qwen2.5-coder-1.5b` | **10.1 s** | **10.0 s** | unload the 30B, load 1.1 GB |
    | `qwen3-coder-30b-a3b` (back) | **281.8 s** | **276.3 s** | 18.5 GB back into VRAM |

    All 200, **one attempt, no resend**, and the answers were visibly from different models. The
    276–282 seconds are ADR 0071's 267-second reload plus generation — **this design's price**
    (decision 3). An unknown id was refused by the router with **400**; the gateway's `404
    model_unknown` is its own verdict and the numbers are deliberately different (that one is not
    observed on hardware — reaching the gateway needs a session token).
11. 🔴 **`/health` said "ok" for four and a half minutes while nothing was loaded.** Sampling both
    endpoints every 20 seconds across a switch, all 14 samples had `/health` at
    `200 {"status":"ok"}` while the same instant's `/models` said `qwen3-coder-30b-a3b: loading`.
    **Had P0's `warmProbe` been pointed at the real router, the panel would have said "ready" for
    276 seconds and the `unwarmed` rule would never have fired.** Those 14 samples are the
    evidence behind decision 3's redefinition of warm.
12. **The deployed image is llama.cpp `b10830`** (`org.opencontainers.image.version`, copied into
    ECR on 2026-09-07). Router mode, `--models-max`, custom preset entries and `load-on-startup`
    are all in that build (checked in `tools/server/README.md` at the same commit). `server-cuda`
    is a moving tag, so **the version is decided by the day it was copied**.

### What bit on hardware

13. 🔴 **An on-demand engine started by hand is stopped 90 seconds later.** Right after
    `aws ecs update-service --desired-count 1`, the CP logged `engine llm: stop (idle)` and the
    pending task went away — `engine_llm_demand_at` was left over from earlier work, so
    `now - lastDemand ≥ idle (1800 s)` was true on the first tick. It is the third face of ADR
    0071's "`on` and `off` are both persistent settings": **on-demand cannot be woken by hand**,
    and making demand means going through the gateway, which needs a session token. So the
    hardware run used **`run-task` to put the llm task definition straight onto the capacity
    provider** (the controller only watches the SERVICE, so it never touched it) and talked to the
    task's private IP.
    - The cost: **the CP-side warm probe was not exercised on hardware.** The CP only probes while
      the service is up (`maintainWarm` is driven by the service's state), and a standalone task
      has no Cloud Map A record. Measurement 11 asks the same question from the probe's side, and
      the verdict itself is pinned by `TestEngineWarmProbeReadsTheRouterModelList`.
14. 🔴 **Overriding the probe's task role with the CP's takes away its S3 write.** The engine's
    `--api-key` is an SSM SecureString and the CP task role is the only one that can read it, so
    `run-task --overrides` was given that `taskRoleArn` — and the transcript upload then failed
    with `AccessDenied … s3:PutObject`. **It is review R3's "the CP holds no S3 permission at all"
    restated by AWS.** The transcript goes to the log instead (passing the key as an override
    environment variable would put it in CloudTrail for ever, so that route is not taken).

15. **The launch menu's two models were driven on hardware too** (definition of done 2, with the
    user pressing the buttons). The panel's "register a file from the bucket" created the second
    row (`qwen2.5-coder-1.5b`, window 32768/4096, size 1,117,320,768) and it was enabled. The CP
    log shows the whole chain: `llm catalogue row registered: qwen2.5-coder-1.5b (1 file(s),
    disabled)` → `llm active set published … (242 bytes)` → `catalogue change for llm pushed to
    1 workspace(s)` → the Agent's `GET /internal/engine/catalog 200`. **The picker offered two
    `llamacpp/` models and a session started on the second one.** A later CP replacement
    published the same two rows from the database
    (`models=llamacpp/qwen2.5-coder-1.5b,llamacpp/qwen3-coder-30b-a3b`) — the ordinary
    observation that the rows, not a hand-written document, are the truth.
    - 🔴 **The registration form had no window field.** P1 made the window per model, and the one
      UI that creates a row could not declare one — so a model registered there carried
      `context_tokens` 0, which reaches opencode as context 0 and **switches auto-compaction
      off**. The window (context and output cap, sent only as a pair) and the size (the only
      source for "sync +N s") were added.

### What P1 needed that the decisions did not name

- **`harness/probe-llm-engine.sh`.** Text, not pictures, so `probe-image-engine.sh` does not
  apply; the llm sibling has the same shape (borrow the ingest task definition, wear the CP's
  security group). `--watch` samples `/health` and `/models` side by side every 20 seconds across
  a switch — that is how 11 was measured.
- **`files[].bytes` (a declared size) and the panel's "sync +N s (estimate)".** Decision 9's "the
  sum of the enabled GGUFs' sizes" needs the CP to know a size, and the CP cannot look in S3
  (R3), so **it is declared when the row is registered** — the same posture as the manifest, and
  no migration was needed (`files` is already a JSON column). Divided by 104 MB/s, the slow end
  of the measured band.
- **`warm_model` and "model switches: N".** What decision 3 says to show rather than hide. The
  gateway takes the model an answer came back as from the usage row and counts the changes. Both
  are facts of THIS CP process (like the demand window), and the panel says so.
- **`warmPath` in the engine table.** The declaration that makes "are there weights in memory" a
  different question from "is it healthy". Not derived from the provider name (ADR 0053): a second
  engine speaking the same API would otherwise inherit a probe nobody chose for it.

## P4 measurements (2026-09-09, while implementing)

Ingest from the Console (decision 6), gating and licence acceptance (decision 10), and
`MODE=delete` (decision 7). This splits in two: **the upstream measuring that changed the design
and what the implementation turned up** (below), and **what pressing the buttons on hardware
produced** (the next section). The second list is the longer one.

1. 🔴 **A gated repository publishes its metadata anonymously.** Checked on FLUX.1-dev and
   SD 3.5 Medium: `api/models/<repo>?blobs=true` returned `gated: "auto"`, `license: "other"`,
   the `license_name` (`flux-1-dev-non-commercial-license` / `stabilityai-ai-community`) and
   **the sha256 and size of all 29 files** with no token — and only
   `resolve/main/<file>` answered 401. That decided the shape: **the Control Plane does not hold
   the Hugging Face token.** The CP resolves, the ingest task (which has the token) fetches, and
   decision 6's "the token never lands on a box" now covers the CP as well.
2. **Civitai's API is alive** (open question 4). `api/v1/model-versions/128713` answered
   anonymously with `files[].hashes.SHA256` (upper case), `sizeKB` (**fractional kilobytes** —
   multiply by 1024), `downloadUrl`, `baseModel` and `model.type`, and the download 302'd to a
   signed R2 URL that needed no key. There is no licence field of the kind Hugging Face has, so
   the catalogue says "see the model page" — **a guessed licence name must not sit next to real
   ones**.
3. **`commercial_use` is read off the licence name** (decision 10): `no` for anything containing
   `non-commercial` / `-nc`, `yes` for Apache-2.0 / MIT / OpenRAIL++ / CreativeML OpenRAIL-M,
   `unknown` otherwise. **`unknown` is a real answer** — cheaper than a list of every licence in
   the world, and far cheaper than a wrong `yes`.
4. 🔴 **A semicolon in a migration comment stopped the Control Plane booting — again.** The
   runner splits the file on semicolons with no SQL parser, so one inside a comment cut a
   `CREATE TABLE` in half and every test failed with `incomplete input`. It is a known trap, and
   it was walked into **inside the warning written to explain it** (which contained a literal
   `;`). The file now says, in words, that no comment in it may contain one.
5. **A job is a row, and it carries the catalogue row it will create.** A download runs for
   minutes (Hugging Face: 4–236 MB/s) and a CP can be replaced inside one. The first cut kept
   the pending row in a map in the process, which leaves "the bytes are in the bucket and the
   row is nobody's to create". The job row now holds the spec as JSON, and a test pins that a
   DIFFERENT process finishes the job and writes the row.
6. **A failure reports the task's own words.** `DescribeTasks` says "fetch exited 1". Reading the
   log (a `logs:GetLogEvents` grant scoped to this stack's group) says
   `ingest: sha256 mismatch: got … want …`, and that is what the panel shows — "the file changed
   upstream" and "this deployment has no token" need completely different things from the reader.
7. **Deleting the bytes is still the ingest task's job** (decision 7). The CP has no
   `s3:DeleteObject` and gains none; `DELETE …/models/{id}?purge=1` forgets the row and then
   starts the task in `MODE=delete`. The row is READ before it is deleted — the keys exist
   nowhere else, and doing it the other way round reports success and deletes nothing.

### What P4 needed that the decisions did not name

- **Resolve and start are two API calls.** An "I accept" offered before the licence and the
  gating are on screen is not an acceptance. `POST …/ingest/resolve` starts nothing and answers
  the licence, the size, the sha256, the gating and `can_ingest`; the panel draws that first and
  only then offers the checkbox.
- **`engine_ingest_jobs`** (sqlite `0058` / pg `0043`), and `license_accepted_by` /
  `license_accepted_at` / `commercial_use` on `engine_models`. An acceptance is the record of a
  HUMAN act, and it cannot be reconstructed from the model card afterwards.

## P4 on hardware (2026-09-09, the dev deployment)

The Console buttons were pressed by a person and corroborated from ECS, S3 and both log groups
(the admin API needs a super_admin browser session and cannot be driven with AWS credentials).
**All three halves of the definition of done passed** — one ungated ingest, the gated refusal,
and a gated ingest. And 🔴 **six defects appeared that only pressing could find, one of which
had killed an entire path.**

1. **One ungated model (`qwen2.5-coder-0.5b`, 491,400,064 bytes). Button to row: 72 seconds.**
   `POST …/ingest` answered in 1.198 s having called RunTask, +16 s to pull start, 6.7 s to
   pull, **13 s** from Hugging Face (37.8 MB/s), 2.1 s to verify the sha256 (234 MB/s), **2 s**
   to S3 (245 MB/s), and +26 s for the CP to notice (a 10-second poll plus the ECS
   reconciliation). The S3 object was **the declared size to the byte** and the sha256 matched
   what the upstream API was independently asked for. The row arrived **disabled**.
2. **The gated refusal starts nothing.** On a `hasToken:false` deployment, looking up FLUX.1-dev
   answered `can_ingest:false` with the non-commercial warning and "no token here", and the
   acceptance checkbox could not be ticked. **Zero tasks existed in the ingest family, RUNNING
   or STOPPED.** The same observation settles "looking it up starts nothing".
3. 🔴 **A gated repository reading anonymously was confirmed on the deployed CP too**
   (corroborating measurement 1). A CP holding no token answered
   `POST …/image/ingest/resolve` **200 in 186 ms**; the ungated resolve took 264 ms. The
   premise holds in a deployment, not just on paper.
4. **A gated model really was taken in (FLUX.1-dev, 23,802,932,552 bytes). Button to row:
   15 minutes 51 seconds.** 7.4 s to pull, **536 s** from Hugging Face (44.4 MB/s, with the
   token), 179 s to verify the sha256 (133 MB/s), **178 s** to S3 (134 MB/s), +2 s for the CP.
   `fetch` and `upload` both exited 0.
5. **The ingest task runs on Fargate (2 vCPU / 4 GB) and starts no GPU box.** `launchType:
   FARGATE`, no capacity provider: taking in 23.8 GB buys nothing at $1.26/hour. Decision 6's
   "ingest is out of the start path" is directly observable. `IngestDiskGiB=80` was enough.
6. 🔴 **`HfTokenSecretArn` alone does not work — the task never starts. The parameter and the
   documentation were both there; the IAM that makes them work was not.** ECS resolves a
   container's `Secrets` **before the container exists**, so it does so as the **execution
   role, not the task role** — and 20-platform scopes that role's
   `secretsmanager:GetSecretValue` to `secret:rds!*` (the database password), while the attached
   `AmazonECSTaskExecutionRolePolicy` contains no Secrets Manager at all. Passing an ARN would
   have died with `ResourceInitializationError` — **not a 401 on the download, and nothing in
   the ingest log**, because no container ever runs. `ExecHfTokenPolicy` was added to
   60-engines: created only when an ARN is given, scoped to that one ARN, attached to the
   imported execution role by name the way `CpIngestPolicy` attaches to the CP's. It stays
   inside decision 6's "only permissions that affect this stack's own resources". **Finding it
   before pressing was luck.**
7. 🔴 **A finished ingest does not put its row on the list.** The job says "done" and the model
   list goes on showing what it had. Nothing re-reads it: the job poll runs only while a job is
   `running`/`pending` and reads only jobs, and the engine poll runs only for
   `starting`/`stopping` or `ondemand`+`running` — **an on-demand engine parked at "stopped"
   matches neither**. The row an ingest creates is disabled by design, so this is exactly the
   row an administrator came to switch on, and it lands on P4's definition of done.
8. 🔴 **An ingest failure reaches a Japanese screen in English.** A filename typed one letter
   short answered `the repository does not list flux1-dev.safetensor` — the CP's developer
   message, verbatim. `errText` looks up `err.<code>` and falls back to the message, and **none
   of the 15 codes the ingest can raise had a translation**; worse, the existing guard
   (`TestCPEmittedErrCodesHaveConsoleCatalogEntry`) **reads only the constants in
   `errcodes.go`**, so codes written as string literals in `engine_ingest.go` had **never been
   checked**. They were promoted to constants, translated, and the panel moved from `errText` to
   `errDetail` — which file is wrong lives in the message alone, so translating without it
   replaces the reason with a generality.
9. 🔴 **A free-text filename makes a typo indistinguishable from a refusal.** The letter above
   is correctly a 404 and looks exactly like a file that is not there. The Hugging Face answer
   already carries every file's name, size and sha256, and **the CP was receiving and
   discarding it**. `POST …/ingest/files` turned it into a picker. 🔴 **The first filter was too
   loose: FLUX.1-dev offered nine files, five of them `…-00001-of-00003.safetensors` shards** —
   one shard is not a model, so the filter written to avoid dead ends was showing them. Dropping
   shards and putting the repository's top level first leaves four.
10. 🔴 **Ingesting onto an existing id silently replaces that row.** `PutEngineModel` is an
    upsert on `(role, id)` — right for the seed and for registering a staged file, and here it
    would replace a working row's files, licence and sha256 and set `enabled=false`. Minutes
    after a button press the engine loses the checkpoint it starts with, **with nothing linking
    the two events**. It is now refused with a 409 before RunTask.
11. 🔴 **There was no way to delete an ingested file.** Decision 7's `?purge=1` was fully
    implemented in the CP (read the row first, start `MODE=delete`, audit it) and **the word
    `purge` appeared nowhere in the Console**. On hardware, "forget" removed the row, the 491 MB
    object stayed in the bucket, and no `MODE=delete` task ran. Server-side with no way in. It
    is now a two-step confirmation where deleting the bytes is ticked deliberately.

### What hardware needed that the decisions did not name

- **`POST …/ingest/files`** (the candidates). From the same single read the resolve uses, it
  returns only the files this engine could load that carry a sha256. Shards and files with no
  LFS pointer are left out — offering either is offering a dead end the resolve refuses a
  moment later.
- **The window read off the model, and the output cap as a fraction.** Hugging Face parses the
  GGUF header itself and publishes `gguf.context_length` (checked against four repositories from
  three publishers). 🔴 **It is the architecture's ceiling, not the window this deployment can
  run** — the 30B declares 262144 and does not fit an L4, so it runs at 32768. It is offered as
  "the model's maximum is N" and only into an empty field. The output cap is published nowhere
  (it is a deployment's policy, not a property of the model), so it is a choice among fractions
  of the window (1/4, 1/8, 1/16) — an eighth of 32,768 is the 4,096 already in use.
- **`engine_models.source`** (migration `0060` / pg `0045`). The row kept a snapshot of the
  licence but not the fact the licence is a property OF: which repository it came from. An id
  only has to be unique within one role in one deployment and is what a member reads in the
  launch menu, so it stays short — **the same name from another vendor is refused with a 409,
  and the provenance lives on the row**.
- **A two-step delete.** Forgetting the row and deleting the file are different acts, and the
  first alone leaves bytes nothing can reach that keep being paid for (measured: a 491 MB file
  outlived its row). The safe option is the default and is reset every time it opens.

## P5 implementation — registering the Hugging Face token from the Console (2026-09-09)

Open question 12's decision (DB as the record of truth, Secrets Manager as transport), built in
the **shape (b)** the review recommended — the stack creates the secret **always**. Nothing else
in P5 was touched.

1. **The entry condition (template headroom) was paid first.** The condition existed because
   adding one resource did not fit under the 51,200-byte wall. It was paid by **deleting the
   `HfTokenSecretArn` parameter itself** (~370 bytes with its description) and moving the stack
   `Description` prose into `PARAMETERS-60-engines.md`, "What this stack is". Result:
   **50,869 bytes**, 331 to spare. As before, moving prose into a `#` comment saves nothing.
   🔴 **A deleted parameter can stop an existing deployment**: `cloudformation deploy` refuses
   a `--parameter-overrides` key the template does not declare, and `params/60-engines` is a
   snapshot of the stack as it was, so the line survives. `env.sh` gained `af_param_drop` and
   stand-up drops it just before deploying 60-engines (`update.sh` passes no parameters there,
   so it was never affected).

2. **The secret is always there.** `HfTokenSecret` is created unconditionally holding the
   sentinel `-`; `Secrets: [{ Name: HF_TOKEN, ValueFrom: !Ref HfTokenSecret }]` lost its `!If`,
   and the fetch shell reads `[ "$HF_TOKEN" != - ]` as "no token". A task definition is
   CloudFormation's and static, so **a `Secrets` block that appears with the token would put the
   round trip back** — which was the whole complaint. The secret is unnamed: a named one cannot
   be re-created while the deleted one sits in its recovery window.

3. **"Never reads back" is enforced by IAM.** `CpIngestPolicy` gains
   `secretsmanager:PutSecretValue` on **that one ARN** and never `GetSecretValue`.
   `ExecHfTokenPolicy` (ECS resolves `Secrets` with the **execution** role, which 20-platform
   scopes to `secret:rds!*` — without this the task dies at startup) stopped being conditional.

4. **The record of truth is the DB.** Four settings rows (`engine_hf_token` = the sealed value,
   `_key_ref`, `_by`, `_at`), sealed with the existing `custodian`, exactly like a tenant's IdP
   client secret. The key ref is a fixed `deployment` rather than a tenant id — this value
   outlives any tenant. **One token per deployment** (decision 6 unchanged: per-tenant tokens
   would let tenant A's acceptance stage a model tenant B then uses).

5. **The secret is written first, the DB second.** The other order gives a deployment whose
   `PutSecretValue` keeps failing **a panel that says "registered", every ingest going out
   anonymous, and a 401 that talks about the licence**. The opposite failure (secret written,
   DB save failed) is overwritten by the next registration, and nothing reads that secret except
   an ingest this CP starts.

6. **It is written again before every ingest.** Not "when it looks stale" — **nothing can look
   stale** without `GetSecretValue`, and a re-created stack holds the sentinel while the DB
   holds a token. The only symptom would be a 401, so `start` stages every time.

7. **The gated verdict moved from the stack's declaration to a DB fact.** The engine table's
   `ingest.hasToken` is replaced by `ingest.tokenSecret` (the ARN). The CP is upgraded before
   the stack, so **an old table (no `tokenSecret`, `hasToken` set) keeps working**: nothing can
   be registered, gated still resolves, and the panel says which of the two it is
   (`stack_token`).

8. **The panel never holds the value.** The input is write-only (`type="password"`) and the
   status is "registered, by whom, when". There is **no current value that could be shown** —
   the CP cannot read it — so a field that looked pre-filled would be a claim the deployment
   cannot back.

**Tests**: 7 in Go, 3 in the DOM. Five regressions were actually introduced to confirm they are
caught — swapping the write order (a registration survives a refused secret), a `clear` that
skips the sentinel (removed on screen, alive in the path the task reads), a `start` that does
not stage (401 on a re-created stack), not clearing the field after saving (the token stays on
screen), and offering the field when `available: false` (a button that does nothing on an old
stack).

**Not verified on hardware.** Three things are worth pressing: that `PutSecretValue` really
passes (the policy's `Roles:` depends on carving the role name out of an imported ARN), that an
ingest with the `-` sentinel succeeds **as anonymous**, and that a gated repository (SD 3.5
Medium, FLUX.1-dev) is taken in without a 401 once a token is registered. The third is also
open question 7's outstanding half — "measure the two gated defaults on a deployment that has
an `HF_TOKEN`".

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

## Open questions — measure 8 before P2 (**9, 10, 11 and 12 were settled on 2026-09-09**; 2 and 6 only when an sd-server deployment needs them)

1. ~~**The router's four points** (decision 3 depends on them)~~ **Resolved (review R1, on a
   CPU)**: `/health` ok while empty, autoload waits, `--models-max 1` evicts the idle LRU and
   loads, `usage` and `model` arrive through the router, the window is per model. ~~One check
   remains — what "idle LRU" does with a model that is generating~~ **also resolved (P1
   measurement 7, on a CPU): it waits.** The interrupted generation was delivered in full, and
   the waiting request moved on to its load 48.6 seconds later.
2. **sd-server's LoRA** (decision 5 depends on it): the cost of a changing LoRA set under
   `immediately`, the VRAM of `at_runtime`, whether `<sd_cpp_extra_args>` also works on
   `/v1/images/edits` (multipart), and what a `baseModel` mismatch does (silent breakage or a
   warning). The `<sd_cpp_extra_args>` path and the mismatch can be measured on the CPU SD1.5
   Q4.
3. **Syncing into a running box.** Can a newly enabled model be added to a running box
   **without waiting for the next start** — a resident sidecar re-syncing the active set, and
   does the router rescan `--models-dir` (or is there a reload endpoint)? If not, llm also
   swaps "at the next start" to begin with, and this is P4.
4. ~~**Civitai's API** (decision 6)~~ **Resolved (P4 measurement 2, 2026-09-09)**: the developer
   site is still 404, but `GET https://civitai.com/api/v1/model-versions/<id>` answers
   ANONYMOUSLY with `files[].hashes.SHA256` (upper-case hex), `files[].sizeKB` (**fractional
   kilobytes**), `files[].downloadUrl`, `baseModel` (a display name such as `"SD 1.5"`) and
   `model.type` (`Checkpoint` / `LORA`). The download 302s to a signed R2 URL and answered 200
   with no key for the public model tried — some models do need one.
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
9. ~~**Can the switch re-read (*Resolved* 5) be shortened? — two candidates, one GPU hour, before
   P2**~~ **(1) is settled (review 2026-09-09, section 1) — `useLocalStorage` took the swap from
   276-282 s to 98.5 s and is now the default. (2) (solve it with RAM: raise `ImageMemMinMiB`
   so a g6.2xlarge is chosen) is still unmeasured, but with (1) working it is no longer a
   premise that could change P2's definition of done.** What follows is the original text:
   (review, answer 7 and R9). (1) `useLocalStorage` (ADR 0071 open question 1): the
   capacity provider's `LocalStorageConfiguration` is not create-only, so `ImageUseLocalStorage=true`
   lands in one stack update (R9). It pays not only on the switch (12.3 GB at 92 MB/s → NVMe)
   but on **the cold start's 33 GB S3 → EBS at 332–360 s**, a number pinned to EBS's write
   ceiling (125 MB/s baseline on a g6.xlarge). (2) **Solve it with RAM**: the three models
   (27 GB) leave the page cache because the box has 15 GB; `ImageMemMinMiB=30000` selects a
   g6.2xlarge (32 GiB) with no code change. If a switch becomes RAM → VRAM (seconds),
   `useLocalStorage` is back to being a start-time question. `bench-image-engine.sh` measures
   both as they are, and since the answer changes P2's definition of done (the price of a
   switch), it is measured **before P2**.
10. ~~**Re-examine where the models live at all**~~ **Settled (review 2026-09-09, sections 1-3)**:
    **(a) adopted** (`useLocalStorage` — cold start 527-586 s → 275 s, swap 276-282 s → 98.5 s,
    now the default), **(b) disproven** (a warm box cannot be built on MI at all: Bottlerocket's
    read-only root means a named `SourcePath` can only land on 3.1 GB), **(c) rejection upheld**,
    but on verified unit prices instead of an assumption about throughput — Elastic's $0.04/GB is
    $0.74 a cold start, 17-21× the GPU time it saves. What follows is the original text, whose
    premise **"S3 is not the bottleneck" was only half right** (removing EBS exposes the next
    wall at about 160 MB/s):
    (raised while verifying P4 on hardware; a separate session takes it.) The starting point is the measurement that the sync dominates every
    cold start — and **S3 is not the bottleneck**. ADR 0071's open question 1 already says the
    g6.xlarge's 125 MB/s EBS baseline binds both the S3 fetch (104–147 MB/s, **which looks like
    the EBS write ceiling**) and the VRAM load, and that an instance store frees both. Cheapest
    first:
    - **(a) `useLocalStorage`** (the same as open question 9(1)). **One parameter update**, and
      it removes the EBS write ceiling, so it pays on both the sync and the switch. **Measure
      this first.**
    - **(b) The warm box.** ADR 0071 decision 7(c) is **still unproven**, and the failure is
      written down (`PARAMETERS-60-engines.md`, "The model volume"): the anonymous host volume
      gives a fresh directory per task, so **a restart onto the very same instance MI had kept
      re-fetched all 18.5 GB** (126 s), and a named `SourcePath` lands on the root filesystem and
      dies with `No space left`. What blocks it is only "the path MI's data volume is actually
      mounted at" — **one GPU hour of investigation**.
    - **(c) Re-measure EFS (a candidate for lifting a 0071 rejection).** The rejection booked its
      own reconsideration: "reconsider if the sync turns out to hurt in P0". **P0 measured that
      it hurts.** On top of that, 🔴 the $0.36/GB-month figure carries the note "third-party
      transcription, needs re-checking" and is **unverified**, and the throughput argument ("NFS
      throughput, an order of magnitude slower") is **an assumption, never measured**. If the
      ceiling is the EBS *write*, EFS removes the write entirely and could be structurally
      better. Arithmetic (assuming $0.36 is right): the bucket measures 99.5 GB, so S3 $2.49/mo
      against EFS Standard $35.8/mo — a $33/mo delta, i.e. **26 GPU-hours**. If it removes 350 s
      from every cold start, that is $0.12 a start, so **more than ~9 starts a day pays for
      itself in GPU time alone** (never mind the person waiting). IA/Archive lifecycles lower the
      storage side further.
11. ~~**Where the tenant axis belongs**~~ **Decided (review 2026-09-09, section 4) — the middle
    option is adopted.** Measurement showed **the walls come in a different order than the text
    below says**: **time (~5 models) → disk (~13) → SSM (~38)**, so 4,096 characters is the last
    wall, not the first. What follows is the original text:
    The catalogue's key is
    `(role, id)` with no tenant, and "a tenant picks a model from Hugging Face and places it" is
    the right direction for usability — but **on a shared box four things multiply**: the active
    set is one per engine so it becomes the **union** of every tenant's enabled models (which is
    where the 4,096 characters finally bite; it is at 6% today), the cold start syncs *every*
    enabled model so it grows with the tenant count, `LlmModelsMax=1` means more swapping at
    1–2.5 minutes each, and one shared active set makes one tenant's model ids visible to
    another. A box per tenant removes all four at $1.26/hour per tenant, which discards the
    premise ADR 0071 was built on (one shared box, asleep). **The middle:** keep the catalogue
    deployment-wide and put the tenant axis on **who may ingest and who accepted the licence**.
    Nothing multiplies and most of the usability is won; `source` and `license_accepted_by` are
    already half of that shape.
    - **A bucket per tenant is not recommended.** A bucket is not the boundary that matters —
      whichever one they came from, the models land on **the same box's same disk and are read by
      the same process**. The boundary is the GPU box. It also duplicates shared models per
      tenant and loosens the engine task role's grant, which is scoped to one bucket ARN today.
      If isolation is wanted, **prefixes in one bucket** (IAM scopes by prefix, nothing is
      duplicated).
12. ~~**Let the Hugging Face token be registered from the Console**~~ **Decided (review
    2026-09-09, section 5) — "DB as the source of truth, Secrets Manager as transport" is adopted,
    and "never reads back" is enforced by IAM (`PutSecretValue` only, `GetSecretValue` never
    granted).** **Implemented (2026-09-09, "P5 implementation" — shape (b), the always-created
    secret; not verified on hardware).** The entry condition — **headroom in
    `60-engines.yaml`** — was paid by deleting one parameter and moving the stack `Description`
    prose into `PARAMETERS-60-engines.md` (50,842 → 50,869 bytes against the 51,200 wall).
    What follows is the original text:
    (same origin, separate session.) Today it is the `HfTokenSecretArn` CloudFormation parameter, so putting one in
    means **a CloudFormation run and a CP restart** — the very path that ran into measurement 6's
    IAM hole. "It is a secret, therefore Secrets Manager" is not an argument: this product keeps
    git OAuth tokens and MCP headers in its own encrypted store. The real constraint is
    **transport** — ECS can put a value in a container in exactly two ways, `secrets[].valueFrom`
    (**only a Secrets Manager or SSM ARN**) or `environment` (plaintext). 🔴 Measured:
    `DescribeTasks` hands the RunTask environment back **verbatim** to anyone holding
    `ecs:DescribeTasks`, so passing a long-lived PAT that way is not worth it. **The shape is
    "the DB is the source of truth and Secrets Manager is the transport"**: register it in the
    Console, and when an ingest starts the CP writes the value into the secret and references it
    by ARN. The price is that decision 6's "the CP does not hold the token" becomes "the CP can
    write it and never reads it back" — **a judgement that needs an ADR revision**. It should
    **not** be per-tenant: the ingest's result (the bucket and the row) is deployment-wide, so a
    tenant's personal acceptance would stage a model every other tenant uses, which is the very
    thing decision 10 warns about.

## Phases

- **P0 — the catalogue's foundation. Implemented and verified on hardware (see *P0 measurements*).** The first step is moving 3 KB of comments out of
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
- **P1 — llm in router mode. Implemented and verified on hardware (see *P1 measurements*).**
  Preset generation (no `--models-dir`), per-model windows, `LlmModelsMax`, the redefinition of
  `warm` (`warmPath` and `loaded` in `/models`), opencode's provider with per-model `limit`,
  syncing every enabled model with the panel's "sync +N s (estimate)", and `warm_model` with the
  switch count. **Definition of done: two `llamacpp/` models in the launch menu, each usable in
  turn, and the reload of a switch answered on the first attempt** (ADR 0071 P0's observation,
  taken across a switch). **All three were driven on hardware** (two GGUFs on one box, answering in
  0.4–0.7 s, 10 s and 276–282 s; and with the second row registered and enabled in the Console,
  the picker offered two models and a session started on the second). Only the row-creating step
  needs a person — it is a super_admin screen, which AWS credentials cannot drive.
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
- **P4 — ingest from the Console. Implemented and verified on hardware (see *P4 measurements*
  and *P4 on hardware*).** The `ingest` API
  (resolve and start as two calls, plus a job list), the RunTask IAM (inside `60-engines`;
  PassRole for the ingest role only, plus `logs:GetLogEvents` so a failure can say why), HF
  resolution of sha256 / `license` / `license_name` / size / gating, Civitai (open question 4
  turned out to be answerable, so it went in at the same time), the gating sentence and the
  licence-acceptance UI (who and when, the commercial axis; decision 10), progress and failure
  in the panel, and the ingest task's `MODE=delete` (decision 7). A job is a row
  (`engine_ingest_jobs`) carrying the catalogue row it will create, so a Control Plane replaced
  mid-download still ends with a row for the bytes that landed.
- **P5 — syncing into a running box (open question 3), virtual model ids for llm (the second
  half of decision 5), the ComfyUI pane, sd-server's async API (open question 6), the tenant
  axis (open question 11).**
  **Registering the Hugging Face token from the Console (open question 12) was implemented
  ahead of the rest** — it depends on nothing else here and it decides outright whether gated
  repositories (all of SD 3.5, all of FLUX.1) can be taken in at all. **Done when: a token is
  registered in the Console, a gated repository is taken in without CloudFormation being
  touched, and the ingest task's log carries no 401.** (Not verified on hardware — see "P5
  implementation".)

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

## Review (2026-09-09, open questions 10, 11 and 12)

The three the user raised while P4 was being pushed through on hardware — **where the models
live (10), the tenant axis (11), and registering the HF token (12)** — settled in a separate
session. 10 was measured on hardware; 11 and 12 were decided as design, without waking a box.
GPU spend: about 35 minutes, **$0.74**.

The premise this started from — "**S3 is not the bottleneck, the disk receiving it is**" — was
**only half right**. The EBS write ceiling was indeed binding, but taking it away put the **next
wall (S3 and the CLI, about 160 MB/s) right behind it**. What actually paid was not the fetch
but the read from disk **into VRAM**.

### 1. Open question 10(a) — `useLocalStorage` works. The default is now `true`

First, **`LocalStorageConfiguration` is confirmed not create-only** on the real API (backing up
R9's claim). **One update, about two minutes**, took the stack to `UPDATE_COMPLETE` and the
capacity provider to `storageConfiguration: null` / `localStorageConfiguration.useLocalStorage:
true`. No stack rebuild, no new capacity provider.

What was measured is **the deployment's own `llm` role** — not a bench box, but the very path
decision 3 put a price on. Same two models, same files, against the numbers already recorded for
the EBS setting:

| | EBS (recorded) | instance store | |
|---|---|---|---|
| S3 → disk, 1.1 GB | 8 s (140 MB/s) | **5 s (223 MB/s)** | 1.6× |
| S3 → disk, 18.5 GB | 159 s (117 MB/s) | **117 s (159 MB/s)** | 1.4× |
| disk → VRAM, 18.5 GB | 267 s | **91 s** | **2.9×** |
| RunTask → model loaded | 527-586 s | **275 s** | ~2× |
| swap to the 1.1 GB model | 10.0-10.1 s | **3.3 s** | 3.0× |
| swap back to the 18.5 GB model | 276-282 s | **98.5 s** | **2.8×** |

One point came off the `image` role too (the bench died for an unrelated reason, but its fetch
ran): SDXL, 6.94 GB in **29 s = 239 MB/s**, against the 92-100 MB/s band of measured point 7 —
2.4×.

🔴 **Read the swap, not the fetch.** Decision 3 priced "one model per box, swap on demand" at
**276-282 seconds**, and measured point 5 put the image role's switch at 1-2.5 minutes. That
price is now **98.5 seconds**. The swap is where a person waits; the cold start (275 s) is next.

Both costs are real and neither is new: an instance store is wiped with the box — but **MI
deletes the EBS data volume too**, so a cold start always paid a fresh S3 fetch. And `*StorageGiB`
stops meaning anything: the box gets whatever the instance type carries, which on g6.xlarge is a
**245 GB ext4 filesystem**, i.e. *more* than the EBS setting's 120 GiB. ⚠️ It follows that
`*AllowedInstanceTypes` may only name types that HAVE an instance store (every g6 and g5 size
does).

**Adopted**: the default of `LlmUseLocalStorage` / `ImageUseLocalStorage` is now `true`, and the
dev deployment is in that state. Reverting is one parameter.

### 2. Open question 10(b) — the warm box is not "unproven", it is DISPROVEN

Decision 7(c) of ADR 0071 sat at "unproven" for two sessions, and
`PARAMETERS-60-engines.md` called it "a GPU hour of investigation" blocked only on where MI's
data volume is mounted. **It took about four minutes, and the answer is that it cannot be done.**
The new tools are `harness/probe-warm-volume.sh` and the `mountinfo:` line the bench's fetch now
prints.

- **Why the anonymous form re-fetches** went from an observation to a mechanism. A container
  cannot see the host path of its own bind mount through `df`, but `/proc/self/mountinfo` can:
  `/._mnt_task/volumes/<TASK-ID>/volumes/models → /models  ext4 /dev/nvme1n1`. **The task id is
  in the path.** An anonymous volume is a fresh empty directory per task *by construction*; no
  setting changes it, and only a named volume could.
- **A named `SourcePath` does persist.** Two tasks in a row on the same instance mounting
  `/var/lib/af-warm-models`: run 1 MISS and fetched, run 2 **HIT**, same mtime. The half everyone
  assumed was hard works fine.
- 🔴 **But it lands on 3.1 GB.** That mount is `/dev/nvme0n1p8`, a small partition on the **root**
  volume — not the 245 GB `/dev/nvme1n1` where anonymous volumes live. The 1.1 GB probe object
  fit at 38% full; **an 18.5 GB model reproduces the recorded `No space left` exactly**. What
  this question kept mistaking for progress was **persistence without capacity**.
- **And the data volume has no nameable path.** The AMI is **Bottlerocket**: mounting the host
  root shows a **2.7 GB, 100%-full, read-only** dm-verity image, and **every** top-level
  directory a `SourcePath` could name — `/local`, `/mnt`, `/data`, `/opt`, `/var` — resolves
  inside that image rather than into the live host's mounts. `/._mnt_task` cannot even be created
  (`read-only file system`). **`useLocalStorage` does not change this**: it changes what the data
  volume *is*, not where a `SourcePath` may point.

**Consequence**: on Managed Instances a warm model volume **cannot be built out of host volumes
at all**. Keeping a box buys the image layers and nothing else, so `*ScaleInAfter: -1` is a way
to spend $1.26/hour on nothing. ADR 0071 decision 7(c) is closed as **disproven**, and
`PARAMETERS-60-engines.md` now says so.

### 3. Open question 10(c) — EFS stays rejected, but the GROUNDS are replaced

The rejection's own conditional ("reconsider if sync turns out to hurt in P0") had been met, so
it was reconsidered. **The unit price was checked first** — the number the ADR flagged 🔴 "a
third-party transcription, needs re-checking". AWS Pricing API, ap-northeast-1, 2026-09-09:

| | price | |
|---|---|---|
| EFS Standard (General Purpose) | **$0.36/GB-mo** | 🔴 **the ADR's number was right** |
| EFS One Zone | $0.192/GB-mo | |
| EFS IA / One Zone-IA | $0.0272 / $0.0145/GB-mo | reads and writes $0.012/GB |
| **EFS Elastic Throughput data access** | **read $0.04/GB, write $0.07/GB** | the line the ADR never saw |
| EFS Provisioned Throughput | $7.20/MiBps-mo | |
| S3 Standard | $0.025/GB-mo | matches the ADR's $2.49/mo |

**The stated reason for rejecting — "throughput is NFS's, an order of magnitude slower" — was
wrong**; EFS on Elastic Throughput is fast. The rejection nevertheless stands, on a different and
now *verified* reason. EFS has exactly three billing modes and **none of them work**:

- **Elastic**: one cold start reads 18.5 GB = **$0.74**. The GPU time it removes is at most
  100-125 s = **$0.035-0.044**. That is **17-21× underwater**, and volume does not help — the
  loss scales per start. **There is no break-even.**
- **Bursting** (the only mode where reads are free): baseline is 50 MiB/s per TiB stored, so at
  the measured 99.5 GB it is **about 5 MiB/s**. Burst is 100 MiB/s — **slower than the 125 MB/s
  EBS ceiling this whole exercise is escaping** — and reading 18.5 GB spends credit that takes
  about 63 minutes to earn back. A second cold start within the hour falls toward 5 MiB/s (over
  an hour for one model).
- **Provisioned**: buying 250 MiB/s to match NVMe costs **$1,800/month**.

Storage compounds it: 99.5 GB is **$35.8/mo** on EFS Standard against **$2.49/mo** on S3 (14.4×).
One Zone-IA would be $1.44/mo — cheaper than S3 — but its reads are $0.012/GB = **$0.22 a start**,
still 5-6× the GPU time saved.

**FSx for Lustre was priced too** (a better-shaped fit than EFS): persistent SSD runs
$0.188-0.848/GB-mo with minimum capacities, and Intelligent-Tiering charges $0.656/MBps-mo for
throughput. **That is HPC-cluster pricing, not the price of one sleeping GPU box.**

🔴 **And the decisive point: the very thing EFS was to fix — the 350-second sync — was mostly
fixed for $0 by `useLocalStorage`.** What is left to chase is about 100 seconds, at $0.74 a time.
**The rejection is upheld**, and the grounds in ADR 0071's rejected-options list change from "an
assumption about throughput" to "**verified unit prices**". The only axis on which EFS wins is
human waiting time, and a deployment that wants to buy it would pay about $200/month at nine
starts a day — **which is at least now a decision someone can make.**

### 4. Open question 11 — the tenant axis goes on ingest and acceptance (the ADR's own middle option)

The ADR's middle option — **the catalogue stays one per deployment; "who may ingest" and "who
accepted" carry the tenant axis** — is **adopted**. Beyond the ADR's four reasons, today's
measurements show **the walls come in a different order than the text says**.

The text named the 4,096-character SSM limit as the first wall ("this is where 4,096 characters
finally bites; 6% today"). Measured, **it is the third**. One model in an active set is about
**105 characters** (llm: 242 for two models; image: 216):

| wall | limit | bites |
|---|---|---|
| **time** (the sync is serial, measured 159 MB/s) | **~5 models** for a 10-minute cold start (95 GB) | **first** |
| **disk** (instance store, 245 GB) | **~13 models** (6 on the old 120 GiB EBS) | second |
| SSM (4,096 chars, 32-char envelope) | **~38 models** | last |

So a per-tenant catalogue pushes the cold start past ten minutes at **about five tenants even at
one model each**. Watching the character limit (38) is a 7× optimism. As long as every enabled
model is synced on every start, **there is no way out but to avoid the multiplication** — and
note that `useLocalStorage` widened the disk wall from 6 to 13 while **leaving the time wall
where it was** (only 1.4× faster).

Decided:

- `engine_models` keeps **`(role, id)`** as its primary key — one per deployment. No change.
- The tenant axis attaches to **(1) whether a tenant may start an ingest** (a permission) and
  **(2) who accepted the licence** — widening `license_accepted_by` to `(tenant_id, member_id,
  accepted_at, license)`. `source` already records provenance; half of this shape shipped in P4.
- 🔴 **Do not hide the cost**: because the catalogue is one per deployment, **every tenant can see
  every model id.** That is an accepted price and it belongs in the user guide — concealing it
  invites operations built on a privacy that is not there.
- **Per-tenant S3 buckets stay unrecommended** (the ADR's reasoning holds: the boundary is the
  GPU box, not the bucket). If isolation is needed, prefixes within the one bucket.

### 5. Open question 12 — DB as the source of truth, Secrets Manager as transport. **"Never reads back" enforced by IAM**

The ADR's recommendation is **adopted**, with three additions.

1. 🔴 **Make "the CP can write but never read back" an IAM fact, not a convention.** Grant the CP
   task role `secretsmanager:PutSecretValue` on **that one ARN only**, and **never**
   `GetSecretValue`. The property decision 6 gives up ("the CP does not hold the token") then
   survives as an **auditable boundary** rather than a promise. It goes in the `AWS::IAM::Policy`
   decision 6 already creates inside `60-engines` for `ecs:RunTask` and `iam:PassRole`, so no new
   resource — and it fits ADR 0071 decision 8's refinement ("only permissions that bite on that
   stack's own resources").
2. **Passing it in `environment` was never available.** As P4 measured, `DescribeTasks` returns
   RunTask environment variables in plaintext, so the transport is `secrets[].valueFrom` and
   nothing else. The CP cannot hand the token over as a per-ingest override.
3. 🔴 **There are two shapes and they cost differently.** Today the whole `Secrets` block
   disappears behind `!If` when `HfTokenSecretArn` is empty. Once the token is a DB fact,
   `hasToken` becomes one too — but **the task definition is CloudFormation's, and static**. So:
   - **(a) the small change**: the secret stays operator-created and the Console only **updates
     its value**. One `PutSecretValue` statement — but **a deployment that never set
     `HfTokenSecretArn` still makes one CloudFormation round trip**, so the user's complaint
     ("running CloudFormation just to enter a token") is only half answered.
   - **(b) the change that meets the request**: the stack **always** creates the secret with a
     sentinel value, so the `Secrets` block always exists, `hasToken` is purely a DB fact and
     **the round trip is gone**. The ingest script treats the sentinel as unset. The cost is **one
     new resource** in `60-engines`.
   - **(b) is recommended** — it is the only one that answers the request. ⚠️ But
     **`60-engines.yaml` had 29 bytes of headroom**. Shortening the `UseLocalStorage` descriptions
     in this session brought it back to **96**, which is still not a resource. **Moving prose to
     `PARAMETERS-60-engines.md` is the first task of that work**, and belongs in its estimate.
4. **Not a per-tenant token** (the ADR's reasoning holds): an ingest's results belong to the whole
   deployment, which is the same direction as open question 11's conclusion.

### The revision to decision 6

Decision 6's "**the token stays inside the ingest task; the CP does not hold it**" becomes:

> **The token's source of truth is the CP's DB (sealed with `custodian`, the same place as git
> OAuth and MCP headers), and Secrets Manager is the transport for a value ECS accepts no other
> way. The CP holds `PutSecretValue` on that secret and NOT `GetSecretValue`** — it cannot read
> back what it wrote, so decision 6's "the CP does not hold the token" is **weakened but not
> lost**. Only the ingest task reads the value, exactly as before.

P4's measured point 1 (gated metadata is readable anonymously) **stays live** — the CP will still
never need a token to resolve a model. Only the **registration path** changes.

### 6. Follow-up — mounting S3 directly (Mountpoint for Amazon S3, 2026-09-09)

EFS lost in 10(c) on **billing** ($0.04/GB under Elastic Throughput, $0.74 a cold start), not on
the **shape** of shared storage. S3 has no per-GB read charge at all and the gateway VPC endpoint
keeps the bytes off the NAT, so **the argument that killed EFS does not exist here**. So it was
measured. The harness is `harness/probe-s3-mount.sh`.

🔴 **Condition 1 holds.** On Bottlerocket under Managed Instances `/dev/fuse` is present, ECS
really does grant `linuxParameters.capabilities.add: [SYS_ADMIN]`, and `mount-s3 1.24.0`
**mounted** (`fuse mountpoint-s3 ro,...`). Everything else was moot if this failed, which is why
it was the first and cheapest thing tried.

**Measured — same box, same task, the cli pass first so a warm page cache cannot flatter the
mount:**

| | the 18.5 GB GGUF | |
|---|---|---|
| control: `aws s3 cp` (what the sidecar does today) | **88 s = 210 MB/s** | |
| **sequential read through Mountpoint** | **32.7 s = 567 MB/s** | **2.7×** |

The byte count is dd's own — 18,556,689,568, the **whole file** — so a short read is not being
mistaken for a fast one. Note the same `aws s3 cp` measured 158 MB/s in measurement 1 and 210 MB/s
here: it moves with the box and the hour, **which is exactly why the control lives inside the same
task**.

🔴 **Condition 2 turned out to be "you need a custom engine image", not merely "it costs
something".** Installing the RPM with `--nodeps` produced `libfuse.so.2: cannot open shared object
file` — **mount-s3 is not a dependency-free static binary; it links libfuse2**
(`fuse-libs-2.9.9`). A FUSE mount is invisible to other containers, so `mount-s3` has to run
inside the **engine** container, i.e. **llama.cpp / sd-server images would have to carry mount-s3
and libfuse2**. That is not a task-definition change, and it costs decision 1's "both roles start
through the same wrapper".

**Condition 3 is untested.** 567 MB/s is a `dd` sequential read, **not llama.cpp opening and
loading a GGUF through mmap**.

**Projection (not a measurement)**: of today's 275-second cold start the 117-second copy
disappears. But **the 91-second VRAM load is not purely disk-bound even on NVMe** — NVMe does
GB/s and 91 s works out to an effective 204 MB/s, so most of it is GGUF processing and the PCIe
transfer. Mounting therefore does not turn it into 33 s; expect the whole thing around
**150-175 s**. The 98.5-second swap should fall similarly.

**Condition 3 was measured the same day (`harness/probe-llm-mount-load.sh`), and the answer is
DO NOT ADOPT.** Same box, same task, with a copy-then-load pass as the control:

| | copy | load | total |
|---|---|---|---|
| A today's path (S3 → instance store → VRAM) | 114 s | **94 s** | **208 s** |
| B mounted, mmap (llama.cpp's default) | 0 | 150 s | **150 s** |
| C mounted, `--no-mmap` | 0 | **130 s** | **130 s** |

Three things fall out. **(1) llama.cpp only gets 123-142 MB/s through the mount** — nowhere near
`dd`'s 567 MB/s, because its load pattern does not exploit Mountpoint's parallelism. **Condition
3's worry was right.** **(2) mmap is expensive over FUSE** (150 s vs 130 s), so `--no-mmap` would
be mandatory. **(3) The cold start alone still wins**: the copy disappears, 208 s → 130 s.

🔴 **But the swap loses, and the swap matters more.** Today every enabled model sits on local
disk, so a swap re-reads locally — measured 98.5 s. Mounting removes the local copy, so **every
swap goes back to S3 at 130-150 s**. The trade is roughly 57 seconds off the cold start (275 s →
about 218 s once the image pull can no longer overlap a fetch that is gone) in exchange for
**making the swap 98.5 s → 130 s**. Decision 3 priced the swap, and the swap is what a person
waits for.

On top of that comes condition 2's cost: mount-s3 and libfuse2 baked into the engine image, and
the loss of decision 1's shared wrapper. **It does not pay.** `useLocalStorage` gave 527-586 s →
275 s for $0; this asks for a custom engine image, returns about 20%, and degrades the swap.
**Not adopted** — recorded as numbers rather than as a verdict, because a role that loads once
and never swaps would get a different answer.

Incidentally, **g6.xlarge ran out in both ap-northeast-1 AZs** during these runs, and this
deployment's `LlmAllowedInstanceTypes` had narrowed to the single type, so no box could launch.
Restoring the template default (`g6.xlarge,g5.xlarge`) fixed it — a re-run of exactly what ADR
0071 recorded under "the box will not launch because the candidate list was one type".

### Suggested status line

Mark open question **10 as settled** ((a) adopted, (b) disproven, (c) rejection upheld) and **11
and 12 as decided**. Implementation of 11 and 12 is P5, and **12's entry condition is "free up a
resource's worth of headroom in `60-engines.yaml`"**. Open question 9(1) is the same thing as
10(a) and closes with it.

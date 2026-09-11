# 0072. A model catalogue for the engines — swap the LLM and SDXL models without a CloudFormation run, and pick LoRAs per request

English | [日本語](0072-engine-model-catalog.ja.md)

- Status: **P0, P1 and P4 implemented and verified on hardware (2026-09-08..09). Of P5, only
  registering the Hugging Face token from the Console (open question 12) is implemented, and it
  was **verified on hardware on 2026-09-10** ("P5 implementation", "P5 on hardware": all three
  things worth pressing passed, and one gap surfaced — a gated repository does not only refuse
  with 401, it refuses with 403, and the two mean different things). P2 (ComfyUI) is implemented
  and CLOSED on hardware as of 2026-09-10, provider included ("P2 implementation", "P2 on
  hardware"). Four gaps surfaced only on the deployment, all of them green in CI and green on
  the bench: torch 2.5.1 cannot run ComfyUI v0.34.0 at all (fixed by moving to 2.9.1);
  `ln -sfn` linked INTO the models directory the clone bakes, so every request 400'd; the fetch
  sidecar used `PRESET_FILE` as a proxy for "is a router" and never staged any model but the
  starting one; and nothing anywhere could write a checkpoint family in the spelling the
  provider dispatches on, so ComfyUI was unusable from the Console alone. The ingest route and
  the three families that had never gone through the provider (Z-Image, FLUX.1, SD3.5) were
  pushed through on hardware the same day ("P2's remaining work 4 and 5, on hardware"): ALL FIVE
  families now return an image through the provider, but SD3.5 could not produce one at all
  until its template was fixed (`--clip_g` was missing from the file vocabulary).
  Six further gaps (5 to 10) are recorded there. **P6 (retiring the seed and six parameters)
  is implemented as of 2026-09-10 and was verified on hardware the same day** ("P6
  implementation", "P6 on hardware"). The definition of done was met — an empty catalogue does
  not bring the engine up, and one registered row brings it up on that model alone, warm in 819
  seconds. **The migration trap was armed on the dev deployment** (both `<Role>Enabled` empty),
  so the translation was paid by riding it on the new template's own update. Two gaps surfaced
  there: a "harmless pre-update" of `<Role>Enabled=true` alone is refused by CloudFormation as an
  empty change set, and **the admin API cannot read a catalogue row back** (no `s3Key` in the
  GET).
  P3 and the rest of P5 are not started.**
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
  Z-Image-Turbo and SDXL all draw on one instance, a warm picture takes 4–11 seconds, and **a switch
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
  left the sidecar doing nothing** (measurement 2), **nothing stopped an instance that reaches RUNNING
  and never warms** (4), and **the straightforward active set did not fit 4,096 characters at 20
  models and 20 LoRAs** (1). Two things P0 needed that the decisions did not name — a way to
  create a catalogue row, and a harness for seeing a picture — are at the end of that section.
- The same day, **P1 (the llm role's router mode) was implemented and measured, on a CPU and on
  hardware** (the "P1 measurements" section): preset generation, syncing every enabled model,
  `LlmModelsMax`, the redefinition of `warm`, and per-model windows. **All three definitions of done were driven on
  hardware** (two GGUFs usable on one instance, the swap answering on one attempt, and — once the
  second row was registered and enabled in the Console — two models in the launch menu with a
  session started on the second). Only creating the row needs a person: it is a super_admin
  screen, the same wall P0 hit. 🔴 Three points of the
  text were corrected by measurement: **`--models-dir` is not used** (it would list names the
  catalogue does not hold and count `llm/loras/` as a model), **`-c` must leave `LlmExtraArgs`**
  (a command-line flag beats the preset, so one `-c` gives every model the same window), and
  **warm is "any model loaded", not "the default model loaded"** (an instance that swapped models would
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

ADR 0071 shipped the foundation — inference on the fleet's own instances — in P0 and P1. The `llm`
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
     deployment would otherwise keep buying a `sleep infinity` instance at $1.26/hour. The panel shows
     "no enabled model" in place of the toggle. What disappears is the second pass; **the one
     GPU instance bought for stabilisation, ten minutes, stays** (the placeholder carries the `GPU`
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
   - **The active set — what the instance loads now — is written by the CP to SSM**:
     `/af-ws/engines/<key>/active`, a JSON of the enabled models' and LoRAs' S3 keys plus the
     material for the preset. The CP task role **already** has `ssm:PutParameter` on `/af-ws/*`
     (`20-platform`, `SsmWorkspaceParams`), so the CP side adds no IAM; the instance side adds
     `ssm:GetParameter` (that path only) to `EngineTaskRole` **inside `60-engines`** (ADR 0071
     decision 8: new IAM stays closed inside the stack). The fetch sidecar reads it, syncs,
     builds the preset and the command line. SSM rather than S3 writes from the CP (a bucket
     policy) because it is **a permission the CP already holds** (confirmed, R2(a)), the value is
     small, and the instance does not need a live CP at the moment it reads. 🔴 **The Standard tier
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
     catalogue's `vramMiB` sum fits the instance is the operator's call; the CP only helps with the
     addition).
   - **`warm` changes meaning.** Today `/health` ok = weights in VRAM. Under the router, warm is
     "`GET /models` shows **at least one model as `loaded`**", and the health path stays
     `/health`. The default is the one catalogue entry flagged `default`, written into the preset
     as `load-on-startup = true`. Without it the first request pays "instance start 527 s + load
     267 s".
     - 🔴 **Not "the DEFAULT model is loaded"** (P1 measurement 4). Under `--models-max 1` an instance
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
   - Changed **while stopped** — the usual state, a $1.26/hour instance sleeps — the next request wakes
     the new checkpoint. **No extra wait**: the instance fetches from S3 at every start anyway (ADR
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
   - **image.** The instance syncs the **enabled** entries of `image/loras/` and starts with
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
       **the instance**: only enabled LoRAs are synced, so a name outside the enum fails at the engine,
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
     the sha256 check was done by `fetch`, and that is enough. The manifest is **for the instance**. The
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

   > ✅ **Addendum (2026-09-11) — a row can be read back.** The admin row said what a model IS
   > and not how it was DECLARED: `files` was base names, and the S3 keys, the flags, the sizes
   > and `args` were nowhere on the wire. With P6 making the catalogue the **only** declaration
   > there is, that means a forgotten row could be rebuilt only from a copy whoever deleted it
   > happened to keep — walked into once on the real deployment, where it worked because the row
   > was a single-file GGUF; FLUX.1's four keys and four flags would not have survived it.
   > Unlike P5's token, **being unreadable was not the design here**: the CP holds the values.
   > The row now carries `file_rows` (`{s3Key, flag, bytes}`) and `args` **in the shape
   > `POST …/models` reads**, so the JSON that was read posts straight back and rebuilds the same
   > declaration. The round trip is pinned by a test (positive controls: drop `file_rows` and the
   > re-registration is refused with a 400; drop only the flags and four unlabelled files come
   > back). `files` (base names) stays as it is because the Console reads it, and the new fields
   > are on the super-admin row alone — the Agent's catalogue is built by
   > `engineCatalogModelRow` and carries no S3 key. Three things deliberately do NOT survive the
   > round trip: `enabled` / `selected` (a re-registration is always disabled), the licence
   > acceptance (a record of a human act, not a field to copy) and `created_at`.
   > **The Console shows the keys in exactly one place** — the "forget this row" confirmation.
   > On the row's meta line they would add four lines to a split model for a value nobody reads
   > while choosing between rows; in the confirmation they are the **last moment anyone can read
   > what this row was**, and with purge ticked they are also the list the delete task is handed.

8. **Provenance and usage carry the model and the LoRAs.** `generate_image`'s result has `model`
   = the checkpoint id and `provenance` with `loras: [{name, weight}]` and `sha256` (from the
   manifest; ADR 0071 decision 10's "file name and sha256"). The llm usage row's `model` is what
   the router returns in the response (= the catalogue id). `engine_hourly` (ADR 0071 decision
   13) is untouched — uptime is a property of the instance, not of the model.

9. **The effect on the cold start is stated in numbers, and what is synced differs by role.**
   S3 → EBS is a steady 104–147 MB/s (ADR 0071 measurement 8; P1 measurement 9 adds 116.7 and
   139.7 MB/s), so the llm sync time is **the sum of the enabled GGUFs' sizes** — 179 seconds per
   18.5 GB.
   - **The llm role (a router) syncs every enabled model.** It is expected to answer for any of
     them, and a model whose file is missing does not wait — it fails with 500 (P1 measurement 6).
     So on this role enabling a model literally means "the next start takes N seconds longer".
   - **The image role (sd-server) syncs only the selected checkpoint.** sd-server holds one, so
     there is no reason to put somebody else's checkpoint into every cold start. (comfy is a
     router with no preset and breaks this — see the `SYNC_ALL` correction under "P2
     implementation".)
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
    - **With no words it is a ranking.** An empty `q` answers "the top of what this role can
      load" — for somebody who does not know a name that is the only way in, and requiring `q`
      rebuilds "only for those who already know" in a different shape. Three orders —
      **downloads, trending, likes** — **mapped per upstream, never passed through**: Civitai
      answers 400 to a sort it does not know (measured) and Hugging Face ignores one silently,
      which is worse — **a list that looks ranked and is not**. HF takes `downloads` /
      `trendingScore` / `likes`; Civitai takes `Most Downloaded`, `Most Downloaded` +
      `period=Month` (it has no trending score, so "this month" is what trending means there)
      and `Highest Rated`.
    - 🔴 **There is no "newest".** Measured 2026-09-09: `sort=lastModified` and `sort=createdAt`
      over `filter=gguf` return nothing but bulk automated re-quantisations
      (`mradermacher/*-i1-GGUF`), every one at 0 downloads and 0 likes. A ranking whose first
      screen is always the same uploader's robot is not a way in, and "trending" already answers
      what somebody reaching for "new" wants.
    - **All three numbers ride on every row, whichever order was used.** Showing only the one
      that was sorted on leaves "why is this here" unanswerable, and "everybody uses it" is not
      the same answer as "people are looking at it this week". Civitai publishes no trending
      score, so that field stays **empty rather than borrowing** another number.
    - 🔴 **Browsing requires no engine.** A deployment that has not adopted 60-engines, and one
      that has switched its engine off, can both still LOOK. The read needs no token, no bucket
      and no task (decision 6) — the only thing an engine is needed for is a role to stage
      INTO. So `POST /api/admin/engines/search` (with `kind` stated, since there is no engine to
      derive it from) sits beside the per-engine route, and the panel shows it when there are
      zero engines. **"There is nothing here" is the worst possible answer to "what could I
      run?"**, and the administrator deciding whether to stand the stack up at all was exactly
      the person who could not see the catalogue. Taking one in is the part that does not work,
      so the panel says so (a note, not a button). Civitai is offered here too, for checkpoints.
      - 🔴 **The real cause was where the routes were registered** (measured 2026-09-09 by
        running the CP in this container): the whole admin route set sat INSIDE
        `registerEngineRoutes`' `if reg == nil { return }`, so a deployment with no engine table
        answered 404 to `GET /api/admin/engines` — the panel could not even say "no engines
        here". The admin routes move outside that guard; every handler is nil-safe on the
        registry and answers "no such engine" by itself.
    - 🔴 **`trendingScore` comes back fractional.** Measured 2026-09-10 (`search=WAI`, the image
      role): two rows of twenty answered `0.1` and `0.7000000000000001`, an `int64` field made
      **the whole array fail to unmarshal**, and the panel showed only "unreadable answer from
      huggingface.co" — **a search that worked the day before, lost entirely depending on which
      rows came back**. It is a score, not a count, so it is taken as `float64`, and the panel
      rounds it (a raw `String(0.7000000000000001)` is what lands on the row otherwise). The
      lesson sits on the other side of the narrow decode's win: **a narrowed type that does not
      match the upstream's real range fails all-or-nothing**.
    - 🔴 **The narrow decode is worth 41x** (measured 2026-09-09, the same 20 rows): **211,015
      bytes** raw upstream against **5,125 bytes** out of this route. Dropping `chat_template`
      and `extra_gated_prompt` is not a marginal saving.
    - **Search is not a precondition for ingest.** Typing `owner/name` or a URL stays. A
      deployment with closed egress loses search too, and there decision 6's hand-run route
      simply goes back to being the main one.
    - **A link back to the page, and the publication date** (added 2026-09-11). Every row
      opens its upstream page, and `published_at` (HF's `createdAt`, Civitai's version
      `publishedAt`) rides **as a pair** with `updated_at`: one alone cannot tell a model
      published a year ago and touched last week from one published last week. 🔴 **Display
      only — the order does not change**; "no newest ranking" above still holds. **The URL is
      composed by the CP** (`url`): the two upstreams spell a page differently, and Civitai's
      is `/models/<model id>?modelVersionId=<version id>` — it needs the MODEL id, while a
      row's `ref` is the version's, so the Console could not build it. A third source then
      costs one change in one place. `updated_at` is now **empty for Civitai**: the only date
      `/api/v1/models` answers is `publishedAt` (measured 2026-09-11), and serving that as
      "updated" was the same date under the wrong name.
      Measured on a real render (#496's harness, ja and en): **ja gains no wrapped line at
      all** (cards stay 118/118/104 px). What buys that is keeping the two dates in **one**
      flex item — as two, the busiest card came to 392 px against the strip's 390 and broke
      to a second line, growing the card 118 → 146 px. **en does grow** (145/145/131 px):
      "Published", "Updated" and " downloads" are wide enough that the pair cannot share the
      line. It moves down whole rather than splitting, so the reading survives.

## Resolved by measurement (2026-09-08, the dev deployment's g6.xlarge)

The harness is `deploy/aws/ecs/harness/bench-image-engine.sh` (+ `.py`). It runs **one task with
RunTask** on the image role's capacity provider — four containers: fetch → ComfyUI → the bench
client → upload to S3 — and never uses ECS Exec. ComfyUI is the community image
`ghcr.io/lecode-official/comfyui-docker:latest` (5.36 GB compressed, **v0.8.2** baked in), copied
to ECR, **checked out at v0.34.0 at start** (1–2 s) plus `pip install -r requirements.txt`
(19–24 s). The text encoder is fp8 (`qwen_3_4b_fp8_mixed`, 5.6 GB). The numbers are from the
last two phases (stock flags, `--highvram`) of four runs on the same instance shape; the pictures
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
   return visit cost the same: SDXL 56–63 s, klein 106–110 s, Z-Image 133–154 s. The instance's
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
10. **The instance**: a g6.xlarge registers **15,000 MiB** with ECS. **A second task on the same instance
    fails with `No space left`** (the anonymous host volume is never reclaimed — ADR 0071
    decision 7's note). A task placed on an instance that had just stopped one sat **9.5 minutes in
    PENDING** before its pull started (33 s on a fresh instance). Waiting for Managed Instances to
    reclaim the instance is faster.
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
   instance scans a directory, so it needs no name and no description) and a flagless file became a
   bare string. 4 KB is not headroom to be careful with; it is what decides the shape.

2. 🔴 **The sidecar came up doing nothing. A YAML FOLDED block (`>-`) does not fold a
   more-indented line.** The jq filter, indented to line up under its own `jq -r`, kept its
   newline, so the shell ran `jq -r --arg s "$START"` (which dumps the whole document) and then
   looked for a command called `[(.models[]?|…`. `/models/cmdline` stayed empty, the engine came
   up as the placeholder, **the service reached a steady state and nothing anywhere said why**.
   That is a GPU instance and ten minutes spent arriving at "no picture, no reason". Fixed by a
   literal block (`|-`). What stops it happening again is
   `deploy/local/engine-sidecar-test.sh`, which **pulls the script out of the template as it is
   actually deployed and runs it** against a stub `aws` and the real `jq`: shell inside a
   CloudFormation `Mappings` entry has no type check, no linter, and its only feedback is a GPU
   instance ten minutes later.

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

8. 🔴 **The G-family vCPU quota of 8 was hit.** Waking an instance straight after stopping one fails
   placement for minutes with `VcpuLimitExceeded: your current vCPU limit of 8`, because the
   draining instance still holds its 4 vCPU. It is exactly what `PARAMETERS-60-engines.md`'s "The
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
   instance that swapped models under `--models-max 1` would read as answering-but-not-warm, and 900
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
9. **The sidecar wrote the router's preset on the real instance**, and its log is the evidence:
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
   decision 6's "the token never lands on an instance" now covers the CP as well.
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
5. **The ingest task runs on Fargate (2 vCPU / 4 GB) and starts no GPU instance.** `launchType:
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

(**All three were pressed on hardware on 2026-09-10** — next section. All three passed.)

## P5 on hardware — registering the Hugging Face token from the Console (2026-09-10, the dev deployment)

The three things the previous section left "not verified on hardware", pressed **without waking
a GPU** (an ingest is a Fargate task; the image role stayed `mode: off` throughout and never
came up). **All three passed.** Times are UTC; seconds and byte counts are the ingest task's log
verbatim.

1. **`PutSecretValue` really passes on a deployment.** Registering from the admin panel makes
   `GET /api/admin/engines/hf-token` answer
   `{"available":true,"configured":true,"updated_by":"…","updated_at":"2026-09-10T13:50:14Z"}`,
   and the secret's `LastChangedDate` is the same 13:50:14Z. **The policy's `Roles:` — carving
   the role name out of an imported ARN — resolves on a real deployment.** The version list also
   shows item 6 (written again before every ingest) actually running:
   `list-secret-version-ids` returns four versions, and after the stack's sentinel and the
   13:50:14Z registration come **13:52:59Z and 13:53:05Z** — the exact moments the two
   `POST …/ingest` calls below were made. `stage` adds one version per ingest.

2. **An ingest under the `-` sentinel succeeds as anonymous.** `DELETE hf-token` answers
   `{"available":true,"configured":false}` and the secret goes back to the sentinel at
   14:02:56Z. With the deployment in that state, an ungated repository
   (`madebyollin/taesdxl`, `taesdxl_decoder.safetensors`, 4,895,612 B) is taken in and the job
   reaches `done` on three log lines:

   ```
   ingest: fetched 4895612 bytes in 2s
   ingest: sha256 ok f6013131e7eb412ef20113f1acc2ea7d3e47e53196ca0530fa65d9b61d814b61
   ingest: uploaded image/vae/r1-taesdxl-decoder.safetensors in 1s
   ```

   Not one line comes from Authorization. **The secret's `LastChangedDate` did not move for that
   ingest** (still 14:02:56Z), which is the measurement behind "with no token stored, `stage`
   writes nothing". Under the sentinel the gated verdict flips too: `resolve` answers
   `can_ingest:false` / `deployment_token:false`, and `POST …/ingest` refuses with
   400 `gated_no_token` **before a task is started**.

3. **A gated repository is taken in without a 401** (open question 7's outstanding half). One
   file from each of SD 3.5 Medium and FLUX.1-dev, and in both cases **the smallest one**:
   `vae/diffusion_pytorch_model.safetensors`, 167,666,902 B — smaller than FLUX.1-dev's
   `ae.safetensors` (335,304,388 B). Gating is decided per repository, so one file settles it —
   **the 23.8 GB `flux1-dev.safetensors` was not taken in, because it does not fit an L4.**

   ```
   13:53:50  ingest: fetched 167666902 bytes in 5s          # FLUX.1-dev
   13:53:51  ingest: sha256 ok f5b59a26851551b67ae1fe58d32e76486e1e812def4696a4bea97f16604d40a3
   13:53:53  ingest: uploaded image/vae/r1-flux1-dev-vae.safetensors in 1s
   14:00:27  ingest: fetched 167666902 bytes in 8s          # SD 3.5 Medium
   14:00:28  ingest: sha256 ok 8f53304a79335b55e13ec50f63e5157fee4deb2f30d5fae0654e2b2653c109dc
   14:00:29  ingest: uploaded image/vae/r1-sd35-medium-vae.safetensors in 1s
   ```

   Neither log holds a 401. **The anonymous resolve holds up on gated repositories too**: the CP
   read sha256, size, licence (`stabilityai-ai-community` / `flux-1-dev-non-commercial-license`)
   and `gated: true` from `?blobs=true` with no key, and only the task did the download — the
   division of labour decision 6 describes, confirmed on the real path.

🔴 **A gated repository does not only refuse with 401 — there is a 403, and it means something
else.** SD 3.5 Medium's first attempt failed, on this one log line:

```
curl: (22) The requested URL returned error: 403
```

FLUX.1-dev had gone through in the same minute on the same token, so **the token was arriving**.
403 is "authenticated, but no access to this repository" — the operator's account had not
accepted `stabilityai-ai-community` (a fine-grained token missing "read access to the contents
of public gated repos" looks identical). Accepting it and retrying the same file passed. This
ADR and `PARAMETERS-60-engines.md` both write 401 as the symptom of "no token", but **401 and
403 hand the reader different homework**: 401 means the deployment has no token (or a trailing
newline got in); 403 means that account has not accepted that repository. The panel shows the
line verbatim, so only the person reading it can tell the two apart.

> ✅ **Fixed (2026-09-10)**: telling them apart is no longer left to the reader. The job row
> names itself — 403 as the new `gated_not_accepted`, 401 as the existing `gated_no_token`,
> anything from Civitai as `civitai_login_required` — read by `engineIngestFailureCode` out of
> the task's own words (curl's `error: 403`, a verbose run's `HTTP/1.1 403`). The Console keeps
> the log line and puts the two different destinations beside it in ja and en (403 → the
> Hugging Face model page, 401 → the token field). The `resolve` side **cannot** carry a code:
> the CP resolves anonymously and has no way to ask whether the registered token's account
> accepted a given repository, so it goes as far as an up-front `gated_needs_acceptance`
> warning whenever a repository is gated and a token is registered. Both statuses are tested;
> with only one, an implementation with no branch passes.

**What was deleted and what was left.** The three rows the verification created
(`r1-flux1-vae-probe`, `r1-sd35-vae-probe2`, `r1-anon-probe`) were never enabled, and afterwards
**only the rows** were deleted — `?purge=1` was not used, because a purge silently deletes bytes
another row still points at when they share an S3 key. Three objects are still in the bucket:
`image/vae/r1-flux1-dev-vae.safetensors` and `image/vae/r1-sd35-medium-vae.safetensors`
(167,666,902 B each) and `image/vae/r1-taesdxl-decoder.safetensors` (4,895,612 B) — about
340 MB in total.

## P2 implementation — ComfyUI (2026-09-10)

Two days after open questions 8 and 9 were settled, the P2 list in the phases section was
implemented as written. It could be verified in CI; **verifying it on a GPU was still
outstanding** (the definition of done became the next hardware session's homework).

1. **Own Dockerfile and CI.** `deploy/aws/ecs/comfyui/Dockerfile` clones ComfyUI pinned at
   `v0.34.0` on top of `pytorch/pytorch:2.5.1-cuda12.4-cudnn9-runtime` (raised to
   `2.9.1-cuda12.8` in 6 below — on torch 2.5.1 `comfy-kitchen` dies on import) and does not
   install the Manager (0071 decision 6). Baking it is NOT folded into `dev-image.yml` /
   `release.sh` but given its own `workflow_dispatch` (`.github/workflows/comfyui-image.yml`) —
   pinning ComfyUI has nothing to do with the application's release cadence, and folding them
   together would re-bake the same content on every release.
   🔴 **`gh workflow run` cannot start a workflow that is not on the default branch** (found by
   trying to fire one that was not yet on develop). There is no Docker in this sandbox either,
   so the Dockerfile and the CI file were sent to develop as a small PR of their own first, and
   only then was the build confirmed — **it passed** (pushed under the tag `v0.34.0-test1`).
2. `20-platform.yaml` gains the ECR repository `af-comfyui` (R4's premise). `standup.sh`'s
   images stage reads `ImageEngine` and copies exactly one of `af-sdcpp` / `af-comfyui`.
3. `60-engines.yaml` gains `ImageEngine` (`sdcpp` / `comfy`, default `sdcpp`). The image role's
   container (renamed from `sd` to `engine`) switches its `Image`/`Command`, and the engine
   table its `health`/`provider`, through `!If` — no extra role, task definition or service
   (decision 4). 🔴 **The 51,200-byte wall** was met the same way as in decision 12 (moving
   duplicated long-form Description text and comments out to `PARAMETERS-60-engines.md`) to make
   room before adding anything (331 bytes free before the work, 1,094 after). `cfn-lint`,
   `ecs-lifecycle-stub-test.sh` and `engine-sidecar-test.sh` are green (and the byte overflow was
   deliberately triggered once to confirm it is caught).
   - The fetch sidecar **needed no change** — with an empty PRESET_FILE its path never
     distinguished roles and syncs the files of every enabled model ("start" first, "rest"
     after). Decision 9's prose saying "image syncs only the selected one" had already been
     contradicted by the 2026-09-09 change (adding "rest" in the background), so it was corrected
     in passing.
     🔴 **Correction (2026-09-10): it did need a change.** The line that writes "rest" sits
     **inside** `if [ -n "$PRESET_FILE" ]`, and the `else` empties `keys.rest`. The image role
     has `PRESET_FILE=""` and therefore always takes the `else`. That was correct while the only
     engines were llama.cpp (a router, with a preset) and sd.cpp (neither, and it holds one
     checkpoint), because "has a preset" implied "is a router" implied "wants other models too".
     **comfy is a router with no preset, and it breaks that equation.** Models other than the
     starting one never reach the instance, and ComfyUI answers
     `Value not in list: unet_name: 'flux-2-klein-4b.safetensors' not in []` — while the file is
     in the active set AND in S3, and the name in the graph is right. Worse, the sidecar printed
     `every enabled model is on this box`, which made the ingest log look healthy. Fixed with
     `SYNC_ALL` (item 4 of "P2 on hardware" below).
   - comfy's start command does not read the **contents** of `/models/cmdline` (there is no
     equivalent of `-m` — the checkpoint is chosen inside the graph JSON the provider builds per
     request). That the file is non-empty is used only as the "there is an enabled model" gate.
     The integration is the single line `ln -sfn /models/image /ComfyUI/models` — 0071 decision
     6's S3 layout is already ComfyUI's own convention, so that is all it takes.
     🔴 **Correction (2026-09-10): one line was not enough.** ComfyUI's repository **tracks
     `models/` as a real directory** (`checkpoints/` and friends exist, with `put_..._here`
     files), so the Dockerfile's `git clone` bakes it in. When the link name is a real directory,
     `ln -sfn` only creates `/ComfyUI/models/image` **inside** it, and `models/checkpoints/`
     stays the shipped empty placeholder (`-n` only affects a symlink *to* a directory). The
     engine starts, answers `/system_stats`, passes health, and 400s every request with
     `ckpt_name: 'sd_xl_base_1.0.safetensors' not in []`. `rm -rf /ComfyUI/models` has to come
     first — **never with a trailing slash** (from the second run on it is a symlink, and the
     slash would empty the shared model volume). The bench missed this because `--baked` bind
     mounts the volume **directly onto** `/ComfyUI/models`: a mount replaces a directory, a
     symlink does not.
4. **The `comfy` provider** (`workspace/agent/internal/imagegen/comfy.go`): `/prompt` (with the
   same 503 `engine_waking` retry as sdcpp) → `/history/<id>` (polling) → `/view`.
   **generate only** — edit/inpaint need per-family image-to-image graphs (LoadImage + VAEEncode)
   that nobody has measured, so they are explicitly out of scope here (the definition of done is
   satisfiable with generate alone).
5. **Workflow templates for the five families** (`comfy_workflows.go`). SDXL, Z-Image-Turbo and
   FLUX.2 klein are ports from bench-image-engine.py (GPU-verified under "Resolved by
   measurement"), inputs (prompt, seed) included — the golden tests pin the same graphs that were
   measured. FLUX.1 and SD3.5 are new implementations from the published standard recipes and
   **were not verified on hardware in this session**. A golden test pins today's shape; it is not
   proof of correctness. The catalogue's `files[]` reuses sd.cpp's `Flag` vocabulary
   (`--diffusion-model`, `--clip_l`, `--t5xxl`, `--vae`) rather than inventing a second one for
   ComfyUI — so even families sd.cpp cannot run (klein, Z-Image) are declared from the same four
   values at ingest time.
6. **`generate_image`'s `model` argument** (decisions 5 and 7). Same "only offer it when there is
   really a choice" rule as `provider`: the enum appears only with two or more enabled
   checkpoints. The description carries the catalogue's `description` and which model is loaded
   right now (`warm`).
   🔴 **Side discovery**: `warm_model` (decision 7) had never once worked for the image role —
   neither sd-server nor ComfyUI carries `usage.model` in its response, so `recordUsage`'s early
   return always called `noteServed("", ok)`. The fix matters for sdcpp too (the image engine's
   `warm_model` panel field had always been empty): both Workspace-side providers now send an
   `X-AF-Model` header.
   One more: `engine_gateway.go`'s `dial()` always inserted `/v1/` when composing the upstream
   path, and ComfyUI's native API has no such prefix — `engineUpstreamPrefix` stops the insertion
   for the `comfy` provider only (sdcpp and llamacpp unchanged).
7. 🔴 **It broke the moment it was pushed to hardware — the own image would not even start.**
   `bench-image-engine.sh` gained `--baked` (measure the own image instead of the community one:
   no checkout, no pip, `/ComfyUI` used directly as the working directory) and was run on a
   g6.xlarge, where ComfyUI crashed at import: `comfy-kitchen==0.2.31` (a `requirements.txt`
   dependency) registers a custom op with a `list[int]` argument, and **torch 2.5.1's
   `torch.library.infer_schema` does not recognise that PEP 585 generic spelling**
   (`ValueError: infer_schema(func): Parameter kernel_size has unsupported type list[int]`).
   ComfyUI's own README states torch 2.7 as the minimum supported — CI only checked that the
   build passes, and **passing a build and starting are different things**. Rebuilt on
   `pytorch/pytorch:2.9.1-cuda12.8-cudnn9-runtime`, re-verified on hardware, **and it passed**.

**The definition of done (phases section) was met on hardware** (2026-09-10, g6.xlarge,
`--baked`). Inside one and the same ComfyUI process (no service or task restart) the checkpoint
was switched SDXL → Z-Image-Turbo → FLUX.2 klein 4B → SDXL → Z-Image-Turbo → klein 4B, and all
13 scenarios succeeded:

| | one warm image | against "Resolved by measurement" (community image) |
|---|---|---|
| SDXL 1024px (20 steps) | **8.02 s** | matches 8.0 s in measurement 3 and 0071 measurement 7's "8-something seconds" |
| klein 4B (4 steps) | **4.01 s** | matches measurement 3's 3.7-4.0 s |
| Z-Image-Turbo (8 steps) | **10.74 s** | matches measurement 3's 10.4-10.6 s |
| SDXL 512px | 3.01 s | — |
| SDXL + LoRA (warm) | 8.02 s (same as without) | matches measurement 3's "a LoRA is free once warm" |

Cold single images (switch included) were SDXL 26 s, Z-Image 55-57 s and klein 39-40 s — faster
than measurement 5's "a switch costs 1 to 2.5 minutes", presumably because
`LlmUseLocalStorage`/`ImageUseLocalStorage` now default to `true` (settled in open questions 9
and 10), so the read comes from instance store rather than EBS (and these models had only just
landed from their first fetch, so this is a first load, not a cached one). All 13 scenarios
returned `ok: true` with no errors. Four things were confirmed to fit together on hardware: the
own image, the CFN `ImageEngine=comfy` switch, the S3 layout (the `ln -sfn` equivalent mount) and
the workflow graphs. What was NOT confirmed is the Go `comfy` provider itself (a real call
through the CP gateway: `/prompt` → `/history` → `/view`), which stopped at unit and integration
tests — carried over.

## P2 on hardware (2026-09-10, af-sandbox)

P2's homework — **calling the Go `comfy` provider for real, through the CP gateway, from a
member session's `generate_image`** — was done. The conclusion first: the provider is correct,
and both `sdxl-base-1.0` (1024x1024) and `flux2-klein-4b` (3 images) generated. Getting there
meant walking into **four gaps in the implementation**. All four were green in CI, green on the
bench, and failed only on the deployment.

### What worked

| measurement | measured | control |
|---|---|---|
| SDXL, one image, cold | 42.33 s | reads `SDXLClipModel`/`SDXL`/`AutoencoderKL` from EBS |
| SDXL, one image, warm | **8.42 s** | ComfyUI on its own ("Resolved by measurement") was 8.02 s |
| klein, 3 images, switch from SDXL included | 35.89 s | klein warm on its own was 4.01 s per image |

**The Go provider's overhead is too small to measure.** A warm SDXL takes 8.42 s against 8.02 s
when ComfyUI is called directly — that 0.4 s difference is the whole `/prompt` → `/history` →
`/view` round trip plus building the graph. It is the plainest evidence that the provider is not
doing anything extra.

**The switch warning is too pessimistic for this configuration.** `comfySwitchWarning` follows
measurement 5 and says "1 to 2.5 minutes (an EBS re-read)"; the reality was 35.89 s, and that
includes generating 3 images. At klein's warm 4.01 s per image, 3 images ≈ 12 s, so **the switch
itself was about 24 s**. That is for a 7.75 GB klein and very likely does not hold for FLUX.1 dev
(22.2 GB), so the warning's wording is left as it is.

### The four gaps

1. **No route existed for writing `base_model` in a family's spelling.** comfy picks one of five
   templates by `base_model` and refuses by design to infer it from an id (decision 2 exists for
   that reason). Yet nothing anywhere wrote `sdxl` / `flux2-klein` / … — `seedEngineCatalog` does
   not set `BaseModel` (a seed cannot know the family), ingest stored HF / Civitai's **display
   name** (`"SDXL 1.0"` ) verbatim, and the Console had no input for it (display only). In other
   words, **using the Console alone, comfy could not generate a single image**. The catalogue was
   written by hand through the admin API this time. Fixed by putting the vocabulary and its
   validation in the CP and adding a picker to the Console.
2. **The Console's catalogue UI was still from a pre-ADR world.** The registration form held one
   fixed file and **could not even register** a three-file model (diffusion model + text encoder +
   VAE) such as klein or Z-Image. The API and the wire had supported it from the start; only the
   UI was missing. Fixed by adding multiple file rows, each with its role flag.
3. **`ln -sfn` created the link inside the baked-in `models/`** (correction 2 above). Every
   request 400'd.
4. **`PRESET_FILE` was used as a proxy for "is a router"** (correction 1 above). Models other
   than the starting one never came down. Fixed by splitting out `SYNC_ALL` (`"1"` when
   `ImageIsComfy`).

Also found in passing: changing an engine's mode never pushed a catalogue invalidation to running
workspaces. `notifyEngineCatalogChanged` fired only from `putModel` and `deleteModel`, never from
the mode route. The Agent caches the catalogue for 10 minutes, so after `mode=off` a session keeps
offering the engine for up to 10 minutes and calling it returns `503 engine_off` (a refusal, not
the retryable `engine_waking`). The mirror image of that — setting `mode=ondemand` and the tool
NOT appearing — was the very first thing that held this session up.

### What to take from these four

**"13/13 on a real GPU" was not evidence that the wiring being shipped had been measured.** There
are only two differences between the bench (`bench-image-engine.sh --baked`) and the production
task definition: how the models are handed over (bind mount vs symlink), and the ingest route
(the bench fetches for itself). **And those two are exactly where it failed.** That 3 and 4 both
happen to be "the part the bench routed around" is not a coincidence.

The harness was enough to measure "does the engine work", but it never measured "does the engine
work on this deployment". The minimum defence when writing the next harness of this kind is to
**keep at least the model hand-over identical to the task definition** (no taking the easy bind
mount). For 1 and 2 the lesson is simpler still — **the API and the wire supporting something
does not mean the UI does**. Decision 2 said "the operator declares it at ingest time", and P2
had nearly been called complete without the place to declare it ever being built.

## P2's remaining work 4 and 5, on hardware (2026-09-10, af-sandbox)

The two things the previous section left behind — **running an ingest for real, from HF and from
CivitAI** (remaining work 4), and **the three families that had never once gone through the
provider** (remaining work 5: Z-Image, FLUX.1, SD3.5) — were pushed through the same day. The
conclusion first: **all three families work now**, but SD3.5's template was wrong and could not
produce a single image until it was fixed. Six further gaps (5 to 10) were walked into — one of
them (8) is open question 3 surfacing as written, not a new discovery, and the last (10) turned
up only afterwards, from watching the fix for gap 1 being used.

### Remaining work 4 — ingest works. How it says "this will not work" has two holes

**The refusal was actionable for an operator.** Trying to take in a checkpoint from CivitAI with
no family declared is refused with a 400 before any task is started, and the message says three
things at once:

```
declare base_model as one of sdxl, sd35, flux1, flux2-klein, zimage: this engine runs comfy,
which picks a workflow by family and will not guess one (the repository calls it "SDXL 1.0")
```

The spellings, why one is needed, and **what upstream calls it**. The last is what does the work:
all the operator has in hand is the display name "SDXL 1.0", and mapping that onto `sdxl` is the
entire job. Putting the display name into `base_model` verbatim earns the same 400. In neither
case is a job row created (checked against the job list).

**Completion was confirmed too.** Four from Hugging Face (clip_l 246 MB, t5xxl_fp8 4.89 GB,
flux1-dev-fp8 11.9 GB, sd3.5_medium 5.11 GB) and one from CivitAI (the DetailedEyes_XL LoRA,
93 MB). And **the CivitAI row's `base_model` was empty** — upstream returns `"SDXL 1.0"` and it
is no longer stored. That is the most direct evidence there is that P2's change works on the real
route.

🔴 **Gap 5 — CivitAI's "you must be logged in" assets are invisible to `resolve`.** For the first
LoRA chosen, `resolve` answered `gated: false` / `can_ingest: true`, the job ran, and the Fargate
task died with `curl: (22) The requested URL returned error: 401`. Called by hand, CivitAI says
`{"error":"Unauthorized","message":"The creator of this asset requires you to be logged in to
download it"}` — **it is a per-uploader setting**. Across five assets the answers split 200 / 401
/ 403. Hugging Face's gated repositories have a route that refuses up front
(`ingest_gated_no_token`, decision 6 and P5); CivitAI has neither the concept nor a token field.
Unless `resolve` checks "can this asset be fetched anonymously" (one `HEAD` would do), what the
operator gets is a bare curl exit code nine minutes later.

> ✅ **Fixed (2026-09-10)**: `resolve` now sends **one `HEAD`** at the Civitai download URL and
> raises `login_required` on 401 / 403 (`engineCivitaiAnonymous`). The panel drops
> `can_ingest`, and the ingest refuses with `civitai_login_required` **before a task is
> started**. It is a DIFFERENT code from Hugging Face's `gated_no_token` not because the key is
> different but because there is none: gating is the repository's terms and a token satisfies
> them, while this is a per-uploader switch and this deployment has no Civitai account at all.
> So the Console's sentence is "pick another asset, or stage it by hand", not "register a
> token". It **fails open** in every direction it cannot read — a CDN that dislikes HEAD (405)
> and a probe that could not be made are not login walls; only 401 and 403 are. The tests cover
> 401, 403, 405 and **an asset with no wall going through** (the positive control).
>
> **No Civitai token field is being added (out of scope).** Three reasons: this deployment has
> no Civitai account, and creating one raises "in whose name, and who takes the terms on" with
> the same weight decision 10 gives licence acceptance; keeping the value means a second copy
> of the Hugging Face token machinery, which touches the 60-engines size wall (~200 bytes
> left); and two assets out of five hit this, all of them **avoidable by choosing another
> asset** — building the field after that stops being true is the cheaper order.

🔴 **Gap 6 — ingest cannot write a file's Flag.** The row `engineIngester` creates holds one
element, `Files: [{S3Key, Bytes}]`, and the Flag is always empty, i.e. "the whole checkpoint".
So **a split model cannot be assembled by ingest alone**. Building FLUX.1's four-file row meant
ingesting three of the parts as throwaway rows (purely to get the bytes into S3), re-registering
the real row with its flags through `POST /models`, and then forgetting the throwaway rows.
Decision 2 says "the operator declares it at ingest time", but the only thing that can be
declared there is the family — **not the role**.

> ✅ **Fixed (2026-09-10)**: an ingest request now carries `file_flag` and `attach`. The first is
> validated against the row's own `file_flags` (the list already served to the Console); the
> second — "add this file to the row that is already there", the only shape in which a split
> model can be assembled by ingest alone — is the **one** route allowed past the duplicate-id
> 409 (`engineAttachAllowed` refuses a missing row, a missing flag and a role the row already
> fills, all **before** the download). The append is one transaction in
> `AppendEngineModelFile` and touches neither the licence, the family nor the enabled flag of
> the row (turning it back into an upsert would recreate exactly what this section's
> duplicate-id refusal exists to prevent). The Console's ingest form has the role selector, and
> **derives the bucket directory from the role** (`engineIngestPrefix`) — a text encoder staged
> under `image/checkpoints/` appears in no loader's menu, so this is not cosmetic.

🔴 **A consequence of gap 6 — `?purge=1` can silently delete a file another row is using.** The
`purge` on forgetting a row hands that row's `files[]` S3 keys straight to the ingest task
(`deleteModel`). **Whether another row references the same key is not checked.** Now that gap 6
has made "ingest the parts as throwaway rows and reference the same keys from the real row" the
normal procedure for a split model, this is easy to walk into: in this very session
`clip_l.safetensors` was pointed at by both the throwaway `tmp-flux-clip-l` and the real
`flux1-dev-fp8`, and forgetting the former with purge would have silently broken the latter. They
were forgotten without purge.

> ✅ **Fixed (2026-09-10)**: `deleteModel` reads the catalogue of EVERY role **before** the row
> goes, and no longer hands MODE=delete a key another row points at (`engineKeysStillUsed`).
> What survived rides in the answer's `purge` string together with the row that keeps it alive
> — "kept" with no name is not something an operator can act on. When the catalogue read
> FAILS, nothing is deleted at all: deleting while it is unknown whether a file is shared is
> precisely this section's accident. Every role is read because nothing says the two rows are
> in the same one. The sharing itself is what decision 2 intended (`text_encoders/`: SD3.5 and
> FLUX.1 read the same T5-XXL and CLIP-L) and does not go away with gap 6. The test pins both
> directions — the shared key survives, and **the key whose last reference has gone is really
> deleted** (the positive control).

### Remaining work 5 — Z-Image and FLUX.1 went through. SD3.5's template was wrong

| family | result | measured (ComfyUI's own `Prompt executed`) |
|---|---|---|
| Z-Image-Turbo | ✅ generated | the first attempt 400'd on gap 7 below; the second worked |
| FLUX.1 dev (fp8, split) | ✅ generated (first attempt) | **78.19 s** (cold, switch included) |
| SD3.5 medium | ❌ → template fixed → ✅ generated | **46.90 s** (cold, switch included) |

**All five families have now returned an image on this deployment's GPU, through the Go
provider.**

FLUX.1 **did not use** the 22.2 GiB fp16 transformer already in S3 (put there for P4's gated
verification). The flux1 template requires the split form — `UNETLoader` + `DualCLIPLoader` +
`VAELoader` — while that row is a single file under `checkpoints/` with no text encoder at all.
A file's basename is the name handed to ComfyUI's loader, and the S3 key's **directory** decides
which loader's enumeration it appears in (`engineImageFiles`) — so anything under
`image/checkpoints/` is permanently invisible to `UNETLoader`. The fp8 split set was ingested
instead (unet 11.9 GB + clip_l + t5xxl_fp8, sharing the `ae.safetensors` VAE Z-Image already
uses). It also avoids betting that fp16's 23.8 GB fits on a 24 GB L4.

🔴 **SD3.5's template could not be known to be wrong until it was run.**

```
Value not in list: clip_name1: 'sd3.5_medium.safetensors'
  not in ['clip_l.safetensors', 'qwen_3_4b_fp8_mixed.safetensors', 't5xxl_fp8_e4m3fn.safetensors']
```

`comfyGraphSD35` assumed "Stability's official release bundles UNet+VAE+CLIP-L+CLIP-G in one
file", handed `TripleCLIPLoader`'s `clip_name1`/`clip_name2` **the checkpoint's own filename**,
and wrote that the loader would read out only the tensors it needed. It does not. It **cannot** —
`TripleCLIPLoader`'s three inputs are enumerations over `models/text_encoders`, and a name that
lives in `models/checkpoints` is not even a candidate. The premise itself was wrong.

The fix adds `--clip_g` to the file vocabulary. That is not the invention of a fifth flag but
**recovering one that was dropped**: the stable-diffusion.cpp vocabulary `EngineFile` borrows
from has always had `--clip_g`, and it was left out when that vocabulary was copied across.
SD3.5's three encoders are three separate files, and with no way to name clip_g this family could
never have generated. The same word was added to the CP's list (the copy served to the Console),
and the drift test was confirmed to hold the two together — by breaking one side and watching it
fail.

**A golden test cannot catch this.** What it pinned was the shape of a graph nobody had run,
exactly as this ADR said at the time of P2. Pinning a shape exists to make a diff readable, not
to prove correctness — and this is now the worked example.

### Three gaps in the deployment itself (nothing to do with families; anyone hits them)

🔴 **Gap 7 — the engine accepts requests for models that are not on the instance yet.** The fetch
sidecar declares `engine may start; 6 file(s) still to sync` as soon as the starting model (the
selected one) is down, and keeps fetching the rest in the background. Z-Image's first request
landed in the middle of that, and ComfyUI answered 400 with "the file is not there":

```
Value not in list: unet_name: 'z_image_turbo_bf16.safetensors' not in ['flux-2-klein-4b.safetensors']
```

The engine passes health, and this is not the gateway's `engine_waking` (which is retryable).
**Neither the operator nor the caller is given any hint that the model has not come down yet.**
On the second instance, 12 files and 48 GB took about 270 s (≈180 MB/s) to sync, and all 270 s of that
is this window. Whether the requested model's files are on the instance is a fact the CP already
knows, so making it wait as an `engine_waking` equivalent looks like the straightforward fix.

**Gap 8 — enabling a model on a running instance never syncs it. This is not a new discovery** — it is
open question 3 ("additional sync after the service is up") surfacing as written, a known hole
already deferred to P5. What is measured for the first time is what it does in the image role:
enabling `flux1-dev-fp8` and `sd35-medium` brought no files down, and the only way through was
`mode` `off` → `on` to **rebuild the instance**. In the llm role "wait for the next start" is enough;
in the image role **an enabled family appears in `generate_image`'s `model` enum while not being
on the instance**, so together with gap 7 it becomes "selectable, and answers 400". Solving this in P5
needs either the enum's condition moved from "enabled" to "on the instance", or the waiting added on
gap 7's side.

🔴 **Gap 9 — a 503 from `/history` is not retried.** The provider waits out and re-sends on a 503
from `/prompt` (`engine_waking`), but the polling that follows does not wait. If the instance is
replaced mid-poll, what reaches the caller is
`the image engine's /history answered 503 Service Unavailable: the fleet's own inference engine
is starting; retry` — a message that **says retry and does not retry** (measured). A cold
generation takes 47 to 78 s, so that window genuinely opens.

**An operational note (walked into here)**: waking an instance with `mode=on` and then putting it back
to `ondemand` makes **the controller stop that instance immediately**, because `last_demand` is stale.
That costs a 48 GB re-sync, so to warm an instance, leave it on `ondemand` and wake it with a request.

### Gap 10 — declaring a family clears the badge without making the row usable

Found after the fact, by watching an operator use the in-row family editor this very PR added.

`flux1-dev` — the 22.2 GiB checkpoint ingested on 2026-09-09 purely to prove that a gated
Hugging Face download works (P4), never enabled, never used to generate — carried
`base_model_missing`, so the row offered the family picker. Picking a family cleared the badge.

**The row still cannot generate, and now nothing says so.** The only check is
`engineBaseModelValid`, which answers "is this one of the five spellings". Nothing checks that
the files the declared family's template requires are actually on the row. `flux1-dev` is a
single unflagged file under `image/checkpoints/`, and the `flux1` template needs
`--diffusion-model` + `--clip_l` + `--t5xxl` + `--vae` — so no correct answer to the picker makes
that row work. (The family it was actually given was `flux2-klein`, which is a different
generation of a different model, but that is beside the point: `flux1` would not have helped
either.)

The failure is at least safe and early-ish: `comfyBuildGraph` refuses with `errComfyMissingFile`
before any HTTP call, so a wrong row produces a readable refusal rather than a wrong picture. But
it is a refusal at generation time, for a row the panel has stopped flagging — which is strictly
worse than the state before the family was declared. The CP holds both halves of the fact (the
family, and each file's flag), so the check belongs where the family is declared or where the row
is enabled.

> ✅ **Fixed (2026-09-10)**: it went in **both** places. Where the row is ENABLED it refuses —
> `engineFilesGuard` answers 409 `engine_files_missing` and names the roles that are missing.
> There is no confirm, unlike the VRAM gate: this is not a bet under uncertainty, it is the
> same fact `comfyBuildGraph` refuses on before it dials anything, and enabling would only put
> an id in `generate_image`'s enum that every request bounces off. Where the family is
> DECLARED it does not refuse — refusing the one act that repairs a broken row would leave it
> broken and unfixable — it puts `files_missing` on the row instead, and the Console names the
> parts. The authority for what a family reads is still the per-template guards in
> `comfy_workflows.go`; `engine_catalog_test.go` builds the list **out of those guards**
> (`f.X == ""` plus `resolveComfyFiles`' switch) and fails on any drift, Fatally if it cannot
> read all five families.

`flux1-dev` was deleted with `?purge=1` afterwards, reclaiming the 22.2 GiB — its P4 purpose was
served in 2026-09-09 and the FLUX.1 row that generates is the separately ingested
`flux1-dev-fp8`.

### Measured in passing

- `warm_model` returned `z-image-turbo`. P2's `X-AF-Model` fix works on the real deployment.
- ADR 0074's VRAM gate fired correctly (`flux1-dev-fp8 wants at least 16571 MiB … the l4 class
  declares 8000 MiB`). But **`l4`'s declared 8000 MiB does not match the hardware** — the engine
  says `Total VRAM 22563 MB`. `l40s` declares 44000 for a 48 GB card, so the L4 rung is the one
  that is an order of magnitude out. The result is that every image model over 8 GB needs
  `confirm_vram`.
- **Not measured**: whether the catalogue push on a mode change reaches a running session in less
  than 10 minutes. The mode was changed five times, but the session's tool listing was never
  observed.
- **Not measured**: the Console ingest form actually drawing. That the CP puts `base_models` and
  `file_flags` on the wire, and that `resolve` returns the display name the hint interpolates,
  were both measured — but a headless click-through could not reach the admin modal, so the
  screen itself was never seen.

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
  sleeping service per model and two instances when two models wake. The router answers for llm,
  "the next start" for image, on the same instance.
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
- **The instance pulling from HF / Civitai directly.** ADR 0071 decision 3 stands (4–236 MB/s with no
  way to know which, and a token on the instance).
- **The gateway reading the catalogue from SSM.** An SSM call per request (the reason
  `engines.go` reads once at startup). SSM carries only **the active set the instance reads**; the CP
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
3. **Syncing into a running instance.** Can a newly enabled model be added to a running instance
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
   **The ingest half was settled on 2026-09-10** ("P5 on hardware"): with a registered token,
   both `stabilityai/stable-diffusion-3.5-medium` and `black-forest-labs/FLUX.1-dev` were taken
   in with no 401. What was taken in is **one smallest file per repository** (the 167,666,902 B
   VAE in both), which is what settles a per-repository gating question — **the checkpoints
   themselves were not taken in and not drawn with** (the 23.8 GB `flux1-dev.safetensors` does
   not fit an L4). Generating with these two families is already done, from the ungated mirrors,
   under "P2's remaining work 4 and 5, on hardware".
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
   (27 GB) leave the page cache because the instance has 15 GB; `ImageMemMinMiB=30000` selects a
   g6.2xlarge (32 GiB) with no code change. If a switch becomes RAM → VRAM (seconds),
   `useLocalStorage` is back to being a start-time question. `bench-image-engine.sh` measures
   both as they are, and since the answer changes P2's definition of done (the price of a
   switch), it is measured **before P2**.
10. ~~**Re-examine where the models live at all**~~ **Settled (review 2026-09-09, sections 1-3)**:
    **(a) adopted** (`useLocalStorage` — cold start 527-586 s → 275 s, swap 276-282 s → 98.5 s,
    now the default), **(b) disproven** (a warm instance cannot be built on MI at all: Bottlerocket's
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
    - **(b) The warm instance.** ADR 0071 decision 7(c) is **still unproven**, and the failure is
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
    option is adopted.** **Implemented (2026-09-10 — "Follow-up: the tenant axis, implemented" at
    the end of this ADR; not verified on hardware).** Measurement showed **the walls come in a different order than the text
    below says**: **time (~5 models) → disk (~13) → SSM (~38)**, so 4,096 characters is the last
    wall, not the first. What follows is the original text:
    The catalogue's key is
    `(role, id)` with no tenant, and "a tenant picks a model from Hugging Face and places it" is
    the right direction for usability — but **on a shared instance four things multiply**: the active
    set is one per engine so it becomes the **union** of every tenant's enabled models (which is
    where the 4,096 characters finally bite; it is at 6% today), the cold start syncs *every*
    enabled model so it grows with the tenant count, `LlmModelsMax=1` means more swapping at
    1–2.5 minutes each, and one shared active set makes one tenant's model ids visible to
    another. An instance per tenant removes all four at $1.26/hour per tenant, which discards the
    premise ADR 0071 was built on (one shared instance, asleep). **The middle:** keep the catalogue
    deployment-wide and put the tenant axis on **who may ingest and who accepted the licence**.
    Nothing multiplies and most of the usability is won; `source` and `license_accepted_by` are
    already half of that shape.
    - **A bucket per tenant is not recommended.** A bucket is not the boundary that matters —
      whichever one they came from, the models land on **the same instance's same disk and are read by
      the same process**. The boundary is the GPU instance. It also duplicates shared models per
      tenant and loosens the engine task role's grant, which is scoped to one bucket ARN today.
      If isolation is wanted, **prefixes in one bucket** (IAM scopes by prefix, nothing is
      duplicated).
12. ~~**Let the Hugging Face token be registered from the Console**~~ **Decided (review
    2026-09-09, section 5) — "DB as the source of truth, Secrets Manager as transport" is adopted,
    and "never reads back" is enforced by IAM (`PutSecretValue` only, `GetSecretValue` never
    granted).** **Implemented (2026-09-09, "P5 implementation" — shape (b), the always-created
    secret; **verified on hardware on 2026-09-10** — "P5 on hardware").** The entry condition — **headroom in
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
  taken across a switch). **All three were driven on hardware** (two GGUFs on one instance, answering in
  0.4–0.7 s, 10 s and 276–282 s; and with the second row registered and enabled in the Console,
  the picker offered two models and a session started on the second). Only the row-creating step
  needs a person — it is a super_admin screen, which AWS credentials cannot drive.
- **P2 — ComfyUI (ADR 0071's P2, moved forward to here). Implemented AND verified on hardware
  2026-09-10** ("P2 implementation", "P2 on hardware", "P2's remaining work 4 and 5, on
  hardware"). The self-built image (pinned tag `v0.34.0`, no
  Manager; the `20-platform` `af-comfyui` ECR repository and a dedicated CI workflow — the CI
  bake itself was confirmed to succeed), the `ImageEngine=comfy` `!If` (decision 4), the `comfy`
  provider (**generate only** — edit/inpaint need a per-family image-to-image graph nobody has
  measured yet, so they are out of scope this round; the definition of done only needs generate;
  driving `/prompt` → `/history` → `/view` with progress notifications), templates for five
  families — SDXL, SD3.5, FLUX.1, FLUX.2 klein, Z-Image — kept in the repository behind golden
  tests (SDXL/Z-Image/klein are ports of the GPU-verified graphs from *Resolved by measurement*;
  FLUX.1/SD3.5 were new, and have since been run on hardware too), and `generate_image`'s `model`
  argument
  (enum = the enabled checkpoints; several for the first time — and **the description names the
  model that is warm now**: a switch is a 1–2.5 minute re-read, so the agent can prefer the warm
  one when the default will do; *Resolved* 5). The
  pane (`/engine/comfy/` over WebSocket) is **not included** — `generate_image` needs only the API;
  the screen is P5. **Definition of done: on one instance, SDXL and
  klein 4B (or Z-Image-Turbo) alternate per request and return pictures with no service restart
  in between, and 1024px SDXL comes back in the 8-second range of ADR 0071 measurement 7. VERIFIED
  ON HARDWARE**: warm SDXL 8.02 s, warm klein 4.01 s, warm Z-Image 10.74 s, all 13 scenarios
  (including three checkpoint switches) succeeded on one g6.xlarge with no restart. A real bug
  surfaced along the way and is fixed: torch 2.5.1 (the original base image) cannot even import
  ComfyUI v0.34.0 (a `comfy-kitchen` dependency needs `torch.library.infer_schema` to understand
  PEP 585 `list[int]`, which 2.5.1 does not) — moved to `pytorch/pytorch:2.9.1-cuda12.8-cudnn9-runtime`.
  The Go `comfy` provider's own call through the real CP gateway was verified on hardware the
  same day ("P2 on hardware"). **The remaining ingest route and the three families that had never
  gone through the provider were pushed through as well, and P2 is closed on hardware** ("P2's
  remaining work 4 and 5, on hardware"): all five families generate. SD3.5 alone had a wrong
  template and was fixed (`--clip_g`).
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
- **P5 — syncing into a running instance (open question 3), virtual model ids for llm (the second
  half of decision 5), the ComfyUI pane, sd-server's async API (open question 6), the tenant
  axis (open question 11).**
  **The tenant axis (open question 11) is implemented** (2026-09-10 — "Follow-up: the tenant
  axis, implemented" at the end of this ADR; not verified on hardware): the permission gate on
  ingest, and the four-part acceptance. **Done when: a tenant_admin of a granted tenant can start
  an ingest, a tenant_admin of an ungranted tenant and a plain member of the granted one are both
  refused with 403, and the row that results records which tenant, which person, when and which
  licence.**
  **Registering the Hugging Face token from the Console (open question 12) was implemented
  ahead of the rest** — it depends on nothing else here and it decides outright whether gated
  repositories (all of SD 3.5, all of FLUX.1) can be taken in at all. **Done when: a token is
  registered in the Console, a gated repository is taken in without CloudFormation being
  touched, and the ingest task's log carries no 401.** (**Met on hardware on 2026-09-10** — see
  "P5 implementation" and "P5 on hardware". The rest of P5 is not started.)
- **P6 — retiring the seed and the four remaining parameters. Implemented and verified on
  hardware ("P6 implementation", "P6 on hardware").** (Added 2026-09-10; the reasoning,
  the trap and the migration window are in the follow-up section at the end of this ADR). This is
  decision 1 finishing rather than a new idea: `*ModelFile` went in 0.18.0, and what is left is
  `<Role>ModelS3Key` / `ModelIds` / `ContextTokens` / `MaxOutputTokens`, `seedEngineCatalog` and
  the engine table's `models` / `contextTokens`.
  **Done when: a stack deployed with `LlmEnabled=true` and no model parameters at all comes up,
  its role's service stabilises, the Console registers a model into an empty catalogue and the
  engine starts on it** — and 🔴 the upgrade note tells a deployment that set only
  `<Role>ModelS3Key` to add `<Role>Enabled=true` FIRST, because the condition that creates the
  service reads that key today.
  (**Met on the dev deployment on 2026-09-10** — "P6 on hardware". The migration side was met the
  same day: that deployment had both `<Role>Enabled` empty, and without paying the translation
  first `update.sh`'s update would have deleted both roles while reporting "no changes".)

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
  administrator keeps buying a "no model" instance at $1.26/hour and the panel says "starting"
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
  instance for stabilisation, as today** (the placeholder carries the `GPU`
  `ResourceRequirements` too) — what disappears is the second pass, not the first ten minutes
  and $0.2. Write it down. (e) Under those conditions the two-pass stand-up really does go.
- **Decision 2** — (a) The IAM premise is correct (R2(a)), and adding the instance-side
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
  **the instance** (only enabled LoRAs are synced, so an absent name fails in the engine;
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
  the instance reads and the CP never does. (c) The alternative that keeps "zero CP IAM" is
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
  with RAM** alongside: the three models (27 GB) fall out of the page cache because the instance
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
   statement inside `60-engines` on the instance side, consistent with 0071 decision 8. What does
   not close is **the size of the value**: 4,096 characters (R2(c)).
2. **It stabilises, under three conditions** (decision 1's revisions (a)(b)(c)): both roles on
   the wrapper, `ParameterNotFound` as empty, the `mode=on` hole closed. There is no container
   health check (ECS's RUNNING means "the essential container started") and that is all
   CloudFormation waits for, so `sleep infinity` stabilises. The second pass goes; the one GPU
   instance for stabilisation stays.
3. **It does not fit.** 3,400 freed against 5,450 added (4,350 with `Mappings` sharing),
   about 1-2 KB over (R4). Moving 3 KB of the 15.5 KB of comments to
   `PARAMETERS-60-engines.md` makes it fit. `20-platform` also needs an ECR repository.
4. **The rejection sits in the wrong place** (decision 5's revision) — the CP does not read
   the body, so the Agent and the instance reject. `model` fits `Request.Model`, `loras` fits with
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
session. 10 was measured on hardware; 11 and 12 were decided as design, without waking an instance.
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

What was measured is **the deployment's own `llm` role** — not a bench instance, but the very path
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

🔴 **Read the swap, not the fetch.** Decision 3 priced "one model per instance, swap on demand" at
**276-282 seconds**, and measured point 5 put the image role's switch at 1-2.5 minutes. That
price is now **98.5 seconds**. The swap is where a person waits; the cold start (275 s) is next.

Both costs are real and neither is new: an instance store is wiped with the instance — but **MI
deletes the EBS data volume too**, so a cold start always paid a fresh S3 fetch. And `*StorageGiB`
stops meaning anything: the instance gets whatever the instance type carries, which on g6.xlarge is a
**245 GB ext4 filesystem**, i.e. *more* than the EBS setting's 120 GiB. ⚠️ It follows that
`*AllowedInstanceTypes` may only name types that HAVE an instance store (every g6 and g5 size
does).

**Adopted**: the default of `LlmUseLocalStorage` / `ImageUseLocalStorage` is now `true`, and the
dev deployment is in that state. Reverting is one parameter.

### 2. Open question 10(b) — the warm instance is not "unproven", it is DISPROVEN

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
at all**. Keeping an instance buys the image layers and nothing else, so `*ScaleInAfter: -1` is a way
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
throughput. **That is HPC-cluster pricing, not the price of one sleeping GPU instance.**

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
  GPU instance, not the bucket). If isolation is needed, prefixes within the one bucket.

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

**Measured — same instance, same task, the cli pass first so a warm page cache cannot flatter the
mount:**

| | the 18.5 GB GGUF | |
|---|---|---|
| control: `aws s3 cp` (what the sidecar does today) | **88 s = 210 MB/s** | |
| **sequential read through Mountpoint** | **32.7 s = 567 MB/s** | **2.7×** |

The byte count is dd's own — 18,556,689,568, the **whole file** — so a short read is not being
mistaken for a fast one. Note the same `aws s3 cp` measured 158 MB/s in measurement 1 and 210 MB/s
here: it moves with the instance and the hour, **which is exactly why the control lives inside the same
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
DO NOT ADOPT.** Same instance, same task, with a copy-then-load pass as the control:

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
deployment's `LlmAllowedInstanceTypes` had narrowed to the single type, so no instance could launch.
Restoring the template default (`g6.xlarge,g5.xlarge`) fixed it — a re-run of exactly what ADR
0071 recorded under "the instance will not launch because the candidate list was one type".

### Suggested status line

Mark open question **10 as settled** ((a) adopted, (b) disproven, (c) rejection upheld) and **11
and 12 as decided**. Implementation of 11 and 12 is P5, and **12's entry condition is "free up a
resource's worth of headroom in `60-engines.yaml`"**. Open question 9(1) is the same thing as
10(a) and closes with it.

## Follow-up — retiring the seed and its four parameters (2026-09-10)

Asked while reviewing the `llamacpp` panel: **why does the seed exist at all? Let the catalogue
start empty, and register a model from the admin modal when somebody wants one.**

That is what this ADR already decided; the only open part is timing. Decision 1 keeps the twelve
parameters "one release for compatibility, read as the **seed** of an empty catalogue (decision
7)" and states that the parameters **shrink to `LlmEnabled` / `ImageEnabled`**. Decision 1's own
sub-point makes an empty catalogue a first-class state: the fetch sidecar treats "nothing to
load" as success, the wrapper idles (`if [ -s /models/cmdline ]; … else sleep infinity`), and the
gateway does not wake a role whose catalogue is empty (`503 engine_unavailable`). The first half
has shipped — **`LlmModelFile` / `ImageModelFile` were removed in 0.18.0.**

What is left to retire:

- `LlmModelS3Key` / `LlmModelIds` / `LlmContextTokens` / `LlmMaxOutputTokens`, and the image
  role's `ImageModelS3Key` / `ImageModelIds`. **`*ExtraArgs` stays** — those are the INSTANCE's flags
  (`-ngl 99`, `--diffusion-fa`), not a model's;
- `seedEngineCatalog` and `engineSeedKind` in the Control Plane, and the engine table's `models`
  / `contextTokens`.

**Why now rather than "eventually".** `60-engines.yaml` is **51,119 bytes, 81 short of the
51,200-byte wall**, and this ADR's own estimate for the twelve parameters plus the old command
lines is **about 3,000 bytes**. Phase P2's remaining work (per-family image-to-image graphs) and
ADR 0074's ladder both have to fit inside that same template.

🔴 **The trap: `*ModelS3Key` is not only a seed.** `HasLlmModel` / `HasImageModel` are
`!Or [ <Role>Enabled = "true", <Role>Enabled = "" AND <Role>ModelS3Key ≠ "" ]`, so the key also
decides **whether the role's service is created at all**. Removing the parameter without making
`LlmEnabled` / `ImageEnabled` the only gate **deletes the role** on every deployment that set
only the key — silently, as an ordinary stack update. The removal and its upgrade note are one
change, not two.

**The migration window is narrow.** The seed runs only on an EMPTY catalogue, so any deployment
that has passed through the release which seeded it already has rows and would not notice the
removal. The condition to state is "upgrade through 0.18.0 first"; only a jump from a
pre-catalogue Control Plane straight to the version without the seed comes up idle.

By-product: one of the two ways to create a row with **no licence, no `source` and no VRAM**
disappears. The other is the hand-registration form, which sends no licence fields although
`POST …/engines/{key}/models` accepts them — worth closing in the same pass, since decision 10
put the licence on the panel row and the panel can only show what was recorded.

## Follow-up: the tenant axis, implemented (2026-09-10)

Open question 11 was settled in the 2026-09-09 review ("adopt the middle option") but never
built. What went in is exactly the two things that were settled — **the primary key stays
`(role, id)`** and the catalogue stays one per deployment.

**1. Who may start an ingest.** A tenant's `limits` gains `allow_engine_ingest` (`tenantLimits`,
toggled by a super_admin on the tenant's settings screen). Of the engine admin routes, **only the
six ingest ones** go through the new gate (`ingestAdminFor` in `engine_ingest_perm.go`):
`POST …/{key}/ingest`, `GET …/{key}/ingest`, `…/ingest/resolve`, `…/ingest/files`,
`…/ingest/search` and `POST /api/admin/engines/search`. Through it come a super_admin, and a
**tenant_admin** of a tenant with the flag — those two and nobody else.

**Enabling a model, the selected checkpoint, forgetting a row and the Hugging Face token stay
super_admin**, because each of them decides what every *other* tenant runs. Only ingest stops at
"add to the catalogue".

The gate is read **per request** (`ListMemberships` returns active rows only, so a tenant_admin
taken off the roster stops passing on the next call). A test pins that withdrawing the grant
gives 403 on the next request.

**2. The four-part acceptance.** `license_accepted_by` / `_at` gain
**`license_accepted_tenant` and `license_accepted_license`**, making the
`(tenant_id, member_id, accepted_at, license)` the review asked for. The migration is written in
both series — sqlite `0061` and postgres `0046` — and
`TestMigrationSeriesDeclareTheSameSchema` compares the two without a Postgres server
(`TestSchemaDialectParity`, which needs `AF_TEST_DATABASE_URL`, skips).

- **An empty tenant is not a gap.** A super_admin accepts on behalf of the whole deployment and
  has no tenant to be acting for. Writing in whichever tenant header the Console happened to send
  would **put a tenant's name on an act it did not perform**.
- **Holding the licence a second time is not duplication.** `license` / `license_name` next door
  **describe the model** and are corrected when the model card is. This one is **evidence about a
  past act** and must not move when upstream relicenses.
- The audit row gets a tenant here for the first time (`auditFor`). Every other engine action — a
  mode, a class — really is deployment-wide, so those stay unscoped.

**3. The cost is not hidden.** The review said to write down that **every model id is visible from
every tenant**, so `guide/admin/04` (ja and en) gains a section and the tenant-limits list in
`guide/admin/02` points at it. The reason is given as the number it is: since every enabled model
is synced on every start, a per-tenant catalogue pushes the cold start **past ten minutes at about
five tenants, even with one model each**.

**What is left.** The Console's **engines panel itself is still super_admin** (`GET
/api/admin/engines` keeps `withSuperAdmin`). A tenant_admin of a granted tenant **can ingest
through the API but has no screen**: opening the panel to a non-super caller needs a reduced row
with the mode, the class and the instance's state taken out, which is wider than this pass. The
operator's side — granting it, and reading the acceptance it produces — is complete. Hardware
verification is also outstanding (nothing here touched a deployment).

## P6 implementation — the seed and six parameters are gone (2026-09-10)

The removal the follow-up section above called for, implemented. **Not verified on hardware.**

1. **The template.** `LlmModelS3Key` / `LlmModelIds` / `LlmContextTokens` /
   `LlmMaxOutputTokens` / `ImageModelS3Key` / `ImageModelIds` are gone, and `HasLlmModel` /
   `HasImageModel` are now the single test `<Role>Enabled = "true"`. Four fields left the engine
   table with them (`models`, `contextTokens`, `maxOutputTokens`, `modelS3Key`). `*ExtraArgs`
   stays. **50,997 → 49,298 bytes** (1,699 freed, 1,902 short of the wall). **The room is left
   unspent** — it belongs to P2's remaining work (per-family image-to-image graphs) and ADR
   0074's ladder.

2. 🔴 **The trap needed three answers, not just a note.**
   - The upgrade note is in `cfn/PARAMETERS-60-engines.md`, "Upgrading: the model parameters are
     gone" — the same place 0.18.0's `*ModelFile` removal was written up. It tells a deployment
     that set only `<Role>ModelS3Key` to **add `<Role>Enabled=true` first**.
   - `standup.sh` translates BEFORE it drops: a captured `<Role>ModelS3Key` with an empty
     `<Role>Enabled` becomes `<Role>Enabled=true`, and only then does `af_param_drop` remove the
     six. Dropping alone would delete the role on any deployment that did not read the note — not
     by refusing the deploy ("Parameters: [X] do not exist"), but by **succeeding** as an ordinary
     stack update with the service gone.
   - The sd-server `crane copy` reads it the same way (earlier in the same script). Read plain
     `ImageEnabled` there and the role is created while its ECR repository is empty:
     `CannotPullContainerError`, with CloudFormation blocked on stabilisation.
   `update.sh` and a hand-run `cloudformation deploy` pass no parameters at all, so nothing can
   translate for them. That is what the note is for.

3. **The Control Plane.** `seedEngineCatalog` / `engineSeedKind` and `engineDef`'s four fields
   are deleted. A table written by an older stack **still parses** (the fields are ignored) —
   the CP is upgraded before the stack is, so that shape is live. The empty-catalogue behaviour
   (`503 engine_unavailable`, `no_model`, `TestDecideEngineAction`'s fixture) is unchanged.

4. **The hand-registration form's licence fields were already closed** before this work started
   (commit `2a998f11`, the same day): the form sends `license_name` / `license_url` and the CP
   derives the commercial-use verdict from them. The by-product the follow-up asked for is done.

5. **Verification.** The Go suites in control-plane and workspace/agent, the Console tests,
   `deploy/local/ecs-lifecycle-stub-test.sh` (including 3b-2's size check), `cfn-ascii-test.sh`,
   `engine-sidecar-test.sh` and `check-cfn-exports.py`. The stub test gained a case that runs a
   pre-P6 capture through stand-up and checks (a) that no retired key reaches `deploy` and (b)
   that `<Role>Enabled=true` does. A positive control that removes the translation shows the
   check actually fails.

**What hardware has to confirm (the definition of done).** A deployment made with
`LlmEnabled=true` and not one model parameter comes up and its role's service stabilises; a model
registered from the Console into an empty catalogue starts the engine. And the migration side —
a stand-up from a capture holding `<Role>ModelS3Key` keeps the role and translates it into
`<Role>Enabled=true`.

(**Pressed on hardware on 2026-09-10** — next section. The definition of done was met, and the
migration trap was armed on the dev deployment.)

## P6 on hardware (2026-09-10, the dev deployment)

The previous section's definition of done, pressed with exactly one GPU wake. **It was met.**
Times are UTC; seconds and byte counts are measured from the API and the logs.

### The trap was armed — and the "harmless pre-update" does not go through

Before deploying, `describe-stacks` was read on the live 60-engines. **Both `LlmEnabled` and
`ImageEnabled` were empty**, and both roles stood on the `<Role>ModelS3Key` branch alone: exactly
the shape the previous section worried about.

🔴 **`<Role>Enabled=true` cannot be recorded "first, harmlessly".** `update-stack
--use-previous-template`, everything else at `UsePreviousValue` and those two set to `true`, is
refused:

```
An error occurred (ValidationError) when calling the UpdateStack operation:
No updates are to be performed.
```

The reason is that in the deployed template `LlmEnabled` is **referenced only from Conditions**.
Setting it merely satisfies the first branch of the `!Or`; the condition's value does not move,
**no resource changes**, and CloudFormation will not execute an empty change set. It is refused
for being *too* harmless — so the translation has to ride on the same update that applies the new
template. What actually went through:

```
aws cloudformation deploy --stack-name <60-engines> --template-file cfn/60-engines.yaml \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides LlmEnabled=true ImageEnabled=true
```

`Successfully created/updated stack`. **`ecs list-services` diffed to nothing** (both engine
services and the other three untouched), the six parameters are gone, and `LlmEnabled` /
`ImageEnabled` read `true`. The engine table (SSM `/af-ws/engines`) lost `models`,
`contextTokens` and `maxOutputTokens` with them.

**`af_param_drop` was not needed on this route.** `cloudformation deploy` only refuses an
undeclared key it is *given*, and `update.sh` gives none — the six simply disappear. The drop
exists for `standup.sh`, which reads a capture and passes it on.

`dev-deploy.sh` was then run. Its 60-engines step said:

```
==> cloudformation deploy af-ecs-engines (60-engines, parameters unchanged)
No changes to deploy. Stack af-ecs-engines is up to date
```

🔴 **That one line is the update that would have deleted both roles had the switches not been set
first.** The deploy succeeds and the output reads as "nothing to do". Nothing anywhere is an
error. "That is what the note is for" looks like this on hardware.

### The definition of done

**An empty catalogue does not bring the engine up.** The llm role's two rows were deleted
(without `?purge=1`), leaving `has_models: false`, and the mode was set to `on`. **It reaches
`desired: 1` first** — the mode toggle moves ECS at once, and `no_model` is seen on the next
controller tick:

```
15:09:38  PUT mode=on  → desired=1
15:09:41  (service …-engines-llm) has started 1 tasks: (task 931031…)
15:09:47  engine llm: stop (no_model)            ← the CP's log line, verbatim
15:09:50  (service …-engines-llm) stopped 1 pending tasks.
15:09:51  (service …-engines-llm) has reached a steady state.
```

**Nine seconds.** The task died pending, never reached RUNNING, and no instance was bought
(`desiredCount` stayed 0 through three further minutes of watching). "Does not start" is true,
but **"never asks" is not**: `decideEngineAction` returns `no_model` correctly, and the mode
route moves ECS ahead of it. On a deployment where `on` is held against an empty catalogue, those
nine seconds repeat per tick.

**Register one row and it comes up on that one model.** The smaller of the two saved rows
(`qwen2.5-coder-1.5b`, 1.1 GB — not the 18.5 GB 30B; the same fact costs 1/17 of the bytes) was
registered with `POST …/models` and enabled:

```
15:14:33  engines: llm catalogue row registered: qwen2.5-coder-1.5b (1 file(s), disabled)
15:14:33  engines: llm active set published to /af-ws/engines/llm/active (149 bytes)
15:14:54  engine llm: start (admin_on)
15:26:29  (service …-engines-llm) has started 1 tasks: (task 9345d9…)
15:28:12  engine llm: warmed up (ready)
```

**819 seconds (13 min 39 s) from enabling the row to warm.** **692 of those are the capacity
provider acquiring a g6.xlarge and placing the task** (`start (admin_on)` → ECS starting the
task); from instance to warm is 103 s. It is longer than P0's 527 s because capacity was slow that
day, not because of the sync — the sync is five seconds:

```
engine fetch: active set for /af-ws/engines/llm/active starts with 'qwen2.5-coder-1.5b'
engine fetch: llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf 1117320768 bytes in 5s
engine fetch: preset /models/llm/presets.ini holds 1 model(s), 'qwen2.5-coder-1.5b' loaded at startup
engine fetch: cmdline = --models-preset /models/llm/presets.ini
engine fetch: engine may start; 0 file(s) still to sync
```

**The instance synced only what the catalogue held** — the 18.5 GB 30B was still sitting in the bucket
and was not touched. That is decision 1's "the catalogue is the whole declaration" demonstrated
on the real path. The engine's own log says the same:

```
srv   load_models: Loaded 1 custom model presets from /models/llm/presets.ini
srv    operator():   * qwen2.5-coder-1.5b
srv  llama_server: starting server in router mode. models will be automatically loaded on-demand
srv  load_startup: (startup) loading model qwen2.5-coder-1.5b
srv          load:   /models/llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf
```

⚠️ **No completion was actually sent.** The gateway (`/engine/{key}/v1/…`) only accepts a token a
Workspace issued, never an administrator's cookie — measurements 10 and 13 already record the
same limit. The evidence used instead is the two logs above and the warm verdict below.

### The warm probe ran on hardware (measurement 13's price is paid)

Measurement 13 left "the CP's warmProbe has not run on hardware" as its price: the instance was
brought up then with a bare `run-task` the controller never sees, and `maintainWarm` is only
called from the service's state. **This time the instance came up through the service** (`mode: on` →
`admin_on`), so the CP's probe read the router's `/models` and set warm: `GET
/api/admin/engines` shows the llm role at `warm: true`, and the CP logged `engine llm: warmed up
(ready)`. That is also the engine **answering `/models` with that one model** — the verdict reads
each model's `status.value` (decision 3's redefinition), so an empty answer would not raise it.

### 🔴 A gap — the admin API cannot read a catalogue row back

Found while cleaning up. **`GET /api/admin/engines` returns none of a row's `s3Key`, `flag`,
`bytes` or `args`** (`files` is the `path.Base` of the keys and nothing else). Now that P6 has
made the catalogue **the only declaration**, that means "delete a row and only the person who
deleted it can put it back". The Console is the same: its form holds `s3Key` only to **write**
it, never to read it back.

It was recoverable here — an llm row is a single gguf whose key is mechanically `llm/<filename>`,
which could be checked against the bucket listing. `bytes` was left at **0** deliberately: filling
in the real size flips `vram_need_source` from `unknown` to `floor`, which is a different row.
Both rows came back **identical in every field the API returns**. But **it does not hold for a
split model** — FLUX.1's four files or SD 3.5's four can only be restored by somebody who
remembers the keys or wrote them down. "The bytes are still in S3 and the row that points at them
cannot be rebuilt" is the same hole decision 7's two-step delete exists to prevent, opened on the
read side. Unlike P5's token (where being unreadable *is* the specification), this one can be
closed.

### What was deleted and what was left

The llm catalogue is back as it was (two rows, both `enabled`, `qwen3-coder-30b-a3b` the
`default`, mode `ondemand`). `?purge=1` was not used, so both objects are untouched in S3
(18,556,689,568 B and 1,117,320,768 B). The GPU was handed back with `mode: off` — off at
15:37:39, `stopped` at 15:40:20, and the G-family container instance was gone within the 15:39
minute (**about two minutes**, not the 7–8 of a drain). The image role was not touched at all.

## Follow-up — phase P3's LoRAs, the Agent's half (2026-09-10)

**Only the Agent's half landed in this pass.** P3's completion definition in the Phases section
— "the same prompt and the same seed produce a different picture with the LoRA than without, and
an SD1.5 LoRA does not appear in SDXL's enum" — is **not met**. The first half can only be
measured on hardware, and no GPU was woken here.

### What landed

- `loras: [{name, weight}]` on `generate_image` (weight 0-2, 1 when unstated, at most four per
  request), and `Loras []{name, description, baseModel}` on `Caps` — exactly the shape decision 5
  predicted in R7.
- A `LoraLoader` chain in all five family templates. Nothing is added when nothing is asked for,
  so a graph without LoRAs is byte-for-byte the one the golden fixtures already pin.
- The base-model refusal, placed in the **Agent** (レビュー決定 5). Three refusals — a name the
  catalogue does not hold, a family that differs from the checkpoint's, and a LoRA that declares
  no family at all — and none of them reaches the gateway or wakes a GPU.
- The Control Plane's catalogue wire **already carried everything**: `loras` is its own array
  next to `model_rows` (`engineCatalogRowFor` in `engine_gateway.go`), and each row goes through
  `engineCatalogModelRow`, so it has `base_model`, `description` and `files`. Not one line was
  added to the CP; a test pins that none of it is dropped
  (`TestEngineCatalogRowSeparatesLorasFromCheckpoints`).
- The other side of the same separation — a LoRA never reaching `model`'s enum — is pinned too.
  The CP puts a LoRA row in neither `models` nor `model_rows`, so the Agent's
  `engineImageModelIDs` cannot see one by construction.

### Where decision 5's two rules appear to collide

The Phases section's completion definition says an SD1.5 LoRA **must not appear in SDXL's enum**,
while decision 5's revision says that **an enum cannot depend on another argument**, so `loras`
enumerates every enabled LoRA, each description states its `baseModel`, and the Agent checks the
combination. The two cannot both hold.

**The latter was taken.** A tool schema is built at tools/list, where no `model` value exists yet.
`Caps(model)` is per (provider, model) and could technically narrow the list, but narrowing it
means **only the LoRAs that fit whatever is warm are visible**, and a LoRA for another checkpoint
the caller may perfectly well name disappears without a word. That is precisely what decision 5
forbids.

So the enum is every enabled LoRA, each line names its family (`watercolor-v2（sdxl 用）— …`), and
a pairing that cannot work is refused by name while the request is assembled. The second half of
the completion definition is met as "**a mismatched pair produces no picture and an explanation**"
rather than as "it is missing from the enum".

### Left for hardware (untouched here)

- **The same prompt and seed producing a different picture with the LoRA than without** — the
  body of P3's completion definition. Hardware only.
  🔴 This is the dangerous one, because 残作業 5's lesson applies unchanged: **a golden fixture
  pins the shape of a graph nobody has run**, which is not a proof of correctness. SD3.5 failed
  exactly there. To avoid the same rut, `LoraLoader`'s node definition was read off ComfyUI
  v0.34.0's own source (`nodes.py`: required inputs `model`, `clip`, `lora_name`,
  `strength_model`, `strength_clip`; returns MODEL and CLIP). That still is not "it ran".
- 🔴 **`lora_name` enumerates `<models>/loras` RECURSIVELY, each entry a path relative to that
  directory** (`recursive_search` in `folder_paths.py`). The instance links `/ComfyUI/models` to
  `/models/image` (`60-engines.yaml`), so `image/loras/x.safetensors` is listed as
  `x.safetensors` — which is the basename the Agent sends. **A key nested one level deeper is
  listed as `sub/x.safetensors` and a basename would be rejected as `Value not in list`.** LoRAs
  landing flat under `image/loras/` is the premise.
- **The sidecar sync already works** (verified in this pass, no code changed). The fetch sidecar
  in `60-engines.yaml` puts `.loras[]?` into `keys.start`, and `buildEngineActiveSet` publishes
  the enabled LoRAs as bare S3 keys. There is **no "the LoRA never reaches the instance" gap**; what
  is unverified is only whether ComfyUI enumerates them once they are there.
- **The llm role's preset-pinned LoRAs and virtual model ids** (decision 5's second half) are
  untouched.
- So is sd-server's `<sd_cpp_extra_args>` route, on the body's own condition: only when an
  `ImageEngine=sdcpp` deployment needs it, and only after open question 2 is measured.

### Fixed alongside — 欠落 9

`/history`'s 503 is now waited out and retried (a separate commit in the same pass). One state a
retry cannot recover was added with it: a restarted ComfyUI holds no history for a prompt id the
previous process accepted, so once `engine_waking` has been seen, a 200 that does not carry this
prompt is reported at once as a lost queue. Adding the retry alone would have turned 欠落 9 from
"says retry and does not retry" into **"stays silent for sixteen minutes"** — the poll would keep
receiving 200s and report nothing until the request's whole budget ran out.

## Follow-up — the ingest form was rendered for the first time (2026-09-10)

This closes the last line of "P2's remaining work 4 and 5": **"not measured: what the Console's
ingest form actually renders as"**. **The wire is unchanged.** One defect was found by looking,
and only that was fixed.

**Why it could not be reached before.** The admin modal's position is React state, not
localStorage (`rootSection` in `AdminTab`), so "already open" cannot be seeded — the only road is
to actually click **account menu → Admin → "Inference engines" in the left rail**. With that
known the rest is the existing README harness: copy `console/scripts/shots/server.mjs`, set
`super_admin` on `/api/tenants`, add fixtures for `/api/admin/engines` and `…/ingest`,
`…/ingest/files`, `…/ingest/resolve`, `…/ingest/search` and `…/hf-token`, and let headless
Chromium (raw CDP) draw the **real `npm run build` bundle**. **The harness is not in the
repository** — it is a throwaway under `~/.cache`.

**Seen for the first time** (the image role on comfy, the llm role on llama.cpp):

- The ingest road runs end to end: repository → "look it up" → the file listing → a pick →
  resolve → accepting the licence → "ingest". The id is proposed as `flux1-dev` from
  `flux1-dev-fp8.safetensors`, with the quantisation tag dropped.
- **Decision 2's hint behaves as designed.** The `Flux.1 D` that `resolve` returned does **not**
  go into the picker; it rides beside it as "the repository calls this 'Flux.1 D'". The picker's
  options are `base_models` (`sdxl` / `flux1` / `flux2` / …), i.e. the CP's spelling.
- **Decision 3's pair holds on the real bundle.** With `context_length: 262144` from the llm
  role's resolve, the window fills with 262144 and the output-cap select lands on "1/8 (32768)".
  Half-filling the pair is not reachable.
- The register form's split model: `file_flags` becomes the "part" select, one bordered block per file.
- The red non-commercial sentence, and the repair select on a row with no family.

**The one defect, and it needed a render.** The forms' label column is a fixed `8ch`, which at
12px is **61px**. Measured, **「コンテキストウィンドウ」 and "licence URL (optional)" each wrap
to THREE lines**; with `align-items: center` the shorter input then floats halfway down a label
taller than itself and the form goes ragged. That is over the budget the CSS itself states ("wraps
to two lines"). **Widened to `10ch` (76px)** — measured across ja/en × image/llm: every label
fits in two lines, nothing overflows.

**Three false defects the harness itself produced** (each looked like a broken screen):

- **The output cap looked stuck at empty.** The harness's fault: it grabbed the second `<select>`
  **by index**, and the llm role has no family picker, so that was the output cap. Setting a value
  no option carries leaves `selectedIndex = -1`, and the `change` handler writes the empty string
  back. **Grabbed by label, it fills correctly.** A harness that addresses the DOM by index lies
  on a screen whose fields differ per role.
- **A clipped screenshot painted a glyph from elsewhere at the clip's left edge.** An artifact of
  `Page.captureScreenshot`'s `clip`: `getBoundingClientRect` puts no element there, and a tighter
  re-capture of the same region is empty. **A smudge is settled by a rectangle or a re-capture,
  never by eye.**
- **🔴 rendered as tofu.** This container ships no emoji font at all (`fc-match 🔴` falls to
  DejaVu Sans); it says nothing about a user's browser. **Headless proves nothing about fonts.**

**Still not said**: how it reads against real Hugging Face / Civitai answers (the fixtures are
invented to the wire's shape), the refusal shown for a gated repository with no token, and phone
width.

## P3 and P2's remainder, on hardware (2026-09-11, dev deployment)

The image role was started twice as comfy and `generate_image` was called from a session driven
over REST. About 52 minutes of GPU time in total (g6.xlarge, the l4 rung). **The conclusion
first: P3's completion definition is met on the "the LoRA reaches the picture" side and NOT on
the "same seed" side — and cannot be met there. P2's remainder (edit / inpaint) could not be put
on hardware at all, because the deployment predates the commit.**

### P3 — the LoRA works on real hardware

`nerijs/pixel-art-xl` (170,543,052 bytes, `creativeml-openrail-m`, not gated) was ingested to
`image/loras/pixel-art-xl.safetensors`. The ingest task is Fargate, so it spends no G-series
vCPU, and it finished in **under 75 seconds**.

Same prompt (`a red fox sitting on a mossy rock in a misty forest at dawn`), same
`sdxl-base-1.0`:

- without the LoRA: `b86dc9971348f9cf8d0f7fd851a48d1e123c4f6a0bc364b3519bf3c910535fda` (1,495,544 bytes)
- with it at weight 1: `40ffa88ee2f4926e61313b24a3bc6bf810b8bff953309e427ab79e20d3902a61` (1,333,432 bytes)

🔴 **That is not a proof that the same SEED produced a different picture.** The provider picks a
fresh random seed per request (`comfyRandomSeed`, because ComfyUI caches a node's output by its
inputs) and ADR 0069's vocabulary has no seed field. So **the first half of the completion
definition is not expressible through today's `generate_image`**, and that line stays open until
a request can pin a seed.

What was proven instead is arguably stronger: **`LoraLoader`'s `lora_name` is an enumeration over
`models/loras`, not a free string** — the same shape as SD3.5's `clip_name1`. A name the instance does
not hold is refused at validation with `Value not in list`. So **a successful generation WITH the
LoRA simultaneously shows that (a) the file reached the instance from `image/loras/`, (b) the
basename matches the enumeration, and (c) `LoraLoader` actually ran.**

**The base-model refusal was confirmed on hardware too** (the refusal レビュー決定 5 moved into
the Agent):

```
comfy: LoRA pixel-art-xl was trained for the sdxl checkpoint family and flux2-klein-4b is
flux2-klein — they cannot be combined; a mismatched LoRA does not fail, it quietly does
nothing to the picture
```

No checkpoint switch happened: the refusal comes back while the request is being assembled and
never touches the GPU. The Phases section's "an SD1.5 LoRA does not appear in SDXL's enum" is met
in this **refusal-by-name** form, because decision 5's revision forbids narrowing the enum (see
the 2026-09-10 follow-up).

### 🔴 Where `imageProviderOrder` predates comfy, `auto` ranks the fleet's own engine LAST

This deployment's ui-prefs held `imageProviderOrder: ["sdcpp","agy","codex"]`. comfy is a
provider that appeared after that list was written, so `effectiveOrder` appends it — making
`auto` walk sdcpp → agy → codex → comfy. sdcpp does not exist on this deployment (the role is
comfy), while agy and codex are ready whenever their logins are. **A `generate_image` call that
omits `provider` therefore spends a member's plan before it ever reaches the GPU this deployment
pays for.**

Every call in this verification named `provider="comfy"` explicitly, so nothing here was affected
— but this is the silent version of exactly what `fallbackWarnings` was written for. Nobody
edited a setting: **a stored preference changed meaning on the day a provider was added.**

### P2's remainder (edit / inpaint) — not reachable on hardware

The deployed Agent is `0.18.1-dev-8eb6bc66`, which predates the commit adding image-to-image.
The deployment answered, honestly:

```
画像を生成できませんでした: no image provider can serve this request: comfy cannot do edit
```

This lane does not run deployments, so it stopped there. **The five families' edit / inpaint
graphs are pinned by shape alone** — the same state SD3.5 was in — and must not be described as
working until a later deployment puts them on a GPU.

### 欠落 7's window did not open on either cold start

A NON-start model (flux1-dev-fp8, 168 s of sync) was requested as soon as the engine could
answer. Both times it **succeeded**; no bare 400 appeared.

| | mode=on | running | first flux1 image |
|---|---|---|---|
| 1st | 15:47:18Z | 15:52:02Z (4 m 44 s) | 15:53:45Z (+103 s) |
| 2nd | 16:29:20Z | 16:40:32–16:40:59Z (~11 m 30 s) | 16:41:59Z (+60 s) |

The reason is plain: **acquiring and booting the instance (4 m 44 s, 11 m 30 s) takes longer than
syncing the remaining models** (P0 measured 12 files / 48 GB at about 270 s). By the time the
engine passes its health check, `keys.rest` is done. The second start took longer precisely
because it took a fresh container instance with a fresh EBS volume and re-synced everything — and
the window still did not open.

**This does not mean 欠落 7 is gone.** The window opens when the sync outlasts the start: a
deployment that lands on already-warm capacity, or a catalogue whose rest-set is much larger.
It is recorded here because **it is hard to hit in this deployment's default shape**, which is
information for whoever prioritises the fix. The fix belongs to another lane and was not touched.

### 🔴 The session used to drive this was not a measuring instrument

`generate_image` cannot be called from this workspace's own MCP, so an opencode managed session
was created on the dev deployment and driven over REST. Even instructed to copy tool output
verbatim and to make nothing up, it produced **three fabrications and one injected argument**:

- it pasted a single **identical 77-digit decimal** as the `sha256sum` of two files of different
  sizes (a clean re-run produced two correct, different hex digests);
- it wrote prompts into its summary that were never sent ("Cyberpunk cityscape", …);
- it pasted an error saying `sdxl` and `sdxl` "cannot be combined", a sentence the code cannot
  emit;
- across an auto-compaction it began **adding the `loras` argument that had been explicitly
  forbidden** to every call, which is what wasted one 欠落 7 observation: the injected LoRA made
  the pair a family mismatch, and the Agent refused it before the engine ever saw it.

Only two kinds of evidence were used: **tool-result JSON that is internally consistent** (paths,
byte counts, dimensions) and **the engine's own clock** (the nanosecond timestamps in the
generated file names). Any verification driven through a session needs that filter every time.

## Follow-up — a seed now reaches P3's completion definition (2026-09-11)

The 2026-09-11 hardware follow-up recorded that "**the same seed**" was not expressible through
`generate_image`. That is now closed: ADR 0069's vocabulary gained `seed` (an integer; random as
before when omitted), and the comfy provider feeds a pinned seed straight into each family's
sampler. The reasoning — and why a seed alone earns a place in a provider-neutral vocabulary — is
in **ADR 0069's follow-up, "`seed` joins the vocabulary"**.

Where P3's completion definition stands now:

- "an SD1.5 LoRA does not appear in SDXL's enum" — **met**, in the refusal-by-name form (the
  2026-09-10 and 2026-09-11 follow-ups);
- "the same prompt and the same seed produce a different picture with the LoRA than without" —
  **the seed can now be pinned; the confirmation on hardware is still outstanding.** It belongs
  to the next deployment pass, as H3, together with P2's remainder (the five families' edit /
  inpaint). Until then this line must not be written up as closed.

A second request with a pinned seed hitting ComfyUI's output cache (the same picture in half a
second) is reported in warnings **only when it actually happens** — decided from the engine's own
`execution_cached` message rather than guessed from a short elapsed time. The design reasoning is
in the same 0069 follow-up.

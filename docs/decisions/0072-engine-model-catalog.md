# 0072. A model catalogue for the engines — swap the LLM and SDXL models without a CloudFormation run, and pick LoRAs per request

English | [日本語](0072-engine-model-catalog.ja.md)

- Status: **draft (2026-09-08). Written to be reviewed before P0 is built.** Every number
  below is quoted from ADR 0071's measurements; the upstream facts (llama.cpp,
  stable-diffusion.cpp) were read the same day from the repositories' `tools/server/README.md`,
  `examples/server/api.md` and `docs/lora.md`. **Nothing was newly measured for this document**
  — what has to be measured before a decision can stand is listed under *Open questions*, and
  each decision names the question it depends on.

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
     load" as **success**: the llm role listens on an empty `--models-dir` (the router starts
     with zero models — open question 1 confirms this), the image role runs a `sleep infinity`
     placeholder and reaches RUNNING. The service stabilises, and the gateway **does not wake
     a role whose catalogue is empty** (`503 engine_unavailable`, "this role has no enabled
     model"). The parameters shrink to `LlmEnabled` / `ImageEnabled`.

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
     `diffusion_model`), `baseModel` (`sdxl` / `sd15` / `flux` / …: what a LoRA **fits**,
     declared by the operator at ingest), `ingestedAt`. "The file exists" is not "usable" —
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
     policy) because it is **a permission the CP already holds**, the value is small (a few KB),
     and the box does not need a live CP at the moment it reads.
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
     SSE comment every 10 seconds until the upstream's first byte (ADR 0071 decision 5), so if the
     router queues the request nothing new is needed. If it refuses instead (open question 1),
     the gateway calls `/models/load` and waits for `loaded` before forwarding — either way the
     wait never leaves the heartbeat.
   - This decision depends on open question 1 (four points). Should the router fall short, the
     fallback is "load the **one selected** entry of the active set with `-m`, and swap at the
     next start" (decision 4's shape) — per-model windows stay in the catalogue, only one model
     is resident at a time, and the catalogue's design does not change.

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
   - **When ComfyUI (ADR 0071 P2) arrives, the "one" in this decision disappears.** ComfyUI picks
     the checkpoint per workflow, so with the same catalogue and the same S3 layout `selected`
     becomes per request. That is why the catalogue is engine-agnostic: **sd-server's limit is
     not baked into the catalogue's shape**.

5. **A LoRA is a catalogue entry, chosen per request. The agent can only name what the
   catalogue has.**
   - **image.** The box syncs the **enabled** entries of `image/loras/` and starts with
     `--lora-model-dir /models/image/loras`. `generate_image` gains two arguments: `model` (enum =
     the enabled checkpoints; on the `sdcpp` provider that is **one today** — decision 4 — but the
     argument's shape allows several from the start, for the Codex / agy routes and for ComfyUI)
     and `loras: [{name, weight}]` (`name`'s enum = only the LoRAs whose **`baseModel` matches the
     selected checkpoint**; `weight` 0–2, default 1). The tool description lists the catalogue's
     `description` lines, so the agent can choose "`watercolor-v2` for a watercolour look".
     - The Agent's `sdcpp` provider appends
       `<sd_cpp_extra_args>{"lora":[{"path":"<file>","multiplier":<w>}]}</sd_cpp_extra_args>` to
       the prompt. **The gateway is not involved** (still verbatim — decision 4 of ADR 0071 reads
       the body for usage only). ⚠️ **A prompt that already contains `<sd_cpp_extra_args>` is
       refused**: this hole carries not only `seed` and `sample_steps` but `lora.path`, a file path
       on the server. The prompt is written by the model, and anything the model has read can end
       up in it (the stance of ADR 0071 decision 4(d)).
     - A LoRA whose `baseModel` does not match (an SD 1.5 LoRA on SDXL) either produces a
       **silently broken picture** or is ignored with a tensor-name warning; to the user both
       read as "did nothing". So **the Agent does not offer it in the enum and the CP refuses it in
       the request**.
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
   - **starts the ingest task with `ecs:RunTask`**. IAM: the CP role needs `ecs:RunTask` (limited
     to the ingest family) and `iam:PassRole` (the ingest task role and the execution role). This
     is the **first breach** of ADR 0071 decision 8's "zero CP IAM additions", but it lives in an
     `AWS::IAM::Policy` inside `60-engines` (`Roles:` = the role name cut out of `20-platform`'s
     `CpTaskRoleArn`), so **a deployment without the stack gains nothing**. The subnets and SG
     RunTask needs are written by `60-engines` into the engine table's `ingest` block;
   - follows completion with `DescribeTasks`, and creates the `engine_models` row only after the
     `upload` container wrote the manifest (decision 2), as `enabled: false` — **ingested is not
     offered**; an admin enables it. In progress and failure reasons (sha256 mismatch, 401, disk)
     show on the panel's row.
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
     actions are enable/disable, select, default, delete (which also deletes the S3 files —
     **manifest first**: a file deleted before its manifest stays a candidate whose sync then
     fails). ADR 0071 decision 13's "models are the stack's declaration" becomes "models are
     the catalogue's declaration", and `warm` means what decision 3 says.
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

## Options rejected

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

## Open questions — measure 1 and 2 before P0, 3 before P1

1. **The router's four points** (decision 3 depends on them): (a) does it start on an empty
   `--models-dir` and answer `/health` ok (decision 1's "stable while empty" also depends on
   it); (b) is a request during autoload queued or refused, and if queued, does the heartbeat
   cover the silence to the first byte; (c) with `--models-max 1`, does a request for a second
   model unload the first and load the second (without crashing); (d) do `usage` (with
   `stream_options.include_usage`) and the response's `model` arrive through the router
   (decision 8). (a), (b) and (d) can be measured in this container with the 1.1 GB CPU model;
   (c) needs a GPU.
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
5. **`engine_models` as a table or as a settings-store value.** Tens of rows, read per
   request — one JSON in `SettingsStore` would do. The table exists for `lastUsedAt`, written
   on every request; whether that is an acceptable write rate for the settings store decides it
   (`engine_<key>_demand_at` is throttled to once a minute for that reason).

## Phases

- **P0 — the catalogue's foundation.** `engine_models` and the seed, manifests, the S3 layout
  migration, the active set in SSM and a role-independent fetch sidecar (one script: sync,
  preset, command line), the shrink to `LlmEnabled` / `ImageEnabled` with the old parameters
  kept for one release, the admin API (list, enable, select) and the panel's `models[]`,
  `/internal/engine/catalog` from the catalogue plus the `catalog-changed` push, `sdcpp`'s
  `Caps.Sizes` from the declaration. **Definition of done: re-select the image checkpoint in
  the Console and, with no CloudFormation touched, the next start returns a picture from the
  new one — the stack's last-updated timestamp in `describe-stacks` has not moved.**
- **P1 — llm in router mode.** After open question 1. Preset generation, per-model windows,
  `LlmModelsMax`, the redefinition of `warm`, opencode's provider with per-model `limit`.
  **Definition of done: two `llamacpp/` models in the launch menu, each usable in turn, and the
  reload of a switch answered on the first attempt** (ADR 0071 P0's observation, taken across a
  switch).
- **P2 — LoRA.** After open question 2. The image role's `loras/` sync, `generate_image`'s
  `model` / `loras`, `sdcpp`'s `<sd_cpp_extra_args>` and the refusal rules; fixed preset LoRAs
  for llm. **Definition of done: the same prompt and seed give a different picture with and
  without the LoRA, and an SD 1.5 LoRA does not appear in the enum on SDXL.**
- **P3 — ingest from the Console.** The `ingest` API, the RunTask IAM (inside `60-engines`),
  HF sha256 / licence resolution, the licence-acceptance UI, progress and failure display,
  Civitai (after open question 4). Until then, `harness/ingest-model.sh`.
- **P4 — syncing into a running box (open question 3), virtual model ids for llm (the second
  half of decision 5), ComfyUI on the same catalogue (ADR 0071 P2).**

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

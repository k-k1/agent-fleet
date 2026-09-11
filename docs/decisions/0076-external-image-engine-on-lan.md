# 0076. Image generation against a ComfyUI on the LAN, for native / docker deployments — an "external" row in the engine table, the gateway unchanged

English | [日本語](0076-external-image-engine-on-lan.ja.md)

- Status: **proposed** (2026-09-11). Nothing is implemented.
- **Nothing was measured for this document.** Every claim says where it comes from —
  (a) measurements in ADR 0069, 0071 and 0072, (b) facts read out of this repository's code on
  2026-09-11 (listed file:line under "Sources checked"), (c) things known only as ComfyUI's
  published behaviour and **not confirmed on this deployment** (that `/system_stats` answers 200
  while a model loads; how a loader lists a file in a subfolder). Decisions that rest on (c) are
  named under "Open questions".
- The user's request is one sentence — **`generate_image` only works on an ecs-ec2 deployment
  today; make it talk to a ComfyUI running on the local network under the native / docker
  runtimes.**

## Background

### What is tied to ecs-ec2 (read on 2026-09-11)

ADR 0071 buys the GPU box through ECS Managed Instances, ADR 0072 declares what that box loads
through a catalogue. The image path has three stages, and **only the middle one, registration,
is tied down**.

1. **The Workspace side does not know the runtime.** The `comfy` provider of `generate_image`
   goes nowhere but `AF_CP_BASE_URL` plus the `base_url` the CP returned (`/engine/image/v1`),
   authenticated by the engine session token the CP mints. No AWS SDK, no S3 reference: it drives
   ComfyUI's `/prompt` → `/history/<id>` → `/view` through the CP. Whether the far end is ECS or
   a LAN host **cannot be told from this layer, and need not be**.
2. **The CP gateway never touches ECS once health passes.** `ensureReady` probes
   `engineHealthy` first and forwards on success. ECS is called only when the engine is not up,
   from `ensureStarted` — **one function** — which uses `DescribeServices` and `UpdateService`.
   Authentication, catalogue publication, usage accounting and the comfy path rule (no `/v1/`
   prefix) assume nothing beyond "there is one upstream HTTP URL".
3. **Registration attaches ECS unconditionally.** `parseEngineTable` requires `service` on every
   row; `newEngineRegistry` loads the AWS config, attaches an `engineECS` adapter and an
   `engineController` to every row, publishes the SSM active set, wires the `pending` watch and
   the GPU class ladder (ADR 0074). The inline `AF_ENGINES_JSON` already exists "for a dev CP
   with no AWS at all" but is undocumented, and under native it makes the controller log a failed
   `DescribeServices` every tick.

### The precedent that already exists — VOICEVOX as "externally managed"

ADR 0013 made VOICEVOX "a URL the CP points at" (`AF_VOICEVOX_URL`); ADR 0070 put it on demand
under ECS. **The branch between the two is one function**: `newTTSEngineFromEnv` returns nil
without `AF_TTS_ECS_SERVICE`, no controller and no demand counter are attached,
`engineMode(v, managed=false)` defaults to `on`, and the admin row reports `managed:false` and
**omits** the ECS-derived fields (state / desired / box / stop_eta). The Console's engines panel
already draws `managed:false` as "externally managed". The native README already documents
pointing a CP inside WSL at a VOICEVOX on the Windows side. **This ADR copies that shape onto the
image engine.**

### Network assumptions

- The CP of a docker deployment runs with `network_mode: host`, so `192.168.x.y:8188` is
  reachable as is. Under native the CP is a host process.
- Workspace → CP is the existing `PUBLIC_BASE_URL` path; `/engine/*` is session-exempt like
  `/mcp`. **Nothing is added to `no_proxy` or to the egress allowlist** (ADR 0071 decision 4 (a)).
- 🔴 A docker Workspace container, however, NATs out of its private bridge onto the LAN. Under
  ECS "the engine's SG admits only the CP" was the defence; **on a LAN, reachability is not a
  defence** (decision 7).

## Decisions

### 1. An external engine is one row of the engine table, **declared** by `lifecycle: "external"`

`engineDef` gains `lifecycle`. Empty (the default) is the ECS-managed row as today; `external`
is "a row that owns neither start nor stop, only a URL". Reading an empty `service` as external
was rejected (ADR 0053: declare, do not derive). An external row gets **none** of: the ECS
adapter, the controller, the SSM active set, the `pending` watch, the GPU class ladder, the
uptime sampler. The admin API reports it as `managed:false` and **omits** the ECS-derived
fields, exactly as VOICEVOX does (nothing is guessed). `parseEngineTable` waives `service` for
external rows only.

### 2. The operator's entry point is one variable, `AF_COMFY_URL`; the CP **synthesises** the row

Same shape as `AF_VOICEVOX_URL`. From `AF_COMFY_URL=http://192.168.1.20:8188` the CP builds
`{key:"image", api:"images", provider:"comfy", health:"/system_stats", lifecycle:"external"}`.
An optional `AF_COMFY_API_KEY` becomes the `Authorization: Bearer` the gateway presents upstream
(ComfyUI itself has no authentication; this is for a reverse proxy in front of it — decision 7).
`AF_ENGINES_JSON` stays as the dev carrier and may also carry `lifecycle`. When the table and
the environment both declare the same `key`, **the environment wins and a log line says so** —
the declaration closest to the operator is taken. The table is read once at startup (ADR 0071's
design), so changing the URL is a CP restart. Entering the URL from the admin panel is
**deferred**, not rejected (see "Rejected").

### 3. The Workspace goes through the CP gateway; the Workspace side is unchanged

ADR 0071 decision 4 stands. Its reasons (a) no new path or allowlist entry, (c) the CP counts
usage, (d) ComfyUI has unauthenticated mutating endpoints (`/prompt`, `/upload/image`) all hold
on a LAN. Only (b), "a place to hold the request while the box wakes", stops applying (decision
4). The `comfy` provider's five workflow families (sdxl / sd35 / flux1 / flux2-klein / zimage)
are used as they are.

### 4. There is nothing to wake, so `ensureStarted` **fails immediately** for an external engine

`ensureReady` forwards as soon as health passes (as today). When it does not, a managed row
buys a box and waits; an external row has no reason to wait — **a LAN ComfyUI whose health is
down will not come up because we waited**. It fails at once, and the non-streaming path answers
`503 engine_unavailable`. Not `engine_waking`: the provider retries that code for 16 minutes
(ADR 0071 decision 5), a budget sized for "buy a box and pull from S3", which against a dead box
is 16 minutes of silence. The body names the URL and the health path — it is text an operator
reads. Health is `/system_stats`, as in 60-engines; ComfyUI answers it while a model loads and
`/prompt` queues, so once health passes there is nothing to wait for (open question 1).

### 5. Two modes, `on` / `off`; the default is `on`

The `on` default of `engineMode(v, managed=false)` is used as is. A stored `ondemand` reads as
`on` (there is no box to switch off). The admin API refuses `ondemand` for an external engine
with 400, and the Console does not render the `ondemand` button on such a row. `off` means what
it means for VOICEVOX — routing is closed — not that ComfyUI is stopped.

### 6. Models are declared by hand in the catalogue; file names are what ComfyUI's loaders list

ADR 0072 decision 1 (the catalogue is the whole declaration) stands. There is no ingest job (no
S3), so an administrator registers id, `base_model` and files through the existing
`POST /api/admin/engines/image/models`. The `files_missing` check asks whether each role's flag
is declared, not whether an S3 object exists, so it applies unchanged. The wire field `s3Key`
keeps its name — for an external engine it carries "the name handed to the loader".

🔴 **Known trap**: the Agent's `engineImageFiles` hands ComfyUI only what follows the last `/` of
the S3 key. The ingest path was flat, so this passed; a LAN ComfyUI with
`checkpoints/sdxl/x.safetensors` in a subfolder gets `x.safetensors` and fails with
`Value not in list`. P0 documents "put files directly under the type folder (`checkpoints/`,
`diffusion_models/`, `clip/`, `vae/`, `loras/`)"; P1 changes the rule to "strip up to and
including the type folder" (open question 2). Reading `/object_info` to offer candidates is
also P1.

### 7. Reachability is not a defence; on a LAN the operator owns the network

Under ECS the security group guaranteed "only from the CP". The CP has no equivalent on a LAN,
and **the documentation says so**. It states that a docker Workspace container can reach ComfyUI
directly through NAT. Two remedies are offered to an operator who wants it closed: egress
enforcement (an RFC1918 address not on the allowlist is refused), or a reverse proxy in front of
ComfyUI that checks a bearer, handed to the CP alone through `AF_COMFY_API_KEY`.

### 8. Usage stays `tool.imagegen` / provider `comfy`; no cost is attached; `warm` on the panel is a liveness probe

The Agent's counting of images and pixels is unchanged (ADR 0069 decision 9, ADR 0071 decision
9). A LAN box has no hourly price, so no cost is shown. With no controller, an external engine's
`warm` is the result of one health probe at the moment the panel is opened (one HTTP call, 5 s
cap). The uptime heatmap is empty in P0 — the sampler is the controller's tick; a light prober
writing health alone every 30 seconds is P1.

## Rejected

- **The Workspace talks to the LAN ComfyUI directly (`AF_COMFY_URL` in the Workspace
  environment).** It puts a LAN address into `no_proxy` and the egress allowlist, exposes
  ComfyUI's unauthenticated mutating API to every session, the `base_model` catalogue lives only
  in the CP's database, and usage cannot be collected. ADR 0071 decision 4's reasons (a)(c)(d)
  are the rejection verbatim.
- **Register ComfyUI as an MCP server.** Community ComfyUI MCP servers exist, but they bypass
  the fleet's `generate_image` (provenance, one tool across every CLI, the artifact card) and
  give each CLI a different tool. Against ADR 0069's "one provider abstraction".
- **Read an empty `service` as external.** A forgotten field in the table would turn into
  "externally managed". ADR 0053 says declare; `lifecycle` is written out.
- **The CP owns the LAN ComfyUI's start and stop (drive systemd / docker from the CP).** ADR
  0070's shape carried onto a LAN. The owner is different — as with VOICEVOX's "standing docker",
  the lifecycle belongs to whoever runs it, and an idle GPU's electricity is orders of magnitude
  from ECS's $1.26/hour.
- **Enter URL and key from the admin panel (encrypted in the database).** The MCP registry is a
  precedent and it would avoid a restart, but it needs a Console form, encryption and a
  connection test, and multiplies P0 several times. **Deferred**, not rejected: added when one
  environment variable starts to hurt operators.

## Decisions overridden, decisions kept

- ADR 0071 decision 5 (wake and hold) **applies to managed rows only** — decision 4 replaces it
  for external rows.
- ADR 0071 decision 4 (through the gateway) and 9 (usage), ADR 0072 decision 1 (the catalogue is
  the declaration) and 4 (ComfyUI workflow templates), ADR 0069's single provider abstraction:
  kept.
- ADR 0071 decision 13's "never ask the engine what it holds (it is asleep)" loses its premise
  for an external engine, which is always awake. P0 still does not ask (decision 6); P1's
  `/object_info` helper is framed as "offering candidates", not "declaring", to stay inside ADR
  0053.

## Open questions (measure before deciding)

1. **Does `/system_stats` answer 200 while a model loads** (decision 4 depends on it)? Understood
   so from the published behaviour, unconfirmed on this deployment. If not, health moves to
   `/queue` or `/`.
2. **How a loader lists a file in a subfolder** (decision 6 depends on it). If it is
   `sdxl/x.safetensors`, "strip up to the type folder" is the whole rule.
3. **Passing a secret from `.env`.** `AF_COMFY_API_KEY` would sit in plain text in compose's
   `.env`. The OIDC secrets already live there, so it follows the convention, but decision 2
   keeps it optional.
4. **Running the fleet's pinned ComfyUI image on a GPU host under the docker runtime (P2).**
   `deploy/aws/ecs/comfyui/Dockerfile` is a plain ComfyUI that reads models from a bind mount, so
   a `run-voicevox.sh`-style script with `--gpus all` should do; unconfirmed.
5. **The size of `/view` answers over the LAN.** The gateway's non-streaming path reads the whole
   answer into memory; a 2048px FLUX output has not been measured.
6. **ComfyUI Desktop's (Windows) listen setting.** The server build takes `--listen`; the Desktop
   build's setting name was not checked. The native README documents the server build and leaves
   Desktop open.

## Phases

- **P0 (CP and documentation)**: `engineDef.Lifecycle`; synthesis from `AF_COMFY_URL` /
  `AF_COMFY_API_KEY`; a `newEngineRegistry` that does not load the AWS config when no row needs
  it; `ensureStarted` failing at once; the admin row's `managed:false` and `url`; `ondemand`
  refused; the Console omitting the `ondemand` button. Fixed alongside: the Agent's
  `serviceLabelOf` / `driverModelOf` have no `comfy` case, so the tool description shows neither
  the route's label nor its model (the same on ECS). Documentation: `deploy/compose/.env.example`,
  `deploy/native/README.md` (next to the VOICEVOX section), a new section under `guide/operate/`,
  an "image generation" row in `guide/ref/deploy-targets.md`'s matrix, and the wrong pointer in
  `guide/ref/features.md`. Tests: table parsing, registry construction without AWS, the gateway
  against an httptest ComfyUI stub (upstream up → forwarded; upstream down → immediate
  `engine_unavailable`), the admin row. **The completion criterion is one real run — one image
  back from `generate_image` against a LAN ComfyUI**: green benches have failed to measure the
  wiring before (ADR 0072 P2).
- **P1**: the basename rule; a health prober and the uptime heatmap; candidates from
  `/object_info`; the reverse-proxy procedure for `AF_COMFY_API_KEY`.
- **P2**: a script that runs the pinned image on a GPU host under docker (open question 4).
- **P3**: the same mechanism for `AF_LLM_URL` (a LAN llama-server / Ollama, the chat role).
  `warmPath` `/models` is llama.cpp-router specific and must be empty. Outside this ADR.

## Sources checked (2026-09-11, this repository's code)

- The Workspace goes only through the CP: `workspace/agent/engines.go:169-172` (`AF_CP_BASE_URL`,
  `AF_ENGINE_ISSUE_TOKEN`), `:395-404` (the `api=images && provider` match and
  `base + base_url`), `workspace/agent/internal/imagegen/comfy.go:61-75` (Ready is "URL and
  token exist").
- The gateway's ECS coupling is one function: `control-plane/engine_gateway.go:813`
  (`ensureReady`), `:837` (`ensureStarted`), `:756` (no `/v1/` for comfy).
- Registration's coupling: `control-plane/engines.go:365` (`service` required), `:382-395` (AWS
  config loaded first), `:453-459` (`engineECS` on every row), `:471` (controller),
  `engine_catalog.go:519` (no-op without SSM).
- The VOICEVOX precedent: `control-plane/engine_ecs.go:376` (`newTTSEngineFromEnv`),
  `engine_control.go:68` (`engineMode(v, managed)`), `tts.go:128-165`, `engine_admin.go:196`
  (`managed`), `:278` (ECS fields omitted when `e.ecs == nil`),
  `console/src/features/settings/admin/adminEngines.tsx:2772` (`admin.tts_external`).
- Network: `deploy/compose/docker-compose.yml:23` (the CP on the host network),
  `control-plane/internal/runtime/runtime_docker.go:280-284` (a Workspace NATs out),
  `control-plane/workspace_lifecycle.go:378` (`AF_CP_BASE_URL = PUBLIC_BASE_URL`),
  `control-plane/egress_proxy.go:144-155` (RFC1918 is not blocked).
- Traps: `workspace/agent/engines.go:481` (basename), `workspace/agent/internal/imagegen/http.go:165-196`
  (no `comfy` case).
- The health path: `deploy/aws/ecs/cfn/60-engines.yaml:889` (comfy is `/system_stats`).

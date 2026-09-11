# 0076. Image generation against a ComfyUI on the LAN, for native / docker deployments — an "external" row in the engine table, the gateway's forwarding path unchanged

English | [日本語](0076-external-image-engine-on-lan.ja.md)

- Status: **proposed, reviewed** (2026-09-11). Nothing is implemented. The review (last section)
  checked every claim against the code and found no premise that fails; its corrections are
  folded into the decisions below and P0 may start.
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

What an external row's `nil`s meet, read from the code (review): most sites that would touch
what the row lacks are already nil-safe — `demand.record` / `units`, `ctrl.warmed` /
`noteAdminAction`, `ecs.logKey`, `controlCfg`, `pendingGuard` (returns on a nil `pending`),
`publishActiveSet` (no-op without SSM), the admin row's `e.ecs == nil` and `e.demand != nil`
branches, `classStartHeld` with no ladder, and `servedModel` (the Workspace's `warm` model is the
one the gateway last saw answer, controller or not). The two that are not — `ensureStarted`'s
`e.ecs.view` and the admin `put`'s start/stop — are exactly what decisions 4 and 5 change. P0
pins this with one test that drives every handler against an external row: the admin list, `put`,
the models routes, the gateway with the upstream up and down, and `/internal/engine/catalog`.

### 2. The operator's entry point is one variable, `AF_COMFY_URL`; the CP **synthesises** the row

Same shape as `AF_VOICEVOX_URL`. From `AF_COMFY_URL=http://192.168.1.20:8188` the CP builds
`{key:"image", api:"images", provider:"comfy", health:"/system_stats", lifecycle:"external"}`.
An optional `AF_COMFY_API_KEY` becomes the `Authorization: Bearer` the gateway presents upstream
(ComfyUI itself has no authentication; this is for a reverse proxy in front of it — decision 7).
It lands in the row's existing `apiKey` field — the one a managed row reads from SSM through
`apiKeyParam` — which `dial` and `engineHealthy` **both** already present as a bearer, so there
is no new plumbing; the consequence is that the proxy has to admit `/system_stats` under the same
bearer, or health never passes. `AF_COMFY_URL` joins the gate in `newEngineRegistry` that today
returns nil unless `AF_ENGINES_SSM_PARAM` or `AF_ENGINES_JSON` is set. `AF_ENGINES_JSON` stays as
the dev carrier and may also carry `lifecycle`.

When the table and the environment both declare the same `key`: the environment wins over a
table row that is absent or itself external, and **a managed table row wins over the
environment**, with a log line either way. The review reversed the draft's "the environment
always wins": replacing a controlled row would leave the ECS service it names running with
nobody to stop it, which is the more expensive mistake, and an operator who wants the LAN box
instead removes the role from the stack.

The SSM table is re-read every 10 seconds (ADR 0074's `engineTableReloader`), but an environment
variable is read once, so changing the URL is a CP restart. The reloader **skips external rows**:
otherwise it would log "changed in the table — restart" on every table change, and try to carry
a GPU ladder onto a row that has none. Entering the URL from the admin panel is **deferred**, not
rejected (see "Rejected").

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
reads. No new error code: the non-streaming path already turns any dial error that is not
`errEngineWaking` into `503 engine_unavailable` with the error's text appended, and the stream
path into an `engine_unavailable` event; what P0 adds is the immediate return and the text.
Health is `/system_stats`, as in 60-engines; ComfyUI answers it while a model loads and
`/prompt` queues, so once health passes there is nothing to wait for (open question 1).

### 5. Two modes, `on` / `off`; the default is `on`

The `on` default of `engineMode(v, managed=false)` is used as is. A stored `ondemand` reads as
`on` (there is no box to switch off). The admin API refuses `ondemand` for an external engine
with 400, and the Console does not render the `ondemand` button on such a row. `off` means what
it means for VOICEVOX — routing is closed — not that ComfyUI is stopped.

The contract between the two sides, so that they can be built apart: the admin row of an
external engine carries `managed:false`, `lifecycle:"external"`, `url` and `warm`, and omits
`state` / `desired` / `box` / `stop_eta` / `idle_secs` / `window_*`. On the Console side the
mode segment renders `off` / `ondemand` / `on` for every row today, while `engineStateLabel` and
`engineStateTone` already branch on `managed` (the "externally managed" string is the TTS
row's, reused); the panel's poll condition reads `state`, which an external row never has, so it
does not poll.

### 6. Models are declared by hand in the catalogue; file names are what ComfyUI's loaders list

ADR 0072 decision 1 (the catalogue is the whole declaration) stands. There is no ingest job (no
S3), so an administrator registers id, `base_model` and files through the existing
`POST /api/admin/engines/image/models`. The `files_missing` check asks whether each role's flag
is declared, not whether an S3 object exists (`engineMissingFileFlags`; the models route says of
itself "nothing here verifies that the S3 key exists"), so it applies unchanged. The wire field
`s3Key` keeps its name — for an external engine it carries "the name handed to the loader".

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
`warm` is the result of one health probe at the moment the panel is opened — one HTTP call with
a **2 second** cap, and the answer cached for 10 seconds. Not the gateway's 5 seconds: the list
handler is synchronous and the Console re-reads it on every load, so a LAN box that is down
would otherwise hold the whole panel for 5 seconds per external row. The uptime heatmap is empty
in P0 — the sampler is the controller's tick; a light prober writing health alone every 30
seconds is P1.

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

- **P0 (CP and documentation)**, in four lanes that touch disjoint files:
  - *CP* (`control-plane/`): `engineDef.Lifecycle`; synthesis from `AF_COMFY_URL` /
    `AF_COMFY_API_KEY` with the precedence of decision 2; a `newEngineRegistry` that does not
    load the AWS config when no row needs it; the reloader skipping external rows;
    `ensureStarted` failing at once; the admin row of decision 5's contract; `ondemand` refused
    with 400; the 2 s cached health probe. Tests: table parsing, registry construction without
    AWS, the gateway against an httptest ComfyUI stub (upstream up → forwarded; upstream down →
    immediate `engine_unavailable` naming the URL), and the nil-walk of decision 1.
  - *Console* (`console/src/features/settings/admin/adminEngines.tsx` and the catalogues): no
    `ondemand` button on a `managed:false` row, the URL shown, the ECS-only fields (idle, box,
    heatmap) not drawn for it; a dom test on a row of decision 5's shape.
  - *Agent* (`workspace/agent/`): the existing hole fixed alongside — `serviceLabelOf` /
    `driverModelOf` have no `comfy` case, so the tool description shows neither the route's
    label nor its model (the same on ECS).
  - *Documentation*: `deploy/compose/.env.example`, `deploy/native/README.md` (next to the
    VOICEVOX section), a new bilingual page `guide/operate/07-image-engine.md` linked from that
    shelf's README, an "image generation" row in `guide/ref/deploy-targets.md`'s matrix, and the
    row in `guide/ref/features.md` that points "the inference engines' GPU class" at the MCP /
    egress page.
  **The completion criterion is one real run — one image back from `generate_image` against a
  LAN ComfyUI**: green benches have failed to measure the wiring before (ADR 0072 P2). That run
  needs a network with a ComfyUI on it, which only the operator has; it is theirs, not a
  session's.
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
  (`ensureReady`), `:837` (`ensureStarted`), `:803` (no `/v1/` for comfy), `:697` and `:726`
  (every non-waking dial error is already `engine_unavailable`).
- Registration's coupling: `control-plane/engines.go:365` (`service` required), `:382-395` (AWS
  config loaded first), `:498` (`engineECS` on every row), `:528` (controller),
  `engine_catalog.go:519` (no-op without SSM), `engine_table_reload.go:60` (the reloader is nil
  without SSM, and re-reads the table every 10 seconds with it).
- Already nil-safe (decision 1): `control-plane/engine_control.go:143` (`demand.record`), `:587`
  (`ctrl.warmed`), `engine_ecs.go:429` (`logKey`), `engines.go:284` (`servedModel`),
  `engine_gateway.go:516` (`pendingGuard`), `engine_class.go:831` (`classStartHeld`).
- The VOICEVOX precedent: `control-plane/engine_ecs.go:446` (`newTTSEngineFromEnv`),
  `engine_control.go:68` (`engineMode(v, managed)`), `tts.go:128-165`, `engine_admin.go:196`
  (`managed`), `:297` (ECS fields omitted when `e.ecs == nil`),
  `console/src/features/settings/admin/adminEngines.tsx:2914` (`admin.tts_external`, reached for
  any row), `:548` (the mode segment renders `ondemand` for every row).
- The catalogue check is flag-based: `control-plane/engine_admin.go:167` (`files_missing`), `:908`
  ("nothing here verifies that the S3 key exists").
- Network: `deploy/compose/docker-compose.yml:23` (the CP on the host network),
  `control-plane/internal/runtime/runtime_docker.go:280-284` (a Workspace NATs out),
  `control-plane/workspace_lifecycle.go:378` (`AF_CP_BASE_URL = PUBLIC_BASE_URL`), `:401`
  (`AF_ENGINE_ISSUE_TOKEN` is injected on every runtime),
  `control-plane/egress_proxy.go:144-155` (only loopback and link-local are refused outright).
- Traps: `workspace/agent/engines.go:481` (basename), `workspace/agent/internal/imagegen/http.go:165-196`
  (no `comfy` case).
- The health path: `deploy/aws/ecs/cfn/60-engines.yaml:943` (comfy is `/system_stats`).
- The wrong pointer: `guide/ref/features.md:124`.

## Review (2026-09-11, before P0)

In the manner of 0071's review: does each decision follow from its evidence, and does the code
say what the draft says it says? The verdict first: **P0 may start.** No premise fails — the
three-stage picture (the Workspace blind to the runtime, the gateway's ECS coupling in one
function, registration attaching ECS to every row) is what the code does, and the VOICEVOX
precedent is one function as claimed. What the review changed is folded into the decisions
above; this section records what was found and why.

- **Six of eighteen `file:line` citations pointed at the wrong line** (`engines.go:453`,
  `engine_ecs.go:376`, `engine_admin.go:278`, `adminEngines.tsx:2772`, `60-engines.yaml:889`,
  `engine_gateway.go:756` a comment away). Corrected above, each re-read with `sed -n`. A
  citation that names a line nobody can find is worse than none: the next reader stops trusting
  the ones that are right.
- **Decision 2 said the table is read once at startup.** It was, until ADR 0074: the reloader
  now re-reads SSM every 10 seconds. The draft's conclusion (a URL change is a restart) still
  holds — for an environment variable — but the reloader would have logged a restart demand on
  every table change against a synthesised row and tried to carry a ladder onto it. It now skips
  external rows.
- **Decision 2's precedence was reversed.** "The environment always wins" would, on an ecs-ec2
  deployment that also sets `AF_COMFY_URL`, replace the managed `image` row and leave its ECS
  service with no controller and no admin button to stop it. A managed table row now wins.
- **Decision 2's `AF_COMFY_API_KEY` needs no plumbing**: the `apiKey` field exists, and both
  `dial` and `engineHealthy` present it. The draft did not say the health probe carries it too,
  which decides how the reverse proxy of decision 7 has to be configured.
- **Decision 4 needs no new error code**: the non-streaming path already maps any dial error
  that is not `errEngineWaking` to `engine_unavailable` with the text appended. The draft implied
  new handling; P0 is the immediate return and the text.
- **Decision 1's "none of" was checked site by site**: nearly every dereference of `ecs`, `ctrl`,
  `demand`, `pending`, `ssm` is already nil-safe, and the two that are not are the two functions
  decisions 4 and 5 rewrite anyway. Recorded so that P0 does not add guards it does not need,
  and pinned with the one test that walks every handler.
- **Decision 8's 5 second probe would have held the admin panel.** The list handler is
  synchronous; 2 seconds and a 10 second cache.
- **Decision 5 lacked a contract.** The row's fields are now written down, so the CP and the
  Console can be built by different sessions; the Console's existing `managed` branches were
  confirmed to be generic (the TTS string is reused), and the `ondemand` button confirmed to
  render on every row today.
- **Decision 6's claim about `files_missing` is confirmed** by the models route's own comment.
- **The documentation list named "a new section under `guide/operate/`"** without a page; the
  shelf is numbered 01–06 and bilingual, so it is `07-image-engine` in both languages plus the
  README row (`scripts/docs-check.py` enforces the pair).
- **The title said "the gateway unchanged"** while decision 4 changes a function in
  `engine_gateway.go`. It now says the forwarding path is unchanged, which is what is true.
- **The completion criterion needs the operator's network.** Said so in the phases: a session
  cannot supply a LAN ComfyUI, so the one real run is the operator's step after the four lanes
  merge.

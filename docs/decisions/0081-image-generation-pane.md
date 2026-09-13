# 0081. Generating images from the Console without an LLM in the loop — one pane, a job queue in the Agent, and prompt help that is deterministic first and a model second

English | [日本語](0081-image-generation-pane.ja.md)

- Status: **proposed** (drafted 2026-09-13; not yet reviewed).
- See also: [0069](0069-image-generation-providers.md) (the provider abstraction this pane drives;
  open question 1 deferred the job shape — this ADR takes it up) /
  [0072](0072-engine-model-catalog.md) (the catalogue rows, `params`, `negative_prompt`, the five comfy families) /
  [0080](0080-image-gallery-pane.md) (where the pictures are looked at; this pane writes what the gallery reads) /
  [0078](0078-sessions-overview-pane.md) (the most recent pane kind, and the boilerplate) /
  [0020](0020-chat-bridge.md) (`POST api/chat/ask`, the one-shot LLM call the prompt help reuses) /
  [0071](0071-self-hosted-inference-engines.md) (the engine box, its cold start, and who pays for it)

## Background

Today a picture is made only by asking an agent. The session calls `generate_image`, the `af` MCP server
posts to the Agent's `POST /imagegen/generate`, the Agent builds a ComfyUI graph, drives the engine through
the control plane's `/engine/image/v1/*` pass-through, stores the PNGs under
`~/.cache/agent-fleet/generated/<sid>/`, and the picture comes back into the transcript as a file card.
That is the right shape for "put an illustration in this document". It is the wrong shape for the person
who wants **forty variations of one prompt at three CFG values across two checkpoints**, because every
round trip costs a model turn, the parameters that matter to them (steps, sampler, seed, LoRA weight)
are either invisible or unreachable, and the agent's own words sit between them and the result.

What already exists, measured on the tree at `2da2bf28` (2026-09-13):

- **The Agent has a non-MCP door.** `GET /imagegen/status` and `POST /imagegen/generate`
  (`workspace/agent/routes.go:121-122`, `internal/imagegen/http.go`). They are behind the Agent bearer
  gate like every route, and **not on the control plane's proxy list** (`http.go:8-9` says so on purpose).
  Nothing in `console/` calls them.
- **Everything that makes a picture lives in `workspace/agent/internal/imagegen/`**: the `Provider`
  interface and the core (`imagegen.go`), the five ComfyUI graph templates (`comfy_workflows.go`:
  `sdxl`, `sd35`, `flux1`, `flux2-klein`, `zimage`), LoRA resolution with a family check and a weight
  (`comfy.go:303-350`), negative-prompt composition (row + caller + deployment-wide, `comfy.go:169-178`),
  the seed (`comfy.go:713-725`), the store and its 30-day sweep (`store.go`), and the usage row
  `tool.imagegen` with image and pixel counters (`imagegen.go:706-726`). The control plane only relays
  bytes (`control-plane/engine_gateway.go:866-957`) and, for the image role, writes **no** usage row of
  its own (`engine_usage.go:180-186`).
- **The engine credential exists only inside the workspace.** `AF_ENGINE_ISSUE_TOKEN` is injected into
  the container (`control-plane/workspace_lifecycle.go:401`); the browser holds nothing that
  `/engine/{key}/v1/*` accepts (`engine_gateway.go:417-424`). A Console that talked to ComfyUI directly
  would need a credential that does not exist.
- **The request vocabulary** (`imagegen.go:56-115`): `op`, `prompt`, `negative_prompt`, `size`,
  `count` (→ ComfyUI `batch_size`, ceiling 4), `inputs`, `mask`, `model`, `seed`, `strength`,
  `loras[{name,weight}]`. **Not** in it: `steps`, `cfg`, `sampler`, `scheduler`. Those come from the
  catalogue row's `params` (ADR 0072, `EngineParams`) laid over the family recipe field by field
  (`comfy_workflows.go:174-195`). ADR 0069 keeps them out of the MCP tool on purpose ("provider
  differences too large; turning the knob does nothing on some paths"). That reasoning is about the
  *tool an agent sees*; it does not bind a pane whose only providers are the fleet's own.
- **What a family reads** (`comfy_workflows.go`; recipe defaults in parentheses):

  | family | steps | cfg | sampler | scheduler | negative prompt |
  |---|---|---|---|---|---|
  | `sdxl` | yes (20) | yes (7) | yes (dpmpp_2m) | yes (karras) | yes |
  | `sd35` | yes (28) | yes (4.5) | yes (dpmpp_2m) | yes (sgm_uniform) | yes |
  | `flux1` | yes (20) | **no** — FluxGuidance 3.5 | yes (euler) | yes (simple) | no |
  | `flux2-klein` | yes (4) | **no** — fixed 1 | yes (euler) | **no** | no |
  | `zimage` | yes (8) | yes (1) | yes (res_multistep) | yes (simple) | no |

  Unknown sampler or scheduler names are **silently ignored** (`comfy_workflows.go:196-209`); a name the
  box does not know would fail after a cold start with `Value not in list`, so the Agent's allow-list
  is the contract. A negative prompt on a distilled family is dropped with a warning
  (`comfy.go:187-204`). Nothing in the Console knows this table.
- **The blocking call and the clocks.** `POST /imagegen/generate` returns when the picture is on disk.
  The chain is: control plane wake budget 900 s (`engine_gateway.go:87`) with a plain-request hold of
  45 s under the ALB's 60 s idle timeout (`:33-52`, `:104`), the Agent's 16-minute bound
  (`sdcpp.go:191`), the MCP caller's 18 minutes (`mcp_imagegen.go:157`). The Console's REST proxy
  (`control-plane/proxy.go:148-180`) is a buffered relay with no streaming and no heartbeat: a browser
  call that sits for a cold start would be cut at 60 s in an ALB deployment.
- **No job id, no cancel, no progress, no seed back.** `prompt_id` never leaves `comfy.go`; nothing
  calls ComfyUI's `/interrupt` or `/queue`; the only "progress" is the MCP heartbeat with no content
  (`mcp_imagegen.go:250-298`); a random seed drawn in `comfySeedFor` is not in `Result`
  (`imagegen.go:154-171`) — the caller cannot reproduce what they got.
- **No provenance on disk.** The Agent writes the PNG bytes as they come and no sidecar (`store.go`).
  ComfyUI's `SaveImage` embeds the API graph as the `prompt` text chunk unless the box runs with
  `--disable-metadata`; nothing in this repository reads or writes PNG text.
- **No member-facing catalogue.** The admin screens (`console/src/features/settings/admin/adminEngines.tsx`,
  `adminEngineModels.tsx`) are super_admin, or tenant_admin under `allow_engine_ingest`. The only door
  a member's browser has today is `GET /imagegen/status`, and its per-model row is `{id, description,
  warm}` — no family, no sizes, no `params`, no row negative (`http.go:61-91`).
- **Trigger words are read and thrown away.** Civitai's `trainedWords` come back from
  `ingest/resolve` and search (`control-plane/engine_admin.go:1673-1675`, `engine_ingest.go:497`) and
  are shown in the wizard, but no column stores them. A LoRA that needs its trigger loads and changes
  nothing visible.
- **There is a one-shot LLM call the Console can already make.** `askAssistant(prompt, assistant?)`
  (`console/src/core/api/client.ts:877-884` → `POST api/chat/ask` → `chatx/chat_handlers.go:228-261`):
  an ephemeral, unpersisted conversation, no tools, 240 s bound (`chat.go:487`), run by the member's
  own CLI login, ledgered as `assistant.ask`. Memo tidy (`MemoTidyModal.tsx:83`) and the TTS summary
  (`useMirrorTts.tsx:160`) already use it and preview the answer before applying it.
- **Upstream, verified in ComfyUI's `server.py` (2026-09-13):** `POST /interrupt` with `{"prompt_id"}`
  interrupts that prompt only, without it the whole box; `POST /queue {"delete":[id]}` removes a
  pending item; `GET /queue` lists running and pending; per-step progress exists **only** on the
  websocket, and the control plane's relay does not upgrade connections.

In the user's words: "I want a UI that hits comfy without going through an agent and mass-produces
images." "Agent" there means the LLM session. The Workspace Agent — the Go daemon in the container —
stays in the loop, because it is where the graph, the credential, the disk and the ledger are.

## Decisions

### Decision 1 — Pixels are still made in the Workspace Agent; the Console reaches it through the control plane's proxy list

- Nothing moves. The Console calls Agent routes under `/api/imagegen/*` through the same
  `agentProxyAPI.rest` relay as every other Agent-backed screen; the control plane's `routes.go` gains
  the lines and nothing else (`withResolved` already requires a running workspace, which is also the
  gallery's condition — no agent process, no pictures either way).
- Lifting the graph builder into the control plane would need a browser-side engine credential that
  does not exist, a second copy of the family dispatch that `engine_catalog_test.go` currently keeps in
  step by reading the Agent's source, and a second store the gallery cannot see. Embedding ComfyUI's own
  web UI in the browser pane has the same credential problem and bypasses the tenant's catalogue.
- Only **fleet providers** are offered here: `comfy` and `sdcpp`. The CLI-driven providers (`codex`,
  `agy`) are agents by construction, own none of these knobs, and burn a plan quota — the reason the
  `agents.image_generation` opt-in exists. That opt-in therefore **does not gate this pane**; the pane
  exists when `GET /imagegen/status` reports a ready fleet provider, and the `session` field, required
  by the existing route because it names the output folder and refuses a provider equal to the
  session's own CLI, has no meaning on this path (decision 3).

### Decision 2 — The Agent gets a job queue; the Console enqueues and polls. The existing blocking route stays for the MCP tool

ADR 0069 open question 1 deferred jobs because a poll from a driver model costs a turn. A browser poll
costs nothing but bytes, and the 60-second rule makes the blocking shape unusable through the proxy.

- **Routes on the Agent**, and their four lines on the control plane's list:
  - `POST /imagegen/jobs` — body is the existing `generateRequest` plus `params` (decision 4), `label`
    (free text shown in the list) and `out_dir` (decision 3). Returns `{id, position}` at once.
    Refused with 429 when the queue holds `imagegenQueueMax` (200) pending jobs — one member cannot
    park a day of GPU on a shared box by accident.
  - `GET /imagegen/jobs` — every job the Agent still remembers (pending, running, the last 500
    finished), newest first, with `state` ∈ `queued | waking | uploading | running | fetching | done |
    failed | cancelled`, `position` while queued, `started_at`, `finished_at`, `elapsed_ms`, the
    resolved request (model, family, seed, effective `params`, loras, sizes) and, when done, `files[]`
    in the `StoredFile` shape plus `warnings[]`. The handler emits the same bytes for the same state so
    the control plane's ETag turns an unchanged poll into a 304.
  - `DELETE /imagegen/jobs/{id}` — cancel. Queued: removed. Running: the provider's optional
    `Canceller` is asked. For comfy that is `POST /queue {"delete":[prompt_id]}` when the prompt is
    still pending upstream and `POST /interrupt {"prompt_id"}` when it is executing — **always with the
    id**, never the bare `/interrupt`, because the box is shared across workspaces and a bare interrupt
    kills someone else's picture. `sdcpp` has no cancel; the job runs out and its result is discarded.
  - `GET /imagegen/status` — widened (decision 5).
- **One worker per provider, jobs run one at a time.** The box already serialises sampling; submitting
  ahead only moves the queue somewhere the Agent cannot see or cancel from, and leaves two requests
  waiting on `engine_waking` at once. Serial also gives the queue position and the estimate a meaning.
- **State phases come from the provider.** `sendWithWake` and `awaitHistory` report `waking`,
  `uploading`, `running`, `fetching` through a callback on the request; the job list is the only reader.
  This is what tells the user "the engine is starting; the first picture waits several minutes" instead
  of an unexplained 5-minute `running`.
- **An estimate, not a progress bar.** Per-step progress is websocket-only upstream and the relay does
  not upgrade. The Agent keeps an exponential moving average of `elapsed_ms` per (provider, model,
  size bucket) from finished jobs and reports it as `typical_ms`; the Console shows "usually ~30 s for
  this model". Honest and cheap; the real bar is P2 (unresolved 1).
- **Memory is the queue's store.** Pending jobs are lost when the Agent restarts; finished ones survive
  through the sidecar on disk (decision 3). A journal is P1 if a restart ever bites — the Agent restarts
  with the container, and the container's restart already discards more than a queue.
- **The Console polls every 2 s while any job is not finished and stops otherwise** — the standing
  policy of not polling idle screens. Tab visibility pauses it. The gallery's own 20-second net picks
  up files that land while the pane is closed.
- The MCP tool `generate_image` does not change. It could later become enqueue-and-wait on the same
  queue; that is a refactor with no user-visible effect and is not part of this ADR.

### Decision 3 — Output goes where the gallery looks, with a sidecar per picture and the seed in the answer

- **Default folder:** `~/.cache/agent-fleet/generated/console/` — a sibling of the per-session folders
  and the folder the gallery's "generated images" family (ADR 0080 unresolved 3) will list. Files keep
  the `image-<unixnano>-<n>.<ext>` name so the gallery's newest-first order holds.
  **It is never swept** (decided 2026-09-13): the 30-day sweep in `store.go` exists because an agent's
  pictures are by-products of a conversation nobody asked to keep; these are the product, and a person
  pressed the button for each of them. The sweep skips the `console` subtree by name, and the pane
  shows the folder's size so the cost of keeping is visible.
- **`out_dir` (optional):** a browse-root-relative folder for keepers, validated like `galleryPath` in
  ADR 0080 and passed through `safeWritableBrowsePath` (the upload route's own gate: inside the browse root, not under `fsDeny`), created on first
  use. It exists for sorting, not for survival — a folder the user names, next to the work it is for.
- **Sidecar:** next to every picture the Agent writes `<name>.json` — the resolved request (prompt,
  negative as composed, model id, family, seed actually used, effective `params`, loras with weights,
  size, op, strength, input paths), `provider`, `job id`, `label`, `elapsed_ms`, `warnings`, and the
  Agent build. Format-independent (works for webp and jpeg, no PNG rewrite), invisible to the gallery
  (it filters by `imageFormat()`), and the thing the gallery's card hover, "open in image generation",
  and any later "X/Y grid" read. ComfyUI's own `prompt` chunk stays in the PNG untouched; it is the API
  graph, not the request, and is absent when the box runs with `--disable-metadata`.
- **`Result` and `StoredFile` gain `seed`** (per image: base seed for batch index 0, `seed+i` after —
  ComfyUI derives batch noise that way). The MCP tool's answer gains the same line; "it was random and I
  cannot get it back" is the single most common complaint in any image UI.

### Decision 4 — The request grows a `params` overlay in the shape the catalogue already uses; the family decides what is read, and says so

- `Request.Params *EngineParams` (`steps`, `cfg`, `sampler`, `scheduler`; `clip_skip` and `weight`
  are not request fields — no template reads `clip_skip`, and a LoRA weight is per-LoRA already).
  Merge order becomes **family recipe ← catalogue row ← request**, field by field, with the merge that
  exists (`comfyRecipe.with`).
- **The MCP tool does not get `params`.** ADR 0069's reason stands for agents. The pane's providers are
  the fleet's own, whose knobs the Agent built itself.
- **The Agent validates, the box never sees a bad value after a cold start.** Sampler and scheduler are
  checked against `comfySamplerNames` / `comfySchedulerNames` and refused with 400 `bad_params` (not
  silently ignored, as the catalogue overlay is — a typed value must fail loudly). Steps 1–150, cfg
  0–30, size a multiple of 8 on each side with a pixel ceiling (`imagegenMaxPixels`, 4 M — a 2048²
  SDXL request on an `l4` is an OOM after a 5-minute wait, and the box's 400 comes back bare, not even
  as `engine_waking`).
- **What a family ignores is reported, not swallowed.** A `cfg` on `flux1` or `flux2-klein`, a
  `scheduler` on `flux2-klein`, a negative on the three distilled families: each produces a warning
  in the job the way the negative already does. The Console **also** greys those fields out, but from
  the Agent's word, not its own table (decision 5) — the two must not be able to disagree.

### Decision 5 — The member-facing catalogue is `GET /imagegen/status`, widened. No new control-plane route

- Per model the status gains `family` (the row's `base_model`), `sizes` (row or the five defaults),
  `params` (**effective** defaults after recipe ← row, so the form's placeholders are what will run),
  `negative` (the row's, shown as a fixed chip the user cannot remove — it is the admin's, and
  `negative_always` is shown the same way), `knobs` (the subset of `steps cfg sampler scheduler
  negative` the family reads — computed in the Agent from the same table the templates use, the only
  place it exists), `warm`, `description`, `license_name`, `license_url`, `source_url`.
  Per LoRA: `base_model`, `weight` (the row's default), `trained_words`. Engine-level: `samplers[]`,
  `schedulers[]` (the allow-lists, so the form cannot offer a name the Agent will refuse), `typical_ms`.
- The rows this reads are what the Agent already receives on `GET /internal/engine/catalog`
  (`engineCatalogModelRow`) — the door the MCP path uses, with the workspace's issuing token. A second,
  browser-authenticated catalogue on the control plane would be a second projection of the same rows
  to keep in step (the `sessionWire` lesson: fields not in the relay vanish silently).
- Models marked `base_model_missing`, `files_missing` or `vae_missing` are **not listed** — the
  catalogue already withholds them from generation, and a disabled entry with a tooltip is the admin's
  screen, not the member's.
- **`trained_words` becomes a column** on `engine_models` (JSON array; migration in both dialects —
  check the next free number against every open lane before merging, see ADR 0072's collision note),
  written by ingest from Civitai's `trainedWords`, editable on the admin row, relayed by
  `engineCatalogModelRow`. One column, three places; without it the pane cannot do the one thing every
  LoRA user asks for (decision 7).

### Decision 6 — One pane kind, `imagegen`. Its draft is a local draft, its truth is the job list

```ts
| { kind: "imagegen" }
```

- **A pane, not a modal**, for the reasons ADR 0080 decision 1 gives (split next to the gallery or the
  mirror, tabs, pop-out, layout persistence, phone single-pane) plus one of its own: mass production is
  a screen one leaves open for an hour.
- **No target field.** `sameTarget` is "same kind"; opening it twice focuses the one that exists. Two
  studios with two models is a request that has not been made (unresolved 4).
- **The form draft lives in `localStorage`** (`af.imagegen-draft.<workspace>`), written on every change
  the way composer drafts are, so a reload keeps a half-written prompt. It is not pane content: the
  layout store is not a place for a 2 kB prompt, and the draft is per browser, not per layout.
- **The job list is the Agent's** (decision 2). Tab switches unmount the view; on remount the list is
  one poll away and nothing is lost — the 0078 rule "what must survive a tab switch goes in the
  content" is satisfied by putting it on the server instead.
- All eight registration points of a pane kind (union, `migrate.ts`, `sameTarget`, render switch,
  `paneTitle` twice, `LayoutMap` abbreviation, pop-out, i18n) plus the ninth: **the command table
  (ADR 0017) gets `open.imagegen` (`g i`)**. ADR 0080 could not register the gallery because it needs a
  folder; this pane has no argument, like `open.sessions`.
- i18n prefix `imggen.*` in a new domain file pair (`ja/imggen.ts`, `en/imggen.ts`) — the catalogue
  test binds one prefix to one file.
- Entry points, each one line in an existing row: the ops bar and `LayoutMap` button next to
  "sessions"; the gallery header "generate here" (P1, when ADR 0080 lands — it opens the pane with
  `out_dir` set to the gallery's folder); the gallery card "open in image generation" (P1, reads the
  sidecar into the form).

### Decision 7 — Prompt help is two layers: a deterministic layer that is always there, and a one-shot model call the user presses

**Layer A — no model, no network, always on.**

- **Family card.** For the selected model's family the pane shows, in one collapsible line: the
  dialect (tag list for `sdxl` and its Pony / Illustrious / NooBAI descendants; natural-language
  sentences for `flux1`, `flux2-klein`, `zimage`, `sd35`), the quality prefix the dialect expects
  (`masterpiece, best quality` / `score_9, score_8_up` — offered as a chip, never inserted silently),
  whether the negative prompt reaches this family, the recommended step and cfg ranges, and the size
  presets. Five families, Console i18n content; a card per checkpoint is unresolved 2.
- **Trigger words.** Selecting a LoRA adds its `trained_words` as chips in the prompt row; removing the
  LoRA removes the chips it added and nothing else. The most common "the LoRA does nothing" is a
  missing trigger, and no model call fixes it.
- **The admin's negative and the row's `params`** are shown as the fixed part of the form, with their
  origin ("declared by the administrator for this model"), not merged invisibly.
- **Sampler and scheduler** are selects over the Agent's lists; a family that does not read one shows
  it disabled with the reason.

**Layer B — a model, once, on a button, previewed before it lands.**

- "Write the prompt for me" sends `askAssistant()` one message built by the Console: the family card,
  the model's `description`, the selected LoRAs' trigger words, the row negative, and the user's intent
  in whatever language they typed it, asking for JSON `{prompt, negative, note}`. The answer is shown as
  a proposal with "use" / "use prompt only" / "discard"; nothing is applied on its own. Variants on the
  same button: "three alternatives", "rewrite in this model's dialect" (a tag list from a sentence or
  the reverse), and, when a reference image is attached, "describe the image as a prompt" (vision —
  P2, the assistant path does not attach files today).
- **Why `api/chat/ask` and not the tenant's `llm` engine.** The browser cannot reach `/engine/llm/*`
  (no credential), not every deployment has the role, and a cold llama.cpp box is minutes of GPU for
  one sentence. `askAssistant` runs on the member's own CLI login, is what memo tidy and TTS summary
  already do, and is ledgered. It is "an LLM in the loop" only when pressed, and the assistant's name
  is on the button. An Agent-side `POST /imagegen/suggest` that uses the workspace's engine token
  against `llm` is a P1 option for deployments without any CLI login (unresolved 3).
- **No model call is made without a press.** ADR 0069 decision 8's reason (invisible plan consumption)
  applies unchanged.

### Decision 8 — Mass production is "N jobs", not one big batch, with a seed policy and a group

- **"Count" in the form means N jobs**, one picture each, seeds by the policy below, enqueued as a
  **group** (one `group` id on every job; the list folds a group into one row with "7 / 40 done";
  cancel is per job or per group). A ComfyUI `batch_size` above 1 stays available as an advanced field
  (`count` in the request, ceiling 4): faster per picture on a card with headroom, an OOM after a
  5-minute wait on one without, and no per-picture cancel.
- **Seed policy:** `random` (default) / `fixed` (every job the same seed — the knob for "same picture,
  vary the cfg") / `sequence` (base + i). Every result shows its seed; "again with this seed" and "again
  with a new seed" are the two buttons on a result.
- **The cache warning is a feature.** ComfyUI returns the cached picture in ~0.5 s for an identical
  graph (`comfyCacheWarning`). The pane shows it as "identical to an earlier run" rather than a warning,
  because with `fixed` seeds it is the expected answer to "did anything change".
- **Sweeps and the prompt matrix are P1** (a group whose jobs differ in one axis: cfg, steps, model,
  LoRA weight, or a `{a|b|c}` alternation in the prompt; the gallery's P1 "X/Y" layout reads the
  sidecar's axis fields). The group and the sidecar are shaped so that P1 adds no wire field.

### Decision 9 — Reference images come from the workspace disk, not from the browser's memory

- `edit` and `inpaint` take `inputs[]` as browse-root paths already. The pane offers: pick from the
  gallery (ADR 0080's card gains "use as reference", P1), drop a file (uploaded through the existing
  `POST /fs/upload` into `generated/console/inputs/`, then referenced by path), or type a path.
  `strength` is the slider the existing `Slider` control draws; a mask is P2 (painting needs a canvas
  the Console does not have; a mask file by path works from day one).
- On `edit` the input's own dimensions win over the size field, with the existing warning; the form
  shows the size field disabled with the input's dimensions in it, so the user reads the rule instead
  of the warning.

### Decision 10 — The engine's state and cost are on the screen, in the member's words

- The header shows one of: **ready** (a warm model), **cold** ("the engine starts on the first job;
  usually N minutes" — from the last observed `waking` duration, kept by the Agent like `typical_ms`),
  **starting** (a job is in `waking`), **unavailable** (`engine_off`, `engine_unavailable`, or no fleet
  provider — with the code's own message). It comes from the widened status and the job phases; there
  is no member route to the admin's engine row and this pane does not need one.
- **Cost is the tenant's GPU hour, and the pane says so** rather than printing `$0.00`: comfy's
  `CostUSD` is 0 by construction and the attribution lives in the admin's hourly table. The usage pane
  gains the two counters it already receives and never shows (`Images`, `Pixels` under
  `tool.imagegen`) so a member can see their own volume; that is a fold in `usage_series.go` and two
  labels, listed in Consequences.

### Decision 11 — A trial run lives in the view: one quick picture, at the head of the queue, seen where the form is

Mass production is decided by looking at one picture first. If that picture has to wait behind forty
queued jobs, or be found in the gallery, the loop is broken and people queue blind. So the pane has two
verbs, and they are not the same button:

- **"Trial run"** (`Ctrl+Enter`, the frequent action): one job from the current form, marked
  `trial: true` in `POST /imagegen/jobs`. The Agent **inserts it at the head of the queue** — after the
  job that is already running, before every queued one — so the wait is one picture, not the batch.
  Trials are capped at 3 waiting per workspace (a fourth is refused with 429 `trial_pending`), so the
  head of the queue cannot itself become a queue.
- **"Enqueue N"** (`Ctrl+Shift+Enter`): the group of decision 8, at the tail, as before.

What a trial changes about the request, and only this:

- **`count` 1, `batch_size` 1.** One picture is the point.
- **Steps: the family's trial value**, not the form's — `sdxl` 10, `sd35` 12, `flux1` 8, `zimage` 4,
  `flux2-klein` 4 (already minimal). The form's own steps are kept on the request as `params.steps`
  so the sidecar records both what ran and what the batch would run. A checkbox "full steps" turns the
  reduction off for the case where the trial *is* the picture.
- **Size, seed, cfg, sampler, LoRAs, negative: exactly the form's.** Composition is fixed by seed and
  size; a trial at another size would preview a different picture. When the seed policy is `random`,
  the trial draws one and **shows it** — "use this seed" copies it into the form as `fixed`, which is
  the whole reason a trial is worth doing before a sweep.
- **Output: `generated/console/trial/`**, a subfolder the gallery lists like any other. Trial pictures
  are disposable by definition (the batch remakes the keeper at full steps), so **`trial/` is the one
  subtree the sweep still clears, after 7 days** — the exception to decision 3, stated here so it does
  not read as a contradiction. "Keep" on a trial result re-enqueues the same request at full steps
  into the main folder; it does not move the draft.

Where it shows: **in the pane, next to the form** — a "latest trial" slot with the thumbnail
(`downloadURL(path, 512)`, the same cache key as the gallery and the mirror), seed, elapsed time and the
warnings, opening the shared lightbox on click. Below it, the job list shows every result of this pane
as cards the same way, newest first, so the view is usable without the gallery at all; the gallery is
for looking *across* folders and sessions, not for finding what one just made. The list holds paths,
not bytes, and a card renders lazily like the gallery's.

The estimate (`typical_ms`) is keyed by steps as well as model and size bucket, otherwise trials would
teach the average that batches are fast.

## Options rejected

- **Proxy the existing blocking `POST /imagegen/generate` as-is.** Dies at the ALB's 60 s on a cold
  start, cannot be cancelled, gives no position, and would run the queue in the browser's memory
  (decision 2).
- **Build the graph in the control plane and give the browser an engine credential.** A credential
  that does not exist, a second family dispatch, a second store the gallery cannot see (decision 1).
- **Embed ComfyUI's web UI in the browser pane.** Same credential problem, bypasses the catalogue,
  unusable on a phone, and the tenant's negative and params never apply.
- **Let the Console post a raw ComfyUI graph** ("custom workflow"). The relay would carry it — the
  control plane does not police bodies by design — but the Agent's templates are the contract that makes
  `base_model`, `params`, the row negative and the LoRA family check mean anything. A power-user
  workflow feature is a different ADR with its own policy question.
- **Job store in the pane's content.** Lost on layout reset, duplicated across pop-outs, and not the
  truth — the Agent is (decision 6).
- **Progress bar in P0 via the websocket.** Needs the relay to upgrade connections and the Agent to
  hold one; the estimate is 80 % of the value for 5 % of the work (decision 2, unresolved 1).
- **Prompt help through the tenant's `llm` engine in P0.** Not reachable from the browser; from the
  Agent it is a P1 option (decision 7).
- **Auto-insert quality tags or trigger words into the prompt text.** Silent edits to the user's text
  are the failure ADR 0069 decision 7 forbids for sizes; chips the user sees and can remove are the form.
- **Silently ignore a bad sampler name in the request, as the catalogue overlay does.** A typed value
  fails loudly; the overlay's leniency is for an admin's old row, not a member's form (decision 4).
- **Bare `POST /interrupt`.** Kills another workspace's picture on a shared box (decision 2).
- **A separate pane per model / per output folder.** Not asked for (unresolved 4).
- **Trial run as an ordinary job at the tail.** Behind a batch it arrives when the batch does, and the
  batch was the thing the trial was meant to decide (decision 11).
- **Trial at a smaller size to make it faster.** A different size is a different composition; the
  preview would not preview anything. Fewer steps at the same size is the honest shortcut (decision 11).

## Consequences

- **Agent** (`workspace/agent`): `internal/imagegen/` gains `jobs.go` (queue, worker, group, EMA,
  cancel), `Request.Params`, `Result/StoredFile.Seed`, the sidecar in `store.go`, request validation,
  phase callbacks in `comfy.go`'s `sendWithWake` / `awaitHistory`, an optional `Canceller` interface
  implemented by comfy, the widened `statusResponse` with `knobs` / `samplers` / `schedulers` /
  `typical_ms`; four routes in `routes.go` (so `testdata/routes.golden` moves — expected here, unlike
  ADR 0080). `usage_series.go` folds `Images` / `Pixels`.
- **Control plane**: four proxy lines in `routes.go`; `engine_models.trained_words` (migration in both
  dialects), written by ingest, editable by the admin row, relayed by `engineCatalogModelRow`.
  No gateway change: cancel is two more pass-through paths.
- **Console**: `features/imagegen/` (view, form, job list, family cards, prompt-help modal, `open.ts`,
  CSS, pure functions for the draft and the group fold); the nine registration points; the i18n pair
  `imggen.*`; two entry buttons; `usage` labels for images and pixels. The gallery's P1 items "open in
  image generation" and "use as reference" land there when ADR 0080's pane exists.
- **Docs**: `guide/ref/features.{md,ja.md}` row + `guide/member/` procedure and
  `workspace/agent/knowledge/af-usage.{md,coverage.tsv}` (docs-check enforces both, as the sessions
  pane found).
- **No new dependency.** Sliders, selects, the lightbox, thumbnails, the one-shot assistant call and
  the upload route all exist.
- Tests: pure (draft round trip, group fold, seed policy, family card selection, the JSON the
  prompt-help parses), DOM (form disables what `knobs` omits; chips follow LoRA selection; the
  proposal is previewed, not applied; cancel per job and per group), Go (queue order, cap 429,
  cancel of queued vs running with the targeted `/interrupt`, validation refusals, sidecar contents,
  seed in the answer, status fields, ETag-stable bytes, EMA), and one live run against a GPU box
  before P0 is called done — the golden files pin graph shapes, and ADR 0072 recorded what a green
  golden is worth when a graph has never run.

## Phases

- **P0** (decisions 1–11 minus the P1 items named in them): the pane, the queue with cancel, group and trial,
  `params`, the widened status, the sidecar and the seed, family cards, trigger-word chips,
  "write the prompt for me" through `api/chat/ask`, edit by path or drop, `out_dir`. Three lanes that do
  not share a file:
  - **Lane A (Agent)**: `jobs.go` (with head insertion and the trial cap), `Request.Params`, validation, seed and sidecar, phases and cancel,
    status widening, routes and golden, usage fold.
  - **Lane B (control plane)**: proxy lines, `trained_words` column end to end.
  - **Lane C (Console)**: pane kind and `features/imagegen/` (form, trial slot, result cards, job list), i18n, entry points, usage labels.
    C can be built against a stub of A's wire (the shapes above are the contract) and finished after A.
- **P1**: sweeps and the prompt matrix; gallery hooks ("open in image generation", "use as reference",
  "generate here"); presets (named parameter sets, local first); a queue journal if a restart bites;
  `POST /imagegen/suggest` via the `llm` role; "send to an agent" (`chatCreate({attachPath})` exists);
  a notification when a group finishes; per-checkpoint prompt notes if families prove too coarse.
- **P2**: the progress bar and preview stream (relay upgrade + websocket in the Agent); mask painting;
  "describe this image" (vision); a CLIP token counter for the 77-token families (a tokenizer in the
  browser — weigh the bundle); `upscale` as an op (needs an upscaler row kind in the catalogue).

## Unresolved

1. **Is the estimate enough, or is the bar needed in P0?** Decide after the live run: if a cold-start
   picture on `flux1` sits in `running` for 90 s with only "usually ~40 s", the estimate is a lie in
   the case that matters most.
2. ~~Family cards or per-checkpoint cards.~~ **Settled 2026-09-13: family cards.** Pony, Illustrious
   and base SDXL are one family with three dialects; if the family card misleads on the first real Pony
   row, add `prompt_notes` to the row in P1 (the admin writes it; ingest could seed it from the Civitai
   description the `params_hint` regex already reads).
3. **A member with no CLI login.** Layer B runs on `api/chat/ask`, which executes one of the member's
   own CLIs headless inside the workspace (`claude -p`, `codex exec`, opencode, agy, cursor — whichever
   the chosen assistant is bound to) under the login that CLI already holds. A member who has never
   signed in to any of them, or a fleet whose members only ever use the self-hosted engines, has no
   assistant to run and gets Layer A alone until the P1 Agent route through the `llm` role exists.
   Whether that route is P0 depends on whether such a fleet is real.
4. **Several studios at once.** One pane per workspace is the P0 answer; if two models side by side is
   asked for, the pane gains a `slot` and `sameTarget` compares it.
5. ~~Retention of `generated/console/`.~~ **Settled 2026-09-13: never swept** (decision 3).
6. **Queue cap and finished-list size.** 200 and 500 are guesses; the shared host's memory is the
   constraint (a finished job holds paths, not bytes, so the list is small; the sidecars are the
   archive).
7. **The trial step counts** (10 / 12 / 8 / 4 / 4) and the 7-day sweep of `trial/` are guesses. The
   steps should be the smallest number at which the composition is recognisable on the live run; if
   the sweep of `trial/` is unwelcome, decision 3's "never" extends to it and the folder simply grows.

# 0098. Qwen-Image 2.1 as an image family — one template that generates and edits, and a VAE that shares nothing

English | [日本語](0098-qwen-image-21-family.ja.md)

- Status: **proposed, and built in the same change** (2026-09-21). **P2 was run on the dev
  deployment on 2026-09-23** (see "P2 on real hardware"): text-to-image passed first time, and
  EVERY edit failed on a wrong input key that the goldens had pinned rather than caught. The fix
  and its live positive control are recorded there.
- **P3 was run the same day** (see "P3 on real hardware"): the family fits a T4 but samples 25x
  slower there, so no `vram_mib` is declared and the file-sum floor stands.
- **The Open items were followed up the same day** (see "The Open items, followed up"): the
  img2img size warning names the size measured off the output, 2048-class sizes are offered after
  a 2048² run, the alpha channel is stripped unless `background=transparent` was asked for, and
  the prompt enhancers are left unwired with the reasons recorded.
- 🔴 **Prerequisite (P0): the ComfyUI pin at v0.37.0** — landed in #859 (the image) and #860 (the
  template default). `TextEncodeQwenImage21` does not exist before that tag, so on an engine still
  pulling an older one a row of this family is refused at `/prompt` validation. ⚠️ A running stack
  keeps its old `ImageComfyImageTag` through `update.sh`; the tag has to be named once.
- Every upstream fact below was MEASURED against the live APIs and the pinned engine's own source
  on 2026-09-21. The measurements are in "What was measured"; where a number was taken from a
  published graph rather than from a run, the text says so at the point it is used.
- Number: 0097 is the highest on `develop`, and no open pull request claims 0098.
- Related: [0072](0072-engine-model-catalog.md) (the per-family templates, `base_model` dispatch,
  and decision 2's rule that an operator declares the family) / [0094](0094-instruction-edit-image-models.md)
  (instruction editing, the two Qwen-Image-Edit families this one is NOT a version of, and 未解決 4's
  consolidated `comfyFamilyRow` that decision 3 below extends) /
  [0082](0082-many-image-engines-at-once.md) (the provider is the unit, not the row) /
  [0069](0069-image-generation-providers.md) (what earns a new vocabulary word).

## Context

Qwen-Image 2.1 was published on 2026-09-20. The name puts it next to the two Qwen-Image-Edit
families ADR 0094 added, and it is the opposite of a version of them:

| | qwen-image-edit-2509 / 2511 | qwen-image-2.1 |
|---|---|---|
| Diffusion model | 20B MMDiT | 7B single-stream DiT |
| Text encoder | Qwen2.5-VL-7B | Qwen3-VL-8B |
| Autoencoder | Qwen-Image VAE — 16 channels, downscale 8 | its own — **64 channels, downscale 16**, RGBA |
| Ops | edit only | **generate and edit** |
| Encode node | `TextEncodeQwenImageEditPlus` | `TextEncodeQwenImage21` |
| Licence | Apache 2.0 | **Qwen Research License — non-commercial** |

ADR 0094 decision 6's rule is "one family per TOPOLOGY, not per version". Three separate facts each
make this a new family on their own: a different encode node, a different autoencoder, and an op
set no existing family has.

The autoencoder is the one that would fail quietly. `VAELoader` takes both files, `--vae` names
both roles, and the Control Plane's parts table stages parts by S3 key — so pointing this family at
the Qwen-Image VAE the other four share would be accepted everywhere and decode to noise. That is
why its key is a NEW key rather than a shared one (`engine_family_parts.go`), which is the reverse
of the sharing those four were written to do.

## What was measured

All 2026-09-21, anonymous unless stated.

**Upstream repositories.** `Qwen/Qwen-Image-2.1` is **ungated** (`gated: false`), `license: other`,
`license_name: qwen-research`, tagged `rgba`. `Comfy-Org/Qwen-Image-2.1` is the ComfyUI mirror and
is ungated too; it publishes the single-file split this deployment's loaders read:

```
diffusion_models/qwen_image_2.1_bf16.safetensors            13.25 GiB
diffusion_models/qwen_image_2.1_int8_convrot.safetensors     6.76 GiB
text_encoders/qwen3vl_8b_bf16.safetensors                   16.33 GiB
text_encoders/qwen3vl_8b_int8_convrot.safetensors            8.71 GiB
text_encoders/qwen3vl_8b_w4a8.safetensors                    5.88 GiB
vae/qwen_image_2.1_vae_bf16.safetensors                      0.63 GiB
```

⚠️ `text_encoders/qwen3.5_9b_qwen_image_2.1_pe_{t2i,i2i}.int8_convrot.safetensors` also live there
and are NOT the text encoder — they are the prompt-enhancer models, and the official template's own
model list does not mention them.

**The pinned engine.** `comfy_extras/nodes_qwen.py` read at three tags: `TextEncodeQwenImage21` is
absent at v0.35.2 (the pin) and at v0.36.0, and present at **v0.37.0**. Held against the pin, the
bump is also safe for what is already here: all 30 node classes this Agent's ten templates emit
exist at both tags, and the only definition changes that touch them are inert — `CLIPLoader`'s type
list gained `yue2`, `EmptyLatentImage`'s width/height DEFAULTS moved 512 → 1024 (every template here
passes both explicitly), and `ModelSamplingAuraFlow` gained an optional `sampling` input defaulting
to `flow`. `nodes_qwen.py` is additions only.

**The model's shape, from the engine's own source at v0.37.0.** `comfy/latent_formats.py`'s
`QwenImage21` is `latent_channels = 64`, `spacial_downscale_ratio = 16`. `comfy/supported_models.py`
gives `shift: 0.69` and `memory_usage_factor: 6.0`. `comfy/sd.py:1955` reaches this family's encoder
only through `clip_type == CLIPType.QWEN_IMAGE` **together with** a state dict detected as
`TEModel.QWEN3VL_8B` — so the CLIPLoader's `type` is read here (the krea2 case, not the anima one),
and the FILE is what separates this family from its siblings at the same type.

**The published graphs.** Comfy Org ships `image_qwen_image_2_1_t2i.json` and
`image_qwen_image_2_1_image_edit.json`. Read side by side they are the same nodes wired the same
way. Both KSamplers carry **25 steps, cfg 1, euler, simple, denoise 1**. Both notes say
"negative_prompt: unused while cfg is 1. Raise it only if you use a negative prompt" and "the
official pipeline uses about 40-50 [steps] with euler. This template starts at 25". The edit
template wires **image_1 … image_10** and says "Up to 10 reference images"; the node itself accepts
image_1 … image_16.

**Civitai.** Model-version 3344134 (`Qwen Image 2.1 GGUF`) publishes `baseModel: "Qwen 2"`, AIR urn
`urn:air:qwen2:…`, three GGUF files of 4.10 / 7.09 / 7.51 GB. Filtering, with `types=Checkpoint` and
both lists complete rather than a first page:

```
baseModels=Qwen 2  ->  5 checkpoints, every one of them Qwen-Image 2.1
baseModels=Qwen    ->  20+ checkpoints spanning FIVE architectures: Qwen-Image,
                       Qwen-Image-2512, Qwen-Image-Edit / 2509 / 2511, and a
                       `Qwen-3-0.6B base/anima` row that is a text encoder
```

On Hugging Face, `filter=base_model:Qwen/Qwen-Image-2.1` answers derivatives (the Comfy-Org mirror
and several GGUF conversions).

## Decision

### Decision 1 — a new family, `qwen-image-2.1`, spelled with the dot

It is a new template rather than a row's `params` on an existing one, on all three of the grounds in
the table above. The spelling carries the product's dot; the Control Plane validates rows against
that exact string and `engine_catalog_test.go` reads it out of the Agent's source, so the two
copies are one spelling. (That test's family regexp had to learn the dot — without it
`qwen-image-2.1` matched as `qwen-image-2` and reported a drift that does not exist.)

### Decision 2 — one template serves both ops, and the latent is the whole difference

The two published templates differ in exactly one wire. Generating starts from `EmptyLatentImage` at
the caller's size; editing starts from `TextEncodeQwenImage21`'s own third output — an all-zero
latent shaped to the resized first reference, which is how "the output follows image_1" is
expressed. The template puts a `ComfySwitchNode` between them; this builder picks by whether a
reference picture is present, which is the same choice with one node fewer.

Neither wire is interchangeable and neither failure is loud. The encode's latent for a generate
would answer every request at 1024² whatever size was asked for. An `EmptyLatentImage` for an edit
would put the edit on a canvas the conditioning was not built for — the template's own note: "Keep
it close to the resized image_1 size, or the edit can shift".

Inpaint is left unclaimed, on ADR 0072 decision 3's grounds: a mask could be wired, nobody upstream
publishes it, and nobody here has run it.

### Decision 3 — this family splits "instruction edit" from "edit only"

Before this, ONE fact answered five questions at once, because the only instruction-edit families
were also edit-only and also built by one builder. ADR 0094 未解決 4 had just consolidated that into
`comfyFamilyRow.InstructionEdit` — non-nil meaning "decisions 2, 3, 4, 5 and 12 all apply". This
family is the counter-example on three of those axes at once, so the row grows the fields that were
being carried by a single pointer:

- `InstructionEdit` keeps the narrow meaning it was consolidated for: a family it is non-nil for is
  exactly a family `comfyGraphQwenImageEdit` can build. This family is **nil** — it has a builder of
  its own.
- `FixedDenoiseEdit` is decision 2's fact alone — `op=edit` conditions the sampler through the
  picture at a full denoise, so `strength` has nowhere to go — and this family is **true**.
  `comfyFamilyInstructionEdit` reads this. The wiring pointer implies it and not the reverse; a test
  pins that one-way direction, with the control that at least one family is true without the
  pointer (otherwise the two fields have silently become one question again).
- `Ops` and `RefInputs` are declarations on the row, with the permissive default for an empty one.

🔴 `comfyFamilyHasNoSizes` is **derived from `Ops`** rather than declared a second time, and the
derivation is the real statement of decision 4: a size only ever reaches an `EmptyLatentImage`, and
only a generate path builds one — so a family that cannot generate has nowhere to put a size.

🔴 The size consequence is the one worth stating: this family HAS sizes, unlike its two neighbours.
Emptying its size list to match them would delete the size control from the generate path, which is
the only op that reads one. On its edit a size is ignored the same way, and what says so is the
img2img size warning every family already shares — not a family-wide "no sizes" claim that would be
false half the time.

### Decision 4 — ten reference pictures, and it is a CITATION, not a measurement

The node takes image_1 … image_16 and the official edit template wires ten. Ten is what this family
declares.

🔴 Called out rather than left to look like the 3 next door. ADR 0094 decision 5 deliberately did
NOT raise that number on the strength of "the wiring is a loop, so more must work" — it waited for
実測 F. Here there is no run at all, so the honest basis is the published wiring, which is the same
basis this repository accepts for a recipe (sd15, anima, krea2 all ship un-run recipes with a
citation). A run that finds the tenth picture ignored makes this the wrong number, and
the row's `RefInputs` is where it is corrected.

### Decision 5 — `resolution` is 1024, the one number not taken verbatim

`TextEncodeQwenImage21` resizes every reference to a `resolution × resolution` pixel budget, aspect
preserved, rounded to a multiple of 32 — and, through its latent output, that is the canvas an edit
is produced on. The two published numbers disagree: the node's own default is 1024, the template's
note calls 1024 "the official default", and the edit template's promoted widget is **0**, meaning
"keep each reference at its own size".

0 is defensible for a template author, who picks the demo assets. It is not defensible here: this
route takes whatever picture a member uploads, and 0 would encode an 8000-pixel photograph at 8000
pixels — a VAE encode and a vision-token count nothing in this path caps, on a card that is also
holding roughly 15 GiB of weights. So the rule "take the published graph's numbers" is followed
everywhere else in this template and broken exactly here, out loud.

### Decision 6 — the licence rides the existing tenant acceptance, and `qwen-research` is classified

Qwen Research License §2a grants rights "FOR NON-COMMERCIAL PURPOSES ONLY". Nothing new is built
for it: the ingest already records `license_accepted_by` / `_at` / `_tenant` / `_license` per tenant
(ADR 0072), and Flux.2 Klein 9B is already in a catalogue through that path.

What did need one line is the classifier. `engineCommercialUse` reads WORDS ("non-commercial",
"-nc"), and "qwen-research" contains none of them, so a correctly-populated row answered `unknown`.
`unknown` is a real answer for terms nobody read (ADR 0072 decision 10); it is the wrong answer for
terms somebody did. The needle is the measured spelling and nothing broader — a needle for
"research" in general would classify other vendors' licences it has not read.

### Decision 7 — the parts come from Comfy-Org's mirror, at int8, on NEW S3 keys

`--clip_l` is `text_encoders/qwen3vl_8b_int8_convrot.safetensors` and `--vae` is
`vae/qwen_image_2.1_vae_bf16.safetensors`, both from `Comfy-Org/Qwen-Image-2.1`. The mirror rather
than the publisher's own repository for the reason `Comfy-Org/Qwen-Image_ComfyUI` is already used:
both are ungated, and what a ComfyUI loader reads is the single-file split.

int8 rather than bf16 because that is what both official templates' CLIPLoader widget names, and
because 8.71 GiB against 16.33 GiB is the difference between fitting beside the weights and not —
the citable choice and the workable one are the same choice here.

🔴 The VAE key is NOT shared with the four families that point at the Qwen-Image VAE. Sharing it
would be worse than a wrong path: the reuse check would find the wrong bytes already staged and
download nothing.

### Decision 8 — the Civitai GGUF route is out of scope

The model page that prompted this work publishes three GGUF conversions. This deployment cannot load
them: **ComfyUI-GGUF is not baked into the engine image** and deliberately so
(`deploy/aws/ecs/comfyui/Dockerfile`) — a pinned, golden-tested engine does not install custom nodes
at runtime. So a GGUF quantisation ladder for this family would be a ladder to a loader that is not
there. The safetensors mirror is the ingest route, and adding GGUF support for the image role is its
own decision with its own supply-chain argument.

### Decision 9 — the family suggestion takes `Qwen 2`, whole, and `Qwen` still gets no rule

`Qwen 2` is matched WHOLE (`engineFamilyRule.equal`, anima's mechanism). As a substring the
normalised `qwen2` would take `Qwen 2.5` and `Qwen2-VL`, each time silencing `base_model_missing` on
a row this template cannot load.

The measurement is the whole argument, and it also settles a question ADR 0094 left as an
assumption. `baseModels=Qwen` is not "the Qwen-Image-Edit string" — it is a junk drawer holding five
architectures, including a text encoder. So `Qwen` keeps its deliberate absence, and `Qwen 2`, which
holds exactly one architecture across its complete checkpoint list, earns a rule and a
`engineFamilyUpstreams` entry.

⚠️ The residual risk is a FUTURE Qwen-Image release published under the same `Qwen 2`: the
suggestion would then offer 2.1's template for a topology it does not load. That is the
`Flux.2 Klein 9B-base` shape of trap (ADR 0072's follow-up), no needle pre-empts it, and what
answers it is re-measuring the table rather than trusting it.

## Rejected

### Make it a row of qwen-image-edit-2511, or a shared template with a wiring flag

The two share no file, no encode node and no autoencoder. `params` cannot express any of that (ADR
0072's `params` vocabulary is four sampler fields), and a shared builder with a branch per difference
would be two templates in one function.

### Emit `QwenImage21Cache`, as the edit template does

Its widgets there are `auto` / `default`, and the model reads
`transformer_options.get("qwen_image21_cache", {})` — absent, device defaults to `auto` and dtype to
`default` (`comfy/ldm/qwen_image21/model.py`, `select_prefix_cache`). So the node is provably a
no-op at those values, and it is `is_experimental`. Emitting it would add a node that exists only at
v0.37.0+ and changes nothing.

### Use `SaveImageAdvanced`, as both templates do

`SaveImage` keeps the alpha this model produces — it hands the raw array to PIL, so a 4-channel
picture is written as a PNG with its alpha channel (`nodes.py`, `save_images`). What `SaveImage`
also does, and this route depends on, is write the `prompt` PNG chunk `readImageProps` reads (ADR
0094 P2). Changing the save node for a cosmetic match to the template would be trading a working
props path for nothing.

### Refuse a size on `op=edit`

Symmetrical with ADR 0094 decision 4's `comfySizeRefusal`, and wrong here: this family advertises
sizes, so the pane draws a size picker, and refusing by value would reject every edit the pane sends.
The existing img2img warning is the right seam and already fires.

⚠️ That warning named the INPUT picture's own dimensions, while this family resizes to the
`resolution` budget — so the number it reported was approximate. It was approximate for the 0094
families too (FluxKontextImageScale rescales), so this was a pre-existing imprecision rather than a
new one; it was listed under "Open" and has since been fixed for every family (see "The Open
items, followed up").

## Phases

- **P0 — the ComfyUI bump (separate change).** `COMFYUI_REF` v0.35.2 → v0.37.0, rebuild via
  `.github/workflows/comfyui-image.yml`. The static half of the check is already done and is in
  "What was measured"; what it cannot cover is a real cold start on a GPU with the existing ten
  families, which is that change's own acceptance.
- **P1 — this change.** Vocabulary, parts table, family suggestion, licence classification, the
  Agent template and its goldens, the Console card. Nothing here has touched hardware.
- **P2 — live acceptance.** Ingest a row through the panel, and take one picture per op. The
  completion definition: `op=generate` at a named size returns that size, and `op=edit` with two
  references returns a picture that followed the instruction and kept the first reference's frame.
- **P3 — the numbers only a run can give.** `vram_mib` for `engineFamilyVram`, taken the reverse way
  ADR 0094 P1 had to learn (declare the hypothesis first so the intended rung is still a candidate,
  or the file-sum floor buys the bigger card and then measures on it). The int8 set totals about
  16.1 GiB, which would put the floor near the 22,000 rung before any measurement.

## P2 on real hardware (2026-09-23)

Run on the dev deployment: Control Plane and Agent `0.22.2-dev-ea8ecbe9` (which carries this ADR's
code), engine `af-comfyui:v0.37.0`, on a box the ladder had already bought — a g6e.xlarge (L40S
48GB). Everything went through the production routes: the panel's ingest API, the catalogue row,
and the Agent's own `POST /imagegen/generate`, which builds its graph with `comfyGraphQwenImage21`.

**Ingest — one press, as designed.** `…/ingest/resolve` on the int8 diffusion model came back with
all three parts planned (17.28 GB to download), `commercial_use: "no"` and `license_name:
qwen-research` — decision 6's classifier working on the live row. The press took about 15 minutes
for the three downloads. The row's `vram_need_mib` is the file-sum floor, 16,482, and enabling it
asked for `confirm_vram` because the engine's selected class is the T4 rung (14,500) — the same gate
ADR 0094 P1 met.

**What the Agent advertises for the row** is decision 3 as written: `ops: [generate, edit]`,
`max_inputs: 10`, `knobs: [steps, cfg, sampler, scheduler]` — `negative` gone because the recipe's
cfg is 1, `strength` gone because the denoise is fixed — and the megapixel size list.

**Text-to-image: passed.**

| request | result | engine |
|---|---|---|
| `1216x832`, a photograph | HTTP 200, 59.6 s end to end, a `1216x832` PNG | `latent_shapes=[(1, 64, 52, 76)]`, 58.1 s |
| `1024x1024`, the templates' RGBA wording | HTTP 200, 27.3 s | `(1, 64, 64, 64)`, 26.0 s |

The engine's own log confirms the model's shape: 64 channels at a downscale of 16, reached from the
4-channel `EmptyLatentImage` by `fix_empty_latent_channels`'s spatial half. The size the caller
named reached the output, which is decision 2's generate half.

The RGBA request came back transparent: 17.6 % of pixels at alpha 0, the rest along soft edges.
⚠️ The ordinary photograph came back RGBA as well, with alpha between **252 and 255** (47 % of the
pixels below 255). Invisible on screen, but composited over a background it is up to 1.2 %
see-through. That is the first measured answer to Open 4, and it is a property of the model's
decode rather than of `SaveImage`.

**Edit: failed, every time, on a key this ADR's own tests pinned.** The first two-reference edit
returned HTTP 502 in 1.2 s, and the engine's log said why:

```
TextEncodeQwenImage21.execute() got an unexpected keyword argument 'image_1'
```

The references are a V3 **Autogrow** group named `images`, and the API-format id of each member is
the group and the member joined by a dot — `images.image_1` (`comfy_api/latest/_io.py`,
`finalize_prefix`). `image_1` is only the label the editor draws; the published template's JSON
carries both, and the template builder took the wrong one. `/prompt` accepts an unknown optional
key, so the graph validated and failed inside the node. Generate never reaches the loop, which is
why only the edit half was broken.

🔴 The goldens were green throughout, and one test (`…WiresEveryReferenceOntoTheOneEncode`)
asserted the bare key — it pinned the bug. This is Open 1 happening exactly as written: a golden
fixes the graph's shape, and the shape was what was wrong.

**The fix, and its positive control on the same box.** The builder now writes `images.image_N`, and
the test asserts it and refuses any other `image*` key. The graph the FIXED builder emits for the
same request — the fox as `image_1`, the transparent scarf as `image_2` — was submitted straight to
the engine through the gateway (the deployed Agent still carries the old builder): `execution_success`
in about 35 s, the scarf wrapped round the fox's neck, the forest, rock, light and framing unchanged.

The output was **1248x832** for a `1216x832` first reference: the node rescales to the 1024² budget
at multiples of 32 (`round(sqrt(1024²·1.4615)/32)·32 = 1248`). That is the measured size of Open 2's
imprecision — the img2img warning would name 1216x832.

**Not taken, on purpose.** No `vram_mib`: the L40S has room to spare, so the reading would be the
whole file set resident — exactly the value ADR 0094 decision 8 says is not a value of that field.

**The fix through the Agent, after it was deployed (2026-09-23, `0.22.2-dev-ef644598`).** The same
two-reference edit, sent to the Agent's own `POST /imagegen/generate` rather than to the gateway:
HTTP 200 in 35.7 s, a `1248x832` picture — and **pixel-identical** to the gateway positive control
above (maximum per-channel difference 0). The deployed Agent builds exactly the graph the fixed
builder emits, which closes the edit half of P2.

## P3 on real hardware — the T4 question (2026-09-23)

The one `vram_mib` worth taking was the one that could change what the deployment buys. The
file-sum floor (16,482 MiB) already selects the 22,000 rung, so a reading on an L4 or an L40S would
buy the same box; only a value that fits the **T4 rung** (g4dn, 14,500 declared, $0.34/h — the
engine's default class) moves the purchase. So the run was done ADR 0094 P1's reverse way:
declare a hypothesis (`vram_mib: 14000`) so the rung is a candidate, pin the class to `g4dn-spot`,
replace the box, and read the engine's own `/system_stats` once a second.

| | generate, 1024² | edit, two references |
|---|---|---|
| result | HTTP 200, a clean picture | the engine finished; the picture followed the instruction |
| sampling | **8.75 s/step**, prompt 249 s | **34.8 s/step**, about 15 minutes |
| peak VRAM in use (of 14,912) | 11,863 MiB, 3,048 free | 13,757 MiB, 1,154 free |
| peak host RAM (of 15,791) | 13,720 MiB | 13,738 MiB |

It **fits**, and the card was tight — the text encoder was evicted after encoding and the weights
were streamed in and out through the edit — so by ADR 0094 decision 8 these ARE values of the
field. The T4 has no bfloat16, and the engine said so: `model weight dtype torch.bfloat16, manual
cast: torch.float32`. The picture quality survives the fp32 compute (the edit differs from the
L40S run by at most 94 in a channel and reads the same); the time does not.

**Decision: declare nothing.** Against the same edit on the g22 rung (35 s at $1.35/h, about
$0.013), the T4 costs about $0.085 for one edit — the hourly rate is a quarter, the time is
twenty-five times — and 15 minutes sits on the Agent's own 15-minute run budget. Declaring 13,757
would make the ladder buy the slower AND dearer card whenever this row sets the engine's need. The
row keeps the floor, and the measurement stays here as the reason rather than in `vram_mib`.
(Today krea2's 17,774 sets the engine's need anyway; the declaration would have bitten the day
that row is switched off.)

⚠️ **The edit on the T4 did not come back through the Agent.** At 74 s the Agent answered
HTTP 502: `/history` returned 503 because the Control Plane's `DescribeServices` call was cancelled,
and a 503 on `/history` is not retried (the gap ADR 0072's P2 follow-up already names). The engine
kept going and finished, and the picture was fetched from the gateway. That is a slow-card failure
of the polling path, not of this family, and it is left to that gap rather than fixed here.

The engine was put back as it was: hypothesis withdrawn (`vram_mib: 0`), class unpinned, box
replaced — the ladder bought a g6e.xlarge on `g22-spot` again.

## The Open items, followed up (2026-09-23)

The four items P2 left open, taken on the dev deployment (`0.22.2-dev-ef644598`, the engine on a
g6e.xlarge / L40S, nothing on the GPU ladder touched) and in code. The requests went through the
Agent's own `POST /imagegen/generate`; the one graph the deployed Agent cannot yet build went
straight to the gateway, as in P2.

### Open 2 — the img2img size warning now names the size the picture was MADE at

The warning used to name the first reference's own dimensions, which is wrong twice over on the
families that rescale: a 1216x832 reference here comes back 1248x832 (the encode's 1024² budget at
multiples of 32), and a caller who asked for exactly 1216x832 was told nothing at all, because the
size asked for equalled the input's.

Computing each family's canvas was the option this item asked about, and it was not taken. It would
mean re-deriving the arithmetic of two nodes (FluxKontextImageScale's preferred-resolution pick,
TextEncodeQwenImage21's budget and rounding) in Go — a second copy that the next pin bump changes
silently. The route already decodes every output's header (`viewOne`), so the warning is now
composed after the fetch from the MEASURED size of the first picture (`comfyImg2ImgSizeWarning`):
it fires whenever the size asked for differs from the size made, names the input's dimensions
(what the caller can actually change) and the output's, and falls back to the input's alone if
the output could not be read. It covers the 0094 families too — no per-family code.

### Open 3 — native 2K: offered, after one run

| request (through the Agent) | result | end to end | peak VRAM in use (of 45,457) |
|---|---|---|---|
| `1024x1024`, the fox, seed 1234 (the model loads here) | HTTP 200, a clean picture | 44.2 s | 16,706 MiB |
| `2048x2048`, the same | HTTP 200, one subject, no tiling, fine fur detail | 116.3 s | 19,592 MiB |

2048² was already inside the route's ceiling — `imagegenMaxPixels` is 4<<20, exactly 2048², and
the check is `>` — so the only thing stopping a member was the list. This family's row now declares
its own `Sizes`: the five megapixel presets, then the same five shapes at twice the side
(`2048x2048`, `2304x1792`, `1792x2304`, `2432x1664`, `1664x2432`). Only the square was run; the
other four are fewer pixels than it, and every side is a multiple of 32 for the 16x latent. The
first entry stays 1024², so a request that names no size costs what it did. The Console's
fallback card (`families.ts`) carries the same ten.

The price is time rather than memory: about 2.6x the 1024² end-to-end time on this card, and the
2048² peak (19.6 GB) is still under the 22,000 rung the file-sum floor already buys. It was not
run on a T4; P3's timings there make 2K on that card a quarter-hour per picture at best.

### Open 4 — RGBA: stripped unless asked for, and the consumers checked

**Downstream of the engine, nothing breaks on transparency.** The props reader walks PNG chunks
without decoding and ignores the colour type; the thumbnailer (`fs_thumb.go`) chooses JPEG only for
an opaque picture and keeps PNG with alpha otherwise, never compositing onto black; the Console's
cards show the panel tint through transparency and the lightbox a checker; no canvas flattens to
JPEG; and every `LoadImage` wires only its IMAGE output, so an RGBA picture sent back as a
reference never becomes a mask by accident.

**The cost was the near-opaque photograph.** One pixel below 255 makes `Opaque()` false, so every
thumbnail of an ordinary picture from this family was a PNG (five to eight times a JPEG) and the
lightbox served the 1-2 MB original instead of its preview — and composited over a background it
was up to 1.2 % see-through.

**Decision (the user's): opaque unless transparency was asked for.** A new row field, `Alpha`,
declares the one family whose decode carries an alpha channel. For it the template puts
`SplitImageWithAlpha` (`image[..., :3]`, a no-op on three channels) between the decode and the
save unless the request says `background=transparent`, and `comfyWarnings` stops telling that
caller "opaque produced". Stripping in the graph rather than re-encoding in the Agent keeps the
`prompt` chunk SaveImage writes. Checked on the engine: the stripped graph saved an `RGB` PNG with
the chunk intact, pixel-identical to the RGB channels of the same request's RGBA output.

⚠️ What lies under an alpha of 0 is not white: the transparent-background picture from P2 holds a
flat purple there (about 163,58,206). A caller who asks for transparency in the prompt but not in
`background` now gets that purple as the background. `generate_image` has the `background`
argument; **the Console's generate form does not**, so from the pane this family is now opaque
only (Open 6).

**Found on the way: props lost this family's prompt.** `TextEncodeQwenImage21` emits both
conditionings from one node, with the prompt in `prompt` and the negative in `negative_prompt`, and
the props reader chose the field by class — so every picture of this family, generate or edit,
came back with no prompt (read off the dev deployment's own output). The field is now chosen by the
output slot the sampler's edge names, and the references are read as `img1..imgN` in order rather
than the first `LoadImage` found.

### Open 5 — the prompt enhancers: wireable, not wired

The Comfy-Org mirror's README says the `qwen3.5_9b_…_pe_{t2i,i2i}` files are "to be used with the
TextGenerate node", and at v0.37.0 that is true with core nodes alone: a Qwen3.5 state dict is
detected by shape whatever the CLIPLoader's type (`comfy/sd.py`, the `QWEN35_*` branch), the 9B
config carries an `lm_head`, and `TextGenerate` → `RegexReplace` (drop the `<think>` block) →
`JsonExtractString` (`rewritten_prompt`) → the encode's `prompt` is a graph /prompt accepts. The
upstream model cards (`Qwen/Qwen-Image-2.1-PE-T2I` / `-I2I`) ship a `system_prompt.txt` (10 KB and
18 KB) and answer a JSON object after a thinking block.

It is not wired, for four reasons, each measured off the files rather than a run:

- **Memory.** Each enhancer is 9.47 GB at int8, beside the 16.5 GB this row already holds. ComfyUI
  can evict between the two, but the file-sum floor rises from 16,482 to about 25,500 MiB — past
  the 22,000 rung this family runs on today.
- **Time.** The upstream example enables thinking with `max_new_tokens=24000`. A thinking 9B model
  in front of every picture is minutes of text generation on a card bought for images.
- **It decides the frame.** The T2I answer carries `wh_ratio` — the enhancer picks the aspect
  ratio — which contradicts the size the caller named; the I2I answer's `ratio_follow` is the
  same question for edits.
- **It fails silent.** `JsonExtractString` returns an empty string on a parse failure, so an
  enhancer that answers anything but clean JSON would sample the picture from an empty prompt, with
  no error and no warning.

And the route already has a better enhancer in front of it: `generate_image` is called by an agent
that IS a language model. If this is taken up, the cheaper shape is to give that caller the
upstream system prompt (the knowledge layer for this family) rather than a second model on the GPU.

## Consequences

- The vocabulary is now eleven families, and the four copies of it (Agent constants, CP
  `engineComfyFamilies`, Console `wire.ts`, Console `families.ts`) all carry the new word. Three of
  those are pinned against each other by `engine_catalog_test.go`; **`families.ts` is not**, and
  that fourth copy is held by this ADR alone (ADR 0072's standing note).
- `comfyFamilyRow` carries three more fields. A family added to the table and leaving them at their
  zero values gets the permissive default — three ops, one reference, `strength` offered — rather
  than an error. That is the same shape `TrialSteps` already had, and it is covered the same way:
  by tests that walk the whole vocabulary rather than by a list of families.
- `comfyFamilyRow` gained a fourth field in the follow-up, `Alpha`, declared on this family alone
  (a test pins that). A family added later whose decode also writes alpha has to declare it AND
  strip in its template, or every caller gets the stray alpha back.
- The engine image pin is now load-bearing for a family that is in the catalogue vocabulary. Until
  P0 lands, an operator can register and enable a `qwen-image-2.1` row and every screen will say it
  is complete; it fails at `/prompt`, loudly, with a node-type error.
- `engineCommercialUse` answers `no` for any row whose licence name carries `qwen-research`, which
  reaches the existing Qwen-Image-Edit rows too — those are Apache 2.0 and unaffected, but a future
  Qwen model published under the research licence is now classified without another change.

## Open

1. ~~**Nothing has been run.**~~ Answered by P2 and P3. The recipe, the reference count and the
   shift are still the published graph's numbers; the whole path now runs.
2. ~~**The img2img size warning reports the input's own dimensions.**~~ Fixed for every family: the
   warning names the size measured off the output (see "The Open items, followed up").
3. ~~**Native 2K is unexercised.**~~ 2048² run through the Agent; the family now offers 2048-class
   sizes.
4. ~~**RGBA end to end is unverified.**~~ The consumers were checked, and the alpha is stripped
   unless `background=transparent` was asked for.
5. **The prompt-enhancer models are unused — now on purpose.** Wireable with core nodes; not wired
   for memory, time, the aspect ratio it imposes and a silent empty-prompt failure. The cheaper
   shape, if wanted, is the upstream system prompt handed to the calling agent.
6. **Transparency is unreachable from the Console.** The generate form has no `background`
   control, so the pane now always gets this family's opaque output; only `generate_image` can ask
   for `background=transparent`. A reproduction from props cannot either: `ImageProps` carries no
   background (a transparent picture is recognisable only by the missing `SplitImageWithAlpha`).

# 0098. Qwen-Image 2.1 as an image family — one template that generates and edits, and a VAE that shares nothing

English | [日本語](0098-qwen-image-21-family.ja.md)

- Status: **proposed, and built in the same change** (2026-09-21). The code is in the tree and the
  tests are green, but **nothing here has been run on a GPU** — and it cannot be until the
  prerequisite below lands, so "built" means the templates and the vocabulary, not a picture.
- 🔴 **Prerequisite (P0): the ComfyUI pin has to move to v0.37.0.** `TextEncodeQwenImage21` does
  not exist before that tag, so a row of this family reaches the engine and is refused at
  `/prompt` validation. That bump is a separate change (see "Phases"), and nothing in this ADR
  works without it.
- Every upstream fact below was MEASURED against the live APIs and the pinned engine's own source
  on 2026-09-21. The measurements are in "What was measured"; where a number was taken from a
  published graph rather than from a run, the text says so at the point it is used.
- Number: 0097 is the highest on `develop`, and no open pull request claims 0098.
- Related: [0072](0072-engine-model-catalog.md) (the per-family templates, `base_model` dispatch,
  and decision 2's rule that an operator declares the family) / [0094](0094-instruction-edit-image-models.md)
  (instruction editing, and the two Qwen-Image-Edit families this one is NOT a version of) /
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

Before this, one predicate answered three questions at once, because the only instruction-edit
families were also edit-only. This one is the counter-example, so the axes separate:

- `comfyFamilyInstructionEdit` keeps its meaning — the picture conditions the sampler and the
  denoise is fixed at 1 — and this family answers **true**. That is what makes `strength` not reach
  it (ADR 0094 decision 2).
- `comfyFamilyEditOnly` is the narrower fact, and this family answers **false**. It is what the op
  set and "can a size reach the sampler" actually turn on.
- The op set and the reference-picture count become tables rather than predicates.

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
`comfyFamilyRefInputs` is where it is corrected.

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

⚠️ That warning names the INPUT picture's own dimensions, while this family resizes to the
`resolution` budget — so the number it reports is approximate. It is approximate for the 0094
families too (FluxKontextImageScale rescales), so this is a pre-existing imprecision rather than a
new one; it is listed under "Open" instead of being fixed inside this change.

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

## Consequences

- The vocabulary is now eleven families, and the four copies of it (Agent constants, CP
  `engineComfyFamilies`, Console `wire.ts`, Console `families.ts`) all carry the new word. Three of
  those are pinned against each other by `engine_catalog_test.go`; **`families.ts` is not**, and
  that fourth copy is held by this ADR alone (ADR 0072's standing note).
- `comfyFamilyOps` and `comfyFamilyMaxInputs` are tables. A family added to the constants and
  forgotten here gets the permissive default — three ops and one reference — rather than an error,
  which is the same shape as `comfyTrialSteps` and is covered the same way, by a test over the
  whole vocabulary.
- The engine image pin is now load-bearing for a family that is in the catalogue vocabulary. Until
  P0 lands, an operator can register and enable a `qwen-image-2.1` row and every screen will say it
  is complete; it fails at `/prompt`, loudly, with a node-type error.
- `engineCommercialUse` answers `no` for any row whose licence name carries `qwen-research`, which
  reaches the existing Qwen-Image-Edit rows too — those are Apache 2.0 and unaffected, but a future
  Qwen model published under the research licence is now classified without another change.

## Open

1. **Nothing has been run.** The recipe, the reference count, the shift and the whole edit path are
   citations from published graphs. The goldens pin the graph's SHAPE, which is exactly the claim
   that was true of SD3.5 while it could not produce a single picture (ADR 0072 P2 残作業 5).
2. **The img2img size warning reports the input's own dimensions**, not the size the picture is
   actually made at after the `resolution` resize. Pre-existing and shared with the 0094 families.
3. **Native 2K is unexercised.** The templates' note says 2048² is supported directly and suggests a
   4-megapixel ResolutionSelector for it; `comfyMegapixelSizes` stops at 1024-class presets, so a
   member cannot ask for it. Whether to widen the size list for this family is a question a run
   should answer first.
4. **RGBA end to end is unverified.** The model writes an alpha channel and `SaveImage` keeps it,
   but nothing downstream of the engine — the props reader, the gallery, the mirror's cards — has
   been checked against a PNG with transparency.
5. **The prompt-enhancer models are unused.** `qwen3.5_9b_…_pe_{t2i,i2i}` ship beside the encoder
   and are a documented part of the official pipeline. They are not in the ComfyUI templates, and
   nothing here declares them.

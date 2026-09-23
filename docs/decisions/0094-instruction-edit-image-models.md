# 0094. Instruction-edit models in the image engine — Qwen-Image-Edit, and `op=edit` meaning two different things

English | [日本語](0094-instruction-edit-image-models.ja.md)

- Status: **P0 through P3 all built and accepted on real hardware** (2026-09-20 to 21). Each
  phase's completion criteria and its acceptance record are in the "Phases" section; P0's is also
  written out at the end.
  🟢 The `file:line` references below have been **re-pointed at develop with P3 in it**
  (2026-09-21). Each was resolved from the SYMBOL it names rather than by counting, and **all 64
  were then verified to land on it** — a line number nobody checked is worse than an old one,
  because it reads as current. Whoever moves those lines next does the same.
  🟢 **Reviewed before implementation** (2026-09-20, in a separate session). The nine findings are
  folded into the text; what came off is recorded in "What the review took off" at the end.
  🟢 **The graph and the capabilities were MEASURED before this was written** (dev deployment,
  g6.xlarge / L4 24GB, ComfyUI 0.35.2, five runs on 2026-09-20). The "Measured" section is that
  record, and **decisions 2, 3, 4 and 5 each rest on it**.
  🟢 The number is settled (develop holds up to 0093).
  🔄 **Revised (2026-09-23): stop centre-cropping; shrink the whole picture to the encoder's rounding
  fixed point** — the "Revision" section at the end. Decisions 3 and 4, "Rejected" and open item 2
  carry a pointer where they stand. **Decided, not built.** 🔴 **Today's wiring very likely makes 3:2
  photos, among others, soft** (the revision's point 3; one measured pair; fixed as part of building it).
- Related: [0072](0072-engine-model-catalog.md) (the per-family templates, `base_model` dispatch
  and the file-role vocabulary — this ADR adds two words to the first and none to the last) /
  [0069](0069-image-generation-providers.md) (`generate_image`'s vocabulary, and the `strength`
  addendum — **decision 2 applies that addendum's own rule to this family and comes out the other
  way**) / [0081](0081-image-generation-pane.md) (the generation pane; decision 4's "a knob the
  family does not read is reported, not swallowed"; **its rejected list's "custom workflow", which
  decision 9 keeps rejected**) / [0085](0085-model-ledger-and-one-press-ingest.md) (one-press
  ingest, parts are never the subject — decision 7 adds one family to that table) /
  [0074](0074-engine-instance-classes.md) (classes and the VRAM guard — decision 8)

## Background

The request (operator, 2026-09-19) was: can we put the qwen-image-edit workflow onto our ComfyUI
and **use it while customising it**. Upstream's tutorial is
`https://docs.comfy.org/tutorials/image/qwen/qwen-image-edit`; the shipped templates are
`image_qwen_image_edit_2509` and `image_qwen_image_edit_2511` in `Comfy-Org/workflow_templates`.

**This family is a different KIND of model from every one this deployment holds.** All eight
existing families make a picture from text, and `op=edit` bolts `LoadImage` → `VAEEncode` onto
that family's graph and runs the sampler at a `denoise` below 1, which is how much of the
caller's picture changes — **partial-denoise img2img** (`comfy_workflows.go:302`,
`comfyRequestLatent`; the default is `comfyEditDenoise = 0.6`, line 300).

Qwen-Image-Edit is an **instruction editor**, and what preserves the original comes from somewhere
else entirely. The input image goes through `TextEncodeQwenImageEditPlus` and enters the
conditioning **twice** — as vision tokens (384²) and as `reference_latents` (~1 MP) — while the
sampler runs at **`denoise` 1.0** (that is the widget value in the shipped template). At denoise 1
the `VAEEncode` latent effectively decides nothing but the output canvas size. (Read off
`comfy_extras/nodes_qwen.py`'s `execute` in the pinned v0.35.2 source rather than assumed.)

So **one word, `op=edit`, names two different mechanisms depending on the family**. What happens if
they are not told apart was measured: **a picture comes back with nothing edited, and no error**
(run C).

### What the measurement settled (dev deployment, 2026-09-20)

| # | Condition | Wall | Result |
|---|---|---|---|
| A | 2509, steps 20, cfg 4, **denoise 1** (the published recipe) | 226.2 s | edited as instructed |
| B | 2509, **steps 8** | **63.2 s** | edited as well |
| C | 2509, **denoise 0.6** (today's `op=edit` default) | 152.8 s | **not edited** (the original picture) |
| D | 2509, **two reference images** | 268.0 s | the second picture's object was transplanted into the first |
| E | **2511**, steps 40, shift 3.1, reference-method node | 393.8 s | edited |

A includes the first load of the 20.43 GB model, E the switch to 2511 (20.53 GB). Buying the box
and syncing 30 GB onto it cost a further 348.9 s. The card was an **L4 24GB** (22,563 MiB total,
1,701 MiB free after a generation), the host had 16.1 GB of RAM.

## Decisions

### Decision 1 — Add two families. Add no file-role words

`base_model` gains `qwen-image-edit-2509` and `qwen-image-edit-2511` (see decision 6 for the
spelling). Both declare three roles — `--diffusion-model`, `--clip_l`, `--vae` — so **`EngineFile`'s
flag vocabulary does not grow at all** (two lines in `engineComfyRequiredFlags`,
`control-plane/engine_catalog.go:196`). The text encoder is Qwen2.5-VL-7B (declared as `--clip_l` by
the existing convention for a family with one encoder), and the VAE is the Qwen-Image one — **the
same key anima and krea2 already hold**.

The vocabulary is declared twice, in the CP (`engine_catalog.go:170`) and in the Agent
(`comfy_workflows.go:401`), and `engine_catalog_test.go` reads the Agent's source to keep them
equal. **Add it in both.**

### Decision 2 — This family does not take `strength`. Its `denoise` is fixed at 1

🔴 **Run C is the whole of this decision.** The same seed and the same prompt, with `denoise` at
0.6, came back with the sign unchanged — **and nothing said so**. Today's edit path returns 0.6 by
default (`denoise()`, `comfy_workflows.go:270`), so **dropping this family into it makes "I asked
for an edit and nothing happened" the default behaviour**.

- Make `Caps.Strength` a per-family answer (it is `true` for everything today, `comfy.go:144`) and
  answer **false** here.
- A caller that sends `strength` anyway is **refused with 400** (the shape `bad_strength` already
  uses), not warned. Run C was "a wrong picture with no warning", so a path that accepts the number
  and quietly drops it leaves the failure looking the same from outside. ADR 0081 decision 4's
  "report, do not swallow" is a rule about values the CATALOGUE declared (the operator's, written
  long before this request); **a value the caller just typed is better refused**.
  🔴 **Refuse only when the RESOLVED model is of this family** — a request that names no provider
  may not land on comfy at all, so "a `strength` present means 400" would be wrong. Resolve
  model → family at the edge (`comfyFamilyFor` is in the same package).
- 🔴 **One route cannot be refused, so a warning catches it — in the CORE, not in the provider.**
  A request naming neither `model` nor `provider` cannot have its family resolved at the edge, so it
  reaches comfy and `strength` is dropped by the denoise-1 graph. `requestWarnings`
  (`imagegen.go:1028`) has a `!caps.Strength` branch that never fired, and the reason was the CALLER:
  it passed the Caps of the model the REQUEST named, which is decision 11's union (true) when that is
  empty.

  So **the Caps handed to `requestWarnings` is the row that actually ran** — `Run`
  (`imagegen.go:927`) and the queue's `finish` (`jobs.go:473`) pass `Caps(res.Model)`.
  🟢 **A strength-shaped twin on the provider side is rejected** (this ADR's first draft asked for
  one): it patches a core mistake inside comfy alone, **the same mistake was already live for
  `negative`**, and the next capability that becomes per-model would repeat it. One fix in the core
  gives every provider the same guarantee. The price is that the sentence goes from naming the
  family to the generic "this route cannot vary how much of the input it keeps"; putting the family
  name back belongs in the core's message or in `Caps` carrying a reason — a different decision.
- ADR 0069's rule for adding a word ("is there no way around it for the caller") **points the other
  way here**: it is not that the caller has no workaround, it is that the knob has no meaning.

### Decision 3 — `Ops` becomes a property of the family. This one has no `generate`

`comfy.go:121` answers `[generate, edit, inpaint]` for every model. Qwen-Image-Edit **cannot work
without an input image** (with no image the encoder is plain text conditioning, which is not a use
upstream documents). Make `Ops` per family and let this one not claim `generate` at all.

`inpaint` was **not claimed at drafting**. A mask could be added with `SetLatentNoiseMask` and the
graph would validate, but nobody here had run it — **a capability this deployment has not measured
is not one it declares** (the lesson SD3.5 charged us in ADR 0072).

🟢 **It was run on 2026-09-21, so it is claimed (実測 G and I, unresolved 2).** `Ops` is
`[edit, inpaint]`. The evidence is the control, not the happy path: the prompt that changes the
sign, sent with a mask that does NOT cover the sign, leaves it unchanged, while the same request
with no mask changes it — the mask really is a gate even at a full denoise.

🔴 **The wiring is NOT the two nodes the other eight families use** (`comfyQwenEditNoiseMask`).
The picture goes through `FluxKontextImageScale`, which **centre-crops** to the nearest trained
ratio before resizing, while `SetLatentNoiseMask`'s mask is only stretched to the latent's shape
with no crop — **the two maps disagree**. Measured (実測 I) on an 1820x1024 input (cropped to
1820x984, then 1392x752): the repainted band's edge sat **8 px** from where the picture's own map
puts it. Sending the mask through the same `FluxKontextImageScale` moves it back (`LoadImage` →
`FluxKontextImageScale` → `ImageToMask`(red) → `SetLatentNoiseMask`). The error is zero at the
centre of the frame and worst at the edges, and **nothing warns about it**.

🔴 **For the same reason the mask must be the picture's own size, or it is refused** (`Generate`
in `comfy.go`). That node resolves its target from the width and height it is handed, so a mask of
another shape resolves a different frame and **repaints somewhere the caller did not draw**. The
other families have no crop, where a different size is still the same relative region — so this
restriction belongs to this family alone.

🔄 **Revised (2026-09-23): `maskscale` and this size restriction go** — without the crop, picture and
mask are both plain stretches and the two maps agree ("Revision" at the end).

⚠️ **qwen-image-2.1 (ADR 0098) does not inherit the claim.** 2509 and 2511 may share a measurement
because they share a builder and a wiring (`comfyQwenEditNoiseMask`), not because their names look
alike. 2.1 has a template of its own and no `FluxKontextImageScale` at all — **a capability this
deployment has not measured is not one it declares** applies there unchanged.

### Decision 4 — `size` offers no choices for this family, and the row cannot override it

The output size is `FluxKontextImageScale` picking the nearest entry of
`PREFERRED_KONTEXT_RESOLUTIONS` by the **input's aspect ratio** (read off the node's v0.35.2 source;
the five runs went 1024² in → 1024² out, which — being all square — does **not** evidence the ratio
table). `comfySizesFor` (`comfy.go:605`) answers **empty** for this family, and a `size` that
arrives anyway is refused the way decision 2 refuses `strength`.

🔴 **The row's own `sizes` are not honoured either.** `comfySizesFor` returns `conn.Sizes[model]`
ahead of the family's list (`comfy.go:604`), so an empty family answer alone would still let an
operator's declared presets through. This family is the one case where **the family wins over the
row** — the point is not to offer a value that cannot take effect, and that reason belongs in this
line of the ADR.
- The Console has the same hole: `sizeOptions` (`families.ts:179`) falls back to
  `familyCard(f)?.sizes ?? DEFAULT_SIZES`, so **a card that omits `sizes` shows the megapixel
  list**. The card states `sizes: []`, and the field itself is not drawn when the list is empty.

🔴 **Dropping `FluxKontextImageScale` to honour `size` is rejected** — see below.

🔄 **Revised (2026-09-23): the picture is shrunk whole, to the encoder's rounding fixed point, instead of
cropped.** No ratio table is used; `size` is still not offered ("Revision" at the end).

### Decision 5 — `MaxInputs` becomes per family. P0 stays at one; the second image opens with its path in P3

`TextEncodeQwenImageEditPlus` takes `image1..image3`, and **run D proved the second one works** (the
potted plant from the second picture entered the first scene with its colour and shape intact).
Make `Caps.MaxInputs` (fixed at 1, `comfy.go:134`) per family.

🔴 **Declaring 2 does not carry a second image.** Today `p.uploadImage(…, req.Inputs[0])`
(`comfy.go:1036`) uploads **one**, and `comfyParams.Image` is a single string
(`comfy_workflows.go:140`). `comfyCheckInputs` (`comfy.go:1136`) only tests
`len(req.Inputs) > caps.MaxInputs`, so raising the number alone lets a second image **pass the check
and go unused** — run C's failure mode exactly: a wrong picture with no warning. Therefore:

- **P0 keeps `MaxInputs` at 1**, and the second image opens **with its path, in P3** (make
  `comfyParams` plural, wire `image2`, and fix the refusal's singular wording).
- 🔴 **The third opens after it is measured.** "The node takes `image3`, so the mechanism is the
  same" is exactly the inference decision 3 forbids for inpaint. Measure three once in P3.
  🟢 **Measured — run F** (2026-09-21, 2511, on a 22,000 rung). Asked for the plant from picture 2
  and the rubber duck from picture 3 side by side on the table, **both arrived with their colour
  and shape intact and nothing else moved**. So `MaxInputs` is **3**, and it stops there because
  **the node takes image1..image3** — a family ceiling, not a step towards a larger one.

⚠️ **`MaxInputs` is not on the wire** (`providerStatus` has no field for it; the only place it
reaches the outside is the refusal at `comfy.go:1136`). For the pane to state the limit, P3 has to
add the field.

🟢 **Done in P3.** `comfyFamilyMaxInputs` answers 3 for the instruction-edit families (run F above), and the path
opened in the same change (`comfyParams.Images` plural, every `req.Inputs` entry uploaded, `image2`
wired, the refusal pluralised). The wire gained **two** fields, not one:
`providerStatus.max_inputs` (a **union** — the MCP tool schema is a connect-time snapshot with no
model chosen, so a union is the only honest ceiling) and `modelStatus.max_inputs` (**per model** —
the pane needs the chosen checkpoint's own answer). The same pairing as decisions 11 and 12.

🔴 **The wiring is run D's graph, and reading it off the node list gets it wrong.** The second
picture does **not** go through `FluxKontextImageScale`: that node fixes the **frame**, the frame is
image1's, and scaling the second to the first's aspect ratio crops away the object being borrowed.
It goes into **both** the positive and the negative `TextEncodeQwenImageEditPlus` (CFG subtracts the
two conditionings, so a reference on one side only leaves its own encoding in the difference). The
latent still comes from the **scaled image1**.

### Decision 6 — The unit of a family is the **topology, not the version**. 2509 and 2511 are wired differently, so they are two

2511's template is **a different topology**: `FluxKontextMultiReferenceLatentMethod(index_timestep_zero)`
sits on both conditionings and `ModelSamplingAuraFlow`'s shift moves 3.0 → 3.1 (steps and cfg come
from the switch's false branch: 40 and 4). **Rewiring nodes cannot be expressed by a row's `params`**
(four words: steps, cfg, sampler, scheduler).

🔴 **The rule is "one family per topology", not "one per version".** The difference will matter:
if 2512 ships with 2511's wiring, **no family is added and the row's `params` suffice**. What adds a
family is upstream inserting or removing nodes, not a version number going up.

The strongest support for the rule is in props: `comfyFamilyFromPrefix` (`props.go:536`) recovers the
family from `SaveImage`'s `af-<family>`, so **two topologies inside one family make it impossible to
say afterwards which graph drew a picture** (ADR 0081 decision 3's "what this was made from" becomes
a lie).

- **Both spellings carry the version**: `qwen-image-edit-2509` and `qwen-image-edit-2511`.
  `base_model` is a string that lives in `engine_models`, so changing it later is a migration of
  deployed catalogues — **decide it now**. The asymmetric pair (`qwen-image-edit` / `…-2511`) becomes
  meaningless the day 2512 arrives with 2509's wiring. **A family name points at the version that
  first shipped that topology; it is not an alias for a version** — that sentence goes into the
  ingest UI's family selector.
- ⚠️ **The cost: one LoRA row per family.** `comfyResolveLoras` (`comfy.go:676`) matches `base_model`
  exactly, so the Lightning LoRA has to be registered twice. "Leave Lightning to the row" (rejected,
  below) stops being one row the moment the family splits.
- The number: one family is about **12 declarations** (`engineComfyFamilies`,
  `engineComfyRequiredFlags`, `engineFamilyParts`, `comfyFamilies`, the template switch,
  `comfyFamilyKnobs`, `comfyFamilyTakesNegative`, `comfyFamilyRecipes`, `comfyTrialSteps`,
  `wire.ts`'s `Family`, `FAMILY_CARDS`, the goldens). Decision 9's trigger is drawn from that number.

### Decision 7 — Ingest stays one press: add one family to the parts table

Add two parts to `engine_family_parts.go:79`. `engineFamilyPart` has **four fields — Flag, Repo,
File, S3Key** — and the S3 key alone is not enough:

| Flag | Repo | File | S3Key |
|---|---|---|---|
| `--clip_l` | `Comfy-Org/Qwen-Image_ComfyUI` | `split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors` | `image/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors` |
| `--vae` | `circlestone-labs/Anima` | `split_files/vae/qwen_image_vae.safetensors` | `image/vae/qwen_image_vae.safetensors` |

🔴 **The VAE is declared from `circlestone-labs/Anima`, the same repository anima and krea2 use.**
The 🔴 above that table says why: reuse is decided on artifact identity
(`hf:<repo>@<rev>/<path>#sha256:…`), not on the hash — **the same bytes declared from another
repository are a second download and a second key**. 2511 names the same two parts.

- The **scaled** fp8 encoder is the right one. krea2's note ("the fp8 conversion breaks the vision
  tower") does not apply here: the shipped template names this exact file, and runs A and D read
  reference images with it.
- The weights are fp8 (2509: 20.43 GB, 2511: 20.53 GB). bf16 (40.86 GB) does not fit an L4.

### Decision 8 — Leave the VRAM guard's estimate alone, and let the OPERATOR declare the measured number

`engineModelVramNeed` (`control-plane/engine_class.go:253`) floors the demand at the **sum of the
row's files**. This family sums to 20.43 + 9.38 + 0.25 GB = **28,676 MiB**, so on the L4 rung
(22,000 MiB, declared by this deployment's `ImageOffers`; a rung is declared BELOW the card's
physical size, and `engine_class.go:37` says why) it always asks for `confirm_vram`.

**Measured** (`/system_stats`, raw): `vram_total` 23,659,151,360 B = **22,563 MiB**, `vram_free`
1,783,934,774 B = 1,701 MiB, so **20,862 MiB in use** — inside the rung. The conditions were
**1024², batch 1, one reference image**. `comfyMaxBatch` is 4 (`comfy.go:661`) and **that range was
not measured** — re-measure in P3, where the second reference opens (decision 5).

Do **not** change the formula — subtracting a text encoder would be a lie for other families. Who
writes `vram_mib`, then:

🔴 **Not the ingest.** `vram_mib` is defined at `engine_class.go:236` as the operator's OWN
measurement, and only the admin PATCH writes it (`engine_admin.go:1140`, `SetEngineModelVram`; 0
means "the measurement is withdrawn"). Letting a machine fill in a family default **changes what the
field means** — from "somebody measured this" to "somebody wrote this" — which is the exact hole ADR
0074 warns about ("a default nobody measured makes decision 1's premise a lie").

Instead **this ADR and the ingest UI publish the measured number, and the operator enters it once** —
one press, with the conditions attached (1024², batch 1, one reference), beats a silent pass.

🔴 **The live run corrected two things** (2026-09-20, during acceptance):

- **Declaring the number does NOT clear `confirm_vram`.** The guard (`engineVramGuardRow`) compares
  against the **selected class**, which on a deployment with an unpinned ladder is its first rung — a
  T4 at 14,500 MiB. Entering an honest 20,862 still answered `engine_vram_confirm` (measured). **Only
  pinning a class as well clears it**, so P1's criterion is not "the prompt stops appearing" but
  "the declared number reaches the ladder".
- **The file-sum estimate does not merely nag — it buys a bigger box.** At acceptance the row was
  enabled at the 28,676 MiB sum, so **the 22,000 rung (L4) dropped out of the ladder and an L40S was
  bought** (45,458 MiB, 33.2 GB of host RAM — measured). With 20,862 declared, the L4 rung is a
  candidate again. This field is a COST field as much as a fit field.

🔴 **A third correction — the measurement depends on the CARD, and this procedure is circular**
(2026-09-20, P1 acceptance):

Running 2511 on real hardware and reading `/system_stats` measured **28,358 MiB in use on an L40S
48GB** (`vram_total` 47,665,709,056 B = 45,458 MiB, `vram_free` 17,930,132,506 B = 17,100 MiB).
That is within a rounding error of the 28,774 MiB file sum — **all three parts stayed resident at
once and nothing was ever evicted**.

2509's 20,862 MiB was measured on an **L4 24GB**, and decision 8 above says why: the text encoder
is **evicted** after encoding — and eviction happens BECAUSE the card is tight. The two numbers
therefore answer **different questions** and must not be compared. Leaving the card out of the
measurement conditions in `engineFamilyVram.ts` is an omission of this ADR.

🔴 **And the procedure itself is circular**: the file-sum estimate (28,774) drops the 22,000 rungs
from the candidate set → an L40S is bought → the measurement is taken on that L40S → it reads
28,358 → declaring that keeps buying L40S. **In this order, "does 2511 also fit an L4?" can never
be discovered.** Measuring it needs the REVERSE order: declare a `vram_mib` as a hypothesis first,
so the rung you want to measure on is a candidate again.

⇒ **28,358 is NOT entered into `engineFamilyVram.ts`** (the maintainer's call). That table is where
the number the operator enters into `vram_mib` in one press comes from, and this one would pin 2511
to the 44,000-and-above rungs for good. The existing policy — a family stays absent from the table
until it has been measured — is the right one here too.

🟢 **Re-measured in the reverse order, the same day (this answers open question 6).** Declaring
`vram_mib: 20862` (2509's number) as a hypothesis first put the 22,000 rungs back in the candidate
set, and **the box bought was an NVIDIA L4 24GB** (`vram_total` 23,659,151,360 B = 22,563 MiB,
16.1 GB of host RAM). The same request (seed 42) answered **200 in 676.6 s** and produced the same
picture as the L40S run — only the sign changed. **2511 does fit an L4.**

**On the L4 it measured 20,974 MiB in use** (peak of a 40-second sampling; it moved between 20,580
and 20,974 during the run, with as little as 1,589 MiB free). The **7,384 MiB difference from the
L40S reading is what gets evicted** — the measured size of the very behaviour decision 8 described
("the text encoder is evicted after encoding"). It sits beside 2509's 20,862 (also L4), which is
what a diffusion model 0.1 GB larger should look like.

⇒ **20,974 is now in `engineFamilyVram.ts`.** Both values in that table were read **on an L4 24GB**,
and that is what gives them their meaning (open question 7 — the field still has no column for the
card — is not yet closed).

### Decision 9 — "Custom workflows" stay out of this ADR; the trigger is duplication, not the family count

ADR 0081's rejected "let the Console post a raw ComfyUI graph" **stands**, for the reason it gave
(the template is the contract that gives `base_model`, `params`, a row's negative and the LoRA
family check their meaning; a raw graph walks past all of them). Measuring added two concrete
dangers:

- **The step ceiling disappears.** `validateRequestParams` (`jobs_http.go:188`) guards the knob route
  only; a raw graph can hand a shared GPU an hour-long job.
- **Pinning stops meaning anything.** The graph in this ADR was written against v0.35.2's node
  definitions (`deploy/aws/ecs/comfyui/Dockerfile:33`), and the node API promises nothing across
  versions (ADR 0072 decision 4). JSON pasted onto a row breaks **silently** on the day the engine is
  upgraded.

**The trigger is the amount of duplication, not the number of families.** A family count moves when
families unrelated to editing are added (8 + 2 = 10 today, so two unrelated ones would fire it).
Use decision 6's number — **one family ≈ 12 declarations** — and open the ADR when either:

- **a third differently-wired family** of the same capability is wanted (three wirings of one
  capability alive at once), or
- **the per-family declaration count grows** (12 → past 15, i.e. adding a family has itself become
  the expensive part).

🟡 **There is a step before raw graphs.** Making a family a DATA ROW (the nodes to insert, the
recipe, the knobs, the sizes, the ops) and having one template read it would turn those 12
declarations into one line whenever two families differ only by inserted nodes. Decision 9's two
dangers (the step ceiling, the pin) **both survive that step**, which is why it should be examined
before custom workflows are.

### Decision 10 — Teach the props reader this family's nodes

`textBehind` (`props.go:492`) returns the **prompt and the negative** only when it lands on
`CLIPTextEncode`, and the size is read by its caller (`props.go:446`) off `Empty*LatentImage`. This
family's graph has **neither**, so shipping it as-is records **an empty prompt, an empty negative and
an empty size** — the gallery's "what this picture was made from" would lie. Add
`TextEncodeQwenImageEdit` / `…Plus` as a source for both texts, and take the size from the image that
reached `SaveImage`.

⚠️ **This is only one of the two sources**: the PNG chunk (`source: "png"` — the blocking route and
MCP, plus every picture made before sidecars existed). The queue route, which is what the pane uses,
writes a sidecar and so fills all three fields for this family already. Which route to verify on is
in the phases section under P2.

🟢 Recovering the family itself already follows: `comfyFamilyFromPrefix` (`props.go:536`) walks
`comfyFamilies`, so decision 1 makes it readable with no further change.

### Decision 11 — Family properties decide GENERATION; what is ADVERTISED is a union across the models

🔴 **Without this, decisions 2-5 produce "editing once makes generation fall through to a paid
provider".** Capabilities leave through `http.go:230`'s `caps := p.Caps("")` — **the warm default
model, one row** — and from there into `st.Ops` (`http.go:240`), `st.Strength` (`http.go:247`), the
MCP tool definition (`mcp_stdio.go:1132`'s `op` enum) and the pane's fields. Worse,
`chooseImageProviders` (`imagegen.go:747`) drops a **whole provider** on
`caps(id).Supports(req.Op)`, so while a qwen row is warm, `op=generate` takes comfy out of the
candidates and lands on a provider that spends a member's plan.

The same trap was already hit with `negative_prompt` and fixed with a **union across the models**
(`http.go:246-270`, with the reason in its comment). `Ops`, `Strength`, `Sizes` and `MaxInputs` take
the same shape:

- 🔴 **The union goes inside `comfyProvider.Caps("")` itself**, not in the route. `Caps` resolves an
  empty model to `DefaultModel()` — the warm row — at `comfy.go:121-124`, so **making that one answer
  a union over the enabled rows fixes both the advertising (`http.go:230`) and the candidate filter
  (`imagegen.go:847`'s `capsOf` → `imagegen.go:747`) at once**. `Caps(model)` for a named model stays
  exactly the family's answer, so judging stays strict.
  ⚠️ **The negative precedent (`http.go:262-270`) unions in the ROUTE, and copying that shape alone is
  not enough**: `capsOf` would still see the warm row, `op=generate` would drop comfy at the
  candidate filter, and the third bullet below (the resolution inside `Generate`) would never be
  reached.
  ⚠️ **A union is right for one surface and wrong for the other.** The MCP tool definition
  (`mcp_stdio.go:1132`'s `op` enum) is a **connect-time snapshot** and cannot be per model at all, so
  a union is correct there and a model-specific refusal can only be decision 2's 400. **The pane
  needs per-model** (decision 12).
- **Judging** (at generation) is `Caps(model)` — `imagegen.go:847`'s `capsOf` already asks
  `p.Caps(req.Model)`, so a request that names a model is already right.
- 🔴 **With no model named, comfy's own model resolution has to read `req.Op`.** The union only
  keeps the provider in the candidates: `comfy.go:954-967` resolves an empty `req.Model` to
  `DefaultModel()` — the warm row — and answers `the self-hosted image engine cannot do generate`
  on `!caps.Supports(req.Op)`. `Run` files that under attempts and **`continue`s**
  (`imagegen.go:903-905`), i.e. **falls through to the next provider, which spends a member's
  plan**, and `recordUsage` (`imagegen.go:899`) writes a failed row on the way. So when the warm
  row's family does not claim the op, resolve to **the first enabled row that does** and say so
  with `comfySwitchWarning` (`comfy.go:812`). A checkpoint switch costs 1-2.5 minutes (measured),
  which is explainable; silently billing another plan is not. **P0's third criterion is only
  testable once this exists.**

### Decision 12 — There are SIX per-family properties. Dropping `cfg` and `negative` produces a false warning

`comfyFamilyKnobs` (`comfy.go:329`) and `comfyFamilyTakesNegative` (`comfy.go:416`) enumerate
families in a hard-coded switch, and **an unregistered family falls to the default**. Forgetting a new
family there means:

- `comfyFamilyKnobs` answers `["steps"]`, so `comfyIgnoredParamWarnings` (`comfy.go:375`) returns
  "cfg=4 was not applied: the qwen-image-edit family folds its guidance into the conditioning" —
  **the opposite of run A**, which edited at cfg 4;
- `comfyFamilyTakesNegative` answers false, so `Caps.Negative` and the pane's negative field
  disappear.

So the per-family set is **six**, not four: `Ops`, `MaxInputs`, `Strength`, `Sizes`, plus `knobs`
(steps, cfg, sampler, scheduler) and `negative`. This family is guided at cfg 4, so `negative` is
**true** (its graph puts the same `TextEncodeQwenImageEditPlus` on the negative branch).

🔴 **Two per-model fields go on the wire; decision 11's union only works paired with them.** With the
union inside `Caps("")`, `st.Ops` (`http.go:240`) and `st.Strength` (`http.go:247`) become
provider-level "some row here can do this" — **true of no particular model**. But `modelStatus`
(`http.go:151-188`) carries neither `ops` nor `strength`; its only per-model line is `Knobs`, defined
as a subset of `steps cfg sampler scheduler negative`. So the two things the Console line below
requires — stop `jobs.ts:204` sending `strength` unconditionally, take the `op` choices from the
status — **have no signal to condition on**. On a deployment holding SDXL next to qwen, `strength`
would always be true and `ops` always three, so the pane would keep showing a slider and offering
`generate` while qwen is selected, and decision 2's 400 would land in front of the member every time.

- **Add `strength` to `Knobs`** (the pane already hides a field with
  `model.knobs.includes("negative")`, so the form side is the same shape).
- **Add `ops` to `modelStatus`.**
- ⚠️ **Keep the existing rule that an ABSENT `knobs` (an older Agent) leaves the whole form usable** —
  `GenerateForm.dom.test.tsx` states it.
- 🔴 **A draft's `op` is re-read when it leaves the choices.** Switching to an edit-only row leaves
  `draft.op` at `"generate"` (`draft.ts`'s default), so the form holds a value the selector no longer
  offers; pressing it earns decision 2's 400 AND a fall-through to another provider. When the current
  op is gone, **fall back to that model's first op and say that it was changed** — changing it
  silently and failing silently are the same hole (ADR 0081 decision 4).

### Decision 13 — A named model PINS the provider that lists it; if it has no such op, refuse instead of falling through

🔴 **Decision 3 opened this door and the ADR had not looked at it.** `chooseImageProviders`
(`imagegen.go:747`) filters candidates on `caps(id).Supports(req.Op)`, and `capsOf` asks
`p.Caps(req.Model)` — strict when a model is named. So `model=qwen-image-edit-2509` with
`op=generate` and no provider named **drops comfy at the candidate filter**, while
`codexProvider.Caps(string)` and `agyProvider.Caps(string)` **ignore the model** and go on claiming
generate — so the picture is drawn on **the member's ChatGPT / Antigravity plan**. `fallbackWarnings`
counts only providers that were tried and failed, so **nothing is said**. Before decision 3, comfy's
`Caps` always claimed all three ops and this door did not exist.

- **If any provider LISTS the named model, the candidates collapse to that one** (a `Models()` id
  match on `ModelLister`; codex and agy are not `ModelLister`, so **naming a model for those two
  still behaves exactly as today**).
- If the pinned provider does not claim the op, **refuse rather than fall through** — an
  `ErrNoProvider`-shaped answer carrying **the model's name and the ops it does have**. "A named
  request is not dropped" is decision 11's own spirit, and saying which ops exist is shorter than
  silently billing another plan.
- ⚠️ A request that names a `pref` is already collapsed to one provider at the top of
  `chooseImageProviders` (`imagegen.go:747-748`), so this decision is about **auto only**. As built,
  the pin itself is `modelOwner` (`imagegen.go:766`) and the gate that calls it is `Run`'s
  `pref == "" || pref == "auto"` (`imagegen.go:864`).

## Rejected

- **Dropping it into the existing edit path.** Run C: at the 0.6 default it quietly returns an
  **unedited** picture.
- **One family holding two topologies, switched by a declaration.** The difference (reference-method
  node, shift) cannot be expressed by four words of `params`, and `comfyFamilyFromPrefix`
  (`props.go:536`) recovers the family from `af-<family>`, so **which wiring drew a picture becomes
  unanswerable**. Templates that branch on a declaration do exist (`comfyModelTakesNegative` and
  `comfyFamilyRecipes` change with a row's `params`) — but those branch on NUMBERS, not on wiring.
- **Removing `FluxKontextImageScale` to honour `size`.** The output size becomes free, but leaves
  the ratio table upstream says it trained on. That trades quality for a knob — and **the result of
  removing it was not measured**. Decision 4 prefers saying "it does not apply".
  🔄 **2026-09-23**: this option (a free pixel budget) was not measured. What was measured keeps 1 MP and
  frees only the ratio; the photos that went soft were at sizes that are not fixed points of the
  encoder's rounding ("Revision" at the end). `size` is still not honoured.
- **Making the Lightning LoRA (4 steps) the family default.** Fast, but it assumes cfg 1, where the
  negative prompt stops moving the picture (`comfyModelTakesNegative` would answer false). Leave it
  to the row, exactly as Krea 2 Turbo is left.
- **Taking Qwen-Image 2512 (text-to-image) in at the same time.** A different capability and a
  different family; the question here was editing, and nothing unmeasured goes in.
- **bf16 as the default.** 40.86 GB fits neither an L4 nor an L40S.
- **Embedding ComfyUI's own web UI in a pane.** ADR 0081's rejection stands (credentials, bypassing
  the catalogue, unusable on a phone).

## Consequences

- **Agent**: `comfy_workflows.go` (two families, one template plus 2511's delta, per-family denoise,
  two `comfyFamilyRecipes` rows — **2511's shift 3.1 has no field in `comfyRecipe`**, so it is either
  a literal in the template or a fifth field; decide when implementing), `comfy.go` (the six
  properties of decisions 11 and 12), `props.go` (decision 10), `comfyFamilyRow.TrialSteps`
  (`comfy_workflows.go:487`; it was `jobs.go`'s `comfyTrialSteps` until 未解決 4's consolidation)
  (**omitting it fails `TestEveryFamilyHasTrialSteps`**, `comfy_test.go:1807`; run B's 8 steps /
  63.2 s is the citation), `mcpx/mcp_stdio.go:1132` (the `op` enum and its description — the first
  case where which ops exist depends on the model; the reference-image argument is **`inputs`**, not
  `images`, `maxItems` 5). 🔴 **`strength`'s description (`mcp_stdio.go:1227-1231`) is rewritten
  too**: it is offered on the union, so "0.6 when omitted" alone walks an agent into a 400 every
  time — it has to say that some checkpoints refuse it, and that the answer is a 400. **P3's second image lands here too**: make `comfyParams` plural
  (`comfy_workflows.go:140`'s `Image string`), call `uploadImage` more than once (`comfy.go:1036`
  takes `req.Inputs[0]` alone), wire `image2`, and fix the singular wording of `comfyCheckInputs`'
  refusal (`comfy.go:1136`).
- **Wire**: `ops` on `modelStatus`, `strength` in `Knobs` (decision 12), and the Console's mirror of
  both: `wire.ts:37`'s `Knob` is a **closed union**
  (`"steps" | "cfg" | "sampler" | "scheduler" | "negative"`), so it will not compile until the type
  changes, and `ImagegenModel` (`wire.ts:90`, `knobs?: Knob[]`) has no `ops`. 🔴 **Three comments
  spell that vocabulary out** — `providerStatus.Strength` (`http.go:108-115`, "No union is needed:
  it is per provider, not per model", which decisions 2 and 11 make false), `comfyFamilyKnobs`
  (`comfy.go:315-329`) and `modelStatus.Knobs` (`http.go:170-173`). All three say "a subset of those
  five words", so **the same change rewrites all three**.
- **CP**: `engine_catalog.go` (two words, required flags), `engine_family_parts.go` (the table), and
  `engine_class.go` is **left alone** (decision 8).
- **Console**: two family cards in `families.ts` (dialect `sentences`, no quality chips, steps 20 for
  2509 and 40 for 2511, `sizes: []` and **no size field at all**), `wire.ts:41`'s `Family` type,
  `families.test.ts`'s family list, **`jobs.ts:204`'s unconditional `strength`** (it is sent whenever
  `op !== "generate"`, so decision 2 would refuse every edit), the `op` choices (`draft.ts`'s constant `OPS`, walked at `GenerateForm.tsx:426`, never the
  status's `ops`), and `GenerateForm.dom.test.tsx` (the test that greys fields out on `knobs` —
  extend it for `strength` and `ops`, without breaking its other claim: an ABSENT `knobs` leaves the
  whole form usable).
- **The refusal has two homes** (decision 2): the blocking `/imagegen/generate` (`http.go:449`) and
  the queue route the pane uses (`jobs_http.go:115`). Both check the RANGE (0 < s ≤ 1) today, so
  **adding it to only one leaves the pane with a failed job instead of a 400** — and P0's second
  criterion would hold on one route and not the other.
- **Guide**: `guide/operate/07-image-engine.{md,ja.md}` (the page that lists per-family behaviour).
- **Deployment**: 20 GB more per checkpoint. The box keeps models on NVMe so the space is there, but
  **the cold-start sync grows** (measured: 348.9 s for 30 GB, purchase included).
- **Tests**: two goldens in `comfy_workflows_test.go`, the `engine_catalog_test.go` equality check,
  `families.test.ts`.

## Phases

- **P0** — the `qwen-image-edit-2509` family and the per-family properties of decisions 2-5, 11 and
  12, with goldens and docs. **The Console family card belongs here too**: half of decision 4 (no
  size field) runs through `sizeOptions` (`families.ts:179`), which reads the card, so without one the
  megapixel list stays. That it is currently harmless rests on the size field being
  `disabled={isEdit}` — an accident, and an accident is not a specification. Three completion criteria: (1) **run A reproduced** on real hardware
  (it edits), (2) **run C, naming qwen, refused with 400 on both routes** (no `strength` here), and (3) 🔴 **with a qwen row warm,
  `op=generate` still lists comfy** (decision 11's union holds, so nothing falls through to a paid
  provider), and (4) **naming qwen as the `model` while asking for `op=generate` is refused with the
  model's name in it** (decision 13 — a named request is not dropped).
- **P1** — 2511 (decision 6), the parts table (decision 7), the road to an operator-entered
  `vram_mib` (decision 8). Four completion criteria: (1) **one press stages all three parts in the
  right directories**, (2) **2511 edits on real hardware** (run E reproduced), (3) **the ingest
  screen and the row's edit both publish the measured number with the conditions it was measured
  under** (size, batch, reference count, and the weights file it was read from), for the operator to
  enter in one press, and (4) 🔴 **the entered number reaches the ladder** — the rung is chosen from
  the measurement (20,862 MiB) and not from the file-sum estimate (28,676 MiB for 2509).
  🔴 **It is NOT "`confirm_vram` stops appearing once it is entered".** The guard
  (`engineVramGuardRow`) compares against the selected class, which on a deployment with an unpinned
  ladder is its first rung — a T4 at 14,500 MiB — so an honest 20,862 still prompts (measured during
  P0's acceptance). Decision 8's 🔴 says the same thing; it is repeated here because this is the
  section an implementer reads first.

  **Result of the live acceptance (2026-09-20)**: 🟢 **all four met** — (3) and (4) only after the
  re-measurement described in decision 8's third 🔴.
  - (1) One press — the three parts did land in the right directories, but **it took a second
    press**: the row's 揃える was needed because the part follow-up failed silently (that seam is
    closed on the ADR 0085 side — PRs #802/#804/#806).
  - (2) 2511 edits — `POST /imagegen/generate` (the blocking route) answered 200 in 823.8 s and only
    the sign changed to CLOSED, everything else intact: **run E reproduced**.
  - (3) The published measurement — the first reading (L40S, 28,358 MiB) was one that **cannot go in
    the table**. Re-measured in the reverse order, the **L4's 20,974 MiB** is now in
    `engineFamilyVram.ts`.
  - (4) The entered value reaches the ladder — **confirmed end to end on real hardware**. At the
    28,774 floor an L40S was bought; declaring 20,862 put the 22,000 rungs back and **the box
    actually bought became an L4 24GB**. The guard's wording moves with it, from
    `wants at least 28774` to `wants 20862` (source `declared`).
- **P2** — props (decision 10). 🔴 **Which ROUTE made the picture decides what "done" means.**
  There are two sources (`readImageProps`: ① the sidecar `<file>.png.json` next to the picture,
  ② the PNG's own `prompt` chunk), and `textBehind` and `Empty*LatentImage` — the two decision 10
  names — belong to **② alone**. **The pane goes through the job queue, which writes ①**
  (`jobs.go`'s `propsFor` records the resolved request and `store.go` overwrites `Size` with the
  saved picture's real dimensions), so on the pane all three fields are filled for this family
  too and the defect is invisible. ② is what the **blocking route `/imagegen/generate` and MCP**
  read, neither of which writes a sidecar, along with every picture made before sidecars existed.
  Done when a picture made **through the blocking route** shows its prompt, its negative and its
  size under "what this was made from".

  **Live acceptance (2026-09-21)**: 🟢 **met** (PR #819). One `op=edit` with a negative prompt
  through the blocking route (seed 42, 1024², only the sign became CLOSED). No sidecar was
  written, so the answer is `source: "png"` — and **the full prompt, the negative exactly as
  sent and `1024x1024` all come out**. As a positive control, reverting the reader to its
  pre-fix shape answers **all three empty on the same picture**.
  - 🔵 **By-product**: with the row's `vram_mib: 20974` (declared), the box the ladder bought was
    the **22,000 rung (`g22-spot` — g6/g5/g6e.xlarge, $1.35/h)**, and 40 steps ran on it. That is
    P1's "2511 fits a 24GB card" confirmed through an actual purchase rather than a measurement.
  - 🔴 **The blocking route's 960-second wait is shorter than this family's cold start.** The
    first attempt answered **HTTP 502 `imagegen_failed: waiting for the image engine timed out`
    after 960.0 s**: buying the box, syncing ~30 GB and running 40 steps did not fit (P1's 823.8 s
    was on an L40S). **The engine finished anyway** — the identical request 22 seconds later came
    back **HTTP 200 in 1.07 s** from ComfyUI's own cache with the same picture. So the first edit
    on this family reads as a failure to the caller while the GPU keeps working and billing.
    Outside decision 10, so whether it becomes an open question of its own is the maintainer's
    call. 🟢 **Fixed in P3** (the maintainer's call was "fix it together with P3"); see below.
- **P3** — the reference-image path end to end (pane and the MCP `inputs` argument) and `MaxInputs`
  on the wire. Done when run D (two images) can be reproduced from the pane, and **three images are
  measured once** before `MaxInputs` goes to 3 (until then it stays 2).

  **Fixed alongside it — waiting for the engine is not one clock.** The settlement of P2's 🔴
  above. **Waking the box and making the picture fail differently**: giving up on the first costs
  nothing, and giving up on the second stops neither the GPU nor the bill. So:
  - `engineTimeout` (960 s) becomes the **wake budget only** — the uploads and `/prompt`. The chain
    that made the number (longer than the gateway's 900 s, because the gateway is the only layer
    that knows WHY a wait was long) is unchanged.
  - Once `/prompt` is accepted the box is up by definition, and the rest runs on
    **`engineRunTimeout` (15 min)**. Measured: 2511 samples 40 steps at 1024² in **393.8 s** warm,
    and a first prompt pays for loading ~20 GB of weights on top. The single-clock run had ~10.7
    minutes left after a 5.3-minute wake, and that was not enough. ⚠️ **A batch is not covered**
    (`comfyMaxBatch` is 4) — deliberately, because overrunning is no longer destructive; see next.
  - 🔴 **Expiring after the prompt was accepted says how to collect the picture**: "it is still
    making this picture (prompt &lt;id&gt;) and was not interrupted — asking again with the same
    request collects it". The evidence is P2's own measurement: the identical request 22 s later
    came back in 1.07 s. **A caller told only "timed out" pays for that picture and never collects
    it.** When the box was being REPLACED the old wording stands ("still starting") — there the
    work may genuinely be gone, and "ask again to collect it" would be advice to wait for nothing.
  - The chain still ends at the provider: the MCP budget goes **18 min → 33 min** (over 16 + 15 =
    31). ⚠️ codex cuts first at its own `tool_timeout_sec` (600 s), so nothing changes there.

  **Live acceptance (2026-09-21)**: 🟢 **met** (PR #827 plus run F).
  - **Two references — run D reproduced.** The blocking route answered **200 in 889.8 s** (678 s of
    it waking). The graph embedded in the picture is **16 nodes**: `img2` a bare `LoadImage`,
    `image2` on **both** pos and neg, `enc.pixels` from `scale` (image1) — **the deployed code
    emitted run D's wiring unchanged**. The plant from picture 2 is on the table to the right of
    the mug; the sign still reads OPEN and the wall, table and mug are untouched.
  - **Three references — run F.** 220 s on the warm box, 17 nodes (`img3` added). The plant and the
    duck **both** arrived with their colour and shape intact and nothing else moved, so `MaxInputs`
    is **3**. ⚠️ **This cannot be measured through the Agent** — it declared 2 at the time and
    refuses a third before anything runs. It went to the engine directly, with the graph produced by
    **this implementation's own builder** and behind a guard that refuses unless `state == running`
    (so that reading it cannot buy a stopped box).
  - 🔴 **It is NOT a positive control for the clock split.** 678 s of wake plus 212 s of generation
    is 889.8 s, which the **single 960 s clock would also have passed, with 70 s to spare**. The
    evidence for that fix is the unit test's mutation (deriving the run clock from the wake one goes
    red in 0.30 s) and P2's run, which actually hit the 502.
  - The wire was checked live: enabling the row moved `providerStatus.max_inputs` from 1 to 2 (as it
    then was) and `modelStatus.max_inputs` appeared — the union and the per-model answer both work.
  - 🟢 **A picture from the generation pane — the completion definition itself — was made too**
    (sandbox, driven with headless Chromium). Choosing the model brought up the family card and
    **took the size field away** (decision 4); Advanced switched the operation to edit; the field's
    heading read **"参照画像 2 / 3 枚"**, so the per-model ceiling reaches the UI. The job finished
    in **868.4 s** with the plant from picture 2 on the table to the right of the mug and nothing
    else moved, and its props answer `source: "sidecar"` with **both** references in `inputs` —
    the queue route, exactly as P2's correction says.
  - 🔴 **That run also found that the pane's reference images fail outright on a fresh workspace.**
    `InputPicker` uploads into `generated/console/inputs`, which does not exist until something has
    been generated there, and `handleFSUpload` **required the directory to already exist**
    (measured: `400 not_dir`, surfaced as "could not upload" with nothing the member can do).
    It now **creates a target that is missing** (`fs.go`), and still refuses one that exists and is
    not a directory — that one is a real mistake and could clobber a file. The same reasoning
    imagegen's `resolveOutDir` already states for out_dir: a folder named for work that has not
    happened yet cannot be expected to exist.

## Open

1. **2511's 40 steps are expensive** (measured 393.8 s). The Lightning LoRA (4 steps) as a row has
   not been measured for quality. **Not measured in P1** (the maintainer's call), so this stays open.
2. **Whether `inpaint` can be claimed** (deferred in decision 3). `SetLatentNoiseMask` on top of a
   denoise-1 instruction edit was unmeasured.
   🟢 **Closed (2026-09-21). Measured, fixed, and claimed** — decision 3 carries it (`Ops` is
   `[edit, inpaint]`). Run G showed the gate works on a square input; run I, on a non-square one,
   found that **the mask and the picture were on different maps** and closed it. Both are below.

   🔵 **Run G (dev deployment, L4 24GB, 2509, 20 steps). The mask works.**

   The graph is this repository's own golden (`testdata/comfy_qwen-image-edit-2509.golden.json`)
   with the **same two nodes** `comfyRequestLatent` (`comfy_workflows.go:302`) already builds for
   the other eight families inserted (`LoadImageMask` channel=red → `SetLatentNoiseMask` →
   KSampler's `latent_image`). Nothing else but the image name, the prompt and the seed differs.
   All four ran at seed 42 against the same input picture.

   | # | mask | prompt | INSIDE mean/max/%>8 | OUTSIDE mean/max/%>8 | picture |
   |---|---|---|---|---|---|
   | G0 | none | sign | (whole frame) 7.56 / 223 / 5.71% | — | the sign reads CLOSED |
   | G1 | **the mug** | **the sign** | 3.09 / 50 / 5.83% | 0.43 / 30 / **0.13%** | **the sign still reads OPEN** |
   | G2 | the sign | the sign | 33.65 / 221 / 20.22% | 0.40 / 38 / **0.08%** | CLOSED, nothing else moved |
   | G3 | the mug | the mug | 65.00 / 189 / 57.88% | 0.42 / 30 / **0.13%** | a blue teapot; the sign still OPEN |

   🔴 **G1 is the control that makes the rest mean anything.** The prompt that changes the sign,
   sent with a mask that does NOT cover the sign, leaves it reading OPEN — while the same request
   with no mask (G0, and 実測 A) changes it. `SetLatentNoiseMask` really is a gate on a denoise-1
   instruction-edit graph. Outside the mask is not bit-identical (the VAE decode rebuilds the whole
   frame), but around 0.1% of pixels differ by more than 8, which is unchanged to the eye. The
   reference conditioning (`TextEncodeQwenImageEditPlus`) still sees the WHOLE unmasked picture,
   which is also why the repainted area agrees with the scene around it.

   🔵 **Run I (same day, non-square). The danger was real, and it was 8 px.**
   The same scene at **1820x1024** (1.7773) — built by extending the 1024² picture's own flat
   edges, so the sign and the mug are the original pixels — through the same four runs.

   - **The frame came back 1392x752** (1.8511). The ratio changed, which means
     `FluxKontextImageScale` is `common_upscale(crop="center")`: it **centre-crops first** and then
     resizes. Corroborated: rebuilding the input as "crop 20 px, then resize" and comparing it
     against the output leaves nothing above a difference of 40 except the sign's lettering (the
     unmasked run's only changed region is bbox (524,234,866,322)).
   - **The gate still works off-square.** The mug's mask with the sign's prompt left the sign OPEN.
   - 🔴 **But the mask was on a different map.** Repainting a band of flat wall (input y 0..200)
     and reading its lower edge: **row 140 without the fix, row 132 with it** (predicted: 146.9 for
     a plain stretch, 137.6 for crop-and-resize). **The 8 px difference matches the predicted 9.3**,
     and in the predicted direction.
   - **The fix** is to send the mask through the picture's own `FluxKontextImageScale` (`LoadImage`
     → `FluxKontextImageScale` → `ImageToMask`(red) → `SetLatentNoiseMask`). Handed the same width
     and height it resolves the same target, so the two maps agree by construction — and that
     sameness is enforced at intake (decision 3).
     🔄 **The revision (2026-09-23) makes this fix unnecessary** — without the crop both are plain
     stretches (log 112 §11, T3: 6.6 px → 3.0 px).

   ⚠️ How far a thin mask bleeds at the latent's 8× granularity is still unmeasured. It is also why
   run I's edges sit about 7 px ahead of the prediction in both arms, together with reading the
   boundary at the 50% crossing.
3. 🟢 **Closed (2026-09-21, run H). Thin host RAM is not eaten by repeated switching.**
   The 2509 row was rebuilt from the objects still in S3 (no re-download), **both families
   enabled**, and the rung pinned to `g6-od` so the card was certainly an L4. The box reported
   `ram_total` **15,371 MiB** and `vram_total` 22,563 MiB — exactly the thin condition unresolved 3
   names (2,260 MiB free at the start).

   Six runs at 8 steps, 1024², one reference picture, **each with its own seed** (an identical
   request comes back from ComfyUI's cache in about a second and would measure no load at all):
   2509 → 2511 → 2509 → 2511 → 2509 → 2511 = **five switches**, where 実測 E made one.

   | run | family | wall | `ram_free` after |
   |---|---|---|---|
   | 101 | 2509 (already resident) | **81 s** | 2,279 MiB |
   | 102 | 2511 (switch 1) | 117 s | 2,282 MiB |
   | 103 | 2509 (switch 2) | 112 s | 2,075 MiB |
   | 104 | 2511 (switch 3) | 120 s | 2,289 MiB |
   | 105 | 2509 (switch 4) | 113 s | 2,311 MiB |
   | 106 | 2511 (switch 5) | 118 s | 2,251 MiB |

   - **A switch costs 31–39 s** (112–120 s against the resident 81 s). Cheap for re-reading 20.4 /
     20.5 GB, and it is cheap because none of it accumulates in host RAM.
   - **`ram_free` oscillates between 2,075 and 2,311 MiB and does not trend down.** The spread
     around the starting 2,260 MiB is measurement noise. **No behaviour was observed in which
     switching eats the host's memory.**
   - Zero failed prompts, and no ECS task restart (no `has stopped N running tasks` event).
   - ⚠️ Scope: two fp8 families, 1024², one reference, batch 1, 8 steps. **`comfyMaxBatch` 4 and a
     third family enabled alongside are still unmeasured.**
4. **Decision 9's trigger** (a third topology / past 15 declarations) is drawn from the measured 12,
   but "15" is not itself a measured number. Count the declarations again when the next family lands.
   🔵 **Counted, while 2511 was being added: 17 places.**

   | where | count | the places |
   |---|---|---|
   | Agent | 9 | the `comfyFamily` constant / `comfyFamilies` / `comfyBuildGraph`'s switch / the template entry point / `comfyQwenEditWirings` / `comfyFamilyRecipes` / `comfyFamilyInstructionEdit` / `comfyFamilyKnobs` / `comfyTrialSteps` |
   | CP | 4 | `engineComfyFamilies` / `engineComfyRequiredFlags` / `engineFamilyUpstreams` / `engineFamilyParts` |
   | Console | 3 | `wire.ts`'s `Family` / `FAMILY_CARDS` / the list in `families.test.ts` |
   | golden | 1 | `testdata/comfy_<family>.golden.json` |

   The drafted 12 missed five: the constant itself, the template entry point,
   `engineFamilyUpstreams` (**without it `TestFamilyUpstreamsCoverTheVocabulary` is red**),
   `families.test.ts`, and the wiring table P1 added. P1's implementation held it to **20 → 17** by
   introducing `comfyFamilyInstructionEdit`, which folds four declarations (ops, strength, sizes,
   negative) into one. That is a reprieve for as long as the families that follow share this
   capability; it does nothing for the next topology with a *different* one.

   🔴 **Decision 9's second trigger ("past 15 of the 12 places") is therefore drawn.**

   🟢 **The Agent half of the intermediate step was done (2026-09-21, the maintainer's call).
   17 → 12 places.** The part of decision 9's 🟡 "a family as a data row" that **closes inside one
   module** is folded into `comfyFamilyRow`: the recipe, the sampler knobs, the trial steps, the
   sizes, whether a negative moves it, the filename prefix, and the instruction-edit wiring are one
   row. The Agent's nine places become **four**: the constant, the row, `comfyBuildGraph`'s case,
   and the template entry point.

   | where | before | after | what is left |
   |---|---|---|---|
   | Agent | 9 | 4 | the `comfyFamily` constant / one `comfyFamilyRows` row / `comfyBuildGraph`'s case / the template entry point |
   | CP | 4 | 4 | untouched (the other module) |
   | Console | 3 | 3 | untouched |
   | golden | 1 | 1 | one fixture per family is the evidence, not duplication |

   🔴 **Putting `Build` on the row was not declined — it does not compile.** The templates READ this
   table (the recipe, the wiring), so a table that also held them is an initialisation cycle.
   Measured: `initialization cycle for rows / rows refers to buildA / buildA refers to rowFor /
   rowFor refers to rows`. **Whoever takes this across the module boundary inherits that
   constraint**: in Go, the data and the graphs can only be one declaration if the graphs take their
   row as an argument. (There is a second reason to keep one entry point per family: the CP's
   `TestComfyRequiredFilesMatchTheAgent` learns which files a family needs from the
   `errComfyMissingFile` calls inside each `comfyGraph*` body, so a family that never names itself
   in a refusal is one that check silently stops measuring.)

   🔵 **The consolidation surfaced a pre-existing gap in the tests.** `comfyFamilyPrefixName` is one
   half of `comfyFamilyFromPrefix` (how a finished picture's reproduction record recovers its
   family), and nothing checked the round trip — a golden compares a prefix against a string written
   down at the same time, so a spelling the reader does not know passes happily. Dropping
   flux2-klein's short name leaves `af-klein` unreadable with the whole suite green (measured);
   `TestEveryFamilysPrefixIsReadableBack` now drives every family from its template's own output.

   **The cross-module ADR (a data row plus a generator) has still not been raised.** Eight of the
   remaining twelve places can only go there, and both the cost (binding the two Go modules at build
   time) and decision 9's two dangers (the `steps` ceiling, the pinning) are unchanged by it.
5. 🟢 **Closed in P1.** **The operator entering `vram_mib` once** (decision 8) is forgettable unless
   the screen asks for it right after the ingest — and P1's completion criterion (3) put it there:
   the ingest screen and the row's "edit" both show the measured number **with the conditions it was
   measured under** (size, batch, reference count, the file, the card), and the operator enters it in
   one press. The record is P1's live acceptance in the Phases section.
6. 🟢 **Closed the same day: 2511 measured on an L4 24GB = 20,974 MiB** (decision 8's third 🔴).
   The reverse order — declare a `vram_mib` as a hypothesis first so the 22,000 rung is a candidate
   again — worked as intended. **That reverse order is itself the procedure the next person adding
   a family needs**, and it is written into decision 8.
7. 🟢 **Closed the same day: `FamilyVramMeasurement` gained `card`** ("L4 24GB"). The column was the
   branch taken and the table's contract stays "what was measured" — rewriting it as "what this
   family needs at MINIMUM" would have made this field speak about cards nobody measured on, which
   is the "a number somebody wrote" it exists to avoid. The card now shows in the measurement
   conditions on the ingest screen and the row's Edit (`admin.engines_vram_measured`).
   🔴 The rule about the VALUE is in the type's doc too: **a reading is a value of this field only
   when the card was tight enough to force eviction.** A reading on a roomier card is not the same
   measurement under different conditions — it is **the answer to a different question**.

## Measured (2026-09-20, dev deployment, g6.xlarge / L4 24GB)

The graph is the shipped template's subgraph copied into API format, 10-12 nodes, with the UI-only
nodes (`ComfySwitchNode`, `Primitive*`) folded away. Every node's existence was confirmed against
the **running** engine's `/object_info` (`CLIPLoader`'s `type` **does offer `qwen_image`**, one of
28; `TextEncodeQwenImageEditPlus`, `FluxKontextImageScale` and `CFGNorm` are there too). The engine
reported **ComfyUI 0.35.2**, which is the pinned ref.

- **It fits an L4 24GB.** Raw `/system_stats`: 22,563 MiB total, 1,701 MiB free, so **20,862 MiB in
  use** (1024², batch 1, one reference image). The guard's 28,676 MiB demand notwithstanding, the
  text encoder is evicted after encoding (decision 8).
  ⚠️ `comfyMaxBatch` is 4 — **that range was not measured**.
- **The output-size rule comes from the node's source, not from these runs**: every run was 1024² in
  and out, so `PREFERRED_KONTEXT_RESOLUTIONS` was never exercised off-square. (🔄 Tried at 1.777, 1.870
  and 3.000 on 2026-09-23 — [log 112](../log/112-kontext-crop-necessity.md).)
- **`comfyFamilyRecipes`' four fields come from the shipped template's KSampler widgets**: both
  families are `sampler=euler` / `scheduler=simple`, and steps/cfg come from the switch's **false
  branch** (the no-LoRA side) — **20 / 4** for 2509 and **40 / 4** for 2511. Runs A and E used
  those. ⚠️ 2509's template ships that switch set to **true**, i.e. the 4-step / cfg 1 Lightning
  path; the family default takes the **no-LoRA** side, for the reason in the rejected list.
- **Buying the box and syncing cost 348.9 s** (503 `engine_waking`, retried every 15 s). A request
  carrying `X-AF-Model` was held with "1 file(s) to go" — `pendingGuard` behaved as designed on real
  hardware.
- **Ingest ran at about 31 MB/s** from Hugging Face to S3 (20.43 GB in ~11 minutes).

### Two holes found on the way (both outside this ADR)

- 🔴 **`MODE=move` could never succeed.** The ingest task role had no `s3:GetObjectTagging`, and
  `aws s3 mv` reads the source's tags by default, so **every object past the multipart threshold**
  failed with `AccessDenied`. ADR 0085's "present at another key → move" — the only repair for a
  misplaced file — was a button that always failed. **Fixed in PR #763**, and a 9.38 GB move
  succeeded within a minute of the fix being applied.
- 🟡 **`replace` can fall through to `Append` in complete.** Pointing `choices` + `replace` at a slot
  the row already fills answers 500 (`engine model file slot is already taken`). The way around is
  to empty the slot first. Not chased down here, but the reproduction is recorded.

## What the review took off (2026-09-20, pre-implementation review, separate session)

**Every citation held** (the `file:line` references and the "exists / does not exist" claims). What
came off was the REACH of the decisions, and three names in the code.

- 🔴 **Decisions 2-5 had not looked at the ADVERTISING path.** Capabilities leave through one warm
  row's `Caps`, so per-family `Ops` produces "the moment somebody edits with qwen, `op=generate`
  falls through to a paid provider". Decision 11 was added. The same trap had already been hit with
  `negative_prompt` and fixed with a union (`http.go:246-270`) — that fix was there to be read.
- 🔴 **There are six per-family properties, not four** (decision 12). Dropping `cfg` and `negative`
  emits a warning that says the opposite of run A.
- 🔴 **Decision 8's "22,000" was the rung's number, not a measurement.** Recomputed from the stored
  raw bytes: **20,862 MiB in use**. And `vram_mib` is defined as the operator's own measurement, so
  having the ingest write it would change what the field means — it now says **the ingest does not
  write it**.
  🟡 The review's own "the measured 22,426 MiB exceeds the rung" was a **GiB / GB mix-up**;
  recomputing from the stored raw values (23,659,151,360 / 1,783,934,774) puts it inside the rung.
  **A finding about a number goes back to the raw number.**
- 🔴 **Decision 7 had no Repo / File for the VAE.** Reuse is decided on artifact identity, so an S3
  key alone downloads the same bytes twice from a second repository.
- 🔴 **Decision 6's rule should read "per topology", not "per version"**, and the asymmetric spelling
  (one with a version, one without) breaks the day 2512 arrives with 2509's wiring. Its cost (one
  LoRA row per family) is now stated.
- 🟡 **Decision 4 was losing to the row's `sizes` and to the Console's default list**
  (`comfy.go:604`, `families.ts:179`).
- 🟡 **Decision 9's "past 12 families" trigger measured the wrong thing** (families unrelated to
  editing would fire it). It now counts duplication, and names the table-driven middle step.
- 🟡 **Three names were wrong**: the `op` enum is in `mcp_stdio.go:1132`, not `mcp_imagegen.go`; the
  reference-image argument is `inputs`, not `images`; the size is read at `props.go:446`, not in
  `textBehind`.
- 🟡 **Five consequences were missing**: `comfyTrialSteps` (omitting it fails a test),
  `comfyFamilyRecipes`, `wire.ts`'s `Family` type, `families.test.ts`, and
  `guide/operate/07-image-engine`.
- 🟡 **Decision 5 contradicted decision 3**: "the third image is the same mechanism, so open it" is
  the inference forbidden for inpaint. It now stops at **two, with the third measured in P3**.

### Second round (same session, against `a2ba1b22`)

Eight of the nine landed as intended. All three remaining 🔴 had the same shape: **the decision was
right, but one path was missing**.

- 🔴 **Decision 11's union alone does not make P0's third criterion green.** A request that names no
  model is resolved to the warm row FIRST and then fails on the op, so keeping the provider in the
  candidates still **falls through to the next one**. Decision 11 now says the model resolution must
  read `req.Op`.
- 🔴 **Decision 1 still carried the old spelling** (in the Japanese file). It is the first place an
  implementer reads, so it is fixed.
- 🔴 **Decision 5's `MaxInputs=2` does not carry a second image on its own** (one upload, a singular
  `comfyParams.Image`). **P0 stays at 1** and the second image moves to P3 with its plumbing:
  *declare a capability in the same phase as the path that serves it.*
- 🟡 Three more: P3 still said `images`, `comfyFamilyRecipes`' sampler/scheduler had no source, and
  P1's criterion contradicted decision 8. All corrected.
- 🟢 **The reviewer withdrew the VRAM figure; 20,862 MiB stands.** A second witness turned up in the
  tree: `PARAMETERS-60-engines.md:659` says "`Total VRAM 22563 MB`, so **22000 is the number**" — the
  rung and the measurement come from the same place.

### Third round (against `8d37d827`)

One 🔴 left, and it was **where the union goes**.

- 🔴 **Calling decision 11's union "advertising" defined it by its consumers.** The negative
  precedent it cited unions in the ROUTE (`http.go:262-270`), so copying that shape leaves `capsOf`
  (`imagegen.go:847`) looking at the warm row: `op=generate` drops comfy **at the candidate filter**
  and never reaches the third bullet (the op-aware resolution inside `Generate`). It now names the
  function — the union lives in **`comfyProvider.Caps("")`** — which fixes advertising and candidate
  selection in one place while `Caps(model)` stays strict.
- 🟡 Decision 8's text had not followed decision 5's retreat (P0 stays at one image), and the
  consequences' Agent line was missing the second image's plumbing (plural `comfyParams`, repeated
  `uploadImage`, the `image2` wire, the singular refusal). Both fixed, and `comfySwitchWarning`'s
  line corrected from 632 to 633.

### Fourth round (against `f0c4a79a`)

One 🔴, and it was **the union's price**. With `Caps("")` unioned, `st.Ops` and `st.Strength` become
provider-level "some row can do this" — **true of no particular model** — while `modelStatus` carries
neither, so the pane cannot write "hide the slider only while qwen is selected". **Two per-model
fields were added to decision 12** (`strength` in `Knobs`, `ops` on `modelStatus`), together with the
comment rewrite: `providerStatus.Strength` (`http.go:108-115`) still claims "No union is needed: it is
per provider, not per model", which decisions 2 and 11 make false.

🟢 The review's sweep of "what else reads `Caps("")`" is worth recording: five call sites take an
unnamed model (`http.go:230`, `imagegen.go:847`, `:821`, `:856` and **`jobs.go:473`** — the last one
this ADR had never named).

⚠️ **Its conclusion that "no warning is lost" was wrong.** It counted the negative correctly and
missed what `strength` becoming per-model had just added; the last two call sites — the warning path
— are readers that must NOT see the union. P0 closes it by passing `Caps(res.Model)` (decision 2).
**The union is now read by three places only**: the advertisement (`http.go:230`) and the candidate
filter (`imagegen.go:847`, `:821`).

### Fifth round (against `a1583944`)

**No 🔴 left.** The three 🟡 were all about the granularity of the consequences, and all three are
fixed.

- 🟡 **Adding a wire field touches three more places**: `wire.ts:37`'s `Knob` is a closed union (it
  will not compile until the type changes) and `ImagegenModel` has no `ops`; and two more comments
  spell the same vocabulary out (`comfy.go:315-329`, `http.go:170-173`) where the consequences named
  only `http.go:108-115`.
- 🟡 **Decision 2's 400 has two homes** (`http.go:449`, `jobs_http.go:115`). With only one, the pane
  gets a failed job instead of a refusal and P0's second criterion holds on one route only. Decision
  2 also now says the refusal applies **only when the resolved model is of this family** — a request
  naming no provider may not land on comfy at all.
- 🟡 One EN/JA mismatch (the Japanese consequences line was missing `GenerateForm.dom.test.tsx`).

🟢 The review's sweep of "what else does the pane want per model" came back with **`ops` and
`strength` and nothing else** (size is already `modelStatus.Sizes`, negative rides `Knobs`, LoRA has
`loraStatus.baseModel`, seed / aspect ratios / samplers are family-independent, and `MaxInputs` is
already deferred to P3 by decision 5's ⚠️).

## What P0 built (2026-09-20, develop `f57e82dd`)

Decisions 1-5, 11, 12 and 13 landed (#773 and #775). **Not verified on hardware** — completion
criteria (1)-(4) wait for a deployment. The seams worth knowing, for whoever picks up P1:

- Decision 11's union is `comfyProvider.capsUnion` (`comfy.go:165`), returned by `Caps("")`
  (`comfy.go:121-124`).
- Decision 13's pin is `modelOwner` (`imagegen.go:766`) and the gate in `Run` that calls it
  (`imagegen.go:864`).
- Decision 2's refusal is `bad_strength_family` (`http.go:480`, `jobs_http.go:124`) — a **separate
  code** from the out-of-range `bad_strength`, and decision 4's size refusal has the same shape
  (`bad_size_family`).
- The warning for the route that cannot be refused lives in the CORE: `requestWarnings` is handed
  the Caps of the row that actually ran (`imagegen.go:927`, `jobs.go:473`). The provider-side twin
  this ADR's first draft asked for was deleted in #779 (comfy.go −73 lines).
- Decision 12's op re-read is the Console's pure `remappedOp` (`draft.ts:65`).

The implementation review (opus, read-only) ran twice: 🔴3 / 🟡5, then 🔴0 / 🟡4 (all code-level and
handed straight to the implementer). **Two holes the ADR was silent about came out of that review**,
and both became decisions: 13 (a named model falling through to a paid provider) and decision 2's
"one route that cannot be refused".

## P0's live acceptance (2026-09-20, dev deployment)

All four completion criteria passed **through the deployed Agent's own path** (its HTTP API). The
engine was ComfyUI 0.35.2 on an **L40S** (45,458 MiB, 33.2 GB of host RAM).

| Criterion | Result |
|---|---|
| (1) run A reproduced | ✅ the sign reads `CLOSED`, mug / table / typography untouched. `provider=image`, `model=zz-exp-qwen-image-edit-2509`, 1024², seed 42, **474 s wall** (box purchase + 30 GB sync + first load included) |
| (2) `strength` refused | ✅ **400 `bad_strength_family` on both routes** (`/imagegen/generate` and `/imagegen/jobs`), reading "the qwen-image-edit-2509 family fixes its denoise at 1 by construction" |
| (3) generate stays on comfy while qwen is warm | ✅ `provider=image`, re-read onto `abyssorangemix2_hard_8832`, **no fall-through to agy**, and the switch warning fired |
| (4) named model, op it cannot do | ✅ `imagegen_no_provider` — "model … cannot do generate (it can: edit)", **no fall-through to agy** |

The per-model wire fields of decision 12 were checked too: the qwen row answers `ops=["edit"]`, no
`strength`, no `sizes`, while the existing rows (SDXL, krea2) answer three ops, `strength` and a size
list — **a control taken before trusting the reading**.

🔴 **Two things went wrong during acceptance** (neither is about the spec):

- **The positive control queued a REAL job and bought a box.** Checking "an SDXL row is not refused"
  through `/imagegen/jobs` was the wrong choice: being accepted there means being queued, which is
  recorded demand. **To watch an acceptance, pick a route that stops before execution.**
  `DELETE /imagegen/jobs/{id}` answered 200 but the job stayed `waking` until the box came up, and
  only then became `cancelled`.
- **Decision 8's two corrections** (folded into decision 8): declaring the number does not clear the
  prompt, and the file-sum estimate buys a bigger box.

## Revision — stop centre-cropping; shrink the whole picture to the encoder's rounding fixed point (2026-09-23)

**Decided by the user, not built yet.** The measurements behind it are
[docs/log/112](../log/112-kontext-crop-necessity.md) (§3–5: four pairs, eight runs; §11–§13: the
extra runs taken before building — §13 is PR #906). Decisions 3 and 4, "Rejected" and open item 2
are left as written, each with a 🔄 pointer here — so that what they rested on at the time stays
readable. This section was rewritten twice on the same day ("shrink to the nearest table entry" →
made conditional by a review → its present form after four isolating runs). A second review then
softened the claim about the mechanism and added how orientation and unreadable sizes are handled.

### Why it changes

1. **The cropped band is in the user's picture but not in the output.** On 1820x1024 that is 20 px
   off the top and the bottom; on 3:1 it is 22% of the width, gone without a word. The mask-painting
   UI (ADR 0081 P2, [log 111](../log/111-inpaint-mask-canvas-p2.md)) would have to draw that band,
   and the ratio table needed to locate it exists nowhere in this repository (111 §9, first 🔴).
2. **The model does not need the crop.** At the same 1392x752, a photo delivered **cropped** (today's
   wiring) and one delivered **whole and shrunk** keep the same texture (gradient-energy ratio — blind
   to a shift, lowered by blur — **1.0297 vs 1.0300** at seed 602, **1.0172 vs 1.0134** at seed 607;
   log 112 §12, R3 and R4; they agree tile by tile on a 3x3 grid too).
3. **Photos go soft when the size handed in is not a fixed point of the encoder's rounding
   (measured).** `TextEncodeQwenImageEditPlus` re-scales image1 **itself** to ~1 MP in multiples of 8
   before encoding the reference latent (ComfyUI v0.37.0 `nodes_qwen.py`: `s=sqrt(1024²/(w·h))`,
   `round(w·s/8)*8`, `round(h·s/8)*8`, re-sampled with `common_upscale`'s `"area"`). The eight photo
   points split on "fixed point or not", and both "inside or outside the ratio table" and "height"
   were refuted (log 112 §12–§13):

   | Size | In table | Fixed point | Photo | Gradient-energy ratio |
   |---|---|---|---|---|
   | 1392x752 (today's wiring, R3, R4) | yes | yes | 1496x800 | 1.02–1.03 |
   | **1400x752 (S1)** | **no** | yes | same | **1.0293** |
   | 1408x736 / 1376x736 | no | no | same | 0.8974 / 0.8823 |
   | 1424x752 (S2, 752 tall) | no | no | same | **0.8942** |
   | **1248x832 (S3, today's wiring, 3:2)** | **yes** | no | 1536x1024 | **0.4053** |
   | 1264x832 (S4) | no | yes | same | **0.7759** |

   (Calibration: on the 1496x800 photo a σ1.0 blur scores 0.874; on the 1536x1024 photo, measured at
   1248x832, σ0.7 scores 0.594. S3 and S4 are the same photo and seed, neither is cropped — the size
   is the only difference. But their latents differ in shape, so the same seed is not the same noise,
   and seed-to-seed spread on this photo was not measured. S4 itself falls on the "soft" side of the
   threshold fixed before the isolating runs (0.92 or below).)

   **The mechanism is not settled.** Two candidates, both consistent with the photo points:
   - **Grid misalignment**: the generated latent made from `enc.pixels` and the reference latent land
     on patch grids (latent/2) one row or column apart. The spatial skew this predicts (aligned at
     the top left, worse towards the bottom right) was weak in S2 and absent in S3.
   - **The reference itself going soft** (found by the second review): an `area` re-sample
     (`adaptive_avg_pool`) at a scale near 1 averages almost every pixel with its neighbour. Building
     only the reference, without a GPU, scores **0.45** for S3, 0.76 for S2, 0.75 for the T2 mutant
     and **1.00** at a fixed point (reproduced by the parent with the same implementation). That fits
     S3's even, frame-wide drop.
   There is an exception: 1376x768 (the A–C and T1 mutants, R2's mutant 1) is not a fixed point and
   its reference softens to 0.81–0.86, yet the output did not drop (R2's mutant 1 scored 0.843 against
   its control's 0.836). **The rule works under either mechanism** — at a fixed point there is
   neither a re-sample nor a grid offset.

🔴 **Today's wiring very likely has this defect too.** Four of the table's seventeen entries —
**1248x832, 1504x688, 832x1248, 688x1504** — are not fixed points and their patch counts differ too:
the same shape as the four points that went soft (1408x736, 1376x736, 1424x752, 1248x832). Only
1248x832 was measured directly, as one pair (S3, one seed); 1504x688 and the two portrait entries are
inferred. 1328x800 and its mirror are not strict fixed points either and their reference softens to
0.50–0.75, but their patch counts match — the shape of 1376x768, which did not drop — so they are not
counted here (**unsettled**; 1328x800 serves input ratios 1.58–1.756). The new rule picks strict fixed
points, which avoids both. Photos at input ratios 1.42–1.58 (which
includes the 3:2 of most DSLRs), 2.10–2.26 and their portrait mirrors **are coming back soft today**
(S3). The user decided to fix it as part of building this revision.

→ The "Rejected" entry was the option that **honours `size`** (a free pixel budget). What was
measured here keeps 1 MP and moves only the size. **A different option was measured**, so this
neither confirms nor overturns that rejection.

### What was decided

| What changes | How |
|---|---|
| Stop cropping (decision 4) | The input is **not cut**; the whole picture is shrunk and fed to `enc.pixels` / `pos.image1` / `neg.image1` (`ImageScale(crop="disabled")`) |
| The size it shrinks to (decision 4) | **The input's size with `TextEncodeQwenImageEditPlus`'s own rounding applied until it stops moving** (a fixed point). The Agent computes it from image1's size (`params.Width` / `params.Height`, already read at `comfy.go:1050`) and writes it into `ImageScale`'s width and height. Swept over ratios 0.25–4.0 it **always converges (in three steps at most)**, with **at most 1.52% aspect distortion** (ratio 3.819 → 2016x520). Outside that range it grows to 2.2% at ratio 8, 3.0% at 16 and 6.2% at 64 (six steps), which a no-crop rule accepts. Iteration **stops at 16 steps** and refuses if it has not converged (no such input has been found). The shrink uses **`upscale_method="lanczos"`** (every run measured used lanczos). The result is **by construction** the size the encoder makes for its reference |
| How the size is read | 🔴 **Compute from the size after orientation is applied.** The Agent reads the size with `image.DecodeConfig` (`comfy.go:1196-1197`), which ignores EXIF Orientation, while ComfyUI's `LoadImage` applies `ImageOps.exif_transpose` (`nodes.py`). Taken naively, an Orientation 5–8 phone JPEG (header 4032x3024, actually portrait) squeezes a portrait picture into a landscape frame (~77% ratio error) — an accident today's `FluxKontextImageScale` cannot have, because it reads the tensor. Recommended: the Agent reads EXIF Orientation and swaps width and height for 5–8 (no re-encode; ComfyUI does the rotation). The method is settled when building |
| Inputs whose size cannot be read | 🔴 Uploads accept `.webp` (`comfy.go:1260`), but imagegen registers only the gif, jpeg and png decoders, so a webp reads as size 0 (harmless today, because the size is not used). Recommended: register `golang.org/x/image/webp`'s `DecodeConfig`. An input whose size still cannot be read is **refused** for this family rather than shrunk to a guessed size. The method is settled when building |
| `FluxKontextImageScale` and the ratio table | **Neither is used**; the node leaves the graph. There is no table to hold and no table size to read through `GetImageSize` |
| Where the rounding formula lives | Copied into the Agent (three lines of arithmetic). 🔴 Decision 9's pin hazard remains here — the formula belongs to the ComfyUI version. **The pin-bump check** (ADR 0098's procedure: read the definition diff of every node we emit) gains `TextEncodeQwenImageEditPlus`'s reference rescale. Unlike a table, a change shows up in that diff |
| Mask wiring (decision 3) | `maskscale` goes. Picture and mask are both plain stretches, so the two maps agree by construction (measured: edge error **6.6 px → 3.0 px**, log 112 §11, T3; 3 px is inside the repaint's transition band. The 15.9 px in §11's table compares the control against the **no-crop** map's prediction, which it does not follow by design) |
| Mask size restriction (decision 3) | **Lifted.** Its reason (the crop splitting the two maps) is gone, so this family returns to the other families' "same relative region" |
| `size` (decision 4) | **Unchanged.** The caller cannot choose the size; no `sizes` are offered |
| Extra references (decision 5) | **Unchanged.** Still a bare `LoadImage` each |

Aspect distortion, against the first draft's "nearest table entry":

| Input | Nearest table entry (first draft) | Fixed point (this revision) |
|---|---|---|
| 1:1 | 1024x1024, 0% | 1024x1024, 0% |
| 3:2 (1536x1024) | 1248x832, 0% (🔴 not a fixed point) | 1256x840, −0.32% |
| 4:3 (1600x1200) | 1184x880, +0.91% | 1184x888, 0% |
| 16:9 (1820x1024) | 1392x752, **+4.15%** | 1368x768, +0.22% |
| 9:16 (1080x1920) | 752x1392, −3.96% | 768x1368, −0.19% |
| 3:1 (1920x640) | 1568x672, **−22.2%** | 1776x592, 0% |
| 4:1 (2048x512) | 1568x672, **−41.7%** | 2048x512, 0% |
| **Worst over ratios 0.25–4.0** | **−41.7%** | **1.52%** |

So the two questions the earlier decision left open **mostly lose their subject**: "shrink extreme
ratios to the table too" now costs at most 1.52%, and "put the output back to the input's ratio" is
no longer needed (it would be a re-scale of 1.5% or less, for little gain).

### Rejected (in this revision)

- **Shrinking to the nearest table entry** (this section's first draft). Four entries are not fixed
  points and photos go soft there (S3), and outside the table's range the distortion is large
  (+4.15% at 16:9, −22% at 3:1, −42% at 4:1).
- **The nearest table entry, with only the four non-fixed entries swapped.** Fixes the softness,
  keeps the distortion.
- **Qwen-Image 2.1's size rule** (keep the ratio, ~1 MP, multiples of 32 — the form of ComfyUI
  v0.37.0's `TextEncodeQwenImage21` and diffusers' `QwenImageEditPlusPipeline.calculate_dimensions`).
  A multiple of 32 is not necessarily a fixed point of this encoder: 1408x736 and 1376x736 are not,
  and those photos went soft (R1, T2).
- **The Agent holding the ratio table, or reading table sizes through `GetImageSize`.** Four of the
  table's sizes are themselves wrong; there is no longer a reason to use the table.
- **Cropping extreme ratios as before.** One exception, and the mask UI is back to drawing "this
  input has a band" — and with distortion at 1.52% at most there is nothing to crop for.

### Consequences (what building it touches)

- `comfy_workflows.go:1306` (`g["scale"]`) — `FluxKontextImageScale` goes; `ImageScale(crop="disabled")`
  is handed the fixed-point size, and the three seams move. The comment just above (`:1307-1311`,
  "that node exists to fix the FRAME") is rewritten too.
- A new function computes the fixed point, with a unit test asserting "converges over ratios
  0.25–4.0, the result is a fixed point, distortion at most 1.6%". Its comment names where the formula
  came from (`nodes_qwen.py`, v0.37.0). The expected values are a table built in Python with upstream's
  own `round` (round-half-to-even): the review checked that no W,H ≤ 20000 lands exactly on a rounding
  boundary, so Go's `math.Round` agrees — the table is there to catch a mis-copied formula (axis order,
  `int(1024*1024)`).
- `comfy_workflows.go:1386` (`comfyQwenEditNoiseMask`) — `maskscale` goes, and so does its doc
  comment's reasoning (`:1369-1385`, "applies the same crop").
- `comfy.go:1065` — the branch refusing a mask whose size differs from the picture's goes, with the
  reasoning at `:1058-1064` and the error string; the comment at `:1041-1044` ("FluxKontextImageScale
  scales image1") is rewritten.
- The goldens `comfy_qwen-image-edit-2509.golden.json` / `…-2511.golden.json`, the mask-wiring test
  from `comfy_workflows_test.go:817` (which asserts `maskscale` is a `FluxKontextImageScale`) and its
  comment (`:813-824`).
- The ComfyUI pin-bump procedure (ADR 0098) gains an item: read `TextEncodeQwenImageEditPlus`'s
  reference-rescale formula.
- The remaining comments that assume `FluxKontextImageScale`: `comfy.go:295` and `:602`,
  `comfy_workflows.go:640`, `:1220` and `:1465`, `console/src/features/imagegen/families.ts:139`.
- [log 111](../log/111-inpaint-mask-canvas-p2.md) §2 constraint 2 and §9's first 🔴 **lose their
  subject** with this revision; the mask-canvas design is rewritten from there.

Live acceptance (through the Agent's own route): (a) the banded input (log 112 §5's magenta/cyan)
keeps its bands in the output; (b) R3's photo (1496x800 → 1400x752) scores around 1.0; (c) with a
mask, the edge error is at T3's level; (d) **the 3:2 photo (S0), over two seeds or more, scores at S4's
level or better** — the check that today's S3 (0.4053) is fixed; the rule picks 1256x840, a size not
measured and not S4's 1264x832; (e) 16:9 and 3:1 photos score at (b)'s level; (f) **an EXIF-rotated
JPEG** (Orientation 6, say) comes back portrait and unsqueezed.

### Open (for this revision)

1. **The mechanism is not settled** (grid misalignment, or the reference itself going soft — point 3
   above). Eight photo points, two scenes and three seeds split on "fixed point or not", and no more.
   Acceptance (d) and (e) are the chance to check the rule itself on hardware.
2. **S4 does not reach 1.0 either** (0.7759). The S0 photo is dense with fine wood grain, and the model
   loses texture there even at a fixed point. This revision fixes the gap from S3 to S4, not beyond it.
3. **2509 has not been measured on a photo** (only T1's illustration). It uses the same encoder node,
   so the same rule should hold — an inference.
4. **Extra references were not measured.** They are re-scaled by the same encoder into reference
   latents, but only image1 has to share a grid with the generated latent (`index_timestep_zero` gives each
   reference its own index — checked against upstream by the review; not measured on hardware).
5. **Not carried over to ADR 0098's Qwen-Image 2.1.** 2.1 re-scales its references through
   `TextEncodeQwenImage21`, a different arrangement; point 3 was measured on 2511.

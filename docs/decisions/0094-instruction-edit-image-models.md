# 0094. Instruction-edit models in the image engine — Qwen-Image-Edit, and `op=edit` meaning two different things

English | [日本語](0094-instruction-edit-image-models.ja.md)

- Status: **P0 built and accepted on real hardware** (2026-09-20). All four completion criteria
  passed on the dev deployment — the record is "P0's live acceptance" at the end.
  🔴 The `file:line` references below have been **re-pointed at the tree as built**; they were taken
  on `ae069aaf` while drafting. Whoever moves those lines in P1 re-points them.
  🟢 **Reviewed before implementation** (2026-09-20, in a separate session). The nine findings are
  folded into the text; what came off is recorded in "What the review took off" at the end.
  🟢 **The graph and the capabilities were MEASURED before this was written** (dev deployment,
  g6.xlarge / L4 24GB, ComfyUI 0.35.2, five runs on 2026-09-20). The "Measured" section is that
  record, and **decisions 2, 3, 4 and 5 each rest on it**.
  🟢 The number is settled (develop holds up to 0093).
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
caller's picture changes — **partial-denoise img2img** (`comfy_workflows.go:345`,
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
(`comfy_workflows.go:443`), and `engine_catalog_test.go` reads the Agent's source to keep them
equal. **Add it in both.**

### Decision 2 — This family does not take `strength`. Its `denoise` is fixed at 1

🔴 **Run C is the whole of this decision.** The same seed and the same prompt, with `denoise` at
0.6, came back with the sign unchanged — **and nothing said so**. Today's edit path returns 0.6 by
default (`denoise()`, `comfy_workflows.go:313`), so **dropping this family into it makes "I asked
for an edit and nothing happened" the default behaviour**.

- Make `Caps.Strength` a per-family answer (it is `true` for everything today, `comfy.go:138`) and
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
  (`imagegen.go:1019`) has a `!caps.Strength` branch that never fired, and the reason was the CALLER:
  it passed the Caps of the model the REQUEST named, which is decision 11's union (true) when that is
  empty.

  So **the Caps handed to `requestWarnings` is the row that actually ran** — `Run`
  (`imagegen.go:922`) and the queue's `finish` (`jobs.go:486`) pass `Caps(res.Model)`.
  🟢 **A strength-shaped twin on the provider side is rejected** (this ADR's first draft asked for
  one): it patches a core mistake inside comfy alone, **the same mistake was already live for
  `negative`**, and the next capability that becomes per-model would repeat it. One fix in the core
  gives every provider the same guarantee. The price is that the sentence goes from naming the
  family to the generic "this route cannot vary how much of the input it keeps"; putting the family
  name back belongs in the core's message or in `Caps` carrying a reason — a different decision.
- ADR 0069's rule for adding a word ("is there no way around it for the caller") **points the other
  way here**: it is not that the caller has no workaround, it is that the knob has no meaning.

### Decision 3 — `Ops` becomes a property of the family. This one is edit-only

`comfy.go:121` answers `[generate, edit, inpaint]` for every model. Qwen-Image-Edit **cannot work
without an input image** (with no image the encoder is plain text conditioning, which is not a use
upstream documents). Make `Ops` per family and let this one claim `[edit]` alone.

It does **not** claim `inpaint`. A mask could be added with `SetLatentNoiseMask` and the graph
would validate, but nobody here has run it — **a capability this deployment has not measured is
not one it declares** (the lesson SD3.5 charged us in ADR 0072).

### Decision 4 — `size` offers no choices for this family, and the row cannot override it

The output size is `FluxKontextImageScale` picking the nearest entry of
`PREFERRED_KONTEXT_RESOLUTIONS` by the **input's aspect ratio** (read off the node's v0.35.2 source;
the five runs went 1024² in → 1024² out, which — being all square — does **not** evidence the ratio
table). `comfySizesFor` (`comfy.go:575`) answers **empty** for this family, and a `size` that
arrives anyway is refused the way decision 2 refuses `strength`.

🔴 **The row's own `sizes` are not honoured either.** `comfySizesFor` returns `conn.Sizes[model]`
ahead of the family's list (`comfy.go:576`), so an empty family answer alone would still let an
operator's declared presets through. This family is the one case where **the family wins over the
row** — the point is not to offer a value that cannot take effect, and that reason belongs in this
line of the ADR.
- The Console has the same hole: `sizeOptions` (`families.ts:145`) falls back to
  `familyCard(f)?.sizes ?? DEFAULT_SIZES`, so **a card that omits `sizes` shows the megapixel
  list**. The card states `sizes: []`, and the field itself is not drawn when the list is empty.

🔴 **Dropping `FluxKontextImageScale` to honour `size` is rejected** — see below.

### Decision 5 — `MaxInputs` becomes per family. P0 stays at one; the second image opens with its path in P3

`TextEncodeQwenImageEditPlus` takes `image1..image3`, and **run D proved the second one works** (the
potted plant from the second picture entered the first scene with its colour and shape intact).
Make `Caps.MaxInputs` (fixed at 1, `comfy.go:125`) per family.

🔴 **Declaring 2 does not carry a second image.** Today `p.uploadImage(…, req.Inputs[0])`
(`comfy.go:1014`) uploads **one**, and `comfyParams.Image` is a single string
(`comfy_workflows.go:134`). `comfyCheckInputs` (`comfy.go:1092`) only tests
`len(req.Inputs) > caps.MaxInputs`, so raising the number alone lets a second image **pass the check
and go unused** — run C's failure mode exactly: a wrong picture with no warning. Therefore:

- **P0 keeps `MaxInputs` at 1**, and the second image opens **with its path, in P3** (make
  `comfyParams` plural, wire `image2`, and fix the refusal's singular wording).
- 🔴 **The third opens after it is measured.** "The node takes `image3`, so the mechanism is the
  same" is exactly the inference decision 3 forbids for inpaint. Measure three once in P3.

⚠️ **`MaxInputs` is not on the wire** (`providerStatus` has no field for it; the only place it
reaches the outside is the refusal at `comfy.go:1092`). For the pane to state the limit, P3 has to
add the field.

### Decision 6 — The unit of a family is the **topology, not the version**. 2509 and 2511 are wired differently, so they are two

2511's template is **a different topology**: `FluxKontextMultiReferenceLatentMethod(index_timestep_zero)`
sits on both conditionings and `ModelSamplingAuraFlow`'s shift moves 3.0 → 3.1 (steps and cfg come
from the switch's false branch: 40 and 4). **Rewiring nodes cannot be expressed by a row's `params`**
(four words: steps, cfg, sampler, scheduler).

🔴 **The rule is "one family per topology", not "one per version".** The difference will matter:
if 2512 ships with 2511's wiring, **no family is added and the row's `params` suffice**. What adds a
family is upstream inserting or removing nodes, not a version number going up.

The strongest support for the rule is in props: `comfyFamilyFromPrefix` (`props.go:489`) recovers the
family from `SaveImage`'s `af-<family>`, so **two topologies inside one family make it impossible to
say afterwards which graph drew a picture** (ADR 0081 decision 3's "what this was made from" becomes
a lie).

- **Both spellings carry the version**: `qwen-image-edit-2509` and `qwen-image-edit-2511`.
  `base_model` is a string that lives in `engine_models`, so changing it later is a migration of
  deployed catalogues — **decide it now**. The asymmetric pair (`qwen-image-edit` / `…-2511`) becomes
  meaningless the day 2512 arrives with 2509's wiring. **A family name points at the version that
  first shipped that topology; it is not an alias for a version** — that sentence goes into the
  ingest UI's family selector.
- ⚠️ **The cost: one LoRA row per family.** `comfyResolveLoras` (`comfy.go:647`) matches `base_model`
  exactly, so the Lightning LoRA has to be registered twice. "Leave Lightning to the row" (rejected,
  below) stops being one row the moment the family splits.
- The number: one family is about **12 declarations** (`engineComfyFamilies`,
  `engineComfyRequiredFlags`, `engineFamilyParts`, `comfyFamilies`, the template switch,
  `comfyFamilyKnobs`, `comfyFamilyTakesNegative`, `comfyFamilyRecipes`, `comfyTrialSteps`,
  `wire.ts`'s `Family`, `FAMILY_CARDS`, the goldens). Decision 9's trigger is drawn from that number.

### Decision 7 — Ingest stays one press: add one family to the parts table

Add two parts to `engine_family_parts.go:48`. `engineFamilyPart` has **four fields — Flag, Repo,
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
physical size, and `engine_class.go:41` says why) it always asks for `confirm_vram`.

**Measured** (`/system_stats`, raw): `vram_total` 23,659,151,360 B = **22,563 MiB**, `vram_free`
1,783,934,774 B = 1,701 MiB, so **20,862 MiB in use** — inside the rung. The conditions were
**1024², batch 1, one reference image**. `comfyMaxBatch` is 4 (`comfy.go:632`) and **that range was
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

### Decision 9 — "Custom workflows" stay out of this ADR; the trigger is duplication, not the family count

ADR 0081's rejected "let the Console post a raw ComfyUI graph" **stands**, for the reason it gave
(the template is the contract that gives `base_model`, `params`, a row's negative and the LoRA
family check their meaning; a raw graph walks past all of them). Measuring added two concrete
dangers:

- **The step ceiling disappears.** `validateRequestParams` (`jobs_http.go:177`) guards the knob route
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

`textBehind` (`props.go:446`) returns the **prompt and the negative** only when it lands on
`CLIPTextEncode`, and the size is read by its caller (`props.go:411`) off `Empty*LatentImage`. This
family's graph has **neither**, so shipping it as-is records **an empty prompt, an empty negative and
an empty size** — the gallery's "what this picture was made from" would lie. Add
`TextEncodeQwenImageEdit` / `…Plus` as a source for both texts, and take the size from the image that
reached `SaveImage`.

🟢 Recovering the family itself already follows: `comfyFamilyFromPrefix` (`props.go:489`) walks
`comfyFamilies`, so decision 1 makes it readable with no further change.

### Decision 11 — Family properties decide GENERATION; what is ADVERTISED is a union across the models

🔴 **Without this, decisions 2-5 produce "editing once makes generation fall through to a paid
provider".** Capabilities leave through `http.go:217`'s `caps := p.Caps("")` — **the warm default
model, one row** — and from there into `st.Ops` (`http.go:227`), `st.Strength` (`http.go:234`), the
MCP tool definition (`mcp_stdio.go:1132`'s `op` enum) and the pane's fields. Worse,
`chooseImageProviders` (`imagegen.go:742`) drops a **whole provider** on
`caps(id).Supports(req.Op)`, so while a qwen row is warm, `op=generate` takes comfy out of the
candidates and lands on a provider that spends a member's plan.

The same trap was already hit with `negative_prompt` and fixed with a **union across the models**
(`http.go:237-247`, with the reason in its comment). `Ops`, `Strength`, `Sizes` and `MaxInputs` take
the same shape:

- 🔴 **The union goes inside `comfyProvider.Caps("")` itself**, not in the route. `Caps` resolves an
  empty model to `DefaultModel()` — the warm row — at `comfy.go:121-125`, so **making that one answer
  a union over the enabled rows fixes both the advertising (`http.go:217`) and the candidate filter
  (`imagegen.go:842`'s `capsOf` → `imagegen.go:742`) at once**. `Caps(model)` for a named model stays
  exactly the family's answer, so judging stays strict.
  ⚠️ **The negative precedent (`http.go:242-247`) unions in the ROUTE, and copying that shape alone is
  not enough**: `capsOf` would still see the warm row, `op=generate` would drop comfy at the
  candidate filter, and the third bullet below (the resolution inside `Generate`) would never be
  reached.
  ⚠️ **A union is right for one surface and wrong for the other.** The MCP tool definition
  (`mcp_stdio.go:1132`'s `op` enum) is a **connect-time snapshot** and cannot be per model at all, so
  a union is correct there and a model-specific refusal can only be decision 2's 400. **The pane
  needs per-model** (decision 12).
- **Judging** (at generation) is `Caps(model)` — `imagegen.go:842`'s `capsOf` already asks
  `p.Caps(req.Model)`, so a request that names a model is already right.
- 🔴 **With no model named, comfy's own model resolution has to read `req.Op`.** The union only
  keeps the provider in the candidates: `comfy.go:647-656` resolves an empty `req.Model` to
  `DefaultModel()` — the warm row — and answers `the self-hosted image engine cannot do generate`
  on `!caps.Supports(req.Op)`. `Run` files that under attempts and **`continue`s**
  (`imagegen.go:898-900`), i.e. **falls through to the next provider, which spends a member's
  plan**, and `recordUsage` (`imagegen.go:894`) writes a failed row on the way. So when the warm
  row's family does not claim the op, resolve to **the first enabled row that does** and say so
  with `comfySwitchWarning` (`comfy.go:782`). A checkpoint switch costs 1-2.5 minutes (measured),
  which is explainable; silently billing another plan is not. **P0's third criterion is only
  testable once this exists.**

### Decision 12 — There are SIX per-family properties. Dropping `cfg` and `negative` produces a false warning

`comfyFamilyKnobs` (`comfy.go:277`) and `comfyFamilyTakesNegative` (`comfy.go:387`) enumerate
families in a hard-coded switch, and **an unregistered family falls to the default**. Forgetting a new
family there means:

- `comfyFamilyKnobs` answers `["steps"]`, so `comfyIgnoredParamWarnings` (`comfy.go:212`) returns
  "cfg=4 was not applied: the qwen-image-edit family folds its guidance into the conditioning" —
  **the opposite of run A**, which edited at cfg 4;
- `comfyFamilyTakesNegative` answers false, so `Caps.Negative` and the pane's negative field
  disappear.

So the per-family set is **six**, not four: `Ops`, `MaxInputs`, `Strength`, `Sizes`, plus `knobs`
(steps, cfg, sampler, scheduler) and `negative`. This family is guided at cfg 4, so `negative` is
**true** (its graph puts the same `TextEncodeQwenImageEditPlus` on the negative branch).

🔴 **Two per-model fields go on the wire; decision 11's union only works paired with them.** With the
union inside `Caps("")`, `st.Ops` (`http.go:227`) and `st.Strength` (`http.go:234`) become
provider-level "some row here can do this" — **true of no particular model**. But `modelStatus`
(`http.go:142-176`) carries neither `ops` nor `strength`; its only per-model line is `Knobs`, defined
as a subset of `steps cfg sampler scheduler negative`. So the two things the Console line below
requires — stop `jobs.ts:203` sending `strength` unconditionally, take the `op` choices from the
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
(`imagegen.go:742`) filters candidates on `caps(id).Supports(req.Op)`, and `capsOf` asks
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
  `chooseImageProviders` (`imagegen.go:743-745`), so this decision is about **auto only**. As built,
  the pin itself is `modelOwner` (`imagegen.go:761`) and the gate that calls it is `Run`'s
  `pref == "" || pref == "auto"` (`imagegen.go:858`).

## Rejected

- **Dropping it into the existing edit path.** Run C: at the 0.6 default it quietly returns an
  **unedited** picture.
- **One family holding two topologies, switched by a declaration.** The difference (reference-method
  node, shift) cannot be expressed by four words of `params`, and `comfyFamilyFromPrefix`
  (`props.go:489`) recovers the family from `af-<family>`, so **which wiring drew a picture becomes
  unanswerable**. Templates that branch on a declaration do exist (`comfyModelTakesNegative` and
  `comfyFamilyRecipes` change with a row's `params`) — but those branch on NUMBERS, not on wiring.
- **Removing `FluxKontextImageScale` to honour `size`.** The output size becomes free, but leaves
  the ratio table upstream says it trained on. That trades quality for a knob — and **the result of
  removing it was not measured**. Decision 4 prefers saying "it does not apply".
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
  properties of decisions 11 and 12), `props.go` (decision 10), `jobs.go:105`'s `comfyTrialSteps`
  (**omitting it fails `TestEveryFamilyHasTrialSteps`**, `comfy_test.go:1585`; run B's 8 steps /
  63.2 s is the citation), `mcpx/mcp_stdio.go:1132` (the `op` enum and its description — the first
  case where which ops exist depends on the model; the reference-image argument is **`inputs`**, not
  `images`, `maxItems` 5). 🔴 **`strength`'s description (`mcp_stdio.go:1219-1221`) is rewritten
  too**: it is offered on the union, so "0.6 when omitted" alone walks an agent into a 400 every
  time — it has to say that some checkpoints refuse it, and that the answer is a 400. **P3's second image lands here too**: make `comfyParams` plural
  (`comfy_workflows.go:134`'s `Image string`), call `uploadImage` more than once (`comfy.go:1014`
  takes `req.Inputs[0]` alone), wire `image2`, and fix the singular wording of `comfyCheckInputs`'
  refusal (`comfy.go:1092`).
- **Wire**: `ops` on `modelStatus`, `strength` in `Knobs` (decision 12), and the Console's mirror of
  both: `wire.ts:35`'s `Knob` is a **closed union**
  (`"steps" | "cfg" | "sampler" | "scheduler" | "negative"`), so it will not compile until the type
  changes, and `ImagegenModel` (`wire.ts:86`, `knobs?: Knob[]`) has no `ops`. 🔴 **Three comments
  spell that vocabulary out** — `providerStatus.Strength` (`http.go:108-112`, "No union is needed:
  it is per provider, not per model", which decisions 2 and 11 make false), `comfyFamilyKnobs`
  (`comfy.go:267-276`) and `modelStatus.Knobs` (`http.go:161-164`). All three say "a subset of those
  five words", so **the same change rewrites all three**.
- **CP**: `engine_catalog.go` (two words, required flags), `engine_family_parts.go` (the table), and
  `engine_class.go` is **left alone** (decision 8).
- **Console**: two family cards in `families.ts` (dialect `sentences`, no quality chips, steps 20 for
  2509 and 40 for 2511, `sizes: []` and **no size field at all**), `wire.ts:39`'s `Family` type,
  `families.test.ts`'s family list, **`jobs.ts:203`'s unconditional `strength`** (it is sent whenever
  `op !== "generate"`, so decision 2 would refuse every edit), the `op` choices (`draft.ts`'s constant `OPS`, walked at `GenerateForm.tsx:417`, never the
  status's `ops`), and `GenerateForm.dom.test.tsx` (the test that greys fields out on `knobs` —
  extend it for `strength` and `ops`, without breaking its other claim: an ABSENT `knobs` leaves the
  whole form usable).
- **The refusal has two homes** (decision 2): the blocking `/imagegen/generate` (`http.go:465`) and
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
  size field) runs through `sizeOptions` (`families.ts:145`), which reads the card, so without one the
  megapixel list stays. That it is currently harmless rests on the size field being
  `disabled={isEdit}` — an accident, and an accident is not a specification. Three completion criteria: (1) **run A reproduced** on real hardware
  (it edits), (2) **run C, naming qwen, refused with 400 on both routes** (no `strength` here), and (3) 🔴 **with a qwen row warm,
  `op=generate` still lists comfy** (decision 11's union holds, so nothing falls through to a paid
  provider), and (4) **naming qwen as the `model` while asking for `op=generate` is refused with the
  model's name in it** (decision 13 — a named request is not dropped).
- **P1** — 2511 (decision 6), the parts table (decision 7), the operator-entered `vram_mib`
  (decision 8). Done when one press stages all three parts in the right directories, **the screen offers
  the measured number (20,862 MiB at 1024², batch 1, one reference) right after the ingest, and
  `confirm_vram` stops appearing once the operator has entered it** (until then it appears, which is
  the correct behaviour — decision 8).
- **P2** — props (decision 10). Done when a picture made from the pane shows its prompt, its
  negative and its size under "what this was made from".
- **P3** — the reference-image path end to end (pane and the MCP `inputs` argument) and `MaxInputs`
  on the wire. Done when run D (two images) can be reproduced from the pane, and **three images are
  measured once** before `MaxInputs` goes to 3 (until then it stays 2).

## Open

1. **2511's 40 steps are expensive** (measured 393.8 s). The Lightning LoRA (4 steps) as a row has
   not been measured for quality. Measure one in P1.
2. **Whether `inpaint` can be claimed** (deferred in decision 3). `SetLatentNoiseMask` on top of a
   denoise-1 instruction edit is unmeasured.
3. **16.1 GB of host RAM is thin** (2.2 GB free). A box with both families enabled, switching
   repeatedly, has not been measured — the run only switched once.
4. **Decision 9's trigger** (a third topology / past 15 declarations) is drawn from the measured 12,
   but "15" is not itself a measured number. Count the declarations again when the next family lands.
5. **The operator entering `vram_mib` once** (decision 8) is forgettable unless the screen asks for it
   right after the ingest. Where the measured number appears in the ingest UI is a P1 question.

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
  and out, so `PREFERRED_KONTEXT_RESOLUTIONS` was never exercised off-square.
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
  `negative_prompt` and fixed with a union (`http.go:237-247`) — that fix was there to be read.
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
  (`comfy.go:576`, `families.ts:145`).
- 🟡 **Decision 9's "past 12 families" trigger measured the wrong thing** (families unrelated to
  editing would fire it). It now counts duplication, and names the table-driven middle step.
- 🟡 **Three names were wrong**: the `op` enum is in `mcp_stdio.go:1132`, not `mcp_imagegen.go`; the
  reference-image argument is `inputs`, not `images`; the size is read at `props.go:411`, not in
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
  precedent it cited unions in the ROUTE (`http.go:242-247`), so copying that shape leaves `capsOf`
  (`imagegen.go:842`) looking at the warm row: `op=generate` drops comfy **at the candidate filter**
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
comment rewrite: `providerStatus.Strength` (`http.go:108-112`) still claims "No union is needed: it is
per provider, not per model", which decisions 2 and 11 make false.

🟢 The review's sweep of "what else reads `Caps("")`" is worth recording: five call sites take an
unnamed model (`http.go:217`, `imagegen.go:842`, `:821`, `:856` and **`jobs.go:486`** — the last one
this ADR had never named).

⚠️ **Its conclusion that "no warning is lost" was wrong.** It counted the negative correctly and
missed what `strength` becoming per-model had just added; the last two call sites — the warning path
— are readers that must NOT see the union. P0 closes it by passing `Caps(res.Model)` (decision 2).
**The union is now read by three places only**: the advertisement (`http.go:217`) and the candidate
filter (`imagegen.go:842`, `:821`).

### Fifth round (against `a1583944`)

**No 🔴 left.** The three 🟡 were all about the granularity of the consequences, and all three are
fixed.

- 🟡 **Adding a wire field touches three more places**: `wire.ts:35`'s `Knob` is a closed union (it
  will not compile until the type changes) and `ImagegenModel` has no `ops`; and two more comments
  spell the same vocabulary out (`comfy.go:267-276`, `http.go:161-164`) where the consequences named
  only `http.go:108-112`.
- 🟡 **Decision 2's 400 has two homes** (`http.go:465`, `jobs_http.go:115`). With only one, the pane
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
  (`comfy.go:121-125`).
- Decision 13's pin is `modelOwner` (`imagegen.go:761`) and the gate in `Run` that calls it
  (`imagegen.go:858`).
- Decision 2's refusal is `bad_strength_family` (`http.go:479`, `jobs_http.go:124`) — a **separate
  code** from the out-of-range `bad_strength`, and decision 4's size refusal has the same shape
  (`bad_size_family`).
- The warning for the route that cannot be refused lives in the CORE: `requestWarnings` is handed
  the Caps of the row that actually ran (`imagegen.go:922`, `jobs.go:486`). The provider-side twin
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


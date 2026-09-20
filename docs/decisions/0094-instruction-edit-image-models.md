# 0094. Instruction-edit models in the image engine — Qwen-Image-Edit, and `op=edit` meaning two different things

English | [日本語](0094-instruction-edit-image-models.ja.md)

- Status: **drafted** (2026-09-20). Not built. Every "exists" / "does not exist" claim below was
  checked by grep on `ae069aaf`.
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
and syncing 30 GB onto it cost a further 348.9 s. The card was an **L4 24GB** (`vram_total`
23.7 GB, 1.8 GB free after a generation), the host had 16.1 GB of RAM.

## Decisions

### Decision 1 — Add two families. Add no file-role words

`base_model` gains `qwen-image-edit` (2509) and `qwen-image-edit-2511`. Both declare three roles —
`--diffusion-model`, `--clip_l`, `--vae` — so **`EngineFile`'s flag vocabulary does not grow at
all** (two lines in `engineComfyRequiredFlags`, `control-plane/engine_catalog.go:196`). The text
encoder is Qwen2.5-VL-7B (declared as `--clip_l` by the existing convention for a family with one
encoder), and the VAE is the Qwen-Image one — **the same key anima and krea2 already hold**.

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
- When a caller sends `strength` anyway, refuse it **out loud**, the way
  `comfyIgnoredParamWarnings` already does (ADR 0081 decision 4).
- ADR 0069's rule for adding a word ("is there no way around it for the caller") **points the other
  way here**: it is not that the caller has no workaround, it is that the knob has no meaning.

### Decision 3 — `Ops` becomes a property of the family. This one is edit-only

`comfy.go:121` answers `[generate, edit, inpaint]` for every model. Qwen-Image-Edit **cannot work
without an input image** (with no image the encoder is plain text conditioning, which is not a use
upstream documents). Make `Ops` per family and let this one claim `[edit]` alone.

It does **not** claim `inpaint`. A mask could be added with `SetLatentNoiseMask` and the graph
would validate, but nobody here has run it — **a capability this deployment has not measured is
not one it declares** (the lesson SD3.5 charged us in ADR 0072).

### Decision 4 — `size` offers no choices for this family

The output size is decided by `FluxKontextImageScale` from the **input's aspect ratio** (measured:
1024² in, 1024² out, all five runs). `comfySizesFor` (`comfy.go:429`) answers **empty** for this
family, and a `size` that arrives anyway is reported the way decision 2 reports `strength`.

🔴 **Dropping `FluxKontextImageScale` to honour `size` is rejected** — see below.

### Decision 5 — Up to three reference images; `MaxInputs` becomes per family

`TextEncodeQwenImageEditPlus` takes `image1..image3`, and **run D proved the second one works** (the
potted plant from the second picture entered the first scene with its colour and shape intact).
Make `Caps.MaxInputs` (fixed at 1, `comfy.go:125`) per family and answer 3 here. The third image
rides the same mechanism, so it opens at the same time — while the description says plainly that
**quality per image count has not been measured**.

### Decision 6 — 2509 and 2511 are **separate families**

2511's template is **a different topology**, not different numbers:
`FluxKontextMultiReferenceLatentMethod(index_timestep_zero)` sits on both conditionings,
`ModelSamplingAuraFlow`'s shift moves 3.0 → 3.1, and the recipe is 40 steps. **Rewiring nodes cannot
be expressed by a row's `params`** (four words: steps, cfg, sampler, scheduler). So each version
adds a family.

⚠️ **This is not a comfortable answer.** Upstream shipped 2509, 2511 and 2512 within months, and this
rule grows "one family per graph" once per version. Decision 9's trigger comes from here.

### Decision 7 — Ingest stays one press: add one family to the parts table

Add `qwen-image-edit`'s two parts to `engine_family_parts.go:48` — `--clip_l` is
`Comfy-Org/Qwen-Image_ComfyUI`'s `split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors`,
and `--vae` is the shared key `image/vae/qwen_image_vae.safetensors`. 2511 names the same two.
**Both were actually taken in on 2026-09-20**, which is the standard ADR 0085 sets for that table.

- The **scaled** fp8 encoder is the right one. krea2's note ("the fp8 conversion breaks the vision
  tower") does not apply here: the shipped template names this exact file, and runs A and D read
  reference images with it.
- The weights are fp8 (2509: 20.43 GB, 2511: 20.53 GB). bf16 (40.86 GB) does not fit an L4.

### Decision 8 — Leave the VRAM guard's estimate alone; silence it with a declared `vram_mib`

`engineModelVramNeed` (`control-plane/engine_class.go:253`) floors the demand at the **sum of the
row's files**. This family sums to 20.43 + 9.38 + 0.25 GB = **28,676 MiB**, so on an L4 (22,000 MiB
declared) it always asks for `confirm_vram`. **It fits in practice** (measured: the weights resident
on a 23.7 GB card, 1.8 GB free).

Do **not** change the formula — subtracting a text encoder would be a lie for other families.
Instead let the ingest write the family's **measured `vram_mib`** onto the row, using the existing
branch where `m.VramMiB > 0` wins over the sum. Declaring a measured number rather than weakening a
guard is the shape ADR 0074 decision 6 already uses.

### Decision 9 — "Custom workflows" stay out of this ADR; only the trigger is decided

ADR 0081's rejected "let the Console post a raw ComfyUI graph" **stands**, for the reason it gave
(the template is the contract that gives `base_model`, `params`, a row's negative and the LoRA
family check their meaning; a raw graph walks past all of them). Measuring added two concrete
dangers:

- **The step ceiling disappears.** `validateRequestParams` guards the knob route only; a raw graph
  can hand a shared GPU an hour-long job.
- **Pinning stops meaning anything.** The graph in this ADR was written against v0.35.2's node
  definitions, and the node API promises nothing across versions (ADR 0072 decision 4). JSON pasted
  onto a row breaks **silently** on the day the engine is upgraded.

**Trigger**: when a **third version** of the same capability demands its own topology (say a 2512-era
edit model differing from 2511), or when the family list passes **12**, open the custom-workflow
ADR. Until then, pay decision 6's price.

### Decision 10 — Teach the props reader two more nodes

`textBehind` (`props.go:446`) returns a prompt only when it lands on `CLIPTextEncode`, and reads the
size off `Empty*LatentImage`. This family's graph has **neither**, so shipping it as-is records
**an empty prompt and an empty size** — the gallery's "what this picture was made from" would lie.
Add `TextEncodeQwenImageEdit` / `…Plus` as a prompt source and take the size from the image that
reached `SaveImage`.

## Rejected

- **Dropping it into the existing edit path.** Run C: at the 0.6 default it quietly returns an
  **unedited** picture.
- **One family for both versions, switched by the row's `params`.** The difference is topology
  (reference-method node, shift), which four words cannot express; and a template that branches on
  a declaration destroys the current property that reading a family's graph answers the question.
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

- **Agent**: `comfy_workflows.go` (two families, one template plus 2511's delta, per-family
  denoise), `comfy.go` (`Ops` / `MaxInputs` / `Strength` / `Sizes` become per-family),
  `props.go` (decision 10), `mcp_imagegen.go` (the `op` enum and its description — the first case
  where which ops exist depends on the model).
- **CP**: `engine_catalog.go` (two words, required flags), `engine_family_parts.go` (the table),
  and `engine_class.go` is **left alone** (decision 8).
- **Console**: two family cards in `families.ts` (dialect `sentences`, no quality chips, steps 20
  for 2509 and 40 for 2511, **no size field**).
- **Deployment**: 20 GB more per checkpoint. The box keeps models on NVMe so the space is there, but
  **the cold-start sync grows** (measured: 348.9 s for 30 GB, purchase included).
- **Tests**: two goldens in `comfy_workflows_test.go`, the `engine_catalog_test.go` equality check,
  `families.test.ts`.

## Phases

- **P0** — the `qwen-image-edit` (2509) family and the per-family properties of decisions 2-5, with
  goldens and docs. Done when **A and C are reproduced on real hardware**: A edits, and C is
  refused with "this family does not read strength".
- **P1** — 2511 (decision 6), the parts table (decision 7), the default `vram_mib` (decision 8).
  Done when one press stages all three parts in the right directories and enabling needs no
  `confirm_vram`.
- **P2** — props (decision 10) and the Console cards. Done when a picture made from the pane shows
  its prompt and size under "what this was made from".
- **P3** — the second and third reference images end to end (pane and the MCP `images` argument).
  Done when run D can be reproduced from the pane.

## Open

1. **2511's 40 steps are expensive** (measured 393.8 s). The Lightning LoRA (4 steps) as a row has
   not been measured for quality. Measure one in P1.
2. **Whether `inpaint` can be claimed** (deferred in decision 3). `SetLatentNoiseMask` on top of a
   denoise-1 instruction edit is unmeasured.
3. **16.1 GB of host RAM is thin** (2.2 GB free). A box with both 2509 and 2511 enabled, switching
   repeatedly, has not been measured — the run only switched once.
4. **The family-count trigger (12, decision 9) is not a measured number.** Re-derive it when the
   next version arrives.

## Measured (2026-09-20, dev deployment, g6.xlarge / L4 24GB)

The graph is the shipped template's subgraph copied into API format, 10-12 nodes, with the UI-only
nodes (`ComfySwitchNode`, `Primitive*`) folded away. Every node's existence was confirmed against
the **running** engine's `/object_info` (`CLIPLoader`'s `type` **does offer `qwen_image`**, one of
28; `TextEncodeQwenImageEditPlus`, `FluxKontextImageScale` and `CFGNorm` are there too). The engine
reported **ComfyUI 0.35.2**, which is the pinned ref.

- **It fits an L4 24GB.** `vram_free` 1.8 GB of 23.7 GB after a generation. The guard's 28,676 MiB
  demand notwithstanding, the text encoder is evicted after encoding (decision 8).
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

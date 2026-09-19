# 0089. One card per quantisation repository, and "will it fit" answered before the download

English | [日本語](0089-quantisation-ladder-and-fit.ja.md)

- Status: **accepted** (2026-09-18). Built in the same change. Every "exists" / "does not exist"
  claim was checked by grep on `2939551d` (develop, ADR 0088 merged), and every "measured" claim
  was read off the live upstream APIs or the real GGUF headers the same day.
- Builds on: [0088](0088-model-catalogue-names-and-examples.md) (the card's name and its grouping,
  which this makes one level finer for the chat role), [0072](0072-engine-model-catalog.md)
  decision 3 (the window is per MODEL), [0074](0074-engine-instance-classes.md) decision 6 (what a
  model wants on the card, and how well that is known).

## Context

Two reports, one root.

**"Adding another quantisation means finding the repository again."** A GGUF repository is one
model published at a dozen sizes. Measured live on `unsloth/Qwen3.8-27B-GGUF` (2026-09-18): thirty
`.gguf` files — fourteen quantisations from `UD-IQ1_S` (6.19 GB) to `BF16`, plus an importance
matrix and two vision projectors. The catalogue drew one card per quantisation, related to each
other by nothing, and the only road to a second size was the 探す tab and the repository name typed
again.

**"It says `KV キャッシュ 66560 MiB` whatever file I pick — this will never run on 22,000 MiB."**
The arithmetic was right and the input was wrong:

- Read off the real headers, 2026-09-18: `UD-IQ4_XS` and `UD-Q2_K_XL` declare `block_count` **65**,
  `head_count_kv` **4**, `key_length`/`value_length` **256**/**256**, `context_length` **262144**.
  `65 × 4 × (256+256) × 262144 × 2 bytes` is exactly **66,560 MiB**.
- It looks constant across the repository because the KV cache is decided by the attention geometry
  and the window, neither of which the weights' quantisation changes — and llama.cpp holds the
  cache in f16 regardless (`LlmExtraArgs` defaults to `-ngl,99,--jinja,--no-mmap`, so no `-ctk` /
  `-ctv` reaches the server; the f16 assumption the panel states is correct for this deployment).
  It is not *quite* constant: `UD-IQ2_XXS` and `UD-IQ2_S` declare 64 blocks, so 65,536 MiB. A 1.6%
  difference, invisible on screen.
- **262,144 was never a setting anybody chose.** `adminEngineAdd.tsx` pre-filled the window field
  with `context_length`, which the CP sends with its own warning attached (`engine_admin.go`): *"a
  ceiling, not a setting — the 30B in this deployment publishes 262144 and is run at 32768"*. The
  panel used the ceiling as the setting and priced the cache at it.

At 32,768 the same figures are 8,320 MiB, and the 22,000 MiB card holds `UD-IQ1_S` through
`UD-IQ3_XXS` with room. The screen was saying "impossible" about a model that runs.

The prior art the operator pointed at is Hermes Agent's local-model picker, which labels every
quantisation before download as *fits in VRAM* / *will use system RAM* / *too big for this machine*
— and which admits in its own documentation that the prediction is an estimate that runtime can
contradict. The second of its three states does not exist here: the engine runs `-ngl 99` on a GPU
instance, so there is no host-RAM fallback to spill into. The third has a better answer here than
"too big": this deployment *buys* its box, so the ladder can be named.

## Decision

### Decision 1 — a chat row is filed under its REPOSITORY, not its publisher

ADR 0088 decision 3 filed a GGUF under the first segment of `display_name`. That was wrong at the
wrong level: it put `Qwen3.8-27B` and `Gemma` in one pile because unsloth published both, and split
nothing that needed splitting. The group is now the whole repository id, and the group heading says
how many of its sizes are here.

The rows stay one per quantisation. They cannot be merged: `--models-max 1` holds one at a time and
a member choosing between "fast" and "good" is choosing between two of them, so each needs its own
id, its own window and its own enable.

Inside a repository group a card is titled with its **quantisation** (`UD-IQ2_S`), because the
repository is the heading above it — titling both cards with the repository left two cards of one
model calling themselves the same thing (seen on the rendered screen, not in any test).

### Decision 2 — the ladder of sizes is drawn with a verdict on every line, from one press

Under the group heading, `POST …/ingest/files` lists the repository: every quantisation, the ones
already taken in marked, the rest with a `追加` that opens the ordinary ingest dialog pre-filled.

🔴 **On a press, never on mount.** A catalogue of eight repositories drawn eagerly is eight upstream
listings and eight ranged header reads nobody asked for, at a host that sheds load with a 503
(ADR 0085's search retry). Same rule as ADR 0088 decision 4, same reason.

🔴 **The dialog is the only road in.** The ladder's button fills the FORM — it hands the dialog a
Hugging Face blob URL through its own `initialRef` prop, which is the shape the dialog already
parses. It does not build a request. The plan, the price and the licence are the same screen they
always were, so there is no second way to start an ingest that skipped them.

*(Measured while building: smuggling that URL through `hit.ref` instead sent `revision:
"https://huggingface.co/…"` upstream — `hit.ref` is the HF revision. Hence the explicit prop.)*

### Decision 3 — the verdict is weights + KV against the class, in three states, and it names the next rung

`engineFit.ts` owns the arithmetic for all three callers (the ingest form, the ladder, a row):

| state | when | drawn as |
|---|---|---|
| `fits` | ≤ 85% of the class's VRAM | 収まります |
| `tight` | 85–100% | ぎりぎり (n%) |
| `over` | > 100% | このクラスでは入りません (— and the smallest rung that would hold it, when there is one) |

85% is a reserve, not a measurement: what this counts is the weights and the KV cache, and what it
cannot count is llama.cpp's compute buffers and the CUDA context, which depend on the batch size
and are in no header. ADR 0074 measured what being wrong costs — an L4 took 17 GB of weights and
then died on `cudaMalloc failed: out of memory ... failed to allocate buffer for kv cache`, four
minutes and one purchased GPU after the press. So 99.6% is not called a fit. The estimate says once,
above the table, what it does not include.

Naming the next rung is the part Hermes Agent has no equivalent of and this deployment does: it
buys its own instance, so "too big for this machine" is a decision rather than a fact.

🔴 A missing KV figure leaves the verdict `unknown` **only while the weights still fit**. Once the
weights alone are over the card no cache size can rescue it, and a repository that will not answer a
ranged GET would otherwise hide its largest quantisations behind a shrug.

### Decision 4 — the window is a control on the screen, defaulted to what the box can hold

The field is out of the `詳細設定` fold and sits next to the verdict it decides. It opens at the
largest power of two that leaves the model under the comfortable line on the chosen class, and the
model's published ceiling is drawn beside it, labelled as a ceiling.

Powers of two because that is what model cards are written in and what people type; offering 37,491
would be arithmetically better and read as a machine talking to itself.

Rejected — a fixed 32768. It is the value this deployment runs its 30B at, and it is wrong in both
directions: needlessly small on a 46 GB class, and still impossible for a model that is nearly the
size of the card. Rejected — leaving the field empty. Honest, and it answers none of "so what
should it be", which is the question the screen exists to answer.

On the ladder the window is the one the rows of that repository actually declare, and editing it
re-prices the table and **writes nothing**: a row's window belongs to that row and is edited on it.

### Decision 5 — the listing classifies what is not a model, and still lists it

`engineCandidate.Role` is `model` / `projector` / `imatrix`, read off the file name. The ladder
draws only the models; the manual file picker still shows everything, because hiding a file from
somebody who came looking for it is the older fault this list was built to fix (ADR 0072: a name one
letter short is indistinguishable from a name that is not there).

Shards are not in the vocabulary: `engineIngestWanted` has dropped `-00002-of-00003` since ADR 0072.

Names and not headers, deliberately: thirty ranged reads to classify thirty files would cost more
than the screen is worth, and being wrong folds one row into a group the operator can unfold.

### Decision 6 — one header read prices the whole repository, and says which file it came from

The listing answers `kv_mib_per_1k_tokens` and `kv_from`. Per 1,024 tokens because the cache is
linear in the window, so one number prices every value anybody types and the formula stays in one
place.

🔴 It is a repository-wide estimate and names the file it was read from, because the geometry is not
quite identical across a repository's own builds (64 vs 65 blocks, measured above). The row that is
actually taken in gets its own header read at the resolve, and that is the number the press is
priced from.

### Decision 7 — still the admin catalogue only

ADR 0088 decision 5 stands: `engineCatalogModelRow` is unchanged, so a member choosing a model still
sees ids and no fit verdict. The consequence is recorded again because this ADR makes it sharper —
a deployment can now hold `UD-IQ2_XXS` and `UD-IQ2_S` of one model, and the launch menu shows a
member two ids that differ by four characters with nothing to say which is which.

## What was measured

| claim | how |
|---|---|
| a 27B's KV cache is 66,560 MiB at its published ceiling and 8,320 MiB at 32,768 | the real GGUF headers of four files of `unsloth/Qwen3.8-27B-GGUF`, ranged GET, 2026-09-18 |
| the geometry differs between builds of one repository (64 vs 65 blocks) | the same four reads |
| the repository publishes 30 `.gguf` files, one imatrix and two projectors | `GET /api/models/unsloth/Qwen3.8-27B-GGUF?blobs=true`, 2026-09-18 |
| the deployment passes no `-ctk` / `-ctv`, so the cache really is f16 | `LlmExtraArgs` default in `deploy/aws/ecs/cfn/60-engines.yaml` |
| a 2-bit 27B fits a 22,000 MiB card at 32,768 and a 4-bit one does not | the two above, through `engineFit.ts` |

## Follow-up — from "automatic at registration" to "automatic for the row's whole life" (2026-09-19)

Raised by the operator: **asking someone with no LLM background to type these numbers when they
add a model is not workable.** Whether the model runs on the instance being used, and how much
context is available, should be **decided automatically, shown on screen, applied, and ready to
use**.

That is what this ADR designed. It was not holding in three places.

### 1. The automatic answer was four times off

`kv_mib_per_1k_tokens` IS `engineKVCacheMiB`, and that formula was four times too big for a
hybrid architecture (ADR 0074's 2026-09-18 follow-up). Measured against the CP that is running
right now, resolving the same model this deployment serves: `kv_mib_per_1k_tokens = 260` where
the truth is 64. For the same 27B on the same L4, `windowThatFits` therefore offered
**16,384 instead of 65,536**. **The screen was lying, modestly, by a factor of four.**

### 2. When no window could be fitted, the field fell back to the ceiling

```ts
windowThatFits(...) || found.context_length   // the ceiling, on failure
```

Which is the exact thing this ADR exists to stop being typed in, reached through the error path
instead of the happy one. A row of that shape is still on af-sandbox, and it asked llama.cpp for
16 GiB of KV cache and took the L4 out of memory.

**Zero is not the answer either** — it travels as "undeclared" all the way to opencode, which
reads a context of 0 as "auto-compaction off" (`opencode/engine.go`). The session then runs
until llama-server rejects it, which is worse than a small window rather than safer.

→ `windowWhenUnsized` (32,768, capped by the ceiling), **named as a fallback** by the form.

### 3. A registered row had no verdict at all

The edit dialog was three raw number boxes. **Correcting a window later meant pricing the KV
cache in your own head.**

→ The same `modelFit` verdict the ingest form draws, plus the fitted window as one press. For
that, the CP now sends `kv_mib_per_1k_tokens` on registered rows too.

And **`context_length` was never stored** — read at the resolve, shown beside the field, gone the
moment the row existed. Without a ceiling a re-fit has no upper bound and would propose windows
**the model was never trained for**. Migration 0070 / pg 0055 adds `context_ceiling`, written at
ingest. 🔴 Stored, never APPLIED: what the architecture allows and what fits on the card are
different questions, and only the first is the publisher's to answer.

A row with no stored ceiling is offered **no button**. An offer computed without an upper bound
is worse than no offer.

### What is still missing

- **Existing rows have no geometry** (`vram_need_source: floor`). A backfill that re-reads the
  GGUF header from the bucket's own object is needed. Until then those rows are offered no
  re-fit, and ADR 0074's follow-up guard asks for a confirmation every time.
- **Changing the instance rung does not move the windows.** A window is fitted once, against the
  `class.vram_mib` of the moment. Nothing re-fits the rows when the ladder step changes.

### A, implemented — the geometry is read from the bucket too (2026-09-19)

`engine_gguf.go`'s own header admitted the hole: "a row registered from the bucket still reaches
this reader through no road, so its geometry stays unknown and its VRAM estimate stays the
weights alone". **Every llm row on both deployments was in that state**, which is why they all
answered `vram_need_source: floor` — and weights alone fit almost any card, so a row declaring
262,144 tokens could be switched on without a word.

So the road was built. `engineGGUFGeometryOfObject` takes the first 64 KiB of the object (1 MiB
on a second try) through `engineStorageMetadataPort.Prefix` and hands it to the same
`parseGGUFGeometry`. The same shape as `engineVaeOfObject` next door, and the same promise to
fail quietly.

Two callers:

- **At registration** (`POST …/models`), beside the VAE verdict, on the road that has no
  upstream URL.
- **At enable** (`healGeometry`): one read on a loading write to a row that has no geometry, and
  it is stored. It runs BEFORE the judgement, so a row that heals passes without being asked to
  confirm anything. From the operator's side nothing was added.

`context_length` is read on the same pass (`engineKVGeometry.Ceiling`), because without a ceiling
a re-fit has no upper bound and would propose windows the model was never trained for.

That makes "add it and use it" true for existing rows as well. What is left is B — re-fitting
when the instance rung changes.

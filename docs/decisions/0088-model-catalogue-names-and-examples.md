# 0088. A catalogue of models, not of keys — the publisher's name, one example image, and the family as the category

English | [日本語](0088-model-catalogue-names-and-examples.ja.md)

- Status: **accepted** (2026-09-18). Built in the same change. Every "exists" / "does not exist"
  claim below was checked by grep on `f9feed5b` (develop), and every "measured" claim was read off
  the live upstream APIs the same day.
- Builds on: [0072](0072-engine-model-catalog.md) (the catalogue and its row), decision 2 of which
  is what this ADR must not break — an upstream display name is never the family the provider
  dispatches on; [0085](0085-model-ledger-and-one-press-ingest.md) (the 登録済み tab this changes,
  and the resolve that already reads everything needed here);
  [0081](0081-image-generation-pane.md) decision 5 (the precedent: a fact the wizard showed and
  then threw away, given a column).

## Context

The `登録済み` tab draws one card per catalogue row, and its title is `model.id`
(`adminEngineAdd.tsx:709` before this change). That id is not a name anybody chose — it is derived
from the *file* name at ingest (`engineIDFromFile`, `engine_plan.go:388`), because it is the key
the launch menu, the active set, the S3 layout and every other admin screen are written in. So the
catalogue reads:

```
abyssorangemix2_hard_8832          説明なし
anima-aesthetic-v1.1               説明なし
meinamix_meinav11_5038             説明なし
```

Three checkpoints, and nothing on the screen says which model any of them is, what it draws, or
which of them are alternatives to each other. The family — the one fact that answers the last
question — is a tag in the middle of the card's body, between the precision and the VRAM figure,
where a page of twenty cannot be scanned by it.

What the row holds today: `description` (empty for every row the ingest created — nothing fills
it), `base_model`, `license*`, `source`, `trained_words`, `params`, the files. What it does **not**
hold: a name, and a picture.

Both exist upstream, and this is the part that decides the shape below — **they arrive on a read
the ingest already makes**:

- **Measured live 2026-09-18**, `GET https://civitai.com/api/v1/model-versions/119057` answers
  `model.name` = `"MeinaMix"`, `name` = `"Meina V11"` and ten `images[]` entries carrying `url`,
  `nsfwLevel`, `width`, `height` and `type`. `engineResolveCivitai` (`engine_ingest.go:481`)
  already decodes that exact document for the sha256, the size and the licence — it just does not
  decode those three fields.
- The `探す` tab has drawn the same pictures since ADR 0085, from the search list
  (`engineSearchHit.PreviewURL` / `ThumbURL`), with a rewrite of Civitai's CDN transform segment
  into a card size and a lightbox size (`engineCivitaiImageVariant`, measured 2026-09-15: a page of
  originals is ~40 MB). So the card side is proven; it is the *row* that keeps nothing.
- **Measured live 2026-09-18** on the Hugging Face side: `cardData.thumbnail` is **absent** on all
  four repositories checked, including the two this deployment runs
  (`unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF`, `bartowski/Qwen2.5-7B-Instruct-GGUF`,
  `Qwen/Qwen2.5-7B-Instruct`, `black-forest-labs/FLUX.1-dev`). **There is effectively no picture
  for an LLM row**, and the design must not depend on one.

## Decision

### Decision 1 — the row records the publisher's name; the id is kept and never replaced

Four columns on `engine_models` (migration `0068` / pg `0053`): `display_name`, `version_name`,
`preview_url`, `thumb_url`. Filled at ingest from the resolve, which costs no upstream read of its
own.

Two names and not one, because they are two facts: a model, and the version of it that was taken
in. That pair is exactly what an id derived from a file name cannot express, and it is what tells
two rows of the same model apart.

**The id stays on the card, on its own monospaced line.** It is what every S3 key, the launch menu
and the active set say. A card showing only "AbyssOrangeMix2" would be one an operator cannot match
to the file in front of them — which is the same failure as today's, in the other direction.

Rejected: renaming the row (making `id` the display name). The id is a primary key referenced by
the S3 layout and the published active set; a catalogue that renames rows for readability is one
where the next cold start syncs a key nothing declares.

### Decision 2 — the picture is a URL this deployment records, never bytes it holds

`preview_url` / `thumb_url` point into the publisher's own CDN. Nothing is mirrored into the S3
bucket.

Rejected — copying a thumbnail into the bucket at ingest. It would survive the publisher deleting
the image and would work where the browser cannot reach civitai.com, and it costs: a third kind of
object that `purge`, the ledger (ADR 0085 decision 2) and the object routes would each have to
learn about, plus a fetch inside the ingest path, for ~30 kB of JPEG. The failure it prevents is an
empty box that one press repairs (decision 4); the failure it introduces is in the delete path of a
system whose delete path is already a task nobody can watch the end of.

The consequences are accepted and stated: an image the publisher removes stops rendering, and a
deployment whose *browsers* cannot reach the CDN sees no examples. Neither is new — the `探す` tab
has hotlinked the same host since ADR 0085 — and the `<img>` carries `referrerpolicy="no-referrer"`
so the publisher is not told which deployment is looking.

### Decision 3 — the family is the category, not a tag

The 登録済み list is drawn as sections with the group's name as the heading, and a row of chips
above it narrows to one. What a row is filed under depends on what it *is*:

| row | filed under | why |
|---|---|---|
| image checkpoint or LoRA | its `base_model` (the family) | it is what the provider picks a workflow with, so it decides whether two rows are interchangeable |
| LLM adapter | its `base_model` (the parent model id) | the same question, in that role's vocabulary |
| GGUF | the **publisher** — the owner segment of `display_name` | it declares no family at all, and two people's quantisations of the same weights are genuinely different rows |

🔴 **The group of rows that declare nothing sorts first**, like the ledger's orphans. A row with no
family is not a tidy leftover: the CP refuses to enable it (`engine_files_missing` /
`base_model_missing`), so it is the one group somebody has to act on. Then come the engine's own
declared families in the engine's own order (`row.base_models`), so the sections read the same way
on every deployment; anything else follows alphabetically.

Nothing here writes an upstream name into `base_model`. ADR 0072 decision 2 stands: the family is a
declaration the operator makes, this ADR only draws it in a place it can be read.

### Decision 4 — a row taken in before these columns existed is filled by one explicit press

`POST /api/admin/engines/{key}/models/{id}/meta` re-reads the page the row's `source` names and
writes the four columns. The Console offers it per row (only on a row that has neither a name nor a
picture, so the control retires itself) and once in the toolbar for all of them, which walks the
rows **one at a time**.

Without this the feature would only ever apply to models nobody has taken in yet — every row a
deployment already holds was written before the columns existed.

Rejected — filling them in behind the panel when a row lacking them is listed. It turns opening a
screen into a page of upstream requests nobody asked for, at a site that sheds load with a 503
(measured, ADR 0085's search retry), and the operator cannot see the cost of it.

The route runs the whole existing resolve rather than a narrower read of its own. It costs two or
three requests where one would do; it buys the thing that matters — there is **one** way this
deployment reads a model page, so a name on a row can never come from a document the ingest does
not read.

A row with no upstream is refused with its own code, `engine_no_source`, and the message says which
of the two cases it is: nothing recorded (a seeded row, or one registered from the bucket by hand),
or a `url:` source, which addresses the weights themselves and is not a page.

### Decision 5 — this is the admin catalogue only

The member-facing projection (`engineCatalogModelRow`, the only one the Agent and therefore the
image-generation pane can see) is **not** changed. The pane would be the natural next home for
this — it is where a member asks "which model shall I use" — and it is deliberately not in scope:
putting an example image in front of members means deciding what to do about Civitai's content
rating by default, and about tenants whose network policy forbids the CDN. Those are answers this
ADR does not have.

The consequence is recorded because it is a real one: a member choosing a model still sees ids.

## What was measured

| claim | how |
|---|---|
| a Civitai version document carries the model name, the version name and ten example images | live `GET /api/v1/model-versions/119057`, 2026-09-18 |
| Hugging Face publishes no `cardData.thumbnail` for the repositories this deployment uses | live `GET /api/models/<repo>` on four repositories, 2026-09-18 |
| the example images are 2048x4096 and larger | the same read — hence `object-fit: contain` and a height cap on the card, rather than a cropped box |

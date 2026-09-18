# 0090. An id a member can tell apart, and a name they can read

English | [日本語](0090-member-facing-model-names.ja.md)

- Status: **accepted** (2026-09-18). Built in the same change. Every "exists" / "does not exist"
  claim was checked by grep on `69122d20` (develop, ADR 0089 merged), and the id collision below
  was reproduced by running `engineIDFromFile` against the real file names.
- Closes the consequence both [0088](0088-model-catalogue-names-and-examples.md) decision 5 and
  [0089](0089-quantisation-ladder-and-fit.md) decision 7 recorded and deliberately left open.

## Context

ADR 0088 gave a catalogue row the publisher's name and ADR 0089 made holding several sizes of one
model a single press. Both stopped at the admin screen, and both wrote down the same consequence:
**a member choosing a model still sees ids.**

That consequence got worse the moment ADR 0089 shipped, and in a way neither ADR predicted.
`engineIDFromFile` (`engine_plan.go`) proposed an id from the file name and **stripped the
quantisation**, on a rule the test spelled out: *"the quantisation names the FILE, not the model:
the same model at two quantisations proposes one id."* Run against the real files of
`unsloth/Qwen3.8-27B-GGUF`:

```
Qwen3.8-27B-UD-IQ2_XXS.gguf  -> "qwen3.8-27b-ud"
Qwen3.8-27B-UD-IQ2_S.gguf    -> "qwen3.8-27b-ud"
Qwen3.8-27B-UD-Q2_K_XL.gguf  -> "qwen3.8-27b-ud"
Qwen3.8-27B-UD-IQ4_XS.gguf   -> "qwen3.8-27b-ud"
```

All four propose one id, so `enginePlanID` makes the second unique with a numeric suffix. A
deployment that used ADR 0089's new button twice ends up offering its members
**`qwen3.8-27b-ud` and `qwen3.8-27b-ud-2`** — a choice between two strings where the one fact that
distinguishes them has been deleted and replaced by a counter.

Where a member meets that, exactly two places:

- **opencode's own model picker**, from the `name` the Agent writes into its config
  (`internal/agents/opencode/engine.go`), which was `<id> + " (self-hosted)"`;
- **the image-generation pane's model `<option>`** (`GenerateForm.tsx`), which drew `{m.id}`.

And the row already holds the answer: `display_name` since ADR 0088, plus `source`, which records
the exact upstream file. Nothing needs fetching.

## Decision

### Decision 1 — the proposed id keeps the quantisation

`engineIDFromFile` no longer strips it. The old rule was true while a deployment held one size of
a model; ADR 0089 made several the normal case, and a rule that deletes the distinguishing token
is then exactly backwards.

Existing rows are untouched — an id is stored, and this is only the proposal for the next ingest.
It remains a proposal the person may edit.

Rejected — stripping only when it would not collide. It produces an inconsistent catalogue (the
first size bare, the second carrying its quantisation) out of an invisible rule, which is worse
than a slightly longer id everywhere.

### Decision 2 — the member-facing relay carries a name, and the id stays the key

`engineCatalogModelRow` emits `label`: the publisher's name plus the one part that tells two rows
of the same model apart — the version for a Civitai row, the quantisation for a Hugging Face file.

🔴 **Beside the id, never instead of it.** `id` is what a request names, what opencode keys a model
by, what the gateway routes on and what the active set is written in. `label` is a string to draw
and reaches no request. The Agent-side test asserts both halves, including that no label was
written as a model key.

🔴 **Absent, not empty**, for a row nobody has read a model page for. Every reader falls back to
the id, which is what all of them did before this field existed — so the absence is the previous
behaviour rather than a gap.

### Decision 3 — composed once, in the Control Plane

`engineModelLabel` / `engineModelVariant` live beside `engineSourceURL` and for its reason: this is
the only side that holds the name, the version and the source together, and composing it again in
the Agent (Go) and a third time in the browser (TypeScript) would be three spellings of one name.

The variant is read off `source` and never off the id — the id is the file name lower-cased, and a
row registered by hand never had a file name at all, so un-mangling it would be re-deriving a
transformation `source` already records exactly.

*(The Console keeps its own `quantLabel` for ADR 0089's ladder. That one labels **upstream
candidates**, which are file names with no row and therefore no composed label behind them — a
different input, not a second copy of this rule.)*

### Decision 4 — five hops, tested end to end

The label crosses the CP's row, the catalogue JSON, `engineCatalogModel`, the two per-id maps, and
opencode's `name` / the image status's `label`. A field missing at any hop is silent — the failure
`sessionWire` taught this fleet — so the test drives `syncEngineProviders` and reads the written
config rather than asserting on an intermediate struct.

### Decision 5 — the option shows the name and drops the id

An `<option>` has no room for both. The admin card keeps the id on every card (ADR 0088
decision 1) because an operator has to match a row to a file in a bucket; **a member never sees an
S3 key**, so for them the id buys nothing and costs the whole line. Different rule, stated reason.

### Still out of scope — the example images

ADR 0088 decision 2 stored one, and ADR 0088 decision 5 kept it off the member-facing relay. That
stands: putting a publisher's example image in front of members means deciding what to do by
default about Civitai's content rating and about tenants whose network policy forbids the CDN.
Names raise neither question, which is why they go and the pictures do not.

## What was measured

| claim | how |
|---|---|
| four quantisations of one repository proposed one id | `engineIDFromFile` run against the real file names, 2026-09-18 |
| the second row taken in would be `<name>-2` | `enginePlanID`'s suffix loop, read on `69122d20` |
| opencode's picker showed `<id> (self-hosted)` | `internal/agents/opencode/engine.go` before this change |
| the image pane's option showed the bare id | `GenerateForm.tsx` before this change |

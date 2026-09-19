# 0092. The model catalogue keeps its place, and a row opens the other editions of its own model

English | [日本語](0092-catalogue-memory-and-other-editions.ja.md)

- Status: **accepted** (2026-09-19). Built in the same change.
- Extends [0085](0085-model-ledger-and-one-press-ingest.md) (the catalogue is a pane with two
  faces) and [0089](0089-quantisation-ladder-and-fit.md) (the ladder of sizes). Neither decision
  is revised: what the pane shows and what the ladder prices stay as they were. What changes is
  that the pane survives being left, and that the ladder is reachable from the row rather than
  from a group heading that only some rows have.

## Context

Two complaints about the same screen, reported together.

**The catalogue forgets everything the moment you look at something else.** A tabbed cell renders
the selected view alone (`PaneHost`'s `selectedView`), so switching tabs unmounts `EngineAddView`
outright. The page of hits, the source, the sort, the family, the filter typed into the registered
list and the scroll position all live in React state and go with it. Coming back re-searches — and
a search here is an UPSTREAM request to Civitai or Hugging Face, hosts that shed load with a 503.
The role tabs had the same shape: 文章 ⇄ 画像 threw the page away and asked again.

**There is no way to take in another edition of a model already in the catalogue.** ADR 0089 built
the ladder of a repository's other quantisations, but drew it under a group heading, and a GGUF row
is filed under its `display_name` — so every row nobody has read a model page for sits in the
unnamed group and can never reach it. The image role never had one at all: a checkpoint's other
VERSIONS could only be found by searching for the model again in the 探す tab, and a registered row
records `civitai:<version>` alone — the model id behind it is a different number the Console does
not hold.

## Decision

### Decision 1 — The pane's state lives outside the pane

A module-scope memory (`catalogMemory.ts`), keyed by pane × engine × face × kind: the source, sort,
family, query and the page of hits for 探す; the filter, sort and open family chip for 登録済み; the
bucket listing per engine and the engine list itself. It is lost on a browser reload, which is the
same lifetime — and the same reasoning — as the file viewer's `scrollMemory`.

**Not the layout store.** That is serialised and written on every geometry change; a hundred hits
with preview URLs do not belong in it. The one thing written back to the pane is the open FACE, so
a reload returns to it.

🔴 Only the face. `sameTarget` identifies an engineAdd pane by `engineKey` and `lora`, so writing
the role tab or model⇄lora into the pane's content changes its identity, and the next
`openEngineAdd` opens a second catalogue beside the first.

**The point is not that the page reappears — it is that the return costs nothing.** A restored page
suppresses the search the mount would otherwise fire. Everything else follows from that.

### Decision 2 — Each list keeps its own place, and a new question starts at the top

The scroll position is filed under the memory's key plus the QUESTION the page answers (submitted
query, source, sort, family), through the viewer's existing `useScrollMemory`. Coming back to the
same list returns to the place in it; 文章 and 画像 are different keys, so each returns to its own;
asking a different question opens at the top — set explicitly, because the scroll memory correctly
does nothing when it finds no record and the box would otherwise stay halfway down a list nobody
has read.

🔴 A restored page must not let the end of the list fetch by itself. The restored position lands at
the bottom, the sentinel is on screen before anybody has done anything, and the `IntersectionObserver`
answers by spending the upstream request this whole decision exists to avoid. It is held off until
the reader moves; the manual button is always there.

### Decision 3 — The other editions, from the row, in the row's own vocabulary

One button on the registered card, opening a modal, offered only where the row records a page:

- a **GGUF** row whose source is `hf:<owner>/<repo>/<file>` gets ADR 0089's ladder of the
  repository's other quantisations — now reachable from any such row, including the unnamed ones
  that no group heading could carry. A chat ADAPTER is left out: it is filed under the model it was
  trained against, and the ladder prices a KV cache it does not have;
- an **IMAGE** row whose source is `civitai:<version>` gets the model's other VERSIONS.

Both end in the ordinary plan dialog, so the licence and the price are seen on the one screen that
has always shown them. No second road into the ingest.

🔴 The CP resolves the model from the version. A row records the version id and nothing else, and
`/api/v1/models/<id>` needs the model id — so `POST …/ingest/versions` now accepts a Civitai `ref`
with no `model_ref` and reads the model out of the version document (`engineReadCivitai`, already
the ingest's own reader), answering with the resolved `model_ref` beside the versions. The Console's
old workaround — inventing a list of one version when it had no model id — is deleted rather than
kept as a fallback: the version document is the same one `ingest/files` would need next, so falling
back only moves the failure one press later and hides which upstream refused.

### Decision 4 — Taking another edition in creates a ROW; it never rewrites one

The id is the key the launch menu, the active set and every S3 path are written in (ADR 0088), so a
newer version arrives as a new row and the swap is two presses the card already has — これで起動する
on the new row, then 登録を消す on the old. The modal says so in one line. A transactional
"replace" is deliberately not built: the CP has nothing that would order it, and a failure halfway
leaves a catalogue nobody can start.

### Decision 5 — A part line says its size and its page at its right end, and only an unhappy part carries a chip

On a card narrower than 560px the part line folded into four rows, scattering the size and the link
away from the flag they belong to. The line is now flag / key / meta, with meta held at the right
end and only the key folding underneath.

The `あり` chip is gone from a `present` part. The card's header already counts them
("保存先: 3/3 ファイルあり"), so the chip repeated one fact once per file; 不足, 取り込み失敗,
取り込み中 and 未確認 keep theirs, which makes a chip mean "this line needs a hand". 未確認 in
particular is every line's state when the bucket cannot be listed, and it must stay visible.

## What was measured

| Claim | How |
|---|---|
| A tab switch unmounts the catalogue and the return searches again | Headless Chromium, tabbed cell with two views: without the memory the second `ingest/search` goes out (Expected 1 / Received 2) |
| The restored page comes back with its scroll position | Same run, scrollTop read before and after the round trip |
| A restored page at the bottom makes the sentinel fire at once | `CatalogMore`'s guard is per page SIZE, and a remount resets it |
| The dom suite shares one memory when no pane id is given | 20 existing cases turned red until `clearCatalogMemory()` was added to their `afterEach` |
| A Civitai link handed over without its source is parsed as a Hugging Face repository | `IngestPlanDialog` reads its source type from `hit` / `initialSource` only |
| Civitai's model document carries the version files' sizes | Read from `modelVersions[].files[].sizeKB`; a version the listing leaves out answers 0 and is drawn as "size not reported", never as a fit |

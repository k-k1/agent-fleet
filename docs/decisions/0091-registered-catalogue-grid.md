# 0091. The registered catalogue is a grid of cards, and each card folds on its own width

English | [日本語](0091-registered-catalogue-grid.ja.md)

- Status: **accepted** (2026-09-18). Built in the same change.
- Revises the PRESENTATION of [0088](0088-model-catalogue-names-and-examples.md) decision 2 — the
  example image's place on the card. What that decision settled (a URL, never bytes in the bucket)
  stands untouched; only how wide it is drawn changes, for a reason that did not exist when it was
  written.

## Context

The 登録済み list was one card per row in a single column (`grid-template-columns: minmax(0, 1fr)`).
On the pane an operator actually uses it at — 1,450px on the reported screen — that makes each
catalogue row a 1,450px strip: a title at the far left, five buttons at the far right, and a lake
of nothing between them. Four models fill the screen, and a catalogue of twenty is five screens of
scrolling to compare things that would fit side by side.

The Console already has the answer in the sessions overview pane (ADR 0078, `.ovw-grid`), with its
reasoning written down: `auto-fill` + `minmax` so the column count follows the width, and the card
is **its own container** so that it folds on the width *it* got from the grid — "a 4-column pane
and a 1-column pane hand out very different widths at the same viewport".

## Decision

### Decision 1 — `auto-fill` + `minmax(380px, 1fr)`, like the sessions overview

380px is set by the FOOTER, which is the widest thing a card must hold: up to six buttons
(配布元から名前を取る / 編集 / 有効にする / これで起動する / 揃える / 登録を消す). Below that they
stack and the card stops being one card.

`auto-fill` and not `auto-fit`, for the reason `gallery.css` gives: a family with two models must
keep card-sized cards rather than stretching two of them across the pane.

The footer takes `margin-top: auto`, so a row of cards whose bodies differ in height still has one
line of buttons across it.

### Decision 2 — the card folds on the CARD's width, not the viewport's

The rules that are about a card's internals move from `@media (max-width: 700px)` into
`@container engcard (max-width: 560px)`. A three-column pane at 1,500px hands out 470px cards, and
a viewport rule would never have folded them — the fold and the thing being folded now measure the
same box.

What stays in the viewport query is what is about the PANE: the role tabs, the view tabs, the
operation grids.

### Decision 3 — the example image becomes a thumbnail column, cropped

ADR 0088 gave it `flex: 0 1 50%` and `object-fit: contain`, and argued against cropping: an example
is often 2048x4096, and a portrait cropped to a landscape box loses the half a person judges a
checkpoint by. **That was right for a card as wide as the pane** — half of 1,450px is a real
picture, and letterboxing it costs nothing.

At grid size it inverts. Rendered with a 1:2 swatch in a 555px card, `contain` draws a **110px
strip in the middle of a 555px box** — the width is spent on emptiness and the picture is smaller
than the 探す tab's thumbnail. So the card takes the same answer that tab has used since ADR 0085:
a portrait thumbnail column with `object-fit: cover`, and the lightbox one click away for the whole
image.

`flex: 0 0 clamp(88px, 22%, 132px)` rather than a fold: the thumbnail shrinks with the card instead
of jumping to full width at a threshold, and full width is precisely the worst case for a portrait
example.

🔴 The stub's swatch was changed from 1:1.18 to **1:2** in the same commit. A near-square swatch
made every shot of this card look fine and said nothing about what a real example does to the
layout — the fault this ADR exists to fix was invisible in the harness that was supposed to show
it.

## What was measured

| claim | how |
|---|---|
| a real example is 2048x4096 | Civitai's first `MeinaMix` image, read live 2026-09-18 (ADR 0088) |
| `contain` in a grid-sized card draws a 110px strip | rendered headless at 1,500px, two columns of 555px |
| 380px is what the six-button footer needs | the same render, before and after the floor was chosen |

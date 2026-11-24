# 138. Docs site search: Japanese katakana compounds (2026-11-24)

status: **Measured; recommendation is to stay on Pagefind and add per-page search keywords.** Issue #4061.
The evaluation the issue asks for is this file. Follow-ups: #5064.

## Problem

On the docs site (Pagefind 1.6.0 through Starlight) the query 「ワークツリー」 puts
`ja/user/sessions/worktrees/` at rank 25 of 31. Pagefind segments Japanese into words, so the query becomes
ワーク + ツリー and ワーク prefix-matches ワークスペース, which most pages use. The worktrees page itself says
「ワークツリー」 once, in its lead. The release's search-quality list allows 25 for it today.

## Method

- **Corpus**: the guide at `v0.49.0` built the way the site builds it (`docs/scripts/build.sh` of the site
  repository): 335 pages per language, 670 in the Pagefind index.
- **Queries**: the 53 rows of the site's `research/search-queries.tsv` (35 ja, 18 en): query, language, the page that
  must come back, and the top N it is allowed to be at. The rank is the position of the expected page in the first 40
  results.
- **Baseline**: the site's own `scripts/search_quality.mjs` (Pagefind's browser library over `dist/pagefind`, with the
  ranking Starlight embeds).
- **Other engines** index the same text. It was dumped from the Pagefind index itself (title, heading texts, body), so
  an engine can only differ by tokenizing and ranking, not by what it was given. Every engine got the same field
  weights (title 10, headings 5, body 1), AND semantics between terms and prefix matching where the engine has it. They
  are **untuned**; Pagefind ships with Starlight's tuning. Read the gaps below with that in mind.
- Throwaway scripts, not kept: a page dump through Pagefind's library, one adapter per engine, and a summary.

## Results

`pass` = within the row's own N; `top3` = within 3 for every row (the strict reading, since the issue wants
ワークツリー back at 3); `miss` = not in the first 40.

| Engine (version) | pass /53 | top3 | top1 | miss | MRR | ワークツリー | エージェントメモリ |
|---|---|---|---|---|---|---|---|
| Pagefind 1.6.0, as shipped | 52 | 49 | 39 | 0 | 0.836 | 25 | 5 |
| Pagefind, Starlight's old weights | 50 | 48 | 37 | 0 | 0.804 | 25 | 7 |
| Pagefind, `termSimilarity` 3 | 52 | 49 | 40 | 0 | 0.846 | 22 | 5 |
| Pagefind, `termFrequency` 0.3 | 51 | 49 | 39 | 0 | 0.835 | 26 | 5 |
| Pagefind, `pageLength` 0.25 | 49 | 48 | 38 | 0 | 0.823 | 29 | 5 |
| **Pagefind + hidden weighted keyword on the page** | 52 | 50 | 39 | 0 | 0.845 | **2** | 5 |
| Typesense 28.0, `ja` locale (run locally) | 47 | 46 | 35 | 1 | 0.771 | 6 | 8 |
| Orama 3.1.18, Japanese tokenizer | 44 | 43 | 35 | 6 | 0.740 | 7 | 1 |
| MiniSearch 7.2.0, `Intl.Segmenter` words | 49 | 48 | 37 | 0 | 0.808 | 17 | 1 |
| MiniSearch 7.2.0, character bigrams | 48 | 47 | 36 | 2 | 0.796 | 9 | 1 |

The only row the shipped Pagefind fails is エージェントメモリ (rank 5, wants 3), as the issue's comment on the
v0.45.0 build already said.

### Where the others lose to Pagefind

Ranks (shipped Pagefind / Typesense / Orama / MiniSearch words / MiniSearch bigrams):

| Query | Pagefind | Typesense | Orama | words | bigrams |
|---|---|---|---|---|---|
| 作業ディスク (ja, wants 3) | 2 | 7 | 27 | 10 | 4 |
| セッションの共有 (ja, wants 3) | 1 | 7 | >40 | 1 | 4 |
| マシンの大きさ (ja, wants 3) | 2 | 2 | >40 | 2 | >40 |
| 共有サインイン (ja, wants 3) | 2 | 2 | 11 | 6 | 2 |
| af-aws-exec (ja, wants 3) | 1 | >40 | 1 | 1 | 1 |
| generate an image (en, wants 3) | 1 | 6 | >40 | 2 | 2 |
| child session (en, wants 3) | 1 | 2 | >40 | 1 | 1 |

Not investigated to the bottom, two observations: Orama matches whole tokens only (no prefix matching), which is
the likely reason `child session` and `generate an image` miss pages that say 「sessions」 or 「images」; and
Typesense returned nothing for `af-aws-exec` (probably its handling of hyphens, not checked).

### Why no engine reaches 3 for ワークツリー

Counting occurrences in the 335 Japanese pages: eleven contain 「ワークツリー」. The release-note index has 15,
`memo-auto-run` 12, `memos` 4, a few release notes 2–3, and the worktrees page **one**. Even an engine that treats
the word as a single token (Typesense 6, Orama 7, bigrams 9) ranks pages that say it more often above the page that
answers the question, because none of them has a signal that says "this page is about it". The page's title says
`worktree`, not the katakana.

So the lever is not the tokenizer. Replacing Pagefind buys rank 25 → 6–17 for this query and costs ranks elsewhere
(table above). What puts the page at the top is a signal on the page itself.

### Per-page search keywords in Pagefind

Pagefind weights any element with `data-pagefind-weight`. On a copy of the build, a hidden element holding the term
was added to the worktrees page (and エージェントメモリ to the memory overview), the index rebuilt with
`pagefind --site` (a control rebuild without the element reproduced the baseline ranks exactly):

| Weight of the keyword | ワークツリー | エージェントメモリ | top3 /53 |
|---|---|---|---|
| none (control) | 25 | 5 | 49 |
| 1 | 17 | 5 | 49 |
| 3 | 8 | 5 | 49 |
| 7 | 2 | 5 | 50 |
| 10 | 2 | 5 | 50 |

Nothing else moved at 7 (52 of 53 rows still within their own N), and the page prose is untouched. エージェントメモリ stays at 5:
the other memory pages say it far more often, and a weight of 10 on the overview was not enough. That row needs its
own look (a second keyword, or loosening the row to 5).

## Candidates not measured

Algolia DocSearch (`@astrojs/starlight-docsearch`), the Typesense Cloud variant and turbopuffer need an account, an
API key or a crawler run against a deployed URL, so they could not be run from this workspace. What is known without
running them, from the issue's table:

- They are hosted services: search queries leave the site, and the site stops being static files on its CDN.
  Pagefind and Orama run in the visitor's browser.
- Self-hosted Typesense needs a server next to a site that has none. The local run above is what its `ja` locale
  gives on this corpus.
- Orama needs its own Starlight integration, and its index is one file: 8.3 MB raw / 2.9 MB gzipped for the
  Japanese pages with the Japanese tokenizer. Pagefind's 8.3 MB covers both languages and is split into 128 index
  and 670 fragment files that the browser fetches on demand. MiniSearch's index for the Japanese pages was
  2.3 MB / 0.8 MB (no stored text).

None of these is worth a measurement round until a signal on the page is shown not to be enough: the measured
engines do not beat Pagefind on the 53 queries.

## Decision

Stay on Pagefind. Add a search-keywords field to the pages (hidden, weight 7, emitted by the site's Starlight
conversion from front matter), starting with the pages whose answer word is not in their title: worktrees
(ワークツリー) and the memory overview (エージェントメモリ). Then 「ワークツリー」 goes back to 3 in
`search-queries.tsv` without renaming the page. The change touches the site repository
(`docs/scripts/to_starlight.py` and a Starlight component override) and the guide's front matter
(`docs/CONVENTIONS.md` §3), so it is a separate change.

## Not verified

- The hidden element was added to a copy of `dist/` and the index rebuilt with the Pagefind CLI. It was not built
  through the site's own pipeline, and the weight was not tried together with Starlight's own `pagefind` options.
- Engines other than Pagefind were not tuned; a tuned Typesense or Orama may close part of the gap, but cannot
  avoid the ranking problem above.
- Hosted engines: not run, see above.

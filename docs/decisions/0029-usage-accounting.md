# 0029. Usage accounting — keep one token ledger, broken down by feature

English | [日本語](0029-usage-accounting.ja.md)

- Status: **adopted (P0.5–P4 implemented; P5, the MCP tools, and beyond not started)** (2026-07-26).
- See also: the design and the measurements proper are in [docs/46](../log/46-usage-accounting.md).
  [0016](0016-i18n.md) (Console wording is in both ja and en), [0021](0021-scheduled-execution.md)
  (`source=schedule` — the primary source for this ADR's `origin=schedule`). Numbers 0022 (agent
  memory versioning, on the unmerged `temp/s7in3bh`) and 0027 (the operator↔session interaction
  graph, on the unmerged `temp/sjoad3a`) were taken, so this is 0029.

## Context

The fleet fires LLM calls outside interactive sessions too (assistant chat, handoff summaries,
title suggestions, branch-name suggestions, reply suggestions, the automatic turn on a completion
report, bridge replies). These are **entirely unmeasured today**, and measuring them showed the
intuition "it's haiku, so it's noise" to be wrong (one title suggestion is 16k input tokens and
$0.023 — docs/46 §0). We keep one ledger that lines up what each feature costs on a single scale.

## Decision

### 1. One ledger row = one LLM call (or one folded-in logical turn of a session)

**No content is ever recorded** (token counts and metadata only). This is non-negotiable. Storage
is `~/.local/share/agent-fleet/usage/raw/YYYY-MM-DD.jsonl` (append only, rotated daily).
`~/.local` survives a workspace recreate.

The wire shape of a row (frozen) — the meaning of each field is in docs/46 §2:

```jsonc
{"ts","call","feature","trigger","origin","origin_conv","kind",
 "model","model_raw","model_req","model_src","ref","verb","sidechain","idx",
 "in","out","cread","ccreate","spend","cost_usd","ms","ok","measured"}
```

- **`spend` = in + ccreate + out** (cache_read is not included). The same definition as the
  existing `get_session_usage` and the mirror's ContextBar — two screens not disagreeing comes
  first.
- **`kind` records what actually ran, not what was requested.** `chatProviderFor` and
  `oneShotHeadless` fall back to whichever backend is available, so writing the requested value
  would attribute all of a claude-less workspace's consumption to claude.
- **When one call splits across models, there is one row per model**, tied together by `call` (the
  call ID). The aggregate `calls` counts distinct `call`s and attributes each **once, to the row
  with the largest spend in that call** (ties broken deterministically by ascending raw id).
  Attributing by row order (the spelling of the raw id) makes the main model show 0 calls under
  `by=model`. We do not apportion, because `calls` is frozen as an integer count — non-representative
  rows have spend>0 with calls=0, so averages are shown as `—` (docs/46 §7-5).
- **`measured` distinguishes "zero" from "not measured"** (`exact` | `partial` | `none`). Even a
  CLI that reports no tokens still **always has its call counted**.

### 2. The enums (frozen; the Console does the i18n)

| Dimension | Values |
|---|---|
| `feature` | `assistant.chat` / `assistant.ask` / `assistant.autoturn` / `assistant.bridge` / `compact` / `title.session` / `title.chat` / `branch.suggest` / `suggest.session` / `suggest.chat` / `suggest.edit` / `session` / `unknown` |
| `trigger` | `user` / `auto` / `manual` / `schedule` / `operator` / `bridge` / `recovery` |
| `origin` | `user` / `operator` / `schedule` / `handoff` / `unknown` |
| `model_src` | `reported` / `requested` / `default_unknown` |
| `measured` | `exact` / `partial` / `none` |

`feature=unknown` is in the enum so that **a new auxiliary feature that forgets to tag itself still
always leaves a row**. Not creating unrecorded (i.e. invisible) consumption takes priority over the
tag being right.

### 3. Collection is "a ctx tag plus one recording point in the provider layer"

Usage is already parsed inside the provider implementations, which have both the model and the
tokens. The only thing missing is "what the call was for", so a
`usageTag{feature, trigger, ref, verb}` rides on `context.Context` and **each consumption site
changes by one line in one place**. Recording is concentrated at the parsing point on the provider
side (claude send/sendStream, codex, opencode, cursor, agy, `oneShotHeadless`).

- Recording is pushed onto a `defer` at the top of each provider function, so it runs **exactly
  once on every path**: success, failure and early return. A failure leaves a row with `ok:false` /
  `measured:"none"` (the call is still counted).
- `oneShotHeadless` **records internally rather than widening its return value** (docs/46 §3-a
  proposed returning the kind and usage, but since the recording point is inside the function there
  is no reason to touch the four call sites).

### 4. The model dimension self-reports the granularity available (settled by measurement)

Probes of the real CLIs (2026-07-26, in this workspace):

| kind | Tokens | Model | Cost | `model_src` |
|---|---|---|---|---|
| claude | `usage.{input,output,cache_read_input,cache_creation_input}_tokens` ◎ | `modelUsage`'s **key is the raw id**, with `canonicalModel` in the value ◎ | `total_cost_usd` / `modelUsage[].costUSD` ◎ **measured** | `reported` |
| codex | `turn.completed.usage.{input,cached_input,cache_write_input,output,reasoning_output}_tokens` ◎ | **absent from every event** (including `thread.started`) | none | `requested` / `default_unknown` |
| cursor | `result.usage.{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens}` ◎ | **absent from `result`** | none | `requested` / `default_unknown` |
| opencode | `part.tokens` on `step_finish` ○ | `reported` if `modelID` can be picked up, otherwise degraded | needs measuring | undecided |
| agy | none (plain text output) | — | none | `requested` |

- **Display groups by the `canonicalModel` equivalent, keeping the raw id in `model_raw`** (so a
  series is not broken by a version bump). On claude, `modelUsage`'s key is the raw id including the
  version and the value's `canonicalModel` is the canonical name.
- **`model_req` is kept separately.** A discrepancy between requested and reported (a fallback under
  load, a change in what an alias resolves to, a misconfiguration) shows up as the difference
  between two columns.
- opencode could not be verified live, as this workspace is not logged in
  (`opencode auth list` = 0 credentials). **The implementation picks up `modelID` and degrades to
  `requested`/`default_unknown` if it cannot** (we do not fix a schema by guesswork).

### 5. Sessions themselves are folded in from the transcript delta (a watermark)

Session consumption is emitted by a separate process (the CLI), so we read the transcript and fold
it into the ledger.

- **The logical turn's ordinal is used as idx.** Transcripts are append-only, so it is stable
  regardless of kind, and `(session, idx)` is an idempotency key. Each kind's `Turn.Idx` (a line
  number) is not used, because the numbering differs per kind and would mix up the watermark.
- **The open trailing group is not folded in.** If events are added to the same logical turn after
  folding, the input snapshot would be counted twice. It is settled when the next user turn arrives
  and closes it, or **when the session is deleted or archived** (`includeTrailing`).
- The trigger is **fold-on-read** (piggybacking on `GET /sessions/usage`, throttled to 60 seconds)
  plus **fold-on-delete**. **No new resident timer** (a memory-constrained host; the lesson of
  docs/26).
- **The initial backfill is automatic**: it runs from watermark 0, so the first pass after
  introduction takes in every past turn. Auxiliary calls have no records and cannot be recovered,
  so those start from the day of introduction.
- No double counting: only **registered sessions (`session.Meta`)** are folded in; assistant
  conversations (which write transcripts into `~/.claude/projects`) are not.
- **Idempotency is doubled up** (following on from review P1, 2026-07-26). The watermark is the
  writer's guarantee, but appending the rows (`raw/*.jsonl`) and the watermark (`state.json`) are
  separate files and cannot be written atomically — a crash in between re-appends. **The aggregation
  side drops duplicate `(ref, idx)`** (docs/46 §7-4). It keeps just one entry per ref, "the highest
  idx counted plus the highest ts observed", not a set (idx is appended monotonically from 1, so a
  duplicate always appears as a re-append at the tail). Looking at ts as well is insurance against
  slug reuse, and **when in doubt the tie goes to keeping the duplicate** — a duplicate is visible in
  the raw data, but consumption that was dropped never comes back. A rollup that has already been
  folded is additive and cannot be subtracted, so **the version is bumped and it is rebuilt from
  raw** (and not rebuilt if the contributing raw has been pruned).

### 6. A session's origin (`origin`) lives in `session.Meta`

A different axis from `trigger` (where the turn was injected from). Consumption means something
different for "a session I opened myself" and "a session the operator stood up on its own" — the
latter, combined with autonomous running and scheduled execution, **grows unattended**.

- `Meta.Origin` / `Meta.OriginConv` are added. **Console = `user` (the default) / MCP
  `create_session` = `operator` plus the creating conversation's slug / a schedule = `schedule` /
  handoff = `handoff`.** A recreate inherits the original origin, and **existing sessions without
  the field are `unknown`** (holding the line that it is neither `0` nor `user`).
- **Schedules are derived without touching the CP**: the CP scheduler already puts
  `source=schedule` / `schedule-manual` on the create (docs/38), so it is resolved from that on the
  server side. No new wire item is added to the CP.
- **It is baked into auxiliary calls too**: a title suggestion or a reply suggestion for a session
  resolves that session's origin from `ref` and **bakes it into the row** (so the aggregate does not
  break after the session is deleted — the same thinking as the other dimensions). Conversation-scoped
  features (`assistant.*`) have an empty origin, because the origin axis belongs to sessions.

### 7. The open questions in §9 → decided (all as recommended)

1. **Cost**: `cost_usd` is recorded only where claude actually measures it. In the UI it stays a
   **secondary display, explicitly labelled "API-equivalent amount (measured for claude only)"**
   (making $ the headline under a flat subscription invites misreading). The primary metric is
   `spend`.
2. **Include sessions themselves**: yes (`feature=session`). Use the feature filter to see only the
   auxiliaries.
3. **Retention**: raw 90 days (`AF_USAGE_RETENTION_DAYS`); rollups indefinitely.
4. **Cross-CP aggregation**: v1 stays inside the workspace. P6 (optional) sends aggregate values
   only to the CP.
5. The UI is a new tab in the settings modal (settled in docs/46 §5; `features/usage/UsageView.tsx`
   is written independently of the modal, leaving room to promote it to a pane).

### 8. The stages, and the contract settled by the rollup

The rollup (`usage/rollup/YYYY-MM.json`) and `/usage/series` **went in together in P3** (we do not
build an aggregate before there is a reader). What P3 froze:

- **Buckets are cut by the row's `ts` (when the consumption happened), not the file date it was
  appended to.** Session folding takes in past transcripts after the fact, so cutting by file date
  would pile all past consumption onto "the day of introduction". The first hole we fell into on
  real hardware (docs/46 §7-3).
- There is exactly one invariant against double counting: **each raw file date is either folded or
  unfolded, never both.** Today's is always on the unfolded side. In addition, each folded
  consumption day keeps its contributing file dates (`src`), so a crash and a retry do not add
  twice.
- **`ref` appears in neither the rollup's keys nor the response.** It would grow without bound and
  break the "it is small" premise, and it also serves privacy (an aggregation API does not emit
  individual names). Per-ref data exists only within raw's retention period. The exception is the
  internal index for `(ref, idx)` deduplication (`rollup/state.json`), where **ref is the first half
  of a SHA-256 and is not kept in plaintext** — deduplication needs nothing but equality. It appears
  in neither the aggregate entries nor the response.
- **`bucket=hour` is only within raw's retention period.** For a period that cannot be
  reconstructed, say so with `truncated: true` (silently returning a short series looks like "there
  was no consumption"). For the same reason, **buckets with no consumption are returned filled with
  zero** — dropping them turns distant days into adjacent bars and erases the gap from the picture.
- **The state does not advance if even one month file fails to write.** We do not create "folded but
  no aggregate" (irrecoverable once raw is pruned). The folding side holds `usageMu` and confirms
  "nothing more will be appended for that day" before reading — with a separate lock, an append
  just before the UTC day boundary would silently vanish.
- `coverage` is **generated from the data** (a hand-written table drifts).

### 9. The Console's colours are a validated palette plus a fixed slot order (P4)

Chart colour is treated as something to validate, not a preference (the six checks in the dataviz
skill). The frozen rules:

- **Categorical colours are `--viz-1..8` from `tokens.css`** (validated separately for light and
  dark). `--viz-other` is the grey "other" and is never assigned to a real entity. **Do not tweak a
  single slot by hand** — if you change it, revalidate the whole set.
- **Colour attaches to the entity, not the rank.** Enumerable axes use a fixed table; axes that can
  grow without bound (model, origin_conv) are decided by **a hash of the key name**. Filtering out
  series does not move the colours of the survivors.
- **Drawing is always in slot order.** In a stacked chart only adjacent slots ever touch, so
  validating adjacent pairs is exactly a guarantee about real adjacency. **Beyond eight, fold into
  "other"** (never invent a ninth colour).
- **kind keeps `--kind-*` and is not repainted** (the single-source rule from agent-display-naming
  wins). Instead **the stacking order is fixed** to pass the adjacency gate (docs/46 §5-a). Bands of
  saturation and lightness do not pass, so **legend labels, tooltips and the table view** are
  permanent relief (never make colour the only channel).
- The UI lives in `features/usage/UsageView.tsx` **independently of the modal**, with the settings
  tab as a thin wrapper. Room to promote it to a pane is preserved structurally (the reverse is not
  possible).

## Results

- "Which feature, which agent, which model is eating what" lines up on one scale. The first hole it
  should expose is docs/46 §2-b's **default model problem** (with
  `AF_TITLE_MODEL_{CODEX,OPENCODE,CURSOR}` unset, auxiliary calls run on the CLI default, i.e.
  normally the flagship).
- The measurement itself costs nothing — it only parses existing CLI output and makes no additional
  LLM calls.
- **Limits**: a subscription allowance cannot be back-calculated from tokens (the allowance's
  canonical source is the usage chip, i.e. the statusline `rate_limits`). rtk's savings are a
  different axis (the ledger measures actual consumption *after* rtk is applied). copilot has only
  `outTok`, and kiro/cursor/agy have no tokens in the transcript — reported honestly via `measured`.
- **Privacy**: no content is recorded. The ledger stays inside the workspace.

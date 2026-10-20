# 135. Background memory review — memory index size against the P1 cap (#2333)

status: **Measured (2026-10-19), one item of #2333.** ADR 0117 decision 2 and P1 (#2320) assumed an
index budget of 12,000 characters (`memreview.IndexChars`) and 2,000 characters per existing body
(`memreview.BodyChars`, `MaxBodies` = 5), marked in `limits.go` as "P1 assumptions to be revisited
from real traffic". This journal measures the index half on the memory of one real workspace. The
body half and the other #2333 items are not covered (section 4).

## 1. Method

- Source: the store behind Settings > Agent memory, read through the member list route
  (`GET api/agents/memory/entries/list`, read-only, no use counted): 636 entries, 3 in user scope
  and 633 in project scope across 8 projects.
- Each entry was rendered as `agentMemIndexLine` renders it (`- [scope] name — description (type;
  date)\n`), which is the text `BuildInput` budgets (`memreview/input.go`). A session sees the user
  scope plus its own project, so the totals below are user scope (541 characters) plus one project.
- "Shown" is a greedy fit of those lines into 12,000 characters in list order, skipping a line
  that no longer fits, as `BuildInput` does.

## 2. Result

| project (own entries) | index characters | shown of total |
|---|---|---|
| largest (500) | 77,578 | 73 / 503 |
| next (53) | 7,205 | 56 / 56 |
| next (38) | 7,334 | 41 / 41 |
| next (37) | 5,035 | 40 / 40 |
| four projects with 1–2 entries | 651–1,083 | all |

Line length: median 140 characters, p90 209, maximum 1,855.

## 3. What it means

- Seven of the eight projects fit the 12,000-character cap with room to spare.
- The one heavily used project is **6.5 times over** the cap: about 14 % of its entries reach the
  helper. The other 86 % are invisible to it, so a duplicate of a hidden memory is not detected
  from the index. A `create` cannot collide with a hidden name, because `Known` still carries every
  revision (`BuildInput`).
- Raising the cap does not fit this project either: 77.6 k characters is nearly twice the
  40,000-character conversation window (`WindowChars`).

Not decided here: whether to rank the index (relevance to the window, as `ReviewRank` does for
bodies), clip descriptions, or raise the cap. That is a design choice for the owner; a fix belongs
in its own issue once chosen.

## 4. Limits and what is not measured

- One workspace, one dominant project; the share that fits is specific to it.
- The list route does not return `kinds`, so the `for <kinds>` tag is missing from the rendered
  lines and the totals are slightly low. The order is the list order, not `memory_index` order
  (pinned, guidance types, most used, newest), so *which* entries are shown differs; the count
  does not change materially.
- Existing-memory body sizes were not measured (reading 636 bodies was not worth the cost for a
  cap that only applies to five of them).
- The remaining #2333 items (transcripts of other kinds, helper quality, effort control, plan
  quota, split workload) need live helper runs and are untouched.

# 0130. Cultivated memory: retire the session-window review, grow knowledge by invalidation, feedback and delta patches

English | [日本語](0130-cultivated-memory.ja.md)

- Status: **draft for the owner's review** (2026-11-06). Nothing is built. Every `file:line` was read on
  `58501ccb0`. The store figures were measured read-only on one development workspace on 2026-11-06
  (section "Measured"); the Hermes Agent figures come from its source at `90d6b98` (2026-11-05); the
  prior-art survey is section "Prior art" and is marked where it was not verified.
- Decided by the owner on 2026-11-06: ADR 0117's review is **removed in the next release**, and the next
  release carries **the first step** of what this record calls cultivation. Everything else here is a
  proposal.
- Related: [0022](0022-agent-memory-management.md) (history, bundles) /
  [0108](0108-af-owned-agent-memory.md) (the store, tools, ranking, pins) /
  [0117](0117-background-memory-skill-review.md) (the review this record retires) /
  [docs/log/134](../log/134-background-review-p0-measurement.md), [135](../log/135-background-review-index-size.md), [136](../log/136-cultivated-memory-survey.md) (measurements and prior-art survey)
- Supersedes: 0117 decisions 1–3 (trigger, input, helper turn) and the member-facing parts of 4 and 6.
  0117's run record, deny-all backend contract, `review` author, run id and "revert this run" are kept
  for reuse (decision 7).

## Context

0108 gave every agent kind one store and five tools. Agents write to it: over the four weeks to
2026-11-06, about 5 creates a day and 1 forget in about 200 writes. Nothing makes the store better as
it grows; it only gets bigger, and the world it describes moves under it.

### Measured (2026-11-06, read-only)

One project plus the user scope, 568 memories, 1.72 MB of bodies. Bodies were read through the
Console's member routes, which count no use (`agent_memory_member.go`, search "counts no use");
a sweep through `memory_read` would have added a use to every memory
(`agent_memory.go:803`, `agent_memory_search.go:102`) and skewed the ranking it was judging.

| Check, no LLM | Memories | Note |
|---|---|---|
| `[[link]]` to a memory that does not exist | 7 | 9 of 1,669 links |
| body > 4,096 bytes (`agent_memory.go:79`; recall shows only the head) | 100 | 33 > 8 KiB, largest 45 KB |
| retired terms (retired AWS profiles, the old trunk branch) | 48 | of 78 hit lines: 13 already say "retired", 18 are commands |
| repo path, `Test*` name or ADR number that does not exist | 63 | 12-sample spot check: about half real moves/deletions |
| near-duplicate bodies (5-gram containment ≥ 0.4) | 8 | 3 pairs; literal duplication is rare |
| any of the above, plus log-shaped and cold | 219 (39 %) | 1.02 MB |
| facts changed (links, retired terms, paths) | 111 (20 %) | the hit lines ±1 are 0.11 MB, 6 % of the store |

The signals a curator would want are not there:

- `updated` is the import date for 448 memories (79 %); 522 are at revision 1. Age says nothing.
- `uses` counts returned search hits as well as reads, and rises with body size (median 3 in the
  smallest quartile, 9 in the largest). The most "used" memory is the largest one.
- No memory is pinned. "Rarely used but must be kept" has no representation.
- A `feedback` memory, ranked first in `memory_index`, still names the retired trunk branch.

### Why 0117's review did not work

Its mode is `off`; 25 proposals from 2026-10-19..20 (13 runs, 5 sessions) sat in the queue
unanswered for 17 days; 1 of the last 300 published changes is authored `review`. All 25 are
`create`; 11 have an existing memory with a near-identical name; one says in its own body that it
contradicts `develop-trunk-ci` and is still a `create`. The causes are structural:

1. The helper may only update a memory it was shown (`memreview/prompt.go:18`) and is shown at most
   five, chosen by keyword overlap (`memreview/limits.go:20`). A near-duplicate outside the five can
   only be answered with `create`.
2. `update` replaces the whole body (`prompt.go:26`). Rewriting a long memory is expensive and
   risky, so creating wins.
3. The input is one session window (`loop.go:39`, 30 turns). Staleness caused by a change in the
   world, and duplication across sessions, are invisible from inside one window.
4. Every proposal costs one human decision. 0117's Consequences named the queue nobody reads; that
   is what happened.
5. Agents already save in the turn. The review mostly re-proposed the same moments, smaller.

## Prior art

Surveyed 2026-11-06; the long form, with sources and read/secondary/inferred marks, is
[docs/log/136](../log/136-cultivated-memory-survey.md). What each one contributes here:

- **ACE (Agentic Context Engineering, ICLR'26)** — the evidence against full rewrites: a monolithic
  rewrite collapsed an 18,282-token playbook to 122 tokens and fell below the no-adaptation
  baseline (66.7 → 57.1 vs 63.7). Entries carry helpful/harmful counters; deltas are merged by
  non-LLM logic. Its caveat: without ground-truth signals a weak reflector pollutes the playbook.
- **Mem0 v2** went ADD-only (no update, no delete) because overwrites lost information; an
  independent study then measured a 30–90 point drop in deletion precision. Both extremes fail;
  supersede-with-history plus local patches is the middle.
- **Zep / Graphiti** — bi-temporal facts (`valid_at`/`invalid_at`, `created_at`/`expired_at`); a
  contradicted fact is closed, not deleted. Several LLM calls per write and a graph database.
- **GitHub Copilot Memory** — the closest analogue: repository facts carry code citations and are
  validated against the current branch just before use (an LLM judgment); expiry (default 90 days)
  is renewed **only by a validated use, not by retrieval**.
- **"Revoked but Still Authoritative" (arXiv 2609.08258)** — none of five memory systems excluded a
  revoked fact from retrieval by default; revoked facts often outranked their replacement.
- **Codex memories** — per-session extraction after idle, then one locked consolidation agent
  writing a bounded diff. Its open bugs (#48679 truncated summary, #57091 links to missing paths)
  show consolidator output needs deterministic validation before it is stored.
- **fiberplane/drift** (MIT) — deterministic doc→code anchors with AST fingerprints in a lockfile,
  checked in CI for the files a diff touches. Prior art for anchors, for documentation only.
- **Letta sleep-time agents, LangMem background reflection, Claude Code auto memory and
  `/doctor prompt-audit`, Supermemory versions, Basic Memory (Markdown + MCP, AGPL-3.0),
  agentskills.io SKILL.md** — the same pieces in other shapes; Cursor removed its memories in favour
  of rules, which says durable knowledge drifts toward reviewed text.

Not found anywhere: a no-LLM lint that combines code anchors, a retired-term list and links for
agent memory; revision-checked delta patches with several concurrent writers; per-kind lifetimes;
a portable memory-entry file spec. Those are what this record would add; the rest is adopted.

Hermes Agent, read at `90d6b98`: a hard character cap on the always-loaded memory (2,200 chars)
that forces "consolidate + add" in one batch on overflow; `patch` by substring; a forked reviewer
every 10 user turns whose additions apply and whose patches/removals are staged for a human;
skills with usage sidecars and an idle-time curator (active → stale 14 d → archived 30 d, never
deleted). Its own issues: a prompt that pushes the reviewer to write every pass, pruning by direct
use that broke indirectly referenced skills, an unattended run that deleted memory, and full
conversation replay per review (up to 600 k tokens).

## Decision

### 1. What "growing" means

The store grows when it becomes more **accurate** (a stale entry is caught and fixed soon after the
world changes), **denser** (one lesson is one rule; the count may fall), **more abstract**
(incidents become rules and verified procedures; the incident becomes history) and **better
placed** (the right entry reaches the head of the session that needs it, and what is not needed
falls out of view). Count of entries is not a goal. Each is measured (decision 9).

### 2. Remove 0117's review in the next release

- The `memoryReview` setting, the "Review now" item in the session menu, the proposals list in
  Settings → Agent memory, the review switch, the notification, and the loop are removed.
  The Agent ignores a stored `memoryReview` value.
- The 25 pending proposals are not deleted: they are exported once to a file under the store's
  work directory and become the first input of decision 5 when it exists. Rejected hashes are
  dropped.
- The `memory.review` ledger value stays in the enum (0029 is append-only and old rows carry it).
- Kept as internal code, not member-facing: the run record and recovery, the deny-all backend
  contract, the `review` author kind and run-id trailer, "revert this run". Decision 7 reuses them.
- 0117 gets a dated note "superseded by 0130 for decisions 1–3".

### 3. Knowledge carries its kind, status and anchors

Frontmatter gains three optional fields; a memory without them behaves as today.

- `kind`: `preference` | `lesson` | `procedure` | `state` | `log` — how it goes stale, beside 0108's
  `type` (who it is about). Proposed lifetimes: preference and lesson do not expire; procedure is
  checked when its anchors move; state expires after N days unless re-verified; log is history, is
  never shown in the described part of the index, and keeps a summary plus a pointer.
- `status`: `active` | `suspect` | `quiet` | `archived`. `suspect` is shown with the memory to the
  reader ("may be stale: <why>"); `quiet` leaves the described index but stays searchable;
  `archived` leaves search too. Nothing is deleted by this; `forget` stays as it is.
- `anchors`: what the memory depends on — repository paths, symbols, test names, ADR numbers,
  profile and branch names, commands, `[[links]]`. Extracted mechanically on save and editable.
- `superseded_by` / `invalid_at` (Graphiti's vocabulary): set when a newer memory replaces this one.

`archived` and superseded memories are excluded by the read paths themselves (`memory_index`,
`memory_search`, the guidance block), with a test per path, not by asking the reader to filter: the
revocation study above found that no system does this by default.

Runtime counters move into the existing `.usage` sidecar with separate fields: `reads`, `hits`,
`confirmed`, `stale_reports`, `last_read`, `verified_at`. They are not published, like today.

### 4. Cheap writes: patch and feedback (tools)

- `memory_patch(name, scope, revision, edits: [{old, new}])`: substring replacement with the
  revision check of 0108 decision 4; each `old` must match exactly once. Same scan, same commit,
  same author rules as `memory_save`. Fixing one line no longer re-sends 8 KB.
- `memory_feedback(name, verdict: confirmed|stale|wrong, note)`: the reader says what happened when
  it relied on a memory. `confirmed` sets `verified_at`; `stale`/`wrong` sets `suspect` with the note.
  The guidance asks only for the negative case; a read with no report is not counted as
  confirmation. As in Copilot Memory, only a confirmation renews a memory; being retrieved does not.
- `memory_save` of a new name returns the closest existing names when they are near (the name and
  description ranking of `memory_search`), with "prefer memory_patch on X". It does not refuse.

### 5. Invalidation without an LLM: lint

A deterministic pass compares each memory's anchors with the world and marks hits `suspect` with a
reason. Inputs: the repository at the project's default branch (paths, symbols, tests, ADRs; a git
rename is a deterministic fix and is proposed as a mechanical patch), a member-maintained list of
retired terms per project, missing `[[link]]` targets, body size, and the expiry of `state`. It
runs on demand, after a pull that moves the default branch, and when the retired-term list changes.
It writes only `status` and the reason (no body change), each as one published change authored
`lint`, so the change list and revert cover it. Output is also a read-only report.

### 6. Consolidation (later step): inbox → topic → delta patches

Out of scope for the next release; recorded so the first step does not block it.
Observations (the 25 exported proposals; `stale`/`wrong` feedback notes; later an `observe` tool)
are grouped by topic cluster with the `suspect` memories of that cluster. A small model classifies
each observation against the cluster as add / patch / supersede / no-op from snippets; a stronger
model writes **delta patches**, never a full rewrite. Same lesson twice becomes one rule; narrow
cases fold into an umbrella memory. Approval is per topic per week, not per item. Reuses 0117's
plumbing (decision 2). The model's output is validated deterministically before it is queued —
size, `[[links]]` and paths that exist, frontmatter schema, the patch applies — because the Codex
consolidator's bugs are exactly those. The judge is not made too weak: ACE reports that a weak
reflector degrades the result, so anchors and feedback carry as much of the judgment as possible.

### 7. What applies by itself and what waits

Applies by itself: lint's status marks, mechanical renames with git evidence, counter updates,
feedback's `suspect`. Waits for the member, grouped by topic: any change of a claim, supersede,
archive, promotion of a lesson to the brief or of a procedure to a skill. Nothing is deleted
automatically; history keeps everything (0022).

### 8. Independence from AF

The format (decision 3) is written as a versioned spec. The engine — lint, patch, feedback
bookkeeping, later consolidation — has no import of AF packages and takes the model call as a
plug-in (a one-shot CLI command or an API). AF is one adapter (scopes, scheduler, ledger, Console
approval, auth); an MCP server mode is another. **Where the engine lives is open question 1.**

### 9. Measures

Per project, shown on the lint report: entries flagged by lint and how long they stay flagged;
`stale`/`wrong` reports per 100 reads; share of active entries verified in the last 30 days; bytes
of the described index; member decisions per week; tokens per week spent on memory upkeep.

### 10. The next release (step 1)

1. Decision 2 (removal of 0117's review, export of the pending queue).
2. Decision 3's counters split (`reads` / `hits` / `last_read`) — the ranking switches from
   `uses` to `reads` once a release of data exists.
3. `memory_patch` (decision 4).
4. Lint as a read-only report in Settings → Agent memory and `af-memory lint`, with the retired-term
   list (decision 5) — **report only**; writing `suspect` comes in step 2.
5. Guidance text: "patch rather than save when a memory exists", "report a memory that misled you"
   (the latter only once `memory_feedback` lands).

Step 2: `kind`/`status`/`anchors`, `memory_feedback`, lint writes `suspect`, `suspect` shown on read.
Step 3: consolidation (decision 6). Step 4: brief and skills promotion. Each step ships on its own.

## Rejected

- **Keep 0117 and widen its input** (more bodies, the whole index): the trigger is still one session
  window, the output still a full rewrite, and the cost per run grows with the store.
- **An LLM pass over the whole store on a schedule**: 570 k tokens per sweep for this store, most of
  it on memories nothing has changed; lint narrows it to 6 % of the bytes.
- **Decay by inactivity alone** (forget what is not read): `uses` is not a usage signal yet, rare
  but critical procedures are exactly the unread ones, and Hermes' curator broke indirectly
  referenced skills this way.
- **Auto-forget**: history keeps everything, but a forgotten memory silently leaves every session's
  view; the member decides.
- **Adopt an existing engine.** Basic Memory is closest (Markdown is the truth, MCP) but has no
  lifecycle, anchors, revisions or feedback, and AGPL-3.0 constrains embedding; Graphiti needs a
  graph database and several LLM calls per write; Mem0 is ADD-only and vector-bound; Letta's memory
  belongs to its harness. Each would drop most of decisions 3–5 and 8. Their semantics are adopted
  instead (Prior art).
- **Hard cap on the whole store**: the store holds history-shaped entries legitimately; the cap
  belongs to the always-loaded brief (step 4), as in Hermes.

## Consequences

- The member loses a queue they did not use and gets a report they can act on in bulk.
- Agents get two cheaper tools; the guidance grows by two lines.
- Frontmatter grows by three optional fields; claude export and imports must carry or drop them.
- Lint needs the project's default branch checked out somewhere it can read; a project without a
  repository gets only the link, size, retired-term and expiry checks.

## Open questions for the owner

1. **Where the engine lives.** (a) A separate Go module in this repository with an import ban on AF
   packages, split out with history (`git subtree split`) once a second consumer exists; (b) a new
   repository from the start, AF pinning it like its other tools. Trade-offs are in the cover note.
2. Whether step 1 includes `memory_feedback` (cheap, but needs `status` to mean anything).
3. The retired-term list: member-edited only, or also proposed from a commit/ADR that retires
   something.
4. The approval unit for step 3: per topic per week, or a single weekly digest.
5. The model and monthly token budget for step 3.

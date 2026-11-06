# 0130. Cultivated memory: retire the session-window review, grow knowledge by invalidation, feedback and delta patches

English | [日本語](0130-cultivated-memory.ja.md)

- Status: **draft for the owner's review** (2026-11-06). Nothing is built. Every `file:line` was read on
  `58501ccb0`. The store figures were measured read-only on one development workspace on 2026-11-06
  (section "Measured"); the Hermes Agent figures come from its source at `90d6b98` (2026-11-05); the
  prior-art survey is [docs/log/136](../log/136-cultivated-memory-survey.md), which marks what was read,
  what is secondary and what is inferred. Reviewed by a separate session (codex `gpt-6.1-sol`); round 1
  had 21 findings, round 2 had 9, round 3 had 3 and a confirming round 4 had 2 minor ones; each was
  checked against the tree and the cited sources and folded into this text.
- Decided by the owner on 2026-11-06: ADR 0117's review is **removed in the next release**, and the next
  release carries **the first step** of what this record calls cultivation (decision 10). Everything
  else here is a proposal.
- Follow-ups: issues are filed per step when this record is accepted (`docs/CONVENTIONS.md` §10).
- Related: [0022](0022-agent-memory-management.md) (history, bundles) /
  [0108](0108-af-owned-agent-memory.md) (the store, tools, ranking, pins) /
  [0117](0117-background-memory-skill-review.md) (the review this record retires) /
  [0029](0029-usage-accounting.md) (the ledger enum) /
  [0042](0042-user-instructions.md) (user instructions, which agents never write) /
  [docs/log/134](../log/134-background-review-p0-measurement.md), [135](../log/135-background-review-index-size.md), [136](../log/136-cultivated-memory-survey.md)
- What this record changes in earlier records, per decision (each gets a dated note when this record is
  accepted, and only then):

  | Earlier decision | Here |
  |---|---|
  | 0117 decisions 1, 2, 3 (trigger, input, helper turn) | superseded: removed (decision 2) |
  | 0117 decision 4 (the model proposes, AF applies) | kept as the rule for the consolidator (decision 6) |
  | 0117 decision 5 (history, run revert, rejected hashes, no revival) | kept; run revert moves to the change list, rejected hashes travel with the exported queue (decision 2), no-revival binds the consolidator (decision 6) |
  | 0117 decision 6 (`off`/`propose`/`auto`) | superseded: the setting is removed; decision 7 replaces it |
  | 0117 decisions 7–9 (skills, failure modes, phases) | superseded; skills are a later step (decision 10) |
  | 0108 decision 3 (format) | amended: new optional frontmatter (decision 3) |
  | 0108 decision 4 (tools) | amended: `memory_patch`, later `memory_feedback` (decision 4) |
  | 0108 decision 5 and the 2026-10-08 note (ranking by `uses`) | amended: split counters, read-side visibility (decision 3) |
  | 0108 decision 8 (agents publish at once, reviewed after) | unchanged for agents (decision 7) |

## Context

0108 gave every agent kind one store and five tools. Agents write to it: of the last 300 published
changes (2026-10-10 to 11-06), agents made 122 creates, 82 updates and 1 forget. Nothing makes the store
better as it grows; it only gets bigger, and the world it describes moves under it.

### Measured (2026-11-06, read-only)

One project plus the user scope, 568 memories, 1.72 MB of bodies. Bodies were read through the
Console's member routes, which count no use (`agent_memory_member.go:6`); a sweep through
`memory_read` would have added a use to every memory (`agent_memory.go:803`,
`agent_memory_search.go:102`) and skewed the ranking it was judging.

| Check, no LLM (measured with a throwaway script) | Memories | Note |
|---|---|---|
| `[[link]]` to a memory that does not exist | 7 | 9 of 1,669 links |
| body > 4,096 bytes (`agent_memory.go:79`; recall shows only the head) | 100 | 33 > 8 KiB, largest 45 KB |
| retired terms (retired AWS profiles, the old trunk branch) | 48 | of 78 hit lines: 13 already say "retired", 18 are commands |
| repo path, `Test*` name or ADR number that does not exist | 63 | 12-sample spot check: about half are real moves or deletions |
| near-duplicate bodies (5-gram containment ≥ 0.4) | 8 | 3 pairs; literal duplication is rare |
| any of the above, plus log-shaped and cold | 219 (39 %) | 1.02 MB |
| facts changed (links, retired terms, paths) | 111 (20 %) | the hit lines ±1 plus descriptions are 0.11 MB |

The last row's 0.11 MB is the size of the extracted snippets, 6 % of the store's body bytes. It is not
the input of any model call: a consolidation prompt also carries instructions, the topic and whole
bodies to patch, so the real reduction is something step 3 has to measure (decision 9). For scale only:
reading every body once is roughly 570 k tokens **estimated** from bytes at about 3 bytes per token for
this mixed Japanese/English text, not counted with a tokenizer.

The signals a curator would want are not there:

- `updated` is the import date for 448 memories (79 %); 522 are at revision 1. Age says nothing.
- `uses` counts returned search hits as well as reads, and rises with body size (median 3 in the
  smallest quartile, 9 in the largest). The most "used" memory is the largest one.
- No memory is pinned. "Rarely used but must be kept" has no representation.
- A `feedback` memory, ranked first in `memory_index`, still names the retired trunk branch.

### Why 0117's review did not work

Its mode is `off`; on the measured workspace 25 proposals from 2026-10-19..20 (13 runs, 5 sessions) sat
in the queue unanswered for 17 days; 1 of the last 300 published changes is authored `review`. All 25
are `create`; 11 have an existing memory with a near-identical name; one says in its own body that it
contradicts `develop-trunk-ci` and is still a `create`. The causes are structural:

1. The helper may only update a memory it was shown (`memreview/prompt.go:18`), at most five chosen by
   keyword overlap (`memreview/limits.go:20`), and of those only one whose whole body fit in
   `BodyChars` = 2,000 characters (`input.go:229`); an update of any longer memory is refused
   (`ops.go:153`). On the measured store 146 of 568 bodies (26 %) are longer than 2,000 characters
   (median 1,343 characters, 2,228 bytes), so for a quarter of the store a change the helper wanted to
   propose could only be a new name.
2. `update` replaces the whole body (`prompt.go:26`), so even where it is allowed it is the expensive
   and risky choice.
3. A run is started by 30 transcript turns or 3 user prompts (`loop.go:39`) and reads that session's
   window, split at `WindowChars` = 40,000 (`limits.go:13`). Staleness caused by a change in the world,
   and duplication across sessions, are invisible from inside one window.
4. Every proposal costs one human decision. 0117's Consequences named the queue nobody reads; that is
   what happened.
5. Agents already save in the turn. The review mostly re-proposed the same moments, smaller.

## Prior art

Surveyed 2026-11-06; sources and read/secondary/inferred marks in
[docs/log/136](../log/136-cultivated-memory-survey.md). What each contributes here:

- **ACE (Agentic Context Engineering, ICLR'26)** — the case against whole rewrites, from an AppWorld
  case study of an earlier method (Dynamic Cheatsheet): one rewrite shrank the context from 18,282 to
  122 tokens and accuracy fell from 66.7 to 57.1, below the 63.7 of no adaptation. ACE itself keeps
  itemised entries with helpful/harmful counters and merges deltas with non-LLM logic. Its finance
  results fall from 81.9 to 77.1 when the reflector has no ground-truth labels; it stays above the base
  model with a much weaker reflector except under adversarial updates every iteration, and its authors
  still say it relies on a reasonably strong reflector.
- **Mem0** — its own documentation describes v2's extraction as ADD-only. An independent study
  (arXiv 2606.15903) compared the same v2.0.2 with its LLM update router on (`infer=True`) and off on an
  adversarial suite, with DeepSeek-V3 rather than Mem0's tuned default model: the router lost 31–92
  points on the supersession categories (for example 82 % → 0 % on paraphrased supersession) while it
  gained on identifier ones. A model asked to update or delete freely did badly at exactly the case
  this record cares about.
- **Zep / Graphiti** — bi-temporal facts (`valid_at`/`invalid_at`, `created_at`/`expired_at`); a
  contradicted fact is closed, not deleted. Several LLM calls per write and a graph database.
- **GitHub Copilot Memory** — the closest analogue: repository facts carry code citations and are
  validated against the current branch just before use (an LLM judgment); expiry (default 90 days) is
  renewed **only by a validated use, not by retrieval**.
- **"Revoked but Still Authoritative" (arXiv 2609.08258)** — none of five memory systems excluded a
  revoked fact from retrieval by default; revoked facts often outranked their replacement.
- **Codex memories** — per-session extraction after idle, then one locked consolidation agent writing a
  bounded diff. Its open bugs (#48679 truncated summary, #57091 links to missing paths) show the
  consolidator's output needs deterministic validation before it is stored.
- **fiberplane/drift** (MIT) — deterministic doc→code anchors with AST fingerprints in a lockfile,
  checked in CI for what a diff touches. Prior art for anchors, for documentation.
- **Hermes Agent**, read at `90d6b98`: a hard character cap on the always-loaded memory (2,200 chars)
  that forces "consolidate + add" in one batch on overflow; `patch` by substring; a forked reviewer
  every 10 user turns whose additions apply and whose patches/removals are staged for a human; skills
  with usage sidecars and an idle-time curator (active → stale 14 d → archived 30 d, never deleted). Its
  own issues: a prompt that pushes the reviewer to write every pass, pruning by direct use that broke
  indirectly referenced skills, an unattended run that deleted memory, and full conversation replay
  per review (up to 600 k tokens).
- **Honcho** (Plastic Labs, AGPL-3.0, v3.5.0 2026-10-30) is a **peer-centric reasoning engine**: its
  README describes memory for agents that understand "changing people, agents, groups, projects, and
  ideas", with documents and tool traces as input, integrations for coding agents (Claude Code,
  OpenCode, Hermes and any MCP client) and a workspace several agents can share. Per (observer,
  observed) pair a background worker derives explicit, deductive and inductive "conclusions", and a
  periodic "dream" (≥ 50 new conclusions and ≥ 8 h since the last) merges duplicates and deletes
  conclusions a newer fact replaced. Postgres + pgvector, managed or self-hosted, a small hosted model
  by default; the docs read describe no review or edit of conclusions by a person.
- **Letta sleep-time agents, LangMem, Claude Code auto memory and `/doctor prompt-audit`,
  Supermemory, Basic Memory (Markdown + MCP, AGPL-3.0), agentskills.io** — the same pieces in other
  shapes. Cursor removed its memories in favour of rules.

Within the products and versions this survey covered, none was found that combines code anchors, a
retired-term list and links in a no-LLM check of agent memory, applies revision-checked delta patches
from several concurrent writers, gives knowledge kinds different lifetimes, or publishes a file spec
for memory entries. That is a statement about this survey's reach, not a proof of absence.

## Decision

### 1. What "growing" means

The store grows when it becomes more **accurate** (a stale entry is caught and fixed soon after the
world changes), **denser** (one lesson is one rule; the count may fall), **more abstract** (incidents
become rules and verified procedures; the incident becomes history) and **better placed** (the right
entry reaches the session that needs it, and what is not needed falls out of view). The number of
entries is not a goal. Decision 9 says how each is observed.

### 2. Remove 0117's review in the next release

Removed, each with its tests:

| Surface | What happens |
|---|---|
| ui-prefs `memoryReview` and `memoryReviewAuto` | ignored on read and dropped on the next save; the Agent's member-origin check, switch sync and wake for them (`workspace/agent/ui_prefs.go:263`, `:312`, `:340`, `:366`) go; the Console prefs registry and `equivalence.golden.json` lose both keys |
| the loop, "Review now", `POST /agents/memory-review/run` | removed from the Agent and from the CP relays (`control-plane/relays_memory.go:42` onward) |
| the proposals list, approve/reject routes and their audit (`control-plane/proxy.go:127`) | removed after the export below has succeeded |
| the review switch and the AI-assist row for `memory.review` (`workspace/agent/ai_assist.go`, `console/src/lib/aiAssistFeatures.ts`) | removed |
| the pending-count notification | no longer raised; an existing one links to the change list |
| `memory.review` in the ledger | kept in the enum and the Console catalogue: 0029 is append-only and past rows carry it |
| "revert this run" (`console/src/features/settings/memory/memoryChanges.tsx:253`, `/agents/memory-review/runs/revert`) | kept, moved out of the review queue module into the change list, still behind the member-origin mark; past runs stay revertible |
| guide/member/18 and 20, `guide/ref` mentions, contract tests | updated in the same change |

**The pending queue is exported, not dropped.** A one-time, versioned, idempotent migration on every
workspace writes each pending proposal — with its id, scope, project, base revision, operation, body,
reason, evidence and run id — plus the rejected-hash list to a file under the store's work directory,
fsyncs it, and only then deletes the queue. A restart re-runs it and finds it done. The run recovery
of 0117 never re-dispatches an old run after the migration, and no notification is raised for it.
An exported proposal is an **observation**, never an approval: when decision 6 exists it reads them as
input, and every rule of decision 6 (including no revival) applies to them.

### 3. Schema: kind, lifecycle, anchors; visibility; counters

**Frontmatter, all optional** — a memory without them behaves as today:

- Classification: `kind` — `preference` | `lesson` | `procedure` | `state` | `log`, how it goes stale,
  beside 0108's `type` (who it is about). Proposed lifetimes: preference and lesson do not expire;
  procedure is checked when its anchors move; state expires after N days unless re-verified; log is
  history, never in the described part of the index, and keeps a summary plus a pointer.
- Dependencies: `anchors` — paths, symbols, test names, ADR numbers, profile and branch names,
  commands, `[[links]]`. Extracted mechanically and editable.
- Lifecycle: `status` (`active` | `quiet` | `archived`) with `status_by` (`agent`, `member`,
  `consolidator`); `superseded_by` and `invalid_at` (Graphiti's vocabulary); and `reasons`, the list of
  open doubts, each `{by: lint|feedback|member, rule, note, revision}` — the rule that fired (a lint
  check id, or `stale`/`wrong`), a one-line note, and the revision it was found on. **Suspect is not a
  status**: a memory is suspect while `reasons` is not empty. Every writer's output is validated
  against this one schema.

What an agent sees is decided in this order, each step independent of the ones after it (the member's
routes see everything):

1. **Archived or superseded excludes**, whatever the kind or pin: no index line, no search hit, a stub
   from `memory_read`, nothing injected or written back.
2. **Open reasons are always shown**: every line, hit, read and injection of a suspect memory carries
   its reasons, and a suspect memory is not written back to claude.
3. **Placement**: a `log` kind is placed like `quiet` whatever its status; `quiet` is not in the
   described index, but is in the names-only tail, search and read.
4. **Pin last**: a pin lifts an `active` memory into the described part; it does not lift `quiet` or
   a `log` (so "log is never in the described part" holds) and never undoes step 1.

| | described index | names-only tail | `memory_search` | `memory_read` by name | lcpp injection | claude write-back |
|---|---|---|---|---|---|---|
| `active` | yes (ranked as today) | when it does not fit | yes | full | yes | yes |
| `active` with open reasons | yes, reasons prefixed | when it does not fit | yes, reasons prefixed | full, reasons prefixed | yes, reasons prefixed | no |
| `quiet`, or any `log` | no | yes | yes | full (reasons prefixed if any) | no | no |
| `archived` or superseded | no | no | no | stub only | no | no |

Acceptance on every path: an archived log, a suspect log, a pinned log, a superseded log, and a pinned
quiet memory.

**Who may set what, and what every writer keeps.** A save, patch, pin or import that does not name a
field keeps its current value; this is a contract of `agentMemSave` (`agent_memory.go:1080` today keeps
only created, source, hash and pin) and of the claude and muse imports (`agent_memory_claude_import.go:487`,
`:781` rebuild the entry). An agent may set `kind` and `anchors`. Lifecycle fields are set only by
lint, feedback, the consolidator through the approval gate, or the member. Each entry of `reasons`
is cleared by its own rule: a lint reason only when lint re-checks a newer revision and the rule no
longer fires; a feedback reason by any body change (from `memory_save` or `memory_patch` alike); a
member reason, or any reason, by the member's explicit operation. A punctuation fix therefore does
not clear a missing path. Acceptance: a lint reason and a feedback reason together, then a body
change: (a) a change that does not fix the lint rule's cause leaves only the lint reason, and it stays
after the next lint run; (b) a change that also fixes it leaves the lint reason until the next lint run
on that revision, which clears it because the rule no longer fires. An import never takes lifecycle fields from outside.
**Revert is the exception to "keep"**: it restores the earlier version whole — kind, anchors,
lifecycle fields and pin — as `agent_memory_changes.go:610`–`620` already does for the body and pin,
with the revision still increasing and the member as author; reverting a lint reason or a pin undoes it.
Acceptance: a test per writer (save, patch, pin, claude import, muse import, project move) that each
field survives, and for revert that a lint reason and a pin are undone.

**Visibility is one predicate, applied by every agent-facing read path.** `archived` and superseded
memories are not returned by `memory_index`, `memory_search`, the external memory MCP (same handlers),
lcpp's per-turn injection, or the claude write-back (`agent_memory_claude_export.go:504` loads the whole
scope). `memory_read` by name returns a stub — name, status, `superseded_by`, reasons — and not the
body, so an old name or a `[[link]]` cannot bring the text back as current. The member's routes (list,
history, diff, restore, bundle export) see everything. A test per path, because the revocation study
found that no system does this by default.

The claude write-back needs more than a filter: it protects every AF file name and keeps native files
it did not choose (`agent_memory_claude_export.go:519`, `:639`–`668`), and re-lists them in
`MEMORY.md` (`:773`–`803`), so a copy written while a memory was eligible would reappear. One
predicate — the write-back column above: `active`, not superseded (no `superseded_by`), no open reasons,
not a `log`, which includes the exclusion of step 1 — decides three things
together: what AF selects, which copies AF wrote earlier are withdrawn, and what `MEMORY.md` lists. A
copy AF wrote and nobody edited since is withdrawn (removed, shown in the preview) as soon as its
memory stops being eligible, for any reason in the table; a copy edited on the native side is
reported as a conflict for the member, and in neither case is it listed in `MEMORY.md`. Path tests:
export, then make the memory suspect, quiet, a log, archived or superseded (the last one with `status`
still `active` and only `superseded_by` / `invalid_at` set), then export again.

**Counters.** Today `.usage/<name>` is a file grown by one byte per use and counted by its size
(`agent_memory_usage.go:5`, `:47`); reads and search hits cannot be told apart afterwards. New sidecars
use the same append-a-byte method, so concurrent writers still lose nothing, in their own directories
so that no name can collide (a name may contain `.`, `agent_memory.go:93`, so `foo` and `foo.reads` are
both valid): `.usage-v2/reads/<name>`, `.usage-v2/hits/<name>`, `.usage-v2/confirmed/<name>`,
`.usage-v2/stale/<name>`. The time of the last read is the reads file's mtime, which is touched even
past the cap. Acceptance includes `foo` beside `foo.reads` and a 64-character name through counting,
forget and re-create. The legacy file keeps
counting for one release and the ranking keeps using it; the switch to `reads` comes with a release of
data. Forget and re-create clear all of a name's sidecars, a project move moves them, a restore starts
them at zero — as today.

### 4. Cheap writes: patch now, feedback in step 2 (tools)

**`memory_patch(scope, name, revision, edits: [{old, new}], description?)`.** Edits apply to the body
only; `description` optionally replaces the description; frontmatter is never touched. Every `old` is
non-empty and matches exactly once in the current body, and the matches do not overlap; all edits are
matched against the same original and applied together. All succeed or nothing is written: one
revision, one commit, the same secret scan, author rules and history as `memory_save`. A missing
memory or revision 0 is refused — patch never creates. Fixing one line no longer re-sends 8 KB.

**`memory_save` of a new name** returns the closest existing names when they are near (the name and
description ranking of `memory_search`), with "prefer memory_patch on X". It does not refuse.

**`memory_feedback(scope, name, revision, verdict: confirmed|stale|wrong, note)`** (step 2). The
revision is the one the reader relied on. When it is the current one, `confirmed` adds a confirmation
and sets the time of the last confirmation; `stale` or `wrong` adds a feedback entry to `reasons`, with the note.
When it is older, the report is kept as an observation only and changes neither status nor
confirmation, so a report on revision 3 cannot mark a fixed revision 4. The note is secret-scanned
like a body. One report per session per revision counts. `confirmed` never clears a reason; how
each reason is cleared is in decision 3. As in Copilot Memory, only a confirmation renews a
memory; being retrieved does not. The guidance asks only for the negative case.

### 5. Invalidation without an LLM: lint

**Step 1 — report only.** Anchors do not exist yet, so lint extracts them on the fly from each body
(paths, `Test*` and code-shaped symbols, ADR numbers, `[[links]]`) and checks existence only: a path
or symbol missing from the project's default branch, a link to no memory, a term from the
member-maintained retired-term list, a body over 4,096 bytes. It reads the repository by ref
(`git ls-tree` / `git grep` on `origin/<default>` in the project's git-common-dir) and never checks out
or changes any working copy. A project without git, an orphaned project and the user scope get the
link, term and size checks only. Output: a read-only report in Settings → Agent memory and
`af-memory lint`. Nothing is written.

**Step 2 — reasons.** With stored anchors, lint adds an entry to `reasons` for each hit. It records the
revision it checked, and applies under the store lock with a compare-and-swap on that revision; a memory
changed since is re-checked, never marked from the old text. Adding or clearing a reason is a published change, one
commit authored `lint`, and it **increments the revision**, so an agent holding an older revision is
refused and re-reads (the 0108 decision 4 rule), instead of silently erasing the mark. Revert works as
for any change. "Exists but changed" needs a baseline (a commit or a fingerprint per anchor, as drift
does); that is recorded with the anchors and is what step 2 adds.

A deterministic rename (a git rename of an anchored path) is its own operation, `lint-rename`: it
replaces only that path in the body, carries the commit as evidence, uses the same compare-and-swap,
and is revertible. It applies by itself (decision 7).

**One writer.** `agentMemMu` is an in-process mutex (`agent_memory.go:98`). Inside AF every engine write
goes through the Agent's store; a standalone engine run against an AF store calls the Agent's API, and
outside AF it takes a lock file in the store. Two processes never write the same store directly.

### 6. Consolidation (step 3): inbox → topic → delta patches

Not in the next release; recorded so step 1 does not block it. Observations (the exported 0117 queue;
`stale`/`wrong` notes; later an `observe` tool) are grouped by topic cluster with the suspect
memories of that cluster. A model classifies each observation against the cluster as add / patch /
supersede / no-op from snippets; a model writes **delta patches** (decision 4's form), never a whole
rewrite. The same lesson twice becomes one rule; narrow cases fold into an umbrella memory.

Rules it inherits:

- **No revival** (0117 decision 5): a create on a name that was forgotten or ever existed is refused,
  checked inside the store lock when queued and again when approved. `agentMemSave` alone would allow
  it; today only the review sets `noRevival` (`agent_memory_review_publish.go:126`).
- **Supersede is a change to one memory, the old one.** The replacement is created or patched first,
  as its own published change; the supersede then sets `superseded_by` and `invalid_at` on the old
  memory, after checking under the store lock that the replacement exists in the same project or the
  user scope, at the revision the proposal saw, and that no cycle forms. Both are ordinary one-target
  changes, so the change list, diff, latest-change check and revert keep working as they are
  (`agent_memory.go:1371` writes one `AF-Memory` trailer; `agent_memory_changes.go:316`, `:434`, `:627`
  assume one target). Reverting the supersede brings the old memory back beside the replacement: two
  active memories, a duplicate the next lint or consolidation sees, never a half-applied state. A
  change spanning several memories in one commit would need its own record.
- **Deterministic validation before queueing**: size, every `[[link]]` and path exists, frontmatter
  schema, the patch applies to the current revision — the Codex consolidator's bugs are exactly these.
- **The model that judges is not chosen to be weak**: anchors and feedback carry as much of the
  judgment as possible. ACE's results do not show that a small judge fails, nor that it is safe; step 3
  measures it.

### 7. Who applies what

Three actors, three rules:

- **Interactive agents** keep 0108 decision 8: `memory_save`, `memory_patch` and `memory_forget` publish
  at once and are found and undone afterwards in the change list.
- **Deterministic lint** applies by itself what it can prove: reasons (step 2) and `lint-rename`.
- **The autonomous consolidator** never publishes: every operation waits for the member, grouped by
  topic, approved per topic, not per item. Nothing is deleted automatically; history keeps everything
  (0022).

### 8. Independence from AF, and position

The format (decision 3) is written as a versioned spec. The engine — lint, patch, feedback
bookkeeping, later consolidation — imports no AF package and takes the model call as a plug-in (a
one-shot CLI command or an API). AF is one adapter (scopes, scheduler, ledger, Console approval, auth);
an MCP server mode is another. **Where the engine lives is open question 1.**

Position against nearby work:

- **Hermes Agent is a harness, not a memory engine.** Its learning loop runs inside its own runtime; its
  files are portable but the loop is not, and it serves one agent for one user without history or a
  revision check. AF could drive Hermes as a kind, and this engine could be offered to Hermes through
  its `MemoryProvider` interface, which also tests the independence above.
- **Honcho is the nearest independent engine, built on different principles.** It covers projects and
  coding agents too, but what it keeps is conclusions a model derived in the background, in a database;
  it has no git history or revert, no revision check between writers, no deterministic anchors into the
  repository, no approval step, and its dream deletes what this record keeps. Those are the properties
  decisions 3–7 rest on, so Honcho is not the engine here. **As a matter of AF policy**, not a limit of
  Honcho, the first place it could plug in is the `preference` kind: opt-in, its output read as evidence
  like any memory, never as an order and never written into the store unreviewed. It sends
  conversations to a service for reasoning (the concern 0117 decision 2 met with redaction).
- **The closer competitor is memory built into the CLIs** (Claude Code auto memory, Copilot Memory,
  Codex memories). What this record offers that they do not: one store read by every kind, checked
  against the repository, with history and revert.

### 9. Measures (proposed, none measured yet)

Per project, on the lint report: entries flagged and how long they stay flagged; `stale`/`wrong` reports
per 100 reads; share of active entries confirmed in the last 30 days; bytes of the described index;
member decisions per week; tokens per week spent on upkeep; and, from step 3, the model input actually
sent per run against the bytes of the store.

### 10. Steps

**Step 1, the next release** — each item ships alone; none needs a later step:

1. Decision 2: removal, the queue export, run revert in the change list.
2. Decision 3's new counter sidecars, beside the legacy one; ranking unchanged.
3. `memory_patch` (decision 4), with the preservation contract of decision 3 for the fields that exist
   today.
4. Lint, report only (decision 5, step 1), with the retired-term list.
5. Guidance: "patch rather than save when the memory exists".

**Step 2**: `kind` / `anchors` / lifecycle fields with their preservation tests, visibility on every
read path, `memory_feedback`, lint writes `reasons`, `lint-rename`. **Step 3**: consolidation (decision 6).
**Step 4**: promotion of verified procedures to skills (agentskills.io format), approval-gated.

**Not decided here: an always-loaded project brief** (Hermes' capped memory). 0108 decision 5 limits
what the 0042 distributor writes to fixed guidance, the distributor is user-wide, and 0042 decision 8
forbids agents to write user instructions; a brief would have to be a separate per-project advisory
artifact with its own writer and approval. It needs its own record.

## Rejected

- **Keep 0117 and widen its input** (more bodies, the whole index): the trigger is still one session
  window, the output still a whole rewrite, and the cost per run grows with the store.
- **An LLM pass over the whole store on a schedule**: about 570 k tokens per sweep (estimated, see
  Measured), most of it on memories nothing has changed.
- **Decay by inactivity alone**: `uses` is not a usage signal yet, rare but critical procedures are
  exactly the unread ones, and Hermes' curator broke indirectly referenced skills this way.
- **Auto-forget**: a forgotten memory silently leaves every session's view; the member decides.
- **Adopt an existing engine.** Basic Memory is closest (Markdown is the truth, MCP) but has no lifecycle,
  anchors, revisions or feedback, and AGPL-3.0 constrains embedding; Graphiti needs a graph database and
  several LLM calls per write; Mem0 is ADD-only and vector-bound; Letta's memory belongs to its harness;
  Honcho keeps model-derived conclusions in a database without history, revision checks or anchors,
  and is AGPL-3.0. Each would drop most of decisions 3–5 and 8. Their semantics are
  adopted instead.
- **Hard cap on the whole store**: the store holds history-shaped entries legitimately; a cap belongs to
  an always-loaded brief, which is not decided here.

## Consequences

- The member loses a queue they did not use and gets a report they can act on in bulk; past review runs
  stay revertible from the change list.
- Agents get one cheaper tool now and one more in step 2; the guidance grows by one line now.
- Every writer and both imports gain a preservation contract and tests for frontmatter they do not set.
- Every agent-facing read path gains a visibility check and a test.
- Lint reads each project's default branch by ref; a project without a repository gets fewer checks.

## Open questions for the owner

1. **Where the engine lives.** (a) A separate Go module in this repository with an import ban on AF
   packages, split out with history (`git subtree split`) once a second consumer exists or the format
   reaches v1; (b) a new repository from the start (for example the spec and lint first), AF pinning it
   like its other tools. Step 1 is AF-side either way.
2. The retired-term list: member-edited only, or also proposed from a commit or ADR that retires
   something.
3. The approval unit for step 3: per topic per week, or a single weekly digest.
4. The model and monthly token budget for step 3.

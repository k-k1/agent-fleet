# 0112. Importing codex's memories into AF memory: what could be measured, and the proposed rules

English | [日本語](0112-codex-memory-import.ja.md)

- Status: **proposed** (2026-10-10). Documentation only: no import code exists for codex. The
  consolidated files (`MEMORY.md`, `memory_summary.md`, `rollout_summaries/*.md`) could **not be
  produced** in the workspace, so every rule below that depends on their shape is an assumption
  taken from the codex binary's embedded prompts, and is marked as one.
- Follow-ups: #1950. Refs: #1683, #1569; [0108](0108-af-owned-agent-memory.md) decision 6 step 1;
  [0022](0022-agent-memory-management.md) decision 6; `memoryx/memory_roots.go`,
  `memoryx/agent_memory_claude_import.go`.

## Context

The one-time claude import (#1569) works because claude's memory is already the shape AF stores:
one directory per project, one file per memory, frontmatter with `name` and `description`. Issue
#1683 asks for the same seed from codex. The issue's rule for doing it is "measure the real file
shape first, then decide". This record is the measurement attempt and what follows from it.

## Measurement

Codex CLI 0.162.1, a throwaway `CODEX_HOME` under the session's work directory (not `~/.codex`),
`[features] memories = true` in its `config.toml` (`codex features list` then prints
`memories stable true`; `external_agent_memory_import` is listed as `under development`, `false`).

**Observed** (one `codex exec "say hi"` run):

- `memories/` appears on the first run, with its own `.git` (the diff baseline of phase 2) and
  these files: `raw_memories.md` (37 bytes, the text `# Raw Memories` / `No raw memories yet.`),
  `phase2_workspace_diff.md` (a generated git-style diff of the workspace, "Read this file first
  and do not edit it"), `extensions/ad_hoc/instructions.md` (733 bytes, fixed text) and an empty
  `rollout_summaries/`. `memories_1.sqlite` sits next to `memories/`, not in it.
- The `codex exec` run failed with `401 Unauthorized` against the model API: the throwaway home
  has no credentials, and the real `~/.codex` is off limits. Consolidation needs model calls
  (phase 1 extracts from a finished rollout after an idle period; phase 2 is a sub-agent), so no
  consolidated output could be obtained: `MEMORY.md`, `memory_summary.md`,
  `rollout_summaries/<slug>.md` and `skills/` were never written. Whether either phase was started
  is not known; only the 401 of the one run was observed. No real consolidated file was seen, and
  none is quoted or invented here.

**Taken from the codex binary's embedded prompt templates** (strings of the same 0.162.1 binary;
what codex *instructs* its model to write, not output that was seen):

- `memory_summary.md`: first line exactly `v1`, then `## User Profile`, `## User preferences`,
  `## General Tips`, `## What's in Memory` (under it `### <project scope>`, `#### <date>` and
  pointer bullets to `rollout_summaries/<file>`); under 10,000 bytes. It is injected into every
  new session, so it is an index and not a set of memories.
- `MEMORY.md`: a sequence of blocks, each
  `# Task Group: <title>` / `scope: <one line>` / `applies_to: cwd=<path>; reuse_rule=<text>`,
  then `## Task <n>: <title>` sections (each with `### rollout_summary_files` and a keywords
  bullet), then optional `## User preferences`, `## Reusable knowledge`,
  `## Failures and how to do differently`. Bullets use `-`; no bold. Blocks are ordered by expected
  usefulness, not by name, and codex rewrites them on every consolidation.
- `rollout_summaries/<rollout_slug>.md`: a per-rollout recap with a `description:` line and
  numbered raw-evidence snippets; it holds `thread_id` and `rollout_path`.
- `skills/<name>/SKILL.md`: reusable procedures with `name` / `description` frontmatter.
- `extensions/ad_hoc/notes/<YYYY-MM-DDTHH-MM-SS-slug>.md`: verbatim notes the user asked codex to
  remember (the `add_ad_hoc_note` tool); the extension text says to treat them as authoritative
  and tag derived claims `[ad-hoc note]`.
- Phase 2 also supports merging another agent's memory dir (a branch of the prompt about
  "imported resources"); that is the upstream counterpart of this issue, not what we need.

## Proposed rules (assumptions until real output has been seen)

1. **Source: `MEMORY.md` only, split by `# Task Group:` block.** One block is the closest thing to
   "one memory" that codex writes, and its header is self-describing. `memory_summary.md` is not
   imported (an index, and it points into files we do not copy). `rollout_summaries/` is not
   imported (per-rollout recaps with raw evidence; the volume is large and the facts worth keeping
   are already consolidated into `MEMORY.md`). `skills/` is not imported (procedures belong to the
   skills mechanism, not to memory). `extensions/ad_hoc/notes/` is a candidate for a second,
   later step: verbatim, member-requested, one file each, so the least lossy source.
2. **Derived fields.** `name`: the block title lowercased and reduced to `[a-z0-9-]`, cut to fit
   `agentMemNameRe` (64 characters), a short hash suffix on collision. `description`: the `scope:`
   line (one line, within `agentMemMaxDescription`); a block without it is `invalid`, as a claude
   file without a description is. `type`: left empty (codex has no type; guessing one would be
   invented data). `body`: the block verbatim from its `# Task Group:` line up to the next one.
3. **Scope.** The `applies_to: cwd=` value is matched to a project the same way claude slugs are
   (`agentMemImportProjects`: a working copy under `~/repos`, worktrees folding into their main
   clone). A block with no match, or whose cwd is a family or a workflow, defaults to **user
   scope** only as a proposal (see open question 7: user scope spreads the text to every
   project); the preview lets the member pick a project or user scope per block, and the request
   carries the choice. Nothing is placed in a project the member did not see in the preview.
4. **Same rules as the claude import**: preview first and apply re-evaluates under the locks; a
   block with a secret-scan hit is listed with masked findings and never imported; a name with a
   tombstone or in the history is `forgotten` and never resurrected; `source` is
   `codex:memories/MEMORY.md#<name>`, `source_hash` the sha256 of the block's bytes; author kind
   and session are `unknown`; the file is opened with `O_NOFOLLOW` against a pinned directory
   handle; a count cap per preview and per apply.
5. **Never written back.** As in 0108 decision 6: AF memory is not copied into `~/.codex/memories`.

## Open questions (they need real output)

1. **Name stability and resurrection.** Not observed: the prompt lets codex reorganise blocks on a
   consolidation, so a title may change. If it does, the derived name changes, and the old name's
   tombstone (kept by scope and name) no longer protects the same knowledge: a forgotten block
   could return under a new name. `source_hash` cannot fix this. It is the sha256 of the whole
   block including its heading (rule 4), so a changed title alone changes it; it stays what it is
   in the claude import, the identity of the input for preview / apply change detection. Stopping
   resurrection needs a separate, still undesigned stable ID or normalised fingerprint. Before
   implementation there must be a test that a block with only its title changed, and one imported
   into a different scope, is not brought back after it was forgotten. How much a block's text
   changes across consolidations can only be measured on several real consolidations.
2. **Whether `applies_to: cwd=` is usually a path.** The prompt allows "cwd family or workflow
   scope". The share of blocks that map to a project decides whether user scope should be the
   default or the exception.
3. **Block size.** A block carries every task of the group; `agentMemMaxBody` is 200 KiB, but the
   memory read tools are meant for short entries. Whether to split by `## Task <n>` instead is a
   question for real block sizes.
4. **Ad-hoc notes**: whether to import them (rule 1) and whether the `[ad-hoc note]` tag is kept.
5. **Whether the stage-1 `raw_memories.md` is worth a source at all.** It is "temporary" per the
   prompt, and the member may have it only between phases.
6. **Whether `external_agent_memory_import`** (under development in codex) replaces this: it is
   the reverse direction (claude → codex) and does not change the import described here.
7. **Personal paths and spreading through user scope.** `cwd=` can contain a person's or a
   customer's name, and the body is copied verbatim; the general secret scan does not reliably
   catch either. Defaulting an unmatched block to user scope would hand that path and
   project-specific text to every other project. This is a different risk from secrets. Before
   implementation decide: the preview shows the body and `cwd` and the distribution scope, and
   the member must choose a scope explicitly for an unmatched block; whether the path is removed,
   the block refused, or the text anonymised (an anonymised body no longer matches the verbatim
   contract, so `source_hash` must then say which bytes it covers).

## Consequences

- No behaviour changes; nothing user-visible is added, so the guide is unchanged.
- The work that remains, building the import, is tracked in the follow-up issue linked from the
  pull request, and waits for a workspace in which codex has run its consolidation with a login
  the member chose to use.

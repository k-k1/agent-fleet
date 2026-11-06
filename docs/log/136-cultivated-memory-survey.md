# 136. Cultivated memory — prior-art survey (ADR 0130)

status: **Surveyed (2026-11-06).** The prior-art input to [ADR 0130](../decisions/0130-cultivated-memory.md).
The store measurements that motivate it are in that record's Context. Hermes Agent was read separately
(source at `90d6b98`) and is summarised in the record, not here.

Accessed 2026-11-06. Evidence markers: **[R]** read in a primary source (repo, official docs, paper);
**[S]** secondary source (blog, third-party review, search summary); **[I]** my inference.
Stars and last push are from `gh api repos/...` on the access date. Hermes Agent is out of scope (studied
already): capped MEMORY.md, background review fork and a skills curator.

## 1. Comparison table

| System | Store / format | Update model | Staleness / contradiction | Human gate | LLM cost | Portability | License · activity | Coding-agent fit |
|---|---|---|---|---|---|---|---|---|
| **Mem0 v2** (OSS + platform) | Vector DB + entity links | **ADD-only** since v2.0.0 (2026-04-16). The old ADD/UPDATE/DELETE/NOOP pass was dropped [R] | No invalidation. Recency is resolved at retrieval time from `created_at`; optional expiry [R] | None | 1 call per `add()` [R] | DB-bound | Apache-2.0 · 68k★ · daily commits; mem0ai 2.2.1 released 2026-09-25 [R] | Generic chat memory. OpenMemory MCP was deprecated and removed on 2026-07-29 [S] |
| **Zep / Graphiti** | Temporal KG in a graph DB (Neo4j/FalkorDB/Kuzu…) [I] | Episode → extract → dedup → edge resolution [R] | **Bi-temporal edges**: `valid_at`/`invalid_at` (world time) and `created_at`/`expired_at` (system time). Contradicted edges are closed, never deleted [R] | None | Many calls per episode (extraction, dedup, per-edge resolution); MinHash/LSH fast path [S]. Issues report $0.80 for 40 chats and 626 s for one sentence [S] | DB-bound | Apache-2.0 · 33k★ · active | Generic. Its validity model is the best one to borrow |
| **Letta / Letta Code** | Memory blocks (DB). Letta Code: **MemFS, all context tracked in git** [R] | Agent self-edits (`memory_insert/replace/rethink`); **sleep-time agent** consolidates asynchronously [R/S] | LLM-judged during "dreaming" | None | One background agent run every N turns | Server-bound; Letta Code memory is in git | Apache-2.0 · 25k★ (letta) / 3.8k★ (letta-code) · active | Letta Code is a coding harness; its memory is tied to the harness |
| **LangMem** | LangGraph BaseStore (KV + vectors) | Memory manager LLM upserts and consolidates; `ReflectionExecutor` debounces work into the background [R] | LLM resolves contradictions while consolidating | None | 1+ call per reflection | Library, store-pluggable | MIT · 1.7k★ · active | Generic; tied to LangGraph |
| **Cognee** | Graph + vector (LanceDB, etc.) | `remember/recall/improve/forget`. `memify` is deprecated in favour of `improve()`, which has an opt-in fact-conflict review [R changelog] | Feedback reweights edges; marketing says it prunes stale nodes, but the changelog suggests the default memify only built embeddings [R/S] | Opt-in review | Per-ingest LLM extraction | DB-bound | Apache-2.0 · 33k★ · active | Generic |
| **A-MEM** (NeurIPS'25) | Zettelkasten notes (keywords, tags, context, links) | On add: LLM links the new note to related notes and **evolves the neighbours' attributes** [R] | None explicit | None | Several calls per add [I] | Research code | MIT · 1k★ | Research |
| **ACE** (ICLR'26) | Playbook of **itemised bullets, each with an id and helpful/harmful counters** [R] | Generator → Reflector → Curator emits **deltas, merged by deterministic non-LLM logic**; grow-and-refine dedup by embeddings [R] | Harmful counters plus refine | None | Reflector + Curator per episode | Plain text | Apache-2.0 · 1.4k★ · last push 2026-08 | Benchmarks only (AppWorld, finance); the community has built MCP servers [S] |
| **Generative Agents / Reflexion / Voyager** | Memory stream / verbal reflections / JS skill library | Importance-triggered reflection; self-critique after a failure; a skill is added after it is **verified in the environment** [R, known papers] | Recency decay (GA) | None | Per-step | Research | Apache/MIT · inactive since 2024 | Voyager's "verify, then add the skill" pattern carries over |
| **MemoryBank** (AAAI'24) | Dialogue memories + FAISS | Ebbinghaus decay R=e^(−t/S); a recall strengthens S [R] | Time-based forgetting | None | Low | Research | MIT · inactive | Weak (companion chatbot) |
| **Supermemory** | Hosted graph. **Versioned memories** (`isLatest`, `parentMemoryId`, `forgetAfter`, `isForgotten`); relations updates/extends/derives [R docs] | New version on update; "dreaming" derives facts [R] | Supersession plus `forgetAfter` expiry; forget is a soft delete [R/S] | None | Proprietary | Engine is a hosted service; the OSS repo is SDK/frontend [S] | MIT (SDK) · 32k★ | Generic |
| **Basic Memory** | **Markdown + frontmatter; files are the source of truth**, SQLite/Postgres is a rebuildable index; observations `[category]` and `[[relations]]` [R] | Agent calls `write_note`/edit tools; capture is manual [R/S] | None | Implicit (user edits files) | None in the engine | **High** (files, Obsidian) | **AGPL-3.0** · 4.5k★ · active | Usable via MCP; no lifecycle |
| **GitHub Copilot Memory** | Hosted. Repo facts **with code citations**; user preferences cite the user's own words [R docs] | Agent writes during work | **Just-in-time validation against the current branch** before use; validated → upvote, wrong → downvote. Default expiry is 90 days (configurable 7–180), **reset only by an upvote, not by retrieval** (the Jan-2026 launch said 28 days) [R] | Asks only in the interactive app/CLI; the cloud agent and code review do not ask [R] | Validation is an LLM judgment [R] | Not portable | Proprietary · GA path in 2026 | **Highest fit**; the closest analogue to our anchors and feedback |
| **Claude Code** | CLAUDE.md (human) + auto memory `~/.claude/projects/<p>/memory/` (MEMORY.md index + topic files; 200 lines / 25 KB loaded) [R] | Claude edits files; harness nags when MEMORY.md nears its limit; "auto-dream" consolidation [S] | `/doctor prompt-audit` flags **references to files/commands that don't exist** and contradictions, and proposes edits [R] | Audit applies only on request | LLM | Plain files, machine-local | Proprietary | Native |
| **Anthropic memory tool** (`memory_20250818`) | Client-side `/memories` directory; the store is yours [R] | view/create/**str_replace**/insert/delete/rename [R/S] | None | None | n/a | Protocol only | API | A good wire protocol for deltas |
| **Codex memories** | `~/.codex/memories(_v2)`: `memory_summary.md`, rollout summaries, skills/ [S, R issues] | **Phase 1** per session after 6 h idle (schema-constrained extraction, secret redaction); **Phase 2** global consolidation sub-agent under a lock, writing a bounded diff, sandboxed to the memory dir [S] | Drops entries older than 30 days [S] | None | 1 extraction per session + 1 consolidation agent | Local files | Apache-2.0 (codex) | Native. Open bugs: #48679, #57091 |
| **Cursor / Windsurf / Devin** | Cursor **removed Memories in 2.1** → Rules [S]. Windsurf: local auto-memories, rules in repo [R docs]. Devin: Knowledge with triggers, **Suggestions** from feedback [R docs] | Auto-generate; Devin suggests updates | None; Devin advises review | Devin: review queue | — | Rules in repo | Proprietary | Vendors are steering durable knowledge toward rules/AGENTS.md/skills |
| **Cline Memory Bank / Aider** | 6 fixed markdown files (`activeContext`, `progress`, …) / CONVENTIONS.md [R] | "update memory bank" means a **full rewrite review** [R] | None | Human-driven | Full read each time | Files in repo | Apache-2.0 | A prompt pattern, not an engine |
| **Coding-agent OSS** (agentmemory, claude-mem, projectmem, ai-memory, Beads, memsearch) | Mixed: git-backed md (ai-memory), Dolt (Beads), md+Milvus (memsearch) [S] | Hook-based auto-capture; projectmem records failed attempts ("do NOT retry") [S] | Mostly none | None | Low/none (ai-memory: no LLM by default) [S] | Files/git | MIT etc.; young | High, but thin on curation |
| **Doc-drift tools** (fiberplane/drift, Swimm, repowise, docdrift) | `drift.lock`: TOML bindings doc→file[#Symbol] with an **AST fingerprint (tree-sitter, XxHash3)** [R] | Re-link after review | **Deterministic**: fingerprint mismatch or missing file → stale; `--changed` limits the check to docs a diff touches [R]. Swimm does patented auto-sync of tokens [S] | CI gate | None | Files | drift MIT; Swimm proprietary | **Direct prior art for anchors** |

## 2. Design elements vs prior art

**(1) No-LLM anchor lint / invalidation.**
- Closest prior art:
  - **Copilot Memory citations**, checked just-in-time, but the check is an LLM judgment [R].
  - **fiberplane/drift**: deterministic AST-fingerprint anchors in a lockfile, CI-gated, with a `--changed` mode [R].
  - **Swimm smart tokens/paths** [S].
  - **MemStrata** (arXiv 2606.26511): deterministic (subject, relation, object) supersession with no LLM. It reports that cosine similarity separates contradicted facts from duplicates at only AUROC 0.59 [R abstract].
  - Claude Code `/doctor prompt-audit` (LLM, files/commands that no longer exist) [R].
- Pitfalls:
  - Copilot warns that validation is a judgment, so a memory is "not a rule that will always be followed" [R].
  - drift falls back to raw-content hashing outside its six languages, so formatting-only edits count as changes [R].
  - A changed anchor does not mean a wrong entry [I]. Route anchor hits to "suspect, needs judge" rather than auto-retire, except for hard misses (file or symbol gone, retired term present).
- No one found matches a *retired-term list* against memory text.

**(2) Observation inbox + idle consolidation.**
- Prior art:
  - Codex Phase 1/Phase 2 (idle 6 h, global lock, bounded diff, sandbox) is the most engineered [S].
  - Letta sleep-time agents [R].
  - LangMem `ReflectionExecutor` debouncing [R].
  - Claude auto-dream [S].
  - Supermemory "dreaming" [R].
- Reported pitfalls:
  - Codex #48679: the consolidator was never told the 2,500-token read limit and nothing checked size afterwards, so the summary is middle-truncated and the newest entries are lost [R].
  - Codex #57091: the consolidator emits links to paths that don't exist [R].
  - Lesson: validate consolidator output **deterministically** (size, links, schema) before committing.

**(3) Delta patches.**
- ACE is the canonical evidence.
  - Monolithic rewrites cause **context collapse**: 18,282 tokens became 122, and accuracy fell from 66.7 to 57.1, below the 63.7 no-adaptation baseline [R].
  - ACE's deltas are merged by non-LLM logic [R].
- Other prior art:
  - The Anthropic memory tool's `str_replace` (old→new) and Letta's `memory_replace` are the same primitive at the protocol level.
  - Community ACE builds use ADD/PATCH/INCR/DEPRECATE ops [S].
- Counter-evidence from Mem0:
  - Mem0 abandoned UPDATE/DELETE because "overwrites sometimes erased key information… deletes removed information that would be relevant later" [S, Mem0 blog].
  - Going ADD-only then **lost 30–90 points on deletion precision** in an independent study (arXiv 2606.15903) [S].
  - So both extremes fail. Supersede-with-history (Graphiti, Supermemory) plus localized patches is the middle path.
- Revision-checked (compare-and-swap) patches: Codex relies on a global lock instead. No other system I found uses CAS [I].

**(4) Reader feedback.**
- Prior art:
  - ACE's helpful/harmful counters per bullet [R].
  - Copilot's up/down votes on validation. **Only a validated use extends a memory's life; retrieval alone does not** [R]. This is directly adoptable.
  - Cognee's feedback-weighted edges [R/S].
  - MemoryBank's recall reinforcement [R]. Its flaw for us: a merely *retrieved* memory gets strengthened, which entrenches stale entries [I].
- ACE's limitation: it relies on "a reasonably strong Reflector", and without ground-truth signals the context gets "polluted by spurious or misleading signals". The finance average fell from 81.9 to 77.1 [R].

**(5) Knowledge kinds with lifetimes.**
- Copilot separates repo facts (configurable expiry) from user preferences (fixed default) and refuses to store task-specific or short-lived instructions [R].
- LangMem has semantic/episodic/procedural types [R]. Supermemory has `isStatic` vs `forgetAfter` [R]. In Cline, `activeContext` churns and `projectbrief` is stable [R].
- Nobody has a full preference/lesson/procedure/state/log taxonomy with per-kind policies [I].

**(6) Lifecycle; never delete.**
- Prior art:
  - Graphiti closes edges with `invalid_at`/`expired_at` [R].
  - Supermemory uses versions, `isLatest` and soft forget [R].
  - Mem0 v2 never overwrites [R].
- Major pitfall, from "Revoked but Still Authoritative" (arXiv 2609.08258):
  - Across 5 memory systems, **no system enforces revocation by default**.
  - Revoked facts are returned, often rank above their replacement, and drive unsafe actions [R abstract].
  - Lesson: archived/superseded entries must be excluded at the retrieval API, with a test.
- Memora (ACL Findings 2026): 64% of sampled recommendation errors came from outdated memory that was never invalidated [S].

**(7) Small model judges, strong model writes.**
- Partial prior art:
  - Graphiti runs deterministic MinHash/LSH first and falls back to an LLM [S].
  - The Letta community recommends Haiku for the sleep-time agent [S].
  - Codex makes the consolidation model configurable [S].
- No system found explicitly splits a cheap snippet-judge from a strong patch-writer. ACE's caveat about Reflector strength argues for keeping the *judge of correctness* reasonably strong, or anchor-assisted [I].

**(8) Platform-independent engine.**
- agentskills.io is the only real cross-vendor *file spec* (SKILL.md with name/description frontmatter, three-tier progressive disclosure, a `skills-ref validate` validator) [R].
- The Anthropic memory tool defines a *protocol* but no file schema [R].
- Basic Memory proves "Markdown is the truth, the DB is a projection" over MCP [R].
- Letta Code and agentmemory put memory in git [R/S].
- No memory-entry file spec carries lifecycle, anchors and kinds [I].

**Skills from experience:**
- Voyager verified skills before adding them [R].
- In 2026 there is a wave of papers (CODESKILL, SkillRL, Trace2Skill, GSkill…) [S], plus the Letta Code `/skill-creator` [R].
- Caution: one benchmark found **self-generated skills give no average gain**, while curated skills help [S, cited in arXiv 2605.25430 search summary]. daVinci-kernel admits a skill only after execution confirms the gain [S].
- Support for our "verified procedures become skills" rule: promote only on evidence (repeated confirmed use), not on authorship.

## 3. Gaps (originality candidates) vs. adopt

**Little or no prior art (candidates for originality):**
- Deterministic anchor invalidation **for agent memory entries** (rather than docs) that combines file paths, symbols, profile names, links and a **retired-term list** into one lint driven by repo diffs. drift and Copilot each cover half.
- Revision-checked (CAS) delta patches on Markdown entries in a multi-writer, multi-agent setting.
- A per-kind lifetime policy across preference/lesson/procedure/state/log, with an active→quiet→archived lifecycle and **retrieval-side enforcement** tested against the revocation failure.
- Incident → rule distillation with provenance back to observations. projectmem's "do NOT retry" list is the nearest [S].
- A portable memory-entry file spec analogous to agentskills.io.

**Adopt rather than invent:**
- Graphiti's bi-temporal vocabulary (`valid_at`, `invalid_at`, `superseded_by`) as frontmatter fields.
- ACE's per-entry helpful/harmful counters and deterministic delta merge; ACE's evidence against full rewrites.
- Copilot's rule that only a *validated* use renews an entry's life, plus vote-threshold demotion.
- Codex Phase 2 guardrails: a single-writer lock, a bounded diff since the last run, a sandboxed consolidator, and **post-hoc deterministic validation** (size, links, schema), which their bugs show is needed.
- agentskills.io SKILL.md as the output format for promoted procedures; do not invent a skill format.
- drift's lockfile/fingerprint approach for symbol anchors. drift itself (MIT, Zig, tree-sitter) could be called as a subprocess, or its design copied [I].

## 4. Build vs adopt

- **Basic Memory** is the nearest storage engine: Markdown + frontmatter as the truth, an SQLite index, MCP, active development. We would lose lifecycle, anchors, revisions, feedback and kinds. Its capture model is manual, and **AGPL-3.0** is a real constraint for a platform-embedded engine.
- **Graphiti** has the best staleness semantics, but needs a graph DB and multiple LLM calls per write, and its store is not plain files or git. That breaks our "no LLM reads the whole store", cost and portability goals.
- **Mem0** is now ADD-only and vector-DB-bound. That is the opposite of cultivation.
- **Letta / Letta Code** works only as part of the Letta harness and server.
- **Cognee and LangMem** are DB/framework-bound.
- **ACE** is a research playbook, not a store.
- **Copilot, Claude Code, Codex and Devin** are closed or host-bound.

**Verdict [I]:** no OSS project can serve as the engine without discarding most of (1), (3), (4), (6) and (8). Build the engine, a small spec + CLI/library + MCP server over git-versioned Markdown. Import the semantics listed in §3. Treat drift and the agentskills validator as optional external tools. Benchmark against the Memora/MemStrata stale-fact protocols and the 2609.08258 revocation test.

## 5. Sources (accessed 2026-11-06)

- Mem0: https://github.com/mem0ai/mem0 · https://mem0.ai/blog/mem0-the-token-efficient-memory-algorithm · https://docs.mem0.ai/migration/oss-v2-to-v3 · https://arxiv.org/abs/2504.19413 · https://pypi.org/project/mem0ai/ · OpenMemory removal: https://github.com/mem0ai/mem0/issues/6078
- Graphiti/Zep: https://github.com/getzep/graphiti · https://help.getzep.com/graphiti/getting-started/overview · https://arxiv.org/abs/2501.13956 · https://github.com/getzep/graphiti/issues/1299 · https://github.com/getzep/graphiti/issues/2074 · https://caura.ai/blog/graphiti-alternatives
- Letta: https://www.letta.com/blog/sleep-time-compute/ · https://github.com/letta-ai/letta · https://github.com/letta-ai/letta-code · https://forum.letta.com/t/sleeptime-agents-for-memory-consolidation-best-practices-guide/154
- LangMem: https://github.com/langchain-ai/langmem · https://langchain-ai.github.io/langmem/background_quickstart/ · https://langchain-ai.github.io/langmem/guides/delayed_processing/
- Cognee: https://github.com/topoteretes/cognee · https://docs.cognee.ai/changelog
- A-MEM: https://arxiv.org/abs/2502.12110 · https://github.com/WujiangXu/A-mem
- ACE: https://arxiv.org/abs/2510.04618 (HTML v3) · https://github.com/ace-agent/ace · https://github.com/DannyMac180/ACE
- MemoryBank: https://arxiv.org/abs/2305.10250 · Generative Agents https://github.com/joonspk-research/generative_agents · Reflexion https://github.com/noahshinn/reflexion · Voyager https://github.com/MineDojo/Voyager
- Supermemory: https://supermemory.ai/docs/concepts/graph-memory · https://supermemory.ai/docs/api-reference/content-management/update-a-memory-creates-new-version · https://lin-guanguo.github.io/llm-memory-research/supermemory.research/
- Basic Memory: https://github.com/basicmachines-co/basic-memory · https://akitaonrails.com/en/2026/05/23/i-built-memory-system-for-coding-agents-ai-memory/
- Copilot Memory: https://docs.github.com/en/copilot/concepts/agents/copilot-memory · https://github.blog/changelog/2026-01-15-agentic-memory-for-github-copilot-is-in-public-preview/ · https://github.blog/changelog/2026-03-04-copilot-memory-now-on-by-default-for-pro-and-pro-users-in-public-preview/
- Claude Code: https://code.claude.com/docs/en/memory · auto-dream (secondary): https://wmedia.es/en/tips/claude-code-auto-dream-memory-consolidation · Memory tool: https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool
- Codex: https://github.com/openai/codex/issues/48679 · https://github.com/openai/codex/issues/57091 · https://github.com/openai/codex/pull/43813 · https://codex.danielvaughan.com/2026/04/18/codex-built-in-memory-system-deep-dive/
- ChatGPT memory FAQ: https://help.openai.com/en/articles/8590148-memory-in-chatgpt-remembering-what-you-chat-about
- Cursor: https://forum.cursor.com/t/are-my-memories-gone/144057 · Windsurf: https://docs.windsurf.com/plugins/cascade/memories · Devin: https://docs.devin.ai/product-guides/knowledge · Cline: https://docs.cline.bot/best-practices/memory-bank
- Coding-agent OSS: https://github.com/rohitg00/agentmemory · https://github.com/riponcm/projectmem · https://github.com/gastownhall/beads · https://github.com/zilliztech/memsearch · https://github.com/akitaonrails/ai-memory
- Doc drift: https://github.com/fiberplane/drift · https://fiberplane.com/blog/drift-documentation-linter/ · https://github.com/mgkvibez/doc-drift · https://repowise.dev/blog/comparisons/best-documentation-drift-detection-tools · https://swimm.io/blog/sync-dont-sink-why-we-built-swimm-for-dev-teams
- Skills: https://agentskills.io/specification · https://github.com/agentskills/agentskills · https://arxiv.org/abs/2605.25430 · https://arxiv.org/pdf/2608.03392
- Staleness/revocation studies: https://arxiv.org/abs/2609.08258 · https://arxiv.org/abs/2606.26511 · https://arxiv.org/pdf/2606.15903 · https://dev.to/shaan_gs/your-agents-memory-is-quietly-going-stale-4bbf (Memora summary) · https://arxiv.org/html/2607.27080v1

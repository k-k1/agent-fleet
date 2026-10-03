# 0108. Agent memory owned by AF: one store per user and project, written and read by every kind through the af MCP

English | [日本語](0108-af-owned-agent-memory.ja.md)

- Status: **proposed** (2026-10-03). Nothing is built yet. The figures below were measured on one
  workspace on 2026-10-03; the claude settings named in decision 6 were found as strings in the
  Claude Code 2.1.288 binary and **their behaviour is not measured**.
- Tracking: #1569
- Related: [0022](0022-agent-memory-management.md) (memory history in a bare repo — this record's
  safety net) / [0042](0042-user-instructions.md) (the distributor this record reuses, and the
  human-only layer it must not blur) / [0031](0031-mcp-registry.md) (how the af MCP reaches each
  kind) / #1558 (cross-session search) / #1559 (background memory and skill review)

## Context

What an agent learns while working survives only in the memory of the CLI that learned it. Of the
kinds AF runs, two keep a local memory ([0022](0022-agent-memory-management.md) Context): claude's
auto-memory (on by default) and codex's memories (off by default). opencode, agy and kiro have none,
and copilot and cursor keep theirs on the vendor's server. A member who switches kind mid-task, hands
a session off to another kind, or starts a child of a different kind loses everything the first
agent recorded.

The obvious fix — hand claude's memory to the other kinds — makes one vendor's private format and
location the fleet's source of truth. Its format can change with any Claude Code release, its
contents are written for claude ("the claude TUI does X"), and only claude can write to it.

Two facts shape the design, both measured on 2026-10-03 in one workspace for this repository's
project: the claude memory index (`MEMORY.md`) is **25,399 bytes** and the directory holds **469
files, 2.7 MB**. Pushing that into every session's instructions is a per-turn cost no one would
accept; it has to be fetched on demand.

AF already has the two halves of the machinery. [0042](0042-user-instructions.md) distributes one
AF-owned body of text to every kind's instruction layer (cursor excepted, decision 2), and
[0022](0022-agent-memory-management.md) versions memory directories in a bare git repo with
rollback and bundle transfer. What is missing is a store AF owns that agents may write.

## Decisions

1. **AF owns a memory store, separate from user instructions.** User instructions are written by the
   person and are authoritative; [0042](0042-user-instructions.md) decision 8 forbids agents from
   writing them, and that stays. AF memory is written by agents and is advisory. They live in
   different files, are distributed as different blocks, and the policy text tells agents which is
   which. Merging them would void 0042 decision 8.
2. **Scope is user × project.** A project is identified the way claude's slug does it: a worktree
   belongs to its parent clone's project, so every session on the same repository shares one
   memory. A user-wide scope exists for facts about the person rather than a repository.
3. **The format is one Markdown file per memory with frontmatter** (`name`, `description`, `type`,
   plus AF's `author_kind`, `author_session`, `created`, `updated`, and an optional `kinds` list
   when a memory only applies to some kinds). It is the shape claude's memory already uses, so an
   import is a copy plus fields, and a person can read the store without a tool.
4. **Every kind reads and writes through af MCP tools** — `memory_search`, `memory_read`,
   `memory_save` (create or update), `memory_forget`. MCP is the one surface that reaches all kinds,
   cursor included. Each write records which kind and session wrote it, and goes into the
   [0022](0022-agent-memory-management.md) history as its own commit, so any write can be traced
   and rolled back.
5. **What is distributed is a short index, not the store.** The 0042 distributor adds an AF-memory
   block to each kind's instruction layer: that the tools exist, when to use them, and at most a
   small fixed number of the most relevant entries' one-line hooks, under a byte cap of its own
   (0042 decision 7's reasoning: the cap is about per-session cost). The bodies are fetched with
   `memory_read` / `memory_search`. cursor, which has no local instruction layer, gets the tools
   only; their descriptions carry the same guidance.
6. **claude's own auto-memory is reconciled in two steps.**
   - Step 1: AF imports claude's memory into the AF store one way (claude → AF), on the same
     trigger 0022 already uses, so the 469 existing files seed the store and claude keeps working
     unchanged.
   - Step 2 (decided after measuring): either point claude's auto-memory at the AF store
     (`autoMemoryDirectory`) so there is one directory and claude writes it natively, or switch it
     off (`autoMemoryEnabled` / `CLAUDE_CODE_DISABLE_AUTO_MEMORY`) and let claude use the MCP tools
     like every other kind. Both names come from the 2.1.288 binary; neither is measured.
7. **A memory is evidence, not an order.** The read tools' descriptions say so, and say that a
   file, function or flag a memory names must be checked before it is relied on. A memory never
   overrides user instructions or the fleet policy.
8. **Writes are proposals by default; the member can make them direct.** A write from a session
   lands as pending and is offered in the Console for approval, because one hostile page read by
   one session would otherwise be distributed to every kind. The member may switch a project to
   direct writes. The approval path is the one #1559 needs for automated review, built once.

## Rejected

- **Share claude's memory with the other kinds as the source of truth.** A vendor's private format
  and location become the fleet's; only claude can write; the content is claude-specific.
- **Distribute the whole store into every kind's instructions.** 25 KB of index alone, every
  session, every turn.
- **An external memory service (for example Honcho).** Conversations leave the workspace, the
  service runs its own LLM over them, and self-hosting needs Docker, which a workspace does not
  have. It stays a member's own choice through Settings → MCP.
- **Let agents write user instructions instead.** Voids 0042 decision 8.
- **Per-kind stores synchronised with each other.** N formats, N writers, and conflicts on every
  edit; one store with one writer path is simpler.

## Consequences

- Knowledge survives switching kind, handing off and spawning children of another kind.
- The kinds 0022 found with no local memory (opencode, agy, kiro) gain one. lcpp and muse came after
  0022's survey and were not surveyed for memory; they get the same tools.
- Every memory has an author and a history, so a bad lesson can be found and rolled back.
- A new failure mode: a poisoned memory reaches every kind. Decision 8 is the mitigation and must
  ship with the write tool, not after it.
- Two memories coexist for claude until step 2 of decision 6 is decided.

## Open questions (decide after measuring)

1. Where the store lives. Under the claude-specific mount beside `af-memory.git` (survives
   everything, one workspace) or in the CP's Postgres (spans workspaces, and could share an index
   with #1558's search). The first is the cheaper start; the choice decides whether a member's
   memory follows them to another workspace.
2. What `autoMemoryDirectory` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY` actually do on the shipped
   Claude Code: whether the directory setting moves the index and the files, and whether claude
   still loads the index into its prompt from there.
3. How "most relevant" is chosen for the distributed index (recency, project, `kinds`), and the
   byte cap.
4. Whether codex's `external_agent_memory_import`
   ([0022](0022-agent-memory-management.md) decision 6) makes a native codex route worth having
   beside the MCP tools.

## Phases

- **P1** — the store, the four MCP tools with authorship and history, the claude → AF import.
  Writes pending by default, with a minimal approval list in the Console.
- **P2** — the distributed index block through the 0042 distributor.
- **P3** — decision 6 step 2 after measuring; tie-in with #1559 (automated review) and #1558
  (search).

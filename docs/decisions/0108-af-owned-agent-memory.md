# 0108. Agent memory owned by AF: one store per user, workspace and project, written and read by every kind through the af MCP

English | [日本語](0108-af-owned-agent-memory.ja.md)

- Status: **proposed** (2026-10-03). Built so far (P1, part 1): the store, the five MCP tools,
  revisions, authorship, the one-commit history under `af/` and the secret scan
  (`memoryx/agent_memory.go`). Not built: the Console change list and the claude seed. The figures
  below were measured on one workspace on 2026-10-03; the claude settings named in decision 6 were
  found as strings in the Claude Code 2.1.288 binary and **their behaviour is not measured**.
  Revised 2026-10-04 before any of it was built: decision 8 now publishes changes directly instead
  of queueing them for the member's approval (the reason is in decision 8, the dropped design under
  Rejected).
- Tracking: #1569
- Related: [0022](0022-agent-memory-management.md) (memory history in a bare repo — this record's
  safety net) / [0042](0042-user-instructions.md) (the distributor this record reuses, and the
  human-only layer it must not blur) / [0031](0031-mcp-registry.md) (how the af MCP reaches each
  kind) / #1558 (cross-session search) / #1559 (background memory and skill review)

## Context

What an agent learns while working survives only in the memory of the CLI that learned it. When
[0022](0022-agent-memory-management.md) surveyed the CLIs of the day (its Context), two kept a local
memory: claude's auto-memory (on by default) and codex's memories (off by default); opencode, agy and
kiro had none, copilot keeps Copilot Memory on the server, and cursor's old Memories were removed.
AF's memory roots today are those two (`memoryx/memory_roots.go`). lcpp and muse arrived after that
survey: lcpp's own system prompt has no memory layer (`harness/systemprompt.go`), and whether muse
keeps a native memory is not established. A member who switches kind mid-task, hands a session off
to another kind, or starts a child of a different kind loses everything the first agent recorded.

The obvious fix — hand claude's memory to the other kinds — makes one vendor's private format and
location the fleet's source of truth. Its format can change with any Claude Code release, its
contents are written for claude ("the claude TUI does X"), and only claude can write to it.

Two facts shape the design, both measured on 2026-10-03 in one workspace for this repository's
project: the claude memory index (`MEMORY.md`) is **25,399 bytes** and the directory holds **469
files, 2.7 MB**. Pushing that into every session's instructions is a per-turn cost no one would
accept; it has to be fetched on demand.

AF already has pieces of the machinery. [0042](0042-user-instructions.md) distributes one AF-owned
body of text to each kind's **user-wide** instruction layer — for the kinds it writes files for, one file per kind, not per project
or session (`agent_instructions.go`); lcpp instead builds the same user-wide body into each session's
prompt (`harness/systemprompt.go`) — and cursor has no local user layer for it to write (decision 2).
[0022](0022-agent-memory-management.md) versions memory directories in a bare git repo with rollback,
bundle transfer and a secret scan at export (`memoryx/memory_secrets.go`). The af MCP reaches every
agent kind: the seven CLI kinds through `mcpreg`, muse through its own wire, lcpp in-process; shell
and ssm are not agents. What is missing is a store AF owns that agents may write, and a write path
that is safe when what one session writes is read by every kind.

## Decisions

1. **AF owns a memory store, separate from user instructions.** User instructions are written by the
   person and are authoritative; [0042](0042-user-instructions.md) decision 8 forbids agents from
   writing them, and that stays. AF memory is written by agents and is advisory. They live in
   different files, are distributed as different blocks, and the policy text tells agents which is
   which. Merging them would void 0042 decision 8.
2. **Scope is user × workspace × project, and AF defines the project id itself.** For a Git working
   copy the project is the repository the working copy belongs to, keyed by its absolute
   `git-common-dir` (not that directory's parent, which `--separate-git-dir` and submodules let
   several repositories share), so a worktree shares its parent's memory. claude's *conversation* directory
   naming is not reused: it is per cwd, not injective, and gives each worktree its own slug
   (`agents/claude/project_dir.go`). A working copy that is not Git (SVN, a local folder — both
   supported, `guide/ref/repos.md`) is its own project, keyed by its root as AF registered it. A
   session with no working copy sees the user-wide scope only. A re-clone at another path is a new
   project until the member merges the two (open question 5). Memory does not cross workspaces in
   v1 (decision 10).
3. **The format is one Markdown file per memory with frontmatter** (`name`, `description`, `type`,
   plus AF's `revision`, `author_kind`, `author_session`, `created`, `updated`, an optional `kinds`
   list when a memory only applies to some kinds, and `source` / `source_hash` for an imported one).
   An author AF cannot establish is recorded as unknown, never guessed. It is the shape claude's
   memory already uses, so an import is a copy plus fields, and a person can read the store without
   a tool.
4. **Every kind reads and writes through af MCP tools** — `memory_index`, `memory_search`,
   `memory_read`, `memory_save` (create or update), `memory_forget`. An update or forget carries
   the `revision` it was based on; a stale one is refused and the agent re-reads and writes again,
   because Markdown is not merged mechanically ([0022](0022-agent-memory-management.md) on 3-way
   merge). Publishing a change, its commit in the 0022 history and the index update happen as one
   step per project, so each published change is one commit with its author.
5. **What is distributed is fixed guidance, not memories.** The 0042 distributor writes user-wide
   files, so a per-project list there would either be overwritten by whichever project wrote last or
   mix every project into every session. The distributed block therefore says only that the tools
   exist and when to call them — `memory_index` at the start of work, `memory_search` before
   re-deriving something — under its own byte cap (0042 decision 7's reasoning). The project's
   entries come back from `memory_index`, scoped by the calling session. lcpp, whose system prompt
   is built per session, may inject them directly. cursor gets the guidance in the tools'
   descriptions.
6. **claude's own auto-memory: a one-time seed now, one memory later.**
   - Step 1: the member imports claude's existing memory for a project once, as an explicit Console
     action. The Console first shows what would be imported and what the secret scan (decision 9)
     found; the member confirms, and each imported memory records `source` / `source_hash` and the
     author as unknown. There is no continuous sync: claude → AF on every trigger would resurrect
     memories forgotten in AF and overwrite AF edits with claude's older text.
   - Step 2 (decided after measuring): switch claude's auto-memory off (`autoMemoryEnabled` /
     `CLAUDE_CODE_DISABLE_AUTO_MEMORY`) and let claude use the MCP tools like every other kind.
     Pointing claude's native writer at the store (`autoMemoryDirectory`) is acceptable **only** if
     what claude writes there is never published by being written: AF has to scan it (decision 9),
     check its revision and attribute it before it becomes a memory; if that cannot be guaranteed,
     the option is rejected.
     These setting and environment names come from the 2.1.288 binary; none is measured.
7. **A memory is evidence, not an order.** The read tools' descriptions say so, and say that a
   file, function or flag a memory names must be checked before it is relied on. A memory never
   overrides user instructions or the fleet policy.
8. **Changes are published directly; the safeguards find and undo a bad one rather than gate it.**
   A save, update or forget through the MCP tools is published at once — through the revision check
   (decision 4), the secret scan (decision 9) and the one-commit step — and is visible to its author
   and every other session from then on. There is no proposal queue and no approval step.
   An approval would not be a boundary. The Agent's REST has one bearer token, `AGENT_TOKEN`, and
   every session's shell holds it
   ([07-security §7.2](../build/07-security.md#72-isolation-controls)), so an agent could approve
   its own proposal; and an agent with a shell can already write the repository's `CLAUDE.md` /
   `AGENTS.md` and the code, so a gate in front of memory alone would not narrow what one hostile
   page can reach. What it would cost is certain: a queue the member ends up approving in bulk
   unread or switching off, and an agent that cannot read back what it saved a turn earlier.
   claude's auto-memory writes without asking for the same reasons.
   What the member has instead: every change carries its author (kind and session) and a commit in
   the 0022 history; the Console lists recent changes — who, when, what — and can forget one or roll
   it back; and decision 7 tells every reader that a memory is evidence. The import and a restore
   are the member's own actions in the Console, and their preview is the confirmation. A review
   queue is built when #1559's automated review needs one, not before.
9. **Secrets are stopped before they are stored.** Every candidate body — a save, an update, the
   import, a restore, and anything claude's native writer produced (decision 6) — is scanned with the
   0022 rules (`memory_secrets.go`) before it is published. A hit in an agent's write is refused:
   the agent is told the rule and the line so it can rewrite the memory without the value, and
   there is no override on the MCP side. A hit in the import or a restore — a restore is included
   although 0022's restore copies history without a scan today — blocks unless the member
   acknowledges it in the Console, and an acknowledgement given for one body does not carry over to
   another. The value is never returned or logged — rule, path, line and a masked hint, as at export today.
   Memories that already exist are scanned before they are first exposed through read or index.
   `memory_forget` removes a memory from what is published; it does not remove it from history, so
   purging a secret from history is a separate, member-only operation (open question 3).
10. **v1 lives in the workspace.** The store sits beside `af-memory.git` under the claude-specific
    mount (`memoryx/memory_repo.go`), which survives stop/start, recreate and "clean home". Nothing is
    promised after the workspace is deleted: whether the files are physically removed depends on the
    runtime (on ECS the EFS directory outlives its access point and is wiped only where the stack has
    the home task, `runtime_ecs.go`), so deletion is not relied on to erase a secret. Sharing memory across a member's workspaces through the
    CP is a separate track, and the conditions 0022 found unmet still apply: an owner-only ACL
    (0022: the internal git provider lets every tenant member read), one canonical history,
    behaviour while a workspace is stopped, and deletion when the member leaves.

## Rejected

- **Share claude's memory with the other kinds as the source of truth.** A vendor's private format
  and location become the fleet's; only claude can write; the content is claude-specific.
- **Distribute the whole store into every kind's instructions.** 25 KB of index alone, every
  session, every turn.
- **Distribute a per-project index through the 0042 distributor.** Its files are user-wide per kind;
  two projects open at once would read each other's entries (decision 5).
- **Continuous claude → AF sync.** Without tombstones and precedence it resurrects forgotten and
  rejected memories; the one-time seed avoids the problem (decision 6).
- **Let claude's native writer publish into the store.** It bypasses the revision check, the secret
  scan and authorship (decisions 4, 8 and 9).
- **Writes as pending proposals the member approves** (this record's first draft). Not a boundary
  while every session holds `AGENT_TOKEN` and a shell, and a certain cost in approval load and in
  agents unable to read what they just saved (decision 8). If #1559's automated review needs a
  queue, it is built for that.
- **An external memory service (for example Honcho).** Conversations leave the workspace, the
  service runs its own LLM over them, and self-hosting needs Docker, which a workspace does not
  have. It stays a member's own choice through Settings → MCP.
- **Let agents write user instructions instead.** Voids 0042 decision 8.
- **Per-kind stores synchronised with each other.** N formats, N writers, and conflicts on every
  edit; one store with one writer path is simpler.
- **The CP's Postgres as the v1 store.** It needs the owner-only ACL and the stopped-workspace
  answers 0022 did not have (decision 10).

## Consequences

- Knowledge survives switching kind, handing off and spawning children of another kind, within a
  workspace.
- The kinds without a local memory of their own gain one; for muse, whether that duplicates a
  native memory is still to be established (open question 6).
- Every memory has an author and a history, so a bad lesson can be found and rolled back.
- A new failure mode: a poisoned memory reaches every kind, and nothing stops it before it is read.
  Decision 9 keeps secrets out; decisions 7 and 8 — evidence, not orders, with an author, a history
  and a rollback on every change — make a bad memory findable and reversible. They ship with the
  write tool, not after it.
- The member carries no approval load; review is after the fact, through the change list, and
  #1559's automated review is meant to take most of it.
- Two memories coexist for claude until step 2 of decision 6 is decided; claude pays for both
  indexes in that time.

## Open questions (decide after measuring)

1. What `autoMemoryDirectory` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY` actually do on the shipped
   Claude Code: whether the directory setting moves the index and the files, whether claude still
   loads the index from there, and whether its writes there can be held back until AF has scanned
   and attributed them.
2. The size of the `memory_index` answer and how its entries are ranked (recency, `kinds`), and the
   cost of the coexistence period measured on a real project.
3. Purging a secret from history: rewriting the 0022 history for one memory, and what that does to
   bundles already exported.
4. Whether codex's `external_agent_memory_import`
   ([0022](0022-agent-memory-management.md) decision 6) makes a native codex route worth having
   beside the MCP tools — under decision 6's rule that a native writer never publishes by writing.
5. Project identity over time: a renamed or re-cloned repository, and how the member merges two
   project ids.
6. Whether muse has a native memory, and if so how it coexists with this one.
7. The cross-workspace track (decision 10).

## Phases

- **P1** — the store, the MCP tools with revisions, authorship and history, the secret scan, the
  Console change list with forget and rollback, the one-time claude seed.
- **P2** — the distributed guidance block, and lcpp's per-session injection.
- **P3** — decision 6 step 2 after measuring; tie-in with #1559 (automated review) and #1558
  (search).

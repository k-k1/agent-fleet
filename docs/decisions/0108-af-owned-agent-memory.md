# 0108. Agent memory owned by AF: one store per user, workspace and project, written and read by every kind through the af MCP

English | [日本語](0108-af-owned-agent-memory.ja.md)

- Status: **accepted** (2026-10-05; proposed 2026-10-03). Built, in stages: P1 (the store, tools and Console list)
  first released in v0.28.0, with the claude seed and index budget in v0.29.0; P2 (#1733), P3 / decision 6 step 2
  (#1734, #1916), the write-back to claude's native memory (#1914), usage ranking and pins (#1703) and size and
  name limits (#1921) in v0.30.0; the automatic import and write-back on switch (#1972, closed 2026-10-14),
  purge and project move in v0.31.0; and the answers to open questions 3, 5 and 6 (#1982, #1984, #1986; see the notes at
  the end). Not built: the codex import. Follow-ups: #1950 (codex import, open), #1559 (background review,
  open; see [0117](0117-background-memory-skill-review.md)). The original status text follows, as of 2026-10-03.
  Built so far (P1 without the claude seed): the store, the
  five MCP tools, revisions, authorship, the one-commit history under `af/` and the secret scan
  (`memoryx/agent_memory.go`); the Console change list with revert and forget
  (`memoryx/agent_memory_changes.go`, Settings → Agent memory); the one-time claude import
  (`memoryx/agent_memory_claude_import.go`, decision 6 step 1, and the `af-memory` command). The figures
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
   Limits come in two tiers (#1921). What the **store accepts** is generous, so nothing claude's own
   memory holds is refused: a name of lower-case letters, digits, `-`, `_` and `.`, 1–64 characters,
   not starting with `.` or `-`, no `..`; a one-line description of up to 2,000 characters (counted
   in characters, not bytes); a body of up to 200 KiB; no limit on a line's length. What the
   **authoring guidance** asks for mirrors claude's own lints and only warns — the save still
   succeeds and `memory_save` returns the warning: one fact per file, a description of about 150
   characters (claude lints above 300 characters), and a body under 4,096 bytes, because claude's
   recall shows another session only the first 4,096 bytes / 200 lines of a memory file. Measured
   on 598 real claude memory files in 7 projects: 117 names use an underscore (the old pattern
   refused them), 15 descriptions exceed 300 bytes (the longest is 1,800 characters), 2 bodies
   exceed 64 KiB (the largest is 174,004 bytes), 136 files exceed 4,096 bytes. claude's synced
   store caps a document at 102,400 bytes and a path at 1,024 bytes / 20 segments, but local files
   have no cap, so the store limit follows the local data rather than the sync cap.
4. **Every kind reads and writes through af MCP tools** — `memory_index`, `memory_search`,
   `memory_read`, `memory_save` (create or update), `memory_forget`. An update or forget carries
   the `revision` it was based on; a stale one is refused and the agent re-reads and writes again,
   because Markdown is not merged mechanically ([0022](0022-agent-memory-management.md) on 3-way
   merge). Publishing a change, its commit in the 0022 history and the index update happen as one
   step per project, so each published change is one commit with its author.
   The tools are **off by default**, behind a per-user switch (Settings → Agent memory, beside the change list; ui-prefs
   `agentMemory`): what one session saves is read by every kind in later sessions, so a member turns
   that on knowingly rather than finding it on after an upgrade. Off means the af server is launched
   without the tools *and* the Agent's tool routes refuse, because the routes answer anything that
   holds `AGENT_TOKEN`. The Console's change list, diff and revert stay available while it is off,
   so what was written while it was on can still be reviewed and undone. The assistant's snapshot
   tool (0022) reads claude's and codex's history only, never `af/`.
5. **What is distributed is fixed guidance, not memories.** The 0042 distributor writes user-wide
   files, so a per-project list there would either be overwritten by whichever project wrote last or
   mix every project into every session. The distributed block therefore says only that the tools
   exist and when to call them — `memory_index` at the start of work, `memory_search` before
   re-deriving something — under its own byte cap (0042 decision 7's reasoning). The project's
   entries come back from `memory_index`, scoped by the calling session. lcpp, whose system prompt
   is built per session, may inject them directly. cursor gets the guidance in the tools'
   descriptions.
   `memory_index` is partial by design (#1702; usage-based ranking and pinned memories are
   follow-ups in #1703, relevance ranking stays with #1558 [now #1981, see the Note of 2026-10-11]). The Agent bounds it, so every client
   gets the same answer:
   - The described part is limited to a byte budget of rendered lines (default 24 KiB; the
     optional `budget` argument is clamped to 4–64 KiB), in rank order: `feedback` and `user`
     first, then the rest, newer first within a tier. A line carries the description cut to 150
     characters; `memory_read` and `memory_search` keep the full text.
   - What did not fit is listed as names only, within a separate 8 KiB: names cut to 32 bytes
     with "…" (a prefix), grouped by first hyphen segment (`adr-{0072-…,0079-…}`). Past that, "and
     N more (use memory_search)".
   - The guidance says the index is partial and to `memory_search` with the task's keywords
     before re-deriving something. Today that is the `memory_index` tool description; the
     distributed block itself is not built yet and must carry the same wording within its cap.
   Measured on the 473 imported claude memories: 111 KB of full lines became 117 described
   lines (24.4 KB) plus a 8.2 KB tail naming 355 more; one was left to the count.
   The cut was 80 characters, with no basis outside this budget (claude's own index guidance is a
   line under about 200 characters), and #1921 re-measured it on the 473 imported memories of this
   project (described lines / names in the tail, and how many descriptions the cut shortens):
   80 → 114 / 359, 218 cut; 120 → 104 / 369, 53 cut; 150 → 103 / 370, 26 cut; 200 → 102 / 371,
   2 cut. Going from 80 to 150 costs 11 described lines and spares 192 descriptions; past 150 the
   line count barely moves, so 150 it is. The other projects fit whole at any of these values.
6. **claude's own auto-memory: a one-time seed now, one memory later.**
   - Step 1: the member imports claude's existing memory for a project, as an explicit Console
     action or with `af-memory import`. It reads `<claude config>/projects/<slug>/memory/*.md`
     (claude's `MEMORY.md` index excluded); a slug cannot be decoded, so each working copy under
     `~/repos` is mapped forward to its key and a slug that no working copy, or two projects,
     claim is listed but not importable. The Console first shows what would be imported and what
     the secret scan (decision 9) found; the member confirms, and each imported memory records
     `source` / `source_hash` and the author as unknown, committed once per memory with the
     operation `import` and the member as the commit's author. Running it again is safe: a
     memory whose claude file is unchanged is skipped, one whose claude file is **newer** than the
     AF memory (and differs) is updated — this overwrites an edit made in AF after the import,
     which the member chose — and one that was ever forgotten or whose import was reverted is
     **never brought back**. A claude description is imported whole, never cut, and the body is not
     prefixed (#1921); a file is listed with its reason and not imported when its name is not a
     valid name, its description exceeds 2,000 characters (`bad_description`) or its body exceeds
     200 KiB (`too_large`). A memory that an earlier import saved with a shortened description (the
     full text prepended to the body) is refreshed from claude's file (`refresh_shortened`) when
     the recorded `source_hash` equals the claude file's hash and AF's copy has not been touched
     since: the memory's latest commit in the history is the import and the file is byte-identical
     to it. A save, a pin, a revert or a hand edit makes the copy differ, and then it is left alone.
     Applying needs the switch on (decision 4's setting); the preview and
     the command's `--dry-run` work while it is off. There is no continuous sync: claude → AF on
     every trigger would resurrect memories forgotten in AF and overwrite AF edits with claude's
     older text.
   - Step 2 (decided 2026-10-08, #1734): while the switch is on, every claude launch carries
     `autoMemoryEnabled:false` in its one `--settings` JSON (the flag layer beats the member's own
     settings; nothing is written to `settings.json`, so switching off needs no cleanup — the next
     launch just omits it). claude then loads no `MEMORY.md` and writes no memory files, and uses the
     MCP tools like every other kind. The environment variable is not used:
     `CLAUDE_CODE_DISABLE_AUTO_MEMORY=0` forces auto-memory back on.
     Pointing claude's native writer at the store (`autoMemoryDirectory`) is **rejected**: claude
     writes there with its file tools, so a memory would be published by being written, with no
     scan (decision 9), revision check or attribution, and claude offers no way to hold the write.
     Measured on the 2.1.293 binary (static; no live probe, which needs a claude login): auto-memory
     off means neither read nor write; the directory setting moves both. Cost for this project:
     claude loads 25,000 B of its 26,817 B `MEMORY.md` (about 12–14k tokens) plus about 3 KB of its
     memory prompt at every start, while AF's guidance is 760 B and `memory_index` about 32 KB
     (24 KB described plus 8 KB names), so both together cost about 30k tokens at start. Over two
     weeks of 767 sessions claude made 52 native memory Writes and 15 Edits against 2
     `memory_save` calls. Consequences: only launches after the change are affected; native
     memories written after the last import are not read until the member imports again, so the
     Console and the guide say to turn the switch on and import once more (applying needs the switch
     on; preview works while off) before starting new claude sessions; switching off
     returns claude to its own memory and what was saved in AF does not appear there (one way,
     until the explicit write-back of step 3 fills the gap).
   - Step 3 (#1914): the reverse copy, AF → claude's native memory, is allowed as an **explicit,
     previewed, one-shot** action per project (Console "Write back to Claude Code", or
     `af-memory export`), never continuous. It is safe because while AF memory is on claude's native
     writer is off (#1734), so nothing writes there concurrently. Conflicts are never overwritten
     silently, a snapshot of claude's memory (0022) is taken first, and codex is deferred to #1683.
     See the note at the end.
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
9. **Secrets are stopped before they are stored.** A long line is not refused (#1921); the scanner
   runs every rule over the whole line, so a value past 4 KiB is found as well as one at the start.
   Every candidate body — a save, an update, the
   import, a restore, and anything claude's native writer produced (decision 6) — is scanned with the
   0022 rules (`memory_secrets.go`) before it is published. A hit in an agent's write is refused:
   the agent is told the rule and the line so it can rewrite the memory without the value, and
   there is no override on the MCP side. A hit in the claude import is skipped and listed with its rule, field, line and masked hint; there
   is no acknowledgement, as with claude's own team-memory sync: the member fixes the claude file and
   imports again. A hit in a restore — a restore is included
   although 0022's restore copies history without a scan today — blocks unless the member
   acknowledges it in the Console, and an acknowledgement given for one body does not carry over to
   another (this part is unchanged). The value is never returned or logged — rule, path, line and a masked hint, as at export today.
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
- claude reads one memory, AF's, while the switch is on (decision 6 step 2); the native one is
  untouched on disk and returns, without AF's memories, when the switch goes off.

## Open questions (decide after measuring)

1. ~~What `autoMemoryDirectory` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY` do~~ — answered in decision 6
   step 2 (#1734). The live check (a new claude session loads no `MEMORY.md`) was answered 2026-10-08, see #1734 and #1916.
2. The size of the `memory_index` answer and how its entries are ranked (recency, `kinds`), and the
   cost of the coexistence period measured on a real project.
3. **Answered 2026-10-14** (note "the member can purge a memory's past revisions from history", #1982).
   Purging a secret from history: rewriting the 0022 history for one memory, and what that does to
   bundles already exported.
4. Whether codex's `external_agent_memory_import`
   ([0022](0022-agent-memory-management.md) decision 6) makes a native codex route worth having
   beside the MCP tools — under decision 6's rule that a native writer never publishes by writing.
5. **Answered 2026-10-14** (note "moving the memories of an orphaned project", #1984).
   Project identity over time: a renamed or re-cloned repository, and how the member merges two
   project ids.
6. **Answered 2026-10-11** (note "muse has a native memory", #1986). Whether muse has a native memory, and if so how it coexists with this one.
7. The cross-workspace track (decision 10).

## Phases

- **P1** — the store, the MCP tools with revisions, authorship and history, the secret scan, the
  Console change list with forget and rollback, the one-time claude seed.
- **P2** — the distributed guidance block, and lcpp's per-session injection.
- **P3** — decision 6 step 2, decided after measuring (#1734, #1916; the live check was answered 2026-10-08); tie-in with #1559 (automated review) and #1558
  (search).

## Note (2026-10-08): the guidance block (P2, #1733)

Built: the 0042 distributor writes the fixed guidance (`userinstr.MemoryGuide`, cap
`MemoryGuideMaxBytes`, pinned by a test) while the switch (ui-prefs `agentMemory`, Settings → Agent
memory) is on and removes it when off. It is a `memory-guide` marker block beside `user-notes` in
claude's `CLAUDE.md` and in the codex, agy, muse and opencode `AGENTS.md`, and an AF-owned file for
copilot and kiro; saving the switch reconciles at once. lcpp's system prompt carries the guidance and
the project's `memory_index` at the 4 KiB budget floor, per turn. cursor was not changed: the tool
descriptions already say when to call (pinned by a test). Not done here: usage-based ranking (#1703).

## Note (2026-10-08): usage ranking and pins (#1703)

`memory_index` now ranks pinned first, then the type tier, then use count, then recency. A use is a
`memory_read` or a returned search hit; the Console's list is not one. The count lives in a sidecar,
`<scope dir>/.usage/<name>`, one byte appended per use (O_APPEND, so no read-modify-write and no lost
use; capped at 4096; not committed, not exported). It is cleared when the memory is forgotten or
re-created. A pin is `pinned: true` in the frontmatter, set from the Console (no MCP tool pins; as with
every Agent route, a shell in the same workspace is not kept out, and the author is recorded as the member by route)
(`POST /agents/memory/entries/pin`, op `pin`, audited as `memory.entry.pin`); it changes neither the
revision nor `updated`, and an agent's save carries it forward. The byte budget still holds: pins
fill the described part first, and pins that do not fit fall to the names-only tail and are counted
in `pinnedOmitted`, which `memory_index` prints. Search relevance stays with #1558.

## Note (2026-10-08): write-back to claude's native memory (#1914)

Turning the switch off returns claude to its own memory, which lacks what was learned through AF
meanwhile. `memoryx/agent_memory_claude_export.go` copies a project's AF memory into
`<claude config>/projects/<slug>/memory/`, where the slug is the key of the project's main working
copy (`project.json` `root`). Decision 6 rejected continuous claude → AF sync; this is a different
thing: explicit, previewed (`GET /agents/memory/claude-export[/preview]`, `POST` audited as
`memory.claude_export`), and it works with the switch on or off.

- One file per project-scope memory, in claude's shape (`name`, `description`, `metadata.type`, body)
  plus `metadata.af_source` (`<project id>/<name>@<revision>`) and `metadata.af_hash` (sha256 of
  description, type and body as written). AF-only fields are not written. User-scope memories are
  not written (claude has no user-wide memory directory); a memory limited to other agent kinds is
  left out.
- Statuses: `new`, `update` (a file AF wrote, unchanged since), `unchanged`, `conflict` (a file AF
  did not write, or that changed since: kept unless the request names it; a symlink leaf is never
  replaced), `native_only` (kept, never deleted), `remove` (a file AF wrote, unchanged, whose memory
  was forgotten), `secret` (scanned again on the way out; no override). The apply carries the
  preview's token and is refused when anything moved.
- `MEMORY.md` is regenerated from AF's ranking (pins, type tier, uses, recency), then the files
  that stay, newest first, within 200 lines and 24 KiB, with a closing "N more memories" line. It
  replaces the old one; the snapshot (`pre-export`, 0022) taken before the first write makes that
  undoable. If the snapshot fails nothing is written. Every write is a temp file renamed inside a
  directory handle opened without following symlinks (below the config root, which is resolved once because it is AF's own setting and may be a link).
- A file the import copied (#1921) carries no AF marker, so it is judged by the import's evidence:
  when its hash equals the memory's recorded `source_hash`, claude's file is the original, and it is
  `unchanged` while AF's copy is still the import's, `update` once AF's copy has moved on. Only a
  file changed in claude after the import, or written by neither side, is a `conflict`. "Moved on"
  must be established from the history: when it cannot be judged (no history, a git error, a
  missing blob) the file is a `conflict` (`import_history_unknown`), never an `update`, and the
  import's refresh leaves such a memory alone. An update copies only the plain one-line
  `key: value` lines directly under the native `metadata` (unquoted key and value, 4 KiB in total;
  claude's `node_type`, `originSessionId` and `modified` appear in all 598 measured files), as
  checked text that AF writes itself. A file with anything else there (a nested value, a quoted or
  escaped key or value, too much) is not updated: it is a `conflict`
  (`native_metadata_not_carried`), and an explicit overwrite does not write those lines either.
- Import loop guard: the import reads a claude file whose `af_source` names the AF memory and whose
  text still hashes to `af_hash` as `unchanged`, although its mtime is newer than AF's update.
- codex: deferred to #1683 (its memory workspace is rewritten by its own pipeline).

## Note (2026-10-10): codex import measured as far as possible (#1683)

Decision 6 step 1 for codex is not built. A throwaway `CODEX_HOME` with `memories` on produced only
the empty scaffold (`raw_memories.md`, `phase2_workspace_diff.md`, `extensions/ad_hoc/`,
`rollout_summaries/`); the one `codex exec` run failed with 401 without a login, and consolidation
needs model calls, so no consolidated output (`MEMORY.md`) was obtained. The shape inferred from the
formats codex's own prompts ask its model to write (not observed output), the proposed
split by `# Task Group:` block, scope, naming rules and the open questions are in
[0112](0112-codex-memory-import.md). Nothing above is changed.

## Note (2026-10-11): status refresh (#1987)

What is built, checked against the closed issues: P2, the guidance block (#1733); P3 / decision 6
step 2, claude's auto-memory switched off while AF memory is on (#1734, shipped in PR #1916); usage
ranking and pins for `memory_index` (#1703); the size and name limits that make import lossless
(#1921); the write-back to claude's native memory (#1914). The live check of open question 1 passed
on a live workspace on 2026-10-08 (last comment on #1734): a new claude session loads no `MEMORY.md`.

Decision 5's "relevance ranking stays with #1558" no longer holds: #1558 closed as conversation
search (#1656, #1664) without relevance ranking for AF `memory_search`, and that work is now #1981. Not built: the codex import (#1950, after [0112](0112-codex-memory-import.md))
and automatic import on switch-on / write-back on switch-off (#1972). The other open questions are
tracked separately (#1982–#1988). The decisions above are unchanged.

## Note (2026-10-11): muse has a native memory (open question 6, #1986)

Looked at: ADR [0095](0095-muse-agent-kind.md)'s clamp table, `internal/agents/muse/program.go`,
`muse --help` / `muse exec --help`, the strings of the installed muse 1.4.4 binary, and one
`muse exec --provider echo` run in a throwaway `HOME` / `XDG_*` (no login; the user's own muse
config and credentials were not read).

Found:
- **A native memory exists.** The binary carries the tools `read_memory`, `add_memory` and
  `edit_memory`, three scopes (`personal_project`, the default; `personal`; `project`, which is
  `<repo>/.agents/memory`), a `MEMORY.md` index per scope in the same `- [Title](file.md) - hook`
  shape and note frontmatter (`name`, `description`, `type` of `user` / `feedback` / `project` /
  `reference`) as claude's, a snapshot of it injected at run start, and a system-prompt paragraph
  telling the model to save lasting preferences with `add_memory`. So with the AF switch on muse
  holds two memories and is told to write to its own, which AF never scans, versions or attributes.
- **AF's existing clamp is not the switch, but a switch exists.** `MUSE_EXPERIMENTAL_MEMORY_REMINDER=0`
  (ADR 0095) stops only the advisory memory-reminder observer, and `--disable-reminders` is the same
  family. `MUSE_EXPERIMENTAL_MEMORY=0` (a separate variable in the binary's strings) removes the
  memory itself: in an A/B on the echo provider, both arms with the reminder variable at 0 and
  `--disable-reminders`, in an isolated `HOME` / `XDG_*` under `env -i`, the default arm's durable
  `session.jsonl` has the three memory tools in `model_request_configured`, the `memory_snapshot`
  context and the "Use persistent memory" paragraph; the `MUSE_EXPERIMENTAL_MEMORY=0` arm has none of
  the three (other tools stay). No login is needed. AF's `childEnv` sets only the reminder variable,
  so the native memory is currently on under AF. Settings has `memory.destinations.*` (where a scope
  is stored) and a policy-gated `memory sync`; neither turns the memory off.
- muse's bundled `migrate` skill already reads claude's and codex's memory notes and records them with
  `add_memory`, so a user may be seeding it from those without AF involved.

Measured: the A/B above (echo provider, no login); the startup of an empty `HOME` creates no memory
directory (that run only; it does not show that none is ever created before the first write);
`--help` lists no memory option. Inferred from binary strings (not observed output): the scopes,
the `memory.destinations.*` key and the relation of the two variables. Not measured: where each
scope's store lives on disk, `add_memory` / write behaviour and import (a real model call, so a
login), and the switch under `muse serve`, which is how AF launches muse.

Follow-ups: #1990 (set `MUSE_EXPERIMENTAL_MEMORY=0` behind the AF switch, as #1734 did for claude, and verify it over `muse serve`),
#1991 (import / write-back). Nothing above is changed.

## Note (2026-10-11): `memory_search` ranks its hits (#1981)

Decision 5 left search relevance to #1558, but #1558 closed with the conversation search of
[0110](0110-past-session-search.md) only, so `memory_search` stayed a newest-first filter. It now orders
hits by matched words (more first), then by the best field of each word (name over description over
body), then pinned, read count and recency. When no memory holds every word of a multi-word query, it
answers with the ones holding some of them and marks the answer (`fallback: "any"`, plus each hit's
`matchedTerms`; the tool text says so). Matching stays case-insensitive substring, so Japanese
without spaces still works; 0110's tokenizer is bound to its FTS5 index and was not reused, and there
is no new index. On 500 synthetic memories (40 body lines each) a search takes about 30 ms warm (a
test logs it). The REST route only gains optional fields. Embeddings are not planned.

## Note (2026-10-11): muse's native memory is switched off with the switch (#1990)

While the Agent memory switch is on, every muse launch (`muse serve` for sessions, the model-list
host, and the `muse exec` of assistant chat; all take their environment from `childEnv`) now carries
`MUSE_EXPERIMENTAL_MEMORY=0`, through a `muse.NativeMemoryOff` hook wired to the switch beside
`claude.AutoMemoryOff`. An inherited value is dropped first, so the member's own environment cannot
re-enable it; with the switch off nothing is set. The `MUSE_EXPERIMENTAL_MEMORY_REMINDER=0` clamp
stays. Running hosts keep what they launched with.

Measured over `muse serve --provider echo` in a throwaway HOME/XDG, one turn per arm, reading
`model_request_configured` in the durable log: without the variable the tool list holds `read_memory`,
`add_memory`, `edit_memory`, the `memory_snapshot` context and the "Use persistent memory" paragraph;
with `=0` the tools, the snapshot and the paragraph are all gone (the only remaining mention is a
generic "do not call read_memory…" sentence in the work-stop instructions, present in both arms). The
variable was set by hand in that probe, not by AF's `childEnv`, whose output the unit tests pin. Not
done: the phase-based switch (#1972) and import of muse memory (#1991).

## Note (2026-10-11): the member can browse, search and restore memories in the Console (#1985)

Decision 8's after-the-fact view had the change list and the pin list, but no way to read a memory or
see what was forgotten. Settings > Agent memory now has a browser per scope. It reuses `entries/list`
(now with revision and author) and the changes list: a memory's newest change, when the memory no
longer exists, is the forgotten row, and its revert is the restore; forget from the list is a revert
with `forget` on the memory's newest change. One route is new, `entries/member-read`: a body for the
Console, not counted as a use, not gated by the `agentMemory` switch (like the changes list), and it
writes nothing. Search filters the list on the Console side and does not read bodies; ranked body
search stays with the tools' search (#1981).

The member-only routes (pin, revert/forget) cannot tell the Console from a session holding
`AGENT_TOKEN`: the author they record is the route, not a verified person. That is acceptable for
them because they cannot write new free text. A member create/edit route could, and would bypass
decision 4's switch, so it is not built; it waits for a member mark the Agent can verify (#1997).

## Note (2026-10-12): the browser searches bodies and forgets without the 300-change window (#1999)

Two limits of the browser note above are lifted, both with read-only routes (no new write path).
`entries/member-search` ranks the user scope and every project (or the one named by `scope` and
`project`) with the ranking of the tools' search (#1981), over names, descriptions and bodies. It is
not gated by the `agentMemory` switch, needs no calling session, counts no use (a Console search
must not feed the usage ranking of #1703), ignores the entries' `kinds` filter as the list does, and
never sees a memory whose file fails the scan: those are not loaded, so neither text nor name can
reach a hit. `entries/member-latest` returns the newest change of one memory (the same marks as the
changes list's newest row), so forget no longer needs that change to be among the 300 loaded ones.
Forget and restore still go through the existing revert route with its newest-change and
`expectRevision` checks; the resolved commit is only what the Console names there. The forgotten
list is still built from the 300 most recent changes, because nothing else enumerates memories that
no longer exist.

## Note (2026-10-12): importing muse's project-scope memory (#1991)

Decision 6 step 1 for muse: Settings > Agent memory gains **Import from muse**, and `af-memory` gains
`muse-import-sources` / `muse-import --dir <name> [--dry-run]`. It runs the claude import's evaluation
(`memoryx/agent_memory_claude_import.go`, generalised by a source parameter; the claude path and its tests are
unchanged): preview first, applying needs the switch, an unchanged file is skipped by `source_hash`, a forgotten
name is never brought back, a secret hit is skipped and listed with a masked hint, `source` is
`muse:project/.agents/memory/<file>`, the author is unknown, and `MEMORY.md` is not a memory. The apply route is
audited in the Control Plane as `memory.muse_import`.

**Scope handled: only `project`**, i.e. `<working copy>/.agents/memory` of a working copy under `~/repos`, into
that working copy's AF project. It is never written into AF's user scope. `.agents/memory` may be committed and
shared, so the preview marks every file git tracks as shared and warns before the confirm. Subdirectories are
not entered, a symlinked `.agents` or `memory` is refused.

**Update rule (new here).** A newer muse file replaces an AF copy only when that copy's `source` is muse's and the
#1921 history check says it is still intact (newest change is an import, file byte for byte its text). Otherwise
the item is listed as `diverged` (`af_changed` or `other_source`) and never sent; a claude import or an AF save
of the same name is not overwritten.

Measured (muse 1.4.4 binary on this machine, no logged-in run): the strings of the bundled `migrate` skill give
the scope names and `project` = `<repo>/.agents/memory`, "shared through the repo", the `MEMORY.md` index and the
note shape; the Settings schema has `memory.destinations.*` and a message that `memory.destinations.project`
cannot select a git destination (`local` or `notes` are the choices); `muse --help` offers no way to call
`add_memory` without a model, and `--provider echo` does not call tools. This machine has no muse memory store
of any scope (no `.agents/memory` under `~/repos`, none under muse's config or data directories).

**Inferred, not measured**: the note shape of a real `.agents/memory` (taken from the skill text and the claude
shape; the import is tested with fixtures of it); that top-level files are the only ones muse writes there.

**Not done**: the on-disk location of `personal` and `personal_project` is not established. `memory.destinations.*`
makes it configurable (`local` and `notes`; the error message implies a git destination exists for the
personal scopes, which is inferred), so a fixed path would be a guess; those two scopes are not read, and nothing is imported into AF's user scope. Establishing the location
needs one logged-in muse run that calls `add_memory` (a member's quota). Write-back to muse is out of scope:
with the switch on muse's native memory is off (#1990), so nothing writes there. Follow-up: #2005 (personal
scopes), the codex import stays with #1950 and the phase-based switch with #1972.


## Note (2026-10-12): muse's personal scopes, notes without frontmatter, nested paths (#2005)

The #1991 note above is amended by a measurement; it is not rewritten. A logged-in `muse exec --model muse-spark-1.3`
(muse 1.4.4, `MUSE_EXPERIMENTAL_MEMORY` unset, a scratch git repository, the member's real home, no setting or
credential touched) called `add_memory` once per scope with fake content; the files were then removed.

**Measured.**
- `project`: `<repo>/.agents/memory/<path>.md` plus `.muse-memory.lock` beside it. Untracked, not added to `.gitignore`.
- `personal_project`: `~/.local/share/muse/memory/projects/<slug>-<16 hex>/<path>.md` plus the lock. `<slug>` is the
  absolute cwd with `/` and `.` turned into `-` and the leading `-` dropped (`/home/dev/.af-work/x/probe-repo` gave
  `home-dev--af-work-x-probe-repo`). **The 16 hex digits are the FNV-1a 64-bit hash of the absolute cwd** (found by
  trying sha256, sha1, md5, sha512, blake2, sha3, crc32 and FNV-1/1a on the path with and without a trailing slash; only FNV-1a
  of the plain path matched: `/home/dev/.af-work/s3cq7ox/probe-repo` -> `51d45085345c6000`, pinned by a unit test).
- `personal`: `~/.local/share/muse/memory/personal/<path>.md` plus the lock. `~/.local/share/muse/memory/` did not exist
  before the first write.
- `path` may contain `/`: `af-probe-2005-sub/nested.md` created the directory in the `personal_project` store
  (the same rule applies to the other two scopes; only that one was written nested).
- Content is stored verbatim, with no frontmatter and no trailing newline added. No `MEMORY.md` was created.

**Consequences (what the code now does).**
- #1991 assumed frontmatter from muse's skill text, so a plain note was refused as `no_frontmatter`. A note without
  frontmatter now imports: name from the file name (AF's name rules, else `bad_name`), description from the first
  non-empty line (cut to 2,000 characters), no type (an empty type is the documented absence of one), the whole text as the
  body. A file that opens a frontmatter block it never closes is still `no_frontmatter`.
- Nested notes are walked through pinned directory handles (no symlink followed, hidden directories skipped, depth 6,
  2,000 files). `a/b.md` is the memory `a--b`; two files mapping to one name are both `name_collision`.
  `.muse-memory.lock`, `MEMORY.md` and non-`.md` files are skipped.
- `personal_project` maps forward: the hash of each working copy's path under `~/repos` names the directory exactly.
  A directory no hash matches is compared by slug (equal, or a subdirectory of a working copy); one project is taken,
  two different ones are `ambiguous`, none is `no_project`. The slug rule was measured on one path only, so the slug
  fallback compares normalised forms and the hash is what is trusted.
- `personal` goes to AF's **user** scope, which every project reads, so it is read only when the request says
  `into=user` (Console checkbox, `af-memory muse-import --scope personal --into-user`); otherwise the preview
  evaluates nothing and an apply is refused. `source` is `muse:personal_project/<dir>/<path>` or `muse:personal/<path>`.

**Not verified / not done.** The mapping of `personal_project` to a working copy was verified by unit tests and
by a real directory name, but not by a real run inside `~/repos` (the probe ran under a scratch directory, which
correctly maps to no project). Whether `XDG_DATA_HOME` moves muse's data directory was not run (the probe's environment had it unset); static evidence only: the
muse 1.4.4 binary's embedded help names the config and data roots as `$XDG_DATA_HOME/muse` or `$HOME/.local/share/muse`, so the import
uses an absolute `XDG_DATA_HOME` and otherwise `~/.local/share`. The store is opened without following a symlink from `muse` down; a
symlink above it (a persisted home) is followed. Only the `personal_project` store was written nested. `type` passed to
`add_memory` was not exercised, and `memory.destinations.*` still shows no value. Write-back to muse stays out of
scope. Follow-ups: #1972 (phase-based switch).

## Note (2026-10-12): the browser lists every forgotten memory (#2003)

The forgotten list of the browser was built from the 300 most recent changes, so a memory forgotten
earlier could not be restored from it. `entries/member-forgotten` lists them from the store's
tombstones (`<scope dir>/.forgotten/<name>`) for the user scope and every project, or the one named by
`scope` and `project`. A row is the same view the changes list gives its newest row (who forgot it and
when, the commit to revert, `revertible`), resolved with the scan of `entries/member-latest`: raw and
decoded text, both sides of the newest change. A name or text that fails it is counted in `withheld`,
never listed. The tombstone directory is untrusted like the rest of the store: a symlink on the way, a
non-regular or oversized entry, or a name that is not a memory name is not a tombstone, and a tombstone
with no published change, or a memory that is live again, is not listed. One answer holds at most 300 rows
(`truncated`; narrow the scope). The route is read-only, counts no use and is not gated by the switch;
restore still goes through the revert route with its newest-change and `expectRevision` checks.

The newest rows are chosen after the tombstones have been judged (within the budget below) (by the newest change's time, found
in one history pass), so the cut never drops a newer memory for an older one, and `truncated` is set
only when a listable row was left out (within the judging budget below). `projects` names every project with a listable forgotten
memory, cut or not, so the Console keeps offering a scope whose rows did not fit. Every tombstone is looked up in that one
history pass and those without a published change are dropped; the scan of the rest is budgeted at 2000
memories per request, newest first. Past the budget the oldest are left unjudged, their project is still
named (unless the memory is plainly live), `incomplete` (not `truncated`) is set and `withheld` is a lower bound; narrowing the scope may not complete it. The project id of an unjudged memory is scanned before it is named, as the judged path does.
The read lock is taken per memory, not across the request.

## Note (2026-10-12): member writes carry a mark the Agent verifies; the member can write memories (#1997)

The limit stated in the #1985 note is lifted by [0113](0113-member-origin-mark.md). The CP signs the member's
relayed `entries/pin`, `entries/revert` and the new `entries/member-save` with an Ed25519 key only it holds,
bound to the method, path, query, body, workspace, member, a 60 s expiry and a nonce; the Agent verifies with
the public keys it was started with and refuses these three routes without a valid mark, including a request
carrying only `AGENT_TOKEN` and `X-AF-Relay: cp`. On these three routes the author `member` / `console` is
therefore recorded only behind a verified mark; the claude and muse imports still record `member` unsigned
until #2014. `entries/member-save` (create or edit, the tools' own revision check, secret scan and
limits, not gated by the switch, no new project) is back in Settings > Agent memory, and the CP audits it as
`memory.entry.save`. What the mark does not change: a session with a shell can still write the store's files
directly (decision 8's reasoning stands); the mark governs the Agent's routes, not the files.

## Note (2026-10-14): moving the memories of an orphaned project (open question 5, #1984)

A project is keyed by its repository's absolute `git-common-dir` (decision 2), so a repository renamed,
moved or cloned again becomes a new project and the old one's memories stay under an id no session reaches.
The Console now lists such projects and the member moves or merges one into another.

- **Orphaned means known to be gone, and in doubt it is not.** The Agent calls a project orphaned when the root
  recorded in its `project.json` does not exist under `~/repos` and no working copy belongs to the same
  project. Working copies are found at any depth under `~/repos`, inside other working copies and behind symlinks
  included, and from the sessions' directories. A
  directory that cannot be read, a repository whose identity cannot be resolved, or a search past its budget
  (100,000 directories) or time (20 s) makes the answer incomplete, and then nothing is called orphaned. A root outside `~/repos`, an empty or unreadable `~/repos` (not mounted yet), a symlink anywhere on
  the way (a `/scratch` link dangles after a stop), any error but "does not exist" and a probe that does not
  answer in 3 s all read as "not orphaned". A worktree shares its parent's id, so it is never a separate
  orphan. A project with nothing readable to move is not offered. The move itself re-checks this, so the route
  cannot move a live project's memories out.
- **Three routes, one of them a write.** `GET entries/projects` lists the projects with their counts and the
  orphaned flag; `GET entries/project-move-preview` shows, per memory, what would happen and writes nothing;
  `POST entries/project-move` moves up to 50 previewed memories, each sent with the revision, status and new
  name the preview showed; the plan is made again and an item that differs stays in the source, so the apply is
  bound to the screen the member confirmed. A move that breaks off answers 500 with the results so far, the
  memory it stopped at (`partial` when its copy is already in the target, with the create commit) and the ones
  not tried. The write answers only a request carrying the
  CP's member-origin mark ([0113](0113-member-origin-mark.md): `memberOriginRoutes` and `memberorigin.Require`),
  the CP audits it as `memory.project.move` with the source project id, and no MCP tool reaches it. The
  target is a project that holds memory or has a working copy under `~/repos`; the latter needs no memory yet,
  and its first memory brings the `project.json` along as a session's save does.
- **Collisions are the member's call.** A name taken in the target is a collision. With no policy chosen the
  preview says so and the move is refused; `skip` leaves those memories in the source, `rename` moves them as
  `<name>-moved`, `-moved-2`, … (names are chosen by the Agent and shown in the preview). A memory whose text,
  pin and import provenance are identical to the target's copy is not a collision: only the source copy goes.
  A pinned source beside an unpinned copy is a collision, so a move never drops the member's pin or the
  import's source hash. Nothing in the target is
  ever overwritten, including a name that appeared after the preview. A memory changed since the preview, and
  a file withheld by the scan, stay where they are.
- **Two published changes per memory, create first.** For each memory the target gets a `create` and then the
  source gets a `forget` (trailers `AF-Moved-From` and `AF-Moved-To` name the other end), both authored by the
  member through the same commit step as every write. That is two commits per memory rather than one per
  operation, because the changes list and revert are per memory path: each row can be undone there (revert the
  create to take the copy out of the target, revert the forget to bring the source copy back). A stop between the
  two leaves the text in both projects; running the move again finds the identical copy in the target and
  finishes with the forget, so the move is resumable and a failure halfway loses nothing. Moves are
  serialized. The pin, kinds, author, creation time and import source travel with the memory; the use count
  and the revision do not (the revision continues the target name's own).
- **Not done:** suggesting the merge when a new project's remote URL matches an orphaned one. `project.json` records
  no remote, so it would need a new field and a backfill; tracked in the follow-up named in the PR.

## Note (2026-10-14): the member can purge a memory's past revisions from history (open question 3, #1982)

Open question 3 is answered. `memory_forget` still only hides a memory; a separate, member-only action
rewrites the 0022 history so the text of its past revisions is gone from this workspace. It is a Console
action behind the member-origin mark ([0113](0113-member-origin-mark.md)) — `POST /agents/memory/entries/purge`,
signed by the CP and audited as `memory.entry.purge` with the project id only — and no MCP tool reaches it.
`GET …/entries/purge-preview` is the read-only confirmation: counts, commit ids, the affected refs and the secret
scanner's findings (rule, path, line, masked hint), never the text. The purge is bound to the preview by a
plan id (a hash of the refs it saw) and by the memory's name typed again, so a history that moved in between
asks to be reviewed again.

What it does, and what it decided:

- **Which revisions.** `af/<scope dir>/<name>.md` is removed from every commit on every ref. The current
  revision is the exception: the commits at the tip of `main` that already hold the tip's text keep it, so
  the published state and the history agree. A leak in the current text is purged by saving the memory
  again first. A forgotten memory has no current revision, so everything goes, the forget record included.
- **Snapshots and other refs.** All of them are rewritten, not left or dropped: `main`, `refs/imports/*`
  (imported lineages) and `refs/premigrate/*` (rescue copies of a replaced lineage). Commits that held only
  this memory are dropped and the rest re-linked with their author, dates and message; signatures do not
  survive a rewrite. A ref of any other kind makes the purge refuse rather than leave the text reachable.
- **Unreachable objects.** After the refs move in one transaction, the index is rebuilt from the new tip
  (it can name a staged old revision), the reflogs are expired and `gc --prune=now` runs; then each purged
  blob is looked up and the purge reports success only if it is gone from disk. A text that another path
  holds byte for byte stays (counted as shared).
- **Bundles already exported** cannot be recalled. The Console dialog and the guide say so and tell the
  member to delete those copies. Copies the Agent itself holds are different: export, bundle import and
  the purge are serialised on one lock, and the purge removes leftover `export-*`, `import-*` and `upload-*` files from the work dir, sparing only an
  upload a handler of this process is still receiving (age cannot tell one from an orphan); a file it cannot remove fails the purge.
- **The changes list.** The memory's own change commits go with the revisions. The newest kept change carries
  `AF-Purged-Before`, the list marks it `purgedBefore`, and reverting it fails with a conflict that says the
  earlier revisions were purged; without the marker the missing earlier side would read as "this change
  created the memory" and the revert would delete it. Forgetting it still works. Other memories' history and
  their revert are untouched.
- **Concurrency and crashes.** The purge holds the store and snapshot locks for its whole run, like a write.
  New objects are written before any ref moves, so a crash there leaves garbage only; a marker file
  written before the refs move (it names the path and the purged blobs) makes the next purge or the next start
  of the snapshot loop finish the job with the same code as a first run — rebuild the index, put the memory's
  staging file back to the live text, gc — and the marker stays until every purged blob is verified gone. The marker also records the ref moves, so a recovery can
  tell what a crash left. `update-ref --stdin` is atomic per ref, not across refs: all refs old means nothing
  happened and the purge runs again; a mix is rolled forward to the recorded values (compare-and-set, stale
  `.lock` files of the dead git removed); every ref is checked against both recorded values before anything is decided, and a value the purge cannot
  account for (a ref at neither) keeps the marker and an error. The dead git's own lock files are removed only if
  they belong to a recorded ref, the update-ref pid recorded in the marker is dead (a dead past producer proves
  nothing about a same-named lock a live writer holds now), the lock is older than this Agent process and no
  process has it open (unreadable /proc counts as open). Otherwise the marker stays and the recovery is refused;
  the member can retry after the Agent restarts.

What it does not do: it cannot reach copies outside the repo (exports, a session's own files), and a session
with a shell can still read the store's files directly, as in decision 8.

## Note (2026-10-14): the switch imports and writes back by itself (#1972)

Decision 6 left the import and the write-back as two manual steps per project, and the guide asked the
member to remember both. Flipping the switch now carries claude's native memory across without them. The
continuous mirror stays rejected: this runs at the edges only (a flip, and one catch-up after it), never
while the switch is steady.

- **A level-triggered reconciler, not an edge handler** (`memoryx/agent_memory_autosync.go`). The phase is
  persisted in `<claude config>/af-agent-memory.mode.json`, beside the store and outside it so that no store
  walk, snapshot or restore can touch it: `off → importing → on → writing_back → off`. The ui-prefs `PUT` only moves
  the phase (synchronously, before it answers) and wakes the loop; the loop also runs at Agent start. A flip
  back during a run turns the phase around (`importing ⇄ writing_back`) and the run in flight stops at its next
  chunk (25 files) or, for a write-back, project boundary. A crash mid-run resumes by running again: both
  operations are idempotent. One run at a time, under the store locks the manual routes take. An Agent that
  starts with no recorded phase adopts the switch as it finds it (`on` owes no import; it predates this).
- **Native memory follows the effective state.** `claude.AutoMemoryOff` reads the phase, not the raw pref:
  claude's auto-memory is off in `importing`, `on` and `writing_back`, and resumes only in `off`. Otherwise a
  session started right after switching off would write native files the write-back then sees as conflicts.
- **The import is stricter than the member's own.** It drives the manual import's evaluation, secret scan and
  publish path (`agentMemImportBuildAuto`), with one difference: a claude file newer than an AF copy that was
  edited in AF (the import's own text is the `intact` tri-state of #1921; anything else, `unknown` included)
  is a `conflict` (`af_changed`), listed and not applied. The manual import keeps replacing it, which decision
  6 calls the member's choice; here nobody chose. Forgotten memories are never brought back, secret hits are
  skipped and listed by rule name only, unmapped claude directories (`no_project`, `ambiguous`) are counted.
- **Catch-up.** Claude sessions running at the switch-on launched with native memory on; their names are
  recorded, and when the last of them has ended (`SessionAlive`, any execution method) the same import runs once
  more under the same rules.
- **The write-back never uses `--overwrite`.** It is the manual write-back (`agentMemExportApply` with the
  preview's token): a `pre-export` snapshot first, `new`/`update`/`remove` applied, `conflict` and `secret` kept
  and listed. A snapshot failure, a leaf that changed under the apply or an unwritable `MEMORY.md` keeps the
  phase at `writing_back`: claude's native memory stays off, and the loop retries after 30 s, 1, 2, 5 and then
  every 10 minutes. From the third failure the status says `failing`; the Console then offers "retry now" and
  "switch native memory back on anyway" (`abandon`: phase `off`, `nativeBackOn` recorded; AF memory is not
  written, and the manual write-back is still there).
- **codex, stage A.** Switching on records codex's `features.memories` when it was `true` and sets it `false`;
  switching off puts `true` back and never forces it on. A value the member sets while the switch is on replaces
  the record. Nothing is imported from or written to codex (stage B waits for #1950, stage C for a
  measurement). The flag is written alone (`SetMemoriesFlag`): no `[memories]` tuning is seeded.
- **Telling the member.** The Console asks before it flips, with counts from `GET …/auto-sync/preview?to=on|off`
  ("Import 474 memories from 6 projects; 1 skipped (secret)"), shows the phase, the last run and its leftover
  files under the switch (`GET …/auto-sync`), and one notification (`memory-sync`, workspace-targeted, row
  "other") is raised for a finished run that left rows. With the workspace stopped there are no counts and the
  dialog says so; the Agent reconciles when it starts, and equally when the pref flips without the dialog
  (another device, a settings import).
- **Audit (the open point of the issue's §5).** The run happens inside the Agent, which has no channel to the
  Control Plane ledger; `memory.claude_import` / `memory.claude_export` are recorded where the member's `POST`
  passes through the CP proxy. Two ways to keep a record were weighed: (a) have the Console drive the run
  through those audited routes, or (b) report each run from the Agent to the CP. (a) cannot be level-triggered
  (a flip from another device, a settings import or a restart mid-run has no Console to drive it) and would
  need a second code path next to the reconciler; (b) needs a new Agent → CP route with its own auth and failure
  mode. Chosen instead, the smaller change: the member's *decisions* pass through the CP and are audited as
  `memory.auto_sync` (target `wake:on`, `wake:off`, `retry`, `abandon`; the Console sends a wake after a
  confirmed flip, which also starts the run without waiting for the settings sync), signed with the member-origin
  mark ([0113](0113-member-origin-mark.md)); the run's effects are each a commit by the member in the 0022 history
  (`import`, `pre-export`), and its result is the persisted run record. What is **not** audited at the CP: a flip
  that never went through the Console's confirmation, and the run itself. If that gap matters, (b) is the
  follow-up.
- **The phase is durable before anything depends on it.** A phase that cannot be saved starts nothing and the
  old one stays; a state file that exists but cannot be read is not a first start (nothing is adopted over it,
  native memory stays off, the status says `unreadable`, and only the member's "switch native memory back on
  anyway" replaces it). `CatchUp` survives an on → off → on flip before the write-back has run. The codex flag is
  reconciled from its persisted record on every pass in every phase (record, then touch the flag, then mark
  done), so a failed write is retried and shown, and the member's last edit is read before the phase leaves `on`.
- Values are never logged: the run record and the state file hold counts, names the secret scan passed, reason
  codes and rule names.

## Note (2026-10-14): suggesting the move when a new project's remote matches an orphan's (#2110)

The part the #1984 note left undone. It is a suggestion in the Console and nothing more: no move happens
unless the member opens the usual preview and confirms it.

- **`project.json` gains `remote`.** It is the repository's `origin` URL reduced to `host/owner/repo`: ssh,
  scp-like, https, a trailing `.git` or `/` and an ssh/https port all collapse to one form, the host is
  lower-cased, the path keeps its case. Userinfo is dropped before anything is built, so a token in a URL never
  reaches the file (a test checks it). Only spellings known to name the same repository are
  unified: ssh and scp-like remotes are recorded for github.com, gitlab.com, bitbucket.org and codeberg.org
  alone, because on a generic ssh server `host:team/repo` is relative to the login home and
  `ssh://host/team/repo` is absolute; any other host is recorded for http(s) remotes only. A remote that does
  not name a repository on a host (a local path, `file://`, `git://`) is not recorded either: two checkouts of
  a path say nothing about each other. A remote the secret scan flags is never recorded, listed or read back
  from a hand-edited record. The field is not part of
  the project's identity (the id is still the `git-common-dir`) and is `omitempty`, so every older
  `project.json` loads as before. A value read back is checked against the normalised form again, since the
  store can be edited by hand. A path in the remote that the secret scan flags drops only the remote, never
  the project's first memory.
- **Backfill.** A write from a working copy adds the remote to an existing record that lacks it, keeping the
  other fields. The Agent also does it once at start for every project with a working copy now, in a single
  commit (`AF-Op: project-info`, author unknown, its own `AF-Trigger: project-record` so the changes list neither
  shows it nor counts it against its limit). The record is extended by splicing one member into its stored
  bytes; its other fields, their order and any field a newer version wrote stay as they were. An orphan whose root is
  gone before it was recorded simply has none and is never suggested; nothing fails on it.
- **The rule.** `GET entries/projects` sets `mergeInto` on an orphaned row when exactly one live project, other
  than the orphan, has the same non-empty remote. Two live projects with the remote (two clones of one
  repository) give no suggestion: a guess between them is wrong half the time, and the member can still
  pick either in the move panel. The Console shows the suggestion above the pickers; its button fills them
  and loads the preview.

## Note (2026-10-14): importing AF memory from a bundle (#1983)

A memory bundle (0022) carries `af/`, but the generic restore and import skip it on purpose: they write
without the secret scan decision 9 requires. A member who moves to a new workspace therefore left the AF
memory behind. This is the other door: a one-shot, member-driven import, previewed, scanned and recorded.
It does not touch the generic paths, which keep skipping `af/`.

- **Where it starts.** The bundle is taken in by the existing `POST /agents/memory/import`
  (`refs/imports/<id>/*`), whose apply still writes no AF memory. `GET /agents/memory/af-import/preview?importId=`
  reads `af/` at the head of that lineage (one streamed `git cat-file --batch`, no history walk; only the small project.json files and the memory files
  of projects that will be imported are requested, never a blob over the limit, each distinct blob once from git; the 64 MiB budget counts every file by its size, so many names for one blob
  cannot get past it, and the project records are held to 4 MiB in all) and
  `POST /agents/memory/af-import` publishes. The POST answers only the member-origin mark
  ([0113](0113-member-origin-mark.md)), is audited as `memory.af_import` (import id only), and has no MCP tool.
  tar.gz is not read: the generic tar import drops `af/` before it is committed.
- **Mapping.** A source project maps to a live local project (a working copy under `~/repos`) by id; when the id
  differs, which is the usual case because it derives from the clone's path, by the one live project with the same
  normalised remote (#2110). None or two: listed as `no_local_project` / `ambiguous`, nothing of it read or shown,
  nothing imported. A search for working copies that did not complete proves nothing about "the one project
  with this remote", so then only an id match maps and the rest is `search_incomplete` (checked again at the
  apply). A source project directory whose name fails the secret scan is counted (`withheld`), never named or read. The user scope maps to itself. There is no manual mapping.
- **Evaluation.** The claude import's scan and publish path, on a file already in memory: the raw file, every
  decoded field, the description, body, type and kinds, and the rendered file are scanned (no override; findings
  are masked, a name that fails the scan is withheld), then the limits. The statuses are `new`, `unchanged` (same
  description, type, body and kinds), `exists` (a live memory of that name with other text: **never replaced**: to
  take the bundle's text the member edits the memory here by hand, since forgetting it would make it
  `forgotten` for good; the same name from two source projects that map to one target is imported once, from
  the first in id order, and the other is `exists` with reason `duplicate_in_bundle`; the publish itself refuses
  a live copy, whatever was evaluated before), `forgotten` (a tombstone, or the name is anywhere in this scope's
  history: never brought back, which includes one forgotten long ago; a memory forgotten on the bundle's side has no
  live file in the bundle and is simply not there), `secret` and `invalid`. The apply repeats the evaluation under both store locks per request of up to
  50 items and refuses an item whose hash or target changed since the preview; the write is
  `agentMemImportWriteTo`, which scans the rendered file again and checks the tombstone.
- **What is preserved, what is reset.** Preserved: name, description, type, body, the agent kinds a memory is
  limited to. Reset: the revision (the next number in this store, or 1), the author (unknown, as for the claude
  import), created / updated (the import time), the pin and the use counts. The origin is kept in `source`
  (`bundle:<import id>:<scope dir>/<name>@<source revision>`) and `source_hash`, and in two commit trailers
  (`AF-Bundle-Import`, `AF-Bundle-Source`), so each import is an `import` change by the member in the changes
  list and can be reverted there. The bundle's history is not imported; it stays under `refs/imports` until pruned.

## Note (2026-10-18): the member's own agent outside Agent Fleet reaches the store through `/mcp` (#1988)

Decision 4 puts the tools on the af server inside the workspace. A member also runs agents outside it
(Claude Code or Desktop on their own machine) and holds a PAT for the CP's `/mcp`. This note adds that door;
no decision above changes.

- **Tools.** `memory_projects`, `memory_index`, `memory_search`, `memory_read` (read-scoped PAT) and
  `memory_save`, `memory_forget` (write-scoped PAT), in `control-plane/internal/mcpsrv/mcp_memory.go`. Each proxies to
  the Agent's `/agents/memory/external/*` routes, which reuse the tools' own read, search, save and forget: the
  revision check (decision 4), the secret scan (decision 9) and the one-commit publish are unchanged.
- **Caller and project.** An outside caller has no session, so it passes the project id itself (from
  `memory_projects`) and the author kind is recorded as `external`, the session as unknown. A project must already
  hold memory, as for the Console: there is no working copy to take a new project's root from. The client's
  self-declared MCP `clientInfo` (else the User-Agent) is sanitised and kept as an `AF-External-Client` commit
  trailer, for information only; nothing is decided by it. A memory limited to certain `kinds` is not shown to
  `external`. Reads count as uses (#1703).
- **Opt-in.** One per-member ui-pref, `externalMemory`: `off` (default), `read`, `write`. It is separate from
  `agentMemory`: either can be on without the other. `write` is a second, deliberate step above `read`, because a
  write from outside is read by every kind inside (decision 8's risk, with a writer outside the workspace) and
  read text reaches an outside model beyond the workspace's egress controls. The Agent enforces it on every call,
  so a PAT cannot outrun the setting. The tools are listed by PAT scope only: the CP does not hold the setting and
  a stopped workspace cannot be asked, so a refusal names the Console setting to change.
- **Audit.** The Agent has no channel to the CP's ledger, but these calls pass through the CP, so a successful
  external save and forget is recorded as `memory.entry.save` / `memory.entry.forget` with actor kind `mcp`, the
  PAT id, and the project id as the target when it has the shape of one. The memory name is free text and is
  never copied. This does not change decision 8 for writes made inside the workspace, which stay unaudited at the CP.
- **Stopped workspace.** The tools answer that the workspace is not running and must be started in the Console.
  Starting it for a tool call is a separate question.
- **Limits.** The new Agent routes answer anything holding `AGENT_TOKEN`, as the tools' routes do (decision 8); the
  setting, not a signature, is what gates them. They do not carry the member-origin mark
  ([0113](0113-member-origin-mark.md)), which is for the Console's own requests. There is no tenant-level admin
  switch; `AF_MCP_ENABLED` still turns `/mcp` off for a whole deployment. Follow-ups: #2318.

## Note (2026-10-18): Settings shows two memory entries, not one (#2324)

The Settings tab "Agent memory" had grown into one scroll of thirteen panels that held two different objects: the
native-memory snapshots of [0022](0022-agent-memory-management.md) and the shared memory of this ADR. This note
records the structure the maintainer chose; the decisions above are unchanged and no API or Agent behaviour moved.

- **Two adjacent rail entries.** "Agent memory" (`set.tab_memory`, ja エージェントメモリ) is this ADR's memory;
  "Memory snapshots" (`set.tab_memory_snapshots`, ja メモリのスナップショット) is 0022's version control. Each has one
  status header and a small view-nav whose last view is remembered per entry. `openSettings("memory:changes")`
  names a view; `err.memory_disabled` and the Agent-settings pointer now say Settings > Agent memory > Settings.
- **Agent memory** has the views Memories (browse and pins, merged later), Changes, Import & export and Settings
  (the sessions' switch with the auto-sync status, and the outside-client setting). Only Settings works while the
  workspace is stopped, because both switches are ui-prefs (#1735).
- **Memory snapshots** has History (with "Restore this point"), Targets and Export / import. **The codex memories
  toggle stays in Targets**: it is a setting of codex's own memory, which is what 0022 versions.
- The views are standalone components behind the nav, so they can be mounted in a first-class Memory view later
  (the maintainer's option C, once an approval queue exists) without a rewrite.
- The forgotten-memory action will be labelled "Unforget" (ja 忘却を取り消す) instead of "Restore", so "Restore this
  point" belongs to snapshots alone. That label change is a later step and not part of this one.

Follow-ups: #2324.

Update (2026-10-18, #2324 PR 4): the label landed as "Unforget" (`mem.br_unforget`, ja 忘却を取り消す). The same pass
hid the `agent memory` rows in Memory snapshots > History behind "Show Agent Fleet memory writes", since Agent memory >
Changes lists those writes already.

Update (2026-10-18, #2324): the view-nav is always a tablist. The phone-width select is gone; at narrow widths the tabs stay on one row and scroll sideways, with arrow-key, Home and End navigation.

## Note (2026-10-19): a tenant-level switch for outside access (#2318)

Decision 2 left "a tenant-level admin switch can disable the feature" open, and the limits paragraph above says there is
none. This note records the one that was added; the decisions above are unchanged and the member-level setting is not
redesigned.

- **Storage.** `tenant.external_memory` (migration 0103 SQLite, 0088 Postgres), `1` by default so every existing tenant
  behaves as before, and the member's `externalMemory` setting still starts off.
- **Enforcement at the CP.** `/mcp` does not list the six memory tools when the tenant's switch is off, and
  `mcpToolCall` resolves tools from the same list, so a call by name is refused as an unknown tool. The switch is read
  live per request from the tenant behind the PAT's membership; when it cannot be read the tools are withheld. The
  Agent's own check of the member setting is unchanged.
- **Admin surface.** `GET`/`PUT /api/admin/tenants/{slug}/external-memory` for the tenant administrator, shown in tenant
  settings as "External memory access" and audited as `tenant.external_memory` (`enabled=true|false`). Guide:
  `admin/08-external-memory`.

## Note (2026-10-26): outside callers select the project by repository remote, and writes name their scope (#2765)

The decisions above are unchanged. This records how an outside caller addresses a project.

- **`repository` argument.** `memory_index`, `memory_search`, `memory_read`, `memory_save` and `memory_forget` accept the
  output of `git remote get-url origin`. The Agent (not the CP, which stays a relay) normalises it with the function that
  produced each project's `Remote` and matches it against the projects that hold memory. `project` stays accepted; the two
  must agree. A remote that matches nothing, matches more than one project, or cannot be normalised is refused, never guessed. Only the forge hosts
  (github.com, gitlab.com, bitbucket.org, codeberg.org) resolve: the stored `Remote` drops the port, so on a self-hosted host
  `forge:8443/x`, `forge:9443/x` and `forge/x` are one string for possibly different services. Such a host is refused with a
  pointer to `project`.
- **Writes name their scope.** An external save or forget without `scope` is refused (it used to fall to the user scope,
  so a model that forgot the project published a repository-specific fact to every project). `scope: project` needs
  `repository` or `project`. The in-workspace tools are unchanged: they derive the project from the working copy.
- **Audit.** The Agent's write answer now carries the resolved `project`, and the CP records that id as the audit target.

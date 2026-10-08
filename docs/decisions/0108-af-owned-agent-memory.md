# 0108. Agent memory owned by AF: one store per user, workspace and project, written and read by every kind through the af MCP

English | [日本語](0108-af-owned-agent-memory.ja.md)

- Status: **proposed** (2026-10-03). Built so far (P1 without the claude seed): the store, the
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
   follow-ups in #1703, relevance ranking stays with #1558). The Agent bounds it, so every client
   gets the same answer:
   - The described part is limited to a byte budget of rendered lines (default 24 KiB; the
     optional `budget` argument is clamped to 4–64 KiB), in rank order: `feedback` and `user`
     first, then the rest, newer first within a tier. A line carries the description cut to 80
     characters; `memory_read` and `memory_search` keep the full text.
   - What did not fit is listed as names only, within a separate 8 KiB: names cut to 32 bytes
     with "…" (a prefix), grouped by first hyphen segment (`adr-{0072-…,0079-…}`). Past that, "and
     N more (use memory_search)".
   - The guidance says the index is partial and to `memory_search` with the task's keywords
     before re-deriving something. Today that is the `memory_index` tool description; the
     distributed block itself is not built yet and must carry the same wording within its cap.
   Measured on the 473 imported claude memories: 111 KB of full lines became 117 described
   lines (24.4 KB) plus a 8.2 KB tail naming 355 more; one was left to the count.
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
     **never brought back**. Applying needs the switch on (decision 4's setting); the preview and
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
9. **Secrets are stopped before they are stored.** Every candidate body — a save, an update, the
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
   step 2 (#1734); still open: a live check that a new claude session loads no `MEMORY.md`.
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
- **P3** — decision 6 step 2, decided after measuring (#1734; the live check is open); tie-in with #1559 (automated review) and #1558
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
- Import loop guard: the import reads a claude file whose `af_source` names the AF memory and whose
  text still hashes to `af_hash` as `unchanged`, although its mtime is newer than AF's update.
- codex: deferred to #1683 (its memory workspace is rewritten by its own pipeline).

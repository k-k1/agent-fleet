---
audience: "anyone who wants agents to remember what they learned, or to undo what they remembered"
---

# 18. Agent memory — what agents remember, and how you keep it

English | [日本語](18-agent-memory.ja.md)

This chapter is moving into pages, one per thing you want to do.

| What you want to do | Page |
|---|---|
| Read, correct, pin and forget what agents remember | [Read and correct agent memory](../user/memory/overview.md) |
| Check who changed what and when, and revert it | [Check memory changes](../user/memory/changes.md) |
| Let sessions use memory, or stop them | [Set how memory is used](../user/memory/settings.md) |
| Check memories for staleness | [Find stale memories](../user/memory/lint.md) |
| Have a model draft fixes to memory | [Use memory tidy-up](../user/memory/tidy-up.md) |
| Import or write back the memory of Claude Code and others | [Import and export memory](18-agent-memory.md#import-export) |
| Roll an agent's own memory back with snapshots | [Roll memory back with snapshots](18-agent-memory.md#memory-snapshots) |
| Use Agent Fleet memory from an external agent | [Use memory from an external agent](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent) |

## Agent memory

The Memory view, Changes and Settings are now described in the pages that start at [Read and correct agent memory](../user/memory/overview.md).

### Import & export

Import & export is a hub of cards, one per source, in three groups: **Import** (Import from Claude Code, Import
from muse, Agent Fleet memory in this bundle), **Export** (Write back to Claude Code) and **Maintenance** (Move
memories of a project that no longer exists). **Start** opens that source's wizard in place of the hub. Every wizard
has the same frame: a step line (**Source**, **Preview**, **Confirm**, **Apply**, **Result**), the preview grouped by
status with a count per group, and **Cancel** (disabled while an apply is running), which becomes **Back to Import &
export** once it has finished.

- **Import from Claude Code**: brings the memory Claude Code kept for a project into Agent
  Fleet memory, once. Pick a project and you see what would happen before anything is written: **new**,
  **newer in Claude** (will overwrite the Agent Fleet copy, even one you edited since; both times are shown),
  **unchanged**, **forgotten** (a memory you forgot, or whose import you reverted, is never brought back),
  **possible secrets** (skipped, with masked findings. Fix the Claude file and preview again; there is no
  way to import it anyway) and **cannot import** (with the reason). Nothing is cut: names with
  `_` or `.`, descriptions up to 2,000 characters, bodies up to 200 KiB and long lines are imported as they are;
  a file is imported when its name, description and body meet those limits (the 200 KiB is the body's) and it is a
  regular text file; otherwise the preview lists it with the reason. A memory an earlier version imported
  with a cut description is shown as **newer in Claude** and refreshed from the Claude file, as long as you have
  not edited the Agent Fleet copy since. A Claude project is matched to a
  working copy under `~/repos`; one with no match, or two, is listed but cannot be imported. Importing needs
  Agent Fleet memory to be on; the preview works either way. The imported memories show in the list as
  **imported**, with the author unknown, and each can be reverted like any other change.

- **Import from muse**: brings the notes Muse Code kept into Agent Fleet memory,
  once. It works like the Claude Code import: pick a folder, read the preview, then confirm. Muse has three
  stores and the picker lists each: **project notes** (the working copy's `.agents/memory`, into that
  project), **personal project notes** (Muse's own folder for a working copy, `~/.local/share/muse/memory/projects/<slug>-<hash>`,
  into the project of the working copy under `~/repos` it belongs to; a folder that matches no working copy
  is listed as *no working copy*, and one that fits two different projects as *more than one matches*, and
  neither can be imported) and **personal notes** (`~/.local/share/muse/memory/personal`). The personal notes
  would go into your **user-wide memory, which every project's sessions read**, so they are neither
  previewed nor imported until you tick the box that says so (`--into-user` in the terminal); nothing else
  is ever imported there. A note needs no frontmatter, because Muse stores what it is given: its name is the
  file name (it must be a valid Agent Fleet name, otherwise it is listed as *bad name*), its description is
  the first non-empty line (cut at 2,000 characters), and it has no type; such notes are marked *no
  frontmatter*. A note with frontmatter is read as before. Muse paths may contain folders: `a/b.md` becomes
  the memory `a--b`, and two files that map to one name are both refused (a folder with more than 2,000 notes is previewed only: the check cannot be completed, so it is not applied). A memory of the same name that came from a different muse file, folder or scope is never overwritten. `MEMORY.md`, Muse's
  `.muse-memory.lock` files and anything that is not `.md` are not notes, and a symlink is never followed.
  **New** notes are added, **newer in muse** notes update an Agent Fleet copy only while it is still exactly what
  this import wrote (a memory you saved, edited, pinned or reverted in Agent Fleet since is listed as **Agent Fleet has
  its own version** and left alone, and so is a memory of the same name that did not come from muse), a
  memory you forgot is never brought back, and a file the secret scan flags is skipped and listed. The
  author is recorded as unknown. `.agents/memory` lives inside the repository and a team may commit it: files git
  tracks there are marked **shared** and the preview warns before you confirm, because importing copies
  teammates' text into the memory your sessions read. Agent Fleet does not write back into Muse. Applying
  needs the switch on; the preview works while it is off.

- **Agent Fleet memory in this bundle**: brings in the Agent Fleet memory a memory bundle carries. Restoring a
  bundle in Memory snapshots never writes it (the restore has no secret scan). **Start** asks for a bundle file
  as the wizard's source (a tar.gz holds none and is turned away), then previews it and imports it on its own.
  Each project in the bundle is matched to a
  project here by its identity, or, when that differs (another path), by the one project here with the same
  repository address; a project with no match, or two, is listed and not imported. The user-wide memories
  are always offered. Every memory is shown as **new**, **already here**, **different here, kept** (a
  memory of that name exists here: it is never replaced), **forgotten here, not brought back** (also when
  only the history remembers it), **possible secret, skipped** (every part of the file is scanned, with no
  override) or **cannot be imported**. Only the latest text comes across, with the agent limits it had; the
  bundle's history does not. What is **reset**: the revision (each memory starts at the next number here),
  the author (unknown, as for the Claude import), the dates (the time of the import), the pin and how often it was read; the origin
  is recorded in the memory's source (`bundle:<import id>:<project>/<name>@<bundle revision>`) and in the
  commit (`AF-Bundle-Import`, `AF-Bundle-Source`), so each import shows in Agent memory > Changes and can be
  reverted there. It works with the switch on or off, answers only your own request, and is recorded in the
  audit log.

- **Write back to Claude Code**: the other direction, by hand and for one project at a
  time. Turning the switch off already writes every project back by itself (see [Set how memory is used](../user/memory/settings.md)); use this to
  resolve a conflict it left, or after switching Claude Code's memory back on anyway. It copies what was
  learned through Agent Fleet into Claude Code's memory (Claude Code only; codex is not covered). Pick a project and you see what
  would happen first: **new**, **newer in Agent Fleet** (updates a file written back earlier), **forgotten in
  Agent Fleet** (removes a file written back earlier), **conflict** (a Claude file Agent Fleet did not write, or
  that you changed since: kept unless you tick it; a file that is still exactly the one imported counts as
  **unchanged**, or as **newer in Agent Fleet** once you edited the memory here), **possible secrets** (skipped), **unchanged** and **only in
  Claude** (never deleted). An update keeps the fields Claude recorded in the file's `metadata`
  (such as `originSessionId`); a file whose metadata cannot be copied as is shows as a conflict instead. User-scope memories, and memories limited to other agent kinds, are not written. `MEMORY.md` is rebuilt within Claude's load
  limit (200 lines, 24 KiB) and replaces the old one; Claude's memory is snapshotted first
  (trigger "pre-write-back"), so you can restore it from Memory snapshots > History. It works with the switch on or off;
  while it is on, end Claude Code sessions started before it was switched on first.

- **Move memories of a project that no longer exists**: A project is tied to the folder of its
  repository, so a repository you renamed, moved or cloned again starts as a new, empty project and the old one
  keeps its memories where no session can reach them. This lists such projects (those whose working copy is known
  to be gone; when Agent Fleet cannot tell, for instance a folder on a disk that is not mounted, the project is not
  listed) with their memory counts. Pick one and the project that replaces it (any project that has memories or a
  working copy under `~/repos`) and read the preview before confirming. A memory whose name is already taken in
  the target is **never overwritten**: you choose to leave those where they are or to move them under a new name
  (`<name>-moved`, then `<name>-moved-2`, `<name>-moved-3` and so on); one whose text is identical to the target's copy only has the old copy removed. A memory that
  changed since the preview, or whose file looks like it contains a secret, stays in the old project. Each memory
  becomes two entries in the Changes view, one in each project, so you can revert either there. If the
  move is interrupted, run it again: nothing is lost and it finishes where it stopped. This works with the
  sessions' switch on or off and is only available from the Console. When exactly one project that has a
  working copy uses the same git remote as the old one (the same repository cloned again), the panel suggests
  it with a **Review the move** button, which only fills in the two choices and shows the preview. Nothing moves
  until you confirm. With two such working copies of the repository there is no suggestion; pick the target
  yourself. Agent Fleet remembers the remote of each project, without any password or token from the URL;
  a project whose working copy was already gone has none and is never suggested. For ssh addresses only the large hosting services (GitHub, GitLab, Bitbucket, Codeberg) are recognised; on any other server only https addresses are compared.

- **From a terminal**: `af-memory import-sources`, `af-memory import --project <slug> --dry-run` (preview) and
  `af-memory import --project <slug>` (import); `af-memory changes [--limit N]` lists the latest changes (30 unless you say otherwise). For muse:
  `af-memory muse-import-sources`, `af-memory muse-import --dir <name> [--scope project|personal_project] [--dry-run]`
  and `af-memory muse-import --scope personal --into-user [--dry-run]`. The imports are
  gated by the same switch and the same secret scan as the Console.

- **From a terminal**: `af-memory export-sources`, `af-memory export --project <id|name> --dry-run` (preview) and
  `af-memory export --project <id|name> [--overwrite <name>]...` (write back).

## Memory snapshots

Version control over the memory an agent accumulates by itself (claude's auto-memory, codex's memories), so
"it learned something it shouldn't have" and "when did this go wrong" are fixable after the fact. This is separate from
[Agent memory](#agent-memory), which is Agent Fleet's own shared memory. The page shows the last snapshot, whether
automatic snapshots are on and **Snapshot now** at the top, over three views: **History · Targets · Export & import**.
The view you leave open is remembered. It needs the running workspace.

### History

- **History**: newest first, with the time and the trigger (auto / manual / pre-restore / pre-write-back / restore /
  import / agent memory). You can also jump to a point in time by date. The snapshots that Agent Fleet memory writes leave
  (trigger "agent memory") are hidden, because Agent memory > Changes already lists those writes: a line "Agent Fleet memory
  writes hidden: N" stands in for each run of them, with a link to Changes, and **Show Agent Fleet memory writes**
  lists them (the choice is remembered on this device). A date jump that lands on one shows it, and its diff and
  **Restore this point** work like any other row's.

- **Restore this point**: pick the scope (everything, or select what to restore). **The state just before
  the restore is snapshotted too**, so the restore itself can be undone. You are warned if a session of that
  kind is running.

### Targets

- **Targets**: what can be versioned, with file count, size and the last snapshot. codex has memory disabled by
  default, so enable it here if you want it. Turning on Agent memory switches codex's memory off once; if you
  turn it on here after that, it stays on beside Agent memory and is left on when Agent memory is turned off
  (see [Set how memory is used](../user/memory/settings.md)).
- **Automatic snapshots**: taken a few minutes after an agent stops (nothing is stored if nothing changed).
  "Snapshot now" takes one by hand. On some deployments the operator has disabled automatic snapshots.

### Export & import (Memory snapshots)

- **Export & import**: bundle (full history) or tar.gz (latest only). If what you are about to export looks
  like it contains secrets, you are warned and asked to confirm first.
  - **Agent Fleet memory from a bundle**: restoring or importing a bundle here never writes Agent Fleet memory
    (the restore has no secret scan). When the bundle you picked carries some, a line under it says "This bundle
    also contains Agent Fleet memory — import it in Agent memory › Import & export." (with an **Open Import &
    export** link); the import itself is **Agent Fleet memory in this bundle** under Agent memory > Import & export.
  - **Size limit**: a bundle or tar.gz up to 64 MiB is taken in. An administrator can raise that to at most 128 MiB with `AF_MEMORY_IMPORT_MAX` ([ref/limits.md](../ref/limits.md)); a larger value is clamped, because the Control Plane refuses bigger uploads.

## Using Agent Fleet memory from an external agent

The same MCP token, or an app you connected by signing in (OAuth), can also use the memory Agent Fleet keeps; the
consent page says so next to `read` and `write`. For example your own Claude Code / Claude Desktop (see
[Agent memory](#agent-memory); how to make a token and connect a client is in
[Drive your workspace from an external Claude](../user/outside/external-claude.md#issue-a-token)). Two things have to be true: the scope of the token or OAuth connection, and **⚙ Settings → Agent
memory → Settings → "Use Agent Fleet memory from outside Agent Fleet"** (`Off` by default; `Read only` or `Read and write`).
The "MCP tokens & apps" tab (under Outside access) lists what the token can do (including that a tenant Block hides these tools) and links to that setting; the setting has a "How to connect"
link back.

- **Use the memory-only endpoint for claude.ai.** `https://<your Console address>/mcp/memory` serves these tools and
  nothing else, and only tokens and connections made for it work there. claude.ai passes its connectors on to the
  Claude Code sessions in your Workspace; a connector to `/mcp/memory` gives them nothing they do not already have,
  while one to `/mcp` would give them your full authority
  ([19 Using it from outside](19-outside-access.md#a-memory-only-connector-for-claudeai)).
- **Your tenant administrator can block it.** If they have, the memory tools are not offered at all, whatever your
  setting and scope say ([admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet)).
- **Tools**: `memory_projects`, `memory_index`, `memory_search` and `memory_read` need a read (or higher) token or OAuth connection;
  `memory_save`, `memory_patch`, `memory_feedback` and `memory_forget` need a write token or OAuth connection and "Read and write". The setting and the scope both have
  to allow a call: the narrower one wins.
- **Asking the agent**: just say it in plain words in your local client, e.g. "search my Agent Fleet memory for how
  we run the integration tests" or "save to Agent Fleet memory that this repo's lint step needs `make gen` first".
  The agent writes facts, not secrets: text that looks like one is refused.
- **Choosing the project**: the tools take `repository`, the output of `git remote get-url origin` in the
  repository the agent is working in. Agent Fleet matches it against each project's remote (`memory_projects` lists
  them), so `https://`, `ssh://` and `git@host:owner/repo` spellings, a trailing `.git` or `/`, and the case of the
  host all find the same project. A project's `remote` exactly as `memory_projects` lists it
  (`github.com/owner/repo`) is accepted too. `project` (an id from `memory_projects`) still works; if both are given they must
  name the same project. A remote that matches no project, or more than one, is refused with a message saying so (a
  project that holds no memory yet is started by the first save when exactly one working copy of that remote is
  cloned under `~/repos` in the workspace; with none, or with several, the save is refused: clone the repository
  first, or pass `project` from `memory_projects`, which also lists such working copies with a count of 0). ssh and `git@` spellings
  and `https://` remotes are matched on github.com, gitlab.com, bitbucket.org and codeberg.org only. A self-hosted host is
  refused (the stored remote does not keep the port, so two services on one host name could be confused): pass `project`.
- **Saving and forgetting need a scope**: `memory_save`, `memory_patch` and `memory_forget` require `scope`: `project` (with
  `repository` or `project`) for a fact about one repository, `user` only for a fact that holds in every project.
  A call without `scope` is refused with a message naming what to pass, so a forgotten project can no longer
  publish a repository-specific fact to every project.
- **Kind and anchors**: the external `memory_save` accepts the same optional `kind` and `anchors` as the in-workspace one (omit either to keep what is there; pass `[]` for `anchors` to clear them). The lifecycle fields are not settable from outside.
- **Reporting a memory that failed you**: `memory_feedback` takes `scope`, `name`, `revision`, `verdict` (`stale`, `wrong` or `confirmed`) and `note`, with the same rules as inside the workspace: a report on the current revision marks the memory as suspect, one on an older revision is only kept as an observation, `confirmed` never clears a doubt, and only the first report per connection per revision counts. The connection is the token or OAuth connection you made; the tool takes no session argument.
- **What is recorded**: a save shows in the Agent memory list as written by author kind `external`, and the
  commit message carries an `AF-External-Client: <name>` trailer with the client's self-declared name (for
  information only). Each external save, patch, feedback report and forget is also written to the audit log as `memory.entry.save` /
  `memory.entry.patch` / `memory.entry.feedback` / `memory.entry.forget`, with the project id and never the memory's name. You can revert or forget any of it from
  the Agent memory list.
- **What leaves the workspace**: the memory text reaches that client's model, outside the workspace's egress
  controls. The secret scan stops credentials, not customer names or internal context.
- **Saves** go through the same revision check, secret scan and limits as an agent's save inside the workspace.
- **A stopped workspace** answers that it must be started first; start it in the Console and ask again.
- A deployment that does not serve `/mcp` has no outside access at all, `/mcp/memory` included.

### Bringing your own Claude Code's memory into Agent Fleet

**Import from Claude Code** (under Import & export) reads only the Claude Code inside your workspace. The memory
Claude Code keeps on your own computer can come across through `/mcp` instead: the Claude there reads its memory
files and saves each one with `memory_save`. Every save goes through the secret scan and shows in **Changes**, so
each one can be reverted.

Before you start:

1. In **⚙ Settings → Agent memory → Settings**, turn on "Let sessions use Agent Fleet memory", so the sessions in
   your workspace read what you bring in.
2. In the same view, set "Use Agent Fleet memory from outside Agent Fleet" to **Read and write**.
3. Connect Claude Code on your computer to `/mcp` with the `write` scope
   ([Move work from your own Claude Code into the workspace](../user/outside/move-work-in.md)
   shows the commands). If the memory tools do not appear, your tenant administrator has blocked them.
4. The repository is cloned in the workspace, under `~/repos`. A clone that holds no memory yet is started by the
   first save, so no session has to save one first. If the workspace holds several clones of the same remote,
   the save is refused with their ids: pass `project` (an id from `memory_projects`) to choose one.

Then save this skill once on your computer, at

```
~/.claude/skills/af-memory-import/SKILL.md
```

````markdown
---
name: af-memory-import
description: Copy this project's Claude Code memory into Agent Fleet memory
disable-model-invocation: true
---
Copy the memory you keep for this project into Agent Fleet memory. Change nothing in your own memory files.

1. Run `git remote get-url origin`; that is `repository` for every call below. Call agent-fleet
   `memory_projects` and check that it lists this repository (a repository cloned in the workspace is listed even before it holds any
   memory, with a count of 0). If it does not, the repository is not cloned in the workspace: stop and tell me.
2. List the files in your memory directory, except the index `MEMORY.md`. For each file, show me its name,
   type and description in one table, and propose a scope: `project` for anything about this repository,
   `user` only for a fact that holds in every project. Wait for my OK on the table.
3. For each file I approved:
   - Call `memory_search` with its name. If a memory of that name exists, `memory_read` it; when the text is
     the same, skip it; when it differs, show me both and ask which to keep. To replace it, pass the
     `revision` you read.
   - Call `memory_save` with `scope`, `repository`, `name` (lower case; letters, digits, `-`, `_`, `.`;
     at most 64 characters), `description` (one line), `type` (`user`, `feedback`, `project` or `reference`)
     and `body` (the file's text without its frontmatter).
   - If a save is refused, for example as a possible secret, do not retry it in other words. Note the reason.
4. Report what was saved, skipped and refused.
````

In the Claude Code session of that repository, type `/af-memory-import`. Afterwards, look at **Changes** in Agent
memory: the saves are listed with author kind `external` and can be reverted there.

Good to know:

- Instruction files (`CLAUDE.md`) are not memory. Put what they say in Settings → Agent instructions, or keep the
  repository's own file in the repository.
- Your own memory files are not changed. If you keep using Claude Code on your computer, what it learns later
  stays there until you run the skill again; a memory you already brought in is skipped when its text is the same.

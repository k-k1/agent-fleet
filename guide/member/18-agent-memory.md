---
audience: "anyone who wants agents to remember what they learned, or to undo what they remembered"
---

# 18. Agent memory — what agents remember, and how you keep it

English | [日本語](18-agent-memory.ja.md)

Two features keep what agents learn. **Agent memory** is Agent Fleet's own memory, shared by every kind of agent.
**Memory snapshots** version the memory an agent keeps in its own files. Both are pages of ⚙ Settings.

## Agent memory

Agent Fleet's own shared memory: the memory every kind of agent reads and writes with the af tools
`memory_index` / `memory_search` / `memory_read` / `memory_save` / `memory_forget`. It is per project (a worktree shares its
repository's), plus one user-wide scope, so what one agent learns survives a switch of kind, a handoff and a child of
another kind. **It is off by default.** Turn it on in the [Settings](#settings) view. The version history of the memory the agents keep in their own files is
[Memory snapshots](#memory-snapshots), further down.

The page opens with one status line: whether it is on, how many memories in how many projects, when it last changed,
and a warning, with a link to Settings, when the background sync with Claude Code needs you. Under it are four views:
**Memories · Changes · Import & export · Settings**. The view you leave open is remembered. While the workspace is stopped
only **Settings** works (the switches are saved for the next start); the other views offer **Start the workspace**. On a
phone or a tablet in portrait the tabs stay on one row and scroll sideways when they do not all fit; the arrow keys,
Home and End move between them.

### Memories

- **List and detail**: the left side lists the memories of **All scopes** (the default: the user-wide scope and every
  project together, each row tagged with where it lives) or of one scope (**User-wide**, which every project receives,
  or one project), with their author, last update, how often agents read them and a **pinned** marker. The chips
  **All**, **Pinned** and **Forgotten** narrow it. Search looks for what you typed, as one piece of text, in the name,
  description, type, author and author session at once; after a short pause it also searches the bodies, and
  memories only the body search found are listed after those, ranked like the agents' `memory_search`; searching is not a use and does not change how
  often a memory is read. The command palette's **Memory** scope searches the same memories from anywhere
  ([02](02-console-basics.md#memory)). Pick a row and the right side shows that memory: its text, scope, type, author, last update
  and how often agents read it, with **Edit**, **Pin** / **Unpin**, **Forget this memory** and, under **More actions**
  (⋯), **Purge history…**. **New memory** (in the scope on screen; the user-wide scope while all scopes are shown) and
  **Edit** open a form in the same place. The scope, the chip and the memory you picked are remembered while you move
  between views, and forgotten when you reload the page. On a phone the list and the detail are two screens: picking a
  row opens the detail, and **Back to the list** (or Escape) returns. A memory whose name or text looks like a
  secret is never listed, only counted.

- **Pinned memories**: **Pin** one in the detail pane and it is always listed first in the index agents read when they
  start work, ahead of everything else; **Unpin** takes it back. The **Pinned** chip lists them. The rest
  of the index is ordered by type (feedback and user memories first), then by how often agents read it (a
  read or a search hit that was returned counts), then by recency. Pins share the index's size limit (about 24 KiB of
  described lines, where each description is cut to 150 characters; `memory_read` returns the full text): if
  the pinned ones alone do not fit, the last of them drop to the names-only list and the agent is told so,
  so pin only what every session needs. `memory_search` ranks its hits too: more of the query's words first,
  then a match in the name before the description before the body, then pins, reads and recency; if no
  memory holds every word it returns the ones that hold some and says so. A pin is recorded as a change by you ("pin changed") and can be
  reverted; an agent's later update of a pinned memory keeps the pin, and the af memory tools offer agents no way to pin or unpin.

- **Forget, Unforget and history**: **Forget this memory** removes it from every agent's view. The **Forgotten** chip
  lists the ones you can **Unforget**, on the row or in the detail pane (the text comes back as it was before; if it now
  looks like a secret, use **Revert this change** on its forget entry in the Changes view, where you can confirm). Both are the same revert and forget as in
  the Changes view, recorded as made by you and undoable. Forget works on any listed memory whose history holds a change: its newest
  change is looked up when you press it, however old. A memory file that was put in place by hand, with no change
  in the history, cannot be forgotten from here. **Forgotten** lists every memory forgotten in the scope you have selected (all scopes
  by default), however long ago, up to 300 at a time. With all scopes shown, picking one scope shows more of
  it; one scope also shows only its newest 300. With a very large number of
  forgotten memories (over about 2000) the oldest are not checked: the list says so, may then be missing some, and the
  hidden count is a minimum. **History of this memory**, at the bottom of the detail pane, lists that memory's own
  changes; pick one to see its diff, as in the Changes view. It is read from the newest 300 changes of all memories, so
  for a memory last changed long ago it can be incomplete: it then says so, and **See all in Changes** opens the Changes
  view. Saves are the same revision check and secret scan as an agent's save, recorded as written by you and undoable from
  the Changes view. A memory can be written into a project only once that project holds memory. An edit keeps the kinds
  the memory was limited to. The Control Plane signs pin, revert, forget and save, and the workspace accepts
  them only with that signature, so a session in the workspace cannot make them as you. For about half a
  minute after a workspace starts they ask you to try again in a minute.

- **Purge history…**: **Forget** only hides a memory; its earlier text stays in the history and in every export.
  **Purge history…** (under **More actions** in a memory's detail pane, and in a forgotten memory's row and detail pane) deletes a memory's past revisions from this
  workspace for good. A dialog first lists what will be rewritten, without showing any text: how many of the
  memory's change records go, how many other snapshots carry a past revision, which histories are touched
  (the main history, imported histories, rescue copies of an earlier history) and what the secret scan
  reports in those past revisions (rule, line and a masked hint only). You then type the memory's name to
  confirm. What happens: the memory's file is removed from every commit of every history and the commits
  that held only it are dropped; the unreachable objects are garbage-collected and each purged revision is
  checked to be gone from disk. The memory's **current text stays**, so the memory and its history still agree; if the
  current text is what must go, edit the memory first and then purge, which makes the old text a past
  revision. A forgotten memory loses every revision, including the forget record, and cannot be unforgotten
  afterwards. Other memories' history, and their revert, are untouched. After a purge the memory's remaining
  change cannot be reverted (there is nothing earlier to go back to); you can still forget it. **Exports you
  already made cannot be recalled**: a bundle (full history) or archive you downloaded, or copied
  elsewhere, still holds the old text, so delete those copies yourself. Temporary export or upload files an
  interrupted transfer left inside the workspace are removed by the purge. A purge asks for the Control Plane's
  signature like revert and save, so a session cannot start one, and the Control Plane records it as
  `memory.entry.purge` with the project only, never the memory's name or text. If the history changes between the
  preview and the confirmation, the purge asks you to review it again.

### Changes

The list shows the newest 100 changes (when, which agent and session, what), newest first;
`af-memory changes --limit N` lists up to 300 from a terminal. On a memory's newest change
you can **revert it** (the earlier text comes back) or **forget the memory**; either is recorded as a new
change, so it can be undone too. If the text you bring back looks like a secret, you are shown the masked
findings and asked to confirm.

While **Have finished sessions suggest memories** is on (it takes effect only while "Let sessions use Agent Fleet
memory" is on too), or while suggestions are still waiting, the view has two
parts: **Published** (the list above) and **Proposed**, with the number of suggestions waiting beside it. The same
number sits beside **Changes** in the view bar, and a notification ("N memory suggestions are waiting for your
approval", under the **Other notices** row of the notification settings) opens this view. If any are waiting, the view opens
on **Proposed**.

- **Proposed** lists what a memory review suggested (the background review, or one you asked for with **Suggest memories now**), oldest first, with the reason it gave and the difference
  from the memory as it is now. **Nothing in it is in memory yet.** **Approve** publishes it as a change by
  `review` (the session it was suggested from is shown as the author session); you can undo it like any other change.
  **Reject** deletes its text and keeps only a fingerprint, so the same suggestion is not made again until that memory
  is forgotten. **Reject all** rejects every suggestion, and **Reject all from this review** the ones of the review
  you picked.
- A suggestion **can't be approved**, only rejected, when the memory has changed since the review looked at it (the
  difference is against what you have now), when it was forgotten (a review never brings a forgotten memory back), when
  the suggestion now matches a secret rule (its text is then not shown at all), or when its project is no longer here.
  Approving also runs the store's secret scan again.
- On a change the review published, **Undo this review's changes** puts back, as new changes, everything that review
  published, each memory only if nobody changed it afterwards. A memory that was edited since is left as it is and
  named in the result (with the reason); running it again retries only what is left.
- Only you can approve, reject or undo: the Control Plane signs those requests and the workspace refuses them from a
  session, and the list of suggestions is not readable from a session either. The Control Plane records each decision
  (without any text) in the audit log.

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
  time. Turning the switch off already writes every project back by itself (see [Settings](#settings)); use this to
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

### Settings

**It is off by default.** Turn on "Let sessions use Agent Fleet memory" first, here in this view: on reaches sessions
started from then on, and off refuses running sessions' reads and writes at once. The Memories and Changes views work
either way. While it is on, every kind is also told, in its own instruction
file, that the tools exist and when to call them (a short fixed note; it never contains memories, and turning
the switch off removes it without touching your own text there). Local-model (lcpp) sessions additionally
start with the project's memory index in their system prompt; Cursor has no such file, so its guidance is in
the tool descriptions. A save is shared at once, **without your approval**; text that looks like a secret is
refused.

- **Claude Code's own memory**: the switch carries it across by itself. Flipping "Let sessions use Agent Fleet
  memory" first asks you to confirm, with counts from a preview. On the way in, **Turn on Agent Fleet memory?** says
  how many memories from how many projects are imported now and how many Claude files are skipped as possible
  secrets (**Turn on and import**). On the way out, **Turn off Agent Fleet memory?** says how many memories are
  written back, after a snapshot of Claude's memory and without overwriting anything (**Turn off and write back**).
  While the workspace is stopped there are no counts yet, and the import or write-back runs when it starts. **On**: Agent Fleet imports every Claude project it can match to a working copy under `~/repos`
  in the background, and starts new Claude sessions with Claude Code's auto-memory switched off, so Claude
  does not load its own `MEMORY.md` (about 25 KB at the start of every session) and uses the af tools like
  every other kind. The automatic import is stricter than **Import from Claude Code** (under Import & export): a Claude file
  newer than an Agent Fleet copy *you edited here* is reported as a conflict and left alone (the manual
  import still replaces it, as your own choice); a memory you forgot is never brought back; a file that looks
  like it holds a secret is skipped and listed. Claude sessions that were already running keep their own memory on; when the last
  of them ends, what they saved is imported once more. **Off**: Agent Fleet writes every project back into
  Claude Code's memory (the same rules as **Write back to Claude Code**, under Import & export: a snapshot first, nothing
  overwritten, conflicts and secrets kept and listed), and Claude Code's own memory stays off until that has
  finished, so a session started in between cannot write files the write-back would then trip over. If the
  write-back keeps failing, Claude Code's memory stays off and Agent Fleet retries with growing pauses; the
  page then offers **Retry now** and **Switch native memory back on anyway**. Under the switch the page shows
  where the run is, what the last one did and the files it left (conflicts, skipped secrets), and one
  notification tells you when a run left any. The work survives a restart of the workspace: it
  resumes where the phase says it was, and a switch that changed while the workspace was stopped is carried
  across when it starts. Codex: turning the switch on switches codex's own `features.memories` off, and
  turning it off puts back the value you had (never turns it on); nothing is imported from or written into
  codex. If you edit that flag while the switch is on, your value is the one restored. Only Claude sessions *started* after the change are
  affected; a running session keeps what it started with.

- **Muse Code's own memory**: while the switch is on, Agent Fleet starts Muse Code with its native memory
  switched off: Muse has no `read_memory` / `add_memory` / `edit_memory`
  tools, no memory snapshot in its context and no "use persistent memory" paragraph in its prompt, so it uses
  the af tools like every other kind. While the switch is on it wins over a native-memory setting in your own environment; with the switch off
  Agent Fleet changes nothing and Muse keeps its default. This covers
  Muse sessions and the assistant chat. Only Muse hosts *started* after the change are affected; a running
  session keeps what it started with.

- **From outside Agent Fleet** (setting "Use Agent Fleet memory from outside Agent Fleet", **off by default**
  and independent of the switch above; **Read only** or **Read and write**): lets your own Claude Code or Claude
  Desktop on another machine, or an app you connected with OAuth (such as claude.ai), use this memory through the
  Control Plane's `/mcp`. The setting is
  your own; your tenant administrator can still block it for the whole tenant. What it allows, and what is
  recorded, is in [Using Agent Fleet memory from an external agent](#using-agent-fleet-memory-from-an-external-agent).

- **Have finished sessions suggest memories** (setting "Have finished sessions suggest memories", **off by default**;
  it also needs the switch above): choosing **Propose** turns the review on, which by itself does nothing in the
  background. You ask for it from a session's **⋯** menu with **"Suggest memories now"**: an excerpt of the
  conversation is read by an agent that has **no tools and no MCP servers**, which lists what looks worth keeping as
  *proposals*. The excerpt is redacted first (anything that looks like a secret is left out), but it is still sent to
  the provider of the agent chosen for this feature under **AI assistance**, which can be a different provider from the
  session's own; it spends tokens (at most 250 calls a day, one at a time), and it does not run while the workspace is
  stopped. **Nothing reaches memory without your approval**: up to 100 proposals wait under
  [Changes › Proposed](#changes), where you approve or reject them. When the setting is on, a **Background memory review**
  card appears under AI assistance, where you can pin the agent and model. Only agents that can be run with their
  tools switched off can do the review, and today that is claude alone; when none is available here, the setting
  says so and the review waits; if the one you pin cannot, the review does not run rather than falling back to another
  provider.
- **Also suggest automatically** (setting "Also suggest automatically", **off by default**, separate from the one
  above; it works only while **Propose** is chosen): with it on, finished sessions are reviewed in the background
  without you asking. A session is picked up 5 minutes after it paused, once it has gained 30 turns or 3 of your
  prompts since its last review, and only if its last turn ended within the last 48 hours; older ones, or what
  happened while the workspace was stopped, are covered by **"Suggest memories now"**. The cost is the same tokens as
  above (the 250 calls a day are shared with the manual button), spent with nobody watching. Turning it off stops new
  reviews right away: a call already running is allowed to finish, and the proposals it brings still wait for your
  approval, but nothing further is started and a failed call is not retried.

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
  (see [Settings](#settings)).
- **Automatic snapshots**: taken a few minutes after an agent stops (nothing is stored if nothing changed).
  "Snapshot now" takes one by hand. On some deployments the operator has disabled automatic snapshots.

### Export & import (Memory snapshots)

- **Export & import**: bundle (full history) or tar.gz (latest only). If what you are about to export looks
  like it contains secrets, you are warned and asked to confirm first.
  - **Agent Fleet memory from a bundle**: restoring or importing a bundle here never writes Agent Fleet memory
    (the restore has no secret scan). When the bundle you picked carries some, a line under it says "This bundle
    also contains Agent Fleet memory — import it in Agent memory › Import & export." (with an **Open Import &
    export** link); the import itself is **Agent Fleet memory in this bundle** under Agent memory > Import & export.

## Using Agent Fleet memory from an external agent

The same MCP token, or an app you connected by signing in (OAuth), can also use the memory Agent Fleet keeps; the
consent page says so next to `read` and `write`. For example your own Claude Code / Claude Desktop (see
[Agent memory](#agent-memory); how to make a token and connect a client is in
[19 Using it from outside](19-outside-access.md#driving-your-workspace-from-an-external-claude-mcp)). Two things have to be true: the scope of the token or OAuth connection, and **⚙ Settings → Agent
memory → Settings → "Use Agent Fleet memory from outside Agent Fleet"** (`Off` by default; `Read only` or `Read and write`).
The "MCP tokens & apps" tab (under Outside access) lists what the token can do (including that a tenant Block hides these tools) and links to that setting; the setting has a "How to connect"
link back.

- **Your tenant administrator can block it.** If they have, the memory tools are not offered at all, whatever your
  setting and scope say ([admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet)).
- **Tools**: `memory_projects`, `memory_index`, `memory_search` and `memory_read` need a read (or higher) token or OAuth connection;
  `memory_save` and `memory_forget` need a write token or OAuth connection and "Read and write". The setting and the scope both have
  to allow a call: the narrower one wins.
- **Asking the agent**: just say it in plain words in your local client, e.g. "search my Agent Fleet memory for how
  we run the integration tests" or "save to Agent Fleet memory that this repo's lint step needs `make gen` first".
  The agent picks the project with `memory_projects` (a project must already hold memory; new projects are
  started from inside the workspace) and writes facts, not secrets: text that looks like one is refused.
- **What is recorded**: a save shows in the Agent memory list as written by author kind `external`, and the
  commit message carries an `AF-External-Client: <name>` trailer with the client's self-declared name (for
  information only). Each external save and forget is also written to the audit log as `memory.entry.save` /
  `memory.entry.forget`, with the project id and never the memory's name. You can revert or forget any of it from
  the Agent memory list.
- **What leaves the workspace**: the memory text reaches that client's model, outside the workspace's egress
  controls. The secret scan stops credentials, not customer names or internal context.
- **Saves** go through the same revision check, secret scan and limits as an agent's save inside the workspace.
- **A stopped workspace** answers that it must be started first; start it in the Console and ask again.
- A deployment that does not serve `/mcp` has no outside access at all.

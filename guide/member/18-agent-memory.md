---
audience: "anyone who wants agents to remember what they learned, or to undo what they remembered"
---

# 18. Agent memory — what agents remember, and how you keep it

English | [日本語](18-agent-memory.ja.md)

Two features keep what agents learn. **Agent memory** is Agent Fleet's own memory, shared by every kind of agent.
**Memory snapshots** version the memory an agent keeps in its own files. Both are pages of ⚙ Settings.

## Agent memory

Agent Fleet's own shared memory: the memory every kind of agent reads and writes with the af tools
`memory_index` / `memory_search` / `memory_read` / `memory_save` / `memory_patch` / `memory_feedback` / `memory_forget`. It is per project (a worktree shares its
repository's), plus one user-wide scope, so what one agent learns survives a switch of kind, a handoff and a child of
another kind. **It is off by default.** Turn it on in the [Settings](#settings) view. The version history of the memory the agents keep in their own files is
[Memory snapshots](#memory-snapshots), further down.

The page opens with one status line: whether it is on, how many memories in how many projects, when it last changed,
and a warning, with a link to Settings, when the background sync with Claude Code needs you. Under it are two views:
**Settings · Import & export**. The view you leave open is remembered. While the workspace is stopped
only **Settings** works (the switches are saved for the next start); the other view offers **Start the workspace**. On a
phone or a tablet in portrait the tabs stay on one row and scroll sideways when they do not all fit; the arrow keys,
Home and End move between them.

**The Memory view.** Reading a body, a diff or a list side by side does not fit in a dialog, so **Memories**, **Changes** and
**Suggestions** live in a pane of their own, the *Memory* tab, like the image gallery. Open it with **Open memory view** at the
top of Settings › Agent memory, or from the daily notice (which lands on **Suggestions**) or a memory in the command palette.
There is one Memory view per workspace; opening it again focuses it. It works like the views below, only larger; the sections
named **Memories**, **Changes** and **Suggestions** in this chapter are its three tabs.

### Memories

- **List and detail**: the left side lists the memories of **All scopes** (the default: the user-wide scope and every
  project together, each row tagged with where it lives) or of one scope (**User-wide**, which every project receives,
  or one project), with their author, last update, how often agents read them and a **pinned** marker. The chips
  **All**, **Pinned** and **Forgotten** narrow it. Search needs every word you typed (in any order) to appear in the name,
  description, type, author and author session taken together; after a short pause it also searches the bodies, and
  memories only the body search found are listed after those, ranked like the agents' `memory_search`. When no memory holds every word, the body search returns the ones that hold some of them, and the list then shows only those, under a "some words only" notice (a memory that matches in full by name, description, type or author always wins over them); searching is not a use and does not change how
  often a memory is read. The command palette's **Memory** scope searches the same memories from anywhere
  ([02](../user/start/shortcuts.md#find-and-open-with-the-command-palette)). Pick a row and the right side shows that memory: its text, scope, type, author, last update
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

- **Kind and lifecycle fields (optional)**: a memory file can carry `kind` (`preference`, `lesson`, `procedure`, `state`
  or `log`: how it goes stale), `anchors` (the paths, symbols, test names, ADR numbers and commands it depends on),
  and a lifecycle: `status` (`active`, `quiet` or `archived`) with `status_by`, `superseded_by` (a memory, or a
  repository document), `invalid_at` and `reasons` (open doubts; a memory with any is *suspect*). An agent may set
  `kind` and `anchors` with `memory_save` (leave them out to keep what is there); the lifecycle fields are set by lint,
  feedback, the consolidator or you, never by an agent and never by an import. A memory with none of them behaves as
  before. What agents see follows one rule on every path (`memory_index`, `memory_search`, `memory_read`, the external
  memory tools, the local-model session prompt and the write-back to Claude): an **archived** or **superseded** memory is
  not listed, not found by search and not injected, and `memory_read` answers with a stub (name, status, what replaced
  it, reasons) instead of the text; a **quiet** memory or a `log` is not in the described part of the index, even when
  pinned, but is in the names-only list, in search and in `memory_read`; a **suspect** memory is shown with its reasons
  on every line, hit and read. Your own views (the list, history, diff, restore and exports) show everything. The
  write-back to Claude skips archived, superseded, quiet, log and suspect memories, and **removes** a copy Agent Fleet
  wrote earlier when its memory stops qualifying (shown in the preview as **remove**); a copy you edited in Claude is
  kept, shown as a conflict, and left out of `MEMORY.md`. Reverting a change restores the earlier version whole, these
  fields included.

- **Forget, Unforget and history**: **Forget this memory** removes it from every agent's view. The **Forgotten** chip
  lists the ones you can **Unforget**, on the row or in the detail pane (the text comes back as it was before; if it now
  looks like a secret, use **Revert this change** on its forget entry in the Changes view, where you can confirm). Both are the same revert and forget as in
  the Changes view, recorded as made by you and undoable. Forget works on any listed memory whose history holds a change: its newest
  change is looked up when you press it, however old. A memory file that was put in place by hand, with no change
  in the history, cannot be forgotten from here. **Forgotten** lists every memory forgotten in the scope you have selected (all scopes
  by default), however long ago, 300 at a time, newest first; **Load older forgotten memories** reads the next 300 until the list ends. With a very large number of
  forgotten memories (over about 2000) one request does not check them all: the list says so, the
  hidden count is a minimum, and the older ones are checked as you load more. **History of this memory**, at the bottom of the detail pane, lists that memory's own
  changes; pick one to see its diff, as in the Changes view. It is read from the changes of all memories, newest 300 first, so
  for a memory last changed long ago it can be incomplete until you press **Load older changes** (300 more each time, back to the first change): it says so while older changes remain, and **See all in Changes** opens the Changes
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

The list shows the newest 100 changes (when, which agent and session, what), newest first, and **Load older changes** adds 100 more each time, back to the first change;
`af-memory changes --limit N` lists up to 300 from a terminal. On a memory's newest change
you can **revert it** (the earlier text comes back) or **forget the memory**; either is recorded as a new
change, so it can be undone too. If the text you bring back looks like a secret, you are shown the masked
findings and asked to confirm.

A change that a past memory review published (author `review`, from before the review was removed, see below) offers
**Undo this review's changes**: it puts back, as new changes, everything that review published, each memory only if
nobody changed it afterwards. A memory that was edited since is left as it is and named in the result (with the
reason); running it again retries only what is left. Only you can undo: the Control Plane signs the request and the
workspace refuses it from a session. The Control Plane records it (without any text) in the audit log.

### Import & export

Importing memory from Claude Code, muse or a bundle, writing it back to Claude Code and moving the memories of a project
that no longer exists are described in [Import and export memory](../user/memory/import-export.md).

### Lint report

**Import & export → Memory lint report** (and `af-memory lint [--user | --project <id|name>] [--json]` from a
terminal) checks the memories of one scope and writes nothing: no memory changes, no read is counted as a use, and no
working copy is touched. It reports, per memory:

- a `[[link]]` to a memory that does not exist;
- a repository path (only a path whose first folder exists in the repository), a `Test*` or code-shaped `symbol` in
  backticks, or an `ADR 0123` that is missing from the project's default branch;
- a term from your **retired terms** list (one per line, edited in the same panel; case-insensitive, whole words for
  plain words). Nothing is ever added to the list for you;
- a body over 4,096 bytes, the part other sessions see in recall.

The repository is read by ref, from `origin`'s default branch in the repository's git directory, never through a
working copy and without fetching, so the report says which ref and commit it read: a stale clone gives a stale
answer. A project without git, an orphaned project (its working copy is gone) and the user scope get the link,
term and size checks only, and the report says what it skipped. A path or name that was renamed is reported as missing:
treat a finding as a question, not a verdict.

The panel (and `af-memory lint`) also shows **proposed retired terms**, read from the same default branch: the name of
a fully superseded ADR, or an `AF_*` key that a recent commit removed and no file mentions any more. Each comes with its
evidence (the ADR's status line, or the commit) and is shown only when a memory in the scope uses it. A proposal never
enters your list by itself: **Add to list** appends it, **Reject** remembers it so it is not proposed again.

### Reasons from lint and from readers

`af-memory lint --apply` (same scope flags) writes what the report finds. Each link, path, symbol, ADR or retired-term
finding becomes a `reasons` entry on the memory (a size finding does not), so the memory is shown as *suspect* wherever an
agent reads it and is not written back to Claude. Each memory changes in one commit authored `lint`, which also raises
its revision, and only if it is still at the revision that was checked; at most 100 memories are written per run, and
running it again continues. A lint reason is removed when the memory has been changed by someone since and the rule no
longer fires; a rule that could not run (no repository) removes nothing. When a missing path was moved by a git rename on
the default branch, lint replaces only that path in the body (and in `anchors`), as its own commit that names the renaming
commit, and the change can be reverted like any other.

**Scheduled lint (opt-in).** Settings › Agent memory › **Settings** › *Automatic lint marks* runs the same thing as
`af-memory lint --apply` on its own, over the user scope and every project that holds memory, so reasons appear without anyone
running a command. It is **off by default**, it also needs agent memory on, and only the Console can switch it on (a session's
own token cannot); it is separate from the tidy-up switch, because lint uses no model and spends no tokens. A run starts only when no session is
working and none has run a turn for 15 minutes, at most once every 6 hours (30 minutes when the 100-memory cap left some
unwritten), and stops between scopes if a session starts working. The 100-memory cap covers the whole run. A failing run
is retried after 15 minutes, doubling up to 6 hours. A manual `af-memory lint --apply` and a scheduled run never overlap: a
manual one started during a scheduled run is refused with "already in progress" and can simply be run again afterwards; if that happens part-way through a run over all scopes, the scopes already done keep their changes and running it again finishes the rest. The
switch and the timing are kept across restarts of the workspace.

Agents report a memory that failed them with `memory_feedback` (the revision they read, `stale` or `wrong`, and a note).
A report on the current revision adds a `feedback` reason, which any change of the body clears; a report on an older
revision is only kept as an observation. One report per session per revision counts. A confirmation counts but never
clears a reason, and agents are only asked to report the negative case.

### Consolidation proposals (opt-in)

Agent Fleet can tidy the memory in the background: it collects what readers and lint reported (`stale` / `wrong` reasons,
lint reasons, and the proposals the retired 0117 review left unanswered), groups them by memory, and — when it has a reason —
asks a model for a **small patch** (never a rewrite). **Nothing is ever written to your memory without your approval**: a run
only queues *proposals*.

- **Turning it on.** Settings › Agent memory › **Settings** › *Memory tidy-up (suggestions)*. It is **off by default**, it also
  needs agent memory on, and turning it on sends memory text to a model and spends tokens while the workspace is idle. Only the
  Console can switch it (a session's own token cannot). Below the switch the **status** shows the last run, the earliest next
  run, how many suggestions wait, and each topic that is ripe but delayed with the cause (token budget, workspace stopped,
  never idle, waiting for the next run, a failed run). A topic that is too large to ever fit one run's budget (a note of
  roughly 180 KB or more at the default 60k) shows as *too-large* with its estimate instead of waiting: trim the note or
  raise the budget and the next run takes it.
- **When it runs.** Only when no session has had a turn for 15 minutes, at most once every 3 hours, and only on topics that are
  ripe: one `wrong` or `stale` report, or 3 new observations, or an observation that has waited 24 hours. A run spends at most a
  token budget; what does not fit waits, and the status says why (budget, workspace stopped, never idle). If a turn starts during
  a run, the model call in flight finishes and the rest waits. The figures are in `config.json` beside the queue (under the
  memory store's work directory, `af-memory.work/consolidation/`); the models are in `models.json` there (default: the `small`
  role `claude:haiku`, the `large` role `claude:sonnet`, which supersede and merge escalate to).
- **Run now (manual).** If a session is busy all day the automatic run may never come. **Run now**, next to the status, starts
  a run yourself. It asks first: it calls the model, spends tokens, and shows the per-run budget. It ignores the idle check
  and the 3-hour limit, and a turn in another session does not stop it; everything else is as for an automatic run (the switch
  must be on, the token budget, validation, nothing written before you approve). By default it takes the ripe topics; tick
  *Include topics that are not due yet* to take every waiting topic (one that already has a suggestion waiting is left out).
  The request returns at once and the status shows the run's progress ("topic n of m"), then its report. Only one run goes at a
  time: a second request is refused, not queued. A manual run does not use up or reset the automatic 3-hour window. Only you
  can start it from the Console; a session cannot.
- **What a proposal can be.** A *patch* of one memory (exact-text edits, checked to apply to the current revision); a *create*,
  either one that an earlier 0117 proposal carried or a new memory the larger model wrote for an *add* with no candidate; or a
  *replacement* for a *supersede* or *merge*: the larger model writes one new memory, and the memories it replaces are marked
  superseded (hidden from agents, kept in history). A create or replacement over a name that was forgotten, or that ever
  existed (a superseded memory still has its name), is refused. When the larger model's text fails a check, the proposal falls
  back to *advice*, which you apply by hand.
- **Retiring an obsolete memory.** A fourth kind, a *retire*, says that a whole memory is obsolete: the thing it is about is
  finished (the issue it tells agents to follow is closed, the workaround is no longer needed). Approving it marks the memory
  *archived* by `consolidator` with the one-line reason; agents no longer see it in the index or in search, and `memory_read`
  answers a stub that carries the reason instead of the text. **Nothing is deleted**: the file stays, it is one change in
  **Changes**, and reverting it brings the memory back as it was (while it has not changed since). A pinned memory is never
  retired by a suggestion (you get advice instead). The larger model has to confirm a retire before it is even suggested.
- **Checked facts.** Before the model decides, the Agent itself looks up whether the issues and pull requests a memory or an
  observation names (`owner/repo#12`, a github.com link, or `#12` in a project whose origin is on github.com) are closed or
  merged, using your GitHub connection, and tells the model as a fact. This is how an observation exported from the old review
  that cites an issue closed weeks ago is marked as citing finished work, so the model leans to *no change* instead of
  creating a note about it. A lookup that fails, times out or is not connected counts as *unknown*, never as closed. Lookups
  are limited per run and cached.
- **Approval.** A proposal is re-checked against the memory as it is when you approve — every memory it touches, each against
  the revision the proposal saw; if any of them moved, nothing is written — and then published the normal way, authored
  `consolidator`: it shows in **Changes** and can be reverted. A replacement is *one* change over the new memory and all the
  memories it supersedes (the row lists them), and one revert undoes all of it. It can be reverted only while none of those
  memories has changed since. A proposal left undecided for 14 days expires (its record
  stays); expiry alone does not bring the topic back — new evidence, a change to the memory, or an explicit retry does.

**Reviewing.** The Memory view's **Suggestions** tab lists the proposals; filter by *Pending*, *Decided* (approved, rejected,
refused, or partly applied) and *Expired*. A proposal is titled by what it changes — the name of the memory it creates or
patches — not by the topic it was grouped under (which can be an unrelated memory; the topic is shown in a smaller line below).
Open one to see **If you approve**, before the Approve button:

- a one-line summary: "Approving adds 2 memories; no existing memory changes", or "Approving changes 1 memory (adr-0094, rev 2 → 3)";
- one card per item. A *create* card is a *New memory*: scope, name, type, description and the body as it will read. When the
  topic memory is not the one being created it says "The topic memory … is not changed". A *patch* card shows the target's
  revision n → n+1 as **Before** and **After** side by side, the removed and added lines highlighted, and says the change is
  published as `consolidator` and can be reverted from the change list. A *replacement* card shows the new memory, the memories it
  supersedes with their revisions, and says it is published and reverted as one change. A *retire* card shows what agents stop
  seeing: the memory's line in the index, that search no longer finds it, the reason a read will answer with, and the text
  that will be hidden (as removed lines). An *advice* card says what would
  happen and that it cannot be approved;
- then the memory's evidence (the reports and lint findings it rests on) and the revisions the proposal was drafted against.

- **Approve** applies that one proposal, after a confirmation that names what will be written. Nothing is approved in bulk, by
  waiting, or from a notification: the daily notice only opens this view.
- **Reject** drops it. **Look again** (for a rejected, expired, refused or partly applied proposal) reopens its observations so a later run
  looks at the topic afresh; the new proposal is validated from scratch.
- **Refusals are explained.** If the memory changed after the proposal was drafted (a revision mismatch), or the proposal expired
  or was already decided, Approve says so instead of failing generically; a stale proposal cannot be approved and is marked *not
  applicable* — use *Look again*.
- **Advice only.** When the larger model's replacement or new memory failed a check (a name that was used before, a schema or
  secret-scan failure, a link to nothing, an invalid answer), the proposal is shown as *Advice only* and has no Approve button.
  Read it and edit the memories by hand in **Memories**.

**The daily notice.** While the switch is on and at least one proposal is pending, you get at most one notification a day,
"N memory suggestions are waiting for your review". It carries the count only (never a memory's text) and opens
**Suggestions** in the Memory view. It is on by default with the other notifications (Settings › Notifications › Other).

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
  in the background, and starts new Claude sessions (and the assistant chat's claude) with Claude Code's auto-memory switched off, so Claude
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
  Control Plane's `/mcp`, or its memory-only `/mcp/memory`. The setting is
  your own; your tenant administrator can still block it for the whole tenant. What it allows, and what is
  recorded, is in [Using Agent Fleet memory from an external agent](#using-agent-fleet-memory-from-an-external-agent).

- **The background memory review was removed.** Earlier versions could have finished sessions suggest memories
  (the settings "Have finished sessions suggest memories" and "Also suggest automatically", the "Suggest memories now"
  item in a session's **⋯** menu, and Changes › Proposed). None of that exists any more: there is no setting,
  button, notification or route for it, and the Background memory review card is gone from AI assistance. What it
  published stays in the change list and can still be undone (see [Changes](#changes)). Suggestions that were still
  waiting when you upgraded were **not** dropped: once, on the first start after the upgrade, the workspace wrote each one
  (with the memory it was about, its base revision, the text, the reason and the run) and the list of rejected
  fingerprints to `review-queue-export-v1.json` in the `af-memory.work` folder of the Claude configuration directory,
  and only then deleted the queue. They are observations, not approvals: nothing in that file is in memory. Only the
  [memory tidy-up](#consolidation-proposals-opt-in), when it is on, reads it, as material for its suggestions. A workspace that never used the review writes nothing.

## Memory snapshots

The version history of the memory an agent keeps in its own files is described in
[Restore memory from a snapshot](../user/memory/snapshots.md).

## Using Agent Fleet memory from an external agent

Using Agent Fleet memory from your own Claude Code, Claude Desktop or claude.ai, and bringing your own Claude Code's
memory in, are described in [Use memory from an external agent](../user/memory/external.md).

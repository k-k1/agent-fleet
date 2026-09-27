# 115. Every session delete goes through the trash — and deleting a worktree is not deleting its sessions

English | [日本語](115-session-delete-via-trash.ja.md)

- Status: **design proposal only (nothing implemented)**. 2026-09-24, session `sjkmbhc`, branch
  `temp/sjkmbhc` (from `origin/develop` 633bcfcf3). No ADR has been changed. Once the user has
  answered the decisions in §7, an ADR is written first and implementation follows.
- Number: 114 is taken on another branch (`114-adr-0100-impl-review.md` on `temp/sajiob7`), so this is 115.
- docs/log is normally Japanese only. This proposal comes as an en/ja pair because the user asked for it.
- 🔴 **Addendum 2026-09-24 (same day, at implementation)**: the user took the recommended option on all six
  decisions of §7; they became ADR 0101 and were implemented. **Only D1's "put the terminal history into the
  trash" was dropped.** Reading `terminal_history.go` while implementing showed that the terminal history is
  short-lived on purpose (`/tmp` by default; its retention is the tenant's setting
  `AF_TERMINAL_HISTORY_RETENTION_DAYS`, where 0 is a policy of deleting it), and a trash with no expiry would
  break that policy. The proposal looked only at "it is a shell's only content" and never checked the retention
  policy. Restoring a shell / ssm from the trash brings back its row (meta) only. Also, the managed kinds'
  ClientMessageID ledger now stays while the session is in the trash and is removed when the archive is purged
  (a session in the trash can be restored and resumed). D7 (counting the existing transcripts) is deferred:
  unless claude's sid re-mapping (`LiveSID`), forks and subagent files are counted correctly, the figure the
  decision rests on would be wrong.
- Starting point (the user's policy):
  1. **Every** session delete goes through the trash (the gz safety net).
  2. **Deleting a worktree and deleting a session are separate operations.**
- How the facts were checked: every "exists / does not", "calls / does not call" below was
  checked by grep and by reading the code at that develop commit. The figures come from counting
  this dev deployment's `~/.local/share/agent-fleet/cleanup/` on 2026-09-24. **Guesses are marked
  as guesses.** The workspace policy does not allow reading `~/.claude` or `~/.config/agent-fleet`,
  so figures that live only there (meta-less transcripts, number of shelved rows) were **not measured**.

## 115.1 Re-reading the table in the brief

The brief listed three paths that delete a session. Reading the code again, there are **seven
entry points and three functions that forget a meta**, and the first row of the table rested on a
wrong premise.

### 115.1.1 The three functions that forget a meta

| Function | Route | Meta | Transcript (claude jsonl) | Side files ※1 | Terminal history | Worktree | Trash |
|---|---|---|---|---|---|---|---|
| `HandleStopSession` (`session_handlers.go:1296`) | `POST /sessions/{name}/stop` | removed | **kept** | **kept** | removed | **may be removed by `MaybePruneWorktree` (:1348)** | no |
| `handleDeleteSession` without reclaim (`cleanup_ops.go:80`) | `DELETE /sessions/{name}` | removed | kept | removed | kept | untouched | no |
| `handleDeleteSession` with `?reclaim=1` (`cleanup_ops.go:84`) | `DELETE /sessions/{name}?reclaim=1` | removed | archived, then removed | removed | **kept** | untouched | **yes** |
| `forgetNonLiveMetasUnder` (`gitx/git.go:1681`) | `DELETE /repos/{name}?prune_sessions=1` | removed (only stopped, not archived, not locked) | kept | kept | kept | (after the worktree itself is gone) | no |

※1 Side files = handoff proposals, transcript marks and cached translations (`removeSessionSideFiles`, `cleanup_ops.go:111`).

All three end in `session.RemoveMetaAndLineage`, which also erases the lineage row (ADR 0096
decision 6). The reclaim route refuses a live session with 409 (`cleanup_ops.go:67`); `/stop` kills
tmux and then forgets.

### 115.1.2 Seven entry points

| # | Entry point | Calls | Which sessions |
|---|---|---|---|
| 1 | Session row ⋯ menu "Delete" (`SessionMenu.tsx:351` → `useSessionActions.tsx:82`) | `/stop` | **shell / ssm only**. AI session rows have no "Delete"; they show "Archive" (`caps.ephemeral ? Delete : Archive`) |
| 2 | Repository row "Tidy stopped" (`RepoNode.tsx:149` → `archiveStopped`, `useSessionActions.tsx:180`) | AI: `/archive`, shell/ssm: `/stop` | stopped, unlocked |
| 3 | "Other sessions" orphan tidy (`OtherSessionsSection.tsx:54` → `clearOrphans`, `:132`) | same | unlocked |
| 4 | Delete the working copy (right-click, `DeleteCopyModal.tsx:116`) | AI: `/archive`, shell/ssm: `/stop`, then `DELETE /repos/{name}` (**no `prune_sessions`**) | stopped (live too with "stop first") |
| 5 | Image studio "Switch agents" (`imagegen/attach.ts:45`) | `/stop` (fire-and-forget) | **an AI session** (the one the studio was bound to) |
| 6 | Cleanup modal (`CleanupModal.tsx:60-72`) | ① `archive_session` / `delete_session` (`?reclaim=1`), ② `delete_worktree` (`?prune_sessions=1`); the shelf and ①'s shell/ssm use `?reclaim=1` | per candidate |
| 7 | MCP (operator) `delete_session` (`mcp_stdio.go:3219`, `?reclaim=1`) and `delete_worktree` (`:3201`, `?prune_sessions=1`) | same | same |

Deleting from the archive list (the shelf, `ArchivedModal.tsx:146,172`) is the same `?reclaim=1` as 6.
`DELETE /sessions/{name}` without reclaim has no caller in the Console or the MCP server (grep).

### 115.1.3 Where the brief's premise differs

1. **The row "Delete" does not appear on AI sessions.** It appears for shell / ssm only; AI rows
   get "Archive". The UI has been like this since the Console rebuild of 2026-07-07 (489965120).
   So **the row delete does not currently "forget the meta and leave the jsonl"** — shell / ssm have
   no jsonl. What a row delete loses is the meta and the **terminal history** (`removeTerminalHistory`,
   up to 4 MiB per session, `terminal_history.go:19`), and none of it goes to the trash.
2. **The entry point that removes an AI session through `/stop` is the studio switch (5).** The meta goes,
   the transcript stays, nothing goes to the trash. On top of that, `MaybePruneWorktree` may remove the
   previous session's worktree (when its meta was the last one referring to it, and it is clean and not
   ahead) — which a studio launched in a worktree would satisfy. **Not verified on a live system.**
3. **The entry points where deleting a worktree forgets AI sessions are cleanup ② and the MCP (6, 7).**
   "Delete the working copy" in the left pane (4) moves the AI sessions to the shelf first and deletes
   without `prune_sessions`. **Shelving the sessions when a worktree is deleted is already implemented,
   in that one entry point.**
4. **A worktree delete without `prune_sessions` keeps the metas.** A meta whose Dir is gone stays in
   the list and the row reads "Folder missing (can't resume)". Entry 4 avoids this by shelving first,
   but a direct call to the Agent's `DELETE /repos` does not.
5. **The reclaim route does not do everything `/stop` cleans up.** It keeps the terminal history and
   the managed kinds' ClientMessageID ledger (`removeManagedLedger`); conversely `/stop` keeps the side
   files. What is left behind depends on which route did the deleting.
6. **ADR 0097's background was already inaccurate when written.** It says "the auto prune was the only
   delete that skipped the trash", but `/stop` and `prune_sessions` skipped it at the same time. Decision
   2's "'every delete goes through gz' is now true without exception" does not hold either.
7. **The CP audit log records `/stop` as `session.stop`** (`control-plane/proxy.go:85`), but does not
   record `DELETE /api/sessions/{name}` (the DELETE branch audits `/api/repos/{name}` and others).
   Switching the row delete to DELETE would **lose the audit entry**.

### 115.1.4 Other facts checked

- **`/stop` is the only caller of `MaybePruneWorktree`** (`session_handlers.go:1348`). ADR 0097 decision 5
  took it out of the TTL sweep (see the comment at `:222`), but it is still attached to a person's delete.
  Archiving (`HandleArchiveSession`) does not call it.
- **Restoring from the trash writes the meta back exactly as it was at delete time**
  (`restoreCleanupArchive`, `CreateMetaIfAbsent`) — `Archived` included, so something deleted from the
  shelf returns to the shelf and something deleted from the list returns to the list. If Dir is gone the
  row reads "Folder missing" and cannot resume (`agents.DirGoneErr`, which the drivers of seven kinds
  return). The conversation stays readable (`DirGoneNotice`, `ComposerNotices.tsx:9`). claude
  transcripts are found by globbing on the sid (`TranscriptRead` → `jsonlByMtime`), so a missing Dir
  does not stop them being found.
- **For kinds other than claude, only the meta goes into the trash** (`archiveSessionForDelete`). The codex
  rollout and the opencode sqlite stay in each CLI's store; their space is not reclaimed.
- **The cache orphan scan** (ADR 0097 addendum, `cache_orphans.go`) treats a UUID as an orphan when no meta,
  no trash archive and no fork lineage refers to it. After today's `/stop` and `prune_sessions`, **the
  session's pasted images become cleanup candidates at once**. Through the trash, they stay protected
  until the archive is purged.
- **The comment on side files (`cleanup_ops.go:103`) assumes "session names are reused".** That is wrong
  (names are random slugs that are never reused), and ADR 0097's list of stale comments misses it.

### 115.1.5 What is in the trash (2026-09-24, this dev deployment)

| | Count | Size (tar.gz) |
|---|---|---|
| Total | 1,082 | 486 MB (`du`) |
| Sessions (`delete_session`) | 1,046 | about 502 MB (sum of file sizes) |
| of which claude with a transcript | 898 | — |
| of which claude without a transcript | 12 | — |
| of which codex / opencode / copilot / cursor (meta only) | 108 / 21 / 3 / 2 | — |
| **of which shell** | **2** | — |
| Branches (`delete_branch`) | 36 | about 0 |

By month: 2026-07 5 items 1.1 MB, 2026-08 659 items 277 MB, 2026-09 (to the 24th) 418 items 224 MB.
Per item: median 302 KB, p90 1.0 MB, max 6.0 MB.

**Only 2 shell archives** means shell / ssm deletes almost all go through `/stop` (entry points 1–4)
and never reach the trash.

## 115.2 The problem

1. **Whether a delete goes through the trash depends on the entry point.** The user sees the same
   "delete", but the row delete (shell/ssm), the studio switch, and the cleanup ② / MCP worktree delete
   cannot be undone, while the shelf's and cleanup ①'s deletes can. The guide says "Deleting too much is
   recoverable" (`02-sessions.md:301` / `.ja.md:281`) and the row's confirmation says "This can't be
   undone". Each is only partly right.
2. **The deletes that cannot be undone do not reclaim space either.** `/stop` and `prune_sessions` forget
   the meta and leave the transcript and side files. With no row, the user can neither see nor remove them.
3. **Stopping, deleting and tidying worktrees are tangled together.**
   - Deleting a session can remove a worktree (`/stop` → `MaybePruneWorktree`).
   - Deleting a worktree can remove sessions (`prune_sessions=1`).
   - The endpoint called `/stop` actually deletes. Stopping is `/halt`.
4. **The clean-up done depends on the route** (115.1.3 item 5).

## 115.3 Proposal

### D1 — One operation removes a session: "move to trash"

The Agent gets one `trashSession(m)`, and **every route that forgets a meta goes through it, without
exception**. It is today's reclaim route plus the clean-up that only `/stop` did:

1. If it is live, stop it (in halt's order: promote the carry-over → disconnect → kill or DropHandle).
   **The caller decides whether stopping is allowed**: the row delete shows a confirmation, so it may.
   Cleanup ① and `delete_session` keep refusing a live session with 409, as now.
2. `finalizeSessionUsage`.
3. `archiveSessionForDelete` puts the meta, the transcript (claude) and **the terminal history (new)**
   into one gz.
4. Only after that, remove the transcript, meta, lineage, side files, terminal history, managed ledger
   and status records.
5. **Leave the worktree alone** (D3).

`/stop` and `DELETE /sessions/{name}` (with or without reclaim) both call `trashSession`. **An older
Console that still calls `/stop` also ends up in the trash.** The Console bundle is served by the CP and
can be out of step with the Agent; this way the skew fails safe. The name `/stop` stays only for
compatibility; a new Console uses `DELETE /sessions/{name}?stop=1` (stop first if live). The CP audit
gains `DELETE /api/sessions/{name}` as `session.delete` (115.1.3 item 7).

**Why terminal history goes into the trash.** For shell / ssm the terminal history is the only content,
the equivalent of a transcript. It is capped at 4 MiB and is text, so gz should compress it well
(**guess** — not measured). Without it, restoring a shell would only bring back a meta, which is hardly
worth restoring.

### D2 — Console row menu: same items, but "Delete" now means "to the trash" (question 1)

| Row | Now | Proposed |
|---|---|---|
| AI session | Stop / Archive / Recreate (no Delete) | **unchanged** |
| shell / ssm | Stop / Delete (`/stop`, irreversible) | Stop / **Delete (to trash)** |

- Confirmation (`sess.delete_body`): "Move “{name}” to the trash. You can restore it from the cleanup trash."
- Entry points 2–4 (tidy stopped, orphan tidy, delete working copy) also stop calling `/stop` for
  shell/ssm and call the same delete. Their wording ("discard", "can't be undone":
  `rp.del.summary_forget`, `sess.tidy_orphans_irreversible` and others) changes too.
- Entry point 5 (studio switch) **shelves** the previous session (`/archive`). The studio is already
  unbound from it, so its conversation can still be read from the shelf. Whether to delete it is up to a
  person on the shelf, as for any other session.

**Why not add "Delete" to AI session rows.** A one-step delete from the row breaks ADR 0097's two
steps: "to keep it, shelve it; to be rid of it, delete it from the shelf". Users are not hurting for a
row delete today (deleting from the shelf exists). Through the trash it would be recoverable, so it
would not be dangerous. But it is not needed for this change's goal (every delete goes through the trash),
so it is split out as decision 2 (§7).

### D3 — Deleting a session never deletes a worktree

Remove `MaybePruneWorktree` from `/stop` (it then has no callers, so the function goes).
Worktrees are removed only by cleanup ②, delete-the-working-copy and MCP `delete_worktree`.

- **Cost**: a worktree whose last shell session was deleted is no longer removed on its own. This is the
  cost ADR 0097 decision 5 already accepted for the TTL sweep; cleanup ② picks them up. AI session rows
  have no Delete, so today this automatic tidy fires only on shell/ssm deletes and the studio switch
  (115.1.4) — the effect is small.
- **Benefit**: a session restored from the trash still has its working copy. Today a restored session can
  find its worktree gone.

### D4 — Deleting a worktree shelves the sessions in it; it does not trash them (question 2)

Replace `forgetNonLiveMetasUnder` with `shelveSessionsUnder`. It does on the Agent side, for every entry
point, what entry point 4 (delete the working copy) already does in the Console.

| Session in the worktree | Now (`prune_sessions=1`) | Proposed |
|---|---|---|
| live | the delete is refused with 409 | unchanged |
| locked | the delete is refused with 403 | unchanged |
| on the shelf | untouched | unchanged |
| stopped AI session | meta forgotten (no trash) | **moved to the shelf** (`Archived = true`) |
| stopped shell / ssm | meta forgotten (no trash) | **to the trash** (D1 — resuming without a working directory is pointless) |

- **No difference between with and without `prune_sessions`.** The Agent always does this. Calls without
  it (entry point 4) then no longer leave metas with a missing Dir in the list (115.1.3 item 4). The
  Console-side archive in entry point 4 becomes redundant but harmless and can stay (it drives the per-row
  progress).
- **Why the shelf rather than the trash**: the user's policy 2 (worktree and session are separate
  operations). A person deleting a worktree decided about the working copy, not about throwing the
  conversation away. Throwing it away is a separate operation: deleting it from the shelf. This matches ADR
  0097 decision 1's idea: automatic and collateral actions only move things, and a person deletes a thing
  by acting on that thing.
- **Why not let the user choose**: a choice adds to the confirmation dialog and to the tool's arguments.
  Shelf to trash is one press, so a choice would save exactly one press. Decision 3 in §7 asks.

**How the sessions look once the worktree is gone** (unchanged by this proposal):

- On the shelf: the conversation is readable. It reads "Folder missing (can't resume)" and no resume
  button is shown.
- Restored from the shelf to the list: struck through, "Folder missing", not clickable.
- Restored from the trash: returns with the `Archived` it had at delete time and looks like one of the two
  above.
- Resume: not possible (`DirGoneErr`). **The branch is still there (until `delete_branch`), so recreating
  the worktree at the same path should make it resumable** — a **guess**. The sid is `UUID(dir, name)`,
  so claude finds the same conversation only at the same path. A "recreate the worktree and resume" flow
  is out of scope (§8).

### D5 — MCP and the operator (question 3)

| Tool | Now | Proposed |
|---|---|---|
| `delete_session` | `?reclaim=1` (trash) | unchanged (its body becomes D1's `trashSession`) |
| `delete_worktree` | `?prune_sessions=1` (forgets stopped sessions) | shelves the sessions in it (D4). **Fix the description**: "stopped sessions tied to the worktree are also cleared from the list" → "stopped AI sessions move to the shelf and shell/ssm go to the trash; both can be restored" |
| `stop_session` | `/halt` (resumable) | unchanged |
| `archive_session`, `restore_cleanup_archive`, `purge_cleanup_archive` | — | unchanged |

- **Permissions do not change.** All of them are visible only to an operator with `writeEnabled()`, and
  never to sessions (children included, ADR 0073).
- **Approval does not change.** `bridgeApprovalGate` posts an approve button only in operator turns driven
  from Discord and is a no-op in Console chat (`bridge_approval.go:132`). `delete_worktree` loses one
  irreversible side effect, but the working copy itself still cannot be restored, so the gate stays.
- No new tools.

### D6 — The trash gets bigger (question 4)

**This proposal adds little (guess).**

- D4 only shelves, so it adds nothing to the trash.
- What is added is shell/ssm deletes (meta plus ≤4 MiB of terminal history). How many a year is unknown;
  counting `session.stop` in the CP audit log would tell (**not measured**).
- AI session deletes already go through the shelf and make up most of the 1,046 items / ~500 MB. This
  proposal neither adds nor removes any of them.

**No automatic expiry** (ADR 0097 decisions 2 and 3 stand). Instead, add two things:

1. **Settings → Machine** already shows the trash size (PR #919); add the count and the oldest date.
2. The cleanup modal's "Trash (restore)" tab gets a **person-pressed "Delete permanently: older than N
   days"**, shaped like the archive list's "Delete old ones (30 days)", default 30 days, with count and size
   in the confirmation.

Item 2 stays inside decision 2's "only a person deletes". It removes the chore of picking items one by
one as the trash grows.

### D7 — Migrating existing data (question 5)

**No migration. Measure first.**

- **Sessions forgotten through `/stop` or `prune_sessions` cannot be put back into the trash.** No meta is
  left, so the name is unknown; the sid is `UUID(dir, name)` and the name cannot be derived from it; and
  the trash restore skips a session without `meta.Name` (`restoreCleanupArchive`).
- Some things cannot be told apart. `~/.claude/projects` also holds transcripts that were never fleet
  sessions (for example, claude run by hand inside a shell session). Calling every sid unreachable from a
  meta a "deleted session" would sweep those up.
- **The TTL prune before ADR 0097 (until 2026-09-20) also forgot metas and kept transcripts.** Most
  meta-less transcripts probably come from there — a **guess**.

So, in this order:

1. **P3 counts (read only).** Using the same rule as the cache orphan scan (reachable from a meta, the
   trash or fork lineage), report the count and size of unreachable claude transcripts, split into those
   provably fleet sessions (the sid-to-name mapping survives somewhere, e.g. the usage ledger — **not
   checked**) and those that are not.
2. With the numbers in hand, the user picks one (§7 decision 5):
   - (a) leave them (as now: invisible, space not reclaimed);
   - (b) add a cleanup row "Transcripts of deleted sessions" that a person presses to **delete them
     permanently** — an exception to the trash, justified the same way as the cache in the ADR 0097
     addendum (only things with no path back to restore);
   - (c) for those whose name is known, rebuild the meta and put them into the trash (restorable).

Metas with a missing Dir left by worktree deletes without `prune_sessions` are already visible rows that can
be shelved, so they need no migration.

### D8 — Documents and on-screen wording (question 6)

| Where | Change |
|---|---|
| `guide/member/02-sessions.md` / `.ja.md`, the stop/tidy table | The "Delete (shell/SSM)" row becomes "goes to the trash; restorable from the cleanup trash". Drop "Log files may remain after deletion, but the session cannot be brought back to the list" |
| same, "Deleting too much is recoverable" (`:301` / `:281`) | Becomes true (text mostly unchanged). To "Only deleting a worktree cannot be undone" add "stopped sessions in it move to the archive (shell/ssm to the trash)" |
| same, "Delete the working copy" section | Say shell sessions go "to the trash" rather than "discarded" |
| same, "When you can — and can't — resume" | One sentence on how shelved rows look after their worktree is deleted |
| i18n: `sess.delete_body`, `sess.tidy_orphans_irreversible`, `sess.cleanup_delete_n`, `clean.stage1_confirm_body`, `rp.del.summary_forget` and others (ja/en) | "can't be undone" / "discard" → "to the trash (restorable)" |
| MCP `delete_worktree` description | D5 |
| `guide/member/08-organising.md` / `.ja.md` (operator) | Unchanged. Its tidy-up example (`:121`) only mentions stopping and does not mention `delete_worktree` |
| ADRs | A new ADR (number taken when filed). ADR 0097 gets an addendum: "the background's 'only' was wrong", "decision 2 becomes true with the new ADR", "decision 5 extends to a person's delete". ADR 0028 gets an addendum that "worktree auto prune" is gone entirely. ADR 0096 decision 6 (a person's delete also erases lineage) is unchanged |
| Code comments | `HandleStopSession`, `handleDeleteSession`, `RemoveMetaAndLineage`, `pruneSessions`, `forgetNonLiveMetasUnder`, the head of `cleanup_archive.go` ("only the sessions/branch tied to it are [archived]" is still false), `removeSessionSideFiles` (name reuse) |
| Release notes | Behaviour change: shell/ssm deletes become restorable / deleting a session no longer removes its worktree / deleting a worktree shelves its stopped sessions |

## 115.4 Rejected

- **Switch the Console to `DELETE ?reclaim=1` and leave `/stop` as it is.** Older Consoles and other callers
  (the studio) keep deleting irreversibly. Without one path in the Agent, the next new entry point will
  bypass the trash again.
- **Deleting a worktree trashes the sessions in it.** Against the user's policy 2: the person deleting a
  worktree did not say to throw the conversation away. Cleanup ②'s "safe" (merged, clean) is a verdict on
  the working copy, not on the conversation.
- **An expiry on the trash.** Collides with ADR 0097 decisions 2 and 3. Volume is handled by D6's
  person-pressed bulk delete.
- **Keep `MaybePruneWorktree` and skip it only when the session goes to the trash.** More conditions, and
  both directions of the tangle remain.

## 115.5 Effects

- shell/ssm deletes become restorable. The trash grows a little (D6).
- Deleting a session leaves its worktree. Cleanup ② gets a few more candidates.
- Deleting a worktree keeps the conversations on the shelf. The shelf grows a little (it has no expiry either).
- The audit action changes from `session.stop` to `session.delete` (CP).
- The cache orphan scan needs no change: deleted sessions sit in the trash, so their pasted images are
  protected until the archive is purged.

## 115.6 Implementation steps (after agreement)

- **P0 (Agent)**: add `trashSession` and route `/stop`, `DELETE /sessions/{name}` and the replacement of
  `forgetNonLiveMetasUnder` through it. Remove `MaybePruneWorktree`. Put terminal history into the archive
  and bring it back on restore. Test: count the call sites, in the style of `meta_remove_sites_test.go`, so
  every route that forgets a meta is shown to go through the trash.
- **P1 (Console, CP)**: rewrite entry points 1–5, the wording, and the audit name.
- **P2 (docs)**: guide, ADRs, MCP descriptions.
- **P3 (measure)**: D7's counts. §7 decision 5 waits for them.
- **P4 (optional)**: D6's bulk permanent delete and the Machine tab figures.

## 115.7 Decisions for the user

1. **D1's policy** (every route that forgets a meta goes through `trashSession`, `/stop` included).
   Recommended: yes.
2. **Add "Delete (to trash)" to AI session rows?** Recommended: no (they can be deleted via the shelf
   today; keeps the two-step meaning).
3. **What happens to stopped AI sessions when their worktree is deleted?** Recommended: shelve them, no
   choice. Alternative: let the confirmation choose "shelf / trash".
4. **Stop removing a worktree automatically when a session is deleted** (D3)? Recommended: yes.
5. **Existing meta-less transcripts** (D7)? Recommended: count them in P3, then decide.
6. **Add a bulk permanent delete for the trash (older than N days)** (D6)? Recommended: yes (default 30
   days, pressed by a person).

## 115.8 Not yet verified

- Whether the studio switch (entry point 5) actually removes the previous session's worktree (not tried on
  a live system).
- The size of terminal history after gz.
- How often shell/ssm are deleted (the CP audit log should tell).
- Whether a sid-to-name mapping survives anywhere, such as the usage ledger (decides whether D7 (c) is
  possible).
- Whether a managed-kind session restored from the trash can resume after its ClientMessageID ledger was
  removed — i.e. whether the ledger should also go into the archive. To check in P0.
- Whether claude can resume a conversation after the worktree is recreated at the same path (D4).

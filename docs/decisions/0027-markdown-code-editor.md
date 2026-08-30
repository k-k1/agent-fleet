# 0027. Add a CodeMirror 6 edit mode to the File pane, and limit saving to an explicit action

English | [日本語](0027-markdown-code-editor.ja.md)

- Status: **adopted; implemented through Phase 4** (2026-07-28)
- The detailed contract: [docs/44-markdown-code-editor.md](../log/44-markdown-code-editor.md)
- See also: [docs/build/02-console.md](../build/02-console.md) (the Console's pane structure) /
  [docs/build/04-agent.md](../build/04-agent.md) (the fs boundary and the denylist) /
  [docs/build/05-api.md](../build/05-api.md) (the map of API relaying) /
  [decisions/0011](0011-console-rebuild.md) (the Console rebuild)

## Context

The Console's File pane currently displays workspace files read-only. Against the demand for
lightly editing code and Markdown, adding a dedicated editor pane would duplicate the existing pane
restoration, layout and Markdown/Marp/Mermaid rendering assets. And a design in which the AI writes
its suggestions straight to disk makes unintended changes and recovery from conflicts hard.

## Decision

1. **Adopt CodeMirror 6 as the editor.** It integrates easily with the current React + Vite +
   TypeScript, and the extensions needed for editing Markdown and code can be added in stages.
   Monaco will be re-evaluated in future only if LSP becomes a requirement.
2. **Extend the existing `file` pane.** No new pane kind: the file pane gains
   `mode: "view" | "edit"`. `view` is read-only as before; `edit` shows an editing buffer inside the
   tab.
3. **The save API is `PUT /fs/file`.** The Console's public entrance is `PUT /api/fs/file` and the
   actual endpoint inside the Workspace Agent is `PUT /fs/file`, with the CP forwarding under the
   existing fs relay convention.
4. **Saving is compare-time CAS plus serialisation within the same API.** The revision is the
   SHA-256 of the file's raw bytes, and a save proceeds only if the save API's `baseDiskRevision`
   matches the revision at the time of comparison. If it does not, the result is
   `409 revision_conflict`. PUTs on the same API take a mutex keyed by the canonical relative path
   fixed by lexical validation, acquired **before** the target's fd-safe validation, open, read and
   hash, and held until the parent directory's fsync result is settled. External writers — the
   shell, Claude/Codex, `git checkout` and so on — do not participate in this mutex, so it offers no
   guarantee of preventing or detecting an external change between the comparison and the rename. A
   cooperative lock across all writers is not adopted.
5. **Saving is an atomic write, and a failure after the rename is a distinct state.** Write a
   temporary file in the same directory as the target, fsync it, rename, and fsync the parent
   directory too. A failure before the rename keeps the old content and is `write_failed`; a failure
   after the rename (in the directory fsync and so on) means the new content is in the live
   namespace but its durability is unknown, and is `write_state_unknown`. A GET comparison alone
   cannot settle durability, so the client transitions to `SaveStateUnknown`, holding its dirty
   "mine", and requires either an explicit re-save or an acknowledgement of the risk. A normal save
   is clean only on a 200 response; the sole exception is an explicit acceptance of the durability
   risk after confirming that the sent content is live. On accepting the risk, the revision of the
   sent content as confirmed by the recovery GET is set as `baseDiskRevision` before branching on
   clean/dirty. The only attribute preserved is `mode.Perm()`; owner, ACLs, xattrs and special bits
   are not guaranteed in v1.
6. **Separate an fd-relative boundary per operation surface.** The v1 Linux Agent fixes the
   root/parent directory fds and uses `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)`,
   `fstatat(AT_SYMLINK_NOFOLLOW)` and the `renameat` equivalent for GET/file, download and PUT. PUT
   accepts only a browse-root-relative canonical path; GET and download additionally accept a
   canonical absolute path under the existing `allowedReadRoots` (browse/scratch/docs), choosing the
   permitted root and taking the path relative to that root fd. The existing lexical check plus
   Lstat helper is not reused as TOCTOU protection for a request-controlled path. Path detours via
   symlinks are prevented, but a namespace mutator running as the same uid, and inode aliasing via
   hardlinks, are outside v1's non-cooperative-writer threat model; the denylist does not guarantee
   anything about an inode's provenance. GET verifies size/content-length consistency with at most
   two rounds of `fstat-before → a bounded 2 MiB+1 read → fstat-after`, but it does not guarantee an
   instantaneous snapshot against an external in-place write of the same size.
7. **The material for editing is LF-only UTF-8 text.** CRLF, lone CR and mixed line endings are
   read-only, so that the raw byte revision and CodeMirror 6's document offsets match with no
   conversion. The CR / NUL / unpaired-surrogate / 2 MiB validators are applied to every
   transaction: typing, IME, paste, undo, redo and AI replacement. If PUT's wire body is invalid
   UTF-8 or invalid JSON, it is 400 `bad_request`; invalid UTF-8 or a NUL in the current file, and a
   NUL in the decoded content, are 415 `binary_not_supported`; CR is 415 `unsupported_newline`. A
   lone high/low surrogate escape in a JSON string is also 400 `bad_request`, while correct
   surrogate pairs and an actual U+FFFD are allowed. Future line-ending mapping or CRLF support will
   be added only after being fixed in a separate design.
8. **The AI changes nothing beyond the editing buffer, through a change-proposal channel.** A
   general Claude/Codex may well have Write/Edit/Bash capabilities, so we do not describe the AI as
   a whole as having "no write tools". Only Phase 4's proposal generation is restricted to a
   read-only allowlisted path, and an `EditSuggestion` is validated together with
   paneId/filePath/requestId/sourceRevision. Accepting does not call PUT; saving is limited to the
   user's Ctrl+S / Cmd+S or the Save button.
9. **Markdown reuses the existing assets.** Three modes — edit, preview and a side-by-side split —
   are offered inside the File pane, reusing the lazy loading of MarkdownView, MarpView and Mermaid.
   The current source becomes edit, while preview and slides become a renderer choice on the preview
   side, so Marp's ordinary preview is not lost.
10. **Unsaved content is memory-only, and every navigation is guarded.** Dirty content, undo, the
    suggestion and the generation are kept out of PaneContent/layout, so the content is not saved
    even though the layout is persisted to storage. Not just closing, but replacing the active pane,
    pane reuse, history, tenant/reset, the reader, popout and beforeunload are protected by a dirty
    registry. Reload, logout, a version update and workspace recreate/clean-home/stop are covered
    too, and the terminal's version-update exception is not carried over to the editor. A dirty
    popout is refused or explicitly confirmed in v1.
11. **A 409 conflict is resolved by a state machine holding a separate remote snapshot.** The dirty
    "mine" is not overwritten; Phase 2 offers taking remote, discarding mine, a manual merge with
    remote as the base, and cancel. If the GET after a 409 finds the target gone, hits a safety
    boundary error or finds it uneditable, the state is `ConflictRemoteUnavailable`, holding mine and
    offering re-fetch, copy and an explicit close. There is no path that updates only the remote
    revision and force-overwrites with mine.
12. **Markdown keeps three top-level modes, and the edit surface subsumes what the source surface
    did.** For Markdown, edit/preview/split are the only top-level operations, and the pane's `mode`
    is derived from them. There is no read-only `source` mode for editable Markdown. Instead, the
    select→send and line-quote jump that CodeView had are implemented on CodeMirror's edit surface.
    The line number and quoted string are taken from CodeMirror's document rather than the DOM
    (virtualisation means an off-screen selection is not in the DOM). Markdown that cannot be edited
    falls back to the old preview/source/slides.
13. **Following external changes is an advisory revision probe; it does not replace CAS.** Changes
    made by writers other than the Console (agents, the shell, git) are detected by polling
    `GET /fs/file?meta=1`, which does not return content. It is a query flag rather than a new
    route, so no CP route has to be added. When dirty, it only notifies, and **the probe does not
    move `phase`**. `Conflict` is created only by a 409 response and by the user explicitly running
    "review the difference" and the content GET succeeding (docs/44 §7.3), preserving the invariant
    that a probe never creates a Conflict by itself. When clean it may follow automatically, but the
    old content is not left in the undo history. Only `editable:true` files that have a `revision`
    are followed.

## Scope and phase boundaries

- Phase 0 (this ADR and [docs/44](../log/44-markdown-code-editor.md)) fixes the design, the API,
  revisions/conflicts, the proposal format and the input constraints.
- Phase 1 covers the save API foundation and its Go unit tests: the Agent/CP routes, relaying,
  auditing (including `write_state_unknown`), the strict decoder, fd-relative operations, the
  GET/download race, symlink/CAS/failure injection, and the current-file and path bounds.
- Phase 2 implements CodeMirror 6, view/edit in the File pane, single-flight saving,
  snapshot/generation, `SaveStateUnknown`, the state machine for a 409 conflict and for an
  unreachable remote, all the buffer validators, the dirty registry and navigation guard,
  beforeunload, ARIA and the Save button.
- Phase 3 implements edit/preview/split for Markdown/Marp, switching between the ordinary preview
  and the slide renderer, select→send and the line jump on the edit surface, and regression tests
  for the existing rendering assets. **Completed 2026-07-28.** It is contained in the Console alone;
  no Agent or CP changes. Eight rounds of review and 14 comments were addressed, and no item was left
  as a known limitation.
- Phase 3.5 implements the `meta=1` metadata GET, the probe in the Console, an advisory notification
  when dirty, and automatic following when clean (including read-only view panes). **Completed
  2026-07-28** (docs/44 §6 Phase 3.5). On the Agent side it only removes `content` from the
  response, sharing the decision, exclusion and error contracts with the ordinary GET; on the Console
  side it follows by rebuilding EditorState without leaving undo history, restoring the cursor and
  scroll position by line number.
- Phase 4 implements the read-only proposal generation channel, structured proposals with an
  identity, the diff review and accept/reject. **Completed 2026-07-28** (docs/44 §6 Phase 4). The UX
  is "a selected range plus an instruction" (the range is fixed by the user's selection; the LLM is
  not asked to compute offsets, and no selection means the whole file); the transport is a
  synchronous `POST /fs/suggest-edit` (the envelope does not go on the wire — the Console composes it
  and passes it through §4.2's validation); generation reuses the same `oneShotHeadless` as title and
  reply suggestions, and the one one-shot that was not read-only, opencode, was closed off by adding
  an edit/bash deny policy via `OPENCODE_CONFIG`. Staleness is not stored but derived from
  `baseRevision !== bufferRevision`, and applying is a single range transaction in CodeMirror
  (undoable, and passing the shared validator filter).
- Phase 5 — multiple candidates, accepting a single hunk, session integration, completion and CRLF
  support — begins after a separate design.

## Options rejected

- **Adopting Monaco first**: without LSP, the dependency and bundle are large and the benefit
  against integrating with the existing lightweight viewers is insufficient. Re-evaluate when LSP
  becomes a requirement.
- **A new pane kind dedicated to editing**: it multiplies the points of layout, URL/history, pane
  restoration and reuse of the existing viewers, and it loses the natural view/edit transition of
  the File pane.
- **The AI writing files directly**: the boundaries of explicit user approval, conflict review and
  auditing become vague. The AI is limited to generating a proposal and applying it to the buffer.
- **Saving drafts to localStorage**: it leaves residue on a shared machine or when switching
  accounts, and unintentionally persists sensitive source code.
- **Overwriting without a revision**: it silently erases changes made by another tab, an external
  agent or a git operation.
- **Putting every writer under a cooperative lock**: unifying every write path including the shell,
  the agents and git would greatly expand v1's change footprint and privilege boundary. The limits of
  compare-time CAS are stated in the contract and the tests instead.
- **Keeping a read-only source mode for editable Markdown**: there would be four top-level modes
  (effectively five for Marp), which breaks keyboard cycling and the phone-width layout. Worse, two
  nearly identical plain-text surfaces would sit side by side with the send pill, search and
  editability differing between them. Moving the features onto the edit surface removes the need for
  the duplication.
- **Detecting external changes with the Agent's fsnotify plus a push stream**: it needs a watch
  registry, new stream wiring through the CP, and investigation of inotify behaviour and watch limits
  in a container FS — well beyond v1's footprint. If the probe proves insufficient, that becomes a
  separate design.
- **Polling a full GET to detect external changes**: it would repeatedly transfer up to 2 MiB per
  open pane, contradicting the policy of reducing Console↔CP traffic.

## Results and the constraints accepted

- Users can edit, review and explicitly save while the existing File pane and rendering assets stay
  as they are.
- On a conflict it errs on the safe side and stops the save. Automatic merging and force
  overwriting are left to a future, separate design.
- v1 is limited to UTF-8 text files with an edited body of 2 MiB or less. Binary, non-UTF-8, large
  files, anything under the denylist and files reached through a symlink cannot be edited. GET and
  download allow absolute paths under the scratch/docs allowed read roots, read-only.
- A dirty buffer exists only in the tab's memory, so there is no restoration after a browser
  restart.
- External-change detection is polling, not real time. It is late by however long the tab is in the
  background plus the probe interval. Files with `editable:false` have no `revision` and are not
  followed. Detection is only an early warning; the correctness of a save is still carried by
  compare-time CAS.
- A namespace mutator running as the same uid, and inode aliasing via hardlinks, are outside the
  non-cooperative-writer threat model; the fd boundary is described as protection for
  request-controlled paths and symlink resolution.
- **A known constraint**: after a client aborts a PUT on a timeout, the recovery GET is closed
  against races within the Agent process by sharing the path mutex and checking the context
  immediately after acquiring it. But the PUT and the recovery GET are separate CP→Agent HTTP
  requests, and the CP guarantees nothing about the order in which cancellation propagates, so an
  extremely narrow window remains: "the recovery GET reads the old base before the cancel reaches a
  PUT that had stopped before acquiring the mutex, and the PUT then resumes, passes its check and
  renames" (docs/44 §3.2). Hitting it requires goroutine scheduling and network timing to coincide;
  the probability is extremely low and v1 accepts it. Closing it would require tracking in-flight
  PUTs per path on the CP side, or having the GET wait on a save operation ID — future work.

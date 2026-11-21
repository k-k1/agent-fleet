# 0141. Agent memory as an activity-bar view, a memory as a file-like pane

English | [日本語](0141-memory-activity-view.ja.md)

- Status: **accepted** (2026-11-20). Layout and edit form chosen with the owner. All eight decisions built: phase 1 (#4567) and phase 2 (#4568). The dirty guards for a memory's draft (close, reload, logout) are #4569.
- Tracking: #4567 (view and proposals), #4568 (memory pane). Follow-ups: #4569.
- Related: [0140](0140-console-activity-bar.md) (the activity bar this adds a view to) /
  [0108](0108-af-owned-agent-memory.md) (the AF-owned store) /
  [0113](0113-member-origin-mark.md) (member writes go through the signed relay) /
  [0130](0130-cultivated-memory.md) decisions 6 and 7 (the consolidator's proposals)

## Context

The Memory pane (#3720) puts three tabs in one pane: the memory master-detail, the change list and
the consolidator's proposals. Each tab draws its own list beside its own detail, so at pane size
the list eats the width the body needs, and a member who wants a memory open next to a session has
to keep the whole viewer open. ADR 0140 decision 2 already says where a list belongs: "A new list
gets a view."

Memories are not workspace files. The store lives under the Agent's config directory, outside every
browse root, and a member write has to go through `member-save`: revision check, the secret scan,
the size limits, the member-author record and the Control Plane's signature (0113). A file editor
that wrote the Markdown file directly would skip all of that.

## Decision

1. **Memory is a view of the activity bar** (`RAIL_VIEWS` gains `memory`, icon `archive`, after the
   memo queue). The view holds, top to bottom:
   - the filter: search (fields at once, bodies after a pause through `member-search`), scope,
     the All / Pinned / Forgotten chips and type;
   - **Proposals**: the consolidator's pending proposals by default (decided / expired one click
     away), with the run state and the run button in the section head; foldable;
   - **Memories**: the filtered list, grouped by scope (user, then each project);
   - (2026-11-21, #4675: the filter now sits under the Memories head and sticks there, the top head
     reads "Agent memory", types are badges and each scope group folds, per device; a group holding
     the open memory or a search hit stays open.)
   - at the foot, a link that opens the change list in the Memory pane, and `+` / overflow in the
     head (new memory, Settings > Agent memory).
2. **Its badge is the number of pending proposals, as a waiting count.** A proposal waits on the
   member's decision and arrives without the member adding it, like a session stopped on an answer
   (0140 decision 4). Memories themselves carry no badge.
3. **The memory list mounts on first show**, like Files (0140 decision 3): it reads the Agent, and
   a hidden view must not. Only the pending-proposal count is read while the view is hidden. A
   stopped workspace shows the stopped-rail section with Start.
4. **Clicking a memory opens it in a pane of its own** (new kind `memoryEntry {scope, project,
   name}`, one pane per memory, like a file). It looks and behaves like the file viewer: a head
   with name, scope, type, pin, uses, updated and author; the body as rendered Markdown; Edit
   switches to the code editor. Description and type are a form above the editor, not frontmatter
   inside it, so no malformed header can reach a save. Save goes through `member-save` and shows
   its refusals in place (revision conflict, secret findings, limits). Pin, Forget, purge history
   and history move from the master-detail into the pane head and its overflow menu. A forgotten
   memory opens read-only with Restore. `+` opens an empty pane in edit mode.
5. **Clicking a proposal opens its detail in the Memory pane**
   (`{kind:"memory", memoryView:"proposals", proposalId}`), full width, with Approve / Reject as
   today. There is still one Memory pane; a second open retargets it (`openMemoryView`).
6. **The Memory pane keeps Changes and Proposals; its Memories tab goes.** A persisted pane on
   `memoryView:"memories"` migrates to a pane that points at the view.
7. **One store for both sides.** The list, the proposals and a write version move to
   `features/memory/store.ts`, so a decision or a save in a pane refreshes the view and the reverse.
   The `reload` counters passed between components today go away.
8. **Entry points follow.** The consolidator's notice shows the view and opens the first pending
   proposal; the command palette opens the `memoryEntry` pane and reveals its row; Settings > Agent
   memory shows the view.

## Alternatives set aside

- **FileView itself on a virtual path** (`af-memory://…`), with the file API routing the path to
  `member-save`. It reuses the most code, but FileView (≈1,000 lines) is built around a working-copy
  path: git change bar, probe, external follow, suggest-edit, the send pill. Each would have to learn
  to stay off. A later step can instead give `useFileEditor` a `{load, save}` adapter, so dirty
  guards, recovery and conflict handling come along without FileView.
- **Opening the Markdown file in the store directly.** It skips the secret scan, the revision check
  and the signature (0113). Not offered.
- **Frontmatter in the editor.** The most file-like, at the price of parsing YAML on save and
  explaining its errors. The owner chose the form.
- **Keeping the master-detail in the pane and adding the view as a launcher.** It leaves the list
  in the pane's width, which is the problem.

## Consequences

- The activity bar has seven views; the phone swipe steps through one more.
- Memories open beside sessions as ordinary pane tabs and survive in the saved layout.
- `memoryMasterDetail.tsx` splits into a data hook (used by the view) and the pane's detail parts;
  its phone two-screen layout is no longer needed, since the drawer and the pane already are two
  screens.

## Notes

- 2026-11-20: `useFileEditor` takes an optional `FileEditorIO` (`load`, `save`) and defaults to the
  file API, so the first step of the adapter named under Alternatives exists. The memory pane does
  not use it yet; that is the remaining part of #4569.
- 2026-11-20: the `memoryEntry` pane of an existing memory edits through `useFileEditor` with an
  adapter (`memory/memoryEntryIO.ts`): the buffer is one JSON header line (description, type) plus
  the body, the store's revision counter is kept by the hash of the text it was read as, and
  `memory_conflict` from member-save becomes `revision_conflict`. Dirty guards and the file
  editor's resolution panel now cover it. A new memory (no name to key a buffer by) still keeps its
  draft in the pane; moving it onto the hook is the remaining part of #4569.

# 0036. Working sets — the definitions live in ui-prefs, the selection is device-local, and the server is not involved

English | [日本語](0036-working-sets.ja.md)

- Status: **adopted and implemented** (awaiting a look at real hardware). The design is [docs/52](../log/52-working-sets.md).
- See also: [0028](0028-deletion-lock.md) (the precedent for cutting across three entity types) / `console/src/lib/settings.ts` and
  `workspace/agent/ui_prefs.go` (the ui-prefs route) / `workspace/agent/model_deny.go` (hiddenModels — the earlier example of a display filter)

## Context

As concurrent pieces of work pile up, the left pane mixes every repository, assistant conversation
and session together. The only existing means of tidying are archiving (hiding) and the deletion
lock; there is no way to cut it down to "just the things for what I am working on now". A session is
tied to a repository by `session.repo`, but a conversation is tied to nothing.

## Decision

**A working set = a named collection of { working copies, conversations, sessions with no repo }.
It is completed on the front end as a display filter, with no new entity or REST on the server.**

1. **The definitions are one ui-prefs key (`workingSets`).** The requirements — per user,
   synchronised across devices, and comfortably within 64KiB — are met by the existing route as is.
   The Agent can read the same key later with `readUIPrefs()` (hiddenModels is exactly this shape),
   so the server only needs to get involved when notifications or operator integration call for it.
2. **The selected set is device-local** (the `DEVICE_LOCAL` key `workingSetActive`). "Which set am I
   looking at" is device-dependent state like the theme or audio on/off — looking at different work
   on a PC and a tablet is natural.
3. **Sessions are not put into a set directly; they inherit from the repository.** This avoids
   adding another source of truth for membership. Only sessions with no repo (shell/ssm and the
   like) are assigned directly, as an exception.
4. **A worktree automatically follows its parent base.** Allowing individual assignment would make
   the launch flow (creating a worktree) demand a set operation, whereas following changes nothing.
5. **Anything created while a set is selected joins that set automatically** (a clone, a new
   conversation, a repo-less launch). Otherwise it "disappears the moment you make it".
6. **References to entities that have gone are left alone** (the predicate simply does not match —
   harmless). Automatically pruning before loading completes is not done, because "not loaded yet"
   and "deleted" cannot be distinguished.
7. **Schedules (the second instalment) prefer derivation.** They persist on the CP, but membership
   can be derived on the front end: they follow the existing membership of the repo they launch
   into, the conversation that created them (`owner_conv`), or `reuse_target` (a conversation or a
   session), and only those matching none of these are assigned directly by id. Because the operator
   creates them via MCP there is no creation seam in the Console for "automatically join the selected
   set" to hook into — following `owner_conv` is the substitute. Rows with a derived membership are
   shown with the toggle checked and disabled.

## Impact

- Zero server changes (the wire, routes.go and the CP allowlist are all untouched).
- Console: a fixed selector at the top of the left rail, assignment in the row menu, ANDing a
  predicate at the confluence of `ProjectTree` / `OtherSessionsSection` / `AssistantSection` /
  `FilesSection`, a zustand store plus pure-function predicates, palette registration, and i18n.

## Options rejected

- **An external ledger on the Agent (`working-sets.json`) plus new REST**: it would let the server
  treat them as first-class, but the cost (registering on both sides of the CP allowlist, and so on)
  is not worth it for v1's "separate the display". ui-prefs is readable from the Agent anyway.
- **The CP database (a `memo_category`-style table)**: repositories, sessions and conversations
  actually live on the Agent side, and the CP cannot keep the references consistent.
- **A `WorkingSet` field on each entity's meta**: a working copy has no AF-owned metadata, so it does
  not work. Multiple memberships are also awkward to express.
- **A dedicated "unfiled" view**: useful as a tidying route, but judged unnecessary for v1
  (confirmed with the user). "All" is enough.
- **Individual assignment of worktrees**: as in decision 4, following is enough.

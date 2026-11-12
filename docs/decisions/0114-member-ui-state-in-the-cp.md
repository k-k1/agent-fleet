# 0114. Member UI state in the control plane: what the Console needs before the workspace runs, kept per membership

English | [日本語](0114-member-ui-state-in-the-cp.ja.md)

- Status: **accepted** (2026-10-13). P1 is built: the `member_ui_state` table,
  `GET`/`PATCH /api/me/ui-state` (`control-plane/member_ui_state.go`), and the Console store that
  holds the first-run state and feeds the Home view. P2 is built: `tenant_member_defaults`, the
  admin's `GET`/`PUT /api/admin/tenants/{slug}/member-defaults` (`control-plane/tenant_member_defaults.go`),
  `defaults` on the member's answer, and the default agent named by the first-run card. The role
  moved to #2032, and P3 is open.
- Tracking: #2027 (this record), #2028 (Home, the first reader). Related: #2026 (the first-day path
  for junior engineers), #2032 (role presets), #2033 (SRE Home and ops settings) /
  [0078](0078-sessions-overview-pane.md) (the overview, whose ordering Home reuses) /
  [0103](0103-branch-naming-rules.md) decision 10 (the tenant-to-member pattern P2 follows).

## Context

The Console is getting a Home view and, later, role presets (#2026). Both decide **what a member
sees**. They have to work in two situations the existing stores do not cover:

- **The workspace is stopped.** The rail and the Home render before the workspace starts. `ui-prefs`
  lives inside the workspace (`workspace/agent/internal/uiprefs`), so the Console cannot read it
  until the workspace is running.
- **The member uses another device.** The first-run card keeps "later" and the chosen track in
  `localStorage` (`af.onboarding.dismissed`, `af.onboarding.track`), so a phone shows the welcome
  card again after the desktop dismissed it.

The other half of #2026 is tenant defaults: the default agent, the default role, launch defaults and
the like. Tenants differ, so these are tenant configuration. They need the same place to live.

## Decisions

1. **Two kinds of settings, two homes.** A setting the Agent acts on stays in `ui-prefs`. Examples
   are the launch guard's hidden models, `skipPermissions`, and the templates the launch dialog
   inserts. A setting the Console alone reads goes to the CP. That covers presentation state, and
   anything the Console must read while the workspace is stopped. Never keep the same setting in
   both: two copies drift, and nothing tells you which one is right.
2. **Per membership, not per identity.** Roles and defaults are tenant-scoped, and a person in two
   tenants can be a developer in one and an SRE in the other. Every row is keyed by
   `membership_id`, and the routes resolve it with `withMembership`, the same route the memo queue
   uses. That route never builds a workspace.
3. **One JSON document per membership, with a closed schema.** The table is
   `member_ui_state(membership_id, state, version, updated_at)`. The CP decodes the document with
   unknown fields refused (`DisallowUnknownFields`) and caps it at 16 KiB. A JSON document rather
   than columns, because the keys grow with each surface (onboarding today, role presets and hints
   next), and the CP never queries by them. A closed schema rather than an open map, because an
   open map becomes a dumping ground no one can read back.
4. **`PATCH` replaces top-level keys and leaves the rest alone.** Two tabs or two devices write
   different keys: the first-run card on one, a future hint on the other. A whole-document `PUT`
   would let one silently undo the other. The CP merges under an optimistic `version`. It reads
   the row, merges, and writes `WHERE version = ?`, retrying a few times when it loses. This works
   the same in SQLite and Postgres, without `SELECT … FOR UPDATE`.
5. **Not secret, not audited.** This is a member's own presentation state. A secret never goes in.
   P2's tenant writes are an admin action, so they are audited the way branch rules are.
6. **The Console migrates `localStorage` once.** On the first load where the CP document has no
   `onboarding` key but `localStorage` does, the Console uploads it with `PATCH ?if_absent=1`. The
   CP then fills only keys it does not hold yet, checked inside the optimistic loop, so a late
   upload from a second tab cannot undo a value saved in the meantime. The old keys are removed
   only after the CP confirmed the upload. `localStorage` stays as a read cache for the first
   paint only, keyed per (user, tenant).
   The upload waits while the tab holds an unconfirmed choice of its own.
   The CP answer wins once it arrives, with these guards:
   - Every owner switch starts a new epoch, and an answer is used only in the epoch its request
     was made in. A key comparison alone would accept a stale answer after A, B, A.
   - PATCHes go one at a time and carry the tenant they were made for. A PATCH still queued at a
     switch is never sent; otherwise it would write into the other tenant's row.
   - Unconfirmed keys are shown on top of every answer. A failed key stops overlaying, so the
     next answer shows what the CP holds.
   - A GET that started before a confirmed PATCH does not replace that PATCH's answer.
7. **Lifecycle follows the membership.** The row is deleted in `deleteMembershipTx`, with the other
   per-membership tables. Like the inbox, it declares no foreign key: an older CP's hard delete has
   its own cascade list, and a key it does not know about would make that delete fail.
8. **Precedence for tenant defaults (P2).** From lowest to highest: built-in, tenant default,
   the admin's per-member setting, then the member's own choice. The CP computes the effective
   value and returns it next to the member's own `state`, so the rule lives in one place. The
   Console never re-derives it. The default agent has no other layer: it only names an agent and
   restricts nothing, so a member has nothing to override. The answer therefore carries it as
   `defaults`, unchanged. `effective` arrives with the first key that does have layers, the role.

## Phases

- **P1 (built):** `member_ui_state`, `GET`/`PATCH /api/me/ui-state`, the `onboarding` key
  (`dismissed`, `track`), the Console store, the one-time `localStorage` migration, and Home (#2028)
  as its first reader.
- **P2 (built):** `tenant_member_defaults` with one key, `agent`. The tenant admin sets it in
  Tenant settings → Member defaults (audited as `tenant.member_defaults`). Members read it as
  `defaults` on `GET /api/me/ui-state`, and the first-run card's connect step names it. Any agent
  still completes the step.
- **Moved to #2032:** the role preset's tenant default, the admin's per-member role, the member's
  own choice and `effective` (decision 8). A role nobody can see the effect of would be a setting
  that does nothing, so it is built together with the presets that read it.
- **P3:** launch defaults, team repositories and non-secret ops settings, with #2032 and #2033.
  Open question: how tenant launch defaults reach the Agent, which enforces some of them
  (decision 1). They could prefill the dialog only, or seed `ui-prefs`.

## Consequences

- The Console gains one boot request (`GET /api/me/ui-state`). It is answered from the CP database
  alone, so it does not wait for the workspace.
- A member's first-run state now follows them across devices.
- Any new Console-only state has an obvious home. Decision 1 is the test for whether it belongs here.

## Note (2026-11-12): P3 open question settled

The user decided how tenant launch defaults reach the Agent: **they only prefill the launch dialog
and the Start hub.** They never seed or write the Agent's `ui-prefs`, and the Agent keeps enforcing
its own `ui-prefs` as before. Decision 1 stands: no setting lives in two homes.

- The tenant's groups (launch defaults, team repositories, non-secret ops settings) extend
  `tenant_member_defaults` and the same admin endpoint and `tenant.member_defaults` audit. The
  launch agent is the document's existing `agent`.
- The member's own override is Console-only state, so it follows decisions 1 and 3: closed-schema
  `launch` and `ops` keys in `member_ui_state`.
- Precedence is decision 8 unchanged (built-in, tenant default, the member's own choice). The CP
  merges the layers field by field and returns the result as `effective` next to `defaults`; the
  Console reads it and never re-derives it.
- Availability follows the branch-rules precedent (0103 decision 10): the Console keeps the last
  answer in its per-(user, tenant) cache, so the dialog and Start honour the last-known value
  while the workspace is stopped or the CP is unreachable.
- Ops settings are validated narrowly (https URLs without credentials, query or fragment, a
  12-digit account id, a role name). A secret has no field to ride in; secrets stay per member.

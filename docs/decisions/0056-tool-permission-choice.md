# 0056. The user chooses whether to skip permission prompts. The default does not change

English | [日本語](0056-tool-permission-choice.ja.md)

- Status: **adopted** (2026-08-24). The record of the investigation and the measurements is [docs/76](../log/76-tool-permission-choice.md).
- See also: [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (what catches an approval folded up while waiting) /
  [build/07 Security](../build/07-security.md) §threat model (the very premise that the container boundary is the only fortress)

## Context

Every kind in the fleet started with a flag that skips all tool approvals (claude
`--dangerously-skip-permissions`, cursor `--force`, copilot `--allow-all`, kiro `--trust-all-tools`,
agy the same, codex's two bypass variants, opencode `--auto`). The grounds were two: "the container is
the sandbox", and "if it stops for approval there is no way to answer from the Console". The latter
expired the moment each kind's approval route went in (the status hook's `permission` state; ACP's
`session/request_permission` → Interaction).

## Decision 1 — the default stays as it is (skip)

The goal is to make it possible to turn off, not to change the default. **Missing, unset and corrupt
values all fall to "skip"** (`SkipPermissions` in Go and `skipPermissions !== false` in the Console).
Falling the other way would put every session into waiting-for-approval on a client that read prefs
written before this feature — the hardest kind of breakage to notice.

## Decision 2 — three layers: an explicit session setting > the per-kind default > true

A per-kind default alone cannot express "normally with approvals, but let this one run
automatically", and per-session alone would mean choosing every time. It is aligned with the shape of
model / effort / startMode. The crux is making `Meta.SkipPermissions` a `*bool` (three-valued) —
collapsing `false` and "unset" means **an existing setting keeps winning even after the default is
changed**.

## Decision 3 — resolve it inside the Agent's process (do not resolve it in the Console alone)

The Console can put the value on a launch request, but **there are launch paths that do not go through
the Console**: MCP's `create_session`, scheduled execution, restarting a stopped session, and
fork/recreate. Having the Agent read ui-prefs directly (`readUIPrefs` — the same precedent as
hiddenModels and opencodeCatalog) makes the same default apply on every path.

**Rejected**: have the Console always send an explicit value. Then changing the setting still leaves
sessions launched beforehand restarting with the old value baked in. In fact the Console sends it
**only when the launch dialogue was touched**.

## Decision 4 — a plan launch never skips, whatever the kind

The long-standing behaviour is folded into one place, `BypassPermissions`
(= `mode != "plan" && SkipPermissions`). The flag-stripping for `mode == "plan"` (`ReplaceAll`) that
was scattered across the kinds was copied out anew each time one was added, and it had in fact drifted
subtly between kinds.

## Decision 5 — only kinds whose approval prompts can be answered from the Console get the choice

`Caps.PermissionChoice` (`caps.permissionChoice` on the Console side). Removing the flag is possible
for any kind, but a session stopped on an approval dialogue nobody can answer is, from the user's point
of view, silently frozen. Following "do not raise an unverified cap" (the lesson of 1854d), it starts
with claude / cursor / copilot / kiro / agy. codex and opencode come after their routes are built
([docs/76](../log/76-tool-permission-choice.md) §76.4).

## Decision 6 — refuse "with approvals" on an unsupported kind rather than ignoring it silently

`POST /sessions` returns 400 with `permission_choice_unsupported`. Ignoring it would leave the caller
believing it is running with approvals. On the ui-prefs side, conversely, it **is ignored silently**
(being unable to launch at all because of an old or corrupt setting is worse) — the asymmetry is fine
here.

## Impact and what remains

- The default stays on, so with nothing configured the behaviour is unchanged.
- **Nothing is lost if it is folded up while waiting for approval** (ADR 0055 decisions 12/13, docs/76
  §76.5). Carrying over covers every kind and both routes (`agents.ModalReporter`). But **only "what
  was being asked" is carried**, not the yes/no itself — the answer's destination (ACP's JSON-RPC id,
  the TUI's modal) died with the process. So a session with approvals on will **redo the approval**
  after being folded up.
  ⚠️ And if the container is SIGKILLed, approvals living only in the ACP handle or the pane are lost
  (they can be captured on `halt` and on a normal stop).
- A session with approvals on will not complete under unattended operation (scheduled execution / the
  operator / MCP drive). A note to that effect is in the settings UI.
- Remaining: the routes for codex and opencode, showing "with approvals" in the list and the mirror,
  and one full pass on a real TUI.

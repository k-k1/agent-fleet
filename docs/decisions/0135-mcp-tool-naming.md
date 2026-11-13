# 0135. A naming rule for MCP tools on both servers, and what to do with `af_report` / `af_stop_after_turn`

English | [日本語](0135-mcp-tool-naming.ja.md)

- Status: **accepted** (2026-11-13): the rule of section 1, and **option A** of section 3 (`af_report` and
  `af_stop_after_turn` keep their names as the documented exception). Option B is the rejected alternative.
  No tool is renamed, aliased or re-described. The check of section 4 (rules 1 to 3) is added with this
  acceptance; rule 4 is not checked.
- Tracking: #3990.
- See also: [0124](0124-second-refactor.md) (the tool-table equivalence goldens this record reads) /
  [0041](0041-cross-session-messaging.md) (`send_to_peer_session`) /
  [0105](0105-stop-continues-into-the-queue.md) (`stop_session_after_turn`)
- Code references are `path` as of `main` at `14f9d74f4`. Paths are relative to the repository root;
  `workspace/agent/` is shortened to `agent/`. Counts are from `git grep` at that commit.

## Context

Two MCP servers advertise tools. The Control Plane's `/mcp` (`control-plane/internal/mcpsrv`) serves a
member's own clients; the Agent's in-workspace server (`agent/internal/mcpx`) serves the session itself
and the assistants. Their `tools/list` output is held in goldens, `*/testdata/mcp_tools/list.json`, and
the generated catalogue `guide/ref/mcp/` is built from them.

No ADR, comment or test states how a tool is named. The names grew by copying their neighbours, and the
result is mostly consistent by accident: 75 of 87 distinct names already read `<verb>_<object>`. What
does not fit is listed in section 2. The one visible oddity is the prefix: `af_report` and
`af_stop_after_turn` are the only tools whose names start with `af_`, and a client already shows every
tool as `mcp__<server>__<tool>`, so the prefix adds nothing a reader can use.

### What was measured

- **Counts.** The Control Plane golden has 43 tools. The Agent golden has 90 entries, written
  `name#n`, one per distinct definition, because a tool with a different description or schema for the
  session and for the assistants appears twice (`create_session#1`, `create_session#2`). That is 133
  entries and **87 distinct names** (43 CP, 76 WS, 32 on both). Issue #3990 says 119 entries; I could
  not reproduce that number from the goldens and use the figures above.
- **Same name on both servers.** 32 names are on both. The argument names of the two definitions agree
  for 35 of the 45 pairs. The 10 that differ: `create_session#1` / `#2` (the Agent adds `effort`, and for
  the session audience `allow_shared_working_copy`, `report_back`, `spend_cap_usd`, `work_item`),
  the seven `memory_*` tools (the Control Plane takes `project` and `repository`; the Agent's session
  server fixes both) and `update_memo#2` (the Control Plane has `position`).
- **Same name, narrower scope.** Inside the Agent server the session audience reuses the operation's name
  for a narrower target: `get_session_output#2`, `stop_session#2` and `stop_session_after_turn#2` act
  on the session's own children only, and their descriptions say so. Tools for a target the narrower
  form cannot reach have a new name instead: `list_peer_sessions`, `peek_session_output`,
  `send_to_peer_session`.
- **Nothing outside the tool definitions matches on the two `af_` names at run time.**
  `mcpreg.IsAFToolName` (`agent/internal/mcpreg/af_server_name.go`) is only used for `generate_image`.
  The names are in the tool definitions (`agent/internal/mcpx/mcp_self_report.go`), in the
  `[agent-fleet]` note sent with an instruction that owes a report
  (`agent/internal/sessionx/session_selfreport.go`), and in the Console's plan-review prompt
  (`console/src/lib/i18n/locales/{en,ja}/mirror.ts`).

## Decision proposed

### 1. The rule

A tool name on either server:

1. **Carries no prefix.** No `af_`, no server name. The client adds `mcp__<server>__`; a second prefix is
   noise and, for `af_`, makes the name look like the server's.
2. **Is lower-case ASCII `snake_case`**: `^[a-z][a-z0-9]*(_[a-z0-9]+)*$`. Every current name already is.
3. **Is verb-first**: `<verb>_<object>` (`list_repos`, `stop_session`). A bare verb is allowed only
   where the verb alone names the operation and its subject is fixed (today `watch` and `unwatch`, which act on
   the session's watches). Only the first-word test is mechanical. Using `list_` for a collection and `get_` for
   one thing or one state is a convention for the reviewer, not for a check: today `get_schedule_runs` (a
   history of runs), `get_session_usage` and `get_agent_usage` (one row per session or CLI) return several
   rows under `get_`, and they are accepted as they are.
4. **Means one operation on both servers.** The same operation has the same name and the same argument
   names wherever it is served. A narrower scope for one audience (children only) keeps the name and says
   so in the description; a target the narrower form cannot reach gets a qualifier after the verb
   (`send_to_peer_session`, `list_peer_sessions`). A name never means two different operations.
5. **Is renamed only with an alias.** The old name stays callable, but is not listed, for at least one
   release, so a session that started before the upgrade and holds the old name in its prompt keeps
   working for that period. The alias narrows the failure, it does not remove it (section 3, "Sessions
   already running").

Two families are grandfathered rather than renamed, because their names are the shared memory API
(ADR 0022 / 0130) and a rename would reach every agent kind's memory prompt: the noun-first
`memory_*` (8 tools) and the two single exceptions `branch_name` and `skill_restore`. A new tool does
not get that allowance.

### 2. Inventory of every current name against the rule

87 distinct names, grouped by first word. `(CP)` is Control-Plane-only, `(WS)` is Agent-only, no mark
means both servers. Rule 1 and 2 hold for everything except the two `af_` names.

| First word | Names | Tools |
|---|---|---|
| `list` | 16 | `list_allowlist` (CP), `list_assistants` (WS), `list_child_sessions` (WS), `list_chromium_targets` (WS), `list_cleanup_archives`, `list_cleanup_candidates`, `list_memory_snapshots` (WS), `list_memos`, `list_models`, `list_my_sessions`, `list_peer_sessions` (WS), `list_repos`, `list_schedules` (WS), `list_sessions` (CP), `list_watches` (WS), `list_workspaces` (CP) |
| `get` | 12 | `get_agent_usage`, `get_browser_action_result` (WS), `get_chat_plan` (WS), `get_chromium_attachment` (WS), `get_egress_stats` (CP), `get_image_studio` (WS), `get_memory_snapshot` (WS), `get_schedule_runs` (WS), `get_session_output`, `get_session_status`, `get_session_usage`, `get_usage` (CP) |
| `memory` | 8 | `memory_feedback`, `memory_forget`, `memory_index`, `memory_patch`, `memory_projects` (CP), `memory_read`, `memory_save`, `memory_search` |
| `delete` | 5 | `delete_branch`, `delete_memo`, `delete_schedule` (WS), `delete_session`, `delete_worktree` |
| `set` | 4 | `set_chat_plan` (WS), `set_chromium_control_mode` (WS), `set_image_draft` (WS), `set_user_quota` (CP) |
| `stop` | 4 | `stop_session`, `stop_session_after_turn`, `stop_user_session` (CP), `stop_workspace` (CP) |
| `add` | 2 | `add_image_knowledge` (WS), `add_memo` |
| `af` | 2 | `af_report` (WS), `af_stop_after_turn` (WS) |
| `create` | 2 | `create_schedule` (WS), `create_session` |
| `propose` | 2 | `propose_allowlist_change` (CP), `propose_session_handoff` (WS) |
| `restore` | 2 | `restore_cleanup_archive`, `restore_memory_snapshot` (WS) |
| `resume` | 2 | `resume_schedule` (WS), `resume_session` |
| `run` | 2 | `run_image_trial` (WS), `run_schedule_now` (WS) |
| `send` | 2 | `send_to_peer_session` (WS), `send_to_session` |
| `update` | 2 | `update_memo`, `update_schedule` (WS) |
| `answer` | 1 | `answer_session_question` (WS) |
| `archive` | 1 | `archive_session` |
| `ask` | 1 | `ask_assistant` (WS) |
| `attach` | 1 | `attach_chromium` (WS) |
| `branch` | 1 | `branch_name` (WS) |
| `detach` | 1 | `detach_chromium` (WS) |
| `flush` | 1 | `flush_memos` |
| `generate` | 1 | `generate_image` (WS) |
| `link` | 1 | `link_work_item` (WS) |
| `pause` | 1 | `pause_schedule` (WS) |
| `peek` | 1 | `peek_session_output` (WS) |
| `purge` | 1 | `purge_cleanup_archive` |
| `rename` | 1 | `rename_child_session` (WS) |
| `request` | 1 | `request_browser_action` (WS) |
| `respond` | 1 | `respond_session_plan` (WS) |
| `search` | 1 | `search_sessions` (WS) |
| `skill` | 1 | `skill_restore` (WS) |
| `tail` | 1 | `tail_audit` (CP) |
| `unwatch` | 1 | `unwatch` (WS) |
| `watch` | 1 | `watch` (WS) |

Against the rule:

| Rule | Conforms | Does not |
|---|---|---|
| 1 prefix | 85 | `af_report`, `af_stop_after_turn` |
| 2 shape | 87 | none |
| 3 verb-first | 75 | the 8 `memory_*`, `branch_name`, `skill_restore`, and the 2 `af_` names (their first word is the prefix) |
| 4 same operation | 32 shared names, 10 pairs differ in argument names (see above) | `peek_session_output` breaks the "qualifier after the verb" form (it is `get_..._output` for a non-child); no rename proposed |
| 5 alias on rename | no rename has happened under this rule | |

The exceptions to hold in a check are therefore 12 names for rule 3 (`memory_*` ×8, `branch_name`,
`skill_restore`, `af_report`, `af_stop_after_turn`) and 2 for rule 1 (the same two).
These are the exceptions to what a check can test: the shape, the prefix and the first word. The
meaning-level points are for the reviewer and are not in the allowlist: the `list_` / `get_` convention
(deviations known today: `get_schedule_runs`, `get_session_usage`, `get_agent_usage`), the bare verbs
`watch` / `unwatch`, rule 4's qualifier form (`peek_session_output`), and whether two tools are the same
operation at all.

### 3. `af_report` and `af_stop_after_turn`: two options

Both exist only on the Agent server (`session` audience); the Control Plane has neither.

#### Option A. Keep both as a documented exception

The rule gets one sentence: "`af_report` and `af_stop_after_turn` keep their names; they are the two
tools about the calling session itself and predate the rule". They go into the check's allowlist and
stay there. Nothing else changes.

#### Option B. Rename, with the old names as aliases for at least one release

Candidate names, verb-first: `af_report` → `report_done`, `af_stop_after_turn` → `stop_self_after_turn`.
(`stop_after_turn` alone would sit next to `stop_session_after_turn` and read as the same tool.) The
old names stay callable and unlisted, so `tools/list` and the catalogue show only the new ones.

What a rename touches, from `git grep -E 'af_report|af_stop_after_turn'` at `14f9d74f4`: **71 files, 213
lines** in all.

| Surface | Files | Lines | Action under B |
|---|---|---|---|
| Behaviour: the two tool definitions (`agent/internal/mcpx/mcp_self_report.go`), the `[agent-fleet]` note (`agent/internal/sessionx/session_selfreport.go`), the Console plan-review prompt (`console/src/lib/i18n/locales/{en,ja}/mirror.ts`) | 4 | 17 | change; add the alias dispatch |
| Other Agent code, comments only (`muse`, `chatx`, `mcpreg`, `mcpx`, `sessionx`, `control-plane/scheduler_wake.go`) | 11 | 17 | reword comments |
| Policy, topic notes and agent knowledge (`workspace/workspace-notes.md`, `workspace/notes/agent-fleet.md`, `agent/knowledge/af-usage.md`) | 3 | 6 | change; the first two reach every agent at the next image build |
| Tests and goldens (`mcpx` `list.json` and `calls.txt`, `sessionx` `create_success.golden`, and 15 `_test.go` / `.test.ts` files) | 18 | 87 | regenerate the goldens; the `calls.txt` golden gains alias lines |
| Catalogue source `scripts/mcp-catalogue/tools.tsv` and the generated `guide/ref/mcp/README{,.ja}.md` | 3 | 6 | edit the tsv, regenerate |
| Console comments (`stopTree.ts`, `SessionMenu.tsx`, `session.ts`) | 3 | 3 | reword |
| `docs/build/04-agent` and `08-integrations`, both languages | 4 | 4 | change |
| ADRs 0035, 0038, 0041, 0069, 0093, 0095, 0105 (both languages, 13 files) | 13 | 34 | **do not edit**: an ADR is immutable |
| `docs/log/` journals | 12 | 39 | **do not edit**: history |

Two of those rows matter more than their size.

- **Sessions already running.** A session started before the upgrade holds the old name in its first
  prompt, and a client may cache its tool list. Without an alias that session calls a tool that is gone
  and never reports (`session_selfreport.go` says as much: "a session told to call `af_report` when it has
  no such tool simply never reports"). The alias covers these sessions only while it exists. A session that is resumed after the alias is
  removed, or a client that keeps an old tool list past that point, still calls the old name and gets an
  unknown-tool error, and the instruction it carried is never reported. Two conditions follow, and both are
  part of B: (1) the removal is a release, not a calendar day, because the Agent updates itself (ADR 0025),
  and it waits until the alias has logged no call for a whole release (the alias dispatch has to count its
  calls, which today nothing does); (2) after removal the unknown-tool error for the old name says which
  tool replaced it, so a resumed session can correct itself. Neither makes B risk-free.
- **The repository can never drop the old name.** The 25 immutable ADR and journal files keep it, so
  `git grep af_report` stays non-empty after B, and the alias code needs a comment saying why.

#### Recommendation: A

1. The gain is cosmetic: 2 of 87 names. The rule and its check work with the two on an allowlist, so
   A loses no enforcement.
2. B's cost is not the 213 lines, which are mostly mechanical, but the in-flight sessions and the alias
   it has to carry and later remove, for a change no agent or user would notice.
3. `af_` means "Agent Fleet", not "this session", and the description and schema are what a model reads to
   pick between `af_stop_after_turn` and `stop_session_after_turn`. What the prefix may do is keep the two
   names from colliding on sight; that is a possibility, not something measured here. A rename to
   `stop_self_after_turn` would have to carry that distinction in the name some other way.

Revisit B when a change that has to re-issue those two definitions anyway (a schema change, say) comes
along, or when a third `af_` tool is proposed.

### 4. Enforcing the rule for new tools

Not added in the proposing change: a check that holds a rule the user has not accepted yet would be CI law
before the decision. It is small, and independent of A and B, so it lands with the change that flips this
record to accepted (`problems()` in `scripts/mcp_catalogue.py`, run by the `docs` workflow):

- Where: `problems()` in `scripts/mcp_catalogue.py`, which `check_mcp` in `scripts/docs-check.py` already
  runs over both goldens. Its test is `McpCatalogueTests` in `scripts/test_docs_check.py`.
- What it checks, per distinct name (the part of `name#n` before `#`): rule 2 by regex; rule 1 by "does not
  start with `af_`"; rule 3 by "the first word is in a closed verb list". The list is the 31 verbs in use
  today (`add`, `answer`, `archive`, `ask`, `attach`, `create`, `delete`, `detach`, `flush`, `generate`,
  `get`, `link`, `list`, `pause`, `peek`, `propose`, `purge`, `rename`, `request`, `respond`, `restore`,
  `resume`, `run`, `search`, `send`, `set`, `stop`, `tail`, `unwatch`, `update`, `watch`); a new verb is a deliberate edit.
- The allowlist: the 12 names of section 2. Under A it stays; under B the two `af_` names leave it.
- Rule 4 can be checked the same way (argument names of a shared name agree) with the 10 known pairs
  allowlisted. It is the larger half and can wait.

The check must be shown failing on a planted violation (`af_new_tool` in a golden) before it is trusted.

## Options rejected

- **Keep the prefix as the rule** (every Agent-server tool gets `af_`). 74 renames, and the prefix still
  repeats what the client's `mcp__<server>__` already says.
- **Rename to `stop_after_turn`** under B. It reads as the child form `stop_session_after_turn`.
- **List the old names as well, as aliases, in `tools/list`.** The catalogue and the golden would show
  both names for a release, which is the inconsistency this record is meant to remove.

## Open questions only the user can answer

1. Accept the rule of section 1, with the `memory_*` / `branch_name` / `skill_restore` grandfathering?
2. A or B for the two `af_` tools? Recommended: A.
3. If B: the candidate names, and how many releases the alias stays.

## Consequences

- Accepting this record changes no behaviour. It makes the rule citable, so a review can point at it.
- Under A the check in section 4 can be added with 12 allowlisted names and never needs to change for
  today's tools. Under B it is added with 10, and the rename is its own change.

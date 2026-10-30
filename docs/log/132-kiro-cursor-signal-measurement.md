# 132. Kiro ACP recorder and Cursor stop hook — signal measurement (#1082)

status: **Measured (2026-10-17).** The two open items of #1082 were run against real, logged-in
CLIs in tmux sessions of their own. Kiro: the recorder works and is recommended (follow-up #2285),
but it was measured on the installed 2.31.0, **not on the 2.32.0 pin**. Cursor: the pin was
measured, the `stop` hook carries usage, which corrects the Track D-1 conclusion in
`docs/log/40-cursor-agent-kind.md` (follow-up #2286). The OpenCode item of #1082 was done in #1630
and is not touched here.

## 0. Versions and setup

| CLI | Pinned (`workspace/Dockerfile`) | Installed here | Measured on |
|---|---|---|---|
| kiro-cli | `KIRO_VERSION=2.32.0` | 2.31.0 (`kiro-cli --version`) | **2.31.0** — the pin was not installed (the image is ~855 MB unpacked; the on-demand install would also replace the user's CLI) |
| cursor-agent | `CURSOR_VERSION=2026.10.16-e3cb207` | 2026.10.16-e3cb207 | 2026.10.16-e3cb207 (= pin) |

Both CLIs were already logged in (Kiro: Builder ID; Cursor: `cursor-agent status`). No login flow
was run. All files lived under `$AF_WORK_DIR`; the tmux sessions (`af1082_kiro`, `af1082_kiro2`,
`af1082_cursor`, `af1082_cursor2`) were all started and killed by this run. Values below are
redacted: e-mail and paths are dropped, ids shortened with `…`.

## 1. Kiro (Terminal): `KIRO_ACP_RECORD_PATH`

### 1.1 Where it lives

The string is not in the native binaries (`kiro-cli`, `kiro-cli-chat`, `kiro-cli-term` — grep finds
only `KIRO_RECORD_API_REQUESTS_PATH` / `_RESPONSES_PATH`). It is in the v2 TUI bundle
`~/.local/share/kiro-cli/tui.js`:

```js
let e=process.env.KIRO_ACP_RECORD_PATH; if(!e) return null; ... z.info(`[acp-recorder] recording ACP traffic to ${e}`)
```

The recorder wraps the TUI's ACP stream and writes one line per message with `dir` (`out` = TUI
→ agent, `in` = agent → TUI). It flushes also on `SIGINT`/`SIGTERM`/`beforeExit`.

### 1.2 Steps

1. `KIRO_ACP_RECORD_PATH=$AF_WORK_DIR/kiro-acp.jsonl kiro-cli chat` in a tmux session (cwd in
   `$AF_WORK_DIR`; no `--trust-all-tools`, so shell commands ask). A second run used the fleet's
   engine flag, `kiro-cli chat --agent-engine v2` (`program.go` adds `--trust-all-tools` on top).
   The first start shows a "Switch to V3" prompt; "Remind me later" was chosen.
2. Prompt: `Run the shell command: touch probe.txt`. The pane showed `shell requires approval` with
   `Yes, single permission` / `Trust, always allow in this session` / `No (Tab to edit)`.
3. While the panel was open, the file already held the request (the file is written live, not at
   exit). Then answered in the pane: Yes (Enter), No (Down Down Enter), Esc, and — last — a prompt
   whose tmux session was killed while the panel was open.

### 1.3 Evidence (JSONL lines; `ts` in ms; session/tool ids shortened)

The request, `dir:"in"`. The `id` is a string UUID (prompt/initialize ids are integers):

```json
{"ts":…678,"dir":"in","msg":{"jsonrpc":"2.0","method":"session/request_permission","params":{"sessionId":"7f1f13a1-…","toolCall":{"toolCallId":"tooluse_zk4K…","title":"Running: touch probe.txt","rawInput":{"__tool_use_purpose":"Run the touch command to create probe.txt","command":"touch probe.txt"}},"options":[{"optionId":"allow_once","name":"Yes","kind":"allow_once"},{"optionId":"allow_always","name":"Always","kind":"allow_always"},{"optionId":"reject_once","name":"No","kind":"reject_once"}],"_meta":{"trustOptions":[{"label":"Full command","display":"touch probe.txt","setting_key":"allowedCommands","patterns":["touch probe\\.txt"]},{"label":"Partial command", …},{"label":"Base command", …}]}},"id":"217c1a80-…"}}
```

The responses, `dir:"out"`, same `id` (taken 20 s after the request for "Yes", i.e. when the human
pressed the key):

```json
{"ts":…516,"dir":"out","msg":{"jsonrpc":"2.0","id":"217c1a80-…","result":{"_meta":{"provenance":"human"},"outcome":{"outcome":"selected","optionId":"allow_once","_meta":{"provenance":"human"}}}}}   // Yes
{"ts":…,"dir":"out","msg":{"jsonrpc":"2.0","id":"64d6c7ca-…","result":{"_meta":{"provenance":"human"},"outcome":{"outcome":"selected","optionId":"reject_once","_meta":{"provenance":"human"}}}}}   // No
{"dir":"out","msg":{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"7f1f13a1-…"}}}                                        // Esc, then:
{"dir":"out","msg":{"jsonrpc":"2.0","id":"e2adf69a-…","result":{"outcome":{"outcome":"cancelled"}}}}                                         // no provenance
{"dir":"in","msg":{"jsonrpc":"2.0","result":{"stopReason":"cancelled"},"id":6}}                                                             // the prompt ends
```

After "Yes" the stream continues with `session/update` `tool_call_update` (`status:"completed"`) and
`{"result":{"stopReason":"end_turn"},"id":3}`. After "No" it carries `tool_call_update`
`status:"failed"`, `_meta.kiro.toolCallFailureReason:"permission_denied"`. Turn metadata
(`_kiro.dev/metadata`: `contextUsagePercentage`, and `meteringUsage` in credits plus
`turnDurationMs` at turn end) is in the same file.

Killing the tmux session while the panel was open left the request as the **last line** with no
response and no `stopReason`.

### 1.4 What is present / absent

| Field | Present? |
|---|---|
| Pending request, with the command (`rawInput.command`), `toolCallId`, `sessionId`, the offered options | yes, live |
| Matching response, paired by JSON-RPC `id` (string UUID) | yes: `selected` + `optionId` (`allow_once` / `reject_once`), or `cancelled` |
| Who answered | `_meta.provenance:"human"` on `selected`; absent on `cancelled` |
| A line when the pane/process dies with the request open | **no** — the request is simply never answered |
| Distinguishing "answered in the pane" from "answered by a Console button" | n/a for Terminal: the TUI is the only ACP client, so every response line is the pane's |
| `allow_always` ("Trust, always allow") | **not measured** (the option list shows it; the response was not captured) |
| 2.32.0 (the pin) | **not measured** — see §0 |

### 1.5 Could it replace the pane scrape (`kiro/state.go`)?

It is a strictly better source for **pending / resolved**: the scrape (`"requires approval"` in the
last 8 pane lines) cannot tell a resolved card from a still-drawn frame, and carries only the
panel's text line. The recorder gives the exact command and an unambiguous resolution event, and
the "TUI answers first" case that #1082 lists is simply the response line (there is no other
client). Edge cases and dedup:

- **Dedup key**: the request's `id` (string UUID); one pending set per recorder file. `toolCallId`
  is the secondary key (the same tool call later gets `tool_call_update`).
- **Resolved** = a response line with that `id`, **or** a later `session/prompt` result
  (`stopReason`), **or** `session/cancel`. Do not wait for `tool_call_update`: `reject` and
  `cancelled` paths differ.
- **Orphans**: when the process/pane disappears, the request is never answered. A pending id must
  also be dropped when the session's pane is gone, otherwise the card would stay forever.
- **File size**: the file holds every ACP message (~120 KB before the first prompt: three
  `commands/available` lines are ~40 KB each). Tail by byte offset; the per-session path must be set in
  the launch env (`KIRO_ACP_RECORD_PATH` is read once at TUI start, so a running session cannot
  be switched on later) and the file bounded or removed with the session.
- **Version drift**: the recorder is not a documented flag (found in the bundle, not in `--help`).
  Keep the pane scrape as the fallback when the file is absent or no recorder line arrives, and
  re-measure on every pin bump.

**Recommendation: implement (low priority), after re-running §1.2 on the 2.32.0 pin.** Follow-up:
#2285.

## 2. Cursor (Terminal): the `stop` hook

### 2.1 Steps

1. A throwaway project in `$AF_WORK_DIR/cursor-probe` with `.cursor/hooks.json` (project-level, so
   the user's global `~/.cursor` was not touched):
   `{"version":1,"hooks":{"stop":[{"command":"<cap.sh> stop"}],"beforeSubmitPrompt":[…],"afterAgentResponse":[…]}}`
   where `cap.sh` appends its stdin as one line to a log file.
2. `cursor-agent --trust` in tmux (the interactive TUI). Turn 1: `Reply with just: pong`. Turn 2:
   `Run ls in the shell, then say done`. Turn 3: a long counting prompt, interrupted with Esc after 5 s.
3. Headless: `cursor-agent --trust -p --output-format json 'Reply with just: pong'` in the same project.
4. A second TUI with `--model gpt-5.3-codex-low-fast` (explicit model), prompt `Reply with just: pong`.

### 2.2 Evidence (hook stdin, one JSON object per call; `user_email`, `workspace_roots`, `transcript_path` removed)

Turn 1, `stop`:

```json
{"conversation_id":"33cf6d19-…","generation_id":"8a8295bf-…","model":"default","status":"completed","loop_count":0,"input_tokens":10303,"output_tokens":74,"cache_read_tokens":0,"cache_write_tokens":0,"session_id":"33cf6d19-…","hook_event_name":"stop","cursor_version":"2026.10.16-e3cb207"}
```

Turn 2 (one tool call), `stop` — same `generation_id` as that turn's `beforeSubmitPrompt` /
`afterAgentResponse`:

```json
{"conversation_id":"33cf6d19-…","generation_id":"1a14f88f-…","model":"default","status":"completed","loop_count":0,"input_tokens":20958,"output_tokens":70,"cache_read_tokens":20608,"cache_write_tokens":0, …}
```

Turn 3, interrupted with Esc — **two** `stop` calls, same `generation_id`, no token fields:

```json
{"conversation_id":"33cf6d19-…","generation_id":"b90ee877-…","model":"default","status":"aborted","loop_count":0, …}
{"conversation_id":"33cf6d19-…","generation_id":"b90ee877-…","model":"default","status":"error","loop_count":0, …}
```

Explicit model (`--model gpt-5.3-codex-low-fast`): `beforeSubmitPrompt` and `stop` both carry
`"model":"gpt-5.3-codex-low-fast"`, but the turn ended with `"status":"error"` and no tokens
(the probable cause is a plan restriction on that model — see the Free-plan note in
`docs/log/40`; the reason was not captured, so a successful explicit-model turn is **not measured**).

`-p` under the same project: result JSON
`{"type":"result","subtype":"success",…,"usage":{"inputTokens":10307,"outputTokens":74,"cacheReadTokens":0,"cacheWriteTokens":0}}`
and **no hook line at all** (log file line count unchanged), so none of the three registered events (`stop`, `beforeSubmitPrompt`, `afterAgentResponse`) fired in this `-p` probe, `stop` in particular. The probe ran no shell tool, so other events (e.g. `beforeShellExecution`, which `docs/log/40` recorded firing under `-p` on an older version) were not measured.

### 2.3 What is present / absent

| Field | Present on `stop` (TUI)? |
|---|---|
| `generation_id`, `conversation_id` / `session_id` (equal), `hook_event_name`, `cursor_version` | yes |
| `model` | yes, but the **raw selector**: `"default"` for Auto (not the model Auto picked); the explicit id when `--model` was given |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | yes **only when `status == "completed"`**; absent for `aborted` / `error` |
| `status`, `loop_count` | yes (`completed` / `aborted` / `error`) |
| `transcript_path` | a path to the JSONL transcript (as before); `null` in `beforeSubmitPrompt` of a new chat |
| USD / cost | no |
| One `stop` per turn | yes for `completed`; **two** for an aborted turn (`aborted`, then `error`) |
| Fired under `-p` | not in this probe (`stop` not observed; other events not measured) |
| Whether `input_tokens` includes `cache_read_tokens` | **unresolved**: turn 2 has `input 20958`, `cache_read 20608`, while `-p` reports them as separate fields (`log/40` §usage probe: `inputTokens 3693`, `cacheReadTokens 22144`). Needs a priced-model comparison before any USD is computed |
| Multi-step turns: sum or last step | **not measured** (one single-tool turn only; input went 10303 → 20958 between turns 1 and 2, which does not say whether a turn reports a sum or its last step) |

This corrects `docs/log/40-cursor-agent-kind.md` Track D-1 ("usage is not available on the live
path"): that probe covered ACP, the JSONL transcript and `-p`, not hooks. Hooks carry usage for
the interactive TUI. They do not help Managed (ACP) sessions, and `stop` was not observed under `-p`.

### 2.4 Recommendation

**Implement, gated on verifying the token granularity** (follow-up #2286). Rules from the measurements:

- Key on `generation_id`; write a ledger row only for `status:"completed"` payloads that carry
  token fields. `aborted` / `error` are never rows, and the double `stop` of an aborted turn cannot
  double-count because neither carries tokens (still dedup by id: a retried hook delivery is possible).
- Store the raw `model` as is (`default` for Auto); leave USD empty — do not guess a price from a
  selector that is not a model. Resolve the real model from the transcript or leave it unknown.
- Hook wiring: project-level `.cursor/hooks.json` worked under `--trust`; the earlier design
  (`log/40` Track D) preferred not to own the user's global `~/.cursor/hooks.json`. The merge rule
  with a user's existing hooks is the open design point.
- Resolve the cache question (above) before computing any cost.
- **Granularity gate**: whether a multi-step turn reports the sum of all steps or only the last step is unmeasured. Before the hook feeds a per-turn ledger, compare a known multi-tool turn against its steps; until then store and show the numbers only as "hook-reported, semantics unverified", so an under-count is not shipped as per-turn usage.

## 3. Not measured (summary)

- Kiro 2.32.0 (the pin): only 2.31.0 was available; re-run §1.2 on the pin.
- Kiro `allow_always` response, multiple sessions in one recorder file, long-running recorder rotation.
- Cursor: a successful turn with an explicit model, a multi-step (several tool calls) turn's token
  accounting, whether input includes cache reads, hooks under Managed.
- Cursor hooks wired through the global `~/.cursor/hooks.json` (the project-level file was used).

## 4. Note (2026-10-17): Kiro recorder implementation on 2.31.0 and 2.32.0 (#2285)

The Terminal approval path now reads `KIRO_ACP_RECORD_PATH`
(`workspace/agent/internal/agents/kiro/recorder.go`). The pin `KIRO_VERSION=2.32.0` was
re-measured separately (#2285 comment): same recorder behaviour as 2.31.0 on every point (live
`request_permission` with a string id, `selected` `allow_once`/`reject_once`, Esc ->
`session/cancel` + `cancelled`, a killed pane leaves the request unanswered, ~121 KB at boot).
Additions to §1.4: `allow_always` opens a trust-option panel and its response carries
`optionId:"allow_always"` plus a trust option in `_meta`, still keyed by the request id; and
after a Kiro upgrade the first start shows a blank pane for >20 s with no recorder file, so a
missing file is never read as "recorder unsupported".

The parser was written from the 2.31.0 lines of §1.3 (fixtures in
`internal/agents/kiro/testdata/`). The recorder only adds to the pane scrape: an unrecognised or
absent line falls back to it. Still not measured: aarch64, the V3 engine / the fleet's
`--agent-engine v2 --trust-all-tools` flags, two simultaneous pending requests, long sessions.

## 5. Addendum (2026-10-17, #2286): the multi-step and cache questions of §2.3

Run for the gate of #2286 on the same pin (cursor-agent `2026.10.16-e3cb207`, already logged in, no
login flow), in tmux sessions of its own (`af2286_cursor_*`, `af2286_cursor2_*`, both killed by
this run), with a project-level `.cursor/hooks.json` in a throwaway directory under `$AF_WORK_DIR`
(`stop`, `beforeSubmitPrompt`, `afterAgentResponse`, `afterShellExecution`, `postToolUse`; the capture
script kept ids, status, model and token counts only). The global `~/.cursor/hooks.json` was not
touched, so a global-file hook is **still not measured**.

| Turn | Model steps | `stop` payload (`input / output / cache_read / cache_write`) |
|---|---|---|
| `Reply with just: pong` | 1 | `10312 / 35 / 0 / 0`, `status: completed` |
| five shell calls, one per tool call, then `done` | 6 (5 `postToolUse` + the final answer) | `63935 / 196 / 63296 / 0`, `status: completed`, one `stop` |

- **Multi-step turns report the SUM of all steps, not the last step.** The first step of this
  conversation costs ~10.3k input (the plain turn). Six steps re-send a growing context each time:
  `63935 ≈ 6 × 10.6k`. The last step alone would be ~11k. The one-tool turn of §2.2 fits the same
  rule (`20958 ≈ 2 × 10.4k`, which §2.3 could not explain). `output_tokens` grows with the step
  count too (35 → 196).
- **`input_tokens` includes `cache_read_tokens`.** `63296` of `63935` input is cache read; if input
  excluded it, a six-step turn would report only the ~640 fresh tokens. So the fresh part is
  `input_tokens - cache_read_tokens`. This is inferred from the magnitudes, not from a priced-model
  comparison, and `cache_write_tokens` was `0` on every turn, so whether it is part of `input_tokens`
  too is **not measured**.
- **The hook child inherits the pane's environment** (an `-e` variable set on the tmux session was
  visible in `stop`, `beforeSubmitPrompt` and `afterAgentResponse`), and its cwd is the project. A
  global hook can therefore tell an AF-launched session (`AF_SESSION_NAME`) from a hand-run one.
- Still not measured: a priced-model turn (USD), `cache_write_tokens > 0`, a global-file hook, other
  cursor versions, hooks under Managed. Follow-ups are on #2286.

# 0023. Add `kind=cursor` (Cursor CLI) as a seventh agent kind

English | [日本語](0023-cursor-agent-kind.ja.md)

- Status: **adopted (Tracks A, A2, B and C implemented)** (2026-07-23. The Track 0 probes passed →
  the read layer, the TUI, the managed driver, deployment and CP/Console are implemented;
  `go build`/`go test` plus the `AF_CURSOR_LIVE=1` real-CLI contract test plus Console
  typecheck/i18n:lint/vitest/vite build all green). Track D and running on real arm64 hardware
  remain. Track C settled that **v1 is login-only** (manual API-key registration moved to Track D —
  see the addendum to decision 5 below). The implementation plan, the measurements, the two Track A
  improvements (self-assigned UUID, state from the tail of the JSONL) and the Track A2 ACP contract
  (starting `cursor-agent acp`, building the transcript memory from session/update, measured
  set_mode/cancel) are in [docs/40](../log/40-cursor-agent-kind.md).
- See also: [0019](0019-copilot-agent-kind.md) (copilot — the most recent kind added, and the template for this),
  [0008](0008-antigravity-cli-agent-kind.md) (agy — the precedent for a Terminal-only MVP),
  [0015](0015-agent-managed-driver.md) (the managed driver abstraction).
  Note: number 0023 was taken because 0022 is in use by agent memory versioning (on the unmerged
  branch temp/s7in3bh).

## Context

Cursor CLI (`cursor-agent`/`agent`, by Anysphere) has `agent acp` (ACP = JSON-RPC over stdio, with
official documentation), `-p --output-format json|stream-json` (Claude Code-like events),
`agent create-chat` (assigning a chat ID up front), `--resume <chatId>`, hooks (`hooks.json`, with
`transcript_path` in the input) and a Claude Code-compatible JSONL transcript. The CLI flags, hook
event names and configuration format were confirmed against the real binary v2026.07.20-8cc9c0b
(docs/40 §measurements). Among the existing six kinds its integration surface is closest to
copilot's (ACP, hooks, event JSONL).

## Decision

1. **Support both Terminal (CLI) and Managed from v1** (following copilot). Cross-process resume
   over ACP, which was the pass/fail condition, **passed in the Track 0 probes** (`loadSession:true`;
   `session/load` demonstrated history replay and context preservation from a different process —
   docs/40 §probe list).
2. **The display order is settled by the user**: Claude, Codex, **Cursor**, GitHub Copilot,
   Antigravity, OpenCode (reflected in `SESSION_KINDS`, `repoLaunchKinds` and the AgentsTab card
   order; other UI derives from these).
3. **The canonical read source is the official contract surfaces only**: hooks
   (stop/beforeSubmitPrompt → the status seam) and the JSONL transcript (`transcript_path`). The
   session's actual store, `~/.cursor/chats/**/store.db` (a SQLite blob in an undocumented format),
   is **not read** — applying the lesson of the false-idle we hit when opencode's store contract
   changed. It does not depend on TUI strings either. **Changed in Track A**: hooks.json wiring is
   **not** set up in v1, and TUI state is taken purely by classifying the tail of the JSONL
   transcript (structurally avoiding the problem of keying the global `~/.cursor/hooks.json` from
   chatId to slot-sid — docs/40 §Track A measurements).
4. **The chat ID is assigned up front by AF using `agent create-chat`** and saved in the sid store
   (the same shape as copilot's `--session-id`, structurally avoiding agy's resume-ID capture
   problem). **Changed in Track A (assigning up front with `create-chat` is not adopted)**:
   measurement showed that passing an unknown but valid v4 UUID to `--resume` creates a new chat
   with that ID, so it was changed to **passing a v4 UUID that AF assigns itself to `--resume`**
   (exactly the same shape as copilot's `--session-id`, and it removes the extra exec at startup —
   docs/40 §Track A measurements).
5. **Authentication uses a dedicated flow**: `NO_OPEN_BROWSER=1 agent login` (extract the URL).
   Credentials are stored at `~/.config/cursor/auth.json` (identified by a probe) and protected by
   the `fs.go` denylist (`.config/cursor` and `.cursor`). **Track C settled that v1 is login-only**
   (the `CURSOR_API_KEY` manual registration originally planned alongside moved to Track D),
   because: (1) the cursor CLI has no command to persist an API key (only ambient use of
   `CURSOR_API_KEY`/`--api-key`); (2) making use of it would require injecting env into every exec,
   but **the TUI (a tmux pane) has no safe injection seam, and embedding it in the Program string
   exposes the key in `ps`** (which violates the ban on plaintext credentials); and (3) the login
   flow (ambient auth.json) covers TUI, managed, status and models with zero env injection.
   **Login is start→poll rather than paste-a-code** (the CLI polls for the browser approval itself —
   the codex device-auth shape, unlike claude's and agy's pasted code).
6. **The model catalogue is fetched live from `agent models` and follows the account** (there is an
   official command, so TUI scraping as with copilot should not be needed).
7. **No usage display in the WS bar in v1** (neither the plan allowance nor the session's token
   usage):
   - The plan-allowance chip: there is no official API/CLI, and an unofficial API would be as
     fragile as in the usage-chip 429 incident.
   - Session token counts (the ContextBar equivalent other agents have): **a probe of the real
     binary on 2026-07-23 confirmed that no token/usage information rides the live paths at all**
     (managed = ACP, TUI = JSONL), so it is not adopted. Over ACP, the `session/prompt` response
     carries only `stopReason` and `session/update` has no usage variant. The JSONL claims to be
     "Claude Code compatible" but has no `message.usage`. Tokens appear only in the terminal
     `result.usage` of `-p --output-format json|stream-json`, which is the one-shot batch path (for
     headless assistant chat) and is not used by a live session. Implementing it would require
     waiting for upstream to put usage into ACP, or switching managed to be driven by `-p` and
     therefore **abandoning decision 1 (cross-process resume via ACP `session/load`)** — not worth
     it. Details in
     [docs/40 §the feasibility probe for a usage display](../log/40-cursor-agent-kind.md).
   - An incidental measurement: **the Free plan cannot use named models**
     (`Named models unavailable. Free plans can only use Auto.`) and is limited to
     Auto/composer-2.5. If the server-side default swings to a named model when none is selected at
     startup, it can hit the free wall — so explicitly prefixing Auto when nothing is selected is
     recorded as a hardening candidate for Track D.
8. **rtk uses the hooks seam** (a new `rtk hook cursor` wired to `beforeShellExecution`): the CLI
   goes through `api2.cursor.sh` rather than connecting to the provider directly, so the base URL
   cannot be swapped. Whether the command can be rewritten will be probed; if not, it falls back to
   instruction-based (on a par with codex and agy).
9. **Cursor's own features that overlap the fleet's responsibilities are not used**: `-w/--worktree`
   (the Console's worktree is canonical for isolation), `agent worker` (orchestration on Cursor's
   cloud) and sending sessions to the cloud (the `&` prefix).
10. **Deployment bakes in a versioned URL** (`downloads.cursor.com/lab/<version>/...` — an
    undocumented URL scheme, so verifying the version pin in the e2e smoke test is mandatory; the
    upstream checksum is not published, so the sha256 is computed ourselves and pinned), **and
    suppressing auto-update is settled on the two official routes** (from re-analysing the bundle in
    Track B): the background update gate is
    `disableAutoUpdate || channel==="static"`, so we use the `--disable-auto-update` root flag
    (prefixed on every path) plus `cli-config.json` with `channel:"static"` (re-pinned by the
    entrypoint). The AUR fallback of making the versions file unwritable is no longer needed. Version
    numbers are date-formatted, not semver. It is baked into root-owned
    `/usr/local/share/cursor-agent/versions/<version>/` with a `/usr/local/bin/cursor-agent` symlink
    (measured: even read-only, the `.running` marker degrades gracefully and it still works).

## Risks (accepted)

- CLI drift from weekly releases (mitigated by depending on the official hooks/JSONL/ACP contracts,
  with the live test as the primary detector).
- The version-pinned URL is an undocumented scheme (breakage is detected by the e2e smoke test).
  Disabling auto-update is updated to the **official means** (`--disable-auto-update` plus
  `channel:"static"` — settled in Track B).
- linux arm64: the distributed assets are verified sound (the bundled node and native addon are
  AArch64/glibc), but starting it on real arm64 hardware is unverified (this container is x64;
  hardware verification is a precondition before ECS/native rollout).
- Forum reports of `CURSOR_API_KEY` misbehaving inside Docker (to be verified in our own
  container).

## Results

- Tracks A/A2/B/C are implemented (2026-07-23). Track C covers login start/poll/disconnect in
  auth.go, dual registration in both routes.go files, a sweep of the kind enums in mcp_stdio.go and
  the CP's mcp.go, the bridge kindLabel, and the Console (the union and SESSION_KINDS, an explicit
  full set of caps in the registry descriptor, a cursor twin in tokens.css and the five colour
  classes, the CursorCard in AgentsTab, settings.ts and agentModels.ts, and i18n ja/en). All tests
  green (cursor 12, agent+bridge 335, CP 222; Console typecheck, i18n:lint, vitest 392, vite build).
- Remaining: Track D (manual API-key registration, the usage chip, the rtk hook seam, headless chat,
  image attachments, and so on), starting on real arm64 hardware, and looking at real hardware after
  the fleet is rebuilt. The details, the track split, the table of lessons applied and the probe list
  are in [docs/40](../log/40-cursor-agent-kind.md).

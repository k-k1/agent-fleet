# 0019. Add `kind=copilot` (GitHub Copilot CLI) as a fifth agent kind

English | [日本語](0019-copilot-agent-kind.ja.md)

- Status: **adopted and implemented** (2026-07-21). The implementation plan and all measurements are in [docs/36](../log/36-copilot-agent-kind.md).
- See also: [0008](0008-antigravity-cli-agent-kind.md) (agy — the precedent for adding a kind),
  [0015](0015-agent-managed-driver.md) (the managed driver abstraction — this is its third implementation).

## Context

GitHub Copilot CLI (npm `@github/copilot`, GA 2026-02) has `-p --output-format json` (JSONL
events), `--acp` (Agent Client Protocol = JSON-RPC over stdio), externally assigned
`--session-id`, and cross-process resume via `session/load`. It has better orchestrator-oriented
interfaces than any of the existing four kinds (all measured against the real binary, v1.0.73 —
see the measurements in docs/36). Authentication uses a GitHub token (the gh CLI app's OAuth is
officially supported), and we verified by measurement that this fleet's transparent gh
authentication passes straight through.

## Decision

1. **Support both Terminal (CLI) and Managed from v1** (unlike agy, structured output was there
   from the start). Managed is the default.
2. **The managed runtime is a per-session child running `copilot --acp`** (stdio JSON-RPC) — a
   third shape, different from codex (a shared daemon over WS) and opencode (a shared serve over
   HTTP+SSE). The reason: ACP has no per-session model selection (configOptions covers only
   mode/allow_all), so the `--model`/`--effort` flags on each child process are the only reliable
   route. A side effect is that exit/OOM recording becomes exactly per-session, from the child's
   `cmd.Wait()`. Memory is the same as a TUI pane (both are one copilot process per session).
3. **The canonical read source is
   `$COPILOT_HOME/session-state/<sid>/events.jsonl`** (measured to be the same format, appended
   live, across all of TUI / -p / ACP). The transcript and the live state classification
   (working/question/idle) share one implementation across both drivers and do not depend on TUI
   strings (the false-idle lesson).
4. **The session UUID is assigned externally by AF** (`--session-id`, RFC4122 v4). The struggle
   to capture a resume ID that we had with agy (0008 / docs/32 202e439) structurally cannot
   happen.
5. **Authentication rides along with the GitHub integration**: no dedicated Connections flow.
   `copilot.connected` is derived from the GitHub git-provider connection, so **GitHub comes
   first**. The TUI uses copilot's own gh fallback (ambient; measured to work), while the managed
   child and the model probe get an explicit
   `COPILOT_GITHUB_TOKEN="$(gh auth token)"` injection (we measured ambient breaking in an
   isolated environment — explicit injection is correct). Whether the user has a Copilot
   subscription is not checked at connection time; it surfaces as a CLI error on the first turn.
6. **The model catalogue is fetched live and follows the plan**: the CLI/ACP has no enumeration
   endpoint, and availability depends on the plan (Copilot Free has Auto only — measured). We
   start the TUI on a PTY under a throwaway COPILOT_HOME and scrape the `/model` picker
   (agents.Flow, a 10-minute cache, stale-if-error). A Free-tier banner → empty means the picker
   offers only the default (auto).
7. **Permissions are implemented defensively**: the fleet default is `--allow-all` (dropped only
   when starting in plan mode), but `session/request_permission` is always mapped to an
   Interaction(question) and answered structurally through /respond (do not trust "it cannot
   happen because it does not appear in the UI" — the agy df996e4 lesson).
8. **Syncing sessions to GitHub and remote steering are off by default**
   (`--no-remote --no-remote-export`) — to prevent conversations leaking out of the fleet and
   double steering.
9. Steer is a queue inside the driver (ACP has no mid-turn injection), there is no Fork, and only
   DynamicMode is dynamic (`session/set_mode`). ask_user degrades to plain text plus end_turn
   over ACP (measured), so the question card is for permissions.

## Results

- The implementation is complete, following the track split in docs/36 (the agent proper, the
  driver, deployment, CP/Console). A table mapping all 46 commits and the 23 categories of review
  comments from the agy integration is recorded in docs/36.
- Verification: Go 451 / CP 150 / console 355 all green, plus **a real CLI contract test**
  (AF_COPILOT_LIVE=1: spawn → session/new → a turn to completion → events.jsonl → kill the child →
  resume via session/load → context preserved, including the model probe) passing. What remains
  is looking at real hardware after the fleet is rebuilt, and Track D as listed in docs/36 (rtk,
  the usage chip, the headless chat backend, image attachments, and so on).
- Constraints accepted: CLI drift from weekly releases (mitigated by depending on events.jsonl
  and ACP, with the live test as the primary detector); no support for classic PATs (`ghp_`)
  (normally not applicable, since the fleet's GitHub OAuth produces `gho_`); and no plan-mode chip
  in the TUI (the footer shows no mode, so it cannot be detected).

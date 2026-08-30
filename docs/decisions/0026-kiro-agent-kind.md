# 0026. Add `kind=kiro` (Kiro) as an eighth agent kind

English | [日本語](0026-kiro-agent-kind.ja.md)

- Status: **adopted (Tracks A, A2, B, C and D implemented)** (2026-07-24. The Track 0 probes
  passed → the read layer, the TUI, the managed driver, deployment, CP/Console and the live usage
  wiring are implemented. `go build`/`go vet`/`gofmt`/`go test` plus the `KIRO_LIVE=1` real
  `kiro-cli` contract test, and Console typecheck / i18n:lint / vitest / vite build, all green.
  Tracks A/B/C/A2 are merged to develop including every P1 review fix [merge de2fb25b]; Track D is
  on temp/kiro-track-d.) What remains is looking at real hardware after the fleet is rebuilt.
- See also: [0023](0023-cursor-agent-kind.md) (cursor — the most recent kind added, and the template for this),
  [0019](0019-copilot-agent-kind.md) (copilot — the previous owner of purple),
  [0008](0008-antigravity-cli-agent-kind.md) (agy — the precedent for a Terminal-only MVP and for ContextReporter),
  [0015](0015-agent-managed-driver.md) (the managed driver abstraction).
  The implementation plan, the Track 0 measurements and the implementation notes for each track
  (the read contract, the ACP contract, deployment, colour, live usage) are in
  [docs/43](../log/43-kiro-agent-kind.md).
  Note: numbers 0022 (agent memory versioning, on an unmerged branch temp/s7in3bh) and 0025
  (native auto-update) were taken, so this is 0026.

## Context

Kiro (`kiro-cli`, formerly the Amazon Q Developer CLI, renamed 2025-11-17; the terminal edition of
the AWS Kiro IDE) has `kiro-cli acp` (ACP = JSON-RPC over stdio), `chat` (a TUI),
`--list-models -f json`, `--resume-id` and a v2 JSONL session store
(`~/.kiro/sessions/cli/<sid>.jsonl`, shared by the TUI and ACP). The real binary 2.14.1 was
installed in this workspace (Debian 12 / x86_64 / glibc 2.36) and every probe was run, including a
Builder ID (free) device-flow login (docs/43 §2). Among the existing seven kinds its integration
surface is closest to cursor's and copilot's (a per-session-child ACP driver).

## Decision

1. **Colour = purple (Kiro inherits purple from copilot); three kinds change at once** (user
   decision, 2026-07-24). The final values, settled by actually rendering swatches in headless
   chromium in both themes: **kiro = dark #a371f7 / light #8250df** (copilot's old values),
   **copilot = a neutral charcoal, dark #7d8590 / light #30363d**, and **opencode = a pale slate
   grey, dark #aab4be / light #6e7781**. The candidate values in the proposal (copilot dark
   #6b7075 / light #24292f, opencode light #9aa4ae) were charcoal on a dark background and a pale
   grey on white, i.e. low contrast, so they were pulled towards legible values while preserving
   the hierarchy (copilot darker, opencode lighter). The colour-class twins sweep all three kinds
   through every file in `kind-color-css-checklist` (tokens.css dark/light plus
   app/terminal/sessions/settings/ui.css). **The icon is `compass` (codicon) and the display
   position is after copilot** (confirmed with the user).

2. **Deployment: installed on demand by default, only for users who use it; it may be baked in
   under `BAKE_AGENT_CLIS=1`** (user decision, 2026-07-24). At ~855MB unpacked (including
   `kiro-cli-chat` at 663M) it is an order of magnitude larger than any other kind, so it does
   **not** join the uniform lean boot-install loop. Instead there is a new pattern
   (`workspace-agent install-kiro`) that **installs into the user's own `~/.local`, pinned by a
   manifest sha256, on their first launch (or from an install button on the connection card)**. On
   arm64/Debian 12 the **musl variant** is required, to avoid the glibc 2.39 requirement.
   Auto-update is re-pinned on every entrypoint start via `app.disableAutoupdates`. The UI states
   plainly that 855MB lands on the home volume.

3. **headlessChat = not needed (settled as out of scope for v1)**. kiro is not added to
   `ASSISTANT_AGENT_KINDS` or `defaultHeadlessOrder`. Headless `--no-interactive` emits no JSON
   (issues #5423/#9066) so ACP would be the proper route, but the demand for assistant chat is met
   by the existing backends. **AI title suggestions still work with the current machinery**
   (`oneShotHeadless` in `session_title.go` reads the generic read layer, i.e. the transcript
   implementation from Track A). Reconsidered in Track D; unchanged.

4. **ToS = documented as a caveat** (whether Builder ID free may be used for work, and consistency
   with organisational policy, are for the adopting organisation to confirm). **Development and
   verification proceed on Free (Builder ID).**

5. **The session ID is assigned by the CLI, and falling back to `session/new` is allowed only when
   that store (`<sid>.json`) has actually disappeared** (settled in review, A2-1). kiro assigns
   session IDs on the CLI side and does not accept a self-assigned `--resume-id` (measured). A
   resume's `session/load` can return "active in another process" because of cross-process
   exclusion by a `.lock` (containing a pid), but **mistaking that lock-busy for "the conversation
   is gone" and creating a new one with `session/new` silently severs a live conversation.**
   Whether to create a new one is therefore judged from the decisive fact of **the store's
   existence on disk**, never from the presence or drift of a lock error message (`isLockBusy` is
   tightened to -32603 AND the message, and used only to decide RETRY). For a corrupted store,
   making resume a permanent error is the right thing for conversation preservation (a deliberate
   fail-safe).

6. **Discovering the sid is fenced by the slot's creation time** (settled in review, A-1). kiro
   creates `~/.kiro/sessions/cli/<sid>.json` (recording the cwd) after it starts, so AF discovers
   it by cwd plus modification time and caches it in the sidstore. But **recreate cuts a new slug in
   the same directory**, so in the window of a fresh start there is a risk of **wrongly grabbing the
   predecessor session** that is still sitting in the same cwd. So discovery accepts **only stores
   created at or after that slot's CreatedAt** (`Meta.CreatedAt`, fixed at creation time and stable
   across resumes). On the managed path, `threadHandle.createdAt` is carried through to the spawn's
   discovery so the same fence applies. When CreatedAt cannot be interpreted, there is no fence
   (a regression is a lighter cost than freezing because nothing can be discovered).

7. **Live usage rides `_kiro.dev/metadata` into the existing UI, converting % into tokens
   (Track D)**. cursor carried no usage on its live paths at all and was not adopted (ADR 0023
   decision 7), but **kiro's managed (ACP) `_kiro.dev/metadata` notification carries
   `contextUsagePercentage` (0–100, the latest value) plus `meteringUsage` (credits, cumulative)
   every turn** (measured). The transcript has no token counts, so **the percentage is converted to
   a token count against the model's real context window** (`context_window_tokens` from
   `--list-models`), and passing the window explicitly puts it straight into the existing
   token-based ContextBar and `get_session_usage` (the percentage round-trips exactly). It is wired
   to the mirror through the same `agents.ContextReporter` (`ContextFill`) seam as agy, so **the
   front end is unmodified** (managed is paneless, and the mirror is the only view). Credits come
   back in `get_session_usage.cumulative.credits`. **The plan-allowance chip (scraping /usage from
   the PTY → get_agent_usage) is deferred in this track** (there is no machine-readable means —
   issue #7752 — and the fragility of an unofficial API or scraping is the same kind as the
   usage-chip 429 incident; file it separately when needed). **API-key authentication is also
   deferred and login-only continues** (injecting env into the TUI exposes it in `ps` — the same
   reason as ADR 0023 decision 5).

## Risks (accepted)

- CLI drift from weekly releases. Managed depends on the official ACP contract (the
  `session/update` discriminator, `session/load` replay, releasing the `.lock`, `stopReason`) with
  the `KIRO_LIVE` contract test as the primary detector. The TUI uses explicit text contracts
  ("Kiro is working" / "ask a question or describe a task" / "requires approval") and does not use
  a spinner regex, because 2.14.1 has no Stop hook (measured). If the metadata field names or
  scales change, Track D's contract test fails.
- On-demand installation of 855MB unpacked. Against interruption (killing the pane while waiting
  for the first launch) it self-heals via staging → atomic rename, a presence marker (kiro-cli
  installed last), a `--version` sanity check and flock exclusion. Watching a real 855MB download
  end to end remains, after the fleet is rebuilt.
- linux arm64: the musl variant's assets are verified sound, but starting on real arm64 hardware is
  unverified (this container is x64).
- Generational differences in the v2 JSONL store (v1 SQLite / v3 JSONL). `--agent-engine v2` is
  pinned explicitly as insurance against the read/state contract breaking when v3 becomes the
  default.
- Live usage is **only for a running managed session** (in memory; hidden when stopped, on the TUI,
  or before anything is received). The token count is approximated from the percentage (the
  percentage itself is exact).

## Results

- Tracks A/A2/B/C implemented (2026-07-24) = the read layer, TUI state and the v2 JSONL
  transcript; the managed ACP driver; on-demand deployment plus the bake-in knob; CP and Console
  wiring plus changing all three colours at once. Merged to develop including all nine P1 review
  fixes (de2fb25b).
- Track D implemented (2026-07-24, temp/kiro-track-d) = the live context % and credits from
  `_kiro.dev/metadata`, converted from % to tokens and wired into the ContextBar (in the mirror,
  via ContextReporter) and `get_session_usage` (context plus cumulative.credits). headlessChat, the
  API key and the plan-allowance chip are deferred as per decision 7.
- Remaining: looking at real hardware after the fleet is rebuilt (colour rendering, the on-demand
  855MB install, the device-flow login, the pct progression in the mirror's ContextBar) and starting
  on real arm64 hardware. The details, the track split and the probe list are in
  [docs/43](../log/43-kiro-agent-kind.md).

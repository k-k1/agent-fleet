# 0008. Take in the Antigravity CLI (`agy`) as a fourth agent kind

English | [日本語](0008-antigravity-cli-agent-kind.ja.md)

- Status: **adopted** (2026-07-20. Implementation started on the Starter/experimental track, aiming at everyday use over the GCP route. The implementation plan is [32](../log/32-agy-agent-kind.md))
- See also: [session.go](../../workspace/agent/internal/sessionx/session.go) (session integration) / [Codex auth](../../workspace/agent/internal/agents/codex/auth.go) (the current device-auth implementation) / [0006-mcp-unified](0006-mcp-unified.md) / [HANDOFF §agent kinds](../HANDOFF.md)
- Origin: a user request — "look into whether the antigravity cli can be built into Agent-Fleet" (investigated 2026-06-29/30)

## Context

Agent-Fleet already switches between several agent kinds —
**`claude | opencode | codex | shell`** — off a single `kind` (the `switch` in
`startSessionTmux` in `session.go`, a `*_auth.go` per kind, inclusion in the image, the session
kind selector in the Console). The question was whether Google's **Antigravity CLI** (binary
name `agy`) could be added as `kind=agy`.

`agy` is a **terminal agent written in Go** that Google shipped in 2026-05 (the successor to the
old Gemini CLI, which was retired on 2026-06-18). Its TUI explicitly assumes SSH/keyboard
driving, which makes it **structurally the same shape as claude/codex: an interactive agent that
runs on a PTY**. So the existing PTY→tmux→xterm bridge can be reused as is.

- Models: mainly the Gemini family, but **Claude and OSS backends can optionally be selected**.
- Authentication: the system keyring, falling back to Google Sign-In. **When it detects an SSH
  session it shows an authorisation URL to be completed locally** (the same shape as codex's
  device-auth and claude's sign-in URL button). For CI/headless there is `ANTIGRAVITY_TOKEN`.
  Plain API-key authentication has been requested ([Issue #78]).
- Configuration idiom: `AGENTS.md` at the root (prepended to every prompt) and
  `.agents/skills/*.md` (slash commands). → **The existing `WS_NOTES`→`AGENTS.md` seeding in the
  entrypoint applies unchanged.**
- It supports MCP (consistent with the unified MCP direction in [0006](0006-mcp-unified.md)).

## The ToS judgement (the biggest gate)

Having worked through the Anthropic ToS carefully in [0001](0001-self-host-vs-saas.md), we did
the equivalent here. **Conclusion: there is a route that is actually cleaner than claude's.**

Authentication is **the same Google Sign-In at every tier** (device-auth / authorisation URL),
and `agy`'s code **does not depend on the login tier**. So choosing a tier is **an
operational/ToS policy question, not an implementation difference**.

| Route (the BYO login tier) | Used for training | Quota | Fit with self-hosting | Verdict |
|---|---|---|---|---|
| **Company Workspace (Gemini for Business / AI Ultra for Business)** | **not collected** (stated explicitly) | enterprise allowance | company-owned seats — matches the direction in [overview](../HANDOFF.md) | ✅ **recommended** |
| **A GCP project** | **not used** (nothing is stored outside the private environment) | consumption billing | each user brings their own GCP credentials → **GCP ToS** | ✅ **recommended** |
| Personal **AI Pro ($20) / Ultra ($249.99)** | **trained on by default** (opt out by turning off "Gemini Apps Activity") | Pro refreshes every 5h, but there are reports of **a 5h lock after 2h** given `agy`'s heavy compute effort | technically BYO-able, but thin | ⚠ **personal evaluation only** (the same shape as "avoid personal Pro/Max" for claude) |
| Consumer / free | trained on (as above) | **20 req/day/account** (shared across desktop/CLI/SDK) | unfit for production | ⚠ smoke testing only |
| Claude models via `agy` | — | — | additionally bound by **Anthropic's commercial terms** | take care when combining |

**The company Workspace and GCP project routes line up directly with Agent-Fleet's "1 company =
1 deployment, self-hosted, BYO"** ([overview](../HANDOFF.md)) and do not step into the ToS grey
area that killed SaaS. Personal AI Pro passes the same device-auth technically, but on two
counts — **training use (opt-out only) and quota exhaustion** — it is avoided for company use
exactly as claude's personal plans are.
→ **Gate passed. The company Workspace / GCP routes are the recommended premise; the
implementation itself is tier-agnostic.**

## Grounding it in the existing patterns (what gets touched)

The same rut as adding codex/opencode. The footprint is small:

1. **Include it in the image** — `workspace/Dockerfile:86` currently says
   `npm install -g … @openai/codex …`. `agy` is not npm but a **Go binary** from
   `curl -fsSL https://antigravity.google/cli/install.sh | bash`, so add one install line of the
   same shape as claude's (`claude.ai/install.sh`) and add `&& agy --version` to the
   verification line. **This is the only structural difference.**
2. **Launch branch** — add `case "agy":` to the `switch m.Kind` at `session.go:210`. Add a
   `buildAgyProgram`, modelled on `buildCodexProgram`, that starts `agy` in the working
   directory (with resume/model flags where needed). Add `"agy"` to the allowlist at
   `session.go:431`.
3. **Authentication, `agy_auth.go`** — **reuse the device-auth/PTY-scraping machinery from
   `codex_auth.go` almost verbatim**. Catch `agy`'s SSH authorisation URL with `claudeFlow`,
   surface it in the Console, and detect completion by polling. For the state display, use
   `agy`'s login-status query (the equivalent of codex's `login status` needs confirming).
   Credentials are held by `agy` itself in the keyring/home — **outside the encrypted store,
   like claude and codex, so a denylist entry is needed**.
4. **CP routes** — just add `/api/connections/agy/...` (device start/poll, disconnect) via
   `proxyAgentREST`, in the same shape as codex in `control-plane/main.go`.
5. **Console** — add `agy` to the session kind selector and one authentication panel in the
   Connections tab. If the backend API matches codex's shape, the UI is a copy.
6. **AGENTS.md seeding** — include `agy`'s reference path in the entrypoint's
   `WS_NOTES`→`AGENTS.md` copy (it already reads `AGENTS.md` at the project root). The same goes
   for appending the rtk block.

## PoC results (2026-06-30, in a throwaway `agent-fleet/workspace:dev` container)

Done in a throwaway container from the existing image rather than by building one (to avoid
[the host OOM risk](../HANDOFF.md)).

- ✅ **Install works**: `curl -fsSL https://antigravity.google/cli/install.sh | bash` is
  **non-interactive, idempotent and sha512-verified**, installing to `$HOME/.local/bin/agy` (it
  fetches a Cloud Run manifest → a flat native build). `--dir` selects the destination; it skips
  when already present. Fine on Debian 12 / curl / x86_64.
- ❌ **Will not start (on this development host only)**: `agy` hits `CRNGT failed` → SIGABRT
  immediately at startup. The stack is
  `crypto/internal/boring._goboringcrypto_RAND_bytes`. **agy is a Go BoringCrypto (FIPS) build**,
  and the x86 FIPS random module **requires the RDRAND instruction**. This host (AMD Ryzen
  Embedded R2514, bare metal, `detect-virt: none`) **does not advertise rdrand in
  `/proc/cpuinfo`** (suspected kernel mask or BIOS disable) → the self-test aborts.
  `seccomp=unconfined` makes no difference, and because it is a prebuilt binary there is no
  switch to disable FIPS — **it cannot be worked around from user space**.

→ **A new deployment requirement: a host running agy must have RDRAND enabled** (most cloud VMs
and current CPUs do; this development host does not). This is not a defect specific to
agy or Agent-Fleet; it is a property of FIPS builds. **On this development host we cannot reach
hands-on verification of interaction, authentication or resume**, so the following was re-run on
a host with RDRAND.

## Second PoC (2026-07-20, on an RDRAND-enabled host = a workspace container on WSL2 / Ryzen 7 PRO 8840HS)

The follow-up action "re-run the PoC on an RDRAND-enabled host" was carried out. On a host whose
`/proc/cpuinfo` advertises `rdrand`/`rdseed`, **everything from startup through authentication to
non-interactive execution ran to completion** (v1.1.4).

- ✅ **Starts**: `install.sh` → `~/.local/bin/agy`, `agy --version` fine. `CRNGT failed` did not
  reproduce. **The RDRAND requirement is confirmed from both sides** (no problem on an enabled
  host; the deployment requirement in 0008 is sound).
- ✅ **The in-container authentication flow completes** (in an environment with no keyring):
  starting the TUI offers "1. Google OAuth / 2. Use a Google Cloud project"; choosing OAuth
  **prints an authorisation URL and an authorisation-code paste prompt in the terminal** (PKCE,
  `redirect_uri=antigravity.google/oauth-callback`). Feeding the code in with tmux `send-keys`
  completes it = **Console integration works with the same PTY scraping as codex's device-auth**.
  The GCP project route exists as option 2 of the same selector.
- **First-run onboarding** (the sequence of screens `agy_auth.go` has to scrape through): colour
  scheme selection → ToS + **an opt-in for Interactions data collection (on by default, can be
  toggled off in the TUI**; turned off for this run) → the workspace trust prompt (Yes/No) → the
  main screen.
- **Where credentials persist**: with no keyring, in
  **`~/.gemini/antigravity-cli/antigravity-oauth-token` (plaintext, under home)** → like
  claude/codex this is **outside the encrypted store, so a denylist entry is required**.
- **Querying login state**: there is no dedicated subcommand. When unauthenticated, `agy models`
  returns a "Please sign in" error, so **that serves as the `login status` equivalent**.
- ✅ **Non-interactive execution**: `agy -p "<prompt>"` replies normally (default Gemini 3.5
  Flash (Medium)).
- **The model list** (a personal Google account on Starter Quota): Gemini 3.5 Flash
  (Low/Medium/High), Gemini 3.1 Pro (Low/High), **Claude Sonnet 4.6 / Opus 4.6 (Thinking)**,
  GPT-OSS 120B.

## Starter Quota measurements and the adoption call (added 2026-07-20)

We looked at whether it can be adopted as a fourth kind while staying on a personal Google
account (display name **Antigravity Starter Quota** = the free consumer tier). Measurements from
the TUI's `/usage` showed that **the quota regime has changed since this ADR's first edition was
researched** (20 req/day, 5h refresh).

- **It is now weekly and per model group**: two pools — "the Gemini family (Flash/Pro shared)"
  and "the Claude/GPT family (Opus 4.6 / Sonnet 4.6 / GPT-OSS 120B shared)" — **each with a
  weekly cap**, consumed **in proportion to token cost** (per the wording in `/usage`). Measured:
  one tiny `-p` call consumed about 1% of the Gemini pool → **the Starter weekly pool is worth
  roughly 100 tiny prompts**. A real agent task (with repository context) should be expected to
  consume several percent each.
- **The quota is shared with the same account's other Google agent surfaces** (Antigravity IDE,
  Jules, Code Assist, etc. — a unified wallet, consistent with the reported 2026-04 quota
  changes). It is not a CLI-only allowance.
- **`/usage` can be scraped from the PTY** → the remaining percentage can be shown in the
  Console's Connections panel.
- Operational findings: **`/logout` exists** (which settles how to log out). **The resume unit is
  a conversation UUID.** `--continue` takes the last conversation for the cwd
  (`cache/last_conversations.json` is **a cwd→conversation-ID map**), `--conversation <ID>`
  resumes explicitly, and the list comes from `conversation_summaries.db` (SQLite, readable in
  plaintext) or the TUI's `/resume`. **Mapping to a slot sid works either way: "automatic via
  `--continue` if each slot has its own working directory", or "store the ID on the CP side".**
- **There is no structured output in this version**: v1.1.4's flags have no `--output-format`
  (the first edition's note appears to describe an older version or an unimplemented feature).
  `-p` produces plain text only.
  → **Terminal (CLI) (tmux/PTY) is the only possible execution method.** A Managed method driven
  by an event stream, as with codex/opencode, cannot be built today. It sits alongside claude.

**Added 2026-07-20 (re-evaluating the three paid personal plans)**: of the grounds for the first
edition's blanket "personal AI Pro is evaluation only", **the quota half can be resolved on
current plans**. Today there is AI Plus $4.99 / AI Pro $19.99 / AI Ultra $100 (5× Pro) / AI Ultra
$200 (20×), and the paid plans are compute-based with **a 5-hour refresh plus a weekly cap**;
Pro and above **can top up credits**, so exhaustion does not immediately halt operations. **The
ToS side, however, stands as first judged**: every consumer plan is limited to personal accounts
(they cannot join a Workspace), and exclusion from training remains opt-out even when paid.
→ **Everyday use on a personal deployment can work on AI Pro (to be measured). Everyday use on a
company deployment remains the company Workspace / GCP project routes only.** The detailed
comparison table is in [32 §comparison of AI subscription routes](../log/32-agy-agent-kind.md).

**Confirmed by measurement the same day**: on real AI Pro hardware, the same real task consumed
6.01% of the weekly pool on Starter versus **0.22% on Pro (a pool ≈27× larger, ≈455 real tasks
per week)**; the Claude family came to ≈81 real tasks per week; and `/usage` gains a 5h-window
bar. **Everyday use on a personal deployment works on AI Pro (measured and settled).** The
figures are in [32 §Track D-4 measurements](../log/32-agy-agent-kind.md). The verdict for company
deployments is unchanged.

**Verdict: `kind=agy` is worth adopting, but on Starter Quota it will not be an everyday driver
alongside claude/codex/opencode.**

| Aspect | Assessment |
|---|---|
| Technical integration (auth scraping, tmux, resume, AGENTS.md, MCP) | ✅ all of it works (Terminal method only) |
| Starter volume | ❌ a tiny weekly pool, shared with the IDE and Jules. Everyday use exhausts it in a few tasks |
| Starter ToS | ⚠ personal evaluation only, as first judged (unfit for company use even with training opted out) |
| Where it sits | **A supporting slot**: because the Claude/GPT pool is separate, it suits "a Gemini/Claude second opinion a few times a week", and verifying the integration |

→ **A personal deployment (the WSL quick-start line) can adopt it on Starter as an
"experimental, supporting agent".** Everyday adoption on a company deployment presupposes **the
Workspace / GCP routes** (the first edition's judgement stands).

## M1 integration results (added 2026-07-20)

Tracks A/B/C were merged and **M1's exit criteria (driving the Console contract's API on real
hardware: create, converse, resume, logout, the authentication flow, the four `/usage` bars, and
no RDRAND exposure) passed on real hardware** — details in
[32 §integration and the M1 E2E results](../log/32-agy-agent-kind.md). E2E found and fixed one
integration bug: **the v1.1.4 TUI only flushes the resume unit (the cwd→conversation map) on a
graceful exit** (the Track D observation that "it writes on the first prompt" was an artefact of
`-p` exiting the process immediately). The fix is a dead-side capture in WireLive plus
`agents.GracefulStopper` on halt (send `/exit`, wait, then kill). This finding overrides the body
text above as the operating condition for "the resume unit is a conversation UUID".

## Open questions

- **Per-user login for the GCP project route** (whether `gcloud` integration is needed, and the
  shape of credentials passed by env). Option 2 of the TUI selector has not been exercised.
- ~~Whether the image installs it as root or into home~~ → **settled on root install in Track B**
  (`--dir /usr/local/bin` plus `AGY_CLI_DISABLE_AUTO_UPDATE=true` to suppress self-update.
  ⚠️ Only the value `true` works — `1` is ignored. docs/32 §(self-update) / docs/70 §70.14.9).
- Whether a Managed execution method is possible waits on `agy` shipping structured output (an
  equivalent of `--output-format`).
- L2 E2E including the CP and a browser (`e2e/`) has to happen separately on a host with docker
  (this container has none).

## Decision (proposed)

**Add `kind=agy` as a fourth agent kind.** The ToS gate is passed via the GCP project route. The
implementation rides the rut of adding codex (a launch branch, an `agy_auth.go` reusing
device-auth, a CP proxy, a Console panel), and the only structural difference is that the image
brings it in with `install.sh` rather than npm. **The PoC confirmed installation, but agy's FIPS
build requires RDRAND and will not start on this development host** (above). The deployment
documentation must state the RDRAND requirement. **Added 2026-07-20: the second PoC on an
RDRAND-enabled host is complete** (see "Second PoC" — startup, OAuth authentication and
non-interactive `-p` all verified on real hardware). **Next action = implement in stages while
closing the remaining open questions (the GCP route, logout, the resume unit).**

[Issue #78]: https://github.com/google-antigravity/antigravity-cli/issues/78

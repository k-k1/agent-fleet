# 0002. Claude authentication — auth and onboarding are two different things

English | [日本語](0002-claude-auth-onboarding.ja.md)

- Status: decided (the end of a chain of corrections, rounds 10 → 11 → 12)
- See also: [HANDOFF §6.10.3](../HANDOFF.md) / [build/08 §8.5 Claude authentication and onboarding](../build/08-integrations.md) (formerly architecture §2.6) / [history/phase0-poc](../log/phase0-poc.md)

## Context

Each user's Claude has to run as that user inside a headless container (BYO `/login`). Phase 0
established that `/login` itself **does not depend on a localhost callback**
(`redirect_uri=platform.claude.com/oauth/code/callback`), which removed the biggest risk. But
while wiring it up from the Console we kept falling into the same hole: "it is supposed to be
authenticated, and yet the login screen appears."

## The chain of corrections (compressed, because the lesson is the point)

1. **Wrong**: inject a `setup-token` through the env var `CLAUDE_CODE_OAUTH_TOKEN` — that is
   only read by `claude -p` (headless); the interactive TUI ignores it.
2. **Wrong**: synthesise a `.credentials.json` with an empty refreshToken — headless accepts
   it, but the interactive TUI cannot refresh and rejects it.
3. `ANTHROPIC_AUTH_TOKEN` can authenticate the interactive session, but it is treated as "API
   Usage Billing" and risks killing subscription features (RC and friends) → not adopted.
4. `tmux new-session -e VAR=val` only populates the session environment; it does not propagate
   to the processes in a pane (pass env by prefixing the command instead).

## Decision

- **Authentication proper** is `claude auth login --claudeai` (the real subscription OAuth).
  claude itself writes a `.credentials.json` with a refreshToken, so the interactive TUI is
  authenticated and RC and the rest keep working. The URL is scraped by driving the PTY, shown
  in the Console, and the code is pasted back.
- **The real cause of the login screen is not the credentials — it is `hasCompletedOnboarding`
  being unset in `.claude.json`.** Even when `auth status` reports `loggedIn:true`, the
  interactive TUI re-runs the onboarding wizard, whose first step is choosing a login method.
  → On every session start, seed `.claude.json` with `hasCompletedOnboarding=true` and
  `projects[dir].hasTrustDialogAccepted=true`. `--dangerously-skip-permissions` skips neither
  trust nor onboarding, so seeding them explicitly is mandatory.
- With `CLAUDE_CONFIG_DIR` set, claude reads `.claude.json` from under the config dir —
  consistent with moving sensitive state out of the way in P3-5.

## Consequences

- **Auth and onboarding are different things.** Whether authentication succeeded cannot be
  judged from `claude auth status` nor from the startup banner; only sending a real prompt with
  `send-keys` and getting a reply proves it.
- Showing Claude's connection state only needs a runtime probe of `claude auth status` (the
  JSON `loggedIn` field). No database table required.

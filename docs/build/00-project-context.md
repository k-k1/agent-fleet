---
audience: "someone changing the code, who needs the assumptions the design rests on"
source_of_truth: "the decision records — this page is an index onto them"
updated: "2026-09"
---

# 00. Project context — status and the settled assumptions

English | [日本語](00-project-context.ja.md)

The premises the rest of this shelf is written on top of. Each row that mattered enough
to be argued has a decision record; this page exists so the assumptions are readable in
one place instead of being reconstructed from twelve ADRs.

## Status

**Phase 2 complete, Phase 3 in progress.** Multiple users can work in parallel, mutually
invisible, on a single on-prem host (per-user Workspace / AuthGateway / network isolation
/ at-rest encryption). Phase 3 productization has reached the packaging and distribution
milestone (P3-10): the Console rebuild (React + Vite), the AWS ECS adapter (P3-7) and the
compose / ECS / Docker-less native distribution targets are shipped, with 0.x releases
published to the [distribution repository](https://github.com/k-k1/agent-fleet-dist).
The forward plan is [roadmap.md](../roadmap.md).

## Settled assumptions (v1)

| Topic | Decision | Rationale / notes |
|------|------|-----------|
| Agent auth | each user connects their own account/seat from the Console (Claude: OAuth code paste; Codex: ChatGPT device code or API key; Copilot rides the GitHub connection; Cursor / Kiro: browser sign-in; OpenCode: provider API keys or an opencode account) | the console surfaces each user's auth state and prompts re-login; a manual `/login` in the terminal still works as a fallback |
| User isolation | one container per user | highly portable, strong isolation, fits AWS well |
| Target scale | ~20 users (concurrent) | a single cluster + an orchestration layer is enough |
| Persistence | `local`=bind mount / `aws`=EBS/EFS | home, clones, credentials and history are kept on disk |
| Git auth | HTTPS tokens/OAuth via Console (Connections) | downgraded from SSH keys; the CP holds no secrets ([decisions/0003](../decisions/0003-ssh-to-connections.md)) |
| Tech stack | Console=React+Vite / Backend=Go | Go suits daemons, WS proxying and container control ([decisions/0004](../decisions/0004-vanilla-to-react.md)) |
| Delivery model | packaged product, self-hosted per company | 1 company = 1 deployment. SaaS abandoned due to ToS ([decisions/0001](../decisions/0001-self-host-vs-saas.md)) |
| Deployment layer | local / aws switchable over one core | separated via ports & adapters (local = Docker, local-first) |

## What this was built out of

A personal fleet-operation setup already existed; the product is that setup generalised.
Knowing which parts came from where explains a few shapes in the code:

- **`oauth2-proxy`** — a Google domain-restricted auth gate (`emails.txt` allowlist).
  **Replaced by CP-native Google OAuth (`AUTH=oauth`)**; the allowlist is now
  `deploy/local/allowed-emails.txt` (emails or `@domain`). Design:
  [07 §7.3](07-security.md).
- **`scripts/tmux-claude.sh`** — idempotently started, resumed and generation-managed
  several Claude CLIs in detached tmux. The session model in [04](04-agent.md) is the
  descendant of this.
- **`CLAUDE_CONFIG_DIR` profile separation** — a separate `~/.claude` per directory.
- **`~/.claude/settings.json`** with `remoteControlAtStartup` and
  `skipDangerousModePermissionPrompt` preconfigured.

## Screenshots

The images in the repository's README are captured from the real Console bundle against
a demo dataset, in both locales. Regenerate with
`node console/scripts/shots/capture.mjs --locale en` (the default locale is `ja`);
[how](../../console/scripts/shots/README.md).

---
audience: "someone changing the code, who needs the assumptions the design rests on"
source_of_truth: "the decision records and guide/ref — this page is an index onto them"
updated: "2026-09"
---

# 00. Project context — status and the settled assumptions

English | [日本語](00-project-context.ja.md)

The premises the rest of this shelf is written on top of. Each row that mattered enough
to be argued has a decision record; this page exists so the assumptions are readable in
one place instead of being reconstructed from the decision records one by one.

## Status

Agent Fleet ships as 0.x releases, published with their release notes on the
[distribution repository](https://github.com/k-k1/agent-fleet-dist/releases). What is
built and what is not, area by area, is [01 §1.7](01-architecture.md#17-what-is-built-and-what-is-not).

The phase plan (Phases 0–3 and their `P3-n` milestones) was frozen on 2026-09-29 and is
no longer maintained; the labels survive in older decision records and journals as
history. The one Phase 3 criterion still open — someone outside the project installs
from the distribution bundle and its runbooks alone and records a passing E2E run
([decisions/0001](../decisions/0001-self-host-vs-saas.md)) — is
[#1175](https://github.com/k-k1/agent-fleet/issues/1175). All other open work is in
[GitHub issues](https://github.com/k-k1/agent-fleet/issues).

## Settled assumptions (v1)

| Topic | Decision | Rationale / notes |
|------|------|-----------|
| Delivery model | packaged product, self-hosted per company | 1 company = 1 deployment. SaaS abandoned due to ToS ([decisions/0001](../decisions/0001-self-host-vs-saas.md)) |
| Agent auth | each member brings their own account for the agent CLIs, connected from the Console | the reason for self-hosting per company ([decisions/0001](../decisions/0001-self-host-vs-saas.md)). `lcpp` is the exception: it runs on the deployment's engine, or on a llama.cpp server the member points it at, and has no sign-in. How each kind signs in: [ref/agents](../../guide/ref/agents.md#how-to-sign-in) |
| User isolation | one workspace per membership (a person in a tenant) | a container on every target except `native`, which is single-user by design ([ref/deploy-targets](../../guide/ref/deploy-targets.md)). Why it is one long-lived workspace per member rather than an environment per task: [decisions/0104](../decisions/0104-long-lived-member-workspace.md) |
| Target scale | tens to ~100 members per deployment; sized for about 20 at once | sizing assumptions, not measured limits. One host, or one ECS cluster, is meant to be enough. The code sets no deployment-wide cap; a tenant can be given workspace and session limits ([ref/limits](../../guide/ref/limits.md)) |
| Deployment layer | one core; the runtime adapter is chosen by `AF_RUNTIME` | `docker` (the default), `native`, `ecs`, `ecs-ec2`, behind ports and adapters ([01 §1.6](01-architecture.md#16-ports-and-adapters--where-the-platform-dependency-is-confined)); what differs between them is [ref/deploy-targets](../../guide/ref/deploy-targets.md) |
| Persistence | a workspace's persistent data — working copies, CLI logins, local conversation history — survives stopping and starting it | not all of it is in the home: Claude's state has its own directory (`CLAUDE_CONFIG_DIR` below), and on `ecs-ec2` the home is on EBS while a selected set of credential and identity files is kept on EFS. Where the home lives per target is the "Home lives on" column of [ref/deploy-targets](../../guide/ref/deploy-targets.md). Some conversations are not stored locally at all; a managed cursor session's, for example, stays on Cursor's server ([ref/agents](../../guide/ref/agents.md)) |
| Git auth | HTTPS tokens/OAuth via Console (Connections) | downgraded from SSH keys ([decisions/0003](../decisions/0003-ssh-to-connections.md)). A member's token lives in their workspace's encrypted store; the CP passes it through but does not hold it. What the CP holds are the tenants' OAuth app secrets ([08 §8.1](08-integrations.md#81-the-integrations)) |
| Tech stack | Console=React+Vite / Backend=Go | React + Vite for the Console: [decisions/0004](../decisions/0004-vanilla-to-react.md). The Control Plane and the Workspace Agent are Go (two modules), which suits daemons, WebSocket relaying and container control; no decision record argues that choice |

## What this was built out of

A personal fleet-operation setup already existed; the product is that setup generalised.
Knowing which parts came from where explains a few shapes in the code:

- **`oauth2-proxy`** — a Google domain-restricted auth gate with an `emails.txt`
  allowlist. **Replaced by the CP's own login (`AUTH=oauth`)**, which now takes Google,
  GitHub or any OIDC provider; `AUTH=proxy` still trusts a gate like oauth2-proxy. The
  file format survives as `AF_OAUTH_ALLOWED_EMAILS_FILE` (one email or `@domain` per
  line; edits need no restart). Design: [07 §7.3](07-security.md#73-l1-console-authentication--three-modes).
- **`tmux-claude.sh`** — a script in that personal setup, never part of this repository,
  which idempotently started, resumed and generation-managed several Claude CLIs in
  detached tmux. The session model in [04](04-agent.md) is the descendant of this; why the
  long-lived shape survived is [decisions/0104](../decisions/0104-long-lived-member-workspace.md).
- **`CLAUDE_CONFIG_DIR` profile separation** — a separate `~/.claude` per directory. Every
  runtime now sets one per workspace, outside the browsable home.
- **`~/.claude/settings.json`** with `remoteControlAtStartup` and
  `skipDangerousModePermissionPrompt` preconfigured. `workspace/entrypoint.sh` still seeds
  a default `settings.json` into a new workspace's `$CLAUDE_CONFIG_DIR` (with Remote
  Control at startup off); after that the Console's Claude settings own the file.

## Screenshots

The images in the repository's README are captured from the real Console bundle against
a demo dataset, once per locale, by `console/scripts/shots/capture.mjs`;
[how](../../console/scripts/shots/README.md).

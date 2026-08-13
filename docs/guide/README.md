# Agent Fleet User Guide

English | [日本語](README.ja.md)

Agent Fleet is a service that lets multiple members of your organization use coding AIs
such as Claude Code from the browser. Each person gets a working environment (workspace)
isolated from other users, where they can clone repositories and launch and operate AI sessions.

This guide is **split into volumes per reader role**. Start with the one that applies to you.
This guide is the authoritative source for the "how to" of operating the product; the internal
"how it works" lives in the developer-facing [dev/](../dev/README.md).

## Which one are you?

| You are… | Volume to read | What it covers |
|-----------|----------|---------------------|
| A developer writing code in the Console every day | **[member/](member/README.md)** | Login through sessions, git, files, agents, chat, and troubleshooting |
| Someone who skips the terminal — mostly chat and progress checks | **[lite.md](lite.md)** | The minimal guide for using Agent Fleet without touching the black screen |
| Someone managing the team's members, limits, and audits | **[admin/](admin/README.md)** | Adding members, resource limits, audit logs, usage, distributing shared MCP servers |
| IT / SRE handling deployment, backups, and incident response | **[operator/](operator/README.md)** | Setup, operations, security, troubleshooting |

You may fall into more than one (e.g. a team lead who also develops is member + admin). The
volumes are written to be readable independently, so move between them as needed.

## Considering adoption?

"What can it do / how is security handled / what do we need" is summarized at the beginning of
[operator/README.md](operator/README.md) (architecture, prerequisites, security posture).
Read that first. For the big picture, see the project [README](../../README.md) and the
developer-facing [dev/01 Architecture](../dev/01-architecture.md).

## Terminology (bare minimum)

- **Workspace** — your own dedicated working environment. Holds your repositories and work.
- **Session** — the unit corresponding to one task: a conversation, a place to work, and an execution state. It may not have a terminal.
- **Tenant** — a group such as a department. Your workspaces are separated per tenant (one by default).
- **Agent** — a coding AI such as Claude / Codex / OpenCode.

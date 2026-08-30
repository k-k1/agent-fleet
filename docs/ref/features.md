# Features — the catalogue

English | [日本語](features.ja.md)

Audience: everyone; the index the other tables hang off
Source of truth: this table for "does it exist and who can use it"; the linked shelf for how
Updated: 2026-08

One row per thing the product does, grouped by where the reader meets it. **Who** is
the smallest role that can use it (see [roles.md](roles.md)); **Where** is the screen,
named as the Console names it.

If a feature ships and does not appear here, it is not done
([CONVENTIONS §8](../CONVENTIONS.md)).

## Working with a session

| Feature | Who | Where | Details |
|---|---|---|---|
| Start a session (agent, model, effort, start mode) | | | |
| Run in a fresh git worktree | | | |
| Chat mirror — follow and steer a running agent | | | |
| Answer a question, plan or permission prompt | | | |
| Skill / command picker | | | |
| Live terminal attached to a session | | | |
| Resume a stopped session | | | |
| Hand off a conversation to another session | | | |
| Fork from a past message | | | |
| Hand a session to another member | | | |
| Share a session read-only | | | |
| Transcript marks | | | |
| Files this session changed | | | |
| Context usage gauge | | | |
| Abort detection and auto-resume | | | |

## Working with code

| Feature | Who | Where | Details |
|---|---|---|---|
| Import a repository | | | |
| Start from an empty folder | | | |
| Commit graph, diff, stage and commit | | | |
| Worktrees | | | |
| File tree and viewer | | | |
| Markdown and code editing | | | |
| `.drawio` diagrams | | | |
| Browser pane for a local web app | | | |
| Attach to a Chromium the agent owns | | | |

## Organising the work

| Feature | Who | Where | Details |
|---|---|---|---|
| Working sets | | | |
| Memo queue | | | |
| Work-item inbox (issues, tickets, pull requests) | | | |
| Scheduled (unattended) runs | | | |
| Notification centre | | | |
| Assistant chat | | | |
| Chat bridge (Discord / Slack) | | | |
| Reply suggestions | | | |
| Keyboard system | | | |
| Text-to-speech | | | |

## Personal settings

| Feature | Who | Where | Details |
|---|---|---|---|
| Agent connections | | | |
| Instructions to agents | | | |
| Agent memory management | | | |
| Git hosting connections | | | |
| Internal repositories | | | |
| AWS SSM | | | |
| Integration servers and tokens | | | |
| Issue-tracker connections | | | |
| Usage | | | |
| Cloud cost | | | |
| Display, language and keys | | | |
| Export / import settings | | | |
| Toolchains | | | |
| Destructive actions | | | |

## Tenant administration

| Feature | Who | Where | Details |
|---|---|---|---|
| Members | | | |
| Sessions across the tenant | | | |
| Limits and idle auto-stop | | | |
| Workspace sizing | | | |
| Sign-in methods and login rules | | | |
| Connection-source restriction | | | |
| Integration app OAuth | | | |
| Distributing integration servers | | | |
| Audit | | | |
| Uptime and cloud cost | | | |
| Deletion lock | | | |
| Cleanup and the restore shelf | | | |

## Operating a deployment

| Feature | Who | Where | Details |
|---|---|---|---|
| Deployment targets | | | |
| Install, upgrade, back up, restore | | | |
| Ingress, TLS and sign-in providers | | | |
| Audit log and egress control | | | |
| Monitoring integrations | | | |
| Slot pool and instance classes | | | |
| Role-scoped documentation in containers | | | |

## Status

Axis fixed, cells to be filled in phase P1. The axis is derived from the Console's own
surfaces — its settings tabs and panes — so that "the product does X" and "the reader
can find X on screen" cannot drift apart.

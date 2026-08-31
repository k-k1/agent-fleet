# Features — the catalogue

English | [日本語](features.ja.md)

Audience: everyone; the index the other tables hang off
Source of truth: this table for "does it exist and who can use it"; the linked page for how
Updated: 2026-08

One row per thing the product does, grouped by where the reader meets it. **Who** is
the smallest role that can use it ([roles.md](roles.md)); **Where** is the screen,
named as the Console names it.

If a feature ships and does not appear here, it is not done
([CONVENTIONS §8](../CONVENTIONS.md)).

> While the reader shelves are being written, the **Details** column points at the
> guide that is still authoritative. Those links move to `use/`, `admin/` and
> `operate/` as each shelf lands.

## Working with a session

| Feature | Who | Where | Details |
|---|---|---|---|
| Start a session — agent, model, effort, start mode | member | workspace action bar → Start | [02 Sessions](../use/02-sessions.md) |
| Run in a fresh git worktree | member | Start dialog | [04 Git](../use/03-code.md) |
| Chat mirror — follow and steer a running agent | member | main area | [02 Sessions](../use/02-sessions.md) |
| Answer a question, plan, or permission prompt | member | mirror | [07 Chat and memos](../use/07-chat-memo.md); [agents.md](agents.md) for which agents ask |
| Skill / command picker | member | mirror composer | [07 Chat and memos](../use/07-chat-memo.md); [agents.md](agents.md) for which agents |
| Live terminal attached to a session | member | main area | [03 Terminal](../use/05-terminal.md) |
| Terminal round-trip time (keystroke lag) | member | terminal pane header | [03 Terminal](../use/05-terminal.md) |
| Resume a stopped session | member | left pane → the session | [02 Sessions](../use/02-sessions.md) |
| Hand a conversation to a new session | member | session ⋯ menu | [02 Sessions](../use/02-sessions.md) |
| Fork from a past message | member | mirror, on a past message | [07 Chat and memos](../use/07-chat-memo.md); [agents.md](agents.md) for which agents |
| Hand a session to another member | member | session ⋯ menu | [02 Sessions](../use/02-sessions.md) |
| Share a session read-only | member | Shared sessions | [02 Sessions](../use/02-sessions.md) |
| Highlights | member | mirror, on a selection | [09 Working with others](../use/09-collaboration.md) |
| Changed files | member | session ⋯ menu | [09 Working with others](../use/09-collaboration.md) |
| Context usage gauge | member | session header | [06 Agents](../use/06-agents.md) |
| Abort detection and auto-resume | member | automatic | [09 Working with others](../use/09-collaboration.md) |

## Working with code

| Feature | Who | Where | Details |
|---|---|---|---|
| Import a repository | member | Repositories → Clone | [04 Git](../use/03-code.md) |
| Start from an empty folder | member | Repositories → new folder | [04 Git](../use/03-code.md) |
| Commit graph, diff, stage and commit | member | Commit graph | [04 Git](../use/03-code.md) |
| Worktrees | member | under each repository | [04 Git](../use/03-code.md) |
| File tree and viewer | member | Files | [05 Files](../use/04-files.md) |
| Markdown and code editing | member | Files → a file | [05 Files](../use/04-files.md) |
| `.drawio` diagrams | member | Files → a `.drawio` file | [05 Files](../use/04-files.md) |
| Browser pane for a local web app | member | workspace action bar → Preview | [08 Going further](../use/10-integrations.md); [browser-pane.md](browser-pane.md) for the contract |
| Attach to a Chromium the agent owns | member | a link the agent hands you | [08 Going further](../use/10-integrations.md) |

## Organising the work

| Feature | Who | Where | Details |
|---|---|---|---|
| Working sets | member | top of the left pane | [02 Sessions](../use/02-sessions.md) |
| Memo queue | member | Memo queue | [07 Chat & memo](../use/07-chat-memo.md) |
| Work-item inbox — issues, tickets, pull requests | member | Issue tracker | [02 Sessions](../use/02-sessions.md); [repos.md](repos.md) for what each provider contributes |
| Scheduled (unattended) runs | member | Schedules | [11 Fleet operator](../use/08-organising.md) |
| Notification centre | member | top bar | [12 Settings](../use/12-settings.md) |
| Assistant chat | member | Assistants | [07 Chat & memo](../use/07-chat-memo.md) |
| Chat bridge — Discord / Slack | member | Settings → Chat | [08 Advanced](../use/10-integrations.md) |
| Reply suggestions | member | mirror composer | [09 Working with others](../use/09-collaboration.md) |
| Keyboard system | member | Settings → Keyboard | [08 Advanced](../use/10-integrations.md) |
| Text-to-speech | member | Settings → Read aloud | [12 Settings](../use/12-settings.md) |

## Personal settings

Every row is under **Settings**; the tab is the "Where".
[settings.md](settings.md) lists the tabs, [12 Settings](../use/12-settings.md)
explains them.

| Feature | Who | Where |
|---|---|---|
| Agent connections | member | Agents |
| Instructions to agents | member | Agent instructions |
| Agent memory management | member | Agent memory |
| Git hosting connections | member | Git hosting |
| Internal repositories | member | Internal repos |
| AWS SSM | member | AWS SSM |
| Integration servers and tokens | member | MCP servers / MCP tokens |
| Issue-tracker connections | member | Issue tracker |
| Usage | member | Agent usage |
| Cloud cost | member | Cloud cost |
| Display, language and keys | member | Display / Keyboard |
| Export / import settings | member | Export / import |
| Toolchains | member | Toolchains |
| Destructive actions | member | Danger zone |

## Tenant administration

Every row is under **Tenant settings**. [admin/](../admin/README.md) is the shelf;
[roles.md](roles.md) says what is read-only for whom.

| Feature | Who | Where | Details |
|---|---|---|---|
| Members | tenant admin | Members | [admin 01](../admin/01-members.md) |
| Sessions across the tenant | tenant admin | Sessions | [admin 02](../admin/02-limits.md) |
| Limits and idle auto-stop | tenant admin (read) | Limits & idle | [admin 02](../admin/02-limits.md) |
| Workspace sizing | deployment admin | — | [deploy-targets.md](deploy-targets.md) |
| Sign-in methods and login rules | tenant admin | Sign-in methods / Login rules | [operator 05](../operate/05-signin.md) |
| Connection-source restriction | tenant admin | Allowed networks | [admin 05](../admin/05-access.md) |
| Integration app OAuth | tenant admin | Integration OAuth apps | [admin 05](../admin/05-access.md) |
| Distributing integration servers | tenant admin | MCP distribution | [admin 04](../admin/04-mcp-egress.md) |
| Audit | tenant admin | Audit | [admin 03](../admin/03-audit-usage.md) |
| Running time and cloud cost | tenant admin | Running time / Cloud cost | [admin 03](../admin/03-audit-usage.md) |
| Deletion lock | member | session ⋯ menu | [09 Working with others](../use/09-collaboration.md) |
| Cleanup and the trash | member | Clean up | [09 Working with others](../use/09-collaboration.md) |

## Operating a deployment

| Feature | Who | Where | Details |
|---|---|---|---|
| Deployment targets | deployment admin | before start | [deploy-targets.md](deploy-targets.md) |
| Install, upgrade, back up, restore | deployment admin | a shell | [operator 01](../operate/02-install.md) / [02](../operate/03-run.md) |
| Ingress, TLS and sign-in providers | deployment admin | a shell | [operator 03](../operate/04-secure.md) |
| Audit log and egress control | deployment admin | Admin | [operator 03](../operate/04-secure.md) |
| Monitoring integrations | deployment admin | Settings → Ops & monitoring | [member 10](../use/13-ops-tooling.md) |
| Slot pool and instance classes | deployment admin | Admin | [deploy-targets.md](deploy-targets.md) |
| Role-scoped documentation in containers | — | automatic | [roles.md](roles.md) |

## Rows with no Details yet

A dash means the feature exists and works, but no reader-facing page covers it — the
old guide never caught up with it. Those gaps are the reason this catalogue exists,
Phase P2 closed them all: highlights, changed files, reply suggestions, deletion lock,
cleanup and the trash, and abort auto-resume are in
[09 Working with others](../use/09-collaboration.md); connection-source restriction
and integration app OAuth are in [admin 05](../admin/05-access.md).

A dash that remains means the row is covered by the table it links to, not that
something is missing.

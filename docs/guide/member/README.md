# member/ — User guide for developers

English | [日本語](README.ja.md)

> Audience: member developers who write code in the Console every day. This volume covers
> everything from logging in through sessions, git, files, agents, chat, and what to do when
> you get stuck, arranged so you can follow it in the order you'll need it as you start out.
> Get the overall picture of the screen and the terminology on this page first, then move on
> to [01 First day](01-first-day.md).

## How to read this guide

The chapters are arranged **chronologically from your first day**. Read them from the top
and they flow naturally: what to do on day one → daily work → going further. When you're
stuck, it's fine to open [09 Troubleshooting](09-troubleshooting.md) first.

To check what something on the screen is called, see [UI and terminology](ui-and-terms.md);
to look up colored marks and right-click actions, see [Icons, badges, and menus](badges-and-menus.md).

## Table of contents

1. [First day](01-first-day.md) — Logging in, the welcome card (2 steps + picking a goal), your first clone, the end of the day
2. [Sessions](02-sessions.md) — Launching, switching, and stopping conversations with AI
3. [Terminal](03-terminal.md) — Working the black screen, copy & paste, shortcuts, phones
4. [Repositories and git](04-git.md) — Cloning, reviewing changes, committing, pushing (SVN too)
5. [Files](05-files.md) — Tree, viewer, Markdown/slide display
6. [Agents](06-agents.md) — Connecting and choosing claude / codex / opencode / GitHub Copilot / Cursor / Kiro
7. [Chat and memos](07-chat-memo.md) — Questions and translations without a repository, collecting memos
8. [Going further](08-advanced.md) — Browser pane / lightweight preview, Discord / Slack integration, other hosts, environment settings
9. [Troubleshooting](09-troubleshooting.md) — Fixes by symptom, plus an FAQ
10. [Ops tooling PoC](10-ops-mcp-poc.md) 🧪 — Wiring up PagerDuty / Grafana / CloudWatch / AWS over MCP to talk through incidents (experimental)
11. [Fleet operator](11-fleet-operator.md) — Directing multiple sessions from chat, handover, parallel work, scheduled runs
12. [Settings](12-settings.md) — Every tab of the ⚙ settings dialog; the map for "where is that setting?"

For the "how to do it", this guide is canonical. When you get curious about the internal
"how it works", follow the "For those who want to know how it works" link at the end of each
chapter into the developer-facing [dev/](../../dev/README.md).

In the Console, you can open this table of contents in the file viewer at any time while the
workspace is running, via **"User guide"** in the account menu at the top right. If the
workspace is stopped, press "Start workspace" first.

## Parts of the screen

The Console is laid out as **two bars at the top**, a **pane on the left**, and a **main
area in the center**. Detailed names and terms are collected in
[UI and terminology](ui-and-terms.md).

### Top row: the bar at the very top of the screen

This is the horizontal strip at the very top of the screen. From the left: the app name
(Agent Fleet), the **tenant selector** (switching between departments and the like; the
default is a single tenant, so you may never touch it), and on the right **⚙ Settings** and
your account (how you appear). Only people with team-management permissions also see a
**shield icon** (the admin screens).

### Bottom row: the workspace action bar

The strip below that is the **workspace action bar**, which operates your own private
environment — your **workspace**. It holds the operations that affect your work as a whole:
"Start", start/stop, Preview, pane splitting, and so on.

- **● Status lamp** — shows by color whether the workspace is **running** or **stopped**. It refreshes automatically every few seconds, so there is no need to reload manually.
- **Start / Stop workspace button** — a single button that starts and stops the workspace.
- **Preview** — the entry point for viewing a web service you started inside the workspace: the **browser pane** rendered inside a Console pane, the **lightweight preview** that opens in another tab, and — where the deployment configures it — the **preview subdomains** issued on every start, one URL per port ([08 Going further](08-advanced.md)).

### Left pane

While the workspace is running, the following sections are stacked vertically.

- **Working set** (pinned at the top) — narrows the view down to one piece of work ([02](02-sessions.md)).
- **Assistants** — simple chat that doesn't use a repository ([07](07-chat-memo.md)).
- **Memo queue** — memos of instructions to send later in a batch ([07](07-chat-memo.md)).
- **Schedules** — scheduled runs and their history ([11](11-fleet-operator.md)).
- **Repositories** — working copies, and the sessions running at each location ([02](02-sessions.md), [04](04-git.md)).
- **Other sessions** — sessions that don't belong to a repository ([02](02-sessions.md)).
- **Shared sessions** — sessions you shared, and sessions shared with you ([02](02-sessions.md)).
- **Files** — a file browser for the workspace ([05](05-files.md)).

While the workspace is stopped, you can review sessions in **Session history**.

(On a phone the left pane is hidden; pull it out with the **≡ (menu)** at the top left of the screen. See [03 Terminal](03-terminal.md) for details.)

### Main area

The wide central area switches its contents depending on what you select in the left pane.

- **Terminal** — the black screen of Terminal (CLI) sessions, shell, and SSM ([03](03-terminal.md)).
- **Commit graph** — changes, diffs, commits, history ([04](04-git.md)).
- **File viewer** — displaying the contents of files ([05](05-files.md)).
- **Chat** — conversations with running agents, and questions / translations that don't use a repository ([07](07-chat-memo.md)).

For AI sessions running as Terminal (CLI), the **Chat ⇄ Terminal** switch at the top toggles
between the conversation view and the terminal view. Managed execution has no terminal view.
The main area can be split into multiple panes arranged side by side
([02](02-sessions.md), [03](03-terminal.md)).

## Terminology (the bare minimum)

- **Workspace** — your own private work environment. It holds your cloned repositories and your work. Its contents survive a stop (behind the scenes it runs as a dedicated container).
- **Session** — a unit of conversation, working location, and execution state corresponding to one task. It may or may not have a terminal.
- **Agent** — a CLI coding AI such as claude / codex / opencode / GitHub Copilot / Cursor / Kiro.
- **Tenant** — a group such as a department. Your workspaces are separated per tenant (the default is a single tenant).
- **worktree** — a git mechanism that carves out an independent working folder per branch from a single repository. Launching a session creates one by default, so multiple tasks can run in parallel without interfering with each other ([02](02-sessions.md) / [04](04-git.md)).

For detailed definitions and writing conventions, see [UI and terminology](ui-and-terms.md).

---

For those who want to know how it works: [dev/02 Console design](../../dev/02-console.md)

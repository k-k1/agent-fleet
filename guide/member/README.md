---
audience: "anyone who runs agents from the Console"
source_of_truth: "the Console itself — if a screen disagrees with this guide, the screen is right"
---

# Using Agent Fleet

English | [日本語](README.ja.md)

This shelf answers **"how do I do this?"** for the person doing the work: starting
sessions, following and steering an agent, working with repositories and files,
connecting the agent you want to use, and getting unstuck.

If you also manage a team's members and limits, [Administering a
tenant](../admin/README.md) is your entry point; if you install and keep a deployment
alive, that is [Operating a deployment](../operate/README.md).

## Chapters

The chapters follow the order you will need them, from your first day to daily work
to going further. When you are stuck, it is fine to open [21 Troubleshooting](21-troubleshooting.md) first.

### Getting started

1. [First day](01-first-day.md): signing in, the welcome card, your first clone
2. [Console basics](02-console-basics.md): shortcuts, the command palette, panes, phones
3. [Starting a session](03-starting-sessions.md): kinds, execution method, the Start dialog, the issue tracker

### Daily work

4. [Talking to a session](04-conversation.md): replying, plans, highlights, changed files, reply suggestions, translation
5. [Stopping, resuming and tidying up](05-stopping-and-tidying.md): stop and resume, handoff, keep awake, locks, cleanup
6. [Repositories and git](06-code.md): cloning, reviewing changes, committing, pushing (SVN too)
7. [Files](07-files.md): the tree, the viewer, editing, Markdown and diagrams
8. [Terminal](08-terminal.md): copy & paste, round-trip time, font and size
9. [Agents](09-agents.md): connecting one, and choosing between them
10. [Chat and memos](10-chat-memo.md): questions without a repository, collecting memos

### Going further

11. [Running many sessions at once](11-many-sessions.md): the overview, working sets, child sessions, messages, the fleet operator, scheduled runs
12. [Working with others](12-collaboration.md): sharing, handing a session over, the inbox, usage limits
13. [Images](13-images.md): the gallery, generation and the studio
14. [Preview and browser](14-preview-browser.md): the browser pane, preview URLs, the development sign-in, a browser the agent opened
15. [External services and signed-in CLIs](15-external-services.md): `gh` / `bkt` / `jira`, Discord / Slack, ops tooling (experimental)
16. [Cloud accounts](16-cloud-accounts.md): AWS profiles, SSM hosts, `af-aws-exec` and Google Cloud
17. [Workspace environment](17-workspace-environment.md): environment settings, recreating, test databases, a local AWS emulator
18. [Agent memory](18-agent-memory.md): what agents remember across kinds and sessions, importing it, and snapshots of their own memory
19. [Using your workspace from outside](19-outside-access.md): MCP, OAuth sign-in, and memory from an external agent

### Reference

20. [Settings](20-settings.md): every tab, and "where is that setting?"
21. [Troubleshooting](21-troubleshooting.md): fixes by symptom, and an FAQ

Also here: [Icons, badges and menus](badges-and-menus.md) for what a mark on the
screen means, and the [Simple guide](lite.md), the short version for someone who never
opens the terminal.

What each agent, provider or role can actually do is in the
[Reference](../ref/README.md), and the terminology is in the
[Glossary](../ref/glossary.md).

You can open this page in the Console at any time while the workspace is running, from
**"User guide"** in the account menu.

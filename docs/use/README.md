# Using Agent Fleet

English | [日本語](README.ja.md)

Audience: anyone who runs agents from the Console
Source of truth: the Console itself — if a screen disagrees with this shelf, the screen is right
Updated: 2026-08

This shelf answers **"how do I do this?"** for the person doing the work: starting
sessions, following and steering an agent, working with repositories and files,
connecting the agent you want to use, and getting unstuck.

## What belongs here

- Step-by-step procedures, in the order the reader will actually do them.
- What a screen, badge or menu means.
- What to try when something looks wrong, and how to tell the difference between
  "still working" and "stuck".

## What does not

- **Capability facts.** Which agent supports plan mode, which provider supports pull
  requests, what a role may do — those live in [ref/](../ref/README.md) so there is
  only one copy. Link to the table.
- **Anything a reader cannot see on screen.** No environment variable names, no
  internal identifiers, no API paths, no source paths. The words in the Console are
  the correct words; see [CONVENTIONS](../CONVENTIONS.md).
- **Administration.** Anything that affects other people belongs in
  [admin/](../admin/README.md); anything about installing or keeping a deployment
  alive belongs in [operate/](../operate/README.md).

## Update trigger

A change to a screen, a flow, or a default that the reader would notice. If a feature
ships and this shelf says nothing about it, the feature is not done
([CONVENTIONS §8](../CONVENTIONS.md)).

## Chapters

Arranged in the order you will need them, starting on your first day. Read from the
top and it flows: day one → daily work → going further. When you are stuck it is fine
to open [11 Troubleshooting](11-troubleshooting.md) first.

1. [First day](01-first-day.md) — signing in, the welcome card, your first clone
2. [Sessions](02-sessions.md) — launching, switching and stopping conversations
3. [Repositories and git](03-code.md) — cloning, reviewing changes, committing, pushing (SVN too)
4. [Files](04-files.md) — the tree, the viewer, editing, Markdown and diagrams
5. [Terminal](05-terminal.md) — the black screen, panes, copy & paste, phones
6. [Agents](06-agents.md) — connecting one, and choosing between them
7. [Chat and memos](07-chat-memo.md) — questions without a repository, collecting memos
8. [Running several at once](08-organising.md) — directing sessions from chat, parallel work, scheduled runs
9. [Working with others, and keeping things tidy](09-collaboration.md) — highlights, changed files, sharing, locks, cleanup
10. [Connecting outward](10-integrations.md) — Discord / Slack, driving from an external Claude, other hosts
11. [Troubleshooting](11-troubleshooting.md) — fixes by symptom, and an FAQ
12. [Settings](12-settings.md) — every tab, and "where is that setting?"
13. [Ops tooling](13-ops-tooling.md) — wiring monitoring tools into a conversation (experimental)

Also here: [Icons, badges and menus](badges-and-menus.md) for what a mark on the
screen means, and [lite.md](lite.md) — the short version for someone who never opens
the terminal.

**What each agent, provider or role can actually do is in
[ref/](../ref/README.md)** — this shelf tells you how, and links there rather than
repeating it. Terminology is [ref/glossary.md](../ref/glossary.md).

You can open this page in the Console at any time while the workspace is running, from
**"User guide"** in the account menu.

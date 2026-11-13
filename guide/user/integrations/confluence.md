---
audience: "anyone who wants agents to work with Confluence pages"
source_of_truth: "the confluence command in a session (Agent Fleet's wrapper) and the Console's Settings > \"Issue tracker\""
---

# Use Confluence

English | [日本語](confluence.ja.md)

Connect Jira, and the same Atlassian connection lets agents find, read and write Confluence pages with the
`confluence` command. Confluence needs no setup or login of its own.

## How it works

| What you do | What the agent does |
|---|---|
| Connect Jira | Finds, reads, creates and updates pages with `confluence` |
| Ask for what you need | Writes to Confluence as your Atlassian account |
| Reconnect when the connection breaks | Says so and stops when the connection cannot be used |

`confluence` works in every agent's sessions. An OAuth token is checked on every call and renewed before it expires, so you never restart a session for it.

## Before you start

Connect Jira in **⚙ Settings → "Issue tracker"**. The steps are in [Connect an issue tracker](issue-tracker.md#connect-jira).

- With an API token connection there is nothing else to prepare
- With an OAuth connection, the tenant's Atlassian app needs the Confluence scopes. Without them, the Jira card shows a warning

| What the Jira card says | What does not work |
|---|---|
| Confluence: not available. | Every `confluence` command |
| Confluence: search and comments work, but … | Reading, creating and updating pages |

Either way, it works once the tenant administrator has added the scopes to the app and you reconnect Jira.

## Ask an agent

Just ask the way you normally would.

> Find the "Release procedure" page in Confluence and sum it up

> Write this design note as a new page in the DEV space

> Add this bug to the "Known issues" section of page 123456

Agents use commands like these.

| Task | Example |
|---|---|
| Find pages | `confluence search "release notes"` |
| Read a page | `confluence read PAGE_ID --format markdown` |
| Look up a space | `confluence space-lookup KEY` |
| Create a page | `confluence create "Title" SPACE --content "…"` |
| Update a page | `confluence update PAGE_ID …` |

- **What is written carries your name.** It can do only what your Atlassian account is allowed to do
- **Pages can be read and written as Markdown.** Japanese, emoji, tables, lists, and `code` or **bold** next to Japanese text are kept as they are
- **Someone else's edit is never overwritten.** If another person changed the page after it was read, the update is
  refused. The agent reads the page again and redoes the edit

## Use it yourself

It works the same way from a terminal. There is no `confluence init` to run.

```sh
confluence search "release notes"
confluence --help
```

## When it goes wrong

### "Reconnect Jira in Settings"

The connection has expired or been revoked, or it lacks the Confluence scopes. Check the Jira card in
**⚙ Settings → "Issue tracker"** and reconnect Jira.

### "may or may not have been …"

The connection dropped after the request was sent, so it is not known whether the page was written. Writes are never
re-run on their own. Search for the title or read the page before running it again.

### Some commands fail with "410"

These commands still use an old API that Atlassian has removed, so they can fail.

- `move`, `delete`, `children`, `find`, `spaces`, `copy-tree`
- Page versions and properties (front matter)

Folders cannot be created with `create --type folder`.

### "--jq is not available"

`confluence api --jq` cannot be used with the saved connection. Pipe instead, as in `confluence api … | jq '…'`.

### Use Confluence Data Center

It cannot be connected in Settings. Set it up yourself with `confluence init`, `--profile`, or variables such as
`CONFLUENCE_DOMAIN` and `CONFLUENCE_API_TOKEN`. `confluence` then runs as you configured it, without the saved connection.

## Related

- [Connect an issue tracker](issue-tracker.md): the Jira connection (Confluence uses the same one)
- [Use Jira](jira.md): Jira issues over the same connection

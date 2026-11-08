---
audience: "anyone who wants agents to work with Jira issues"
source_of_truth: "the jira command in a session (Agent Fleet's wrapper) and the Console's Settings > \"Issue tracker\""
---

# Use Jira

English | [日本語](jira.ja.md)

Connect Jira, and agents can read, create, comment on and move issues with the `jira` command. No agent has to log in
inside a session.

## How it works

| What you do | What the agent does |
|---|---|
| Connect Jira | Reads, creates and updates issues with `jira` |
| Ask for what you need | Writes to Jira as your Jira account |
| Reconnect when the connection breaks | Says so and stops when the connection cannot be used |

`jira` runs with the permissions of the Jira account you connected. Agents already know how to use it, so there is no
command to teach them. An OAuth token is checked on every call and renewed before it expires, so you never restart a session for it.

## Before you start

Connect Jira in **⚙ Settings → "Issue tracker"**. The steps are in [Connect an issue tracker](issue-tracker.md#connect-jira).

- Either OAuth or an API token will do
- Only Jira Cloud (`….atlassian.net`). Jira Data Center is [set up yourself](#use-jira-data-center)

## Ask an agent

Just ask the way you normally would.

> Read PROJ-123 and comment with a proposed fix

> File an issue in PROJ for what we are working on. Its parent is PROJ-100

> Move PROJ-123 to "In Progress" and assign it to me

Agents can do things like these.

| Task | Example |
|---|---|
| Read and find issues | `jira issue view PROJ-1`, `jira issue list -p PROJ` |
| Create an issue | `jira issue create -p PROJ` (parent: `-P PROJ-100`) |
| Comment | `jira issue comment add PROJ-1 "…"` |
| Edit or delete a comment | `jira issue comment edit PROJ-1 10001 -b "…"`, `jira issue comment delete PROJ-1 10001` |
| Move an issue | `jira issue move PROJ-1 "In Progress"` |
| Assign an issue | `jira issue assign PROJ-1 <user>` |

- **What is written carries your name.** It reaches as far as your Jira account is allowed
- **Comments and descriptions can be Markdown.** `code` and **bold** next to Japanese text, lists and tables show in Jira as written
- **Find a comment's ID** with `jira issue view PROJ-1 --raw` (`.fields.comment.comments[].id`)
- **Only certain comments get edited or deleted.** Agents are told to edit or delete only a comment they posted in that
  session, or one you named
- **Check the parent of an issue that was created with one.** Agent Fleet adjusts the call so that the parent is sent,
  but this has not been confirmed for every kind of project

## Use boards, sprints and epics

To work with boards, sprints and epics, set up the project and board once. Ask an agent and it runs this for you.

```sh
jira init --project PROJ --board "PROJ board"
```

- `jira board list -p PROJ` shows the exact board name
- Add `--force` to redo the setup
- Without the setup, issues still work if each command names `-p PROJ`

A second board gets a configuration file of its own. The connection is used only with configuration files in
`~/.config/agent-fleet/jira/`.

```sh
jira init -c ~/.config/agent-fleet/jira/other-board.yml --project PROJ --board "Other board"
jira -c ~/.config/agent-fleet/jira/other-board.yml sprint list
```

> [!NOTE]
> With an OAuth connection, the tenant's Atlassian app needs the Jira Software scopes. Without them, the Jira card in
> Settings › "Issue tracker" says "**Boards, sprints and epics: not available.**", and those commands and `jira init` fail. Have the tenant administrator add
> them, then reconnect Jira.

## Have agents use the Atlassian MCP tool

Agents normally use `jira` and turn to an MCP tool only for what `jira` cannot do. To have them prefer the MCP tool,
write it in **⚙ Settings → "Agent instructions"**.

> For Jira, use the Atlassian MCP tool instead of the `jira` CLI.

Agents that have the connector follow it; the others keep using `jira`. An MCP connector has to be set up for each
agent, and when its login expires the session has to be restarted.

## Use it yourself

It works the same way from a terminal.

```sh
jira issue list -p PROJ
jira issue view PROJ-1 --comments 5
```

- `jira init` never asks for a token
- A run with `--debug` does not use the connection (the debug output would show the credentials)

## When it goes wrong

### "the Jira connection is unusable"

The connection has expired or been revoked. Reconnect Jira in **⚙ Settings → "Issue tracker"**.

### Creating an issue exited with status 3

The issue exists; only its description was not set. **Do not create it again.** Set the description with
`jira issue edit PROJ-1 --no-input -b "…"`.
When `issue edit -b` exits with status 3 it means the same, and the other changes were applied.

### A write exited with status 124

Jira did not answer, and the command was stopped. In a run without a terminal (an agent's, for example) a write is
stopped after about two minutes. The issue or comment may exist, so check Jira before running it again.

- For `issue create`, the issues you created with the same summary in the last 15 minutes are listed as "possible matches"
- Reading commands are re-run up to twice after a connection drop. Writes are never re-run

### "this config has no issue types"

`jira issue create` needs the project's issue types, and `jira` could not get them. Run `jira init` as the hint printed with it says.

### Assigning someone asks for a project

Looking up an assignee needs a project. Pass `-p PROJ`, or run `jira init --project PROJ --board "PROJ board"`.
`jira issue assign PROJ-1 <user>` takes the project from the issue key.

### Boards, sprints or epics fail with an authorization error

The OAuth connection lacks the Jira Software scopes. Ask the tenant administrator, as in [the note above](#use-boards-sprints-and-epics).
Meanwhile the links `jira` prints point at `api.atlassian.com` rather than your site.

### Use Jira Data Center

It cannot be connected in Settings. Set it up yourself with `jira init` and your own `--server`, a configuration file of
your own, or `JIRA_API_TOKEN`. `jira` then runs as you configured it, without the saved connection.

## Related

- [Connect an issue tracker](issue-tracker.md): the Jira connection and the left rail's list
- [Use Confluence](confluence.md): Confluence pages over the same connection

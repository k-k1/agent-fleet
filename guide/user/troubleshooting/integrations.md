---
audience: "anyone whose MCP server, AWS or Google Cloud command, Jira or outside app does not behave"
source_of_truth: "the Console's Settings > MCP servers (used by sessions), Issue tracker and MCP tokens & apps, and the messages the session wrapper commands print"
---

# Fix MCP, cloud and Jira integrations

English | [日本語](integrations.ja.md)

Find the cause and the fix when a registered MCP server is not available, an AWS or Google Cloud command stops, `jira` ends with an error, or an app connected from outside shows no tools.

## An MCP server you registered is not available in sessions or assistants

Work through ⚙ Settings → MCP servers (used by sessions) from the top.

1. Is the server **enabled**?
2. Do the **targets** under Advanced include sessions or assistants, and are the **target agents** not narrowed too far?
3. Does it say **"These personal registrations are unused because a tenant-distributed server has the same name: …"**? The tenant's server of the same name wins
4. Press **"Test connection"**: do a server name and a tool count come back?

A change applies to the sessions you start next. A running session does not change until it restarts.
Missing values, and asking for outbound access, are in [Add an MCP server to your sessions](../integrations/mcp-servers.md#when-it-goes-wrong).

## The connection test passes, but the session says `Session not found`

This happens when a server that keeps MCP sessions in memory (a stateful Streamable HTTP server) **runs as several instances without session affinity**.
One instance issues the session ID, and the next request lands on another that has never heard of it.
When the connection test finds this, it says **"The server rejected the session ID it just issued. …"**.

Agent Fleet cannot fix it on its side. Change the server to one of these:

- Make it stateless (a stateless Streamable HTTP mode, for example)
- Run a single instance
- Route every request with the same `Mcp-Session-Id` to the same instance

Load-balancer cookie stickiness does not help, because Claude Code does not send cookies back on MCP requests.
Once the server is fixed, restart the session.

## An MCP server's row says "Sign in again" or "More permission needed"

These are the states of an OAuth server's row. An OAuth server is handed to sessions and assistants only while you are signed in.

| The row says | What it means | What to do |
|---|---|---|
| **"Sign in again"** | The saved sign-in can no longer be renewed | Press **"Sign in again"**. Until you do, sessions you start are not given the server |
| **"More permission needed"** | You have not yet granted a permission a tool needs | Press **"Approve the extra permission"**. It signs you in again, adding that permission to what you granted |
| A note that the sign-in has not finished | A sign-in was started and never came back | Retry as described in [Add an MCP server to your sessions](../integrations/mcp-servers.md#the-sign-in-never-finishes) |

## An app connected from outside shows no tools

In ⚙ Settings → MCP tokens & apps, open **"Recent calls"** for that app under **"Connected apps"**.
Check the HTTP status of its requests and how many tools `tools/list` returned.

- If the list looks empty, refresh it
- If needed, connect again from the app

The details are in [Using your workspace from outside](../../member/19-outside-access.md#recent-calls-when-an-app-connects-but-shows-no-tools).

## Jira says boards, sprints and epics are "not available"

The OAuth connection lacks the Jira Software scopes. The Jira card in ⚙ Settings → Issue tracker says **"Boards, sprints and epics: not available. …"**, and `jira init` and the board commands fail with an authorization error.

1. Ask your tenant administrator to add the scopes to the Atlassian app and tick the Jira Software option on the tenant's Jira card
2. Then reconnect Jira once

Retrying `jira init` before that changes nothing. Viewing, listing, creating and commenting on issues do not need these scopes ([Use Jira](../integrations/jira.md#use-boards-sprints-and-epics)).

## `jira issue create` or `comment add` exited with status 124

The wrapper cut the command off at its time limit. **The issue or comment may already exist**, so do not run it again straight away.
Check in Jira first, and create it again only if it is not there. How to check is in [Use Jira](../integrations/jira.md#a-write-exited-with-status-124).

## `jira` exited with status 3

`jira issue create` or `jira issue edit -b` could not set the description, and nothing else failed.
After a `create` the issue exists, so do not create it again. Set the description with `jira issue edit` ([Use Jira](../integrations/jira.md#creating-an-issue-exited-with-status-3)).

## An AWS command says "Unable to locate credentials"

The command found no credentials of yours. The workspace's own permissions are not handed to your shell.
Do not configure credentials: use a profile from ⚙ Settings and run the command through `af-aws-exec`.

```
af-aws-exec --profile <name> --account <id> -- <command>
```

The fix is in [Use AWS SSO](../integrations/aws-sso.md#unable-to-locate-credentials), and every option is in [af-aws-exec](../../ref/cli/af-aws-exec.md).

## `af-gcloud-exec` exited with status 3

Status 3 means a login only when the wrapper says so ("the login was requested in the Agent Fleet Console …", "Google Cloud login required …").
Log in from the toast or ⚙ Settings, or run the terminal command it printed, then run the command again.
Otherwise the status is the command's own: read that command's error ([Use Google Cloud](../integrations/gcloud.md#when-it-goes-wrong)).

## Related

- [Add an MCP server to your sessions](../integrations/mcp-servers.md)
- [Use Jira](../integrations/jira.md)
- [Use AWS SSO](../integrations/aws-sso.md)
- [Use Google Cloud](../integrations/gcloud.md)

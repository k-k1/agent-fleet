---
audience: "anyone looking up the af- commands in a workspace"
source_of_truth: "each command's --help"
---

# Commands

English | [日本語](README.ja.md)

A workspace comes with Agent Fleet's own commands. Each works from a terminal and for agents alike, and prints its
usage with `--help`.

| Command | What it does |
|---|---|
| [af-aws-exec](af-aws-exec.md) | Run one command with the permissions of an AWS profile |
| [af-gcloud-exec](af-gcloud-exec.md) | Run one command with a token of a Google Cloud profile |
| [af-watch-hook](af-watch-hook.md) | Add a webhook to a private repository so watches are poked |

Pages for the other commands (`af-db`, `af-memory`, `af-scratch`, `af-arch-repair`) are added one
by one.

## CLIs that are already signed in

These commands use a connection you made in Settings, in a session or a terminal, for every agent. You never log
them in from the terminal, and an expiring token is renewed on use.

| Command | Connection it uses | Details |
|---|---|---|
| `git` | **Git hosting** (GitHub, Bitbucket) | [Connect a Git service](../../user/code/connect-git.md) |
| `gh` | **Git hosting**, GitHub | [Use the GitHub CLI](../../user/integrations/github-cli.md) |
| `bkt` | **Git hosting**, Bitbucket Cloud (bitbucket.org) | [Use the Bitbucket CLI](../../user/integrations/bitbucket-cli.md) |
| `jira` | **Issue tracker**, Jira | [Use Jira](../../user/integrations/jira.md) |
| `confluence` | **Issue tracker**, Jira (the same Atlassian connection) | [Use Confluence](../../user/integrations/confluence.md) |
| `af-aws-exec --profile <name> -- <command>` | **AWS profiles/SSM**, after you log in to the profile | [af-aws-exec](af-aws-exec.md) |
| `af-gcloud-exec --profile <name> -- <command>` | **Google Cloud**, after you log in to the profile | [af-gcloud-exec](af-gcloud-exec.md) |

Agents are told to use `bkt` rather than an MCP server for Bitbucket, and `jira` by default for Jira. A CLI reads
your saved connection on every call, so unlike an MCP connector it never needs the session restarted after a login
expires.
To have agents prefer an Atlassian MCP tool for Jira, see [Use Jira](../../user/integrations/jira.md#have-agents-use-the-atlassian-mcp-tool).

---
audience: "anyone running agents from the Console"
source_of_truth: "the Console itself (when the screen and this guide disagree, the screen is right)"
---

# Find what you want to do

English | [日本語](README.ja.md)

Each page covers one thing you want to do. The chapters of the current guide are being rewritten into these pages.
Anything not here yet is in the chapters of [Using Agent Fleet](../member/README.md).

## Integrations

| Page | What you can do |
|---|---|
| [Use AWS SSO](integrations/aws-sso.md) | Have agents run AWS commands with your permissions |
| [Log in to EC2 with SSM](integrations/aws-ssm.md) | Log in to an EC2 host from a terminal in your browser |
| [Use Google Cloud](integrations/gcloud.md) | Have agents run Google Cloud commands with your permissions |
| [Add an MCP server to your sessions](integrations/mcp-servers.md) | Let agents use an in-house tool or an outside service as an MCP server |
| [Connect an issue tracker](integrations/issue-tracker.md) | List your Jira issues and GitHub issues in the left rail and start sessions from them |
| [Use Jira](integrations/jira.md) | Have agents read, file and update Jira issues |
| [Use Confluence](integrations/confluence.md) | Have agents find and write Confluence pages |

## Workspace

| Page | What you can do |
|---|---|
| [Recreate the workspace](workspace/recreate.md) | Rebuild a broken environment from the latest image |
| [See your machine and its usage](workspace/machine.md) | Check the machine's size and how memory and disk are used, and free disk space |
| [Choose your toolchains](workspace/toolchains.md) | Pick the Node.js, Java and Go versions and the time zone |
| [Run database-backed tests](workspace/databases.md) | Run tests against PostgreSQL and MySQL inside the workspace |
| [Try AWS code against a local emulator](workspace/aws-emulator.md) | Try AWS code without touching a real account |
| [Export and import your settings](workspace/export-import.md) | Take your settings to another environment or account |
| [See running time and cloud cost](workspace/cost.md) | See when the workspace was running and what it costs |

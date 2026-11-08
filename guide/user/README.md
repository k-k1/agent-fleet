---
audience: "anyone running agents from the Console"
source_of_truth: "the Console itself (when the screen and this guide disagree, the screen is right)"
---

# Find what you want to do

English | [日本語](README.ja.md)

Each page covers one thing you want to do. The chapters of the current guide are being rewritten into these pages.
Anything not here yet is in the chapters of [Using Agent Fleet](../member/README.md).

## Agents

| Page | What you can do |
|---|---|
| [Choose an agent](agents/choose.md) | Pick and connect the agents that match your subscriptions, and set default and excluded models |
| [Connect Claude](agents/claude.md) | Run Claude Code in sessions, and use Remote control |
| [Connect Codex](agents/codex.md) | Run Codex on a ChatGPT subscription or an API key |
| [Connect OpenCode](agents/opencode.md) | Switch between models from many providers with OpenCode |
| [Connect GitHub Copilot](agents/copilot.md) | Run the Copilot CLI on your GitHub connection alone |
| [Connect Cursor](agents/cursor.md) | Run the Cursor CLI on your Cursor account |
| [Connect Kiro](agents/kiro.md) | Install Kiro and run it on a Builder ID or similar |
| [Connect Antigravity](agents/agy.md) | Run the Antigravity CLI on a Google account (experimental) |
| [Connect Muse Code](agents/muse.md) | Install Muse Code and run it on a Meta account or an API key |
| [Use lcpp](agents/lcpp.md) | Run sessions on the deployment's llama.cpp engine or your own llama-server |
| [Check prices and usage](agents/usage.md) | See model list prices, what is left of your allowance and what your tokens went on; save with RTK |
| [Write down how you work, once](agents/instructions.md) | Write your standing requests once so that every agent gets them |
| [Bring your Claude Code setup](agents/bring-claude-setup.md) | Use your own skills, subagents and hooks in the workspace |

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
| [Use the GitHub CLI](integrations/github-cli.md) | Have agents create and read GitHub pull requests with `gh` |
| [Use the Bitbucket CLI](integrations/bitbucket-cli.md) | Have agents create and read Bitbucket pull requests and check pipelines with `bkt` |
| [Connect Discord and Slack](integrations/chat-bridge.md) | Get session notifications in Discord or Slack, and answer by replying or with buttons |
| [Connect monitoring tools](integrations/ops-monitoring.md) | Have the assistant help sort out an incident while it looks at PagerDuty, Grafana, CloudWatch and AWS |

## Images

| Page | What you can do |
|---|---|
| [Use the image gallery](images/gallery.md) | Look through a folder's pictures as cards |
| [Generate images](images/generate.md) | Make pictures in bulk on the deployment's engine, without an agent |
| [Finish an image in the studio](images/studio.md) | Refine an image-generation draft by talking to an agent |
| [Have a session make images](images/from-session.md) | Have an agent make a picture during a conversation |

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

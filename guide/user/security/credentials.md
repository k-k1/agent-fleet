---
audience: "anyone who wants to handle tokens and sign-ins safely in their workspace"
---

# Handle credentials safely

English | [日本語](credentials.ja.md)

Where to keep tokens, API keys and sign-ins so that agents can use them safely.
Once each has its place, no secret slips into a prompt or a repository.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Register connections to services in **⚙Settings** | Keeps the values inside your workspace and lets commands such as `git`, `gh`, `jira` and `af-aws-exec` use them |
| Keep secrets out of prompts, instructions and repositories | Leaves connections out of the file it exports your settings to |
| Use tokens for outside clients only in clients on your own computer, never in the workspace | Refuses to register this deployment's own `/mcp` for a session |
| Remove or revoke connections you no longer need | Stops using a removed connection from the next call |

## Decide where each one lives

Each kind of credential has its own Settings tab. Agents take it from there, so you never add it to a command.

| Credential | Where it lives | More |
|---|---|---|
| GitHub and Bitbucket tokens | ⚙Settings → "Git hosting" | [Connect a Git service](../code/connect-git.md) |
| Agent sign-ins and API keys | ⚙Settings → "Agents" | [Choose an agent](../agents/choose.md) |
| Jira and Confluence | ⚙Settings → "Issue tracker" | [Use Jira](../integrations/jira.md) |
| AWS | An SSO profile in ⚙Settings → "AWS profiles/SSM" | [Use AWS SSO](../integrations/aws-sso.md) |
| Google Cloud | ⚙Settings → "Google Cloud" | [Use Google Cloud](../integrations/gcloud.md) |
| MCP server tokens and headers | "Authentication" in ⚙Settings → "MCP servers (used by sessions)" | [Add an MCP server to your sessions](../integrations/mcp-servers.md) |

- MCP server values are stored encrypted and never shown on screen again. A URL with a username and password embedded in it is refused
- AWS and Google Cloud keep no long-lived keys. Agents run each command through `af-aws-exec` or `af-gcloud-exec`, with short-lived access from your sign-in
- An MCP server you signed in to with OAuth is called through a relay inside the workspace. Its token is never written to an agent's configuration

## Where never to write a secret

These are plain text that agents and people read as they are. Keep tokens and passwords out of them.

- Prompts to sessions and to the assistant
- [Instructions for agents](../agents/instructions.md), and a repository's `CLAUDE.md` and `AGENTS.md`
- Files in a repository (`.env`, a `.mcp.json` at the project root, and so on). Committed by accident, it stays in the history
- Agent memory, memos and inbox messages

When you need a key for a service that has no connection screen, keep it in the workspace, in a file outside every repository.

## Know what agents can reach

An agent runs inside the workspace as the same user as you. So whatever services you connected, the agent reaches with your
authority too. Assume that instructions planted in a file the agent reads, or in a dependency it installs, can act with that same authority.

- Create tokens with only the permissions the work needs
- Turn on "**Confirm before use**" for a production AWS profile, and add `--account` to commands ([Use AWS SSO](../integrations/aws-sso.md#use-it-more-safely))
- Remove connections you no longer use

## Watch what leaves the workspace

| When you | What happens |
|---|---|
| [Share a conversation](../team/share.md) | The people you share with see the whole conversation, prompts, answers and tool output included. Unsharing does not take back what they saved |
| Send through the inbox | The body is stored encrypted when the deployment has a tenant key. Operators can decrypt it |
| Send full text to Discord or Slack | Strings that look like secrets are masked automatically. Even so, use it only when the chat server is your own ([Connect Discord and Slack](../integrations/chat-bridge.md)) |
| Export your settings | Connections are left out. You can hand the file to someone else ([Export and import your settings](../workspace/export-import.md)) |

## Keep Console tokens and cookies out of the workspace

A token issued in "**MCP tokens & apps**" drives your workspace with your full member authority.
The Console's cookie is you, signed in.

- Put neither in a prompt to a session or in a file in the workspace. Agent Fleet cannot detect it if you do
- Use tokens only in clients outside the workspace, such as Claude Code or Claude Desktop on your own computer
- Why, and which connections to avoid: [Do not connect this deployment's /mcp to a session](../outside/external-claude.md#do-not-connect-this-deployments-mcp-to-a-session)

## When something may have leaked

1. For a Console token, press "**Revoke**" in "**MCP tokens & apps**". For an OAuth connection, press "**Revoke**" on its row under "**Connected apps**"
2. For a connection to a service, invalidate the token or key on that service's side, then connect again in ⚙Settings
3. If you shared the conversation, stop sharing it
4. Tell your administrator

## Related

- [Drive your workspace from an external Claude](../outside/external-claude.md)
- [Connect a remote MCP client by signing in](../outside/oauth-client.md)
- [Bring your Claude Code setup](../agents/bring-claude-setup.md): bring no credentials; keep them under ⚙Settings → "Connections"

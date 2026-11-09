---
audience: "anyone using the workspace from an external agent or app"
---

# 19. Using your workspace from outside — MCP and OAuth

English | [日本語](19-outside-access.ja.md)

This chapter has moved to the pages below.

| What you want to do | Page |
|---|---|
| Drive your workspace's sessions from your own Claude Code or Claude Desktop with a token | [Drive your workspace from an external Claude](../user/outside/external-claude.md) |
| Connect claude.ai, ChatGPT, Claude Code or Codex by signing in, without a token | [Connect a remote MCP client by signing in](../user/outside/oauth-client.md) |
| Carry on in a workspace session with work you started in your own Claude Code | [Move work from your own Claude Code into the workspace](../user/outside/move-work-in.md) |

Using Agent Fleet memory from an external agent is still described below.

## Using memory from an external agent

### A memory-only connector (for claude.ai)

claude.ai hands every connector you add there to all the Claude Code sessions of your account, and that includes
the sessions running in this Workspace. A claude.ai connector to `/mcp` would therefore give those sessions your
full member authority (see [Do not connect this deployment's /mcp to a session](../user/outside/external-claude.md#do-not-connect-this-deployments-mcp-to-a-session)). A connector to **`/mcp/memory`** does not:

- A token or OAuth connection made for `/mcp/memory` lists and runs the seven memory tools (`memory_projects`,
  `memory_index`, `memory_search`, `memory_read`, `memory_save`, `memory_patch`, `memory_forget`) and nothing else. `/mcp`
  refuses it, and `/mcp/memory` refuses a `/mcp` token or connection, so switching means connecting again.
- Your Workspace sessions already reach the same memory through their built-in af tools, so a copy of this
  connector inside a session adds nothing they did not have.
- The consent page says the app is asking for your memory only. Your memory setting, your tenant administrator's
  block, the `read` / `write` scope and the audit log apply exactly as on `/mcp`
  ([18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent)).

A claude.ai connector you already have on `/mcp` keeps working. To move it, remove it in claude.ai and add a new
one with the `/mcp/memory` address. Keep `/mcp` for a client that runs only on your own computer, such as Claude
Desktop or a command-line client, and needs the session tools.

### Using Agent Fleet memory from an external agent

Your own Claude Code or Claude Desktop can use Agent Fleet memory through the same MCP token, and claude.ai through
a [memory-only connector](#a-memory-only-connector-for-claudeai). See
[18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent).

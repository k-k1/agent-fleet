---
audience: "anyone using the workspace from an external agent or app"
updated: "2026-10"
---

# 19. Using your workspace from outside — MCP and OAuth

English | [日本語](19-outside-access.ja.md)

This chapter is for using your workspace from outside the Console: driving it from an external Claude or another MCP client, connecting a remote MCP client by signing in (OAuth), and using Agent Fleet memory from an external agent.

## Using your workspace from an external agent (MCP)

Your workspace answers MCP at one address, `https://<your Console address>/mcp`. An MCP client on your own
computer connects there and works with your sessions, and with Agent Fleet memory if you allow it. Every call is
checked against your role at that moment.

### Driving your workspace from an external Claude (MCP)

From Claude Code / Claude Desktop on your local PC you can **remotely drive** sessions in your workspace.
Think "from my own Claude while I'm out, check on the session running in the company workspace and send it the
next instruction". Issue the token for this in **⚙ Settings → the "MCP tokens" tab**.

1. Choose a **name** (e.g. `laptop-claude`), a **scope**, and an **expiry**, then press **"Issue token"**.
   - Scopes are **read (view only)** / **write (drive sessions; the default)** / **admin:dangerous (elevated / admin)**. You cannot pick a scope beyond your own permissions. If all you want is to drive sessions remotely, write is enough.
   - Expiry is 90 days (default) / 30 days / 365 days / no expiry.
2. On issue you'll see **"Token issued (you can't see it again once you close this)."**: **the token is shown
   only this once**. Save it with "Copy token".
3. The same screen also shows a **`.mcp.json`** template for your local Claude Code. Copy it with
   "Copy .mcp.json" and save it at the project root (or add `agent-fleet` to an existing file). The endpoint is
   `/mcp`, with `Authorization: Bearer <token>` in the header.

**What the session sees.** A prompt sent with `send_to_session` does not look like your own typing. The session
receives it behind a one-line header, `[agent-fleet:mcp via=<connection name> connection=<id> reply=none]`,
and the Console shows that message as its own bubble with a **"Via MCP: <name>"** badge. The name is the
connection's: the app name of an OAuth connection or the name you gave the token. It names the connection,
not a conversation, because MCP clients do not send a session id. The agent treats the message like one from
another session: as a teammate's request that it carries out within its own permissions, but never as your
approval. It will not answer a question card, a permission prompt or a plan approval on your behalf, will not
change permissions or configuration because the message asked, and will not run commands just because they
are quoted in it. A prompt that itself begins with `[agent-fleet:` is refused. Raw terminal sessions
(shell / SSM) refuse input over MCP, so an outside assistant can never type into them.

Tokens you no longer need can be **revoked** ("Revoke") on the same screen (once revoked, connections using
that token are rejected from the next attempt).

### Connecting a remote MCP client by signing in (OAuth)

Where your administrator has turned it on, claude.ai, ChatGPT, Claude Code and Codex can connect to your workspace
**without a token**: you sign in to the Console, look at what the app asks for, and allow it. If the deployment has
not turned it on, the client asks for a token as before and the section above applies.

1. In the client, add a custom connector / remote MCP server whose address is `https://<your Console address>/mcp`.
   For claude.ai and ChatGPT that is the same address you would have used with a token; leave any client ID and
   secret fields empty.
2. The client sends you to the Console. Sign in if you are not already, and the **consent page** opens. It shows:
   - **Where you are sent back to**, in large type: `claude.ai`, `chatgpt.com`, or "this computer itself" for a
     command-line client. Allow only what you started yourself.
   - **The app name**, marked as declared by the app itself. It is free text the app chose, so it proves nothing;
     trust the address above it.
   - **The tenant** to connect, one choice per tenant you belong to. The connection is fixed to it, as a token is.
   - **The scope**: `read` (see sessions and output) or `write` (read, plus act). You can choose less than the app
     asked for. `read` starts selected; choose `write` explicitly if the app requested it.
     The scopes also reach your Agent Fleet memory: `read` lets the app list, search and read it, and `write` also
     lets it save and forget memories. That applies only where your tenant administrator has not blocked External
     memory access and you have turned on "Use Agent Fleet memory from outside Agent Fleet" yourself (off by
     default); see [14 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent). The memory
     text then reaches that app's model.
     `admin:dangerous` is never granted this way; use a token for that.
3. Press **Allow** (or **Cancel**, which tells the app you declined). The client is connected. It keeps itself
   signed in with a short-lived access token (1 hour) that it renews on its own; you are not asked again.

A connection ends when it has not been used for **30 days**, or **90 days** after you allowed it, whichever comes
first; the app then asks you to sign in and allow it again. Using the connection from a different tenant, or after
you left a tenant, is rejected from the next call. Everything the connection does is checked against your role at
that moment, exactly as with a token, and appears in the audit log as coming from MCP.

Only addresses on the deployment's approved list can receive the sign-in: `claude.ai`, `claude.com` and
`chatgpt.com` by default, plus a command-line client on your own computer (`127.0.0.1`, `localhost`). A client whose
callback is anywhere else is refused with an error page and is never sent your code.

#### Connected apps

Open **Settings → Connections → MCP tokens** and look below your Personal Access Tokens for **Connected apps**. Each OAuth connection shows the app name (self-declared, unverified), redirect host, tenant, scope, connection date and last use. Choose **Revoke** and confirm to end that connection immediately, including its refresh token. Reconnect from the app if you want to use it again. The section appears only when this deployment serves MCP OAuth.

Your tenant administrator can disable MCP OAuth for the tenant. This revokes its existing OAuth connections and prevents new ones. Personal Access Tokens continue to work.

#### Recent calls (when an app connects but shows no tools)

Under each connected app, and under each Personal Access Token, choose **Recent calls** to see the last 50 requests made with it: time, HTTP method and status, the JSON-RPC methods (each item of a batch), the protocol era and version, the client name, which `Mcp-Method` / `Mcp-Name` headers were sent, the error returned and, for `tools/list`, how many tools came back. Tokens, authorization headers, tool arguments and results are never recorded.

The list is kept in memory by the Control Plane only. It disappears when the Control Plane restarts, and with several Control Plane replicas each one keeps its own list, so you see only the requests that replica served — press **Refresh** a few times, or ask your operator to read the Control Plane log, where every request (including those refused before sign-in) is one `mcp-call` line.

### Using Agent Fleet memory from an external agent

Your own Claude Code or Claude Desktop can use Agent Fleet memory through the same MCP token. See
[18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent).

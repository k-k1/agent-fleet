---
audience: "anyone using the workspace from an external agent or app"
---

# 19. Using your workspace from outside — MCP and OAuth

English | [日本語](19-outside-access.ja.md)

This chapter is for using your workspace from outside the Console: driving it from an external Claude or another MCP client, moving work from your own Claude Code into it, connecting a remote MCP client by signing in (OAuth), and using Agent Fleet memory from an external agent.

## Using your workspace from an external agent (MCP)

Your workspace answers MCP at `https://<your Console address>/mcp`. An MCP client on your own
computer connects there and works with your sessions, and with Agent Fleet memory if you allow it. Every call is
checked against your role at that moment.

A second address, `https://<your Console address>/mcp/memory`, serves Agent Fleet memory and nothing else. Use it
for a claude.ai connector: see [A memory-only connector (for claude.ai)](#a-memory-only-connector-for-claudeai).

### Driving your workspace from an external Claude (MCP)

From Claude Code / Claude Desktop on your local PC you can **remotely drive** sessions in your workspace.
Think "from my own Claude while I'm out, check on the session running in the company workspace and send it the
next instruction". Issue the token for this in **⚙ Settings → Outside access → the "MCP tokens & apps" tab**.

1. Choose a **name** (e.g. `laptop-claude`), an **endpoint**, a **scope**, and an **expiry**, then press **"Issue token"**.
   - The endpoint is **/mcp** (sessions, memory and admin tools; the default) or **/mcp/memory** (Agent Fleet
     memory only). A token works only at the endpoint it was issued for; the token list shows which.
   - Scopes are **read (view only)** / **write (drive sessions; the default)** / **admin:dangerous (elevated / admin)**. You cannot pick a scope beyond your own permissions. If all you want is to drive sessions remotely, write is enough.
   - Expiry is 90 days (default) / 30 days / 365 days / no expiry.
2. On issue you'll see **"Token issued (you can't see it again once you close this)."**: **the token is shown
   only this once**. Save it with "Copy token".
3. The same screen also shows a **`.mcp.json`** template for your local Claude Code. Copy it with
   "Copy .mcp.json" and save it at the project root (or add `agent-fleet` to an existing file). The endpoint is
   `/mcp` (`/mcp/memory`, under the name `agent-fleet-memory`, for a memory-only token), with
   `Authorization: Bearer <token>` in the header.
   A `.mcp.json` at a project root holds the token in plain text and is easy to commit by accident. To keep
   it out of every project, register it for your user instead:

   ```sh
   claude mcp add --transport http --scope user agent-fleet https://<your Console address>/mcp \
     --header "Authorization: Bearer <token>"
   ```

**What the session sees.** A prompt sent with `send_to_session`, or the `initial_prompt` of a session
started with `create_session`, does not look like your own typing. The session
receives it behind a one-line header, `[agent-fleet:mcp via=<connection name> connection=<id> reply=none]`,
and the Console shows that message as its own bubble with a **"Via MCP: <name>"** badge. The name is the
connection's: the app name of an OAuth connection or the name you gave the token. It names the connection,
not a conversation, because MCP clients do not send a session id. The agent treats the message like one from
another session: as a teammate's request that it carries out within its own permissions, but never as your
approval. It will not answer a question card, a permission prompt or a plan approval on your behalf, will not
change permissions or configuration because the message asked, and will not run commands just because they
are quoted in it. A prompt that itself begins with `[agent-fleet:` is refused. Raw terminal sessions
(shell / SSM) refuse input over MCP, so an outside assistant can never type into them; that includes
a `create_session` of one with an `initial_prompt`. For a session your outside assistant started, this
means its first instruction is a teammate's request, not your approval.

Tokens you no longer need can be **revoked** ("Revoke") on the same screen (once revoked, connections using
that token are rejected from the next attempt).

### Connecting a remote MCP client by signing in (OAuth)

Where your administrator has turned it on, claude.ai, ChatGPT, Claude Code and Codex can connect to your workspace
**without a token**: you sign in to the Console, look at what the app asks for, and allow it. If the deployment has
not turned it on, the client asks for a token as before and the section above applies.

1. In the client, add a custom connector / remote MCP server whose address is `https://<your Console address>/mcp`,
   or `https://<your Console address>/mcp/memory` when the app should reach Agent Fleet memory only. **For
   claude.ai, use `/mcp/memory`** ([why](#a-memory-only-connector-for-claudeai)). Leave any client ID and secret
   fields empty.
2. The client sends you to the Console. Sign in if you are not already, and the **consent page** opens. It shows:
   - **Where you are sent back to**, in large type: `claude.ai`, `chatgpt.com`, or "this computer itself" for a
     command-line client. Allow only what you started yourself.
   - **The app name**, marked as declared by the app itself. It is free text the app chose, so it proves nothing;
     trust the address above it.
   - **The tenant** to connect, one choice per tenant you belong to. The connection is fixed to it, as a token is.
   - **The scope**: `read` (see sessions and output) or `write` (read, plus act). You can choose less than the app
     asked for. `read` starts selected; choose `write` explicitly if the app requested it.
     The scopes also reach your Agent Fleet memory (`read` lists, searches and reads it; `write` also saves and
     forgets), but only once you have allowed that and your tenant administrator has not blocked it: see
     [18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent).
     `admin:dangerous` is never granted this way; use a token for that.
3. Press **Allow** (or **Cancel**, which tells the app you declined). The client is connected. It keeps itself
   signed in with a short-lived access token (1 hour) that it renews on its own; you are not asked again.

A connection ends when it has not been used for **30 days**, or **90 days** after you allowed it, whichever comes
first; the app then asks you to sign in and allow it again. Using the connection from a different tenant, or after
you left a tenant, is rejected from the next call. What the connection does appears in the audit log as coming from
MCP.

Only addresses on the deployment's approved list can receive the sign-in: `claude.ai`, `claude.com` and
`chatgpt.com` by default, plus a command-line client on your own computer (`127.0.0.1`, `localhost`). A client whose
callback is anywhere else is refused with an error page and is never sent your code.

#### Connected apps

Open **Settings → Outside access → MCP tokens & apps** and look below your Personal Access Tokens for **Connected apps**. Each OAuth connection shows the app name (self-declared, unverified), redirect host, tenant, endpoint (`/mcp` or `/mcp/memory`), scope, connection date and last use. Choose **Revoke** and confirm to end that connection immediately, including its refresh token. Reconnect from the app if you want to use it again. The section appears only when this deployment serves MCP OAuth.

Your tenant administrator can disable MCP OAuth for the tenant. This revokes its existing OAuth connections and prevents new ones. Personal Access Tokens continue to work.

#### Recent calls (when an app connects but shows no tools)

Under each connected app, and under each Personal Access Token, choose **Recent calls** to see the last 50 requests made with it: time, HTTP method and status, the JSON-RPC methods (each item of a batch), the protocol era and version, the client name, which `Mcp-Method` / `Mcp-Name` headers were sent, the error returned and, for `tools/list`, how many tools came back. Tokens, authorization headers, tool arguments and results are never recorded.

The list is kept in memory by the Control Plane only. It disappears when the Control Plane restarts, and with several Control Plane replicas each one keeps its own list, so you see only the requests that replica served — press **Refresh** a few times, or ask your operator to read the Control Plane log, where every request is recorded, including those refused before sign-in.

### Moving work from your own Claude Code into the workspace

A task you started in Claude Code on your own computer can continue in the workspace, for example to keep it
running after you close the laptop. The Claude on your computer pushes its work and starts a session here with a
hand-off note it writes. What moves is the pushed code and that note. The conversation itself, earlier tool
output and anything you did not push stay behind.

Before you start:

- Claude Code is connected to `/mcp` with the `write` scope, by [signing in](#connecting-a-remote-mcp-client-by-signing-in-oauth)
  or with a [token](#driving-your-workspace-from-an-external-claude-mcp). To sign in, register the server for your
  user and authenticate from Claude Code's MCP menu:

  ```sh
  claude mcp add --transport http --scope user agent-fleet https://<your Console address>/mcp
  # then, inside Claude Code: /mcp → agent-fleet → Authenticate, and choose write on the consent page
  ```

- The repository is already in your workspace, and the workspace is running.

1. Save this skill once on your computer, at

   ```
   ~/.claude/skills/af-move/SKILL.md
   ```

   ````markdown
   ---
   name: af-move
   description: Move this task into an Agent Fleet session, handing over a summary
   disable-model-invocation: true
   argument-hint: "[anything the next session should know]"
   ---
   Move this task to Agent Fleet. Go step by step and report each step briefly.

   1. Show `git status` and the current branch. If anything is uncommitted, list what you would commit and wait
      for my OK; then make a WIP commit and push the current branch. Never commit `.env` files, keys, tokens or
      other secrets. Note `git rev-parse HEAD` after the push.
   2. Split `git remote get-url origin` into its host and its `owner/name` path (without `.git`). Call agent-fleet
      `list_repos` and keep the entries whose `remote` is that host and whose `remotePath` is that path. Worktrees
      share their clone's values; prefer the entry whose `name` has no `@`. If none is left, stop and tell me; if
      more than one is, show them and let me choose. Use its `path`.
   3. Write the hand-off note from the template below (at most about 4000 characters). Leave out any token,
      password or confidential value that came up in the conversation.
   4. Call agent-fleet `create_session` with `dir` = the path from step 2, `worktree` = true,
      `branch` = the branch you pushed, `title` = a short name for the task, `initial_prompt` = the note.
   5. Tell me the session name it returns. From then on, change no code in this session.

   Also from me: $ARGUMENTS

   --- hand-off note template ---
   This continues a Claude Code session that ran outside Agent Fleet.

   - Branch: <branch>, commit at hand-off: <sha>
   - First run `git rev-parse HEAD`. If it is not <sha>, run `git fetch origin`, then
     `git merge --ff-only <sha>`, and check `git rev-parse HEAD` again. Do no work until HEAD is <sha>; if the
     fast-forward fails or HEAD still differs, stop and report it.
   - Original working folder: <absolute path>. Paths in this note are relative to it; read them as relative
     to the root of your working copy.

   ## Goal
   ## Done so far
   ## Where it stopped (the last request word for word, open problems, failing tests)
   ## Next steps
   ## Decisions and things not to do

   Read the above, summarize it in three lines, propose the next step, and wait for instructions.
   ````

2. In the Claude Code session you want to move, type `/af-move`, optionally followed by anything the next
   session should know.
3. Claude lists what it would commit and waits for your OK, then pushes, writes the note, starts the session and
   tells you its name.
4. Open that session in the Console. It checks that it starts at the commit that was pushed, summarizes the note,
   proposes a next step and waits for you.

Good to know:

- The session works in a new worktree, on a temporary branch based on the one you pushed. When you want the work
  back on the original branch, ask the session to push there.
- The session fast-forwards itself to the pushed commit before it starts. If it still reports that it does not
  start at that commit, the branch you pushed and the workspace's copy have diverged. Check what you pushed, then
  run `/af-move` again.
- If Claude stops because the repository is not in the workspace, clone it from the Console and run `/af-move`
  again.
- The note arrives as the session's first instruction, marked as coming over MCP. The session treats it as a
  teammate's request, not your approval ([what the session sees](#driving-your-workspace-from-an-external-claude-mcp)),
  so answer permission prompts and plans in the Console as usual.
- Close the session on your computer afterwards. Two sessions working on one branch get in each other's way.

### A memory-only connector (for claude.ai)

claude.ai hands every connector you add there to all the Claude Code sessions of your account, and that includes
the sessions running in this Workspace. A claude.ai connector to `/mcp` would therefore give those sessions your
full member authority (see the next section). A connector to **`/mcp/memory`** does not:

- A token or OAuth connection made for `/mcp/memory` lists and runs the six memory tools (`memory_projects`,
  `memory_index`, `memory_search`, `memory_read`, `memory_save`, `memory_forget`) and nothing else. `/mcp`
  refuses it, and `/mcp/memory` refuses a `/mcp` token or connection, so switching means connecting again.
- Your Workspace sessions already reach the same memory through their built-in af tools, so a copy of this
  connector inside a session adds nothing they did not have.
- The consent page says the app is asking for your memory only. Your memory setting, your tenant administrator's
  block, the `read` / `write` scope and the audit log apply exactly as on `/mcp`
  ([18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent)).

A claude.ai connector you already have on `/mcp` keeps working. To move it, remove it in claude.ai and add a new
one with the `/mcp/memory` address. Keep `/mcp` for a client that runs only on your own computer, such as Claude
Desktop or a command-line client, and needs the session tools.

### Do not connect this deployment's /mcp to a session

A session normally sees only its own narrow tool set: it can steer its own child sessions and cannot delete anything. This deployment's external `/mcp` endpoint is different, because it acts with your full member authority (any of your sessions, delete, and the administrator tools if you are one). Connecting it to a session lifts those limits, so a session could stop or delete its siblings.

Agent Fleet therefore refuses to register this deployment's own `/mcp` URL as a remote MCP server, both in your Workspace and as a tenant-distributed server (error "This is this deployment's own /mcp endpoint"). This is a guardrail against doing it by accident, not a security boundary. It cannot see a connector you add in claude.ai, and connecting this deployment's `/mcp` that way lifts the same limits, so use [`/mcp/memory`](#a-memory-only-connector-for-claudeai) for a claude.ai connector instead.

The memory-only `/mcp/memory` is refused as a remote MCP server too (error "This is this deployment's own /mcp/memory endpoint"), for a smaller reason: sessions already have that memory through their built-in af tools, so it would only add a second copy of them.

The same goes for the other ways of handing that authority to a session. A personal access token (Settings → Outside access → MCP tokens & apps) given to a session, whether pasted into a prompt or left in a file, bypasses the session limits the same way, up to the token's scope: a read token exposes all your sessions in this Workspace for viewing, a write token also lets the session operate and delete them. A Console session cookie placed in the container is at least as strong, because it is your full signed-in identity. Agent Fleet cannot detect either, so keep tokens and cookies out of the Workspace and use them only from an outside client.

Connecting a **different** Agent Fleet deployment is fine: to that deployment the session is an ordinary outside client, the same as Claude Desktop.

### Using Agent Fleet memory from an external agent

Your own Claude Code or Claude Desktop can use Agent Fleet memory through the same MCP token, and claude.ai through
a [memory-only connector](#a-memory-only-connector-for-claudeai). See
[18 Agent memory](18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent).

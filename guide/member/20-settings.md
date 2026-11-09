---
audience: "anyone looking for a setting"
---

# 20. Settings — every tab of the ⚙ settings dialog

English | [日本語](20-settings.ja.md)

How each setting is *used* belongs to the other chapters, so read this one as a
**map**.

## Opening it, and how it is organised

**⚙ Settings** is in the account menu of the top bar. The left rail is split into four groups.

| Group | What is in it |
|---|---|
| **Personal** | Display / Account / Keyboard / Read aloud / Notifications / Limit board / Assistant / AI assistance / Agent instructions / Agent memory / Memory snapshots |
| **Connections** | Agents / Git hosting / Ops & monitoring / Issue tracker / Chat / MCP servers (used by sessions) / AWS profiles/SSM / Google Cloud |
| **Outside access** | MCP tokens & apps (personal access tokens and connected apps that drive this Workspace from outside) |
| **Workspace** | Agent usage / Cloud cost / Running time / Machine / Toolchains / Databases / Preview subdomains / Internal repos / Export / import / Danger zone |

- It remembers the tab you opened last and reopens there.
- **On a phone it is a list → detail drill-down.** Back returns to the list; back again closes the dialog.
- Managing your team's **members, quotas and audit** is a **separate dialog**, **Tenant settings** in the same
  menu, shown only to those with the permission ([admin/](../admin/README.md)). **Admin** (the shield icon)
  is the deployment-wide one.
- Some tabs **need the workspace running**, because the setting itself lives inside the workspace. While it is
  stopped you get a note and a "Start workspace" button.

### When a change takes effect

Getting this wrong is what makes a setting look like it "didn't work".

| Timing | What |
|---|---|
| **Immediately** | Display, keys, speech, notifications; adding and removing connections; the stopped-session archive period (on the next session-list refresh) |
| **From the next session you start** | Agent behaviour settings, agent instructions, session-to-session messaging, starting sessions from sessions, past-session search (turning it off also refuses running sessions' searches at once), Agent Fleet memory (likewise), image generation, MCP servers |
| **From the next chat message** | Assistant settings; ops & monitoring connections (when used from an assistant) |
| **After stopping and starting the workspace** | Toolchains (timezone, language versions); Machine (a size or class your admin changed) |

There are also **two storage scopes**. The theme, the surface colours and the main-area layout are stored **on
this device only**; everything else (font, font size, …) is stored on the server and follows you to another PC
or browser. If saving to the server fails, the top of the settings screen says your settings could not be
saved; until then the agents act on the previous settings, so press **Retry**.

---

## Personal

### Display

Language, time zone, theme, fonts and the main-area layout. See [Change the display and your account](../user/settings/display.md).

### Account

Adding and removing sign-in methods. See [Change the display and your account](../user/settings/display.md#add-a-sign-in-method).

### Keyboard

- **Shortcut assignment**: you can change the direct keys (`Alt+1` …) and the three app-wide keys. Sequences
  under the leader (e.g. leader → `p` → `r`) are structural and cannot be changed. **?** opens the cheat sheet
  at any time.
- **Terminal input priority**: while a terminal has focus, every Ctrl-key goes to the terminal. Only the leader
  survives on the app side, and everything remains reachable from the command menu / palette.
- **Pass every key to shell / SSM terminals**: stronger than the above. The leader (Ctrl/⌘+K) and the palette
  (Ctrl/⌘+P) are passed through too, making it a pure terminal. It applies only to shell / SSM, not to agent
  terminals. Off by default.
- **Send key**: **Ctrl+Enter to send (Enter for a newline, the default)** or **Enter to send
  (Shift+Enter for a newline)**. It applies to both session chat and assistant chat.
- **Reply suggestions**: chips with short replies above the composer ([Reply to a session](../user/sessions/reply.md#use-reply-suggestions)). This is also
  where you clear what has been learned or unpin pinned chips. There are two ways to clear: **Clear used-once**
  drops only the throwaway phrasings you sent exactly once (pinned ones stay, and sending one again learns it
  back), while **Clear all learned** wipes both what was learned and the dismissed list, back to the initial state.

### Read aloud

Reads answers out loud. See [Use read-aloud](../user/settings/read-aloud.md).

### Notifications

Choose the unread dot, the OS notification and read-aloud for each kind of notification. See [Get notified](../user/settings/notifications.md).

### Limit board

Choose who in this tenant sees where you stand on usage limits, and how much. See [Show each other where you stand on usage limits](../user/team/usage-limits.md).

### Assistant

Reply language, agent priority, the assistant models, auto-compaction and so on. See [Ask an assistant](../user/chat/assistant.md#choose-the-reply-language-and-the-models). Auto-replies to session reports and autopilot are in [11](11-many-sessions.md#session-reports-and-auto-reply).

### AI assistance

The agents and models used for one-shot AI generation such as titles and reply suggestions, and turning each feature on or off. See [Use translation and purpose-built assistants](../user/chat/purpose-assistants.md#set-up-ai-assistance).

### Agent instructions

Adds your own standing instructions to every agent started afterwards. See [Write down how you work, once](../user/agents/instructions.md).

### Skills

Lists each agent's personal skills, read-only. See [Bring your Claude Code setup](../user/agents/bring-claude-setup.md#check-your-skills).

### Agent memory

Agent Fleet's own shared memory, which every kind of agent reads and writes, with the sessions' switch in
its **Settings** view. The page is described in [18 Agent memory](18-agent-memory.md#agent-memory).

### Memory snapshots

Version control over the memory an agent keeps in its own files. The page is described in
[18 Agent memory](18-agent-memory.md#memory-snapshots).


---

## Connections

### Agents

Each agent's card connects it and sets its behavior. See [Choose an agent](../user/agents/choose.md) and each agent's page.

The
**Sessions** group holds **when stopped sessions are archived** (Default, which is the deployment's period (7 days unless the deployment changed it), 1 / 3 / 7 / 14 / 30 days, or Off), **session-to-session messaging**, **starting sessions from sessions** (with
**children per session**), **past-session search** (on by default: lets a
session search this workspace's past conversations with the `search_sessions` tool; the command palette's
"Conversations" mode works either way, see [02](02-console-basics.md#command-palette)), **image generation** and the **image provider order** (this deployment's own engines first, each under its own name, then the CLI routes).
→ [11 Running many sessions](11-many-sessions.md#messages-between-sessions),
[11 Running many sessions](11-many-sessions.md#starting-sessions-from-a-session-child-sessions),
[Have a session make images](../user/images/from-session.md)

### Git hosting

The GitHub and Bitbucket connections, the commit author, and the saved Subversion credentials. Connecting GitHub
also connects GitHub Copilot. See [Connect a Git service](../user/code/connect-git.md).

### Ops & monitoring

Connects PagerDuty, Grafana, CloudWatch and AWS to the **SRE assistant**. How to connect them, and what the assistant
does with them, is in [Connect monitoring tools](../user/integrations/ops-monitoring.md).

### Issue tracker

The Jira connection behind the left rail's **Issue tracker**. Connecting it and saving queries are in
[Connect an issue tracker](../user/integrations/issue-tracker.md).

### Chat

Connects a Discord or Slack bot and sets which notifications it sends. How to connect it, and how to answer by
replying, is in [Connect Discord and Slack](../user/integrations/chat-bridge.md).

### MCP servers (used by sessions)

The MCP servers your sessions and assistants use: registering one, the three origins, the connection test and the
OAuth sign-in are in [Add an MCP server to your sessions](../user/integrations/mcp-servers.md). To let an outside app
drive this Workspace, see [MCP tokens & apps](#mcp-tokens-apps) under **Outside access**.

### AWS profiles/SSM

Your AWS profiles (shared settings), which `af-aws-exec` and SSM sessions sign in with, and SSM hosts (individual)
for logging in to another in-house host. A profile is **IAM Identity Center (SSO)** or **Assume a role from another
profile**; an SSO row has **Log in** and, when signed in, **Log out**; an assume-role row's **Log out** ends its source's login, after a confirmation that names it. How logging
in and out work, and the two types, is in [Use AWS SSO](../user/integrations/aws-sso.md); SSM hosts are in
[Log in to EC2 with SSM](../user/integrations/aws-ssm.md).

### Google Cloud

Your Google Cloud profiles, which `af-gcloud-exec` runs commands with. Adding, importing and logging in, and the WS
bar's Google Cloud badge, are in [Use Google Cloud](../user/integrations/gcloud.md).

---

## Outside access

### MCP tokens & apps

Personal access tokens and connected apps for using this workspace from outside. Tokens are in [Drive your workspace from an external Claude](../user/outside/external-claude.md), connected apps in [Connect a remote MCP client by signing in](../user/outside/oauth-client.md). To register a server your sessions call, see [Add an MCP server to your sessions](../user/integrations/mcp-servers.md).

## Workspace

### Agent usage

The ledger of what your tokens went on. How to read it is in [Check prices and usage](../user/agents/usage.md#see-what-your-tokens-went-on).

### Cloud cost

The cloud cost attached directly to your workspace (deployments on AWS only). How to read it is in
[See running time and cloud cost](../user/workspace/cost.md#see-the-cloud-cost).

### Running time

When your workspace was running, as a grid of hours. How to read it is in
[See running time and cloud cost](../user/workspace/cost.md#see-the-running-time).

### Machine

The size of the machine your workspace runs on, its memory and disk usage, and how to free disk space:
[See your machine and its usage](../user/workspace/machine.md).

### Toolchains

The time zone, the Node.js / Java / Go versions, tool versions and agent CLI updates:
[Choose your toolchains](../user/workspace/toolchains.md).

### Databases

This workspace's PostgreSQL and MySQL:
[Run database-backed tests](../user/workspace/databases.md#manage-them-from-the-console).

### Preview subdomains

Settings for opening the app you are building at `https://<random>-<port>.<domain>/`. **The tab is only
there on a deployment that issues preview subdomains** (elsewhere no URL would ever be minted, whatever
you set here). Which ports are exposed, pinning the URL, sharing it with your tenant, opening it without a
login, and re-issuing the URL all live here. Where the deployment offers the development OIDC IdP, the tab
also shows its Issuer, Discovery URL and Client ID with **Copy** buttons (the client secret is not shown),
**Issue a new secret…**, **App configuration examples** and **Test personas**.
→ [14 Preview and browser](14-preview-browser.md#preview-subdomains-only-where-they-are-issued)

### Internal repos

Create Git repositories inside the tenant, with no external service. See
[Use the built-in Git](../user/code/internal-git.md).

### Export / import

Your settings in one file, to take to another environment and read back:
[Export and import your settings](../user/workspace/export-import.md).

### Danger zone

Recreating the workspace and cleaning home (on deployments that can). What stays and what goes is in
[Recreate the workspace](../user/workspace/recreate.md).

---

## What you want to do → which tab

| What you want | Tab |
|---|---|
| Text is too small / change the colours | Display |
| Sign in with another method too | Account |
| Send with Enter | Keyboard (send key) |
| Switch panes by tab | Display (main area layout) |
| Rebind a shortcut / pass keys to the terminal | Keyboard |
| Have replies read out loud | Read aloud |
| Silence Slack without disconnecting | Notifications (service notifications) |
| Make the assistant answer in English | Assistant (output language) |
| Let the operator run ahead, or stop it | Assistant (autopilot, auto-reply) |
| Stop AI from writing titles or reply suggestions | AI assistance (per-feature on/off) |
| Run only the assistance on a cheap model | AI assistance (priority, models) |
| I keep typing the same preamble | Agent instructions |
| Sign in to Claude / Codex | Agents |
| Stop a model that bills extra from being picked | Agents (models to exclude) |
| Keep stopped sessions in the list longer, or out of it sooner | Agents (archive stopped sessions after) |
| Let sessions talk to each other | Agents (session-to-session messaging) |
| Stop sessions from searching past conversations | Agents (past-session search) |
| Let agents share what they learn across kinds | Agent memory (Settings) |
| Let a session see what the other sessions are doing | Nothing to set: every session can ([11](11-many-sessions.md#letting-a-session-see-the-fleet)) |
| Have a session leave you a note about what it noticed | Nothing to set: every session can ([11](11-many-sessions.md#letting-a-session-see-the-fleet)) |
| Clone a private repository | Git hosting |
| Get help investigating an incident | Ops & monitoring |
| Start work from a ticket or an issue | Issue tracker |
| Follow progress while away from the desk | Chat |
| Let the AI use an in-house tool | MCP servers (used by sessions) |
| Drive it from the Claude on my laptop | Outside access → MCP tokens & apps |
| Find out what the tokens went on | Agent usage |
| Find out what my workspace costs to run | Cloud cost |
| I want to know when my workspace was actually running | Running time |
| It memorised something wrong | Agent memory (Memories, Changes); Memory snapshots (History) |
| Change the Java / Node version | Toolchains |
| Run a test suite that needs a database | Databases |
| Show the app you are building to someone | Preview subdomains |
| Get into another server | AWS profiles/SSM |
| Run commands in my Google Cloud projects | Google Cloud |
| Keep code that cannot leave the building | Internal repos |
| Take my settings to another environment | Export / import |
| The environment is broken | Danger zone |

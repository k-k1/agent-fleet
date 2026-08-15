# 12. Settings — every tab of the ⚙ settings dialog

English | [日本語](12-settings.ja.md)

> Audience: members who want to find where a setting lives and what an item actually changes. How each setting is
> *used* belongs to the other chapters, so read this one as a **map**. Items with a fuller explanation elsewhere
> link to that chapter.

## Opening it, and how it is organised

**⚙ Settings** in the top bar. The left rail is split into three groups.

| Group | What is in it |
|---|---|
| **Personal** | Display / Account / Keys / Speech / Notifications / Assistant / Agent instructions |
| **Connections** | Agents / Git hosting / Ops & monitoring / Chat integration / MCP servers / MCP tokens |
| **Workspace** | Usage / Agent memory / Toolchain / AWS SSM / Internal repositories / Danger zone |

- It remembers the tab you opened last and reopens there.
- **On a phone it is a list → detail drill-down.** Back returns to the list; back again closes the dialog.
- Managing your team's **members, quotas and audit** is a **separate dialog** (the shield icon), shown only to
  those with the permission ([admin/](../admin/README.md)).
- Some tabs **need the workspace running**, because the setting itself lives inside the workspace. While it is
  stopped you get a note and a "Start workspace" button.

### When a change takes effect

Getting this wrong is what makes a setting look like it "didn't work".

| Timing | What |
|---|---|
| **Immediately** | Display, keys, speech, notifications; adding and removing connections |
| **From the next session you start** | Agent behaviour settings, agent instructions, session-to-session messaging, MCP servers |
| **From the next chat message** | Assistant settings; ops & monitoring connections (when used from an assistant) |
| **After stopping and starting the workspace** | Toolchain (timezone, language versions) |

There are also **two storage scopes**. The theme, the surface colours and the main-area layout are stored **on
this device only**; everything else (font, font size, …) is stored on the server and follows you to another PC
or browser.

---

## Personal

### Display

Everything about appearance.

- **Colour theme** — besides the app itself, you can theme the **session**, **shared session** and **assistant**
  surfaces separately ("inherit" follows the app theme). Different colours per surface make a grid of panes
  readable at a glance.
- **Terminal** — font and font size ([03](03-terminal.md)).
- **File viewer** — tab width, line numbers, wrapping, minimap, Markdown rendering ([05](05-files.md)).
- **Reader view** / **file icons** (icon set).
- **Main area layout** — **split panes** (drag the dividers) or **tabbed grid** (each cell switches by tab).
  Stored **on this device only**, and the two layouts are remembered separately, so moving between them does not
  disturb either ([03](03-terminal.md)). If you switch often, the same choice sits in **Appearance** (the paint
  can) in the top bar.

### Account

The **sign-in methods** linked to your account (Google / Microsoft / GitHub …). Whichever one you use, you
land in the same workspace, the same home and the same settings.

- **Add a sign-in method** — when your company offers more than one (say Microsoft at head office and GitHub
  at the subsidiary you are seconded to), you can add a second one to your account. The button takes you
  through that method's sign-in and back; the list then shows it.
- Only a method that asserts **the same email address as this account** can be added. Accounts under
  different addresses cannot be merged into one (it could not be undone).
- That method's own entry rules still apply — organization membership for GitHub, the allowed email domains.
  Linking is not a way around them.
- A method already used by **somebody else's account** cannot be added.
- If the sign-in page told you "this email address is already used by another sign-in method", the fix is to
  **sign in the way you normally do** and add the other method here.

### Keys

- **Shortcut assignment** — you can change the direct keys (`Alt+1` …) and the three app-wide keys. Sequences
  under the leader (e.g. leader → `p` → `r`) are structural and cannot be changed. **?** opens the cheat sheet
  at any time.
- **Terminal input priority** — while a terminal has focus, every Ctrl-key goes to the terminal. Only the leader
  survives on the app side, and everything remains reachable from the command menu / palette.
- **Pass every key to shell / SSM terminals** — stronger than the above: the leader (Ctrl/⌘+K) and the palette
  (Ctrl/⌘+P) are passed through too, making it a pure terminal. It applies only to shell / SSM, not to agent
  terminals. Off by default.
- **Send key** — **Ctrl+Enter to send (Enter for a newline — the default)** or **Enter to send
  (Shift+Enter for a newline)**. It applies to both session chat and assistant chat.
- **Reply suggestions** — chips with short replies above the composer ([07](07-chat-memo.md)). This is also
  where you clear what has been learned or unpin pinned chips.
- **✨ AI reply suggestions button** — the button that generates suggestions from the recent conversation. On by
  default.

### Speech

Reads out replies from sessions and assistants.

- **Voice** — engine (Zundamon (VOICEVOX) / Polly / auto), speaker, **a different voice per session** (assigned
  from a character pool), emotion, reading speed.
- **Auto-read** — read new replies automatically, **read the work in progress in a quiet voice**, read in every
  open pane, summarise long replies, read out confirmations and questions.
- **How it reads** — abbreviate code fragments, pause after particles, read English as kana, and a
  **pronunciation dictionary** (`written=reading`, one per line).
- **Advanced** — background playback and volume, panning to match the pane position, audio cache.
- **Audio notifications** — announce session state changes and rate-limit resets by voice.
- "Reset to defaults" resets the speech settings only (the pronunciation dictionary is kept).

### Notifications

- **Audio notifications** on / off (the entry point into the speech tab's detail).
- **Service notifications** — stop sending to Discord / Slack **without disconnecting**. The connection itself
  lives in the "Chat integration" tab ([08](08-advanced.md)).
- **Allow desktop notifications** — asks the browser for permission.
- History is in the **notification centre** (last 7 days), opened from the bell in the top bar.

### Assistant

The behaviour of assistant chat and the fleet operator ([07](07-chat-memo.md), [11](11-fleet-operator.md)).

- **Output language** — follow the input / 日本語 / English.
- **AI title suggestions** — whether the rename dialog offers "let AI suggest".
- **Agent priority** — the first connected CLI from the top of this list runs the assistants.
- **Assistant models** / **models for titles and suggestions** — per CLI. "Recommended (currently: …)" picks a
  safe default from the live catalogue and shows what it currently resolves to.
- **Auto-reply to session reports** — the operator takes one turn automatically when a report arrives.
  **Automatic reply limit** (default 10, max 50 — it cannot be unlimited), **model for automatic replies**
  (reading a report is routine work, so a lighter model saves a lot), **batching window** (reports arriving
  within it are handled in one turn), **quiet completion reports** (a normal completion delivers the card and
  the notification but takes no turn).
- **Autopilot** — carries questions and plan approvals through automatically. Off by default
  ([11](11-fleet-operator.md)).
- **Auto-resume after an interruption** — resumes a turn cut short by a dropped connection or a temporary rate
  limit. On by default.
- **Automatic context compaction** and its **threshold** — summarise and hand a long conversation forward.
- **Session output fetch limit** — how much of a session's output the operator reads at once (default 32 KiB).
  The larger it is, the more accumulates in the conversation and the more tokens every later turn costs. The
  full output is always readable in the chat view.
- **Appearance** — theme and background colour of the assistant surface (this device only).

### Agent instructions

Adds your own standing instructions to every agent newly started in this workspace, with a per-target row
showing which file it was written to and whether it is in effect.
See [06 Agents](06-agents.md#agent-instructions-write-down-how-you-work-once).

---

## Connections

### Agents

Connecting and configuring claude / codex / opencode / GitHub Copilot / Cursor / Kiro (and the experimental
Antigravity): default model, **models you don't use**, **extra Claude models**, expanded thinking, RTK. The
**Sessions** group holds automatic title suggestions, **session-to-session messaging**, auto-resume after a rate
limit resets, and auto-resume of an interrupted turn.
→ [06 Agents](06-agents.md), [02 Sessions](02-sessions.md#messages-between-sessions)

### Git hosting

GitHub / Bitbucket (authentication for clone / push). **Connecting GitHub also connects GitHub Copilot.**
→ [04 Repositories and git](04-git.md)

### Ops & monitoring

Connect PagerDuty / Grafana / CloudWatch / AWS so the **SRE assistant** can talk through an incident against
real data. CloudWatch and AWS only need a profile picked from your SSM connections — no secret to type. AWS
**write tools are off by default**. → [10 Ops tooling](10-ops-mcp-poc.md)

### Chat integration

Connect a Discord / Slack bot to follow session progress in chat and drive it by replying.
→ [08 Going further](08-advanced.md#connecting-discord--slack-chat-bridge)

### MCP servers

**Register the MCP servers you want to use here** and they become available to your assistants and sessions.
This is where you add tools Agent Fleet does not ship with — an internal wiki, an issue tracker, a document
search.

- **Transport** — **stdio** (run an executable inside the workspace: command, arguments, environment variables)
  or **remote (HTTP)** (URL and headers). **Environment variable and header values are stored encrypted** and
  handed to the server only when it starts, so they never sit in a config file in the clear. Put credentials in
  a header, not in the URL.
- **Targets** — whether it is handed to **assistants**, **sessions**, or both (clear both and the entry stays
  but goes nowhere). Leave **target agents** empty to cover every agent.
- **Connection test** — reports the server name, version, tool count and round-trip time.
- **Enabled / disabled** — disabling keeps the definition but stops handing it out.
- Sessions pick it up **from the next session you start**. For assistants, choose it in the assistant's own
  edit form under "MCP servers" ([07](07-chat-memo.md)).
- Entries are labelled by origin: **user** (yours), **tenant** (distributed by an admin — a user entry with the
  same name is not used; some are distributed such that only the values are asked of each member), and
  **built-in** (Agent Fleet's own server and the ops & monitoring integrations).
- On a deployment with restricted egress, you get a flow to **request access** for the host and wait for an
  admin to approve it.
- MCP definitions committed in a repository (`.mcp.json` and friends) are shown in **Project settings**, from
  the repository row's menu, with per-agent status and warnings (a secret already under Git, a name clash,
  files that disagree with each other).

> **Agent Fleet's own MCP** is the "built-in" entry handed to every session from the start — no setup needed.
> It is what lets a session report that it finished, or propose a handoff to the next session.

### MCP tokens

Tokens for driving your workspace remotely from Claude Code / Claude Desktop on your own machine.
→ [08 Going further](08-advanced.md#driving-your-workspace-from-an-external-claude-mcp)

---

## Workspace

### Usage

The ledger of **what your tokens went on**. Alongside the sessions themselves, it lists the auxiliary calls
Agent Fleet makes behind the scenes (title suggestions, summarised handoffs, reply suggestions …) on the same
scale.

- **Range** — 24 hours / 7 days / 30 days.
- **Split by** — feature / agent / model / session origin (started by a person, created by the operator, created
  by a schedule, handoff) / trigger (user, automatic, schedule, operator, bridge …).
- **Metric** — tokens spent / number of calls / cache reads / **API-equivalent cost** (what the same work would
  have cost on the API, as a yardstick, even when it ran on a subscription).
- Clicking a series in the time chart filters to it. There is also a **feature × model** matrix.
- **Measurement coverage** is stated explicitly. Calls that do not report tokens are shown as counted-only
  (which does not mean they were free).
- **RTK gain** — cumulative tokens saved, average saving rate and command count, daily / weekly / monthly
  (RTK is in [06](06-agents.md#rtk-token-savings)).

### Agent memory

Version control over the memory an agent accumulates by itself (claude's auto-memory, codex's memories), so
"it learned something it shouldn't have" and "when did this go wrong" are fixable after the fact.

- **Targets** — what can be versioned, with file count, size and the last snapshot. codex has memory disabled by
  default, so enable it here if you want it.
- **Automatic snapshots** — taken a few minutes after an agent stops (nothing is stored if nothing changed).
  "Snapshot now" takes one by hand. On some deployments the operator has disabled automatic snapshots.
- **History** — newest first, with the time and the trigger (automatic / manual / pre-restore / restore /
  import). You can also jump to a point in time by date.
- **Restore to this point** — pick the scope (everything, or select what to restore). **The state just before
  the restore is snapshotted too**, so the restore itself can be undone. You are warned if a session of that
  kind is running.
- **Export / import** — bundle (full history) or tar.gz (latest only). If what you are about to export looks
  like it contains secrets, you are warned and asked to confirm first.

### Toolchain

Timezone, Node.js / Java / Go versions, the table of effective tool versions, **agent CLI updates** (off by
default), and applying an Agent Fleet update.
→ [08 Going further](08-advanced.md#environment-settings-and-recreating-the-workspace)

### AWS SSM

Profiles (shared settings) and SSM hosts (individual) for logging in to another in-house host.
→ [08 Going further](08-advanced.md#logging-in-to-another-in-house-host-ssm)

### Internal repositories

Create, rename, delete and browse git repositories that **live entirely inside the tenant**, with no external
git hosting. The clone URL authenticates itself, so there is no connect step (and it works while the workspace
is stopped). → [04 Repositories and git](04-git.md)

### Danger zone

**Recreate the workspace** (delete `~/repos` only and rebuild from the latest image) and **clean home** (a
deeper reset that also removes home except logins and connections). Both lose uncommitted changes.
→ [08 Going further](08-advanced.md#recreating-the-workspace-danger-zone)

---

## What you want to do → which tab

| What you want | Tab |
|---|---|
| Text is too small / change the colours | Display |
| Sign in with another method too | Account |
| Send with Enter | Keys (send key) |
| Switch panes by tab | Display (main area layout) |
| Rebind a shortcut / pass keys to the terminal | Keys |
| Have replies read out loud | Speech |
| Silence Slack without disconnecting | Notifications (service notifications) |
| Make the assistant answer in English | Assistant (output language) |
| Let the operator run ahead — or stop it | Assistant (autopilot, auto-reply) |
| I keep typing the same preamble | Agent instructions |
| Sign in to Claude / Codex | Agents |
| Stop a model that bills extra from being picked | Agents (models you don't use) |
| Let sessions talk to each other | Agents (session-to-session messaging) |
| Clone a private repository | Git hosting |
| Get help investigating an incident | Ops & monitoring |
| Follow progress while away from the desk | Chat integration |
| Let the AI use an in-house tool | MCP servers |
| Drive it from the Claude on my laptop | MCP tokens |
| Find out what the tokens went on | Usage |
| It memorised something wrong | Agent memory |
| Change the Java / Node version | Toolchain |
| Get into another server | AWS SSM |
| Keep code that cannot leave the building | Internal repositories |
| The environment is broken | Danger zone |

---

For those who want the internals: [dev/02 Console design](../../dev/02-console.md)

# 06. Agents — connecting and choosing claude / codex / opencode / GitHub Copilot / Cursor / Kiro

English | [日本語](06-agents.ja.md)

> For: members deciding which agent to use and connecting it. Covers how the connections
> differ, model selection, and the RTK setting shared by the three agents. All connections
> are made from **⚙Settings → the "Agents" tab** (the workspace must be running).

## Supported agents and how to choose

Six major CLI coding agents are supported (the experimental Antigravity (agy) slot is
covered in [08](08-advanced.md)). Connection changes take effect immediately; behavior
settings apply **from each agent's new sessions**.

| | claude | codex | opencode | copilot | cursor | kiro |
|--|--------|-------|----------|---------|--------|------|
| Authentication | OAuth connection (paste a code) | ChatGPT subscription / API key | Provider API keys (env) | Rides the GitHub connection (no separate sign-in) | Sign in with a Cursor account (browser approval only) | Device-flow sign-in (Builder ID / Google / GitHub — browser approval only) |
| Model choice at launch | Yes | Yes | Yes | Yes (plan-dependent — Free is Auto only) | Yes (tied to the account) | Yes (named models even on Free) |
| States | Working / Question / Plan ready / Awaiting permission / Ready | Working / Question / Plan ready / Ready | Working / Question / Ready | Working / Awaiting permission / Ready | Working / Ready | Working / Awaiting permission / Ready |
| Chat view & history | Yes | Yes | Yes | Yes | Live: yes (simplified tool output). Stopped: no history under Managed | Yes (readable history even under Managed) |
| Plan mode | Yes | Yes | Yes | Set at launch + switchable from managed settings | Yes | Not supported |
| Execution method | Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) |
| Resume | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (can't resume across execution methods) | Yes (not if the working folder is gone) |
| Hand off | Yes | Yes | Yes | Yes | Yes | Yes |
| Image paste | Yes | Yes | Yes (model-dependent) | Not supported | Not supported | Not supported |

If you're unsure, choose by the subscription or models you use. If you use an Anthropic
account, pick **claude**; if you use ChatGPT or the OpenAI API, pick **codex**; if you want
to switch between API keys from multiple providers, pick **opencode**; if you have a
GitHub Copilot subscription, pick **copilot**; if you have a Cursor plan, pick
**cursor**; if you use an AWS Builder ID (or Kiro plan), pick **kiro**. They all support
the conversation view, answering questions, and handing a conversation off to another
agent; the context gauge is on claude / codex / opencode / kiro.

**Managed execution** for Codex / opencode / copilot / cursor / kiro lets you handle your everyday
work entirely from the conversation view (Codex / opencode carry no extra per-session
process, which makes them well suited to parallel work; copilot / cursor / kiro run a dedicated
per-session process even when Managed). Pick **Terminal (CLI)** only when you need the
CLI's own black screen. For details, see
[02 Sessions](02-sessions.md#execution-method--managed-and-terminal-cli).
The Managed chat view is separate from the assistant chat in the left pane, which doesn't use a repository.

You can confirm a connection succeeded on each card in ⚙Settings → the "Agents" tab.
It shows **"Connected"**, and for claude / codex also the signed-in account (email) and
plan. Once one agent is connected, it appears among the session types when you launch a
new session ([02](02-sessions.md)).

## Feature matrix (all agent kinds)

The table at the top compares the six main agents. This one adds Antigravity (agy) and
the non-agent session kinds (shell / SSM), and rolls in the cross-cutting features
covered elsewhere in this guide — worktrees ([04](04-git.md)), scheduled runs and the
chat bridge ([11](11-fleet-operator.md), [08](08-advanced.md)). ✓ = supported,
— = not applicable / not supported.

| Capability | claude | codex | cursor | copilot | kiro | agy | opencode | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed (paneless) execution | — | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| History when stopped (read-only) | ✓ | ✓ | —³ | ✓ | ✓ | ✓ | ✓ | — | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓ | ✓ | ✓ | — | — |
| Reasoning-effort control | ✓ | ✓ | —² | ✓ | — | —² | ✓ | — | — |
| Plan mode | ✓ | ✓ | ✓ | ✓ | — | — | ✓ | — | — |
| Context-window gauge | ✓ | ✓ | — | — | ✓ | — | ✓ | — | — |
| Image paste | ✓ | ✓ | — | — | — | ✓ | ✓ | — | — |
| Hand off a conversation | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Runs in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | — | ✓ | ✓ | — | — |
| WS-bar usage / limit chip | ✓ | ✓ | — | ✓ | — | ✓ | — | — | — |

¹ copilot's model choice is plan-dependent (Free = Auto only).

² cursor and agy fold the reasoning effort into the model name, so there is no separate
control. kiro accepts a `--effort` flag but exposes no per-model effort picker.

³ cursor's managed (default) execution keeps no local transcript — a **stopped** cursor
session has no history to show (the live mirror works while running, and running cursor
as Terminal (CLI) does persist a readable history). kiro, by contrast, persists a readable
transcript even under Managed, so a stopped kiro session still shows its history.

The WS-bar usage chip needs an account-level limit to show — opencode
(bring-your-own provider API keys), cursor, and kiro expose none. **shell** is a raw shell and
**ssm** is a remote login over AWS SSM — both are terminal-only with no conversation,
state model, or notifications.

**Default model for the assistant chat** — each assistant can pin its own model, and
claude's default is settable deployment-wide via `AF_CHAT_MODEL`. Fast, low-cost tiers are
the defaults because the assistant is conversational: claude → Sonnet 5 · codex →
`gpt-5.6-luna` · opencode → `opencode/nemotron-3-ultra-free` · agy → Gemini 3.5 Flash ·
cursor → its own default (Auto). cursor's assistant runs **read-only** (`--mode ask`).
kiro is **not** available as an assistant chat (it has no headless chat mode).

> **A note on autonomous execution.** Agents run commands, edit files, and push on your
> behalf — including unattended (scheduled runs) and, in permission-bypassing modes,
> without asking each time. shell / SSM sessions run the string you send **verbatim**.
> These actions can be destructive or irreversible. Keep backups, use least-privilege
> credentials, and lean on the approval gates (shell-command confirmation, chat-bridge
> approve / deny). See also [11 Fleet operator](11-fleet-operator.md).

## Claude

On **Claude** in the "Agents" tab, press **"Connect via OAuth"** and sign-in opens in a
new tab. Approve in your own browser, then **paste the displayed code and press "Done"**
(if the tab doesn't open automatically, you can open it from "the sign-in link ↗"). Once
connected, the email and plan (e.g. `…@gmail.com · pro`) are shown.

Claude's behavior can be adjusted on the same screen.

- **Default model** — the model initially selected when launching a claude session. Tier aliases such as Opus / Sonnet / Haiku follow the newest release in that tier; a registered full model ID pins one release.
- **Additional Claude models** — register a full ID such as `claude-opus-4-8` to make an older release a normal choice in launch dialogs, default-model settings, and MCP `list_models`. Claude Code's OAuth subscription has no account-aware catalog endpoint, so it checks whether your account can still use that model only when the session starts. Removing an entry removes it from the catalog but does not rewrite existing sessions.
- **Models to exclude** — take a model out of circulation. An excluded model disappears from the launch dialog, from settings, and from the list an assistant picks from (MCP `list_models`), and any launch that names it explicitly — including a scheduled run's model field or one an assistant starts — is refused. Use it to avoid accidentally picking a model your plan bills extra for (Fable on a Claude Team plan draws on API credit, for example). It is per agent, and excluding a model also clears it from your default model and from any repository's last-used value. Excluding one model affects only that model (`gpt-5.4-mini` stays available after you exclude `gpt-5.4`) — except for claude's tier names (`fable` and friends), which are aliases and so also cover the full model ids that contain them. It cannot stop the CLI's own controls, such as typing `/model` inside the terminal — this prevents accidental selection, it is not a hard billing guard.
- **Remote control** — turns on / off the ability to remotely drive running sessions from your local Claude app and the like. Off by default in new workspaces (turn it on here if you need it).
- **Notifications** — whether to notify you of session state changes.
- **RTK (token savings)** — see below.

> **If "Select login method" or a login screen shows up** → it's almost always a
> transient session-side state, and the connection itself is still alive. For the fix, see
> [09 Troubleshooting](09-troubleshooting.md). The traditional approach of running
> `/login` manually inside the terminal also still works.

## Codex

**Codex** can be connected in two ways.

- **Connect with a ChatGPT subscription** (recommended) — uses your Plus / Pro quota, no extra charge. It's a device-code flow. Beforehand you must **turn on "Enable device-code authentication for Codex" in ChatGPT's "Settings > Security"** (if this is off, approving won't advance).
- **Connect with an API key** — OpenAI API pay-as-you-go (`sk-…`).

The connection flow is the same 3 steps as GitHub (copy the code → open the link and
paste it → wait for approval).

**Behavior** also covers how codex's thinking (chain-of-thought) is shown.

- **Show thinking expanded** — the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Display only — it doesn't change how the agent works. It's per agent, and opencode has the same setting.

## OpenCode

**opencode** saves the **API key of the LLM provider you want to use as an env**.
Picking a preset fills in the env name automatically.

- **OpenCode Go** (default · `OPENCODE_API_KEY`) / **Anthropic** / **OpenAI** / **OpenRouter** / **Google Gemini** / **Sakana AI** (`SAKANA_API_KEY` · Fugu / Fugu Ultra) / **Custom…** (specify the env name yourself)

Paste the key and press **"Connect"** to save it; it's injected when opencode launches. You
can register multiple keys, and choose from the connected providers' models at launch.

**Behavior** also lets you choose how opencode's thinking (chain-of-thought) is shown.

- **Show thinking expanded** — the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Display only — it doesn't change how the agent works (independent of the same setting on codex).

## GitHub Copilot

**copilot** (GitHub Copilot CLI) has no separate sign-in. **Connecting GitHub as a git
provider automatically makes it "Connected"** (Git hosting tab > GitHub; disconnecting
follows the GitHub side too). As a prerequisite, that GitHub account needs a **Copilot
subscription** (including the Free plan) — without one, the first instruction fails with an error.

- The model choices at launch **switch automatically based on your plan**. The Free plan
  offers only "Auto (Copilot picks)", while paid plans list the models available to that account.
- The Free plan's monthly quota is on the small side. Check your usage on GitHub's settings pages.

## Cursor

**cursor** (Cursor CLI) — on the **Cursor** card in the "Agents" tab, press
**"Sign in to Cursor"**. An authorize link is shown; just open it in your browser and
approve (**there is no code to paste** — once you approve, the card automatically shows
"Connected"). A Cursor account is required. Connecting with an API key is not
supported.

- The model choices at launch are **exactly the models available to that account**
  (fetched live). You can't change the model after a session has started.
- The usage chip, context gauge, and image paste are not supported for cursor.
  Check your plan's remaining quota on the Cursor dashboard.

## Kiro

**kiro** (Kiro — formerly Amazon Q Developer CLI) — on the **Kiro** card in the
"Agents" tab, press **"Sign in to Kiro"**. It's a **device-flow** sign-in: an authorize
link with a confirmation code is shown; open it in your browser and approve (Builder ID /
Google / GitHub etc.). Once you approve, the card shows "Connected" with your account
email. Connecting with an API key is not supported.

- **On-demand install.** Kiro's CLI is large (~855 MB) and is **not baked into the image**
  by default. The first time you use it, it's downloaded into your home directory — the
  connection card shows an **"Install"** button with progress before you can sign in.
  (Deployments that set `BAKE_AGENT_CLIS=1` ship it pre-installed.)
- The model choices at launch are fetched live; **named models are available even on the
  Free plan** (Auto, Claude Sonnet / Haiku, and others). There's no separate
  reasoning-effort picker.
- Both **Managed (default)** and **Terminal (CLI)** execution are supported. Unlike cursor,
  a **stopped** kiro session still shows a readable history, and running kiro exposes a
  live **context gauge**.
- The image paste, Plan mode, and the WS-bar usage chip are not supported for kiro, and it
  can't be used as the left-pane assistant chat.

## Checking remaining context

In claude / codex / opencode / kiro sessions, a **"Context"** gauge (`ctx` when the screen is
narrow) appears at the top. Hover over it to see how many tokens the current conversation
is using, the limit, and the breakdown into cache reuse, new cache writes, and uncached.
As you approach the limit, a "May be auto-compacted soon" warning appears. If a long
task suddenly feels like the context has "thinned out", look here. The chat view also
shows the per-turn token-spend trend.

## RTK (token savings)

Five agents — claude / codex / opencode / GitHub Copilot / agy — have an on / off setting
for **"RTK (token savings)"** (cursor and Kiro don't have it yet). It smartly
rewrites the commands the agent runs to keep token consumption down. If this workspace's
image doesn't include RTK, "This workspace has no rtk." is shown.

How it takes effect differs a little by agent.

- **claude / opencode** — commands are rewritten transparently, so it works without you noticing.
- **copilot** — shell commands are routed through rtk by a hook, so it takes effect deterministically, like claude / opencode (applies to new sessions).
- **codex / agy** — they have no command-rewrite mechanism, so it's **instruction-based (best effort)**. It only nudges the agent to "please use rtk"; it isn't enforced.

---

For those who want to know how it works: [dev/08 Integrations (auth methods)](../../dev/08-integrations.md) · [dev/04 Workspace Agent (kind integration / RTK mechanism)](../../dev/04-workspace-agent.md)

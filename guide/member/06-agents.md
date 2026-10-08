---
audience: "anyone choosing and connecting an agent"
updated: "2026-10"
---

# 06. Agents — connecting one, and choosing between them

English | [日本語](06-agents.ja.md)

All connections are made from **⚙Settings → the "Agents" tab** (the workspace must
be running).

## Supported agents and how to choose

Six major CLI coding agents are compared in the table below. Two more run Managed only:
**Muse Code** (muse, [below](#muse-code)) and the fleet's own llama.cpp engine
(**lcpp**, [below](#lcpp)). The experimental Antigravity (agy) slot is covered in
[10](10-integrations.md). All of them have a column in the
[feature matrix](#feature-matrix-all-agent-kinds). Connection changes take effect
immediately; behavior settings apply **from each agent's new sessions**.

| | claude | codex | opencode | copilot | cursor | kiro |
|--|--------|-------|----------|---------|--------|------|
| Authentication | OAuth connection (paste a code) | ChatGPT subscription / API key | Provider API keys (env) | Rides the GitHub connection (no separate sign-in) | Sign in with a Cursor account (browser approval only) | Device-flow sign-in (Builder ID / Google / GitHub; browser approval only) |
| Model choice at launch | Yes | Yes | Yes | Yes (plan-dependent; Free is Auto only) | Yes (tied to the account) | Yes (named models even on Free) |
| States | Working / Question / Plan ready / Awaiting permission / Ready | Working / Question / Plan ready / Ready | Working / Question / Ready | Working / Awaiting permission / Ready | Working / Awaiting permission / Plan ready / Ready | Working / Awaiting permission / Ready |
| Chat view & history | Yes | Yes | Yes | Yes | Live: yes (simplified tool output). Stopped: no history under Managed | Yes (readable history even under Managed) |
| Plan mode | Yes | Yes | Yes | Set at launch + switchable from managed settings | Yes | Not supported |
| Execution method | Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) | Managed (default) / Terminal (CLI) |
| Resume | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (not if the working folder is gone) | Yes (can't resume across execution methods) | Yes (not if the working folder is gone) |
| Hand off | Yes | Yes | Yes | Yes | Yes | Yes |
| Image paste | Yes | Yes | Yes (model-dependent) | Not supported | Not supported | Not supported |
| Auto-resume after a usage limit resets | Yes | Managed only | Not supported | Not supported | Not supported | Not supported |

If you're unsure, choose by the subscription or models you use. If you use an Anthropic
account, pick **claude**; if you use ChatGPT or the OpenAI API, pick **codex**; if you want
to switch between API keys from multiple providers, pick **opencode**; if you have a
GitHub Copilot subscription, pick **copilot**; if you have a Cursor plan, pick
**cursor**; if you use an AWS Builder ID (or Kiro plan), pick **kiro**. They all support
the conversation view, answering questions, and handing a conversation off to another
agent; the context gauge is on claude / codex / opencode / kiro / lcpp / muse. Beyond those
six: if you have a Meta account (or a Meta Model API key), pick **muse**; if you want a model
that needs no vendor account at all, pick **lcpp**. It runs on your organization's own
engine (or a llama-server on your network), so there is no sign-in and no subscription limit.

**Managed execution** for Codex / opencode / copilot / cursor / kiro lets you handle your everyday
work entirely from the conversation view (Codex / opencode carry no extra per-session
process, which makes them well suited to parallel work; copilot / cursor / kiro run a dedicated
per-session process even when Managed). Pick **Terminal (CLI)** only when you need the
CLI's own black screen. lcpp and muse run Managed only, so there is no Terminal (CLI) to pick;
lcpp needs no per-session process either, while muse runs one per session. For details, see
[02 Sessions](02-sessions.md#execution-method-managed-and-terminal-cli).
The Managed chat view is separate from the assistant chat in the left pane, which doesn't use a repository.

You can confirm a connection succeeded on each card in ⚙Settings → the "Agents" tab.
It shows **"Connected"**, and for claude / codex also the signed-in account (email) and
plan. Once one agent is connected, it appears among the session types when you launch a
new session ([02](02-sessions.md)).

## Feature matrix (all agent kinds)

The table at the top compares the six main CLI agents. This one adds Antigravity (agy),
the Managed-only agents (lcpp and muse), and the non-agent session kinds (shell / SSM),
and rolls in the cross-cutting features covered elsewhere in this guide: worktrees
([03](03-code.md)), scheduled runs and the chat bridge ([08](08-organising.md),
[10](10-integrations.md)). ✓ = supported, — = not applicable / not supported.

| Capability | claude | codex | cursor | copilot | kiro | agy | opencode | lcpp | muse | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed (paneless) execution | — | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | ✓ | — | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | —⁴ | —⁴ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| History when stopped (read-only) | ✓ | ✓ | —³ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Reasoning-effort control | ✓ | ✓ | —² | ✓ | — | —² | ✓ | — | ✓ | — | — |
| Plan mode | ✓ | ✓ | ✓ | ✓ | — | — | ✓ | ✓ | — | — | — |
| Context-window gauge | ✓ | ✓ | — | — | ✓ | — | ✓ | ✓ | ✓ | — | — |
| Image paste | ✓ | ✓ | — | — | — | ✓ | ✓ | — | ✓ | — | — |
| Hand off a conversation | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Runs in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | — | ✓ | ✓ | — | ✓⁵ | — | — |
| WS-bar usage / limit chip | ✓ | ✓ | — | ✓ | — | ✓ | — | — | ✓ | — | — |

¹ copilot's model choice is plan-dependent (Free = Auto only).

² cursor and agy fold the reasoning effort into the model name, so there is no separate
control. kiro accepts a `--effort` flag but exposes no per-model effort picker.

The model lists show each model's **API list price** (input / output per 1M tokens) and context
window, and the line under the chosen model adds the cache-read price and release date. The
numbers are models.dev's published **pay-as-you-go API prices**, not what a claude, codex or agy
subscription is charged (for opencode they are the price opencode actually bills). A model codex
has announced it will retire, or one models.dev marks deprecated, is tagged "Retiring". Nothing is
shown for cursor, muse and lcpp, or for claude's tier aliases (Opus and so on): there is no price
source for the first three, and which model an alias runs depends on the CLI version. A full claude
id you registered does get one.

³ cursor's managed (default) execution keeps no local transcript: a **stopped** cursor
session has no history to show (the live mirror works while running, and running cursor
as Terminal (CLI) does persist a readable history). kiro, by contrast, persists a readable
transcript even under Managed, so a stopped kiro session still shows its history.

⁴ lcpp and muse have no Terminal (CLI) route: lcpp has no vendor CLI to put in a pane;
muse drives the session protocol directly. Sessions of either kind default to Managed.

⁵ As the assistant, muse answers one prompt per turn as its own headless run and remembers
the conversation; it answers rather than acts (shell, file writing and web tools are off for
those turns). It needs Muse Code installed and signed in first; see the connection card below.
Every muse row was ticked only after it was observed working on a real session.

Usage chips add up, so the bar keeps only the **two agents you used most recently**. The
rest fold into a **"+N"** chip on the right and open from inside it (a folded chip keeps
reading its usage, so opening the popover costs no fresh fetch). A chip near its cap
(95% or more, or holding a Full reset) comes back onto the bar even if you have not run
that agent lately. Each chip's dropdown has an **"On the WS bar"** control (**Always
show** / **Auto** (default) / **Always fold**). Pin a third agent if you want it on the bar
permanently, and note that "Always fold" keeps it folded even near its cap. On a phone the
chips already sit in the ⋯ overflow as a list, so nothing is folded there.

The WS-bar usage chip needs an account-level limit to show: opencode
(bring-your-own provider API keys), cursor, and kiro expose none. **lcpp** and **muse**
have no Terminal (CLI) route; sessions of either kind default to Managed. **muse**
requires on-demand installation (~299 MB) and a sign-in before it appears in the launch
menu. 🔴 A muse session asks for no tool approvals: every tool call is allowed before any
approval is considered, giving it the same reach over this workspace as `shell` (read the
autonomous execution note above with that in mind). **shell** is a raw shell and **ssm**
is a remote login over AWS SSM; both are terminal-only with no conversation, state model,
or notifications.

**Default model for the assistant chat**: each assistant can pin its own model, and
claude's default is settable deployment-wide via `AF_CHAT_MODEL`. Fast, low-cost tiers are
the defaults because the assistant is conversational: claude → Sonnet 5 · codex → the newest
Luna it lists (`gpt-6-luna` at the time of writing) · opencode → `opencode-go/glm-5.2` when the account lists it, otherwise `opencode/nemotron-3-ultra-free` · agy → Gemini 3.5 Flash ·
cursor → its own default (Auto). cursor's assistant runs **read-only** (`--mode ask`).
kiro is **not** available as an assistant chat (it has no headless chat mode).

> **A note on autonomous execution.** Agents run commands, edit files, and push on your
> behalf, including unattended (scheduled runs) and, in permission-bypassing modes,
> without asking each time. shell / SSM sessions run the string you send **verbatim**.
> These actions can be destructive or irreversible. Keep backups, use least-privilege
> credentials, and lean on the approval gates (shell-command confirmation, chat-bridge
> approve / deny). See also [08 Fleet operator](08-organising.md).

## Claude

On **Claude** in the "Agents" tab, press **"Connect via OAuth"** and sign-in opens in a
new tab. Approve in your own browser, then **paste the displayed code and press "Done"**
(if the tab doesn't open automatically, you can open it from "the sign-in link ↗"). Once
connected, the email and plan (e.g. `…@gmail.com · pro`) are shown.

Claude's behavior can be adjusted on the same screen.

- **Default model**: the model initially selected when launching a claude session. Tier aliases such as Opus / Sonnet / Haiku follow the newest release in that tier; a registered full model ID pins one release.
- **Additional Claude models**: register a full ID such as `claude-opus-4-8` to make an older release a normal choice in launch dialogs, default-model settings, and MCP `list_models`. Claude Code's OAuth subscription has no account-aware catalog endpoint, so it checks whether your account can still use that model only when the session starts. Removing an entry removes it from the catalog but does not rewrite existing sessions.
- **Models to exclude**: take a model out of circulation. An excluded model disappears from the launch dialog, from settings, and from the list an assistant picks from (MCP `list_models`), and any launch that names it explicitly (including a scheduled run's model field or one an assistant starts) is refused. Use it to avoid accidentally picking a model your plan bills extra for (Fable on a Claude Team plan draws on API credit, for example). It is per agent, and excluding a model also clears it from your default model and from any repository's last-used value. Excluding one model affects only that model (`gpt-5.4-mini` stays available after you exclude `gpt-5.4`), except for claude's tier names (`fable` and friends), which are aliases and so also cover the full model ids that contain them. It cannot stop the CLI's own controls, such as typing `/model` inside the terminal: this prevents accidental selection and is not a hard billing guard.
- **Show thinking expanded**: the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Claude's thinking is the short note on what it is doing right now, written between tool runs. It is the same text the terminal shows, and sometimes the only prose there is in the middle of a long autonomous stretch. Display only: it doesn't change how the agent works (independent of the same setting on codex / opencode).
- **Stream replies in the chat view**: how the session view shows a reply while Claude is still writing it, in a "Writing…" block that gives way to the finished reply once Claude has written it. Three choices. **Typewriter** (the default) types the new text out character by character, the way a chat app reveals a reply. **Line by line** shows each line whole as soon as it arrives. **Off** shows a reply only when it is complete. Either way the session view receives a line once it ends, so a long paragraph arrives in one piece, and prose written between tool runs still appears when it is complete. On a device set to reduce motion, Typewriter behaves like Line by line. Display only: the terminal and the agent are unaffected.
- **Remote control**: turns on / off the ability to remotely drive running sessions from your local Claude app and the like. Off by default in new workspaces (turn it on here if you need it).
  - **What it needs**: Claude can only start Remote Control when it is allowed to fetch feature flags. The workspace image normally switches that off (it sets `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`), and with it off Claude starts the session but quietly creates no Remote Control session.
  - **What the toggle does**: while it is on, Claude sessions **started afterwards** are started without that switch in the environment they inherit, which lifts its block on starting Remote Control. It takes effect from the next time a session starts or is resumed; a session that is already running keeps the setting it started with. Turning it off restores the image's behaviour for sessions started after that. The toggle is a setting of the workspace's user-level Claude settings; a project or local Claude settings file can still override `remoteControlAtStartup`.
  - **Only the inherited environment is changed**: if `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is also set in the `env` block of a user, project, local or managed Claude `settings.json`, Claude sets it again after it starts and Remote Control still does not connect. Remove it from the file where it is set; the toggle never edits your settings files.
  - **Organizations that require Trusted Devices**: the image also keeps `DISABLE_TELEMETRY` set (this change leaves it so). With it set, Remote Control needs Claude Code v2.1.283 or later, and does not connect at all when your organization requires Trusted Devices. In that case this toggle is not enough on its own.
  - **What else this switch was holding back**: for sessions started with the toggle on, this switch no longer suppresses release-note fetching, PR/MR status-badge checks or availability checks (for example whether fast mode is usable), and `/feedback` works. Telemetry metrics, error reports and auto-update stay off, because their own variables stay set.
  - **Not known**: whether Claude fetches feature flags in these sessions (`DISABLE_TELEMETRY`, which stays set, also skips that fetch according to the upstream documentation) and, if it does, what that fetch sends to Anthropic. Neither has been measured.
- **Notifications**: whether to notify you of session state changes.
- **RTK (token savings)**: see below.

> **If "Select login method" or a login screen shows up**, it's almost always a
> transient session-side state, and the connection itself is still alive. For the fix, see
> [11 Troubleshooting](11-troubleshooting.md). The traditional approach of running
> `/login` manually inside the terminal also still works.

## Codex

**Codex** can be connected in two ways.

- **Connect with a ChatGPT subscription** (recommended): uses your Plus / Pro quota, no extra charge. It's a device-code flow. Beforehand you must **turn on "Enable device-code authentication for Codex" in ChatGPT's "Settings > Security"** (if this is off, approving won't advance).
- **Connect with an API key**: OpenAI API pay-as-you-go (`sk-…`).

The connection flow is the same 3 steps as GitHub (copy the code → open the link and
paste it → wait for approval).

**Behavior** also covers how codex's thinking (chain-of-thought) is shown.

- **Show thinking expanded**: the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Display only: it doesn't change how the agent works. It's per agent, and claude / opencode have the same setting.

## OpenCode

The card has two controls, and they decide different things.

**"Use opencode"** is the switch for the whole agent. While it is **Off**, opencode never
launches: not with a stored API key, not with a signed-in account, not if a key is added
later. A fresh workspace starts here, so nothing reaches opencode.ai until you say so.

**"opencode.ai billing"** appears once it is On, and decides **only how opencode.ai is used**.
Whichever you pick, the providers you connect yourself (Anthropic, OpenRouter, …) and the
fleet's own engines stay in the launch list.

| Choice | What the launch list holds | What it needs |
|---|---|---|
| **None (my own keys)** | Your own providers and the fleet's engines only. `OPENCODE_API_KEY` is not injected even if stored. | One provider key of your own (or a fleet engine) |
| **Free models only** | opencode.ai's models that work with no credentials. Subject to congestion and caps. | Nothing |
| **Go (subscription)** | The subscription ids (`opencode-go/…`). | An API key (signing in is optional) |
| **Zen (metered)** | The pay-per-request ids (`opencode/…`), plus Go's when you have both. | An account sign-in or an API key |

If the route you picked has no model at all (Go without a Go contract, say), the launch list
falls back to Zen, and the card tells you it did. Check the sign-in or the plan if you meant
to be billed the other way.

**API keys.** Picking a preset fills in the env name automatically.

- **opencode.ai** (`OPENCODE_API_KEY`; the same key pays for both Go and Zen) / **Anthropic** / **OpenAI** / **OpenRouter** / **Google Gemini** / **Sakana AI** (`SAKANA_API_KEY` · Fugu / Fugu Ultra) / **Custom…** (specify the env name yourself)

Paste the key and press **"Connect"** to save it; it's injected when opencode launches. You
can register multiple keys, and choose from the connected providers' models at launch. A key
the current route does not inject is kept but marked as such rather than deleted.

Changing a key or the billing route does **not** reach a running opencode serve on its own:
serve keeps the configuration it started with. When a change needs applying, the card says so
and offers **"Restart opencode serve to apply"**. A restart waits for opencode sessions to
finish answering and cuts short any that do not, so press it when nothing is mid-turn.

**Behavior** also lets you choose how opencode's thinking (chain-of-thought) is shown.

- **Show thinking expanded**: the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Display only: it doesn't change how the agent works (independent of the same setting on claude / codex).

## GitHub Copilot

**copilot** (GitHub Copilot CLI) has no separate sign-in. **Connecting GitHub as a git
provider automatically makes it "Connected"** (Git hosting tab > GitHub; disconnecting
follows the GitHub side too). As a prerequisite, that GitHub account needs a **Copilot
subscription** (including the Free plan); without one, the first instruction fails with an error.

- The model choices at launch **switch automatically based on your plan**. The Free plan
  offers only "Auto (Copilot picks)", while paid plans list the models available to that account.
- The Free plan's monthly quota is on the small side. Check your usage on GitHub's settings pages.

## Cursor

**cursor** (Cursor CLI): on the **Cursor** card in the "Agents" tab, press
**"Sign in to Cursor"**. An authorize link is shown; just open it in your browser and
approve (**there is no code to paste**: once you approve, the card automatically shows
"Connected"). A Cursor account is required. Connecting with an API key is not
supported.

- The model choices at launch are **exactly the models available to that account**
  (fetched live). You can't change the model after a session has started.
- The usage chip, context gauge, and image paste are not supported for cursor.
  Check your plan's remaining quota on the Cursor dashboard.

## Kiro

**kiro** (Kiro, formerly Amazon Q Developer CLI): on the **Kiro** card in the
"Agents" tab, press **"Sign in to Kiro"**. It's a **device-flow** sign-in: an authorize
link with a confirmation code is shown; open it in your browser and approve (Builder ID /
Google / GitHub etc.). Once you approve, the card shows "Connected" with your account
email. Connecting with an API key is not supported.

- **On-demand install.** Kiro's CLI is large (~855 MB) and is **not baked into the image**
  by default. The first time you use it, it's downloaded into your home directory. The
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

## Muse Code

**Muse Code** is a Managed-only session kind. Before it appears in the launch menu, two
things must be true:

1. On the **Muse Code** card in ⚙Settings → "Agents", press **"Install Muse Code"** (about 299 MB
   into your home, once; the sign-in screen appears by itself when it finishes). When a newer
   pinned build is available the card says so and offers **"Update Muse Code"**; until you press
   it the installed build keeps being used, and running muse sessions stay on the old build until
   they are restarted.
2. Then sign in. **"Sign in with your Meta account"** shows an authorize link and a code to approve
   in your browser, with nothing to paste back; this is the subscription route. **"Use an API key"** is
   the pay-as-you-go route: saving a key removes a stored account sign-in and moves you onto
   per-use billing, so disconnect first if you are signed in with an account.

The **Behaviour** settings on the same card let you set the model (Agent Fleet selects the
newest model without the "-contributor" clause by default; see
[Agents reference](../ref/agents.md) for what the `-contributor` models mean) and the
reasoning effort.

In a running muse session the **`/`** button beside the input lists the session's own skills:
Muse Code's bundled ones, plugin skills, yours under `~/.config/muse/skills`, and the working
copy's `.agents/skills/`. Picking one runs it
([07](07-chat-memo.md#calling-a-skill-or-a-command)).

> 🔴 **A muse session asks for no tool approvals.** Every tool call is allowed before any
> approval is considered: the sandbox cannot be built inside this Workspace container.
> Treat a muse session as having the same reach over this workspace as a `shell` session.

For details on what Agent Fleet cannot see inside a muse session (its own scheduled runs,
cross-session messaging, and session list), see [Agents reference](../ref/agents.md#muse-what-agent-fleet-does-not-see).

## lcpp

**lcpp** is the fleet's own llama.cpp engine, and needs no sign-in or separate installation. Launch it like any other session kind from the session dialog.

Its card in ⚙Settings → "Agents" has two controls. **"Use llama.cpp"** (On by default) is the
switch: Off takes it out of the launch menus and refuses a launch by any other route, while
sessions already running keep going. **"Your own connection"** points your sessions at a
llama-server on your own network instead of the deployment's engine: enter its URL (and an API
key if it wants one), press **"Check connection"** to see its build, context window and models,
and the launch dialog's model list becomes that server's. While it is set, lcpp sessions connect
straight to it (the tenant administrator's engine permission does not apply), and the **Chat**
pill in the top bar reports that connection (**Connected** / **Not reachable** / **Checking**,
and the model it found) rather than the deployment's engine
([badges](badges-and-menus.md#the-engine-pills-in-the-top-bar)). Clear the fields to go back to
the deployment's engine.

What to expect before your first session:

- **The first turn can take minutes.** When the engine instance is stopped, waking it is a
  genuine cold start (roughly 3.5–7 minutes). Subsequent turns are fast while the
  instance stays warm.
- **Choose a context window of 8000 tokens or more.** At smaller windows, some model
  families trigger compaction loops the harness deliberately breaks out of.
- **Stick to a verified model family.** Qwen3, GPT-OSS, and Gemma are confirmed to work;
  `llama-3.1-8b-instruct` does not (no Llama-specific tool-call parser in this build).

For full details (measured turn counts by model family, cold-start timings, and the
reasoning behind the window guidance), see [Agents reference](../ref/agents.md#lcpp-what-hardware-measurement-found).

## Checking remaining context

In claude / codex / opencode / kiro / lcpp / muse sessions, a **"Context"** gauge (`ctx` when the screen is
narrow) appears at the top. Hover over it to see how many tokens the current conversation
is using, the limit, and the breakdown into cache reuse, new cache writes, and uncached.
As you approach the limit, a "May be auto-compacted soon" warning appears. If a long
task suddenly feels like the context has "thinned out", look here. The chat view also
shows the per-turn token-spend trend.

## RTK (token savings)

Five agents (claude / codex / opencode / GitHub Copilot / agy) have an on / off setting
for **"RTK (token savings)"** (cursor and Kiro don't have it yet). It smartly
rewrites the commands the agent runs to keep token consumption down. If this workspace's
image doesn't include RTK, "This workspace has no rtk." is shown.

How it takes effect differs a little by agent.

- **claude / opencode**: commands are rewritten transparently, so it works without you noticing.
- **copilot**: shell commands are routed through rtk by a hook, so it takes effect deterministically, like claude / opencode (applies to new sessions).
- **codex / agy**: they have no command-rewrite mechanism, so it's **instruction-based (best effort)**. It only nudges the agent to "please use rtk"; it isn't enforced.

## Agent instructions (write down how you work, once)

Whatever you write under **⚙Settings → "Agent instructions"** is added to the instructions of
every agent you start in this workspace from then on. The language and tone of reports, when
you want to be asked before something happens, which tools to prefer: anything you find
yourself **retyping into every prompt** can move here.

Instructions come in three layers, and this setting is the **middle** one.

| Layer | Whose it is | Can you change it? |
|-------|-------------|--------------------|
| Workspace guide | The whole fleet (your operator) | Not from here, and it wins if the two conflict |
| **Agent instructions** | **You** | **This setting** |
| Repository instructions (`CLAUDE.md` / `AGENTS.md`) | The whole team (committed) | Edit them in the repository |

- **It is never committed to a repository.** It affects you, not your colleagues.
- It applies to **sessions started from now on**. Running sessions keep what they read at start.
  lcpp is the exception: it has no file to write, so its row says it is added to the system
  prompt every turn, and a running lcpp session picks up a change on its next turn.
- It can be delivered to claude / codex / opencode / GitHub Copilot / agy / Kiro / lcpp / muse.
  **Cursor is the only one that can't take it**, and it still appears in the list with the reason
  (Cursor keeps User Rules in your Cursor account, with no local per-user place for instructions).
- Each row shows **where it goes** (the file it was written to, or for lcpp the system prompt) and **whether it is actually in effect**. When
  something saved but isn't in effect, that row says why.
- There is a length limit: this text rides along in **every session's context, every time**, so
  shorter works better.
- **Don't put secrets (API keys, tokens) here.** It is plain text that several agents read.

## Bringing your personal Claude Code setup

Two settings cover part of this: **⚙Settings → "Agent instructions"** is your global `CLAUDE.md`
(see above) and **⚙Settings → "MCP servers"** covers MCP. Personal **hooks, skills and subagents**
have no settings screen. In a Workspace they are plain files in Claude Code's configuration
folder, `/var/lib/af/claude` (run `echo $CLAUDE_CONFIG_DIR` in a shell to confirm the path on your
deployment). It is what `~/.claude` is on your own machine.

| What | Where | Shared with the team? |
|------|-------|-----------------------|
| Hooks, skills and subagents the team should have | `.claude/` in the repository, committed | Yes |
| Your own skills | `<configuration folder>/skills/<name>/SKILL.md` | No |
| Your own subagents | `<configuration folder>/agents/<name>.md` | No |
| Your own hooks | the `hooks` key of `<configuration folder>/settings.json` | No |

- **It works, but it is a Claude Code feature you drive by hand.** Agent Fleet has no screen for
  these files and does not check them. Checked on Claude Code 2.1.293 with a throwaway
  configuration folder: a `SessionStart` hook ran, and the skill and the subagent showed up in the
  session's list. A skill in `skills/` also appears in the session view's skill picker.
  Try yours in a real session before relying on it. Claude reads these files **when a session
  starts**, so a change applies to sessions you start afterwards.
- **It survives.** The folder is on its own storage: Stop / Start, **Recreate** (which only deletes
  `~/repos`) and a home clean-up do not touch it.
- **Edit it from a shell or Terminal (CLI) session.** The Console's file browser hides this folder
  (it also holds your login). Don't copy `.credentials.json` or `.claude.json` anywhere.
  **Keep secrets out of `settings.json`, your hook commands and the committed `.claude/`**: no API
  keys or tokens in `env` or in a command line. Sign-ins and credentials belong in
  **⚙Settings → Connections**.
- **Don't edit Fleet's own hook entries in `settings.json`.** The Agent adds hooks that feed the
  Console (running / waiting for your answer, a pending question or plan, a permission prompt,
  forwarded notifications). They are the entries whose command runs `session-status` or
  `session-push-notification`. The RTK entry (`rtk hook claude`) is switched by
  **⚙Settings → "Agents" → Claude → RTK**, not by editing. Without Fleet's entries the Console
  stops showing state for that session. The Agent re-adds them when it starts.
- **Your own hooks and other keys in `settings.json` are kept.** The Agent recognises its entries
  by their command, so a hook of yours on the same tool matcher (for example `Bash`) stays, also
  when you flip RTK on and off. Known limit: a command of yours that itself contains the word
  `session-status` is mistaken for Fleet's, so don't use that word in your own hook commands.
- **Write hooks for the Workspace, not for your laptop.** Depend only on what the image has
  (bash, jq, node, python) or on what you installed under `~/.local`. A path into your own
  machine does not exist here, and paths such as `/usr`, `/opt` and `/tmp` revert to the image, so keep scripts in your home.
- **A hook that starts `claude -p` needs `env -u AF_SESSION_NAME`.** The child inherits the
  session's identity and the same `settings.json`, with Fleet's status hooks. Those hooks then
  report the child's activity as the parent session's, which can scramble its status. Start it as
  `env -u AF_SESSION_NAME claude -p …`. The first half of this is read from the Agent's code; run
  your own hook once and watch that the session's status stays right.

The workspace policy file that tells **agents** not to read or touch `~/.claude` and the other
agent state is written for the agents themselves. It does not forbid you from the above. You can
ask an agent to add or edit your personal skills, subagents and hooks; credentials and Fleet's own
hook entries stay off limits to it.

---
audience: "anyone choosing and connecting an agent"
---

# 09. Agents — connecting one, and choosing between them

English | [日本語](09-agents.ja.md)

All connections are made from **⚙Settings → the "Agents" tab** (the workspace must
be running).

## Supported agents and how to choose

Agent Fleet drives eleven session kinds, and what each of them supports (execution methods,
history, plan mode, usage chips, instructions and so on), how each one signs in, which states it
shows, and how to choose between them are all in the
[Agents reference](../ref/agents.md#choosing-one). Connection changes take effect
immediately; behavior settings apply **from each agent's new sessions**. The sections below cover
the cards in ⚙Settings → "Agents": [Claude](#claude), [Codex](#codex), [OpenCode](#opencode),
[GitHub Copilot](#github-copilot), [Cursor](#cursor), [Kiro](#kiro),
[Antigravity (agy)](#antigravity-agy), [Muse Code](#muse-code) and [lcpp](#lcpp).

Which kinds run Managed and which run in Terminal (CLI) is in the
[Agents reference](../ref/agents.md), and what each choice means is in
[03 Starting sessions](03-starting-sessions.md#execution-method-managed-and-terminal-cli).
The Managed chat view is separate from the assistant chat in the left pane, which doesn't use a repository.

You can confirm a connection succeeded on each card in ⚙Settings → the "Agents" tab.
It shows **"Connected"**, and for claude / codex also the signed-in account (email) and
plan. Once one agent is connected, it appears among the session types when you launch a
new session ([03](03-starting-sessions.md)).

> **A note on autonomous execution.** Agents run commands, edit files, and push on your
> behalf, including unattended (scheduled runs) and, in permission-bypassing modes,
> without asking each time. shell / SSM sessions run the string you send **verbatim**.
> These actions can be destructive or irreversible. Keep backups, use least-privilege
> credentials, and lean on the approval gates (shell-command confirmation, chat-bridge
> approve / deny). See also [11 Running many sessions](11-many-sessions.md).

**shell** is a raw shell and **ssm** is a remote login over AWS SSM; both are terminal-only
with no conversation, state model, or notifications. A muse session asks for no tool
approvals, so read the note above as applying to it the way it applies to `shell`
([Muse Code](#muse-code)). The states every kind shows, the other agents included, are listed in
the [Agents reference](../ref/agents.md#states-shown-in-the-mirror).

## Model prices and usage chips

The model lists show each model's **API list price** (input / output per 1M tokens) and context
window, and the line under the chosen model adds the cache-read price and release date. The
numbers are models.dev's published **pay-as-you-go API prices**, not what a claude, codex or agy
subscription is charged (for opencode they are the price opencode actually bills). A model codex
has announced it will retire, or one models.dev marks deprecated, is tagged "Retiring". Nothing is
shown for cursor, muse and lcpp, or for claude's tier aliases (Opus and so on): there is no price
source for the first three, and which model an alias runs depends on the CLI version. A full claude
id you registered does get one.

Usage chips add up, so the bar keeps only the **two agents you used most recently**. The
rest fold into a **"+N"** chip on the right and open from inside it (a folded chip keeps
reading its usage, so opening the popover costs no fresh fetch). A chip near its cap
(95% or more, or holding a Full reset) comes back onto the bar even if you have not run
that agent lately. Each chip's dropdown has an **"On the WS bar"** control (**Always
show** / **Auto** (default) / **Always fold**). Pin a third agent if you want it on the bar
permanently, and note that "Always fold" keeps it folded even near its cap. On a phone the
chips already sit in the ⋯ overflow as a list, so nothing is folded there.

The WS-bar usage chip needs an account-level limit to show; which kinds have one is the "Usage /
remaining-quota chip" row of the [Agents reference](../ref/agents.md).

## Default model for the assistant chat

Each assistant can pin its own model, and an administrator can set claude's default for the whole
deployment. Fast, low-cost tiers are the defaults because the assistant is conversational:
claude → Sonnet 5 · codex → the newest Luna it lists (`gpt-6-luna` at the time of writing) ·
opencode → `opencode-go/glm-5.2` when the account lists it, otherwise `opencode/nemotron-3-ultra-free` ·
agy → Gemini 3.5 Flash · cursor → its own default (Auto). muse is not a fast tier: left on the
recommendation it picks the newest Muse Code model that is not a "contributor" model; whether Meta
may use your conversations still follows your own Contributor mode setting (the assistant-chat
note in the [Agents reference](../ref/agents.md)). Which kinds can be an
assistant chat at all is the "Usable as the assistant chat" row there; cursor's assistant, for
one, runs **read-only**.

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
  - **What it needs**: Claude does not start Remote Control while `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set, and the workspace image normally sets it. In that state the Claude session itself runs, but quietly no Remote Control session is created.
  - **What the toggle does**: while it is on, Claude sessions **started afterwards** are started without that switch in the environment they inherit, which lifts its block on starting Remote Control. It takes effect from the next time a session starts or is resumed; a session that is already running keeps the setting it started with. Turning it off restores the image's behaviour for sessions started after that. The toggle is a setting of the workspace's user-level Claude settings; a project or local Claude settings file can still override `remoteControlAtStartup`.
  - **Only the inherited environment is changed**: if `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is also set in the `env` block of a user, project, local or managed Claude `settings.json`, Claude sets it again after it starts and Remote Control still does not connect. Remove it from the file where it is set; the toggle does not remove or rewrite `env` entries in those settings files.
  - **Organizations that require Trusted Devices**: the image also keeps `DISABLE_TELEMETRY` set (this change leaves it so). With it set, Remote Control needs Claude Code v2.1.283 or later, and does not connect at all when your organization requires Trusted Devices. In that case this toggle is not enough on its own.
  - **What else this switch was holding back**: for sessions started with the toggle on, this switch no longer suppresses release-note fetching, PR/MR status-badge checks or availability checks (for example whether fast mode is usable), and `/feedback` works. Telemetry metrics, error reports and auto-update stay off, because their own variables stay set.
  - **Not known**: whether Claude fetches feature flags in these sessions (`DISABLE_TELEMETRY`, which stays set, also skips that fetch according to the upstream documentation) and, if it does, what that fetch sends to Anthropic. Neither has been measured.
- **Notifications**: whether to notify you of session state changes.
- **RTK (token savings)**: see below.

> **If "Select login method" or a login screen shows up**, it's almost always a
> transient session-side state, and the connection itself is still alive. For the fix, see
> [21 Troubleshooting](21-troubleshooting.md). The traditional approach of running
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

A few things work differently from the other agents:

- **Only `AGENTS.md` is read.** opencode does not fall back to `CLAUDE.md`, so a repository
  that has only a `CLAUDE.md` gives opencode nothing. Put what opencode should follow in the
  repository's `AGENTS.md`. Your own **Agent instructions** do reach opencode: Agent Fleet
  writes them into a marked block of opencode's global `AGENTS.md`, and the row in
  **⚙Settings → "Agent instructions"** shows that this is where they went and whether they are
  in effect. Leave that block alone; it is rewritten when you save.
- **Terminal (CLI) sessions share one server.** Each Terminal (CLI) session attaches to the
  opencode server Agent Fleet runs (it starts it when needed), instead of starting a private
  one, which saves about 150-250 MB of memory per additional Terminal session (measured with
  idle sessions). Every session stays separate: its own conversation, its own environment and
  its own status. If that server cannot be had (it fails to start, or an administrator has
  turned it off for the Workspace), the session starts with a private server instead, on its own. An
  administrator can also force a private server per session
  ([operate/03](../operate/03-run.md#opencode-2-terminal-sessions-and-the-shared-server)).
  A session keeps the mode it was launched in.
- **Questions are forms.** When opencode asks you something, the chat view shows it as a
  question card, one field per question, and you answer it there: pick an option or type your
  own. **Cancel — back to chat** dismisses the form and the agent carries on. This works the
  same in Managed sessions and in Terminal (CLI) sessions on the shared server, including a form
  with several questions or multiple-choice fields. If you answer in the terminal while the card
  is open, the card just reports the form as already answered. A Terminal (CLI) session on a
  private server shows the card too, but a form with several questions or multiple-choice fields
  has to be answered in the terminal view (for a single question the card's buttons press the
  keys). A form that is waiting is lost if the opencode server restarts.
- **No ToDo list by default.** opencode 2 only has its `todowrite` tool when you turn on
  `experimental.todo` in your own opencode config (measured on 2.1.4: off by default). Without
  it the session view shows no ToDo list, which is expected, not a lost connection; with it on,
  the session view shows the list of the latest `todowrite` call.

**Behavior** also lets you choose how opencode's thinking (chain-of-thought) is shown.

- **Show thinking expanded**: the session view's "Thinking" block starts expanded. Off by default (collapsed; click the heading to read it). Display only: it doesn't change how the agent works (independent of the same setting on claude / codex).

### When the provider returns an error

opencode retries a provider error such as a rate limit or an overloaded service. While it does,
a Managed session's view shows a band with the attempt and the provider's message ("The provider
returned an error and the agent is retrying (attempt N): …") instead of a plain working state. It
goes away when the answer starts or the turn ends, and you can stop the turn at any time. A
Terminal (CLI) session has no such band; the terminal shows what opencode itself prints.

In a Managed session, errors that no retry can fix (a region or privacy setting, an exhausted quota or billing balance,
a bad API key) end the turn as **failed** with the provider's message, and the band then reads
"The provider returned an error that retrying cannot fix, so the turn was stopped: …" until your
next input. For a "requires Global regions" message, change the privacy / region setting of the
provider account (for opencode Go, the workspace's Privacy settings on opencode.ai); for quota or
billing, top up the account; for a key, replace it on the agent's card ([21](21-troubleshooting.md)).

### How opencode 2 differs

The image runs opencode 2. A workspace that used opencode 1 keeps its conversations: the
first time opencode 2 starts there, it converts opencode's database in place and imports the
existing sessions once, with the same session ids, so they resume in Managed and Terminal (CLI)
sessions alike. Conversations started under opencode 2 are not visible to opencode 1, so going
back later loses them. If your administrator has pinned the image back to opencode 1, the
`AGENTS.md`, shared-server, question-form and ToDo points above do not apply
([operate/03](../operate/03-run.md#going-back-to-opencode-1)).

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
- Check your plan's remaining quota on the Cursor dashboard.
- In a Terminal (CLI) session, each finished turn's token counts reach ⚙Settings →
  "Agent usage" through a `stop` entry Agent Fleet adds to `~/.cursor/hooks.json`. Other
  entries in that file are kept. The counts are cursor's own report, so they are marked
  partial, and a turn on Auto is recorded under the model name `default`. A Managed cursor
  session's turns are not counted this way.

## Kiro

**kiro** (Kiro, formerly Amazon Q Developer CLI): on the **Kiro** card in the
"Agents" tab, press **"Sign in to Kiro"**. It's a **device-flow** sign-in: an authorize
link with a confirmation code is shown; open it in your browser and approve (Builder ID /
Google / GitHub etc.). Once you approve, the card shows "Connected" with your account
email. Connecting with an API key is not supported.

- **On-demand install.** Kiro's CLI is large (~855 MB) and is **not baked into the image**
  by default. The first time you use it, it's downloaded into your home directory. The
  connection card shows an **"Install Kiro"** button with progress before you can sign in.
  When a newer pinned build exists, the card offers **"Update Kiro"** so you can update ahead of
  time. A Terminal (CLI) launch updates to the pinned build by itself when it starts.
  (A deployment that bakes Kiro into the image ships it pre-installed.) While an update runs,
  Kiro sessions already running keep working on the old build until they are restarted.
- The model choices at launch are fetched live; **named models are available even on the
  Free plan** (Auto, Claude Sonnet / Haiku, and others). There's no separate
  reasoning-effort picker.
- Both **Managed (default)** and **Terminal (CLI)** execution are supported; what else kiro
  supports (history, context gauge, plan mode and so on) is in the
  [Agents reference](../ref/agents.md).

## Antigravity (agy)

**agy** (the Antigravity CLI) is an experimental slot. Its card says "Experimental slot
(small quota, shared with the Antigravity IDE / Jules)", and the launch menu offers it as
"Launch Antigravity (experimental)".

On the **Antigravity** card in the "Agents" tab, press **"Connect via OAuth"**. A Google sign-in
opens in a new tab. Approve it, then paste the displayed code and press "Done", as for
claude. Once connected, the card shows the account email and plan. The other auth method, GCP
project, is listed as "coming soon" and can't be picked yet. On a host where agy cannot run, the
card says "Not available on this host (…)" and agy is not offered.

- **Terminal (CLI) only.** There is no Managed execution for agy.
- The model choices at launch are the account's live list. The reasoning effort is part of the
  model name, so there is no separate picker. The WS bar shows its usage (5-hour and weekly).
  What else agy supports is in the [Agents reference](../ref/agents.md).
- **Background commands.** agy ends its turn soon after a
  command it ran with `run_command` starts, and leaves the command running. While such a command
  runs, the row reads **"Ready · running in background"**, and the session does not count as
  idle. That background work keeps the Workspace awake for at most 2 hours by default (the
  deployment can change the bound); after that the Workspace can stop as idle while the command
  still runs. For a longer build, use **Keep awake**
  ([05](05-stopping-and-tidying.md#keeping-a-session-awake)). A turn that agy never closes (the CLI died
  mid-turn) stops reading Working after an hour with no change, unless one of its commands is
  still running.
- **Agent Fleet adds a hook entry to agy's configuration.** To know which conversation a session
  holds and whether it is working, it adds an entry named `agent-fleet` to
  `~/.gemini/config/hooks.json` and writes it again at every launch. Entries under other names
  in that file are kept. When the hook has not reported, the state is read from agy's
  conversation database instead. Each launch also marks the working folder as trusted, so agy
  does not stop on its trust prompt, and keeps agy's telemetry setting off.
- **Image generation.** "Antigravity (agy)" is one of the image routes a session can use
  ([13](13-images.md#having-a-session-generate-an-image)). It goes through agy's own
  image-generator subagent and makes one picture per call. An edit takes up to 3 reference
  images (the Codex route takes 5); they are copied into the run's own folder, and that folder
  is the only place the run may read. When the signed-in account is not offered image
  generation, the call fails with "the signed-in Antigravity/Gemini account is not offered image
  generation (agy says: …)".
- RTK works by instruction only, as best effort ([RTK](#rtk-token-savings)).

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

The **Behavior** settings on the same card let you set the model and the reasoning effort,
and switch Muse Code's **contributor mode**. While it is on, Meta may use your conversations
to improve the product. It is off by default, and each session keeps the mode it was created
with across a stop and start. See [Agents reference](../ref/agents.md) for the details.

In a running muse session the **`/`** button beside the input lists the session's own skills,
and picking one runs it ([04](04-conversation.md#calling-a-skill-or-a-command); what the list
holds is in the [Agents reference](../ref/agents.md)). When a usage limit stops a muse turn, the
session resumes by itself once the limit resets (the states are in the same reference).

The card shows how muse is billed. Signed in with a Meta account it uses your plan; connected with
an API key it bills per use. If an API key is also set in the workspace's environment variables,
the card warns that it takes priority over the stored sign-in, so you are billed per use whatever
the card says; remove that variable to use your plan.

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

Read [lcpp: what hardware measurement found](../ref/agents.md#lcpp-what-hardware-measurement-found)
before your first session: the first turn can take minutes, the context window has a minimum worth
choosing, and only some model families are verified.

What else to know:

- **External (MCP) tools are loaded on demand.** Once the tools from your enabled MCP servers add up to more than a few thousand characters of schema, the model is not sent them all on every turn. It gets three small tools instead (`tool_search`, `tool_describe`, `tool_call`) and looks up the tool it needs. This keeps a small context window free. Approvals work as for any other tool, and the transcript shows the tool that was actually called. lcpp's own tools (read, edit, bash and so on) are always sent. An administrator can force either behaviour for the whole deployment.


## Checking remaining context

In the sessions of the kinds that have a context gauge (the "Context usage gauge" row of the
[Agents reference](../ref/agents.md)), a **"Context"** gauge (`ctx` when the screen is
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
- Which kinds can receive it is the "Receives your agent instructions" row of the
  [Agents reference](../ref/agents.md) (opencode takes it through a marked block; see
  [OpenCode](#opencode)). A kind that cannot still appears in the list with the reason.
- Each row shows **where it goes** (the file it was written to, or for lcpp the system prompt) and **whether it is actually in effect**. When
  something saved but isn't in effect, that row says why.
- There is a length limit: this text rides along in **every session's context, every time**, so
  shorter works better.
- **Don't put secrets (API keys, tokens) here.** It is plain text that several agents read.
- While Agent memory is on, Agent Fleet also writes a short fixed note on its memory tools into the same
  instruction file, beside your text (copilot and Kiro get a file of their own; lcpp gets it in the system
  prompt). It never contains memories, and turning Agent memory off removes it without touching your text
  ([18 Agent memory](18-agent-memory.md#settings)).

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
  `session-push-notification`, plus a `PreModelSwitch` entry that answers "allow" so `/model` does not stop
  on claude's "Switch model?" cache warning (the next message then re-reads the whole history at
  uncached prices). The RTK entry (`rtk hook claude`) is switched by
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

---
audience: "everyone"
source_of_truth: "this table; the columns and the marked rows are checked against the code"
---

# Agents — what each kind can do

English | [日本語](agents.ja.md)

Eleven session kinds exist. Nine drive (or are meant to drive) a coding agent (lcpp and
muse are managed-only and have no Terminal (CLI) route at all; see the footnote on that row).
`shell` and `ssm` are terminals with no agent behind them, and they are in the table
because "does this apply to a plain shell session?" is a real question.

✓ = supported, — = not supported or not applicable.

| Capability | claude | codex | opencode | copilot | cursor | kiro | agy | lcpp | muse | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed execution (no terminal) | — | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | — | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | —⁹ | —⁹ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Read-only history while stopped | ✓ | ✓ | ✓ | ✓ | —³ | ✓ | ✓ | ✓ | ✓¹² | — | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓²² | ✓²² | ✓ | ✓¹⁹ | ✓¹⁴ | — | — |
| Reasoning effort | ✓ | ✓ | ✓ | ✓ | —² | —⁵ | —² | — | ✓ | — | — |
| Plan mode | ✓ | ✓ | ✓ | ✓²¹ | ✓ | — | — | ✓ | — | — | — |
| Context usage gauge | ✓ | ✓ | ✓ | — | — | ✓ | — | ✓ | ✓ | — | — |
| Image paste | ✓ | ✓ | ✓⁶ | — | — | — | ✓ | — | ✓ | — | — |
| Copy the conversation into a new session | ✓ | ✓ | ✓ | ✓ | — | — | — | ✓ | ✓ | — | — |
| Fork from a past message | ✓ | ✓²⁰ | ✓²⁰ | ✓ | — | — | — | ✓ | ✓ | — | — |
| Choosing to skip permission prompts | ✓ | — | — | ✓ | ✓ | ✓ | ✓ | ✓ | —¹³ | — | — |
| Skill / command picker | ✓ | ✓ | ✓ | —⁴ | ✓ | —⁴ | —⁴ | —⁴ | ✓⁴ | — | — |
| Handoff to another session | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Start in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | ✓⁷ | — | ✓ | — | ✓¹¹ | — | — |
| Usage / remaining-quota chip | ✓ | ✓ | — | ✓ | — | — | ✓ | — | ✓¹⁵ | — | — |
| Receives your agent instructions | ✓ | ✓ | ✓ | ✓ | —⁸ | ✓ | ✓ | ✓¹⁰ | ✓¹⁶ | — | — |
| Receives integration (MCP) servers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓¹⁸ | ✓¹⁷ | — | — |
| Shares Agent Fleet memory (af memory tools) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Agent memory is version-managed | ✓ | ✓ | — | — | — | — | — | — | — | — | — |
| RTK (token savings) | ✓²³ | ✓²³ | ✓²³ | ✓²³ | — | — | ✓²³ | — | — | — | — |
| Starts child sessions and messages other sessions | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | ✓²⁴ | — | — |
| Bound to an image studio | ✓²⁵ | ✓²⁵ | ✓²⁵ | ✓²⁵ | ✓²⁵ | ✓²⁵ | ✓²⁵ | ✓²⁵ | — | — | — |

¹ copilot's model list depends on the plan: Free offers only "Auto (Copilot picks)".

² cursor and agy fold the reasoning effort into the model name, so there is no
separate control.

³ cursor's managed execution keeps no local transcript, so a **stopped** cursor
session has nothing to show. The live mirror works while it runs (with simplified tool output), and running cursor
as Terminal (CLI) does persist a readable history. kiro, by contrast, keeps a readable
transcript even under Managed.

⁴ The picker lists what the CLI can discover and launch by itself, and copilot, kiro,
agy and lcpp have no verified mechanism for that (lcpp drives no CLI at all, so there is
nothing of its own to discover). Skills written to another convention's `SKILL.md` tree in
the repository are still offered to them by injection.

muse is the exception on this row, and it is a different mechanism from the other ✓s: its
own protocol carries the list, so what the picker offers is the running session's own
answer: Muse Code's bundled skills, plugin skills, your own under `~/.config/muse/skills`,
and the working copy's `.agents/skills/`. Picking one sends it as a skill invocation rather
than as the text of its name, which is what makes it run. It needs a running session,
because the list belongs to the session; before that, and for a stopped one, the injection
route is what remains.

⁵ kiro accepts an effort flag but exposes no per-model picker.

⁶ opencode's image paste depends on the model behind the provider key.

⁷ cursor's assistant runs read-only.

⁸ Cursor keeps User Rules in your Cursor account and has no local per-user place for
instructions, so it is the one kind that cannot receive them. It still appears in the
settings list, with that reason shown.

⁹ lcpp (ADR 0093's own llama.cpp harness) and muse (Meta's Muse Code) have no Terminal
(CLI) route at all, by design. lcpp has no vendor CLI to put in a pane, only a managed
runtime this process runs in-process; muse has one, but its session protocol already
carries more than a pane would show, so Agent Fleet drives that instead. Creating a
session of either kind with no driver specified defaults it to Managed rather than
trying (and failing) to launch a terminal.

¹⁰ lcpp drives no CLI to write your instructions into. Instead, the harness reads the
same fleet/your-own/project instruction layers itself and folds them into the system
prompt on every turn. Its row in ⚙Settings → "Agent instructions" is the switch for your own
layer, and takes effect from the next turn, running sessions included.

## How to sign in

| Kind | Sign-in |
|---|---|
| claude | OAuth: approve in your browser, then paste the code back. Shows the account email and plan once connected. |
| codex | A ChatGPT subscription via device code (turn on device-code authentication in ChatGPT's security settings first), or an OpenAI API key. |
| opencode | Two controls. **"Use opencode"** (off by default; while off, stored keys and sign-ins are ignored) and **"opencode.ai billing"** (None (my own keys) / Free models only / Go (subscription) / Zen (metered)). The latter decides how opencode.ai is used only — the providers you connect yourself stay in the list on every choice. Keys are the API key of whichever LLM provider you want, stored as an environment variable (presets fill the name in; several at once). |
| copilot | None of its own — connecting GitHub as a git provider connects it, and disconnecting GitHub disconnects it. The account needs a Copilot subscription, including the Free plan. |
| cursor | Open the authorize link and approve in your browser. There is no code to paste. A Cursor account is required; API keys are not accepted. |
| kiro | Device flow: open the link with the confirmation code and approve (Builder ID, Google, GitHub…). API keys are not accepted. The CLI is large and is installed on demand the first time unless the deployment bakes it in. |
| agy | Sign in from its card in the agent settings. |
| lcpp | None — the deployment's engine needs no sign-in. Its card has **"Use llama.cpp"** (the switch; Off removes it from the launch menus and refuses a launch by any route) and **"Your own connection"** (URL, optional API key, **"Check connection"**), which points sessions at a llama-server on your own network instead of the deployment's engine. |
| muse | **"Install Muse Code"** once (about 299 MB into your home), then **"Sign in with your Meta account"** (approve in the browser — no code to paste; the subscription route) or **"Use an API key"** (per-use billing; saving a key removes a stored account sign-in). |
| shell | Not applicable. |
| ssm | Uses the workspace's AWS SSM connection. |

## States shown in the mirror

| Kind | States |
|---|---|
| claude | Working / Question / Plan ready / Awaiting permission / Ready |
| codex | Working / Question / Plan ready / Ready |
| opencode | Working / Question / Ready |
| copilot | Working / Awaiting permission / Ready |
| cursor | Working / Awaiting permission / Plan ready / Ready |
| kiro | Working / Question / Ready |
| agy | Working / Question / Awaiting permission / Ready |
| lcpp | Working / Ready |
| muse | Working / Question / Ready |

On a Terminal (CLI) session, cursor stops for a command approval only when **Tool
permissions** asks for approval or the session started in plan mode, and for a build
decision only in a plan launch. The mirror shows the state but draws no card for it:
answer in the terminal. Text sent from anywhere while it waits is refused, because the
menu would take its Enter as "run" or "build".

kiro shows a tool approval it is waiting for as **Question**, not as Awaiting permission.
lcpp's tool approvals and its questions appear as cards in the mirror while the state
stays Working. muse asks for no tool approvals (footnote 13), so it never shows Awaiting
permission.

After a usage limit stops a turn, claude, codex under Managed and muse show **Waiting for
limit reset** with the time, and resume by themselves once the limit resets. When a turn
has ended but work it started is still running, claude, codex under Managed, agy and muse
show **Ready · running in background**.

`shell` and `ssm` have no conversation and therefore no state model and no
notifications.

A stopped session of any kind with a conversation can be resumed, except that claude, codex,
opencode, copilot, cursor, kiro, agy, lcpp and muse cannot resume when the working folder they
were launched in is gone (shell falls back to home); see
[05 Stopping and tidying up](../member/05-stopping-and-tidying.md#when-you-can-and-cant-resume).

## Stopping a turn, and messages sent while one runs

How Stop treats a waiting queue is explained in
[04 Talking to a session](../member/04-conversation.md#stopping-a-turn-when-something-is-queued);
this is what differs by kind.

**Managed**

- **A message sent while a turn runs.** codex and muse do not queue it: the agent takes it into
  the running turn at once, so there is usually no need to stop. If you stop anyway, the message
  ends with that turn, and the next turn still sees it in the conversation. The same holds for
  **Send now** on the waiting list. Every other kind accepts the message and handles it after the
  running turn.
- **A stop that arrives while a message is being handed over.** On codex and muse it still stops
  the message the moment the agent starts it (lcpp runs inside the Agent and stops it the same
  way). On copilot, cursor, kiro and opencode that stop is best effort: the message can take a
  first step before it stops.
- **Cancelling a question** stops the turn on codex only. The other kinds answer the question as
  declined and leave the queue alone.

**Terminal (CLI)**

Stop sends the key the CLI stops a turn with: Esc, two Esc for opencode, Ctrl+C for cursor and
copilot. cursor and copilot get Ctrl+C only while the turn is running; otherwise Stop answers
that nothing is running. This is what each CLI did with a prompt typed while its turn was
running, measured once per CLI. They are observations, not promises, and a CLI update can change
them:

| CLI | Stop while a prompt is queued | The pane is closed while a prompt is queued |
|---|---|---|
| claude | The turn stops and the queued prompt runs next | The queued prompt is lost |
| codex | The turn stops and the queued prompt runs next | The queued prompt still runs: codex's background process outlives the pane |
| opencode | Stop sends two Esc about half a second apart (one Esc only arms the interrupt). The turn stops and the queued prompt runs next | The queued prompt still runs: opencode's background server outlives the pane |
| cursor | Stop sends Ctrl+C: the turn stops and the prompt is left unsent in the CLI's panel (a plain Esc would cancel the queued prompt and let the turn run on) | Not restored or run when the CLI resumes |
| kiro | The turn stops and the queued prompt runs next | Not restored or run when the CLI resumes |
| copilot | Stop sends Ctrl+C: the turn stops and the queued prompt is dropped (a plain Esc does not interrupt) | Not restored or run when the CLI resumes |
| agy | Not measured (Stop still sends one Esc; the reply says the key is unmeasured) | Not measured |

**Messages from other sessions to a Terminal (CLI) session** follow the same rules, with these
reports to the sender:

- A message to a claude session in the middle of a turn is reported as queued, not delivered:
  claude keeps it in memory only, so ending the pane before the turn ends (stopping, archiving or
  recreating the session) loses it.
- A message to a claude session whose CLI was launched less than three minutes ago (for example one
  the send just resumed) is reported as queued, waiting on `starting`: text typed while claude
  boots can be lost, so the message is held and delivered once the pane has sat idle at its prompt
  for about 45 seconds.
- A message to a cursor session whose CLI refuses it at submit (for example a model the plan
  cannot use) is reported the same way, with the CLI's error text: the message stays in the input
  box and nothing runs it, so the sender is told not to resend.
- A message to a cursor session whose input box still holds unsent text (a prompt the CLI refused
  earlier, or a draft) is refused, because cursor would run the two as one prompt. The sender is
  told to have that text sent or cleared in the pane first.

## Moving a session to another agent

A move ([05 Stopping and tidying up](../member/05-stopping-and-tidying.md#moving-a-session-to-another-agent-move))
has two sides, and a kind can be on one without the other.

| Kind | As the source | As the target |
|---|:--:|:--:|
| claude | ✓ | ✓ |
| codex | ✓ | ✓ |
| opencode | ✓ | ✓ |
| copilot | ✓ | ✓ |
| cursor | ✓ (not a stopped Managed session) | — (not yet verified) |
| kiro | ✓ | ✓ |
| agy | ✓ | ✓ |
| lcpp | ✓ | — (cannot read the handover file) |
| muse | ✓ | ✓ |
| shell / ssm | — | — |

- **A stopped Managed cursor session cannot be moved.** Its conversation lived only in the
  running agent, so nothing is left to write the handover file from.
- **cursor is not offered as a target** until reading the handover file through its Managed
  driver has been verified.
- **lcpp is not offered as a target** because its file tools refuse paths outside the working
  folder, and the handover file lives outside it.
- A move always goes to another kind. To continue in the same kind, fork the session where the
  kind can fork. shell and ssm have no conversation to hand over.

## Each agent's own memory

What the switch **"Let sessions use Agent Fleet memory"**
([18 Agent memory](../member/18-agent-memory.md#settings)) does to the memory a kind keeps by itself.

| Kind | While the switch is on | Carried into Agent Fleet memory | Written back when it goes off |
|---|---|---|---|
| claude | New sessions start with Claude Code's auto-memory off | ✓ (imported in the background) | ✓ |
| codex | codex's `features.memories` is switched off, and your value is restored when the switch goes off | — | — |
| muse | New hosts start with Muse Code's native memory off | Once, with **Import from muse** | — |
| other kinds | Nothing is changed | — | — |

## Choosing one

Choose by the subscription you already pay for. An Anthropic account → **claude**;
ChatGPT or the OpenAI API → **codex**; switching between several providers' API keys →
**opencode**; a GitHub Copilot subscription → **copilot**; a Cursor plan → **cursor**;
an AWS Builder ID or a Kiro plan → **kiro**; a Meta account (or a Meta Model API key) →
**muse**. If you want a model that needs no vendor account at all, choose **lcpp**: it runs on
your organization's own engine (or a llama-server on your network), so there is no sign-in and no
subscription limit.

All of them show the conversation, let you answer from the mirror, and hand a
conversation off to another agent. The differences that most often decide it in
practice are the context gauge (claude / codex / opencode / kiro / lcpp / muse), image paste, and
whether you want Managed execution: Codex and opencode carry no per-session process
at all, which is what makes them comfortable to run many of at once.

## lcpp: what hardware measurement found

`lcpp` (ADR 0093) runs its own instance of the llama.cpp engine instead of a vendor's
hosted API, and hardware measurement across sixteen live runs found costs
worth knowing before your first session.

- **The first turn can take minutes.** If the engine instance was stopped, waking it
  is a genuine cold start, measured at roughly 3.5 to 7 minutes, most runs landing
  in the 4–5 minute band (one measured run hit 302 seconds and timed out). The
  session is not stuck; it is buying and starting an instance. Every turn after the
  first, while the instance stays warm, is fast.
- **Choose a context window of 8000 tokens or more.** At a window of 3500, some
  model families compact so often the harness deliberately stops rather than loop
  forever, reproduced on both Gemma and Qwen3-Coder. At 8000 both complete
  cleanly; at 24000 compaction never fires at all. That stop is working as
  designed, not a malfunction.
- **The same task costs a very different number of turns by family**, even at the
  same window. On one measured benchmark (five fixes to a small project plus a
  recall question), Qwen3.8 took 29–30 turns, Gemma-4 took 63, GPT-OSS took 95, and
  Qwen3-Coder took 164 (Gemma-4 and Qwen3-Coder both at window 8000). All four
  finished the task correctly, so the gap is cost, not correctness.
- **Verified working model families: Qwen3, GPT-OSS and Gemma** (four checkpoints
  measured). `llama-3.1-8b-instruct-q4_k_m` specifically does **not** work: this
  engine build has no Llama-specific tool-call parser, so its tool calls fail to
  parse. Not every catalogue entry behaves the same; stick to a verified family.
- **Swapping the model does not require swapping the instance.** Across every
  measured run after the first, moving to a different model on the same engine
  never triggered another cold start. Only the very first purchase pays that cost.

`lcpp` has no Terminal (CLI) route at all (see footnote 9 above); it only runs
Managed.

## muse: what Agent Fleet does not see

Muse Code brings its own versions of things Agent Fleet also does. They keep working
inside a muse session, and Agent Fleet sees little or nothing of them, so if you use them,
expect the Console to tell you less than Muse Code itself would.

- **Runs a muse session schedules for itself.** A muse session can put a prompt on its
  own timer and run it later without you. Those are Muse Code's jobs, not Agent Fleet's:
  they do not appear in the Console's scheduled runs, Agent Fleet cannot cancel one, and
  only the session itself can list or delete them: ask it to. When one fires, the mirror
  shows a one-line `scheduledFire` row for it, so you can see that it ran. They fire while that
  session is running, and a repeating one expires by itself after seven days. Agent Fleet
  cannot switch the feature off, either: Muse Code has no setting for it, and its one
  control that would remove those tools removes every integration (MCP) tool along with
  them, including the ones Agent Fleet gives the session. The "Scheduled (unattended)
  runs" row above is about Agent Fleet's own schedules, which is a different thing.
- **Messages muse sessions send each other, and the names they use.** Muse Code has its
  own cross-session messaging and its own registry of session names, both kept per user
  in your home directory. Agent Fleet is connected to neither: a message sent that way
  never reaches the mirror, the conversation or your notifications, and the names in that
  registry are not the session names the Console shows. To have one session talk to
  another, use Agent Fleet's own cross-session messaging, which is mirrored.
- **Its own view of your muse conversations.** One workspace keeps one Muse Code store
  for all of your muse sessions, so Muse Code's own session list is per user rather than
  per session: measured, a muse session listed a conversation belonging to a muse session
  it had nothing to do with. Agent Fleet never offers a muse conversation it did not
  start, but the session itself can reach the others through Muse Code's own commands and
  its `read-session` skill.

## Not in this table

- **Rovo Dev** was studied as a further agent kind and is not implemented.
- Per-kind quirks that matter only while debugging a driver belong in
  `docs/build/`.

> Agents run commands, edit files and push on your behalf: unattended in scheduled
> runs, and without asking each time in permission-skipping modes. `shell` and `ssm`
> run what you send verbatim. Keep backups, use least-privilege credentials, and lean
> on the approval gates.

¹¹ Two things have to be true before muse appears in the launch menu, and equally in the
assistant's choice of agent: Muse Code is proprietary and not included in the image, so it has
to be installed on demand (the Muse Code connection card offers it, ~299MB into your home), and
you have to be signed in: an unauthenticated session would accept work and then fail every
turn. As the assistant, muse answers one prompt per turn as its own headless run, and it
remembers the conversation: ask a follow-up and it has the earlier turns. It answers rather than
acts: Agent Fleet runs those turns with shell, file writing and web tools switched off, and
measured twice, a chat turn asked to write a file made no tool call and wrote nothing.

¹² Agent Fleet keeps its own copy of a muse conversation as it happens, so a stopped
session still shows its history. Muse Code's own session file is a runtime log in its
internal format rather than a readable transcript, so it is not the source here.

¹³ 🔴 A muse session asks for no tool approvals, so there is nothing to choose to skip.
This is the one row where the dash means *less* safety, not a missing feature. Muse
Code's own sandbox cannot be built inside a Workspace container (the container's security
profile refuses it), and the flag that turns the sandbox off also leaves the session's
filesystem and local network unrestricted: every tool call is allowed by policy before any
approval is considered. Measured, a muse turn wrote a file outside its working copy with no
prompt. So treat a muse session as having the same reach over this container as `shell`
does, and read the warning above as applying to it in full.

¹⁴ Muse Code has a **contributor mode**: while it is on, Meta may use your conversations,
including messages between sessions, to improve the product. Agent Fleet runs every muse
process that handles a conversation with the mode set explicitly. It is **off** unless you switch it on under Settings ›
Agents › Muse Code › Behaviour › **Contributor mode**. Each session keeps the mode it was
created with, across a stop and start and an Agent restart, and a fork or recreate keeps it
too. Changing the setting applies to sessions created afterwards, and to assistant chat and
AI assist turns from the next one. A session cannot switch the setting on by itself: only a
change you make from the Console can.

Muse Code 1.3 offered the same choice as a "-contributor" twin of every model, and made that
twin its own default. Where a host still lists those twins, Agent Fleet never picks one for
you: a session launched on **Default** runs on the newest model without the clause, and the
twins stay in the picker for anyone who wants one. The default model and the reasoning effort
(`none` … `ultra`) are chosen on the same Behaviour settings.

The assistant chat follows the same rule, under Settings › AI: left on the recommendation it
runs on the newest model without the clause, and if the catalogue cannot be read at all the
turn is refused rather than quietly run on Muse Code's default.

¹⁵ The muse chip shows what a running muse session last observed, not a number Agent Fleet
can go and fetch: Muse Code reports its own subscription usage over the session protocol, and
only after a turn finishes. Until you have run muse in this workspace the chip shows "—",
which means "no reading yet" rather than "nothing used". The first window's length is the
provider's own (measured: five hours), so the row is labelled "current window".

¹⁶ Muse Code keeps your personal rules in one file, `~/.config/muse/AGENTS.md`, and both the
workspace policy and your own instructions have to go there. Agent Fleet writes them as two
marked blocks and leaves everything else in the file alone, so rules you put there yourself
survive. The workspace's topic files arrive separately, as skills under
`~/.config/muse/skills/`. Your repository's own `AGENTS.md` is read as well, because sessions
run with the workspace trusted. Note that Muse Code treats `AGENTS.md` and `CLAUDE.md` as
a precedence, not a sum: with both present it uses `AGENTS.md` and says it is skipping the
other.

¹⁷ muse is one of the two kinds (lcpp is the other; see footnote 18) whose integration servers
Agent Fleet does not write into a
configuration file: they are handed to Muse Code when the session starts, which means the set
can differ per session rather than being one list for your whole workspace. Two things follow.
A server you add starts being used by sessions launched **after** the change; a running
session keeps the set it was given. And each server starts when the session's first turn runs,
not when the session opens, so a brand-new session shows nothing connected until you send
something. Every server is passed as optional, so one that fails to start costs you that
integration and not the session.

¹⁸ lcpp has no configuration file either: it drives no vendor CLI, so the enabled servers are
read at the **start of every turn** and connected by Agent Fleet itself. A server you enable or
disable therefore takes effect on the next turn of a session that is already open; you do not
have to restart it (this is where lcpp differs from muse, footnote 17). A server that cannot be
reached costs you its tools for that turn and never the turn itself; the failure is noted once
in the conversation rather than on every turn. One consequence of lcpp running the tools itself:
**every tool an integration server offers asks for your permission before it runs**, because
nothing in the MCP protocol tells Agent Fleet which of them only read.

¹⁹ lcpp's model list is your own llama.cpp server's, so it has no "let the tool decide" entry
the way the vendor CLIs do: a launch waits until you pick one. Which model you pick is the
cost decision on this kind: on one measured benchmark the same task took 29 turns on one
family and 164 on another, and both finished it correctly. Swapping models later does not buy
a new instance (see "lcpp: what hardware measurement found" above).

²⁰ codex and opencode can fork only in Managed execution: their CLI launch command has no way to
pass a branch point. (lcpp and muse are always Managed.) One exception: an opencode 2 Terminal
(CLI) session attached to the shared server can fork too, because that server takes the branch
point. A Terminal session on a private server, or on opencode 1.x, cannot.

²¹ copilot's plan mode is set at launch, and can also be switched from the managed settings.

²² cursor's list is exactly the models available to the signed-in account (fetched live); kiro offers named models (Auto, Claude Sonnet / Haiku and others) even on the Free plan.

²³ RTK rewrites the commands an agent runs so that their output costs fewer tokens. It is
on or off per agent in ⚙Settings → "Agents", and does nothing when the workspace image has no
`rtk`. How it takes effect differs. claude, opencode and copilot have a hook or plugin that
rewrites each shell command before it runs, so it applies every time (copilot: to sessions
started after the change). codex and agy have no way to rewrite a command, so Agent Fleet
only adds an instruction block asking the agent to use `rtk`; whether it does is up to the
agent (best effort). cursor, kiro, lcpp and muse have no RTK setting. This row is checked
against the code: a kind is marked when its driver ships an RTK artifact.

²⁴ Both are opt-in settings, off by default (Settings › Agents › Session). A session starts a
child, or sends a message, with tools of Agent Fleet's own MCP server, which every kind marked in
"Receives integration (MCP) servers" gets; so this row follows that one, and the check ties it
to the same code field. The other end accepts every kind in the row: a child can be any of
these nine kinds, and any of them can be the target of a message (a stopped session is resumed
first). `shell` and `ssm` can do neither, because text sent to them would run as a command.

²⁵ The studio's agent session is bound under either execution method, except where the agent
cannot tell the studio's tools which session it is. For opencode that is its Managed mode (one
server is shared by every session of a folder, so a write could not be traced to the session that
made it), and for copilot, cursor and kiro their Managed mode (not told their session name yet);
those four bind in Terminal (CLI) only. claude and agy have no Managed mode, so Terminal (CLI) is
their only route anyway; codex binds under either method, and lcpp (Managed only, no Terminal
(CLI) route) binds as Managed. muse has no Terminal (CLI) route to fall back on and its Managed
mode is refused for the same reason, so a muse session cannot be bound yet. The binding is
checked against the code. A worktree is on by default for the studio's session; as the Console
offers it, only a Terminal (CLI) session and lcpp can turn it off.

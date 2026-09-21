---
audience: "everyone"
source_of_truth: "this table; the columns and the marked rows are checked against the code"
updated: "2026-09"
---

# Agents — what each kind can do

English | [日本語](agents.ja.md)

Eleven session kinds exist. Nine drive (or are meant to drive) a coding agent — lcpp and
muse are managed-only and have no Terminal (CLI) route at all, see the footnote on that row;
`shell` and `ssm` are terminals with no agent behind them, and they are in the table
because "does this apply to a plain shell session?" is a real question.

✓ = supported, — = not supported or not applicable.

| Capability | claude | codex | opencode | copilot | cursor | kiro | agy | lcpp | muse | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed execution (no terminal) | — | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | — | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | —⁹ | —⁹ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Read-only history while stopped | ✓ | ✓ | ✓ | ✓ | —³ | ✓ | ✓ | ✓ | ✓¹² | — | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓ | ✓ | ✓ | — | ✓¹⁴ | — | — |
| Reasoning effort | ✓ | ✓ | ✓ | ✓ | —² | —⁵ | —² | — | ✓ | — | — |
| Plan mode | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | ✓ | — | — | — |
| Context usage gauge | ✓ | ✓ | ✓ | — | — | ✓ | — | — | ✓ | — | — |
| Image paste | ✓ | ✓ | ✓⁶ | — | — | — | ✓ | — | —¹¹ | — | — |
| Copy the conversation into a new session | ✓ | ✓ | ✓ | ✓ | — | — | — | ✓ | ✓ | — | — |
| Fork from a past message | ✓ | ✓ | ✓ | ✓ | — | — | — | ✓ | ✓ | — | — |
| Choosing to skip permission prompts | ✓ | — | — | ✓ | ✓ | ✓ | ✓ | ✓ | —¹³ | — | — |
| Skill / command picker | ✓ | ✓ | ✓ | —⁴ | ✓ | —⁴ | —⁴ | —⁴ | —⁴ | — | — |
| Handoff to another session | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | —¹¹ | — | — |
| Start in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓ | — | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | —¹¹ | — | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | ✓⁷ | — | ✓ | — | —¹¹ | — | — |
| Usage / remaining-quota chip | ✓ | ✓ | — | ✓ | — | — | ✓ | — | ✓¹⁵ | — | — |
| Receives your agent instructions | ✓ | ✓ | ✓ | ✓ | —⁸ | ✓ | ✓ | ✓¹⁰ | ✓¹⁶ | — | — |
| Receives integration (MCP) servers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | ✓¹⁷ | — | — |
| Agent memory is version-managed | ✓ | ✓ | — | — | — | — | — | — | — | — | — |

¹ copilot's model list depends on the plan: Free offers only "Auto (Copilot picks)".

² cursor and agy fold the reasoning effort into the model name, so there is no
separate control.

³ cursor's managed execution keeps no local transcript, so a **stopped** cursor
session has nothing to show. The live mirror works while it runs, and running cursor
as Terminal (CLI) does persist a readable history. kiro, by contrast, keeps a readable
transcript even under Managed.

⁴ The picker lists what the CLI can discover and launch by itself, and copilot, kiro,
agy, lcpp and muse have no verified mechanism for that (lcpp drives no CLI at all, so
there is nothing of its own to discover; muse's own protocol does carry one, and Agent
Fleet has not built that half yet). Skills written to another convention's `SKILL.md`
tree in the repository are still offered to them by injection — measured for muse.

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
prompt on every turn.

## How to sign in

| Kind | Sign-in |
|---|:--:|---|
| claude | OAuth: approve in your browser, then paste the code back. Shows the account email and plan once connected. |
| codex | A ChatGPT subscription via device code (turn on device-code authentication in ChatGPT's security settings first), or an OpenAI API key. |
| opencode | Two controls. **"Use opencode"** (off by default; while off, stored keys and sign-ins are ignored) and **"opencode.ai billing"** (None (my own keys) / Free models only / Go (subscription) / Zen (metered)). The latter decides how opencode.ai is used only — the providers you connect yourself stay in the list on every choice. Keys are the API key of whichever LLM provider you want, stored as an environment variable (presets fill the name in; several at once). |
| copilot | None of its own — connecting GitHub as a git provider connects it, and disconnecting GitHub disconnects it. The account needs a Copilot subscription, including the Free plan. |
| cursor | Open the authorize link and approve in your browser. There is no code to paste. A Cursor account is required; API keys are not accepted. |
| kiro | Device flow: open the link with the confirmation code and approve (Builder ID, Google, GitHub…). API keys are not accepted. The CLI is large and is installed on demand the first time unless the deployment bakes it in. |
| agy | Sign in from its card in the agent settings. |
| shell | Not applicable. |
| ssm | Uses the workspace's AWS SSM connection. |

## States shown in the mirror

| Kind | States |
|---|:--:|---|
| claude | Working / Question / Plan ready / Awaiting permission / Ready |
| codex | Working / Question / Plan ready / Ready |
| opencode | Working / Question / Ready |
| copilot | Working / Awaiting permission / Ready |
| cursor | Working / Ready |
| kiro | Working / Awaiting permission / Ready |

`shell` and `ssm` have no conversation and therefore no state model and no
notifications. agy's states are not separately documented — treat its mirror as
best-effort.

## Choosing one

Choose by the subscription you already pay for. An Anthropic account → **claude**;
ChatGPT or the OpenAI API → **codex**; switching between several providers' API keys →
**opencode**; a GitHub Copilot subscription → **copilot**; a Cursor plan → **cursor**;
an AWS Builder ID or a Kiro plan → **kiro**.

All of them show the conversation, let you answer from the mirror, and hand a
conversation off to another agent. The differences that most often decide it in
practice are the context gauge (claude / codex / opencode / kiro), image paste, and
whether you want Managed execution — Codex and opencode carry no per-session process
at all, which is what makes them comfortable to run many of at once.

## lcpp: what hardware measurement found

`lcpp` (ADR 0093) runs its own instance of the llama.cpp engine instead of a vendor's
hosted API, and hardware measurement across sixteen live runs found real,
worth-knowing costs before your first session.

- **The first turn can take minutes.** If the engine instance was stopped, waking it
  is a genuine cold start — measured at roughly 3.5 to 7 minutes, most runs landing
  in the 4–5 minute band (one measured run hit 302 seconds and timed out). The
  session is not stuck; it is buying and starting an instance. Every turn after the
  first, while the instance stays warm, is fast.
- **Choose a context window of 8000 tokens or more.** At a window of 3500, some
  model families compact so often the harness deliberately stops rather than loop
  forever — reproduced on both Gemma and Qwen3-Coder. At 8000 both complete
  cleanly; at 24000 compaction never fires at all. That stop is working as
  designed, not a malfunction.
- **The same task costs a very different number of turns by family**, even at the
  same window. On one measured benchmark (five fixes to a small project plus a
  recall question), Qwen3.8 took 29–30 turns, Gemma-4 took 63, GPT-OSS took 95, and
  Qwen3-Coder took 164 (Gemma-4 and Qwen3-Coder both at window 8000). All four
  finished the task correctly — the gap is cost, not correctness.
- **Verified working model families: Qwen3, GPT-OSS and Gemma** (four checkpoints
  measured). `llama-3.1-8b-instruct-q4_k_m` specifically does **not** work: this
  engine build has no Llama-specific tool-call parser, so its tool calls fail to
  parse. Not every catalogue entry behaves the same — stick to a verified family.
- **Swapping the model does not require swapping the instance.** Across every
  measured run after the first, moving to a different model on the same engine
  never triggered another cold start. Only the very first purchase pays that cost.

`lcpp` has no Terminal (CLI) route at all — see footnote 9 above — it only runs
Managed.

## muse: what Agent Fleet does not see

Muse Code brings its own versions of things Agent Fleet also does. They keep working
inside a muse session, and Agent Fleet has no view of them — so if you use them, use
them knowing that no Agent Fleet screen will show what happened.

- **Runs a muse session schedules for itself.** A muse session can put a prompt on its
  own timer and run it later without you. Those are Muse Code's jobs, not Agent Fleet's:
  they do not appear in the Console's scheduled runs, Agent Fleet cannot cancel one, and
  only the session itself can list or delete them — ask it to. They fire while that
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

> Agents run commands, edit files and push on your behalf — unattended in scheduled
> runs, and without asking each time in permission-skipping modes. `shell` and `ssm`
> run what you send verbatim. Keep backups, use least-privilege credentials, and lean
> on the approval gates.

¹¹ Two things have to be true before muse appears in the launch menu: Muse Code is
proprietary and not included in the image, so it has to be installed on demand (the Muse Code
connection card offers it, ~299MB into your home), and you have to be signed in — an
unauthenticated session would accept work and then fail every turn.

The rows carrying this footnote are the ones Agent Fleet has not built for muse. They are not
blocked by Muse Code: the protocol carries image attachments, and the handoff and bridge paths
are not written per agent at all. They are simply unverified here, and this table only ticks a
row that was seen to work end to end — which is why the worktree, scheduled-run and context
gauge rows are now ticked and these are not: those were watched working, on a real muse session,
before the tick was written.

¹² Agent Fleet keeps its own copy of a muse conversation as it happens, so a stopped
session still shows its history. Muse Code's own session file is a runtime log in its
internal format rather than a readable transcript, so it is not the source here.

¹³ 🔴 A muse session asks for no tool approvals, so there is nothing to choose to skip —
and this is the one row where the dash means *less* safety, not a missing feature. Muse
Code's own sandbox cannot be built inside a Workspace container (the container's security
profile refuses it), and the flag that turns the sandbox off also leaves the session's
filesystem and local network unrestricted: every tool call is allowed by policy before any
approval is considered. Measured, a muse turn wrote a file outside its working copy with no
prompt. So treat a muse session as having the same reach over this container as `shell`
does, and read the warning above as applying to it in full.

¹⁴ Muse Code's catalogue lists a "-contributor" twin of every model — the same model at the
same price, except that Meta may use those conversations, including messages between
sessions, to improve the product. It is Muse Code's own default. Agent Fleet does not pick it
for you: a session launched on **Default** runs on the newest model without that clause, and
the twins stay in the picker for anyone who wants one. Settings › Agents › Muse Code ›
Behaviour is where you choose, and the reasoning effort (`none` … `ultra`) sits beside it.

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
run with the workspace trusted — and note that Muse Code treats `AGENTS.md` and `CLAUDE.md` as
a precedence, not a sum: with both present it uses `AGENTS.md` and says it is skipping the
other.

¹⁷ muse is the one kind whose integration servers Agent Fleet does not write into a
configuration file: they are handed to Muse Code when the session starts, which means the set
can differ per session rather than being one list for your whole workspace. Two things follow.
A server you add starts being used by sessions launched **after** the change — a running
session keeps the set it was given. And each server starts when the session's first turn runs,
not when the session opens, so a brand-new session shows nothing connected until you send
something. Every server is passed as optional, so one that fails to start costs you that
integration and not the session.

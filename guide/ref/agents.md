# Agents — what each kind can do

English | [日本語](agents.ja.md)

Audience: everyone
Source of truth: this table; the columns and the marked rows are checked against the code
Updated: 2026-08

Nine session kinds exist. Seven drive a coding agent; `shell` and `ssm` are terminals
with no agent behind them, and they are in the table because "does this apply to a
plain shell session?" is a real question.

✓ = supported, — = not supported or not applicable.

| Capability | claude | codex | opencode | copilot | cursor | kiro | agy | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed execution (no terminal) | — | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Read-only history while stopped | ✓ | ✓ | ✓ | ✓ | —³ | ✓ | ✓ | — | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓ | ✓ | ✓ | — | — |
| Reasoning effort | ✓ | ✓ | ✓ | ✓ | —² | —⁵ | —² | — | — |
| Plan mode | ✓ | ✓ | ✓ | ✓ | ✓ | — | — | — | — |
| Context usage gauge | ✓ | ✓ | ✓ | — | — | ✓ | — | — | — |
| Image paste | ✓ | ✓ | ✓⁶ | — | — | — | ✓ | — | — |
| Copy the conversation into a new session | ✓ | ✓ | ✓ | ✓ | — | — | — | — | — |
| Fork from a past message | ✓ | ✓ | ✓ | ✓ | — | — | — | — | — |
| Choosing to skip permission prompts | ✓ | — | — | ✓ | ✓ | ✓ | ✓ | — | — |
| Skill / command picker | ✓ | ✓ | ✓ | —⁴ | ✓ | —⁴ | —⁴ | — | — |
| Handoff to another session | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Start in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | ✓⁷ | — | ✓ | — | — |
| Usage / remaining-quota chip | ✓ | ✓ | — | ✓ | — | — | ✓ | — | — |
| Receives your agent instructions | ✓ | ✓ | ✓ | ✓ | —⁸ | ✓ | ✓ | — | — |
| Receives integration (MCP) servers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — | — |
| Agent memory is version-managed | ✓ | ✓ | — | — | — | — | — | — | — |

¹ copilot's model list depends on the plan: Free offers only "Auto (Copilot picks)".

² cursor and agy fold the reasoning effort into the model name, so there is no
separate control.

³ cursor's managed execution keeps no local transcript, so a **stopped** cursor
session has nothing to show. The live mirror works while it runs, and running cursor
as Terminal (CLI) does persist a readable history. kiro, by contrast, keeps a readable
transcript even under Managed.

⁴ The picker lists what the CLI can discover and launch by itself, and copilot, kiro
and agy have no verified mechanism for that. Skills written to another convention's
`SKILL.md` tree in the repository are still offered to them by injection.

⁵ kiro accepts an effort flag but exposes no per-model picker.

⁶ opencode's image paste depends on the model behind the provider key.

⁷ cursor's assistant runs read-only.

⁸ Cursor keeps User Rules in your Cursor account and has no local per-user place for
instructions, so it is the one kind that cannot receive them. It still appears in the
settings list, with that reason shown.

## How to sign in

| Kind | Sign-in |
|---|---|
| claude | OAuth: approve in your browser, then paste the code back. Shows the account email and plan once connected. |
| codex | A ChatGPT subscription via device code (turn on device-code authentication in ChatGPT's security settings first), or an OpenAI API key. |
| opencode | The API key of whichever LLM provider you want, stored as an environment variable. Presets fill the variable name in; several keys can be registered at once. |
| copilot | None of its own — connecting GitHub as a git provider connects it, and disconnecting GitHub disconnects it. The account needs a Copilot subscription, including the Free plan. |
| cursor | Open the authorize link and approve in your browser. There is no code to paste. A Cursor account is required; API keys are not accepted. |
| kiro | Device flow: open the link with the confirmation code and approve (Builder ID, Google, GitHub…). API keys are not accepted. The CLI is large and is installed on demand the first time unless the deployment bakes it in. |
| agy | Sign in from its card in the agent settings. |
| shell | Not applicable. |
| ssm | Uses the workspace's AWS SSM connection. |

## States shown in the mirror

| Kind | States |
|---|---|
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

## Not in this table

- **Rovo Dev** was studied as a further agent kind and is not implemented.
- Per-kind quirks that matter only while debugging a driver belong in
  `docs/build/`.

> Agents run commands, edit files and push on your behalf — unattended in scheduled
> runs, and without asking each time in permission-skipping modes. `shell` and `ssm`
> run what you send verbatim. Keep backups, use least-privilege credentials, and lean
> on the approval gates.

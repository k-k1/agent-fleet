# Agents — what each kind can do

English | [日本語](agents.ja.md)

Audience: everyone
Source of truth: this table; the columns are checked against the kind constants in the code
Updated: 2026-08

Nine session kinds exist. Seven drive a coding agent; `shell` and `ssm` are terminals
with no agent behind them, and they are in the table because "does this apply to a
plain shell session?" is a real question.

✓ = supported, — = not supported or not applicable.

| Capability | claude | codex | opencode | copilot | cursor | kiro | agy | shell | ssm |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed execution (no terminal) | | | | | | | | | |
| Terminal (CLI) execution | | | | | | | | | |
| Live chat mirror | | | | | | | | | |
| Read-only history while stopped | | | | | | | | | |
| Model choice at launch | | | | | | | | | |
| Reasoning effort | | | | | | | | | |
| Plan mode | | | | | | | | | |
| Context usage gauge | | | | | | | | | |
| Image paste | | | | | | | | | |
| Answering questions from the mirror | | | | | | | | | |
| Permission prompts in the mirror | | | | | | | | | |
| Skill / command picker | | | | | | | | | |
| Fork from a past message | | | | | | | | | |
| Handoff to another session | | | | | | | | | |
| Start in a git worktree | | | | | | | | | |
| Scheduled (unattended) runs | | | | | | | | | |
| Chat bridge (Discord / Slack) | | | | | | | | | |
| Usable as the assistant chat | | | | | | | | | |
| Usage / remaining-quota chip | | | | | | | | | |
| Per-project instructions | | | | | | | | | |
| Project-scoped integration servers | | | | | | | | | |
| Agent memory management | | | | | | | | | |

## How to sign in

| Kind | Sign-in |
|---|---|
| claude | |
| codex | |
| opencode | |
| copilot | |
| cursor | |
| kiro | |
| agy | |
| shell | not applicable |
| ssm | |

## Choosing one

To be written (P1): a short "if you have *this* subscription, use *that* kind"
paragraph, which is the question most readers actually arrive with.

## Not in this table

- **Rovo Dev** was studied as a ninth agent kind and is not implemented; the study is
  in the frozen archive.
- Per-kind quirks that matter only while debugging a driver belong in
  [build/](../build/README.md), not here.

## Status

Axis fixed, cells to be filled in phase P1. The material is `../guide/member/06-agents.ja.md`,
which already carries two hand-maintained tables of this shape — they are correct
today but live inside the member guide, where an administrator or a developer never
finds them.

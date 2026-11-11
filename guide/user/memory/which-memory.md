---
audience: "anyone who sees three places where agents keep what they learn and wonders which to use"
source_of_truth: "the Console's ⚙ Settings > Agent instructions, Agent memory and Memory snapshots views, and workspace-agent's memory switching"
---

# Choose which memory your agents use

English | [日本語](which-memory.ja.md)

There are three things an agent "remembers".

- **Instruction files**: rules you or your team write down (`CLAUDE.md`, `AGENTS.md`, ⚙ Settings > Agent instructions)
- **The agent's own memory**: what each kind of agent writes down by itself (Claude Code's auto-memory, Codex's memories, Muse Code's memory)
- **Agent Fleet memory**: the memory every kind of agent shares (⚙ Settings > Agent memory)

## How it works

| | Instruction files | The agent's own memory | Agent Fleet memory |
|---|---|---|---|
| Who writes it | You (your team, for the repository's files) | The agent itself, while it works | Agents, with af's `memory_*` tools. You can write it in the memory view too |
| Where it lives | The repository (committed) and each agent's instruction file | Each kind's own files (Claude Code's `MEMORY.md` and the rest, Codex's `~/.codex/memories/`) | Inside the workspace, in a store Agent Fleet manages |
| Which agents read it | The kinds that read that file. Agent instructions reach most kinds | Only the kind that wrote it | With the switch on, every kind that has af's memory tools |
| Where you see or undo it in the Console | ⚙ Settings > **Agent instructions** | ⚙ Settings > **Memory snapshots** (Claude Code, Codex and Muse Code) | ⚙ Settings > **Agent memory** > **Memories** and **Changes** |
| When the switch is on | A short fixed note on the memory tools goes in beside your text | Switched off for the kinds that support it (next table) | New sessions read and write it |

The switch is **Let sessions use Agent Fleet memory** under ⚙ Settings > Agent memory > Settings (off by default). How to turn it on is in [Set how memory is used](settings.md).

## What the switch does to the agent's own memory

| Kind | Switched off while on? | Imported when switched on? | Written back when switched off? | Kept in snapshots? |
|---|---|---|---|---|
| claude | Yes (new sessions and the assistant chat) | Yes, by itself | Yes. It stays off until that has finished | Yes |
| codex | Yes (`features.memories`). Switching off puts back the earlier value | No | No | Yes (once Codex's memories folder exists) |
| muse | Yes (sessions started from then on) | Not by itself. **Import from muse** brings it in by hand | No | No |
| agy, copilot, cursor, opencode, kiro, lcpp | Agent Fleet does not switch it off | No | No | Agent Fleet keeps no history of it |

- For every kind, a flip reaches sessions started after it
- Importing and writing back are in [Import and export memory](import-export.md); history and restoring are in [Restore memory from a snapshot](snapshots.md)

## Whether to turn Agent Fleet memory on

| Turn it on when | Leaving it off is fine when |
|---|---|
| You use several kinds of agent | You use only one kind of agent |
| You hand off to another kind, or start child sessions of another kind | There is no need to share what was learned with other kinds |
| You want to read and correct what was learned in the Console | The agent's own memory is enough |
| You want to use it from your own Claude Code or claude.ai too (**Use Agent Fleet memory from outside Agent Fleet** is a separate switch) | |

## Related

- [Write down how you work, once](../agents/instructions.md): instruction files and Agent instructions
- [Set how memory is used](settings.md): the switch, and carrying Claude Code's memory across
- [Read and correct agent memory](overview.md): read and correct Agent Fleet memory
- [Restore memory from a snapshot](snapshots.md): the history of the agent's own memory
- [Import and export memory](import-export.md): bring Claude Code's or muse's memory in
- [Use memory from an external agent](external.md): use it from outside Agent Fleet
- [Agents — what each kind can do](../../ref/agents.md#each-agents-own-memory): what each kind supports

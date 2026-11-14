---
audience: "Whoever wants every agent in the deployment to follow operating rules"
source_of_truth: "`workspace/workspace-notes.md`, `workspace/notes/` and the Workspace Agent's distribution code"
---

# Give every agent instructions

English | [日本語](fleet-policy.ja.md)

Every agent running in a workspace can read the deployment operator's policy (the fleet policy) every time.
This is where prohibitions go, and the traps an agent in this environment would walk into without noticing.

## How it works

| What you do | What the agent does |
|---|---|
| Write the policy in `workspace/workspace-notes.md` and rebuild the workspace image | Reads the policy at the start of every session |
| Put long procedures in `workspace/notes/<topic>.md` and add a line to the index at the end of the policy | Reads the matching topic file when it reaches that situation |

## Where it lands

The policy is baked into the image and delivered to every container.

| Agent | Where it reads it |
|---|---|
| claude | `/etc/claude-code/CLAUDE.md` (managed policy), every session |
| codex, opencode, agy, copilot, kiro | The same body is merged into each one's instruction file at every start |
| cursor | Not delivered |

Topic files sit in `/usr/local/share/agent-fleet/notes/` in the container.
For claude, codex, opencode and muse, the Workspace Agent also registers each topic as a skill named `af-<topic>` at every start.
The CLI's skill mechanism becomes a second way to the text. For the other agents, the index is the only way.

## Write it

Split it in two layers to keep the body short.

| Where | What |
|---|---|
| The body (`workspace-notes.md`) | Only what must be known before it is needed: what not to do (delete a repository, write credentials in plaintext), this environment's constraints (no root, no Docker, shared memory), how to handle branches, and the like |
| Topic files | Every other procedure, findable from one line in the body's index |

> [!WARNING]
> **The body's length is a context cost for every session.** Every agent reads it every time, so before you add to it, check that everyone really needs it every time.
> A topic file costs nothing until it is read.

## Apply it

1. Edit `workspace/workspace-notes.md` (or a topic file)
2. Rebuild the workspace image
3. It takes effect when a member recreates their workspace, or the next time a container is created

## How it fits with members' instructions

What a member adds for themselves is not this layer. They write it in their own ⚙ Settings → "Agent instructions".
When it conflicts with the fleet policy, the fleet policy wins ([Write down how you work, once](../../user/agents/instructions.md#how-it-fits-with-other-instructions)).

## Related

- [Upgrade](upgrade.md): how an image is replaced

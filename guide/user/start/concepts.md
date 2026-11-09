---
audience: "someone about to start using Agent Fleet"
source_of_truth: "the Console's strings for the screen words, the glossary for their meaning"
---

# Terms and the big picture

English | [日本語](concepts.ja.md)

How the words in this guide and on the screen relate to each other. After reading it you know how tenants, workspaces,
sessions and agents nest inside one another.

## How it works

| What you do | What the agent does |
|---|---|
| Sign in to a tenant and start your own workspace | — |
| Put repositories in the workspace and launch sessions | Reads and writes that repository's working copy inside the session |
| Sign in to agents and outside services through connections | Uses the models and outside services with the connected accounts |

Nested, it looks like this.

```
Deployment (one installation of Agent Fleet)
└─ Tenant (a team or a department)
   └─ Your workspace (your own working environment)
      ├─ Repositories (working copies, worktrees)
      │  └─ Sessions (one piece of work handed to an agent)
      ├─ Other sessions (ones running in home or a shell)
      └─ Assistant chats (questions that need no repository)
```

## Terms

| Word on screen | Meaning |
|---|---|
| Tenant | One team or department. Members of different tenants cannot see each other |
| Workspace | Your own working environment. Repositories, uncommitted work and sessions live in it. What is inside stays when you stop it |
| Workspace action bar | The strip with workspace start and stop, Start, pane splitting and more |
| Session | One piece of work handed to an agent: its conversation, its working place and its running state |
| Agent | The coding AI that runs in a session (Claude Code, Codex, OpenCode and others) |
| Execution method | How a session runs: Managed, operated from a chat, or Terminal (CLI), where you operate the agent's own screen in a terminal |
| Working copy | The repository folder inside the workspace that is actually edited |
| Worktree | Another working copy of the same repository. One per session keeps parallel work from colliding |
| Assistant | A purpose-built chat that uses no repository. It is not a session |
| Memo queue | A place to jot down instructions and send them to a session together later |
| Connection | A credential registered in the Console (agents, Git services, issue trackers and so on) |
| Pane | One section of the main area. You can lay out several |

## Signing in and logging in

This guide keeps two words apart.

| Word | Used for |
|---|---|
| Sign in | Getting into Agent Fleet (the Console) |
| Log in | Getting into an agent or an outside service (claude, GitHub, AWS and so on) |

## Related

- [Glossary](../../ref/glossary.md): screen words and the words in the code
- [Find your way around the screen](console-tour.md)
- [Start a session](../sessions/start.md)

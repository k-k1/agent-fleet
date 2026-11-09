---
audience: "anyone who wants to carry on in the workspace with work started in their own Claude Code"
---

# Move work from your own Claude Code into the workspace

English | [日本語](move-work-in.ja.md)

A task you started in Claude Code on your own computer can carry on in a workspace session, for example to keep it running after you close the laptop.

## How it works

| What you do | What the agent does |
|---|---|
| Save the `af-move` skill once | — |
| Type `/af-move` in your own Claude Code | Your Claude shows what it would commit and waits for your OK |
| OK the commit | Your Claude pushes, writes a hand-off note and starts a session in the workspace |
| Open that session in the Console | The workspace session checks it starts at the pushed commit, summarizes the note and waits for instructions |

What moves is the pushed code and the hand-off note. The conversation itself, earlier tool output and anything you did not push stay on your computer.

## Before you start

- Your Claude Code is connected to `/mcp` with the `write` scope, by [signing in](oauth-client.md#connect)
  or with a [token](external-claude.md#issue-a-token). To sign in, register the server for your user and
  authenticate from Claude Code's MCP menu

  ```sh
  claude mcp add --transport http --scope user agent-fleet https://<your Console address>/mcp
  # then, inside Claude Code: /mcp → agent-fleet → Authenticate, and choose write on the consent page
  ```

- The repository is already in your workspace
- The workspace is running

## Save the skill

Save this skill once on your computer, at `~/.claude/skills/af-move/SKILL.md`.

````markdown
---
name: af-move
description: Move this task into an Agent Fleet session, handing over a summary
disable-model-invocation: true
argument-hint: "[anything the next session should know]"
---
Move this task to Agent Fleet. Go step by step and report each step briefly.

1. Show `git status` and the current branch. If anything is uncommitted, list what you would commit and wait
   for my OK; then make a WIP commit and push the current branch. Never commit `.env` files, keys, tokens or
   other secrets. Note `git rev-parse HEAD` after the push.
2. Split `git remote get-url origin` into its host and its `owner/name` path (without `.git`). Call agent-fleet
   `list_repos` and keep the entries whose `remote` is that host and whose `remotePath` is that path. Worktrees
   share their clone's values; prefer the entry whose `name` has no `@`. If none is left, stop and tell me; if
   more than one is, show them and let me choose. Use its `path`.
3. Write the hand-off note from the template below (at most about 4000 characters). Leave out any token,
   password or confidential value that came up in the conversation.
4. Call agent-fleet `create_session` with `dir` = the path from step 2, `worktree` = true,
   `branch` = the branch you pushed, `title` = a short name for the task, `initial_prompt` = the note.
5. Tell me the session name it returns. From then on, change no code in this session.

Also from me: $ARGUMENTS

--- hand-off note template ---
This continues a Claude Code session that ran outside Agent Fleet.

- Branch: <branch>, commit at hand-off: <sha>
- First run `git rev-parse HEAD`. If it is not <sha>, run `git fetch origin`, then
  `git merge --ff-only <sha>`, and check `git rev-parse HEAD` again. Do no work until HEAD is <sha>; if the
  fast-forward fails or HEAD still differs, stop and report it.
- Original working folder: <absolute path>. Paths in this note are relative to it; read them as relative
  to the root of your working copy.

## Goal
## Done so far
## Where it stopped (the last request word for word, open problems, failing tests)
## Next steps
## Decisions and things not to do

Read the above, summarize it in three lines, propose the next step, and wait for instructions.
````

## Move the work

1. In the Claude Code session you want to move, type `/af-move`, optionally followed by anything the next session should know
2. Claude lists what it would commit and waits for your OK. It never commits files holding secrets, such as `.env` files, keys or tokens
3. Once you OK it, it pushes, writes the hand-off note, starts the session and tells you its name
4. Open that session in the Console. It checks that it starts at the pushed commit, summarizes the note, proposes a next step and waits for you
5. Close the session on your computer. Two sessions working on one branch get in each other's way

The workspace session works in a new worktree, on a temporary branch based on the one you pushed.
When you want the work back on the original branch, ask the session to push there.

The note arrives as the session's first instruction, marked "**Via MCP**". The session treats it as a colleague's request, not your approval
([How an instruction looks to the session](external-claude.md#how-an-instruction-looks-to-the-session)). Answer permission prompts and plans in the Console as usual.

## When it goes wrong

### The session reports it does not start at the pushed commit

The session fast-forwards itself to the pushed commit before it starts. If it still reports this, the branch you pushed and the
workspace's copy have diverged. Check what you pushed, then run `/af-move` again.

### Claude stops because it cannot find the repository

The repository is not in your workspace. [Clone](../code/clone.md) it in the Console, then run `/af-move` again.

## Related

- [Connect a remote MCP client by signing in](oauth-client.md)
- [Drive your workspace from an external Claude](external-claude.md)
- [Hand off to another session or agent](../sessions/handoff.md): hand off to another session inside the workspace

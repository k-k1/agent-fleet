---
audience: "anyone using Agent Fleet with a quick question"
source_of_truth: "the Console itself and the pages linked from each answer"
---

# Read the frequently asked questions

English | [日本語](faq.ja.md)

What to try first when something goes wrong, and short answers to the questions people ask most. When the symptom is clear, look it up from the pages under [Related](#related).

## What to try first

1. **When items vanish from the left pane**, check that the bar at the very top of the left pane says **"All"**. With a working set selected, only that set is shown
2. **Reload** the page in the browser
3. **Check that the workspace is running.** While it is stopped, most features do not work
4. If that does not help, do not struggle on: **ask your administrator**. Logins, connections and permissions are theirs to look after

## Can a session you removed be brought back?

Yes. How depends on what you did.

| What you did | How to bring it back |
|---|---|
| Archived it | **"Restore"** in the archive list |
| Stopped it | It stays in the list as stopped. Open it to resume |
| Deleted it (shell and SSM too) | It is in the trash. Bring it back from the cleanup screen |

What stays and what goes is in [Clear away finished sessions](../sessions/cleanup.md#what-stays-and-what-goes).

## What is the difference between stop, delete, archive and recreate?

| Action | What it does |
|---|---|
| Stop | Only stops it. You can resume later |
| Delete | Takes it off the list and moves it to the trash |
| Archive | Hides it from the list. You can restore it later |
| Recreate | Starts a new conversation in the same place. The current one goes to the archive |

The details are in [Stop and resume a session](../sessions/stop-resume.md#choose-how-to-stop).

## Does stopping the workspace lose your work?

No. Clones, uncommitted changes, the session list and connections all stay.
Uncommitted changes exist only inside the workspace, though. Commit and push the work you want to keep ([Connect a Git service](../code/connect-git.md#push)).

## Can you use it from both a work and a home computer?

Yes. Logins, connections and display settings such as the font and text size are saved on the server, so another computer or browser gets the same.

## Can you use it on a phone?

Yes, for checking progress and sending short replies. The left pane opens from the ≡ at the top left.
Longer work is easier on a computer ([Fix the terminal, keys and phone layout](terminal-phone.md)).

## Can you use it without touching the terminal?

Yes. Giving a session instructions, replying, reading files and committing all work with buttons and input fields.

## How is the chat different from a session?

| | Suited to |
|---|---|
| The assistant chat | Questions that need no repository, translation, tidying up text |
| A session | Real work, such as having an agent rewrite files |

How to use the chat is in [Ask an assistant](../chat/assistant.md#choose-between-a-chat-and-a-session).

## Who receives your memos?

Nothing is delivered automatically. When you send them, they go to the running session you chose ([Collect memos and hand them over](../chat/memos.md)).

## Is it fine to run several jobs at once?

Yes. Give each session its own working copy (worktree) and their edits do not collide.
A worktree is created by default ([Work in parallel with worktrees](../sessions/worktrees.md)).

## opencode does not appear among the kinds of new session

On the opencode card in ⚙ Settings → Agents, both of these must hold:

- **"Use opencode"** is on. While it is off, opencode does not appear even with keys registered
- What you chose under **"opencode.ai billing"** has what it needs

| Billing choice | What it needs |
|---|---|
| **"Free models only"** | Nothing |
| **"Go (subscription)"** | An opencode.ai API key |
| **"Zen (metered)"** | An opencode.ai account login or an API key |
| **"None (my own keys)"** | Your own provider's API key, or the deployment's own engine |

The details are in [Connect OpenCode](../agents/opencode.md#choose-the-billing).

## Does putting a model in "Models to exclude" prevent charges?

No. It prevents picking the wrong model. The model disappears from the launch dialog, the settings and the assistant's list, and launching it by name is refused,
but the CLI itself, such as typing `/model` in the terminal, is not stopped ([Choose an agent](../agents/choose.md#exclude-models)).

## Does stopping a share remove it from the other person too?

No. Only access from then on stops. They can save what was shown, and anything already saved cannot be taken back.
Before you share, check that no secrets appear in the conversation. Nothing finds them for you ([Share a conversation](../team/share.md)).

## A build runs out of memory and dies or freezes

The host is shared, and memory has a limit.

- Raise the Node heap only for the command that needs it (for example `NODE_OPTIONS=--max-old-space-size=2048`)
- Keep test runners' parallelism down
- Do not leave watchers or dev servers running
- After Gradle, run `./gradlew --stop`
- Run heavy builds one at a time

You can check the memory limit and usage in [See your machine and its usage](../workspace/machine.md).

## `sudo` or `git-delta` is missing

They are left out of the workspace image on purpose. You can install the tools you need inside your own home.
Anything installed with `pip install --user` stays when the workspace stops.

## Related

- [Fix a workspace that will not start or has stopped](workspace.md)
- [Find a missing session or one that will not resume](sessions.md)
- [Fix an agent's login or a missing model](agents.md)
- [Fix a failed clone or Git authentication](git.md)
- [Fix a preview or browser pane that does not show](preview.md)
- [Fix MCP, cloud and Jira integrations](integrations.md)
- [Fix the terminal, keys and phone layout](terminal-phone.md)
- [Report a bug or request a feature](report.md)

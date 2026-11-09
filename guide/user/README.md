---
audience: "anyone running agents from the Console"
source_of_truth: "the Console itself (when the screen and this guide disagree, the screen is right)"
---

# Find what you want to do

English | [日本語](README.ja.md)

Each page covers one thing you want to do. The chapters of the current guide are being rewritten into these pages.
Anything not here yet is in the chapters of [Using Agent Fleet](../member/README.md).

## Getting started

| Page | What you can do |
|---|---|
| [What you can do with Agent Fleet](start/overview.md) | Learn what it does and which page to read first |
| [Start your first session](start/quickstart.md) | Go from signing in to the first reply |
| [Terms and the big picture](start/concepts.md) | See how tenants, workspaces, sessions and agents relate |
| [Find your way around the screen](start/console-tour.md) | Know what sits where on the top bar, the workspace action bar, the left pane and the main area |
| [Use the keyboard and the command palette](start/shortcuts.md) | Drive the Console without the mouse, with shortcuts and the command palette |
| [Split the screen into panes](start/panes.md) | Lay out sessions and files in panes, and choose the layout |
| [Use it on a phone](start/phone.md) | Check progress and reply from a phone |
| [Stop the workspace at the end of the day](start/end-of-day.md) | Stop the workspace with your work kept, and deal with Restart needed |
| [Start here if you don't use the terminal](start/without-terminal.md) | Use only chat, reading and progress checks |

## Sessions

| Page | What you can do |
|---|---|
| [Start a session](sessions/start.md) | Launch a session from Start, choosing the agent, the model and a first prompt |
| [Work in parallel with worktrees](sessions/worktrees.md) | Give each session its own working copy and work in parallel in one repository |
| [Start from an issue or a ticket](sessions/from-issues.md) | Start a session from an issue or a pull request in the issue tracker list |
| [Read a session's state](sessions/status.md) | Tell from the row marks and notifications which sessions are waiting for you |
| [Reply to a session](sessions/reply.md) | Read the conversation, answer question, plan and permission cards, and send the next instruction |
| [Hold what you send, stop a turn](sessions/queue.md) | Hold follow-ups during a turn and send them in order, and stop a turn when something is queued |
| [Comment on a plan, have it reviewed](sessions/plans.md) | Send comments on parts of a plan, and have another agent review it |
| [Call a skill or a command](sessions/skills.md) | Pick a skill or a command the session can call and put it in the input box |
| [Branch from a past message](sessions/branch-message.md) | Start a new session holding the conversation up to one of your past messages |
| [See what a session changed](sessions/changes.md) | See the files a session actually touched, as a list |
| [Save a conversation as Markdown](sessions/export.md) | Download the whole conversation as a Markdown file |
| [Read an answer in your own language](sessions/translate.md) | Read an answer in a language you do not read, translated without using a turn |
| [Stop and resume a session](sessions/stop-resume.md) | Pause a session and later carry on from where it stopped |
| [Stop when done, or keep a session awake](sessions/auto-stop.md) | Have a session stop once its work is done, or keep a long job from being stopped |
| [Clear away finished sessions](sessions/cleanup.md) | Archive sessions and delete working copies and branches you no longer need, and restore them from the trash |
| [Stop a session that spends too much with a budget](sessions/budget.md) | Stop a session when its estimated spend reaches a budget |
| [Hand off to another session or agent](sessions/handoff.md) | Continue a conversation in a new session or with another kind of agent |

## Code

| Page | What you can do |
|---|---|
| [Connect a Git service](code/connect-git.md) | Connect GitHub or Bitbucket to clone and push private repositories |
| [Clone a repository](code/clone.md) | Bring a repository into the workspace, or start from nothing |
| [Use the built-in Git](code/internal-git.md) | Keep repositories inside the tenant, with no external service |
| [Launch a session from a repository](code/launch-from-repo.md) | Launch a session from a repository row and read the row's badges |
| [Initialize Git Flow](code/git-flow.md) | Make the branches started from issues follow git-flow |
| [Commit in the commit graph](code/commit.md) | Read history and diffs, and commit without opening a terminal |
| [Use a Subversion repository](code/svn.md) | Check out a Subversion repository and work in it from a session |

## Agents

| Page | What you can do |
|---|---|
| [Choose an agent](agents/choose.md) | Pick and connect the agents that match your subscriptions, and set default and excluded models |
| [Connect Claude](agents/claude.md) | Run Claude Code in sessions, and use Remote control |
| [Connect Codex](agents/codex.md) | Run Codex on a ChatGPT subscription or an API key |
| [Connect OpenCode](agents/opencode.md) | Switch between models from many providers with OpenCode |
| [Connect GitHub Copilot](agents/copilot.md) | Run the Copilot CLI on your GitHub connection alone |
| [Connect Cursor](agents/cursor.md) | Run the Cursor CLI on your Cursor account |
| [Connect Kiro](agents/kiro.md) | Install Kiro and run it on a Builder ID or similar |
| [Connect Antigravity](agents/agy.md) | Run the Antigravity CLI on a Google account (experimental) |
| [Connect Muse Code](agents/muse.md) | Install Muse Code and run it on a Meta account or an API key |
| [Use lcpp](agents/lcpp.md) | Run sessions on the deployment's llama.cpp engine or your own llama-server |
| [Check prices and usage](agents/usage.md) | See model list prices, what is left of your allowance and what your tokens went on; save with RTK |
| [Write down how you work, once](agents/instructions.md) | Write your standing requests once so that every agent gets them |
| [Bring your Claude Code setup](agents/bring-claude-setup.md) | Use your own skills, subagents and hooks in the workspace |

## Integrations

| Page | What you can do |
|---|---|
| [Use AWS SSO](integrations/aws-sso.md) | Have agents run AWS commands with your permissions |
| [Log in to EC2 with SSM](integrations/aws-ssm.md) | Log in to an EC2 host from a terminal in your browser |
| [Use Google Cloud](integrations/gcloud.md) | Have agents run Google Cloud commands with your permissions |
| [Add an MCP server to your sessions](integrations/mcp-servers.md) | Let agents use an in-house tool or an outside service as an MCP server |
| [Connect an issue tracker](integrations/issue-tracker.md) | List your Jira issues and GitHub issues in the left rail and start sessions from them |
| [Use Jira](integrations/jira.md) | Have agents read, file and update Jira issues |
| [Use Confluence](integrations/confluence.md) | Have agents find and write Confluence pages |
| [Use the GitHub CLI](integrations/github-cli.md) | Have agents create and read GitHub pull requests with `gh` |
| [Use the Bitbucket CLI](integrations/bitbucket-cli.md) | Have agents create and read Bitbucket pull requests and check pipelines with `bkt` |
| [Connect Discord and Slack](integrations/chat-bridge.md) | Get session notifications in Discord or Slack, and answer by replying or with buttons |
| [Connect monitoring tools](integrations/ops-monitoring.md) | Have the assistant help sort out an incident while it looks at PagerDuty, Grafana, CloudWatch and AWS |

## Images

| Page | What you can do |
|---|---|
| [Use the image gallery](images/gallery.md) | Look through a folder's pictures as cards |
| [Generate images](images/generate.md) | Make pictures in bulk on the deployment's engine, without an agent |
| [Finish an image in the studio](images/studio.md) | Refine an image-generation draft by talking to an agent |
| [Have a session make images](images/from-session.md) | Have an agent make a picture during a conversation |

## Workspace

| Page | What you can do |
|---|---|
| [Recreate the workspace](workspace/recreate.md) | Rebuild a broken environment from the latest image |
| [See your machine and its usage](workspace/machine.md) | Check the machine's size and how memory and disk are used, and free disk space |
| [Choose your toolchains](workspace/toolchains.md) | Pick the Node.js, Java and Go versions and the time zone |
| [Run database-backed tests](workspace/databases.md) | Run tests against PostgreSQL and MySQL inside the workspace |
| [Try AWS code against a local emulator](workspace/aws-emulator.md) | Try AWS code without touching a real account |
| [Export and import your settings](workspace/export-import.md) | Take your settings to another environment or account |
| [See running time and cloud cost](workspace/cost.md) | See when the workspace was running and what it costs |

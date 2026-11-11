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

## Files and terminal

| Page | What you can do |
|---|---|
| [See and download files](files/browse.md) | Walk the workspace's files, open them, and download files and folders |
| [View documents and diagrams](files/view.md) | Read Markdown, slides, `.drawio` diagrams, PDFs and Office documents in the Console |
| [Edit a file](files/edit.md) | Fix an open file in place and save it. Have an AI propose a change |
| [Hand a file to a session](files/send.md) | Send a selection, with instructions, to a session or an assistant |
| [Use the terminal](files/terminal.md) | Copy and paste in the browser terminal, read its lag, change its font |

## Preview

| Page | What you can do |
|---|---|
| [See a web app you started](preview/browser-pane.md) | Open a web app started in the workspace in the browser pane or the lightweight preview |
| [Use preview subdomains](preview/subdomains.md) | Open the app at its own root URL and show it to colleagues or outsiders |
| [Try Google sign-in on a preview](preview/dev-signin.md) | Try Google sign-in on a preview URL or from an app on your own machine |
| [Operate a browser the agent opened](preview/agent-browser.md) | Take over a step only a person can do, such as a login, in the agent's browser |

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

## Fleet

| Page | What you can do |
|---|---|
| [See every running session](fleet/overview.md) | Lay out the running sessions as cards and spot the ones waiting for you |
| [See how sessions relate over time](fleet/graph.md) | See on a time axis which session started which, and what passed between them |
| [Narrow the view with working sets](fleet/working-sets.md) | Group repositories and conversations per piece of work and switch what the left pane shows |
| [Start child sessions from a session](fleet/child-sessions.md) | Have a session start other sessions and hand them parts of the work |
| [Let sessions message each other](fleet/messages.md) | Have sessions running side by side send each other short messages |
| [Tell a session about its pull request's review](fleet/pr-feedback.md) | Tell the session that opened a PR about failed CI and review comments |
| [Let a session see the fleet](fleet/fleet-visibility.md) | Let a session see the other sessions' state and leave you notes |
| [Hand work to the Fleet Operator](fleet/operator.md) | Have it start sessions, instruct them and take their reports, from chat |
| [Ways to use the Fleet Operator](fleet/operator-patterns.md) | Patterns that work, such as parallel implementation and cross-review |
| [Run at a set time](fleet/schedules.md) | Have an agent work at set times or intervals |

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

## Team

| Page | What you can do |
|---|---|
| [Hand a session to a member](team/hand-over.md) | Have a member you share a session with carry it on |
| [Ask a teammate through the inbox](team/inbox.md) | Send members a message or a request; whoever takes it starts a session |
| [Share a conversation](team/share.md) | Show a conversation read-only, and take suggestions on it if you want |
| [Show each other where you stand on usage limits](team/usage-limits.md) | Show members you choose how close you are to your usage limits |

## Chat

| Page | What you can do |
|---|---|
| [Ask an assistant](chat/assistant.md) | Ask questions without a repository; compact a long conversation and keep a work plan |
| [Use translation and purpose-built assistants](chat/purpose-assistants.md) | Translate or summarize a file; make your own assistant and set up AI assistance |
| [Collect memos and hand them over](chat/memos.md) | Collect instructions as they come, tidy them, and send them to a session together |

## Memory

| Page | What you can do |
|---|---|
| [Read and correct agent memory](memory/overview.md) | Read, rewrite, pin and forget what agents remember |
| [Check memory changes](memory/changes.md) | See who changed what and when, and revert a bad change |
| [Set how memory is used](memory/settings.md) | Let sessions use memory, and carry Claude Code's memory across |
| [Find stale memories](memory/lint.md) | Check for memories that use missing paths or retired terms, and mark them |
| [Use memory tidy-up](memory/tidy-up.md) | Have a model draft fixes to memory, and approve them one by one |
| [Import and export memory](memory/import-export.md) | Bring Claude Code's or muse's memory in and write it back to Claude Code; move the memories of a project that no longer exists |
| [Restore memory from a snapshot](memory/snapshots.md) | See the history of Claude Code's and Codex's own memory, restore a point in time, take it to another environment |
| [Use memory from an external agent](memory/external.md) | Use Agent Fleet memory from your own Claude Code or claude.ai; bring your own memory in |

## Outside access

| Page | What you can do |
|---|---|
| [Drive your workspace from an external Claude](outside/external-claude.md) | Drive sessions from your own Claude Code or Claude Desktop with a token |
| [Connect a remote MCP client by signing in](outside/oauth-client.md) | Connect claude.ai, ChatGPT, Claude Code or Codex without a token |
| [Move work from your own Claude Code into the workspace](outside/move-work-in.md) | Push work you started on your computer and carry on in a workspace session |

## Settings

| Page | What you can do |
|---|---|
| [Change the display and your account](settings/display.md) | Change language, time zone, theme and fonts; add a sign-in method |
| [Get notified](settings/notifications.md) | Choose the unread dot, the OS notification and read-aloud for each kind of notification |
| [Use read-aloud](settings/read-aloud.md) | Have answers read out loud; choose the voice and how it reads |

## Security

| Page | What you can do |
|---|---|
| [Handle credentials safely](security/credentials.md) | Know where tokens and keys belong, and where never to write them |

## Troubleshooting

| Page | What you can do |
|---|---|
| [Fix a workspace that will not start or has stopped](troubleshooting/workspace.md) | Find the fix from the badge beside the power button, or a start or disk message |
| [Find a missing session or one that will not resume](troubleshooting/sessions.md) | Fix sessions that vanished, will not resume or stopped partway, and sharing or scheduled runs that misbehave |
| [Fix an agent's login or a missing model](troubleshooting/agents.md) | Fix an expired login, a model you cannot pick, or an assistant that stopped |
| [Fix a failed clone or Git authentication](troubleshooting/git.md) | Fix failed clones, private-repository authentication and submodule fetches |
| [Fix a preview or browser pane that does not show](troubleshooting/preview.md) | Find the fix from the error the browser pane or a preview URL shows |
| [Fix MCP, cloud and Jira integrations](troubleshooting/integrations.md) | Fix MCP servers, AWS and Google Cloud commands, `jira`, and apps connected from outside |
| [Fix the terminal, keys and phone layout](troubleshooting/terminal-phone.md) | Fix keys, copy and paste, the phone layout, and menus that do not appear |
| [Read the frequently asked questions](troubleshooting/faq.md) | Read what to try first, and answers to common questions |
| [Report a bug or request a feature](troubleshooting/report.md) | Tell the project about a bug in Agent Fleet itself, or a feature you want, with an issue |

---
audience: "anyone connecting a session to services outside the workspace"
updated: "2026-10"
---

# 15. External services and signed-in CLIs — gh / bkt / jira, Discord / Slack, ops tooling

English | [日本語](15-external-services.ja.md)

This chapter covers the outside services you use from a session: the `gh`, `bkt` and `jira` CLIs, the other CLIs that are already signed in, the Discord / Slack chat bridge, and wiring monitoring tools into a conversation. AWS and Google Cloud are in [16 Cloud accounts](16-cloud-accounts.md).

## Use `gh` and `bkt` in a session

Once GitHub is connected, `gh` in a session is already signed in. Once Bitbucket Cloud
(bitbucket.org) is connected, so is `bkt`, the Bitbucket CLI: `bkt pr list`, `bkt pr create`,
`bkt pipeline` and so on need no `auth login`. Both use your saved connection and renew it
the way git does.

- **Scopes**: `bkt` can do only what the connection was granted. For pull requests and
  pipelines the Bitbucket OAuth app needs `repository`, `pullrequest:write`, `pipeline` (read)
  and `account`, plus `pipeline:write` to start or stop a pipeline; a pasted API token needs the
  matching `read:` / `write:` scopes (for example `read:pullrequest:bitbucket`, and
  `write:pipeline:bitbucket` to start or stop a pipeline — write does not imply read). A token without the pull-request scopes can list repositories
  but gets `403 Forbidden` from `bkt pr list`. The OAuth app's scopes are set by the tenant administrator.
- **Bitbucket Data Center** (or any host other than bitbucket.org) is not connected through
  Settings. Set it up yourself (`bkt auth login`, or `BKT_HOST` and `BKT_TOKEN`); the
  `bkt` then runs as you configured it, without the saved connection.

## Use `jira` in a session

Once Jira is connected (Settings, **Issue tracker**), `jira`, the Jira CLI, works in every session
and for every agent, with no `jira init` token prompt and no login. It uses your saved connection,
and an OAuth connection is renewed on each call, so an expired token never means restarting the
session. You can view, list, create, edit, comment on and transition issues:
`jira issue view PROJ-1`, `jira issue list -p PROJ`, `jira issue create -p PROJ`,
`jira issue comment add PROJ-1 "…"`, `jira issue move PROJ-1 "In Progress"`.

- **Creating issues**: `jira issue create -p PROJ …` works without `jira init`; name the project with
  `-p` (or set a default project, below). If `jira` cannot work out the issue types it needs, it prints a
  one-line hint and fails with `invalid issue types in config`. Run the `jira init` command the hint
  names, as printed; `jira board list -p PROJ` shows the exact board name it wants. Creating an issue
  with `--parent` in a project other than your default one always takes this route.
- **Assigning**: looking up an assignee (`jira issue assign`, and `-a` / `--assignee` of
  `issue edit`, `create` and `move`) searches users in the default or `-p` project, so it needs a
  project. With no default project (no `jira init` yet), `jira issue assign PROJ-1 <user>`
  takes the project from the issue key's prefix for you. The other commands print a one-line hint:
  pass `-p PROJ` or run `jira init --project PROJ --board "PROJ board"`. An explicit `-p` is never
  overridden.
- **Project, board, sprints and epics**: run `jira init --project PROJ --board "PROJ board"` once
  (add `--force` to replace an earlier setup). It writes your default project, board, issue types
  and epic fields, which `jira sprint list` and the epic commands need, and it asks no questions.
  Without it, pass `-p PROJ` to each command. For a second board (or a project the first
  setup does not cover), make a configuration file of its own in the folder where `jira` keeps its
  files: `jira init -c ~/.config/agent-fleet/jira/other-board.yml --project PROJ --board "Other board"`,
  then use `jira -c ~/.config/agent-fleet/jira/other-board.yml …`. Your connection is used only with
  configuration files in that folder; one anywhere else runs without it. (When `jira` itself suggests such a
  command, for example for another project's issue types, run it as printed.)
- **Markdown in comments and descriptions**: the CLI converts Markdown to Jira markup, and until the
  first piece of markup (a heading, a list, bold, a link) a list, heading,
  code block or quote that follows a plain paragraph is glued onto it (two paragraphs in a row are
  fine). A paragraph, a blank line and a list are sent as:

  ```
  Intro.          →  Intro.* a
  (blank line)
  - a
  ```

  Open the body with a heading or a list, or put inline markup (bold, a link, `code`) in the first
  paragraph, and always leave a blank line before a list. Agents are told this too.
- **Flaky network to Atlassian**: reading commands (`issue view` / `issue list`, `board`, `sprint`,
  `epic` and `project list`, `me`) are re-run up to twice after a connection reset or timeout, and
  only the last attempt's output is shown. Commands that change something are never re-run, so after
  a failed **or timed-out** `issue create` or `comment add` check Jira before running it again (for
  example with the JQL `project = PROJ AND creator = currentUser() AND created >= -15m`). `comment edit` and
  `comment delete` retry on their own; "probably already deleted" means the delete went through.
  When no terminal is attached, a call that gets no answer is stopped rather than left waiting: after
  about two minutes for a command that changes something, after about 45 seconds per attempt for a
  reading one (which is then re-run as above). The command that changes something exits with status
  124 and says that the issue or comment may exist; for `issue create` it also lists the issues you
  created in the last 15 minutes with exactly that summary ("possible matches", or "no matching issue
  found"), still with status 124. If the authorization expired or was revoked, `jira` prints "the Jira
  connection is unusable" instead of asking for an API token: reconnect Jira in Settings. In output
  that is not a terminal, links to issues point at your Jira site.
- **Using an Atlassian MCP tool instead**: `jira` is the default because it works for every agent and
  renews the token itself, while an MCP connector serves one agent and needs a session restart when
  its login expires. To prefer the MCP route, say so in **⚙Settings → "Agent instructions"**, for
  example: "For Jira, use the Atlassian MCP tool instead of the `jira` CLI." Agents that have the
  connector follow it; the others keep using `jira`. Without such a line, an agent uses an MCP tool
  only for what the CLI cannot do.
- **Editing and deleting comments**: `jira issue comment edit PROJ-1 10001 -b "…"` and
  `jira issue comment delete PROJ-1 10001`. `jira` itself has only `comment add`; these two are
  added by Agent Fleet. `edit` takes the new text from `-b`, `-T FILE` (`-` reads stdin) or a pipe,
  and sends it as Markdown converted for Jira, so it is also how a garbled comment (see above) is
  repaired. Find the comment ID with `jira issue view PROJ-1 --raw` (`fields.comment.comments[].id`).
  Jira usually lets you change only your own comments (someone else's answers 403 unless your account may manage all comments), and agents
  are told to edit or delete only a comment they posted in that session or one you named. They
  work with your saved connection only: not with `--debug`, `JIRA_API_TOKEN` or a configuration of your
  own, where plain `jira` answers that it has no such command.
- **`--debug` runs without your connection**: jira's debug output prints the authorization
  header, so a debug run gets no token from the connection.
- **What it can change** is what your connection can. The CLI writes to Jira as you, so creating,
  editing and moving issues needs an account that is allowed to.
- **With OAuth, boards, sprints and epics** need the Jira Software scopes on the tenant's Jira app.
  Your tenant administrator turns them on, and you then **reconnect Jira** once. Until then those
  commands, and `jira init`, fail with an authorization error; Settings › Issue tracker shows
  "Boards, sprints and epics: not available" on the Jira card, and `jira` prints the same
  explanation before running those commands. Links the CLI prints point at
  `api.atlassian.com` rather than your site.
- **Jira Data Center** (or another Jira) is not connected through Settings. Set it up yourself
  (`jira init` with your own `--server`, a configuration file of your own, or
  `JIRA_API_TOKEN`); `jira` then runs as you configured it, without the saved connection.

## CLIs that are already signed in

These commands in a session or terminal use a connection you made in Settings, for every agent. You never log
them in from the terminal, and an expiring token is renewed on use.

| Command | Signed in by | Details |
|---|---|---|
| `git` | **Git hosting** (GitHub, Bitbucket) | [06](06-code.md#connect-a-git-provider) |
| `gh` | **Git hosting**, GitHub | [15](15-external-services.md#use-gh-and-bkt-in-a-session) |
| `bkt` | **Git hosting**, Bitbucket Cloud (bitbucket.org) | [15](15-external-services.md#use-gh-and-bkt-in-a-session) |
| `jira` | **Issue tracker**, Jira | [15](15-external-services.md#use-jira-in-a-session) |
| `af-aws-exec --profile <name> -- <command>` | **AWS profiles/SSM**, after **Log in** on the profile | [16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools) |
| `af-gcloud-exec --profile <name> -- <command>` | **Google Cloud**, after **Log in** on the profile | [16](16-cloud-accounts.md#running-a-command-as-a-profile) |

Agents are told to use `bkt` rather than a Bitbucket or Atlassian MCP server, and `jira` by default for Jira. The
CLIs read your saved connection on every call and renew it when it is about to expire, while an MCP connector's
expired login needs the session restarted. To have agents prefer an Atlassian MCP tool for Jira, see
[15](15-external-services.md#use-jira-in-a-session) ("Using an Atlassian MCP tool instead").

## Connecting Discord / Slack (chat bridge)

Connect your own Discord / Slack bot from ⚙ Settings → the **"Chat"** tab in the Connections group, and session
progress reaches your chat even while you're away from your desk, and you can steer sessions right from your
replies.

- **Connecting**: Discord takes **a single Bot token** (the card's wizard walks you through validation →
  inviting it to your server → picking a channel, and a test notification arrives on connect). Slack takes two:
  a Bot token (`xoxb-…`) and, if you want two-way operation, an App-level token (`xapp-…`).
  You can also connect both at the same time.
- **What arrives**: a thread is created per session, and you receive "Answer ready", "Questions & plan
  approvals", "Permission requests", "Abnormal exits", and "Session reports" (each type has its own toggle).
  With the opt-in **full-text mode**, the response body itself is delivered (secrets such as tokens are
  automatically redacted).
- **Driving from chat**: turn on **"Reply to steer"** (opt-in) and replies in the thread become input to that
  session as-is. Questions can be answered with choice buttons, plan approvals with "Approve / Reject" buttons,
  and permission requests with "Allow / Deny" buttons (button coverage varies by agent kind).
- **Fleet operator**: write in the standing thread "🛰 Fleet Operator" to talk with the
  [11 Running many sessions](11-many-sessions.md) from chat (the same conversation as the operator on the Console
  side). Destructive operations initiated from chat (deletion etc.) pause for an "Approve / Reject" button
  before executing.
- **Just want to silence notifications**: under Personal → the "Notifications" tab, **Service notifications**
  lets you turn off delivery without disconnecting.

## PagerDuty / Grafana / CloudWatch / AWS connect from the "Ops & monitoring" tab (recommended)

**PagerDuty, Grafana, CloudWatch and AWS are already built into the product**. Use those cards.

1. Open the **Settings > Ops & monitoring** tab, enter the connection details on each card, and hit "Connect":
   - **PagerDuty**: an API key. A **read-only key** is recommended (choose "Read-only" under Integrations > API Access Keys in PagerDuty). For EU accounts, turn the toggle on.
   - **Grafana**: the instance URL and a **service account token** (Viewer permission recommended). Self-hosted / Grafana Cloud / **Amazon Managed Grafana** all work (for AMG, use the workspace endpoint as the URL; for how to issue a token and the 30-day expiry, see "Amazon Managed Grafana" below).
   - **CloudWatch**: just **pick an SSM connection's profile** from the dropdown (the region can optionally be overridden). **No secrets are entered**; a dedicated config file is generated from the profile's SSO settings (non-secret), and the AWS credentials inside the container are read as-is. If SSO login hasn't been done yet / has expired, the tools will error out, so press **Log in** on that profile under Settings › [AWS profiles/SSM](16-cloud-accounts.md#logging-in-to-another-in-house-host-ssm), or open the matching SSM session once. If you manage `~/.aws` yourself, you can specify the profile name directly via "Manual entry".
   - **AWS** (Agent Toolkit for AWS): connects to the MCP server AWS operates. The profile works exactly like CloudWatch (pick an SSM connection's profile; no secrets entered). Two extra settings:
     - **MCP endpoint**: the region the MCP server itself runs in (`us-east-1` / `eu-central-1`). This is **not** where your resources live. That goes in the "Region" field above.
     - **Write tools**: off by default (read-only). Turning it on enables AWS API calls (`call_aws`) and script execution (`run_script`), which **can create, change and delete real AWS resources**. Turn it on only when you need it.
     - AWS is the only one that also works **from interactive sessions** (the other three are chat-only), so AWS documentation search, skill retrieval and AWS API lookups are available right where you write code.
     - If SSO login hasn't been done yet / has expired, press **Log in** on that profile under Settings › [AWS profiles/SSM](16-cloud-accounts.md#logging-in-to-another-in-house-host-ssm), or open the matching SSM session once.
2. Credentials are stored encrypted inside the workspace and are handed over only when the MCP server starts (they don't end up in config files or plaintext). Grafana starts with write and admin tools disabled; the CloudWatch server itself has read-only tools only; AWS starts with `--read-only` by default.
3. In chat, pick the **"SRE Assistant"** and start a new conversation. Ask things like "List the PagerDuty incidents currently open and summarize what happened", "Check this service's error rate for the last hour in Grafana", or "Analyze the ERROR entries in this log group with CloudWatch". It will help you organize the situation, form hypotheses about the cause, and draft external reports while checking the real data (read-only; it does not ack/resolve).
4. Connection changes take effect **from the next chat message** (and, for AWS in a session, **from the next session launch**). No workspace restart needed.

### Amazon Managed Grafana: issuing the token

The card needs the same kind of service account token as self-hosted Grafana (no IAM or SigV4 setup).
Issue it from AMG's own Grafana admin UI (Administration → Service accounts; administrator required), or,
if you have the IAM permissions, with the AWS CLI's `aws grafana create-workspace-service-account-token`
(run it as a profile with `af-aws-exec`, see [16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools)).
AMG tokens last **30 days at most**; paste a new one into the card when the old one expires.

### Other tools (Zabbix and the like)

Tools without a card (Zabbix, a Terminal (CLI) session's own Grafana / PagerDuty / CloudWatch server, and the like) are
connected as MCP servers under Settings > Connections > [MCP servers](20-settings.md#mcp-servers). Under
**Advanced**, choose the **targets**: **sessions** for Terminal (CLI) and other agent sessions, which pick the
server up from the next session you start, and **assistants** for chat. For an assistant, then pick it in the
assistant's edit form under "MCP servers (optional)"; only assistants you created can be edited, and the built-in
ones (SRE Assistant, Fleet Operator, Agent Fleet Assistant) cannot. Choose the server you want from the tool's own
documentation (for Zabbix, the server of your Zabbix vendor; for AWS Athena, an Athena or data-processing MCP
server, or simply the `aws` CLI). A server added by hand with `claude mcp add` never appears in the assistant
choice, and its token sits in plaintext in the home folder, so prefer the registration, which is stored encrypted.

Alert bodies and logs are **input an attacker can influence**: keep these connections read-only, and if you
experiment with writes, do it explicitly in a dedicated assistant or session.

To read CloudWatch Logs from a Terminal (CLI) session with no connection at all, ask the agent to use the
`aws` CLI as a profile through `af-aws-exec` (`aws logs tail`, `aws logs filter-log-events`, Logs Insights);
the profile needs only read access to the logs ([16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools)).

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
  wrapper then leaves `bkt` alone.

## Use `jira` in a session

Once Jira is connected (Settings, **Issue tracker**), `jira`, the Jira CLI, works in every session
and for every agent, with no `jira init` token prompt and no login. It uses your saved connection,
and an OAuth connection is renewed on each call, so an expired token never means restarting the
session. You can view, list, create, edit, comment on and transition issues:
`jira issue view PROJ-1`, `jira issue list -p PROJ`, `jira issue create -p PROJ`,
`jira issue comment add PROJ-1 "…"`, `jira issue move PROJ-1 "In Progress"`.

- **Creating issues**: `jira issue create -p PROJ …` works without `jira init`. When your config
  holds no issue types, the wrapper reads the project's types from Jira (this needs only the default
  scopes) and gives `jira` a generated copy of the config that includes them. It only does this
  for the generated config and for configs in `~/.config/agent-fleet/jira/`, and it needs `-p`
  (or a default project in that config). If it cannot, it prints a one-line hint and `jira` then
  fails with `invalid issue types in config`; run `jira init` below (`jira board list -p PROJ`
  shows the exact board name it wants). A config written by `jira init` holds the types of its
  own project only (IDs are per project in team-managed projects), so for `-p` naming another
  project the wrapper hands `jira` a copy with that project's types and without the config's
  custom fields (they belong to the first project). If the types cannot be read, `jira` runs on
  the config as it is and a hint names `jira init -c ~/.config/agent-fleet/jira/<name>.yml
  --project PROJ --board "PROJ board"`, which gives that project a config of its own. Creating with `--parent` for another project always
  takes that route, because the config cannot say what kind of project it is.
- **Assigning**: looking up an assignee (`jira issue assign`, and `-a` / `--assignee` of
  `issue edit`, `create` and `move`) searches users in the configured or `-p` project, so it
  needs a project. With no default project (no `jira init` yet), `jira issue assign PROJ-1 <user>`
  takes the project from the issue key's prefix for you. The other commands print a one-line hint:
  pass `-p PROJ` or run `jira init --project PROJ --board "PROJ board"`. An explicit `-p` and a
  config with a project are never overridden.
- **Project, board, sprints and epics**: run `jira init --project PROJ --board "PROJ board"` once
  (add `--force` to replace an earlier setup). It writes your default project, board, issue types
  and epic fields, which `jira sprint list` and the epic commands need, and it asks no questions.
  Without it, pass `-p PROJ` to each command. For a second board, run
  `jira init -c ~/.config/agent-fleet/jira/other-board.yml --project PROJ --board "Other board"`
  and then use `jira -c ~/.config/agent-fleet/jira/other-board.yml …`. Your connection is used
  only with configs in `~/.config/agent-fleet/jira/`.
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
  `jira` can otherwise wait forever for a server that never answers, so a run with no terminal is
  stopped after `AF_JIRA_TIMEOUT` seconds (default 120 for a command that changes something, 45 per
  attempt for a reading one; `0` turns it off). A reading command is then re-run like after a reset.
  A command that changes something exits with status 124 and says that the issue or comment may
  exist; for `issue create` it also lists the issues you created in the last 15 minutes with exactly
  that summary ("possible matches", or "no matching issue found"), still with status 124. `jira` also reads standard input to the end even when `-b` is given; with the body on the
  command line the wrapper gives it an empty input instead. If a run still hangs, the file
  `/proc/<pid>/wchan` of the `jira` process tells a wait on input (`pipe_read`,
  `unix_stream_data_wait`) from a wait on the network. If
  the authorization expired or was revoked, `jira` prints "the Jira connection is unusable"
  instead of asking for an API token: reconnect Jira in Settings. In output that is not a terminal,
  links to issues point at your Jira site.
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
  work with your saved connection only: not with `--debug`, `JIRA_API_TOKEN` or a config of your
  own, where plain `jira` answers that it has no such command.
- **`--debug` runs without your connection**: jira's debug output prints the authorization
  header, so the wrapper does not hand the token to a debug run.
- **What it can change** is what your connection can. The CLI writes to Jira as you, so creating,
  editing and moving issues needs an account that is allowed to.
- **With OAuth, boards, sprints and epics** need the Jira Software scopes on the tenant's Jira app.
  Your tenant administrator turns them on, and you then **reconnect Jira** once. Until then those
  commands, and `jira init`, fail with an authorization error; Settings › Issue tracker shows
  "Boards, sprints and epics: not available" on the Jira card, and `jira` prints the same
  explanation before running those commands. Links the CLI prints point at
  `api.atlassian.com` rather than your site.
- **Jira Data Center** (or another Jira) is not connected through Settings. Set it up yourself
  (`jira init` with your own `--server`, a config outside `~/.config/agent-fleet/jira/`, or
  `JIRA_API_TOKEN`); the wrapper then leaves `jira` alone.

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

**PagerDuty, Grafana, CloudWatch and AWS are already built into the product**. Use that first, rather than the manual PoC steps described later.

1. Open the **Settings > Ops & monitoring** tab, enter the connection details on each card, and hit "Connect":
   - **PagerDuty**: an API key. A **read-only key** is recommended (choose "Read-only" under Integrations > API Access Keys in PagerDuty). For EU accounts, turn the toggle on.
   - **Grafana**: the instance URL and a **service account token** (Viewer permission recommended). Self-hosted / Grafana Cloud / **Amazon Managed Grafana** all work (for AMG, use the workspace endpoint as the URL; for how to issue a token and the 30-day expiry, see the AMG section below).
   - **CloudWatch**: just **pick an SSM connection's profile** from the dropdown (the region can optionally be overridden). **No secrets are entered**; a dedicated config file is generated from the profile's SSO settings (non-secret), and the AWS credentials inside the container are read as-is. If SSO login hasn't been done yet / has expired, the tools will error out, so press **Log in** on that profile under Settings › [AWS profiles/SSM](16-cloud-accounts.md#logging-in-to-another-in-house-host-ssm), or open the matching SSM session once. If you manage `~/.aws` yourself, you can specify the profile name directly via "Manual entry".
   - **AWS** (Agent Toolkit for AWS): connects to the MCP server AWS operates. The profile works exactly like CloudWatch (pick an SSM connection's profile; no secrets entered). Two extra settings:
     - **MCP endpoint**: the region the MCP server itself runs in (`us-east-1` / `eu-central-1`). This is **not** where your resources live. That goes in the "Region" field above.
     - **Write tools**: off by default (read-only). Turning it on enables AWS API calls (`call_aws`) and script execution (`run_script`), which **can create, change and delete real AWS resources**. Turn it on only when you need it.
     - AWS is the only one that also works **from interactive sessions** (the other three are chat-only), so AWS documentation search, skill retrieval and AWS API lookups are available right where you write code.
     - If SSO login hasn't been done yet / has expired, press **Log in** on that profile under Settings › [AWS profiles/SSM](16-cloud-accounts.md#logging-in-to-another-in-house-host-ssm), or open the matching SSM session once.
2. Credentials are stored encrypted inside the workspace and are handed over only when the MCP server starts (they don't end up in config files or plaintext). Grafana starts with write and admin tools disabled; the CloudWatch server itself has read-only tools only; AWS starts with `--read-only` by default.
3. In chat, pick the **"SRE Assistant"** and start a new conversation. Ask things like "List the PagerDuty incidents currently open and summarize what happened", "Check this service's error rate for the last hour in Grafana", or "Analyze the ERROR entries in this log group with CloudWatch". It will help you organize the situation, form hypotheses about the cause, and draft external reports while checking the real data (read-only; it does not ack/resolve).
4. Connection changes take effect **from the next chat message** (and, for AWS in a session, **from the next session launch**). No workspace restart needed.

For other tools such as Zabbix, register their MCP server under Settings > Connections > [MCP servers](20-settings.md#mcp-servers); the manual PoC steps below remain for experimenting from a Terminal (CLI) claude session.

---

## (PoC) Connecting other tools manually 🧪

**These are experimental steps.** Manual steps for tools not in the "Ops & monitoring" tab (Zabbix, etc.), or for when you want to connect a **Terminal (CLI) claude session** rather than chat.

- Scope: Terminal (CLI) claude sessions. **To give chat (an assistant) a tool, register the server under Settings > Connections > [MCP servers](20-settings.md#mcp-servers)** with assistants as a target, then pick it in the assistant's edit form under "MCP servers (optional)". Only assistants you created can be edited; the built-in ones (SRE Assistant, Fleet Operator, Agent Fleet Assistant) cannot. A server added by hand with `claude mcp add` is not a way to attach one: it never appears in that choice, and a claude-based assistant that has MCP servers or Agent Fleet tools attached runs with a strict MCP configuration that leaves it out.
- Prerequisite: outbound connectivity from the workspace to each monitoring tool's endpoint. PyPI access is needed for `uvx`'s first fetch.
- ⚠️ **Token handling (a PoC-only compromise)**: tokens passed via `claude mcp add -e` are **stored in plaintext** in `~/.claude.json`. Because it's inside the home volume it survives a container recreate, but never write tokens into a repository, and use **read-only, dedicated tokens only**. To keep the stored token encrypted, register the server under [MCP servers](20-settings.md#mcp-servers) instead: the registration is stored encrypted, though the configuration handed to the CLI can still contain the token.

## 0. Prep (one time only; survives a recreate)

```bash
mkdir -p ~/.local/bin
# uv/uvx (launcher for Python MCP servers; goes into ~/.local so it persists)
pip install --user uv
```

## 1. Grafana (metrics, logs, alerts, OnCall)

A single Go binary, the lightest and most complete. **Verified**: v0.17.1 with `-disable-write -disable-admin` becomes read-only with 52 tools (Prometheus/Loki queries, dashboard search, alerts, Incident/OnCall lookups, Sift analysis; zero create/update/delete/install tools). Tools that query CloudWatch / Athena / Elasticsearch etc. through Grafana data sources are also included.

```bash
# Fetch the binary (put it in ~/.local/bin so it persists)
curl -sL https://github.com/grafana/mcp-grafana/releases/download/v0.17.1/mcp-grafana_Linux_x86_64.tar.gz \
  | tar xz -C ~/.local/bin mcp-grafana

# On the Grafana side: create a Viewer-permission service account in the admin UI and issue a token

# Register with claude (user scope = active in every session in this workspace)
claude mcp add -s user grafana \
  -e GRAFANA_URL=https://grafana.example.com \
  -e GRAFANA_SERVICE_ACCOUNT_TOKEN=<viewer-sa-token> \
  -- ~/.local/bin/mcp-grafana -disable-write -disable-admin
```

**Amazon Managed Grafana (AMG)** connects with the same steps (authentication is the same service account token as self-hosted; no IAM/SigV4 needed). There are only two differences:

- `GRAFANA_URL` is the workspace endpoint (`https://g-xxxxxxxxxx.grafana-workspace.<region>.amazonaws.com`).
- The token is issued from AMG's Grafana admin UI (Administration → Service accounts; admin required), or, if you have the IAM permissions, via the AWS CLI (it expires after **30 days at most**, so watch the expiry):

```bash
aws grafana create-workspace-service-account-token \
  --workspace-id g-xxxxxxxxxx --service-account-id <sa-id> \
  --name poc-$(date +%Y%m%d) --seconds-to-live 604800   # 7 days. The key in the response is the token (cannot be shown again)
```

## 2. PagerDuty (incidents, on-call)

The official self-hosted version (Python / PyPI `pagerduty-mcp`). **Read-only by default**; write tools don't appear unless you pass `--enable-write-tools`. The token is a PagerDuty User API Token (My Profile → User Settings).

```bash
claude mcp add -s user pagerduty \
  -e PAGERDUTY_USER_API_KEY=<user-api-token> \
  -- ~/.local/bin/uvx pagerduty-mcp
# For EU accounts, add -e PAGERDUTY_API_HOST=https://api.eu.pagerduty.com
```

> There is also a hosted version (`https://mcp.pagerduty.com/mcp`, remote HTTP), but it **exposes write tools by default**, so for the PoC we recommend self-host with the read-only default.

## 3. CloudWatch (alarm-driven investigation, log analysis)

### 3a. Start without MCP: read logs directly with the aws CLI (fastest)

Terminal (CLI) claude sessions can use Bash, so **you can brainstorm over logs with the baked-in aws CLI without wiring up MCP at all**. If you're already logged in with the profile you use for SSM connections (Settings › AWS profiles/SSM, **Log in**), there is zero extra setup. Name the profile on every call through `af-aws-exec` rather than exporting `AWS_PROFILE` ([16](16-cloud-accounts.md#using-the-profiles-from-the-terminal-sdks-and-build-tools) explains why). Ask claude to "look at the last hour of ERROR in `<log-group>`" and it will run commands like the following on its own:

```bash
awsx() { af-aws-exec --profile <name> --region ap-northeast-1 -- aws "$@"; }

awsx logs describe-log-groups --log-group-name-prefix /aws/   # find log groups
awsx logs tail /aws/ecs/my-service --since 1h                 # recent logs (--follow to keep tailing)
awsx logs filter-log-events --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '1 hour ago' +%s)000 --filter-pattern ERROR

# For aggregation or cross-group queries, use Logs Insights (two steps: start-query → get-query-results)
qid=$(awsx logs start-query --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '3 hours ago' +%s) --end-time $(date +%s) \
  --query-string 'filter @message like /ERROR/ | stats count(*) by bin(5m)' \
  --query queryId --output text)
awsx logs get-query-results --query-id $qid
```

The IAM required is read-only (equivalent to `CloudWatchLogsReadOnlyAccess`: DescribeLogGroups / FilterLogEvents / GetLogEvents / StartQuery / GetQueryResults).

### 3b. Connect via MCP (when you want alarm analysis and anomaly detection tools)

Official AWS (awslabs). Credentials are read from the same chain as the baked-in aws CLI, so **no extra secret is needed if you have an SSO profile**. All tools are read-only (metric retrieval, alarm history, log-group anomaly pattern analysis, Logs Insights queries, and more).

```bash
# Log in to the profile first (Settings › AWS profiles/SSM, Log in)
claude mcp add -s user cloudwatch \
  -e AWS_PROFILE=<sso-profile> \
  -e FASTMCP_LOG_LEVEL=ERROR \
  -- ~/.local/bin/uvx awslabs.cloudwatch-mcp-server@latest
```

Note: if you only want to use it from the SRE Assistant in chat, these steps are unnecessary (connect from the "Ops & monitoring" tab described at the top). These steps are for connecting a Terminal (CLI) claude session.

For Athena: (a) start with the plain aws CLI (zero extra setup; claude can drive it from Bash), (b) for the full experience, `uvx awslabs.aws-dataprocessing-mcp-server@latest`. For a PoC, (a) is often enough.

## 4. Zabbix

The initMAX one (the de facto standard; read_only setting, full API coverage) is designed to run as a **team-shared systemd resident service** connected over remote HTTP, which is heavy for a personal PoC. To try things out lightly, get a feel with a stdio version from PyPI (e.g. `uvx zabbix-mcp`, `ZABBIX_URL`/`ZABBIX_TOKEN`), and evaluate initMAX when assessing it for real adoption.

## 5. Verifying it works and trying the brainstorming

```bash
claude mcp list        # list of registered servers and connection health
```

Open a (claude) session and try it on a real incident:

- "List the incidents currently open in PagerDuty and summarize the timeline of the latest one"
- "Pull that service's error rate for the last hour from Grafana's Prometheus and cross-check it against the alert firing time"
- "Analyze the ERROR patterns in that Lambda's logs in CloudWatch and give 3 hypotheses of what happened, in chronological order"
- "Organize the investigation so far into timeline → impact scope → cause hypotheses → next actions, formatted for an external report"

Points to evaluate: whether cross-tool situation assessment is fast / quality of hypotheses / how usable the external drafts are / token consumption and response speed.

## 6. Cleanup

```bash
claude mcp remove -s user grafana
claude mcp remove -s user pagerduty
claude mcp remove -s user cloudwatch
```

Don't forget to revoke the tokens as well (delete the Grafana SA token and the PagerDuty User API Token).

## Limits of the manual route

- Tokens sit in plaintext in `~/.claude.json` (→ register the server in [MCP servers](20-settings.md#mcp-servers) to keep the registration encrypted)
- Servers added with `claude mcp add` cannot be attached to chat / an assistant (→ register them in [MCP servers](20-settings.md#mcp-servers) instead, and attach them to an assistant you created)
- Alert bodies and logs are **input an attacker can influence**. Do not break the read-only setup. If you experiment with writes, do it explicitly in a dedicated assistant/session
- uvx-based servers fetch from PyPI on first launch (egress required). On a memory-constrained host, don't start too many at once

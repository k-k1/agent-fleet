---
audience: "anyone connecting a session to services outside the workspace"
---

# 15. External services and signed-in CLIs — gh / bkt / jira / confluence, Discord / Slack, ops tooling

English | [日本語](15-external-services.ja.md)

This chapter covers the outside services you use from a session: the `gh`, `bkt`, `jira` and `confluence` CLIs, the other CLIs that are already signed in, the Discord / Slack chat bridge, and wiring monitoring tools into a conversation. AWS and Google Cloud are in [16 Cloud accounts](16-cloud-accounts.md).

Using Jira and Confluence in a session is covered in [Use Jira](../user/integrations/jira.md) and [Use Confluence](../user/integrations/confluence.md).

## Use `gh` and `bkt` in a session

Once GitHub is connected, `gh` in a session is already signed in. Once Bitbucket Cloud
(bitbucket.org) is connected, so is `bkt`, the Bitbucket CLI: `bkt pr list`, `bkt pr create`,
`bkt pipeline` and so on need no `auth login`. Both use your saved connection and renew it
the way git does.

**GitHub: `gh pr edit` can fail.** The built-in GitHub OAuth App token has the scopes `repo workflow`
only, and `gh pr edit` loads the pull request in a way that can need `read:org`, so it may end with
`GraphQL: … requires one of the following scopes: ['read:org']`, even on your own personal
repository. The token settings page the message links to does not apply to this token. Edit the
pull request through the REST API instead, which needs only `repo`:
`gh api -X PATCH repos/<owner>/<repo>/pulls/<N> -F body=@<file>` (add `-f title="…"` to change
the title). This avoids the `read:org` error; a token you pasted with `read:org` does not hit that
one, though `gh pr edit` can still fail for other reasons (for example, no access to a team
reviewer on the pull request).

- **Scopes**: `bkt` can do only what the connection was granted. For pull requests and
  pipelines the Bitbucket OAuth app needs `repository`, `pullrequest:write`, `pipeline` (read)
  and `account`, plus `pipeline:write` to start or stop a pipeline; a pasted API token needs the
  matching `read:` / `write:` scopes (for example `read:pullrequest:bitbucket`, and
  `write:pipeline:bitbucket` to start or stop a pipeline — write does not imply read). A token without the pull-request scopes can list repositories
  but gets `403 Forbidden` from `bkt pr list`. The OAuth app's scopes are set by the tenant administrator. The same scope lets the Console's work-item panel post the report-back comment on a Bitbucket pull request: `pullrequest:write` on the OAuth app, `write:pullrequest:bitbucket` on an API token. The Console offers the comment only when the connection is known to hold it. A connection whose scopes are not known (it predates this, or Bitbucket named none) stays read-only until a work-item refresh learns them, and Bitbucket issues are never listed, so only pull requests can be commented on. If you add the scope to the app, re-connect Bitbucket.
- **Bitbucket Data Center** (or any host other than bitbucket.org) is not connected through
  Settings. Set it up yourself (`bkt auth login`, or `BKT_HOST` and `BKT_TOKEN`); the
  `bkt` then runs as you configured it, without the saved connection.

## CLIs that are already signed in

These commands in a session or terminal use a connection you made in Settings, for every agent. You never log
them in from the terminal, and an expiring token is renewed on use.

| Command | Signed in by | Details |
|---|---|---|
| `git` | **Git hosting** (GitHub, Bitbucket) | [06](06-code.md#connect-a-git-provider) |
| `gh` | **Git hosting**, GitHub | [15](15-external-services.md#use-gh-and-bkt-in-a-session) |
| `bkt` | **Git hosting**, Bitbucket Cloud (bitbucket.org) | [15](15-external-services.md#use-gh-and-bkt-in-a-session) |
| `jira` | **Issue tracker**, Jira | [Use Jira](../user/integrations/jira.md) |
| `confluence` | **Issue tracker**, Jira (the same Atlassian connection) | [Use Confluence](../user/integrations/confluence.md) |
| `af-aws-exec --profile <name> -- <command>` | **AWS profiles/SSM**, after **Log in** on the profile | [af-aws-exec](../ref/cli/af-aws-exec.md) |
| `af-gcloud-exec --profile <name> -- <command>` | **Google Cloud**, after **Log in** on the profile | [16](16-cloud-accounts.md#running-a-command-as-a-profile) |

Agents are told to use `bkt` rather than a Bitbucket or Atlassian MCP server, and `jira` by default for Jira. The
CLIs read your saved connection on every call and renew it when it is about to expire, while an MCP connector's
expired login needs the session restarted. To have agents prefer an Atlassian MCP tool for Jira, see
[Use Jira](../user/integrations/jira.md#have-agents-use-the-atlassian-mcp-tool).

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
  Replies are accepted only for the session kinds that can receive messages from other sessions: a reply to a
  shell or SSM session is refused with a note in the thread and never typed into the terminal. Chat traffic
  does not pass through your tenant's allowed-networks restriction (it comes from your bot, not a browser),
  so that refusal is what keeps a chat reply from reaching a raw terminal.
- **Fleet operator**: write in the standing thread "🛰 Fleet Operator" to talk with the
  [11 Running many sessions](11-many-sessions.md) from chat (the same conversation as the operator on the Console
  side). Destructive operations initiated from chat (deletion etc.) pause for an "Approve / Reject" button
  before executing. The button only counts if it is pressed in time and the press can be confirmed:
  a press after the approval has expired is answered "expired or already handled", and one that
  cannot be confirmed is answered "received, but it could not be confirmed (not running it)". Neither
  press runs anything. "Expired or already handled" is also what a second press on an approval you
  already approved gets, and that first press may well have run the operation: check the earlier
  result before asking again. Ask again only after "could not be confirmed", or when the first
  approval clearly lapsed unanswered.
- **Just want to silence notifications**: under Personal → the "Notifications" tab, **Service notifications**
  lets you turn off delivery without disconnecting.

## PagerDuty / Grafana / CloudWatch / AWS connect from the "Ops & monitoring" tab (recommended)

**PagerDuty, Grafana, CloudWatch and AWS are already built into the product**. Use those cards.

1. Open the **Settings > Ops & monitoring** tab, enter the connection details on each card, and hit "Connect":
   - **PagerDuty**: an API key. A **read-only key** is recommended (choose "Read-only" under Integrations > API Access Keys in PagerDuty). For EU accounts, turn the toggle on.
   - **Grafana**: the instance URL and a **service account token** (Viewer permission recommended). Self-hosted / Grafana Cloud / **Amazon Managed Grafana** all work (for AMG, use the workspace endpoint as the URL; for how to issue a token and the 30-day expiry, see "Amazon Managed Grafana" below).
   - **CloudWatch**: just **pick an SSM connection's profile** from the dropdown (the region can optionally be overridden). **No secrets are entered**; a dedicated config file is generated from the profile's SSO settings (non-secret), and the AWS credentials inside the container are read as-is. If SSO login hasn't been done yet / has expired, the tools will error out, so press **Log in** on that profile under Settings › [AWS profiles/SSM](../user/integrations/aws-sso.md#log-in), or open the matching SSM session once. If you manage `~/.aws` yourself, you can specify the profile name directly via "Manual entry".
   - **AWS** (Agent Toolkit for AWS): connects to the MCP server AWS operates. The profile works exactly like CloudWatch (pick an SSM connection's profile; no secrets entered). Two extra settings:
     - **MCP endpoint**: the region the MCP server itself runs in (`us-east-1` / `eu-central-1`). This is **not** where your resources live. That goes in the "Region" field above.
     - **Write tools**: off by default (read-only). Turning it on enables AWS API calls (`call_aws`) and script execution (`run_script`), which **can create, change and delete real AWS resources**. Turn it on only when you need it.
     - AWS is the only one that also works **from interactive sessions** (the other three are chat-only), so AWS documentation search, skill retrieval and AWS API lookups are available right where you write code.
     - If SSO login hasn't been done yet / has expired, press **Log in** on that profile under Settings › [AWS profiles/SSM](../user/integrations/aws-sso.md#log-in), or open the matching SSM session once.
2. Credentials are stored encrypted inside the workspace and are handed over only when the MCP server starts (they don't end up in config files or plaintext). Grafana starts with write and admin tools disabled; the CloudWatch server itself has read-only tools only; AWS starts with `--read-only` by default.
3. In chat, pick the **"SRE Assistant"** and start a new conversation. Ask things like "List the PagerDuty incidents currently open and summarize what happened", "Check this service's error rate for the last hour in Grafana", or "Analyze the ERROR entries in this log group with CloudWatch". It will help you organize the situation, form hypotheses about the cause, and draft external reports while checking the real data (read-only; it does not ack/resolve).
4. Connection changes take effect **from the next chat message** (and, for AWS in a session, **from the next session launch**). No workspace restart needed.

### Amazon Managed Grafana: issuing the token

The card needs the same kind of service account token as self-hosted Grafana (no IAM or SigV4 setup).
Issue it from AMG's own Grafana admin UI (Administration → Service accounts; administrator required), or,
if you have the IAM permissions, with the AWS CLI's `aws grafana create-workspace-service-account-token`
(run it as a profile with `af-aws-exec`, see [af-aws-exec](../ref/cli/af-aws-exec.md)).
AMG tokens last **30 days at most**; paste a new one into the card when the old one expires.

### Other tools (Zabbix and the like)

Tools without a card (Zabbix, a Terminal (CLI) session's own Grafana / PagerDuty / CloudWatch server, and the like) are
connected as MCP servers under Settings > Connections > [MCP servers (used by sessions)](../user/integrations/mcp-servers.md#advanced). Under
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
the profile needs only read access to the logs ([af-aws-exec](../ref/cli/af-aws-exec.md)).

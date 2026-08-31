---
audience: "anyone wiring monitoring tools into a conversation"
updated: "2026-08"
---

# 13. Ops tooling PoC — incident brainstorming over MCP 🧪

English | [日本語](13-ops-tooling.ja.md)

## PagerDuty / Grafana / CloudWatch / AWS connect from the "Ops & monitoring" tab (recommended)

**PagerDuty, Grafana, CloudWatch and AWS are already built into the product** (docs/25 Phase 1; requires an image rebuild). Use that first, rather than the manual PoC steps described later.

1. Open the **Settings > Ops & monitoring** tab, enter the connection details on each card, and hit "Connect":
   - **PagerDuty**: an API key. A **read-only key** is recommended (choose "Read-only" under Integrations > API Access Keys in PagerDuty). For EU accounts, turn the toggle on.
   - **Grafana**: the instance URL and a **service account token** (Viewer permission recommended). Self-hosted / Grafana Cloud / **Amazon Managed Grafana** all work (for AMG, use the workspace endpoint as the URL; for how to issue a token and the 30-day expiry, see the AMG section below).
   - **CloudWatch**: just **pick an SSM connection's profile** from the dropdown (the region can optionally be overridden). **No secrets are entered** — a dedicated config file is generated from the profile's SSO settings (non-secret), and the AWS credentials inside the container are read as-is. If SSO login hasn't been done yet / has expired, the tools will error out, so open the relevant SSM session once, or run `AWS_CONFIG_FILE=~/.aws/af-ops/cloudwatch.config aws sso login --profile <profile-name>` in a terminal. If you manage `~/.aws` yourself, you can specify the profile name directly via "Manual entry".
   - **AWS** (Agent Toolkit for AWS): connects to the MCP server AWS operates. The profile works exactly like CloudWatch (pick an SSM connection's profile; no secrets entered). Two extra settings:
     - **MCP endpoint**: the region the MCP server itself runs in (`us-east-1` / `eu-central-1`). This is **not** where your resources live — that goes in the "Region" field above.
     - **Write tools**: off by default (read-only). Turning it on enables AWS API calls (`call_aws`) and script execution (`run_script`), which **can create, change and delete real AWS resources**. Turn it on only when you need it.
     - AWS is the only one that also works **from interactive sessions** (the other three are chat-only), so AWS documentation search, skill retrieval and AWS API lookups are available right where you write code.
     - If SSO login hasn't been done yet / has expired, open the relevant SSM session once, or run `AWS_CONFIG_FILE=~/.aws/af-ops/aws.config aws sso login --profile <profile-name>` in a terminal.
2. Credentials are stored encrypted inside the workspace and are handed over only when the MCP server starts (they don't end up in config files or plaintext). Grafana starts with write and admin tools disabled; the CloudWatch server itself has read-only tools only; AWS starts with `--read-only` by default.
3. In chat, pick the **"SRE Assistant"** and start a new conversation. Ask things like "List the PagerDuty incidents currently open and summarize what happened", "Check this service's error rate for the last hour in Grafana", or "Analyze the ERROR entries in this log group with CloudWatch" — it will help you organize the situation, form hypotheses about the cause, and draft external reports while checking the real data (read-only; it does not ack/resolve).
4. Connection changes take effect **from the next chat message** (and, for AWS in a session, **from the next session launch**). No workspace restart needed.

Other tools such as Zabbix can be connected manually with the PoC steps below (they will be folded into the "Ops & monitoring" tab over time).

---

## (PoC) Connecting other tools manually 🧪

**These are experimental steps.** Manual steps for tools not yet in the "Ops & monitoring" tab (CloudWatch / Zabbix, etc.), or for when you want to connect a **Terminal (CLI) claude session** rather than chat.

- Scope: Terminal (CLI) claude sessions. **Chat (the assistant) cannot take extra MCP servers today** (planned for Phase 1).
- Prerequisite: outbound connectivity from the workspace to each monitoring tool's endpoint. PyPI access is needed for `uvx`'s first fetch.
- ⚠️ **Token handling (a PoC-only compromise)**: tokens passed via `claude mcp add -e` are **stored in plaintext** in `~/.claude.json`. Because it's inside the home volume it survives a container recreate, but never write tokens into a repository, and use **read-only, dedicated tokens only**. Fixing this plaintext problem (integrating into Connections) is the main goal of Phase 1.

## 0. Prep (one time only; survives a recreate)

```bash
mkdir -p ~/.local/bin
# uv/uvx (launcher for Python MCP servers; goes into ~/.local so it persists)
pip install --user uv
```

## 1. Grafana (metrics, logs, alerts, OnCall)

A single Go binary — the lightest and most complete. **Verified**: v0.17.1 with `-disable-write -disable-admin` becomes read-only with 52 tools (Prometheus/Loki queries, dashboard search, alerts, Incident/OnCall lookups, Sift analysis; zero create/update/delete/install tools). Tools that query CloudWatch / Athena / Elasticsearch etc. through Grafana data sources are also included.

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
- The token is issued from AMG's Grafana admin UI (Administration → Service accounts; admin required), or — if you have the IAM permissions — via the AWS CLI (it expires after **30 days at most**, so watch the expiry):

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

Terminal (CLI) claude sessions can use Bash, so **you can brainstorm over logs with the baked-in aws CLI without wiring up MCP at all**. If you're already logged in with the SSO profile you use for SSM connections, there is zero extra setup. Ask claude to "look at the last hour of ERROR in `<log-group>`" and it will run commands like the following on its own:

```bash
aws sso login --profile <sso-profile>          # get this done when your on-call shift starts
export AWS_PROFILE=<sso-profile> AWS_REGION=ap-northeast-1

aws logs describe-log-groups --log-group-name-prefix /aws/   # find log groups
aws logs tail /aws/ecs/my-service --since 1h                 # recent logs (--follow to keep tailing)
aws logs filter-log-events --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '1 hour ago' +%s)000 --filter-pattern ERROR

# For aggregation or cross-group queries, use Logs Insights (two steps: start-query → get-query-results)
qid=$(aws logs start-query --log-group-name /aws/ecs/my-service \
  --start-time $(date -d '3 hours ago' +%s) --end-time $(date +%s) \
  --query-string 'filter @message like /ERROR/ | stats count(*) by bin(5m)' \
  --query queryId --output text)
aws logs get-query-results --query-id $qid
```

The IAM required is read-only (equivalent to `CloudWatchLogsReadOnlyAccess`: DescribeLogGroups / FilterLogEvents / GetLogEvents / StartQuery / GetQueryResults).

### 3b. Connect via MCP (when you want alarm analysis and anomaly detection tools)

Official AWS (awslabs). Credentials are read from the same chain as the baked-in aws CLI, so **no extra secret is needed if you have an SSO profile**. All tools are read-only (metric retrieval, alarm history, log-group anomaly pattern analysis, Logs Insights queries, and more).

```bash
# You must have already run aws sso login inside the container (same routine as SSM sessions)
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

Points to evaluate (UC1/UC2 in docs/25): whether cross-tool situation assessment is fast / quality of hypotheses / how usable the external drafts are / token consumption and response speed.

## 6. Cleanup

```bash
claude mcp remove -s user grafana
claude mcp remove -s user pagerduty
claude mcp remove -s user cloudwatch
```

Don't forget to revoke the tokens as well (delete the Grafana SA token and the PagerDuty User API Token).

## Known limitations (= to be resolved in Phase 1 and later)

- Tokens sit in plaintext in `~/.claude.json` (→ moving to Connections + secrets.enc)
- Not usable from chat / the assistant (→ making `chatMCPArgs` catalog-driven)
- Alert bodies and logs are **input an attacker can influence**. Do not break the read-only setup. If you experiment with writes, do it explicitly in a dedicated assistant/session
- uvx-based servers fetch from PyPI on first launch (egress required). On a memory-constrained host, don't start too many at once

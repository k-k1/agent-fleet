---
audience: "whoever defends the deployment (IT / SRE with a shell on the host and the deployment administrator role)"
source_of_truth: "the Control Plane's sign-in, cookie and workspace-start settings (a statement here that contradicts the code is a bug in this page)"
---

# Other operational controls

English | [日本語](controls.ja.md)

The controls that protect the deployment and fit on no other page. Each of them, set wrong, weakens the protection without anyone noticing.

## How it works

| Control | What you do |
|---|---|
| The preview domain | Put it on a registrable domain other than the Console's |
| The sign-in allowlist | Remove people from it to remove them. Left empty, nobody gets in |
| Each IdP's trust declaration | Always set `AF_OIDC_<ID>_TRUST`, and pin Entra ID to your own tenant |
| The host's cloud identity | Keep workspaces from reaching the metadata endpoint |
| The audit log | Follow changes and destructive operations in Admin > Audit |

## Put the preview domain on a separate domain

The sign-in cookie is `SameSite=Lax`, and sibling subdomains count as the same site. A page served from a preview under the Console's domain can send requests to the Console with the member's cookie.

The Control Plane has these safety nets:

- Cookie-authenticated writes whose `Sec-Fetch-Site` is not `same-origin`, or whose `Origin` is not the Console's own host, are refused with 403
- `/api/` bodies must be `application/json` (`multipart/form-data` for uploads)
- It returns `X-Frame-Options: DENY` and `frame-ancestors 'self'`, and the Console's shell carries `script-src 'self'` (same-origin scripts only, no inline)

**Stronger is to put `AF_PREVIEW_DOMAIN` on a separate registrable domain** (the Console on `af.example.com`, previews on `*.af-preview.example.net`): the cookie is then never sent at all.

- The cookie is named `af_session` and has no `__Host-` prefix yet, so a `__Host-` name cannot be chosen today
- Behind `AUTH=proxy`, the gateway must pass `Host` through unchanged, or `PUBLIC_BASE_URL` must name the public host: that is what `Origin` is compared with, the same condition as for WebSocket connections

## Keep the sign-in allowlist

| Rule | Detail |
|---|---|
| Empty means nobody | When all three `AF_OAUTH_ALLOWED_*` lists are empty and there is no one with a membership and no auto-join domain, every sign-in is refused |
| No restart needed | `_EMAILS_FILE` is re-read on every sign-in, so additions and removals take effect without restarting the Control Plane |
| Checked on every request | Not only at sign-in but on every request. Someone removed from the list is shut out on their next request, without waiting for `AF_SESSION_TTL` |
| An invitation is also a right to sign in | Someone added to a tenant in the Admin screen can sign in without being on `AF_OAUTH_ALLOWED_*`. On an invitation-run deployment the roster lives in one place, the memberships |

Getting through the door does not mean getting into a tenant: someone with no membership stops at the "ask your administrator" screen.
Setting it up is in [Install Agent Fleet](../install.md#6-sign-in-as-the-first-administrator); removing people is in [Revoke access](offboarding.md).

## Declare why each IdP's email address can be trusted

The allowlist is written in email addresses, so why the address an IdP returns can be trusted is the strength of the authorization itself.

- `AF_OIDC_<ID>_TRUST` (`email_verified` or `issuer`) is required. An IdP without it is disabled at start
- Pin the Entra ID issuer to your own tenant's GUID. With `/common/` or `/organizations/`, anyone with a Microsoft account reaches the sign-in screen, and a personal account can change its own email address
- So with such an issuer the Control Plane does not start unless `AF_OIDC_<ID>_ALLOWED_TIDS` is set ([Sign in with Microsoft Entra ID](../signin/entra.md))

## Read the audit log

- `audit_log` holds changes and destructive operations only. Reads are not recorded (the exception is exporting a member's agent memory)
- The raw terminal stream is never stored, because secrets can be mixed into it
- The deployment administrator and tenant administrators read it in Admin > Audit ([Read the audit log](../../admin/audit.md))

## A vendor's equivalent feature is turned off on purpose

Claude Code's own cross-session messaging (`/list-agents`, `SendMessage`) also brings back usage telemetry to Claude when enabled, so a self-hosted deployment leaves it off by default.

- Agent Fleet's **Messages between sessions** (Settings > Agents, off by default) covers the same need. Delivery and attribution stay under Agent Fleet's control: messages reach only the same workspace, and the receiver is told the message is not the user's instruction
- When a member reports that "`/list-agents` doesn't work", it is this decision, not a fault

## Keep secrets out of logs

- The Control Plane does not hold or interpret credentials in plain text, and never logs them
- A common credential helper decrypts them on each use, so git and provider credentials never become plain-text files
- The exception is MCP server secrets: they are written in plain text (0600) inside the home, to the agent CLIs' settings files ([Operate MCP and external-connection sign-in](mcp.md#what-to-know-about-members-mcp-servers))

## Hand over the in-workspace browser

An agent can hand a page of a Chromium it started in the workspace to the member, as a Console pane, so a person can do something like a login.

- Remote debugging is opened on loopback only
- Right after the handover it is "**View only**" and passes none of the member's input. Letting them operate it takes an explicit move to "**User control**"
- The agents' instructions also tell them never to put the CDP endpoint or cookies in answers, logs or commits

## Keep the host's cloud identity from workspaces

A workspace container can reach the metadata endpoint (`169.254.169.254`) of the machine it runs on.
An AWS SDK without the member's credentials silently switches to the role it finds there. On an EC2 host with an instance profile, every session becomes that role, with no error.

The Control Plane keeps the SDK from asking:

- docker and Kubernetes: workspaces start with `AWS_EC2_METADATA_DISABLED=true`
- ECS: the task role is removed, and the same variable is set

Blocking it on the network is the host's job.

| Host | What to do |
|---|---|
| The compose EC2 host (`deploy/aws/ec2-single`) | IMDSv2 with hop limit 1 (`HttpTokens: required`, `HttpPutResponseHopLimit: 1`). The host-network Control Plane is one hop away and can use the instance profile; workspaces on the docker bridge are two hops away and get no token. Apply it directly to a running instance rather than through a stack update (commands in the [ec2-single runbook](../../../deploy/aws/ec2-single/README.md#instance-metadata-imds)); when the Ubuntu AMI parameter has moved on, a stack update can replace the instance |
| Any other docker host in a cloud | The same metadata settings, or a host firewall rule in Docker's `DOCKER-USER` chain that rejects `169.254.169.254` from the workspace bridge. It needs root on the host (compose itself needs no new privilege) |
| Kubernetes | The variable does not block the cluster's metadata server (the node's service account, Workload Identity on GKE). Block that path in the cluster, with a NetworkPolicy egress rule or GKE metadata concealment. The Control Plane does not do it |
| ecs-ec2 | The slot user data sets `ECS_AWSVPC_BLOCK_IMDS=true`. Older slots need replacing: schedule it in Admin > Slots ([The ecs-ec2 slot pool](../11-slot-pool.md#ecs-ec2-replacing-outdated-slots-new-ami-or-launch-template-change)) |
| native | Workspaces run directly on the member's machine, so leave it as it is: the instance role there is that machine's own |

### When workspaces get the task role

`AF_WS_WORKLOAD_AWS=1` on the Control Plane gives ECS workspaces the task role back and stops suppressing the SDK's metadata lookup.

- It lifts none of the network protections above: a docker workspace still cannot reach the host's instance profile
- Handing that over takes a separate, deliberately allowed credential path. By default, keep the network protections

### Apply it

A running docker workspace keeps the environment it was created with; `docker compose up -d` or a Docker restart does not change it.

1. Update the Control Plane and workspace images (the same when you change `AF_WS_WORKLOAD_AWS`)
2. **Stop and then start** every workspace in the Console. The containers are recreated
3. Apply the host settings in the table above
4. Check from a session's shell without printing the environment (the three checks are in the [ec2-single runbook](../../../deploy/aws/ec2-single/README.md#instance-metadata-imds))

| Check | `AF_WS_WORKLOAD_AWS` off (default) | `AF_WS_WORKLOAD_AWS=1` |
|---|---|---|
| The variable | Shows `true` | Absent on docker |
| An AWS call with no profile | Fails with "Unable to locate credentials" | Resolves the task role on ECS |
| An IMDSv2 token request | Does not succeed | Where the host blocks metadata, it must still fail |

Members run AWS commands as themselves with `af-aws-exec` ([af-aws-exec](../../ref/cli/af-aws-exec.md)).

## Related

- [Understand the threat model and residual risks](threat-model.md)
- [Revoke access and hand over super_admin](offboarding.md)
- [Set up how people sign in](../signin/overview.md)
- [Expose it to the internet](../run/public-internet.md)

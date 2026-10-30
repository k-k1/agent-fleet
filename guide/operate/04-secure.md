---
audience: "someone responsible for the deployment's security posture"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
---

# 04. Security Operations

English | [日本語](04-secure.ja.md)

This chapter summarizes the **assumptions an operator must understand** and the **day-to-day
controls to apply** in order to run Agent Fleet safely. It
**discloses properties inherent in the architecture and shows how to handle them in
operations**. The external-facing threat model is in `SECURITY.md`
(English), and the design background is in `docs/build/07-security.md`; this
document expands those into operational procedures.

## Threat model (summary)

Inside a Workspace, CLI agents **execute arbitrary code** (operation that includes
`--dangerously-skip-permissions` is the default). The boundaries are drawn under the assumption
that "a user's session runs untrusted code," and what we protect is "other users' data, the
CP/host infrastructure, secrets, and data exfiltration." The primary isolation boundary sits
between the **Workspace (low trust)** and the **CP and the infrastructure it runs on (high
trust)** on the multi-user targets (`docker`, `ecs`, `ecs-ec2`). `native` has no such boundary:
it is single-user, and its one user is the operator (`SECURITY.md` → "`docker` and `native`").

Skipping every tool approval is the **default, not a fixed rule**: each user can turn approvals
back on per agent kind (Settings > Agents) or for a single session at launch. That changes how
much a mistake costs, **not where the boundary is**: only some agent kinds offer the choice,
the mode can be cycled back from inside the TUI, and the CLI's own settings are not locked down. Treat tool
approval as a way to catch accidents, and keep treating the workspace boundary as the only real
containment on the multi-user targets.

- **The CP can reach every workspace in its deployment.** It starts them, hands each one its
  DEK, and on `docker` drives the host's daemon through the mounted Docker socket. Consequently,
  **if the CP or the infrastructure under it is compromised, isolation within that deployment
  collapses all at once**.
- **Companies are separated by separate deployments.** The impact above is **confined to the
  inside of that single deployment** and does not spread to other companies (= other
  deployments); on AWS, only when each deployment has its own AWS account. This is the core
  strength of the one-company = one-deployment delivery model.

## Residual risks: what to do about them

The list of residual risks (what each one is and why) is `SECURITY.md` → "Known residual
risks", grouped by deployment target (every target, `docker` and `native`, `ecs` /
`ecs-ec2`). Read it for the risks themselves; this section keeps only the operational steps.

- **Restrict administrative access to what the CP runs on.** On `docker`, **minimize the set
  of people who can SSH into the host or run sudo / docker there**, and consider putting the
  Docker socket behind a filtering proxy (e.g. `tecnativa/docker-socket-proxy`). On AWS,
  **give each deployment its own AWS account** and limit who administers it.
- **`AF_MASTER_KEY`**: **store it in a vault separate from the DB and homes, and back it up
  independently**. Never place it in the data area or in backup archives (by design it never
  goes in). For when it is generated and how to store it, see [02 §2](02-install.md); for the
  identity requirement at restore time, see [03](03-run.md).
  **Without it, members' stored credentials are kept unencrypted in their homes.** That is
  only meant for `AUTH=dev`: under any other `AUTH` the Control Plane still starts, but logs a
  `WARNING` at start-up and shows a warning banner in the administration screen. Setting the
  key on a deployment that has been running without one is not transparent, so plan it with
  your members, in this order: set the key and restart the Control Plane; **stop and start
  every existing workspace** (one that keeps running still has no key and goes on writing
  plaintext); then members reconnect the credentials they had stored. Finally delete the old
  unencrypted file (`~/.config/agent-fleet/secrets.json` in each home), which is also in your
  backups, and rotate the credentials it held.
- **Backups**: strictly control who can access where they are stored, and enforce at-rest
  encryption there.

A caveat on limits: by default the key that protects each workspace's credentials is derived
from the master key, so the effective strength is equivalent to the single `AF_MASTER_KEY`. On
AWS that key can be held by KMS instead ([12 Keys at rest](12-keys.md)). Details in `docs/build/07-security.md`
§7.6.

## Operating egress control

There is a mechanism for controlling outbound traffic (egress) from Workspaces. It is a
**forward-proxy approach**: it inspects the FQDN (CONNECT/SNI) to make allow/deny decisions and
does not decrypt TLS. Only super_admins operate it, from the **Egress** tab of the Admin panel
in the Console.

**Rolling it out in stages is the core of the design.** Proceed in this order.

1. **log-only (observe only, the default)**: blocks nothing; it only records destinations.
   Per-host counts of allow/block candidates accumulate under "Observed destinations" in the
   Admin panel. Start here to **understand the actual traffic**.
2. **Firm up the allowlist**: while reviewing the observations, add the legitimate
   destinations to the allowlist. The allowlist is versioned (active / proposed / retired); the
   AI only **proposes; approval is done by humans** (approve/reject under "Proposed (needs
   approval)" in the Admin panel). Entries are a **host** or a **`.suffix.example.com`**, with an optional
   reason, and are **retired** when no longer needed; with no added entries only the product's built-in
   allowances apply. This deployment's own Control Plane host (the public URL, and the internal
   URL where one is set) is always allowed and listed as **Built-in**: workspaces reach the
   Control Plane through the egress path, so it cannot be retired, and enforce does not cut them
   off from a Control Plane served on ports 80/443 under a name of its own. The entry is a whole
   host, every port and path of it, so give the Control Plane a hostname no other service
   shares. It lifts none of the gateway's other refusals (other ports, internal addresses). An
   IPv6-literal URL (`https://[2001:db8::5]`) is listed as the address in its compressed lowercase form, written to the gateway in both the bare and the bracketed spelling (the ACL match was checked against Squid 6.13; reaching a real Control Plane over IPv6 has not been, and neither has the Squid version you run, so check that workspaces reach it); one with a zone ID, an IPv4-mapped or IPv4-compatible address, or a loopback / link-local / multicast address is not, and the Control Plane logs a warning at start. Entries you add yourself stay host names and `.suffix` entries; an IPv6 literal cannot be added by hand. A member's request from ⚙ Settings → MCP servers (used by sessions) arrives as a proposal too, and the
   member sees "waiting for an administrator's approval" until you decide.

   **Reading the observations:** counts are approximate. A report that is retried after a
   timeout can be counted twice, and the proxy keeps at most 5000 distinct destinations
   while the Control Plane is unreachable (overflow shows up as an `egress.dropped` audit
   entry), and loses what it held in memory if it restarts. Treat a missing host as "not
   seen", not "never used", and check the audit log for `egress.dropped` before enforcing.
   Where the egress gateway records connections (AWS with `EgressRoute=gateway`), each one is also
   kept as a raw event attributed to its workspace, read through `GET /api/admin/egress/events`
   (super_admin; the Admin panel shows only the per-host counts). Raw events are deleted after
   `AF_EGRESS_EVENT_RETENTION_DAYS` (default 30, `0` keeps them forever); the per-host counts stay.
3. **Switch to enforce**: once the allowlist is sufficiently solid, switch the mode to
   enforce. From then on, traffic outside the allowlist is **blocked**. Switching straight to enforce also
   kills the traffic you need (agent CLI updates, MCP endpoints), and clearing the block candidates under
   **Observed destinations** while still in log-only is what makes the switch uneventful. The Admin UI also
   warns you to confirm reality in log-only first before switching.

> Current implementation scope: **observation (log-only) and allowlist management work, and the
> proxy itself can block (enforce)**. Setting `AF_EGRESS_PROXY_ADDR` on the Control Plane
> injects `http_proxy` / `https_proxy` / `no_proxy` (and their upper-case forms) into every workspace
> on every target (off by default), so programs that honour those variables go through the proxy.
> The CP's internal address (`AF_CP_INTERNAL_URL`) is kept out of the proxy on docker, Kubernetes, ecs and
> ecs-ec2, and the ecs / ecs-ec2 targets also keep the link-local task and instance metadata
> addresses (`169.254.170.2`, `169.254.169.254`) out of it, because the proxy refuses link-local
> destinations. What is **not built yet** is forcing
> traffic through it (an internal network or security-group egress rule that leaves the proxy as
> the only way out, and templates that run the proxy), so a process that ignores the variables
> still goes straight out, and **switching to enforce does not yet constrain a workspace**.
> The exception is AWS (`ecs` / `ecs-ec2`) with `00-network`'s `EgressRoute=gateway`: the
> workspace route table then goes through an egress gateway (Squid), so a workspace has no other
> way out and enforce applies to it. That option is new and was verified on a test deployment
> only, **not yet in production** (see "Choosing the egress route on AWS" in [choosing a target](01-choose.md)).
> Elsewhere, understand that you can operate up to the "observe and grow the allowlist" stage.
> The full design picture is in `docs/build/07-security.md` §7.8.

## MCP servers and external connections

Users register **their own MCP servers** under ⚙ Settings → MCP servers (used by sessions), and a tenant admin can
**distribute one to every member** ([admin/04](../admin/04-mcp-outside.md#distributing-an-mcp-server-to-everyone-in-the-tenant-your-permission)). Four things matter to
an operator.

- **Where secrets live.** Environment variable and header values are stored with envelope
  encryption and handed over only when the server starts; nothing is left in a config file in the
  clear.
- **Only remote (HTTP) can be distributed.** Distributing stdio would be equivalent to an admin
  running an arbitrary command in everybody's container, so it is forbidden by design (a personal
  registration may still use stdio).
- **It is coupled to egress.** A registration does nothing if its destination host is not on the
  allowlist. A user's request for one arrives in the Admin egress tab as "Proposed (needs
  approval)"; see the procedure above.
- **There is an inbound door too.** A user can issue an **MCP token** and drive their workspace
  from Claude Code / Claude Desktop on their own machine. The endpoint is `/mcp` (Bearer auth);
  on the deployment side it depends on the feature flag (`AF_MCP_ENABLED`) and on whether the
  ingress passes `/mcp`. The scope (read / write / admin:dangerous) and the expiry are the user's
  choice, and they can revoke it themselves. Over that door a member can also let their own agent
  use their Agent Fleet memory; it is a separate per-member setting, off by default ([member guide](../member/18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent),
  Settings > Agent memory > Settings). External saves and forgets are audited (`memory.entry.save` /
  `memory.entry.forget`, actor `mcp`, target the project id). A tenant administrator can block it for the
  whole tenant ([admin 04](../admin/04-mcp-outside.md#memory-from-outside-agent-fleet)); `AF_MCP_ENABLED` turns the whole endpoint off.

### Sign-in for remote MCP clients (OAuth)

By default `/mcp` accepts a Personal Access Token and nothing else. With `AF_MCP_OAUTH=1` the Control Plane is also
an OAuth 2.1 authorization server for `/mcp` (ADR 0118), so claude.ai,
ChatGPT, Claude Code and Codex connect by signing the member in to the Console instead of pasting a token. PATs keep
working for CI and clients without OAuth.

| Setting | Meaning |
|---|---|
| `AF_MCP_OAUTH=1` | turns the server on. Needs `AF_MCP_ENABLED=true` and an `https` `PUBLIC_BASE_URL`; with any of the three missing every path below answers 404 and `/mcp` keeps its bare 401, so clients fall back to asking for a token |
| `AF_MCP_OAUTH_TRUSTED_HOSTS` | comma-separated host names that may receive the sign-in redirect and serve client metadata. Default `claude.ai,claude.com,chatgpt.com`; `none` leaves loopback clients only. A super admin can override it at runtime ([Trusted client hosts](#trusted-client-hosts-deployment-wide-superadmin-only)) |

- **Turning it on.** Compose / native / local: set `AF_MCP_ENABLED=true` and `AF_MCP_OAUTH=1` (exactly those
  values; `1` is not accepted for `AF_MCP_ENABLED`) and, optionally, `AF_MCP_OAUTH_TRUSTED_HOSTS` in the Control Plane's
  environment. Kubernetes: add them to the Control Plane's environment file (`cp.env`). **ECS:** the 30-ingress
  parameters `McpEnabled=on`, `McpOAuth=on` and optionally `McpOAuthTrustedHosts`, set with a targeted
  `aws cloudformation deploy` of `30-ingress.yaml` (`--parameter-overrides McpEnabled=on McpOAuth=on`).
  `update.sh` overrides only `ImageTag` and the facts it reads from the other stacks (`EfsBackup`, `WorkspaceSubnets`
  and the egress parameters), so it neither turns them on nor turns them off: parameters keep their
  previous value, and one a stack has not had yet starts off. See [`deploy/aws/ecs/README.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/README.md).
- **What is served** (issuer `{PUBLIC_BASE_URL}/mcp-oauth`): `/.well-known/oauth-protected-resource/mcp`,
  `/.well-known/oauth-protected-resource/mcp/memory`, `/.well-known/oauth-authorization-server/mcp-oauth`, `/mcp-oauth/authorize`, `/mcp-oauth/consent`,
  `/mcp-oauth/token`, `/mcp-oauth/register` and `/mcp-oauth/revoke`. The `/mcp` 401 now carries
  `WWW-Authenticate: Bearer resource_metadata="…"` (with `, error="invalid_token"` appended when a presented token is invalid).
  The client requests the scopes; the consent page starts with `read` selected and offers `write` only when requested.
- **Ingress.** The hosted clients call `/mcp` and the token endpoint **from their vendors' servers**, so the Control
  Plane has to be reachable from the internet on those paths. A deployment behind a VPN or a narrow ALB ingress CIDR
  gets command-line (loopback) clients only; that is by construction, not a fault.
  Under `AUTH=oauth` or `AUTH=dev` the Console's own session gate already lets the discovery, `token`, `register`
  and `revoke` paths through and keeps `authorize` and `consent` behind it. Under **`AUTH=proxy`** the gateway has
  to do the same: `/mcp-oauth/authorize` and `/mcp-oauth/consent` must pass **through** it (the member's identity header comes from
  there), and the other six paths plus `/mcp` and `/mcp/memory` must **bypass** it.
- **Two resources.** `/mcp` serves every tool; `/mcp/memory` serves the six external memory tools and nothing else, for
  a claude.ai connector that claude.ai also hands to the member's Workspace sessions. Each has its own metadata
  document and 401 challenge, and each refuses the other's tokens: OAuth codes, grants and tokens are bound to the
  resource the client asked for (a refresh keeps it), and a Personal Access Token is issued for one endpoint
  (`mcp` by default, which every token issued before the split is). The tenant switch, the member's memory
  setting, the scope tiers and the audit entries are the same on both.
- **Who may be a client.** Registration is open (dynamic client registration is rate-limited per source, capped in
  number, and a registration that never produces a grant is deleted after 30 days) but a registration only chooses
  where a code is *sent*, so a redirect must be loopback or an `https` address on the trusted list, spelled
  canonically (no upper case, trailing dot, explicit port, user info or fragment). A client identified by a metadata
  document URL is fetched only when that URL is on the same list, without following redirects, with a short timeout
  and a size cap, and never to a non-public address.
- **Tokens.** Access tokens are opaque, last one hour and are bound to `{PUBLIC_BASE_URL}/mcp` or `{PUBLIC_BASE_URL}/mcp/memory`; refresh tokens rotate on
  every use and presenting a used one revokes the whole connection. A connection ends after 30 idle days or 90 days.
  Only hashes are stored. They are not PAT rows and do not count toward the 20-token PAT limit.
- **Audit.** `mcp.oauth.grant` (created, revoked, reuse-revoked) and `mcp.oauth.token` (each issuance) are written before
  the response leaves; a call made with a connection is recorded as `mcp` with the connection's id as the actor. If the
  audit store is down no token is issued.
  Tenant policy changes are recorded as `tenant.mcp_oauth` (`enabled=true/false`). Member revocations use
  `mcp.oauth.grant` with `revoked by member`; tenant disablement uses `revoked reason=tenant-disabled` for each
  connection it ends. A code exchange refused during disablement appends `refused reason=tenant-disabled`
  to distinguish its earlier audit-before-issue `created` attempt from an actual connection.
- **Scope.** `read` and `write` only. `admin:dangerous` is never granted over OAuth, whatever the member's role.
- **Tenant network restriction** applies to the consent step (the person in the browser), not to `/mcp` and the token
  endpoint, which come from the vendors' servers, as with PATs (ADR 0047).

### Trusted client hosts (deployment-wide — super_admin only)

**Admin → Deployment → MCP sign-in hosts** lists the client hosts this deployment's MCP OAuth server trusts: the hosts that may receive the sign-in redirect and serve client metadata. It is deployment-wide, not per tenant, and only a super administrator sees it. Loopback clients (a CLI on the member's own machine) are always allowed.

- **To let another Agent Fleet deployment sign in to this one's `/mcp`, add its host here** (the host name only, for example `af.example.com`).
- **Removing a vendor works**: delete `chatgpt.com` from the list and save, and that vendor can no longer sign in. Saving an empty list trusts no hosted client at all.
- Entries are exact host names: no `https://`, port, path or wildcard, and no IP address. A mistake is refused with a message and nothing is saved.
- **Which list applies**: the list saved here wins; with nothing saved, the `AF_MCP_OAUTH_TRUSTED_HOSTS` environment variable; with that unset, the built-in default (`claude.ai`, `claude.com`, `chatgpt.com`). The screen says which one is in effect. **Reset to default** deletes the saved list so the other two apply again.
- A change applies from the next request, with no restart, to new sign-ins, client registration, and token requests. A connection whose redirect host (or, for a metadata-URL client, whose own host) was removed can no longer refresh its token or redeem a pending code; an access token already issued works until it expires (at most one hour). Loopback connections are not affected. Every change is audited (`mcp_oauth.trusted_hosts.set` / `.reset`, with the old and new lists).
- **A client host that resolves to a non-public address breaks metadata-URL (CIMD) sign-in.** The Control Plane fetches the client's metadata document from the host named in `client_id`, and refuses any host whose address, as the Control Plane resolves it, is not public (loopback, private ranges, link-local, and carrier-grade NAT `100.64.0.0/10`, which is where tailnet addresses live). So when the client deployment's own name resolves to such an address from here, its sign-in stops at `authorize` with the generic refusal page, even though the host is on this list. Only dynamic client registration (DCR) works for that client: the client side offers **Retry with dynamic registration** in its unfinished-sign-in note. The check is a deliberate SSRF guard and is not configurable; fix the name resolution as seen from the Control Plane if CIMD is wanted.

### Sign-in for OAuth-protected MCP servers members register

The other direction from the section above: when a member registers a remote MCP server that only accepts OAuth, the
member's own Agent signs in to it (ADR 0119). The Control Plane owns two public paths for that, and both need a
reachable `PUBLIC_BASE_URL`:

- `GET {PUBLIC_BASE_URL}/oauth2/mcp/callback` — where the server's authorization server sends the browser back. It
  sits under the `/oauth2/` prefix that is already outside the session gate, and checks the caller itself (the Console session under `AUTH=oauth`, the
  gateway's identity header under `AUTH=proxy`): the browser must belong to the member who started the sign-in, and a sign-in is valid for 10 minutes and once.
- `GET {PUBLIC_BASE_URL}/oauth/mcp-client.json` — the client ID metadata document (CIMD), served only when
  `PUBLIC_BASE_URL` is `https`. Authorization servers that support CIMD fetch it **from their own servers**, so
  under `AUTH=proxy` the gateway has to let exactly this path bypass authentication; nothing else under `/oauth/` is
  public. The callback, by contrast, must pass **through** the gateway so the member's identity header is on it: with no
  identity the sign-in fails. Without an `https` base URL, or when the authorization server cannot fetch it, the
  Agent falls back to dynamic client registration, or asks the member for a client id.

Tokens are kept by the Agent, in the member's encrypted store. One case sends them through the Control Plane: a tenant
administrator who distributes such a server with a **confidential client** they registered (its client secret). The
secret stays in the Control Plane, so the Control Plane runs the token, refresh and revocation requests for the
members' Agents and passes the answers through without storing them. For that case the **Control Plane** itself has
to reach the authorization server over `https` (it discovers the token endpoint from the issuer the administrator entered,
connects to public addresses only and follows no redirect); every other request is made from the workspace. The Control Plane writes
`mcp.client_oauth` audit rows (`started`, `completed`, `failed`, `disconnected`) that name the server and the
authorization server, never a code or token. Members sign in from Settings › MCP servers
([member guide 20](../member/20-settings.md#mcp-servers-used-by-sessions)).

### Reading what a client sent to `/mcp`

Every request to `/mcp` — refused ones, a `GET` or `DELETE` answered 405 before authentication, each
item of a batch — writes one Control Plane log line, so a client that connects and then shows no tools can be
diagnosed from what it actually did:

```
mcp-call endpoint="/mcp" http_method="POST" status=400 latency_ms=2 principal="grant:<id>" client_id="<id>" header_version="2026-07-28" mcp_method_header=false mcp_name_header=false accept="application/json, text/event-stream" content_type="application/json" user_agent="…" items=[{"method":"tools/list","era":"stateless","meta_version":"2026-07-28","client_name":"…","status":400,"error_code":-32020,"error_message":"header mismatch: Mcp-Method (absent) does not match body method \"tools/list\""}]
```

- `endpoint` is `/mcp` or `/mcp/memory`, the path the request was made to.
- `principal` is `pat:<id>`, `grant:<id>` (OAuth; `client_id` follows) or `unauthenticated`, with `reason=` saying why
  (no bearer token, unknown or expired token, an OAuth token bound to another resource, a PAT bound to another endpoint, `method not allowed`).
- `era` is `legacy` (an `initialize` handshake) or `stateless` (2026-07-28: `_meta` on every request). A stateless request
  the server rejects shows the JSON-RPC `error_code` (-32020 header mismatch, -32022 unsupported version, -32602 a missing `_meta` field, `clientInfo` and `clientCapabilities` included)
  and the `Mcp-Method` / `Mcp-Name` presence flags; `tools/list` items carry `tools`, the number returned. A `tools/call` that ran and failed (answered 200 with `isError`) shows `tool_error:true`, without the text.
- Never recorded: a token, the `Authorization` value, request arguments or tool results. Only the tool name of a `tools/call` / `prompts/get` is.
- The same records of an authenticated caller are readable by their owner in memory (last 50 per token / connection,
  not persisted, lost on restart, **per Control Plane replica**) under Settings → Outside access → MCP tokens & apps → *Recent calls*.
  Behind several replicas look for the line in the logs of all of them; 405 and unauthenticated requests exist only in the log.

## Handing over the in-workspace browser

An agent can hand a page from the Chromium it started inside the workspace to the user as a
Console pane (so a human can perform a login, say). Remote debugging is exposed on **loopback
only**, an attachment starts in **view-only** (it rejects every input from the user) and must be
explicitly moved to user-control before they can operate it. Not putting CDP endpoints or cookies
into answers, logs or commits is part of the agent-side instructions as well.

## Development sign-in (dev OIDC IdP — super_admin only)

The **Dev sign-in** section of the Admin modal switches the Control Plane's development OIDC IdP
(ADR 0116) for the whole deployment. **It is on by default** and super_admin only, so tenant
administrators do not see it. There is no per-workspace switch: while it is on (and preview subdomains exist) every workspace's
client is served.

- The section shows the issuer, how many workspaces hold a client (one is created when a workspace starts while
  the IdP is offered, and kept from then on), and the one rule that matters:
  **a production app must never trust this issuer**, because it signs in as any member without a
  password.
- Turning it off takes effect without a restart (another Control Plane replica follows within a few
  seconds): every `/dev-idp/…` URL answers 404. **Workspaces' client registrations are kept**, so turning it on
  again hands the same client ids and secrets back.
- **Members can use it only with preview subdomains.** Without a preview domain
  (`AF_PREVIEW_DOMAIN`) the section says so, the `/dev-idp/…` URLs answer 404, nothing is injected into
  workspaces and workspaces report it unavailable. The switch itself stays operable.
- It shows as locked when the operator set `AF_DEV_IDP=0`, and cannot be on without
  `PUBLIC_BASE_URL`; the section says which one applies. Every change is audited (`dev_idp.set`).

## Other operational controls

- **The login allowlist is fail-closed.** If all 3 of the `AF_OAUTH_ALLOWED_*` variables are
  empty **and nobody holds a tenant membership and no tenant has an auto-join domain**, all
  logins are rejected. `_EMAILS_FILE` is re-read on every login, so **additions take effect
  without a CP restart** (removals likewise). The check runs **on every request**, not just at
  sign-in, so removing someone locks them out on their very next request instead of waiting out
  `AF_SESSION_TTL`. That is the offboarding path. Configuration is in [02 §6](02-install.md).
- **Being invited is itself permission to reach the login.** Somebody added to a tenant in the
  Admin panel can sign in without also appearing in `AF_OAUTH_ALLOWED_*`, so a deployment run
  on invitations keeps one roster instead of two lists that drift apart. Passing the door does
  not put anyone *inside* anything: which tenant they may use is a separate check, and somebody
  with no membership lands on the same "ask an administrator" page as before.
- **Each login provider declares why its email may be trusted.** `AF_OIDC_<ID>_TRUST` is
  mandatory (`email_verified` or `issuer`) and a provider that omits it is disabled at startup,
  because the allowlist is written in email addresses. In particular, an Entra ID issuer must be
  pinned to your tenant GUID: on the `/common/` or `/organizations/` endpoints every Microsoft
  account on earth reaches the login and a personal account can rewrite its own email address,
  so the CP refuses to start there unless `AF_OIDC_<ID>_ALLOWED_TIDS` is set
  ([05 §4](05-signin.md) / `docs/build/07-security.md` §7.3).
- **Audit log.** Only mutating / destructive operations are recorded in `audit_log` (reads are
  not, except the export of a member's agent memory, and **raw terminal streams are never
  stored, due to the risk of secrets leaking into them**). super_admins / tenant_admins view it
  from the Audit tab of the Admin panel. The admin volume covers how to read it operationally.
- **Some vendor features are deliberately left disabled.** Claude Code's own cross-session
  messaging (`/list-agents` / `SendMessage`) is one: **enabling it also brings back Claude's
  usage telemetry**, so it stays off as a self-hosted default. The same capability is provided by
  Agent Fleet's own implementation instead (Settings > Agents > session-to-session messaging,
  **off by default**), where delivery and attribution are under your control; messages stay
  within one workspace, and the receiving side is told explicitly that it is not an instruction
  from the user. When a user reports that "`/list-agents` doesn't work", it is this decision, not
  a fault.
- **Designed to keep secrets out of logs.** The CP neither holds nor interprets credential
  plaintext, and does not emit it into logs. The unified cred helper decrypts on demand and
  hands it over, so no plaintext files are ever created
  (`docs/build/07-security.md` §7.6).
- **Workspaces do not get the host's cloud identity.** A workspace container can reach the
  cloud metadata endpoint (`169.254.169.254`) of the machine it runs on, and an AWS SDK with no
  member credentials falls back to whatever role it finds there: on an EC2 host with an
  instance profile, that role, in every session, without an error. The Control Plane starts
  every workspace with `AWS_EC2_METADATA_DISABLED=true` (docker and Kubernetes) or withholds the task role
  and sets it (ECS), which stops the SDKs from asking; the network block is the host's job:
  - **EC2 host for compose** (`deploy/aws/ec2-single`): IMDSv2 with hop limit 1
    (`HttpTokens: required`, `HttpPutResponseHopLimit: 1`). The host-network Control Plane is
    one hop and can still use an instance profile; a workspace on a docker bridge is two and
    gets no token. For an instance that already exists, apply it to the running instance (the
    call is in [the ec2-single runbook](../../deploy/aws/ec2-single/README.md#instance-metadata-imds)); a stack update can replace the instance
    when the Ubuntu AMI parameter has moved.
  - **Any other docker host in a cloud**: the same metadata settings, or a host firewall rule
    in Docker's `DOCKER-USER` chain that rejects `169.254.169.254` from the workspace bridges
    (it needs root on the host; compose itself needs nothing new).
  - **Kubernetes**: the variable does not block the cluster's metadata server (the node's
    service account, Workload Identity on GKE). Blocking that route is the cluster's job (a
    NetworkPolicy egress rule, or GKE metadata concealment); the Control Plane does not do it.
  - **ecs-ec2**: the slot user data sets `ECS_AWSVPC_BLOCK_IMDS=true`; retained slots have to
    be replaced: reserve them in Admin → Slots ([03-run](03-run.md), "ecs-ec2:
    replacing slots after a launch template change").

  `AF_WS_WORKLOAD_AWS=1` on the Control Plane hands the ECS task role back to workspaces and
  stops suppressing the SDKs' metadata lookup. It removes none of the network protections above,
  so a docker workspace still cannot reach the host's instance profile; giving it one needs a
  credential path you permit separately. Keep the network protection as the default.

  **Rolling it out.** A running docker workspace keeps the environment it was created with:
  `docker compose up -d` and a Docker restart do not change it. After upgrading the Control
  Plane and the workspace image (or changing `AF_WS_WORKLOAD_AWS`), have every workspace
  **Stopped and Started** in the Console, which recreates its container, and apply the host
  metadata settings above. Check from a session shell, without printing the environment (the three checks
  are in [the ec2-single runbook](../../deploy/aws/ec2-single/README.md#instance-metadata-imds)). With the opt-in off (the default), the variable reads
  `true`, an AWS call with no profile fails with "Unable to locate credentials", and the IMDSv2 token
  request does not succeed. With `AF_WS_WORKLOAD_AWS=1` the first two are expected to differ (the
  variable is unset on docker, and ECS resolves the task role); the token request must still
  fail wherever the host blocks metadata.

  A workspace on the native runtime runs directly on the member's machine and is left alone:
  an instance role there is that machine's own.
  Members run AWS commands as themselves with `af-aws-exec` ([member guide 16](../member/16-cloud-accounts.md)).

## Offboarding: how access is actually revoked

There are two ways in, so there are two ways out. Which one applies depends on how the
deployment admits people.

| How they get in | How you take them out | When it takes effect |
|---|---|---|
| The allowlist (`AF_OAUTH_ALLOWED_*`) | Remove them from it | The next request (the check runs per request) |
| Tenant membership (an invitation) | **Admin panel → the tenant → the member → "Remove member"** | The next request |

**Sessions cannot be revoked individually.** The session cookie is stateless: a signed
`{email, exp}` and nothing else, with no server-side session store, so there is no "sign out
of all devices" and a cookie stays technically valid for up to `AF_SESSION_TTL` (7 days by
default). What actually shuts the door is the per-request re-check above. So:

> **Removing them at the IdP is not enough on its own.** Disabling the Microsoft/Google account
> stops them getting a *new* session; the one already in their browser keeps working until you
> also remove them from the allowlist or from the tenant.

Take the steps in this order; the first one is what revokes access, the rest are cleanup:

1. **Remove the membership** (or take them off the allowlist).
2. **Stop the workspace** (Admin panel → the member → "Force-stop the workspace").
3. **Clean the home**: only after they have pushed anything they still want. `~/repos` is not
   recoverable afterwards. It keeps their logins and connections, and on deployments that take
   backups of homes it keeps those too; "Delete backups" and destroying the workspace remove
   them. Where the deployment does not offer Clean home, destroying the workspace is the step
   ([ref/deploy-targets](../ref/deploy-targets.md)).

Two asymmetries are worth knowing *before* somebody leaves rather than after:

- **Scheduled runs are personal.** A schedule belongs to the membership, so everything the
  person had scheduled **stops**. Whoever takes over recreates them.
- **Internal git repositories belong to the tenant.** They survive; nothing is lost when the
  person who created them leaves.

### The emergency stop: rotating `AF_COOKIE_SECRET`

If you need everyone's session invalidated *right now* (a leaked cookie, a laptop lost, an
account you cannot reach), the only immediate switch is to change the cookie signing key: generate a new random value
(the [compose runbook](../../deploy/compose/README.md) has the one-liner for `AF_COOKIE_SECRET`), put it in
`AF_COOKIE_SECRET` (`.env`, `oauth.env` or the SSM parameter) and restart the CP.

Every session cookie signed with the old key stops verifying, so **everybody is logged out and
signs in again**. It is blunt, it costs everyone one sign-in, and it is the only thing that
works within seconds. Note what it does *not* do: it does not remove anyone's access: if the
person is still on the allowlist or still holds a membership, they simply sign in again. Use it
together with the removal steps above, not instead of them.

## Handing over `super_admin`

`SUPER_ADMIN_EMAILS` (the host's env) is the single source of truth for who administers the
deployment, and it is read **once at startup**, so a change needs a CP restart. Deliberately,
there is no way to promote a super_admin from inside the Console: the people who can run the
whole deployment should be exactly the people who can edit the host's files.

1. Edit `SUPER_ADMIN_EMAILS` (add the successor, remove the predecessor) and restart the CP.
2. On restart the CP **also revokes the role in the database** for any account no longer listed.
   It logs `super_admin revoked (not in SUPER_ADMIN_EMAILS): …`. Without this step the old
   administrator would keep the role in the database forever, because the natural fix ("sync it
   at login") never reaches somebody who has left and never logs in again.
3. The successor gets the role on their first sign-in.
4. Then offboard the predecessor as above (memberships, workspace, home).

> If the only super_admin leaves without handing over, this is recoverable: whoever can edit
> the host's env adds themselves and restarts. The one prerequisite is that **somebody in the
> company can still reach the host**, which is worth checking before you need it.

## Reporting vulnerabilities

If you find a vulnerability, **do not open a public issue**; report it privately. The
channels, what to include in a report, and which versions receive fixes are in `SECURITY.md`
→ "Reporting a vulnerability" and "Supported versions".

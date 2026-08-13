# 03. Security Operations

English | [日本語](03-security.ja.md)

This chapter summarizes the **assumptions an operator must understand** and the **day-to-day
controls to apply** in order to run Agent Fleet safely. It is not a list of hidden bugs; it
**honestly discloses properties inherent in the architecture and shows how to handle them in
operations**. The external-facing threat model is in [SECURITY.md](../../../SECURITY.md)
(English), and the design background is in [dev/07 Security](../../dev/07-security.md); this
document expands those into operational procedures.

## Threat model (summary)

Inside a Workspace, CLI agents **execute arbitrary code** (operation that includes
`--dangerously-skip-permissions` is the default). The boundaries are drawn under the assumption
that "a user's session runs untrusted code," and what we protect is "other users' data, the
CP/host infrastructure, secrets, and data exfiltration." The primary isolation boundary sits
between the **Workspace container (low trust)** and the **CP/host infrastructure (high trust)**.

- **`docker.sock` = equivalent to host root.** The CP drives the host's daemon through the
  mounted Docker socket and injects plaintext DEKs at Workspace startup. Consequently, **if the
  CP or the host is compromised, isolation within that deployment collapses all at once**.
- **Companies are separated by separate deployments.** The impact above is **confined to the
  inside of that single deployment** and does not spread to other companies (= other
  deployments). This is the core strength of the one-company = one-deployment delivery model.

## The 4 residual risks (operators must understand these)

Here we expand, from an operational standpoint, the 4 points that
[SECURITY.md](../../../SECURITY.md) lists. These are not undisclosed bugs; they are known
properties inherent in the current architecture.

1. **The CP holds privileges equivalent to host root.** As noted above, it can operate the host
   via docker.sock. Operations: **minimize the set of people who can SSH into the host or run
   sudo / docker there**. If you want to narrow the Docker API surface, a hardening option is to
   put the socket behind a filtering proxy (e.g. `tecnativa/docker-socket-proxy`).

2. **`AF_MASTER_KEY` is the root of credential encryption.** Every per-workspace DEK is wrapped
   with a per-tenant KEK derived from this key. **Losing it means crypto-shred = the stored
   credentials and every backup become permanently undecryptable**. Operations: **store it in a
   vault separate from the DB and homes, and back it up independently**. Never place it in the
   data area or in backup archives (by design it never goes in). For when it is generated and
   how to store it, see [01 §2](01-install.md); for the identity requirement at restore time,
   see [02](02-operations.md).

3. **Backups are sensitive.** The archive contains each user's home and **plaintext Claude
   login state**. Operations: strictly control the permissions of the storage location (limit
   who can access it) and enforce at-rest encryption.

4. **Access to docker.sock = access to the host.** Anyone who can run the CP container, or who
   can reach the Docker socket, can control the host. Operations: **restrict who can deploy /
   operate**.

A caveat on limits: in the current localCustodian, the KEK is derived from the master key, so
the effective strength is equivalent to the single `AF_MASTER_KEY`. **True per-tenant
crypto-shred via tenant key revocation will be achieved when a Vault/KMS custodian is adopted
in the future** (currently design only). Details in [dev/07 §7.6](../../dev/07-security.md).

## Operating egress control

There is a mechanism for controlling outbound traffic (egress) from Workspaces. It is a
**forward-proxy approach**: it inspects the FQDN (CONNECT/SNI) to make allow/deny decisions and
does not decrypt TLS. Only super_admins operate it, from the **Egress** tab of the Admin panel
in the Console.

**Rolling it out in stages is the core of the design.** Proceed in this order.

1. **log-only (observe only — the default)** — blocks nothing; it only records destinations.
   Per-host counts of allow/block candidates accumulate under "Observed destinations" in the
   Admin panel. Start here to **understand the actual traffic**.
2. **Firm up the allowlist** — while reviewing the observations, add the legitimate
   destinations to the allowlist. The allowlist is versioned (active / proposed / retired); the
   AI only **proposes — approval is done by humans** (approve/reject under "Proposed (needs
   approval)" in the Admin panel).
3. **Switch to enforce** — once the allowlist is sufficiently solid, switch the mode to
   enforce. From then on, traffic outside the allowlist is **blocked**. The Admin UI also warns
   you to confirm reality in log-only first before switching.

> Current implementation scope: **observation (log-only) and allowlist management work**. The
> actual blocking (enforce), and enabling the accompanying always-on container-side wiring
> (internal network + proxy env injection), are **not yet completed and are follow-up work**.
> For now, understand that you can operate up to the "observe and grow the allowlist" stage.
> The full design picture is in [dev/07 §7.8](../../dev/07-security.md).

## MCP servers and external connections

Users register **their own MCP servers** under ⚙ Settings → MCP servers, and a tenant admin can
**distribute one to every member** ([admin/04](../admin/04-mcp-egress.md)). Four things matter to
an operator.

- **Where secrets live.** Environment variable and header values are stored with envelope
  encryption and handed over only when the server starts; nothing is left in a config file in the
  clear.
- **Only remote (HTTP) can be distributed.** Distributing stdio would be equivalent to an admin
  running an arbitrary command in everybody's container, so it is forbidden by design (a personal
  registration may still use stdio).
- **It is coupled to egress.** A registration does nothing if its destination host is not on the
  allowlist. A user's request for one arrives in the Admin egress tab as "Proposed (needs
  approval)" — see the procedure above.
- **There is an inbound door too.** A user can issue an **MCP token** and drive their workspace
  from Claude Code / Claude Desktop on their own machine. The endpoint is `/mcp` (Bearer auth);
  on the deployment side it depends on the feature flag (`AF_MCP_ENABLED`) and on whether the
  ingress passes `/mcp`. The scope (read / write / admin:dangerous) and the expiry are the user's
  choice, and they can revoke it themselves.

## Handing over the in-workspace browser

An agent can hand a page from the Chromium it started inside the workspace to the user as a
Console pane (so a human can perform a login, say). Remote debugging is exposed on **loopback
only**, an attachment starts in **view-only** (it rejects every input from the user) and must be
explicitly moved to user-control before they can operate it. Not putting CDP endpoints or cookies
into answers, logs or commits is part of the agent-side instructions as well.

## Other operational controls

- **The login allowlist is fail-closed.** If all 3 of the `AF_OAUTH_ALLOWED_*` variables are
  empty, all logins are rejected. `_EMAILS_FILE` is re-read on every login, so **additions take
  effect without a CP restart** (removals likewise). Configuration is in [01 §6](01-install.md).
- **Audit log.** Only mutating / destructive operations are recorded in `audit_log` (reads are
  off by default, and **raw terminal streams are never stored, due to the risk of secrets
  leaking into them**). super_admins / tenant_admins view it from the Audit tab of the Admin
  panel. The admin volume covers how to read it operationally.
- **Some vendor features are deliberately left disabled.** Claude Code's own cross-session
  messaging (`/list-agents` / `SendMessage`) is one: **enabling it also brings back Claude's
  usage telemetry**, so it stays off as a self-hosted default. The same capability is provided by
  Agent Fleet's own implementation instead (Settings > Agents > session-to-session messaging,
  **off by default**), where delivery and attribution are under your control — messages stay
  within one workspace, and the receiving side is told explicitly that it is not an instruction
  from the user. When a user reports that "`/list-agents` doesn't work", it is this decision, not
  a fault.
- **Designed to keep secrets out of logs.** The CP neither holds nor interprets credential
  plaintext, and does not emit it into logs. The unified cred helper decrypts on demand and
  hands it over, so no plaintext files are ever created
  ([dev/07 §7.6](../../dev/07-security.md)).

## Reporting vulnerabilities

If you find a vulnerability, **do not open a public issue** — report it privately. The
preferred channel is GitHub's Security → "Report a vulnerability" (private advisory). The
information to include in a report (affected version/commit, deployment form, reproduction
steps, observed impact) and the response policy are in [SECURITY.md](../../../SECURITY.md).
Because we are pre-1.0, fixes are made against the latest tag. Update to the latest version
before reporting.

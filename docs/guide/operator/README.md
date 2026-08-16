# operator — Self-Hosting Operator Guide

English | [日本語](README.ja.md)

This guide is for the people who **deploy, operate, and protect** Agent Fleet on their own
infrastructure (IT / SRE staff with SSH access to the host OS and `super_admin` privileges).
No knowledge of the development workflow is assumed. What you need is a general grounding in
Docker, DNS, OAuth, and backup operations.

For those in the position where "if it breaks, only I can fix it," this guide explains
**what, why, and by which decisions**. Meanwhile, **the source of truth for the actual command
procedures is [deploy/compose/README.md](../../../deploy/compose/README.md) (the English runbook)**.
This guide does not duplicate commands; it points you to the right section. If you want to know
how things work internally, see the developer docs [dev/09 Deploy](../../dev/09-deploy.md) and
[dev/07 Security](../../dev/07-security.md) (links go one way: guide → dev).

## Structure of this volume

| File | Contents |
|----------|------|
| [README.md](README.md) (this document) | Big picture, components, operator responsibilities, summary for prospective adopters |
| [01-install.md](01-install.md) | Initial setup (generating secrets, OAuth configuration, startup, first login, first tenant) |
| [02-operations.md](02-operations.md) | Day-to-day operations (backup, restore, upgrades, air-gapped networks, shutdown) |
| [03-security.md](03-security.md) | Security operations (threat model, residual risks, egress controls, reporting channel) |
| [04-troubleshooting.md](04-troubleshooting.md) | Incident response and FAQ (diagnosing the 3 DooD constraints, triage flow) |
| [05-login-idp.md](05-login-idp.md) | Sign-in methods, end to end: what to create at Google / Entra ID / GitHub / another OIDC IdP, which value goes into which setting, and how to check it |

---

## For those considering adoption

This section summarizes "what it can do, what it requires, and what its security posture is."
Read this section first as the entry point for a technical evaluation.

### What it can do

- Your team members can use CLI coding agents such as Claude Code **from a browser**.
  Each member gets an isolated environment (**Workspace** = a dedicated container), clones
  repositories, and launches and drives AI sessions and shells. There is also a chat-centric
  way of working for members who are not comfortable with a terminal.
- Administrators (`super_admin` / `tenant_admin`) can add members, set resource limits
  (memory, idle shutdown), visualize usage, view audit logs, and observe outbound destinations,
  all from the Admin panel in the browser.
- If you split departments into **tenants**, their Workspaces are fully isolated from each
  other (the default is a single tenant).

### Prerequisites (what you need to adopt)

- **A Linux host that runs Docker** (Docker Engine + `docker compose`). A single host is all it takes.
- **A public domain** and DNS A/AAAA records pointing at that host (for automatic TLS).
  If your deployment is internal-only and you cannot provide public DNS, there is a
  self-signed TLS alternative (see [01](01-install.md)).
- **A login IdP client**: a Google OAuth 2.0 web client, or an OIDC app registration at
  Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab. You register exactly one
  redirect URI, whichever (and however many) providers you enable. What to create at the IdP is
  [05-login-idp.md](05-login-idp.md).
- Your team's **Claude seats are brought by each member** (BYO). After the first startup,
  each member logs in with their own seat from the Console. Company Team/Enterprise seats
  are recommended over personal Pro/Max.

### Delivery model and security posture (summary)

- **One company = one deployment.** Each company stands up an independent instance on its own
  infrastructure. Isolation between companies is guaranteed by **separate deployments**, not by
  "in-process boundaries." As a result, the impact of a compromise (blast radius) is **confined
  to the inside of that one deployment**.
- Inside a Workspace, the boundaries are designed on the assumption that AI agents **execute
  arbitrary code**. What we protect is "other users' data, the CP/host infrastructure, secrets,
  and data exfiltration."
- There are 4 residual risks that must be disclosed honestly (`docker.sock` = host-root
  equivalent, losing `AF_MASTER_KEY` = crypto-shred, the sensitivity of backups, host access =
  full control). Details are collected in [03-security.md](03-security.md) and the public-facing
  [SECURITY.md](../../../SECURITY.md). Be sure to read them before making an adoption decision.

For the broader picture, see the project [README](../../../README.md) and
[dev/01 Architecture](../../dev/01-architecture.md).

---

## Components (the minimal model an operator must understand)

On a single host, `docker compose` manages **two services**.

- **Control Plane (CP)** — the brain. It handles login authentication, tenant/member
  management, starting and stopping Workspaces, and relaying all API traffic. The CP is a
  container, but it **drives the host's Docker daemon from the outside** to start Workspace
  containers (this approach is called DooD = docker-out-of-docker).
- **Caddy** — the front door (reverse proxy). It automatically obtains and renews the TLS
  certificate for the public domain from Let's Encrypt and forwards traffic to the CP behind it.

Meanwhile, **user Workspace containers (`af-ws-<user>`) are not managed by compose**.
The CP starts them at runtime with `docker run`. This is a very important operational property.

- `docker compose down` or restarting the CP **does not stop Workspaces** (users are not disconnected).
- Briefly stopping the CP during a backup does not affect running Workspaces.
- On the other hand, "stopping compose stops everything" is not true, so if you want to
  forcibly stop all Workspaces, you need a separate operation (force-stop in [02](02-operations.md)).

Persistent data lives **entirely under `DATA_DIR` (default `/srv/agent-fleet/data`)**: the DB,
each user's home, envelope-encrypted credentials, and Caddy's certificates. Backups target this
directory. The sole exception is `AF_MASTER_KEY`, which goes into **neither** `DATA_DIR` **nor**
backups (see below).

The DooD approach has 3 constraints that break things silently when violated (host networking,
the identical absolute `DATA_DIR` path, and the docker group GID). The compose definition
contains them, but their meaning is explained from a diagnostic perspective in
[04](04-troubleshooting.md). The background on how this works is in [dev/09](../../dev/09-deploy.md).

## Operator responsibility checklist

- [ ] Stored `AF_MASTER_KEY` in **a vault separate from the data** and backed it up
      independently (loss = all credentials become permanently undecryptable).
- [ ] Run `backup.sh` regularly via cron, and protect the archive storage location with
      permissions and encryption.
- [ ] Tried the restore procedure at least once for real, and understand the `DATA_DIR`
      basename constraint.
- [ ] Always take a backup before upgrading (**downgrade is not possible**).
- [ ] Configured the login allowlist (`AF_OAUTH_ALLOWED_*`) appropriately (empty = deny all,
      fail-closed).
- [ ] If you sign in with Entra ID, pinned `AF_OIDC_<ID>_ISSUER` to your own tenant GUID
      (the `/common/` endpoint would put every Microsoft account in front of the login).
- [ ] Kept the set of people with SSH, sudo, and docker execution rights on the host OS to a
      minimum (= host-root equivalent).
- [ ] If introducing egress controls, understand the policy of observing in log-only mode
      first, then moving to enforce in stages.
- [ ] Know the procedure for reporting a vulnerability you find
      ([SECURITY.md](../../../SECURITY.md)).

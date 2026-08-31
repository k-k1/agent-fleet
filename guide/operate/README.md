---
audience: "whoever installs, upgrades and protects a deployment — IT / SRE with shell access to the host and deployment-administrator rights"
source_of_truth: "the scripts under `deploy/` for commands; this shelf for what each step decides"
updated: "2026-08"
---

# Operating a deployment

English | [日本語](README.ja.md)

For the person in the position of "if it breaks, only I can fix it". This shelf
explains **what, why, and by which decisions**. No knowledge of the development
workflow is assumed; a general grounding in Docker, DNS, OAuth and backups is.

## Chapters

1. [Choosing a deployment target](01-choose.md) — which one, and what ECS really costs
2. [Install](02-install.md) — generating secrets, sign-in configuration, first start, first tenant
3. [Running it](03-run.md) — backup, restore, upgrades, air-gapped networks, shutdown
4. [Securing it](04-secure.md) — threat model, the residual risks, egress control, reporting channel
5. [Sign-in methods](05-signin.md) — end to end: what to create at Google / Entra ID / GitHub / another OIDC provider, which value goes where, and how to check it
6. [Diagnosing it](06-diagnose.md) — incident response and FAQ, including the three constraints that break silently

What each target supports is [ref/deploy-targets.md](../ref/deploy-targets.md); who may
do what is [ref/roles.md](../ref/roles.md).

## Where the commands are

**This shelf does not duplicate commands.** They live next to the thing they operate,
and they ship inside the release bundle, so a customer holding only the tarball still
has them:

| Target | Runbook |
|---|---|
| compose (the default) | [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | [deploy/native/README.md](../../deploy/native/README.md) |
| a personal WSL2 machine | [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| ec2-single | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |

Inside a workspace the same files are staged as `operate/runbooks/*.md` beside this
shelf, so they are readable from the container too — which is where you will want them
when something is on fire.

If a command in this shelf ever contradicts the script it describes, **the script is
right and this shelf has a bug.**

## For those considering adoption

Read this first for a technical evaluation.

**What it can do.** Your team uses CLI coding agents from a browser. Each member gets
an isolated environment — a dedicated container — clones repositories, and drives agent
and shell sessions. There is a chat-centred way of working for people who would rather
not touch a terminal. Administrators add members, set limits, see usage and audit logs,
and observe outbound destinations, all from the browser. Splitting departments into
**tenants** isolates their workspaces completely; the default is a single tenant.

**What you need.**

- **A Linux host running Docker** (Engine + `docker compose`). One host is enough.
- **A public domain** with DNS pointing at it, for automatic TLS. An internal-only
  deployment that cannot have public DNS has a self-signed alternative
  ([02 Install](02-install.md)).
- **A login provider**: a Google OAuth client, or an OIDC app registration at Entra ID
  / Okta / Keycloak / Auth0 / Cognito / GitLab. You register exactly one redirect URI
  no matter how many providers you enable ([05 Sign-in](05-signin.md)).
- **Agent seats are brought by each member.** After the first start, each person signs
  in with their own. Company Team/Enterprise seats are preferable to personal ones.

**Delivery model and security posture.** One company, one deployment, on its own
infrastructure. Isolation between companies is guaranteed by **separate deployments**,
not by in-process boundaries, so the blast radius of a compromise is confined to one
deployment. Inside a workspace, the boundaries assume the agent **executes arbitrary
code**; what is protected is other users' data, the control plane and host, the
secrets, and exfiltration.

**Four residual risks are disclosed honestly** — `docker.sock` is host-root equivalent,
losing `AF_MASTER_KEY` is a crypto-shred, backups are sensitive, and host access is
total control. They are in [04 Securing it](04-secure.md) and in
`SECURITY.md`. **Read them before deciding to adopt.**

## The minimal model you must hold in your head

On one host, `docker compose` manages **two services**: the **Control Plane**, which
authenticates logins, manages tenants and members, starts and stops workspaces and
relays all API traffic; and **Caddy**, the front door, which obtains and renews the TLS
certificate and forwards to the CP behind it.

**User workspace containers are not managed by compose.** The CP starts them at
runtime. This has consequences that surprise people:

- `docker compose down`, or restarting the CP, **does not stop workspaces**. Users are
  not disconnected.
- Briefly stopping the CP for a backup does not affect running workspaces.
- Conversely, "stopping compose stops everything" is false. Forcibly stopping every
  workspace is a separate operation ([03 Running it](03-run.md)).

Persistent data lives **entirely under `DATA_DIR`** — the database, each user's home,
the envelope-encrypted credentials, Caddy's certificates. That directory is what you
back up. The sole exception is `AF_MASTER_KEY`, which belongs in neither `DATA_DIR` nor
the backup.

Driving the host's Docker daemon from a container has **three constraints that break
things silently when violated** — host networking, an identical absolute `DATA_DIR`
path, and the docker group GID. The compose definition contains them;
[06 Diagnosing it](06-diagnose.md) explains them from the "what does this symptom mean"
side.

## Responsibility checklist

- [ ] `AF_MASTER_KEY` stored in a vault **separate from the data**, and backed up
      independently. Loss = every credential permanently undecryptable.
- [ ] `backup.sh` running on a schedule, with the archive location protected by
      permissions and encryption.
- [ ] The restore procedure **actually rehearsed once**, including the `DATA_DIR`
      basename constraint.
- [ ] A backup taken before every upgrade. **There is no downgrade.**
- [ ] The login allowlist configured. Empty means deny-all — it fails closed.
- [ ] If signing in with Entra ID, the issuer pinned to your own tenant GUID. The
      common endpoint would put every Microsoft account in front of your login screen.
- [ ] The set of people with SSH, sudo or docker rights on the host kept minimal —
      that is host-root equivalent.
- [ ] If introducing egress control, the staged policy understood: observe in log-only
      mode first, then move to enforce.
- [ ] The procedure for reporting a vulnerability known
      (`SECURITY.md`).

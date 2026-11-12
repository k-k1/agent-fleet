---
audience: "whoever defends the deployment (IT / SRE with a shell on the host and the deployment administrator role)"
source_of_truth: "`SECURITY.md` (the threat model and the list of residual risks); the operating steps are this page"
---

# Understand the threat model and residual risks

English | [日本語](threat-model.ja.md)

Learn where Agent Fleet's isolation boundary is and where it is not, and keep the remaining risks in check by how you operate.
The list of risks itself (what remains and why) is "Known residual risks" in `SECURITY.md`; this page covers what you do about them.

## How it works

| What you do | What happens |
|---|---|
| Limit who can touch the infrastructure the Control Plane runs on | The one path that breaks every isolation in a deployment is held by few people |
| Give each company its own deployment (and its own AWS account on AWS) | An incident stays inside one deployment |
| Protect `AF_MASTER_KEY` and the backups separately | Leaked data alone does not reveal the stored secrets |
| Report a vulnerability privately | Other deployments are not targeted before a fix ships |

## Where the boundary is

Inside a workspace, the agent CLIs run arbitrary code, and skipping tool approval is the default.
The boundary is drawn on the assumption that a member's session runs untrusted code.

| | Trust | What it protects |
|---|---|---|
| Workspace | Low | — |
| The Control Plane and the infrastructure under it | High | Other members' data, the infrastructure itself, secrets, exfiltration |

- This boundary exists on the multi-user targets (`docker`, `ecs`, `ecs-ec2`)
- `native` has no such boundary: it has one user, and that user is the operator (`SECURITY.md`, "`docker` and `native`")

### The Control Plane reaches every workspace in the deployment

The Control Plane starts the workspaces and hands each its keys; on `docker` it drives the host's daemon through the mounted Docker socket.
So **a compromise of the Control Plane or the infrastructure under it breaks isolation for the whole deployment at once**.

### Companies are separated by deployment

That blast radius stops at one deployment and does not reach another company's. This is the core reason for one deployment per company.
On AWS it holds only when each deployment has its own AWS account.

### Tool approval is not the boundary

Skipping approval is a default, not a fixture. Members can turn approval back on per agent kind (Settings > Agents) or when they start a session.

- What changes is how likely an accident is, not where the boundary is
- Only some kinds offer it, the mode can be switched back inside the TUI, and the CLI's own settings are not locked

Treat approval as a way to reduce accidents. What actually contains the agent is still the workspace boundary.

## Prepare for the remaining risks

| What to protect | What to do |
|---|---|
| Administrative access to the infrastructure | On `docker`, keep the people who can SSH, sudo or run docker on the host to a minimum, and consider fronting the Docker socket with a filtering proxy (such as `tecnativa/docker-socket-proxy`). On AWS, give each deployment its own account and limit its administrators |
| `AF_MASTER_KEY` | Keep it in a vault separate from the database and the homes, and back it up separately. Never put it in the data directory or a backup archive (by design it is not included) |
| Backups | Limit who can reach the store, and always use encryption at rest |

- Generating and keeping `AF_MASTER_KEY` is in [Install Agent Fleet](../install.md#2-generate-the-secrets); that a restore needs the same key is in [Back up and restore](../run/backup-restore.md#restore)
- A deployment without a key, what the keys must be and what changing them costs are in [Handle the deployment keys](deployment-keys.md)

### The key is the ceiling

By default the key protecting each workspace's credentials is derived from `AF_MASTER_KEY`, so the whole is as strong as that one key.
On AWS the key can be held by KMS ([Keys at rest](../12-keys.md)).

## Report a vulnerability

If you find a vulnerability, report it privately; **do not open a public issue**.

- Where to report and what to include: "Reporting a vulnerability" in `SECURITY.md`
- Which versions receive fixes: "Supported versions" in the same file

## Related

- [Handle the deployment keys](deployment-keys.md): what the keys must be, a deployment without a key, values stored before the key
- [Control outbound traffic](egress.md): narrow what can leave a workspace
- [Revoke access and hand over super_admin](offboarding.md)
- [Self-hosting at a glance](../overview.md): what the operator is responsible for

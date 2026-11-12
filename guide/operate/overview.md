---
audience: "whoever is evaluating running Agent Fleet on their own infrastructure, and whoever builds and protects the deployment (IT / SRE)"
source_of_truth: "the runbooks under `deploy/` for commands; this shelf for what each step decides"
---

# Self-hosting at a glance

English | [日本語](overview.ja.md)

What Agent Fleet does when you run it on your own infrastructure, what you need for it, and what the people running it are responsible for.
Read this before you decide to adopt it, and before your first install.

## How it works

| What the operator does | What Agent Fleet does |
|---|---|
| Prepares a host, decides the settings and secrets, and starts it | The Control Plane handles sign-in, tenants and members, and starting and stopping workspaces |
| Decides the sign-in methods and who may enter | Lets in nobody who is outside the allowlist and the roster |
| Takes backups, guards the keys, upgrades | Keeps all persistent data under `DATA_DIR` |
| Welcomes members | Gives each member an isolated workspace; each member logs in to their agent seat themselves |

## What it can do

- Your team uses CLI coding agents from a browser
- Each member has an isolated environment (a dedicated container), clones repositories, and runs agent and shell sessions
- There is a chat-centred way of working for people who would rather not touch a terminal
- Administrators add members, set limits, see usage and audit logs, and observe outbound destinations, all from the browser
- Splitting departments into **tenants** isolates their workspaces completely. The default is one tenant

## What you need

| What | For |
|---|---|
| A Linux host running Docker (Docker Engine and `docker compose`) | One host is enough |
| A public domain, with DNS pointing at it | The TLS certificate is obtained automatically. An internal-only deployment that cannot have public DNS has a self-signed alternative ([Choose the tenant and provisioning model](install-decisions.md#whether-to-use-tls-internal)) |
| A sign-in provider | A Google OAuth client, or an OIDC app at Entra ID, Okta, Keycloak, Auth0, Cognito or GitLab. However many you enable, you register one redirect URI |
| Agent seats | Each member brings their own. After the first start, each person logs in with their own seat. Company Team / Enterprise seats are preferable to personal plans |

The other targets (AWS, Kubernetes, a machine without Docker) are chosen in [Choose where to deploy](choose.md).

## One company, one deployment

- Each company builds an independent deployment on its own infrastructure
- Isolation between companies comes from **separate deployments**, not from boundaries inside a process. A compromise stays inside that one deployment
- On AWS, that holds only when each deployment has its own AWS account ([Understand the threat model and residual risks](secure/threat-model.md))
- The boundaries inside a workspace assume the agent **executes arbitrary code**. What they protect is other users' data, the Control Plane and the host, the secrets, and exfiltration

### Risks to read before you adopt it

Four risks remain. They are in [Understand the threat model and residual risks](secure/threat-model.md) and in `SECURITY.md`; read them before you decide.

| Risk | What it means |
|---|---|
| `docker.sock` | Whoever can touch it can do what host root can |
| Losing `AF_MASTER_KEY` | Every stored credential becomes permanently undecryptable |
| Backups | Their contents are sensitive |
| Host access | It amounts to every permission there is |

## The minimal model to keep in your head

On one host, `docker compose` manages just two services.

| Service | Role |
|---|---|
| Control Plane | Authenticates sign-ins, manages tenants and members, starts and stops workspaces, relays every API call |
| Caddy | The front door. Obtains and renews the TLS certificate and forwards to the Control Plane behind it |

**Members' workspace containers are not managed by compose.** The Control Plane starts them at runtime, which means:

- `docker compose down`, or restarting the Control Plane, does not stop workspaces. Members are not disconnected
- Pausing the Control Plane for a backup does not affect running workspaces
- Stopping compose does not stop everything. Forcibly stopping every workspace is a separate operation ([Use idle stop and force-stop](run/idle-stop.md#force-stop-a-workspace))

### Where the data lives

- All persistent data is under `DATA_DIR`: the database, each member's home, the encrypted credentials, Caddy's certificates
- That directory is what you back up
- The one exception is `AF_MASTER_KEY`, which goes in neither `DATA_DIR` nor the backup

### Three constraints that break things silently

Because a container drives the host's Docker, three things must hold. The compose definition keeps them; take care when you change it by hand.

- Host networking
- The same absolute `DATA_DIR` path on the host and in the container
- The docker group GID

[Find the cause from the symptom](diagnose/checklist.md#three-constraints-of-driving-docker-from-a-container) reads them from the symptom side.

## Responsibility checklist

- [ ] `AF_MASTER_KEY` is in a vault **separate from the data**, and backed up on its own. Losing it makes every stored credential undecryptable
- [ ] `backup.sh` runs on a schedule, and its archive location is protected by permissions and encryption
- [ ] The restore procedure has been **actually rehearsed once**, including the `DATA_DIR` basename constraint
- [ ] A backup is taken before every upgrade. **There is no downgrade**
- [ ] The sign-in allowlist is set. Left empty, nobody can sign in
- [ ] If you use Entra ID, the issuer is pinned to your own tenant GUID. Left on the common endpoint, every Microsoft account reaches your sign-in page
- [ ] The people who can use SSH, sudo or docker on the host are kept to a minimum. That is the same as host root
- [ ] If you introduce egress control, you understand the stages: observe in log-only first, then enforce
- [ ] You know how to report a vulnerability (`SECURITY.md`)

## Where the commands are

The commands are on each target's procedure pages. The README in each `deploy/` directory is an index of those pages.
Inside a workspace that index is staged under `operate/runbooks/`, so it can be read from the container too.

| Target | Procedure |
|---|---|
| compose (the default) | [Deploy with Docker Compose](compose/install.md) |
| native | [Install the native package](native/install.md) |
| a personal WSL2 machine | [Run it for yourself on WSL](wsl.md) |
| ecs / ecs-ec2 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| ec2-single | [Deploy on a single EC2 instance](aws-ec2-single.md) |
| kubernetes (preview) | [deploy/kubernetes/README.md](../../deploy/kubernetes/README.md) |

If this shelf and a script disagree, the script is right.

## Agents act on their own

- Agents run commands, edit files, and commit and push on members' behalf
- They also act when nobody is watching (scheduled runs that wake a stopped workspace), in modes that skip permission prompts, and in shell or SSM sessions that run the strings they receive verbatim
- Those actions can be destructive or irreversible, and can incur charges on the connected AI provider and cloud accounts
- The operator and each member are responsible for the workspaces, credentials, repositories and infrastructure they connect, and for checking what the agents did. Use least-privilege credentials, keep backups, and put an approval in front of destructive actions
- Agent Fleet is distributed under the Apache License 2.0, without warranty. The authors accept no liability for damage, data loss, downtime or cost arising from its use. See `LICENSE`, and `NOTICE` for the bundled open-source notices
- Official releases come only from <https://github.com/k-k1/agent-fleet-dist>. If you pass a bundle or package on, Apache-2.0 §4(d) requires you to keep the notices in `NOTICE` with it, that URL included, so the next recipient can find the original

## Related

- [Choose where to deploy](choose.md)
- [Install Agent Fleet](install.md)
- [Deployment targets](../ref/deploy-targets.md)
- [Roles](../ref/roles.md)

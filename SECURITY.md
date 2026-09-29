# Security Policy

Agent Fleet stores its members' credentials — sign-ins to the coding-agent CLIs,
git hosting tokens and provider keys, for example — and runs agents that execute arbitrary code
on their behalf. We publish the source so that operators can audit the encryption and
isolation themselves.

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** — do not open a public
issue for anything exploitable.

- Preferred: GitHub → *Security* → *Report a vulnerability* (private advisory).
- Or email [security@agent-fleet.org](mailto:security@agent-fleet.org).

Include: the affected version or commit, the deployment target (`docker`, `native`,
`ecs` or `ecs-ec2`) and the agent kind when it matters, a reproduction or PoC, and the
impact you observed. We aim to acknowledge within a few business days.

## Deployment model (why cross-company blast radius is bounded)

The intended deployment is **one company = one self-hosted deployment**: each company
runs its own instance on its own infrastructure
([decisions/0001](docs/decisions/0001-self-host-vs-saas.md)). Companies are isolated
by *separate deployments*, not by in-process boundaries. On AWS a separate deployment
also needs a separate AWS account (see below).

Tenants inside one deployment are an organisational split, not a wall against the
operator or a compromised Control Plane: they share one Control Plane, one database
and one master key.

The threat model, and the controls that keep one member's workspace from another's on
each deployment target, are owned by
[docs/build/07-security.md](docs/build/07-security.md) (§7.1 and §7.2); what a
workspace is on each target is [guide/ref/deploy-targets.md](guide/ref/deploy-targets.md).

## Known residual risks (operators must understand these)

These are properties of the current architecture, documented so operators make an
informed choice — not undisclosed bugs.

### On every deployment target

- **The Control Plane holds the keys to every workspace.** It unwraps each
  workspace's DEK and injects it at start, and it holds the platform's own power: the
  host Docker socket on `docker`, an AWS task role on `ecs` / `ecs-ec2`
  ([07 §7.1](docs/build/07-security.md#71-threat-model-and-trust-boundary)). A
  compromise of the CP or the host therefore breaks isolation *within that one
  deployment*. It does **not** reach other companies, which are separate deployments.

- **`AF_MASTER_KEY` is the root of the credential encryption.** Every per-workspace
  DEK is derived from it and wrapped by a tenant KEK that is derived from it too
  ([07 §7.6](docs/build/07-security.md#76-secrets-and-envelope-encryption)). **Losing it
  = crypto-shred: all stored credentials become permanently undecryptable**, backups
  included. Store it in a **separate vault** from the database and the homes, and back
  it up independently: neither `deploy/compose/backup.sh` nor the AWS templates copy
  it. On AWS it is the SSM SecureString `<SsmPrefix>/master-key` (`/af-cp` by default),
  and `deploy/aws/ecs/teardown.sh --purge-secrets` deletes it. **Left unset, the CP
  still starts, and members' stores are written in plaintext.** See
  `deploy/compose/README.md`.

- **Backups are sensitive.** Members' homes (with the encrypted store `secrets.enc`)
  and the agent CLIs' sign-in state, which is plaintext, are in the archive
  `deploy/compose/backup.sh` writes and, on AWS, in the file-system recovery points
  (`Persistence=retain`) and the `ecs-ec2` home-volume snapshots the CP takes when it
  hibernates or backs up a home. Protect where they are stored.

- **Anything running in a Workspace can read that user's own secrets.** Agents,
  their shells and every process they start (build scripts, package install hooks,
  MCP servers) run as the same uid as the Workspace Agent. They can read the
  per-workspace DEK (`AF_SECRET_KEY`) and the CP↔Agent `AGENT_TOKEN` — from their
  own environment, the Agent's `/proc/<pid>/environ` and the tmux global
  environment — and can obtain plaintext git tokens through the credential helper.
  The boundary is the workspace: on the multi-user targets none of this reaches other
  users or the CP. What a leaked DEK adds is the ability to decrypt a copy of that
  user's `secrets.enc` taken later (backups, volume snapshots); the DEK is not
  rotated. Treat a prompt injection or a malicious dependency inside a session as able
  to exfiltrate that user's connected credentials. See
  [07 §7.2](docs/build/07-security.md#72-isolation-controls).

- **Workspace egress is not fenced.** An egress proxy exists, but no deployment
  template routes a workspace's traffic through it, so a workload can send data
  anywhere its network reaches
  ([07 §7.8](docs/build/07-security.md),
  [#1181](https://github.com/k-k1/agent-fleet/issues/1181)).

### `docker` and `native`

- **`docker.sock` access = host access.** On `docker` the CP drives the host Docker
  daemon through the mounted `/var/run/docker.sock` (docker-out-of-docker), so anyone
  able to run the CP container or reach the socket can control the host. Restrict who can
  deploy and operate it.
  - Hardening option: front the socket with a filtering proxy
    (e.g. `tecnativa/docker-socket-proxy`) to narrow the Docker API surface.

- **Tenant slugs share a namespace with member homes.** A tenant slug
  that equals an existing member's key, or a name the CP uses under `WS_DATA`, places
  that tenant's homes inside another directory
  ([#1214](https://github.com/k-k1/agent-fleet/issues/1214)). Tenants are created by an
  administrator; check the slug before creating one.

- **`native` is single-user only, and the user is the operator.** There is no
  container: the workspace is a bubblewrap sandbox that shares the host's network, and
  the CP refuses any `AUTH` other than `dev`, which answers as `super_admin` without a
  credential. Code running in a session can therefore reach the CP on the host's
  loopback with the operator's rights. Run it only on a machine whose one user already owns it.

### `ecs` / `ecs-ec2` (AWS)

What follows is what the templates in `deploy/aws/ecs/cfn/` and the CP's AWS runtimes
declare.

- **The operator's AWS account is inside the trust boundary.** Each workspace's
  `AGENT_TOKEN` and DEK are SSM SecureStrings under `/af-ws/`, the CP's own secrets
  are under `<SsmPrefix>`, and the homes and agent state are on the deployment's own
  storage. Encryption at rest is on with the account's default keys (no KMS key is
  named), so it protects the media, not the data from the account's own principals.
  Whoever administers the account can read every member's data.

- **Give each deployment its own AWS account.** The CP task role (`CpTaskRole` in
  `20-platform.yaml`) is scoped to the account and region, not to one deployment:
  several of its statements name `Resource: "*"`, and the workspace parameters are one
  account-wide prefix. A compromised CP can therefore reach other deployments, and
  other SSM-managed instances, in the same account
  ([#1182](https://github.com/k-k1/agent-fleet/issues/1182)).

- **Members share the deployment's infrastructure.** All members' workspaces run in
  one VPC and one ECS cluster (on `ecs-ec2`, on a pool of slot instances that pass
  from one member to the next), and their data is in one file system and one
  database. What separates them is listed per target in
  [07 §7.2](docs/build/07-security.md#72-isolation-controls).

- **Every workload leaves through the CP's address.** Workspace tasks get no public
  IP; the private subnets route out through one NAT gateway, which the CP uses too. An
  outside service that trusts that address (a git host's IP allowlist, for example)
  trusts every member's workload, not just the CP.

- **Clean home and Recreate do not reach the member's home** on `ecs` / `ecs-ec2`
  ([#1225](https://github.com/k-k1/agent-fleet/issues/1225)). Do not rely on them to
  remove a member's data there.

## Supported versions

Only the latest release is supported. There are no maintenance branches: a fix ships
in the next release, which is cut from `main`
([CONTRIBUTING.md](CONTRIBUTING.md#commits--prs)), and earlier versions do not receive
backports. Please upgrade to the newest release before reporting.

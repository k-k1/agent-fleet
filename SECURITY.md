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
`ecs`, `ecs-ec2` or `kubernetes`) and the agent kind when it matters, a reproduction or
PoC, and the impact you observed. We aim to acknowledge within a few business days.

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

- **A compromise of the Control Plane or the host breaks isolation within that
  deployment**, because the CP can reach every workspace in it
  ([07 §7.1](docs/build/07-security.md#71-threat-model-and-trust-boundary) says how).
  It does **not** reach other companies as long as each runs its own deployment —
  on AWS, in its own AWS account (see below).

- **`AF_MASTER_KEY` is the root of the credential encryption.** With the default
  local custodian, every per-workspace DEK is derived from it and wrapped by a tenant
  KEK that is derived from it too, and the tenant secrets in the database are sealed
  with that KEK ([07 §7.6](docs/build/07-security.md#76-secrets-and-envelope-encryption)).
  **Losing it = crypto-shred: the CP can no longer unwrap any DEK or open the tenant
  secrets in the database**, backups included. A member's encrypted store then stays
  readable only through a copy of its DEK that still exists, such as the one a running
  workspace holds. The agent CLIs' own sign-in state is not sealed by it and stays
  readable.
  On `ecs` / `ecs-ec2` the operator can switch the CP to the KMS custodian
  (`AF_KEY_CUSTODIAN=kms`, off by default). What the CP seals after the switch then
  depends on the KMS key instead, but a member's DEK is still derived from the master
  key unless `AF_WORKSPACE_DEK=random` gives that home a KMS-sealed key of its own, so
  the master key stays required on every target
  ([deploy-targets ¹⁴](guide/ref/deploy-targets.md#capability-differences)).
  Store it in a **separate vault** from the database and the homes, and back it up
  independently: neither `deploy/compose/backup.sh` nor the AWS templates copy it. On
  AWS it is the SSM SecureString `<SsmPrefix>/master-key` (`/af-cp` by default), and
  `deploy/aws/ecs/teardown.sh --purge-secrets` deletes it. **Left unset with the local
  custodian, the CP still starts, and members' stores are written in plaintext**
  (`secrets.json` instead of `secrets.enc`); outside `AUTH=dev` it logs a warning and
  shows super_admins a banner. With `AF_KEY_CUSTODIAN=kms` the CP refuses to start
  without it. See `deploy/compose/README.md`.

- **Backups are sensitive.** Members' homes (with their store: `secrets.enc`, or the
  plaintext `secrets.json` when no master key is set) and the agent CLIs' sign-in
  state, which is plaintext, are in the archive `deploy/compose/backup.sh` writes and,
  on AWS, in the file system's daily AWS Backup recovery points (`Persistence=retain`)
  and the `ecs-ec2` home-volume snapshots the CP takes when it hibernates or backs up a
  home. The database — wrapped DEKs, tenant secrets sealed by the custodian — is in
  that archive when it is the default SQLite file under `DATA_DIR` (a database moved
  elsewhere with `AF_DB` or `AF_DATABASE_URL` needs its own backup), on AWS in its
  automated backups and final snapshot (`Persistence=retain`), and on `kubernetes` in
  the backups of the Postgres the deployment provides. On `kubernetes` the homes are not
  backed up in this version, and the CP's own disk (`af-cp-data`, with the git token
  key) is backed up only once the operator gives it a snapshot schedule, again after
  every restore
  ([deploy/kubernetes/README.md](deploy/kubernetes/README.md#backups-point-in-time-recovery-and-the-restore-rehearsal)).
  Protect where they are stored.

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
  ([07 §7.8](docs/build/07-security.md#78-egress-control-),
  [#1181](https://github.com/k-k1/agent-fleet/issues/1181)).

### `docker` and `native`

- **`docker.sock` access = host access.** On `docker` the CP drives the host Docker
  daemon through the mounted `/var/run/docker.sock` (docker-out-of-docker), so anyone
  able to run the CP container or reach the socket can control the host. Restrict who can
  deploy and operate it.
  - Hardening option: front the socket with a filtering proxy
    (e.g. `tecnativa/docker-socket-proxy`) to narrow the Docker API surface.

- **Data-root name collisions stored before 0.27.0 are not repaired.** A tenant slug and
  a default-tenant member's key are sibling directories under `WS_DATA`, next to the
  CP's own entries. New names that would collide are refused, but a collision stored
  earlier stays as it is: the CP logs `data root name collision` at start-up, and only
  an administrator can move it
  ([operate/02 §7](guide/operate/02-install.md#7-the-first-tenant-and-members)). Check
  the start-up log after an upgrade.

- **`native` is single-user only, and the user is the operator.** There is no
  container: the workspace is a bubblewrap sandbox that shares the host's network, and
  the CP refuses any `AUTH` other than `dev`, which answers as `super_admin` without a
  credential. Code running in a session can therefore reach the CP on the host's
  loopback with the operator's rights. Run it only on a machine whose one user
  already owns it.

### `ecs` / `ecs-ec2` (AWS)

What follows is what the templates in `deploy/aws/ecs/cfn/` and the CP's AWS runtimes
declare.

- **The operator's AWS account is inside the trust boundary.** Each workspace's
  `AGENT_TOKEN` and DEK are SSM SecureStrings under `/af-ws/`, the CP's application
  secrets are under `<SsmPrefix>`, the database password is an RDS-managed Secrets
  Manager secret, and the homes and agent state are on the deployment's own
  storage. Encryption at rest is on with the account's default keys (the templates
  name no KMS key for it), so it protects the media, not the data from the account's
  own principals. The optional custodian KMS key (`CustodianKmsKey` in
  `10-data.yaml`, or a key of the operator's own) seals the CP's secrets, not the
  storage. Whoever administers the account can read every member's data.

- **Give each deployment its own AWS account.** The CP task role (`CpTaskRole` in
  `20-platform.yaml`) is scoped to the account, not to one deployment: its ECS
  statements name `Resource: "*"` with no condition, and the workspace parameters are
  one prefix for the whole account. A compromised CP can therefore reach other
  deployments in the same account: their services and parameters. Its direct shell
  (`ssm:SendCommand`), its tag writes and its other EC2 writes (start, stop, terminate,
  attach, detach, snapshot, delete) are fenced to resources in its own pool, and it may
  pass only its own slot role
  ([#1182](https://github.com/k-k1/agent-fleet/issues/1182),
  [#1419](https://github.com/k-k1/agent-fleet/issues/1419),
  [#1423](https://github.com/k-k1/agent-fleet/issues/1423)). A launch may boot only from
  an Amazon-owned or public image or the slot AMIs its pool stack names, and is meant
  not to map a snapshot the account owns, so that another deployment's home snapshots
  cannot be booted and read ([#1522](https://github.com/k-k1/agent-fleet/issues/1522)).
  Three things stay open: snapshots shared in from other accounts can still be mapped;
  the snapshot fence is the negated form on `ec2:Owner`, so if AWS presented no owner
  for this account's snapshots it would be silently void; and an engine purchase's
  mapping override is fenced only if EC2 Fleet checks the caller's `RunInstances`.
  None of this has had its live check under the real role yet (the run is listed in
  [#1527](https://github.com/k-k1/agent-fleet/pull/1527)). Which statements there are,
  and what they reach, is in
  [07 §7.1](docs/build/07-security.md#71-threat-model-and-trust-boundary).

- **Members share the deployment's infrastructure.** All members' workspaces run in
  one VPC and one ECS cluster (on `ecs-ec2`, on a pool of slot instances that pass
  from one member to the next), and their data is in one database and one file system
  (on `ecs-ec2` each home is its own EBS volume; the file system keeps the agent
  state). What separates them is listed per target in
  [07 §7.2](docs/build/07-security.md#72-isolation-controls).

- **Workloads reach the internet from the CP's address.** Workspace tasks get no
  public IP; the private subnets send internet-bound traffic through one NAT gateway,
  which the CP uses too (S3 too: there is no S3 gateway endpoint, see ADR 0122). An outside
  service that trusts that address (a git host's IP allowlist, for example) trusts
  every member's workload, not just the CP.

- **Clean home does not remove all of a member's data.** It keeps the member's
  logins, connections and Claude state, which on `ecs` / `ecs-ec2` live on the file
  system; **Destroy workspace** removes those, and its outcome is written to the audit
  log once the removal task has finished. On `ecs-ec2` Clean home also keeps the backup
  copies of the home; deleting those is a separate action in the member's detail. With
  `Persistence=retain`, the file system's AWS Backup recovery points stay until their
  retention ends
  ([deploy-targets ⁷–⁹](guide/ref/deploy-targets.md#capability-differences)). To remove
  a departing member's data, account for all three.

### `kubernetes` (preview)

- **The isolation holds only on a cluster that meets the preconditions.** A
  NetworkPolicy-enforcing CNI, application-layer encryption of Secrets, a private or
  authorised-network control-plane endpoint, the kubelet's read-only port off, and the
  rest of the list in
  [deploy/kubernetes/README.md](deploy/kubernetes/README.md#preconditions). The CP cannot
  check any of them: a cluster that misses one runs, and is not isolated.

- **Pod creation in the workspace namespace is access to every member.** Each
  workspace's `AGENT_TOKEN`, DEK and minted tokens are in a Secret in that namespace, and
  a pod created there can mount any member's claim. Members must have no Kubernetes API
  access to it. A compromised CP reaches that namespace — every Secret, and pods it can
  create but not make privileged — and, through its read-only cluster role, nothing else
  ([07 §7.2](docs/build/07-security.md#72-isolation-controls)).

- **Node disk is shared.** A workspace's ephemeral-storage limit and its `/tmp` size
  limit are enforced by eviction after the fact, not as a quota at write time, so a
  workload that writes fast can fill a node's disk first and get its neighbours on that
  node evicted too. Keep disk headroom on the nodes and alert on disk pressure
  ([deploy/kubernetes/README.md](deploy/kubernetes/README.md#node-disk)).

## Supported versions

Only the latest release is supported. There are no maintenance branches: a fix ships
in the next release, which is cut from `main`
([CONTRIBUTING.md](CONTRIBUTING.md#commits--prs)), and earlier versions do not receive
backports. Please upgrade to the newest release before reporting.

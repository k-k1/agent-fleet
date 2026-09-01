---
audience: "someone adding a deployment target or an adapter"
source_of_truth: "the code plus each runbook (`deploy/*/README.md`)"
updated: "2026-07"
---

# 09. Deployment — the forms, the adapters, the environment index

English | [日本語](09-deploy.ja.md)

**The actual commands live in the runbooks and are not duplicated here.** This chapter
is the map: what forms exist, what gets swapped, and which knob controls it.

## 9.1 The deployment forms

| Form | Summary | State | Runbook |
|---|---|---|---|
| **local dev** | The CP as a host process. One entry script with subcommands, plus a light path that swaps only the CP | ✅ in use for development and small shared setups | the comments at the top of the scripts; the reflect-a-change table is [10](10-development.md) |
| **wsl (personal)** | A WSL2 preset of local dev. Where Docker cannot be installed, the containerless subcommand ([ref/deploy-targets](../../guide/ref/deploy-targets.md)) | ✅ personal use | [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| **compose** | The self-hosting mainline: a CP container plus Caddy for automatic TLS. The CP binds loopback, and **the compose definition contains the three constraints** of driving the host's Docker daemon from a container | ✅ | [deploy/compose/README.md](../../deploy/compose/README.md) |
| **aws** | Either the native ECS adapter, or compose on a single EC2 VM | 🚧 implemented, no production mileage | [ecs](../../deploy/aws/ecs/README.md) / [ec2-single](../../deploy/aws/ec2-single/README.md) |

- **ec2-single is compose on a VM** — a variant of the compose form, not a separate
  runtime.
- The authentication modes are [07 §7.3](07-security.md) and are not repeated here.

## 9.2 Ports and adapters — which knob swaps what

**The core is the same artefact on every target**; only interface seams inside the CP
change (the seam list is [01 §1.6](01-architecture.md)). This section is the knobs:

| Seam | Knob | Choices |
|---|---|---|
| `Runtime` / `RuntimeFactory` | `AF_RUNTIME` | empty, `local`, `docker` = Docker Engine (default) / `ecs`, `aws` = ECS 🚧 / `native`, `wsl` = containerless host processes, **which requires `AUTH=dev`**. **An unknown value fails fast at boot** |
| `Store` | the database variables | SQLite (default, pure Go) or Postgres |
| `KeyCustodian` | whether the master key is set | set = the local custodian; unset = no encryption, development only. KMS / Vault is 📋 ([decisions/0005](../decisions/0005-envelope-custodian.md)) |
| `AuthGateway` | `AUTH` | `dev` / `oauth` / `proxy` ([07 §7.3](07-security.md)) |
| Ingress / TLS | outside the CP | Caddy (compose) / Tailscale Funnel (local) / ALB + ACM (aws) |

## 9.3 Ingress, and the loopback invariant

**The invariant: the CP binds loopback, and anything public is always behind the
ingress.** The ingress terminates TLS and forwards; with `AUTH=oauth` the CP
authenticates for itself, and only with `AUTH=proxy` does the ingress inject the
identity header.

| Ingress | When | Notes |
|---|---|---|
| **Caddy** | the compose default | point DNS at it and certificates are obtained and renewed automatically, WebSockets included. A site with its own proxy can drop it |
| **Tailscale Funnel** | one local form | straight through to the CP's loopback port |
| **ALB + ACM** | aws 🚧 | TLS only — authentication stays native. Using the load balancer's own OIDC means `AUTH=proxy` |

**Whenever the ingress changes, the public base URL must change with it** — it is what
the OAuth redirect is built from, and the `https` prefix is what makes a Secure cookie
possible.

## 9.4 Environment variable index

**The values, how to generate them and the caveats live in the annotated example env
files** ([compose](../../deploy/compose/.env.example),
[local](../../deploy/local/oauth.env.example)). This is only an index.

| Group | Variables | Role | Detail |
|---|---|---|---|
| CP core | the bind address, the Console directory, the runtime, the database path, the public base URL | where it listens, what it serves, which adapter | this chapter |
| Workspace template | the image, the data root, the memory limit, the agent port base and host, the JVM directory, extra env, the session command | the common template the CP fills in when starting a container | [04](04-agent.md) |
| L1 auth | the mode, the dev user, the header name, the provider client ids and secrets, the provider list and its per-provider settings, the cookie secret and TTL, the allowlists | Console login. **Every list empty means fail-closed.** A provider that declares no trust mode is disabled, and **zero working providers is fatal** | [07 §7.3](07-security.md) |
| Provisioning and roles | the provisioning mode, the deployment-administrator list | how an unknown identity is admitted; who is an administrator | [06](06-data.md) |
| At-rest encryption | the master key | unset means plaintext (development only). **Losing it is a crypto-shred** — keep it in a vault separate from the data | [07 §7.6](07-security.md) |
| Git provider OAuth | **there are none** | A tenant administrator registers the apps in the Console. The old variables are no longer read, and the GitHub one is now for sign-in only | [decisions/0052](../decisions/0052-tenant-git-oauth.md) |
| Scale-to-zero and showback | autostart, the several idle timeouts, the sweep interval, the stop grace, the sampling interval | auto-start, idle stop, the grace period, usage sampling | [03](03-control-plane.md) |
| MCP | the enable flag | whether the endpoint exists at all | [08](08-integrations.md) |
| Egress 🚧 | the listen address, the token, the ingest and policy URLs, the proxy address, the enforce flag, the allowlist | the forward proxy and the CP's aggregation | [07 §7.8](07-security.md) |
| Postgres | a URL, or the individual parts, and **the ARN of the secret the password really lives in** | only when the store is Postgres. The parts are composed into a DSN; the ARN is what lets a rotated password be picked up without replacing the task (§9.9) | [06](06-data.md) |
| ECS adapter 🚧 | the cluster, region, subnets, security group, namespace, filesystem, roles, log group, task size, uid/gid, start timeout | handing the CP the coordinates of the static infrastructure the templates built | [ecs runbook](../../deploy/aws/ecs/README.md) |
| Containerless adapter 🚧 | the agent binary path | where the agent lives when there is no container | — |
| Inside the container (injected by the CP; **an operator never sets these**) | the agent address and token, the secret key, the stop grace, the session command, the config directory, the self-update permission, the docs token | CP ↔ agent authentication, the DEK, the grace period | [04](04-agent.md) / [07 §7.5](07-security.md) |

How to check this index is complete: **the variable names are their own grep anchors.**
Cross-check what the CP reads against the pass-through list in the start script and the
example env files.

**How a JDK is provided differs by runtime — never assume the system JVM directory
exists.** The bind-mount knob is **local-only**; on ECS there is no such mount, so that
directory can be empty. The runtime-independent answer is a directory on the home
volume, populated by an install subcommand. Choosing a Java version in the Console
installs any missing one and puts it on the path. **Choosing a version that is not
installed offers a button that installs it there and then** — previously nothing
happened until the next container start; now it needs neither a restart nor a terminal.
After installation the toolchain resolution globs the directory at every start, so it
takes effect **from the next session**.

## 9.5 The AWS target 🚧

**Implemented, with no production mileage** — proven in a sandbox from deploy through
end-to-end to teardown.

- The mapping: the runtime is ECS (one workspace = one service, desired 0/1 =
  scale-to-zero); the persistent home is an EFS access point with a fixed root and
  uid/gid; secrets are parameter-store entries, **so the DEK appears in the task
  definition only as a reference and never as plaintext**; the CP reaches the agent over
  service discovery.
- **The ownership boundary**: the CloudFormation templates build **static
  infrastructure once**. Per-workspace resources are **created at runtime by the CP
  under deterministic names** — the adapter is stateless and the templates never churn.
- The `starting` state in the Runtime contract is effectively ECS-only, because a cold
  image pull takes minutes. **While it is converging, callers neither re-start it nor
  idle-stop it.** The Docker adapter comes up in seconds and never reports it.
  - Reducing that latency — whether to adopt lazy image loading — concluded as a
    **conditional yes**: the prerequisites are met by the current setup unchanged, but
    **the ~100 s has never been broken down**, so the gate is to measure the pull
    separately first.
  - **The 504 on a first start had a different cause**: a synchronous wait longer than
    the load balancer's idle timeout. It is fixed independently — **start returns as
    soon as the desired count is set**, and waiting for the agent's health moved to a
    background goroutine. A synchronous wait cannot be reintroduced there, because by
    the time that code runs the task is always starting from nothing (there is no image
    cache). Convergence is observed by the Console's own polling.
- The cost characteristics are §9.8.

## 9.6 Parity and differences

| Aspect | local / compose | aws (ECS) 🚧 |
|---|---|---|
| Workspace image and agent | the same artefact | the same artefact — this is the point of the split |
| Scale-to-zero | stop / start the container | desired 0/1. **The idle logic is common**; the Runtime absorbs the difference |
| Isolation strength | the container boundary, sharing a kernel | plus task isolation |
| Egress | the container network plus a host firewall (enforce is 🚧) | security groups and network ACLs |
| Storage performance | local disk, fast | **a network filesystem can be slow for git**, which is metadata-heavy |
| Infrastructure privilege | the Docker socket is host-root equivalent ([07 §7.1](07-security.md)) | metadata blocked, a minimal task role |

## 9.7 Backup, restore and upgrade — the assumptions

- **The data directory is everything you must preserve**: the database, every user's
  home including the encrypted store, the plaintext agent state, the wrapped DEKs, and
  the certificates. Only what can be re-provisioned is excluded.
- **The master key goes in neither the data directory nor the backup** — keep it
  separately. Losing it makes every backup undecryptable. Conversely, **the archive
  contains plaintext agent state, so the archive itself needs protecting.**
- A restore may land under a different parent path: the CP re-points at start, **but the
  basename is a contract**.
- **Upgrades apply migrations automatically at start and cannot be downgraded** — always
  back up first.
- The actual procedures are the [compose runbook](../../deploy/compose/README.md).

## 9.8 Cost characteristics

The two AWS forms bill in a different **shape**. A single VM is **near-flat regardless
of headcount**; ECS is **a standing floor plus headcount × hours**, where scale-to-zero
does the work. **The choice follows that shape**; the absolute numbers below only
support it.

> **Assumptions**: us-east-1 on-demand list price, 730 h/month, as of 2026-07. Tokyo and
> other regions run roughly **+10–30%**. No reserved or savings plans are applied.
> **The agent subscriptions are each user's own and are not included at all.**

### 9.8.1 A single VM — flat

| Item | Monthly | Note |
|---|---|---|
| The instance (2 vCPU / 8 GB) | $61 | smaller and larger sizes are offered by the template |
| Disk, 30 GB | $2 | everything you back up lives here |
| A static IP | $4 | public IPv4 is always billed now |
| DNS zone | $1 | not needed with a wildcard DNS service |
| **Total** | **≈ $67/month** | **it does not change as people are added** — until the RAM runs out |

**RAM is the limit, not CPU.** Subtract the CP, the proxy and the OS, and divide what is
left by the per-workspace memory limit: about **4–5 concurrent workspaces** on that
size. That is why the runbook tells you to lower the limit on a smaller instance.

Properties to watch:

- **A burstable instance family drops to its baseline** once the credits are gone.
  Sustained heavy builds want a fixed-performance family.
- **Scale-to-zero does not help.** Idle-stop only stops the workspace containers; the VM
  is still billed. **Cutting weekend cost means stopping the VM itself.**
- **It is a single point of failure**, and the only isolation is the container boundary.

### 9.8.2 ECS — a floor plus usage

**The standing floor, payable with zero workspaces running:**

| Item | Monthly | Note |
|---|---|---|
| NAT gateway | $33 | required for egress, and **endpoints cannot remove it** |
| Load balancer | $20 | plus usage |
| Database | $15 | why state survives a CP task replacement |
| Network filesystem, 50 GB | $15 | billed by usage |
| The CP task, 24/7 | $18 | |
| Registry, logs, storage, DNS | $5–10 | |
| **Floor** | **≈ $110/month** | |

**Per workspace**, at about **$0.049/hour**:

| Pattern | Per person | 20 people | With the floor |
|---|---|---|---|
| Weekdays, 8 h (assumes scale-to-zero) | $8.7 | $174 | **≈ $285/month** |
| 24/7 (idle stop not working) | $36 | $720 | ≈ $830/month |

**That second row is also the bill when scale-to-zero breaks.** Whether the idle
settings are actually working is therefore a **cost** concern as much as an operational
one.

### 9.8.3 Choosing between them

| People | Single VM | ECS (weekdays, 8 h) | Verdict |
|---|---|---|---|
| up to 5 | **$67** | $154 | **the VM, no contest** — ECS cannot earn back its floor |
| up to 15 | $283 | $240 | roughly even; decide on operational effort and isolation |
| 20 | $283–368 | **$285** | **about the same** — the crossover, past which non-cost factors dominate |
| 20, 24/7 | $283 | $830 | consolidating onto a VM wins by a mile |

**There is almost no case where ECS wins on cost.** You choose it for task-level
isolation, per-user fault isolation, rolling image replacement, and a tighter metadata
and role posture (§9.6). **Scale-to-zero is not what makes it cheap — it is what dilutes
the premium down to "about the same as a VM, at twenty people".**

- **Small, single team → the VM** (compose on AWS).
- **Isolation or per-user availability requirements → ECS**, accepting the floor and the
  fact that idle-stop must actually work.
- Both are still 🚧 with no production mileage. **Check the real numbers against the
  first month's actual bill.**

## 9.9 Health, readiness, and a credential that moves

**`/healthz` is liveness only.** It writes the literal `ok` and touches nothing else —
no database, no filesystem, no adapter. `deploy/local/restart-cp.sh` compares its body
to that string verbatim, and the ALB target group health-checks it. Treat it as a frozen
contract.

**`/readyz` is the one that consults the store.** It pings the metadata store with a two
second budget and answers `503 database unavailable` when it cannot. It is reachable
without a session, because a monitor cannot sign in, and the body deliberately carries
nothing an unauthenticated caller should not see.

**The ALB stays on `/healthz` on purpose.** Pointing it at `/readyz` would let a
momentary database unavailability kill the CP task, and the CP runs at `desiredCount 1`
— a permanent restart risk in exchange for a self-heal the CP now performs by itself
([decisions/0065](../decisions/0065-db-credential-rotation.md)).

### Why the database password cannot be an environment variable on RDS

An ECS task definition's `secrets` block is resolved **once, when the task starts**. RDS
rotates its managed master password on a schedule (seven days by default), so a
long-running task ends up presenting a password the database has stopped accepting, and
every query fails with `28P01`. **Nothing about this is visible from outside**: the
process is up, so `/healthz` is `ok`, so the target is healthy, so the service is at
steady state.

So the CP treats the injected value as a bootstrap hint and re-reads
`AF_DB_PASSWORD_SECRET_ARN` from Secrets Manager when Postgres refuses it, retrying
inside the connector. Two things have to be true for that to work:

1. **`CpTaskRole` — not the execution role — needs `secretsmanager:GetSecretValue`.** The
   execution role's copy is what injects the variable at start and does nothing
   afterwards. When the task role lacks it, the CP keeps running on the injected value
   and logs `DB_SECRET_REFRESH_FAILED`; the mechanism is gone but nothing breaks until
   the next rotation.
2. **`AF_DB_PASSWORD_SECRET_ARN` has to be set.** Unset means the injected value is all
   there is — correct for compose, on-prem and SQLite, and a latent outage on RDS.

### Making it audible

The CP logs `DB_UNAVAILABLE` when it cannot open a connection. `30-ingress.yaml` turns
that into the CloudWatch metric `AgentFleet/<stack>/DbUnavailable` **unconditionally**,
and `CpAlarmEmail` — empty by default — subscribes an address to an alarm on it.

**Set it.** On 2026-09-01 the only record that a production deployment was returning 500
to every caller was a line in a log group nobody had reason to open. Recovery of last
resort, four minutes and no interruption because it is blue/green:

```sh
aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
```

---
audience: "a deployment administrator working out what is wrong with a deployment (works on the host)"
source_of_truth: "the commands are the runbooks and scripts under `deploy/` (where they disagree, this page is wrong)"
---

# Find the cause from the symptom

English | [日本語](checklist.ja.md)

When "it is up but does not work" or "I can't use it", narrow the cause down from the symptom.
The commands that fix it are on each target's procedure pages (for compose, [Deploy with Docker Compose](../compose/install.md)).

## How it works

| What you do | What is going on |
|---|---|
| Look at the Control Plane's log and health | The reason for a failed start or a refused sign-in is usually in the log |
| Tell one person's problem from everybody's | Everybody points at the deployment; one person points at their workspace or account |
| Narrow it down with the symptom table, and fix it with the target's procedure | |

The commands are the compose ones, run from `deploy/compose/`.

## The first 3 places to look

| Where | Command |
|---|---|
| The Control Plane's log | `docker compose logs -f cp`. Failed starts and refused sign-ins nearly always say why here |
| Health | Does `curl -s http://127.0.0.1:8099/healthz` print `ok`? |
| Readiness | Does `curl -s http://127.0.0.1:8099/readyz` print `ok`? |

> [!WARNING]
> **`ok` from `/healthz` does not mean the product works.** It reports only that the process runs; it does not look at the database.
> One deployment answered every API call with 500 while `/healthz` said `ok` and the load balancer target stayed healthy throughout.
> `/readyz` is the one that looks at the database. When people say it does not work and `/healthz` says `ok`, believe the people.

## Look up the symptom

| Symptom | What to check |
|---|---|
| The Control Plane does not start | `docker compose logs cp`; does `/healthz` print `ok`? |
| Everything returns 500 but health says `ok` | If `/readyz` returns `503`, it is the database. With a managed database whose password rotates, a Control Plane started before the rotation may still hold the old password. Search the log for `DB_UNAVAILABLE` and `DB_SECRET_REFRESH_FAILED`, and recreate the Control Plane's task so it rereads the password |
| "permission denied" on docker.sock | Does `DOCKER_GID` match the host's docker group GID? ([constraint C](#three-constraints-of-driving-docker-from-a-container)) |
| A workspace starts but its home is empty | Is `DATA_DIR` the same absolute path inside and outside the Control Plane, and the same path on restore? ([constraint B](#three-constraints-of-driving-docker-from-a-container)) |
| A started workspace cannot be reached | Are the Control Plane and Caddy both `network_mode: host`? ([constraint A](#three-constraints-of-driving-docker-from-a-container)) |
| Workspaces cannot reach the Control Plane URL that the host can | See [Workspaces cannot reach the public URL](#workspaces-cannot-reach-the-public-url) below |
| The TLS certificate is not issued | See [The TLS certificate is not issued](#the-tls-certificate-is-not-issued) below |
| Cannot sign in, a button is missing, a removed person still gets in | [Cannot log in](cannot-login.md) |
| Exits at startup with `AF_WORKSPACE_DEK=random needs AF_KEY_CUSTODIAN=kms` | A key per home needs the KMS custodian. Set `AF_KEY_CUSTODIAN=kms` (and `AF_KMS_KEY_ID`), or unset `AF_WORKSPACE_DEK` ([Give each home its own key](../keys/home-keys.md)) |
| A workspace start fails with `open the home's credential-store key` | KMS is unreachable, or the key is disabled or refusing. There is no fallback to the master key. Fix access to KMS, then start it again ([Give each home its own key](../keys/home-keys.md)) |
| After a restore, a workspace refuses to write credentials and the log says its credential store is `unreadable` | The home was restored from before the store moved to the home's key. Run `af-cp home-dek-status --remigrate <membership-id>` where the Control Plane runs, then restart the workspace ([Give each home its own key](../keys/home-keys.md#restoring-a-copy-from-before-the-move)) |
| The workspace log has `[entrypoint] WARN: npm boot-install failed (retrying next start)` | Installing a CLI failed (a full home, the network, a checksum). It retries at the next start, and the member's roster card says "A CLI failed to install at the last start" ([Check on your members](../../admin/members/roster.md#act-on-cards-that-need-attention)) |

## Three constraints of driving Docker from a container

The Control Plane is a container that drives the host's Docker from outside. That arrangement has three constraints that, when broken, **fail silently instead of with an error**.
The compose definition already keeps them; read this when you change the compose file yourself, or to guess a cause from a symptom.
On a Mac, A is handled differently ([Run on an Apple Silicon Mac](../compose/macos.md#what-the-overlay-changes)).

| Constraint | What to keep | Symptom when broken |
|---|---|---|
| A. Host networking | The Control Plane and Caddy both `network_mode: host`, because workspaces are published on the host's `127.0.0.1:<port>` | A started workspace cannot be reached from the browser |
| B. The absolute path of `DATA_DIR` | Mount `DATA_DIR` so it is the same absolute path inside the Control Plane, because workspace `-v` mounts are built from host paths | A workspace starts but its home is empty and the work is missing |
| C. The user and the docker group | `user: "1000:1000"` and `group_add: <DOCKER_GID>`, because homes are owned by uid 1000 and the Control Plane needs the docker socket | permission denied on the socket, or workspaces fail to start |

If B's symptom shows after a restore, check that the restored `DATA_DIR` does not differ from the original in its path (at least in its last directory name) ([Back up and restore](../run/backup-restore.md#restore)).

## Workspaces cannot reach the public URL

The host gets through, but from a workspace a `curl` to `PUBLIC_BASE_URL` is refused or reset. Fetching docs, branch name rules, the MCP registry
and git through the Control Plane all fail at once.

Whatever serves the public URL (a tunnel or reverse proxy) no longer accepts connections from the Docker bridges (it was rebound to one interface, say).
Set up the workspace-only listener (`AF_CP_INTERNAL_LISTEN` on the docker0 address, `AF_CP_INTERNAL_URL=http://host.docker.internal:<port>`)
and stop and start each workspace. The steps are in [When workspaces cannot reach the CP](../compose/workspace-listener.md).

## The TLS certificate is not issued

When Caddy cannot get a certificate from Let's Encrypt, it is one of these:

- The DNS A or AAAA record does not point at this host
- Ports 80 and 443 cannot be reached from outside (a firewall)
- Let's Encrypt's rate limit was hit

On a closed network with no public DNS, skip ACME and switch to `tls internal` (self-signed) ([Choose the tenant and provisioning model](../install-decisions.md#whether-to-use-tls-internal)).

## Check that no address is attached to the wrong person

`af-cp identity-email-check` is a read-only report that lists **every** person with an active membership in more than one tenant, with user key, email address, tenants and flags.
It opens the metadata store the same way `af-cp rewrap-keys` does.

| Flag | Meaning |
|---|---|
| `no_login` | No sign-in method was ever attached; the address did not come from the person's own sign-in |
| `email_audited` | There is an `identity.email_set` or `identity.email_change` audit record |
| `key_not_derived_from_email` | The user key is not of the shape that address would produce: how an address attached to somebody else's invitation looks |

Exit status 0 means no flags, exit status 1 means someone is flagged, and exit status 2 means the store could not be read.
A flag is a lead, not a verdict. Check with the people involved before you change anything.

## Triage a report

When somebody says "it doesn't work", first decide whether **it is that one person or the whole deployment**.

1. **Are other people having trouble at the same time?**
   - Yes: suspect the deployment. Look at the Control Plane's log and health, TLS, the sign-in allowlist and the host's load (memory). If nobody can sign in, it is the allowlist or the IdP settings; if nobody can connect, it is the front door (Caddy, TLS) or constraint A
   - No: suspect that person. Go on
2. **Narrow down one person's problem**
   - Cannot sign in: are they on the allowlist, or with `AF_PROVISION=invite`, have they been added to the roster?
   - Signed in but the workspace misbehaves: look at their workspace (`af-ws-<user>`). An empty home is constraint B (but then it would hit everybody). An agent that will not connect is their own agent login, which they redo themselves from the Console
   - How to use the Console: that is [Find what you want to do](../../user/README.md)
3. If you still cannot tell, look in the Control Plane's log for what happens to that person's address (the log shows it as the sanitised `user_key`)

## Frequently asked questions

### What happens if AF_MASTER_KEY is lost?

Every stored credential and **every past backup become permanently undecryptable**. There is no way back.
Keep it in a vault separate from the data, and back it up separately ([Understand the threat model and residual risks](../secure/threat-model.md#prepare-for-the-remaining-risks)).

### What does a backup hold?

The database, every user's home, the plaintext Claude state and Caddy's certificates. Not `shared/jvm` (it can be fetched again) and not `AF_MASTER_KEY` ([Back up and restore](../run/backup-restore.md#what-the-archive-holds)).

### Can it run on a network with no internet?

Yes. Mirror the GHCR images into an internal registry and point `REGISTRY` at it, or carry them in with `release.sh --save` and `load-images.sh`.
TLS uses `tls internal`, and Claude uses the copy in the image with `CLAUDE_INSTALL=0` ([Install into a closed network](../run/air-gapped.md)).
The agents still need to reach their model endpoints to work.

### I want to downgrade

Not supported. Migrations are forward-compatible and applied automatically, and an older Control Plane cannot read the newer schema.
To go back, restore the backup taken before the upgrade rather than switching to an older image ([Upgrade](../run/upgrade.md)).

### Workspaces are still there after docker compose down

That is normal. Workspaces (`af-ws-*`) are not managed by compose; the Control Plane started them with `docker run`.
Stop them with the force stop in the admin panel, or when taking the whole host down, `docker stop` the remaining `af-ws-*` separately ([Use idle stop and force-stop](../run/idle-stop.md#force-stop-a-workspace)).

### Can it be spread across several hosts (HA, horizontal scaling)?

The model is one deployment per company. With compose the Control Plane drives one host's Docker, so workspaces are not spread across hosts.
On ecs and ecs-ec2, workspaces run on AWS ([Choose where to deploy](../choose.md)). The Control Plane runs as one instance on every target; there is no HA setup.

### I want to sign in with something other than Google (Microsoft 365, LDAP, SAML…)

Microsoft Entra ID, Okta, Keycloak, Auth0, Cognito and GitLab work with settings alone ([Set up how people sign in](../signin/overview.md)).
The Control Plane does not speak SAML-only IdPs or LDAP. Put a gateway in front (oauth2-proxy, Keycloak, ALB OIDC) and have `AUTH=proxy` trust the email header that gateway sets.

## Related

- [Cannot log in](cannot-login.md)
- [Operating a deployment](../README.md)
- [Roles — who may do what](../../ref/roles.md)

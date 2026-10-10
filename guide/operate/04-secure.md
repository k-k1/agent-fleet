---
audience: "someone responsible for the deployment's security posture"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
updated: "2026-09"
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
AWS that key can be held by KMS instead (next section). Details in `docs/build/07-security.md`
§7.6.

### Keys at rest on AWS KMS

On `ecs` / `ecs-ec2` the Control Plane can have AWS KMS hold the keys for what it seals: MCP
connection headers, sign-in client secrets, engine tokens, session handoffs and shares, and the
key of each workspace's credential store. Each value is sealed with a fresh data key from KMS,
bound to its tenant, so it cannot be opened under another tenant's name. Turning it on:

1. Deploy `10-data` with `CustodianKmsKey=create` (or bring a symmetric KMS key of your own in
   the same account), and read the `CustodianKmsKeyArn` output.
2. Deploy `30-ingress` with `CustodianKmsKeyArn=<that ARN>`. The Control Plane gets
   `AF_KEY_CUSTODIAN=kms` and `AF_KMS_KEY_ID`, and its task role gets `kms:GenerateDataKey` and
   `kms:Decrypt` on that key only, and only with the Control Plane's encryption context.

What to know before you switch:

- **`AF_MASTER_KEY` stays required.** It still opens everything stored before the switch, and
  other keys are derived from it. Keep it exactly as before.
- **Nothing is re-encrypted on its own.** Values stored before the switch stay readable and stay
  protected by the master key alone; only what is stored afterwards is protected by KMS, until
  you run `rewrap-keys` ([below](#re-sealing-values-stored-before-the-switch)).
- **Members' stored credentials are not shredded by KMS.** Only a workspace whose key is stored
  for the first time after the switch has it wrapped by KMS; a workspace that already had one
  keeps the master-key wrapping, and restarting it changes nothing. Either way the key itself is
  still derived from `AF_MASTER_KEY` and the member, so that stores written before keep opening,
  and anyone with the master key can still derive it. Protect the master key as before. A key
  of its own for each home takes the master key out of that path, though not every copy of
  the key ([below](#a-key-of-its-own-for-each-home)).
- **No fallback.** If KMS cannot be reached or refuses, sealing and opening fail with an error
  that names KMS. The Control Plane never quietly uses the master key for a value KMS sealed.
- **Opened keys are cached in memory for 5 minutes** (`AF_KMS_DATA_KEY_CACHE_TTL`, `0` turns it
  off), so disabling the key takes effect within that time, not at once.
- **Disabling or scheduling deletion of the KMS key makes everything sealed after the switch
  unreadable to the Control Plane**, for every tenant at once. That is the crypto-shred lever, so restrict who can
  administer the key. To cut off one tenant, add a statement to the key policy that denies
  `kms:Decrypt` when `kms:EncryptionContext:af:key_ref` is that tenant's id; that is a
  revocation an administrator of the key can undo, not a shred.
- **Do not switch back to `local`** while values sealed by KMS exist: the local custodian refuses
  them with an error that says so.

### Re-sealing values stored before the switch

`af-cp rewrap-keys` re-seals, under KMS, every value the Control Plane sealed with the master
key before the switch: the wrapped key of each workspace's credential store, MCP connection
headers, sign-in and Git OAuth client secrets, session handoffs and share proposals, and the
Hugging Face, Civitai and ComfyUI keys of the engines. Afterwards disabling the KMS key makes
those unreadable too.

- Run it **after** the Control Plane itself runs with `AF_KEY_CUSTODIAN=kms`, with the Control
  Plane's own environment: the same `AF_MASTER_KEY`, `AF_KMS_KEY_ID`, database settings and
  task role. It refuses to run under `local`. A Control Plane still on `local` cannot open what
  the command writes.
- Start with `--dry-run`: it prints, for each place, how many values are already on KMS, how
  many are in the old format, and how many are stored unsealed (written by a Control Plane that
  had no master key; the command leaves those alone). It changes nothing and does not call KMS.
- Without `--dry-run` each value is opened with the master key, sealed by KMS, opened again
  through KMS to check it reads back, and only then written, one row at a time and only if the
  row has not changed meanwhile. Interrupting it, a KMS error or a refused `Decrypt` stops it
  before the row it was on is written: every row is left either in the old format or on KMS,
  and both open. After the rewrite it looks at every place again without changing anything,
  and the exit code comes from that final check. Run it again until it exits `0`; a run with
  nothing left to do changes nothing.
- **Run it while no administrator is editing.** The Control Plane can keep running, but saving
  a sign-in provider or Git OAuth app without retyping its secret, or the ComfyUI panel without
  retyping its key, writes back the stored value as it was read; a save that read the old value
  before the command rewrote it puts the old value back. The final check catches one that lands
  during the run; one that lands after it does not. Confirm afterwards with `--dry-run`.
- Exit `0` (with or without `--dry-run`): when the command last looked, nothing was in the old
  format and every row could be read. `1`: something is still in the old format or could not
  be read or written, or the run stopped; the log names the place and the row id, never a
  value. A `--dry-run` therefore exits `1` before the rewrap and `0` after it. `2`:
  configuration (not `kms`, no master key, no key id, no database).
- On `ecs` / `ecs-ec2`, run it as a one-off task of the Control Plane's task definition with
  the command overridden, in the Control Plane's subnets and security group; the output is in
  the Control Plane's log group:

  ```bash
  aws ecs run-task --cluster <cluster> --launch-type FARGATE \
    --task-definition <the Control Plane task definition> \
    --network-configuration 'awsvpcConfiguration={subnets=[<private subnet>],securityGroups=[<Control Plane security group>]}' \
    --overrides '{"containerOverrides":[{"name":"cp","command":["rewrap-keys","--dry-run"]}]}'
  ```

**`AF_MASTER_KEY` still cannot be dropped afterwards**, and must not change:

- The key of each workspace's credential store is still derived from it and the member
  (`HMAC(master, userKey)`). The command moves the *wrapped copy* of that key to KMS, so with
  the KMS key disabled the Control Plane can no longer start the workspace with it, but anyone
  holding the master key can still derive it and open `secrets.enc`. Members' stored
  credentials are therefore still not crypto-shredded by KMS. A key of its own for each home
  changes that only in part ([next section](#a-key-of-its-own-for-each-home)).
- A workspace started for the first time still gets its key from the same derivation.
- The tokens that workspaces use to call back into the Control Plane (Git credentials, Git
  OAuth, memos, schedules, engines, branch rules, AWS and Google Cloud profiles, documents, MCP)
  are signed with keys derived from it.
- The Control Plane refuses to start with `AF_KEY_CUSTODIAN=kms` and no `AF_MASTER_KEY`.

What the command does change: the master key alone no longer opens the values it re-sealed,
apart from the credential-store keys, which it can still derive.

### A key of its own for each home

With `AF_WORKSPACE_DEK=random` the Control Plane gives each member's home a random key for
its credential store (`secrets.enc`), sealed by KMS. It is accepted only with
`AF_KEY_CUSTODIAN=kms`; any other combination, or another value, stops the Control Plane at
boot. The `ecs` / `ecs-ec2` stacks do not expose it as a parameter yet.

- **The key belongs to the home, not the workspace.** Deleting and re-creating a workspace over
  a kept home keeps the key. Nothing removes it yet, Destroy included: not every runtime can
  prove the whole home is gone, and a key dropped while part of it survives makes that part
  unreadable.
- **Moving a store to it.** The first start after you turn it on mints the home's key, and the
  Control Plane passes it to the workspace beside the derived key. At boot the workspace opens
  `secrets.enc` with either one and re-seals it under the home's key. A store that neither key
  opens is not touched. The workspace's `/healthz` reports the outcome as `secrets_key`
  (`none`, `current`, `migrated`, `derived` when the re-seal failed, `unreadable`) and
  `secrets_key_next` (whether it sealed under the home's key), never a key.
- **Confirming it.** After such a start the Control Plane waits for that report, and only for
  the one from the workspace that start launched: each start passes a fresh identifier
  (`AF_HOME_KEY_START`, not a secret) that the workspace echoes as `secrets_key_start`, so an
  answer from an earlier task or another workspace is ignored, and a redirect is not followed.
  When the workspace sealed under the home's key and reports `migrated`, `current` or `none`
  (`none` only when it could look and found no store), the
  Control Plane marks the home confirmed, and from the **next** start the workspace gets the
  home's key alone (as `AF_SECRET_KEY`) and no derived key; on `ecs` / `ecs-ec2` that start also
  deletes the `secret-key-next` parameter. A workspace that is already running keeps both keys
  in its environment until it is restarted. `unreadable` or `derived` leaves the home
  migrating, is logged, and deletes nothing. If the Control Plane restarts while it waits, the
  home simply stays migrating and is confirmed at a later start. `af-cp home-dek-status` counts
  migrating and confirmed homes.
- **What disabling the KMS key does, and what it does not.** Once a home's store has been
  re-sealed, the key derived from `AF_MASTER_KEY` no longer opens it, and once the Control
  Plane's data-key cache has expired (`AF_KMS_DATA_KEY_CACHE_TTL`, 5 minutes by default) the
  Control Plane can no longer unwrap the home's key, so it refuses the next start. Within that
  TTL a start can still succeed. **That alone is not crypto-shredding**: copies of the home's
  key already handed out still open the store, each protected by something other than the
  custodian key:
  - a **running workspace** keeps it in its environment, and its Agent, git helper and MCP
    servers go on reading and writing the store without asking KMS;
  - the **runtime's own copy** used to start it, which outlives a stop: the container's
    environment on docker, the Kubernetes Secret, and on `ecs` / `ecs-ec2` the per-workspace
    SSM SecureString parameters (`secret-key`, `secret-key-next`), encrypted with the
    account's SSM key.

  To shred a home's store while keeping the home, disable the key **and** stop the workspace
  **and** remove those runtime copies yourself. Nothing here does that automatically. Destroy
  is not that procedure: it removes the runtime copies and tries to remove the home as well,
  and on an `ecs` stack without the home task it removes only the access points and leaves
  the home's EFS directories behind.
- **What it does not.** A home that has not been started since you turned it on is still on the
  derived key, and is **not** shredded by disabling the KMS key. `af-cp home-dek-status` counts
  them (read-only). Copies of a home made before its store was re-sealed (ecs-ec2 snapshots,
  backups) still hold the old file, which the master key can open. Until a home is confirmed,
  the derived key is still passed beside the home's key; it no longer opens a re-sealed store.
- **A copy restored from before the move.** On a confirmed home, a `secrets.enc` restored from
  a snapshot or backup taken before its store moved is sealed under the derived key, which
  that home no longer receives: the workspace reports `unreadable` and refuses to write over
  it. Run `af-cp home-dek-status --remigrate <membership-id>` with the Control Plane's
  environment, then restart the workspace: it gets both keys again and re-seals the restored
  store.
- **Turning it off does not undo it.** With `AF_WORKSPACE_DEK` unset again, homes that already
  have a key keep getting it, because their store may be sealed under it. After a store has
  been re-sealed, two kinds of going back differ:
  - **A Control Plane that does not know the home's key, or a lost `home_dek` table**, with the
    workspace image of this version: the store no longer opens, and the workspace refuses
    every write to it, reconnecting a credential included (this version never writes over a
    store it cannot open). A Control Plane that knows the home's key but not the confirm step
    is fine: it hands out both keys again, and the workspace opens the store with the home's.
  - **A workspace image from before this version: do not start one on a home that has
    moved.** Its Agent has no such guard: a save after a failed read can write an empty
    store over the re-sealed one, and the credentials are lost.

  To recover, first restore what opens the store, before any workspace starts on that home:
  the Control Plane version, its `home_dek` rows and the KMS key. Only if the stored
  credentials are to be given up, stop the workspace, move the unreadable `secrets.enc` out of
  the member's agent configuration directory (`~/.config/agent-fleet/`) yourself, and have the
  member reconnect. A workspace image older than the Control Plane loses nothing as long as
  the store has not been re-sealed yet: it ignores the home's key and keeps using the derived
  one.
- **No fallback.** If KMS cannot seal or open a home's key, the workspace does not start. It is
  never started on the derived key alone.

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
   approval)" in the Admin panel).
3. **Switch to enforce**: once the allowlist is sufficiently solid, switch the mode to
   enforce. From then on, traffic outside the allowlist is **blocked**. The Admin UI also warns
   you to confirm reality in log-only first before switching.

> Current implementation scope: **observation (log-only) and allowlist management work, and the
> proxy itself can block (enforce)**. On the compose (Docker) target, setting `AF_EGRESS_PROXY_ADDR`
> on the Control Plane injects `http_proxy` / `https_proxy` / `no_proxy` (and their upper-case forms)
> into every workspace container (off by default), so programs that honour those variables go through
> the proxy. The ecs / ecs-ec2 targets do not pass this setting on to workspaces. What is **not built
> yet** is forcing
> traffic through it (an internal network or security-group egress rule that leaves the proxy as
> the only way out, and templates that run the proxy), so a process that ignores the variables
> still goes straight out, and **switching to enforce does not yet constrain a workspace**
> ([#1181](https://github.com/k-k1/agent-fleet/issues/1181)).
> For now, understand that you can operate up to the "observe and grow the allowlist" stage.
> The full design picture is in `docs/build/07-security.md` §7.8.

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
  approval)"; see the procedure above.
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
  every workspace with `AWS_EC2_METADATA_DISABLED=true` (docker) or withholds the task role
  and sets it (ECS), which stops the SDKs from asking; the network block is the host's job:
  - **EC2 host for compose** (`deploy/aws/ec2-single`): IMDSv2 with hop limit 1
    (`HttpTokens: required`, `HttpPutResponseHopLimit: 1`). The host-network Control Plane is
    one hop and can still use an instance profile; a workspace on a docker bridge is two and
    gets no token. For an instance that already exists, apply it with
    `aws ec2 modify-instance-metadata-options --instance-id <id> --http-tokens required
    --http-put-response-hop-limit 1`. A stack update can replace the instance when the
    Ubuntu AMI parameter has moved.
  - **Any other docker host in a cloud**: the same metadata settings, or a host firewall rule
    in Docker's `DOCKER-USER` chain that rejects `169.254.169.254` from the workspace bridges
    (it needs root on the host; compose itself needs nothing new).
  - **ecs-ec2**: the slot user data sets `ECS_AWSVPC_BLOCK_IMDS=true`; retained slots have to
    be replaced: reserve them in Settings → Admin → Slots ([03-run](03-run.md), "ecs-ec2:
    replacing slots after a launch template change").

  `AF_WS_WORKLOAD_AWS=1` on the Control Plane hands the ECS task role back to workspaces and
  stops suppressing the SDKs' metadata lookup. It removes none of the network protections above,
  so a docker workspace still cannot reach the host's instance profile; giving it one needs a
  credential path you permit separately. Keep the network protection as the default.

  **Rolling it out.** A running docker workspace keeps the environment it was created with:
  `docker compose up -d` and a Docker restart do not change it. After upgrading the Control
  Plane and the workspace image (or changing `AF_WS_WORKLOAD_AWS`), have every workspace
  **Stopped and Started** in the Console, which recreates its container, and apply the host
  metadata settings above. Check from a session shell, without printing the environment. With
  the opt-in off (the default):
  `echo ${AWS_EC2_METADATA_DISABLED:-unset}` prints `true`; `aws sts get-caller-identity`
  with no profile fails with "Unable to locate credentials"; and the IMDSv2 token request
  (`curl -s -o /dev/null -m 3 -w '%{http_code}' -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token`)
  does not print `200`. With `AF_WS_WORKLOAD_AWS=1` the first two are expected to differ (the
  variable is unset on docker, and ECS resolves the task role); the token request must still
  not print `200` wherever the host blocks metadata.

  A workspace on the native runtime runs directly on the member's machine and is left alone:
  an instance role there is that machine's own.
  Members run AWS commands as themselves with `af-aws-exec` ([member guide 10](../member/10-integrations.md)).

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
account you cannot reach), the only immediate switch is to change the cookie signing key:

```sh
openssl rand -base64 32          # generate a new value
# put it in AF_COOKIE_SECRET in .env / oauth.env / the SSM parameter, then restart the CP
docker compose up -d cp
```

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

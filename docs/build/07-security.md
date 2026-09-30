---
audience: "anyone touching authentication, crypto, isolation, audit or egress"
source_of_truth: "the code (this is the boundaries and the intent)"
updated: "2026-09"
---

# 07. Security — threat model, authentication, crypto, audit

English | [日本語](07-security.ja.md)

## 7.1 Threat model and trust boundary

Inside every workspace, a CLI agent **executes arbitrary code** — including with
permission prompts skipped. The boundaries are designed on the assumption that **a
user's session runs untrusted code**. What is protected is **other users' data, the CP
and host infrastructure, the secrets, and exfiltration**.

Skipping permission prompts has been **the default, but not a fixture, since 2026-08** —
a user can turn it off per kind and per session
([decisions/0056](../decisions/0056-tool-permission-choice.md); which kinds offer the
choice is [ref/agents](../../guide/ref/agents.md)). **The boundary design does not change
because of it**: only some kinds can turn it off, the mode can be changed back from
inside the TUI, and the CLI's own configuration path is not blocked. In other words,
**the permission prompt is a way to reduce accidents, not an isolation boundary. The
workspace boundary is still the only wall.**

```
low trust  ┌──────────────────────────────┐
           │ inside the workspace         │ ← arbitrary code execution is allowed here
           └──────────────┬───────────────┘
                          │ the isolation boundary
high trust ┌──────────────▼───────────────┐
           │ Control Plane / host / cloud │ ← must not be reachable from a workspace
           └──────────────────────────────┘
```

**The honest limit.** Within one deployment the CP holds the keys to every workspace,
so **if the CP or the host is compromised, the separation inside that deployment falls
at once**:

- On `docker` the CP holds the Docker socket, which is host-root equivalent.
- On `ecs` / `ecs-ec2` it holds a task role (`CpTaskRole` in
  `deploy/aws/ecs/cfn/20-platform.yaml`) that creates and deletes the workspace
  services, writes the SSM parameters under `/af-ws/` that carry every workspace's
  `AGENT_TOKEN` and DEK, attaches home volumes, runs shell commands on the slots over
  `ssm:SendCommand`, and (with the engines stack) buys GPU instances. `SendCommand` is
  limited to the `AWS-RunShellScript` document on instances tagged with this pool's
  `af-pool` and `af-role=slot`. While those tags stay correct, a CP bug that picks the
  wrong target cannot send a shell command to an instance outside that set, engine
  boxes included ([#1182](https://github.com/k-k1/agent-fleet/issues/1182)). That is
  all the fence does. `Ec2SlotPool` grants `ec2:CreateTags` on `Resource: "*"` with no
  condition, so a tagging bug or a compromised CP can retag any instance into the pool,
  and the same statement's instance and volume actions (stop, terminate, detach, …)
  have no fence at all.
- On every target it unwraps the DEKs and injects them in plaintext (§7.6).

It does not spread between companies, because those are separate deployments — which is
the strength of the delivery model
([decisions/0001](../decisions/0001-self-host-vs-saas.md)). **On `ecs` / `ecs-ec2` that
holds only when each deployment has its own AWS account.** `CpTaskRole` is scoped to the
account, not to the deployment: `EcsDrive`, `Ec2SlotPool` and `EcsContainerInstances`
name `Resource: "*"` with no condition, and `SsmWorkspaceParams` covers
`parameter/af-ws/*`, one prefix for the whole account with no deployment in the path.
A compromised CP can therefore update or delete another deployment's services, stop,
terminate or snapshot its instances and volumes, and read or overwrite its workspaces'
`AGENT_TOKEN` and DEK. Candidate mitigations: rootless Docker, a socket proxy, a
narrower CP role.

## 7.2 Isolation controls

What a workspace *is* on each target is [ref/deploy-targets](../../guide/ref/deploy-targets.md).
This table is what separates one member's workspace from another's, and from the CP.

| Concern | docker (the default) | ecs (Fargate) | ecs-ec2 (production) |
|---|---|---|---|
| Files between users | the member's home, bind-mounted from `<WS_DATA>/[<slug>/]<key>/home`. **No other user's home is mounted at all** | EFS access points per membership (`/home/<membership>`, `/claude-config/<membership>`) fixing the root directory; one uid/gid for everyone (`AF_ECS_POSIX_UID` / `_GID`) | the member's own EBS volume, attached to a slot that serves **one member at a time** ([decisions/0045](../decisions/0045-ec2-persistent-workspace.md) decision 8) and mounted by the CP over SSM. The Claude state and the kept dotfiles stay on EFS access points |
| Process and memory | one membership, one container: `--memory` (`WS_MEMORY`, default `1g`, overridable per workspace), `--cpus` when set | one task; Fargate shares no kernel between tasks | one task per slot, memory capped below the slot's size (`AF_ECS_EC2_HOST_RESERVE_MB`). `/tmp` is a tmpfs, because the slot's root volume outlives its previous member |
| Network | a network per workspace (`af-net-…`), so containers cannot reach each other; the agent is published on the host's loopback only | `awsvpc`: each task has its own ENI in the workspace security group, which admits the agent port from the CP's security group only — never from another workspace — and assigns no public IP. Outbound is open (§7.8) | the same as ecs. The slot's own security group has no ingress |
| Privileges | not privileged; runs as `dev`. **`SYS_ADMIN` is added to the bounding set** so Chromium's setuid sandbox can create namespaces; the image build fails if any other setuid/setgid binary remains, so `dev` itself holds no effective capability | no privileged mode, no added capabilities | the same as ecs |
| Cloud identity | none | the task role `WsTaskRole` has **no policy at all**; `AGENT_TOKEN` and the DEK arrive through the task definition's `secrets` (SSM, read by the execution role) | the same task role. The slot's instance role carries ECS registration, SSM management, image pull and logs only |
| Sensitive state | the agent's plaintext state is moved to a second mount (`CLAUDE_CONFIG_DIR=/var/lib/af/claude`) **outside the file browser's reach**; the encrypted store stays in the home behind the agent's denylist (`fsDeny`) | the same — the image and the agent are common | the same |

**`native` has none of this.** It runs the agent as sandboxed host processes with no
container boundary and no memory limit, so it is **single-user only**: the CP refuses to
start it under any `AUTH` other than `dev` (`AF_RUNTIME=native is single-user only`).

**The limit**: a shell inside the workspace runs as the same uid as the agent, so a
user's own bring-your-own tokens, `AF_SECRET_KEY` and `AGENT_TOKEN` cannot be made
invisible to that user's own sessions. That is not solvable in principle. Keeping them
out of the browser, encrypting them at rest and injecting them as environment variables
is judged good enough; what it means for code running in a session is stated in
[SECURITY.md](../../SECURITY.md).

### 7.2.1 Engines: reachability is the access control

The deployment's own model servers — llama.cpp, ComfyUI
([decisions/0071](../decisions/0071-self-hosted-inference-engines.md)) — are outside
every workspace. **A workspace reaches one only through the CP's gateway** —
`/engine/{key}/v1/*`, plus `GET /engine/{key}/props`, which the agent reads to learn
the context window the running engine started with (`engine_gateway.go`):

- llama-server has mutating endpoints, and ComfyUI has no authentication at all, so
  **who can reach the port is the access control**. On AWS the engine security group
  (`EngineSg` in `60-engines.yaml`) admits the engine port from the CP's security group
  only; a workspace is not on the list. llama-server additionally gets an API key from
  SSM (`LlmApiKeySsmParam`), which the CP adds upstream — a second lock, and deliberately
  not fatal when it cannot be read (`readEngineAPIKey`).
- The gateway is exempt from the login gate and authenticates on its own. The workspace
  holds a per-membership issuing token (`AF_ENGINE_ISSUE_TOKEN`, `afei_…`) and exchanges
  it at `POST /internal/engine/token` for a token (`afe_…`, valid for 30 days) bound to
  the membership, one engine key and, when the caller names one, one session — an lcpp
  session or a terminal opencode session. Where the caller names none (opencode's shared
  Managed daemon, image generation, the boot-time probes) it covers the whole workspace.
  Every call re-checks that the membership is still live and that the tenant may use that
  engine.
- An engine on `docker` / `native` is one the operator already runs on the network
  ([decisions/0076](../decisions/0076-external-image-engine-on-lan.md)); a bearer for it
  comes from `AF_ENGINE_API_KEY_<KEY>`, and whether a workspace could reach it directly is
  the operator's network. A member's own llama.cpp connection is dialled by the workspace
  itself and never passes the gateway.
- **The CP buys the GPU instances itself** on `ecs-ec2`
  ([decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.md)). That adds to the
  CP's role, and only through `CpIngestPolicy` in `60-engines.yaml`:
  `ec2:CreateFleet` / `DescribeFleets` / `DeleteFleets`, `iam:PassRole` for the engine
  instance role, the service-linked roles for Spot and EC2 Fleet, and, for model ingest,
  `ecs:RunTask` on the ingest task definition plus writes to the Hugging Face and Civitai
  token secrets. The engine instance role carries ECS registration and SSM management;
  the engine task role reads the models bucket.

VOICEVOX is not behind this gateway: the Console asks the CP (`/api/tts/*`, behind the
normal login), and the CP calls the engine, whose security group admits the CP only.

## 7.3 L1 Console authentication — three modes

Selected by `AUTH`. All three sanitise the resolved email into the identity key
(lower-cased, each run of other characters becomes `-`, at most 40 characters).

| Mode | How | For |
|---|---|---|
| `oauth` | **The CP is itself an OIDC client** (§7.3.1). It owns `/login`, `/login/{slug}` and `/oauth2/{login,callback,logout,link}`, and on success issues a signed cookie (HMAC-SHA256 with `AF_COOKIE_SECRET`, HttpOnly, SameSite=Lax, Secure when the public URL is HTTPS, `AF_SESSION_TTL`, default 168 h). What the compose and AWS templates set | self-hosting. Assumes HTTPS at the edge |
| `proxy` | Trust an upstream gateway's email header (`AUTH_EMAIL_HEADER`, default `X-Forwarded-Email`). **A missing header is a 401 — there is no fallback.** The header is not stripped, so the CP must be reachable only through that gateway | an existing gate such as oauth2-proxy. **This is also the answer for a SAML IdP**: bridge it ([decisions/0043](../decisions/0043-login-idp.md)). The AWS template does not offer it |
| `dev` | A fixed user (`DEV_USER`, default `dev`), who is a deployment administrator | local development only, and **the default when `AUTH` is unset**. `native` requires it |

**The auth gate**, in `oauth` mode:

- It inspects every request and **always deletes any inbound identity header** before
  injecting the verified one — so an edge that passes headers through cannot be used to
  impersonate.
- The exempt paths are declared in one place (`exemptExact` / `exemptPrefix` in
  `routes.go` and beside each route group): the login routes, the health and readiness
  checks, the brand assets and the web manifest (a monitor or an unauthenticated page
  cannot sign in); the surfaces with their own authentication — `/mcp` (a bearer PAT),
  `/git/` (basic auth with a git token), `/engine/` (§7.2.1) and `/internal/` (a
  per-purpose bearer token each: egress, memos, schedules, MCP servers, docs, AWS
  profiles, git OAuth refresh, engine tokens); and the legacy redirect.
- Three allowlist mechanisms compose: emails (`AF_OAUTH_ALLOWED_EMAILS`), domains
  (`AF_OAUTH_ALLOWED_DOMAINS`), and **a file (`AF_OAUTH_ALLOWED_EMAILS_FILE`) re-read on
  every check — so adding someone needs no restart**. A per-provider list
  (`AF_OIDC_<ID>_ALLOWED_{EMAILS,DOMAINS}`) *replaces* the deployment-wide one for that
  provider.
- **The entry decision is a union along the email axis**
  ([decisions/0043](../decisions/0043-login-idp.md)):

  ```
  ( provider list | deployment list )  ∪  ( tenant auto-join domains | holds a membership )
  ```

  The second half comes from the database (cached for 30 seconds, dropped on every
  membership or tenant write), so **an invited person gets in without being in the
  environment allowlist** — which lets an invitation-driven deployment keep the roster in
  one place. **Everything empty still means deny-all (fail-closed).** The union is taken
  **only within the email axis**: a different kind of check, such as GitHub organisation
  membership, stays an AND — otherwise merely holding a membership would bypass it.
- **The check runs on every request, not only at login.** Removing someone from the
  list, or deactivating their membership, locks them out on their next request rather
  than at cookie expiry. The cookie is stateless, so **there is no individual
  revocation**; the only way to cut every session at once is to rotate the cookie
  secret.
- **The tenant gate is not in the auth gate.** The gate does not know the tenant —
  that is resolved later — so a tenant rule there would have no tenant to be about. The
  tenant check happens during resolution (`resolveFull` / `resolveMembership` compare the
  tenant's allowed providers with the session's), and a mismatch returns
  **`provider_required`**, which leads the Console back to sign-in rather than ending at a
  403.
- **The deployment-administrator list (`SUPER_ADMIN_EMAILS`) is read once at boot and is
  the only truth.** Role hints only ever upgrade, so **demotion happens in a sweep at
  start** (`DemoteSuperAdmins`; an identity with no email cannot be named and is left
  alone). It is deliberately not synchronised at login, because **someone who has left
  never logs in again**.

### 7.3.1 Login providers

Not Google-specific: **one generic OIDC client** carries Entra ID, Okta, Keycloak,
Auth0, Cognito and GitLab by configuration alone (`AF_OIDC_PROVIDERS` plus
`AF_OIDC_<ID>_*`). Google is one instance of the same implementation, and **its
environment variable names (`GOOGLE_OAUTH_*`) are unchanged**, so existing deployments
need no edit.

- The login screen shows one button per enabled provider.
- **There is still exactly one redirect URI** (`/oauth2/callback`). Which provider a
  callback belongs to travels in a signed state cookie and is **checked against the
  configured set before dispatching**. The session cookie carries the provider and
  subject, and the per-request check asks that provider.
- **Trust has no default** (`AF_OIDC_<ID>_TRUST`). Either the provider asserts that the
  email is verified (`email_verified`), or the issuer is pinned to a single tenant
  (`issuer`). **Entra ID does not emit a verified flag**, so it must use the issuer form.
  A provider that declares neither is disabled at boot.
- **A multi-tenant issuer (`common`, `organizations`, `consumers`) with no tenant
  restriction (`AF_OIDC_<ID>_ALLOWED_TIDS`) stops the boot.** Allowing it would put
  *everyone with a Microsoft account* at the door, and a personal account can change its
  email — which makes an email allowlist meaningless.
- **One misconfigured provider must not lock everybody out**: a provider missing
  configuration is disabled with a warning, and only **zero** working providers is
  fatal.
- **The id token's signature is not verified**, deliberately: this is the authorisation
  code flow with a client secret, and the token comes straight from the token endpoint
  over TLS. The tenant id is read from its payload. **If a front-channel path is ever
  added, JWKS verification becomes mandatory.** There is no JWT library dependency at
  all.

**GitHub is a separate adapter** — it is not OIDC. Permission is **the AND of two
independent gates**:

1. **Organisation membership** (required, `AF_GITHUB_ALLOWED_ORGS`). If no organisations
   are configured the provider is disabled entirely — **this environment variable is
   what enables GitHub login**, because the thing granting access *is* the organisation
   membership. A client id alone enables nothing.
2. **An email allowlist** (`AF_GITHUB_ALLOWED_{EMAILS,DOMAINS}`), falling back to the
   deployment-wide one; with neither, the organisation is the allowlist. The
   database-derived terms above are added **to this gate only**. The email is taken as
   the account's **primary and verified** address, and the subject is the **numeric id**
   — a login name can be changed.

Re-checking every request would be an API call, so it is cached per subject
(`AF_GITHUB_MEMBERSHIP_TTL`, default 10 minutes), and if GitHub is unreachable **the last
positive result is honoured for a grace period** (`AF_GITHUB_MEMBERSHIP_GRACE`, default
one hour) and then refused.

**The access token is held only in process memory** — putting it in a cookie would
expose it to XSS. Therefore **a CP restart loses the evidence.** That person is still
an organisation member, so the answer is **"sign in again" (`reauth`, a 401 to the API),
not "forbidden"** — still fail-closed, but without telling them something untrue.

**Tenant-defined sign-in methods** (`tenant_idp`) let a subsidiary bring its own IdP —
OIDC, or a GitHub organisation. The decisive difference from an environment-configured
provider is **who activates it**, and that is what carries the whole safety argument:

- **A tenant administrator writes the row; only a deployment administrator can make it
  active.** Registering an IdP is the power to *declare who someone is*, and an identity
  is one per deployment keyed by email — so anyone who could activate their own IdP
  could mint a token claiming the IT department's address. **Pinning the issuer is not a
  defence, because the issuer would be the attacker.** Even without malice, registering
  a self-signup-enabled tenant in good faith opens the whole deployment.
- Before approval **there is no button, and the callback and session paths refuse it**.
  Changing the issuer, the client id, the trust mode, the kind or the linking claim — or
  *widening* the allowed domains, tenant ids or organisations — sends the row back for
  approval. A tenant administrator can suspend it or send it back to pending; every
  status change is audited (`tenant_idp.*`).
- **Provider ids are namespaced** (`t:<tenant-slug>:<name>`), so a tenant cannot create a
  row that shadows an environment-configured provider.
- **A deployment role cannot be obtained** through such a login, because the identity
  upsert never downgrades — if that were reachable, the role would survive deleting the
  rogue provider.
- **It does not attach to an existing identity by email match.** It may only claim an
  identity that has **never signed in** — an invitation placeholder — and an address
  with a login history is refused.
- **The only entry gate is the row's own allowed domains, which are mandatory**, with no
  fallback to the deployment list or another tenant's roster. One domain belongs to one
  tenant, and that is what bounds the addresses this issuer may speak for. A
  multi-tenant issuer also requires allowed tenant ids.
- **A session from it can only enter its own tenant.**
- The client secret is **sealed with the tenant key in the database** (the custodian of
  §7.6), never returned to the UI, and **a failure to decrypt is an explicit error rather
  than an empty value**. Because a secret now lives in the data directory, **the rule that
  the master key lives outside it matters more, not less** (§7.6).

Authorisation after sign-in is [05 §5.4](05-api.md): your own resources only, plus a
membership check; admin APIs are role-gated (a deployment administrator for the whole
deployment, a tenant administrator for their own tenant). Roles live at two levels, the
identity and the membership ([06 §6.2](06-data.md)).

### 7.3.2 Restricting where a tenant may connect from

The gate after "who": "from where", configured per tenant as `allowed_cidrs`
([decisions/0047](../decisions/0047-tenant-network-restriction.md)).

⚠️ **This is not a network defence.** The request reaches the CP and is refused **after**
the session is verified. It does nothing against pre-authentication vulnerabilities,
denial of service or scanning — those belong to the ingress rules (`AlbIngressCidr` on
AWS) and a WAF. What it protects against is **someone with valid credentials touching
data from a place they should not**.

- **The source address is decided by how many proxy hops you declare**
  (`AF_TRUSTED_PROXY_HOPS`, default 0; the AWS template sets 1). Zero means the socket's
  peer; N means **the Nth entry from the right** of the forwarded-for header. Anyone can
  prepend to that header, but a trusted hop **appends on the right** — so counting from
  the right cannot be spoofed. **Only an implementation that reads the leftmost value is
  dangerous.** It is read in exactly one place, the outermost middleware, for the same
  reason the auth gate deletes the identity header in exactly one place.
- **The surfaces a workspace calls are exempt** — `/mcp`, `/git/`, `/engine/` and
  `/internal/`. Their source is the user's own workspace, which says nothing about where
  the person is. Including them would block every call from your own workspace.
- **Escape hatches against locking yourself out**: a deployment administrator is exempt;
  a save that would shut out the editor's current address is refused
  (`would_lock_out`); and so is a save when the forwarded-for header arrives while no
  proxy hops are declared (`proxy_not_configured`) or the chain is shorter than declared
  (`client_ip_unknown`).

## 7.4 Keeping L2 separate

L2 — as whom the agent runs — is the user's own sign-in, and the CP takes no part in it
beyond showing the state and offering the flow ([08](08-integrations.md)). **Sharing
credentials across workspaces is forbidden by design**; the home separation is the
boundary. The one place a member's token passes through the CP is the git OAuth refresh
([decisions/0052](../decisions/0052-tenant-git-oauth.md)): the workspace posts its refresh
token to `/internal/git-oauth/{bitbucket,jira}/refresh`, and the CP adds the tenant's
OAuth app secret — which then never has to be copied into every member's store — and
returns the result without storing the token.

## 7.5 CP ↔ agent authentication

- A per-workspace token (`AGENT_TOKEN`) is minted when the workspace record is created
  and persisted ([06](06-data.md)). It reaches the agent as a 0600 env file for
  `docker run` (never on the command line), as an SSM SecureString through the task
  definition's `secrets` on `ecs` / `ecs-ec2`, and in the process environment on
  `native`. A container that exists without a record has its token adopted by inspection
  rather than being recreated.
- Every relay — REST, SSE, WebSocket, preview, and the CP's own calls to the agent — adds
  it as a bearer, and the agent's `RequireToken` verifies it on everything except
  `/healthz`, **in constant time**. With no token set the gate is open, for development
  only.
- This is defence in depth on top of the network separation (§7.2), and it protects the
  agent **from the network, not from its own workspace**: agent sessions hold the same
  token (the af MCP server needs it), so any route of the agent is callable by code
  running in a session. A feature that must start only on a person's press therefore
  binds the result to that press instead of relying on route authentication
  ([decisions/0102](../decisions/0102-aws-login-through-the-console.md)).

## 7.6 Secrets and envelope encryption

**The principle: secrets stay in the user's own area; the CP neither holds nor
interprets the plaintext of a member's credentials; nothing secret is logged.**

| Secret | Stored | Exposure |
|---|---|---|
| Git credentials and provider keys (GitHub, Bitbucket, Jira, opencode providers, MCP secrets, …) | **the encrypted store** `~/.config/agent-fleet/secrets.enc` (AES-256-GCM, 0600) in the workspace home | that user only. Git reads it through the credential helper (`workspace-agent cred`), which decrypts on demand and writes to stdout; other keys are injected into the process that needs them. **No plaintext file is created** |
| The agent's own credentials file | the agent's config directory, outside the home and outside the browser's reach (§7.2) | that user only |
| System secrets — login client secrets, the master key (`AF_MASTER_KEY`), the cookie secret | environment files kept out of git; on AWS, SSM SecureStrings under `/af-cp/` (the database password is RDS's managed secret) | the CP only. **The master key is stored outside the data area and never included in a backup** — losing it is a crypto-shred |
| Tenant secrets — a tenant IdP's or git OAuth app's client secret, MCP server credentials | the database, sealed with the tenant key | the CP only; never returned to the UI |
| A signed-in GitHub user's access token | **process memory only** | the CP only; lost on restart, and that person is asked to sign in again |
| Personal access tokens | only a SHA-256 hash, in the database | shown in plaintext exactly once, at issue |

⚠️ **Without `AF_MASTER_KEY` the store is plaintext.** No master key means no DEK, and the
agent then writes the same store as `secrets.json`, unencrypted. That is the intent under
`AUTH=dev`. Under any other `AUTH` the CP starts anyway but logs a `WARNING` at start-up and
lists `plaintext_secrets` in `deployment_warnings` on a super_admin's `GET /api/admin/tenants`,
which the Console's admin modal shows as a banner (`master_key_guard.go`). It warns rather
than refuses so that a deployment already running without a key is not stopped by an upgrade:
setting the key later is not transparent. A running workspace keeps the environment it was
started with, so it has no `AF_SECRET_KEY` until it is stopped and started again (`Start` on a
running container only inspects it). After that restart the agent reads `secrets.enc` and never
migrates `secrets.json`, so members reconnect what they had stored — and the old
`secrets.json` stays in each home, and in every backup, until someone deletes it; treat those
credentials as exposed and rotate them.

**Envelope encryption with a custodian abstraction**
([decisions/0005](../decisions/0005-envelope-custodian.md)):

- A per-workspace DEK is wrapped by a per-tenant KEK and stored (`wrapped_dek`). The CP
  unwraps it when starting the workspace and injects it as `AF_SECRET_KEY`. **The agent
  is indifferent to the scheme.**
- The custodian is an interface (`KeyCustodian`). The current implementation
  (`localCustodian`) derives the KEK from the master key; the same custodian seals the
  tenant secrets above and the session handoff and share payloads.
- ⚠️ **The honest limit**: because that KEK derives from the master key — and the DEK
  itself is derived from the master key and the user key, so that stores written before
  envelope storage still open — the effective strength equals a single master key.
  **True per-tenant crypto-shredding only arrives with a Vault or KMS custodian**, which
  is 📋 — the seam exists and nothing more.

## 7.7 Audit

- The store is the audit log (`audit_log`, [06](06-data.md)), with an actor kind of
  user, admin, MCP or system — and `claude` for the opt-in import of Claude's own tool
  calls from its transcripts (`AF_CLAUDE_AUDIT_INTERVAL`, off by default).
- **Only mutating and destructive operations are recorded**, with one read as the
  exception: `GET /api/agents/memory/export`, the one path that carries a member's
  memory out of the environment (the target is the format). **The raw terminal stream is
  never stored** — it would capture secrets. The proxy layer takes the target from the
  URL, except `PUT /api/fs/file`, whose target is the path from the validated JSON body;
  **file contents are never recorded**.
- Written from the CP's proxy layer, the admin and tenant APIs, and the MCP write tools
  (which record the token's id, with **the role resolved live at call time**).
- **Irreversible admin actions record the request first** (`store.BeginIrreversible`):
  clean home, home-backup deletion, workspace destroy, membership remove and delete, tenant
  delete, pool-slot terminate, engine-model purge, tenant sign-in method (IdP) delete and
  internal-git repository delete and rename write `<action>.requested` before acting
  and **refuse with `503 audit_unavailable` when that write fails**, then `<action>` with the
  outcome and the answered status. A failed outcome write is logged, not returned: the action
  has happened and the request row still names who asked. Every other audit write stays
  best-effort after the fact.
- Read through `GET /api/admin/audit` and the Console, scoped by tenant and role.

## 7.8 Egress control 🚧

Implemented:

- **A forward proxy**, run as a subcommand of the same binary (`egress-proxy`,
  `AF_EGRESS_LISTEN`, default `:3128`). It decides by the host name of the CONNECT or
  HTTP request and **does not decrypt TLS**. It is designed to check for loopback,
  link-local (the cloud metadata address included) and unspecified destinations and
  refuse them in every mode.
- Events go to the CP (`POST /internal/egress`, `AF_EGRESS_TOKEN`) and are aggregated
  daily (`egress_daily`); the policy is served back from `GET /internal/egress/policy`.
- **The allowlist is versioned** (`egress_allowlist`: active / proposed / retired) with a
  deployment-wide mode (`egress_mode`). **An AI may only propose; a human approves** —
  nothing is applied automatically.
- **Enforce blocks at the proxy**: outside the allowlist the answer is a 403.
- `AF_EGRESS_PROXY_ADDR` injects the proxy variables (`http_proxy`, `https_proxy`,
  `no_proxy`) into every workspace, on every target.

**What is missing is the fence.** Nothing forces a workspace's traffic through the
proxy: there is no internal-only network on `docker` and no egress rule on the AWS
security groups, and no deployment template runs the proxy. A process that ignores the
proxy variables goes straight out, so **enforce does not yet constrain a workspace**.
The staged rollout stays the design — observe in log-only mode, harden the allowlist
from what you measured, and only then switch to enforce.

## 7.9 Risks and open work

1. **Skipping permission prompts by default** — the workspace boundary is the only wall,
   so §7.2 must hold. A user can turn it off
   ([decisions/0056](../decisions/0056-tool-permission-choice.md)), but that is not a
   substitute for isolation (§7.1).
2. **Compromise of the CP or host collapses one deployment at once** (§7.1). The
   mitigation is that it does not spread between companies — on AWS, only across
   separate AWS accounts. The CP's AWS role can still be narrowed ([#1182](https://github.com/k-k1/agent-fleet/issues/1182)).
3. **Revoking and rotating long-lived agent credentials** — the framework is there, but
   real revocation waits for Vault or KMS (§7.6).
4. **Supply chain** — provenance and regular updates for what is baked into the
   workspace image ([04](04-agent.md)).
5. **Egress enforcement does not constrain workspaces yet** — the proxy blocks, but no
   network fence routes a workspace through it (§7.8,
   [#1181](https://github.com/k-k1/agent-fleet/issues/1181)).
6. The outward-facing threat model and the vulnerability reporting channel are
   [SECURITY.md](../../SECURITY.md).

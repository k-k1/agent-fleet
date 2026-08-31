---
audience: "anyone touching authentication, crypto, isolation, audit or egress"
source_of_truth: "the code (this is the boundaries and the intent)"
updated: "2026-07"
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
([decisions/0056](../decisions/0056-tool-permission-choice.md)). **The boundary design
does not change because of it**: only some kinds can turn it off, the mode can be
changed back from inside the TUI, and the CLI's own configuration path is not blocked.
In other words, **the permission prompt is a way to reduce accidents, not an isolation
boundary. The container boundary is still the only wall.**

```
low trust  ┌──────────────────────────────┐
           │ inside the workspace         │ ← arbitrary code execution is allowed here
           └──────────────┬───────────────┘
                          │ the isolation boundary
high trust ┌──────────────▼───────────────┐
           │ Control Plane / host         │ ← must not be reachable from a workspace
           └──────────────────────────────┘
```

**The honest limit.** Within one deployment the CP holds the Docker socket — which is
host-root equivalent — and injects plaintext DEKs, so **if the CP or the host is
compromised, the separation inside that deployment falls at once**. It does not spread
between companies, because those are separate deployments — which is the strength of
the delivery model ([decisions/0001](../decisions/0001-self-host-vs-saas.md)).
Candidate mitigations: rootless Docker, a socket proxy, a least-privilege CP.

## 7.2 Isolation controls

| Concern | local (Docker, the default) | aws (ECS) 🚧 |
|---|---|---|
| Files between users | a per-membership home, bind-mounted. **No other user's home is mounted at all** | an EFS access point fixing the root directory and the uid/gid |
| Process and memory | one membership, one container, with a memory limit | task isolation (Fargate shares no host) |
| Network | a dedicated network so containers cannot reach each other; the agent is published on the host's loopback only | security groups and subnets, instance metadata blocked, a minimal task role |
| Container → host | unprivileged, capabilities minimised | no shared host on Fargate |
| Sensitive state | the agent's plaintext state is moved to a second mount **outside the file browser's reach**; the encrypted store stays in the home behind a denylist | the same — the image and the agent are common |

**The limit**: a shell inside the container runs as the same uid, so a user's own
bring-your-own token cannot be made completely invisible to them. That is not solvable
in principle. Keeping it out of the browser, encrypting it at rest and injecting it as
an environment variable is judged good enough.

## 7.3 L1 Console authentication — three modes

Selected by `AUTH`. All three sanitise the resolved email into the identity key.

| Mode | How | For |
|---|---|---|
| `oauth` (the production default) | **The CP is itself an OIDC client** (§7.3.1). It owns the login routes and issues a signed, HttpOnly, Secure cookie on success | self-hosting. Assumes HTTPS at the edge |
| `proxy` | Trust an upstream gateway's identity header. **A missing header is a 401 — there is no fallback.** Assumes the CP is bound to loopback | ALB OIDC, or an existing gate. **This is also the answer for a SAML IdP**: bridge it ([decisions/0043](../decisions/0043-login-idp.md)) |
| `dev` | A fixed user | local development only. `AUTH=oauth` cannot work over plain HTTP, because a Secure cookie will not be stored |

**The auth gate**, in `oauth` mode:

- It inspects every request and **always deletes any inbound identity header** before
  injecting the verified one — so an edge that passes headers through cannot be used to
  impersonate.
- The exempt paths are declared in one place: the login routes and the health check;
  the surfaces with their own authentication (MCP with a bearer PAT, git with basic
  auth, the internal endpoints with per-purpose tokens); and the legacy redirect.
- Three allowlist mechanisms compose: emails, domains, and **a file re-read on every
  login — so adding someone needs no restart**. A per-provider list *replaces* the
  deployment-wide one for that provider.
- ★ **The entry decision is a union along the email axis**
  ([decisions/0043](../decisions/0043-login-idp.md)):

  ```
  ( provider list | deployment list )  ∪  ( tenant auto-join domains | holds a membership )
  ```

  The second half comes from the database, so **an invited person gets in without being
  in the environment allowlist** — which lets an invitation-driven deployment keep the
  roster in one place. **Everything empty still means deny-all (fail-closed).** The
  union is taken **only within the email axis**: a different kind of check, such as
  GitHub organisation membership, stays an AND — otherwise merely holding a membership
  would bypass it.
- **The check runs on every request, not only at login.** Removing someone from the
  list, or deactivating their membership, locks them out on their next request rather
  than at cookie expiry. The cookie is stateless, so **there is no individual
  revocation**; the only way to cut every session at once is to rotate the cookie
  secret.
- ★ **The tenant gate is not in the auth gate.** The gate does not know the tenant —
  that is resolved later — so a tenant rule there would have no tenant to be about. The
  tenant check happens during resolution, and a mismatch returns a **"sign in with a
  different provider"** result that leads the Console back to sign-in, rather than
  ending at a 403.
- **The deployment-administrator list is read once at boot and is the only truth.**
  Role hints only ever upgrade, so **demotion happens in a sweep at start**. It is
  deliberately not synchronised at login, because **someone who has left never logs in
  again**.

### 7.3.1 Login providers

Not Google-specific: **one generic OIDC client** carries Entra ID, Okta, Keycloak,
Auth0, Cognito and GitLab by configuration alone. Google is one instance of the same
implementation, and **its environment variable names are unchanged**, so existing
deployments need no edit.

- The login screen shows one button per enabled provider.
- **There is still exactly one redirect URI.** Which provider a callback belongs to
  travels in a signed state cookie and is **checked against the configured set before
  dispatching**.
- **Trust has no default.** Either the provider asserts that the email is verified, or
  the issuer is pinned to a single tenant. **Entra ID does not emit a verified flag**,
  so it must use the issuer form. A provider that declares neither is disabled at boot.
- ★ **A multi-tenant issuer with no tenant restriction stops the boot.** Allowing it
  would put *everyone with a Microsoft account* at the door, and a personal account can
  change its email — which makes an email allowlist meaningless.
- **One misconfigured provider must not lock everybody out**: a provider missing
  configuration is disabled with a warning, and only **zero** working providers is
  fatal.
- **The id token's signature is not verified**, deliberately: this is the authorisation
  code flow with a client secret, and the token comes straight from the token endpoint
  over TLS. **If a front-channel path is ever added, JWKS verification becomes
  mandatory.** There is no JWT library dependency at all.

**GitHub is a separate adapter** — it is not OIDC. Permission is **the AND of two
independent gates**:

1. **Organisation membership** (required). If no organisations are configured the
   provider is disabled entirely — **this environment variable is what enables GitHub
   login**, because the thing granting access *is* the organisation membership.
2. **An email allowlist**, falling back to the deployment-wide one; with neither, the
   organisation is the allowlist. The database-derived terms above are added **to this
   gate only**. The email is taken as the account's **primary and verified** address,
   and the subject is the **numeric id** — a login name can be changed.

Re-checking every request would be an API call, so it is cached per subject, and if
GitHub is unreachable **the last positive result is honoured for a grace period** and
then refused.

★ **The access token is held only in process memory** — putting it in a cookie would
expose it to XSS. Therefore **a CP restart loses the evidence.** That person is still
an organisation member, so the answer is **"sign in again", not "forbidden"** — still
fail-closed, but without telling them something untrue.

**Tenant-defined sign-in methods** let a subsidiary bring its own IdP. The decisive
difference from an environment-configured provider is **who activates it**, and that is
what carries the whole safety argument:

- **A tenant administrator writes the row; only a deployment administrator can make it
  active.** Registering an IdP is the power to *declare who someone is*, and an identity
  is one per deployment keyed by email — so anyone who could activate their own IdP
  could mint a token claiming the IT department's address. **Pinning the issuer is not a
  defence, because the issuer would be the attacker.** Even without malice, registering
  a self-signup-enabled tenant in good faith opens the whole deployment.
- Before approval **there is no button, and the callback and session paths refuse it**.
  Changing the issuer, the client id or the trust mode — or *widening* the allowed
  domains — sends the row back for approval. Suspension is available to the tenant
  administrator, and both are audited.
- **Provider ids are namespaced**, so a tenant cannot create a row that shadows an
  environment-configured provider.
- **A deployment role cannot be obtained** through such a login, because the identity
  upsert never downgrades — if that were reachable, the role would survive deleting the
  rogue provider.
- **It does not attach to an existing identity by email match.** It may only claim an
  identity that has **never signed in** — an invitation placeholder — and an address
  with a login history is refused.
- **The only entry gate is the row's own allowed domains, which are mandatory**, with no
  fallback to the deployment list or another tenant's roster. One domain belongs to one
  tenant, and that is what bounds the addresses this issuer may speak for.
- **A session from it can only enter its own tenant.**
- The client secret is **sealed with the tenant key in the database**, never returned to
  the UI, and **a failure to decrypt is an explicit error rather than an empty value**.
  ★ Because a secret now lives in the data directory, **the rule that the master key
  lives outside it matters more, not less** (§7.6).

### 7.3.2 Restricting where a tenant may connect from

The gate after "who": "from where"
([decisions/0047](../decisions/0047-tenant-network-restriction.md)).

⚠️ **This is not a network defence.** The request reaches the CP and is refused **after**
the session is verified. It does nothing against pre-authentication vulnerabilities,
denial of service or scanning — those belong to the ingress rules and a WAF. What it
protects against is **someone with valid credentials touching data from a place they
should not**.

- **The source address is decided by how many proxy hops you declare.** Zero means the
  socket's peer; N means **the Nth entry from the right** of the forwarded-for header.
  Anyone can prepend to that header, but a trusted hop **appends on the right** — so
  counting from the right cannot be spoofed. **Only an implementation that reads the
  leftmost value is dangerous.** It is read in exactly one place, the outermost
  middleware, for the same reason the auth gate deletes the identity header in exactly
  one place.
- **The MCP and git surfaces are exempt.** Their source is the user's own workspace
  container, which says nothing about where the person is. Including them would block
  every MCP and git call from your own workspace.
- **Escape hatches against locking yourself out**: a deployment administrator is exempt;
  saving always admits the editor's current address; and if the header arrives while no
  proxy hops are declared, **the save itself is refused**.

## 7.4 Keeping L2 separate

L2 — as whom the agent runs — is the user's own sign-in, and the CP takes no part in it
beyond showing the state and offering the flow ([08](08-integrations.md)). **Sharing
credentials across workspaces is forbidden by design**; the home separation is the
boundary.

## 7.5 CP ↔ agent authentication

- A per-container token is injected at start and persisted ([06](06-data.md)). After a
  CP restart the existing container's token is adopted by inspection rather than
  recreated.
- Every relay adds it as a bearer, and the agent verifies it on everything except the
  health check **in constant time**.
- This is defence in depth on top of the network separation (§7.2).

## 7.6 Secrets and envelope encryption

**The principle: secrets stay in the user's own area; the CP neither holds nor
interprets the plaintext; nothing secret is logged.**

| Secret | Stored | Exposure |
|---|---|---|
| Git credentials and provider keys | **the encrypted store** in the workspace home | that user only. One credential helper decrypts on demand and writes to stdout — **it never creates a plaintext file** |
| The agent's own credentials file | the agent's config directory, outside the home and outside the browser's reach (§7.2) | that user only |
| System secrets — client secrets, the master key, the cookie secret | environment files kept out of git; a secrets manager on AWS 🚧 | the CP only. **The master key is stored outside the data area and never included in a backup** — losing it is a crypto-shred |
| A signed-in GitHub user's access token | **process memory only** | the CP only; lost on restart, and that person is asked to sign in again |
| Personal access tokens | only a hash, in the database | shown in plaintext exactly once, at issue |

**Envelope encryption with a custodian abstraction**
([decisions/0005](../decisions/0005-envelope-custodian.md)):

- A per-workspace DEK is wrapped by a per-tenant KEK and stored. The CP unwraps it when
  starting the workspace and injects it. **The agent is indifferent to the scheme.**
- The custodian is an interface. The current implementation derives the KEK from the
  master key.
- ⚠️ **The honest limit**: because that KEK derives from the master key, the effective
  strength equals a single master key. **True per-tenant crypto-shredding only arrives
  with a Vault or KMS custodian**, which is 📋 — the seam exists and nothing more.

## 7.7 Audit

- The store is the audit log ([06](06-data.md)), with an actor kind of user, admin, MCP
  or system.
- **Only mutating and destructive operations are recorded.** Reads are off by default,
  and **the raw terminal stream is never stored** — it would capture secrets.
- Written from the CP's proxy layer, the admin API, and the MCP write tools (which
  record the token's id, with **the role resolved live at call time**).
- Read through the admin API and the Console, scoped by tenant and role.

## 7.8 Egress control 🚧

Implemented so far:

- **A forward proxy**, run as a subcommand of the same binary. It decides by FQDN and
  **does not decrypt TLS**.
- Events are aggregated daily in the CP; policy is distributed back to the proxy.
- **The allowlist is versioned** (active / proposed / retired) with a deployment-wide
  mode. **An AI may only propose; a human approves** — nothing is applied automatically.
- **The staged rollout is the core of the design**: observe in log-only mode, harden the
  allowlist from what you measured, and only then switch to enforce. Enforcement and
  always-on container wiring are still to come.

## 7.9 Risks and open work

1. **Skipping permission prompts by default** — the container boundary is the only wall,
   so §7.2 must hold. A user can turn it off
   ([decisions/0056](../decisions/0056-tool-permission-choice.md)), but that is not a
   substitute for isolation (§7.1).
2. **Compromise of the CP or host collapses one deployment at once** (§7.1). The
   mitigation is that it does not spread between companies.
3. **Revoking and rotating long-lived agent credentials** — the framework is there, but
   real revocation waits for Vault or KMS (§7.6).
4. **Supply chain** — provenance and regular updates for what is baked into the
   workspace image ([04](04-agent.md)).
5. **Egress enforcement is not done** — observation exists, blocking does not (§7.8).
6. The outward-facing threat model and the vulnerability reporting channel are
   [SECURITY.md](../../SECURITY.md).

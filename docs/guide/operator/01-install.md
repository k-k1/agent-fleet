# 01. Initial Setup

English | [日本語](01-install.ja.md)

This page walks through your first deployment step by step, with the decision points along the
way. **The source of truth for the actual commands is the "Quick start" section of
[deploy/compose/README.md](../../../deploy/compose/README.md).** Here we supplement it with
"what to decide and what to watch out for at each step." The working directory is
`deploy/compose/`. For the big picture and where this guide fits, read
[README.md](README.md) first.

## 0. Check the prerequisites

Before starting the build-out, confirm you have the 4 items listed under "Prerequisites" in
[README.md](README.md).

- A Linux host running Docker Engine + `docker compose`.
- A public domain and DNS A/AAAA records pointing at this host (for TLS). For internal-only
  deployments, see the decision in §4.
- A login IdP client (created in §3): a Google OAuth 2.0 web client, or an OIDC app
  registration at Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab.
- Claude seats are brought by each member later, so they are not needed at build time.

## 1. Prepare the configuration file

Copy `deploy/compose/.env.example` to `.env` and edit it (the commands are in the runbook's
"Quick start"). `.env` is outside git management and is the **single source of configuration**.
The meaning of each variable, generation steps, and annotations are described in detail in
[.env.example](../../../deploy/compose/.env.example) itself. If you want an index, see
[dev/09 §9.4](../../dev/09-deploy.md).

The main values you must fill in at build time are the public URL (`PUBLIC_DOMAIN` /
`PUBLIC_BASE_URL`), your login IdP's client ID/secret, the login allowlist
(`AF_OAUTH_ALLOWED_DOMAINS`, etc.), the initial administrators (`SUPER_ADMIN_EMAILS`), the data
storage location (`DATA_DIR`), and the 2 secrets (next section).

## 2. Generate the secrets — put `AF_MASTER_KEY` in a vault at this point

There are 2 secrets in `.env` that you generate yourself. The generation command (32 bytes from
`/dev/urandom`, base64-encoded) is in the runbook's "Quick start."

- **`AF_MASTER_KEY`** — the root of all credential encryption (the master key for envelope encryption).
- **`AF_COOKIE_SECRET`** — the signing key for login session cookies.

> The most important decision: **the moment you generate `AF_MASTER_KEY`, record a copy in a
> password vault / secret manager and store it independently, separate from the data area.**
> This key goes into neither `DATA_DIR` nor backup archives (deliberately, by design). If you
> lose it, all stored credentials and every past backup become **permanently undecryptable**
> (crypto-shred). A restore requires "the same key."
> Details in [03-security.md](03-security.md) and [dev/07 §7.6](../../dev/07-security.md).

In addition, set the `DOCKER_GID` used by the CP to match the host's docker group GID (how to
find the value is in the runbook). Getting this wrong results in permission denied on the docker
socket after startup ([04](04-troubleshooting.md)).

## 3. Configure the login IdP

Register the following as an **authorized redirect URI** at your IdP. You register this **one
URI** no matter how many providers you enable.

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

This path must match `<PUBLIC_BASE_URL>/oauth2/callback`. If they diverge, you get
"redirect URI mismatch" at login (a common failure — [04](04-troubleshooting.md)).

**Google** — create an OAuth client ID (web application) in the Google Cloud Console and put the
issued client ID/secret into `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` in `.env`.

**Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab** — register a confidential
web app at the IdP, then list it in `.env`:

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<tenant-guid>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<client-secret-value>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

Two things about that block are worth reading before you copy it:

- **`_TRUST` has no default, on purpose.** It records *why* this IdP's email address may be
  believed, because the allowlist is written in email addresses. `email_verified` accepts only
  addresses the IdP itself marks as verified (Google, and most Okta / Keycloak / Auth0 setups);
  `issuer` says the issuer is pinned to a single tenant, so that directory's addresses are
  authoritative. **Entra ID never emits `email_verified`**, so `issuer` is the value there.
  A provider with no `_TRUST` is disabled at startup rather than guessed at.
- **Pin the Entra issuer to your own tenant GUID.** If you use the `/common/` or
  `/organizations/` endpoint, everyone on earth with a Microsoft account reaches your login
  screen — and a personal Microsoft account can change its own email address, which would make
  the allowlist meaningless. The CP refuses to start on those endpoints unless
  `AF_OIDC_ENTRA_ALLOWED_TIDS` names the tenants you accept.

**GitHub** — GitHub has no OIDC for user sign-in, so it is configured separately, and what
authorizes the login is membership in an organization you name:

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # required; also what turns the button on
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # strongly recommended, see below
```

- The OAuth App is the same one behind the Console's GitHub **Connect** button
  (`GITHUB_OAUTH_CLIENT_ID`); add the redirect URI above to it. If you would rather the login
  had an app of its own, set `AF_GITHUB_LOGIN_CLIENT_ID` / `AF_GITHUB_LOGIN_CLIENT_SECRET`.
  Setting `GITHUB_OAUTH_CLIENT_ID` alone still means only the git-connect device flow.
- **If your organization restricts third-party OAuth apps, an owner must approve this app.**
  Until they do, the membership check can see nothing and *every* GitHub login is rejected —
  with settings that look entirely correct.
- **Set `AF_GITHUB_ALLOWED_DOMAINS` as well.** GitHub gives the CP the account's *primary
  verified* address, which for most people is a personal one — and a different address is a
  different person here, so they would land in a new empty workspace instead of their own.
  Turning them away at the door is kinder than letting them work somewhere they didn't mean
  to be.
- Membership is re-checked through the GitHub API, cached per person for
  `AF_GITHUB_MEMBERSHIP_TTL` (10m) and, when GitHub is unreachable, honored for
  `AF_GITHUB_MEMBERSHIP_GRACE` (1h) past the last positive answer. That cache lives in memory,
  so after a CP restart these people are asked to **sign in again** — they are not rejected,
  and the round trip usually completes without prompting them.

List several ids (`AF_OIDC_PROVIDERS=entra,okta`) to offer several buttons; the login page shows
one button per enabled provider, and with a single provider it looks exactly as it does today.
A provider whose settings are incomplete is disabled with a warning in the CP log — one broken
IdP never locks the whole company out — and the CP only refuses to start when no provider at all
is usable. Detailed steps are in the runbook's "Login IdP setup" section.

Note: for Console login authentication (L1), the CP performs the OAuth/OIDC flow itself
(`AUTH=oauth`, the default). Companies that put an existing authentication gateway
(oauth2-proxy / ALB OIDC, etc.) in front can choose `AUTH=proxy` (delegating email
identification to upstream headers) — **this is also the answer for a SAML-only IdP**
(HENNGE One / TrustLogin / CloudGate and the like): bridge it with oauth2-proxy or Keycloak.
How this works is in [dev/07 §7.3](../../dev/07-security.md).
The GitHub/Bitbucket integration OAuth is optional — everything works with token pasting even
without it, so you can skip it during initial setup.

## 4. Decision points

Before starting up, make 3 decisions to fit your deployment.

### When to use `tls internal`

By default, Caddy automatically obtains a certificate for the public domain from Let's Encrypt.
This requires **public DNS and reachability on 80/443**. If your deployment is internal-only or
on an isolated network and you cannot provide public DNS, switch to the Caddyfile alternative
(`tls internal`, self-signed). In this case browsers will show a certificate warning, so
consider distributing an internal CA separately. For how to switch, see the "Quick start"
footnote in the runbook and the Caddyfile. Companies with an existing TLS-terminating proxy in
front can remove the Caddy service entirely (Caddyfile alternative 2).

### `AF_PROVISION`: auto or invite

- **`auto` (default)** — logins that pass the allowlist are automatically accepted as members
  of the default tenant. Suited to small teams and domain-based allow policies.
- **`invite`** — unknown identities are rejected until an administrator adds them in the
  Admin panel. Choose this when you want to control who gets in, one by one.

With `invite`, **being invited is itself permission to log in**: somebody you add in the Admin
panel gets in without also being listed in `AF_OAUTH_ALLOWED_*`, so you keep one roster rather
than two lists that drift apart. With `auto`, the allowlist is what decides who may enter at
all, and everyone who passes it lands in the default tenant.

You can start with `invite` from the very first boot — a `SUPER_ADMIN_EMAILS` account reaches
the Admin panel even with no membership of its own.

### Single tenant, or separate tenants

- **Single tenant (default)** — everyone joins the built-in `default` tenant, with zero
  friction. This is enough for most companies.
- **Tenant separation** — add it only when you need **hard isolation**, e.g. between
  departments. Each membership gets a fully isolated Workspace. You can add tenants later, so
  when in doubt it is safest to start with a single tenant and split only when the need arises.

Once you do split, each tenant can carry its own login rules (Admin panel → the tenant →
**Login rules**):

| Setting | What it does | Where it applies |
|---|---|---|
| **Sign-in methods** | Which of the enabled IdPs may be used to enter this tenant | Enforced on every request, not just by hiding buttons |
| **Auto-join domains** | An address in this domain joins this tenant on first sign-in | One domain can belong to only one tenant |
| **Invite domains** | Bounds who may be **added** as a member | The invite form only — never a per-request check |

Each tenant also gets its own sign-in page at `https://<PUBLIC_DOMAIN>/login/<slug>`, showing
only the methods that tenant accepts. Hand that URL to new members; there is no invitation email
(the CP has no SMTP, by design).

> **"Invite domains" is not "who may use this tenant."** It only bounds who can be put on the
> roster. Somebody already invited keeps working even from another domain — which is what makes
> a contractor's address workable — and the way to end their access is to remove the member, not
> to narrow this field.

### A subsidiary with its own IdP

When a tenant is a separate company — a group subsidiary, or a business still being merged — its
Entra ID (or Okta / Keycloak) tenant is a different one, with its own issuer, client ID and
secret. Rather than adding it to `.env` and restarting the CP for every subsidiary, that tenant's
own administrator registers it from the Console: **Tenant settings → "Sign-in methods"** (the
account menu's *Tenant settings*). They fill in the issuer, client ID, client secret, how the
email is trusted, and the email domains it may admit. As a deployment administrator you reach the
same panel from **Admin → the tenant → "Sign-in methods."**

> **A new sign-in method does nothing until you approve it.** It is created as *waiting for
> approval*, and until a deployment administrator activates it (from Admin → the tenant), no
> button appears on the tenant's sign-in page and no session is issued even to somebody who
> constructs the sign-in link by hand.
>
> **This one step is not bureaucracy.** Registering an IdP is the power to declare *who somebody
> is*, and on this deployment a person is identified by their email address — deployment-wide,
> including who is a deployment administrator. An admin who could activate their own issuer could
> issue themselves a token carrying *your* address. Approving is a once-per-subsidiary action, so
> the day-to-day picture ("the department runs itself") is unchanged.

What to check before approving, on the deployment-wide list under **Admin → Tenants**:

- **The issuer** really is that company's own tenant — not a `common` / `organizations` endpoint
  (those accept every Microsoft account in the world, and are refused unless tenant ids are pinned).
- **The email domains** are theirs. This list is what the approval is *for*: it bounds which
  addresses that issuer is allowed to assert, and a domain can belong to only one tenant. Do not
  approve a method that claims the parent company's domain.

Changing the issuer, the client ID or the trust rule — or *adding* a domain — sends the method
back for approval, because the approval was given to that issuer for that scope. Suspending is
always available, to the tenant's own administrator as well: stopping should never wait for you.

Approved methods stay on that list with who approved them and when. Treat it as a register to
re-read now and then, not a queue that empties: the IdP stays under the other company's control,
and settings such as self-sign-up can be turned on after you approved it.

## 5. Start it up

Once `.env` is complete, create `DATA_DIR` and start with `docker compose up -d` (as-is if you
use the prebuilt image, or with `--build` for a local build). The exact commands are in the
runbook's "Quick start." After startup, follow the CP's logs and confirm the health check
passes.

```
curl -s http://127.0.0.1:8099/healthz    # -> ok
```

If `ok` does not come back, or the CP does not come up at all, see "CP does not start" in
[04-troubleshooting.md](04-troubleshooting.md).

## 6. First login and the first administrator

Open `https://<PUBLIC_DOMAIN>` in a browser and sign in with an account listed in
`SUPER_ADMIN_EMAILS`. **That email address becomes `super_admin` on first login.** A
super_admin sees the shield-icon **Admin panel** in the Console and can manage the entire
deployment.

> If login is always rejected, the allowlist is most likely empty. With all 3 channels
> (`AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` / `_EMAILS_FILE`) **empty, and nobody invited to a
> tenant yet, all logins are rejected** (fail-closed = designed to fail safe). On a first
> install nobody is invited yet, so set at least one of them to get your first administrator in.
> Details in [04](04-troubleshooting.md).

## 7. The first tenant and members

As super_admin, from the Admin panel you can create tenants, add members, and configure
resource limits and idle shutdown. With the default single-tenant operation, no tenant creation
is needed, and with `AF_PROVISION=auto`, members inside the allowlist can start using it just by
logging in. The browser operations themselves — member management, limits, auditing — are
covered by the admin volume for administrators.

After starting their own Workspace, each member **logs in with their own Claude seat** from the
Console (BYO). The operator never sets up members' Claude credentials on their behalf.

For day-to-day operations after the build-out (backup, upgrades, shutdown), continue to
[02-operations.md](02-operations.md), and for security operations, to
[03-security.md](03-security.md).

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

> **The full procedure is [05-login-idp.md](05-login-idp.md)** — what to create at Google /
> Microsoft Entra ID / GitHub / another OIDC IdP, which value goes into which `.env` key, how to
> confirm it worked, and what usually goes wrong. That page is the source of truth for sign-in
> configuration; this section is only the shape of the decision. Work through it now and come
> back here for §4.

Register the following as an **authorized redirect URI** at your IdP. You register this **one
URI** no matter how many providers you enable.

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

This path must match `<PUBLIC_BASE_URL>/oauth2/callback` exactly. If they diverge, you get
"redirect URI mismatch" at login (a common failure — [04](04-troubleshooting.md)).

Which keys you fill in depends on the IdP:

| IdP | The keys in `.env` |
|---|---|
| **Google** | `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` (not listed in `AF_OIDC_PROVIDERS`) |
| **Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab** | `AF_OIDC_PROVIDERS=<id>` plus `AF_OIDC_<ID>_ISSUER` / `_CLIENT_ID` / `_CLIENT_SECRET` / `_TRUST` |
| **GitHub** | `AF_GITHUB_ALLOWED_ORGS` (required — it is also what turns the button on) plus `GITHUB_OAUTH_CLIENT_ID` / `_SECRET`, and `AF_GITHUB_ALLOWED_DOMAINS` |

> **Not here: the git providers' OAuth apps.** The "Connect with OAuth" buttons for cloning
> GitHub / Bitbucket repositories are **per-tenant**, registered in the Console by a tenant
> administrator under **Tenant settings → Integrations → Git provider OAuth**. There is no
> deployment-level setting for them and `BITBUCKET_OAUTH_KEY` / `_SECRET` are not read at all;
> `GITHUB_OAUTH_CLIENT_ID` above means the sign-in app only. See
> [docs/71](../../71-tenant-git-oauth.md).

Three points decide whether this goes smoothly, and all three are covered in detail in
[05](05-login-idp.md):

- **`AF_OIDC_<ID>_TRUST` has no default, on purpose** — it records *why* this IdP's email address
  may be believed, and a provider without it is disabled at startup rather than guessed at.
  Entra ID never emits `email_verified`, so `issuer` is the value there.
- **Pin an Entra issuer to your own tenant GUID.** On a `/common/` or `/organizations/` endpoint
  the CP **refuses to start** unless `AF_OIDC_<ID>_ALLOWED_TIDS` names the tenants you accept —
  otherwise every Microsoft account on earth reaches your login and the allowlist stops meaning
  anything.
- **A GitHub login also needs the organization to approve the OAuth app** when the org restricts
  third-party apps; until it does, everybody is rejected with settings that look correct.

List several ids (`AF_OIDC_PROVIDERS=entra,okta`) to offer several buttons; the login page shows
one button per enabled provider, and with a single provider it looks exactly as it does today.
A provider whose settings are incomplete is disabled with a warning in the CP log — one broken
IdP never locks the whole company out — and the CP only refuses to start when no provider at all
is usable.

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

- **`invite` (what new installs start with)** — unknown identities are rejected until an
  administrator adds them in the Admin panel. You control who gets in, one by one.
- **`auto`** — logins that pass the allowlist are automatically accepted as members
  of the default tenant. Suited to small teams and domain-based allow policies.

With `invite`, **being invited is itself permission to log in**: somebody you add in the Admin
panel gets in without also being listed in `AF_OAUTH_ALLOWED_*`, so you keep one roster rather
than two lists that drift apart. With `auto`, the allowlist is what decides who may enter at
all, and everyone who passes it lands in the default tenant — which means that with `auto`,
**`AF_OAUTH_ALLOWED_*` is the only thing between a stranger and a workspace**.

You can start with `invite` from the very first boot — a `SUPER_ADMIN_EMAILS` account reaches
the Admin panel even with no membership of its own. Anyone not invited yet lands on a
**"you haven't been invited yet"** page showing the address they signed in with, so they can
quote it to you (you add people by address).

> ★ **Only the templates changed.** The CP's built-in default is still `auto`, so an existing
> `.env` that never set `AF_PROVISION` behaves exactly as before. What starts closed is a **new
> install** made from the current `.env.example` (or from the ECS `AfProvision` parameter).
> Deriving it at runtime — "invite while there are no members yet" — was rejected: the condition
> stops holding the moment the first person joins, so the next restart would silently reopen the
> deployment (docs/61 §61.17.7).
>
> ★ **Switching an existing deployment to `invite` does not affect anyone already working.**
> An existing membership is read first (`membershipsFor`); only NEW auto-admissions stop.

### Single tenant, or separate tenants

- **Single tenant (default)** — everyone joins the built-in `default` tenant, with zero
  friction. This is enough for most companies.
- **Tenant separation** — add it only when you need **hard isolation**, e.g. between
  departments. Each membership gets a fully isolated Workspace. You can add tenants later, so
  when in doubt it is safest to start with a single tenant and split only when the need arises.

Once you do split, each tenant can carry its own login rules (Admin panel → the tenant →
**Sign-in methods** and **Login rules**):

| Setting | Where | What it does | Where it applies |
|---|---|---|---|
| **Accept** | Sign-in methods | This method may be used to enter this tenant | Enforced on every request, not just by hiding buttons |
| **Show button** | Sign-in methods | Removes the button only, leaving the method accepted | Display only — somebody signing in with a cleared method is still admitted |
| **Auto-join domains** | Login rules | An address in this domain joins this tenant on first sign-in | One domain can belong to only one tenant |
| **Invite domains** | Login rules | Bounds who may be **added** as a member | The invite form only — never a per-request check |

The **Sign-in methods** panel lists the deployment's own methods (marked *deployment-wide*,
enabled in your `.env`) and the tenant's own registered methods in one list. There is no field to
type ids into any more — you flip the two toggles on each row. Only a deployment administrator
can flip them; a tenant administrator sees the same state read-only.

Each tenant also gets its own sign-in page at `https://<PUBLIC_DOMAIN>/login/<slug>`, showing
only the methods that tenant accepts, minus any whose **Show button** is cleared. Hand that URL
to new members; there is no invitation email (the CP has no SMTP, by design).

> **Clearing "Show button" now works on the plain `/login` too.** The page without a slug is
> rendered as the **default tenant's** page, so a method whose button you cleared on the default
> tenant is gone from there as well.
>
> ★ **"Accept" is deliberately NOT applied there.** That page is the only door for somebody who
> belongs to no tenant yet — a new deployment admin, anybody not invited so far. If narrowing
> what the default tenant accepts reached it, one edit would leave a page with no buttons at all,
> and undoing that edit needs a session, which needs that page: nobody could get in again short
> of editing the database. Narrowing is still enforced where it always was — when the tenant is
> resolved, on every request.

> **"Invite domains" is not "who may use this tenant."** It only bounds who can be put on the
> roster. Somebody already invited keeps working even from another domain — which is what makes
> a contractor's address workable — and the way to end their access is to remove the member, not
> to narrow this field.

### A tenant with its own IdP

Some tenants have an identity source of their own: a different Entra ID (or Okta / Keycloak)
tenant, with its own issuer, client ID and secret, or **a GitHub organization** instead. A group
subsidiary is the obvious case, but so is a business still being merged, an outsourcing partner,
or a division that simply runs its own directory. Rather than adding each one to `.env` and
restarting the CP, that tenant's own administrator registers it
from the Console — **Tenant settings → "Sign-in methods"** (the account menu's *Tenant settings*),
which you reach from **Admin → the tenant → "Sign-in methods."** Nothing here needs a restart.

**The step-by-step — what the tenant fills in, what to check before approving, and the GitHub
variant — is [05 §7](05-login-idp.md).** What belongs here is the decision:

> **A new sign-in method does nothing until you approve it.** It is created as *waiting for
> approval*, and until a deployment administrator activates it, no
> button appears on the tenant's sign-in page and no session is issued even to somebody who
> constructs the sign-in link by hand.
>
> **This one step is not bureaucracy.** Registering an IdP is the power to declare *who somebody
> is*, and on this deployment a person is identified by their email address — deployment-wide,
> including who is a deployment administrator. An admin who could activate their own issuer could
> issue themselves a token carrying *your* address. Approving is a once-per-tenant action, so
> the day-to-day picture ("the department runs itself") is unchanged.

So the deployment-wide register at **Admin → Tenants › Sign-in method register**
("Tenant-defined sign-in methods") is a list you own: it carries the approve and suspend buttons, and
approved methods stay on it with who approved them and when. Treat it as a register to re-read
now and then, not a queue that empties — the IdP stays under the other company's control, and
settings such as self-sign-up can be turned on after you approved it. Suspending is always
available to the tenant's own administrator too: stopping should never wait for you.

> **Watch what you clear under "Accept" in a tenant whose people also belong to
> another tenant.** Say Yamada belongs to both head office and a subsidiary and normally signs in
> with head office's Google. If the subsidiary accepts only its own GitHub,
> switching to that tenant asks Yamada to sign in again with it — and pressing that button is
> refused with *"This email address is already used by another sign-in method."* The same address
> at a different IdP is a different login, and if Yamada has no GitHub account at all there is
> nothing to press.
>
> Two ways out. **(a) Invite Yamada into the subsidiary's GitHub organization** (keeping
> "GitHub only" literally true), or **(b) leave head office's method on Accept.** (b)
> does not widen who can enter: the roster decides that, and this toggle only says which identity
> sources are accepted (do check **auto-join domains** though — that one does create roster
> entries).
>
> If (b) bothers you because an unused Google button then sits on the subsidiary's sign-in page,
> clear **Show button** on the Google row alone. It stays accepted and the button
> disappears from that page — Yamada keeps signing in on the generic `/login` and switches
> tenants. You cannot clear it on every row, so the page is never left without buttons.
>
> The **same GitHub account** is fine either way: the deployment-wide GitHub button and the
> tenant's own GitHub button resolve to one person.
>
> There is also **(c): let Yamada link it**, an alternative to (a). If Yamada **already has an
> account in the subsidiary's GitHub org**, they sign in with the usual Google and press
> **Settings → Personal → Account → Add a sign-in method**. Either button then leads to the same
> account. Two conditions hold: only a method asserting **the same email address** can be added
> (accounts under different addresses are never merged), and they must **be a member of that
> organization** — linking is not a way around the entry rules. Somebody with no account on the
> other side cannot use this, so for them it is (a) or (b).

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

**Deleting a tenant you no longer need.** Admin panel → open the tenant → **Limits** →
*Delete tenant* (super_admin only). It only accepts a tenant that is already **empty**: it is
refused while a member is still on the roster, while a workspace row still exists, and while an
internal git repository is still there. That is deliberate — the database row is the only handle
left on a home, an EBS volume or a bare repository, so deleting it first would leave those
billing with nothing pointing at them. Work through the order instead: remove the members, then
destroy their workspaces, then delete the tenant.

> ⚠️ **Delete the internal git repositories while a member is still on the roster.** The screen
> that deletes them is reached through a membership, so once the last member is removed, nobody
> can get to it any more.

The audit log, cloud cost and occupancy of a deleted tenant are kept (their tenant column simply
goes blank). The reserved `golden snapshot (system)` tenant is not shown and cannot be deleted —
it belongs to the deployment itself and is recreated automatically.

For day-to-day operations after the build-out (backup, upgrades, shutdown), continue to
[02-operations.md](02-operations.md), and for security operations, to
[03-security.md](03-security.md).

# 05. Sign-in Methods — Setting Up an IdP End to End

English | [日本語](05-login-idp.ja.md)

This page is the **source of truth for configuring sign-in**: what to create on the IdP's side,
which values to write down, which key in `.env` (or which field in the Console) they go into,
how to confirm it worked, and what usually goes wrong. [01-install §3](01-install.md) gives the
one-paragraph version and points here; when the two disagree, **this page wins**.

Read it top to bottom for the IdP you are adding — one pass should be enough to get the door
open. Diagnosing a deployment that is already running belongs to
[04-troubleshooting.md](04-troubleshooting.md), which is symptom-driven; this page is
procedure-driven and does not repeat it.

For how the login works internally (the trust rules, how one person is recognised across two
IdPs), see [dev/07 §7.3.1](../../dev/07-security.md) and
[ADR 0043](../../decisions/0043-login-idp.md).

## 0. Before you start

- **The Control Plane runs the OAuth/OIDC flow itself** (`AUTH=oauth`, the default). You need a
  confidential client — one with a client secret — at every IdP you enable.
- **You can enable several at once.** The sign-in page shows one button per enabled method:
  Google first, then the OIDC providers in the order you listed them, GitHub last.
- **A broken IdP disables only itself.** A provider whose settings are incomplete is dropped
  with a warning in the CP log; the CP refuses to start only when *no* method is usable at all
  (and in the one unsafe Entra case in §4).
- **`AUTH=proxy` is the answer for a SAML-only IdP** (HENNGE One / TrustLogin / CloudGate and
  the like): bridge it with oauth2-proxy or Keycloak and let the gateway assert the email
  address. None of this page applies in that mode.
- Every `.env` change here takes effect at the **next CP start**. Sign-in methods a tenant
  registers in the Console (§7) need no restart at all.

## 1. The one thing that is the same for every IdP: the redirect URI

Register exactly this one **authorized redirect URI** (GitHub calls it the *authorization
callback URL*) at every IdP:

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

It must be a **character-for-character match** for `<PUBLIC_BASE_URL>/oauth2/callback` — same
scheme, same host, no trailing slash, no port unless `PUBLIC_BASE_URL` carries one. This is the
single most common mistake in the whole setup.

**This one URI does not multiply.** Ten providers, one URI. Which provider a callback belongs to
travels in a signed cookie, not in the URL, so there is nothing per-provider to register. The
same URI is also what a **tenant** registers in *its own* IdP for a method it defines itself
(§7) — it is your console's URL, never theirs.

If you ever change `PUBLIC_BASE_URL`, update the URI at every IdP in the same maintenance
window, or every sign-in fails at the IdP's own error page.

## 2. Where each setting lives

There are two kinds of sign-in method, and the most common confusion is that **the same
setting lives in a different place depending on which kind it is**.

- **A deployment-wide method** is one you configure in `.env`. It is offered to everybody, on
  every sign-in page.
- **A tenant-defined method** is registered by a tenant administrator in the Console and
  activated by you (§7). It belongs to one tenant and appears only on that tenant's own sign-in
  page.

| What you are configuring | Deployment-wide (`.env`) | A tenant's own method (Console) |
|---|---|---|
| The app / client registration | you create it at the IdP | the tenant creates it in *its* IdP |
| Redirect (callback) URI | `https://<PUBLIC_DOMAIN>/oauth2/callback` | the same one URI — yours, not theirs |
| Which methods exist | `AF_OIDC_PROVIDERS` (+ Google's and GitHub's own keys) | one row per method in **Sign-in methods** |
| Client ID / secret | `.env` | the form; stored encrypted with `AF_MASTER_KEY`, never shown again |
| Issuer | `AF_OIDC_<ID>_ISSUER` | **Issuer URL** |
| Why the email may be believed | `AF_OIDC_<ID>_TRUST` (no default) | **How the email is trusted** |
| Who may sign in, by email | `AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` / `_EMAILS_FILE`, or `AF_OIDC_<ID>_ALLOWED_*` for one provider | **Email domains to admit** — required, and there is no fallback to the deployment list |
| GitHub organizations | `AF_GITHUB_ALLOWED_ORGS` | **Allowed GitHub organizations** |
| Entra tenant ids | `AF_OIDC_<ID>_ALLOWED_TIDS` | **Allowed tenant ids** |
| Button label | `AF_OIDC_<ID>_LABEL_JA` / `_LABEL_EN` | **Button label**; the default names the company |
| One person across two app registrations | `AF_OIDC_<ID>_LINK_CLAIM` (any claim) | **How the same account is recognised** (`oid` only) |
| When it starts working | at the next CP start | when a deployment administrator **approves** it — no restart |
| Where its button appears | `/login` and every `/login/<slug>` | only on `/login/<slug>` |
| Its provider id (for **Sign-in methods** in a tenant's login rules) | the id you listed (`google`, `github`, `entra`, …) | `t:<tenant-slug>:<name>` |

Three things are **not** configurable anywhere and are worth knowing before you go looking for
them: Google's endpoints and its trust rule, GitHub's `github.com` / `api.github.com` hosts, and
the scopes each adapter requests.

## 3. Google

### 3.1 What to create in Google Cloud Console

1. Pick (or create) the project this client should live in.
2. Configure the **OAuth consent screen** for that project. If your organization is on Google
   Workspace and only your own domain should ever see this screen, choose the **Internal** user
   type; otherwise it is **External**. This bounds who *reaches* the screen — it is not the
   allowlist, which is §2's email settings.
3. **Credentials → Create credentials → OAuth client ID**, application type **Web application**.
4. Under *Authorized redirect URIs*, add the one URI from §1. (An *authorized JavaScript
   origin* is not needed — the CP is not a browser client.)
5. Create it, and copy the **client ID** and **client secret** now.

### 3.2 The values, and where they go

```sh
GOOGLE_OAUTH_CLIENT_ID=<client-id>.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=<client-secret>
```

That is all. Google keeps its historical variable names, so there is **no** `AF_OIDC_GOOGLE_*`
and you do **not** list it in `AF_OIDC_PROVIDERS` — setting both keys above is what enables it,
and its provider id is `google`. Its issuer, endpoints and trust rule are built in: the CP
performs no discovery request for Google, and the rule is always "the address must carry
Google's own `email_verified`". The scope requested is `openid email`.

### 3.3 Common failures

- **`redirect_uri_mismatch`** — the URI in the client does not match §1 exactly. Google compares
  the whole string.
- **Set only one of the two keys** — the button silently does not appear, and the CP log says
  `google login disabled — set both GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET`.
- **Google accepts the sign-in but Agent Fleet rejects it** — that is the allowlist, not Google.
  See §8 and [04 "Cannot log in"](04-troubleshooting.md).
- **An External consent screen still in testing** admits only the accounts registered as test
  users; everybody else is stopped by Google before the CP ever sees them.
- Remember that a personal `gmail.com` address passes Google's verification perfectly well. What
  keeps this button to your company is `AF_OAUTH_ALLOWED_DOMAINS` (or the per-provider
  `AF_OIDC_*_ALLOWED_DOMAINS`), never the fact that the IdP is Google.

## 4. Microsoft Entra ID

### 4.1 What to create in the Entra admin center

In **Microsoft Entra ID → App registrations → New registration**:

1. Give it a name (it is shown on the consent screen), and for *supported account types* choose
   **accounts in this organizational directory only** — the single-tenant option. That is the
   setting that matches pinning the issuer in §4.2, and choosing a multi-tenant option here is
   how deployments end up refusing to start.
2. Add a redirect URI on the **Web** platform, with the URI from §1. The platform matters: a
   *single-page application* registration cannot exchange a code with a client secret, and the
   failure appears only at the first sign-in.
3. **Certificates & secrets → New client secret.** Copy the secret's **Value** immediately — it
   is displayed once, and the *Secret ID* next to it is not the secret. **Note its expiry date
   and put it in your calendar**: an expired secret fails every sign-in through this button at
   the token exchange, with nothing wrong on the Agent Fleet side.
4. From the registration's overview, write down the **Directory (tenant) ID** and the
   **Application (client) ID** — both GUIDs.
5. The delegated permissions this needs are the standard OIDC ones (`openid`, `email`,
   `profile`). Directories that have turned off user consent need an administrator to grant them
   once.

### 4.2 The values, and where they go

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<directory-tenant-id>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<the secret VALUE from step 3>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

- **The id you list is what the other variable names are built from.** `AF_OIDC_PROVIDERS=entra`
  means `AF_OIDC_ENTRA_*`. An id may use `a-z 0-9 - _` (up to 32 characters, starting with a
  letter or digit), and a `-` becomes `_` in the variable name — id `entra-id` reads
  `AF_OIDC_ENTRA_ID_*`. The id is also the name you type into a tenant's **Sign-in methods**
  rule, so keep it short and stable.
- **`AF_OIDC_<ID>_TRUST` has no default, deliberately.** It records *why* this IdP's email
  address may be believed, because the allowlist is written in email addresses.
  `email_verified` accepts only addresses the IdP itself marks verified; `issuer` says the
  issuer is pinned to a single directory, so that directory's addresses are authoritative.
  **Entra never emits `email_verified`**, so `issuer` is the value there — with `email_verified`
  every Entra sign-in would be refused as unverified. A provider with no `_TRUST` is disabled at
  startup rather than guessed at.
- **Pin the issuer to your own tenant GUID.** With a `/common/`, `/organizations/` or
  `/consumers/` issuer, everyone on earth with a Microsoft account reaches your login screen —
  and a personal Microsoft account can change its own email address, which would make the
  allowlist meaningless. **The CP refuses to start** on those endpoints unless
  `AF_OIDC_ENTRA_ALLOWED_TIDS` lists the tenant GUIDs you accept. This is the one IdP mistake
  that is fatal rather than merely disabling a button.
- Optional, and rarely needed: `AF_OIDC_ENTRA_SCOPES` (default `openid email profile`) and
  `AF_OIDC_ENTRA_PROMPT` (default `select_account`; `none` omits the parameter entirely for an
  IdP that rejects it). `AF_OIDC_ENTRA_ALLOWED_EMAILS` / `_ALLOWED_DOMAINS` narrow the allowlist
  for this provider alone — when set, they **replace** the deployment-wide list for it rather
  than adding to it.

The CP reads `<issuer>/.well-known/openid-configuration` lazily, on the first sign-in through
this button, and caches it for 24 hours. So a wrong issuer does not show up at startup: it shows
up as an error the first time somebody presses the button.

### 4.3 One directory behind two app registrations

When two app registrations point at the same Entra directory — this deployment's own button and
a tenant that registered its own method (§7), say — **Entra's `sub` differs per app
registration**, so one person pressing one button and then the other looks like two people: two
accounts, two workspaces. Naming a stable claim makes them one:

```sh
AF_OIDC_ENTRA_LINK_CLAIM=oid
```

`oid` is the person's object id within that directory: the same value in every app registration,
and not something anybody can choose. That last part is the whole point — see the warning in
§7.1 before naming anything else, and [ADR 0043](../../decisions/0043-login-idp.md) (decision 38)
for why this is an *additional* key rather than a replacement for `sub`.

### 4.4 Common failures

- **The CP exits at startup complaining about a multi-tenant endpoint** — §4.2, the issuer.
- **Entra shows its own error page with an `AADSTS…` code.** The two you will actually meet are
  an invalid client secret (the *Secret ID* was pasted instead of the *Value*, or the secret has
  expired) and a redirect-URI mismatch (registered under the wrong platform, or not
  character-for-character). Both are settings on the Entra side; nothing in `.env` will fix
  them.
- **`no email claim from id_token or userinfo` in the CP log** — the account has no mail address
  in the directory, and its `preferred_username` / `upn` did not look like one either. Add the
  `email` optional claim to the ID token in the app registration, or give the account a mail
  attribute.
- **`discovery …: issuer mismatch (got "…")`** — the issuer you configured is not the one the
  IdP advertises. Copy it out of the discovery document rather than typing it.
- **Everybody is refused after some months of working fine** — the client secret expired.

## 5. GitHub

GitHub has no OIDC for user sign-in, so it is a separate adapter with its own settings. What
authorizes a GitHub sign-in is **active membership in an organization you name** — not merely
holding a GitHub account.

### 5.1 What to create on GitHub

1. Create an **OAuth App** (not a GitHub App): *Settings → Developer settings → OAuth Apps → New
   OAuth App*. Create it under the **organization** if you want the org to own it rather than a
   person.
2. Set the **Authorization callback URL** to the URI from §1. The homepage URL can be your
   console's URL.
3. Generate a **client secret** and copy it — it is shown once.
4. **If your organization restricts third-party application access, an organization owner must
   approve this OAuth app for the organization** — in the organization's settings, under its
   third-party application access policy. Until they do, the membership check can see nothing
   and **every** GitHub sign-in is refused, with settings that look entirely correct. This is
   the trap in §5.3.

You may reuse the OAuth App that already backs the Console's GitHub **Connect** button
(`GITHUB_OAUTH_CLIENT_ID`); it only needs the callback URL added. Scopes are granted per
authorization, so the same app can serve the git device flow (`repo`) and the login
(`read:org user:email`) without interfering. Use a separate app when you would rather not have
one org approval cover both flows.

### 5.2 The values, and where they go

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # required — and what turns the button on
GITHUB_OAUTH_CLIENT_ID=<client-id>
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # strongly recommended, see below
```

- **`AF_GITHUB_ALLOWED_ORGS` is what enables the button**, not the client id. A deployment that
  has only ever used `GITHUB_OAUTH_CLIENT_ID` for the git-connect device flow keeps working
  exactly as before, and is not nagged about a login it never asked for. With the list empty,
  the log says `github login disabled — AF_GITHUB_ALLOWED_ORGS is required`.
- For an OAuth App used **only** for signing in, set `AF_GITHUB_LOGIN_CLIENT_ID` /
  `AF_GITHUB_LOGIN_CLIENT_SECRET` instead; they take precedence over the `GITHUB_OAUTH_*` pair.
- **Set `AF_GITHUB_ALLOWED_DOMAINS` as well.** GitHub hands the CP the account's *primary
  verified* address, which for most people is a personal one — and here a different address is a
  different person, so they land in a **new empty workspace** instead of their own. Turning them
  away at the door is kinder than letting them work somewhere they did not mean to be. Without
  any email list at all the organizations are the whole gate, and the CP says so at startup.
- Optional: `AF_GITHUB_ALLOWED_EMAILS`, `AF_GITHUB_LABEL_JA` / `_LABEL_EN`,
  `AF_GITHUB_MEMBERSHIP_TTL` (default `10m`) and `AF_GITHUB_MEMBERSHIP_GRACE` (default `1h`).

### 5.3 Two behaviours worth knowing in advance

**The org-approval trap.** An organization that restricts third-party OAuth apps hides its
membership from an unapproved app, and the CP cannot tell "not a member" from "not allowed to
look." The result is that everybody is refused while every value you set is correct. The log
line is explicit:

```
WARNING: github: org "acme" returned 403 for a membership check — if the org restricts
third-party OAuth apps, an org owner must approve this OAuth app before anyone can sign in
```

**People are asked to sign in again after a CP restart.** Membership is re-checked through the
GitHub API on every request, so a positive answer is cached per person for the TTL, with the
person's token beside it; if GitHub is unreachable at refresh time the last positive answer
stands for the grace window and is refused after it. That cache is **in memory**, so a restart
leaves nothing to re-verify anybody with. They are asked to sign in again rather than told they
are not allowed, and with a live GitHub session the round trip is usually invisible. This is
expected, not a fault.

### 5.4 Common failures

- **Every sign-in refused, settings look right** — §5.3, the org approval.
- **One person refused** — their primary verified GitHub address is outside
  `AF_GITHUB_ALLOWED_DOMAINS`, or their membership of the org is still *pending* rather than
  active. They can make the company address primary and verify it on GitHub, or use another
  button.
- **"The GitHub account has no primary verified email address"** — exactly that: the account
  has no verified primary address to hand over.
- **A member renamed their GitHub account and lost nothing** — correct. Identity is keyed on the
  numeric account id, not the username, precisely because a released username can be claimed by
  somebody else.

## 6. Okta, Keycloak, Auth0, Cognito, GitLab, and other OIDC IdPs

They all ride on the same generic OIDC client, so the procedure is §4 with a different issuer:
register a **confidential web application** with the redirect URI from §1, take its client ID
and secret, and add five lines to `.env`.

```sh
AF_OIDC_PROVIDERS=okta
AF_OIDC_OKTA_ISSUER=https://<your-org>.okta.com
AF_OIDC_OKTA_CLIENT_ID=<client-id>
AF_OIDC_OKTA_CLIENT_SECRET=<client-secret>
AF_OIDC_OKTA_TRUST=email_verified
```

Issuer shapes, so you know what you are looking for in the IdP's own admin UI:

| IdP | Issuer looks like |
|---|---|
| Okta | `https://<org>.okta.com`, or `https://<org>.okta.com/oauth2/<authorization-server-id>` when you use a custom authorization server |
| Keycloak | `https://kc.example.com/realms/<realm>` |
| Auth0 | `https://<tenant>.<region>.auth0.com/` |
| Cognito | `https://cognito-idp.<region>.amazonaws.com/<user-pool-id>` |
| GitLab | `https://gitlab.com`, or your self-managed base URL |

- **Take the issuer from the IdP's own discovery document**, not from the browser address bar.
  The CP appends `/.well-known/openid-configuration` to it and then requires the `issuer` inside
  that document to be the same value; a mismatch fails the sign-in with
  `discovery …: issuer mismatch (got "…")`. A trailing slash is not a mismatch — both sides are
  compared with it stripped.
- **`_TRUST`**: `email_verified` when the IdP actually asserts that claim (most Okta / Keycloak /
  Auth0 setups do), `issuer` when it does not but the issuer is pinned to one directory. If you
  are not sure, start with `email_verified`: the failure mode is a refused sign-in you can read
  in the log, not a sign-in that should not have happened.
- `https` is required. `http` is accepted only for `localhost` / `127.0.0.1` / `::1`, so that a
  local Keycloak or Dex can be used while developing; the CP warns loudly when it is.
- List several ids to offer several buttons: `AF_OIDC_PROVIDERS=entra,okta`.

## 7. A sign-in method a tenant defines itself

Some tenants have an identity source of their own: a different Entra ID (or Okta / Keycloak)
tenant, with its own issuer, client ID and secret, or a GitHub organization instead. A group
subsidiary is the obvious case, but so is a business still being merged, an outsourcing partner,
or a division that simply runs its own directory. Rather than editing `.env` and restarting the
CP for each one, that tenant's own administrator registers it from the Console, and **you approve
it**. Nothing here needs a restart.

Where it is: **Tenant settings → Sign-in → Sign-in methods** for the tenant's own administrator
(the account menu's *Tenant settings*). As a deployment administrator you reach the same panel
from **Admin → the tenant → Sign-in methods**, and the deployment-wide **Sign-in method
register** in the rail (*Tenant-defined sign-in methods*) carries the approve and suspend
buttons for every tenant at once.

> Whether to split into tenants at all, and how a tenant's login rules interact with people who
> belong to two tenants, is a decision — it stays in [01-install §4](01-install.md).

### 7.1 What the tenant administrator fills in

Their work at their own IdP is §3–§6, on their side — **with your callback URL**
(`https://<PUBLIC_DOMAIN>/oauth2/callback`, the same single URI from §1). That is the step people
get wrong: it is not their console, it is yours.

| Field | What goes in it |
|---|---|
| **Name** | the identifier within this tenant (`a-z 0-9 - _`), e.g. `entra`. The deployment-wide id becomes `t:<tenant-slug>:<name>` |
| **Kind of sign-in** | *Our own IdP* (OIDC) or *A GitHub organization* (§7.3) |
| **Issuer URL** | OIDC only; the same rules as §4.2, including the refusal of `common` / `organizations` without tenant ids |
| **Client ID / Client secret** | from their own app registration. The secret is encrypted on save and never shown again; leaving it blank on a later edit keeps the stored one |
| **How the email is trusted** | *The issuer is pinned to our own tenant* (Entra) or *The IdP asserts email_verified* |
| **Email domains to admit** | **required.** A tenant-defined method does not fall back to the deployment-wide allowlist, so an empty list would admit nobody. It also bounds which addresses this issuer may assert |
| **Allowed tenant ids** | Entra `tid`s; required when the issuer is a multi-tenant endpoint |
| **How the same account is recognised** | `oid`, when the same issuer already serves this deployment through a different app registration (§4.3). This is the only value a tenant may name |
| **Button label** | optional; the generated default already names the company, so the row does not produce a button reading the same as yours |

> **Why a tenant may only name `oid` here, while `AF_OIDC_<ID>_LINK_CLAIM` accepts any claim.**
> `oid` is assigned by the directory and nobody can choose it. An *asserted* value — `email`,
> `upn`, `preferred_username` — would let any method sharing that issuer **land on an existing
> account** merely by asserting it. The env variable is not restricted because it is your own
> declaration about your own deployment; that does not make naming `email` there a good idea.

### 7.2 What you check before approving

A new or edited method is created **waiting for approval**. Until a deployment administrator
activates it, no button appears on the tenant's sign-in page and **no session is issued** even to
somebody who constructs the sign-in link by hand — hiding the button is presentation, and
presentation is never the enforcement here.

That step is not bureaucracy: registering an IdP is the power to declare *who somebody is*, and
on this deployment a person is identified by their email address, deployment-wide, including who
is a deployment administrator. An administrator who could activate their own issuer could issue
themselves a token carrying **your** address.

Read two things on the row before you approve it:

- **The issuer really is that company's own tenant** — not a `common` / `organizations`
  endpoint. (For a GitHub row, read the organizations instead; the issuer is `github.com` for
  everybody and tells you nothing.)
- **The email domains are theirs.** The approval is *for that scope*: this list bounds which
  addresses the issuer may assert, and **one domain belongs to one tenant** — saving a row that
  claims a domain another tenant already holds is refused with
  `domain … is already claimed by the sign-in method of tenant …`. Never approve a method that
  claims a domain belonging to the rest of the deployment.

Approval is refused, with the reason, if the row could not actually be built into a working
method (a bad issuer, an empty domain list, a client secret that can no longer be decrypted) —
an approved row that shows no button is indistinguishable from one that was never approved, so
the CP will not record one.

**What sends an approved method back to the queue**: changing the issuer, the client ID, the
trust rule, the kind, or how the same account is recognised — or **adding** a domain, a tenant id
or a GitHub organization. Narrowing any of those lists does not, and neither does rotating the
client secret (forcing re-approval on a routine rotation would only teach people not to rotate).
**Suspending is always available**, to the tenant's own administrator as well: stopping should
never wait for you.

Approved methods stay on the register with who approved them and when. Treat it as a register to
re-read now and then, not a queue that empties: the IdP stays under the other company's control,
and settings such as self-sign-up can be turned on after you approved it.

### 7.3 When the tenant uses a GitHub organization

Choosing *A GitHub organization* as the kind replaces the issuer field with **Allowed GitHub
organizations**. `github.com` is one issuer for the whole world, so active membership of that
organization is what makes a sign-in theirs.

- **The tenant brings its own OAuth App**, created in its own organization with your callback
  URL (§1) — sharing yours would make every such tenant's owner approve the app your git device
  flow also uses. **Your `.env` needs no GitHub settings at all**: the env-level GitHub login can
  stay off while a tenant's own GitHub method works.
- **The org-approval trap of §5.3 applies here too**, and it is the tenant's org owner who has
  to act.
- **Email domains are required here as well**, for the same one-domain-one-tenant reason, and
  because somebody whose primary GitHub address is outside the company domain would land in a
  new workspace rather than their existing one.
- Two tenants may register the same organization. Who lands where is decided by the email
  domain, and that is still one tenant only.
- The deployment-wide GitHub button and a tenant's own GitHub button **resolve to one person**
  for the same GitHub account.

## 8. Check that it worked

Work down this list; each step tells you something the next one assumes.

1. **Which methods were built at all.**

   ```sh
   docker compose logs cp | grep "login providers:"
   ```

   One line, listing every enabled deployment-wide provider id in button order. An id that is
   missing here was never built, and no amount of looking at the sign-in page will explain why.

2. **Why one is missing.**

   ```sh
   docker compose logs cp | grep -i "login provider"
   ```

   Each `WARNING: login provider "…" disabled — …` names the exact setting that was missing or
   invalid (most often `AF_OIDC_<ID>_TRUST`, which has no default on purpose). The Google and
   GitHub adapters have their own lines, quoted in §3.3 and §5.2.

3. **The CP did not start at all.** Two messages are fatal by design: the multi-tenant Entra
   issuer (§4.2), and `AUTH=oauth requires AF_COOKIE_SECRET, PUBLIC_BASE_URL and at least one
   login provider …`, which means nothing usable was configured.

4. **Open `https://<PUBLIC_DOMAIN>/login`.** Count the buttons: one per id from step 1, Google
   first, GitHub last. A page saying no sign-in method is configured means step 1 found nothing.

5. **Open `https://<PUBLIC_DOMAIN>/login/<slug>`** for a tenant. It shows the deployment-wide
   buttons narrowed by that tenant's **Sign-in methods** rule, minus anything listed under
   **Methods to keep off the sign-in page**, plus that tenant's own **approved** methods at the
   end. If an approved method has no button, the CP log says why:

   ```sh
   docker compose logs cp | grep -i "tenant login provider"
   ```

   (Hiding every button is ignored, so this page is never left empty. An unknown slug quietly
   renders the generic page — that is deliberate, not a 404 you should chase.)

6. **GitHub only**: `docker compose logs cp | grep "returned 403"` before you conclude anything
   about a rejected sign-in (§5.3).

7. **Sign in once, end to end**, with an account you expect to be admitted. If you land on a
   page saying **a new workspace was created**, the address you signed in with is not one this
   deployment has seen — which is exactly the warning you want before people start working in
   the wrong place. Accounts under different addresses cannot be merged afterwards; somebody who
   holds both can add the second method themselves under **Settings → Personal → Account → Add a
   sign-in method** (only a method asserting the same address, and its own org / domain rules
   still apply).

## 9. When it still does not work

Symptom-driven diagnosis lives in [04-troubleshooting.md](04-troubleshooting.md) — go to
**"Cannot log in"** there, which covers the rejected sign-in, the redirect URI mismatch, the
missing button, the multi-tenant issuer, the GitHub cases, and cookies not being saved over
plain HTTP. The per-IdP mistakes are §3.3, §4.4 and §5.4 above.

Two things that are *not* faults, and are asked about often enough to repeat here: GitHub users
being asked to sign in again after a CP restart (§5.3), and a tenant's approved method having no
button on the plain `/login` (that is by design — its button lives on `/login/<slug>` only).

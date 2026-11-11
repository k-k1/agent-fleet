---
audience: "someone standing a deployment up for the first time"
source_of_truth: "the \"Quick start\" of deploy/compose/README.md for commands, deploy/compose/.env.example for what each variable means (a contradiction is a bug in this page)"
---

# Install Agent Fleet

English | [日本語](install.ja.md)

Stand up your first deployment with Docker Compose, up to the point where the first administrator signs in and can welcome members.
The commands are in the "Quick start" of the [compose runbook](../../deploy/compose/README.md). This page says what each step decides and what to watch for.

## How it works

| What you do | What happens |
|---|---|
| Prepare `.env` and generate the secrets | The Control Plane starts with these values and encrypts the credentials it stores with `AF_MASTER_KEY` |
| Register an app at your IdP and decide the allowlist | The sign-in page shows a button for each enabled method |
| Start it | The Control Plane and Caddy come up, and Caddy obtains the TLS certificate |
| Sign in with an address in `SUPER_ADMIN_EMAILS` | That account becomes the deployment administrator (`super_admin`) |
| Welcome members | When a member signs in, their workspace is prepared |

You work in `deploy/compose/`. For another target, choose it in [Choose where to deploy](choose.md) and follow its runbook.

## Before you start

| What you need | Notes |
|---|---|
| A Linux host running Docker Engine and `docker compose` | One host is enough |
| A public domain, and DNS A / AAAA records pointing at this host | For TLS. If it is internal only, decide [whether to use `tls internal`](install-decisions.md#whether-to-use-tls-internal) first |
| A client at the sign-in IdP | Created in step 3: a Google OAuth 2.0 web client, or an OIDC app at Entra ID, Okta, Keycloak, Auth0, Cognito or GitLab |
| Agent seats | Each member brings their own later, so you do not need them now |

## 1. Prepare the configuration file

Copy `deploy/compose/.env.example` to `.env` and edit it.

- `.env` is not tracked by git, and all the configuration goes there
- What each variable means and how to produce it is written in `.env.example` itself

Fill in these first:

| Variable | What goes in |
|---|---|
| `PUBLIC_DOMAIN`, `PUBLIC_BASE_URL` | The public URL |
| The IdP's client ID and secret | Step 3 |
| `AF_OAUTH_ALLOWED_DOMAINS` and the like | The sign-in allowlist |
| `SUPER_ADMIN_EMAILS` | The first deployment administrator's address |
| `DATA_DIR` | Where the data goes. Its absolute path is hard to change later, so decide it now |
| `AF_MASTER_KEY`, `AF_COOKIE_SECRET` | Step 2 |
| `DOCKER_GID` | The host's docker group GID. The runbook shows how to find it |

A wrong `DOCKER_GID` gives permission denied on the docker socket after start-up ([06 Diagnosing it](06-diagnose.md)).

## 2. Generate the secrets

`.env` has two secrets you generate yourself.

| Secret | Role |
|---|---|
| `AF_MASTER_KEY` | The root key that encrypts every credential |
| `AF_COOKIE_SECRET` | The key that signs sign-in session cookies |

Make both **at least 32 random bytes**. Never use a passphrase.

```
head -c 32 /dev/urandom | base64
```

- A shorter value, or one that only repeats a pattern, still starts, but logs a `WARNING` and shows a warning banner in the administration screen. A later release will refuse to start with it
- The requirements, and what changing each key costs, are in [04 Securing it](04-secure.md#the-deployment-keys-requirements-and-what-changing-them-costs)

> [!IMPORTANT]
> **The moment you generate `AF_MASTER_KEY`, record it in a password vault or secret manager, kept apart from the data.**
> The key goes into neither `DATA_DIR` nor any backup. Lose it and the stored credentials and every backup become permanently undecryptable.
> A restore needs the same key.

## 3. Configure the sign-in IdP

What to create at each IdP, which values to enter and how to check it are in [05 Sign-in methods](05-signin.md). Finish that before you go on to step 4.

At the IdP, register exactly one **authorized redirect URI**, however many IdPs you enable:

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

Unless it matches `<PUBLIC_BASE_URL>/oauth2/callback` exactly, sign-in fails with "redirect URI mismatch".

Each IdP fills different keys in `.env`:

| IdP | Keys in `.env` |
|---|---|
| Google | `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`. Not listed in `AF_OIDC_PROVIDERS` |
| Entra ID, Okta, Keycloak, Auth0, Cognito, GitLab | `AF_OIDC_PROVIDERS=<id>`, plus `AF_OIDC_<ID>_ISSUER`, `_CLIENT_ID`, `_CLIENT_SECRET`, `_TRUST` |
| GitHub | `AF_GITHUB_ALLOWED_ORGS` (required, and also what turns the button on), `GITHUB_OAUTH_CLIENT_ID`, `_SECRET`, and `AF_GITHUB_ALLOWED_DOMAINS` |

Three things decide whether it works:

- **`AF_OIDC_<ID>_TRUST` has no default.** It states why this IdP's email address may be believed, and an IdP without it is disabled at start-up. Entra ID never returns `email_verified`, so give it `issuer`
- **Pin the Entra ID issuer to your own tenant GUID.** On a `/common/` or `/organizations/` endpoint the Control Plane refuses to start unless `AF_OIDC_<ID>_ALLOWED_TIDS` names the tenants you accept
- **If the GitHub organization restricts third-party apps, the organization must also approve the OAuth App.** Until it does, everyone is refused even though the settings look right

When you enable several IdPs:

- List them, as in `AF_OIDC_PROVIDERS=entra,okta`, and the sign-in page shows a button for each
- An IdP with incomplete settings is only disabled, with a warning in the log, so one mistake does not lock everyone out
- The Control Plane refuses to start only when no IdP at all is usable

### When you use an authentication gateway in front

- Sign-in done by the Control Plane itself over OAuth / OIDC is recommended (`AUTH=oauth`). `AUTH` has no default, so always set it
- If you already have an authentication gateway, such as oauth2-proxy or the ALB's OIDC, you can choose `AUTH=proxy`. People are identified by the email address in the header the gateway adds
- An IdP that speaks only SAML (HENNGE One, TrustLogin, CloudGate and the like) is bridged with oauth2-proxy or Keycloak and used with `AUTH=proxy`

### What you do not set here

The GitHub and Bitbucket "Connect with OAuth" apps for cloning repositories are not per deployment: a tenant administrator registers them per tenant, in the Console.

- `GITHUB_OAUTH_CLIENT_ID` in `.env` is the sign-in app
- `BITBUCKET_OAUTH_KEY` and `_SECRET` are not read
- Pasting a token works without them, so you can skip them at install time. The steps are in [Choose the OAuth apps for integrations](../admin/access/oauth-apps.md)

## 4. Choose the tenant and provisioning model

Before starting, decide these three. How to choose is in [Choose the tenant and provisioning model](install-decisions.md).

- Whether to use `tls internal`
- Whether `AF_PROVISION` is `invite` or `auto`
- One tenant, or several

## 5. Start it

1. Create `DATA_DIR`
2. Start it with `docker compose up -d`. Add `--build` to build locally instead of using the prebuilt image
3. Follow the Control Plane's log, and check that `/healthz` on its loopback port answers `ok`

The exact check is in the runbook's troubleshooting table.

## 6. Sign in as the first administrator

Open `https://<PUBLIC_DOMAIN>` in a browser and sign in with an account listed in `SUPER_ADMIN_EMAILS`.

- That address becomes the deployment administrator (`super_admin`) on its first sign-in
- A deployment administrator sees the shield icon **Admin** in the Console, from which the whole deployment is managed
- A `SUPER_ADMIN_EMAILS` account reaches **Admin** without being a member of any tenant, so starting with `AF_PROVISION=invite` is no problem

> [!WARNING]
> **Fill in at least one allowlist.**
> With `AF_OAUTH_ALLOWED_EMAILS`, `_DOMAINS` and `_EMAILS_FILE` all empty and nobody invited to a tenant yet, every sign-in is refused.
> Right after installing, nobody is invited, so not even the first administrator gets in.

## 7. Welcome members

From **Admin** you can create tenants, add members, and set limits and idle stop.

- With one tenant, there is no tenant to create
- With `AF_PROVISION=auto`, members on the allowlist start just by signing in
- After starting their workspace, each member **logs in with their own agent seat** in the Console. The operator never sets up a member's credentials for them
- The screens for members, limits and audit are in [Administering a tenant](../admin/README.md)

### Names a tenant slug cannot take

A tenant's directory and a default-tenant member's home sit directly under the data directory (`WS_DATA`), next to the Control Plane's own files. So a tenant or member whose name would collide cannot be created.

| What you create | The name it collides with | Error |
|---|---|---|
| A tenant | A name the deployment uses (`git`, `shared`, `caddy`, `drawio-stencils`, `guide-staged`, the database and key files) | `tenant_slug_reserved` |
| A tenant | The user key of a default-tenant member (the email address made safe, for example `alice-example-com`) | `tenant_slug_conflict` |
| A default-tenant member | A name the deployment uses | `user_key_reserved` |
| A default-tenant member | A tenant's slug | `user_key_conflict` |

- Invite somebody who cannot be added to the default tenant into another tenant
- Names stored before this check are left as they are, and logged at Control Plane start as `data root name collision`

## Delete a tenant you no longer need

**Admin** → open the tenant → **Delete tenant** at the bottom of "Limits & idle". Only a deployment administrator can delete one.

Only a tenant that is **already empty** can be deleted. Clear it in this order:

1. Delete the internal git repositories
2. Remove the members
3. Destroy their workspaces
4. Delete the tenant

- Deletion is refused while a member, a workspace or an internal git repository is left. The database row can be the only handle on a home, an EBS volume or a repository; deleting it first would leave those billing with nothing pointing at them
- Delete the internal git repositories while a member is still on the roster. The screen that deletes them is reached only as a member, so once the last one is removed nobody can get there
- The audit log, cloud cost and occupancy are kept; their tenant column simply goes blank
- The `golden snapshot (system)` tenant is not listed and cannot be deleted. It belongs to the deployment and is recreated automatically

## When it goes wrong

| Symptom | Where to look |
|---|---|
| "redirect URI mismatch" at sign-in | Whether the URI registered at the IdP matches `<PUBLIC_BASE_URL>/oauth2/callback` exactly (step 3) |
| Sign-in is always refused | Whether the allowlists are all empty (step 6) |
| Permission denied on the docker socket after start-up | `DOCKER_GID` (step 1) |
| `/healthz` does not answer `ok`, or the Control Plane does not come up | "CP does not start" in [06 Diagnosing it](06-diagnose.md#symptom-based-checklist) |

## Related

- [Choose the tenant and provisioning model](install-decisions.md)
- [05 Sign-in methods](05-signin.md)
- [03 Running it](03-run.md): backup, upgrades, stopping
- [04 Securing it](04-secure.md)

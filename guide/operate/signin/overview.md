---
audience: "a deployment administrator who connects an IdP to the deployment (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in settings, as it reads them from `.env` (where this page and the code disagree, the page is wrong)"
---

# Set up how people sign in

English | [日本語](overview.ja.md)

Connect Google, Microsoft Entra ID, GitHub or another OIDC IdP to the deployment and put its button on the sign-in page.
This page covers what every IdP has in common and how to check that it worked.

## How it works

| What you do | What the Control Plane does |
|---|---|
| Create an app (client) at the IdP and register one redirect URI | |
| Put the values in `.env` and restart the Control Plane | Reads the settings at startup and builds a button for each usable IdP |
| | Leaves out only an IdP whose settings are incomplete, and logs why |
| Approve an IdP a tenant registered | Shows the approved IdP's button on that tenant's own sign-in page |

The Control Plane runs the sign-in flow (OAuth / OIDC) itself. That is `AUTH=oauth` in `.env`; `AUTH` has no default.

## Before you start

- Each IdP you enable needs a **confidential client**, one with a client secret
- **You can enable as many as you like.** The buttons run Google first, then the OIDC IdPs in the order listed in `AF_OIDC_PROVIDERS`, then GitHub last
- **An IdP with a settings problem disables only that IdP.** The Control Plane refuses to start only when no method is usable, or when an Entra issuer is a dangerous one ([Sign in with Microsoft Entra ID](entra.md))
- The Control Plane does not speak **SAML-only IdPs** (HENNGE One, TrustLogin, CloudGate and the like) or LDAP. Put oauth2-proxy or Keycloak in front and have `AUTH=proxy` trust the email address that gateway sets. This chapter does not apply in that mode
- A change to `.env` takes effect **the next time the Control Plane starts**. An IdP a tenant registers from the Console takes effect when you approve it

## Register the redirect URI

Register this one URI at every IdP as the **authorized redirect URI** (on GitHub, the *Authorization callback URL*):

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

- It must match `<PUBLIC_BASE_URL>/oauth2/callback` **character for character**: same scheme, same host, no trailing slash, and no port unless `PUBLIC_BASE_URL` has one
- It stays one URI however many IdPs you enable. Which IdP a callback belongs to travels in a signed cookie, not in the URL
- A tenant registers this same URI at its own IdP. It is **this deployment's** URL, not the tenant's
- When you change `PUBLIC_BASE_URL`, fix the URI at every IdP in the same change. Otherwise everybody stops at the IdP's error page

This is the most common setup failure.

## Which setting goes where

There are two kinds of sign-in method, and the same setting lives in a different place for each.

- **A deployment-wide method**: you write it in `.env`. It appears on every sign-in page
- **A method a tenant registers**: a tenant administrator registers it in the Console, and it works once you approve it. It appears only on that tenant's own sign-in page ([Approve a tenant's own sign-in](tenant-idp.md))

| What you set | Deployment-wide (`.env`) | Registered by a tenant (Console) |
|---|---|---|
| The app registration | You create it at the IdP | The tenant creates it at its own IdP |
| The redirect URI | `https://<PUBLIC_DOMAIN>/oauth2/callback` | The same one URI |
| Which methods exist | `AF_OIDC_PROVIDERS` (Google and GitHub have their own keys) | One row of **Sign-in methods** per method |
| client_id and client_secret | `.env` | Input fields. Encrypted with `AF_MASTER_KEY` and never shown again |
| The issuer | `AF_OIDC_<ID>_ISSUER` | **Issuer URL** |
| Why the email address can be trusted | `AF_OIDC_<ID>_TRUST` (no default) | **How the email is trusted** |
| Who may enter | `AF_OAUTH_ALLOWED_EMAILS`, `_DOMAINS`, `_EMAILS_FILE`, or the per-IdP `AF_OIDC_<ID>_ALLOWED_*` | **Email domains to admit** (required; the deployment-wide allowlist is not used) |
| GitHub organisations | `AF_GITHUB_ALLOWED_ORGS` | **Allowed GitHub organizations** |
| Entra tenant ids | `AF_OIDC_<ID>_ALLOWED_TIDS` | **Allowed tenant ids (Entra tid, optional)** |
| The button label | `AF_OIDC_<ID>_LABEL_JA`, `_LABEL_EN` | **Button label (Japanese)**, **Button label (English)** |
| Treating one person as the same across app registrations | `AF_OIDC_<ID>_LINK_CLAIM` (any claim) | **How the same account is recognised** (`oid` only) |
| When it takes effect | The next time the Control Plane starts | When you approve it |
| Where the button appears | `/login` and every `/login/<slug>` | `/login/<slug>` only |
| How the tenant's list shows it | Marked "**Deployment-wide**"; the tenant cannot edit it | A row the tenant created and can edit |

Three things have no setting, however hard you look:

- Google's endpoints and trust rule
- GitHub's `github.com` and `api.github.com`
- The scopes asked of Google and GitHub (an OIDC IdP can change them with `AF_OIDC_<ID>_SCOPES`, but you rarely need to)

## Set up each IdP

| IdP | Page |
|---|---|
| Google | [Sign in with Google](google.md) |
| Microsoft Entra ID | [Sign in with Microsoft Entra ID](entra.md) |
| A GitHub organisation | [Sign in with GitHub](github.md) |
| Okta, Keycloak, Auth0, Cognito, GitLab and others | [Sign in with another OIDC IdP](oidc.md) |
| One a tenant registered itself | [Approve a tenant's own sign-in](tenant-idp.md) |

## Check that it worked

Work down the list; each step assumes the one before. The commands are the compose ones. On another target, read the Control Plane's log
the way that target's runbook says (they open from [Operating a deployment](../README.md)).

1. **See which methods were built.** One line lists the enabled deployment-wide IdP ids in button order. An id missing here was never built, so staring at the sign-in page will not tell you why

   ```sh
   docker compose logs cp | grep "login providers:"
   ```

2. **See why one is missing.** Each `WARNING: login provider "…" disabled — …` line names the setting that was missing or invalid. The commonest is `AF_OIDC_<ID>_TRUST`, which has no default. Google and GitHub print their own lines

   ```sh
   docker compose logs cp | grep -i "login provider"
   ```

3. **When the Control Plane does not start**, there are only two reasons
   - An Entra issuer is a multi-tenant endpoint ([Sign in with Microsoft Entra ID](entra.md#when-it-goes-wrong))
   - `AUTH=oauth requires AF_COOKIE_SECRET, PUBLIC_BASE_URL and at least one login provider …`: no usable method is configured

4. **Open `https://<PUBLIC_DOMAIN>/login`** and count the buttons: as many as the ids in step 1, Google first and GitHub last
   - "No sign-in method is configured. Please contact your administrator." means step 1 built nothing
   - A method the default tenant (`default`) has "**Show button**" turned off for does not appear here either, because this page is drawn as the default tenant's page. When the count is off, look at the default tenant's list of sign-in methods first
   - The default tenant's "**Accept**" restriction does not apply here. It is the only way in for people who belong to no tenant, so there must be no path to zero buttons

5. **Open `https://<PUBLIC_DOMAIN>/login/<slug>`.** It shows the deployment-wide buttons the tenant accepts (minus those with "Show button" off), followed by the tenant's own approved methods. When an approved method has no button, the reason is in the log

   ```sh
   docker compose logs cp | grep -i "tenant login provider"
   ```

   - An instruction to hide every button is ignored, so this page is never empty
   - An unknown slug is not an error; it shows the plain sign-in page. That is deliberate, so do not chase it

6. **For GitHub only**, check `docker compose logs cp | grep "returned 403"` before concluding anything about a rejection ([Sign in with GitHub](github.md#the-organisation-has-to-approve-the-app))

7. **Sign in once, end to end,** with an account that should get in
   - Landing on a page that says a **new workspace** was created means this deployment has never seen that address. It is there so people notice before they start work in the wrong place
   - Accounts with different email addresses cannot be merged afterwards. Somebody who has both accounts can add the other one under **Settings → Personal → Account**, "**Add a sign-in method**". Only a method that claims the same email address can be added, and it must meet that method's own organisation and domain conditions

If it still does not work, see [Cannot log in](../diagnose/cannot-login.md).

## Related

- [Approve a tenant's own sign-in](tenant-idp.md)
- [Cannot log in](../diagnose/cannot-login.md)
- [Choose how people sign in](../../admin/access/signin-methods.md) (what a tenant administrator does)
- [Roles — who may do what](../../ref/roles.md)

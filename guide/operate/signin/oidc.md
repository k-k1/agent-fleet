---
audience: "a deployment administrator who connects an IdP to the deployment (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in settings, as it reads them from `.env` (where this page and the code disagree, the page is wrong)"
---

# Sign in with another OIDC IdP

English | [日本語](oidc.ja.md)

Let people sign in to this deployment through an IdP that speaks OIDC: Okta, Keycloak, Auth0, Cognito, GitLab and others.
They all ride on the same client, so you register an app at the IdP and write five lines in `.env`.

## How it works

| What you do | What the Control Plane does |
|---|---|
| Register a confidential web app at the IdP | |
| Put the issuer, client ID, secret and trust rule in `.env` and restart | Shows a button for each IdP whose settings are complete |
| | The first time the button is pressed, reads the issuer's discovery document and checks that the issuer it names matches |

## Register the app at the IdP

In the IdP's admin console, register a **confidential client** (a web app with a client secret).

- The redirect URI is `https://<PUBLIC_DOMAIN>/oauth2/callback` ([Register the redirect URI](overview.md#register-the-redirect-uri))
- Note the client ID and client secret

The issuer has this shape at each IdP; use it as a guide to what to look for in the admin console.

| IdP | Issuer shape |
|---|---|
| Okta | `https://<org>.okta.com`; with a custom authorization server, `https://<org>.okta.com/oauth2/<authorization-server-id>` |
| Keycloak | `https://kc.example.com/realms/<realm>` |
| Auth0 | `https://<tenant>.<region>.auth0.com/` |
| Cognito | `https://cognito-idp.<region>.amazonaws.com/<user-pool-id>` |
| GitLab | `https://gitlab.com`, or the base URL of your own GitLab |

## Put it in .env

```sh
AF_OIDC_PROVIDERS=okta
AF_OIDC_OKTA_ISSUER=https://<your-org>.okta.com
AF_OIDC_OKTA_CLIENT_ID=<client-id>
AF_OIDC_OKTA_CLIENT_SECRET=<client-secret>
AF_OIDC_OKTA_TRUST=email_verified
```

- **Copy the issuer from the discovery document, not from the browser's address bar.** The Control Plane appends `/.well-known/openid-configuration` and requires the `issuer` in that document to be the same value. A trailing slash on one side only is not a mismatch
- **`_TRUST`** is why the email address can be trusted, and it has no default
  - `email_verified` if the IdP really sends `email_verified` (most Okta, Keycloak and Auth0 setups do)
  - `issuer` if it does not, but the issuer is pinned to one directory
  - When unsure, start with `email_verified`. Getting it wrong means a refusal with the reason in the log, never somebody getting in who should not
- The issuer must be `https`. `http` is allowed only for `localhost`, `127.0.0.1` and `::1`, for developing against a local Keycloak or Dex, and the Control Plane warns when it is used
- List more ids for more buttons (`AF_OIDC_PROVIDERS=entra,okta`, say)
- The id rules, the settings you rarely need (scopes, PKCE, a per-IdP allowlist) and treating one person as the same across app registrations work as for Entra ([Sign in with Microsoft Entra ID](entra.md))

Then restart the Control Plane and go through [Check that it worked](overview.md#check-that-it-worked).

## When it goes wrong

### The button does not appear

The log's `WARNING: login provider "…" disabled — …` line names the missing setting. The usual ones are a forgotten `_TRUST` and an issuer that is not `https`.

### The log says discovery …: issuer mismatch

The issuer you set differs from the `issuer` in the discovery document. Copy the document's value as it is.

### Everybody is refused with `_TRUST=email_verified`

The IdP does not send `email_verified`, so every address counts as unverified. If the issuer is pinned to one directory, use `_TRUST=issuer`.

## Related

- [Set up how people sign in](overview.md)
- [Sign in with Microsoft Entra ID](entra.md)
- [Cannot log in](../diagnose/cannot-login.md)

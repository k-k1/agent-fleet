---
audience: "a deployment administrator who connects an IdP to the deployment (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in settings, as it reads them from `.env` (where this page and the code disagree, the page is wrong)"
---

# Sign in with Microsoft Entra ID

English | [日本語](entra.ja.md)

Let people sign in to this deployment with a Microsoft Entra ID account. Register an app in Entra,
pin the issuer to your own directory, and put it in `.env`.

## How it works

| What you do | What the Control Plane does |
|---|---|
| Register an app in the Entra admin center and create a secret | |
| Put the issuer, client ID and secret in `.env` and restart | Refuses to start when the issuer is a multi-tenant endpoint and no accepted tenant is listed |
| | Reads the issuer's discovery document the first time the button is pressed (cached for 24 hours) |
| Keep track of the secret's expiry | |

## Register the app in the Entra admin center

Under **Microsoft Entra ID → App registrations → New registration**:

1. Give it a name (it appears on the consent screen). Under **Supported account types**, choose **Accounts in this organizational directory only** (single tenant). Choosing multi-tenant here is how the Control Plane ends up refusing to start
2. Add the redirect URI on the **Web** platform with `https://<PUBLIC_DOMAIN>/oauth2/callback` ([Register the redirect URI](overview.md#register-the-redirect-uri))
   - Registered as a **Single-page application**, the code cannot be exchanged with a client secret, and it fails only at the first sign-in
3. Under **Certificates & secrets → New client secret**, create one and copy its **Value** on the spot
   - The value is shown once. The **Secret ID** beside it is not the secret
   - **Put the expiry date in your calendar.** When it lapses, every sign-in through this button fails at the token exchange while nothing looks wrong in Agent Fleet
4. From the overview, note the **Directory (tenant) ID** and the **Application (client) ID**
5. The standard OIDC permissions (`openid`, `email`, `profile`) are enough. In a directory that turns off user consent, an administrator grants consent once

## Put it in .env

```sh
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<directory-tenant-id>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<application-client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<the Value from step 3>
AF_OIDC_ENTRA_TRUST=issuer
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
```

| Key | What it decides |
|---|---|
| `AF_OIDC_PROVIDERS` | The list of IdP ids. The id names the other keys (`entra` gives `AF_OIDC_ENTRA_*`) |
| `AF_OIDC_<ID>_ISSUER` | The issuer, with your directory's GUID in it |
| `AF_OIDC_<ID>_TRUST` | Why the email address can be trusted. For Entra, `issuer` |
| `AF_OIDC_<ID>_LABEL_JA`, `_LABEL_EN` | The button label |

- An **id** uses `a-z 0-9 - _`, up to 32 characters, starting with a letter or digit. In key names `-` becomes `_` (`entra-id` gives `AF_OIDC_ENTRA_ID_*`). The id also shows in tenants' lists of sign-in methods, so keep it short and stable
- **`_TRUST` has no default.** The allowlist is written in email addresses, so you have to state why this IdP's addresses can be believed. An IdP without it is not guessed at; it is left out at startup
  - `email_verified`: take only addresses the IdP itself says it has verified
  - `issuer`: the issuer is pinned to one directory, so that directory's addresses are taken as correct
  - **Entra does not send `email_verified`.** With `email_verified`, every Entra sign-in is refused as unverified
- **Pin the issuer to your directory's GUID.** With a `/common/`, `/organizations/` or `/consumers/` issuer, anybody with a Microsoft account reaches the sign-in, and a personal Microsoft account can change its own email address, which makes the allowlist meaningless
  - If you do use one of those issuers, list the directory GUIDs you accept in `AF_OIDC_<ID>_ALLOWED_TIDS`. Without them the Control Plane refuses to start

Then restart the Control Plane and go through [Check that it worked](overview.md#check-that-it-worked).
A wrong issuer does not show at startup; it shows as an error the first time somebody presses the button.

### Settings you rarely need

| Key | Default | When to use it |
|---|---|---|
| `AF_OIDC_<ID>_SCOPES` | `openid email profile` | To change the scopes asked for |
| `AF_OIDC_<ID>_PROMPT` | `select_account` | `none` stops sending the parameter, for an IdP that rejects it |
| `AF_OIDC_<ID>_ALLOWED_EMAILS`, `_ALLOWED_DOMAINS` | none | An allowlist for this IdP only. Set, it replaces the deployment-wide allowlist rather than adding to it |
| `AF_OIDC_<ID>_PKCE` | on | `off` stops sending PKCE. Use it only when sign-in starts failing at the authorization step after an update |

## One directory behind two app registrations

The same Entra directory may sit behind two app registrations (a deployment-wide button and a button a tenant registered, say).
Entra's `sub` differs per app registration, so left alone the same person becomes two accounts and two workspaces depending on which button they press.

```sh
AF_OIDC_ENTRA_LINK_CLAIM=oid
```

- `oid` is the person's object ID in that directory. It is the same in every app registration, and the person cannot change it
- Naming a value the person can claim (`email`, `upn`, `preferred_username` and the like) lets another method on the same issuer get into an existing account just by claiming it. Check this before you name anything other than `oid`
- What a tenant needs when it registers the same directory is in [Approve a tenant's own sign-in](tenant-idp.md#when-the-deployment-uses-the-same-idp)

## When it goes wrong

### The Control Plane exits at startup over a multi-tenant endpoint

The issuer is `/common/`, `/organizations/` or `/consumers/` and `AF_OIDC_<ID>_ALLOWED_TIDS` is empty. Pin the issuer to
`https://login.microsoftonline.com/<directory-tenant-id>/v2.0`, or list the directories you accept.

### Entra shows a code starting with AADSTS

It is almost always one of two things, both on the Entra side. Changing `.env` does not fix them.

- The client secret is invalid: the **Secret ID** was pasted instead of the **Value**, or it has expired
- The redirect URI does not match: the wrong platform type, or one character off

### The log says no email claim from id_token or userinfo

The account has no email address in the directory, and neither `preferred_username` nor `upn` looks like an address.
Add the `email` optional claim to the ID token in the app registration, or give the account an email address.

### The log says discovery …: issuer mismatch

The issuer you set differs from the one the IdP names. Copy it from the discovery document rather than typing it.

### Everybody is refused after months of working

The client secret has expired. Create a new one, replace `AF_OIDC_<ID>_CLIENT_SECRET` and restart.

## Related

- [Set up how people sign in](overview.md)
- [Sign in with another OIDC IdP](oidc.md)
- [Cannot log in](../diagnose/cannot-login.md)

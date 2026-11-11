---
audience: "a deployment administrator who connects an IdP to the deployment (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in settings, as it reads them from `.env` (where this page and the code disagree, the page is wrong)"
---

# Sign in with GitHub

English | [日本語](github.ja.md)

Let members of the GitHub organisations you name sign in to this deployment with their GitHub account.
What lets someone in is being an active member of one of those organisations, not having a GitHub account.

## How it works

| What you do | What the Control Plane does |
|---|---|
| Create an OAuth App on GitHub | |
| Have the organisation's owner approve that OAuth App | |
| Put the allowed organisations and the OAuth App's values in `.env` and restart | With `AF_GITHUB_ALLOWED_ORGS` set, shows the GitHub button last on the sign-in page |
| | Asks GitHub at every sign-in whether the person is a member, and keeps the answer in memory for a while |

GitHub has no OIDC for user sign-in, so it has its own settings.

## Create the OAuth App on GitHub

1. Create an **OAuth App** (not a GitHub App): **Settings → Developer settings → OAuth Apps → New OAuth App**. To have the organisation own it rather than a person, create it on the organisation's side
2. Set the **Authorization callback URL** to `https://<PUBLIC_DOMAIN>/oauth2/callback` ([Register the redirect URI](overview.md#register-the-redirect-uri)). The **Homepage URL** can be the Console's URL
3. Generate a **client secret** and copy it (it is shown once)
4. If the organisation restricts third-party OAuth Apps, **an organisation owner approves this OAuth App** (in the organisation's settings, under the third-party application access policy). Until then everybody is refused, however correct the settings are

You may reuse the OAuth App behind the Console's GitHub **connect** (`GITHUB_OAUTH_CLIENT_ID`); just add the callback URL.
Scopes are asked per authorization, so one app can serve both the git connection (`repo`) and sign-in (`read:org user:email`).
If you do not want one organisation approval to cover both, use a separate app.

## Put it in .env

```sh
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs      # required; this is what turns the button on
GITHUB_OAUTH_CLIENT_ID=<client-id>
GITHUB_OAUTH_CLIENT_SECRET=<client-secret>
AF_GITHUB_ALLOWED_DOMAINS=example.com      # strongly recommended
```

| Key | What it decides |
|---|---|
| `AF_GITHUB_ALLOWED_ORGS` | The organisations allowed to sign in (comma-separated). An active member of any one gets through |
| `GITHUB_OAUTH_CLIENT_ID`, `_SECRET` | The OAuth App's values |
| `AF_GITHUB_LOGIN_CLIENT_ID`, `_SECRET` | Only for an OAuth App used for sign-in alone. Takes precedence over `GITHUB_OAUTH_*` |
| `AF_GITHUB_ALLOWED_DOMAINS` | The email domains admitted |

- **`AF_GITHUB_ALLOWED_ORGS` is what turns the button on**, not the client id. A deployment that uses `GITHUB_OAUTH_CLIENT_ID` only for the git connection keeps working as before
- **Set `AF_GITHUB_ALLOWED_DOMAINS` too.** GitHub hands over the account's primary verified address, and that is often a personal one. A different address is a different person here, so they land in a **new, empty workspace** rather than their usual one. Refusing them at the door is kinder
- With no email list set anywhere, the organisation is the only condition, and the Control Plane says so in its log at startup

Then restart the Control Plane and go through [Check that it worked](overview.md#check-that-it-worked).

### Settings you rarely need

| Key | Default | What it decides |
|---|---|---|
| `AF_GITHUB_ALLOWED_EMAILS` | none | The email addresses admitted |
| `AF_GITHUB_LABEL_JA`, `_LABEL_EN` | the built-in label | The button label |
| `AF_GITHUB_MEMBERSHIP_TTL` | `10m` | How long an "is a member" answer is kept |
| `AF_GITHUB_MEMBERSHIP_GRACE` | `1h` | How long the last answer keeps people in while GitHub cannot be reached. After that they are refused |
| `AF_GITHUB_PKCE` | on | `off` stops sending PKCE |

## Worth knowing in advance

### The organisation has to approve the app

An organisation that restricts third-party OAuth Apps hides membership from an app it has not approved.
The Control Plane cannot tell "not a member" from "not allowed to look", so everybody is refused. The log says so:

```
WARNING: github: org "acme" returned 403 for a membership check — if the org restricts
third-party OAuth apps, an org owner must approve this OAuth app before anyone can sign in
```

### A Control Plane restart asks people to sign in again

The membership answer, and the token to refresh it, are kept in memory only. A restart leaves nothing to re-check with,
so people see "please sign in again" rather than "not allowed". With a live GitHub session they are usually back without doing anything.
It is not a fault.

### Changing a GitHub username changes nothing

A username given up can be taken by somebody else, so Agent Fleet tells people apart by GitHub's numeric account ID.

## When it goes wrong

### Everybody is refused though the settings look right

The organisation has not approved the OAuth App yet ([The organisation has to approve the app](#the-organisation-has-to-approve-the-app)). Check with `docker compose logs cp | grep "returned 403"`.

### Only some people are refused

- Their GitHub primary verified address is outside `AF_GITHUB_ALLOWED_DOMAINS`: have them verify their company address on GitHub and make it primary, or use another button
- Their organisation membership is still pending: have them accept the invitation

### It says the GitHub account has no primary verified email address

The account has no verified primary address to hand over. Have them verify an address on GitHub and make it primary.

### The button does not appear

The log has a `github login disabled — …` line: `AF_GITHUB_ALLOWED_ORGS` is empty, or only one of the client id and secret is set.

## Related

- [Set up how people sign in](overview.md)
- [Approve a tenant's own sign-in](tenant-idp.md) (when a tenant registers a GitHub organisation)
- [Cannot log in](../diagnose/cannot-login.md)

---
audience: "a deployment administrator who connects an IdP to the deployment (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in settings, as it reads them from `.env` (where this page and the code disagree, the page is wrong)"
---

# Sign in with Google

English | [日本語](google.ja.md)

Let people sign in to this deployment with a Google account. Create an OAuth client in Google Cloud Console
and put two values in `.env`; that is all.

## How it works

| What you do | What the Control Plane does |
|---|---|
| Create an OAuth client in Google Cloud Console | |
| Put the client ID and secret in `.env` and restart | With both set, shows the Google button first on the sign-in page |
| Decide who may enter with the allowlist | Takes only addresses Google has verified, and checks them against the allowlist |

## Create the client in Google Cloud Console

1. Pick the project to hold the client (create one if there is none)
2. Configure that project's **OAuth consent screen**
   - **Internal** if you use Google Workspace and want nobody outside your domain to see it; **External** otherwise
   - This is who can reach the consent screen. Who may sign in is decided by Agent Fleet's allowlist
3. Choose **Credentials → Create credentials → OAuth client ID**, with the type **Web application**
4. Add `https://<PUBLIC_DOMAIN>/oauth2/callback` under **Authorized redirect URIs** ([Register the redirect URI](overview.md#register-the-redirect-uri)). **Authorized JavaScript origins** is not needed
5. Create it and note the **client ID** and **client secret** on the spot

## Put it in .env

```sh
GOOGLE_OAUTH_CLIENT_ID=<client-id>.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=<client-secret>
```

- These two are all. Setting both turns the Google button on, and the IdP's id is `google`
- You do not add `google` to `AF_OIDC_PROVIDERS`, and there are no `AF_OIDC_GOOGLE_*` keys
- The issuer, the endpoints and the trust rule are built in and cannot be changed. Only addresses Google marks `email_verified` are taken, and the scopes asked for are `openid email`
- A personal `gmail.com` address passes Google's check too. Keeping the button to your own people is the job of `AF_OAUTH_ALLOWED_DOMAINS`

Then restart the Control Plane and go through [Check that it worked](overview.md#check-that-it-worked).

## When it goes wrong

### Google shows redirect_uri_mismatch

The URI registered on the client does not match `<PUBLIC_BASE_URL>/oauth2/callback` exactly. Google compares the whole string.

### The button does not appear

Only one of the two keys is set. The Control Plane's log has this line:

```
WARNING: google login disabled — set both GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET
```

### Only test users get through

The consent screen is **External** and still in testing. Google stops every account that is not a test user before it reaches the Control Plane.

### Google lets them through but Agent Fleet refuses

That is the allowlist, not Google. See [Cannot log in](../diagnose/cannot-login.md).

## Related

- [Set up how people sign in](overview.md)
- [Cannot log in](../diagnose/cannot-login.md)

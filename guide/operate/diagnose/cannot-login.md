---
audience: "a deployment administrator told that someone cannot sign in (works on the host and edits `.env`)"
source_of_truth: "the Control Plane's sign-in handling and the sign-in page's wording (where this page and the code disagree, the page is wrong)"
---

# Cannot log in

English | [日本語](cannot-login.ja.md)

The IdP is set up, yet sign-in is refused, a button is missing, or people are bounced straight back: find the cause from the symptom.
How to set an IdP up is in [Set up how people sign in](../signin/overview.md).

## How it works

| What you do | What the Control Plane does |
|---|---|
| Hear the symptom and read the log | Logs the IdPs it left out at startup and why a sign-in was refused |
| Fix the allowlist, the roster or the IdP's settings | Applies allowlist and roster changes from the next request |

The commands are the compose ones. On another target, read the Control Plane's log the way that target's runbook says.

## Where to look first

| Symptom | Where to look |
|---|---|
| Everybody is refused | [An empty allowlist](#everybody-is-refused), or for GitHub, [the organisation's approval](#everybody-is-refused-on-github) |
| A button is missing | `docker compose logs cp \| grep -i "login provider"` |
| The IdP shows an error | [redirect URI mismatch](#the-idp-says-redirect-uri-mismatch) |
| The Control Plane does not start | [The Entra issuer](#the-control-plane-exits-over-the-entra-issuer) |
| Only certain people or certain tenants | [The tenant's sign-in methods](#it-says-this-tenant-needs-a-different-sign-in) |
| Sent straight back after signing in | [HTTPS](#the-cookie-is-not-kept-and-people-land-back-on-the-sign-in-page) |

## Everybody is refused

With the allowlists (`AF_OAUTH_ALLOWED_EMAILS`, `_DOMAINS`, `_EMAILS_FILE`) **all empty and nobody invited to a tenant yet**, everybody is refused. It fails closed on purpose.

- Set at least one allowlist, or add the person as a member
- `_EMAILS_FILE` is reread at every sign-in, so adding to it needs no restart. An invitation takes effect at once too

The sign-in page says "This account isn't allowed access. Ask an administrator to add your email address."

## The IdP says redirect URI mismatch

The redirect URI registered at the IdP (Google Cloud Console, the Entra app registration and so on) does not match `<PUBLIC_BASE_URL>/oauth2/callback` exactly.

- When you change `PUBLIC_BASE_URL`, fix it at every IdP
- It is one URI however many IdPs you enable ([Register the redirect URI](../signin/overview.md#register-the-redirect-uri))

## A button you set up is missing

An IdP with incomplete settings is left out at startup, so that one IdP's mistake does not lock everybody out.

```sh
docker compose logs cp | grep -i "login provider"
```

The missing setting is named there. The commonest is a forgotten `AF_OIDC_<ID>_TRUST`, which has no default.

When a method a tenant registered has no button, it is still "Waiting for approval", or approved with incomplete settings. **Admin → Sign-in method register**
shows which, and the log's `tenant login provider` lines say what is wrong ([Approve a tenant's own sign-in](../signin/tenant-idp.md#read-the-state)).
A tenant's approved method appears only on `/login/<slug>`, never on `/login`. That is not a fault.

## The Control Plane exits over the Entra issuer

The Entra ID issuer is `/common/`, `/organizations/` or `/consumers/`, and `AF_OIDC_<ID>_ALLOWED_TIDS` is empty.
As it stands, anybody with a Microsoft account could try to sign in, and personal accounts can change their own email address.

Pin the issuer to your directory (`https://login.microsoftonline.com/<tenant-guid>/v2.0`) or list the directories you accept
([Sign in with Microsoft Entra ID](../signin/entra.md)).

## Everybody is refused on GitHub

- **The organisation has not approved the OAuth App**: an organisation that restricts third-party OAuth Apps hides membership until it approves. `docker compose logs cp | grep "returned 403"` shows it
- **The address is outside the domains**: GitHub hands over the account's primary verified address. A personal one falls outside `AF_GITHUB_ALLOWED_DOMAINS`. Have them verify their company address on GitHub and make it primary, or sign in with another button

More in [Sign in with GitHub](../signin/github.md#when-it-goes-wrong).

## Only GitHub users are asked to sign in again after a restart

Not a fault. The membership answer and the token to refresh it are kept in memory only, so a restart leaves nothing to re-check with.
That is why they see "please sign in again" rather than "not allowed". With a live GitHub session, they usually never notice.

## It says "This tenant needs a different sign-in"

In that tenant's **Sign-in methods**, "**Accept**" is off for the IdP the person used. Not a fault.

- A session is tied to one IdP, so moving to a tenant that wants another method means signing in again. The Console offers "**Sign in again**"
- `https://<PUBLIC_DOMAIN>/login/<slug>` lists only the methods that tenant accepts
- If the rule itself is wrong, fix it in the tenant's list of sign-in methods

## It says "This email address is already used by another sign-in method on this deployment"

A method a tenant registered claimed an address that has signed in before. This stops that method from taking over an existing account.

- Have them sign in the way they normally do
- With **the same GitHub account**, they are the same person through either the deployment-wide GitHub or a tenant's GitHub. This appears when they pressed a different IdP's button
- If they have both accounts, have them sign in the usual way and add that method under **Settings → Personal → Account**, "**Add a sign-in method**". Only a method that claims the same email address can be added, and it must meet that method's own organisation and domain conditions
- If people without the other account are stuck, keep their method on "**Accept**". If its button is in the way, turn off only "**Show button**"

## It says "This account can't sign in right now because it is still on another tenant's roster"

Through a method a tenant registered, somebody with an active membership in another tenant tried to get in. A person belongs to one tenant, so this is not a fault.

1. The tenant that holds the person uses **Remove member** on its roster. A removed membership does not count
2. The person signs in again

If the other tenant does not respond, a deployment administrator removes that membership. `af-cp identity-email-check` tells you which tenant holds them
([Find the cause from the symptom](checklist.md#check-that-no-address-is-attached-to-the-wrong-person)).
Inviting somebody who signs in through another tenant's method is refused with `identity_bound_to_other_tenant`.

## A removed person can still get in

Disabling the account at the IdP **does not end a session they already have**. The signed cookie lasts up to `AF_SESSION_TTL` (7 days by default), and there is no way to revoke one person's.

- Remove them from the roster (**Admin → Tenants → the tenant → Members → Remove member**) or from the allowlist. Either takes effect from the next request
- To end everybody's sessions at once, rotate `AF_COOKIE_SECRET` ([Revoke access and hand over super_admin](../secure/offboarding.md#end-everybodys-sessions-now))

## The cookie is not kept, and people land back on the sign-in page

`AUTH=oauth` uses a Secure cookie, so **HTTPS is required**. Over plain HTTP the cookie is not kept.

- Does `PUBLIC_BASE_URL` start with `https://`?
- Has the TLS certificate actually been issued? ([Find the cause from the symptom](checklist.md#the-tls-certificate-is-not-issued))

## Related

- [Set up how people sign in](../signin/overview.md)
- [Approve a tenant's own sign-in](../signin/tenant-idp.md)
- [Find the cause from the symptom](checklist.md)
- [Choose how people sign in](../../admin/access/signin-methods.md) (what a tenant administrator does)

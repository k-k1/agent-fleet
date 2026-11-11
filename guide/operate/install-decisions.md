---
audience: "someone standing a deployment up for the first time, and the deployment administrator who decides how tenants are split and how people sign in to them"
source_of_truth: "`deploy/compose/.env.example` and the Console's Admin screen (a contradiction is a bug in this page)"
---

# Choose the tenant and provisioning model

English | [日本語](install-decisions.ja.md)

What to decide before you start (how the certificate is obtained, how people not yet on a roster are let in, whether to split tenants), and how to decide each tenant's sign-in.
All of it can be changed later.

## How it works

| What you decide | What Agent Fleet does |
|---|---|
| A certificate from a public CA, or self-signed | Caddy obtains the certificate from Let's Encrypt; with `tls internal`, it signs its own |
| Invitation only (`invite`) or automatic admission (`auto`) | `invite` refuses anyone not on a roster. `auto` puts whoever passes the allowlist into the default tenant |
| Whether to split tenants, and which methods each tenant accepts | Checks on every request that the method is one the tenant accepts |
| Whether to approve an IdP a tenant registered | Until approved, shows no button for that IdP and issues no session |

## Whether to use `tls internal`

Caddy obtains the public domain's certificate from Let's Encrypt automatically. That needs public DNS, and ports 80 and 443 reachable from outside.

| Situation | Choose |
|---|---|
| You can have public DNS | Leave the default (Let's Encrypt) |
| Internal only or a closed network, without public DNS | Switch to the alternative Caddyfile setting (`tls internal`, self-signed) |
| A TLS-terminating proxy already sits in front | Remove the Caddy service itself (Caddyfile alternative 2) |

- With `tls internal`, browsers show a certificate warning. Plan separately for things like distributing an internal CA
- How to switch is in the footnote to the runbook's "Quick start" and in the Caddyfile

## `AF_PROVISION`: `invite` or `auto`

Decide what happens when somebody not yet on a roster signs in.

| | `invite` (the default in a new `.env`) | `auto` |
|---|---|---|
| Somebody not on a roster | Refused until an administrator adds them in **Admin** | Admitted as a member of the default tenant, if they pass the allowlist |
| What decides who gets in | The roster. Somebody you added gets in even when not listed in `AF_OAUTH_ALLOWED_*` | `AF_OAUTH_ALLOWED_*`. It is the only thing between a stranger and a workspace |
| Suited to | Deciding who gets in one person at a time | A small team, allowed by domain |

- **You can start with `invite` from the first boot.** A `SUPER_ADMIN_EMAILS` account reaches **Admin** without being a member of any tenant
- Somebody not invited yet sees a "You haven't been invited yet" page showing the address they signed in with. Have them pass that address on, and you can add it to the roster as it is
- Only `.env.example` (and the ECS `AfProvision` parameter) default to `invite`. The Control Plane's own default is `auto`, so an existing `.env` that never set `AF_PROVISION` behaves as before
- Switching a running deployment to `invite` does not affect current members. Anyone who belongs somewhere has that roster read first. Only automatic admission of new people stops

## One tenant, or several

| | One (the default) | Several |
|---|---|---|
| What it means | Everyone joins the built-in `default` tenant | Each tenant gets fully isolated workspaces |
| When | Enough in most cases | When you need hard separation, such as between departments |

Tenants can be added later. When in doubt, start with one and split when you need to.

## Decide each tenant's sign-in

Once tenants are split, **Admin** → open the tenant → "Sign-in methods" and "Login rules" decide each tenant's sign-in.

| Setting | Screen | What it does | Where it applies |
|---|---|---|---|
| **Accept** | Sign-in methods | This method may be used to enter this tenant | Checked on every request, not only by hiding the button |
| **Show button** | Sign-in methods | Removes **only the button** from this tenant's sign-in page, leaving the method accepted | Display only. Somebody who signs in with a hidden method still gets in |
| **Auto-join domains** | Login rules | An address in this domain joins this tenant on first sign-in | One tenant per domain |
| **Invite domains** | Login rules | Bounds who may be added as a member | Only when adding a member. Not checked on every request |

- "Sign-in methods" lists the methods the deployment enables in `.env` ("Deployment-wide") together with the ones the tenant registered itself. Two toggles on each row decide them
- **Only a deployment administrator can flip the toggles.** A tenant administrator sees the same list read-only ([Choose how people sign in](../admin/access/signin-methods.md))
- Neither "Accept" nor "Show button" can be cleared on the last row. Clearing them all would mean "no restriction", or a page with no buttons

### The tenant's own sign-in page

Each tenant has its own sign-in page at `https://<PUBLIC_DOMAIN>/login/<slug>`, listing only the methods the tenant accepts and shows a button for.

- Give new members this URL. No invitation email is sent (the Control Plane has no way to send mail)
- The plain `/login`, without a slug, is rendered as the default tenant's (`default`) page. A method whose "Show button" is cleared on the default tenant is gone from `/login` too
- "Accept", however, does not apply to `/login`. It is the only door for somebody who belongs to no tenant yet (a new deployment administrator, somebody not yet invited). Narrowing it there would leave a page without buttons the moment you narrowed the default tenant, and nobody could get in until the database was fixed by hand. Narrowing what is accepted is enforced when the tenant is resolved, on every request

> [!NOTE]
> **"Invite domains" is not "the domains that may use this tenant".**
> It only bounds who can be put on the roster; somebody already a member keeps working even from another domain (which is what makes a contractor's address work).
> To end somebody's access, do not narrow this field: **remove the member**.

## Welcome a tenant with its own IdP

Some tenants have an identity source of their own: a different Entra ID, Okta or Keycloak tenant (with its own issuer, client ID and secret), or a GitHub organization instead of an IdP.
It happens with group subsidiaries, companies still being merged, outsourcing partners, and divisions that run their own directory.

| Who | Where | What |
|---|---|---|
| The tenant administrator | **Tenant settings** → "Sign-in methods" → **Add a sign-in method** | Registers their own IdP or GitHub organization. The row starts as "Waiting for approval" |
| The deployment administrator | **Admin** → "Sign-in method register" | Checks it and presses **Approve and activate** |

- No `.env` edit and no Control Plane restart is needed
- A deployment administrator can also open the same fields from **Admin** → the tenant → "Sign-in methods"
- The fields, what to check before approving, and how a GitHub organization differs are in [05 Sign-in methods](05-signin.md#7-a-sign-in-method-a-tenant-defines-itself). The tenant administrator's side is [Choose how people sign in](../admin/access/signin-methods.md)

### Approval is not a formality

- Registering alone does nothing. Until it is approved, no button appears on the tenant's sign-in page, and no session is issued even to somebody who builds the sign-in URL by hand
- Being able to register an IdP means being able to **say who somebody is**. On this deployment a person is identified by email address, so that reaches across the whole deployment, including who is a deployment administrator
- An administrator who could activate their own IdP could issue themselves a token carrying **your address**
- Approval happens once per tenant. Day-to-day work stays inside the tenant

### Re-read the register now and then

The "Sign-in method register" is not a queue that empties; it is a list to re-read now and then.

- **Approve and activate** and **Suspend** are on each row. Approved methods stay on it, with who approved them and when
- The IdP stays under the other company's control, so settings such as self-sign-up can change after you approved it. Review the issuer and the accepted domains regularly
- A tenant administrator can suspend at any time too. Stopping never waits for you

## When people belong to more than one tenant

Narrowing what a tenant accepts can stop somebody who belongs to several tenants from switching.

Example: Yamada belongs to both head office and a subsidiary, and normally signs in with head office's Google. If the subsidiary accepts only its own GitHub:

1. When Yamada switches to the subsidiary, "This tenant needs a different sign-in method. Please sign in again." appears
2. Pressing that GitHub button is refused with "This email address is already used by another sign-in method on this deployment." The same address at Google and at GitHub is two different sign-ins
3. If Yamada is not in the subsidiary's GitHub organization, there is no way through at all

There are three ways out.

| Way | What to do | What to watch |
|---|---|---|
| (a) Keep it GitHub only | Invite Yamada into the subsidiary's GitHub organization | |
| (b) Accept head office's method too | Leave head office's method on "Accept" | It does not widen who can get in; the roster decides that. If **Auto-join domains** is set, though, check it, because that field creates roster entries |
| (c) Let Yamada link it | Yamada signs in with the usual Google and presses **Settings → Personal → Account → "Add a sign-in method"**. From then on either button leads to the same account | Only for somebody who already has an account in the subsidiary's GitHub organization. Only a method asserting the same email address can be linked, and they must be a member of that organization. Linking is not a way around the entry rules |

- If, with (b), you do not want an unused Google button on the subsidiary's sign-in page, clear "Show button" on the Google row alone. Yamada keeps signing in at `/login` and switches tenants
- The same GitHub account is one person whichever button it comes through: the deployment-wide GitHub button or the tenant's own

## Related

- [Install Agent Fleet](install.md)
- [05 Sign-in methods](05-signin.md)
- [Choose how people sign in](../admin/access/signin-methods.md): the tenant administrator's screen
- [Add a member](../admin/members/add.md)

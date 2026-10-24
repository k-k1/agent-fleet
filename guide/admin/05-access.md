---
audience: "a tenant administrator deciding how people get in"
updated: "2026-08"
---

# 05. Who may sign in, and from where

English | [日本語](05-access.ja.md)

Four screens decide who reaches your tenant and how. Two are yours to change, one is
read-only for you, and one is yours to register but somebody else's to approve.

| Screen | Yours? |
|---|---|
| **Sign-in methods** | register and edit; a deployment administrator approves |
| **Login rules** | read-only |
| **Allowed networks** | yours |
| **Integration OAuth apps** | yours |

The last section, development sign-in, is not one of them: it is a deployment-wide switch
that only a super_admin sees.

## Allowed networks

By default your tenant is reachable from anywhere the deployment itself is reachable
from. **Source networks** narrows that to a list you write: comma-separated CIDR ranges
or single addresses, IPv4 or IPv6. Leaving it empty means no restriction.

The screen shows **your address, as this deployment sees it**. That is the value a
rule is matched against, *not* what your browser believes its address is. Check it
before you save. If the deployment cannot determine the source of the request, the
screen says so and the rules cannot be applied; that is a deployment-side setting
(how many proxies to trust), so raise it with your deployment administrator rather
than guessing at a range.

> **Lock yourself out and you cannot fix it from here.** Add the range you are
> currently connecting from *before* you save, and confirm it against the address on
> the screen. Recovering from a wrong entry needs a deployment administrator.

Two more things worth knowing:

- The restriction is applied **after** authentication, not instead of it. It narrows
  who may use a valid session; it is not a substitute for sign-in.
- It applies to your tenant. Someone who also belongs to another tenant reaches that
  one under its own rules.

## MCP OAuth

Under **Tenant settings → Allowed networks**, the **MCP OAuth** panel controls whether external apps may connect to this tenant through OAuth. It appears only when the deployment serves MCP OAuth. The tenant setting is enabled by default; a tenant administrator or deployment administrator can change it.

Clear **Allow apps to connect through MCP OAuth**, choose **Save**, and confirm. Existing OAuth grants for this tenant are revoked and audited immediately. The tenant disappears from consent choices, code exchange and refresh fail, and its OAuth access tokens stop working at `/mcp`. Other tenants and Personal Access Tokens are unaffected.

An OAuth connection's `read` and `write` scopes also reach members' Agent Fleet memory, exactly as a token's do: the consent page says so. Whether they can is decided by **External memory access** ([08](08-external-memory.md)) and each member's own setting, not by this panel. Block there hides the memory tools from OAuth connections and tokens alike.

Enabling the switch again permits new connections; it does not restore revoked connections. Members reconnect from their apps. They can also revoke individual connections in **Settings → Connections → MCP tokens → Connected apps**.

### Trusted client hosts (deployment-wide — super_admin only)

**Admin → Deployment → MCP sign-in hosts** lists the client hosts this deployment's MCP OAuth server trusts: the hosts that may receive the sign-in redirect and serve client metadata. It is deployment-wide, not per tenant, and only a super administrator sees it. Loopback clients (a CLI on the member's own machine) are always allowed.

- **To let another Agent Fleet deployment sign in to this one's `/mcp`, add its host here** (the host name only, for example `af.example.com`).
- **Removing a vendor works**: delete `chatgpt.com` from the list and save, and that vendor can no longer sign in. Saving an empty list trusts no hosted client at all.
- Entries are exact host names: no `https://`, port, path or wildcard, and no IP address. A mistake is refused with a message and nothing is saved.
- **Which list applies**: the list saved here wins; with nothing saved, the `AF_MCP_OAUTH_TRUSTED_HOSTS` environment variable; with that unset, the built-in default (`claude.ai`, `claude.com`, `chatgpt.com`). The screen says which one is in effect. **Reset to default** deletes the saved list so the other two apply again.
- A change applies from the next request, with no restart, to new sign-ins, client registration, and token requests. A connection whose redirect host (or, for a metadata-URL client, whose own host) was removed can no longer refresh its token or redeem a pending code; an access token already issued works until it expires (at most one hour). Loopback connections are not affected. Every change is audited (`mcp_oauth.trusted_hosts.set` / `.reset`, with the old and new lists).

## Integration OAuth apps

Members can always connect GitHub or Bitbucket by pasting a token. Registering your
tenant's **own** OAuth app adds the nicer path: a **"Connect with OAuth"** button on
that provider, so nobody has to mint a token by hand.

### GitHub: choose which app

GitHub's card does not start empty. You pick which app the button talks to:

| Choice | What members go through | When to pick it |
|---|---|---|
| **Built-in OAuth App** | One authorization. The token reaches every repository the member can access (scopes `repo` and `workflow`). | Individuals, small setups. This is the **default** of a deployment's first tenant, so a personal install has the button from the start. |
| **Built-in GitHub App** | Install the app on their account or organisation, choose the repositories, then authorize. | Teams that want narrower, per-repository access. In an organisation only an owner can install it. |
| **Your own app** | Whatever your app is: an OAuth App or a GitHub App you registered. | Organisations that keep their own app. |
| **None** | No button; members paste a token. | |

The built-in apps need nothing from you, and they cannot see members' tokens: the
device flow GitHub uses needs no secret, and the token goes only into the member's own
workspace. The operator can withdraw them from the whole deployment
(`AF_GITHUB_BUILTIN_APPS=off`); the card then says so.

For **your own app**, paste its client_id. Whether it is an OAuth App or a GitHub App is
**checked with GitHub when you save** and shown on the card; if GitHub cannot be reached,
the card shows an estimate, which the first member's connection confirms. Two things to
set on GitHub's side:

- Tick **Enable Device Flow**: a client_id without it is refused when you save.
- For a GitHub App, **Expire user authorization tokens** may stay on: af renews those
  tokens itself and stores no client secret. A member whose authorization is revoked, or
  whose refresh token goes unused for about six months, sees "reconnect needed" in Connections and connects again.
  Enter the app's page (`https://github.com/apps/<name>`) as the install page. A GitHub App reaches only the
  repositories it is installed on; members are sent there to install it, and are warned
  after connecting if it is installed nowhere.

**Reset to default** removes your choice.

### Bitbucket and Jira

To register one:

1. Create an OAuth app in your own organisation on the provider's side. The screen
   tells you where.
2. Copy the **callback URL** the screen shows into that app. This has to match exactly,
   or approval comes back with an error the provider words unhelpfully.
3. Paste the **client_id** and **client_secret** here. Bitbucket calls the same two
   things Key and Secret.

> **Jira: turn Sharing on as well.** An Atlassian 3LO app is "in development" until you
> enable Sharing under its **Distribution**, and until then **only the person who created
> it can authorize it**, so your own test passes and every other member is stopped by
> Atlassian's *"You don't have access to this app"*. That page is before the consent
> screen, so nothing comes back to af: the member simply stays unconnected, with no error
> anywhere. Enabling Sharing asks for a **Vendor name**, **Contact link** and **Privacy
> policy URL**, which the members authorizing the app can see; put your company name and
> a support address there, not a personal name or inbox. It does not list the app on the
> Atlassian Marketplace. Members will still see a notice that the app has not been
> reviewed by Atlassian; that is expected for an app you registered yourself.

> **Jira: boards, sprints and epics need more scopes.** The Jira CLI in members' sessions
> reaches boards, sprints and epics through Jira Software, which accepts none of the
> scopes above. In the Developer Console, open the app's **Permissions → Jira API →
> Configure → Granular scopes → Edit Scopes** and select these eight. They sit next to the
> classic scopes in the same app, so keep those as they are.
>
> | Shown as | Code |
> |---|---|
> | View boards, backlogs, and related items | `read:board-scope:jira-software` |
> | View sprints | `read:sprint:jira-software` |
> | Update and delete sprints | `write:sprint:jira-software` |
> | View epics and related issues | `read:epic:jira-software` |
> | Update epics | `write:epic:jira-software` |
> | View projects | `read:project:jira` |
> | View issue details | `read:issue-details:jira` |
> | View JQL | `read:jql:jira` |
>
> Check the code column. *View board configuration* (`read:board-scope.admin:jira-software`)
> looks like the first row but is a different scope, and af does not ask for it.
> **Then** tick *The app also grants the Jira Software scopes* on the Jira card and save.
> In that order: Atlassian refuses an authorization that asks for a scope the app does not
> have, so ticking first stops every member from connecting. (Measured: the refusal is a
> dead-end page on Atlassian's side with no redirect back to af, so af cannot fall back to the
> basic scopes.) A member whose Jira card says "Boards, sprints and epics: not available" is
> connected without these scopes. Members who are already
> connected get the new scopes when they reconnect Jira.

The secret is **encrypted on save and never shown again**. When you edit the
registration later, leaving the secret field empty keeps the stored one; fill it in
only when you actually want to change it. **Remove registration** takes the OAuth
button away again; members who connected with a token are unaffected.

This is per provider and per tenant. It does not widen what members may do with the
provider; that is decided by the app's own scopes and by each member's permissions
there.

## Sign-in methods, and login rules

**Sign-in methods** is where you register your own company's IdP, or a GitHub
organisation, as a way into this tenant. Registering is not enough: a deployment
administrator approves it, and a later change (adding an organisation, changing how
the same account is recognised) sends the row back for approval.

**Login rules** shows the join mode and the domains in force. It is read-only for you.
It exists so you can see *why* an invitation was refused without having to ask anyone.

Both screens have sharp edges that are easier to hit than to notice. The
deployment administrator's chapter on sign-in
([operate/](../operate/README.md)) covers them in detail; the most important are:

- **Narrowing what your tenant accepts can lock out people who belong to another
  tenant too.** The same address at a different IdP is a *different login*. Leave their
  method accepted and just stop showing its button instead.
- **Clearing "show button" keeps a method accepted.** People already using it still
  can; it just stops appearing on the sign-in page. You cannot clear it on every
  method: a sign-in page with no buttons is a dead end, so the setting is ignored.
- If someone holds both accounts, they can link the second method to their own account
  themselves, under their personal account settings. You have nothing to do.

## Development sign-in (dev OIDC IdP — super_admin only)

The **Dev sign-in** section of the Admin modal switches the Control Plane's development OIDC IdP
(ADR 0116) for the whole deployment. **It is on by default** and super_admin only, so it does not
appear in your tenant settings. There is no per-workspace switch: while it is on (and preview subdomains exist) every workspace's
client is served.

- The section shows the issuer, how many workspaces hold a client (one is created when a workspace starts while
  the IdP is offered, and kept from then on), and the one rule that matters:
  **a production app must never trust this issuer**, because it signs in as any member without a
  password.
- Turning it off takes effect without a restart (another Control Plane replica follows within a few
  seconds): every `/dev-idp/…` URL answers 404. **Workspaces' client registrations are kept**, so turning it on
  again hands the same client ids and secrets back.
- **Members can use it only with preview subdomains.** Without a preview domain
  (`AF_PREVIEW_DOMAIN`) the section says so, the `/dev-idp/…` URLs answer 404, nothing is injected into
  workspaces and workspaces report it unavailable. The switch itself stays operable.
- It shows as locked when the operator set `AF_DEV_IDP=0`, and cannot be on without
  `PUBLIC_BASE_URL`; the section says which one applies. Every change is audited (`dev_idp.set`).

---

Related: [ref/roles.md](../ref/roles.md) for who may do what ·
[01 Members](01-members.md) for who is in the tenant at all

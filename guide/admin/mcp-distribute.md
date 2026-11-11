---
audience: "Tenant administrators who want to hand a shared MCP server to every member"
source_of_truth: "The Console's Tenant settings → \"Operations\" → \"MCP distribution\" and \"Sign-in\" → \"Allowed networks\""
---

# Distribute an MCP server to everyone

English | [日本語](mcp-distribute.ja.md)

Register an MCP server the team uses once, and every member of the tenant can use it from their sessions and the assistant.
Choose whether everyone shares one token, each member enters their own value, or each member signs in.

## How it works

| What you do | What happens on the members' side |
|---|---|
| Add a server under "MCP distribution" in Tenant settings | Within five minutes it appears in each member's MCP server list, labelled "Tenant" |
| Choose how credentials are handled | With the value distributed it just works; per-member values show "Enter values", OAuth shows "Sign in" |
| Disable or delete it | From the next fetch, it is no longer handed to members |

It takes effect in sessions started after the fetch. Sessions already running do not change.

## Before you start

- **Only remote (Streamable HTTP) servers can be distributed.** stdio, which starts a command inside the workspace, cannot: distributing it would let an administrator run any command in everyone's container. Members who need stdio register it in their own settings
- **Try it in your own workspace first.** Register it for yourself and get a passing connection test before you distribute it ([Add an MCP server to your sessions](../user/integrations/mcp-servers.md))
- **On a deployment that restricts outbound traffic, the destination host has to be allowed.** See [below](#the-destination-host-is-not-allowed)

## Distribute a server

1. Open **Operations** → "**MCP distribution**" in Tenant settings and press "**Distribute an MCP server**"
2. Enter the "**Display name**" and the URL. The "**ID**" is filled in from the display name until you edit it
3. Choose "**Credentials**" and "**Authentication**" ([next section](#decide-how-credentials-are-handled))
4. If needed, change "**Used by**", "**Agents**" and "**Enabled**" under "**Advanced**". The closed heading shows the current values
5. Press "**Distribute**"

![The "Distribute an MCP server" form: display name, ID and URL, the credentials checkbox, the authentication choice and the closed Advanced section](../assets/admin-mcp-form-en-light.webp)

Distributed servers appear in members' **⚙ Settings → Connections → "MCP servers (used by sessions)"**, labelled "Tenant".
Members can only run the connection test and disable it in their own workspace; they cannot edit or delete it.

## Decide how credentials are handled

This is the most important decision in a distribution.

| Choice | What is distributed | Suited to |
|---|---|---|
| "**Each member enters the credential**" off | The header values too. Stored encrypted, and handed over only when the server starts | A shared read-only token everyone reads with the same rights |
| "**Each member enters the credential**" on | The destination and the header names only. Each member enters the value in their own workspace | Per-person tokens, or anything you want to tell people apart by in an audit |
| "**OAuth**" under "**Authentication**" | The destination only. Each member signs in with their own account | Hosted servers that accept only an OAuth sign-in |

**A distributed value can be read in plain text inside every member's container.** For a powerful token, or one you want to tell people apart by, have each member enter it.

- "**Authentication**" is one of "**None**", "**Bearer token**", "**Custom headers**" and "**OAuth**"
- With per-member values and "Bearer token", only the `Authorization` header name is distributed and members get a field for the token alone. Until a member enters it, the server is not used
- With the value distributed, the saved token is shown as `Bearer ***`
- Changing the URL to another host clears the saved header values. Enter values for the new host again

### Distribute an OAuth server

OAuth distributes no credential at all, so "Credentials" does not appear and no header can be added.
Each member's row shows "**Sign in**", and the grant stays in that member's workspace only. The server reaches only the sessions of members who signed in.

If you registered a client for Agent Fleet at the server's authorization server, enter it below. Members' sign-ins use it instead of each registering a client.

| Field | What to enter |
|---|---|
| "**Client ID registered by the tenant**" | The registered client's ID. Empty means each member's sign-in registers its own client |
| "**Authorization server (issuer)**" | The issuer of the authorization server the client is registered at. An https URL |
| "**Client secret**" | Only for a confidential client. Leave it empty for a public client |

- The secret is stored encrypted and is never sent to a workspace. The Control Plane makes the token requests, so check with the deployment administrator that the Control Plane can reach that authorization server
- Changing the issuer or the client ID asks for the secret again

These changes cut every member's sign-in, and members sign in again.

- Changing the URL, leaving OAuth, deleting the distribution (at the next fetch)
- Changing or removing the client. With a confidential client, earlier tokens can no longer be revoked and stay valid at the old authorization server until they expire
- Switching a distribution that carried values to OAuth deletes the stored header values

## When changes arrive

| What you do | On the members' side |
|---|---|
| Add or change | Each workspace fetches every five minutes; sessions started after that use it |
| Turn "**Enabled**" off | The definition stays, but it is distributed to no one |
| Delete | It disappears from each workspace at the next fetch |

- If a member has a personal registration with the **same name**, the distribution wins and the member's screen says so. Pick names that are unlikely to collide
- Members can press "**Fetch tenant set**" to take it in without waiting five minutes

## Rotate a token

When you retire a distributed token, **replace the distributed value with the new token first**, then disable the old one.
In the other order, every member's tools fail until the next fetch.

## Stop outside apps from connecting with OAuth

Outside apps such as claude.ai can connect to this tenant through MCP OAuth. Stop that in the **MCP OAuth** box under **Sign-in** → "**Allowed networks**" in Tenant settings.
The box appears only when the deployment offers MCP OAuth. It is allowed by default.

1. Clear "**Allow apps to connect through MCP OAuth**" and press "**Save**"
2. Press "**Save**" in the confirmation dialog

Once it is off:

- Every OAuth connection of this tenant is revoked at once, and the audit log records it
- The tenant leaves the choices on the consent screen, and code exchange and token refresh are refused
- Other tenants and personal access tokens (PATs) are not affected

Allowing it again does not restore the revoked connections. Members reconnect from their apps.
To revoke one connection only, the member revokes it under "Connected apps" in **⚙ Settings → Outside access → "MCP tokens & apps"**.

## When it goes wrong

### The destination host is not allowed

Where workspaces may connect is the deployment administrator's decision. That screen is not in Tenant settings.
A distributed server and a member's own registration both fail when the destination host is not allowed.

When the form or the list says "This destination (…) is not on the outbound allowlist, so the workspace cannot reach it.", press "**Request access**", write the reason and press "**Send request**".
Until the deployment administrator approves it, it shows as awaiting an administrator's approval.

### Asking the deployment administrator

What a tenant administrator cannot finish goes to the deployment administrator. Include these three, and they can act on it as is.

- **What**: the host name to allow (`api.example.com` or `.example.com`)
- **Why**: which MCP server, for what work
- **Until when**: for good, or for a period

The deployment administrator's side is in [Operating egress control](../operate/04-secure.md#operating-egress-control).

## Related

- [Add an MCP server to your sessions](../user/integrations/mcp-servers.md#what-you-can-change-by-origin): what members see
- [Allow memory use from outside](memory-outside.md): whether outside agents may use memory
- [Allow MCP servers as watch sources](watch-sources.md): whether members' MCP servers may feed watches

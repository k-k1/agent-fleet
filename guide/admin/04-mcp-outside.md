---
audience: "a tenant administrator distributing integrations to the team and deciding what outside apps may reach"
updated: "2026-10"
---

# 04. MCP distribution and outside access

English | [日本語](04-mcp-outside.ja.md)

Handing the team a shared tool is yours to do, and so is deciding whether apps and agents outside
Agent Fleet may connect to your tenant. Controlling outbound traffic belongs to the deployment
administrator.

## Distributing an MCP server to everyone in the tenant (your permission)

Tenant settings → **"Operations → MCP distribution"** distributes an MCP server to every member of
the tenant.
A distributed server appears in each member's ⚙ Settings → MCP servers labelled **tenant**,
ready to use from their assistants and sessions (what they see:
[member/20 Settings](../member/20-settings.md#mcp-servers)).

### Only remote (HTTP) can be distributed

**stdio (running a command inside the workspace) cannot be distributed.** Distributing one
would be equivalent to an admin running an arbitrary command in everybody's container. Only a
**Streamable HTTP endpoint** can be distributed, specified by URL and headers. A member who
needs stdio registers it personally in their own settings.

### Decide how credentials are handled (this is the important one)

**"Credential handling"** in the form offers two choices. It is the key design decision of a
distribution.

| Choice | What is distributed | Fits |
|---|---|---|
| Distribute the values | The header values as well (stored encrypted, handed over only when the server starts) | A shared read-only token: everyone reads with the same credential |
| **"Each member enters the credential"** | **Only the endpoint and the header names**; each member enters the value in their own workspace | Per-person tokens, and anything you want attributable in an audit |

**A distributed value is readable inside every member's container.** For anything you want
attributed to a person, or any token with real power, choose "each member enters the
credential". Distributed that way, the member's card offers an **"Enter values"** flow (until
they do, the server is not used for them).

The form is the same shape as a member's own: **display name** first (the **ID** is filled in
from it until you edit it), then the URL, the credential choice above, and an
**Authentication** selector: **None**, **Bearer token** or **Custom headers**. With "each member
enters the credential", Bearer token distributes only the `Authorization` header name and the member
is offered a token-only field; otherwise your token is stored encrypted and shown back as
`Bearer ***`. Targets, target agents and **enabled** sit in a closed **Advanced** section whose
summary line shows the current values; it opens by itself for a distribution that differs from the defaults.

### When it takes effect

- Each member's workspace **fetches the distribution every 5 minutes**.
- It actually applies **from the next session they start**. Sessions already running do not
  change.
- **Disabling** keeps the definition but hands it to nobody. **Deleting** removes it from each
  workspace at its next fetch.
- For a member who has a **personal entry with the same name**, the distributed one wins (their
  screen says so). Pick names that are unlikely to collide.

### Practical notes

- Register it personally in your own workspace first and run the **connection test**; move to
  distribution once you know the configuration works.
- To rotate a distributed token, **replace the value in the distribution first**, then revoke
  the old token. Doing it the other way round breaks the tool for everyone until the next
  fetch.
- On a deployment with restricted egress, a distribution still fails if **the destination host
  is not allowed**. Ask for the host first ([below](#outbound-traffic-is-not-yours-to-control)).

## MCP OAuth

Under **Tenant settings → Allowed networks**, the **MCP OAuth** panel controls whether external apps may connect to this tenant through OAuth. It appears only when the deployment serves MCP OAuth. The tenant setting is enabled by default; a tenant administrator or deployment administrator can change it.

Clear **Allow apps to connect through MCP OAuth**, choose **Save**, and confirm. Existing OAuth grants for this tenant are revoked and audited immediately. The tenant disappears from consent choices, code exchange and refresh fail, and its OAuth access tokens stop working at `/mcp`. Other tenants and Personal Access Tokens are unaffected.

An OAuth connection's `read` and `write` scopes also reach members' Agent Fleet memory, exactly as a token's do: the consent page says so. Whether they can is decided by **External memory access** ([below](#memory-from-outside-agent-fleet)) and each member's own setting, not by this panel. Block there hides the memory tools from OAuth connections and tokens alike.

Enabling the switch again permits new connections; it does not restore revoked connections. Members reconnect from their apps. They can also revoke individual connections in **Settings → Connections → MCP tokens → Connected apps**.

## Memory from outside Agent Fleet

A member can let an agent outside Agent Fleet (Claude Code or Claude Desktop on their own machine
holding their MCP token, or an app they connected through MCP OAuth such as claude.ai) list, search and read their Agent Fleet memory, and with a second choice
save and forget it ([member guide](../member/18-agent-memory.md#using-agent-fleet-memory-from-an-external-agent)).
The memory text then leaves the workspace and reaches that client's model. The secret scan stops
credentials, not customer names or internal context. This section is how you switch the whole
tenant off.

### The switch

Tenant settings → **External memory access** (under Operations). **Allow** (the starting
state of every tenant) lets members opt in; each member's own setting still starts **Off**, so
nothing changes for anyone until they choose. **Block** withholds the memory tools from the
tenant altogether.

While it is blocked:

- the `/mcp` endpoint does not list `memory_projects`, `memory_index`, `memory_search`,
  `memory_read`, `memory_save` or `memory_forget` for anyone in the tenant, and a call to one by
  name is refused as an unknown tool, whatever the member's own setting or token scope says;
- it applies from the next request, with no restart, to tokens already issued;
- members' settings are kept, so allowing it again restores what each member had chosen;
- memory used inside Agent Fleet (sessions, the Console's Agent memory screens) is not affected,
  and neither are the other `/mcp` tools.

If you cannot read the switch (for example the store is unavailable), the tools are withheld
rather than offered.

Changing it is recorded in the [audit log](03-audit-usage.md) as `tenant.external_memory`, with
`enabled=true` or `enabled=false`. Only a tenant administrator or a super_admin can change it. Turning off the
whole `/mcp` endpoint for a deployment is the operator's `AF_MCP_ENABLED`, a different switch.

## Outbound traffic is not yours to control

Which destinations the deployment's workspaces may reach is decided by the deployment
administrator, in a section that does not appear in your tenant settings ([how they run it](../operate/04-secure.md#operating-egress-control)).
It reaches you in one place: a distributed server (above) and a member's own registration both fail
when their destination host is not allowed. A member's request for a host waits on the member's side
as "waiting for an administrator's approval" until the deployment administrator approves or rejects it.

## A template for asking upstream

For anything outside a tenant_admin's permission, ask the super_admin / IT. Include these three
and they can act on it as-is.

- **What**: the host to allow (`api.example.com` or `.example.com`)
- **Why**: which MCP server, for which work
- **How long**: permanent, or until when

---

- Previously: [03 Audit and usage](03-audit-usage.md)
- What members see: [member/20 Settings](../member/20-settings.md#mcp-servers)


# 04. Distributing MCP servers and controlling outbound traffic

English | [日本語](04-mcp-egress.ja.md)

Audience: a tenant administrator distributing integrations to the team
Source of truth: the Console's tenant settings — if a screen disagrees with this page, the screen is right
Updated: 2026-08

> Audience: tenant admins who want to hand the whole team's agents a shared tool (an
> internal wiki, an issue tracker, a document search), and the super_admin who controls
> outbound traffic. The first is yours to do; the second belongs to IT / the deployment
> administrator.

## Distributing an MCP server to everyone in the tenant (your permission)

Tenant settings → **"Operations → MCP distribution"** distributes an MCP server to every member of
the tenant.
A distributed server appears in each member's ⚙ Settings → MCP servers labelled **tenant**,
ready to use from their assistants and sessions (what they see:
[member/12 Settings](../member/12-settings.md#mcp-servers)).

### Only remote (HTTP) can be distributed

**stdio — running a command inside the workspace — cannot be distributed.** Distributing one
would be equivalent to an admin running an arbitrary command in everybody's container. Only a
**Streamable HTTP endpoint** can be distributed, specified by URL and headers. A member who
needs stdio registers it personally in their own settings.

### Decide how credentials are handled (this is the important one)

**"Credential handling"** in the form offers two choices. It is the key design decision of a
distribution.

| Choice | What is distributed | Fits |
|---|---|---|
| Distribute the values | The header values as well (stored encrypted, handed over only when the server starts) | A shared read-only token — everyone reads with the same credential |
| **"Each member enters the credential"** | **Only the endpoint and the header names**; each member enters the value in their own workspace | Per-person tokens, and anything you want attributable in an audit |

**A distributed value is readable inside every member's container.** For anything you want
attributed to a person, or any token with real power, choose "each member enters the
credential". Distributed that way, the member's card offers an **"Enter values"** flow (until
they do, the server is not used for them).

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
  is not allowed**. Check the allowlist below first.

## Speech (tenant-wide settings)

Speech is super_admin only (the **Speech** section of the Admin modal). It starts and stops the VOICEVOX (Zundamon) engine and
holds the **tenant-wide pronunciation dictionary**, which applies to everyone's text-to-speech
(a member's personal dictionary overrides the same spelling). When a product name or an
in-house term is consistently mispronounced, ask the super_admin to add it there.

## Controlling outbound traffic (egress — super_admin only)

This is the **Traffic** section of the Admin modal. **It is super_admin only, so it does not appear
in your tenant settings.** It belongs
to IT / the deployment administrator, so ask them when you need it (see the request template at
the end). The following is here so you know what happens on their side.

### log-only and enforce

- **log-only** — observes without blocking. Start here to **learn what actually flows**.
- **enforce** — **blocks** anything not on the allowlist.

The order matters. Switching straight to enforce also kills the traffic you need — agent CLI
updates, MCP endpoints, and so on. The correct progression is **observe in log-only → settle
the allowlist → enforce**.

### The allowlist, and requests from members

- Entries are added as a **host** or as **`.suffix.example.com`**, with an optional reason, and
  **retired** when no longer needed. With no added entries, only the product's built-in
  allowances apply.
- When a member requests access from ⚙ Settings → MCP servers, it lands here as
  **"Proposed (needs approval)"**. Review it and **approve** (it joins the allowlist) or
  **reject**. On the member's side it sits showing "waiting for an administrator's approval".
- **Observed destinations** shows, over a chosen period, where traffic actually went and
  whether it was **allowed / blocked / a block candidate**. Clearing the block candidates while
  still in log-only is what makes the switch to enforce uneventful.

## A template for asking upstream

For anything outside a tenant_admin's permission, ask the super_admin / IT. Include these three
and they can act on it as-is.

- **What**: the host to allow (`api.example.com` or `.example.com`)
- **Why**: which MCP server, for which work
- **How long**: permanent, or until when

---

- Previously: [03 Audit and usage](03-audit-usage.md)
- What members see: [member/12 Settings](../member/12-settings.md#mcp-servers)

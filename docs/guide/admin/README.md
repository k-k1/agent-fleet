# Tenant Administrator Guide

English | [日本語](README.ja.md)

You are your team's **tenant administrator (tenant_admin)**. From the browser's tenant settings you add
members, keep track of how resources are being used, and review audit logs and usage. Many
administrators are also developing members themselves. For everyday development operations, read
[member/](../member/README.md). Here we talk only about the "managing the team" side.

This volume is written to be readable on its own, but the internal "mechanics" live in the
developer-facing [dev/](../../dev/README.md). To avoid duplicating facts, we link you there
whenever you want to know how things work.

## The short version: what you can do / what you can see

- **You can only see your own tenant.** Members and sessions of other departments (other tenants) are
  completely invisible to you. Only a super_admin (described below) can see the whole deployment.
- Settings that span tenants (creating new tenants, tenant-wide resource limits, idle auto-stop,
  egress controls, and so on) are outside your authority. When you need them, **ask your IT
  department / deployment administrator**.

| What you can do | Screen | Details |
|-----------|------|------|
| Add members | Members → Add member | [01-members.md](01-members.md) |
| Set per-member session limits | Members → the member → Operations | [02-limits.md](02-limits.md) |
| View a member's resources (memory / CPU / disk) and running sessions | Members → the member | [02-limits.md](02-limits.md) |
| Force-stop a running workspace | Members → the member → Operations | [02-limits.md](02-limits.md) |
| Get an overview of running sessions across the tenant | Sessions | [02-limits.md](02-limits.md) |
| See who changed what (audit log) | Audit | [03-audit-usage.md](03-audit-usage.md) |
| View usage (running time) and export it as CSV | Usage | [03-audit-usage.md](03-audit-usage.md) |
| Distribute a shared MCP server to the whole team | MCP distribution | [04-mcp-egress.md](04-mcp-egress.md) |
| Let people sign in with your own company's IdP | Sign-in methods | [operator/05 Sign-in methods](../operator/05-login-idp.md) |

## How to enter tenant settings

1. Open the button at the top right of the screen showing your email address (the account menu).
2. In the menu, choose **"Tenant settings"**.

This item is shown only to people who are a tenant_admin **somewhere**. Ordinary members don't see
it. The same menu also has "Settings" (your personal settings for display, agents, Git, and so on),
but that is something else. Managing the team is done from "Tenant settings".

The shield-icon **"Admin" item in that menu is super_admin only** (the deployment-wide
administrator), and you won't see it. It holds settings that reach past your tenant — creating
tenants, tenant-wide limits, and so on. Everything that is yours to do lives in "Tenant settings".

## Layout of tenant settings

The rail on the left holds three groups.

**Tenant**

- **Limits & idle** — **read-only**: this tenant's member count, its running workspaces and the
  limits currently in effect (workspaces / sessions). A deployment administrator decides them, so
  ask when you want one changed. → [02-limits.md](02-limits.md)

**Sign-in**

- **Sign-in methods** — register your own company's IdP (Entra ID / Okta / Keycloak …), or a
  **GitHub organization**, as a way into this tenant. Registering is not enough: a deployment
  administrator has to approve it. With GitHub you list the **allowed organizations** instead of
  an issuer and use an OAuth App created in your own organization — the approval is read as
  "these organizations, these domains", so **adding an organization sends it back for approval**.
  ★ **"How the same account is recognised"** only matters when your company and the deployment's
  own sign-in use **the same IdP through different app registrations**. Entra's default `sub`
  differs per app registration, so one person is two accounts across that button and this method;
  picking `oid` makes them one. Only values the IdP assigns can be picked — never one somebody
  can **assert**, such as an email address (asserting it would be enough to reach another
  person's account). **Changing it sends the row back for approval.**
  → [operator/05 Sign-in methods](../operator/05-login-idp.md)
- **Login rules** — **read-only** view of what is in effect for this tenant: the usable sign-in
  methods (★ narrowing these to your own methods locks out people who also belong to another
  tenant and sign in there — the same address at a different IdP is a different login; keep their
  method accepted and list it under *methods to keep off the sign-in page* instead), the
  auto-join domains and the invitable domains. Changing them is a deployment
  administrator's job; this panel exists so you can read *why* an invitation was refused without
  asking anyone. ★ If that person **holds both accounts**, they can instead link the second method
  to their own account under **Settings → Personal → Account → Add a sign-in method** (only a
  method asserting the same address, and its own entry rules still apply). Nothing for you to do.
  A method they no longer use can be **removed** from the same panel — except the only one left
  and the one they are signed in with, which would lock them out.
  ★ **"Methods to keep off the sign-in page" has no effect on the plain `/login`.** The page
  without a slug belongs to no tenant, so the deployment-wide methods stay on it (hiding them
  there would lock out everybody not in a tenant). For the setting to do anything, hand this
  tenant's people **its own sign-in URL (`/login/<slug>`)** — it is shown under Login rules on
  this panel.

**Operations**

- **Members** — the roster, with a drill-down into each member's detail (resources, sessions,
  operations). This is the section you will use most day to day. → [01-members.md](01-members.md) / [02-limits.md](02-limits.md)
- **Sessions** — an at-a-glance list of the sessions "running right now" within the tenant. → [02-limits.md](02-limits.md)
- **Usage** — per-member workspace running time over a chosen period. Exportable as CSV. → [03-audit-usage.md](03-audit-usage.md)
- **Audit** — search the record of change operations (who, when, what). → [03-audit-usage.md](03-audit-usage.md)
- **MCP distribution** — distribute a shared MCP server to every member of the tenant.
  → [04-mcp-egress.md](04-mcp-egress.md)

Egress control and the speech engine / shared dictionary are **super_admin only** and do not appear
on this screen. Ask your IT department / deployment administrator when you need
them. → [04-mcp-egress.md](04-mcp-egress.md)

Every section shows only your own tenant. A tenant switcher appears at the top of the rail **only
if you administer more than one tenant**.

## Who is responsible for what

Managing Agent Fleet is not something you (tenant_admin) can complete alone. Responsibilities are
divided like this.

| Matter | Owner |
|------|------|
| Adding members, session limits, force-stopping workspaces, reviewing audit and usage, **the whole offboarding sequence (remove → stop → clean home)** | **You (tenant_admin)** |
| Creating new tenants, setting tenant-wide resource limits and idle auto-stop, granting admin rights, changing the login rules, approving sign-in methods | Deployment-wide administrator (super_admin) |
| Distributing a shared MCP server to the team | **You (tenant_admin)** |
| Egress (external traffic) controls, the speech engine and the shared dictionary | super_admin |
| Switching the join mode (auto-join ⇄ invite-only), deployment-wide environment settings, backups, upgrades, host incident response | IT / SRE (operator) |

super_admin and IT are often the same person, and from your point of view both are "someone
upstream to ask". Whenever you notice something can't be completed with your own authority, consult
the people in [operator/README.md](../operator/README.md).

If you want the details of the permission model (RBAC) or the internal design of the admin API,
see the developer-facing [dev/03 Control plane](../../dev/03-control-plane.md).

## Once you become an administrator, first

Touring everything once on the day you first become a tenant administrator means you won't be lost
when it matters.

1. **Confirm you can enter tenant settings** — if "Tenant settings" appears in the account menu at
   the top right, you're fine. If it doesn't, your account doesn't have tenant_admin rights yet.
   Ask a super_admin to grant them.
2. **Look over the member list** — open "Members" and check who currently belongs.
   In auto-join mode, everyone who has logged in should already be listed. → [01-members.md](01-members.md)
3. **Know the limits currently in effect** — look at "Limits — Workspace / Session" under
   **Tenant › Limits & idle**. If they feel too strict or too loose, tenant-wide limits are
   adjusted by a super_admin, and per-person session limits by you yourself.
   → [02-limits.md](02-limits.md)
4. **Learn how to read audit and usage** — looking at the logs and running time once during normal
   operation lets you notice "this is different from usual" when something is wrong. → [03-audit-usage.md](03-audit-usage.md)

In daily operation, the typical rhythm is: first thing in the morning, get an overview of running
state under "Sessions"; at month end, export running time from "Usage".

---

- Read next: [01 Member management](01-members.md) → [02 Resource limits and sessions](02-limits.md) → [03 Audit and usage](03-audit-usage.md) → [04 MCP distribution and egress](04-mcp-egress.md)
- Guide index: [../README.md](../README.md)

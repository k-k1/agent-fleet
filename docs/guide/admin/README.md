# Tenant Administrator Guide

English | [日本語](README.ja.md)

You are your team's **tenant administrator (tenant_admin)**. From the browser admin screen you add
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
| Add members | Tenants → tenant → Add member | [01-members.md](01-members.md) |
| Set per-member session limits | Member detail → Operations | [02-limits.md](02-limits.md) |
| View a member's resources (memory / CPU / disk) and running sessions | Member detail | [02-limits.md](02-limits.md) |
| Force-stop a running workspace | Member detail → Operations | [02-limits.md](02-limits.md) |
| Get an overview of running sessions across the tenant | Sessions | [02-limits.md](02-limits.md) |
| See who changed what (audit log) | Audit | [03-audit-usage.md](03-audit-usage.md) |
| View usage (running time) and export it as CSV | Usage | [03-audit-usage.md](03-audit-usage.md) |
| Distribute a shared MCP server to the whole team | MCP | [04-mcp-egress.md](04-mcp-egress.md) |

## How to enter the admin screen

1. Open the button at the top right of the screen showing your email address (the account menu).
2. In the menu, choose **"Admin"**, the item with the shield icon.

This "Admin" item is shown only to tenant_admin and super_admin. Ordinary members don't see it.
The same menu also has "Settings" (your personal settings for display, agents, Git, and so on), but
that is something else. Managing the team is done from "Admin".

## Layout of the admin screen

The admin screen is divided into 7 sections by the tabs at the top.

- **Tenants** — the member list, with a drill-down into each member's detail (resources, sessions,
  operations). This is the section you will use most day to day. → [01-members.md](01-members.md) / [02-limits.md](02-limits.md)
- **Sessions** — an at-a-glance list of the sessions "running right now" within the tenant. → [02-limits.md](02-limits.md)
- **Usage** — per-member workspace running time over a chosen period. Exportable as CSV. → [03-audit-usage.md](03-audit-usage.md)
- **Audit** — search the record of change operations (who, when, what). → [03-audit-usage.md](03-audit-usage.md)
- **Egress** — control of external traffic (egress). This is super_admin only. If you, as a
  tenant_admin, open it, you'll see "You don't have permission". When egress controls become
  necessary, go to your IT department. → [04-mcp-egress.md](04-mcp-egress.md)
- **MCP** — distribute a shared MCP server to every member of the tenant. This one is yours to
  operate. → [04-mcp-egress.md](04-mcp-egress.md)
- **Speech** — start/stop the VOICEVOX engine and hold the tenant-wide pronunciation
  dictionary. super_admin only. → [04-mcp-egress.md](04-mcp-egress.md)

In every section, the option to switch tenants **appears only for super_admin**. You always see
only your own tenant.

## Who is responsible for what

Managing Agent Fleet is not something you (tenant_admin) can complete alone. Responsibilities are
divided like this.

| Matter | Owner |
|------|------|
| Adding members, session limits, force-stopping workspaces, reviewing audit and usage | **You (tenant_admin)** |
| Creating new tenants, setting tenant-wide resource limits and idle auto-stop, granting admin rights, cleaning home | Deployment-wide administrator (super_admin) |
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

1. **Confirm you can enter the admin screen** — if "Admin" appears in the account menu at the top
   right, you're fine. If it doesn't, your account doesn't have tenant_admin rights yet. Ask a
   super_admin to grant them.
2. **Look over the member list** — open your tenant under "Tenants" and check who currently belongs.
   In auto-join mode, everyone who has logged in should already be listed. → [01-members.md](01-members.md)
3. **Know the limits currently in effect** — look at "Limits — Workspace / Session" on the tenant
   card. If they feel too strict or too loose, tenant-wide limits are adjusted by a super_admin,
   and per-person session limits by you yourself.
   → [02-limits.md](02-limits.md)
4. **Learn how to read audit and usage** — looking at the logs and running time once during normal
   operation lets you notice "this is different from usual" when something is wrong. → [03-audit-usage.md](03-audit-usage.md)

In daily operation, the typical rhythm is: first thing in the morning, get an overview of running
state on the "Sessions" tab; at month end, export running time from "Usage".

---

- Read next: [01 Member management](01-members.md) → [02 Resource limits and sessions](02-limits.md) → [03 Audit and usage](03-audit-usage.md) → [04 MCP distribution and egress](04-mcp-egress.md)
- Guide index: [../README.md](../README.md)

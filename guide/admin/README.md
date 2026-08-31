# Administering a tenant

English | [日本語](README.ja.md)

Audience: a tenant administrator — someone responsible for a team's members, limits and integrations
Source of truth: the Console's tenant settings — if a screen disagrees with this shelf, the screen is right
Updated: 2026-08

You are your team's **tenant administrator**. From tenant settings you add members,
keep an eye on how resources are being used, and review the audit log and usage. Many
administrators are also working members: for everyday development read
[use/](../member/README.md), and treat this shelf as the "managing the team" half.

## The short version

- **You can only see your own tenant.** Members and sessions of other departments are
  completely invisible to you.
- Settings that span tenants — creating tenants, tenant-wide limits, idle auto-stop,
  egress control — are not yours. Ask your deployment administrator.
- Exactly what each role may do is [ref/roles.md](../ref/roles.md); this shelf is how.

## Chapters

1. [Members](01-members.md) — adding, removing, and the offboarding sequence
2. [Resource limits and sessions](02-limits.md) — per-member limits, what is running, force-stopping
3. [Audit and usage](03-audit-usage.md) — who changed what, running time, cost
4. [Distributing integrations](04-mcp-egress.md) — handing servers to the whole team
5. [Who may sign in, and from where](05-access.md) — sign-in methods, login rules, allowed networks, your own OAuth apps

## Getting in

The account menu at the top right (the button showing your email) has **Tenant
settings**. It appears only for someone who administers a tenant somewhere; ordinary
members do not see it. The same menu also has **Settings** — that is your *personal*
settings, a different thing.

The shield-icon **Admin** item is for the deployment administrator, and you will not
see it.

## What is yours, and what is not

The split is not a hierarchy of trust; it is a split of blast radius. Anything whose
effect crosses tenants, or that could lock people out, sits upstream of you.

| Matter | Owner |
|---|---|
| Adding and removing members, per-member session limits, force-stopping a workspace, reviewing audit and usage, **the whole offboarding sequence** | **you** |
| Distributing a shared integration server to the team | **you** |
| Registering a sign-in method, login rules you can read, restricting where members connect from, your tenant's own OAuth apps | **you** (a sign-in method still needs approval) |
| Creating tenants, tenant-wide limits and idle auto-stop, granting admin rights, changing the login rules, approving a sign-in method | deployment administrator |
| Egress control, the speech engine, the shared dictionary | deployment administrator |
| Backups, upgrades, host incident response | deployment administrator (often the same person as IT / SRE) |

When something cannot be completed with your own authority, the people to consult are
in [operate/](../operate/README.md).

## Your first day as an administrator

Touring it once, before it matters, is what stops you being lost when it does.

1. **Check you can get in** — "Tenant settings" in the account menu. If it is not
   there, you do not have the rights yet; ask a deployment administrator.
2. **Look over the member list** — who currently belongs
   ([01 Members](01-members.md)).
3. **Learn the limits in force** — under Limits & idle. They are read-only for you;
   per-member session limits are yours ([02 Limits](02-limits.md)).
4. **Read the audit and usage screens once** while everything is normal — that is what
   lets you notice "this is different from usual" later ([03 Audit](03-audit-usage.md)).

Day to day, the rhythm is: a glance at Sessions in the morning, an export from Running
time at month end.

## What belongs on this shelf

- Procedures whose effect lands on **other people**.
- How to read the administrator-facing views, and what each number does and does not
  include.

What does not: capability facts (they are [ref/](../ref/README.md)), anything a member
does for themselves ([use/](../member/README.md)), standing up or upgrading the
deployment ([operate/](../operate/README.md)), and implementation vocabulary — a tenant
administrator has no shell on the host, and this shelf never assumes one.

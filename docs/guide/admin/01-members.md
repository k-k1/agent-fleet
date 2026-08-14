# Member management

English | [日本語](01-members.ja.md)

You manage tenants and members on the **"Tenants"** tab of the admin screen. It is a
three-level drill-down: tenant list → tenant detail → member detail.
To go back up a level, use the "Back" button or follow the breadcrumb at the top of the screen
(tenant › member).

Since you can only see your own tenant, in most cases the first list contains just your one tenant.
Open it and you're straight into member management.

## Finding your way around

**Tenant list** — tenants appear as cards. Each card shows the display name, the member count, the
number of workspaces currently running ("N running"), and the tenant-wide limits
("Limits — Workspace: X / Session: Y"). The "New tenant" button is shown only to super_admin
(you won't see it).

**Tenant detail** — opening a tenant shows the "Members" list. For super_admin a limits-settings
section also appears above it, but as tenant_admin you won't see the limits section (for limits,
see [02-limits.md](02-limits.md)). Each member row shows a dot indicating running state, the
internal identifier (`user_key`), the email address, and the role (`member` / `tenant_admin`).
Clicking a row takes you to the member detail.

**Member detail** — the screen that gathers that member's workspace resources, running sessions,
and the various operations. Resources and sessions, force-stop, and limit settings are covered in
[02-limits.md](02-limits.md).

## Adding a member

At the very bottom of the "Members" section in the tenant detail there is an **"Add member"** form.

1. Enter the email address the member signs in with in the "email" field. Alternatively you
   can enter a key directly in the "or user_key" (internal identifier) field (if you enter an email,
   the key is derived from it automatically).
2. As tenant_admin, you can only add **`member` (regular members)**. The role selector is shown
   only to super_admin. If you want to make someone an administrator, ask a super_admin as
   described below.
3. Press "Add" and the member joins this tenant. You can add someone even before they have ever
   logged in (an invite in advance). When they log in for the first time, a workspace is
   provisioned based on this membership.

Point the newly added person to [member/](../member/README.md) in this guide.

### How email addresses relate to internal identifiers

Members are identified internally by a **user_key** (a short identifier derived from the email).
That is the monospace string you see in member rows and session lists. Normally you don't need to
think about it — entering an email determines the key automatically — but in audit logs and the
sessions overview the internal identifier takes center stage, so knowing the mapping of
"this identifier = this person" makes them easier to read. Even for the same person, membership
(and the workspace) is treated completely separately per tenant.

## Auto-join and invite-only

There are two ways, depending on deployment settings, for a new person to enter this tenant.

- **Auto-join (`AF_PROVISION=auto`, the default)** — anyone with an email address permitted to log
  in automatically becomes a member of the default tenant on first login. You don't need to add
  people one by one.
- **Invite-only (`AF_PROVISION=invite`)** — only people registered by an administrator via
  "Add member" can enter. Logins without a registration are rejected.

**Switching between these modes is outside your authority.** It is a deployment-wide environment
setting, so when you want it changed, ask your IT department / deployment administrator
([operator/README.md](../operator/README.md)). Even in invite-only mode, the add operation itself
is done via "Adding a member" on this page.

Note that deciding who can log in at all (the permitted email domains / addresses) is also IT's
domain. Someone who isn't permitted can't log in even if you add them to the tenant.

## Removing a member

The admin screen has **no button to delete a member on the spot**. When you want to cleanly revoke
a membership (removing someone who has left the company, for example), ask your IT department /
deployment administrator. As a practical interim measure, stopping that person's running workspace
("Force-stop the workspace" in [02-limits.md](02-limits.md)) reins in their resource occupancy and
activity.

## What the roles mean

Agent Fleet has 3 roles. The ones that mainly concern you (tenant_admin) are the first two below.

- **member (regular member)** — someone who writes code in their own workspace and runs sessions.
  They cannot enter the admin screen.
- **tenant_admin (tenant administrator)** — can manage members within this tenant, view resources,
  force-stop workspaces, and set session limits. **They cannot touch other tenants at all.** They
  cannot create tenants, change tenant-wide limits, clean home, or grant admin rights. = You.
- **super_admin** — the deployment-wide administrator. Sees all tenants and can create tenants,
  set limits, and grant rights such as "make someone a tenant_admin". super_admins are determined
  by environment configuration (`SUPER_ADMIN_EMAILS`) and are marked with a star in the member
  list of the admin screen.

**When you want to promote someone to tenant_admin**, you can't do it yourself. Only a super_admin
can grant roles, from the "Permissions" section of the member detail. A granted administrator's
authority is likewise limited to that tenant and does not affect other tenants.
The precise definition of the permission model is in
[dev/03 Control plane](../../dev/03-control-plane.md), and the table structure in
[dev/06 Data model](../../dev/06-data-model.md).

---

- Read next: [02 Resource limits and sessions](02-limits.md)
- Back to: [admin/README.md](README.md)

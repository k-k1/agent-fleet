# Member management

English | [日本語](01-members.ja.md)

Audience: a tenant administrator managing who belongs to the team
Source of truth: the Console's tenant settings — if a screen disagrees with this page, the screen is right
Updated: 2026-08

You manage members under **"Operations → Members"** in the tenant settings rail. It is a
two-level drill-down: roster → member detail. To go back up a level, use the "Back" button or
follow the breadcrumb at the top of the panel (Members › the person).

## Finding your way around

**The tenant's numbers** — **Tenant › Limits & idle** in the rail shows this tenant's display
name, its member count, the number of workspaces currently running ("N running"), and the
tenant-wide limits ("Limits — Workspace: X / Session: Y"). This is **read-only** (a super_admin
sets the caps — see [02-limits.md](02-limits.md)).

**Roster** — each member row shows a dot indicating running state, the internal identifier
(`user_key`), the email address, and the role (`member` / `tenant_admin`). Clicking a row takes you
to the member detail.

**Member detail** — the screen that gathers that member's workspace resources, running sessions,
and the various operations. Resources and sessions, force-stop, and limit settings are covered in
[02-limits.md](02-limits.md).

## Adding a member

At the very bottom of the roster there is an **"Add member"** form.

1. Enter the email address the member signs in with in the "email" field. Alternatively you
   can enter a key directly in the "or user_key" (internal identifier) field (if you enter an email,
   the key is derived from it automatically).
2. As tenant_admin, you can only add **`member` (regular members)**. The role selector is shown
   only to super_admin. If you want to make someone an administrator, ask a super_admin as
   described below.
3. Press "Add" and the member joins this tenant. You can add someone even before they have ever
   logged in (an invite in advance). When they log in for the first time, a workspace is
   provisioned based on this membership.

Point the newly added person to [member/](../use/README.md) in this guide.

### How email addresses relate to internal identifiers

Members are identified internally by a **user_key** (a short identifier derived from the email).
That is the monospace string you see in member rows and session lists. Normally you don't need to
think about it — entering an email determines the key automatically — but in audit logs and the
sessions overview the internal identifier takes center stage, so knowing the mapping of
"this identifier = this person" makes them easier to read. Even for the same person, membership
(and the workspace) is treated completely separately per tenant.

## Auto-join and invite-only

There are two ways, depending on deployment settings, for a new person to enter this tenant.

- **Invite-only (`AF_PROVISION=invite`, what new installs start with)** — only people registered by
  an administrator via "Add member" can enter. Anyone else still signs in fine and lands on a
  **"you haven't been invited yet"** page. That page shows the address they signed in with — use
  exactly that address when they ask to be added (it is not necessarily their display name, or the
  address they usually give out).
- **Auto-join (`AF_PROVISION=auto`)** — anyone with an email address permitted to log
  in automatically becomes a member of the default tenant on first login. You don't need to add
  people one by one.

**Switching between these modes is outside your authority.** It is a deployment-wide environment
setting, so when you want it changed, ask your IT department / deployment administrator
([operator/README.md](../operate/README.md)). Even in invite-only mode, the add operation itself
is done via "Adding a member" on this page.

Note that deciding who can log in at all (the permitted email domains / addresses) is also IT's
domain. Someone who isn't permitted can't log in even if you add them to the tenant.

## Removing a member

Offboarding is **entirely yours to do**. The department is the one that knows who left, so the
sequence isn't half a ticket to IT. From the "Operations" section of the member detail, in this
order:

1. **Remove member** — takes them off the roster and stops access. **Do this first**: the signed
   session cookie cannot be revoked individually, so this — effective from the next request — is
   what actually cuts access. The workspace, its home and stored credentials are kept, so a mistake
   is undone by adding the same email address again.
2. **Force-stop the workspace** — stop what is running ([02-limits.md](02-limits.md)).
3. **Clean home** — erase the contents of home. **This cannot be undone.**

Someone you removed stays on the roster marked "removed". That is so steps 2 and 3 remain reachable
afterwards — they have not vanished.

**Deleting the row for good.** After you have destroyed a removed member's workspace, the same
"Operations" box offers **Delete this member**. It appears only once the workspace is gone: while
it is still there, deleting the row would leave the home and its cloud resources with nothing
pointing at them, and the server refuses for the same reason.

- **Deleted:** their quotas, access tokens, SSM settings, schedules, memos, notifications and
  session shares.
- **Kept:** the audit log, cloud cost and occupancy. Offboarding must not be able to erase its own
  record, and last month's cost total must not change after the fact.

There is no undo. Inviting the same person again starts a brand new member.

## What the roles mean

Agent Fleet has 3 roles. The ones that mainly concern you (tenant_admin) are the first two below.

- **member (regular member)** — someone who writes code in their own workspace and runs sessions.
  They cannot enter tenant settings.
- **tenant_admin (tenant administrator)** — can manage members within this tenant, view resources,
  force-stop workspaces, set session limits, remove members and clean their home. **They cannot
  touch other tenants at all.** They cannot create tenants, change tenant-wide limits, grant admin
  rights, or change the login rules. = You.
- **super_admin** — the deployment-wide administrator. Sees all tenants and can create tenants,
  set limits, and grant rights such as "make someone a tenant_admin". super_admins are determined
  by environment configuration (`SUPER_ADMIN_EMAILS`) and are marked with a star in the member
  roster.

**When you want to promote someone to tenant_admin**, you can't do it yourself. Only a super_admin
can grant roles, from the "Permissions" section of the member detail. A granted administrator's
authority is likewise limited to that tenant and does not affect other tenants.
The precise definition of the permission model is in
[dev/03 Control plane](../build/03-control-plane.md), and the table structure in
[dev/06 Data model](../build/06-data.md).

---

- Read next: [02 Resource limits and sessions](02-limits.md)
- Back to: [admin/README.md](README.md)

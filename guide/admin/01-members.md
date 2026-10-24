---
audience: "a tenant administrator managing who belongs to the team"
---

# 01. Member management

English | [日本語](01-members.ja.md)

You manage members under **"Operations → Members"** in the tenant settings rail. It is a
two-level drill-down: roster → member detail. To go back up a level, use the "Back" button or
follow the breadcrumb at the top of the panel (Members › the person).

## Finding your way around

**The tenant's numbers**: **Tenant › Limits & idle** in the rail shows this tenant's display
name, its member count, the number of workspaces currently running ("N running"), and the
tenant-wide limits ("Limits — Workspace: X / Session: Y"). This is **read-only** (a super_admin
sets the caps; see [02-limits.md](02-limits.md)).

The roster has one card per member. Clicking a card takes you to the member detail.

### The roster cards

Each card shows the email address with the internal identifier (`user_key`) under it, the role
(`member` / `tenant_admin`), and a state badge that says in words what the workspace is doing:
Running, Starting, Stopped, Never started, Stopped automatically (the Control Plane stopped it)
or Removed. A removed member whose workspace is still up keeps Running or Starting, since
stopping it is the next offboarding step; the role line then reads "removed".

Below that are the sessions figure ("3 live / 5 total", with the limit and a thin bar next to the
numbers when the member has a session limit, and how long ago that count was taken), the disk
gauge, the auto-stop outlook and the machine size. A stopped card also says when the member was
last active, and a running card that is on screen adds live memory and CPU. The next two sections
say when each of these appears and how fresh it is.

Above the cards are the counts ("N of M running", where starting workspaces count as running, and
"N need attention"), a filter (All / Running / Needs attention / Stopped / Removed), a sort and a
search box. The sort is "Needs attention first" (the default), "Name", "Last active" (members
with no activity recorded last) or "Disk usage" (fullest first, members with no percentage to show last). The
search box ("Search name or e-mail") matches the email address and the `user_key`; the roster
holds no other name to match.

### Needs attention

A card that needs you has a warning edge and a **Needs attention** mark. Hovering the mark lists
the reasons, which are:

- "The Control Plane stopped this workspace": the platform stopped it itself (see
  [02-limits.md](02-limits.md#viewing-a-members-resources)).
- "A session is still holding it past its deadline": the workspace is running and a session is
  still holding it open after its deadline.
- "The home disk is nearly full": the home is over 90 % full or has less than
  2 GiB free (the same rule as the member detail's notice).
- "The home disk is full or almost out of space": the same condition, escalated at 95 % or more
  or when less than 2 GiB is left.
- "A CLI failed to install at the last start": a full home is the usual cause. The per-CLI reason
  is in the member detail. A CLI whose upgrade was merely skipped for lack of space, with the old
  version still installed and working, does not raise this flag; the member detail notes it instead.

In the default order, cards whose home disk is full or almost out of space come first, then cards
with any other reason, then everyone else, each group by name.

### Where the numbers come from

The session counts, last active, disk gauge and install flag are what the Control Plane recorded,
so opening the roster makes no call to any workspace. They can lag what the member sees.

- **Sessions** come from the Control Plane's copy of each workspace's session list, which it
  refreshes about every five minutes for every running workspace (`AF_USAGE_SAMPLE_INTERVAL`).
- **The disk gauge** appears where the home is the member's own volume (ECS on EC2, Kubernetes) or
  the deployment can read the home folder's size (local, native), with its numbers in text, for
  example "Disk 19.0 / 20.0 GiB (95%)". Without a disk quota (local, native) only the used size is
  shown. On ECS Fargate the working disk is wiped on every stop, so there is no gauge, only the
  configured size. The gauge is the last reading the Control Plane took while the workspace ran
  (about every five minutes); on a stopped card it says how old that reading is.
- **The install flag** is read at the same time. A stopped card keeps the last start the Control
  Plane observed (a start that began and ended between two readings is not seen).
- **Live memory and CPU** are the one exception. While a **running** card is on screen (and the
  browser tab is in the foreground), the Console reads that member's workspace about every 15
  seconds, at most three at a time, and shows for example "Memory 2.0 / 8.0 GiB (25%)" and "CPU
  25% of 4 vCPU limit". A card shows "Live usage: reading…" until its first reading. The CPU bar is
  a share of the member's CPU limit where the runtime enforces one; otherwise only the figure is
  shown, as "CPU 200% (100% = 1 core)". Cards scrolled out of view, hidden by the filter, or
  stopped are not asked, and nothing is asked while the Members page is closed.

### Member detail

The member detail gathers that member's workspace resources, running sessions, and the various
operations. Resources and sessions, force-stop, and limit settings are covered in
[02-limits.md](02-limits.md).

Its **"Size and limits"** card names each CLI that failed to install at the last start, with the
reason. On ECS on EC2, where the home is the member's own volume, it also shows a notice when the
home is nearly full. That notice uses the member's own thresholds: it appears above 90 % or with
less than 2 GiB free and goes away once usage is below 85 % with more than 3 GiB free. The roster
card warns by the same rule, so for a running workspace with a valid own-home reading, a card marked nearly full has this notice behind it.
The notice needs a running workspace; a stopped or unreachable one shows none, and the install
flag appears only when a failure was actually recorded.

## Adding a member

At the very bottom of the roster there is an **"Add member"** form.

1. Enter the email address the member signs in with in the "email" field. Alternatively you
   can enter a key directly in the "or user_key" (internal identifier) field (if you enter an email,
   the key is derived from it automatically). A key entered directly must already be in that
   derived form (lowercase letters and digits separated by single hyphens, at most 40
   characters) because it becomes the name of the member's home directory; anything else is
   refused with a message that suggests the accepted form. Keys the server generated itself, as
   shown on the member cards, are accepted even when they run past 40 characters.
2. As tenant_admin, you can only add **`member` (regular members)**. The role selector is shown
   only to super_admin. If you want to make someone an administrator, ask a super_admin as
   described below.
3. Press "Add" and the member joins this tenant. You can add someone even before they have ever
   logged in (an invite in advance). When they log in for the first time, a workspace is
   provisioned based on this membership.

Point the newly added person to [member/](../member/README.md) in this guide.

### How email addresses relate to internal identifiers

Members are identified internally by a **user_key** (a short identifier derived from the email).
That is the monospace string you see on member cards and in session lists. Normally you don't need to
think about it, since entering an email determines the key automatically, but in audit logs and the
sessions overview the internal identifier is what is shown, so knowing the mapping of
"this identifier = this person" makes them easier to read. Even for the same person, membership
(and the workspace) is treated completely separately per tenant.

## Auto-join and invite-only

There are two ways, depending on deployment settings, for a new person to enter this tenant.

- **Invite-only (`AF_PROVISION=invite`, what new installs start with)**: only people registered by
  an administrator via "Add member" can enter. Anyone else still signs in fine and lands on a
  **"you haven't been invited yet"** page. That page shows the address they signed in with; use
  exactly that address when they ask to be added (it is not necessarily their display name, or the
  address they usually give out).
- **Auto-join (`AF_PROVISION=auto`)**: anyone with an email address permitted to log
  in automatically becomes a member of the default tenant on first login. You don't need to add
  people one by one.

**Switching between these modes is outside your authority.** It is a deployment-wide environment
setting, so when you want it changed, ask your IT department / deployment administrator
([operate/README.md](../operate/README.md)). Even in invite-only mode, the add operation itself
is done via "Adding a member" on this page.

Note that deciding who can log in at all (the permitted email domains / addresses) is also IT's
domain. Someone who isn't permitted can't log in even if you add them to the tenant.

## Removing a member

Offboarding is **entirely yours to do**. The department is the one that knows who left, so the
sequence isn't half a ticket to IT. From the "Operations" section of the member detail, in this
order:

1. **Remove member**: takes them off the roster and stops access. **Do this first**: the signed
   session cookie cannot be revoked individually, so this, effective from the next request, is
   what actually cuts access. The workspace, its home and stored credentials are kept, so a mistake
   is undone by adding the same email address again.
2. **Force-stop the workspace**: stop what is running ([02-limits.md](02-limits.md)).
3. **Clean home**: erase everything in their home **except their logins and connections** (git
   credentials, SSH keys, the encrypted connection store, Claude and Codex logins). **This cannot be
   undone.** Those logins and connections go only when the workspace is destroyed (below).
4. **Delete backups**: shown only where the deployment keeps backup copies of homes, and only
   when this member has some. Clean home leaves them, and each one still holds the home as it was
   when it was taken. Do it after Clean home: while the home still exists, the tenant's backup
   schedule goes on taking copies of it. **This cannot be undone.**

Clean home is not offered on every deployment ([ref/deploy-targets](../ref/deploy-targets.md)).
Where it is missing, the home is removed by **Destroy workspace** instead, which lists anything it
could not delete. On Fargate (`ecs`) Clean home and Destroy run as a task that takes a few
minutes: the button answers at once, and the outcome is written to the audit log when it is done.
On the EC2 slot pool (`ecs-ec2`) Destroy runs the same task for what the member keeps on EFS, and
answers the same way. If the Control Plane restarts meanwhile, the outcome entry is not written;
see [ref/deploy-targets](../ref/deploy-targets.md) for how to tell how it ended.

Someone you removed stays on the roster marked "removed". That is so steps 2 to 4 remain reachable
afterwards; they have not vanished.

**Deleting the row for good.** After you have destroyed a removed member's workspace, the same
"Operations" box offers **Delete this member**. It appears only once the workspace is gone: while
it is still there, deleting the row would leave the home and its cloud resources with nothing
pointing at them, and the server refuses for the same reason.

- **Deleted:** their quotas, access tokens, SSM settings, schedules, memos, notifications and
  session shares.
- **Kept:** the audit log, cloud cost and occupancy. Offboarding must not be able to erase its own
  record, and last month's cost total must not change after the fact.

There is no undo. Inviting the same person again starts a brand new member.

## Rotating a member's git token

Each member reaches the tenant's internal git repositories (the "Internal repos" tab) with a token
their workspace holds for them. If that token may have leaked out of the workspace (pasted into a
log, copied to another machine), press **Rotate git token** in the "Operations" section of the
member detail. The current token stops working at once, for anyone holding it, and the member gets
a new one; nothing else about the member changes.

- **A running workspace** is handed the new token straight away, and the member carries on without
  noticing. If the message says the workspace did not take it, restart the workspace: until then,
  clones in it cannot fetch or push.
- **A stopped workspace** gets the new token at its next start.
- A removed member can be rotated too. Do it before inviting them back if their old token may be
  out there: coming back restores the same membership, and with it the same token.

The rotation is recorded in the audit log. To change every member's token at once, the operator
changes the deployment's master key instead.

## What the roles mean

Agent Fleet has 3 roles. The ones that mainly concern you (tenant_admin) are the first two below.

- **member (regular member)**: someone who writes code in their own workspace and runs sessions.
  They cannot enter tenant settings.
- **tenant_admin (tenant administrator)**: can manage members within this tenant, view resources,
  force-stop workspaces, set session limits, rotate a member's git token, remove members, clean
  their home and delete its backups. **They cannot
  touch other tenants at all.** They cannot create tenants, change tenant-wide limits, grant admin
  rights, or change the login rules. This is you.
- **super_admin**: the deployment-wide administrator. Sees all tenants and can create tenants,
  set limits, and grant rights such as "make someone a tenant_admin". super_admins are determined
  by environment configuration (`SUPER_ADMIN_EMAILS`) and are marked with a star in the member
  roster.

**When you want to promote someone to tenant_admin**, you can't do it yourself. Only a super_admin
can grant roles, from the "Permissions" section of the member detail. A granted administrator's
authority is likewise limited to that tenant and does not affect other tenants.
The precise definition of the permission model is in
the developer documentation, along with the table structure.

---

- Read next: [02 Resource limits and sessions](02-limits.md)
- Back to: [admin/README.md](README.md)

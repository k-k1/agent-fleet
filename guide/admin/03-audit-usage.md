# Audit log and usage

English | [日本語](03-audit-usage.ja.md)

Audience: a tenant administrator reviewing what happened and what it cost
Source of truth: the Console's tenant settings — if a screen disagrees with this page, the screen is right
Updated: 2026-08

The audit log lets you trace "who did what"; running time tallies "how much was used". Both are
available from the tenant settings rail, scoped to your own tenant.

## Audit log ("Audit")

The **"Audit"** section is the **record of change operations** that happened within the tenant. Each
row is one operation, with the following columns.

- **Time** — when it was done.
- **Action** — what was done (`fs.*` = file operations, `git.*` = commits, checkouts, and so on,
  `repo.*` = repository creation / deletion, `session.*` = session creation / fork / stop, etc.).
- **Actor** — who did it (email address). Besides human operations, the actor can also be an
  external Claude client (MCP) or a system action (auto-stop, etc.).
- **Target** — which file, repository, or session it was done to.

Typing "Action / target / user" into the search field at the top filters the list. The log does not
auto-refresh, so press the refresh button when you want the latest. The item count appears at the
top right.

### What is recorded, and what is not

Only **operations that change or destroy something** are recorded. This is a deliberate line, so
keep it in mind.

- **Recorded** — operations that "change state": file upload / creation / rename / deletion, git
  commit / checkout / fetch, repository clone / deletion, session creation / fork / stop, and so on.
- **Not recorded** — plain **reads** (just opening and viewing a file, just browsing a list) are
  not kept by default. And **raw terminal input/output (the very characters flowing across the
  screen) is not stored**. This is by design, to avoid the risk of passwords or tokens slipping in.

Therefore the audit log cannot trace "what exactly that member typed in the terminal". What it can
trace is "when, who, against which file or session, made what kind of change". The design intent
behind the recording scope is laid out in [dev/07 §7.7 Audit](../build/07-security.md).

## Running time ("Running time")

The **"Running time"** section tallies each member's **workspace running time**. What is counted is
"how much infrastructure was occupied" — the time a workspace was up — not Claude fees. Claude is
"bring your own" (BYO): each member logs in with their own subscription (seat), so the cost borne
by the operator is the occupancy time. The value is sampled roughly every 5 minutes, so it is an
approximation with some margin of error.

- **Period** — set dates with "From" and "To", and press "Apply" to take effect.
- **Per member** — each member's running time appears as a bar chart, with "Total running" and the
  "Members" count at the top.
- **CSV** — the "CSV" button exports the data for the displayed period and scope as-is (usable for
  cost allocation — showback — and internal reporting).

The option to switch tenants appears only for super_admin. Your running-time screen is always scoped
to your own tenant.

---

## FAQ

**Q. Is adding members invite-based or automatic?**
It depends on deployment settings. With auto-join (the default), permitted logins join
automatically on first login; with invite-only, only people an administrator has added can enter.
Switching the mode is IT's domain. For details, see "Auto-join and invite-only" in
[01-members.md](01-members.md).

**Q. What happens to a member who hits a limit?**
Nothing that is already running gets stopped. Only the attempt to start something new is rejected,
and the Console shows a "limit reached" message. Once they tidy up, they can continue. For details,
see "What members experience when a limit is hit" in [02-limits.md](02-limits.md).

**Q. Can I only see my own tenant?**
Yes. Members, sessions, audit, and running time are all scoped to your own tenant. The option to switch
tenants is not shown. Only a super_admin can see the whole deployment.

**Q. Can I see in the audit log the actual commands a member typed in the terminal?**
No. Raw terminal input/output is not stored. What the audit log can trace is the kind, actor,
target, and time of change operations (files, git, sessions, etc.).

**Q. Does running time include Claude fees?**
No. What is counted is workspace running time (infrastructure occupancy). Claude charges sit on each member's
own subscription.

**Q. I want to promote a member to administrator, but there's no button.**
As tenant_admin you cannot grant rights. Only a super_admin can grant roles. Ask your IT
department / deployment administrator ("What the roles mean" in [01-members.md](01-members.md)).

**Q. I want to delete a member who has left the company.**
You can. From "Operations" on the member detail: **Remove member → Force-stop the workspace →
Clean home**, in that order ("Removing a member" in [01-members.md](01-members.md)).

**Q. Where is egress (external traffic) control?**
Egress (external traffic) control is super_admin only. When traffic control becomes necessary,
consult your IT department ([operator/README.md](../operate/README.md)).

**Q. If I force-stop a workspace, does that member's work disappear?**
No. The container merely stops for the moment; the contents of home (repositories and settings)
remain. The member can start it again from the Console.

---

- Back to: [02 Resource limits and sessions](02-limits.md)
- Guide index: [../README.md](../member/README.md)

---
audience: "someone who has just become their team's tenant administrator"
source_of_truth: "the Console's tenant settings (if a screen disagrees with this guide, the screen is right)"
---

# What a tenant administrator does

English | [日本語](overview.ja.md)

A tenant administrator manages the team's members, resources, sign-in methods and defaults from **Tenant settings** in the Console.
This page says how to get in, which parts are your job, and what to look at on your first day.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Add and remove members | Lets the people on the roster into the tenant when they sign in, and gives them a workspace |
| Check on members and act on what needs attention | Collects each workspace's figures and notifies you when a home runs low, an install fails and the like |
| Decide the sign-in methods and the networks people connect from | Keeps the tenant out of reach for any other method or network |
| Set defaults such as branch names and the agent new members are pointed at | Delivers those defaults to every member's workspace and screen |

- **You see your own tenant only.** Members and sessions of other tenants are invisible to you
- Most administrators also work as members. Your own work goes through **Settings**, managing the team through **Tenant settings**

## Open tenant settings

Choose **Tenant settings** in the account menu at the top right (the button showing your email address).

- **Tenant settings** appears only for someone who administers a tenant
- **Settings** in the same menu is your personal settings
- **Admin** is the deployment administrator's screen and does not appear for a tenant administrator

The rail on the left is grouped into Tenant, Sign-in, Integrations and Operations.

## Your job and the deployment administrator's

Whatever takes effect across tenants, or could lock people out, belongs to the deployment administrator.
Whatever stays inside one team belongs to its tenant administrator, including the whole sequence of removing a member.

| Matter | Owner |
|---|---|
| Adding and removing members, and the steps of removing one | **you** |
| Per-member session limits, home size, and the machine a member's workspace runs on (where the deployment offers it) | **you** |
| Force-stopping a workspace; reading the audit log, running time and cloud cost | **you** |
| Which members may use the inference engines (within what the deployment administrator allows your tenant), and taking models in when allowed | **you** |
| Whether the tenant uses the limit board | **you** |
| Distributing MCP servers, MCP OAuth connections, and allowing members' memory to be used from outside | **you** |
| Branch naming rules and the agent new members are pointed at | **you** |
| Registering a sign-in method, allowed networks, integration OAuth apps | **you** (a sign-in method needs approval) |
| Creating tenants, tenant-wide limits and idle auto-stop, granting admin rights | deployment administrator |
| Changing the sign-in rules, approving a sign-in method, whether your tenant may use the self-hosted engines | deployment administrator |
| Egress control, the speech engine, the shared dictionary, MCP sign-in hosts, the development OIDC IdP | deployment administrator |
| Backups, upgrades, host incident response | deployment administrator |

What each role may do is listed in [Roles and what they may do](../ref/roles.md).
Anything your rights do not cover goes to the deployment administrator ([the operator's guide](../operate/README.md)).

## On your first day as an administrator

Look around once while nothing is happening, and you will not be lost when something does.

1. Check that the account menu has **Tenant settings**. If not, you do not have the rights yet; ask a deployment administrator
2. Under **Operations → Members**, see who is in the tenant ([Check on your members](members/roster.md))
3. Under **Tenant → Limits & idle**, see the limits in force. The tenant-wide limits are read-only for you
4. Read **Operations → Audit** and **Running time** once while all is normal. Later you will notice when something looks different

Day to day, it comes down to a glance at **Sessions** in the morning and an export from **Running time** at the end of the month.

## Related

- [Add a member](members/add.md)
- [Choose how people sign in](access/signin-methods.md)
- [Roles and what they may do](../ref/roles.md)

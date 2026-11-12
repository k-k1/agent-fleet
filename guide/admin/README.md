---
audience: "a tenant administrator — someone responsible for a team's members, limits and integrations"
source_of_truth: "the Console's tenant settings — if a screen disagrees with this guide, the screen is right"
---

# Administering a tenant

English | [日本語](README.ja.md)

Each page covers one thing you want to do as the person managing the team.
For your own work, see [Find what you want to do](../user/README.md).

## Getting started

| Page | What you can do |
|---|---|
| [What a tenant administrator does](overview.md) | Learn how to open tenant settings, where your job ends and the deployment administrator's begins, and what to look at on your first day |

## Members

| Page | What you can do |
|---|---|
| [Check on your members](members/roster.md) | See workspaces, disks, sessions and what needs attention, on the roster and in the member detail |
| [Add a member](members/add.md) | Put someone on the roster by email address, and know invite-only from auto-join |
| [Remove a member](members/remove.md) | Offboard someone: stop their access, erase their home and backups, delete them from the roster |
| [Replace a member's Git token](members/git-token.md) | Replace an internal Git token that may have leaked |

## Sign-in

| Page | What you can do |
|---|---|
| [Choose how people sign in](access/signin-methods.md) | Register your company's IdP or a GitHub organisation, and read the login rules |
| [Limit the networks people sign in from](access/networks.md) | Narrow where your tenant can be used from to a set of address ranges |
| [Choose the OAuth apps for integrations](access/oauth-apps.md) | Decide which app "Connect with OAuth" uses for GitHub, Bitbucket and Jira |

## Resources

| Page | What you can do |
|---|---|
| [See how resources are used](resources/overview.md) | See members' resources and the sessions running now, and narrow down the cause when things are slow |
| [Set session limits](resources/session-limits.md) | Set each member's session limit, and check the tenant-wide limits and the limit board |
| [Choose who may use the inference engines and which models](resources/engines.md) | Choose the members who may use the self-hosted engines, and take models in when allowed |
| [Set the workspace size](resources/workspace-size.md) | Set memory, CPU, disk and the machine, and grow a home |
| [Stop workspaces left idle](resources/idle-stop.md) | Read the idle-stop outlook, and force-stop a workspace that runs away |
| [Start the workspace at sign-in](resources/start-at-signin.md) | Have a stopped workspace start when its member opens the Console |

## Team defaults

| Page | What you can do |
|---|---|
| [Set the branch naming rules](defaults/branch-rules.md) | Line up the branch names and starting branches proposed when work starts from an issue |
| [Set member defaults](defaults/members.md) | Decide which agent new members are pointed at |

## Audit and usage

| Page | What you can do |
|---|---|
| [Read the audit log](audit.md) | Check when, who, against what and what kind of change was made in your tenant |
| [Read running time and cloud cost](usage.md) | See each member's running time and cloud cost for a period and by month, and export it as CSV |

## Outside connections

| Page | What you can do |
|---|---|
| [Distribute an MCP server to everyone](mcp-distribute.md) | Hand a shared MCP server to every member of the tenant, and stop outside apps' OAuth connections |
| [Allow memory use from outside](memory-outside.md) | Decide whether members may use memory from agents outside Agent Fleet |
| [Allow MCP servers as watch sources](watch-sources.md) | Decide whether members may make their own MCP servers a source for the `watch` tool |

What each role may do is in [Roles and what they may do](../ref/roles.md).

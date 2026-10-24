---
audience: "a tenant administrator setting the team's defaults: branch names, and the agent new members are pointed at"
updated: "2026-10"
---

# 06. Team defaults

English | [日本語](06-team-defaults.ja.md)

Two settings under Operations decide what a member starts from: the branch names their launch
dialog proposes, and the agent the welcome card names. Both are defaults that advise; nothing is
refused.

## Branch naming rules

When a member launches work on an issue, the launch dialog fills in a branch name and the branch
to start from. The rules behind that come in four layers, strongest first:

1. **the repository**: what it declares itself (`.agent-fleet/branches`, git-flow, a Bitbucket
   branching model);
2. **the member**: their own template in the work-items settings, and their own rules;
3. **the tenant**: the rules on this screen;
4. **the built-in default**: `{prefix}{ref}-{slug}` off the current branch, e.g.
   `feature/45-empty-list`.

The layers merge **field by field**: a repository that declares only where branches start keeps
the name from your rule, and a member who sets only a template keeps your base. So your rules are
the team's default, and anything a repository or a member says for themselves wins.

**Rules only advise.** A name that does not follow them gets a note in the launch and rename
dialogs; nothing is refused, and there is no switch to make them binding.

### Editing the rules

Tenant settings → **Branch naming rules** (under Operations). The rules are a JSON list; each rule
has these fields, all optional except `match`:

| Field | Meaning |
|---|---|
| `match` | which repositories: `host/owner/repo` from `origin`, where each `*` is one segment and a bare `*` is every repository. A more specific match beats a less specific one. |
| `name` | the template, e.g. `{prefix}{key}` or `{prefix}{ref}-{slug}` |
| `base` | where branches start: `head` (the current branch), `default` (`origin/HEAD`) or a branch name such as `develop` |
| `types.<kind>` | per-kind `prefix`, `base` and `from` (the issue types and labels that pick this kind) |

The kinds are `feature`, `bugfix`, `hotfix`, `release`, `support`, `docs`, `chore` and `refactor`.

```json
[
  { "match": "*", "name": "{prefix}{key}" },
  {
    "match": "bitbucket.org/acme/*",
    "base": "develop",
    "types": { "bugfix": { "prefix": "bugfix/", "from": ["Bug", "Defect"] } }
  }
]
```

**Save** checks every rule the way a member's Workspace will: an unknown kind, a base or prefix
git would not accept as a branch name, or a misspelt field is refused with the rule number and the
reason, and nothing is saved. An empty box saves no rules. Every save is in the audit log as
`tenant.branch_rules`.

### When it reaches members

Each member's Workspace asks for the rules when it starts and every five minutes after that, so a
change reaches running Workspaces within five minutes. If the control plane cannot be reached, a
Workspace keeps using the last rules it received rather than falling back to the built-in names.
In the launch dialog, the place a field came from reads `tenant: <match>`.

## Member defaults

A new member's first screen is Home, with a welcome card at the top. Its second step is
**Connect an agent**. Agent Fleet supports several agents (Claude, Codex, opencode and others),
and a new member usually cannot tell which one the team pays for. Here you name it, and the step
then says **"Your team's default is Claude (any other agent works too)"**.

It **names an agent; it does not restrict anything**. A member who connects another agent still
completes the step, and every agent stays available in the launch dialog. Which agents members
may use at all is decided by their own accounts and by the deployment, not here.

### Setting the default agent

Tenant settings → **Member defaults** (under Operations). Pick one of Claude, Codex, opencode,
Cursor, Copilot, Kiro or Muse, or **None**, and press **Save**. **None** brings back the generic
wording that lists the choices.

Agents that need no account of the member's own are not offered: the shell and SSM, and the
deployment's own engine (who may use that is set under [who may use the inference engines](02-limits.md#who-may-use-the-inference-engines-you-can-set-this)).

### When members see it

Members read the default each time the Console loads, even while their workspace is stopped.
A member who already has the page open sees the change after a reload. The welcome card shows
only until a member creates a first session or starts a first chat, so the default matters for
members who have not started yet.

Saving is recorded in the [audit log](03-audit-usage.md) as `tenant.member_defaults`, with the
chosen agent.

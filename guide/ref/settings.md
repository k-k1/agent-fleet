---
audience: "everyone, but written for whoever is looking for a knob and cannot find it"
source_of_truth: "the Console for personal and tenant settings; `deploy/compose/.env.example` for deployment variables"
updated: "2026-08"
---

# Settings — where things are configured

English | [日本語](settings.ja.md)

Three layers set behaviour, and confusing them is the usual reason a change appears to
have no effect. From narrowest to widest:

| Layer | Set by | Where | Takes effect |
|---|---|---|---|
| Personal | you | account menu → **Settings** | immediately, or from your next session |
| Tenant | a tenant administrator | account menu → **Tenant settings** | on the members of that tenant |
| Deployment | whoever operates the deployment | environment, before start | on restart |

**A narrower layer cannot widen a wider one.** A personal setting cannot exceed a
tenant limit, and a tenant setting cannot exceed what the deployment allows. When a
value you set is not the value in force, look one layer out.

## Personal settings

| Tab | Configures |
|---|---|
| Display | language, theme, density |
| Keyboard | shortcuts and rebinding |
| Agents | connecting each agent, its default model, models to exclude, per-agent behaviour |
| Agent instructions | text added to every agent you start in this workspace |
| Agent memory | version management, rollback, import / export of an agent's memory |
| Assistant | the assistant chat's agent and model |
| Agent usage | your token spend, by feature, agent and model |
| Cloud cost | your share of the deployment's cloud spend |
| Running time | when your workspace was running (a 24-hour x date heatmap) |
| Git hosting | GitHub / Bitbucket connections |
| Internal repos | repositories hosted by the deployment itself |
| AWS SSM | remote login targets |
| Issue tracker | Jira and the other work-item sources |
| Chat | Discord / Slack bridge |
| MCP servers | integration servers available to your agents |
| MCP tokens | tokens for driving your sessions from outside |
| Notifications | what you are told about, and how |
| Read aloud | speech engine and voice |
| Toolchains | JDK and other per-workspace toolchains |
| Ops & monitoring | monitoring integrations |
| Export / import | take your settings to another deployment |
| Account | sign-in methods linked to your account |
| Danger zone | destructive actions on your own workspace |

## Tenant settings

| Tab | Configures |
|---|---|
| Members | the roster; per-member resources, sessions and operations |
| Sessions | everything running in the tenant right now |
| Limits & idle | the limits in force (read-only — a deployment administrator sets them) |
| Sign-in methods | your own IdP or GitHub organisation as a way in (needs approval) |
| Login rules | join mode and domains in force (read-only) |
| Allowed networks | where members may connect from |
| Integration OAuth apps | your tenant's own OAuth apps for GitHub / Bitbucket |
| MCP distribution | integration servers handed to every member |
| Audit | who changed what, when |
| Running time | per-member workspace uptime, exportable, plus an hour-by-hour heatmap of the whole tenant |
| Cloud cost | the tenant's cloud spend |

## Deployment variables

Set before the Control Plane starts. The annotated list is
`deploy/compose/.env.example`, which stays the
source of truth for defaults; these are the ones that decide the shape of everything
else:

| Variable | Decides |
|---|---|
| `AF_RUNTIME` | the deployment target — see [deploy-targets.md](deploy-targets.md). Rejected at boot if unknown |
| `AUTH` | how people sign in: `dev` (single user), `oauth` (the Control Plane's own), `proxy` (an upstream gateway) |
| `DATA_DIR` | where all persistent state lives — the thing to back up |
| `AF_MASTER_KEY` | the root of at-rest encryption. Lose it and the stored credentials are unrecoverable |
| `SUPER_ADMIN_EMAILS` | who is a deployment administrator |
| `PUBLIC_BASE_URL` | the address people reach, and what OAuth callbacks are built from |
| `WS_MEMORY` | the default memory ceiling for a workspace |

## Status

The tab rows come from the Console's own labels, so this table cannot silently miss a
screen. What each tab means in practice is the reader's shelf —
[use/](../member/README.md) and [admin/](../admin/README.md) — and the procedures for the
variables are [operate/](../operate/README.md).

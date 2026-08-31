# Roles — who may do what

English | [日本語](roles.ja.md)

Audience: everyone; the axis [features.md](features.md) resolves "who" against
Source of truth: this table (maintained by hand); which shelves a role receives is decided in one place in the Control Plane
Updated: 2026-08

Three roles. An unknown role is treated as the least privileged one, deliberately: a
new role must be granted reach explicitly, never inherit it.

| Role | Shown as | Reach |
|---|---|---|
| member | — (the default; no badge) | their own workspace |
| tenant administrator | "Tenant admin (tenant_admin)" | everyone in one tenant |
| deployment administrator | "super_admin (whole deployment)" | every tenant, and the host |

A tenant administrator is usually also a working member, and the two hats are entered
from different places: personal work from **Settings**, the team from **Tenant
settings**. Only a deployment administrator sees the shield-icon **Admin** item.

## What each may do

| | member | tenant admin | deployment admin |
|---|:--:|:--:|:--:|
| Own sessions, repositories and files | ✓ | ✓ | ✓ |
| Own connections and settings | ✓ | ✓ | ✓ |
| See every session in the tenant | — | ✓ | ✓ |
| Force-stop another member's workspace | — | ✓ | ✓ |
| Add and remove members | — | ✓ | ✓ |
| Per-member session limits | — | ✓ | ✓ |
| Tenant-wide limits, sizing and idle auto-stop | — | —¹ | ✓ |
| Register a sign-in method for the tenant | — | ✓² | ✓ |
| Approve a sign-in method | — | — | ✓ |
| Login rules (join mode, domains) | — | —¹ | ✓ |
| Restrict where members may connect from | — | ✓ | ✓ |
| Register the tenant's integration app OAuth | — | ✓ | ✓ |
| Distribute integration servers to the team | — | ✓ | ✓ |
| Read the audit log | — | ✓ | ✓ |
| Running time and cloud cost | — | ✓ | ✓ |
| Egress control, speech engine, shared dictionary | — | — | ✓ |
| Create tenants and grant admin rights | — | — | ✓ |
| Install, upgrade, back up, restore | — | — | ✓ |
| A shell on the host | — | — | ✓ |

¹ Visible, but read-only. The panel exists so an administrator can see *why* something
was refused without having to ask.

² Registering is not enough — a deployment administrator approves it, and a later
change (adding an organisation, changing how the same account is recognised) sends the
row back for approval.

## Where a tenant administrator has to ask

The split is not a hierarchy of trust; it is a split of blast radius. Anything whose
effect crosses tenants, or that could lock people out of the deployment, sits with the
deployment administrator — creating tenants, granting rights, the login rules,
approving a sign-in method, egress. Anything scoped to one team sits with its own
administrator, including the whole offboarding sequence.

Backups, upgrades and host incident response are the deployment administrator's too,
and in most organisations that is the same person as IT or SRE.

## Which documentation each role receives

The shelves are the shipping unit: a container is handed only what its user's role may
read, which is why the shelves are cut by reader in the first place.

| Role | Shelves in the container |
|---|---|
| member | [use/](../use/README.md), [ref/](README.md) |
| tenant administrator | + [admin/](../admin/README.md) |
| deployment administrator | + [operate/](../operate/README.md), [build/](../build/README.md) |

The decision records and the frozen work journals are shipped to nobody.

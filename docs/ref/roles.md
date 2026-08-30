# Roles — who may do what

English | [日本語](roles.ja.md)

Audience: everyone; the axis [features.md](features.md) resolves "who" against
Source of truth: this table (maintained by hand); the shelf each role receives is decided in one place in the Control Plane
Updated: 2026-08

Three roles. An unknown role is treated as the least privileged one, deliberately —
a new role must be granted reach explicitly, never inherit it.

| Role | Called | Reach |
|---|---|---|
| member | | own workspace only |
| tenant administrator | | everyone in one tenant |
| deployment administrator | | the whole deployment |

## What each may do

| Area | member | tenant admin | deployment admin |
|---|:--:|:--:|:--:|
| Own sessions, repositories and files | | | |
| Own connections and settings | | | |
| See other members' sessions | | | |
| Invite and remove members | | | |
| Set limits, sizing and idle stop | | | |
| Configure sign-in and connection sources | | | |
| Register tenant integration apps | | | |
| Read the audit log | | | |
| See usage and cost | | | |
| Manage tenants | | | |
| Install, upgrade and back up | | | |
| Shell on the host | | | |

## Which documentation each role receives

The shelves are the shipping unit: a container is handed only what its user's role may
read, which is why the shelves are cut by reader in the first place.

| Role | Shelves in the container |
|---|---|
| member | [use/](../use/README.md), [ref/](README.md) |
| tenant administrator | + [admin/](../admin/README.md) |
| deployment administrator | + [operate/](../operate/README.md), [build/](../build/README.md) |

`decisions/`, `log/` and the handoff notes are shipped to nobody.

## Status

Axis fixed, cells to be filled in phase P1.

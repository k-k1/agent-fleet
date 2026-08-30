# Administering a tenant

English | [日本語](README.ja.md)

Audience: a tenant administrator — someone responsible for a team's members, limits and integrations
Source of truth: the Console's tenant settings — if a screen disagrees with this shelf, the screen is right
Updated: 2026-08

This shelf answers **"how do I run this for my team?"**: who may sign in, how much
they may consume, which integrations the tenant offers, and what the audit and cost
views tell you.

## What belongs here

- Procedures whose effect lands on **other people**: inviting and removing members,
  sign-in methods and login rules, restricting where members may connect from,
  setting limits and idle-stop, registering the tenant's own integration apps,
  distributing servers to members.
- How to read the audit, usage and cost views, and what each number does and does not
  include.

## What does not

- **Capability facts** — which role may do what, what each provider supports. Those
  live in [ref/](../ref/README.md); link to the table.
- **Anything a member does for themselves** — that is [use/](../use/README.md).
- **Standing up or upgrading the deployment** — that is
  [operate/](../operate/README.md). A tenant administrator does not have a shell on
  the host, and this shelf should never assume one.
- Implementation vocabulary: no variable names, internal identifiers, API paths or
  source paths in prose ([CONVENTIONS](../CONVENTIONS.md)).

## Update trigger

A change to a tenant-scoped setting, a limit, a role's reach, or what an
administrator-facing view reports.

## Migration in progress

Not written yet. Until it is, `../guide/admin/` is the source of truth. Written in
phase P2 of the documentation rebuild; several tenant-scoped features shipped without
ever reaching the old guide (connection-source restriction, workspace sizing, member
handoff, settings export/import, the work-item inbox) and filling those gaps is part
of that phase.

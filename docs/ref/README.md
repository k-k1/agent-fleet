# Reference — what Agent Fleet can do

English | [日本語](README.ja.md)

Audience: everyone — this is the one shelf all four readers share
Source of truth: this shelf, for capability facts (the axes are checked against the code)
Updated: 2026-08

Every other shelf links here instead of restating what the product supports. One copy
of each fact, readable from four directions.

| Table | Ask it |
|---|---|
| [features.md](features.md) | What can it do at all, and who can do it? |
| [agents.md](agents.md) | Can *this agent* do it? |
| [repos.md](repos.md) | Can it do it against *this repository provider*? |
| [deploy-targets.md](deploy-targets.md) | Is it there on *this deployment target*? |
| [roles.md](roles.md) | May *this role* do it? |
| [settings.md](settings.md) | Where is it configured, and what is the variable? |
| [limits.md](limits.md) | What is the default, and what is the ceiling? |
| [glossary.md](glossary.md) | What is this word, on screen and in the code? |

## Why a shared shelf

The same question arrives from four readers with different stakes. "Does Cursor
support plan mode?" is asked by a member choosing an agent, by an administrator
deciding what to offer, and by a developer adding the eighth kind. When each shelf
keeps its own copy of the answer, they drift, and the reader cannot tell which copy is
stale — so capability facts live here and nowhere else
([CONVENTIONS §6](../CONVENTIONS.md)).

## How these tables stay true

Where an axis exists in the code, CI compares the table against it — it does not
generate the table, so the wording stays yours:

- the agent columns must cover the `Kind*` constants in
  `workspace/agent/internal/session/session.go`;
- the deployment rows must cover the profiles `newRuntimeFactory` accepts in
  `control-plane/runtime.go`.

Axes that have no single definition in the code — repository providers, roles, limits
— are maintained by hand, and their tables say so.

**When prose and a table disagree, the table wins.** If you are about to write a
capability sentence outside this shelf, link instead.

## Status

The axes are fixed; the cells are being filled in phase P1 of the documentation
rebuild. A table that still says "to be filled" is not a claim that the capability is
absent — check the shelf it links to.

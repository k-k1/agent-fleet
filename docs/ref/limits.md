# Limits and defaults

English | [日本語](limits.ja.md)

Audience: everyone; most often a member asking "why did it stop?" and an administrator asking "what should I set?"
Source of truth: this table (maintained by hand); a limit's own screen shows the value actually in force
Updated: 2026-08

Collected in one place because a limit is only ever met at the worst moment, and the
reader then needs to know three things at once: what the ceiling is, who can raise it,
and what happens when it is reached.

| Limit | Default | Ceiling set by | What happens at the limit |
|---|---|---|---|
| Workspace CPU | | | |
| Workspace memory | | | |
| Workspace disk | | | |
| Idle auto-stop | | | |
| Concurrent sessions | | | |
| Session title length | | | |
| Transcript window | | | |
| Uploaded / pasted image size | | | |
| Scheduled runs | | | |
| Retention before cleanup | | | |
| Tenant workspace count | | | |

## Status

Axis fixed, cells to be filled in phase P1. Two things to be careful about when
filling this in: a limit that is enforced in more than one layer must state **all** of
them, since a value that differs per layer produces failures that only appear at one
specific moment; and a limit that is not measured anywhere must say so rather than
being given a plausible number.

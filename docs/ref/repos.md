# Repository providers — what each supports

English | [日本語](repos.ja.md)

Audience: everyone
Source of truth: this table (maintained by hand — there is no single provider registry in the code)
Updated: 2026-08

Where a working copy can come from, and what the Console can do with it afterwards.
The differences are not cosmetic: a provider that cannot be searched across
repositories changes how the work-item inbox behaves, and a provider without pull
requests changes what the review flow can offer.

✓ = supported, — = not supported or not applicable.

| Capability | GitHub | Bitbucket | Internal git | SVN | Local git |
|---|:--:|:--:|:--:|:--:|:--:|
| Connect an account from the Console | | | | | |
| OAuth sign-in | | | | | |
| Token / app-password sign-in | | | | | |
| Tenant-registered OAuth app | | | | | |
| Browse and pick a repository | | | | | |
| Private repositories | | | | | |
| Clone / checkout | | | | | |
| Push with transparent credentials | | | | | |
| Branches and worktrees | | | | | |
| Commit graph and diff | | | | | |
| Large files (LFS) | | | | | |
| Issues in the work-item inbox | | | | | |
| Pull requests in the work-item inbox | | | | | |
| Search across repositories | | | | | |
| Write back to the item (comment, state) | | | | | |

## Issue trackers are a separate axis

An issue tracker is not always the same product as the code host — Jira sits beside
Bitbucket or GitHub. The tracker side is listed in
[features.md](features.md#organising-the-work) and detailed on the reader's shelf; this
table stays about repositories.

## Status

Axis fixed, cells to be filled in phase P1. Two facts already known to be
counter-intuitive and worth stating loudly when the cells are filled: Bitbucket has no
cross-repository item search, and a 404 from a provider is never evidence that a thing
does not exist.

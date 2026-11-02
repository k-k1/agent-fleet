---
audience: "everyone"
source_of_truth: "this table (maintained by hand — there is no single provider registry in the code)"
---

# Repository providers — what each supports

English | [日本語](repos.ja.md)

Where a working copy can come from, and what the Console can do with it afterwards.
The differences are not cosmetic: a provider with no cross-repository search changes
how the work-item inbox has to be driven, and one without pull requests changes what
the review flow can offer at all.

✓ = supported, — = not supported or not applicable.

| Capability | GitHub | Bitbucket | Internal git | SVN | Local |
|---|:--:|:--:|:--:|:--:|:--:|
| Connect an account from the Console | ✓ | ✓ | ✓¹ | ✓² | — |
| Sign in with OAuth | ✓³ | ✓ | — | — | — |
| Sign in with a token / app password | ✓ | ✓ | ✓¹ | ✓² | — |
| Tenant-registered OAuth app | ✓ | ✓ | — | — | — |
| Browse and pick a repository | ✓ | ✓ | ✓ | — | — |
| Private repositories | ✓ | ✓ | ✓ | ✓ | ✓ |
| Clone / checkout | ✓ | ✓ | ✓ | ✓ | ✓⁴ |
| Push with credentials applied transparently | ✓ | ✓ | ✓ | ✓ | — |
| Branches and worktrees | ✓ | ✓ | ✓ | —⁵ | ✓ |
| Commit graph and diff | ✓ | ✓ | ✓ | —⁵ | ✓ |
| Large files (LFS) | ✓ | ✓ | ✓ | — | ✓ |
| Issues in the work-item inbox | ✓ | — | — | — | — |
| Pull requests in the work-item inbox | ✓ | ✓⁶ | — | — | — |
| Start a review from a pull request (its head branch checked out) | ✓ | ✓⁶ | — | — | — |
| Search across repositories | ✓ | —⁷ | — | — | — |
| Write back to the item (comment) | ✓ | ✓⁸ | — | — | — |
| Pull request chip on a session's row (state, draft, checks) | ✓ | ✓ | — | — | — |
| Notification when the branch's pull request is merged or closed | ✓ | ✓ | — | — | — |
| Notification when the branch's pull request is ready to merge | ✓ | — | — | — | — |

¹ The internal provider is hosted by the deployment itself, and credentials are
derived per membership rather than stored; there is nothing for you to connect.

² A URL plus basic authentication. Self-signed certificates can be trusted explicitly
per checkout.

³ GitHub also carries the Copilot agent connection: connecting GitHub connects it, and
disconnecting takes it away too.

⁴ "Local" means a working copy that is not backed by a remote: a new folder you
started here, or a clone you made yourself. It works, but nothing is pushed anywhere.

⁵ SVN checkouts are flat working copies. The branch, worktree and commit-graph views
are git-shaped and do not apply; an svn row has a read-only **Show log** (linear revision
list and per-revision diff) and **Local changes** (`svn status`) instead.

⁶ Bitbucket contributes **pull requests only**; its issue tracker is not read.

⁷ Bitbucket has no cross-repository search API, so a query has to name the workspace
(and usually the repository) it applies to. The Console builds those queries for you
rather than asking you to write them. A query with no workspace comes back empty,
which is easy to mistake for "there is nothing assigned to me".

⁸ Posting a comment on a Bitbucket pull request needs the pull request write permission
(`pullrequest:write` on the OAuth app), which means a tenant administrator widening the scope
and **every member re-authorising**; reading was added without it on purpose. The Console
offers the button on a Bitbucket row only when the connection is known to hold that scope, and
otherwise Agent Fleet only reads ([15](../member/15-external-services.md)). Bitbucket issues are
never listed, so only pull requests can be commented on.

## Issue trackers are a separate axis

The tracker is not always the same product as the code host: Jira sits beside
Bitbucket or GitHub, and is connected separately.

| Tracker | Contributes | Write back |
|---|---|---|
| GitHub | issues and pull requests, in one query | ✓ comment |
| Jira | issues | ✓ comment |
| Bitbucket | pull requests | ✓ comment (needs the write scope⁸) |

**A 404 from a provider is never evidence that a thing does not exist.** It is equally
consistent with "you cannot see it", and treating it as absence is how an inbox
silently under-reports.

## Not in this table

Whether a *deployment* can reach a provider at all is an egress question, not a
provider capability; see [deploy-targets.md](deploy-targets.md) and the tenant
settings.

---
audience: "a tenant administrator reviewing what happened and what it cost"
---

# 03. Audit log and usage

English | [日本語](03-audit-usage.ja.md)

What this chapter held now lives on one page per task.

| What you want to do | Page |
|---|---|
| Check who changed what, what is and is not recorded, the two rows of an operation that cannot be undone | [Read the audit log](audit.md) |
| Running time per member, the trend by month, cloud cost | [Read running time and cloud cost](usage.md) |

Questions about adding members, limits and force-stopping are answered in [01 Members](01-members.md) and [02 Resource limits and sessions](02-limits.md).

## Not moved yet

**Q. A member says their agent CLI has been the same version for weeks. Is that normal?**
Look at **Settings → Toolchain**, at the line under the tool-version table: "Upstream release
watch: last clean run …". Upstream releases are watched every 2 hours, and the versions this deployment
bakes only move when that watch reports one. When the line is recent and carries no warning, the
versions really have not moved and there is nothing to do. When it warns (that the watch could
not read a particular source, or that it has had no clean run for over 48 hours), the version
standing still says nothing about upstream, and it is your deployment administrator's monitoring
that needs looking at ([operate/](../operate/README.md)). If the line is **absent altogether**,
this deployment cannot reach GitHub and simply does not know; that is not itself a fault.

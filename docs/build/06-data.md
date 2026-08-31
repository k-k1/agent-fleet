---
audience: "someone touching the schema or a migration"
source_of_truth: "`control-plane/migrations/*.sql` (this is a reading of them, as of 0001–0028)"
updated: "2026-07"
---

# 06. The data model and migrations

English | [日本語](06-data.ja.md)

## 6.1 Store layout

- **The metadata store belongs to the CP.** One interface, two implementations:
  **SQLite** (the default, pure Go, WAL) and **Postgres** (a parallel migration
  directory, the same schema).
- **User credentials never go in the database.** They live in the encrypted store inside
  the workspace's home ([07 §7.6](07-security.md)). All the database holds is the
  wrapped DEK.
- The internal git repositories are files on disk; the database is only the ledger.

## 6.2 Entities

**People and tenants** — identity ↔ tenant is **many-to-many**.

| Table | Role and notable columns |
|---|---|
| `tenant` | A department (the default is one tenant for the whole company). A unique slug, JSON limits, and three login rules, all CSV. **There is deliberately no `allowed_emails`** — the roster of who may enter is `membership` ([decisions/0043](../decisions/0043-login-idp.md)) |
| `identity` | A person. Unique email, a unique sanitised key, and a deployment-wide role |
| `membership` | The join of identity × tenant, unique per pair, with a status. **Offboarding is a soft delete** — the workspace and home survive — and every resolution path requires an active status. **Only the invite API may revive one**, because an automatic path that revived a membership would silently undo a removal |
| `identity_provider` | (provider, subject) → identity. This is the key that **keeps the home directory still when the IdP changes someone's email**. A single row means "this identity has signed in at least once", which is what one of the tenant-IdP rules keys off |
| `tenant_idp` | A tenant-defined sign-in method: issuer, client id, a sealed secret, trust settings, allowed tenants and **mandatory** domains, and a status. **A tenant administrator writes the row; only a deployment administrator may make it active** — registering an IdP is the power to declare *who someone is*, and an identity is one per deployment, keyed by email. The provider ids are namespaced so they cannot collide with the environment-configured ones |
| `tenant_git_oauth` | A tenant's own OAuth app for a git provider, sealed in the same envelope. **The difference from `tenant_idp` is that there is no status column** — a clone-time OAuth app does not declare who anyone is, the callback is fixed by the CP, and the token only ever reaches the owner's workspace, so a tenant administrator's save takes effect immediately ([decisions/0052](../decisions/0052-tenant-git-oauth.md)). **The environment is not read for these at all** |
| `user_limit` | Per-membership limits, set by an administrator within the tenant's own allowance |

**Runtime** — a workspace is **per membership**, so the same person is completely
separated per tenant.

| Table | Role |
|---|---|
| `workspace` | The container name, network, data directory, agent port and token, state, and a JSON settings blob the CP owns (editable while stopped, applied at start) |
| `session` | **A mirror of the agent's tmux state**, not the truth |
| `wrapped_dek` | Envelope encryption: the per-workspace DEK wrapped by the per-tenant KEK, plus a key reference and version |

**Access and audit**

| Table | Role |
|---|---|
| `pat` | The personal access token for MCP. **Only the hash is stored**, and **the role is resolved live at call time, not frozen at issue** |
| `audit_log` | Actor kind, action, target, tenant (empty means deployment-wide) and the upstream status. Where it is written is [05 §5.5](05-api.md) |
| `usage_daily` | Showback: a daily bucket of **occupied workspace seconds**. With bring-your-own credentials the operator's cost is occupancy, not tokens. A sampler adds to it — an approximation is enough by design |

**Per feature**

| Table | Role |
|---|---|
| `ssm_profile` / `ssm_host` | SSM login, in two layers. **No AWS secret is ever stored**: the short-lived credentials are obtained inside the container and never reach the CP |
| `egress_daily` / `egress_allowlist` / `deployment_setting` | Egress control ([07 §7.8](07-security.md)): a daily aggregate, a versioned allowlist (active / proposed / retired), and deployment-wide key-values |
| `git_repo` / `lfs_object` / `lfs_lock` | The internal git provider's ledger ([91](91-internal-git.md)). The LFS blobs are content-addressed on disk; the tables exist for O(1) quota accounting and for locks. **Access tokens are not stored** — a per-membership HMAC is derived each time |
| `memo` / `memo_category` | The memo queue ([03](03-control-plane.md)). Attachments are JSON references; the images themselves stay in the container |
| `notification` / `notification_usage_state` | The notification centre ([03](03-control-plane.md)) |
| `schedule` / `schedule_run` | Scheduled execution ([decisions/0021](../decisions/0021-scheduled-execution.md)). **It lives in the CP's database because the CP is the only thing that can look at the clock while the workspace is stopped** |
| `mcp_server` | Tenant-distributed MCP servers: remote definitions only, and it **deliberately has no columns for a stdio command, arguments or environment** ([decisions/0031](../decisions/0031-mcp-registry.md)) |

## 6.3 The relationships that matter

```
identity ──< membership >── tenant
                │ 1:1                └─< git_repo / egress_allowlist / mcp_server
             workspace ──< session
                │ 1:1
             wrapped_dek
membership ──< pat / user_limit / ssm_profile ──< ssm_host / memo / memo_category
           / usage_daily / notification / schedule ──< schedule_run
```

- The identity's key is a sanitised email — lowercased, non-alphanumerics replaced,
  length-capped — and it appears in container names and home paths.
- `workspace.state` is synchronised by start and stop; if it diverges from reality,
  inspection wins and repairs it.
- **`session` is a mirror; the agent's tmux is the truth.** Use it for display, the
  administrator's overview and quota decisions — but every real operation goes to the
  agent.

## 6.4 Migration practice

- SQL is embedded and applied **idempotently** at start. **Put it in both directories.**
  The numbers do not line up, because the Postgres series began with a consolidated
  schema; the correspondence is stated in a comment at the top of each file.
- ⚠️ **Add it to one dialect, forget the other, and nobody notices.** This actually
  happened: a table added on the SQLite side was never mirrored, and on the Postgres
  deployment every call for that feature returned a 500. **The Console folds a
  non-array response into an empty list, so the symptom was not an error — it was
  "the categories don't show up"**, which is a shape nobody can report as a fault.
  The fix added a **schema parity test that compares the landing schema of both series
  by measurement**. It only runs with a database URL set, so **run it against a real
  Postgres once whenever you add a migration** ([how to stand one up](10-development.md)).
- ⚠️ **The migrator splits naively on `;`**, so **never write a semicolon — or a quote —
  in a SQL comment.**
- Destructive changes are done as a new table plus data migration. An unused column may
  stay, out of respect for SQLite's ALTER limitations.
- **A new member setting is a field in the JSON blob, not a new column.**
- When you add a migration, **update the table in §6.2** (the responsibility table is
  [README](README.md)).

---
audience: "someone touching the schema or a migration"
source_of_truth: "`control-plane/internal/store/migrations/*.sql` and `migrations-pg/*.sql` (this is a reading of them, as of SQLite 0076 / Postgres 0061)"
updated: "2026-09"
---

# 06. The data model and migrations

English | [日本語](06-data.ja.md)

## 6.1 Store layout

- **The metadata store belongs to the CP.** One port, `store.Store`, is the union of
  feature-scoped sub-interfaces (`TenantStore`, `WorkspaceStore`, `SessionShareStore`,
  `EngineModelStore` …). A new method goes on the sub-interface it belongs to, and a
  self-contained component may depend on the narrowest one it needs. Behind it is **one
  SQL layer shared by two dialects**. The queries are written with `?`, and Postgres
  rewrites them to `$n`.
  - **SQLite** is the default: pure Go (`modernc.org/sqlite`), WAL, foreign keys on, and
    one open connection. The file is `AF_DB` (default `<WS_DATA>/control-plane.db`).
  - **Postgres** is chosen when `AF_DATABASE_URL` is set, or when `AF_DB_HOST` is (the
    DSN is then composed from `AF_DB_*`). It is what a CP needs when it runs as more than
    one replica. The workspace operation fence (`AcquireWorkspaceOperationFence`, a
    session advisory lock) exists only here. SQLite is the single-CP profile.
- **User credentials never go in the database.** They live in the encrypted store in
  the workspace's home ([07 §7.6](07-security.md)). All the database holds is the
  wrapped DEK. **Tenant-supplied secrets are sealed** under the tenant key: a tenant
  IdP's client secret, a tenant git OAuth secret, MCP headers, the body of a share
  proposal or a handoff. They are stored as ciphertext plus `key_ref`, and fall back to
  plaintext with an empty `key_ref` only on a deployment with no master key.
- ⚠️ **One secret is stored in plaintext: `workspace.agent_token`**, the bearer the CP
  presents to that workspace's agent ([07 §7.5](07-security.md)). Anyone holding a
  copy of the database can authenticate as the CP to any agent they can reach, so treat a dump or a backup of it
  as a secret.
- **On RDS, the password is not a value the process may keep.** `AF_DB_PASSWORD`
  arrives through the task definition's `secrets`, **which ECS resolves once, at task
  start**. RDS rotates its managed master password every seven days. So when Postgres
  answers `28P01`, the CP re-reads the secret named by `AF_DB_PASSWORD_SECRET_ARN`
  (`AWSCURRENT`, then `AWSPENDING`) and retries **inside the connector**, so no caller
  sees it ([decisions/0065](../decisions/0065-db-credential-rotation.md)). When the
  env var was all there was, a rotation cost fifteen minutes of total outage on
  2026-09-01. With the ARN unset, the injected value is all there is; that is every
  non-RDS deployment.
- **`GET /readyz` is the endpoint that actually consults this store** (`Store.Ping`).
  `/healthz` reports that a process is running and nothing more; see
  [09 §9.9](09-deploy.md).

## 6.2 Entities

**People and tenants.** Identity ↔ tenant is **many-to-many**.

| Table | Role and notable columns |
|---|---|
| `tenant` | A department (the default is one tenant for the whole company). A unique `slug`, and `limits` as JSON (`max_workspaces`, `max_sessions`, `max_workspace_mem` …). Its login rules are CSV columns: `allowed_providers`, `auto_join_domains`, `allowed_domains`, and `hidden_providers`. The last one is **display only, never a gate**. `allowed_cidrs` is the source-network restriction ([decisions/0047](../decisions/0047-tenant-network-restriction.md)). It is a column, not part of `limits`, because it is read on every request behind the login-rule cache. **There is deliberately no `allowed_emails`**: the roster of who may enter is `membership` ([decisions/0043](../decisions/0043-login-idp.md)). `isolation` and `key_ref` are not acted on. The key custodian uses the tenant id as the key reference ([07 §7.6](07-security.md)) |
| `identity` | A person. `email` is unique when non-empty. `user_key` is unique: the sanitised key that names the container and the home. `role` is the deployment-wide role (`super_admin` \| `user`) |
| `membership` | The join of identity × tenant, `UNIQUE(identity_id, tenant_id)`, with `role` (`tenant_admin` \| `member`) and `status`. **Offboarding is a soft delete** (`status='inactive'`): the workspace and home survive, and every resolution path requires `status='active'`. **No sign-in or auto-provisioning path revives one** (`EnsureMembership` never reactivates), because an automatic path that did would silently undo a removal. An invite, which is an explicit decision, does. Deleting the row is a separate, later step (§6.3) |
| `identity_provider` | (provider, subject) → identity. This is the key that **keeps the home directory still when the IdP changes someone's email**, so `user_key` need not derive from the current address. A single row means "this identity has signed in at least once", which one of the tenant-IdP rules keys off. `realm` (the issuer, or `https://github.com`), together with `realm_claim` / `realm_subject` (a stable claim such as Entra's `oid`, by name and value), lets the same IdP account reached through two buttons resolve to one identity. The claim value always comes from the signed token, never from a tenant's row |
| `tenant_idp` | A tenant-defined sign-in method. `kind` is `oidc` (`issuer`, `trust`, `allowed_tids`) or `github` (`allowed_orgs`). It also has a sealed `secret_enc`, **mandatory** `allowed_domains`, `link_claim`, and `status` (`pending` \| `active` \| `suspended`). **A tenant administrator writes the row; only a deployment administrator may make it active.** Registering an IdP is the power to declare *who someone is*, and an identity is one per deployment, keyed by email. Changing what was approved on an active row sends it back to `pending`: the issuer, client id, trust, kind or `link_claim`, or widening the domains, tenant ids or orgs (`repend`). The CP sees the provider as `t:<tenant-slug>:<name>`, so it cannot collide with an environment-configured one |
| `tenant_git_oauth` | A tenant's own OAuth app for a git provider (`github` \| `bitbucket`), one per (tenant, provider), sealed in the same envelope. **Unlike `tenant_idp`, it has no status column.** A clone-time OAuth app does not declare who anyone is, the callback is fixed by the CP, and the token only ever reaches the owner's workspace. So a tenant administrator's save takes effect immediately ([decisions/0052](../decisions/0052-tenant-git-oauth.md)). A GitHub row's secret is empty on purpose, because the device flow needs none. **The environment is not read for these at all**: `GITHUB_OAUTH_CLIENT_ID` now means the sign-in app only |
| `tenant_branch_rules` | A tenant's branch naming rules, one row per tenant holding the whole list as JSON in the Agent's rule shape (`{match, name, base, types}`), checked on save with the Agent's own checks. The Agent polls it through `/internal/branch-rules` as the tenant layer ([decisions/0103](../decisions/0103-branch-naming-rules.md) decision 10). No row is no rules |
| `user_limit` | Per-membership limits, set by an administrator within the tenant's allowance: `max_sessions`, `disk_gb`, `mem_limit` (bytes), `cpu_limit` (Fargate CPU units), and `slot_class` (a deployment-declared class id, which only `ecs-ec2` acts on). 0 or empty means the tenant or deployment default |

**Workspaces and sessions.** A workspace is **per membership**, so the same person is
completely separated per tenant.

| Table | Role |
|---|---|
| `workspace` | `membership_id` (unique), `container_name`, `network`, `data_dir`, `agent_port`, `agent_token`, `state`, and `settings`. `settings` is a JSON blob the CP owns: it can be edited while the workspace is stopped and is applied to the environment at start. `data_dir` is re-rooted onto the current `WS_DATA` whenever it is used, so moving the data directory does not hand the workspace an empty home. `preview_slug` is minted at each start and cleared at stop. It is unique only when non-empty, because a preview request carries nothing but its Host ([decisions/0062](../decisions/0062-preview-subdomain.md)) |
| `session` | **A mirror of the agent's session list**, not the truth. PK (`workspace_id`, `name`), plus `kind`, `dir`, `repo`, `label`, `state` and `last_seen`. `carried` is the question, plan or permission that was still waiting when the session was folded away, and `studio` is the image studio the session is bound to. Both exist because a stopped workspace's list is built from this table alone |
| `wrapped_dek` | Envelope encryption: the per-workspace DEK wrapped by the per-tenant KEK, plus `key_ref` and `key_version` |
| `workspace_activity` | One row per workspace: `last_seen_at` and `connected_until`, **written by every CP replica**, so that an idle stop on one replica sees a connection held on another |
| `workspace_stop_intent` | The atomic claim an idle stop takes before it stops a workspace, so that new activity arriving on another replica cannot race it |

**Access and audit**

| Table | Role |
|---|---|
| `pat` | The personal access token for MCP. **Only the SHA-256 hash is stored.** It belongs to an identity and optionally to one membership. `scope` is `read` \| `write` \| `admin:dangerous`, capped at issue by the issuer's role. **The role itself is resolved live at call time, not frozen at issue** |
| `audit_log` | `actor_kind` (`user` \| `admin` \| `mcp` \| `system` \| `claude`, the last for a claude session's own edits and commands, recorded by the transcript sweeper), `actor_id`, `action`, `target`, `detail`, and `tenant_id` (empty means deployment-wide). `http_status` is the upstream status of a relayed mutation (0 = not recorded). **It has no membership column**, so offboarding cannot erase its own record. Where it is written is [05 §5.5](05-api.md) |

**Occupancy and cost.** None of these is ever rendered as money unless it came from an
invoice.

| Table | Role |
|---|---|
| `usage_daily` | Showback: a daily bucket of **occupied workspace seconds**. With bring-your-own credentials, the operator's cost is occupancy, not tokens. A sampler adds to it, and an approximation is enough by design |
| `usage_hourly` | The same occupancy per HOUR, plus session counts ([decisions/0066](../decisions/0066-uptime-heatmap.md)). ⚠️ `membership_id = ''` is not a member: it is the sampler's heartbeat for that hour. That row is what makes "observed and stopped" different from "never recorded". `measured_secs` does the same one level down: a running workspace whose agent could not be read has unknown sessions, not zero. Retained 92 days |
| `cloud_cost_daily` | The AWS invoice attributed by cost allocation tag, per (day, membership, service) ([decisions/0048](../decisions/0048-member-cloud-cost.md)). Amounts are integer **micro-units**, never floats. `estimated` marks a day Cost Explorer may still change. `membership_id = ''` is the shared bucket, and **it is not divided among people** |
| `cloud_cost_role_daily` | The same shared bucket cut a second way, by the `af-role` tag. It is the same line items, so its total equals the shared total. `role = ''` is the honest residual (NAT, ALB, RDS, tax), not a missing value |

**Sharing and handoff.** Transcript bodies never leave the owner's workspace.

| Table | Role |
|---|---|
| `session_share` | An ACL row: owner membership → recipient membership. `scope_type` is `session` \| `repo` \| `worktree`, and `permission` is `ro` \| `rw` |
| `shared_session_catalog` | The owner workspace's session list as recipients may see it. It is replaced wholesale on each sync and carries enough for the recipient's project/worktree tree: `working_copy_id`, `parent_working_copy_id`, `branch`, and the live `activity` |
| `session_share_proposal` | **Recipient → owner**: an operation a `rw` recipient proposes and the owner approves. The body is sealed. `status` is `pending` \| `processing` \| `approved` \| `rejected` \| `expired`, and the table cascades from the catalog row |
| `session_share_owner_lease` | Serialises an approved operation with share mutations for one owner, across replicas |
| `session_handoff_offer` | **Owner → recipient**: "carry this on", offered to someone the session is already shared with ([decisions/0057](../decisions/0057-member-handoff.md)). The direction is the opposite of a proposal, which is why it is a separate table. It holds only the sealed text and the git coordinates the agent read (`repo_remote`, `branch`, `head_sha`). **A partial unique index allows only one pending offer per session.** Counting before inserting would let two simultaneous offers both pass |

**Self-hosted engines** ([decisions/0071](../decisions/0071-self-hosted-inference-engines.md),
[0072](../decisions/0072-engine-model-catalog.md),
[0079](../decisions/0079-remote-engine-from-another-deployment.md)). The engine key is
the engine table's key (`llm`, `image`).

| Table | Role |
|---|---|
| `engine_models` | The model catalogue, PK (`role`, `id`), and **deployment-wide**. `files` names the objects in the engine's bucket; the bytes live there, not here. Toggles (`enabled`, `selected`, `is_default`), per-model arguments and generation defaults, display metadata, the KV-cache geometry that sizes the context window, and the licence as a snapshot at ingest (`license`, `license_name` and `commercial_use` are all kept). The licence acceptance records who, when, under which tenant, and the licence text as accepted. It is a table rather than a settings blob because it has two writers and settings have no compare-and-swap. **There is deliberately no `last_used_at`**: it would be a write on the request path for a figure nobody decides anything on |
| `engine_ingest_jobs` | A model download running as a task. **It is the only record that the job exists**, and its state is reconciled from ECS rather than believed. `spec` is the catalogue row the job will create, written at start, so a CP replaced mid-download can still create the row |
| `engine_hourly` | An engine's occupancy per hour, written by the on-demand controller's own tick. There are three states: a row means the hour was observed, and no row means unknown. `observed_secs` is stored, not derived, because the tick interval varies. `draining_secs` is stopped-but-still-billing |
| `engine_membership_hourly` / `engine_usage_undelivered` | Whose work an engine was doing, for a deployment that **lends** its engines to a borrower with no workspace. The first counts requests per membership per hour. The second keeps the usage row the gateway could not deliver to a workspace. **Neither is a second usage ledger**, and undelivered rows are never re-delivered. Retained 92 days |

**Per feature**

| Table | Role |
|---|---|
| `ssm_profile` / `ssm_host` | SSM login, in two layers: a profile is a shared SSO bundle mapped to one `~/.aws` named profile, and a host is one instance. **No AWS secret is ever stored**: the short-lived credentials are obtained inside the container and never reach the CP |
| `egress_daily` / `egress_allowlist` | Egress control ([07 §7.8](07-security.md)): a daily aggregate per (day, host, allowed), and a versioned allowlist, global or per tenant (`active` \| `proposed` \| `retired`) |
| `deployment_setting` | A deployment-wide key-value bag. It holds the egress mode, branding, per-engine settings, the sealed Hugging Face and Civitai tokens, and the claude-audit cursors. A value here has no compare-and-swap, so anything with two writers needs a table |
| `git_repo` / `lfs_object` / `lfs_lock` | The internal git provider's ledger ([91](91-internal-git.md)). The bare repositories are files under `<WS_DATA>/git/<tenant-slug>/<name>.git`, and the LFS blobs are content-addressed beside them. The tables exist for the repository list, O(1) quota accounting and locks. **Access tokens are not stored**: a per-membership HMAC is derived each time |
| `memo` / `memo_category` | The memo queue ([03 §3.6](03-control-plane.md)). Attachments are JSON references, and the images themselves stay in the container. `memo_category` holds the order of the categories and keeps empty ones. Sent memos are swept after 7 days |
| `notification` / `notification_usage_state` | The notification centre ([03](03-control-plane.md)): rows per membership, unique by `event_id` and kept 7 days, plus the window state of usage-threshold notifications |
| `schedule` / `schedule_run` | Scheduled execution ([decisions/0021](../decisions/0021-scheduled-execution.md)). The first holds the definition and its fire ledger (`next_run`, `last_run`), reuse-mode rotation, `report` and `stop_after_run`. The second is a bounded run history. **It lives in the CP's database because the CP is the only thing that can look at the clock while the workspace is stopped** |
| `mcp_server` | Tenant-distributed MCP servers: remote definitions only, and it **deliberately has no columns for a stdio command, arguments or environment** ([decisions/0031](../decisions/0031-mcp-registry.md)). Headers are sealed. `user_secret=1` distributes only the header names, and each member fills in the values |
| `work_item_query` / `work_item_cache` / `work_item_session` | The work item inbox ([decisions/0061](../decisions/0061-work-item-inbox.md)): saved queries, a cache of **non-secret** ticket metadata (never the description, comments or tokens) so the rail renders while the workspace is stopped, and the ledger of which ticket started which session. The ledger deliberately has no foreign key to the cache, because the cache is a volatile query result |

`schema_migrations` records the applied versions (§6.5).

## 6.3 The relationships that matter

```
identity ──< membership >── tenant ──< git_repo, tenant_idp, tenant_git_oauth,
   │            │                      mcp_server, egress_allowlist
   │            │ 1:1
   │         workspace ──< session
   │            │      ──< shared_session_catalog ──< session_share_proposal
   │            │                                 ──< session_handoff_offer
   │            └─ 1:1  wrapped_dek, workspace_activity, workspace_stop_intent
   ├─< identity_provider
   └─< pat (optionally pinned to one membership)

membership ──< user_limit (1:1), ssm_profile ──< ssm_host, memo, memo_category,
               notification, schedule ──< schedule_run,
               work_item_query ──< work_item_cache,
               work_item_session (no link to a query or the cache),
               session_share (as owner or as recipient)
```

- The identity's key is a sanitised email: lowercased, runs of non-alphanumerics
  replaced by `-`, and capped at 40 characters. It names the container
  (`af-ws-<slug>-<key>`; the default tenant keeps `af-ws-<key>`) and the home path.
- `workspace.state` is written by start and stop. What the API reports is the runtime's
  own answer, so a stale column is not believed.
- **`session` is a mirror; the agent is the truth.** `ReplaceSessions` swaps a
  workspace's rows for the current list. Use the mirror for display, the administrator's
  overview and quota decisions, but send every real operation to the agent.
- **Deletion names its tables explicitly.** `DeleteWorkspace`, `DeleteMembership`
  (`membershipCascade`) and `DeleteTenant` list their dependents in order rather than
  rely on `ON DELETE CASCADE`, which only a few tables declare. There is one exception.
  `DeleteWorkspace` deletes the `shared_session_catalog` rows and relies on the
  foreign-key cascade from them to remove `session_share_proposal` and
  `session_handoff_offer`, both of which declare it in both dialects.
  `membershipCascade` deletes those two explicitly. A membership is deleted
  as the last step of remove → destroy the workspace → delete the row, and it is
  irreversible. **What is deliberately kept is the history**: `audit_log`,
  `usage_daily`, `usage_hourly`, `cloud_cost_daily` and the engine attribution tables.
  Deleting them would change past totals after the fact.

## 6.4 What is not in the database

This chapter owns the CP's database only. The rest of the persistent state is owned
elsewhere:

- **The workspace's own state** is on its home: the session metadata (the trash
  included), transcripts and the usage records ([04 §4.2](04-agent.md),
  [04 §4.7](04-agent.md)). **User secrets** are in the encrypted store on the same home
  ([07 §7.6](07-security.md)).
- **Where the home physically lives** depends on the deployment target
  ([09](09-deploy.md)).
- **The internal git repositories** are files under `<WS_DATA>/git/`
  ([91](91-internal-git.md)).
- **Engine model files** are objects in the engine's bucket, named by
  `engine_models.files`.

## 6.5 Migration practice

- The SQL is embedded (`//go:embed`) and applied **idempotently** at start, each file in
  one transaction, and its version is recorded in `schema_migrations`. **Put every change
  in both directories.** The numbers do not line up: `migrations-pg/0001` is a
  consolidated schema of the early SQLite series, and the two have numbered
  independently since. **The name after the number is what pairs them** (for example
  `migrations/0071_session_studio.sql` ↔ `migrations-pg/0056_session_studio.sql`).
- ⚠️ **Add it to one dialect, forget the other, and nobody notices.** This has happened
  twice. `memo_category` was never mirrored, so on Postgres every category call returned
  500. **The Console folds a non-array response into an empty list, so the symptom was
  "the categories don't show up"**, which is not a shape anyone reports as a fault.
  `workspace.settings` was also never mirrored: reads swallowed the error, so only saving
  failed. Two tests guard it now:
  - `TestMigrationSeriesDeclareTheSameSchema` compares the SQL as written, needs no
    database, and runs in CI.
  - `TestSchemaDialectParity` compares the landed schemas, and only runs with
    `AF_TEST_DATABASE_URL` set. **Run it against a real Postgres once whenever you add a
    migration** ([how to stand one up](10-development.md)).
- ⚠️ **Two files with the same version number stop the CP from booting.** This is on
  purpose: otherwise the second would be skipped forever as already applied. Parallel
  branches collide this way routinely. **Renumbering a migration that any deployment has
  already applied is itself a breaking change**, because that deployment then meets the
  same DDL under a new number. When two branches collide, move the one that has never
  been deployed. If both have been, add a repair migration (as SQLite `0059` / Postgres
  `0044` did) instead of renaming.
- ⚠️ **The migrator splits naively on `;`**, so **never write a semicolon anywhere
  except at the end of a statement**: not in a comment, and not in a string literal.
  One inside a comment cuts the statement in half, and the CP stops booting with
  "incomplete input".
- ⚠️ **On SQLite, a new `workspace` column must also be listed in
  `repairWorkspaceColumns`.** Migration 0002's `workspace_new` swap runs after the later
  ALTERs and throws away every column they added. The repair re-adds them after the swap.
- **Keep the SQL portable.** Timestamps are RFC3339 `TEXT`, booleans are 0/1 `INTEGER`
  (no `BOOLEAN` in either dialect), and no column may be named after a keyword that
  only one dialect accepts (hence `model_precision`, not `precision`).
- Destructive changes are done as a new table plus data migration. An unused column may
  stay (`ssm_host.account_id`), out of respect for SQLite's ALTER limitations.
- **A new member setting is a field in the `workspace.settings` JSON blob, not a new
  column.**
- **A new table keyed to a membership, workspace or tenant** goes into the matching
  delete in §6.3, unless it is history.
- When you add a migration, **update §6.2** (the responsibility table is
  [README](README.md)).

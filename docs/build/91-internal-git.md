---
audience: "anyone touching the internal git provider"
source_of_truth: "the code"
updated: "2026-09"
---

# 91. The tenant's internal git provider (bare + smart HTTP)

English | [日本語](91-internal-git.ja.md)

Related: [01](01-architecture.md) · [03 §3.4](03-control-plane.md#34-wiring-at-start-keys-and-tokens) ·
[05](05-api.md) · [06](06-data.md) · [07 §7.6](07-security.md#76-secrets-and-envelope-encryption) ·
ADR [0010](../decisions/0010-internal-git-provider.md) (whether to build it) ·
[0003](../decisions/0003-ssh-to-connections.md) (git auth is a connection)

## 91.1 Purpose

Let a tenant keep repositories **entirely inside the fleet** — no external account
involved. The CP hosts bare repositories per tenant over smart HTTP, and they ride the
existing provider abstraction: connections, the repository picker, the credential
helper, clone and SCM browsing.

The three things it is for: **sharing within a team** (members of one tenant share
repositories and the branches agents push), **a private scratch space for agents**, and
**not letting code leave** — for compliance or isolation, with no external credential to
hold either.

**Not in scope**: pull requests, review and CI; and fine-grained permissions. Read and
write by membership is the whole model (§91.5). If pull requests, review or CI are ever
needed, ADR 0010 chose to host an existing forge rather than grow this one.

## 91.2 Why this shape

The decision and the options it rejected are ADR 0010. What the shape rests on:

- **It lives in the CP** because **the CP is the only shared component that knows about
  tenants.** A per-user container is closed to its own workspace and cannot share
  across.
- **Bare repositories plus git's own `git http-backend`** gets clone, fetch and **push**
  with the least code, and the existing SCM views (the commit graph and the rest) work on
  the clone unchanged.
- Clone, browsing and committing were **already host-independent**, so the addition is
  three blocks: a git server in the CP, token injection, and registering the provider.

## 91.3 The shape

```
  Console ──(X-AF-Tenant)──▶ Control Plane ───proxy /api──▶ the per-user agent
     │                          │  ▲                               │  git clone / fetch / push
     │ "Internal repos" tab,    │  │ CP-native (not via the agent) ▼
     │ repository picker        │  │            <PUBLIC_BASE_URL>/git/<slug>/<repo>.git
     └── /api/internal-git/* ───┘  │                               ▲
                                   └── smart HTTP (git-http-backend) + LFS
        bare repos: <WS_DATA>/git/<slug>/<repo>.git     Basic auth; the password is a
                                                        per-membership token the
                                                        credential helper supplies
```

- **Listing, creating and browsing are CP-native** (`/api/internal-git/*`, registered in
  `routes.go` `registerInternalGitRoutes`). The CP owns the repositories, so **this is a
  deliberate exception** to "every provider goes through the agent".
- **Clone and push come from git inside the workspace**, to
  `<PUBLIC_BASE_URL>/git/<slug>/<repo>.git` — the deployment's public address, the one the
  Console is served on, not a shared container network. `/git/` is exempt from the
  session gate (`exemptPrefix("/git/")`) because it authenticates itself (§91.5); the LFS
  routes under `/git/{slug}/{repo}/info/lfs/` are registered ahead of the smart-HTTP
  catch-all.
- **Without `PUBLIC_BASE_URL` the provider is only partly off**: creating a repository
  answers 503 `not_configured`, an LFS batch answers 503, and no token is injected into
  workspaces. The smart-HTTP routes and the LFS transfer and lock routes stay registered
  and still accept a valid token, listing still answers, and a credential the agent
  seeded earlier stays in its store ([#1212](https://github.com/k-k1/agent-fleet/issues/1212)).

## 91.4 Storage

- Bare repositories live at `<WS_DATA>/git/<tenant-slug>/<repo>.git`, a tree separate
  from the workspaces. Every tenant, the default one included, gets a `<slug>` directory
  here — unlike workspace homes, where the default tenant's are flat. On disk the CP uses
  the token tenant's canonical slug, never the URL's spelling of it.
- **The database is the truth for what is listed and served, not a directory scan.** The
  `git_repo` table (one row per tenant and name, with the default branch and the creating
  membership) gates the smart-HTTP handler as well as the list. The GC job is the
  exception: it walks the directory tree (§91.9).
- **LFS objects are content-addressed inside the repository's own directory**
  (`<repo>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>`), so a rename moves them and a
  delete removes them with the repository. The `lfs_object` table (tenant, repository,
  oid, size) makes the tenant's total a single sum rather than a walk; `lfs_lock` holds the
  LFS locks. Rename and delete change the `git_repo` row and both LFS tables in one
  transaction (`RenameGitRepo` / `DeleteGitRepo`). Delete commits it before removing the
  directory, so a store error fails the request with nothing changed; rename moves the
  directory first and moves it back if the transaction fails. An upload writes its ledger
  row before it publishes the object and fails the request if it cannot, because a
  published object is never uploaded again.
- The tables themselves are described in [06](06-data.md). There is **deliberately no
  token table**.

## 91.5 Authentication and the token model

Two surfaces.

**The management and browsing API** uses the ordinary session identity and tenant
resolution (`X-AF-Tenant`, through `withMembership`), scoped to the resolved tenant. No
extra credential. It checks for an active membership and **no role**: any member can
create, rename or delete any of the tenant's repositories, not only push to them ([#1200](https://github.com/k-k1/agent-fleet/issues/1200)).

**The git surface** uses a **deterministic HMAC token per membership, with no token
table at all**: `afg_<base64url(membership id)>.<tag>`, where the tag is a truncated
HMAC-SHA256 of the membership id (`mintGitToken`, `verifyGitToken`). The signing key
(`gitSignKey`) derives from the deployment's token-signing master — `AF_MASTER_KEY`, or a
random key kept under `WS_DATA` when there is none ([03 §3.4](03-control-plane.md#34-wiring-at-start-keys-and-tokens)).
So **the CP can regenerate the token**: injection is idempotent, the CP stores no token,
and there is no recovery problem. (Reusing the personal-access-token table was rejected:
it cannot be reconstructed, which makes injection non-idempotent, and it would pollute
the user's own token list.)

At every workspace start `workspaceExtraEnv` injects `AF_INTERNAL_GIT_HOST` (the host
name of `PUBLIC_BASE_URL`) and `AF_INTERNAL_GIT_TOKEN`. The agent's `seedInternalGit`
writes them into its credential store as an ordinary git credential
(`x-access-token` and the token) at startup, and **the unified credential helper
(`runCredHelper`) serves any host found in the store**, so clone and push authenticate
with no further work. The key it is stored under is the host name alone, without a
port, while git asks for `host:port` when the URL carries an explicit one — so the
lookup only matches a `PUBLIC_BASE_URL` on its scheme's default port ([#1198](https://github.com/k-k1/agent-fleet/issues/1198)).

The smart-HTTP and LFS handlers share `authorizeGitRepo`, which verifies the token,
resolves the membership **live** (`GetMembershipByID`, active memberships only), and
enforces on **every request**:

- the slug in the URL equals the token's tenant — **you cannot reach another tenant's
  repository** (403);
- the repository name is valid and present in `git_repo` (otherwise 404);
- read requires an active membership, and **push is decided by role** (`canPush`). The
  roles that may push are `member` and `tenant_admin`. The column is free text, but the
  code paths that create a membership or change its role (joining, inviting, the role
  API) write only those two, so in practice the check refuses nothing yet; it exists so
  that a read-only role would be refused.

**Revocation is live**: deactivating a membership makes the same deterministic token
stop working immediately, without a token table to update. **There is no rotation of a
single membership's token**: it changes only when the signing master does, and then
every token changes ([#1199](https://github.com/k-k1/agent-fleet/issues/1199)).

## 91.6 Integration points

Point at files and symbols, not line numbers.

| Where | What it holds |
|---|---|
| `control-plane/routes.go` `registerInternalGitRoutes` | The smart-HTTP catch-all `/git/{slug}/{repo...}`, the LFS routes, the management API `/api/internal-git/*`, and `exemptPrefix("/git/")` |
| `control-plane/git_http.go` | `gitServerAPI`; token minting and verification; `authorizeGitRepo`; `canPush`; the `git http-backend` CGI wrapper |
| `control-plane/internal_git.go` | List, create, delete, rename, branches, `cloneURL`, the repository quota, audit entries |
| `control-plane/internal_git_browse.go` | Tree, blob and commit browsing without a clone |
| `control-plane/git_lfs.go`, `git_lfs_locks.go` | The LFS batch API and basic transfer; the lock API |
| `control-plane/git_gc.go` | The GC job and orphan LFS collection |
| `control-plane/internal/store/migrations/` `0014_git_repo.sql`, `0015_lfs_object.sql`, `0016_lfs_lock.sql` | The SQLite tables. Postgres has them in `migrations-pg/0001_init.sql` |
| `control-plane/main.go`, `workspace_lifecycle.go` `workspaceExtraEnv` | `PUBLIC_BASE_URL` → `internalGitHost`; the per-start injection of `AF_INTERNAL_GIT_HOST` / `AF_INTERNAL_GIT_TOKEN` |
| `workspace/agent/cred_helper.go` | `seedInternalGit`, `internalGitHost`, `runCredHelper` |
| `workspace/agent/connections.go` `internalGitStatus` | The `internal` entry in the connections status |
| `workspace/agent/internal/gitx/git.go` `gitProviderHost` | Badges a remote on the injected host as `internal` |
| `console/src/features/repos/RepoPicker.tsx` | The `internal` provider tab; its repository and branch lists come from `/api/internal-git/*` |
| `console/src/features/settings/workspace/InternalReposTab.tsx`, `InternalRepoBrowser.tsx` | The settings "Internal repos" tab (list, create, rename, delete; works while the workspace is stopped) and the browser behind its "Browse" button |

`git` comes with the CP: the CP image installs it (`control-plane/Dockerfile`), and the
native package bundles a static `git` and `git-http-backend` and points
`GIT_HTTP_BACKEND` at it (`deploy/native/af`). Without the variable the CP uses
`/usr/lib/git-core/git-http-backend`.

**What is deliberately *not* touched**: the agent's remote-listing switch
(`internal/gitx/git_remote.go` has no internal case: internal listing goes straight to
the CP, because going through the agent would need agent → CP authentication), and the
known-hosts map `gitHosts` in `connections.go` (the internal host is only known at run
time, and the helper serves any host in the store).

`control-plane/git_e2e_test.go` and `git_lfs_e2e_test.go` drive a real `git` and
`git-lfs` against the handlers, and skip where those binaries are absent.

## 91.7 Isolation and security

- **Cross-tenant is blocked on every request** — info/refs, upload-pack, receive-pack and
  every LFS operation alike.
- **Path containment**: repository names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, and
  anything containing `..` is refused. `git-http-backend` runs with
  `GIT_PROJECT_ROOT` set to the tenant's own directory, built from the canonical slug, so
  a crafted path cannot leave it.
- **The token at rest**: the agent keeps it in its credential store, which is encrypted
  when the deployment sets `AF_MASTER_KEY`; without one (development) the store is plain
  JSON ([07 §7.6](07-security.md#76-secrets-and-envelope-encryption)). The CP stores no
  token.
- **A git execution surface in the CP is new attack surface** — input validation on refs
  and paths is deliberately strict.
- **LFS reuses exactly the same authorisation** (`authorizeGitRepo`) for every operation.
  Object ids must be 64 lowercase hex characters (a SHA-256), which doubles as path
  containment. Uploads are **hashed while streamed and refused on mismatch** (422);
  the byte quota is enforced at batch time and during the upload (507); objects are
  streamed rather than buffered, out of respect for a shared host.

## 91.8 Data flow

- **Create**: `POST /api/internal-git/repos {name}` writes the bare repository with
  `git init --bare --initial-branch=<default branch>` (`main` unless given), then the
  `git_repo` row (a failed insert removes the directory again), and returns the clone URL.
- **List**: the repository picker's internal tab and the settings tab call
  `GET /api/internal-git/repos` on the CP, not the agent.
- **Branches**: `GET /api/internal-git/repos/{name}/branches` reads the bare repository
  with `git for-each-ref`.
- **Clone**: the existing clone flow, given the clone URL; the credential helper supplies
  the token.
- **Share**: members push branches to the same URL and fetch each other's.
- **After the clone**, everything else — graph, status, checkout, the file APIs — is
  provider-independent and already worked.

## 91.9 What is implemented

- **The git surface**: clone, fetch and push over smart HTTP; token injection; create,
  list, delete and the provider tabs.
- **Rename** (`POST /api/internal-git/repos/{name}/rename {new_name}`) moves the bare
  repository and updates the ledger, rolling the move back if the ledger update fails.
  **Existing clones keep their old remote URL and must update it.**
- **A per-tenant repository cap** (`max_git_repos` in the tenant limits, 0 = unlimited),
  enforced at creation with 409 `quota_exceeded`.
- **Audit entries** `internal_git.repo.create`, `internal_git.repo.delete` and
  `internal_git.repo.rename`.
- **An empty repository is selectable and clonable**: with no branches yet, the branches
  endpoint returns an empty list plus the repository's `default_branch`, and the
  repository picker offers that name as a placeholder branch.
- **A GC job** runs `git gc --auto` over every bare repository **sequentially**, out of
  respect for memory; its interval and grace period are listed in
  [03 §3.7](03-control-plane.md#37-background-jobs).
- **LFS**: the batch API and basic transfer; **digest verification on upload**, with
  atomic publication (temporary file, fsync, rename) and de-duplication (a re-upload of a
  stored object is a no-op); a byte quota (`max_lfs_bytes`) enforced **both at batch
  time, projected across the whole batch, and during the upload**; and the **lock
  API** — create, list, verify, unlock. A path holds at most one lock per repository (a
  second attempt is a 409 carrying the existing lock); verify splits locks into yours and
  theirs so a push can detect someone else's; creating and releasing a lock need push
  rights; and only a tenant administrator may force-unlock another person's.
  - **Orphan collection** folds into the same GC job, for repositories that have LFS
    objects. Enumerating referenced ids is **pure git** — the CP needs no LFS client: it
    reads every small blob in the object store, reachable or not, and extracts the pointer
    ids. Two safety properties are worth keeping: **a grace period keeps recently written
    objects**, so an upload whose ref has not been pushed yet is not deleted; and **if
    listing the objects or starting to read them fails, or the tenant cannot be resolved,
    nothing is deleted.** A failure while reading the pointer contents is *not* detected:
    the ids read so far are taken as the whole set, and objects whose pointers were not
    read may be deleted once they are past the grace period
    ([#1210](https://github.com/k-k1/agent-fleet/issues/1210)). An object to be deleted
    leaves the ledger first, which frees quota; if that fails the object is kept
    for the next sweep.
- **The client side needs no change**: the workspace image ships `git-lfs` with its
  filters in the system gitconfig, and LFS authenticates through the same credential
  helper. The packaged native runtime runs the agent on an extracted workspace-image
  rootfs (`AF_NATIVE_ROOTFS`), so it has the same `git-lfs`; the development mode that
  runs a host-built agent (`AF_NATIVE_AGENT_BIN`) uses whatever the host has.
- **Browsing without cloning**: a read-only tree, blob and commit API
  (`GET /api/internal-git/repos/{name}/tree|blob|commits`) reading the bare repository
  directly. Blobs above 1 MiB, binaries and LFS pointers are flagged rather than
  returned; an unborn branch lists as empty. **A ref must not start with a dash or
  contain `..`. A path has its leading and trailing slashes stripped, so it is always
  relative to the repository root, and must not contain a `..` segment or a control
  character** — guarding against both traversal and a value being mistaken for an
  argument.

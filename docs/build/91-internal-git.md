# 91. The tenant's internal git provider (bare + smart HTTP)

English | [日本語](91-internal-git.ja.md)

Audience: anyone touching the internal git provider
Source of truth: the code
Updated: 2026-07

Related: [01](01-architecture.md) · [05](05-api.md) · [07 §7.6](07-security.md) ·
ADR [0010](../decisions/0010-internal-git-provider.md) (whether to build it) ·
[0003](../decisions/0003-ssh-to-connections.md) (git auth is a connection)

## 91.1 Purpose

Let a tenant keep repositories **entirely inside the fleet** — no external account
involved. The CP hosts a bare repository per tenant over smart HTTP, and it rides the
existing provider abstraction unchanged: connections, the repository picker, the
credential helper, clone and SCM browsing.

The three things it is for: **sharing within a team**, **a private scratch space for
agents**, and **not letting code leave** — for compliance or isolation, with no external
credential to hold either.

**Not in scope**: pull requests, review and CI; and fine-grained team permissions.
Read/write by membership role is the whole model. (LFS *is* implemented — §91.9.)

## 91.2 Why this shape

- **It lives in the CP** because **the CP is the only shared component that knows about
  tenants.** A per-user container is closed to its own workspace and cannot share
  across.
- **Bare repositories plus git's own HTTP backend** gets clone, fetch and **push** with
  the least code, and browsing works through the existing SCM views once cloned.
- A managed service was rejected: it stopped accepting new customers, and its IAM
  authentication does not fit a credential helper that injects a token.
- Clone, browsing and committing were **already host-independent**, so the addition was
  only three blocks: a git server in the CP, token injection, and registering the
  provider.

## 91.3 The shape

```
  Console ──────▶ Control Plane ───proxy /api──▶ the per-user agent
     │               │  ▲                             │  git clone / fetch / push
     │ provider tab  │  │ CP-native (not via agent)   ▼
     └── the management API                https://<base>/git/<slug>/<repo>.git
                        │                             ▲
                        └── smart HTTP ────────────────┘  Basic auth; the password is
        bare repos: <data>/git/<slug>/<repo>.git          a per-membership token the
                                                          credential helper injects
```

- **Listing and creating are CP-native** — the CP owns the repositories, so **this is a
  deliberate exception** to "every provider goes through the agent".
- **Clone and push come from git inside the container**, over the deployment's own base
  URL rather than a shared container network.

## 91.4 Storage

- Bare repositories under the data directory, in a tree separate from the workspaces.
- **The database is the truth, not a directory scan** — a table lists them, which avoids
  filesystem races and gives O(1) accounting.
- **LFS objects are content-addressed inside the repository's own directory**, so
  deleting or renaming the repository moves them with it. A ledger table makes the
  tenant's total a single sum rather than a walk.

## 91.5 Authentication and the token model

Two surfaces.

**The management API** reuses the ordinary identity and tenant resolution, scoped to the
resolved tenant. No extra credential.

**The git surface** uses a **deterministic HMAC token per membership, with no token
table at all**. The signing key derives from the deployment master key, so **the CP can
regenerate the token** — injection is idempotent, nothing is stored in plaintext, and
there is no recovery problem. (Reusing the personal-access-token table was rejected: it
cannot be reconstructed, which makes injection non-idempotent, and it would pollute the
user's own token list.)

At workspace start the CP injects the host and token, and the agent seeds them into the
encrypted store as an ordinary git credential. **The unified credential helper already
serves any host**, so clone and push authenticate transparently with no further work.

The smart-HTTP handler verifies the token, resolves the membership **live**, and
enforces on **every request**:

- the slug in the URL equals the token's tenant — **you cannot reach another tenant's
  repository**;
- the repository exists in the ledger (otherwise 404);
- read requires an active membership, and **push is decided by role**.

**Revocation is live**: deactivating a membership makes the same deterministic token
stop working immediately, without a token table to update.

## 91.6 Integration points

The additions are confined: a smart-HTTP handler with token minting and verification, a
management API, one migration for the ledger (**deliberately no token table**), the
environment injection at workspace start, the agent's seeding of the credential, and the
Console's provider tab. The CP image gains a `git` binary.

**What was deliberately *not* touched**: the agent's remote-listing switch (internal
listing goes straight to the CP, because going through the agent would need agent → CP
authentication), and the known-hosts list (the internal host is dynamic and the helper
serves any host in the store).

## 91.7 Isolation and security

- **Cross-tenant is blocked on every request** — info/refs, upload-pack and
  receive-pack alike.
- **Path containment**: names are validated by pattern, `..` is refused, and everything
  is confined under the tenant's own directory.
- **Secrets are not leaked**: the token goes into the encrypted store and is never
  written in plaintext.
- **A git execution surface in the CP is new attack surface** — input validation on refs
  and paths is deliberately strict.
- **LFS reuses exactly the same authorisation** for every operation. Object ids must be
  hex digests, which doubles as path containment; uploads are **verified against the
  digest** and refused on mismatch; quota is enforced on both the batch and the upload;
  and large objects are streamed rather than buffered, out of respect for a shared host.

## 91.8 Data flow

Create a repository from the Console and the CP writes the ledger row and initialises
the bare repository, returning a clone URL. Cloning is the existing flow with that URL;
the helper supplies the token. Push and fetch share branches between members. **After
the clone, everything else — graph, status, checkout, the file APIs — is
provider-independent and already worked.**

## 91.9 What is implemented

- **P1**: the git surface, token injection, create/list/delete, and the provider tab.
- **P2**: rename (moving the bare repository and updating the ledger — **existing clones
  must update their remote**); a per-tenant repository quota enforced at creation; a
  garbage-collection job that repacks **sequentially**, out of respect for memory; audit
  entries for create, delete and rename; and making an empty repository selectable and
  clonable despite having no branches yet.
- **P3, LFS**: the batch API and basic transfer; **digest verification on upload**, with
  atomic publication and de-duplication; a byte quota enforced **both at batch time and
  at upload**; and the **lock API** — create, list, verify, unlock — where a path is
  unique per repository, verify splits locks into yours and theirs so a push can detect
  someone else's, and only an administrator may force-unlock another person's.
  - **Orphan collection** folds into the same GC job. Enumerating referenced ids is
    **pure git** — the CP needs no LFS client — and there are two safety properties worth
    keeping: **a grace period keeps recently written objects**, so an upload that has not
    yet had its ref pushed is not deleted; and **if enumeration fails, nothing is
    deleted at all.**
- **Browsing without cloning**: a read-only tree, blob and commit API reading the bare
  repository directly. Blobs above a size cap, binaries and LFS pointers are flagged
  rather than returned. Refs and paths are pattern-validated — **rejecting `..`, a
  leading dash, absolute paths and control characters**, which guards against both
  traversal and a path being mistaken for an argument.

**If pull requests, review or CI are ever needed, the plan is to host an existing forge
rather than grow this one.**

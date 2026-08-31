# 0010. An internal git provider per tenant — bare repos + git-http-backend, self-hosted on the CP

English | [日本語](0010-internal-git-provider.ja.md)

- Status: **adopted, P1 implemented**. The design is [reference/internal-git-provider](../build/91-internal-git.md).
- See also: [0001](0001-self-host-vs-saas.md) (SaaS abandoned, self-hosting) / [0003](0003-ssh-to-connections.md) (git auth = Connections) /
  [0005](0005-envelope-custodian.md) (envelope encryption) / [architecture](../build/01-architecture.md)

## Context

There is a requirement to hold repositories **entirely inside the fleet** within a tenant
(A. sharing within a team / B. a private scratch or seed for agents / C. keeping code from
leaving). Today there are only Connections to external providers (GitHub/Bitbucket), which
presuppose an external account and put the code outside.

## Decision

**The Control Plane self-hosts a bare repository per tenant over smart HTTP
(`git http-backend`), riding the existing provider abstraction.** No PRs, no review, no CI (two
levels, read and write, for now).

- **It lives on the CP**: the only shared surface that knows about tenants. A per-user container
  cannot share across users.
- **bare + git-http-backend**: the least code that makes clone/fetch/**push** work. For browsing,
  the existing SCM view (the commit graph) is reused as is after cloning.
- **Reuse the token-injecting credential helper for authentication**: a tenant-scoped token per
  membership is injected into the encrypted store at `s.Git[internal-host]`, and the unified
  credential helper already serves arbitrary hosts, so it is transparent. On the CP side, smart
  HTTP validates the Basic password as a token and enforces `<slug>` == tenant on every request.
- **The token is a deterministic HMAC per membership (no token table)**: the signing key derives
  from the deployment master key, and the token embeds the `membershipID` with an HMAC tag. The
  CP can regenerate it with the same function, so injection is idempotent and no plaintext needs
  storing. On validation the embedded membership is **looked up live** to resolve (tenant, role),
  so a disabled membership is revoked immediately (see "the PAT table" under rejected options).
- Storage is `${DATA_DIR}/git/<slug>/<repo>.git` (the existing persistent volume + bind).

### Options rejected

- **AWS CodeCommit**: closed to new customers in 2024, so it cannot be a new foundation.
  Additionally, IAM authentication does not mesh with the token-injecting unified credential
  helper, and per-tenant IAM is heavy. It also does not sit well on the provider abstraction.
- **Embedding Gitea/Forgejo from the start (option ②)**: gives orgs, permissions and web
  operations, but adds another application to operate. A–C do not need PRs or a permission
  matrix, so it is overkill. We take the staged strategy of swapping it in when PRs/CI are
  actually needed.
- **Leaning on external SaaS (GitHub/GitLab)**: contradicts C (keeping code in).
- **Storing the token in the PAT table** (the original first choice): rejected. A PAT is stored
  as a hash only and **the plaintext cannot be recovered**, so the CP cannot hold the plaintext
  it injects into the workspace, making injection non-idempotent (re-issue and re-inject every
  time). Machine tokens would also pollute the user's PAT list in the Console. The token is
  managed automatically and the membership state is the real gate, so a PAT's advantages
  (individual revocation, expiry) are not needed → **a deterministic HMAC (no table)** is the
  straightforward answer.
- **Enumerating internal repos through the Agent** (an internal case in `git_remote.go`'s
  switch): needs Agent→CP authentication and is complicated. The CP owns the repositories, so
  **listing and creating them natively on the CP** is the straightforward branch.

## Consequences

- The addition is three blocks (the git server on the CP, the admin API, token injection) plus a
  small provider registration (`gitHosts`, `RepoPicker`, `GitTab`, `handleConnectionsGet`).
  Clone, browsing and commits work **unmodified**.
- **The CP gains a git execution surface** (a new attack surface). Refspec and path validation
  are tightened, and slug containment plus a tenant match are mandatory.
- The CP image gains a dependency on `git` (http-backend) (`control-plane/Dockerfile`). No table
  is created for tokens (deterministic HMAC); the only new migration is `git_repo`. The clone URL
  derives from `PUBLIC_BASE_URL`.
- If PRs/CI become necessary later, option ② can be swapped in (this decision deliberately stops
  at the minimal foundation).

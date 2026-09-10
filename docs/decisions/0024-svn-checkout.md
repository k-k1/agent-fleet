# 0024. SVN checkout — a flat working copy with no provider, using a URL and basic authentication

English | [日本語](0024-svn-checkout.ja.md)

- Status: **adopted and implemented**. The design is [docs/41](../log/41-svn-checkout.md).
- See also: [0003](0003-ssh-to-connections.md) (git auth = Connections) / [0005](0005-envelope-custodian.md) (envelope encryption) /
  [0010](0010-internal-git-provider.md) (the provider abstraction)

## Context

A request to work with **Subversion repositories** as well as git. There is no provider for SVN
the way there is for git (GitHub/Bitbucket), and an in-house SVN server needs no more than **a URL
plus basic authentication**. The main uses are checking out a particular path (trunk,
branches/x, …) and checking out several different paths.

## Decision

**Treat an SVN working copy as a flat working copy at `~/repos/<name>`, exactly like git.** It is
not put on the provider abstraction.

- **The flat model**: the folder name is the id. Where git has `.git`, `.svn` identifies the kind.
  `Repo.Vcs="svn"` tells the Console which git-only operations to hide. There is no
  branch/ahead/behind/worktree.
- **The URL expresses the subtree**: "check out a particular path" is part of the URL, and
  "several different paths" is several folders. This has the same character as the isolation git
  gets from separate clones, and it **substitutes for the absent worktree**.
- **No worktrees**: SVN has no equivalent, so a session **starts directly** inside the checkout
  folder. `ensureWorktree` refuses non-git, and create_session raises an explicit error if a
  worktree is specified for an svn directory.
- **Authentication injects from the store (git's credential helper cannot be reused)**: SVN does
  not speak git's credential-helper protocol. The REST checkout/update pulls credentials from the
  encrypted store `secrets.SVN` (longest URL prefix match) and passes them with
  `svn --username … --password-from-stdin --non-interactive --no-auth-cache`. Passing on stdin
  keeps the password out of the process list, and `--no-auth-cache` leaves no plaintext in
  `~/.subversion/auth`. Saving them is an optional opt-in at checkout time.
- **Locks are self-healed by us**: if a checkout/update fails on a working-copy lock (`E155004`),
  run `svn cleanup` and retry once. An explicit cleanup operation is also provided (local, no
  authentication needed).

### Options rejected

- **A dedicated SVN provider / settings tab**: overkill for a URL plus basic authentication. The
  lightweight design of optionally saving credentials at checkout time was adopted.
- **Caching credentials in `~/.subversion/auth` so the agent's own svn authenticates
  transparently**: that means plaintext storage, which violates the "no plaintext secrets"
  policy. → Inject only on the REST path, and accept that **direct svn inside a session is not
  transparent** (see the limit below).
- **Reusing git's credential helper**: impossible, since SVN does not speak git's protocol.

## Consequences

- The addition is `svn.go` on the Agent side (checkout/update/cleanup/info/creds), the listing and
  deletion branches in `git.go`, the routes (the agent, the CP allowlist, `auditActionTarget`) and
  the Console (the Repo type, a Git/SVN switch in the checkout modal, update/cleanup on svn rows,
  suppressing worktrees). The git paths for clone, browsing and starting a session are unmodified.
- **A deliberate limit**: the `svn` an agent runs itself inside a session (update/commit) does not
  authenticate transparently. The REST path (the Console's update button) injects credentials, but
  interactive svn has to be supplied credentials each time.
- **Self-signed and untrusted certificates are trusted by an opt-in per server**: non-interactive
  svn fails by default, so a "trust self-signed certificates" opt-in adds
  `--trust-server-cert-failures=unknown-ca,cn-mismatch,expired,not-yet-valid,other` (the full-set
  version of the old `--trust-server-cert`). Certificate trust is a property of the server rather
  than a secret, so it is handled independently of saving credentials: it is always persisted at
  checkout time and carries on into later updates (a public self-signed server with no
  authentication becomes a trust-only entry with an empty username). The trade-off is that
  certificate verification for that server is disabled, so it stays an explicit, per-server opt-in.
- **An environmental limit**: the native (WSL) runtime needs `svn` on the host (absent, it is an
  explicit `svn_missing` error).

## Amendment (2026-09-10) — credentials can be entered afterwards, and direct `svn` does authenticate

Two reports from use, both descending from the same decision above.

**Optional saving was a one-way door.** Declining it at checkout time left the password nowhere:
not in the store, and (by `--no-auth-cache`) not under `~/.subversion` either. Every later update
then failed with no surface to supply it. So a credential can now be entered against a working
copy that already exists (`POST /repos/{name}/svn-auth`, offered as "re-authenticate" on the
row and raised automatically when an update comes back `svn_auth_required`) or from the
connections screen (`PUT /connections/svn`). It is **proven against the server before it is
stored** — "saved" must not be able to mean "saved a typo", which is the failure that sends
people here.

**The rejected option was rejected for the right reason, and the wrong conclusion was drawn from
it.** Caching in `~/.subversion/auth` is indeed plaintext at rest and stays rejected. But
transparent authentication does not require it: `/usr/local/bin/svn` is now a shim that hands the
call to `workspace-agent svn-run`, which resolves the credential from the ENCRYPTED store and
passes it to the real svn on stdin — the same shape `gh` already uses (`gh-auth-wrapper.sh`), with
nothing on disk and nothing in `ps`. So "the `svn` an agent runs itself is not transparently
authenticated" is **no longer a limit of this design**.

The wrapper only ever injects or passes through, never changes what a command means; it never
takes stdin from a command that reads it (`-F -`, `--targets -`, a `commit` with no `-m`, which
would open an editor); and an explicit credential on the command line always wins. Design and the
traps found in the process: [docs/41 §9](../log/41-svn-checkout.md).

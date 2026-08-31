# 0003. git authentication — from SSH keys to Connections (HTTPS tokens/OAuth)

English | [日本語](0003-ssh-to-connections.ja.md)

- Status: decided (Phase 2). Replaces the old `docs/08-bitbucket.md` (the SSH-key model; deleted as obsolete)
- See also: [HANDOFF §6.6](../HANDOFF.md) / [build/05 API contract](../build/05-api.md) (formerly api-agent §7.0, the API surface map) / [build/07 §7.6 Secrets and envelope encryption](../build/07-security.md#76-secrets-and-envelope-encryption) (formerly security §4.4)

## Context

The original design (old doc 08) was **one SSH key per user, registered by hand in Bitbucket**,
with git using SSH URLs (`git@bitbucket.org:...`). But the Google oauth2-proxy in front gates
every request, so redirect-style OAuth callbacks hit a wall; and registering SSH keys by hand
is heavy UX. We wanted integrated authentication that completes in the web UI.

## Decision

**Drop the SSH-key model and demote it in favour of Connections (HTTPS tokens/OAuth).** The CP
user authenticates per provider in the web UI; the resulting credentials are **stored encrypted
in the container's home and used by git/claude inside the container**. No CLI authentication in
a terminal is needed. The CP neither holds nor interprets the secret (it delegates to the Agent
via `proxyAgentREST`).

- **GitHub** = Device Flow (approve at `github.com/login/device`) or a PAT
  (`x-access-token`). Scope `repo`.
- **Bitbucket** = Auth Code Grant (the callback carries the browser's Google cookie and so
  passes straight through oauth2-proxy — **no change needed in front**), or email + API token.
  Expired tokens are refreshed automatically by the git credential helper
  (`workspace-agent bitbucket-cred`), which reads `bitbucket.json`.
- Storage is the encrypted store `secrets.enc` (AES-256-GCM, 0600). The unified credential
  helper `workspace-agent cred` decrypts on each call and **never creates a plaintext file**.
  The key itself is protected by envelope encryption ([0005](0005-envelope-custodian.md)).
- SSH keys are demoted to an optional add-on (unused by default).

## Consequences

- The old `/sshkey` and `/sshkey/rotate` endpoints, the `SshKey` table and known_hosts
  distribution are all gone.
- clone/fetch/**push** authenticate transparently; private repositories work through the same
  unified credential helper. A submodule's SSH URL is rewritten to HTTPS on a best-effort basis
  after cloning ([HANDOFF §6.10.5](../HANDOFF.md)).
- The CP does not hold Bitbucket/GitHub tokens (a smaller exposure surface). Responsibility
  stays scoped to each user.

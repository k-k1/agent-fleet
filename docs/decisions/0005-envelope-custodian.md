# 0005. Keys at rest — envelope encryption + a custodian abstraction (with the on-prem limit stated)

English | [日本語](0005-envelope-custodian.ja.md)

- Status: decided (P3-3)
- Follow-ups: #1645, #1646
- See also: [history/p3-3-envelope-crypto](../log/p3-3-envelope-crypto.md) / [build/07 §7.6 Secrets and envelope encryption](../build/07-security.md#76-secrets-and-envelope-encryption) (formerly security §4.4) / [Roadmap §12.3](../log/roadmap.md#123-tos-と分離の留意自社ホスト前提)

## Context

The Phase 2 A3 key scheme was a single `AF_MASTER_KEY` (env) →
`HMAC(SHA256(master), userKey)` injected as `AF_SECRET_KEY`. The master is a single point of
failure, there is no per-tenant key rotation or revocation, and the key lives permanently in the
CP's environment. A self-hosted product ([0001](0001-self-host-vs-saas.md)) needs "the company
holds the keys, and offboarding revokes cleanly."

## Decision

**Promote it to envelope encryption plus a custodian abstraction.** A per-workspace DEK is
wrapped by a per-tenant KEK and stored as `WrappedDEK`. When a workspace starts, the CP unwraps
it through the custodian and **injects `AF_SECRET_KEY` over exactly the same path as Phase 2**
(the Agent's `secrets.go` is untouched). The custodian is swappable per environment:

- `local`, the default = `localCustodian` (KEK = `HKDF(AF_MASTER_KEY, "af-kek:"+keyRef)`,
  AES-256-GCM).
- `local`, hardened = Vault transit. `aws` = KMS. All behind the same
  `KeyCustodian{Wrap, Unwrap}` interface.
- The DEK granularity is **per workspace** (one user's leaked key does not spread to others; we
  deliberately do not use one key per tenant).
- Migration needs no code change and no downtime: existing `secrets.enc` files are not
  re-encrypted. The initial DEK is the old `HMAC(master, userKey)`, wrapped and stored — the
  same value is injected, so the existing store decrypts as before.

## Consequences, and the honest limits

- What this buys: (1) the **seam** where dropping in Vault/KMS later gives real per-tenant
  revocation (crypto-shredding), (2) a `wrapped_dek` structure that lets the DEK be rotated to a
  random one in future, (3) the per-tenant `key_ref` plumbing.
- **On-prem, the localCustodian's KEK is derived from the master, so anyone holding the master
  can unwrap every DEK** — the strength is equivalent to a single master. **Real per-tenant
  crypto-shredding (disable a tenant key so that only that tenant becomes undecryptable) arrives
  with Vault/KMS.** P3-3 lays the groundwork safely up to that point.

## Addendum (2026-10-04) — the AWS KMS custodian (#969)

The `aws` = KMS line of the decision is built (`control-plane/custodian_kms.go`). The decisions
above stand; this records how the KMS custodian fills them in and what it does not do.

- **Selection.** `AF_KEY_CUSTODIAN=local` (default) or `kms`, with `AF_KMS_KEY_ID` (a key ARN,
  or an alias). An unknown value, `kms` without a key id, or `kms` without `AF_MASTER_KEY`
  stops the Control Plane at boot.
- **Envelope.** Each `Wrap` asks KMS for a fresh AES-256 data key (`GenerateDataKey`) and seals
  the payload with it locally (AES-256-GCM, `keyRef` as AAD); the stored value is
  `kms1:` + base64(blob length, KMS ciphertext blob, nonce, sealed payload). The custodian seals
  more than DEKs — session handoff and share bodies exceed KMS `Encrypt`'s 4 KiB — which is
  why it is an envelope rather than a direct `Encrypt`. `Unwrap` calls `Decrypt` with the
  configured key id.
- **Binding.** Every call carries the encryption context `af:purpose=agent-fleet-custodian`,
  `af:key_ref=<keyRef>`, so a value moved to another tenant's row is refused by KMS. A
  deployment name is deliberately not in the context: the key is the deployment's own, and a
  renamed stack would otherwise make every stored value unreadable. The CP task role's grant
  (`30-ingress` `CpCustodianKmsPolicy`) and the key policy (`10-data` `CustodianKey`) require
  that context.
- **Migration: format dispatch, no rewrite.** A stored value without the `kms1:` prefix was
  sealed by the local custodian and is opened by it, so everything stored before the switch
  stays readable for as long as `AF_MASTER_KEY` is set. What reads a value is decided by its
  format, never by a KMS failure. This follows the "no code change, no downtime, no
  re-encryption" stance of the original migration.
- **Fail closed.** A KMS error fails the seal or the open with an error naming KMS; the local
  key is never tried for a `kms1:` value. The local custodian, given a `kms1:` value (a
  deployment switched back), refuses it and says so.
- **Cache.** Unwrapped data keys are cached in memory per (keyRef, blob) for
  `AF_KMS_DATA_KEY_CACHE_TTL` (default 5 minutes, `0` = off), at most 1,024 entries.

Limits, stated plainly:

- **Rows stored before the switch keep master-key-only protection**, and a workspace's DEK is
  wrapped once, at its first start, so existing workspaces keep it too. **Crypto-shredding by
  disabling the KMS key covers only values sealed after the switch.**
- **The workspace DEK is still the legacy `HMAC(master, userKey)`**, also for a workspace first
  started under KMS, because the CP cannot tell whether the member's home already holds a
  `secrets.enc` written with it (a workspace row can be recreated over a kept home). KMS wraps
  it, but a holder of the master key can derive it, so members' stored credentials are not
  crypto-shredded by disabling the KMS key. What KMS does shred is what the custodian seals
  directly: MCP headers, sign-in client secrets, engine tokens, handoffs, shares.
- **`AF_MASTER_KEY` stays required** with `kms`: it opens the legacy values and still derives
  the legacy DEK and the bridge signing keys.
- One KMS key per deployment. Disabling it shreds every tenant's post-switch values at once; a
  single tenant can be cut off by a key-policy deny on its `af:key_ref`, which is revocation an
  administrator of the key can undo, not a shred. A key per tenant is not built.
- A disabled key keeps opening already-cached data keys for up to one cache TTL.

Remaining: random DEKs for workspaces, with a way to re-encrypt an existing `secrets.enc`
(#1646); a one-shot rewrap command that re-seals every legacy value (`wrapped_dek`, MCP
headers, sign-in client secrets, engine tokens, handoffs, shares) under KMS (#1645);
per-tenant KMS keys; Vault transit. KMS key rotation is AWS's automatic rotation (`EnableKeyRotation`), which
needs nothing from the Control Plane.

## Addendum (2026-10-10) — re-sealing legacy values (#1645)

The 2026-10-04 addendum stands. `af-cp rewrap-keys` (`control-plane/rewrap_keys.go`) is the
one-shot rewrite it left for later: an operator runs it once the Control Plane is on
`AF_KEY_CUSTODIAN=kms`, and every value without the `kms1:` prefix is opened by the local
custodian and sealed again by KMS. Format dispatch stays as it is: the command only changes
which format a row is in.

- **Scope.** The six tables in `store.sealedColumns` (`wrapped_dek`, `mcp_server`,
  `tenant_idp`, `tenant_git_oauth`, `session_share_proposal`, `session_handoff_offer`) and the
  three settings rows of the engine tokens (Hugging Face, Civitai, the ComfyUI record). Two
  tests keep the list complete: one fails on a schema column that looks sealed (`key_ref`,
  `*_enc`, `ciphertext`) and is not listed, the other on any `Wrap` / `sealTenantSecret` call in
  the module that is not mapped to a target. Values stored unsealed (empty key ref) are counted
  and left alone.
- **Per row, fail closed.** Open with the master key, seal with KMS, open the new value through
  KMS with the data-key cache off, and only then write it with a compare-and-swap on the old
  value. A KMS error, a refused `Decrypt` or a read-back mismatch stops the run before the row is
  written; a row that changed meanwhile is left to the newer value. Every row is therefore in
  one of the two formats at every moment, and both open. `--dry-run` counts without calling KMS.
- **Exit `0` comes from a final read-only pass**, for `--dry-run` and a real run alike: no
  legacy value and no unreadable row when the command last looked. It is not a lock. Control
  Plane edits that carry a stored value forward (an IdP or Git OAuth app saved without its
  secret, the ComfyUI panel saved without its key) write back what they read, so one that read a
  legacy value before the swap can restore it; the final pass catches that during the run, not
  after it. The guide therefore says to run it while no administrator is editing and to confirm
  with `--dry-run`. Re-sealing in those edit paths instead was left out to keep the change to
  the command.
- **`AF_MASTER_KEY` can still not be dropped.** It derives the workspace DEK
  (`HMAC(master, userKey)`) — the command moves `wrapped_dek` to KMS, but the DEK inside is still
  derivable, so credential stores are still not crypto-shredded (#1646) — and every bridge
  signing key, and `kms` without it stops the Control Plane at boot. What the rewrap does buy:
  once a `--dry-run` exits `0` with no edit in flight, disabling the KMS key shreds every
  custodian-sealed value, including the ones stored before the switch, and the master key no
  longer opens them.

Not verified against a real KMS key: the tests use an in-memory KMS. The first run on a real
deployment should be a `--dry-run`.

## Addendum (2026-10-10) — a random credential-store key per home, part A (#1646)

The decision's "a `wrapped_dek` structure that lets the DEK be rotated to a random one in
future" is taken up, opt-in. The derived DEK and every decision above stand.

- **Per home, not per workspace.** `home_dek` (migration 0090 / pg 0075) holds one random key
  per membership, sealed by the custodian under the tenant key ref. `DeleteWorkspace` drops
  `wrapped_dek` while the home may be kept, so a random key that went with the workspace row
  would shred a kept home by accident. Nothing deletes a row yet, Destroy included: an adapter's
  Destroy does not prove the whole home is gone (ecs without the home task removes only the
  access points, and a retry after a partial failure then reports no leftovers). A kept row is
  an unused sealed key, never an unreadable store.
- **Opt-in, kms only.** `AF_WORKSPACE_DEK=random`, refused at boot unless
  `AF_KEY_CUSTODIAN=kms`: under the local custodian the key would be wrapped by a
  master-derived KEK and buy nothing. A home that has a key keeps getting it after the flag is
  turned off, because its store may already be sealed under it.
- **Migration without guessing.** The CP cannot see a kept home's `secrets.enc`, so it does not
  decide which key a store uses. While a home is `migrating` it injects both: `AF_SECRET_KEY`
  stays the derived key and `AF_SECRET_KEY_NEXT` is the home's key, through each runtime's
  secret channel. The Agent opens with either, re-seals under NEXT at boot (temp file + rename
  under the store lock), and never rewrites a store neither key opens. The naming is chosen
  for version skew: an Agent that predates NEXT ignores it and keeps the store on the derived
  key, so a new CP with an old workspace image loses nothing until the store has moved.
  `/healthz` reports `secrets_key` (a state name, never a key or length).
- **Fail closed.** A custodian that cannot seal or open the home's key fails the start; it is
  never started on the derived key alone. The keys are resolved again at every real start, not
  taken from the memoized runtime.

Limits, stated plainly:

- Homes not started since the flag was turned on stay derived and are **not** shredded by
  disabling the KMS key; `af-cp home-dek-status` counts them. Snapshots and backups taken
  before a store moved hold the derived-key file.
- Disabling the key shreds the store at rest only. A running workspace keeps the key in its
  environment and goes on using the store; the runtime keeps its own copy to start it (docker
  container env, Kubernetes Secret, ECS SSM SecureStrings under the account's SSM key, kept
  until Destroy); the CP's data-key cache holds it for one TTL.
- The Agent's `Save` now refuses to write over a store it cannot open (the same rule `Update`
  already had), so a member whose store is unreadable cannot overwrite it until it is removed.
- Part A does not stop injecting the derived key; it no longer opens a moved store, and the
  confirm step that marks a home `random` and stops it is part B.
- Downgrading the CP or the workspace image after a store moved, or losing `home_dek`, leaves
  that store unreadable; members reconnect what they had stored.
- Verified by unit tests with fakes only. A kept home recreated on real ECS/EFS, and a
  downgrade, have not been run. The ECS stacks do not expose the flag yet.

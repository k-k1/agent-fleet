# 0005. Keys at rest — envelope encryption + a custodian abstraction (with the on-prem limit stated)

English | [日本語](0005-envelope-custodian.ja.md)

- Status: decided (P3-3)
- See also: [history/p3-3-envelope-crypto](../log/p3-3-envelope-crypto.md) / [build/07 §7.6 Secrets and envelope encryption](../build/07-security.md#76-secrets-and-envelope-encryption) (formerly security §4.4) / [Roadmap §12.3](../roadmap.md#123-tos-と分離の留意自社ホスト前提)

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

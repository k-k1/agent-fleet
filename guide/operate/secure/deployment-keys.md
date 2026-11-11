---
audience: "whoever generates and replaces the deployment's secrets (IT / SRE with a shell on the host and the deployment administrator role)"
source_of_truth: "the Control Plane's key check at start (`control-plane/master_key_guard.go`) and `af-cp rewrap-keys --seal-plaintext`"
---

# Handle the deployment keys

English | [日本語](deployment-keys.ja.md)

What the two keys that protect everything else, `AF_MASTER_KEY` and `AF_COOKIE_SECRET`, must be, and what happens when you change them.
This page also covers adding a key to a deployment that ran without one and sealing the values stored before it. Protecting the key with KMS and per-home keys are in [Keys at rest](../12-keys.md).

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Generate both keys at random and set them | Checks their length at start and refuses to start when one is too short |
| Add a key to a deployment that had none | Seals values from then on; keeps warning while values from before the key remain |
| Seal the old values with `af-cp rewrap-keys --seal-plaintext` | Seals them row by row, opening each again to compare before writing |

## The two keys

Each is only as strong as the value you chose.

| | `AF_MASTER_KEY` | `AF_COOKIE_SECRET` |
|---|---|---|
| Protects | The root of every sealed value (client secrets, MCP headers, handoffs, inbox), the key of each member's credential store, the tokens workspaces use to call the Control Plane back | The signatures of the session, sign-in state and preview cookies |
| Must be | **32 random bytes or more** (hex or base64; 43 characters or more as a raw string). No passphrases | The same |
| Generate with | `head -c 32 /dev/urandom \| base64` | `head -c 32 /dev/urandom \| base64` |
| Cost of changing it | **Do not change it on a running deployment.** Everything sealed under the old value stops opening, and no command re-keys it | Everybody signs in once more ([End everybody's sessions now](offboarding.md#end-everybodys-sessions-now)) |

Generating and keeping them is in [Install Agent Fleet](../install.md#2-generate-the-secrets).

## The check at start

- Only the length is measured (a repeat of a few byte values is refused too). A long passphrase and random bytes cannot be told apart, so always generate the value
- On anything but `AUTH=dev`, a value that fails the check **stops the Control Plane at start**, with a message naming the variable (`AF_MASTER_KEY`, `AF_COOKIE_SECRET` or both)
- The check never logs the value
- On ECS, `standup.sh` checks the stored parameters by the same rule and prints only OK or too short

### Before upgrading to a release that refuses to start

Check the values you use and replace a short one first.

1. A short `AF_COOKIE_SECRET` is easy: generate a new one, set it and restart. Everybody signs in once more
2. A short `AF_MASTER_KEY` is not: what it sealed stops opening, and no command re-keys it. Before upgrading, plan the move to a generated key, including members reconnecting the credentials they saved. With the old value, the Control Plane does not start

## Add a key to a deployment without one

Without `AF_MASTER_KEY`, credentials members save sit unencrypted in each home. That behaviour exists for `AUTH=dev`.
With any other `AUTH` the Control Plane still starts, but logs a `WARNING` at start and shows a warning banner in the Admin screen.

Adding a key later is not transparent, so arrange it with the members and go in this order:

1. Set the key and restart the Control Plane
2. **Stop every existing workspace, then start it again.** One left running has no key and keeps writing in plain text
3. Have members reconnect the credentials they had saved
4. Delete the old unencrypted file (`~/.config/agent-fleet/secrets.json` in each home) and rotate the credentials it held. The file is in the backups too

## Seal values stored before the key

A Control Plane without `AF_MASTER_KEY` stored some values unencrypted. **Setting the key later does not seal them**; they are read as they are.

- Which values: the tenant sign-in and Git OAuth client secrets, MCP headers and client secrets, engine tokens, handoffs, share proposals and inbox messages
- While any remain, the Control Plane logs a `WARNING` at start and shows a warning banner in the Admin screen (`plaintext_sealed`)

Run it while no administrator is editing, with the same configuration and database as the Control Plane:

```
af-cp rewrap-keys --seal-plaintext --dry-run   # counts only, changes nothing
af-cp rewrap-keys --seal-plaintext
```

- It works with local keys and with `AF_KEY_CUSTODIAN=kms`, sealing with whichever is configured
- It seals one row at a time and opens the sealed value again to compare before writing. It exits with status 0 only when a final read-only check finds nothing left
- The banner disappears within minutes (or at the next restart)
- With KMS as well, follow with `af-cp rewrap-keys` in [Keys at rest](../12-keys.md)

Those values were readable from the database and the backups. **Rotate them at their issuer too** (IdP and Git OAuth client secrets, MCP tokens, Hugging Face, Civitai and ComfyUI tokens), and delete backups you no longer need.

## Per-purpose cookie keys

The Control Plane signs the session, sign-in state and preview cookies each with its own key, so a cookie made for one purpose does not verify for another. This is the default.
A replica older than 0.42.0 cannot read the new format, so upgrade in this order:

1. **First bring every replica to 0.42.0 or later.** It reads both formats
2. Then move to the release that issues per-purpose keys. It still accepts the old format for one session lifetime per start (`AF_SESSION_TTL`, 7 days by default), so rolling updates and restarts sign nobody out

- Sign-in state and preview tokens expire within minutes. During that period, the old format is not separated by purpose
- To keep issuing the old format for one more release, set `AF_COOKIE_PURPOSE_KEYS=0` (on ECS, set the ingress stack's `CookiePurposeKeys` parameter to `0` and update the stack). This fallback goes away, with the old format, in a later release
- Everybody is signed out only if you roll back to a release older than 0.42.0 after the new format was issued
- Do not shorten `AF_SESSION_TTL` in the same update: it shortens the period the old format is accepted too

## Related

- [Install Agent Fleet](../install.md#2-generate-the-secrets): generate and keep the keys
- [Keys at rest](../12-keys.md): KMS for the key, per-home keys
- [Back up and restore](../run/backup-restore.md#restore): a restore needs the same `AF_MASTER_KEY`
- [Understand the threat model and residual risks](threat-model.md)

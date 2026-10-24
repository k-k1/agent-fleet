---
audience: "a deployment administrator on AWS who wants the Control Plane's keys held by KMS, with or without a key of its own for each home"
source_of_truth: "`deploy/aws/ecs/README.md` and `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters; `af-cp rewrap-keys --help` and `af-cp home-dek-status --help` for the commands"
---

# 12. Keys at rest: AWS KMS and a key for each home

English | [日本語](12-keys.ja.md)

By default every secret the Control Plane seals is protected by a key derived from `AF_MASTER_KEY`. On
`ecs` / `ecs-ec2` that custody can move to AWS KMS, and each member's home can then have a key of its own.
This chapter is what each step decides, and what disabling a key does and does not shred. The threat
model these answer is in [04 Securing it](04-secure.md#residual-risks-what-to-do-about-them); the parameters
are in the [ECS runbook](../../deploy/aws/ecs/README.md#keys-at-rest-on-kms-optional-off-by-default).

## Keys at rest on AWS KMS

On `ecs` / `ecs-ec2` the Control Plane can have AWS KMS hold the keys for what it seals: MCP
connection headers, sign-in and Git OAuth client secrets, engine tokens, session handoffs and
shares, inbox messages and their attachments, the development sign-in's signing keys and client
secrets, the key of each workspace's credential store, and each home's own credential-store key
where homes have one. Each value is sealed with a fresh data key from KMS,
bound to its tenant, so it cannot be opened under another tenant's name. Turning it on:

1. Deploy `10-data` with `CustodianKmsKey=create` (or bring a symmetric KMS key of your own in
   the same account), and read the `CustodianKmsKeyArn` output.
2. Deploy `30-ingress` with `CustodianKmsKeyArn=<that ARN>`. The Control Plane gets
   `AF_KEY_CUSTODIAN=kms` and `AF_KMS_KEY_ID`, and its task role gets `kms:GenerateDataKey` and
   `kms:Decrypt` on that key only, and only with the Control Plane's encryption context.

What to know before you switch:

- **`AF_MASTER_KEY` stays required.** It still opens everything stored before the switch, and
  other keys are derived from it. Keep it exactly as before.
- **Nothing is re-encrypted on its own.** Values stored before the switch stay readable and stay
  protected by the master key alone; only what is stored afterwards is protected by KMS, until
  you run `rewrap-keys` ([below](#re-sealing-values-stored-before-the-switch)).
- **Members' stored credentials are not shredded by KMS.** Only a workspace whose key is stored
  for the first time after the switch has it wrapped by KMS; a workspace that already had one
  keeps the master-key wrapping, and restarting it changes nothing. Either way the key itself is
  still derived from `AF_MASTER_KEY` and the member, so that stores written before keep opening,
  and anyone with the master key can still derive it. Protect the master key as before. A key
  of its own for each home takes the master key out of that path, though not every copy of
  the key ([below](#a-key-of-its-own-for-each-home)).
- **No fallback.** If KMS cannot be reached or refuses, sealing and opening fail with an error
  that names KMS. The Control Plane never quietly uses the master key for a value KMS sealed.
- **Opened keys are cached in memory for 5 minutes** (`AF_KMS_DATA_KEY_CACHE_TTL`, `0` turns it
  off), so disabling the key takes effect within that time, not at once.
- **Disabling or scheduling deletion of the KMS key makes everything sealed after the switch
  unreadable to the Control Plane**, for every tenant at once. That is the crypto-shred lever, so restrict who can
  administer the key. To cut off one tenant, add a statement to the key policy that denies
  `kms:Decrypt` when `kms:EncryptionContext:af:key_ref` is that tenant's id; that is a
  revocation an administrator of the key can undo, not a shred.
- **Do not switch back to `local`** while values sealed by KMS exist: the local custodian refuses
  them with an error that says so.

## Re-sealing values stored before the switch

`af-cp rewrap-keys` re-seals, under KMS, every value the Control Plane sealed with the master
key before the switch: the wrapped key of each workspace's credential store, MCP connection
headers, sign-in and Git OAuth client secrets, session handoffs and share proposals, inbox
messages and their attachments, the development sign-in's signing keys and client secrets, each
home's own credential-store key, and the Hugging Face, Civitai and ComfyUI keys of the engines. Afterwards disabling the KMS key makes
those unreadable too.

- Run it **after** the Control Plane itself runs with `AF_KEY_CUSTODIAN=kms`, with the Control
  Plane's own environment: the same `AF_MASTER_KEY`, `AF_KMS_KEY_ID`, database settings and
  task role. It refuses to run under `local`. A Control Plane still on `local` cannot open what
  the command writes.
- Start with `--dry-run`: it prints, for each place, how many values are already on KMS, how
  many are in the old format, and how many are stored unsealed (written by a Control Plane that
  had no master key; the command leaves those alone). It changes nothing and does not call KMS.
- Without `--dry-run` each value is opened with the master key, sealed by KMS, opened again
  through KMS to check it reads back, and only then written, one row at a time and only if the
  row has not changed meanwhile. Interrupting it, a KMS error or a refused `Decrypt` stops it
  before the row it was on is written: every row is left either in the old format or on KMS,
  and both open. After the rewrite it looks at every place again without changing anything,
  and the exit code comes from that final check. Run it again until it exits `0`; a run with
  nothing left to do changes nothing.
- **Run it while no administrator is editing.** The Control Plane can keep running, but saving
  a sign-in provider or Git OAuth app without retyping its secret, or the ComfyUI panel without
  retyping its key, writes back the stored value as it was read; a save that read the old value
  before the command rewrote it puts the old value back. The final check catches one that lands
  during the run; one that lands after it does not. Confirm afterwards with `--dry-run`.
- Exit `0` (with or without `--dry-run`): when the command last looked, nothing was in the old
  format and every row could be read. `1`: something is still in the old format or could not
  be read or written, or the run stopped; the log names the place and the row id, never a
  value. A `--dry-run` therefore exits `1` before the rewrap and `0` after it. `2`:
  configuration (not `kms`, no master key, no key id, no database).
- On `ecs` / `ecs-ec2`, run it as a one-off task of the Control Plane's task definition with the command
  overridden; the command to paste is in [the ECS runbook](../../deploy/aws/ecs/README.md#running-af-cp-commands-against-the-control-planes-database).

**`AF_MASTER_KEY` still cannot be dropped afterwards**, and must not change:

- The key of each workspace's credential store is still derived from it and the member
  (`HMAC(master, userKey)`). The command moves the *wrapped copy* of that key to KMS, so with
  the KMS key disabled the Control Plane can no longer start the workspace with it, but anyone
  holding the master key can still derive it and open `secrets.enc`. Members' stored
  credentials are therefore still not crypto-shredded by KMS. A key of its own for each home
  changes that only in part ([next section](#a-key-of-its-own-for-each-home)).
- A workspace started for the first time still gets its key from the same derivation.
- The tokens that workspaces use to call back into the Control Plane (Git credentials, Git
  OAuth, memos, schedules, engines, branch rules, AWS and Google Cloud profiles, documents, MCP)
  are signed with keys derived from it.
- The Control Plane refuses to start with `AF_KEY_CUSTODIAN=kms` and no `AF_MASTER_KEY`.

What the command does change: the master key alone no longer opens the values it re-sealed,
apart from the credential-store keys, which it can still derive.

## A key of its own for each home

With `AF_WORKSPACE_DEK=random` the Control Plane gives each member's home a random key for
its credential store (`secrets.enc`), sealed by KMS. It is accepted only with
`AF_KEY_CUSTODIAN=kms`; any other combination, or another value, stops the Control Plane at
boot. On `ecs` / `ecs-ec2` it is the `30-ingress` parameter `WorkspaceDekRandom=on`, which needs
`CustodianKmsKeyArn` (without it the update is rolled back when CloudFormation runs it; a change set can still be created); [before you turn it on](#before-you-turn-it-on-on-ecs).

### Before you turn it on on ECS

Deploy `30-ingress` with `WorkspaceDekRandom=on`
together with `CustodianKmsKeyArn`; only the Control Plane task definition changes (it gets
`AF_WORKSPACE_DEK=random`) and the service rolls. Check first:

- Kept homes stay on the derived key until their first start with the flag; the
  `secret-key-next` SSM parameter appears per workspace from that start. After the home is
  confirmed, the next start tries to delete it (a failure is logged and the start goes on), and
  Destroy deletes it; a running task keeps its environment until restarted. The existing
  `/af-ws/*` grants already cover it.
- Turning the parameter off only stops minting keys for new homes: homes that already have a
  key keep using it and need no reconnecting. Going back to a Control Plane that does not
  know the home's key, or losing the `home_dek` table, is a different case and can need
  credentials reconnected (see "Turning it off does not undo it" below).
- Homes that have not started since, and old snapshots or backups, are **not** shredded
  by disabling the KMS key. A key whose home is destroyed is removed only when the runtime
  proves the home gone (next item).

### How the home's key behaves

- **The key belongs to the home, not the workspace.** Deleting and re-creating a workspace over
  a kept home keeps the key. A key is removed in one case: Destroy finished, a workspace row no
  longer names the home, and the runtime **proves** the whole home is gone. A key dropped while
  part of the home survives makes that part unreadable, so any other answer keeps it.
  What each runtime can prove: `docker` and `native` cannot (a missing directory is also what a
  detached volume or a wrong `WS_DATA` looks like), so their keys go through the operator
  path below; `kubernetes` and `ecs`/`ecs-ec2` can only say so
  from the Destroy they just ran (the StatefulSet deleted after every claim and volume was
  confirmed gone; the EFS home task exiting 0 with every access point rooted at a directory it
  removes; an access point rooted elsewhere is kept and reported, not deleted), never afterwards, and never from "no leftovers"
  (`ecs` without the home task removes only the access points, and a retry then reports none).
  A key still `migrating` is never removed this way, and a runtime that cannot prove it keeps
  the key. Once the row is deleted, the store and any copy of it sealed under that key (a
  snapshot, a backup) no longer open, unless a database backup that still holds the
  `home_dek` row is restored while the KMS key is enabled. Copies made before the store moved
  are on the derived key ([below](#disabling-the-kms-key-restoring-a-copy-turning-it-off)).
- **Orphaned keys.** Keys that were kept (a `migrating` probe of a golden bake, a runtime that
  could not prove it, a destroy by an older Control Plane) are listed by
  `af-cp home-dek-status --orphans` (read-only; keys no workspace row names). A listed key is a
  candidate, not a proof, because a home can outlive its workspace row. After you have checked
  that the home is gone (the EFS directories, the EBS volume and snapshots, the cluster
  claims, or the data directory), remove it with
  `af-cp home-dek-status --remove-orphan <membership-id> --force`. `--force` is your statement
  that the home is gone, because that command has no runtime to check with. It still refuses
  a key a workspace row names, and refuses while a workspace operation on that membership is
  in progress. It exits `0` when it removed the key, `1` when it removed nothing, and `2`
  when the database fails. A key removed while the home exists makes its `secrets.enc`
  unreadable for good.
- **Moving a store to it.** The first start after you turn it on mints the home's key, and the
  Control Plane passes it to the workspace beside the derived key. At boot the workspace opens
  `secrets.enc` with either one and re-seals it under the home's key. A store that neither key
  opens is not touched. A home with no store yet gets an empty one under the home's key at
  that boot, so that no writer holding only the derived key can create one under it later.
  The workspace's `/healthz` reports the outcome as `secrets_key` (`none`, `current`,
  `migrated`, `derived` when the store could not be put under the home's key, `unreadable`) and
  `secrets_key_next` (whether it sealed under the home's key), never a key.
- **Confirming it.** After such a start the Control Plane waits for that report, and only for
  the one from the workspace that start launched: each start passes a fresh identifier
  (`AF_HOME_KEY_START`, not a secret) that the workspace echoes as `secrets_key_start`, so an
  answer from an earlier task or another workspace is ignored, and a redirect is not followed.
  When the workspace sealed under the home's key and reports `migrated` or `current`, the
  Control Plane marks the home confirmed, and from the **next** start the workspace gets the
  home's key alone (as `AF_SECRET_KEY`) and no derived key; on `ecs` / `ecs-ec2` that start also
  deletes the `secret-key-next` parameter. A workspace that is already running keeps both keys
  in its environment until it is restarted. `unreadable` or `derived` leaves the home
  migrating, is logged, and deletes nothing. The wait asks every 3 seconds for at most 15
  minutes; if that runs out, or the Control Plane restarts while it waits, the home stays
  migrating and is confirmed at a later start. `af-cp home-dek-status` counts migrating and
  confirmed homes; on `ecs` / `ecs-ec2` run it as a one-off task the same way as `rewrap-keys` above.

### Disabling the KMS key, restoring a copy, turning it off

- **What disabling the KMS key does, and what it does not.** Once a home's store has been
  re-sealed, the key derived from `AF_MASTER_KEY` no longer opens it, and once the Control
  Plane's data-key cache has expired (`AF_KMS_DATA_KEY_CACHE_TTL`, 5 minutes by default) the
  Control Plane can no longer unwrap the home's key, so it refuses the next start. Within that
  TTL a start can still succeed. **That alone is not crypto-shredding**: copies of the home's
  key already handed out still open the store, each protected by something other than the
  custodian key:
  - a **running workspace** keeps it in its environment, and its Agent, git helper and MCP
    servers go on reading and writing the store without asking KMS;
  - the **runtime's own copy** used to start it, which outlives a stop: the container's
    environment on docker, the Kubernetes Secret, and on `ecs` / `ecs-ec2` the per-workspace
    SSM SecureString parameters (`secret-key`, `secret-key-next`), encrypted with the
    account's SSM key.

  To shred a home's store while keeping the home, disable the key **and** stop the workspace
  **and** remove those runtime copies yourself. Nothing here does that automatically. Destroy
  is not that procedure: it removes the runtime copies and tries to remove the home as well,
  and on an `ecs` stack without the home task it removes only the access points and leaves
  the home's EFS directories behind.
- **What it does not.** A home that has not been started since you turned it on is still on the
  derived key, and is **not** shredded by disabling the KMS key. `af-cp home-dek-status` counts
  them (read-only). Copies of a home made before its store was re-sealed (ecs-ec2 snapshots,
  backups) still hold the old file, which the master key can open. Until a home is confirmed,
  the derived key is still passed beside the home's key; it no longer opens a re-sealed store.
- **A copy restored from before the move.** On a confirmed home, a `secrets.enc` restored from
  a snapshot or backup taken before its store moved is sealed under the derived key, which
  that home no longer receives: the workspace reports `unreadable` and refuses to write over
  it. Run `af-cp home-dek-status --remigrate <membership-id>` with the Control Plane's
  environment, then restart the workspace: it gets both keys again and re-seals the restored
  store. The command exits `1` when the membership has no confirmed home key, `2` when the
  database fails.
- **Turning it off does not undo it.** With `AF_WORKSPACE_DEK` unset again, homes that already
  have a key keep getting it, because their store may be sealed under it. After a store has
  been re-sealed, two kinds of going back differ:
  - **A Control Plane that does not know the home's key, or a lost `home_dek` table**, with a
    workspace image of v0.31.0 or later: the store no longer opens, and the workspace refuses
    every write to it, reconnecting a credential included (that image never writes over a
    store it cannot open).
  - **A workspace image older than v0.31.0: do not start one on a home that has moved.**
    Its Agent has no such guard: a save after a failed read can write an empty store over the
    re-sealed one, and the credentials are lost.

  To recover, first restore what opens the store, before any workspace starts on that home:
  the Control Plane version, its `home_dek` rows and the KMS key. Only if the stored
  credentials are to be given up, stop the workspace, move the unreadable `secrets.enc` out of
  the member's agent configuration directory (`~/.config/agent-fleet/`) yourself, and have the
  member reconnect. A workspace image older than the Control Plane loses nothing as long as
  the store has not been re-sealed yet: it ignores the home's key and keeps using the derived
  one.
- **No fallback.** If KMS cannot seal or open a home's key, the workspace does not start. It is
  never started on the derived key alone.

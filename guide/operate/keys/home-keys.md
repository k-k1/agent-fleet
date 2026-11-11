---
audience: "a deployment administrator on AWS who wants members' credential stores protected by a key of each home's own"
source_of_truth: "`deploy/aws/ecs/README.md` and `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters; `af-cp home-dek-status --help` for the command"
---

# Give each home its own key

English | [日本語](home-keys.ja.md)

This is for AWS deployments (`ecs` / `ecs-ec2`) only. Each member's home gets a random key, and its credential store (`secrets.enc`) is sealed under that key.
Even someone holding `AF_MASTER_KEY` can then no longer open the store, and disabling the KMS key makes it unreadable. [Put the keys at rest on AWS KMS](kms.md) comes first. The parameters are in the [ECS runbook](../../../deploy/aws/ecs/README.md#a-key-of-its-own-for-each-home-optional-kms-only).

## How it works

| What you do | What happens |
|---|---|
| Deploy `30-ingress` with `WorkspaceDekRandom=on` | The Control Plane gets `AF_WORKSPACE_DEK=random` and the service rolls |
| Nothing (members start their workspaces) | The first start mints the home's key, the workspace re-seals its store under it, and the Control Plane confirms the result |
| Run `af-cp home-dek-status` | It counts migrating homes, confirmed homes, and homes not started since |
| Remove an orphaned key after the home is gone | That home's store and its copies never open again |

## Turn it on

Deploy `30-ingress` with `WorkspaceDekRandom=on` together with `CustodianKmsKeyArn`. Only the Control Plane task definition changes.

- Without `CustodianKmsKeyArn` the update is rolled back when CloudFormation runs it. A change set can still be created, so check the stack events
- `AF_WORKSPACE_DEK=random` is accepted only with `AF_KEY_CUSTODIAN=kms`. Any other combination, or another value, stops the Control Plane at boot
- The existing grants on `/af-ws/*` already cover what it needs

### Check before you turn it on

| Point | Detail |
|---|---|
| Kept homes | They stay on the derived key until their first start with the flag |
| The `secret-key-next` SSM parameter | It appears per workspace from that start. After the home is confirmed, the next start tries to delete it (a failure is logged and the start goes on); Destroy deletes it too |
| Running tasks | They keep their current environment until restarted |
| Turning the parameter off | It only stops minting keys for new homes. Homes that already have a key keep using it and need no reconnecting ([Turning it off](#turning-it-off)) |
| What is not shredded | Homes that have not started since, and old snapshots and backups, stay readable when you disable the KMS key |

## From the home's key to a confirmed home

1. The first start after you turn it on mints the home's key, and the Control Plane passes it to the workspace beside the derived key
2. At boot the workspace opens `secrets.enc` with either key and re-seals it under the home's key. A store neither key opens is not touched. A home with no store yet gets an empty store under the home's key
3. The Control Plane waits for the report of the workspace that start launched. It asks every 3 seconds, for at most 15 minutes
4. When the workspace sealed under the home's key and reports `migrated` or `current`, the home is marked confirmed
5. From the **next** start the workspace gets the home's key alone, as `AF_SECRET_KEY`, and no derived key. On ECS that start also deletes `secret-key-next`

The empty store is created first so that no writer holding only the derived key can create one under it later.

### What the workspace reports

The workspace's `/healthz` reports the outcome, never a key.

| `secrets_key` | Meaning | What the Control Plane does |
|---|---|---|
| `none` | There is no store. A workspace given the home's key creates an empty store and reports `current`, so this does not normally appear | Leaves it migrating |
| `current` | The store is already sealed under the home's key | Marks it confirmed |
| `migrated` | This start re-sealed it under the home's key | Marks it confirmed |
| `derived` | It could not be put under the home's key and stays on the derived key | Leaves it migrating, logs it, deletes nothing |
| `unreadable` | Neither key opens it | Leaves it migrating, logs it, deletes nothing |

- `secrets_key_next` reports whether it sealed under the home's key
- Each start passes a fresh identifier (`AF_HOME_KEY_START`, not a secret) that the workspace echoes as `secrets_key_start`. An answer from an earlier task or another workspace is ignored, and a redirect is not followed
- If the 15 minutes run out, or the Control Plane restarts while it waits, the home stays migrating and is confirmed at a later start
- A workspace that is already running keeps both keys in its environment until it is restarted

`af-cp home-dek-status` counts migrating and confirmed homes. On ECS, run it as a one-off task the same way as `rewrap-keys` ([ECS runbook](../../../deploy/aws/ecs/README.md#running-af-cp-commands-against-the-control-planes-database)).

## When a home's key is removed

The key belongs to the home, not the workspace. Deleting and re-creating a workspace over a kept home keeps the key.

A key is removed on its own only when all three hold:

- Destroy finished
- No workspace row names the home any more
- The runtime **proves** the whole home is gone

A key dropped while part of the home survives makes that part unreadable, so any other answer keeps the key. A key still `migrating` is never removed this way.

| Runtime | What it can prove |
|---|---|
| `ecs` / `ecs-ec2` | Only from the Destroy it just ran: the EFS home task exited with status 0 and every access point was rooted at a directory it removes. An access point rooted elsewhere is kept and reported, not deleted. `ecs` without the home task removes only the access points, and a retry then reports none |
| `kubernetes` | Only from the Destroy it just ran: the StatefulSet deleted after every claim and volume was confirmed gone |
| `docker` / `native` | Nothing: a missing directory is also what a detached volume or a wrong `WS_DATA` looks like |

It never checks afterwards, and never concludes from "no leftovers".

Once the key's row is deleted, the store and any copy of it sealed under that key (a snapshot, a backup) no longer open, unless a database backup that still holds the `home_dek` row is restored while the KMS key is enabled. Copies made before the store moved are on the derived key.

### Remove an orphaned key

Keys that were kept (a `migrating` probe of a golden bake, a runtime that could not prove it, a destroy by an older Control Plane) are removed by you.

1. List them with `af-cp home-dek-status --orphans`. It is read-only and shows the keys no workspace row names
2. A listed key is a candidate, not a proof: a home can outlive its workspace row. Check that the home is gone (the EFS directories, the EBS volume and snapshots, the cluster claims, the data directory)
3. Remove it with `af-cp home-dek-status --remove-orphan <membership-id> --force`

`--force` is your statement that the home is gone, because the command has no runtime to check with. Even with `--force` it refuses a key a workspace row names, and a key whose membership has a workspace operation in progress.

| Exit status | Meaning |
|---|---|
| Exits with status 0 | It removed the key |
| Exits with status 1 | It removed nothing |
| Exits with status 2 | The database failed |

A key removed while the home exists makes its `secrets.enc` unreadable for good.

## Disabling the KMS key

Once a home's store has been re-sealed, the key derived from `AF_MASTER_KEY` no longer opens it. Once the Control Plane's data-key cache has expired (`AF_KMS_DATA_KEY_CACHE_TTL`, 5 minutes by default), the Control Plane can no longer unwrap the home's key and refuses the next start. Within the cache's lifetime a start can still succeed.

**That alone does not shred the store.** Copies of the home's key already handed out remain, each protected by something other than the KMS key:

| Copy | Where it remains |
|---|---|
| A running workspace | Its environment. Its Agent, git credential helper and MCP servers go on reading and writing the store without asking KMS |
| The runtime's own copy | It outlives a stop. On ECS, the per-workspace SSM SecureString parameters (`secret-key`, `secret-key-next`), encrypted with the account's SSM key; on docker, the container's environment; on Kubernetes, the Secret |

To make a home's store unreadable while keeping the home, do all three. Nothing does it automatically.

1. Disable the KMS key
2. Stop the workspace
3. Remove those runtime copies yourself

Destroy is not that procedure. It removes the runtime copies and tries to remove the home as well; on an `ecs` stack without the home task it removes only the access points and leaves the home's EFS directories behind.

### What stays readable

- A home not started since you turned it on. It is still on the derived key. `af-cp home-dek-status` counts them
- Copies of a home made before its store was re-sealed (`ecs-ec2` snapshots, backups). They hold the old file, which `AF_MASTER_KEY` opens

## Restoring a copy from before the move

On a confirmed home, a `secrets.enc` restored from a snapshot or backup taken before its store moved is sealed under the derived key. That home no longer receives that key, so the workspace reports `unreadable` and refuses to write over it.

1. Run `af-cp home-dek-status --remigrate <membership-id>` with the Control Plane's environment
2. Restart the workspace. It gets both keys again and re-seals the restored store under the home's key

The command exits with status 1 when the membership has no confirmed home key, and with status 2 when the database fails.

## Turning it off

With `AF_WORKSPACE_DEK` unset again, homes that already have a key keep getting it, because their store may be sealed under it.

After a store has been re-sealed, the way you go back decides what happens:

| Going back | Result |
|---|---|
| A Control Plane that does not know the home's key, or a lost `home_dek` table (workspace image v0.31.0 or later) | The store no longer opens. The workspace refuses every write to it, reconnecting a credential included |
| A workspace image older than v0.31.0 | **Do not start one on a home that has moved.** A save after a failed read can write an empty store over the re-sealed one, and the credentials are lost |

A workspace image older than the Control Plane loses nothing as long as the store has not been re-sealed yet: it ignores the home's key and keeps using the derived one.

### Recover

1. Before any workspace starts on that home, restore what opens the store: the Control Plane version, its `home_dek` rows and the KMS key
2. Only if the stored credentials are to be given up: stop the workspace and move the unreadable `secrets.enc` out of the member's agent configuration directory (`~/.config/agent-fleet/`) yourself
3. Have the member reconnect their credentials

## When it goes wrong

| Symptom | Cause and fix |
|---|---|
| The workspace does not start | If KMS cannot seal or open the home's key, the workspace does not start; it is never started on the derived key alone. Check that the KMS key is enabled and the Control Plane's role can use it |
| A home stays migrating | The workspace reported `derived` or `unreadable`, or the 15-minute wait ran out. A later start confirms it. If `unreadable` persists, see [Restoring a copy from before the move](#restoring-a-copy-from-before-the-move) |
| A member says their credentials do not open | The deployment went back to a Control Plane that does not know the key, or lost the `home_dek` table. Go to [Recover](#recover) |

## Related

- [Put the keys at rest on AWS KMS](kms.md)
- [Rotate the bridge tokens](bridge-tokens.md)
- [The ECS runbook's "A key of its own for each home"](../../../deploy/aws/ecs/README.md#a-key-of-its-own-for-each-home-optional-kms-only)

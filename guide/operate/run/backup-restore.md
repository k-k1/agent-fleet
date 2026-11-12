---
audience: "Whoever protects a deployment's data and brings it back when it breaks"
source_of_truth: "`deploy/compose/backup.sh` and `restore.sh` (where they disagree, the scripts win)"
---

# Back up and restore

English | [日本語](backup-restore.ja.md)

Pack a Docker Compose deployment's data into an archive on a schedule, so you can bring it back on another host or after losing data.
The commands are in [the compose runbook, "Backup & restore"](../../../deploy/compose/README.md#backup--restore).

## How it works

| What you do | What happens |
|---|---|
| Run `backup.sh` from cron | All of `DATA_DIR` becomes a timestamped `tar.gz`, and old generations are pruned |
| Keep `AF_MASTER_KEY` in a vault separate from the data | Someone who steals only the archive cannot read members' credentials |
| To restore, put the same key back, then run `restore.sh` | The Control Plane comes up, and each workspace returns when a member presses Start |

## What the archive holds

| Included | Not included |
|---|---|
| `control-plane.db` (tenants, members, ports, tokens) | `shared/jvm` (a large JDK that can be fetched again) |
| Each member's home (working trees, dotfiles, the encrypted `secrets.enc`) | **`AF_MASTER_KEY`** (it lives in `.env` and is left out on purpose) |
| Each member's `claude-config` (**Claude login state, in plaintext**) | |
| Caddy's certificates (so a restore does not run into Let's Encrypt's rate limits) | |

That is everything needed to restore on another host.

> [!WARNING]
> **Keep the key and the data apart, and back up both.**
>
> - The archive holds plaintext Claude state. Lock down who can read where you store it, and encrypt it
> - The archive also holds the client secrets of the sign-in methods tenants registered, sealed with this key. A key kept next to the data defeats the encryption
> - Lose `AF_MASTER_KEY` and every past archive becomes undecryptable

## Take a backup

Run `deploy/compose/backup.sh`. In production, run it from cron.

| Setting | Meaning |
|---|---|
| `OUT_DIR` | Where archives go. Point it at another volume or remote storage. The default is `deploy/compose/backups/` |
| `KEEP` | How many generations to keep. The default is 7; `0` keeps them all |
| `--no-stop` | Takes the snapshot without stopping the Control Plane. Use it only when the caller guarantees nothing is writing |

The runbook has an example cron entry. After each run, generations beyond `KEEP` are deleted automatically.

### Effect on members

- By default `backup.sh` **stops the Control Plane and Caddy for a few seconds** to take a consistent SQLite snapshot, then starts them again
- **Members' workspaces (`af-ws-*`) keep running.** They are not compose services. Sessions stay connected and work carries on
- For those few seconds, Console sign-in and API relaying do not answer; that is all

## Restore

Do this on a clean host, or on a host that lost its data.

1. Prepare `.env` and put back **the same `AF_MASTER_KEY` as the source**, from your vault
2. Run `deploy/compose/restore.sh <archive>`
3. Bring up the Control Plane and Caddy with `docker compose up -d`
4. Members (or an administrator) press **Start** on each workspace in the Console

Three things to watch:

| Watch | Why |
|---|---|
| `AF_MASTER_KEY` must be the same value | With a different key, or none, members' credentials cannot be decrypted. Check the vault restore first |
| Keep **the last directory name** of `DATA_DIR` the same | The parent path may differ (`/srv` → `/mnt`, say); the Control Plane re-roots it at start. The last name must match the archive's top-level directory, or `restore.sh` refuses |
| Right after a restore there are no workspace containers | They are not compose services, so the archive does not hold them. Start has the Control Plane recreate them with the restored database's ports and tokens, and the home's `secrets.enc` and `claude-config` bring back connections and the Claude login |

> [!TIP]
> Rehearse the restore once before you need it in production. That is usually when the `DATA_DIR` name rule bites.

## Other deployment types

| Type | Backup and restore |
|---|---|
| ECS (`ecs` / `ecs-ec2`) | [The ECS runbook, "EFS backup and restore"](../../../deploy/aws/ecs/README.md#efs-backup-and-restore) |
| Kubernetes | [Back up and restore on Kubernetes](../kubernetes/backup.md) |

## Related

- [Upgrade](upgrade.md): always take a backup before an upgrade
- [Handle the deployment keys](../secure/deployment-keys.md): what losing `AF_MASTER_KEY` means

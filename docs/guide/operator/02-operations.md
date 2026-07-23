# 02. Day-to-Day Operations

English | [日本語](02-operations.ja.md)

This chapter covers steady-state operations after installation — backup, restore, upgrades,
air-gapped networks, and stopping Workspaces — together with the decision points involved.
**The actual commands (`backup.sh` / `restore.sh` / upgrade / air-gapped procedures) are
canonically documented in [deploy/compose/README.md](../../../deploy/compose/README.md).**
Rather than duplicating the commands here, this chapter supplements them with "what happens
and what to watch out for." The working directory is `deploy/compose/`. To dig into the
design assumptions, see [dev/09 §9.7](../../dev/09-deploy.md).

## Backup

`deploy/compose/backup.sh` packs the entire `DATA_DIR` into a timestamped `tar.gz`. For the
command and its options (`OUT_DIR` / `KEEP` / `--no-stop`), see the "Backup & restore" section
of the runbook.

### What is included and what is not

What **goes into** the archive (= with this alone you can restore onto another host):

- `control-plane.db` — the graph of tenants / members / ports / tokens.
- Each user's home (working trees, dotfiles, the envelope-encrypted `secrets.enc`).
- Each user's `claude-config` (**plaintext Claude login state**).
- Caddy's certificates (to avoid Let's Encrypt rate limits at restore time).

What is **not included**:

- `shared/jvm` (the re-fetchable, huge Temurin JDKs) is deliberately excluded.
- **`AF_MASTER_KEY` is not included.** It lives in `.env` and is by design never put into
  the archive.

> These two points are the heart of operations. The backup archive is **sensitive data that
> contains plaintext Claude state**, so be strict about the permissions and encryption of
> wherever you store it. At the same time, possessing the archive alone is not enough: without
> `AF_MASTER_KEY`, the envelope-encrypted credentials cannot be decrypted. Conversely, if you
> lose `AF_MASTER_KEY`, every past archive becomes permanently undecryptable (crypto-shred —
> see [03](03-security.md)). **Keep the key and the data separate, but back up both** — that is
> the right answer.

### Impact on users

By default `backup.sh` **briefly stops the CP and Caddy** to take a consistent SQLite snapshot,
then restarts them immediately. During this, **user Workspaces (`af-ws-*`) do not stop, since
they are outside compose management** — sessions stay connected and work continues. During the
few seconds of downtime, Console logins and API relaying merely become temporarily unresponsive.
If the caller has already guaranteed quiescence, `--no-stop` lets you take the backup without
stopping anything.

### Running it from cron

In production, run `backup.sh` periodically from cron. Use `OUT_DIR` to point at your
organization's backup area (a separate volume / a remote location) and `KEEP` to control the
number of generations. An example cron entry is in the "Backup & restore" section of the
runbook. After each backup, pruning of old generations is performed automatically.

## Restore

This is the recovery procedure onto a clean host, or after data loss. The commands are in the
runbook's "Backup & restore".

The flow is: "prepare `.env` (restore **the same `AF_MASTER_KEY` as the backup source** from
your vault) → `restore.sh <archive>` → `docker compose up -d` → **Start** each Workspace from
the Console". Three key points.

1. **`AF_MASTER_KEY` must be identical to the original.** If it differs or is missing, the
   wrapped DEKs cannot be unwrapped and the credentials cannot be decrypted. Verify the
   restoration from the vault first.
2. **The `DATA_DIR` basename constraint.** The parent path of the restore destination may
   differ from the original (e.g. `/srv` → `/mnt`); at startup the CP re-points each
   Workspace's on-disk root to the current `DATA_DIR`. However, the top-level directory name
   inside the archive (= the basename of the original `DATA_DIR`) and the basename of the
   destination `DATA_DIR` **must match**. `restore.sh` validates this and refuses on a mismatch.
3. **Workspaces rehydrate via "Start workspace" in the Console.** Immediately after a restore,
   no Workspace containers exist (they are outside compose management). When a user (or an
   admin) presses Start in the Console, the CP re-creates `af-ws-*` using the ports/tokens in
   the restored DB, and the user's connections and Claude login come back from `secrets.enc`
   and `claude-config` in their home.

## Upgrade

Just change `VERSION` in `.env` to the new tag, pull (or build) the images, and run `up -d`.
The commands are in the runbook's "Upgrade" section.

- **Schema migrations are embedded in the CP and applied automatically at startup**
  (**forward-compatible**). No manual migration runs are needed.
- **Downgrades are not supported.** An older CP cannot understand migrations applied by a newer
  version. Therefore **always take a `backup.sh` before upgrading**. If something goes wrong,
  the correct rollback path is not "go back to the old image" but "restore from the backup."
- Check the release notes for breaking changes.

## Installing into an air-gapped network

You can install onto a host with no external network access. On a machine with internet
connectivity, `docker save` the images and carry them over; on the target host, run
`load-images.sh` and then `up -d`. The commands are in the runbook's "Air-gapped install"
section.

There are two decision points.

- **TLS**: Let's Encrypt is unusable in an air-gapped network, so either switch to
  `tls internal` (self-signed) per [01 §4](01-install.md), or use an internal CA.
- **Installing Claude**: the Workspace image by default fetches the latest Claude at container
  startup. On a fully offline host, set `CLAUDE_INSTALL=0` (via `WS_ENV`) and use an image with
  Claude baked in.

## Idle stop and force-stop

- **Automatic idle stop (scale-to-zero)**: setting `AF_SESSION_IDLE_TIMEOUT` /
  `AF_WS_IDLE_TIMEOUT` automatically stops Workspaces that have been unused for a given period
  (per-tenant overrides are in the Admin UI). Disabled by default. A stopped Workspace starts
  automatically the next time the user opens a terminal (`AF_AUTOSTART`). This is effective for
  saving resources. For the meaning of the env vars, see
  [.env.example](../../../deploy/compose/.env.example); for how it works, see
  [dev/09 §9.4](../../dev/09-deploy.md).
- **force-stop (brute force)**: `docker compose down` **does not stop user Workspaces** (they
  are outside compose management). To stop a specific Workspace for sure, a super_admin
  force-stops it from the Admin panel in the Console. When the whole host must be brought fully
  down for maintenance, after stopping the CP/Caddy you also need to `docker stop` the remaining
  `af-ws-*` containers separately (this also comes up in the troubleshooting chapter,
  [04](04-troubleshooting.md)).

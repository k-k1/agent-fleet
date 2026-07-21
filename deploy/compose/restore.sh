#!/usr/bin/env bash
# agent-fleet — restore (P3-10 stage 3)
#
# Extracts a tar.gz made by backup.sh into ${DATA_DIR}. Bring-up on a clean host:
#   1) prepare .env (**AF_MASTER_KEY must be identical to the backup source's**,
#      restored from your separate vault)
#   2) restore.sh <archive.tar.gz>
#   3) docker compose up -d
#   4) browser → Start (the CP recreates af-ws-* from the restored DB's port/token
#      state; secrets.enc / claude-config in home revive each user's connections
#      and claude login)
#
# ⚠️ If AF_MASTER_KEY differs from the original or is missing, wrapped DEKs cannot
#    be unwrapped and the credentials stay undecryptable.
# The restore parent path may differ from the source (e.g. /srv→/mnt). At start the
# CP re-roots each workspace's on-disk root onto the current WS_DATA(=DATA_DIR)
# (rootedDataDir), so as long as .env's DATA_DIR points at the restore target, homes
# mount correctly even on a different host/path. However, the archive's top-level
# directory name (= the source DATA_DIR's basename) must match the restore DATA_DIR's
# basename (checked below).
#
#   deploy/compose/restore.sh backups/agent-fleet-YYYYMMDD-HHMMSS.tar.gz
#   deploy/compose/restore.sh <archive>   # refuses if DATA_DIR is non-empty
#                                         # (--force overwrites)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

ARCHIVE="${1:?usage: restore.sh <archive.tar.gz> [--force]}"
FORCE=0
[ "${2:-}" = "--force" ] && FORCE=1
[ -f "$ARCHIVE" ] || { echo "ERROR: archive not found: $ARCHIVE" >&2; exit 1; }

if [ -f .env ]; then set -a; . ./.env; set +a; fi
DATA_DIR="${DATA_DIR:?DATA_DIR must be set (deploy/compose/.env)}"

# Verify the archive's top-level directory name (basename of DATA_DIR).
TOP="$(tar -tzf "$ARCHIVE" | head -1 | cut -d/ -f1)"
WANT="$(basename "$DATA_DIR")"
if [ "$TOP" != "$WANT" ]; then
  echo "ERROR: archive top-level dir '$TOP' != DATA_DIR basename '$WANT'." >&2
  echo "       DATA_DIR in .env must match the source deployment's path." >&2
  exit 1
fi

if [ -e "$DATA_DIR" ] && [ -n "$(ls -A "$DATA_DIR" 2>/dev/null)" ] && [ "$FORCE" = 0 ]; then
  echo "ERROR: $DATA_DIR is not empty. Refusing to overwrite (pass --force)." >&2
  exit 1
fi

# Stop the CP for consistency if it is running (no-op on a clean host).
docker compose stop cp caddy >/dev/null 2>&1 || true

echo "==> restoring $ARCHIVE -> $DATA_DIR"
mkdir -p "$(dirname "$DATA_DIR")"
# --numeric-owner: restore the stored uid(1000) as-is (keeps ownership when run as root).
tar --numeric-owner -xzf "$ARCHIVE" -C "$(dirname "$DATA_DIR")"

echo "==> restored. next:"
echo "   1) confirm AF_MASTER_KEY in .env matches the backup source (separate vault)"
echo "   2) docker compose up -d"
echo "   3) browser → Start to recreate af-ws-* (connections and claude login revive)"

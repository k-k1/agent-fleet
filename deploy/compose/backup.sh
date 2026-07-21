#!/usr/bin/env bash
# agent-fleet — backup (P3-10 stage 3)
#
# Packs the whole ${DATA_DIR} into a timestamped tar.gz:
#   - control-plane.db(+ -wal/-shm)   ... tenant/membership/port/token graph
#   - <key>/home/.config/secrets.enc  ... envelope-encrypted git/claude credentials
#   - <key>/home/...                  ... per-user working trees and dotfiles
#   - <key>/claude-config             ... claude login state (plaintext)
#   - caddy/                          ... ACME certs (no re-issue on restore = avoids
#                                         Let's Encrypt rate limits)
# shared/jvm (re-provisionable Temurin, huge) is excluded by default.
#
# ⚠️ AF_MASTER_KEY is NOT in this archive (it lives in .env). Keep it in a SEPARATE vault.
#    Losing it = no wrapped DEK can be unwrapped = crypto-shred (this backup too).
# ⚠️ The archive itself is sensitive (contains plaintext claude state). Guard the
#    storage location's permissions and encryption.
#
# By default CP+Caddy are stopped briefly to quiesce SQLite for a consistent snapshot,
# then restarted (workspace containers af-ws-* are not compose-managed and stay up,
# so users are not disconnected).
#
#   deploy/compose/backup.sh                 # to ./backups/, briefly stopping the CP
#   OUT_DIR=/mnt/bkp deploy/compose/backup.sh
#   deploy/compose/backup.sh --no-stop       # snapshot without stopping (caller quiesced)
#   KEEP=7 deploy/compose/backup.sh          # generations to retain (default 7, 0=unlimited)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

NO_STOP=0
[ "${1:-}" = "--no-stop" ] && NO_STOP=1

# Read DATA_DIR from .env (environment overrides win).
if [ -f .env ]; then set -a; . ./.env; set +a; fi
DATA_DIR="${DATA_DIR:?DATA_DIR must be set (deploy/compose/.env)}"
[ -d "$DATA_DIR" ] || { echo "ERROR: DATA_DIR not found: $DATA_DIR" >&2; exit 1; }
OUT_DIR="${OUT_DIR:-$HERE/backups}"
KEEP="${KEEP:-7}"
EXCLUDES=(--exclude=shared/jvm)   # re-provisionable and huge

TS="$(date +%Y%m%d-%H%M%S)"
ARCHIVE="$OUT_DIR/agent-fleet-$TS.tar.gz"
mkdir -p "$OUT_DIR"

stop_stack()  { docker compose stop cp caddy >/dev/null 2>&1 || true; }
start_stack() { docker compose start cp caddy >/dev/null 2>&1 || true; }

if [ "$NO_STOP" = 0 ]; then
  echo "==> quiescing CP+Caddy (workspaces stay up)"
  stop_stack
  trap start_stack EXIT   # always restart, even on mid-run failure
fi

echo "==> archiving $DATA_DIR -> $ARCHIVE"
# --numeric-owner: store uid 1000(dev) ownership numerically so a restore run as
# root keeps the owner.
tar --numeric-owner "${EXCLUDES[@]}" \
    -czf "$ARCHIVE" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"

if [ "$NO_STOP" = 0 ]; then start_stack; trap - EXIT; echo "==> CP+Caddy resumed"; fi

echo "==> done: $(du -h "$ARCHIVE" | cut -f1)  $ARCHIVE"

# Generation retention (keep the newest KEEP archives).
if [ "$KEEP" -gt 0 ]; then
  mapfile -t OLD < <(ls -1t "$OUT_DIR"/agent-fleet-*.tar.gz 2>/dev/null | tail -n +$((KEEP+1)))
  if [ "${#OLD[@]}" -gt 0 ]; then
    echo "==> pruning $(( ${#OLD[@]} )) old archive(s) beyond KEEP=$KEEP"
    rm -f "${OLD[@]}"
  fi
fi

echo "reminder: store AF_MASTER_KEY separately (losing it = crypto-shred)."

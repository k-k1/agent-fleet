#!/usr/bin/env bash
# agent-fleet — 復元（P3-10 段3）
#
# backup.sh が作った tar.gz を ${DATA_DIR} へ展開する。クリーン環境での立ち上げ手順:
#   1) .env を用意（**AF_MASTER_KEY はバックアップ元と同一値**＝別金庫から復元）
#   2) restore.sh <archive.tar.gz>
#   3) docker compose up -d
#   4) ブラウザ → Start（CP が復元済み DB の port/token で af-ws-* を再作成、
#      home の secrets.enc / claude-config でユーザーの接続・claude ログインが復活）
#
# ⚠️ AF_MASTER_KEY が元と違う/欠落なら wrapped DEK を unwrap できず資格は復号不能。
# 復元先の親パスはバックアップ元と違ってよい（例 /srv→/mnt）。CP は起動時に各
# workspace の on-disk root を現在の WS_DATA(=DATA_DIR) へ re-root する（rootedDataDir）
# ため、.env の DATA_DIR を復元先に合わせておけば別ホスト/別パスでも home を正しく
# マウントする。ただしアーカイブ先頭ディレクトリ名（= 元 DATA_DIR の basename）と復元先
# DATA_DIR の basename は一致させること（下でチェック）。
#
#   deploy/compose/restore.sh backups/agent-fleet-YYYYMMDD-HHMMSS.tar.gz
#   deploy/compose/restore.sh <archive>   # DATA_DIR が空でなければ拒否（--force で上書き）
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

ARCHIVE="${1:?usage: restore.sh <archive.tar.gz> [--force]}"
FORCE=0
[ "${2:-}" = "--force" ] && FORCE=1
[ -f "$ARCHIVE" ] || { echo "ERROR: archive not found: $ARCHIVE" >&2; exit 1; }

if [ -f .env ]; then set -a; . ./.env; set +a; fi
DATA_DIR="${DATA_DIR:?DATA_DIR must be set (deploy/compose/.env)}"

# アーカイブ内の top-level ディレクトリ名（basename of DATA_DIR）を検証。
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

# CP が動いていれば整合のため止める（クリーン環境なら no-op）。
docker compose stop cp caddy >/dev/null 2>&1 || true

echo "==> restoring $ARCHIVE -> $DATA_DIR"
mkdir -p "$(dirname "$DATA_DIR")"
# --numeric-owner: 保存された uid(1000) をそのまま復元（root 実行時に owner を維持）。
tar --numeric-owner -xzf "$ARCHIVE" -C "$(dirname "$DATA_DIR")"

echo "==> restored. next:"
echo "   1) .env の AF_MASTER_KEY がバックアップ元と同一であることを確認（別金庫）"
echo "   2) docker compose up -d"
echo "   3) ブラウザ → Start で af-ws-* を再作成（接続・claude ログイン復活）"

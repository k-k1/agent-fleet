#!/usr/bin/env bash
# agent-fleet — バックアップ（P3-10 段3）
#
# ${DATA_DIR} を丸ごと timestamped tar.gz に固める:
#   - control-plane.db(+ -wal/-shm)   … tenant/membership/port/token グラフ
#   - <key>/home/.config/secrets.enc  … 封筒暗号された git/claude 資格
#   - <key>/home/…                    … per-user 作業ツリー・dotfiles
#   - <key>/claude-config             … claude ログイン状態（平文）
#   - caddy/                          … ACME 証明書（復元時の再取得＝Let's Encrypt レート回避）
# 既定で shared/jvm（再取得可能な Temurin、巨大）は除外。
#
# ⚠️ AF_MASTER_KEY はこのアーカイブに入らない（.env にあり）。**別金庫**で保管すること。
#    紛失 = 全 wrapped DEK が unwrap 不能 = crypto-shred（このバックアップも復号不能）。
# ⚠️ アーカイブ自体が機微（claude 平文状態を含む）。保管先の権限・暗号化に注意。
#
# 既定は CP+Caddy を一瞬停止して SQLite を静止させ整合スナップショットを取り、その後再開する
# （workspace コンテナ af-ws-* は compose 管理外ゆえ止めない＝ユーザーは切断されない）。
#
#   deploy/compose/backup.sh                 # ./backups/ へ、CP を一瞬停止して取得
#   OUT_DIR=/mnt/bkp deploy/compose/backup.sh
#   deploy/compose/backup.sh --no-stop       # 停止せず取得（呼び出し側で静止済みの前提）
#   KEEP=7 deploy/compose/backup.sh          # 世代保持数（既定 7、0=無制限）
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

NO_STOP=0
[ "${1:-}" = "--no-stop" ] && NO_STOP=1

# .env から DATA_DIR を読む（環境で上書き可）。
if [ -f .env ]; then set -a; . ./.env; set +a; fi
DATA_DIR="${DATA_DIR:?DATA_DIR must be set (deploy/compose/.env)}"
[ -d "$DATA_DIR" ] || { echo "ERROR: DATA_DIR not found: $DATA_DIR" >&2; exit 1; }
OUT_DIR="${OUT_DIR:-$HERE/backups}"
KEEP="${KEEP:-7}"
EXCLUDES=(--exclude=shared/jvm)   # 再取得可能・巨大

TS="$(date +%Y%m%d-%H%M%S)"
ARCHIVE="$OUT_DIR/agent-fleet-$TS.tar.gz"
mkdir -p "$OUT_DIR"

stop_stack()  { docker compose stop cp caddy >/dev/null 2>&1 || true; }
start_stack() { docker compose start cp caddy >/dev/null 2>&1 || true; }

if [ "$NO_STOP" = 0 ]; then
  echo "==> quiescing CP+Caddy (workspaces stay up)"
  stop_stack
  trap start_stack EXIT   # 途中失敗でも必ず再開
fi

echo "==> archiving $DATA_DIR -> $ARCHIVE"
# --numeric-owner: uid 1000(dev) 所有を数値で保存し、root で復元しても owner が保たれる。
tar --numeric-owner "${EXCLUDES[@]}" \
    -czf "$ARCHIVE" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"

if [ "$NO_STOP" = 0 ]; then start_stack; trap - EXIT; echo "==> CP+Caddy resumed"; fi

echo "==> done: $(du -h "$ARCHIVE" | cut -f1)  $ARCHIVE"

# 世代保持（新しい順に KEEP 個を残す）。
if [ "$KEEP" -gt 0 ]; then
  mapfile -t OLD < <(ls -1t "$OUT_DIR"/agent-fleet-*.tar.gz 2>/dev/null | tail -n +$((KEEP+1)))
  if [ "${#OLD[@]}" -gt 0 ]; then
    echo "==> pruning $(( ${#OLD[@]} )) old archive(s) beyond KEEP=$KEEP"
    rm -f "${OLD[@]}"
  fi
fi

echo "reminder: store AF_MASTER_KEY separately (losing it = crypto-shred)."

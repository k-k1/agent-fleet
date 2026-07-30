#!/usr/bin/env bash
# build_console — build the Vite/Rollup console bundle to console/dist, escaping the
# fleet's per-agent cgroup memory cap when we're boxed inside one.
#
# なぜ必要か: tmux-claude.sh は各フリートエージェント（claude セッション）を
# `systemd-run --scope -p MemoryMax=2G` で包んで起動する（host-OOM 対策＝暴走した 1
# エージェント／ビルドがホストとフリート全体を道連れに落とさないため）。cgroup v2 の
# メモリ上限は子孫プロセスすべてに継承されるので、その capped なエージェントの中から
# 走らせた console ビルドも 2G に縛られる。console バンドルが育つと Vite/Rollup の
# ピーク RSS が 2G を超え、ホストに RAM が余っていても OOM/スラッシングする（症状＝
# Vite が "transforming…" のまま数分〜十数分固まり、最後は "Killed"）。コンテナは無関係。
#
# 対策: 「小さい有限の cgroup 上限」かつ「per-user systemd マネージャが使える」ことを
# 検出したときだけ、app.slice 直下の *兄弟* 一時 scope に逃がして潤沢な上限
# （AF_BUILD_MEM_MAX、既定 12G）を与える。上限が無い/大きいホストや systemd --user が
# 無い環境（CI 等）では従来どおりインプロセスでビルドする＝挙動不変。
#
# 前提: 呼び出し側で $ROOT（リポジトリルート）が定義済み。$NVM_DIR / $AF_BUILD_MEM_MAX
# を尊重する。set -e 下で失敗時はビルド失敗を伝播する。
build_console() {
  local heap=3072 esc=0 cg cgmax
  cg="$(awk -F: '/^0:/{print $3}' /proc/self/cgroup 2>/dev/null)"
  cgmax="$(cat "/sys/fs/cgroup${cg}/memory.max" 2>/dev/null || echo max)"
  # 小さい有限上限(<6GiB)に閉じ込められ、かつユーザ systemd で scope を作れるときだけ逃がす。
  if [ "$cgmax" != max ] && [ "${cgmax:-0}" -lt 6442450944 ] 2>/dev/null \
     && command -v systemd-run >/dev/null 2>&1 \
     && systemd-run --user --scope --quiet -- true >/dev/null 2>&1; then
    esc=1
    heap=8192
  fi

  if [ "$esc" = 1 ]; then
    local mem="${AF_BUILD_MEM_MAX:-12G}"
    echo "==> build console (vite) — $((cgmax / 1024 / 1024 / 1024))G cgroup 上限を回避し transient scope MemoryMax=$mem で実行"
    local inner
    inner="export NVM_DIR='${NVM_DIR:-$HOME/.nvm}'; [ -s \"\$NVM_DIR/nvm.sh\" ] && . \"\$NVM_DIR/nvm.sh\" >/dev/null 2>&1; cd '$ROOT/console' && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS='--max-old-space-size=$heap' npm run build"
    systemd-run --user --scope -p MemoryMax="$mem" --quiet -- bash -c "$inner"
  else
    echo "==> build console (vite)"
    ( cd "$ROOT/console" && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS="--max-old-space-size=$heap" npm run build )
  fi
}

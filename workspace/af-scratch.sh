#!/usr/bin/env bash
# af-scratch — ビルド生成物をタスクローカルの速いディスクへ逃がす（ADR 0044 決定 3）。
#
#   af-scratch node_modules       # ./node_modules を /scratch へ移して symlink を張る
#   af-scratch target .venv       # 複数まとめて
#   af-scratch --status           # いま何が逃がされているか
#
# なぜ必要か（実測 docs/63 §63.4）: ECS では `~` が EFS（NFS）に載っており、1 ファイル
# あたり約 14.5ms の固定ペナルティがある。`node_modules` のような数万ファイルの木は
# これで 9 倍以上遅くなり、並列度を上げても vCPU を増やしても改善しない。逃がすと
# `npm ci` が 105 秒から 11 秒側になる（Console 相当・外挿）。
#
# 代わりに払うもの: **Workspace を停止すると中身は消える**。だから対象は「再生成できる
# 生成物」だけにすること——追跡ファイルや未コミットの変更を逃がしてはいけない。
# パッケージのキャッシュ（~/.npm）は EFS に残っているので、作り直しにネットワークは要らない。
set -euo pipefail

SCRATCH="${AF_WS_SCRATCH:-}"
if [ -z "$SCRATCH" ]; then
  echo "af-scratch: この Workspace には作業ディスクがありません（AF_WS_SCRATCH 未設定）。" >&2
  echo "  ローカルディスクにホームが載っている構成では、逃がす意味がないので何もしません。" >&2
  exit 1
fi

# 逃がし先はワークツリー毎に分ける。同名の node_modules が複数プロジェクトにあっても
# 衝突しないよう、絶対パスをそのまま階層に写す。
dest_for() { printf '%s/artifacts%s\n' "$SCRATCH" "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }

if [ "${1:-}" = "--status" ]; then
  base="$SCRATCH/artifacts"
  [ -d "$base" ] || { echo "逃がしているものはありません。"; exit 0; }
  find "$base" -mindepth 1 -maxdepth 6 -type d -exec test -e '{}' ';' -print 2>/dev/null |
    while read -r d; do
      orig="${d#"$base"}"
      if [ -L "$orig" ]; then printf '%-60s %s\n' "$orig" "$(du -sh "$d" 2>/dev/null | cut -f1)"; fi
    done
  exit 0
fi

[ $# -gt 0 ] || { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }

for target in "$@"; do
  if [ -L "$target" ]; then
    echo "af-scratch: $target は既に symlink です（-> $(readlink "$target")）。何もしません。"
    continue
  fi
  dest="$(dest_for "$target")"
  mkdir -p "$(dirname "$dest")"
  if [ -e "$target" ]; then
    # EFS から読んで書き戻すので、ここは 1 回だけ遅い。次回以降は最初から scratch 側。
    echo "af-scratch: $target を作業ディスクへ移しています…"
    rm -rf "$dest"
    mv "$target" "$dest"
  else
    mkdir -p "$dest"
  fi
  ln -s "$dest" "$target"
  echo "af-scratch: $target -> $dest（Workspace 停止で消えます）"
done

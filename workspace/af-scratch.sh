#!/usr/bin/env bash
# af-scratch — ビルド生成物をタスクローカルの速いディスクへ逃がす（ADR 0044 決定 3）。
#
#   af-scratch node_modules       # ./node_modules を /scratch へ移して symlink を張る
#   af-scratch target .venv       # 複数まとめて
#   af-scratch --status           # いま何が逃がされているか
#   af-scratch --auto <dir>       # dir 配下のプロジェクトを見て先回りで逃がす（Agent が clone 直後に叩く）
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

MODE="${1:-}"
SCRATCH="${AF_WS_SCRATCH:-}"
if [ -z "$SCRATCH" ]; then
  # --auto は Agent が clone / worktree 作成のたびに best-effort で叩く。作業ディスクが
  # 無い構成（docker / native、または退避が無効なデプロイ）では黙って何もしないこと——
  # ここで失敗を返すと、逃がす動機が無いだけの正常な環境でログが埋まる。
  [ "$MODE" = "--auto" ] && exit 0
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

# --- --auto: 作業コピーを見て「これから作られる生成物」を先回りで逃がす -------------
#
# 手で `af-scratch node_modules` を張る形だと、実際には **1 回目の npm ci が EFS 上で走って
# しまってから**逃がすことになり、効き幅（105s → 11s）が取れないうえ、数万ファイルを
# EFS から読み直して移す羽目になる。だから「まだ無いうちに symlink だけ張っておく」。
#
# 安全側の規則:
#   - 既に symlink → 触らない（利用者が親クローンへ張った共有かもしれない）
#   - 実体があり、**git が無視していない** → 触らない（追跡物を動かすことは絶対にしない）
#   - 実体があり、git が無視している → 移して symlink に置き換える
#   - 実体が無い → 空の逃がし先を作って symlink を張る（この経路が本命）
#
# 副作用として `[ -d node_modules ] || npm install` の形をしたスクリプトは
# 「もう入っている」と誤認する（空ディレクトリでも -d は真）。AF_WS_SCRATCH_AUTO=0 で切れる。
auto_relocate() {
  target="$1"
  if [ -L "$target" ]; then return 0; fi
  if [ -e "$target" ]; then
    if ! git -C "$(dirname "$target")" check-ignore -q "$target" 2>/dev/null; then return 0; fi
  fi
  dest="$(dest_for "$target")"
  mkdir -p "$(dirname "$dest")" 2>/dev/null || return 0
  if [ -e "$target" ]; then
    rm -rf "$dest"
    mv "$target" "$dest" 2>/dev/null || return 0
  else
    mkdir -p "$dest" 2>/dev/null || return 0
  fi
  ln -s "$dest" "$target" 2>/dev/null && echo "af-scratch: $target -> $dest（Workspace 停止で消えます）"
}

if [ "$MODE" = "--auto" ]; then
  [ "${AF_WS_SCRATCH_AUTO:-1}" = "0" ] && exit 0
  root="${2:-$PWD}"
  [ -d "$root" ] || exit 0
  root="$(cd "$root" && pwd)"
  depth="${AF_WS_SCRATCH_AUTO_DEPTH:-3}"
  # マーカーの在り処＝生成物の在り処。モノレポのために深さを見る（既定 3 階層）。
  # 生成物ディレクトリ自身と .git には降りない（node_modules の中の package.json を拾わない）。
  find "$root" -maxdepth "$depth" \
    \( -name .git -o -name node_modules -o -name .venv -o -name target -o -name build -o -name dist \) -prune -o \
    -type f \( -name package.json -o -name Cargo.toml -o -name pyproject.toml -o -name pom.xml \
               -o -name build.gradle -o -name build.gradle.kts \) -print 2>/dev/null |
    while read -r marker; do
      d="$(dirname "$marker")"
      case "$(basename "$marker")" in
        package.json)               arts="node_modules" ;;
        Cargo.toml|pom.xml)         arts="target" ;;
        pyproject.toml)             arts=".venv" ;;
        build.gradle|build.gradle.kts) arts="build" ;;
        *)                          arts="" ;;
      esac
      for a in $arts; do auto_relocate "$d/$a"; done
    done || true
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

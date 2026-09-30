#!/usr/bin/env bash
# af-scratch — move build output onto the task-local fast disk (ADR 0044 decision 3).
#
#   af-scratch target             # move ./target to /scratch and leave a symlink
#   af-scratch target build       # several at once
#   af-scratch --status           # what is relocated now
#   af-scratch --auto <dir>       # relocate ahead of time for the projects under dir (the Agent runs this on a new clone)
#
# Why (measured, docs/log/63 §63.4): on ECS `~` is EFS (NFS), with a fixed ~14.5ms per
# file. A tree of tens of thousands of files is 9x+ slower there, and neither more
# parallelism nor more vCPUs helps.
#
# The price: **the contents are gone when the Workspace stops**, so only regenerable
# output belongs here — never tracked files or uncommitted work.
#
# node_modules cannot be relocated this way: npm (arborist `_createSparseTree`) replaces
# any symlink on the path from a package to the project root with a real directory, so
# the first `npm ci` / `npm install` puts the tree back on EFS — and `npm ci` empties the
# scratch side first. Measured with npm 10.9.9, even on a no-op `npm install`.
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

# --- --auto: look at the working copy and relocate the output it is about to produce ---
#
# Linking while the directory does not exist yet means the first build already writes
# to /scratch; relocating afterwards re-reads the whole tree off EFS to move it.
#
# Only target/ and build/ are pre-created. node_modules is not (see the header), and
# neither is .venv: `python3 -m venv .venv` refuses a symlink ("Unable to create
# directory", Python 3.13), so a pre-created link breaks the stock command.
#
# 安全側の規則:
#   - 既に symlink → 触らない（利用者が親クローンへ張った共有かもしれない）
#   - 実体があり、**git が無視していない** → 触らない（追跡物を動かすことは絶対にしない）
#   - 実体があり、git が無視している → 移して symlink に置き換える
#   - 実体が無い → 空の逃がし先を作って symlink を張る（この経路が本命）
#
# Side effect: `[ -d build ] || …` style scripts see the empty link as done.
# AF_WS_SCRATCH_AUTO=0 turns the step off.
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
    -type f \( -name Cargo.toml -o -name pom.xml \
               -o -name build.gradle -o -name build.gradle.kts \) -print 2>/dev/null |
    while read -r marker; do
      d="$(dirname "$marker")"
      case "$(basename "$marker")" in
        Cargo.toml|pom.xml)         arts="target" ;;
        build.gradle|build.gradle.kts) arts="build" ;;
        *)                          arts="" ;;
      esac
      for a in $arts; do auto_relocate "$d/$a"; done
    done || true
  exit 0
fi

[ $# -gt 0 ] || { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }

status=0
for target in "$@"; do
  case "$(basename "$target")" in
    node_modules)
      echo "af-scratch: node_modules は逃がせません。npm がリンクを実体ディレクトリに置き換えて EFS へ戻し、npm ci は逃がし先を空にします。" >&2
      status=1
      continue ;;
    .venv)
      echo "af-scratch: 注意: .venv へのリンクは uv venv / uv sync なら使えますが、python3 -m venv .venv は失敗します。" >&2 ;;
  esac
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
exit "$status"

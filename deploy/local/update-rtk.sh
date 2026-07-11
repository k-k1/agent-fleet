#!/usr/bin/env bash
# rtk 自動更新: rtk-ai/rtk の GitHub 最新リリースをホストへ入れ、vendor にも反映する。
# 冪等（既に最新なら何もしない）・チェックサム検証つき。ネットワークと gh 認証が要る。
#
# 使い方:
#   deploy/local/update-rtk.sh            # 最新へ更新（既に最新なら no-op）
#   deploy/local/update-rtk.sh --check    # 版の確認だけ（更新しない）
# 環境変数:
#   WS_RTK_BIN  更新先バイナリ（既定 $HOME/.local/bin/rtk）
#   RTK_REPO    リリース元（既定 rtk-ai/rtk）
set -euo pipefail

REPO="${RTK_REPO:-rtk-ai/rtk}"
DEST="${WS_RTK_BIN:-$HOME/.local/bin/rtk}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VENDOR="$ROOT/workspace/vendor/rtk"

semver() { grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }

case "$(uname -m)" in
  x86_64)  ASSET="rtk-x86_64-unknown-linux-musl.tar.gz" ;;
  aarch64) ASSET="rtk-aarch64-unknown-linux-gnu.tar.gz" ;;
  *) echo "未対応の arch: $(uname -m)" >&2; exit 2 ;;
esac

latest="$(gh api "repos/$REPO/releases/latest" --jq .tag_name | semver)"
current="$([ -x "$DEST" ] && "$DEST" --version 2>/dev/null | semver || echo "")"
echo "rtk: current=${current:-none} latest=${latest} (asset=$ASSET)"

if [ "${1:-}" = "--check" ]; then exit 0; fi
if [ -n "$current" ] && [ "$current" = "$latest" ]; then
  echo "既に最新。何もしない。"; exit 0
fi

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
gh release download "v$latest" --repo "$REPO" --pattern "$ASSET" --pattern checksums.txt --dir "$tmp" --clobber
( cd "$tmp" && grep "$ASSET" checksums.txt | sha256sum -c - )
tar xzf "$tmp/$ASSET" -C "$tmp"
[ -x "$tmp/rtk" ] || { echo "展開物に rtk バイナリが無い" >&2; exit 1; }

install -D -m 0755 "$tmp/rtk" "$DEST"
echo "==> installed $("$DEST" --version) -> $DEST"
if [ -d "$ROOT/workspace/vendor" ]; then
  install -m 0755 "$DEST" "$VENDOR"
  echo "==> vendored -> $VENDOR"
fi
echo "反映するには Workspace イメージを再ビルド（deploy/local/run-dev.sh か docker build + e2e-smoke.sh）。"

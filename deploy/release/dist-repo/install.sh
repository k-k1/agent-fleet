#!/usr/bin/env bash
# Agent Fleet — native パッケージ導入ワンライナー（docs/35 §35.4.2）。
#
#   curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
#
# 最新（または AF_VERSION 指定版）の native tar を取得し、同リリースの SHA256SUMS で
# 検証して ~/.local/opt/agent-fleet/<v>/ へ展開、~/.local/bin/af に symlink する。
# 更新も同じコマンド（版ディレクトリ切替 — データ WS_DATA には触らない）。
#
# env:
#   AF_VERSION        導入する版（省略時は最新リリース）
#   AF_PREFIX         導入先 prefix（既定 ~/.local）
#   AF_DIST_REPO      配布 repo（既定 k-k1/agent-fleet-dist）
#   AF_DIST_URL_BASE  取得元 URL 基底の差し替え（検証・ミラー用。指定時は AF_VERSION 必須）
set -euo pipefail

REPO="${AF_DIST_REPO:-k-k1/agent-fleet-dist}"
DEFAULT_BASE="https://github.com/$REPO/releases/download"
BASE="${AF_DIST_URL_BASE:-$DEFAULT_BASE}"
PREFIX="${AF_PREFIX:-$HOME/.local}"
VER="${AF_VERSION:-}"
ARCH=amd64

die() { echo "ERROR: $*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "Linux 専用です（WSL2 を含む）"
[ "$(uname -m)" = x86_64 ] || die "現在 linux-amd64 のみ配布しています（$(uname -m) は未対応）"

fetch() { # fetch <url> <out>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "curl も wget もありません"
  fi
}

# 最新版の解決: releases/latest のリダイレクト先 tag（API レート制限に依らない）。
if [ -z "$VER" ]; then
  [ "$BASE" = "$DEFAULT_BASE" ] || die "AF_DIST_URL_BASE 指定時は AF_VERSION も指定してください"
  if command -v curl >/dev/null 2>&1; then
    loc="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
    tag="${loc##*/}"
  else
    tag="$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  fi
  case "$tag" in
    v[0-9]*) VER="${tag#v}" ;;
    *) die "最新リリースを解決できません（tag=$tag）。AF_VERSION=<v> で明示してください" ;;
  esac
fi

NAME="agent-fleet-native-$VER-linux-$ARCH"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> agent-fleet $VER を取得: $BASE/v$VER/$NAME.tar.gz"
fetch "$BASE/v$VER/$NAME.tar.gz" "$TMP/$NAME.tar.gz"
fetch "$BASE/v$VER/SHA256SUMS" "$TMP/SHA256SUMS"

echo "==> 検証（sha256）"
grep -E "  $NAME\.tar\.gz\$" "$TMP/SHA256SUMS" > "$TMP/want.sum" \
  || die "SHA256SUMS に $NAME.tar.gz の行がありません"
(cd "$TMP" && sha256sum -c want.sum >/dev/null) \
  || die "sha256 が一致しません（ダウンロード破損の可能性 — やり直してください）"

echo "==> 展開 -> $PREFIX/opt/agent-fleet/$VER"
mkdir -p "$TMP/x"
tar xzf "$TMP/$NAME.tar.gz" -C "$TMP/x"
[ -x "$TMP/x/$NAME/af" ] || die "tar の内容が想定と異なります（af がありません）"
DEST_ROOT="$PREFIX/opt/agent-fleet"
STAGING="$DEST_ROOT/.staging-$VER.$$"
mkdir -p "$DEST_ROOT"
rm -rf "$STAGING"
mv "$TMP/x/$NAME" "$STAGING"
rm -rf "${DEST_ROOT:?}/${VER:?}"
mv "$STAGING" "$DEST_ROOT/$VER"

mkdir -p "$PREFIX/bin"
ln -sfn "$DEST_ROOT/$VER/af" "$PREFIX/bin/af"

echo "==> 導入完了: $PREFIX/bin/af -> $DEST_ROOT/$VER/af"
case ":$PATH:" in
  *":$PREFIX/bin:"*) ;;
  *) echo "    注意: $PREFIX/bin が PATH にありません（ログインし直すか PATH に追加してください）" ;;
esac
cat <<EOF
次の一手:
  af start        # 初回のみ rootfs（200MB台）を取得・検証して展開
  # ブラウザで http://localhost:8099
旧版の掃除（任意）: $DEST_ROOT/ の不要な版ディレクトリを削除（データは別置きで無傷）
EOF

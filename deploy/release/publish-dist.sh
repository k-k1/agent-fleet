#!/usr/bin/env bash
# Agent Fleet — dist repo publish（docs/35 §35.7.4-1 / §35.4.2）。
#
#   VERSION=0.1.0 deploy/release/publish-dist.sh [--dist-dir <d>] [--repo <o/r>] [--seed] [--dry-run]
#
# deploy/release/dist/ の成果物を公開 dist repo の GitHub Releases へ publish する:
#   - rootfs リリース `rootfs-<r>` … R を添付。既存 tag なら何もしない（<r> は内容
#     ハッシュ = 同一物。イメージ不変リリースで利用者の再 DL を発生させない）。
#   - app リリース `v<v>`         … A / B / C（+ -bundle）/ SHA256SUMS を添付。
#     既存 tag は fail（リリースは不変 — やり直しは版を上げる）。
# --seed は dist repo 本体（README.md / install.sh）を deploy/release/dist-repo/ から
# contents API で push（内容一致ならスキップ = 冪等）。repo が無ければ作る。
# 認証は gh（ローカル = gh auth login / CI = GH_TOKEN に DIST_PUBLISH_TOKEN）。
# 実 publish の runbook は docs/35 §35.8.2。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.1.0)}"
REPO="k-k1/agent-fleet-dist"
DIST="$HERE/dist"
SEED=0
DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dist-dir) DIST="${2:?--dist-dir needs a path}"; shift ;;
    --repo)     REPO="${2:?--repo needs owner/repo}"; shift ;;
    --seed)     SEED=1 ;;
    --dry-run)  DRY=1 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

die() { echo "ERROR: $*" >&2; exit 1; }
# 変更系の gh 呼び出しだけ通す（--dry-run では表示のみ）。読み取り系は素で呼ぶ。
run() {
  if [ "$DRY" = 1 ]; then echo "DRY-RUN: $*" >&2; else "$@"; fi
}

# GitHub Releases の 1 アセット上限（2GiB）。超過分は添付できない — B の air-gap は
# ファイル渡し経路（§35.2）が正なので、警告してスキップする。
GH_MAX_ASSET=2147483648

ARCH=amd64
C_NAME="agent-fleet-native-$VERSION-linux-$ARCH"
C_TAR="$DIST/$C_NAME.tar.gz"
[ -f "$C_TAR" ] || die "$C_TAR がありません（VERSION=$VERSION build.sh --native が先）"
[ -f "$DIST/SHA256SUMS" ] || die "$DIST/SHA256SUMS がありません（build.sh が生成する）"

# ---- C 内の rootfs.json を読む（af ランチャと同じ sed パーサ）---------------------
MANIFEST="$(tar xzf "$C_TAR" -O "$C_NAME/rootfs.json")"
mget() { sed -n 's/.*"'"$1"'": *"\{0,1\}\([^",}]*\)"\{0,1\}.*/\1/p' <<<"$MANIFEST" | head -1; }
R_VER="$(mget version)"
R_SHA="$(mget sha256)"
R_URL="$(mget url)"
if [ -z "$R_VER" ] || [ -z "$R_SHA" ]; then die "C 内の rootfs.json が読めません"; fi
R_NAME="agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst"

# C が指す rootfs URL は必ずこの repo の Releases でなければならない。ここが食い違う
# C を publish すると、利用者の `af start` が存在しない/別の URL を叩く。
WANT_URL="https://github.com/$REPO/releases/download/rootfs-$R_VER/$R_NAME"
[ "$R_URL" = "$WANT_URL" ] || die "C の rootfs URL が publish 先と不一致です。
  rootfs.json: $R_URL
  期待値:      $WANT_URL
  （ROOTFS_URL_BASE を変えてビルドした C は、この repo へは publish できません）"

# ---- --seed: dist repo 本体（README.md / install.sh）--------------------------------
if [ "$SEED" = 1 ]; then
  echo "==> [publish] seed dist repo contents ($REPO)"
  if ! gh repo view "$REPO" --json name >/dev/null 2>&1; then
    run gh repo create "$REPO" --public \
      --description "Agent Fleet — distribution artifacts (no source here)"
  fi
  for f in README.md install.sh; do
    local_b64="$(base64 -w0 < "$HERE/dist-repo/$f")"
    resp="$(gh api "repos/$REPO/contents/$f" \
      --jq '.sha + " " + (.content | gsub("\n"; ""))' 2>/dev/null)" || resp=""
    sha="${resp%% *}"
    cur="${resp#* }"
    if [ -n "$resp" ] && [ "$cur" = "$local_b64" ]; then
      echo "    $f: 変更なし（スキップ）"
      continue
    fi
    put=(-X PUT "repos/$REPO/contents/$f" -f "message=seed: $f" -f "content=$local_b64")
    if [ -n "$sha" ]; then put+=(-f "sha=$sha"); fi
    run gh api "${put[@]}" > /dev/null
    echo "    $f: push 済み"
  done
fi

# ---- rootfs リリース ----------------------------------------------------------------
if gh release view "rootfs-$R_VER" -R "$REPO" >/dev/null 2>&1; then
  echo "==> [publish] rootfs-$R_VER は既存 — 再利用（アップロードなし）"
else
  R_TAR="$DIST/$R_NAME"
  [ -f "$R_TAR" ] || die "rootfs-$R_VER の tag が無く、R も手元にありません: $R_TAR
  （--rootfs-json での再利用ビルドは、publish 済みの <r> に対してのみ可能です）"
  echo "==> [publish] rootfs 検証（sha256）"
  echo "$R_SHA  $R_TAR" | sha256sum -c - >/dev/null \
    || die "R の sha256 が C 内 rootfs.json と一致しません: $R_TAR"
  echo "==> [publish] release rootfs-$R_VER を作成"
  run gh release create "rootfs-$R_VER" -R "$REPO" \
    --title "rootfs $R_VER (linux-$ARCH)" \
    --notes "workspace rootfs（内容ハッシュ $R_VER）。app リリースの native tar が参照する。単体では使わない。" \
    "$R_TAR"
fi

# ---- app リリース -------------------------------------------------------------------
if gh release view "v$VERSION" -R "$REPO" >/dev/null 2>&1; then
  die "v$VERSION は既に存在します（リリースは不変 — 版を上げてやり直してください）"
fi
assets=()
for f in "agent-fleet-$VERSION.tar.gz" "agent-fleet-images-$VERSION.tar.gz" \
         "$C_NAME.tar.gz" "$C_NAME-bundle.tar.gz"; do
  p="$DIST/$f"
  [ -f "$p" ] || continue
  size="$(stat -c%s "$p")"
  if [ "$size" -ge "$GH_MAX_ASSET" ]; then
    echo "WARN: $f は ${size} bytes で GitHub Releases の 2GiB 上限超 — 添付をスキップ" >&2
    echo "      （air-gap はファイル渡し経路で配布する — docs/35 §35.2）" >&2
    continue
  fi
  assets+=("$p")
done
assets+=("$DIST/SHA256SUMS")
echo "==> [publish] release v$VERSION を作成（${#assets[@]} assets）"
run gh release create "v$VERSION" -R "$REPO" \
  --title "agent-fleet $VERSION" \
  --notes "Agent Fleet $VERSION。native は install.sh（README 参照）、compose は agent-fleet-$VERSION.tar.gz。rootfs は rootfs-$R_VER。" \
  "${assets[@]}"

cat <<EOF
==> [publish] done
  releases: https://github.com/$REPO/releases/tag/v$VERSION
            https://github.com/$REPO/releases/tag/rootfs-$R_VER
  導入:     curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash
EOF

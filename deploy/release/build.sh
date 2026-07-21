#!/usr/bin/env bash
# Agent Fleet — release オーケストレータ（単一入口、docs/35 §35.6.2）。
#
#   VERSION=0.2.0 deploy/release/build.sh [--compose] [--native] [--save] [--all]
#
#   --compose … compose 系成果物（A: bundle tar / B: air-gap images tar / D: SHA256SUMS）。
#               実装は deploy/compose/release.sh への委譲。P1 ゲート（A+B+D）を
#               満たすため既定で B（docker save）も作る。イメージは配布 variant
#               （workspace: BAKE_AGENT_CLIS=0 lean / CP: docs distignore 適用）。
#   --native  … C（native tar）+ R（lean rootfs）— docs/35 §35.7.2-7。
#   --bundle-rootfs     … --native で R 同梱の self-contained 版（-bundle tar）も生成。
#   --rootfs-json <path> … 既存の rootfs.json を使い R の生成をスキップ
#                          （イメージ不変リリース: 利用者の再 DL なし）。
#   --save    … --compose の B 生成を明示（既定 ON。互換のため受けるだけ）。
#   --all     … --compose + --native。
#
# env: ROOTFS_URL_BASE … rootfs.json に焼く R の配布 URL の基底
#      （既定 https://github.com/k-k1/agent-fleet-dist/releases/download — §35.4.2）。
#
# 出力: deploy/release/dist/（各成果物 + SHA256SUMS）。
# リリース実ビルドは hosted CI（release-gate.yml）か十分なメモリのあるホストで。
# ビルドは 1 つずつ直列に（共有ホストのメモリ制約 — docs/35 §35.4）。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.2.0)}"
DIST="$HERE/dist"

usage() { echo "usage: VERSION=<v> $0 [--compose] [--native] [--all] [--bundle-rootfs] [--rootfs-json <path>]" >&2; }

DO_COMPOSE=0; DO_NATIVE=0; BUNDLE_ROOTFS=0; ROOTFS_JSON=""
while [ $# -gt 0 ]; do
  case "$1" in
    --compose) DO_COMPOSE=1 ;;
    --native)  DO_NATIVE=1 ;;
    --all)     DO_COMPOSE=1; DO_NATIVE=1 ;;
    --bundle-rootfs) BUNDLE_ROOTFS=1 ;;
    --rootfs-json) ROOTFS_JSON="${2:?--rootfs-json needs a path}"; shift ;;
    --save)    : ;; # B は --compose の既定に含む（互換受け）
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ "$DO_COMPOSE" = 0 ] && [ "$DO_NATIVE" = 0 ]; then
  usage
  exit 2
fi

mkdir -p "$DIST"

if [ "$DO_COMPOSE" = 1 ]; then
  echo "==> [build.sh] compose artifacts (A+B) -> $DIST"
  DIST_DIR="$DIST" VERSION="$VERSION" REGISTRY="${REGISTRY:-agent-fleet}" \
    bash "$ROOT/deploy/compose/release.sh" --save
fi

if [ "$DO_NATIVE" = 1 ]; then
  # C（native tar）+ R（lean rootfs）— docs/35 §35.7.2-7。amd64 先行（§35.3.1）。
  ARCH=amd64
  PKG_NAME="agent-fleet-native-$VERSION-linux-$ARCH"
  NATIVE_DIR="$HERE/native"
  WORK="$DIST/.native-work"
  OUT="$DIST/$PKG_NAME"
  echo "==> [build.sh] native artifacts (C+R) -> $DIST"
  rm -rf "$WORK" "$OUT" "$DIST/$PKG_NAME.tar.gz" "$DIST/$PKG_NAME-bundle.tar.gz"
  mkdir -p "$WORK" "$OUT/bin"

  # (i) af-cp 静的ビルド（golang コンテナ・ldflags VERSION — CP Dockerfile と同レシピ）。
  echo "==> [native] build af-cp (static)"
  docker build -f "$NATIVE_DIR/Dockerfile.afcp" --build-arg "VERSION=$VERSION" \
    --output "type=local,dest=$WORK/afcp" "$ROOT"
  install -m 0755 "$WORK/afcp/af-cp" "$OUT/bin/af-cp"

  # (ii) console dist（node コンテナ）。
  echo "==> [native] build console dist"
  docker build -f "$NATIVE_DIR/Dockerfile.console" \
    --output "type=local,dest=$WORK/console" "$ROOT"
  cp -R "$WORK/console/console" "$OUT/console"

  # (iii) docs ステージング（internal denylist 適用 — compose/release.sh と同じ規則）。
  echo "==> [native] stage docs (distignore applied)"
  mkdir -p "$OUT/docs"
  EXCLUDES=()
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
    if [ -n "$line" ]; then EXCLUDES+=(--exclude="./$line"); fi
  done < "$ROOT/docs/.distignore"
  tar -C "$ROOT/docs" -cf - "${EXCLUDES[@]}" . | tar -C "$OUT/docs" -xf -

  # (iv) 静的 bwrap / git+git-http-backend / zstd（alpine builder・版は Dockerfile.tools の ARG ピン）。
  echo "==> [native] build static tools (bwrap/git/zstd)"
  docker build -f "$NATIVE_DIR/Dockerfile.tools" \
    --output "type=local,dest=$WORK/tools" "$NATIVE_DIR"
  for b in bwrap git git-http-backend zstd; do
    install -m 0755 "$WORK/tools/$b" "$OUT/bin/$b"
  done

  # (v)(vi) lean rootfs（R）と rootfs.json。--rootfs-json 指定時は既存 manifest を
  # 再利用して R の生成を丸ごとスキップ（<r> 不変リリース）。
  R_TAR=""
  if [ -n "$ROOTFS_JSON" ]; then
    echo "==> [native] reuse rootfs manifest: $ROOTFS_JSON"
    cp "$ROOTFS_JSON" "$OUT/rootfs.json"
  else
    WS_NATIVE_IMAGE="agent-fleet/workspace:native-$VERSION"
    echo "==> [native] build lean rootfs image ($WS_NATIVE_IMAGE)"
    docker build -t "$WS_NATIVE_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "BAKE_AGENT_CLIS=0" \
      --build-arg "BAKE_OPTIONAL_TOOLS=0" \
      "$ROOT/workspace"
    echo "==> [native] export rootfs"
    cid="$(docker create "$WS_NATIVE_IMAGE")"
    docker export "$cid" > "$WORK/rootfs.tar"
    docker rm "$cid" >/dev/null
    # docker の ENV はイメージ config にあり filesystem export に乗らない。CP の
    # rootfs モードが env を再構成できるよう manifest を tar へ注入する（§35.7.2-2）。
    docker image inspect --format '{{json .Config.Env}}' "$WS_NATIVE_IMAGE" \
      > "$WORK/image-env.json"
    mkdir -p "$WORK/inject/usr/local/share/agent-fleet"
    cp "$WORK/image-env.json" "$WORK/inject/usr/local/share/agent-fleet/image-env.json"
    tar --append -f "$WORK/rootfs.tar" -C "$WORK/inject" usr/local/share/agent-fleet/image-env.json
    # <r> は内容ハッシュ由来（イメージ不変なら再利用でき、利用者の再 DL が要らない）。
    R_VER="$(sha256sum "$WORK/rootfs.tar" | cut -c1-12)"
    R_TAR="$DIST/agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst"
    echo "==> [native] compress rootfs -> $R_TAR"
    "$OUT/bin/zstd" -T0 -15 -f -q "$WORK/rootfs.tar" -o "$R_TAR"
    R_SHA="$(sha256sum "$R_TAR" | awk '{print $1}')"
    R_SIZE="$(stat -c%s "$R_TAR")"
    URL_BASE="${ROOTFS_URL_BASE:-https://github.com/k-k1/agent-fleet-dist/releases/download}"
    # 整形は af ランチャの sed パーサと対（1 キー 1 行を崩さないこと）。
    cat > "$OUT/rootfs.json" <<EOF
{
  "version": "$R_VER",
  "url": "$URL_BASE/rootfs-$R_VER/agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst",
  "sha256": "$R_SHA",
  "size": $R_SIZE
}
EOF
  fi

  # (vii) C の組立。
  echo "==> [native] assemble $PKG_NAME"
  install -m 0755 "$ROOT/deploy/native/af" "$OUT/af"
  cp "$ROOT/deploy/native/README.md" "$ROOT/LICENSE" "$ROOT/NOTICE" "$OUT/"
  printf '%s\n' "$VERSION" > "$OUT/VERSION"
  tar -czf "$DIST/$PKG_NAME.tar.gz" -C "$DIST" "$PKG_NAME"
  if [ "$BUNDLE_ROOTFS" = 1 ]; then
    [ -n "$R_TAR" ] || { echo "ERROR: --bundle-rootfs は --rootfs-json と併用できません（R が手元に無い）" >&2; exit 2; }
    mkdir -p "$OUT/rootfs"
    cp "$R_TAR" "$OUT/rootfs/"
    tar -czf "$DIST/$PKG_NAME-bundle.tar.gz" -C "$DIST" "$PKG_NAME"
    rm -rf "$OUT/rootfs"
  fi
  rm -rf "$WORK"
  echo "==> [native] done: $DIST/$PKG_NAME.tar.gz${R_TAR:+  +  $R_TAR}"
fi

# D: dist 直下の全成果物を対象に SHA256SUMS を作り直す（release.sh が書いた分を
# 上書き — native 成果物が増えても常に全量をカバーする）。
echo "==> [build.sh] SHA256SUMS"
(
  cd "$DIST"
  rm -f SHA256SUMS
  find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\n' | LC_ALL=C sort \
    | xargs -r sha256sum > SHA256SUMS
  cat SHA256SUMS
)
echo "==> [build.sh] done: $DIST"

#!/usr/bin/env bash
# Agent Fleet — release オーケストレータ（単一入口、docs/35 §35.6.2）。
#
#   VERSION=0.2.0 deploy/release/build.sh [--compose] [--native] [--save] [--all]
#
#   --compose … compose 系成果物（A: bundle tar / B: air-gap images tar / D: SHA256SUMS）。
#               実装は deploy/compose/release.sh への委譲。P1 ゲート（A+B+D）を
#               満たすため既定で B（docker save）も作る。イメージは配布 variant
#               （workspace: BAKE_AGENT_CLIS=0 lean / CP: docs distignore 適用）。
#   --native  … C（native tar）+ R（lean rootfs）。P2 で実装（今はエラー）。
#   --save    … --compose の B 生成を明示（既定 ON。互換のため受けるだけ）。
#   --all     … --compose + --native。
#
# 出力: deploy/release/dist/（各成果物 + SHA256SUMS）。
# CI（GitHub Actions）は支払い停止のためローカル実行が正。ビルドは 1 つずつ直列に
# （共有ホストのメモリ制約 — docs/35 §35.4）。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.2.0)}"
DIST="$HERE/dist"

DO_COMPOSE=0; DO_NATIVE=0
for a in "$@"; do
  case "$a" in
    --compose) DO_COMPOSE=1 ;;
    --native)  DO_NATIVE=1 ;;
    --all)     DO_COMPOSE=1; DO_NATIVE=1 ;;
    --save)    : ;; # B は --compose の既定に含む（互換受け）
    *) echo "unknown arg: $a" >&2; echo "usage: VERSION=<v> $0 [--compose] [--native] [--all]" >&2; exit 2 ;;
  esac
done
if [ "$DO_COMPOSE" = 0 ] && [ "$DO_NATIVE" = 0 ]; then
  echo "usage: VERSION=<v> $0 [--compose] [--native] [--all]" >&2
  exit 2
fi

mkdir -p "$DIST"

if [ "$DO_COMPOSE" = 1 ]; then
  echo "==> [build.sh] compose artifacts (A+B) -> $DIST"
  DIST_DIR="$DIST" VERSION="$VERSION" REGISTRY="${REGISTRY:-agent-fleet}" \
    bash "$ROOT/deploy/compose/release.sh" --save
fi

if [ "$DO_NATIVE" = 1 ]; then
  echo "ERROR: --native (C: native tar + R: lean rootfs) is not implemented yet (P2 — docs/35 §35.7)" >&2
  exit 3
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

#!/usr/bin/env bash
# Phase 1 ローカル dev 起動: Workspace イメージをビルドし、Control Plane をホストで起動する。
# 検証済みの最小ワークフロー（docs/11-phase1-plan.md）。
#
# 前提: docker（docker グループ有効、無ければ `sg docker -c "deploy/local/run-dev.sh"`）と
#       Go（host）。ブラウザで http://localhost:8099 を開く。
#
# 例:
#   deploy/local/run-dev.sh                      # claude 同梱イメージで起動
#   INSTALL_CLAUDE=0 WS_SESSION_CMD=bash \        # claude 非同梱の軽量検証
#     deploy/local/run-dev.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

WS_IMAGE="${WS_IMAGE:-agent-fleet/workspace:dev}"
INSTALL_CLAUDE="${INSTALL_CLAUDE:-1}"
CP_ADDR="${CP_ADDR:-:8099}"
WS_DATA="${WS_DATA:-/tmp/af-data}"

echo "==> build workspace image ($WS_IMAGE, INSTALL_CLAUDE=$INSTALL_CLAUDE)"
docker build -t "$WS_IMAGE" --build-arg INSTALL_CLAUDE="$INSTALL_CLAUDE" "$ROOT/workspace"

echo "==> build control-plane"
( cd "$ROOT/control-plane" && go build -o /tmp/af-cp . )

echo "==> control-plane on $CP_ADDR  (console: http://localhost${CP_ADDR})"
exec env \
  CP_ADDR="$CP_ADDR" \
  WS_IMAGE="$WS_IMAGE" \
  CONSOLE_DIR="$ROOT/console" \
  WS_DATA="$WS_DATA" \
  ${WS_SESSION_CMD:+WS_SESSION_CMD="$WS_SESSION_CMD"} \
  /tmp/af-cp

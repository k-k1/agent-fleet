#!/usr/bin/env bash
# Phase 1 ローカル dev 起動: Workspace イメージをビルドし、Control Plane をホストで起動する。
# 検証済みの最小ワークフロー（docs/11-phase1-plan.md）。
#
# 前提: docker（docker グループ有効、無ければ `sg docker -c "deploy/local/run-dev.sh"`）と
#       Go（host）。ブラウザで http://localhost:8099 を開く。
#
# claude CLI はイメージに焼き込まず、コンテナ起動時(entrypoint)に最新を取得する。
#
# 例:
#   deploy/local/run-dev.sh                          # 起動時に最新 claude を install
#   WS_ENV=CLAUDE_INSTALL=0 WS_SESSION_CMD=bash \      # claude 抜きの軽量検証
#     deploy/local/run-dev.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Go is user-local on this host; ensure `go` is found even when invoked from a
# non-login shell (e.g. `sg docker -c "deploy/local/run-dev.sh"`).
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"

WS_IMAGE="${WS_IMAGE:-agent-fleet/workspace:dev}"
CP_ADDR="${CP_ADDR:-:8099}"
WS_DATA="${WS_DATA:-/tmp/af-data}"

# git-provider OAuth config (contains a secret -> git-ignored). If present, export
# GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY / BITBUCKET_OAUTH_SECRET / PUBLIC_BASE_URL.
# See deploy/local/oauth.env.example.
OAUTH_ENV="$ROOT/deploy/local/oauth.env"
if [ -f "$OAUTH_ENV" ]; then
  set -a; . "$OAUTH_ENV"; set +a
fi

echo "==> build workspace image ($WS_IMAGE)"
docker build -t "$WS_IMAGE" "$ROOT/workspace"

echo "==> build control-plane"
( cd "$ROOT/control-plane" && go build -o /tmp/af-cp . )

echo "==> control-plane on $CP_ADDR  (console: http://localhost${CP_ADDR})"
exec env \
  CP_ADDR="$CP_ADDR" \
  WS_IMAGE="$WS_IMAGE" \
  CONSOLE_DIR="$ROOT/console" \
  WS_DATA="$WS_DATA" \
  ${AUTH:+AUTH="$AUTH"} \
  ${DEV_USER:+DEV_USER="$DEV_USER"} \
  ${AUTH_EMAIL_HEADER:+AUTH_EMAIL_HEADER="$AUTH_EMAIL_HEADER"} \
  ${WS_SESSION_CMD:+WS_SESSION_CMD="$WS_SESSION_CMD"} \
  ${WS_ENV:+WS_ENV="$WS_ENV"} \
  ${GITHUB_OAUTH_CLIENT_ID:+GITHUB_OAUTH_CLIENT_ID="$GITHUB_OAUTH_CLIENT_ID"} \
  ${BITBUCKET_OAUTH_KEY:+BITBUCKET_OAUTH_KEY="$BITBUCKET_OAUTH_KEY"} \
  ${BITBUCKET_OAUTH_SECRET:+BITBUCKET_OAUTH_SECRET="$BITBUCKET_OAUTH_SECRET"} \
  ${PUBLIC_BASE_URL:+PUBLIC_BASE_URL="$PUBLIC_BASE_URL"} \
  ${AF_MASTER_KEY:+AF_MASTER_KEY="$AF_MASTER_KEY"} \
  /tmp/af-cp

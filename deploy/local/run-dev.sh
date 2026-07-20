#!/usr/bin/env bash
# Phase 1 ローカル dev 起動: Workspace イメージをビルドし、Control Plane をホストで起動する。
# 検証済みの最小ワークフロー（docs/11-phase1-plan.md）。
#
# 前提: docker（docker グループ有効、無ければ `sg docker -c "deploy/local/run-dev.sh"`）と
#       Go（host）。ブラウザで http://localhost:8099 を開く。
#
# claude / opencode / codex はイメージに焼き込み（Dockerfile の ARG でピン止め）。
# 版の bump 手順（runbook）は docs/dev/10-development.md §10.2.1。
#
# 例:
#   deploy/local/run-dev.sh
#   WS_ENV=CLAUDE_INSTALL=0 WS_SESSION_CMD=bash \      # claude 抜きの軽量検証
#     deploy/local/run-dev.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Go is user-local on this host; ensure `go` is found even when invoked from a
# non-login shell (e.g. `sg docker -c "deploy/local/run-dev.sh"`).
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"

WS_IMAGE="${WS_IMAGE:-agent-fleet/workspace:dev}"
CP_ADDR="${CP_ADDR:-:8099}"
# Persistent data root (DB + per-user homes + shared JDKs). NOT under /tmp: that's
# tmpfs (RAM) here, so it would be wiped on reboot and permanently occupy RAM.
WS_DATA="${WS_DATA:-$HOME/.local/share/agent-fleet}"
# Per-workspace RAM cap (docker --memory; applied at container start). Raise with
# care — this host is RAM-constrained and over-committing can OOM the fleet.
WS_MEMORY="${WS_MEMORY:-5g}"
# Shared Temurin JDKs live here on the host (provisioned once below) and are
# bind-mounted read-only into every workspace at /usr/lib/jvm.
WS_JVM_DIR="${WS_JVM_DIR:-$WS_DATA/shared/jvm}"
# The host-run Control Plane is not built from control-plane/Dockerfile, so it
# does not have the baked docs tree. Point staging at this checkout by default.
# A caller may override this for a different docs source.
AF_DOCS_DIR="${AF_DOCS_DIR:-$ROOT/docs}"

# Runtime profile: "local" (docker, 既定) | "native"/"wsl" (Docker なし・単一ユーザー、
# docs/34)。native はイメージビルド等の docker 工程を丸ごとスキップし、workspace-agent を
# ホストビルドしてプロセス起動する。
AF_RUNTIME="${AF_RUNTIME:-local}"
case "$AF_RUNTIME" in native|wsl) NATIVE=1 ;; *) NATIVE=0 ;; esac

# git-provider OAuth config (contains a secret -> git-ignored). If present, export
# GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY / BITBUCKET_OAUTH_SECRET / PUBLIC_BASE_URL.
# See deploy/local/oauth.env.example.
OAUTH_ENV="$ROOT/deploy/local/oauth.env"
if [ -f "$OAUTH_ENV" ]; then
  set -a; . "$OAUTH_ENV"; set +a
fi

# rtk を最新へ自動更新（rtk-ai/rtk のリリース取得。既に最新なら no-op）。ネットワーク /
# gh 認証が要るので失敗はソフト（オフライン時は現状の rtk のまま続行）。WS_RTK_UPDATE=0 で無効。
if [ "$NATIVE" = "0" ] && [ "${WS_RTK_UPDATE:-1}" = "1" ]; then
  bash "$ROOT/deploy/local/update-rtk.sh" || echo "WARN: rtk 自動更新に失敗（現状の rtk のまま続行）"
fi

if [ "$NATIVE" = "0" ]; then
  # Vendor a host rtk binary into the build context so the image installs it
  # (token-saving claude hook). Static binary → runs in the slim container. Other
  # deployments leave vendor/ empty. Override the source with WS_RTK_BIN.
  RTK_SRC="${WS_RTK_BIN:-$HOME/.local/bin/rtk}"
  if [ -x "$RTK_SRC" ]; then
    install -m 0755 "$RTK_SRC" "$ROOT/workspace/vendor/rtk" && echo "==> vendored rtk from $RTK_SRC"
  else
    rm -f "$ROOT/workspace/vendor/rtk" 2>/dev/null || true
  fi

  # Provision the shared JDKs into WS_JVM_DIR (idempotent; first run is slow).
  bash "$ROOT/deploy/local/provision-jvm.sh" "$WS_JVM_DIR" || echo "WARN: jvm provision failed (java unavailable)"

  echo "==> build workspace image ($WS_IMAGE)"
  docker build -t "$WS_IMAGE" "$ROOT/workspace"

  # イメージスモーク（焼き込み CLI の版 = Dockerfile の ARG ピン等を検証、数秒）。
  # WS_SMOKE=0 でスキップ。
  if [ "${WS_SMOKE:-1}" = "1" ]; then
    bash "$ROOT/deploy/local/e2e-smoke.sh" "$WS_IMAGE"
  fi
else
  # native: no image — build the workspace-agent for this host instead, and check
  # the host provides what the Dockerfile normally would (warn-only; docs/34).
  echo "==> build workspace-agent (native runtime)"
  ( cd "$ROOT/workspace/agent" && go build -o /tmp/af-agent . )
  AF_NATIVE_AGENT_BIN=/tmp/af-agent
  for c in tmux git claude; do
    command -v "$c" >/dev/null 2>&1 || echo "WARN: '$c' not found on host PATH (native workspaces need it)"
  done
fi

# Console is now a Vite + React app: build it to console/dist, which the CP serves
# statically (no-store). Node is user-local via nvm and not on a non-login shell's
# PATH (e.g. under `sg docker`), so source nvm first. Run `npm --prefix console run
# dev` (vite build --watch) in a separate shell during active UI work for fast
# rebuilds; this script does a one-shot production build.
echo "==> build console (vite)"
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
# shellcheck disable=SC1091
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
if ! command -v npm >/dev/null 2>&1; then
  echo "ERROR: npm not found (need Node via nvm). Skipping console build." >&2
  exit 1
fi
# mermaid is large; raise the Node heap so the production build doesn't OOM on
# this RAM-constrained host.
( cd "$ROOT/console" && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS="--max-old-space-size=3072" npm run build )

echo "==> build control-plane"
( cd "$ROOT/control-plane" && go build -o /tmp/af-cp . )

echo "==> control-plane on $CP_ADDR  (console: http://${CP_ADDR/#:/localhost:})"
exec env \
  CP_ADDR="$CP_ADDR" \
  AF_RUNTIME="$AF_RUNTIME" \
  ${AF_NATIVE_AGENT_BIN:+AF_NATIVE_AGENT_BIN="$AF_NATIVE_AGENT_BIN"} \
  WS_IMAGE="$WS_IMAGE" \
  CONSOLE_DIR="$ROOT/console/dist" \
  WS_DATA="$WS_DATA" \
  WS_MEMORY="$WS_MEMORY" \
  ${WS_JVM_DIR:+WS_JVM_DIR="$WS_JVM_DIR"} \
  ${AF_DOCS_DIR:+AF_DOCS_DIR="$AF_DOCS_DIR"} \
  ${AUTH:+AUTH="$AUTH"} \
  ${DEV_USER:+DEV_USER="$DEV_USER"} \
  ${AUTH_EMAIL_HEADER:+AUTH_EMAIL_HEADER="$AUTH_EMAIL_HEADER"} \
  ${WS_SESSION_CMD:+WS_SESSION_CMD="$WS_SESSION_CMD"} \
  ${WS_ENV:+WS_ENV="$WS_ENV"} \
  ${GITHUB_OAUTH_CLIENT_ID:+GITHUB_OAUTH_CLIENT_ID="$GITHUB_OAUTH_CLIENT_ID"} \
  ${BITBUCKET_OAUTH_KEY:+BITBUCKET_OAUTH_KEY="$BITBUCKET_OAUTH_KEY"} \
  ${BITBUCKET_OAUTH_SECRET:+BITBUCKET_OAUTH_SECRET="$BITBUCKET_OAUTH_SECRET"} \
  ${PUBLIC_BASE_URL:+PUBLIC_BASE_URL="$PUBLIC_BASE_URL"} \
  ${GOOGLE_OAUTH_CLIENT_ID:+GOOGLE_OAUTH_CLIENT_ID="$GOOGLE_OAUTH_CLIENT_ID"} \
  ${GOOGLE_OAUTH_CLIENT_SECRET:+GOOGLE_OAUTH_CLIENT_SECRET="$GOOGLE_OAUTH_CLIENT_SECRET"} \
  ${AF_COOKIE_SECRET:+AF_COOKIE_SECRET="$AF_COOKIE_SECRET"} \
  ${AF_SESSION_TTL:+AF_SESSION_TTL="$AF_SESSION_TTL"} \
  ${AF_OAUTH_ALLOWED_EMAILS:+AF_OAUTH_ALLOWED_EMAILS="$AF_OAUTH_ALLOWED_EMAILS"} \
  ${AF_OAUTH_ALLOWED_EMAILS_FILE:+AF_OAUTH_ALLOWED_EMAILS_FILE="$AF_OAUTH_ALLOWED_EMAILS_FILE"} \
  ${AF_MASTER_KEY:+AF_MASTER_KEY="$AF_MASTER_KEY"} \
  ${AF_DB:+AF_DB="$AF_DB"} \
  ${AF_PROVISION:+AF_PROVISION="$AF_PROVISION"} \
  ${SUPER_ADMIN_EMAILS:+SUPER_ADMIN_EMAILS="$SUPER_ADMIN_EMAILS"} \
  ${AF_MCP_ENABLED:+AF_MCP_ENABLED="$AF_MCP_ENABLED"} \
  /tmp/af-cp

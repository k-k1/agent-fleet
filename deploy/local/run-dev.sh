#!/usr/bin/env bash
# Single entry point for local startup (merges the old run-dev.sh and wsl-quickstart.sh).
# Pick how Workspaces run via a subcommand; builds the Console and Control Plane and
# starts the CP on the host. Open http://localhost:8099 in a browser.
#
# Usage:
#   deploy/local/run-dev.sh [local]   # Docker runtime (dev default)
#   deploy/local/run-dev.sh wsl       # WSL personal-use preset (Docker required;
#                                     # docker/cgroup preflight, AUTH=dev forced)
#   deploy/local/run-dev.sh native    # containerless, no Docker (single user; docs/34)
#   deploy/local/run-dev.sh reset [--all] [--yes]
#                                     # wipe local data. Default: only the dev user's
#                                     # workspace ($WS_DATA/<DEV_USER>).
#                                     # --all: all of $WS_DATA incl. DB and shared JDKs.
#
# Notes:
#   - The old deploy/local/wsl-quickstart.sh is a backward-compat wrapper for
#     `run-dev.sh wsl`.
#   - With no subcommand, env AF_RUNTIME decides (native|wsl -> containerless).
#     Note: env AF_RUNTIME=wsl is, to the CP, an alias for "containerless" and is NOT
#     the `wsl` subcommand (Docker preset). Easy to mix up — prefer the subcommand.
#   - claude / opencode / codex / agy / rtk are baked into the image (pinned via
#     Dockerfile ARGs). Version bump runbook: docs/dev/10-development.md §10.2.1.
#     Tracking latest is also possible via the settings modal's self-update opt-in
#     (AF_AGENT_SELF_UPDATE), rtk included.
#
# Examples:
#   deploy/local/run-dev.sh
#   WS_ENV=CLAUDE_INSTALL=0 WS_SESSION_CMD=bash deploy/local/run-dev.sh   # light check without claude
#   WS_JDK=0 WS_SMOKE=0 deploy/local/run-dev.sh wsl
#   deploy/local/run-dev.sh reset --all --yes
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Go / Node are usually user-local installs; extend PATH so they are visible even
# from non-login shells (sg docker etc.).
export PATH="$HOME/.local/go/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"

CP_ADDR="${CP_ADDR:-:8099}"
# Persistent data root (DB + per-user homes + shared JDKs). NOT under /tmp: that's
# tmpfs (RAM) here, so it would be wiped on reboot and permanently occupy RAM.
WS_DATA="${WS_DATA:-$HOME/.local/share/agent-fleet}"
# Per-workspace RAM cap (docker --memory; not applied on native). Raise with care.
WS_MEMORY="${WS_MEMORY:-5g}"
# Shared Temurin JDKs live here on the host and are bind-mounted read-only into
# every workspace at /usr/lib/jvm (docker runtime only).
WS_JVM_DIR="${WS_JVM_DIR:-$WS_DATA/shared/jvm}"
# The host-run Control Plane is not built from control-plane/Dockerfile, so it
# does not have the baked docs tree. Point staging at this checkout by default.
AF_DOCS_DIR="${AF_DOCS_DIR:-$ROOT/docs}"
WS_JDK="${WS_JDK:-1}"                  # 1=provision shared JDKs / 0=skip (rely on on-demand install-jdk)
RTK_VERSION="${RTK_VERSION:-}"         # override the baked rtk version (empty = Dockerfile's ARG pin)
DEV_KEY="${DEV_USER:-dev}"

# Print the leading comment block (after the shebang, up to the first non-comment
# line) as help.
usage() { awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0"; }

# ---- reset: wipe local data --------------------------------------------------
# Whether docker or native ran last, clean up leftovers (container/network, agent
# process, dedicated tmux socket) before deleting data. Default: dev user only
# (DB and shared JDKs kept = no re-provision needed). --all deletes all of $WS_DATA.
do_reset() {
  local all=0 yes=0 a
  for a in "$@"; do
    case "$a" in
      --all) all=1 ;;
      --yes | -y) yes=1 ;;
      *)
        echo "unknown reset option: $a" >&2
        usage
        exit 1
        ;;
    esac
  done
  local target="$WS_DATA/$DEV_KEY"
  [ "$all" = 1 ] && target="$WS_DATA"
  if [ ! -e "$target" ]; then
    echo "nothing to delete (already clean): $target"
    exit 0
  fi
  # Don't delete while the CP still holds the data (it would write the DB/home
  # back and leave a half-wiped state).
  if pgrep -f '^/tmp/af-cp' >/dev/null 2>&1; then
    echo "ERROR: control-plane (/tmp/af-cp) is running. Stop it with Ctrl-C, then retry." >&2
    exit 1
  fi
  echo "==> deleting: $target"
  if [ "$all" = 1 ]; then
    echo "    full wipe: DB, all user homes, claude-config, shared JDKs (JDKs re-provision next run)"
  else
    echo "    user $DEV_KEY only: home / claude-config (incl. Claude login). DB and shared JDKs kept"
  fi
  if [ "$yes" != 1 ]; then
    if [ -t 0 ]; then
      read -r -p "Really delete? [y/N] " ans
      case "$ans" in y | Y | yes) ;; *)
        echo "aborted"
        exit 1
        ;;
      esac
    else
      echo "ERROR: pass --yes for non-interactive runs" >&2
      exit 1
    fi
  fi
  # docker-runtime leftovers (no-op where docker is absent).
  docker rm -f "af-ws-$DEV_KEY" >/dev/null 2>&1 || true
  docker network rm "af-net-$DEV_KEY" >/dev/null 2>&1 || true
  # native-runtime leftovers: stop the agent process group, then clean its tmux socket.
  local pidf="$WS_DATA/$DEV_KEY/agent.pid" pid=""
  if pid="$(cat "$pidf" 2>/dev/null)" && [ -n "$pid" ]; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    sleep 1
    kill -KILL -- "-$pid" 2>/dev/null || true
  fi
  tmux -L "af-ws-$DEV_KEY" kill-server 2>/dev/null || true
  # Go module caches inside workspace homes are write-protected (0555/0444 —
  # `go mod` does this on purpose), which makes a plain rm -rf fail with
  # "Permission denied". Restore owner write permission first.
  chmod -R u+w "$target" 2>/dev/null || true
  rm -rf "$target"
  echo "==> wipe complete (recreated on next start)"
}

# ---- mode selection ----------------------------------------------------------
MODE=""
case "${1:-}" in
  local | docker) MODE=local ;;
  wsl | quickstart) MODE=wsl ;;
  native) MODE=native ;;
  reset)
    shift
    do_reset "$@"
    exit 0
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  "") ;;
  *)
    echo "unknown subcommand: $1" >&2
    usage
    exit 1
    ;;
esac
if [ -z "$MODE" ]; then
  # No subcommand: fall back to env AF_RUNTIME (native|wsl are containerless to the CP).
  case "${AF_RUNTIME:-local}" in native | wsl) MODE=native ;; *) MODE=local ;; esac
fi
# Settle the runtime passed to the CP (subcommand wins). The wsl preset uses the
# docker runtime.
case "$MODE" in native) AF_RUNTIME=native ;; *) AF_RUNTIME=local ;; esac

WS_IMAGE_DEFAULT="agent-fleet/workspace:dev"
[ "$MODE" = wsl ] && WS_IMAGE_DEFAULT="agent-fleet/workspace:wsl"
WS_IMAGE="${WS_IMAGE:-$WS_IMAGE_DEFAULT}"

# git-provider OAuth config (contains a secret -> git-ignored). If present, export
# GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY / BITBUCKET_OAUTH_SECRET / PUBLIC_BASE_URL.
# See deploy/local/oauth.env.example.
OAUTH_ENV="$ROOT/deploy/local/oauth.env"
if [ -f "$OAUTH_ENV" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$OAUTH_ENV"
  set +a
  gh_state="unset"
  [ -n "${GITHUB_OAUTH_CLIENT_ID:-}" ] && gh_state="set"
  echo "==> loaded $OAUTH_ENV (GitHub device flow client_id: $gh_state)"
fi
# The wsl preset is single-user only: AUTH=oauth in oauth.env is not honored.
[ "$MODE" = wsl ] && AUTH=dev

# ---- preflight ---------------------------------------------------------------
fail=0
if ! command -v go >/dev/null 2>&1; then
  echo "✗ go not found. Install Go and add it to PATH (https://go.dev/dl/)" >&2
  fail=1
fi
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
# shellcheck disable=SC1091
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
if ! command -v npm >/dev/null 2>&1; then
  echo "✗ npm/node not found. Install Node (nvm recommended)" >&2
  fail=1
fi
if [ "$MODE" = wsl ]; then
  # Can we reach the Docker daemon? (native dockerd inside WSL recommended over
  # Docker Desktop).
  if ! docker info >/dev/null 2>&1; then
    echo "✗ cannot reach the docker daemon. Start dockerd inside WSL and add ${USER:-$(id -un)} to the docker group" >&2
    echo "   (e.g. sudo service docker start / sudo usermod -aG docker ${USER:-$(id -un)}, then log in again)" >&2
    echo "   If Docker is not an option: deploy/local/run-dev.sh native (docs/34)" >&2
    fail=1
  fi
  # cgroup v2 (the --memory cap and resource display depend on it).
  cgt="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
  [ "$cgt" = "cgroup2fs" ] || echo "! cgroup is not v2 ($cgt). Memory caps and resource display may not behave as expected" >&2
fi
[ "$fail" = 0 ] || {
  echo "preflight failed. Fix the issues above and retry." >&2
  exit 1
}

# ---- prepare the Workspace runtime (per mode) --------------------------------
# rtk is always baked into the image (Dockerfile BAKE_RTK=1 default, ARG-pinned).
# The old host vendoring (update-rtk.sh -> vendor/rtk) is gone.
if [ "$MODE" != native ]; then
  # Provision the shared JDKs into WS_JVM_DIR (idempotent; first run is slow).
  # With WS_JDK=0, skip and rely on on-demand `workspace-agent install-jdk` in-container.
  if [ "$WS_JDK" = "1" ]; then
    bash "$ROOT/deploy/local/provision-jvm.sh" "$WS_JVM_DIR" || echo "WARN: jvm provision failed (java unavailable)"
  else
    WS_JVM_DIR=""
    echo "==> WS_JDK=0: skipping shared JDK provision (use workspace-agent install-jdk <major> when needed)"
  fi

  # Dockerfile ARG default is now lean-CLI (BAKE_AGENT_CLIS=0): the dev image ships
  # without the agent CLIs and boot-install fetches the pinned versions on first
  # workspace start. Set BAKE_AGENT_CLIS=1 to bake them in for a faster first start.
  echo "==> build workspace image ($WS_IMAGE, BAKE_AGENT_CLIS=${BAKE_AGENT_CLIS:-0})"
  docker build \
    ${RTK_VERSION:+--build-arg "RTK_VERSION=$RTK_VERSION"} \
    ${BAKE_AGENT_CLIS:+--build-arg "BAKE_AGENT_CLIS=$BAKE_AGENT_CLIS"} \
    -t "$WS_IMAGE" "$ROOT/workspace"

  # Image smoke test (verifies baked pins / lean absence; takes seconds).
  # Match the smoke's expectation to how we built (default lean). Skip with WS_SMOKE=0.
  if [ "${WS_SMOKE:-1}" = "1" ]; then
    EXPECT_AGENT_CLIS="${BAKE_AGENT_CLIS:-0}" bash "$ROOT/deploy/local/e2e-smoke.sh" "$WS_IMAGE"
  fi
else
  # native: no image — build the workspace-agent for this host instead, and check
  # the host provides what the Dockerfile normally would (warn-only; docs/34).
  echo "==> build workspace-agent (native runtime)"
  (cd "$ROOT/workspace/agent" && go build -o /tmp/af-agent .)
  AF_NATIVE_AGENT_BIN=/tmp/af-agent
  for c in tmux git claude; do
    command -v "$c" >/dev/null 2>&1 || echo "WARN: '$c' not found on host PATH (native workspaces need it)"
  done
fi

# ---- build & start Console / Control Plane (all modes) -----------------------
# Console is a Vite + React app: build it to console/dist, which the CP serves
# statically (no-store). Run `npm --prefix console run dev` (vite build --watch) in a
# separate shell during active UI work; this script does a one-shot production build.
# mermaid is large and the bundle keeps growing; the build can peak past the 2 GiB
# cgroup cap that tmux-claude.sh pins on each fleet agent. build_console escapes into a
# sibling systemd scope when boxed into a small cap, else builds in-process. See
# console-build.sh.
# shellcheck disable=SC1091
. "$ROOT/deploy/local/console-build.sh"
build_console

echo "==> build control-plane"
(cd "$ROOT/control-plane" && go build -o /tmp/af-cp .)

echo "==> control-plane on $CP_ADDR  (console: http://${CP_ADDR/#:/localhost:})  mode=$MODE runtime=$AF_RUNTIME auth=${AUTH:-dev}"
# The generic OIDC login providers (docs/61) are named at runtime — AF_OIDC_PROVIDERS
# plus AF_OIDC_<ID>_* per provider — so they can't be listed one by one like the
# fixed vars below. Forward whatever is exported, along with the GitHub adapter's
# AF_GITHUB_* (P2), which has the same open-ended shape.
oidc_env=()
while IFS='=' read -r k _; do
  case "$k" in AF_OIDC_*|AF_GITHUB_*) oidc_env+=("$k=${!k}") ;; esac
done < <(env)

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
  ${GITHUB_OAUTH_CLIENT_SECRET:+GITHUB_OAUTH_CLIENT_SECRET="$GITHUB_OAUTH_CLIENT_SECRET"} \
  ${BITBUCKET_OAUTH_KEY:+BITBUCKET_OAUTH_KEY="$BITBUCKET_OAUTH_KEY"} \
  ${BITBUCKET_OAUTH_SECRET:+BITBUCKET_OAUTH_SECRET="$BITBUCKET_OAUTH_SECRET"} \
  ${PUBLIC_BASE_URL:+PUBLIC_BASE_URL="$PUBLIC_BASE_URL"} \
  ${GOOGLE_OAUTH_CLIENT_ID:+GOOGLE_OAUTH_CLIENT_ID="$GOOGLE_OAUTH_CLIENT_ID"} \
  ${GOOGLE_OAUTH_CLIENT_SECRET:+GOOGLE_OAUTH_CLIENT_SECRET="$GOOGLE_OAUTH_CLIENT_SECRET"} \
  ${AF_COOKIE_SECRET:+AF_COOKIE_SECRET="$AF_COOKIE_SECRET"} \
  ${AF_SESSION_TTL:+AF_SESSION_TTL="$AF_SESSION_TTL"} \
  ${AF_OAUTH_ALLOWED_EMAILS:+AF_OAUTH_ALLOWED_EMAILS="$AF_OAUTH_ALLOWED_EMAILS"} \
  ${AF_OAUTH_ALLOWED_DOMAINS:+AF_OAUTH_ALLOWED_DOMAINS="$AF_OAUTH_ALLOWED_DOMAINS"} \
  ${AF_OAUTH_ALLOWED_EMAILS_FILE:+AF_OAUTH_ALLOWED_EMAILS_FILE="$AF_OAUTH_ALLOWED_EMAILS_FILE"} \
  "${oidc_env[@]}" \
  ${AF_MASTER_KEY:+AF_MASTER_KEY="$AF_MASTER_KEY"} \
  ${AF_DB:+AF_DB="$AF_DB"} \
  ${AF_PROVISION:+AF_PROVISION="$AF_PROVISION"} \
  ${SUPER_ADMIN_EMAILS:+SUPER_ADMIN_EMAILS="$SUPER_ADMIN_EMAILS"} \
  ${AF_MCP_ENABLED:+AF_MCP_ENABLED="$AF_MCP_ENABLED"} \
  /tmp/af-cp

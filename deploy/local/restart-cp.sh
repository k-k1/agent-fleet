#!/usr/bin/env bash
# Rebuild ONLY the Control Plane (Go) + Console (Vite) and restart the running
# `af-cp` host process in place — no Workspace image rebuild. Use this to reflect
# changes under control-plane/ or console/ during dev. For Workspace/Agent changes
# (workspace/), rebuild the image instead — see docs/HANDOFF.md §2 (the
# "what-to-rebuild" quick reference).
#
# Env is reproduced exactly as deploy/local/run-dev.sh would: oauth.env supplies
# AUTH + secrets + CP_ADDR; the WS_* defaults below match run-dev.sh.
#
# Run from a shell that has the `docker` group (the launched CP shells out to
# docker for Workspace start/stop) — on a non-login shell use `sg docker -c`.
#
#   deploy/local/restart-cp.sh            # rebuild console + CP, restart
#   SKIP_CONSOLE=1 deploy/local/restart-cp.sh   # CP-only change: skip vite build
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"

# --- env (mirror run-dev.sh) -------------------------------------------------
set -a; . "$ROOT/deploy/local/oauth.env"; set +a   # AUTH, CP_ADDR, AF_*, *_OAUTH_*
export CONSOLE_DIR="$ROOT/console/dist"
export WS_IMAGE="${WS_IMAGE:-agent-fleet/workspace:dev}"
export WS_DATA="${WS_DATA:-$HOME/.local/share/agent-fleet}"
export WS_MEMORY="${WS_MEMORY:-5g}"
export WS_JVM_DIR="${WS_JVM_DIR:-$WS_DATA/shared/jvm}"
# The host-run Control Plane needs an explicit source for the role-scoped docs
# mount; the containerized CP instead gets this tree from its image.
export AF_DOCS_DIR="${AF_DOCS_DIR:-$ROOT/docs}"
CP_ADDR="${CP_ADDR:-127.0.0.1:8099}"
PORT="${CP_ADDR##*:}"

# --- build -------------------------------------------------------------------
if [ "${SKIP_CONSOLE:-0}" != "1" ]; then
  export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
  # shellcheck disable=SC1091
  [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
  # Vite build can peak past the 2 GiB cgroup cap on a fleet agent; build_console
  # escapes into a sibling scope when boxed in. See console-build.sh.
  # shellcheck disable=SC1091
  . "$ROOT/deploy/local/console-build.sh"
  build_console
fi
# control-plane go build hits the same 2G cgroup cap as vite when a new/changed
# dependency (e.g. an AWS SDK v2 service package) makes it spawn enough parallel
# compile workers — see go-build.sh.
# shellcheck disable=SC1091
. "$ROOT/deploy/local/go-build.sh"
build_go_binary "$ROOT/control-plane" /tmp/af-cp "control-plane"

# --- restart in place --------------------------------------------------------
# Stop the running af-cp by program name, NOT by scraping its pid from
# `ss -ltnp`: a non-login `sg docker` shell can't read the socket owner pid, so
# the old pid-scrape silently found nothing, skipped the kill, and the new bind
# died with "address already in use" (the stale CP kept serving — healthz passed
# against it, masking the failed restart). pkill -x targets the af-cp process
# directly regardless of shell privilege.
if pkill -x af-cp 2>/dev/null; then
  echo "==> stopping current af-cp"
fi
for _ in $(seq 1 50); do ss -ltn 2>/dev/null | grep -q ":${PORT} " || break; sleep 0.1; done

echo "==> starting af-cp on $CP_ADDR (log: /tmp/af-cp.log)"
cd "$ROOT/control-plane"
setsid env CP_ADDR="$CP_ADDR" CONSOLE_DIR="$CONSOLE_DIR" \
  WS_IMAGE="$WS_IMAGE" WS_DATA="$WS_DATA" WS_MEMORY="$WS_MEMORY" WS_JVM_DIR="$WS_JVM_DIR" AF_DOCS_DIR="$AF_DOCS_DIR" \
  /tmp/af-cp >>/tmp/af-cp.log 2>&1 < /dev/null &
disown || true

# --- verify ------------------------------------------------------------------
for _ in $(seq 1 50); do
  [ "$(curl -fsS "http://${CP_ADDR}/healthz" 2>/dev/null)" = "ok" ] && { echo "==> healthz OK"; exit 0; }
  sleep 0.1
done
echo "ERROR: af-cp did not become healthy; see /tmp/af-cp.log" >&2
exit 1

#!/usr/bin/env bash
# build_console — build the Vite/Rollup console bundle to console/dist, escaping the
# fleet's per-agent cgroup memory cap when we're boxed inside one.
#
# Why this is needed: tmux-claude.sh wraps every fleet agent (claude session) in
# `systemd-run --scope -p MemoryMax=2G` to guard against host OOM — one runaway agent or
# build must not take the host and the whole fleet down with it. A cgroup v2 memory cap is
# inherited by all descendants, so a console build started inside such a capped agent is
# bound to 2G too. As the console bundle grows, Vite/Rollup's peak RSS passes 2G and it
# OOMs or thrashes even with plenty of RAM free on the host (symptom: Vite sits at
# "transforming…" for minutes to tens of minutes and finally prints "Killed"). Containers
# are not involved.
#
# The fix: only when a small finite cgroup cap is detected *and* a per-user systemd manager
# is usable, escape into a *sibling* transient scope directly under app.slice with a
# generous cap (AF_BUILD_MEM_MAX, 12G by default). On hosts with no cap or a large one, and
# in environments without systemd --user (CI and the like), the build runs in-process as
# before — behaviour unchanged.
#
# Precondition: the caller has defined $ROOT (the repository root). $NVM_DIR and
# $AF_BUILD_MEM_MAX are honoured. Under set -e a failure propagates as a build failure.
build_console() {
  local heap=3072 esc=0 cg cgmax
  cg="$(awk -F: '/^0:/{print $3}' /proc/self/cgroup 2>/dev/null)"
  cgmax="$(cat "/sys/fs/cgroup${cg}/memory.max" 2>/dev/null || echo max)"
  # Escape only when boxed into a small finite cap (<6GiB) and a user systemd can create a
  # scope.
  if [ "$cgmax" != max ] && [ "${cgmax:-0}" -lt 6442450944 ] 2>/dev/null \
     && command -v systemd-run >/dev/null 2>&1 \
     && systemd-run --user --scope --quiet -- true >/dev/null 2>&1; then
    esc=1
    heap=8192
  fi

  if [ "$esc" = 1 ]; then
    local mem="${AF_BUILD_MEM_MAX:-12G}"
    echo "==> build console (vite) — escaping the $((cgmax / 1024 / 1024 / 1024))G cgroup cap, running in a transient scope with MemoryMax=$mem"
    local inner
    inner="export NVM_DIR='${NVM_DIR:-$HOME/.nvm}'; [ -s \"\$NVM_DIR/nvm.sh\" ] && . \"\$NVM_DIR/nvm.sh\" >/dev/null 2>&1; cd '$ROOT/console' && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS='--max-old-space-size=$heap' npm run build"
    systemd-run --user --scope -p MemoryMax="$mem" --quiet -- bash -c "$inner"
  else
    echo "==> build console (vite)"
    ( cd "$ROOT/console" && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS="--max-old-space-size=$heap" npm run build )
  fi
}

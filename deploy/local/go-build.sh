#!/usr/bin/env bash
# build_go_binary <src_dir> <out_path> [label] — go build, escaping the fleet's
# per-agent cgroup memory cap when we're boxed inside one. Same rationale and
# detection as build_console in console-build.sh: tmux-claude.sh wraps each fleet
# agent in `systemd-run --scope -p MemoryMax=2G`, and a `go build` with many new/
# changed dependencies (e.g. adding an AWS SDK v2 service package) spawns enough
# parallel `compile` workers to push peak RSS past 2G, even though host RAM is
# free. Symptom: build cache under /tmp/go-build*/ stops gaining new files, the
# `go build` process sits in D state (disk sleep) doing near-zero further CPU
# time, and it can stay stuck for 20+ minutes without ever getting OOM-killed
# (memory reclaim just stalls instead of Killed, unlike the vite case).
#
# Fix: when boxed into a small finite cgroup with a per-user systemd manager
# available, run the go build in a *sibling* transient scope with a roomy
# MemoryMax (AF_BUILD_MEM_MAX, default 12G) so the build cache gets populated
# without hitting the cap; the caller's own go build (run again after, still
# inside the small cgroup) then hits a warm cache and finishes in seconds. On
# hosts without a small cap, or without systemd --user (CI etc.), builds
# in-process as before — behavior unchanged there.
#
# Preconditions: caller has $ROOT defined; set -e propagates build failure.
build_go_binary() {
  local src_dir="$1" out_path="$2" label="${3:-$2}" esc=0 cg cgmax
  cg="$(awk -F: '/^0:/{print $3}' /proc/self/cgroup 2>/dev/null)"
  cgmax="$(cat "/sys/fs/cgroup${cg}/memory.max" 2>/dev/null || echo max)"
  if [ "$cgmax" != max ] && [ "${cgmax:-0}" -lt 6442450944 ] 2>/dev/null \
     && command -v systemd-run >/dev/null 2>&1 \
     && systemd-run --user --scope --quiet -- true >/dev/null 2>&1; then
    esc=1
  fi

  if [ "$esc" = 1 ]; then
    local mem="${AF_BUILD_MEM_MAX:-12G}"
    echo "==> build $label — $((cgmax / 1024 / 1024 / 1024))G cgroup 上限を回避し transient scope MemoryMax=$mem で実行"
    local inner
    inner="cd '$src_dir' && go build -o '$out_path' ."
    systemd-run --user --scope -p MemoryMax="$mem" --quiet -- bash -c "$inner"
  else
    echo "==> build $label"
    ( cd "$src_dir" && go build -o "$out_path" . )
  fi
}

#!/bin/sh
# Copy the engine's non-test sources from memengine/ into the Agent's mirror,
# workspace/agent/internal/memengine/. The Agent module builds from the workspace/
# directory alone (workspace/Dockerfile's context), so it cannot `replace` the
# engine module that lives outside it; the mirror is the engine's source, byte for
# byte, and TestMirrorMatchesEngine fails on any drift. Run this after changing
# memengine/*.go.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
src=$root/memengine
dst=$root/workspace/agent/internal/memengine
for f in "$src"/*.go; do
	case $f in *_test.go) continue ;; esac
	cp "$f" "$dst/$(basename "$f")"
done

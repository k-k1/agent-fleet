#!/usr/bin/env bash
# Checks from outside that the sharded memoryx test runner fails a run whose child fails.
#
# The runner's own tests run under the runner: a runner that stopped flagging failed children
# would swallow those tests' failures as well and still exit 0 (measured: that mutation passes
# `go test ./internal/memoryx/` sharded). This script is the oracle that does not depend on it.
# It drives the probe tests (internal/memoryx/shard_test.go) through `go test` itself.
#
# Usage, from workspace/agent: ../scripts/memoryx-shard-smoke.sh
set -uo pipefail

run() { AF_MEMORYX_SHARDS=2 AF_MEMORYX_SHARD_PROBE="$1" go test -count=1 -run '^TestShardProbe' ./internal/memoryx/ 2>&1; }

bad=0
out=$(run "")
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "memoryx shard smoke: the passing probes exited $rc:"; echo "$out"; bad=1
fi
for mode in fail exit0; do
  out=$(run "$mode")
  rc=$?
  if [ "$rc" -eq 0 ] || ! grep -q 'TestShardProbeB' <<<"$out"; then
    echo "memoryx shard smoke: probe mode $mode exited $rc without naming TestShardProbeB:"; echo "$out"; bad=1
  fi
done
[ "$bad" -eq 0 ] && echo "memoryx shard smoke: ok"
exit "$bad"

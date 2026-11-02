#!/bin/bash
label=$1; bin=$2; shift 2
out=/home/dev/.af-work/sange6k/m
cd /home/dev/repos/agent-fleet@feature-3102-memoryx-parallel-tests-study/workspace/agent/internal/memoryx
l0=$(cat /proc/loadavg); s=$EPOCHREALTIME
"$bin" -test.count=1 -test.timeout 20m -test.v=test2json "$@" 2>&1 | go tool test2json -t > $out/$label.json
rc=${PIPESTATUS[0]}; e=$EPOCHREALTIME; l1=$(cat /proc/loadavg)
printf '%s rc=%s wall=%.1f load_start=[%s] load_end=[%s] args=[%s]\n' "$label" "$rc" "$(awk "BEGIN{print $e - $s}")" "$l0" "$l1" "$*" | tee -a $out/summary.txt

#!/bin/bash
# usage: run.sh <label> <testbinary> [extra test flags...]
label=$1; bin=$2; shift 2
out=/home/dev/.af-work/sange6k/m
cd /home/dev/repos/agent-fleet@feature-3102-memoryx-parallel-tests-study/workspace/agent/internal/memoryx
l0=$(cat /proc/loadavg)
s=$(date +%s.%N)
"$bin" -test.count=1 -test.timeout 20m -test.v=test2json "$@" > $out/$label.raw 2>&1
rc=$?
e=$(date +%s.%N)
l1=$(cat /proc/loadavg)
go tool test2json -t < $out/$label.raw > $out/$label.json
printf '%s rc=%s wall=%.1f load_start=[%s] load_end=[%s]\n' "$label" "$rc" "$(awk "BEGIN{print $e - $s}")" "$l0" "$l1" | tee -a $out/summary.txt

#!/usr/bin/env bash
# Stub test for deploy/gcp/gke/pause.sh (issue #1639).
#
# No cluster, project or credentials: stateful fake `gcloud` / `kubectl` on PATH keep the
# deployment's moving parts in files and log every call. What is pinned is the ORDER, the
# remembered sizes and the refusals, because those are what go wrong on a real cluster:
#
#   - down: CP, workspaces, workspace pool, system pool, Cloud SQL. The fake autoscaler starts
#     a workspace node when the system pool hits 0 while the workspace pool still autoscales
#     (the pool has no taint), so a wrong order leaves a node billing. It also adds one when the system pool hits 0 with
#     autoscaling already off (the autoscaler lags), so the workspace pool must be re-read and
#     emptied again after the system pool, and the pause must fail when it cannot.
#   - sizes are per ZONE, as in `gcloud container clusters resize`; the suite runs on a
#     two-zone (regional) and a one-zone (zonal) cluster
#   - the sizes are read from the pools, kept in one annotate, never overwritten by a re-run,
#     and restored in full by --up (also after an interrupted resize)
#   - a read that fails stops the script before any write; so does a context that points at
#     another cluster
#   - without --yes, nothing is written
# shellcheck disable=SC2016,SC2034  # check/eval conditions are deliberately single-quoted
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$(cd "$HERE/../.." && pwd)/deploy/gcp/gke/pause.sh"

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; S="$WORK/s"; LOG="$WORK/calls.log"
mkdir -p "$STUB" "$S"

cat > "$STUB/gcloud" <<'STUBEOF'
#!/usr/bin/env bash
S="$AF_STUB_STATE"; echo "gcloud $*" >> "$AF_STUB_LOG"
a="$*"
if [ -n "${AF_STUB_FAIL_ON:-}" ] && [[ "$a" == *"$AF_STUB_FAIL_ON"* ]]; then echo "stub: injected failure" >&2; exit 1; fi
zones() { if [ "${AF_STUB_ZONES:-2}" = 1 ]; then echo "a"; else echo "a b"; fi; }
urls() {  # <pool tag>
  local out="" z
  for z in $(zones); do out="${out:+$out;}https://x/zones/zone-$z/instanceGroupManagers/ig-$1-$z"; done
  echo "$out"
}
case "$a" in
  *"clusters describe"*"value(endpoint)"*) echo "${AF_STUB_ENDPOINT:-10.0.0.1}" ;;
  *"clusters describe"*"privateEndpoint"*) echo 10.1.0.2 ;;
  *"clusters describe"*"clusterCaCertificate"*) echo "Q0E=" ;;
  *"sql instances describe"*"settings.activationPolicy"*) cat "$S/sql_policy" ;;
  *"sql instances describe"*"value(state)"*) echo RUNNABLE ;;
  *"sql instances patch"*"=NEVER"*) echo NEVER > "$S/sql_policy" ;;
  *"sql instances patch"*"=ALWAYS"*) echo ALWAYS > "$S/sql_policy" ;;
  *"node-pools describe workspace"*"name,autoscaling.enabled"*)
    v="$(cat "$S/ws_auto")"
    # gcloud omits a false value: the name alone is a disabled pool. AF_STUB_AUTO_JUNK: garbage.
    if [ -n "${AF_STUB_AUTO_JUNK:-}" ]; then printf 'workspace\t%s\n' "$AF_STUB_AUTO_JUNK"
    elif [ "$v" = True ]; then printf 'workspace\tTrue\n'; else echo workspace; fi ;;
  *"node-pools describe workspace"*"autoscaling.enabled"*) exit 98 ;;
  *"node-pools describe workspace"*"autoscaling.minNodeCount"*) v="$(cat "$S/ws_min")"; [ "$v" = 0 ] || echo "$v" ;;
  *"node-pools describe workspace"*"autoscaling.maxNodeCount"*) cat "$S/ws_max" ;;
  *"node-pools describe workspace"*"instanceGroupUrls"*) urls ws ;;
  *"node-pools describe system"*"instanceGroupUrls"*) urls sys ;;
  *"node-pools describe"*) : ;;
  *"instance-groups managed describe ig-sys-b"*)
    if [ -f "$S/sys_nodes_b" ]; then cat "$S/sys_nodes_b"; else cat "$S/sys_nodes"; fi ;;
  *"instance-groups managed describe ig-sys-"*) cat "$S/sys_nodes" ;;
  *"instance-groups managed describe ig-ws-"*)
    cat "$S/ws_nodes"
    # AF_STUB_LATE: the autoscaler's node appears just after the first read that saw 0.
    if [ -f "$S/ws_late" ]; then rm -f "$S/ws_late"; echo 1 > "$S/ws_nodes"; fi ;;
  *"node-pools update workspace"*"--no-enable-autoscaling"*) echo False > "$S/ws_auto" ;;
  *"node-pools update workspace"*"--enable-autoscaling"*)
    echo True > "$S/ws_auto"
    set -- $a
    while [ $# -gt 0 ]; do
      case "$1" in --min-nodes) echo "$2" > "$S/ws_min" ;; --max-nodes) echo "$2" > "$S/ws_max" ;; esac
      shift
    done ;;
  *"clusters resize"*"--node-pool workspace"*)
    set -- $a; while [ $# -gt 0 ]; do [ "$1" = --num-nodes ] && echo "$2" > "$S/ws_nodes"; shift; done
    # AF_STUB_STUCK: the autoscaler keeps replacing the node while the system pool is empty.
    if [ -n "${AF_STUB_STUCK:-}" ] && [ "$(cat "$S/sys_nodes")" = 0 ]; then echo 1 > "$S/ws_nodes"; fi ;;
  *"clusters resize"*"--node-pool system"*)
    set -- $a; while [ $# -gt 0 ]; do [ "$1" = --num-nodes ] && echo "$2" > "$S/sys_nodes"; shift; done
    rm -f "$S/sys_nodes_b"
    # The trap: kube-system pods are pending and the untainted workspace pool has a cluster
    # autoscaler that adds a node once the system pool is empty -- even when autoscaling was
    # already switched off, because it has not seen that yet (measured on a real cluster).
    if [ "$(cat "$S/sys_nodes")" = 0 ]; then
      if [ -n "${AF_STUB_LATE:-}" ]; then : > "$S/ws_late"; else echo 1 > "$S/ws_nodes"; fi
    fi ;;
  *) echo "gcloud stub: unexpected: $a" >&2; exit 99 ;;
esac
STUBEOF
cat > "$STUB/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
S="$AF_STUB_STATE"; echo "kubectl $*" >> "$AF_STUB_LOG"
args=(); skip=0
for x in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  if [ "$x" = --context ]; then skip=1; continue; fi
  args+=("$x")
done
a="${args[*]}"
if [ -n "${AF_STUB_FAIL_ON:-}" ] && [[ "$a" == *"$AF_STUB_FAIL_ON"* ]]; then echo "stub: injected failure" >&2; exit 1; fi
nz=2; [ "${AF_STUB_ZONES:-2}" = 1 ] && nz=1
sys_total() {
  local t=$(( $(cat "$S/sys_nodes") * nz ))
  if [ -f "$S/sys_nodes_b" ] && [ "$nz" = 2 ]; then t=$(( $(cat "$S/sys_nodes") + $(cat "$S/sys_nodes_b") )); fi
  echo "$t"
}
nodes() { local n="$1" i; for ((i = 0; i < n; i++)); do echo "node/$2-$i"; done; }
ready() { local n="$1" i; for ((i = 0; i < n; i++)); do echo True; done; }
case "$a" in
  "config view --raw --minify -o jsonpath={.clusters[0].cluster.server}") echo "${AF_STUB_SERVER:-https://10.0.0.1}" ;;
  "config view --raw --minify -o jsonpath={.clusters[0].cluster.certificate-authority-data}") echo "Q0E=" ;;
  "get namespace "*"-o jsonpath="*annotations*pause-*)
    k="${a##*pause-}"; k="${k%\}}"; cat "$S/ann_$k" 2>/dev/null || true ;;
  "get namespace "*) : ;;
  *"annotate namespace"*)
    for x in "${args[@]}"; do
      case "$x" in
        agent-fleet.io/pause-*-) k="${x#agent-fleet.io/pause-}"; rm -f "$S/ann_${k%-}" ;;
        agent-fleet.io/pause-*=*) kv="${x#agent-fleet.io/pause-}"; echo "${kv#*=}" > "$S/ann_${kv%%=*}" ;;
      esac
    done ;;
  *"get deployment af-cp"*) cat "$S/cp_replicas" ;;
  *"get pods -o name"*) cat "$S/ws_pods" ;;
  *"get nodes -l agent-fleet.io/pool=system -o jsonpath"*) ready "$(sys_total)" ;;
  *"get nodes -l agent-fleet.io/pool=workspace"*) [ -z "${AF_STUB_HIDE_NODES:-}" ] || exit 0; false ;;&
  *"get nodes -l agent-fleet.io/pool=workspace -o jsonpath"*) ready $(( $(cat "$S/ws_nodes") * nz )) ;;
  *"get nodes -l agent-fleet.io/pool=system"*) nodes "$(sys_total)" sys ;;
  *"get nodes -l agent-fleet.io/pool=workspace"*) nodes $(( $(cat "$S/ws_nodes") * nz )) ws ;;
  *"scale deployment/af-cp --replicas="*) echo "${a##*=}" > "$S/cp_replicas" ;;
  *"scale statefulset --all --replicas=0"*) : > "$S/ws_pods" ;;
  *"wait "*|*"rollout status"*) : ;;
  *) echo "kubectl stub: unexpected: $a" >&2; exit 99 ;;
esac
STUBEOF
chmod +x "$STUB/gcloud" "$STUB/kubectl"

export AF_PAUSE_POLL=1 AF_PAUSE_READY_TIMEOUT=3 AF_STUB_STATE="$S" AF_STUB_LOG="$LOG"
export PATH="$STUB:$PATH"
unset AF_STUB_FAIL_ON AF_STUB_ENDPOINT AF_STUB_SERVER AF_STUB_LATE AF_STUB_STUCK AF_STUB_AUTO_JUNK AF_STUB_HIDE_NODES

SYS=2   # system nodes per zone while running
reset() {  # a running deployment: autoscaled workspace pool, no record
  rm -f "$S"/ann_* "$S/sys_nodes_b" "$S/ws_late"; : > "$LOG"
  echo 1 > "$S/cp_replicas"; echo "$SYS" > "$S/sys_nodes"; echo 0 > "$S/ws_nodes"
  echo True > "$S/ws_auto"; echo 0 > "$S/ws_min"; echo 3 > "$S/ws_max"
  echo ALWAYS > "$S/sql_policy"; : > "$S/ws_pods"
}
P=(--project proj --location asia-northeast1 --prefix af)
pause() { "$SCRIPT" "${P[@]}" "$@" > "$WORK/out" 2>&1 < /dev/null; }

fails=0
ok()   { echo "ok   - $1"; }
fail() { echo "FAIL - $1"; fails=$((fails + 1)); }
check() { if eval "$2"; then ok "[zones=$AF_STUB_ZONES] $1"; else fail "[zones=$AF_STUB_ZONES] $1"; fi; }
line() { grep -n -m1 -F -- "$1" "$LOG" | cut -d: -f1; }
before() {  # <first> <second> — both calls present, first earlier
  local x y; x="$(line "$1")"; y="$(line "$2")"
  [ -n "$x" ] && [ -n "$y" ] && [ "$x" -lt "$y" ]
}
writes() { grep -cE 'scale |annotate |resize|node-pools update|instances patch' "$LOG" || true; }

suite() {
  export AF_STUB_ZONES="$1"

  # --- 1. no --yes: a plan, no write
  reset
  pause && rc=0 || rc=$?
  check "plan only without --yes (exit 0)" '[ "$rc" = 0 ]'
  check "plan only without --yes (no write)" '[ "$(writes)" = 0 ]'
  check "plan names the billing that remains" 'grep -q "Cloud NAT" "$WORK/out" && grep -q "forwarding rule" "$WORK/out"'

  # --- 2. down
  reset
  pause --yes && rc=0 || rc=$?
  check "down exits 0" '[ "$rc" = 0 ]'
  check "order: CP before workspace pool autoscaling off" 'before "scale deployment/af-cp --replicas=0" "--no-enable-autoscaling"'
  check "order: autoscaling off before system pool to 0" 'before "--no-enable-autoscaling" "--node-pool system --num-nodes 0"'
  check "order: system pool before Cloud SQL" 'before "--node-pool system --num-nodes 0" "--activation-policy=NEVER"'
  check "no workspace node left (the untainted-pool trap)" '[ "$(cat "$S/ws_nodes")" = 0 ]'
  check "final: CP 0, system 0, Cloud SQL NEVER" '[ "$(cat "$S/cp_replicas")" = 0 ] && [ "$(cat "$S/sys_nodes")" = 0 ] && [ "$(cat "$S/sql_policy")" = NEVER ]'
  check "records the per-zone system size" '[ "$(cat "$S/ann_system-nodes")" = "$SYS" ]'
  check "records workspace autoscaling" '[ "$(cat "$S/ann_workspace-min")" = 0 ] && [ "$(cat "$S/ann_workspace-max")" = 3 ]'
  check "the record is written in one annotate" '[ "$(grep -c "annotate" "$LOG")" = 1 ]'

  # --- 2b. the autoscaler adds a workspace node after the system pool hits 0
  check "re-checks the workspace pool after the system pool is empty" '[ "$(grep -n -- "--node-pool workspace --num-nodes 0" "$LOG" | tail -1 | cut -d: -f1)" -gt "$(line "--node-pool system --num-nodes 0")" ]'
  check "down says the pool came back and fixed it" 'grep -q "workspace pool is back" "$WORK/out"'
  check "status after the pause says paused, no node up" 'pause --status; grep -q "state              : paused" "$WORK/out" && grep -q "workspace nodes    : 0" "$WORK/out"'
  reset
  AF_STUB_LATE=1 pause --yes && rc=0 || rc=$?
  check "a node that appears after the first clean read is still removed" '[ "$rc" = 0 ] && [ "$(cat "$S/ws_nodes")" = 0 ] && [ "$(cat "$S/sys_nodes")" = 0 ]'
  reset
  AF_STUB_STUCK=1 AF_PAUSE_SETTLE_ROUNDS=3 pause --yes && rc=0 || rc=$?
  check "a node that cannot be removed fails loudly, not 'paused'" '[ "$rc" != 0 ] && grep -q "did not stay at 0" "$WORK/out" && ! grep -q "==> paused" "$WORK/out"'
  pause --status
  check "status after a failed pause does not say paused" '! grep -q "state              : paused" "$WORK/out" && grep -q "NOT fully paused" "$WORK/out"'
  # an empty / unknown autoscaling answer is not "off"
  reset
  AF_STUB_AUTO_JUNK=maybe pause --yes && rc=0 || rc=$?
  check "an unknown autoscaling.enabled stops the pause before any write" '[ "$rc" != 0 ] && [ "$(writes)" = 0 ]'
  # a VM that is starting is in the instance group but not yet a registered node
  reset; echo 0 > "$S/cp_replicas"; echo 0 > "$S/sys_nodes"; echo 1 > "$S/ws_nodes"; echo False > "$S/ws_auto"
  AF_STUB_HIDE_NODES=1 pause --status
  check "status: instance group size 1 with no registered node is not paused" '! grep -q "state              : paused" "$WORK/out" && grep -q "NOT fully paused" "$WORK/out"'
  reset
  pause --yes && rc=0 || rc=$?   # leave the paused state the next steps expect

  # --- 3. a second pause changes nothing and keeps the record
  : > "$LOG"
  pause --yes && rc=0 || rc=$?
  check "second down exits 0" '[ "$rc" = 0 ]'
  check "second down: no resize / patch / autoscaling change / annotate" '! grep -qE "resize|instances patch|node-pools update|annotate" "$LOG"'
  check "second down keeps the record" '[ "$(cat "$S/ann_system-nodes")" = "$SYS" ] && [ "$(cat "$S/ann_workspace-max")" = 3 ]'

  # --- 4. status
  : > "$LOG"
  pause --status && rc=0 || rc=$?
  check "status exits 0 and writes nothing" '[ "$rc" = 0 ] && [ "$(writes)" = 0 ]'
  check "status reports the pause" 'grep -q "desired=0" "$WORK/out" && grep -q "policy=NEVER" "$WORK/out" && grep -q "system nodes       : 0" "$WORK/out"'

  # --- 5. up
  : > "$LOG"
  pause --up && rc=0 || rc=$?
  check "up exits 0" '[ "$rc" = 0 ]'
  check "order: Cloud SQL before system pool" 'before "--activation-policy=ALWAYS" "--node-pool system --num-nodes $SYS"'
  check "order: system pool before autoscaling" 'before "--node-pool system --num-nodes $SYS" "--enable-autoscaling"'
  check "order: autoscaling before CP" 'before "--enable-autoscaling" "scale deployment/af-cp --replicas=1"'
  check "up restores the per-zone sizes that were recorded" '[ "$(cat "$S/sys_nodes")" = "$SYS" ] && [ "$(cat "$S/ws_max")" = 3 ] && [ "$(cat "$S/ws_auto")" = True ]'
  check "up clears the record" '! ls "$S"/ann_* >/dev/null 2>&1'
  : > "$LOG"
  pause --up && rc=0 || rc=$?
  check "second up is a no-op" '[ "$rc" = 0 ] && ! grep -qE "resize|instances patch|node-pools update" "$LOG"'

  # --- 6. running workspaces
  reset; printf 'pod/ws-a-0\n' > "$S/ws_pods"
  pause --yes && rc=0 || rc=$?
  check "running workspace without --stop-workspaces refuses" '[ "$rc" = 1 ] && [ "$(writes)" = 0 ] && grep -q "pod/ws-a-0" "$WORK/out"'
  pause --yes --stop-workspaces && rc=0 || rc=$?
  check "--stop-workspaces pauses" '[ "$rc" = 0 ] && [ ! -s "$S/ws_pods" ] && [ "$(cat "$S/sql_policy")" = NEVER ]'
  check "order: CP before workspaces before pool resize" 'before "scale deployment/af-cp --replicas=0" "scale statefulset" && before "scale statefulset" "--node-pool system"'

  # --- 7. --dry-run
  reset
  pause --yes --dry-run && rc=0 || rc=$?
  check "dry-run echoes the writes and changes nothing" '[ "$rc" = 0 ] && grep -q "^DRY: " "$WORK/out" && [ "$(cat "$S/cp_replicas")" = 1 ] && [ "$(cat "$S/sql_policy")" = ALWAYS ] && [ "$(writes)" = 0 ]'

  # --- 8. --up without a record
  reset; echo 0 > "$S/cp_replicas"; echo 0 > "$S/sys_nodes"; echo NEVER > "$S/sql_policy"; echo False > "$S/ws_auto"
  pause --up && rc=0 || rc=$?
  check "up without a record asks for --system-nodes (and writes nothing)" '[ "$rc" = 1 ] && grep -q -- "--system-nodes" "$WORK/out" && [ "$(writes)" = 0 ]'
  pause --up --system-nodes 3 && rc=0 || rc=$?
  check "up with --system-nodes works" '[ "$rc" = 0 ] && [ "$(cat "$S/sys_nodes")" = 3 ] && [ "$(cat "$S/cp_replicas")" = 1 ]'

  # --- 9. R1: a failed read is not an empty answer
  reset; printf 'pod/ws-a-0\n' > "$S/ws_pods"
  AF_STUB_FAIL_ON="get pods" pause --yes --stop-workspaces && rc=0 || rc=$?
  check "failed pod listing stops the pause before any write" '[ "$rc" != 0 ] && [ "$(writes)" = 0 ] && [ "$(cat "$S/cp_replicas")" = 1 ]'
  reset
  AF_STUB_FAIL_ON="node-pools describe workspace" pause --yes && rc=0 || rc=$?
  check "failed pool read stops the pause before any write" '[ "$rc" != 0 ] && [ "$(writes)" = 0 ] && [ "$(cat "$S/sql_policy")" = ALWAYS ]'
  reset
  AF_STUB_FAIL_ON="annotations" pause --yes && rc=0 || rc=$?
  check "failed record read stops the pause before any write" '[ "$rc" != 0 ] && [ "$(writes)" = 0 ]'
  reset; echo 0 > "$S/cp_replicas"; echo 0 > "$S/sys_nodes"; echo NEVER > "$S/sql_policy"; echo 3 > "$S/ann_system-nodes"
  AF_STUB_FAIL_ON="annotations" pause --up && rc=0 || rc=$?
  check "failed record read stops --up before any write (no default restore)" '[ "$rc" != 0 ] && [ "$(writes)" = 0 ]'

  # --- 10. R2: an interrupted pause keeps the original record
  reset; echo 3 > "$S/ann_system-nodes"; echo 1 > "$S/sys_nodes"   # resize to 0 stopped early
  pause --yes && rc=0 || rc=$?
  check "re-run after an interrupted resize keeps the first record" '[ "$rc" = 0 ] && [ "$(cat "$S/ann_system-nodes")" = 3 ] && [ "$(cat "$S/sys_nodes")" = 0 ]'
  reset
  if [ "$1" = 2 ]; then
    echo 3 > "$S/sys_nodes"; echo 1 > "$S/sys_nodes_b"            # zones disagree, nothing recorded
    pause --yes && rc=0 || rc=$?
    check "zones that disagree with no record: abort, no write" '[ "$rc" = 1 ] && [ "$(writes)" = 0 ]'
  fi

  # --- 11. R3: up finishes a partial resize and keeps the record until the CP is back
  reset; echo 0 > "$S/cp_replicas"; echo NEVER > "$S/sql_policy"; echo False > "$S/ws_auto"
  echo 3 > "$S/ann_system-nodes"; echo 1 > "$S/sys_nodes"
  pause --up && rc=0 || rc=$?
  check "up resizes a partly restored pool to the recorded size" '[ "$rc" = 0 ] && [ "$(cat "$S/sys_nodes")" = 3 ] && grep -q -- "--node-pool system --num-nodes 3" "$LOG" && ! ls "$S"/ann_* >/dev/null 2>&1'
  reset; echo 0 > "$S/cp_replicas"; echo NEVER > "$S/sql_policy"; echo False > "$S/ws_auto"
  echo 3 > "$S/ann_system-nodes"; echo 0 > "$S/sys_nodes"
  AF_STUB_FAIL_ON="rollout status" pause --up && rc=0 || rc=$?
  check "the record survives a CP that does not come up" '[ "$rc" != 0 ] && [ "$(cat "$S/ann_system-nodes")" = 3 ]'

  # --- 12. R4: a context of the right name that points at another cluster
  reset
  AF_STUB_SERVER=https://10.9.9.9 pause --yes && rc=0 || rc=$?
  check "foreign endpoint behind the right context name: refuse, no write" '[ "$rc" = 1 ] && [ "$(writes)" = 0 ] && grep -q "does not point at cluster" "$WORK/out"'
  AF_STUB_ENDPOINT=10.7.7.7 pause --status && rc=0 || rc=$?
  check "the same check guards --status" '[ "$rc" = 1 ]'
  reset
  AF_STUB_FAIL_ON="get namespace af-cp" pause --yes && rc=0 || rc=$?
  check "unreachable cluster stops before any write" '[ "$rc" = 1 ] && [ "$(writes)" = 0 ] && grep -q get-credentials "$WORK/out"'
}

suite 2
suite 1

"$SCRIPT" --project p > /dev/null 2>&1 && rc=0 || rc=$?
AF_STUB_ZONES=0; check "missing args exit 2" '[ "$rc" = 2 ]'

[ "$fails" = 0 ] || { echo "$fails check(s) failed"; exit 1; }
echo "all checks passed"

#!/usr/bin/env bash
# Stub test for deploy/gcp/gke/pause.sh (issue #1639).
#
# No cluster, project or credentials: stateful fake `gcloud` / `kubectl` on PATH keep the
# deployment's moving parts in files and log every call. What is pinned is the ORDER and the
# remembered sizes, because those are what go wrong on a real cluster and cost money:
#
#   - down: CP, workspaces, workspace pool, system pool, Cloud SQL. The fake autoscaler starts
#     a workspace node when the system pool hits 0 while the workspace pool still autoscales
#     (the pool has no taint), so a wrong order leaves a node billing.
#   - the sizes are read from the live pools and restored by --up, not assumed
#   - a second pause run is a no-op and keeps the first run's record
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
case "$a" in
  *"sql instances describe"*"settings.activationPolicy"*) cat "$S/sql_policy" ;;
  *"sql instances describe"*"value(state)"*) echo RUNNABLE ;;
  *"sql instances patch"*"=NEVER"*) echo NEVER > "$S/sql_policy" ;;
  *"sql instances patch"*"=ALWAYS"*) echo ALWAYS > "$S/sql_policy" ;;
  *"node-pools describe workspace"*"autoscaling.enabled"*) cat "$S/ws_auto" ;;
  *"node-pools describe workspace"*"autoscaling.minNodeCount"*) cat "$S/ws_min" ;;
  *"node-pools describe workspace"*"autoscaling.maxNodeCount"*) cat "$S/ws_max" ;;
  *"node-pools describe"*"value(locations)"*) echo "zone-a;zone-b" ;;
  *"node-pools describe"*) : ;;
  *"node-pools update workspace"*"--no-enable-autoscaling"*) echo False > "$S/ws_auto" ;;
  *"node-pools update workspace"*"--enable-autoscaling"*)
    echo True > "$S/ws_auto"
    set -- $a
    while [ $# -gt 0 ]; do
      case "$1" in --min-nodes) echo "$2" > "$S/ws_min" ;; --max-nodes) echo "$2" > "$S/ws_max" ;; esac
      shift
    done ;;
  *"clusters resize"*"--node-pool workspace"*)
    set -- $a; while [ $# -gt 0 ]; do [ "$1" = --num-nodes ] && echo "$2" > "$S/ws_nodes"; shift; done ;;
  *"clusters resize"*"--node-pool system"*)
    set -- $a; while [ $# -gt 0 ]; do [ "$1" = --num-nodes ] && echo "$2" > "$S/sys_nodes"; shift; done
    # The trap: kube-system pods are pending, the untainted workspace pool autoscales.
    if [ "$(cat "$S/sys_nodes")" = 0 ] && [ "$(cat "$S/ws_auto")" = True ]; then echo 1 > "$S/ws_nodes"; fi ;;
  *) echo "gcloud stub: unexpected: $a" >&2; exit 99 ;;
esac
STUBEOF
cat > "$STUB/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
S="$AF_STUB_STATE"; echo "kubectl $*" >> "$AF_STUB_LOG"
[ "${AF_STUB_NO_CLUSTER:-0}" != 1 ] || { echo "no such context" >&2; exit 1; }
args=(); skip=0
for x in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  if [ "$x" = --context ]; then skip=1; continue; fi
  args+=("$x")
done
a="${args[*]}"
nodes() { local n="$1" i; for ((i = 0; i < n; i++)); do echo "node/$2-$i"; done; }
case "$a" in
  "get namespace "*"-o jsonpath="*annotations*pause-*)
    k="${a##*pause-}"; k="${k%\}}"; cat "$S/ann_$k" 2>/dev/null || true ;;
  "get namespace "*) : ;;
  *"annotate namespace"*"-")
    k="${a##*pause-}"; k="${k%-}"; rm -f "$S/ann_$k" ;;
  *"annotate namespace"*"--overwrite"*)
    kv="${a##*pause-}"; echo "${kv#*=}" > "$S/ann_${kv%%=*}" ;;
  *"get deployment af-cp"*readyReplicas*) cat "$S/cp_replicas" ;;
  *"get deployment af-cp"*) cat "$S/cp_replicas" ;;
  *"get pods -o name"*) cat "$S/ws_pods" ;;
  *"get nodes -l agent-fleet.io/pool=system"*) nodes "$(cat "$S/sys_nodes")" sys ;;
  *"get nodes -l agent-fleet.io/pool=workspace"*) nodes "$(cat "$S/ws_nodes")" ws ;;
  *"scale deployment/af-cp --replicas="*) echo "${a##*=}" > "$S/cp_replicas" ;;
  *"scale statefulset --all --replicas=0"*) : > "$S/ws_pods" ;;
  *"wait "*|*"rollout status"*) : ;;
  *) echo "kubectl stub: unexpected: $a" >&2; exit 99 ;;
esac
STUBEOF
chmod +x "$STUB/gcloud" "$STUB/kubectl"

export AF_STUB_STATE="$S" AF_STUB_LOG="$LOG"
export PATH="$STUB:$PATH"

reset() {  # a running deployment: 2 system nodes (one per zone), autoscaled workspace pool
  rm -f "$S"/ann_*; : > "$LOG"
  echo 1 > "$S/cp_replicas"; echo 2 > "$S/sys_nodes"; echo 0 > "$S/ws_nodes"
  echo True > "$S/ws_auto"; echo 0 > "$S/ws_min"; echo 3 > "$S/ws_max"
  echo ALWAYS > "$S/sql_policy"; : > "$S/ws_pods"
}
P=(--project proj --location asia-northeast1 --prefix af)
pause() { "$SCRIPT" "${P[@]}" "$@" > "$WORK/out" 2>&1 < /dev/null; }

fails=0
ok()   { echo "ok   - $1"; }
fail() { echo "FAIL - $1"; fails=$((fails + 1)); }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }
line() { grep -n -m1 -F -- "$1" "$LOG" | cut -d: -f1; }
before() {  # <first> <second> — both calls present, first earlier
  local x y; x="$(line "$1")"; y="$(line "$2")"
  [ -n "$x" ] && [ -n "$y" ] && [ "$x" -lt "$y" ]
}
writes() { grep -cE 'scale |annotate |resize|node-pools update|instances patch' "$LOG" || true; }

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
check "records system nodes per zone" '[ "$(cat "$S/ann_system-nodes")" = 1 ]'
check "records workspace autoscaling" '[ "$(cat "$S/ann_workspace-min")" = 0 ] && [ "$(cat "$S/ann_workspace-max")" = 3 ]'

# --- 3. a second pause changes nothing and keeps the record
: > "$LOG"
pause --yes && rc=0 || rc=$?
check "second down exits 0" '[ "$rc" = 0 ]'
check "second down: no resize / patch / autoscaling change" '! grep -qE "resize|instances patch|node-pools update" "$LOG"'
check "second down keeps the record" '[ "$(cat "$S/ann_system-nodes")" = 1 ] && [ "$(cat "$S/ann_workspace-max")" = 3 ]'

# --- 4. status
: > "$LOG"
pause --status && rc=0 || rc=$?
check "status exits 0 and writes nothing" '[ "$rc" = 0 ] && [ "$(writes)" = 0 ]'
check "status reports the pause" 'grep -q "desired=0" "$WORK/out" && grep -q "policy=NEVER" "$WORK/out" && grep -q "system nodes       : 0" "$WORK/out"'

# --- 5. up
: > "$LOG"
pause --up && rc=0 || rc=$?
check "up exits 0" '[ "$rc" = 0 ]'
check "order: Cloud SQL before system pool" 'before "--activation-policy=ALWAYS" "--node-pool system --num-nodes 1"'
check "order: system pool before autoscaling" 'before "--node-pool system --num-nodes 1" "--enable-autoscaling"'
check "order: autoscaling before CP" 'before "--enable-autoscaling" "scale deployment/af-cp --replicas=1"'
check "up restores the sizes that were recorded" '[ "$(cat "$S/sys_nodes")" = 1 ] && [ "$(cat "$S/ws_max")" = 3 ] && [ "$(cat "$S/ws_auto")" = True ]'
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
check "up without a record asks for --system-nodes" '[ "$rc" = 1 ] && grep -q -- "--system-nodes" "$WORK/out"'
pause --up --system-nodes 3 && rc=0 || rc=$?
check "up with --system-nodes works" '[ "$rc" = 0 ] && [ "$(cat "$S/sys_nodes")" = 3 ] && [ "$(cat "$S/cp_replicas")" = 1 ]'

# --- 9. wrong cluster
reset
AF_STUB_NO_CLUSTER=1 pause --yes && rc=0 || rc=$?
check "unreachable cluster stops before any write" '[ "$rc" = 1 ] && [ "$(writes)" = 0 ] && grep -q get-credentials "$WORK/out"'

# --- 10. usage
"$SCRIPT" --project p > /dev/null 2>&1 && rc=0 || rc=$?
check "missing args exit 2" '[ "$rc" = 2 ]'

[ "$fails" = 0 ] || { echo "$fails check(s) failed"; exit 1; }
echo "all checks passed"

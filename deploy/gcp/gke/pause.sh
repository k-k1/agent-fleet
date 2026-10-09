#!/usr/bin/env bash
# Agent Fleet — put a GKE deployment to sleep without tearing it down, and wake it again.
#
#   deploy/gcp/gke/pause.sh --project <p> --location <region|zone> --prefix <name_prefix> --yes
#   deploy/gcp/gke/pause.sh --project <p> --location <l> --prefix <x> --up
#   deploy/gcp/gke/pause.sh --project <p> --location <l> --prefix <x> --status
#
# The GKE counterpart of deploy/aws/ecs/pause.sh (issue #1639). Names come from
# deploy/gcp/gke (`name_prefix`): cluster `<prefix>-gke`, Cloud SQL `<prefix>-pg`, namespaces
# `<prefix>-cp` and `<prefix>-ws`, node pools `system` and `workspace`. Run it in a shell whose
# kubectl points at the cluster (`terraform output -raw get_credentials`, runbook step 3); it
# addresses that context explicitly and refuses to continue when the cluster is not there.
#
# ## The order matters
#
# Down: CP to 0, workspaces gone, workspace pool to 0, system pool to 0, Cloud SQL stopped.
# Up is the reverse: Cloud SQL, system pool, workspace pool's autoscaler, CP. A CP that starts
# before its database answers 500 until it is reachable; a database stopped before the CP is
# gone has a CP writing into nothing.
#
# The workspace pool has no taint. With the system pool at 0, the cluster's own pods
# (kube-dns and friends) are pending and the workspace pool's autoscaler would start a
# workspace node for them, which bills ~¥78/hour for nothing. So the pool's autoscaling is
# switched off and the pool resized to 0 explicitly, before the system pool goes; waiting for
# the autoscaler's scale-down would be a ten-minute guess at when it has finished.
#
# ## What is remembered, and where
#
# `--up` must restore the node counts as they were, not as Terraform's defaults say. They are
# written as annotations on the CP namespace before anything is changed:
#   agent-fleet.io/pause-system-nodes   nodes per zone in the system pool
#   agent-fleet.io/pause-workspace-min  the workspace pool's autoscaling minimum (per zone)
#   agent-fleet.io/pause-workspace-max  and maximum
# and removed by --up. A namespace survives `kubectl apply -k`; a running pool is never
# overwritten with a zero (a second pause run keeps the first run's record). While paused,
# `terraform plan` shows the workspace pool's autoscaling and the system pool's size as drift;
# `--up` closes it. Without a record (the namespace was deleted), --up needs --system-nodes.
#
# ## What keeps billing while paused
#
# Only deletion stops these: the GKE cluster management fee, the load balancer's forwarding
# rule, the Private Service Connect endpoint, Cloud NAT and its reserved address, the
# persistent disks (every workspace's two claims, the CP's own) and Cloud SQL's storage.
# Measured in deploy/gcp/gke/README.md "Cost". A stopped Cloud SQL instance, unlike RDS, does
# not start itself again after 7 days.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: pause.sh --project <p> --location <region|zone> --prefix <name_prefix> [--up|--status] [options]
  --project          Google Cloud project (Terraform's project_id)
  --location         the cluster's location: its region, or its zone for a zonal cluster
  --prefix           Terraform's name_prefix
  --up               resume: Cloud SQL, system pool, workspace pool autoscaler, CP
  --status           print what is running and what still bills; change nothing
  --stop-workspaces  scale the running workspaces to 0 (their sessions end); without it a
                     running workspace makes the pause refuse
  --keep-db          leave Cloud SQL running (it is stopped by default)
  --system-nodes N   (--up) system nodes per zone when no record exists (Terraform's system_node_count)
  --workspace-min N  (--up) autoscaling minimum per zone when no record exists (default 0)
  --workspace-max N  (--up) autoscaling maximum per zone when no record exists (default 4)
  --wait SEC         how long to wait for workspace pods to go away (default 900)
  --yes              actually do it (without this, the pause only prints the plan)
  --dry-run          print the writes without making them (reads still run)
EOF
}

PROJECT=""; LOCATION=""; PREFIX=""; MODE=down; STOP_WS=0; KEEP_DB=0; AF_YES=0; AF_DRY=0
SYSTEM_NODES=""; WS_MIN=""; WS_MAX=""; WAIT=900
while [ $# -gt 0 ]; do
  case "$1" in
    --project)         PROJECT="${2:?--project needs a value}"; shift ;;
    --location)        LOCATION="${2:?--location needs a value}"; shift ;;
    --prefix)          PREFIX="${2:?--prefix needs a value}"; shift ;;
    --up)              MODE=up ;;
    --status)          MODE=status ;;
    --stop-workspaces) STOP_WS=1 ;;
    --keep-db)         KEEP_DB=1 ;;
    --system-nodes)    SYSTEM_NODES="${2:?--system-nodes needs a value}"; shift ;;
    --workspace-min)   WS_MIN="${2:?--workspace-min needs a value}"; shift ;;
    --workspace-max)   WS_MAX="${2:?--workspace-max needs a value}"; shift ;;
    --wait)            WAIT="${2:?--wait needs a value}"; shift ;;
    --yes)             AF_YES=1 ;;
    --dry-run)         AF_DRY=1 ;;
    -h|--help)         usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROJECT" ] || [ -z "$LOCATION" ] || [ -z "$PREFIX" ]; then usage; exit 2; fi
for v in "$SYSTEM_NODES" "$WS_MIN" "$WS_MAX" "$WAIT"; do
  case "$v" in *[!0-9]*) echo "ERROR: counts and --wait must be whole numbers (got '$v')" >&2; exit 2 ;; esac
done

CLUSTER="$PREFIX-gke"; SQL="$PREFIX-pg"; CP_NS="$PREFIX-cp"; WS_NS="$PREFIX-ws"
WS_POOL=workspace; SYS_POOL=system
CTX="gke_${PROJECT}_${LOCATION}_${CLUSTER}"
ANN="agent-fleet.io/pause"
GC=(gcloud --project "$PROJECT")
K=(kubectl --context "$CTX")

# run — every write goes through here. Reads call out directly.
run() {
  if [ "$AF_DRY" = 1 ]; then echo "DRY: $*"; return 0; fi
  "$@"
}

# confirm <one-line description> — nothing irreversible without --yes; with a terminal,
# typing the cluster's name shows which deployment is aimed at.
confirm() {
  echo ""
  echo "⚠️  $1"
  echo "    cluster: $CLUSTER  (project=$PROJECT location=$LOCATION)"
  if [ "$AF_YES" != 1 ]; then
    echo "    → add --yes to actually run it (nothing was done)"
    return 1
  fi
  if [ -t 0 ]; then
    local typed=""
    printf '    confirm by typing the cluster name (%s): ' "$CLUSTER"
    read -r typed
    [ "$typed" = "$CLUSTER" ] || { echo "    mismatch — aborted"; return 1; }
  fi
  return 0
}

if ! "${K[@]}" get namespace "$CP_NS" >/dev/null 2>&1; then
  echo "ERROR: kubectl cannot reach namespace $CP_NS in context $CTX." >&2
  echo "       Run: gcloud container clusters get-credentials $CLUSTER --project $PROJECT --location $LOCATION" >&2
  exit 1
fi

cp_replicas() { "${K[@]}" -n "$CP_NS" get deployment af-cp -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "?"; }
cp_ready()    { "${K[@]}" -n "$CP_NS" get deployment af-cp -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true; }
ws_pods()     { "${K[@]}" -n "$WS_NS" get pods -o name 2>/dev/null || true; }
pool_nodes()  { "${K[@]}" get nodes -l "agent-fleet.io/pool=$1" -o name 2>/dev/null | grep -c . || true; }

# pool_field <pool> <gcloud --format value expression>
pool_field() {
  "${GC[@]}" container node-pools describe "$1" --cluster "$CLUSTER" --location "$LOCATION" \
    --format="value($2)" 2>/dev/null || true
}
autoscaling_on() { [ "$(pool_field "$WS_POOL" autoscaling.enabled)" = True ]; }

sql_policy() { "${GC[@]}" sql instances describe "$SQL" --format='value(settings.activationPolicy)' 2>/dev/null || true; }
sql_state()  { "${GC[@]}" sql instances describe "$SQL" --format='value(state)' 2>/dev/null || true; }

# recorded <name> — an annotation of the CP namespace, empty when absent.
recorded() {
  "${K[@]}" get namespace "$CP_NS" -o jsonpath="{.metadata.annotations.agent-fleet\\.io/pause-$1}" 2>/dev/null || true
}
record() { run "${K[@]}" annotate namespace "$CP_NS" --overwrite "$ANN-$1=$2" >/dev/null; }

# wait_ws_gone — poll until the workspace namespace has no pod. `kubectl wait --for=delete`
# with no pod left errors out on some versions, and one-shot erase pods count too.
wait_ws_gone() {
  local deadline left
  deadline=$(( $(date +%s) + WAIT ))
  while :; do
    left="$(ws_pods | tr '\n' ' ')"
    [ -z "${left// /}" ] && return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "ERROR: workspace pods still present after ${WAIT}s: $left" >&2
      return 1
    fi
    sleep 10
  done
}

status() {
  local pods np sp
  pods="$(ws_pods | wc -l | tr -d ' ')"
  echo "==> $CLUSTER (project=$PROJECT location=$LOCATION)"
  echo "    control plane      : desired=$(cp_replicas) ready=$(cp_ready)"
  echo "    workspace pods     : $pods"
  echo "    system nodes       : $(pool_nodes "$SYS_POOL")"
  echo "    workspace nodes    : $(pool_nodes "$WS_POOL") (autoscaling: $(autoscaling_on && echo on || echo off))"
  echo "    cloud sql ($SQL) : policy=$(sql_policy) state=$(sql_state)"
  np="$(recorded system-nodes)"
  if [ -n "$np" ]; then
    echo "    pause record       : system nodes/zone=$np workspace min/max=$(recorded workspace-min)/$(recorded workspace-max)"
  fi
  if [ "$(cp_replicas)" = 0 ]; then
    [ "$(sql_policy)" != ALWAYS ] || {
      echo ""; echo "    NOTE: Cloud SQL is running while the control plane is paused; pause.sh stops it."; }
    sp="$(pool_nodes "$WS_POOL")"
    [ "${sp:-0}" = 0 ] || {
      echo ""; echo "    WARN: $sp workspace node(s) up with no control plane. If the system pool is at 0 the"
      echo "          autoscaler started them for kube-system; run pause.sh again."; }
  fi
  echo ""
  echo "    still billing while paused (only deletion stops these): the GKE management fee, the"
  echo "    load balancer's forwarding rule, the Private Service Connect endpoint, Cloud NAT and its"
  echo "    reserved address, persistent disks (workspace claims, the CP's disk) and Cloud SQL storage."
}

case "$MODE" in
  status)
    status
    exit 0
    ;;

  up)
    echo "==> resuming $CLUSTER"
    # Cloud SQL first: a CP that comes up before it answers 500 until it is reachable.
    if [ "$(sql_policy)" = NEVER ]; then
      echo "==> starting Cloud SQL $SQL"
      run "${GC[@]}" sql instances patch "$SQL" --activation-policy=ALWAYS --quiet
    fi

    if [ "$(pool_nodes "$SYS_POOL")" = 0 ]; then
      n="${SYSTEM_NODES:-$(recorded system-nodes)}"
      if [ -z "$n" ]; then
        echo "ERROR: no pause record on namespace $CP_NS; pass --system-nodes <N> (Terraform's system_node_count)." >&2
        exit 1
      fi
      echo "==> system pool to $n node(s) per zone"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$SYS_POOL" --num-nodes "$n" \
        --location "$LOCATION" --quiet
      if [ "$AF_DRY" != 1 ]; then
        "${K[@]}" wait --for=condition=Ready node -l "agent-fleet.io/pool=$SYS_POOL" --timeout=600s
      fi
    fi

    if ! autoscaling_on; then
      mn="${WS_MIN:-$(recorded workspace-min)}"; mn="${mn:-0}"
      mx="${WS_MAX:-$(recorded workspace-max)}"; mx="${mx:-4}"
      echo "==> workspace pool autoscaling back on ($mn-$mx per zone)"
      run "${GC[@]}" container node-pools update "$WS_POOL" --cluster "$CLUSTER" --location "$LOCATION" \
        --enable-autoscaling --min-nodes "$mn" --max-nodes "$mx" --quiet
    fi

    echo "==> starting the control plane"
    run "${K[@]}" -n "$CP_NS" scale deployment/af-cp --replicas=1
    if [ "$AF_DRY" != 1 ]; then
      "${K[@]}" -n "$CP_NS" rollout status deployment/af-cp --timeout=600s
    fi
    for a in system-nodes workspace-min workspace-max; do
      [ -z "$(recorded "$a")" ] || run "${K[@]}" annotate namespace "$CP_NS" "$ANN-$a-" >/dev/null
    done
    cat <<EOF

==> up: users start their own workspaces (each wakes a workspace node, about 2-5 min the first time)
EOF
    ;;

  down)
    status
    pods="$(ws_pods | tr '\n' ' ')"
    if ! confirm "pause $CLUSTER (the control plane stops responding${pods:+; running workspaces are stopped with --stop-workspaces})"; then
      echo ""
      dbstep=" -> Cloud SQL stopped"; [ "$KEEP_DB" != 1 ] || dbstep=""
      echo "plan: ${pods:+workspaces to 0 -> }CP to 0 -> workspace pool 0 -> system pool 0$dbstep"
      exit 0
    fi
    if [ -n "${pods// /}" ] && [ "$STOP_WS" != 1 ]; then
      echo "ERROR: workspace pods are running: $pods" >&2
      echo "       Stop them in the Console, or pass --stop-workspaces (their sessions end)." >&2
      exit 1
    fi

    # Remember the sizes before touching anything. A pool already at 0 keeps the earlier record.
    if [ -n "$SYSTEM_NODES" ]; then
      record system-nodes "$SYSTEM_NODES"
    else
      total="$(pool_nodes "$SYS_POOL")"
      if [ "${total:-0}" -gt 0 ]; then
        zones="$(pool_field "$SYS_POOL" locations | tr ';' '\n' | grep -c . || true)"
        [ "${zones:-0}" -ge 1 ] || zones=1
        per=$(( total / zones )); [ "$per" -ge 1 ] || per=1
        record system-nodes "$per"
      fi
    fi
    if autoscaling_on; then
      record workspace-min "$(pool_field "$WS_POOL" autoscaling.minNodeCount | sed 's/^$/0/')"
      record workspace-max "$(pool_field "$WS_POOL" autoscaling.maxNodeCount)"
    fi

    echo "==> stopping the control plane"
    run "${K[@]}" -n "$CP_NS" scale deployment/af-cp --replicas=0
    if [ "$AF_DRY" != 1 ]; then
      "${K[@]}" -n "$CP_NS" wait --for=delete pod -l app.kubernetes.io/name=af-cp --timeout=300s
    fi

    # Only now: with the CP gone nothing recreates a workspace pod behind the scale-down.
    if [ -n "${pods// /}" ]; then
      echo "==> stopping workspaces"
      run "${K[@]}" -n "$WS_NS" scale statefulset --all --replicas=0
    fi
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for workspace pods to go (up to ${WAIT}s)"
      wait_ws_gone
    fi

    # Autoscaling off before the system pool goes: see the header.
    if autoscaling_on; then
      echo "==> workspace pool: autoscaling off"
      run "${GC[@]}" container node-pools update "$WS_POOL" --cluster "$CLUSTER" --location "$LOCATION" \
        --no-enable-autoscaling --quiet
    fi
    if [ "$(pool_nodes "$WS_POOL")" != 0 ]; then
      echo "==> workspace pool to 0"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$WS_POOL" --num-nodes 0 \
        --location "$LOCATION" --quiet
    fi
    if [ "$(pool_nodes "$SYS_POOL")" != 0 ]; then
      echo "==> system pool to 0"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$SYS_POOL" --num-nodes 0 \
        --location "$LOCATION" --quiet
    fi

    # Last: the CP is gone, so nothing is connected to it.
    dbnote="Cloud SQL storage"
    if [ "$KEEP_DB" = 1 ]; then
      dbnote="Cloud SQL (kept running: --keep-db)"
    elif [ "$(sql_policy)" = ALWAYS ]; then
      echo "==> stopping Cloud SQL $SQL"
      run "${GC[@]}" sql instances patch "$SQL" --activation-policy=NEVER --quiet
    fi
    cat <<EOF

==> paused: $CLUSTER's Console stops responding
    resume:  deploy/gcp/gke/pause.sh --project $PROJECT --location $LOCATION --prefix $PREFIX --up
    remaining cost: the GKE management fee, load balancer forwarding rule, Private Service Connect
    endpoint, Cloud NAT and its address, persistent disks, $dbnote. For close to zero, tear down
    (runbook "Tearing down").
EOF
    ;;
esac

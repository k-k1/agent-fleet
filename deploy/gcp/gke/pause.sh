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
# and removed by --up once the CP is back. The sizes are the pools' managed instance group
# sizes (what `resize` sets), not a count of registered nodes, which a half-finished resize
# makes too small. A namespace survives `kubectl apply -k`; an existing record is never
# overwritten, so a pause that is run again after an interruption keeps the original sizes
# (--system-nodes replaces it on purpose). While paused,
# `terraform plan` shows the workspace pool's autoscaling and the system pool's size as drift;
# `--up` closes it. Without a record (the namespace was deleted), --up needs --system-nodes.
#
# ## Reads that decide a write
#
# "Could not read" is not "nothing there": a failed `get pods` must not pass as "no workspace
# running". Every read that feeds a write returns its exit status and the script stops before
# the first write; only --status prints `?` and carries on. The context is also checked
# against the cluster's endpoint and CA, since its name alone says nothing about where it points.
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

# Polling knobs for the tests; an operator has no reason to touch them.
POLL="${AF_PAUSE_POLL:-10}"; READY_TIMEOUT="${AF_PAUSE_READY_TIMEOUT:-600}"
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

err() { echo "ERROR: $*" >&2; }

# verify_target — the context's name proves nothing (a copied or hand-edited kubeconfig entry
# can carry the standard name and point at another cluster, while gcloud below addresses this
# one). The cluster's CA and endpoint, as GKE reports them, must be the ones the context uses.
verify_target() {
  local ep pep ca srv kca host
  ep="$("${GC[@]}" container clusters describe "$CLUSTER" --location "$LOCATION" --format='value(endpoint)')" || { err "cannot describe cluster $CLUSTER"; return 1; }
  pep="$("${GC[@]}" container clusters describe "$CLUSTER" --location "$LOCATION" --format='value(privateClusterConfig.privateEndpoint)')" || return 1
  ca="$("${GC[@]}" container clusters describe "$CLUSTER" --location "$LOCATION" --format='value(masterAuth.clusterCaCertificate)')" || return 1
  srv="$("${K[@]}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')" || { err "kubectl context $CTX not found"; return 1; }
  kca="$("${K[@]}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')" || return 1
  host="${srv#*://}"; host="${host%%[:/]*}"
  if [ -z "$ca" ] || [ "${ca//[[:space:]]/}" != "${kca//[[:space:]]/}" ] || { [ "$host" != "$ep" ] && [ "$host" != "$pep" ]; }; then
    err "context $CTX does not point at cluster $CLUSTER (server '$srv' vs endpoint '$ep'; the CA must match too)."
    err "Run: gcloud container clusters get-credentials $CLUSTER --project $PROJECT --location $LOCATION"
    return 1
  fi
}
verify_target || exit 1
if ! "${K[@]}" get namespace "$CP_NS" >/dev/null 2>&1; then
  err "kubectl cannot reach namespace $CP_NS in context $CTX."
  err "Run: gcloud container clusters get-credentials $CLUSTER --project $PROJECT --location $LOCATION"
  exit 1
fi

# Reads: stdout is the answer, a non-zero status is "could not read" (never an empty success).
cp_replicas() {
  local v
  v="$("${K[@]}" -n "$CP_NS" get deployment af-cp -o jsonpath='{.spec.replicas}')" && [ -n "$v" ] || { err "cannot read deployment af-cp"; return 1; }
  echo "$v"
}
cp_ready() { "${K[@]}" -n "$CP_NS" get deployment af-cp -o jsonpath='{.status.readyReplicas}'; }
ws_pods() { "${K[@]}" -n "$WS_NS" get pods -o name || { err "cannot list pods in $WS_NS"; return 1; }; }
pool_nodes() {
  local out
  out="$("${K[@]}" get nodes -l "agent-fleet.io/pool=$1" -o name)" || { err "cannot list $1 nodes"; return 1; }
  if [ -z "$out" ]; then echo 0; else printf '%s\n' "$out" | wc -l | tr -d ' '; fi
}
pool_ready() {
  local out
  out="$("${K[@]}" get nodes -l "agent-fleet.io/pool=$1" -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}')" || return 1
  printf '%s\n' "$out" | grep -c '^True$' || true
}
pool_field() {  # <pool> <gcloud value() expression>
  "${GC[@]}" container node-pools describe "$1" --cluster "$CLUSTER" --location "$LOCATION" \
    --format="value($2)" || { err "cannot describe node pool $1"; return 1; }
}
# pool_sizes <pool> — the managed instance groups' target size, one line per zone: the number
# `resize --num-nodes` sets, and the one to restore. Registered nodes are not counted: a
# half-finished resize leaves too few of them.
pool_sizes() {
  local urls u z n v
  urls="$(pool_field "$1" instanceGroupUrls)" || return 1
  [ -n "$urls" ] || { err "node pool $1 reports no instance groups"; return 1; }
  for u in ${urls//;/ }; do
    z="${u##*/zones/}"; z="${z%%/*}"; n="${u##*/}"
    v="$("${GC[@]}" compute instance-groups managed describe "$n" --zone "$z" --format='value(targetSize)')" \
      || { err "cannot read the size of $n"; return 1; }
    case "$v" in ''|*[!0-9]*) err "unreadable size '$v' for $n"; return 1 ;; esac
    echo "$v"
  done
}
ws_autoscaling() {
  local v
  v="$(pool_field "$WS_POOL" autoscaling.enabled)" || return 1
  if [ "$v" = True ]; then echo 1; else echo 0; fi
}
sql_policy() {
  local v
  v="$("${GC[@]}" sql instances describe "$SQL" --format='value(settings.activationPolicy)')" || { err "cannot describe Cloud SQL $SQL"; return 1; }
  case "$v" in ALWAYS|NEVER) echo "$v" ;; *) err "Cloud SQL $SQL: unexpected activation policy '$v'"; return 1 ;; esac
}
sql_state() { "${GC[@]}" sql instances describe "$SQL" --format='value(state)'; }
recorded() {  # <name> — empty when absent
  "${K[@]}" get namespace "$CP_NS" -o jsonpath="{.metadata.annotations.agent-fleet\\.io/pause-$1}" \
    || { err "cannot read the pause record on $CP_NS"; return 1; }
}
all_equal() { local n="$1" x; shift; for x in "$@"; do [ "$x" = "$n" ] || return 1; done; }
any_nonzero() { local x; for x in "$@"; do [ "$x" = 0 ] || return 0; done; return 1; }

# wait_ws_gone — poll until the workspace namespace has no pod (one-shot erase pods count).
wait_ws_gone() {
  local deadline left
  deadline=$(( $(date +%s) + WAIT ))
  while :; do
    left="$(ws_pods)" || return 1
    left="$(printf '%s' "$left" | tr '\n' ' ')"
    [ -z "${left// /}" ] && return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then
      err "workspace pods still present after ${WAIT}s: $left"
      return 1
    fi
    sleep "$POLL"
  done
}
# wait_ready <pool> <count> — until that many nodes are Ready (kubectl wait passes on fewer).
wait_ready() {
  local deadline got
  deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while :; do
    got="$(pool_ready "$1")" || return 1
    [ "$got" -ge "$2" ] && return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then err "only $got of $2 $1 nodes Ready after ${READY_TIMEOUT}s"; return 1; fi
    sleep "$POLL"
  done
}

status() {
  local t
  try() { "$@" 2>/dev/null || echo '?'; }
  echo "==> $CLUSTER (project=$PROJECT location=$LOCATION)"
  echo "    control plane      : desired=$(try cp_replicas) ready=$(try cp_ready)"
  echo "    workspace pods     : $(try ws_pods | grep -c . || true)"
  echo "    system nodes       : $(try pool_nodes "$SYS_POOL")"
  echo "    workspace nodes    : $(try pool_nodes "$WS_POOL") (autoscaling: $(try ws_autoscaling | sed 's/^1$/on/;s/^0$/off/'))"
  echo "    cloud sql ($SQL) : policy=$(try sql_policy) state=$(try sql_state)"
  t="$(try recorded system-nodes)"
  if [ -n "$t" ] && [ "$t" != '?' ]; then
    echo "    pause record       : system nodes/zone=$t workspace min/max=$(try recorded workspace-min)/$(try recorded workspace-max)"
  fi
  if [ "$(try cp_replicas)" = 0 ]; then
    [ "$(try sql_policy)" != ALWAYS ] || {
      echo ""; echo "    NOTE: Cloud SQL is running while the control plane is paused; pause.sh stops it."; }
    t="$(try pool_nodes "$WS_POOL")"
    [ "$t" = 0 ] || [ "$t" = '?' ] || {
      echo ""; echo "    WARN: $t workspace node(s) up with no control plane. If the system pool is at 0 the"
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
    # All reads first: nothing is started on a guess.
    pol="$(sql_policy)"
    sysrec="$(recorded system-nodes)"; wsmin="$(recorded workspace-min)"; wsmax="$(recorded workspace-max)"
    n="${SYSTEM_NODES:-$sysrec}"
    sizes="$(pool_sizes "$SYS_POOL")"
    mapfile -t sz <<< "$sizes"
    AUTO="$(ws_autoscaling)"
    if [ -z "$n" ]; then
      if ! all_equal "${sz[0]}" "${sz[@]}"; then
        err "system pool sizes differ per zone (${sz[*]}) and there is no pause record; pass --system-nodes <N>."
        exit 1
      fi
      if [ "${sz[0]}" = 0 ]; then
        err "no pause record on namespace $CP_NS; pass --system-nodes <N> (Terraform's system_node_count)."
        exit 1
      fi
      n="${sz[0]}"
    fi

    # Cloud SQL first: a CP that comes up before it answers 500 until it is reachable.
    if [ "$pol" = NEVER ]; then
      echo "==> starting Cloud SQL $SQL"
      run "${GC[@]}" sql instances patch "$SQL" --activation-policy=ALWAYS --quiet
    fi

    # Whenever the pool is not at its recorded size, not only when it is empty: a resize that
    # stopped halfway leaves some nodes and must be finished.
    if ! all_equal "$n" "${sz[@]}"; then
      echo "==> system pool to $n node(s) per zone"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$SYS_POOL" --num-nodes "$n" \
        --location "$LOCATION" --quiet
    fi
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for $(( n * ${#sz[@]} )) system node(s) to be Ready"
      wait_ready "$SYS_POOL" $(( n * ${#sz[@]} ))
    fi

    if [ "$AUTO" = 0 ]; then
      mn="${WS_MIN:-$wsmin}"; mn="${mn:-0}"
      mx="${WS_MAX:-$wsmax}"; mx="${mx:-4}"
      echo "==> workspace pool autoscaling back on ($mn-$mx per zone)"
      run "${GC[@]}" container node-pools update "$WS_POOL" --cluster "$CLUSTER" --location "$LOCATION" \
        --enable-autoscaling --min-nodes "$mn" --max-nodes "$mx" --quiet
    fi

    echo "==> starting the control plane"
    run "${K[@]}" -n "$CP_NS" scale deployment/af-cp --replicas=1
    if [ "$AF_DRY" != 1 ]; then
      "${K[@]}" -n "$CP_NS" rollout status deployment/af-cp --timeout=600s
    fi
    # The record goes only now that everything it describes is back.
    if [ -n "$sysrec$wsmin$wsmax" ]; then
      run "${K[@]}" annotate namespace "$CP_NS" "$ANN-system-nodes-" "$ANN-workspace-min-" "$ANN-workspace-max-" >/dev/null
    fi
    cat <<EOF

==> up: users start their own workspaces (each wakes a workspace node, about 2-5 min the first time)
EOF
    ;;

  down)
    status
    # Every read that decides a write happens before the first write.
    pods="$(ws_pods)"; pods="$(printf '%s' "$pods" | tr '\n' ' ')"
    pol="$(sql_policy)"
    sysrec="$(recorded system-nodes)"; wsmin="$(recorded workspace-min)"; wsmax="$(recorded workspace-max)"
    sizes="$(pool_sizes "$SYS_POOL")"
    mapfile -t sz <<< "$sizes"
    AUTO="$(ws_autoscaling)"
    rec=()
    if [ -n "$SYSTEM_NODES" ]; then
      rec+=("$ANN-system-nodes=$SYSTEM_NODES")
    elif [ -z "$sysrec" ]; then
      if any_nonzero "${sz[@]}"; then
        if ! all_equal "${sz[0]}" "${sz[@]}"; then
          err "system pool sizes differ per zone (${sz[*]}) and nothing is recorded; pass --system-nodes <N> to say what --up restores."
          exit 1
        fi
        rec+=("$ANN-system-nodes=${sz[0]}")
      else
        echo "WARN: the system pool is already empty and nothing is recorded; --up will need --system-nodes." >&2
      fi
    fi
    if [ "$AUTO" = 1 ] && { [ -z "$wsmin" ] || [ -z "$wsmax" ]; }; then
      mn="$(pool_field "$WS_POOL" autoscaling.minNodeCount)"
      mx="$(pool_field "$WS_POOL" autoscaling.maxNodeCount)"
      mn="${mn:-0}"   # gcloud omits a zero
      case "$mn" in *[!0-9]*) err "cannot read the workspace pool's autoscaling minimum ('$mn')"; exit 1 ;; esac
      case "$mx" in ''|*[!0-9]*) err "cannot read the workspace pool's autoscaling maximum ('$mx')"; exit 1 ;; esac
      rec+=("$ANN-workspace-min=$mn" "$ANN-workspace-max=$mx")
    fi

    if ! confirm "pause $CLUSTER (the control plane stops responding${pods:+; running workspaces are stopped with --stop-workspaces})"; then
      echo ""
      dbstep=" -> Cloud SQL stopped"; [ "$KEEP_DB" != 1 ] || dbstep=""
      echo "plan: ${pods:+workspaces to 0 -> }CP to 0 -> workspace pool 0 -> system pool 0$dbstep"
      exit 0
    fi
    if [ -n "${pods// /}" ] && [ "$STOP_WS" != 1 ]; then
      err "workspace pods are running: $pods"
      echo "       Stop them in the Console, or pass --stop-workspaces (their sessions end)." >&2
      exit 1
    fi

    # One annotate: the record is complete or absent. An existing record is kept as it is.
    if [ "${#rec[@]}" -gt 0 ]; then
      run "${K[@]}" annotate namespace "$CP_NS" --overwrite "${rec[@]}" >/dev/null
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
    if [ "$AUTO" = 1 ]; then
      echo "==> workspace pool: autoscaling off"
      run "${GC[@]}" container node-pools update "$WS_POOL" --cluster "$CLUSTER" --location "$LOCATION" \
        --no-enable-autoscaling --quiet
    fi
    wsizes="$(pool_sizes "$WS_POOL")"
    mapfile -t wz <<< "$wsizes"
    if any_nonzero "${wz[@]}"; then
      echo "==> workspace pool to 0"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$WS_POOL" --num-nodes 0 \
        --location "$LOCATION" --quiet
    fi
    if any_nonzero "${sz[@]}"; then
      echo "==> system pool to 0"
      run "${GC[@]}" container clusters resize "$CLUSTER" --node-pool "$SYS_POOL" --num-nodes 0 \
        --location "$LOCATION" --quiet
    fi

    # Last: the CP is gone, so nothing is connected to it.
    dbnote="Cloud SQL storage"
    if [ "$KEEP_DB" = 1 ]; then
      dbnote="Cloud SQL (kept running: --keep-db)"
    elif [ "$pol" = ALWAYS ]; then
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

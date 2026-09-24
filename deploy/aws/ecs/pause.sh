#!/usr/bin/env bash
# Agent Fleet — put a deployment to sleep without tearing it down, and wake it again.
#
#   deploy/aws/ecs/pause.sh --profile <p> --region <r>          # scale down
#   deploy/aws/ecs/pause.sh --profile <p> --region <r> --up     # resume
#   deploy/aws/ecs/pause.sh --profile <p> --region <r> --status # what state it is in
#
# ## Why this is separate from teardown.sh
#
# Deleting everything is out of proportion to a period of not using it: coming back needs a
# rebuild and the ECR goes with it (20-platform's ECR is `EmptyOnDelete: true`). Scaling
# down makes the round trip in minutes and leaves home and the database's contents untouched.
#
# The saving is limited, though. The slots, the engines' GPU boxes, the CP's Fargate and the
# RDS instance's compute go away; NAT / ALB / EFS and RDS storage stay, because AWS has no
# "stop" for a NAT gateway or a load balancer, only delete (measured before RDS was stopped
# here: roughly $5.5 a day, $2.6 while paused). For "as close to $0 as possible", use
# teardown.sh.
#
# RDS has a trap of its own: AWS starts a stopped instance again by itself after 7 days, and
# says so only in an RDS event. `--status` flags a database that is running while the CP is
# not, and running this script again stops it (and restarts the 7-day clock).
#
# ## The order matters
#
# What puts a slot to sleep is the CP's sweeper (`AF_ECS_EC2_SLOT_SLEEP_SEC`, 15 min by
# default). Stopping the CP first therefore strands any running slot awake — the most
# painful way to get this wrong, since the most expensive thing keeps billing after you
# think you stopped it. The order is: stop the Workspaces, wait for the slots to fall
# asleep, then stop the CP. When there is no time to wait, `--fast` (the Workspaces are
# already stopped, so stopping the slots here does exactly what the CP would do 15 minutes
# later).
#
# The engines' GPU boxes are the same trap at ten times the price ($1.17/hour for a g6.xlarge):
# the CP's controller is the only thing that ends one (ADR 0077), so it is waited for too, and
# once the CP is down a final sweep terminates any box that is still alive — after that moment
# nothing else ever will. The database goes last on the way down and first on the way up: a CP
# that starts before it answers 500 until it is reachable.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: pause.sh --profile <p> --region <r> [--up|--status] [--fast] [--keep-db] [--yes] [--dry-run]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    ingress stack name (default af-ecs-ingress)
  --up       resume: bring the Control Plane back (users start their own workspaces)
  --status   print what is running and what still bills; change nothing
  --fast     stop the slots and end the engines' GPU boxes directly instead of waiting for the CP
  --keep-db  leave the RDS instance running (it is stopped by default)
  --yes      actually do it (without this, --down only prints the plan)
  --dry-run  print the writes without making them
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; MODE=down; FAST=0; KEEP_DB=0; AF_YES=0; AF_DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --up)      MODE=up ;;
    --status)  MODE=status ;;
    --fast)    FAST=1 ;;
    --keep-db) KEEP_DB=1 ;;
    --yes)     AF_YES=1 ;;
    --dry-run) AF_DRY=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then usage; exit 2; fi
export AF_YES AF_DRY   # read by af_confirm / af_run in env.sh
af_env_init "$PROFILE" "$REGION" "$STACK"
[ "$AF_LIVE" = 1 ] || { echo "ERROR: $STACK not found in $PROFILE/$REGION (use standup.sh for a deployment that was torn down)" >&2; exit 1; }
CLUSTER="$(af_cluster)"
CP_SERVICE="af-$AF_STACK_INGRESS-cp"

# ws_services — Workspace services with desired>0, i.e. the ones somebody is using.
# Leave list-services' paging to the CLI: adding --max-items truncates silently and reads
# as "we looked at everyone". describe-services takes at most 10 per call.
ws_services() {
  local arns names="" batch i got
  arns="$("${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null || true)"
  for a in $arns; do
    case "${a##*/}" in af-ws-*) names="$names ${a##*/}" ;; esac
  done
  # shellcheck disable=SC2086  # word splitting is the batching
  set -- $names
  while [ $# -gt 0 ]; do
    batch=""; i=0
    while [ $# -gt 0 ] && [ $i -lt 10 ]; do batch="$batch $1"; shift; i=$((i + 1)); done
    # shellcheck disable=SC2086,SC2016  # splitting is the batching; backticks are JMESPath literals
    got="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services $batch \
      --query 'services[?desiredCount>`0`].serviceName' --output text 2>/dev/null || true)"
    for g in $got; do echo "$g"; done
  done
}

slots() {  # <state> ...
  "${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-pool,Values=$CLUSTER" "Name=tag:af-role,Values=slot" \
      "Name=instance-state-name,Values=$(IFS=,; echo "$*")" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true
}

cp_counts() {
  "${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$CP_SERVICE" \
    --query 'services[0].[desiredCount,runningCount]' --output text 2>/dev/null || echo "? ?"
}

# engine_boxes — the GPU boxes the CP bought for its engine roles and that can still bill.
# Found by the tags the CP writes at CreateFleet (control-plane/engine_fleet.go), not through
# the engine stack: a box is a box whether or not its stack still resolves, and one nobody can
# find is exactly the one that bills all night.
engine_boxes() {
  "${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-pool,Values=$CLUSTER" "Name=tag:af-role,Values=engine-*" \
      "Name=instance-state-name,Values=pending,running" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true
}

# helper_services — the engine and speech services, whichever stacks this deployment has.
# The CP moves their desired count; nothing else does.
helper_services() {
  local s k
  if [ -n "${AF_STACK_ENGINES:-}" ]; then
    for k in LlmServiceName ImageServiceName; do
      s="$(af_stack_output "$AF_STACK_ENGINES" "$k")"
      [ -z "$s" ] || echo "$s"
    done
  fi
  if [ -n "${AF_STACK_TTS:-}" ]; then
    s="$(af_stack_output "$AF_STACK_TTS" TtsEcsService)"
    [ -z "$s" ] || echo "$s"
  fi
}

# helpers_up — the helper services whose desired count is above 0. The speech engine is
# Fargate: desired 1 with no CP to lower it is ~$90 a month on its own.
helpers_up() {
  local names
  names="$(helper_services | tr '\n' ' ')"
  [ -n "${names// /}" ] || return 0
  # shellcheck disable=SC2086,SC2016  # splitting is the list; backticks are JMESPath literals
  "${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services $names \
    --query 'services[?desiredCount>`0`].serviceName' --output text 2>/dev/null | tr '\t' ' ' || true
}

# engine_wait_bound — how long the CP may take to stop an idle engine: the longest idle time the
# engine stack declares plus one control interval's slack. The stack value is the default; an
# administrator's stored setting can lengthen it, and then the wait times out and says so.
engine_wait_bound() {
  local b=0 v k
  if [ -n "${AF_STACK_ENGINES:-}" ]; then
    for k in LlmIdleSec ImageIdleSec; do
      v="$(af_stack_param "$AF_STACK_ENGINES" "$k")"
      case "$v" in ''|*[!0-9]*) v=1800 ;; esac
      [ "$v" -le "$b" ] || b="$v"
    done
  fi
  echo $(( b + 300 ))
}

# The RDS instance, when 10-data has one. Same lookup as teardown.sh.
DB_ID=""
if af_stack_exists "$AF_STACK_DATA"; then
  DB_ID="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$AF_STACK_DATA" \
    --logical-resource-id Db --query 'StackResourceDetail.PhysicalResourceId' --output text 2>/dev/null || true)"
  case "$DB_ID" in None) DB_ID="" ;; esac
fi

db_status() {
  [ -n "$DB_ID" ] || return 0
  local v
  v="$("${AWS[@]}" rds describe-db-instances --db-instance-identifier "$DB_ID" \
    --query 'DBInstances[0].DBInstanceStatus' --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# db_wait_for <status> — poll until the instance reports it. `rds wait` has no "stopped"
# waiter, and a start issued while the instance is still `stopping` is refused outright.
db_wait_for() {
  local want="$1" deadline st
  deadline=$(( $(date +%s) + 1800 ))
  while :; do
    st="$(db_status)"
    [ "$st" = "$want" ] && return 0
    [ "$(date +%s)" -ge "$deadline" ] && { echo "WARN: $DB_ID is still '$st' after 30 min (waiting for '$want')"; return 1; }
    sleep 30
  done
}

status() {
  local running stopped ws boxes helpers db cp
  running="$(slots pending running | tr '\t' ' ')"; stopped="$(slots stopping stopped | tr '\t' ' ')"
  ws="$(ws_services | tr '\n' ' ')"
  boxes="$(engine_boxes | tr '\t' ' ')"; helpers="$(helpers_up)"
  db="$(db_status)"; cp="$(cp_counts | tr '\t' '/')"
  echo "==> $AF_FQDN (profile=$AF_PROFILE region=$AF_REGION)"
  echo "    control plane : desired/running = $cp"
  echo "    workspaces up : ${ws:-(none)}"
  echo "    slots running : ${running:-(none)}"
  echo "    slots stopped : ${stopped:-(none)}"
  echo "    engine boxes  : ${boxes:-(none)}"
  echo "    engine / tts services up : ${helpers:-(none)}"
  [ -z "$DB_ID" ] || echo "    database      : $DB_ID ${db:-?}"
  # A paused CP with a running database is almost always RDS's own 7-day restart.
  if [ -n "$DB_ID" ] && [ "${cp%%/*}" = 0 ] && [ "$db" = available ]; then
    echo ""
    echo "    WARN: the database is running while the control plane is paused. AWS restarts a"
    echo "          stopped instance after 7 days; run pause.sh again to stop it."
  fi
  if [ "${cp%%/*}" = 0 ] && [ -n "${boxes// /}" ]; then
    echo ""
    echo "    WARN: GPU boxes are running with no control plane to end them: $boxes"
    echo "          run pause.sh again (it terminates them), or --up to hand them back to the CP."
  fi
  echo ""
  echo "    fixed cost that remains while paused: NAT / ALB / EFS / RDS storage plus home's EBS"
  echo "    (AWS cannot stop a NAT gateway or a load balancer, only delete them)"
}

case "$MODE" in
  status)
    status
    exit 0
    ;;

  up)
    echo "==> resuming $AF_FQDN"
    # The database first: a CP that comes up before it answers 500 until it is reachable.
    db="$(db_status)"
    case "$db" in
      "") ;;
      available) ;;
      stopping|stopped)
        if [ "$db" = stopping ] && [ "$AF_DRY" != 1 ]; then
          echo "==> waiting for $DB_ID to finish stopping (a start is refused until it has)"
          db_wait_for stopped || exit 1
        fi
        echo "==> starting the database $DB_ID"
        af_run "${AWS[@]}" rds start-db-instance --db-instance-identifier "$DB_ID" >/dev/null
        if [ "$AF_DRY" != 1 ]; then
          echo "==> waiting for $DB_ID (typically 5-10 min)"
          "${AWS[@]}" rds wait db-instance-available --db-instance-identifier "$DB_ID"
        fi
        ;;
      *)
        if [ "$AF_DRY" != 1 ]; then
          echo "==> waiting for $DB_ID ($db)"
          "${AWS[@]}" rds wait db-instance-available --db-instance-identifier "$DB_ID"
        fi
        ;;
    esac
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 1 >/dev/null
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for $CP_SERVICE"
      "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
    fi
    cat <<EOF

==> up: https://$AF_FQDN
    Users start their own Workspaces (a stopped slot wakes up again on Start); an engine
    starts when something asks for it, cold (about 10 minutes for the first picture).
EOF
    ;;

  down)
    status
    ws="$(ws_services | tr '\n' ' ')"
    if ! af_confirm "scale $AF_FQDN down (running Workspaces are stopped, so their sessions die)"; then
      echo ""
      dbstep=""; [ -z "$DB_ID" ] || [ "$KEEP_DB" = 1 ] || dbstep=" -> database stopped"
      echo "plan: ${ws:-(no Workspace to stop)} -> slots asleep, engines idle -> CP desired 0 -> leftover engines ended$dbstep"
      exit 0
    fi

    for s in $ws; do
      echo "==> stopping workspace $s"
      af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null
    done

    if [ "$FAST" = 1 ]; then
      ids="$(slots pending running)"
      if [ -n "${ids// /}" ]; then
        echo "==> stopping slots directly: $ids"
        # shellcheck disable=SC2086
        af_run "${AWS[@]}" ec2 stop-instances --instance-ids $ids >/dev/null
      fi
      for s in $(helpers_up); do
        echo "==> scaling $s to 0"
        af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null
      done
      ids="$(engine_boxes)"
      if [ -n "${ids// /}" ]; then
        # Terminated, not stopped: that is how the CP ends one (engine_fleet.go). A box is
        # bought per demand and holds nothing that outlives it.
        echo "==> ending engine boxes directly: $ids"
        # shellcheck disable=SC2086
        af_run "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null
      fi
    elif [ "$AF_DRY" != 1 ]; then
      # Wait for the CP to put the slots to sleep and to stop its idle engines. The bound is the
      # longer of the two: the slot sleep time plus one sweep, or the engines' idle time plus one
      # control interval.
      sleep_s="$(af_stack_param "$AF_STACK_INGRESS" Ec2SlotSleepSec)"
      case "$sleep_s" in ''|*[!0-9]*) sleep_s=900 ;; esac
      bound=$(( sleep_s + 300 )); eb="$(engine_wait_bound)"; [ "$eb" -le "$bound" ] || bound="$eb"
      deadline=$(( $(date +%s) + bound ))
      echo "==> waiting for the CP to put the slots to sleep and stop idle engines (up to $(( bound / 60 )) min; --fast skips this)"
      while :; do
        ids="$(slots pending running) $(engine_boxes) $(helpers_up)"
        [ -z "${ids// /}" ] && break
        [ "$(date +%s)" -ge "$deadline" ] && {
          echo "WARN: still awake: $ids"
          echo "    Stopping the CP now would strand them. Stop them with --fast, or leave the CP running."
          echo "    (An engine whose mode is 'on' never idles: turn it off in the Console, or use --fast.)"
          exit 1
        }
        sleep 30
      done
      echo "==> slots are asleep and engines are idle"
    fi

    echo "==> stopping the control plane"
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 0 >/dev/null
    if [ "$AF_DRY" != 1 ]; then
      "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
    fi

    # The sweep. From here on nothing will ever end an engine box or lower a helper's desired
    # count, so whatever is still up is ended now: a box the CP bought between the wait and its
    # own stop is exactly the $28-a-day one nobody sees.
    for s in $(helpers_up); do
      echo "==> no CP left to lower it: scaling $s to 0"
      af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null
    done
    ids="$(engine_boxes)"
    if [ -n "${ids// /}" ]; then
      echo "==> no CP left to end them: terminating engine boxes $ids"
      # shellcheck disable=SC2086
      af_run "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null
    fi

    # The database last: the CP is gone, so nothing is connected to it.
    dbnote="RDS storage"
    if [ -n "$DB_ID" ]; then
      if [ "$KEEP_DB" = 1 ]; then
        dbnote="RDS (kept running: --keep-db)"
      elif [ "$(db_status)" = available ]; then
        echo "==> stopping the database $DB_ID (AWS starts it again by itself after 7 days)"
        af_run "${AWS[@]}" rds stop-db-instance --db-instance-identifier "$DB_ID" >/dev/null
      fi
    fi
    cat <<EOF

==> paused: $AF_FQDN stops responding
    resume:  deploy/aws/ecs/pause.sh --profile $AF_PROFILE --region $AF_REGION --up
    remaining cost: NAT / ALB / EFS / $dbnote plus home's EBS. For close to zero, use teardown.sh.
EOF
    ;;
esac

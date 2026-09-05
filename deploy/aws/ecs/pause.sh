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
# down makes the round trip in minutes and leaves home and the database untouched.
#
# The saving is limited, though. Only the EC2 slots' compute and the CP's Fargate go away;
# the fixed cost of NAT / ALB / RDS / EFS stays (measured: roughly $5.5 a day, $2.6 while
# paused — half of it is fixed). For "as close to $0 as possible", use teardown.sh.
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
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: pause.sh --profile <p> --region <r> [--up|--status] [--fast] [--yes] [--dry-run]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    ingress stack name (default af-ecs-ingress)
  --up       resume: bring the Control Plane back (users start their own workspaces)
  --status   print what is running and what still bills; change nothing
  --fast     stop the slot instances directly instead of waiting for the CP's sweeper
  --yes      actually do it (without this, --down only prints the plan)
  --dry-run  print the writes without making them
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; MODE=down; FAST=0; AF_YES=0; AF_DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --up)      MODE=up ;;
    --status)  MODE=status ;;
    --fast)    FAST=1 ;;
    --yes)     AF_YES=1 ;;
    --dry-run) AF_DRY=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
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

status() {
  local running stopped ws
  running="$(slots pending running | tr '\t' ' ')"; stopped="$(slots stopping stopped | tr '\t' ' ')"
  ws="$(ws_services | tr '\n' ' ')"
  echo "==> $AF_FQDN (profile=$AF_PROFILE region=$AF_REGION)"
  echo "    control plane : desired/running = $(cp_counts | tr '\t' '/')"
  echo "    workspaces up : ${ws:-(none)}"
  echo "    slots running : ${running:-(none)}"
  echo "    slots stopped : ${stopped:-(none)}"
  echo ""
  echo "    fixed cost that remains while paused: NAT / ALB / RDS / EFS plus home's EBS (scaling down does not remove them)"
}

case "$MODE" in
  status)
    status
    exit 0
    ;;

  up)
    echo "==> resuming $AF_FQDN"
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 1 >/dev/null
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for $CP_SERVICE"
      "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
    fi
    cat <<EOF

==> up: https://$AF_FQDN
    Users start their own Workspaces (a stopped slot wakes up again on Start).
EOF
    ;;

  down)
    status
    ws="$(ws_services | tr '\n' ' ')"
    if ! af_confirm "scale $AF_FQDN down (running Workspaces are stopped, so their sessions die)"; then
      echo ""
      echo "plan: ${ws:-(no Workspace to stop)} -> slots asleep -> CP desired 0"
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
    elif [ "$AF_DRY" != 1 ]; then
      # Wait for the CP's sweeper to put them to sleep. The bound is the sleep time plus
      # one sweep.
      sleep_s="$(af_stack_param "$AF_STACK_INGRESS" Ec2SlotSleepSec)"; : "${sleep_s:=900}"
      deadline=$(( $(date +%s) + sleep_s + 300 ))
      echo "==> waiting for the CP to put the slots to sleep (up to $(( (sleep_s + 300) / 60 )) min; --fast skips this)"
      while :; do
        ids="$(slots pending running)"
        [ -z "${ids// /}" ] && break
        [ "$(date +%s)" -ge "$deadline" ] && {
          echo "WARN: some slots are still awake: $ids"
          echo "    Stopping the CP now would strand them. Stop them with --fast, or leave the CP running."
          exit 1
        }
        sleep 30
      done
      echo "==> slots are asleep"
    fi

    echo "==> stopping the control plane"
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 0 >/dev/null
    cat <<EOF

==> paused: $AF_FQDN stops responding
    resume:  deploy/aws/ecs/pause.sh --profile $AF_PROFILE --region $AF_REGION --up
    remaining cost: NAT / ALB / RDS / EFS plus home's EBS. For close to zero, use teardown.sh.
EOF
    ;;
esac

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
# The saving is limited, though. The slots, the engines' GPU boxes, the CP's Fargate, the
# RDS instance's compute and the NAT gateway go away; ALB / EFS and RDS storage stay, because
# AWS has no "stop" for a load balancer, only delete (measured before RDS was stopped here:
# roughly $5.5 a day, $2.6 while paused). For "as close to $0 as possible", use teardown.sh.
#
# A NAT gateway has no "stop" either, so it is deleted through 00-network's NatEnabled
# parameter (never by hand: the stack would keep a dead physical id) and created again on
# `--up`. The egress address is retained by that stack, so the source IP does not change.
#
# RDS has a trap of its own: AWS starts a stopped instance again by itself after 7 days, and
# says so only in an RDS event (RDS-EVENT-0154). 10-data's re-stop Lambda catches that event
# and stops the instance again, but only while it carries the af-paused tag set here. Without
# that stack, `--status` flags a database that is running while the CP is not, and running
# this script again stops it (and restarts the 7-day clock).
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
# nothing else ever will. The database goes next to last on the way down and second on the way
# up: a CP that starts before it answers 500 until it is reachable. The NAT gateway is the very
# last to go (the sweep and the sleeping slots may still need to reach out) and the very first
# to come back (the CP's own image pull and logs go through it).
#
# The egress gateway (00-network EgressRoute=gateway, ADR 0122) carries workspace traffic only.
# It is scaled to zero through GatewayEnabled once the Workspaces are stopped and before the CP,
# whose agent endpoint takes the last of Squid's log. Its interface and Elastic IP stay, so the
# route target and the egress address are unchanged; the workspace route blackholes meanwhile,
# which is harmless only because no workspace runs. `--up` starts it with the NAT gateway and
# waits for it twice: up (interface attached, Squid and the agent running) before the CP, and
# its first policy, which needs the CP, before reporting the deployment up.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: pause.sh --profile <p> --region <r> [--up|--status] [--fast] [--keep-db] [--keep-nat] [--yes] [--dry-run]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    ingress stack name (default af-ecs-ingress)
  --up       resume: bring the Control Plane back (users start their own workspaces)
  --status   print what is running and what still bills; change nothing
  --fast     stop the slots and end the engines' GPU boxes directly instead of waiting for the CP
  --keep-db  leave the RDS instance running (it is stopped by default)
  --keep-nat leave the NAT gateway in place (it is deleted by default; --up recreates it)
  --yes      actually do it (without this, --down only prints the plan)
  --dry-run  print the writes without making them
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; MODE=down; FAST=0; KEEP_DB=0; KEEP_NAT=0; AF_YES=0; AF_DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --up)      MODE=up ;;
    --status)  MODE=status ;;
    --fast)    FAST=1 ;;
    --keep-db) KEEP_DB=1 ;;
    --keep-nat) KEEP_NAT=1 ;;
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

# ws_tasks_alive — Workspace tasks that have not reached STOPPED, whatever their service's desired
# count: a workspace its user stopped a moment ago, or one an earlier pause cut short, still has
# connections open through the egress gateway. Fails when it cannot tell.
ws_tasks_alive() {
  local st arns all="" batch i got
  for st in RUNNING STOPPED; do
    arns="$("${AWS[@]}" ecs list-tasks --cluster "$CLUSTER" --desired-status "$st" \
      --query taskArns --output text)" || return 1
    case "$arns" in None) arns="" ;; esac
    all="$all $arns"
  done
  # shellcheck disable=SC2086  # word splitting is the batching
  set -- $all
  while [ $# -gt 0 ]; do
    batch=""; i=0
    while [ $# -gt 0 ] && [ $i -lt 100 ]; do batch="$batch $1"; shift; i=$((i + 1)); done
    # shellcheck disable=SC2086
    got="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks $batch \
      --query "tasks[?starts_with(group,'service:af-ws-') && lastStatus!='STOPPED'].taskArn" --output text)" || return 1
    case "$got" in None) got="" ;; esac
    [ -z "$got" ] || echo "$got"
  done
}

# ws_tasks_wait — until no Workspace task is left (AF_WS_TASK_WAIT_SEC, default 600). Fails on
# timeout and when the tasks cannot be read.
ws_tasks_wait() {
  local left deadline
  deadline=$(( $(date +%s) + ${AF_WS_TASK_WAIT_SEC:-600} ))
  while :; do
    if ! left="$(ws_tasks_alive)"; then
      echo "    could not list the cluster's tasks" >&2
      return 1
    fi
    [ -n "${left//[[:space:]]/}" ] || return 0
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "    still running: $(printf '%s\n' "$left" | tr -s '[:space:]' ' ')" >&2
      return 1
    fi
    sleep "${AF_GATEWAY_POLL_SEC:-15}"
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

# db_tag <set|clear> — the af-paused marker 10-data's re-stop Lambda looks for. Tagging an
# instance is allowed in every state, stopped included.
db_tag() {
  [ -n "$DB_ID" ] || return 0
  local arn
  arn="$("${AWS[@]}" rds describe-db-instances --db-instance-identifier "$DB_ID" \
    --query 'DBInstances[0].DBInstanceArn' --output text 2>/dev/null || true)"
  # A tag that was not changed is a database nobody re-stops (set) or one the Lambda may
  # stop under a running CP (clear): never report it as done.
  case "$arn" in ""|None) echo "ERROR: could not read the ARN of $DB_ID, af-paused tag not changed" >&2; return 1 ;; esac
  case "$1" in
    set)   af_run "${AWS[@]}" rds add-tags-to-resource --resource-name "$arn" --tags Key=af-paused,Value=true >/dev/null ;;
    clear) af_run "${AWS[@]}" rds remove-tags-from-resource --resource-name "$arn" --tag-keys af-paused >/dev/null ;;
  esac
}

# nat_enabled — what 00-network's NatEnabled is on the live stack: true, false, or empty when
# there is no such stack. A stack older than the parameter has no NatEnabled and has its NAT.
nat_enabled() {
  af_stack_exists "$AF_STACK_NETWORK" || return 0
  local v
  v="$(af_stack_param "$AF_STACK_NETWORK" NatEnabled)"
  echo "${v:-true}"
}

# net_set <Key=value>... — switch the NAT gateway (NatEnabled) or the egress gateway
# (GatewayEnabled) through the stack that owns them. Every other parameter keeps its live value
# (`deploy` leaves what it is not given at UsePreviousValue). For the NAT the wait is the point:
# on `--up` nothing may start until the route exists again. Both default routes (private and
# workspace route table) follow NatEnabled, except that under EgressRoute=gateway the workspace
# route stays on GatewayEni whatever either switch says.
#
# This deploys the checkout's template, so a network stack older than it gains what it adds. The
# workspace subnets are harmless before the CP uses them, but the S3 endpoint is not: its policy
# refuses the own S3 of every workspace still in a private subnet (#2716). So a stack that has
# never had S3EndpointEnabled gets "false"; switching it on is the last step of the migration
# (README, NAT bullet), never a side effect of a pause.
net_set() {
  local extra=()
  if grep -q '^  S3EndpointEnabled:' "$HERE/cfn/00-network.yaml" &&
     af_stack_exists "$AF_STACK_NETWORK" &&
     [ -z "$(af_stack_param "$AF_STACK_NETWORK" S3EndpointEnabled)" ]; then
    extra=("S3EndpointEnabled=false")
  fi
  if [ "$AF_DRY" = 1 ]; then
    echo "DRY: cloudformation deploy --stack-name $AF_STACK_NETWORK --template-file $HERE/cfn/00-network.yaml --parameter-overrides $*${extra[*]:+ ${extra[*]}}"
    return 0
  fi
  # CAPABILITY_IAM: the template declares the egress gateway's role (only created with
  # EgressRoute=gateway, but CloudFormation asks for the capability regardless).
  af_cfn_deploy "$AF_STACK_NETWORK" "$HERE/cfn/00-network.yaml" --capabilities CAPABILITY_IAM \
    --parameter-overrides "$@" ${extra[@]+"${extra[@]}"} --no-fail-on-empty-changeset
}

# ws_subnets_check — say, before any workspace can start again, when the network has workspace
# subnets the CP has not been given: until update.sh runs, a started workspace lands in a private
# subnet (and, with the S3 endpoint on, its own S3 is refused).
ws_subnets_check() {
  local net ing ep
  net="$(af_stack_output "$AF_STACK_NETWORK" WorkspaceSubnets)"
  [ -n "$net" ] || return 0
  ing="$(af_stack_param "$AF_STACK_INGRESS" WorkspaceSubnets)"
  [ "$ing" != "$net" ] || return 0
  ep="$(af_stack_param "$AF_STACK_NETWORK" S3EndpointEnabled)"
  echo "WARNING: $AF_STACK_NETWORK has workspace subnets ($net) that the CP is not using" >&2
  echo "         (30-ingress WorkspaceSubnets='${ing}'). Run update.sh before anyone starts a" >&2
  echo "         workspace, then restart the running ones (README, NAT bullet)." >&2
  if [ "${ep:-true}" = true ]; then
    echo "         The S3 endpoint is ON: until then a workspace's own S3 is refused (AccessDenied)." >&2
    echo "         Rollback: deploy 00-network with S3EndpointEnabled=false." >&2
  fi
}

status() {
  local running stopped ws boxes helpers db cp nat gw gwi eni restop
  running="$(slots pending running | tr '\t' ' ')"; stopped="$(slots stopping stopped | tr '\t' ' ')"
  ws="$(ws_services | tr '\n' ' ')"
  boxes="$(engine_boxes | tr '\t' ' ')"; helpers="$(helpers_up)"
  db="$(db_status)"; cp="$(cp_counts | tr '\t' '/')"
  nat="$(nat_enabled)"
  gw="$(af_gateway_state "$AF_STACK_NETWORK")"; gwi=""
  if [ "$gw" = on ]; then
    eni="$(af_gateway_eni "$AF_STACK_NETWORK")"
    if [ -n "$eni" ]; then gwi="$(af_gateway_instance "$eni")" || gwi="its interface could not be read"; fi
  fi
  restop=""; af_stack_exists "$AF_STACK_DATA" && restop="$(af_stack_output "$AF_STACK_DATA" RdsRestopFunction)"
  echo "==> $AF_FQDN (profile=$AF_PROFILE region=$AF_REGION)"
  echo "    control plane : desired/running = $cp"
  echo "    workspaces up : ${ws:-(none)}"
  echo "    slots running : ${running:-(none)}"
  echo "    slots stopped : ${stopped:-(none)}"
  echo "    engine boxes  : ${boxes:-(none)}"
  echo "    engine / tts services up : ${helpers:-(none)}"
  [ -z "$DB_ID" ] || echo "    database      : $DB_ID ${db:-?}"
  [ -z "$nat" ] || echo "    nat gateway   : $([ "$nat" = true ] && echo present || echo deleted)"
  case "$gw" in
    on)  echo "    egress gateway: on (${gwi:-no instance has attached its interface yet})" ;;
    off) echo "    egress gateway: stopped (the workspace route blackholes until --up)" ;;
  esac
  # A paused CP with a running database is almost always RDS's own 7-day restart.
  if [ -n "$DB_ID" ] && [ "${cp%%/*}" = 0 ] && [ "$db" = available ]; then
    echo ""
    echo "    WARN: the database is running while the control plane is paused. AWS restarts a"
    echo "          stopped instance after 7 days; run pause.sh again to stop it."
    [ -z "$restop" ] || echo "          (10-data's re-stop Lambda $restop only acts on a database tagged af-paused.)"
  fi
  if [ "${cp%%/*}" = 0 ] && [ "$gw" = on ]; then
    echo ""
    echo "    WARN: the egress gateway is on while the control plane is paused (an earlier pause"
    echo "          stopped part way?). Run pause.sh again: it stops it."
  fi
  if [ "${cp%%/*}" = 0 ] && [ -n "${boxes// /}" ]; then
    echo ""
    echo "    WARN: GPU boxes are running with no control plane to end them: $boxes"
    echo "          run pause.sh again (it terminates them), or --up to hand them back to the CP."
  fi
  echo ""
  echo "    fixed cost that remains while paused: ALB / EFS / RDS storage plus home's EBS"
  [ "$nat" = true ] && echo "    and the NAT gateway (pause.sh deletes it unless --keep-nat)"
  [ -z "$gw" ] || echo "    and the egress gateway's Elastic IP (kept: it is the workspace egress address)"
  echo "    (AWS cannot stop a load balancer, only delete it)"
}

case "$MODE" in
  status)
    status
    exit 0
    ;;

  up)
    echo "==> resuming $AF_FQDN"
    # The NAT first: the CP's image pull, logs and secrets all go through it. The egress
    # gateway in the same deploy: it boots through its own public address, not the NAT, and
    # its boot (packages, the CP image) then overlaps the database's start.
    ups=()
    if [ "$(nat_enabled)" = false ]; then
      echo "==> recreating the NAT gateway (same egress address; typically 1-2 min)"
      ups+=(NatEnabled=true)
    fi
    gw="$(af_gateway_state "$AF_STACK_NETWORK")"
    if [ "$gw" = off ]; then
      echo "==> starting the egress gateway (same interface and egress address)"
      ups+=(GatewayEnabled=true)
    fi
    [ ${#ups[@]} -eq 0 ] || net_set "${ups[@]}"
    [ "$AF_DRY" = 1 ] || ws_subnets_check
    # The database next: a CP that comes up before it answers 500 until it is reachable.
    # The tag comes off before anything else touches it, so the re-stop Lambda cannot stop
    # the database under the CP that is about to start. A stop the Lambda issued just before
    # the tag went is already on its way and cannot be recalled, so wait for it to land and
    # read the status only afterwards (a database found stopping is started again below).
    # The wait must exceed the Lambda's stop call, which 10-data bounds at 8 s (connect 3 +
    # read 5, no SDK retries). After the CP is up the status is read once more.
    db_tag clear
    [ -z "$DB_ID" ] || [ "$AF_DRY" = 1 ] || sleep "${AF_TAG_GRACE_SEC:-20}"
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
    # No CP, no workspace: the gateway must be carrying traffic before the CP can start one.
    # Its first policy has to wait for the CP (below).
    if [ -n "$gw" ] && [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for the egress gateway: interface attached, Squid and its agent running (a boot takes a few minutes)"
      if ! af_gateway_wait "$AF_STACK_NETWORK" up; then
        echo "ERROR: the egress gateway did not come up; the control plane stays down so no workspace starts" >&2
        echo "       without egress. On the instance: /var/log/af-egress-gateway.log (README, \"Egress gateway\")." >&2
        echo "       Then run pause.sh --up again." >&2
        exit 1
      fi
    fi
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 1 >/dev/null
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for $CP_SERVICE"
      "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
    fi
    if [ -n "$DB_ID" ] && [ "$AF_DRY" != 1 ] && [ "$(db_status)" != available ]; then
      echo "ERROR: the control plane is up but $DB_ID is '$(db_status)' (a re-stop landed late?)." >&2
      echo "       Run pause.sh --up again: it starts the database, then the control plane." >&2
      exit 1
    fi
    # Until its first policy the gateway refuses every workspace connection (fail closed), so a
    # workspace started in this window has no egress; it is seconds once the CP answers.
    if [ -n "$gw" ] && [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for the egress gateway's first policy from the control plane"
      if ! af_gateway_wait "$AF_STACK_NETWORK" ready; then
        echo "ERROR: the control plane is up but the egress gateway has no policy from it: workspaces" >&2
        echo "       have no egress (it refuses everything). Check the agent (journalctl -u af-egress-agent" >&2
        echo "       on the instance, README \"Egress gateway\"), then run pause.sh --up again." >&2
        exit 1
      fi
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
      natstep=""; [ "$KEEP_NAT" = 1 ] || [ "$(nat_enabled)" != true ] || natstep=" -> NAT gateway deleted"
      gwstep=""; [ "$(af_gateway_state "$AF_STACK_NETWORK")" != on ] || gwstep=" -> egress gateway stopped"
      echo "plan: ${ws:-(no Workspace to stop)} -> slots asleep, engines idle$gwstep -> CP desired 0 -> leftover engines ended$dbstep$natstep"
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

    # The egress gateway before the CP: a tunnel is logged when it closes and the agent ships
    # the log to the CP, so the workspaces' last connections are recorded only while both are
    # up, and the scale-in destroys the instance's disk with whatever it had not shipped. So:
    # no workspace task left, then the CP has accepted the whole log, then the scale-in. Any
    # of these that cannot be confirmed stops here with the gateway and the CP still running.
    if [ "$(af_gateway_state "$AF_STACK_NETWORK")" = on ]; then
      if [ "$AF_DRY" != 1 ]; then
        echo "==> waiting until no Workspace task is left (the egress gateway must see their connections close)"
        if ! ws_tasks_wait; then
          echo "ERROR: Workspace tasks may still be running, so the egress gateway and the control plane are" >&2
          echo "       left running. Run pause.sh again once they have stopped." >&2
          exit 1
        fi
        echo "==> draining the egress gateway: Squid stopped, waiting until the CP has accepted all of its log"
        if ! af_gateway_drain "$AF_STACK_NETWORK"; then
          echo "ERROR: the CP has not accepted all of the egress gateway's log, so the gateway and the CP are" >&2
          echo "       left running (stopping it now would destroy those records). Check the agent" >&2
          echo "       (journalctl -u af-egress-agent on the instance) and run pause.sh again. To give the records" >&2
          echo "       up, deploy $AF_STACK_NETWORK with GatewayEnabled=false yourself." >&2
          exit 1
        fi
      fi
      echo "==> stopping the egress gateway through $AF_STACK_NETWORK (its interface and Elastic IP are kept)"
      net_set GatewayEnabled=false
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
    tagfail=""
    dbnote="RDS storage"
    if [ -n "$DB_ID" ]; then
      if [ "$KEEP_DB" = 1 ]; then
        dbnote="RDS (kept running: --keep-db)"
        # Kept on purpose, so a marker left by an earlier pause must not stop it again.
        db_tag clear || tagfail="clear"
      else
        # Tagged before the stop, so a forced start can never beat the tag. A failed tag still
        # stops the database (the bill comes first) and fails the run at the end.
        db_tag set || tagfail="set"
        if [ "$(db_status)" = available ]; then
          echo "==> stopping the database $DB_ID (AWS starts it again by itself after 7 days)"
          af_run "${AWS[@]}" rds stop-db-instance --db-instance-identifier "$DB_ID" >/dev/null
        fi
      fi
    fi

    # The NAT last: a failure here leaves a paused deployment that still bills the gateway,
    # not a running database.
    natnote=""
    if [ "$(nat_enabled)" = true ]; then
      if [ "$KEEP_NAT" = 1 ]; then
        natnote="NAT (kept: --keep-nat)"
      else
        echo "==> deleting the NAT gateway through $AF_STACK_NETWORK (the egress address is kept)"
        net_set NatEnabled=false
      fi
    fi
    cat <<EOF

==> paused: $AF_FQDN stops responding
    resume:  deploy/aws/ecs/pause.sh --profile $AF_PROFILE --region $AF_REGION --up
    remaining cost: ${natnote:+$natnote / }ALB / EFS / $dbnote plus home's EBS. For close to zero, use teardown.sh.
EOF
    case "$tagfail" in
      set)
        echo "ERROR: the af-paused tag on $DB_ID was not set (see above). Paused, but the database will" >&2
        echo "       not be stopped again if AWS starts it after 7 days. Run pause.sh again." >&2
        exit 1 ;;
      clear)
        echo "ERROR: the af-paused tag on $DB_ID was not removed (see above). It stays running (--keep-db)" >&2
        echo "       but the re-stop Lambda may stop it. Run pause.sh --keep-db again, or remove the tag." >&2
        exit 1 ;;
    esac
    ;;
esac

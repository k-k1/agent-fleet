#!/bin/bash
# How long does an ECS Managed Instances engine take to come up, and how long does the
# instance linger after desired goes back to 0? (ADR 0071, open question 2)
#
#   AWS_PROFILE=af-sandbox probe-managed-instances.sh up       # create engprobe.yaml
#   AWS_PROFILE=af-sandbox probe-managed-instances.sh measure  # desired 1 → ready → desired 0 → terminated
#   AWS_PROFILE=af-sandbox probe-managed-instances.sh down     # delete the stack
#
# The stack is a throwaway on a SHARED cluster: it names FARGATE/FARGATE_SPOT alongside its
# own capacity provider (the association API replaces the list) and leaves the default
# strategy empty. Delete it before any teardown of the platform stack.
#
# ⚠️ The box is found THROUGH THE TASK (describe-tasks → containerInstanceArn → ec2InstanceId),
# never through list-container-instances: on an ecs-ec2 deployment the slot pool is
# registered in the same cluster, and the first attempt at this script timed a slot box.
#
# Measured 2026-09-07 (c6a.large, ecs-managed-instances-standard-x86_64-20260827, the 297 MB
# CPU llama.cpp image, a 1.1 GB model fetched from HF at start):
#   desired 1 → task created +6 s → instance launched +10 s → pull started +32 s
#             → task RUNNING +68 s (pull 35 s) → model loaded and listening +109 s
#   desired 0 → tasks gone +10 s → instance shutting-down +79 s → terminated +93 s
set -u
STACK=${STACK:-af-ecs-engprobe}
CLUSTER=${CLUSTER:-af-af-ecs-platform}
HERE=$(cd "$(dirname "$0")" && pwd)
ts() { date +%T; }

case "${1:-}" in
up)
  aws cloudformation create-stack --stack-name "$STACK" --template-body "file://$HERE/engprobe.yaml" \
    --capabilities CAPABILITY_NAMED_IAM --query StackId --output text
  aws cloudformation wait stack-create-complete --stack-name "$STACK"
  aws cloudformation describe-stack-events --stack-name "$STACK" \
    --query 'StackEvents[?ResourceStatus==`CREATE_FAILED`].[LogicalResourceId,ResourceStatusReason]' --output text
  aws ecs describe-clusters --clusters "$CLUSTER" --query 'clusters[0].[capacityProviders,defaultCapacityProviderStrategy]' --output json
  ;;
down)
  aws cloudformation delete-stack --stack-name "$STACK"
  aws cloudformation wait stack-delete-complete --stack-name "$STACK" && echo "deleted"
  aws ecs describe-clusters --clusters "$CLUSTER" --query 'clusters[0].capacityProviders' --output json
  ;;
measure)
  S=$(aws cloudformation describe-stacks --stack-name "$STACK" --query 'Stacks[0].Outputs[?OutputKey==`ServiceName`].OutputValue' --output text)
  LG="/af/$STACK/engine"
  echo "$(ts) T0 desired->1 ($S)"; aws ecs update-service --cluster "$CLUSTER" --service "$S" --desired-count 1 --query 'service.desiredCount' --output text
  T0=$(date +%s); inst=""; seen_task=""; seen_run=""; st=""
  for i in $(seq 1 120); do
    now=$(( $(date +%s) - T0 ))
    task=$(aws ecs list-tasks --cluster "$CLUSTER" --service-name "$S" --query 'taskArns[0]' --output text 2>/dev/null)
    if [ -n "$task" ] && [ "$task" != "None" ]; then
      read -r st cr ps pe sa cia <<<"$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$task" \
        --query 'tasks[0].[lastStatus,createdAt,pullStartedAt,pullStoppedAt,startedAt,containerInstanceArn]' --output text)"
      [ -z "$seen_task" ] && { seen_task=1; echo "$(ts) +${now}s task created createdAt=$cr"; }
      if [ -z "$inst" ] && [ -n "$cia" ] && [ "$cia" != "None" ]; then
        inst=$(aws ecs describe-container-instances --cluster "$CLUSTER" --container-instances "$cia" --query 'containerInstances[0].ec2InstanceId' --output text)
        echo "$(ts) +${now}s placed on $inst: $(aws ec2 describe-instances --instance-ids "$inst" --query 'Reservations[0].Instances[0].[InstanceType,LaunchTime,ImageId]' --output text)"
      fi
      [ "$st" = "RUNNING" ] && [ -z "$seen_run" ] && { seen_run=1; echo "$(ts) +${now}s task RUNNING pullStarted=$ps pullStopped=$pe startedAt=$sa"; }
    fi
    l=$(aws logs filter-log-events --log-group-name "$LG" --filter-pattern '"model loaded"' --query 'events[0].timestamp' --output text 2>/dev/null)
    if [ -n "$l" ] && [ "$l" != "None" ]; then echo "$(ts) +${now}s MODEL LOADED (log ts $l)"; break; fi
    [ $((i % 6)) -eq 0 ] && echo "$(ts) +${now}s ... task=${st:-none}"
    sleep 10
  done
  [ -z "$inst" ] && { echo "no instance placed; service events:"; aws ecs describe-services --cluster "$CLUSTER" --services "$S" --query 'services[0].events[0:3].message' --output text; exit 1; }
  sleep 60
  echo "$(ts) T1 desired->0"; aws ecs update-service --cluster "$CLUSTER" --service "$S" --desired-count 0 --query 'service.desiredCount' --output text
  T1=$(date +%s); seen_t0=""
  for i in $(seq 1 180); do
    sleep 10; now=$(( $(date +%s) - T1 ))
    ntask=$(aws ecs list-tasks --cluster "$CLUSTER" --service-name "$S" --query 'length(taskArns)' --output text)
    est=$(aws ec2 describe-instances --instance-ids "$inst" --query 'Reservations[0].Instances[0].State.Name' --output text 2>/dev/null)
    [ "$ntask" = "0" ] && [ -z "$seen_t0" ] && { seen_t0=1; echo "$(ts) +${now}s tasks gone"; }
    [ "$est" != "running" ] && echo "$(ts) +${now}s ec2 state=$est"
    [ "$est" = "terminated" ] && break
    [ $((i % 6)) -eq 0 ] && echo "$(ts) +${now}s ... tasks=$ntask ec2=$est"
  done
  ;;
*)
  echo "usage: $0 up|measure|down" >&2; exit 2
  ;;
esac

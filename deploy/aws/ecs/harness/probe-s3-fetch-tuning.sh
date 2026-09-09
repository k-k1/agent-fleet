#!/usr/bin/env bash
# How fast can the fetch sidecar actually pull a model out of S3? (ADR 0072 open question 10,
# the third and cheapest of the remaining cold-start levers.)
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-s3-fetch-tuning.sh [--key llm/<file>.gguf]
#                                                     [--configs "label:concurrency:chunk,..."] [--print]
#
# ## Why
#
# After `useLocalStorage` the cold start is 267 s, and 115 of those are one `aws s3 cp` of an
# 18.5 GB GGUF — an effective 161 MB/s. Two measurements say that is not the ceiling:
#
#   - the SAME command in `probe-s3-mount.sh` did 210 MB/s on a box of the same type, so the
#     number moves with something other than S3;
#   - Mountpoint read the SAME object at 567 MB/s on that box, which puts the floor under the
#     network far above what the CLI is achieving.
#
# So the suspicion is the CLI's own defaults — `max_concurrent_requests` is 10 and
# `multipart_chunksize` is 8 MB — rather than the network or the disk. This measures that
# instead of assuming it, because assuming is what put "S3 is not the bottleneck" in the ADR.
#
# ## Method
#
# Every configuration downloads the SAME object on the SAME box in the SAME task, back to back,
# with the stock settings FIRST so a later run cannot be flattered by anything warm. The file is
# deleted between passes, and the destination is the instance store, which is not the limit
# (measured: it takes a mounted read at 567 MB/s without complaint).
#
# `nproc` is reported because the AWS CLI is Python and checksum work is CPU-bound at these
# rates: if the tuned configurations plateau, the next question is vCPU, not settings.
#
# ## Cost
#
# One GPU box, one 18.5 GB download per configuration — about 90 s each, so roughly $0.25 for
# the default three.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
KEY=""
# label:max_concurrent_requests:multipart_chunksize — "stock" means leave the CLI alone.
CONFIGS="stock:0:0,c20x16:20:16MB,c40x32:40:32MB,c64x64:64:64MB"
PRINT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?}"; shift ;;
    --key) KEY="${2:?}"; shift ;;
    --configs) CONFIGS="${2:?}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,38p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }
out() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text; }

BUCKET="$(out "$STACK" ModelsBucket)"
CP="$(out "$STACK" LlmCapacityProviderName)"
SERVICE="$(out "$STACK" LlmServiceName)"
CLUSTER="$("${AWS[@]}" cloudformation list-exports --query "Exports[?Name=='$PLATFORM_STACK-ClusterName'].Value" --output text)"
[ -n "$BUCKET" ] && [ -n "$CP" ] && [ -n "$CLUSTER" ] || { echo "missing coordinates" >&2; exit 1; }

if [ -z "$KEY" ]; then
  KEY="$("${AWS[@]}" s3api list-objects-v2 --bucket "$BUCKET" --prefix "llm/" \
    --query 'reverse(sort_by(Contents,&Size))[0].Key' --output text)"
fi
SZ="$("${AWS[@]}" s3api head-object --bucket "$BUCKET" --key "$KEY" --query ContentLength --output text)"

SVC_JSON="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query 'services[0]' --output json)"
SUBNETS="$(jq -r '.networkConfiguration.awsvpcConfiguration.subnets | join(",")' <<<"$SVC_JSON")"
SG="$(jq -r '.networkConfiguration.awsvpcConfiguration.securityGroups[0]' <<<"$SVC_JSON")"
TD_JSON="$("${AWS[@]}" ecs describe-task-definition --task-definition "$(jq -r .taskDefinition <<<"$SVC_JSON")" --query 'taskDefinition' --output json)"
EXEC_ROLE="$(jq -r '.executionRoleArn' <<<"$TD_JSON")"
TASK_ROLE="$(jq -r '.taskRoleArn' <<<"$TD_JSON")"
LOG_GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' <<<"$TD_JSON")"
CPU="$(jq -r '.cpu' <<<"$TD_JSON")"; MEM="$(jq -r '.memory' <<<"$TD_JSON")"

say "cluster=$CLUSTER cp=$CP bucket=$BUCKET key=$KEY size=$SZ configs=$CONFIGS"

# shellcheck disable=SC2016
PROBE_CMD='
echo "s3t: object $KEY is $SZ bytes; nproc=$(nproc); $(aws --version 2>&1)"
mkdir -p /work
IFS=,
for C in $CONFIGS; do
  IFS=" "
  L=$(echo "$C" | cut -d: -f1); N=$(echo "$C" | cut -d: -f2); CH=$(echo "$C" | cut -d: -f3)
  rm -f /root/.aws/config
  if [ "$N" != 0 ]; then
    mkdir -p /root/.aws
    printf "[default]\ns3 =\n  max_concurrent_requests = %s\n  multipart_chunksize = %s\n  max_queue_size = 10000\n" "$N" "$CH" > /root/.aws/config
  fi
  rm -f /work/blob
  t0=$(date +%s)
  aws s3 cp "s3://$BUCKET/$KEY" /work/blob --only-show-errors
  t=$(( $(date +%s) - t0 ))
  GOT=$(stat -c %s /work/blob 2>/dev/null || echo 0)
  if [ "$GOT" = "$SZ" ]; then
    echo "s3t: RESULT $L (concurrency=$N chunk=$CH) ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s"
  else
    echo "s3t: RESULT $L FAILED - got $GOT of $SZ bytes in ${t}s"
  fi
  rm -f /work/blob
  IFS=,
done
IFS=" "
echo "s3t: === done ==="
'

TASKDEF="$(jq -n --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg cmd "$PROBE_CMD" \
  --arg bucket "$BUCKET" --arg key "$KEY" --arg sz "$SZ" --arg cfg "$CONFIGS" \
  --arg cpu "$CPU" --arg mem "$MEM" --arg g "$LOG_GROUP" --arg r "$REGION" '
{
  family: "af-engprobe-s3tune",
  requiresCompatibilities: ["MANAGED_INSTANCES"],
  networkMode: "awsvpc",
  # The same cpu/memory the real llm task gets, because the sidecar has the box to itself while
  # it runs and a smaller share would measure a different machine than the one being tuned.
  cpu: $cpu, memory: $mem,
  executionRoleArn: $exec, taskRoleArn: $task,
  containerDefinitions: [
    {name: "probe", essential: true, image: "public.ecr.aws/aws-cli/aws-cli:latest",
     entryPoint: ["sh","-c"], command: [$cmd],
     environment: [{name:"BUCKET",value:$bucket},{name:"KEY",value:$key},{name:"SZ",value:$sz},{name:"CONFIGS",value:$cfg}],
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"s3tune"}}}
  ]
}')"

if [ "$PRINT" = 1 ]; then jq . <<<"$TASKDEF"; exit 0; fi

TD_ARN="$("${AWS[@]}" ecs register-task-definition --cli-input-json "$TASKDEF" --query 'taskDefinition.taskDefinitionArn' --output text)"
say "registered $TD_ARN"
T0=$(date +%s)
TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --task-definition "$TD_ARN" \
  --capacity-provider-strategy "capacityProvider=$CP,weight=1" \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SG],assignPublicIp=DISABLED}" \
  --query 'tasks[0].taskArn' --output text)"
say "task ${TASK##*/}"

while :; do
  sleep 30
  st="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].lastStatus' --output text)"
  [ "$st" = STOPPED ] && break
  [ $(( $(date +%s) - T0 )) -gt 2400 ] && { say "giving up after 40 minutes"; break; }
done
say "stopped after $(( $(date +%s) - T0 ))s — $("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].stoppedReason' --output text)"
# Read the whole window once: an incremental poll drops lines to CloudWatch ingestion lag
# (measured on probe-llm-mount-load.sh, where a RESULT went missing and looked like a failure).
sleep 15
"${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "s3tune" \
  --start-time "$((T0 * 1000))" --query 'events[].message' --output text 2>/dev/null \
  | tr '\t' '\n' | grep '^s3t:' || true

#!/usr/bin/env bash
# A faster S3 client, and how much of the VRAM load is actually disk (ADR 0072 open question 10).
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-fetch-client.sh [--key llm/<file>.gguf] [--s5cmd-version 2.2.2] [--print]
#
# ## Two questions, one box
#
# **1. Is the AWS CLI the ceiling?** `probe-s3-fetch-tuning.sh` showed `aws s3 cp` plateauing at
# 218 MB/s no matter how much concurrency it is given, while Mountpoint read the same object off
# the same class of box at 567 MB/s. That points at the client — the CLI is Python, and TLS plus
# checksums are CPU-bound at these rates. s5cmd is the Go one. Unlike Mountpoint it would live in
# the FETCH SIDECAR's image, so it touches neither the engine image nor the fast local swap,
# which is what made Mountpoint not worth it.
#
# **2. Would overlapping the copy and the load help?** The cold start is copy (115 s) then load
# (98 s), strictly serial, and the obvious idea is to pipeline them. Before building anything,
# this bounds the prize: `dd iflag=direct` reads the finished file back off the instance store,
# which is the DISK part of those 98 seconds. Whatever is left is GGUF parsing and the transfer
# into VRAM, and no amount of overlapping touches it.
#
#   overlap can save AT MOST the disk-read time, and only if the reader could follow the writer.
#
# ⚠️ It could not, as things stand: `aws s3 cp` writes multipart ranges OUT OF ORDER, so a
# follower would read holes, and llama.cpp needs random access to a complete GGUF rather than a
# stream. That is why this measures the ceiling first — if the disk part is small the idea is
# dead on arithmetic and nobody has to find the ordering problem the hard way.
#
# `iflag=direct` is the whole point of the dd: without it the 18.5 GB would come partly out of
# the page cache and report a disk far faster than the one llama.cpp meets on a cold box.
#
# ## Cost
#
# One GPU box for about eight minutes — two downloads, a re-read and one model load. ~$0.20.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
KEY=""
S5V=2.2.2
PRINT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?}"; shift ;;
    --key) KEY="${2:?}"; shift ;;
    --s5cmd-version) S5V="${2:?}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,34p' "$0"; exit 0 ;;
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
LLAMA_IMAGE="$(jq -r '.containerDefinitions[]|select(.name=="llama")|.image' <<<"$TD_JSON")"
CPU="$(jq -r '.cpu' <<<"$TD_JSON")"; MEM="$(jq -r '.memory' <<<"$TD_JSON")"

say "cluster=$CLUSTER cp=$CP bucket=$BUCKET key=$KEY size=$SZ s5cmd=$S5V"

# shellcheck disable=SC2016
FETCH_CMD='
echo "fc: nproc=$(nproc); $(aws --version 2>&1)"
mkdir -p /models
echo "fc: === 1: aws s3 cp (control, what the sidecar runs today) ==="
rm -f /models/a.gguf /models/b.gguf
t0=$(date +%s); aws s3 cp "s3://$BUCKET/$KEY" /models/a.gguf --only-show-errors; t=$(( $(date +%s) - t0 ))
G=$(stat -c %s /models/a.gguf 2>/dev/null || echo 0)
[ "$G" = "$SZ" ] && echo "fc: RESULT aws-s3-cp ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s" || echo "fc: aws s3 cp FAILED ($G of $SZ)"
echo "fc: === 2: s5cmd ==="
if curl -fsSL --max-time 60 "https://github.com/peak/s5cmd/releases/download/v$S5V/s5cmd_${S5V}_Linux-64bit.tar.gz" | tar -xz -C /usr/local/bin s5cmd 2>/dev/null && command -v s5cmd >/dev/null; then
  echo "fc: $(s5cmd version 2>&1 | head -1)"
  t0=$(date +%s); s5cmd cp "s3://$BUCKET/$KEY" /models/b.gguf >/dev/null; t=$(( $(date +%s) - t0 ))
  G=$(stat -c %s /models/b.gguf 2>/dev/null || echo 0)
  [ "$G" = "$SZ" ] && echo "fc: RESULT s5cmd ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s" || echo "fc: s5cmd FAILED ($G of $SZ)"
else
  echo "fc: RESULT s5cmd UNAVAILABLE - the engine subnet has no egress to github (measured)"
fi
# The second copy exists either way: the load comparison below needs two files, not one.
[ -s /models/b.gguf ] || aws s3 cp "s3://$BUCKET/$KEY" /models/b.gguf --only-show-errors
echo "fc: === handing over to llama ==="
'

# shellcheck disable=SC2016
LOAD_CMD='
# ⚠️ ORDER IS THE EXPERIMENT, and the first version of this probe got it wrong. It ran dd, then
# mmap, then --no-mmap, and --no-mmap came out at 29 s against mmap 94 s. But two full passes had
# warmed the page cache by then, and the container has 14 GB for an 18.5 GB file, so most of it
# could have been resident. A 3x win from one flag has to be earned, not accepted.
#
# The fix is two COPIES of the same object. Each load is preceded by 18.5 GB of other I/O, and
# 18.5 GB through a 14 GB cgroup cycles the cache completely -- so both loads start cold, which
# is also the state a cold start is in. The flag under test goes FIRST so it cannot be flattered.
load_once() {
  rm -f /tmp/srv.log; s=$(date +%s)
  # shellcheck disable=SC2086
  /app/llama-server --host 127.0.0.1 --port 8080 -ngl 99 --jinja -m "$2" $3 >/tmp/srv.log 2>&1 &
  P=$!; ok=0
  for _ in $(seq 1 600); do
    grep -q "model loaded" /tmp/srv.log 2>/dev/null && { ok=1; break; }
    kill -0 $P 2>/dev/null || break
    sleep 1
  done
  t=$(( $(date +%s) - s ))
  [ "$ok" = 1 ] && echo "fc: RESULT load-$1 ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s effective" \
                || echo "fc: RESULT load-$1 FAILED after ${t}s: $(tail -3 /tmp/srv.log | tr "\n" " ")"
  kill $P 2>/dev/null || true; wait $P 2>/dev/null || true
  sleep 15
}
ls -l /models/a.gguf /models/b.gguf 2>&1 | sed "s/^/fc: file: /"
echo "fc: === load 1 of 2: --no-mmap on a.gguf (cold: b.gguf was written after it) ==="
load_once cold-nommap /models/a.gguf "--no-mmap"
echo "fc: === load 2 of 2: mmap on b.gguf (cold: reading a.gguf just cycled the cache) ==="
load_once cold-mmap /models/b.gguf ""
echo "fc: === done ==="
'

TASKDEF="$(jq -n --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg img "$LLAMA_IMAGE" \
  --arg fetch "$FETCH_CMD" --arg load "$LOAD_CMD" --arg bucket "$BUCKET" --arg key "$KEY" \
  --arg sz "$SZ" --arg s5v "$S5V" --arg cpu "$CPU" --arg mem "$MEM" --arg g "$LOG_GROUP" --arg r "$REGION" '
{
  family: "af-engprobe-fetchclient",
  requiresCompatibilities: ["MANAGED_INSTANCES"],
  networkMode: "awsvpc",
  cpu: $cpu, memory: $mem,
  executionRoleArn: $exec, taskRoleArn: $task,
  volumes: [{name: "models", host: {}}],
  containerDefinitions: [
    {name: "fetch", essential: false, image: "public.ecr.aws/aws-cli/aws-cli:latest",
     entryPoint: ["sh","-c"], command: [$fetch],
     environment: [{name:"BUCKET",value:$bucket},{name:"KEY",value:$key},{name:"SZ",value:$sz},{name:"S5V",value:$s5v}],
     mountPoints: [{sourceVolume: "models", containerPath: "/models"}],
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"fetchclient"}}},
    {name: "probe", essential: true, image: $img,
     entryPoint: ["sh","-c"], command: [$load],
     environment: [{name:"SZ",value:$sz},{name:"BUCKET",value:$bucket},{name:"KEY",value:$key}],
     resourceRequirements: [{type:"GPU", value:"1"}],
     mountPoints: [{sourceVolume: "models", containerPath: "/models"}],
     dependsOn: [{containerName:"fetch", condition:"SUCCESS"}],
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"fetchclient"}}}
  ]
}')"

if [ "$PRINT" = 1 ]; then jq . <<<"$TASKDEF"; exit 0; fi

TD_ARN="$("${AWS[@]}" ecs register-task-definition --cli-input-json "$TASKDEF" --query 'taskDefinition.taskDefinitionArn' --output text)"
# A wrong --query returns "None" with exit 0, so an `||` fallback never fires and RunTask fails
# with "TaskDefinition not found" five attempts later. Check the value, not the exit code.
[ -n "$TD_ARN" ] && [ "$TD_ARN" != None ] || { echo "register-task-definition returned no ARN" >&2; exit 1; }
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
  [ $(( $(date +%s) - T0 )) -gt 2400 ] && { say "giving up"; break; }
done
say "stopped after $(( $(date +%s) - T0 ))s — $("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].stoppedReason' --output text)"
sleep 15
"${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "fetchclient" \
  --start-time "$((T0 * 1000))" --query 'events[].message' --output text 2>/dev/null \
  | tr '\t' '\n' | grep '^fc:' || true

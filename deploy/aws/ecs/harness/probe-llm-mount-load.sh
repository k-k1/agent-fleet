#!/usr/bin/env bash
# Does llama.cpp actually load faster off a mounted bucket? Condition 3 of ADR 0072 open
# question 10's Mountpoint follow-up.
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-llm-mount-load.sh [--key llm/<file>.gguf] [--print]
#
# ## Why this exists
#
# `probe-s3-mount.sh` measured a `dd` sequential read through Mountpoint at 567 MB/s against
# `aws s3 cp` at 210 MB/s on the same box — 2.7x. That is NOT the question anyone actually has.
# The question is what llama.cpp does, and it differs in two ways that could erase the win:
#
#   - the load is not purely a disk read. On local NVMe 18.5 GB takes 91 s = an effective
#     204 MB/s, while the NVMe itself does GB/s — so most of those 91 seconds are GGUF parsing
#     and the PCIe transfer to VRAM, which mounting cannot speed up.
#   - llama.cpp mmaps by default, and FUSE mmap semantics are partial. `--no-mmap` is the escape
#     hatch, so both are measured.
#
# ## Three loads, one box, one task
#
#   A  local     copied to the instance store first, then loaded — reproduces today's path and
#                the 91 s number, ON THIS BOX, so B and C are compared against a fresh control
#                rather than against a number remembered from another day and another instance.
#   B  mount     loaded straight off Mountpoint, mmap (llama.cpp's default)
#   C  mount     the same, with --no-mmap
#
# Each load is timed from process start to `model loaded` in llama-server's own log, and the
# server is killed and given time to release VRAM between loads (18.5 GB of a 22.9 GB L4).
#
# ## What it does NOT prove
#
# Nothing about steady-state inference. With `-ngl 99` every layer ends up in VRAM and the file
# is not read again, which is exactly why mounting is plausible here at all — but a role that
# spilled to disk would have a different answer.
#
# ## The image
#
# mount-s3 and libfuse2 are installed INTO the deployed llama.cpp image at run time. That is a
# probe convenience, and it is also the finding: mount-s3 links libfuse2 (measured — a
# `--nodeps` install died with `libfuse.so.2: cannot open shared object file`), and a FUSE mount
# is invisible to other containers, so shipping this for real means baking both into the engine
# image rather than adding a sidecar.
#
# ## Cost
#
# One GPU box for roughly 10 minutes — about $0.25. ⚠️ g6.xlarge capacity in ap-northeast-1 has
# been intermittent; `LlmAllowedInstanceTypes` naming two types is what makes a retry work.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
KEY=""
PRINT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?}"; shift ;;
    --key) KEY="${2:?}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
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

say "cluster=$CLUSTER cp=$CP bucket=$BUCKET key=$KEY size=$SZ image=$LLAMA_IMAGE"

FETCH_CMD="set -e; mkdir -p /models/llm; t0=\$(date +%s); aws s3 cp s3://$BUCKET/$KEY /models/local.gguf --only-show-errors; echo \"mload: COPY \$(stat -c %s /models/local.gguf) bytes in \$(( \$(date +%s) - t0 ))s\""

# shellcheck disable=SC2016
PROBE_CMD='
set -u
echo "mload: === setup ==="
echo "mload: fuse device: $(ls -l /dev/fuse 2>&1)"
export DEBIAN_FRONTEND=noninteractive
(apt-get update -qq && apt-get install -y -qq curl fuse libfuse2 >/dev/null 2>&1) || echo "mload: apt step reported a problem"
curl -fsSL -o /tmp/m.deb https://s3.amazonaws.com/mountpoint-s3-release/latest/x86_64/mount-s3.deb || { echo "mload: FAILED to download mount-s3"; exit 1; }
# apt-get install of a local .deb pulls the dependencies dpkg alone would only complain about.
apt-get install -y -qq /tmp/m.deb >/dev/null 2>&1 || dpkg -i /tmp/m.deb >/dev/null 2>&1 || true
command -v mount-s3 >/dev/null || { echo "mload: FAILED - no mount-s3"; exit 1; }
echo "mload: $(mount-s3 --version 2>&1 | head -1)"
mkdir -p /mnt/s3
mount-s3 "$BUCKET" /mnt/s3 --read-only 2>/tmp/mnt.err && echo "mload: MOUNTED" || { echo "mload: MOUNT FAILED: $(cat /tmp/mnt.err)"; }

nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>&1 | sed "s/^/mload: gpu: /"

load_once() {
  LBL="$1"; MP="$2"; EXTRA="$3"
  [ -e "$MP" ] || { echo "mload: $LBL SKIPPED - $MP is not there"; return; }
  rm -f /tmp/srv.log
  s=$(date +%s)
  # shellcheck disable=SC2086
  /app/llama-server --host 127.0.0.1 --port 8080 -ngl 99 --jinja -m "$MP" $EXTRA >/tmp/srv.log 2>&1 &
  P=$!
  ok=0
  for _ in $(seq 1 600); do
    grep -q "model loaded" /tmp/srv.log 2>/dev/null && { ok=1; break; }
    kill -0 $P 2>/dev/null || break
    sleep 1
  done
  t=$(( $(date +%s) - s ))
  if [ "$ok" = 1 ]; then
    echo "mload: RESULT $LBL loaded in ${t}s  ($(( SZ / (t>0?t:1) / 1000000 )) MB/s effective)"
  else
    echo "mload: RESULT $LBL FAILED after ${t}s: $(tail -4 /tmp/srv.log | tr "\n" " " | tr -d "\t")"
  fi
  kill $P 2>/dev/null || true
  wait $P 2>/dev/null || true
  # 18.5 GB out of a 22.9 GB L4 has to be released before the next load, or it fails on VRAM.
  sleep 15
}

echo "mload: === A: local (instance store), mmap - reproduces todays path ==="
load_once "A-local-mmap" /models/local.gguf ""
echo "mload: === B: mounted, mmap (llama.cpp default) ==="
load_once "B-mount-mmap" "/mnt/s3/$KEY" ""
echo "mload: === C: mounted, --no-mmap ==="
load_once "C-mount-nommap" "/mnt/s3/$KEY" "--no-mmap"
echo "mload: === done ==="
'

TASKDEF="$(jq -n --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg img "$LLAMA_IMAGE" \
  --arg fetch "$FETCH_CMD" --arg probe "$PROBE_CMD" --arg bucket "$BUCKET" --arg key "$KEY" \
  --arg sz "$SZ" --arg cpu "$CPU" --arg mem "$MEM" --arg g "$LOG_GROUP" --arg r "$REGION" '
{
  family: "af-engprobe-mountload",
  requiresCompatibilities: ["MANAGED_INSTANCES"],
  networkMode: "awsvpc",
  cpu: $cpu, memory: $mem,
  executionRoleArn: $exec, taskRoleArn: $task,
  volumes: [{name: "models", host: {}}],
  containerDefinitions: [
    {name: "fetch", essential: false, image: "public.ecr.aws/aws-cli/aws-cli:latest",
     entryPoint: ["sh","-c"], command: [$fetch],
     mountPoints: [{sourceVolume: "models", containerPath: "/models"}],
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"mountload"}}},
    {name: "probe", essential: true, image: $img,
     entryPoint: ["sh","-c"], command: [$probe],
     environment: [{name:"BUCKET",value:$bucket},{name:"KEY",value:$key},{name:"SZ",value:$sz}],
     resourceRequirements: [{type:"GPU", value:"1"}],
     mountPoints: [{sourceVolume: "models", containerPath: "/models"}],
     dependsOn: [{containerName:"fetch", condition:"SUCCESS"}],
     linuxParameters: {
       capabilities: {add: ["SYS_ADMIN"]},
       devices: [{hostPath: "/dev/fuse", containerPath: "/dev/fuse", permissions: ["read","write"]}]
     },
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"mountload"}}}
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

last=$((T0 * 1000))
while :; do
  sleep 30
  "${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "mountload" \
    --start-time "$last" --query 'events[].message' --output text 2>/dev/null | tr '\t' '\n' | grep '^mload:' || true
  last=$(( $(date +%s) * 1000 ))
  st="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].lastStatus' --output text)"
  [ "$st" = STOPPED ] && break
  [ $(( $(date +%s) - T0 )) -gt 2400 ] && { say "giving up after 40 minutes"; break; }
done
say "stopped after $(( $(date +%s) - T0 ))s — $("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].stoppedReason' --output text)"
# ⚠️ The incremental poll above CAN drop a line: CloudWatch ingestion lags, and a RESULT that
# lands between two windows is simply never printed (measured — the first run of this probe
# appeared to produce no result for load A when the log had it all along). Re-read the whole run.
say "full transcript:"
sleep 15
"${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "mountload" \
  --start-time "$((T0 * 1000))" --query 'events[].message' --output text 2>/dev/null \
  | tr '\t' '\n' | grep '^mload:' || true

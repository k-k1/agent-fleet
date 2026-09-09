#!/usr/bin/env bash
# Can the engine read its weights straight out of S3? Mountpoint for Amazon S3 on a fleet GPU box
# (ADR 0072 open question 10, the follow-up to `useLocalStorage`).
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-s3-mount.sh [--role llm|image] [--key llm/<file>.gguf] [--print]
#
# ## The question
#
# Today a cold start COPIES every enabled model S3 → local disk and only then opens it: measured
# 117 s + 91 s = 208 s for one 18.5 GB GGUF (PARAMETERS-60-engines.md, `LlmUseLocalStorage`).
# Mounting S3 would delete the copy from the critical path entirely.
#
# EFS was rejected for this job on price — Elastic Throughput bills $0.04/GB read, i.e. $0.74 a
# cold start, 17-21x the GPU time it saves. **S3 has no per-GB read charge at all**, and the
# gateway VPC endpoint keeps the bytes off the NAT, so the same objection does not apply here.
# That is why this is worth a box and EFS was not.
#
# ## Three things have to hold, and they fail in this order
#
#   1. FUSE has to work at all. `mount-s3` needs /dev/fuse and CAP_SYS_ADMIN, and the host is
#      Bottlerocket under Managed Instances. If ECS will not grant the capability, or the device
#      node is absent, everything below is moot — WHICH IS WHY THIS PROBE EXISTS.
#   2. A FUSE mount made in one container is NOT visible in another (that needs bidirectional
#      mount propagation, which ECS does not expose). So `mount-s3` cannot live in the fetch
#      sidecar the way `aws s3 cp` does — it has to run inside the ENGINE container, which costs
#      the "both roles start through the same wrapper" property of decision 1.
#   3. llama.cpp mmaps by default and FUSE mmap semantics are partial. Less likely to bite here
#      because `-ngl 99` puts every layer in VRAM and the file is not read again after load.
#
# This probe answers 1, and measures what 2 would buy before anyone pays for it.
#
# ## The comparison is the point
#
# It reads the SAME object twice on the SAME box in the SAME task: once through the mount, once
# through `aws s3 cp` (the way the fetch sidecar does it today). Throughput on a shared network
# varies by time of day and by box, so a number from one and a remembered number from the other
# would not be a comparison. Both are timed here, back to back, and the cli pass runs FIRST so a
# warm page cache cannot flatter the mount.
#
# ## The base image, and two dead ends already paid for with a GPU box
#
# It runs on `public.ecr.aws/amazonlinux/amazonlinux:2023` and installs BOTH `mount-s3` and the
# AWS CLI with dnf. Two other shapes were tried on hardware first and both cost a GPU box:
#
#   - amazonlinux:2023 WITHOUT the AWS CLI: with no `aws` on PATH the control reported
#     "0s / 0 MB/s" and an empty object size — it produced a NUMBER instead of an error, which is
#     the worst way to fail.
#   - the aws-cli image + the mount-s3 RPM forced in with `rpm -i --nodeps`: mount-s3 then died
#     with `libfuse.so.2: cannot open shared object file`. **Mountpoint is NOT a dependency-free
#     static binary** — it links libfuse2, which dnf pulls in and --nodeps does not. That matters
#     beyond this probe: condition 2 below means the ENGINE image would have to carry libfuse2
#     and mount-s3, so "mount instead of copy" is not a task-definition change, it is a custom
#     engine image.
#
# That first failure is why every step here checks its own result: the control compares the bytes
# copied against the object size, and the mount read trusts dd's own byte count rather than a
# wall-clock subtraction, which cannot tell a short read from a fast one.
#
# ## Cost
#
# The capacity provider only launches GPU boxes, so this is $1.26/hour for the few minutes it
# takes to read one model twice — budget about $0.15. ⚠️ g6.xlarge capacity in ap-northeast-1
# ran out in BOTH AZs while this was being written, so expect `InsufficientInstanceCapacity` and
# retries; `LlmAllowedInstanceTypes` naming two types is what makes that survivable.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
ROLE=llm
KEY=""
PRINT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?}"; shift ;;
    --role) ROLE="${2:?}"; shift ;;
    --key) KEY="${2:?}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,48p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }
out() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text; }

case "$ROLE" in
  llm)   CP_OUT=LlmCapacityProviderName;   SVC_OUT=LlmServiceName ;;
  image) CP_OUT=ImageCapacityProviderName; SVC_OUT=ImageServiceName ;;
  *) echo "--role must be llm or image" >&2; exit 2 ;;
esac

BUCKET="$(out "$STACK" ModelsBucket)"
CP="$(out "$STACK" "$CP_OUT")"
SERVICE="$(out "$STACK" "$SVC_OUT")"
CLUSTER="$("${AWS[@]}" cloudformation list-exports --query "Exports[?Name=='$PLATFORM_STACK-ClusterName'].Value" --output text)"
[ -n "$BUCKET" ] && [ -n "$CP" ] && [ -n "$CLUSTER" ] || { echo "missing coordinates" >&2; exit 1; }

# The BIGGEST object under the role's prefix: the question is about a cold start's dominant file,
# and a small one would measure request overhead instead of throughput.
if [ -z "$KEY" ]; then
  KEY="$("${AWS[@]}" s3api list-objects-v2 --bucket "$BUCKET" --prefix "$ROLE/" \
    --query 'reverse(sort_by(Contents,&Size))[0].Key' --output text)"
  [ -n "$KEY" ] && [ "$KEY" != None ] || { echo "no object under $ROLE/ in $BUCKET" >&2; exit 1; }
fi

SVC_JSON="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query 'services[0]' --output json)"
SUBNETS="$(jq -r '.networkConfiguration.awsvpcConfiguration.subnets | join(",")' <<<"$SVC_JSON")"
SG="$(jq -r '.networkConfiguration.awsvpcConfiguration.securityGroups[0]' <<<"$SVC_JSON")"
TD_JSON="$("${AWS[@]}" ecs describe-task-definition --task-definition "$(jq -r .taskDefinition <<<"$SVC_JSON")" --query 'taskDefinition' --output json)"
EXEC_ROLE="$(jq -r '.executionRoleArn' <<<"$TD_JSON")"
TASK_ROLE="$(jq -r '.taskRoleArn' <<<"$TD_JSON")"   # already carries s3:GetObject on this bucket
LOG_GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' <<<"$TD_JSON")"

SZ="$("${AWS[@]}" s3api head-object --bucket "$BUCKET" --key "$KEY" --query ContentLength --output text)"
say "cluster=$CLUSTER cp=$CP bucket=$BUCKET key=$KEY size=$SZ"

# Every step reports and the script does NOT stop on the first failure: one run should say which
# of the three conditions broke, not just that something did.
# shellcheck disable=SC2016
PROBE_CMD='
echo "s3m: === step 0: what the container was given ==="
echo "s3m: fuse device: $(ls -l /dev/fuse 2>&1)"
echo "s3m: capabilities: $(grep -E "^CapEff|^CapBnd" /proc/self/status | tr "\n" " ")"
echo "s3m: kernel: $(uname -r)"
echo "s3m: === step 1: install mount-s3 and the AWS CLI (dnf, WITH dependencies) ==="
dnf install -y -q awscli-2 >/dev/null 2>&1 || dnf install -y -q awscli >/dev/null 2>&1 || true
curl -fsSL -o /tmp/m.rpm https://s3.amazonaws.com/mountpoint-s3-release/latest/x86_64/mount-s3.rpm || {
  echo "s3m: FAILED to download mount-s3"; exit 1; }
dnf install -y -q /tmp/m.rpm >/dev/null 2>&1 || { echo "s3m: FAILED to install mount-s3"; exit 1; }
MS3=$(command -v mount-s3)
echo "s3m: mount-s3 -> $($MS3 --version 2>&1 | head -1)"
echo "s3m: libfuse: $(rpm -q fuse-libs 2>&1 | head -1)"
echo "s3m: aws cli $(aws --version 2>&1 || echo ABSENT)"
# SZ is passed in from the host, which always has a working CLI. The container needing one is
# the CONTROL step alone, and a missing control must not cost the mount measurement.
echo "s3m: object $KEY is $SZ bytes"
echo "s3m: === step 2: CONTROL — aws s3 cp to the local disk (what the sidecar does today) ==="
if command -v aws >/dev/null; then
  mkdir -p /work
  t0=$(date +%s); aws s3 cp "s3://$BUCKET/$KEY" /work/blob --only-show-errors; t=$(( $(date +%s) - t0 ))
  GOT=$(stat -c %s /work/blob 2>/dev/null || echo 0)
  if [ "$GOT" = "$SZ" ]; then echo "s3m: CONTROL aws-s3-cp ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s"
  else echo "s3m: CONTROL FAILED — copied $GOT of $SZ bytes"; fi
  rm -f /work/blob
else
  echo "s3m: CONTROL SKIPPED — no aws cli in this image (mount measurement still runs)"
fi
echo "s3m: === step 3: mount S3 ==="
mkdir -p /mnt/s3
if "$MS3" "$BUCKET" /mnt/s3 --read-only 2>/tmp/mnt.err; then
  echo "s3m: MOUNTED. $(grep " /mnt/s3 " /proc/self/mountinfo | tr -d "\t" || true)"
  echo "s3m: listing: $(ls -l "/mnt/s3/$KEY" 2>&1 | tr -d "\t")"
  echo "s3m: === step 4: sequential read THROUGH the mount ==="
  # dd reports its OWN byte count and rate on stderr; that is the authoritative number, not a
  # wall-clock subtraction that cannot tell a short read from a fast one.
  t0=$(date +%s); DDOUT=$(dd if="/mnt/s3/$KEY" of=/dev/null bs=16M 2>&1); t=$(( $(date +%s) - t0 ))
  echo "s3m: dd says: $(echo "$DDOUT" | tr "\n" " " | tr -d "\t")"
  DDBYTES=$(echo "$DDOUT" | grep -oE "^[0-9]+ bytes" | grep -oE "[0-9]+" | head -1)
  if [ "$DDBYTES" = "$SZ" ]; then echo "s3m: MOUNT read ${t}s = $(( SZ / (t>0?t:1) / 1000000 )) MB/s  (full $SZ bytes)"
  else echo "s3m: MOUNT READ SHORT — dd got $DDBYTES of $SZ bytes, the timing means nothing"; fi
  umount /mnt/s3 || true
else
  echo "s3m: MOUNT FAILED — this is the answer to condition 1"
  sed "s/^/s3m:   /" /tmp/mnt.err
fi
echo "s3m: === done ==="
'

TASKDEF="$(jq -n --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg cmd "$PROBE_CMD" \
  --arg bucket "$BUCKET" --arg key "$KEY" --arg sz "$SZ" --arg g "$LOG_GROUP" --arg r "$REGION" '
{
  family: "af-engprobe-s3mount",
  requiresCompatibilities: ["MANAGED_INSTANCES"],
  networkMode: "awsvpc",
  cpu: "2048", memory: "8192",
  executionRoleArn: $exec, taskRoleArn: $task,
  containerDefinitions: [
    {name: "probe", essential: true, image: "public.ecr.aws/amazonlinux/amazonlinux:2023",
     entryPoint: ["sh","-c"], command: [$cmd],
     environment: [{name:"BUCKET",value:$bucket},{name:"KEY",value:$key},{name:"SZ",value:$sz}],
     # The whole experiment: does Managed Instances hand a task CAP_SYS_ADMIN and /dev/fuse?
     linuxParameters: {
       capabilities: {add: ["SYS_ADMIN"]},
       devices: [{hostPath: "/dev/fuse", containerPath: "/dev/fuse", permissions: ["read","write"]}]
     },
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"s3mount"}}}
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

for _ in $(seq 1 120); do
  sleep 20
  st="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].lastStatus' --output text)"
  [ "$st" = STOPPED ] && break
done
say "stopped after $(( $(date +%s) - T0 ))s — $("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].stoppedReason' --output text)"
"${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "s3mount" \
  --start-time "$((T0 * 1000))" --query 'events[].message' --output text 2>/dev/null | tr '\t' '\n' | grep '^s3m:' || true

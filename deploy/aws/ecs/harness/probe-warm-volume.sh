#!/usr/bin/env bash
# Does a kept box still hold its models? The warm-box probe (ADR 0071 decision 7(c), ADR 0072
# open question 10(b)).
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-warm-volume.sh [--role llm|image] [--source-path /var/lib/af-warm-models]
#                                                 [--key llm/<file>.gguf] [--runs 2] [--print]
#
# ## The question
#
# ADR 0071 decision 7(c) says a box MI keeps would be a warm start. It has stayed UNPROVEN
# because of two measured failures, both recorded in cfn/PARAMETERS-60-engines.md ("The model
# volume"):
#
#   1. the engine task definitions mount an ANONYMOUS host volume, and a restart onto the very
#      same instance re-fetched all 18.5 GB (126 s);
#   2. a named `SourcePath` died with "No space left on device" on a brand-new 120 GiB box,
#      because MI's `storageSizeGiB` sizes the DATA volume the container runtime uses, while an
#      arbitrary host path lands on the much smaller ROOT filesystem.
#
# Failure 1 is now explained rather than guessed. The bench's `mountinfo:` line shows where an
# anonymous volume actually lives:
#
#   /._mnt_task/volumes/<TASK-ID>/volumes/models  ->  /models   ext4 /dev/nvme1n1
#
# The TASK ID is IN THE PATH, so every task gets a new empty directory by construction. No
# setting makes an anonymous volume survive a task; only a named one can.
#
# Failure 2 was re-tested here on 2026-09-09 under `UseLocalStorage=true`, on the guess that the
# instance store might be the whole disk and give a named path 245 GB under it. IT IS NOT, and
# the guess was wrong in an instructive way — keep the probe, because the answer it produced
# CLOSES decision 7(c) rather than leaving it open:
#
#   run 1  MISS, fetched 1.1 GB in 15 s   on /dev/nvme0n1p8  3.1G
#   run 2  HIT, same box, same mtime      on /dev/nvme0n1p8  3.1G
#
# So a named `SourcePath` really does outlive its task — the persistence half works. What does
# not work is the SIZE: `/var/lib/...` lands on a 3.1 GB partition, and the 245 GB data volume
# (`/dev/nvme1n1`, where the anonymous volumes live) has no nameable path at all. Mounting the
# host root shows why: the AMI is Bottlerocket, `/` is a 2.7 GB 100%-full read-only dm-verity
# image, and EVERY top-level directory a `SourcePath` could name — `/local`, `/mnt`, `/data`,
# `/opt`, `/var` — resolves into that image, not into the live host's mounts. `/._mnt_task`
# cannot even be created ("read-only file system").
#
# The conclusion is therefore not "needs another GPU hour" but: on Managed Instances a warm
# model volume cannot be built out of host volumes at all. An anonymous volume gets a fresh
# directory per task BY CONSTRUCTION, and a named one cannot reach the only disk big enough.
#
# ## What it does
#
# Runs the SAME tiny task `--runs` times in a row on the engine role's capacity provider, each
# run mounting `--source-path` as a NAMED host volume. Each run reports HIT or MISS for the
# object, and prints the DEVICE it landed on — that last part is the one that matters, since
# persistence without capacity is what this question kept mistaking for progress.
#
# The ec2 instance id is printed for the same reason: a HIT on a DIFFERENT box would prove
# nothing, and a MISS on a different box would disprove nothing.
#
# ## Cost and the traps it inherits
#
# The capacity provider only launches GPU boxes, so this is $1.26/hour even though the probe
# needs no GPU — keep it to the small `--key`, not an 18.5 GB one. Two traps carry over from ADR
# 0072's measurements: a task placed on a JUST-STOPPED box waited 9.5 minutes in PENDING, and MI
# reclaims an idle box about 8 minutes after its last task, so the runs go back to back.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
ROLE=llm
# Small on purpose: the question is whether the directory survives, not how fast it fills.
KEY=""
SOURCE_PATH=/var/lib/af-warm-models
RUNS=2
PRINT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?}"; shift ;;
    --platform-stack) PLATFORM_STACK="${2:?}"; shift ;;
    --role) ROLE="${2:?}"; shift ;;
    --key) KEY="${2:?}"; shift ;;
    --source-path) SOURCE_PATH="${2:?}"; shift ;;
    --runs) RUNS="${2:?}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,64p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }
out() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text; }

case "$ROLE" in
  llm)   SVC_OUT=LlmServiceName ;;
  image) SVC_OUT=ImageServiceName ;;
  *) echo "--role must be llm or image" >&2; exit 2 ;;
esac

BUCKET="$(out "$STACK" ModelsBucket)"
# 🔴 Managed Instances era. The engine roles lost their capacity provider in ADR 0077 - the
# Control Plane buys the box itself with EC2 Fleet - so the run-task below was rewritten to
# place the probe on a box that already exists, and the task definition now asks for EC2.
# 🔴 NOTHING BELOW HAS BEEN RE-MEASURED since that rewrite: it is mechanical, and the numbers
# in the header came from the Managed Instances shape. This guard is what stops a stale probe
# being read as a measurement - delete it when you re-run the thing and record what came out.
cat >&2 <<MIERA
probe-warm-volume.sh has not been run since ADR 0077 moved the engine off Managed Instances.
Start the $ROLE role first (the Control Plane buys the box; this script no longer does), then
delete the guard at this line and re-measure. The numbers in the header are pre-0077.
MIERA
exit 2
SERVICE="$(out "$STACK" "$SVC_OUT")"
CLUSTER="$("${AWS[@]}" cloudformation list-exports --query "Exports[?Name=='$PLATFORM_STACK-ClusterName'].Value" --output text)"
[ -n "$BUCKET" ] && [ -n "$CLUSTER" ] || { echo "missing coordinates: bucket=$BUCKET cluster=$CLUSTER" >&2; exit 1; }

# The smallest object under the role's prefix, so the probe is about persistence and not about
# throughput. Picked from the bucket rather than hard-coded: which models are staged is the
# catalogue's business, not this script's (ADR 0072 decision 1).
if [ -z "$KEY" ]; then
  # shellcheck disable=SC2016  # JMESPath backticks are a literal, not a shell expansion
  KEY="$("${AWS[@]}" s3api list-objects-v2 --bucket "$BUCKET" --prefix "$ROLE/" \
    --query 'sort_by(Contents[?Size>`10000000`],&Size)[0].Key' --output text)"
  [ -n "$KEY" ] && [ "$KEY" != None ] || { echo "no object under $ROLE/ in $BUCKET — stage one first" >&2; exit 1; }
fi

SVC_JSON="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query 'services[0]' --output json)"
SUBNETS="$(jq -r '.networkConfiguration.awsvpcConfiguration.subnets | join(",")' <<<"$SVC_JSON")"
SG="$(jq -r '.networkConfiguration.awsvpcConfiguration.securityGroups[0]' <<<"$SVC_JSON")"
TD_JSON="$("${AWS[@]}" ecs describe-task-definition --task-definition "$(jq -r .taskDefinition <<<"$SVC_JSON")" --query 'taskDefinition' --output json)"
EXEC_ROLE="$(jq -r '.executionRoleArn' <<<"$TD_JSON")"
TASK_ROLE="$(jq -r '.taskRoleArn' <<<"$TD_JSON")"
LOG_GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' <<<"$TD_JSON")"

say "cluster=$CLUSTER bucket=$BUCKET key=$KEY sourcePath=$SOURCE_PATH runs=$RUNS"

# HIT/MISS is decided on SIZE, not on the name alone: a half-written file from a task that was
# killed mid-fetch is not a warm start, and reporting it as one is how this question got its
# wrong answer the first time.
# shellcheck disable=SC2016
PROBE_CMD="set -e; F=/warm/\$(basename $KEY); echo \"warm: box=\$(cat /warm/.box 2>/dev/null || echo none)\"; echo \"warm: disk: \$(df -PH /warm | tail -1)\"; echo \"warm: mountinfo: \$(grep ' /warm ' /proc/self/mountinfo || echo NONE)\"; WANT=\$(aws s3api head-object --bucket $BUCKET --key $KEY --query ContentLength --output text); HAVE=\$(stat -c %s \"\$F\" 2>/dev/null || echo 0); if [ \"\$HAVE\" = \"\$WANT\" ]; then echo \"warm: HIT \$F \$HAVE bytes, already here (mtime \$(stat -c %y \"\$F\"))\"; else echo \"warm: MISS have=\$HAVE want=\$WANT\"; s=\$(date +%s); aws s3 cp s3://$BUCKET/$KEY \"\$F\" --only-show-errors; echo \"warm: fetched \$(stat -c %s \"\$F\") bytes in \$(( \$(date +%s) - s ))s\"; fi; echo \"warm: dir now: \$(ls -la /warm | tail -n +2 | tr '\n' '|')\""

TASKDEF="$(jq -n --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg cmd "$PROBE_CMD" \
  --arg sp "$SOURCE_PATH" --arg g "$LOG_GROUP" --arg r "$REGION" '
{
  family: "af-engprobe-warmvol",
  requiresCompatibilities: ["EC2"],
  networkMode: "awsvpc",
  cpu: "512", memory: "1024",
  executionRoleArn: $exec, taskRoleArn: $task,
  # The whole point: a NAMED host path, not the anonymous {} the engine task definitions use.
  volumes: [{name: "warm", host: {sourcePath: $sp}}],
  containerDefinitions: [
    {name: "probe", essential: true, image: "public.ecr.aws/aws-cli/aws-cli:latest",
     entryPoint: ["sh","-c"], command: [$cmd],
     mountPoints: [{sourceVolume: "warm", containerPath: "/warm"}],
     logConfiguration: {logDriver:"awslogs", options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":"warmvol"}}}
  ]
}')"

if [ "$PRINT" = 1 ]; then jq . <<<"$TASKDEF"; exit 0; fi

TD_ARN="$("${AWS[@]}" ecs register-task-definition --cli-input-json "$TASKDEF" --query 'taskDefinition.taskDefinitionArn' --output text)"
say "registered $TD_ARN"

for run in $(seq 1 "$RUNS"); do
  T0=$(date +%s)
  TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --task-definition "$TD_ARN" \
    --launch-type EC2 --placement-constraints "type=memberOf,expression=attribute:af-role == engine-$ROLE" \
    --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SG],assignPublicIp=DISABLED}" \
    --query 'tasks[0].taskArn' --output text)"
  ID="${TASK##*/}"
  say "run $run: task $ID"
  for _ in $(seq 1 80); do
    sleep 15
    st="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].lastStatus' --output text)"
    [ "$st" = STOPPED ] && break
  done
  # Which box it landed on decides whether run 2 means anything at all.
  CI="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].containerInstanceArn' --output text)"
  EC2="$("${AWS[@]}" ecs describe-container-instances --cluster "$CLUSTER" --container-instances "$CI" --query 'containerInstances[0].ec2InstanceId' --output text 2>/dev/null || echo unknown)"
  say "run $run: finished on $EC2 after $(( $(date +%s) - T0 ))s — $("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].stoppedReason' --output text)"
  "${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix "warmvol" \
    --start-time "$((T0 * 1000))" --query 'events[].message' --output text 2>/dev/null \
    | tr '\t' '\n' | grep '^warm:' | sed "s/^/  run $run [$EC2] /" || true
done

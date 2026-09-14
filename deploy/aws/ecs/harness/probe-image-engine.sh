#!/usr/bin/env bash
# Ask the deployment's `image` engine whether it is alive, from inside the VPC.
#
#   harness/probe-image-engine.sh --profile <p> --region <r> [--wake] [--stop] [--name TAG]
#
# ## Why this exists
#
# The engine is on a private subnet and its security group admits the Control Plane and
# nobody else (ADR 0071 decision 4 — reachability IS the access control, and ComfyUI has no
# authentication of its own). The only ways to reach it are therefore a member's
# `generate_image` inside a Workspace, or a task inside the VPC wearing the CP's security
# group. This is the second one.
#
# ⚠️ **This is a LIVENESS probe, not a picture.** Before ADR 0083 the `image` role could be
# stable-diffusion.cpp, and this script asked it for one image over its OpenAI-compatible
# `/v1/images/generations` — a request the retired engine understood. ComfyUI does not: its
# native API is `POST /prompt` with a per-checkpoint-family workflow graph
# (`workspace/agent/internal/imagegen/comfy.go`'s `comfyBuildGraph`), which this script does not
# reimplement — hand-rolling that graph in bash would duplicate non-trivial Go logic that could
# silently drift from what the `comfy` provider actually sends. So this probe only asks
# `GET /system_stats` (unauthenticated, answers immediately once the process is up) and uploads
# the response to S3 for inspection. To see an actual picture, use a member's `generate_image`
# inside a Workspace pointed at this deployment — the only real entry point for one (ADR 0077).
#
# It is a PROBE, not a benchmark. `bench-image-engine.sh` next door measures ComfyUI on its own
# GPU box for phase P2.
#
# ## What it reuses, and why that is not a shortcut
#
# The ingest task definition. It is already two containers sharing a volume — curl, then
# aws-cli with S3 write on the models bucket — which is exactly the shape needed, and both
# entry points are `sh -c` so the commands can be overridden. Standing up a second task
# definition for this would be a CloudFormation resource in a template that has 820 bytes left.
#
# ⚠️ An override takes ONE string per command, because the EntryPoint is already ["sh","-c"].
# Passing ["sh","-c",<script>] becomes `sh -c sh -c <script>` — it does nothing and exits 0,
# i.e. it reads as success and ran nothing (measured, ADR 0071).
set -euo pipefail

PROFILE=""; REGION=""; WAKE=0; STOP=0
NAME="probe"
ENGINES_STACK="af-ecs-engines"; NET_STACK="af-ecs-network"; PLATFORM_STACK="af-ecs-platform"

usage() { sed -n '2,30p' "$0" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?}"; shift ;;
    --region)  REGION="${2:?}"; shift ;;
    --name)    NAME="${2:?}"; shift ;;
    --stack)   ENGINES_STACK="${2:?}"; shift ;;
    --wake)    WAKE=1 ;;
    --stop)    STOP=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
AWS=(aws --profile "$PROFILE" --region "$REGION")

out() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
  --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text; }

CLUSTER="$(out "$PLATFORM_STACK" ClusterName)"
BUCKET="$(out "$ENGINES_STACK" ModelsBucket)"
FAMILY="$(out "$ENGINES_STACK" IngestTaskDefFamily)"
SERVICE="$(out "$ENGINES_STACK" ImageServiceName)"
SUBNETS="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$NET_STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='PrivateSubnets'].OutputValue" --output text)"
# The CP's own security group: the engine admits it and nothing else.
CPSG="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$NET_STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='CpSgId'].OutputValue" --output text)"
[ -n "$CPSG" ] && [ "$CPSG" != "None" ] || { echo "ERROR: no CpSgId output on $NET_STACK" >&2; exit 1; }

if [ "$WAKE" = 1 ]; then
  echo "==> waking $SERVICE (desired 1)"
  "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --desired-count 1 \
    --query 'service.desiredCount' --output text >/dev/null
  # A GPU box has to be bought, pulled to and fed 6.5 GB from S3 — measured 165-197 s once the
  # box exists, and the box itself took anywhere from 8 to 88 s to appear (ADR 0071 P0).
  echo "==> waiting for a running task (this is minutes, not seconds)"
  for _ in $(seq 1 120); do
    n="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
      --query 'services[0].runningCount' --output text)"
    [ "$n" = "1" ] && break
    sleep 10
  done
  echo "==> runningCount=$n"
fi

KEY="probe/${NAME}.json"

# The engine listens only once ComfyUI's process is up, so a connection refused here is "not
# ready yet" rather than a failure: keep asking for a few minutes, then give up loudly.
FETCH='set -e;
  echo "probe: GET $ENGINE/system_stats";
  i=0;
  until curl -sS --max-time 30 "$ENGINE/system_stats" -o /scratch/resp.json; do
    i=$((i+1)); [ "$i" -lt 40 ] || { echo "probe: the engine never answered"; exit 1; };
    echo "probe: not listening yet ($i)"; sleep 15;
  done;
  echo "probe: $(stat -c %s /scratch/resp.json) bytes of answer";
  head -c 400 /scratch/resp.json; echo'

UPLOAD='set -e;
  aws s3 cp /scratch/resp.json "s3://$BUCKET/$KEY" --only-show-errors;
  echo "probe: uploaded $KEY"'

echo "==> run-task $FAMILY (probe)"
TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --launch-type FARGATE \
  --task-definition "$FAMILY" \
  --network-configuration "awsvpcConfiguration={subnets=[${SUBNETS//,/,}],securityGroups=[$CPSG],assignPublicIp=DISABLED}" \
  --overrides "$(python3 - "$BUCKET" "$KEY" "$FETCH" "$UPLOAD" <<'PY'
import json, sys
bucket, key, fetch, upload = sys.argv[1:5]
env = lambda **kw: [{"name": k, "value": v} for k, v in kw.items()]
print(json.dumps({"containerOverrides": [
    {"name": "fetch", "command": [fetch],
     "environment": env(ENGINE="http://image.af.internal:8080")},
    {"name": "upload", "command": [upload],
     "environment": env(BUCKET=bucket, KEY=key)},
]}))
PY
)" --query 'tasks[0].taskArn' --output text)"
echo "==> task ${TASK##*/}"
"${AWS[@]}" ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$TASK" || true
"${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" \
  --query 'tasks[0].containers[].{name:name,exit:exitCode,reason:reason}' --output table

echo "==> probe log"
"${AWS[@]}" logs tail "/af/${ENGINES_STACK}/engines" --since 20m \
  --filter-pattern "probe" 2>/dev/null | tail -30 || true

echo "==> the answer: s3://$BUCKET/$KEY (for a picture, use generate_image in a Workspace)"

if [ "$STOP" = 1 ]; then
  echo "==> stopping $SERVICE (desired 0) — a GPU box is \$1.26/hour"
  "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --desired-count 0 \
    --query 'service.desiredCount' --output text >/dev/null
fi

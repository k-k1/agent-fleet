#!/usr/bin/env bash
# Ask the deployment's `image` engine for one picture, from inside the VPC.
#
#   harness/probe-image-engine.sh --profile <p> --region <r> [--wake] [--stop]
#                                 [--prompt TEXT] [--size WxH] [--seed N] [--name TAG]
#
# ## Why this exists
#
# The engine is on a private subnet and its security group admits the Control Plane and
# nobody else (ADR 0071 decision 4 — reachability IS the access control, and sd-server has no
# authentication of its own). The only ways to see a picture are therefore a member's
# `generate_image` inside a Workspace, or a task inside the VPC wearing the CP's security
# group. This is the second one, and it exists because ADR 0072 P0's definition of done —
# "choose another checkpoint and the next start returns a new picture" — cannot be observed
# from a laptop at all.
#
# It is a PROBE, not a benchmark. `bench-image-engine.sh` next door measures ComfyUI on its own
# GPU box for phase P2; this one asks the engine that is already running for one image and puts
# it in S3, so what came back can be looked at.
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
PROMPT="a red apple on a wooden table, studio lighting, photograph"
SIZE="512x512"; SEED=""; NAME="probe"
ENGINES_STACK="af-ecs-engines"; NET_STACK="af-ecs-network"; PLATFORM_STACK="af-ecs-platform"

usage() { sed -n '2,30p' "$0" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?}"; shift ;;
    --region)  REGION="${2:?}"; shift ;;
    --prompt)  PROMPT="${2:?}"; shift ;;
    --size)    SIZE="${2:?}"; shift ;;
    --seed)    SEED="${2:?}"; shift ;;
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

BODY="{\"prompt\":$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$PROMPT"),\"size\":\"$SIZE\""
[ -n "$SEED" ] && BODY="$BODY,\"seed\":$SEED"
BODY="$BODY}"
KEY="probe/${NAME}.png"

# The engine listens only once its checkpoint is loaded, so a connection refused here is "not
# ready yet" rather than a failure: keep asking for a few minutes, then give up loudly.
FETCH='set -e;
  echo "probe: POST $ENGINE/v1/images/generations";
  echo "probe: body $BODY";
  i=0;
  until curl -sS --max-time 900 -X POST "$ENGINE/v1/images/generations" \
        -H "Content-Type: application/json" -d "$BODY" -o /scratch/resp.json; do
    i=$((i+1)); [ "$i" -lt 40 ] || { echo "probe: the engine never answered"; exit 1; };
    echo "probe: not listening yet ($i)"; sleep 15;
  done;
  echo "probe: $(stat -c %s /scratch/resp.json) bytes of answer";
  head -c 200 /scratch/resp.json; echo'

UPLOAD='set -e;
  python3 - <<PY
import base64, json, sys
d = json.load(open("/scratch/resp.json"))
if "data" not in d:
    print("probe: the engine answered an error:", json.dumps(d)[:400]); sys.exit(1)
raw = base64.b64decode(d["data"][0]["b64_json"])
open("/scratch/out.png", "wb").write(raw)
print("probe: decoded", len(raw), "bytes,", d.get("output_format", "png"))
PY
  aws s3 cp /scratch/out.png "s3://$BUCKET/$KEY" --only-show-errors;
  echo "probe: uploaded $KEY";
  # What the engine says it is holding. sd-server answers a fixed id, so this is a liveness
  # line rather than the checkpoint name -- the checkpoint is in the engine container is log.
  curl -sS --max-time 30 "$ENGINE/v1/models" || true; echo'

echo "==> run-task $FAMILY (probe)"
TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --launch-type FARGATE \
  --task-definition "$FAMILY" \
  --network-configuration "awsvpcConfiguration={subnets=[${SUBNETS//,/,}],securityGroups=[$CPSG],assignPublicIp=DISABLED}" \
  --overrides "$(python3 - "$BODY" "$BUCKET" "$KEY" "$FETCH" "$UPLOAD" <<'PY'
import json, sys
body, bucket, key, fetch, upload = sys.argv[1:6]
env = lambda **kw: [{"name": k, "value": v} for k, v in kw.items()]
print(json.dumps({"containerOverrides": [
    {"name": "fetch", "command": [fetch],
     "environment": env(ENGINE="http://image.af.internal:8080", BODY=body)},
    {"name": "upload", "command": [upload],
     "environment": env(BUCKET=bucket, KEY=key, ENGINE="http://image.af.internal:8080")},
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

echo "==> the picture: s3://$BUCKET/$KEY"

if [ "$STOP" = 1 ]; then
  echo "==> stopping $SERVICE (desired 0) — a GPU box is \$1.26/hour"
  "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --desired-count 0 \
    --query 'service.desiredCount' --output text >/dev/null
fi

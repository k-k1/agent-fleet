#!/usr/bin/env bash
# Image-engine bench on the fleet's own GPU box — ComfyUI, several checkpoints, one L4 (ADR 0072)
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/bench-image-engine.sh [--stack af-ecs-engines] [--phases "default:,highvram:--highvram"]
#                                                  [--comfy-tag v0.34.0] [--image <ecr uri:tag>] [--print]
#
# Starts ONE task on the `image` role's Managed Instances capacity provider — a fresh g6.xlarge,
# $1.26/hour while it runs — made of four containers, and follows it to the end:
#
#   fetch   aws-cli: the checkpoints, text encoder, VAEs and a LoRA from the stack's models
#           bucket into ComfyUI's models/ layout (the S3 half of a cold start, timed per file);
#   comfy   ComfyUI on the GPU, checked out at --comfy-tag over the baked copy (the community
#           image pins an older ComfyUI; what the checkout and pip cost is itself a measurement
#           for ADR 0072's own-image question). Restarted once per label:flags pair in
#           --phases;
#   bench   bench-image-engine.py: SDXL / Z-Image-Turbo / FLUX.2 klein 4B twice each, a round
#           trip between them, SDXL with and without a LoRA at one seed, a 512px SDXL — once per
#           phase — logging wall time, ComfyUI's own execution time and VRAM after each;
#   upload  the pictures and results.jsonl to s3://<bucket>/bench/<run>/, then the task ends.
#
# ## What it does NOT touch
#
# ⚠️ Not the `image` service and not the stack. The service's desired count belongs to the
# Control Plane's controller (see bench-tts-engine.sh for how that bites), so the task is run
# with RunTask on the same capacity provider, outside the service and outside Cloud Map.
#
# ## Two traps, both measured
#
# - **Run it on a box nothing else has run on.** A task's anonymous host volume is never
#   reclaimed (ADR 0071, decision 7's note), so the second task on the same 60 GB box fails
#   its fetch with `No space left on device`. Wait for Managed Instances to reclaim the box
#   (it disappears from the cluster's container instances ~8 minutes after the last task) —
#   this script does that wait before RunTask.
# - **The models have to be in the bucket already.** Stage them with the ingest task
#   (deploy/aws/ecs/README.md, "Getting a model into the catalogue"); the keys this expects
#   are listed under MODELS below.
#
# ## The image
#
# There is no official ComfyUI image. The default `--image` is `<account>.dkr.ecr.<region>.amazonaws.com/af-engbench:comfyui`,
# which you create and fill by hand before the first run — pulling 5.4 GB from GHCR through the
# NAT at every box start is what the copy avoids (measured on ADR 0071: 12-14 MB/s):
#
#   aws ecr create-repository --repository-name af-engbench
#   aws ecr get-login-password | crane auth login <account>.dkr.ecr.<region>.amazonaws.com -u AWS --password-stdin
#   crane copy ghcr.io/lecode-official/comfyui-docker:latest <account>.dkr.ecr.<region>.amazonaws.com/af-engbench:comfyui
#
# and delete the repository when the measurements are done. The community image bakes an older
# ComfyUI (v0.8.2 on 2026-09-08, before FLUX.2 klein existed), which is what --comfy-tag is for.
#
# ## Cost
#
# One g6.xlarge for 25-45 minutes per run, plus the drain Managed Instances charges after.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")
HERE="$(cd "$(dirname "$0")" && pwd)"

STACK=af-ecs-engines
PLATFORM_STACK=af-ecs-platform
# label:flags pairs, one ComfyUI start per pair. An empty flag list is the stock configuration.
PHASES="default:,highvram:--highvram"
COMFY_TAG=v0.34.0
IMAGE=""
PRINT=0
TEXT_ENCODER=qwen_3_4b_fp8_mixed.safetensors

while [ $# -gt 0 ]; do
  case "$1" in
    --stack) STACK="${2:?--stack needs a value}"; shift ;;
    --platform-stack) PLATFORM_STACK="${2:?--platform-stack needs a value}"; shift ;;
    --phases) PHASES="${2:?--phases needs a value}"; shift ;;
    --comfy-tag) COMFY_TAG="${2:?--comfy-tag needs a value}"; shift ;;
    --image) IMAGE="${2:?--image needs a value}"; shift ;;
    --text-encoder) TEXT_ENCODER="${2:?--text-encoder needs a value}"; shift ;;
    --print) PRINT=1 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }
output_of() { "${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text; }
export_of() { "${AWS[@]}" cloudformation list-exports --query "Exports[?Name=='$1'].Value" --output text; }

# --- coordinates, read off the stack and the real service rather than guessed ---------------
BUCKET="$(output_of ModelsBucket)"
CAPACITY_PROVIDER="$(output_of ImageCapacityProviderName)"
SERVICE="$(output_of ImageServiceName)"
INGEST_TD="$(output_of IngestTaskDefFamily)"
CLUSTER="$(export_of "$PLATFORM_STACK-ClusterName")"
[ -n "$BUCKET" ] && [ -n "$CAPACITY_PROVIDER" ] && [ -n "$CLUSTER" ] && [ "$SERVICE" != "-" ] || {
  echo "missing coordinates: bucket=$BUCKET cp=$CAPACITY_PROVIDER cluster=$CLUSTER image-service=$SERVICE (is the image role staged?)" >&2
  exit 1
}
SVC_JSON="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query 'services[0]' --output json)"
SUBNETS="$(jq -r '.networkConfiguration.awsvpcConfiguration.subnets | join(",")' <<<"$SVC_JSON")"
ENGINE_SG="$(jq -r '.networkConfiguration.awsvpcConfiguration.securityGroups[0]' <<<"$SVC_JSON")"
IMAGE_TD="$(jq -r '.taskDefinition' <<<"$SVC_JSON")"
TD_JSON="$("${AWS[@]}" ecs describe-task-definition --task-definition "$IMAGE_TD" --query 'taskDefinition' --output json)"
EXEC_ROLE="$(jq -r '.executionRoleArn' <<<"$TD_JSON")"
LOG_GROUP="$(jq -r '.containerDefinitions[0].logConfiguration.options["awslogs-group"]' <<<"$TD_JSON")"
# The ingest task's role, not the engine's: the upload container needs to WRITE the results
# into the bucket, and the engine role can only read it.
TASK_ROLE="$("${AWS[@]}" ecs describe-task-definition --task-definition "$INGEST_TD" --query 'taskDefinition.taskRoleArn' --output text)"
if [ -z "$IMAGE" ]; then
  ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
  IMAGE="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/af-engbench:comfyui"
fi
RUN="$(date +%Y%m%d-%H%M%S)"
say "cluster=$CLUSTER cp=$CAPACITY_PROVIDER bucket=$BUCKET image=$IMAGE run=$RUN"

# S3 key -> path under ComfyUI's models/. Stage them with the ingest task first.
MODELS=(
  "image/sd_xl_base_1.0.safetensors checkpoints/sd_xl_base_1.0.safetensors"
  "image/diffusion_models/z_image_turbo_bf16.safetensors diffusion_models/z_image_turbo_bf16.safetensors"
  "image/diffusion_models/flux-2-klein-4b.safetensors diffusion_models/flux-2-klein-4b.safetensors"
  "image/text_encoders/$TEXT_ENCODER text_encoders/$TEXT_ENCODER"
  "image/vae/ae.safetensors vae/ae.safetensors"
  "image/vae/flux2-vae.safetensors vae/flux2-vae.safetensors"
  "image/loras/pixel-art-xl.safetensors loras/pixel-art-xl.safetensors"
)
FETCH_LIST=""
for m in "${MODELS[@]}"; do FETCH_LIST="$FETCH_LIST '$m'"; done

# shellcheck disable=SC2016
FETCH_CMD="set -e; B=s3://$BUCKET; mkdir -p /models/checkpoints /models/diffusion_models /models/text_encoders /models/vae /models/loras /out; aws s3 cp \$B/bench/bench.py /out/bench.py --only-show-errors; T0=\$(date +%s); for p in $FETCH_LIST; do set -- \$p; s=\$(date +%s); aws s3 cp \$B/\$1 /models/\$2 --only-show-errors; echo \"fetch: \$2 \$(stat -c %s /models/\$2) bytes in \$(( \$(date +%s) - s ))s\"; done; echo \"fetch: all in \$(( \$(date +%s) - T0 ))s\"; df -h /models | tail -1"

# The comfy container: update the baked checkout, then one ComfyUI per label:flags pair.
# Between pairs it waits for the bench to touch /out/next. ⚠️ Never add --cache-none here to
# "save memory": measured, it drops the loader nodes' outputs and every prompt then re-reads
# the weights from disk (a warm SDXL took 57 s instead of 19). The Manager symlink the image's entrypoint
# creates is removed on purpose (ADR 0071 decision 6: no Manager on a fleet box).
# shellcheck disable=SC2016
COMFY_CMD='set -e; cd /opt/comfyui; echo "comfy: baked $(git describe --tags --always)"; if [ -n "$COMFY_TAG" ]; then s=$(date +%s); git fetch --depth 1 origin tag $COMFY_TAG -q && git checkout -q $COMFY_TAG && echo "comfy: checkout $COMFY_TAG in $(( $(date +%s) - s ))s"; s=$(date +%s); pip install -q -r requirements.txt 2>&1 | grep -v "pip as the" | tail -3; echo "comfy: pip in $(( $(date +%s) - s ))s"; fi; rm -rf /opt/comfyui/custom_nodes/ComfyUI-Manager; for d in checkpoints clip clip_vision configs controlnet diffusers diffusion_models embeddings gligen hypernetworks loras photomaker style_models text_encoders unet upscale_models vae vae_approx; do mkdir -p /opt/comfyui/models/$d; done; rm -f /out/next; IFS=,; for PH in $FLAGS; do IFS=" "; F="${PH#*:}"; echo "comfy: starting $(date +%T) phase=${PH%%:*} flags=[$F]"; /opt/conda/bin/python main.py --port 8188 --listen 0.0.0.0 --disable-auto-launch --output-directory /out $F & P=$!; while [ ! -f /out/next ]; do sleep 2; if ! kill -0 $P 2>/dev/null; then echo "comfy: server died"; break; fi; done; rm -f /out/next; kill $P 2>/dev/null; wait $P || true; echo "comfy: stopped $(date +%T)"; IFS=,; done; echo "comfy: all phases done"'

# The labels alone, for the bench's result names.
PHASE_LABELS="$(tr ',' '\n' <<<"$PHASES" | sed 's/:.*//' | paste -sd, -)"

logcfg() { jq -n --arg g "$LOG_GROUP" --arg r "$REGION" --arg p "$1" '{logDriver:"awslogs",options:{"awslogs-group":$g,"awslogs-region":$r,"awslogs-stream-prefix":$p}}'; }

TASKDEF="$(jq -n \
  --arg exec "$EXEC_ROLE" --arg task "$TASK_ROLE" --arg image "$IMAGE" --arg bucket "$BUCKET" --arg run "$RUN" \
  --arg fetch "$FETCH_CMD" --arg comfy "$COMFY_CMD" --arg tag "$COMFY_TAG" --arg flags "$PHASES" \
  --arg phases "$PHASE_LABELS" --arg te "$TEXT_ENCODER" \
  --argjson lf "$(logcfg bench-fetch)" --argjson lc "$(logcfg bench-comfy)" --argjson lb "$(logcfg bench)" --argjson lu "$(logcfg bench-upload)" '
{
  family: "af-engbench-comfy",
  requiresCompatibilities: ["MANAGED_INSTANCES"],
  networkMode: "awsvpc",
  cpu: "4096",
  # The whole box: a g6.xlarge registers 15,000 MiB with ECS (measured), and ComfyUI keeps
  # what it evicts from VRAM in RAM.
  memory: "15000",
  executionRoleArn: $exec,
  taskRoleArn: $task,
  volumes: [{name: "models", host: {}}, {name: "out", host: {}}],
  containerDefinitions: [
    {name: "fetch", essential: false, image: "public.ecr.aws/aws-cli/aws-cli:latest", entryPoint: ["sh","-c"], command: [$fetch],
     mountPoints: [{sourceVolume: "models", containerPath: "/models"}, {sourceVolume: "out", containerPath: "/out"}], logConfiguration: $lf},
    {name: "comfy", essential: false, image: $image, entryPoint: ["/bin/bash","-c"], command: [$comfy],
     environment: [{name: "COMFY_TAG", value: $tag}, {name: "FLAGS", value: $flags}],
     portMappings: [{containerPort: 8188}], resourceRequirements: [{type: "GPU", value: "1"}],
     mountPoints: [{sourceVolume: "models", containerPath: "/opt/comfyui/models"}, {sourceVolume: "out", containerPath: "/out"}],
     dependsOn: [{containerName: "fetch", condition: "SUCCESS"}], logConfiguration: $lc},
    {name: "bench", essential: false, image: "public.ecr.aws/docker/library/python:3.12-slim", command: ["python3","-u","/out/bench.py"],
     environment: [{name: "PHASES", value: $phases}, {name: "TEXT_ENCODER", value: $te}],
     mountPoints: [{sourceVolume: "out", containerPath: "/out"}],
     dependsOn: [{containerName: "fetch", condition: "SUCCESS"}, {containerName: "comfy", condition: "START"}], logConfiguration: $lb},
    {name: "upload", essential: true, image: "public.ecr.aws/aws-cli/aws-cli:latest", entryPoint: ["sh","-c"],
     command: ["aws s3 cp /out s3://\($bucket)/bench/\($run)/ --recursive --exclude bench.py --only-show-errors; echo \"upload: done $(ls /out | wc -l) files\""],
     mountPoints: [{sourceVolume: "out", containerPath: "/out"}],
     dependsOn: [{containerName: "bench", condition: "COMPLETE"}], logConfiguration: $lu}
  ]
}')"

if [ "$PRINT" = 1 ]; then
  jq . <<<"$TASKDEF"
  exit 0
fi

"${AWS[@]}" s3 cp "$HERE/bench-image-engine.py" "s3://$BUCKET/bench/bench.py" --only-show-errors
TD_ARN="$("${AWS[@]}" ecs register-task-definition --cli-input-json "$TASKDEF" --query 'taskDefinition.taskDefinitionArn' --output text)"
say "registered $TD_ARN"

# A box that ran a task before has that task's model volume still on it (see the header).
gpu_boxes() {
  local arns
  arns="$("${AWS[@]}" ecs list-container-instances --cluster "$CLUSTER" --query 'containerInstanceArns' --output text)"
  [ -n "$arns" ] || return 0
  "${AWS[@]}" ecs describe-container-instances --cluster "$CLUSTER" --container-instances $arns \
    --query "containerInstances[?capacityProviderName=='$CAPACITY_PROVIDER'].ec2InstanceId" --output text
}
for _ in $(seq 1 40); do
  b="$(gpu_boxes)"
  [ -z "$b" ] && break
  say "waiting for Managed Instances to reclaim $b (a used box has no room for the models)"
  sleep 30
done

TASK=""
cleanup() {
  [ -n "$TASK" ] && "${AWS[@]}" ecs stop-task --cluster "$CLUSTER" --task "$TASK" --reason "bench-image-engine cleanup" >/dev/null 2>&1 || true
}
trap cleanup EXIT

T0=$(date +%s)
TASK="$("${AWS[@]}" ecs run-task --cluster "$CLUSTER" --task-definition "$TD_ARN" \
  --capacity-provider-strategy "capacityProvider=$CAPACITY_PROVIDER,weight=1" \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$ENGINE_SG],assignPublicIp=DISABLED}" \
  --query 'tasks[0].taskArn' --output text)"
say "task $TASK"

last=$((T0 * 1000))
while :; do
  sleep 30
  "${AWS[@]}" logs filter-log-events --log-group-name "$LOG_GROUP" --log-stream-name-prefix bench --start-time "$last" \
    --query 'events[].[timestamp,logStreamName,message]' --output text \
    | awk -v t0="$T0" '{printf "+%ds %s ", ($1/1000)-t0, substr($2,1,11); $1="";$2=""; print substr($0,1,330)}' \
    | grep -E 'fetch:|comfy:|RESULT|SUMMARY|phase|upload:|rror' | grep -v ComfyUI-Manager || true
  last=$(( $(date +%s) * 1000 ))
  st="$("${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" --query 'tasks[0].lastStatus' --output text)"
  [ "$st" = STOPPED ] && break
  [ $(( $(date +%s) - T0 )) -gt 5400 ] && { say "giving up after 90 minutes"; break; }
done
"${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$TASK" \
  --query 'tasks[0].[createdAt,pullStartedAt,pullStoppedAt,startedAt,stoppedAt,stopCode]' --output text | tr '\n' ' '; echo
TASK=""
OUT="${AF_BENCH_OUT:-/tmp/bench-image-engine}/$RUN"
mkdir -p "$OUT"
"${AWS[@]}" s3 cp "s3://$BUCKET/bench/$RUN/" "$OUT/" --recursive --only-show-errors || true
say "results in $OUT ($(ls "$OUT" | wc -l) files)"
[ -f "$OUT/results.jsonl" ] && jq -c '{name,ok,wall_s,exec_s,vram_used_mib}' "$OUT/results.jsonl"

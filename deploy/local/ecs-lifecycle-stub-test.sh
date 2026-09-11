#!/usr/bin/env bash
# Stub end-to-end test for the deployment lifecycle scripts
# (standup.sh / teardown.sh / pause.sh — docs/log/73).
#
# It uses neither real AWS nor docker. Fake `aws` / `crane` / `curl` on PATH record the
# calls and pin down their order. What is guarded here is not "things get deleted" but the
# order in which they get deleted — every breakage actually measured was an ordering one:
#
#   - start cleaning up before stopping the CP and the running CP recreates what you delete
#   - terminating a slot does not remove its home EBS volume (that needs a separate delete)
#   - delete the stacks all at once and, while an importer is still around, the exporting
#     stack's deletion is cancelled silently, leaving the wait loop spinning while you
#     believe it was deleted
#   - on the way up, the image has to go in after 20 (which creates the ECR) and the pool
#     has to be created before 30, whose parameters must receive that launch template's new
#     physical ID, or the slots never come up again
#
# And the last case is a test for doing nothing: a teardown without `--yes` must not emit a
# single write.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ECS="$ROOT/deploy/aws/ecs"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# --- fake capture (standing in for what capture-env.sh writes). Named <profile>.<region>. ---
export AF_DEPLOY_STATE_DIR="$WORK/state"
STATE="$AF_DEPLOY_STATE_DIR/p.ap-northeast-1.t-ingress"
mkdir -p "$STATE/params"
cat > "$STATE/env" <<'EOF'
AF_FQDN=af.example.test
AF_STACK_NETWORK=t-network
AF_STACK_DATA=t-data
AF_STACK_PLATFORM=t-platform
AF_STACK_POOL=t-pool
AF_STACK_INGRESS=t-ingress
AF_WS_RUNTIME=ecs-ec2
AF_PERSISTENCE=delete
AF_IMAGE_TAG=9.9.9-dev-test
AF_DEV_DEPLOY=1
EOF
# A second one: a deployment with Persistence=retain (profile p2). The retain path can only
# be walked here.
STATE2="$AF_DEPLOY_STATE_DIR/p2.ap-northeast-1.t-ingress"
mkdir -p "$STATE2/params"
sed 's/^AF_PERSISTENCE=delete$/AF_PERSISTENCE=retain/' "$STATE/env" > "$STATE2/env" 2>/dev/null || true

echo "VpcCidr=10.20.0.0/16"       > "$STATE/params/00-network"
echo "Persistence=delete"         > "$STATE/params/10-data"
echo "NetworkStackName=t-network" > "$STATE/params/20-platform"
echo "SlotRootVolumeGiB=100"      > "$STATE/params/40-ec2-pool"
cat > "$STATE/params/30-ingress" <<'EOF'
Fqdn=af.example.test
HostedZoneId=ZTEST
SsmPrefix=/af-cp
GoogleClientId=gid
ImageTag=0.0.0-old
Ec2SlotLaunchTemplate=lt-OLD
Ec2SlotAmiArm64=
CpArch=x86_64
BitbucketOauthKey=must-not-be-printed
EOF
cp -a "$STATE/params/." "$STATE2/params/"
sed -i 's/^AF_PERSISTENCE=delete$/AF_PERSISTENCE=retain/' "$STATE2/env"

# A third one (profile p3): the same deployment with speech opted in (ADR 0070). Note what
# opts it in — the presence of params/50-tts, with no AF_STACK_TTS in the env. That is the
# path a deployment turning speech on for the FIRST time takes, and the one where a wrong
# default stack name would go unnoticed.
STATE3="$AF_DEPLOY_STATE_DIR/p3.ap-northeast-1.t-ingress"
mkdir -p "$STATE3/params"
cp -a "$STATE/params/." "$STATE3/params/"
cp "$STATE/env" "$STATE3/env"
echo "ServiceConnectNamespace=af.internal" > "$STATE3/params/50-tts"

# A fourth (profile p4): self-hosted inference opted in (ADR 0071), the same way — the
# presence of params/60-engines, with no AF_STACK_ENGINES in the env.
STATE4="$AF_DEPLOY_STATE_DIR/p4.ap-northeast-1.t-ingress"
mkdir -p "$STATE4/params"
cp -a "$STATE/params/." "$STATE4/params/"
cp "$STATE/env" "$STATE4/env"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageImageTag=master-cuda\n' > "$STATE4/params/60-engines"

# --- fake aws. Answers queries in the same shape the real one does ----------
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
args="$*"
case "$args" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  *"cloudformation list-exports"*"SlotLaunchTemplateId"*) echo "t-pool-SlotLaunchTemplateId" ;;
  # How update.sh finds the engine stack: the ingress stack's EnginesSsmParam, then the export
  # that carries the same value. Answer the generic list-exports here and update.sh never sees
  # an engine stack at all, so the P6 gate below cannot be tested. Behind a flag because
  # standup.sh reads the same pair: resolve an engine stack for a profile whose capture has no
  # params/60-engines and its preflight refuses to build at all.
  *"cloudformation list-exports"*"EnginesSsmParam"*)
    [ "${STUB_ENGINES_LIVE:-0}" = 1 ] && echo "af-ecs-engines-EnginesSsmParam" || echo "" ;;
  *"cloudformation list-exports"*) echo "t-cluster" ;;
  *"--profile p2"*"describe-stack-resource"*) echo "t-db" ;;
  *"describe-stack-resource"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EfsId']"*) echo "fs-1" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotLaunchTemplateId']"*) echo "lt-NEW" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='CfnTemplatesBucket']"*) echo "t-cfn-bucket" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotAmiIdArm64']"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='Url']"*) echo "https://af.example.test" ;;
  # --- the speech-engine stack (50-tts). Its existence probe must be able to say "no":
  # standup scales a NEWLY CREATED service to 0 and must leave an existing one alone, so a
  # fake that always answers "the stack is there" would make that branch untestable.
  *"cloudformation describe-stacks --stack-name af-ecs-tts") [ "${STUB_TTS_EXISTS:-0}" = 1 ] || exit 1 ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='TtsEcsService']"*) echo "af-af-ecs-tts-voicevox" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='VoicevoxUrl']"*) echo "http://voicevox.af.internal:50021" ;;
  *"ParameterKey=='EngineImageTag'"*) echo "cpu-ubuntu24.04-0.25.2" ;;
  # --- the inference-engine stack (60-engines, ADR 0071). Same shape as 50-tts above: the
  # existence probe has to be able to say "no", or the "scale a NEWLY created service to 0"
  # branch cannot be tested — and an engine left at desired 1 is $1.26/hour, ten times the
  # speech engine's.
  *"cloudformation describe-stacks --stack-name af-ecs-engines") [ "${STUB_ENGINES_EXISTS:-0}" = 1 ] || exit 1 ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EnginesSsmParam']"*) echo "/af-ws/engines" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='LlmServiceName']"*) echo "af-af-ecs-engines-llm" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='ImageServiceName']"*) echo "af-af-ecs-engines-image" ;;
  *"ParameterKey=='LlmImageTag'"*) echo "server-cuda" ;;
  # The engine tools tag the LIVE stack asks for. Settable to empty on purpose: that is the
  # state of every stack on the update that INTRODUCES the parameter, and then the value the
  # stack is about to take is cfn/60-engines.yaml's own Default.
  *"ParameterKey=='EngineToolsImageTag'"*) echo "${STUB_ET_WANT-2026-09-11}" ;;
  *"ParameterKey=='LlmApiKeySsmParam'"*) echo "/af-ws/engine-llm-key" ;;
  *"ParameterKey=='EnginesSsmParam'"*)
    [ "${STUB_ENGINES_LIVE:-0}" = 1 ] && echo "/af-ws/engines" || echo "" ;;
  # A LIVE engine stack from before ADR 0072 P6: both roles were switched on by naming a model
  # key, and `<Role>Enabled` was never set. This is the state the production deployment was
  # measured in on 2026-09-11, and the one an update silently deletes both roles from.
  # STUB_ENGINES_PRE_P6 unset = a stack already through P6: the key is not a parameter any more.
  *"ParameterKey=='LlmEnabled'"*|*"ParameterKey=='ImageEnabled'"*) echo "" ;;
  *"ParameterKey=='LlmModelS3Key'"*)
    [ "${STUB_ENGINES_PRE_P6:-0}" = 1 ] && echo "llm/model.gguf" || echo "" ;;
  *"ParameterKey=='ImageModelS3Key'"*)
    [ "${STUB_ENGINES_PRE_P6:-0}" = 1 ] && echo "image/checkpoints/sd_xl_base_1.0.safetensors" || echo "" ;;
  # For capture-env.sh: the parameter and output listings (join form). NatEipAllocationId
  # reproduces exactly the shape that was hit for real — empty as a parameter, but with a
  # real value in the outputs.
  # The stack name appears before --query. Get the glob order wrong and everything falls
  # through to the catch-all branch below, and the test lies in the shape of "nothing
  # happens".
  *"t-network"*"Parameters[].join"*) printf 'VpcCidr=10.20.0.0/16\nNatEipAllocationId=\n' ;;
  *"t-network"*"Outputs[].join"*)    printf 'NatEipAllocationId=eipalloc-REAL\nVpcId=vpc-1\n' ;;
  *"Parameters[].join"*)             printf 'Fqdn=af.example.test\n' ;;
  *"Outputs[].join"*)                printf '\n' ;;
  *"ParameterKey=='Fqdn'"*) echo "af.example.test" ;;
  *"ParameterKey=='NetworkStackName'"*) echo "t-network" ;;
  *"ParameterKey=='DataStackName'"*) echo "t-data" ;;
  *"ParameterKey=='PlatformStackName'"*) echo "t-platform" ;;
  *"ParameterKey=='WsRuntime'"*) echo "ecs-ec2" ;;
  *"ParameterKey=='ImageTag'"*) echo "9.9.9-dev-test" ;;
  *"--profile p2"*"ParameterKey=='Persistence'"*) echo "retain" ;;
  *"ParameterKey=='Persistence'"*) echo "delete" ;;
  *"ParameterKey=='CpArch'"*) echo "x86_64" ;;
  *"ParameterKey=='Ec2SlotLaunchTemplate'"*) echo "lt-OLD" ;;
  *"ParameterKey=='SsmPrefix'"*) echo "/af-cp" ;;
  *"ParameterKey=='HostedZoneId'"*) echo "ZTEST" ;;
  *"ParameterKey=='Ec2SlotSleepSec'"*) echo "900" ;;
  # --- 20-platform through a change set (update.sh step 1a). `deploy --no-execute-changeset`
  # answers with the review command, and the ARN inside it is the only handle to what it built;
  # describe-change-set is then what says whether anything is REPLACED. Both knobs default to
  # the ordinary case (there are changes, nothing is replaced).
  *"cloudformation deploy"*"--no-execute-changeset"*)
    if [ "${STUB_PLATFORM_CHANGES:-1}" = 0 ]; then
      echo "No changes to deploy. Stack t-platform is up to date"
    else
      echo "Waiting for changeset to be created.."
      echo "Changeset created successfully. Run the following command to review changes:"
      echo "aws cloudformation describe-change-set --change-set-name arn:aws:cloudformation:ap-northeast-1:123456789012:changeSet/awscli-cloudformation-package-deploy-1/abcd"
    fi ;;
  *"cloudformation describe-change-set"*)
    printf 'Add\tEcrEngineTools\tNone\n'
    printf 'Modify\tCpTaskRole\tFalse\n'
    [ "${STUB_PLATFORM_REPLACE:-0}" = 1 ] && printf 'Modify\tEcrControlPlane\tTrue\n'
    : ;;
  *"cloudformation describe-stacks"*) echo "STACK" ;;
  *"ecs list-services"*) echo "arn:aws:ecs:x:1:service/t-cluster/af-ws-alice" ;;
  # Do not confuse the runningCount query (waiting for the CP to stop) with the listing of
  # services whose desired count is > 0. Return a name for the former and the wait loop
  # spins for five minutes.
  *"ecs describe-services"*"runningCount"*) echo "0" ;;
  *"ecs describe-services"*) echo "af-ws-alice" ;;
  *"ecs list-container-instances"*) echo "arn:aws:ecs:x:1:container-instance/ci-1" ;;
  *"ecs list-task-definitions"*) echo "arn:aws:ecs:x:1:task-definition/af-ws-alice:1" ;;
  *"ec2 describe-instances"*)
    if [ "${STUB_NO_SLOTS:-0}" = 1 ]; then echo ""; else echo "i-1"; fi ;;
  *"ec2 describe-volumes"*) echo "vol-1" ;;
  *"ec2 describe-snapshots"*) echo "snap-1" ;;
  *"efs describe-access-points"*) echo "fsap-1" ;;
  *"ssm describe-parameters"*) echo "/af-ws/alice" ;;
  # af-engine-tools has its own knob, separate from the release images: the case that matters
  # is "the release tag IS in ECR and this one is not", which is exactly what happened on the
  # deployment (nothing had ever copied it there).
  *"ecr describe-images"*af-engine-tools*) [ "${STUB_ET_IN_ECR:-0}" = 1 ] || exit 1 ;;
  *"ecr describe-images"*)
    # After a teardown the ECR is empty. Forces standup down the crane copy path.
    [ "${STUB_ECR_HAS:-0}" = 1 ] || exit 1 ;;
  # The engine's own --api-key is the one secret standup CREATES rather than merely checks
  # for (it is machine-generated; an operator cannot usefully choose it). So the probe has to
  # be able to answer "not there" — otherwise the generate branch is never exercised, and a
  # real stand-up would fail at the engine task with a ResourceNotFoundException that reads
  # as a broken stack.
  *"ssm get-parameter --name /af-ws/engine-llm-key"*) [ "${STUB_ENGINE_KEY_EXISTS:-0}" = 1 ] || exit 1 ;;
  *"ssm get-parameter"*) echo "ok" ;;
  *"iam get-role"*) echo "ok" ;;
  *"route53 get-hosted-zone"*) echo "ok" ;;
  *"route53 list-resource-record-sets"*) printf '_abc.af.example.test.\t300\tval.acm-validations.aws.\n' ;;
  *"logs describe-log-groups"*) echo "" ;;
  *"ecs list-clusters"*) echo "" ;;
  *"rds describe-db-snapshots"*) echo "t-data-snapshot-db-xyz" ;;
  *"rds describe-db-instances"*) echo "" ;;
  *"efs describe-file-systems"*) echo "" ;;   # checking after deletion, so empty
esac
FAKE
cat > "$STUB/crane" <<'FAKE'
#!/usr/bin/env bash
echo "crane $*" >> "$STUB_LOG"
case "$1" in
  # Return a two-architecture index (the shape of a released CP image). Return only one and
  # the --cp-arch arm64 path fails preflight, so what comes after it is never exercised.
  manifest)
    # "is it in GHCR" is asked through the manifest. engine-tools has to be able to answer no:
    # nothing on the release route may bake an image, so that answer is where update.sh stops.
    case "$*" in
      *engine-tools*) [ "${STUB_ET_IN_GHCR:-1}" = 1 ] || exit 1 ;;
    esac
    echo '{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}]}' ;;
  auth) cat >/dev/null ;;
esac
FAKE
cat > "$STUB/curl" <<'FAKE'
#!/usr/bin/env bash
echo "curl $*" >> "$STUB_LOG"
echo 200
FAKE
chmod +x "$STUB/aws" "$STUB/crane" "$STUB/curl"
export PATH="$STUB:$PATH" STUB_LOG="$LOG"

fail() { echo "NG: $1"; echo "--- log ---"; cat "$LOG"; exit 1; }
# Under `set -o pipefail` a grep that finds nothing fails the whole pipeline, and set -e
# then kills the script before the assertion is reported. Finding nothing is valid input
# here, so swallow it explicitly.
lineno() { local n; n="$(grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1)" || true; echo "$n"; }
lineno_last() { local n; n="$(grep -nF -- "$1" "$LOG" | tail -1 | cut -d: -f1)" || true; echo "$n"; }
has() { grep -qF -- "$1" "$LOG" || fail "missing: $1"; }
hasnt() { ! grep -qF -- "$1" "$LOG" || fail "must not happen: $1"; }
order() { # order <earlier> <later>
  local a b; a="$(lineno "$1")"; b="$(lineno "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || fail "order: '$1' must precede '$2' (a=${a:-?} b=${b:-?})"
}
# For when the same call appears twice (the enumeration at plan time, and the re-enumeration
# after the CP has stopped). Seeing that the last occurrence comes later is what shows the
# re-enumeration really ran.
order_again() { # order_again <earlier> <repeated-later>
  local a b; a="$(lineno "$1")"; b="$(lineno_last "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || fail "order: '$2' must be re-read after '$1' (a=${a:-?} b=${b:-?})"
}

echo "== case 1: teardown without --yes touches nothing =="
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress > "$WORK/out1" </dev/null
hasnt "delete-stack"
hasnt "delete-service"
hasnt "terminate-instances"
hasnt "delete-volume"
hasnt "update-service"
grep -q "cannot be undone" "$WORK/out1" || fail "the plan did not say what it would do"

echo "== case 2: teardown order =="
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out2" </dev/null
# 1. Stopping the CP comes first. A running CP recreates things as fast as you delete them.
order "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0" \
      "ecs delete-service --cluster t-cluster --service arn:aws:ecs:x:1:service/t-cluster/af-ws-alice"
# 2. Delete the slots, then the home volumes (terminate does not remove them)
order "ec2 terminate-instances" "ec2 delete-volume --volume-id vol-1"
# 3. EFS access points, then the data-layer stack (10-data stalls if they go second)
order "efs delete-access-point" "cloudformation delete-stack --stack-name t-data"
# 4. Stacks in reverse order, and one at a time, waiting before moving on
order "cloudformation delete-stack --stack-name t-ingress" "cloudformation wait stack-delete-complete --stack-name t-ingress"
order "cloudformation wait stack-delete-complete --stack-name t-ingress" "cloudformation delete-stack --stack-name t-pool"
order "cloudformation wait stack-delete-complete --stack-name t-pool" "cloudformation delete-stack --stack-name t-platform"
order "cloudformation wait stack-delete-complete --stack-name t-platform" "cloudformation delete-stack --stack-name t-data"
order "cloudformation wait stack-delete-complete --stack-name t-data" "cloudformation delete-stack --stack-name t-network"
# 5. Re-count the leftovers only after confirming the CP has stopped (mid-teardown the CP can
#    rebake the golden image and wake slots, and terminating from the list taken before it ran
#    leaves orphans)
order_again "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0" \
            "ec2 describe-instances"
# 6. By default secrets are kept (so it can be stood up again in the same account)
has "ssm delete-parameter --name /af-ws/alice"
hasnt "ssm delete-parameter --name /af-cp"
# 7. ACM's validation CNAME is sent back with the TTL and value matching exactly
has '"TTL":300'
has "val.acm-validations.aws."

echo "== case 3: standup order and the launch template hand-off =="
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3" </dev/null
order "cloudformation deploy --stack-name t-network" "cloudformation deploy --stack-name t-data"
order "cloudformation deploy --stack-name t-data" "cloudformation deploy --stack-name t-platform"
# 20 creates the ECR. The image goes in after that, and before 30.
order "cloudformation deploy --stack-name t-platform" "crane copy ghcr.io/k-k1/agent-fleet/control-plane:9.9.9-dev-test"
order "crane copy ghcr.io/k-k1/agent-fleet/workspace:9.9.9-dev-test" "cloudformation deploy --stack-name t-pool"
order "cloudformation deploy --stack-name t-pool" "cloudformation deploy --stack-name t-ingress"
# Get a capability wrong and it is refused immediately
grep -q "deploy --stack-name t-data .*CAPABILITY_AUTO_EXPAND" "$LOG" || fail "10-data needs CAPABILITY_AUTO_EXPAND"
grep -q "deploy --stack-name t-platform .*CAPABILITY_NAMED_IAM" "$LOG" || fail "20-platform needs CAPABILITY_NAMED_IAM"
grep -q "deploy --stack-name t-pool .*CAPABILITY_NAMED_IAM" "$LOG" || fail "40-ec2-pool needs CAPABILITY_NAMED_IAM"
# The rebuilt pool's *new* launch template has to reach 30. Leave the old value in and both
# CFN and the CP succeed while the slots alone never come up again.
grep -q "deploy --stack-name t-ingress .*Ec2SlotLaunchTemplate=lt-NEW" "$LOG" || fail "30-ingress got a stale launch template"
hasnt "Ec2SlotLaunchTemplate=lt-OLD"
grep -q "deploy --stack-name t-ingress .*ImageTag=9.9.9-dev-test" "$LOG" || fail "30-ingress did not get the deployed tag"
# Speech is opt-in and this capture did not opt in, so nothing about it may happen. This is
# also the control for case 3f below: without it, a 50-tts step that never ran and a 50-tts
# step that ran for everyone would look the same.
hasnt "deploy --stack-name af-ecs-tts"
hasnt "TtsEcsService="
hasnt "AF_VOICEVOX_URL"
hasnt "voicevox_engine"
# The same control for the inference engines: they are opt-in too, and a GPU box that
# appears because a template exists in the repo is $918/month.
hasnt "deploy --stack-name af-ecs-engines"
hasnt "EnginesSsmParam="
hasnt "ggml-org/llama.cpp"
# Does the flag reach the value that is actually passed? Passing the check and then standing
# up on the default value is something that really happened.
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --cp-arch arm64 > /dev/null </dev/null
grep -q "deploy --stack-name t-ingress .*CpArch=arm64" "$LOG" || fail "--cp-arch did not reach the CFN parameters"
if grep -q "deploy --stack-name t-ingress .*CpArch=x86_64" "$LOG"; then fail "the captured CpArch overrode the flag"; fi

echo "== case 3f: the speech engine is built between the pool and ingress (ADR 0070) =="
#
# 50-tts is optional and deployed LATE, and both facts are what make the order fragile:
#
#   - after 20-platform, because it imports the cluster, the exec role and the namespace;
#   - BEFORE 30-ingress, because 30-ingress is handed its two outputs. Slip it after and the
#     stack deploys perfectly while the CP never learns where the engine is — the feature
#     just silently stays on Polly, which is exactly what ADR 0070 found had been true since
#     ADR 0013;
#   - the image can only be copied in AFTER the stack exists, since 50-tts owns the ECR
#     repository;
#   - and the freshly created service must be scaled to 0. CloudFormation starts a new
#     service at desired 1 (the template omits DesiredCount on purpose so that later updates
#     leave the count alone), so forgetting this leaves a $0.12/hour engine running that
#     nobody asked for.
: > "$LOG"
"$ECS/standup.sh" --profile p3 --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3f" </dev/null
order "cloudformation deploy --stack-name t-pool" "cloudformation deploy --stack-name af-ecs-tts"
order "cloudformation deploy --stack-name af-ecs-tts" "cloudformation deploy --stack-name t-ingress"
# The image goes in BEFORE the stack, not after. CloudFormation blocks on ECS service
# stabilisation, so a service created against an empty repository leaves the stack in
# CREATE_IN_PROGRESS repeating CannotPullContainerError — and no later step can rescue it,
# because the deploy never returns. Measured on the first real stand-up of this template;
# the repository is a 20-platform resource so that this ordering is possible at all.
order "crane copy docker.io/voicevox/voicevox_engine:cpu-ubuntu24.04-0.25.2" "cloudformation deploy --stack-name af-ecs-tts"
order "cloudformation deploy --stack-name t-platform" "crane copy docker.io/voicevox/voicevox_engine:cpu-ubuntu24.04-0.25.2"
has "ecs update-service --cluster t-cluster --service af-af-ecs-tts-voicevox --desired-count 0"
order "cloudformation deploy --stack-name af-ecs-tts" "ecs update-service --cluster t-cluster --service af-af-ecs-tts-voicevox --desired-count 0"
# The values 30-ingress is given come from the stack's outputs, not from the capture.
grep -q "deploy --stack-name t-ingress .*TtsEcsService=af-af-ecs-tts-voicevox" "$LOG" \
  || fail "30-ingress did not get the engine's service name (the CP would never drive it)"
grep -q "deploy --stack-name t-ingress .*VoicevoxUrl=http://voicevox.af.internal:50021" "$LOG" \
  || fail "30-ingress did not get the engine's URL (the CP would keep its 127.0.0.1 default)"
# An engine that is already there is somebody's running engine. Re-running a stand-up from
# the middle must not stop it.
: > "$LOG"
STUB_TTS_EXISTS=1 "$ECS/standup.sh" --profile p3 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
hasnt "--service af-af-ecs-tts-voicevox --desired-count 0"

echo "== case 3g: the inference engines are built between speech and ingress (ADR 0071) =="
#
# The same ordering trap as 50-tts, with a more expensive failure at the end of it:
#
#   - after 20-platform (it imports the cluster, the exec role, the namespace and the
#     af-llamacpp repository) and BEFORE 30-ingress (which is handed EnginesSsmParam —
#     slip it after and everything deploys while the CP never learns the engine exists);
#   - the image goes into ECR BEFORE the stack, for the CloudFormation-stabilisation reason
#     50-tts documents above, and for a measured one: GHCR through the NAT is 12-14 MB/s;
#   - the engine's own --api-key is generated into SSM if it is not there, because the task
#     cannot start without it and the failure reads as a broken stack, not a missing secret;
#   - and the freshly created service must be scaled to 0.
: > "$LOG"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3g" 2>&1 </dev/null || { cat "$WORK/out3g"; fail "standup for p4 failed"; }
order "cloudformation deploy --stack-name t-pool" "cloudformation deploy --stack-name af-ecs-engines"
order "cloudformation deploy --stack-name af-ecs-engines" "cloudformation deploy --stack-name t-ingress"
order "crane copy ghcr.io/ggml-org/llama.cpp:server-cuda" "cloudformation deploy --stack-name af-ecs-engines"
order "cloudformation deploy --stack-name t-platform" "crane copy ghcr.io/ggml-org/llama.cpp:server-cuda"
grep -q "deploy --stack-name af-ecs-engines .*CAPABILITY_NAMED_IAM" "$LOG" \
  || fail "60-engines creates named IAM roles and needs CAPABILITY_NAMED_IAM"
has "ssm put-parameter --cli-input-json"
has "ecs update-service --cluster t-cluster --service af-af-ecs-engines-llm --desired-count 0"
order "cloudformation deploy --stack-name af-ecs-engines" "ecs update-service --cluster t-cluster --service af-af-ecs-engines-llm --desired-count 0"
# The image role (ADR 0071 P1) is the same shape, with two differences that are easy to get
# wrong: its image comes from a different upstream, and it has NO generated key at all
# (sd-server has no authentication option — the security group is the whole of it).
order "crane copy ghcr.io/leejet/stable-diffusion.cpp:master-cuda" "cloudformation deploy --stack-name af-ecs-engines"
has "ecs update-service --cluster t-cluster --service af-af-ecs-engines-image --desired-count 0"
grep -q "deploy --stack-name t-ingress .*EnginesSsmParam=/af-ws/engines" "$LOG" \
  || fail "30-ingress did not get the engine table's SSM name (the gateway would 404)"
# ⚠️ The key is machine-generated and must never reach an argument. An argv is in
# /proc/<pid>/cmdline for anything on the host to read, and it lands in every shell trace and
# command log that happens to be on — which is exactly how this check first fired.
if grep -qE "put-parameter .*--value [A-Za-z0-9]{20,}" "$LOG"; then fail "the generated engine key was passed as an argument"; fi
# An engine that already exists is somebody's running engine — and a GPU one, so stopping it
# mid-answer also throws away a nine-minute cold start.
: > "$LOG"
STUB_ENGINES_EXISTS=1 STUB_ENGINE_KEY_EXISTS=1 "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
hasnt "--service af-af-ecs-engines-llm --desired-count 0"
hasnt "--service af-af-ecs-engines-image --desired-count 0"
# And a key that is already there is never rotated: the CP is holding the old value, and
# replacing it under a running engine locks the gateway out of it.
hasnt "ssm put-parameter"
# A deployment that runs only an LLM must not pay for the 2.3 GB image it will never start.
# The condition is the same one that decides whether the service exists at all — without
# ImageEnabled 60-engines creates no image service, so there is nothing to pull.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
hasnt "crane copy ghcr.io/leejet/stable-diffusion.cpp"
has "crane copy ghcr.io/ggml-org/llama.cpp:server-cuda"

# A capture taken before ADR 0072 phase P6 names a model key and says nothing about Enabled,
# because until P6 the key ALSO decided whether the role's service existed. Two things have to
# happen at once here, and either one alone is an outage:
#
#   - the retired parameters must not reach `deploy`, which refuses a key the template does not
#     declare ("Parameters: [LlmModelS3Key] do not exist in the template");
#   - what they implied must be carried over as `<role>Enabled=true`. Drop them without that and
#     the roles fall to the template default, i.e. a plain stack update DELETES a running engine
#     service — silently, with a nine-minute cold start and a GPU box behind it.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmModelS3Key=llm/model.gguf\nImageModelS3Key=image/checkpoints/sd_xl_base_1.0.safetensors\nImageImageTag=master-cuda\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
if grep -qE "deploy --stack-name af-ecs-engines .*(LlmModelS3Key|LlmModelIds|LlmContextTokens|LlmMaxOutputTokens|ImageModelS3Key|ImageModelIds)=" "$LOG"; then
  fail "a parameter retired in ADR 0072 P6 was passed to deploy (the CLI refuses it)"
fi
grep -q "deploy --stack-name af-ecs-engines .*LlmEnabled=true" "$LOG" \
  || fail "the llm role implied by LlmModelS3Key was not carried over (the update would delete it)"
grep -q "deploy --stack-name af-ecs-engines .*ImageEnabled=true" "$LOG" \
  || fail "the image role implied by ImageModelS3Key was not carried over"
# And the sd-server image is read the same way, so the role is never created against an empty
# ECR repository — the stabilisation trap the ordering above exists for.
has "crane copy ghcr.io/leejet/stable-diffusion.cpp"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageImageTag=master-cuda\n' > "$STATE4/params/60-engines"

# Buying the image role's box on Spot is a captured parameter like any other, and it has to
# travel: it is the ONE parameter of this stack whose change replaces a resource, so a path
# that quietly dropped it would leave the box on demand while the capture says otherwise.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageImageTag=master-cuda\nImageCapacityOptionType=SPOT\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
grep -q "deploy --stack-name af-ecs-engines .*ImageCapacityOptionType=SPOT" "$LOG" \
  || fail "ImageCapacityOptionType did not reach the deploy (the box would stay on demand)"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageImageTag=master-cuda\n' > "$STATE4/params/60-engines"

# 🔴 And in the template the type and the NAME move together. The field is create-only, so a
# switch is a replacement, and CloudFormation refuses to replace a custom-named resource that
# keeps its name (measured 2026-09-11: `cannot update a stack when a custom-named resource
# requires replacing`). Change one of these two without the other and every Spot deployment
# stops on that refusal, having already rolled back — which no test above would notice.
grep -q "CapacityOptionType: !Ref ImageCapacityOptionType" "$ECS/cfn/60-engines.yaml" \
  || fail "the image provider no longer reads ImageCapacityOptionType"
grep -q 'Sub "af-${AWS::StackName}-image-spot"' "$ECS/cfn/60-engines.yaml" \
  || fail "the image provider's name does not move with the option type (a switch cannot deploy)"

echo "== case 3h: update.sh carries a pre-P6 role over instead of deleting it =="
#
# update.sh redeploys 60-engines on every release and passes NO parameters, which is normally
# the safe thing: CloudFormation keeps every parameter at its previous value. ADR 0072 phase P6
# turned that into a hazard. `<Role>ModelS3Key` used to decide whether the role's SERVICE
# existed, and it is gone; a deployment that switched a role on by naming a key has
# `<Role>Enabled` empty, so "keep the previous value" now means NO SERVICE. The failure mode is
# the bad one: the update SUCCEEDS and the engine is deleted. Measured 2026-09-11 - the
# production deployment has both roles in exactly that state.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_PRE_P6=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h" 2>&1 \
  || { cat "$WORK/out3h"; fail "update.sh failed against a pre-P6 engine stack"; }
grep -q "deploy --stack-name af-ecs-engines .*--parameter-overrides LlmEnabled=true ImageEnabled=true" "$LOG" \
  || fail "update.sh did not carry the roles over (this update would delete both engine services)"
grep -q "LlmEnabled=true (it was implied by LlmModelS3Key" "$WORK/out3h" \
  || fail "the translation happened without saying so (an operator cannot see what changed)"
# The retired parameters themselves must never be passed: the template no longer declares them
# and `deploy` refuses a key it does not know.
if grep -qE "deploy --stack-name af-ecs-engines .*(LlmModelS3Key|ImageModelS3Key|LlmModelIds)=" "$LOG"; then
  fail "update.sh passed a parameter retired in ADR 0072 P6"
fi
# And 30-ingress still gets ImageTag and nothing else - the engine gate must not leak into the
# call the comment above it says never to add a parameter to.
grep -q "deploy --stack-name t-ingress .*--parameter-overrides ImageTag=9.9.9-dev-test --no-fail" "$LOG" \
  || fail "the ingress deploy no longer overrides ImageTag alone"

# A deployment already through P6 has no model key to read, and then this is byte for byte the
# update it always was. Without this the gate could pass by always sending the parameter, which
# would overwrite an administrator who deliberately switched a role OFF.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h2" 2>&1 \
  || { cat "$WORK/out3h2"; fail "update.sh failed against a post-P6 engine stack"; }
grep -q "deploy --stack-name af-ecs-engines" "$LOG" || fail "60-engines was not deployed at all"
if grep -q "deploy --stack-name af-ecs-engines .*--parameter-overrides" "$LOG"; then
  fail "a post-P6 stack was given parameters it does not need"
fi

# --dry-run has to SHOW the translation. An operator reads the plan to decide whether to run it,
# and "Enabled is about to be set" is the one thing in this deploy worth reading.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_PRE_P6=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --dry-run > "$WORK/out3h3" 2>&1 \
  || { cat "$WORK/out3h3"; fail "update.sh --dry-run failed"; }
grep -q "DRY: aws cloudformation deploy --stack-name af-ecs-engines .*--parameter-overrides LlmEnabled=true ImageEnabled=true" "$WORK/out3h3" \
  || fail "--dry-run did not show the planned translation"
hasnt "cloudformation deploy --stack-name af-ecs-engines --template-file"   # nothing was run

echo "== case 3i: update.sh does repository -> image -> stack, in that order =="
#
# 🔴 ECR REPOSITORY (20-platform) -> IMAGE (crane copy) -> STACK (60-engines). 0.19.0 moved the
# engine's fetch and ingest steps into `af-engine-tools`, and the only thing that ever copied
# that image into ECR was `standup.sh` — which a release does not go through. What the hardware
# lane found on 2026-09-11, before it deployed: GHCR and ECR both empty, and 20-platform (which
# owns the repository) not updated either. Run in that state, 60-engines deploys perfectly and
# both roles' fetch containers plus the ingest task sit in CannotPullContainerError while the
# service reports a steady state. It took three hand-run steps that existed in no script.
#
# The order is the whole assertion, so it is checked as an order and not as a call set.
ET_DEFAULT="$(sed -n '/^  EngineToolsImageTag:$/,/^  [A-Za-z]/p' "$ECS/cfn/60-engines.yaml" \
  | sed -n 's/^ *Default: *//p' | head -1 | tr -d '"')"
[ -n "$ET_DEFAULT" ] || fail "cfn/60-engines.yaml declares no Default for EngineToolsImageTag"
ET_GHCR="crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i" 2>&1 \
  || { cat "$WORK/out3i"; fail "update.sh failed while carrying the engine tools image over"; }
order "cloudformation deploy --stack-name t-platform" "cloudformation execute-change-set"
order "cloudformation execute-change-set" "$ET_GHCR"   # the repository exists only after this
order "$ET_GHCR" "cloudformation deploy --stack-name af-ecs-engines"
# And the change set is READ OUT before it is executed — an unreviewed update to the stack that
# owns the repositories, the cluster and the task roles is not something a release does.
grep -q "· Add EcrEngineTools" "$WORK/out3i" || fail "the 20-platform change set was executed without showing it"
order "cloudformation describe-change-set" "cloudformation execute-change-set"

echo "== case 3i-2: a tag that is already in ECR is not copied again =="
# The control for the case above. A step that copies on every run passes 3i and is still wrong:
# it pulls an image that is already there on every release, and teaches the reader that the
# copy means nothing.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ET_IN_ECR=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i2" 2>&1 \
  || { cat "$WORK/out3i2"; fail "update.sh failed with the engine tools image already in ECR"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools"
grep -q "af-engine-tools:2026-09-11 is already in ECR" "$WORK/out3i2" \
  || fail "it did not say why it copied nothing"
has "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3i-3: GHCR has not got it either -- stop, and deploy nothing =="
# Nothing on the release route may bake an image (engine-tools-image.yml is dispatched by hand
# or by dev-deploy.sh), so this is where it has to stop. Stopping BEFORE 60-engines is the
# point: deploying it here is what produces a steady-state service that cannot pull.
: > "$LOG"
rc=0
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ET_IN_GHCR=0 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i3" 2>&1 || rc=$?
[ "$rc" != 0 ] || fail "update.sh carried on with no engine tools image anywhere"
grep -q "engine-tools-image.yml" "$WORK/out3i3" || fail "it did not say how to produce the image"
hasnt "cloudformation deploy --stack-name af-ecs-engines"
hasnt "cloudformation deploy --stack-name t-ingress"

echo "== case 3i-4: on the update that INTRODUCES the parameter, the template's Default is used =="
# The live stack has no EngineToolsImageTag to read on the release that adds it, and the value
# it is about to take is the new template's Default. Read only the live stack and the copy is
# either skipped or made under an empty tag — either way the stack comes up pointing at nothing.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ET_WANT="" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i4" 2>&1 \
  || { cat "$WORK/out3i4"; fail "update.sh failed against a stack without the parameter"; }
has "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:$ET_DEFAULT"

echo "== case 3i-5: a 20-platform change set that REPLACES something is handed back =="
# Replacing an ECR repository throws its images away and replacing a role breaks every task
# that names it. Neither is something a release does on its own, and the refusal has to come
# before anything else runs.
: > "$LOG"
rc=0
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_PLATFORM_REPLACE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i5" 2>&1 || rc=$?
[ "$rc" != 0 ] || fail "update.sh executed a change set that replaces a resource"
grep -q "EcrControlPlane" "$WORK/out3i5" || fail "it did not name what would be replaced"
hasnt "cloudformation execute-change-set"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools"
hasnt "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3i-6: 20-platform is deployed only on the round where something moved =="
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_PLATFORM_CHANGES=0 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i6" 2>&1 \
  || { cat "$WORK/out3i6"; fail "update.sh failed on an empty 20-platform change set"; }
hasnt "cloudformation execute-change-set"
grep -q "nothing moved in 20-platform" "$WORK/out3i6" || fail "an empty change set was not reported"
# The rest of the release is unaffected by it.
has "cloudformation deploy --stack-name af-ecs-engines"
has "cloudformation deploy --stack-name t-ingress"

echo "== case 3i-7: --dry-run shows the order and the plan, and runs none of it =="
# 🔴 Assert on the OUTPUT here, not on the call log: a dry run executes nothing, so an
# assertion on the log passes just as happily for a step that decided to do nothing at all.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --dry-run > "$WORK/out3i7" 2>&1 \
  || { cat "$WORK/out3i7"; fail "update.sh --dry-run failed"; }
grep -q "1. t-platform (20-platform" "$WORK/out3i7" || fail "the plan did not name 20-platform first"
grep -q "4. af-engine-tools:2026-09-11 into ECR, then af-ecs-engines" "$WORK/out3i7" \
  || fail "the plan did not show the image going in before the engines stack"
grep -q "DRY: crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11" "$WORK/out3i7" \
  || fail "the dry run did not show the copy it would make"
hasnt "cloudformation execute-change-set"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools"

echo "== case 3b: a template over 51,200 bytes is handed over via S3 =="
#
# Without this it happens all over again. The moment 30-ingress.yaml went over 51,200 bytes,
# every path that deploys it stopped (standup and update both call the same
# `cloudformation deploy --template-file`). The AWS CLI refuses on file size before it calls
# the API, so the symptom surfaces as a CLI error, not a CFN one. And because teardown →
# rebuild had never been run end to end, the path that *creates* ingress had not run once in
# nearly three months (docs/log/73 §73.7.2).
#
# The decision is made on size, not on the name. Fatten one template and check that
# --s3-bucket appears only then.
: > "$LOG"
FATCFN="$WORK/cfn"; mkdir -p "$FATCFN"; cp "$ECS"/cfn/*.yaml "$FATCFN"/
python3 - "$FATCFN/30-ingress.yaml" <<'PYEOF'
import sys
p = sys.argv[1]
with open(p, "a") as f:
    f.write("\n# pad " + "x" * 60000 + "\n")
PYEOF
[ "$(wc -c < "$FATCFN/30-ingress.yaml")" -gt 51200 ] || fail "the padding had no effect"
[ "$(wc -c < "$FATCFN/00-network.yaml")" -le 51200 ] || fail "00-network is large too (the premise no longer holds)"
AF_STANDUP_CFN_DIR="$FATCFN" "$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3b" </dev/null
grep -q "deploy --stack-name t-ingress .*--s3-bucket t-cfn-bucket" "$LOG" \
  || fail "the large 30-ingress was passed without --s3-bucket (the same incident again)"
# Small templates keep going the old way (routing them through S3 needs extra permissions and
# extra cleanup)
if grep -q "deploy --stack-name t-network .*--s3-bucket" "$LOG"; then
  fail "even small templates are being routed through S3"
fi

echo "== case 3b-2: the shipped templates stay inside 51,200 bytes =="
#
# case 3b covers the fallback -- hand it over through S3 once it is too big. This checks the
# step before that: not going over in the first place. The S3 route needs 20-platform's
# bucket, depends on the teardown order, and the CLI's error leaves nothing in CFN's events.
# Stay inside the wall and none of that applies.
#
# It fails rather than warns. 30-ingress grew silently to 54,681 bytes and nobody noticed
# until a stand-up three months later. The job here is to make the change that fattens it
# fail on the spot, so moving the limit has to be deliberate and shows up in the diff.
# Long prose belongs in cfn/PARAMETERS.md: a YAML comment counts toward the body exactly
# like a Description, so "move it to a #" saves nothing.
CFN_MAX=51200
for t in "$ECS"/cfn/*.yaml; do
  sz="$(wc -c < "$t" | tr -d ' ')"
  if [ "$sz" -gt "$CFN_MAX" ]; then
    echo "NG: $(basename "$t") is $sz bytes > $CFN_MAX -- move the prose into cfn/PARAMETERS.md"
    exit 1
  fi
done

echo "== case 3c: teardown empties the bucket before deleting 20-platform =="
# CFN cannot delete a bucket that still has contents. Skip this and 20-platform stops at
# DELETE_FAILED, so the teardown dies half way -- and the next stand-up again goes unproven.
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3c" </dev/null
order "s3 rm s3://t-cfn-bucket --recursive" "cloudformation delete-stack --stack-name t-platform"

echo "== case 3d: capture does not drop values that exist only in the outputs =="
#
# An empty parameter can be the mark of a branch that says "create it yourself", and the id
# of the thing created then exists only on the output side. Copy the parameters alone and the
# stand-up picks "create" again from the same empty value, orphaning the previous resource
# (a Retain'd EIP) — the egress address a customer put on their allow-list silently changes.
# This was hit for real on a full pass against a live deployment.
: > "$LOG"
CAPOUT="$WORK/capture"; rm -rf "$CAPOUT"
AF_DEPLOY_STATE_DIR="$CAPOUT" "$ECS/capture-env.sh" --profile p --region ap-northeast-1 --stack t-ingress > "$WORK/out3d" </dev/null
CAPFILE="$CAPOUT/p.ap-northeast-1.t-ingress/params/00-network"
[ -r "$CAPFILE" ] || fail "capture did not write 00-network"
grep -q "^NatEipAllocationId=eipalloc-REAL$" "$CAPFILE" \
  || fail "the empty parameter was not filled in from the output (the stand-up would orphan the EIP): $(grep NatEip "$CAPFILE" || echo '<no such line>')"
# Outputs that are not parameters must not be copied across (CFN rejects parameters it does not know)
if grep -q "^VpcId=" "$CAPFILE"; then fail "an output that is not a parameter was copied into the capture"; fi

echo "== case 3e: the preflight required-parameter check does not miss because of formatting =="
#
# This check (matching the capture against the templates) is the kind that shows nothing when
# it misses, and it did count on a hardcoded 2-space / 4-space indent. Feed it a template
# written differently and it finds 0 required parameters, so the whole point — "say what is
# missing before standing anything up" — silently disappears. It is the same shape as "a path
# that never runs can be broken without anyone knowing", so make it run here.
#
# Two runs:
#   3e-1 a template that differs only in depth -> it must still report the missing required parameter
#   3e-2 a template with the section present but nothing readable -> it must say it found nothing
: > "$LOG"
IDCFN="$WORK/cfn-indent"; mkdir -p "$IDCFN"; cp "$ECS"/cfn/*.yaml "$IDCFN"/
# 4-space indentation. `Fqdn` has no Default, so it is required, and the capture (VpcCidr only) lacks it.
cat > "$IDCFN/00-network.yaml" <<'YEOF'
AWSTemplateFormatVersion: '2010-09-09'
Description: a template written with 4-space indentation (a hardcoded depth reads none of it)
Parameters:
    VpcCidr:
        Type: String
        Default: 10.20.0.0/16
    ReindentProbe:
        Type: String
        Description: >-
            Has no Default, so it is required. The capture does not have it, so preflight should say so
Resources:
    Vpc:
        Type: AWS::EC2::VPC
        Properties:
            CidrBlock: !Ref VpcCidr
YEOF
if AF_STANDUP_CFN_DIR="$IDCFN" "$ECS/standup.sh" --profile p --region ap-northeast-1 \
     --stack t-ingress --yes > "$WORK/out3e1" 2>&1 </dev/null; then
  fail "a differently indented template let a missing required parameter through (the check is a no-op)"
fi
grep -q "required parameter ReindentProbe is missing" "$WORK/out3e1" \
  || fail "the reason for the missing parameter was not printed: $(tail -3 "$WORK/out3e1")"
# It must not have started standing anything up (the value of this check is that it stops before 00-20 are created)
hasnt "cloudformation deploy --stack-name t-network"

# 3e-2: the section is present but not one entry is readable (flow style). Do not pass quietly on "0 required".
: > "$LOG"
cat > "$IDCFN/00-network.yaml" <<'YEOF'
AWSTemplateFormatVersion: '2010-09-09'
Parameters: { VpcCidr: { Type: String, Default: 10.20.0.0/16 } }
Resources:
  Vpc:
    Type: AWS::EC2::VPC
YEOF
if AF_STANDUP_CFN_DIR="$IDCFN" "$ECS/standup.sh" --profile p --region ap-northeast-1 \
     --stack t-ingress --yes > "$WORK/out3e2" 2>&1 </dev/null; then
  fail "preflight passed although it could not read a single entry under Parameters: (the no-op is invisible)"
fi
grep -q "read no Parameters: at all" "$WORK/out3e2" \
  || fail "it did not declare that it read nothing: $(tail -3 "$WORK/out3e2")"
hasnt "cloudformation deploy --stack-name t-network"

echo "== case 4: pause stops the control plane LAST =="
: > "$LOG"
"$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4" </dev/null
order "ecs update-service --cluster t-cluster --service af-ws-alice --desired-count 0" "ec2 stop-instances"
order "ec2 stop-instances" "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0"

echo "== case 5: nothing prints a secret-looking parameter =="
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out5" </dev/null
if grep -q "must-not-be-printed" "$WORK/out5"; then fail "a secret-looking parameter value was printed"; fi
grep -q "BitbucketOauthKey=\*\*\*" "$WORK/out5" || fail "the masked form is missing"

echo "== case 6: retain — deletion protection comes off, but what was retained stays =="
: > "$LOG"
"$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out6" </dev/null
# Deletion protection comes off before the stack delete (leave it on and delete-stack fails there)
order "rds modify-db-instance --db-instance-identifier t-db --no-deletion-protection" \
      "cloudformation delete-stack --stack-name t-data"
# Do not touch what retain kept
hasnt "rds delete-db-snapshot"
hasnt "efs delete-file-system"
grep -q "retain" "$WORK/out6" || fail "it did not say that retain kept things"

echo "== case 7: retain + --purge-retained — delete everything, and confirm it is gone =="
: > "$LOG"
"$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --purge-retained > "$WORK/out7" </dev/null
# Deletion happens only after every stack is gone (do it earlier and delete-stack recreates
# the final snapshot, or holds on to it)
order "cloudformation wait stack-delete-complete --stack-name t-network" "rds delete-db-snapshot"
order "cloudformation wait stack-delete-complete --stack-name t-network" "efs delete-file-system"
has "rds delete-db-snapshot --db-snapshot-identifier t-data-snapshot-db-xyz"
has "efs delete-file-system --file-system-id fs-1"
# The sweep looks the real resources up again and counts them (nothing is left behind silently)
order "efs delete-file-system" "efs describe-file-systems --file-system-id fs-1"

echo "OK: deployment lifecycle stub test passed"

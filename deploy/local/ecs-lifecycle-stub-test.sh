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
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# A fifth (profile p5): a data stack name one character past what its EFS backup vault name
# leaves room for (37), and retain only in params/10-data — the value the deploy passes —
# while the recorded persistence still says delete.
STATE5="$AF_DEPLOY_STATE_DIR/p5.ap-northeast-1.t-ingress"
mkdir -p "$STATE5/params"
cp -a "$STATE/params/." "$STATE5/params/"
sed -e 's/^AF_PERSISTENCE=delete$/AF_PERSISTENCE=retain/' \
    -e 's/^AF_STACK_DATA=t-data$/AF_STACK_DATA=t-data-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' "$STATE/env" > "$STATE5/env"
echo "Persistence=retain" > "$STATE5/params/10-data"

export AF_TAG_GRACE_SEC=0   # pause.sh --up waits this long after clearing the af-paused tag
# The egress gateway's polls (readiness, drain, Workspace tasks): no sleeping in between.
export AF_GATEWAY_POLL_SEC=0

# --- fake aws. Answers queries in the same shape the real one does ----------
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
args="$*"
case "$args" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  # STUB_POOL_DEPLOY_FAIL=1: the slot pool's stack update fails (a rollback, a refused
  # capability). update.sh must then leave 20-platform untouched.
  *"cloudformation deploy --stack-name t-pool"*)
    if [ "${STUB_POOL_DEPLOY_FAIL:-0}" = 1 ]; then
      echo "Failed to create/update the stack. Status: UPDATE_ROLLBACK_COMPLETE" >&2; exit 255
    fi ;;
  *"cloudformation list-exports"*"SlotLaunchTemplateId"*)
    case "${STUB_POOL_EXPORT:-ok}" in
      fail) echo "An error occurred (AccessDenied) when calling the ListExports operation" >&2; exit 254 ;;
      none) echo "" ;;
      *)    echo "t-pool-SlotLaunchTemplateId" ;;
    esac ;;
  # How update.sh finds the engine stack: the ingress stack's EnginesSsmParam, then the export
  # that carries the same value. Answer the generic list-exports here and update.sh never sees
  # an engine stack at all, so the P6 gate below cannot be tested. Behind a flag because
  # standup.sh reads the same pair: resolve an engine stack for a profile whose capture has no
  # params/60-engines and its preflight refuses to build at all.
  *"cloudformation list-exports"*"EnginesSsmParam"*)
    [ "${STUB_ENGINES_LIVE:-0}" = 1 ] && echo "af-ecs-engines-EnginesSsmParam" || echo "" ;;
  *"cloudformation list-exports"*) echo "t-cluster" ;;
  # The EFS backup facts efs-backup-check.sh reads. STUB_VAULT: unset = a data stack without the
  # vault output (pre-0.26.0), ok = vault and a plan selecting fs-1, noplan = vault but the
  # plan selects another file system, fail = every one of these reads errors.
  *"Outputs[?OutputKey=='EfsBackupVaultName']"*)
    case "${STUB_VAULT:-}" in fail) exit 1 ;; ok|noplan) echo "t-data-efs-1" ;; *) echo "None" ;; esac ;;
  *"Stacks[0].StackId"*) echo "arn:aws:cloudformation:ap-northeast-1:111122223333:stack/t-data/u" ;;
  *"logical-resource-id EfsBackupPlan"*) echo "plan-1" ;;
  *"backup list-backup-selections"*) echo "sel-1" ;;
  *"backup get-backup-selection"*"length(NotResources"*) printf '0\t0\t0\n' ;;
  *"backup get-backup-selection"*"BackupSelection.Resources"*)
    [ "${STUB_VAULT:-}" = noplan ] && echo "arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/fs-other" \
      || echo "arn:aws:elasticfilesystem:ap-northeast-1:111122223333:file-system/fs-1" ;;
  # 00-network's GatewayEni (EgressRoute=gateway), before the database lookup below.
  *"logical-resource-id GatewayEni"*) echo "eni-gw" ;;
  *"--profile p2"*"describe-stack-resource"*) echo "t-db" ;;
  *"describe-stack-resource"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EfsId']"*) echo "fs-1" ;;
  # 00-network's workspace subnets (#2716). Unset = a network stack older than them.
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='WorkspaceSubnets']"*) echo "${STUB_WS_SUBNETS:-None}" ;;
  # 00-network's egress gateway outputs (EgressRoute=gateway, #2717). Unset = nat-gateway.
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EgressProxyAddr']"*) echo "${STUB_EGRESS_ADDR:-None}" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EgressTokenSecretArn']"*) echo "${STUB_EGRESS_SECRET:-None}" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotLaunchTemplateId']"*) echo "${STUB_POOL_LT_OUTPUT:-lt-NEW}" ;;
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
  # ADR 0083's migration guard (update.sh, before touching 20-platform): whether the LIVE
  # 60-engines stack still imports the export 20-platform's new template drops. Default "no
  # importer" is the state every deployment reaches once its own 60-engines has been updated
  # once; the flag simulates a deployment still on the pre-ADR-0083 template.
  *"cloudformation list-imports"*"EcrSdcppUri"*)
    [ "${STUB_ENGINES_IMPORTS_SDCPP:-0}" = 1 ] && echo "af-ecs-engines" || echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EnginesSsmParam']"*) echo "/af-ws/engines" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='LlmServiceName']"*) echo "af-af-ecs-engines-llm" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='ImageServiceName']"*) echo "af-af-ecs-engines-image" ;;
  *"ParameterKey=='LlmImageTag'"*) echo "server-cuda" ;;
  # The engine tools tag the LIVE stack asks for. Settable to empty on purpose: that is the
  # state of every stack on the update that INTRODUCES the parameter, and then the value the
  # stack is about to take is cfn/60-engines.yaml's own Default. The default answer here is a
  # tag that is NEITHER the template's Default nor the stale one 3i-7 repairs, so the ordinary
  # cases below show the LIVE value being honoured rather than agreeing by coincidence.
  *"ParameterKey=='EngineToolsImageTag'"*) echo "${STUB_ET_WANT-2026-09-16}" ;;
  # The ComfyUI tag the LIVE stack asks for. The default answer is a tag no template ever
  # shipped, i.e. somebody's choice, which update.sh must leave alone; 3i-9 sets the stale one.
  *"ParameterKey=='ImageComfyImageTag'"*) echo "${STUB_COMFY_WANT-v0.36.0}" ;;
  *"ParameterKey=='LlmApiKeySsmParam'"*) echo "/af-ws/engine-llm-key" ;;
  # `<Role>OfferBudgetSec` on the LIVE stack. 180 is the OLD meaning's default (a per-offer
  # purchase clock); since ADR 0077 the parameter bounds the box's ECS registration instead and
  # the template's default is 300. The flag has to be able to say "already 300", or the branch
  # that leaves a migrated deployment alone cannot be tested.
  *"ParameterKey=='LlmOfferBudgetSec'"*|*"ParameterKey=='ImageOfferBudgetSec'"*)
    [ "${STUB_ENGINES_OLD_BUDGET:-0}" = 1 ] && echo "180" || echo "300" ;;
  *"ParameterKey=='EnginesSsmParam'"*)
    [ "${STUB_ENGINES_LIVE:-0}" = 1 ] && echo "/af-ws/engines" || echo "" ;;
  # A LIVE engine stack from before ADR 0072 P6: both roles were switched on by naming a model
  # key, and `<Role>Enabled` was never set. This is the state the production deployment was
  # measured in on 2026-09-11, and the one an update silently deletes both roles from.
  # STUB_ENGINES_PRE_P6 unset = a stack already through P6: the key is not a parameter any more.
  # STUB_ENGINES_IMAGE_ON = a stack through P6 that runs the image role, i.e. one that pulls
  # af-comfyui. Left unset the role is off (or implied by a pre-P6 key, as above).
  *"ParameterKey=='LlmEnabled'"*) echo "" ;;
  *"ParameterKey=='ImageEnabled'"*) [ "${STUB_ENGINES_IMAGE_ON:-0}" = 1 ] && echo "true" || echo "" ;;
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
  # The egress gateway's address (#2728) has the same shape: created by the stack, so empty as
  # parameters and real in the outputs.
  *"t-network"*"Parameters[].join"*) printf 'VpcCidr=10.20.0.0/16\nNatEipAllocationId=\nGatewayEipAllocationId=\nGatewayEipAddress=\n' ;;
  *"t-network"*"Outputs[].join"*)    printf 'NatEipAllocationId=eipalloc-REAL\nVpcId=vpc-1\nGatewayEipAllocationId=eipalloc-GW\nGatewayEipAddress=203.0.113.7\n' ;;
  *"Parameters[].join"*)             printf 'Fqdn=af.example.test\n' ;;
  *"Outputs[].join"*)                printf '\n' ;;
  # pause.sh: whether 00-network's NAT gateway is on, and whether 10-data carries the re-stop Lambda.
  *"ParameterKey=='NatEnabled'"*) echo "${STUB_NAT_ENABLED:-true}" ;;
  # #2716: unset = a network / ingress stack older than the S3 endpoint switch / workspace subnets.
  *"ParameterKey=='S3EndpointEnabled'"*) echo "${STUB_S3EP:-None}" ;;
  *"ParameterKey=='WorkspaceSubnets'"*) echo "${STUB_ING_WS:-None}" ;;
  *"ParameterKey=='EgressRoute'"*) echo "${STUB_EGRESS_ROUTE:-None}" ;;
  # Unset = a gateway stack older than the switch (its gateway is on).
  *"ParameterKey=='GatewayEnabled'"*) echo "${STUB_GW_ENABLED:-None}" ;;
  *"ParameterKey=='EgressTokenSecretArn'"*) echo "${STUB_ING_EGRESS:-None}" ;;
  *"Outputs[?OutputKey=='RdsRestopFunction']"*) echo "${STUB_RESTOP:-}" ;;
  *"ParameterKey=='Fqdn'"*) echo "af.example.test" ;;
  *"ParameterKey=='NetworkStackName'"*) echo "t-network" ;;
  *"--profile p5"*"ParameterKey=='DataStackName'"*) echo "t-data-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ;;
  *"ParameterKey=='DataStackName'"*) echo "t-data" ;;
  *"ParameterKey=='PlatformStackName'"*) echo "t-platform" ;;
  *"ParameterKey=='WsRuntime'"*) echo "${STUB_WS_RUNTIME:-ecs-ec2}" ;;
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
  # pause.sh asks which engine / speech services are above desired 0. Answer the generic
  # branch below and a Workspace name comes back as an "engine service" — the sweep would then
  # scale a Workspace down and report it as an engine.
  *"ecs describe-services"*"af-af-ecs-engines-"*)
    [ "${STUB_ENGINE_SVC_UP:-0}" = 1 ] && echo "af-af-ecs-engines-image" || echo "" ;;
  # STUB_WS_DESIRED0=1: every workspace service is already at desired 0 (stopped by its user a
  # moment ago, or by an earlier pause cut short), while its task may still be running.
  *"ecs describe-services"*) [ "${STUB_WS_DESIRED0:-0}" = 1 ] && echo "" || echo "af-ws-alice" ;;
  *"ecs list-container-instances"*) echo "arn:aws:ecs:x:1:container-instance/ci-1" ;;
  *"ecs list-task-definitions"*) echo "arn:aws:ecs:x:1:task-definition/af-ws-alice:1" ;;
  # The engines' GPU boxes (af-role=engine-*), before the slot branch that answers everything.
  # STUB_ENGINE_BOXES=late: a box that exists only once the CP has been stopped — the one the
  # final sweep exists for, bought between the wait and the CP's own stop.
  *"ec2 describe-instances"*"af-role,Values=engine-"*)
    case "${STUB_ENGINE_BOXES:-0}" in
      1) echo "i-gpu" ;;
      late) grep -q "af-t-ingress-cp --desired-count 0" "$STUB_LOG" && echo "i-late" || echo "" ;;
      *) echo "" ;;
    esac ;;
  *"ec2 describe-instances"*)
    if [ "${STUB_NO_SLOTS:-0}" = 1 ]; then echo ""; else echo "i-1"; fi ;;
  # The egress gateway's interface and its checks through SSM. STUB_GW_ATTACH= (empty) is an
  # interface no instance has attached. STUB_GW_PROBE is what the readiness check answers;
  # "model" is a gateway whose agent gets its first policy only once the CP is up (the log is
  # the clock). STUB_GW_DRAIN is the drain's answer: drained, pending (never), or late (pending
  # on the first ask, a CP report that failed once).
  *"ec2 describe-network-interfaces"*)
    [ "${STUB_ENI_FAIL:-0}" = 1 ] && { echo "An error occurred (UnauthorizedOperation)" >&2; exit 254; }
    printf '%s\n' "${STUB_GW_ATTACH-attached	i-gw}" ;;
  *"ssm send-command"*"egress gateway drain"*) echo "cmd-drain" ;;
  *"ssm send-command"*) echo "cmd-probe" ;;
  *"ssm get-command-invocation --command-id cmd-drain"*)
    case "${STUB_GW_DRAIN:-drained}" in
      pending) printf 'Success\taf-gw-pending 120 bytes not yet accepted by the CP\n' ;;
      late) [ "$(grep -c "get-command-invocation --command-id cmd-drain" "$STUB_LOG")" -ge 2 ] \
              && printf 'Success\taf-gw-drained\n' || printf 'Success\taf-gw-pending 120 bytes\n' ;;
      *) printf 'Success\taf-gw-drained\n' ;;
    esac ;;
  *"ssm get-command-invocation"*)
    probe="${STUB_GW_PROBE:-af-gw-ready}"
    if [ "$probe" = model ]; then
      grep -q "af-t-ingress-cp --desired-count 1" "$STUB_LOG" && probe=af-gw-ready || probe=af-gw-up
    fi
    printf 'Success\t%s\n' "$probe" ;;
  # Workspace tasks still alive (pause.sh before it stops the egress gateway). One task of a
  # workspace service; STUB_WS_TASKS: gone (default), stuck, late (alive on the first look),
  # fail (the read is refused).
  *"ecs list-tasks"*"--desired-status RUNNING"*) echo "arn:aws:ecs:x:1:task/t-cluster/t1" ;;
  *"ecs list-tasks"*) echo "None" ;;
  *"ecs describe-tasks"*)
    case "${STUB_WS_TASKS:-gone}" in
      stuck) echo "arn:aws:ecs:x:1:task/t-cluster/t1" ;;
      late) [ "$(grep -c "ecs describe-tasks" "$STUB_LOG")" -ge 2 ] && echo "" || echo "arn:aws:ecs:x:1:task/t-cluster/t1" ;;
      fail) echo "An error occurred (AccessDeniedException) when calling the DescribeTasks operation" >&2; exit 254 ;;
      *) echo "" ;;
    esac ;;
  # The workspace egress address handed back to standup.sh (#2728): its address and the
  # interface holding it. STUB_GW_EIP: free (default), elsewhere (another interface), gone.
  *"ec2 describe-addresses --allocation-ids"*)
    case "${STUB_GW_EIP:-free}" in
      gone) echo "An error occurred (InvalidAllocationID.NotFound)" >&2; exit 254 ;;
      elsewhere) printf '203.0.113.7\teni-other\n' ;;
      *) printf '203.0.113.7\tNone\n' ;;
    esac ;;
  *"ec2 describe-volumes"*) echo "vol-1" ;;
  *"ec2 describe-snapshots"*) echo "snap-1" ;;
  *"efs describe-access-points"*) echo "fsap-1" ;;
  *"ssm describe-parameters"*) echo "/af-ws/alice" ;;
  # af-engine-tools has its own knob, separate from the release images: the case that matters
  # is "the release tag IS in ECR and this one is not", which is exactly what happened on the
  # deployment (nothing had ever copied it there).
  *"ecr describe-images"*af-engine-tools*) [ "${STUB_ET_IN_ECR:-0}" = 1 ] || exit 1 ;;
  # af-llamacpp also has its own knob: the case that matters for --llm-digest is "the tag is
  # ALREADY in ECR" (a repeat stand-up — the common case once the repository is no longer
  # empty), which never reaches the crane copy branch at all.
  # af-comfyui follows STUB_ECR_HAS unless told otherwise, so standup's cases are unchanged; the
  # update cases need "the release images are there and the new ComfyUI tag is not".
  *"ecr describe-images"*af-comfyui*) [ "${STUB_COMFY_IN_ECR:-${STUB_ECR_HAS:-0}}" = 1 ] || exit 1 ;;
  *"ecr describe-images"*af-llamacpp*) [ "${STUB_LLM_IN_ECR:-0}" = 1 ] || exit 1 ;;
  *"ecr describe-images"*)
    # After a teardown the ECR is empty. Forces standup down the crane copy path.
    [ "${STUB_ECR_HAS:-0}" = 1 ] || exit 1 ;;
  # The engine's own --api-key is the one secret standup CREATES rather than merely checks
  # for (it is machine-generated; an operator cannot usefully choose it). So the probe has to
  # be able to answer "not there" — otherwise the generate branch is never exercised, and a
  # real stand-up would fail at the engine task with a ResourceNotFoundException that reads
  # as a broken stack.
  *"ssm get-parameter --name /af-ws/engine-llm-key"*) [ "${STUB_ENGINE_KEY_EXISTS:-0}" = 1 ] || exit 1 ;;
  *"ssm get-parameter --name /af-cp/github-client-secret"*) [ "${STUB_NO_GH_KEY:-0}" = 1 ] && exit 1; echo ok ;;
  *"ssm get-parameter --name /af-cp/comfy-api-key"*) [ "${STUB_NO_COMFY_KEY:-0}" = 1 ] && exit 1; echo ok ;;
  *"ssm get-parameter"*) echo "ok" ;;
  *"iam get-role"*) echo "ok" ;;
  *"route53 get-hosted-zone"*) echo "ok" ;;
  *"route53 list-resource-record-sets"*) printf '_abc.af.example.test.\t300\tval.acm-validations.aws.\n' ;;
  *"logs describe-log-groups"*) echo "" ;;
  *"ecs list-clusters"*) echo "" ;;
  *"rds describe-db-snapshots"*) echo "t-data-snapshot-db-xyz" ;;
  # pause.sh reads one instance's status; teardown's listing below must keep answering "gone".
  *"rds describe-db-instances --db-instance-identifier"*"DBInstanceArn"*)
    [ "${STUB_DB_ARN_FAIL:-0}" = 1 ] && exit 254
    echo "arn:aws:rds:ap-northeast-1:123456789012:db:t-db" ;;
  # A database that was stopped answers 'available' once a start has been issued (the log is
  # the fake's clock): pause.sh --up reads it again after the CP is up.
  *"rds describe-db-instances --db-instance-identifier"*"DBInstanceStatus"*)
    if [ "${STUB_DB_STATUS:-available}" = stopped ] && grep -q "rds start-db-instance" "$STUB_LOG"; then
      echo available
    else
      echo "${STUB_DB_STATUS:-available}"
    fi ;;
  *"rds describe-db-instances"*) echo "" ;;
  *"efs describe-file-systems"*) echo "" ;;   # checking after deletion, so empty
  # One vault a retain period left, found by the "<data stack>-efs-" prefix whatever the
  # deployment's persistence is now; gone once teardown has deleted it. STUB_VAULT_ABSENT is
  # a retain deployment from before 10-data declared backups.
  # Only the query filtered to this deployment's prefix sees its vault; anything broader
  # also sees another deployment's, which teardown must never empty.
  *"backup list-backup-vaults"*"starts_with(BackupVaultName,'t-data-efs-')"*)
    [ "${STUB_VAULT_ABSENT:-0}" = 1 ] || grep -q "backup delete-backup-vault" "$STUB_LOG" \
      || echo "t-data-efs-1a2b3c4d" ;;
  *"backup list-backup-vaults"*) printf 't-data-efs-1a2b3c4d\tother-data-efs-deadbeef\n' ;;
  # The vault holds a point until teardown deletes it. Keep answering "one point" after
  # that and the wait for the vault to empty spins for five minutes. STUB_RP_STUCK is a
  # point that will not go (a backup job still running).
  *"backup list-recovery-points-by-backup-vault"*)
    { [ "${STUB_RP_STUCK:-0}" != 1 ] && grep -q "backup delete-recovery-point" "$STUB_LOG"; } \
      || echo "arn:aws:backup:ap-northeast-1:123456789012:recovery-point:rp-1" ;;
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
      *comfyui*) [ "${STUB_COMFY_IN_GHCR:-1}" = 1 ] || exit 1 ;;
    esac
    echo '{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}]}' ;;
  auth) cat >/dev/null ;;
  # standup.sh reads this back after every llm / ComfyUI copy to record what actually landed in
  # ECR. af-comfyui has its own knobs so a ComfyUI pin can be tested with the llm read-back fine.
  digest)
    case "$*" in
      *af-comfyui*)
        [ "${STUB_COMFY_DIGEST_FAILS:-0}" = 1 ] && exit 1
        echo "sha256:${STUB_COMFY_DIGEST:-fake000000000000000000000000000000000000000000000000000000000}" ;;
      *)
        [ "${STUB_CRANE_DIGEST_FAILS:-0}" = 1 ] && exit 1
        echo "sha256:${STUB_CRANE_DIGEST:-fake000000000000000000000000000000000000000000000000000000000}" ;;
    esac ;;
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

# Every deploy of a template that holds IAM resources must carry the capability, or
# CreateChangeSet refuses it (InsufficientCapabilitiesException) - on real AWS only, which is
# how 30-ingress went on being deployed without one after it gained an IAM policy (#1576).
# A template that names a role or policy needs CAPABILITY_NAMED_IAM, any other CAPABILITY_IAM.
iam_caps() {
  local line tpl n=0
  while IFS= read -r line; do
    tpl="$(printf '%s\n' "$line" | sed -n 's/.*--template-file \([^ ]*\).*/\1/p')"
    [ -n "$tpl" ] && [ -r "$tpl" ] || continue
    grep -q 'Type: AWS::IAM::' "$tpl" || continue
    n=$((n + 1))
    if grep -qE '^ +(RoleName|ManagedPolicyName|InstanceProfileName|PolicyName):' "$tpl"; then
      case "$line" in *CAPABILITY_NAMED_IAM*) ;; *) fail "IAM template deployed without CAPABILITY_NAMED_IAM: $line" ;; esac
    else
      case "$line" in *CAPABILITY_IAM*|*CAPABILITY_NAMED_IAM*) ;; *) fail "IAM template deployed without CAPABILITY_IAM: $line" ;; esac
    fi
  done < <(grep -F "cloudformation deploy " "$LOG" || true)
  [ "$n" -gt 0 ] || fail "iam_caps: no deploy of a template with IAM resources in the log (the check saw nothing)"
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
# 6. persistence=delete deletes no backups, but a vault left from a retain period is named in
#    the plan and counted in the sweep instead of vanishing from view.
hasnt "backup delete-"
grep -q "vault t-data-efs-1a2b3c4d .* is kept — persistence=delete" "$WORK/out2" || fail "the plan did not name the leftover vault and how to delete it"
grep -q "efs backup vaults *1" "$WORK/out2" || fail "the sweep did not count the leftover vault"
# 6. By default secrets are kept (so it can be stood up again in the same account)
has "ssm delete-parameter --name /af-ws/alice"
hasnt "ssm delete-parameter --name /af-cp"
# 7. ACM's validation CNAME is sent back with the TTL and value matching exactly
has '"TTL":300'
has "val.acm-validations.aws."

echo "== case 2b: standup refuses a retain data stack name its backup vault cannot carry =="
: > "$LOG"
if "$ECS/standup.sh" --profile p5 --region ap-northeast-1 --stack t-ingress > "$WORK/out2b" 2>&1 </dev/null; then
  fail "standup accepted a 38-character data stack name under retain"
fi
grep -q "must be at most 37" "$WORK/out2b" || fail "standup did not say why the data stack name was refused"
hasnt "cloudformation deploy"

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
grep -q "deploy --stack-name t-data .*CAPABILITY_IAM" "$LOG" || fail "10-data needs CAPABILITY_IAM (the EFS backup role)"
grep -q "deploy --stack-name t-platform .*CAPABILITY_NAMED_IAM" "$LOG" || fail "20-platform needs CAPABILITY_NAMED_IAM"
grep -q "deploy --stack-name t-pool .*CAPABILITY_NAMED_IAM" "$LOG" || fail "40-ec2-pool needs CAPABILITY_NAMED_IAM"
# The rebuilt pool's *new* launch template has to reach 30. Leave the old value in and both
# CFN and the CP succeed while the slots alone never come up again.
grep -q "deploy --stack-name t-ingress .*Ec2SlotLaunchTemplate=lt-NEW" "$LOG" || fail "30-ingress got a stale launch template"
hasnt "Ec2SlotLaunchTemplate=lt-OLD"
grep -q "deploy --stack-name t-ingress .*ImageTag=9.9.9-dev-test" "$LOG" || fail "30-ingress did not get the deployed tag"
grep -q "deploy --stack-name t-ingress .*CAPABILITY_NAMED_IAM" "$LOG" || fail "30-ingress needs CAPABILITY_NAMED_IAM (its home-ops policy)"
iam_caps
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
# The image role (ADR 0071 P1, ComfyUI since ADR 0083) is the same shape, with two differences
# that are easy to get wrong: its image comes from a different upstream, and it has NO
# generated key at all (ComfyUI has no authentication option — the security group is the whole
# of it).
order "crane copy ghcr.io/k-k1/agent-fleet/comfyui:v0.37.0" "cloudformation deploy --stack-name af-ecs-engines"
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
# A deployment that runs only an LLM must not pay for an image it will never start.
# The condition is the same one that decides whether the service exists at all — without
# ImageEnabled 60-engines creates no image service, so there is nothing to pull.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
has "crane copy ghcr.io/ggml-org/llama.cpp:server-cuda"
# Every llm copy reads the digest back from ECR afterwards, so what actually landed is in the
# stand-up's own output even when nobody pinned one on purpose (the tag is a moving target
# otherwise — docs/log/99 §12.3).
has "crane digest 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-llamacpp:server-cuda"

echo "== case 3g-2: --llm-digest pins the crane copy source, not the destination tag =="
# LlmImageTag (and so 60-engines' `!Ref LlmImageTag`) must not move — only WHICH content that
# ECR tag holds. Passing the check and then standing up on the floating tag anyway is exactly
# the failure mode this flag exists to close.
: > "$LOG"
PIN="sha256:1111111111111111111111111111111111111111111111111111111111111111"
PIN_HEX="${PIN#sha256:}"
# The fake crane digest echoes back what THIS copy actually put in ECR, matching the requested
# pin — this run's copy really did fetch it, so the read-back must agree.
STUB_CRANE_DIGEST="$PIN_HEX" "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --llm-digest "$PIN" \
  > "$WORK/out3g2" 2>&1 </dev/null || { cat "$WORK/out3g2"; fail "standup with --llm-digest failed"; }
has "crane copy ghcr.io/ggml-org/llama.cpp@$PIN 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-llamacpp:server-cuda"
hasnt "crane copy ghcr.io/ggml-org/llama.cpp:server-cuda"
has "crane digest 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-llamacpp:server-cuda"
# A malformed digest is refused before anything is built, not handed to crane to fail on.
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --llm-digest not-a-digest \
  >/dev/null 2>"$WORK/out3g2.err" </dev/null && fail "a malformed --llm-digest was accepted"
grep -q -- "--llm-digest" "$WORK/out3g2.err" || fail "the malformed --llm-digest was not explained"

echo "== case 3g-3: a repeat stand-up (image already in ECR) still records the digest =="
# The common case once af-llamacpp is no longer empty: the crane copy above never runs, so the
# digest read-back has to happen on THIS branch too, not just after a fresh copy.
: > "$LOG"
STUB_LLM_IN_ECR=1 STUB_CRANE_DIGEST="$PIN_HEX" \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes \
  > "$WORK/out3g3" 2>&1 </dev/null || { cat "$WORK/out3g3"; fail "standup failed with the image already in ECR"; }
hasnt "crane copy ghcr.io/ggml-org/llama.cpp"
has "crane digest 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-llamacpp:server-cuda"
has "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3g-4: --llm-digest matching what is already in ECR passes silently =="
: > "$LOG"
STUB_LLM_IN_ECR=1 STUB_CRANE_DIGEST="$PIN_HEX" \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --llm-digest "$PIN" \
  > "$WORK/out3g4" 2>&1 </dev/null || { cat "$WORK/out3g4"; fail "standup refused a --llm-digest that matched what was already in ECR"; }
hasnt "crane copy ghcr.io/ggml-org/llama.cpp"
has "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3g-5: 🔴 --llm-digest that does not match an image already in ECR must fail loudly, not silently no-op =="
# This is the case the flag exists for, and the one a check placed only in the crane-copy branch
# cannot see: a repeat stand-up whose af-llamacpp:server-cuda was copied before this pin was ever
# chosen. The copy above never runs (STUB_LLM_IN_ECR=1), so if verification only lived inside
# that branch, --llm-digest would silently do nothing here and 60-engines would deploy believing
# an image is pinned when it is not — worse than not pinning at all.
: > "$LOG"
OTHER_DIGEST="sha256:2222222222222222222222222222222222222222222222222222222222222222"
STUB_LLM_IN_ECR=1 STUB_CRANE_DIGEST="${OTHER_DIGEST#sha256:}" \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --llm-digest "$PIN" \
  >"$WORK/out3g5.out" 2>"$WORK/out3g5.err" </dev/null \
  && fail "a --llm-digest mismatch against what is already in ECR was silently accepted"
grep -q -- "--llm-digest" "$WORK/out3g5.err" || fail "the digest mismatch was not explained"
hasnt "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3g-6: a failed digest read-back is explained differently from a real mismatch =="
# "Could not read the digest" (ECR auth lapsed, a transient read failure) is not evidence the pin
# is wrong, and must not tell an operator to delete/retag ECR over a read that never happened —
# that is the wrong fix for this failure and could throw away a perfectly good pin.
: > "$LOG"
STUB_LLM_IN_ECR=1 STUB_CRANE_DIGEST_FAILS=1 \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --llm-digest "$PIN" \
  >"$WORK/out3g6.out" 2>"$WORK/out3g6.err" </dev/null \
  && fail "a failed digest read-back with --llm-digest set was silently accepted"
hasnt "cloudformation deploy --stack-name af-ecs-engines"
grep -q "could not be read back" "$WORK/out3g6.err" || fail "an unreadable digest was not explained as unreadable"
grep -q -- "most likely copied before this pin was chosen" "$WORK/out3g6.err" \
  && fail "an unreadable digest was explained with the real-mismatch wording (misdirects to delete/retag ECR)"
diff <(sort -u "$WORK/out3g5.err") <(sort -u "$WORK/out3g6.err") >/dev/null \
  && fail "an unreadable digest produced the exact same message as a real mismatch"

echo "== case 3g-7: --comfy-digest pins the ComfyUI copy source, not the destination tag =="
# ImageComfyImageTag looks like a version, but re-dispatching comfyui-image.yml with the same tag
# overwrites it in GHCR, so the tag alone does not say which bytes a stand-up copies.
COMFY_DST="123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-comfyui:v0.37.0"
printf 'ServiceConnectNamespace=af.internal\nImageEnabled=true\n' > "$STATE4/params/60-engines"
: > "$LOG"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null \
  || fail "standup with only the image role failed"
has "crane copy ghcr.io/k-k1/agent-fleet/comfyui:v0.37.0 $COMFY_DST"
# Unpinned, the read-back still runs, so the output says what was copied.
has "crane digest $COMFY_DST"
: > "$LOG"
STUB_COMFY_DIGEST="$PIN_HEX" "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --comfy-digest "$PIN" \
  > "$WORK/out3g7" 2>&1 </dev/null || { cat "$WORK/out3g7"; fail "standup with --comfy-digest failed"; }
has "crane copy ghcr.io/k-k1/agent-fleet/comfyui@$PIN $COMFY_DST"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui:"
has "crane digest $COMFY_DST"
has "cloudformation deploy --stack-name af-ecs-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --comfy-digest not-a-digest \
  >/dev/null 2>"$WORK/out3g7.err" </dev/null && fail "a malformed --comfy-digest was accepted"
grep -q -- "--comfy-digest" "$WORK/out3g7.err" || fail "the malformed --comfy-digest was not explained"

echo "== case 3g-8: 🔴 --comfy-digest is checked against an image already in ECR =="
# A repeat stand-up never reaches the copy, so the pin has to be verified on that branch too.
: > "$LOG"
STUB_COMFY_IN_ECR=1 STUB_COMFY_DIGEST="$PIN_HEX" \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --comfy-digest "$PIN" \
  > "$WORK/out3g8" 2>&1 </dev/null || { cat "$WORK/out3g8"; fail "standup refused a --comfy-digest that matched what was already in ECR"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
has "cloudformation deploy --stack-name af-ecs-engines"
: > "$LOG"
STUB_COMFY_IN_ECR=1 STUB_COMFY_DIGEST="${OTHER_DIGEST#sha256:}" \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --comfy-digest "$PIN" \
  >"$WORK/out3g8.out" 2>"$WORK/out3g8.err" </dev/null \
  && fail "a --comfy-digest mismatch against what is already in ECR was silently accepted"
grep -q -- "--comfy-digest" "$WORK/out3g8.err" || fail "the ComfyUI digest mismatch was not explained"
grep -q "af-comfyui:v0.37.0 in ECR" "$WORK/out3g8.err" || fail "the ComfyUI digest mismatch did not name af-comfyui"
hasnt "cloudformation deploy --stack-name af-ecs-engines"

echo "== case 3g-9: an unreadable ComfyUI digest is explained as unreadable =="
: > "$LOG"
STUB_COMFY_IN_ECR=1 STUB_COMFY_DIGEST_FAILS=1 \
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes --comfy-digest "$PIN" \
  >"$WORK/out3g9.out" 2>"$WORK/out3g9.err" </dev/null \
  && fail "a failed ComfyUI digest read-back with --comfy-digest set was silently accepted"
hasnt "cloudformation deploy --stack-name af-ecs-engines"
grep -q "could not be read back" "$WORK/out3g9.err" || fail "an unreadable ComfyUI digest was not explained as unreadable"
grep -q -- "most likely copied before this pin was chosen" "$WORK/out3g9.err" \
  && fail "an unreadable ComfyUI digest was explained with the real-mismatch wording"
# Without a pin, an unreadable digest must not fail a copy that already succeeded.
: > "$LOG"
STUB_COMFY_DIGEST_FAILS=1 "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes \
  > "$WORK/out3g9b" 2>&1 </dev/null || { cat "$WORK/out3g9b"; fail "an unpinned stand-up failed on an unreadable ComfyUI digest"; }
has "cloudformation deploy --stack-name af-ecs-engines"

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
# ImageImageTag was retired separately, by ADR 0083 — a capture from before that still names
# it, and `deploy` refuses it the same way as the six above.
if grep -qE "deploy --stack-name af-ecs-engines .*ImageImageTag=" "$LOG"; then
  fail "ImageImageTag, retired in ADR 0083, was passed to deploy (the CLI refuses it)"
fi
grep -q "deploy --stack-name af-ecs-engines .*LlmEnabled=true" "$LOG" \
  || fail "the llm role implied by LlmModelS3Key was not carried over (the update would delete it)"
grep -q "deploy --stack-name af-ecs-engines .*ImageEnabled=true" "$LOG" \
  || fail "the image role implied by ImageModelS3Key was not carried over"
# And the image is read the same way, so the role is never created against an empty ECR
# repository — the stabilisation trap the ordering above exists for.
has "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# 🔴 `ImageEngine` is gone from the template (ADR 0083 retired the engine it chose; the
# parameter outlived its last `!If` by a release), so a captured value of ANY spelling has to be
# dropped rather than only the retired one: `deploy` refuses a key the template does not
# declare, which makes `comfy` as fatal as `sdcpp`. Both are asked, so a conditional drop that
# only knew about `sdcpp` fails this.
for stale_engine in sdcpp comfy; do
  : > "$LOG"
  printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageEngine=%s\n' \
    "$stale_engine" > "$STATE4/params/60-engines"
  "$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
  if grep -qE "deploy --stack-name af-ecs-engines .*ImageEngine=" "$LOG"; then
    fail "a captured ImageEngine=$stale_engine reached deploy (the template no longer declares it)"
  fi
done
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# How the image role's box is bought is an OFFER now (ADR 0075), and the offers are a captured
# parameter like any other: a path that quietly dropped ImageOffers would put the deployment
# back on plain on-demand while the capture says otherwise, and nothing would say so.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageOffers=spot3|Spot|22000|g6.xlarge|4-8|15000-65536|1.57|spot\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
grep -q "deploy --stack-name af-ecs-engines .*ImageOffers=" "$LOG" \
  || fail "ImageOffers did not reach the deploy (the box would never be tried on Spot)"

# 🔴 And the parameter it REPLACES must not: ImageCapacityOptionType is gone from the template,
# and `cloudformation deploy` refuses a key it is given and does not know, so a capture holding
# the old line would fail the stand-up outright rather than deploy without it.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nImageCapacityOptionType=SPOT\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
if grep -q "deploy --stack-name af-ecs-engines .*ImageCapacityOptionType=" "$LOG"; then
  fail "a parameter retired in ADR 0075 was passed to deploy (the CLI refuses it)"
fi
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# 🔴 Same again for the Managed Instances requirement block, retired in ADR 0077 (the offer's
# own type set is the requirement now). A deployment that has been running since ADR 0071 has
# every one of these in its capture, and one of them reaching `deploy` stops the stand-up.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nLlmAllowedInstanceTypes=g6.xlarge,g5.xlarge\nLlmAcceleratorMemMinMiB=21000\nLlmVCpuMin=4\nLlmVCpuMax=8\nLlmMemMinMiB=15000\nLlmMemMaxMiB=65536\nLlmUseLocalStorage=true\nLlmScaleInAfter=-2\nImageAllowedInstanceTypes=g6.xlarge\nImageUseLocalStorage=true\nLlmStorageGiB=120\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
if grep -qE "deploy --stack-name af-ecs-engines .*(AllowedInstanceTypes|AcceleratorMemMinMiB|VCpuMin|VCpuMax|MemMinMiB|MemMaxMiB|UseLocalStorage|ScaleInAfter)=" "$LOG"; then
  fail "a parameter retired in ADR 0077 was passed to deploy (the CLI refuses it)"
fi
# ...and the one that survived the cull must still get through: <Role>StorageGiB stayed and
# changed meaning (the box's root volume), so dropping it with its neighbours would silently
# put every deployment back on the AMI's 30 GiB default.
grep -q "deploy --stack-name af-ecs-engines .*LlmStorageGiB=120" "$LOG" \
  || fail "LlmStorageGiB was dropped with the ADR 0077 parameters (it sizes the root volume now)"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# 🔴 And the parameter ADR 0077 RE-MEANT is dropped only when it still holds the OLD default.
# `<Role>OfferBudgetSec` stopped bounding "how long to wait for this offer's box" and started
# bounding "how long the box that was bought may take to register"; a captured 180 is the old
# meaning's default and would silently halve the new ceiling, while a value somebody chose must
# survive a stand-up like any other.
: > "$LOG"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\nLlmOfferBudgetSec=180\nImageOfferBudgetSec=600\n' > "$STATE4/params/60-engines"
"$ECS/standup.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --yes > /dev/null </dev/null
if grep -q "deploy --stack-name af-ecs-engines .*LlmOfferBudgetSec=" "$LOG"; then
  fail "a captured LlmOfferBudgetSec=180 was passed on (it means something else since ADR 0077)"
fi
grep -q "deploy --stack-name af-ecs-engines .*ImageOfferBudgetSec=600" "$LOG" \
  || fail "a chosen ImageOfferBudgetSec was dropped (only the old DEFAULT may be dropped)"
printf 'ServiceConnectNamespace=af.internal\nLlmEnabled=true\nImageEnabled=true\n' > "$STATE4/params/60-engines"

# 🔴 The box is the Control Plane's to buy now (ADR 0077): one launch template per role, the
# role's name written into the ECS agent's attributes, and the service placed by that attribute.
# Each of the three is load-bearing on its own - a template without the attribute gives a box
# nothing will be placed on, and a service without the constraint places the engine on a slot.
for role in llm image; do
  grep -q "LaunchTemplateName: !Sub \"af-\${AWS::StackName}-engine-$role\"" "$ECS/cfn/60-engines.yaml" \
    || fail "the $role role has no launch template (the CP has nothing to buy a box from)"
  grep -q "ECS_INSTANCE_ATTRIBUTES={\"af-role\":\"engine-$role\"}" "$ECS/cfn/60-engines.yaml" \
    || fail "the $role launch template does not write af-role into /etc/ecs/ecs.config"
  grep -q "attribute:af-role == engine-$role" "$ECS/cfn/60-engines.yaml" \
    || fail "the $role service lost its placement constraint (it could land on a slot)"
done
grep -q "ImageId: resolve:ssm:/aws/service/ecs/optimized-ami/amazon-linux-2023/gpu/recommended/image_id" \
  "$ECS/cfn/60-engines.yaml" \
  || fail "the engine AMI is no longer EC2's resolve:ssm: on the public GPU parameter (ADR 0077 decision 7)"
# 🔴 ...and the caller of CreateFleet has to be able to READ that parameter. Measured in ADR 0077's
# P0 run: without ssm:GetParameters the call fails top-level with SsmAccessDenied, naming neither
# the action nor the parameter - i.e. an engine that never starts and a message nobody can act on.
grep -q "parameter/aws/service/ecs/optimized-ami/\*" "$ECS/cfn/60-engines.yaml" \
  || fail "CpIngestPolicy cannot read the AMI parameter (CreateFleet fails with SsmAccessDenied)"
# 🔴 The instance store carries the models (Docker's data-root moves, the anonymous volume
# follows), and losing this line is a start that is merely SLOW - about 2x, measured under
# Managed Instances. Nothing else would report it.
[ "$(grep -c "awk '/Instance Storage/" "$ECS/cfn/60-engines.yaml")" = 2 ] \
  || fail "a launch template stopped putting Docker's data-root on the instance store (ADR 0077 open question 8)"
if grep -q "AWS::ECS::CapacityProvider" "$ECS/cfn/60-engines.yaml"; then
  fail "60-engines still creates a capacity provider (ADR 0077 removed all three)"
fi
if grep -q "MANAGED_INSTANCES" "$ECS/cfn/60-engines.yaml"; then
  fail "a task definition still requires MANAGED_INSTANCES (it cannot be placed on an EC2 box)"
fi

echo "== case 3g-efs: standup hands the EFS backup state of the data stack to 30-ingress =="
# Only a confirmed verdict is written. An unreadable stack must not be turned into "missing"
# (a false banner), and must not overwrite an earlier value either: nothing is passed.
efs_standup() { # <profile> [VAR=val ...] -> $WORK/out3e
  local prof="$1"; shift
  : > "$LOG"
  env "$@" "$ECS/standup.sh" --profile "$prof" --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3e" 2>&1 </dev/null \
    || { cat "$WORK/out3e"; fail "standup for $prof failed"; }
}
efs_standup p2 STUB_VAULT=ok
grep -q "deploy --stack-name t-ingress .*EfsBackup=present" "$LOG" || fail "retain + vault + plan did not give EfsBackup=present"
efs_standup p2
grep -q "deploy --stack-name t-ingress .*EfsBackup=missing" "$LOG" || fail "retain without a vault did not give EfsBackup=missing"
efs_standup p2 STUB_VAULT=noplan
grep -q "deploy --stack-name t-ingress .*EfsBackup=missing" "$LOG" || fail "a plan selecting another file system did not give EfsBackup=missing"
# ...including a value the capture itself holds: it is a snapshot, and the live stack may have moved on.
for stale in present missing; do
  echo "EfsBackup=$stale" >> "$STATE2/params/30-ingress"
  efs_standup p2 STUB_VAULT=fail
  if grep -q "deploy --stack-name t-ingress .*EfsBackup=" "$LOG"; then fail "an unreadable data stack still passed the captured EfsBackup=$stale"; fi
  sed -i '/^EfsBackup=/d' "$STATE2/params/30-ingress"
done
efs_standup p
grep -q "deploy --stack-name t-ingress .*EfsBackup=off" "$LOG" || fail "Persistence=delete did not give EfsBackup=off"

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
# The slot pool rides every release too: its user data carries ECS_AWSVPC_BLOCK_IMDS, and a
# pool left on an old template launches every future slot without it.
grep -q "deploy --stack-name t-pool .*40-ec2-pool.yaml.*CAPABILITY_NAMED_IAM" "$LOG" \
  || fail "update.sh did not redeploy the slot pool (new slots would miss the IMDS block)"
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
# call the comment above it says never to add a parameter to. The one exception is EfsBackup,
# the data stack's backup state for the Console's warning (the stub's data stack is
# Persistence=delete, so "off").
grep -q "deploy --stack-name t-ingress .*--parameter-overrides ImageTag=9.9.9-dev-test EfsBackup=off --no-fail" "$LOG" \
  || fail "the ingress deploy overrides something besides ImageTag and EfsBackup"
grep -q "deploy --stack-name t-ingress .*CAPABILITY_NAMED_IAM" "$LOG" \
  || fail "update.sh deploys 30-ingress without CAPABILITY_NAMED_IAM (InsufficientCapabilitiesException)"
iam_caps

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

echo "== case 3h-ws: update.sh and standup.sh hand 00-network's workspace subnets to 30-ingress (#2716) =="
# A fact of the network stack, passed on every run; a network stack without the output passes
# nothing (the case above pins that byte for byte) and says so.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_WS_SUBNETS=subnet-0a,subnet-0b \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3hw" 2>&1 \
  || { cat "$WORK/out3hw"; fail "update.sh failed with workspace subnets"; }
grep -q "describe-stacks --stack-name t-network .*OutputKey=='WorkspaceSubnets'" "$LOG" \
  || fail "update.sh did not read the workspace subnets from the ingress stack's NetworkStackName"
grep -q "deploy --stack-name t-ingress .*--parameter-overrides ImageTag=9.9.9-dev-test EfsBackup=off WorkspaceSubnets=subnet-0a,subnet-0b --no-fail" "$LOG" \
  || fail "update.sh did not pass WorkspaceSubnets to 30-ingress"
grep -q "has no WorkspaceSubnets output" "$WORK/out3h" \
  || fail "update.sh against a network stack without workspace subnets did not say so"
efs_standup p STUB_WS_SUBNETS=subnet-0a,subnet-0b
grep -q "deploy --stack-name t-ingress .*WorkspaceSubnets=subnet-0a,subnet-0b" "$LOG" \
  || fail "standup.sh did not pass the network stack's WorkspaceSubnets to 30-ingress"
# A live network stack without S3EndpointEnabled may still run workspaces in its private subnets:
# standup must not switch the endpoint on for it; one that has the switch keeps its value.
grep -q "deploy --stack-name t-network .*S3EndpointEnabled=false" "$LOG" \
  || fail "standup.sh switched the S3 endpoint on for a live network stack older than the switch"
efs_standup p STUB_S3EP=true
if grep -q "S3EndpointEnabled=" "$LOG"; then fail "standup.sh overrode a live S3EndpointEnabled"; fi
# The capture's value is a snapshot of old subnet ids; it must never reach the deploy.
echo "WorkspaceSubnets=subnet-stale" >> "$STATE/params/30-ingress"
efs_standup p
if grep -q "WorkspaceSubnets=subnet-stale" "$LOG"; then fail "standup.sh passed the captured WorkspaceSubnets"; fi
sed -i '/^WorkspaceSubnets=/d' "$STATE/params/30-ingress"

echo "== case 3h-egress: the egress gateway's address and token secret reach 30-ingress (#2717) =="
SECRET_ARN="arn:aws:secretsmanager:ap-northeast-1:123456789012:secret:af-t-network-egress-token-eni-1-AbCdEf"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_EGRESS_ADDR=10.20.0.50:3128 STUB_EGRESS_SECRET="$SECRET_ARN" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3he" 2>&1 \
  || { cat "$WORK/out3he"; fail "update.sh failed with an egress gateway"; }
grep -q "deploy --stack-name t-ingress .*EgressProxyAddr=10.20.0.50:3128 EgressTokenSecretArn=$SECRET_ARN" "$LOG" \
  || fail "update.sh did not pass the egress gateway's address and secret to 30-ingress"
# Back on nat-gateway: the network has no outputs, the ingress stack still has the secret -> cleared.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ING_EGRESS="$SECRET_ARN" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3he2" 2>&1 \
  || { cat "$WORK/out3he2"; fail "update.sh failed clearing the egress gateway"; }
grep -q "deploy --stack-name t-ingress .*EgressProxyAddr= EgressTokenSecretArn= " "$LOG" \
  || fail "update.sh did not clear the egress parameters after a switch back to nat-gateway"
efs_standup p STUB_EGRESS_ADDR=10.20.0.50:3128 STUB_EGRESS_SECRET="$SECRET_ARN"
grep -q "deploy --stack-name t-ingress .*EgressProxyAddr=10.20.0.50:3128.*EgressTokenSecretArn=$SECRET_ARN" "$LOG" \
  || fail "standup.sh did not pass the egress gateway's outputs to 30-ingress"
grep -q "runs the egress gateway" "$WORK/out3e" || fail "standup.sh did not warn about the egress gateway"
grep -q "pause.sh refuses" "$WORK/out3e" && fail "standup.sh still says pause.sh refuses the gateway (#2729)"
# It waits for the gateway's first policy once the CP is up, and says when it has none.
order "cloudformation deploy --stack-name t-ingress" "ssm send-command --instance-ids i-gw"
grep -q "first policy applied" "$WORK/out3e" || fail "standup.sh did not report the egress gateway ready"
efs_standup p STUB_EGRESS_ADDR=10.20.0.50:3128 STUB_EGRESS_SECRET="$SECRET_ARN" STUB_GW_PROBE=af-gw-up AF_GATEWAY_WAIT_SEC=0
grep -q "the egress gateway is not ready" "$WORK/out3e" || fail "standup.sh did not warn about a gateway without a policy"
echo "EgressTokenSecretArn=arn:stale" >> "$STATE/params/30-ingress"
efs_standup p
if grep -q "EgressTokenSecretArn=arn:stale" "$LOG"; then fail "standup.sh passed the captured EgressTokenSecretArn"; fi
sed -i '/^EgressTokenSecretArn=/d' "$STATE/params/30-ingress"

echo "== case 3h-pool: update.sh never skips the slot pool on ecs-ec2 without saying so =="
#
# The pool's user data carries ECS_AWSVPC_BLOCK_IMDS. A lookup that fails (no
# cloudformation:ListExports) or finds nothing must stop the update, not read as "no pool".
for mode in fail none; do
  : > "$LOG"
  if VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_POOL_EXPORT=$mode \
    "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3hp" 2>&1; then
    cat "$WORK/out3hp"; fail "update.sh succeeded on ecs-ec2 with the pool lookup '$mode'"
  fi
  grep -q "slot pool stack (40-ec2-pool)" "$WORK/out3hp" || { cat "$WORK/out3hp"; fail "pool lookup '$mode': no explanation"; }
  hasnt "deploy --stack-name t-ingress"
done
# The explicit override, checked against the launch template it must own.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_POOL_EXPORT=fail STUB_POOL_LT_OUTPUT=lt-OLD \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --pool-stack t-pool > "$WORK/out3hp" 2>&1 \
  || { cat "$WORK/out3hp"; fail "update.sh --pool-stack failed"; }
grep -q "deploy --stack-name t-pool .*40-ec2-pool.yaml" "$LOG" || fail "--pool-stack did not deploy the named pool"
: > "$LOG"
if VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_POOL_EXPORT=fail STUB_POOL_LT_OUTPUT=lt-OTHER \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --pool-stack t-pool > "$WORK/out3hp" 2>&1; then
  fail "update.sh accepted a --pool-stack that owns another launch template"
fi
grep -q "owns launch template 'lt-OTHER'" "$WORK/out3hp" || { cat "$WORK/out3hp"; fail "wrong --pool-stack: no explanation"; }
# Fargate has no pool: the same failing lookup is never made and the update goes through.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_POOL_EXPORT=fail STUB_WS_RUNTIME=ecs \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3hp" 2>&1 \
  || { cat "$WORK/out3hp"; fail "update.sh failed on a Fargate deployment without a pool"; }
hasnt "40-ec2-pool.yaml"
grep -q "deploy --stack-name t-ingress" "$LOG" || fail "the Fargate update did not deploy 30-ingress"
iam_caps

echo "== case 3h-2: update.sh repairs an OfferBudgetSec left on the OLD meaning's default =="
#
# ADR 0077 re-meant the parameter instead of retiring it: 180 used to be "how long this offer may
# wait for a box" and now means "how long the box that WAS bought may take to register with ECS".
# `cloudformation deploy` keeps an unnamed parameter at its previous value for ever, and
# standup.sh's drop only reaches a deployment that is REBUILT from its capture — so without this
# every deployment that updates in place keeps a registration ceiling shorter than the one the
# ADR chose, and the symptom is an engine that "does not start".
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_OLD_BUDGET=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h2" 2>&1 \
  || { cat "$WORK/out3h2"; fail "update.sh failed against an engine stack on the old budget"; }
grep -q "deploy --stack-name af-ecs-engines .*LlmOfferBudgetSec=300" "$LOG" \
  || fail "update.sh left LlmOfferBudgetSec at the old meaning's 180"
grep -q "deploy --stack-name af-ecs-engines .*ImageOfferBudgetSec=300" "$LOG" \
  || fail "update.sh left ImageOfferBudgetSec at the old meaning's 180"
# And a deployment that is already on the new default is not given the parameter at all — an
# override is what stops `deploy` from keeping a value somebody chose later.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h3" 2>&1 \
  || { cat "$WORK/out3h3"; fail "update.sh failed against a migrated engine stack"; }
if grep -q "deploy --stack-name af-ecs-engines .*OfferBudgetSec=" "$LOG"; then
  fail "update.sh overrode an OfferBudgetSec that was not the old default"
fi

echo "== case 3h-3: update.sh refuses to update 20-platform while 60-engines still imports EcrSdcppUri (ADR 0083) =="
#
# ADR 0083 decision 9's order is 60-engines FIRST (drops the import), 20-platform SECOND (drops
# the export) — the opposite of update.sh's usual 20-platform-first order (case 3i below, a
# DIFFERENT reason: a NEW repository must exist before 60-engines references it). A deployment
# whose 60-engines is still on the pre-ADR-0083 template is still importing EcrSdcppUri, so
# running 20-platform first would try to delete an export CloudFormation refuses to let go
# while it is in use. The change set itself looks clean (a deletion, not a Replacement), so
# nothing catches it until `execute-change-set` runs and the stack update rolls back — an
# opaque CFN error rather than a named fix. The guard has to stop BEFORE that, with nothing
# touched.
: > "$LOG"
if VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMPORTS_SDCPP=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h3a" 2>&1; then
  cat "$WORK/out3h3a"; fail "update.sh must refuse when 60-engines still imports EcrSdcppUri"
fi
grep -q "Update 60-engines to the current template FIRST" "$WORK/out3h3a" \
  || { cat "$WORK/out3h3a"; fail "the refusal did not name the fix (an operator would read a raw CFN rollback instead)"; }
hasnt "cloudformation deploy --stack-name t-platform"   # nothing was touched, not even a plan
hasnt "cloudformation execute-change-set"

# The ordinary case (no live importer, the state every deployment is in once its own 60-engines
# has been updated once) must be unaffected — the guard is a preflight, not a new requirement.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3h3b" 2>&1 \
  || { cat "$WORK/out3h3b"; fail "update.sh failed with no live EcrSdcppUri importer"; }
has "cloudformation deploy --stack-name t-platform"

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
ET_GHCR="crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-16"
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

echo "== case 3i-pool: the slot pool goes in BEFORE 20-platform, and its failure stops the update =="
#
# 40-ec2-pool carries the CP's iam:PassRole for the slot role; 20-platform's template no longer
# grants any. 20-platform first would leave every slot grow on AccessDenied whenever the
# update stops between the two (a missing image, a refused change set, a pool rollback).
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3ip" 2>&1 \
  || { cat "$WORK/out3ip"; fail "update.sh failed on the ordinary ecs-ec2 path"; }
order "cloudformation deploy --stack-name t-pool" "cloudformation deploy --stack-name t-platform"
order "cloudformation deploy --stack-name t-pool" "cloudformation execute-change-set"
# The pool's deploy failing: 20-platform is neither change-setted nor executed, and nothing
# after it runs, so the live 20-platform keeps whatever PassRole it had.
: > "$LOG"
rc=0
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_POOL_DEPLOY_FAIL=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3ip2" 2>&1 || rc=$?
[ "$rc" != 0 ] || { cat "$WORK/out3ip2"; fail "update.sh carried on after the slot pool's deploy failed"; }
has "cloudformation deploy --stack-name t-pool"
hasnt "cloudformation deploy --stack-name t-platform"
hasnt "cloudformation execute-change-set"
hasnt "cloudformation deploy --stack-name t-ingress"
# A missing release image stops the update after the pool and 20-platform: the pool's grant is
# already in place by then (the order above), so the slot grow survives that stop too.
: > "$LOG"
rc=0
VERSION=9.9.9-dev-test STUB_ECR_HAS=0 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3ip3" 2>&1 || rc=$?
[ "$rc" != 0 ] || { cat "$WORK/out3ip3"; fail "update.sh carried on with the release image missing"; }
grep -q "not in ECR" "$WORK/out3ip3" || { cat "$WORK/out3ip3"; fail "the missing image was not the stop"; }
has "cloudformation deploy --stack-name t-pool"
hasnt "cloudformation deploy --stack-name t-ingress"

echo "== case 3i-2: a tag that is already in ECR is not copied again =="
# The control for the case above. A step that copies on every run passes 3i and is still wrong:
# it pulls an image that is already there on every release, and teaches the reader that the
# copy means nothing.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ET_IN_ECR=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i2" 2>&1 \
  || { cat "$WORK/out3i2"; fail "update.sh failed with the engine tools image already in ECR"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools"
grep -q "af-engine-tools:2026-09-16 is already in ECR" "$WORK/out3i2" \
  || fail "it did not say why it copied nothing"
has "cloudformation deploy --stack-name af-ecs-engines"
# A live tag that is somebody's choice is left alone — the control for 3i-7's repair.
hasnt "EngineToolsImageTag="

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

echo "== case 3i-8: the stale first Default is repaired instead of carried for ever =="
# 🔴 `2026-09-11` was this parameter's first Default and names an image speaking contract 1,
# while the template has asked for a newer number since 0.21.0 — every ingest on such a stack
# exits 78 before fetching a byte. A live stack records the resolved default, so `deploy` keeps
# it for ever unless the value is NAMED. Both halves are the assertion: the copy carries the new
# tag over, and the stack is told to use it. Copy the new tag without naming it and the stack
# goes on pointing at the broken one; name it without copying and the task cannot pull.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 STUB_ET_WANT=2026-09-11 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i8" 2>&1 \
  || { cat "$WORK/out3i8"; fail "update.sh failed against a stack on the stale default"; }
has "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:$ET_DEFAULT"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11"
has "EngineToolsImageTag=$ET_DEFAULT"
order "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:$ET_DEFAULT" \
  "cloudformation deploy --stack-name af-ecs-engines"
grep -q "exiting 78" "$WORK/out3i8" || fail "the repair did not say what it was repairing"

echo "== case 3i-9: a stack still on an earlier ComfyUI Default is moved to the current one =="
# A live stack records the resolved Default, so `deploy` keeps the ComfyUI tag of whichever
# release created it; 0.23.0 had to ship a hand-run command for exactly that. update.sh moves a
# stack on an earlier Default and leaves any other value alone. Copy and name are one repair:
# name the tag without copying it and the image role cannot pull.
COMFY_DEFAULT="$(sed -n '/^  ImageComfyImageTag:$/,/^  [A-Za-z]/p' "$ECS/cfn/60-engines.yaml" \
  | sed -n 's/^ *Default: *//p' | head -1 | tr -d '"')"
[ -n "$COMFY_DEFAULT" ] || fail "cfn/60-engines.yaml declares no Default for ImageComfyImageTag"
COMFY_STALE="$(sed -n 's/^COMFY_STALE_DEFAULTS="\(.*\)"$/\1/p' "$ECS/update.sh")"
[ -n "$COMFY_STALE" ] || fail "update.sh lists no earlier ImageComfyImageTag defaults"
# The rule that keeps the list honest: whoever moves the Default appends the old one, and never
# lists the current one (that would name the Default on every release and override nothing).
case " $COMFY_STALE " in
  *" $COMFY_DEFAULT "*) fail "COMFY_STALE_DEFAULTS lists the current Default $COMFY_DEFAULT" ;;
esac
COMFY_OLD="${COMFY_STALE##* }"
COMFY_GHCR="crane copy ghcr.io/k-k1/agent-fleet/comfyui:$COMFY_DEFAULT"

echo "   3i-9a: image role on — copied, then named"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9a" 2>&1 \
  || { cat "$WORK/out3i9a"; fail "update.sh failed against a stack on an earlier ComfyUI Default"; }
has "$COMFY_GHCR"
has "ImageComfyImageTag=$COMFY_DEFAULT"
order "$COMFY_GHCR" "cloudformation deploy --stack-name af-ecs-engines"
grep -q "ImageComfyImageTag=$COMFY_DEFAULT ($COMFY_OLD was an earlier template default" "$WORK/out3i9a" \
  || fail "the repair did not say what it was repairing"
# Unpinned, the digest that landed is still read back and printed, so the output says what the
# stack is about to run.
COMFY_ECR="123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/af-comfyui:$COMFY_DEFAULT"
has "crane digest $COMFY_ECR"
grep -q "af-comfyui:$COMFY_DEFAULT digest: sha256:" "$WORK/out3i9a" || fail "the landed ComfyUI digest was not printed"

echo "   3i-9b: image role off — named, nothing copied"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 \
  STUB_COMFY_WANT="$COMFY_OLD" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9b" 2>&1 \
  || { cat "$WORK/out3i9b"; fail "update.sh failed against an LLM-only stack on an earlier ComfyUI Default"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
has "ImageComfyImageTag=$COMFY_DEFAULT"

echo "   3i-9c: a chosen tag, and the current one, are left alone"
for want in v0.36.0 "$COMFY_DEFAULT"; do
  : > "$LOG"
  VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
    STUB_COMFY_WANT="$want" \
    "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9c" 2>&1 \
    || { cat "$WORK/out3i9c"; fail "update.sh failed against a stack on ImageComfyImageTag=$want"; }
  hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
  hasnt "ImageComfyImageTag="
done

echo "   3i-9d: already in ECR — named, not copied again"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9d" 2>&1 \
  || { cat "$WORK/out3i9d"; fail "update.sh failed with the new ComfyUI image already in ECR"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
has "ImageComfyImageTag=$COMFY_DEFAULT"

echo "   3i-9e: the new image is nowhere — warned, tag kept, release goes on"
# The old tag still runs, so this is no reason to stop the release; naming the new tag with no
# image behind it would be (the image role could not pull).
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_COMFY_IN_GHCR=0 STUB_ENGINES_LIVE=1 \
  STUB_ENGINES_IMAGE_ON=1 STUB_COMFY_WANT="$COMFY_OLD" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9e" 2>&1 \
  || { cat "$WORK/out3i9e"; fail "update.sh stopped the release over a missing ComfyUI image"; }
grep -q "comfyui-image.yml" "$WORK/out3i9e" || fail "it did not say how to produce the image"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
hasnt "ImageComfyImageTag="
has "cloudformation deploy --stack-name af-ecs-engines"
has "cloudformation deploy --stack-name t-ingress"

echo "   3i-9f: --comfy-digest pins the copy source; the destination is still the tag"
# comfyui-image.yml re-dispatched with an existing tag overwrites it in GHCR, so the tag alone
# does not say which bytes this repair copies. Same contract as standup.sh --comfy-digest (3g-7).
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" STUB_COMFY_DIGEST="$PIN_HEX" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest "$PIN" \
  > "$WORK/out3i9f" 2>&1 || { cat "$WORK/out3i9f"; fail "update.sh with a matching --comfy-digest failed"; }
has "crane manifest ghcr.io/k-k1/agent-fleet/comfyui@$PIN"
has "crane copy ghcr.io/k-k1/agent-fleet/comfyui@$PIN $COMFY_ECR"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui:"
order "crane copy ghcr.io/k-k1/agent-fleet/comfyui@$PIN" "crane digest $COMFY_ECR"
order "crane digest $COMFY_ECR" "cloudformation deploy --stack-name af-ecs-engines"
has "ImageComfyImageTag=$COMFY_DEFAULT"

echo "   3i-9g: 🔴 a pin that does not match what is already in ECR — tag kept, release goes on"
# The repeat-run branch never reaches the copy, so the pin has to be checked there too. Naming the
# tag anyway would point the image role at bytes nobody vouched for; the old tag still runs.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=1 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" STUB_COMFY_DIGEST="${OTHER_DIGEST#sha256:}" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest "$PIN" \
  > "$WORK/out3i9g" 2>&1 || { cat "$WORK/out3i9g"; fail "update.sh stopped the release over a ComfyUI pin mismatch"; }
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
has "crane digest $COMFY_ECR"
hasnt "ImageComfyImageTag="
grep -q "most likely copied before this pin was chosen" "$WORK/out3i9g" || fail "the ComfyUI pin mismatch was not explained"
grep -q "keeps ImageComfyImageTag=$COMFY_OLD" "$WORK/out3i9g" || fail "it did not say the stack keeps its tag"
grep -q "comfyui@$PIN" "$WORK/out3i9g" || fail "the hand-run copy it suggests dropped the pin"
has "cloudformation deploy --stack-name af-ecs-engines"
has "cloudformation deploy --stack-name t-ingress"

echo "   3i-9h: a pinned read-back that fails is explained as unreadable, and the tag is kept"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" STUB_COMFY_DIGEST_FAILS=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest "$PIN" \
  > "$WORK/out3i9h" 2>&1 || { cat "$WORK/out3i9h"; fail "update.sh stopped the release over an unreadable ComfyUI digest"; }
hasnt "ImageComfyImageTag="
grep -q "could not be read back" "$WORK/out3i9h" || fail "an unreadable ComfyUI digest was not explained as unreadable"
grep -q "most likely copied before this pin was chosen" "$WORK/out3i9h" \
  && fail "an unreadable ComfyUI digest was explained with the real-mismatch wording"
# Unpinned, the same failed read must not undo a copy that succeeded.
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_OLD" STUB_COMFY_DIGEST_FAILS=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress > "$WORK/out3i9h2" 2>&1 \
  || { cat "$WORK/out3i9h2"; fail "an unpinned update failed on an unreadable ComfyUI digest"; }
has "ImageComfyImageTag=$COMFY_DEFAULT"

echo "   3i-9i: a pinned digest GHCR has not got points at the pin, not at a re-bake"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_COMFY_IN_GHCR=0 STUB_ENGINES_LIVE=1 \
  STUB_ENGINES_IMAGE_ON=1 STUB_COMFY_WANT="$COMFY_OLD" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest "$PIN" \
  > "$WORK/out3i9i" 2>&1 || { cat "$WORK/out3i9i"; fail "update.sh stopped the release over a pin absent from GHCR"; }
grep -q "check the --comfy-digest value" "$WORK/out3i9i" || fail "a pin absent from GHCR was not blamed on the pin"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
hasnt "ImageComfyImageTag="

echo "   3i-9j: a pin with nothing to pin is said to be ignored; a malformed one is refused"
: > "$LOG"
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_COMFY_IN_ECR=0 STUB_ENGINES_LIVE=1 STUB_ENGINES_IMAGE_ON=1 \
  STUB_COMFY_WANT="$COMFY_DEFAULT" \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest "$PIN" \
  > "$WORK/out3i9j" 2>&1 || { cat "$WORK/out3i9j"; fail "update.sh failed with an unused --comfy-digest"; }
grep -q -- "--comfy-digest is ignored" "$WORK/out3i9j" || fail "an unused --comfy-digest was dropped silently"
hasnt "crane copy ghcr.io/k-k1/agent-fleet/comfyui"
: > "$LOG"
rc=0
VERSION=9.9.9-dev-test STUB_ECR_HAS=1 STUB_ENGINES_LIVE=1 \
  "$ECS/update.sh" --profile p4 --region ap-northeast-1 --stack t-ingress --comfy-digest not-a-digest \
  >/dev/null 2>"$WORK/out3i9j.err" || rc=$?
[ "$rc" = 2 ] || fail "a malformed --comfy-digest was not refused with exit 2 (got $rc)"
grep -q -- "--comfy-digest wants" "$WORK/out3i9j.err" || fail "the malformed --comfy-digest was not explained"
hasnt "cloudformation"

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
grep -q "4. af-engine-tools:2026-09-16 into ECR, then af-ecs-engines" "$WORK/out3i7" \
  || fail "the plan did not show the image going in before the engines stack"
grep -q "DRY: crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-16" "$WORK/out3i7" \
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

echo "== case 3b-3: a secret switch that is on needs its SSM parameter before anything is created =="
# 30-ingress wires AF_COMFY_API_KEY only when ComfyApiKey=on, and a Secrets entry whose parameter
# does not exist stops the CP task from starting -- after 00-20 are already up.
cp "$STATE/params/30-ingress" "$WORK/params-30-saved"
echo "ComfyApiKey=on" >> "$STATE/params/30-ingress"
: > "$LOG"
if STUB_NO_COMFY_KEY=1 "$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes \
     > "$WORK/out3b3" 2>&1 </dev/null; then
  fail "standup went on although ComfyApiKey=on and the SSM parameter is missing"
fi
grep -q "no SSM /af-cp/comfy-api-key (ComfyApiKey=on)" "$WORK/out3b3" \
  || fail "the missing secret was not named: $(tail -3 "$WORK/out3b3")"
hasnt "cloudformation deploy --stack-name t-network"
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3b3b" 2>&1 </dev/null \
  || fail "ComfyApiKey=on with the SSM parameter present was refused: $(tail -3 "$WORK/out3b3b")"
grep -q "deploy --stack-name t-ingress .*ComfyApiKey=on" "$LOG" || fail "ComfyApiKey=on did not reach 30-ingress"
# GitHub: the dedicated sign-in App replaces github-client-secret, so standup must not demand it
# then -- but must when the switch is off (the template injects it for GithubAllowedOrgs).
printf 'GithubAllowedOrgs=example-org\n' >> "$STATE/params/30-ingress"
: > "$LOG"
if STUB_NO_GH_KEY=1 "$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes \
     > "$WORK/out3b4" 2>&1 </dev/null; then
  fail "standup went on although GithubAllowedOrgs is set and github-client-secret is missing"
fi
grep -q "no SSM /af-cp/github-client-secret" "$WORK/out3b4" || fail "the missing github-client-secret was not named"
printf 'GithubLoginSecret=on\n' >> "$STATE/params/30-ingress"
: > "$LOG"
STUB_NO_GH_KEY=1 "$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3b5" 2>&1 </dev/null \
  || fail "GithubLoginSecret=on was refused for the missing legacy github-client-secret: $(tail -3 "$WORK/out3b5")"
cp "$WORK/params-30-saved" "$STATE/params/30-ingress"

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
{ grep -q "^GatewayEipAllocationId=eipalloc-GW$" "$CAPFILE" && grep -q "^GatewayEipAddress=203.0.113.7$" "$CAPFILE"; } \
  || fail "the workspace egress address was not captured from the outputs (a rebuild would allocate a new one): $(grep GatewayEip "$CAPFILE" || echo '<no such line>')"

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
# No engine stack, no box, no database here: none of the new steps may fire.
hasnt "ec2 terminate-instances"
hasnt "rds stop-db-instance"

CP_DOWN="ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0"
CP_UP="ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 1"

echo "== case 4b: --fast ends the engine boxes and the helper services, and sweeps again after the CP =="
: > "$LOG"
STUB_ENGINES_LIVE=1 STUB_ENGINE_BOXES=1 STUB_ENGINE_SVC_UP=1 \
  "$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4b" </dev/null
order "ecs update-service --cluster t-cluster --service af-af-ecs-engines-image --desired-count 0" "$CP_DOWN"
order "ec2 terminate-instances --instance-ids i-gpu" "$CP_DOWN"
# The sweep: asked again once the CP is gone (a static stub still answers i-gpu, so the
# last terminate lands after the CP stop).
order_again "$CP_DOWN" "ec2 terminate-instances --instance-ids i-gpu"
# Engine boxes are terminated, never stopped: stop-instances belongs to the slots alone.
hasnt "ec2 stop-instances --instance-ids i-gpu"

echo "== case 4c: a box bought between the wait and the CP stop is ended by the sweep =="
: > "$LOG"
STUB_NO_SLOTS=1 STUB_ENGINE_BOXES=late \
  "$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out4c" </dev/null
order "$CP_DOWN" "ec2 terminate-instances --instance-ids i-late"
grep -q "no CP left to end them" "$WORK/out4c" || fail "the sweep did not say why it terminated: $(tail -3 "$WORK/out4c")"

echo "== case 4d: the database stops AFTER the control plane, and only when it is running =="
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4d" </dev/null
order "$CP_DOWN" "rds stop-db-instance --db-instance-identifier t-db"
: > "$LOG"
STUB_DB_STATUS=stopped \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4d2" </dev/null
hasnt "rds stop-db-instance"   # stopping a stopped instance is refused by RDS
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast --keep-db > "$WORK/out4d3" </dev/null
hasnt "rds stop-db-instance"
grep -q "kept running" "$WORK/out4d3" || fail "--keep-db did not say the database is still billing"

echo "== case 4e: --up starts the database and waits for it BEFORE the control plane =="
: > "$LOG"
STUB_DB_STATUS=stopped \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4e" </dev/null
order "rds start-db-instance --db-instance-identifier t-db" "$CP_UP"
order "rds wait db-instance-available --db-instance-identifier t-db" "$CP_UP"
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4e2" </dev/null
hasnt "rds start-db-instance"   # already available: nothing to start
has "$CP_UP"

echo "== case 4f: --status flags RDS's 7-day restart and a box with no CP =="
: > "$LOG"
STUB_ENGINE_BOXES=1 \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --status > "$WORK/out4f" </dev/null
grep -q "restarts a" "$WORK/out4f" || fail "status did not flag a running database under a paused CP"
grep -q "no control plane to end them" "$WORK/out4f" || fail "status did not flag a GPU box with no CP"
hasnt "update-service"; hasnt "terminate-instances"; hasnt "stop-db-instance"   # status changes nothing

echo "== case 4g: without --yes, pause writes nothing =="
: > "$LOG"
STUB_ENGINES_LIVE=1 STUB_ENGINE_BOXES=1 STUB_ENGINE_SVC_UP=1 \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --fast > "$WORK/out4g" </dev/null
hasnt "update-service"; hasnt "terminate-instances"; hasnt "stop-instances"; hasnt "stop-db-instance"
grep -q "database stopped" "$WORK/out4g" || fail "the plan did not mention the database step"
grep -q "NAT gateway deleted" "$WORK/out4g" || fail "the plan did not mention the NAT step"
hasnt "cloudformation deploy"; hasnt "add-tags-to-resource"

NAT_DEL="cloudformation deploy --stack-name t-network"

echo "== case 4h: the NAT gateway goes LAST (after the CP, the sweep and the database), through the stack =="
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4h" </dev/null
order "$CP_DOWN" "rds add-tags-to-resource"
order "rds add-tags-to-resource" "rds stop-db-instance --db-instance-identifier t-db"   # tagged before it can be force-started
order "rds stop-db-instance --db-instance-identifier t-db" "$NAT_DEL"
has "NatEnabled=false"
has "rds add-tags-to-resource --resource-name arn:aws:rds:ap-northeast-1:123456789012:db:t-db --tags Key=af-paused,Value=true"
hasnt "delete-nat-gateway"   # never by hand: the stack would keep a dead physical id
: > "$LOG"
STUB_DB_STATUS=stopped \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4h2" </dev/null
has "rds add-tags-to-resource"   # a database that is already stopped is tagged all the same

echo "== case 4i: --keep-nat, and a NAT that is already gone, leave the network stack alone =="
: > "$LOG"
"$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --fast --keep-nat > "$WORK/out4i" </dev/null
hasnt "$NAT_DEL"
grep -q "NAT (kept" "$WORK/out4i" || fail "--keep-nat did not say the gateway is still billing"
: > "$LOG"
STUB_NAT_ENABLED=false \
  "$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4i2" </dev/null
hasnt "$NAT_DEL"
: > "$LOG"
"$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --fast --dry-run --yes > "$WORK/out4i3" </dev/null
hasnt "cloudformation deploy"
grep -q "DRY: cloudformation deploy --stack-name t-network .*NatEnabled=false" "$WORK/out4i3" || fail "dry-run did not print the NAT step"

echo "== case 4j: --up brings the NAT back FIRST, and takes the tag off before it starts the database =="
: > "$LOG"
STUB_NAT_ENABLED=false STUB_DB_STATUS=stopped \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4j" </dev/null
order "$NAT_DEL" "rds remove-tags-from-resource"
order "rds remove-tags-from-resource" "rds start-db-instance --db-instance-identifier t-db"
order "rds start-db-instance --db-instance-identifier t-db" "$CP_UP"
has "NatEnabled=true"
has "--tag-keys af-paused"
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4j2" </dev/null
hasnt "$NAT_DEL"   # the gateway is already there: nothing to recreate

echo "== case 4j-ws: pause/--up on a network stack older than #2716 never switches the S3 endpoint on =="
# The checkout's 00-network adds the workspace subnets and S3Endpoint; the endpoint's policy would
# refuse the own S3 of every workspace still in a private subnet, so an old stack gets "false".
: > "$LOG"
STUB_NAT_ENABLED=false \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4jw" 2>&1 </dev/null
grep -q "$NAT_DEL .*NatEnabled=true S3EndpointEnabled=false" "$LOG" \
  || fail "--up on a stack without S3EndpointEnabled did not pass S3EndpointEnabled=false"
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4jw2" 2>&1 </dev/null
grep -q "$NAT_DEL .*NatEnabled=false S3EndpointEnabled=false" "$LOG" \
  || fail "pause on a stack without S3EndpointEnabled did not pass S3EndpointEnabled=false"
# A stack that already has the switch keeps its value: the operator's step, not pause.sh's.
: > "$LOG"
STUB_NAT_ENABLED=false STUB_S3EP=true \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4jw3" 2>&1 </dev/null
has "NatEnabled=true"
if grep -q "S3EndpointEnabled=" "$LOG"; then fail "--up overrode an existing S3EndpointEnabled"; fi
# --up says so, before the CP is back, when the CP is not using the network's workspace subnets.
grep -q "WARNING: .* has workspace subnets" "$WORK/out4jw" && fail "warned without workspace subnets in the network"
: > "$LOG"
STUB_WS_SUBNETS=subnet-0a,subnet-0b STUB_S3EP=true \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4jw4" 2>&1 </dev/null
grep -q "WARNING: t-network has workspace subnets (subnet-0a,subnet-0b) that the CP is not using" "$WORK/out4jw4" \
  || { cat "$WORK/out4jw4"; fail "--up did not warn that the CP lacks the workspace subnets"; }
grep -q "The S3 endpoint is ON" "$WORK/out4jw4" || fail "--up did not say the endpoint refuses workspace S3"
: > "$LOG"
STUB_WS_SUBNETS=subnet-0a,subnet-0b STUB_ING_WS=subnet-0a,subnet-0b STUB_S3EP=true \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4jw5" 2>&1 </dev/null
if grep -q "WARNING: .* has workspace subnets" "$WORK/out4jw5"; then fail "warned although the CP uses the workspace subnets"; fi

echo "== case 4j-egress: pause stops the egress gateway after the Workspaces and before the CP (#2729) =="
GW_DOWN="--parameter-overrides GatewayEnabled=false"
: > "$LOG"
STUB_EGRESS_ROUTE=gateway \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4je" 2>&1 </dev/null \
  || { cat "$WORK/out4je"; fail "pause.sh failed on a stack with EgressRoute=gateway"; }
order "ecs update-service --cluster t-cluster --service af-ws-alice --desired-count 0" "ecs describe-tasks"
order "ecs describe-tasks" "ssm send-command --instance-ids i-gw"
grep -q "ssm send-command .*egress gateway drain" "$LOG" || fail "pause.sh did not drain the gateway"
order "get-command-invocation --command-id cmd-drain" "$GW_DOWN"
order "$GW_DOWN" "$CP_DOWN"
grep -q "$NAT_DEL .*--capabilities CAPABILITY_IAM $GW_DOWN" "$LOG" || fail "the gateway was not stopped through the network stack"
# The NAT still goes last, in its own deploy that leaves the gateway switch alone.
order "$CP_DOWN" "--parameter-overrides NatEnabled=false"
grep "NatEnabled=false" "$LOG" | grep -q "GatewayEnabled" && fail "the NAT deploy touched GatewayEnabled"
# Never the interface or the address: they are the route target and the egress address.
hasnt "release-address"; hasnt "disassociate-address"; hasnt "delete-network-interface"; hasnt "detach-network-interface"
hasnt "autoscaling "   # through the stack only, or the next deploy scales it back

echo "== case 4j-egress-scripts: what the gateway runs: applied (not just written) policy, and the drain =="
# The two scripts env.sh sends through SSM, run here with fake systemctl / journalctl and their
# paths moved under $GW. Python is the real one (the drain reads xattrs and state.json).
GW="$WORK/gw"; mkdir -p "$GW/bin" "$GW/lib" "$GW/log" "$GW/run"
gw_script() { # gw_script PROBE|DRAIN -> the script with its paths under $GW
  # shellcheck disable=SC1091  # only the two variables are read
  ( . "$ECS/env.sh"; eval "printf '%s\n' \"\$AF_GATEWAY_$1\"" ) |
    sed -e "s#/var/lib/af-egress-agent#$GW/lib#g" -e "s#/var/log/squid#$GW/log#g" -e "s#/run/#$GW/run/#g"
}
# Squid's state is a file: stop creates it (GW_STOP=fail fails, GW_STOP=noop leaves Squid
# running), start removes it.
cat > "$GW/bin/systemctl" <<'FAKE'
#!/usr/bin/env bash
echo "systemctl $*" >> "$GW_LOG"
case "$1" in
  is-active) [ "${*: -1}" = squid ] && [ -e "$GW_STATE/squid-stopped" ] && exit 3
             [ "${GW_ACTIVE:-1}" = 1 ] ;;
  stop) case "${GW_STOP:-ok}" in
          fail) echo "injected stop failure" >&2; exit 1 ;;
          noop) ;;
          *) touch "$GW_STATE/squid-stopped" ;;
        esac ;;
  start) rm -f "$GW_STATE/squid-stopped" ;;
  show) echo "${GW_INV:-inv-now}" ;;
esac
FAKE
cat > "$GW/bin/journalctl" <<'FAKE'
#!/usr/bin/env bash
for a in "$@"; do case "$a" in _SYSTEMD_INVOCATION_ID=*) cat "$GW_JOURNAL/${a#*=}" 2>/dev/null ;; esac; done
FAKE
chmod +x "$GW/bin/systemctl" "$GW/bin/journalctl"
mkdir -p "$GW/journal"
gw_run() { GW_LOG="$GW/calls" GW_JOURNAL="$GW/journal" GW_STATE="$GW/run" PATH="$GW/bin:$PATH" bash -c "$(gw_script "$1")"; }
MODE_AGENT='# Generated by control-plane egress-agent. Do not edit: the next policy change overwrites it.
# mode: enforce'
# Journal lines as `journalctl -o short-unix` prints them; the files' mtimes are set to match.
jl() { echo "$1 ip-10-20-0-5 af-cp[812]: 2026/10/25 09:00:00 egress-agent: $2"; }
policy_at() { # policy_at <unix time> [bootstrap]: the two policy files, last written then
  if [ "${2:-}" = bootstrap ]; then printf '# bootstrap: closed until the first policy\nhttp_access deny all\n' > "$GW/lib/af-mode.conf"
  else printf '%s\n' "$MODE_AGENT" > "$GW/lib/af-mode.conf"; fi
  echo .example.com > "$GW/lib/af-allow.txt"
  touch -d "@$1" "$GW/lib/af-mode.conf" "$GW/lib/af-allow.txt"
}
APPLIED='policy applied (enforce)'
policy_at 1000 bootstrap; jl 1100 "$APPLIED" > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: the bootstrap mode file read as ready"
policy_at 1000; : > "$GW/journal/inv-now"; jl 1100 "$APPLIED" > "$GW/journal/inv-old"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: a policy applied by an earlier run of the agent read as ready"
jl 1100 'policy not updated, keeping the last one that worked: reload: exit status 1' > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: a written policy whose reload failed read as ready"
jl 1100 "$APPLIED" > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-ready ] || fail "probe: an applied policy did not read as ready"
# Started while the CP was down: the policy on disk (written by an earlier run) reloaded; the CP
# later answers with the same policy, so no "policy applied" ever follows. Ready all the same.
{ jl 1100 'reloaded Squid with the policy on disk (enforce)'
  jl 1130 'policy not updated, keeping the last one that worked: dial tcp: connection refused'; } > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-ready ] || fail "probe: a policy reloaded from disk in this run (CP answers the same) did not read as ready"
# A new instance: the bootstrap files reloaded from disk while the CP was down, then the CP's
# policy written and its reload failed, or not run yet. Squid still has the bootstrap policy.
policy_at 1150; { jl 1100 'reloaded Squid with the policy on disk (log-only)'
  jl 1160 'policy not updated, keeping the last one that worked: reload: exit status 1'; } > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: a bootstrap reload followed by a failed reload of the CP's policy read as ready"
jl 1100 'reloaded Squid with the policy on disk (log-only)' > "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: a bootstrap reload with the CP's policy written but not yet reloaded read as ready"
jl 1170 "$APPLIED" >> "$GW/journal/inv-now"
[ "$(gw_run PROBE)" = af-gw-ready ] || fail "probe: the CP's policy reloaded after it was written did not read as ready"
# Only the allow file changed (same mode): its write counts too.
touch -d @1200 "$GW/lib/af-allow.txt"
[ "$(gw_run PROBE)" = af-gw-up ] || fail "probe: an allow file written after the last reload read as ready"
policy_at 1000; jl 1100 "$APPLIED" > "$GW/journal/inv-now"
[ "$(GW_ACTIVE=0 gw_run PROBE)" = af-gw-starting ] || fail "probe: stopped services did not read as starting"
: > "$GW/calls"; touch "$GW/run/af-egress-drained"
gw_run PROBE > /dev/null
grep -q "systemctl start squid" "$GW/calls" && [ ! -e "$GW/run/af-egress-drained" ] \
  || fail "probe: Squid stopped by a drain was not started again"
: > "$GW/calls"; gw_run PROBE > /dev/null
grep -q "systemctl start" "$GW/calls" && fail "probe: started Squid without the drain's marker (a booting box would run the default config)"
# The drain: the log against the committed offsets, by generation id.
printf 'a\nbb\n' > "$GW/log/af-egress.log"; printf 'old\n' > "$GW/log/af-egress.log.0"
python3 -c "import os,sys; os.setxattr(sys.argv[1],'user.af-egress.gen',b'g1'); os.setxattr(sys.argv[2],'user.af-egress.gen',b'g0')" \
  "$GW/log/af-egress.log" "$GW/log/af-egress.log.0" || fail "drain: this filesystem has no user xattrs (the check cannot run)"
echo '{"offsets":{"g1":2,"g0":4}}' > "$GW/lib/state.json"
: > "$GW/calls"
gw_run DRAIN | grep -q '^af-gw-pending 3 ' || fail "drain: a log line the CP has not accepted read as drained"
grep -q "systemctl stop squid" "$GW/calls" && [ -e "$GW/run/af-egress-drained" ] || fail "drain: Squid was not stopped behind its marker"
echo '{"offsets":{"g1":5}}' > "$GW/lib/state.json"
gw_run DRAIN | grep -q '^af-gw-pending 4 ' || fail "drain: an unshipped rotated log read as drained"
echo '{"offsets":{"g1":5,"g0":4}}' > "$GW/lib/state.json"
[ "$(gw_run DRAIN)" = af-gw-drained ] || fail "drain: a fully accepted log did not read as drained"
# A Squid that did not stop has no final log, however much of it was accepted.
rm -f "$GW/run/squid-stopped"
out="$(GW_STOP=fail gw_run DRAIN 2>/dev/null)"
case "$out" in af-gw-error*) ;; *) fail "drain: a failed Squid stop read as '$out'" ;; esac
out="$(GW_STOP=noop gw_run DRAIN)"
case "$out" in af-gw-error*) ;; *) fail "drain: a Squid still running read as '$out'" ;; esac
rm -f "$GW/run/af-egress-drained" "$GW/run/squid-stopped"; chmod 0555 "$GW/run"
out="$(gw_run DRAIN 2>/dev/null)"; chmod 0755 "$GW/run"
[ "$out" = 'af-gw-error: cannot write the drain marker' ] || fail "drain: an unwritable marker read as '$out'"
rm "$GW/lib/state.json"
gw_run DRAIN | grep -q '^af-gw-pending' || fail "drain: no state file read as drained"

echo "== case 4j-egress-drain: nothing unconfirmed is destroyed: tasks, task reads and the log =="
# A workspace already at desired 0 (no service to stop) whose task is still up is waited for.
: > "$LOG"
STUB_EGRESS_ROUTE=gateway STUB_WS_DESIRED0=1 STUB_WS_TASKS=late \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4jd" 2>&1 </dev/null \
  || { cat "$WORK/out4jd"; fail "pause.sh failed waiting for a desired-0 workspace task"; }
hasnt "ecs update-service --cluster t-cluster --service af-ws-alice"
[ "$(grep -c "ecs describe-tasks" "$LOG")" -ge 2 ] || fail "the desired-0 task was not waited for"
order_again "ecs describe-tasks" "$GW_DOWN"
# A task that stays, a task read that is refused, a log the CP has not accepted: the gateway
# and the CP are both left running, and the run fails.
for knob in STUB_WS_TASKS=stuck STUB_WS_TASKS=fail STUB_ENI_FAIL=1 STUB_GW_DRAIN=pending; do
  : > "$LOG"
  if env STUB_EGRESS_ROUTE=gateway "$knob" AF_WS_TASK_WAIT_SEC=0 AF_GATEWAY_DRAIN_SEC=0 \
       "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4jd2" 2>&1 </dev/null; then
    fail "pause.sh exited 0 with $knob"
  fi
  hasnt "GatewayEnabled="; hasnt "$CP_DOWN"; hasnt "NatEnabled=false"
  grep -q "left running" "$WORK/out4jd2" || fail "$knob: pause.sh did not say the gateway and the CP were left running"
done
grep -q "not accepted all of the egress gateway's log" "$WORK/out4jd2" || fail "the drain failure did not say why"
# A report that failed once (the agent backs off) is waited out, not given up on.
: > "$LOG"
STUB_EGRESS_ROUTE=gateway STUB_GW_DRAIN=late \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4jd3" 2>&1 </dev/null \
  || { cat "$WORK/out4jd3"; fail "pause.sh gave up on a drain that completes on the second look"; }
order_again "get-command-invocation --command-id cmd-drain" "$GW_DOWN"
: > "$LOG"
STUB_EGRESS_ROUTE=gateway STUB_GW_ENABLED=false \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4je2" 2>&1 </dev/null
hasnt "GatewayEnabled="   # already stopped
: > "$LOG"
STUB_EGRESS_ROUTE=gateway \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --fast > "$WORK/out4je3" 2>&1 </dev/null
grep -q "engines idle -> egress gateway stopped -> CP desired 0" "$WORK/out4je3" || fail "the plan did not show the gateway step"
hasnt "cloudformation deploy"
: > "$LOG"
STUB_EGRESS_ROUTE=gateway \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast --dry-run > "$WORK/out4je4" 2>&1 </dev/null
grep -q "DRY: cloudformation deploy --stack-name t-network .*GatewayEnabled=false" "$WORK/out4je4" || fail "dry-run did not print the gateway step"
hasnt "cloudformation deploy"

echo "== case 4j-egress2: --up starts the gateway with the NAT and waits for it before and after the CP =="
: > "$LOG"
STUB_EGRESS_ROUTE=gateway STUB_GW_ENABLED=false STUB_NAT_ENABLED=false STUB_GW_PROBE=model \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4je5" 2>&1 </dev/null \
  || { cat "$WORK/out4je5"; fail "pause.sh --up failed on a stack with EgressRoute=gateway"; }
grep -q "$NAT_DEL .*--parameter-overrides NatEnabled=true GatewayEnabled=true" "$LOG" \
  || fail "--up did not start the NAT and the gateway in one network deploy"
order "GatewayEnabled=true" "ssm send-command --instance-ids i-gw"
order "ssm send-command --instance-ids i-gw" "$CP_UP"   # up before any workspace can start
order_again "$CP_UP" "ssm send-command --instance-ids i-gw"   # and its first policy after the CP
grep -q "first policy from the control plane" "$WORK/out4je5" || fail "--up did not say it waits for the first policy"
# A gateway that is already on is still waited for (an earlier --up may have stopped half way).
: > "$LOG"
STUB_EGRESS_ROUTE=gateway \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4je6" 2>&1 </dev/null
hasnt "$NAT_DEL"
order "ssm send-command --instance-ids i-gw" "$CP_UP"
# An interface nobody attaches: the CP stays down.
: > "$LOG"
if STUB_EGRESS_ROUTE=gateway STUB_GW_ENABLED=false STUB_GW_ATTACH='' AF_GATEWAY_WAIT_SEC=0 \
     "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4je7" 2>&1 </dev/null; then
  fail "--up exited 0 although the gateway never attached its interface"
fi
hasnt "$CP_UP"
grep -q "eni-gw is not attached" "$WORK/out4je7" || fail "--up did not say what it last saw of the gateway"
# Up, but no policy once the CP is there: a failure, after the CP.
: > "$LOG"
if STUB_EGRESS_ROUTE=gateway STUB_GW_PROBE=af-gw-up AF_GATEWAY_WAIT_SEC=0 \
     "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4je8" 2>&1 </dev/null; then
  fail "--up exited 0 although the gateway never got a policy"
fi
has "$CP_UP"
grep -q "has no policy from it" "$WORK/out4je8" || fail "--up did not say the gateway has no policy"
# Squid or the agent not running is not "up".
: > "$LOG"
if STUB_EGRESS_ROUTE=gateway STUB_GW_PROBE=af-gw-starting AF_GATEWAY_WAIT_SEC=0 \
     "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4je9" 2>&1 </dev/null; then
  fail "--up exited 0 although Squid / the agent were not running"
fi
hasnt "$CP_UP"

echo "== case 4j-egress3: --status shows the gateway; a stack without one is paused exactly as before =="
: > "$LOG"
STUB_EGRESS_ROUTE=gateway \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --status > "$WORK/out4je10" 2>&1 </dev/null
grep -q "egress gateway: on (i-gw)" "$WORK/out4je10" || fail "status did not show the running gateway"
grep -q "the egress gateway is on while the control plane is paused" "$WORK/out4je10" || fail "status did not flag a gateway left on"
hasnt "cloudformation deploy"; hasnt "ssm send-command"
: > "$LOG"
STUB_EGRESS_ROUTE=gateway STUB_GW_ENABLED=false \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --status > "$WORK/out4je11" 2>&1 </dev/null
grep -q "egress gateway: stopped" "$WORK/out4je11" || fail "status did not show the stopped gateway"
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --status > "$WORK/out4je12" 2>&1 </dev/null
grep -q "egress gateway" "$WORK/out4je12" && fail "status showed a gateway on a stack without one"
for mode in --fast --up; do
  : > "$LOG"
  STUB_NAT_ENABLED=false STUB_GW_ENABLED=false \
    "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes "$mode" > "$WORK/out4je13" 2>&1 </dev/null
  hasnt "GatewayEnabled="; hasnt "ssm send-command"; hasnt "describe-network-interfaces"; hasnt "ecs describe-tasks"
done

echo "== case 4k: --status shows the NAT, and names the re-stop Lambda when the stack has one =="
: > "$LOG"
STUB_NAT_ENABLED=false STUB_RESTOP=t-data-RestopFunction-1 \
  "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --status > "$WORK/out4k" </dev/null
grep -q "nat gateway   : deleted" "$WORK/out4k" || fail "status did not say the NAT is deleted"
grep -q "re-stop Lambda t-data-RestopFunction-1" "$WORK/out4k" || fail "status did not name the re-stop Lambda"
hasnt "cloudformation deploy"; hasnt "add-tags-to-resource"

echo "== case 4l: a capture taken while paused (NatEnabled=false) still stands up WITH a NAT =="
STATE6="$AF_DEPLOY_STATE_DIR/p6.ap-northeast-1.t-ingress"
mkdir -p "$STATE6/params"
cp -a "$STATE/params/." "$STATE6/params/"
cp "$STATE/env" "$STATE6/env"
echo "NatEnabled=false" >> "$STATE6/params/00-network"
: > "$LOG"
"$ECS/standup.sh" --profile p6 --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out4l" </dev/null
grep -q "NatEnabled=true" "$WORK/out4l" || fail "standup did not force NatEnabled=true"
if grep -q "NatEnabled=false" "$WORK/out4l"; then fail "standup passed the captured NatEnabled=false on"; fi
# The same for a capture taken with the egress gateway stopped.
echo "GatewayEnabled=false" >> "$STATE6/params/00-network"
: > "$LOG"
"$ECS/standup.sh" --profile p6 --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out4l2" </dev/null
grep -q "GatewayEnabled=true" "$WORK/out4l2" || fail "standup did not force GatewayEnabled=true"
if grep -q "GatewayEnabled=false" "$WORK/out4l2"; then fail "standup passed the captured GatewayEnabled=false on"; fi

echo "== case 4l-eip: a handed-back workspace egress address is checked before anything is built (#2728) =="
STATE7="$AF_DEPLOY_STATE_DIR/p7.ap-northeast-1.t-ingress"
mkdir -p "$STATE7/params"
cp -a "$STATE/params/." "$STATE7/params/"
cp "$STATE/env" "$STATE7/env"
printf 'EgressRoute=gateway\nGatewayEipAllocationId=eipalloc-GW\nGatewayEipAddress=203.0.113.7\n' >> "$STATE7/params/00-network"
: > "$LOG"
"$ECS/standup.sh" --profile p7 --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out4le" 2>&1 </dev/null \
  || { cat "$WORK/out4le"; fail "standup refused a free, matching workspace egress address"; }
has "ec2 describe-addresses --allocation-ids eipalloc-GW"
{ grep -q "GatewayEipAllocationId=eipalloc-GW" "$WORK/out4le" && grep -q "GatewayEipAddress=203.0.113.7" "$WORK/out4le"; } \
  || fail "standup did not hand the captured workspace egress address back to 00-network"
for knob in STUB_GW_EIP=gone STUB_GW_EIP=elsewhere; do
  : > "$LOG"
  if env "$knob" "$ECS/standup.sh" --profile p7 --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out4le2" 2>&1 </dev/null; then
    fail "standup passed preflight with $knob"
  fi
  hasnt "cloudformation deploy"
done
grep -q "associated with eni-other" "$WORK/out4le2" || fail "standup did not say where the address is held"
sed -i 's/^GatewayEipAddress=.*/GatewayEipAddress=198.51.100.9/' "$STATE7/params/00-network"
: > "$LOG"
if "$ECS/standup.sh" --profile p7 --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out4le3" 2>&1 </dev/null; then
  fail "standup passed preflight with an address that is not the allocation's"
fi
grep -q "GatewayEipAddress '198.51.100.9' is not the address of eipalloc-GW (203.0.113.7)" "$WORK/out4le3" \
  || fail "standup did not say the captured address does not match"
# Without the parameter (every capture so far) nothing is looked up.
: > "$LOG"
"$ECS/standup.sh" --profile p6 --region ap-northeast-1 --stack t-ingress --yes --dry-run > /dev/null 2>&1 </dev/null
hasnt "describe-addresses"

echo "== case 4m: a tag that cannot be written is an error, not a quiet success =="
: > "$LOG"
if STUB_DB_ARN_FAIL=1 "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4m" 2>&1 </dev/null; then
  fail "pause exited 0 although the af-paused tag could not be set"
fi
has "rds stop-db-instance --db-instance-identifier t-db"   # the bill comes first
has "$NAT_DEL"                                            # and the rest of the pause still ran
grep -q "was not set" "$WORK/out4m" || fail "the failure did not say the tag was not set"
grep -q "not be stopped again" "$WORK/out4m" || fail "the failure did not say what is now unprotected"
: > "$LOG"
if STUB_DB_ARN_FAIL=1 STUB_DB_STATUS=stopped "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4m2" 2>&1 </dev/null; then
  fail "--up exited 0 although the af-paused tag could not be removed"
fi
hasnt "rds start-db-instance"
hasnt "$CP_UP"
: > "$LOG"
if STUB_DB_ARN_FAIL=1 "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast --keep-db > "$WORK/out4m3" 2>&1 </dev/null; then
  fail "--keep-db exited 0 although the af-paused tag could not be removed"
fi
grep -q "was not removed" "$WORK/out4m3" || fail "the clear failure did not say the tag was not removed"
grep -q -- "pause.sh --keep-db again" "$WORK/out4m3" || fail "the clear failure did not keep --keep-db in the rerun hint"
echo "== case 4m4: --up fails loudly when the database is not available once the CP is up =="
: > "$LOG"
if STUB_DB_STATUS=backing-up "$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4m4" 2>&1 </dev/null; then
  fail "--up exited 0 with the database not available"
fi

echo "== case 4n: --keep-db takes an old af-paused marker off; --up reads the status only after the tag is gone =="
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --fast --keep-db > "$WORK/out4n" </dev/null
has "rds remove-tags-from-resource"
hasnt "rds add-tags-to-resource"
hasnt "rds stop-db-instance"
: > "$LOG"
"$ECS/pause.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --up > "$WORK/out4n2" </dev/null
order "rds remove-tags-from-resource" "DBInstanceStatus"

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
hasnt "backup delete-recovery-point"
hasnt "backup delete-backup-vault"
grep -q "retain" "$WORK/out6" || fail "it did not say that retain kept things"
grep -q "vault t-data-efs-1a2b3c4d (1 recovery points) is kept" "$WORK/out6" || fail "it did not say that retain kept the EFS backup vault"

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
# The vault refuses deletion while it holds a recovery point, so the points go first, and
# the vault is found by its prefix even though 10-data is already gone.
order "cloudformation wait stack-delete-complete --stack-name t-network" "backup delete-recovery-point"
has "backup delete-recovery-point --backup-vault-name t-data-efs-1a2b3c4d --recovery-point-arn arn:aws:backup:ap-northeast-1:123456789012:recovery-point:rp-1"
order "backup delete-recovery-point" "backup delete-backup-vault --backup-vault-name t-data-efs-1a2b3c4d"
order_again "backup delete-backup-vault" "backup list-backup-vaults"
grep -q "efs backup vaults *0" "$WORK/out7" || fail "the sweep did not re-count the vaults"
# The prefix is the only thing that keeps another deployment's backups out of the purge.
hasnt "--backup-vault-name other-"

echo "== case 7b: retain from before the EFS backups — no vault, nothing to delete =="
: > "$LOG"
STUB_VAULT_ABSENT=1 \
  "$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --purge-retained > "$WORK/out7b" </dev/null
has "backup list-backup-vaults"
hasnt "backup delete-recovery-point"
hasnt "backup delete-backup-vault"

echo "== case 7c: --dry-run --purge-retained prints the backup deletions and makes none =="
: > "$LOG"
"$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --purge-retained --dry-run > "$WORK/out7c" </dev/null
hasnt "backup delete-"
grep -q "DRY: backup delete-recovery-point --backup-vault-name t-data-efs-1a2b3c4d" "$WORK/out7c" \
  || fail "dry-run did not print the recovery-point deletion"
grep -q "DRY: backup delete-backup-vault --backup-vault-name t-data-efs-1a2b3c4d" "$WORK/out7c" \
  || fail "dry-run did not print the vault deletion"

echo "== case 7d: a recovery point that will not go is reported, not waited on forever =="
: > "$LOG"
STUB_RP_STUCK=1 AF_VAULT_WAIT_TRIES=2 AF_VAULT_WAIT_SEC=0 \
  "$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --purge-retained > "$WORK/out7d" </dev/null
grep -q "1 recovery points still in t-data-efs-1a2b3c4d" "$WORK/out7d" || fail "a stuck recovery point went unreported"

echo "OK: deployment lifecycle stub test passed"

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

# --- fake aws. Answers queries in the same shape the real one does ----------
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
args="$*"
case "$args" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  *"cloudformation list-exports"*"SlotLaunchTemplateId"*) echo "t-pool-SlotLaunchTemplateId" ;;
  *"cloudformation list-exports"*) echo "t-cluster" ;;
  *"--profile p2"*"describe-stack-resource"*) echo "t-db" ;;
  *"describe-stack-resource"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EfsId']"*) echo "fs-1" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotLaunchTemplateId']"*) echo "lt-NEW" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='CfnTemplatesBucket']"*) echo "t-cfn-bucket" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotAmiIdArm64']"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='Url']"*) echo "https://af.example.test" ;;
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
  *"ecr describe-images"*)
    # After a teardown the ECR is empty. Forces standup down the crane copy path.
    [ "${STUB_ECR_HAS:-0}" = 1 ] || exit 1 ;;
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
  manifest) echo '{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}]}' ;;
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
# Does the flag reach the value that is actually passed? Passing the check and then standing
# up on the default value is something that really happened.
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --cp-arch arm64 > /dev/null </dev/null
grep -q "deploy --stack-name t-ingress .*CpArch=arm64" "$LOG" || fail "--cp-arch did not reach the CFN parameters"
if grep -q "deploy --stack-name t-ingress .*CpArch=x86_64" "$LOG"; then fail "the captured CpArch overrode the flag"; fi

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

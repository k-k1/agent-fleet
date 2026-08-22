#!/usr/bin/env bash
# Build the workspace image for arm64 natively and PUSH it to a deployment's ECR
# (docs/70 §70.9 / P5). The read-only sibling is build-arm64-image.sh.
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/push-arm64-image.sh --ref v0.10.0 --tag 0.10.0-arm64
#
# ## Why this is a separate script from build-arm64-image.sh
#
# Pushing needs a credential, and that is the whole difference. This one creates a
# throwaway IAM role + instance profile scoped to ONE ECR repository, hands it to the
# instance, and deletes all three afterwards. build-arm64-image.sh deliberately has no
# credential at all, which is why it can stay a pure "does it build" check.
#
# ⚠️ The credential is NEVER passed through user-data. user-data is readable from IMDS
# by anything on the box — including the build it is running, and anything that build
# pulls from the network. The instance profile lets the instance mint its own ECR token
# instead, so there is no secret to leak in the first place.
#
# ⚠️ It pushes a SINGLE-ARCHITECTURE arm64 image under its own tag. Joining it to the
# amd64 image as a multi-arch index is a separate, deliberate step (crane index append)
# so that the moment a shared tag changes meaning is one you choose, not a side effect
# of a build finishing.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

TYPE="m8g.2xlarge"
# ⚠️ 0 = the LEAN variant, which is what a release actually publishes
# (deploy/compose/release.sh defaults BAKE_AGENT_CLIS=0 and publish-dist never
# overrides it). Getting this wrong does not fail anything — it produces an arm64
# image that WORKS and is simply a different product from the amd64 one under the
# same tag: baked CLIs in /usr/local versus boot-installed into ~/.local. Measured
# once: 2,536 MiB against the released 920 MiB, which is the only reason it was
# noticed at all.
BAKE=0
REPO="af-workspace"
TAG=""
REF=""
BUDGET_SEC=3600
RUN_TAG="af-armpush-$$-$(date +%s)"
GIT_REPO="https://github.com/k-k1/agent-fleet"

while [ $# -gt 0 ]; do
  case "$1" in
    --type) TYPE="${2:?}"; shift ;;
    --repo) REPO="${2:?}"; shift ;;
    --tag)  TAG="${2:?}"; shift ;;
    --bake) BAKE="${2:?}"; shift ;;
    --ref)  REF="${2:?}"; shift ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$TAG" ] && [ -n "$REF" ] || { echo "--ref and --tag are required" >&2; exit 2; }

say() { printf '==> %s\n' "$*" >&2; }

ACCOUNT=$("${AWS[@]}" sts get-caller-identity --query Account --output text)
ECR_HOST="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"
ROLE="af-armpush-$$"
say "account=$ACCOUNT repo=$REPO tag=$TAG ref=$REF bake=$BAKE role=$ROLE"

# The repo must already exist — it is owned by the 20-platform stack, and creating one
# here out of band is what breaks the next CFN deploy with AlreadyExists.
"${AWS[@]}" ecr describe-repositories --repository-names "$REPO" >/dev/null

# --- sweep, armed before anything exists ------------------------------------------
sweep() {
  local ids
  ids=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-armpush-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  if [ -n "$ids" ]; then
    say "terminating: $ids"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null || true
    # The instance profile cannot be deleted while it is associated with a live
    # instance, so wait for the terminate to take before unwinding IAM.
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 wait instance-terminated --instance-ids $ids 2>/dev/null || true
  fi
  # IAM comes off in the reverse order it went on. Each step tolerates "already gone"
  # so a re-run of the sweep is harmless.
  "${AWS[@]}" iam remove-role-from-instance-profile --instance-profile-name "$ROLE" --role-name "$ROLE" >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-instance-profile --instance-profile-name "$ROLE" >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-role-policy --role-name "$ROLE" --policy-name push >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-role --role-name "$ROLE" >/dev/null 2>&1 || true
  local left
  left=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-armpush-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  local role_left
  role_left=$("${AWS[@]}" iam get-role --role-name "$ROLE" --query 'Role.RoleName' --output text 2>/dev/null || echo "none")
  say "residual: instances=${left:-none} role=$role_left"
}
trap sweep EXIT INT TERM

# --- the credential: scoped to one repository, and to pushing ----------------------
"${AWS[@]}" iam create-role --role-name "$ROLE" \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
  --tags Key=af-armpush-run,Value="$RUN_TAG" >/dev/null
"${AWS[@]}" iam put-role-policy --role-name "$ROLE" --policy-name push --policy-document "$(cat <<JSON
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
 {"Effect":"Allow","Resource":"arn:aws:ecr:$REGION:$ACCOUNT:repository/$REPO",
  "Action":["ecr:BatchCheckLayerAvailability","ecr:CompleteLayerUpload","ecr:InitiateLayerUpload","ecr:PutImage","ecr:UploadLayerPart","ecr:BatchGetImage","ecr:GetDownloadUrlForLayer"]}]}
JSON
)"
"${AWS[@]}" iam create-instance-profile --instance-profile-name "$ROLE" >/dev/null
"${AWS[@]}" iam add-role-to-instance-profile --instance-profile-name "$ROLE" --role-name "$ROLE"
say "created role + instance profile $ROLE (scoped to $REPO, push only)"

VPC=$("${AWS[@]}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
SUBNET=$("${AWS[@]}" ec2 describe-subnets --filters "Name=vpc-id,Values=$VPC" \
  --query 'Subnets[?MapPublicIpOnLaunch==`true`]|[0].SubnetId' --output text)
SG=$("${AWS[@]}" ec2 describe-security-groups --filters "Name=vpc-id,Values=$VPC" Name=group-name,Values=default \
  --query 'SecurityGroups[0].GroupId' --output text)
AMI=$("${AWS[@]}" ssm get-parameter \
  --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id \
  --query Parameter.Value --output text)

read -r -d '' UD <<EOF || true
#!/bin/bash
exec 2>&1
export HOME=/root
say() { echo "AF-PUSH|\$1" > /dev/console; }
emit() { tr -d '|' | while IFS= read -r l; do say "\$1|\$l"; done; }

say "uname|\$(uname -m)"
systemctl start docker 2>/dev/null || true
for i in \$(seq 30); do docker info >/dev/null 2>&1 && break; sleep 2; done
dnf install -y git >/dev/null 2>&1

cd /root
git clone --depth 1 --branch "${REF}" "${GIT_REPO}" repo || { say "clone|FAIL"; say "DONE"; exit 0; }
cd repo
say "head|\$(git rev-parse --short HEAD)"

s=\$SECONDS
if docker build --build-arg BAKE_AGENT_CLIS=${BAKE} -t "${ECR_HOST}/${REPO}:${TAG}" workspace >/tmp/b.log 2>&1; then
  say "build|\$((SECONDS-s))"
else
  say "build|FAIL"; tail -30 /tmp/b.log | emit build-err; say "DONE"; exit 0
fi

# The instance mints its own token from the instance profile — nothing was handed to it.
if ! aws ecr get-login-password --region "${REGION}" | docker login --username AWS --password-stdin "${ECR_HOST}" >/tmp/l.log 2>&1; then
  say "login|FAIL"; tail -5 /tmp/l.log | emit login-err; say "DONE"; exit 0
fi
s=\$SECONDS
if docker push "${ECR_HOST}/${REPO}:${TAG}" >/tmp/p.log 2>&1; then
  say "push|\$((SECONDS-s))"
  say "digest|\$(docker inspect --format '{{index .RepoDigests 0}}' "${ECR_HOST}/${REPO}:${TAG}" 2>/dev/null)"
else
  say "push|FAIL"; tail -20 /tmp/p.log | emit push-err
fi
say "DONE"
EOF

# ⚠️ IAM is eventually consistent: RunInstances rejects a profile that was created
# moments ago with "Invalid IAM Instance Profile name". Retry rather than fail the run.
ID=""
for i in $(seq 12); do
  if ID=$("${AWS[@]}" ec2 run-instances \
      --image-id "$AMI" --instance-type "$TYPE" --subnet-id "$SUBNET" \
      --security-group-ids "$SG" --associate-public-ip-address \
      --iam-instance-profile "Name=$ROLE" \
      --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2' \
      --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=120,VolumeType=gp3,DeleteOnTermination=true}' \
      --user-data "$UD" \
      --tag-specifications "ResourceType=instance,Tags=[{Key=af-armpush-run,Value=$RUN_TAG},{Key=Name,Value=af-arm64-push}]" \
      --query 'Instances[0].InstanceId' --output text 2>/dev/null); then
    break
  fi
  say "waiting for the instance profile to propagate ($i/12)"
  sleep 5
done
[ -n "$ID" ] || { echo "run-instances never succeeded" >&2; exit 1; }
say "launched $ID"

deadline=$((SECONDS + BUDGET_SEC))
while [ "$SECONDS" -lt "$deadline" ]; do
  sleep 45
  out=$("${AWS[@]}" ec2 get-console-output --instance-id "$ID" --latest --query Output --output text 2>/dev/null || true)
  printf '%s\n' "$out" | grep -o 'AF-PUSH|[^[:cntrl:]]*' | sed 's/^AF-PUSH|//' > /tmp/arm64-push.txt || true
  grep -q '^DONE' /tmp/arm64-push.txt 2>/dev/null && break
  say "working… ($(wc -l < /tmp/arm64-push.txt) lines, ${SECONDS}s/${BUDGET_SEC}s)"
done

echo
echo "=== arm64 workspace image -> $ECR_HOST/$REPO:$TAG (ref=$REF) ==="
cat /tmp/arm64-push.txt 2>/dev/null || echo "(no output)"

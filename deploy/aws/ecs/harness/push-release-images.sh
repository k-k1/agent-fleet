#!/usr/bin/env bash
# Build the CP + workspace images the way a release does, on an EC2 instance, and push
# them to a deployment's ECR under one tag (docs/70 §70.14).
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/push-release-images.sh --ref temp/xyz --tag 0.10.1-dev-abc1234
#
# ## Why it runs the real release scripts rather than `docker build`
#
# Two things about the release build are easy to get wrong by hand and invisible
# afterwards:
#
#   - the workspace image is the LEAN variant (BAKE_AGENT_CLIS=0)
#   - the CP image's docs come from a STAGED tree with docs/.distignore applied
#
# Getting the first one wrong produces a working image that is a different product
# (measured once — docs/70 §70.14.2). Getting the second wrong ships internal documents.
# So the instance runs `deploy/compose/release.sh` and `deploy/aws/ecs/release-ecr.sh`,
# which are the same paths a real release takes; this harness only supplies a box with
# docker on it and a credential.
#
# ⚠️ This pushes to a LIVE deployment's registry. It does not change what anything runs
# — that is the ImageTag parameter, updated separately and deliberately.
#
# ⚠️ It is NOT a release. The tag is whatever you pass; released tags are cut by
# publish-dist.yml from a tagged commit and must never be overwritten from here.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

TYPE="m7i.2xlarge"
TAG=""
REF=""
BUDGET_SEC=3600
RUN_TAG="af-relpush-$$-$(date +%s)"
GIT_REPO="https://github.com/k-k1/agent-fleet"

while [ $# -gt 0 ]; do
  case "$1" in
    --type) TYPE="${2:?}"; shift ;;
    --tag)  TAG="${2:?}"; shift ;;
    --ref)  REF="${2:?}"; shift ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$TAG" ] && [ -n "$REF" ] || { echo "--ref and --tag are required" >&2; exit 2; }

say() { printf '==> %s\n' "$*" >&2; }
ACCOUNT=$("${AWS[@]}" sts get-caller-identity --query Account --output text)
ROLE="af-relpush-$$"
say "account=$ACCOUNT tag=$TAG ref=$REF role=$ROLE"
"${AWS[@]}" ecr describe-repositories --repository-names af-control-plane af-workspace >/dev/null

sweep() {
  local ids
  ids=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-relpush-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  if [ -n "$ids" ]; then
    say "terminating: $ids"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null || true
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 wait instance-terminated --instance-ids $ids 2>/dev/null || true
  fi
  "${AWS[@]}" iam remove-role-from-instance-profile --instance-profile-name "$ROLE" --role-name "$ROLE" >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-instance-profile --instance-profile-name "$ROLE" >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-role-policy --role-name "$ROLE" --policy-name push >/dev/null 2>&1 || true
  "${AWS[@]}" iam delete-role --role-name "$ROLE" >/dev/null 2>&1 || true
  local left role_left
  left=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-relpush-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  role_left=$("${AWS[@]}" iam get-role --role-name "$ROLE" --query 'Role.RoleName' --output text 2>/dev/null || echo "none")
  say "residual: instances=${left:-none} role=$role_left"
}
trap sweep EXIT INT TERM

"${AWS[@]}" iam create-role --role-name "$ROLE" \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
  --tags Key=af-relpush-run,Value="$RUN_TAG" >/dev/null
"${AWS[@]}" iam put-role-policy --role-name "$ROLE" --policy-name push --policy-document "$(cat <<JSON
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":["ecr:GetAuthorizationToken","ecr:DescribeRepositories"],"Resource":"*"},
 {"Effect":"Allow","Resource":[
    "arn:aws:ecr:$REGION:$ACCOUNT:repository/af-control-plane",
    "arn:aws:ecr:$REGION:$ACCOUNT:repository/af-workspace"],
  "Action":["ecr:BatchCheckLayerAvailability","ecr:CompleteLayerUpload","ecr:InitiateLayerUpload","ecr:PutImage","ecr:UploadLayerPart","ecr:BatchGetImage","ecr:GetDownloadUrlForLayer"]}]}
JSON
)"
"${AWS[@]}" iam create-instance-profile --instance-profile-name "$ROLE" >/dev/null
"${AWS[@]}" iam add-role-to-instance-profile --instance-profile-name "$ROLE" --role-name "$ROLE"
say "created role + instance profile $ROLE (scoped to the two ECR repos, push only)"

VPC=$("${AWS[@]}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
SUBNET=$("${AWS[@]}" ec2 describe-subnets --filters "Name=vpc-id,Values=$VPC" \
  --query 'Subnets[?MapPublicIpOnLaunch==`true`]|[0].SubnetId' --output text)
SG=$("${AWS[@]}" ec2 describe-security-groups --filters "Name=vpc-id,Values=$VPC" Name=group-name,Values=default \
  --query 'SecurityGroups[0].GroupId' --output text)
AMI=$("${AWS[@]}" ssm get-parameter \
  --name /aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id \
  --query Parameter.Value --output text)

read -r -d '' UD <<EOF || true
#!/bin/bash
exec 2>&1
export HOME=/root
say() { echo "AF-REL|\$1" > /dev/console; }
emit() { tr -d '|' | while IFS= read -r l; do say "\$1|\$l"; done; }

systemctl start docker 2>/dev/null || true
for i in \$(seq 30); do docker info >/dev/null 2>&1 && break; sleep 2; done
dnf install -y git tar >/dev/null 2>&1

# release-ecr.sh takes --profile, so give it one that resolves to the instance role.
# Nothing is handed to the box: credential_source reads IMDS at call time.
aws configure set profile.af.credential_source Ec2InstanceMetadata
aws configure set profile.af.region "${REGION}"

cd /root
git clone --depth 1 --branch "${REF}" "${GIT_REPO}" repo || { say "clone|FAIL"; say "DONE"; exit 0; }
cd repo
say "head|\$(git rev-parse --short HEAD)"

# The real release path: lean workspace + docs staged through .distignore.
s=\$SECONDS
if VERSION="${TAG}" bash deploy/compose/release.sh >/tmp/b.log 2>&1; then
  say "build|\$((SECONDS-s))"
else
  say "build|FAIL"; tail -30 /tmp/b.log | emit build-err; say "DONE"; exit 0
fi

s=\$SECONDS
if VERSION="${TAG}" bash deploy/aws/ecs/release-ecr.sh --profile af --region "${REGION}" >/tmp/p.log 2>&1; then
  say "push|\$((SECONDS-s))"
else
  say "push|FAIL"; tail -25 /tmp/p.log | emit push-err
fi
say "DONE"
EOF

ID=""
for i in $(seq 12); do
  if ID=$("${AWS[@]}" ec2 run-instances \
      --image-id "$AMI" --instance-type "$TYPE" --subnet-id "$SUBNET" \
      --security-group-ids "$SG" --associate-public-ip-address \
      --iam-instance-profile "Name=$ROLE" \
      --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2' \
      --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=120,VolumeType=gp3,DeleteOnTermination=true}' \
      --user-data "$UD" \
      --tag-specifications "ResourceType=instance,Tags=[{Key=af-relpush-run,Value=$RUN_TAG},{Key=Name,Value=af-release-images}]" \
      --query 'Instances[0].InstanceId' --output text 2>/tmp/run-err.$$); then
    break
  fi
  # ⚠️ Show the error, do not swallow it. The retry exists for ONE cause (IAM is
  # eventually consistent, so a just-created instance profile is briefly invalid), and
  # hiding stderr turns every other cause — a vCPU quota, an unavailable instance type,
  # a bad subnet — into the same useless "never succeeded" after a minute of waiting.
  say "run-instances failed ($i/12): $(tr -d '\n' < /tmp/run-err.$$ | tail -c 300)"
  sleep 5
done
rm -f /tmp/run-err.$$
[ -n "$ID" ] || { echo "run-instances never succeeded — see the errors above" >&2; exit 1; }
say "launched $ID"

deadline=$((SECONDS + BUDGET_SEC))
while [ "$SECONDS" -lt "$deadline" ]; do
  sleep 45
  out=$("${AWS[@]}" ec2 get-console-output --instance-id "$ID" --latest --query Output --output text 2>/dev/null || true)
  printf '%s\n' "$out" | grep -o 'AF-REL|[^[:cntrl:]]*' | sed 's/^AF-REL|//' > /tmp/rel-push.txt || true
  grep -q '^DONE' /tmp/rel-push.txt 2>/dev/null && break
  say "working… ($(wc -l < /tmp/rel-push.txt) lines, ${SECONDS}s/${BUDGET_SEC}s)"
done

echo
echo "=== CP + workspace images -> ECR at :$TAG (ref=$REF) ==="
cat /tmp/rel-push.txt 2>/dev/null || echo "(no output)"

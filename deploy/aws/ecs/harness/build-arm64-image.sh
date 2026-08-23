#!/usr/bin/env bash
# Build the workspace image for arm64, natively, and prove the agent CLIs run on it
# (docs/70 §70.9 / §70.13 — the real P1 blocker).
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/build-arm64-image.sh [--type m8g.2xlarge] [--ref <branch>]
#
# Launches ONE Graviton instance, has it build `workspace/` with BAKE_AGENT_CLIS=1 and
# then run the L1 image smoke (deploy/local/e2e-smoke.sh) against what it built, prints
# the result, and terminates the instance. Nothing else is created.
#
# ## Why an EC2 instance and not CI
#
# Because the question is not "does the Dockerfile parse" — it is **"do nine third-party
# agent CLIs actually execute on arm64"**, and three of them have a documented reason to
# doubt it: agy aborts on hosts that do not offer RDRAND (decisions/0008) and Graviton
# has no such instruction at all; cursor-agent's arm64 distribution was verified by
# INSPECTION only, never executed (docs/40 §10, forum #148408); kiro needs the musl
# variant because its gnu build wants glibc 2.39 and the image is Debian 12 (docs/43).
# QEMU would answer a different question, and a CI run would publish something.
#
# So: BAKE_AGENT_CLIS=1 (the fully-baked variant), which pulls every CLI's arm64 asset
# and checks its pinned sha256 AT BUILD TIME, and then e2e-smoke.sh, which runs each one
# and compares `--version` against the Dockerfile's pin. Build validates the bytes;
# smoke validates that they execute.
#
# ## What it deliberately does NOT do
#
# It does not push anything. Publishing needs a registry credential, and a credential
# passed through user-data is readable from IMDS by anything on the box — including the
# build it is running. Publishing is the CI path (publish-dist.yml, workspace_arm64).
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

# 8 vCPU: the build is dominated by apt/dpkg and by unpacking ~1.3 GB of CLIs, both of
# which parallelise. The box lives for under an hour, and the difference between
# large and 2xlarge here is a few cents against 20 minutes of waiting.
TYPE="m8g.2xlarge"
REF="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --abbrev-ref HEAD 2>/dev/null || echo develop)"
REPO="https://github.com/k-k1/agent-fleet"
BUDGET_SEC=3600
RUN_TAG="af-armbuild-$$-$(date +%s)"

while [ $# -gt 0 ]; do
  case "$1" in
    --type) TYPE="${2:?--type needs a value}"; shift ;;
    --ref)  REF="${2:?--ref needs a value}"; shift ;;
    --repo) REPO="${2:?--repo needs a value}"; shift ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }

# Armed before anything exists, and by tag — see bench-instance-classes.sh for why.
sweep() {
  local ids
  ids=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-armbuild-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  if [ -n "$ids" ]; then
    say "terminating: $ids"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null || true
  fi
  local left
  left=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-armbuild-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  say "residual instances for $RUN_TAG: ${left:-none}"
}
trap sweep EXIT INT TERM

VPC=$("${AWS[@]}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
[ "$VPC" != "None" ] || { echo "no default VPC in $REGION" >&2; exit 2; }
SUBNET=$("${AWS[@]}" ec2 describe-subnets --filters "Name=vpc-id,Values=$VPC" \
  --query 'Subnets[?MapPublicIpOnLaunch==`true`]|[0].SubnetId' --output text)
SG=$("${AWS[@]}" ec2 describe-security-groups --filters "Name=vpc-id,Values=$VPC" Name=group-name,Values=default \
  --query 'SecurityGroups[0].GroupId' --output text)
# The ECS-optimized AMI ships docker already running, which is the whole reason this
# harness needs no provisioning step.
AMI=$("${AWS[@]}" ssm get-parameter \
  --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id \
  --query Parameter.Value --output text)
say "type=$TYPE ami=$AMI subnet=$SUBNET ref=$REF"

read -r -d '' UD <<EOF || true
#!/bin/bash
exec 2>&1
export HOME=/root
say() { echo "AF-ARM|\$1" > /dev/console; }
emit() { tr -d '|' | while IFS= read -r l; do say "\$1|\$l"; done; }

say "uname|\$(uname -m)"
say "docker|\$(docker --version 2>&1 | head -1 | tr -d '|')"
systemctl start docker 2>/dev/null || true

dnf install -y git >/dev/null 2>&1
cd /root
git clone --depth 1 --branch "${REF}" "${REPO}" repo || { say "clone|FAIL"; say "DONE"; exit 0; }
cd repo

# BAKE_AGENT_CLIS=1: every CLI's arm64 asset is fetched and its pinned sha256 verified
# here. A mismatch or a missing arm64 asset fails the BUILD, which is the earliest and
# clearest place for it to fail.
s=\$SECONDS
if docker build --build-arg BAKE_AGENT_CLIS=1 -t agent-fleet/workspace:arm64 workspace >/tmp/build.log 2>&1; then
  say "build|\$((SECONDS-s))"
else
  say "build|FAIL"
  tail -40 /tmp/build.log | emit build-err
  say "DONE"; exit 0
fi
say "arch|\$(docker image inspect agent-fleet/workspace:arm64 --format '{{.Architecture}}/{{.Os}}')"
say "size_mb|\$(( \$(docker image inspect agent-fleet/workspace:arm64 --format '{{.Size}}') / 1048576 ))"

# The L1 smoke RUNS each CLI and compares --version against the Dockerfile pin. On
# arm64 this is the first time any of them has been executed on real hardware.
if WS_IMAGE=agent-fleet/workspace:arm64 bash deploy/local/e2e-smoke.sh agent-fleet/workspace:arm64 >/tmp/smoke.log 2>&1; then
  say "smoke|PASS"
else
  say "smoke|FAIL"
fi
grep -E '^(ok|NG)' /tmp/smoke.log | emit smoke
say "DONE"
EOF

ID=$("${AWS[@]}" ec2 run-instances \
  --image-id "$AMI" --instance-type "$TYPE" --subnet-id "$SUBNET" \
  --security-group-ids "$SG" --associate-public-ip-address \
  --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2' \
  --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=120,VolumeType=gp3,DeleteOnTermination=true}' \
  --user-data "$UD" \
  --tag-specifications "ResourceType=instance,Tags=[{Key=af-armbuild-run,Value=$RUN_TAG},{Key=Name,Value=af-arm64-image-build}]" \
  --query 'Instances[0].InstanceId' --output text)
say "launched $ID"

deadline=$((SECONDS + BUDGET_SEC))
while [ "$SECONDS" -lt "$deadline" ]; do
  sleep 45
  out=$("${AWS[@]}" ec2 get-console-output --instance-id "$ID" --latest --query Output --output text 2>/dev/null || true)
  printf '%s\n' "$out" | grep -o 'AF-ARM|[^[:cntrl:]]*' | sed 's/^AF-ARM|//' > /tmp/arm64-build.txt || true
  if grep -q '^DONE' /tmp/arm64-build.txt 2>/dev/null; then break; fi
  say "building… ($(wc -l < /tmp/arm64-build.txt) lines, ${SECONDS}s/${BUDGET_SEC}s)"
done

echo
echo "=== docs/70 §70.9 / §70.13 — arm64 workspace image, built natively on $TYPE ==="
cat /tmp/arm64-build.txt 2>/dev/null || echo "(no output)"

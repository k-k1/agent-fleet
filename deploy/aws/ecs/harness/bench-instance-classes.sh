#!/usr/bin/env bash
# Slot instance classes — what does the cheaper box actually cost you? (docs/70 §70.3.1)
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/bench-instance-classes.sh [--types m7i.large,m6g.large] [--ref <git ref>]
#
# Launches ONE instance per type, has each of them build this repository, prints the
# times side by side, and terminates everything. Nothing else is created: no VPC, no
# stack, no IAM role, no key pair — it runs in the account's DEFAULT VPC and reads its
# results back off the serial console, so a failed run leaves at most an instance,
# which the final sweep terminates by tag.
#
# ## What this measures, and what it does not
#
# The question P0 asks is "how much slower is the cheap box for OUR work", and our work
# is `npm ci` / a Vite build / a Go build — single-thread-heavy, which is exactly where
# instance generations differ. So the workload is this repository itself, at a fixed
# ref, on the ECS-optimized AMI a real slot boots.
#
# ⚠️ It is NOT run inside the workspace image. It cannot be: the arm64 image does not
# exist yet (docs/70 P1), and comparing an x86_64 container against a bare arm64 host
# would measure the difference between those two things instead. Node and Go are
# installed from the same upstream tarballs at the versions the image pins, so the five
# boxes differ in CPU and nothing else. Once the arm64 image exists, re-run this inside
# it to fold in the container and libc differences.
#
# ⚠️ It measures COMPUTE. It deliberately does not measure the home volume: a slot's
# home is a gp3 EBS volume whose performance is a function of its size and type, not of
# the instance family, and mixing that in would attribute storage to the CPU.
#
# ## Cost
#
# 5 × 2 vCPU on-demand for ~30 minutes is under $1 (ap-northeast-1, measured prices in
# docs/70 §70.3). The root volumes are billed by the second and go with the instances.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

# The base rung of every ladder in docs/70 §70.3: 2 vCPU / 8 GiB in five families, so
# the only variable is the CPU.
TYPES="m7i.large,m6i.large,m8g.large,m7g.large,m6g.large"
# A BRANCH name, not a SHA: `git clone --depth 1 --branch` only accepts a branch or a
# tag, and a shallow clone is most of the difference between "the instance is measuring"
# and "the instance is still downloading history".
REF="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --abbrev-ref HEAD 2>/dev/null || echo develop)"
REPO="https://github.com/k-k1/agent-fleet"
GO_VERSION=1.26.7
NODE_MAJOR=22
BUDGET_SEC=2700
# One tag identifies everything this run made, so the sweep at the end never has to
# guess and never touches anything else in the account.
RUN_TAG="af-bench-$$-$(date +%s)"

while [ $# -gt 0 ]; do
  case "$1" in
    --types) TYPES="${2:?--types needs a value}"; shift ;;
    --ref)   REF="${2:?--ref needs a value}"; shift ;;
    --repo)  REPO="${2:?--repo needs a value}"; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }

# --- the sweep, armed before anything exists ------------------------------------
#
# ⚠️ Armed FIRST and by TAG, not by a list of ids collected as we go. A run that dies
# between RunInstances and the id being recorded would otherwise leave a box billing
# forever, and that is precisely the window a benchmark script spends most of its time
# in (docs/64's live harness learned this the expensive way).
sweep() {
  local ids
  ids=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-bench-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  if [ -n "$ids" ]; then
    say "terminating: $ids"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null || true
  fi
  # Say what is left rather than assuming. "residual 0" is a measurement, not a hope.
  local left
  left=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-bench-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  say "residual instances for $RUN_TAG: ${left:-none}"
}
trap sweep EXIT INT TERM

# --- where to run ----------------------------------------------------------------
VPC=$("${AWS[@]}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
[ "$VPC" != "None" ] || { echo "no default VPC in $REGION; this harness does not build one" >&2; exit 2; }
SUBNET=$("${AWS[@]}" ec2 describe-subnets --filters "Name=vpc-id,Values=$VPC" \
  --query 'Subnets[?MapPublicIpOnLaunch==`true`]|[0].SubnetId' --output text)
[ "$SUBNET" != "None" ] || { echo "no public subnet in the default VPC" >&2; exit 2; }
SG=$("${AWS[@]}" ec2 describe-security-groups --filters "Name=vpc-id,Values=$VPC" Name=group-name,Values=default \
  --query 'SecurityGroups[0].GroupId' --output text)
say "vpc=$VPC subnet=$SUBNET sg=$SG ref=$REF"

ami_for() {
  local arch=$1 name=/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id
  [ "$arch" = arm64 ] && name=/aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id
  "${AWS[@]}" ssm get-parameter --name "$name" --query Parameter.Value --output text
}
AMI_X86=$(ami_for x86_64)
AMI_ARM=$(ami_for arm64)
say "ami x86_64=$AMI_X86 arm64=$AMI_ARM"

# --- the workload ------------------------------------------------------------------
#
# Written to the SERIAL CONSOLE (`> /dev/console`) rather than fetched over SSH or SSM:
# both of those would need a key pair or an instance profile, i.e. two more things to
# create and clean up, for a one-line result per step.
userdata() {
  local goarch=$1
  cat <<EOF
#!/bin/bash
exec 2>&1
say() { echo "AF-BENCH|\$1" > /dev/console; }
t() { local k=\$1; shift; local s=\$SECONDS; if "\$@" >/tmp/\$k.log 2>&1; then say "\$k|\$((SECONDS-s))"; else say "\$k|FAIL"; tail -5 /tmp/\$k.log > /dev/console; fi; }

say "cpu|\$(lscpu | sed -n 's/^Model name: *//p' | head -1 | tr -d '|')"
say "nproc|\$(nproc)"

dnf install -y git tar gzip gcc gcc-c++ make >/dev/null 2>&1

curl -fsSL --retry 5 "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz
tar -C /usr/local -xzf /tmp/go.tgz
export PATH=/usr/local/go/bin:\$PATH GOTOOLCHAIN=local

nvmdir=/usr/local/nvm; mkdir -p \$nvmdir
curl -fsSL --retry 5 https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | NVM_DIR=\$nvmdir bash >/dev/null 2>&1
export NVM_DIR=\$nvmdir; . \$nvmdir/nvm.sh
nvm install ${NODE_MAJOR} >/dev/null 2>&1
# Exported explicitly: every step below runs in its own \`bash -c\`, which inherits the
# environment but not nvm's shell function, so PATH is the only thing carrying node.
export PATH="\$(dirname "\$(nvm which ${NODE_MAJOR} 2>/dev/null || echo /usr/bin/node)"):\$PATH"
say "node|\$(node -v 2>/dev/null || echo MISSING)"
say "go|\$(go version 2>/dev/null | awk '{print \$3}' || echo MISSING)"

cd /root
t clone git clone --depth 1 --branch "${REF}" "${REPO}" repo
cd /root/repo || { say "clone|FAIL"; say "DONE"; exit 0; }

# 1. npm ci — I/O plus single-thread CPU, and the command a member runs most often.
t npm_ci bash -c 'cd console && npm ci --no-audit --no-fund'
# 2. the Console build — Vite/Rollup, the most single-thread-bound step we have.
#    NODE_OPTIONS matches what the Workspace guide tells members to use.
t npm_build bash -c 'cd console && NODE_OPTIONS=--max-old-space-size=3072 npm run build'
# 3. a cold Go build of the Control Plane — parallel CPU, no network after the modules
#    are fetched (which is why the fetch is timed separately and excluded).
t go_mod bash -c 'cd control-plane && go mod download'
t go_build bash -c 'cd control-plane && go build ./...'
# 4. the Go test suite: the closest thing to "a member's inner loop" that exists here.
#    -p 2 matches the guidance for a memory-constrained box.
t go_test bash -c 'cd control-plane && go test ./... -count=1 -p 2'

say "DONE"
EOF
}

# --- launch --------------------------------------------------------------------
declare -A INST
IFS=',' read -r -a TYPE_LIST <<< "$TYPES"
for ty in "${TYPE_LIST[@]}"; do
  arch=x86_64; ami=$AMI_X86; goarch=amd64
  case "$ty" in *g.*|*gd.*|*gn.*) arch=arm64; ami=$AMI_ARM; goarch=arm64 ;; esac
  id=$("${AWS[@]}" ec2 run-instances \
    --image-id "$ami" --instance-type "$ty" --subnet-id "$SUBNET" \
    --security-group-ids "$SG" --associate-public-ip-address \
    --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2' \
    --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=40,VolumeType=gp3,DeleteOnTermination=true}' \
    --user-data "$(userdata "$goarch")" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=af-bench-run,Value=$RUN_TAG},{Key=Name,Value=af-bench-$ty}]" \
    --query 'Instances[0].InstanceId' --output text)
  INST["$ty"]=$id
  say "launched $ty -> $id ($arch)"
done

# --- collect --------------------------------------------------------------------
declare -A RESULT
deadline=$((SECONDS + BUDGET_SEC))
pending=${#INST[@]}
while [ "$pending" -gt 0 ] && [ "$SECONDS" -lt "$deadline" ]; do
  sleep 30
  pending=0
  for ty in "${!INST[@]}"; do
    [ "${RESULT[$ty]:-}" = "done" ] && continue
    out=$("${AWS[@]}" ec2 get-console-output --instance-id "${INST[$ty]}" --latest \
      --query Output --output text 2>/dev/null || true)
    lines=$(printf '%s\n' "$out" | grep -o 'AF-BENCH|[^[:cntrl:]]*' || true)
    if [ -n "$lines" ]; then
      printf '%s\n' "$lines" | sed "s#^AF-BENCH|#$ty|#" > "/tmp/bench-$ty.txt"
      if printf '%s\n' "$lines" | grep -q 'AF-BENCH|DONE'; then
        RESULT[$ty]="done"
        say "$ty finished"
        continue
      fi
    fi
    pending=$((pending + 1))
  done
  say "waiting: $pending of ${#INST[@]} still running (${SECONDS}s/${BUDGET_SEC}s)"
done

echo
echo "=== docs/70 §70.3.1 — build times by instance family (seconds, lower is better) ==="
echo "ref=$REF region=$REGION"
for ty in "${TYPE_LIST[@]}"; do
  [ -f "/tmp/bench-$ty.txt" ] || { echo "$ty: no output"; continue; }
  echo "--- $ty"
  cat "/tmp/bench-$ty.txt"
done

#!/usr/bin/env bash
# Does agy actually run on Graviton? (docs/log/32 Track B / decisions/0008 / docs/log/70 §70.13)
#
#   AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
#     deploy/aws/ecs/harness/probe-agy-arm64.sh [--types m8g.large,m7g.large,m6g.large]
#
# ## The question
#
# agy is a Go **BoringCrypto (FIPS)** build. On x86 its FIPS RNG self-test requires the
# RDRAND instruction, and a host that does not expose it dies at launch with
# `CRNGT failed` → SIGABRT (decisions/0008, measured). `hostcaps.AgyStatus` therefore
# hides the kind — but only `if runtime.GOARCH == "amd64"`:
#
#	// RDRAND 要件は x86 の FIPS 乱数モジュール固有（0008）。arm64 等では課さない。
#
# **That "arm64 では課さない" is an assumption that has never been executed.** The L1
# image smoke deliberately does not run `agy --version` (it would SIGABRT on the build
# host), so building the arm64 image proves nothing about agy — it is the one CLI that
# came out of that build unverified (docs/log/70 §70.9.5).
#
# ## Why three generations and not one
#
# If BoringCrypto's arm64 FIPS RNG reaches for **RNDR** (ARMv8.5-RNG) the way the x86
# one reaches for RDRAND, the answer differs *within* Graviton:
#
#   Graviton2 (Neoverse-N1, ARMv8.2)  — no RNDR
#   Graviton3 (Neoverse-V1, ARMv8.4)  — no RNDR
#   Graviton4 (Neoverse-V2, ARMv9.0)  — has RNDR
#
# A single-instance probe on m8g would then report "agy works on arm64" and be wrong
# for exactly the class docs/log/70 §70.3.3 recommends for cost-sensitive members (m6g).
# So: one instance per generation, and `/proc/cpuinfo` Features recorded next to the
# result so the two can be correlated rather than guessed at.
#
# The probe runs inside `node:22-trixie-slim` — the workspace image's own base — so
# the libc under agy is the one production uses, not the AL2023 host's.
set -euo pipefail

REGION="${AWS_REGION:-ap-northeast-1}"
PROFILE_ARG=()
[ -n "${AWS_PROFILE:-}" ] && PROFILE_ARG=(--profile "$AWS_PROFILE")
AWS=(aws "${PROFILE_ARG[@]+"${PROFILE_ARG[@]}"}" --region "$REGION")

TYPES="m8g.large,m7g.large,m6g.large"
BUDGET_SEC=1200
RUN_TAG="af-agy-$$-$(date +%s)"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

while [ $# -gt 0 ]; do
  case "$1" in
    --types) TYPES="${2:?--types needs a value}"; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '==> %s\n' "$*" >&2; }

# The pins come from the Dockerfile, not from a copy here: a probe that tests a
# different build than the image bakes answers a question nobody asked.
arg_pin() { sed -n "s/^ARG $1=\(.*\)$/\1/p" "$ROOT/workspace/Dockerfile" | head -1; }
AGY_VERSION="$(arg_pin AGY_VERSION)"
AGY_BUILD="$(arg_pin AGY_RELEASE_BUILD)"
AGY_SHA="$(arg_pin AGY_SHA256_ARM64)"
[ -n "$AGY_VERSION" ] && [ -n "$AGY_BUILD" ] && [ -n "$AGY_SHA" ] || {
  echo "could not read AGY_* pins from workspace/Dockerfile" >&2; exit 2; }
say "agy pin: $AGY_VERSION-$AGY_BUILD sha256=${AGY_SHA:0:12}…"

sweep() {
  local ids
  ids=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-agy-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  if [ -n "$ids" ]; then
    say "terminating: $ids"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $ids >/dev/null || true
  fi
  local left
  left=$("${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-agy-run,Values=$RUN_TAG" \
              "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)
  say "residual instances for $RUN_TAG: ${left:-none}"
}
trap sweep EXIT INT TERM

VPC=$("${AWS[@]}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
SUBNET=$("${AWS[@]}" ec2 describe-subnets --filters "Name=vpc-id,Values=$VPC" \
  --query 'Subnets[?MapPublicIpOnLaunch==`true`]|[0].SubnetId' --output text)
SG=$("${AWS[@]}" ec2 describe-security-groups --filters "Name=vpc-id,Values=$VPC" Name=group-name,Values=default \
  --query 'SecurityGroups[0].GroupId' --output text)
AMI=$("${AWS[@]}" ssm get-parameter \
  --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id \
  --query Parameter.Value --output text)
say "ami=$AMI subnet=$SUBNET"

read -r -d '' UD <<EOF || true
#!/bin/bash
exec 2>&1
export HOME=/root
say() { echo "AF-AGY|\$1" > /dev/console; }
emit() { tr -d '|' | while IFS= read -r l; do say "\$1|\$l"; done; }

say "cpu|\$(lscpu | sed -n 's/^Model name: *//p;s/^BIOS Model name: *//p' | head -1 | tr -d '|')"
# ARM reports capabilities as "Features", not x86's "flags". \`rng\` is the kernel's
# name for ARMv8.5-RNG (the RNDR instruction) — the arm64 analogue of rdrand.
# ⚠️ タブ区切り。arm64 の /proc/cpuinfo は "Features\t: fp asimd …" なので、
# 空白だけを見る正規表現は無言で空を返す（初回はそれで features が空になった）。
say "features|\$(sed -n 's/^Features[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo | head -1 | tr -d '|')"
say "has_rng|\$(grep -qw rng /proc/cpuinfo && echo yes || echo no)"

systemctl start docker 2>/dev/null || true
for i in \$(seq 30); do docker info >/dev/null 2>&1 && break; sleep 2; done

# node:22-trixie-slim is the workspace image's own base, so agy sees production's
# libc rather than the AL2023 host's.
docker run --rm node:22-trixie-slim bash -c '
  set -e
  apt-get update >/dev/null 2>&1 && apt-get install -y --no-install-recommends curl ca-certificates >/dev/null 2>&1
  cd /tmp
  curl -fsSL --retry 5 "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_VERSION}-${AGY_BUILD}/linux-arm/cli_linux_arm64.tar.gz" -o agy.tgz
  echo "${AGY_SHA}  agy.tgz" | sha256sum -c - >/dev/null && echo "SHA-OK"
  tar -xzf agy.tgz antigravity
  install -m 0755 antigravity /usr/local/bin/agy
  # ⚠️ set -e OFF from here, and the status captured BEFORE any pipe. The whole point
  # of this probe is the failing case: with set -e a SIGABRT would abort the script
  # and take the diagnostic with it, and `agy --version | head` would report head\x27s
  # status (0) instead of agy\x27s (134 = SIGABRT).
  set +e
  echo "--- agy --version"
  o=\$(agy --version 2>&1); rc=\$?
  printf "%s\\n" "\$o" | head -5
  echo "VERSION-RC=\$rc"
  echo "--- agy --help"
  o2=\$(agy --help 2>&1); rc2=\$?
  printf "%s\\n" "\$o2" | head -3
  echo "HELP-RC=\$rc2"
' > /tmp/agy.log 2>&1
rc=\$?
say "docker_rc|\$rc"
head -40 /tmp/agy.log | emit out
say "DONE"
EOF

declare -A INST
IFS=',' read -r -a TYPE_LIST <<< "$TYPES"
for ty in "${TYPE_LIST[@]}"; do
  id=$("${AWS[@]}" ec2 run-instances \
    --image-id "$AMI" --instance-type "$ty" --subnet-id "$SUBNET" \
    --security-group-ids "$SG" --associate-public-ip-address \
    --metadata-options 'HttpTokens=required,HttpPutResponseHopLimit=2' \
    --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=40,VolumeType=gp3,DeleteOnTermination=true}' \
    --user-data "$UD" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=af-agy-run,Value=$RUN_TAG},{Key=Name,Value=af-agy-$ty}]" \
    --query 'Instances[0].InstanceId' --output text)
  INST["$ty"]=$id
  say "launched $ty -> $id"
done

declare -A FIN
deadline=$((SECONDS + BUDGET_SEC))
pending=${#INST[@]}
while [ "$pending" -gt 0 ] && [ "$SECONDS" -lt "$deadline" ]; do
  sleep 30
  pending=0
  for ty in "${!INST[@]}"; do
    [ "${FIN[$ty]:-}" = "y" ] && continue
    out=$("${AWS[@]}" ec2 get-console-output --instance-id "${INST[$ty]}" --latest --query Output --output text 2>/dev/null || true)
    lines=$(printf '%s\n' "$out" | grep -o 'AF-AGY|[^[:cntrl:]]*' || true)
    if [ -n "$lines" ]; then
      printf '%s\n' "$lines" | sed 's/^AF-AGY|//' > "/tmp/agy-$ty.txt"
      if printf '%s\n' "$lines" | grep -q 'AF-AGY|DONE'; then FIN[$ty]=y; say "$ty done"; continue; fi
    fi
    pending=$((pending + 1))
  done
  say "waiting: $pending of ${#INST[@]} (${SECONDS}s/${BUDGET_SEC}s)"
done

echo
echo "=== agy on Graviton — docs/log/32 Track B / decisions/0008 / docs/log/70 §70.13 ==="
echo "agy $AGY_VERSION-$AGY_BUILD (linux-arm/cli_linux_arm64.tar.gz), inside node:22-trixie-slim"
for ty in "${TYPE_LIST[@]}"; do
  echo "--- $ty"
  cat "/tmp/agy-$ty.txt" 2>/dev/null || echo "(no output)"
done

#!/usr/bin/env bash
# Stub test for deploy/aws/ecs/harness/teardown.sh's Cloud Map sweep (#3555) and its Elastic IP
# release (#3796).
#
# A fake `aws` stands in for an account that holds the harness's namespace AND another
# deployment's stack-owned Cloud Map services (50-tts / 60-engines). Like the real API, an
# unfiltered `servicediscovery list-services` returns every service in the account and region;
# only `--filters Name=NAMESPACE_ID,...` narrows it. The sweep must delete the harness's own
# services and nothing else.
#
# The same fake account holds the harness VPC's NAT gateway (with its EIP) and a foreign,
# unassociated EIP. An unfiltered `ec2 describe-addresses` returns both; the teardown must
# release the NAT's EIP (a leak costs money) and leave the foreign one alone.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
LOG="$WORK/calls.log"; : > "$LOG"

cat > "$WORK/bin/aws" <<'STUB'
#!/usr/bin/env bash
args="$*"
echo "$args" >> "$STUB_LOG"
case "$args" in
  *"servicediscovery list-namespaces"*) echo "ns-harness" ;;
  *"servicediscovery list-services"*)
    case "$args" in
      *"NAMESPACE_ID,Values=ns-harness"*) echo "srv-harness-1 srv-harness-2" ;;
      *"--filters"*) ;;                                    # another namespace: nothing
      *) echo "srv-harness-1 srv-harness-2 srv-stack-tts srv-stack-llm srv-stack-image" ;;
    esac ;;
  *"servicediscovery delete-service"*) ;;
  *"ec2 describe-vpcs"*"isDefault"*) echo "${STUB_DEFAULT_VPC-vpc-default}" ;;
  *"ec2 describe-vpcs"*) echo "${STUB_TAGGED_VPC-vpc-harness}" ;;
  *"ec2 describe-security-groups"*"group-name,Values=af-ec2c-ws"*"vpc-id,Values="*)
    # Real API: vpc-id narrows the account-wide name match. A foreign VPC holds a same-named group.
    case "$args" in
      *"vpc-harness"*) echo "sg-ws-harness" ;;
      *) ;;
    esac
    [ -z "${STUB_SG_DUP:-}" ] || echo "sg-ws-dup" ;;
  *"ec2 describe-security-groups"*"group-name,Values=af-ec2c-ws"*) echo "sg-ws-foreign" ;;  # unscoped: foreign first
  *"ec2 describe-security-groups"*"group-name,Values=af-ec2c-efs"*"vpc-id,Values="*) ;;
  *"ec2 describe-security-groups"*"group-name,Values=af-ec2c-efs"*) echo "sg-efs-foreign" ;;
  *"ec2 describe-nat-gateways"*"Values=vpc-harness"*"NatGatewayAddresses"*)
    [ -n "${STUB_NO_NAT:-}" ] || echo "eipalloc-harness" ;;
  *"ec2 describe-nat-gateways"*"Values=vpc-harness"*)
    [ -n "${STUB_NO_NAT:-}" ] || echo "nat-harness" ;;
  *"ec2 describe-nat-gateways"*) ;;                       # any other VPC: nothing
  *"ec2 describe-addresses"*"tag:Name,Values=af-ec2c"*) echo "eipalloc-orphan" ;;
  *"ec2 describe-addresses"*) echo "eipalloc-harness eipalloc-foreign" ;;
esac
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' > "$WORK/bin/sleep"
chmod +x "$WORK/bin/aws" "$WORK/bin/sleep"

STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed"; exit 1; }

for sv in srv-harness-1 srv-harness-2; do
  grep -qF "servicediscovery delete-service --id $sv" "$LOG" \
    || { cat "$LOG"; echo "NG: the harness's own service $sv was not deleted"; exit 1; }
done
if grep -E "servicediscovery delete-service --id srv-stack-" "$LOG"; then
  echo "NG: a stack-owned Cloud Map service was deleted"; exit 1
fi
echo "OK: harness teardown deletes only its own namespace's Cloud Map services"

grep -qF "ec2 release-address --allocation-id eipalloc-harness" "$LOG" \
  || { cat "$LOG"; echo "NG: the harness's NAT EIP was not released"; exit 1; }
if grep -F "release-address --allocation-id eipalloc-foreign" "$LOG"; then
  echo "NG: a foreign Elastic IP was released"; exit 1
fi
grep -qF "release-address --allocation-id eipalloc-orphan" "$LOG" \
  || { echo "NG: the tagged EIP with no NAT gateway was not released"; exit 1; }
echo "OK: harness teardown releases only the Elastic IPs its NAT gateways held (plus tagged orphans)"

# No NAT gateway in the VPC: nothing but the tagged orphan may be released.
: > "$LOG"
STUB_NO_NAT=1 STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed without a NAT gateway"; exit 1; }
if grep -E "release-address --allocation-id eipalloc-(foreign|harness)" "$LOG"; then
  echo "NG: an EIP was released with no NAT gateway to own it"; exit 1
fi
echo "OK: no NAT gateway, no untagged EIP released"

# Security groups (#3807): only a group inside the harness VPCs may be deleted.
: > "$LOG"
STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed (security groups)"; exit 1; }
grep -qF "delete-security-group --group-id sg-ws-harness" "$LOG" \
  || { cat "$LOG"; echo "NG: the harness VPC's security group was not deleted"; exit 1; }
if grep -E "delete-security-group --group-id sg-.*-foreign" "$LOG"; then
  echo "NG: a security group outside the harness VPC was deleted"; exit 1
fi
echo "OK: security groups are looked up inside the harness VPC only"

# No VPC resolvable: skip, never fall back to the unscoped lookup.
: > "$LOG"
STUB_TAGGED_VPC= STUB_DEFAULT_VPC= STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed with no VPC"; exit 1; }
if grep -F "delete-security-group" "$LOG"; then echo "NG: a security group was deleted with no VPC resolved"; exit 1; fi
grep -qF "skipping security group deletion" "$WORK/out" || { echo "NG: no skip message"; exit 1; }
echo "OK: no VPC resolved, no security group deleted"

# Ambiguous match inside the harness VPCs: delete nothing.
: > "$LOG"
STUB_SG_DUP=1 STUB_LOG="$LOG" PATH="$WORK/bin:$PATH" bash "$ROOT/deploy/aws/ecs/harness/teardown.sh" > "$WORK/out" 2>&1 \
  || { cat "$WORK/out"; echo "NG: teardown.sh failed (ambiguous sg)"; exit 1; }
if grep -F "delete-security-group" "$LOG"; then echo "NG: an ambiguous security group was deleted"; exit 1; fi
echo "OK: ambiguous security group match deletes nothing"

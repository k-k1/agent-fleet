#!/usr/bin/env bash
# Stub end-to-end test for release-ecr.sh (docs/log/35 §35.7.3 gate g).
# Uses no real AWS/docker: PATH-prepended fake aws / fake docker pin down the call
# set and the assembled ECR URIs. Runs both in CI (release-gate.yml ecs-gate) and
# locally (no docker needed).
# Note: `get-login-password | docker login` runs both sides of the pipe concurrently,
# so log order is nondeterministic — order asserts are limited to meaningful
# dependencies (repo check -> push, load -> tag).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$ROOT/deploy/aws/ecs/release-ecr.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# fake aws: records each call and returns a canned response per subcommand.
# STUB_REPOS_MISSING=1 makes describe-repositories fail (for the negative path).
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
case "$*" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  *"ecr describe-repositories"*)
    if [ "${STUB_REPOS_MISSING:-0}" = 1 ]; then
      echo "RepositoryNotFoundException" >&2; exit 254
    fi ;;
  *"ecr get-login-password"*) echo "stub-token" ;;
esac
FAKE
# fake docker: record only. login consumes stdin (--password-stdin).
cat > "$STUB/docker" <<'FAKE'
#!/usr/bin/env bash
echo "docker $*" >> "$STUB_LOG"
if [ "$1" = login ]; then cat >/dev/null; fi
FAKE
chmod +x "$STUB/aws" "$STUB/docker"
export PATH="$STUB:$PATH" STUB_LOG="$LOG"

fail() { echo "NG: $1"; echo "--- full log ---"; cat "$LOG"; exit 1; }
# Exact match on the call set, order-insensitive (absorbs the pipe-concurrency nondeterminism)
expect_set() { # expect_set <expected-file>
  diff <(LC_ALL=C sort "$1") <(LC_ALL=C sort "$LOG") || fail "call set mismatch"
}
lineno() { grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1; }
expect_order() { # expect_order <earlier> <later>
  local a b; a="$(lineno "$1")"; b="$(lineno "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || fail "order: '$1' must precede '$2'"
}

echo "== case 1: normal push (account via sts) =="
: > "$LOG"
VERSION=1.2.3 "$SCRIPT" --profile p1 --region ap-northeast-1 > "$WORK/out1.txt"
H="123456789012.dkr.ecr.ap-northeast-1.amazonaws.com"
cat > "$WORK/want1" <<EOF
aws --profile p1 --region ap-northeast-1 sts get-caller-identity --query Account --output text
aws --profile p1 --region ap-northeast-1 ecr describe-repositories --repository-names af-control-plane af-workspace
aws --profile p1 --region ap-northeast-1 ecr get-login-password
docker login --username AWS --password-stdin $H
docker tag agent-fleet/control-plane:1.2.3 $H/af-control-plane:1.2.3
docker push $H/af-control-plane:1.2.3
docker tag agent-fleet/workspace:1.2.3 $H/af-workspace:1.2.3
docker push $H/af-workspace:1.2.3
EOF
expect_set "$WORK/want1"
expect_order "ecr describe-repositories" "docker tag agent-fleet/control-plane:1.2.3"
expect_order "docker login" "docker push $H/af-control-plane:1.2.3"
expect_order "docker tag agent-fleet/control-plane:1.2.3" "docker push $H/af-control-plane:1.2.3"
grep -q "ImageTag=1.2.3" "$WORK/out1.txt" || fail "next-step hint missing"
echo "ok"

echo "== case 2: --account + --images-tar (air-gap B; no sts) =="
: > "$LOG"
touch "$WORK/images.tar.gz"
VERSION=2.0.0 "$SCRIPT" --profile p2 --region us-east-1 --account 000011112222 \
  --images-tar "$WORK/images.tar.gz" > /dev/null
H2="000011112222.dkr.ecr.us-east-1.amazonaws.com"
cat > "$WORK/want2" <<EOF
aws --profile p2 --region us-east-1 ecr describe-repositories --repository-names af-control-plane af-workspace
aws --profile p2 --region us-east-1 ecr get-login-password
docker login --username AWS --password-stdin $H2
docker load -i $WORK/images.tar.gz
docker tag agent-fleet/control-plane:2.0.0 $H2/af-control-plane:2.0.0
docker push $H2/af-control-plane:2.0.0
docker tag agent-fleet/workspace:2.0.0 $H2/af-workspace:2.0.0
docker push $H2/af-workspace:2.0.0
EOF
expect_set "$WORK/want2"
expect_order "docker load" "docker tag agent-fleet/control-plane:2.0.0"
echo "ok"

echo "== case 3: repos missing -> fail with 20-platform guidance, no push =="
: > "$LOG"
rc=0
VERSION=1.2.3 STUB_REPOS_MISSING=1 "$SCRIPT" --profile p1 --region ap-northeast-1 \
  > /dev/null 2> "$WORK/err3.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "20-platform" "$WORK/err3.txt" || { cat "$WORK/err3.txt"; fail "guidance missing"; }
if grep -q "docker push" "$LOG"; then fail "pushed despite missing repos"; fi
echo "ok"

echo "== release-ecr stub test OK =="

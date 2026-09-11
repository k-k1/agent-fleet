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
# STUB_ENGINES=1 gives the deployment an engines stack, which is what puts the third image —
# af-engine-tools — in the ledger. Without it the step must not happen at all (cases 1 and 2
# assert the exact call set, so they are that control).
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
case "$*" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  *"describe-stacks"*EnginesSsmParam*)
    [ "${STUB_ENGINES:-0}" = 1 ] && echo "/af-ws/engines" || echo "" ;;
  *"list-exports"*EnginesSsmParam*) echo "t-engines-EnginesSsmParam" ;;
  *"describe-stacks"*EngineToolsImageTag*) echo "${STUB_ET_WANT-2026-09-11}" ;;
  *"ecr describe-images"*af-engine-tools*) [ "${STUB_ET_IN_ECR:-0}" = 1 ] || exit 254 ;;
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
# fake crane: the engine tools image never passes through the local docker (it is baked to GHCR
# by engine-tools-image.yml), so it travels registry-to-registry. `manifest` is how "is it in
# GHCR" is asked, and it has to be able to say no.
cat > "$STUB/crane" <<'FAKE'
#!/usr/bin/env bash
echo "crane $*" >> "$STUB_LOG"
case "$1" in
  manifest) [ "${STUB_ET_IN_GHCR:-1}" = 1 ] || exit 1; echo '{"manifests":[]}' ;;
  auth) cat >/dev/null ;;
esac
FAKE
chmod +x "$STUB/aws" "$STUB/docker" "$STUB/crane"
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
docker image inspect agent-fleet/control-plane:1.2.3
docker tag agent-fleet/control-plane:1.2.3 $H/af-control-plane:1.2.3
docker push $H/af-control-plane:1.2.3
docker image inspect agent-fleet/workspace:1.2.3
docker tag agent-fleet/workspace:1.2.3 $H/af-workspace:1.2.3
docker push $H/af-workspace:1.2.3
aws --profile p1 --region ap-northeast-1 cloudformation describe-stacks --stack-name af-ecs-ingress --query Stacks[0].Parameters[?ParameterKey=='EnginesSsmParam'].ParameterValue --output text
EOF
expect_set "$WORK/want1"
expect_order "ecr describe-repositories" "docker tag agent-fleet/control-plane:1.2.3"
# The inspect is a pre-flight guard: a multi-arch build never loads locally, and without
# it `docker tag` fails with "No such image", which reads like a failed build.
expect_order "docker image inspect agent-fleet/control-plane:1.2.3" "docker tag agent-fleet/control-plane:1.2.3"
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
docker image inspect agent-fleet/control-plane:2.0.0
docker tag agent-fleet/control-plane:2.0.0 $H2/af-control-plane:2.0.0
docker push $H2/af-control-plane:2.0.0
docker image inspect agent-fleet/workspace:2.0.0
docker tag agent-fleet/workspace:2.0.0 $H2/af-workspace:2.0.0
docker push $H2/af-workspace:2.0.0
aws --profile p2 --region us-east-1 cloudformation describe-stacks --stack-name af-ecs-ingress --query Stacks[0].Parameters[?ParameterKey=='EnginesSsmParam'].ParameterValue --output text
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

echo "== case 4: a deployment with engines also gets the engine tools image =="
#
# The third image in the ledger, and the only one that does not travel through the local docker:
# `af-engine-tools` is baked to GHCR by engine-tools-image.yml, never built here and never part
# of a distribution, so it is carried registry-to-registry. It is in THIS script because this is
# the step of a release that fills ECR — 60-engines names a tag that nothing else on the release
# route would put there, and a task definition pointing at an image nobody copied does not fail
# until the engine tries to start.
: > "$LOG"
VERSION=1.2.3 STUB_ENGINES=1 "$SCRIPT" --profile p1 --region ap-northeast-1 --stack t-ingress \
  > "$WORK/out4.txt"
cat > "$WORK/want4" <<EOF
aws --profile p1 --region ap-northeast-1 sts get-caller-identity --query Account --output text
aws --profile p1 --region ap-northeast-1 ecr describe-repositories --repository-names af-control-plane af-workspace
aws --profile p1 --region ap-northeast-1 ecr get-login-password
docker login --username AWS --password-stdin $H
docker image inspect agent-fleet/control-plane:1.2.3
docker tag agent-fleet/control-plane:1.2.3 $H/af-control-plane:1.2.3
docker push $H/af-control-plane:1.2.3
docker image inspect agent-fleet/workspace:1.2.3
docker tag agent-fleet/workspace:1.2.3 $H/af-workspace:1.2.3
docker push $H/af-workspace:1.2.3
aws --profile p1 --region ap-northeast-1 cloudformation describe-stacks --stack-name t-ingress --query Stacks[0].Parameters[?ParameterKey=='EnginesSsmParam'].ParameterValue --output text
aws --profile p1 --region ap-northeast-1 cloudformation list-exports --query Exports[?Value=='/af-ws/engines'&&ends_with(Name,'-EnginesSsmParam')].Name --output text
aws --profile p1 --region ap-northeast-1 cloudformation describe-stacks --stack-name t-engines --query Stacks[0].Parameters[?ParameterKey=='EngineToolsImageTag'].ParameterValue --output text
aws --profile p1 --region ap-northeast-1 ecr describe-repositories --repository-names af-engine-tools
aws --profile p1 --region ap-northeast-1 ecr describe-images --repository-name af-engine-tools --image-ids imageTag=2026-09-11
crane manifest ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11
aws --profile p1 --region ap-northeast-1 ecr get-login-password
crane auth login $H -u AWS --password-stdin
crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11 $H/af-engine-tools:2026-09-11
EOF
expect_set "$WORK/want4"
# The repository is 20-platform's, and it is checked before anything is copied into it — the
# same rule the two release images follow (never create one out of band).
expect_order "ecr describe-repositories --repository-names af-engine-tools" \
             "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11"
# And ECR is asked first: a tag that is already there is not pulled again on every release.
expect_order "ecr describe-images --repository-name af-engine-tools" \
             "crane manifest ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11"
echo "ok"

echo "== case 5: the tag is in neither ECR nor GHCR -> stop, and say which workflow makes it =="
# Nothing on the release route may bake an image, so this is the one thing this step cannot fix
# by itself. It has to say so in a line, rather than copy nothing and report success.
: > "$LOG"
rc=0
VERSION=1.2.3 STUB_ENGINES=1 STUB_ET_IN_GHCR=0 "$SCRIPT" --profile p1 --region ap-northeast-1 \
  --stack t-ingress > /dev/null 2> "$WORK/err5.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "engine-tools-image.yml" "$WORK/err5.txt" || { cat "$WORK/err5.txt"; fail "guidance missing"; }
if grep -q "crane copy" "$LOG"; then fail "copied something that is in neither registry"; fi
echo "ok"

echo "== case 6: a tag already in ECR is not copied again =="
: > "$LOG"
VERSION=1.2.3 STUB_ENGINES=1 STUB_ET_IN_ECR=1 "$SCRIPT" --profile p1 --region ap-northeast-1 \
  --stack t-ingress > "$WORK/out6.txt"
if grep -q "crane copy" "$LOG"; then fail "re-copied an image that is already in ECR"; fi
if grep -q "crane manifest" "$LOG"; then fail "asked GHCR about an image that is already in ECR"; fi
grep -q "already in ECR" "$WORK/out6.txt" || fail "it did not say why it copied nothing"
echo "ok"

echo "== release-ecr stub test OK =="

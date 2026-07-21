#!/usr/bin/env bash
# release-ecr.sh の stub 実走テスト（docs/35 §35.7.3 ゲート g）。
# 実 AWS/docker を使わず、PATH 前置の fake aws / fake docker で呼び出し列と
# ECR URI の組み立てを固定する。CI（release-gate.yml ecs-gate）とローカルの両方で
# 実行可（docker 不要）。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$ROOT/deploy/aws/ecs/release-ecr.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# fake aws: 呼び出しを記録し、サブコマンドごとに決め打ちの応答を返す。
# STUB_REPOS_MISSING=1 で describe-repositories を失敗させる（否定経路用）。
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
# fake docker: 記録のみ。login は stdin（--password-stdin）を消費する。
cat > "$STUB/docker" <<'FAKE'
#!/usr/bin/env bash
echo "docker $*" >> "$STUB_LOG"
if [ "$1" = login ]; then cat >/dev/null; fi
FAKE
chmod +x "$STUB/aws" "$STUB/docker"
export PATH="$STUB:$PATH" STUB_LOG="$LOG"

expect() { # expect <lineno> <exact line>
  local got; got="$(sed -n "$1p" "$LOG")"
  if [ "$got" != "$2" ]; then
    echo "NG: call $1"; echo "  want: $2"; echo "  got : $got"
    echo "--- full log ---"; cat "$LOG"; exit 1
  fi
}

echo "== case 1: normal push (account via sts) =="
: > "$LOG"
VERSION=1.2.3 "$SCRIPT" --profile p1 --region ap-northeast-1 > "$WORK/out1.txt"
H="123456789012.dkr.ecr.ap-northeast-1.amazonaws.com"
expect 1 "aws --profile p1 --region ap-northeast-1 sts get-caller-identity --query Account --output text"
expect 2 "aws --profile p1 --region ap-northeast-1 ecr describe-repositories --repository-names af-control-plane af-workspace"
expect 3 "aws --profile p1 --region ap-northeast-1 ecr get-login-password"
expect 4 "docker login --username AWS --password-stdin $H"
expect 5 "docker tag agent-fleet/control-plane:1.2.3 $H/af-control-plane:1.2.3"
expect 6 "docker push $H/af-control-plane:1.2.3"
expect 7 "docker tag agent-fleet/workspace:1.2.3 $H/af-workspace:1.2.3"
expect 8 "docker push $H/af-workspace:1.2.3"
test "$(wc -l < "$LOG")" = 8 || { echo "NG: extra calls"; cat "$LOG"; exit 1; }
grep -q "ImageTag=1.2.3" "$WORK/out1.txt" || { echo "NG: next-step hint missing"; exit 1; }
echo "ok"

echo "== case 2: --account + --images-tar (air-gap B; no sts) =="
: > "$LOG"
touch "$WORK/images.tar.gz"
VERSION=2.0.0 "$SCRIPT" --profile p2 --region us-east-1 --account 000011112222 \
  --images-tar "$WORK/images.tar.gz" > /dev/null
H2="000011112222.dkr.ecr.us-east-1.amazonaws.com"
expect 1 "aws --profile p2 --region us-east-1 ecr describe-repositories --repository-names af-control-plane af-workspace"
expect 2 "aws --profile p2 --region us-east-1 ecr get-login-password"
expect 3 "docker login --username AWS --password-stdin $H2"
expect 4 "docker load -i $WORK/images.tar.gz"
expect 5 "docker tag agent-fleet/control-plane:2.0.0 $H2/af-control-plane:2.0.0"
test "$(wc -l < "$LOG")" = 8 || { echo "NG: call count"; cat "$LOG"; exit 1; }
echo "ok"

echo "== case 3: repos missing -> fail with 20-platform guidance, no push =="
: > "$LOG"
rc=0
VERSION=1.2.3 STUB_REPOS_MISSING=1 "$SCRIPT" --profile p1 --region ap-northeast-1 \
  > /dev/null 2> "$WORK/err3.txt" || rc=$?
test "$rc" = 1 || { echo "NG: expected exit 1, got $rc"; exit 1; }
grep -q "20-platform" "$WORK/err3.txt" || { echo "NG: guidance missing"; cat "$WORK/err3.txt"; exit 1; }
grep -q "docker push" "$LOG" && { echo "NG: pushed despite missing repos"; exit 1; }
echo "ok"

echo "== release-ecr stub test OK =="

#!/usr/bin/env bash
# Agent Fleet — ECR release push（docs/35 §35.7.3-1）。
#
#   VERSION=0.2.0 deploy/aws/ecs/release-ecr.sh --profile af-sandbox --region ap-northeast-1 \
#     [--account 123456789012] [--images-tar dist/agent-fleet-images-0.2.0.tar.gz] \
#     [--registry agent-fleet]
#
# runbook（README §ECR push）の手打ちコマンド列が正で、このスクリプトはその写し。
# やること: sts で account 解決 → ECR repo の存在確認（作らない — repo の正は
# 20-platform CFN。out-of-band create は後続の CFN deploy を AlreadyExists で壊す）
# → docker login → （--images-tar 指定時は air-gap B を docker load）→
# ローカルイメージ agent-fleet/{control-plane,workspace}:$VERSION を
# af-{control-plane,workspace}:$VERSION として tag/push。
#
# 前提: 20-platform デプロイ済み（ECR repo）・push するイメージが手元 docker に
# あるか --images-tar で渡すこと。イメージのビルド自体は deploy/release/build.sh。
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: VERSION=<v> release-ecr.sh --profile <p> --region <r> [--account <acct>]
                                  [--images-tar <B.tar.gz>] [--registry <local-prefix>]
  --profile     aws cli プロファイル（必須）
  --region      ECR リージョン（必須）
  --account     アカウント ID（省略時 sts get-caller-identity で解決）
  --images-tar  air-gap images tar（B）を docker load してから push
  --registry    ローカルイメージ名の前置（既定 agent-fleet — release.sh の既定と対）
EOF
}

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.2.0)}"
PROFILE=""; REGION=""; ACCOUNT=""; IMAGES_TAR=""; LOCAL_REGISTRY="agent-fleet"
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --account)    ACCOUNT="${2:?--account needs a value}"; shift ;;
    --images-tar) IMAGES_TAR="${2:?--images-tar needs a path}"; shift ;;
    --registry)   LOCAL_REGISTRY="${2:?--registry needs a value}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then usage; exit 2; fi

AWS=(aws --profile "$PROFILE" --region "$REGION")

if [ -z "$ACCOUNT" ]; then
  ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
fi
ECR_HOST="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"

# repo は 20-platform CFN が所有する（ここでは作らない — §35.7.3-1）。
if ! "${AWS[@]}" ecr describe-repositories \
    --repository-names af-control-plane af-workspace >/dev/null; then
  echo "ERROR: ECR repos af-control-plane / af-workspace not found in $ACCOUNT/$REGION." >&2
  echo "       Deploy cfn/20-platform.yaml first (it owns the repositories)." >&2
  exit 1
fi

echo "==> docker login $ECR_HOST"
"${AWS[@]}" ecr get-login-password | \
  docker login --username AWS --password-stdin "$ECR_HOST"

if [ -n "$IMAGES_TAR" ]; then
  echo "==> docker load < $IMAGES_TAR (air-gap B)"
  docker load -i "$IMAGES_TAR"
fi

for pair in "control-plane=af-control-plane" "workspace=af-workspace"; do
  local_name="$LOCAL_REGISTRY/${pair%%=*}:$VERSION"
  ecr_uri="$ECR_HOST/${pair#*=}:$VERSION"
  echo "==> push $local_name -> $ecr_uri"
  docker tag "$local_name" "$ecr_uri"
  docker push "$ecr_uri"
done

cat <<EOF
==> done: pushed :$VERSION to $ECR_HOST/af-{control-plane,workspace}
next: aws cloudformation deploy --stack-name af-ecs-ingress \\
        --template-file cfn/30-ingress.yaml \\
        --parameter-overrides ImageTag=$VERSION \\
        --profile $PROFILE --region $REGION
      (他パラメータは previous value 維持。CP は rolling replace、Workspace は
       次回 Start から新イメージ — 稼働中ワークスペースは巻き込まない)
EOF

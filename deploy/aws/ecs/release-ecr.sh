#!/usr/bin/env bash
# Agent Fleet — ECR release push (docs/35 §35.7.3-1).
#
#   VERSION=0.2.0 deploy/aws/ecs/release-ecr.sh --profile af-sandbox --region ap-northeast-1 \
#     [--account 123456789012] [--images-tar dist/agent-fleet-images-0.2.0.tar.gz] \
#     [--registry agent-fleet]
#
# The hand-typed command sequence in the runbook (README §ECR push) is the source of
# truth; this script is its transcript. Steps: resolve the account via sts -> check
# that the ECR repos exist (never create them — the repos are owned by the
# 20-platform CFN stack; an out-of-band create breaks the later CFN deploy with
# AlreadyExists) -> docker login -> (docker load the air-gap B tar when --images-tar
# is given) -> tag/push the local images agent-fleet/{control-plane,workspace}:$VERSION
# as af-{control-plane,workspace}:$VERSION.
#
# Prerequisites: 20-platform deployed (ECR repos), and the images to push either
# present in the local docker or supplied via --images-tar. Building the images
# themselves is deploy/release/build.sh.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: VERSION=<v> release-ecr.sh --profile <p> --region <r> [--account <acct>]
                                  [--images-tar <B.tar.gz>] [--registry <local-prefix>]
  --profile     aws cli profile (required)
  --region      ECR region (required)
  --account     account ID (resolved via sts get-caller-identity when omitted)
  --images-tar  docker load the air-gap images tar (B) before pushing
  --registry    local image name prefix (default agent-fleet — pairs with release.sh's default)
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

# The repos are owned by the 20-platform CFN stack (never created here — §35.7.3-1).
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
  # ⚠️ This path goes through the LOCAL docker, so it is single-architecture by
  # construction: a multi-arch build (WS_PLATFORMS / CP_PLATFORMS — docs/70 §70.9,
  # docs/72) produces a manifest LIST, which buildx pushes straight to a registry and
  # never loads locally. The image is then simply absent here, and `docker tag`'s
  # "No such image" reads like a failed build rather than the wrong tool. Say so.
  if ! docker image inspect "$local_name" >/dev/null 2>&1; then
    echo "ERROR: $local_name is not in the local docker." >&2
    echo "       If it was built multi-arch, it never will be — a manifest list cannot be" >&2
    echo "       loaded locally. Copy the index registry-to-registry instead:" >&2
    echo "         crane copy ghcr.io/k-k1/agent-fleet/${pair%%=*}:$VERSION $ecr_uri" >&2
    echo "       Otherwise build it first (deploy/release/build.sh) or pass --images-tar." >&2
    exit 1
  fi
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
      (keep the previous values for the other parameters. The CP does a rolling
       replace; workspaces pick up the new image from their next Start — running
       workspaces are not disrupted)
EOF

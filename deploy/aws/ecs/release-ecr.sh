#!/usr/bin/env bash
# Agent Fleet — ECR release push (docs/log/35 §35.7.3-1).
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
# as af-{control-plane,workspace}:$VERSION -> carry the engine tools image over from
# GHCR when this deployment runs engines.
#
# ⚠️ That last one is in the ledger below but not in the loop, because it does not
# travel the same way: `af-engine-tools` is baked by engine-tools-image.yml straight to
# GHCR (never into a local docker, never into a distribution), so it is a registry-to-
# registry `crane copy` rather than a tag-and-push. It is here for the reason the other
# two are: this is the step of a release that fills ECR, and 60-engines names a tag that
# nothing else on the release route would put there.
#
# Prerequisites: 20-platform deployed (ECR repos), and the images to push either
# present in the local docker or supplied via --images-tar. Building the images
# themselves is deploy/release/build.sh.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: VERSION=<v> release-ecr.sh --profile <p> --region <r> [--account <acct>]
                                  [--images-tar <B.tar.gz>] [--registry <local-prefix>]
                                  [--stack <af-ecs-ingress>]
  --profile     aws cli profile (required)
  --region      ECR region (required)
  --account     account ID (resolved via sts get-caller-identity when omitted)
  --images-tar  docker load the air-gap images tar (B) before pushing
  --registry    local image name prefix (default agent-fleet — pairs with release.sh's default)
  --stack       ingress stack name (default af-ecs-ingress) — only used to find the
                engines stack, i.e. which engine tools tag this deployment asks for
EOF
}

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.2.0)}"
PROFILE=""; REGION=""; ACCOUNT=""; IMAGES_TAR=""; LOCAL_REGISTRY="agent-fleet"
STACK="af-ecs-ingress"
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --account)    ACCOUNT="${2:?--account needs a value}"; shift ;;
    --images-tar) IMAGES_TAR="${2:?--images-tar needs a path}"; shift ;;
    --registry)   LOCAL_REGISTRY="${2:?--registry needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then usage; exit 2; fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AWS=(aws --profile "$PROFILE" --region "$REGION")
# For the engine tools step alone (af_engines_stack / af_engine_tools_ensure). af_env_init is
# not called: like update.sh, this script works against a live deployment with no local capture.
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"
AF_STACK_INGRESS="$STACK"

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
  # construction: a multi-arch build (WS_PLATFORMS / CP_PLATFORMS — docs/log/70 §70.9,
  # docs/log/72) produces a manifest LIST, which buildx pushes straight to a registry and
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

# --- the engine tools image, when this deployment runs engines (ADR 0072) ---
#
# 🔴 ECR REPOSITORY (20-platform) -> IMAGE (here) -> STACK (60-engines). The tag comes from the
# engines stack itself, or — on the release that INTRODUCES the parameter — from the default the
# new template brings. Skipped entirely by a deployment with no engines stack, which is most of
# them.
ENGINES_STACK="$(af_engines_stack || true)"
if [ -n "$ENGINES_STACK" ]; then
  ET_TAG="$(af_stack_param "$ENGINES_STACK" EngineToolsImageTag)"
  [ -n "$ET_TAG" ] || ET_TAG="$(af_cfn_param_default "$HERE/cfn/60-engines.yaml" EngineToolsImageTag)"
  echo "==> engine tools image for $ENGINES_STACK: af-engine-tools:$ET_TAG"
  if ! "${AWS[@]}" ecr describe-repositories --repository-names af-engine-tools >/dev/null 2>&1; then
    echo "ERROR: ECR repo af-engine-tools not found in $ACCOUNT/$REGION." >&2
    echo "       Deploy cfn/20-platform.yaml first (it owns the repository; update.sh does it" >&2
    echo "       before calling this script)." >&2
    exit 1
  fi
  et_rc=0
  af_engine_tools_ensure "$ECR_HOST" "$ET_TAG" || et_rc=$?
  case "$et_rc" in
    0) ;;
    1)
      echo "ERROR: engine-tools:$ET_TAG is in neither ECR nor GHCR — run engine-tools-image.yml with tag=$ET_TAG first." >&2
      exit 1 ;;
    *)
      echo "ERROR: af-engine-tools:$ET_TAG is not in ECR and there is no crane to carry it over." >&2
      echo "       crane copy $AF_GHCR_DEFAULT/engine-tools:$ET_TAG $ECR_HOST/af-engine-tools:$ET_TAG" >&2
      exit 1 ;;
  esac
fi

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

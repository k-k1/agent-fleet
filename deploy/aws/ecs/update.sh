#!/usr/bin/env bash
# Agent Fleet — update an ECS / ecs-ec2 deployment in one command (README §Upgrade, executable).
#
#   VERSION=0.2.0 deploy/aws/ecs/update.sh --profile af-sandbox --region ap-northeast-1
#   VERSION=0.2.0 deploy/aws/ecs/update.sh --profile p --region r --push   # from the ECR push on
#
# The counterpart of `docker compose pull && docker compose up -d` for a compose deployment
# (deploy/compose/README.md §Upgrade) and of `deploy/local/run-dev.sh` for dev. Running the
# runbook by hand from README §Upgrade is fine; this exists because three of its steps are
# pitfalls unless you already know them:
#
#  1. A tag can be deployed while it is not in ECR. CloudFormation only takes a string, so an
#     update that forgot to push (or pushed to another region) "succeeds", and the CP task then
#     fails to come up with CannotPullContainerError. Here both images are checked to exist
#     before the deploy, so it fails earlier.
#  2. With a mutable tag (the :dev sandbox style), CFN reports "no changes" and nothing happens.
#     An update re-pushed to the same tag has zero template diff, so the CP keeps running the
#     old task. The empty changeset is detected and turned into an ECS force-new-deployment.
#  3. Workspaces do not update themselves. The adapter rebuilds the task definition on every
#     Start, so a running workspace stays on the old image until it is stopped and started
#     again. Who is in that state is listed at the end (in the Console the same fact reaches
#     each user as the "restart required" badge on the WS bar —
#     control-plane/runtime_ecs_stale.go).
#  4. A release can need a NEW ECR repository and a new image in it, and CloudFormation will
#     happily deploy the stack that names them first. 0.19.0 moved the engine's fetch and
#     ingest steps into af-engine-tools; a deployment taking that release without the
#     repository (20-platform) and the image (a crane copy from GHCR) gets a 60-engines whose
#     fetch containers cannot pull, while the stack reports a steady state. The three steps go
#     in ONE order, and step 1 below is where it is written down.
#
# Never stop a workspace on the operator's behalf. Stopping kills sessions, and choosing the
# moment belongs to the user (the ADR reason the update toast does not push for a restart).
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: VERSION=<v> update.sh --profile <p> --region <r>
                             [--stack <af-ecs-ingress>] [--template <cfn/30-ingress.yaml>]
                             [--push] [--images-tar <B.tar.gz>] [--registry <prefix>]
                             [--force] [--dry-run]
  --profile     aws cli profile (required)
  --region      region of the deployment (required)
  --stack       ingress stack name (default af-ecs-ingress) — the one with ImageTag
  --template    template file (default <script dir>/cfn/30-ingress.yaml)
  --push        run release-ecr.sh first (build must already have produced the images)
  --images-tar  passed through to release-ecr.sh (air-gap B tar)
  --registry    passed through to release-ecr.sh (local image name prefix)
  --force       force a new CP deployment even when CloudFormation reports a change
  --dry-run     print what would happen; touch nothing
EOF
}

VERSION="${VERSION:?set VERSION=<tag> (the ImageTag both images are pushed under)}"
PROFILE=""; REGION=""; STACK="af-ecs-ingress"; TEMPLATE=""
PUSH=0; IMAGES_TAR=""; LOCAL_REGISTRY=""; FORCE=0; DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --template)   TEMPLATE="${2:?--template needs a path}"; shift ;;
    --push)       PUSH=1 ;;
    --images-tar) IMAGES_TAR="${2:?--images-tar needs a path}"; shift ;;
    --registry)   LOCAL_REGISTRY="${2:?--registry needs a value}"; shift ;;
    --force)      FORCE=1 ;;
    --dry-run)    DRY=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then usage; exit 2; fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ -n "$TEMPLATE" ] || TEMPLATE="$HERE/cfn/30-ingress.yaml"
AWS=(aws --profile "$PROFILE" --region "$REGION")
# shellcheck source=deploy/aws/ecs/env.sh
# Sourced only for af_cfn_deploy (which goes through S3 past 51,200 bytes) and af_stack_output.
# af_env_init is deliberately NOT called: this script updates a LIVE deployment, and its value
# is that it works with no local capture. The bucket comes from 20-platform's outputs.
. "$HERE/env.sh"
AF_STACK_INGRESS="$STACK"
AF_STACK_PLATFORM="$(af_stack_param "$STACK" PlatformStackName)"
: "${AF_STACK_PLATFORM:=af-ecs-platform}"
TTS_STACK="$(af_tts_stack || true)"
ENGINES_STACK="$(af_engines_stack || true)"
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
ECR_HOST="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"

run() {  # dry-run support. Reads always run; only writes go through here.
  if [ "$DRY" = 1 ]; then echo "DRY: $*"; return 0; fi
  "$@"
}
# env.sh's helpers write through af_run, which reads this. Same rule as `run` above: a dry run
# prints the command and touches nothing.
export AF_DRY="$DRY"

# --- 1) the order, in one place (pitfall 4) ---------------------------------
# 🔴 ECR REPOSITORY (20-platform) -> IMAGE (crane copy) -> STACK (60-engines). Each step needs
# the one before it and none of them fails at the time it is skipped:
#
#   - no repository and the crane copy has nowhere to go;
#   - no image and 60-engines deploys perfectly, after which both roles' fetch containers and
#     the ingest task sit in CannotPullContainerError. The service reports a steady state.
#
# Measured 2026-09-11 on the hardware lane, the first time 0.19.0 was put on a deployment:
# neither GHCR nor ECR had the image and 20-platform (which owns the repository) had not been
# updated, so it took three hand-run steps that existed in no script. The plan is printed
# before anything happens so that the order is readable on a --dry-run.
ET_TAG=""
if [ -n "$ENGINES_STACK" ]; then
  # What 60-engines will ask for: the live value, or — on the update that INTRODUCES the
  # parameter — the default the new template brings with it.
  ET_TAG="$(af_stack_param "$ENGINES_STACK" EngineToolsImageTag)"
  [ -n "$ET_TAG" ] || ET_TAG="$(af_cfn_param_default "$HERE/cfn/60-engines.yaml" EngineToolsImageTag)"
fi
echo "==> plan for $STACK (ImageTag=$VERSION):"
echo "      1. $AF_STACK_PLATFORM (20-platform — it owns the ECR repositories)"
[ "$PUSH" = 1 ] && echo "      2. release-ecr.sh (push af-control-plane / af-workspace :$VERSION)"
[ -n "$TTS_STACK" ] && echo "      3. $TTS_STACK (50-tts)"
if [ -n "$ENGINES_STACK" ]; then
  echo "      4. af-engine-tools:${ET_TAG:-<unknown>} into ECR, then $ENGINES_STACK (60-engines)"
fi
echo "      5. $STACK (30-ingress, ImageTag=$VERSION)"

# --- 1a) 20-platform, through a change set that is shown first --------------
# It is deployed only on the round where the template or a parameter actually moved (an empty
# change set is exactly that fact), and never blindly: 20-platform owns the ECR repositories,
# the cluster and the task roles, so an unreviewed update here is the one that can take a
# deployment apart. A change set that REPLACES anything is handed back rather than executed —
# replacing a repository throws its images away, and replacing a role breaks every task that
# names it.
if ! af_stack_exists "$AF_STACK_PLATFORM"; then
  echo "==> no $AF_STACK_PLATFORM stack — skipping 20-platform (standup.sh is what creates it)"
elif [ "$DRY" = 1 ]; then
  echo "DRY: aws cloudformation deploy --stack-name $AF_STACK_PLATFORM --template-file $HERE/cfn/20-platform.yaml --no-execute-changeset"
  echo "     (then describe-change-set, printed here, and execute-change-set only when nothing is replaced)"
else
  echo "==> 20-platform: building a change set for $AF_STACK_PLATFORM"
  set +e
  cs_out="$(af_cfn_deploy "$AF_STACK_PLATFORM" "$HERE/cfn/20-platform.yaml" \
    --capabilities CAPABILITY_NAMED_IAM --no-execute-changeset --no-fail-on-empty-changeset 2>&1)"
  cs_rc=$?
  set -e
  printf '%s\n' "$cs_out" | while IFS= read -r l; do echo "    $l"; done
  [ $cs_rc -eq 0 ] || exit $cs_rc
  if echo "$cs_out" | grep -qi "No changes to deploy"; then
    echo "    · nothing moved in 20-platform — not deployed"
  else
    # The CLI prints the review command; the ARN in it is the only handle to the change set it
    # just made. Not being able to read it stops the run: continuing would deploy 60-engines
    # against a repository that may not exist yet, which is the failure this section is for.
    cs_arn="$(printf '%s\n' "$cs_out" | sed -n 's/.*--change-set-name \([^ ]*\).*/\1/p' | tail -1)"
    if [ -z "$cs_arn" ]; then
      echo "ERROR: a change set was created for $AF_STACK_PLATFORM but its name could not be read" >&2
      echo "       from the output above. Review and execute it by hand (aws cloudformation" >&2
      echo "       list-change-sets --stack-name $AF_STACK_PLATFORM), then re-run this script." >&2
      exit 1
    fi
    cs_changes="$("${AWS[@]}" cloudformation describe-change-set --change-set-name "$cs_arn" \
      --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output text 2>/dev/null || true)"
    if [ -z "${cs_changes// /}" ]; then
      echo "ERROR: the change set $cs_arn could not be read — nothing was executed." >&2
      exit 1
    fi
    printf '%s\n' "$cs_changes" | while IFS=$'\t' read -r act lid repl; do
      echo "    · $act $lid (replacement: ${repl:-None})"
    done
    replaced="$(printf '%s\n' "$cs_changes" | awk '$NF == "True" { print $2 }' | tr '\n' ' ')"
    if [ -n "${replaced// /}" ]; then
      echo "ERROR: this 20-platform change set REPLACES: $replaced" >&2
      echo "       A release does not do that unreviewed — replacing an ECR repository throws its" >&2
      echo "       images away, and replacing a role breaks every task that names it. Nothing was" >&2
      echo "       executed; the change set is left for you to read:" >&2
      echo "         aws cloudformation describe-change-set --change-set-name $cs_arn --profile $PROFILE --region $REGION" >&2
      exit 1
    fi
    echo "==> executing the 20-platform change set"
    "${AWS[@]}" cloudformation execute-change-set --change-set-name "$cs_arn" >/dev/null
    "${AWS[@]}" cloudformation wait stack-update-complete --stack-name "$AF_STACK_PLATFORM"
    echo "    · $AF_STACK_PLATFORM updated"
  fi
fi

# --- 1b) push (optional) ----------------------------------------------------
# After 20-platform on purpose: it owns the ECR repositories, and release-ecr.sh refuses to
# create one out of band (an out-of-band create breaks the later CFN deploy with AlreadyExists).
if [ "$PUSH" = 1 ]; then
  args=(--profile "$PROFILE" --region "$REGION" --stack "$STACK")
  [ -n "$IMAGES_TAR" ] && args+=(--images-tar "$IMAGES_TAR")
  [ -n "$LOCAL_REGISTRY" ] && args+=(--registry "$LOCAL_REGISTRY")
  echo "==> release-ecr.sh (VERSION=$VERSION)"
  run env VERSION="$VERSION" "$HERE/release-ecr.sh" "${args[@]}"
fi

# --- 1c) is the tag really in ECR (pitfall 1) -------------------------------
missing=""
for repo in af-control-plane af-workspace; do
  if ! "${AWS[@]}" ecr describe-images --repository-name "$repo" \
        --image-ids "imageTag=$VERSION" >/dev/null 2>&1; then
    missing="$missing $repo:$VERSION"
  fi
done
if [ -n "$missing" ]; then
  echo "ERROR: not in ECR:$missing" >&2
  echo "       push first (--push, or VERSION=$VERSION $HERE/release-ecr.sh --profile $PROFILE --region $REGION)." >&2
  echo "       Deploying a tag that is not there succeeds, and the CP task then fails to pull." >&2
  exit 1
fi
echo "==> ECR has af-control-plane:$VERSION and af-workspace:$VERSION"

# --- 1d) does the CP image's architecture match CpArch (sibling of pitfall 1) -----
# Pitfall 1 is "the tag is missing". This one is "the tag is there but that architecture is
# not", and the symptom is worse: not even a CannotPullContainerError — ECS simply cannot
# place the task and spins at desired=1 / running=0 (docs/log/72). CpArch=arm64 is only
# possible for a build where publish-dist ran with control_plane_arm64, which is off by
# default.
# Fail only when it can be proven. Passing when it could not be checked is deliberate: this is
# an update preflight, not a publishing gate. AF_CP_ARCH_CHECK=0 disables it entirely.
if [ "${AF_CP_ARCH_CHECK:-1}" = 1 ]; then
  # An older stack without the parameter at all is x86_64 (the default Fargate applies when it
  # is omitted).
  cp_arch="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Parameters[?ParameterKey=='CpArch'].ParameterValue" \
    --output text 2>/dev/null || true)"
  case "$cp_arch" in ""|None) cp_arch=x86_64 ;; esac
  want=amd64; [ "$cp_arch" = arm64 ] && want=arm64   # CFN's vocabulary -> OCI's
  manifest="$("${AWS[@]}" ecr batch-get-image --repository-name af-control-plane \
    --image-ids "imageTag=$VERSION" \
    --accepted-media-types \
      "application/vnd.docker.distribution.manifest.v2+json" \
      "application/vnd.oci.image.manifest.v1+json" \
      "application/vnd.docker.distribution.manifest.list.v2+json" \
      "application/vnd.oci.image.index.v1+json" \
    --query 'images[0].imageManifest' --output text 2>/dev/null || true)"
  case "$manifest" in
    *'"manifests"'*)   # A manifest list: the architectures inside are readable, so this is
                       # something we can assert.
      # Parsed this way to avoid needing jq (an operator does not necessarily have it). Only
      # the platform side's "architecture" appears, so splitting on commas is enough.
      archs="$(printf '%s' "$manifest" | tr ',' '\n' \
        | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' \
        | sort -u | tr '\n' ' ')"
      echo "==> af-control-plane:$VERSION is an index of: ${archs:-<none read>}"
      case " $archs " in
        *" $want "*) ;;
        *)
          echo "ERROR: CpArch=$cp_arch needs a '$want' entry, and af-control-plane:$VERSION has: ${archs:-<none>}" >&2
          echo "       Deploying this puts the service in desired=1 / running=0 with no pull error to read." >&2
          echo "       Publish with control_plane_arm64, or set CpArch back (docs/log/72)." >&2
          exit 1 ;;
      esac
      ;;
    *)                 # A single manifest: one architecture only, and its contents cannot be
                       # read here.
      if [ "$want" = arm64 ]; then
        echo "ERROR: CpArch=arm64 but af-control-plane:$VERSION is a SINGLE manifest — it can only" >&2
        echo "       serve one architecture, and this pipeline's single-arch builds are the build" >&2
        echo "       host's (amd64). Re-publish with control_plane_arm64 (docs/log/72)." >&2
        echo "       Check by hand: crane manifest <ecr>/af-control-plane:$VERSION" >&2
        echo "       Override with AF_CP_ARCH_CHECK=0 if you know better." >&2
        exit 1
      fi
      ;;
  esac
fi

# Which service in which cluster is the CP. Resolved from the resource the stack actually owns
# (the AWS::ECS::Service physical id = arn:…:service/<cluster>/<name>). Transcribing the naming
# convention (`af-${AWS::StackName}-cp`) instead breaks silently the moment the stack is
# renamed — the force and the wait would then hit a different service and "succeed" — so the
# convention is only the last-resort fallback.
CP_ARN="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$STACK" \
  --logical-resource-id Service --query 'StackResourceDetail.PhysicalResourceId' \
  --output text 2>/dev/null || true)"
CLUSTER=""; CP_SERVICE=""
case "$CP_ARN" in
  *:service/*/*)                       # arn:aws:ecs:<r>:<a>:service/<cluster>/<name>
    CP_SERVICE="${CP_ARN##*/}"
    rest="${CP_ARN%/*}"
    CLUSTER="${rest##*/}"
    ;;
esac
if [ -z "$CLUSTER" ]; then             # fallback: 20-platform's export + the naming convention
  PLATFORM_STACK="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Parameters[?ParameterKey=='PlatformStackName'].ParameterValue" --output text)"
  CLUSTER="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Name=='${PLATFORM_STACK}-ClusterName'].Value" --output text)"
  CP_SERVICE="af-${STACK}-cp"
fi
if [ -z "$CLUSTER" ] || [ "$CLUSTER" = "None" ] || [ -z "$CP_SERVICE" ]; then
  echo "ERROR: could not resolve the ECS cluster / CP service of stack $STACK" >&2
  exit 1
fi
echo "==> stack=$STACK cluster=$CLUSTER cp-service=$CP_SERVICE"

# --- 1e) the speech engine stack, when this deployment has one (ADR 0070) -------------
# Kept in step with the repo the same way 30-ingress is: a release ships template fixes,
# and a 50-tts left behind would drift silently (nothing reads its version).
#
# Safe to run against a live engine. The template omits DesiredCount, so CloudFormation
# leaves the count out of the update call — a running engine is not stopped by a stack
# update, only by a change to the task definition, which replaces the task as it should.
# Its image is pinned upstream and has nothing to do with VERSION, so no tag is overridden.
if [ -n "$TTS_STACK" ]; then
  echo "==> cloudformation deploy $TTS_STACK (50-tts, parameters unchanged)"
  if [ "$DRY" = 1 ]; then
    echo "DRY: aws cloudformation deploy --stack-name $TTS_STACK --template-file $HERE/cfn/50-tts.yaml"
  else
    af_cfn_deploy "$TTS_STACK" "$HERE/cfn/50-tts.yaml" --no-fail-on-empty-changeset
  fi
fi

# --- 1f) the inference-engine stack, when this deployment has one (ADR 0071) ----------
# Same reasoning and the same safety: DesiredCount is absent from the template, so a stack
# update never stops an engine somebody is mid-answer with. The llama.cpp image is pinned
# upstream and unrelated to VERSION, so no tag is overridden here either.
if [ -n "$ENGINES_STACK" ]; then
  # The image BEFORE the stack — step 2 of the order at step 1. The repository was created or
  # updated there; this is the only place on the release route that fills it, because
  # engine-tools-image.yml bakes to GHCR and `standup.sh` (which copies it in) is not part of
  # a release.
  echo "==> engine tools image for $ENGINES_STACK: af-engine-tools:$ET_TAG"
  if [ -z "$ET_TAG" ]; then
    echo "ERROR: could not work out which engine tools tag $ENGINES_STACK wants (neither the live" >&2
    echo "       stack nor cfn/60-engines.yaml's Default for EngineToolsImageTag could be read)." >&2
    exit 1
  fi
  et_rc=0
  af_engine_tools_ensure "$ECR_HOST" "$ET_TAG" || et_rc=$?
  case "$et_rc" in
    0) ;;
    1)
      echo "ERROR: engine-tools:$ET_TAG is in neither ECR nor GHCR — run engine-tools-image.yml with tag=$ET_TAG first." >&2
      echo "       (On a standard release GHCR has it: the workflow is dispatched when" >&2
      echo "       deploy/aws/ecs/engine-tools/ changes. Nothing here can bake an image.)" >&2
      echo "       Deploying $ENGINES_STACK now would leave both roles' fetch containers in" >&2
      echo "       CannotPullContainerError while the service reports a steady state." >&2
      exit 1 ;;
    *)
      echo "ERROR: af-engine-tools:$ET_TAG is not in ECR and there is no crane to carry it over." >&2
      echo "       Install crane, or copy it by hand and re-run:" >&2
      echo "         crane copy $AF_GHCR_DEFAULT/engine-tools:$ET_TAG $ECR_HOST/af-engine-tools:$ET_TAG" >&2
      exit 1 ;;
  esac

  echo "==> cloudformation deploy $ENGINES_STACK (60-engines, parameters unchanged)"
  # 🔴 ONE parameter cannot be left unchanged, and this is the only path that can repair it.
  # ADR 0072 phase P6 retired `<Role>ModelS3Key`, which until then ALSO decided whether the
  # role's SERVICE existed. Passing no parameters means `<Role>Enabled` keeps its previous
  # value, and for a deployment that switched a role on by naming a key that value is `""` —
  # so this update DELETES the engine service. Not by failing: as an ordinary, successful
  # stack update, with a GPU engine and a 527-586-second cold start behind it. Measured
  # 2026-09-11: the production deployment has BOTH roles in exactly that state.
  #
  # Read off the LIVE stack because this script deliberately works without a local capture
  # (see the note at the top). standup.sh does the same translation from `params/60-engines`.
  # A deployment that has already been through P6 has no `<Role>ModelS3Key` to read, so
  # nothing is passed and the deploy is byte for byte what it was.
  eng_params=()
  for eng_role in Llm Image; do
    if [ -z "$(af_stack_param "$ENGINES_STACK" "${eng_role}Enabled")" ] &&
       [ -n "$(af_stack_param "$ENGINES_STACK" "${eng_role}ModelS3Key")" ]; then
      echo "    · ${eng_role}Enabled=true (it was implied by ${eng_role}ModelS3Key, retired in ADR 0072 P6)"
      eng_params+=("${eng_role}Enabled=true")
    fi
  done
  eng_deploy=(--capabilities CAPABILITY_NAMED_IAM --no-fail-on-empty-changeset)
  eng_shown=""
  if [ "${#eng_params[@]}" -gt 0 ]; then
    eng_deploy+=(--parameter-overrides "${eng_params[@]}")
    eng_shown=" --parameter-overrides ${eng_params[*]}"
  fi
  if [ "$DRY" = 1 ]; then
    echo "DRY: aws cloudformation deploy --stack-name $ENGINES_STACK --template-file $HERE/cfn/60-engines.yaml$eng_shown"
  else
    af_cfn_deploy "$ENGINES_STACK" "$HERE/cfn/60-engines.yaml" "${eng_deploy[@]}"
  fi
fi

# --- 2) CFN deploy: override ImageTag only (everything else keeps its previous value) -------
# `cloudformation deploy` keeps parameters it was not given at UsePreviousValue, so never add
# another parameter here — adding one overwrites that "previous value".
echo "==> cloudformation deploy $STACK (ImageTag=$VERSION)"
deploy_out=""
if [ "$DRY" = 1 ]; then
  echo "DRY: aws cloudformation deploy --stack-name $STACK --template-file $TEMPLATE --parameter-overrides ImageTag=$VERSION"
else
  set +e
  # Always go through af_cfn_deploy (env.sh): it switches to S3 once a template passes 51,200
  # bytes. 30-ingress crossed that line once and every release deployment stopped dead.
  deploy_out="$(af_cfn_deploy "$STACK" "$TEMPLATE" \
    --parameter-overrides "ImageTag=$VERSION" \
    --no-fail-on-empty-changeset 2>&1)"
  rc=$?
  set -e
  echo "$deploy_out"
  [ $rc -eq 0 ] || exit $rc
fi

# --- 3) what a mutable tag's "no changes" would drop (pitfall 2) -------------
# An update re-pushed to the same tag has zero template diff, so CFN does nothing and succeeds.
# Detect that here and fall back to an ECS force-new-deployment (which re-pulls the new image).
if [ "$FORCE" = 1 ] || echo "$deploy_out" | grep -qi "No changes to deploy"; then
  echo "==> forcing a new CP deployment (mutable tag / --force): $CP_SERVICE"
  run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
    --force-new-deployment >/dev/null
fi

# --- 4) wait until the CP has been replaced ---------------------------------
if [ "$DRY" != 1 ]; then
  echo "==> waiting for $CP_SERVICE to stabilise (blue/green behind the ALB)"
  "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
  echo "==> CP is running the new task definition"
fi

# --- 5) list the running workspaces (pitfall 3) ------------------------------
# Nothing is stopped here; this only reports who is still running the old image.
# Leave list-services' paging to the CLI (--max-items truncates silently and then reads as
# "everyone was seen"). describe-services takes 10 at a time, so batch them.
arns="$("${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null || true)"
names=""
for arn in $arns; do
  name="${arn##*/}"
  case "$name" in af-ws-*) names="$names $name" ;; esac
done
running=""
# shellcheck disable=SC2086  # word splitting is the batching
set -- $names
while [ $# -gt 0 ]; do
  batch=""
  i=0
  while [ $# -gt 0 ] && [ $i -lt 10 ]; do batch="$batch $1"; shift; i=$((i + 1)); done
  # shellcheck disable=SC2086
  # shellcheck disable=SC2016  # backticks are JMESPath's literal syntax, not a subshell
  got="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services $batch \
    --query 'services[?desiredCount>`0`].serviceName' --output text 2>/dev/null || true)"
  running="$running $got"
done

# --- 6) the ecs-ec2 golden snapshot (the other thing an update quietly makes stale) ---------
# It seeds a new user's home. When the image moves, the af-image mismatch makes the CP fall to
# "do not use the golden, create an empty home" (ADR 0045 decision 9). Nothing breaks, but a
# new user's first start gets visibly slower and the only place it shows is the CP log — hence
# reporting it on every update.
#
# The re-bake is the CP's job by default (decision 9-1). It is still reported here because
# there are two conditions under which the automatic bake never starts:
# AF_ECS_EC2_GOLDEN_AUTOBAKE=0, and fewer than 2 free slots in the pool. Both simply do nothing,
# silently — so the message below says that, together with the fact that it always looks stale
# right after an update (it catches up within minutes).
WS_IMAGE="$ECR_HOST/af-workspace:$VERSION"
golden_stale="$("${AWS[@]}" ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:af-role,Values=golden" \
  --query "Snapshots[?Tags[?Key=='af-image'&&Value!='$WS_IMAGE']].[SnapshotId,Tags[?Key=='af-image']|[0].Value]" \
  --output text 2>/dev/null || true)"

URL="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='Url'].OutputValue" --output text 2>/dev/null || true)"
cat <<EOF

==> done: $STACK is on ImageTag=$VERSION${URL:+  ($URL)}

The Console (in each user's browser) shows "a new version is available" within a few minutes,
and a reload is all it takes (sessions are not interrupted).

Workspaces are NOT replaced automatically. The adapter rebuilds the task definition on every
Start, so anything running now keeps the old image until it is stopped and started again. The
affected users get the "restart required" badge on the WS bar in their Console (they press it
themselves — stopping kills their sessions).

Note: if nobody gets the badge, 20-platform is out of date. Without ecr:BatchGetImage on
   CpTaskRole the drift check falls to "unknown" and the badge just disappears, with no error.
   Step 1a above deploys it, so this only remains when that step was skipped or handed back.
EOF
if [ -n "${running// /}" ]; then
  echo "Workspaces running now (on the old image until stopped and started again):"
  for n in $running; do echo "  - $n"; done
else
  echo "No workspaces are running (the next Start picks up the new image)."
fi

if [ -n "${golden_stale// /}" ]; then
  cat <<EOF

ecs-ec2: the golden snapshot is stale (it seeds a new user's home). The CP does not use a
   golden that does not match and creates an empty home instead, so nothing breaks, but a new
   user's FIRST START gets slower and the only place it shows is the CP log (ADR 0045
   decision 9):
$(printf '%s\n' "$golden_stale" | while IFS= read -r l; do echo "     $l"; done)
   The image it should carry now: $WS_IMAGE

   Normally the CP re-bakes it by itself within a few minutes (decision 9-1). It does not start
   when AF_ECS_EC2_GOLDEN_AUTOBAKE=0, or when the pool has fewer than 2 free slots (in both
   cases the CP log gives the reason). To bake it by hand, use bake-golden.sh.
EOF
fi

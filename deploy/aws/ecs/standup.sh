#!/usr/bin/env bash
# Agent Fleet — stand a deployment up / rebuild it (the executable form of the order in
# README §Stack decomposition).
#
#   deploy/aws/ecs/standup.sh --profile <p> --region <r>          # plan and preflight (default)
#   deploy/aws/ecs/standup.sh --profile <p> --region <r> --yes    # execute
#
# Builds from the environment `capture-env.sh` wrote (the stack names and the full set of
# parameters passed to each stack), in the order
# `00 -> 10 -> 20 -> (images into ECR) -> 40 -> 30`. The reverse of teardown.sh.
#
# ## What it will not come up without
#
#  1. Order and capabilities. Each stack imports the previous one's exports, so the order
#     is fixed. `10-data` declares `Transform: AWS::LanguageExtensions` and so needs
#     CAPABILITY_AUTO_EXPAND; `20-platform` and `40-ec2-pool` create named IAM roles and
#     so need CAPABILITY_NAMED_IAM (without them the call is refused immediately).
#  2. ECR starts empty. The ECR repositories are 20-platform resources with
#     `EmptyOnDelete: true`, so a teardown took the images with them. Put them back with
#     `crane copy` after 20 and before 30 (`docker pull`+`push` flattens the index down to
#     a single architecture).
#  3. Two of the captured parameters cannot be reused as they are. `Ec2SlotLaunchTemplate`
#     and `Ec2SlotAmiArm64` are physical IDs the pool layer created, and a rebuild gives
#     them different values; override them with the new 40 outputs. Miss this and CFN
#     succeeds, the CP starts, and only the slots never come up again — they point at a
#     launch template that no longer exists.
#  4. The secrets are not in the stacks. The SSM SecureStrings (cookie-secret, master-key,
#     the IdP client secret) are not CFN parameters, so they are not in the environment
#     file either. Check only that they exist, and stop before building if they do not:
#     30 coming up while the CP cannot offer a login is the hardest breakage to read.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: standup.sh --profile <p> --region <r> [--yes] [--image-tag <tag>] [--cp-arch <a>]
                  [--from <prefix>] [--dry-run]
  --profile    aws cli profile (this is how a deployment is addressed)
  --region     region to build it in
  --stack      ingress stack name (default af-ecs-ingress)
  --yes        actually deploy (without it: preflight + plan only)
  --image-tag  image tag to run (default: the captured AF_IMAGE_TAG)
  --cp-arch    x86_64 | arm64 — which architecture the Control Plane's own task runs on
               (default: the captured CpArch). ⚠️ arm64 needs a CP image that is a
               two-architecture index at this tag; the check below refuses the pair
  --from       registry to copy the images from (default ghcr.io/k-k1/agent-fleet)
  --dry-run    print every write instead of making it
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; AF_YES=0; AF_DRY=0; TAG=""; CP_ARCH_ARG=""
FROM="ghcr.io/k-k1/agent-fleet"
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)   PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)    REGION="${2:?--region needs a value}"; shift ;;
    --stack)     STACK="${2:?--stack needs a value}"; shift ;;
    --yes)       AF_YES=1 ;;
    --image-tag) TAG="${2:?--image-tag needs a value}"; shift ;;
    --cp-arch)   CP_ARCH_ARG="${2:?--cp-arch needs x86_64|arm64}"; shift ;;
    --from)      FROM="${2:?--from needs a value}"; shift ;;
    --dry-run)   AF_DRY=1 ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
export AF_YES AF_DRY
af_env_init "$PROFILE" "$REGION" "$STACK"
if [ ! -r "$AF_ENV_DIR/params/30-ingress" ]; then
  echo "ERROR: nothing captured in $AF_ENV_DIR — building needs to know what to pass:" >&2
  echo "         deploy/aws/ecs/capture-env.sh --profile $PROFILE --region $REGION   # while the deployment is still alive" >&2
  echo "       the templates are in the repo, but the parameters passed to them exist only inside the deployment." >&2
  exit 1
fi
: "${TAG:=${AF_IMAGE_TAG:-}}"
[ -n "$TAG" ] || { echo "ERROR: no image tag (env has no AF_IMAGE_TAG; pass --image-tag)" >&2; exit 2; }
# CFN_DIR is overridable so the test (deploy/local/ecs-lifecycle-stub-test.sh) can swap in
# its own templates. The 51,200-byte branch is decided by size, so unless a padded template
# is fed through it far enough to see --s3-bucket actually appear, that path rots unrun
# again.
CFN_DIR="${AF_STANDUP_CFN_DIR:-$HERE/cfn}"

echo "==> standup plan: ${AF_FQDN:-<from the captured parameters>} (profile=$AF_PROFILE region=$AF_REGION)"
echo "    order : $AF_STACK_NETWORK → $AF_STACK_DATA → $AF_STACK_PLATFORM → images:$TAG${AF_STACK_POOL:+ → $AF_STACK_POOL} → $AF_STACK_INGRESS"

# --- 0) preflight (finding out mid-build is the most expensive way) ----------
fail=0
say_missing() { echo "    ✗ $1"; fail=1; }

for slug in 00-network 10-data 20-platform 30-ingress; do
  [ -r "$(af_params_file "$slug")" ] || say_missing "no params/$slug (was capture-env.sh ever run?)"
done
[ -z "$AF_STACK_POOL" ] || [ -r "$(af_params_file 40-ec2-pool)" ] || say_missing "AF_STACK_POOL=$AF_STACK_POOL but there is no params/40-ec2-pool"

# The ECS service-linked role. A new account does not have it, and creating the cluster
# with a Service Connect default namespace then fails with "ECS Service Linked Role is not
# ready". Creating it is idempotent.
if ! "${AWS[@]}" iam get-role --role-name AWSServiceRoleForECS >/dev/null 2>&1; then
  echo "    · creating AWSServiceRoleForECS (once per account)"
  af_run "${AWS[@]}" iam create-service-linked-role --aws-service-name ecs.amazonaws.com >/dev/null 2>&1 || true
fi

af_read_params 30-ingress
p30() { local p; for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do case "$p" in "$1"=*) echo "${p#*=}"; return ;; esac; done; }
SSM_PREFIX="$(p30 SsmPrefix)"; : "${SSM_PREFIX:=/af-cp}"
HOSTED_ZONE="$(p30 HostedZoneId)"
CP_ARCH="${CP_ARCH_ARG:-$(p30 CpArch)}"; : "${CP_ARCH:=x86_64}"
case "$CP_ARCH" in x86_64|arm64) ;; *) echo "--cp-arch takes x86_64|arm64 (got '$CP_ARCH')" >&2; exit 2 ;; esac

have_ssm() { "${AWS[@]}" ssm get-parameter --name "$1" --query 'Parameter.Name' --output text >/dev/null 2>&1; }
# Never read the value (no --with-decryption). All that is needed is whether it exists,
# and there is no reason at all to put a secret through this process's output or logs.
for s in cookie-secret master-key; do
  have_ssm "$SSM_PREFIX/$s" || say_missing "no SSM $SSM_PREFIX/$s (README §30-ingress stand-up, step 2)"
done
[ -z "$(p30 GoogleClientId)" ] || have_ssm "$SSM_PREFIX/google-client-secret" || say_missing "no SSM $SSM_PREFIX/google-client-secret (GoogleClientId is set)"
[ -z "$(p30 OidcClientId)" ]   || have_ssm "$SSM_PREFIX/oidc-client-secret"   || say_missing "no SSM $SSM_PREFIX/oidc-client-secret (OidcClientId is set)"
[ -z "$(p30 GithubClientId)" ] || have_ssm "$SSM_PREFIX/github-client-secret" || say_missing "no SSM $SSM_PREFIX/github-client-secret (GithubClientId is set)"

if [ -n "$HOSTED_ZONE" ]; then
  "${AWS[@]}" route53 get-hosted-zone --id "$HOSTED_ZONE" >/dev/null 2>&1 \
    || say_missing "cannot look up hosted zone $HOSTED_ZONE (the ACM DNS validation and the alias go in there)"
fi
command -v crane >/dev/null || say_missing "no crane (needed to carry GHCR -> ECR with the index intact)"

# Do the captured parameters still fit the templates as they are now?
#
# Without this, CFN refuses with "missing required parameter" only after 00-20 are already
# built — the exact hole this preflight exists to close, since finding out mid-build is the
# most expensive way. A capture is from the day the deployment was photographed and the
# templates keep growing after it. Only a parameter with no Default is required.
#
# The counting must not hard-code the indent depth. Reading "2 spaces = parameter name /
# 4 spaces = Default" picks up nothing from a template written differently and passes
# silently with "0 required" — a check that misfires where no one can see it, the same
# shape as #307 ("a path that is never run is broken without anyone finding out"). So take
# the depth of the first key inside the Parameters: section as the depth of a parameter
# name, and read anything deeper as one of its attributes. And when the section exists but
# nothing could be read, say so as an anomaly — that is distinguishable from a genuine
# "0 required" where every parameter has a Default.
for slug in 00-network 10-data 20-platform 30-ingress 40-ec2-pool; do
  f="$(af_params_file "$slug")"; t="$CFN_DIR/$slug.yaml"
  [ -r "$f" ] && [ -r "$t" ] || continue
  missing=""; has_section=0; parsed=0
  while read -r kind a b; do
    case "$kind" in
      req)  missing="$missing $a" ;;
      stat) has_section="$a"; parsed="$b" ;;
    esac
  done < <(awk '
    # Top-level keys decide entry to and exit from the section (skip all but Parameters:).
    /^[A-Za-z]/          { inp = ($0 ~ /^Parameters:/); if (inp) sect = 1; name = ""; ind = 0; next }
    !inp                 { next }
    /^[[:space:]]*(#|$)/ { next }
    {
      match($0, /^ */); w = RLENGTH
      key = substr($0, w + 1)
      if (key !~ /^[A-Za-z0-9_]+:/) next      # list items and block-scalar bodies
      sub(/:.*$/, "", key)
      if (!ind) ind = w                       # depth of the first key = depth of a name
      if (w == ind) { name = key; req[name] = 1; total++ }
      else if (key == "Default" && name != "") delete req[name]
    }
    END {
      for (k in req) print "req " k
      print "stat " sect+0 " " total+0
    }
  ' "$t")
  # The two messages below stay Japanese: ecs-lifecycle-stub-test.sh asserts them verbatim
  # ("read no Parameters: at all", "required parameter <name> is missing"), which is how it
  # proves the check is not misfiring. Translate one and its case passes blind.
  if [ "$has_section" = 1 ] && [ "$parsed" -eq 0 ]; then
    say_missing "read no Parameters: at all from $slug.yaml (this check is firing blind: either the template is written differently now or this awk is stale)"
    continue
  fi
  for k in $missing; do
    grep -q "^$k=" "$f" || say_missing "required parameter $k is missing from params/$slug (the template is newer than the capture)"
  done
done

# A template over 51,200 bytes has to go through S3 (af_cfn_deploy switches by itself).
# All that is said here is which ones will need it — the bucket can only be looked up once
# 20-platform is built, so it not existing yet is normal. af_cfn_deploy measures the size
# every time, so no future template can grow past the limit and fail silently.
for t in "$CFN_DIR"/*.yaml; do
  sz="$(wc -c < "$t" | tr -d ' ')"
  [ "$sz" -gt "$AF_CFN_TEMPLATE_MAX" ] && echo "    · $(basename "$t") is $sz bytes > $AF_CFN_TEMPLATE_MAX — passed via S3 (20-platform's CfnTemplatesBucket)"
done

# Are both images available at that tag? Without this check the build stops after 00-20
# with "workspace is not in GHCR", and there is a real path that gets there: on a round
# where dev-deploy does not bake workspace it only re-tags inside ECR, so that dev tag's
# workspace never exists in GHCR — and once a teardown takes ECR with it, the image exists
# nowhere at all.
if [ "$AF_DRY" != 1 ] && command -v crane >/dev/null; then
  for pair in "control-plane=af-control-plane" "workspace=af-workspace"; do
    if "${AWS[@]}" ecr describe-images --repository-name "${pair#*=}" --image-ids "imageTag=$TAG" >/dev/null 2>&1; then
      continue    # already in ECR (a rebuild re-run from the middle)
    fi
    crane manifest "$FROM/${pair%%=*}:$TAG" >/dev/null 2>&1 \
      || say_missing "no $FROM/${pair%%=*}:$TAG (in neither ECR nor GHCR). Bake dev-image.yml with image=both, or pass a tag that has both to --image-tag"
  done
fi

# A declared arm64 slot class needs both an arm64 AMI and an arm64 workspace image. With
# either missing CFN still succeeds and only the symptom shows up later (without the AMI
# the CP refuses to start; without the image the arm slots cannot pull). Fail only on
# proof — never stop the build merely because the image's architecture was unreadable.
case "$(p30 Ec2SlotTypes)" in
  *"|arm64|"*)
    [ -n "$(p30 Ec2SlotAmiArm64)" ] || say_missing "Ec2SlotTypes declares an arm64 class but Ec2SlotAmiArm64 is empty (the CP will refuse to start)"
    if [ "$AF_DRY" != 1 ] && command -v crane >/dev/null; then
      ws_manifest="$(crane manifest "$FROM/workspace:$TAG" 2>/dev/null || true)"
      case "$ws_manifest" in
        "") ;;                       # unreadable (not in GHCR — the check above already said so)
        *'"manifests"'*)             # an index: the architectures are readable, so this can be asserted
          ws_archs="$(printf '%s' "$ws_manifest" | tr ',' '\n' \
            | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' | sort -u | tr '\n' ' ')"
          case " $ws_archs " in
            *" arm64 "*) ;;
            *) say_missing "Ec2SlotTypes declares an arm64 class but workspace:$TAG only has ${ws_archs}(the arm slots cannot pull the image)" ;;
          esac ;;
        *)                           # a single manifest: only one architecture. A single
                                     # build on this path is the build host's amd64 (same
                                     # judgement as update.sh)
          say_missing "Ec2SlotTypes declares an arm64 class but workspace:$TAG is a single manifest (the arm slots cannot pull the image). Drop the arm class, or use a tag baked for both architectures" ;;
      esac
    fi
    ;;
esac

if [ "$fail" = 1 ]; then
  echo ""
  echo "ERROR: preconditions are missing. Clear the ✗ above first." >&2
  exit 1
fi
echo "    ✓ preflight"

if ! af_confirm "stand this deployment up (${AF_FQDN:-?}, image $TAG)"; then
  echo ""
  echo "(nothing was done. Add --yes to execute)"
  exit 0
fi

# --- 1-3) the static foundation ----------------------------------------------
deploy_stack() {  # deploy_stack <stack> <template> <slug> [capability...]
  local stack="$1" tpl="$2" slug="$3"; shift 3
  af_read_params "$slug"
  local caps=()
  [ $# -gt 0 ] && caps=(--capabilities "$@")
  echo "==> deploy $stack ($slug)"
  # A dry-run is there to show what would be passed, so print the values masked: handing
  # the raw array to af_run leaves values like BitbucketOauthKey on the terminal and in
  # the logs.
  if [ "$AF_DRY" = 1 ]; then
    echo "DRY: cloudformation deploy --stack-name $stack --template-file $CFN_DIR/$tpl ${caps[*]+${caps[*]}} \\"
    echo "     --parameter-overrides $(af_params_masked | tr '\n' ' ')"
    return 0
  fi
  af_cfn_deploy "$stack" "$CFN_DIR/$tpl" \
    ${caps[@]+"${caps[@]}"} \
    --parameter-overrides ${AF_PARAMS[@]+"${AF_PARAMS[@]}"} \
    --no-fail-on-empty-changeset
}

deploy_stack "$AF_STACK_NETWORK"  00-network.yaml  00-network
deploy_stack "$AF_STACK_DATA"     10-data.yaml     10-data     CAPABILITY_AUTO_EXPAND
deploy_stack "$AF_STACK_PLATFORM" 20-platform.yaml 20-platform CAPABILITY_NAMED_IAM

# --- 4) images (ECR starts out empty) ----------------------------------------
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
ECR_HOST="$ACCOUNT.dkr.ecr.$AF_REGION.amazonaws.com"
echo "==> images $TAG -> $ECR_HOST"
if [ "$AF_DRY" != 1 ]; then
  "${AWS[@]}" ecr get-login-password | crane auth login "$ECR_HOST" -u AWS --password-stdin
fi
for pair in "control-plane=af-control-plane" "workspace=af-workspace"; do
  src="$FROM/${pair%%=*}:$TAG"; dst="$ECR_HOST/${pair#*=}:$TAG"
  if "${AWS[@]}" ecr describe-images --repository-name "${pair#*=}" --image-ids "imageTag=$TAG" >/dev/null 2>&1; then
    echo "    · ${pair#*=}:$TAG is already in ECR"
  else
    echo "    · crane copy $src"
    af_run crane copy "$src" "$dst"
  fi
done
# Do CpArch and the CP image's architecture match? A mismatch is not even a
# CannotPullContainerError: the task simply cannot be placed, desired=1 / running=0, with
# no pull error logged at all. Same discipline as update.sh — fail only on proof.
if [ "$AF_DRY" != 1 ]; then
  want=amd64; [ "$CP_ARCH" = arm64 ] && want=arm64
  archs="$(crane manifest "$ECR_HOST/af-control-plane:$TAG" 2>/dev/null \
    | tr ',' '\n' | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' | sort -u | tr '\n' ' ')"
  case " $archs " in
    *" $want "*) echo "    ✓ af-control-plane:$TAG covers $want (CpArch=$CP_ARCH)" ;;
    "  ")        echo "    · af-control-plane:$TAG is a single manifest — assuming the build host's amd64"
                 [ "$want" = arm64 ] && { echo "ERROR: CpArch=arm64 but the image is single-arch" >&2; exit 1; } ;;
    *)           echo "ERROR: CpArch=$CP_ARCH needs '$want' and the image has: $archs" >&2; exit 1 ;;
  esac
fi

# --- 5) the pool layer (ecs-ec2 only) ----------------------------------------
if [ -n "$AF_STACK_POOL" ]; then
  deploy_stack "$AF_STACK_POOL" 40-ec2-pool.yaml 40-ec2-pool CAPABILITY_NAMED_IAM
fi

# --- 6) ingress (parameters holding physical IDs get the new outputs) --------
af_read_params 30-ingress
af_param_override ImageTag "$TAG"
# A flag has to move the value that is actually passed, not just what gets checked. Forget
# this and `--cp-arch arm64` becomes the worst kind of success: it verifies that arm64
# would work and then builds x86_64 (measured — the CP that came up had runtimePlatform
# x86_64). Some captures have no CpArch line at all, so the override has to add it.
af_param_override CpArch "$CP_ARCH"
if [ -n "$AF_STACK_POOL" ] && [ "$AF_DRY" != 1 ]; then
  lt="$(af_stack_output "$AF_STACK_POOL" SlotLaunchTemplateId)"
  ami="$(af_stack_output "$AF_STACK_POOL" SlotAmiIdArm64)"
  [ -n "$lt" ] || { echo "ERROR: $AF_STACK_POOL has no SlotLaunchTemplateId output" >&2; exit 1; }
  echo "==> slot launch template: $lt${ami:+ / arm64 ami $ami}"
  af_param_override Ec2SlotLaunchTemplate "$lt"
  # Never override with an empty value. The arm64 AMI does not necessarily come from the
  # pool layer: deployments exist where it is passed straight to 30 (40's SlotAmiIdArm64
  # is empty while 30's Ec2SlotAmiArm64 holds an ami-…). Overwriting with an empty value
  # leaves a declared arm64 class with no arm64 AMI, and the CP refuses to start
  # (runtime_ecs_ec2.go: "refuses to start when a declared arm64 class has no launch
  # template to run on"). Keep the captured value instead.
  if [ -n "$ami" ]; then
    af_param_override Ec2SlotAmiArm64 "$ami"
  else
    echo "    · $AF_STACK_POOL outputs no arm64 AMI — keeping the captured Ec2SlotAmiArm64"
  fi
fi
echo "==> deploy $AF_STACK_INGRESS (30-ingress)"
if [ "$AF_DRY" = 1 ]; then
  echo "DRY: cloudformation deploy --stack-name $AF_STACK_INGRESS --template-file $CFN_DIR/30-ingress.yaml \\"
  echo "     --parameter-overrides $(af_params_masked | tr '\n' ' ')"
else
  af_cfn_deploy "$AF_STACK_INGRESS" "$CFN_DIR/30-ingress.yaml" \
    --parameter-overrides ${AF_PARAMS[@]+"${AF_PARAMS[@]}"} \
    --no-fail-on-empty-changeset
fi

# --- 7) did it come up? ------------------------------------------------------
URL="$(af_stack_output "$AF_STACK_INGRESS" Url)"
if [ "$AF_DRY" != 1 ]; then
  echo "==> healthz"
  for _ in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "https://$AF_FQDN/healthz" || true)"
    [ "$code" = 200 ] && { echo "    ✓ https://$AF_FQDN/healthz 200"; break; }
    sleep 10
  done
  [ "${code:-}" = 200 ] || cat >&2 <<EOF
⚠️  /healthz is not returning 200 yet. Most likely, in order:
    - ACM DNS validation and DNS propagation (a few minutes the first time)
    - the CP task cannot be placed (CpArch vs the image architecture — unlikely if the check above passed)
    - the CP is stuck at startup: aws logs tail /af/$AF_STACK_INGRESS/cp --follow
EOF
fi

cat <<EOF

==> up: ${URL:-https://$AF_FQDN}  (ImageTag=$TAG)
    what matters next:
      - there is no golden yet. A new user's first launch is merely slow, not broken (the CP bakes one itself)
      - the cost allocation tags are not discovered by AWS until one Workspace has been raised (README §Prerequisites)
      - for a dev deployment update with deploy/aws/ecs/dev-deploy.sh from now on; for a release, update.sh
EOF

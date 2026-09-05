#!/usr/bin/env bash
# Agent Fleet — put develop on a development deployment WITHOUT cutting a version tag.
#
#   deploy/aws/ecs/dev-deploy.sh --profile <p> --region <r>
#
# A deployment is addressed by AWS profile, exactly as in `update.sh`.
# Which deployment is the development one is never written into this repository — the mark is
# `AF_DEV_DEPLOY=1` in the local capture (written by `capture-env.sh`).
#
# If `update.sh` is the one command that ships a release, this is the one command that ships
# today's develop. It folds the four steps of docs/log/72 §72.6.2 (dispatch dev-image.yml →
# crane copy the CP into ECR → put the workspace under the same tag → update.sh) into one, and
# it always ends in `update.sh`: the preflights for a missing tag, an empty changeset, a CpArch
# mismatch and the list of running Workspaces exist only there.
#
# ## The pitfalls this takes care of
#
#  1. CI bakes ORIGIN's ref, not the local working tree. Changes that are not committed (or not
#     pushed) silently stay out while the deploy "succeeds". So the tag's sha is taken from the
#     remote ref, and a local tree that differs gets a warning.
#  2. `ImageTag` is shared by the CP and the workspace (ADR 0045 decision 8). Re-baking only
#     the CP without putting the workspace under the same tag leaves the tag missing, and the
#     CP task will not come up. A `crane copy` within ECR is a pure re-tag that does not
#     duplicate any bytes, so that is enough on a run where the workspace did not change —
#     re-baking the workspace costs +593 seconds under QEMU (docs/log/72 §72.5.1). Whether to
#     bake is decided by the diff under `workspace/` (per `deploy/compose/release.sh`, the ws
#     image's build context is `workspace/` alone).
#     Touching the build arguments instead (`BAKE_AGENT_CLIS` in `release.sh`, …) does not show
#     up in that diff — say `--image both` explicitly in that case.
#  3. `docker pull` + `push` flattens an index down to one architecture. GHCR → ECR is carried
#     index and all by `crane copy` (there is no docker in this container, so crane is assumed).
#  4. Even a re-tag of the same digest makes the golden be re-baked (docs/log/72 §72.6.4). A
#     golden's `af-image` tag holds a reference string, not a digest, so even byte-identical
#     content is judged as "no golden for the new tag" and both architectures are re-baked, at
#     roughly 10 minutes and 2 slots. Confirming that the old and new digests are equal and
#     then re-stamping the golden's `af-image` removes those 10 minutes entirely.
#     (Fixing the CP to match on content equality would make the re-stamp unnecessary; even
#     after that, keeping the string tag aligned with reality is reason enough to leave this.)
#  5. Never aim this at a deployment with real users. Moving `ImageTag` raises a "restart
#     required" badge for everyone running there. Hence it only touches a deployment marked
#     `AF_DEV_DEPLOY=1` in the environment file (the mark lives outside the repo, so it cannot
#     be pointed at another deployment by mistake).
#
# ## What it does not do
#
# - It is not a release. It touches neither the dist repo nor a GitHub Release, and it has NOT
#   passed the forbidden-token gate. `dev-image.yml` refuses any tag without `-dev` in it.
# - It never stops a Workspace on its own (same reason as `update.sh`).
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: dev-deploy.sh --profile <p> --region <r> [options]

  --profile     aws cli profile (this is how a deployment is addressed)
  --region      region of the deployment
  --stack       ingress stack name (default af-ecs-ingress) — the one with ImageTag
                the deployment must be marked AF_DEV_DEPLOY=1 in the local capture
                (capture-env.sh writes it; see env.sh)
  --ref         git ref to bake (default develop). CI checks out ORIGIN's ref.
  --tag         image tag to use (default <latest tag + patch>-dev-<sha8>). Must contain -dev
  --image       auto | cp | both (default auto — both when `workspace/` has changed)
  --platforms   architectures to bake (default linux/amd64,linux/arm64)
  --skip-bake   do not dispatch dev-image.yml; the tag must already be in GHCR
  --rebake      bake even when the tag is already in GHCR
  --dry-run     print what would happen; touch nothing
  -h, --help

env:
  AF_DEV_GHCR   GHCR prefix (default ghcr.io/k-k1/agent-fleet)
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; REF="develop"; TAG=""
IMAGE="auto"; PLATFORMS="linux/amd64,linux/arm64"
SKIP_BAKE=0; REBAKE=0; DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --ref)        REF="${2:?--ref needs a value}"; shift ;;
    --tag)        TAG="${2:?--tag needs a value}"; shift ;;
    --image)      IMAGE="${2:?--image needs auto|cp|both}"; shift ;;
    --platforms)  PLATFORMS="${2:?--platforms needs a value}"; shift ;;
    --skip-bake)  SKIP_BAKE=1 ;;
    --rebake)     REBAKE=1 ;;
    --dry-run)    DRY=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
case "$IMAGE" in auto|cp|both) ;; *) echo "--image takes auto|cp|both (got '$IMAGE')" >&2; exit 2 ;; esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"
af_env_init "$PROFILE" "$REGION" "$STACK"
GHCR="${AF_DEV_GHCR:-ghcr.io/k-k1/agent-fleet}"
GIT=(git -C "$ROOT")

run() { if [ "$DRY" = 1 ]; then echo "DRY: $*"; return 0; fi; "$@"; }

for tool in gh crane aws git; do
  command -v "$tool" >/dev/null || { echo "ERROR: $tool is not installed" >&2; exit 1; }
done

# --- 1) confirm the target deployment (pitfall 5) ---------------------------
# Whether this is a development deployment is decided by the mark in the environment file.
# Writing a specific FQDN into the script would also mean leaving a deployment's identity in
# the repository, and this repository is public.
if [ "${AF_DEV_DEPLOY:-0}" != 1 ]; then
  echo "ERROR: this deployment ($PROFILE / $REGION) is not marked as a development deployment." >&2
  echo "       Moving ImageTag raises a 'restart required' badge for everyone running there." >&2
  echo "       If it IS a development deployment:" >&2
  echo "         deploy/aws/ecs/capture-env.sh --profile $PROFILE --region $REGION" >&2
  echo "         \$EDITOR $AF_ENV_DIR/env   # AF_DEV_DEPLOY=1" >&2
  exit 1
fi
params="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Parameters[].[ParameterKey,ParameterValue]' --output text)"
param() { echo "$params" | awk -v k="$1" '$1==k {sub(/^[^\t]*\t/,""); print; exit}'; }
FQDN="$(param Fqdn)"; CUR_TAG="$(param ImageTag)"; CP_ARCH="$(param CpArch)"; WS_RUNTIME="$(param WsRuntime)"
[ -n "$CUR_TAG" ] || { echo "ERROR: stack $STACK has no ImageTag parameter" >&2; exit 1; }
echo "==> target: $FQDN (stack=$STACK, now ImageTag=$CUR_TAG, CpArch=${CP_ARCH:-x86_64}, WsRuntime=${WS_RUNTIME:-ecs})"

# --- 2) the commit to bake (pitfall 1) --------------------------------------
# The remote ref is the truth. CI checks out origin's ref with `actions/checkout`, so using the
# local HEAD here would leave you believing you deployed the code in front of you.
SHA="$("${GIT[@]}" ls-remote origin "refs/heads/$REF" | awk '{print $1; exit}')"
[ -n "$SHA" ] || { echo "ERROR: origin has no branch '$REF'" >&2; exit 1; }
SHORT="${SHA:0:8}"
"${GIT[@]}" fetch --quiet origin "$REF" || true   # objects only, so the diff can be taken
if [ -n "$("${GIT[@]}" status --porcelain)" ] || [ "$("${GIT[@]}" rev-parse HEAD)" != "$SHA" ]; then
  echo "NOTE: what gets baked is origin/$REF ($SHORT). This working tree's HEAD and any uncommitted changes are not included."
fi

if [ -z "$TAG" ]; then
  base="$("${GIT[@]}" tag --sort=-v:refname | head -1)"; base="${base#v}"
  if [ -n "$base" ]; then
    IFS=. read -r maj min pat <<EOF
$base
EOF
    base="${maj:-0}.${min:-0}.$(( ${pat:-0} + 1 ))"
  else
    base="0.0.1"
  fi
  TAG="$base-dev-$SHORT"
fi
case "$TAG" in
  *-dev*) ;;
  *) echo "ERROR: '$TAG' does not contain '-dev'. dev-image.yml refuses it, and a release tag must never be overwritten from here." >&2; exit 1 ;;
esac
echo "==> tag: $TAG (origin/$REF = $SHA)"

# --- 3) bake the workspace too? (pitfall 2) ---------------------------------
# Measured against the commit of the tag that is running now: the trailing sha for a dev tag,
# whatever `v<semver>` points at for a release tag. When it is neither (i.e. not in this clone),
# give up on deciding automatically and ask for it to be said explicitly.
if [ "$IMAGE" = auto ]; then
  case "$CUR_TAG" in
    *-dev-*) base_sha="${CUR_TAG##*-dev-}" ;;
    *)       base_sha="v$CUR_TAG" ;;
  esac
  if "${GIT[@]}" cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
    changed="$("${GIT[@]}" diff --name-only "${base_sha}^{commit}" "$SHA" -- workspace/ | head -5)"
    if [ -n "$changed" ]; then
      IMAGE=both
      echo "==> workspace/ changed since $CUR_TAG — baking BOTH images (+~10min, QEMU):"
      while IFS= read -r f; do [ -n "$f" ] && echo "     $f"; done <<EOF
$changed
EOF
    else
      IMAGE="cp"
      echo "==> workspace/ unchanged since $CUR_TAG — baking the control-plane only, re-tagging the workspace image"
    fi
  else
    echo "ERROR: cannot resolve what '$CUR_TAG' was built from ($base_sha is not in this clone)," >&2
    echo "       so 'is the workspace image still current?' cannot be answered. Say it: --image cp | both" >&2
    exit 1
  fi
fi

# --- 4) bake (dev-image.yml) ------------------------------------------------
have_ghcr() { crane digest "$GHCR/$1:$TAG" >/dev/null 2>&1; }
need_bake=1
if [ "$SKIP_BAKE" = 1 ]; then
  need_bake=0
elif [ "$REBAKE" != 1 ] && have_ghcr control-plane && { [ "$IMAGE" = cp ] || have_ghcr workspace; }; then
  echo "==> $TAG is already in GHCR — skipping the bake (--rebake to force)"
  need_bake=0
fi
if [ "$need_bake" = 1 ]; then
  wf_image=control-plane; [ "$IMAGE" = both ] && wf_image=both
  echo "==> gh workflow run dev-image.yml (tag=$TAG, image=$wf_image, platforms=$PLATFORMS, ref=$REF)"
  run gh -R "$("${GIT[@]}" remote get-url origin | sed -E 's#.*github\.com[:/]##; s#\.git$##')" \
    workflow run dev-image.yml --ref "$REF" \
    -f tag="$TAG" -f image="$wf_image" -f platforms="$PLATFORMS"
  if [ "$DRY" != 1 ]; then
    # A dispatch returns no run id, so our own run is found by `run-name: dev-image <tag>`
    # (which is why dev-image.yml emits a run-name). The tag is unique, so it cannot pick up
    # somebody else's run.
    run_id=""
    for _ in $(seq 1 30); do
      sleep 4
      run_id="$(gh run list --workflow dev-image.yml --limit 20 \
        --json databaseId,displayTitle,headSha \
        --jq "[.[] | select(.displayTitle | contains(\"$TAG\"))] | first | .databaseId" 2>/dev/null || true)"
      [ -n "$run_id" ] && [ "$run_id" != "null" ] && break
    done
    [ -n "$run_id" ] && [ "$run_id" != "null" ] || {
      echo "ERROR: could not find the dev-image run for $TAG (look at the Actions tab)" >&2; exit 1; }
    echo "==> watching run $run_id"
    gh run watch "$run_id" --exit-status --interval 20
    # Did it really bake that commit? If develop moved between the dispatch and the checkout,
    # something else was baked and the sha in the tag is a lie.
    got_sha="$(gh run view "$run_id" --json headSha --jq .headSha)"
    [ "$got_sha" = "$SHA" ] || {
      echo "ERROR: the run built $got_sha but the tag says $SHA — origin/$REF moved. Re-run." >&2; exit 1; }
  fi
fi

# --- 5) carry it into ECR (pitfall 3) ---------------------------------------
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
ECR_HOST="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"
ecr_digest() {  # repo tag -> digest ("" when absent)
  "${AWS[@]}" ecr describe-images --repository-name "$1" --image-ids "imageTag=$2" \
    --query 'imageDetails[0].imageDigest' --output text 2>/dev/null || true
}
echo "==> crane auth login $ECR_HOST"
if [ "$DRY" != 1 ]; then
  "${AWS[@]}" ecr get-login-password | crane auth login "$ECR_HOST" -u AWS --password-stdin
fi
echo "==> crane copy control-plane (index and all)"
run crane copy "$GHCR/control-plane:$TAG" "$ECR_HOST/af-control-plane:$TAG"

WS_OLD="$ECR_HOST/af-workspace:$CUR_TAG"
WS_NEW="$ECR_HOST/af-workspace:$TAG"
ws_same_content=0
if [ "$IMAGE" = both ]; then
  echo "==> crane copy workspace (index and all)"
  run crane copy "$GHCR/workspace:$TAG" "$WS_NEW"
else
  # A re-tag inside ECR. The blobs are already there, so nothing is duplicated.
  echo "==> re-tag the workspace image $CUR_TAG -> $TAG (same bytes)"
  run crane copy "$WS_OLD" "$WS_NEW"
  ws_same_content=1
  # The re-tag happens inside ECR only. No workspace exists in GHCR under this $TAG, and
  # 20-platform's ECR is EmptyOnDelete: true, so it is gone everywhere the moment the
  # deployment is torn down. A capture left pointing at this tag becomes "the file is there but
  # the restore point is not" (env.sh).
  echo "    NOTE: this workspace exists only in ECR (GHCR has no $TAG). A teardown deletes the image with it."
  echo "       To keep it as material for a rebuild, re-bake with --image both or crane copy it to GHCR."
fi

# --- 6) re-stamp the golden (pitfall 4) -------------------------------------
# Only when the digests are equal. This says "the content was proven identical, so the same
# golden may be used" — it is not about making the tags agree.
if [ "${WS_RUNTIME:-}" = "ecs-ec2" ] && [ "$ws_same_content" = 1 ]; then
  old_d="$(ecr_digest af-workspace "$CUR_TAG")"
  new_d="$(ecr_digest af-workspace "$TAG")"
  if [ "$DRY" = 1 ]; then
    echo "DRY: would re-stamp af-image on golden snapshots ($WS_OLD -> $WS_NEW) if the digests match"
  elif [ -n "$old_d" ] && [ "$old_d" = "$new_d" ]; then
    ids="$("${AWS[@]}" ec2 describe-snapshots --owner-ids self \
      --filters "Name=tag:af-role,Values=golden" "Name=tag:af-image,Values=$WS_OLD" \
      --query 'Snapshots[].SnapshotId' --output text 2>/dev/null || true)"
    if [ -n "${ids// /}" ]; then
      echo "==> re-stamping af-image on golden snapshots (same digest $new_d): $ids"
      # shellcheck disable=SC2086  # word splitting is the list
      "${AWS[@]}" ec2 create-tags --resources $ids --tags "Key=af-image,Value=$WS_NEW"
      echo "    (without this the CP re-bakes an identical home, at ~10 min and 2 slots)"
    else
      echo "==> no golden stamped with $WS_OLD — nothing to re-stamp"
    fi
  else
    echo "NOTE: the workspace digests differ (old=$old_d new=$new_d) — the golden is not re-stamped."
  fi
fi

# --- 7) apply it (preflight, force, wait and the running-WS list are update.sh's job) -----
echo "==> update.sh (VERSION=$TAG)"
args=(--profile "$PROFILE" --region "$REGION" --stack "$STACK")
if [ "$DRY" = 1 ]; then
  # update.sh's first check is "is that tag in ECR", and on a dry run nothing was actually
  # pushed, so of course it is not. Failing here is the expected script, so say so and carry on
  # — exiting 1 silently would make a rehearsal of the steps look like a failure.
  echo "    (dry-run: the tag is not in ECR, so update.sh's preflight saying so is normal)"
  args+=(--dry-run)
  VERSION="$TAG" "$HERE/update.sh" "${args[@]}" || true
else
  VERSION="$TAG" "$HERE/update.sh" "${args[@]}"
fi

if [ "$DRY" = 1 ]; then
  cat <<EOF

==> dry-run: nothing was changed. The DRY: lines above are the operations that would run.
EOF
else
  # --- 8) keep the capture from going stale -----------------------------------
  # Whoever moves ImageTag also moves the capture. capture-env.sh refuses without `--force`, so
  # "re-capture after every deploy" is not a workable habit — the capture's AF_IMAGE_TAG would
  # rot at whatever it was on the day it was first taken, and that only becomes visible after
  # the teardown. Only 2 lines are rewritten, so that information AWS does not hold (the
  # AF_DEV_DEPLOY mark and the like) is not dropped.
  if [ -w "$AF_ENV_DIR/env" ]; then
    af_env_set AF_IMAGE_TAG "$TAG"
    recoverable=yes
    [ "$ws_same_content" = 1 ] && recoverable=no   # a re-tag means GHCR has no workspace
    af_env_set AF_IMAGE_RECOVERABLE "$recoverable"
    echo "==> capture updated: $AF_ENV_DIR/env (AF_IMAGE_TAG=$TAG / AF_IMAGE_RECOVERABLE=$recoverable)"
  else
    echo "NOTE: could not update the capture ($AF_ENV_DIR/env). Run capture-env.sh --force before tearing down."
  fi
  cat <<EOF

==> dev deploy: $FQDN is running origin/$REF ($SHORT) (ImageTag=$TAG)
    Confirm it in the CP log: control-plane $TAG on 0.0.0.0:...
EOF
fi

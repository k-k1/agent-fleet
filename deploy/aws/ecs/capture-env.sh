#!/usr/bin/env bash
# Agent Fleet — capture the arguments a live deployment would need to be rebuilt (read env.sh).
#
#   deploy/aws/ecs/capture-env.sh --profile <p> --region <r>
#
# Output goes to `~/.config/agent-fleet/deploy/<profile>.<region>/` (outside the repository).
#   env             … stack names / FQDN / runtime properties (the base for standing it back up)
#   params/<slug>   … each stack's parameters, one per line (`Key=Value`)
#
# ## This is what you always run before folding a deployment away
#
# The templates are in the repo, but what was passed TO those templates exists only inside the
# deployment — and it becomes unreadable the moment `delete-stack` is issued. Without it a
# rebuild is next to impossible, so step 0 of a teardown is a script.
#
# No secrets are captured. SSM SecureStrings (cookie-secret / master-key / the IdP client
# secret) are not CFN parameters, so they do not appear here and are not restored either.
# Account-specific values DO appear (hosted zone ID, allowed e-mail addresses, OAuth client
# IDs) — which is why this is stored outside the repo, with tight permissions.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: capture-env.sh --profile <p> --region <r> [--stack af-ecs-ingress] [--force]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    the stack that has ImageTag (default af-ecs-ingress). Everything else is
             discovered from it
  --force    overwrite what was captured before
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; FORCE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --force)   FORCE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }

af_env_init "$PROFILE" "$REGION" "$STACK"
[ "$AF_LIVE" = 1 ] || {
  echo "ERROR: stack '$STACK' not found in $PROFILE/$REGION — nothing to capture" >&2
  exit 1
}
OUT="$AF_ENV_DIR"
if [ -e "$OUT/env" ] && [ "$FORCE" != 1 ]; then
  echo "ERROR: $OUT is already captured (--force to refresh)" >&2
  # Say what is stale while refusing. Refusing alone hides a captured ImageTag that has drifted
  # from the live deployment, and since `dev-deploy.sh` leaves the capture behind every time it
  # moves ImageTag, this refusal was itself the way stale captures survived.
  captured_tag="$(sed -n 's/^AF_IMAGE_TAG=//p' "$OUT/env" | head -1)"
  if [ -n "$AF_IMAGE_TAG" ] && [ "$captured_tag" != "$AF_IMAGE_TAG" ]; then
    echo "       the capture is stale: AF_IMAGE_TAG=${captured_tag:-<none>} / live is $AF_IMAGE_TAG" >&2
    echo "       Tearing down like this leaves the rebuild pointing at the old tag (ECR is EmptyOnDelete). Re-capture with --force." >&2
  fi
  exit 1
fi

mkdir -p "$OUT/params"
chmod 700 "$OUT" "$OUT/params" 2>/dev/null || true

save_params() {  # save_params <stack> <slug>
  local stack="$1" slug="$2" f
  f="$OUT/params/$slug"
  if ! af_stack_exists "$stack"; then
    echo "  - $slug: stack '$stack' not found — skipped"
    return 0
  fi
  # `join` makes one `Key=Value` per line. A value never contains a newline (CFN parameters are
  # single-line), so values with spaces, brackets, `|` or commas round-trip unchanged.
  "${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
    --query "Stacks[0].Parameters[].join('=',[ParameterKey,ParameterValue])" \
    --output text | tr '\t' '\n' | grep -v '^$' > "$f"

  # An empty parameter can mean that the empty value selected the "create it myself" branch,
  # and then the id of what was created exists only in the Outputs, not in the parameters.
  # Copy the parameters alone and the next stand-up sees the same empty value, picks "create"
  # again, and the previous resource is orphaned.
  #
  # Measured on a real round trip: `NatEipAllocationId`. When empty, 00-network allocates the
  # EIP itself (Retain, so a teardown leaves it behind) and its allocation id appears only in
  # the Outputs. With parameters alone captured, the next stand-up takes a SECOND EIP and the
  # egress address customers have allow-listed silently changes (the orphan costs $3.6/month).
  #
  # One rule: when an Output has the same name as a parameter captured empty, take the Output's
  # value. Outputs whose name is not a parameter are not copied — CFN rejects parameters it does
  # not know, and most Outputs (VpcId and friends) are not parameters at all.
  local outs key val
  outs="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
    --query "Stacks[0].Outputs[].join('=',[OutputKey,OutputValue])" \
    --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true)"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    key="${line%%=*}"; val="${line#*=}"
    [ -n "$val" ] && [ "$val" != "None" ] || continue
    grep -q "^$key=\$" "$f" || continue          # only parameters captured empty
    sed -i "s|^$key=\$|$key=$val|" "$f"
    echo "  - $slug: $key taken from the Outputs (an empty parameter marks the 'create it' branch)"
  done <<EOF
$outs
EOF

  chmod 600 "$f" 2>/dev/null || true
  echo "  - $slug: $(wc -l < "$f") parameters ($stack)"
}

echo "==> capturing $AF_FQDN into $OUT"
save_params "$AF_STACK_NETWORK"  00-network
save_params "$AF_STACK_DATA"     10-data
save_params "$AF_STACK_PLATFORM" 20-platform
[ -n "${AF_STACK_POOL:-}" ] && save_params "$AF_STACK_POOL" 40-ec2-pool
save_params "$AF_STACK_INGRESS"  30-ingress

# Keep an existing mark, so that a --force re-capture does not erase the dev-deployment mark.
DEV_MARK=0
if [ -r "$OUT/env" ] && grep -q '^AF_DEV_DEPLOY=1' "$OUT/env"; then DEV_MARK=1; fi

# Decide at capture time whether the image this capture points at can still be pulled after a
# teardown (read the explanation in env.sh). What is measured is whether the restore point
# exists, not whether the capture file exists.
RECOVERABLE="$(af_image_recoverable "$AF_IMAGE_TAG")"

cat > "$OUT/env" <<EOF
# agent-fleet deployment — captured $(date +%Y-%m-%dT%H:%M:%S%z) from $AF_FQDN.
# Written by deploy/aws/ecs/capture-env.sh. Read when the deployment is NOT live
# (i.e. by standup.sh, to build it back). Not a secret store — see env.sh.
AF_FQDN=$AF_FQDN
AF_STACK_NETWORK=$AF_STACK_NETWORK
AF_STACK_DATA=$AF_STACK_DATA
AF_STACK_PLATFORM=$AF_STACK_PLATFORM
AF_STACK_POOL=${AF_STACK_POOL:-}
AF_STACK_INGRESS=$AF_STACK_INGRESS
AF_WS_RUNTIME=$AF_WS_RUNTIME
AF_PERSISTENCE=$AF_PERSISTENCE
AF_IMAGE_TAG=$AF_IMAGE_TAG
# Set to 1 for a development deployment. dev-deploy.sh (which puts develop on a deployment
# without cutting a version tag) only ever touches a deployment carrying this mark, because
# moving ImageTag raises a "restart required" badge for everyone running there. The mark lives
# here rather than in the repo because which deployment is the development one is part of a
# deployment's identity, and this repository is public.
AF_DEV_DEPLOY=$DEV_MARK
# Whether the AF_IMAGE_TAG this capture points at can be pulled again after a teardown (ECR is
# EmptyOnDelete). yes=both are in GHCR / no=not pullable (a tag that exists only in ECR) /
# unknown=crane is missing, so it could not be measured.
AF_IMAGE_RECOVERABLE=$RECOVERABLE
EOF
chmod 600 "$OUT/env" 2>/dev/null || true

if [ "$RECOVERABLE" = no ]; then
  cat >&2 <<EOF

This capture's restore point WILL BE LOST as it stands.
   AF_IMAGE_TAG=$AF_IMAGE_TAG is not complete in GHCR (when dev-deploy.sh only re-tagged the
   workspace inside ECR, no workspace exists in GHCR under that tag).
   Meanwhile 20-platform's ECR is EmptyOnDelete: true, so a teardown deletes the images too.
   Do one of these before tearing down:
     - Copy it out to GHCR:  crane copy <account>.dkr.ecr.$AF_REGION.amazonaws.com/af-workspace:$AF_IMAGE_TAG $AF_GHCR_DEFAULT/workspace:$AF_IMAGE_TAG
     - Re-bake both:         deploy/aws/ecs/dev-deploy.sh --image both (bakes under a new tag; re-capture afterwards)
     - At stand-up, point --image-tag at a tag that has both (standup.sh's preflight names one)
EOF
fi

cat <<EOF

==> captured: $AF_FQDN  (profile=$AF_PROFILE region=$AF_REGION)
    stacks: $AF_STACK_NETWORK / $AF_STACK_DATA / $AF_STACK_PLATFORM${AF_STACK_POOL:+ / $AF_STACK_POOL} / $AF_STACK_INGRESS
    runtime=$AF_WS_RUNTIME persistence=$AF_PERSISTENCE image=$AF_IMAGE_TAG

Every tool that touches a deployment addresses the same one with --profile / --region:
    deploy/aws/ecs/pause.sh      --profile $AF_PROFILE --region $AF_REGION [--up]
    deploy/aws/ecs/teardown.sh   --profile $AF_PROFILE --region $AF_REGION
    deploy/aws/ecs/standup.sh    --profile $AF_PROFILE --region $AF_REGION
    deploy/aws/ecs/dev-deploy.sh --profile $AF_PROFILE --region $AF_REGION   # needs AF_DEV_DEPLOY=1

Note: the SSM secrets ($(af_stack_param "$AF_STACK_INGRESS" SsmPrefix)/cookie-secret, master-key, the IdP client secret)
   are NOT in here. Tear down without deleting them and they can be used as-is on a rebuild.
EOF

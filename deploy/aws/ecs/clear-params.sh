#!/usr/bin/env bash
# Agent Fleet — make parameters of a live stack empty, everything else as it is.
#
#   deploy/aws/ecs/clear-params.sh --profile p --region r --stack af-ecs-ingress \
#     EgressProxyAddr EgressTokenSecretArn
#
# In the #2763 rehearsal `aws cloudformation deploy --parameter-overrides Key=` did not clear Key
# on a live stack, for a reason not yet known. This runs env.sh's af_cfn_clear_params, the same
# step update.sh takes: a change set on the stack's current template that leaves the keys out
# (that template's Default, checked to be exactly "", applies) and keeps every other parameter
# at its previous value, then each key read back.
#
# Unlike update.sh, whose own flow gates it, this one is gated on the change set: by default it
# builds the change set, prints it (resource changes and which keys go to empty), keeps it and
# stops. Executing is a second, explicit step (--execute <name>, or --yes for both at once), and
# only a change set that is still AVAILABLE and clears exactly the keys given.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: clear-params.sh --profile <p> --region <r> --stack <stack>
                       [--capability CAPABILITY_IAM|CAPABILITY_NAMED_IAM]
                       [--yes | --dry-run | --execute <change-set-name>] <key>...
  (no flag)     build the change set, print it, keep it and print the command that executes it;
                nothing on the stack changes
  --yes         build, print and execute at once, then read every key back
  --dry-run     build and print the change set, then delete it; nothing changes
  --execute N   execute the change set N from an earlier run, if it is AVAILABLE and clears
                exactly the keys given, then read every key back
  --capability  default CAPABILITY_NAMED_IAM (00-network and 10-data take CAPABILITY_IAM)
EOF
}

PROFILE=""; REGION=""; STACK=""; CAP=CAPABILITY_NAMED_IAM; MODE=keep; MODE_SET=0; CS=""; KEYS=()
set_mode() {
  if [ "$MODE_SET" = 1 ]; then echo "--yes, --dry-run and --execute exclude each other" >&2; usage; exit 2; fi
  MODE_SET=1; MODE="$1"
}
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --capability) CAP="${2:?--capability needs a value}"; shift ;;
    --yes)        set_mode execute ;;
    --dry-run)    set_mode discard ;;
    --execute)    set_mode run; CS="${2:?--execute needs a change set name}"; shift ;;
    -h|--help)    usage; exit 0 ;;
    -*) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    *) KEYS+=("$1") ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ] || [ -z "$STACK" ] || [ ${#KEYS[@]} -eq 0 ]; then
  usage; exit 2
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AWS=(aws --profile "$PROFILE" --region "$REGION")
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"
# The mode is passed explicitly, not through AF_DRY, which means "create nothing on AWS" to
# update.sh / standup.sh; an AF_DRY=1 inherited from the shell would otherwise make every mode a no-op.
export AF_DRY=0 AF_CLEAR_MODE="$MODE" AF_CLEAR_CS="$CS"
af_cfn_clear_params "$STACK" "$CAP" "${KEYS[@]}"
if [ "$MODE" = keep ] && [ -n "$AF_CLEAR_CS_KEPT" ]; then
  echo
  echo "Nothing was executed. To execute exactly this change set:"
  echo "  $0 --profile $PROFILE --region $REGION --stack $STACK --capability $CAP \\"
  echo "    --execute $AF_CLEAR_CS_KEPT ${AF_CLEAR_TODO[*]}"
  echo "To drop it instead:"
  echo "  aws --profile $PROFILE --region $REGION cloudformation delete-change-set --stack-name $STACK --change-set-name $AF_CLEAR_CS_KEPT"
fi

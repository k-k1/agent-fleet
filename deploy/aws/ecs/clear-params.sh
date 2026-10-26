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
# at its previous value, executed, then each key read back.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: clear-params.sh --profile <p> --region <r> --stack <stack>
                       [--capability CAPABILITY_IAM|CAPABILITY_NAMED_IAM] [--dry-run] <key>...
  --capability  default CAPABILITY_NAMED_IAM (00-network and 10-data take CAPABILITY_IAM)
  --dry-run     say which keys hold a value and would be cleared; change nothing
EOF
}

PROFILE=""; REGION=""; STACK=""; CAP=CAPABILITY_NAMED_IAM; DRY=0; KEYS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --capability) CAP="${2:?--capability needs a value}"; shift ;;
    --dry-run)    DRY=1 ;;
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
export AF_DRY="$DRY"
af_cfn_clear_params "$STACK" "$CAP" "${KEYS[@]}"

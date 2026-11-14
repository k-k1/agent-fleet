#!/usr/bin/env bash
# Dispatch the contract of every CLI whose public version just changed.
#
#   CHANGED_JSON='{"claude":"true",...}' deploy/local/cli-dispatch.sh
#
# In (environment):
#   CHANGED_JSON  `toJSON(steps.changed.outputs)` of the release-edges step (`<kind>=true`)
#   <SECRET NAME> each CI secret a dispatching contract needs, bound by the workflow; a
#                 contract with several runs when any one of them is non-empty
#   GH            the gh binary (default: gh); tests pass a stub
#
# Which kinds, which workflow, which `latest` input and which secrets come from the CLI
# manifest. `dispatch=manual` kinds (cursor, kiro) are never run from here: their credential
# is a refreshable interactive login that an unattended run must not spend, so their update
# issue asks a person (cli-update-issues.sh). A missing secret is a warning, not a failure:
# one CLI without CI credentials must not stop the others.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=cli-manifest.sh
. "$HERE/cli-manifest.sh"
manifest_load || exit 1
GH="${GH:-gh}"

for kind in "${M_NAMES[@]}"; do
  [ "$(jq -r --arg k "$kind" '.[$k] // empty' <<< "${CHANGED_JSON:-{\}}")" = true ] || continue
  [ "${M_dispatch[$kind]}" = auto ] || continue
  if [ "${M_secrets[$kind]}" != - ]; then
    have=false
    IFS=',' read -r -a names <<< "${M_secrets[$kind]}"
    for s in "${names[@]}"; do
      [ -z "${!s:-}" ] || have=true
    done
    if [ "$have" != true ]; then
      echo "::warning::$kind changed but none of ${M_secrets[$kind]//,/, } is configured; its contract was not dispatched"
      continue
    fi
  fi
  args="$(manifest_dispatch_args "$kind")"
  # shellcheck disable=SC2086 # $args is `-f input=latest` or empty
  "$GH" workflow run "${M_contract[$kind]}" $args
done

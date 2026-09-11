#!/bin/sh
# Ingest, step 2: put the verified file in the models bucket, or delete keys (MODE=delete).
#
# The essential container of the ingest task: its exit code is the job's. `cfn/PARAMETERS-60-
# engines.md`, "The ingest containers" and "The ingest permissions" -- deleting is a separate
# act by a separate principal, which is why `s3:DeleteObject` is on this task role and on
# nothing the Control Plane holds.
#
# Environment (the contract, version CONTRACT):
#   BUCKET  the models bucket   (required; it was a CloudFormation substitution while this
#           script lived inside the template, and is an environment variable now)
#   KEY     the destination key, or a space-separated list of keys when MODE=delete (required)
#   MODE    "delete" removes the keys instead of uploading  (optional)

# --- the contract gate -----------------------------------------------------------------
#
# 🔴 This is the whole answer to "the script and the template no longer ship together".
# Since the scripts moved out of `60-engines.yaml` into this image, a stack update and an image
# copy are two acts, and `standup.sh` copies images one step BEFORE it deploys the stack. The
# failure that buys is silent: an old image runs the old script, the service reaches a steady
# state, and the engine does the previous release's thing for as long as nobody looks.
#
# So the template declares which contract it was written against and every entry point refuses
# a number it does not recognise -- loudly, in the task's own log, at the first second rather
# than at the first surprise. What the number covers is the INTERFACE, not the code: the
# environment variables below, the SSM keys they name and the files written under $MODELS_DIR.
# Bump it in deploy/aws/ecs/engine-tools/CONTRACT and in 60-engines.yaml together; changing the
# script's behaviour without changing that interface does not need a bump.
#
# The number is read relative to THIS FILE so the same script works from the image (/opt/af)
# and straight out of the repository, which is what deploy/local/engine-sidecar-test.sh runs.
af_check_contract() {
  _want="$(cat "$(dirname "$0")/CONTRACT" 2>/dev/null || echo '?')"
  if [ "${ENGINE_TOOLS_CONTRACT:-}" = "$_want" ]; then return 0; fi
  echo "engine-tools: CONTRACT MISMATCH. This image speaks '$_want'; the task definition asked for '${ENGINE_TOOLS_CONTRACT:-<unset>}'." >&2
  echo "engine-tools: 60-engines.yaml and EngineToolsImageTag are out of step - nothing has run." >&2
  echo "engine-tools: PARAMETERS-60-engines.md, \"The engine tools image\"." >&2
  exit 78 # EX_CONFIG: a configuration error, not a failure of the work
}
af_check_contract

set -e
[ -n "$BUCKET" ] || { echo "ingest: BUCKET is required"; exit 2; }
[ -n "$KEY" ] || { echo "ingest: KEY is required"; exit 2; }
start=$(date +%s)
if [ "$MODE" = delete ]; then
  for k in $KEY; do
    aws s3 rm "s3://$BUCKET/$k" --only-show-errors
    echo "ingest: deleted $k"
  done
  exit 0
fi
aws s3 cp /scratch/blob "s3://$BUCKET/$KEY" --only-show-errors
echo "ingest: uploaded $KEY in $(( $(date +%s) - start ))s"

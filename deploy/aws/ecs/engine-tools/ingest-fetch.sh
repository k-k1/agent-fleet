#!/bin/sh
# Ingest, step 1: fetch one file from its source and verify it (ADR 0072 decision 6).
#
# Runs as the non-essential `fetch` container of the ingest task definition; the `upload`
# container depends on it with `Condition: SUCCESS`, so a file that fails its checksum never
# reaches the bucket. `cfn/PARAMETERS-60-engines.md`, "The ingest containers".
#
# Environment (the contract, version CONTRACT):
#   URL       what to fetch                              (required unless MODE=delete)
#   SHA256    the expected digest                        (optional, and warned about when absent)
#   MODE      "delete" skips this step entirely          (optional)
#   HF_TOKEN  injected from Secrets Manager; "-" is the sentinel for "no token registered"

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
[ "$MODE" = delete ] && { echo "ingest: delete mode, nothing to fetch"; exit 0; }
[ -n "$URL" ] || { echo "ingest: URL is required"; exit 2; }
start=$(date +%s)
if [ -n "$HF_TOKEN" ] && [ "$HF_TOKEN" != - ]; then
  set -- -H "Authorization: Bearer $HF_TOKEN"
else
  set --
fi
curl -fsSL "$@" -o /scratch/blob "$URL"
size=$(stat -c %s /scratch/blob)
secs=$(( $(date +%s) - start ))
echo "ingest: fetched $size bytes in ${secs}s"
if [ -n "$SHA256" ]; then
  got=$(sha256sum /scratch/blob | cut -d" " -f1)
  [ "$got" = "$SHA256" ] || { echo "ingest: sha256 mismatch: got $got want $SHA256"; exit 1; }
  echo "ingest: sha256 ok $got"
else
  echo "ingest: WARNING no SHA256 given - the upload is unverified"
fi

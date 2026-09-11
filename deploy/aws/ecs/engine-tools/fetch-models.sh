#!/bin/sh
# The engine fetch sidecar: turn the active set the Control Plane publishes into the engine's
# command line, pull what it names out of S3, and keep the instance in step afterwards.
#
# It runs in BOTH engine roles' task definitions (60-engines.yaml), as a non-essential container
# that shares the model volume with the engine. What it must get right, and why, is
# `cfn/PARAMETERS-60-engines.md`, "The fetch sidecar" -- including the one thing that is a
# CONTRACT rather than an implementation detail: the S3 layout (ADR 0071 decision 6, ADR 0072
# decision 2) is shared with the ingest task and with the provider that builds ComfyUI's graph,
# so a path here is never a local choice.
#
# 🔴 It is exercised for real by deploy/local/engine-sidecar-test.sh, against a stub `aws` and
# the real `jq`. That test reads THIS FILE. It used to read the script out of the template's
# `Mappings`, and moving the script without moving the test first would have left it proving
# things about a copy nothing runs.
#
# Environment (the contract, version CONTRACT):
#   MODELS_DIR     where the engine expects its files      (required)
#   BUCKET         the models bucket                       (required)
#   ACTIVE_PARAM   SSM parameter holding the active set    (required)
#   PENDING_PARAM  SSM parameter to publish what is still missing (optional; empty = do not)
#   WATCH_SEC      poll the active set every N seconds after the first pass (0 = exit)
#   PRESET_FILE    write a llama.cpp preset here instead of a command line (optional)
#   ALIAS_FLAG     the engine's flag for a model alias, when it takes one (optional)
#   CTX_FLAG       the engine's flag for the context window, when it takes one (optional)
#   SYNC_ALL       non-empty: sync every enabled model, not only the starting one

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
mkdir -p "$MODELS_DIR"
trap 'touch "$MODELS_DIR/ready" 2>/dev/null || true; rm -rf "${K:-}" 2>/dev/null || true' EXIT
# The key lists live under a directory of this PROCESS's own. They were fixed /tmp
# paths, which is invisible in a container of one process and a trap everywhere else:
# this script now stays resident (WATCH_SEC), so two of them - a leaked one from a
# previous test run, a second task on one host - rewrite each other's lists while the
# first is still reading them. Measured: it turned every later assertion in
# engine-sidecar-test.sh into a different failure.
K="${TMPDIR:-/tmp}/engine-fetch.$$"
mkdir -p "$K"
want_keys() {
  : > "$K"/keys.rest
  if [ -n "$PRESET_FILE$SYNC_ALL" ]; then
    printf '%s' "$1" | jq -r --arg s "$2" '[.models[]?|select(.id!=$s)|.f[]?|if type=="string" then . else .k end]|.[]' > "$K"/keys.rest
  fi
  printf '%s' "$1" | jq -r --arg s "$2" '[(.models[]?|select(.id==$s)|.f[]?|if type=="string" then . else .k end),(.loras[]?)]|.[]' > "$K"/keys.start
  cat "$K"/keys.start "$K"/keys.rest > "$K"/keys.all
}
pending_put() {
  if [ -z "$PENDING_PARAM" ]; then return 0; fi
  p=$(jq -R -s -c '[splits("\n")|select(length>0)]' < "$1" || printf '[]')
  if [ -z "$p" ]; then p='[]'; fi
  while [ ${#p} -gt 4000 ]; do p=$(printf '%s' "$p" | jq -c '.[:-1]'); done
  aws ssm put-parameter --name "$PENDING_PARAM" --type String --overwrite --value "$p" >/dev/null 2>&1 \
    || echo "engine fetch: could not write $PENDING_PARAM - the gateway cannot know to wait"
}
pending_sync() {
  if [ -z "$PENDING_PARAM" ]; then return 0; fi
  : > "$K"/keys.pending
  while read -r k; do
    if [ -z "$k" ]; then continue; fi
    if [ -s "$MODELS_DIR/$k" ]; then continue; fi
    echo "$k" >> "$K"/keys.pending
  done < "$K"/keys.all
  pending_put "$K"/keys.pending
}
fetch_keys() {
  while read -r k; do
    [ -n "$k" ] || continue
    dst="$MODELS_DIR/$k"
    if [ -s "$dst" ]; then echo "engine fetch: $k is already on this box"; continue; fi
    mkdir -p "$(dirname "$dst")"
    t0=$(date +%s)
    aws s3 cp "s3://$BUCKET/$k" "$dst.part" --only-show-errors
    mv "$dst.part" "$dst"
    echo "engine fetch: $k $(stat -c %s "$dst") bytes in $(( $(date +%s) - t0 ))s"
    pending_sync
  done < "$1"
}
V=$(aws ssm get-parameter --name "$ACTIVE_PARAM" --query Parameter.Value --output text 2>/dev/null || true)
if [ -z "$V" ] || [ "$V" = "None" ]; then
  echo "engine fetch: no active set at $ACTIVE_PARAM - this engine has nothing to load"
  : > "$MODELS_DIR/cmdline"
  : > "$K"/keys.pending
  pending_put "$K"/keys.pending
  exit 0
fi
START=$(printf '%s' "$V" | jq -r '.start // ""')
echo "engine fetch: active set for $ACTIVE_PARAM starts with '$START'"
if [ -n "$PRESET_FILE" ]; then
  START=$(printf '%s' "$V" | jq -r --arg s "$START" '[(.models[]?|select(.id==$s)|.id),(.models[0].id?)][0]//""')
fi
want_keys "$V" "$START"
pending_sync
fetch_keys "$K"/keys.start
if [ -n "$PRESET_FILE" ]; then
  mkdir -p "$(dirname "$PRESET_FILE")"
  printf '%s' "$V" | jq -r --arg s "$START" --arg d "$MODELS_DIR" '.models[]? as $m | ($m.a // []) as $a | (["["+$m.id+"]"] + [$m.f[]?|if type=="string" then "model = "+$d+"/"+. else (.g|ltrimstr("-")|ltrimstr("-"))+" = "+$d+"/"+.k end] + (if ($m.c//0)>0 then ["c = "+($m.c|tostring)] else [] end) + [$a|to_entries[]|select(.value|startswith("-"))|(.value|ltrimstr("-")|ltrimstr("-")) as $k|($a[.key+1]//"") as $v|if ($v!="" and ($v|startswith("-")|not)) then $k+" = "+$v else $k+" = true" end] + (if (($m.lo//[])|length)>0 then ["lora-scaled = " + (($m.lo)|map($d+"/"+.)|join(","))] else [] end) + (if $m.id==$s then ["load-on-startup = true"] else [] end) + [""])[]' > "$PRESET_FILE.part"
  mv "$PRESET_FILE.part" "$PRESET_FILE"
  echo "engine fetch: preset $PRESET_FILE holds $(grep -c '^\[' "$PRESET_FILE" || true) model(s), '$START' loaded at startup"
  if [ -s "$PRESET_FILE" ]; then printf -- '--models-preset %s' "$PRESET_FILE" > "$MODELS_DIR/cmdline.part"; else : > "$MODELS_DIR/cmdline.part"; fi
else
  printf '%s' "$V" | jq -r --arg s "$START" --arg d "$MODELS_DIR" --arg al "$ALIAS_FLAG" --arg cf "$CTX_FLAG" '(.models[]?|select(.id==$s)) as $m | if $m == null then "" else ([$m.f[]? | if type=="string" then ["-m",($d+"/"+.)] else [.g,($d+"/"+.k)] end] | add // []) + (if $al != "" then [$al,$m.id] else [] end) + (if $cf != "" and (($m.c // 0) > 0) then [$cf,($m.c|tostring)] else [] end) + ($m.a // []) | join(" ") end' > "$MODELS_DIR/cmdline.part"
fi
mv "$MODELS_DIR/cmdline.part" "$MODELS_DIR/cmdline"
echo "engine fetch: cmdline = $(cat "$MODELS_DIR/cmdline")"
touch "$MODELS_DIR/ready"
echo "engine fetch: engine may start; $(grep -c . "$K"/keys.rest || true) file(s) still to sync"
fetch_keys "$K"/keys.rest
pending_sync
echo "engine fetch: sync done"
case "${WATCH_SEC:-0}" in ''|*[!0-9]*) WATCH_SEC=0 ;; esac
if [ "$WATCH_SEC" -gt 0 ]; then
  echo "engine fetch: watching $ACTIVE_PARAM every ${WATCH_SEC}s for models enabled later"
  LAST="$V"
  while sleep "$WATCH_SEC"; do
    N=$(aws ssm get-parameter --name "$ACTIVE_PARAM" --query Parameter.Value --output text 2>/dev/null || true)
    if [ -z "$N" ] || [ "$N" = "None" ] || [ "$N" = "$LAST" ]; then continue; fi
    LAST="$N"
    S=$(printf '%s' "$N" | jq -r '.start // ""')
    want_keys "$N" "$S"
    pending_sync
    echo "engine fetch: the active set changed; $(grep -c . "$K"/keys.pending || true) file(s) to add"
    fetch_keys "$K"/keys.all
    pending_sync
    echo "engine fetch: this instance is in step with the active set"
  done
fi

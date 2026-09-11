#!/usr/bin/env bash
# The engine fetch sidecar, run for real (ADR 0072 P0).
#
#   deploy/local/engine-sidecar-test.sh
#
# ## Why this exists
#
# The sidecar is the heart of ADR 0072's P0 — it turns the active set the Control Plane
# publishes into the engine's command line — and it lives inside a CloudFormation `Mappings`
# entry, where nothing type-checks it, no linter reads it and the only feedback is a GPU box
# that comes up idle ten minutes later.
#
# It cost a real deployment to learn that. The first version used a FOLDED block (`>-`), and
# YAML keeps a MORE-INDENTED line inside one literal rather than folding it — so a jq filter
# indented under its own `jq -r` kept its newline, the shell ran `jq -r --arg s "$START"`
# (which dumps the whole document) and then tried to execute `[(.models[]?|…` as a command.
# The service reached a steady state with the idle placeholder and nothing anywhere said why.
#
# So this runs it, against a stub `aws` and the real `jq`, asserting the command line it writes.
# Two things are checked that only running it can check: that it is valid shell at all, and that
# the flags come out in the order the engine expects.
#
# 🔴 It reads `deploy/aws/ecs/engine-tools/fetch-models.sh` -- THE FILE THE IMAGE BAKES. Until
# the scripts moved out of the template this pulled the same text out of `Mappings`, and that is
# what has to stay true through any further move: a harness that reads a copy nobody runs
# reports on a copy nobody runs. (ADR 0072 P2 is the measured version of that lesson -- 13/13
# bench scenarios green against an integration path the task definition did not use.)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TPL="$ROOT/deploy/aws/ecs/cfn/60-engines.yaml"
TOOLS="$ROOT/deploy/aws/ecs/engine-tools"

command -v jq >/dev/null || { echo "SKIP: jq is not installed" >&2; exit 0; }
command -v python3 >/dev/null || { echo "SKIP: python3 is not installed" >&2; exit 0; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail() { echo "NG: $*" >&2; exit 1; }

# The scripts as the image runs them: copied next to CONTRACT, because the gate at the top of
# each one reads the number relative to its own directory (so the layout here and in the image
# are one thing, not two that have to agree).
cp "$TOOLS"/CONTRACT "$TOOLS"/*.sh "$WORK/"
SIDECAR="$WORK/fetch-models.sh"
CONTRACT="$(cat "$TOOLS/CONTRACT")"
export ENGINE_TOOLS_CONTRACT="$CONTRACT"

echo "== every engine-tools script is valid shell =="
for f in "$WORK"/*.sh; do sh -n "$f" || fail "$(basename "$f") does not parse as shell"; done

# 🔴 The gate is only worth having if the two numbers are kept together. The template declares
# the contract it was written against; let that drift and an old image passes unnoticed, which
# is the whole failure the gate exists for.
echo "== the template asks for the contract this tree ships =="
asked="$(python3 "$ROOT/deploy/local/cfn-contract.py" "$TPL")" || fail "could not read the template's contract"
[ "$asked" = "$CONTRACT" ] \
  || fail "60-engines.yaml asks for contract '$asked', engine-tools/CONTRACT says '$CONTRACT'"

# ... and that a number it does not recognise stops the container instead of running the wrong
# script. The positive control for the gate itself.
echo "== a contract mismatch refuses to run, and says so =="
if ENGINE_TOOLS_CONTRACT="$CONTRACT-nope" sh "$SIDECAR" >"$WORK/mismatch.out" 2>&1; then
  fail "the sidecar ran against a contract it does not speak"
fi
grep -q "CONTRACT MISMATCH" "$WORK/mismatch.out" \
  || fail "the refusal did not say why: $(cat "$WORK/mismatch.out")"
if ENGINE_TOOLS_CONTRACT="" sh "$SIDECAR" >"$WORK/unset.out" 2>&1; then
  fail "the sidecar ran for a task definition that declares no contract at all"
fi

# A stub `aws`: `ssm get-parameter` prints whatever ACTIVE_SET_FIXTURE holds (or fails the way
# a missing parameter does), and `s3 cp` writes a file of the right shape.
mkdir -p "$WORK/bin"
cat > "$WORK/bin/aws" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "ssm get-parameter")
    if [ -z "${ACTIVE_SET_FIXTURE:-}" ]; then
      echo "An error occurred (ParameterNotFound)" >&2
      exit 254
    fi
    printf '%s' "$ACTIVE_SET_FIXTURE"
    ;;
  "ssm put-parameter")
    # What the instance has NOT synced yet (ADR 0072 P2 欠落 7). Recorded as one line per
    # write, because the SEQUENCE is the contract: the list has to shrink as files land and
    # end empty, or the gateway holds requests for a model that is already there.
    name=""; value=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --name) name="$2"; shift 2 ;;
        --value) value="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    [ -z "${AF_TEST_PENDING:-}" ] || echo "$name $value" >> "$AF_TEST_PENDING"
    ;;
  "s3 cp")
    dst="$4"
    [ -z "${AF_TEST_FAIL_CP:-}" ] || { echo "stub aws: refusing to copy" >&2; exit 1; }
    mkdir -p "$(dirname "$dst")"
    printf 'weights' > "$dst"
    # ready=n/y records whether the ENGINE had already been released when this object was
    # fetched. That, not the order of the list, is the property start-first sync is for.
    if [ -f "$MODELS_DIR/ready" ]; then r=y; else r=n; fi
    echo "$3 ready=$r" >> "$AF_TEST_FETCHED"
    ;;
  *) echo "stub aws: unexpected $*" >&2; exit 2 ;;
esac
STUB
chmod +x "$WORK/bin/aws"
export PATH="$WORK/bin:$PATH"

run() { # run <fixture> <alias-flag> <ctx-flag> [preset-file] [sync-all] -> writes $WORK/models/cmdline
  rm -rf "$WORK/models"; mkdir -p "$WORK/models"
  : > "$WORK/fetched"
  : > "$WORK/pending"
  ACTIVE_SET_FIXTURE="$1" ALIAS_FLAG="$2" CTX_FLAG="$3" PRESET_FILE="${4:-}" SYNC_ALL="${5:-}" \
  MODELS_DIR="$WORK/models" BUCKET="b" ACTIVE_PARAM="/af-ws/engines/x/active" \
  PENDING_PARAM="${PENDING_PARAM:-}" AF_TEST_PENDING="$WORK/pending" \
  AF_TEST_FETCHED="$WORK/fetched" \
    sh "$SIDECAR" > "$WORK/out" 2>&1 || fail "the sidecar exited non-zero: $(cat "$WORK/out")"
}

echo "== ParameterNotFound is EMPTY, not an error =="
# 60-engines is created before 30-ingress, so at stack-creation time there is no Control Plane
# and no parameter. A `set -e` failure here brings the two-pass stand-up back in a new shape
# (ADR 0072 decision 1(b)).
run "" "" ""
[ -f "$WORK/models/cmdline" ] || fail "no cmdline was written for an absent active set"
[ ! -s "$WORK/models/cmdline" ] || fail "an absent active set produced a command line: $(cat "$WORK/models/cmdline")"
grep -q "nothing to load" "$WORK/out" || fail "the sidecar did not say why: $(cat "$WORK/out")"
[ ! -s "$WORK/fetched" ] || fail "it fetched something with no active set"

echo "== the image role: one checkpoint, no alias and no window =="
IMG='{"v":1,"key":"image","start":"sdxl-base-1.0","models":[{"id":"sdxl-base-1.0","f":["image/checkpoints/sd_xl_base_1.0.safetensors"]}]}'
run "$IMG" "" ""
got="$(cat "$WORK/models/cmdline")"
want="-m $WORK/models/image/checkpoints/sd_xl_base_1.0.safetensors"
[ "$got" = "$want" ] || fail "image cmdline is '$got', want '$want'"
grep -q "s3://b/image/checkpoints/sd_xl_base_1.0.safetensors" "$WORK/fetched" \
  || fail "the checkpoint was not fetched: $(cat "$WORK/fetched")"

echo "== a single-model role: --alias and -c come from the catalogue =="
# The shape the llm role had before router mode, and the one any single-model engine still
# gets: everything model-specific on the command line.
LLM='{"v":1,"key":"llm","start":"qwen3","models":[{"id":"qwen3","f":["llm/q.gguf"],"c":32768,"a":["--jinja"]}]}'
run "$LLM" "--alias" "-c"
got="$(cat "$WORK/models/cmdline")"
want="-m $WORK/models/llm/q.gguf --alias qwen3 -c 32768 --jinja"
[ "$got" = "$want" ] || fail "llm cmdline is '$got', want '$want'"

echo "== a split model keeps each part's own flag =="
# The flag is stored in the catalogue rather than a role name, so the box needs no mapping
# table. sd.cpp mixes --clip_l with --diffusion-model, and that spelling has to survive.
SPLIT='{"v":1,"key":"image","start":"klein","models":[{"id":"klein","f":["image/diffusion_models/k.safetensors",{"g":"--t5xxl","k":"image/text_encoders/q.safetensors"},{"g":"--vae","k":"image/vae/ae.safetensors"}],"a":["--type","q8_0"]}]}'
run "$SPLIT" "" ""
got="$(cat "$WORK/models/cmdline")"
want="-m $WORK/models/image/diffusion_models/k.safetensors --t5xxl $WORK/models/image/text_encoders/q.safetensors --vae $WORK/models/image/vae/ae.safetensors --type q8_0"
[ "$got" = "$want" ] || fail "split cmdline is '$got', want '$want'"

echo "== only the STARTING model is fetched, and every enabled LoRA =="
# Enabling a model OFFERS it; selecting one LOADS it. Syncing every enabled model would put
# minutes of somebody else's checkpoint into every cold start (S3 to EBS: 92-147 MB/s measured).
MANY='{"v":1,"key":"image","start":"a","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]},{"id":"b","f":["image/checkpoints/b.safetensors"]}],"loras":["image/loras/w.safetensors"]}'
run "$MANY" "" ""
grep -q "image/checkpoints/a.safetensors" "$WORK/fetched" || fail "the starting checkpoint was not fetched"
grep -q "image/loras/w.safetensors" "$WORK/fetched" || fail "an enabled LoRA was not fetched"
if grep -q "image/checkpoints/b.safetensors" "$WORK/fetched"; then
  fail "a model that is merely enabled was fetched — that is minutes of cold start per start"
fi

echo "== SYNC_ALL: an engine that switches per REQUEST stages every enabled model =="
# The other half of the rule above. comfy has no preset file, so PRESET_FILE cannot be the
# signal — `enabled` has to mean `on the box` or the engine answers `Value not in list: … not
# in []` for everything but the starting model (measured on af-sandbox, ADR 0072 P2).
run "$MANY" "" "" "" 1
grep -q "image/checkpoints/a.safetensors" "$WORK/fetched" || fail "SYNC_ALL lost the starting checkpoint"
grep -q "image/checkpoints/b.safetensors" "$WORK/fetched" || fail "SYNC_ALL did not stage a merely-enabled model"

echo "== SYNC_ALL: a SPLIT model that is not the starting one lands file by file =="
# The shape that actually failed: FLUX.2 klein is three files under their own flags, and the
# non-start branch has to read `.k` out of each object exactly as the start branch does.
SPLITREST='{"v":1,"key":"image","start":"a","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]},{"id":"k","f":[{"g":"--diffusion-model","k":"image/diffusion_models/k.safetensors"},{"g":"--clip_l","k":"image/text_encoders/q.safetensors"},{"g":"--vae","k":"image/vae/v.safetensors"}]}]}'
run "$SPLITREST" "" "" "" 1
for k in image/diffusion_models/k.safetensors image/text_encoders/q.safetensors image/vae/v.safetensors; do
  grep -q "$k" "$WORK/fetched" || fail "SYNC_ALL did not stage $k from a split non-start model"
done

echo "== a start id that names nothing leaves an EMPTY command line, not a broken one =="
# The engine then idles instead of being handed half an argument list. Reachable when a row is
# deleted between the publish and the box coming up.
GONE='{"v":1,"key":"image","start":"gone","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]}]}'
run "$GONE" "" ""
[ ! -s "$WORK/models/cmdline" ] || fail "a missing start id produced '$(cat "$WORK/models/cmdline")'"

echo "== the llm ROUTER: one preset section per model, and nothing model-specific on the command line =="
# ADR 0072 decision 3 / phase P1. The section name is the CATALOGUE ID — measured against
# llama.cpp b10853, a preset section that matches no file DEFINES a model, which is what lets a
# member pick `llamacpp/qwen3-coder-30b-a3b` for a file called Qwen3-Coder-…-Q4_K_M.gguf.
ROUTER='{"v":1,"key":"llm","start":"small","models":[{"id":"qwen3","f":["llm/q.gguf"],"c":32768,"a":["--jinja","-ngl","99"]},{"id":"small","f":["llm/s.gguf"],"c":8192}]}'
run "$ROUTER" "" "" "$WORK/models/llm/presets.ini"
got="$(cat "$WORK/models/cmdline")"
want="--models-preset $WORK/models/llm/presets.ini"
[ "$got" = "$want" ] || fail "router cmdline is '$got', want '$want'"
# ⚠️ -m / --alias / -c must be GONE. A -c on the command line wins over every preset section
# (measured: two models declaring 4096 and 384 both came up --ctx-size 32768), so a stray flag
# here would silently give every model one window.
case "$got" in *" -m "*|*"--alias"*|*" -c "*) fail "the router command line still carries a model flag: $got";; esac
preset="$(cat "$WORK/models/llm/presets.ini")"
want_preset='[qwen3]
model = '"$WORK"'/models/llm/q.gguf
c = 32768
jinja = true
ngl = 99

[small]
model = '"$WORK"'/models/llm/s.gguf
c = 8192
load-on-startup = true'
[ "$preset" = "$want_preset" ] || fail "the preset is:
$preset
want:
$want_preset"

echo "== the router syncs EVERY enabled model, not just the starting one =="
# The opposite rule to the image role's, and for a measured reason: the router answers a request
# for any listed model, and a model whose file is absent fails with 500 rather than waiting.
grep -q "llm/q.gguf" "$WORK/fetched" || fail "a model that is not the starting one was not fetched"
grep -q "llm/s.gguf" "$WORK/fetched" || fail "the starting model was not fetched"

echo "== a start id that names nothing still loads something at startup =="
# Not the image role's rule (empty command line, engine idles): every model is already on the
# box, so the honest repair is to load the first one — otherwise nothing is ever `loaded`, the
# engine never reads as warm, and the controller stops it as a failed start 900 s later.
ORPHAN='{"v":1,"key":"llm","start":"gone","models":[{"id":"first","f":["llm/a.gguf"],"c":4096},{"id":"second","f":["llm/b.gguf"]}]}'
run "$ORPHAN" "" "" "$WORK/models/llm/presets.ini"
grep -q "^load-on-startup = true" "$WORK/models/llm/presets.ini" || fail "nothing is loaded at startup"
python3 - "$WORK/models/llm/presets.ini" <<'PY' || fail "load-on-startup did not fall back to the first model"
import sys
sec, hit = "", ""
for line in open(sys.argv[1]):
    if line.startswith("["): sec = line.strip()
    if line.strip() == "load-on-startup = true": hit = sec
raise SystemExit(0 if hit == "[first]" else 1)
PY

echo "== a pinned LoRA becomes ONE lora-scaled key, in the section of its base model =="
# ADR 0072 decision 5, the llm half: "this model, with this fine-tune", declared in the
# catalogue and invisible to opencode, which sees an ordinary model id.
#
# 🔴 ONE key with a comma-separated list, never one key per adapter. llama.cpp parses a preset
# into `std::map<common_arg, std::string>` and the INI reader writes `parsed[section][key] =
# value`, so a second `lora-scaled = …` line OVERWRITES the first — two adapters would silently
# become one. The CSV form is what `--lora-scaled FNAME:SCALE,...` documents and the only one
# that carries both (read in common/preset.cpp and common/arg.cpp at master, 2026-09-11).
PINNED='{"v":1,"key":"llm","start":"base","models":[{"id":"base","f":["llm/b.gguf"],"c":4096,"lo":["llm/loras/a.gguf:1","llm/loras/b.gguf:0.8"]},{"id":"plain","f":["llm/p.gguf"]}],"loras":["llm/loras/a.gguf","llm/loras/b.gguf"]}'
run "$PINNED" "" "" "$WORK/models/llm/presets.ini"
preset="$(cat "$WORK/models/llm/presets.ini")"
want_preset='[base]
model = '"$WORK"'/models/llm/b.gguf
c = 4096
lora-scaled = '"$WORK"'/models/llm/loras/a.gguf:1,'"$WORK"'/models/llm/loras/b.gguf:0.8
load-on-startup = true

[plain]
model = '"$WORK"'/models/llm/p.gguf'
[ "$preset" = "$want_preset" ] || fail "the pinned preset is:
$preset
want:
$want_preset"
# The adapters themselves are fetched with the starting model, before the engine is released:
# a preset naming a file that is not there is a model that fails to load, not one that waits.
grep -q "llm/loras/a.gguf" "$WORK/fetched" || fail "a pinned LoRA was never downloaded"
# And a model with no adapters says nothing at all — an empty `lora-scaled =` would be a path
# of "", which llama.cpp reads as a file it cannot open.
case "$preset" in *"[plain]"$'\n'*"lora"*) fail "a model with no LoRA got a lora key anyway";; esac

echo "== disabling the LoRA takes the line out again (positive control) =="
# The same document with the pins removed, which is exactly what the CP publishes once the row
# is switched off. Without this the golden above would pass just as well against a sidecar that
# writes `lora-scaled` unconditionally from `loras[]` — the bug that would pin every adapter to
# every model.
UNPINNED='{"v":1,"key":"llm","start":"base","models":[{"id":"base","f":["llm/b.gguf"],"c":4096}],"loras":["llm/loras/a.gguf"]}'
run "$UNPINNED" "" "" "$WORK/models/llm/presets.ini"
grep -q "lora" "$WORK/models/llm/presets.ini" && fail "the preset still pins a LoRA nobody enabled"
grep -q "llm/loras/a.gguf" "$WORK/fetched" || fail "an enabled LoRA must still be staged on the box"

echo "== an empty catalogue leaves the router idling too =="
# The wrapper starts the engine only when /models/cmdline is non-empty, so an active set with no
# models has to produce an EMPTY one — a router started with an empty preset would answer
# /health with ok for ever and read as a healthy engine holding nothing.
EMPTY='{"v":1,"key":"llm","start":"","models":[]}'
run "$EMPTY" "" "" "$WORK/models/llm/presets.ini"
[ ! -s "$WORK/models/cmdline" ] || fail "an empty catalogue produced '$(cat "$WORK/models/cmdline")'"

echo "== the engine is released after the START model and BEFORE the rest =="
# The point of the change: a router syncs every enabled model, but only the starting one is on
# the critical path. Measured on the deployment, the second model cost 5 s and the starting one
# 117 s -- with five models enabled the old order put minutes of somebody else's weights in
# front of the first token (ADR 0072 open question 11, the "time wall").
ORDER='{"v":1,"key":"llm","start":"second","models":[{"id":"first","f":["llm/a.gguf"]},{"id":"second","f":["llm/b.gguf"]},{"id":"third","f":["llm/c.gguf"]}],"loras":["llm/loras/l.gguf"]}'
run "$ORDER" "" "" "$WORK/models/llm/presets.ini"
grep -q '^s3://b/llm/b.gguf ready=n$' "$WORK/fetched" || fail "the START model was not fetched before the engine was released: $(cat "$WORK/fetched")"
grep -q '^s3://b/llm/loras/l.gguf ready=n$' "$WORK/fetched" || fail "a LoRA was deferred; they are small and belong with the start model: $(cat "$WORK/fetched")"
grep -q '^s3://b/llm/a.gguf ready=y$' "$WORK/fetched" || fail "a non-start model was fetched BEFORE the engine was released: $(cat "$WORK/fetched")"
grep -q '^s3://b/llm/c.gguf ready=y$' "$WORK/fetched" || fail "a non-start model was fetched BEFORE the engine was released: $(cat "$WORK/fetched")"
[ "$(grep -c . "$WORK/fetched")" = 4 ] || fail "every enabled model must still be synced, just later: $(cat "$WORK/fetched")"

echo "== the marker is written even when there is nothing to load =="
# The engine no longer waits for the sidecar to EXIT, it waits for the marker. So every way out
# of this script has to leave one, or an engine with an empty catalogue waits an hour instead of
# idling and the service never stabilises (ADR 0072 decision 1(b)).
run "" "" ""
[ -f "$WORK/models/ready" ] || fail "no marker for an absent active set: the engine would hang"

echo "== a FAILED fetch still releases the engine, and leaves no command line =="
# Without the trap the engine waits out its whole hour on a box that is billing at $1.26/h, and
# the panel says nothing. With it, the wrapper finds no cmdline and idles, which is the same
# thing an empty catalogue does and is already understood everywhere downstream.
rm -rf "$WORK/models"; mkdir -p "$WORK/models"; : > "$WORK/fetched"
ACTIVE_SET_FIXTURE="$IMG" ALIAS_FLAG="" CTX_FLAG="" PRESET_FILE="" MODELS_DIR="$WORK/models" BUCKET="b" ACTIVE_PARAM="/af-ws/engines/x/active" AF_TEST_FETCHED="$WORK/fetched" AF_TEST_FAIL_CP=1   sh "$SIDECAR" > "$WORK/out" 2>&1 && fail "a failed fetch should not exit 0"
[ -f "$WORK/models/ready" ] || fail "a failed fetch left no marker: the engine would hang for an hour"
[ ! -s "$WORK/models/cmdline" ] || fail "a failed fetch produced a command line: $(cat "$WORK/models/cmdline")"

# --- what the instance has not synced yet (ADR 0072 P2 欠落 7) ---------------------------
#
# 🔴 The window the gateway needs told about: the engine is released as soon as the START model
# is down, so for the length of the rest of the sync it is UP and missing weights. Measured on
# af-sandbox: 12 files / 48 GB / ~270 s, and a request for one of them got ComfyUI's bare
# `Value not in list: …` 400, which nothing retries.

echo "== the pending list is published, shrinks as files land, and ends empty =="
PENDING_PARAM=/af-ws/engines/x/pending run "$MANY" "" "" "" 1
[ -s "$WORK/pending" ] || fail "nothing was written to the pending parameter"
awk '{print $1}' "$WORK/pending" | sort -u | grep -qx "/af-ws/engines/x/pending" \
  || fail "the wrong parameter was written: $(cat "$WORK/pending")"
first="$(head -1 "$WORK/pending" | cut -d' ' -f2-)"
last="$(tail -1 "$WORK/pending" | cut -d' ' -f2-)"
# The first write is BEFORE anything is fetched, so it names every file the instance owes.
for k in image/checkpoints/a.safetensors image/checkpoints/b.safetensors image/loras/w.safetensors; do
  case "$first" in *"$k"*) ;; *) fail "the first pending list is missing $k: $first";; esac
done
[ "$last" = "[]" ] || fail "the last pending list is '$last', want [] — the gateway would hold a model that is there"
# And it is a JSON array of bare keys, which is what the Control Plane parses.
printf '%s' "$last" | jq -e 'type=="array"' >/dev/null || fail "the pending value is not a JSON array: $last"
printf '%s' "$first" | jq -e 'all(type=="string")' >/dev/null || fail "the pending list is not bare keys: $first"

echo "== the START model leaves the list before the engine is released =="
# The other half of the ordering the sync already keeps: by the time the engine may start, the
# model it starts with is no longer pending, or the gateway would refuse the very first request.
PENDING_PARAM=/af-ws/engines/x/pending run "$ORDER" "" "" "$WORK/models/llm/presets.ini"
grep -q "engine may start" "$WORK/out" || fail "the engine was never released: $(cat "$WORK/out")"
# The first list written after the START model landed: it must already be without that model
# — the engine is about to serve requests for it — and must still name the rest.
after="$(grep -v "llm/b.gguf" "$WORK/pending" | head -1 | cut -d' ' -f2-)"
case "$after" in
  *llm/a.gguf*) ;;
  *) fail "the start model and the rest left the pending list together: $(cat "$WORK/pending")" ;;
esac
[ "$(tail -1 "$WORK/pending" | cut -d' ' -f2-)" = "[]" ] || fail "the sync ended with files still pending"

echo "== no PENDING_PARAM: nothing is written, and the sidecar behaves exactly as before =="
# Every deployment whose engine stack predates this. The Control Plane reads "unknown" there
# and holds nothing, so the sidecar must not fail for want of a parameter to write.
run "$MANY" "" "" "" 1
[ ! -s "$WORK/pending" ] || fail "a deployment with no pending parameter had one written: $(cat "$WORK/pending")"
grep -q "image/checkpoints/a.safetensors" "$WORK/fetched" || fail "the sync stopped working without the parameter"

echo "== an engine with nothing to load publishes an EMPTY list, not a stale one =="
# A previous instance may have died mid-sync, leaving keys in the parameter. The gateway would
# then hold requests for a model this instance has no intention of fetching.
PENDING_PARAM=/af-ws/engines/x/pending run "" "" ""
[ "$(tail -1 "$WORK/pending" | cut -d' ' -f2-)" = "[]" ] || fail "an empty active set left the pending list alone: $(cat "$WORK/pending")"

echo "== WATCH_SEC: a model enabled AFTER the start is synced without restarting the instance =="
# ADR 0072 open question 3. Until now the sidecar exited after the first sync, so enabling a
# model reached the instance only at the next cold start — and on the image role the model
# appears in `generate_image`'s list immediately, so what a member got was a 400 (欠落 8).
rm -rf "$WORK/models"; mkdir -p "$WORK/models"; : > "$WORK/fetched"; : > "$WORK/pending"
ONE='{"v":1,"key":"image","start":"a","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]}]}'
TWO='{"v":1,"key":"image","start":"a","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]},{"id":"c","f":["image/checkpoints/c.safetensors"]}]}'
cat > "$WORK/bin/aws2" <<'STUB'
#!/usr/bin/env bash
# The same stub, except that the active set CHANGES once the marker appears.
if [ "$1 $2" = "ssm get-parameter" ] && [ -f "$AF_TEST_SECOND" ]; then
  printf '%s' "$ACTIVE_SET_FIXTURE2"
  exit 0
fi
exec aws.real "$@"
STUB
chmod +x "$WORK/bin/aws2"
mv "$WORK/bin/aws" "$WORK/bin/aws.real"; mv "$WORK/bin/aws2" "$WORK/bin/aws"
ACTIVE_SET_FIXTURE="$ONE" ACTIVE_SET_FIXTURE2="$TWO" AF_TEST_SECOND="$WORK/second" \
  ALIAS_FLAG="" CTX_FLAG="" PRESET_FILE="" SYNC_ALL=1 WATCH_SEC=1 \
  MODELS_DIR="$WORK/models" BUCKET="b" ACTIVE_PARAM="/af-ws/engines/x/active" \
  PENDING_PARAM=/af-ws/engines/x/pending AF_TEST_PENDING="$WORK/pending" \
  AF_TEST_FETCHED="$WORK/fetched" \
  sh "$SIDECAR" > "$WORK/out" 2>&1 &
watcher=$!
# 🔴 Only ever this PID: the host is shared, and `pkill -f sh` would take somebody else's
# session. And the trap keeps the kill for the REST of the file, not just this block — a
# resident sidecar that outlives a failed assertion goes on writing its key lists, and every
# later run of this script then fails somewhere else (walked into while writing this).
trap 'kill "$watcher" 2>/dev/null || true; wait "$watcher" 2>/dev/null || true; rm -rf "$WORK"' EXIT
for _ in $(seq 1 100); do grep -q "sync done" "$WORK/out" && break; sleep 0.1; done
grep -q "sync done" "$WORK/out" || fail "the first sync never finished: $(cat "$WORK/out")"
grep -q "watching /af-ws/engines/x/active" "$WORK/out" || fail "the sidecar did not stay to watch: $(cat "$WORK/out")"
if grep -q "image/checkpoints/c.safetensors" "$WORK/fetched"; then fail "it fetched a model that was not enabled yet"; fi
touch "$WORK/second"
for _ in $(seq 1 100); do grep -q "image/checkpoints/c.safetensors" "$WORK/fetched" && break; sleep 0.1; done
grep -q "image/checkpoints/c.safetensors" "$WORK/fetched" \
  || fail "a model enabled after the start never reached the instance: $(cat "$WORK/out")"
# It goes onto the pending list while it is coming down and comes off when it lands, which is
# what makes the gateway's wait end by itself.
grep -q "image/checkpoints/c.safetensors" "$WORK/pending" || fail "the new model was never announced as pending"
for _ in $(seq 1 50); do [ "$(tail -1 "$WORK/pending" | cut -d' ' -f2-)" = "[]" ] && break; sleep 0.1; done
[ "$(tail -1 "$WORK/pending" | cut -d' ' -f2-)" = "[]" ] || fail "the pending list never emptied again: $(tail -3 "$WORK/pending")"
kill "$watcher" 2>/dev/null || true
wait "$watcher" 2>/dev/null || true
# A killed shell does not run its EXIT trap, so its scratch directory is this test's to clear.
# Named after the sidecar's own $$, which is the pid that was just killed.
rm -rf "${TMPDIR:-/tmp}/engine-fetch.$watcher"
mv "$WORK/bin/aws.real" "$WORK/bin/aws"

echo "OK: the engine fetch sidecar behaves"

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
# So this pulls the script out of the template as it is actually deployed and runs it, against
# a stub `aws` and the real `jq`, asserting the command line it writes. Two things are checked
# that only running it can check: that it is valid shell at all, and that the flags come out in
# the order the engine expects.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TPL="$ROOT/deploy/aws/ecs/cfn/60-engines.yaml"

command -v jq >/dev/null || { echo "SKIP: jq is not installed" >&2; exit 0; }
command -v python3 >/dev/null || { echo "SKIP: python3 is not installed" >&2; exit 0; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail() { echo "NG: $*" >&2; exit 1; }

# The script exactly as CloudFormation would hand it to `sh -c`.
python3 - "$TPL" > "$WORK/sidecar.sh" <<'PY'
import sys, yaml
class L(yaml.SafeLoader): pass
def multi(loader, suffix, node):
    if isinstance(node, yaml.ScalarNode):   return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode): return loader.construct_sequence(node, deep=True)
    return loader.construct_mapping(node, deep=True)
L.add_multi_constructor('!', multi)
doc = yaml.load(open(sys.argv[1]), Loader=L)
sys.stdout.write(doc["Mappings"]["Engine"]["fetch"]["script"])
PY

echo "== the sidecar is valid shell =="
sh -n "$WORK/sidecar.sh" || fail "the sidecar does not parse as shell"

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
  ACTIVE_SET_FIXTURE="$1" ALIAS_FLAG="$2" CTX_FLAG="$3" PRESET_FILE="${4:-}" SYNC_ALL="${5:-}" \
  MODELS_DIR="$WORK/models" BUCKET="b" ACTIVE_PARAM="/af-ws/engines/x/active" \
  AF_TEST_FETCHED="$WORK/fetched" \
    sh "$WORK/sidecar.sh" > "$WORK/out" 2>&1 || fail "the sidecar exited non-zero: $(cat "$WORK/out")"
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
ACTIVE_SET_FIXTURE="$IMG" ALIAS_FLAG="" CTX_FLAG="" PRESET_FILE="" MODELS_DIR="$WORK/models" BUCKET="b" ACTIVE_PARAM="/af-ws/engines/x/active" AF_TEST_FETCHED="$WORK/fetched" AF_TEST_FAIL_CP=1   sh "$WORK/sidecar.sh" > "$WORK/out" 2>&1 && fail "a failed fetch should not exit 0"
[ -f "$WORK/models/ready" ] || fail "a failed fetch left no marker: the engine would hang for an hour"
[ ! -s "$WORK/models/cmdline" ] || fail "a failed fetch produced a command line: $(cat "$WORK/models/cmdline")"

echo "OK: the engine fetch sidecar behaves"

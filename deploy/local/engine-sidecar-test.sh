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
    mkdir -p "$(dirname "$dst")"
    printf 'weights' > "$dst"
    echo "$3" >> "$AF_TEST_FETCHED"
    ;;
  *) echo "stub aws: unexpected $*" >&2; exit 2 ;;
esac
STUB
chmod +x "$WORK/bin/aws"
export PATH="$WORK/bin:$PATH"

run() { # run <fixture> <alias-flag> <ctx-flag> -> writes $WORK/models/cmdline
  rm -rf "$WORK/models"; mkdir -p "$WORK/models"
  : > "$WORK/fetched"
  ACTIVE_SET_FIXTURE="$1" ALIAS_FLAG="$2" CTX_FLAG="$3" \
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

echo "== the llm role: --alias and -c come from the catalogue =="
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

echo "== a start id that names nothing leaves an EMPTY command line, not a broken one =="
# The engine then idles instead of being handed half an argument list. Reachable when a row is
# deleted between the publish and the box coming up.
GONE='{"v":1,"key":"image","start":"gone","models":[{"id":"a","f":["image/checkpoints/a.safetensors"]}]}'
run "$GONE" "" ""
[ ! -s "$WORK/models/cmdline" ] || fail "a missing start id produced '$(cat "$WORK/models/cmdline")'"

echo "OK: the engine fetch sidecar behaves"

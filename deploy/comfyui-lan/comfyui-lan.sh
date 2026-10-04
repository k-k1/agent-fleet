#!/usr/bin/env bash
# Run the fleet's pinned ComfyUI image on a GPU host on your own network, for a Control
# Plane that reaches it through AF_COMFY_URL or the LAN ComfyUI panel (ADR 0076, P2).
#
#   deploy/comfyui-lan/comfyui-lan.sh up --models /srv/comfy-models --bind 192.0.2.10
#   deploy/comfyui-lan/comfyui-lan.sh up ... --api-key-file ~/.config/af-comfy.key
#   deploy/comfyui-lan/comfyui-lan.sh status
#   deploy/comfyui-lan/comfyui-lan.sh down
#
# The image is the one the ecs-ec2 image role runs: ghcr.io/k-k1/agent-fleet/comfyui at the
# tag 60-engines.yaml pins as ImageComfyImageTag, read from this checkout. Upgrading is
# `git pull` and the same `up` again; the container is recreated only when the image or
# a setting changed. Guide: guide/operate/07-image-engine.md, "Running the fleet's image".
#
# Every option also reads an environment variable (in brackets), so a host can keep its
# settings in one file and source it before each `up`.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ENGINES_YAML="$ROOT/deploy/aws/ecs/cfn/60-engines.yaml"
DOCKERFILE_DIR="$ROOT/deploy/aws/ecs/comfyui"

REGISTRY="${AF_COMFY_LAN_REGISTRY:-ghcr.io/k-k1/agent-fleet/comfyui}"
# caddy:2-alpine is what deploy/compose already runs; one proxy image across the targets.
PROXY_IMAGE="${AF_COMFY_LAN_PROXY_IMAGE:-caddy:2-alpine}"
NAME="${AF_COMFY_LAN_NAME:-af-comfyui-lan}"
MODELS="${AF_COMFY_LAN_MODELS:-}"
BIND="${AF_COMFY_LAN_BIND:-127.0.0.1}"
PORT="${AF_COMFY_LAN_PORT:-8188}"
KEY_FILE="${AF_COMFY_LAN_KEY_FILE:-}"
TAG="${AF_COMFY_LAN_TAG:-}"
DIGEST="${AF_COMFY_LAN_DIGEST:-}"
GPUS="${AF_COMFY_LAN_GPUS:-all}"
BUILD=0; PULL=0; ALL_IFACES=0
# Inside the container ComfyUI always listens here; only the published side is configurable.
INNER_PORT=8188
# The folders ComfyUI's loaders read that the fleet's workflows name (ADR 0076 decision 6).
# text_encoders/ is clip/'s newer name; ComfyUI lists both under the same loader.
TYPE_DIRS="checkpoints diffusion_models clip text_encoders vae loras"
SPEC_LABEL=af.comfyui-lan.spec
# Set on every container and network this script creates, valued with --name. Only objects
# carrying it are ever removed or replaced; a same-named object without it is someone else's.
OWNER_LABEL=af.comfyui-lan.owner

usage() {
  cat <<'EOF'
usage: comfyui-lan.sh up --models <dir> [options]
       comfyui-lan.sh status | down [--name <name>]

up options:
  --models <dir>        host folder mounted read-only as ComfyUI's models/ [AF_COMFY_LAN_MODELS]
  --bind <addr>         host address to publish on (default 127.0.0.1) [AF_COMFY_LAN_BIND]
  --all-interfaces      required to accept --bind 0.0.0.0 / :: (every interface)
  --port <n>            published port (default 8188) [AF_COMFY_LAN_PORT]
  --api-key-file <f>    put a bearer-checking proxy in front; the file holds the key
                        the CP sends as AF_COMFY_API_KEY [AF_COMFY_LAN_KEY_FILE]
  --tag <t>             image tag (default: ImageComfyImageTag in 60-engines.yaml) [AF_COMFY_LAN_TAG]
  --digest <sha256:..>  pin the pulled image by digest [AF_COMFY_LAN_DIGEST]
  --build               build from deploy/aws/ecs/comfyui/Dockerfile at --tag instead of pulling
  --pull                pull (or rebuild) even when present; also re-pulls the proxy image
  --gpus <spec>         value for docker run --gpus (default all) [AF_COMFY_LAN_GPUS]
  --name <name>         container name (default af-comfyui-lan) [AF_COMFY_LAN_NAME]
EOF
}

die() { echo "comfyui-lan: $*" >&2; exit 1; }

CMD="${1:-}"
case "$CMD" in
  up|down|status) shift ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --models) MODELS="${2:?--models needs a directory}"; shift 2 ;;
    --bind) BIND="${2:?--bind needs an address}"; shift 2 ;;
    --all-interfaces) ALL_IFACES=1; shift ;;
    --port) PORT="${2:?--port needs a number}"; shift 2 ;;
    --api-key-file) KEY_FILE="${2:?--api-key-file needs a path}"; shift 2 ;;
    --tag) TAG="${2:?--tag needs a value}"; shift 2 ;;
    --digest) DIGEST="${2:?--digest needs sha256:<64 hex>}"; shift 2 ;;
    --build) BUILD=1; shift ;;
    --pull) PULL=1; shift ;;
    --gpus) GPUS="${2:?--gpus needs a value}"; shift 2 ;;
    --name) NAME="${2:?--name needs a value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "comfyui-lan: unknown argument '$1'" >&2; usage >&2; exit 2 ;;
  esac
done
PROXY_NAME="$NAME-proxy"
NET="$NAME"

# --- preconditions shared by every command ----------------------------------------------

command -v docker >/dev/null 2>&1 \
  || die "docker is not installed (or not on PATH). Install Docker Engine on this GPU host first."
if ! docker info >/dev/null 2>&1; then
  die "docker is installed but the daemon did not answer 'docker info'. Is it running, and is this user in the docker group (try: sg docker -c \"$0 $CMD ...\")?"
fi

# container_field <container> <go-template> — empty when the container does not exist.
container_field() { docker container inspect -f "$2" "$1" 2>/dev/null || true; }

# refuse_foreign — die, before anything is changed, when a container or network under one of
# our names exists without our owner label (or with another --name's). Every name is checked
# first so a mixed set is never half removed.
refuse_foreign() {
  local bad="" o c
  for c in "$NAME" "$PROXY_NAME"; do
    if o="$(docker container inspect -f "{{with index .Config.Labels \"$OWNER_LABEL\"}}{{.}}{{else}}-{{end}}" "$c" 2>/dev/null)" \
        && [ "$o" != "$NAME" ]; then
      bad="$bad container '$c' (owner label: $o);"
    fi
  done
  if o="$(docker network inspect -f "{{with index .Labels \"$OWNER_LABEL\"}}{{.}}{{else}}-{{end}}" "$NET" 2>/dev/null)" \
      && [ "$o" != "$NAME" ]; then
    bad="$bad network '$NET' (owner label: $o);"
  fi
  [ -z "$bad" ] || die "refusing to touch objects this script did not create:$bad remove or rename them yourself, or pick another --name."
}

if [ "$CMD" = down ]; then
  refuse_foreign
  for c in "$PROXY_NAME" "$NAME"; do
    if [ -n "$(container_field "$c" '{{.Id}}')" ]; then
      docker rm -f "$c" >/dev/null
      echo "==> removed $c"
    fi
  done
  if docker network inspect "$NET" >/dev/null 2>&1; then
    docker network rm "$NET" >/dev/null
  fi
  echo "==> down. The models folder and the pulled image are left in place."
  exit 0
fi

if [ "$CMD" = status ]; then
  if [ -z "$(container_field "$NAME" '{{.Id}}')" ]; then
    echo "$NAME: not created (run: $0 up --models <dir> ...)"; exit 3
  fi
  echo "$NAME: $(container_field "$NAME" '{{.State.Status}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} image={{.Config.Image}}')"
  echo "  spec: $(container_field "$NAME" "{{index .Config.Labels \"$SPEC_LABEL\"}}")"
  if [ -n "$(container_field "$PROXY_NAME" '{{.Id}}')" ]; then
    echo "$PROXY_NAME: $(container_field "$PROXY_NAME" '{{.State.Status}}')"
  fi
  exit 0
fi

# --- up: validate everything before touching docker state -------------------------------

arch="$(uname -m)"
[ "$arch" = x86_64 ] || die "this host is $arch; the pinned ComfyUI image is built for amd64 (x86_64) only."

[ -n "$MODELS" ] || die "--models <dir> is required: the host folder holding checkpoints/, diffusion_models/, clip/, vae/, loras/."
[ -d "$MODELS" ] || die "--models '$MODELS' is not a directory. Create it first; this script does not guess a path."
MODELS="$(cd "$MODELS" && pwd -P)"

[[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "--port '$PORT' is not a TCP port."

# is_unspecified <addr> — true when docker would read the address as "all interfaces":
# 0.0.0.0 in any zero-padding, and any IPv6 whose value is all zero or the IPv4-mapped
# zero (::ffff:0.0.0.0), because docker unmaps a mapped host address to IPv4. The IPv6 form
# is expanded to its eight hextets and judged by value, because matching spellings misses
# the compressed ones (0:0:0:0:0:ffff:: is the mapped zero too).
# Anything that does not parse returns false and is left for docker to reject.
is_unspecified() {
  local a="${1#[}" left right h=() l=() r=() x i
  a="${a%]}"; a="${a,,}"
  [ -n "$a" ] || return 0
  if [[ "$a" != *:* ]]; then
    [[ "$a" =~ ^0+(\.0+){3}$ ]]
    return
  fi
  # A dotted IPv4 tail is two hextets.
  if [[ "$a" =~ ^(.*:)([0-9]+)\.([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    for i in 2 3 4 5; do (( 10#${BASH_REMATCH[$i]} <= 255 )) || return 1; done
    a="${BASH_REMATCH[1]}$(printf '%x:%x' $(( 10#${BASH_REMATCH[2]} * 256 + 10#${BASH_REMATCH[3]} )) \
      $(( 10#${BASH_REMATCH[4]} * 256 + 10#${BASH_REMATCH[5]} )))"
  fi
  if [[ "$a" == *::* ]]; then
    left="${a%%::*}"; right="${a#*::}"
    [[ "$right" != *::* ]] || return 1
    [ -z "$left" ] || IFS=: read -ra l <<< "$left"
    [ -z "$right" ] || IFS=: read -ra r <<< "$right"
    (( ${#l[@]} + ${#r[@]} <= 7 )) || return 1
    h=("${l[@]}")
    for (( i = ${#l[@]} + ${#r[@]}; i < 8; i++ )); do h+=(0); done
    h+=("${r[@]}")
  else
    IFS=: read -ra h <<< "$a"
    [[ "$a" != *: ]] || return 1
  fi
  (( ${#h[@]} == 8 )) || return 1
  for x in "${h[@]}"; do [[ "$x" =~ ^[0-9a-f]{1,4}$ ]] || return 1; done
  for i in 0 1 2 3 4 6 7; do (( 16#${h[$i]} == 0 )) || return 1; done
  (( 16#${h[5]} == 0 || 16#${h[5]} == 0xffff ))
}
if is_unspecified "$BIND"; then
  [ "$ALL_IFACES" = 1 ] || die "--bind '$BIND' publishes ComfyUI on EVERY interface of this host. Give the LAN address the Control Plane reaches (e.g. --bind 192.0.2.10), or pass --all-interfaces to mean it."
  case "$BIND" in *:*) BIND=:: ;; *) BIND=0.0.0.0 ;; esac
  echo "comfyui-lan: WARNING publishing on every interface of this host (--all-interfaces)." >&2
fi
# docker -p wants an IPv6 host address in brackets.
publish_host="$BIND"
case "$BIND" in *:*) publish_host="[${BIND#[}"; publish_host="${publish_host%]}]" ;; esac

KEY=""
if [ -n "$KEY_FILE" ]; then
  [ -f "$KEY_FILE" ] && [ -r "$KEY_FILE" ] || die "--api-key-file '$KEY_FILE' is not a readable file."
  # A key any local user can read lets them past the proxy, which is the only thing keeping
  # "only the Control Plane" true (ADR 0076 decision 7).
  key_mode="$(stat -L -c %a "$KEY_FILE")"
  if (( 8#$key_mode & 8#044 )); then
    die "--api-key-file '$KEY_FILE' is readable by group or others (mode $key_mode). Run: chmod 600 '$KEY_FILE'"
  fi
  KEY="$(tr -d '\r\n' < "$KEY_FILE")"
  # The key lands in a Caddyfile placeholder and an Authorization header: no spaces or quotes.
  [[ "$KEY" =~ ^[A-Za-z0-9._~+/=-]{24,}$ ]] \
    || die "the key in '$KEY_FILE' must be at least 24 characters of [A-Za-z0-9._~+/=-] (e.g. (umask 077; openssl rand -hex 32 > comfy.key))."
  [ -f "$HERE/Caddyfile" ] || die "$HERE/Caddyfile is missing; the proxy needs it."
elif [ "$BIND" != 127.0.0.1 ] && [ "$BIND" != ::1 ]; then
  echo "comfyui-lan: note: no --api-key-file, so anything that reaches $BIND:$PORT can queue prompts (ADR 0076 decision 7)." >&2
fi

if [ -n "$DIGEST" ]; then
  [[ "$DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || die "--digest wants sha256:<64 lowercase hex> (got '$DIGEST')."
  [ "$BUILD" = 0 ] || die "--digest pins a pulled image; it cannot be combined with --build."
fi
if [ -z "$TAG" ]; then
  [ -f "$ENGINES_YAML" ] || die "$ENGINES_YAML not found; run this from an agent-fleet checkout or pass --tag."
  # Same reading as af_cfn_param_default in deploy/aws/ecs/env.sh, which update.sh uses.
  TAG="$(sed -n '/^  ImageComfyImageTag:[[:space:]]*$/,/^  [A-Za-z]/p' "$ENGINES_YAML" \
    | sed -n 's/^[[:space:]]*Default:[[:space:]]*//p' | head -1 | tr -d "\"'")"
  [ -n "$TAG" ] || die "could not read ImageComfyImageTag's Default from $ENGINES_YAML; pass --tag."
fi
[[ "$TAG" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || die "'$TAG' is not a usable image tag."

# GPU: the driver answers, and docker has the NVIDIA container toolkit that makes --gpus work.
command -v nvidia-smi >/dev/null 2>&1 \
  || die "nvidia-smi not found: this host has no NVIDIA driver installed. ComfyUI here needs an NVIDIA GPU."
nvidia-smi -L >/dev/null 2>&1 \
  || die "nvidia-smi is installed but failed to list a GPU (driver not loaded, or no device). Fix 'nvidia-smi -L' first."
toolkit=0
for t in nvidia-container-runtime-hook nvidia-ctk nvidia-container-cli; do
  if command -v "$t" >/dev/null 2>&1; then toolkit=1; break; fi
done
if [ "$toolkit" = 0 ] && docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q nvidia; then
  toolkit=1
fi
[ "$toolkit" = 1 ] || die "the NVIDIA Container Toolkit is not installed, so 'docker run --gpus' cannot reach the GPU. Install nvidia-container-toolkit and run 'nvidia-ctk runtime configure --runtime=docker', then restart docker."

refuse_foreign

# --- image ------------------------------------------------------------------------------

if [ "$BUILD" = 1 ]; then
  IMAGE="af-comfyui-lan:$TAG"
  if [ "$PULL" = 1 ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "==> building $IMAGE from $DOCKERFILE_DIR (COMFYUI_REF=$TAG)"
    docker build --build-arg "COMFYUI_REF=$TAG" -t "$IMAGE" "$DOCKERFILE_DIR"
  fi
else
  IMAGE="$REGISTRY:$TAG"
  [ -z "$DIGEST" ] || IMAGE="$REGISTRY@$DIGEST"
  if [ "$PULL" = 1 ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "==> pulling $IMAGE (about 5 GB the first time)"
    docker pull "$IMAGE"
  fi
fi
image_id="$(docker image inspect -f '{{.Id}}' "$IMAGE")"
[ -n "$image_id" ] || die "docker has no image id for $IMAGE after pulling/building it."

# The proxy's image id and its Caddyfile's content go into its spec: Caddy reads the file once
# at start (admin off, no reload), so a changed file or a re-pulled caddy:2-alpine needs a
# recreate that a spec of names alone would never trigger.
proxy_id=""; caddy_hash=""
if [ -n "$KEY" ]; then
  if [ "$PULL" = 1 ] || ! docker image inspect "$PROXY_IMAGE" >/dev/null 2>&1; then
    echo "==> pulling $PROXY_IMAGE"
    docker pull "$PROXY_IMAGE"
  fi
  proxy_id="$(docker image inspect -f '{{.Id}}' "$PROXY_IMAGE")"
  [ -n "$proxy_id" ] || die "docker has no image id for $PROXY_IMAGE after pulling it."
  caddy_hash="$(sha256sum < "$HERE/Caddyfile" | cut -c1-16)"
fi

for d in $TYPE_DIRS; do
  if [ ! -d "$MODELS/$d" ] && ! mkdir -p "$MODELS/$d" 2>/dev/null; then
    echo "comfyui-lan: note: could not create $MODELS/$d (not writable); ComfyUI lists it as empty." >&2
  fi
done

# --- containers -------------------------------------------------------------------------

# Without a key the proxy has to go before ComfyUI is published: it holds the same host port,
# and docker refuses the second -p with "port is already allocated".
if [ -z "$KEY" ] && [ -n "$(container_field "$PROXY_NAME" '{{.Id}}')" ]; then
  docker rm -f "$PROXY_NAME" >/dev/null
  echo "==> removed $PROXY_NAME (no --api-key-file this time)"
fi

if ! docker network inspect "$NET" >/dev/null 2>&1; then
  docker network create --label "$OWNER_LABEL=$NAME" "$NET" >/dev/null
fi

# ensure <name> <spec> <docker run args...> — leave a container whose spec label matches
# alone (start it if stopped), replace one whose spec differs, create one that is missing.
# The spec is every input that shapes the container, so a re-run of the same `up` is a no-op
# and a new pin, port or key is a recreate.
ensure() {
  local name="$1" spec="$2"; shift 2
  local have running
  have="$(container_field "$name" "{{index .Config.Labels \"$SPEC_LABEL\"}}")"
  running="$(container_field "$name" '{{.State.Running}}')"
  if [ -n "$running" ] && [ "$have" = "$spec" ]; then
    if [ "$running" = true ]; then
      echo "==> $name is up to date"
    else
      docker start "$name" >/dev/null
      echo "==> $name started"
    fi
    return 0
  fi
  if [ -n "$running" ]; then
    echo "==> $name changed; recreating"
    docker rm -f "$name" >/dev/null
  fi
  docker run -d --name "$name" --label "$OWNER_LABEL=$NAME" --label "$SPEC_LABEL=$spec" --network "$NET" \
    --restart unless-stopped --log-opt max-size=50m --log-opt max-file=3 "$@" >/dev/null
  echo "==> $name created"
}

comfy_publish=()
key_hash=none
if [ -n "$KEY" ]; then
  key_hash="$(printf '%s' "$KEY" | sha256sum | cut -c1-16)"
else
  comfy_publish=(-p "$publish_host:$PORT:$INNER_PORT")
fi
comfy_spec="image=$image_id models=$MODELS gpus=$GPUS publish=${comfy_publish[*]:-proxy}"

# The ECS task runs the same main.py with the same flags (60-engines.yaml, ImageTaskDef);
# 0.0.0.0 is the container's own interface — what reaches it from the host is the -p above
# or the proxy, never this flag.
ensure "$NAME" "$comfy_spec" \
  --gpus "$GPUS" \
  -v "$MODELS:/ComfyUI/models:ro" \
  --health-cmd "python3 -c \"import urllib.request; urllib.request.urlopen('http://127.0.0.1:$INNER_PORT/system_stats', timeout=5)\"" \
  --health-interval 30s --health-timeout 10s --health-start-period 180s --health-retries 3 \
  "${comfy_publish[@]}" \
  "$IMAGE" python3 /ComfyUI/main.py --listen 0.0.0.0 --port "$INNER_PORT" --disable-auto-launch

if [ -n "$KEY" ]; then
  proxy_spec="image=$proxy_id ref=$PROXY_IMAGE caddyfile=$HERE/Caddyfile@$caddy_hash upstream=$NAME:$INNER_PORT publish=$publish_host:$PORT key=$key_hash"
  # -e NAME without a value hands docker the variable from this environment, so the key never
  # appears on a command line (ps).
  AF_COMFY_LAN_KEY="$KEY" AF_COMFY_LAN_UPSTREAM="$NAME:$INNER_PORT" ensure "$PROXY_NAME" "$proxy_spec" \
    -e AF_COMFY_LAN_KEY -e AF_COMFY_LAN_UPSTREAM \
    -v "$HERE/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -p "$publish_host:$PORT:$INNER_PORT" \
    "$PROXY_IMAGE"
fi

url_host="$BIND"; [ "$BIND" = 0.0.0.0 ] && url_host="<this host's LAN address>"
case "$url_host" in *:*) url_host="[${url_host#[}"; url_host="${url_host%]}]" ;; esac
echo
echo "ComfyUI ($IMAGE) will answer on http://$url_host:$PORT once it has loaded (a minute or two)."
echo "Point the Control Plane at it: Admin > Inference engines > LAN ComfyUI, or AF_COMFY_URL=http://$url_host:$PORT"
if [ -n "$KEY" ]; then
  echo "and give it the same key (the panel's key field, or AF_COMFY_API_KEY) - every path, /system_stats included, needs it."
fi
echo "Check: $0 status"

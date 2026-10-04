#!/usr/bin/env bash
# Stub test for deploy/comfyui-lan/comfyui-lan.sh (ADR 0076 P2). No docker and no GPU: a
# fake `docker` keeps containers, images and networks as files, so a second `up` sees what
# the first one left and idempotency is asserted, not assumed. `nvidia-smi`, the toolkit
# probe and `uname` are stubs too.
#
# PATH is cut down to the stubs plus a hand-picked set of coreutils: a CI runner has a real
# docker (and could have a real nvidia-ctk), and a refusal test that finds the real one
# passes on a laptop and fails on the runner.
#
# Not measured here: that the pinned image really starts ComfyUI under --gpus on a real
# NVIDIA host, that the Caddyfile accepts the bearer, and the health command's output.
# shellcheck disable=SC2015 # `cond && ok … || ng …`: ok only echoes and cannot fail.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$ROOT/deploy/comfyui-lan/comfyui-lan.sh"
YAML="$ROOT/deploy/aws/ecs/cfn/60-engines.yaml"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/stub"; SYS="$WORK/sys"; STATE="$WORK/state"; LOG="$WORK/calls.log"
mkdir -p "$STUB" "$SYS" "$STATE/c" "$STATE/img" "$STATE/net"

for t in bash env sed head tr sha256sum cut mkdir grep cat rm dirname ls; do
  p="$(command -v "$t")" || { echo "test needs $t"; exit 1; }
  ln -s "$p" "$SYS/$t"
done

# fake docker. Mutating calls are logged one per line; `inspect` / `info` reads are logged
# with a "read " prefix so the exact-call assertions can filter them out.
cat > "$STUB/docker" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
S="$STUB_STATE"
key() { printf '%s' "$1" | tr '/:@' '___'; }
case "$1" in
  info)
    echo "read docker $*" >> "$STUB_LOG"
    [ "${STUB_DAEMON_DOWN:-0}" = 0 ] || exit 1
    case "$*" in *Runtimes*) echo "${STUB_RUNTIMES:-{\"runc\":{}\}}" ;; esac ;;
  container)
    echo "read docker $*" >> "$STUB_LOG"
    tmpl="$4"; c="$5"; d="$S/c/$c"
    [ -d "$d" ] || { echo "Error: No such container: $c" >&2; exit 1; }
    case "$tmpl" in
      *Labels*) cat "$d/spec" ;;
      *State.Running*) cat "$d/running" ;;
      *.Id*) echo "cid-$c" ;;
      *) echo "running" ;;
    esac ;;
  image)
    echo "read docker $*" >> "$STUB_LOG"
    ref="${!#}"; f="$S/img/$(key "$ref")"
    [ -f "$f" ] || exit 1
    case "$*" in *-f*) cat "$f" ;; esac ;;
  network)
    case "$2" in
      inspect) echo "read docker $*" >> "$STUB_LOG"; [ -f "$S/net/$3" ] ;;
      create) echo "docker $*" >> "$STUB_LOG"; : > "$S/net/$3" ;;
      rm) echo "docker $*" >> "$STUB_LOG"; rm -f "$S/net/$3" ;;
    esac ;;
  pull)
    echo "docker $*" >> "$STUB_LOG"
    echo "sha256:${STUB_PULL_ID:-aaaa}" > "$S/img/$(key "$2")" ;;
  build)
    echo "docker $*" >> "$STUB_LOG"
    shift; ref=""
    while [ $# -gt 0 ]; do [ "$1" = -t ] && ref="$2"; shift; done
    echo "sha256:built" > "$S/img/$(key "$ref")" ;;
  run)
    # Logged with the env the -e flags would hand over, so a test can see the key travel
    # by environment and never on the command line.
    echo "docker $*" >> "$STUB_LOG"
    [ -z "${AF_COMFY_LAN_KEY:-}" ] || echo "env AF_COMFY_LAN_KEY=$AF_COMFY_LAN_KEY" >> "$S/env.log"
    name=""; spec=""; args=("$@")
    for ((i=0; i<${#args[@]}; i++)); do
      case "${args[$i]}" in
        --name) name="${args[$((i+1))]}" ;;
        --label) spec="${args[$((i+1))]#af.comfyui-lan.spec=}" ;;
      esac
    done
    mkdir -p "$S/c/$name"; printf '%s\n' "$spec" > "$S/c/$name/spec"; echo true > "$S/c/$name/running" ;;
  start) echo "docker $*" >> "$STUB_LOG"; echo true > "$S/c/$2/running" ;;
  rm) echo "docker $*" >> "$STUB_LOG"; rm -rf "${S:?}/c/$3" ;;
  *) echo "unexpected docker $*" >> "$STUB_LOG"; exit 99 ;;
esac
FAKE
cat > "$STUB/nvidia-smi" <<'FAKE'
#!/usr/bin/env bash
echo "read nvidia-smi $*" >> "$STUB_LOG"
exit "${STUB_NVIDIA_RC:-0}"
FAKE
cat > "$STUB/uname" <<'FAKE'
#!/usr/bin/env bash
echo "${STUB_ARCH:-x86_64}"
FAKE
printf '#!/usr/bin/env bash\nexit 0\n' > "$STUB/nvidia-ctk"
chmod +x "$STUB"/*

MODELS="$WORK/models"; mkdir -p "$MODELS"
MODELS_REAL="$(cd "$MODELS" && pwd -P)"
TAG="$(sed -n '/^  ImageComfyImageTag:/,/Default:/s/^ *Default: *//p' "$YAML")"
[ -n "$TAG" ] || { echo "could not read the pin from $YAML"; exit 1; }
IMG="ghcr.io/k-k1/agent-fleet/comfyui:$TAG"

pass=0; fail=0
ok() { pass=$((pass+1)); echo "  ok   $1"; }
ng() { fail=$((fail+1)); echo "  FAIL $1"; }

OUT="$WORK/out.txt"
# run <args...> — the script under the cut-down PATH; RC holds the exit code. The log is
# cleared first so every assertion is about this call only.
run() {
  : > "$LOG"
  set +e
  env -i HOME="$WORK" PATH="$STUB:$SYS" STUB_LOG="$LOG" STUB_STATE="$STATE" \
    ${STUB_ARCH:+STUB_ARCH=$STUB_ARCH} ${STUB_NVIDIA_RC:+STUB_NVIDIA_RC=$STUB_NVIDIA_RC} \
    ${STUB_DAEMON_DOWN:+STUB_DAEMON_DOWN=$STUB_DAEMON_DOWN} ${STUB_PULL_ID:+STUB_PULL_ID=$STUB_PULL_ID} \
    ${STUB_RUNTIMES:+STUB_RUNTIMES=$STUB_RUNTIMES} \
    bash "$SCRIPT" "$@" > "$OUT" 2>&1
  RC=$?
  set -e
}
writes() { grep -v '^read ' "$LOG" || true; }
expect_writes() { # <label> <expected multi-line>
  if [ "$(writes)" = "$2" ]; then ok "$1"; else
    ng "$1"; echo "--- want"; echo "$2"; echo "--- got"; writes; echo "--- output"; cat "$OUT"; fi
}
expect_refused() { # <label> <message substring> <args...>
  local label="$1" msg="$2"; shift 2
  run "$@"
  if [ "$RC" != 0 ] && grep -qF -- "$msg" "$OUT" && [ -z "$(writes)" ]; then ok "$label"
  else ng "$label (rc=$RC)"; cat "$OUT"; writes; fi
}
reset_state() { rm -rf "${STATE:?}"/c/* "${STATE:?}"/img/* "${STATE:?}"/net/* "$STATE/env.log"; }

HC="python3 -c \"import urllib.request; urllib.request.urlopen('http://127.0.0.1:8188/system_stats', timeout=5)\""
RUNCOMMON="--network af-comfyui-lan --restart unless-stopped --log-opt max-size=50m --log-opt max-file=3 --gpus all -v $MODELS_REAL:/ComfyUI/models:ro --health-cmd $HC --health-interval 30s --health-timeout 10s --health-start-period 180s --health-retries 3"
comfy_run() { # <publish args or empty> <image> <spec>
  echo "docker run -d --name af-comfyui-lan --label af.comfyui-lan.spec=$3 $RUNCOMMON ${1:+$1 }$2 python3 /ComfyUI/main.py --listen 0.0.0.0 --port 8188 --disable-auto-launch"
}

echo "== up: fresh host, defaults (loopback, the 60-engines pin)"
run up --models "$MODELS"
spec="image=sha256:aaaa models=$MODELS_REAL gpus=all publish=-p 127.0.0.1:8188:8188"
expect_writes "pulls $IMG, creates the network and one container" "docker pull $IMG
docker network create af-comfyui-lan
$(comfy_run "-p 127.0.0.1:8188:8188" "$IMG" "$spec")"
[ "$RC" = 0 ] && ok "exit 0" || { ng "exit $RC"; cat "$OUT"; }
missing=""
for d in checkpoints diffusion_models clip text_encoders vae loras; do [ -d "$MODELS/$d" ] || missing="$missing $d"; done
[ -z "$missing" ] && ok "type folders created" || ng "type folders missing:$missing"

echo "== up again with the same arguments: nothing to do"
run up --models "$MODELS"
expect_writes "no pull, no run, no rm" ""
grep -q "af-comfyui-lan is up to date" "$OUT" && ok "says up to date" || ng "no up-to-date line"

echo "== container stopped: up starts it, does not recreate"
echo false > "$STATE/c/af-comfyui-lan/running"
run up --models "$MODELS"
expect_writes "docker start only" "docker start af-comfyui-lan"

echo "== a different port recreates"
run up --models "$MODELS" --port 8190
spec2="image=sha256:aaaa models=$MODELS_REAL gpus=all publish=-p 127.0.0.1:8190:8188"
expect_writes "rm -f then run with the new port" "docker rm -f af-comfyui-lan
$(comfy_run "-p 127.0.0.1:8190:8188" "$IMG" "$spec2")"

echo "== upgrade: --pull brings a new image id, so the container is recreated"
STUB_PULL_ID=bbbb run up --models "$MODELS" --port 8190 --pull
spec3="image=sha256:bbbb models=$MODELS_REAL gpus=all publish=-p 127.0.0.1:8190:8188"
expect_writes "pull, rm -f, run" "docker pull $IMG
docker rm -f af-comfyui-lan
$(comfy_run "-p 127.0.0.1:8190:8188" "$IMG" "$spec3")"
STUB_PULL_ID=bbbb run up --models "$MODELS" --port 8190 --pull
expect_writes "a pull that returns the same image changes nothing else" "docker pull $IMG"

echo "== --api-key-file: ComfyUI loses its port, the proxy takes it"
KEYF="$WORK/key"; K1="k1-0123456789abcdef0123456789abcdef"; echo "$K1" > "$KEYF"
reset_state
run up --models "$MODELS" --bind 192.0.2.10 --api-key-file "$KEYF"
h1="$(printf '%s' "$K1" | sha256sum | cut -c1-16)"
cspec="image=sha256:aaaa models=$MODELS_REAL gpus=all publish=proxy"
pspec="image=caddy:2-alpine upstream=af-comfyui-lan:8188 publish=192.0.2.10:8188 key=$h1"
expect_writes "comfy without -p, proxy with -e and -p" "docker pull $IMG
docker network create af-comfyui-lan
$(comfy_run "" "$IMG" "$cspec")
docker run -d --name af-comfyui-lan-proxy --label af.comfyui-lan.spec=$pspec --network af-comfyui-lan --restart unless-stopped --log-opt max-size=50m --log-opt max-file=3 -e AF_COMFY_LAN_KEY -e AF_COMFY_LAN_UPSTREAM -v $ROOT/deploy/comfyui-lan/Caddyfile:/etc/caddy/Caddyfile:ro -p 192.0.2.10:8188:8188 caddy:2-alpine"
if grep -qF "$K1" "$LOG" || grep -qF "$K1" "$OUT"; then ng "key leaked onto a command line or the output"; else ok "key on no command line"; fi
grep -qxF "env AF_COMFY_LAN_KEY=$K1" "$STATE/env.log" && ok "key handed over by environment" || ng "proxy did not get the key"
run up --models "$MODELS" --bind 192.0.2.10 --api-key-file "$KEYF"
expect_writes "same key again: no-op" ""
K2="k2-fedcba9876543210fedcba9876543210"; echo "$K2" > "$KEYF"
run up --models "$MODELS" --bind 192.0.2.10 --api-key-file "$KEYF"
h2="$(printf '%s' "$K2" | sha256sum | cut -c1-16)"
expect_writes "new key: only the proxy is recreated" "docker rm -f af-comfyui-lan-proxy
docker run -d --name af-comfyui-lan-proxy --label af.comfyui-lan.spec=${pspec%key=*}key=$h2 --network af-comfyui-lan --restart unless-stopped --log-opt max-size=50m --log-opt max-file=3 -e AF_COMFY_LAN_KEY -e AF_COMFY_LAN_UPSTREAM -v $ROOT/deploy/comfyui-lan/Caddyfile:/etc/caddy/Caddyfile:ro -p 192.0.2.10:8188:8188 caddy:2-alpine"
run up --models "$MODELS" --bind 192.0.2.10
expect_writes "key dropped: comfy republished, proxy removed" "docker rm -f af-comfyui-lan
$(comfy_run "-p 192.0.2.10:8188:8188" "$IMG" "image=sha256:aaaa models=$MODELS_REAL gpus=all publish=-p 192.0.2.10:8188:8188")
docker rm -f af-comfyui-lan-proxy"
grep -q "can queue prompts" "$OUT" && ok "warns that a LAN bind without a key is open" || ng "no open-LAN note"

echo "== image sources"
reset_state
run up --models "$MODELS" --build
expect_writes "--build builds the Dockerfile at the pin, no pull" "docker build --build-arg COMFYUI_REF=$TAG -t af-comfyui-lan:$TAG $ROOT/deploy/aws/ecs/comfyui
docker network create af-comfyui-lan
$(comfy_run "-p 127.0.0.1:8188:8188" "af-comfyui-lan:$TAG" "image=sha256:built models=$MODELS_REAL gpus=all publish=-p 127.0.0.1:8188:8188")"
reset_state
D="sha256:$(printf 'c%.0s' $(seq 64))"
run up --models "$MODELS" --digest "$D" --tag v9.9.9 --gpus device=0
expect_writes "--digest pulls by digest; --gpus is passed through" "docker pull ghcr.io/k-k1/agent-fleet/comfyui@$D
docker network create af-comfyui-lan
$(comfy_run "-p 127.0.0.1:8188:8188" "ghcr.io/k-k1/agent-fleet/comfyui@$D" "image=sha256:aaaa models=$MODELS_REAL gpus=device=0 publish=-p 127.0.0.1:8188:8188" | sed 's/--gpus all/--gpus device=0/')"
reset_state
run up --models "$MODELS" --bind fd00::10
grep -q -- "-p \[fd00::10\]:8188:8188" "$LOG" && ok "IPv6 bind is bracketed for -p" || { ng "IPv6 bind"; writes; }

echo "== every interface only when asked"
reset_state
run up --models "$MODELS" --bind 0.0.0.0 --all-interfaces
grep -q -- "-p 0.0.0.0:8188:8188" "$LOG" && grep -q WARNING "$OUT" && ok "--all-interfaces publishes on 0.0.0.0 with a warning" || { ng "--all-interfaces"; cat "$OUT"; }

echo "== refusals (no docker state may change)"
reset_state
expect_refused "0.0.0.0 without --all-interfaces" "EVERY interface" up --models "$MODELS" --bind 0.0.0.0
expect_refused ":: without --all-interfaces" "EVERY interface" up --models "$MODELS" --bind ::
expect_refused "no --models" "--models <dir> is required" up
expect_refused "models dir missing" "is not a directory" up --models "$WORK/nope"
expect_refused "bad port" "is not a TCP port" up --models "$MODELS" --port 99999
expect_refused "bad digest" "--digest wants" up --models "$MODELS" --digest sha256:xyz
expect_refused "digest with build" "cannot be combined with --build" up --models "$MODELS" --digest "$D" --build
echo short > "$WORK/shortkey"
expect_refused "short key" "at least 24 characters" up --models "$MODELS" --api-key-file "$WORK/shortkey"
expect_refused "unreadable key file" "is not readable" up --models "$MODELS" --api-key-file "$WORK/nokey"
expect_refused "bad tag" "is not a usable image tag" up --models "$MODELS" --tag 'v1;rm'
STUB_ARCH=aarch64 expect_refused "arm64 host" "amd64 (x86_64) only" up --models "$MODELS"
STUB_NVIDIA_RC=9 expect_refused "nvidia-smi fails" "failed to list a GPU" up --models "$MODELS"
STUB_DAEMON_DOWN=1 expect_refused "daemon down" "did not answer 'docker info'" up --models "$MODELS"
mv "$STUB/nvidia-ctk" "$WORK/nvidia-ctk.off"
expect_refused "no container toolkit" "NVIDIA Container Toolkit is not installed" up --models "$MODELS"
STUB_RUNTIMES='{"nvidia":{},"runc":{}}' run up --models "$MODELS"
[ "$RC" = 0 ] && ok "toolkit found through docker's nvidia runtime instead" || { ng "runtime fallback rc=$RC"; cat "$OUT"; }
mv "$WORK/nvidia-ctk.off" "$STUB/nvidia-ctk"
reset_state
mv "$STUB/nvidia-smi" "$WORK/nvidia-smi.off"
expect_refused "no NVIDIA driver" "no NVIDIA driver" up --models "$MODELS"
mv "$WORK/nvidia-smi.off" "$STUB/nvidia-smi"
mv "$STUB/docker" "$WORK/docker.off"
expect_refused "no docker" "docker is not installed" up --models "$MODELS"
mv "$WORK/docker.off" "$STUB/docker"

echo "== down / status"
reset_state
run up --models "$MODELS" --api-key-file "$KEYF"
run status
[ "$RC" = 0 ] && grep -q "af-comfyui-lan-proxy: running" "$OUT" && ok "status names both containers" || { ng "status"; cat "$OUT"; }
run down
expect_writes "down removes proxy, comfy, network" "docker rm -f af-comfyui-lan-proxy
docker rm -f af-comfyui-lan
docker network rm af-comfyui-lan"
run down
expect_writes "down again is a no-op" ""
[ "$RC" = 0 ] && ok "down again exits 0" || ng "down again rc=$RC"
run status
[ "$RC" = 3 ] && ok "status of nothing exits 3" || ng "status rc=$RC"
[ -d "$MODELS/checkpoints" ] && ok "down leaves the models folder" || ng "models folder gone"

echo
echo "comfyui-lan stub test: $pass passed, $fail failed"
[ "$fail" = 0 ]
